package execution

import (
	"path/filepath"
	"strings"
	"testing"
)

// newBranchCleanupManager returns a Manager whose RepoRoot resolves the given
// repo root directly, regardless of the "repo" slug passed in — mirrors how
// production code resolves via m.repoRoot(repo), but tests only ever operate
// on a single fixture root.
func newBranchCleanupManager(root string) *Manager {
	m := NewManager(root, nil)
	m.SetRepoPathResolver(func(string) string { return root })
	return m
}

func TestCleanupBranchIfMerged_DeletesMergedBranch(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(200, "fix/200-thing")
	f.commitIn(wt, "fix.txt", "fixed\n")
	f.squashMergeToMain("fix/200-thing")

	// Detach the worktree so the branch is deletable from the repo root.
	run(t, f.root, "git", "worktree", "remove", wt, "--force")

	m := newBranchCleanupManager(f.root)
	if err := m.CleanupBranchIfMerged("owner/repo", "fix/200-thing"); err != nil {
		t.Fatalf("CleanupBranchIfMerged: %v", err)
	}

	out, err := gitOutput(f.root, "branch", "--list", "fix/200-thing")
	if err != nil {
		t.Fatalf("git branch --list: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("expected merged branch fix/200-thing to be deleted, still present: %q", out)
	}
}

func TestCleanupBranchIfMerged_KeepsUnmergedBranch(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(201, "fix/201-thing")
	f.commitIn(wt, "fix.txt", "unmerged work\n")
	// Not merged into main — no squashMergeToMain call.
	run(t, f.root, "git", "worktree", "remove", wt, "--force")

	m := newBranchCleanupManager(f.root)
	if err := m.CleanupBranchIfMerged("owner/repo", "fix/201-thing"); err != nil {
		t.Fatalf("CleanupBranchIfMerged: %v", err)
	}

	out, err := gitOutput(f.root, "branch", "--list", "fix/201-thing")
	if err != nil {
		t.Fatalf("git branch --list: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("expected unmerged branch fix/201-thing to survive, but it was deleted")
	}
}

func TestCleanupBranchIfMerged_KeepsBranchWithNoOwnCommits(t *testing.T) {
	f := newSweepFixture(t)
	// A branch created at the tip of main with zero commits of its own: an
	// empty content diff that must NOT be mistaken for "already merged".
	run(t, f.root, "git", "branch", "fix/202-nocommits", "origin/main")

	m := newBranchCleanupManager(f.root)
	if err := m.CleanupBranchIfMerged("owner/repo", "fix/202-nocommits"); err != nil {
		t.Fatalf("CleanupBranchIfMerged: %v", err)
	}

	out, err := gitOutput(f.root, "branch", "--list", "fix/202-nocommits")
	if err != nil {
		t.Fatalf("git branch --list: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("expected no-commits branch fix/202-nocommits to survive, but it was deleted")
	}
}

func TestCleanupWorktree_PreservesDirtyWorktree(t *testing.T) {
	f := newSweepFixture(t)
	m := newBranchCleanupManager(f.root)

	// CleanupWorktree resolves its own path via m.worktreePath — create the
	// worktree there directly rather than via the fixture's ".worktrees/"
	// helper, which uses a different layout.
	wt := m.worktreePath("owner/repo", 203)
	run(t, f.root, "git", "worktree", "add", wt, "-b", "fix/203-thing", "origin/main")
	writeFile(t, filepath.Join(wt, "fix.txt"), "fixed\n")
	run(t, wt, "git", "add", ".")
	run(t, wt, "git", "commit", "-m", "work: fix.txt")
	// Leave an uncommitted change on top.
	writeFile(t, filepath.Join(wt, "dirty.txt"), "uncommitted\n")

	if err := m.CleanupWorktree("owner/repo", 203); err != nil {
		t.Fatalf("CleanupWorktree: %v", err)
	}

	list, err := gitOutput(f.root, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	if !strings.Contains(list, wt) {
		t.Fatalf("expected dirty worktree %s to be preserved, but it was removed", wt)
	}
}
