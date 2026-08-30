package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
)

// --- V2 tests ---

func TestWriteV2_ProducesValidRecord(t *testing.T) {
	dir := t.TempDir()
	hw := NewHistoryWriter(dir)

	rs := NewRuntimeState("nightgauge/nightgauge", 2001, "item-v2", testRunID())
	rs.BeginStage(StageIssuePickup)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 5000, Output: 2000}, "", "")
	rs.BeginStage(StageFeaturePlanning)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 8000, Output: 3000}, "", "")

	input := V2RunInput{
		Title:           "Test V2 record",
		Branch:          "feat/2001",
		BaseBranch:      "main",
		Labels:          []string{"enhancement", "type:feature"},
		Size:            "medium",
		IssueType:       "feature",
		ComplexityScore: 5,
		RoutingPath:     "standard",
	}

	if err := hw.WriteV2(rs, true, "", input); err != nil {
		t.Fatalf("WriteV2: %v", err)
	}

	// Read the daily JSONL file
	today := time.Now().Format("2006-01-02") + ".jsonl"
	data, err := os.ReadFile(filepath.Join(dir, ".nightgauge", "pipeline", "history", today))
	if err != nil {
		t.Fatalf("read daily file: %v", err)
	}

	var record V2RunRecord
	if err := json.Unmarshal(data[:len(data)-1], &record); err != nil { // trim trailing newline
		t.Fatalf("unmarshal: %v", err)
	}

	if record.SchemaVersion != "2" {
		t.Errorf("schema_version = %q, want \"2\"", record.SchemaVersion)
	}
	if record.RecordType != "run" {
		t.Errorf("record_type = %q, want \"run\"", record.RecordType)
	}
	if record.IssueNumber != 2001 {
		t.Errorf("issue_number = %d, want 2001", record.IssueNumber)
	}
	// repo must be written from RuntimeState.Repo — the platform's strict V4
	// telemetry contract requires it, and without it the dashboard run list
	// (pipeline_runs) cannot be populated for multi-repo workspaces (#dashboard-0-runs).
	if record.Repo != "nightgauge/nightgauge" {
		t.Errorf("repo = %q, want \"nightgauge/nightgauge\"", record.Repo)
	}
	if record.Outcome != "complete" {
		t.Errorf("outcome = %q, want \"complete\"", record.Outcome)
	}
	if len(record.Stages) != 2 {
		t.Errorf("stages count = %d, want 2", len(record.Stages))
	}
	if record.Tokens.TotalInput != 13000 {
		t.Errorf("total_input = %d, want 13000", record.Tokens.TotalInput)
	}
	if record.Routing.ComplexityScore != 5 {
		t.Errorf("complexity_score = %d, want 5", record.Routing.ComplexityScore)
	}
}

func TestWriteV2_FailedPipeline(t *testing.T) {
	dir := t.TempDir()
	hw := NewHistoryWriter(dir)

	rs := NewRuntimeState("nightgauge/nightgauge", 2002, "item-fail", testRunID())
	rs.BeginStage(StageIssuePickup)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "", "")
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStage(1, tokens.TokenCounts{Input: 3000, Output: 1000}, "", "")
	// Through the production writer, in the production ORDER (#407): every
	// failure path books the stage's spend first and records the error second,
	// and completion is now the StageErrors clear site — so a raw map poke here
	// would no longer be exercising the sequence the pipeline actually emits.
	rs.SetStageError(StageFeatureDev, "compilation failed")

	input := V2RunInput{
		Title:      "Failing pipeline",
		Branch:     "feat/2002",
		BaseBranch: "main",
	}

	if err := hw.WriteV2(rs, false, "compilation failed", input); err != nil {
		t.Fatalf("WriteV2: %v", err)
	}

	today := time.Now().Format("2006-01-02") + ".jsonl"
	data, err := os.ReadFile(filepath.Join(dir, ".nightgauge", "pipeline", "history", today))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var record V2RunRecord
	if err := json.Unmarshal(data[:len(data)-1], &record); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if record.Outcome != "failed" {
		t.Errorf("outcome = %q, want \"failed\"", record.Outcome)
	}
	devStage, ok := record.Stages[string(StageFeatureDev)]
	if !ok {
		t.Fatal("feature-dev stage missing")
	}
	if devStage.Status != "failed" {
		t.Errorf("feature-dev status = %q, want \"failed\"", devStage.Status)
	}
	if devStage.Error != "compilation failed" {
		t.Errorf("feature-dev error = %q, want \"compilation failed\"", devStage.Error)
	}
}

func TestWriteV2_UpdatesIndex(t *testing.T) {
	dir := t.TempDir()
	hw := NewHistoryWriter(dir)

	for i := 0; i < 3; i++ {
		rs := NewRuntimeState("nightgauge/nightgauge", 3000+i, "item", testRunID())
		rs.BeginStage(StageIssuePickup)
		rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "", "")
		input := V2RunInput{Title: "Index test", Branch: "feat/test"}
		if err := hw.WriteV2(rs, true, "", input); err != nil {
			t.Fatalf("WriteV2 %d: %v", i, err)
		}
	}

	indexPath := filepath.Join(dir, ".nightgauge", "pipeline", "history", "index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}

	var idx V2Index
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatalf("unmarshal index: %v", err)
	}

	if idx.TotalRuns != 3 {
		t.Errorf("total_runs = %d, want 3", idx.TotalRuns)
	}
	// Most recent first
	if idx.Entries[0].IssueNumber != 3002 {
		t.Errorf("first entry = %d, want 3002", idx.Entries[0].IssueNumber)
	}
}

func TestWriteV2_SkippedStages(t *testing.T) {
	dir := t.TempDir()
	hw := NewHistoryWriter(dir)

	rs := NewRuntimeState("nightgauge/nightgauge", 4000, "item-skip", testRunID())
	rs.BeginStage(StageIssuePickup)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "", "")
	rs.SkippedStages = []string{string(StageFeaturePlanning)}

	input := V2RunInput{
		Title:      "Skipped stages test",
		Branch:     "feat/4000",
		BaseBranch: "main",
		SkipStages: []string{string(StageFeaturePlanning)},
	}

	if err := hw.WriteV2(rs, true, "", input); err != nil {
		t.Fatalf("WriteV2: %v", err)
	}

	today := time.Now().Format("2006-01-02") + ".jsonl"
	data, err := os.ReadFile(filepath.Join(dir, ".nightgauge", "pipeline", "history", today))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var record V2RunRecord
	if err := json.Unmarshal(data[:len(data)-1], &record); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	planStage, ok := record.Stages[string(StageFeaturePlanning)]
	if !ok {
		t.Fatal("feature-planning stage missing")
	}
	if planStage.Status != "skipped" {
		t.Errorf("feature-planning status = %q, want \"skipped\"", planStage.Status)
	}
	if len(record.Routing.SkipStages) != 1 {
		t.Errorf("skip_stages len = %d, want 1", len(record.Routing.SkipStages))
	}
}

// --- interim partial pipeline tests (Issue #2617) ---

// TestBuildV2Record_InterimPartialPipeline verifies that token/cost data is
// correctly computed from completed stages even when only a subset of pipeline
// stages have run (simulating an interim write after 2 of 6 stages complete).
func TestBuildV2Record_InterimPartialPipeline(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	input := V2RunInput{Title: "interim test", Branch: "feat/2617", BaseBranch: "main"}
	now := time.Now()

	// Only issue-pickup and feature-planning have completed (pipeline interrupted).
	rs := NewRuntimeState("nightgauge/nightgauge", 2617, "item-interim", testRunID())
	rs.BeginStage(StageIssuePickup)
	rs.CompleteStageWithCost(0, 10000, 5000, 2000, 0.05)
	rs.BeginStage(StageFeaturePlanning)
	rs.CompleteStageWithCost(0, 20000, 8000, 3000, 0.10)

	record := hw.BuildV2Record(rs, false, "pipeline interrupted", input, now)

	// Tokens should reflect the 2 completed stages, not zeros.
	if record.Tokens.TotalInput == 0 {
		t.Error("TotalInput should be non-zero for partial pipeline with completed stages")
	}
	wantInput := 10000 + 2000 + 20000 + 3000 // InputTokens combined (actual+cache) per stage
	if record.Tokens.TotalInput != wantInput {
		t.Errorf("TotalInput = %d, want %d", record.Tokens.TotalInput, wantInput)
	}
	if record.Tokens.TotalOutput == 0 {
		t.Error("TotalOutput should be non-zero for partial pipeline")
	}
	wantOutput := 5000 + 8000
	if record.Tokens.TotalOutput != wantOutput {
		t.Errorf("TotalOutput = %d, want %d", record.Tokens.TotalOutput, wantOutput)
	}
	if record.Tokens.TotalCacheRead == 0 {
		t.Error("TotalCacheRead should be non-zero when stages used cache")
	}
	wantCacheRead := 2000 + 3000
	if record.Tokens.TotalCacheRead != wantCacheRead {
		t.Errorf("TotalCacheRead = %d, want %d", record.Tokens.TotalCacheRead, wantCacheRead)
	}
	if record.Tokens.EstimatedCostUSD == 0 {
		t.Error("EstimatedCostUSD should be non-zero for partial pipeline")
	}
	wantCost := 0.05 + 0.10
	if record.Tokens.EstimatedCostUSD < wantCost-0.0001 || record.Tokens.EstimatedCostUSD > wantCost+0.0001 {
		t.Errorf("EstimatedCostUSD = %f, want %f", record.Tokens.EstimatedCostUSD, wantCost)
	}

	// Record should reflect failed outcome (pipeline was interrupted).
	if record.Outcome != "failed" {
		t.Errorf("Outcome = %q, want \"failed\"", record.Outcome)
	}
}

// TestBuildV2Record_OutcomeTypePropagates verifies the input's OutcomeType
// (e.g. "blocked" for a needs-human repo-config block) is copied onto the
// V2RunRecord so it reaches the platform wire, and stays empty for ordinary
// runs so omitempty drops it.
func TestBuildV2Record_OutcomeTypePropagates(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()
	rs := NewRuntimeState("nightgauge/nightgauge", 234, "item-blocked", testRunID())

	blocked := hw.BuildV2Record(rs, false, "required-check-config-mismatch:Sentry Smoke", V2RunInput{OutcomeType: "blocked"}, now)
	if blocked.OutcomeType != "blocked" {
		t.Errorf("OutcomeType = %q, want \"blocked\"", blocked.OutcomeType)
	}

	plain := hw.BuildV2Record(rs, false, "generic failure", V2RunInput{}, now)
	if plain.OutcomeType != "" {
		t.Errorf("OutcomeType = %q, want empty for a run with no outcome type", plain.OutcomeType)
	}
}

