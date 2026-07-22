package platform

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

func fullTestRecord() state.V2RunRecord {
	size := "L"
	return state.V2RunRecord{
		SchemaVersion: "2",
		RecordType:    "run",
		IssueNumber:   42,
		Repo:          "nightgauge/nightgauge",
		Title:         "Test issue",
		Branch:        "feat/42-test",
		StartedAt:     "2026-04-01T10:00:00-06:00",
		CompletedAt:   "2026-04-01T10:05:00-06:00",
		TotalDuration: 300000,
		Outcome:       "complete",
		Size:          &size,
		Stages: map[string]state.V2StageDetail{
			"issue-pickup": {
				Status:         "complete",
				DurationMs:     1000,
				ModelSelection: &state.V2ModelSelect{Model: "claude-sonnet-4-5", Source: "routing"},
			},
			"feature-dev": {
				Status:     "complete",
				DurationMs: 2000,
			},
		},
		Tokens: state.V2Tokens{
			EstimatedCostUSD: 0.5,
			PerStage: map[string]state.V2StageTokens{
				"issue-pickup": {Input: 1000, Output: 200, CostUSD: 0.1, Adapter: "claude"},
				"feature-dev":  {Input: 3000, Output: 800, CostUSD: 0.0, Adapter: "codex"},
			},
		},
		Routing: state.V2Routing{
			ComplexityScore: 3,
			Path:            "issue-pickup,feature-dev",
		},
		OutcomePrediction: &state.OutcomePrediction{
			PredictedModel: "claude-sonnet-4-5",
			ActualModel:    "claude-sonnet-4-5",
		},
	}
}

