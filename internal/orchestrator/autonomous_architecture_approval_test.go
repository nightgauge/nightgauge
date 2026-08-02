package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
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
	as.retryBackoff = map[string]retryPlan{}

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
		if _, ok := retryDeadline(as, key); ok {
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
	as.retryBackoff = map[string]retryPlan{}

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

// TestArchitectureApproval_DoesNotFeedConsecutiveFailureRail is the regression
// for the SECOND breaker. Exempting the lifetime cap and the cascade tracker was
// not enough: SafetyRails.ConsecutiveFailures is independent, and three issues
// queued behind one un-reviewed epic tripped `safety:rail-check` at max 3,
// halting every unrelated repo — the same whole-workspace stop by another route.
//
// Observed in production after the first fix shipped: #1181, #1182 and #1185 all
// hit the gate, and the fleet stopped despite four other issues completing fine.
func TestArchitectureApproval_DoesNotFeedConsecutiveFailureRail(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.state.LifetimeIssueFailures = map[string]int{}
	as.perIssueFailureCount = map[string]int{}
	as.retryBackoff = map[string]retryPlan{}
	// Wire real rails: the cascade fixture leaves them nil, and a skipped test
	// would assert nothing about the breaker that actually stopped the fleet.
	as.safetyRails = NewSafetyRails(SafetyConfig{CircuitBreakerMax: 3})

	before := as.safetyRails.State().ConsecutiveFailures

	for i, num := range []int{900, 901, 902, 903} {
		addRunning(as, "acme/platform", num, "awaiting architecture approval")
		as.onPipelineComplete("acme/platform", num, false, false,
			TerminalKindArchitectureApprovalRequired,
			"ARCHITECTURE APPROVAL REQUIRED — a human must approve this decision")
		if got := as.safetyRails.State().ConsecutiveFailures; got != before {
			t.Fatalf("after %d approval halt(s): ConsecutiveFailures = %d, want %d unchanged — "+
				"a run awaiting a human is not a factory failure", i+1, got, before)
		}
	}

	if as.state.Status == "safety_tripped" || as.state.Status == "paused" {
		t.Errorf("status = %q after 4 approval halts, want the fleet still running", as.state.Status)
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
	as.retryBackoff = map[string]retryPlan{}

	addRunning(as, "acme/platform", 900, "awaiting approval")
	as.NotifyComplete("acme/platform", 900, false, false,
		"", // no structured kind — only the raw gate text
		"feature-planning exit 1: ARCHITECTURE APPROVAL REQUIRED — issue #900 is a "+
			"high-impact decision that must be human-approved before feature-dev implements it.")

	key := "acme/platform#900"
	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d, want 0 — the raw gate text must reclassify to architecture_approval_required", key, got)
	}
	if _, ok := retryDeadline(as, key); ok {
		t.Errorf("retryBackoff[%q] was set — the reclassified halt must not be queued for retry", key)
	}
}

// TestArchitectureApproval_NoPRSidelinedIssueBlocksDependent is the AC #1
// regression for #281: a no-implementation architecture-approval halt must
// land at "In progress" (never "In review", which has no PR behind it here)
// and a downstream blockedBy dependent must NOT become a dispatch candidate
// afterward — evaluateDeps requires confirmed PR evidence, not the bare
// status string, before treating an upstream "In review" as satisfied.
func TestArchitectureApproval_NoPRSidelinedIssueBlocksDependent(t *testing.T) {
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		return []byte("[]"), nil // no open PRs anywhere — the gate fired before feature-dev
	})

	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}

	// Simulate the halted upstream issue parked at "In review" by a stale/buggy
	// caller (defense in depth for the isWorkCompleteStatus/evaluateDeps split):
	// with no PR-backing evidence refreshed, evaluateDeps must still block.
	g := &depgraph.Graph{
		Nodes: map[string]*depgraph.Node{
			"O/app#900": {Repo: "O/app", Number: 900, State: "OPEN", BoardStatus: "In review"},
			"O/app#901": {Repo: "O/app", Number: 901, State: "OPEN", BoardStatus: "Ready"},
		},
		Edges: []depgraph.Edge{
			{From: depgraph.NodeID{Repo: "O/app", Number: 901}, To: depgraph.NodeID{Repo: "O/app", Number: 900}, Type: "blockedBy"},
		},
	}

	candidates := as.prioritize(context.Background(), g)
	for _, c := range candidates {
		if c.Repo == "O/app" && c.Number == 901 {
			t.Fatalf("O/app#901 dispatched despite depending on an In-review issue with no confirmed PR — evaluateDeps must fail closed")
		}
	}

	// sidelineHalt itself must choose "In progress", not "In review", for the
	// halted issue given no confirmed PR.
	buf := withCapturedLog(t)
	as.sidelineHalt("O/app", 900, "architecture approval required")
	got := buf.String()
	if !strings.Contains(got, "move-to-in-progress:") {
		t.Errorf("expected sidelineHalt to choose In progress with no PR, got log: %q", got)
	}
}
