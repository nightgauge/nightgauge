package ipc

import (
	"bytes"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/state"
)

// TestRunStage_EscalationLandsInEscalationHistory is the IPC half of #463.
//
// This runner is the escalation site for the extension path — the mode the
// product is actually operated in — and it used to call only
// RetryEngine.RecordEscalation, a counter in process memory that nothing
// persists. The run's own record therefore showed no escalation at all:
// AttemptsUntilSuccess undercounted it, model_selection.source attributed the
// stage to the scheduler as though nothing had substituted its model, and the
// "escalation" member of the vocabulary could not fire.
//
// TestRunStage_EscalationReachesTheNextDispatch (#340) already proves the
// escalation reaches the NEXT DISPATCH. That is a different claim from reaching
// the RECORD, and it stayed green through the whole life of this defect —
// which is exactly why this arm exists separately.
func TestRunStage_EscalationLandsInEscalationHistory(t *testing.T) {
	var buf bytes.Buffer
	runner, engine := newEscalatingStageRunner(&buf)

	runtime := state.NewRuntimeState("nightgauge/nightgauge", 463, "item-463", testRunID)
	runtime.BeginStage(state.StageFeatureDev)

	res := runStageWithResult(t, runner, orchestrator.StageRunParams{
		Stage:       state.StageFeatureDev,
		IssueNumber: 463,
		Repo:        "nightgauge/nightgauge",
		Model:       "sonnet",
		Timeout:     30 * time.Second,
		RunID:       testRunID,
		Runtime:     runtime,
	}, StageResultParams{
		Stage:       string(state.StageFeatureDev),
		IssueNumber: 463,
		Success:     false,
		ExitCode:    1,
		ErrorText:   "stage feature-dev exited 1",
	})

	if !res.EscalationRecorded {
		t.Fatal("EscalationRecorded = false — the fixture never reached the escalation branch, " +
			"so it observes nothing about #463")
	}

	snap := runtime.Snapshot()
	if len(snap.EscalationHistory) != 1 {
		t.Fatalf("EscalationHistory has %d entries, want 1 — the runner bumped the in-memory "+
			"retry counter and left no durable record, so this escalation is invisible to "+
			"AttemptsUntilSuccess and to model_selection attribution: %+v",
			len(snap.EscalationHistory), snap.EscalationHistory)
	}

	rec := snap.EscalationHistory[0]
	if rec.Stage != state.StageFeatureDev {
		t.Errorf("record stage = %q, want feature-dev", rec.Stage)
	}
	// From and To must be the real pair, not a placeholder: the whole reason
	// this append lives in the runner rather than in the scheduler's
	// EscalationRecorded branch is that only this site holds both.
	if rec.FromModel != "sonnet" {
		t.Errorf("record fromModel = %q, want sonnet (the model that failed)", rec.FromModel)
	}
	if want := engine.CurrentModel(string(state.StageFeatureDev)); rec.ToModel != want {
		t.Errorf("record toModel = %q, want %q — the tier the run actually escalated to",
			rec.ToModel, want)
	}
	if rec.Reason != state.EscalationReasonStageFailed {
		t.Errorf("record reason = %q, want %q", rec.Reason, state.EscalationReasonStageFailed)
	}
	if rec.At.IsZero() {
		t.Error("record At is the zero time — an escalation with no timestamp cannot be " +
			"ordered against the run's other events")
	}

	// THE DOWNSTREAM READING, on the real producer path. "Tries until green" is
	// what the calibration corpus learns difficulty from, and it is derived from
	// len(EscalationHistory) — so while this runner wrote nothing, an escalating
	// run reported 1 attempt instead of 2, under-reporting difficulty on exactly
	// the issues that were hardest. Asserting it here rather than only over a
	// hand-built snapshot is what ties the number to the code that produces it.
	if got := state.ComputeAttemptsUntilSuccess(nil, 0, len(snap.EscalationHistory)); got != 2 {
		t.Errorf("attempts-until-success reads %d for a run that escalated once, want 2", got)
	}
}

// TestRunStage_SuccessfulStageRecordsNoEscalation is the negative arm. Without
// it, an append that fired unconditionally — on every stage, escalating or not
// — would satisfy every assertion above while inflating AttemptsUntilSuccess
// for runs that never escalated at all.
func TestRunStage_SuccessfulStageRecordsNoEscalation(t *testing.T) {
	var buf bytes.Buffer
	runner, _ := newEscalatingStageRunner(&buf)

	runtime := state.NewRuntimeState("nightgauge/nightgauge", 463, "item-463-ok", testRunID)
	runtime.BeginStage(state.StageFeatureDev)

	runStageWithResult(t, runner, orchestrator.StageRunParams{
		Stage:       state.StageFeatureDev,
		IssueNumber: 463,
		Repo:        "nightgauge/nightgauge",
		Model:       "sonnet",
		Timeout:     30 * time.Second,
		RunID:       testRunID,
		Runtime:     runtime,
	}, StageResultParams{
		Stage:       string(state.StageFeatureDev),
		IssueNumber: 463,
		Success:     true,
		ExitCode:    0,
	})

	if got := len(runtime.Snapshot().EscalationHistory); got != 0 {
		t.Errorf("EscalationHistory has %d entries after a clean stage, want 0 — a run that "+
			"never escalated would report more attempts than it took", got)
	}
}
