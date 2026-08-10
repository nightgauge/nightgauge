package state

import (
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
)

// The StageErrors contract suite (#407).
//
// CONTRACT: a stage has an entry in StageErrors ⇔ that stage's MOST RECENT
// attempt failed (or the pipeline refused before the stage could attempt at
// all). SetStageError is the only writer; completeStageInternal (reached
// through CompleteStage / CompleteStageWithCost) is the only clear site, and it
// clears only when the completion SUCCEEDS and is actually booked.
//
// Before #407 there was no clear site at all in production — the map was
// write-only — so a stage that failed and then SUCCEEDED on retry stayed in
// StageErrors for the rest of the run while also sitting in CompletedStages.
// Both TS snapshot appliers apply stageErrors AFTER completedStages, so the
// recovered stage rendered "failed" forever, countFailedStages counted it, and
// a fully green run's outcome was downgraded to "Complete — 1 stage failed".
//
// RED-FIRST: every test below was run against an unfixed file via
// `go test -overlay`. The recovery cases fail against the pre-#407
// internal/state/runtime_state.go; the two placement guards
// (SuppressedDuplicateKeepsTheEntry, FailingCompletionDoesNotClear) fail
// against #407's first cut, which cleared unconditionally and ahead of the
// #230 guard.

// TestStageErrors_CompletionClearsTheRecoveredStage is the headline case: the
// failure-then-retry-then-success sequence the IPC and scheduler paths both
// produce, asserted on the object AND on the Snapshot() the UI actually reads.
func TestStageErrors_CompletionClearsTheRecoveredStage(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 407, "item-407", testRunID())

	// Attempt 1 — fails. The IPC "failed" branch books the spend first and
	// records the error second, which is the order reproduced here.
	rs.BeginStage(StageFeatureValidate)
	rs.CompleteStageWithCost(1, 3000, 900, 500, 0.21)
	rs.SetStageError(StageFeatureValidate, "exit 1: 2 tests failed")

	if rs.StageErrors[string(StageFeatureValidate)] == "" {
		t.Fatal("precondition: a failed attempt must record an entry")
	}

	// Attempt 2 — the retry succeeds.
	rs.BeginStage(StageFeatureValidate)
	rs.CompleteStageWithCost(0, 4000, 1100, 700, 0.28)

	if msg, ok := rs.StageErrors[string(StageFeatureValidate)]; ok {
		t.Errorf("a stage that completed still carries a StageErrors entry %q — "+
			"the entry means \"the LATEST attempt failed\", and the latest attempt succeeded", msg)
	}

	snap := rs.Snapshot()
	if _, ok := snap.StageErrors[string(StageFeatureValidate)]; ok {
		t.Errorf("Snapshot().StageErrors still carries the recovered stage: %+v — "+
			"this snapshot is what reaches the TS appliers, which apply stageErrors after completedStages",
			snap.StageErrors)
	}
	// The stage is genuinely in the completed list — twice, because both
	// attempts booked their own spend (history.go accumulates per stage).
	var completions int
	for _, sr := range snap.CompletedStages {
		if sr.Stage == StageFeatureValidate {
			completions++
		}
	}
	if completions != 2 {
		t.Errorf("completedStages entries for feature-validate = %d, want 2 "+
			"(the failed attempt's spend must not vanish with its error)", completions)
	}
}

// TestStageErrors_ClearIsScopedToTheCompletingStage guards the obvious
// over-correction: completing one stage must not wipe another stage's error.
func TestStageErrors_ClearIsScopedToTheCompletingStage(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 407, "item-407", testRunID())

	rs.BeginStage(StageFeaturePlanning)
	rs.SetStageError(StageFeaturePlanning, "planning timed out")

	rs.BeginStage(StageFeatureDev)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 100, Output: 50}, "")

	if got := rs.StageErrors[string(StageFeaturePlanning)]; got != "planning timed out" {
		t.Errorf("feature-planning's error = %q after feature-dev completed, want it untouched", got)
	}
}

