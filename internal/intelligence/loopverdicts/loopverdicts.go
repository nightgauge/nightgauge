// Package loopverdicts analyzes self-improvement loop effectiveness by reading
// pipeline data files and returning deterministic verdicts per loop.
package loopverdicts

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Verdict represents the health status of a single improvement loop.
type Verdict string

const (
	VerdictClosing       Verdict = "closing"
	VerdictStalling      Verdict = "stalling"
	VerdictDegrading     Verdict = "degrading"
	VerdictNoData        Verdict = "no-data"
	VerdictBootstrapping Verdict = "bootstrapping"
)

// LoopResult holds the verdict for one loop.
type LoopResult struct {
	Loop     string            `json:"loop"`
	Verdict  Verdict           `json:"verdict"`
	Points   int               `json:"points"`
	Reason   string            `json:"reason"`
	Evidence map[string]string `json:"evidence,omitempty"`
}

// Report is the full output of Analyze().
type Report struct {
	V              int          `json:"v"`
	CompositeScore int          `json:"compositeScore"`
	HealthBand     string       `json:"healthBand"`
	Loops          []LoopResult `json:"loops"`
	Period         int          `json:"period"`
	GeneratedAt    time.Time    `json:"generatedAt"`
}

// AnalyzeInput provides all data needed for verdict computation.
type AnalyzeInput struct {
	WorkspaceRoot string
	Period        int // days; default 30
}

// Analyze reads loop data files and returns a verdict report.
// When a data file is missing, the corresponding loop gets VerdictNoData (not an error).
func Analyze(input AnalyzeInput) (Report, error) {
	if input.Period <= 0 {
		input.Period = 30
	}
	if input.WorkspaceRoot == "" {
		input.WorkspaceRoot, _ = os.Getwd()
	}

	since := time.Now().UTC().AddDate(0, 0, -input.Period)

	loops := []LoopResult{
		analyzeSkillDrift(input.WorkspaceRoot, since),
		analyzeCalibration(input.WorkspaceRoot, since),
		analyzeHealthMonitoring(input.WorkspaceRoot, since),
		analyzeCostOptimization(input.WorkspaceRoot, since),
		analyzeReliability(input.WorkspaceRoot, since),
	}

	raw := 0
	for _, l := range loops {
		raw += l.Points
	}
	// Normalize: (raw + 50) / 100 * 100 clamped 0-100
	// With 5 loops: max raw = +100, min raw = -50 (degrading×5 = -50)
	// Formula per plan: (raw+50)/100*100 → but we normalize to percentage
	composite := int(math.Round(float64(raw+50) / 100.0 * 100.0))
	if composite < 0 {
		composite = 0
	}
	if composite > 100 {
		composite = 100
	}

	return Report{
		V:              1,
		CompositeScore: composite,
		HealthBand:     healthBand(composite),
		Loops:          loops,
		Period:         input.Period,
		GeneratedAt:    time.Now().UTC(),
	}, nil
}

func healthBand(score int) string {
	switch {
	case score >= 80:
		return "highly-effective"
	case score >= 60:
		return "working"
	case score >= 40:
		return "needs-attention"
	default:
		return "urgent"
	}
}

func verdictPoints(v Verdict) int {
	switch v {
	case VerdictClosing:
		return 20
	case VerdictStalling:
		return 5
	case VerdictDegrading:
		return -10
	default:
		return 0
	}
}

// --- Skill Drift Loop ---

type assessmentFriction struct {
	Type string `json:"type"`
}

type assessmentRecord struct {
	Friction []assessmentFriction `json:"friction"`
}

