package hooks

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// readFileIfExists returns the file's contents and whether it existed; the
// silent-default test reads the side-channel log to assert the diagnostic
// landed there even though stderr was empty.
func readFileIfExists(t *testing.T, path string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b), true
}

// TestGuardShell exercises the claude-plugins/nightgauge/hooks/lib/guard.sh
// resolution chain across the conditions that triggered the #3234 incident:
//
//   - missing binary, default policy → graceful skip (exit 0, warning)
//   - missing binary, announce / gate policies → TestGuardShellMissingBinaryPolicy
//   - canonical-repo binary visible from inside a worktree → resolves correctly
//   - PATH-resolvable binary takes precedence
//
// The shell script is executed via bash so we exercise the actual code that
// runs on the user's machine, not a Go reimplementation.
//
// @see Issue #3234 — Stop hook hard-fails on missing binary in worktree mode.
func TestGuardShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guard.sh is bash-only")
	}

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	guardPath := filepath.Join(repoRoot, "claude-plugins", "nightgauge", "hooks", "lib", "guard.sh")
	if _, err := os.Stat(guardPath); err != nil {
		t.Fatalf("guard.sh not found at %s: %v", guardPath, err)
	}

	t.Run("missing binary, non-blocking + verbose → exit 0 with skip warning on stderr", func(t *testing.T) {
		// Verbose mode (NIGHTGAUGE_HOOK_SILENT=false) restores the pre-#3262
		// behavior — stderr carries the [hook-skipped] line for users who are
		// debugging hooks. The silent-default subtest below covers the new
		// default.
		stdout, stderr, code := runGuard(t, guardPath, map[string]string{
			"PATH":                   "/nonexistent",
			"HOME":                   t.TempDir(),
			"NIGHTGAUGE_HOOK_SILENT": "false",
		}, t.TempDir())
		if code != 0 {
			t.Errorf("expected exit 0 (graceful skip), got %d. stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !strings.Contains(stderr, "[hook-skipped]") {
			t.Errorf("expected '[hook-skipped]' warning, got stderr=%q", stderr)
		}
	})

	t.Run("missing binary, default silent → empty stderr, side-channel log captures", func(t *testing.T) {
		// #3262: silent-by-default is the new contract. The Claude CLI surfaces
		// hook stderr to the parent agent as a stop-hook-error notification
		// regardless of exit code, so the graceful-skip path MUST NOT emit
		// stderr in default mode. The diagnostic still has to land somewhere
		// the user can find it — the side-channel log file.
		home := t.TempDir()
		logFile := filepath.Join(home, "hook-warnings.log")
		stdout, stderr, code := runGuard(t, guardPath, map[string]string{
			"PATH":                "/nonexistent",
			"HOME":                home,
			"NIGHTGAUGE_HOOK_LOG": logFile,
		}, t.TempDir())
		if code != 0 {
			t.Errorf("expected exit 0 (silent graceful skip), got %d. stdout=%q stderr=%q", code, stdout, stderr)
		}
		if strings.Contains(stderr, "[hook-skipped]") {
			t.Errorf("silent default must not emit [hook-skipped] to stderr (would surface as stop-hook-error notification per #3262), got stderr=%q", stderr)
		}
		if strings.TrimSpace(stderr) != "" {
			t.Errorf("silent default must produce empty stderr, got stderr=%q", stderr)
		}
		logContent, ok := readFileIfExists(t, logFile)
		if !ok {
			t.Fatalf("side-channel log %s was not created", logFile)
		}
		if !strings.Contains(logContent, "[hook-skipped]") {
			t.Errorf("expected '[hook-skipped]' in side-channel log, got %q", logContent)
		}
		if !strings.Contains(logContent, "nightgauge binary not found") {
			t.Errorf("expected 'binary not found' diagnostic in side-channel log, got %q", logContent)
		}
	})

	t.Run("default policy is non-blocking and silent", func(t *testing.T) {
		// Neither NIGHTGAUGE_HOOK_ON_MISSING_BINARY nor NIGHTGAUGE_HOOK_SILENT
		// is set — guard.sh defaults to skip + silent (#3262). The
		// test asserts both: graceful exit 0 AND empty stderr (so no
		// stop-hook-error notification surfaces to the parent agent).
		stdout, stderr, code := runGuard(t, guardPath, map[string]string{
			"PATH": "/nonexistent",
			"HOME": t.TempDir(),
		}, t.TempDir())
		if code != 0 {
			t.Errorf("expected exit 0 (default = graceful skip), got %d. stdout=%q stderr=%q", code, stdout, stderr)
		}
		if strings.TrimSpace(stderr) != "" {
			t.Errorf("default mode must produce empty stderr (silent by default per #3262), got stderr=%q", stderr)
		}
	})

	t.Run("canonical repo binary is found from inside a worktree", func(t *testing.T) {
		// Build a fake repo with a worktree subdir, drop a fake binary in
		// canonical_repo/bin/nightgauge, source guard.sh from inside the
		// worktree subdir. The canonical binary resolves only in a verified
		// core checkout with the development setting on (#2758).
		fakeRepo := t.TempDir()
		runShell(t, fakeRepo, "git init -q && git config user.email t@t && git config user.name t && git commit --allow-empty -qm init")
		runShell(t, fakeRepo, "mkdir -p .worktrees/fake-issue && git worktree add -q -b fake-branch .worktrees/fake-issue HEAD 2>/dev/null || git worktree add -q .worktrees/fake-issue HEAD")
		runShell(t, fakeRepo, "mkdir -p bin && printf '#!/bin/bash\necho fake-binary\n' > bin/nightgauge && chmod +x bin/nightgauge")
		worktreeDir := filepath.Join(fakeRepo, ".worktrees", "fake-issue")

		// Setting off, and setting on in an unverified repository: never resolved.
		unverified := []map[string]string{
			{"PATH": "/nonexistent", "HOME": t.TempDir()},
			{"PATH": "/nonexistent", "HOME": t.TempDir(), "NIGHTGAUGE_DEV_REPO_BIN": "1"},
		}
		for _, env := range unverified {
			if out, _, _ := runGuard(t, guardPath, env, worktreeDir); strings.Contains(out, filepath.Join(fakeRepo, "bin", "nightgauge")) {
				t.Errorf("unverified canonical bin resolved with env %v: stdout=%q", env, out)
			}
		}

		runShell(t, fakeRepo, "printf 'module github.com/nightgauge/nightgauge\\n' > go.mod && git remote add origin https://github.com/nightgauge/nightgauge.git")
		stdout, stderr, code := runGuard(t, guardPath, map[string]string{
			"PATH":                    "/nonexistent",
			"HOME":                    t.TempDir(),
			"NIGHTGAUGE_DEV_REPO_BIN": "1",
		}, worktreeDir)
		if code != 0 {
			t.Errorf("expected exit 0 (canonical resolved), got %d. stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, filepath.Join(fakeRepo, "bin", "nightgauge")) {
			t.Errorf("expected canonical bin path printed, got stdout=%q", stdout)
		}
	})

	t.Run("PATH-resolvable binary takes precedence", func(t *testing.T) {
		fakeBin := t.TempDir()
		bin := filepath.Join(fakeBin, "nightgauge")
		if err := os.WriteFile(bin, []byte("#!/bin/bash\necho path-binary\n"), 0755); err != nil {
			t.Fatal(err)
		}
		stdout, _, code := runGuard(t, guardPath, map[string]string{
			"PATH": fakeBin,
			"HOME": t.TempDir(),
		}, t.TempDir())
		if code != 0 {
			t.Errorf("expected exit 0, got %d. stdout=%q", code, stdout)
		}
		if !strings.Contains(stdout, bin) {
			t.Errorf("expected PATH-bin path %q, got stdout=%q", bin, stdout)
		}
	})
}

// runGuard sources guard.sh and prints $NIGHTGAUGE_BINARY. cwd controls
// where guard.sh runs from (lets us simulate worktree vs canonical repo).
//
// The test always appends the system git location to PATH so guard.sh's
// `git rev-parse` calls work. Tests that want to suppress PATH-based binary
// resolution still get nothing matching `nightgauge` from the system
// PATH (only git lives there).
func runGuard(t *testing.T, guardPath string, env map[string]string, cwd string) (stdout, stderr string, exitCode int) {
	t.Helper()
	gitPath := lookupGitDir(t)
	// Append common system paths for git, mkdir, date, dirname — guard.sh uses
	// all four. Tests still suppress PATH-based binary resolution because
	// `nightgauge` is not present in /bin or /usr/bin on a clean system.
	systemPaths := gitPath + ":/bin:/usr/bin"
	if existing, ok := env["PATH"]; ok && existing != "" {
		env["PATH"] = existing + ":" + systemPaths
	} else {
		env["PATH"] = systemPaths
	}
	cmd := exec.Command("bash", "-c", `source "`+guardPath+`" && echo "$NIGHTGAUGE_BINARY"`)
	cmd.Dir = cwd
	cmd.Env = []string{}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		return outBuf.String(), errBuf.String(), exitErr.ExitCode()
	}
	if err != nil {
		t.Fatalf("guard.sh execution error: %v", err)
	}
	return outBuf.String(), errBuf.String(), 0
}

