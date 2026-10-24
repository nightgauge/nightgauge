package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// TestRecordValidationSkipped pins #1890's run-record requirement: a recovery
// resume that jumps past feature-validate leaves a validation_skipped anomaly
// on the run record, carried on the failing stage (which always has a stage
// detail in the V2 record; a skipped feature-validate may not).
func TestRecordValidationSkipped(t *testing.T) {
	stages := []state.PipelineStage{
		state.StageIssuePickup, state.StageFeatureDev, state.StageFeatureValidate,
		state.StagePRCreate, state.StagePRMerge,
	}
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		from     state.PipelineStage
		target   state.PipelineStage
		stages   []state.PipelineStage
		recorded bool
	}{
		{"killed feature-dev resumed at pr-create", state.StageFeatureDev, state.StagePRCreate, stages, true},
		{"killed feature-validate resumed at pr-create", state.StageFeatureValidate, state.StagePRCreate, stages, true},
		{"rewind back to feature-dev crosses nothing", state.StagePRCreate, state.StageFeatureDev, stages, false},
		{"resume to feature-validate itself still validates", state.StageFeatureDev, state.StageFeatureValidate, stages, false},
		{"pipeline without feature-validate", state.StageFeatureDev, state.StagePRCreate,
			[]state.PipelineStage{state.StageFeatureDev, state.StagePRCreate}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rt := state.NewRuntimeState("o/r", 1890, "item", "run-1")
			_, ok := recordValidationSkipped(rt, c.stages, c.from, c.target,
				"abandoned-commit-recoverable", "runner punted", now)
			if ok != c.recorded {
				t.Fatalf("recorded = %v, want %v", ok, c.recorded)
			}
			got := rt.StageAnomaliesFor(c.from)
			if !c.recorded {
				if len(got) != 0 {
					t.Fatalf("unexpected anomalies: %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Kind != anomalyValidationSkipped {
				t.Fatalf("anomalies = %+v, want one %s", got, anomalyValidationSkipped)
			}
			if !strings.Contains(got[0].Detail, "no quality-gate results") {
				t.Errorf("detail %q does not say validation recorded nothing", got[0].Detail)
			}
		})
	}
}
