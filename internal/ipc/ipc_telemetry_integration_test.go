// Package ipc — Telemetry integration tests for the real IPC server binary.
//
// These tests start a real nightgauge binary with --platform-url pointing
// at an in-process httptest.Server, pre-populate workspace history files, and
// exercise the full end-to-end path:
//
//	binary startup → IPC wire protocol → platform.syncTelemetry handler
//	→ HistoryWriter.ReadRecentV2 → V2RunRecordToExecutionHistoryRunRecord mapper
//	→ pushPipelineRunSync → mock HTTP server receives and validates payload
//
// (POST /v1/telemetry/pipeline-run — the single canonical pipeline-run
// telemetry sink; this replaced
// the retired POST /v1/pipelines/runs sink.)
//
// Tests also cover platform.submitAnalytics via the Ingest → oapi-codegen client path.
//
// @see Issue #2160 — Write integration test for platform telemetry end-to-end
package ipc

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// ─── Telemetry integration test helpers ────────────────────────────────────

// capturedRequest records an HTTP request received by the mock platform server.
type capturedRequest struct {
	method string
	path   string
	body   []byte
}

// setupTelemetryWorkspace creates a temp workspace with .nightgauge/config.yaml
// and optionally writes V2RunRecord history files. Returns the workDir path.
func setupTelemetryWorkspace(t *testing.T, records []state.V2RunRecord) string {
	t.Helper()
	workDir := t.TempDir()
	configDir := filepath.Join(workDir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"),
		[]byte("project:\n  owner: test-org\n  number: 1\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if len(records) > 0 {
		historyDir := filepath.Join(workDir, ".nightgauge", "pipeline", "history")
		writeV2TestRecord(t, historyDir, "2026-03-15", records)
	}
	return workDir
}

// newIpcTelemetryHarness starts the binary with a pre-populated workspace and
// a mock platform server. The caller must create workDir and write history files
// before calling this function — unlike newIpcTestHarnessWithPlatform which
// creates its own workspace internally.
func newIpcTelemetryHarness(t *testing.T, workDir, platformURL, apiKey string) *ipcTestHarness {
	t.Helper()

	args := []string{"serve", "--workspace", workDir}
	if platformURL != "" {
		args = append(args, "--platform-url", platformURL)
	}
	if apiKey != "" {
		args = append(args, "--api-key", apiKey)
	}

	cmd := exec.Command(binaryPath, args...)
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN=fake-token-for-integration-test")

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}

	h := &ipcTestHarness{
		t:      t,
		cmd:    cmd,
		stdin:  stdinPipe,
		lines:  make(chan string, 64),
		nextID: 1,
	}

	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			h.lines <- scanner.Text()
		}
		close(h.lines)
	}()

	t.Cleanup(func() {
		stdinPipe.Close()
		if cmd.Process != nil {
			cmd.Process.Signal(os.Interrupt)
			cmd.Wait()
		}
	})

	return h
}

// telemetryPlatformHandlers returns mock HTTP handlers for platform telemetry
// endpoints. Captured requests are appended to the captured slice (thread-safe).
func telemetryPlatformHandlers(t *testing.T, captured *[]capturedRequest, mu *sync.Mutex) map[string]http.HandlerFunc {
	t.Helper()
	return map[string]http.HandlerFunc{
		"/v1/health": jsonHandler(200, healthOKResponse),
		"/v1/telemetry/pipeline-run": func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			*captured = append(*captured, capturedRequest{r.Method, r.URL.Path, body})
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			// A 202 with a per-record {accepted, rejected} body — the real
			// contract; pushPipelineRunSync parses this to detect rejections.
			json.NewEncoder(w).Encode(map[string]interface{}{"accepted": 1, "rejected": []interface{}{}}) //nolint:errcheck
		},
		"/v1/pipelines/events": func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			*captured = append(*captured, capturedRequest{r.Method, r.URL.Path, body})
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		},
		"/v1/analytics/events": func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			*captured = append(*captured, capturedRequest{r.Method, r.URL.Path, body})
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"accepted": true, "events_count": 1}) //nolint:errcheck
		},
	}
}

