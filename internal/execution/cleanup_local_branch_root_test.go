package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// makeRepoWithBranch builds a repo on `main` plus a branch carrying one extra
// commit, and returns the repo root.
func makeRepoWithBranch(t *testing.T, branch string, withOwnCommit bool) string {
	t.Helper()
	dir := initTestGitRepo(t, "main")
	runGitCleanup(t, dir, "checkout", "-b", branch)
	if withOwnCommit {
		if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("unique"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitCleanup(t, dir, "add", ".")
		runGitCleanup(t, dir, "commit", "-m", "unique work")
	}
	runGitCleanup(t, dir, "checkout", "main")
	return dir
}

func runGitCleanup(t *testing.T, dir string, args ...string) {
	t.Helper()
	gittest.Run(t, dir, args...)
}

func branchExists(t *testing.T, repoRoot, branch string) bool {
	t.Helper()
	return strings.TrimSpace(gittest.Run(t, repoRoot, "branch", "--list", branch)) != ""
}

// TestCleanupLocalBranch_JudgesAndDeletesInTheSameRepo pins the defect #1020
// found: the safety check evaluated `m.repoRoot(repo)` while the delete ran in
// `m.workspaceRoot`.
//
// On any repo that is not the workspace root those are different directories,
// so the guard judged repository A and the deletion targeted repository B. A
// branch carrying unique work in B could be deleted because a same-named branch
// in A did not carry any — and the failure is silent, because the caller reads
// a nil error as "cleaned up".
func TestCleanupLocalBranch_JudgesAndDeletesInTheSameRepo(t *testing.T) {
	const branch = "feat/shipped"

	// The workspace root: a DIFFERENT repo, where the branch has no own commits
	// and would therefore pass the guard.
	workspace := makeRepoWithBranch(t, branch, false)
	// The run's actual repo: the same branch name, carrying unique work.
	target := makeRepoWithBranch(t, branch, true)

	m := &Manager{workspaceRoot: workspace}
	m.SetRepoPathResolver(func(string) string { return target })

	if err := m.CleanupLocalBranch("acme/target", branch); err != nil {
		t.Fatalf("CleanupLocalBranch: %v", err)
	}

	if !branchExists(t, target, branch) {
		t.Error("the branch carrying unique work was DELETED — the guard judged the " +
			"workspace-root repo while the delete ran in the target repo")
	}
}

// TestCleanupLocalBranch_DeletesInTheTargetRepo is the other half: when the
// branch genuinely has nothing unique, the delete must land in the run's repo
// rather than in the workspace root.
func TestCleanupLocalBranch_DeletesInTheTargetRepo(t *testing.T) {
	const branch = "feat/merged"

	workspace := makeRepoWithBranch(t, branch, true) // would be protected here
	target := makeRepoWithBranch(t, branch, false)   // safe to delete here

	m := &Manager{workspaceRoot: workspace}
	m.SetRepoPathResolver(func(string) string { return target })

	if err := m.CleanupLocalBranch("acme/target", branch); err != nil {
		t.Fatalf("CleanupLocalBranch: %v", err)
	}

	if branchExists(t, target, branch) {
		t.Error("the branch was not deleted in the run's own repo")
	}
	if !branchExists(t, workspace, branch) {
		t.Error("the workspace-root repo's same-named branch was touched — cleanup " +
			"must act only on the run's repo")
	}
}

// TestCleanupLocalBranch_ReportsARefusedDelete pins the swallowed failure.
// git refuses to delete a branch checked out in a worktree, and
// `_ = delLocal.Run()` made that invisible: the caller logged "cleaned up
// feature branch" for a branch that is still there.
func TestCleanupLocalBranch_ReportsARefusedDelete(t *testing.T) {
	const branch = "feat/held"
	repo := makeRepoWithBranch(t, branch, false)

	// Hold the branch in a worktree so git must refuse the delete.
	wt := filepath.Join(t.TempDir(), "held")
	gittest.Run(t, repo, "worktree", "add", wt, branch)

	m := &Manager{workspaceRoot: repo}

	// Still non-fatal — a branch left behind is not a pipeline failure.
	if err := m.CleanupLocalBranch("acme/target", branch); err != nil {
		t.Fatalf("a refused delete must stay non-fatal: %v", err)
	}
	if !branchExists(t, repo, branch) {
		t.Fatal("git should have refused to delete a branch held by a worktree")
	}
}

// TestBranchMergedIntoDefault_FetchesBeforeJudging pins the load-bearing half
// of #1020.
//
// The verdict is a content diff against origin/<default>, and the commit that
// makes a just-merged branch redundant is the SQUASH COMMIT THE FORGE JUST
// CREATED. Without a fetch that commit is invisible locally, so every freshly
// merged branch classifies as "unmerged content" and is kept — silently,
// because the caller reads `false` as a legitimate refusal.
//
// The fixture is the real shape: the merge happens on the upstream only, and
// the clone has never seen it.
func TestBranchMergedIntoDefault_FetchesBeforeJudging(t *testing.T) {
	const branch = "feat/shipped"

	upstream := initTestGitRepo(t, "main")
	gittest.Run(t, upstream, "config", "receive.denyCurrentBranch", "ignore")

	clone := filepath.Join(t.TempDir(), "clone")
	gittest.Run(t, filepath.Dir(clone), "clone", upstream, clone)
	gittest.Run(t, clone, "config", "user.email", "test@test")
	gittest.Run(t, clone, "config", "user.name", "test")

	// Work on a branch, push it.
	gittest.Run(t, clone, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(clone, "feature.txt"), []byte("shipped"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, clone, "add", ".")
	gittest.Run(t, clone, "commit", "-m", "the feature")
	gittest.Run(t, clone, "push", "-u", "origin", branch)
	gittest.Run(t, clone, "checkout", "main")

	// The forge squash-merges it — on the UPSTREAM only. The clone has not
	// fetched, so origin/main locally still predates the merge.
	gittest.Run(t, upstream, "merge", "--squash", branch)
	gittest.Run(t, upstream, "commit", "-m", "squash: the feature")

	m := &Manager{workspaceRoot: clone}

	if !m.branchMergedIntoDefault("acme/target", branch) {
		t.Error("a branch whose content IS on the upstream default was classified as unmerged.\n" +
			"Without a fetch the squash commit is invisible, so every freshly merged branch " +
			"is kept — and the caller reads that refusal as legitimate.")
	}
}
