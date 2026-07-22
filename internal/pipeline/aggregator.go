// Package pipeline provides deterministic readers and aggregators over the
// .nightgauge/pipeline/history/YYYY-MM-DD.jsonl files. The Result JSON
// schema is stable — field names and types must not change after first merge.
// Skills parse `nightgauge pipeline aggregate --json` output via jq paths;
// any breaking change requires incrementing the V field. Mirrors the
// versioning + package-layout discipline of internal/scan/deps.go (audit row
// B2; precedents B1/#3059 and B3/#3061).
package pipeline

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// SchemaVersion is the JSON schema version emitted by Aggregate. Additive
// evolution only — renames or removed fields require a bump.
const SchemaVersion = 1

// StageNames is the canonical six-stage pipeline order. Maps used by the
// aggregator iterate this slice rather than ranging over map keys so JSON
// output is stable across runs.
var StageNames = []string{
	"issue-pickup",
	"feature-planning",
	"feature-dev",
	"feature-validate",
	"pr-create",
	"pr-merge",
}

// SizeOrder is the canonical XS<S<M<L<XL ordering used by the size-accuracy
// analysis block. Mirrors the SIZE_ORDER constant in the original Python
// aggregator (skills/nightgauge-pipeline-audit/SKILL.md L422).
var SizeOrder = map[string]int{"XS": 0, "S": 1, "M": 2, "L": 3, "XL": 4}

// Result is the stable JSON output schema for `nightgauge pipeline
// aggregate`. Schema version 1.
type Result struct {
	V            int                 `json:"v"`
	RunsAnalyzed int                 `json:"runs_analyzed"`
	DateFrom     string              `json:"date_from"`
	DateTo       string              `json:"date_to"`
	Filters      AppliedFilters      `json:"filters"`
	Runs         []RunMetric         `json:"runs"`
	StageMetrics map[string]StageAgg `json:"stage_metrics"`
	ModelUsage   ModelUsage          `json:"model_usage"`
	Analysis     *Analysis           `json:"analysis,omitempty"`
	// Recovery is the aggregate Recovery Dialog metric (Issue #3239).
	// Additive field — consumers ignore it on older readers.
	Recovery RecoveryAggregate `json:"recovery"`
	// Knowledge is the additive `knowledge` block (Issue #3592, ADR-006).
	// Populated by the CLI from AggregateKnowledge — Aggregate itself stays
	// pure and never touches the filesystem, so callers without telemetry
	// data leave this as the zero-valued aggregate.
	Knowledge KnowledgeAggregate `json:"knowledge"`
	Warnings  []string           `json:"warnings"`
}

// RecoveryAggregate summarizes recovery_events across analyzed runs.
// Issue #3239. RecoveryRate = runs_with_events / runs_analyzed.
type RecoveryAggregate struct {
	RunsWithEvents int            `json:"runs_with_events"`
	TotalEvents    int            `json:"total_events"`
	RecoveryRate   float64        `json:"recovery_rate"`
	ByAction       map[string]int `json:"by_action"`
	ByErrorKind    map[string]int `json:"by_error_kind"`
}

// AppliedFilters echoes the input flags so consumers can confirm what was
// applied without re-parsing CLI args.
type AppliedFilters struct {
	Runs  int    `json:"runs"`
	Since string `json:"since"`
	Until string `json:"until"`
	Issue int    `json:"issue"`
}

// RunMetric is a per-run snapshot used by both the audit narrative and the
// size-accuracy analysis. Field names match the original Python aggregator.
type RunMetric struct {
	IssueNumber        int      `json:"issue_number"`
	Title              string   `json:"title"`
	Outcome            string   `json:"outcome"`
	TotalDurationMs    int64    `json:"total_duration_ms"`
	StartedAt          string   `json:"started_at"`
	TotalInput         int      `json:"total_input"`
	TotalOutput        int      `json:"total_output"`
	TotalCacheRead     int      `json:"total_cache_read"`
	TotalCacheCreation int      `json:"total_cache_creation"`
	EstimatedCostUSD   float64  `json:"estimated_cost_usd"`
	Labels             []string `json:"labels"`
	Size               string   `json:"size,omitempty"`
	Type               string   `json:"type,omitempty"`
	Priority           string   `json:"priority,omitempty"`
	SkippedStages      []string `json:"skipped_stages"`
}

