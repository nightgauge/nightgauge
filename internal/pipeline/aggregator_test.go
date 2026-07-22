package pipeline

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// strPtr is a helper to convert string literals to *string for fixture
// V2RunRecord fields (Size, Type, Priority).
func strPtr(s string) *string { return &s }

// fixtureRecord builds a V2RunRecord with sensible defaults; tests override
// individual fields. RecordType defaults to "run" so the record passes the
// defensive filter in Aggregate.
func fixtureRecord(issue int, started, recorded string) state.V2RunRecord {
	return state.V2RunRecord{
		SchemaVersion: "2",
		RecordType:    "run",
		IssueNumber:   issue,
		StartedAt:     started,
		RecordedAt:    recorded,
		Outcome:       "complete",
		Stages:        map[string]state.V2StageDetail{},
		Tokens: state.V2Tokens{
			PerStage: map[string]state.V2StageTokens{},
		},
	}
}

// withStage attaches a stage detail to a record (helper for chained fixture
// construction).
func withStage(r state.V2RunRecord, stage, status string, durationMs int64) state.V2RunRecord {
	r.Stages[stage] = state.V2StageDetail{
		Status:     status,
		DurationMs: durationMs,
	}
	return r
}

// withStageModel adds a stage with model_selection populated.
func withStageModel(r state.V2RunRecord, stage, status string, durationMs int64, model, source string) state.V2RunRecord {
	r.Stages[stage] = state.V2StageDetail{
		Status:         status,
		DurationMs:     durationMs,
		ModelSelection: &state.V2ModelSelect{Model: model, Source: source},
	}
	return r
}

// withStageTokens attaches per-stage token counts.
func withStageTokens(r state.V2RunRecord, stage string, input, output, cacheRead, cacheCreation int) state.V2RunRecord {
	r.Tokens.PerStage[stage] = state.V2StageTokens{
		Input:         input,
		Output:        output,
		CacheRead:     cacheRead,
		CacheCreation: cacheCreation,
	}
	return r
}

func TestAggregate_RunsLimitTrimsLastN(t *testing.T) {
	records := []state.V2RunRecord{
		fixtureRecord(1, "2026-04-20T10:00:00Z", "2026-04-20T10:01:00Z"),
		fixtureRecord(2, "2026-04-21T10:00:00Z", "2026-04-21T10:01:00Z"),
		fixtureRecord(3, "2026-04-22T10:00:00Z", "2026-04-22T10:01:00Z"),
		fixtureRecord(4, "2026-04-23T10:00:00Z", "2026-04-23T10:01:00Z"),
		fixtureRecord(5, "2026-04-24T10:00:00Z", "2026-04-24T10:01:00Z"),
	}
	res, _ := Aggregate(records, Options{Runs: 3})
	if res.RunsAnalyzed != 3 {
		t.Errorf("RunsAnalyzed = %d, want 3", res.RunsAnalyzed)
	}
	if len(res.Runs) != 3 {
		t.Fatalf("len(Runs) = %d, want 3", len(res.Runs))
	}
	gotIssues := []int{res.Runs[0].IssueNumber, res.Runs[1].IssueNumber, res.Runs[2].IssueNumber}
	want := []int{3, 4, 5}
	for i := range gotIssues {
		if gotIssues[i] != want[i] {
			t.Errorf("Runs[%d].IssueNumber = %d, want %d", i, gotIssues[i], want[i])
		}
	}
	if res.DateFrom != "2026-04-22" || res.DateTo != "2026-04-24" {
		t.Errorf("date range = %q..%q, want 2026-04-22..2026-04-24", res.DateFrom, res.DateTo)
	}
}

func TestAggregate_IssueFilter(t *testing.T) {
	records := []state.V2RunRecord{
		fixtureRecord(100, "2026-04-22T10:00:00Z", "2026-04-22T10:01:00Z"),
		fixtureRecord(200, "2026-04-22T11:00:00Z", "2026-04-22T11:01:00Z"),
		fixtureRecord(100, "2026-04-22T12:00:00Z", "2026-04-22T12:01:00Z"),
	}
	res, _ := Aggregate(records, Options{Issue: 100})
	if res.RunsAnalyzed != 2 {
		t.Errorf("RunsAnalyzed = %d, want 2", res.RunsAnalyzed)
	}
	for _, r := range res.Runs {
		if r.IssueNumber != 100 {
			t.Errorf("found IssueNumber=%d in filtered output", r.IssueNumber)
		}
	}
}