// ─── Telemetry IPC Integration Tests ──────────────────────────────────────

// TestIPCTelemetry_SyncTelemetry_PayloadSchema verifies that the full subprocess
// path produces a correctly-structured JSON payload on
// POST /v1/telemetry/pipeline-run: a `{records: [...]}` batch envelope,
// camelCase keys, RFC3339 timestamps, and correct field values.
func TestIPCTelemetry_SyncTelemetry_PayloadSchema(t *testing.T) {
	var (
		mu       sync.Mutex
		captured []capturedRequest
	)

	handlers := telemetryPlatformHandlers(t, &captured, &mu)
	srv := newMockPlatformServer(t, handlers)

	// Real run (has a completed stage + duration) so CanonicalizeRuns keeps it —
	// zero-stage/zero-cost/zero-duration records are dropped as synthetic noise.
	records := []state.V2RunRecord{{
		SchemaVersion: "2",
		IssueNumber:   42,
		StartedAt:     "2026-03-15T10:00:00Z",
		CompletedAt:   "2026-03-15T10:05:00Z",
		TotalDuration: 300000,
		Outcome:       "complete",
		Stages:        map[string]state.V2StageDetail{"feature-dev": {Status: "complete"}},
	}}
	workDir := setupTelemetryWorkspace(t, records)
	h := newIpcTelemetryHarness(t, workDir, srv.URL, "test-api-key")
	h.awaitReady()

	// Give the health poller time to mark the platform as online.
	time.Sleep(500 * time.Millisecond)

	id := h.sendRequest("platform.syncTelemetry", map[string]interface{}{
		"repo": "test-org/test-repo",
	})
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("platform.syncTelemetry returned error: %+v", resp.Error)
	}

	// Verify IPC result reports 1 synced record.
	resultBytes, _ := json.Marshal(resp.Result)
	var result PlatformSyncTelemetryResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.Synced != 1 {
		t.Errorf("expected 1 synced, got %d", result.Synced)
	}

	// Verify HTTP payload structure sent to mock platform.
	mu.Lock()
	defer mu.Unlock()

	var runsRequests []capturedRequest
	for _, c := range captured {
		if c.path == "/v1/telemetry/pipeline-run" {
			runsRequests = append(runsRequests, c)
		}
	}
	if len(runsRequests) != 1 {
		t.Fatalf("expected 1 POST to /v1/telemetry/pipeline-run, got %d", len(runsRequests))
	}

	var batch []map[string]interface{}
	if err := json.Unmarshal(runsRequests[0].body, &batch); err != nil {
		t.Fatalf("unmarshal payload as bare top-level array (#261): %v", err)
	}
	if len(batch) != 1 {
		t.Fatalf("expected 1 record in bare array, got %d", len(batch))
	}
	payload := batch[0]

	// Verify camelCase keys (not snake_case or PascalCase). The V4 telemetry
	// schema carries no client-supplied run id.
	if _, ok := payload["runId"]; ok {
		t.Error(`payload carries a "runId" key — the V4 telemetry schema has none`)
	}
	if _, ok := payload["issueNumber"]; !ok {
		t.Error("payload missing camelCase 'issueNumber' key")
	}
	if _, ok := payload["startedAt"]; !ok {
		t.Error("payload missing camelCase 'startedAt' key")
	}

	// Verify field values.
	if v, ok := payload["issueNumber"].(float64); !ok || int(v) != 42 {
		t.Errorf("payload issueNumber = %v, want 42", payload["issueNumber"])
	}
	if v, ok := payload["repo"].(string); !ok || v != "test-org/test-repo" {
		t.Errorf("payload repo = %v, want %q", payload["repo"], "test-org/test-repo")
	}

	// Verify startedAt is valid RFC3339 (not a unix timestamp).
	startedAt, ok := payload["startedAt"].(string)
	if !ok {
		t.Fatalf("payload startedAt is not a string: %v", payload["startedAt"])
	}
	if _, err := time.Parse(time.RFC3339, startedAt); err != nil {
		t.Errorf("payload startedAt %q is not valid RFC3339: %v", startedAt, err)
	}

	// Verify outcome passes through unmodified: the local V2RunRecord writer
	// already emits the V4 schema's own outcome vocabulary (complete | failed |
	// cancelled) — unlike the retired /v1/pipelines/runs sink, which required
	// translating "complete" -> "success".
	if v, ok := payload["outcome"].(string); !ok || v != "complete" {
		t.Errorf("payload outcome = %v, want %q", payload["outcome"], "complete")
	}
	if v, ok := payload["schemaVersion"].(float64); !ok || int(v) != 5 {
		t.Errorf("payload schemaVersion = %v, want 5", payload["schemaVersion"])
	}
}

