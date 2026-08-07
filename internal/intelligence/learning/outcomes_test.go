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
	if report.SizeSamples != 10 || report.ModelSamples != 10 {
		t.Errorf("samples = size %d / model %d, want 10 / 10", report.SizeSamples, report.ModelSamples)
	}
	if report.SizeAccuracy == nil || *report.SizeAccuracy != 1.0 {
		t.Errorf("size accuracy = %v, want 1.0", report.SizeAccuracy)
	}
	if report.ModelAccuracy == nil || *report.ModelAccuracy != 1.0 {
		t.Errorf("model accuracy = %v, want 1.0", report.ModelAccuracy)
	}
	if report.AvgCostPerRun != 0.50 {
		t.Errorf("avg cost = %f, want 0.50", report.AvgCostPerRun)
	}
}

// GUARDED DENOMINATORS (#304). A row counts toward an accuracy only when BOTH
// halves of that pair are present. Without the guard, the corpus's own
// conventions made both numbers lie in opposite directions at once: rows whose
// model fields are both "" scored a HIT (""==""), and rows with an unmeasurable
// actual size scored a MISS.
//
// The eight rows this repo's real corpus contained before the fix are exactly
// the degenerate shape below — which is why mixed old/new history needs no
// discriminator field: the guard excludes legacy rows because their halves are
// precisely the ones that are empty.
func TestCalibrate_CountsOnlyRowsWithBothHalves(t *testing.T) {
	dir := t.TempDir()
	r := &Recorder{filePath: filepath.Join(dir, "outcomes.jsonl")}

	// 3 legacy-shaped rows: no models at all, a fabricated predicted size, no
	// actual size. They measure nothing and must move neither number.
	for i := 0; i < 3; i++ {
		if err := r.Record(Outcome{
			IssueNumber:   i + 1,
			PredictedSize: "small",
			CompletedAt:   time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 2 measurable model predictions, one right and one wrong.
	if err := r.Record(Outcome{IssueNumber: 4, PredictedModel: "sonnet", ActualModel: "sonnet", CompletedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := r.Record(Outcome{IssueNumber: 5, PredictedModel: "sonnet", ActualModel: "opus", CompletedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	report, err := r.Calibrate()
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalRuns != 5 {
		t.Errorf("TotalRuns = %d, want 5 — every row still counts as a run", report.TotalRuns)
	}
	if report.ModelSamples != 2 {
		t.Errorf("ModelSamples = %d, want 2 — the three model-less rows measured nothing", report.ModelSamples)
	}
	if report.ModelAccuracy == nil || *report.ModelAccuracy != 0.5 {
		t.Errorf("ModelAccuracy = %v, want 0.5 (1 of 2 measurable) — counting \"\"==\"\" as a hit reported 8 pre-#304 rows as 100%% accurate routing", report.ModelAccuracy)
	}
	if report.SizeSamples != 0 {
		t.Errorf("SizeSamples = %d, want 0 — no row carries both halves of the size pair", report.SizeSamples)
	}
	if report.SizeAccuracy != nil {
		t.Errorf("SizeAccuracy = %v, want nil — \"nothing measurable\" must not be reported as \"measured 0%%\"; `learn tune` optimizes against this number", report.SizeAccuracy)
	}
}

// An unmeasurable half must not drag the number down. A corpus whose router is
// PERFECT on every measurable row still reported a near-zero accuracy when most
// rows simply lacked one half, and the loop then chased label hygiene as if it
// were routing error.
func TestCalibrate_UnmeasurableRowsDoNotDilute(t *testing.T) {
	dir := t.TempDir()
	r := &Recorder{filePath: filepath.Join(dir, "outcomes.jsonl")}

	for i := 0; i < 2; i++ {
		if err := r.Record(Outcome{
			IssueNumber: i + 1, PredictedSize: "medium", ActualSize: "medium",
			PredictedModel: "opus", ActualModel: "opus", CompletedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 18; i++ {
		if err := r.Record(Outcome{
			IssueNumber: i + 3, PredictedModel: "sonnet", CompletedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	report, err := r.Calibrate()
	if err != nil {
		t.Fatal(err)
	}
	if report.SizeAccuracy == nil || *report.SizeAccuracy != 1.0 {
		t.Errorf("SizeAccuracy = %v over %d samples, want 1.0 over 2 — 18 rows with no actual size must be excluded, not counted as misses",
			report.SizeAccuracy, report.SizeSamples)
	}
	if report.ModelAccuracy == nil || *report.ModelAccuracy != 1.0 {
		t.Errorf("ModelAccuracy = %v over %d samples, want 1.0 over 2", report.ModelAccuracy, report.ModelSamples)
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
