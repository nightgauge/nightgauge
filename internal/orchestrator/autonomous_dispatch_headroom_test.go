// Tests for the GraphQL dispatch headroom check (root cause of the
// recurring 5000 GraphQL-quota exhaustion incidents that paused autonomous).
//
// Each pipeline run burns ~1500-2000 GraphQL requests across stages. Without
// a pre-dispatch gate, the scheduler will start a new pipeline even when
// remaining=200, blowing through the bucket within minutes. The per-call
// `rateLimitFloor` (default 100) only engages AFTER the budget is gone.
package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// withFreshTracker constructs a tracker rooted at a temp file and seeds it
// with an entry for the test user. Caller-supplied entry is treated as fresh
// (CheckedAt = now); tests that want "stale" should explicitly set CheckedAt.
func withFreshTracker(t *testing.T, user string, remaining int, resetIn time.Duration) (*gh.Client, func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "rate-limit.json")
	tr := gh.NewSharedRateLimitTracker(path)
	info := &gh.RateLimitInfo{
		Remaining: remaining,
		Limit:     5000,
		ResetAt:   time.Now().Add(resetIn).Unix(),
	}
	if err := tr.Set(user, info); err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	c := gh.NewClientWithURL("test-token", "https://api.github.test/graphql").WithRateLimitTracker(tr, user)
	cleanup := func() {
		_ = os.Remove(path)
	}
	return c, cleanup
}

// TestDispatchHeadroom_NoTrackerAllowsDispatch — a scheduler wired without a
// rate-limit tracker (or without a ghClient) must allow dispatch. The check
// is opt-in based on tracker presence so CLI/test environments aren't blocked.
func TestDispatchHeadroom_NoTrackerAllowsDispatch(t *testing.T) {
	t.Setenv(dispatchHeadroomFloorEnv, "")
	as := &AutonomousScheduler{}
	if ok, reason := as.hasDispatchHeadroom(); !ok {
		t.Fatalf("want allowed (no tracker), got blocked: %s", reason)
	}
}

// TestDispatchHeadroom_FloorDisabled — an explicit 0 env override disables
// the gate entirely. Operators who want the old behavior (rely on the
// per-call floor) can opt out without recompiling.
func TestDispatchHeadroom_FloorDisabled(t *testing.T) {
	t.Setenv(dispatchHeadroomFloorEnv, "0")
	c, cleanup := withFreshTracker(t, "alice", 50, 30*time.Minute)
	defer cleanup()
	as := &AutonomousScheduler{ghClient: c}
	if ok, _ := as.hasDispatchHeadroom(); !ok {
		t.Fatalf("want allowed (floor=0 disables gate), got blocked")
	}
}

// TestDispatchHeadroom_BlocksWhenBelowFloor — the core regression: with
// remaining=200 and floor=2000, the gate MUST block to prevent a new
// pipeline from blowing through the rest of the bucket.
func TestDispatchHeadroom_BlocksWhenBelowFloor(t *testing.T) {
	t.Setenv(dispatchHeadroomFloorEnv, "2000")
	c, cleanup := withFreshTracker(t, "alice", 200, 30*time.Minute)
	defer cleanup()
	as := &AutonomousScheduler{ghClient: c}
	ok, reason := as.hasDispatchHeadroom()
	if ok {
		t.Fatalf("want blocked (remaining=200 < floor=2000), got allowed")
	}
	if reason == "" {
		t.Error("want non-empty reason for blocked dispatch")
	}
}

// TestDispatchHeadroom_AllowsWhenAboveFloor — sanity: when the bucket is
// healthy, dispatch proceeds without comment.
func TestDispatchHeadroom_AllowsWhenAboveFloor(t *testing.T) {
	t.Setenv(dispatchHeadroomFloorEnv, "2000")
	c, cleanup := withFreshTracker(t, "alice", 4500, 30*time.Minute)
	defer cleanup()
	as := &AutonomousScheduler{ghClient: c}
	if ok, reason := as.hasDispatchHeadroom(); !ok {
		t.Fatalf("want allowed (remaining=4500 > floor=2000), got blocked: %s", reason)
	}
}

// TestDispatchHeadroom_AllowsWhenResetImminent — if the bucket is about to
// refill within 30 seconds, don't gate. Holding back at that point just adds
// latency for no benefit; the per-call floor will catch any in-flight burst.
func TestDispatchHeadroom_AllowsWhenResetImminent(t *testing.T) {
	t.Setenv(dispatchHeadroomFloorEnv, "2000")
	c, cleanup := withFreshTracker(t, "alice", 100, 10*time.Second)
	defer cleanup()
	as := &AutonomousScheduler{ghClient: c}
	if ok, _ := as.hasDispatchHeadroom(); !ok {
		t.Fatalf("want allowed (reset in 10s), got blocked")
	}
}

// TestDispatchHeadroomFloor_DefaultAndOverride — the env override is parsed
// correctly; bad values fall back to the default. This is also where we
// pin the default so a future change to defaultDispatchHeadroomFloor is a
// deliberate, reviewed move.
func TestDispatchHeadroomFloor_DefaultAndOverride(t *testing.T) {
	t.Setenv(dispatchHeadroomFloorEnv, "")
	if got := dispatchHeadroomFloor(); got != defaultDispatchHeadroomFloor {
		t.Errorf("default: got %d, want %d", got, defaultDispatchHeadroomFloor)
	}
	if defaultDispatchHeadroomFloor != 2000 {
		t.Errorf("defaultDispatchHeadroomFloor moved to %d — review intentionally",
			defaultDispatchHeadroomFloor)
	}
	t.Setenv(dispatchHeadroomFloorEnv, "500")
	if got := dispatchHeadroomFloor(); got != 500 {
		t.Errorf("override=500: got %d, want 500", got)
	}
	t.Setenv(dispatchHeadroomFloorEnv, "  300  ")
	if got := dispatchHeadroomFloor(); got != 300 {
		t.Errorf("override with whitespace: got %d, want 300", got)
	}
	t.Setenv(dispatchHeadroomFloorEnv, "garbage")
	if got := dispatchHeadroomFloor(); got != defaultDispatchHeadroomFloor {
		t.Errorf("invalid override: got %d, want default %d", got, defaultDispatchHeadroomFloor)
	}
	t.Setenv(dispatchHeadroomFloorEnv, "-5")
	if got := dispatchHeadroomFloor(); got != defaultDispatchHeadroomFloor {
		t.Errorf("negative override: got %d, want default", got)
	}
}
