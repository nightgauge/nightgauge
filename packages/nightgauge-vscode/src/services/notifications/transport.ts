/**
 * Provider-agnostic primitives shared by chat-notifier services
 * (`DiscordService`, `MattermostService`, future Slack/Teams).
 *
 * Extracted in #3373 so each notifier consumes — rather than duplicates —
 * retry/backoff, debounce-timer management, secret redaction, and small
 * formatting helpers. Each notifier still owns its own wire format and
 * lifecycle subscriptions.
 *
 * @see Issue #3373 (ADR-001)
 */

import type { Logger } from "../../utils/logger";

// ─── Shared retry & debounce constants ──────────────────────────────────────

/** Backoff delays for transient fetch failures [200ms, 800ms] — 3 total attempts. */
export const FETCH_RETRY_DELAYS: readonly number[] = [200, 800];

/** Maximum retries for the final PATCH (3 total attempts: initial + 2 retries). */
export const FINAL_PATCH_MAX_RETRIES = 2;

/** Backoff delays for final-PATCH retries [3 s, 6 s]. */
export const FINAL_PATCH_RETRY_DELAYS: readonly number[] = [3000, 6000];

/** Debounce window for non-final updates (keeps within provider rate limits). */
export const DEBOUNCE_MS = 1500;

// ─── Formatting helpers ─────────────────────────────────────────────────────

export function formatDuration(ms: number): string {
  if (ms < 60_000) return `${Math.round(ms / 1000)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.round((ms % 60_000) / 1000);
  return `${m}m ${s}s`;
}

export function formatCost(usd: number): string {
  return `$${usd.toFixed(3)}`;
}

/**
 * Render the completion-embed "Budget" field: actual spend vs the ceiling,
 * plus — when a pre-flight estimate exists — that estimate labeled
 * unambiguously as a *pre-run* prediction, together with how far off it
 * landed vs the real cost.
 *
 * The pre-flight estimate (`HeadlessOrchestrator` `preFlightResult.estimatedCost`)
 * is computed once before a single token is spent and never updated again.
 * Rendered as a bare "Est: $2.703" on a completion embed sitting right next
 * to the real cost ("$28.259"), it read as a second "actual" figure to
 * operators — when it can be an order of magnitude off. Labeling it
 * "Pre-run est." and appending the actual/estimate ratio makes clear it is a
 * before-the-run prediction and surfaces how (in)accurate it was (#267).
 *
 * @param costUsd - Actual cost incurred so far (0 for a not-yet-terminal run).
 * @param ceilingUsd - Budget ceiling in USD. Must be > 0 (callers gate on this
 *   before invoking).
 * @param estimateUsd - Pre-flight estimated cost in USD, if one was recorded.
 */
export function formatBudgetFieldValue(
  costUsd: number,
  ceilingUsd: number,
  estimateUsd?: number
): string {
  const pct = costUsd > 0 ? ((costUsd / ceilingUsd) * 100).toFixed(0) : "0";
  let estimateNote = "";
  if (estimateUsd != null && estimateUsd > 0) {
    const accuracy = costUsd > 0 ? ` (actual: ${(costUsd / estimateUsd).toFixed(1)}x)` : "";
    estimateNote = `  ·  Pre-run est. ${formatCost(estimateUsd)}${accuracy}`;
  }
  return `${formatCost(costUsd)} / ${formatCost(ceilingUsd)} (${pct}%)${estimateNote}`;
}

/** Truncate a string to maxLen, appending "…" if truncated. */
export function truncate(str: string, maxLen: number): string {
  if (str.length <= maxLen) return str;
  return str.slice(0, maxLen - 1) + "…";
}

/** Shorten model name for display: "claude-sonnet-4-6" → "sonnet-4-6". */
export function shortModel(model: string): string {
  return model.replace(/^claude-/, "");
}

/**
 * Convert a 24-bit RGB integer (Discord embed-style color) to a CSS hex
 * string ("#rrggbb"). Mattermost / Slack attachments use the string form.
 */
export function hexColor(rgb: number): string {
  const clamped = Math.max(0, Math.min(0xffffff, Math.floor(rgb)));
  return `#${clamped.toString(16).padStart(6, "0")}`;
}

// ─── Secret redaction ───────────────────────────────────────────────────────

// `redactSecrets` now lives in utils/redaction.ts so the session-log writer can
// share the same value-based redactor without a util→service dependency (#170).
// Re-exported here so existing notifier importers keep working unchanged.
export { redactSecrets } from "../../utils/redaction";

// ─── retryWithBackoff ───────────────────────────────────────────────────────

export interface RetryWithBackoffOpts {
  /** Backoff delays in ms; total attempts = delays.length + 1. */
  delays: readonly number[];
  logger: Logger;
  /** Service name for log lines (e.g. "DiscordService"). */
  label: string;
  /** Sanitized URL (no token) for log lines. */
  sanitizedUrl?: string;
}

/**
 * Wrap a fetch call with bounded backoff retries.
 *
 * Retries on non-ok responses or thrown errors, sleeping `delays[i]` ms
 * between attempts. Throws the last error after the final attempt fails.
 */
export async function retryWithBackoff(
  fetchFn: () => Promise<Response>,
  opts: RetryWithBackoffOpts
): Promise<Response> {
  const { delays, logger, label, sanitizedUrl } = opts;
  let lastError: unknown;
  for (let attempt = 0; attempt <= delays.length; attempt++) {
    try {
      const res = await fetchFn();
      if (res.ok) return res;
      lastError = new Error(`HTTP ${res.status}`);
      if (attempt < delays.length) {
        logger.info(`${label}: fetch failed, retrying`, {
          attempt: attempt + 1,
          status: (lastError as Error).message,
          sanitizedUrl,
          delayMs: delays[attempt],
        });
        await new Promise<void>((resolve) => setTimeout(resolve, delays[attempt]));
      }
    } catch (err) {
      lastError = err;
      if (attempt < delays.length) {
        logger.info(`${label}: fetch error, retrying`, {
          attempt: attempt + 1,
          sanitizedUrl,
          delayMs: delays[attempt],
        });
        await new Promise<void>((resolve) => setTimeout(resolve, delays[attempt]));
      }
    }
  }
  throw lastError;
}

// ─── DebouncedPatcher ───────────────────────────────────────────────────────

/**
 * Per-key debounce / retry timer manager.
 *
 * One timer per key. Scheduling overwrites any existing timer for that key
 * — used for both debounced updates and final-PATCH retries (a debounce
 * scheduled while a retry is pending replaces the retry, and vice versa,
 * matching the original `DiscordService.updateTimers` semantics).
 */
export class DebouncedPatcher {
  private readonly timers = new Map<number, ReturnType<typeof setTimeout>>();

  /**
   * Schedule `fn` to run after `delayMs`. Cancels any existing timer for
   * `key` first.
   */
  schedule(key: number, fn: () => void | Promise<void>, delayMs: number): void {
    this.cancel(key);
    const timer = setTimeout(() => {
      this.timers.delete(key);
      void fn();
    }, delayMs);
    this.timers.set(key, timer);
  }

  /** Cancel any pending timer for `key`. No-op if none scheduled. */
  cancel(key: number): void {
    const existing = this.timers.get(key);
    if (existing) {
      clearTimeout(existing);
      this.timers.delete(key);
    }
  }

  /** True if a timer is currently scheduled for `key`. */
  has(key: number): boolean {
    return this.timers.has(key);
  }

  /** Clear every pending timer. Idempotent — safe to call multiple times. */
  dispose(): void {
    for (const timer of this.timers.values()) clearTimeout(timer);
    this.timers.clear();
  }
}
