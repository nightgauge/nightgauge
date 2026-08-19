package tokens

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/models"
)

func TestBudget_Record(t *testing.T) {
	b := NewBudget(100_000, 50_000, 5.00)
	b.Record("feature-dev", 5000, 3000, 0.50)

	if b.InputTokens != 5000 {
		t.Errorf("input = %d, want 5000", b.InputTokens)
	}
	if b.OutputTokens != 3000 {
		t.Errorf("output = %d, want 3000", b.OutputTokens)
	}
	if b.CostUSD != 0.50 {
		t.Errorf("cost = %f, want 0.50", b.CostUSD)
	}

	usage := b.StageUsage["feature-dev"]
	if usage == nil || usage.InputTokens != 5000 {
		t.Error("stage usage not tracked")
	}
}

func TestBudget_Remaining(t *testing.T) {
	b := NewBudget(100_000, 50_000, 5.00)
	b.Record("feature-dev", 30_000, 20_000, 2.00)

	inLeft, outLeft, costLeft := b.Remaining()
	if inLeft != 70_000 {
		t.Errorf("input remaining = %d, want 70000", inLeft)
	}
	if outLeft != 30_000 {
		t.Errorf("output remaining = %d, want 30000", outLeft)
	}
	if costLeft != 3.00 {
		t.Errorf("cost remaining = %f, want 3.00", costLeft)
	}
}

func TestBudget_IsExhausted(t *testing.T) {
	b := NewBudget(10_000, 5_000, 1.00)
	if b.IsExhausted() {
		t.Error("fresh budget should not be exhausted")
	}

	b.Record("feature-dev", 10_000, 1_000, 0.50)
	if !b.IsExhausted() {
		t.Error("budget at input limit should be exhausted")
	}
}

func TestBudget_UsagePct(t *testing.T) {
	b := NewBudget(100_000, 50_000, 5.00)
	b.Record("feature-dev", 50_000, 10_000, 1.00)

	pct := b.UsagePct()
	if pct != 50.0 {
		t.Errorf("usage pct = %f, want 50.0", pct)
	}
}

func TestDefaultBudget(t *testing.T) {
	b := DefaultBudget()
	if b.MaxInputTokens != 200_000 {
		t.Errorf("default max input = %d, want 200000", b.MaxInputTokens)
	}
	if b.MaxCostUSD != 5.00 {
		t.Errorf("default max cost = %f, want 5.00", b.MaxCostUSD)
	}
}

func TestEstimateCost(t *testing.T) {
	stages := []string{"issue-pickup", "feature-planning", "feature-dev", "feature-validate", "pr-create", "pr-merge"}
	est := EstimateCost("claude", stages, 5)

	if est.TotalCostUSD <= 0 {
		t.Errorf("total cost = %f, want > 0", est.TotalCostUSD)
	}
	if len(est.StageBreakdown) != 6 {
		t.Errorf("breakdown stages = %d, want 6", len(est.StageBreakdown))
	}
	if est.Confidence != "medium" {
		t.Errorf("confidence = %s, want medium", est.Confidence)
	}
}

func TestEstimateCost_HighComplexity(t *testing.T) {
	low := EstimateCost("claude", []string{"feature-dev"}, 2)
	high := EstimateCost("claude", []string{"feature-dev"}, 9)

	if high.TotalCostUSD <= low.TotalCostUSD {
		t.Errorf("high complexity cost %f should exceed low %f", high.TotalCostUSD, low.TotalCostUSD)
	}
}

func TestCalculateCost_OpusPricing(t *testing.T) {
	// 1M input + 1M output tokens → input_price + output_price dollars
	cases := []struct {
		model       string
		wantInput   float64
		wantOutput  float64
		wantTotal1M float64
	}{
		{"claude-opus-4-8", 5.00, 25.00, 30.00},
		{"claude-opus-4-7", 5.00, 25.00, 30.00},
		// 4.6 pricing was previously $15/$75 (4.0/4.1 era); corrected to $5/$25
		// to match current Anthropic pricing so historical outcome replay is accurate.
		{"claude-opus-4-6", 5.00, 25.00, 30.00},
		// Fable 5 — premium frontier tier at ~2× Opus.
		{"claude-fable-5", 10.00, 50.00, 60.00},
		{"claude-sonnet-4-6", 3.00, 15.00, 18.00},
		// Haiku 4.5 pricing corrected from $0.80/$4.00 (launch-era) to $1/$5
		// to match Anthropic's current published pricing.
		{"claude-haiku-4-5-20251001", 1.00, 5.00, 6.00},
	}
	for _, tc := range cases {
		got := CalculateCost(tc.model, TokenCounts{Input: 1_000_000, Output: 1_000_000})
		if got != tc.wantTotal1M {
			t.Errorf("CalculateCost(%s, 1M, 1M) = %f, want %f", tc.model, got, tc.wantTotal1M)
		}
	}
}

