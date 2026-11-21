package orchestrator

import (
	"fmt"
	"testing"
	"time"
)

// TestOnPipelineComplete_Exit0SevenDayQuotaMarker pins #3465 end to end on the
// scheduler: the marker skillRunner emits for a seven-day rejection that exits
// 0 is booked quota-exhausted, sets the cooldown to resetsAt (+grace), charges
// no lifetime failure, pauses nothing, and makes a racing
// haltQueueOnSlotFailure pause a no-op.
//
// Serial: sets the environment (via newAutonomousForCascadeTest).
func TestOnPipelineComplete_Exit0SevenDayQuotaMarker(t *testing.T) {
	as := newAutonomousForCascadeTest(t, 3, 30*time.Minute)
	// Must match formatQuotaRejectionMarker in
	// packages/nightgauge/src/utils/tokenParser.ts byte for byte.
	resetsAt := time.Now().Add(6 * time.Hour).Truncate(time.Second).UTC()
	marker := fmt.Sprintf("[rate-limit-quota-exhausted] seven-day usage limit reached; "+
		"resumes at %s (rate_limit_event status=rejected; seven_day bucket; resetsAt=%d)",
		resetsAt.Format(time.RFC3339), resetsAt.Unix())

	kind := ClassifyTerminalKind(marker)
	if kind != TerminalKindRateLimitQuotaExhausted {
		t.Fatalf("ClassifyTerminalKind: want %q, got %q", TerminalKindRateLimitQuotaExhausted, kind)
	}

	addRunning(as, "acme/core", 3465, "quota exit 0")
	as.onPipelineComplete("acme/core", 3465, false, false, kind, marker, false)
	as.drainBackground()

	if got := as.state.LifetimeIssueFailures["acme/core#3465"]; got != 0 {
		t.Errorf("LifetimeIssueFailures: want 0 (environmental), got %d", got)
	}
	if as.state.Status != "running" || as.state.PauseReason != "" || as.state.PauseTriggeredBy != "" {
		t.Errorf("want running and unpaused, got Status=%q PauseReason=%q PauseTriggeredBy=%q",
			as.state.Status, as.state.PauseReason, as.state.PauseTriggeredBy)
	}
	until, err := time.Parse(time.RFC3339, as.state.QuotaCooldownUntil)
	if err != nil {
		t.Fatalf("QuotaCooldownUntil %q: %v", as.state.QuotaCooldownUntil, err)
	}
	want := resetsAt.Add(quotaResetGrace)
	if d := until.Sub(want); d > time.Second || d < -time.Second {
		t.Errorf("QuotaCooldownUntil: want %s (resetsAt+grace), got %s", want, until)
	}
	if _, ok := as.retryBackoff["acme/core#3465"]; !ok {
		t.Error("retryBackoff: want a scheduled retry for acme/core#3465")
	}

	as.Pause("haltQueueOnSlotFailure: issue #3465 failed at feature-planning", "haltQueueOnSlotFailure")
	if as.state.Status != "running" {
		t.Errorf("racing haltQueueOnSlotFailure pause must be declined during cooldown, got Status=%q",
			as.state.Status)
	}
}
