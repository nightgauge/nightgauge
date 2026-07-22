package health

import "sort"

// AggregateGateMetrics groups entries by gate_name and computes hit rates.
// Results are sorted by GateName for deterministic output.
func AggregateGateMetrics(entries []GateMetricsEntry) []GateMetricsAggregate {
	type accum struct {
		catches     int
		skipped     int
		invocations int
		totalMs     float64
	}
	gates := make(map[string]*accum)

	for _, e := range entries {
		a, ok := gates[e.GateName]
		if !ok {
			a = &accum{}
			gates[e.GateName] = a
		}
		a.invocations++
		a.totalMs += float64(e.DurationMs)
		switch e.Result {
		case "catch":
			a.catches++
		case "skip":
			a.skipped++
		}
	}

	results := make([]GateMetricsAggregate, 0, len(gates))
	for name, a := range gates {
		passes := a.invocations - a.catches - a.skipped
		total := a.catches + passes
		hitRate := 0.0
		if total > 0 {
			hitRate = float64(a.catches) / float64(total)
		}
		avgDuration := 0.0
		if a.invocations > 0 {
			avgDuration = a.totalMs / float64(a.invocations)
		}
		results = append(results, GateMetricsAggregate{
			GateName:        name,
			Invocations:     a.invocations,
			Catches:         a.catches,
			Skipped:         a.skipped,
			HitRate:         hitRate,
			AverageDuration: avgDuration,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].GateName < results[j].GateName
	})
	return results
}
