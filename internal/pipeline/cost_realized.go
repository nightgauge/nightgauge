package pipeline

import (
	"sort"

	"github.com/nightgauge/nightgauge/internal/models"
	"github.com/nightgauge/nightgauge/internal/state"
)

// RealizedCostResult is the stable JSON output for `nightgauge cost realized`
// (#55). It sets what routing assumes a run costs — list price, first attempt,
// no handoffs — against what the run actually cost, per successful run.
//
// ListPriceUSD prices every stage's input (fresh, cache read and cache write
// alike) at the routed model's full input rate plus output, divided by the
// stage's attempts. RealizedUSD is the recorded cost (cache reads and writes
// at their own rates, every attempt) plus each handoff's estimated cold start.
type RealizedCostResult struct {
	V                     int     `json:"v"`
	RunsAnalyzed          int     `json:"runs_analyzed"`
	SuccessfulRuns        int     `json:"successful_runs"`
	ListPriceUSD          float64 `json:"list_price_usd"`
	RealizedUSD           float64 `json:"realized_usd"`
	HandoffColdStartUSD   float64 `json:"handoff_cold_start_usd"`
	HandoffEvents         int     `json:"handoff_events"`
	ListPerSuccessUSD     float64 `json:"list_per_success_usd"`
	RealizedPerSuccessUSD float64 `json:"realized_per_success_usd"`
	// UnpricedStages counts stages the registry could not list-price; their
	// list price is missing, so ListPriceUSD is a floor when it is non-zero.
	UnpricedStages int `json:"unpriced_stages"`
	// HandoffReasons counts handoff events by reason.
	HandoffReasons []ReasonCount `json:"handoff_reasons"`
}

// ReasonCount is one handoff reason and how often it occurred.
type ReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// AggregateRealizedCost computes the realized-cost report over records. Pure
// and deterministic. Every run's spend counts toward the totals; only
// successful (outcome "complete") runs count in the per-success denominator,
// so a run that failed is spend with nothing to show for it.
func AggregateRealizedCost(records []state.V2RunRecord) RealizedCostResult {
	res := RealizedCostResult{V: 1, RunsAnalyzed: len(records), HandoffReasons: []ReasonCount{}}
	reasons := map[string]int{}
	for _, r := range records {
		if r.Outcome == "complete" {
			res.SuccessfulRuns++
		}
		res.RealizedUSD += r.Tokens.EstimatedCostUSD
		for _, h := range r.HandoffEvents {
			res.HandoffColdStartUSD += h.EstimatedColdStartUSD
			res.HandoffEvents++
			reasons[h.Reason]++
		}
		names := make([]string, 0, len(r.Tokens.PerStage))
		for name := range r.Tokens.PerStage {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			st := r.Tokens.PerStage[name]
			list, ok := listPrice(st)
			if !ok {
				res.UnpricedStages++
				continue
			}
			if attempts := r.Stages[name].AttemptsUntilSuccess; attempts > 1 {
				list /= float64(attempts)
			}
			res.ListPriceUSD += list
		}
	}
	res.RealizedUSD += res.HandoffColdStartUSD
	if res.SuccessfulRuns > 0 {
		res.ListPerSuccessUSD = res.ListPriceUSD / float64(res.SuccessfulRuns)
		res.RealizedPerSuccessUSD = res.RealizedUSD / float64(res.SuccessfulRuns)
	}
	for reason, n := range reasons {
		res.HandoffReasons = append(res.HandoffReasons, ReasonCount{Reason: reason, Count: n})
	}
	sort.Slice(res.HandoffReasons, func(i, j int) bool { return res.HandoffReasons[i].Reason < res.HandoffReasons[j].Reason })
	return res
}

// listPrice prices a stage as routing does: every input token at the full
// input rate. A stage with no tokens costs nothing; one whose model the
// registry cannot resolve is unpriced.
func listPrice(st state.V2StageTokens) (float64, bool) {
	in := st.Input + st.CacheRead + st.CacheCreation
	if in == 0 && st.Output == 0 {
		return 0, true
	}
	d, ok := models.Resolve(models.ProviderFor(st.Adapter, st.Model), st.Model)
	if !ok && st.Adapter == "" {
		// Records from before per-stage adapter attribution: the model alone.
		d, ok = models.Get(st.Model)
	}
	if !ok {
		return 0, false
	}
	return (float64(in)*d.Rates.Input + float64(st.Output)*d.Rates.Output) / 1e6, true
}
