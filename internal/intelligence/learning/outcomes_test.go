package learning

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
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

func TestRecorder_AttributesActualSizeToMatchingConcurrentRun(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, ".nightgauge", "pipeline")
	completedAt := time.Now().UTC()

	target := state.NewRuntimeState("acme/widget", 369, "item-old", "01900130-0000-7000-8000-000000000369")
	target.StartedAt = completedAt.Add(-10 * time.Minute)
	target.InputTokens, target.OutputTokens, target.TotalCostUSD = 111, 22, 0.369
	target.SetActualLinesChanged(1000) // default L -> learning medium
	if err := target.Persist(stateDir); err != nil {
		t.Fatalf("persist target: %v", err)
	}

	newerOtherRun := state.NewRuntimeState("acme/widget", 369, "item-new", "01900130-0000-7000-8000-000000000370")
	newerOtherRun.StartedAt = completedAt.Add(-time.Minute)
	newerOtherRun.InputTokens, newerOtherRun.OutputTokens, newerOtherRun.TotalCostUSD = 333, 44, 0.111
	newerOtherRun.SetActualLinesChanged(10) // small; choosing latest would be wrong
	if err := newerOtherRun.Persist(stateDir); err != nil {
		t.Fatalf("persist concurrent run: %v", err)
	}

	r := NewRecorder(root)
	if err := r.Record(Outcome{
		IssueNumber: 369, Repo: "acme/widget", DurationMs: int64((10 * time.Minute) / time.Millisecond),
		InputTokens: 111, OutputTokens: 22, CostUSD: 0.369, CompletedAt: completedAt,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	loaded, err := r.LoadAll()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("LoadAll = %v, %v; want one row", loaded, err)
	}
	if loaded[0].ActualSize != "medium" {
		t.Errorf("ActualSize = %q, want medium from the matching run; latest issue snapshot belonged to another run", loaded[0].ActualSize)
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

// A retry escalation is not a routing miss. resolveDispatchModel applies the
// retry engine's escalated tier as the dispatch override, and that override is
// re-recorded as ActualModel — so a haiku->sonnet escalation on a failed stage
// (the retry ladder doing exactly its job) produced a pair that read
// indistinguishable from the router simply mispredicting. Retries > 0 is the
// discriminator: it is a superset of "the tier actually changed" (a retry with
// no tier change also gets excluded), accepted deliberately because erring
// toward fewer false misses beats erring toward inflated ones (issue #1002).
func TestCalibrate_RetriedRowIsNotAModelMiss(t *testing.T) {
	dir := t.TempDir()
	r := &Recorder{filePath: filepath.Join(dir, "outcomes.jsonl")}

	if err := r.Record(Outcome{
		IssueNumber: 1, PredictedModel: "haiku", ActualModel: "sonnet",
		Retries: 1, CompletedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Record(Outcome{
		IssueNumber: 2, PredictedModel: "haiku", ActualModel: "sonnet",
		Retries: 0, CompletedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	report, err := r.Calibrate()
	if err != nil {
		t.Fatal(err)
	}
	if report.ModelSamples != 1 {
		t.Errorf("ModelSamples = %d, want 1 — the retried row must not enter the denominator", report.ModelSamples)
	}
	if report.ModelMatches != 0 {
		t.Errorf("ModelMatches = %d, want 0 — the surviving row is a genuine haiku!=sonnet miss", report.ModelMatches)
	}
	if report.ModelSamplesExcludedRetry != 1 {
		t.Errorf("ModelSamplesExcludedRetry = %d, want 1 — the excluded row must be visible, not silently dropped", report.ModelSamplesExcludedRetry)
	}
}

// Guards against the fix over-excluding: a non-retry override (a floor,
// ceiling, or sticky-downgrade clamp applied with no retry involved) still
// changes PredictedModel vs ActualModel with Retries == 0, and that is a real
// divergence the corpus must still count — Retries > 0 is the only exclusion
// signal, not "predicted != actual" in general.
func TestCalibrate_NonRetryOverrideStillBooksDivergence(t *testing.T) {
	dir := t.TempDir()
	r := &Recorder{filePath: filepath.Join(dir, "outcomes.jsonl")}

	if err := r.Record(Outcome{
		IssueNumber: 1, PredictedModel: "opus", ActualModel: "sonnet",
		Retries: 0, CompletedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	report, err := r.Calibrate()
	if err != nil {
		t.Fatal(err)
	}
	if report.ModelSamples != 1 {
		t.Errorf("ModelSamples = %d, want 1 — a non-retry override must still be measured", report.ModelSamples)
	}
	if report.ModelMatches != 0 {
		t.Errorf("ModelMatches = %d, want 0", report.ModelMatches)
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

// The schema marker is the band-retirement cutover discriminator (#582, spike
// #568 §5): Record stamps it on the WIRE (the raw JSONL line), callers cannot
// override it, and a legacy line without the key loads as "" — absence is the
// legacy signal, never fabricated into a value.
func TestRecorder_RecordStampsSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	r := &Recorder{filePath: filepath.Join(dir, "outcomes.jsonl")}

	// A caller-supplied value must not survive: the writer is the authority.
	if err := r.Record(Outcome{IssueNumber: 1, SchemaVersion: "bogus", CompletedAt: time.Now()}); err != nil {
		t.Fatalf("record: %v", err)
	}

	data, err := os.ReadFile(r.filePath)
	if err != nil {
		t.Fatalf("read raw corpus: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw line: %v", err)
	}
	if got := raw["schema_version"]; got != OutcomeSchemaVersion {
		t.Errorf("schema_version on the wire = %v, want %q", got, OutcomeSchemaVersion)
	}

	// A legacy (pre-cutover) line has no marker; it must load as "" — the
	// absent-means-legacy contract — not error and not invent a version.
	legacy := []byte(`{"issueNumber":2,"repo":"a/b","predictedSize":"","predictedModel":"","actualModel":"","success":true,"durationMs":1,"inputTokens":1,"outputTokens":1,"costUsd":0,"complexityScore":0,"retries":0,"completedAt":"2025-01-01T00:00:00Z"}` + "\n")
	f, err := os.OpenFile(r.filePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open for legacy append: %v", err)
	}
	if _, err := f.Write(legacy); err != nil {
		t.Fatalf("append legacy line: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	loaded, err := r.LoadAll()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded = %d, want 2", len(loaded))
	}
	if loaded[0].SchemaVersion != OutcomeSchemaVersion {
		t.Errorf("stamped row loads SchemaVersion = %q, want %q", loaded[0].SchemaVersion, OutcomeSchemaVersion)
	}
	if loaded[1].SchemaVersion != "" {
		t.Errorf("legacy row loads SchemaVersion = %q, want empty", loaded[1].SchemaVersion)
	}
}
