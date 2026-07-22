package ipc

import (
	"testing"
	"time"
)

var telemetryNow = time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

func TestBuildStageTelemetryEvent_StageStartedCarriesRunCreationContext(t *testing.T) {
	ev, ok := buildStageTelemetryEvent("run-uuid", "nightgauge/acmeapp", 153, "issue-pickup", "running", "", 0, 0, 0, 0, 0.0, telemetryNow)
	if !ok {
		t.Fatal("expected running/issue-pickup to map to a platform event")
	}
	if ev.EventType != "stage_started" {
		t.Errorf("EventType = %q, want stage_started", ev.EventType)
	}
	if ev.RunID != "run-uuid" {
		t.Errorf("RunID = %q, want run-uuid", ev.RunID)
	}
	if ev.IssueNumber != 153 {
		t.Errorf("IssueNumber = %d, want 153", ev.IssueNumber)
	}
	if ev.Stage != "issue-pickup" {
		t.Errorf("Stage = %q, want issue-pickup", ev.Stage)
	}
	// Run-creation context is what materialises the live pipeline_runs row (#1047).
	if ev.Repo != "nightgauge/acmeapp" {
		t.Errorf("Repo = %q, want nightgauge/acmeapp", ev.Repo)
	}
	if ev.Origin != "local_cli" {
		t.Errorf("Origin = %q, want local_cli (platform enum: local_cli|cloud)", ev.Origin)
	}
	if ev.SchemaVersion != "1" {
		t.Errorf("SchemaVersion = %q, want 1", ev.SchemaVersion)
	}
}

func TestBuildStageTelemetryEvent_Completed(t *testing.T) {
	// #233: the authoritative per-stage token/cost totals ride stage_completed
	// alongside durationMs so the platform reconciles the live estimate.
	ev, ok := buildStageTelemetryEvent("run-uuid", "nightgauge/acmeapp", 153, "feature-dev", "complete", "", 4200, 12000, 3400, 900, 1.23, telemetryNow)
	if !ok {
		t.Fatal("expected complete to map to a platform event")
	}
	if ev.EventType != "stage_completed" {
		t.Errorf("EventType = %q, want stage_completed", ev.EventType)
	}
	if ev.DurationMs != 4200 {
		t.Errorf("DurationMs = %d, want 4200", ev.DurationMs)
	}
	if ev.InputTokens != 12000 || ev.OutputTokens != 3400 || ev.CacheReadTokens != 900 {
		t.Errorf("tokens = %d/%d/%d, want 12000/3400/900", ev.InputTokens, ev.OutputTokens, ev.CacheReadTokens)
	}
	if ev.CostUsd != 1.23 {
		t.Errorf("CostUsd = %v, want 1.23", ev.CostUsd)
	}
}

func TestBuildStageTelemetryEvent_Error(t *testing.T) {
	ev, ok := buildStageTelemetryEvent("run-uuid", "nightgauge/acmeapp", 153, "pr-create", "failed", "boom", 0, 0, 0, 0, 0.0, telemetryNow)
	if !ok {
		t.Fatal("expected failed to map to a platform event")
	}
	if ev.EventType != "stage_error" {
		t.Errorf("EventType = %q, want stage_error", ev.EventType)
	}
	if ev.Metadata["error"] != "boom" {
		t.Errorf("Metadata[error] = %v, want boom", ev.Metadata["error"])
	}
	if ev.Metadata["error_code"] != "STAGE_FAILED" {
		t.Errorf("Metadata[error_code] = %v, want STAGE_FAILED", ev.Metadata["error_code"])
	}
}

func TestBuildStageTelemetryEvent_SkipsBookendStages(t *testing.T) {
	// Bookend / internal stages are not in the platform's stage enum — emitting
	// them would 400. They must be skipped.
	for _, stage := range []string{"init", "pipeline-start", "pipeline-finish", "spike-materialize"} {
		if _, ok := buildStageTelemetryEvent("run-uuid", "nightgauge/acmeapp", 153, stage, "running", "", 0, 0, 0, 0, 0.0, telemetryNow); ok {
			t.Errorf("stage %q should be skipped (not a platform stage)", stage)
		}
	}
}

func TestBuildStageTelemetryEvent_SkipsNonEmittingStatus(t *testing.T) {
	for _, status := range []string{"initialized", "skipped", "deferred", "unknown"} {
		if _, ok := buildStageTelemetryEvent("run-uuid", "nightgauge/acmeapp", 153, "feature-dev", status, "", 0, 0, 0, 0, 0.0, telemetryNow); ok {
			t.Errorf("status %q should not map to a platform event", status)
		}
	}
}

func TestBuildStageTelemetryEvent_RequiresRunID(t *testing.T) {
	if _, ok := buildStageTelemetryEvent("", "nightgauge/acmeapp", 153, "issue-pickup", "running", "", 0, 0, 0, 0, 0.0, telemetryNow); ok {
		t.Error("empty runId must skip emission (platform requires a UUID runId)")
	}
}

