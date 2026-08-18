/**
 * ClaudeRateLimitUsageProvider — subscription-window usage for the `claude`
 * adapter, built from the `rate_limit_event` envelope the CLI already emits
 * (Issue #709).
 *
 * The first producer of `plan.kind: "subscription-window"`. On a Claude Max
 * plan there is no dollar budget to report — there are refilling session and
 * weekly allowances — so the operator question is "how much of my window is
 * left, and when does it reset", and this provider answers it with the
 * vendor's own figure rather than a locally-derived spend estimate.
 *
 * ## Where the number comes from
 *
 * `claude -p --output-format stream-json` emits `rate_limit_event` mid-stream.
 * `utils/tokenParser.ts` has parsed it into `RateLimitEventData` for a long
 * time, and `utils/skillRunner.ts` consumes it for exactly one purpose: to
 * pause or fast-fail a stage on quota exhaustion. This provider adds a
 * **second reader** of the same parse and changes nothing about the first —
 * no new CLI invocation, no new flag, no second data path.
 *
 * ## Risk: this is an unofficial, reverse-engineered wire format
 *
 * `rate_limit_event` is not a documented API surface. It is a detail of the
 * CLI's `stream-json` output, discovered by observation, and it has already
 * changed shape once inside this repository's own history (the fields moved
 * from the envelope's top level into a nested `rate_limit_info` object, which
 * is why `tokenParser` still keeps a flat-fields fallback). The spike that
 * recommended this provider rated its confidence
 * "reverse-engineered, unofficial; measured only mid-run"
 * (docs/spikes/662-adapter-usage-quota-signals.md §2.2).
 *
 * Everything here is therefore written to degrade rather than guess:
 *
 * - No observed event → `getSnapshot` returns `null`, and
 *   `AdapterUsageService` falls through to `LocalTelemetryUsageProvider`'s
 *   dollar windows. An API-key (pay-per-token) user never emits this envelope
 *   and so keeps the dollar rendering — the plan kind follows the **observed
 *   signal**, never the adapter name.
 * - An unrecognised `rateLimitType` produces no window rather than a guessed
 *   scope.
 * - A cached reading whose own `resetsAt` has passed is dropped by
 *   `ClaudeRateLimitStore`, because the window it describes has refilled and
 *   its percentage is known-wrong, not merely stale.
 *
 * ## What this signal does not carry
 *
 * No model family. `rateLimitType` names a *window* (five-hour, daily,
 * seven-day), not a model, so `modelFamily` is deliberately never populated
 * here — a per-family breakdown would be invented.
 *
 * @see Issue #709 - Claude usage provider
 * @see docs/spikes/662-adapter-usage-quota-signals.md
 * @see docs/decisions/018-adapter-usage-quota-model.md
 */

import type { ExecutionAdapter } from "../../config/schema";
import type { ClaudeRateLimitStore, RateLimitReading } from "./ClaudeRateLimitStore";
import type { UsageProvider, UsageSnapshot, UsageWindow, UsageWindowScope } from "./types";

/**
 * The vendor's bucket names, mapped onto `UsageWindowScope`.
 *
 * `five_hour` is the rolling session allowance, `seven_day` the weekly one,
 * and `daily` a calendar day. These three are the values observed in this
 * codebase; any other bucket name maps to nothing, because guessing a scope
 * would mislabel the period the number describes.
 */
const SCOPE_BY_BUCKET: Readonly<Record<string, UsageWindowScope>> = {
  five_hour: "rolling",
  seven_day: "weekly",
  daily: "daily",
};

/** Human-facing period names, one per mapped scope. */
const LABEL_BY_SCOPE: Readonly<Record<string, string>> = {
  rolling: "Session (5h)",
  daily: "Today",
  weekly: "This week",
};

/**
 * Display order: shortest window first.
 *
 * The status-bar meter renders `windows[0]` until the operator cycles, and on
 * a Max plan the five-hour allowance is the one that actually stops work.
 */
const SCOPE_ORDER: readonly UsageWindowScope[] = ["rolling", "daily", "weekly"];

/**
 * Map one stored reading onto a `UsageWindow`, or `null` when its bucket name
 * is not one this provider can honestly name.
 *
 * `used: utilization, limit: 100, unit: "percent"` is the mapping ADR 018
 * reserved: the channel reports a percentage and no denominator, so 100 is the
 * denominator the percentage is already against, not an invented ceiling.
 */
export function readingToWindow(providerId: string, reading: RateLimitReading): UsageWindow | null {
  const scope = SCOPE_BY_BUCKET[reading.rateLimitType];
  if (scope === undefined) {
    return null;
  }
  return {
    id: `${providerId}:${scope}`,
    label: LABEL_BY_SCOPE[scope],
    scope,
    used: reading.utilization,
    limit: 100,
    unit: "percent",
    resetsAt: reading.resetsAt > 0 ? new Date(reading.resetsAt * 1000) : null,
    // `measured` is reserved for a figure the vendor stated during the run
    // that is still streaming. The moment that run ends the same number
    // becomes a cached one, and a cached percentage is an estimate of the
    // present no matter how confidently the vendor stated it in the past.
    confidence: reading.live ? "measured" : "estimated",
    observedAt: reading.observedAt,
  };
}

export class ClaudeRateLimitUsageProvider implements UsageProvider {
  readonly id = "claude-rate-limit";

  constructor(private readonly store: ClaudeRateLimitStore) {}

  /**
   * Claims `claude` only.
   *
   * Claiming the adapter is not the same as describing it: `getSnapshot`
   * returns `null` until an event has actually been observed, which is what
   * lets the pay-per-token path keep answering for API-key users on the same
   * adapter id.
   */
  supports(adapter: ExecutionAdapter): boolean {
    return adapter === "claude";
  }

  async getSnapshot(adapter: ExecutionAdapter): Promise<UsageSnapshot | null> {
    if (!this.supports(adapter)) {
      return null;
    }
    await this.store.load();

    const now = new Date();
    const windows = this.store
      .readings(now)
      .map((reading) => readingToWindow(this.id, reading))
      .filter((window): window is UsageWindow => window !== null)
      .sort((a, b) => SCOPE_ORDER.indexOf(a.scope) - SCOPE_ORDER.indexOf(b.scope));

    // No usable reading — every bucket has refilled, or none was ever
    // observed. Returning null hands the adapter to the next provider rather
    // than asserting a subscription plan we have no evidence for.
    if (windows.length === 0) {
      return null;
    }

    return {
      adapter,
      plan: { kind: "subscription-window" },
      capturedAt: now,
      windows,
    };
  }
}
