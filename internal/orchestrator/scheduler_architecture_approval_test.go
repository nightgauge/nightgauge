package orchestrator

import (
	"testing"
)

// TestClassifyTerminalKind_ArchitectureApprovalRequired locks in the fix for
// the gate halt being bucketed as a process crash.
//
// The architecture-approval gate (#4098/#4222) stops a run BEFORE feature-dev
// and states in its own message that this is NOT a failure. Pre-fix it had no
// matcher and fell through to subagent_crash, which counted it toward
// LifetimeIssueFailures and reverted the issue to Ready — re-dispatching it
// into a gate only a human can open. Observed on a production autonomous run at
// ~$5.32 and 13.5 minutes per attempt, with the second attempt due to trip the
// whole scheduler to safety_tripped.
func TestClassifyTerminalKind_ArchitectureApprovalRequired(t *testing.T) {
	// The real gate text, as emitted by verifyArchitectureApproval and observed
	// in the a production autonomous run run record.
	live := "ARCHITECTURE APPROVAL REQUIRED — issue #900 is a high-impact decision " +
		"that must be human-approved before feature-dev implements it. Why: " +
		"production-touching change — irreversible blast radius (#4135); " +
		"architecture not yet human-approved."

	gated := []string{
		live,
		"[architecture-approval-required] a human must approve this decision",
		"architecture_approval_required",
	}
	for _, text := range gated {
		if got := ClassifyTerminalKind(text); got != TerminalKindArchitectureApprovalRequired {
			t.Errorf("ClassifyTerminalKind(%q) = %q, want %q",
				text, got, TerminalKindArchitectureApprovalRequired)
		}
	}

	// The halt is wrapped by SetStageError, which appends "exit N" — the
	// generic subagent-crash fallback must not win over the specific kind.
	// This is the exact shape that produced the misclassification in the wild.
	withExit := "feature-planning exit 1: " + live
	if got := ClassifyTerminalKind(withExit); got != TerminalKindArchitectureApprovalRequired {
		t.Errorf("ClassifyTerminalKind(%q) = %q, want %q — the crash fallback must not win",
			withExit, got, TerminalKindArchitectureApprovalRequired)
	}

	// And it must not steal from neighbouring deliberate-halt kinds.
	notApproval := map[string]string{
		"[blocked-dependency] issue has open blockers":           TerminalKindBlockedDependency,
		"[validation-failed] feature-validate reported failure":  TerminalKindValidationFailed,
		"[branch-forked] remote is not an ancestor of local tip": TerminalKindBranchForked,
		"[adapter-auth-failed] probe timed out":                  TerminalKindAdapterAuthFailed,
	}
	for text, want := range notApproval {
		if got := ClassifyTerminalKind(text); got != want {
			t.Errorf("ClassifyTerminalKind(%q) = %q, want %q — approval kind must not steal it",
				text, got, want)
		}
	}
}