func TestBuildStageProgressEvent(t *testing.T) {
	ev, ok := buildStageProgressEvent("run-uuid", "nightgauge/acmeapp", 153, "feature-dev", 1500, 800, 200, 0.42, telemetryNow)
	if !ok {
		t.Fatal("expected stage_progress to map to a platform event")
	}
	if ev.EventType != "stage_progress" {
		t.Errorf("EventType = %q, want stage_progress", ev.EventType)
	}
	if ev.RunID != "run-uuid" {
		t.Errorf("RunID = %q, want run-uuid", ev.RunID)
	}
	if ev.Stage != "feature-dev" {
		t.Errorf("Stage = %q, want feature-dev", ev.Stage)
	}
	if ev.InputTokens != 1500 || ev.OutputTokens != 800 || ev.CacheReadTokens != 200 {
		t.Errorf("tokens = (%d in, %d out, %d cache), want (1500, 800, 200)", ev.InputTokens, ev.OutputTokens, ev.CacheReadTokens)
	}
	if ev.CostUsd != 0.42 {
		t.Errorf("CostUsd = %v, want 0.42", ev.CostUsd)
	}
	if ev.SchemaVersion != "1" {
		t.Errorf("SchemaVersion = %q, want 1", ev.SchemaVersion)
	}
}

func TestBuildStageProgressEvent_RequiresRunID(t *testing.T) {
	// Progress is best-effort — with no runId to correlate, it must be skipped
	// rather than emitted (mirrors the stage/pipeline_done builders).
	if _, ok := buildStageProgressEvent("", "nightgauge/acmeapp", 153, "feature-dev", 100, 50, 0, 0.01, telemetryNow); ok {
		t.Error("empty runId must skip stage_progress emission")
	}
}

func TestBuildStageProgressEvent_SkipsBookendStages(t *testing.T) {
	// Bookend / internal stages are not in the platform's stage enum.
	for _, stage := range []string{"init", "pipeline-start", "pipeline-finish", "spike-materialize"} {
		if _, ok := buildStageProgressEvent("run-uuid", "nightgauge/acmeapp", 153, stage, 100, 50, 0, 0.01, telemetryNow); ok {
			t.Errorf("stage %q should be skipped (not a platform stage)", stage)
		}
	}
}

func TestBuildPipelineDoneEvent(t *testing.T) {
	p := PipelineNotifyCompleteParams{
		Repo:            "nightgauge/acmeapp",
		IssueNumber:     153,
		Success:         true,
		TotalDurationMs: 99000,
		// Includes a bookend that must be filtered out of stagesRun.
		StagesRun: []string{"issue-pickup", "feature-dev", "pipeline-finish", "pr-merge"},
	}
	ev, ok := buildPipelineDoneEvent("run-uuid", p, telemetryNow)
	if !ok {
		t.Fatal("expected pipeline_done event")
	}
	if ev.EventType != "pipeline_done" {
		t.Errorf("EventType = %q, want pipeline_done", ev.EventType)
	}
	if ev.Success == nil || !*ev.Success {
		t.Error("Success should be true")
	}
	if ev.TotalDurationMs != 99000 {
		t.Errorf("TotalDurationMs = %d, want 99000", ev.TotalDurationMs)
	}
	want := []string{"issue-pickup", "feature-dev", "pr-merge"}
	if len(ev.StagesRun) != len(want) {
		t.Fatalf("StagesRun = %v, want %v (bookends filtered)", ev.StagesRun, want)
	}
	for i := range want {
		if ev.StagesRun[i] != want[i] {
			t.Errorf("StagesRun[%d] = %q, want %q", i, ev.StagesRun[i], want[i])
		}
	}
}

func TestBuildPipelineDoneEvent_RequiresRunID(t *testing.T) {
	if _, ok := buildPipelineDoneEvent("", PipelineNotifyCompleteParams{IssueNumber: 1}, telemetryNow); ok {
		t.Error("empty runId must skip pipeline_done emission")
	}
}

// TestBuildPipelineDoneEvent_DeferredEmitsNonFailure verifies that a
// blocked-dependency deferral (#305) — reported as success=false, deferred=true
// — emits the terminal pipeline_done event as a NON-FAILURE (Success=true),
// since the pipeline_done wire has no "cancelled" state and a deferral must not
// paint a red "failed" row on the live Pipelines view. Issue #305.
func TestBuildPipelineDoneEvent_DeferredEmitsNonFailure(t *testing.T) {
	p := PipelineNotifyCompleteParams{
		Repo:            "nightgauge/nightgauge",
		IssueNumber:     305,
		Success:         false,
		Deferred:        true,
		TotalDurationMs: 1200,
	}
	ev, ok := buildPipelineDoneEvent("run-uuid", p, telemetryNow)
	if !ok {
		t.Fatal("expected pipeline_done event")
	}
	if ev.Success == nil || !*ev.Success {
		t.Error("deferred completion must emit Success=true (non-failure terminal), not failed")
	}
}

func TestFilterPlatformStages(t *testing.T) {
	in := []string{"init", "issue-pickup", "pipeline-finish", "pr-merge", "spike-materialize"}
	got := filterPlatformStages(in)
	want := []string{"issue-pickup", "pr-merge"}
	if len(got) != len(want) {
		t.Fatalf("filterPlatformStages(%v) = %v, want %v", in, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
