package gates

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/ci"
	"github.com/nightgauge/nightgauge/internal/deliverable"
	"github.com/nightgauge/nightgauge/internal/state"
)

// FeatureValidateGate verifies the post-conditions of feature-validate by
// reading the gate-metrics.jsonl emitted by the validate stage. It judges the
// latest verdict per gate name, in file order (latestVerdicts): a verdict other
// than "pass" trips this gate unless a later verdict for that gate supersedes
// it, so a retry in a reused worktree can clear an earlier attempt's "catch"
// by re-running the gate (#2746). The "judges" gate is an aggregate of
// independent critics, so its verdict is the whole of its latest attempt: any
// non-pass row in that attempt trips this gate. An interrupted-judges marker
// stands until a later attempt, by any path, records a quality-gate verdict.
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
			return false, "failed to read gate-metrics.jsonl", []string{err.Error()}, KindFail, TerminalKindStageContextUnreadable
		}
		if len(results) == 0 {
			// validate skill said success but never wrote any quality-gate
			// records — no-op (the skill skipped the work).
			return false, "no quality-gate results recorded", []string{
				"feature-validate skill did not emit any gate-metrics records",
			}, KindNoOp, ""
		}
		verdicts := latestVerdicts(results)
		if len(verdicts) == 0 {
			// The skill wrote rows, but none of them carries a verdict. Work
			// was recorded and nothing vouches for it, which is a failure and
			// not a skipped stage.
			return false, "no quality-gate verdicts recorded", []string{
				fmt.Sprintf("%d gate-metrics row(s) carry no result", len(results)),
			}, KindFail, TerminalKindValidationFailed
		}
		var failed []string
		capped := false
		for _, r := range verdicts {
			if r.Result != "pass" {
				failed = append(failed, fmt.Sprintf("%s=%s", r.GateName, r.Result))
				capped = capped || isReviewCapRow(r)
			}
		}
		if capped {
			// #2865: the cap says something about this issue, so it parks here
			// rather than failing as a repository-halting validation_failed.
			return false, "the independent review reached its round cap", failed, KindFail, TerminalKindReviewCapReached
		}
		if len(failed) > 0 {
			// Real quality-gate failure — work happened and produced a failing result.
			return false, "quality gates did not all pass", failed, KindFail, TerminalKindValidationFailed
		}

		// #1261: a suite the configured test command structurally cannot run
		// has never been executed by anything, so "all quality gates passed"
		// is a statement about other code. This blocks, and it runs after the
		// quality gates so a real test failure — which is more urgent and more
		// actionable — is always the reported cause when both are true.
		te := checkTestExecution(issueNumber, workspace)
		if te.Blocked {
			return false, te.Reason, te.Evidence, KindFail, TerminalKindValidationFailed
		}

		details := []string{fmt.Sprintf("verdicts=%d rows=%d", len(verdicts), len(results))}
		details = append(details, te.Evidence...)
		details = append(details, validatePolicy.Summary()...)
		if summary := markUnexercisedDeliverable(issueNumber, workspace); summary != "" {
			details = append(details, summary)
		}
		return true, "all quality gates passed", details, KindOK, ""
	})
}

// ReviewGateName and ReviewCapMarker mirror review.GateName and review.CapMarker:
// internal/hooks imports this package and review's tests import hooks, so this
// package cannot import review. orchestrator's
// TestGateTerminalKindConstantsMirrorOrchestrator pins the pairs equal.
const (
	ReviewGateName  = "adversarial-review"
	ReviewCapMarker = "[review-cap-reached]"
)

// isReviewCapRow reports whether a row records a review stopped by its round cap
// (#2865). The result stays "catch"; the row's summary carries the marker.
func isReviewCapRow(r state.GateResult) bool {
	return r.GateName == ReviewGateName && strings.HasPrefix(r.ErrorSummary, ReviewCapMarker)
}

// latestVerdicts reduces gate-metric rows to the verdicts the gate judges: the
// last row per GateName, in file order, kept at that name's first position so
// the output is deterministic. Telemetry reads every row, so this view stays
// unexported and private to the gate (#2746).
//
// A row with an empty Result carries no verdict and supersedes nothing. A row
// with a Result but no GateName has no key to collapse on, so it stands alone
// and a nameless "pass" cannot clear a nameless "catch". File order decides,
// not Timestamp, because rows may lack one.
//
// Judge rows do not collapse by name: one run writes a row per judge under the
// shared name, so a later judge's pass must not erase an earlier judge's fail.
// Every row of the latest attempt stands, and an older attempt's rows drop out.
// A row with no AttemptID cannot be placed in an attempt, so it always stands,
// and a latest row with no AttemptID keeps every judge row. Doubt fails closed.
//
// An interrupted marker (a judges row whose ErrorSummary starts with
// state.JudgesInterruptedMarker) is released when any later row, in file
// order, carries a Result under a GateName that is neither empty nor judges.
// The SDK writes the marker after the interrupted attempt's own rows, so such
// a row comes from a later attempt, and every completion path writes those
// quality-gate rows whether or not it ran judges (#2880). A released marker
// takes part in neither the attempt selection nor the verdicts. Later judges
// rows do not release it: a partial run's judges must not clear it.
func latestVerdicts(results []state.GateResult) []state.GateResult {
	lastGateRow := -1
	for i, r := range results {
		if r.Result != "" && r.GateName != "" && r.GateName != state.JudgeGateName {
			lastGateRow = i
		}
	}
	released := func(i int, r state.GateResult) bool {
		return i < lastGateRow && r.GateName == state.JudgeGateName &&
			strings.HasPrefix(r.ErrorSummary, state.JudgesInterruptedMarker)
	}

	var judgeAttempt string
	for i, r := range results {
		if r.GateName == state.JudgeGateName && r.Result != "" && !released(i, r) {
			judgeAttempt = r.AttemptID
		}
	}

	verdicts := make([]state.GateResult, 0, len(results))
	position := make(map[string]int, len(results))
	for i, r := range results {
		if r.Result == "" || released(i, r) {
			continue
		}
		if r.GateName == state.JudgeGateName {
			if judgeAttempt == "" || r.AttemptID == "" || r.AttemptID == judgeAttempt {
				verdicts = append(verdicts, r)
			}
			continue
		}
		if r.GateName == "" {
			verdicts = append(verdicts, r)
			continue
		}
		if i, seen := position[r.GateName]; seen {
			verdicts[i] = r
			continue
		}
		position[r.GateName] = len(verdicts)
		verdicts = append(verdicts, r)
	}
	return verdicts
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