func TestAggregate_EmptyInput(t *testing.T) {
	res, warnings := Aggregate(nil, Options{})
	if res.V != SchemaVersion {
		t.Errorf("V = %d, want %d", res.V, SchemaVersion)
	}
	if res.RunsAnalyzed != 0 {
		t.Errorf("RunsAnalyzed = %d, want 0", res.RunsAnalyzed)
	}
	if len(res.Runs) != 0 {
		t.Errorf("len(Runs) = %d, want 0", len(res.Runs))
	}
	if len(res.StageMetrics) != len(StageNames) {
		t.Errorf("StageMetrics has %d entries, want %d", len(res.StageMetrics), len(StageNames))
	}
	for _, stage := range StageNames {
		if _, ok := res.StageMetrics[stage]; !ok {
			t.Errorf("StageMetrics[%s] missing", stage)
		}
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want []", warnings)
	}
	// Should not panic on JSON marshal.
	if _, err := json.Marshal(res); err != nil {
		t.Errorf("json.Marshal: %v", err)
	}
}

func TestAggregate_StageDurations(t *testing.T) {
	// Build five records with known feature-dev durations: 1000, 2000, 3000,
	// 4000, 5000 ms. Hand-computed: median=3000, p90=4600 (linear
	// interpolation: rank=3.6 → 4000+0.6*(5000-4000)=4600), min=1000, max=5000,
	// mean=3000.
	records := []state.V2RunRecord{}
	for i, dur := range []int64{1000, 2000, 3000, 4000, 5000} {
		r := fixtureRecord(i+1, "2026-04-22T10:00:00Z", "2026-04-22T10:00:00Z")
		r = withStage(r, "feature-dev", "complete", dur)
		records = append(records, r)
	}
	res, _ := Aggregate(records, Options{})
	stats := res.StageMetrics["feature-dev"].DurationStats
	if stats.Count != 5 {
		t.Errorf("Count = %d, want 5", stats.Count)
	}
	if math.Abs(stats.Median-3000) > 0.001 {
		t.Errorf("Median = %v, want 3000", stats.Median)
	}
	if math.Abs(stats.P90-4600) > 0.001 {
		t.Errorf("P90 = %v, want 4600", stats.P90)
	}
	if stats.Min != 1000 || stats.Max != 5000 {
		t.Errorf("Min/Max = %v/%v, want 1000/5000", stats.Min, stats.Max)
	}
	if math.Abs(stats.Mean-3000) > 0.001 {
		t.Errorf("Mean = %v, want 3000", stats.Mean)
	}
}

func TestAggregate_ZeroDurationSkippedFromStatsButCountedInStatus(t *testing.T) {
	records := []state.V2RunRecord{
		withStage(fixtureRecord(1, "2026-04-22T10:00:00Z", "t1"), "feature-dev", "skipped", 0),
		withStage(fixtureRecord(2, "2026-04-22T11:00:00Z", "t2"), "feature-dev", "complete", 1000),
		withStage(fixtureRecord(3, "2026-04-22T12:00:00Z", "t3"), "feature-dev", "complete", 3000),
	}
	res, _ := Aggregate(records, Options{})
	stats := res.StageMetrics["feature-dev"].DurationStats
	if stats.Count != 2 {
		t.Errorf("DurationStats.Count = %d, want 2 (skipped excluded)", stats.Count)
	}
	if got := res.StageMetrics["feature-dev"].Status["skipped"]; got != 1 {
		t.Errorf("Status[skipped] = %d, want 1", got)
	}
	if got := res.StageMetrics["feature-dev"].Status["complete"]; got != 2 {
		t.Errorf("Status[complete] = %d, want 2", got)
	}
	// SkippedStages list on the run with duration=0 must include feature-dev.
	if !contains(res.Runs[0].SkippedStages, "feature-dev") {
		t.Errorf("Runs[0].SkippedStages = %v, want to contain feature-dev", res.Runs[0].SkippedStages)
	}
}

