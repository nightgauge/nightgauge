package pipeline

import "github.com/nightgauge/nightgauge/internal/state"
import "testing"

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
