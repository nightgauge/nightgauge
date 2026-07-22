package gitlab_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge/webhook"
	"github.com/nightgauge/nightgauge/internal/gitlab"
)

// mockDispatcher is a test double that records dispatched events.
type mockDispatcher struct {
	events []webhook.ForgeWebhookEvent
	err    error
}

func (m *mockDispatcher) Dispatch(_ context.Context, evt webhook.ForgeWebhookEvent) error {
	m.events = append(m.events, evt)
	return m.err
}

func newTestHandler(t *testing.T, token string, opts ...gitlab.HandlerOption) (*gitlab.GitLabHandler, *mockDispatcher) {
	t.Helper()
	dedup, err := gitlab.NewDedupeCache(":memory:", time.Hour)
	if err != nil {
		t.Fatalf("NewDedupeCache: %v", err)
	}
	t.Cleanup(func() { _ = dedup.Close() })

	disp := &mockDispatcher{}
	// Default to disabled replay window so testdata with old timestamps pass.
	// Tests that want stale-check behaviour pass WithReplayWindow explicitly.
	allOpts := append([]gitlab.HandlerOption{gitlab.WithReplayWindow(0)}, opts...)
	h := gitlab.NewGitLabHandler(token, dedup, disp, allOpts...)
	return h, disp
}

func postWebhook(h http.Handler, path, token, eventKind, deliveryID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("X-Gitlab-Token", token)
	req.Header.Set("X-Gitlab-Event", eventKind)
	if deliveryID != "" {
		req.Header.Set("X-Gitlab-Event-UUID", deliveryID)
	}
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestGitLabHandler_ValidToken_Returns200(t *testing.T) {
	h, disp := newTestHandler(t, "my-secret")
	body := string(readFile(t, "testdata/pipeline_event.json"))
	rr := postWebhook(h, "/gitlab", "my-secret", gitlab.EventKindPipeline, "delivery-1", body)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", rr.Code)
	}
	if len(disp.events) != 1 {
		t.Errorf("dispatched events = %d; want 1", len(disp.events))
	}
}

func TestGitLabHandler_WrongToken_Returns401(t *testing.T) {
	h, disp := newTestHandler(t, "my-secret")
	body := string(readFile(t, "testdata/pipeline_event.json"))
	rr := postWebhook(h, "/gitlab", "wrong-token", gitlab.EventKindPipeline, "delivery-1", body)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rr.Code)
	}
	if len(disp.events) != 0 {
		t.Error("expected no dispatch on 401")
	}
}

func TestGitLabHandler_EmptyToken_Returns401(t *testing.T) {
	h, _ := newTestHandler(t, "my-secret")
	body := string(readFile(t, "testdata/pipeline_event.json"))
	rr := postWebhook(h, "/gitlab", "", gitlab.EventKindPipeline, "delivery-1", body)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rr.Code)
	}
}

func TestGitLabHandler_DuplicateDelivery_Returns200NoDispatch(t *testing.T) {
	h, disp := newTestHandler(t, "secret")
	body := string(readFile(t, "testdata/pipeline_event.json"))

	// First delivery
	rr := postWebhook(h, "/gitlab", "secret", gitlab.EventKindPipeline, "delivery-dup", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("first delivery status = %d; want 200", rr.Code)
	}

	// Second delivery — same UUID
	rr = postWebhook(h, "/gitlab", "secret", gitlab.EventKindPipeline, "delivery-dup", body)
	if rr.Code != http.StatusOK {
		t.Errorf("duplicate delivery status = %d; want 200", rr.Code)
	}
	if len(disp.events) != 1 {
		t.Errorf("dispatched events = %d; want 1 (duplicate should not dispatch)", len(disp.events))
	}
}

func TestGitLabHandler_StaleEvent_Returns200NoDispatch(t *testing.T) {
	h, disp := newTestHandler(t, "secret",
		gitlab.WithReplayWindow(1*time.Second), // very short window
	)
	// Craft a payload with a timestamp from 10 seconds ago
	stalePayload := `{
		"object_kind": "pipeline",
		"object_attributes": {
			"id": 1, "iid": 1, "status": "success", "state": "success",
			"created_at": "2020-01-01T00:00:00Z"
		},
		"project": {"id": 99, "web_url": "https://gitlab.example.com/p"}
	}`
	rr := postWebhook(h, "/gitlab", "secret", gitlab.EventKindPipeline, "delivery-stale", stalePayload)
	if rr.Code != http.StatusOK {
		t.Errorf("stale event status = %d; want 200", rr.Code)
	}
	if len(disp.events) != 0 {
		t.Errorf("dispatched events = %d; want 0 (stale should not dispatch)", len(disp.events))
	}
}