// TestBuildV2Record_IssueBody verifies the issue body captured at pickup (#183)
// is threaded onto the run record and clipped to the wire bound.
func TestBuildV2Record_IssueBody(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()
	rs := NewRuntimeState("nightgauge/nightgauge", 183, "item-body", testRunID())

	// Ordinary body flows through verbatim.
	rec := hw.BuildV2Record(rs, true, "", V2RunInput{Body: "## Problem\nNeeds context."}, now)
	if rec.Body != "## Problem\nNeeds context." {
		t.Errorf("Body = %q, want the captured issue body", rec.Body)
	}

	// A run with no captured body leaves it empty (omitempty drops it on disk).
	empty := hw.BuildV2Record(rs, true, "", V2RunInput{}, now)
	if empty.Body != "" {
		t.Errorf("Body = %q, want empty for a run with no captured body", empty.Body)
	}

	// An over-long body is clipped to the wire bound as a safety net.
	long := hw.BuildV2Record(rs, true, "", V2RunInput{Body: strings.Repeat("x", v2RunBodyMax+500)}, now)
	if got := len([]rune(long.Body)); got != v2RunBodyMax {
		t.Errorf("clipped Body len = %d, want %d", got, v2RunBodyMax)
	}
}

// TestBuildV2Record_ToolCallsSurviveIntoRecord verifies the run's aggregated
// tool-call log threads verbatim from V2RunInput.ToolCalls onto
// V2RunRecord.ToolCalls, and that RunID/Repo are populated on the same
// record (Issue #144 AC #3/#4).
func TestBuildV2Record_ToolCallsSurviveIntoRecord(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()
	// A pinned canonical identity rather than testRunID(): the record assertions
	// below name it. It is the CONSTRUCTOR argument — RunID has no setter.
	rs := NewRuntimeState("nightgauge/nightgauge", 144, "item-toolcalls", "01966b4c-0000-7000-a000-000000000144")

	toolCalls := []diagnostics.ToolCallRecord{
		{Tool: "Read", Target: "internal/state/history.go", Stage: "feature-dev", Timestamp: "2026-07-29T00:00:00Z"},
		{Tool: "Edit", Target: "internal/state/history.go", Stage: "feature-dev", DurationMs: 42, Result: "ok"},
		{Tool: "Bash", Target: "go build ./...", Stage: "feature-validate", Error: "exit 1"},
	}

	rec := hw.BuildV2Record(rs, true, "", V2RunInput{ToolCalls: toolCalls}, now)

	if len(rec.ToolCalls) != len(toolCalls) {
		t.Fatalf("ToolCalls len = %d, want %d", len(rec.ToolCalls), len(toolCalls))
	}
	for i, want := range toolCalls {
		if rec.ToolCalls[i] != want {
			t.Errorf("ToolCalls[%d] = %+v, want %+v", i, rec.ToolCalls[i], want)
		}
	}
	if rec.RunID == "" {
		t.Error("RunID should be non-empty")
	}
	if rec.Repo == "" {
		t.Error("Repo should be non-empty")
	}
}

// TestBuildV2Record_ZeroStagesNoTokens verifies that a record with no completed
// stages correctly reports zero tokens (not a crash).
func TestBuildV2Record_ZeroStagesNoTokens(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	input := V2RunInput{Title: "empty", Branch: "feat/test", BaseBranch: "main"}
	now := time.Now()

	rs := NewRuntimeState("nightgauge/nightgauge", 9999, "item-empty", testRunID())
	// No stages completed.
	record := hw.BuildV2Record(rs, false, "preflight failed", input, now)

	if record.Tokens.TotalInput != 0 {
		t.Errorf("TotalInput = %d, want 0 for empty pipeline", record.Tokens.TotalInput)
	}
	if record.Tokens.EstimatedCostUSD != 0 {
		t.Errorf("EstimatedCostUSD = %f, want 0 for empty pipeline", record.Tokens.EstimatedCostUSD)
	}
}

func TestBuildV2Record_ActualSizeDistinguishesMeasuredZeroFromAbsent(t *testing.T) {
	root := t.TempDir()
	hw := NewHistoryWriter(root)
	now := time.Now()

	absent := NewRuntimeState("nightgauge/nightgauge", 368, "item-368", testRunID())
	absentRecord := hw.BuildV2Record(absent, false, "pre-pr-create failure", V2RunInput{}, now)
	if absentRecord.ActualLinesChanged != nil {
		t.Fatalf("pre-pr-create record ActualLinesChanged = %v, want absent", absentRecord.ActualLinesChanged)
	}
	if absentRecord.OutcomePrediction != nil {
		t.Fatalf("pre-pr-create record OutcomePrediction = %+v, want absent", absentRecord.OutcomePrediction)
	}

	measured := NewRuntimeState("nightgauge/nightgauge", 369, "item-369", testRunID())
	measured.SetActualLinesChanged(0)
	measuredRecord := hw.BuildV2Record(measured, true, "", V2RunInput{}, now)
	if measuredRecord.ActualLinesChanged == nil || *measuredRecord.ActualLinesChanged != 0 {
		t.Fatalf("measured ActualLinesChanged = %v, want pointer to zero", measuredRecord.ActualLinesChanged)
	}
	if measuredRecord.OutcomePrediction == nil || measuredRecord.OutcomePrediction.ActualSize != "small" {
		t.Fatalf("measured OutcomePrediction = %+v, want actual_size small", measuredRecord.OutcomePrediction)
	}
}

// TestBuildV2Record_TotalCacheReadPopulated verifies that TotalCacheRead is
// populated from stage data (previously always 0).
func TestBuildV2Record_TotalCacheReadPopulated(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	input := V2RunInput{Title: "cache read test", Branch: "feat/test", BaseBranch: "main"}
	now := time.Now()

	rs := NewRuntimeState("nightgauge/nightgauge", 2617, "item-cache", testRunID())
	rs.BeginStage(StageIssuePickup)
	rs.CompleteStageWithCost(0, 5000, 2000, 1500, 0.03) // 1500 cache read tokens
	rs.BeginStage(StageFeaturePlanning)
	rs.CompleteStageWithCost(0, 8000, 3000, 2500, 0.06) // 2500 cache read tokens

	record := hw.BuildV2Record(rs, true, "", input, now)

	wantCacheRead := 1500 + 2500
	if record.Tokens.TotalCacheRead != wantCacheRead {
		t.Errorf("TotalCacheRead = %d, want %d", record.Tokens.TotalCacheRead, wantCacheRead)
	}
}

// --- cache hit rate tests (Issue #2459) ---

func TestBuildV2Record_CacheHitRate(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	input := V2RunInput{Title: "cache test", Branch: "feat/2459", BaseBranch: "main"}
	now := time.Now()

	t.Run("typical: input + cache_read", func(t *testing.T) {
		// 100 input + 50 cacheRead → sr.InputTokens=150, sr.CacheRead=50
		// rate = 50/150 ≈ 0.333
		rs := NewRuntimeState("nightgauge/nightgauge", 2459, "item-1", testRunID())
		rs.BeginStage(StageIssuePickup)
		rs.CompleteStageWithCost(0, 100, 200, 50, 0.01)

		record := hw.BuildV2Record(rs, true, "", input, now)
		st, ok := record.Tokens.PerStage[string(StageIssuePickup)]
		if !ok {
			t.Fatal("per-stage tokens missing for issue-pickup")
		}
		if st.CacheHitRate == nil {
			t.Fatal("CacheHitRate should not be nil when tokens > 0")
		}
		want := 50.0 / 150.0
		if got := *st.CacheHitRate; got < want-0.001 || got > want+0.001 {
			t.Errorf("CacheHitRate = %f, want %f", got, want)
		}
		// Input field should be actual non-cached input (150 - 50 = 100)
		if st.Input != 100 {
			t.Errorf("Input = %d, want 100 (non-cached input)", st.Input)
		}
		if st.CacheRead != 50 {
			t.Errorf("CacheRead = %d, want 50", st.CacheRead)
		}
	})

	t.Run("zero denominator: no tokens", func(t *testing.T) {
		rs := NewRuntimeState("nightgauge/nightgauge", 2459, "item-2", testRunID())
		rs.BeginStage(StageIssuePickup)
		rs.CompleteStageWithCost(0, 0, 0, 0, 0.0)

		record := hw.BuildV2Record(rs, true, "", input, now)
		st, ok := record.Tokens.PerStage[string(StageIssuePickup)]
		if !ok {
			t.Fatal("per-stage tokens missing")
		}
		if st.CacheHitRate != nil {
			t.Errorf("CacheHitRate should be nil when no tokens, got %f", *st.CacheHitRate)
		}
	})

	t.Run("full cache hit: only cache_read, no fresh input", func(t *testing.T) {
		// 0 input + 50 cacheRead → rate = 50/50 = 1.0
		rs := NewRuntimeState("nightgauge/nightgauge", 2459, "item-3", testRunID())
		rs.BeginStage(StageIssuePickup)
		rs.CompleteStageWithCost(0, 0, 200, 50, 0.01)

		record := hw.BuildV2Record(rs, true, "", input, now)
		st, ok := record.Tokens.PerStage[string(StageIssuePickup)]
		if !ok {
			t.Fatal("per-stage tokens missing for issue-pickup")
		}
		if st.CacheHitRate == nil {
			t.Fatal("CacheHitRate should not be nil")
		}
		if got := *st.CacheHitRate; got < 0.999 || got > 1.001 {
			t.Errorf("CacheHitRate = %f, want 1.0 (100%% cache hit)", got)
		}
	})

	t.Run("zero cache: only fresh input", func(t *testing.T) {
		// 100 input + 0 cacheRead → rate = 0/100 = 0.0
		rs := NewRuntimeState("nightgauge/nightgauge", 2459, "item-4", testRunID())
		rs.BeginStage(StageIssuePickup)
		rs.CompleteStageWithCost(0, 100, 200, 0, 0.01)

		record := hw.BuildV2Record(rs, true, "", input, now)
		st, ok := record.Tokens.PerStage[string(StageIssuePickup)]
		if !ok {
			t.Fatal("per-stage tokens missing for issue-pickup")
		}
		if st.CacheHitRate == nil {
			t.Fatal("CacheHitRate should not be nil when input > 0")
		}
		if got := *st.CacheHitRate; got > 0.001 {
			t.Errorf("CacheHitRate = %f, want 0.0 (no cache)", got)
		}
	})

	t.Run("multiple stages compute independently", func(t *testing.T) {
		rs := NewRuntimeState("nightgauge/nightgauge", 2459, "item-5", testRunID())

		rs.BeginStage(StageIssuePickup)
		rs.CompleteStageWithCost(0, 100, 50, 50, 0.01) // ~33% hit (50 cache_read / 150 combined input)

		rs.BeginStage(StageFeaturePlanning)
		rs.CompleteStageWithCost(0, 200, 80, 0, 0.02) // 0% hit

		record := hw.BuildV2Record(rs, true, "", input, now)

		pickup := record.Tokens.PerStage[string(StageIssuePickup)]
		if pickup.CacheHitRate == nil {
			t.Fatal("issue-pickup CacheHitRate should not be nil")
		}
		wantPickup := 50.0 / 150.0
		if got := *pickup.CacheHitRate; got < wantPickup-0.001 || got > wantPickup+0.001 {
			t.Errorf("issue-pickup CacheHitRate = %f, want %f", got, wantPickup)
		}

		planning := record.Tokens.PerStage[string(StageFeaturePlanning)]
		if planning.CacheHitRate == nil {
			t.Fatal("feature-planning CacheHitRate should not be nil")
		}
		if got := *planning.CacheHitRate; got > 0.001 {
			t.Errorf("feature-planning CacheHitRate = %f, want 0.0", got)
		}
	})
}

