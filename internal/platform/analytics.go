package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
	"github.com/nightgauge/nightgauge/internal/state"
)

// AnalyticsEvent represents a single analytics event to push.
type AnalyticsEvent struct {
	Type      string                 `json:"type"`
	Timestamp time.Time              `json:"timestamp"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

// Note: ExecutionHistoryRunRecord (the wire shape PushPipelineRun/SyncTelemetry
// push to POST /v1/telemetry/pipeline-run) is defined in
// execution_history_run_record.go; the mapper lives in
// execution_history_mapper.go. The retired PipelineRunRecord (the
// /v1/pipelines/runs wire shape) has been removed — see Issue
// The platform ingestion contract.

// PipelineEvent is the in-binary representation of a pipeline lifecycle event.
// emitPipelineEventSync maps it to the platform's POST /v1/pipelines/events wire
// contract (camelCase `type`/`runId`, per-type fields) — see buildEventWire.
// Fields beyond the core four are per-event-type and optional.
type PipelineEvent struct {
	RunID         string                 `json:"run_id,omitempty"`
	IssueNumber   int                    `json:"issue_number"`
	EventType     string                 `json:"event_type"` // stage_started | stage_completed | stage_error | pipeline_done
	Stage         string                 `json:"stage"`
	Timestamp     time.Time              `json:"timestamp"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	SchemaVersion string                 `json:"schema_version"`

	// Run-creation context (stage_started) — lets the platform materialise a
	// status='running' row for the live Pipelines view (#1047).
	Repo   string `json:"-"`
	Origin string `json:"-"`

	// Branch + perf mode (stage_started) — surface the feature branch and the
	// performance-mode badge on the in-flight 'running' row. Branch is empty on
	// the first stage (issue-pickup hasn't resolved it yet); the platform
	// enriches the row on a later stage_started. Mode must already be in the
	// dashboard's efficiency|elevated|maximum vocabulary (mapped at the emit
	// site); empty when unresolvable (e.g. the premium 'frontier' tier) so it's
	// omitted from the wire rather than sent as an unrenderable value.
	Branch string `json:"-"`
	Mode   string `json:"-"`

	// Per-type measurements surfaced as first-class fields for the platform
	// contract. DurationMs (stage_completed); TotalDurationMs + StagesRun +
	// Success (pipeline_done). Zero/nil when not applicable.
	DurationMs      int
	TotalDurationMs int
	StagesRun       []string
	Success         *bool

	// Per-stage token/cost measurements (#233). On stage_progress these carry
	// the LIVE in-stage estimate: InputTokens/CacheReadTokens are latest-wins
	// snapshots of the growing context, OutputTokens is summed per-turn, and
	// CostUsd is the pricing-table-computed estimate. On stage_completed they
	// carry the authoritative terminal totals. Zero when not applicable.
	InputTokens     int
	OutputTokens    int
	CacheReadTokens int
	CostUsd         float64
}

// QueueSyncItem is one entry in a machine's queue snapshot pushed to
// PUT /v1/queue/sync. Mirrors the platform's SyncQueueItemSchema. Priority is
// one of critical|high|medium|low; Status is one of pending|processing.
type QueueSyncItem struct {
	IssueNumber  int    `json:"issueNumber"`
	Position     int    `json:"position"`
	Priority     string `json:"priority,omitempty"`
	Status       string `json:"status"`
	RepoFullName string `json:"repoFullName,omitempty"`
	Title        string `json:"title,omitempty"`
}

// QueueSyncPayload is the body for PUT /v1/queue/sync — a single machine's full
// queue snapshot. The platform replaces all rows for (account, machineId) with
// these items, so the dashboard reflects exactly the local queue-state.json.
type QueueSyncPayload struct {
	MachineID string          `json:"machineId"`
	Origin    string          `json:"origin"`
	Items     []QueueSyncItem `json:"items"`
}

// AnalyticsService handles fire-and-forget analytics ingestion with local buffering.
type AnalyticsService struct {
	client     *Client
	mu         sync.Mutex
	buffer     []bufferedBatch             // Ingest-path retries
	runQueue   []ExecutionHistoryRunRecord // PushPipelineRun retries
	eventQueue []PipelineEvent             // EmitPipelineEvent retries
}

type bufferedBatch struct {
	RunID       string
	IssueNumber int
	Events      []AnalyticsEvent
}

