package gates

import (
	"context"
	"fmt"

	"github.com/nightgauge/nightgauge/internal/state"
)

// FeatureValidateGate verifies the post-conditions of feature-validate by
// reading the gate-metrics.jsonl emitted by the validate stage. Any record
// with Result == "catch" is a quality-gate failure and trips this gate.
//
// Unlike the per-stage skill output, the gate-metrics file is the canonical
// signal for build/lint/test results — it is what the existing scheduler
// already consumes via state.ReadGateMetricsForIssue.
type FeatureValidateGate struct{}

// Name implements StageGate.
func (FeatureValidateGate) Name() string { return "feature-validate" }

// Verify implements StageGate.
func (FeatureValidateGate) Verify(_ context.Context, issueNumber int, workspace string) GateResult {
	return timedKind("feature-validate", func() (bool, string, []string, Kind) {
		results, err := state.ReadGateMetricsForIssue(workspace, issueNumber)
		if err != nil {
			return false, "failed to read gate-metrics.jsonl", []string{err.Error()}, KindFail
		}
		if len(results) == 0 {
			// validate skill said success but never wrote any quality-gate
			// records — no-op (the skill skipped the work).
			return false, "no quality-gate results recorded", []string{
				"feature-validate skill did not emit any gate-metrics records",
			}, KindNoOp
		}
		var failed []string
		for _, r := range results {
			if r.Result != "pass" {
				failed = append(failed, fmt.Sprintf("%s=%s", r.GateName, r.Result))
			}
		}
		if len(failed) > 0 {
			// Real quality-gate failure — work happened and produced a failing result.
			return false, "quality gates did not all pass", failed, KindFail
		}
		return true, "all quality gates passed", []string{
			fmt.Sprintf("gates=%d", len(results)),
		}, KindOK
	})
}