// TestWriteV2_PerStagePerformanceMode verifies that the per-stage
// performance_mode field captured via RecordStageMode round-trips through
// BuildV2Record / json.Marshal / json.Unmarshal (Issue #3215).
//
// Three stages exercise three branches:
//   - one with mode "efficiency"
//   - one with mode "maximum"
//   - one with no mode recorded — the omitempty tag MUST keep the field absent
//     so old readers see the same on-the-wire shape they did before #3215.
func TestWriteV2_PerStagePerformanceMode(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 3215, "item-mode", testRunID())

	rs.BeginStage(StageIssuePickup)
	rs.RecordStageMode(StageIssuePickup, "efficiency")
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "", "")

	rs.BeginStage(StageFeaturePlanning)
	// Deliberately no RecordStageMode here.
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1500, Output: 600}, "", "")

	rs.BeginStage(StageFeatureDev)
	rs.RecordStageMode(StageFeatureDev, "maximum")
	rs.CompleteStage(0, tokens.TokenCounts{Input: 2000, Output: 700}, "", "")

	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()
	record := hw.BuildV2Record(rs, true, "", V2RunInput{
		Title:      "perf-mode test",
		Branch:     "feat/3215",
		BaseBranch: "main",
	}, now)

	// In-memory shape.
	if got := record.Stages[string(StageIssuePickup)].PerformanceMode; got != "efficiency" {
		t.Errorf("issue-pickup PerformanceMode = %q, want %q", got, "efficiency")
	}
	if got := record.Stages[string(StageFeaturePlanning)].PerformanceMode; got != "" {
		t.Errorf("feature-planning PerformanceMode = %q, want empty", got)
	}
	if got := record.Stages[string(StageFeatureDev)].PerformanceMode; got != "maximum" {
		t.Errorf("feature-dev PerformanceMode = %q, want %q", got, "maximum")
	}

	// Wire-format shape — verify omitempty omits the key for unknown stages.
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw struct {
		Stages map[string]map[string]any `json:"stages"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := raw.Stages[string(StageFeaturePlanning)]["performance_mode"]; present {
		t.Errorf("feature-planning emitted performance_mode key — omitempty must drop empty values")
	}
	if got, _ := raw.Stages[string(StageIssuePickup)]["performance_mode"].(string); got != "efficiency" {
		t.Errorf("issue-pickup wire performance_mode = %q, want %q", got, "efficiency")
	}
	if got, _ := raw.Stages[string(StageFeatureDev)]["performance_mode"].(string); got != "maximum" {
		t.Errorf("feature-dev wire performance_mode = %q, want %q", got, "maximum")
	}
}

// TestRecordStageMode_IgnoresEmpty verifies the no-op contract for empty
// modes — keeps the on-the-wire shape clean when ResolvePerformanceMode
// fails to read a value.
func TestRecordStageMode_IgnoresEmpty(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 3215, "item-empty", testRunID())
	rs.BeginStage(StageIssuePickup)
	rs.RecordStageMode(StageIssuePickup, "")
	if got := rs.StageMode(StageIssuePickup); got != "" {
		t.Errorf("StageMode after empty record = %q, want empty", got)
	}
	if rs.StageModes != nil {
		t.Errorf("StageModes should remain nil after empty RecordStageMode call, got %#v", rs.StageModes)
	}
}

// TestWriteV2_PerStageAdapter verifies that the per-stage `adapter` token
// field captured via RecordStageAdapter round-trips through BuildV2Record /
// json.Marshal / json.Unmarshal (Issue #3224). Mirrors the existing
// TestWriteV2_PerStagePerformanceMode coverage:
//   - one stage with adapter "claude" (recorded explicitly)
//   - one stage with no recorded adapter — falls back to V2RunInput.DefaultAdapter
//   - one stage with no recorded adapter and no default — omitempty drops the key
func TestWriteV2_PerStageAdapter(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 3224, "item-adapter", testRunID())

	rs.BeginStage(StageIssuePickup)
	rs.RecordStageAdapter(StageIssuePickup, "claude")
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "", "")

	rs.BeginStage(StageFeaturePlanning)
	// No RecordStageAdapter — should fall back to V2RunInput.DefaultAdapter.
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1500, Output: 600}, "", "")

	rs.BeginStage(StageFeatureDev)
	rs.RecordStageAdapter(StageFeatureDev, "gemini")
	rs.CompleteStage(0, tokens.TokenCounts{Input: 2000, Output: 700}, "", "")

	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()
	record := hw.BuildV2Record(rs, true, "", V2RunInput{
		Title:          "adapter test",
		Branch:         "feat/3224",
		BaseBranch:     "main",
		DefaultAdapter: "codex",
	}, now)

	// In-memory shape — explicit recordings win, missing stages fall back to default.
	if got := record.Tokens.PerStage[string(StageIssuePickup)].Adapter; got != "claude" {
		t.Errorf("issue-pickup Adapter = %q, want %q", got, "claude")
	}
	if got := record.Tokens.PerStage[string(StageFeaturePlanning)].Adapter; got != "codex" {
		t.Errorf("feature-planning Adapter = %q, want %q (DefaultAdapter fallback)", got, "codex")
	}
	if got := record.Tokens.PerStage[string(StageFeatureDev)].Adapter; got != "gemini" {
		t.Errorf("feature-dev Adapter = %q, want %q", got, "gemini")
	}

	// Wire-format shape — recorded values appear, omitempty drops empty values.
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw struct {
		Tokens struct {
			PerStage map[string]map[string]any `json:"per_stage"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := raw.Tokens.PerStage[string(StageIssuePickup)]["adapter"].(string); got != "claude" {
		t.Errorf("issue-pickup wire adapter = %q, want %q", got, "claude")
	}
	if got, _ := raw.Tokens.PerStage[string(StageFeaturePlanning)]["adapter"].(string); got != "codex" {
		t.Errorf("feature-planning wire adapter = %q, want %q", got, "codex")
	}
	if got, _ := raw.Tokens.PerStage[string(StageFeatureDev)]["adapter"].(string); got != "gemini" {
		t.Errorf("feature-dev wire adapter = %q, want %q", got, "gemini")
	}

	// Back-compat: when neither recorded nor default adapter is supplied, the
	// adapter key MUST be absent on the wire so existing dashboards keep
	// treating absence as adapter-unknown.
	rsNoAdapter := NewRuntimeState("nightgauge/nightgauge", 3224, "item-no-adapter", testRunID())
	rsNoAdapter.BeginStage(StageIssuePickup)
	rsNoAdapter.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "", "")

	recordNoAdapter := hw.BuildV2Record(rsNoAdapter, true, "", V2RunInput{
		Title:      "no adapter",
		Branch:     "feat/3224",
		BaseBranch: "main",
	}, now)

	if got := recordNoAdapter.Tokens.PerStage[string(StageIssuePickup)].Adapter; got != "" {
		t.Errorf("Adapter without record or default = %q, want empty", got)
	}
	noAdapterData, err := json.Marshal(recordNoAdapter)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var rawNoAdapter struct {
		Tokens struct {
			PerStage map[string]map[string]any `json:"per_stage"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(noAdapterData, &rawNoAdapter); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := rawNoAdapter.Tokens.PerStage[string(StageIssuePickup)]["adapter"]; present {
		t.Errorf("issue-pickup emitted adapter key — omitempty must drop empty values")
	}
}

// TestBuildV2Record_ModelSelectionEffortEnvelope pins the field-by-field
// wiring BuildV2Record performs (internal/state/history.go, the
// V2ModelSelect construction inside the CompletedStages loop) from the six
// per-stage effort/thinking maps a scheduler dispatch populates via
// RecordStageEffort/RecordStageThinking/RecordStageServedEffort/
// RecordStageServedThinking/RecordStageModelSelectionMode into
// V2ModelSelect.Effort/Thinking/ServedEffort/ServedThinking/Mode (#606
// served-envelope attribution; #612 gap: "the wiring itself has no test").
// Requested and served values are deliberately distinct in this test so a
// swapped assignment (e.g. Effort fed from StageServedEfforts, or
// ServedEffort fed from StageEfforts) fails loudly instead of passing on
// coincidentally-equal fixture data.
func TestBuildV2Record_ModelSelectionEffortEnvelope(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 606, "item-606", testRunID())

	rs.BeginStage(StageFeatureDev)
	rs.RecordStageModel(StageFeatureDev, "grok-4.5")
	rs.RecordStageAdapter(StageFeatureDev, "grok")
	rs.RecordStageServedModel(StageFeatureDev, "grok-4.5-served")
	rs.RecordStageEffort(StageFeatureDev, "high")
	rs.RecordStageServedEffort(StageFeatureDev, "medium")
	rs.RecordStageThinking(StageFeatureDev, "on")
	rs.RecordStageServedThinking(StageFeatureDev, "off")
	rs.RecordStageModelSelectionMode(StageFeatureDev, "automatic")
	rs.CompleteStage(0, tokens.TokenCounts{Input: 100, Output: 50}, "", "")

	hw := NewHistoryWriter(t.TempDir())
	record := hw.BuildV2Record(rs, true, "", V2RunInput{
		Title:      "606 effort envelope",
		Branch:     "feat/606",
		BaseBranch: "main",
	}, time.Now())

	sel := record.Stages[string(StageFeatureDev)].ModelSelection
	if sel == nil {
		t.Fatalf("ModelSelection is nil, want populated (StageModels was set)")
	}
	if sel.Effort != "high" {
		t.Errorf("Effort = %q, want %q (requested rung, not served)", sel.Effort, "high")
	}
	if sel.ServedEffort != "medium" {
		t.Errorf("ServedEffort = %q, want %q (served rung, not requested)", sel.ServedEffort, "medium")
	}
	if sel.Thinking != "on" {
		t.Errorf("Thinking = %q, want %q", sel.Thinking, "on")
	}
	if sel.ServedThinking != "off" {
		t.Errorf("ServedThinking = %q, want %q", sel.ServedThinking, "off")
	}
	if sel.Mode != "automatic" {
		t.Errorf("Mode = %q, want %q", sel.Mode, "automatic")
	}
	if sel.Adapter != "grok" {
		t.Errorf("Adapter = %q, want %q", sel.Adapter, "grok")
	}
	if sel.ServedModel != "grok-4.5-served" {
		t.Errorf("ServedModel = %q, want %q", sel.ServedModel, "grok-4.5-served")
	}

	// Wire-format shape — every field above round-trips under its documented
	// JSON key, and the served/requested pairs stay distinguishable on the
	// wire exactly as they are in memory.
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw struct {
		Stages map[string]struct {
			ModelSelection map[string]any `json:"model_selection"`
		} `json:"stages"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wireSel := raw.Stages[string(StageFeatureDev)].ModelSelection
	if got, _ := wireSel["effort"].(string); got != "high" {
		t.Errorf("wire effort = %q, want %q", got, "high")
	}
	if got, _ := wireSel["served_effort"].(string); got != "medium" {
		t.Errorf("wire served_effort = %q, want %q", got, "medium")
	}
	if got, _ := wireSel["thinking"].(string); got != "on" {
		t.Errorf("wire thinking = %q, want %q", got, "on")
	}
	if got, _ := wireSel["served_thinking"].(string); got != "off" {
		t.Errorf("wire served_thinking = %q, want %q", got, "off")
	}
}

// TestBuildV2Record_ServedEffortOpenVocabulary pins the PRODUCER half of the
// served-envelope vocabulary contract (#612's "minor asymmetries" gap): Go
// records and emits whatever rung the executor actually reported, verbatim,
// with no validation against EFFORT_LEVELS. That is deliberate — ServedEffort
// is first-hand evidence of what the last-mile translation dispatched, and
// normalizing or dropping an unrecognized value would turn observed evidence
// into a guess, the exact failure ServedModel's "empty means
// honestly-unreported, never a guess" contract exists to prevent.
//
// It is also the input every consumer schema must tolerate. The TypeScript
// reader currently types served_effort as a strict z.enum while served_model
// is a free z.string(), so a value like the one below fails the parse of the
// ENTIRE record rather than just that field. Reconciling the two readers is
// tracked separately; this test fixes the producer side of the contract so
// that work has something to reconcile against, and fails if Go ever starts
// silently filtering served efforts to the canonical ladder.
func TestBuildV2Record_ServedEffortOpenVocabulary(t *testing.T) {
	const offLadder = "ultra" // deliberately not an EFFORT_LEVELS rung

	rs := NewRuntimeState("nightgauge/nightgauge", 612, "item-612", testRunID())
	rs.BeginStage(StageFeatureDev)
	rs.RecordStageModel(StageFeatureDev, "grok-4.5")
	rs.RecordStageEffort(StageFeatureDev, "high")
	rs.RecordStageServedEffort(StageFeatureDev, offLadder)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 10, Output: 5}, "", "")

	hw := NewHistoryWriter(t.TempDir())
	record := hw.BuildV2Record(rs, true, "", V2RunInput{
		Title:      "612 served-effort vocabulary",
		Branch:     "chore/612",
		BaseBranch: "main",
	}, time.Now())

	sel := record.Stages[string(StageFeatureDev)].ModelSelection
	if sel == nil {
		t.Fatalf("ModelSelection is nil, want populated (StageModels was set)")
	}
	if sel.ServedEffort != offLadder {
		t.Errorf("ServedEffort = %q, want %q recorded verbatim (no ladder filtering)", sel.ServedEffort, offLadder)
	}
	if sel.Effort != "high" {
		t.Errorf("Effort = %q, want %q — an off-ladder served value must not disturb the requested rung", sel.Effort, "high")
	}

	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw struct {
		Stages map[string]struct {
			ModelSelection map[string]any `json:"model_selection"`
		} `json:"stages"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wireSel := raw.Stages[string(StageFeatureDev)].ModelSelection
	if got, _ := wireSel["served_effort"].(string); got != offLadder {
		t.Errorf("wire served_effort = %q, want %q — the off-ladder value must reach the wire, not be dropped by omitempty after a silent reset", got, offLadder)
	}
}

// TestRecordStageAdapter_IgnoresEmpty verifies the no-op contract for empty
// adapter strings — preserves the omitempty guarantee when the resolver
// fails to produce a value.
func TestRecordStageAdapter_IgnoresEmpty(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 3224, "item-empty-adapter", testRunID())
	rs.BeginStage(StageIssuePickup)
	rs.RecordStageAdapter(StageIssuePickup, "")
	if got := rs.StageAdapter(StageIssuePickup); got != "" {
		t.Errorf("StageAdapter after empty record = %q, want empty", got)
	}
	if rs.StageAdapters != nil {
		t.Errorf("StageAdapters should remain nil after empty RecordStageAdapter call, got %#v", rs.StageAdapters)
	}
}

// TestRunLevelPerformanceMode verifies that BuildV2Record derives the dominant
// run-level performance_mode from per-stage modes, and that updateIndex writes
// it to index.json (Issue #3218 fix).
func TestRunLevelPerformanceMode(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 3218, "item-mode-fix", testRunID())

	// Two elevated stages, one with no mode.
	rs.BeginStage(StageIssuePickup)
	rs.RecordStageMode(StageIssuePickup, "elevated")
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "", "")

	rs.BeginStage(StageFeaturePlanning)
	// No mode recorded — omitempty must keep the field absent.
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1500, Output: 600}, "", "")

	rs.BeginStage(StageFeatureDev)
	rs.RecordStageMode(StageFeatureDev, "elevated")
	rs.CompleteStage(0, tokens.TokenCounts{Input: 2000, Output: 700}, "", "")

	dir := t.TempDir()
	hw := NewHistoryWriter(dir)
	now := time.Now()
	record := hw.BuildV2Record(rs, true, "", V2RunInput{
		Title:      "run-level mode test",
		Branch:     "fix/3218",
		BaseBranch: "main",
	}, now)

	// Run-level field should reflect the dominant mode.
	if got := record.PerformanceMode; got != "elevated" {
		t.Errorf("run-level PerformanceMode = %q, want %q", got, "elevated")
	}

	// Index entry must carry the same value.
	if err := hw.WriteRecord(record); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".nightgauge", "pipeline", "history", "index.json"))
	if err != nil {
		t.Fatalf("read index.json: %v", err)
	}
	var idx V2Index
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatalf("unmarshal index: %v", err)
	}
	if len(idx.Entries) == 0 {
		t.Fatal("index has no entries")
	}
	if got := idx.Entries[0].PerformanceMode; got != "elevated" {
		t.Errorf("index entry PerformanceMode = %q, want %q", got, "elevated")
	}
}

// TestBuildV2Record_TerminatingStageTokensSurvive verifies that a stage which
// crashed before reaching CompleteStage/CompleteStageWithCost still gets a
// matching tokens.per_stage entry (and is included in estimated_cost_usd),
// sourced from RuntimeState.TerminatingStageTokens (Issue #146).
func TestBuildV2Record_TerminatingStageTokensSurvive(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()
	rs := NewRuntimeState("nightgauge/nightgauge", 146, "item-terminating", testRunID())

	// Simulate a normally-completed first stage.
	rs.BeginStage(StageIssuePickup)
	rs.CompleteStageWithCost(0, 5000, 2000, 1500, 0.03)

	// Simulate the terminating stage: it never reached CompleteStage (crash
	// before that call), but its ground-truth tokens were captured via the
	// same path writeStageExitRecord uses.
	rs.BeginStage(StageFeatureDev)
	rs.SetStageError(StageFeatureDev, "runaway_progress: stage killed")
	rs.RecordTerminatingStageTokens(StageFeatureDev, 20000, 8000, 6000, 0.45)
	rs.Stage = StageFeatureDev

	record := hw.BuildV2Record(rs, false, "runaway_progress: stage killed", V2RunInput{}, now)

	stageName := string(StageFeatureDev)
	detail, ok := record.Stages[stageName]
	if !ok || detail.Status != "failed" {
		t.Fatalf("Stages[%q] = %+v, ok=%v; want failed status", stageName, detail, ok)
	}

	tok, ok := record.Tokens.PerStage[stageName]
	if !ok {
		t.Fatalf("Tokens.PerStage missing entry for %q; want synthesized entry from TerminatingStageTokens", stageName)
	}
	if tok.CostUSD != 0.45 {
		t.Errorf("PerStage[%q].CostUSD = %f, want 0.45", stageName, tok.CostUSD)
	}
	wantInput := 20000 - 6000
	if tok.Input != wantInput {
		t.Errorf("PerStage[%q].Input = %d, want %d", stageName, tok.Input, wantInput)
	}
	if tok.Output != 8000 {
		t.Errorf("PerStage[%q].Output = %d, want 8000", stageName, tok.Output)
	}
	if tok.CacheRead != 6000 {
		t.Errorf("PerStage[%q].CacheRead = %d, want 6000", stageName, tok.CacheRead)
	}

	wantCost := 0.03 + 0.45
	if record.Tokens.EstimatedCostUSD < wantCost-0.0001 || record.Tokens.EstimatedCostUSD > wantCost+0.0001 {
		t.Errorf("EstimatedCostUSD = %f, want ~%f (includes terminating stage's cost)", record.Tokens.EstimatedCostUSD, wantCost)
	}
}

// TestBuildV2Record_BacktrackedStageAccumulates pins the sharp edge created by
// sourcing run totals from the per-stage map: that map is keyed by stage name,
// but one stage legitimately runs twice when the retry/backtrack engine rewinds
// to it. If the map is assigned rather than accumulated, the earlier attempt's
// spend vanishes from BOTH tokens.per_stage and estimated_cost_usd — biasing
// calibration low on precisely the runs that cost the most, which is the defect
// Issue #146 exists to remove.
//
// #556 MOVED THE SOURCE THIS TEST GUARDS. CompletedStages now means "the
// stage's most recent attempt completed", so the earlier attempt is no longer
// in it — it is in SupersededStages, and BuildV2Record reads
// AllStageAttempts(). This test is therefore also the red bar for a summer that
// forgets the superseded half: it fails with exactly the #146 numbers if
// AllStageAttempts() stops returning them.
func TestBuildV2Record_BacktrackedStageAccumulates(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()
	rs := NewRuntimeState("nightgauge/nightgauge", 146, "item-backtrack", testRunID())

	// feature-dev runs, feature-validate rejects the work, and the backtrack
	// engine rewinds to feature-dev — which then runs a second time.
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStageWithCost(0, 10000, 4000, 3000, 0.20)
	rs.BeginStage(StageFeatureValidate)
	rs.CompleteStageWithCost(0, 2000, 500, 1000, 0.02)
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStageWithCost(0, 12000, 5000, 4000, 0.25)

	record := hw.BuildV2Record(rs, true, "", V2RunInput{}, now)

	devName := string(StageFeatureDev)
	tok, ok := record.Tokens.PerStage[devName]
	if !ok {
		t.Fatalf("Tokens.PerStage missing entry for %q", devName)
	}

	// Both attempts, summed — not just the last. CompleteStageWithCost takes
	// NON-cached input and folds cache_read in itself, so the per-stage Input
	// here is simply the two non-cached figures added together.
	if wantInput := 10000 + 12000; tok.Input != wantInput {
		t.Errorf("PerStage[%q].Input = %d, want %d (both attempts)", devName, tok.Input, wantInput)
	}
	if tok.Output != 4000+5000 {
		t.Errorf("PerStage[%q].Output = %d, want %d (both attempts)", devName, tok.Output, 4000+5000)
	}
	if tok.CacheRead != 3000+4000 {
		t.Errorf("PerStage[%q].CacheRead = %d, want %d (both attempts)", devName, tok.CacheRead, 3000+4000)
	}
	if wantStageCost := 0.20 + 0.25; tok.CostUSD < wantStageCost-0.0001 || tok.CostUSD > wantStageCost+0.0001 {
		t.Errorf("PerStage[%q].CostUSD = %f, want ~%f (both attempts) — the first attempt's spend was dropped", devName, tok.CostUSD, wantStageCost)
	}

	// The cache hit rate must describe the whole stage, not its final attempt.
	if tok.CacheHitRate == nil {
		t.Fatalf("PerStage[%q].CacheHitRate = nil, want a rate over the accumulated totals", devName)
	}
	wantRate := float64(3000+4000) / float64((10000+12000)+(3000+4000))
	if *tok.CacheHitRate < wantRate-0.0001 || *tok.CacheHitRate > wantRate+0.0001 {
		t.Errorf("PerStage[%q].CacheHitRate = %f, want ~%f", devName, *tok.CacheHitRate, wantRate)
	}

	// Run total covers every attempt of every stage.
	wantTotal := 0.20 + 0.02 + 0.25
	if record.Tokens.EstimatedCostUSD < wantTotal-0.0001 || record.Tokens.EstimatedCostUSD > wantTotal+0.0001 {
		t.Errorf("EstimatedCostUSD = %f, want ~%f — a backtracked run must not under-report", record.Tokens.EstimatedCostUSD, wantTotal)
	}
}

func TestExtractSizeFromLabels(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{"canonical label", []string{"type:feature", "size:M"}, "M"},
		{"lowercase value", []string{"size:xl"}, "XL"},
		{"whitespace tolerated", []string{" Size: L "}, "L"},
		{"first size label wins", []string{"size:S", "size:L"}, "S"},
		{"no size label", []string{"type:bug", "area:vscode"}, ""},
		{"unrecognized bucket is not defaulted", []string{"size:huge"}, ""},
		{"empty", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractSizeFromLabels(tt.labels); got != tt.want {
				t.Errorf("ExtractSizeFromLabels(%v) = %q, want %q", tt.labels, got, tt.want)
			}
		})
	}
}

// TestBuildV2Record_RecoveredStageIsCompleteNotFailed is the durable-record
// half of #407.
//
// BuildV2Record stamps a stage detail "complete" from CompletedStages and then
// OVERWRITES it with "failed" for any stage present in StageErrors
// (history.go's "Check for stage error"). That overwrite is correct and stays —
// it is what makes a genuinely failed stage read failed — but before #407
// nothing ever removed a StageErrors key, so a stage that failed and then
// SUCCEEDED on retry was stamped "failed" in the permanent run record. Every
// downstream consumer of history (pipeline-health, retro, the platform's
// stage-effectiveness math) then treated a recovered run as a broken one.
//
// The record is built from the run's own state, through the production writers,
// in the production order — no hand-built snapshot.
func TestBuildV2Record_RecoveredStageIsCompleteNotFailed(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()
	rs := NewRuntimeState("nightgauge/nightgauge", 407, "item-407", testRunID())

	rs.BeginStage(StageIssuePickup)
	rs.CompleteStageWithCost(0, 5000, 2000, 1500, 0.03)

	// feature-validate fails, then succeeds on the retry.
	rs.BeginStage(StageFeatureValidate)
	rs.CompleteStageWithCost(1, 9000, 2500, 3000, 0.19)
	rs.SetStageError(StageFeatureValidate, "exit 1: 2 tests failed")
	rs.BeginStage(StageFeatureValidate)
	rs.CompleteStageWithCost(0, 11000, 3100, 4000, 0.24)

	record := hw.BuildV2Record(rs, true, "", V2RunInput{}, now)

	detail, ok := record.Stages[string(StageFeatureValidate)]
	if !ok {
		t.Fatalf("feature-validate missing from the record's stages: %+v", record.Stages)
	}
	if detail.Status != "complete" {
		t.Errorf("recovered stage status = %q (error=%q), want \"complete\" — "+
			"the stage's LATEST attempt succeeded, and the record is what health "+
			"analysis and retro read months later", detail.Status, detail.Error)
	}
	if detail.Error != "" {
		t.Errorf("recovered stage carries error text %q in the durable record", detail.Error)
	}

	// The failed attempt's spend is still booked — clearing the error must not
	// erase the money the run actually spent (the #4172-era accumulate rule).
	tok, ok := record.Tokens.PerStage[string(StageFeatureValidate)]
	if !ok {
		t.Fatalf("Tokens.PerStage missing feature-validate")
	}
	if want := 0.19 + 0.24; tok.CostUSD < want-0.0001 || tok.CostUSD > want+0.0001 {
		t.Errorf("PerStage[feature-validate].CostUSD = %f, want ~%f (both attempts)", tok.CostUSD, want)
	}
}

// TestBuildV2Record_StageErrorEntryStampsFailed is the counterweight, and it
// pins the ONLY overwrite that can produce it.
//
// history.go's "Check for stage error" block is what makes a genuinely failed
// stage read "failed" in the permanent record — every CompletedStages entry is
// stamped "complete" first and BuildV2Record never consults
// StageResult.ExitCode, so a stage's StageErrors entry is the sole carrier of
// the failure. Nothing else in this package pinned that direction:
// TestWriteV2_FailedPipeline's stage detail is stamped by the global-error
// fallback further down ("If there's a global error but no specific stage
// error, attach to the last stage"), which targets snap.Stage regardless of
// StageErrors, and the recovered-stage test above asserts only the
// complete direction.
//
// So this case arms nothing but the overwrite: the failed stage is NOT
// snap.Stage, and no global error is supplied, leaving the fallback disarmed.
// Delete history.go's overwrite and this test goes red.
func TestBuildV2Record_StageErrorEntryStampsFailed(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()
	rs := NewRuntimeState("nightgauge/nightgauge", 407, "item-407", testRunID())

	rs.BeginStage(StageIssuePickup)
	rs.CompleteStageWithCost(0, 5000, 2000, 1500, 0.03)

	// feature-dev fails and is never re-run successfully, so its entry stands.
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStageWithCost(1, 9000, 2500, 3000, 0.19)
	rs.SetStageError(StageFeatureDev, "exit 1: compilation failed")

	// A LATER stage is the current one, so the global-error fallback — which
	// only ever stamps snap.Stage — cannot be what marks feature-dev failed.
	rs.BeginStage(StageFeatureValidate)
	rs.CompleteStageWithCost(0, 4000, 1200, 900, 0.08)

	record := hw.BuildV2Record(rs, false, "", V2RunInput{}, now)

	detail, ok := record.Stages[string(StageFeatureDev)]
	if !ok {
		t.Fatalf("feature-dev missing from the record's stages: %+v", record.Stages)
	}
	if detail.Status != "failed" {
		t.Errorf("stage with a StageErrors entry recorded status %q, want \"failed\" — "+
			"the record is exit-code blind, so this overwrite is the only thing that "+
			"can mark a stage failed", detail.Status)
	}
	if detail.Error != "exit 1: compilation failed" {
		t.Errorf("recorded error = %q, want the text SetStageError wrote", detail.Error)
	}

	// The stage the fallback WOULD have stamped is untouched, which is what
	// proves the assertion above came from the StageErrors overwrite.
	if got := record.Stages[string(StageFeatureValidate)].Status; got != "complete" {
		t.Errorf("feature-validate status = %q, want \"complete\" — no global error was supplied", got)
	}
}

// --- cost_unstamped survival tests (Issue #585, #588 review finding B1) ---
//
// StageResult.CostUnstamped (set by RuntimeState.CompleteStage when the
// serving adapter's (provider, model) pair cannot be priced) must survive
// BuildV2Record's per-stage fold into V2StageTokens.CostUnstamped and the
// run-level V2Tokens.CostUnstamped aggregate — otherwise an unresolvable
// pricing lookup persists into the durable JSONL history as `cost_usd: 0`,
// indistinguishable from a legitimately-free run.

// TestBuildV2Record_UnresolvableAdapterMarksCostUnstamped verifies a
// CompleteStage call whose (model, adapter) pair cannot be resolved against
// the pricing registry lands in the built V2 record with cost_unstamped set,
// at both the per-stage and run levels.
func TestBuildV2Record_UnresolvableAdapterMarksCostUnstamped(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()
	rs := NewRuntimeState("nightgauge/nightgauge", 585, "item-585", testRunID())

	rs.BeginStage(StageFeaturePlanning)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "nonexistent-band-xyz", "grok")

	record := hw.BuildV2Record(rs, true, "", V2RunInput{}, now)

	stageName := string(StageFeaturePlanning)
	tok, ok := record.Tokens.PerStage[stageName]
	if !ok {
		t.Fatalf("Tokens.PerStage missing entry for %q", stageName)
	}
	if !tok.CostUnstamped {
		t.Errorf("PerStage[%q].CostUnstamped = false, want true for an unresolvable (grok, nonexistent-band-xyz) pair", stageName)
	}
	if tok.CostUSD != 0 {
		t.Errorf("PerStage[%q].CostUSD = %v, want 0 (unstamped placeholder)", stageName, tok.CostUSD)
	}
	if !record.Tokens.CostUnstamped {
		t.Error("Tokens.CostUnstamped = false, want true — the run-level aggregate must surface an unstamped stage")
	}
}

// TestBuildV2Record_StampedStageDoesNotCarryCostUnstamped is the negative
// counterpart: an ordinary stage whose (model, adapter) pair resolves
// cleanly must NOT carry cost_unstamped at either the per-stage or run level.
func TestBuildV2Record_StampedStageDoesNotCarryCostUnstamped(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()
	rs := NewRuntimeState("nightgauge/nightgauge", 585, "item-585-stamped", testRunID())

	rs.BeginStage(StageFeaturePlanning)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "sonnet", "claude")

	record := hw.BuildV2Record(rs, true, "", V2RunInput{}, now)

	stageName := string(StageFeaturePlanning)
	tok, ok := record.Tokens.PerStage[stageName]
	if !ok {
		t.Fatalf("Tokens.PerStage missing entry for %q", stageName)
	}
	if tok.CostUnstamped {
		t.Errorf("PerStage[%q].CostUnstamped = true, want false for a resolvable (claude, sonnet) pair", stageName)
	}
	if tok.CostUSD == 0 {
		t.Errorf("PerStage[%q].CostUSD = 0, want a non-zero priced figure", stageName)
	}
	if record.Tokens.CostUnstamped {
		t.Error("Tokens.CostUnstamped = true, want false — no stage in this run is unstamped")
	}
}

// TestCompleteStage_CostSourceMatchesStampedness (Issue #682) pins
// RuntimeState.CompleteStage's cost_source decision directly against the
// exact stamped bool CalculateCostForAdapter returned — the same value that
// already decides CostUnstamped, so the two fields can never disagree about
// whether an occurrence was priced.
func TestCompleteStage_CostSourceMatchesStampedness(t *testing.T) {
	// grok/sonnet resolves against the pricing registry for a NON-Claude
	// adapter (see TestCalculateCostForAdapter_PinsRun01a007d5Regression) —
	// exactly the case #682 exists to make reachable: a rate-card-derived,
	// not vendor-reported, cost.
	rs := NewRuntimeState("nightgauge/nightgauge", 682, "item-682-computed", testRunID())
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "sonnet", "grok")

	got := rs.CompletedStages[0]
	if got.CostSource != CostSourceComputed {
		t.Errorf("CostSource = %q, want %q for a resolvable (grok, sonnet) pair", got.CostSource, CostSourceComputed)
	}
	if got.CostUnstamped {
		t.Error("CostUnstamped = true, want false — grok/sonnet resolves cleanly")
	}

	// An unresolvable (provider, model) pair: CalculateCostForAdapter returns
	// stamped=false, so CostSource must read "unknown", not "computed".
	rs2 := NewRuntimeState("nightgauge/nightgauge", 682, "item-682-unknown", testRunID())
	rs2.BeginStage(StageFeatureDev)
	rs2.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "nonexistent-band-xyz", "grok")

	got2 := rs2.CompletedStages[0]
	if got2.CostSource != CostSourceUnknown {
		t.Errorf("CostSource = %q, want %q for an unresolvable pair", got2.CostSource, CostSourceUnknown)
	}
	if !got2.CostUnstamped {
		t.Error("CostUnstamped = false, want true — the pair does not resolve")
	}

	// The local-provider convention (#56): ollama/lm-studio resolve to a
	// STAMPED $0 (genuinely free, not "we don't know"), so CostSource must
	// read "computed" — a rate-card resolution at a zero rate — never
	// "unknown".
	rs3 := NewRuntimeState("nightgauge/nightgauge", 682, "item-682-local", testRunID())
	rs3.BeginStage(StageFeatureDev)
	rs3.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "qwen3-coder:32b", "ollama")

	got3 := rs3.CompletedStages[0]
	if got3.CostSource != CostSourceComputed {
		t.Errorf("CostSource = %q, want %q for the local-provider intentional $0", got3.CostSource, CostSourceComputed)
	}
	if got3.CostUnstamped {
		t.Error("CostUnstamped = true, want false — a local-provider $0 is not a pricing gap")
	}
}

// TestCompleteStageWithCost_CostSourceIsAlwaysNative (Issue #682) pins that
// the CLI-reported actual-cost path always writes CostSourceNative — by
// definition, this IS the vendor-measured figure, never a rate-card estimate.
func TestCompleteStageWithCost_CostSourceIsAlwaysNative(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 682, "item-682-native", testRunID())
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStageWithCost(0, 10000, 4000, 3000, 0.20)

	got := rs.CompletedStages[0]
	if got.CostSource != CostSourceNative {
		t.Errorf("CostSource = %q, want %q for the CLI-reported cost path", got.CostSource, CostSourceNative)
	}
	if got.CostUnstamped {
		t.Error("CostUnstamped = true, want false — a CLI-reported cost is always priced")
	}
}

// TestBuildV2Record_ComputedCostSourceReachesPerStage is the end-to-end proof
// AC 2 of #682 asks for: a rate-card-derived (non-Claude) cost reaches the
// DURABLE V2StageTokens record with cost_source="computed" — the exact value
// LocalTelemetryUsageProvider.stageCostConfidence maps to confidence
// "estimated". Before #682, V2StageTokens had no CostSource field at all, so
// this value could never leave RuntimeState.
func TestBuildV2Record_ComputedCostSourceReachesPerStage(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()
	rs := NewRuntimeState("nightgauge/nightgauge", 682, "item-682-e2e-computed", testRunID())

	rs.BeginStage(StageFeatureDev)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "sonnet", "grok")

	record := hw.BuildV2Record(rs, true, "", V2RunInput{}, now)

	stageName := string(StageFeatureDev)
	tok, ok := record.Tokens.PerStage[stageName]
	if !ok {
		t.Fatalf("Tokens.PerStage missing entry for %q", stageName)
	}
	if tok.CostSource != CostSourceComputed {
		t.Errorf("PerStage[%q].CostSource = %q, want %q — a grok/sonnet stage is rate-card priced", stageName, tok.CostSource, CostSourceComputed)
	}
	if tok.CostUSD <= 0 {
		t.Errorf("PerStage[%q].CostUSD = %v, want a positive priced figure", stageName, tok.CostUSD)
	}

	// Marshal to the actual bytes the writer would persist to the JSONL
	// history file — proving the field survives serialization onto the wire
	// TypeScript's HistoryStageTokenUsageSchema validates, not merely that
	// the in-memory struct carries it.
	raw, err := json.Marshal(record.Tokens)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"cost_source":"computed"`) {
		t.Errorf("marshaled Tokens JSON missing `\"cost_source\":\"computed\"`; got: %s", raw)
	}
}

