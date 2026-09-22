package orchestrator

import "testing"

// TestShouldSkipBoardRevert_MergedCommitShaSkipsRevert is the #1969 Ask 2
// regression. #1650's run merged PR #1966 (verified MERGED, breadcrumb
// recorded), then spike-materialize failed downstream because the stage was
// unregistered in StageSkillDirs (Ask 1's own bug). The scheduler's failure
// handler still reverted Status to Ready even though the merge had already
// landed and the issue was closed — a terminal verdict contradicting
// observable forge state.
//
// A merge that landed must not be relitigated by a later stage's failure:
// once mergedCommitSha is non-empty, the revert must be skipped regardless of
// terminalFailureKind or workRecovered.
func TestShouldSkipBoardRevert_MergedCommitShaSkipsRevert(t *testing.T) {
	got := shouldSkipBoardRevert(false, "validation_error", "8b82c582abcdef")
	if !got {
		t.Fatalf("shouldSkipBoardRevert(false, %q, <non-empty sha>) = false, want true — "+
			"a merge the forge already confirmed must not be reverted by a later stage's failure",
			"validation_error")
	}
}

// TestShouldSkipBoardRevert_NoMergeStillReverts is the false-positive guard:
// an ordinary failure with no merge breadcrumb (the pr-merge stage itself
// never verified, or failed before it) must still revert exactly as before
// #1969 — the fifth condition widens skipBoardRevert, it must not swallow
// every failure.
func TestShouldSkipBoardRevert_NoMergeStillReverts(t *testing.T) {
	got := shouldSkipBoardRevert(false, "validation_error", "")
	if got {
		t.Fatalf("shouldSkipBoardRevert(false, %q, \"\") = true, want false — an ordinary failure "+
			"with no confirmed merge must still revert the board to Ready", "validation_error")
	}
}

// TestShouldSkipBoardRevert_PreexistingConditionsUnchanged pins the four
// conditions #1969 must not disturb: each was already sufficient on its own
// to skip the revert, with no merge breadcrumb involved.
func TestShouldSkipBoardRevert_PreexistingConditionsUnchanged(t *testing.T) {
	cases := []struct {
		name                string
		workRecovered       bool
		terminalFailureKind string
	}{
		{"workRecovered", true, "some_other_kind"},
		{"worktree_uncommitted", false, TerminalKindWorktreeUncommitted},
		{"budget_ceiling_hit", false, TerminalKindBudgetCeiling},
		{"branch_forked", false, TerminalKindBranchForked},
		{"commit_orphaned", false, TerminalKindCommitOrphaned},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !shouldSkipBoardRevert(tc.workRecovered, tc.terminalFailureKind, "") {
				t.Errorf("shouldSkipBoardRevert(%v, %q, \"\") = false, want true (pre-#1969 condition)",
					tc.workRecovered, tc.terminalFailureKind)
			}
		})
	}
}

// TestShouldSkipBoardRevert_OrdinaryFailureReverts is the base case none of
// the five conditions cover: a plain stage failure with no recovery, no
// special terminal kind, and no merge. It must still revert.
func TestShouldSkipBoardRevert_OrdinaryFailureReverts(t *testing.T) {
	if shouldSkipBoardRevert(false, "subagent_crash", "") {
		t.Fatalf("shouldSkipBoardRevert(false, %q, \"\") = true, want false", "subagent_crash")
	}
}
