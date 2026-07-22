package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHistoryWriteAndRead(t *testing.T) {
	dir := t.TempDir()
	hw := NewHistoryWriter(dir)

	rs := NewRuntimeState("nightgauge/nightgauge", 1311, "item-123")
	rs.BeginStage(StageIssuePickup)
	rs.CompleteStage(0, 1000, 500, "")

	if err := hw.Write(rs, true, ""); err != nil {
		t.Fatalf("Write: %v", err)
	}

	entries, err := hw.ReadRecent(10)
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].IssueNumber != 1311 {
		t.Errorf("IssueNumber = %d, want 1311", entries[0].IssueNumber)
	}
	if !entries[0].Success {
		t.Error("Success should be true")
	}
	if len(entries[0].Stages) != 1 {
		t.Errorf("Stages = %d, want 1", len(entries[0].Stages))
	}
}

func TestHistoryMultipleEntries(t *testing.T) {
	dir := t.TempDir()
	hw := NewHistoryWriter(dir)

	for i := 0; i < 5; i++ {
		rs := NewRuntimeState("nightgauge/nightgauge", 1300+i, "item")
		if err := hw.Write(rs, true, ""); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	// Read last 3
	entries, err := hw.ReadRecent(3)
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("entries = %d, want 3", len(entries))
	}
	if entries[0].IssueNumber != 1302 {
		t.Errorf("first entry should be 1302, got %d", entries[0].IssueNumber)
	}
}

func TestHistoryReadEmpty(t *testing.T) {
	dir := t.TempDir()
	hw := NewHistoryWriter(dir)

	entries, err := hw.ReadRecent(10)
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries = %d, want 0", len(entries))
	}
}

// --- V2 tests ---

