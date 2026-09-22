package skillrender

// budget.go answers one question a render never asked before #1645: does the
// composed stage prompt fit the model that will actually run it? Estimate and
// Fit are pure functions over already-rendered content — they measure
// reality, unlike internal/intelligence/routing/router.go's estimateTokens,
// which predicts a token count BEFORE a render exists for routing/cost
// purposes. The two must not be merged (ADR 023 § "Prior Art").
//
// Every constant and the per-stage share table below is the ONE place ADR-023
// Q1's budget-share answer lives, so #1654–#1664 never edit it directly.

// safetyMargin covers the tokenizer spread bytes/4 estimation carries (ADR
// 023 § 2): a raw estimate is scaled up by this factor before it is compared
// to a budget, never the budget scaled down — a single, one-directional
// margin keeps the arithmetic in Fit auditable from its own tests.
const safetyMargin = 1.15

// minShare is the floor Share never returns below, so no stage — measured or
// not — is starved of a workable prompt (ADR 023 § 1).
const minShare = 0.35

// harnessBaselineTokens, toolSchemaReserveTokens and compactionReserveTokens
// are the fixed, off-the-top reserves ADR 023 § 1 orders before a stage's own
// share is computed: the executing harness's own system prompt and tools, the
// adapter's tool-schema payload, and the adapter's reply/compaction reserve.
// They are conservative, adapter-agnostic constants — measured for OpenCode
// 1.18.30 (harnessBaselineTokens) or estimated defaults for the other two —
// rather than per-adapter figures, because Fit takes no adapter parameter:
// callers on every dispatch path (the CLI flag, the Go scheduler) share one
// reservation model. A future PR may key these per adapter once a dispatch
// consumer shows the generic figure over- or under-reserves in practice
// (ADR 023's own "measured per adapter at doctor time" language names that
// refinement, not this one).
const (
	harnessBaselineTokens   = 7400
	toolSchemaReserveTokens = 3000
	compactionReserveTokens = 4096
)

// toolOutputReserveFraction is the fraction of whatever remains after the
// fixed reserves above that is held back for tool output rather than offered
// to stage content (ADR 023 § 1).
const toolOutputReserveFraction = 0.15

// stageBaseTokens is the measured base-only render token table from issue
// #1645's own body ("Measured base-only renders (bytes/4)"). The absolute
// figures are not reused as an absolute budget — only their PROPORTIONS,
// normalized against the heaviest stage, become each stage's Share.
var stageBaseTokens = map[string]int{
	"issue-pickup":     16500,
	"feature-planning": 12300,
	"feature-dev":      11600,
	"feature-validate": 12600,
	"pr-create":        11200,
	"pr-merge":         21700,
}

// maxStageBaseTokens is the normalization denominator: the heaviest measured
// stage, pr-merge, whose Share is always 1.0.
func maxStageBaseTokens() int {
	max := 0
	for _, tokens := range stageBaseTokens {
		if tokens > max {
			max = tokens
		}
	}
	return max
}

// Estimate returns content's estimated token count: bytes/4, the same
// methodology behind the issue's own measured-token table and the ADR-020-era
// convention it continues (ADR 023 § 2).
func Estimate(content string) int {
	return len(content) / 4
}

// Share returns stage's fraction of the usable window (the window after
// usableWindow's reserves), normalized against the heaviest measured stage
// and floored at minShare. A stage with no entry in stageBaseTokens —
// issue-refine, or a future stage never added to the table — has no
// measurement to derive a bigger share from, so it gets the floor.
func Share(stage string) float64 {
	base, ok := stageBaseTokens[stage]
	if !ok {
		return minShare
	}
	share := float64(base) / float64(maxStageBaseTokens())
	if share < minShare {
		return minShare
	}
	return share
}

// FitResult is the fit verdict for a rendered stage against a model's context
// window — both `nightgauge skill render --context-window`'s verdict and the
// scheduler's context-budget refusal reason are built from its fields.
type FitResult struct {
	Fits            bool
	EstimatedTokens int
	Budget          int
	Window          int
	Share           float64
}

// usableWindow returns the portion of window left for stage content after
// the fixed reserves and the tool-output fraction of what remains (ADR 023
// § 1). Never negative: a window smaller than the fixed reserves alone
// leaves nothing.
func usableWindow(window int) int {
	remaining := window - harnessBaselineTokens - toolSchemaReserveTokens - compactionReserveTokens
	if remaining < 0 {
		return 0
	}
	return int(float64(remaining) * (1 - toolOutputReserveFraction))
}

// ProfileDecision is the ADR 023 §Q3 dispatch outcome for a stage whose full
// render is being checked against a model's window: dispatch it as rendered,
// re-render it compact, or refuse. It is the compact half of the ADR's
// "(1) one re-route hop → (2) refusal" order — the model-swap hop and its
// scheduler wiring are #1645's own, out of this package and out of #1654's
// file ownership; this function only ever chooses between the SAME model's
// full and compact renders.
type ProfileDecision string

const (
	// DecisionFits means the full render already fits; no re-route needed.
	DecisionFits ProfileDecision = "fits"
	// DecisionCompact means the full render did not fit, a compact profile
	// exists, and the compact render fits.
	DecisionCompact ProfileDecision = "compact"
	// DecisionRefuse means the full render did not fit and either no compact
	// profile exists or the compact render does not fit either.
	DecisionRefuse ProfileDecision = "refuse"
)

// DecideProfile checks fullContent against window first; only when it does
// NOT fit does it consult the compact render. hasCompactProfile distinguishes
// "no _profiles/compact.md for this stage" from "compact profile exists but
// renders empty" — the caller (the skillrender.Render result's own Profile
// field, ADR 023 §Q5) already knows which one happened and must not have that
// re-derived from an empty compactContent string, which is a legitimate
// (if degenerate) render rather than "absent."
//
// Returns the FitResult for whichever profile the decision is based on: the
// full result for DecisionFits and DecisionRefuse-with-no-compact-profile,
// otherwise the compact result.
func DecideProfile(stage string, fullContent string, window int, hasCompactProfile bool, compactContent string) (ProfileDecision, FitResult) {
	full := Fit(stage, fullContent, window)
	if full.Fits {
		return DecisionFits, full
	}
	if !hasCompactProfile {
		return DecisionRefuse, full
	}
	compact := Fit(stage, compactContent, window)
	if compact.Fits {
		return DecisionCompact, compact
	}
	return DecisionRefuse, compact
}

// Fit checks whether stage's rendered content fits window, applying
// safetyMargin to the raw estimate before comparing it to stage's share of
// the usable window (ADR 023 § 2).
//
// window <= 0 is the unknown-window case: it short-circuits to {Fits: true}
// per ADR 023 § 4's fail-open rule, so no caller has to special-case
// "unknown" at its own call site — a hosted model absent from the registry or
// an unresolved local descriptor renders and dispatches unchecked, exactly
// like OverlayKeys' existing fail-open contract for the same conditions.
func Fit(stage string, content string, window int) FitResult {
	if window <= 0 {
		return FitResult{Fits: true}
	}
	share := Share(stage)
	budget := int(float64(usableWindow(window)) * share)
	estimated := Estimate(content)
	adjusted := int(float64(estimated) * safetyMargin)
	return FitResult{
		Fits:            adjusted <= budget,
		EstimatedTokens: estimated,
		Budget:          budget,
		Window:          window,
		Share:           share,
	}
}
