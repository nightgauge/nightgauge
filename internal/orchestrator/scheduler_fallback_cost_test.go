package orchestrator

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
)

// TestFallbackCostCalculation verifies that tokens.CalculateCost produces a
// non-zero value for known models, confirming the Go-side fallback used in
// scheduler.go (when actualCostUsd == 0) will never silently pass zero cost
// to the IPC stage.complete event when real tokens were consumed.
func TestFallbackCostCalculation(t *testing.T) {
	tests := []struct {
		model        string
		inputTokens  int
		outputTokens int
	}{
		{"claude-sonnet-4-6", 1000, 500},
		{"claude-haiku-4-5-20251001", 2000, 1000},
		{"claude-opus-4-7", 500, 250},
	}

	for _, tc := range tests {
		cost := tokens.CalculateCost(tc.model, tokens.TokenCounts{Input: tc.inputTokens, Output: tc.outputTokens})
		if cost <= 0 {
			t.Errorf("CalculateCost(%s, %d, %d) = %f; want > 0",
				tc.model, tc.inputTokens, tc.outputTokens, cost)
		}
	}
}

// TestFallbackCostZeroTokens verifies that zero tokens produces zero cost,
// so the Go fallback does not generate spurious non-zero costs for cache-hit
// or skipped stages.
func TestFallbackCostZeroTokens(t *testing.T) {
	cost := tokens.CalculateCost("claude-sonnet-4-6", tokens.TokenCounts{})
	if cost != 0 {
		t.Errorf("CalculateCost with zero tokens = %f; want 0", cost)
	}
}