// TestBuildV2Record_UnknownCostSourceReachesPerStage extends
// TestBuildV2Record_UnresolvableAdapterMarksCostUnstamped with the
// cost_source assertion: an unpriceable pair must read "unknown", not be
// silently absent (absence reads as "we never learned the source at all",
// a weaker and different claim than "we tried and could not price it").
func TestBuildV2Record_UnknownCostSourceReachesPerStage(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()
	rs := NewRuntimeState("nightgauge/nightgauge", 682, "item-682-e2e-unknown", testRunID())

	rs.BeginStage(StageFeaturePlanning)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "nonexistent-band-xyz", "grok")

	record := hw.BuildV2Record(rs, true, "", V2RunInput{}, now)

	stageName := string(StageFeaturePlanning)
	tok, ok := record.Tokens.PerStage[stageName]
	if !ok {
		t.Fatalf("Tokens.PerStage missing entry for %q", stageName)
	}
	if tok.CostSource != CostSourceUnknown {
		t.Errorf("PerStage[%q].CostSource = %q, want %q", stageName, tok.CostSource, CostSourceUnknown)
	}
}

// TestBuildV2Record_NativeCostSourceReachesPerStage is the native-path
// counterpart of the computed/unknown end-to-end tests above.
func TestBuildV2Record_NativeCostSourceReachesPerStage(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()
	rs := NewRuntimeState("nightgauge/nightgauge", 682, "item-682-e2e-native", testRunID())

	rs.BeginStage(StageFeatureDev)
	rs.CompleteStageWithCost(0, 10000, 4000, 3000, 0.20)

	record := hw.BuildV2Record(rs, true, "", V2RunInput{}, now)

	stageName := string(StageFeatureDev)
	tok, ok := record.Tokens.PerStage[stageName]
	if !ok {
		t.Fatalf("Tokens.PerStage missing entry for %q", stageName)
	}
	if tok.CostSource != CostSourceNative {
		t.Errorf("PerStage[%q].CostSource = %q, want %q", stageName, tok.CostSource, CostSourceNative)
	}
}

