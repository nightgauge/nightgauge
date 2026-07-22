// Package tokens tracks token budgets and cost estimation for pipeline runs.
package tokens

import (
	"fmt"
	"sync"

	"github.com/nightgauge/nightgauge/internal/models"
)

// Budget tracks token usage against a budget for a pipeline run.
type Budget struct {
	mu sync.Mutex

	// Limits
	MaxInputTokens  int
	MaxOutputTokens int
	MaxCostUSD      float64

	// Usage
	InputTokens  int
	OutputTokens int
	CostUSD      float64

	// Per-stage tracking
	StageUsage map[string]*StageTokens
}

// StageTokens records per-stage token consumption.
type StageTokens struct {
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	CostUSD      float64 `json:"costUsd"`
}

// NewBudget creates a token budget with the given limits.
func NewBudget(maxInput, maxOutput int, maxCost float64) *Budget {
	return &Budget{
		MaxInputTokens:  maxInput,
		MaxOutputTokens: maxOutput,
		MaxCostUSD:      maxCost,
		StageUsage:      make(map[string]*StageTokens),
	}
}

// DefaultBudget returns a standard budget for a single pipeline run.
func DefaultBudget() *Budget {
	return NewBudget(200_000, 100_000, 5.00)
}

// Record adds token usage for a stage.
func (b *Budget) Record(stage string, input, output int, cost float64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.InputTokens += input
	b.OutputTokens += output
	b.CostUSD += cost

	usage, ok := b.StageUsage[stage]
	if !ok {
		usage = &StageTokens{}
		b.StageUsage[stage] = usage
	}
	usage.InputTokens += input
	usage.OutputTokens += output
	usage.CostUSD += cost
}

// Remaining returns tokens and cost remaining in the budget.
func (b *Budget) Remaining() (inputLeft, outputLeft int, costLeft float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.MaxInputTokens - b.InputTokens,
		b.MaxOutputTokens - b.OutputTokens,
		b.MaxCostUSD - b.CostUSD
}

// IsExhausted returns true if any budget dimension is exceeded.
func (b *Budget) IsExhausted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.InputTokens >= b.MaxInputTokens ||
		b.OutputTokens >= b.MaxOutputTokens ||
		b.CostUSD >= b.MaxCostUSD
}

// UsagePct returns the percentage of budget consumed (0-100).
func (b *Budget) UsagePct() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	inputPct := safePct(b.InputTokens, b.MaxInputTokens)
	outputPct := safePct(b.OutputTokens, b.MaxOutputTokens)
	costPct := safePctFloat(b.CostUSD, b.MaxCostUSD)

	// Return the highest utilization
	max := inputPct
	if outputPct > max {
		max = outputPct
	}
	if costPct > max {
		max = costPct
	}
	return max
}

// Summary returns a human-readable budget summary.
func (b *Budget) Summary() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return fmt.Sprintf("tokens: %d/%d in, %d/%d out | cost: $%.2f/$%.2f",
		b.InputTokens, b.MaxInputTokens,
		b.OutputTokens, b.MaxOutputTokens,
		b.CostUSD, b.MaxCostUSD)
}

// CostEstimate predicts total cost for remaining stages.
type CostEstimate struct {
	TotalCostUSD   float64             `json:"totalCostUsd"`
	TotalDuration  int                 `json:"totalDurationMinutes"`
	StageBreakdown []StageCostEstimate `json:"stageBreakdown"`
	Confidence     string              `json:"confidence"`
}

// StageCostEstimate is a per-stage cost prediction.
type StageCostEstimate struct {
	Stage   string  `json:"stage"`
	Model   string  `json:"model"`
	CostUSD float64 `json:"costUsd"`
	Minutes float64 `json:"minutes"`
}

// EstimateCost predicts the cost for a set of stages.
func EstimateCost(stages []string, complexityScore int) CostEstimate {
	var total float64
	var totalMinutes float64
	breakdown := make([]StageCostEstimate, 0, len(stages))

	for _, stage := range stages {
		model := defaultModelForEstimate(stage, complexityScore)
		tokens := estimateStageTokens(stage, complexityScore)
		cost := CalculateCost(model, tokens.Input, tokens.Output)
		minutes := estimateMinutes(stage, complexityScore)

		total += cost
		totalMinutes += minutes
		breakdown = append(breakdown, StageCostEstimate{
			Stage:   stage,
			Model:   model,
			CostUSD: cost,
			Minutes: minutes,
		})
	}

	confidence := "medium"
	if complexityScore <= 3 {
		confidence = "high"
	} else if complexityScore >= 8 {
		confidence = "low"
	}

	return CostEstimate{
		TotalCostUSD:   total,
		TotalDuration:  int(totalMinutes),
		StageBreakdown: breakdown,
		Confidence:     confidence,
	}
}

func defaultModelForEstimate(stage string, complexity int) string {
	switch stage {
	case "issue-pickup", "pr-create", "pr-merge":
		return "claude-haiku-4-5-20251001"
	default:
		if complexity <= 3 {
			return "claude-haiku-4-5-20251001"
		} else if complexity <= 6 {
			return "claude-sonnet-4-6"
		}
		return "claude-opus-4-8"
	}
}

type tokenPair struct{ Input, Output int }

func estimateStageTokens(stage string, complexity int) tokenPair {
	base := map[string]tokenPair{
		"issue-pickup":     {2000, 1000},
		"feature-planning": {4000, 3000},
		"feature-dev":      {8000, 6000},
		"feature-validate": {4000, 3000},
		"pr-create":        {2000, 1500},
		"pr-merge":         {1500, 500},
	}
	t, ok := base[stage]
	if !ok {
		t = tokenPair{4000, 2000}
	}
	mult := 1.0 + float64(complexity-1)*0.15
	return tokenPair{int(float64(t.Input) * mult), int(float64(t.Output) * mult)}
}

// CalculateCost returns the USD cost for the given model and token counts.
//
// Rates come from the single-source model registry (internal/models, canonical
// in packages/nightgauge-sdk/src/eval/model-registry.json). Models unknown to
// the registry cost a truthful $0 — never a fabricated tier default — because
// the only unknown ids in practice are user-configured local models
// (ollama/lm-studio), whose marginal cost IS zero (#56). Deprecated models
// (e.g. claude-opus-4-7, claude-sonnet-4-6) remain in the registry for
// historical cost replay. See #4169.
func CalculateCost(model string, inputTokens, outputTokens int) float64 {
	d, ok := models.Get(model)
	if !ok {
		return 0
	}
	return (float64(inputTokens)*d.Rates.Input + float64(outputTokens)*d.Rates.Output) / 1_000_000
}

func estimateMinutes(stage string, complexity int) float64 {
	base := map[string]float64{
		"issue-pickup":     1.0,
		"feature-planning": 3.0,
		"feature-dev":      8.0,
		"feature-validate": 4.0,
		"pr-create":        2.0,
		"pr-merge":         1.0,
	}
	m, ok := base[stage]
	if !ok {
		m = 3.0
	}
	return m * (1.0 + float64(complexity-1)*0.1)
}

func safePct(used, max int) float64 {
	if max <= 0 {
		return 0
	}
	return float64(used) / float64(max) * 100
}

func safePctFloat(used, max float64) float64 {
	if max <= 0 {
		return 0
	}
	return used / max * 100
}
