package doctor

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitBinPath and bashBinPath are resolved once via the real PATH at package
// init, before any test clears PATH via isolateCascade — tests that shell
// out to git/bash as *helpers* (not as the resolution target under test)
// must not be broken by the very PATH-clearing they're testing.
var (
	gitBinPath  string
	bashBinPath string
)

func init() {
	gitBinPath, _ = exec.LookPath("git")
	bashBinPath, _ = exec.LookPath("bash")
}

// realPath resolves symlinks (e.g. macOS's /tmp -> /private/tmp) so paths
// built from t.TempDir() compare equal to the physical path `git
// rev-parse` reports. Falls back to the original path if resolution fails.
func realPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}

// writeFakeBinary creates an executable file at path with placeholder content.
func writeFakeBinary(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("failed to create parent dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho fake\n"), 0o755); err != nil {
		t.Fatalf("failed to write fake binary: %v", err)
	}
}

// isolateCascade clears every cascade input so only the step under test can
// resolve, then restores the environment/cwd on cleanup.
func isolateCascade(t *testing.T) {
	t.Helper()
	t.Setenv("NIGHTGAUGE_BIN", "")
	// PATH is narrowed to just git's directory (never "nightgauge" itself) —
	// both ResolveBinary's own git-based cascade steps and this test file's
	// git helper calls need `git` resolvable, while step 1 (PATH lookup of
	// "nightgauge") must still fail unless a test explicitly overrides PATH.
	safePath := ""
	if gitBinPath != "" {
		safePath = filepath.Dir(gitBinPath)
	}
	t.Setenv("PATH", safePath)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
}

func TestResolveBinary_EnvOverride(t *testing.T) {
	isolateCascade(t)

	fake := filepath.Join(t.TempDir(), "nightgauge")
	writeFakeBinary(t, fake)
	t.Setenv("NIGHTGAUGE_BIN", fake)

	resolved := ResolveBinary()
	if resolved.Path != fake {
		t.Errorf("expected path %q, got %q", fake, resolved.Path)
	}
	if resolved.Step != StepEnvOverride {
		t.Errorf("expected step %q, got %q", StepEnvOverride, resolved.Step)
	}
}

func TestResolveBinary_EnvOverride_StaleFallsThrough(t *testing.T) {
	isolateCascade(t)

	// Points at a non-existent file — must fall through to "unresolved"
	// since none of the other cascade steps can resolve either.
	t.Setenv("NIGHTGAUGE_BIN", filepath.Join(t.TempDir(), "does-not-exist"))

	resolved := ResolveBinary()
	if resolved.Path != "" {
		t.Errorf("expected stale NIGHTGAUGE_BIN to fall through, got path %q via step %q", resolved.Path, resolved.Step)
	}
}

func TestResolveBinary_Path(t *testing.T) {
	isolateCascade(t)

	dir := t.TempDir()
	fake := filepath.Join(dir, "nightgauge")
	writeFakeBinary(t, fake)
	t.Setenv("PATH", dir)

	resolved := ResolveBinary()
	if resolved.Step != StepPath {
		t.Errorf("expected step %q, got %q (path=%q)", StepPath, resolved.Step, resolved.Path)
	}
}

func TestResolveBinary_RepoBin(t *testing.T) {
	if gitBinPath == "" {
		t.Skip("git not available")
	}
	isolateCascade(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	writeFakeBinary(t, filepath.Join(repo, "bin", "nightgauge"))
	t.Chdir(repo)

	resolved := ResolveBinary()
	if resolved.Step != StepRepoBin {
		t.Errorf("expected step %q, got %q (path=%q)", StepRepoBin, resolved.Step, resolved.Path)
	}
}

func TestResolveBinary_CanonicalRepoBin(t *testing.T) {
	if gitBinPath == "" {
		t.Skip("git not available")
	}
	isolateCascade(t)

	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "commit", "--allow-empty", "-m", "init")

	worktreeDir := filepath.Join(t.TempDir(), "worktree")
	if out, err := exec.Command(gitBinPath, "-C", repo, "worktree", "add", "--detach", worktreeDir).CombinedOutput(); err != nil {
		t.Skipf("git worktree add failed (environment limitation): %v: %s", err, out)
	}

	writeFakeBinary(t, filepath.Join(repo, "bin", "nightgauge"))
	t.Chdir(worktreeDir)

	resolved := ResolveBinary()
	if resolved.Step != StepCanonicalRepoBin {
		t.Errorf("expected step %q, got %q (path=%q)", StepCanonicalRepoBin, resolved.Step, resolved.Path)
	}
}

func TestResolveBinary_VSCodeExtensionBundle(t *testing.T) {
	isolateCascade(t)

	home := os.Getenv("HOME")
	extDir := filepath.Join(home, ".vscode", "extensions", "nightgauge.nightgauge-vscode-0.1.0", "dist", "bin")
	writeFakeBinary(t, filepath.Join(extDir, "nightgauge"))

	resolved := ResolveBinary()
	if resolved.Step != StepVSCodeExtension {
		t.Errorf("expected step %q, got %q (path=%q)", StepVSCodeExtension, resolved.Step, resolved.Path)
	}
}

