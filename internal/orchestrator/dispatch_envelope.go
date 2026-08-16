package orchestrator

import (
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/models"
	"github.com/nightgauge/nightgauge/internal/preflight"
	"github.com/nightgauge/nightgauge/internal/state"
)

// dispatch_envelope.go resolves the effort and thinking axes of a stage
// dispatch (Issue #580 origins, grown to the wire in #581 — the spike #568
// selection-query-cutover phase this file's earlier header deferred to).
//
// Two kinds of function live here, and the distinction is the honesty rule:
//
//   - WIRE RESOLUTION (resolveWireEffort / resolveWireThinking, #581): on the
//     IPC dispatch path the scheduler is the ONLY resolver (#340), so these
//     produce the effort/thinking halves of the dispatch envelope that ride
//     `RunStageParams` next to Model. The extension executes the wire effort
//     verbatim (utils/skillRunner.ts consumes it in the modelOverride branch,
//     exactly where it consumed its own resolveStageEffort before); the wire
//     thinking is the selection query's declared answer for the dispatched
//     band under the anthropic band contract the Model field already speaks.
//     These are resolutions, not observations — the same epistemic status as
//     the Model field itself.
//   - OBSERVATION (resolveDispatchEffort / resolveDispatchThinking, #580): on
//     the Go-direct adapter path the axes stay env/adapter-owned, and these
//     return "" — absent, the #299/#397 empty-means-undetermined convention —
//     the instant Go's own evidence runs out, rather than filling the gap
//     with a plausible-looking default.

// resolveDispatchEffort returns the EFFORT_LEVELS rung the OPERATOR pinned
// for a grok-family dispatch via NIGHTGAUGE_GROK_EFFORT, or "" when the env
// var is unset/off-ladder.
//
// Since #606 the env var is an operator OVERRIDE, not the sole authority:
// internal/execution/adapters/grok.go dispatches the env value when set and
// the RunOptions-threaded envelope effort otherwise (dispatchGrokEffort).
// This observation therefore OUTRANKS the wire effort in attribution —
// mirroring the dispatch precedence — and the wire value answers when it is
// absent. Every other Go-direct adapter's effort is env/TS-owned with no
// Go-visible signal — recording a value there would be a guess, not a fact,
// so the field stays absent rather than falling back to the model's registry
// effort_default, which is only a fact about the UNOVERRIDDEN case and Go
// cannot tell whether an override happened. (On the IPC path this
// observation is superseded by the WIRE RESOLUTION above: there Go OWNS the
// effort chain, so the wire value is the fact — see the header.)
//
// The raw env value is validated against models.EffortOrder (the ladder
// pinned to the SDK's EFFORT_LEVELS authority, #394/#578): the Grok CLI
// accepts extra native rungs ("none", "minimal") that are not Nightgauge's
// vocabulary (grok_effort.go), and per the spike's vocabulary-unification
// rule the adapter-boundary translation is a one-way filter INTO
// EFFORT_LEVELS — it must never be re-widened on the way back into the run
// record.
func resolveDispatchEffort(adapterName string) string {
	if models.ProviderForAdapter(adapterName) != "xai" {
		return ""
	}
	effort := strings.TrimSpace(os.Getenv("NIGHTGAUGE_GROK_EFFORT"))
	for _, lvl := range models.EffortOrder {
		if effort == lvl {
			return effort
		}
	}
	return ""
}

// resolveDispatchThinking returns the "on"/"off" thinking state actually in
// force for a stage's dispatch, or "" when Go does not know.
//
// The base fact is the resolved model's registry data
// (behavior.thinking_default) — declared for both the anthropic and xai
// model families today, undeclared (and so absent here) for openai, google
// and copilot. The one override Go's own process can observe is the Claude
// Code disable escape hatch (CLAUDE_CODE_DISABLE_THINKING, #76's interlock,
// preflight.ThinkingDisabledInEnv): when it is set for an anthropic-provider
// stage, thinking is genuinely off regardless of the registry default. It is
// gated on the anthropic provider because the env var only affects the
// claude CLI — applying it universally would misattribute "off" to an
// adapter the flag never touched.
func resolveDispatchThinking(adapterName, model string) string {
	provider := models.ProviderForAdapter(adapterName)
	desc, ok := models.Resolve(provider, model)
	if !ok || desc.Behavior == nil || !state.IsThinkingState(desc.Behavior.ThinkingDefault) {
		return ""
	}
	if provider == "anthropic" && preflight.ThinkingDisabledInEnv() {
		return "off"
	}
	return desc.Behavior.ThinkingDefault
}

// defaultStageEfforts is the manual-mode effort fallback table. Mirrors
// DEFAULT_STAGE_EFFORTS (stageResolver.ts) — consulted only when
// model_routing.mode is "manual" and nothing more explicit named an effort,
// exactly where getStageEffort consults its copy.
var defaultStageEfforts = map[state.PipelineStage]string{
	state.StageFeaturePlanning: "medium",
	state.StageFeatureDev:      "medium",
	state.StageFeatureValidate: "low",
}

// validEffortLevel keeps operator effort input to the EFFORT_LEVELS
// vocabulary, mirroring the VALID_CLAUDE_EFFORTS guards in
// getExplicitStageEffort / getModelDefaultEffort: anything else is ignored
// rather than dispatched.
func validEffortLevel(effort string) string {
	if routing.IsEffortLevel(effort) {
		return effort
	}
	return ""
}