func analyzeSkillDrift(root string, since time.Time) LoopResult {
	assessDir := filepath.Join(root, ".nightgauge", "pipeline", "assessments")
	entries, err := os.ReadDir(assessDir)
	if err != nil || len(entries) == 0 {
		return noDataResult("skill-drift", "no assessment records found")
	}

	var total, withFriction int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().Before(since) {
			continue
		}
		total++
		data, err := os.ReadFile(filepath.Join(assessDir, e.Name()))
		if err != nil {
			continue
		}
		var rec assessmentRecord
		if err := json.Unmarshal(data, &rec); err != nil || len(rec.Friction) > 0 {
			withFriction++
		}
	}

	if total < 5 {
		return noDataResult("skill-drift", "fewer than 5 assessment records in period")
	}

	frictionRate := float64(withFriction) / float64(total)
	evidence := map[string]string{
		"totalAssessments": itoa(total),
		"withFriction":     itoa(withFriction),
		"frictionRate":     pct(frictionRate),
	}

	// Without multi-period data we can only assess current state.
	// Treat high friction rate as degrading, low as closing.
	switch {
	case frictionRate < 0.25:
		v := VerdictClosing
		return LoopResult{
			Loop:     "skill-drift",
			Verdict:  v,
			Points:   verdictPoints(v),
			Reason:   "friction rate below 25% — skill instructions are largely accurate",
			Evidence: evidence,
		}
	case frictionRate >= 0.50:
		v := VerdictDegrading
		return LoopResult{
			Loop:     "skill-drift",
			Verdict:  v,
			Points:   verdictPoints(v),
			Reason:   "friction rate at or above 50% — skill instructions need attention",
			Evidence: evidence,
		}
	default:
		v := VerdictStalling
		return LoopResult{
			Loop:     "skill-drift",
			Verdict:  v,
			Points:   verdictPoints(v),
			Reason:   "friction rate between 25-50% — loop is stalling",
			Evidence: evidence,
		}
	}
}

// --- Calibration Loop ---

type outcomeRecord struct {
	PredictedSize  string    `json:"predictedSize"`
	ActualSize     string    `json:"actualSize"`
	PredictedModel string    `json:"predictedModel"`
	ActualModel    string    `json:"actualModel"`
	Success        bool      `json:"success"`
	CompletedAt    time.Time `json:"completedAt"`
}

// routingPair is one MEASURABLE predicted-vs-actual comparison: which pair it
// came from, and whether the two halves agreed. Carrying the kind is what lets
// the loop publish a decomposable number instead of one blended figure under a
// label that describes only half of it.
type routingPair struct {
	kind string // pairKindSize | pairKindModel
	hit  bool
}

const (
	pairKindSize  = "size"
	pairKindModel = "model"
)

// calibrationRecentWindow is the size of the trailing trend window, in
// MEASURABLE comparisons. It is capped at half the corpus's comparisons by
// recentWindowSize below — see there for why.
const calibrationRecentWindow = 10

// routingComparisons returns one entry per MEASURABLE predicted-vs-actual pair
// on a row, and nothing at all for a pair with an absent half.
//
// The absent case is the whole point. The corpus writes "" for unknown, so a
// pair with an empty half measured nothing — but the loop used to divide its
// match count by the row count, which turned every such pair into a recorded
// MISS. With ~95% of real issues carrying no size input that pinned the
// reported accuracy near zero regardless of routing quality, and a corpus whose
// router was PERFECT could still flip the verdict to `degrading` purely because
// the ten most recent issues were unlabelled — steering the improvement loop at
// label hygiene as if it were routing error (#304).
//
// Both pairs are checked, because both are routing predictions and either can
// be the measurable one. Current writers capture actualSize only for runs that
// reach pr-create; older and pre-pr-create rows can still yield no size pair.
// That is reported as no-data in the evidence rather than left to look like a
// measured contribution of zero.
func (o outcomeRecord) routingComparisons() []routingPair {
	var out []routingPair
	if o.PredictedSize != "" && o.ActualSize != "" {
		out = append(out, routingPair{kind: pairKindSize, hit: o.PredictedSize == o.ActualSize})
	}
	if o.PredictedModel != "" && o.ActualModel != "" {
		out = append(out, routingPair{kind: pairKindModel, hit: o.PredictedModel == o.ActualModel})
	}
	return out
}

// measurablePairs flattens the period's rows into their measurable comparisons,
// oldest first (the corpus is an append-only JSONL, so row order is time
// order).
func measurablePairs(outcomes []outcomeRecord) []routingPair {
	var out []routingPair
	for _, o := range outcomes {
		out = append(out, o.routingComparisons()...)
	}
	return out
}

// accuracyOf folds comparisons into a hit fraction. Callers must guard len == 0
// themselves: "no measurable pair" is not accuracy 0.
func accuracyOf(pairs []routingPair) float64 {
	if len(pairs) == 0 {
		return 0
	}
	matches := 0
	for _, p := range pairs {
		if p.hit {
			matches++
		}
	}
	return float64(matches) / float64(len(pairs))
}

