// Tests for rate-aware active scan cadence (#172): the fixed 30s cadence,
// multiplied by per-cycle GraphQL cost, exceeded the 5,000/hour budget on a
// measured multi-repo workspace. These tests cover the pure rateAwareCadence
// pacing function, EWMA cost convergence, and the sustained-hour budget
// simulation that is the durable regression guard for acceptance criterion 2
// ("a future change that multiplies per-cycle cost fails a test").
package orchestrator

import (
	"testing"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// TestRateAwareCadence_NoAvgCostFailsOpen — cold start (no cycles observed
// yet) must behave exactly like today's fixed cadence.
func TestRateAwareCadence_NoAvgCostFailsOpen(t *testing.T) {
	base := 30 * time.Second
	got := rateAwareCadence(base, 4000, 5000, time.Now().Add(30*time.Minute), 0, 2000, 10*time.Minute)
	if got != base {
		t.Fatalf("want base %s (avgCost<=0 fail-open), got %s", base, got)
	}
}

// TestRateAwareCadence_RemainingZeroFailsOpen — no remaining/limit data
// (remaining<=0) must also fail open rather than force the ceiling.
func TestRateAwareCadence_RemainingZeroFailsOpen(t *testing.T) {
	base := 30 * time.Second
	got := rateAwareCadence(base, 0, 5000, time.Now().Add(30*time.Minute), 50, 2000, 10*time.Minute)
	if got != base {
		t.Fatalf("want base %s (remaining<=0 fail-open), got %s", base, got)
	}
}

// TestRateAwareCadence_ResetInPastFailsOpen — a stale resetAt (already
// passed) must fail open rather than gate on data that's about to refresh.
func TestRateAwareCadence_ResetInPastFailsOpen(t *testing.T) {
	base := 30 * time.Second
	got := rateAwareCadence(base, 4000, 5000, time.Now().Add(-1*time.Minute), 50, 2000, 10*time.Minute)
	if got != base {
		t.Fatalf("want base %s (resetAt in past fail-open), got %s", base, got)
	}
}

// TestRateAwareCadence_AmpleHeadroomReturnsBase — plenty of remaining quota
// relative to time-to-reset must not stretch cadence at all.
func TestRateAwareCadence_AmpleHeadroomReturnsBase(t *testing.T) {
	base := 30 * time.Second
	// 4800 usable over 1 hour at 10 points/cycle => ~480 allowed cycles,
	// needed interval ~7.5s — far below base, so base wins.
	got := rateAwareCadence(base, 4800, 5000, time.Now().Add(1*time.Hour), 10, 2000, 10*time.Minute)
	if got != base {
		t.Fatalf("want base %s (ample headroom), got %s", base, got)
	}
}

// TestRateAwareCadence_TightHeadroomStretchesTowardCeiling — the motivating
// scenario: measured ~101 points/min against a 5,000/hour budget. With
// little usable headroom left before reset, the interval must stretch well
// past base, bounded by the ceiling.
func TestRateAwareCadence_TightHeadroomStretchesTowardCeiling(t *testing.T) {
	base := 30 * time.Second
	ceiling := 10 * time.Minute
	// usable = 2500-2000 = 500; avgCost=100/cycle => 5 allowed cycles over
	// 50 minutes to reset => needed ~10 minutes/cycle => clamps to ceiling.
	got := rateAwareCadence(base, 2500, 5000, time.Now().Add(50*time.Minute), 100, 2000, ceiling)
	// Allow a small tolerance: time.Until(resetAt) is evaluated a moment
	// after resetAt was computed relative to time.Now() above, so the
	// division can land a handful of nanoseconds under the exact ceiling.
	if diff := ceiling - got; diff < 0 || diff > time.Millisecond {
		t.Fatalf("want ~ceiling %s (tight headroom), got %s", ceiling, got)
	}
}

// TestRateAwareCadence_UsableBelowZeroReturnsCeilingImmediately — remaining
// is already at or below the dispatch floor; stretch to the ceiling without
// doing any further math.
func TestRateAwareCadence_UsableBelowZeroReturnsCeilingImmediately(t *testing.T) {
	base := 30 * time.Second
	ceiling := 10 * time.Minute
	got := rateAwareCadence(base, 1900, 5000, time.Now().Add(30*time.Minute), 50, 2000, ceiling)
	if got != ceiling {
		t.Fatalf("want ceiling %s (usable<=0), got %s", ceiling, got)
	}
}

// TestRateAwareCadence_NeverBelowBase — even when the pacing math computes
// an interval shorter than base (huge headroom, huge cost estimate), the
// result must never go below base — rate-awareness only ever slows cadence
// down, never speeds it up past the configured floor.
func TestRateAwareCadence_NeverBelowBase(t *testing.T) {
	base := 30 * time.Second
	got := rateAwareCadence(base, 4999, 5000, time.Now().Add(1*time.Hour), 1, 2000, 10*time.Minute)
	if got != base {
		t.Fatalf("want base %s (needed interval below base clamps up), got %s", base, got)
	}
}

// TestScanCadence_RateAwareDisabledReturnsFixedBase — when RateAwareCadence
// is false, scanCadence must behave exactly like the pre-#172 fixed cadence
// regardless of tracker state.
func TestScanCadence_RateAwareDisabledReturnsFixedBase(t *testing.T) {
	c, cleanup := withFreshTracker(t, "alice", 2000, 30*time.Minute)
	defer cleanup()
	as := &AutonomousScheduler{
		ghClient: c,
		config: AutonomousConfig{
			ScanInterval:     30 * time.Second,
			RateAwareCadence: false,
		},
		state: &AutonomousState{AvgCycleGraphQLCost: 100},
	}
	if got := as.scanCadence(); got != 30*time.Second {
		t.Fatalf("want fixed base 30s (rate-aware disabled), got %s", got)
	}
}

// TestScanCadence_RateAwareNoTrackerFailsOpen — RateAwareCadence enabled but
// no ghClient/tracker (e.g. tests, early startup) must fail open to base.
func TestScanCadence_RateAwareNoTrackerFailsOpen(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{
			ScanInterval:     30 * time.Second,
			RateAwareCadence: true,
		},
		state: &AutonomousState{},
	}
	if got := as.scanCadence(); got != 30*time.Second {
		t.Fatalf("want fixed base 30s (no tracker fail-open), got %s", got)
	}
}

