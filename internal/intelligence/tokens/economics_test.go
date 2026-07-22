package tokens

import "testing"

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
	est := EstimateCost(stages, 5)

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
	low := EstimateCost([]string{"feature-dev"}, 2)
	high := EstimateCost([]string{"feature-dev"}, 9)

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
		got := CalculateCost(tc.model, 1_000_000, 1_000_000)
		if got != tc.wantTotal1M {
			t.Errorf("CalculateCost(%s, 1M, 1M) = %f, want %f", tc.model, got, tc.wantTotal1M)
		}
	}
}

func TestDefaultModelForEstimate_HighComplexityReturnsOpus(t *testing.T) {
	got := defaultModelForEstimate("feature-dev", 9)
	if got != "claude-opus-4-8" {
		t.Errorf("high-complexity feature-dev model = %s, want claude-opus-4-8", got)
	}
}

func TestCalculateCost_UnknownModelIsZero(t *testing.T) {
	// Models unknown to the registry (user-configured local ollama/lm-studio
	// models) cost a truthful $0 — never a fabricated sonnet default (#56).
	if got := CalculateCost("qwen3-coder:32b", 1_000_000, 1_000_000); got != 0 {
		t.Errorf("CalculateCost(unknown, 1M, 1M) = %f, want 0", got)
	}
}

func TestCalculateCost_NonAnthropicRegistryRates(t *testing.T) {
	// Non-Anthropic registry entries cost at their own rates now that the
	// registry carries every provider (#56).
	if got := CalculateCost("gemini-2.5-flash", 1_000_000, 1_000_000); got != 2.80 {
		t.Errorf("CalculateCost(gemini-2.5-flash, 1M, 1M) = %f, want 2.80", got)
	}
	if got := CalculateCost("gpt-5.5", 1_000_000, 1_000_000); got != 11.25 {
		t.Errorf("CalculateCost(gpt-5.5, 1M, 1M) = %f, want 11.25", got)
	}
}