// runShell runs an arbitrary bash command in a directory, failing the test on
// non-zero exit. Used to set up fake repo / worktree fixtures.
func runShell(t *testing.T, cwd, script string) {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shell setup failed (cwd=%s): %v\noutput: %s", cwd, err, out)
	}
}

// lookupGitDir returns the directory containing the system `git` binary so
// the test can include it in PATH without dragging the rest of the system
// PATH along.
func lookupGitDir(t *testing.T) string {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git not found on system PATH: %v", err)
	}
	return filepath.Dir(gitBin)
}

// findRepoRoot walks up from the test's directory until it finds the repo's
// claude-plugins directory. Used because go test's cwd is the package dir.
func findRepoRoot() (string, error) {
	pwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := pwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "claude-plugins")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

// runHookWrapper runs a real plugin wrapper script with no binary resolvable
// (empty PATH apart from system tools, empty HOME) from cwd, and returns its
// stdout, stderr, exit code and side-channel log.
func runHookWrapper(t *testing.T, repoRoot, wrapper, cwd string, extraEnv map[string]string) (stdout, stderr string, code int, log string) {
	t.Helper()
	logFile := filepath.Join(t.TempDir(), "hook-warnings.log")
	env := map[string]string{
		"HOME":                t.TempDir(),
		"PATH":                lookupGitDir(t) + ":/bin:/usr/bin",
		"NIGHTGAUGE_HOOK_LOG": logFile,
	}
	for k, v := range extraEnv {
		env[k] = v
	}
	cmd := exec.Command(hookBash(t), filepath.Join(repoRoot, "claude-plugins", "nightgauge", "hooks", wrapper))
	cmd.Dir = cwd
	cmd.Stdin = strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"ls"}}`)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%s failed to run: %v", wrapper, err)
		}
		code = exitErr.ExitCode()
	}
	log, _ = readFileIfExists(t, logFile)
	return outBuf.String(), errBuf.String(), code, log
}

// TestGuardShellMissingBinaryPolicy pins #2765: with no binary, the session
// gets ONE visible warning (SessionStart binary-check.sh), the safety gates
// deny in a repository that opted in and stay out of the way elsewhere, and
// nothing ever reaches stderr or exits non-zero (#3262).
func TestGuardShellMissingBinaryPolicy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guard.sh is bash-only")
	}
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}

	optedIn := gittestRepo(t)
	optIn(t, optedIn)
	notOptedIn := gittestRepo(t)

	t.Run("binary-check announces once with a systemMessage", func(t *testing.T) {
		stdout, stderr, code, log := runHookWrapper(t, repoRoot, "binary-check.sh", notOptedIn, nil)
		if code != 0 || strings.TrimSpace(stderr) != "" {
			t.Fatalf("announce must exit 0 with empty stderr, got code=%d stderr=%q", code, stderr)
		}
		lines := strings.Split(strings.TrimSpace(stdout), "\n")
		if len(lines) != 1 {
			t.Fatalf("want exactly one stdout line, got %q", stdout)
		}
		var msg struct {
			SystemMessage string `json:"systemMessage"`
		}
		if err := json.Unmarshal([]byte(lines[0]), &msg); err != nil {
			t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
		}
		if !strings.Contains(msg.SystemMessage, "NOT enforcing") || !strings.Contains(msg.SystemMessage, "go install") {
			t.Errorf("systemMessage must say the gates are off and how to install, got %q", msg.SystemMessage)
		}
		if !strings.Contains(log, "[hook-skipped]") {
			t.Errorf("announce should also leave a side-channel line, got %q", log)
		}
	})

	t.Run("binary-check is silent when the binary resolves", func(t *testing.T) {
		fakeBin := t.TempDir()
		if err := os.WriteFile(filepath.Join(fakeBin, "nightgauge"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code, _ := runHookWrapper(t, repoRoot, "binary-check.sh", notOptedIn,
			map[string]string{"PATH": fakeBin + ":" + lookupGitDir(t) + ":/bin:/usr/bin"})
		if code != 0 || stdout != "" || strings.TrimSpace(stderr) != "" {
			t.Fatalf("a resolved binary must produce no output, got code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})

	for _, wrapper := range []string{"workflow-gate.sh", "prompt-sanitize.sh"} {
		t.Run(wrapper+" denies in an opted-in repository", func(t *testing.T) {
			stdout, stderr, code, log := runHookWrapper(t, repoRoot, wrapper, optedIn, nil)
			if code != 0 || strings.TrimSpace(stderr) != "" {
				t.Fatalf("gate must deny by decision (exit 0, no stderr), got code=%d stderr=%q", code, stderr)
			}
			var out struct {
				HookSpecificOutput struct {
					HookEventName            string `json:"hookEventName"`
					PermissionDecision       string `json:"permissionDecision"`
					PermissionDecisionReason string `json:"permissionDecisionReason"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &out); err != nil {
				t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout)
			}
			d := out.HookSpecificOutput
			if d.HookEventName != "PreToolUse" || d.PermissionDecision != "deny" || d.PermissionDecisionReason == "" {
				t.Fatalf("want a PreToolUse deny with a reason, got %+v", d)
			}
			if !strings.Contains(log, "[hook-denied]") {
				t.Errorf("want a [hook-denied] side-channel line, got %q", log)
			}
		})

		t.Run(wrapper+" skips in a repository that did not opt in", func(t *testing.T) {
			stdout, stderr, code, log := runHookWrapper(t, repoRoot, wrapper, notOptedIn, nil)
			if code != 0 || stdout != "" || strings.TrimSpace(stderr) != "" {
				t.Fatalf("want a silent skip, got code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if !strings.Contains(log, "[hook-skipped]") {
				t.Errorf("want a [hook-skipped] side-channel line, got %q", log)
			}
		})
	}

	t.Run("gate denies from a worktree of an opted-in canonical checkout", func(t *testing.T) {
		repo := gittestRepo(t)
		gittest.Run(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-qm", "init")
		wt := filepath.Join(t.TempDir(), "wt")
		gittest.Run(t, repo, "worktree", "add", "-q", wt, "HEAD")
		optIn(t, repo) // uncommitted: present only in the canonical checkout
		stdout, _, code, _ := runHookWrapper(t, repoRoot, "workflow-gate.sh", wt, nil)
		if code != 0 || !strings.Contains(stdout, `"permissionDecision":"deny"`) {
			t.Fatalf("worktree of an opted-in checkout must be gated, got code=%d stdout=%q", code, stdout)
		}
	})

	t.Run("stop-verification never blocks without the binary", func(t *testing.T) {
		stdout, stderr, code, _ := runHookWrapper(t, repoRoot, "stop-verification.sh", optedIn, nil)
		if code != 0 || stdout != "" || strings.TrimSpace(stderr) != "" {
			t.Fatalf("stop-verification must skip (blocking a stop would trap the session), got code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})
}

// TestGuardShellUserShim pins step 3a (#2765): ~/.nightgauge/bin/nightgauge
// resolves ahead of the VS Code bundle glob and behind PATH.
func TestGuardShellUserShim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guard.sh is bash-only")
	}
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	guardPath := filepath.Join(repoRoot, "claude-plugins", "nightgauge", "hooks", "lib", "guard.sh")

	writeExe := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	shim := filepath.Join(home, ".nightgauge", "bin", "nightgauge")
	writeExe(shim)
	writeExe(filepath.Join(home, ".vscode", "extensions", "nightgauge.nightgauge-0.8.0", "dist", "bin", "nightgauge"))

	stdout, _, code := runGuard(t, guardPath, map[string]string{"PATH": "/nonexistent", "HOME": home}, t.TempDir())
	if code != 0 || strings.TrimSpace(stdout) != shim {
		t.Fatalf("shim must win over the VS Code bundle, got code=%d stdout=%q", code, stdout)
	}

	pathDir := t.TempDir()
	onPath := filepath.Join(pathDir, "nightgauge")
	writeExe(onPath)
	stdout, _, _ = runGuard(t, guardPath, map[string]string{"PATH": pathDir, "HOME": home}, t.TempDir())
	if strings.TrimSpace(stdout) != onPath {
		t.Fatalf("PATH must win over the shim, got stdout=%q", stdout)
	}
}