const (
	analyticsFlushInterval = 30 * time.Second
	maxBufferSize          = 500
)

// NewAnalyticsService creates an analytics ingestion service.
func NewAnalyticsService(client *Client) *AnalyticsService {
	return &AnalyticsService{
		client: client,
	}
}

// Ingest sends analytics events to the platform. If offline, buffers locally.
func (s *AnalyticsService) Ingest(ctx context.Context, runID string, issueNumber int, events []AnalyticsEvent) {
	if !s.client.IsOnline() {
		s.bufferEvents(runID, issueNumber, events)
		return
	}

	apiEvents := make([]struct {
		Data      *map[string]interface{} `json:"data,omitempty"`
		Timestamp time.Time               `json:"timestamp"`
		Type      string                  `json:"type"`
	}, len(events))

	for i, e := range events {
		apiEvents[i].Type = e.Type
		apiEvents[i].Timestamp = e.Timestamp
		if e.Data != nil {
			data := e.Data
			apiEvents[i].Data = &data
		}
	}

	resp, err := s.client.api.AnalyticsIngestWithResponse(ctx, api.AnalyticsIngestJSONRequestBody{
		RunId:       runID,
		IssueNumber: issueNumber,
		Events:      apiEvents,
	})
	if err != nil || resp.JSON200 == nil {
		// Fire-and-forget: buffer on failure
		s.bufferEvents(runID, issueNumber, events)
	}
}

// bufferEvents stores events locally for later flush.
func (s *AnalyticsService) bufferEvents(runID string, issueNumber int, events []AnalyticsEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.buffer) >= maxBufferSize {
		// Drop oldest to prevent unbounded growth
		s.buffer = s.buffer[1:]
	}

	s.buffer = append(s.buffer, bufferedBatch{
		RunID:       runID,
		IssueNumber: issueNumber,
		Events:      events,
	})
}

// FlushBuffered pushes all buffered events to the platform.
func (s *AnalyticsService) FlushBuffered(ctx context.Context) int {
	if !s.client.IsOnline() {
		return 0
	}

	s.mu.Lock()
	pending := make([]bufferedBatch, len(s.buffer))
	copy(pending, s.buffer)
	s.buffer = nil
	s.mu.Unlock()

	flushed := 0
	for _, batch := range pending {
		apiEvents := make([]struct {
			Data      *map[string]interface{} `json:"data,omitempty"`
			Timestamp time.Time               `json:"timestamp"`
			Type      string                  `json:"type"`
		}, len(batch.Events))

		for i, e := range batch.Events {
			apiEvents[i].Type = e.Type
			apiEvents[i].Timestamp = e.Timestamp
			if e.Data != nil {
				data := e.Data
				apiEvents[i].Data = &data
			}
		}

		resp, err := s.client.api.AnalyticsIngestWithResponse(ctx, api.AnalyticsIngestJSONRequestBody{
			RunId:       batch.RunID,
			IssueNumber: batch.IssueNumber,
			Events:      apiEvents,
		})
		if err != nil || resp.JSON200 == nil {
			// Re-buffer on failure
			s.bufferEvents(batch.RunID, batch.IssueNumber, batch.Events)
			continue
		}
		flushed += len(batch.Events)
	}

	// Flush buffered pipeline run records
	s.mu.Lock()
	pendingRuns := make([]ExecutionHistoryRunRecord, len(s.runQueue))
	copy(pendingRuns, s.runQueue)
	s.runQueue = nil
	s.mu.Unlock()

	for _, run := range pendingRuns {
		if err := s.pushPipelineRunSync(ctx, run); err != nil {
			log.Printf("platform: flush PushPipelineRun failed, re-queuing: %v", err)
			s.enqueueRun(run)
			continue
		}
		flushed++
	}

	// Flush buffered pipeline events
	s.mu.Lock()
	pendingEvents := make([]PipelineEvent, len(s.eventQueue))
	copy(pendingEvents, s.eventQueue)
	s.eventQueue = nil
	s.mu.Unlock()

	for _, event := range pendingEvents {
		if err := s.emitPipelineEventSync(ctx, event); err != nil {
			log.Printf("platform: flush EmitPipelineEvent failed, re-queuing: %v", err)
			s.enqueueEvent(event)
			continue
		}
		flushed++
	}

	return flushed
}

