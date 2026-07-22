// Package routing selects the optimal AI model for each pipeline stage
// based on complexity, cost budget, and historical performance.
package routing

import (
	"context"
	"fmt"

	"github.com/nightgauge/nightgauge/internal/intelligence/complexity"
	"github.com/nightgauge/nightgauge/internal/models"
	"github.com/nightgauge/nightgauge/internal/platform"
)

// Model IDs for the Claude family, ordered by capability and cost:
// Haiku < Sonnet < Opus < Fable. Resolved from the model registry at init so
// a model rotation (deprecating a dated ID) can never strand routing on a
// stale model (#50).
//
// Fable is the premium frontier tier — the most capable model, priced at
// ~2× Opus. Because Opus is already state-of-the-art for long-horizon
// agentic coding, automatic complexity routing NEVER escalates to Fable: the
// auto ceiling is Opus. Fable is reachable only via explicit opt-in — the
// `frontier` performance mode (see performance_mode.go), an explicit per-run
// model override, or a `model_routing.minimum_model.<stage>: fable` config
// entry.
var (
	ModelHaiku  = mustCurrentModelID("haiku")
	ModelSonnet = mustCurrentModelID("sonnet")
	ModelOpus   = mustCurrentModelID("opus")
	ModelFable  = mustCurrentModelID("fable")
)

// mustCurrentModelID resolves a tier to the registry's current non-deprecated
// model ID. The registry is embedded, so a missing tier is a build defect —
// panic mirrors the registry's own mustLoad.
func mustCurrentModelID(tier string) string {
	m, ok := models.Get(tier)
	if !ok {
		panic(fmt.Sprintf("routing: model registry has no current model for tier %q", tier))
	}
	return m.ID
}

// Recommendation is the model routing result.
type Recommendation struct {
	Model           string        `json:"model"`
	Reasoning       string        `json:"reasoning"`
	EstimatedCost   float64       `json:"estimatedCostUsd"`
	EstimatedTokens TokenEstimate `json:"estimatedTokens"`
	Alternatives    []Alternative `json:"alternatives"`
}

// TokenEstimate holds predicted token usage.
type TokenEstimate struct {
	Input  int `json:"input"`
	Output int `json:"output"`
}

// Alternative is a model the user could choose instead.
type Alternative struct {
	Model    string `json:"model"`
	TradeOff string `json:"tradeOff"`
}

// Router selects models based on stage and complexity.
type Router struct {
	platformClient *platform.Client
	workspaceRoot  string
}

// NewRouter creates a model router. workspaceRoot is the project root used to
// locate .nightgauge/performance-mode.yaml; pass "" to skip file-based
// mode resolution (env var and elevated default still apply).
func NewRouter(client *platform.Client, workspaceRoot string) *Router {
	return &Router{platformClient: client, workspaceRoot: workspaceRoot}
}

// Route selects the best model for a stage given complexity.
func (r *Router) Route(ctx context.Context, stage string, cplx complexity.Score) Recommendation {
	// Try platform API first
	if r.platformClient != nil && r.platformClient.IsOnline() {
		if rec, err := r.routeFromPlatform(ctx, stage, cplx); err == nil {
			return rec
		}
	}

	// Local routing fallback
	return r.routeLocal(stage, cplx)
}

