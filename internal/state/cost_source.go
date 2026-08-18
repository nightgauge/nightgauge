package state

// Cost-source vocabulary for V2StageTokens.CostSource / StageResult.CostSource
// (Issue #682). Mirrors the TypeScript
// `cost_source: z.enum(["native", "computed", "unknown"]).optional()` in
// packages/nightgauge-vscode/src/schemas/executionHistory.ts — Go is the sole
// writer of this field, TypeScript is the only validator, so the two
// vocabularies must never drift. TestHistorySchemaParity_StageTokens (in
// history_schema_parity_test.go) pins the FIELD NAME across both languages;
// nothing today cross-checks these three VALUE strings against the Zod enum's
// literal members the way TestModelSelectionSourcesPinnedToSDK does for
// model_selection.source — see that test's doc comment for the pattern this
// would follow if the vocabulary ever needs its own pin.
const (
	// CostSourceNative is a vendor/CLI-reported cost — e.g. the Claude CLI's
	// `total_cost_usd`. The only path that writes it is
	// RuntimeState.CompleteStageWithCost.
	CostSourceNative = "native"

	// CostSourceComputed is a cost derived from the rate-card pricing
	// registry (tokens.CalculateCostForAdapter resolved the (provider, model)
	// pair). Written by RuntimeState.CompleteStage whenever
	// CalculateCostForAdapter reports `stamped == true` — including the
	// legitimate $0 for a local provider (ollama/lm-studio), which IS priced,
	// just at a zero rate.
	CostSourceComputed = "computed"

	// CostSourceUnknown marks a cost that could not be priced at all: the
	// (provider, model) pair has no pricing-registry entry, so CostUSD is a
	// placeholder 0 rather than a fabricated figure. Written by
	// RuntimeState.CompleteStage whenever CalculateCostForAdapter reports
	// `stamped == false` — mirrors CostUnstamped, which is set true on the
	// same condition.
	CostSourceUnknown = "unknown"
)

// costSourcePriority ranks how much a cost-source label can be trusted, for
// folding multiple CompleteStage occurrences of the SAME stage (a retry or a
// backtrack re-run) into one accumulated per-stage entry (#585, #588's
// summing behavior, extended to cost_source by #682). native (a real,
// vendor-reported measurement) is the strongest claim; unknown (no pricing
// evidence at all) is the weakest. An empty/unset label — the
// RecordTerminatingStageTokens synthesis path never sets one (see
// BuildV2Record's "Check for stage error" branch) — ranks as weak as
// "unknown" rather than winning against a real label.
func costSourcePriority(source string) int {
	switch source {
	case CostSourceNative:
		return 2
	case CostSourceComputed:
		return 1
	default: // CostSourceUnknown, "", or any unrecognized future value
		return 0
	}
}

// foldCostSource picks the LEAST confident of two cost-source labels
// attributed to the same accumulated (summed) stage entry. The accumulated
// cost_usd sums every occurrence's contribution (BuildV2Record, "ACCUMULATE,
// never assign"), so a stage that ran once "native" and once "computed" now
// reports a total that is PART measured and part estimated — the fold must
// not let that total claim more confidence than its weakest contributor
// actually earned. Mirrors CostUnstamped's OR-fold ("one unresolved attempt
// taints the accumulated cost_usd for the whole entry") one priority level
// up: native+computed folds to computed, and either one folding with unknown
// (or empty) folds to unknown.
func foldCostSource(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if costSourcePriority(a) <= costSourcePriority(b) {
		return a
	}
	return b
}