// TestStageErrors_SuppressedDuplicateKeepsTheEntry pins the placement of the
// delete: AFTER the #230 idempotency guard.
//
// A completion that arrives with the same BeginStage-stamped StageStart as the
// previous one is a residual double-complete: the guard early-returns so the
// occurrence is booked exactly once and its tokens/cost are not double-counted.
// It books NOTHING, so it must retire nothing. Clearing ahead of the guard
// laundered a failure instead: the run was left holding one CompletedStages
// entry with ExitCode 1 and no StageErrors entry, and BuildV2Record never reads
// StageResult.ExitCode — it stamps "complete" for every CompletedStages entry
// and only a StageErrors entry flips it to "failed" — so the PERMANENT run
// record said the stage completed.
//
// Neither production writer can emit a retry that re-completes without an
// intervening BeginStage, which is why nothing here asserts the opposite: the
// Go scheduler re-runs BeginStage on every loop iteration, and the extension's
// markStageRunning latch is a per-invocation local so every attempt sends its
// own "running".
func TestStageErrors_SuppressedDuplicateKeepsTheEntry(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 407, "item-407", testRunID())

	rs.BeginStage(StageFeatureDev)
	rs.CompleteStageWithCost(1, 2000, 400, 0, 0.11)
	rs.SetStageError(StageFeatureDev, "exit 1: build failed")

	before := len(rs.CompletedStages)

	// Same StageStart — the guard suppresses the append.
	rs.CompleteStageWithCost(0, 2000, 400, 0, 0.11)

	if got := len(rs.CompletedStages); got != before {
		t.Fatalf("precondition: the duplicate completion must be deduped; CompletedStages went %d → %d", before, got)
	}
	if got := rs.StageErrors[string(StageFeatureDev)]; got != "exit 1: build failed" {
		t.Errorf("StageErrors[feature-dev] = %q after a SUPPRESSED completion, want the error kept — "+
			"a completion that books nothing must retire nothing", got)
	}

	// The durable record is the reason this matters: it is exit-code blind, so
	// the StageErrors entry is the sole carrier of "this stage failed".
	hw := NewHistoryWriter(t.TempDir())
	record := hw.BuildV2Record(rs, false, "", V2RunInput{}, time.Now())
	detail, ok := record.Stages[string(StageFeatureDev)]
	if !ok {
		t.Fatalf("feature-dev missing from the record's stages: %+v", record.Stages)
	}
	if detail.Status != "failed" || detail.Error != "exit 1: build failed" {
		t.Errorf("durable record says feature-dev is %q (error=%q), want \"failed\" with the recorded text — "+
			"the only booked occurrence exited 1", detail.Status, detail.Error)
	}
}

// TestStageErrors_FailingCompletionDoesNotClear is the other placement guard: a
// completion that BOOKS A FAILURE must not retire the entry it is about to
// replace.
//
// This is the retry path #407 is about. The Go scheduler books the stage's exit
// at the top of its post-run block, then emits pipeline.stateChanged and
// persists the runtime to disk, and only afterwards reaches the branches that
// call SetStageError. The state asserted below is exactly what that emit and
// that persist see. An unconditional clear made a just-failed second attempt
// broadcast and stored as complete-with-no-error, which both TS appliers render
// green and countFailedStages counts as zero failures — this issue's own
// silent-failure class, on this issue's own path.
func TestStageErrors_FailingCompletionDoesNotClear(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 407, "item-407", testRunID())

	// Attempt 1 fails and records its error.
	rs.BeginStage(StageFeatureValidate)
	rs.CompleteStageWithCost(1, 3000, 900, 500, 0.21)
	rs.SetStageError(StageFeatureValidate, "exit 1: 2 tests failed")

	// Attempt 2 fails too. Its SetStageError has not run yet.
	rs.BeginStage(StageFeatureValidate)
	rs.CompleteStageWithCost(1, 3200, 950, 600, 0.23)

	if got := rs.StageErrors[string(StageFeatureValidate)]; got != "exit 1: 2 tests failed" {
		t.Errorf("StageErrors[feature-validate] = %q in the window between the failing booking and "+
			"the new SetStageError, want the previous attempt's entry to survive", got)
	}

	snap := rs.Snapshot()
	if _, ok := snap.StageErrors[string(StageFeatureValidate)]; !ok {
		t.Errorf("the snapshot the scheduler emits and persists in that window carries no stageErrors "+
			"entry for a stage whose latest attempt just failed: %+v", snap.StageErrors)
	}

	// And the new attempt's own text lands when SetStageError finally runs.
	rs.SetStageError(StageFeatureValidate, "exit 1: 1 test failed")
	if got := rs.StageErrors[string(StageFeatureValidate)]; got != "exit 1: 1 test failed" {
		t.Errorf("StageErrors[feature-validate] = %q after the failure branch recorded, want the newest attempt's error", got)
	}
}