// countKind returns how many comparisons came from one pair.
func countKind(pairs []routingPair, kind string) int {
	n := 0
	for _, p := range pairs {
		if p.kind == kind {
			n++
		}
	}
	return n
}

// recentWindowSize returns how many of the newest comparisons form the "recent"
// half of the trend, given the total number of measurable comparisons.
//
// It is the trailing window of calibrationRecentWindow comparisons — the same
// shape as the original last-N window, except N counts MEASUREMENTS instead of
// rows, so a period of mostly-unmeasurable rows no longer produces a "recent
// accuracy" computed from two of them.
//
// The cap at half the corpus is load-bearing. Without it the window swallows
// the whole corpus whenever there are ≤ N comparisons, recentAccuracy becomes
// definitionally EQUAL to historicalAccuracy, and only the `default:` (stalling)
// branch is reachable — a verdict with zero information content, credited +5
// into the composite score forever. At the corpus's real density that state
// would persist past any plausible 30-day window.
func recentWindowSize(samples int) int {
	n := calibrationRecentWindow
	if half := samples / 2; n > half {
		n = half
	}
	return n
}

func analyzeCalibration(root string, since time.Time) LoopResult {
	path := filepath.Join(root, ".nightgauge", "pipeline", "history", "outcomes.jsonl")
	outcomes, err := readOutcomes(path, since)
	if err != nil || len(outcomes) == 0 {
		return noDataResult("calibration", "no outcome records found")
	}

	total := len(outcomes)
	pairs := measurablePairs(outcomes)
	samples := len(pairs)

	// No measurable pair is NOT "accuracy 0" — it is no data. Reporting a
	// number here is what let the loop assert a confident 5.0% computed
	// entirely from rows that carried one half of a pair.
	if samples == 0 {
		return noDataResult("calibration",
			"no measurable predicted-vs-actual pairs — every outcome in the period is missing one half")
	}

	sizePairs := countKind(pairs, pairKindSize)
	modelPairs := countKind(pairs, pairKindModel)
	// Which pair produced the number. Without this the published accuracy is a
	// blend of two quantities under one label — and an absent size pair is
	// indistinguishable from a pair that was measured and happened to
	// contribute nothing without these explicit counts.
	pairEvidence := func(e map[string]string) map[string]string {
		e["sizePairsMeasured"] = itoa(sizePairs)
		e["modelPairsMeasured"] = itoa(modelPairs)
		if sizePairs == 0 {
			e["sizeCalibration"] = "no-data — this period has no row with both predictedSize and measured actualSize"
		}
		return e
	}

	if samples < 10 {
		v := VerdictBootstrapping
		return LoopResult{
			Loop:    "calibration",
			Verdict: v,
			Points:  verdictPoints(v),
			Reason:  "fewer than 10 measurable predictions — calibration loop bootstrapping",
			Evidence: pairEvidence(map[string]string{
				"totalOutcomes":       itoa(total),
				"measuredPredictions": itoa(samples),
			}),
		}
	}

	historicalAccuracy := accuracyOf(pairs)
	// Recent = the newest measurable comparisons (see recentWindowSize), so the
	// trend compares measurements against measurements instead of dividing a
	// match count by a row count that mostly measured nothing.
	recentSamples := recentWindowSize(samples)
	recentAccuracy := accuracyOf(pairs[samples-recentSamples:])

	evidence := pairEvidence(map[string]string{
		"totalOutcomes":             itoa(total),
		"measuredPredictions":       itoa(samples),
		"recentMeasuredPredictions": itoa(recentSamples),
		"historicalAccuracy":        pct(historicalAccuracy),
		"recentAccuracy":            pct(recentAccuracy),
	})

	switch {
	case recentAccuracy > historicalAccuracy && recentAccuracy > 0.60:
		v := VerdictClosing
		return LoopResult{
			Loop: "calibration", Verdict: v, Points: verdictPoints(v),
			Reason:   "recent accuracy exceeds historical and is above 60%",
			Evidence: evidence,
		}
	case recentAccuracy < historicalAccuracy-0.10:
		v := VerdictDegrading
		return LoopResult{
			Loop: "calibration", Verdict: v, Points: verdictPoints(v),
			Reason:   "recent accuracy declined more than 10% below historical",
			Evidence: evidence,
		}
	default:
		v := VerdictStalling
		return LoopResult{
			Loop: "calibration", Verdict: v, Points: verdictPoints(v),
			Reason:   "recent accuracy within 10% of historical — loop stalling",
			Evidence: evidence,
		}
	}
}