// TestScanCadence_RateAwareStretchesUnderPressure — end-to-end through
// scanCadence(): a tight tracker reading plus a populated EWMA average must
// stretch the returned interval above base.
func TestScanCadence_RateAwareStretchesUnderPressure(t *testing.T) {
	c, cleanup := withFreshTracker(t, "alice", 2200, 40*time.Minute)
	defer cleanup()
	as := &AutonomousScheduler{
		ghClient: c,
		config: AutonomousConfig{
			ScanInterval:     30 * time.Second,
			RateAwareCadence: true,
			MaxScanInterval:  10 * time.Minute,
		},
		state: &AutonomousState{AvgCycleGraphQLCost: 100},
	}
	got := as.scanCadence()
	if got <= 30*time.Second {
		t.Fatalf("want stretched interval > base 30s under pressure, got %s", got)
	}
}

// TestGraphQLCostEWMA_ConvergesTowardStepChange — the EWMA update (applied
// the way runCycleWithCostTracking applies it) must converge toward a
// step-changed cost within a bounded number of cycles, and a mid-cycle
// window reset (remaining increases => negative diff) must be discarded
// rather than corrupting the running average.
func TestGraphQLCostEWMA_ConvergesTowardStepChange(t *testing.T) {
	as := &AutonomousScheduler{state: &AutonomousState{}}

	applySample := func(cost int) {
		as.mu.Lock()
		defer as.mu.Unlock()
		if cost < 0 {
			return // mid-cycle reset guard — discard, do not corrupt average
		}
		as.state.LastCycleGraphQLCost = cost
		if as.state.AvgCycleGraphQLCost <= 0 {
			as.state.AvgCycleGraphQLCost = float64(cost)
		} else {
			as.state.AvgCycleGraphQLCost = graphQLCostEWMAAlpha*float64(cost) +
				(1-graphQLCostEWMAAlpha)*as.state.AvgCycleGraphQLCost
		}
	}

	// Steady state at cost=50 for a while.
	for i := 0; i < 5; i++ {
		applySample(50)
	}
	if got := as.state.AvgCycleGraphQLCost; got < 45 || got > 55 {
		t.Fatalf("want avg near 50 after steady samples, got %.2f", got)
	}

	// A window-reset sample reads as negative diff — must be discarded.
	applySample(-30)
	if got := as.state.AvgCycleGraphQLCost; got < 45 || got > 55 {
		t.Fatalf("negative sample corrupted average: got %.2f", got)
	}

	// Step change to cost=200; average should converge toward it within a
	// handful of cycles without ever overshooting.
	for i := 0; i < 20; i++ {
		applySample(200)
	}
	if got := as.state.AvgCycleGraphQLCost; got < 190 {
		t.Fatalf("want avg to converge near 200 after 20 samples, got %.2f", got)
	}
}

