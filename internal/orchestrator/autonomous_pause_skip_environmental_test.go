// Tests for the #3444 defense-in-depth: Pause() declines
// haltQueueOnSlotFailure pauses when a fresh quota cooldown is active.
//
// The primary fix lives on the TS side (haltQueueOnSlotFailure detects the
// environmental marker in pipelineResult.error.message and short-circuits).
// This Go-side guard catches the race where the TS regex misses (e.g. a new
// upstream marker shape) but onPipelineComplete already classified the
// failure and set QuotaCooldownUntil — without this guard, a stale TS halt
// would land moments later and pin Status="paused" until manual Resume,
// while the cooldown auto-clears at expiry.
package orchestrator

import (
	"testing"
	"time"
)

// TestPause_DeclinedWhenQuotaCooldownActive — environmental failure path:
// quota cooldown is set, then a haltQueueOnSlotFailure pause arrives.
// Pause() must decline so Status stays running and autonomous resumes
// automatically when the cooldown expires.
func TestPause_DeclinedWhenQuotaCooldownActive(t *testing.T) {
	as := &AutonomousScheduler{
		state: &AutonomousState{
			Status:              "running",
			QuotaCooldownUntil:  time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339),
			QuotaCooldownReason: "rate-limit-quota-exhausted",
		},
		rescanCh: make(chan struct{}, 1),
	}

	as.Pause("haltQueueOnSlotFailure: issue #3375 failed at feature-dev", "haltQueueOnSlotFailure")

	if as.state.Status != "running" {
		t.Errorf("Status: want 'running' (pause declined during cooldown), got %q", as.state.Status)
	}
	if as.state.PauseReason != "" {
		t.Errorf("PauseReason: want empty (pause declined), got %q", as.state.PauseReason)
	}
	if as.state.PauseTriggeredBy != "" {
		t.Errorf("PauseTriggeredBy: want empty (pause declined), got %q", as.state.PauseTriggeredBy)
	}
	if as.state.QuotaCooldownUntil == "" {
		t.Error("QuotaCooldownUntil cleared by Pause — must be preserved so cooldown still gates dispatch")
	}
}

// TestPause_AllowedAfterQuotaCooldownExpired — once the cooldown lapses, a
// subsequent haltQueueOnSlotFailure pause MUST land normally (e.g. a real
// follow-up failure after the bucket recovered). The guard is conditional,
// not absolute.
func TestPause_AllowedAfterQuotaCooldownExpired(t *testing.T) {
	as := &AutonomousScheduler{
		state: &AutonomousState{
			Status:             "running",
			QuotaCooldownUntil: time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339),
		},
		rescanCh: make(chan struct{}, 1),
	}

	as.Pause("haltQueueOnSlotFailure: issue #100 failed at validation", "haltQueueOnSlotFailure")

	if as.state.Status != "paused" {
		t.Errorf("Status: want 'paused' (expired cooldown does not protect), got %q", as.state.Status)
	}
	if as.state.PauseReason == "" {
		t.Error("PauseReason: want non-empty, got empty (pause should have landed)")
	}
}

// TestPause_UserActionAlwaysAllowed — even with an active cooldown, a manual
// user-initiated pause MUST land. The guard targets only haltQueueOnSlotFailure
// (auto-triggered post-failure) — not deliberate operator actions.
func TestPause_UserActionAlwaysAllowed(t *testing.T) {
	as := &AutonomousScheduler{
		state: &AutonomousState{
			Status:             "running",
			QuotaCooldownUntil: time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339),
		},
		rescanCh: make(chan struct{}, 1),
	}

	as.Pause("user requested via UI", "user")

	if as.state.Status != "paused" {
		t.Errorf("Status: want 'paused' (user action must always land), got %q", as.state.Status)
	}
	if as.state.PauseTriggeredBy != "user" {
		t.Errorf("PauseTriggeredBy: want 'user', got %q", as.state.PauseTriggeredBy)
	}
}

// TestPause_SafetyTripAlwaysAllowed — safety rail trips must land even with
// an active cooldown. They indicate a different class of problem (rate-limit
// abuse, circuit-breaker, lifetime-failure cap) that requires manual triage,
// separate from environmental backoff.
func TestPause_SafetyTripAlwaysAllowed(t *testing.T) {
	as := &AutonomousScheduler{
		state: &AutonomousState{
			Status:             "running",
			QuotaCooldownUntil: time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339),
		},
		rescanCh: make(chan struct{}, 1),
	}

	as.Pause("safety: rate limit exceeded", "safety:rate-limit")

	if as.state.Status != "paused" {
		t.Errorf("Status: want 'paused' (safety trip must always land), got %q", as.state.Status)
	}
}

// TestPause_NoCooldownStillHalts — without any cooldown set,
// haltQueueOnSlotFailure pauses normally (the common case for non-environmental
// failures like validation_error or subagent_crash).
func TestPause_NoCooldownStillHalts(t *testing.T) {
	as := &AutonomousScheduler{
		state: &AutonomousState{
			Status: "running",
			// QuotaCooldownUntil intentionally empty
		},
		rescanCh: make(chan struct{}, 1),
	}

	as.Pause("haltQueueOnSlotFailure: issue #42 failed at feature-validate", "haltQueueOnSlotFailure")

	if as.state.Status != "paused" {
		t.Errorf("Status: want 'paused' (real bug — no cooldown active), got %q", as.state.Status)
	}
	if as.state.PauseTriggeredBy != "haltQueueOnSlotFailure" {
		t.Errorf("PauseTriggeredBy: want 'haltQueueOnSlotFailure', got %q", as.state.PauseTriggeredBy)
	}
}
