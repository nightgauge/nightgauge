package orchestrator

import (
	"fmt"
	"testing"
	"time"
)

// Tests for the #312 adapter-auth-failed terminal kind. An autonomous restart
// fanned out 4 runs across 3 repos within ~30s; three cold `claude auth status`
// probes lost the CPU race and timed out at 5s, killing their runs at
// pipeline-start in ~8s at $0 — even though auth was fine (a fourth probe in the
// same burst succeeded on the same CLI). Pre-fix those false-negatives recorded
// terminal_failure_kind=subagent_crash, so three burst timeouts landed in the
// cascade-breaker window and the lifetime-failure cap as if the subagent had
// crashed. The kind must route through the retryable-infra recovery path
// instead: short backoff, board→Ready, NO lifetime-cap increment, NO cascade
// feed, NO pause.

// TestOnPipelineComplete_AdapterAuthFailed_TransientNoPauseNoCascade verifies
// the adapter-auth-failed kind routes like the other transient infra kinds and
// — critically — does NOT feed the cascade breaker even when a whole burst of
// them arrives inside the window.
func TestOnPipelineComplete_AdapterAuthFailed_TransientNoPauseNoCascade(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	as.state.LifetimeIssueFailures = map[string]int{}
	as.perIssueFailureCount = map[string]int{}
	as.retryBackoff = map[string]time.Time{}

	before := time.Now()
	// Four burst false-negatives across repos — one more than the cascade
	// threshold. If the kind fed the breaker, this would trip it.
	cases := []struct {
		repo string
		num  int
	}{
		{"acme/dashboard", 96},
		{"acme/mobile", 303},
		{"acme/infra", 162},
		{"acme/infra", 163},
	}
	for _, c := range cases {
		addRunning(as, c.repo, c.num, "burst probe starvation")
		as.onPipelineComplete(c.repo, c.num, false, false,
			TerminalKindAdapterAuthFailed,
			"[adapter-auth-failed] Auth pre-flight failed — auth probe timed out after retry "+
				"(adapter CLI unresponsive — transient, not a logged-out session).")
	}

	if as.state.Status == "safety_tripped" || as.state.Status == "paused" {
		t.Fatalf("scheduler tripped/paused on adapter-auth-failed burst; want still running (probe starvation is not a crash)")
	}
	if as.cascadeTracker.IsTripped() {
		t.Errorf("cascadeTracker tripped on adapter-auth-failed burst; want excluded from cascade")
	}

	for _, c := range cases {
		key := fmt.Sprintf("%s#%d", c.repo, c.num)
		if got := as.state.LifetimeIssueFailures[key]; got != 0 {
			t.Errorf("LifetimeIssueFailures[%q] = %d, want 0 (retryable infra)", key, got)
		}
		if got := as.perIssueFailureCount[key]; got != 0 {
			t.Errorf("perIssueFailureCount[%q] = %d, want 0", key, got)
		}
		retryAt, ok := as.retryBackoff[key]
		if !ok {
			t.Fatalf("expected retryBackoff[%q] to be set after adapter-auth-failed", key)
		}
		if !retryAt.After(before) {
			t.Errorf("retryAt %v for %q not after call start %v", retryAt, key, before)
		}
	}
	if len(as.state.Failed) != len(cases) {
		t.Fatalf("expected %d failed entries, got %d", len(cases), len(as.state.Failed))
	}
}

// TestNotifyComplete_EmptyKindAdapterAuthDetail_RoutesTransient is the
// defense-in-depth contract: when the IPC caller passes terminalFailureKind=""
// but failureDetail carries the `[adapter-auth-failed]` marker, the Go-side
// ClassifyTerminalKind fallback must re-classify and route to the transient
// branch (no lifetime increment, no pause, backoff set).
func TestNotifyComplete_EmptyKindAdapterAuthDetail_RoutesTransient(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "acme/infra", Number: 162, Title: "Reclassify case"},
			},
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}

	as.NotifyComplete("acme/infra", 162, false, false, "",
		"[adapter-auth-failed] Auth pre-flight failed — auth probe timed out after retry.")

	key := "acme/infra#162"
	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d, want 0 (reclassified transient from detail)", key, got)
	}
	if as.state.Status == "paused" || as.state.Status == "safety_tripped" {
		t.Errorf("autonomous paused; want still running (reclassified transient)")
	}
	if _, ok := as.retryBackoff[key]; !ok {
		t.Errorf("expected retryBackoff[%q] after reclassification", key)
	}
}
