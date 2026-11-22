package orchestrator

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// #2756: an architecture-approval hold books as a non-failure, every other
// terminal kind keeps its failure representation.
func TestApplyNonFailureOutcome(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, kind, outcome, outcomeType string
		wantOutcome, wantType            string
	}{
		{"approval hold → cancelled awaiting_approval", TerminalKindArchitectureApprovalRequired, "failed", "", "cancelled", OutcomeTypeAwaitingApproval},
		{"not_pipeline_actionable alone stays failed", TerminalKindNotPipelineActionable, "failed", "", "failed", ""},
		{"subagent crash stays failed", TerminalKindSubagentCrash, "failed", "", "failed", ""},
		{"validation failure stays failed", "validation_failed", "failed", "", "failed", ""},
		{"blocked record unchanged", "", "failed", OutcomeTypeBlocked, "failed", OutcomeTypeBlocked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := state.V2RunRecord{Outcome: tc.outcome, OutcomeType: tc.outcomeType,
				TerminalFailureKind: tc.kind}
			ApplyNonFailureOutcome(&rec)
			if rec.Outcome != tc.wantOutcome || rec.OutcomeType != tc.wantType {
				t.Errorf("got outcome=%q type=%q, want %q/%q", rec.Outcome, rec.OutcomeType, tc.wantOutcome, tc.wantType)
			}
			if rec.TerminalFailureKind != tc.kind {
				t.Errorf("terminal kind changed: %q → %q", tc.kind, rec.TerminalFailureKind)
			}
		})
	}
	ApplyNonFailureOutcome(nil) // must not panic
}

// #3487: only a writer holding the booking's flag calls this, and it books
// nothing but not_pipeline_actionable.
func TestApplyPartialNoOpOutcome(t *testing.T) {
	t.Parallel()

	rec := state.V2RunRecord{Outcome: "failed", OutcomeType: OutcomeTypeBlocked, TerminalFailureKind: TerminalKindNotPipelineActionable}
	ApplyPartialNoOpOutcome(&rec)
	if rec.Outcome != "cancelled" || rec.OutcomeType != OutcomeTypeNoOp || rec.TerminalFailureKind != TerminalKindNotPipelineActionable {
		t.Errorf("record = %+v, want cancelled/no_op with the kind retained", rec)
	}
	stopped := state.V2RunRecord{Outcome: "cancelled", OutcomeType: OutcomeTypeOperatorStopped, TerminalFailureKind: TerminalKindOperatorStop}
	ApplyPartialNoOpOutcome(&stopped)
	if stopped.OutcomeType != OutcomeTypeOperatorStopped {
		t.Errorf("an operator stop was rebooked %q", stopped.OutcomeType)
	}
	crashed := state.V2RunRecord{Outcome: "failed", TerminalFailureKind: TerminalKindSubagentCrash}
	ApplyPartialNoOpOutcome(&crashed)
	if crashed.Outcome != "failed" || crashed.OutcomeType != "" {
		t.Errorf("a crash was rebooked %s/%s", crashed.Outcome, crashed.OutcomeType)
	}
	ApplyPartialNoOpOutcome(nil) // must not panic
}
