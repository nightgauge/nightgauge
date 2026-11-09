package orchestrator

import (
	"os"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// Tests for #2895: an out-of-scope blocker stop that recorded its finding is a
// non-fault outcome — it never charges the lifetime cap or backs the issue off.

const outOfScopeDetail = "BLOCKED — PR unmerged: out-of-scope: PLAN_REVISION_NEEDED. " +
	"Remediation: resolve the upstream dependency, then clear the finding."

const validFinding7 = `{"schema_version":"1.0","issue_number":7,"stage":"feature-dev",` +
	`"signal_type":"PLAN_REVISION_NEEDED","reason":"needs upstream","rationale":"r",` +
	`"evidence":["blocked-on: x"],"run_id":"run-1","recorded_at":"2026-11-01T00:00:00Z"}`

func outOfScopeScheduler(root string) *AutonomousScheduler {
	return &AutonomousScheduler{
		trustGateOverride: allWritersGate(),
		config:            AutonomousConfig{MaxConcurrent: 3},
		workspaceRoot:     root,
		state: &AutonomousState{
			Status:                "running",
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]retryPlan{},
	}
}

func completeFailure(as *AutonomousScheduler, detail string) {
	as.state.Running = []RunningItem{{Repo: "", Number: 7, Title: "X"}}
	as.onPipelineComplete("", 7, false, false, "", detail, false)
	as.drainBackground()
}

func TestOutOfScopeBlockedStop_WithFinding_NotCharged(t *testing.T) {
	root := layouttest.Repo(t)
	path := writeBlockedFindingFile(t, root, 7, validFinding7)
	as := outOfScopeScheduler(root)

	completeFailure(as, outOfScopeDetail)
	completeFailure(as, outOfScopeDetail)

	key := "#7"
	if got, ok := as.state.LifetimeIssueFailures[key]; ok {
		t.Errorf("LifetimeIssueFailures[%s] = %d, want absent", key, got)
	}
	if got := as.perIssueFailureCount[key]; got != 0 {
		t.Errorf("perIssueFailureCount[%s] = %d, want 0", key, got)
	}
	if _, ok := as.retryBackoff[key]; ok {
		t.Errorf("retry plan scheduled for %s; want none", key)
	}
	if as.state.QuarantinedIssues[key] {
		t.Errorf("%s quarantined; want dispatchable", key)
	}
	if HoldForTerminalKind(TerminalKindOutOfScopeBlocked) != "" {
		t.Error("out-of-scope stop must not be a held kind — clear-failures would be needed")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, found := as.blockedFindingFor("", 7); found {
		t.Error("finding still reported after it was cleared")
	}
}

func TestOutOfScopeBlockedStop_WithoutFinding_StillCharged(t *testing.T) {
	as := outOfScopeScheduler(layouttest.Repo(t))
	completeFailure(as, outOfScopeDetail)
	if got := as.state.LifetimeIssueFailures["#7"]; got != 1 {
		t.Errorf("LifetimeIssueFailures = %d, want 1 when no finding is on disk", got)
	}
}

func TestRealFailures_StillReachLifetimeCap(t *testing.T) {
	root := layouttest.Repo(t)
	writeBlockedFindingFile(t, root, 7, validFinding7)
	as := outOfScopeScheduler(root)
	for i := 0; i < MaxLifetimeFailuresPerIssue; i++ {
		completeFailure(as, "premature turn end: stage ended on a promise")
	}
	if got := as.state.LifetimeIssueFailures["#7"]; got != MaxLifetimeFailuresPerIssue {
		t.Errorf("LifetimeIssueFailures = %d, want %d", got, MaxLifetimeFailuresPerIssue)
	}
}

func TestIsOutOfScopeBlockedOutcome(t *testing.T) {
	cases := map[string]bool{
		outOfScopeDetail: true,
		"BLOCKED — PR #12 unmerged: required check failing.":                    false,
		"out-of-scope: PLAN_REVISION_NEEDED":                                    false,
		"BLOCKED — PR unmerged: checks. Remediation: unmerged: out-of-scope: X": false,
		"": false,
	}
	for in, want := range cases {
		if got := IsOutOfScopeBlockedOutcome(in); got != want {
			t.Errorf("IsOutOfScopeBlockedOutcome(%q) = %v, want %v", in, got, want)
		}
	}
}