func TestResolveBinary_GoBin(t *testing.T) {
	isolateCascade(t)

	home := os.Getenv("HOME")
	writeFakeBinary(t, filepath.Join(home, "go", "bin", "nightgauge"))

	resolved := ResolveBinary()
	if resolved.Step != StepGoBin {
		t.Errorf("expected step %q, got %q (path=%q)", StepGoBin, resolved.Step, resolved.Path)
	}
}

func TestResolveBinary_Unresolved(t *testing.T) {
	isolateCascade(t)

	resolved := ResolveBinary()
	if resolved.Path != "" || resolved.Step != "" {
		t.Errorf("expected unresolved result, got path=%q step=%q", resolved.Path, resolved.Step)
	}
}

// TestResolveBinary_GuardShParity asserts guard.sh resolves to the same path
// as ResolveBinary() for each of the five filesystem-based cascade steps
// (#277 AC2). Skips gracefully when bash is unavailable.
func TestResolveBinary_GuardShParity(t *testing.T) {
	if bashBinPath == "" {
		t.Skip("bash not available")
	}
	if gitBinPath == "" {
		t.Skip("git not available")
	}

	repoRoot, err := gitRepoRootForTest()
	if err != nil {
		t.Skipf("could not locate repo root for guard.sh: %v", err)
	}
	guardPath := filepath.Join(repoRoot, "claude-plugins", "nightgauge", "hooks", "lib", "guard.sh")
	if _, err := os.Stat(guardPath); err != nil {
		t.Skipf("guard.sh not found at %q: %v", guardPath, err)
	}

	cases := []struct {
		name  string
		setup func(t *testing.T) string // returns expected path
	}{
		{
			name: "path",
			setup: func(t *testing.T) string {
				isolateCascade(t)
				dir := t.TempDir()
				fake := filepath.Join(dir, "nightgauge")
				writeFakeBinary(t, fake)
				t.Setenv("PATH", dir)
				return fake
			},
		},
		{
			name: "repo_bin",
			setup: func(t *testing.T) string {
				isolateCascade(t)
				repo := realPath(t, t.TempDir())
				runGit(t, repo, "init")
				fake := filepath.Join(repo, "bin", "nightgauge")
				writeFakeBinary(t, fake)
				t.Chdir(repo)
				return fake
			},
		},
		{
			name: "canonical_repo_bin",
			setup: func(t *testing.T) string {
				isolateCascade(t)
				repo := realPath(t, t.TempDir())
				runGit(t, repo, "init")
				runGit(t, repo, "config", "user.email", "test@example.com")
				runGit(t, repo, "config", "user.name", "Test")
				runGit(t, repo, "commit", "--allow-empty", "-m", "init")
				worktreeDir := filepath.Join(realPath(t, t.TempDir()), "worktree")
				if out, err := exec.Command(gitBinPath, "-C", repo, "worktree", "add", "--detach", worktreeDir).CombinedOutput(); err != nil {
					t.Skipf("git worktree add failed (environment limitation): %v: %s", err, out)
				}
				fake := filepath.Join(repo, "bin", "nightgauge")
				writeFakeBinary(t, fake)
				t.Chdir(worktreeDir)
				return fake
			},
		},
		{
			name: "vscode_extension",
			setup: func(t *testing.T) string {
				isolateCascade(t)
				home := os.Getenv("HOME")
				fake := filepath.Join(home, ".vscode", "extensions", "nightgauge.nightgauge-vscode-0.1.0", "dist", "bin", "nightgauge")
				writeFakeBinary(t, fake)
				return fake
			},
		},
		{
			name: "go_bin",
			setup: func(t *testing.T) string {
				isolateCascade(t)
				home := os.Getenv("HOME")
				fake := filepath.Join(home, "go", "bin", "nightgauge")
				writeFakeBinary(t, fake)
				return fake
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expected := tc.setup(t)

			goResolved := ResolveBinary()
			if goResolved.Path != expected {
				t.Fatalf("Go resolver: expected %q, got %q", expected, goResolved.Path)
			}

			cwd, err := os.Getwd()
			if err != nil {
				t.Fatalf("failed to get cwd: %v", err)
			}
			// guard.sh itself shells out to `git rev-parse` — give it a PATH
			// that includes git's directory (so its own cascade steps work)
			// in addition to whatever the test case configured (e.g. a temp
			// dir holding the fake "nightgauge" binary for the "path" case).
			shPath := filepath.Dir(gitBinPath) + string(os.PathListSeparator) + os.Getenv("PATH")
			cmd := exec.Command(bashBinPath, "-c", `source "$1"; echo "$NIGHTGAUGE_BINARY"`, "guard.sh", guardPath)
			cmd.Dir = cwd
			cmd.Env = append(os.Environ(),
				"NIGHTGAUGE_HOOK_BLOCKING=false",
				"HOME="+os.Getenv("HOME"),
				"PATH="+shPath,
				"NIGHTGAUGE_BIN="+os.Getenv("NIGHTGAUGE_BIN"),
			)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("guard.sh invocation failed: %v (stderr: %s)", err, stderr.String())
			}

			shResolved := trimTrailingNewline(stdout.String())
			if shResolved != expected {
				t.Errorf("guard.sh resolved %q, Go resolver resolved %q (expected %q)", shResolved, goResolved.Path, expected)
			}
		})
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(gitBinPath, append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v: %s", args, err, out)
	}
}

func gitRepoRootForTest() (string, error) {
	cmd := exec.Command(gitBinPath, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return trimTrailingNewline(string(out)), nil
}
