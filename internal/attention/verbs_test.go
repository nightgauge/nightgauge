package attention

import "testing"

func TestRegistryIsClosedAllowlist(t *testing.T) {
	registered := []string{
		VerbQueueAdd, VerbIssueRemoveBlockedBy, VerbAutonomousResume, VerbAutonomousRescan,
		VerbAutonomousComplete, VerbAutonomousClearIssueFailures, VerbProjectSyncStatus,
		VerbIssueClose, VerbBudgetRaiseCeiling, VerbRunRetryWithEscalation,
		VerbIssueApproveArchitecture, VerbNoop,
	}
	for _, v := range registered {
		if !IsRegisteredVerb(v) {
			t.Errorf("verb %q should be registered", v)
		}
	}
	// The two new verbs the fleet lacked before E1 must be present.
	if !IsRegisteredVerb(VerbBudgetRaiseCeiling) || !IsRegisteredVerb(VerbRunRetryWithEscalation) {
		t.Error("the two new E1 verbs must be registered")
	}
	// The architecture-approval verb is the first that mutates labels; it must
	// be registry-gated like any other, never special-cased.
	if !IsRegisteredVerb(VerbIssueApproveArchitecture) {
		t.Error("issue.approveArchitecture must be registered")
	}
	// Anything not on the allowlist is rejected — the security boundary. The
	// near-miss spellings of the approval verb must NOT resolve: the executor
	// resolves the label name from config, so a surface cannot reach a
	// generic label-mutating verb even by guessing a plausible name.
	for _, v := range []string{"rm", "shell.exec", "queue.remove", "", "budget.raise",
		"issue.addLabel", "issue.approveArchitecture ", "issue.approve"} {
		if IsRegisteredVerb(v) {
			t.Errorf("verb %q must NOT be registered", v)
		}
	}
	if got := len(RegisteredVerbs()); got != len(registered) {
		t.Errorf("RegisteredVerbs len = %d, want %d", got, len(registered))
	}
}

func TestValidateOptionRejectsUnknownAndUnregistered(t *testing.T) {
	req := &DecisionRequest{
		ID: "dr_x",
		Options: []Option{
			{ID: "ok", Verb: VerbNoop},
			{ID: "bad-verb", Verb: "not-a-verb"},
		},
	}
	if _, err := ValidateOption(req, "ok"); err != nil {
		t.Errorf("valid option rejected: %v", err)
	}
	if _, err := ValidateOption(req, "missing"); err == nil {
		t.Error("unknown option id should be rejected")
	}
	if _, err := ValidateOption(req, "bad-verb"); err == nil {
		t.Error("option binding an unregistered verb should be rejected")
	}
}