func TestWriteV2_ProducesValidRecord(t *testing.T) {
	dir := t.TempDir()
	hw := NewHistoryWriter(dir)

	rs := NewRuntimeState("nightgauge/nightgauge", 2001, "item-v2")
	rs.BeginStage(StageIssuePickup)
	rs.CompleteStage(0, 5000, 2000, "")
	rs.BeginStage(StageFeaturePlanning)
	rs.CompleteStage(0, 8000, 3000, "")

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

	rs := NewRuntimeState("nightgauge/nightgauge", 2002, "item-fail")
	rs.BeginStage(StageIssuePickup)
	rs.CompleteStage(0, 1000, 500, "")
	rs.BeginStage(StageFeatureDev)
	rs.CompleteStage(1, 3000, 1000, "")
	rs.StageErrors[string(StageFeatureDev)] = "compilation failed"

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
		rs := NewRuntimeState("nightgauge/nightgauge", 3000+i, "item")
		rs.BeginStage(StageIssuePickup)
		rs.CompleteStage(0, 1000, 500, "")
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

	rs := NewRuntimeState("nightgauge/nightgauge", 4000, "item-skip")
	rs.BeginStage(StageIssuePickup)
	rs.CompleteStage(0, 1000, 500, "")
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
	rs := NewRuntimeState("nightgauge/nightgauge", 2617, "item-interim")
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
	rs := NewRuntimeState("nightgauge/nightgauge", 234, "item-blocked")

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
	rs := NewRuntimeState("nightgauge/nightgauge", 183, "item-body")

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

// TestBuildV2Record_ZeroStagesNoTokens verifies that a record with no completed
// stages correctly reports zero tokens (not a crash).
func TestBuildV2Record_ZeroStagesNoTokens(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	input := V2RunInput{Title: "empty", Branch: "feat/test", BaseBranch: "main"}
	now := time.Now()

	rs := NewRuntimeState("nightgauge/nightgauge", 9999, "item-empty")
	// No stages completed.
	record := hw.BuildV2Record(rs, false, "preflight failed", input, now)

	if record.Tokens.TotalInput != 0 {
		t.Errorf("TotalInput = %d, want 0 for empty pipeline", record.Tokens.TotalInput)
	}
	if record.Tokens.EstimatedCostUSD != 0 {
		t.Errorf("EstimatedCostUSD = %f, want 0 for empty pipeline", record.Tokens.EstimatedCostUSD)
	}
}

// TestBuildV2Record_TotalCacheReadPopulated verifies that TotalCacheRead is
// populated from stage data (previously always 0).
func TestBuildV2Record_TotalCacheReadPopulated(t *testing.T) {
	hw := NewHistoryWriter(t.TempDir())
	input := V2RunInput{Title: "cache read test", Branch: "feat/test", BaseBranch: "main"}
	now := time.Now()

	rs := NewRuntimeState("nightgauge/nightgauge", 2617, "item-cache")
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
		rs := NewRuntimeState("nightgauge/nightgauge", 2459, "item-1")
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
		rs := NewRuntimeState("nightgauge/nightgauge", 2459, "item-2")
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
		rs := NewRuntimeState("nightgauge/nightgauge", 2459, "item-3")
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
		rs := NewRuntimeState("nightgauge/nightgauge", 2459, "item-4")
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
		rs := NewRuntimeState("nightgauge/nightgauge", 2459, "item-5")

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
	rs := NewRuntimeState("nightgauge/nightgauge", 3215, "item-mode")

	rs.BeginStage(StageIssuePickup)
	rs.RecordStageMode(StageIssuePickup, "efficiency")
	rs.CompleteStage(0, 1000, 500, "")

	rs.BeginStage(StageFeaturePlanning)
	// Deliberately no RecordStageMode here.
	rs.CompleteStage(0, 1500, 600, "")

	rs.BeginStage(StageFeatureDev)
	rs.RecordStageMode(StageFeatureDev, "maximum")
	rs.CompleteStage(0, 2000, 700, "")

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
	rs := NewRuntimeState("nightgauge/nightgauge", 3215, "item-empty")
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
	rs := NewRuntimeState("nightgauge/nightgauge", 3224, "item-adapter")

	rs.BeginStage(StageIssuePickup)
	rs.RecordStageAdapter(StageIssuePickup, "claude")
	rs.CompleteStage(0, 1000, 500, "")

	rs.BeginStage(StageFeaturePlanning)
	// No RecordStageAdapter — should fall back to V2RunInput.DefaultAdapter.
	rs.CompleteStage(0, 1500, 600, "")

	rs.BeginStage(StageFeatureDev)
	rs.RecordStageAdapter(StageFeatureDev, "gemini")
	rs.CompleteStage(0, 2000, 700, "")

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
	rsNoAdapter := NewRuntimeState("nightgauge/nightgauge", 3224, "item-no-adapter")
	rsNoAdapter.BeginStage(StageIssuePickup)
	rsNoAdapter.CompleteStage(0, 1000, 500, "")

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

// TestRecordStageAdapter_IgnoresEmpty verifies the no-op contract for empty
// adapter strings — preserves the omitempty guarantee when the resolver
// fails to produce a value.
func TestRecordStageAdapter_IgnoresEmpty(t *testing.T) {
	rs := NewRuntimeState("nightgauge/nightgauge", 3224, "item-empty-adapter")
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
	rs := NewRuntimeState("nightgauge/nightgauge", 3218, "item-mode-fix")

	// Two elevated stages, one with no mode.
	rs.BeginStage(StageIssuePickup)
	rs.RecordStageMode(StageIssuePickup, "elevated")
	rs.CompleteStage(0, 1000, 500, "")

	rs.BeginStage(StageFeaturePlanning)
	// No mode recorded — omitempty must keep the field absent.
	rs.CompleteStage(0, 1500, 600, "")

	rs.BeginStage(StageFeatureDev)
	rs.RecordStageMode(StageFeatureDev, "elevated")
	rs.CompleteStage(0, 2000, 700, "")

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
