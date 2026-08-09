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
// The single USD formatter (#333 decision E). It lives in utils/ so the token
// parser can share it without a utils → services import.
import { formatCost } from "../../utils/formatCost";

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

/**
 * Render the completion-embed "Budget" field: actual spend vs the ceiling.
 *
 * The pre-flight estimate no longer lives here — it is its own "Cost Accuracy"
 * field (`formatCostAccuracyValue`), because estimate-vs-actual is the single
 * strongest pipeline-health signal in the message and was buried as a suffix
 * inside the noisiest field (#333 decision G, extending #267).
 *
 * @param costUsd - Actual cost incurred so far (0 for a not-yet-terminal run).
 * @param ceilingUsd - Budget ceiling in USD. Must be > 0 (callers gate on this
 *   via `shouldRenderBudgetField`).
 */
export function formatBudgetFieldValue(costUsd: number, ceilingUsd: number): string {
  const pct = costUsd > 0 ? ((costUsd / ceilingUsd) * 100).toFixed(0) : "0";
  return `${formatCost(costUsd)} / ${formatCost(ceilingUsd)} (${pct}%)`;
}

/** Spend/ceiling ratio at or above which the budget field carries information. */
export const BUDGET_FIELD_MIN_RATIO = 0.5;

/** Outcome types for which the budget field is always relevant. */
const BUDGET_OUTCOME_TYPES: ReadonlySet<string> = new Set([
  "budget-ceiling",
  "shipped-but-overbudget",
]);

/**
 * Should the notifier render the budget field at all? (#333 decision F)
 *
 * `$1.52 / $75.00 (2%)` occupied a top-level field on every single run to say
 * "nothing is wrong" — permanent noise that pushed the actionable fields below
 * the fold. It earns its place only when spend has reached half the ceiling,
 * or when the run's outcome is about the budget.
 */
export function shouldRenderBudgetField(
  costUsd: number,
  ceilingUsd: number,
  outcomeType: string | undefined
): boolean {
  if (!(ceilingUsd > 0)) return false;
  if (outcomeType != null && BUDGET_OUTCOME_TYPES.has(outcomeType)) return true;
  return costUsd / ceilingUsd >= BUDGET_FIELD_MIN_RATIO;
}

/** Lower bound of the "on estimate" neutral band (actual/estimate ratio). */
export const COST_ACCURACY_BAND_LOW = 0.8;
/** Upper bound of the "on estimate" neutral band (actual/estimate ratio). */
export const COST_ACCURACY_BAND_HIGH = 1.25;

/**
 * Render the standalone "Cost Accuracy" field: what the run was predicted to
 * cost vs what it actually cost (#333 decision G).
 *
 * Inside the neutral band the field reads "≈ on estimate" so it is not a
 * permanent alarm; outside it, the ratio is the loudest thing in the field
 * because a 3x miss is the signal an operator most needs from the message.
 */
export function formatCostAccuracyValue(actualUsd: number, estimateUsd: number): string {
  const head = `Est. ${formatCost(estimateUsd)} → Actual ${formatCost(actualUsd)}`;
  if (actualUsd <= 0 || estimateUsd <= 0) return head;
  const ratio = actualUsd / estimateUsd;
  const rendered = `${ratio.toFixed(1)}x`;
  if (ratio >= COST_ACCURACY_BAND_LOW && ratio <= COST_ACCURACY_BAND_HIGH) {
    return `${head}  ·  ≈ on estimate (${rendered})`;
  }
  return `${head}  ·  **${rendered} ${ratio > 1 ? "over" : "under"}**`;
}

/** Minimal logging surface the pure render helpers need. */
export interface WarnLogger {
  warn(message: string, meta?: Record<string, unknown>): void;
}

/**
 * Cross-check the reported run total against the per-stage costs before the
 * embed asserts it (#333 decision A / AC1).
 *
 * The #289 message claimed a `$1.518` run total while its own Feature Dev
 * line read `$13.319` — a total smaller than one of its components. The root
 * cause was fixed upstream (#309 books failing stages into the run total),
 * but the render layer must never restate a total its own stage list
 * contradicts: when the reported figure is below the largest single stage,
 * render the per-stage **sum** and log both numbers. Never silently assert
 * the contradiction, and never silently correct it either.
 *
 * The invariant is deliberately max-based, not sum-based: a total below its
 * largest component is impossible, whereas a total merely below the *sum* is
 * routine float and rounding noise, and a sum-strict check would fire on
 * every healthy run. A quietly-undercounted total that still clears the max
 * is therefore out of scope here (AC1) — the fix for that lives upstream in
 * whoever books the stage costs, not in the renderer.
 *
 * @returns The total to render — the reported one unless the stages disprove it.
 */
export function reconcileRunTotalUsd(
  reportedUsd: number,
  perStage: Record<string, { cost_usd?: number } | undefined> | undefined,
  logger?: WarnLogger
): number {
  if (!perStage) return reportedUsd;
  const costs = Object.values(perStage)
    .map((s) => s?.cost_usd)
    .filter((c): c is number => typeof c === "number" && c > 0);
  if (costs.length === 0) return reportedUsd;

  const maxStageCostUsd = Math.max(...costs);
  if (reportedUsd >= maxStageCostUsd) return reportedUsd;

  const perStageSumUsd = costs.reduce((sum, c) => sum + c, 0);
  logger?.warn(
    "Notifier: reported run total is below a single stage's cost — rendering the per-stage sum",
    { reportedTotalUsd: reportedUsd, maxStageCostUsd, perStageSumUsd }
  );
  return perStageSumUsd;
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
