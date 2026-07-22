package orchestrator

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

// Package-level constants for the cascading-failure circuit breaker (#3605
// bullet C). The defaults are aggressive but not paranoid: three failures
// inside a 30-minute window is a strong "something is structurally wrong"
// signal that warrants explicit operator triage rather than yet another
// auto-resume.
//
// Both knobs are overridable at process start via env vars so an operator
// can dial the breaker without a rebuild. The cascade tracker reads them
// once at construction; subsequent changes require a restart.
//
//	NIGHTGAUGE_CASCADE_FAILURE_THRESHOLD  default 3
//	NIGHTGAUGE_CASCADE_FAILURE_WINDOW     default 30m
//
// See docs/CASCADE_CIRCUIT_BREAKER.md for the design rationale, on-disk
// state, and operator runbook.
const (
	// DefaultCascadeFailureThreshold is the number of pipeline failures within
	// the sliding window that trip the breaker.
	DefaultCascadeFailureThreshold = 3
	// DefaultCascadeFailureWindow is the sliding-window duration for the
	// failure count. 30 minutes is wide enough to capture related failures
	// across N pipelines (a rate-limit storm fans out across this much
	// wall-clock; #3499 / #3544 retros showed clustered failures land
	// inside ~20 minutes from the first one) without being so wide that
	// an early-morning failure plus an unrelated mid-day failure trips
	// the breaker incorrectly.
	DefaultCascadeFailureWindow = 30 * time.Minute
	// CascadePauseReason is the canonical PauseTriggeredBy tag for cascade
	// pauses. Recorded on state.json so retros / dashboards can group by it.
	// NOT in any auto-resume allowlist — clearing requires explicit user
	// Resume() (which is the default behaviour of Resume()).
	CascadePauseReason = "safety:cascading-failures"
)

// CascadeTrackerConfig holds the tunables for the cascading-failure breaker.
// Zero values fall back to the package defaults so the autonomous scheduler
// can construct a tracker with `NewCascadeTracker(CascadeTrackerConfig{})`
// and get sensible behaviour.
type CascadeTrackerConfig struct {
	// Threshold is the failure count that trips the breaker. <=0 falls back
	// to DefaultCascadeFailureThreshold.
	Threshold int
	// Window is the sliding window over which failures are counted. <=0
	// falls back to DefaultCascadeFailureWindow.
	Window time.Duration
}

// CascadeFailureEntry is one in-window failure observation. Persisted-only
// in-memory; the breaker is intentionally scoped to a single autonomous
// process run so an operator restart implicitly resets it.
type CascadeFailureEntry struct {
	Repo      string    `json:"repo"`
	Number    int       `json:"number"`
	Timestamp time.Time `json:"timestamp"`
	// Reason is the human-readable terminal kind / pause reason that caused
	// this failure to be recorded. Surfaced on the trip message so the
	// operator sees what cluster the breaker fired on without re-reading
	// the daily JSONL.
	Reason string `json:"reason"`
}

// CascadeTracker is a sliding-window failure tracker that trips when the
// number of pipeline failures inside Window meets or exceeds Threshold.
//
// The tracker is intentionally simple: a slice of timestamps pruned on each
// RecordFailure / IsTripped call. The slice never grows beyond Threshold
// recent entries because everything older than Window is dropped on every
// access, so the data structure is bounded by O(Threshold) memory in
// steady state.
//
// Thread-safety: every public method acquires the internal mutex.
type CascadeTracker struct {
	threshold int
	window    time.Duration

	mu       sync.Mutex
	entries  []CascadeFailureEntry
	tripped  bool
	trippedAt time.Time
}

// NewCascadeTracker constructs a tracker from cfg. Zero / negative values in
// cfg fall back to package defaults. Env-var overrides
// (NIGHTGAUGE_CASCADE_FAILURE_THRESHOLD / _WINDOW) take precedence over
// cfg so an operator can dial without code change.
func NewCascadeTracker(cfg CascadeTrackerConfig) *CascadeTracker {
	threshold := cfg.Threshold
	if threshold <= 0 {
		threshold = DefaultCascadeFailureThreshold
	}
	window := cfg.Window
	if window <= 0 {
		window = DefaultCascadeFailureWindow
	}

	// Env overrides — silently ignored when malformed so a typo in a shell
	// rc file never bricks the autonomous loop. The malformed value path
	// is exercised by TestCascadeTracker_EnvOverridesIgnoreMalformed.
	if env := os.Getenv("NIGHTGAUGE_CASCADE_FAILURE_THRESHOLD"); env != "" {
		if v, err := strconv.Atoi(env); err == nil && v > 0 {
			threshold = v
		}
	}
	if env := os.Getenv("NIGHTGAUGE_CASCADE_FAILURE_WINDOW"); env != "" {
		if v, err := time.ParseDuration(env); err == nil && v > 0 {
			window = v
		}
	}

	return &CascadeTracker{
		threshold: threshold,
		window:    window,
	}
}

