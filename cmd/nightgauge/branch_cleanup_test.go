package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/nightgauge/nightgauge/internal/git"
)

func TestGitBranchCleanupCmd_HelpListsDocsAndChore(t *testing.T) {
	cmd := gitBranchCleanupCmd()
	long := cmd.Long
	for _, prefix := range []string{"docs/", "chore/", "feat/", "epic/"} {
		if !strings.Contains(long, prefix) {
			t.Errorf("branch-cleanup help must mention %s:\n%s", prefix, long)
		}
	}
}

func TestGitBranchCleanup_PrefixAllowlist(t *testing.T) {
	// CLI contract: GIT_WORKFLOW prefixes + epic/; never operator wip/.
	tests := []struct {
		branch string
		want   bool
	}{
		{branch: "docs/583-example", want: true},
		{branch: "chore/583-example", want: true},
		{branch: "feat/583-thing", want: true},
		{branch: "wip/583-example", want: false},
		{branch: "feat/no-number", want: false},
	}
	for _, tt := range tests {
		if got := gitpkg.IsCleanupCandidate(tt.branch); got != tt.want {
			t.Errorf("IsCleanupCandidate(%q) = %v, want %v", tt.branch, got, tt.want)
		}
	}
}

// #593 — cleanupClosedIssueBranch classification. -----------------------

// branchCleanupFixture builds an origin+clone git pair with an initial commit
// on main, ready for a branch to be created and cleaned up against.
func branchCleanupFixture(t *testing.T) (svc *gitpkg.Service, root string) {
	t.Helper()
	base := t.TempDir()
	origin := filepath.Join(base, "origin.git")
	clone := filepath.Join(base, "clone")

	gitIn(t, base, "init", "--bare", "-b", "main", origin)
	seed := filepath.Join(base, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, seed, "init", "-b", "main")
	gitIn(t, seed, "config", "user.email", "test@test")
	gitIn(t, seed, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(seed, "README"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, seed, "add", ".")
	gitIn(t, seed, "commit", "-m", "initial")
	gitIn(t, seed, "remote", "add", "origin", origin)
	gitIn(t, seed, "push", "-u", "origin", "main")

	gitIn(t, base, "clone", origin, clone)
	gitIn(t, clone, "config", "user.email", "test@test")
	gitIn(t, clone, "config", "user.name", "test")

	resolved, err := filepath.EvalSymlinks(clone)
	if err != nil {
		t.Fatalf("resolve clone path: %v", err)
	}

	svc, err = gitpkg.NewService(resolved)
	if err != nil {
		t.Fatalf("open service: %v", err)
	}
	return svc, resolved
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v: %s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

func TestCleanupClosedIssueBranch_DeletesLocalAndRemote(t *testing.T) {
	svc, root := branchCleanupFixture(t)
	branch := "fix/601-thing"
	gitIn(t, root, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(root, "fix.txt"), []byte("fixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-m", "work")
	gitIn(t, root, "push", "-u", "origin", branch)
	gitIn(t, root, "checkout", "main")

	action, reason, err := cleanupClosedIssueBranch(svc, branch)
	if err != nil {
		t.Fatalf("cleanupClosedIssueBranch: %v", err)
	}
	if action != "deleted" {
		t.Fatalf("action = %q (reason=%q), want %q", action, reason, "deleted")
	}
	if local := strings.TrimSpace(gitIn(t, root, "branch", "--list", branch)); local != "" {
		t.Errorf("local branch survived: %q", local)
	}
	if remote := strings.TrimSpace(gitIn(t, root, "ls-remote", "--heads", "origin", branch)); remote != "" {
		t.Errorf("remote branch survived: %q", remote)
	}
}

func TestCleanupClosedIssueBranch_AbsentRemoteIsSuccess(t *testing.T) {
	// #593 live evidence: BranchDeleteRemote can fail with a misleading
	// go-git transport error even when the remote branch is already gone (or,
	// as here, never existed). The desired end state — no remote branch —
	// already holds, so this must report "deleted", never "error".
	svc, root := branchCleanupFixture(t)
	branch := "fix/602-thing"
	gitIn(t, root, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(root, "fix.txt"), []byte("fixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-m", "work")
	gitIn(t, root, "checkout", "main")
	// Deliberately never pushed — the remote branch never existed.

	action, reason, err := cleanupClosedIssueBranch(svc, branch)
	if err != nil {
		t.Fatalf("cleanupClosedIssueBranch: %v", err)
	}
	if action != "deleted" {
		t.Fatalf("action = %q (reason=%q), want %q", action, reason, "deleted")
	}
	if local := strings.TrimSpace(gitIn(t, root, "branch", "--list", branch)); local != "" {
		t.Errorf("local branch survived: %q", local)
	}
}

func TestCleanupClosedIssueBranch_SkipsWorktreeHeldBranch(t *testing.T) {
	svc, root := branchCleanupFixture(t)
	branch := "fix/603-thing"
	wt := filepath.Join(root, ".worktrees", "issue-603")
	gitIn(t, root, "worktree", "add", wt, "-b", branch, "main")

	action, reason, err := cleanupClosedIssueBranch(svc, branch)
	if err != nil {
		t.Fatalf("cleanupClosedIssueBranch: %v", err)
	}
	if action != "skipped" {
		t.Fatalf("action = %q (reason=%q), want %q", action, reason, "skipped")
	}
	if !strings.Contains(reason, "worktree") {
		t.Errorf("reason = %q, want it to name the worktree", reason)
	}
	if local := strings.TrimSpace(gitIn(t, root, "branch", "--list", branch)); local == "" {
		t.Error("a branch a worktree holds must survive cleanup")
	}
}

func TestCleanupClosedIssueBranch_RefusesProtectedBranch(t *testing.T) {
	svc, _ := branchCleanupFixture(t)
	action, _, err := cleanupClosedIssueBranch(svc, "main")
	if err == nil {
		t.Fatal("expected an error refusing to delete main")
	}
	if action != "error" {
		t.Errorf("action = %q, want %q", action, "error")
	}
}
