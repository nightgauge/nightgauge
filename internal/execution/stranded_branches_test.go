package execution

import (
	"strings"
	"testing"
)

// #912. Three squash-merged branches sat in the core repo while
// `worktree sweep --dry-run` reported "no reclaimable worktrees" and `doctor`
// reported "healthy". These tests drive real git for the same reason the
// sweep's do: the defect is state that exists only in a repository, and the
// classifier's single most dangerous mistake — calling a branch with live work
// on it merged — cannot be reproduced against a mock.

// strandBranch reproduces the exact production sequence: a pipeline worktree
// does work, the work squash-merges, and the WORKTREE is removed before any
// sweep runs. The branch survives with no worktree pointing at it, which is
// what puts it out of `git worktree list`'s reach permanently.
func (f *sweepFixture) strandBranch(issue int, branch string, file string) {
	f.t.Helper()
	wt := f.addWorktree(issue, branch)
	f.commitIn(wt, file, "content of "+file+"\n")
	f.squashMergeToMain(branch)
	run(f.t, f.root, "git", "worktree", "remove", wt)
}

func keptReason(res StrandedBranchScan, branch string) BranchKeepReason {
	for _, k := range res.Kept {
		if k.Name == branch {
			return k.Reason
		}
	}
	return ""
}

func strandedNames(res StrandedBranchScan) []string {
	names := make([]string, 0, len(res.Stranded))
	for _, b := range res.Stranded {
		names = append(names, b.Name)
	}
	return names
}

