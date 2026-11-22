package orchestrator

// A zero-change feature-dev on a partial-delivery re-run (#3163).
//
// stopNoDiffPartialRerun (#3160) only ends a re-run whose branch is level with
// its base. Every other zero-change feature-dev fell through to the gate's
// no-op verdict, `premature turn end`, and was booked as a failure that fed the
// cascade breaker and the per-issue lifetime cap. Session 99 lost the fleet to
// two of them: platform#1750, whose verdict was right (the criterion was
// already on main), and a downstream #1051, whose feature-dev ignored an
// operator-revised plan. Neither is a fault of the factory.
//
// decidePartialNoOp is the whole rule:
//   - a named PLAN_REVISION_NEEDED dispute ends the run as that dispute;
//   - an operator-revised plan that names files earns one more feature-dev lap
//     with the rule restated, so the operator's revision is not lost to one
//     model's re-reading of the remainder;
//   - a remainder JudgeRemainder finds all met on the earlier PR's merged diff
//     closes the issue as completed (#3423);
//   - anything else books not_pipeline_actionable: no retry, no cascade
//     strike, no lifetime-cap increment, the remainder and the unmet list left
//     on the issue.

import (
	"errors"
	"fmt"
	"strings"

	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"
	"github.com/nightgauge/nightgauge/internal/state"
)

type partialNoOpAction int

const (
	// partialNoOpNotScoped: not a partial re-run, so the gate's verdict stands.
	partialNoOpNotScoped partialNoOpAction = iota
	// partialNoOpRelap: re-dispatch feature-dev once with the rule restated.
	partialNoOpRelap
	// partialNoOpDispute: feature-dev named the plan step it disputes.
	partialNoOpDispute
	// partialNoOpBook: end the run as a non-failure no-op.
	partialNoOpBook
	// partialNoOpClose: every remaining criterion is met on the earlier PR's
	// merged diff, so the issue is closed as completed (#3423).
	partialNoOpClose
)

// PartialNoOpResult.Action values, shared with the extension's stage loop.
const (
	PartialNoOpActionNotScoped = "not_scoped"
	PartialNoOpActionRelap     = "relap"
	PartialNoOpActionDispute   = "dispute"
	PartialNoOpActionBook      = "book"
	PartialNoOpActionClose     = "close"
)

// partialNoOpSettlementError is the error of a pr-create no-diff settlement that
// left a partial-delivery re-run's issue open (#3487). It is the structured
// booking of that stop: the run loop books it and returns before any retry, and
// the exit-record writer classifies it without reading the text.
type partialNoOpSettlementError struct{ detail string }

func (e partialNoOpSettlementError) Error() string { return e.detail }

// isPartialNoOpSettlement reports whether err carries that booking. The error
// is returned by value, and only for a scoped run whose issue stayed open.
func isPartialNoOpSettlement(err error) bool {
	var settlement partialNoOpSettlementError
	return errors.As(err, &settlement)
}

type partialNoOpDecision struct {
	Action partialNoOpAction
	// Detail is the stage error and run-record detail for Dispute and Book.
	Detail string
	// Steer is the rule restated to feature-dev for Relap.
	Steer string
}

// decidePartialNoOp decides what a zero-change feature-dev means on a run
// scoped to scope. dispute is planRevisionDispute's result; relapped is true
// once this run has spent its one extra lap; remainder is JudgeRemainder's
// verdict on the scope. Only remainder decides a close: nothing a model wrote
// is read here.
func decidePartialNoOp(issue int, scope *state.PartialRerun, rev operatorRevision, dispute string, relapped bool, remainder RemainderVerdict) partialNoOpDecision {
	if scope == nil || scope.PR <= 0 {
		return partialNoOpDecision{Action: partialNoOpNotScoped}
	}
	if dispute != "" {
		return partialNoOpDecision{
			Action: partialNoOpDispute,
			Detail: fmt.Sprintf("[not-pipeline-actionable] feature-dev: partial-delivery re-run of #%d (after PR #%d) disputed the plan instead of changing it: %s; #%d waits for a human to revise the plan [#3163]",
				issue, scope.PR, dispute, issue),
		}
	}
	if rev.binding() && !relapped {
		files := append(append([]string{}, rev.FilesToModify...), rev.FilesToCreate...)
		return partialNoOpDecision{
			Action: partialNoOpRelap,
			Steer: fmt.Sprintf("The previous feature-dev lap ended with zero changes on an operator-revised plan. "+
				"The plan is binding: implement %s, or record a PLAN_REVISION_NEEDED signal whose evidence names "+
				"the plan step you dispute. Do not end with zero changes on your own reading of the remaining criteria.",
				strings.Join(files, ", ")),
		}
	}
	if remainder.AllMet && len(remainder.Criteria) > 0 {
		return partialNoOpDecision{Action: partialNoOpClose, Detail: PartialClosedDetail(issue, scope.PR, remainder)}
	}
	why := "wrote no changes"
	if rev.binding() {
		why = "wrote no changes twice on an operator-revised plan"
	}
	detail := fmt.Sprintf("[not-pipeline-actionable] feature-dev: partial-delivery re-run of #%d (after PR #%d) %s, so the run ends as a no-op, not a failure; %d remaining criteria stay open and #%d waits in Backlog for a human [#3163]",
		issue, scope.PR, why, len(scope.Criteria), issue)
	if unmet := unmetSummary(remainder); unmet != "" {
		detail += unmetDetailSeparator + unmet
	}
	return partialNoOpDecision{Action: partialNoOpBook, Detail: detail}
}

// PartialNoOpResult is decidePartialNoOp's verdict in the shape
// `nightgauge issue partial-noop --json` hands the extension, so both stage
// loops take one decision from one source (#3163).
type PartialNoOpResult struct {
	// Action is one of the PartialNoOpAction* values.
	Action string `json:"action"`
	Detail string `json:"detail,omitempty"`
	Steer  string `json:"steer,omitempty"`
	// Remainder is the deterministic re-judge of the open criteria, reported
	// with every action on a scoped run: the pr-create no-diff stop reads it
	// after feature-dev, when an operator-revised plan still answers relap.
	Remainder *RemainderVerdict `json:"remainder,omitempty"`
}

// DecidePartialNoOp reads ws's planning-{N}.json and dev-{N}.json and returns
// the decision for a zero-change feature-dev on a run scoped to scope, given
// remainder, JudgeRemainder's verdict on the scope.
func DecidePartialNoOp(ws string, issue int, scope *state.PartialRerun, relapped bool, remainder RemainderVerdict) PartialNoOpResult {
	planningPath, _ := stagecontext.ContextPath(ws, issue, "planning")
	d := decidePartialNoOp(issue, scope, readOperatorRevision(planningPath), planRevisionDispute(ws, issue), relapped, remainder)
	action := map[partialNoOpAction]string{
		partialNoOpNotScoped: PartialNoOpActionNotScoped,
		partialNoOpRelap:     PartialNoOpActionRelap,
		partialNoOpDispute:   PartialNoOpActionDispute,
		partialNoOpBook:      PartialNoOpActionBook,
		partialNoOpClose:     PartialNoOpActionClose,
	}[d.Action]
	res := PartialNoOpResult{Action: action, Detail: d.Detail, Steer: d.Steer}
	if d.Action != partialNoOpNotScoped {
		res.Remainder = &remainder
	}
	return res
}