func TestAggregate_PerStageTokens(t *testing.T) {
	r1 := withStageTokens(withStage(fixtureRecord(1, "2026-04-22T10:00:00Z", "t1"),
		"feature-dev", "complete", 1000), "feature-dev", 100, 50, 1000, 200)
	r2 := withStageTokens(withStage(fixtureRecord(2, "2026-04-22T11:00:00Z", "t2"),
		"feature-dev", "complete", 2000), "feature-dev", 300, 150, 3000, 400)
	res, _ := Aggregate([]state.V2RunRecord{r1, r2}, Options{})
	ts := res.StageMetrics["feature-dev"].TokenStats
	if ts.Input.Count != 2 || ts.Input.Min != 100 || ts.Input.Max != 300 {
		t.Errorf("Input stats = %+v", ts.Input)
	}
	if ts.Output.Count != 2 || ts.Output.Min != 50 || ts.Output.Max != 150 {
		t.Errorf("Output stats = %+v", ts.Output)
	}
	if ts.CacheRead.Count != 2 || ts.CacheRead.Min != 1000 || ts.CacheRead.Max != 3000 {
		t.Errorf("CacheRead stats = %+v", ts.CacheRead)
	}
	if ts.CacheCreation.Count != 2 || ts.CacheCreation.Min != 200 || ts.CacheCreation.Max != 400 {
		t.Errorf("CacheCreation stats = %+v", ts.CacheCreation)
	}
}

func TestAggregate_ModelUsage(t *testing.T) {
	records := []state.V2RunRecord{
		withStageModel(fixtureRecord(1, "2026-04-22T10:00:00Z", "t1"),
			"feature-dev", "complete", 1000, "claude-sonnet-4-6", "auto"),
		withStageModel(fixtureRecord(2, "2026-04-22T11:00:00Z", "t2"),
			"feature-dev", "complete", 2000, "claude-sonnet-4-6", "config"),
		withStageModel(fixtureRecord(3, "2026-04-22T12:00:00Z", "t3"),
			"feature-dev", "complete", 3000, "claude-opus-4-7", "auto"),
	}
	res, _ := Aggregate(records, Options{})
	byStage := res.ModelUsage.ByStage["feature-dev"]
	if byStage["claude-sonnet-4-6"] != 2 || byStage["claude-opus-4-7"] != 1 {
		t.Errorf("ByStage[feature-dev] = %v", byStage)
	}
	bySource := res.ModelUsage.BySource["feature-dev"]
	if bySource["auto"] != 2 || bySource["config"] != 1 {
		t.Errorf("BySource[feature-dev] = %v", bySource)
	}
	models := res.StageMetrics["feature-dev"].Models
	if models["claude-sonnet-4-6"] != 2 {
		t.Errorf("StageMetrics.Models[claude-sonnet-4-6] = %d, want 2", models["claude-sonnet-4-6"])
	}
}

func TestAggregate_SkippedStagesPerRun(t *testing.T) {
	r := fixtureRecord(42, "2026-04-22T10:00:00Z", "t1")
	r = withStage(r, "issue-pickup", "complete", 1000)
	r = withStage(r, "feature-dev", "skipped", 0)
	// pr-merge stage missing entirely
	res, _ := Aggregate([]state.V2RunRecord{r}, Options{})
	skipped := res.Runs[0].SkippedStages
	if !contains(skipped, "feature-dev") {
		t.Errorf("SkippedStages = %v, want feature-dev present (duration=0)", skipped)
	}
	if !contains(skipped, "pr-merge") {
		t.Errorf("SkippedStages = %v, want pr-merge present (stage absent)", skipped)
	}
	if contains(skipped, "issue-pickup") {
		t.Errorf("SkippedStages = %v, must NOT contain issue-pickup (it ran)", skipped)
	}
}

func TestAggregate_DoesNotRecomputeCacheHitRate(t *testing.T) {
	// Regression guard: ensure aggregator does NOT expose its own cache hit
	// rate. The TS dashboard already provides V2StageTokens.CacheHitRate;
	// drift between Python copies of the audit aggregator is the failure mode
	// this verb exists to prevent.
	res, _ := Aggregate(nil, Options{})
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if contains([]string{string(data)}, "cache_hit_rate") || contains([]string{string(data)}, "cacheHitRate") {
		t.Errorf("aggregator output contains a cache_hit_rate field — must not recompute it")
	}
	// substring check, since contains() is exact-match
	if jsonContains(data, "cache_hit_rate") {
		t.Errorf("aggregator output contains cache_hit_rate substring")
	}
}