// routeLocal applies the local heuristic routing algorithm.
func (r *Router) routeLocal(stage string, cplx complexity.Score) Recommendation {
	model := selectModel(stage, cplx.Value)

	// Apply performance-mode override (reads fresh from disk on every call).
	mode := resolvePerformanceMode(r.workspaceRoot)
	model = applyModeOverride(mode, stage, model)

	tokens := estimateTokens(stage, cplx.Value)
	cost := estimateCost(model, tokens)

	rec := Recommendation{
		Model:           model,
		EstimatedCost:   cost,
		EstimatedTokens: tokens,
	}

	switch {
	case cplx.Value <= 3:
		rec.Reasoning = fmt.Sprintf("low complexity (%d/10) — fast model sufficient", cplx.Value)
		rec.Alternatives = []Alternative{
			{Model: ModelSonnet, TradeOff: "better quality, ~3x cost"},
		}
	case cplx.Value <= 6:
		rec.Reasoning = fmt.Sprintf("medium complexity (%d/10) — balanced model", cplx.Value)
		rec.Alternatives = []Alternative{
			{Model: ModelHaiku, TradeOff: "faster, cheaper, may miss edge cases"},
			{Model: ModelOpus, TradeOff: "highest quality, ~3x cost"},
		}
	default:
		rec.Reasoning = fmt.Sprintf("high complexity (%d/10) — strongest model recommended", cplx.Value)
		rec.Alternatives = []Alternative{
			{Model: ModelSonnet, TradeOff: "cheaper, may need retries for complex logic"},
		}
	}

	if mode != ModeElevated {
		rec.Reasoning += fmt.Sprintf(" (performance-mode: %s)", mode)
	}

	return rec
}

// selectModel implements the local routing heuristic.
func selectModel(stage string, complexityScore int) string {
	// Lightweight stages always use haiku
	switch stage {
	case "issue-pickup", "pr-create", "pr-merge":
		return ModelHaiku
	}

	// Complexity-based routing for dev/planning/validate
	switch {
	case complexityScore <= 3:
		if stage == "feature-planning" {
			return ModelSonnet // Planning benefits from reasoning
		}
		return ModelHaiku
	case complexityScore <= 6:
		return ModelSonnet
	default:
		if stage == "feature-dev" || stage == "feature-validate" {
			return ModelOpus
		}
		return ModelSonnet
	}
}

// estimateTokens predicts token usage by stage and complexity.
func estimateTokens(stage string, complexityScore int) TokenEstimate {
	// Base tokens per stage
	baseInput := map[string]int{
		"issue-pickup":     2000,
		"feature-planning": 4000,
		"feature-dev":      8000,
		"feature-validate": 4000,
		"pr-create":        2000,
		"pr-merge":         1500,
	}
	baseOutput := map[string]int{
		"issue-pickup":     1000,
		"feature-planning": 3000,
		"feature-dev":      6000,
		"feature-validate": 3000,
		"pr-create":        1500,
		"pr-merge":         500,
	}

	input := baseInput[stage]
	output := baseOutput[stage]
	if input == 0 {
		input = 4000
	}
	if output == 0 {
		output = 2000
	}

	// Scale by complexity
	multiplier := 1.0 + float64(complexityScore-1)*0.15
	return TokenEstimate{
		Input:  int(float64(input) * multiplier),
		Output: int(float64(output) * multiplier),
	}
}

// estimateCost calculates estimated cost from model and tokens.
func estimateCost(model string, tokens TokenEstimate) float64 {
	// Pricing per 1M tokens (approximate, May 2025)
	inputPrice, outputPrice := modelPricing(model)
	return (float64(tokens.Input)*inputPrice + float64(tokens.Output)*outputPrice) / 1_000_000
}

// modelPricing returns the per-1M-token input/output price for a model,
// read from the model registry (the single pricing source). Unknown models
// price at a truthful $0 — matching tokens.CalculateCost — because the only
// unknown ids in practice are user-configured local models (#56).
func modelPricing(model string) (inputPerM, outputPerM float64) {
	if m, ok := models.Get(model); ok {
		return m.Rates.Input, m.Rates.Output
	}
	return 0, 0
}

// routeFromPlatform calls the platform API for model routing.
func (r *Router) routeFromPlatform(ctx context.Context, stage string, cplx complexity.Score) (Recommendation, error) {
	apiClient := r.platformClient.API()
	if apiClient == nil {
		return Recommendation{}, fmt.Errorf("no platform client")
	}

	// The platform API handles the routing; we just need to convert the response
	// For now, fall through to local routing since the platform may not be deployed
	return Recommendation{}, fmt.Errorf("platform routing not yet available")
}