// TestBuildV2Record_CostSourceFoldsToWeakestAcrossRetries (Issue #682) proves
// foldCostSource's "weakest wins" rule end to end: a stage that ran once
// native and once computed (a genuine backtrack/adapter-switch scenario, same
// shape as TestBuildV2Record_BacktrackedStageAccumulates) must report the
// ACCUMULATED entry as "computed" — the sum is only PART vendor-measured, and
// "native" would overstate that. Order must not matter, so both orderings are
// exercised.
func TestBuildV2Record_CostSourceFoldsToWeakestAcrossRetries(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()

	t.Run("native then computed", func(t *testing.T) {
		rs := NewRuntimeState("nightgauge/nightgauge", 682, "item-682-fold-a", testRunID())
		rs.BeginStage(StageFeatureDev)
		rs.CompleteStageWithCost(0, 10000, 4000, 3000, 0.20) // native
		rs.BeginStage(StageFeatureValidate)
		rs.CompleteStageWithCost(0, 2000, 500, 1000, 0.02)
		rs.BeginStage(StageFeatureDev)                                                        // backtrack re-run
		rs.CompleteStage(0, tokens.TokenCounts{Input: 12000, Output: 5000}, "sonnet", "grok") // computed

		record := hw.BuildV2Record(rs, true, "", V2RunInput{}, now)
		tok := record.Tokens.PerStage[string(StageFeatureDev)]
		if tok.CostSource != CostSourceComputed {
			t.Errorf("CostSource = %q, want %q (weakest of native, computed)", tok.CostSource, CostSourceComputed)
		}
	})

	t.Run("computed then native", func(t *testing.T) {
		rs := NewRuntimeState("nightgauge/nightgauge", 682, "item-682-fold-b", testRunID())
		rs.BeginStage(StageFeatureDev)
		rs.CompleteStage(0, tokens.TokenCounts{Input: 10000, Output: 4000}, "sonnet", "grok") // computed
		rs.BeginStage(StageFeatureValidate)
		rs.CompleteStageWithCost(0, 2000, 500, 1000, 0.02)
		rs.BeginStage(StageFeatureDev)                       // backtrack re-run
		rs.CompleteStageWithCost(0, 12000, 5000, 4000, 0.25) // native

		record := hw.BuildV2Record(rs, true, "", V2RunInput{}, now)
		tok := record.Tokens.PerStage[string(StageFeatureDev)]
		if tok.CostSource != CostSourceComputed {
			t.Errorf("CostSource = %q, want %q (weakest of computed, native — order must not matter)", tok.CostSource, CostSourceComputed)
		}
	})

	t.Run("computed then unknown", func(t *testing.T) {
		rs := NewRuntimeState("nightgauge/nightgauge", 682, "item-682-fold-c", testRunID())
		rs.BeginStage(StageFeatureDev)
		rs.CompleteStage(0, tokens.TokenCounts{Input: 10000, Output: 4000}, "sonnet", "grok") // computed
		rs.BeginStage(StageFeatureValidate)
		rs.CompleteStageWithCost(0, 2000, 500, 1000, 0.02)
		rs.BeginStage(StageFeatureDev)                                                                      // backtrack re-run
		rs.CompleteStage(0, tokens.TokenCounts{Input: 12000, Output: 5000}, "nonexistent-band-xyz", "grok") // unknown

		record := hw.BuildV2Record(rs, true, "", V2RunInput{}, now)
		tok := record.Tokens.PerStage[string(StageFeatureDev)]
		if tok.CostSource != CostSourceUnknown {
			t.Errorf("CostSource = %q, want %q (weakest of computed, unknown)", tok.CostSource, CostSourceUnknown)
		}
	})
}

