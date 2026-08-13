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

// sizePairRows builds `n` rows whose size pair is measurable and either agrees
// or not, plus `filler` rows that measure nothing — the shape of a real corpus,
// where most rows carry one half of a pair at best.
func sizePairRows(n int, hit bool, base time.Time, offset int) []interface{} {
	var out []interface{}
	for i := 0; i < n; i++ {
		actual := "large"
		if hit {
			actual = "small"
		}
		out = append(out, map[string]interface{}{
			"predictedSize": "small", "actualSize": actual,
			"success": true, "completedAt": base.Add(time.Duration(offset+i) * time.Minute),
		})
	}
	return out
}

func unmeasurableRows(n int, base time.Time, offset int) []interface{} {
	var out []interface{}
	for i := 0; i < n; i++ {
		out = append(out, map[string]interface{}{
			"predictedSize": "small", "predictedModel": "",
			"success": true, "completedAt": base.Add(time.Duration(offset+i) * time.Minute),
		})
	}
	return out
}

// THE TREND MUST BE REACHABLE AT REAL DENSITY. The round-3 window walked
// backwards until it accumulated 10 measurable pairs, so on any corpus with
// ≤ ~20 measurable pairs it reached row 0, recentAccuracy was definitionally
// EQUAL to historicalAccuracy, and only `stalling` could ever be returned —
// +5 composite points for a verdict with no information in it.
//
// This is the reviewer's reproduction corpus: 20 rows, 10 measurable size
// pairs, the oldest 5 agreeing and the newest 5 not. A router that has
// regressed from perfect to useless must read as `degrading`.
func TestCalibration_RecentWindowDetectsARealRegressionAtCorpusDensity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".nightgauge", "pipeline", "history", "outcomes.jsonl")
	now := time.Now().UTC().Add(-24 * time.Hour)

	var records []interface{}
	records = append(records, sizePairRows(5, true, now, 0)...)
	records = append(records, unmeasurableRows(10, now, 5)...)
	records = append(records, sizePairRows(5, false, now, 15)...)
	writeJSONL(t, path, records)

	got := calibrationResult(t, dir)
	if got.Verdict != VerdictDegrading {
		t.Errorf("verdict = %s (%s) evidence %v, want %s — the newest 5 measurable predictions are all wrong and the oldest 5 were all right",
			got.Verdict, got.Reason, got.Evidence, VerdictDegrading)
	}
	if got.Evidence["historicalAccuracy"] != "50.0%" {
		t.Errorf("historicalAccuracy = %q, want 50.0%% (5 of 10 measurable pairs agree)", got.Evidence["historicalAccuracy"])
	}
	if got.Evidence["recentAccuracy"] != "0.0%" {
		t.Errorf("recentAccuracy = %q, want 0.0%% — the recent window must be a trailing SLICE, not the whole corpus",
			got.Evidence["recentAccuracy"])
	}
	if got.Evidence["recentAccuracy"] == got.Evidence["historicalAccuracy"] {
		t.Errorf("recent == historical (%q) — the window swallowed the corpus and the verdict cannot move",
			got.Evidence["recentAccuracy"])
	}
}

// The improving direction has to be reachable too, or the loop can only ever
// report bad news and stalling.
func TestCalibration_RecentWindowDetectsRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".nightgauge", "pipeline", "history", "outcomes.jsonl")
	now := time.Now().UTC().Add(-24 * time.Hour)

	var records []interface{}
	records = append(records, sizePairRows(5, false, now, 0)...)
	records = append(records, unmeasurableRows(10, now, 5)...)
	records = append(records, sizePairRows(5, true, now, 15)...)
	writeJSONL(t, path, records)

	got := calibrationResult(t, dir)
	if got.Verdict != VerdictClosing {
		t.Errorf("verdict = %s (%s) evidence %v, want %s", got.Verdict, got.Reason, got.Evidence, VerdictClosing)
	}
	if got.Evidence["recentAccuracy"] != "100.0%" {
		t.Errorf("recentAccuracy = %q, want 100.0%%", got.Evidence["recentAccuracy"])
	}
}

// Above 2×N the window is exactly the newest N comparisons — the cap only binds
// on a thin corpus.
func TestCalibration_RecentWindowIsTenComparisonsOnceTheCorpusAllows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".nightgauge", "pipeline", "history", "outcomes.jsonl")
	now := time.Now().UTC().Add(-24 * time.Hour)

	var records []interface{}
	records = append(records, sizePairRows(30, true, now, 0)...)
	records = append(records, sizePairRows(10, false, now, 30)...)
	writeJSONL(t, path, records)

	got := calibrationResult(t, dir)
	if got.Evidence["recentMeasuredPredictions"] != "10" {
		t.Errorf("recentMeasuredPredictions = %q, want 10", got.Evidence["recentMeasuredPredictions"])
	}
	if got.Evidence["measuredPredictions"] != "40" {
		t.Errorf("measuredPredictions = %q, want 40", got.Evidence["measuredPredictions"])
	}
	if got.Evidence["recentAccuracy"] != "0.0%" || got.Evidence["historicalAccuracy"] != "75.0%" {
		t.Errorf("historical/recent = %q/%q, want 75.0%%/0.0%%",
			got.Evidence["historicalAccuracy"], got.Evidence["recentAccuracy"])
	}
}

// A period can still have no measured size rows (legacy corpus or runs that
// never reached pr-create), so it must be reported as no-data rather than
// silently contributing nothing to a number labelled as if it covered size.
func TestCalibration_SizePairReportsNoDataWhenPeriodHasNoActualSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".nightgauge", "pipeline", "history", "outcomes.jsonl")
	now := time.Now().UTC()

	var records []interface{}
	for i := 0; i < 12; i++ {
		records = append(records, map[string]interface{}{
			"predictedSize":  "small", // written on every row; never scored
			"predictedModel": "sonnet", "actualModel": "sonnet",
			"success": true, "completedAt": now,
		})
	}
	writeJSONL(t, path, records)

	got := calibrationResult(t, dir)
	if got.Evidence["sizePairsMeasured"] != "0" {
		t.Errorf("sizePairsMeasured = %q, want 0", got.Evidence["sizePairsMeasured"])
	}
	if got.Evidence["modelPairsMeasured"] != "12" {
		t.Errorf("modelPairsMeasured = %q, want 12", got.Evidence["modelPairsMeasured"])
	}
	if got.Evidence["sizeCalibration"] == "" {
		t.Errorf("evidence %v does not say the size pair is unmeasurable — the published accuracy is 100%% model content under a label that reads as size/complexity accuracy",
			got.Evidence)
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
