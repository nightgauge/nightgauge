package state

// Cost-source vocabulary for V2StageTokens.CostSource / StageResult.CostSource
// (Issue #682, extended by #890). Mirrors the TypeScript
// `cost_source: z.enum([...]).optional()` in
// packages/nightgauge-vscode/src/schemas/executionHistory.ts — Go is the sole
// writer of this field, TypeScript is the only validator, so the two
// vocabularies must never drift. TestHistorySchemaParity_StageTokens (in
// history_schema_parity_test.go) pins the FIELD NAME across both languages;
// TestCostSourcesPinnedToTSSchema (cost_source_parity_test.go) pins these
// VALUE strings against the Zod enum's literal members, following
// TestModelSelectionSourcesPinnedToSDK's pattern — it was written when #890
// added the fourth member and made the drift risk real. Add a value to
// CostSources below and to the TS enum in the same change, or that pin fails.
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

	// CostSourceDeterministic marks a stage that dispatched NO model at all
	// (Issue #890): the deterministic bookends and deterministic-path stages
	// — pipeline-start, pipeline-finish, issue-pickup, and the non-LLM
	// execution paths of pr-create / pr-merge — run compiled Go, spend no
	// tokens, and therefore cost exactly $0. Nothing was looked up, so the
	// pricing registry did not "miss": this is a genuine, intentional zero,
	// the same carve-out CostUnstamped already makes for the local-provider
	// (ollama/lm-studio) $0, and CostUnstamped is NOT set alongside it.
	//
	// Before #890 these stages priced through the unresolvable
	// (anthropic, "") pair and landed as CostSourceUnknown + unstamped. Four
	// of them are present in EVERY run, so the run-level OR in
	// computeAccumulatedTokens was true for every run ever recorded and
	// `nightgauge cost by-class` marked 100% of runs untrustworthy by
	// construction. Keeping a distinct label rather than silently unsetting
	// the flag is what preserves the difference between "ran nothing" and
	// "ran something we could not price" in the durable record.
	CostSourceDeterministic = "deterministic"
)

// CostSources is the closure list for the cost_source vocabulary: every value
// any writer emits MUST be listed here. TestCostSourcesPinnedToTSSchema reads
// the `cost_source: z.enum([...])` literal out of the TypeScript schema and
// compares the two sets, so a value added on one side and not the other fails
// at test time instead of silently failing Zod validation at read time. This
// is the value-level pin the package comment above used to note was missing.
var CostSources = []string{
	CostSourceNative,
	CostSourceComputed,
	CostSourceUnknown,
	CostSourceDeterministic,
}

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
	case CostSourceDeterministic:
		return 3
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
// (or empty) folds to unknown. deterministic sits one level ABOVE native (see
// costSourcePriority), so it never wins the fold against a label describing an
// occurrence that actually spent money.
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
