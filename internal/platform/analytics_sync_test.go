package platform

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// newAnalyticsSvcForSyncTest creates an AnalyticsService backed by a test HTTP
// server. The handler receives each POST /v1/telemetry/pipeline-run request.
func newAnalyticsSvcForSyncTest(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *AnalyticsService {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handler != nil {
			handler(w, r)
		} else {
			w.WriteHeader(http.StatusCreated)
		}
	}))
	t.Cleanup(srv.Close)

	cfg := Config{BaseURL: srv.URL}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.setMode(ModeOnline)
	return NewAnalyticsService(c)
}

// makeV2Record creates a minimal real (non-noise) V2RunRecord for platform sync
// tests. It carries a completed stage and non-zero duration so CanonicalizeRuns
// keeps it (zero-everything records are dropped as synthetic noise).
func makeV2Record(issueNumber int) state.V2RunRecord {
	return state.V2RunRecord{
		SchemaVersion: "2",
		RecordType:    "run",
		IssueNumber:   issueNumber,
		StartedAt:     "2026-03-15T10:00:00Z",
		CompletedAt:   "2026-03-15T10:05:00Z",
		TotalDuration: 300000,
		Outcome:       "complete",
		Stages: map[string]state.V2StageDetail{
			"feature-dev": {Status: "complete"},
		},
	}
}

func TestSyncTelemetry_EmptyRecords(t *testing.T) {
	svc := newAnalyticsSvcForSyncTest(t, nil)
	result := svc.SyncTelemetry(context.Background(), nil, "owner/repo")
	if result.Synced != 0 || result.Failed != 0 {
		t.Errorf("expected {0, 0}, got {%d, %d}", result.Synced, result.Failed)
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got %v", result.Errors)
	}
}

func TestSyncTelemetry_AllSuccess(t *testing.T) {
	var callCount int32
	svc := newAnalyticsSvcForSyncTest(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusCreated)
	})

	records := []state.V2RunRecord{
		makeV2Record(1),
		makeV2Record(2),
		makeV2Record(3),
	}
	result := svc.SyncTelemetry(context.Background(), records, "owner/repo")
	if result.Synced != 3 {
		t.Errorf("expected 3 synced, got %d", result.Synced)
	}
	if result.Failed != 0 {
		t.Errorf("expected 0 failed, got %d (errors: %v)", result.Failed, result.Errors)
	}
	if atomic.LoadInt32(&callCount) != 3 {
		t.Errorf("expected 3 HTTP calls, got %d", callCount)
	}
}

func TestSyncTelemetry_PartialFailure(t *testing.T) {
	var callCount int32
	svc := newAnalyticsSvcForSyncTest(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		// Second call fails.
		if n == 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})

	records := []state.V2RunRecord{
		makeV2Record(10),
		makeV2Record(20),
		makeV2Record(30),
	}
	result := svc.SyncTelemetry(context.Background(), records, "owner/repo")
	if result.Synced != 2 {
		t.Errorf("expected 2 synced, got %d", result.Synced)
	}
	if result.Failed != 1 {
		t.Errorf("expected 1 failed, got %d", result.Failed)
	}
	if len(result.Errors) != 1 {
		t.Errorf("expected 1 error, got %d: %v", len(result.Errors), result.Errors)
	}
}

