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
		// Estimation has no cache model: the projection is per-stage fresh
		// context, so there is nothing cached to read or write.
		cost := CalculateCost(model, TokenCounts{Input: tokens.Input, Output: tokens.Output})
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
		return concreteForTier(models.BandHaiku)
	default:
		if complexity <= 3 {
			return concreteForTier(models.BandHaiku)
		} else if complexity <= 6 {
			return concreteForTier(models.BandSonnet)
		}
		return concreteForTier(models.BandOpus)
	}
}

// concreteForTier resolves a routing tier to the current non-deprecated model
// serving that band. Hardcoding concrete ids here is how this layer drifted a
// full model generation behind the registry (#74): the ids kept naming
// superseded models long after the band moved on, and nothing failed loudly.
// An unknown tier returns the tier name itself, which costs $0 via the
// unknown-model default rather than fabricating a price.
func concreteForTier(tier string) string {
	if m, ok := models.Get(tier); ok {
		return m.ID
	}
	return tier
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

// TokenCounts is one stage's billable token usage, split by the pools the
// vendor prices separately. Cache fields are zero-valued for callers that have
// no cache data, which is the honest reading: nothing was cached.
type TokenCounts struct {
	Input  int
	Output int
	// CacheRead is tokens served from an existing cache entry.
	CacheRead int
	// CacheCreation5m is cache writes bought with a 5-minute TTL.
	CacheCreation5m int
	// CacheCreation1h is cache writes bought with a 1-hour TTL.
	CacheCreation1h int
}

// NormalizeCacheCreation reconciles an optional TTL split with its flat
// cache-write total. Explicit split counts are preserved; any unclassified
// remainder is assigned to the cheaper 5-minute tier so no observed tokens are
// lost and computed cost remains a conservative floor.
func NormalizeCacheCreation(total, fiveMinute, oneHour int) (int, int) {
	fiveMinute = max(fiveMinute, 0)
	oneHour = max(oneHour, 0)
	if remainder := total - fiveMinute - oneHour; remainder > 0 {
		fiveMinute += remainder
	}
	return fiveMinute, oneHour
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
//
// All four billable pools are priced (#358). Cache dominates real agentic
// usage — on the captured haiku stage in internal/execution/testdata, cache
// read and cache creation are 88.9% of the bill (99.2% of the tokens), so an
// input+output-only formula under-reported that stage by 9x. A model whose
// registry entry omits a cache rate contributes $0 for that pool, matching the
// unknown-model shape.
//
// CONVENTION for unsplit cache-creation totals: a caller that knows only a
// single combined cache-creation count must put it in CacheCreation5m. That is
// the cheaper tier, so the resulting estimate is a floor rather than an
// overstatement. Claude's stream parser supplies the real split; this
// convention remains for adapters and historical inputs that expose only a
// flat total.
func CalculateCost(model string, t TokenCounts) float64 {
	d, ok := models.Get(model)
	if !ok {
		return 0
	}
	return priceCounts(d, t)
}

// CalculateCostForAdapter prices a stage's tokens using the concrete model's
// rates, resolved through the PROVIDER the serving adapter maps to (#585) —
// not through CalculateCost's anthropic-default lookup.
//
// CalculateCost's models.Get(model) == models.Resolve("anthropic", model) is
// only correct when the serving adapter genuinely IS Anthropic's. Every other
// caller in the pipeline dispatches a bare routing-tier alias ("haiku",
// "sonnet", "opus") whenever the CLI's own stream never reported a served
// model back — and Get/Resolve's provider default silently priced that alias
// at claude-sonnet's $3/$15 (or claude-haiku's $1/$5) even when the stage was
// actually served by grok-4.6 at $0.34/$1.02. Observed live on run 01a007d5
// (issue #583): feature-planning stamped $2.8989 (exactly the anthropic
// sonnet rate) for tokens that price at ~$0.26 under grok's own rates — an
// ~11x overstatement that poisons cost-per-success history and makes grok
// look an order of magnitude more expensive than it is.
//
// adapter == "" keeps CalculateCost's existing anthropic default — a caller
// that has not been updated to carry adapter context gets byte-identical
// behavior rather than silently degrading to the providerless "other" path.
//
// stamped reports whether cost is a priced figure:
//   - true when (provider, model) resolved a registry rate.
//   - true, cost 0, for the local providers (ollama/lm-studio): they carry no
//     registry rows BY DESIGN because their marginal cost genuinely IS zero
//     (#56) — that $0 is an honest answer, not a gap.
//   - false for every other unresolved (provider, model) pair — a REAL
//     (billed) provider whose concrete model this call could not price. The
//     caller MUST record this as explicitly unstamped/incomplete: never
//     fabricate $0 as if it were a priced answer, and never fall back to
//     another provider's rates (matches #528's cost criterion).
func CalculateCostForAdapter(adapter, model string, t TokenCounts) (cost float64, stamped bool) {
	provider := "anthropic"
	if adapter != "" {
		provider = models.ProviderForAdapter(adapter)
	}
	if d, ok := models.Resolve(provider, model); ok {
		return priceCounts(d, t), true
	}
	if provider == "ollama" || provider == "lm-studio" {
		return 0, true
	}
	return 0, false
}

// priceCounts prices every billable pool for an already-resolved model
// descriptor. Shared by CalculateCost and CalculateCostForAdapter so the two
// resolution strategies (anthropic-default vs. adapter-provider-aware) cannot
// drift into two different pricing formulas.
func priceCounts(d models.ModelDescriptor, t TokenCounts) float64 {
	total := float64(t.Input)*d.Rates.Input + float64(t.Output)*d.Rates.Output
	total += float64(t.CacheRead) * rate(d.Rates.CacheRead)
	total += float64(t.CacheCreation5m) * rate(d.Rates.CacheCreation5m)
	total += float64(t.CacheCreation1h) * rate(d.Rates.CacheCreation1h)
	return total / 1_000_000
}

// rate dereferences an optional registry rate. nil means this registry entry
// carries no rate for that pool — either the provider does not bill it or the
// rate is not recorded yet (#392) — so it contributes $0 rather than a guessed
// default.
func rate(r *float64) float64 {
	if r == nil {
		return 0
	}
	return *r
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
