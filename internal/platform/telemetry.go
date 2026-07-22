package platform

import (
	"context"
	"log"

	"github.com/nightgauge/nightgauge/internal/state"
)

// TelemetryService pushes pipeline run records to the platform.
// All methods are fire-and-forget — errors are logged, never propagated.
type TelemetryService struct {
	analytics *AnalyticsService
}

// NewTelemetryService creates a telemetry service backed by the given platform client.
func NewTelemetryService(client *Client) *TelemetryService {
	return &TelemetryService{
		analytics: NewAnalyticsService(client),
	}
}

// PushPipelineRun pushes a completed pipeline run record to the platform's
// canonical telemetry sink. Fire-and-forget: if offline, the record is
// buffered for retry by StartAutoFlush.
//
// It maps the local V2RunRecord to an ExecutionHistoryRunRecord and hands it
// to AnalyticsService.PushPipelineRun, which posts to
// POST /v1/telemetry/pipeline-run — the single canonical pipeline-run
// telemetry sink. This
// replaces the previous POST /v1/pipelines/runs sink (nightgauge#4162 /
// #1143), which the platform-side effort for #1146 retires: only
// /v1/telemetry/pipeline-run writes the full analytics surface
// (usage_events/cost_events/pipeline_outcomes) the dashboard's cost/token
// widgets read, not just pipeline_runs.
//
// Stage-level token/cost/provider data is already present on `record` by the
// time this runs — see V2RunRecordToExecutionHistoryRunRecord's doc comment
// for why (recordOutcome / buildRunRecordForTelemetry in
// internal/orchestrator/scheduler.go builds it via the same
// state.HistoryWriter.BuildV2Record used for the on-disk JSONL history).
func (s *TelemetryService) PushPipelineRun(ctx context.Context, record state.V2RunRecord) {
	runRecord, err := V2RunRecordToExecutionHistoryRunRecord(record, ExecutionHistoryMapperInput{
		Repo: record.Repo,
	})
	if err != nil {
		log.Printf("telemetry: map pipeline run record (issue %d): %v", record.IssueNumber, err)
		return
	}

	// Fire-and-forget with offline buffering + retry handled inside.
	s.analytics.PushPipelineRun(ctx, runRecord)
}

// EmitPipelineEvent sends a real-time stage event to the platform.
// Fire-and-forget: errors are buffered and retried automatically.
func (s *TelemetryService) EmitPipelineEvent(ctx context.Context, event PipelineEvent) {
	s.analytics.EmitPipelineEvent(ctx, event)
}

// SyncQueue mirrors the local queue snapshot to the platform. The caller (the
// scheduler) supplies only the items; this wrapper stamps the machine id (from
// the platform client's resolved agent id) and the local_cli origin so callers
// stay identity-agnostic. Fire-and-forget.
func (s *TelemetryService) SyncQueue(ctx context.Context, items []QueueSyncItem) {
	s.analytics.SyncQueue(ctx, QueueSyncPayload{
		MachineID: s.analytics.client.AgentID(),
		Origin:    "local_cli",
		Items:     items,
	})
}

// StartAutoFlush starts periodic background flushing of buffered analytics data.
func (s *TelemetryService) StartAutoFlush(ctx context.Context) {
	s.analytics.StartAutoFlush(ctx)
}
