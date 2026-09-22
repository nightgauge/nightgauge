package orchestrator

import (
	"testing"
	"time"
)

// TestNetworkUnavailableDoesNotFeedCascadeBreaker is the regression test for
// #1989 (AC4 gap in #1646): a network_unavailable readiness refusal — the
// local endpoint (e.g. LM Studio) was unreachable when the readiness check
// probed it, so the pipeline never dispatched into it — must never reach
// cascadeTracker.RecordFailure, the same way model_unavailable already
// doesn't. Asserted directly on the tracker's own count, not on a log line,
// per the issue's verification requirement.
func TestNetworkUnavailableDoesNotFeedCascadeBreaker(t *testing.T) {
	stubReconcileGhUnreachable(t)

	tests := []struct {
		name              string
		kind              string
		wantCascadeCalled bool
	}{
		{
			name:              "network_unavailable is exempt",
			kind:              TerminalKindNetworkUnavailable,
			wantCascadeCalled: false,
		},
		{
			// Guards against a refactor that drops both exemptions at once
			// (e.g. by deleting the whole if-chain segment instead of just
			// the new block).
			name:              "model_unavailable stays exempt",
			kind:              TerminalKindModelUnavailable,
			wantCascadeCalled: false,
		},
		{
			// An ordinary, unclassified pipeline failure has no exemption
			// block and must still reach the breaker — otherwise this test
			// would pass even if the failure handler exempted everything.
			name:              "unclassified pipeline failure is NOT exempt",
			kind:              "",
			wantCascadeCalled: true,
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := NewCascadeTracker(CascadeTrackerConfig{Threshold: 100, Window: time.Hour})
			as := &AutonomousScheduler{
				config:               AutonomousConfig{MaxConcurrent: 3},
				state:                &AutonomousState{Status: "running", LifetimeIssueFailures: map[string]int{}},
				rescanCh:             make(chan struct{}, 16),
				perIssueFailureCount: map[string]int{},
				retryBackoff:         map[string]retryPlan{},
				safetyRails:          NewSafetyRails(SafetyConfig{CircuitBreakerMax: 100}),
				cascadeTracker:       tracker,
			}

			issueNum := 601 + i
			as.state.Running = []RunningItem{{Repo: "nightgauge/nightgauge", Number: issueNum, Title: "cascade exemption test"}}
			as.onPipelineComplete("nightgauge/nightgauge", issueNum, false, false, tt.kind, "test failure: "+tt.name)
			as.drainBackground()

			got := tracker.CountInWindow(time.Now())
			gotCalled := got > 0
			if gotCalled != tt.wantCascadeCalled {
				t.Errorf("kind=%q: cascadeTracker.CountInWindow = %d (called=%v), want called=%v",
					tt.kind, got, gotCalled, tt.wantCascadeCalled)
			}
		})
	}
}