func TestScanStrandedBranches_ReportsASquashMergedBranchWithNoWorktree(t *testing.T) {
	f := newSweepFixture(t)
	f.strandBranch(912, "fix/912-stranded", "fix.txt")

	// Assert the trap is armed before testing it: ancestry reports this
	// branch as UNMERGED, so a classifier built on `merge-base --is-ancestor`
	// would find nothing here and look correct doing it.
	if isAncestor(t, f.root, "fix/912-stranded", "origin/main") {
		t.Fatal("fixture is wrong: squash-merged branch must not be an ancestor of origin/main")
	}

	res, err := ScanStrandedBranches(StrandedBranchOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got := strandedNames(res); len(got) != 1 || got[0] != "fix/912-stranded" {
		t.Fatalf("expected exactly fix/912-stranded stranded, got %v (kept %+v, errors %v)",
			got, res.Kept, res.Errors)
	}
	if res.Stranded[0].Tip == "" {
		t.Error("stranded branch reported without its tip SHA; the report is not actionable after a hand delete")
	}
	if res.BaseRef == "" {
		t.Error("scan did not echo the base ref it measured against")
	}
}

// The failure mode on this side of the classifier is a human deleting real
// work on the report's say-so, so every keep gets its own pin.

func TestScanStrandedBranches_KeepsAnUnmergedUnpushedBranch(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(913, "feat/913-in-flight")
	f.commitIn(wt, "unlanded.txt", "work nobody has merged\n")
	// Worktree gone, work NOT merged and never pushed — the branch is now in
	// exactly the same structural position as a stranded one, and only the
	// content test separates them.
	run(t, f.root, "git", "worktree", "remove", wt)

	res, err := ScanStrandedBranches(StrandedBranchOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got := strandedNames(res); len(got) != 0 {
		t.Fatalf("branch carrying unmerged work reported as stranded: %v", got)
	}
	if r := keptReason(res, "feat/913-in-flight"); r != KeepUnmergedContent {
		t.Errorf("kept for the wrong reason: got %q, want %q", r, KeepUnmergedContent)
	}
}

func TestScanStrandedBranches_KeepsABranchAWorktreeStillHolds(t *testing.T) {
	f := newSweepFixture(t)
	wt := f.addWorktree(914, "fix/914-live")
	f.commitIn(wt, "fix.txt", "fixed\n")
	f.squashMergeToMain("fix/914-live")
	// Worktree deliberately left in place: merged or not, this branch belongs
	// to the worktree sweep, and a run may still be writing in that directory.

	res, err := ScanStrandedBranches(StrandedBranchOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got := strandedNames(res); len(got) != 0 {
		t.Fatalf("branch held by a live worktree reported as stranded: %v", got)
	}
	if r := keptReason(res, "fix/914-live"); r != KeepHeldByWorktree {
		t.Errorf("kept for the wrong reason: got %q, want %q", r, KeepHeldByWorktree)
	}
}

func TestScanStrandedBranches_KeepsABranchWithNoCommitsOfItsOwn(t *testing.T) {
	f := newSweepFixture(t)
	run(t, f.root, "git", "branch", "feat/915-empty", "origin/main")

	res, err := ScanStrandedBranches(StrandedBranchOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got := strandedNames(res); len(got) != 0 {
		t.Fatalf("branch with no commits of its own reported as stranded: %v", got)
	}
	if r := keptReason(res, "feat/915-empty"); r != KeepNoOwnCommits {
		t.Errorf("kept for the wrong reason: got %q, want %q", r, KeepNoOwnCommits)
	}
}

func TestScanStrandedBranches_KeepsTheDefaultBranch(t *testing.T) {
	f := newSweepFixture(t)
	f.strandBranch(916, "fix/916-stranded", "fix.txt")

	res, err := ScanStrandedBranches(StrandedBranchOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, b := range res.Stranded {
		if b.Name == "main" {
			t.Fatal("the default branch was reported as stranded")
		}
	}
	if r := keptReason(res, "main"); r != KeepDefaultBranch {
		t.Errorf("main kept for the wrong reason: got %q, want %q", r, KeepDefaultBranch)
	}
	// The scan still found the real one — otherwise this test passes vacuously
	// on a classifier that reports nothing at all.
	if got := strandedNames(res); len(got) != 1 || got[0] != "fix/916-stranded" {
		t.Fatalf("expected the stranded branch alongside a kept main, got %v", got)
	}
}

// The scan is reachable from the sweep, and only when asked. Pinned because
// the guarantee lives in ONE assignment in SweepMergedWorktrees: the scanner's
// own tests above all pass while nothing calls it (#912, and the Unpinned
// Wiring class in docs/FAILURE_TAXONOMY.md).

func TestSweepMergedWorktrees_ReportsStrandedBranchesWhenAsked(t *testing.T) {
	f := newSweepFixture(t)
	f.strandBranch(917, "fix/917-stranded", "fix.txt")

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{
		RepoRoot:               f.root,
		DryRun:                 true,
		ReportStrandedBranches: true,
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.StrandedBranches == nil {
		t.Fatal("ReportStrandedBranches was set and the sweep reported no scan at all")
	}
	if got := strandedNames(*res.StrandedBranches); len(got) != 1 || got[0] != "fix/917-stranded" {
		t.Fatalf("sweep did not surface the stranded branch: %v", got)
	}
	// The whole point is that the WORKTREE pass sees nothing here.
	if len(res.Reclaimed) != 0 {
		t.Errorf("worktree pass claimed something to reclaim: %+v", res.Reclaimed)
	}
}

func TestSweepMergedWorktrees_LeavesStrandedBranchesAloneByDefault(t *testing.T) {
	f := newSweepFixture(t)
	f.strandBranch(918, "fix/918-stranded", "fix.txt")

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.StrandedBranches != nil {
		t.Error("the branch scan ran without being asked; the daemon's periodic sweep pays for it every cycle")
	}
	// REPORT-ONLY is the contract, and a non-dry-run sweep is where a deleting
	// implementation would show itself. The branch must survive.
	out := strings.TrimSpace(gitOut(t, f.root, "for-each-ref", "--format=%(refname:short)", "refs/heads/"))
	if !strings.Contains(out, "fix/918-stranded") {
		t.Fatalf("a live sweep deleted a stranded branch; the scan must never delete. branches: %q", out)
	}
}

// gitOut runs git in dir and fails the test on error. Distinct from the
// package's gitOutput, which returns the error to production callers.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitOutput(dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func isAncestor(t *testing.T, root, commit, ancestorOf string) bool {
	t.Helper()
	ok, err := mergeBaseIsAncestor(root, commit, ancestorOf)
	if err != nil {
		t.Fatalf("merge-base --is-ancestor: %v", err)
	}
	return ok
}
