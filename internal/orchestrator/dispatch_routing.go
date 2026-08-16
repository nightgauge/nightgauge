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
// stop existing. What moved: the performance-mode pin AND its envelope,
// `pipeline.stage_models` + its `NIGHTGAUGE_PIPELINE_STAGE_MODEL_*` env
// overrides, `model_routing.mode` (manual/automatic/hybrid), the
// lightweight-stage defaults, and `ui.core.default_model` /
// `NIGHTGAUGE_UI_CORE_DEFAULT_MODEL` (Step 3 — pre-#340 that was the EFFECTIVE
// model for every reasoning stage on the IPC path, because services/
// SkillRunner.ts passed no issue metadata so Step 2 never fired). Without them
// a high-complexity issue ran `issue-pickup` and `pr-create` on Opus, Maximum
// mode stopped pinning anything, Efficiency stopped capping anything, and the
// only remaining knob (`model_routing.minimum_model`) could raise a stage but
// never cap one.
//
// What did NOT move, stated so the gap is not silent: the adaptive-policy
// override (Step 1.6), the A/B experiment assignment (Step 1.7) and
// `AutoModelSelector` (Step 2 — Go routes from the issue's complexity score at
// pickup instead). Those three are simply not consulted on an autonomous run,
// and docs/PIPELINE_EXECUTION.md names the same three.
//
// Every table here mirrors a named TypeScript counterpart, and each mirror is
// annotated with it. This is duplication, deliberately: threading TS config
// through the wire would put the extension back in the routing business, which
// is the drift #340 removed. Keep the pairs in sync.
//
// The performance mode is the one exception: it is NOT copied here. Both
// callers read routing.modeProfiles (internal/intelligence/routing/
// performance_mode.go), the Go mirror of MODE_PROFILES — one table for the
// router's recommendation at pickup and for this file's per-stage resolution.

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
//
// Validity is state.IsModelRoutingMode, not a locally hand-listed set — this
// is also the vocabulary resolveDispatchSelectionMode
// (dispatch_envelope.go) feeds straight into model_selection.mode, and
// state.ModelRoutingModes is what TestModelSelectionModePinnedToModelRoutingModeSchema
// pins against the TS ModelRoutingModeSchema authority (#580, resolves the
// #446 lesson for the mode axis).
func modelRoutingMode(cfg *config.Config) string {
	if env := strings.TrimSpace(os.Getenv("NIGHTGAUGE_MODEL_ROUTING_MODE")); state.IsModelRoutingMode(env) {
		return env
	}
	if cfg == nil || cfg.ModelRouting == nil {
		return "automatic"
	}
	if m := strings.TrimSpace(cfg.ModelRouting.Mode); state.IsModelRoutingMode(m) {
		return m
	}
	return "automatic"
}

// validStageModel keeps a per-stage operator value to the four registry bands,
// mirroring getStageModel's `validModels` guard (stageResolver.ts): anything
// else — a typo, a concrete provider id — is ignored rather than dispatched.
// Without the same guard on this side a value one resolver drops would be
// honored by the other, which is the drift #340 removed.
func validStageModel(model string) string {
	switch model {
	case routing.TierHaiku, routing.TierSonnet, routing.TierOpus, routing.TierFable:
		return model
	}
	return ""
}

// stageEnvModel returns the NIGHTGAUGE_PIPELINE_STAGE_MODEL_{STAGE} override,
// or "" when it is unset or not a registry band.
//
// It is separate from stageConfiguredModel because it resolves in a different
// PLACE: this override wins in every performance mode, ahead of a mode pin,
// while the rest of the explicit chain sits behind one. See stageBaseModel.
func stageEnvModel(stage state.PipelineStage) string {
	envKey := "NIGHTGAUGE_PIPELINE_STAGE_MODEL_" +
		strings.ToUpper(strings.ReplaceAll(string(stage), "-", "_"))
	return validStageModel(strings.TrimSpace(os.Getenv(envKey)))
}

// stageConfiguredModel resolves the operator's explicit per-stage model from
// CONFIG, or "" when the stage defers to the router. Mirrors the config half of
// `getStageModel` (stageResolver.ts) exactly:
//
//  1. pipeline.stage_models.{stage} — manual/hybrid only
//  2. defaultStageModels[stage]     — manual only
//  3. "" (defer to the router)      — automatic/hybrid
//
// The env override is resolved by stageEnvModel, ahead of the mode pin.
func stageConfiguredModel(workspaceRoot string, stage state.PipelineStage) string {
	cfg, err := config.Load(workspaceRoot)
	if err != nil {
		cfg = nil
	}
	mode := modelRoutingMode(cfg)
	if mode == "automatic" {
		return ""
	}

	if cfg != nil && cfg.Pipeline != nil {
		if v := validStageModel(strings.TrimSpace(cfg.Pipeline.StageModels[string(stage)])); v != "" {
			return v
		}
	}

	if mode == "manual" {
		return defaultStageModels[stage]
	}
	return ""
}

