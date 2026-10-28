package pipeline

import (
	"math"
	"testing"

	"github.com/nightgauge/nightgauge/internal/models"
	"github.com/nightgauge/nightgauge/internal/state"
)

func sonnetRates(t *testing.T) models.Rates {
	t.Helper()
	d, ok := models.Resolve("anthropic", "sonnet")
	if !ok {
		t.Fatal("registry does not resolve anthropic sonnet")
	}
	return d.Rates
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestAggregateRealizedCost_HandoffPenaltyExceedsCacheSaving is the red
// fixture: the cache made the run cheaper than list price, and a handoff's
// cold start more than gave the saving back.
func TestAggregateRealizedCost_HandoffPenaltyExceedsCacheSaving(t *testing.T) {
	r := sonnetRates(t)
	st := state.V2StageTokens{Input: 0, CacheRead: 1_000_000, Output: 0, Adapter: "claude-headless", Model: "sonnet",
		CostUSD: *r.CacheRead}
	list := r.Input // 1M tokens at the full input rate
	saving := list - *r.CacheRead
	rec := state.V2RunRecord{
		Outcome: "complete",
		Tokens:  state.V2Tokens{EstimatedCostUSD: st.CostUSD, PerStage: map[string]state.V2StageTokens{"feature-dev": st}},
		HandoffEvents: []state.HandoffEvent{{FromStage: "feature-planning", ToStage: "feature-dev",
			Reason: "provider_change", EstimatedColdStartUSD: saving + 1}},
	}
	got := AggregateRealizedCost([]state.V2RunRecord{rec})
	if !near(got.ListPriceUSD, list) {
		t.Errorf("list = %v, want %v", got.ListPriceUSD, list)
	}
	if got.RealizedUSD <= got.ListPriceUSD {
		t.Errorf("realized %v <= list %v: the handoff penalty must show", got.RealizedUSD, got.ListPriceUSD)
	}
	if got.HandoffEvents != 1 || len(got.HandoffReasons) != 1 || got.HandoffReasons[0].Reason != "provider_change" {
		t.Errorf("handoffs = %d %+v", got.HandoffEvents, got.HandoffReasons)
	}
}

// TestAggregateRealizedCost_RetriesCountInRealizedOnly: list price is the
// first attempt; realized is every attempt.
func TestAggregateRealizedCost_RetriesCountInRealizedOnly(t *testing.T) {
	r := sonnetRates(t)
	st := state.V2StageTokens{Input: 2_000_000, Adapter: "claude-headless", Model: "sonnet", CostUSD: 2 * r.Input}
	rec := state.V2RunRecord{
		Outcome: "complete",
		Stages:  map[string]state.V2StageDetail{"feature-validate": {AttemptsUntilSuccess: 2}},
		Tokens:  state.V2Tokens{EstimatedCostUSD: st.CostUSD, PerStage: map[string]state.V2StageTokens{"feature-validate": st}},
	}
	got := AggregateRealizedCost([]state.V2RunRecord{rec})
	if !near(got.ListPriceUSD, r.Input) || !near(got.RealizedUSD, 2*r.Input) {
		t.Errorf("list %v realized %v, want %v and %v", got.ListPriceUSD, got.RealizedUSD, r.Input, 2*r.Input)
	}
}

// TestAggregateRealizedCost_FailedRunsAreSpendWithoutASuccess: a failed run's
// cost counts, but not in the per-success denominator.
func TestAggregateRealizedCost_FailedRunsAreSpendWithoutASuccess(t *testing.T) {
	ok := state.V2RunRecord{Outcome: "complete", Tokens: state.V2Tokens{EstimatedCostUSD: 1}}
	failed := state.V2RunRecord{Outcome: "failed", Tokens: state.V2Tokens{EstimatedCostUSD: 3}}
	got := AggregateRealizedCost([]state.V2RunRecord{ok, failed})
	if got.SuccessfulRuns != 1 || !near(got.RealizedPerSuccessUSD, 4) {
		t.Errorf("successes %d per-success %v, want 1 and 4", got.SuccessfulRuns, got.RealizedPerSuccessUSD)
	}
	if none := AggregateRealizedCost([]state.V2RunRecord{failed}); none.RealizedPerSuccessUSD != 0 {
		t.Errorf("zero successes: per-success %v, want 0", none.RealizedPerSuccessUSD)
	}
}

func TestAggregateRealizedCost_UnpricedStageIsCounted(t *testing.T) {
	rec := state.V2RunRecord{Tokens: state.V2Tokens{PerStage: map[string]state.V2StageTokens{
		"feature-dev": {Input: 10, Adapter: "claude-headless", Model: "no-such-model"}}}}
	if got := AggregateRealizedCost([]state.V2RunRecord{rec}); got.UnpricedStages != 1 {
		t.Errorf("unpriced = %d, want 1", got.UnpricedStages)
	}
}
