/**
 * terminalKindSignal.ts — the extension's terminal-kind SIGNAL (#306).
 *
 * WHY THIS SIDE MATTERS DISPROPORTIONATELY: the kind returned here is sent to
 * Go with `autonomousComplete`, and Go's NotifyComplete only re-classifies when
 * it received an EMPTY kind. A non-empty answer is therefore used VERBATIM — so
 * a wrong answer here silently overrides the authoritative classifier for the
 * fleet's reaction while the run RECORD is still written from Go's own
 * classification of the same failure. That split is the defect #306 exists to
 * make impossible.
 *
 * ON EVERY RULE THE ANSWER IS BOUNDED, and by construction rather than by
 * testing. This is no longer a ladder: `signalTerminalKind` runs the SAME
 * ordered rule table Go embeds (internal/terminalkind/table.json, generated into
 * the SDK) and returns the winning rule's kind only when that rule is declared
 * `signal: true`. So for rules the answer is either nothing or exactly what Go
 * will record — bounded above (it can never name a different kind) and below
 * (when the winning rule is in the declared subset it must answer, which the
 * corpus suite asserts per row).
 *
 * Note the shape of the bound: the full ladder runs first and only the WINNER
 * is projected. Skipping non-signal rules would reintroduce disagreement — a
 * lower-precedence signal rule could claim text that a higher-precedence
 * non-signal rule owns.
 *
 * ONE DECLARED EXCEPTION, and it is data. When the ladder projects no SIGNAL,
 * the table's `signal_extensions` are consulted; today there is one, for an
 * Anthropic/Codex session-or-usage-limit line (#3792). Say its bound exactly:
 * an extension can never overrule a kind projected by a `signal: true` RULE,
 * which is narrower than "a kind the record names" — a kind the record names
 * through a NON-signal rule is not protected, and this extension deliberately
 * diverges from precisely that case. A quota line that names a model records
 * `model_unavailable` (a plan restriction) and makes the fleet react
 * `rate_limit_quota_exhausted` (an environmental window); a bare one records
 * nothing at all and reacts the same way. Both are pinned by corpus rows whose
 * `expected_signal` differs from `expected` — which the corpus well-formedness
 * test permits for declared extensions and for nothing else. See
 * docs/FAILURE_TAXONOMY.md.
 *
 * THE VALUE THIS RETURNS IS NOT THE ONLY THING THAT MATTERS. It is assembled
 * into the IPC argument in bootstrap/services.ts, and that call site is asserted
 * to be a bare single-assignment delegation by terminalKindSignal.corpusParity.
 * test.ts — guarding this file alone left the actual producer unguarded.
 *
 * Returning `undefined` is safe by design: the caller forwards the raw failure
 * text as `failureDetail` unconditionally (#3442) and Go classifies from it. A
 * MISS costs nothing; a WRONG HIT costs a wrong recovery.
 *
 * The subset covers the environmental failures the TS layer sees first — it is
 * not a second taxonomy. Which rules are in it is declared in table.json and
 * pinned per row by `expected_signal` in the shared corpus, so a rule joining or
 * leaving the subset turns every row it wins red in all three suites.
 */

import { signalTerminalKind } from "@nightgauge/sdk";

/**
 * Classify a failed run's error text into the terminal kind the extension
 * signals to the Go scheduler, or `undefined` to let Go classify from the
 * forwarded failure text.
 *
 * @param errorText - `error.message` from the failed PipelineRunResult
 */
export function classifyTerminalKindForSignal(errorText: string): string | undefined {
  return signalTerminalKind(errorText);
}