// TestSyncTelemetry_PostsBareArrayWithPipelineRunID verifies the wire
// contract for POST /v1/telemetry/pipeline-run (#261): the body is a BARE
// top-level JSON array of records — the platform's canonical routes parse
// `record | record[]` and strict-reject anything else, which is how the old
// `{records: [...]}` envelope silently zeroed the entire telemetry surface —
// and each record carries `pipelineRunId` when the local record has a
// well-formed run UUID (so the platform upsert converges with the live
// event-stream row) and omits the key otherwise.
func TestSyncTelemetry_PostsBareArrayWithPipelineRunID(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte

	svc := newAnalyticsSvcForSyncTest(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	})

	withID := makeV2Record(1014)
	withID.RunID = "6f883acb-5490-46d0-a8e8-1c985ba9dbfc"
	withoutID := makeV2Record(1015)
	withoutID.RunID = ""
	result := svc.SyncTelemetry(
		context.Background(), []state.V2RunRecord{withID, withoutID}, "nightgauge/nightgauge")
	if result.Synced != 2 {
		t.Fatalf("expected 2 synced, got %d (errors: %v)", result.Synced, result.Errors)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("expected 2 POSTs, got %d", len(bodies))
	}
	for i, body := range bodies {
		var records []map[string]json.RawMessage
		if err := json.Unmarshal(body, &records); err != nil {
			t.Fatalf("POST %d body is not a bare top-level array (#261): %v", i, err)
		}
		if len(records) != 1 {
			t.Fatalf("POST %d: expected 1 record in bare array, got %d", i, len(records))
		}
		if _, hasLegacy := records[0]["runId"]; hasLegacy {
			t.Errorf(`POST %d record carries a legacy "runId" key — the telemetry schema has none`, i)
		}
		if string(records[0]["repo"]) != `"nightgauge/nightgauge"` {
			t.Errorf(`POST %d repo = %s, want "nightgauge/nightgauge"`, i, records[0]["repo"])
		}
	}
	byIssue := map[string]map[string]json.RawMessage{}
	for _, body := range bodies {
		var records []map[string]json.RawMessage
		_ = json.Unmarshal(body, &records)
		byIssue[string(records[0]["issueNumber"])] = records[0]
	}
	if got := string(byIssue["1014"]["pipelineRunId"]); got != `"6f883acb-5490-46d0-a8e8-1c985ba9dbfc"` {
		t.Errorf("pipelineRunId = %s, want the record's run UUID", got)
	}
	if _, has := byIssue["1015"]["pipelineRunId"]; has {
		t.Error("record without a run UUID must omit pipelineRunId (schema is .uuid().optional())")
	}
}

// TestSyncTelemetry_DedupsDuplicateRecords verifies that the duplicate records
// the local history writes per logical run fold to a single POST.
func TestSyncTelemetry_DedupsDuplicateRecords(t *testing.T) {
	var callCount int32
	svc := newAnalyticsSvcForSyncTest(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusCreated)
	})

	// Three records for the same logical run (same issue + same instant).
	dup := func(mode, outcome string, cost float64) state.V2RunRecord {
		return state.V2RunRecord{
			SchemaVersion: "2",
			RecordType:    "run",
			IssueNumber:   1014,
			StartedAt:     "2026-03-15T10:00:00Z",
			CompletedAt:   "2026-03-15T10:05:00Z",
			TotalDuration: 300000,
			ExecutionMode: mode,
			Outcome:       outcome,
			Stages:        map[string]state.V2StageDetail{"feature-dev": {Status: "complete"}},
			Tokens:        state.V2Tokens{EstimatedCostUSD: cost},
			RecordedAt:    "2026-03-15T10:0" + map[bool]string{true: "5", false: "6"}[outcome == "cancelled"] + ":00Z",
		}
	}
	records := []state.V2RunRecord{
		dup("headless", "cancelled", 1.0),
		dup("automatic", "complete", 1.5),
	}

	result := svc.SyncTelemetry(context.Background(), records, "nightgauge/nightgauge")
	if result.Synced != 1 {
		t.Errorf("expected 1 synced (deduped), got %d", result.Synced)
	}
	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("expected 1 HTTP POST (deduped), got %d", got)
	}
}

func TestSyncTelemetry_MalformedStartedAt(t *testing.T) {
	svc := newAnalyticsSvcForSyncTest(t, nil)

	records := []state.V2RunRecord{
		{
			SchemaVersion: "2",
			IssueNumber:   99,
			StartedAt:     "not-a-timestamp",
			Stages:        map[string]state.V2StageDetail{},
		},
	}
	// CanonicalizeRuns drops records whose StartedAt does not parse (counted as
	// ParseSkipped) before the sync loop, so they never reach a push — neither
	// synced nor failed.
	result := svc.SyncTelemetry(context.Background(), records, "owner/repo")
	if result.Synced != 0 {
		t.Errorf("expected 0 synced for bad timestamp, got %d", result.Synced)
	}
	if result.Failed != 0 {
		t.Errorf("expected 0 failed for bad timestamp (dropped during canonicalization), got %d", result.Failed)
	}
}
