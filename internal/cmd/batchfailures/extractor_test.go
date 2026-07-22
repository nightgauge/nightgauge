package batchfailures

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePipelineFile writes a file under workdir/.nightgauge/pipeline/.
func writePipelineFile(t *testing.T, workdir, name, content string) {
	t.Helper()
	dir := filepath.Join(workdir, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// writeHistoryFile writes a file under workdir/.nightgauge/pipeline/history/.
func writeHistoryFile(t *testing.T, workdir, name, content string) {
	t.Helper()
	dir := filepath.Join(workdir, ".nightgauge", "pipeline", "history")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestExtract_EmptyWorkdir(t *testing.T) {
	dir := t.TempDir()
	res, err := Extract(Options{Workdir: dir})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.V != SchemaVersion {
		t.Errorf("V = %d, want %d", res.V, SchemaVersion)
	}
	if len(res.BatchFailures) != 0 || len(res.HistoryFailures) != 0 || len(res.ContextFailures) != 0 {
		t.Errorf("expected zero failures, got batch=%d history=%d context=%d",
			len(res.BatchFailures), len(res.HistoryFailures), len(res.ContextFailures))
	}
	if res.Batch != nil {
		t.Errorf("Batch should be nil when batch-state.json is missing")
	}
	if res.SkippedRecords != 0 {
		t.Errorf("SkippedRecords = %d, want 0", res.SkippedRecords)
	}
}

func TestExtract_BatchStateOnly(t *testing.T) {
	dir := t.TempDir()
	writePipelineFile(t, dir, "batch-state.json", `{
		"status": "partial",
		"started_at": "2026-05-01T12:00:00Z",
		"updated_at": "2026-05-01T13:00:00Z",
		"issueResults": [
			{
				"issueNumber": 100,
				"title": "good run",
				"status": "completed",
				"completedStages": ["pipeline-start","issue-pickup","feature-planning","feature-dev","feature-validate","pr-create","pr-merge","pipeline-finish"],
				"durationMs": 1000,
				"tokenUsage": {"input": 10}
			},
			{
				"issueNumber": 200,
				"title": "stalled run",
				"status": "failed",
				"completedStages": ["pipeline-start","issue-pickup"],
				"durationMs": 500,
				"tokenUsage": {}
			}
		]
	}`)
	res, err := Extract(Options{Workdir: dir})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.Batch == nil || res.Batch.TotalIssues != 2 {
		t.Fatalf("Batch summary mismatch: %+v", res.Batch)
	}
	if len(res.BatchFailures) != 1 {
		t.Fatalf("expected 1 batch failure, got %d", len(res.BatchFailures))
	}
	f := res.BatchFailures[0]
	if f.IssueNumber != 200 {
		t.Errorf("IssueNumber = %d, want 200", f.IssueNumber)
	}
	if f.Source != SourceBatchState {
		t.Errorf("Source = %q, want %q", f.Source, SourceBatchState)
	}
	if len(f.FailedStages) == 0 {
		t.Errorf("expected non-empty FailedStages, got %v", f.FailedStages)
	}
	// The good run completed all 8 canonical stages, so it should NOT be in failures.
	for _, ff := range res.BatchFailures {
		if ff.IssueNumber == 100 {
			t.Errorf("issue 100 should not appear (completed all stages)")
		}
	}
}

func TestExtract_HistoryOnly(t *testing.T) {
	dir := t.TempDir()
	writeHistoryFile(t, dir, "2026-04-22.jsonl", strings.Join([]string{
		`{"record_type":"run","issue_number":300,"title":"ok","outcome":"complete","started_at":"2026-04-22T10:00:00Z","total_duration_ms":1000,"stages":{"feature-dev":{"status":"complete"}},"tokens":{"estimated_cost_usd":0.5}}`,
		`{"record_type":"run","issue_number":400,"title":"failed run","outcome":"failed","started_at":"2026-04-22T11:00:00Z","total_duration_ms":2000,"stages":{"feature-dev":{"status":"complete"},"feature-validate":{"status":"failed"}},"tokens":{"estimated_cost_usd":1.5}}`,
	}, "\n"))

	res, err := Extract(Options{Workdir: dir})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.HistoryFailures) != 1 {
		t.Fatalf("expected 1 history failure, got %d", len(res.HistoryFailures))
	}
	hf := res.HistoryFailures[0]
	if hf.IssueNumber != 400 {
		t.Errorf("IssueNumber = %d, want 400", hf.IssueNumber)
	}
	if hf.Source != SourceHistory {
		t.Errorf("Source = %q, want %q", hf.Source, SourceHistory)
	}
	if hf.StageFailures["feature-validate"] != "failed" {
		t.Errorf("stage_failures[feature-validate] = %q, want failed", hf.StageFailures["feature-validate"])
	}
	if hf.EstimatedCostUSD != 1.5 {
		t.Errorf("EstimatedCostUSD = %f, want 1.5", hf.EstimatedCostUSD)
	}
}

func TestExtract_HistorySinceFilter(t *testing.T) {
	dir := t.TempDir()
	failedLine := `{"record_type":"run","issue_number":900,"outcome":"failed","stages":{},"tokens":{}}`
	writeHistoryFile(t, dir, "2026-04-01.jsonl", failedLine)
	writeHistoryFile(t, dir, "2026-04-22.jsonl", failedLine)

	res, err := Extract(Options{Workdir: dir, Since: "2026-04-15"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.HistoryFailures) != 1 {
		t.Fatalf("Since filter should keep only 1 file's failures, got %d", len(res.HistoryFailures))
	}

	// AllFailures=true disables the Since filter.
	res2, _ := Extract(Options{Workdir: dir, Since: "2026-04-15", AllFailures: true})
	if len(res2.HistoryFailures) != 2 {
		t.Errorf("AllFailures=true should ignore Since, got %d failures", len(res2.HistoryFailures))
	}
}

func TestExtract_HistoryIssueFilter(t *testing.T) {
	dir := t.TempDir()
	writeHistoryFile(t, dir, "2026-04-22.jsonl", strings.Join([]string{
		`{"record_type":"run","issue_number":501,"outcome":"failed","stages":{},"tokens":{}}`,
		`{"record_type":"run","issue_number":502,"outcome":"failed","stages":{},"tokens":{}}`,
	}, "\n"))

	res, err := Extract(Options{Workdir: dir, Issue: 502})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.HistoryFailures) != 1 || res.HistoryFailures[0].IssueNumber != 502 {
		t.Errorf("Issue filter mismatch: %+v", res.HistoryFailures)
	}
}

func TestExtract_HistoryMalformedLineSkipped(t *testing.T) {
	dir := t.TempDir()
	writeHistoryFile(t, dir, "2026-04-22.jsonl", strings.Join([]string{
		`{"record_type":"run","issue_number":701,"outcome":"failed","stages":{},"tokens":{}}`,
		`not-json`,
		``,
		`{"record_type":"run","issue_number":702,"outcome":"failed","stages":{},"tokens":{}}`,
	}, "\n"))

	res, err := Extract(Options{Workdir: dir})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.SkippedRecords != 1 {
		t.Errorf("SkippedRecords = %d, want 1", res.SkippedRecords)
	}
	if len(res.HistoryFailures) != 2 {
		t.Errorf("expected 2 valid failures, got %d", len(res.HistoryFailures))
	}
}

func TestExtract_ContextFilesFallback(t *testing.T) {
	dir := t.TempDir()
	pipelineDir := filepath.Join(dir, ".nightgauge", "pipeline")
	if err := os.MkdirAll(pipelineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"issue-100.json",
		"issue-200.json",
		"issue-300.json",
		"pr-200.json", // 200 has PR — should not flag
		"dev-300.json",
	} {
		if err := os.WriteFile(filepath.Join(pipelineDir, name), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	res, err := Extract(Options{Workdir: dir})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	got := map[int]bool{}
	for _, f := range res.ContextFailures {
		got[f.IssueNumber] = f.HasDevContext
	}
	if _, ok := got[100]; !ok {
		t.Errorf("expected 100 in context_failures")
	}
	if _, ok := got[200]; ok {
		t.Errorf("200 has pr context — should not appear")
	}
	if hasDev, ok := got[300]; !ok || !hasDev {
		t.Errorf("expected 300 with HasDevContext=true, got ok=%v hasDev=%v", ok, hasDev)
	}
}

// TestExtract_JSONSchemaStability asserts the JSON output keys retro Phase 3
// consumes. Field-name regressions break the skill silently — this test pins
// the shape.
func TestExtract_JSONSchemaStability(t *testing.T) {
	dir := t.TempDir()
	writePipelineFile(t, dir, "batch-state.json", `{
		"status": "partial",
		"started_at": "2026-05-01T12:00:00Z",
		"updated_at": "2026-05-01T13:00:00Z",
		"issueResults": [{"issueNumber":1,"title":"x","status":"failed","completedStages":[],"durationMs":0,"tokenUsage":{}}]
	}`)
	writeHistoryFile(t, dir, "2026-04-22.jsonl",
		`{"record_type":"run","issue_number":2,"title":"y","outcome":"failed","started_at":"2026-04-22T10:00:00Z","total_duration_ms":100,"stages":{},"tokens":{"estimated_cost_usd":0.1}}`)

	res, err := Extract(Options{Workdir: dir})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	out, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	requiredTopLevel := []string{"v", "filters", "batch", "batch_failures", "history_failures", "context_failures", "skipped_records", "warnings"}
	for _, k := range requiredTopLevel {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing required top-level key %q", k)
		}
	}

	bf := raw["batch_failures"].([]any)[0].(map[string]any)
	for _, k := range []string{"issue_number", "title", "status", "completed_stages", "failed_stages", "duration_ms", "token_usage", "source"} {
		if _, ok := bf[k]; !ok {
			t.Errorf("batch_failures[].%q missing", k)
		}
	}

	hf := raw["history_failures"].([]any)[0].(map[string]any)
	for _, k := range []string{"issue_number", "title", "outcome", "started_at", "total_duration_ms", "stage_failures", "estimated_cost_usd", "source"} {
		if _, ok := hf[k]; !ok {
			t.Errorf("history_failures[].%q missing", k)
		}
	}
}
