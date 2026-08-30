package gates

import (
	"context"
	"fmt"
	"time"

	"github.com/nightgauge/nightgauge/internal/ci"
	"github.com/nightgauge/nightgauge/internal/deliverable"
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
	return timedKindTerminal("feature-validate", func() (bool, string, []string, Kind, string) {
		// #1182: run the deliverable-schema policy over validate-{N}.json before
		// anything reads it. `validate-340` and `validate-232` shipped gate
		// metrics with no `gate_name` behind nothing but a warning; a nameless
		// row can neither be counted in a per-gate tally nor excluded from one,
		// so the policy drops it and marks `gate_metrics` untrustworthy inside
		// the deliverable. Tolerating a mismatch and silently forwarding it are
		// different things — this is the difference.
		validatePolicy, policyErr := deliverable.ApplyPolicyToFile(
			"validate", contextFilePath(workspace, "validate", issueNumber), time.Now())
		if policyErr == nil && !validatePolicy.OK() {
			evidence := append([]string{}, validatePolicy.Summary()...)
			return false, "validate context does not match the expected schema and no total repair exists",
				evidence, KindFail, TerminalKindValidationError
		}

		results, err := state.ReadGateMetricsForIssue(workspace, issueNumber)
		if err != nil {
			return false, "failed to read gate-metrics.jsonl", []string{err.Error()}, KindFail, ""
		}
		if len(results) == 0 {
			// validate skill said success but never wrote any quality-gate
			// records — no-op (the skill skipped the work).
			return false, "no quality-gate results recorded", []string{
				"feature-validate skill did not emit any gate-metrics records",
			}, KindNoOp, ""
		}
		var failed []string
		for _, r := range results {
			if r.Result != "pass" {
				failed = append(failed, fmt.Sprintf("%s=%s", r.GateName, r.Result))
			}
		}
		if len(failed) > 0 {
			// Real quality-gate failure — work happened and produced a failing result.
			return false, "quality gates did not all pass", failed, KindFail, TerminalKindValidationFailed
		}

		details := []string{fmt.Sprintf("gates=%d", len(results))}
		details = append(details, validatePolicy.Summary()...)
		if summary := markUnexercisedDeliverable(issueNumber, workspace); summary != "" {
			details = append(details, summary)
		}
		return true, "all quality gates passed", details, KindOK, ""
	})
}

// markUnexercisedDeliverable re-derives the validation verdict from the change's
// own file set and the per-tier `ran` flags, superseding `passed` with
// `passed_unverified` when the run built a test suite it never executed (#152).
//
// It runs here, after the quality gates have already passed, because that is
// the only moment when the artifact is complete and the branch is still
// checked out. It returns a one-line summary for the gate details, or "" when
// there is nothing to report.
//
// Every failure path is silent and non-blocking. This check exists to stop a
// run from claiming more than it did — it must never become a new way for a
// run to die, or the next person to hit it will route around it, which is how
// the pipeline acquired the self-granted exemption in the first place.
func markUnexercisedDeliverable(issueNumber int, workspace string) string {
	doc, err := deliverable.ReadValidateContext(workspace, issueNumber)
	if err != nil {
		return ""
	}
	changed := ci.ChangedFilesAgainstDefaultBase(workspace)
	if len(changed) == 0 {
		// No computable diff means no evidence, and no evidence must not read
		// as an accusation.
		return ""
	}

	finding := deliverable.Derive(doc, changed)
	if !deliverable.Apply(doc, finding) {
		return ""
	}
	if err := deliverable.WriteValidateContext(workspace, issueNumber, doc); err != nil {
		return ""
	}
	return finding.Summary()
}
