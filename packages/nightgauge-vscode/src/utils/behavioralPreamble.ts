/**
 * Behavioral preamble for the Haiku tier — measured in spike #77:
 * +7.9 composite / +11.1pp pass rate on Haiku 4.5, ≈0 on Sonnet/Opus
 * (measured skip — do NOT extend the injection to other tiers).
 *
 * Prepended prompt-proximally to the stage prompt at dispatch, never
 * delivered as a system-prompt preset: the measurement showed Haiku does
 * not act on this guidance from the harness system prompt alone.
 *
 * Source of truth: evals/variants/behavioral-preamble.json (.prepend),
 * pinned by tests/utils/behavioral-preamble-parity.test.ts. This file
 * mirrors packages/nightgauge-sdk/src/orchestrator/behavioralPreamble.ts
 * (the extension does not import the SDK package) and
 * internal/execution/preamble.go, each with its own sync test.
 */
export const BEHAVIORAL_PREAMBLE =
  "When you have enough information to act, act — do not re-derive established facts or survey options you will not pursue. If you are weighing a choice, give a recommendation, not an exhaustive survey. Lead with the outcome: state what you did or found first, with supporting detail after. Before finishing, verify your work against the stated requirements, and report outcomes faithfully — if a build or test fails, say so with the output rather than smoothing it over; when something is done and verified, state it plainly without hedging.";

/** Whether a resolved model id (short or dated) refers to the Haiku tier. */
export function isHaikuModelId(model: string | undefined): boolean {
  return !!model && model.includes("haiku");
}

/**
 * Prepend the measured behavioral preamble when the resolved model is
 * Haiku-tier; every other tier passes through unchanged. The join mirrors
 * the eval treatment (applyPromptVariant: prepend + "\n\n" + text) so
 * production matches the measured shape. Call only after the model
 * resolution is final.
 */
export function withBehavioralPreamble(prompt: string, model: string | undefined): string {
  return isHaikuModelId(model) ? `${BEHAVIORAL_PREAMBLE}\n\n${prompt}` : prompt;
}