func TestV2RunRecordToExecutionHistoryRunRecord_HappyPath(t *testing.T) {
	rec := fullTestRecord()

	got, err := V2RunRecordToExecutionHistoryRunRecord(rec, ExecutionHistoryMapperInput{Repo: "nightgauge/nightgauge"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.SchemaVersion != 5 {
		t.Errorf("SchemaVersion = %d, want 5", got.SchemaVersion)
	}
	if got.IssueNumber != 42 {
		t.Errorf("IssueNumber = %d, want 42", got.IssueNumber)
	}
	if got.Repo != "nightgauge/nightgauge" {
		t.Errorf("Repo = %q, want owner/repo", got.Repo)
	}
	if got.StartedAt != "2026-04-01T16:00:00Z" {
		t.Errorf("StartedAt = %q, want UTC 'Z' normalised", got.StartedAt)
	}
	if got.CompletedAt == nil || *got.CompletedAt != "2026-04-01T16:05:00Z" {
		t.Errorf("CompletedAt = %v, want UTC 'Z' normalised pointer", got.CompletedAt)
	}
	if got.Outcome != "complete" {
		t.Errorf("Outcome = %q, want %q", got.Outcome, "complete")
	}
	if got.TerminalFailureKind != nil {
		t.Errorf("TerminalFailureKind = %v, want nil", got.TerminalFailureKind)
	}
	if got.ActualSize == nil || *got.ActualSize != "L" {
		t.Errorf("ActualSize = %v, want pointer to L", got.ActualSize)
	}
	if got.PredictedSize != nil {
		t.Errorf("PredictedSize = %v, want nil (local vocabulary mismatch)", got.PredictedSize)
	}
	if got.PredictedModel == nil || *got.PredictedModel != "claude-sonnet-4-5" {
		t.Errorf("PredictedModel = %v, want pointer to claude-sonnet-4-5", got.PredictedModel)
	}
	if got.ActualModel == nil || *got.ActualModel != "claude-sonnet-4-5" {
		t.Errorf("ActualModel = %v, want pointer to claude-sonnet-4-5", got.ActualModel)
	}
	if got.ComplexityScore == nil || *got.ComplexityScore != 3 {
		t.Errorf("ComplexityScore = %v, want pointer to 3", got.ComplexityScore)
	}
	if got.Retries != 0 {
		t.Errorf("Retries = %d, want 0 (AttemptsUntilSuccess unset)", got.Retries)
	}
	if got.DurationMs == nil || *got.DurationMs != 300000 {
		t.Errorf("DurationMs = %v, want pointer to 300000", got.DurationMs)
	}
	if got.TotalCostUsd == nil || *got.TotalCostUsd != 0.5 {
		t.Errorf("TotalCostUsd = %v, want pointer to 0.5 (run-level estimate)", got.TotalCostUsd)
	}
	if len(got.Agents) != 0 || got.Agents == nil {
		t.Errorf("Agents = %v, want non-nil empty slice", got.Agents)
	}
	if want := []string{"issue-pickup", "feature-dev"}; !equalStrings(got.RoutingPath, want) {
		t.Errorf("RoutingPath = %v, want %v", got.RoutingPath, want)
	}

	if len(got.Stages) != 2 {
		t.Fatalf("len(Stages) = %d, want 2", len(got.Stages))
	}
	// canonicalStageOrder places issue-pickup before feature-dev.
	s0 := got.Stages[0]
	if s0.StageID != "issue-pickup" || s0.StageName != "issue-pickup" {
		t.Errorf("Stages[0] id/name = %q/%q, want issue-pickup", s0.StageID, s0.StageName)
	}
	if s0.Model == nil || *s0.Model != "claude-sonnet-4-5" {
		t.Errorf("Stages[0].Model = %v, want pointer to claude-sonnet-4-5 (from ModelSelection)", s0.Model)
	}
	if s0.Provider == nil || *s0.Provider != "claude" {
		t.Errorf("Stages[0].Provider = %v, want pointer to claude (from per-stage adapter)", s0.Provider)
	}
	if s0.InputTokens != 1000 || s0.OutputTokens != 200 || s0.TotalTokens != 1200 {
		t.Errorf("Stages[0] tokens = %d/%d/%d, want 1000/200/1200", s0.InputTokens, s0.OutputTokens, s0.TotalTokens)
	}
	if s0.CostUsd == nil || *s0.CostUsd != 0.1 {
		t.Errorf("Stages[0].CostUsd = %v, want pointer to 0.1", s0.CostUsd)
	}
	if s0.DurationMs == nil || *s0.DurationMs != 1000 {
		t.Errorf("Stages[0].DurationMs = %v, want pointer to 1000", s0.DurationMs)
	}
	if !s0.Success {
		t.Error("Stages[0].Success = false, want true")
	}
	if s0.Attempt != 1 {
		t.Errorf("Stages[0].Attempt = %d, want 1", s0.Attempt)
	}

	s1 := got.Stages[1]
	if s1.StageID != "feature-dev" {
		t.Errorf("Stages[1] id = %q, want feature-dev", s1.StageID)
	}
	if s1.Model == nil || *s1.Model != "codex" {
		t.Errorf("Stages[1].Model = %v, want pointer to codex (adapter fallback, no ModelSelection)", s1.Model)
	}
	if s1.Provider == nil || *s1.Provider != "codex" {
		t.Errorf("Stages[1].Provider = %v, want pointer to codex (from per-stage adapter)", s1.Provider)
	}
	if s1.CostUsd != nil {
		t.Errorf("Stages[1].CostUsd = %v, want nil (zero cost omitted)", s1.CostUsd)
	}
}

// TestV2RunRecordToExecutionHistoryRunRecord_ProviderNullWhenNoAdapter asserts a
// stage with no recorded adapter marshals `"provider": null` (present key, null
// value) — the V5 schema declares provider `.nullable()` but NOT `.optional()`,
// so the key must never be dropped (#268).
func TestV2RunRecordToExecutionHistoryRunRecord_ProviderNullWhenNoAdapter(t *testing.T) {
	rec := state.V2RunRecord{
		IssueNumber: 7,
		StartedAt:   "2026-04-01T10:00:00Z",
		Outcome:     "complete",
		Stages: map[string]state.V2StageDetail{
			"feature-dev": {Status: "complete", DurationMs: 1000},
		},
		Tokens: state.V2Tokens{
			PerStage: map[string]state.V2StageTokens{
				// No Adapter field → provider must serialize as null.
				"feature-dev": {Input: 100, Output: 50, CostUSD: 0.01},
			},
		},
	}

	got, err := V2RunRecordToExecutionHistoryRunRecord(rec, ExecutionHistoryMapperInput{Repo: "owner/repo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Stages) != 1 {
		t.Fatalf("len(Stages) = %d, want 1", len(got.Stages))
	}
	if got.Stages[0].Provider != nil {
		t.Errorf("Stages[0].Provider = %v, want nil (no adapter recorded)", got.Stages[0].Provider)
	}

	data, err := json.Marshal(got.Stages[0])
	if err != nil {
		t.Fatalf("marshal stage: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal stage: %v", err)
	}
	if v, ok := m["provider"]; !ok {
		t.Error("stage JSON missing required key \"provider\"")
	} else if string(v) != "null" {
		t.Errorf(`provider = %s, want "null"`, v)
	}
}

// TestV2RunRecordToExecutionHistoryRunRecord_KilledStageBooksEstimatedCost is
// the telemetry-mapper half of the #296 contract. When a stage is killed
// mid-flight the TS SkillRunner now books the live cost estimate into
// pipeline.stageResult.costUsd (instead of $0), so the scheduler records it via
// CompleteStageWithCost and it lands in the V3 history record's per_stage
// cost_usd. This test asserts the mapper then surfaces that non-zero cost on the
// killed stage's telemetry metric AND folds it into the run-level total — the
// exact path that under-reported before #296 (bowlsheet #262: run showed $3.70
// while feature-dev really burned $9.15 and carried NO per_stage entry).
func TestV2RunRecordToExecutionHistoryRunRecord_KilledStageBooksEstimatedCost(t *testing.T) {
	rec := state.V2RunRecord{
		SchemaVersion: "3",
		RecordType:    "run",
		IssueNumber:   262,
		Repo:          "acme/mobile",
		StartedAt:     "2026-07-19T12:00:00Z",
		Outcome:       "failed",
		Stages: map[string]state.V2StageDetail{
			"issue-pickup":     {Status: "complete", DurationMs: 1000},
			"feature-planning": {Status: "complete", DurationMs: 2000},
			// feature-dev was SIGTERM'd by the runaway monitor — a failed stage
			// that nonetheless carries its live-estimate cost (#296).
			"feature-dev": {Status: "failed", DurationMs: 300000},
		},
		Tokens: state.V2Tokens{
			PerStage: map[string]state.V2StageTokens{
				"issue-pickup":     {Input: 1000, Output: 200, CostUSD: 0.50, Adapter: "claude"},
				"feature-planning": {Input: 8000, Output: 1500, CostUSD: 3.20, Adapter: "claude"},
				// The killed stage's real burn, booked from the live estimator.
				"feature-dev": {Input: 400000, Output: 40000, CostUSD: 9.1496, Adapter: "claude"},
			},
		},
		Routing: state.V2Routing{Path: "issue-pickup,feature-planning,feature-dev"},
	}

	got, err := V2RunRecordToExecutionHistoryRunRecord(rec, ExecutionHistoryMapperInput{Repo: "acme/mobile"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The killed stage must appear with its booked cost — not omitted as a
	// zero-cost stage (the pre-#296 failure mode).
	var featureDev *ExecutionHistoryStageMetric
	for i := range got.Stages {
		if got.Stages[i].StageID == "feature-dev" {
			featureDev = &got.Stages[i]
			break
		}
	}
	if featureDev == nil {
		t.Fatalf("feature-dev stage missing from telemetry metrics (regression: killed stage dropped)")
	}
	if featureDev.CostUsd == nil {
		t.Fatalf("feature-dev.CostUsd = nil, want the booked live estimate $9.1496 (killed stage under-reported)")
	}
	if *featureDev.CostUsd != 9.1496 {
		t.Errorf("feature-dev.CostUsd = %v, want 9.1496 (the real burn from the kill log)", *featureDev.CostUsd)
	}
	if featureDev.Success {
		t.Errorf("feature-dev.Success = %v, want false (stage was killed)", featureDev.Success)
	}

	// The run-level total is backfilled from the summed per-stage cost when no
	// run-level estimate is present (#4009), so the killed stage's burn now
	// shows up in the total instead of vanishing.
	wantTotal := 0.50 + 3.20 + 9.1496
	if got.TotalCostUsd == nil {
		t.Fatalf("TotalCostUsd = nil, want ~%.4f (summed per-stage incl. killed stage)", wantTotal)
	}
	if diff := *got.TotalCostUsd - wantTotal; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("TotalCostUsd = %v, want ~%.4f (killed stage burn must be included)", *got.TotalCostUsd, wantTotal)
	}
}

func TestV2RunRecordToExecutionHistoryRunRecord_OutcomeType(t *testing.T) {
	// Unset OutcomeType → nil on the wire.
	got, err := V2RunRecordToExecutionHistoryRunRecord(fullTestRecord(), ExecutionHistoryMapperInput{Repo: "nightgauge/nightgauge"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.OutcomeType != nil {
		t.Errorf("OutcomeType = %v, want nil when unset", got.OutcomeType)
	}

	// "blocked" (needs-human repo-config block) → pointer to "blocked".
	rec := fullTestRecord()
	rec.Outcome = "failed"
	rec.OutcomeType = "blocked"
	gotBlocked, err := V2RunRecordToExecutionHistoryRunRecord(rec, ExecutionHistoryMapperInput{Repo: "nightgauge/nightgauge"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBlocked.OutcomeType == nil || *gotBlocked.OutcomeType != "blocked" {
		t.Errorf("OutcomeType = %v, want pointer to \"blocked\"", gotBlocked.OutcomeType)
	}
}

func TestV2RunRecordToExecutionHistoryRunRecord_MarshalsAllKeysNoOmitempty(t *testing.T) {
	// A minimal record with every optional field absent must still marshal
	// every schema key with an explicit `null` — the platform's .strict() Zod
	// schema requires each nullable field's key present, not omitted.
	rec := state.V2RunRecord{
		IssueNumber: 1,
		StartedAt:   "2026-04-01T10:00:00Z",
		Outcome:     "complete",
	}

	got, err := V2RunRecordToExecutionHistoryRunRecord(rec, ExecutionHistoryMapperInput{Repo: "owner/repo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	wantKeys := []string{
		"schemaVersion", "issueNumber", "repo", "startedAt", "completedAt",
		"outcome", "terminalFailureKind", "predictedSize", "actualSize",
		"predictedModel", "actualModel", "complexityScore", "retries",
		"durationMs", "totalCostUsd", "stages", "agents", "routingPath",
	}
	for _, k := range wantKeys {
		if _, ok := m[k]; !ok {
			t.Errorf("marshaled JSON missing required key %q", k)
		}
	}
	if string(m["completedAt"]) != "null" {
		t.Errorf(`completedAt = %s, want "null"`, m["completedAt"])
	}
	if string(m["stages"]) != "[]" {
		t.Errorf(`stages = %s, want "[]" (non-null empty array)`, m["stages"])
	}
	if string(m["agents"]) != "[]" {
		t.Errorf(`agents = %s, want "[]" (non-null empty array)`, m["agents"])
	}
	if string(m["routingPath"]) != "null" {
		t.Errorf(`routingPath = %s, want "null"`, m["routingPath"])
	}
	// pipelineRunId is the ONE optional (not nullable) key — the platform
	// schema is `.uuid().optional()`, so a record without a run UUID must
	// OMIT the key entirely rather than send null (#261).
	if _, ok := m["pipelineRunId"]; ok {
		t.Errorf(`pipelineRunId key present for a record with no RunID — must be omitted, got %s`, m["pipelineRunId"])
	}
}

func TestV2RunRecordToExecutionHistoryRunRecord_PipelineRunID(t *testing.T) {
	base := state.V2RunRecord{
		IssueNumber: 1,
		StartedAt:   "2026-04-01T10:00:00Z",
		Outcome:     "complete",
	}

	valid := base
	valid.RunID = "6f883acb-5490-46d0-a8e8-1c985ba9dbfc"
	got, err := V2RunRecordToExecutionHistoryRunRecord(valid, ExecutionHistoryMapperInput{Repo: "owner/repo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.PipelineRunID != valid.RunID {
		t.Errorf("PipelineRunID = %q, want the record's run UUID %q", got.PipelineRunID, valid.RunID)
	}

	// A malformed run id must be dropped, not sent — `.uuid()` validation
	// would strict-reject the whole record.
	malformed := base
	malformed.RunID = "not-a-uuid"
	got, err = V2RunRecordToExecutionHistoryRunRecord(malformed, ExecutionHistoryMapperInput{Repo: "owner/repo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.PipelineRunID != "" {
		t.Errorf("PipelineRunID = %q for malformed RunID, want empty (key omitted)", got.PipelineRunID)
	}
}

func TestV2RunRecordToExecutionHistoryRunRecord_Errors(t *testing.T) {
	tests := []struct {
		name string
		rec  state.V2RunRecord
	}{
		{
			name: "unparseable started_at",
			rec:  state.V2RunRecord{IssueNumber: 1, StartedAt: "not-a-time", Outcome: "complete"},
		},
		{
			name: "unparseable completed_at",
			rec: state.V2RunRecord{
				IssueNumber: 1, StartedAt: "2026-04-01T10:00:00Z",
				CompletedAt: "not-a-time", Outcome: "complete",
			},
		},
		{
			name: "unmappable outcome (retired-sink vocabulary)",
			rec:  state.V2RunRecord{IssueNumber: 1, StartedAt: "2026-04-01T10:00:00Z", Outcome: "success"},
		},
		{
			name: "unmappable outcome (partial)",
			rec:  state.V2RunRecord{IssueNumber: 1, StartedAt: "2026-04-01T10:00:00Z", Outcome: "partial"},
		},
		{
			name: "empty outcome",
			rec:  state.V2RunRecord{IssueNumber: 1, StartedAt: "2026-04-01T10:00:00Z", Outcome: ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := V2RunRecordToExecutionHistoryRunRecord(tt.rec, ExecutionHistoryMapperInput{Repo: "owner/repo"})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestV2RunRecordToExecutionHistoryRunRecord_ValidOutcomesPassThrough(t *testing.T) {
	for _, outcome := range []string{"complete", "failed", "cancelled"} {
		rec := state.V2RunRecord{IssueNumber: 1, StartedAt: "2026-04-01T10:00:00Z", Outcome: outcome}
		got, err := V2RunRecordToExecutionHistoryRunRecord(rec, ExecutionHistoryMapperInput{Repo: "owner/repo"})
		if err != nil {
			t.Fatalf("outcome %q: unexpected error: %v", outcome, err)
		}
		if got.Outcome != outcome {
			t.Errorf("outcome %q: got %q", outcome, got.Outcome)
		}
	}
}

// TestV2RunRecordToExecutionHistoryRunRecord_IssueContext asserts the issue
// title/body/labels captured at pickup (#183) map onto the wire fields.
func TestV2RunRecordToExecutionHistoryRunRecord_IssueContext(t *testing.T) {
	rec := fullTestRecord()
	rec.Title = "Add rate limiting to the ingest endpoint"
	rec.Body = "## Problem\nNo per-account throttle exists yet."
	rec.Labels = []string{"type:feature", "component:api"}

	got, err := V2RunRecordToExecutionHistoryRunRecord(rec, ExecutionHistoryMapperInput{Repo: "nightgauge/nightgauge"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.IssueTitle == nil || *got.IssueTitle != rec.Title {
		t.Errorf("IssueTitle = %v, want %q", got.IssueTitle, rec.Title)
	}
	if got.IssueBody == nil || *got.IssueBody != rec.Body {
		t.Errorf("IssueBody = %v, want %q", got.IssueBody, rec.Body)
	}
	if want := []string{"type:feature", "component:api"}; !equalStrings(got.Labels, want) {
		t.Errorf("Labels = %v, want %v", got.Labels, want)
	}
}

// TestV2RunRecordToExecutionHistoryRunRecord_IssueContextOmittedWhenEmpty
// asserts that a record with no captured issue context drops the optional keys
// entirely (omitempty) rather than sending nulls — the graceful-degradation
// path for runs that pre-date capture (#183).
func TestV2RunRecordToExecutionHistoryRunRecord_IssueContextOmittedWhenEmpty(t *testing.T) {
	rec := state.V2RunRecord{IssueNumber: 1, StartedAt: "2026-04-01T10:00:00Z", Outcome: "complete"}
	got, err := V2RunRecordToExecutionHistoryRunRecord(rec, ExecutionHistoryMapperInput{Repo: "owner/repo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.IssueTitle != nil {
		t.Errorf("IssueTitle = %v, want nil", *got.IssueTitle)
	}
	if got.IssueBody != nil {
		t.Errorf("IssueBody = %v, want nil", *got.IssueBody)
	}
	if got.Labels != nil {
		t.Errorf("Labels = %v, want nil", got.Labels)
	}

	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"issueTitle", "issueBody", "labels"} {
		if bytesContains(data, key) {
			t.Errorf("wire JSON should omit %q key when unset: %s", key, data)
		}
	}
}

// TestV2RunRecordToExecutionHistoryRunRecord_IssueContextBounds asserts the
// wire values are clipped to the platform's telemetry bounds (#183): title 256,
// body 8192, labels count 50 — exceeding any would reject the whole record under
// the platform's `.strict()` validation.
func TestV2RunRecordToExecutionHistoryRunRecord_IssueContextBounds(t *testing.T) {
	rec := fullTestRecord()
	rec.Title = strings.Repeat("t", executionHistoryIssueTitleMax+50)
	rec.Body = strings.Repeat("b", executionHistoryIssueBodyMax+100)
	labels := make([]string, executionHistoryLabelsMax+10)
	for i := range labels {
		labels[i] = "label"
	}
	rec.Labels = labels

	got, err := V2RunRecordToExecutionHistoryRunRecord(rec, ExecutionHistoryMapperInput{Repo: "owner/repo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.IssueTitle == nil || len([]rune(*got.IssueTitle)) != executionHistoryIssueTitleMax {
		t.Errorf("IssueTitle len = %d, want %d", len([]rune(deref(got.IssueTitle))), executionHistoryIssueTitleMax)
	}
	if got.IssueBody == nil || len([]rune(*got.IssueBody)) != executionHistoryIssueBodyMax {
		t.Errorf("IssueBody len = %d, want %d", len([]rune(deref(got.IssueBody))), executionHistoryIssueBodyMax)
	}
	if len(got.Labels) != executionHistoryLabelsMax {
		t.Errorf("Labels count = %d, want %d", len(got.Labels), executionHistoryLabelsMax)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func bytesContains(b []byte, sub string) bool {
	return strings.Contains(string(b), sub)
}

func TestValidTelemetrySize(t *testing.T) {
	valid := "M"
	invalid := "medium"
	if got := validTelemetrySize(&valid); got == nil || *got != "M" {
		t.Errorf("validTelemetrySize(M) = %v, want pointer to M", got)
	}
	if got := validTelemetrySize(&invalid); got != nil {
		t.Errorf("validTelemetrySize(medium) = %v, want nil", got)
	}
	if got := validTelemetrySize(nil); got != nil {
		t.Errorf("validTelemetrySize(nil) = %v, want nil", got)
	}
}

func TestValidTelemetryComplexity(t *testing.T) {
	for _, score := range []int{1, 2, 3, 5, 8} {
		if got := validTelemetryComplexity(score); got == nil || *got != score {
			t.Errorf("validTelemetryComplexity(%d) = %v, want pointer to %d", score, got, score)
		}
	}
	for _, score := range []int{0, 4, 6, 7, 13, -1} {
		if got := validTelemetryComplexity(score); got != nil {
			t.Errorf("validTelemetryComplexity(%d) = %v, want nil", score, got)
		}
	}
}

func TestToTelemetryRoutingPath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"standard placeholder only", "standard", nil},
		{"comma separated", "issue-pickup,feature-dev", []string{"issue-pickup", "feature-dev"}},
		{"arrow separated with spaces", "issue-pickup > feature-dev", []string{"issue-pickup", "feature-dev"}},
		{"drops standard among real stages", "standard,issue-pickup", []string{"issue-pickup"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toTelemetryRoutingPath(tt.in)
			if !equalStrings(got, tt.want) {
				t.Errorf("toTelemetryRoutingPath(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestBuildExecutionHistoryStages_CapsAt32(t *testing.T) {
	stages := make(map[string]state.V2StageDetail, 40)
	for i := 0; i < 40; i++ {
		name := "extra-stage-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		stages[name] = state.V2StageDetail{Status: "complete"}
	}
	rec := state.V2RunRecord{Stages: stages}

	got, _ := buildExecutionHistoryStages(rec)
	if len(got) != executionHistoryStagesMax {
		t.Errorf("len(stages) = %d, want %d (capped)", len(got), executionHistoryStagesMax)
	}
}

func TestBuildExecutionHistoryStages_EmptyIsNonNilEmptySlice(t *testing.T) {
	got, cost := buildExecutionHistoryStages(state.V2RunRecord{})
	if got == nil {
		t.Error("buildExecutionHistoryStages returned nil slice, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
	if cost != 0 {
		t.Errorf("cost = %f, want 0", cost)
	}
}

func TestBuildExecutionHistoryStages_SuccessFalseOnFailedOrError(t *testing.T) {
	rec := state.V2RunRecord{
		Stages: map[string]state.V2StageDetail{
			"feature-dev": {Status: "failed"},
		},
	}
	got, _ := buildExecutionHistoryStages(rec)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].Success {
		t.Error("Success = true for a failed stage, want false")
	}
}

func TestBuildExecutionHistoryStages_SkippedCountsAsSuccess(t *testing.T) {
	rec := state.V2RunRecord{
		Stages: map[string]state.V2StageDetail{
			"pr-create": {Status: "skipped"},
		},
	}
	got, _ := buildExecutionHistoryStages(rec)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if !got[0].Success {
		t.Error("Success = false for a skipped stage, want true")
	}
}

func TestV2RunRecordToExecutionHistoryRunRecord_TotalCostBackfillFromStages(t *testing.T) {
	rec := state.V2RunRecord{
		IssueNumber: 1,
		StartedAt:   "2026-04-01T10:00:00Z",
		Outcome:     "complete",
		Stages: map[string]state.V2StageDetail{
			"issue-pickup": {Status: "complete"},
		},
		Tokens: state.V2Tokens{
			// EstimatedCostUSD absent (0) — must backfill from per-stage sum.
			PerStage: map[string]state.V2StageTokens{
				"issue-pickup": {CostUSD: 0.25},
			},
		},
	}

	got, err := V2RunRecordToExecutionHistoryRunRecord(rec, ExecutionHistoryMapperInput{Repo: "owner/repo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TotalCostUsd == nil || *got.TotalCostUsd != 0.25 {
		t.Errorf("TotalCostUsd = %v, want pointer to 0.25 (backfilled from stage sum)", got.TotalCostUsd)
	}
}

func TestV2RunRecordToExecutionHistoryRunRecord_RetriesFromAttemptsUntilSuccess(t *testing.T) {
	tests := []struct {
		attempts int
		want     int
	}{
		{0, 0},
		{1, 0},
		{2, 1},
		{5, 4},
	}
	for _, tt := range tests {
		rec := state.V2RunRecord{
			IssueNumber: 1, StartedAt: "2026-04-01T10:00:00Z", Outcome: "complete",
			AttemptsUntilSuccess: tt.attempts,
		}
		got, err := V2RunRecordToExecutionHistoryRunRecord(rec, ExecutionHistoryMapperInput{Repo: "owner/repo"})
		if err != nil {
			t.Fatalf("attempts=%d: unexpected error: %v", tt.attempts, err)
		}
		if got.Retries != tt.want {
			t.Errorf("attempts=%d: Retries = %d, want %d", tt.attempts, got.Retries, tt.want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
