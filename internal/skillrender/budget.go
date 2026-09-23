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
// "spike-materialize" and "issue-refine" were measured the same way for
// #1969: `nightgauge skill render --stage <stage> --skills-root ./skills`,
// byte length of the base-only stdout render divided by 4 —
// spike-materialize (6246 bytes -> 1561) and issue-refine (26227 bytes ->
// 6556). Both land under minShare's floor of maxStageBaseTokens*0.35 (pr-merge
// is heaviest at 21700, so the floor is ~7595), so Share still returns
// minShare for them; the entries exist so a future re-measurement of pr-merge
// (the normalization denominator) does not silently change these two stages'
// share out from under an empty table lookup.
var stageBaseTokens = map[string]int{
	"issue-pickup":      16500,
	"feature-planning":  12300,
	"feature-dev":       11600,
	"feature-validate":  12600,
	"pr-create":         11200,
	"pr-merge":          21700,
	"spike-materialize": 1561,
	"issue-refine":      6556,
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
// and floored at minShare. A stage with no entry in stageBaseTokens — a
// future stage never added to the table — has no measurement to derive a
// bigger share from, so it gets the floor; issue-refine and
// spike-materialize are both measured (see stageBaseTokens) but land under
// the floor anyway, so they also return minShare today.
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

// CapacityRow is one row of the ADR-023 Q9 capacity table: a model whose
// context window is at least MinWindow tokens may take issues up to MaxSize.
type CapacityRow struct {
	MinWindow int
	MaxSize   string
}

// capacityTable is the ONE window → maximum-issue-size table (ADR 023 Q9,
// #1655). Every enforcement point reads it through MaxIssueSizeForWindow:
// `nightgauge size-gate check --context-window`, `nightgauge size-gate
// capacity` (which issue-create's scope gate calls) and the scheduler's
// dispatch-time capacity check. Rows ascend by MinWindow, and the last row a
// window reaches decides.
//
// The fit check above bounds one rendered stage prompt; this table bounds the
// work a whole run accumulates on top of it, which grows with the issue's
// size. The boundaries sit on the windows vendors quote (32k, 128k, 200k,
// 400k), so a model advertised as "32k" lands in the same row whether it
// reports 32,000 or 32,768 tokens. Below 32k only XS work is admitted; a 32k
// model takes XS and S; a 128k model (131,072 included) takes up to M; L and
// XL need the 200k and 400k windows.
var capacityTable = []CapacityRow{
	{MinWindow: 1, MaxSize: "XS"},
	{MinWindow: 32_000, MaxSize: "S"},
	{MinWindow: 128_000, MaxSize: "M"},
	{MinWindow: 200_000, MaxSize: "L"},
	{MinWindow: 400_000, MaxSize: "XL"},
}

// MaxIssueSizeForWindow returns the largest issue size a model with window
// tokens of context may take, per capacityTable. ok is false for an unknown
// window (window <= 0): no row applies and the caller applies no cap, the
// same fail-open rule Fit follows (ADR 023 § 4).
func MaxIssueSizeForWindow(window int) (maxSize string, ok bool) {
	if window <= 0 {
		return "", false
	}
	for i := len(capacityTable) - 1; i >= 0; i-- {
		if window >= capacityTable[i].MinWindow {
			return capacityTable[i].MaxSize, true
		}
	}
	return "", false
}