func TestAggregate_AnalysisBlock_SizeBaselinesAndAccuracy(t *testing.T) {
	// Build six records: three S-sized at $0.50/$1.00/$1.50, three M-sized at
	// $3.00/$4.00/$5.00. Median S = $1.00, median M = $4.00. Within 0.5x–2x
	// band around median: S band [$0.50, $2.00] catches all three S; M band
	// [$2.00, $8.00] catches all three M.
	build := func(issue int, size string, cost float64, dur int64, started string) state.V2RunRecord {
		r := fixtureRecord(issue, started, started)
		r.Size = strPtr(size)
		r.TotalDuration = dur
		r.Tokens.EstimatedCostUSD = cost
		return r
	}
	records := []state.V2RunRecord{
		build(1, "S", 0.50, 1000, "2026-04-20T10:00:00Z"),
		build(2, "S", 1.00, 2000, "2026-04-21T10:00:00Z"),
		build(3, "S", 1.50, 3000, "2026-04-22T10:00:00Z"),
		build(4, "M", 3.00, 5000, "2026-04-23T10:00:00Z"),
		build(5, "M", 4.00, 6000, "2026-04-24T10:00:00Z"),
		build(6, "M", 5.00, 7000, "2026-04-25T10:00:00Z"),
	}
	res, _ := Aggregate(records, Options{IncludeAnalysis: true})
	if res.Analysis == nil {
		t.Fatal("Analysis = nil, want populated when IncludeAnalysis=true")
	}
	a := res.Analysis
	bS := a.SizeBaselines["S"]
	if bS.Count != 3 || math.Abs(bS.MedianCost-1.0) > 0.001 {
		t.Errorf("S baseline = %+v", bS)
	}
	bM := a.SizeBaselines["M"]
	if bM.Count != 3 || math.Abs(bM.MedianCost-4.0) > 0.001 {
		t.Errorf("M baseline = %+v", bM)
	}
	if a.SizeAccuracyRates["S"].Total != 3 || a.SizeAccuracyRates["S"].WithinRange != 3 {
		t.Errorf("S accuracy = %+v, want 3/3", a.SizeAccuracyRates["S"])
	}
	if a.SizeAccuracyRates["M"].Total != 3 || a.SizeAccuracyRates["M"].WithinRange != 3 {
		t.Errorf("M accuracy = %+v, want 3/3", a.SizeAccuracyRates["M"])
	}
	if a.RunsWithSize != 6 || a.RunsWithoutSize != 0 {
		t.Errorf("size counts: with=%d without=%d, want 6/0", a.RunsWithSize, a.RunsWithoutSize)
	}
}

func TestAggregate_AnalysisBlock_OversizedAndUndersizedBoundaries(t *testing.T) {
	// Establish robust baselines for both S and M (3 records each), then add
	// outliers whose own contribution does not flip the labeled<->bracket
	// comparison. Boundary case at cost = 2× S median = $2.00 verifies the
	// inclusive upper edge of the bracket-detection band.
	build := func(issue int, size string, cost float64, started string) state.V2RunRecord {
		r := fixtureRecord(issue, started, started)
		r.Size = strPtr(size)
		r.Tokens.EstimatedCostUSD = cost
		r.Title = "test issue " + size
		return r
	}
	records := []state.V2RunRecord{
		// S baseline: median $1.00 → band [$0.50, $2.00]
		build(1, "S", 0.50, "2026-04-20T10:00:00Z"),
		build(2, "S", 1.00, "2026-04-21T10:00:00Z"),
		build(3, "S", 1.50, "2026-04-22T10:00:00Z"),
		// M baseline: median $4.00 → band [$2.00, $8.00]
		build(4, "M", 3.00, "2026-04-23T10:00:00Z"),
		build(5, "M", 4.00, "2026-04-24T10:00:00Z"),
		build(6, "M", 5.00, "2026-04-25T10:00:00Z"),
		// Oversized: labeled M (order 2), cost $1 → falls into S band (order 1).
		// Adding to M baseline shifts median to (1+3+4+5)/4 sorted median = 3.5
		// with band [1.75, 7.0]; $1 still misses M, falls into S band [0.5, 2].
		build(7, "M", 1.00, "2026-04-26T10:00:00Z"),
		// Undersized: labeled S (order 1), cost $4 → falls into M band (order 2).
		// Adding to S baseline: sorted [0.5, 1.0, 1.5, 4.0], median = 1.25,
		// band [0.625, 2.5]; $4 misses S, hits M band [2.0, 8.0].
		build(8, "S", 4.00, "2026-04-27T10:00:00Z"),
	}
	res, _ := Aggregate(records, Options{IncludeAnalysis: true})
	a := res.Analysis
	if len(a.Oversized) != 1 || a.Oversized[0].IssueNumber != 7 {
		t.Errorf("Oversized = %+v, want one entry for issue #7", a.Oversized)
	}
	if a.Oversized[0].LabeledSize != "M" || a.Oversized[0].ActualBracket != "S" {
		t.Errorf("Oversized[0] = %+v, want labeled M → bracket S", a.Oversized[0])
	}
	if len(a.Undersized) != 1 || a.Undersized[0].IssueNumber != 8 {
		t.Errorf("Undersized = %+v, want one entry for issue #8", a.Undersized)
	}
	if a.Undersized[0].LabeledSize != "S" || a.Undersized[0].ActualBracket != "M" {
		t.Errorf("Undersized[0] = %+v, want labeled S → bracket M", a.Undersized[0])
	}
}

