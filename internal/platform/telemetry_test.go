package platform

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

func makeTestRecord() state.V2RunRecord {
	size := "M"
	issueType := "feature"
	return state.V2RunRecord{
		SchemaVersion: "2",
		RecordType:    "run",
		IssueNumber:   42,
		Title:         "Test issue",
		Repo:          "nightgauge/nightgauge",
		Branch:        "run-42",
		BaseBranch:    "main",
		ExecutionMode: "automatic",
		StartedAt:     time.Now().Format(time.RFC3339),
		CompletedAt:   time.Now().Format(time.RFC3339),
		TotalDuration: 1000,
		Outcome:       "complete",
		Size:          &size,
		Type:          &issueType,
		Stages:        map[string]state.V2StageDetail{},
		Tokens:        state.V2Tokens{},
		Files:         state.V2Files{},
		Routing:       state.V2Routing{ComplexityScore: 3, Path: "standard", SkipStages: []string{}},
		RecordedAt:    time.Now().Format(time.RFC3339),
	}
}

func TestPushPipelineRun_PayloadConstruction(t *testing.T) {
	// Channel capture (not a shared `var body []byte`) — the httptest handler
	// runs on its own goroutine, and the test body previously read the plain
	// variable after a fixed time.Sleep with no synchronization, which
	// go test -race correctly flags as a data race (Issue #354). A buffered
	// channel gives the read a real happens-before edge on the write.
	bodyCh := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Telemetry lands on the single canonical pipeline-run sink
		// The platform ingestion contract writes the full
		// analytics surface (pipeline_runs/usage_events/cost_events/
		// pipeline_outcomes) — not the retired /v1/pipelines/runs sink, and not
		// the JWT-only /v1/analytics/events sink that 401'd for the license-key
		// pipeline.
		if r.URL.Path == "/v1/telemetry/pipeline-run" && r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			bodyCh <- b
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewTelemetryService(c)
	record := makeTestRecord()
	svc.PushPipelineRun(context.Background(), record)

	var body []byte
	select {
	case body = <-bodyCh:
	case <-time.After(2 * time.Second):
		t.Fatal("no request body received on /v1/telemetry/pipeline-run — PushPipelineRun did not send")
	}

	var batch []ExecutionHistoryRunRecord
	if err := json.Unmarshal(body, &batch); err != nil {
		t.Fatalf("unmarshal bare record array (#261): %v", err)
	}
	if len(batch) != 1 {
		t.Fatalf("expected 1 record in bare array, got %d", len(batch))
	}
	payload := batch[0]

	if payload.SchemaVersion != 5 {
		t.Errorf("schemaVersion = %d, want 5", payload.SchemaVersion)
	}
	if payload.IssueNumber != 42 {
		t.Errorf("issueNumber = %d, want 42", payload.IssueNumber)
	}
	if payload.Repo != "nightgauge/nightgauge" {
		t.Errorf("repo = %q, want nightgauge/nightgauge", payload.Repo)
	}
	if payload.Outcome != "complete" {
		t.Errorf("outcome = %q, want complete", payload.Outcome)
	}
}

func TestPushPipelineRun_OfflineBuffers(t *testing.T) {
	// Channel capture instead of a shared `called bool` (Issue #354) — see
	// TestPushPipelineRun_PayloadConstruction above. This test asserts the
	// handler is NEVER invoked, so the read side waits out a bounded window
	// for a signal that should not arrive rather than racing a bare bool.
	calledCh := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calledCh <- struct{}{}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Client starts offline — do not set mode to online

	svc := NewTelemetryService(c)
	svc.PushPipelineRun(context.Background(), makeTestRecord())

	select {
	case <-calledCh:
		t.Error("HTTP call made while offline — should have been skipped")
	case <-time.After(100 * time.Millisecond):
		// Expected: PushPipelineRun buffers on a goroutine and never calls out.
	}
	// The run is buffered in the pipeline-run retry queue (runQueue), not the
	// analytics-events buffer.
	if svc.analytics.RunQueueCount() != 1 {
		t.Errorf("RunQueueCount = %d, want 1 (run should be buffered when offline)", svc.analytics.RunQueueCount())
	}
}

func TestPushPipelineRun_UnparseableStartedAt(t *testing.T) {
	// A record whose StartedAt can't be parsed must be dropped (logged), never
	// sent — the mapper requires a valid RFC3339 timestamp.
	//
	// Channel capture instead of a shared `called bool` (Issue #354) — see
	// TestPushPipelineRun_PayloadConstruction above.
	calledCh := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calledCh <- struct{}{}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewTelemetryService(c)
	rec := makeTestRecord()
	rec.StartedAt = "not-a-timestamp"
	svc.PushPipelineRun(context.Background(), rec)

	select {
	case <-calledCh:
		t.Error("a record with an unparseable started_at must not be sent")
	case <-time.After(100 * time.Millisecond):
		// Expected: the mapper drops the record before any HTTP call.
	}
}
