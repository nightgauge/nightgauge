package ipc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/state"
)

// newSyncTelemetryServer creates an IPC Server with an AnalyticsService wired
// to the given mock HTTP server URL. workspaceRoot is set on the server.
func newSyncTelemetryServer(t *testing.T, mockURL string, workspaceRoot string) *Server {
	t.Helper()

	pc, err := platform.NewClient(platform.Config{BaseURL: mockURL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	s := NewServer(nil, WithPlatformClient(pc))
	s.writer = &bytes.Buffer{}
	if workspaceRoot != "" {
		s.workspaceRoot = workspaceRoot
	}
	return s
}

// writeV2TestRecord writes a V2RunRecord to a daily JSONL file under the history dir.
func writeV2TestRecord(t *testing.T, historyDir string, date string, records []state.V2RunRecord) {
	t.Helper()
	if err := os.MkdirAll(historyDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(filepath.Join(historyDir, date+".jsonl"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	for _, r := range records {
		data, _ := json.Marshal(r)
		f.Write(append(data, '\n'))
	}
}

func TestPlatformSyncTelemetry_NoAnalyticsSvc(t *testing.T) {
	// Server with no platform client → analyticsSvc is nil.
	s := NewServer(nil)
	s.writer = &bytes.Buffer{}

	_, err := callHandler(t, s, "platform.syncTelemetry", PlatformSyncTelemetryParams{})
	if err == nil {
		t.Fatal("expected error when analyticsSvc is nil")
	}
	if err.Error() != "platform client not configured" {
		t.Errorf("expected 'platform client not configured', got %q", err.Error())
	}
}

func TestPlatformSyncTelemetry_NoWorkspaceRoot(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer mock.Close()

	// workspaceRoot left empty.
	s := newSyncTelemetryServer(t, mock.URL, "")

	_, err := callHandler(t, s, "platform.syncTelemetry", PlatformSyncTelemetryParams{})
	if err == nil {
		t.Fatal("expected error when workspaceRoot is empty")
	}
	if err.Error() != "workspace root not set" {
		t.Errorf("expected 'workspace root not set', got %q", err.Error())
	}
}

func TestPlatformSyncTelemetry_Success(t *testing.T) {
	var callCount int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/telemetry/pipeline-run" {
			atomic.AddInt32(&callCount, 1)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer mock.Close()

	root := t.TempDir()
	historyDir := filepath.Join(root, ".nightgauge", "pipeline", "history")
	// Records carry a real stage + duration so CanonicalizeRuns keeps them
	// (zero-stage/zero-cost/zero-duration records are dropped as synthetic noise).
	writeV2TestRecord(t, historyDir, "2026-03-15", []state.V2RunRecord{
		{
			SchemaVersion: "2",
			IssueNumber:   1,
			StartedAt:     "2026-03-15T10:00:00Z",
			CompletedAt:   "2026-03-15T10:05:00Z",
			TotalDuration: 300000,
			Outcome:       "complete",
			Stages:        map[string]state.V2StageDetail{"feature-dev": {Status: "complete"}},
		},
		{
			SchemaVersion: "2",
			IssueNumber:   2,
			StartedAt:     "2026-03-15T11:00:00Z",
			CompletedAt:   "2026-03-15T11:05:00Z",
			TotalDuration: 300000,
			Outcome:       "complete",
			Stages:        map[string]state.V2StageDetail{"feature-dev": {Status: "complete"}},
		},
	})

	s := newSyncTelemetryServer(t, mock.URL, root)

	result, err := callHandler(t, s, "platform.syncTelemetry", PlatformSyncTelemetryParams{
		Limit:    10,
		DaysBack: 7,
		Repo:     "owner/repo",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	res, ok := result.(PlatformSyncTelemetryResult)
	if !ok {
		t.Fatalf("expected PlatformSyncTelemetryResult, got %T", result)
	}
	if res.Synced != 2 {
		t.Errorf("expected 2 synced, got %d", res.Synced)
	}
	if res.Failed != 0 {
		t.Errorf("expected 0 failed, got %d (errors: %v)", res.Failed, res.Errors)
	}
	if atomic.LoadInt32(&callCount) != 2 {
		t.Errorf("expected 2 HTTP push calls, got %d", callCount)
	}
}

func TestPlatformSyncTelemetry_DefaultParams(t *testing.T) {
	// Verify that zero-value params resolve to defaults (limit=50, daysBack=7).
	// The handler receives requests — we just verify no error occurs with empty history.
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer mock.Close()

	root := t.TempDir()
	s := newSyncTelemetryServer(t, mock.URL, root)

	result, err := callHandler(t, s, "platform.syncTelemetry", PlatformSyncTelemetryParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	res, ok := result.(PlatformSyncTelemetryResult)
	if !ok {
		t.Fatalf("expected PlatformSyncTelemetryResult, got %T", result)
	}
	// Empty history dir → 0 synced, 0 failed.
	if res.Synced != 0 || res.Failed != 0 {
		t.Errorf("expected {0,0} with empty history, got {%d,%d}", res.Synced, res.Failed)
	}
}
