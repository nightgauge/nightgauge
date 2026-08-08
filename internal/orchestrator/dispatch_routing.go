package orchestrator

import (
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/state"
)

// dispatch_routing.go holds the per-stage BASE model routing the Go dispatch
// path applies before any escalation, floor or downgrade (#340).
//
// It exists because #340 made `RunStageParams.Model` authoritative: the
// extension now executes the wire value instead of running its own
// `resolveModel` chain. Everything that chain contributed had to move here or
// stop existing. What moved: the performance-mode pin, `pipeline.stage_models`
// + its `NIGHTGAUGE_PIPELINE_STAGE_MODEL_*` env overrides, `model_routing.mode`
// (manual/automatic/hybrid), and the lightweight-stage defaults. Without them a
// high-complexity issue ran `issue-pickup` and `pr-create` on Opus, Maximum
// mode stopped pinning anything, Efficiency stopped capping anything, and the
// only remaining knob (`model_routing.minimum_model`) could raise a stage but
// never cap one.
//
// Every table here mirrors a named TypeScript counterpart, and each mirror is
// annotated with it. This is duplication, deliberately: threading TS config
// through the wire would put the extension back in the routing business, which
// is the drift #340 removed. Keep the pairs in sync.

// defaultStageModels is the `manual` routing mode's built-in table — the
// answer when the operator asked for explicit routing but named no model for
// this stage. Mirrors DEFAULT_STAGE_MODELS in
// packages/nightgauge-vscode/src/utils/resolvers/stageResolver.ts.
//
// Consulted ONLY in manual mode. In automatic/hybrid a stage with no explicit
// entry defers to the router, exactly as `getStageModel` returns undefined.
var defaultStageModels = map[state.PipelineStage]string{
	state.StageIssuePickup:     "haiku",
	state.StageFeaturePlanning: "sonnet",
	state.StageFeatureDev:      "sonnet",
	state.StageFeatureValidate: "sonnet",
	state.StagePRCreate:        "haiku",
	// sonnet, not haiku (#197): the pr-merge LLM path only runs when the
	// deterministic runner punted — i.e. exclusively on the judgment-heavy
	// instances. Issue size does not predict punt difficulty.
	state.StagePRMerge: "sonnet",
}

// lightweightStageDefaults is the automatic/hybrid base for stages whose LLM
// role is structured and shallow. Mirrors LIGHTWEIGHT_STAGE_DEFAULTS in
// packages/nightgauge-vscode/src/utils/skillRunner.ts.
//
// These are BASES, not caps: the pr-create large-diff escalation, the
// minimum_model floor and post-failure escalation all still raise them. pr-merge
// is deliberately absent (#197) — its LLM path runs only on deterministic
// punts, which are the hardest merges, so the cheapest tier is the wrong
// default there.
var lightweightStageDefaults = map[state.PipelineStage]string{
	state.StageIssuePickup: "haiku",
	state.StagePRCreate:    "haiku",
}

// modelRoutingMode resolves `model_routing.mode` with the same precedence as
// the TS getModelRoutingMode (modelResolver.ts): the
// NIGHTGAUGE_MODEL_ROUTING_MODE env var wins over config, and anything
// unrecognized (or absent) resolves to "automatic".
func modelRoutingMode(cfg *config.Config) string {
	valid := func(m string) bool {
		return m == "manual" || m == "automatic" || m == "hybrid"
	}
	if env := strings.TrimSpace(os.Getenv("NIGHTGAUGE_MODEL_ROUTING_MODE")); valid(env) {
		return env
	}
	if cfg == nil || cfg.ModelRouting == nil {
		return "automatic"
	}
	if m := strings.TrimSpace(cfg.ModelRouting.Mode); valid(m) {
		return m
	}
	return "automatic"
}

// stageConfiguredModel resolves the operator's explicit per-stage model, or ""
// when the stage defers to the router. Mirrors `getStageModel`
// (stageResolver.ts) precedence exactly:
//
//  1. NIGHTGAUGE_PIPELINE_STAGE_MODEL_{STAGE} — every mode, highest priority
//  2. pipeline.stage_models.{stage}          — manual/hybrid only
//  3. defaultStageModels[stage]              — manual only
//  4. "" (defer to the router)               — automatic/hybrid
func stageConfiguredModel(workspaceRoot string, stage state.PipelineStage) string {
	envKey := "NIGHTGAUGE_PIPELINE_STAGE_MODEL_" +
		strings.ToUpper(strings.ReplaceAll(string(stage), "-", "_"))
	if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
		return v
	}

	cfg, err := config.Load(workspaceRoot)
	if err != nil {
		cfg = nil
	}
	mode := modelRoutingMode(cfg)
	if mode == "automatic" {
		return ""
	}

	if cfg != nil && cfg.Pipeline != nil {
		if v := strings.TrimSpace(cfg.Pipeline.StageModels[string(stage)]); v != "" {
			return v
		}
	}

	if mode == "manual" {
		return defaultStageModels[stage]
	}
	return ""
}

