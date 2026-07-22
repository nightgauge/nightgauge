package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAnalyticsService_Ingest_Online(t *testing.T) {
	var received bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/analytics/events" {
			received = true
			jsonResponse(w, map[string]interface{}{
				"accepted":     true,
				"events_count": 2,
			})
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewAnalyticsService(c)
	events := []AnalyticsEvent{
		{Type: "stage_start", Timestamp: time.Now()},
		{Type: "stage_complete", Timestamp: time.Now()},
	}

	svc.Ingest(context.Background(), "run-1", 42, events)

	if !received {
		t.Error("analytics not sent to server")
	}
	if svc.BufferedCount() != 0 {
		t.Errorf("buffer = %d, want 0", svc.BufferedCount())
	}
}

func TestAnalyticsService_Ingest_Offline_Buffers(t *testing.T) {
	cfg := Config{BaseURL: "http://unreachable:9999"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}

	svc := NewAnalyticsService(c)
	events := []AnalyticsEvent{
		{Type: "stage_start", Timestamp: time.Now()},
	}

	svc.Ingest(context.Background(), "run-1", 42, events)

	if svc.BufferedCount() != 1 {
		t.Errorf("buffer = %d, want 1", svc.BufferedCount())
	}
}

func TestAnalyticsService_GetUsageSummary_Offline(t *testing.T) {
	cfg := Config{BaseURL: "http://unreachable:9999"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}

	svc := NewAnalyticsService(c)
	summary, err := svc.GetUsageSummary(context.Background())
	if err != nil {
		t.Fatalf("GetUsageSummary offline: unexpected error: %v", err)
	}
	if summary.TotalRuns != 0 {
		t.Errorf("TotalRuns = %d, want 0", summary.TotalRuns)
	}
	if summary.TotalCostUsd != 0 {
		t.Errorf("TotalCostUsd = %f, want 0", summary.TotalCostUsd)
	}
}

func TestAnalyticsService_GetUsageSummary_Online(t *testing.T) {
	var gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/analytics/dashboard" {
			gotRawQuery = r.URL.RawQuery
			// Real platform DashboardSummary shape (period/quota/usage/team/
			// recentRuns) — NOT the prior fictional summary/by_model/by_stage.
			jsonResponse(w, map[string]interface{}{
				"period": map[string]interface{}{
					"start": "2026-06-01T00:00:00Z",
					"end":   "2026-06-28T00:00:00Z",
					"type":  "month",
				},
				"quota": map[string]interface{}{
					"runsUsedToday": 3,
					"runsLimit":     nil,
					"quotaPercent":  nil,
					"nextReset":     "2026-06-29T00:00:00Z",
				},
				"usage": map[string]interface{}{
					"tokenUsageThisPeriod":   150000,
					"pipelineRunsThisPeriod": 25,
				},
				"team":       map[string]interface{}{"activeMemberCount": 1},
				"recentRuns": []interface{}{},
			})
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewAnalyticsService(c)
	summary, err := svc.GetUsageSummary(context.Background())
	if err != nil {
		t.Fatalf("GetUsageSummary online: %v", err)
	}
	// Must use the platform's `range` query param (not the old, ignored `period`).
	if !strings.Contains(gotRawQuery, "range=7d") {
		t.Errorf("query = %q, want it to contain range=7d", gotRawQuery)
	}
	// Real data now flows from `usage` — the prior bug read a non-existent
	// `summary` object and always returned zeros.
	if summary.TotalRuns != 25 {
		t.Errorf("TotalRuns = %d, want 25 (usage.pipelineRunsThisPeriod)", summary.TotalRuns)
	}
	if summary.TotalTokens != 150000 {
		t.Errorf("TotalTokens = %d, want 150000 (usage.tokenUsageThisPeriod)", summary.TotalTokens)
	}
	if summary.Period != "month" {
		t.Errorf("Period = %q, want month (period.type)", summary.Period)
	}
	// Not provided by this license-key endpoint — must be a clean zero, not a
	// misread of some other field.
	if summary.SuccessRatePct != 0 || summary.TotalCostUsd != 0 {
		t.Errorf("SuccessRatePct/TotalCostUsd = %f/%f, want 0/0 (not on /dashboard)",
			summary.SuccessRatePct, summary.TotalCostUsd)
	}
}