func TestGitLabHandler_UnsupportedEventKind_Returns200(t *testing.T) {
	h, _ := newTestHandler(t, "secret")
	rr := postWebhook(h, "/gitlab", "secret", "System Hook", "delivery-x", `{}`)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", rr.Code)
	}
}

func TestGitLabHandler_MethodNotPost_Returns405(t *testing.T) {
	h, _ := newTestHandler(t, "secret")
	req := httptest.NewRequest(http.MethodGet, "/gitlab", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d; want 405", rr.Code)
	}
}

func TestGitLabHandler_HealthEndpoint(t *testing.T) {
	h, _ := newTestHandler(t, "secret", gitlab.WithVersion("1.2.3"))
	mux := http.NewServeMux()
	h.Register(mux, "/gitlab")

	req := httptest.NewRequest(http.MethodGet, "/-/health", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("health status = %d; want 200", rr.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("health body not JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("health status field = %q; want %q", body["status"], "ok")
	}
	if body["version"] != "1.2.3" {
		t.Errorf("health version field = %q; want %q", body["version"], "1.2.3")
	}
}

func TestGitLabHandler_MetricsEndpoint(t *testing.T) {
	h, _ := newTestHandler(t, "secret")
	mux := http.NewServeMux()
	h.Register(mux, "/gitlab")

	req := httptest.NewRequest(http.MethodGet, "/-/metrics", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("metrics status = %d; want 200", rr.Code)
	}
	var body map[string]int64
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("metrics body not JSON: %v", err)
	}
	if _, ok := body["events_received"]; !ok {
		t.Error("metrics missing events_received field")
	}
}

func TestGitLabHandler_MetricsCounters(t *testing.T) {
	h, _ := newTestHandler(t, "secret")
	mux := http.NewServeMux()
	h.Register(mux, "/gitlab")

	body := string(readFile(t, "testdata/pipeline_event.json"))

	// One valid event
	postWebhook(h, "/gitlab", "secret", gitlab.EventKindPipeline, "m-delivery-1", body)
	// One bad token (should increment events_dropped)
	postWebhook(h, "/gitlab", "bad", gitlab.EventKindPipeline, "m-delivery-2", body)

	req := httptest.NewRequest(http.MethodGet, "/-/metrics", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	var metrics map[string]int64
	_ = json.Unmarshal(rr.Body.Bytes(), &metrics)

	if metrics["events_received"] < 2 {
		t.Errorf("events_received = %d; want >= 2", metrics["events_received"])
	}
	if metrics["events_dropped"] < 1 {
		t.Errorf("events_dropped = %d; want >= 1", metrics["events_dropped"])
	}
}

func TestGitLabHandler_AllEventTypes_Dispatch(t *testing.T) {
	cases := []struct {
		kind     string
		file     string
		wantType string
	}{
		{gitlab.EventKindPipeline, "pipeline_event.json", "gitlab.pipeline"},
		{gitlab.EventKindMergeRequest, "mr_event.json", "gitlab.mr"},
		{gitlab.EventKindNote, "note_event.json", "gitlab.note"},
		{gitlab.EventKindPush, "push_event.json", "gitlab.push"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			h, disp := newTestHandler(t, "secret")
			body := string(readFile(t, "testdata/"+tc.file))
			rr := postWebhook(h, "/gitlab", "secret", tc.kind, "d-"+tc.kind, body)
			if rr.Code != http.StatusOK {
				t.Errorf("status = %d; want 200", rr.Code)
			}
			if len(disp.events) != 1 {
				t.Fatalf("dispatched events = %d; want 1", len(disp.events))
			}
			if disp.events[0].EventType != tc.wantType {
				t.Errorf("EventType = %q; want %q", disp.events[0].EventType, tc.wantType)
			}
		})
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readFile %q: %v", path, err)
	}
	return b
}
