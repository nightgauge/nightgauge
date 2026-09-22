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
// agentic coding, the auto ceiling is Opus in every mode but one: only the
// `frontier` envelope (see performance_mode.go) lifts it, and even there only
// for a heavy reasoning stage at top complexity. Two explicit opt-ins reach it
// from any mode, because neither is clamped: a per-run model override and an
// explicit per-stage model (`pipeline.stage_models` /
// NIGHTGAUGE_PIPELINE_STAGE_MODEL_*). A `model_routing.minimum_model.<stage>:
// fable` floor is NOT one of them: floors land inside the stage's ROUTED-TIER
// envelope (RoutedTierEnvelope), so a non-frontier ceiling caps them at Opus,
// and even under `frontier` a floor reaches Fable only on feature-planning and
// feature-dev — feature-validate and the plumbing stages stay at Opus.
var (
	ModelHaiku  = mustCurrentModelID(models.BandHaiku)
	ModelSonnet = mustCurrentModelID(models.BandSonnet)
	ModelOpus   = mustCurrentModelID(models.BandOpus)
	ModelFable  = mustCurrentModelID(models.BandFable)
)

// currentModelForBand resolves a registry BAND back to the concrete Anthropic
// id this file's callers speak (selectModel returns ModelHaiku and friends, and
// Recommendation.Model is persisted as a concrete id). Anything the registry
// does not resolve is returned as-is.
func currentModelForBand(band string) string {
	if m, ok := models.Get(band); ok {
		return m.ID
	}
	return band
}

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

	// Apply the performance mode (reads fresh from disk on every call), through
	// the ONE mode table in performance_mode.go. A mode either PINS the stage
	// (only `maximum` does) or supplies an envelope the heuristic pick is
	// clamped into — mirroring MODE_PROFILES, where `efficiency.stages` and
	// `frontier.stages` are both `{}`.
	mode := resolvePerformanceMode(r.workspaceRoot)
	if pin := ModeStagePin(mode, stage); pin != "" {
		model = currentModelForBand(pin)
	} else {
		envelope := RoutedTierEnvelopeForWorkspace(r.workspaceRoot, mode, stage)
		// Frontier-reasoning escalation: a fable ceiling is the ONLY way
		// automatic routing reaches Fable, and only on a heavy reasoning stage
		// at top complexity. Mirrors AutoModelSelector.selectModel; applied
		// before the clamp, exactly as it is there.
		if envelope.Ceiling == TierFable && frontierReasoningStage(stage) &&
			cplx.Value >= frontierReasoningComplexity {
			model = ModelFable
		}
		if clamped := ClampToEnvelope(model, envelope); clamped != model {
			model = currentModelForBand(clamped)
		}
	}

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
	case cplx.Value <= 6 && stage == "feature-dev":
		rec.Reasoning = fmt.Sprintf("medium complexity (%d/10) — implementation routed by cost per closed issue: fewer turns and rework rounds outweigh the per-token premium", cplx.Value)
		rec.Alternatives = []Alternative{
			{Model: ModelSonnet, TradeOff: "half the per-token price, but measured at ~2x the turns on implementation and more rework rounds"},
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
//
// The rule it encodes is cost per CLOSED issue, not price per token. A turn
// re-reads the context it inherits, so an implementation stage's cost is
// dominated by how many turns it takes and how many paid rework rounds follow
// it. Measured on implementation work, the mid tier took roughly twice the
// turns of Opus and deferred or missed more, which costs further rounds; at
// half the price per token that is break-even or worse per closed issue. So
// feature-dev routes to Opus from mid complexity up. The mid tier stays where
// it measured cheap and clean — narrow, bounded work — and feature-validate,
// the adversarial review stage, keeps its table: Opus reviewers were measured
// catching defects Opus implementers still ship, and a high-risk issue gets an
// Opus floor there through RiskFloorBand. See docs/CONFIGURATION.md
// § Routing by cost per closed issue.
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
		if stage == "feature-dev" {
			return ModelOpus // cost per closed issue, not per token
		}
		return ModelSonnet
	default:
		if stage == "feature-dev" || stage == "feature-validate" {
			return ModelOpus
		}
		return ModelSonnet
	}
}

// routerScoreForSize places a size bucket on selectModel's 1-10 input scale, at
// the score complexity.Estimator itself maps to that bucket (1 XS, 3 S, 5 M,
// 7 L, 9 XL). routing.Decision scores on the Fibonacci 1/2/3/5/8 scale instead,
// and feeding that scale into selectModel would put an M issue (3) in the
// Haiku band — so ImplementationBand converts through the bucket.
var routerScoreForSize = map[string]int{"XS": 1, "S": 3, "M": 5, "L": 7, "XL": 9}

// ImplementationBand returns the band the scheduler dispatches feature-dev on
// for an issue with Decision d, or "" when the table does not put this issue's
// implementation on Opus and the stage keeps the tier it would otherwise run.
//
// It only ever RAISES to Opus, from selectModel's own feature-dev row: a
// bucket that row sends below Opus (XS, S) returns "" rather than a Haiku
// downgrade. The bucket is the Decision's priority-adjusted complexity mapped
// back through SizeForBaseScore, so a docs/config change (capped at 2) never
// qualifies and an M issue at priority:critical scores as L.
//
// A DEFAULTED size never qualifies (#1909): Derive assumes M when no board
// size, `size:*` label or planner assessment names one, and routing every
// unsized issue's implementation to Opus on that guess would spend the premium
// on no evidence. The scheduler re-derives once feature-planning has assessed a
// size, so an unsized issue that plans is routed from the planner's size.
func ImplementationBand(d Decision) string {
	if d.SizeSource == SizeSourceDefault {
		return ""
	}
	score, ok := routerScoreForSize[SizeForBaseScore(d.ComplexityScore)]
	if !ok {
		return ""
	}
	if selectModel("feature-dev", score) != ModelOpus {
		return ""
	}
	return models.BandOpus
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
// Input/output only — routing projects fresh per-stage context, so there is no
// cache term.
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