// Threshold returns the configured failure-count threshold. Exported for
// tests and the operator-facing logging that records "X failures in Yh"
// on the trip message.
func (c *CascadeTracker) Threshold() int { return c.threshold }

// Window returns the sliding window duration. Exported for the same
// reasons as Threshold.
func (c *CascadeTracker) Window() time.Duration { return c.window }

// RecordFailure adds one failure entry to the window. now is injectable so
// tests can drive deterministic time without depending on time.Now().
// Returns (tripped, reasonIfTripped):
//   - tripped == true on the first call that crosses the threshold, with
//     reasonIfTripped containing a human-readable summary suitable for
//     PauseReason.
//   - tripped == false otherwise (sub-threshold OR already-tripped — the
//     breaker only fires once until Reset, so a 5th failure after a 3-failure
//     trip does NOT re-report).
//
// Callers MUST pass a non-empty repo string. number may be zero for
// non-issue failures (e.g. orchestrator-level safety trips) — the entry is
// still recorded for window accounting but is rendered as "(no issue)" on
// the trip message.
func (c *CascadeTracker) RecordFailure(repo string, number int, reason string, now time.Time) (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.tripped {
		// Already tripped; subsequent failures still get recorded for
		// post-trip retro forensics (CountInWindow continues to grow) but
		// the trip event is only fired once.
		c.entries = append(c.entries, CascadeFailureEntry{
			Repo:      repo,
			Number:    number,
			Timestamp: now,
			Reason:    reason,
		})
		return false, ""
	}

	c.pruneLocked(now)
	c.entries = append(c.entries, CascadeFailureEntry{
		Repo:      repo,
		Number:    number,
		Timestamp: now,
		Reason:    reason,
	})

	if len(c.entries) < c.threshold {
		return false, ""
	}

	c.tripped = true
	c.trippedAt = now
	return true, c.summarizeLocked(now)
}

// IsTripped returns whether the breaker has fired. Used by callers that
// want to read the trip state without changing it (e.g. a status endpoint).
func (c *CascadeTracker) IsTripped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tripped
}

// Reset clears every recorded failure AND the tripped flag. Called from
// AutonomousScheduler.Resume() so an explicit user resume bypasses the
// breaker. NOT called from any auto-resume / self-clear path — cascade
// trips require explicit operator triage (the whole point).
func (c *CascadeTracker) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = nil
	c.tripped = false
	c.trippedAt = time.Time{}
}

// CountInWindow returns the number of failures currently inside the sliding
// window. Used by tests and by the autonomous status snapshot so a
// dashboard can render "X/Y in last Z" without re-implementing the prune
// rules.
func (c *CascadeTracker) CountInWindow(now time.Time) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)
	return len(c.entries)
}

// pruneLocked drops every entry older than (now - window). Caller must
// hold c.mu.
func (c *CascadeTracker) pruneLocked(now time.Time) {
	cutoff := now.Add(-c.window)
	if len(c.entries) == 0 {
		return
	}
	// Most failures are appended in chronological order, so the common
	// case is a single tail-trim — but we don't assume monotonic time in
	// case a caller passes a backdated `now` (tests sometimes do). A
	// linear scan is fine at the threshold-bounded size we operate on.
	out := c.entries[:0]
	for _, e := range c.entries {
		if e.Timestamp.After(cutoff) || e.Timestamp.Equal(cutoff) {
			out = append(out, e)
		}
	}
	c.entries = out
}

// summarizeLocked renders the trip-event human-readable summary that lands
// on PauseReason. Caller must hold c.mu.
//
// Format:
//   "cascading-failures: 3 pipeline failures in the last 30m0s
//    (owner/repo#100: stall_kill; owner/repo#101: network_unavailable;
//    owner/repo#102: stop_hook_dropped_commit). Manual triage required."
//
// The closing "Manual triage required" sentence is deliberate — every
// status-bar / Discord / log reader sees the same operator-facing nudge
// so the next action is unambiguous.
func (c *CascadeTracker) summarizeLocked(now time.Time) string {
	var failures string
	tail := c.entries
	// Render only the last `threshold` entries so the line stays compact
	// when the tracker is re-tripped after many failures.
	if len(tail) > c.threshold {
		tail = tail[len(tail)-c.threshold:]
	}
	for i, e := range tail {
		if i > 0 {
			failures += "; "
		}
		issueRef := "(no issue)"
		if e.Number > 0 {
			issueRef = fmt.Sprintf("%s#%d", e.Repo, e.Number)
		}
		reason := e.Reason
		if reason == "" {
			reason = "unspecified"
		}
		failures += fmt.Sprintf("%s: %s", issueRef, reason)
	}
	return fmt.Sprintf(
		"cascading-failures: %d pipeline failures in the last %s (%s). Manual triage required.",
		len(c.entries), c.window, failures,
	)
}
