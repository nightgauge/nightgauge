package orchestrator

import (
	"fmt"
	"testing"
	"time"
)

// Tests for the #4098/#4222 architecture_approval_required terminal kind on the
// autonomous side.
//
// The approval gate halts a run BEFORE feature-dev because a human must approve
// a high-impact decision — its own message says "This is NOT a failure". Pre-fix
// it classified as subagent_crash, so each halt consumed a slot of the issue's
// lifetime budget and reverted the board to Ready, re-dispatching into a gate
// only a human can open. In a production autonomous run this cost ~$5.32 and 13.5
// minutes per attempt, and the SECOND attempt would have hit
// MaxLifetimeFailuresPerIssue and tripped the entire scheduler to
// safety_tripped — a full workspace stop caused by a safety feature working
// exactly as designed.

// TestOnPipelineComplete_ArchitectureApproval_NoRetryNoLifetimeIncrementNoCascade
// verifies the kind is excluded from every automatic-retry mechanism, and — the
// property that actually matters — that repeated halts never walk the issue
// toward the lifetime cap that stops the whole queue.
func TestOnPipelineComplete_ArchitectureApproval_NoRetryNoLifetimeIncrementNoCascade(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.state.LifetimeIssueFailures = map[string]int{}
	as.perIssueFailureCount = map[string]int{}
	as.retryBackoff = map[string]time.Time{}

	detail := "ARCHITECTURE APPROVAL REQUIRED — issue #900 is a high-impact decision " +
		"that must be human-approved before feature-dev implements it."

	cases := []struct {
		repo string
		num  int
	}{
		{"acme/platform", 900},
		{"acme/platform", 901},
		{"acme/dashboard", 96},
	}
	for _, c := range cases {
		addRunning(as, c.repo, c.num, "awaiting architecture approval")
		as.onPipelineComplete(c.repo, c.num, false, false,
			TerminalKindArchitectureApprovalRequired, detail)
	}

	if as.state.Status == "safety_tripped" || as.state.Status == "paused" {
		t.Fatalf("scheduler tripped/paused on architecture-approval halt; want still running — one issue awaiting a human must not stop every other Ready issue")
	}
	if as.cascadeTracker.IsTripped() {
		t.Errorf("cascadeTracker tripped on architecture-approval halt; want excluded — a human decision point says nothing about the health of the factory")
	}

	for _, c := range cases {
		key := fmt.Sprintf("%s#%d", c.repo, c.num)
		if got := as.state.LifetimeIssueFailures[key]; got != 0 {
			t.Errorf("LifetimeIssueFailures[%q] = %d, want 0 — a deliberate gate must not consume the issue's lifetime budget", key, got)
		}
		if got := as.perIssueFailureCount[key]; got != 0 {
			t.Errorf("perIssueFailureCount[%q] = %d, want 0", key, got)
		}
		if _, ok := as.retryBackoff[key]; ok {
			t.Errorf("retryBackoff[%q] was set — no retry can grant approval, so scheduling one is the defect", key)
		}
	}
	if len(as.state.Failed) != len(cases) {
		t.Fatalf("expected %d failed entries (the condition is still recorded for visibility), got %d", len(cases), len(as.state.Failed))
	}
}

// TestOnPipelineComplete_ArchitectureApproval_RepeatedHaltsNeverTripCap is the
// regression that pins the observed near-miss: the same issue gating more times
// than MaxLifetimeFailuresPerIssue must still leave the scheduler running.
// Pre-fix, the second halt was enough to stop the entire workspace.
func TestOnPipelineComplete_ArchitectureApproval_RepeatedHaltsNeverTripCap(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.state.LifetimeIssueFailures = map[string]int{}
	as.perIssueFailureCount = map[string]int{}
	as.retryBackoff = map[string]time.Time{}

	const repo, issue = "acme/platform", 900
	key := fmt.Sprintf("%s#%d", repo, issue)

	for i := 0; i < MaxLifetimeFailuresPerIssue+2; i++ {
		addRunning(as, repo, issue, "awaiting architecture approval")
		as.onPipelineComplete(repo, issue, false, false,
			TerminalKindArchitectureApprovalRequired,
			"ARCHITECTURE APPROVAL REQUIRED — a human must approve this decision")
	}

	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d after %d halts, want 0",
			key, got, MaxLifetimeFailuresPerIssue+2)
	}
	if as.state.Status == "safety_tripped" {
		t.Errorf("scheduler tripped to safety_tripped after repeated approval halts — this is the exact workspace stop the fix exists to prevent")
	}
}

// TestNotifyComplete_EmptyKindApprovalDetail_Reclassifies is the
// defense-in-depth contract: an IPC caller that passes terminalFailureKind=""
// but whose failureDetail carries the gate text must still route to the
// no-retry branch via the Go-side ClassifyTerminalKind fallback. This is the
// shape the a production autonomous run run actually produced — the TS layer knew
// it was an approval pause, but nothing forwarded a structured kind to Go.
func TestNotifyComplete_EmptyKindApprovalDetail_Reclassifies(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.state.LifetimeIssueFailures = map[string]int{}
	as.perIssueFailureCount = map[string]int{}
	as.retryBackoff = map[string]time.Time{}

	addRunning(as, "acme/platform", 900, "awaiting approval")
	as.NotifyComplete("acme/platform", 900, false, false,
		"", // no structured kind — only the raw gate text
		"feature-planning exit 1: ARCHITECTURE APPROVAL REQUIRED — issue #900 is a "+
			"high-impact decision that must be human-approved before feature-dev implements it.")

	key := "acme/platform#900"
	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d, want 0 — the raw gate text must reclassify to architecture_approval_required", key, got)
	}
	if _, ok := as.retryBackoff[key]; ok {
		t.Errorf("retryBackoff[%q] was set — the reclassified halt must not be queued for retry", key)
	}
}
