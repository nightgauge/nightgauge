package teams

import (
	"math"
)

// BudgetStrategy defines how tokens are allocated.
type BudgetStrategy string

const (
	StrategyProportional BudgetStrategy = "proportional"
	StrategyEqual        BudgetStrategy = "equal"
)

// BudgetAllocation is the token budget for a single issue.
type BudgetAllocation struct {
	IssueNumber int     `json:"issueNumber"`
	TokenBudget int     `json:"tokenBudget"`
	Percentage  float64 `json:"percentage"`
}

// BudgetResult is the output of budget splitting.
type BudgetResult struct {
	Allocations []BudgetAllocation `json:"allocations"`
	Strategy    BudgetStrategy     `json:"strategy"`
	TotalBudget int                `json:"totalBudget"`
	Unallocated int                `json:"unallocated"`
}

// complexityWeight maps complexity labels to weights.
var complexityWeight = map[string]float64{
	"simple":  1.0,
	"medium":  2.0,
	"complex": 3.0,
}

// SplitBudget allocates a token budget across issues using the given strategy.
func SplitBudget(issues []SubIssue, totalBudget int, strategy BudgetStrategy) BudgetResult {
	n := len(issues)
	if n == 0 {
		return BudgetResult{
			Strategy:    strategy,
			TotalBudget: totalBudget,
			Unallocated: totalBudget,
		}
	}

	result := BudgetResult{
		Strategy:    strategy,
		TotalBudget: totalBudget,
	}

	switch strategy {
	case StrategyEqual:
		result.Allocations = splitEqual(issues, totalBudget)
	default: // proportional
		result.Allocations = splitProportional(issues, totalBudget)
	}

	// Calculate unallocated remainder
	allocated := 0
	for _, a := range result.Allocations {
		allocated += a.TokenBudget
	}
	result.Unallocated = totalBudget - allocated

	return result
}

func splitEqual(issues []SubIssue, totalBudget int) []BudgetAllocation {
	n := len(issues)
	perIssue := totalBudget / n
	pct := 1.0 / float64(n)

	allocs := make([]BudgetAllocation, n)
	for i, issue := range issues {
		allocs[i] = BudgetAllocation{
			IssueNumber: issue.Number,
			TokenBudget: perIssue,
			Percentage:  pct,
		}
	}
	return allocs
}

func splitProportional(issues []SubIssue, totalBudget int) []BudgetAllocation {
	n := len(issues)
	weights := make([]float64, n)
	totalWeight := 0.0

	for i, issue := range issues {
		w := complexityWeight[issue.Complexity]
		if w == 0 {
			w = 2.0 // default to medium
		}
		weights[i] = w
		totalWeight += w
	}

	allocs := make([]BudgetAllocation, n)
	for i, issue := range issues {
		fraction := weights[i] / totalWeight
		tokens := int(math.Floor(fraction * float64(totalBudget)))
		allocs[i] = BudgetAllocation{
			IssueNumber: issue.Number,
			TokenBudget: tokens,
			Percentage:  fraction,
		}
	}
	return allocs
}
