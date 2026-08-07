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
 * IT IS NOW IMPOSSIBLE BY CONSTRUCTION, not by testing. This is no longer a
 * ladder: `signalTerminalKind` runs the SAME ordered rule table Go embeds
 * (internal/terminalkind/table.json, generated into the SDK) and returns the
 * winning rule's kind only when that rule is declared `signal: true`. So the
 * answer is either nothing or exactly what Go will record — bounded above (it
 * can never name a different kind) and below (when the winning rule is in the
 * declared subset it must answer, which the corpus suite asserts per row).
 *
 * Note the shape of the bound: the full ladder runs first and only the WINNER
 * is projected. Skipping non-signal rules would reintroduce disagreement — a
 * lower-precedence signal rule could claim text that a higher-precedence
 * non-signal rule owns, which is how the old hand-written ladder answered
 * `rate_limit_quota_exhausted` for text Go recorded as `model_unavailable`.
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