func TestAnalyticsService_FlushBuffered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, map[string]interface{}{
			"accepted":     true,
			"events_count": 1,
		})
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}

	svc := NewAnalyticsService(c)

	// Buffer some events while offline
	c.setMode(ModeOffline)
	svc.Ingest(context.Background(), "run-1", 42, []AnalyticsEvent{{Type: "test", Timestamp: time.Now()}})
	svc.Ingest(context.Background(), "run-2", 43, []AnalyticsEvent{{Type: "test", Timestamp: time.Now()}})

	if svc.BufferedCount() != 2 {
		t.Fatalf("buffer = %d, want 2", svc.BufferedCount())
	}

	// Come online and flush
	c.setMode(ModeOnline)
	flushed := svc.FlushBuffered(context.Background())

	if flushed != 2 {
		t.Errorf("flushed = %d, want 2", flushed)
	}
	if svc.BufferedCount() != 0 {
		t.Errorf("buffer after flush = %d, want 0", svc.BufferedCount())
	}
}

func TestAnalyticsService_PushPipelineRun_Online(t *testing.T) {
	// Channel capture (not shared `received`/`requestBody` vars) — the
	// httptest handler runs on its own goroutine, and reading plain variables
	// after a fixed time.Sleep with no synchronization is a data race under
	// go test -race (Issue #354). A buffered channel gives the read a real
	// happens-before edge on the write.
	bodyCh := make(chan []byte, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/telemetry/pipeline-run" && r.Method == http.MethodPost {
			b := make([]byte, r.ContentLength)
			r.Body.Read(b)
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

	svc := NewAnalyticsService(c)

	// Create a test record using the V4 telemetry wire shape.
	completedAt := "2026-01-15T10:05:00Z"
	durationMs := int64(5000)
	run := ExecutionHistoryRunRecord{
		SchemaVersion: 4,
		IssueNumber:   42,
		Repo:          "nightgauge/nightgauge",
		StartedAt:     "2026-01-15T10:00:00Z",
		CompletedAt:   &completedAt,
		Outcome:       "complete",
		DurationMs:    &durationMs,
		Stages:        []ExecutionHistoryStageMetric{},
		Agents:        []any{},
		RoutingPath:   []string{"issue-pickup", "feature-planning", "feature-dev"},
	}

	svc.PushPipelineRun(context.Background(), run)

	var requestBody []byte
	select {
	case requestBody = <-bodyCh:
	case <-time.After(2 * time.Second):
		t.Fatal("PushPipelineRun: server did not receive request")
	}

	var decoded []ExecutionHistoryRunRecord
	if err := json.Unmarshal(requestBody, &decoded); err != nil {
		t.Fatalf("PushPipelineRun: could not decode request body as bare array (#261): %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("expected 1 record in bare array, got %d", len(decoded))
	}
	if decoded[0].IssueNumber != 42 {
		t.Errorf("IssueNumber = %d, want 42", decoded[0].IssueNumber)
	}
}

func TestAnalyticsService_PushPipelineRun_Online_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/telemetry/pipeline-run" {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewAnalyticsService(c)
	run := ExecutionHistoryRunRecord{
		IssueNumber: 43,
		Repo:        "nightgauge/nightgauge",
		StartedAt:   "2026-01-15T10:00:00Z",
	}

	// Call synchronous helper directly to verify error return
	if err := svc.pushPipelineRunSync(context.Background(), run); err == nil {
		t.Error("pushPipelineRunSync: expected error on HTTP 500, got nil")
	}
}

func TestAnalyticsService_PushPipelineRun_Offline_Buffers(t *testing.T) {
	var requestCount int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Client starts offline by default

	svc := NewAnalyticsService(c)
	run := ExecutionHistoryRunRecord{
		IssueNumber: 44,
		Repo:        "nightgauge/nightgauge",
		StartedAt:   "2026-01-15T10:00:00Z",
	}

	svc.PushPipelineRun(context.Background(), run)

	// Give the goroutine time to complete
	time.Sleep(50 * time.Millisecond)

	if requestCount != 0 {
		t.Errorf("PushPipelineRun offline: expected 0 HTTP requests, got %d", requestCount)
	}
	if svc.RunQueueCount() != 1 {
		t.Errorf("RunQueueCount = %d, want 1", svc.RunQueueCount())
	}
}

func TestAnalyticsService_EmitPipelineEvent_Online(t *testing.T) {
	// Channel capture instead of shared `received`/`requestBody` vars — see
	// TestAnalyticsService_PushPipelineRun_Online above (Issue #354).
	bodyCh := make(chan []byte, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/pipelines/events" && r.Method == http.MethodPost {
			b := make([]byte, r.ContentLength)
			r.Body.Read(b)
			bodyCh <- b
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewAnalyticsService(c)
	event := PipelineEvent{
		IssueNumber: 42,
		EventType:   "stage_started",
		Stage:       "feature-dev",
		Timestamp:   time.Now(),
	}

	svc.EmitPipelineEvent(context.Background(), event)

	var requestBody []byte
	select {
	case requestBody = <-bodyCh:
	case <-time.After(2 * time.Second):
		t.Fatal("EmitPipelineEvent: server did not receive request")
	}

	// The wire contract is the platform's camelCase shape, not the Go struct.
	var decoded map[string]interface{}
	if err := json.Unmarshal(requestBody, &decoded); err != nil {
		t.Fatalf("EmitPipelineEvent: could not decode request body: %v", err)
	}
	if decoded["type"] != "stage_started" {
		t.Errorf("type = %v, want stage_started", decoded["type"])
	}
	if decoded["issueNumber"] != float64(42) {
		t.Errorf("issueNumber = %v, want 42", decoded["issueNumber"])
	}
	if svc.EventQueueCount() != 0 {
		t.Errorf("EventQueueCount = %d, want 0", svc.EventQueueCount())
	}
}

func TestAnalyticsService_EmitPipelineEvent_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/pipelines/events" {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewAnalyticsService(c)
	event := PipelineEvent{
		IssueNumber: 43,
		EventType:   "stage_started",
		Stage:       "feature-dev",
		Timestamp:   time.Now(),
	}

	if err := svc.emitPipelineEventSync(context.Background(), event); err == nil {
		t.Error("emitPipelineEventSync: expected error on HTTP 500, got nil")
	}
}

func TestAnalyticsService_EmitPipelineEvent_Offline_Buffers(t *testing.T) {
	cfg := Config{BaseURL: "http://unreachable:9999"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Client starts offline by default

	svc := NewAnalyticsService(c)
	event := PipelineEvent{
		IssueNumber: 44,
		EventType:   "stage_started",
		Stage:       "feature-dev",
		Timestamp:   time.Now(),
	}

	svc.EmitPipelineEvent(context.Background(), event)

	// Give the goroutine time to complete
	time.Sleep(50 * time.Millisecond)

	if svc.EventQueueCount() != 1 {
		t.Errorf("EventQueueCount = %d, want 1", svc.EventQueueCount())
	}
}

func TestBuildEventWire_StageStarted_CarriesRunContext(t *testing.T) {
	repo := "nightgauge/acmeapp"
	w := buildEventWire(PipelineEvent{
		EventType:   "stage_started",
		RunID:       "run-1",
		Stage:       "feature-planning",
		IssueNumber: 42,
		Repo:        repo,
		Origin:      "local_cli",
		Branch:      "feat/42-add-thing",
		Mode:        "maximum",
		Timestamp:   time.Now(),
	})
	if w["type"] != "stage_started" || w["runId"] != "run-1" {
		t.Fatalf("type/runId = %v/%v", w["type"], w["runId"])
	}
	if w["issueNumber"] != 42 || w["repo"] != repo || w["origin"] != "local_cli" {
		t.Errorf("run context = %v/%v/%v", w["issueNumber"], w["repo"], w["origin"])
	}
	if w["stage"] != "feature-planning" {
		t.Errorf("stage = %v", w["stage"])
	}
	// Branch + perf mode enrich the live 'running' row.
	if w["branch"] != "feat/42-add-thing" {
		t.Errorf("branch = %v, want feat/42-add-thing", w["branch"])
	}
	if w["mode"] != "maximum" {
		t.Errorf("mode = %v, want maximum", w["mode"])
	}
}

// TestBuildEventWire_StageStarted_OmitsEmptyBranchAndMode guards the first
// stage_started (issue-pickup), where the branch isn't resolved yet, and the
// case where the perf mode is unresolvable ('frontier' maps to ""). Empty
// values must be ABSENT from the wire (not sent as ""), so the platform's
// COALESCE enrichment doesn't clobber an already-known value and the dashboard
// never renders a bad mode badge.
func TestBuildEventWire_StageStarted_OmitsEmptyBranchAndMode(t *testing.T) {
	w := buildEventWire(PipelineEvent{
		EventType:   "stage_started",
		RunID:       "run-1",
		Stage:       "issue-pickup",
		IssueNumber: 42,
		Repo:        "nightgauge/acmeapp",
		Origin:      "local_cli",
		// Branch + Mode deliberately empty.
		Timestamp: time.Now(),
	})
	if _, ok := w["branch"]; ok {
		t.Errorf("branch key should be absent when empty, got %v", w["branch"])
	}
	if _, ok := w["mode"]; ok {
		t.Errorf("mode key should be absent when empty, got %v", w["mode"])
	}
}

// TestBuildEventWire_StageProgress_CarriesTokensCost guards the #233 live
// in-stage estimate: the stage_progress wire must carry the running token/cost
// snapshot so the run-detail view can show tokens/cost accruing mid-stage.
func TestBuildEventWire_StageProgress_CarriesTokensCost(t *testing.T) {
	w := buildEventWire(PipelineEvent{
		EventType:       "stage_progress",
		RunID:           "run-1",
		Stage:           "feature-dev",
		IssueNumber:     42,
		InputTokens:     1500,
		OutputTokens:    800,
		CacheReadTokens: 200,
		CostUsd:         0.42,
		Timestamp:       time.Now(),
	})
	if w == nil {
		t.Fatal("stage_progress is a platform event type — wire must not be nil")
	}
	if w["type"] != "stage_progress" || w["stage"] != "feature-dev" {
		t.Fatalf("type/stage = %v/%v", w["type"], w["stage"])
	}
	if w["inputTokens"] != 1500 || w["outputTokens"] != 800 || w["cacheReadTokens"] != 200 {
		t.Errorf("tokens = %v/%v/%v, want 1500/800/200", w["inputTokens"], w["outputTokens"], w["cacheReadTokens"])
	}
	if w["costUsd"] != 0.42 {
		t.Errorf("costUsd = %v, want 0.42", w["costUsd"])
	}
}

// TestBuildEventWire_StageCompleted_CarriesTokensCost guards the #233 enrichment
// of stage_completed: alongside durationMs, the authoritative final token/cost
// totals ride the wire so the platform reconciles the live estimate on
// completion.
func TestBuildEventWire_StageCompleted_CarriesTokensCost(t *testing.T) {
	w := buildEventWire(PipelineEvent{
		EventType:       "stage_completed",
		RunID:           "run-1",
		Stage:           "feature-dev",
		DurationMs:      4200,
		InputTokens:     12000,
		OutputTokens:    3400,
		CacheReadTokens: 900,
		CostUsd:         1.23,
		Timestamp:       time.Now(),
	})
	if w == nil {
		t.Fatal("stage_completed wire must not be nil")
	}
	if w["durationMs"] != 4200 {
		t.Errorf("durationMs = %v, want 4200", w["durationMs"])
	}
	if w["inputTokens"] != 12000 || w["outputTokens"] != 3400 || w["cacheReadTokens"] != 900 {
		t.Errorf("tokens = %v/%v/%v, want 12000/3400/900", w["inputTokens"], w["outputTokens"], w["cacheReadTokens"])
	}
	if w["costUsd"] != 1.23 {
		t.Errorf("costUsd = %v, want 1.23", w["costUsd"])
	}
}

// TestBuildEventWire_TimestampIsUTCZulu guards the live-verified regression
// where time.Time's default marshaling emitted a numeric timezone offset
// ("...-06:00"), which the platform's Zod z.string().datetime() rejects with a
// 400 ("Invalid ISO datetime"). Every event timestamp must be UTC with a
// trailing 'Z'. Uses a deliberately non-UTC input zone to prove normalisation.
func TestBuildEventWire_TimestampIsUTCZulu(t *testing.T) {
	mountain := time.FixedZone("MDT", -6*60*60)
	local := time.Date(2026, 6, 9, 20, 24, 28, 646000000, mountain)

	for _, et := range []string{"stage_started", "stage_completed", "stage_error", "pipeline_done"} {
		w := buildEventWire(PipelineEvent{
			EventType: et,
			RunID:     "run-1",
			Stage:     "issue-pickup",
			Timestamp: local,
		})
		ts, ok := w["timestamp"].(string)
		if !ok {
			t.Fatalf("%s: timestamp not a string: %T", et, w["timestamp"])
		}
		if !strings.HasSuffix(ts, "Z") {
			t.Errorf("%s: timestamp %q must end in Z (UTC), not a numeric offset", et, ts)
		}
		if strings.Contains(ts, "+") {
			t.Errorf("%s: timestamp %q must not carry a timezone offset", et, ts)
		}
		// Must parse as RFC3339 and represent the same instant in UTC.
		parsed, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			t.Errorf("%s: timestamp %q not RFC3339-parseable: %v", et, ts, err)
		}
		if !parsed.Equal(local) {
			t.Errorf("%s: timestamp %q != original instant %v", et, ts, local)
		}
	}
}

func TestBuildEventWire_PipelineDone_CarriesOutcome(t *testing.T) {
	ok := true
	w := buildEventWire(PipelineEvent{
		EventType:       "pipeline_done",
		RunID:           "run-1",
		TotalDurationMs: 1234,
		Success:         &ok,
		Timestamp:       time.Now(),
	})
	if w["success"] != true || w["totalDurationMs"] != 1234 {
		t.Errorf("success/totalDurationMs = %v/%v", w["success"], w["totalDurationMs"])
	}
	// stagesRun is required by the contract — always present (empty when unknown).
	if _, ok := w["stagesRun"].([]string); !ok {
		t.Errorf("stagesRun missing/non-array: %v", w["stagesRun"])
	}
}

func TestBuildEventWire_StageError_FillsRequiredFields(t *testing.T) {
	w := buildEventWire(PipelineEvent{
		EventType: "stage_error",
		RunID:     "run-1",
		Stage:     "",
		Metadata:  map[string]interface{}{"error": "boom"},
		Timestamp: time.Now(),
	})
	if w["stage"] != nil {
		t.Errorf("stage should be null when unknown, got %v", w["stage"])
	}
	if w["errorCode"] == "" || w["message"] != "boom" || w["retryable"] != false {
		t.Errorf("error fields = %v/%v/%v", w["errorCode"], w["message"], w["retryable"])
	}
}

func TestBuildEventWire_LocalOnlyType_Skipped(t *testing.T) {
	if w := buildEventWire(PipelineEvent{EventType: "pipeline.anomaly", RunID: "r"}); w != nil {
		t.Errorf("local-only event type should be skipped (nil), got %v", w)
	}
}

func TestAnalyticsService_PushPipelineRun_ServerError_Buffers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/telemetry/pipeline-run" {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewAnalyticsService(c)
	run := ExecutionHistoryRunRecord{
		IssueNumber: 50,
	}

	svc.PushPipelineRun(context.Background(), run)
	time.Sleep(100 * time.Millisecond)

	if svc.RunQueueCount() != 1 {
		t.Errorf("RunQueueCount = %d, want 1", svc.RunQueueCount())
	}
}

