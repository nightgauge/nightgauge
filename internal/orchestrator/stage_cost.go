package orchestrator

import (
	"fmt"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/state"
)

// StageCost is the one cost a run reports for a stage (#1934): the figure the
// runtime booked, which is also what the run summary, the history record and
// every downstream consumer read.
//
// Before this existed the live per-stage line and the stage-complete callback
// re-derived cost from the executor's result. A result can arrive without the
// cache pools that the booking merges in (RuntimeState.RecordStageTokenCounts),
// so the live line priced a stage on its non-cached input and output alone and
// printed "(0 cached)": $0.72 for a pr-merge the record priced at $6.27 on
// 14.5M cache reads. Measured against the CLI's own total_cost_usd, the
// cache-inclusive figure is the right one; the cache-less figure was 3–8x low.
type StageCost struct {
	// Input is the NON-cached input pool; CacheRead is disjoint from it.
	Input         int
	Output        int
	CacheRead     int
	CacheCreation int
	CostUSD       float64
	// Source is the booking's cost provenance, a state.CostSource* value.
	Source string
}

// stageCostFromBooking reads the booked attempt of stage. ok is false only
// when nothing was booked, which the scheduler's unconditional booking makes
// unreachable after a stage returns.
func stageCostFromBooking(rt *state.RuntimeState, stage state.PipelineStage) (StageCost, bool) {
	sr, ok := rt.BookedStage(stage)
	if !ok {
		return StageCost{}, false
	}
	return stageCostOf(sr), true
}

// stageCostOf converts a booked StageResult, whose InputTokens is
// cache-inclusive, into the disjoint pools a reader prints.
func stageCostOf(sr state.StageResult) StageCost {
	return StageCost{
		Input:         max(sr.InputTokens-sr.CacheRead, 0),
		Output:        sr.OutputTokens,
		CacheRead:     sr.CacheRead,
		CacheCreation: sr.CacheCreation,
		CostUSD:       sr.CostUSD,
		Source:        sr.CostSource,
	}
}

// TokenSummary renders the four token pools. The live line and the run
// summary both use it, so one stage never reads as two different counts.
func (c StageCost) TokenSummary() string {
	return fmt.Sprintf("%d in + %d cache read + %d cache write / %d out",
		c.Input, c.CacheRead, c.CacheCreation, c.Output)
}

// CostSummary renders the cost with its provenance.
func (c StageCost) CostSummary() string {
	return fmt.Sprintf("$%.4f (%s)", c.CostUSD, c.CostLabel())
}

// CostLabel names the cost's provenance for a reader, so a CLI-reported figure
// and a rate-card derivation are never printed as the same kind of number.
func (c StageCost) CostLabel() string {
	switch c.Source {
	case state.CostSourceNative:
		return "cli-reported"
	case state.CostSourceComputed:
		return "derived from tokens"
	case state.CostSourceDeterministic:
		return "no model"
	default:
		return "unpriced"
	}
}

// costDivergenceRatio is how far a CLI-reported cost may sit from the rate-card
// derivation of the same tokens before it is an anomaly. Across 14 recorded
// stages the derivation ran −23% to +34% of the CLI's figure, which is rate-card
// calibration; the defect this guards against was 3–9x. 2x separates the two.
const costDivergenceRatio = 2.0

// costDivergenceFloorUSD skips the comparison when both figures are too small
// for a ratio between them to mean anything.
const costDivergenceFloorUSD = 0.05

// nativeCostDivergence prices a CLI-reported stage's own booked tokens on the
// rate card and reports the derivation when the two disagree by more than
// costDivergenceRatio. Only a native booking has two figures to compare.
// Cache creation is priced at the 5-minute rate, the booking's convention for
// an unsplit count (#390).
func nativeCostDivergence(c StageCost, adapter, model string) (derived float64, diverged bool) {
	if c.Source != state.CostSourceNative || c.CostUSD <= 0 {
		return 0, false
	}
	derived, stamped := tokens.CalculateCostFor(adapter, model, tokens.TokenCounts{
		Input: c.Input, Output: c.Output, CacheRead: c.CacheRead, CacheCreation5m: c.CacheCreation,
	})
	if !stamped || derived <= 0 || max(derived, c.CostUSD) < costDivergenceFloorUSD {
		return derived, false
	}
	ratio := derived / c.CostUSD
	return derived, ratio > costDivergenceRatio || ratio < 1/costDivergenceRatio
}

// anomalyCostSourceDivergence is the anomaly kind recordCostDivergence writes.
const anomalyCostSourceDivergence = "cost_source_divergence"

// recordCostDivergence persists a cost_source_divergence anomaly on the stage
// when nativeCostDivergence reports one, so the disagreement survives into the
// run record instead of scrolling past in a log. It returns the finding.
func recordCostDivergence(rt *state.RuntimeState, stage state.PipelineStage, c StageCost,
	adapter, model string, now time.Time) (string, bool) {
	derived, diverged := nativeCostDivergence(c, adapter, model)
	if !diverged {
		return "", false
	}
	detail := fmt.Sprintf("cli-reported $%.4f; the rate card prices the same tokens at $%.4f",
		c.CostUSD, derived)
	rt.AppendStageAnomaly(stage, state.Anomaly{
		Kind:          anomalyCostSourceDivergence,
		Stage:         string(stage),
		ExecutionPath: "llm",
		StageCostUSD:  c.CostUSD,
		Detail:        detail,
		Timestamp:     now.UTC().Format(time.RFC3339),
	})
	return detail, true
}
