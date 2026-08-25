package execution

import (
	"path/filepath"
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

// #916. The content test is right for squash merges and wrong in one
// direction: once the base evolves a file the branch owns, the diff is
// non-empty again and a merged branch reads as unmerged forever. That is
// fail-closed and therefore safe — and it made a real branch vanish from this
// scan's report within an hour of shipping, which is the invisibility #912 was
// filed to end.

// evolveBaseOn commits a change to one of the branch's own files on main and
// pushes it, reproducing "a later PR touched a file this branch also owned".
func (f *sweepFixture) evolveBaseOn(file, content string) {
	f.t.Helper()
	writeFile(f.t, filepath.Join(f.root, file), content)
	run(f.t, f.root, "git", "add", ".")
	run(f.t, f.root, "git", "commit", "-m", "later work on "+file)
	run(f.t, f.root, "git", "push", "origin", "main")
	run(f.t, f.root, "git", "fetch", "origin")
}

func TestScanStrandedBranches_WithoutTheDoorTheReportGoesQuietWhenTheBaseMovesOn(t *testing.T) {
	// The bug, stated as a test. Not an aspiration — this is current, correct,
	// fail-closed behaviour, pinned so the door below is demonstrably what
	// changes it.
	f := newSweepFixture(t)
	f.strandBranch(916, "fix/916-stranded", "shared.txt")
	f.evolveBaseOn("shared.txt", "content of shared.txt\nand a later change\n")

	res, err := ScanStrandedBranches(StrandedBranchOptions{RepoRoot: f.root})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got := strandedNames(res); len(got) != 0 {
		t.Fatalf("expected the content test alone to lose the branch, got %v — fixture no longer reproduces #916", got)
	}
	if r := keptReason(res, "fix/916-stranded"); r != KeepUnmergedContent {
		t.Fatalf("kept for %q, want %q", r, KeepUnmergedContent)
	}
}

func TestScanStrandedBranches_TheDoorKeepsAMergedBranchVisibleAfterTheBaseMovesOn(t *testing.T) {
	f := newSweepFixture(t)
	f.strandBranch(916, "fix/916-stranded", "shared.txt")
	tip := strings.TrimSpace(gitOut(t, f.root, "rev-parse", "fix/916-stranded"))
	f.evolveBaseOn("shared.txt", "content of shared.txt\nand a later change\n")

	res, err := ScanStrandedBranches(StrandedBranchOptions{
		RepoRoot: f.root,
		// The forge says: this branch merged as a PR whose head was its tip.
		MergedPRLookup: func(branch string) (string, []string, bool) {
			if branch == "fix/916-stranded" {
				return tip, nil, true
			}
			return "", nil, false
		},
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got := strandedNames(res); len(got) != 1 || got[0] != "fix/916-stranded" {
		t.Fatalf("the door did not restore the branch to the report: %v (kept %+v)", got, res.Kept)
	}
}

func TestScanStrandedBranches_TheDoorNeverReportsUnmergedWork(t *testing.T) {
	// The direction that costs something. A door that says "not found" — no
	// PR, no auth, no network — must leave the content test's KEEP standing.
	// Every failure mode of the lookup lands here.
	f := newSweepFixture(t)
	wt := f.addWorktree(917, "feat/917-unlanded")
	f.commitIn(wt, "unlanded.txt", "work nobody has merged\n")
	run(t, f.root, "git", "worktree", "remove", wt)

	for name, lookup := range map[string]MergedPRLookup{
		"not found":       func(string) (string, []string, bool) { return "", nil, false },
		"empty head":      func(string) (string, []string, bool) { return "", nil, true },
		"unrelated head":  func(string) (string, []string, bool) { return "0000000000000000000000000000000000000000", nil, true },
		"wrong parents":   func(string) (string, []string, bool) { return "abc", []string{"def"}, true },
		"nil (no client)": nil,
	} {
		res, err := ScanStrandedBranches(StrandedBranchOptions{RepoRoot: f.root, MergedPRLookup: lookup})
		if err != nil {
			t.Fatalf("%s: scan: %v", name, err)
		}
		if got := strandedNames(res); len(got) != 0 {
			t.Errorf("%s: unmerged work reported as stranded: %v", name, got)
		}
	}
}

func TestScanStrandedBranches_TheDoorIsNotConsultedForBranchesThatPassTheContentTest(t *testing.T) {
	// Cost, stated as a property. The daemon runs this on every reconcile
	// cycle across every root, and #842 is an open epic about the API budget:
	// a door consulted for every branch would spend forge quota on every idle
	// tick. It is consulted only after the content test has already failed.
	f := newSweepFixture(t)
	f.strandBranch(918, "fix/918-clean-merge", "fix.txt")

	calls := 0
	res, err := ScanStrandedBranches(StrandedBranchOptions{
		RepoRoot: f.root,
		MergedPRLookup: func(string) (string, []string, bool) {
			calls++
			return "", nil, false
		},
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(res.Stranded) != 1 {
		t.Fatalf("fixture: expected the branch to pass the content test, got %+v", res)
	}
	if calls != 0 {
		t.Errorf("door consulted %d time(s) for a branch the content test already resolved", calls)
	}
}

func TestSweepMergedWorktrees_HandsItsOwnDoorToTheBranchScan(t *testing.T) {
	// Wiring, again: the sweep resolving worktree branches through the forge
	// while resolving stranded ones by content alone would report the two
	// halves of the same question by two different rules.
	f := newSweepFixture(t)
	f.strandBranch(919, "fix/919-stranded", "shared.txt")
	tip := strings.TrimSpace(gitOut(t, f.root, "rev-parse", "fix/919-stranded"))
	f.evolveBaseOn("shared.txt", "content of shared.txt\nand a later change\n")

	res, err := SweepMergedWorktrees(WorktreeSweepOptions{
		RepoRoot:               f.root,
		DryRun:                 true,
		ReportStrandedBranches: true,
		MergedPRLookup: func(branch string) (string, []string, bool) {
			return tip, nil, branch == "fix/919-stranded"
		},
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.StrandedBranches == nil {
		t.Fatal("no branch scan ran")
	}
	if got := strandedNames(*res.StrandedBranches); len(got) != 1 {
		t.Fatalf("the sweep's door did not reach the branch scan: %v", got)
	}
}