// stageBaseModel resolves the tier a stage STARTS from, before escalation, the
// minimum_model floor, sticky downgrades and the stage-specific haiku
// exclusions (#340). Mirrors the ordering of `resolveModel` in
// packages/nightgauge-vscode/src/utils/skillRunner.ts:
//
//	Step 0   performance-mode pin        — Maximum pins Opus; Efficiency and
//	                                       Frontier pin their per-stage tiers
//	Step 1   explicit per-stage config   — env var / pipeline.stage_models
//	Step 1.5 lightweight stage defaults  — issue-pickup, pr-create → haiku
//	Step 2   the run's routed tier       — pickup_recommendation.dev_model
//	Step 3   defaultDispatchModel        — an unrouted run still dispatches
//
// The performance mode and the config are read fresh per stage, like the
// router's own mode lookup: an operator who switches to Maximum mid-run gets it
// on the next stage rather than at the next pickup.
func stageBaseModel(workspaceRoot string, stage state.PipelineStage, predictedModel string) string {
	mode := routing.ResolvePerformanceMode(workspaceRoot)
	if pin := routing.ModePin(mode, string(stage)); pin != "" {
		return pin
	}
	if configured := stageConfiguredModel(workspaceRoot, stage); configured != "" {
		return configured
	}
	if lightweight, ok := lightweightStageDefaults[stage]; ok {
		return lightweight
	}
	if predictedModel != "" {
		return predictedModel
	}
	return defaultDispatchModel
}

// normalizeDispatchTier collapses a dispatch model onto the registry BAND
// vocabulary (#340) — the single vocabulary the wire, both executors and every
// ladder in this package speak.
//
// Two producers put concrete ids into the dispatch value: the router's
// recommendation (`routing.ModelSonnet` and friends are mustCurrentModelID
// results, persisted into `pickup_recommendation.dev_model`), and any
// operator-supplied tier. Leaving both vocabularies on the wire is not a
// cosmetic inconsistency: every band-keyed consumer downstream fails SILENTLY
// against a concrete id. In the extension that is `modelSupportsEffort`
// (dropping `--effort` on every dispatch) and the mode/tier predicate that
// keeps a Maximum-mode Codex/Gemini/Copilot run off the adapter's default
// model; in this package it is `RetryEngine.NextModel`, which walks a literal
// [haiku sonnet opus] ladder a dated id is not a member of.
//
// Models the registry does not know — user-defined local models (#56) — pass
// through untouched: there is no band to collapse them onto, and inventing one
// would reroute an explicit local-model choice.
func normalizeDispatchTier(model string) string {
	if tier := NormalizeModelTier(model); tier != "" {
		return tier
	}
	return model
}

// raiseStageFloors returns floors with every pipeline stage floored at `tier`,
// keeping any configured floor that is already stronger.
//
// Used for the run.retryWithEscalation forced tier (ADR 015 §B). The forced
// tier also becomes the run's predicted model, but a prediction alone is not
// enough once stageBaseModel exists: the lightweight defaults sit ABOVE the
// prediction, so an operator escalating a stalled pr-create would have watched
// it re-run on haiku. "Run at least this tier" is a floor, so it is expressed
// as one.
func raiseStageFloors(floors map[string]string, tier string) map[string]string {
	if strings.TrimSpace(tier) == "" {
		return floors
	}
	raised := make(map[string]string, len(floors)+len(dispatchStages))
	for k, v := range floors {
		raised[k] = v
	}
	for _, stage := range dispatchStages {
		key := string(stage)
		if cur, ok := raised[key]; ok && tierRank(cur) >= tierRank(tier) {
			continue
		}
		raised[key] = tier
	}
	return raised
}

// dispatchStages is every stage the scheduler can dispatch, in pipeline order.
// Used where a rule applies run-wide rather than per stage.
var dispatchStages = []state.PipelineStage{
	state.StageIssuePickup,
	state.StageFeaturePlanning,
	state.StageFeatureDev,
	state.StageFeatureValidate,
	state.StagePRCreate,
	state.StagePRMerge,
	state.StageSpikeMaterialize,
}
