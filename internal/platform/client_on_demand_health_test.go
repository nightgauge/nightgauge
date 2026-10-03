package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// countingHealthServer answers /v1/health "ok" and counts every request.
func countingHealthServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		jsonResponse(w, map[string]interface{}{
			"status":         "ok",
			"version":        "1.0.0",
			"uptime_seconds": 1,
			"dependencies":   map[string]interface{}{},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestOnDemandHealth_SendsNothingOnItsOwn pins the client a daemon builds
// while the user has not opted in to the cloud: it runs no poller, so a
// signed-in session does not ping the hosted service every minute under the
// user's identity.
func TestOnDemandHealth_SendsNothingOnItsOwn(t *testing.T) {
	srv, hits := countingHealthServer(t)
	c, err := NewClient(Config{BaseURL: srv.URL, PollInterval: 20 * time.Millisecond, OnDemandHealth: true})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.SetSessionToken("user-jwt")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.StartHealthPolling(ctx) // a no-op for an on-demand client
	time.Sleep(120 * time.Millisecond)

	if n := hits.Load(); n != 0 {
		t.Fatalf("an on-demand client sent %d requests on its own, want 0", n)
	}
	if c.Mode() != ModeOffline {
		t.Errorf("Mode() = %s before any request asked, want offline", c.Mode())
	}
}

// TestOnDemandHealth_IsOnlineChecksWhenAsked pins the other half: a request
// the user makes (IsOnline gates every service call) checks the platform then,
// at most once per poll interval, so the user's own requests still work.
func TestOnDemandHealth_IsOnlineChecksWhenAsked(t *testing.T) {
	srv, hits := countingHealthServer(t)
	c, err := NewClient(Config{BaseURL: srv.URL, PollInterval: time.Hour, OnDemandHealth: true})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if !c.IsOnline() {
		t.Fatal("IsOnline() = false against a healthy platform, want true after an on-demand check")
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("first IsOnline made %d health checks, want 1", n)
	}
	for i := 0; i < 5; i++ {
		_ = c.IsOnline()
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("repeated IsOnline within the poll interval made %d health checks, want 1", n)
	}
}

// TestPollingClient_IsOnlineNeverChecks keeps the ordinary client unchanged:
// without OnDemandHealth, IsOnline reads the poller's answer and sends nothing.
func TestPollingClient_IsOnlineNeverChecks(t *testing.T) {
	srv, hits := countingHealthServer(t)
	c, err := NewClient(Config{BaseURL: srv.URL, PollInterval: time.Hour})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.IsOnline() {
		t.Error("IsOnline() = true before any health check")
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("IsOnline on a polling client made %d requests, want 0", n)
	}
}