// TestFoldCostSource pins the priority table directly, independent of the
// BuildV2Record plumbing above.
func TestFoldCostSource(t *testing.T) {
	cases := []struct {
		a, b, want string
	}{
		{"", "", ""},
		{"", CostSourceNative, CostSourceNative},
		{CostSourceComputed, "", CostSourceComputed},
		{CostSourceNative, CostSourceNative, CostSourceNative},
		{CostSourceNative, CostSourceComputed, CostSourceComputed},
		{CostSourceComputed, CostSourceNative, CostSourceComputed},
		{CostSourceComputed, CostSourceUnknown, CostSourceUnknown},
		{CostSourceUnknown, CostSourceComputed, CostSourceUnknown},
		{CostSourceNative, CostSourceUnknown, CostSourceUnknown},
	}
	for _, tc := range cases {
		if got := foldCostSource(tc.a, tc.b); got != tc.want {
			t.Errorf("foldCostSource(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestBuildV2Record_UnstampedFoldSurvivesRetry pins the OR-fold semantics: a
// stage that ran twice (retry/backtrack) with the FIRST attempt unstamped and
// the SECOND attempt stamped must still read cost_unstamped=true overall — a
// later successful pricing must not erase the earlier placeholder-zero
// contribution baked into the accumulated cost_usd.
func TestBuildV2Record_UnstampedFoldSurvivesRetry(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()
	rs := NewRuntimeState("nightgauge/nightgauge", 585, "item-585-retry", testRunID())

	// First attempt: unresolvable pair, placeholder $0.
	rs.BeginStage(StageFeaturePlanning)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "nonexistent-band-xyz", "grok")

	// Retry: resolves cleanly.
	rs.BeginStage(StageFeaturePlanning)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "sonnet", "claude")

	record := hw.BuildV2Record(rs, true, "", V2RunInput{}, now)

	stageName := string(StageFeaturePlanning)
	tok, ok := record.Tokens.PerStage[stageName]
	if !ok {
		t.Fatalf("Tokens.PerStage missing entry for %q", stageName)
	}
	if !tok.CostUnstamped {
		t.Error("PerStage[...].CostUnstamped = false, want true — the first attempt's unstamped $0 must survive the fold even after a stamped retry")
	}
	if !record.Tokens.CostUnstamped {
		t.Error("Tokens.CostUnstamped = false, want true — the run-level aggregate must reflect the tainted stage")
	}
}

// --- deterministic-stage cost tests (Issue #890) ---
//
// Four stages that dispatch NO model (the pipeline-start/pipeline-finish
// bookends and the deterministic paths of pr-create/pr-merge) appear in every
// run. Until #890 each of them priced through the unresolvable (anthropic, "")
// pair, landed CostUnstamped, and made the run-level OR true unconditionally —
// so `cost by-class` marked 100% of runs untrustworthy by construction.

// TestBuildV2Record_DeterministicStagesDoNotMarkRunUnstamped is the FALSE-case
// assertion #890 turns on: a run whose stages are all either natively priced
// or deterministic must report cost_unstamped: false at the run level. A test
// asserting only the true case passes on the pre-#890 code and proves nothing
// (docs/FAILURE_TAXONOMY.md § Vacuous Assertion).
func TestBuildV2Record_DeterministicStagesDoNotMarkRunUnstamped(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()
	rs := NewRuntimeState("nightgauge/nightgauge", 890, "item-890-false", testRunID())

	// One model-running stage, priced natively (the CLI-reported figure).
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStageWithCost(0, 1000, 500, 0, 12.23)

	// The deterministic stages: no model, not one billable token.
	deterministic := []PipelineStage{
		PipelineStage("pipeline-start"),
		StageIssuePickup,
		StagePRCreate,
		StagePRMerge,
		PipelineStage("pipeline-finish"),
	}
	for _, stage := range deterministic {
		rs.BeginStage(stage)
		rs.CompleteStage(0, tokens.TokenCounts{}, "", "")
	}

	record := hw.BuildV2Record(rs, true, "", V2RunInput{}, now)

	if record.Tokens.CostUnstamped {
		t.Error("Tokens.CostUnstamped = true, want false — every stage in this run is either natively priced or dispatched no model at all")
	}
	for _, stage := range deterministic {
		stageName := string(stage)
		tok, ok := record.Tokens.PerStage[stageName]
		if !ok {
			t.Fatalf("Tokens.PerStage missing entry for %q", stageName)
		}
		if tok.CostUnstamped {
			t.Errorf("PerStage[%q].CostUnstamped = true, want false — a stage that dispatched no model has a genuine $0", stageName)
		}
		if tok.CostSource != CostSourceDeterministic {
			t.Errorf("PerStage[%q].CostSource = %q, want %q — \"ran nothing\" must stay distinguishable from \"ran something unpriceable\"",
				stageName, tok.CostSource, CostSourceDeterministic)
		}
		if tok.CostUSD != 0 {
			t.Errorf("PerStage[%q].CostUSD = %v, want an exact 0", stageName, tok.CostUSD)
		}
	}
	if got := record.Tokens.EstimatedCostUSD; got != 12.23 {
		t.Errorf("Tokens.EstimatedCostUSD = %v, want 12.23 — the deterministic stages must contribute exactly nothing", got)
	}
}

// TestBuildV2Record_DeterministicStagesDoNotHideAnUnpriceableStage pins the
// OR aggregation #890 must NOT weaken: add one genuinely unpriceable stage to
// the same otherwise-clean run and the run-level flag goes back to true.
func TestBuildV2Record_DeterministicStagesDoNotHideAnUnpriceableStage(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()
	rs := NewRuntimeState("nightgauge/nightgauge", 890, "item-890-true", testRunID())

	rs.BeginStage(PipelineStage("pipeline-start"))
	rs.CompleteStage(0, tokens.TokenCounts{}, "", "")
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStageWithCost(0, 1000, 500, 0, 12.23)
	// A real dispatch whose (provider, model) pair has no registry entry.
	rs.BeginStage(StageFeatureValidate)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 500}, "nonexistent-band-xyz", "grok")
	rs.BeginStage(PipelineStage("pipeline-finish"))
	rs.CompleteStage(0, tokens.TokenCounts{}, "", "")

	record := hw.BuildV2Record(rs, true, "", V2RunInput{}, now)

	if !record.Tokens.CostUnstamped {
		t.Error("Tokens.CostUnstamped = false, want true — one unpriceable stage still taints the run total")
	}
	if tok := record.Tokens.PerStage[string(StageFeatureValidate)]; tok.CostSource != CostSourceUnknown {
		t.Errorf("PerStage[feature-validate].CostSource = %q, want %q", tok.CostSource, CostSourceUnknown)
	}
}