// StartAutoFlush runs periodic buffer flushes in the background.
func (s *AnalyticsService) StartAutoFlush(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(analyticsFlushInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n := s.FlushBuffered(ctx); n > 0 {
					log.Printf("analytics: flushed %d buffered events", n)
				}
			}
		}
	}()
}

// BufferedCount returns the number of buffered batches.
func (s *AnalyticsService) BufferedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.buffer)
}

// enqueueRun buffers an ExecutionHistoryRunRecord for later retry.
func (s *AnalyticsService) enqueueRun(run ExecutionHistoryRunRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.runQueue) >= maxBufferSize {
		s.runQueue = s.runQueue[1:]
	}
	s.runQueue = append(s.runQueue, run)
}

// enqueueEvent buffers a PipelineEvent for later retry.
func (s *AnalyticsService) enqueueEvent(event PipelineEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.eventQueue) >= maxBufferSize {
		s.eventQueue = s.eventQueue[1:]
	}
	s.eventQueue = append(s.eventQueue, event)
}

// RunQueueCount returns the number of buffered pipeline run records.
func (s *AnalyticsService) RunQueueCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runQueue)
}

// EventQueueCount returns the number of buffered pipeline events.
func (s *AnalyticsService) EventQueueCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.eventQueue)
}

// UsageSummaryResult is the IPC-facing representation of analytics usage data.
// Maps from the platform's DashboardSummary (GET /v1/analytics/dashboard):
// TotalRuns/TotalTokens come from `usage`; Period is the bucket type. The
// extension's PlatformQuotaService renders only runs + tokens. SuccessRatePct
// and TotalCostUsd are not provided by this license-key endpoint and remain
// zero (sourced elsewhere, JWT-only, for the web dashboard).
type UsageSummaryResult struct {
	TotalRuns      int     `json:"totalRuns"`
	SuccessRatePct float64 `json:"successRatePct"`
	TotalCostUsd   float64 `json:"totalCostUsd"`
	TotalTokens    int     `json:"totalTokens"`
	Period         string  `json:"period"`
}

// PushPipelineRun pushes a completed pipeline run record to the platform's
// canonical telemetry sink, POST /v1/telemetry/pipeline-run (Issue
// This replaces the retired
// POST /v1/pipelines/runs sink). Fire-and-forget: launches a goroutine, logs
// errors, does not block the caller. Buffers the record for retry when
// offline or on HTTP failure.
func (s *AnalyticsService) PushPipelineRun(ctx context.Context, run ExecutionHistoryRunRecord) {
	go func() {
		if !s.client.IsOnline() {
			log.Printf("platform: PushPipelineRun buffered (offline), issue=%d", run.IssueNumber)
			s.enqueueRun(run)
			return
		}
		if err := s.pushPipelineRunSync(ctx, run); err != nil {
			log.Printf("platform: PushPipelineRun failed, buffered for retry: %v", err)
			s.enqueueRun(run)
		}
	}()
}

// pushPipelineRunMaxAttempts bounds how many times pushPipelineRunSync retries a
// single record when the platform returns 429 (rate limited). Bulk callers (the
// backfill) can post far more records than the platform's global request budget
// allows in one window, so each record may need to wait out a window.
const pushPipelineRunMaxAttempts = 6

// The wire body for POST /v1/telemetry/pipeline-run is a BARE JSON array of
// records (or a single bare record) — the shape the platform's canonical
// telemetry routes actually parse (`Array.isArray(body) ? body : [body]`).
// The previous `{"records": [...]}` envelope was silently rejected on every
// push: the route wrapped the envelope object itself as one "record", strict
// Zod validation failed it, and the 202 response reported the rejection in a
// body nobody read — zero telemetry ever ingested (#261).

// executionHistoryPushResponse mirrors the platform's 202 Accepted body for
// POST /v1/telemetry/pipeline-run — a per-record {accepted, rejected} report.
// A 202 status alone is NOT proof every record was accepted; the body must be
// parsed, mirroring TelemetryUploaderService.ts's identical handling.
type executionHistoryPushResponse struct {
	Accepted int `json:"accepted"`
	Rejected []struct {
		Index  int    `json:"index"`
		Reason string `json:"reason"`
	} `json:"rejected"`
}

