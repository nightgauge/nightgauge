package tokens

import (
	"testing"
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

// TestCalculateCostFor_PinsRun01a007d5Regression pins the exact-match
// arithmetic observed live on run 01a007d5 (issue #583, adapter grok) —
// feature-planning: 484,709 in / 96,317 out. Before #585 this stamped
// $2.8989, exactly claude-sonnet's $3/$15 rate, because the cost call had no
// provider input and defaulted to Anthropic regardless of the serving
// adapter. It must now price at grok-4.6's registry rate ($0.34 in / $1.02
// out per MTok ≈ $0.263), and the SAME tokens on adapter claude must still
// stamp the pre-existing $2.8989-equivalent anthropic-rate figure — proving
// the fix is adapter-scoped, not a blanket rate change.
func TestCalculateCostFor_PinsRun01a007d5Regression(t *testing.T) {
	counts := TokenCounts{Input: 484709, Output: 96317}

	grokCost, grokStamped := CalculateCostFor("grok", "sonnet", counts)
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

	claudeCost, claudeStamped := CalculateCostFor("claude", "sonnet", counts)
	if !claudeStamped {
		t.Fatal("claude/sonnet should resolve to a stamped cost")
	}
	const wantClaude = 2.8989 // 484709*3/1e6 + 96317*15/1e6, matches run 01a007d5's stamped figure
	if diff := claudeCost - wantClaude; diff > 1e-3 || diff < -1e-3 {
		t.Errorf("claude/sonnet cost = %.6f, want ~%.6f (anthropic sonnet rates, unchanged)", claudeCost, wantClaude)
	}
}

// TestCalculateCostFor_EmptyAdapterMatchesCalculateCost pins the
// no-regression requirement: a caller that has not been updated to pass
// adapter context (adapter == "") must price EXACTLY like the pre-#585
// CalculateCost anthropic-default path — byte-identical, not merely close.
func TestCalculateCostFor_EmptyAdapterMatchesCalculateCost(t *testing.T) {
	counts := TokenCounts{Input: 484709, Output: 96317}
	for _, model := range []string{"sonnet", "claude-sonnet-4-6", "claude-opus-4-8", "unknown-model-xyz"} {
		want := CalculateCost(model, counts)
		got, stamped := CalculateCostFor("", model, counts)
		if got != want {
			t.Errorf("CalculateCostFor(%q, %q) = %v, want CalculateCost's %v (byte-identical anthropic default)",
				"", model, got, want)
		}
		if model == "unknown-model-xyz" && stamped {
			t.Errorf("CalculateCostFor(%q) unresolvable model should be unstamped", model)
		}
	}
}

// TestCalculateCostFor_LocalProviderStaysIntentionalZero pins the
// pre-existing local-model convention (#56): ollama/lm-studio have no
// registry rows by design because their marginal cost genuinely IS zero.
// That must remain a STAMPED $0 — not get reclassified as "unstamped" now
// that unstamped exists for a different reason (an unresolved REAL provider).
func TestCalculateCostFor_LocalProviderStaysIntentionalZero(t *testing.T) {
	counts := TokenCounts{Input: 1_000_000, Output: 1_000_000}
	for _, adapter := range []string{"ollama", "lm-studio"} {
		cost, stamped := CalculateCostFor(adapter, "qwen3-coder:32b", counts)
		if !stamped {
			t.Errorf("adapter %q: local-model $0 should be stamped=true (intentional, not a gap)", adapter)
		}
		if cost != 0 {
			t.Errorf("adapter %q: cost = %v, want 0", adapter, cost)
		}
	}
}

// TestCalculateCostFor_UnresolvedRealProviderIsUnstamped is the
// explicit-unstamped semantic the issue's acceptance criteria requires: when
// the serving provider is a REAL (billed) provider but the concrete model
// cannot be resolved against it, the result must be unstamped/incomplete —
// never a fabricated $0 and never another provider's rate.
func TestCalculateCostFor_UnresolvedRealProviderIsUnstamped(t *testing.T) {
	counts := TokenCounts{Input: 1_000_000, Output: 1_000_000}
	cost, stamped := CalculateCostFor("grok", "nonexistent-band-xyz", counts)
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

func TestModelForProviderBand(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		want     string
		wantOK   bool
	}{
		{"anthropic model translated to xai same band", "xai", "claude-sonnet-5", "grok-4.6", true},
		{"opus band translated to xai", "xai", "claude-opus-5", "grok-4.6", true},
		{"same provider is unchanged", "anthropic", "claude-sonnet-5", "claude-sonnet-5", true},
		{"empty provider is unchanged", "", "claude-sonnet-5", "claude-sonnet-5", true},
		{"empty model is unchanged", "xai", "", "", true},
		{"unknown model is left alone, not guessed", "xai", "some-local-model", "some-local-model", true},
		// "other" is the registry's bucket for unrecognised adapters and has
		// no tier bands, so there is nothing to translate to.
		{"provider serving no model in the band reports the band", "other", "claude-sonnet-5", "sonnet", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ModelForProviderBand(tt.provider, tt.model)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("ModelForProviderBand(%q, %q) = (%q, %v), want (%q, %v)",
					tt.provider, tt.model, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// The six EstimateCost tests that lived here are gone with the function
// (#1213). Their INTENT was not lost: the grok-rate-card, unpriceable-provider
// and local-provider-zero contracts they pinned (#696) are now asserted against
// the one surviving estimator, in
// packages/nightgauge-sdk/tests/analysis/AutoModelSelector.costEstimation.test.ts.
// The Go forecast they covered priced feature-dev at 8k input tokens against a
// measured 5.65M, so keeping it green would have pinned a wrong answer.

// TestCalculateCostForOpenCodeLocal: an opencode stage on a model a local
// provider serves is a stamped zero (ADR-022 § 3), in the -m form the stage
// was dispatched with and in the form its record carries (§ 2). Before #1630
// the adapter name priced it, and "opencode" maps to "other", so every local
// stage was unstamped.
func TestCalculateCostForOpenCodeLocal(t *testing.T) {
	counts := TokenCounts{Input: 1_000_000, Output: 1_000_000, CacheRead: 500_000, CacheCreation5m: 10_000}
	for _, model := range []string{
		"lmstudio/qwen/qwen3.8-27b",
		"ollama/qwen3-coder:30b",
		"lm-studio/qwen/qwen3.8-27b",
	} {
		cost, stamped := CalculateCostFor("opencode", model, counts)
		if cost != 0 || !stamped {
			t.Errorf("CalculateCostFor(opencode, %q) = (%v, %v), want (0, true): no provider bills a local model", model, cost, stamped)
		}
	}
}

// TestCalculateCostForOpenCodeCloudPriced: an opencode stage on a hosted
// model the registry lists is priced at that model's registry rates, exactly
// as the model's own vendor adapter prices it, whether the stage carries the
// -m value or the recorded bare id.
func TestCalculateCostForOpenCodeCloudPriced(t *testing.T) {
	counts := TokenCounts{Input: 1_000_000, Output: 1_000_000}
	want, wantStamped := CalculateCostFor("claude", "claude-sonnet-5", counts)
	if !wantStamped || want != 18.0 {
		t.Fatalf("CalculateCostFor(claude, claude-sonnet-5) = (%v, %v), want the registry's $3 + $15 per MTok", want, wantStamped)
	}
	for _, model := range []string{"anthropic/claude-sonnet-5", "claude-sonnet-5"} {
		got, stamped := CalculateCostFor("opencode", model, counts)
		if got != want || !stamped {
			t.Errorf("CalculateCostFor(opencode, %q) = (%v, %v), want (%v, true)", model, got, stamped, want)
		}
	}
	all := TokenCounts{Input: 1000, Output: 2000, CacheRead: 3000, CacheCreation5m: 4000, CacheCreation1h: 5000}
	got, _ := CalculateCostFor("opencode", "anthropic/claude-sonnet-5", all)
	if want, _ := CalculateCostFor("claude", "claude-sonnet-5", all); got != want {
		t.Errorf("cache pools priced %v through opencode, want %v as through claude", got, want)
	}
}

// TestCalculateCostForOpenCodeUnstamped: a hosted model the registry cannot
// price is unstamped, never a zero that reads as priced. That covers a model
// the registry does not list, a provider key Nightgauge does not recognize
// (which bills by its own rates even for an id the registry knows), and a
// tier band, which OpenCode never serves.
func TestCalculateCostForOpenCodeUnstamped(t *testing.T) {
	counts := TokenCounts{Input: 1_000_000, Output: 1_000_000}
	for _, model := range []string{
		"openrouter/x",
		"openrouter/meta-llama/llama-4",
		"openrouter/claude-sonnet-5",
		"openai/gpt-9-preview",
		"lmstudio-remote/qwen/qwen3.8-27b",
		"anthropic/sonnet",
		"sonnet",
		"",
	} {
		cost, stamped := CalculateCostFor("opencode", model, counts)
		if stamped || cost != 0 {
			t.Errorf("CalculateCostFor(opencode, %q) = (%v, %v), want (0, false)", model, cost, stamped)
		}
	}
}

// TestOpenCodeModelIdentity is ADR-022 § 1's fixture table: the model a
// stage record carries and its provider, from the -m value and from the
// recorded form alike. The endpoint rows wait for declared endpoints (#1679),
// so lmstudio-remote is an "other" key until then.
func TestOpenCodeModelIdentity(t *testing.T) {
	for _, tc := range []struct{ in, recorded, provider string }{
		{"lmstudio/qwen/qwen3.8-27b", "lm-studio/qwen/qwen3.8-27b", "lm-studio"},
		{"lm-studio/qwen/qwen3.8-27b", "lm-studio/qwen/qwen3.8-27b", "lm-studio"},
		{"ollama/qwen3-coder:30b", "ollama/qwen3-coder:30b", "ollama"},
		{"anthropic/claude-sonnet-5", "claude-sonnet-5", "anthropic"},
		{"claude-sonnet-5", "claude-sonnet-5", "anthropic"},
		{"openai/gpt-5.5", "gpt-5.5", "openai"},
		{"xai/grok-4.6", "grok-4.6", "xai"},
		{"google/gemini-2.5-pro", "gemini-2.5-pro", "google"},
		{"openai/gpt-9-preview", "openai/gpt-9-preview", "openai"},
		{"openrouter/meta-llama/llama-4", "openrouter/meta-llama/llama-4", "other"},
		{"lmstudio-remote/qwen/qwen3.8-27b", "lmstudio-remote/qwen/qwen3.8-27b", "other"},
		{"sonnet", "", ""},
		{"", "", ""},
	} {
		recorded, provider := OpenCodeModelIdentity(tc.in)
		if recorded != tc.recorded || provider != tc.provider {
			t.Errorf("OpenCodeModelIdentity(%q) = (%q, %q), want (%q, %q)", tc.in, recorded, provider, tc.recorded, tc.provider)
		}
	}
}
