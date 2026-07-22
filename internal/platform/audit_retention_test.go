package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestRetentionClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c, err := NewClient(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)
	return c
}

func TestAuditRetentionService_GetRetentionConfig(t *testing.T) {
	t.Run("returns retention config", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/v1/audit/retention" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(RetentionConfig{RetentionDays: 730, UpdatedAt: "2026-01-01T00:00:00Z"})
		}))
		defer srv.Close()

		svc := NewAuditRetentionService(newTestRetentionClient(t, srv))
		result, err := svc.GetRetentionConfig(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RetentionDays != 730 {
			t.Errorf("expected 730, got %d", result.RetentionDays)
		}
	})

	t.Run("returns error when platform offline", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer srv.Close()
		c, err := NewClient(Config{BaseURL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		// Leave mode as offline (default)
		svc := NewAuditRetentionService(c)
		_, err = svc.GetRetentionConfig(context.Background())
		if err == nil {
			t.Fatal("expected error but got nil")
		}
	})

	t.Run("wraps 403 as enterprise-only error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		svc := NewAuditRetentionService(newTestRetentionClient(t, srv))
		_, err := svc.GetRetentionConfig(context.Background())
		if err == nil {
			t.Fatal("expected enterprise-only error")
		}
	})
}

func TestAuditRetentionService_UpdateRetentionConfig(t *testing.T) {
	t.Run("sends PUT and returns updated config", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPut || r.URL.Path != "/v1/audit/retention" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
			var body map[string]int
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode body: %v", err)
			}
			if body["retentionDays"] != 365 {
				t.Errorf("expected 365, got %d", body["retentionDays"])
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(RetentionConfig{RetentionDays: 365, UpdatedAt: "2026-05-01T00:00:00Z"})
		}))
		defer srv.Close()

		svc := NewAuditRetentionService(newTestRetentionClient(t, srv))
		result, err := svc.UpdateRetentionConfig(context.Background(), 365)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.RetentionDays != 365 {
			t.Errorf("expected 365, got %d", result.RetentionDays)
		}
	})

	t.Run("returns error when platform offline", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer srv.Close()
		c, err := NewClient(Config{BaseURL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		svc := NewAuditRetentionService(c)
		_, err = svc.UpdateRetentionConfig(context.Background(), 365)
		if err == nil {
			t.Fatal("expected error but got nil")
		}
	})
}

func TestAuditRetentionService_VerifyIntegrity(t *testing.T) {
	t.Run("sends POST and returns integrity result", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/audit/integrity/verify" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
			var body map[string]int
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode body: %v", err)
			}
			if body["windowDays"] != 30 {
				t.Errorf("expected 30, got %d", body["windowDays"])
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(IntegrityResult{
				Valid: true, CheckedCount: 1000, WindowDays: 30,
				Message: "All entries valid", CheckedAt: "2026-05-01T00:00:00Z",
			})
		}))
		defer srv.Close()

		svc := NewAuditRetentionService(newTestRetentionClient(t, srv))
		result, err := svc.VerifyIntegrity(context.Background(), 30)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Valid {
			t.Error("expected valid result")
		}
		if result.CheckedCount != 1000 {
			t.Errorf("expected 1000, got %d", result.CheckedCount)
		}
	})

	t.Run("wraps 403 as enterprise-only error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		svc := NewAuditRetentionService(newTestRetentionClient(t, srv))
		_, err := svc.VerifyIntegrity(context.Background(), 30)
		if err == nil {
			t.Fatal("expected enterprise-only error")
		}
	})

	t.Run("returns error when platform offline", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer srv.Close()
		c, err := NewClient(Config{BaseURL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		svc := NewAuditRetentionService(c)
		_, err = svc.VerifyIntegrity(context.Background(), 90)
		if err == nil {
			t.Fatal("expected error but got nil")
		}
	})
}
