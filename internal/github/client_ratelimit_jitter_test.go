package github

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Release jitter and the per-process release governor on the rate-limit gate.
//
// Every process on a machine reads the same ~/.nightgauge/rate-limit.json, so
// before this every gated caller in every process slept until reset+500ms and
// woke as one wave; the fresh window was drained in minutes (2026-09-03).
// Three properties are pinned here: the jitter is bounded and injectable, the
// jitter is actually added to the wait, and a burst of gated callers leaves
// the gate one per interval rather than all at once.

// gateTestHooks makes a client's gate deterministic: a fixed uniform variate,
// an explicit jitter ceiling, and a private governor so tests do not pace each
// other through the process-wide one.
func gateTestHooks(c *Client, variate float64, jitterMax, interval time.Duration) *gateReleaseGovernor {
	g := newGateReleaseGovernor(interval)
	c.mu.Lock()
	c.gateRand = func() float64 { return variate }
	c.gateJitterMax = jitterMax
	c.gateGovernor = g
	c.mu.Unlock()
	return g
}

// disableGateJitter zeroes the jitter and gives the client a private governor
// so pre-existing wait tests keep their original timing.
func disableGateJitter(c *Client) {
	gateTestHooks(c, 0, time.Nanosecond, time.Nanosecond)
}

func seedGatedTracker(t *testing.T, resetOffsetSeconds int64) *SharedRateLimitTracker {
	t.Helper()
	tr := NewSharedRateLimitTracker(filepath.Join(t.TempDir(), "rate-limit.json"))
	// Whole seconds on a whole-second reading, see the #923 note in
	// client_ratelimit_wait_test.go.
	resetAt := time.Now().Unix() + resetOffsetSeconds
	if err := tr.Set("alice", &RateLimitInfo{Remaining: 5, Limit: 5000, ResetAt: resetAt}); err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	t.Setenv(rateLimitFloorEnv, "100")
	return tr
}

func TestGateJitter_BoundsAndDefault(t *testing.T) {
	c := NewClientWithURL("tok", "http://unused")

	gateTestHooks(c, 0, 10*time.Second, time.Second)
	if got := c.gateJitter(); got != 0 {
		t.Errorf("variate 0 → jitter %s, want 0", got)
	}

	gateTestHooks(c, 0.999, 10*time.Second, time.Second)
	if got := c.gateJitter(); got >= 10*time.Second || got < 9*time.Second {
		t.Errorf("variate 0.999 with max 10s → jitter %s, want in [9s, 10s)", got)
	}

	// Zero ceiling falls back to the 30s default.
	gateTestHooks(c, 0.5, 0, time.Second)
	if got := c.gateJitter(); got != defaultGateJitterMax/2 {
		t.Errorf("variate 0.5 with default max → jitter %s, want %s", got, defaultGateJitterMax/2)
	}
}

// TestRateLimitGate_JitterExtendsWait verifies the jitter is added to the
// sleep, not merely computed. The reset is seeded 2s out; a second boundary
// crossed during setup still leaves ≥1s of reset. Without jitter the gate
// sleeps at most 2.5s; with ~2s of jitter it sleeps ≥ 3.5s, so the bound
// below can only be met by the jitter actually being added.
func TestRateLimitGate_JitterExtendsWait(t *testing.T) {
	tr := seedGatedTracker(t, 2)
	c := NewClientWithURL("tok", "http://unused").WithRateLimitTracker(tr, "alice").WithRateLimitWait()
	gateTestHooks(c, 0.999, 2*time.Second, time.Nanosecond)

	start := time.Now()
	if err := c.waitRateLimitGate(context.Background()); err != nil {
		t.Fatalf("waitRateLimitGate: %v", err)
	}
	if waited := time.Since(start); waited < 3450*time.Millisecond {
		t.Errorf("waited %s; want ≥ 3.45s (≥1s reset + 500ms + ~2s jitter)", waited)
	}
}

// TestRateLimitGate_GovernorSpreadsBurst is the lockstep regression: five
// callers gated on the same reset must leave the gate at least one interval
// apart, so the burst spans ≥ (N-1)·interval, and the opening is logged once
// naming how many were queued.
func TestRateLimitGate_GovernorSpreadsBurst(t *testing.T) {
	const (
		n        = 5
		interval = 500 * time.Millisecond
	)
	tr := seedGatedTracker(t, 2)
	logger, logs := captureLogger()
	c := NewClientWithURL("tok", "http://unused").WithRateLimitTracker(tr, "alice").WithRateLimitWait()
	c.mu.Lock()
	c.gateLogger = logger
	c.mu.Unlock()
	gateTestHooks(c, 0, time.Nanosecond, interval)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		released []time.Time
		errs     []error
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := c.waitRateLimitGate(ctx)
			mu.Lock()
			released = append(released, time.Now())
			errs = append(errs, err)
			mu.Unlock()
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Fatalf("gated caller returned %v", err)
		}
	}
	first, last := released[0], released[0]
	for _, ts := range released[1:] {
		if ts.Before(first) {
			first = ts
		}
		if ts.After(last) {
			last = ts
		}
	}
	if spread := last.Sub(first); spread < (n-1)*interval {
		t.Errorf("%d gated callers released within %s; want ≥ %s — they left the gate as one wave",
			n, spread, (n-1)*interval)
	}

	var opened []string
	for _, line := range logs() {
		if strings.Contains(line, "rate limit gate opened") {
			opened = append(opened, line)
		}
	}
	if len(opened) != 1 {
		t.Fatalf("want exactly one 'gate opened' line, got %d: %q", len(opened), opened)
	}
	if !strings.Contains(opened[0], "5 caller(s) queued") {
		t.Errorf("gate-opened line does not name the queue depth: %q", opened[0])
	}
}

// TestRateLimitGate_GovernorHonoursContext: a caller cancelled while waiting
// for its release slot returns ctx.Err() and leaves the queue.
func TestRateLimitGate_GovernorHonoursContext(t *testing.T) {
	g := newGateReleaseGovernor(5 * time.Second)
	logger, _ := captureLogger()
	g.enqueue()
	g.enqueue()
	if err := g.release(context.Background(), logger); err != nil {
		t.Fatalf("first release: %v", err)
	}
	g.dequeue()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := g.release(ctx, logger); err != context.DeadlineExceeded {
		t.Fatalf("second release: got %v, want context.DeadlineExceeded", err)
	}
	g.dequeue()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.queued != 0 || g.open {
		t.Errorf("after the episode queued=%d open=%v; want 0/false", g.queued, g.open)
	}
}