// TestIPCTelemetry_SyncTelemetry_RichRecord_IncludesRoutingAndStages verifies
// that a V2RunRecord with a routing path, complexity score, and per-stage
// token/cost data maps to the correct V4 payload fields via the full
// subprocess path. Unlike the retired /v1/pipelines/runs mapper (whose
// routingPath was derived from which stages actually ran, and whose
// routingComplexity was a string label), the V4 schema's routingPath parses
// the free-text V2Routing.Path field verbatim and complexityScore is the raw
// Fibonacci score — see V2RunRecordToExecutionHistoryRunRecord.
func TestIPCTelemetry_SyncTelemetry_RichRecord_IncludesRoutingAndStages(t *testing.T) {
	var (
		mu       sync.Mutex
		captured []capturedRequest
	)

	handlers := telemetryPlatformHandlers(t, &captured, &mu)
	srv := newMockPlatformServer(t, handlers)

	records := []state.V2RunRecord{{
		SchemaVersion: "2",
		IssueNumber:   99,
		StartedAt:     "2026-03-15T10:00:00Z",
		CompletedAt:   "2026-03-15T10:30:00Z",
		Outcome:       "complete",
		Stages: map[string]state.V2StageDetail{
			"issue-pickup":     {Status: "complete", DurationMs: 1000},
			"feature-planning": {Status: "complete", DurationMs: 2000},
			"feature-dev":      {Status: "complete", DurationMs: 3000},
		},
		Tokens: state.V2Tokens{
			PerStage: map[string]state.V2StageTokens{
				"feature-dev": {Input: 500, Output: 100, CostUSD: 0.05, Adapter: "claude"},
			},
		},
		Routing: state.V2Routing{
			ComplexityScore: 3,
			Path:            "issue-pickup,feature-planning,feature-dev",
		},
	}}
	workDir := setupTelemetryWorkspace(t, records)
	h := newIpcTelemetryHarness(t, workDir, srv.URL, "test-api-key")
	h.awaitReady()
	time.Sleep(500 * time.Millisecond)

	id := h.sendRequest("platform.syncTelemetry", map[string]interface{}{
		"repo": "test-org/test-repo",
	})
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("platform.syncTelemetry returned error: %+v", resp.Error)
	}

	mu.Lock()
	defer mu.Unlock()

	var runsRequests []capturedRequest
	for _, c := range captured {
		if c.path == "/v1/telemetry/pipeline-run" {
			runsRequests = append(runsRequests, c)
		}
	}
	if len(runsRequests) != 1 {
		t.Fatalf("expected 1 POST to /v1/telemetry/pipeline-run, got %d", len(runsRequests))
	}

	var batch []map[string]interface{}
	if err := json.Unmarshal(runsRequests[0].body, &batch); err != nil {
		t.Fatalf("unmarshal payload as bare top-level array (#261): %v", err)
	}
	if len(batch) != 1 {
		t.Fatalf("expected 1 record in bare array, got %d", len(batch))
	}
	payload := batch[0]

	// Verify routingPath is the parsed free-text routing path.
	routingPath, ok := payload["routingPath"].([]interface{})
	if !ok {
		t.Fatalf("payload routingPath is not an array: %v (%T)", payload["routingPath"], payload["routingPath"])
	}
	expectedPath := []string{"issue-pickup", "feature-planning", "feature-dev"}
	if len(routingPath) != len(expectedPath) {
		t.Errorf("routingPath length = %d, want %d: %v", len(routingPath), len(expectedPath), routingPath)
	} else {
		for i, expected := range expectedPath {
			if routingPath[i] != expected {
				t.Errorf("routingPath[%d] = %v, want %q", i, routingPath[i], expected)
			}
		}
	}

	// Verify complexityScore is the raw Fibonacci score (not a string label).
	if v, ok := payload["complexityScore"].(float64); !ok || int(v) != 3 {
		t.Errorf("payload complexityScore = %v, want 3", payload["complexityScore"])
	}

	// Verify per-stage token/cost/model data landed on the wire (Issue #1146's
	// open question: stage-level data IS available at the telemetry call site).
	stages, ok := payload["stages"].([]interface{})
	if !ok {
		t.Fatalf("payload stages is not an array: %v (%T)", payload["stages"], payload["stages"])
	}
	if len(stages) != 3 {
		t.Fatalf("stages length = %d, want 3", len(stages))
	}
	var featureDev map[string]interface{}
	for _, s := range stages {
		sm := s.(map[string]interface{})
		if sm["stageName"] == "feature-dev" {
			featureDev = sm
		}
	}
	if featureDev == nil {
		t.Fatal("feature-dev stage missing from payload stages")
	}
	if v, ok := featureDev["inputTokens"].(float64); !ok || int(v) != 500 {
		t.Errorf("feature-dev inputTokens = %v, want 500", featureDev["inputTokens"])
	}
	if v, ok := featureDev["model"].(string); !ok || v != "claude" {
		t.Errorf("feature-dev model = %v, want claude (adapter fallback)", featureDev["model"])
	}
	// V5 (#268): the per-stage adapter rides the wire as `provider` — the field
	// the platform persists to cost_events.provider and backfills onto
	// pipeline_events.adapter (Adapter Mix donut).
	if v, ok := featureDev["provider"].(string); !ok || v != "claude" {
		t.Errorf("feature-dev provider = %v, want claude (per-stage adapter)", featureDev["provider"])
	}
}