// Asserts the BAND, resolved through the registry — not a pinned id. See the
// note on TestRecommendModel_ResolvesCurrentBandModel (#74): pinning the
// concrete id is what let this layer keep naming a superseded model.
func TestDefaultModelForEstimate_HighComplexityReturnsCurrentOpus(t *testing.T) {
	want, ok := models.Get("opus")
	if !ok {
		t.Fatal("registry has no non-deprecated opus-band model")
	}
	got := defaultModelForEstimate("anthropic", "feature-dev", 9)
	if got != want.ID {
		t.Errorf("high-complexity feature-dev model = %s, want %s (current opus band)", got, want.ID)
	}
}

// #696: the forecast and the recording must price one run from ONE rate card.
// Before the fix EstimateCost resolved every tier through the registry's
// anthropic default and priced it with CalculateCost, so a grok run was
// forecast at claude-sonnet's $3/$15 while CalculateCostForAdapter recorded it
// at grok-4.6's $0.34/$1.02 — an ~11x bias that made forecast-vs-actual
// variance structurally wrong rather than noisy (#583, run 01a007d5).
func TestEstimateCost_GrokPricesFromGrokRateCard(t *testing.T) {
	const stage = "feature-planning"
	const complexity = 5

	xai, ok := models.Resolve("xai", models.BandSonnet)
	if !ok {
		t.Fatal("registry has no non-deprecated xai sonnet-band model")
	}

	est := EstimateCost("grok", []string{stage}, complexity)
	if est.Provider != "xai" {
		t.Errorf("provider = %q, want xai", est.Provider)
	}
	if len(est.StageBreakdown) != 1 {
		t.Fatalf("breakdown stages = %d, want 1", len(est.StageBreakdown))
	}
	got := est.StageBreakdown[0]
	if !got.Stamped || !est.Stamped {
		t.Fatalf("grok forecast unstamped (stage=%v total=%v), want priced", got.Stamped, est.Stamped)
	}
	if got.Model != xai.ID {
		t.Errorf("forecast model = %q, want %q — the tier must resolve against the SERVING provider", got.Model, xai.ID)
	}

	// The money itself, derived straight from the xai rate card — so the
	// assertion fails on a wrong NUMBER, not only on a wrong model name.
	tk := estimateStageTokens(stage, complexity)
	counts := TokenCounts{Input: tk.Input, Output: tk.Output}
	wantCost := (float64(counts.Input)*xai.Rates.Input + float64(counts.Output)*xai.Rates.Output) / 1_000_000
	if got.CostUSD != wantCost {
		t.Errorf("grok forecast = $%.6f, want $%.6f from xai's own rate card", got.CostUSD, wantCost)
	}

	// The recording side prices the same tokens for the same adapter. The two
	// halves must agree to the cent; they cannot if one of them silently used
	// anthropic's card.
	recorded, stamped := CalculateCostForAdapter("grok", got.Model, counts)
	if !stamped {
		t.Fatal("CalculateCostForAdapter(grok) unstamped — fixture broken")
	}
	if got.CostUSD != recorded {
		t.Errorf("forecast $%.6f != recorded $%.6f for the same grok tokens", got.CostUSD, recorded)
	}

	// And it must not be the anthropic figure the old code produced.
	anthropicModel := defaultModelForEstimate("anthropic", stage, complexity)
	anthropicCost := CalculateCost(anthropicModel, counts)
	if got.CostUSD == anthropicCost {
		t.Errorf("grok forecast $%.6f equals the anthropic-rate figure for %s — still adapter-blind",
			got.CostUSD, anthropicModel)
	}
}

