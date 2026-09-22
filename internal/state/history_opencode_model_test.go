package state

import (
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
)

// history_opencode_model_test.go pins #1646's calibration acceptance
// criterion: the per-stage model recorded for an OpenCode LOCAL run is its
// ADR-022 local id ("<endpoint-id>/<model-id>"), never a registry band, so
// flagship band calibration cells (tokens.per_stage[*].model, read by the
// per-(stage, model) calibration loop — see V2StageTokens.Model's own
// comment) never absorb a local sample under a shared band name.
//
// The scheduler never has a "band" to write for an OpenCode dispatch in the
// first place: OpenCodeModelArg (execution/adapters/opencode.go) refuses to
// build the -m flag from anything that is not already "<provider>/<model>",
// so runtime.RecordStageModel(stage, model) only ever receives the qualified
// local id for an opencode stage. This is the regression guard that keeps it
// that way — BuildV2Record must project snap.StageModels through unchanged,
// with no bandification step reintroduced between them.
func TestBuildV2Record_OpenCodeLocalModelWritesTheLocalID(t *testing.T) {
	const localID = "lm-studio/qwen/qwen3.8-27b"

	rs := NewRuntimeState("o/r", 1646, "item-1646", testRunID())
	rs.StartedAt = time.Now()
	rs.BeginStage(StageFeatureDev)
	rs.RecordStageAdapter(StageFeatureDev, "opencode")
	rs.RecordStageModel(StageFeatureDev, localID)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 100, Output: 200}, localID, "")

	hw := NewHistoryWriter(t.TempDir())
	rec := hw.BuildV2Record(rs.Snapshot(), true, "", V2RunInput{}, time.Now())

	got, ok := rec.Tokens.PerStage[string(StageFeatureDev)]
	if !ok {
		t.Fatal("feature-dev missing from tokens.per_stage")
	}
	if got.Model != localID {
		t.Errorf("tokens.per_stage[feature-dev].model = %q, want the ADR-022 local id %q", got.Model, localID)
	}

	stage, ok := rec.Stages[string(StageFeatureDev)]
	if !ok {
		t.Fatal("feature-dev missing from stages")
	}
	if stage.ModelSelection == nil || stage.ModelSelection.Model != localID {
		t.Errorf("stages[feature-dev].model_selection.model = %#v, want the ADR-022 local id %q", stage.ModelSelection, localID)
	}

	// The negative half: a band string in StageModels (the shape a stage
	// dispatched on a hosted Claude tier writes) must NOT read back as this
	// local id — proves the assertions above are not vacuously true.
	if got.Model == "sonnet" || got.Model == "haiku" || got.Model == "opus" || got.Model == "fable" {
		t.Fatalf("tokens.per_stage[feature-dev].model = %q, a band string leaked in where the local id belongs", got.Model)
	}
}

// TestBuildV2Record_OpenCodeLocalModelSurvivesTerminatingStageTokens is the
// same guarantee on the OTHER producer of tokens.per_stage: a stage that
// fails on its TERMINATING attempt (RecordTerminatingStageTokens, #146)
// rather than completing normally. The expensive failures are exactly the
// samples a calibration corpus must not silently omit or mislabel.
func TestBuildV2Record_OpenCodeLocalModelSurvivesTerminatingStageTokens(t *testing.T) {
	const localID = "ollama/qwen3-coder:30b"

	rs := NewRuntimeState("o/r", 1646, "item-1646", testRunID())
	rs.StartedAt = time.Now()
	rs.BeginStage(StageFeatureDev)
	rs.RecordStageAdapter(StageFeatureDev, "opencode")
	rs.RecordStageModel(StageFeatureDev, localID)
	rs.RecordTerminatingStageTokens(StageFeatureDev, 100, 200, 0, 0)
	rs.SetStageError(StageFeatureDev, "stall_kill: no output for 30m")

	hw := NewHistoryWriter(t.TempDir())
	rec := hw.BuildV2Record(rs.Snapshot(), false, "stall_kill", V2RunInput{}, time.Now())

	got, ok := rec.Tokens.PerStage[string(StageFeatureDev)]
	if !ok {
		t.Fatal("feature-dev missing from tokens.per_stage on a terminating-attempt record")
	}
	if got.Model != localID {
		t.Errorf("tokens.per_stage[feature-dev].model = %q, want the ADR-022 local id %q", got.Model, localID)
	}
}