// TestStageErrors_CompleteThenFailKeepsTheEntry is the backtrack order, and the
// reason the TS appliers still apply stageErrors AFTER completedStages.
//
// A stage that completed earlier and was re-run later by the backtrack engine —
// and failed that time — must keep its entry. "Most recent attempt failed" is
// the contract; a clear-on-completion that outlived the later failure would
// render a genuinely broken stage green.
func TestStageErrors_CompleteThenFailKeepsTheEntry(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 407, "item-407", testRunID())

	rs.BeginStage(StageFeatureDev)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 5000, Output: 1200}, "")
	if _, ok := rs.StageErrors[string(StageFeatureDev)]; ok {
		t.Fatal("precondition: a clean completion records no error")
	}

	// Backtrack rewinds to feature-dev; this attempt fails.
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStageWithCost(1, 6000, 1500, 0, 0.4)
	rs.SetStageError(StageFeatureDev, "exit 1: regression introduced")

	if got := rs.StageErrors[string(StageFeatureDev)]; got != "exit 1: regression introduced" {
		t.Errorf("StageErrors[feature-dev] = %q, want the LATEST attempt's failure preserved", got)
	}
}

// TestStageErrors_RehydratePreservesUncompletedStages pins the deliberate
// non-change: crash-recovery rehydration keeps whatever the snapshot carried.
//
// The clear site is completion, and only completion. A reloaded run whose
// terminating stage never completes keeps its error (that is the whole point of
// the crash snapshot — see internal/ipc's "failed transition must PERSIST"
// regression), while a reloaded entry for a stage that goes on to complete is
// retired by that completion like any other.
func TestStageErrors_RehydratePreservesUncompletedStages(t *testing.T) {
	dir := t.TempDir()

	rs := NewRuntimeState("nightgauge/nightgauge", 407, "item-407", testRunID())
	rs.BeginStage(StageFeaturePlanning)
	rs.SetStageError(StageFeaturePlanning, "planning timed out")
	rs.BeginStage(StageFeatureDev)
	rs.SetStageError(StageFeatureDev, "host died mid-stage")
	if err := rs.Persist(dir); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	loaded, err := LoadPersistedState(dir, rs.RunID)
	if err != nil {
		t.Fatalf("LoadPersistedState: %v", err)
	}
	if got := loaded.StageErrors[string(StageFeaturePlanning)]; got != "planning timed out" {
		t.Errorf("rehydrated StageErrors[feature-planning] = %q, want it preserved", got)
	}
	if got := loaded.StageErrors[string(StageFeatureDev)]; got != "host died mid-stage" {
		t.Errorf("rehydrated StageErrors[feature-dev] = %q, want it preserved", got)
	}

	// Resuming the run: feature-dev is re-run and succeeds. Its rehydrated
	// entry is retired by that completion; the stage that never re-ran keeps
	// its own.
	loaded.BeginStage(StageFeatureDev)
	loaded.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 300}, "")

	if msg, ok := loaded.StageErrors[string(StageFeatureDev)]; ok {
		t.Errorf("a rehydrated error survived the stage's successful re-run: %q", msg)
	}
	if got := loaded.StageErrors[string(StageFeaturePlanning)]; got != "planning timed out" {
		t.Errorf("a rehydrated error for a stage that never re-ran was lost: %q", got)
	}
}