func readOutcomes(path string, since time.Time) ([]outcomeRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var records []outcomeRecord
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var o outcomeRecord
		if err := json.Unmarshal(line, &o); err != nil {
			continue
		}
		if !o.CompletedAt.IsZero() && o.CompletedAt.Before(since) {
			continue
		}
		records = append(records, o)
	}
	return records, scanner.Err()
}

// --- Health Monitoring Loop ---

type healthTrendEntry struct {
	Timestamp    string             `json:"timestamp"`
	OverallScore float64            `json:"overall_score"`
	Dimensions   map[string]float64 `json:"dimensions"`
	Findings     []string           `json:"significant_findings"`
}

func analyzeHealthMonitoring(root string, since time.Time) LoopResult {
	path := filepath.Join(root, ".nightgauge", "health", "trends.jsonl")
	entries, err := readHealthTrends(path, since)
	if err != nil || len(entries) == 0 {
		return noDataResult("health-monitoring", "no health trend history available")
	}

	// Simple heuristic: check if score is trending up
	total := len(entries)
	var scoreSum float64
	for _, e := range entries {
		scoreSum += e.OverallScore
	}
	avgScore := scoreSum / float64(total)

	// Check recurring findings (findings that repeat across entries)
	findingCounts := map[string]int{}
	for _, e := range entries {
		for _, f := range e.Findings {
			findingCounts[f]++
		}
	}
	var recurringCount int
	for _, cnt := range findingCounts {
		if cnt > 2 {
			recurringCount++
		}
	}

	evidence := map[string]string{
		"entriesAnalyzed":   itoa(total),
		"avgScore":          pct(avgScore / 100.0),
		"recurringFindings": itoa(recurringCount),
	}

	switch {
	case avgScore >= 70 && recurringCount == 0:
		v := VerdictClosing
		return LoopResult{
			Loop: "health-monitoring", Verdict: v, Points: verdictPoints(v),
			Reason:   "average health score ≥70 with no recurring findings",
			Evidence: evidence,
		}
	case recurringCount > 3:
		v := VerdictDegrading
		return LoopResult{
			Loop: "health-monitoring", Verdict: v, Points: verdictPoints(v),
			Reason:   "more than 3 recurring findings — recommendations not being addressed",
			Evidence: evidence,
		}
	default:
		v := VerdictStalling
		return LoopResult{
			Loop: "health-monitoring", Verdict: v, Points: verdictPoints(v),
			Reason:   "health data exists but findings recurring — loop stalling",
			Evidence: evidence,
		}
	}
}

func readHealthTrends(path string, since time.Time) ([]healthTrendEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []healthTrendEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e healthTrendEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if ts, err := time.Parse(time.RFC3339, e.Timestamp); err == nil && ts.Before(since) {
			continue
		}
		entries = append(entries, e)
	}
	return entries, scanner.Err()
}

// --- Cost Optimization Loop ---

type gateMetricEntry struct {
	GateName  string `json:"gate_name"`
	Timestamp string `json:"timestamp"`
	Result    string `json:"result"`
}

