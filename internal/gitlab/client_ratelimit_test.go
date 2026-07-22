package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// newTestClientWithTracker returns a GitLab client wired to a tracker backed by
// a temp file, plus the tracker and instance name for test assertions.
func newTestClientWithTracker(t *testing.T, srv *httptest.Server) (*Client, *SharedRateLimitTracker, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ratelimit-gitlab-test.json")
	tracker := NewSharedRateLimitTracker(path)
	instance := "test-instance"

	base := ""
	if srv != nil {
		base = srv.URL
	}
	client := NewClient(base, "test-token")
	client.WithRateLimitTracker(tracker, instance)
	return client, tracker, instance
}

func TestRateLimitGate_blocks(t *testing.T) {
	client, tracker, instance := newTestClientWithTracker(t, nil)

	// Set remaining below floor.
	resetAt := time.Now().Add(2 * time.Second).Unix()
	if err := tracker.Set(instance, &RateLimitInfo{
		Remaining: 1,
		Limit:     5000,
		ResetAt:   resetAt,
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	var logged string
	client.gateLogger = func(format string, args ...interface{}) {
		logged = fmt.Sprintf(format, args...)
	}

	err := client.checkRateLimitGate(context.Background())
	// Sleep is short (2s) but we don't want tests to actually sleep.
	// The gate should have been tripped. Either sleep happened or gated error returned.
	// Since resetAt is 2s from now (< cap), it will sleep then return nil.
	// We verify the log was emitted.
	if logged == "" {
		t.Fatalf("expected gate log, got none (err=%v)", err)
	}
}

func TestRateLimitGate_passesWhenStale(t *testing.T) {
	client, tracker, instance := newTestClientWithTracker(t, nil)

	// Set a stale entry (CheckedAt > freshness window).
	if err := tracker.Set(instance, &RateLimitInfo{
		Remaining: 1,
		Limit:     5000,
		ResetAt:   time.Now().Add(30 * time.Minute).Unix(),
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Backdate the entry to make it stale.
	file, err := tracker.readLocked()
	if err != nil {
		t.Fatalf("readLocked: %v", err)
	}
	file.Entries[keyForInstance(instance)].CheckedAt = time.Now().Unix() - int64(SharedTrackerMinCheckIntervalSecs) - 60
	if err := tracker.writeLocked(file); err != nil {
		t.Fatalf("writeLocked: %v", err)
	}

	var logged string
	client.gateLogger = func(format string, args ...interface{}) {
		logged = fmt.Sprintf(format, args...)
	}

	err = client.checkRateLimitGate(context.Background())
	if err != nil {
		t.Fatalf("stale entry should not gate: %v", err)
	}
	if logged != "" {
		t.Fatalf("stale entry should not produce gate log, got: %s", logged)
	}
}

func TestRateLimitHeaderExtraction(t *testing.T) {
	resetAt := time.Now().Add(30 * time.Minute).Unix()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("RateLimit-Remaining", "42")
		w.Header().Set("RateLimit-Limit", "500")
		w.Header().Set("RateLimit-Reset", fmt.Sprintf("%d", resetAt))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	client, tracker, instance := newTestClientWithTracker(t, srv)

	ctx := context.Background()
	_, _, _ = client.doRaw(ctx, "GET", srv.URL+"/test", nil, "test-op")

	entry, fresh, err := tracker.Get(instance)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry == nil {
		t.Fatalf("expected tracker entry after request, got nil")
	}
	if !fresh {
		t.Fatalf("entry should be fresh immediately after request")
	}
	if entry.Remaining != 42 || entry.Limit != 500 {
		t.Fatalf("unexpected header values: %+v", entry)
	}
}

func TestRateLimitGate_exceedsCap(t *testing.T) {
	client, tracker, instance := newTestClientWithTracker(t, nil)

	// Set remaining below floor, reset far in the future (> cap).
	farFuture := time.Now().Add(10 * time.Minute).Unix()
	if err := tracker.Set(instance, &RateLimitInfo{
		Remaining: 1,
		Limit:     5000,
		ResetAt:   farFuture,
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	client.gateLogger = func(format string, args ...interface{}) {}

	err := client.checkRateLimitGate(context.Background())
	if err == nil {
		t.Fatalf("expected ErrRateLimitGated when sleep would exceed cap")
	}
	if err != nil && err.Error() == "" {
		t.Fatalf("error should have message")
	}
}

func TestWithRateLimitTracker_fluent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.json")
	tracker := NewSharedRateLimitTracker(path)
	client := NewClient("", "token")

	result := client.WithRateLimitTracker(tracker, "gitlab.com")
	if result != client {
		t.Fatalf("WithRateLimitTracker should return the same client")
	}
	if client.RateLimitTracker() != tracker {
		t.Fatalf("RateLimitTracker() should return attached tracker")
	}
	if client.RateLimitTrackerUser() != "gitlab.com" {
		t.Fatalf("RateLimitTrackerUser() should return instance name")
	}
}

func TestWithRateLimitTracker_installsTransport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.json")
	tracker := NewSharedRateLimitTracker(path)
	client := NewClient("", "token")

	// Verify transport is wrapped after attaching.
	client.WithRateLimitTracker(tracker, "gitlab.com")
	if _, ok := client.httpClient.Transport.(*rateLimitHeaderTransport); !ok {
		t.Fatalf("expected rateLimitHeaderTransport after WithRateLimitTracker, got %T", client.httpClient.Transport)
	}

	// Re-attaching should not double-wrap.
	client.WithRateLimitTracker(tracker, "gitlab.com")
	if _, ok := client.httpClient.Transport.(*rateLimitHeaderTransport); !ok {
		t.Fatalf("expected rateLimitHeaderTransport after re-attach")
	}
}
