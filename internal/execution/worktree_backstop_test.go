package execution

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runBackstop runs scripts/state-backstop.sh with HOME set to home.
func runBackstop(t *testing.T, home string, args ...string) (string, int) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "state-backstop.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run backstop: %v", err)
	}
	return string(out), 0
}

// TestStateBackstop_ExcusesPipelineWorktreeRemoval is the end-to-end contract
// of #2701: the real CleanupWorktree, between the gate's snapshot and verify,
// leaves the backstop green and names the removal; the same worktree removed
// by anything else still fails it.
func TestStateBackstop_ExcusesPipelineWorktreeRemoval(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("state-backstop.sh is exercised on Unix layouts only")
	}
	for _, tc := range []struct {
		name    string
		remove  func(m *Manager, path string) error
		wantRC  int
		wantOut string
	}{
		{"pipeline cleanup", func(m *Manager, _ string) error {
			return m.CleanupWorktree("nightgauge/nightgauge", 2701)
		}, 0, "(information) pipeline worktrees removed"},
		{"unrecorded removal", func(_ *Manager, p string) error {
			return os.RemoveAll(p)
		}, 1, "removed paths from the machine-state directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("NIGHTGAUGE_STATE_HOME", "")
			t.Setenv("XDG_STATE_HOME", "")
			repoRoot := initTestGitRepo(t, "main")
			m := &Manager{workspaceRoot: repoRoot}
			wt, err := m.ensureWorktree("nightgauge/nightgauge", 2701)
			if err != nil {
				t.Fatalf("ensureWorktree: %v", err)
			}
			if !strings.HasPrefix(wt, home) {
				t.Fatalf("worktree %s is not under the fake HOME %s", wt, home)
			}
			snap := t.TempDir()
			if out, rc := runBackstop(t, home, "snapshot", filepath.Join(snap, "before")); rc != 0 {
				t.Fatalf("snapshot: exit %d\n%s", rc, out)
			}
			if err := tc.remove(m, wt); err != nil {
				t.Fatalf("remove: %v", err)
			}
			if _, err := os.Stat(wt); !os.IsNotExist(err) {
				t.Fatalf("worktree still present after removal: %v", err)
			}
			out, rc := runBackstop(t, home, "verify", filepath.Join(snap, "before"), filepath.Join(snap, "after"))
			if rc != tc.wantRC {
				t.Fatalf("verify exit %d, want %d\n%s", rc, tc.wantRC, out)
			}
			if !strings.Contains(out, tc.wantOut) || !strings.Contains(out, filepath.Base(wt)) {
				t.Fatalf("verify output does not name %q and %s:\n%s", tc.wantOut, filepath.Base(wt), out)
			}
		})
	}
}