// An unpriceable (provider, tier) pair must SAY it is unpriced rather than
// return a confident anthropic-rate number — CalculateCostForAdapter's
// stamped contract (#585), now honored by the forecasting half too.
func TestEstimateCost_UnpriceableProviderIsUnstamped(t *testing.T) {
	// "vendor-mystery-cli" is not a known adapter → provider "other", which
	// carries no tier bands, so no stage can be priced.
	est := EstimateCost("vendor-mystery-cli", []string{"feature-dev"}, 5)
	if est.Provider != "other" {
		t.Fatalf("provider = %q, want other", est.Provider)
	}
	if est.Stamped {
		t.Error("estimate stamped for a provider with no rate card, want unstamped")
	}
	if est.TotalCostUSD != 0 {
		t.Errorf("unpriced total = %f, want 0 (absence of a price, not a price)", est.TotalCostUSD)
	}
	for _, s := range est.StageBreakdown {
		if s.Stamped {
			t.Errorf("stage %s stamped, want unstamped", s.Stage)
		}
	}
}

// Local providers keep #56's honest $0: stamped, because zero marginal cost is
// an answer, not a gap.
func TestEstimateCost_LocalProviderIsStampedZero(t *testing.T) {
	est := EstimateCost("ollama", []string{"feature-dev"}, 5)
	if !est.Stamped {
		t.Error("ollama estimate unstamped, want stamped $0 (#56)")
	}
	if est.TotalCostUSD != 0 {
		t.Errorf("ollama total = %f, want 0", est.TotalCostUSD)
	}
}

func TestCalculateCost_UnknownModelIsZero(t *testing.T) {
	// Models unknown to the registry (user-configured local ollama/lm-studio
	// models) cost a truthful $0 — never a fabricated sonnet default (#56).
	if got := CalculateCost("qwen3-coder:32b", TokenCounts{Input: 1_000_000, Output: 1_000_000}); got != 0 {
		t.Errorf("CalculateCost(unknown, 1M, 1M) = %f, want 0", got)
	}
}

func TestCalculateCost_NonAnthropicRegistryRates(t *testing.T) {
	// Non-Anthropic registry entries cost at their own rates now that the
	// registry carries every provider (#56).
	if got := CalculateCost("gemini-2.5-flash", TokenCounts{Input: 1_000_000, Output: 1_000_000}); got != 2.80 {
		t.Errorf("CalculateCost(gemini-2.5-flash, 1M, 1M) = %f, want 2.80", got)
	}
	// gpt-5.5 is $5.00 in / $30.00 out per 1M (live-verified 2026-08-09; see
	// packages/nightgauge-vscode/tests/utils/registryRatesLiveVerified.test.ts,
	// which pins every non-Anthropic rate to its cited vendor figure).
	if got := CalculateCost("gpt-5.5", TokenCounts{Input: 1_000_000, Output: 1_000_000}); got != 35.0 {
		t.Errorf("CalculateCost(gpt-5.5, 1M, 1M) = %f, want 35.00", got)
	}
}

// TestCalculateCostForAdapter_PinsRun01a007d5Regression pins the exact-match
// arithmetic observed live on run 01a007d5 (issue #583, adapter grok) —
// feature-planning: 484,709 in / 96,317 out. Before #585 this stamped
// $2.8989, exactly claude-sonnet's $3/$15 rate, because the cost call had no
// provider input and defaulted to Anthropic regardless of the serving
// adapter. It must now price at grok-4.6's registry rate ($0.34 in / $1.02
// out per MTok ≈ $0.263), and the SAME tokens on adapter claude must still
// stamp the pre-existing $2.8989-equivalent anthropic-rate figure — proving
// the fix is adapter-scoped, not a blanket rate change.
func TestCalculateCostForAdapter_PinsRun01a007d5Regression(t *testing.T) {
	counts := TokenCounts{Input: 484709, Output: 96317}

	grokCost, grokStamped := CalculateCostForAdapter("grok", "sonnet", counts)
	if !grokStamped {
		t.Fatal("grok/sonnet should resolve to a stamped cost (grok-4.6 serves the sonnet band)")
	}
	const wantGrok = 0.263044 // 484709*0.34/1e6 + 96317*1.02/1e6
	if diff := grokCost - wantGrok; diff > 5e-5 || diff < -5e-5 {
		t.Errorf("grok/sonnet cost = %.6f, want ~%.6f (grok-4.6 rates)", grokCost, wantGrok)
	}
	if grokCost >= 1.0 {
		t.Errorf("grok/sonnet cost = %.4f looks priced at anthropic rates ($2.8989), not grok's (~$0.26)", grokCost)
	}

	claudeCost, claudeStamped := CalculateCostForAdapter("claude", "sonnet", counts)
	if !claudeStamped {
		t.Fatal("claude/sonnet should resolve to a stamped cost")
	}
	const wantClaude = 2.8989 // 484709*3/1e6 + 96317*15/1e6, matches run 01a007d5's stamped figure
	if diff := claudeCost - wantClaude; diff > 1e-3 || diff < -1e-3 {
		t.Errorf("claude/sonnet cost = %.6f, want ~%.6f (anthropic sonnet rates, unchanged)", claudeCost, wantClaude)
	}
}

