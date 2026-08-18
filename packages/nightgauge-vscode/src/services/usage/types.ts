/**
 * Provider-neutral adapter usage model (Issue #658).
 *
 * One snapshot shape that every usage surface reads — the status-bar meter
 * (#659) and the usage webview (#661) both consume `UsageSnapshot` and nothing
 * else, so they cannot drift into two data paths.
 *
 * ## What "quota" means here
 *
 * Every `limit` produced **today** is a locally configured budget ceiling
 * (`ui.limits.monthly_budget_usd`), because `LocalTelemetryUsageProvider` is
 * the only provider wired up. That is a fact about this PR, not about the
 * model: no adapter exposes a quota *HTTP API*, but the Claude CLI does emit a
 * real provider-reported allowance on its `stream-json` channel
 * (`rate_limit_event` → `RateLimitEventData` in `utils/tokenParser.ts`), today
 * consumed only to pause/fast-fail a stage. The types below are sized so the
 * provider that surfaces it needs no type change.
 *
 * A `limit` of `null` means "no ceiling is known", never "no usage".
 *
 * The vocabularies below are deliberately wider than what
 * `LocalTelemetryUsageProvider` can produce, so a later per-adapter provider
 * can add windows without a type change. `docs/decisions/018-adapter-usage-quota-model.md`
 * records, member by member, which values have a producer today and which are
 * reserved for a named future provider.
 *
 * @see docs/decisions/018-adapter-usage-quota-model.md
 * @see Issue #658 - Provider-neutral adapter usage model
 */

import type { ExecutionAdapter } from "../../config/schema";

/**
 * How the active adapter is billed, as far as the resolved provider can tell.
 *
 * - `pay-per-token` — spend accrues per token and is denominated in dollars.
 *   Produced by `LocalTelemetryUsageProvider`.
 * - `subscription-window` — a flat plan with refilling per-window allowances
 *   (Claude Code Max session/weekly caps, Copilot premium requests). **No
 *   provider produces this today**; reserved for the `rate_limit_event`-backed
 *   Claude provider and a Copilot provider.
 * - `unknown` — no provider could describe this adapter. Always paired with an
 *   empty `windows` list; see `unknownUsageSnapshot`.
 */
export type UsagePlanKind = "subscription-window" | "pay-per-token" | "unknown";

/**
 * The period a `UsageWindow` measures.
 *
 * `session`, `daily` and `monthly` have a producer today. `rolling` and
 * `weekly` are reserved for the Claude `rate_limit_event` provider — that
 * channel's `rateLimitType` bucket names are literally `"five_hour"`,
 * `"daily"` and `"seven_day"`. Local telemetry cannot produce them: it has no
 * way to know where a provider's sliding window starts, and inventing a start
 * would be a fabricated percentage.
 */
export type UsageWindowScope = "session" | "rolling" | "daily" | "weekly" | "monthly";

/**
 * What `used`/`limit` are counted in.
 *
 * `usd` has a producer today. The rest are reserved for named providers:
 *
 * - `percent` — the Claude `rate_limit_event` provider. That channel reports
 *   `utilization` (0-100) and no denominator, so the only honest rendering is
 *   `used: utilization, limit: 100, unit: "percent"`. This is a **vendor
 *   reported** percentage; a percentage this model computed for a window
 *   whose real usage it does not know remains forbidden.
 * - `tokens` — a provider that gets an absolute token allowance.
 * - `requests` — a Copilot provider; premium requests per month.
 */
export type UsageUnit = "tokens" | "usd" | "requests" | "percent";

/**
 * How much the `used` figure can be trusted.
 *
 * - `measured` — every contributing record carried a vendor-emitted figure.
 *   This is what a `rate_limit_event`-backed provider reports: the vendor
 *   states the utilization outright.
 * - `estimated` — at least one contributing record was priced from the local
 *   rate card rather than reported by the vendor.
 * - `unknown` — at least one contributing record could not be priced at all,
 *   so `used` is a floor and not a total.
 *
 * Later providers MUST honour the same contract: `measured` is reserved for
 * figures the provider itself reported, and any derived or unpriced input
 * degrades the whole window. Confidence is never averaged — the weakest input
 * decides, because a total that folds in one unpriced record is not a total.
 */
export type UsageConfidence = "measured" | "estimated" | "unknown";

/**
 * One measured period of adapter usage.
 *
 * The list on a snapshot is open by design: a provider emits whichever windows
 * it can actually measure, and consumers render what they are given rather
 * than expecting a fixed set.
 */
export interface UsageWindow {
  /** Stable identity, unique within a snapshot. Convention: `<providerId>:<scope>`. */
  id: string;
  /** Human-facing name for the period, e.g. "This month". */
  label: string;
  scope: UsageWindowScope;
  /**
   * Model family this window is scoped to, when the provider buckets per
   * family (Max meters Opus separately from Sonnet). Absent means the window
   * covers every model the adapter ran.
   */
  modelFamily?: string;
  /**
   * Consumption in `unit` — an absolute figure the producer observed, never a
   * ratio this model derived. When `unit` is `"percent"` the figure is one the
   * provider itself reported.
   */
  used: number;
  /**
   * Ceiling in `unit`, or `null` when no ceiling is known. Carries either a
   * locally configured budget or a provider-reported allowance; consumers must
   * not assume which. `null` MUST render as "no limit configured" — never as a
   * zero-width or full bar. `used > limit` is legal (an overage-enabled plan
   * keeps serving past 100%).
   */
  limit: number | null;
  unit: UsageUnit;
  /** When `used` returns to zero, or `null` when the period has no scheduled reset. */
  resetsAt: Date | null;
  confidence: UsageConfidence;
}

/** The billing arrangement a snapshot describes. */
export interface UsagePlan {
  kind: UsagePlanKind;
}

/** Everything a usage surface needs to render the active adapter's usage. */
export interface UsageSnapshot {
  adapter: ExecutionAdapter;
  plan: UsagePlan;
  /** When this snapshot was derived — the input to staleness checks. */
  capturedAt: Date;
  /** Empty exactly when `plan.kind === "unknown"`. */
  windows: UsageWindow[];
}

/**
 * A source of usage for some subset of adapters.
 *
 * `supports` is a cheap, synchronous, adapter-only predicate so the registry
 * can resolve without I/O. `getSnapshot` returns `null` — not an empty
 * snapshot — when the provider supports the adapter in principle but has no
 * data to describe it; the caller turns that into the unknown snapshot so
 * "cannot say" has exactly one representation.
 */
export interface UsageProvider {
  /** Stable id, used as the `UsageWindow.id` prefix and in diagnostics. */
  readonly id: string;
  supports(adapter: ExecutionAdapter): boolean;
  getSnapshot(adapter: ExecutionAdapter): Promise<UsageSnapshot | null>;
}

/**
 * The one honest answer when nothing can describe an adapter.
 *
 * `plan.kind: "unknown"` with an empty window list. Never a fabricated
 * percentage, and never a window whose `used` is a placeholder zero — a zeroed
 * bar is indistinguishable from a genuine "you have spent nothing", so we
 * refuse to draw one.
 */
export function unknownUsageSnapshot(adapter: ExecutionAdapter, capturedAt: Date): UsageSnapshot {
  return {
    adapter,
    plan: { kind: "unknown" },
    capturedAt,
    windows: [],
  };
}