// TestIPCTelemetry_SyncTelemetry_MultipleRecords_AllPushed verifies that 3
// V2RunRecords produce 3 HTTP calls to /v1/telemetry/pipeline-run and
// Synced=3 in the IPC response.
func TestIPCTelemetry_SyncTelemetry_MultipleRecords_AllPushed(t *testing.T) {
	var (
		mu       sync.Mutex
		captured []capturedRequest
	)

	handlers := telemetryPlatformHandlers(t, &captured, &mu)
	srv := newMockPlatformServer(t, handlers)

	// Real runs (each has a completed stage + duration) so CanonicalizeRuns keeps
	// them — zero-everything records are dropped as synthetic noise.
	records := []state.V2RunRecord{
		{SchemaVersion: "2", IssueNumber: 1, StartedAt: "2026-03-15T10:00:00Z", CompletedAt: "2026-03-15T10:05:00Z", TotalDuration: 300000, Outcome: "complete", Stages: map[string]state.V2StageDetail{"feature-dev": {Status: "complete"}}},
		{SchemaVersion: "2", IssueNumber: 2, StartedAt: "2026-03-15T11:00:00Z", CompletedAt: "2026-03-15T11:05:00Z", TotalDuration: 300000, Outcome: "complete", Stages: map[string]state.V2StageDetail{"feature-dev": {Status: "complete"}}},
		{SchemaVersion: "2", IssueNumber: 3, StartedAt: "2026-03-15T12:00:00Z", CompletedAt: "2026-03-15T12:05:00Z", TotalDuration: 300000, Outcome: "failed", Stages: map[string]state.V2StageDetail{"feature-dev": {Status: "complete"}}},
	}
	workDir := setupTelemetryWorkspace(t, records)
	h := newIpcTelemetryHarness(t, workDir, srv.URL, "test-api-key")
	h.awaitReady()
	time.Sleep(500 * time.Millisecond)

	id := h.sendRequest("platform.syncTelemetry", map[string]interface{}{
		"repo": "test-org/test-repo",
	})
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("platform.syncTelemetry returned error: %+v", resp.Error)
	}

	resultBytes, _ := json.Marshal(resp.Result)
	var result PlatformSyncTelemetryResult
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.Synced != 3 {
		t.Errorf("expected 3 synced, got %d", result.Synced)
	}

	mu.Lock()
	defer mu.Unlock()

	var runsCount int
	for _, c := range captured {
		if c.path == "/v1/telemetry/pipeline-run" {
			runsCount++
		}
	}
	if runsCount != 3 {
		t.Errorf("expected 3 POST calls to /v1/telemetry/pipeline-run, got %d", runsCount)
	}
}

