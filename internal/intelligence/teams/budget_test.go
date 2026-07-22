package teams

import (
	"testing"
)

func TestSplitBudgetEqual(t *testing.T) {
	issues := []SubIssue{
		{Number: 1, Complexity: "simple"},
		{Number: 2, Complexity: "complex"},
		{Number: 3, Complexity: "medium"},
	}

	result := SplitBudget(issues, 90000, StrategyEqual)
	if len(result.Allocations) != 3 {
		t.Fatalf("expected 3 allocations, got %d", len(result.Allocations))
	}

	for _, a := range result.Allocations {
		if a.TokenBudget != 30000 {
			t.Errorf("issue %d budget = %d, want 30000", a.IssueNumber, a.TokenBudget)
		}
	}
	if result.Unallocated != 0 {
		t.Errorf("unallocated = %d, want 0", result.Unallocated)
	}
}

func TestSplitBudgetProportional(t *testing.T) {
	issues := []SubIssue{
		{Number: 1, Complexity: "simple"},  // weight 1
		{Number: 2, Complexity: "complex"}, // weight 3
	}
	// Total weight = 4, budget = 100000
	// simple: 25000, complex: 75000

	result := SplitBudget(issues, 100000, StrategyProportional)
	if len(result.Allocations) != 2 {
		t.Fatalf("expected 2 allocations, got %d", len(result.Allocations))
	}

	simple := result.Allocations[0]
	complex := result.Allocations[1]

	if simple.TokenBudget != 25000 {
		t.Errorf("simple budget = %d, want 25000", simple.TokenBudget)
	}
	if complex.TokenBudget != 75000 {
		t.Errorf("complex budget = %d, want 75000", complex.TokenBudget)
	}
}

func TestSplitBudgetEmpty(t *testing.T) {
	result := SplitBudget(nil, 100000, StrategyEqual)
	if result.Unallocated != 100000 {
		t.Errorf("unallocated = %d, want 100000", result.Unallocated)
	}
}

func TestSplitBudgetDefaultComplexity(t *testing.T) {
	issues := []SubIssue{
		{Number: 1}, // no complexity set — defaults to "medium" (weight 2)
		{Number: 2, Complexity: "medium"},
	}

	result := SplitBudget(issues, 100000, StrategyProportional)
	// Both should get equal allocation since both default to medium
	if result.Allocations[0].TokenBudget != result.Allocations[1].TokenBudget {
		t.Errorf("expected equal budgets for same complexity, got %d vs %d",
			result.Allocations[0].TokenBudget, result.Allocations[1].TokenBudget)
	}
}
