package ipc

import (
	"context"
	"time"

	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/state"
)

// platformPipelineStages is the exact set of stages the platform's
// /v1/pipelines/events contract accepts (Zod `z.enum(PIPELINE_STAGES)`).
// Bookend/internal stages (init, pipeline-start, pipeline-finish,
// spike-materialize) are intentionally excluded — emitting them would fail the
// platform's enum validation and drop the event.
var platformPipelineStages = map[string]bool{
	string(state.StageIssuePickup):     true,
	string(state.StageFeaturePlanning): true,
	string(state.StageFeatureDev):      true,
	string(state.StageFeatureValidate): true,
	string(state.StagePRCreate):        true,
	string(state.StagePRMerge):         true,
}

// extensionTelemetryOrigin marks runs as executing on the user's machine,
// mirroring the Go scheduler's "local_cli". The platform's origin enum only
// accepts 'local_cli' | 'cloud'; the VSCode extension is a local execution.
const extensionTelemetryOrigin = "local_cli"

// filterPlatformStages returns only the canonical stages the platform accepts,
// preserving order. Used to sanitise the pipeline_done `stagesRun` list.
func filterPlatformStages(stages []string) []string {
	out := make([]string, 0, len(stages))
	for _, s := range stages {
		if platformPipelineStages[s] {
			out = append(out, s)
		}
	}
	return out
}

// buildStageTelemetryEvent maps a TypeScript-orchestrator stage transition onto
// the platform's real-time pipeline event contract. Returns (event, true) when
// the transition maps to a platform event, or (zero, false) when it must be
// skipped (missing runId, non-canonical/bookend stage, or a status with no
// platform event such as initialized/skipped/deferred).
//
// Pure and side-effect-free so the mapping is unit-testable without a live
// AnalyticsService. The actual emit lives in emitStageTelemetry.
//
// The token/cost args are the authoritative per-stage totals (#233), threaded
// from the notify "complete" transition (which #227 populates from the terminal
// CLI `result` envelope). They ride the stage_completed event so the platform
// reconciles the live stage_progress estimate against the real totals on
// completion. Zero for running/failed transitions (not applicable there).
func buildStageTelemetryEvent(runID, repo string, issueNumber int, stage, status, errMsg string, durationMs, inputTokens, outputTokens, cacheReadTokens int, costUsd float64, now time.Time) (platform.PipelineEvent, bool) {
	if runID == "" || !platformPipelineStages[stage] {
		return platform.PipelineEvent{}, false
	}

	event := platform.PipelineEvent{
		RunID:         runID,
		IssueNumber:   issueNumber,
		Stage:         stage,
		Timestamp:     now,
		SchemaVersion: "1",
	}

	switch status {
	case "running":
		event.EventType = "stage_started"
		// Run-creation context: the first stage_started materialises a live
		// status='running' pipeline_runs row (#1047).
		event.Repo = repo
		event.Origin = extensionTelemetryOrigin
	case "complete":
		event.EventType = "stage_completed"
		event.DurationMs = durationMs
		// Authoritative final totals so stage_completed reconciles the live
		// stage_progress estimate (#233).
		event.InputTokens = inputTokens
		event.OutputTokens = outputTokens
		event.CacheReadTokens = cacheReadTokens
		event.CostUsd = costUsd
	case "failed":
		event.EventType = "stage_error"
		event.Metadata = map[string]interface{}{
			"error_code": "STAGE_FAILED",
			"error":      errMsg,
		}
	default:
		// initialized / skipped / deferred have no platform event.
		return platform.PipelineEvent{}, false
	}

	return event, true
}

// buildStageProgressEvent maps a live in-stage token/cost estimate onto the
// platform's stage_progress event (#233). Returns (event, true) when the
// progress maps to a platform event, or (zero, false) when it must be skipped
// (missing runId or a non-canonical/bookend stage the platform's enum rejects).
//
// Pure and side-effect-free so the mapping is unit-testable without a live
// AnalyticsService. The tokens are the LIVE estimate (input/cacheRead
// latest-wins, output summed, cost pricing-table-computed); the authoritative
// stage_completed totals reconcile them at stage end.
func buildStageProgressEvent(runID, repo string, issueNumber int, stage string, inputTokens, outputTokens, cacheReadTokens int, costUsd float64, now time.Time) (platform.PipelineEvent, bool) {
	if runID == "" || !platformPipelineStages[stage] {
		return platform.PipelineEvent{}, false
	}
	return platform.PipelineEvent{
		RunID:           runID,
		Repo:            repo,
		IssueNumber:     issueNumber,
		EventType:       "stage_progress",
		Stage:           stage,
		Timestamp:       now,
		SchemaVersion:   "1",
		InputTokens:     inputTokens,
		OutputTokens:    outputTokens,
		CacheReadTokens: cacheReadTokens,
		CostUsd:         costUsd,
	}, true
}

