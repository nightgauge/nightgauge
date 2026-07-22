package orchestrator

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCascadeTracker_TripsOnThirdFailureInWindow is the headline pin: three
// failures inside the window MUST trip the breaker, and the trip message
// MUST identify the involved issues.
func TestCascadeTracker_TripsOnThirdFailureInWindow(t *testing.T) {
	c := NewCascadeTracker(CascadeTrackerConfig{Threshold: 3, Window: 30 * time.Minute})
	base := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)

	tripped, reason := c.RecordFailure("nightgauge/nightgauge", 100, "stall_kill", base)
	if tripped {
		t.Fatalf("first failure should not trip: %s", reason)
	}
	tripped, reason = c.RecordFailure("nightgauge/nightgauge", 101, "network_unavailable", base.Add(5*time.Minute))
	if tripped {
		t.Fatalf("second failure should not trip: %s", reason)
	}
	tripped, reason = c.RecordFailure("nightgauge/nightgauge", 102, "stop_hook_dropped_commit", base.Add(15*time.Minute))
	if !tripped {
		t.Fatalf("third failure should trip; got tripped=false reason=%q", reason)
	}
	if !strings.Contains(reason, "cascading-failures") {
		t.Errorf("trip reason missing 'cascading-failures' tag: %q", reason)
	}
	if !strings.Contains(reason, "Manual triage required") {
		t.Errorf("trip reason missing operator nudge: %q", reason)
	}
	for _, want := range []string{"#100", "#101", "#102", "stall_kill", "network_unavailable", "stop_hook_dropped_commit"} {
		if !strings.Contains(reason, want) {
			t.Errorf("trip reason missing %q: %q", want, reason)
		}
	}
}

// TestCascadeTracker_DoesNotTripOutsideWindow asserts the sliding-window
// semantics: failures spaced WIDER than the window must NOT trip.
func TestCascadeTracker_DoesNotTripOutsideWindow(t *testing.T) {
	c := NewCascadeTracker(CascadeTrackerConfig{Threshold: 3, Window: 30 * time.Minute})
	base := time.Now()

	c.RecordFailure("r", 1, "x", base)
	c.RecordFailure("r", 2, "x", base.Add(40*time.Minute)) // outside 30m window — prunes first
	tripped, _ := c.RecordFailure("r", 3, "x", base.Add(80*time.Minute))
	if tripped {
		t.Fatalf("failures spaced outside window must not trip")
	}
	// Only two failures in any 30m window — never trips.
	if c.IsTripped() {
		t.Errorf("IsTripped reports true; want false")
	}
}

// TestCascadeTracker_TripsOnlyOnceUntilReset confirms that the breaker
// fires exactly once per trip. A 4th, 5th failure after the trip MUST NOT
// re-emit a trip event so a Discord notifier doesn't get spammed.
func TestCascadeTracker_TripsOnlyOnceUntilReset(t *testing.T) {
	c := NewCascadeTracker(CascadeTrackerConfig{Threshold: 3, Window: 30 * time.Minute})
	base := time.Now()

	c.RecordFailure("r", 1, "x", base)
	c.RecordFailure("r", 2, "x", base.Add(1*time.Minute))
	tripped, _ := c.RecordFailure("r", 3, "x", base.Add(2*time.Minute))
	if !tripped {
		t.Fatalf("third failure should trip")
	}
	for i := 4; i <= 8; i++ {
		got, _ := c.RecordFailure("r", i, "x", base.Add(time.Duration(i)*time.Minute))
		if got {
			t.Errorf("failure #%d re-fired the trip event; want trip-once semantics", i)
		}
	}
	if !c.IsTripped() {
		t.Errorf("IsTripped should still return true after trip-once")
	}
}

// TestCascadeTracker_ResetAllowsRetrip pins the explicit-operator-triage
// requirement: Reset (called from AutonomousScheduler.Resume) must clear
// both the recorded failures and the tripped flag so the breaker can fire
// again on a fresh cascade.
func TestCascadeTracker_ResetAllowsRetrip(t *testing.T) {
	c := NewCascadeTracker(CascadeTrackerConfig{Threshold: 3, Window: 30 * time.Minute})
	base := time.Now()

	c.RecordFailure("r", 1, "x", base)
	c.RecordFailure("r", 2, "x", base)
	c.RecordFailure("r", 3, "x", base)
	if !c.IsTripped() {
		t.Fatalf("setup: expected trip after 3 failures")
	}

	c.Reset()
	if c.IsTripped() {
		t.Fatalf("IsTripped should be false after Reset")
	}
	if got := c.CountInWindow(base); got != 0 {
		t.Fatalf("CountInWindow after Reset = %d, want 0", got)
	}

	// A fresh 3 failures triggers another trip.
	c.RecordFailure("r", 1, "x", base.Add(time.Hour))
	c.RecordFailure("r", 2, "x", base.Add(time.Hour))
	tripped, _ := c.RecordFailure("r", 3, "x", base.Add(time.Hour))
	if !tripped {
		t.Errorf("re-trip after Reset should fire")
	}
}

