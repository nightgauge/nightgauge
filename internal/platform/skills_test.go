package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSkillService_Resolve_CacheHit(t *testing.T) {
	cfg := Config{BaseURL: "http://unreachable:9999"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}

	svc := NewSkillService(c)
	// Pre-populate cache with a fresh entry
	svc.cache["feature-dev:latest"] = &CachedSkill{
		Stage:    "feature-dev",
		Content:  "cached skill content",
		Version:  "1.0.0",
		Variant:  "default",
		CachedAt: time.Now(), // Fresh
	}

	result, err := svc.Resolve(context.Background(), "feature-dev", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "cached skill content" {
		t.Errorf("content = %s, want cached skill content", result.Content)
	}
}

func TestSkillService_Resolve_CacheMiss_Online(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/health":
			jsonResponse(w, map[string]interface{}{
				"status": "ok", "version": "1.0.0", "uptime_seconds": 1, "dependencies": map[string]interface{}{},
			})
		case r.URL.Path == "/v1/skills/resolve" && r.Method == "POST":
			jsonResponse(w, map[string]interface{}{
				"skill_content": "resolved skill content from platform",
				"version":       "2.0.0",
				"variant":       "complex",
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewSkillService(c)
	result, err := svc.Resolve(context.Background(), "feature-dev", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "resolved skill content from platform" {
		t.Errorf("content = %s, want resolved skill content from platform", result.Content)
	}
	if result.Version != "2.0.0" {
		t.Errorf("version = %s, want 2.0.0", result.Version)
	}
}

func TestSkillService_Resolve_Offline_CacheFallback(t *testing.T) {
	cfg := Config{BaseURL: "http://unreachable:9999"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Leave mode as offline (default)

	svc := NewSkillService(c)
	// Pre-populate cache with a stale entry (beyond TTL but still usable offline)
	svc.cache["feature-dev:latest"] = &CachedSkill{
		Stage:    "feature-dev",
		Content:  "stale cached content",
		Version:  "1.0.0",
		Variant:  "default",
		CachedAt: time.Now().Add(-2 * time.Hour), // Past 1h TTL
	}

	result, err := svc.Resolve(context.Background(), "feature-dev", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "stale cached content" {
		t.Errorf("content = %s, want stale cached content", result.Content)
	}
}

func TestSkillService_Resolve_Offline_NoCache_Error(t *testing.T) {
	cfg := Config{BaseURL: "http://unreachable:9999"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Leave mode as offline

	svc := NewSkillService(c)
	_, err = svc.Resolve(context.Background(), "feature-dev", nil)
	if err == nil {
		t.Fatal("expected error when offline with no cache")
	}
}

func TestSkillService_ClearCache(t *testing.T) {
	cfg := Config{BaseURL: "http://unreachable:9999"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}

	svc := NewSkillService(c)
	svc.cache["feature-dev:latest"] = &CachedSkill{
		Stage:    "feature-dev",
		Content:  "cached",
		CachedAt: time.Now(),
	}

	svc.ClearCache()

	if len(svc.cache) != 0 {
		t.Errorf("cache length = %d, want 0 after ClearCache", len(svc.cache))
	}
}
