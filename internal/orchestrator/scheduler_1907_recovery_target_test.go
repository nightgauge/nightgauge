// Tests covering Issue #1907 — a recovery commit must never land on the
// default branch or in the operator's primary checkout, and must leave an
// operator's unrelated uncommitted edits exactly as it found them.
package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := gittest.Command(dir, "rev-parse", ref).Output()
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
}

// TestRecoverUncommittedWork_RefusesPrimaryCheckoutOnDefaultBranch drives the
// observed shape: a run with no worktree of its own resolves to the operator's
// primary checkout on main, which carries planted unrelated edits. The rescue
// must refuse, name the dirty paths, commit nothing, and leave the edits dirty
// and byte-identical.
func TestRecoverUncommittedWork_RefusesPrimaryCheckoutOnDefaultBranch(t *testing.T) {
	primary := t.TempDir()
	gittest.Run(t, primary, "init")
	gittest.Run(t, primary, "checkout", "-b", "main")
	gittest.Run(t, primary, "config", "user.email", "test@nightgauge.dev")
	gittest.Run(t, primary, "config", "user.name", "Nightgauge Test")
	gittest.Run(t, primary, "config", "commit.gpgsign", "false")
	gitignore := filepath.Join(primary, ".gitignore")
	if err := os.WriteFile(gitignore, []byte("build/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, primary, "add", "-A")
	gittest.Run(t, primary, "commit", "-m", "seed")
	before := revParse(t, primary, "main")

	planted := []byte("build/\noperator-local/\n")
	if err := os.WriteFile(gitignore, planted, 0o644); err != nil {
		t.Fatal(err)
	}
	// The scheduler's fallback when no worktree is recorded is the workspace
	// root itself.
	worktreePath := loadWorktreePath(primary, 93)
	if !hasUncommittedWork(worktreePath) {
		t.Fatal("precondition: planted edit must read as uncommitted work")
	}

	_, err := RecoverUncommittedWork(worktreePath, 93, "issue-pickup")
	if err == nil {
		t.Fatal("RecoverUncommittedWork in the primary checkout = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), ".gitignore") {
		t.Errorf("refusal %q does not name the dirty path", err)
	}
	if got := revParse(t, primary, "main"); got != before {
		t.Errorf("main moved %s -> %s: a recovery commit landed on the default branch", before, got)
	}
	got, _ := os.ReadFile(gitignore)
	if string(got) != string(planted) {
		t.Errorf("planted edit modified: %q", got)
	}
	status, _ := gittest.Command(primary, "status", "--porcelain").Output()
	if !strings.Contains(string(status), " M .gitignore") {
		t.Errorf("planted edit no longer uncommitted/unstaged: %q", status)
	}
}

// TestRecoverUncommittedWork_RefusesLinkedWorktreeOnDefaultBranch pins the
// branch half on its own: even a linked worktree may not take a rescue commit
// while HEAD is the default branch or detached.
func TestRecoverUncommittedWork_RefusesLinkedWorktreeOnDefaultBranch(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		checkout   []string
	}{
		{"default branch", "default branch", []string{"checkout", "-B", "master"}},
		{"detached", "detached", []string{"checkout", "--detach"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			gitInitRepo(t, dir)
			gittest.Run(t, dir, tc.checkout...)
			head := revParse(t, dir, "HEAD")
			if err := os.WriteFile(filepath.Join(dir, "work.go"), []byte("package x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := RecoverUncommittedWork(dir, 1907, "feature-dev")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want refusal mentioning %q", err, tc.want)
			}
			if got := revParse(t, dir, "HEAD"); got != head {
				t.Errorf("HEAD moved %s -> %s", head, got)
			}
		})
	}
}