func analyzeCostOptimization(root string, since time.Time) LoopResult {
	// Cost data comes from outcomes (cost per run)
	outcomePath := filepath.Join(root, ".nightgauge", "pipeline", "history", "outcomes.jsonl")
	costOutcomes, err := readCostOutcomes(outcomePath, since)
	if err != nil || len(costOutcomes) < 5 {
		return noDataResult("cost-optimization", "fewer than 5 completed runs in period")
	}

	half := len(costOutcomes) / 2
	var earlyTotal, recentTotal float64
	for i, o := range costOutcomes {
		if i < half {
			earlyTotal += o.CostUSD
		} else {
			recentTotal += o.CostUSD
		}
	}

	earlyAvg := earlyTotal / float64(half)
	recentAvg := recentTotal / float64(len(costOutcomes)-half)

	evidence := map[string]string{
		"totalRuns":     itoa(len(costOutcomes)),
		"earlyAvgCost":  fmtFloat(earlyAvg),
		"recentAvgCost": fmtFloat(recentAvg),
	}

	switch {
	case recentAvg < earlyAvg*0.95:
		v := VerdictClosing
		return LoopResult{
			Loop: "cost-optimization", Verdict: v, Points: verdictPoints(v),
			Reason:   "cost per run decreasing — optimization loop is closing",
			Evidence: evidence,
		}
	case recentAvg > earlyAvg*1.05:
		v := VerdictDegrading
		return LoopResult{
			Loop: "cost-optimization", Verdict: v, Points: verdictPoints(v),
			Reason:   "cost per run increasing — cost optimization loop degrading",
			Evidence: evidence,
		}
	default:
		v := VerdictStalling
		return LoopResult{
			Loop: "cost-optimization", Verdict: v, Points: verdictPoints(v),
			Reason:   "cost per run flat — no cost improvement observed",
			Evidence: evidence,
		}
	}
}

type costRecord struct {
	CostUSD     float64   `json:"costUsd"`
	Success     bool      `json:"success"`
	CompletedAt time.Time `json:"completedAt"`
}

func readCostOutcomes(path string, since time.Time) ([]costRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var records []costRecord
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var o costRecord
		if err := json.Unmarshal(line, &o); err != nil {
			continue
		}
		if !o.CompletedAt.IsZero() && o.CompletedAt.Before(since) {
			continue
		}
		records = append(records, o)
	}
	return records, scanner.Err()
}

// --- Reliability Loop ---

type reliabilityRecord struct {
	Success     bool      `json:"success"`
	FailedStage string    `json:"failedStage"`
	Retries     int       `json:"retries"`
	CompletedAt time.Time `json:"completedAt"`
}

func analyzeReliability(root string, since time.Time) LoopResult {
	path := filepath.Join(root, ".nightgauge", "pipeline", "history", "outcomes.jsonl")
	records, err := readReliabilityRecords(path, since)
	if err != nil || len(records) < 5 {
		return noDataResult("reliability", "fewer than 5 runs in period")
	}

	total := len(records)
	half := total / 2

	var earlyFailures, recentFailures int
	for i, r := range records {
		if !r.Success {
			if i < half {
				earlyFailures++
			} else {
				recentFailures++
			}
		}
	}

	earlyRate := float64(earlyFailures) / float64(half)
	recentRate := float64(recentFailures) / float64(total-half)

	evidence := map[string]string{
		"totalRuns":      itoa(total),
		"earlyFailRate":  pct(earlyRate),
		"recentFailRate": pct(recentRate),
	}

	switch {
	case recentRate < earlyRate*0.90:
		v := VerdictClosing
		return LoopResult{
			Loop: "reliability", Verdict: v, Points: verdictPoints(v),
			Reason:   "failure rate decreasing — reliability loop is closing",
			Evidence: evidence,
		}
	case recentRate > earlyRate*1.10:
		v := VerdictDegrading
		return LoopResult{
			Loop: "reliability", Verdict: v, Points: verdictPoints(v),
			Reason:   "failure rate increasing — reliability loop degrading",
			Evidence: evidence,
		}
	default:
		v := VerdictStalling
		return LoopResult{
			Loop: "reliability", Verdict: v, Points: verdictPoints(v),
			Reason:   "failure rate flat — reliability loop stalling",
			Evidence: evidence,
		}
	}
}

func readReliabilityRecords(path string, since time.Time) ([]reliabilityRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var records []reliabilityRecord
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var r reliabilityRecord
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		if !r.CompletedAt.IsZero() && r.CompletedAt.Before(since) {
			continue
		}
		records = append(records, r)
	}
	return records, scanner.Err()
}

// --- Helpers ---

func noDataResult(loop, reason string) LoopResult {
	return LoopResult{
		Loop:    loop,
		Verdict: VerdictNoData,
		Points:  0,
		Reason:  reason,
	}
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

func pct(f float64) string {
	return fmt.Sprintf("%.1f%%", f*100)
}

func fmtFloat(f float64) string {
	return fmt.Sprintf("%.4f", f)
}
