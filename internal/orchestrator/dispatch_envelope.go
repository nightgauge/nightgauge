package orchestrator

import (
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/models"
	"github.com/nightgauge/nightgauge/internal/preflight"
	"github.com/nightgauge/nightgauge/internal/state"
)

// dispatch_envelope.go resolves the effort and thinking rungs Go can
// honestly attribute to a stage dispatch (Issue #580, materialized from
// spike #568 §3). It is deliberately narrow: unlike model and adapter, effort
// and thinking are resolved almost entirely on the TypeScript side today
// (stageResolver.ts / skillRunner.ts), with no value threaded onto the IPC
// wire (that lands later, in the spike's selection-query-cutover phase). Both
// functions here return "" — absent, the #299/#397 empty-means-undetermined
// convention — the instant Go's own evidence runs out, rather than filling
// the gap with a plausible-looking default.

// resolveDispatchEffort returns the EFFORT_LEVELS rung actually in force for
// a grok-family dispatch, or "" when Go has no direct evidence of it.
//
// Grok is the only adapter whose invocation Go's own process can observe
// end-to-end: internal/execution/adapters/grok.go builds `--effort <val>`
// straight from the ambient NIGHTGAUGE_GROK_EFFORT env var it inherits via
// os.Environ(). This reads that SAME env var, at the SAME trust level,
// without editing that package (it is a parallel agent's this wave). Every
// other adapter's effort is resolved entirely in TypeScript with no
// Go-visible signal — recording a value there would be a guess, not a fact,
// so the field stays absent rather than falling back to the model's registry
// effort_default, which is only a fact about the UNOVERRIDDEN case and Go has
// no way to tell whether an override happened on the TS side.
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