// TestCompleteStage_NoModelDispatchedIsDeterministic pins the predicate at the
// point of decision. Both halves matter: a stage that NAMES a model keeps
// whatever stamped-ness the registry gives it even at zero tokens, so the
// carve-out cannot absorb a real dispatch that merely lost its token counts.
func TestCompleteStage_NoModelDispatchedIsDeterministic(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 890, "item-890-unit", testRunID())
	rs.BeginStage(StagePRCreate)
	rs.CompleteStage(0, tokens.TokenCounts{}, "", "")

	got := rs.CompletedStages[0]
	if got.CostSource != CostSourceDeterministic {
		t.Errorf("CostSource = %q, want %q for a stage that dispatched no model", got.CostSource, CostSourceDeterministic)
	}
	if got.CostUnstamped {
		t.Error("CostUnstamped = true, want false — nothing was looked up, so the registry did not miss")
	}
	if got.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0", got.CostUSD)
	}

	// Named model, unresolvable, zero tokens: still unstamped/unknown. The
	// carve-out is deliberately narrow.
	rs2 := NewRuntimeState("nightgauge/nightgauge", 890, "item-890-named", testRunID())
	rs2.BeginStage(StagePRCreate)
	rs2.CompleteStage(0, tokens.TokenCounts{}, "nonexistent-band-xyz", "grok")
	got2 := rs2.CompletedStages[0]
	if got2.CostSource != CostSourceUnknown {
		t.Errorf("CostSource = %q, want %q — a named model that will not resolve is still a registry miss", got2.CostSource, CostSourceUnknown)
	}
	if !got2.CostUnstamped {
		t.Error("CostUnstamped = false, want true — a named, unresolvable model must stay flagged")
	}

	// No model but real tokens: something was dispatched and we cannot price
	// it, so the unstamped contract still applies.
	rs3 := NewRuntimeState("nightgauge/nightgauge", 890, "item-890-tokens", testRunID())
	rs3.BeginStage(StagePRCreate)
	rs3.CompleteStage(0, tokens.TokenCounts{Input: 1000}, "", "grok")
	got3 := rs3.CompletedStages[0]
	if got3.CostSource == CostSourceDeterministic {
		t.Error("CostSource = deterministic for a stage that spent tokens — a token count is evidence of a dispatch")
	}
}