// pushPipelineRunSync is the synchronous implementation called by PushPipelineRun.
// It retries on HTTP 429 (rate limited), honouring a Retry-After header when
// present, else exponential backoff — so bulk backfills ride out the platform's
// global per-minute request budget instead of dropping records.
//
// A record the platform rejects at the application level (reported inside a
// 2xx body's `rejected` array — see executionHistoryPushResponse) is a
// validation failure that will never succeed on retry (a poison message), so
// it is logged loudly and treated as terminal — NOT buffered for retry —
// mirroring TelemetryUploaderService.ts's identical handling.
func (s *AnalyticsService) pushPipelineRunSync(ctx context.Context, run ExecutionHistoryRunRecord) error {
	data, err := json.Marshal([]ExecutionHistoryRunRecord{run})
	if err != nil {
		return fmt.Errorf("marshal pipeline run record: %w", err)
	}

	for attempt := 0; attempt < pushPipelineRunMaxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			s.client.base+"/v1/telemetry/pipeline-run", bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("create push pipeline run request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if s.client.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+s.client.apiKey)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("push pipeline run POST: %w", err)
		}
		status := resp.StatusCode
		retryAfter := resp.Header.Get("Retry-After")

		if status == http.StatusTooManyRequests {
			resp.Body.Close()
			wait := retryBackoff(attempt, retryAfter)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		if status >= 400 {
			resp.Body.Close()
			return fmt.Errorf("push pipeline run: server returned %d", status)
		}

		var body executionHistoryPushResponse
		decErr := json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if decErr != nil {
			// Unparseable 2xx body: the server returned success, so treat the
			// record as accepted rather than re-sending (matches
			// TelemetryUploaderService.ts's "assuming batch accepted").
			return nil
		}
		if len(body.Rejected) > 0 {
			log.Printf("platform: PushPipelineRun REJECTED by platform (issue=%d) — likely a producer/consumer schema or status-vocab skew; reason=%q — not retrying (poison message)",
				run.IssueNumber, body.Rejected[0].Reason)
		}
		return nil
	}
	return fmt.Errorf("push pipeline run: still rate limited (429) after %d attempts", pushPipelineRunMaxAttempts)
}

// retryBackoff returns how long to wait before retrying a 429. It honours a
// Retry-After header (integer seconds) when present, otherwise uses exponential
// backoff (1s, 2s, 4s, …) capped at 30s.
func retryBackoff(attempt int, retryAfter string) time.Duration {
	const cap = 30 * time.Second
	if secs, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && secs > 0 {
		d := time.Duration(secs) * time.Second
		if d > cap {
			return cap
		}
		return d
	}
	d := time.Duration(1<<uint(attempt)) * time.Second
	if d > cap {
		return cap
	}
	return d
}

// EmitPipelineEvent sends a real-time pipeline event to POST /v1/pipelines/events.
// Fire-and-forget: launches a goroutine, logs errors, does not block the caller.
// Buffers the event for retry when offline or on HTTP failure.
func (s *AnalyticsService) EmitPipelineEvent(ctx context.Context, event PipelineEvent) {
	go func() {
		if !s.client.IsOnline() {
			s.enqueueEvent(event)
			return
		}
		if err := s.emitPipelineEventSync(ctx, event); err != nil {
			log.Printf("platform: EmitPipelineEvent failed, buffered for retry: %v", err)
			s.enqueueEvent(event)
		}
	}()
}

// platformEventTypes are the event types POST /v1/pipelines/events accepts.
// Other (local-only) event types are skipped rather than POSTed and rejected.
var platformEventTypes = map[string]bool{
	"stage_started":   true,
	"stage_progress":  true,
	"stage_completed": true,
	"stage_error":     true,
	"pipeline_done":   true,
}

