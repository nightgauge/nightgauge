package ipc

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/state"
)

// extensionTelemetryOrigin marks runs as executing on the user's machine,
// mirroring the Go scheduler's "local_cli". The platform's origin enum only
// accepts 'local_cli' | 'cloud'; the VSCode extension is a local execution.
const extensionTelemetryOrigin = "local_cli"

// filterPlatformStages returns only the canonical stages the platform accepts,
// preserving order. Used to sanitise the pipeline_done `stagesRun` list.
func filterPlatformStages(stages []string) []string {
	out := make([]string, 0, len(stages))
	for _, s := range stages {
		if platform.IsPipelineStage(s) {
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
	if runID == "" || !platform.IsPipelineStage(stage) {
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
	if runID == "" || !platform.IsPipelineStage(stage) {
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
func buildPipelineDoneEvent(runID string, p PipelineNotifyCompleteParams, heldForApproval bool, now time.Time) (platform.PipelineEvent, bool) {
	if runID == "" {
		return platform.PipelineEvent{}, false
	}
	// An architecture-approval hold (#2756) and a partial-delivery no-op
	// (#3487) are non-failures too.
	success := p.Success || p.Deferred || p.PartialNoOp || heldForApproval
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
// p's token/cost fields are the authoritative per-stage totals for a
// "complete" transition (#233); its pre-flight estimate rides the
// stage_started event it was sent with (#3455), which the extension does on
// the run's first "running" transition only.
func (s *Server) emitStageTelemetry(runID, repo string, p PipelineNotifyStageTransitionParams, rt *state.RuntimeState) {
	if s.analyticsSvc == nil || !s.TelemetryAllowed() {
		return
	}
	durationMs := 0
	if rt != nil {
		durationMs = rt.LastStageDurationMs()
	}
	event, ok := buildStageTelemetryEvent(runID, repo, p.IssueNumber, p.Stage, p.Status, p.Error, durationMs, p.InputTokens, p.OutputTokens, p.CacheReadTokens, p.CostUsd, time.Now())
	if !ok {
		return
	}
	if event.EventType == "stage_started" {
		event.EstimatedCostUsd = p.EstimatedCostUsd
		event.EstimateBasis = p.EstimateBasis
	}
	// A private run says so on every event (#2400), its first stage_started
	// included: the transition handler raised rt before calling here.
	event.Visibility = rt.RunVisibility()
	platform.StampPR(&event, rt.PRURL())
	s.analyticsSvc.EmitPipelineEvent(context.Background(), event)
}

// emitStageProgressTelemetry emits a live in-stage token/cost estimate to the
// platform as a stage_progress event (#233). Mirrors emitStageTelemetry:
// fire-and-forget through the same proven AnalyticsService, so it never blocks
// the IPC handler. Best-effort — a missing runID or bookend stage is skipped by
// buildStageProgressEvent rather than emitted. Its message is the stage's
// latest phase and tool step (#3455), so the run's activity line says what the
// stage is doing; platform.ProgressMessage caps it and fills an empty one.
func (s *Server) emitStageProgressTelemetry(runID, repo string, issueNumber int, stage string, inputTokens, outputTokens, cacheReadTokens int, costUsd float64, activity string, rt *state.RuntimeState) {
	if s.analyticsSvc == nil || !s.TelemetryAllowed() {
		return
	}
	event, ok := buildStageProgressEvent(runID, repo, issueNumber, stage, inputTokens, outputTokens, cacheReadTokens, costUsd, time.Now())
	if !ok {
		return
	}
	if msg := stageActivityMessage(rt.LatestPhaseLabel(state.PipelineStage(stage)), activity); msg != "" {
		event.Metadata = map[string]interface{}{"message": msg}
	}
	event.Visibility = rt.RunVisibility()
	platform.StampPR(&event, rt.PRURL())
	s.analyticsSvc.EmitPipelineEvent(context.Background(), event)
}

// stageActivitySeparator joins a stage's phase label and tool step in its
// stage_progress message (#3455).
const stageActivitySeparator = " · "

// stageActivityMax caps the client-sent tool step, matching the extension's
// TOOL_STEP_ACTIVITY_MAX (#3455).
const stageActivityMax = 120

// stageActivityMessage composes a stage_progress message from the stage's
// latest phase label and its latest tool step (#3455): both set gives
// "<phase> · <activity>", one set gives that one, neither gives "" so the
// platform falls back to "<stage> in progress". The tool step comes from an
// IPC client, so it is cut at the first control character and capped at
// stageActivityMax runes before it can reach the platform.
func stageActivityMessage(phaseLabel, activity string) string {
	phaseLabel = strings.TrimSpace(phaseLabel)
	activity = sanitizeStageActivity(activity)
	switch {
	case phaseLabel != "" && activity != "":
		return phaseLabel + stageActivitySeparator + activity
	case phaseLabel != "":
		return phaseLabel
	default:
		return activity
	}
}

// sanitizeStageActivity keeps a client-sent tool step to one line of at most
// stageActivityMax runes: it drops everything from the first control
// character (a newline included) and trims the rest.
func sanitizeStageActivity(activity string) string {
	if i := strings.IndexFunc(activity, unicode.IsControl); i >= 0 {
		activity = activity[:i]
	}
	activity = strings.TrimSpace(activity)
	if r := []rune(activity); len(r) > stageActivityMax {
		activity = strings.TrimSpace(string(r[:stageActivityMax]))
	}
	return activity
}

// emitPhaseTelemetry emits one in-stage phase transition as a stage_progress
// event (#3455). Called for extension-owned runs only: a scheduler-owned run's
// phases are emitted by the scheduler, and a second emitter would double them.
// index is 0-based, as the transition carries it; a transition the
// platform does not record (unreported, observed, superseded) sends nothing.
func (s *Server) emitPhaseTelemetry(runID, repo string, issueNumber int, stage, name string, index, total int, transition string, rt *state.RuntimeState) {
	if s.analyticsSvc == nil || !s.TelemetryAllowed() {
		return
	}
	event, ok := platform.PhaseProgressEvent(runID, repo, issueNumber, stage, name, index, total, transition, time.Now())
	if !ok {
		return
	}
	event.Visibility = rt.RunVisibility()
	platform.StampPR(&event, rt.PRURL())
	s.analyticsSvc.EmitPipelineEvent(context.Background(), event)
}

// emitPipelineDoneTelemetry emits the terminal pipeline_done event so the live
// Pipelines view transitions the run from 'running' to 'complete'/'failed'.
// snap is the claimed terminal snapshot; its PR URL rides the event (#3455).
func (s *Server) emitPipelineDoneTelemetry(runID string, p PipelineNotifyCompleteParams, heldForApproval bool, snap *state.RuntimeState) {
	if s.analyticsSvc == nil || !s.TelemetryAllowed() {
		return
	}
	event, ok := buildPipelineDoneEvent(runID, p, heldForApproval, time.Now())
	if !ok {
		return
	}
	if snap != nil {
		event.Visibility = snap.Visibility
	}
	platform.StampPR(&event, snap.PRURL())
	s.analyticsSvc.EmitPipelineEvent(context.Background(), event)
}

// heldForApproval reports whether a failed run stopped on a deliberate human
// hold, read from the terminating stage's gate-sourced terminal kind — the same
// source the run record's terminal_failure_kind comes from. Issue #2756.
func heldForApproval(snap *state.RuntimeState) bool {
	if snap == nil {
		return false
	}
	results := snap.StageGateResults[string(snap.Stage)]
	return len(results) > 0 && orchestrator.IsNonFailureTerminalKind(results[len(results)-1].TerminalKind)
}