// StageAgg is the per-stage aggregation. Status counts every record (even
// skipped stages with duration_ms=0); duration_stats and token_stats only
// include stages that actually ran.
type StageAgg struct {
	Status        map[string]int `json:"status"`
	DurationStats Stats          `json:"duration_stats"`
	TokenStats    TokenStats     `json:"token_stats"`
	Models        map[string]int `json:"models"`
	ModelSources  map[string]int `json:"model_sources"`
}

// Stats holds count, median, mean, p90, min, max for a numeric series. Median
// uses the standard "average of the two middle values for even-length lists"
// definition (matches Python statistics.median). P90 uses linear interpolation
// between adjacent ranks (matches numpy.percentile default).
type Stats struct {
	Count  int     `json:"count"`
	Median float64 `json:"median"`
	Mean   float64 `json:"mean"`
	P90    float64 `json:"p90"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

// TokenStats holds Stats for the four token dimensions per stage.
type TokenStats struct {
	Input         Stats `json:"input"`
	Output        Stats `json:"output"`
	CacheRead     Stats `json:"cache_read"`
	CacheCreation Stats `json:"cache_creation"`
}

// ModelUsage breaks down model selections across stages and selection sources.
type ModelUsage struct {
	ByStage  map[string]map[string]int `json:"by_stage"`
	BySource map[string]map[string]int `json:"by_source"`
}

// Analysis is the size-accuracy / weekly-trend block, populated only when
// Options.IncludeAnalysis is true. Matches the size_estimation_accuracy block
// produced by the original Python aggregator (Issue #1591).
type Analysis struct {
	SizeBaselines     map[string]SizeBaseline `json:"size_baselines"`
	SizeAccuracyRates map[string]AccuracyRate `json:"size_accuracy_rates"`
	Oversized         []SizeFinding           `json:"oversized"`
	Undersized        []SizeFinding           `json:"undersized"`
	WeeklyAccuracy    []WeeklyAccuracy        `json:"weekly_accuracy"`
	RunsWithSize      int                     `json:"runs_with_size"`
	RunsWithoutSize   int                     `json:"runs_without_size"`
}

// SizeBaseline is the cost+duration baseline for a single size bucket.
type SizeBaseline struct {
	Count            int     `json:"count"`
	MedianCost       float64 `json:"median_cost"`
	AvgCost          float64 `json:"avg_cost"`
	MinCost          float64 `json:"min_cost"`
	MaxCost          float64 `json:"max_cost"`
	MedianDurationMs float64 `json:"median_duration_ms"`
	AvgDurationMs    float64 `json:"avg_duration_ms"`
}

// AccuracyRate is the within-band accuracy for a size bucket.
type AccuracyRate struct {
	Total       int     `json:"total"`
	WithinRange int     `json:"within_range"`
	AccuracyPct float64 `json:"accuracy_pct"`
}

// SizeFinding identifies a single mis-sized run.
type SizeFinding struct {
	IssueNumber   int     `json:"issue_number"`
	Title         string  `json:"title"`
	LabeledSize   string  `json:"labeled_size"`
	ActualBracket string  `json:"actual_bracket"`
	CostUSD       float64 `json:"cost_usd"`
}

// WeeklyAccuracy aggregates accuracy per ISO week.
type WeeklyAccuracy struct {
	Week        string  `json:"week"`
	Total       int     `json:"total"`
	Accurate    int     `json:"accurate"`
	AccuracyPct float64 `json:"accuracy_pct"`
}

// Options controls a single aggregation run.
type Options struct {
	// Runs trims to the last N records by RecordedAt order. 0 = unbounded.
	Runs int
	// Since is a YYYY-MM-DD lower bound. Used by LoadHistory to pre-filter
	// daily JSONL filenames; Aggregate itself does not re-check.
	Since string
	// Until is a YYYY-MM-DD upper bound (forward-compat with pipeline-health).
	// Used by LoadHistory to pre-filter daily JSONL filenames.
	Until string
	// Issue keeps only records with this issue number. 0 = all.
	Issue int
	// IncludeAnalysis gates the Analysis block (size accuracy + weekly trend).
	IncludeAnalysis bool
}

// Aggregate produces a Result from the supplied V2RunRecords. It is pure —
// no file I/O, no time.Now reads. Filter order: record_type=="run", issue
// match, then runs trim by RecordedAt. Returns a non-fatal warnings list
// alongside the Result; errors are reserved for input validation failures.
func Aggregate(records []state.V2RunRecord, opts Options) (Result, []string) {
	warnings := []string{}

	// Filter by record_type defensively (the reader already skips malformed
	// lines, but record_type can be set to other values for non-run rows).
	filtered := make([]state.V2RunRecord, 0, len(records))
	for _, r := range records {
		if r.RecordType != "" && r.RecordType != "run" {
			continue
		}
		if opts.Issue != 0 && r.IssueNumber != opts.Issue {
			continue
		}
		filtered = append(filtered, r)
	}

	// Sort by RecordedAt ascending so the runs trim takes the most recent
	// records and DateFrom/DateTo are well-defined.
	sort.SliceStable(filtered, func(i, j int) bool {
		return filtered[i].RecordedAt < filtered[j].RecordedAt
	})

	if opts.Runs > 0 && len(filtered) > opts.Runs {
		filtered = filtered[len(filtered)-opts.Runs:]
	}

	result := Result{
		V:            SchemaVersion,
		RunsAnalyzed: len(filtered),
		Filters: AppliedFilters{
			Runs:  opts.Runs,
			Since: opts.Since,
			Until: opts.Until,
			Issue: opts.Issue,
		},
		Runs:         make([]RunMetric, 0, len(filtered)),
		StageMetrics: make(map[string]StageAgg, len(StageNames)),
		ModelUsage: ModelUsage{
			ByStage:  make(map[string]map[string]int, len(StageNames)),
			BySource: make(map[string]map[string]int, len(StageNames)),
		},
		Knowledge: emptyKnowledgeAggregate(),
		Warnings:  warnings,
	}

	// Pre-populate stage metrics so consumers can pin to a known shape even
	// when no records are present. Status maps stay empty rather than
	// pre-seeded with possible values — skills inspect actual statuses only.
	for _, stage := range StageNames {
		result.StageMetrics[stage] = StageAgg{
			Status:       map[string]int{},
			Models:       map[string]int{},
			ModelSources: map[string]int{},
		}
	}

	if len(filtered) > 0 {
		result.DateFrom = first10(filtered[0].StartedAt)
		result.DateTo = first10(filtered[len(filtered)-1].StartedAt)
	}

	stageDurations := make(map[string][]float64, len(StageNames))
	stageTokens := make(map[string]map[string][]float64, len(StageNames))
	for _, stage := range StageNames {
		stageTokens[stage] = map[string][]float64{
			"input": {}, "output": {}, "cache_read": {}, "cache_creation": {},
		}
	}

	for _, r := range filtered {
		// Per-run metric.
		labels := r.Labels
		if labels == nil {
			labels = []string{}
		}
		rm := RunMetric{
			IssueNumber:        r.IssueNumber,
			Title:              r.Title,
			Outcome:            r.Outcome,
			TotalDurationMs:    r.TotalDuration,
			StartedAt:          r.StartedAt,
			TotalInput:         r.Tokens.TotalInput,
			TotalOutput:        r.Tokens.TotalOutput,
			TotalCacheRead:     r.Tokens.TotalCacheRead,
			TotalCacheCreation: r.Tokens.TotalCacheCreation,
			EstimatedCostUSD:   r.Tokens.EstimatedCostUSD,
			Labels:             labels,
			Size:               derefString(r.Size),
			Type:               derefString(r.Type),
			Priority:           derefString(r.Priority),
			SkippedStages:      []string{},
		}

		for _, stage := range StageNames {
			s, ok := r.Stages[stage]
			if !ok {
				rm.SkippedStages = append(rm.SkippedStages, stage)
				continue
			}
			agg := result.StageMetrics[stage]
			status := s.Status
			if status == "" {
				status = "unknown"
			}
			agg.Status[status]++
			if s.DurationMs > 0 {
				stageDurations[stage] = append(stageDurations[stage], float64(s.DurationMs))
			} else {
				rm.SkippedStages = append(rm.SkippedStages, stage)
			}
			if s.ModelSelection != nil {
				model := s.ModelSelection.Model
				if model == "" {
					model = "unknown"
				}
				source := s.ModelSelection.Source
				if source == "" {
					source = "unknown"
				}
				agg.Models[model]++
				agg.ModelSources[source]++
				if _, ok := result.ModelUsage.ByStage[stage]; !ok {
					result.ModelUsage.ByStage[stage] = map[string]int{}
				}
				if _, ok := result.ModelUsage.BySource[stage]; !ok {
					result.ModelUsage.BySource[stage] = map[string]int{}
				}
				result.ModelUsage.ByStage[stage][model]++
				result.ModelUsage.BySource[stage][source]++
			}
			result.StageMetrics[stage] = agg

			ps, ok := r.Tokens.PerStage[stage]
			if !ok {
				continue
			}
			if ps.Input > 0 {
				stageTokens[stage]["input"] = append(stageTokens[stage]["input"], float64(ps.Input))
			}
			if ps.Output > 0 {
				stageTokens[stage]["output"] = append(stageTokens[stage]["output"], float64(ps.Output))
			}
			if ps.CacheRead > 0 {
				stageTokens[stage]["cache_read"] = append(stageTokens[stage]["cache_read"], float64(ps.CacheRead))
			}
			if ps.CacheCreation > 0 {
				stageTokens[stage]["cache_creation"] = append(stageTokens[stage]["cache_creation"], float64(ps.CacheCreation))
			}
		}

		result.Runs = append(result.Runs, rm)
	}

	// Compute stats from collected per-stage series.
	for _, stage := range StageNames {
		agg := result.StageMetrics[stage]
		agg.DurationStats = computeStats(stageDurations[stage])
		agg.TokenStats = TokenStats{
			Input:         computeStats(stageTokens[stage]["input"]),
			Output:        computeStats(stageTokens[stage]["output"]),
			CacheRead:     computeStats(stageTokens[stage]["cache_read"]),
			CacheCreation: computeStats(stageTokens[stage]["cache_creation"]),
		}
		result.StageMetrics[stage] = agg
	}

	if opts.IncludeAnalysis {
		result.Analysis = computeAnalysis(result.Runs)
	}

	result.Recovery = computeRecovery(filtered)

	return result, warnings
}

// computeRecovery aggregates recovery_events across the filtered records
// to produce a recovery_rate plus per-action/per-error breakdowns.
// Pure — no I/O. Issue #3239.
func computeRecovery(records []state.V2RunRecord) RecoveryAggregate {
	agg := RecoveryAggregate{
		ByAction:    map[string]int{},
		ByErrorKind: map[string]int{},
	}
	runsWithEvents := 0
	for _, r := range records {
		if len(r.RecoveryEvents) == 0 {
			continue
		}
		runsWithEvents++
		agg.TotalEvents += len(r.RecoveryEvents)
		for _, ev := range r.RecoveryEvents {
			if ev.Action != "" {
				agg.ByAction[ev.Action]++
			}
			if ev.ErrorKind != "" {
				agg.ByErrorKind[ev.ErrorKind]++
			}
		}
	}
	agg.RunsWithEvents = runsWithEvents
	if len(records) > 0 {
		agg.RecoveryRate = round4(float64(runsWithEvents) / float64(len(records)))
	}
	return agg
}

// computeStats returns a Stats over the supplied series. Empty series produce
// a zero-value Stats with Count=0 — JSON consumers should branch on count.
func computeStats(values []float64) Stats {
	if len(values) == 0 {
		return Stats{}
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)

	min := sorted[0]
	max := sorted[len(sorted)-1]
	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	mean := sum / float64(len(sorted))
	median := percentile(sorted, 50)
	p90 := percentile(sorted, 90)

	return Stats{
		Count:  len(sorted),
		Median: median,
		Mean:   mean,
		P90:    p90,
		Min:    min,
		Max:    max,
	}
}

// percentile returns the p-th percentile of an already-sorted ascending slice
// using linear interpolation between adjacent ranks. p in [0, 100]. The 50th
// percentile equals statistics.median for even-length lists (average of the
// two middle values) — verified by tests.
func percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sorted[0]
	}
	rank := (p / 100.0) * float64(n-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower == upper {
		return sorted[lower]
	}
	frac := rank - float64(lower)
	return sorted[lower]*(1-frac) + sorted[upper]*frac
}

// computeAnalysis produces the size-accuracy + weekly-trend block from
// per-run metrics. Mirrors the size_estimation_accuracy block from
// skills/nightgauge-pipeline-audit/SKILL.md L396–L520.
func computeAnalysis(runs []RunMetric) *Analysis {
	a := &Analysis{
		SizeBaselines:     map[string]SizeBaseline{},
		SizeAccuracyRates: map[string]AccuracyRate{},
		Oversized:         []SizeFinding{},
		Undersized:        []SizeFinding{},
		WeeklyAccuracy:    []WeeklyAccuracy{},
	}

	sizeCosts := make(map[string][]float64, 5)
	sizeDurations := make(map[string][]float64, 5)
	for _, r := range runs {
		if r.Size == "" {
			a.RunsWithoutSize++
			continue
		}
		a.RunsWithSize++
		if r.EstimatedCostUSD > 0 {
			sizeCosts[r.Size] = append(sizeCosts[r.Size], r.EstimatedCostUSD)
		}
		if r.TotalDurationMs > 0 {
			sizeDurations[r.Size] = append(sizeDurations[r.Size], float64(r.TotalDurationMs))
		}
	}

	for _, size := range []string{"XS", "S", "M", "L", "XL"} {
		costs := sizeCosts[size]
		if len(costs) == 0 {
			continue
		}
		sortedCosts := append([]float64(nil), costs...)
		sort.Float64s(sortedCosts)
		durs := sizeDurations[size]
		sortedDurs := append([]float64(nil), durs...)
		sort.Float64s(sortedDurs)

		a.SizeBaselines[size] = SizeBaseline{
			Count:            len(costs),
			MedianCost:       round4(percentile(sortedCosts, 50)),
			AvgCost:          round4(mean(costs)),
			MinCost:          round4(sortedCosts[0]),
			MaxCost:          round4(sortedCosts[len(sortedCosts)-1]),
			MedianDurationMs: roundN(percentile(sortedDurs, 50), 0),
			AvgDurationMs:    roundN(mean(durs), 0),
		}
	}

	// Oversized / undersized detection — assign each cost to the first size
	// bracket whose median is within [0.5x, 2x]. Mirrors Python L433–L455.
	for _, r := range runs {
		if r.Size == "" || r.EstimatedCostUSD <= 0 {
			continue
		}
		labelOrder, labelKnown := SizeOrder[r.Size]
		if !labelKnown {
			continue
		}
		actualBracket := ""
		for _, candidate := range []string{"XS", "S", "M", "L", "XL"} {
			bl, ok := a.SizeBaselines[candidate]
			if !ok {
				continue
			}
			if bl.MedianCost*0.5 <= r.EstimatedCostUSD && r.EstimatedCostUSD <= bl.MedianCost*2.0 {
				actualBracket = candidate
				break
			}
		}
		if actualBracket == "" {
			continue
		}
		bracketOrder := SizeOrder[actualBracket]
		title := r.Title
		if len(title) > 60 {
			title = title[:60]
		}
		finding := SizeFinding{
			IssueNumber:   r.IssueNumber,
			Title:         title,
			LabeledSize:   r.Size,
			ActualBracket: actualBracket,
			CostUSD:       round2(r.EstimatedCostUSD),
		}
		if bracketOrder < labelOrder {
			a.Oversized = append(a.Oversized, finding)
		} else if bracketOrder > labelOrder {
			a.Undersized = append(a.Undersized, finding)
		}
	}

	for size, costs := range sizeCosts {
		bl, ok := a.SizeBaselines[size]
		if !ok {
			continue
		}
		within := 0
		for _, c := range costs {
			if bl.MedianCost*0.5 <= c && c <= bl.MedianCost*2.0 {
				within++
			}
		}
		pct := 0.0
		if len(costs) > 0 {
			pct = round1(float64(within) / float64(len(costs)) * 100.0)
		}
		a.SizeAccuracyRates[size] = AccuracyRate{
			Total:       len(costs),
			WithinRange: within,
			AccuracyPct: pct,
		}
	}

	// Weekly accuracy — group by ISO week of started_at. Go's time.ISOWeek
	// returns the same year/week numbers as Python's datetime.isocalendar()
	// for ISO 8601 dates.
	weekTotal := map[string]int{}
	weekAccurate := map[string]int{}
	for _, r := range runs {
		if r.Size == "" || r.EstimatedCostUSD <= 0 || r.StartedAt == "" {
			continue
		}
		bl, ok := a.SizeBaselines[r.Size]
		if !ok {
			continue
		}
		t, err := time.Parse(time.RFC3339, r.StartedAt)
		if err != nil {
			t2, err2 := time.Parse("2006-01-02T15:04:05.000Z", r.StartedAt)
			if err2 != nil {
				continue
			}
			t = t2
		}
		year, week := t.ISOWeek()
		key := fmt.Sprintf("%04d-W%02d", year, week)
		weekTotal[key]++
		if bl.MedianCost*0.5 <= r.EstimatedCostUSD && r.EstimatedCostUSD <= bl.MedianCost*2.0 {
			weekAccurate[key]++
		}
	}
	weeks := make([]string, 0, len(weekTotal))
	for w := range weekTotal {
		weeks = append(weeks, w)
	}
	sort.Strings(weeks)
	for _, w := range weeks {
		total := weekTotal[w]
		acc := weekAccurate[w]
		pct := 0.0
		if total > 0 {
			pct = round1(float64(acc) / float64(total) * 100.0)
		}
		a.WeeklyAccuracy = append(a.WeeklyAccuracy, WeeklyAccuracy{
			Week:        w,
			Total:       total,
			Accurate:    acc,
			AccuracyPct: pct,
		})
	}

	return a
}

// LoadHistory reads V2RunRecords from workdir's history directory and applies
// the date pre-filter (--since / --until) at the filename level. days is the
// number of recent daily files to consider; 0 selects a 30-day window matching
// the audit skill's default. Warnings are non-fatal (e.g. unrecognized
// --include value passed up by the caller).
//
// LoadHistory is a thin orchestration helper — Aggregate itself remains pure
// and free of file I/O, so unit tests can hand it canned []V2RunRecord
// fixtures without writing to disk.
func LoadHistory(workdir, since, until string, days int) ([]state.V2RunRecord, error) {
	if since != "" && !isYYYYMMDD(since) {
		return nil, fmt.Errorf("--since must be YYYY-MM-DD (got %q)", since)
	}
	if until != "" && !isYYYYMMDD(until) {
		return nil, fmt.Errorf("--until must be YYYY-MM-DD (got %q)", until)
	}
	if days <= 0 {
		days = 30
	}
	hw := state.NewHistoryWriter(workdir)
	records, err := hw.ReadRecentV2(0, days)
	if err != nil {
		return nil, err
	}
	if since == "" && until == "" {
		return records, nil
	}
	out := make([]state.V2RunRecord, 0, len(records))
	for _, r := range records {
		date := first10(r.StartedAt)
		if date == "" {
			date = first10(r.RecordedAt)
		}
		if since != "" && date < since {
			continue
		}
		if until != "" && date > until {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// --- helpers ---

func first10(s string) string {
	if len(s) < 10 {
		return ""
	}
	return s[:10]
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func mean(vs []float64) float64 {
	if len(vs) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vs {
		sum += v
	}
	return sum / float64(len(vs))
}

func round1(v float64) float64 { return roundN(v, 1) }
func round2(v float64) float64 { return roundN(v, 2) }
func round4(v float64) float64 { return roundN(v, 4) }

func roundN(v float64, digits int) float64 {
	mult := math.Pow(10, float64(digits))
	return math.Round(v*mult) / mult
}

func isYYYYMMDD(s string) bool {
	if len(s) != 10 {
		return false
	}
	if s[4] != '-' || s[7] != '-' {
		return false
	}
	for i, c := range s {
		if i == 4 || i == 7 {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