func TestAggregate_AnalysisBlock_WeeklyBucketingCrossYear(t *testing.T) {
	// 2025-12-29 is ISO week 2026-W01 (Monday of week containing first
	// Thursday of 2026 — Dec 29 2025 is Monday, Jan 1 2026 is Thursday). This
	// test confirms Go's time.ISOWeek matches Python's datetime.isocalendar
	// for cross-year edges.
	build := func(issue int, started string) state.V2RunRecord {
		r := fixtureRecord(issue, started, started)
		r.Size = strPtr("S")
		r.Tokens.EstimatedCostUSD = 1.00
		return r
	}
	records := []state.V2RunRecord{
		build(1, "2025-12-30T10:00:00Z"),
		build(2, "2026-01-01T10:00:00Z"),
		build(3, "2026-01-05T10:00:00Z"),
	}
	res, _ := Aggregate(records, Options{IncludeAnalysis: true})
	a := res.Analysis
	weeks := []string{}
	for _, w := range a.WeeklyAccuracy {
		weeks = append(weeks, w.Week)
	}
	// 2025-12-30 → 2026-W01 (Mon-Sun starting Mon Dec 29 2025)
	// 2026-01-01 → 2026-W01
	// 2026-01-05 → 2026-W02
	if !sortedSliceEqual(weeks, []string{"2026-W01", "2026-W02"}) {
		t.Errorf("weeks = %v, want [2026-W01 2026-W02]", weeks)
	}
}

func TestAggregate_SchemaStability_RoundTrip(t *testing.T) {
	r := fixtureRecord(99, "2026-04-22T10:00:00Z", "t1")
	r.Title = "round-trip"
	r.Size = strPtr("M")
	r.Type = strPtr("feature")
	r = withStageModel(r, "feature-dev", "complete", 1500, "claude-sonnet-4-6", "auto")
	r = withStageTokens(r, "feature-dev", 100, 50, 1000, 200)
	res, _ := Aggregate([]state.V2RunRecord{r}, Options{IncludeAnalysis: true})

	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var rt Result
	if err := json.Unmarshal(data, &rt); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if rt.V != SchemaVersion {
		t.Errorf("V after round-trip = %d, want %d", rt.V, SchemaVersion)
	}
	if rt.RunsAnalyzed != 1 {
		t.Errorf("RunsAnalyzed after round-trip = %d, want 1", rt.RunsAnalyzed)
	}
	if rt.Runs[0].Size != "M" {
		t.Errorf("Runs[0].Size after round-trip = %q, want M", rt.Runs[0].Size)
	}
}

