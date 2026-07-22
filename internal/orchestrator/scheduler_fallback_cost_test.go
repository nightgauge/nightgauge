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
		cost := tokens.CalculateCost(tc.model, tc.inputTokens, tc.outputTokens)
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
	cost := tokens.CalculateCost("claude-sonnet-4-6", 0, 0)
	if cost != 0 {
		t.Errorf("CalculateCost with zero tokens = %f; want 0", cost)
	}
}

// TestSchedulerFallbackCostLogic mirrors the scheduler.go fallback pattern:
//
//	stageCostForCb := actualCostUsd
//	if stageCostForCb == 0 { stageCostForCb = tokens.CalculateCost(...) }
//
// Verifies the branch behaves correctly for both the zero and non-zero cases.
func TestSchedulerFallbackCostLogic(t *testing.T) {
	model := "claude-sonnet-4-6"
	inputTokens := 1000
	outputTokens := 500

	// Case 1: CLI reported a cost — use it as-is
	actualCostUsd := 0.0123
	stageCostForCb := actualCostUsd
	if stageCostForCb == 0 {
		stageCostForCb = tokens.CalculateCost(model, inputTokens, outputTokens)
	}
	if stageCostForCb != actualCostUsd {
		t.Errorf("expected CLI cost %f to be used unchanged, got %f", actualCostUsd, stageCostForCb)
	}

	// Case 2: CLI did not report cost (total_cost_usd absent) — fallback must produce > 0
	actualCostUsd = 0
	stageCostForCb = actualCostUsd
	if stageCostForCb == 0 {
		stageCostForCb = tokens.CalculateCost(model, inputTokens, outputTokens)
	}
	if stageCostForCb <= 0 {
		t.Errorf("fallback cost for %s (%d/%d tokens) = %f; want > 0",
			model, inputTokens, outputTokens, stageCostForCb)
	}
}
