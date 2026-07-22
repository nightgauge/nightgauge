package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// #3976: WithRateLimitWait flips the pre-call gate from fail-fast to
// wait-for-reset, bounded by the caller's context. These tests cover the new
// wait path for both GraphQL (query) and REST, plus the REST 429 retry loop
// (REST previously had NO rate-limit handling at all).

// TestRateLimitGate_WaitsThenProceeds_WhenEnabled verifies that with
// WithRateLimitWait and a near reset, the gate WAITS and then dispatches the
// call (rather than returning ErrRateLimitGated).
func TestRateLimitGate_WaitsThenProceeds_WhenEnabled(t *testing.T) {
	var calls int32
	srv := graphQLProbeServer(t, nil, &calls)
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)
	// Below floor, reset ~1s out so the wait is short and then clears.
	resetAt := time.Now().Add(1 * time.Second).Unix()
	if err := tr.Set("alice", &RateLimitInfo{Remaining: 5, Limit: 5000, ResetAt: resetAt}); err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	t.Setenv(rateLimitFloorEnv, "100")

	c := NewClientWithURL("test-token", srv.URL).WithRateLimitTracker(tr, "alice").WithRateLimitWait()

	start := time.Now()
	if _, err := c.GetRepositoryID(context.Background(), "nightgauge", "nightgauge"); err != nil {
		t.Fatalf("expected success after waiting out the reset, got %v", err)
	}
	if waited := time.Since(start); waited < 500*time.Millisecond {
		t.Errorf("expected the gate to wait ~1s for reset, only waited %s", waited)
	}
	if got := atomic.LoadInt32(&calls); got == 0 {
		t.Fatal("expected the call to dispatch after the wait, got 0 HTTP calls")
	}
}

// TestRateLimitGate_WaitRespectsContext verifies the wait is bounded by the
// caller's context: a short-deadline call bails with the ctx error (NOT
// ErrRateLimitGated) instead of blocking for the full reset window.
func TestRateLimitGate_WaitRespectsContext(t *testing.T) {
	var calls int32
	srv := graphQLProbeServer(t, nil, &calls)
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)
	resetAt := time.Now().Add(20 * time.Minute).Unix() // far away
	if err := tr.Set("alice", &RateLimitInfo{Remaining: 5, Limit: 5000, ResetAt: resetAt}); err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	t.Setenv(rateLimitFloorEnv, "100")

	c := NewClientWithURL("test-token", srv.URL).WithRateLimitTracker(tr, "alice").WithRateLimitWait()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := c.GetRepositoryID(ctx, "nightgauge", "nightgauge")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded from the bounded wait, got %v", err)
	}
	if errors.Is(err, ErrRateLimitGated) {
		t.Error("wait-enabled client must not fail-fast with ErrRateLimitGated")
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("expected 0 dispatches while gated+waiting, got %d", got)
	}
}

// TestREST_GateFailFastWithoutWait verifies REST now honors the rate-limit gate
// (previously REST had none): without WithRateLimitWait a below-floor reading
// fails fast with ErrRateLimitGated.
func TestREST_GateFailFastWithoutWait(t *testing.T) {
	var calls int32
	srv := graphQLProbeServer(t, nil, &calls)
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)
	resetAt := time.Now().Add(20 * time.Minute).Unix()
	if err := tr.Set("alice", &RateLimitInfo{Remaining: 5, Limit: 5000, ResetAt: resetAt}); err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	t.Setenv(rateLimitFloorEnv, "100")

	c := NewClientWithURL("test-token", srv.URL).WithRateLimitTracker(tr, "alice") // no wait

	_, err := c.restGet(context.Background(), "/repos/o/r")
	if !errors.Is(err, ErrRateLimitGated) {
		t.Fatalf("expected REST to fail fast with ErrRateLimitGated, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("expected 0 REST dispatches when gated, got %d", got)
	}
}

// TestREST_RetriesOnRateLimitResponse verifies the REST retry loop: a 429 with a
// Retry-After header and rate-limit body is retried (after the honored delay)
// and the follow-up 200 succeeds. No tracker → the gate is a no-op, isolating
// the response-path retry.
func TestREST_RetriesOnRateLimitResponse(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for installation"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewClientWithURL("test-token", srv.URL)

	body, err := c.restGet(context.Background(), "/repos/o/r")
	if err != nil {
		t.Fatalf("expected success after rate-limit retry, got %v", err)
	}
	if !strings.Contains(string(body), "ok") {
		t.Errorf("expected the post-retry 200 body, got %q", string(body))
	}
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Fatalf("expected 1 retry (2 total calls), got %d", got)
	}
}

// TestREST_DoesNotRetryGenuine403 verifies a non-rate-limit 403 (permission) is
// returned immediately, not retried.
func TestREST_DoesNotRetryGenuine403(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	}))
	defer srv.Close()

	c := NewClientWithURL("test-token", srv.URL)

	if _, err := c.restGet(context.Background(), "/repos/o/r"); err == nil {
		t.Fatal("expected an error for a genuine 403")
	}
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("expected no retry on a permission 403, got %d calls", got)
	}
}