func TestAggregate_AnalysisOmittedWhenDisabled(t *testing.T) {
	r := fixtureRecord(1, "2026-04-22T10:00:00Z", "t1")
	r.Size = strPtr("M")
	r.Tokens.EstimatedCostUSD = 4.00
	res, _ := Aggregate([]state.V2RunRecord{r}, Options{IncludeAnalysis: false})
	if res.Analysis != nil {
		t.Errorf("Analysis = %+v, want nil when IncludeAnalysis=false", res.Analysis)
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if jsonContains(data, "\"analysis\"") {
		t.Errorf("output contains analysis key when IncludeAnalysis=false")
	}
}

func TestAggregate_MissingOptionalFields(t *testing.T) {
	// Record with nil Size, nil Type, nil Priority, nil Labels — must not
	// panic and must not show up in size baselines.
	r := state.V2RunRecord{
		SchemaVersion: "2",
		RecordType:    "run",
		IssueNumber:   1,
		StartedAt:     "2026-04-22T10:00:00Z",
		RecordedAt:    "t1",
		Outcome:       "complete",
		Stages:        map[string]state.V2StageDetail{},
		Tokens:        state.V2Tokens{},
	}
	res, _ := Aggregate([]state.V2RunRecord{r}, Options{IncludeAnalysis: true})
	if res.Runs[0].Size != "" || res.Runs[0].Type != "" || res.Runs[0].Priority != "" {
		t.Errorf("nil ptr fields not normalized: Size=%q Type=%q Priority=%q",
			res.Runs[0].Size, res.Runs[0].Type, res.Runs[0].Priority)
	}
	if len(res.Runs[0].Labels) != 0 {
		t.Errorf("nil Labels not normalized: %v", res.Runs[0].Labels)
	}
	if res.Analysis.RunsWithoutSize != 1 {
		t.Errorf("RunsWithoutSize = %d, want 1", res.Analysis.RunsWithoutSize)
	}
	if len(res.Analysis.SizeBaselines) != 0 {
		t.Errorf("SizeBaselines = %v, want empty", res.Analysis.SizeBaselines)
	}
}

func TestAggregate_NonRunRecordTypeFiltered(t *testing.T) {
	// A record with record_type other than "run" or "" must be filtered out.
	r1 := fixtureRecord(1, "2026-04-22T10:00:00Z", "t1")
	r2 := fixtureRecord(2, "2026-04-22T11:00:00Z", "t2")
	r2.RecordType = "summary" // not "run" — must be excluded
	res, _ := Aggregate([]state.V2RunRecord{r1, r2}, Options{})
	if res.RunsAnalyzed != 1 || res.Runs[0].IssueNumber != 1 {
		t.Errorf("filtered = %+v, want only issue #1", res.Runs)
	}
}

func TestPercentile_LinearInterpolation(t *testing.T) {
	// Hand-computed cases.
	cases := []struct {
		name   string
		values []float64
		p      float64
		want   float64
	}{
		{"single", []float64{42}, 50, 42},
		{"two-elements-median", []float64{10, 20}, 50, 15},
		{"three-elements-median", []float64{10, 20, 30}, 50, 20},
		{"four-elements-median", []float64{10, 20, 30, 40}, 50, 25},
		{"five-elements-p90", []float64{1000, 2000, 3000, 4000, 5000}, 90, 4600},
		{"p0", []float64{1, 2, 3}, 0, 1},
		{"p100", []float64{1, 2, 3}, 100, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sorted := append([]float64(nil), tc.values...)
			sort.Float64s(sorted)
			got := percentile(sorted, tc.p)
			if math.Abs(got-tc.want) > 0.001 {
				t.Errorf("percentile(%v, %v) = %v, want %v", tc.values, tc.p, got, tc.want)
			}
		})
	}
}

func TestLoadHistory_RejectsInvalidDate(t *testing.T) {
	cases := []string{"04/22/2026", "2026-04", "20260422", "abc"}
	for _, since := range cases {
		_, err := LoadHistory(t.TempDir(), since, "", 30)
		if err == nil {
			t.Errorf("LoadHistory(since=%q) returned nil err, want validation error", since)
		}
	}
}

func TestLoadHistory_FilenameAndStartedAtFilters(t *testing.T) {
	// Write three daily JSONL files; LoadHistory should apply the
	// --since/--until filter against StartedAt (the per-record date).
	dir := t.TempDir()
	historyDir := filepath.Join(dir, ".nightgauge", "pipeline", "history")
	if err := os.MkdirAll(historyDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(filename, started string, issue int) {
		rec := state.V2RunRecord{
			SchemaVersion: "2",
			RecordType:    "run",
			IssueNumber:   issue,
			StartedAt:     started,
			RecordedAt:    started,
			Outcome:       "complete",
			Stages:        map[string]state.V2StageDetail{},
		}
		data, _ := json.Marshal(rec)
		path := filepath.Join(historyDir, filename)
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	write("2026-04-20.jsonl", "2026-04-20T10:00:00Z", 1)
	write("2026-04-22.jsonl", "2026-04-22T10:00:00Z", 2)
	write("2026-04-24.jsonl", "2026-04-24T10:00:00Z", 3)

	got, err := LoadHistory(dir, "2026-04-22", "", 30)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
	for _, r := range got {
		if r.IssueNumber == 1 {
			t.Errorf("LoadHistory included pre-since record (issue 1)")
		}
	}

	got, err = LoadHistory(dir, "", "2026-04-22", 30)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2 (issues 1 and 2)", len(got))
	}
}

// --- helpers ---

func contains(slice []string, want string) bool {
	for _, s := range slice {
		if s == want {
			return true
		}
	}
	return false
}

func jsonContains(data []byte, sub string) bool {
	s := string(data)
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func sortedSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}
