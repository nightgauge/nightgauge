/**
 * terminalKindSignal.ts — the extension's terminal-kind SIGNAL ladder (#306).
 *
 * This is the THIRD terminal-kind classifier in the codebase, and the one with
 * the most authority per line:
 *
 *   Go   `ClassifyTerminalKind`  internal/orchestrator/failure_handler.go
 *        The authoritative classifier. Writes `terminal_kind` into the run
 *        record and drives the scheduler's recovery routing.
 *   SDK  `classifyTerminalKind`  packages/nightgauge-sdk/.../failureClassifier.ts
 *        The published mirror, pinned to Go block-for-block.
 *   HERE `classifyTerminalKindForSignal`
 *        A defense-in-depth signal for IPC-mode runs, where the TS layer
 *        observed the adapter's result envelope before Go saw anything.
 *
 * WHY THIS ONE MATTERS DISPROPORTIONATELY: the kind returned here is sent to
 * Go with `autonomousComplete`, and Go's NotifyComplete only re-classifies when
 * it received an EMPTY kind. A non-empty answer here is therefore used
 * VERBATIM — so a wrong answer silently overrides the authoritative classifier
 * for the fleet's reaction, while the run RECORD is still written from Go's own
 * classification of the same failure. That split (record says one thing, fleet
 * reaction does another) is the defect #306 exists to make visible.
 *
 * Returning `undefined` is safe by design: the caller forwards the raw failure
 * text as `failureDetail` unconditionally (#3442), and Go re-classifies from
 * it. A MISS costs nothing; a WRONG HIT costs a wrong recovery.
 *
 * The ladder was extracted from `bootstrap/services.ts` VERBATIM — same rules,
 * same order, same wording — so it could be driven by a test at all. It is
 * pinned against the shared corpus in
 * `internal/orchestrator/testdata/terminal-kind/corpus.json` by
 * `tests/services/terminalKindSignal.corpusParity.test.ts`, which asserts that
 * whenever this ladder answers, its answer matches Go's — except for the
 * disagreements the corpus records explicitly as `known_divergence.signal`.
 *
 * This ladder covers 8 of the 32 declared kinds on purpose: it exists to catch
 * the environmental failures the TS layer sees first, not to be a second
 * complete taxonomy. The eight are enumerated in the corpus suite's "covers the
 * environmental kinds it exists to catch" case, which fails if this ladder ever
 * stops producing one of them — so the count above is checked, not asserted.
 */

import { ARCHITECTURE_APPROVAL_REQUIRED_MARKER } from "../utils/failureComment";

/**
 * Classify a failed run's error text into the terminal kind the extension
 * signals to the Go scheduler, or `undefined` to let Go classify from the
 * forwarded failure text.
 *
 * @param errorText - `error.message` from the failed PipelineRunResult
 */
export function classifyTerminalKindForSignal(errorText: string): string | undefined {
  if (!errorText) return undefined;

  if (/stream idle timeout/i.test(errorText)) {
    // #3398: mid-stream cut while the model was producing. Environmental —
    // Go applies the long environmental-failure backoff and skips the
    // lifetime-failure-cap increment.
    return "stream_idle_timeout";
  }

  if (/github-quota-low/i.test(errorText)) {
    // #3896: transient GitHub-API quota dip at pipeline-start. Forward the
    // explicit kind so Go applies the GitHub-quota cooldown (issue stays
    // Ready, no lifetime-cap increment) rather than treating it as a real
    // failure. The Go fallback also matches the embedded token, but sending
    // the kind keeps IPC-mode runs unambiguous.
    return "github_quota_low";
  }

  if (
    /rate-limit-quota-exhausted/i.test(errorText) ||
    // Anthropic session/usage limit — same environmental-quota class, so Go
    // applies the cooldown-until-reset backoff and skips the
    // lifetime-failure-cap increment. #3792.
    //
    // KNOWN DIVERGENCE (#306): when the text ALSO names a model, both text
    // classifiers call it `model_unavailable` and route to #42's tier
    // downgrade, while this rule reports a quota exhaustion — and Go uses this
    // answer verbatim, so the downgrade never fires on the extension path.
    // Recorded in the corpus as `divergence-signal-usage-limit-with-model`
    // rather than silently "aligned": which taxonomy is right is a routing
    // decision (#305/#370), not a parity fix.
    /\b(?:session|usage)\s+limit\b/i.test(errorText)
  ) {
    return "rate_limit_quota_exhausted";
  }

  if (/overloaded/i.test(errorText)) {
    // Anthropic API 529 "Overloaded" — a transient capacity blip. Routes to
    // the api_overloaded recovery path: 5-minute per-issue backoff,
    // board→Ready, no lifetime-cap increment, no pause.
    //
    // KNOWN DIVERGENCE (#306): this ladder ranks the explicit quota marker
    // ABOVE the generic `overloaded` substring; Go ranks them the other way
    // round. On text carrying both, the two disagree, and the recoveries are
    // opposites (a five-minute retry versus a cooldown until the bucket
    // resets). Corpus row `divergence-signal-overloaded-with-quota-marker`.
    return "api_overloaded";
  }

  if (/github-network-outage/i.test(errorText)) {
    // #4002: api.github.com unreachable at pipeline-start. Routes to the
    // github_network_outage recovery path: short GLOBAL cooldown, board→Ready,
    // no lifetime-cap increment, no pause.
    return "github_network_outage";
  }

  if (/socket connection was closed/i.test(errorText) || /socket hang up/i.test(errorText)) {
    // #4002: Anthropic API transport drop (a local network blip killed the
    // stream mid-stage). Same recovery path as api_overloaded: 5-minute
    // per-issue backoff, board→Ready, no lifetime-cap increment, no pause.
    return "api_connection_lost";
  }

  if (/\[adapter-auth-failed\]|adapter_auth_failed/i.test(errorText)) {
    // #312: the pipeline-start adapter auth gate refused to launch — a probe
    // timed out under a concurrent dispatch burst (transient starvation; auth
    // was never broken) or the adapter CLI is logged out. Forward the explicit
    // kind so the Go scheduler routes it to the adapter_auth_failed recovery
    // path (short backoff, board→Ready, NO lifetime-cap increment, NO cascade
    // feed, NO pause) instead of the generic subagent_crash path that would
    // pause the queue and count three burst false-negatives toward the
    // cascade breaker.
    return "adapter_auth_failed";
  }

  if (errorText.includes(ARCHITECTURE_APPROVAL_REQUIRED_MARKER)) {
    // #4098/#4222: the architecture-approval gate halted the run before
    // feature-dev because a human must approve a high-impact decision. The TS
    // layer already treats this as an actionable pause (see failureComment.ts
    // and ConcurrentPipelineManager), but the Go scheduler never heard about
    // it — so it bucketed a deliberate human gate into subagent_crash, counted
    // it toward the lifetime failure cap, and reverted the issue to Ready,
    // re-dispatching it into a gate only a human can open (~$5/attempt, and
    // the second attempt trips the whole scheduler). Forward the explicit kind
    // so Go routes it to the architecture_approval_required path: board → In
    // review, NO lifetime-cap increment, NO cascade feed, NO pause, and no
    // automatic re-dispatch.
    return "architecture_approval_required";
  }

  return undefined;
}