// TestCascadeTracker_DefaultThresholdAndWindow asserts the documented
// defaults (3 failures in 30 minutes) so a config-less call to
// NewCascadeTracker matches what docs/operator runbook describe.
func TestCascadeTracker_DefaultThresholdAndWindow(t *testing.T) {
	// Clear env so the test's defaults reflect the CONST defaults, not
	// whatever the developer happens to have set locally.
	t.Setenv("NIGHTGAUGE_CASCADE_FAILURE_THRESHOLD", "")
	t.Setenv("NIGHTGAUGE_CASCADE_FAILURE_WINDOW", "")
	c := NewCascadeTracker(CascadeTrackerConfig{})
	if c.Threshold() != DefaultCascadeFailureThreshold {
		t.Errorf("Threshold default = %d, want %d", c.Threshold(), DefaultCascadeFailureThreshold)
	}
	if c.Window() != DefaultCascadeFailureWindow {
		t.Errorf("Window default = %s, want %s", c.Window(), DefaultCascadeFailureWindow)
	}
}

// TestCascadeTracker_EnvOverridesApply confirms that the env-var knobs
// land on the constructed tracker so an operator can tune without rebuild.
func TestCascadeTracker_EnvOverridesApply(t *testing.T) {
	t.Setenv("NIGHTGAUGE_CASCADE_FAILURE_THRESHOLD", "5")
	t.Setenv("NIGHTGAUGE_CASCADE_FAILURE_WINDOW", "1h")
	c := NewCascadeTracker(CascadeTrackerConfig{}) // env wins over cfg
	if c.Threshold() != 5 {
		t.Errorf("Threshold env override = %d, want 5", c.Threshold())
	}
	if c.Window() != time.Hour {
		t.Errorf("Window env override = %s, want 1h", c.Window())
	}
}

// TestCascadeTracker_EnvOverridesIgnoreMalformed asserts that a typo in a
// shell rc file never bricks the autonomous loop — malformed env values
// fall back to defaults rather than panicking.
func TestCascadeTracker_EnvOverridesIgnoreMalformed(t *testing.T) {
	t.Setenv("NIGHTGAUGE_CASCADE_FAILURE_THRESHOLD", "notanumber")
	t.Setenv("NIGHTGAUGE_CASCADE_FAILURE_WINDOW", "notaduration")
	c := NewCascadeTracker(CascadeTrackerConfig{})
	if c.Threshold() != DefaultCascadeFailureThreshold {
		t.Errorf("malformed threshold env should fall back to default; got %d", c.Threshold())
	}
	if c.Window() != DefaultCascadeFailureWindow {
		t.Errorf("malformed window env should fall back to default; got %s", c.Window())
	}
}

// TestCascadeTracker_CountInWindowPrunesOld checks the housekeeping path:
// CountInWindow must drop entries older than `window` before reporting.
func TestCascadeTracker_CountInWindowPrunesOld(t *testing.T) {
	c := NewCascadeTracker(CascadeTrackerConfig{Threshold: 99, Window: 10 * time.Minute})
	base := time.Now()
	c.RecordFailure("r", 1, "x", base)
	c.RecordFailure("r", 2, "x", base.Add(1*time.Minute))
	c.RecordFailure("r", 3, "x", base.Add(2*time.Minute))

	// Probe at base+15m — the first three entries are all older than 10m,
	// so the window count must be zero.
	if got := c.CountInWindow(base.Add(15 * time.Minute)); got != 0 {
		t.Errorf("CountInWindow after window expiry = %d, want 0", got)
	}
}

// TestCascadeTracker_ConcurrentRecordIsSafe shotguns RecordFailure from
// many goroutines and asserts the breaker still trips deterministically.
// Race-detector friendly — the internal mutex must serialize every state
// mutation.
func TestCascadeTracker_ConcurrentRecordIsSafe(t *testing.T) {
	c := NewCascadeTracker(CascadeTrackerConfig{Threshold: 5, Window: time.Hour})
	base := time.Now()

	var wg sync.WaitGroup
	const writers = 20
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.RecordFailure("r", i, "x", base.Add(time.Duration(i)*time.Second))
		}(i)
	}
	wg.Wait()

	// 20 failures, threshold 5, window 1h → breaker must have tripped.
	if !c.IsTripped() {
		t.Errorf("breaker not tripped after 20 concurrent failures")
	}
	if got := c.CountInWindow(base.Add(time.Minute)); got < 5 {
		t.Errorf("CountInWindow = %d, want >= 5", got)
	}
}