// buildEventWire maps a PipelineEvent to the platform's ingest contract
// (camelCase `type`/`runId`, per-type required fields). Returns nil for event
// types the platform does not model so the caller skips the POST. stage_started
// carries the run-creation context (issueNumber/repo/origin) that materialises
// the live `status='running'` row (#1047).
func buildEventWire(e PipelineEvent) map[string]interface{} {
	if !platformEventTypes[e.EventType] {
		return nil
	}
	w := map[string]interface{}{
		"type":  e.EventType,
		"runId": e.RunID,
		// The platform's Zod `z.string().datetime()` only accepts UTC with a
		// trailing 'Z' — it rejects numeric timezone offsets (e.g. "-06:00"),
		// which is what time.Time's default JSON marshaling emits in local time.
		// Normalise to UTC RFC3339 so every event validates. Without this, the
		// ingest endpoint 400s on "Invalid ISO datetime" and the run never
		// appears in the live Pipelines view.
		"timestamp": e.Timestamp.UTC().Format(time.RFC3339Nano),
	}
	switch e.EventType {
	case "stage_started":
		w["stage"] = e.Stage
		if e.IssueNumber > 0 {
			w["issueNumber"] = e.IssueNumber
		}
		if e.Repo != "" {
			w["repo"] = e.Repo
		}
		if e.Origin != "" {
			w["origin"] = e.Origin
		}
		// Branch + perf mode enrich the live 'running' row. Both optional: the
		// branch isn't known until issue-pickup completes, and `mode` is only
		// emitted when it maps to the dashboard's efficiency|elevated|maximum
		// vocabulary (the scheduler omits unresolvable modes like 'frontier').
		if e.Branch != "" {
			w["branch"] = e.Branch
		}
		if e.Mode != "" {
			w["mode"] = e.Mode
		}
	case "stage_progress":
		w["stage"] = e.Stage
		w["message"] = truncate(metaString(e.Metadata, "message"), 1000)
		// Live in-stage token/cost estimate (#233). Cumulative snapshot for the
		// in-flight stage: input/cacheRead latest-wins, output summed, cost
		// pricing-table-computed. Reconciled by the authoritative stage_completed
		// totals at stage end.
		w["inputTokens"] = e.InputTokens
		w["outputTokens"] = e.OutputTokens
		w["cacheReadTokens"] = e.CacheReadTokens
		w["costUsd"] = e.CostUsd
	case "stage_completed":
		w["stage"] = e.Stage
		w["durationMs"] = e.DurationMs
		// Authoritative final token/cost totals for the stage (#233), mirrored
		// from the terminal CLI `result` envelope. Same field names the
		// stage_progress case emits so the platform reconciles the live estimate
		// against these on completion.
		w["inputTokens"] = e.InputTokens
		w["outputTokens"] = e.OutputTokens
		w["cacheReadTokens"] = e.CacheReadTokens
		w["costUsd"] = e.CostUsd
	case "stage_error":
		// `stage` is nullable in the contract — send null when unknown.
		if e.Stage != "" {
			w["stage"] = e.Stage
		} else {
			w["stage"] = nil
		}
		w["errorCode"] = truncate(firstNonEmpty(metaString(e.Metadata, "error_code"), "STAGE_FAILED"), 100)
		w["message"] = truncate(firstNonEmpty(metaString(e.Metadata, "error"), "stage failed"), 2000)
		w["retryable"] = false
	case "pipeline_done":
		w["totalDurationMs"] = e.TotalDurationMs
		stages := e.StagesRun
		if stages == nil {
			stages = []string{}
		}
		w["stagesRun"] = stages
		if e.Success != nil {
			w["success"] = *e.Success
		}
	}
	return w
}

