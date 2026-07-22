package learning

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecorder_RecordAndLoad(t *testing.T) {
	dir := t.TempDir()
	r := &Recorder{filePath: filepath.Join(dir, "outcomes.jsonl")}

	outcomes := []Outcome{
		{
			IssueNumber:    42,
			Repo:           "nightgauge/test",
			PredictedSize:  "M",
			ActualSize:     "M",
			PredictedModel: "claude-sonnet-4-6",
			ActualModel:    "claude-sonnet-4-6",
			Success:        true,
			DurationMs:     300_000,
			InputTokens:    10000,
			OutputTokens:   5000,
			CostUSD:        0.50,
			CompletedAt:    time.Now(),
		},
		{
			IssueNumber:    43,
			Repo:           "nightgauge/test",
			PredictedSize:  "S",
			ActualSize:     "M",
			PredictedModel: "claude-haiku-4-5-20251001",
			ActualModel:    "claude-sonnet-4-6",
			Success:        false,
			CompletedAt:    time.Now(),
		},
	}

	for _, o := range outcomes {
		if err := r.Record(o); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	loaded, err := r.LoadAll()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded = %d, want 2", len(loaded))
	}
	if loaded[0].IssueNumber != 42 {
		t.Errorf("first issue = %d, want 42", loaded[0].IssueNumber)
	}
	if loaded[1].Success {
		t.Error("second outcome should be failure")
	}
}

func TestRecorder_LoadAll_NoFile(t *testing.T) {
	r := &Recorder{filePath: filepath.Join(t.TempDir(), "nonexistent.jsonl")}
	loaded, err := r.LoadAll()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("loaded = %d, want 0", len(loaded))
	}
}

func TestRecorder_Calibrate(t *testing.T) {
	dir := t.TempDir()
	r := &Recorder{filePath: filepath.Join(dir, "outcomes.jsonl")}

	// Record 10 outcomes with mixed results
	for i := 0; i < 10; i++ {
		o := Outcome{
			IssueNumber:    i + 1,
			PredictedSize:  "M",
			ActualSize:     "M",
			PredictedModel: "claude-sonnet-4-6",
			ActualModel:    "claude-sonnet-4-6",
			Success:        i%3 != 0, // 70% success rate
			CostUSD:        0.50,
			CompletedAt:    time.Now(),
		}
		if err := r.Record(o); err != nil {
			t.Fatal(err)
		}
	}

	report, err := r.Calibrate()
	if err != nil {
		t.Fatal(err)
	}

	if report.TotalRuns != 10 {
		t.Errorf("total runs = %d, want 10", report.TotalRuns)
	}
	if report.SizeAccuracy != 1.0 {
		t.Errorf("size accuracy = %f, want 1.0", report.SizeAccuracy)
	}
	if report.ModelAccuracy != 1.0 {
		t.Errorf("model accuracy = %f, want 1.0", report.ModelAccuracy)
	}
	if report.AvgCostPerRun != 0.50 {
		t.Errorf("avg cost = %f, want 0.50", report.AvgCostPerRun)
	}
}

func TestRecorder_CreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub", "dir")
	r := &Recorder{filePath: filepath.Join(dir, "outcomes.jsonl")}

	err := r.Record(Outcome{IssueNumber: 1, CompletedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}

	// Directory should have been created
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Error("directory not created")
	}
}
