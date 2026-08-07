// Guarded denominators for the calibration loop (#304).
//
// The corpus writes "" for an unknown predicted/actual half. This loop used to
// divide its match count by the ROW count, so every row with an empty half was
// counted as a routing MISS. That is not a rounding error: ~95% of real issues
// carry no size:* label, so the reported accuracy tracked label hygiene rather
// than routing quality, and the loop could publish `degrading` against a corpus
// whose router was perfect.
package loopverdicts

import (
	"path/filepath"
	"testing"
	"time"
)

func calibrationResult(t *testing.T, dir string) LoopResult {
	t.Helper()
	report, err := Analyze(AnalyzeInput{WorkspaceRoot: dir, Period: 30})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	for _, l := range report.Loops {
		if l.Loop == "calibration" {
			return l
		}
	}
	t.Fatal("calibration loop not found in report")
	return LoopResult{}
}

// A PERFECT router whose ten most recent issues merely lack a measurable half
// must not read as a regression. Pre-guard this flipped the verdict to
// `degrading` (66.7% → 0.0%) and fed that into the composite score.
func TestCalibration_UnmeasurableRecentRowsDoNotDegradeAPerfectRouter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".nightgauge", "pipeline", "history", "outcomes.jsonl")
	now := time.Now().UTC()

	var records []interface{}
	// 12 measurable rows, every one correct.
	for i := 0; i < 12; i++ {
		records = append(records, map[string]interface{}{
			"predictedModel": "sonnet", "actualModel": "sonnet",
			"success": true, "completedAt": now.Add(-time.Duration(20-i) * time.Hour),
		})
	}
	// …then 10 more recent runs on issues with no routing prediction at all.
	for i := 0; i < 10; i++ {
		records = append(records, map[string]interface{}{
			"predictedSize": "small", "predictedModel": "",
			"success": true, "completedAt": now.Add(-time.Duration(8-i) * time.Hour),
		})
	}
	writeJSONL(t, path, records)

	got := calibrationResult(t, dir)
	if got.Verdict == VerdictDegrading {
		t.Errorf("verdict = %s (%s) with evidence %v — the router got every measurable prediction right; only unlabelled issues arrived recently",
			got.Verdict, got.Reason, got.Evidence)
	}
	if got.Evidence["historicalAccuracy"] != "100.0%" {
		t.Errorf("historicalAccuracy = %q, want 100.0%% — the 10 unmeasurable rows must be excluded, not counted as misses",
			got.Evidence["historicalAccuracy"])
	}
	if got.Evidence["measuredPredictions"] != "12" {
		t.Errorf("measuredPredictions = %q, want 12 — the denominator is measurable pairs, not rows",
			got.Evidence["measuredPredictions"])
	}
}

// Nothing measurable is NO DATA, not "accuracy 0". The eight rows this repo's
// real corpus held before #304 are exactly this shape: no models, a fabricated
// predicted size, no actual size.
func TestCalibration_NoMeasurablePairsIsNoData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".nightgauge", "pipeline", "history", "outcomes.jsonl")
	now := time.Now().UTC()

	var records []interface{}
	for i := 0; i < 15; i++ {
		records = append(records, map[string]interface{}{
			"predictedSize": "small", "predictedModel": "", "actualModel": "",
			"success": i%2 == 0, "completedAt": now,
		})
	}
	writeJSONL(t, path, records)

	got := calibrationResult(t, dir)
	if got.Verdict != VerdictNoData {
		t.Errorf("verdict = %s (%s), want %s — a number computed from rows that measured nothing is worse than no data, because the reader stops saying \"bootstrapping\"",
			got.Verdict, got.Reason, VerdictNoData)
	}
	if _, ok := got.Evidence["historicalAccuracy"]; ok {
		t.Errorf("evidence reports an accuracy (%v) for a corpus with no measurable pair", got.Evidence)
	}
}

// A genuinely mis-routed run must still register as a miss — the guard excludes
// unmeasurable rows, it does not excuse wrong ones.
func TestCalibration_MeasurableMissesStillCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".nightgauge", "pipeline", "history", "outcomes.jsonl")
	now := time.Now().UTC()

	var records []interface{}
	for i := 0; i < 6; i++ {
		records = append(records, map[string]interface{}{
			"predictedModel": "sonnet", "actualModel": "sonnet",
			"success": true, "completedAt": now,
		})
	}
	for i := 0; i < 6; i++ {
		records = append(records, map[string]interface{}{
			"predictedModel": "sonnet", "actualModel": "opus",
			"success": true, "completedAt": now,
		})
	}
	writeJSONL(t, path, records)

	got := calibrationResult(t, dir)
	if got.Evidence["historicalAccuracy"] != "50.0%" {
		t.Errorf("historicalAccuracy = %q, want 50.0%% (6 of 12 measurable pairs agree)", got.Evidence["historicalAccuracy"])
	}
}
