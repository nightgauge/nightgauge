package state

// model_selection_envelope.go declares the Go-side vocabulary for the
// dispatch-envelope fields added to model_selection by Issue #580
// (materialized from spike #568 §3, "vocabulary unification").
//
// Effort has an established cross-language authority already: EFFORT_LEVELS
// in packages/nightgauge-sdk/src/eval/modelEvalSchemas.ts, mirrored by
// internal/models.EffortOrder and pinned by
// internal/models/registry_test.go's TestEffortOrderPinnedToSDKEffortLevels
// (#394/#578). model_selection.effort reuses that same authority — see
// resolveDispatchEffort in internal/orchestrator/dispatch_envelope.go and the
// TS-derivation pin in execution_history_envelope_parity_test.go — so no new
// effort vocabulary is declared here.
//
// Thinking has no existing cross-language authority to derive from: it is a
// new axis this issue introduces (spike #568 §3, "the canonical thinking
// axis is on|off"). ThinkingStates is therefore Go's declaration of it,
// pinned against the TypeScript literal in
// packages/nightgauge-vscode/src/schemas/executionHistory.ts
// (model_selection.thinking) by
// TestModelSelectionThinkingPinnedToExecutionHistorySchema.

// ThinkingStates is the canonical binary thinking axis: "on" or "off",
// whether the model reasoned with no thinking parameter set, or the runtime
// explicitly turned it off (the Claude Code CLAUDE_CODE_DISABLE_THINKING
// escape hatch, #76's interlock). Order is part of the pin.
var ThinkingStates = []string{"on", "off"}

// IsThinkingState reports whether v is a member of ThinkingStates. Used by
// resolveDispatchThinking (internal/orchestrator/dispatch_envelope.go) to
// keep the registry's behavior.thinking_default from writing an unlisted
// value onto the record if the axis is ever widened without updating this
// declaration.
func IsThinkingState(v string) bool {
	for _, s := range ThinkingStates {
		if s == v {
			return true
		}
	}
	return false
}