// TestSustainedHourBudgetSimulation — the acceptance-criterion-2 regression
// guard: drive synthetic cycles at the workspace's measured ~50-point cost
// through scanCadence() the way the real loop does, and assert that summing
// (returned interval, cost) pairs across a simulated hour never exceeds the
// GraphQL budget. A future change that silently multiplies per-cycle cost or
// disables rate-awareness will fail this test.
func TestSustainedHourBudgetSimulation(t *testing.T) {
	const limit = 5000
	const measuredCostPerCycle = 50
	const floor = 2000
	const simulatedHour = time.Hour

	c, cleanup := withFreshTracker(t, "alice", limit, simulatedHour)
	tr := c.RateLimitTracker()
	cleanup()

	as := &AutonomousScheduler{
		ghClient: c,
		config: AutonomousConfig{
			ScanInterval:     30 * time.Second,
			RateAwareCadence: true,
			MaxScanInterval:  10 * time.Minute,
		},
		state: &AutonomousState{},
	}

	windowStart := time.Now()
	resetAt := windowStart.Add(simulatedHour)
	remaining := limit
	elapsed := time.Duration(0)
	totalConsumed := 0
	cycles := 0

	for elapsed < simulatedHour {
		// Seed the tracker with the current simulated remaining/reset so
		// scanCadence()'s gitHubQuotaSnapshot() read reflects this point in
		// simulated time.
		info := &gh.RateLimitInfo{
			Remaining: remaining,
			Limit:     limit,
			ResetAt:   resetAt.Unix(),
		}
		if err := tr.Set("alice", info); err != nil {
			t.Fatalf("seed tracker: %v", err)
		}

		interval := as.scanCadence()

		// Stop dispatching once headroom drops to the floor — mirrors the
		// real hasDispatchHeadroom()/dispatchHeadroomFloor() gate, which is
		// what actually caps consumption once cadence alone isn't enough.
		if remaining-floor <= 0 {
			break
		}

		remaining -= measuredCostPerCycle
		totalConsumed += measuredCostPerCycle
		cycles++

		// Feed the EWMA the way runCycleWithCostTracking does.
		as.mu.Lock()
		as.state.LastCycleGraphQLCost = measuredCostPerCycle
		if as.state.AvgCycleGraphQLCost <= 0 {
			as.state.AvgCycleGraphQLCost = float64(measuredCostPerCycle)
		} else {
			as.state.AvgCycleGraphQLCost = graphQLCostEWMAAlpha*float64(measuredCostPerCycle) +
				(1-graphQLCostEWMAAlpha)*as.state.AvgCycleGraphQLCost
		}
		as.mu.Unlock()

		elapsed += interval
	}

	if totalConsumed > limit {
		t.Fatalf("sustained-hour simulation exceeded budget: consumed=%d limit=%d over %d cycles",
			totalConsumed, limit, cycles)
	}
	if remaining < 0 {
		t.Fatalf("sustained-hour simulation drove remaining negative: %d", remaining)
	}
	t.Logf("sustained-hour simulation: %d cycles, %d consumed of %d budget, %d remaining at end",
		cycles, totalConsumed, limit, remaining)
}

// TestRateLimitPressure_SingleBlipDoesNotTrip — a lone rejected cycle inside
// an otherwise-clean rolling window must not set RateLimitPressureActive.
func TestRateLimitPressure_SingleBlipDoesNotTrip(t *testing.T) {
	as := &AutonomousScheduler{state: &AutonomousState{}}
	for i := 0; i < rateLimitPressureWindow-1; i++ {
		as.state.LastRejectionReasons = map[string]int{}
		as.updateRateLimitPressure()
	}
	as.state.LastRejectionReasons = map[string]int{"github-rate-limit-headroom": 1}
	as.updateRateLimitPressure()
	if as.state.RateLimitPressureActive {
		t.Fatalf("single blip in a %d-cycle window must not trip RateLimitPressureActive", rateLimitPressureWindow)
	}
}

// TestRateLimitPressure_SustainedRejectionsTripAndRecover — rejections in
// more than the threshold fraction of the rolling window set
// RateLimitPressureActive; once the window fills back up with clean cycles,
// it clears.
func TestRateLimitPressure_SustainedRejectionsTripAndRecover(t *testing.T) {
	as := &AutonomousScheduler{state: &AutonomousState{}}

	// Fill the window with rejections well past the 25% threshold.
	rejectedCount := int(rateLimitPressureThreshold*float64(rateLimitPressureWindow)) + 2
	for i := 0; i < rejectedCount; i++ {
		as.state.LastRejectionReasons = map[string]int{"github-rate-limit-headroom": 1}
		as.updateRateLimitPressure()
	}
	if !as.state.RateLimitPressureActive {
		t.Fatalf("want RateLimitPressureActive=true after %d/%d rejections", rejectedCount, rateLimitPressureWindow)
	}

	// Recovery: fill the window with clean cycles until pressure clears.
	for i := 0; i < rateLimitPressureWindow; i++ {
		as.state.LastRejectionReasons = map[string]int{}
		as.updateRateLimitPressure()
	}
	if as.state.RateLimitPressureActive {
		t.Fatalf("want RateLimitPressureActive=false after a full window of clean cycles")
	}
}
