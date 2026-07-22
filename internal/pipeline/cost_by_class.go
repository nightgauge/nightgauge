package pipeline

import (
	"sort"

	"github.com/nightgauge/nightgauge/internal/state"
)

// ClassCostStats is the per-change-class cost/duration aggregate emitted by
// `nightgauge cost by-class` (#4129). It lets a team see, empirically,
// that fast-tracked trivial changes cost less than full source changes — the
// measurement loop for epic #4123.
type ClassCostStats struct {
	ChangeClass   string  `json:"change_class"`
	Runs          int     `json:"runs"`
	CostP50USD    float64 `json:"cost_p50_usd"`
	CostP95USD    float64 `json:"cost_p95_usd"`
	CostMeanUSD   float64 `json:"cost_mean_usd"`
	DurationP50Ms int64   `json:"duration_p50_ms"`
	DurationP95Ms int64   `json:"duration_p95_ms"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
}

// CostByClassResult is the stable JSON output for `cost by-class`.
type CostByClassResult struct {
	V            int              `json:"v"`
	RunsAnalyzed int              `json:"runs_analyzed"`
	Classes      []ClassCostStats `json:"classes"`
}

// classBucketOrder gives the by-class output a stable, meaningful ordering
// (cheapest/fast-trackable first, then heavier classes, unknown last).
var classBucketOrder = map[string]int{
	"docs_only":   0,
	"config_only": 1,
	"source":      2,
	"mixed":       3,
	"empty":       4,
	"":            5, // pre-#4129 records with no recorded change_class
}

// AggregateCostByClass groups run records by their authoritative
// routing.change_class and computes p50/p95/mean cost + p50/p95 duration per
// class. Pure and deterministic: identical input → identical output, no I/O.
// Records with an empty change_class (pre-#4129 runs) bucket under "unknown".
func AggregateCostByClass(records []state.V2RunRecord) CostByClassResult {
	type acc struct {
		costs []float64
		durs  []float64
		total float64
	}
	buckets := map[string]*acc{}
	for _, r := range records {
		key := r.Routing.ChangeClass
		b := buckets[key]
		if b == nil {
			b = &acc{}
			buckets[key] = b
		}
		c := r.Tokens.EstimatedCostUSD
		b.costs = append(b.costs, c)
		b.durs = append(b.durs, float64(r.TotalDuration))
		b.total += c
	}

	out := make([]ClassCostStats, 0, len(buckets))
	for key, b := range buckets {
		sort.Float64s(b.costs)
		sort.Float64s(b.durs)
		var sum float64
		for _, c := range b.costs {
			sum += c
		}
		mean := 0.0
		if len(b.costs) > 0 {
			mean = sum / float64(len(b.costs))
		}
		label := key
		if label == "" {
			label = "unknown"
		}
		out = append(out, ClassCostStats{
			ChangeClass:   label,
			Runs:          len(b.costs),
			CostP50USD:    percentile(b.costs, 50),
			CostP95USD:    percentile(b.costs, 95),
			CostMeanUSD:   mean,
			DurationP50Ms: int64(percentile(b.durs, 50)),
			DurationP95Ms: int64(percentile(b.durs, 95)),
			TotalCostUSD:  b.total,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		// Map "unknown" back to the "" bucket-order slot.
		ki, kj := out[i].ChangeClass, out[j].ChangeClass
		if ki == "unknown" {
			ki = ""
		}
		if kj == "unknown" {
			kj = ""
		}
		oi, iok := classBucketOrder[ki]
		oj, jok := classBucketOrder[kj]
		if !iok {
			oi = 99
		}
		if !jok {
			oj = 99
		}
		if oi != oj {
			return oi < oj
		}
		return out[i].ChangeClass < out[j].ChangeClass
	})

	return CostByClassResult{V: 1, RunsAnalyzed: len(records), Classes: out}
}
