package execution

import (
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// TestBranchDeleteIsRefusedWhileItsWorktreeLives is the mechanism behind
// #1561, pinned so the scheduler's ordering has something to justify it.
//
// On the success path the run's OWN worktree still held the feature branch
// when the branch cleanup ran, so `git branch -D` was refused on EVERY
// successful run — after CleanupBranch had already deleted origin's copy
// unconditionally. The extension has always removed the worktree first and
// says why: "a live worktree blocks the branch delete, so order matters"
// (ConcurrentPipelineManager.cleanupSlot, #3969).
func TestBranchDeleteIsRefusedWhileItsWorktreeLives(t *testing.T) {
	const branch = "feat/held-by-its-own-worktree"
	repo := makeRepoWithBranch(t, branch, false)
	wt := filepath.Join(t.TempDir(), "run")
	gittest.Run(t, repo, "worktree", "add", wt, branch)

	m := &Manager{workspaceRoot: repo}

	deleted, err := m.CleanupLocalBranch("acme/target", branch)
	if err != nil {
		t.Fatalf("must stay non-fatal: %v", err)
	}
	if deleted {
		t.Fatal("git cannot delete a branch its worktree holds; reporting deleted=true is the #1561 defect")
	}
	if !branchExists(t, repo, branch) {
		t.Fatal("branch should still exist")
	}

	// Remove the worktree — the scheduler's new ordering — and the same call
	// now succeeds. This is the whole argument for the reorder.
	gittest.Run(t, repo, "worktree", "remove", "--force", wt)

	deleted, err = m.CleanupLocalBranch("acme/target", branch)
	if err != nil {
		t.Fatalf("CleanupLocalBranch after worktree removal: %v", err)
	}
	if !deleted {
		t.Fatal("with the worktree gone the delete must succeed — if it does not, the reorder buys nothing")
	}
	if branchExists(t, repo, branch) {
		t.Fatal("branch should be gone once its worktree is")
	}
}