func metaString(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// truncate caps a string to n runes (Zod .max() is on string length), keeping
// UTF-8 valid so the platform never rejects on an oversized field.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// emitPipelineEventSync is the synchronous implementation of EmitPipelineEvent.
func (s *AnalyticsService) emitPipelineEventSync(ctx context.Context, event PipelineEvent) error {
	wire := buildEventWire(event)
	if wire == nil {
		// Local-only event type the platform does not accept — no-op.
		return nil
	}

	data, err := json.Marshal(wire)
	if err != nil {
		return fmt.Errorf("marshal pipeline event: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.client.base+"/v1/pipelines/events", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create emit pipeline event request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.client.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("emit pipeline event POST: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("emit pipeline event: server returned %d", resp.StatusCode)
	}
	return nil
}

// SyncQueue mirrors a machine's queue snapshot to the platform via
// PUT /v1/queue/sync so the web dashboard shows live queued/working items.
// Fire-and-forget: launches a goroutine, logs errors, never blocks the caller.
//
// Unlike run records, queue snapshots are NOT buffered when offline: the queue
// is a latest-wins snapshot, and the next persistQueue() re-pushes current
// state — replaying a stale snapshot could overwrite newer cloud state. A
// no-op when the machine id is empty (sync scope is unresolved).
func (s *AnalyticsService) SyncQueue(ctx context.Context, payload QueueSyncPayload) {
	if payload.MachineID == "" {
		return
	}
	go func() {
		if !s.client.IsOnline() {
			return
		}
		if err := s.syncQueueSync(ctx, payload); err != nil {
			log.Printf("platform: SyncQueue failed: %v", err)
		}
	}()
}

// syncQueueSync is the synchronous implementation called by SyncQueue.
func (s *AnalyticsService) syncQueueSync(ctx context.Context, payload QueueSyncPayload) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal queue sync payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		s.client.base+"/v1/queue/sync", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create queue sync request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.client.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("queue sync PUT: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("queue sync: server returned %d", resp.StatusCode)
	}
	return nil
}

// SyncTelemetryResult holds the outcome of a batch telemetry sync.
type SyncTelemetryResult struct {
	Synced int
	Failed int
	Errors []string
}

// syncThrottleInterval spaces out the per-record POSTs in SyncTelemetry so a
// large backfill stays under the platform's global per-minute request budget
// (~600/min). ~120ms ≈ 500/min steady state; the 429-retry in pushPipelineRunSync
// rides out any remaining bursts. Negligible for the small IPC sync path.
const syncThrottleInterval = 120 * time.Millisecond

// SyncTelemetry converts V2RunRecords to ExecutionHistoryRunRecords and
// pushes each to the platform's canonical telemetry sink synchronously —
// POST /v1/telemetry/pipeline-run (Issue
// This replaces the retired
// POST /v1/pipelines/runs sink). Returns counts of synced and failed records.
//
// The records are canonicalized first (CanonicalizeRuns) so duplicate records
// the local history writes per logical run fold into one, and pure-synthetic
// noise is dropped. Unlike the retired sink, this endpoint has no
// client-supplied run id to key an idempotent upsert on — the
// ExecutionHistoryRunRecordV4 schema carries no `runId` field at all, so the
// platform derives run identity itself from (repo, issueNumber, startedAt);
// this mirrors the VSCode extension's TelemetryUploaderService, which also
// sends no id (see pipelineRunV4Mapper.ts). accountID is likewise resolved
// server-side from the auth credential and is no longer a mapper input — the
// parameter was accepted here previously but every caller always passed nil.
func (s *AnalyticsService) SyncTelemetry(ctx context.Context, records []state.V2RunRecord, repo string) SyncTelemetryResult {
	canonical, _ := CanonicalizeRuns(records)

	var result SyncTelemetryResult
	for i, rec := range canonical {
		// Throttle between posts to stay under the platform's global per-minute
		// request budget; skipped before the first record. Abort cleanly (return
		// the partial result) if the caller's context is cancelled mid-sleep.
		if i > 0 {
			select {
			case <-ctx.Done():
				return result
			case <-time.After(syncThrottleInterval):
			}
		}

		// The mapper derives `retries` from rec.AttemptsUntilSuccess, which is
		// persisted on-disk (Issue #4172) and survives the backfill round-trip —
		// see retriesFromAttempts.
		runRecord, err := V2RunRecordToExecutionHistoryRunRecord(rec, ExecutionHistoryMapperInput{Repo: repo})
		if err != nil {
			result.Failed++
			result.Errors = append(result.Errors, fmt.Sprintf("issue %d: map record: %v", rec.IssueNumber, err))
			continue
		}

		if err := s.pushPipelineRunSync(ctx, runRecord); err != nil {
			result.Failed++
			result.Errors = append(result.Errors, fmt.Sprintf("issue %d: push: %v", rec.IssueNumber, err))
			continue
		}
		result.Synced++
	}
	return result
}

// CostAnalyticsResult is the IPC-facing representation of platform cost analytics data.
// Maps from the platform's GET /v1/analytics/cost response.
type CostAnalyticsResult struct {
	TotalInputTokens  int    `json:"totalInputTokens"`
	TotalOutputTokens int    `json:"totalOutputTokens"`
	TotalTokens       int    `json:"totalTokens"`
	TotalCostUsd      string `json:"totalCostUsd"`
	Breakdown         struct {
		ByModel []struct {
			ModelId string `json:"modelId"`
			CostUsd string `json:"costUsd"`
			Tokens  int    `json:"tokens"`
		} `json:"byModel"`
		ByProject []struct {
			ProjectId *string `json:"projectId"`
			CostUsd   string  `json:"costUsd"`
		} `json:"byProject"`
		ByDay []struct {
			Date    string `json:"date"`
			CostUsd string `json:"costUsd"`
		} `json:"byDay"`
	} `json:"breakdown"`
}

// GetCostAnalytics fetches cost analytics from GET /v1/analytics/cost.
// startDate and endDate are optional ISO 8601 date strings (e.g., "2026-04-01").
// Returns an empty result if offline.
func (s *AnalyticsService) GetCostAnalytics(ctx context.Context, startDate, endDate string) (*CostAnalyticsResult, error) {
	if !s.client.IsOnline() {
		return &CostAnalyticsResult{}, nil
	}

	url := s.client.base + "/v1/analytics/cost"
	sep := "?"
	if startDate != "" {
		url += sep + "startDate=" + startDate
		sep = "&"
	}
	if endDate != "" {
		url += sep + "endDate=" + endDate
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create cost analytics request: %w", err)
	}
	if s.client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.client.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get cost analytics: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("get cost analytics: server returned %d", resp.StatusCode)
	}

	var result CostAnalyticsResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode cost analytics response: %w", err)
	}
	return &result, nil
}

