package pipeline

import (
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/state"
)

func rec(class string, costUSD float64, durMs int64) state.V2RunRecord {
	return state.V2RunRecord{
		Routing:       state.V2Routing{ChangeClass: class},
		Tokens:        state.V2Tokens{EstimatedCostUSD: costUSD},
		TotalDuration: durMs,
	}
}

func recUnstamped(class string, costUSD float64, durMs int64) state.V2RunRecord {
	return state.V2RunRecord{
		Routing:       state.V2Routing{ChangeClass: class},
		Tokens:        state.V2Tokens{EstimatedCostUSD: costUSD, CostUnstamped: true},
		TotalDuration: durMs,
	}
}

func TestAggregateCostByClass(t *testing.T) {
	records := []state.V2RunRecord{
		rec("docs_only", 0.20, 60000),
		rec("docs_only", 0.40, 90000),
		rec("source", 6.00, 1800000),
		rec("source", 8.00, 2400000),
		rec("", 1.0, 100000), // pre-#4129 record → unknown bucket
	}
	res := AggregateCostByClass(records)

	if res.RunsAnalyzed != 5 {
		t.Errorf("RunsAnalyzed = %d, want 5", res.RunsAnalyzed)
	}

	byClass := map[string]ClassCostStats{}
	for _, c := range res.Classes {
		byClass[c.ChangeClass] = c
	}

	docs, ok := byClass["docs_only"]
	if !ok {
		t.Fatal("docs_only bucket missing")
	}
	if docs.Runs != 2 {
		t.Errorf("docs_only Runs = %d, want 2", docs.Runs)
	}
	if docs.CostMeanUSD < 0.29 || docs.CostMeanUSD > 0.31 {
		t.Errorf("docs_only mean = %.4f, want ~0.30", docs.CostMeanUSD)
	}

	src := byClass["source"]
	// docs is materially cheaper than source — the epic's whole claim.
	if !(docs.CostP50USD < src.CostP50USD) {
		t.Errorf("docs p50 (%.2f) should be < source p50 (%.2f)", docs.CostP50USD, src.CostP50USD)
	}

	if _, ok := byClass["unknown"]; !ok {
		t.Error("empty change_class should bucket under 'unknown'")
	}

	// Ordering: docs_only before source before unknown.
	order := []string{}
	for _, c := range res.Classes {
		order = append(order, c.ChangeClass)
	}
	if order[0] != "docs_only" {
		t.Errorf("first class = %q, want docs_only (cheapest-first ordering)", order[0])
	}
	if order[len(order)-1] != "unknown" {
		t.Errorf("last class = %q, want unknown", order[len(order)-1])
	}
}

func TestAggregateCostByClass_Empty(t *testing.T) {
	res := AggregateCostByClass(nil)
	if res.RunsAnalyzed != 0 || len(res.Classes) != 0 {
		t.Errorf("empty input = %+v, want zero", res)
	}
	if res.V != 1 {
		t.Errorf("V = %d, want 1", res.V)
	}
}

// TestAggregateCostByClass_UnstampedRuns verifies runs whose Tokens.CostUnstamped
// is true are counted per-bucket (#585, #588) without being excluded from the
// cost sums — a silent exclusion would just undercount real spend elsewhere.
func TestAggregateCostByClass_UnstampedRuns(t *testing.T) {
	records := []state.V2RunRecord{
		rec("source", 6.00, 1800000),
		recUnstamped("source", 0.00, 1200000),
		recUnstamped("source", 4.00, 1500000),
		rec("docs_only", 0.20, 60000),
	}
	res := AggregateCostByClass(records)

	byClass := map[string]ClassCostStats{}
	for _, c := range res.Classes {
		byClass[c.ChangeClass] = c
	}

	src, ok := byClass["source"]
	if !ok {
		t.Fatal("source bucket missing")
	}
	if src.UnstampedRuns != 2 {
		t.Errorf("source UnstampedRuns = %d, want 2", src.UnstampedRuns)
	}
	if src.Runs != 3 {
		t.Errorf("source Runs = %d, want 3 (unstamped runs still counted)", src.Runs)
	}
	wantTotal := 6.00 + 0.00 + 4.00
	if src.TotalCostUSD < wantTotal-0.0001 || src.TotalCostUSD > wantTotal+0.0001 {
		t.Errorf("source TotalCostUSD = %f, want %f — unstamped runs must still be summed, not silently dropped", src.TotalCostUSD, wantTotal)
	}

	docs, ok := byClass["docs_only"]
	if !ok {
		t.Fatal("docs_only bucket missing")
	}
	if docs.UnstampedRuns != 0 {
		t.Errorf("docs_only UnstampedRuns = %d, want 0", docs.UnstampedRuns)
	}
}

// TestAggregateCostByClass_RealisticRunIsNotSelfInvalidating (Issue #890) is
// the end-to-end sanity check for `cost by-class`: it builds a V2 record the
// way a real run does — three model-running stages plus the five deterministic
// stages that dispatch nothing — and asserts the bucket does NOT count it as
// unstamped.
//
// Written against the real writer rather than a hand-built V2RunRecord on
// purpose: asserting UnstampedRuns == 0 for a literal with CostUnstamped:false
// would test nothing but the struct literal. Before #890 the deterministic
// stages priced through the unresolvable (anthropic, "") pair, the run-level
// OR was true for every run ever recorded, and every bucket reported
// unstamped_runs == runs — which the field's own contract says disqualifies
// cost_mean_usd/cost_p50_usd/cost_p95_usd.
func TestAggregateCostByClass_RealisticRunIsNotSelfInvalidating(t *testing.T) {
	hw := state.NewHistoryWriter(t.TempDir())
	rs := state.NewRuntimeState("nightgauge/nightgauge", 890, "item-890-bucket", "01a00000-0000-7000-8000-000000000890")

	for _, stage := range []state.PipelineStage{
		state.StageFeaturePlanning, state.StageFeatureDev, state.StageFeatureValidate,
	} {
		rs.BeginStage(stage)
		rs.CompleteStageWithCost(0, 1_000_000, 20_000, 900_000, 5.87)
	}
	for _, stage := range []state.PipelineStage{
		state.PipelineStage("pipeline-start"), state.StageIssuePickup,
		state.StagePRCreate, state.StagePRMerge, state.PipelineStage("pipeline-finish"),
	} {
		rs.BeginStage(stage)
		rs.CompleteStage(0, tokens.TokenCounts{}, "", "")
	}

	record := hw.BuildV2Record(rs, true, "", state.V2RunInput{}, time.Now())
	record.Routing.ChangeClass = "source"

	res := AggregateCostByClass([]state.V2RunRecord{record})
	if len(res.Classes) != 1 {
		t.Fatalf("Classes = %d, want 1", len(res.Classes))
	}
	if got := res.Classes[0].UnstampedRuns; got != 0 {
		t.Errorf("UnstampedRuns = %d, want 0 — a run whose stages are all either natively priced or deterministic must not invalidate its own bucket", got)
	}
	if got := res.Classes[0].TotalCostUSD; got != 17.61 {
		t.Errorf("TotalCostUSD = %v, want 17.61 — the deterministic stages contribute exactly nothing", got)
	}
}