// TestIPCTelemetry_SyncTelemetry_NoPlatformClient verifies that when no
// platform is configured, platform.syncTelemetry returns an error with
// "platform client not configured" in the IPC error field.
func TestIPCTelemetry_SyncTelemetry_NoPlatformClient(t *testing.T) {
	h := newIpcTestHarness(t) // no platform flags
	h.awaitReady()

	id := h.sendRequest("platform.syncTelemetry", map[string]interface{}{})
	resp := h.readResponseFor(id, nil)

	if resp.Error == nil {
		t.Fatal("expected error when platform not configured, got nil")
	}
	if !strings.Contains(resp.Error.Message, "platform client not configured") {
		t.Errorf("expected 'platform client not configured' in error, got %q", resp.Error.Message)
	}
}

// TestIPCTelemetry_SubmitAnalytics_ReachesEndpoint verifies the
// platform.submitAnalytics fire-and-forget path via real subprocess: the IPC
// request triggers Ingest() which sends POST /v1/analytics/events to the mock.
func TestIPCTelemetry_SubmitAnalytics_ReachesEndpoint(t *testing.T) {
	var (
		mu       sync.Mutex
		captured []capturedRequest
	)

	handlers := telemetryPlatformHandlers(t, &captured, &mu)
	srv := newMockPlatformServer(t, handlers)

	// No history files needed — submitAnalytics doesn't read history.
	workDir := setupTelemetryWorkspace(t, nil)
	h := newIpcTelemetryHarness(t, workDir, srv.URL, "test-api-key")
	h.awaitReady()

	// Wait for health poller to mark platform as online.
	time.Sleep(500 * time.Millisecond)

	id := h.sendRequest("platform.submitAnalytics", map[string]interface{}{
		"eventType": "pipeline_run_completed",
		"payload": map[string]interface{}{
			"issueNumber": 42,
			"outcome":     "success",
		},
	})
	resp := h.readResponseFor(id, nil)
	if resp.Error != nil {
		t.Fatalf("platform.submitAnalytics returned error: %+v", resp.Error)
	}

	// Ingest is synchronous when the platform is online, so the HTTP call
	// completes before the IPC response. Add a small buffer for I/O scheduling.
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	var analyticsCount int
	for _, c := range captured {
		if c.path == "/v1/analytics/events" {
			analyticsCount++
		}
	}
	if analyticsCount == 0 {
		t.Errorf("expected at least 1 POST to /v1/analytics/events, got 0; all captured: %v",
			func() []string {
				var paths []string
				for _, c := range captured {
					paths = append(paths, c.method+" "+c.path)
				}
				return paths
			}())
	}
}