func TestAnalyticsService_EmitPipelineEvent_ServerError_Buffers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/pipelines/events" {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)

	svc := NewAnalyticsService(c)
	event := PipelineEvent{
		IssueNumber:   50,
		EventType:     "stage_started",
		Stage:         "feature-dev",
		Timestamp:     time.Now(),
		SchemaVersion: "1",
	}

	svc.EmitPipelineEvent(context.Background(), event)
	time.Sleep(100 * time.Millisecond)

	if svc.EventQueueCount() != 1 {
		t.Errorf("EventQueueCount = %d, want 1", svc.EventQueueCount())
	}
}

func TestAnalyticsService_FlushBuffered_RetriesRunQueue(t *testing.T) {
	var received bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/telemetry/pipeline-run" && r.Method == http.MethodPost {
			received = true
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}

	svc := NewAnalyticsService(c)

	// Buffer a run record while offline
	c.setMode(ModeOffline)
	svc.PushPipelineRun(context.Background(), ExecutionHistoryRunRecord{
		IssueNumber: 60,
	})
	time.Sleep(50 * time.Millisecond)

	if svc.RunQueueCount() != 1 {
		t.Fatalf("RunQueueCount = %d, want 1 before flush", svc.RunQueueCount())
	}

	// Come online and flush
	c.setMode(ModeOnline)
	flushed := svc.FlushBuffered(context.Background())

	if !received {
		t.Error("FlushBuffered: server did not receive run record")
	}
	if flushed < 1 {
		t.Errorf("flushed = %d, want >= 1", flushed)
	}
	if svc.RunQueueCount() != 0 {
		t.Errorf("RunQueueCount after flush = %d, want 0", svc.RunQueueCount())
	}
}

