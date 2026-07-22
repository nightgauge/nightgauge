package execution

import "strings"

// BehavioralPreamble is the behavioral preamble measured in spike #77:
// +7.9 composite / +11.1pp pass rate on the Haiku tier, ≈0 on Sonnet/Opus
// (measured skip — do NOT extend the injection to other tiers). Prepended
// prompt-proximally to the stage prompt at dispatch, never delivered as a
// system-prompt preset: the measurement showed Haiku does not act on this
// guidance from the harness system prompt alone.
//
// Source of truth: evals/variants/behavioral-preamble.json (.prepend),
// pinned by preamble_test.go. The TS mirrors carry the same text
// (packages/nightgauge-sdk/src/orchestrator/behavioralPreamble.ts,
// packages/nightgauge-vscode/src/utils/behavioralPreamble.ts), each with
// its own sync test.
const BehavioralPreamble = "When you have enough information to act, act — do not re-derive established facts or survey options you will not pursue. If you are weighing a choice, give a recommendation, not an exhaustive survey. Lead with the outcome: state what you did or found first, with supporting detail after. Before finishing, verify your work against the stated requirements, and report outcomes faithfully — if a build or test fails, say so with the output rather than smoothing it over; when something is done and verified, state it plainly without hedging."

// WithBehavioralPreamble prepends BehavioralPreamble when the resolved model
// is Haiku-tier; every other tier passes through unchanged. The join mirrors
// the eval treatment (applyPromptVariant: prepend + "\n\n" + text) so
// production matches the measured shape. Call only after ALL model
// escalations are final — a stage that escalated off Haiku must get the
// unmodified prompt.
func WithBehavioralPreamble(prompt, model string) string {
	if strings.Contains(model, "haiku") {
		return BehavioralPreamble + "\n\n" + prompt
	}
	return prompt
}