// buildPipelineDoneEvent maps a completion notification onto the terminal
// pipeline_done event. Returns (zero, false) when there is no runId to correlate.
//
// The pipeline_done wire carries only a boolean Success — it has no "cancelled"
// state — so a blocked-dependency deferral (#305), which is a NON-FAILURE
// booked locally as outcome="cancelled", is emitted through the non-failure
// (success-shaped terminal) branch rather than as failed. Without this the live
// Pipelines view would flip a deferral to a red "failed" row.
func buildPipelineDoneEvent(runID string, p PipelineNotifyCompleteParams, now time.Time) (platform.PipelineEvent, bool) {
	if runID == "" {
		return platform.PipelineEvent{}, false
	}
	success := p.Success || p.Deferred
	return platform.PipelineEvent{
		RunID:           runID,
		IssueNumber:     p.IssueNumber,
		EventType:       "pipeline_done",
		Timestamp:       now,
		TotalDurationMs: p.TotalDurationMs,
		StagesRun:       filterPlatformStages(p.StagesRun),
		Success:         &success,
		SchemaVersion:   "1",
	}, true
}

// emitStageTelemetry emits a stage transition to the platform via the proven
// AnalyticsService (same emitter + license the Go scheduler uses). The
// extension/HeadlessOrchestrator path bypasses the Go scheduler, so this is the
// only place these runs become visible to the live Pipelines view.
//
// Fire-and-forget: AnalyticsService.EmitPipelineEvent launches its own
// goroutine and buffers on failure, so this never blocks the IPC handler.
//
// inputTokens/outputTokens/cacheReadTokens/costUsd are the authoritative
// per-stage totals for a "complete" transition (#233), threaded from the notify
// params #227 populates; they are 0 (and unused) for running/failed.
func (s *Server) emitStageTelemetry(runID, repo string, issueNumber int, stage, status, errMsg string, inputTokens, outputTokens, cacheReadTokens int, costUsd float64, rt *state.RuntimeState) {
	if s.analyticsSvc == nil {
		return
	}
	durationMs := 0
	if rt != nil {
		durationMs = rt.LastStageDurationMs()
	}
	event, ok := buildStageTelemetryEvent(runID, repo, issueNumber, stage, status, errMsg, durationMs, inputTokens, outputTokens, cacheReadTokens, costUsd, time.Now())
	if !ok {
		return
	}
	s.analyticsSvc.EmitPipelineEvent(context.Background(), event)
}

// emitStageProgressTelemetry emits a live in-stage token/cost estimate to the
// platform as a stage_progress event (#233). Mirrors emitStageTelemetry:
// fire-and-forget through the same proven AnalyticsService, so it never blocks
// the IPC handler. Best-effort — a missing runID or bookend stage is skipped by
// buildStageProgressEvent rather than emitted.
func (s *Server) emitStageProgressTelemetry(runID, repo string, issueNumber int, stage string, inputTokens, outputTokens, cacheReadTokens int, costUsd float64) {
	if s.analyticsSvc == nil {
		return
	}
	event, ok := buildStageProgressEvent(runID, repo, issueNumber, stage, inputTokens, outputTokens, cacheReadTokens, costUsd, time.Now())
	if !ok {
		return
	}
	s.analyticsSvc.EmitPipelineEvent(context.Background(), event)
}

// emitPipelineDoneTelemetry emits the terminal pipeline_done event so the live
// Pipelines view transitions the run from 'running' to 'complete'/'failed'.
func (s *Server) emitPipelineDoneTelemetry(runID string, p PipelineNotifyCompleteParams) {
	if s.analyticsSvc == nil {
		return
	}
	event, ok := buildPipelineDoneEvent(runID, p, time.Now())
	if !ok {
		return
	}
	s.analyticsSvc.EmitPipelineEvent(context.Background(), event)
}
