package orchestrator

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// #2756: an architecture-approval hold books as a non-failure, every other
// terminal kind keeps its failure representation.
func TestApplyNonFailureOutcome(t *testing.T) {
	cases := []struct {
		name, kind, outcome, outcomeType string
		wantOutcome, wantType            string
	}{
		{"approval hold → cancelled awaiting_approval", TerminalKindArchitectureApprovalRequired, "failed", "", "cancelled", OutcomeTypeAwaitingApproval},
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