func TestAnalyticsService_FlushBuffered_RetriesEventQueue(t *testing.T) {
	var received bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/pipelines/events" && r.Method == http.MethodPost {
			received = true
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}

	svc := NewAnalyticsService(c)

	// Buffer an event while offline
	c.setMode(ModeOffline)
	svc.EmitPipelineEvent(context.Background(), PipelineEvent{
		IssueNumber:   61,
		EventType:     "stage_started",
		Stage:         "feature-dev",
		Timestamp:     time.Now(),
		SchemaVersion: "1",
	})
	time.Sleep(50 * time.Millisecond)

	if svc.EventQueueCount() != 1 {
		t.Fatalf("EventQueueCount = %d, want 1 before flush", svc.EventQueueCount())
	}

	// Come online and flush
	c.setMode(ModeOnline)
	flushed := svc.FlushBuffered(context.Background())

	if !received {
		t.Error("FlushBuffered: server did not receive event")
	}
	if flushed < 1 {
		t.Errorf("flushed = %d, want >= 1", flushed)
	}
	if svc.EventQueueCount() != 0 {
		t.Errorf("EventQueueCount after flush = %d, want 0", svc.EventQueueCount())
	}
}

func TestExecutionHistoryRunRecord_JSONFieldNames(t *testing.T) {
	completedAt := "2026-01-15T10:05:00Z"
	durationMs := int64(5000)
	costUsd := 0.5
	complexity := 3

	run := ExecutionHistoryRunRecord{
		SchemaVersion:   4,
		IssueNumber:     42,
		Repo:            "owner/repo",
		StartedAt:       "2026-01-15T10:00:00Z",
		CompletedAt:     &completedAt,
		Outcome:         "complete",
		ComplexityScore: &complexity,
		Retries:         2,
		DurationMs:      &durationMs,
		TotalCostUsd:    &costUsd,
		Stages:          []ExecutionHistoryStageMetric{},
		Agents:          []any{},
		RoutingPath:     []string{"issue-pickup", "feature-planning"},
	}

	data, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	// Verify all camelCase keys are present
	camelCaseKeys := []string{
		"schemaVersion", "issueNumber", "repo", "startedAt", "completedAt",
		"outcome", "complexityScore", "retries", "durationMs", "totalCostUsd",
		"stages", "agents", "routingPath",
	}
	for _, key := range camelCaseKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("expected camelCase key %q in JSON output, not found", key)
		}
	}

	// Verify PascalCase variants are NOT present
	pascalCaseKeys := []string{
		"SchemaVersion", "IssueNumber", "Repo", "StartedAt", "CompletedAt",
		"Outcome", "ComplexityScore", "Retries", "DurationMs", "TotalCostUsd",
		"Stages", "Agents", "RoutingPath",
	}
	for _, key := range pascalCaseKeys {
		if _, ok := m[key]; ok {
			t.Errorf("unexpected PascalCase key %q in JSON output — should be camelCase", key)
		}
	}

	if v, ok := m["issueNumber"].(float64); !ok || int(v) != 42 {
		t.Errorf("issueNumber = %v, want 42", m["issueNumber"])
	}

	routingPath, ok := m["routingPath"].([]interface{})
	if !ok {
		t.Fatalf("routingPath is not an array: %T", m["routingPath"])
	}
	if len(routingPath) != 2 {
		t.Errorf("routingPath len = %d, want 2", len(routingPath))
	}
}

