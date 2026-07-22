package orchestrator

import (
	"testing"
	"time"
)

// TestQuotaCooldownSnapshot pins the read-only, side-effect-free accessor that
// the workflow.quotaState IPC bridge (#3909) consumes. Unlike
// quotaCooldownActiveLocked, this snapshot must NOT mutate state — a stale or
// expired deadline reads as inactive but is left in place for the scheduler's
// own clearing path to handle.
func TestQuotaCooldownSnapshot(t *testing.T) {
	t.Run("active cooldown reports active with until+reason", func(t *testing.T) {
		until := time.Now().Add(90 * time.Minute).UTC().Format(time.RFC3339)
		as := &AutonomousScheduler{
			state: &AutonomousState{
				Status:              "running",
				QuotaCooldownUntil:  until,
				QuotaCooldownReason: "GitHub API quota low — dispatch suspended",
			},
		}
		gotUntil, gotReason, active := as.QuotaCooldownSnapshot()
		if !active {
			t.Fatalf("expected active=true for a future deadline")
		}
		if gotUntil != until {
			t.Errorf("until = %q, want %q", gotUntil, until)
		}
		if gotReason != "GitHub API quota low — dispatch suspended" {
			t.Errorf("reason = %q, want the GitHub cooldown reason", gotReason)
		}
	})

	t.Run("expired cooldown reads inactive without clearing state", func(t *testing.T) {
		until := time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339)
		as := &AutonomousScheduler{
			state: &AutonomousState{
				Status:              "running",
				QuotaCooldownUntil:  until,
				QuotaCooldownReason: "Anthropic API quota exhausted",
			},
		}
		gotUntil, gotReason, active := as.QuotaCooldownSnapshot()
		if active {
			t.Errorf("expected active=false for a past deadline")
		}
		// Side-effect-free contract: the deadline+reason are still returned and
		// the underlying state is left untouched for the scheduler to clear.
		if gotUntil != until || gotReason != "Anthropic API quota exhausted" {
			t.Errorf("snapshot should echo stored values, got until=%q reason=%q", gotUntil, gotReason)
		}
		if as.state.QuotaCooldownUntil != until {
			t.Errorf("snapshot must not clear state; QuotaCooldownUntil = %q, want %q",
				as.state.QuotaCooldownUntil, until)
		}
	})

	t.Run("no cooldown reports inactive with empty fields", func(t *testing.T) {
		as := &AutonomousScheduler{state: &AutonomousState{Status: "running"}}
		until, reason, active := as.QuotaCooldownSnapshot()
		if active || until != "" || reason != "" {
			t.Errorf("expected inactive empty snapshot, got until=%q reason=%q active=%v", until, reason, active)
		}
	})

	t.Run("malformed deadline reads inactive", func(t *testing.T) {
		as := &AutonomousScheduler{
			state: &AutonomousState{
				Status:             "running",
				QuotaCooldownUntil: "not-a-timestamp",
			},
		}
		_, _, active := as.QuotaCooldownSnapshot()
		if active {
			t.Errorf("expected active=false for a malformed deadline")
		}
	})
}