// stageEffortConfig resolves the explicit effort chain below the mode pin,
// mirroring getStageEffort (stageResolver.ts) for the dispatch path — where
// no issue metadata exists (services/SkillRunner passes none on the IPC path,
// so the effort_auto derivation structurally never fires there and is NOT
// mirrored):
//
//  1. NIGHTGAUGE_PIPELINE_STAGE_EFFORT_{STAGE}   env override
//  2. model_routing.stage_efforts.{stage}         config
//  3. NIGHTGAUGE_MODEL_ROUTING_DEFAULT_EFFORT     env override
//  4. model_routing.default_effort                config
//  5. manual mode only: defaultStageEfforts       built-in table
//  6. "" (no explicit effort — the model's declared default applies)
func stageEffortConfig(workspaceRoot string, stage state.PipelineStage) string {
	envKey := "NIGHTGAUGE_PIPELINE_STAGE_EFFORT_" +
		strings.ToUpper(strings.ReplaceAll(string(stage), "-", "_"))
	if v := validEffortLevel(strings.TrimSpace(os.Getenv(envKey))); v != "" {
		return v
	}
	cfg, err := config.Load(workspaceRoot)
	if err != nil {
		cfg = nil
	}
	if cfg != nil && cfg.ModelRouting != nil {
		if v := validEffortLevel(strings.TrimSpace(cfg.ModelRouting.StageEfforts[string(stage)])); v != "" {
			return v
		}
	}
	if v := validEffortLevel(strings.TrimSpace(os.Getenv("NIGHTGAUGE_MODEL_ROUTING_DEFAULT_EFFORT"))); v != "" {
		return v
	}
	if cfg != nil && cfg.ModelRouting != nil {
		if v := validEffortLevel(strings.TrimSpace(cfg.ModelRouting.DefaultEffort)); v != "" {
			return v
		}
	}
	if modelRoutingMode(cfg) == "manual" {
		return defaultStageEfforts[stage]
	}
	return ""
}

// resolveWireEffort resolves the effort half of the wire envelope (#581 —
// "the wire grows effort and thinking alongside model", spike #568 §4.1).
// Mirrors resolveStageEffort (utils/skillRunner.ts) step for step, which is
// what the extension ran for itself on the IPC path before the wire carried
// the value — so the cutover is outcome-identical by construction:
//
//	Step 0  performance-mode effort pin — only `maximum` pins ({opus, high}
//	        per stage); suppressed by a NIGHTGAUGE_PIPELINE_STAGE_MODEL_*
//	        override exactly as the TS chain suppresses its Step 0.
//	Step 1  the explicit chain (stageEffortConfig), clamped into the mode's
//	        [EffortFloor, EffortCeiling] — Efficiency caps at medium, Maximum
//	        floors at high even when its model pin was overridden away.
//
// "" means no explicit effort resolved anywhere: the extension omits
// `--effort` and the model's declared default rules — the selection query's
// rung effort, implied rather than spelled, which keeps the spawned argv
// byte-identical to the pre-wire behavior.
func resolveWireEffort(workspaceRoot string, stage state.PipelineStage) string {
	mode := routing.ResolvePerformanceMode(workspaceRoot)
	if mode != routing.ModeElevated && stageEnvModel(stage) == "" {
		if pin := routing.ModeStageEffort(mode, string(stage)); pin != "" {
			return pin
		}
	}
	return routing.ClampEffortToEnvelope(stageEffortConfig(workspaceRoot, stage), routing.Envelope(mode))
}

// resolveWireThinking resolves the thinking half of the wire envelope (#581)
// through the selection query: the dispatched band's rung under the anthropic
// provider — the band contract RunStageParams.Model already speaks (the
// extension translates the band at the last mile for codex/gemini/copilot,
// and the envelope translates or drops with it) — overridden by the mode
// envelope's thinking policy when the mode declares one (#606, spike #568
// §4.1.3; no mode does today, so the policy branch is dormant by
// construction), and adjusted by the one override Go's own process observes,
// the Claude Code disable escape hatch (#76's interlock) — an operator
// escape hatch outranks both the rung's declared default and a mode table.
// "" for a model with no rung (a user-defined local model) or an undeclared
// thinking default under a policy-less mode: absent, never fabricated.
func resolveWireThinking(model string, mode routing.PerformanceMode) string {
	return wireThinkingUnderPolicy(model, routing.Envelope(mode).ThinkingPolicy)
}

// wireThinkingUnderPolicy is resolveWireThinking with the mode's thinking
// policy made explicit — the seam the policy-branch tests exercise, since no
// shipped mode table declares a policy value yet (#606).
func wireThinkingUnderPolicy(model, policy string) string {
	thinking := ""
	if rung, ok := routing.ResolveBandEnvelope("anthropic", routing.TierBand(model), ""); ok && state.IsThinkingState(rung.Thinking) {
		thinking = rung.Thinking
	}
	if state.IsThinkingState(policy) {
		thinking = policy
	}
	if thinking == "" {
		return ""
	}
	if preflight.ThinkingDisabledInEnv() {
		return "off"
	}
	return thinking
}

// resolveDispatchSelectionMode returns model_routing.mode (manual | automatic
// | hybrid) active for this dispatch (Issue #580, resolves #462).
// modelRoutingMode is total — it never errors and defaults to "automatic" —
// so unlike its siblings above this is a genuine attribution Go always has,
// not a best-effort one; a config.Load failure here degrades to the same
// "automatic" default modelRoutingMode already returns for a nil config.
func resolveDispatchSelectionMode(workspaceRoot string) string {
	cfg, _ := config.Load(workspaceRoot)
	return modelRoutingMode(cfg)
}