// TestCalculateCostForAdapter_EmptyAdapterMatchesCalculateCost pins the
// no-regression requirement: a caller that has not been updated to pass
// adapter context (adapter == "") must price EXACTLY like the pre-#585
// CalculateCost anthropic-default path — byte-identical, not merely close.
func TestCalculateCostForAdapter_EmptyAdapterMatchesCalculateCost(t *testing.T) {
	counts := TokenCounts{Input: 484709, Output: 96317}
	for _, model := range []string{"sonnet", "claude-sonnet-4-6", "claude-opus-4-8", "unknown-model-xyz"} {
		want := CalculateCost(model, counts)
		got, stamped := CalculateCostForAdapter("", model, counts)
		if got != want {
			t.Errorf("CalculateCostForAdapter(%q, %q) = %v, want CalculateCost's %v (byte-identical anthropic default)",
				"", model, got, want)
		}
		if model == "unknown-model-xyz" && stamped {
			t.Errorf("CalculateCostForAdapter(%q) unresolvable model should be unstamped", model)
		}
	}
}

// TestCalculateCostForAdapter_LocalProviderStaysIntentionalZero pins the
// pre-existing local-model convention (#56): ollama/lm-studio have no
// registry rows by design because their marginal cost genuinely IS zero.
// That must remain a STAMPED $0 — not get reclassified as "unstamped" now
// that unstamped exists for a different reason (an unresolved REAL provider).
func TestCalculateCostForAdapter_LocalProviderStaysIntentionalZero(t *testing.T) {
	counts := TokenCounts{Input: 1_000_000, Output: 1_000_000}
	for _, adapter := range []string{"ollama", "lm-studio"} {
		cost, stamped := CalculateCostForAdapter(adapter, "qwen3-coder:32b", counts)
		if !stamped {
			t.Errorf("adapter %q: local-model $0 should be stamped=true (intentional, not a gap)", adapter)
		}
		if cost != 0 {
			t.Errorf("adapter %q: cost = %v, want 0", adapter, cost)
		}
	}
}

// TestCalculateCostForAdapter_UnresolvedRealProviderIsUnstamped is the
// explicit-unstamped semantic the issue's acceptance criteria requires: when
// the serving provider is a REAL (billed) provider but the concrete model
// cannot be resolved against it, the result must be unstamped/incomplete —
// never a fabricated $0 and never another provider's rate.
func TestCalculateCostForAdapter_UnresolvedRealProviderIsUnstamped(t *testing.T) {
	counts := TokenCounts{Input: 1_000_000, Output: 1_000_000}
	cost, stamped := CalculateCostForAdapter("grok", "nonexistent-band-xyz", counts)
	if stamped {
		t.Error("unresolvable (xai, nonexistent-band-xyz) should be unstamped, not a priced figure")
	}
	if cost != 0 {
		t.Errorf("unstamped cost placeholder = %v, want 0 (never fabricate a nonzero price)", cost)
	}

	// And it must never silently borrow another provider's rate for the same
	// bare band name — grok's failure must not fall through to Anthropic's
	// sonnet price.
	anthropicSonnet := CalculateCost("sonnet", counts)
	if cost == anthropicSonnet {
		t.Error("unstamped cost accidentally matches anthropic's rate — cross-provider fallback regression")
	}
}
