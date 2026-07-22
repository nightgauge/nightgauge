// Package batch assesses epic sub-issues to recommend batch processing strategy
// (sequential vs parallel vs mixed).
package batch

import (
	"github.com/nightgauge/nightgauge/internal/intelligence/complexity"
)

// Strategy indicates the recommended batch processing approach.
type Strategy string

const (
	StrategySequential Strategy = "sequential"
	StrategyParallel   Strategy = "parallel"
	StrategyMixed      Strategy = "mixed"
)

// Assessment is the result of batch strategy analysis.
type Assessment struct {
	Strategy         Strategy        `json:"strategy"`
	Reasoning        string          `json:"reasoning"`
	EstimatedCostUSD float64         `json:"estimatedCostUsd"`
	EstimatedMinutes float64         `json:"estimatedMinutes"`
	IssueAssessments []IssueEstimate `json:"issues"`
}

// IssueEstimate holds per-issue analysis within a batch.
type IssueEstimate struct {
	IssueNumber      int     `json:"issueNumber"`
	ComplexityScore  int     `json:"complexityScore"`
	RecommendedModel string  `json:"recommendedModel"`
	EstimatedCostUSD float64 `json:"estimatedCostUsd"`
	HasDependencies  bool    `json:"hasDependencies"`
}

// Assessor analyzes epic sub-issues for batch strategy.
type Assessor struct {
	estimator *complexity.Estimator
}

// NewAssessor creates a batch assessor.
func NewAssessor() *Assessor {
	return &Assessor{
		estimator: complexity.NewEstimator(),
	}
}

// IssueInput holds the metadata for a single issue in the batch.
type IssueInput struct {
	Number    int
	Title     string
	Body      string
	Labels    []string
	BlockedBy []int // Issue numbers that block this one
}

// Assess analyzes a set of issues and recommends a batch strategy.
func (a *Assessor) Assess(issues []IssueInput) Assessment {
	if len(issues) == 0 {
		return Assessment{Strategy: StrategySequential, Reasoning: "no issues to process"}
	}

	estimates := make([]IssueEstimate, 0, len(issues))
	var totalCost, totalMinutes float64
	hasDeps := false
	highComplexityCount := 0

	for _, issue := range issues {
		score := a.estimator.Estimate(complexity.Input{
			Title:  issue.Title,
			Body:   issue.Body,
			Labels: issue.Labels,
		})

		model := recommendModel(score.Value)
		cost := estimateIssueCost(score.Value, model)
		blocked := len(issue.BlockedBy) > 0

		if blocked {
			hasDeps = true
		}
		if score.Value >= 7 {
			highComplexityCount++
		}

		totalCost += cost
		totalMinutes += estimateIssueMinutes(score.Value)

		estimates = append(estimates, IssueEstimate{
			IssueNumber:      issue.Number,
			ComplexityScore:  score.Value,
			RecommendedModel: model,
			EstimatedCostUSD: cost,
			HasDependencies:  blocked,
		})
	}

	strategy, reasoning := selectStrategy(issues, estimates, hasDeps, highComplexityCount)

	// Adjust time for parallel execution
	if strategy == StrategyParallel && len(issues) > 1 {
		totalMinutes = totalMinutes / float64(len(issues)) * 1.2 // 20% overhead
	}

	return Assessment{
		Strategy:         strategy,
		Reasoning:        reasoning,
		EstimatedCostUSD: totalCost,
		EstimatedMinutes: totalMinutes,
		IssueAssessments: estimates,
	}
}

func selectStrategy(issues []IssueInput, estimates []IssueEstimate, hasDeps bool, highComplexity int) (Strategy, string) {
	if len(issues) <= 2 {
		return StrategySequential, "small batch — sequential is simpler"
	}

	if hasDeps {
		// Check if only some have dependencies
		depsCount := 0
		for _, e := range estimates {
			if e.HasDependencies {
				depsCount++
			}
		}
		if depsCount == len(estimates) {
			return StrategySequential, "all issues have dependencies — must process sequentially"
		}
		return StrategyMixed, "some issues have dependencies — independent ones can run in parallel"
	}

	if highComplexity > len(issues)/2 {
		return StrategySequential, "majority high-complexity issues — sequential avoids resource contention"
	}

	return StrategyParallel, "independent low/medium complexity issues — parallel is efficient"
}

func recommendModel(complexityScore int) string {
	switch {
	case complexityScore <= 3:
		return "claude-haiku-4-5-20251001"
	case complexityScore <= 6:
		return "claude-sonnet-4-6"
	default:
		return "claude-opus-4-8"
	}
}

func estimateIssueCost(complexityScore int, model string) float64 {
	// Rough estimate: 6 stages per issue
	baseCost := 0.10
	switch model {
	case "claude-haiku-4-5-20251001":
		baseCost = 0.05
	case "claude-sonnet-4-6":
		baseCost = 0.30
	case "claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6":
		baseCost = 1.50
	case "claude-fable-5":
		baseCost = 3.00 // premium frontier tier — ~2× Opus
	}
	multiplier := 1.0 + float64(complexityScore-1)*0.15
	return baseCost * 6 * multiplier // 6 stages
}

func estimateIssueMinutes(complexityScore int) float64 {
	base := 15.0 // Base pipeline time in minutes
	return base * (1.0 + float64(complexityScore-1)*0.1)
}
