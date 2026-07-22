package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// makeTestV2Record creates a minimal valid V2RunRecord for testing.
func makeTestV2Record(issueNumber int, startedAt string) V2RunRecord {
	return V2RunRecord{
		SchemaVersion: "2",
		RecordType:    "run",
		IssueNumber:   issueNumber,
		StartedAt:     startedAt,
		CompletedAt:   startedAt,
		Outcome:       "complete",
		Stages:        map[string]V2StageDetail{},
	}
}

// writeTestJSONL writes V2RunRecords to a daily JSONL file in dir.
func writeTestJSONL(t *testing.T, dir, filename string, records []V2RunRecord) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(filepath.Join(dir, filename))
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	defer f.Close()
	for _, r := range records {
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		f.Write(append(data, '\n'))
	}
}

func newTestHistoryWriter(t *testing.T) (*HistoryWriter, string) {
	t.Helper()
	root := t.TempDir()
	hw := NewHistoryWriter(root)
	return hw, hw.dir
}

func TestReadRecentV2_Empty(t *testing.T) {
	hw, _ := newTestHistoryWriter(t)
	records, err := hw.ReadRecentV2(10, 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("expected 0 records, got %d", len(records))
	}
}

func TestReadRecentV2_SingleFile(t *testing.T) {
	hw, dir := newTestHistoryWriter(t)
	writeTestJSONL(t, dir, "2026-03-15.jsonl", []V2RunRecord{
		makeTestV2Record(1, "2026-03-15T10:00:00Z"),
		makeTestV2Record(2, "2026-03-15T11:00:00Z"),
	})

	records, err := hw.ReadRecentV2(10, 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 2 {
		t.Errorf("expected 2 records, got %d", len(records))
	}
	if records[0].IssueNumber != 1 {
		t.Errorf("expected issue 1 first, got %d", records[0].IssueNumber)
	}
	if records[1].IssueNumber != 2 {
		t.Errorf("expected issue 2 second, got %d", records[1].IssueNumber)
	}
}

func TestReadRecentV2_MultipleFiles_OrderedChronologically(t *testing.T) {
	hw, dir := newTestHistoryWriter(t)
	writeTestJSONL(t, dir, "2026-03-14.jsonl", []V2RunRecord{
		makeTestV2Record(10, "2026-03-14T09:00:00Z"),
	})
	writeTestJSONL(t, dir, "2026-03-15.jsonl", []V2RunRecord{
		makeTestV2Record(20, "2026-03-15T10:00:00Z"),
	})

	records, err := hw.ReadRecentV2(10, 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 2 {
		t.Errorf("expected 2 records, got %d", len(records))
	}
	// Oldest file first → issue 10 before issue 20.
	if records[0].IssueNumber != 10 {
		t.Errorf("expected issue 10 first (chronological), got %d", records[0].IssueNumber)
	}
	if records[1].IssueNumber != 20 {
		t.Errorf("expected issue 20 second, got %d", records[1].IssueNumber)
	}
}

func TestReadRecentV2_LimitN(t *testing.T) {
	hw, dir := newTestHistoryWriter(t)
	writeTestJSONL(t, dir, "2026-03-15.jsonl", []V2RunRecord{
		makeTestV2Record(1, "2026-03-15T08:00:00Z"),
		makeTestV2Record(2, "2026-03-15T09:00:00Z"),
		makeTestV2Record(3, "2026-03-15T10:00:00Z"),
		makeTestV2Record(4, "2026-03-15T11:00:00Z"),
		makeTestV2Record(5, "2026-03-15T12:00:00Z"),
	})

	records, err := hw.ReadRecentV2(3, 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 3 {
		t.Errorf("expected 3 records (limit), got %d", len(records))
	}
	// Should be the last 3 (most recent).
	if records[0].IssueNumber != 3 {
		t.Errorf("expected issue 3, got %d", records[0].IssueNumber)
	}
	if records[2].IssueNumber != 5 {
		t.Errorf("expected issue 5 last, got %d", records[2].IssueNumber)
	}
}

func TestReadRecentV2_DaysBack(t *testing.T) {
	hw, dir := newTestHistoryWriter(t)
	// Write 3 files but only read 1 day back.
	writeTestJSONL(t, dir, "2026-03-13.jsonl", []V2RunRecord{
		makeTestV2Record(100, "2026-03-13T08:00:00Z"),
	})
	writeTestJSONL(t, dir, "2026-03-14.jsonl", []V2RunRecord{
		makeTestV2Record(200, "2026-03-14T08:00:00Z"),
	})
	writeTestJSONL(t, dir, "2026-03-15.jsonl", []V2RunRecord{
		makeTestV2Record(300, "2026-03-15T08:00:00Z"),
	})

	// daysBack=1 → only the most recent file (2026-03-15.jsonl).
	records, err := hw.ReadRecentV2(50, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 1 {
		t.Errorf("expected 1 record with daysBack=1, got %d", len(records))
	}
	if records[0].IssueNumber != 300 {
		t.Errorf("expected issue 300 (most recent day), got %d", records[0].IssueNumber)
	}
}

// TestReadRecentV2_PerStagePerformanceMode verifies that V2 records carrying
// a per-stage `performance_mode` round-trip through ReadRecentV2 (Issue #3215).
// Also pins backward-compat: a sibling record without the field on any stage
// must continue to parse cleanly into V2RunRecord (the omitempty tag yields
// an empty PerformanceMode field, which existing dashboards already treat as
// "mode unknown").
func TestReadRecentV2_PerStagePerformanceMode(t *testing.T) {
	hw, dir := newTestHistoryWriter(t)

	withMode := makeTestV2Record(31501, "2026-03-15T10:00:00Z")
	withMode.Stages = map[string]V2StageDetail{
		string(StageIssuePickup): {
			Status:          "complete",
			PerformanceMode: "elevated",
		},
		string(StageFeatureDev): {
			Status:          "complete",
			PerformanceMode: "maximum",
		},
	}

	legacy := makeTestV2Record(31502, "2026-03-15T11:00:00Z")
	legacy.Stages = map[string]V2StageDetail{
		string(StageIssuePickup): {Status: "complete"},
		string(StageFeatureDev):  {Status: "complete"},
	}

	writeTestJSONL(t, dir, "2026-03-15.jsonl", []V2RunRecord{withMode, legacy})

	records, err := hw.ReadRecentV2(10, 7)
	if err != nil {
		t.Fatalf("ReadRecentV2: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}

	got := records[0]
	if got.Stages[string(StageIssuePickup)].PerformanceMode != "elevated" {
		t.Errorf("issue-pickup PerformanceMode = %q, want %q",
			got.Stages[string(StageIssuePickup)].PerformanceMode, "elevated")
	}
	if got.Stages[string(StageFeatureDev)].PerformanceMode != "maximum" {
		t.Errorf("feature-dev PerformanceMode = %q, want %q",
			got.Stages[string(StageFeatureDev)].PerformanceMode, "maximum")
	}

	gotLegacy := records[1]
	if gotLegacy.Stages[string(StageIssuePickup)].PerformanceMode != "" {
		t.Errorf("legacy issue-pickup PerformanceMode = %q, want empty",
			gotLegacy.Stages[string(StageIssuePickup)].PerformanceMode)
	}
}

// TestReadRecentV2_PerStageAdapter verifies that V2 records carrying a
// per-stage `adapter` token field round-trip through ReadRecentV2 (Issue
// #3224). Also pins backward-compat: a sibling record without the field on
// any per-stage tokens entry parses cleanly into V2RunRecord — the omitempty
// tag yields an empty Adapter field, which dashboards already treat as
// "adapter unknown".
func TestReadRecentV2_PerStageAdapter(t *testing.T) {
	hw, dir := newTestHistoryWriter(t)

	withAdapter := makeTestV2Record(32241, "2026-05-07T10:00:00Z")
	withAdapter.Tokens.PerStage = map[string]V2StageTokens{
		string(StageIssuePickup): {
			Input:   1000,
			Output:  500,
			CostUSD: 0.05,
			Adapter: "claude",
		},
		string(StageFeatureDev): {
			Input:   2000,
			Output:  700,
			CostUSD: 0.12,
			Adapter: "gemini",
		},
	}

	legacy := makeTestV2Record(32242, "2026-05-07T11:00:00Z")
	legacy.Tokens.PerStage = map[string]V2StageTokens{
		string(StageIssuePickup): {Input: 1000, Output: 500, CostUSD: 0.05},
		string(StageFeatureDev):  {Input: 2000, Output: 700, CostUSD: 0.12},
	}

	writeTestJSONL(t, dir, "2026-05-07.jsonl", []V2RunRecord{withAdapter, legacy})

	records, err := hw.ReadRecentV2(10, 7)
	if err != nil {
		t.Fatalf("ReadRecentV2: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}

	got := records[0]
	if got.Tokens.PerStage[string(StageIssuePickup)].Adapter != "claude" {
		t.Errorf("issue-pickup Adapter = %q, want %q",
			got.Tokens.PerStage[string(StageIssuePickup)].Adapter, "claude")
	}
	if got.Tokens.PerStage[string(StageFeatureDev)].Adapter != "gemini" {
		t.Errorf("feature-dev Adapter = %q, want %q",
			got.Tokens.PerStage[string(StageFeatureDev)].Adapter, "gemini")
	}

	gotLegacy := records[1]
	if gotLegacy.Tokens.PerStage[string(StageIssuePickup)].Adapter != "" {
		t.Errorf("legacy issue-pickup Adapter = %q, want empty",
			gotLegacy.Tokens.PerStage[string(StageIssuePickup)].Adapter)
	}
}

func TestReadRecentV2_SkipsMalformedLines(t *testing.T) {
	hw, dir := newTestHistoryWriter(t)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	filePath := filepath.Join(dir, "2026-03-15.jsonl")
	content := []byte("{not valid json}\n")
	r := makeTestV2Record(42, "2026-03-15T10:00:00Z")
	data, _ := json.Marshal(r)
	content = append(content, append(data, '\n')...)
	content = append(content, []byte("also bad\n")...)
	os.WriteFile(filePath, content, 0644)

	records, err := hw.ReadRecentV2(10, 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 1 {
		t.Errorf("expected 1 valid record (malformed skipped), got %d", len(records))
	}
	if records[0].IssueNumber != 42 {
		t.Errorf("expected issue 42, got %d", records[0].IssueNumber)
	}
}
