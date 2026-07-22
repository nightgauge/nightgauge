package diagnostics

import (
	"sort"
)

// StageStats holds aggregate statistics for a (repo, stage, size_label) group.
type StageStats struct {
	Repo      string  `json:"repo"`
	Stage     string  `json:"stage"`
	SizeLabel string  `json:"size_label"`
	N         int     `json:"n"`          // total records in group
	OkN       int     `json:"ok_n"`       // successful records
	P50Cost   float64 `json:"p50_cost"`
	P75Cost   float64 `json:"p75_cost"`
	P95Cost   float64 `json:"p95_cost"`
	MedianDurMs int64 `json:"median_dur_ms"`
	OkRate    float64 `json:"ok_rate"`    // successful / total
}

// groupKey identifies a (repo, stage, size_label) aggregate bucket.
type groupKey struct {
	Repo      string
	Stage     string
	SizeLabel string
}

// ComputeStats groups exit records by (repo, stage, size_label) and computes
// percentile statistics. Records with an empty size_label are grouped under "".
func ComputeStats(records []StageExitRecord) []StageStats {
	type bucket struct {
		all      []StageExitRecord
		okCosts  []float64
		allDurMs []int64
	}
	buckets := make(map[groupKey]*bucket)

	for _, rec := range records {
		k := groupKey{Repo: rec.Repo, Stage: rec.Stage, SizeLabel: rec.SizeLabel}
		b := buckets[k]
		if b == nil {
			b = &bucket{}
			buckets[k] = b
		}
		b.all = append(b.all, rec)
		if rec.Success && rec.Tokens.CostUsd > 0 {
			b.okCosts = append(b.okCosts, rec.Tokens.CostUsd)
		}
		if rec.ElapsedMs > 0 {
			b.allDurMs = append(b.allDurMs, rec.ElapsedMs)
		}
	}

	// Sort keys for deterministic output.
	keys := make([]groupKey, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Repo != keys[j].Repo {
			return keys[i].Repo < keys[j].Repo
		}
		if keys[i].Stage != keys[j].Stage {
			return keys[i].Stage < keys[j].Stage
		}
		return keys[i].SizeLabel < keys[j].SizeLabel
	})

	out := make([]StageStats, 0, len(keys))
	for _, k := range keys {
		b := buckets[k]
		n := len(b.all)
		ok := 0
		for _, r := range b.all {
			if r.Success {
				ok++
			}
		}

		okRate := 0.0
		if n > 0 {
			okRate = float64(ok) / float64(n)
		}

		var p50, p75, p95 float64
		if len(b.okCosts) > 0 {
			sorted := make([]float64, len(b.okCosts))
			copy(sorted, b.okCosts)
			sort.Float64s(sorted)
			p50 = percentile(sorted, 0.50)
			p75 = percentile(sorted, 0.75)
			p95 = percentile(sorted, 0.95)
		}

		var medDur int64
		if len(b.allDurMs) > 0 {
			sorted := make([]int64, len(b.allDurMs))
			copy(sorted, b.allDurMs)
			sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
			medDur = sorted[len(sorted)/2]
		}

		out = append(out, StageStats{
			Repo:        k.Repo,
			Stage:       k.Stage,
			SizeLabel:   k.SizeLabel,
			N:           n,
			OkN:         ok,
			P50Cost:     p50,
			P75Cost:     p75,
			P95Cost:     p95,
			MedianDurMs: medDur,
			OkRate:      okRate,
		})
	}
	return out
}

// percentile computes the Nth percentile (0–1) of a sorted float64 slice.
// Returns 0 for an empty slice. Uses nearest-rank method.
func percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sorted[0]
	}
	idx := int(p * float64(n-1))
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}