// TestFoldCostSource_DeterministicNeverWeakensASpendingOccurrence pins the
// fold direction for the new label. A stage folded from a deterministic run
// and an LLM-fallback run (pr-create's two execution paths, #890) reports the
// label of the occurrence that actually spent money: the exact $0 adds no
// uncertainty to the sum, so it must not win foldCostSource's weakest-wins
// rule.
func TestFoldCostSource_DeterministicNeverWeakensASpendingOccurrence(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{CostSourceDeterministic, CostSourceDeterministic, CostSourceDeterministic},
		{CostSourceDeterministic, CostSourceNative, CostSourceNative},
		{CostSourceNative, CostSourceDeterministic, CostSourceNative},
		{CostSourceDeterministic, CostSourceComputed, CostSourceComputed},
		{CostSourceDeterministic, CostSourceUnknown, CostSourceUnknown},
		{CostSourceUnknown, CostSourceDeterministic, CostSourceUnknown},
		{"", CostSourceDeterministic, CostSourceDeterministic},
	}
	for _, tc := range cases {
		if got := foldCostSource(tc.a, tc.b); got != tc.want {
			t.Errorf("foldCostSource(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestBuildV2Record_BacktrackedStageDetailDescribesTheStandingAttempt is the
// half of #556 that the accumulation test above cannot see.
//
// Spend is a SUM, so it does not care which attempt an entry came from. The
// stage DETAIL is not a sum: stages[<name>].started_at, .completed_at and
// .duration_ms describe one attempt, and an operator reading the record needs
// them to describe the attempt that actually stands. Since #556 the standing
// attempt is the one in CompletedStages and the displaced one is in
// SupersededStages, and BuildV2Record iterates superseded-first precisely so
// the LAST write wins. Reverse that order and this arm goes red while every
// cost assertion stays green.
func TestBuildV2Record_BacktrackedStageDetailDescribesTheStandingAttempt(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	now := time.Now()

	rs := NewRuntimeState("nightgauge/nightgauge", 556, "item-556-detail", testRunID())
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStageWithCost(0, 10000, 4000, 0, 1.25) // first attempt
	rs.BeginStage(StageFeatureValidate)
	rs.CompleteStageWithCost(0, 2000, 500, 0, 0.02)
	// started_at is formatted as RFC 3339 with SECOND granularity, so the two
	// attempts must land in different seconds or the assertion below cannot
	// tell them apart and would pass against either ordering. Spinning to the
	// next second boundary costs under a second and is deterministic, unlike a
	// fixed sleep sized by guesswork.
	for mark := time.Now().Second(); time.Now().Second() == mark; {
		time.Sleep(5 * time.Millisecond)
	}
	rs.BeginStage(StageFeatureDev) // the rewind
	standingStart := rs.StageStart
	rs.CompleteStageWithCost(0, 3000, 1000, 0, 0.75) // second attempt

	record := hw.BuildV2Record(rs, true, "", V2RunInput{}, now)

	detail, ok := record.Stages[string(StageFeatureDev)]
	if !ok {
		t.Fatalf("feature-dev missing from record.Stages: %+v", record.Stages)
	}
	if want := standingStart.Format(time.RFC3339); detail.StartedAt != want {
		t.Errorf("stages[feature-dev].started_at = %q, want the STANDING attempt's %q — "+
			"the record describes a superseded attempt as though it were the outcome",
			detail.StartedAt, want)
	}
	if detail.Status != "complete" {
		t.Errorf("stages[feature-dev].status = %q, want complete", detail.Status)
	}

	// And the invariant that ties the two halves together: the record's run
	// total is the runtime's own total. Asserted against the live value rather
	// than a transcribed constant, so it cannot drift into agreeing with a
	// wrong sum.
	if record.Tokens.EstimatedCostUSD != rs.TotalCostUSD {
		t.Errorf("estimated_cost_usd = %v but the run booked %v — a superseded attempt's "+
			"spend is missing from the durable record",
			record.Tokens.EstimatedCostUSD, rs.TotalCostUSD)
	}
}

// --- per-stage model attribution (#1213) ---
//
// The per-(stage, model) calibration loop reads tokens.per_stage[*].model and
// NOTHING wrote it: PostPipelineAnalyzer's `.filter(([, usage]) => usage.model)`
// dropped every row, stage-model-calibration.json did not exist in any
// workspace after hundreds of runs, and estimatePipelineCost always fell
// through to the static baselines — while docs/SELF_IMPROVEMENT_LOOP.md
// described the loop as working.

func TestBuildV2Record_PerStageTokensCarryTheServedModel(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	rs := NewRuntimeState("nightgauge/nightgauge", 1213, "item", testRunID())

	rs.BeginStage(StageFeatureDev)
	rs.RecordStageModel(StageFeatureDev, "opus")
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 100}, "", "")
	rs.BeginStage(StagePRCreate)
	rs.RecordStageModel(StagePRCreate, "haiku")
	rs.CompleteStage(0, tokens.TokenCounts{Input: 200, Output: 20}, "", "")

	rec := hw.BuildV2Record(rs, true, "", V2RunInput{Title: "t"}, time.Now())

	for stage, want := range map[string]string{"feature-dev": "opus", "pr-create": "haiku"} {
		got := rec.Tokens.PerStage[stage].Model
		if got != want {
			t.Errorf("tokens.per_stage[%q].model = %q, want %q", stage, got, want)
		}
		// The two attributions of one fact must agree — they read a single
		// hoisted variable precisely so they cannot drift.
		if sel := rec.Stages[stage].ModelSelection; sel == nil || sel.Model != got {
			t.Errorf("stage %q: per_stage.model=%q disagrees with model_selection", stage, got)
		}
	}
}

func TestBuildV2Record_PerStageModelSurvivesJSONRoundTrip(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	rs := NewRuntimeState("nightgauge/nightgauge", 1213, "item", testRunID())
	rs.BeginStage(StageFeatureDev)
	rs.RecordStageModel(StageFeatureDev, "sonnet")
	rs.CompleteStage(0, tokens.TokenCounts{Input: 1000, Output: 100}, "", "")

	rec := hw.BuildV2Record(rs, true, "", V2RunInput{Title: "t"}, time.Now())
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The analyzer reads the WIRE, so the json tag is what matters.
	if !strings.Contains(string(data), `"model":"sonnet"`) {
		t.Error("marshalled record carries no per-stage model")
	}
	var back V2RunRecord
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := back.Tokens.PerStage["feature-dev"].Model; got != "sonnet" {
		t.Errorf("round-tripped model = %q, want sonnet", got)
	}
}

// A backtracked stage sums tokens across attempts but has ONE model: the one
// the last attempt used — the same attempt stages[<name>] describes. Assigned,
// never accumulated, matching Adapter.
func TestBuildV2Record_PerStageModelIsTheLastAttempt(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	rs := NewRuntimeState("nightgauge/nightgauge", 1213, "item", testRunID())

	rs.BeginStage(StageFeatureDev)
	rs.RecordStageModel(StageFeatureDev, "sonnet")
	rs.CompleteStage(1, tokens.TokenCounts{Input: 1000, Output: 100}, "", "")
	rs.BeginStage(StageFeatureDev)
	rs.RecordStageModel(StageFeatureDev, "opus") // escalated on the retry
	rs.CompleteStage(0, tokens.TokenCounts{Input: 2000, Output: 200}, "", "")

	rec := hw.BuildV2Record(rs, true, "", V2RunInput{Title: "t"}, time.Now())
	usage := rec.Tokens.PerStage["feature-dev"]
	if usage.Model != "opus" {
		t.Errorf("model = %q, want opus (the attempt that stands)", usage.Model)
	}
	// Tokens still accumulate — the two folds are deliberately different.
	if usage.Output != 300 {
		t.Errorf("output = %d, want 300 (accumulated across attempts)", usage.Output)
	}
}

// --- the durable budget estimate (#1213) ---

func TestBuildV2Record_BudgetEstimateRoundTrips(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	rs := NewRuntimeState("nightgauge/nightgauge", 1213, "item", testRunID())
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 10, Output: 1}, "", "")

	rec := hw.BuildV2Record(rs, true, "", V2RunInput{
		Title: "t",
		BudgetEstimate: &V2BudgetEstimate{
			USD: 14.62, Source: "historical-p75", Provider: "anthropic", CeilingUSD: 50,
		},
	}, time.Now())

	data, _ := json.Marshal(rec)
	var back V2RunRecord
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.BudgetEstimate == nil {
		t.Fatal("budget_estimate lost in the round trip")
	}
	if back.BudgetEstimate.USD != 14.62 || back.BudgetEstimate.Source != "historical-p75" {
		t.Errorf("budget_estimate = %+v, want the input values", *back.BudgetEstimate)
	}
	if back.BudgetEstimate.Provider != "anthropic" {
		t.Errorf("provider = %q — an estimate is only comparable within one rate card",
			back.BudgetEstimate.Provider)
	}
}

// Absent means NOT ESTIMATED, never "estimated at zero" — a zero here becomes a
// division by zero in the accuracy report, or a 0-cost "perfect" prediction.
func TestBuildV2Record_NoEstimateOmitsTheBlock(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	rs := NewRuntimeState("nightgauge/nightgauge", 1213, "item", testRunID())
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStage(0, tokens.TokenCounts{Input: 10, Output: 1}, "", "")

	rec := hw.BuildV2Record(rs, true, "", V2RunInput{Title: "t"}, time.Now())
	if rec.BudgetEstimate != nil {
		t.Errorf("budget_estimate = %+v with no estimate supplied, want nil", *rec.BudgetEstimate)
	}
	data, _ := json.Marshal(rec)
	if strings.Contains(string(data), "budget_estimate") {
		t.Error("un-estimated record emits a budget_estimate key")
	}
}