func TestPushPipelineRunSync_JSONFieldNames(t *testing.T) {
	var requestBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/telemetry/pipeline-run" && r.Method == http.MethodPost {
			requestBody = make([]byte, r.ContentLength)
			r.Body.Read(requestBody)
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

	svc := NewAnalyticsService(c)

	run := ExecutionHistoryRunRecord{
		SchemaVersion: 4,
		IssueNumber:   42,
		Repo:          "owner/repo",
		StartedAt:     "2026-01-15T10:00:00Z",
		Outcome:       "complete",
		Stages:        []ExecutionHistoryStageMetric{},
		Agents:        []any{},
		RoutingPath:   []string{"issue-pickup", "feature-planning"},
	}

	if err := svc.pushPipelineRunSync(context.Background(), run); err != nil {
		t.Fatalf("pushPipelineRunSync: %v", err)
	}

	var batch []ExecutionHistoryRunRecord
	if err := json.Unmarshal(requestBody, &batch); err != nil {
		t.Fatalf("unmarshal request body as bare array (#261): %v", err)
	}
	if len(batch) != 1 {
		t.Fatalf("expected 1 record in bare array, got %d", len(batch))
	}

	var recordsRaw []interface{}
	if err := json.Unmarshal(requestBody, &recordsRaw); err != nil {
		t.Fatalf("unmarshal request body as bare top-level array (#261): %v", err)
	}
	if len(recordsRaw) != 1 {
		t.Fatalf("expected bare top-level array with 1 entry, got %d", len(recordsRaw))
	}
	rm, ok := recordsRaw[0].(map[string]interface{})
	if !ok {
		t.Fatalf("record entry is not an object: %T", recordsRaw[0])
	}

	if v, ok := rm["issueNumber"].(float64); !ok || int(v) != 42 {
		t.Errorf("issueNumber = %v, want 42", rm["issueNumber"])
	}
	startedAt, ok := rm["startedAt"].(string)
	if !ok {
		t.Fatalf("startedAt is not a string: %T", rm["startedAt"])
	}
	if _, err := time.Parse(time.RFC3339, startedAt); err != nil {
		t.Errorf("startedAt %q is not RFC3339: %v", startedAt, err)
	}
	routingPath, ok := rm["routingPath"].([]interface{})
	if !ok {
		t.Fatalf("routingPath is not an array: %T", rm["routingPath"])
	}
	if len(routingPath) != 2 {
		t.Errorf("routingPath len = %d, want 2", len(routingPath))
	}
}

