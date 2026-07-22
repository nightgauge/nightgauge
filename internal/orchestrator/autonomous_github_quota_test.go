package orchestrator

import (
	"path/filepath"
	"testing"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// TestGitHubQuotaSnapshot reads remaining/limit/resetAt from the shared tracker,
// and reports ok=false when there is no client/tracker so the gate treats the
// quota as UNKNOWN and never blocks (#3896).
func TestGitHubQuotaSnapshot(t *testing.T) {
	// No client → not ok (UNKNOWN).
	if _, _, _, ok := (&AutonomousScheduler{}).gitHubQuotaSnapshot(); ok {
		t.Fatal("nil ghClient must yield ok=false (UNKNOWN, never blocks)")
	}

	tracker := gh.NewSharedRateLimitTracker(filepath.Join(t.TempDir(), "rl.json"))
	reset := time.Now().Add(20 * time.Minute).Unix()
	if err := tracker.Set("", &gh.RateLimitInfo{Remaining: 8, Limit: 5000, ResetAt: reset}); err != nil {
		t.Fatalf("tracker.Set: %v", err)
	}
	client := gh.NewClientWithToken("x").WithRateLimitTracker(tracker, "")
	as := &AutonomousScheduler{ghClient: client}

	remaining, limit, resetAt, ok := as.gitHubQuotaSnapshot()
	if !ok || remaining != 8 || limit != 5000 || resetAt.Unix() != reset {
		t.Fatalf("snapshot=(%d,%d,%d,%v), want (8,5000,%d,true)", remaining, limit, resetAt.Unix(), ok, reset)
	}
	// The gate only blocks while the reading is still current; a future reset
	// makes this a blocking reading, a past reset makes it stale.
	if !time.Now().Before(resetAt) {
		t.Fatal("a 20-minute-out reset should be in the future (blocking reading)")
	}
}

// parseRFC3339OrFail is a small helper to read back the persisted cooldown.
func parseRFC3339OrFail(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("QuotaCooldownUntil %q is not RFC3339: %v", s, err)
	}
	return v
}

// TestApplyGitHubQuotaCooldown_SetsUntilReset: a near-term reset (within the
// hour) is honored verbatim — the scheduler suspends dispatch until the bucket
// actually resets, not a fixed floor.
func TestApplyGitHubQuotaCooldown_SetsUntilReset(t *testing.T) {
	as := &AutonomousScheduler{state: &AutonomousState{}}
	reset := time.Now().Add(15 * time.Minute)
	as.applyGitHubQuotaCooldownLocked(reset, "8/5000 remaining")

	got := parseRFC3339OrFail(t, as.state.QuotaCooldownUntil)
	if d := got.Sub(reset.UTC()); d < -time.Second || d > time.Second {
		t.Fatalf("cooldown until %v, want ~%v", got, reset.UTC())
	}
	if active, _ := as.quotaCooldownActiveLocked(); !active {
		t.Fatal("cooldown should be active immediately after being set")
	}
}

// TestApplyGitHubQuotaCooldown_FloorsPastReset: a missing/past reset must NOT
// produce an already-expired cooldown (which would hot-loop the dispatcher into
// the same exhausted bucket) — it floors to at least a minute out.
func TestApplyGitHubQuotaCooldown_FloorsPastReset(t *testing.T) {
	as := &AutonomousScheduler{state: &AutonomousState{}}
	as.applyGitHubQuotaCooldownLocked(time.Now().Add(-5*time.Minute), "stale reading")

	got := parseRFC3339OrFail(t, as.state.QuotaCooldownUntil)
	if !got.After(time.Now().Add(30 * time.Second)) {
		t.Fatalf("past reset should floor to ≥~1m out, got %v", got)
	}
	if active, _ := as.quotaCooldownActiveLocked(); !active {
		t.Fatal("floored cooldown should be active")
	}
}

// TestApplyGitHubQuotaCooldown_CapsFarFutureReset: a bad far-future reading is
// capped at one hour (the GitHub bucket is hourly) so it can't wedge the queue.
func TestApplyGitHubQuotaCooldown_CapsFarFutureReset(t *testing.T) {
	as := &AutonomousScheduler{state: &AutonomousState{}}
	as.applyGitHubQuotaCooldownLocked(time.Now().Add(6*time.Hour), "bad reading")

	got := parseRFC3339OrFail(t, as.state.QuotaCooldownUntil)
	if got.After(time.Now().Add(61 * time.Minute)) {
		t.Fatalf("far-future reset should cap at ~1h, got %v", got)
	}
}

// TestApplyGitHubQuotaCooldown_HonorsLongerExisting: an active (longer)
// Anthropic cooldown must never be shortened by a GitHub-quota dip — they share
// the QuotaCooldownUntil suspend field, and the longer wins.
func TestApplyGitHubQuotaCooldown_HonorsLongerExisting(t *testing.T) {
	as := &AutonomousScheduler{state: &AutonomousState{}}
	longer := time.Now().Add(50 * time.Minute).UTC()
	as.state.QuotaCooldownUntil = longer.Format(time.RFC3339)

	as.applyGitHubQuotaCooldownLocked(time.Now().Add(5*time.Minute), "github dip")

	got := parseRFC3339OrFail(t, as.state.QuotaCooldownUntil)
	if d := got.Sub(longer); d < -time.Second || d > time.Second {
		t.Fatalf("existing longer cooldown was shortened: got %v, want %v", got, longer)
	}
}