// workspaceDefaultModel resolves `ui.core.default_model` with the same
// precedence as the TS getDefaultModel (modelResolver.ts): the
// NIGHTGAUGE_UI_CORE_DEFAULT_MODEL env var wins over config, and a value that
// is not one of the four registry bands is ignored rather than dispatched.
//
// This is `resolveModel` Step 3, and it is not decoration on the Go side: on
// the IPC path it used to be the EFFECTIVE model for every reasoning stage,
// because services/SkillRunner.ts passed no issue metadata, so Step 2
// (AutoModelSelector) never fired and Step 3 always won.
func workspaceDefaultModel(workspaceRoot string) string {
	if env := validStageModel(strings.TrimSpace(os.Getenv("NIGHTGAUGE_UI_CORE_DEFAULT_MODEL"))); env != "" {
		return env
	}
	cfg, err := config.Load(workspaceRoot)
	if err != nil || cfg == nil || cfg.UI == nil || cfg.UI.Core == nil {
		return ""
	}
	return validStageModel(strings.TrimSpace(cfg.UI.Core.DefaultModel))
}

// stageBaseModel resolves the tier a stage STARTS from, before escalation, the
// minimum_model floor, sticky downgrades and the stage-specific haiku
// exclusions (#340). Mirrors `resolveModel`
// (packages/nightgauge-vscode/src/utils/skillRunner.ts) step for step:
//
//	Step 0   performance-mode pin        — ONLY `maximum` pins (Opus, every
//	                                       stage). efficiency/frontier are
//	                                       ENVELOPES (#19), applied below.
//	                                       The NIGHTGAUGE_PIPELINE_STAGE_MODEL_*
//	                                       override is read FIRST, so it wins in
//	                                       every mode.
//	Step 1   explicit per-stage config   — pipeline.stage_models, then the
//	                                       manual-mode table
//	Step 1.5 lightweight stage defaults  — issue-pickup, pr-create → haiku
//	Step 2   the run's routed tier       — pickup_recommendation.dev_model
//	Step 3   ui.core.default_model       — the workspace-wide fallback
//	Step 4   defaultDispatchModel        — an unrouted run still dispatches
//
// Steps 1.5–4 are clamped into the mode's routed-tier envelope, at the same
// position `resolveModel` clamps them (clampModelToEnvelope on the
// lightweight/auto/default branches). Step 1 is NOT clamped, also mirroring
// resolveModel: an explicit per-stage model is the operator overriding the
// mode for that stage, not the pipeline choosing within it. `explicit` reports
// that, so resolveDispatchModel knows which value the mode ceiling is allowed
// to leave alone — and, once a raising mechanism has fired, which value it may
// not lower below.
//
// The performance mode and the config are read fresh per stage, like the
// router's own mode lookup: an operator who switches to Maximum mid-run gets it
// on the next stage rather than at the next pickup.
func stageBaseModel(
	workspaceRoot string,
	mode routing.PerformanceMode,
	stage state.PipelineStage,
	predictedModel string,
) (model string, explicit bool) {
	if env := stageEnvModel(stage); env != "" {
		return env, true
	}
	if pin := routing.ModeStagePin(mode, string(stage)); pin != "" {
		return pin, false
	}
	if configured := stageConfiguredModel(workspaceRoot, stage); configured != "" {
		return configured, true
	}
	envelope := routing.RoutedTierEnvelope(mode, string(stage))
	if lightweight, ok := lightweightStageDefaults[stage]; ok {
		return routing.ClampToEnvelope(lightweight, envelope), false
	}
	if predictedModel != "" {
		return routing.ClampToEnvelope(predictedModel, envelope), false
	}
	if def := workspaceDefaultModel(workspaceRoot); def != "" {
		return routing.ClampToEnvelope(def, envelope), false
	}
	return routing.ClampToEnvelope(defaultDispatchModel, envelope), false
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
// as one — and like every other floor it lands inside the stage's routed-tier
// envelope (resolveDispatchModel), so forcing Opus under Efficiency runs sonnet
// until the operator changes the mode too, and forcing Fable under Frontier
// reaches Fable only on feature-planning/feature-dev. The one thing the ceiling
// may not do is drop the stage BELOW an explicit `pipeline.stage_models` entry:
// a forced tier is a raise, so a no-op raise leaves the operator's own model
// standing.
func raiseStageFloors(floors map[string]string, tier string) map[string]string {
	if strings.TrimSpace(tier) == "" {
		return floors
	}
	raised := make(map[string]string, len(dispatchStages))
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