func TestPushPipelineRunSync_RejectedRecordIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"accepted": 0,
			"rejected": []map[string]interface{}{
				{"index": 0, "reason": "schema validation failed"},
			},
		})
	}))
	defer srv.Close()

	c, err := NewClient(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)
	svc := NewAnalyticsService(c)

	run := ExecutionHistoryRunRecord{IssueNumber: 1, Repo: "owner/repo", StartedAt: "2026-01-15T10:00:00Z"}

	// A platform-rejected record is a permanent validation failure (poison
	// message) — pushPipelineRunSync must NOT return an error for it (that
	// would cause PushPipelineRun to buffer it for a retry that can never
	// succeed).
	if err := svc.pushPipelineRunSync(context.Background(), run); err != nil {
		t.Errorf("pushPipelineRunSync should not error on a rejected record, got: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("expected exactly 1 POST attempt (no retry on rejection), got %d", got)
	}
}

func TestAnalyticsService_RunQueue_DropOldestAtCapacity(t *testing.T) {
	cfg := Config{BaseURL: "http://unreachable:9999"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}

	svc := NewAnalyticsService(c)

	// Enqueue maxBufferSize + 1 records directly
	for i := 0; i <= maxBufferSize; i++ {
		svc.enqueueRun(ExecutionHistoryRunRecord{
			IssueNumber: i,
		})
	}

	if svc.RunQueueCount() != maxBufferSize {
		t.Errorf("RunQueueCount = %d, want %d (oldest should be dropped)", svc.RunQueueCount(), maxBufferSize)
	}
}

