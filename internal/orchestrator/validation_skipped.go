package orchestrator

import (
	"fmt"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// anomalyValidationSkipped is the anomaly kind a recovery resume writes when
// it rewinds the pipeline forward past feature-validate (#1890).
const anomalyValidationSkipped = "validation_skipped"

// recordValidationSkipped persists a validation_skipped anomaly on the
// resuming stage (the one that failed, so it always has a stage detail in the
// run record to carry it — a skipped feature-validate may have none) when a recovery resume jumps from a stage at or
// before feature-validate to a stage after it — today abandoned-commit-
// recoverable's killed-stage resume at pr-create (#191). That resume is
// correct for a killed stage, but the run then reaches pr-create with no
// quality-gate results, and before #1890 the only trace was a mid-run log
// line: the run finished green and nothing a watcher reads said validation
// never happened. Writing it onto the run record makes "this run verified
// nothing" survive into the terminal record.
//
// Returns the detail and true when an anomaly was recorded; false when the
// resume does not cross feature-validate or the pipeline has no such stage.
func recordValidationSkipped(rt *state.RuntimeState, stages []state.PipelineStage,
	from, target state.PipelineStage, action, reason string, now time.Time) (string, bool) {
	if rt == nil {
		return "", false
	}
	fromIdx, targetIdx, validateIdx := -1, -1, -1
	for i, st := range stages {
		switch st {
		case from:
			fromIdx = i
		case target:
			targetIdx = i
		}
		if st == state.StageFeatureValidate {
			validateIdx = i
		}
	}
	if validateIdx < 0 || fromIdx < 0 || targetIdx < 0 {
		return "", false
	}
	if fromIdx > validateIdx || targetIdx <= validateIdx {
		return "", false
	}
	detail := fmt.Sprintf("recovery %s resumed %s → %s; feature-validate did not run to completion, so this run recorded no quality-gate results (%s)",
		action, from, target, reason)
	rt.AppendStageAnomaly(from, state.Anomaly{
		Kind:          anomalyValidationSkipped,
		Stage:         string(from),
		ExecutionPath: "deterministic",
		Detail:        detail,
		Timestamp:     now.UTC().Format(time.RFC3339),
	})
	return detail, true
}