// TestCascadeTracker_TripIncludesIssueRefsForOperator pins the operator-
// facing trip-reason shape: every involved issue must appear so the
// status bar / Discord embed can list "what tripped me" without the
// operator having to read the daily JSONL.
func TestCascadeTracker_TripIncludesIssueRefsForOperator(t *testing.T) {
	c := NewCascadeTracker(CascadeTrackerConfig{Threshold: 3, Window: 30 * time.Minute})
	base := time.Now()
	c.RecordFailure("nightgauge/nightgauge", 3365, "stop_hook_dropped_commit", base)
	c.RecordFailure("nightgauge/nightgauge", 3499, "stall_kill", base.Add(1*time.Minute))
	tripped, reason := c.RecordFailure("nightgauge/nightgauge", 3591, "subagent_crash", base.Add(2*time.Minute))
	if !tripped {
		t.Fatalf("expected trip")
	}
	for _, want := range []string{
		"nightgauge/nightgauge#3365",
		"nightgauge/nightgauge#3499",
		"nightgauge/nightgauge#3591",
		"stop_hook_dropped_commit",
		"stall_kill",
		"subagent_crash",
	} {
		if !strings.Contains(reason, want) {
			t.Errorf("trip reason missing %q\nfull reason: %s", want, reason)
		}
	}
}

// TestCascadeTracker_ZeroIssueNumberRendersGracefully covers the
// orchestrator-level safety-trip case where a failure isn't tied to a
// specific issue. The trip message must still be readable.
func TestCascadeTracker_ZeroIssueNumberRendersGracefully(t *testing.T) {
	c := NewCascadeTracker(CascadeTrackerConfig{Threshold: 2, Window: 30 * time.Minute})
	base := time.Now()
	c.RecordFailure("r", 0, "rate-limit-cooldown-expired", base)
	tripped, reason := c.RecordFailure("r", 0, "rate-limit-cooldown-expired", base.Add(1*time.Minute))
	if !tripped {
		t.Fatalf("expected trip")
	}
	if !strings.Contains(reason, "(no issue)") {
		t.Errorf("zero-issue trip reason missing '(no issue)' marker: %s", reason)
	}
}

// TestOnPipelineComplete_CascadeTripsAndPauses verifies the
// onPipelineComplete → cascadeTracker → safety_tripped status pipeline. After
// three generic-failure completions inside the window, the autonomous state
// MUST transition to safety_tripped with PauseTriggeredBy=CascadePauseReason.
//
// The test exercises ONLY the generic failure branch (terminalFailureKind=""),
// since the kind-specific branches (stall_kill, quota_exhausted, ...) short-
// circuit before the cascade tracker as documented in onPipelineComplete.
func TestOnPipelineComplete_CascadeTripsAndPauses(t *testing.T) {
	t.Setenv("NIGHTGAUGE_CASCADE_FAILURE_THRESHOLD", "")
	t.Setenv("NIGHTGAUGE_CASCADE_FAILURE_WINDOW", "")
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status:                "running",
			Running:               []RunningItem{},
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
		cascadeTracker:       NewCascadeTracker(CascadeTrackerConfig{Threshold: 3, Window: 30 * time.Minute}),
	}

	// Three back-to-back generic failures, threshold == 3.
	as.state.Running = []RunningItem{{Repo: "R", Number: 1}}
	as.onPipelineComplete("R", 1, false, false, "", "")
	if as.state.Status == "safety_tripped" {
		t.Fatalf("status tripped after first failure; want still running")
	}

	as.state.Running = []RunningItem{{Repo: "R", Number: 2}}
	as.onPipelineComplete("R", 2, false, false, "", "")
	if as.state.Status == "safety_tripped" {
		t.Fatalf("status tripped after second failure; want still running")
	}

	as.state.Running = []RunningItem{{Repo: "R", Number: 3}}
	as.onPipelineComplete("R", 3, false, false, "", "")
	if as.state.Status != "safety_tripped" {
		t.Fatalf("status = %q after third failure, want safety_tripped", as.state.Status)
	}
	if as.state.PauseTriggeredBy != CascadePauseReason {
		t.Errorf("PauseTriggeredBy = %q, want %q", as.state.PauseTriggeredBy, CascadePauseReason)
	}
	if !strings.Contains(as.state.PauseReason, "cascading-failures") {
		t.Errorf("PauseReason missing cascading-failures tag: %q", as.state.PauseReason)
	}
	if !strings.Contains(as.state.PauseReason, "#1") ||
		!strings.Contains(as.state.PauseReason, "#2") ||
		!strings.Contains(as.state.PauseReason, "#3") {
		t.Errorf("PauseReason missing issue refs: %q", as.state.PauseReason)
	}
	if as.state.Safety == nil || as.state.Safety.TripReason == "" {
		t.Errorf("Safety.TripReason missing; want preserved cascade reason")
	}
}

