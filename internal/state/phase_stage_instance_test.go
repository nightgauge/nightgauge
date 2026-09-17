package state

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
)

// Regression tests for #1850 — a green run that rendered as broken.
//
// The observed run exited 0 on every stage with an empty stageErrors, merged
// its PR and closed its issue, and still painted a red ✗ in the tree: pr-merge
// carried a `failed` freshness-check, and that record was stamped 1.3s BEFORE
// the stage instance it was being rendered against had started. Both come from
// the same place — phase records outliving the stage attempt that produced
// them — and both are asserted here as invariants of the record rather than as
// properties of any one stage's code path.

// TestGreenStageHasNoFailedPhase is the issue's third acceptance criterion: a
// stage with exitCode 0 and an empty StageErrors entry produces no phase with
// status "failed".
//
// Deliberately driven through the PUBLIC writers in the order production uses
// them, not by assembling a PhaseHistory literal. The bug was never in what a
// PhaseRecord can hold; it was in which writer got the last word.
func TestGreenStageHasNoFailedPhase(t *testing.T) {
	rs := NewRuntimeState("owner/repo", 42, "Phase telemetry", "run-1")
	rs.BeginStage(StagePRCreate)

	rs.BeginPhase(StagePRCreate, "load-context", 1, 14)
	rs.CompletePhase(StagePRCreate, "load-context")
	// write-context genuinely failed: pr-{N}.json did not land. The PR exists,
	// so the runner returns success-with-warning and the stage exits 0.
	rs.BeginPhase(StagePRCreate, "write-context", 12, 14)
	rs.FailPhase(StagePRCreate, "write-context", 12, 14)

	rs.CompleteStage(0, tokens.TokenCounts{}, "", "")

	snap := rs.Snapshot()
	if got := snap.StageErrors[string(StagePRCreate)]; got != "" {
		t.Fatalf("setup: stageErrors[pr-create] = %q, want empty", got)
	}
	for _, p := range snap.PhaseHistory {
		if p.Stage == StagePRCreate && p.Status == "failed" {
			t.Errorf("phase %q is %q on a stage that exited 0 with no stage error — "+
				"a phase verdict that contradicts its own stage's exit code is worse "+
				"than no phase verdict (#1850)", p.Name, p.Status)
		}
	}

	// The evidence must survive the downgrade. Erasing the record would trade
	// one dishonest reading for another.
	var wc *PhaseRecord
	for i := range snap.PhaseHistory {
		if snap.PhaseHistory[i].Name == "write-context" {
			wc = &snap.PhaseHistory[i]
		}
	}
	if wc == nil {
		t.Fatal("write-context was dropped from the record entirely")
	}
	if wc.Status != "degraded" {
		t.Errorf("write-context = %q, want degraded — it did not succeed and the "+
			"stage succeeded anyway", wc.Status)
	}
	if wc.CompletedAt == nil {
		t.Error("the downgraded record lost its CompletedAt; it is evidence of " +
			"something that happened, not a zero-width placeholder")
	}
}

// TestFailingStageKeepsItsFailedPhase is the other side of that invariant, and
// the one a careless fix breaks: on a stage that actually failed, the `failed`
// phase is the most useful record in the file — it says where the stage died.
func TestFailingStageKeepsItsFailedPhase(t *testing.T) {
	rs := NewRuntimeState("owner/repo", 42, "Phase telemetry", "run-1")
	rs.BeginStage(StagePRCreate)
	rs.BeginPhase(StagePRCreate, "create-pr", 9, 14)
	rs.FailPhase(StagePRCreate, "create-pr", 9, 14)
	rs.CompleteStage(1, tokens.TokenCounts{}, "", "")

	for _, p := range rs.Snapshot().PhaseHistory {
		if p.Name == "create-pr" && p.Status != "failed" {
			t.Errorf("create-pr = %q on a stage that exited 1, want failed — the "+
				"downgrade is only ever legal on a succeeding completion", p.Status)
		}
	}
}

