/**
 * Rate-limit circuit breaker — pauses Go's autonomous scheduler when GitHub
 * returns a rate-limit error.
 *
 * Background (#3020):
 *   In the original incident, autonomous mode kept dispatching pipeline runs
 *   for an hour after GitHub's REST quota was exhausted. Each run launched
 *   into a fully-blocked API and burned tokens spinning on doomed gh calls.
 *   Pre-flight headroom check (preCheckAuth) catches the *next* run, but
 *   already-running runs that hit the limit mid-stage need a backstop.
 *
 *   This helper:
 *   1. Detects "rate limit exceeded" / "429" / "secondary rate limit" patterns
 *      anywhere in an error message or stderr.
 *   2. Calls IpcClient.autonomousPause() exactly once per outage (deduped via
 *      a module-level flag so a burst of failures doesn't spam IPC).
 *   3. Surfaces a one-shot warning toast naming the reset time.
 *   4. Auto-arms again after the first successful gh call (caller invokes
 *      noteRateLimitOk() — typically the next preCheckAuth() that succeeds).
 *
 *   Best-effort: a transient IPC failure must NEVER block the original error
 *   path. The caller still needs to handle the underlying failure.
 */

import * as vscode from "vscode";
import { IpcClient } from "../services/IpcClient";
import type { Logger } from "./logger";

const RATE_LIMIT_PATTERN =
  /(rate.?limit\s+(?:exceeded|already|hit)|secondary.rate|\b429\b|too many requests)/i;

let breakerTripped = false;
let lastTripAt = 0;
// #3509 — epoch-seconds value of GitHub's X-RateLimit-Reset (from the
// githubRateLimit probe). When non-zero, the watchdog skips probes until
// Date.now() > breakerResetAt * 1000, avoiding calls while quota is exhausted.
let breakerResetAt = 0;

/** Heuristic detector — true when the error/output looks like a GitHub 429. */
export function isGithubRateLimitError(input: unknown): boolean {
  if (!input) return false;
  const text = input instanceof Error ? `${input.message}\n${input.stack ?? ""}` : String(input);
  return RATE_LIMIT_PATTERN.test(text);
}

/**
 * If the error looks like a rate-limit, pause autonomous mode and surface a
 * toast. Idempotent within a single outage. Returns true when the breaker
 * tripped (so callers can short-circuit retry loops).
 *
 * Never throws — IPC/UI failures are swallowed.
 */
export async function tripBreakerIfRateLimited(
  err: unknown,
  logger: Logger,
  context: { source: string; issueNumber?: number }
): Promise<boolean> {
  if (!isGithubRateLimitError(err)) return false;
  return tripBreaker(err, logger, context);
}

async function tripBreaker(
  err: unknown,
  logger: Logger,
  context: { source: string; issueNumber?: number }
): Promise<boolean> {
  if (breakerTripped) {
    // Already paused — short-circuit without spamming IPC.
    return true;
  }
  breakerTripped = true;
  lastTripAt = Date.now();

  const errMsg = err instanceof Error ? err.message : String(err);
  logger.error("Rate-limit circuit breaker tripped — pausing autonomous", {
    source: context.source,
    issueNumber: context.issueNumber,
    error: errMsg,
  });

  // Best-effort pause + toast — never throw.
  try {
    const ipc = IpcClient.getInstance();
    const status = await ipc.autonomousStatus().catch(() => null);
    if (status?.status === "running") {
      await ipc.autonomousPause(
        "GitHub API rate limit hit — circuit breaker opened",
        "rate-limit-circuit-breaker"
      );
    }
    let resetMsg = "";
    try {
      const info = await ipc.githubRateLimit();
      if (info.resetAt > 0) {
        // #3509 — store the resetAt so the watchdog can skip probe calls
        // until the reset window has passed (avoids calling githubRateLimit()
        // every 2 min while the quota is provably exhausted).
        breakerResetAt = info.resetAt;
        const minutes = Math.max(1, Math.ceil((info.resetAt * 1000 - Date.now()) / 60_000));
        resetMsg = ` Quota resets in ~${minutes} min.`;
      }
    } catch {
      // ignore — best-effort
    }
    void vscode.window.showWarningMessage(
      `Nightgauge: GitHub rate limit hit during ${context.source}. Autonomous paused.${resetMsg}`
    );
  } catch (pauseErr) {
    logger.warn("Rate-limit breaker failed to pause autonomous (best-effort)", {
      source: context.source,
      error: pauseErr instanceof Error ? pauseErr.message : String(pauseErr),
    });
  }

  return true;
}

/**
 * Re-arm the breaker after a successful API call. Callers should invoke this
 * from a code path that only runs when GitHub responded normally (e.g. the
 * preCheckAuth rate-limit query that returned a healthy `remaining`).
 */
export function noteRateLimitOk(): void {
  if (breakerTripped) {
    breakerTripped = false;
    breakerResetAt = 0;
  }
}

/**
 * Epoch-seconds reset time from the last githubRateLimit response captured
 * when the breaker tripped. Zero means unknown / breaker not currently open.
 * Use to avoid calling githubRateLimit() before the reset window has passed.
 */
export function getBreakerResetAt(): number {
  return breakerResetAt;
}

/** Returns true when the circuit breaker is open (rate limit previously hit). */
export function isBreakerTripped(): boolean {
  return breakerTripped;
}

/** Test-only — reset module state between tests. */
export function _resetBreakerForTests(): void {
  breakerTripped = false;
  lastTripAt = 0;
  breakerResetAt = 0;
}

/** Test-only — observe internal state. */
export function _isBreakerTrippedForTests(): boolean {
  return breakerTripped;
}

/** Last trip timestamp (epoch ms), or 0 if never. */
export function getLastTripAt(): number {
  return lastTripAt;
}
