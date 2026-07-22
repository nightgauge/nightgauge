package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
//   - missing binary, blocking policy → hard fail (exit 1)
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
			"PATH":                          "/nonexistent",
			"HOME":                          t.TempDir(),
			"NIGHTGAUGE_HOOK_BLOCKING": "false",
			"NIGHTGAUGE_HOOK_SILENT":   "false",
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
			"PATH":                          "/nonexistent",
			"HOME":                          home,
			"NIGHTGAUGE_HOOK_BLOCKING": "false",
			"NIGHTGAUGE_HOOK_LOG":      logFile,
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

	t.Run("missing binary, blocking → exit 1 with error", func(t *testing.T) {
		stdout, stderr, code := runGuard(t, guardPath, map[string]string{
			"PATH":                          "/nonexistent",
			"HOME":                          t.TempDir(),
			"NIGHTGAUGE_HOOK_BLOCKING": "true",
		}, t.TempDir())
		if code != 1 {
			t.Errorf("expected exit 1, got %d. stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !strings.Contains(stderr, "nightgauge binary not found") {
			t.Errorf("expected 'binary not found' error, got stderr=%q", stderr)
		}
	})

	t.Run("missing binary, blocking + silent → stderr stays loud (AC3)", func(t *testing.T) {
		// AC3 contract: load-bearing hooks (NIGHTGAUGE_HOOK_BLOCKING=true)
		// MUST keep stderr output even when silent mode is on. Silencing a
		// load-bearing-hook failure would hide a real problem from the user.
		// The side-channel log mirrors the message but does not replace stderr
		// for blocking failures.
		home := t.TempDir()
		logFile := filepath.Join(home, "hook-warnings.log")
		stdout, stderr, code := runGuard(t, guardPath, map[string]string{
			"PATH":                          "/nonexistent",
			"HOME":                          home,
			"NIGHTGAUGE_HOOK_BLOCKING": "true",
			"NIGHTGAUGE_HOOK_SILENT":   "true",
			"NIGHTGAUGE_HOOK_LOG":      logFile,
		}, t.TempDir())
		if code != 1 {
			t.Errorf("expected exit 1 (blocking failure), got %d. stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !strings.Contains(stderr, "nightgauge binary not found") {
			t.Errorf("blocking failures MUST emit stderr regardless of silent mode, got stderr=%q", stderr)
		}
		// Side-channel log mirrors the failure for completeness.
		if logContent, ok := readFileIfExists(t, logFile); ok {
			if !strings.Contains(logContent, "[hook-blocked]") {
				t.Errorf("expected '[hook-blocked]' mirror in side-channel log, got %q", logContent)
			}
		}
	})

	t.Run("default policy is non-blocking and silent", func(t *testing.T) {
		// Neither NIGHTGAUGE_HOOK_BLOCKING nor NIGHTGAUGE_HOOK_SILENT
		// is set — guard.sh defaults to non-blocking + silent (#3262). The
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
		// worktree subdir, assert it resolves to the canonical binary.
		fakeRepo := t.TempDir()
		runShell(t, fakeRepo, "git init -q && git config user.email t@t && git config user.name t && git commit --allow-empty -qm init")
		runShell(t, fakeRepo, "mkdir -p .worktrees/fake-issue && git worktree add -q -b fake-branch .worktrees/fake-issue HEAD 2>/dev/null || git worktree add -q .worktrees/fake-issue HEAD")
		runShell(t, fakeRepo, "mkdir -p bin && printf '#!/bin/bash\necho fake-binary\n' > bin/nightgauge && chmod +x bin/nightgauge")
		worktreeDir := filepath.Join(fakeRepo, ".worktrees", "fake-issue")

		stdout, stderr, code := runGuard(t, guardPath, map[string]string{
			"PATH": "/nonexistent",
			"HOME": t.TempDir(),
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