// TestOnPipelineComplete_TwoFailuresPlusOneOutsideWindow_NoTrip pins the
// sliding-window semantics through the orchestrator: a stale failure must
// not contribute to the trip count.
func TestOnPipelineComplete_TwoFailuresPlusOneOutsideWindow_NoTrip(t *testing.T) {
	t.Setenv("NIGHTGAUGE_CASCADE_FAILURE_THRESHOLD", "")
	t.Setenv("NIGHTGAUGE_CASCADE_FAILURE_WINDOW", "")
	tracker := NewCascadeTracker(CascadeTrackerConfig{Threshold: 3, Window: 5 * time.Millisecond})
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status:                "running",
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
		cascadeTracker:       tracker,
	}

	as.state.Running = []RunningItem{{Repo: "R", Number: 1}}
	as.onPipelineComplete("R", 1, false, false, "", "")
	// Sleep past the 5ms window so the first failure prunes out before the
	// next two land. Without the prune, this would trip.
	time.Sleep(10 * time.Millisecond)
	as.state.Running = []RunningItem{{Repo: "R", Number: 2}}
	as.onPipelineComplete("R", 2, false, false, "", "")
	as.state.Running = []RunningItem{{Repo: "R", Number: 3}}
	as.onPipelineComplete("R", 3, false, false, "", "")

	if as.state.Status == "safety_tripped" {
		t.Errorf("status = safety_tripped; the first failure was outside the 5ms window and must have pruned")
	}
}

// TestOnPipelineComplete_StallKillDoesNotFeedCascade verifies that
// stall-kill (and other short-circuited terminal kinds) never reach the
// cascade tracker. A sequence of three stall-kills should NOT trip the
// breaker because stall-kills are transient infrastructure, not code-quality
// failures, and the autonomous handler returns before the cascade feed.
func TestOnPipelineComplete_StallKillDoesNotFeedCascade(t *testing.T) {
	t.Setenv("NIGHTGAUGE_CASCADE_FAILURE_THRESHOLD", "")
	t.Setenv("NIGHTGAUGE_CASCADE_FAILURE_WINDOW", "")
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status:                "running",
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
		cascadeTracker:       NewCascadeTracker(CascadeTrackerConfig{Threshold: 3, Window: 30 * time.Minute}),
	}

	for i := 1; i <= 5; i++ {
		as.state.Running = []RunningItem{{Repo: "R", Number: i}}
		as.onPipelineComplete("R", i, false, false, TerminalKindStallKill, "")
	}

	if as.state.Status == "safety_tripped" {
		t.Errorf("status = safety_tripped after 5 stall-kills; stall-kills must NOT feed cascade tracker")
	}
	if got := as.cascadeTracker.CountInWindow(time.Now()); got != 0 {
		t.Errorf("cascadeTracker recorded %d stall-kill failures; want 0", got)
	}
}

// TestResume_ClearsCascadeBreaker verifies the manual-triage contract: an
// operator Resume must clear both the recorded failures AND the tripped flag
// so the next 3 failures fire a fresh cascade rather than auto-firing on
// what's already in the window.
func TestResume_ClearsCascadeBreaker(t *testing.T) {
	t.Setenv("NIGHTGAUGE_CASCADE_FAILURE_THRESHOLD", "")
	t.Setenv("NIGHTGAUGE_CASCADE_FAILURE_WINDOW", "")
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status:                "running",
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
		conflictRestartCount: map[string]int{},
		refinementCooldown:   map[string]time.Time{},
		refinementFailures:   map[string]int{},
		cascadeTracker:       NewCascadeTracker(CascadeTrackerConfig{Threshold: 3, Window: 30 * time.Minute}),
	}

	// Trip the breaker via three generic failures.
	for i := 1; i <= 3; i++ {
		as.state.Running = []RunningItem{{Repo: "R", Number: i}}
		as.onPipelineComplete("R", i, false, false, "", "")
	}
	if as.state.Status != "safety_tripped" {
		t.Fatalf("setup: status = %q, want safety_tripped", as.state.Status)
	}
	if !as.cascadeTracker.IsTripped() {
		t.Fatalf("setup: cascadeTracker.IsTripped() = false, want true")
	}

	as.Resume()

	if as.state.Status != "running" {
		t.Errorf("status after Resume = %q, want running", as.state.Status)
	}
	if as.cascadeTracker.IsTripped() {
		t.Errorf("cascadeTracker still tripped after Resume; want reset")
	}
	if got := as.cascadeTracker.CountInWindow(time.Now()); got != 0 {
		t.Errorf("cascadeTracker has %d entries after Resume; want 0", got)
	}
}