func TestPipelineEvent_RunID_Serialization(t *testing.T) {
	// RunID is serialized as run_id when set, omitted when empty.
	event := PipelineEvent{
		RunID:         "01966b4c-1234-7000-a000-000000000001",
		IssueNumber:   42,
		EventType:     "stage_started",
		Stage:         "feature-dev",
		Timestamp:     time.Now(),
		SchemaVersion: "1",
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if got, ok := m["run_id"]; !ok || got != "01966b4c-1234-7000-a000-000000000001" {
		t.Errorf("run_id = %v, want 01966b4c-1234-7000-a000-000000000001", got)
	}

	// Without RunID, run_id must be omitted (omitempty).
	eventNoRun := PipelineEvent{
		IssueNumber:   42,
		EventType:     "stage_started",
		Stage:         "feature-dev",
		Timestamp:     time.Now(),
		SchemaVersion: "1",
	}
	data2, _ := json.Marshal(eventNoRun)
	var m2 map[string]interface{}
	_ = json.Unmarshal(data2, &m2)
	if _, ok := m2["run_id"]; ok {
		t.Error("run_id should be omitted when empty")
	}
}

func TestRetryBackoff(t *testing.T) {
	// Retry-After (integer seconds) is honoured when present and positive.
	if got := retryBackoff(0, "2"); got != 2*time.Second {
		t.Errorf("retryBackoff(_, \"2\") = %v, want 2s", got)
	}
	// Retry-After is capped at 30s.
	if got := retryBackoff(0, "120"); got != 30*time.Second {
		t.Errorf("retryBackoff(_, \"120\") = %v, want 30s (capped)", got)
	}
	// No / invalid / non-positive header → exponential backoff on the attempt.
	for _, ra := range []string{"", "abc", "0", "-5"} {
		if got := retryBackoff(0, ra); got != 1*time.Second {
			t.Errorf("retryBackoff(0, %q) = %v, want 1s (exponential)", ra, got)
		}
		if got := retryBackoff(2, ra); got != 4*time.Second {
			t.Errorf("retryBackoff(2, %q) = %v, want 4s (exponential)", ra, got)
		}
	}
	// Exponential backoff is also capped at 30s.
	if got := retryBackoff(20, ""); got != 30*time.Second {
		t.Errorf("retryBackoff(20, \"\") = %v, want 30s (capped)", got)
	}
}

func TestPushPipelineRunSync_RetriesOn429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/telemetry/pipeline-run" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Rate-limit the first attempt, accept the retry. Retry-After: 0 forces the
		// fast exponential path (1s) so the test stays quick and deterministic.
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c, err := NewClient(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)
	svc := NewAnalyticsService(c)

	run := ExecutionHistoryRunRecord{IssueNumber: 1, Repo: "nightgauge/nightgauge", StartedAt: "2026-01-15T10:00:00Z"}

	if err := svc.pushPipelineRunSync(context.Background(), run); err != nil {
		t.Fatalf("pushPipelineRunSync should succeed after a 429 retry, got: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("expected 2 POST attempts (429 then 201), got %d", got)
	}
}
