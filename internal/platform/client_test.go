package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func jsonResponse(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func healthyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, map[string]interface{}{
			"status":         "ok",
			"version":        "1.0.0",
			"uptime_seconds": 100,
			"dependencies":   map[string]interface{}{},
		})
	})
}

func TestNewClient(t *testing.T) {
	cfg := DefaultConfig()
	cfg.APIKey = "test-key"
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.Mode() != ModeOffline {
		t.Errorf("initial mode = %s, want offline", c.Mode())
	}
}

func TestHealthPolling_Online(t *testing.T) {
	srv := httptest.NewServer(healthyHandler())
	defer srv.Close()

	cfg := Config{
		BaseURL:      srv.URL,
		PollInterval: 100 * time.Millisecond,
	}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.StartHealthPolling(ctx)
	time.Sleep(300 * time.Millisecond)

	if c.Mode() != ModeOnline {
		t.Errorf("mode = %s, want online", c.Mode())
	}
	c.StopHealthPolling()
}

func TestHealthPolling_Offline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	cfg := Config{
		BaseURL:      srv.URL,
		PollInterval: 100 * time.Millisecond,
	}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.StartHealthPolling(ctx)
	time.Sleep(300 * time.Millisecond)

	if c.Mode() != ModeOffline {
		t.Errorf("mode = %s, want offline", c.Mode())
	}
	c.StopHealthPolling()
}

func TestModeChangeCallback(t *testing.T) {
	srv := httptest.NewServer(healthyHandler())
	defer srv.Close()

	cfg := Config{
		BaseURL:      srv.URL,
		PollInterval: 100 * time.Millisecond,
	}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	called := make(chan ConnectivityMode, 1)
	c.OnModeChange(func(old, new ConnectivityMode) {
		select {
		case called <- new:
		default:
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.StartHealthPolling(ctx)

	select {
	case mode := <-called:
		if mode != ModeOnline {
			t.Errorf("callback mode = %s, want online", mode)
		}
	case <-time.After(2 * time.Second):
		t.Error("mode change callback not called")
	}
	c.StopHealthPolling()
}