// AnalyticsHealthFinding is a single finding from a health dimension (#3318).
type AnalyticsHealthFinding struct {
	Severity       string `json:"severity"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	Recommendation string `json:"recommendation"`
	IssueNumber    *int   `json:"issue_number,omitempty"`
}

// AnalyticsHealthDimension is a scored dimension from the health response (#3318).
type AnalyticsHealthDimension struct {
	Name     string                   `json:"name"`
	Score    float64                  `json:"score"`
	Label    string                   `json:"label"`
	Findings []AnalyticsHealthFinding `json:"findings"`
}

// AnalyticsHealthResult is the IPC-facing representation of GET /v1/analytics/health (#3318).
type AnalyticsHealthResult struct {
	OverallScore float64                    `json:"overall_score"`
	Dimensions   []AnalyticsHealthDimension `json:"dimensions"`
	GeneratedAt  string                     `json:"generated_at"`
	PeriodDays   int                        `json:"period_days"`
	TotalRuns    int                        `json:"total_runs"`
}

// GetAnalyticsHealth fetches the 7-dimension health score from GET /v1/analytics/health.
// Uses a raw HTTP GET because the generated OpenAPI client does not yet include this endpoint.
// TODO: replace with generated client method when platform spec is regenerated.
// Returns an empty result if offline.
func (s *AnalyticsService) GetAnalyticsHealth(ctx context.Context) (*AnalyticsHealthResult, error) {
	if !s.client.IsOnline() {
		return &AnalyticsHealthResult{}, nil
	}

	url := s.client.base + "/v1/analytics/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create analytics health request: %w", err)
	}
	if s.client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.client.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get analytics health: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("get analytics health: server returned %d", resp.StatusCode)
	}

	var result AnalyticsHealthResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode analytics health response: %w", err)
	}
	return &result, nil
}

// RunsStageEntry holds per-stage detail for a single pipeline run (#3319).
type RunsStageEntry struct {
	Name            string `json:"name"`
	Model           string `json:"model"`
	DurationMs      int64  `json:"duration_ms"`
	InputTokens     int    `json:"input_tokens"`
	OutputTokens    int    `json:"output_tokens"`
	CostUsd         string `json:"cost_usd"`
	RetryCount      int    `json:"retry_count"`
	FailureCategory string `json:"failure_category,omitempty"`
}

// RunsEntry is a single row in the runs list returned by GET /v1/analytics/runs (#3319).
type RunsEntry struct {
	IssueNumber  int              `json:"issue_number"`
	Title        string           `json:"title"`
	Branch       string           `json:"branch"`
	Outcome      string           `json:"outcome"`
	DurationMs   int64            `json:"duration_ms"`
	TotalCostUsd string           `json:"total_cost_usd"`
	StartedAt    string           `json:"started_at"`
	Stages       []RunsStageEntry `json:"stages,omitempty"`
}

// AnalyticsRunsResult is the IPC-facing representation of GET /v1/analytics/runs (#3319).
type AnalyticsRunsResult struct {
	Entries    []RunsEntry `json:"entries"`
	TotalCount int         `json:"total_count"`
	NextCursor string      `json:"next_cursor,omitempty"`
	HasMore    bool        `json:"has_more"`
}

// GetAnalyticsRuns fetches paginated pipeline run history from GET /v1/analytics/runs.
// Uses a raw HTTP GET because the generated OpenAPI client does not yet include this endpoint.
// Returns an empty result if offline.
func (s *AnalyticsService) GetAnalyticsRuns(ctx context.Context, startDate, endDate, cursor, outcome, branch string, limit int) (*AnalyticsRunsResult, error) {
	if !s.client.IsOnline() {
		return &AnalyticsRunsResult{Entries: []RunsEntry{}}, nil
	}

	url := s.client.base + "/v1/analytics/runs"
	sep := "?"
	if startDate != "" {
		url += sep + "startDate=" + startDate
		sep = "&"
	}
	if endDate != "" {
		url += sep + "endDate=" + endDate
		sep = "&"
	}
	if cursor != "" {
		url += sep + "cursor=" + cursor
		sep = "&"
	}
	if outcome != "" {
		url += sep + "outcome=" + outcome
		sep = "&"
	}
	if branch != "" {
		url += sep + "branch=" + branch
		sep = "&"
	}
	if limit > 0 {
		url += fmt.Sprintf("%slimit=%d", sep, limit)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create analytics runs request: %w", err)
	}
	if s.client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.client.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get analytics runs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("get analytics runs: server returned %d", resp.StatusCode)
	}

	var result AnalyticsRunsResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode analytics runs response: %w", err)
	}
	if result.Entries == nil {
		result.Entries = []RunsEntry{}
	}
	return &result, nil
}

// AnalyticsTrendEntry is a single time-bucketed data point from GET /v1/analytics/trends (#3320).
type AnalyticsTrendEntry struct {
	Date        string  `json:"date"`
	SuccessRate float64 `json:"successRate"`
	CostPerRun  float64 `json:"costPerRun"`
	TotalRuns   int     `json:"totalRuns"`
}

// AnalyticsTrendsResult is the IPC-facing representation of GET /v1/analytics/trends (#3320).
type AnalyticsTrendsResult struct {
	Current  []AnalyticsTrendEntry `json:"current"`
	Previous []AnalyticsTrendEntry `json:"previous"`
	Period   string                `json:"period"`
}

// GetAnalyticsTrends fetches longitudinal pipeline trends from GET /v1/analytics/trends.
// Returns an empty offline result when the platform is unreachable.
func (s *AnalyticsService) GetAnalyticsTrends(ctx context.Context, period string) (*AnalyticsTrendsResult, error) {
	if !s.client.IsOnline() {
		return &AnalyticsTrendsResult{Current: []AnalyticsTrendEntry{}, Previous: []AnalyticsTrendEntry{}}, nil
	}
	url := s.client.base + "/v1/analytics/trends?period=" + period
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create analytics trends request: %w", err)
	}
	if s.client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.client.apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get analytics trends: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("get analytics trends: server returned %d", resp.StatusCode)
	}
	var result AnalyticsTrendsResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode analytics trends response: %w", err)
	}
	if result.Current == nil {
		result.Current = []AnalyticsTrendEntry{}
	}
	if result.Previous == nil {
		result.Previous = []AnalyticsTrendEntry{}
	}
	return &result, nil
}

// GetUsageSummary fetches the analytics dashboard summary from the platform.
// Returns a zero-value summary if offline.
//
// Maps from the platform's DashboardSummary contract
// (period/quota/usage/team/recentRuns). Runs and tokens come from `usage`;
// success-rate and per-model cost are NOT part of this endpoint — they live on
// the JWT-only /v1/analytics/cost and /v1/analytics/trends endpoints (the web
// dashboard's concern, unreachable on the license-key pipeline path), so those
// two fields are left zero here rather than silently reading a struct field the
// platform never sends (the prior "reads zeros for everything" bug).
func (s *AnalyticsService) GetUsageSummary(ctx context.Context) (*UsageSummaryResult, error) {
	if !s.client.IsOnline() {
		return &UsageSummaryResult{}, nil
	}

	r := api.GetAnalyticsDashboardParamsRangeN7d
	resp, err := s.client.api.GetAnalyticsDashboardWithResponse(ctx, &api.GetAnalyticsDashboardParams{
		Range: &r,
	})
	if err != nil {
		return nil, fmt.Errorf("get analytics dashboard: %w", err)
	}

	if resp.JSON200 == nil {
		return nil, fmt.Errorf("get analytics dashboard: unexpected response %d", resp.StatusCode())
	}

	return &UsageSummaryResult{
		TotalRuns:   resp.JSON200.Usage.PipelineRunsThisPeriod,
		TotalTokens: resp.JSON200.Usage.TokenUsageThisPeriod,
		Period:      string(resp.JSON200.Period.Type),
	}, nil
}