// TestNoPhaseRecordPredatesItsStageInstance is the issue's fourth acceptance
// criterion.
//
// It reproduces the observed shape directly: pr-merge runs, punts inside
// freshness-check, and is then re-dispatched. Before #1850 the second attempt
// inherited the first one's verdicts — BeginStage un-booked the StageResult and
// left PhaseHistory untouched — so the stage's header described attempt 2 while
// its phase rows still described attempt 1, timestamps and all.
func TestNoPhaseRecordPredatesItsStageInstance(t *testing.T) {
	rs := NewRuntimeState("owner/repo", 42, "Phase telemetry", "run-1")

	// Attempt 1: gets as far as freshness-check and punts.
	rs.BeginStage(StagePRMerge)
	rs.BeginPhase(StagePRMerge, "read-pr-context", 0, 14)
	rs.CompletePhase(StagePRMerge, "read-pr-context")
	rs.BeginPhase(StagePRMerge, "freshness-check", 8, 14)
	rs.SupersedePhase(StagePRMerge, "freshness-check", 8, 14)
	rs.CompleteStage(0, tokens.TokenCounts{}, "", "")

	// Attempt 2: re-enters and merges.
	rs.BeginStage(StagePRMerge)
	rs.BeginPhase(StagePRMerge, "freshness-check", 8, 14)
	rs.CompletePhase(StagePRMerge, "freshness-check")
	rs.BeginPhase(StagePRMerge, "merge", 9, 14)
	rs.CompletePhase(StagePRMerge, "merge")
	rs.CompleteStage(0, tokens.TokenCounts{}, "", "")

	snap := rs.Snapshot()

	// The stage instance every consumer renders against is its CompletedStages
	// entry — "the stage's MOST RECENT attempt".
	var instance *StageResult
	for i := range snap.CompletedStages {
		if snap.CompletedStages[i].Stage == StagePRMerge {
			instance = &snap.CompletedStages[i]
		}
	}
	if instance == nil {
		t.Fatal("setup: pr-merge is not in CompletedStages")
	}

	for _, p := range snap.PhaseHistory {
		if p.Stage != StagePRMerge {
			continue
		}
		if p.StartedAt.Before(instance.StartedAt) {
			t.Errorf("phase %q started %s, before its stage instance started at %s "+
				"(%s earlier) — it belongs to a previous attempt and is being "+
				"rendered as this one's verdict (#1850)",
				p.Name, p.StartedAt, instance.StartedAt, instance.StartedAt.Sub(p.StartedAt))
		}
	}

	// Attempt 1 is not deleted, only relocated — the same MOVE contract
	// SupersededStages holds. "What did the first attempt get through before it
	// punted?" stays answerable.
	var supersededNames []string
	for _, p := range snap.SupersededPhaseHistory {
		supersededNames = append(supersededNames, p.Name)
	}
	if len(supersededNames) != 2 {
		t.Errorf("SupersededPhaseHistory = %v, want attempt 1's two records — a "+
			"displaced attempt is moved, never dropped", supersededNames)
	}
}

// TestReEntryLetsTheNewAttemptRecordItsOwnSkips covers the half of the
// attribution bug that is silent rather than visibly wrong.
//
// SkipPhase and UnreportedPhase are first-writer-wins on stage+name. With a
// previous attempt's records still in PhaseHistory they did not merely sit
// there — they OUTRANKED the new attempt, so attempt 2's honest record was
// discarded in favour of attempt 1's.
func TestReEntryLetsTheNewAttemptRecordItsOwnSkips(t *testing.T) {
	rs := NewRuntimeState("owner/repo", 42, "Phase telemetry", "run-1")

	rs.BeginStage(StagePRMerge)
	rs.UnreportedPhase(StagePRMerge, "merge", 9, 14)
	rs.CompleteStage(0, tokens.TokenCounts{}, "", "")

	rs.BeginStage(StagePRMerge)
	rs.BeginPhase(StagePRMerge, "merge", 9, 14)
	rs.CompletePhase(StagePRMerge, "merge")
	rs.CompleteStage(0, tokens.TokenCounts{}, "", "")

	// Asserted as "exactly one record, and it is complete" rather than "some
	// record is complete". Leaving the stale row in place ALSO yields a
	// complete record — BeginPhase appends and CompletePhase amends the new
	// one — so the looser assertion passes on the broken code and proves
	// nothing. What must not survive is attempt 1's `unreported` claim about a
	// phase attempt 2 demonstrably ran.
	var got []string
	for _, p := range rs.Snapshot().PhaseHistory {
		if p.Stage == StagePRMerge && p.Name == "merge" {
			got = append(got, p.Status)
		}
	}
	if len(got) != 1 || got[0] != "complete" {
		t.Errorf("merge records = %v, want exactly [complete] — the previous "+
			"attempt's back-fill must not outrank or shadow it", got)
	}
}
