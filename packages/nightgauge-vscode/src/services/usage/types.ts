/**
 * Provider-neutral adapter usage model (Issue #658).
 *
 * One snapshot shape that every usage surface reads — the status-bar meter
 * (#659) and the usage webview (#661) both consume `UsageSnapshot` and nothing
 * else, so they cannot drift into two data paths.
 *
 * ## What "quota" means here
 *
 * A `limit` is one of two very different things, and consumers must not
 * assume which. `LocalTelemetryUsageProvider` produces locally configured
 * budget ceilings (`ui.limits.monthly_budget_usd`).
 * `ClaudeRateLimitUsageProvider` (Issue #709) produces a provider-reported
 * allowance: the Claude CLI emits a real utilization figure on its
 * `stream-json` channel (`rate_limit_event` → `RateLimitEventData` in
 * `utils/tokenParser.ts`), which that provider reads alongside the
 * pause/fast-fail consumer that has always keyed on it. The types below were
 * sized for that provider in advance and needed no widening when it landed.
 *
 * A `limit` of `null` means "no ceiling is known", never "no usage".
 *
 * The vocabularies below were deliberately wider than what
 * `LocalTelemetryUsageProvider` could produce, so a later per-adapter provider
 * could add windows without a type change. Issue #709 was that provider and
 * needed none. `docs/decisions/018-adapter-usage-quota-model.md` records,
 * member by member, which values have a producer and which are still reserved
 * for a named future one — its amendment section covers what #709 changed.
 *
 * @see docs/decisions/018-adapter-usage-quota-model.md
 * @see Issue #658 - Provider-neutral adapter usage model
 */

import type { ExecutionAdapter } from "../../config/schema";
import type { ClaudeFeedHealth } from "./claudeStatusLineSetup";

/**
 * How the active adapter is billed, as far as the resolved provider can tell.
 *
 * - `pay-per-token` — spend accrues per token and is denominated in dollars.
 *   Produced by `LocalTelemetryUsageProvider`.
 * - `subscription-window` — a flat plan with refilling per-window allowances
 *   (Claude Code Max session/weekly caps, Copilot premium requests). Produced
 *   by `ClaudeRateLimitUsageProvider` (Issue #709) once a `rate_limit_event`
 *   has actually been observed. It follows the **observed signal**, never the
 *   adapter name: the `claude` adapter also covers an API-key, pay-per-token
 *   path that emits no such event and stays `pay-per-token`. A Copilot
 *   provider would be the second producer; none exists yet.
 * - `unknown` — no provider could describe this adapter. Always paired with an
 *   empty `windows` list; see `unknownUsageSnapshot`.
 */
export type UsagePlanKind = "subscription-window" | "pay-per-token" | "unknown";

/**
 * The period a `UsageWindow` measures.
 *
 * `session`, `daily` and `monthly` come from local telemetry. `rolling` and
 * `weekly` come from `ClaudeRateLimitUsageProvider` (Issue #709), which maps
 * that channel's `rateLimitType` bucket names — literally `"five_hour"`,
 * `"daily"` and `"seven_day"` — onto `rolling`, `daily` and `weekly`. Local
 * telemetry still cannot produce the sliding ones: it has no way to know
 * where a provider's window starts, and inventing a start would be a
 * fabricated percentage. The vendor stating its own utilization is what makes
 * the difference.
 */
export type UsageWindowScope = "session" | "rolling" | "daily" | "weekly" | "monthly";

/**
 * What `used`/`limit` are counted in.
 *
 * Two have producers:
 *
 * - `usd` — every `LocalTelemetryUsageProvider` window.
 * - `percent` — `ClaudeRateLimitUsageProvider` (Issue #709). That channel
 *   reports `utilization` (0-100) and no denominator, so the only honest
 *   rendering is `used: utilization, limit: 100, unit: "percent"`. This is a
 *   **vendor reported** percentage; a percentage this model computed for a
 *   window whose real usage it does not know remains forbidden.
 *
 * Two are still reserved for named providers, and nothing emits them:
 *
 * - `tokens` — a provider that gets an absolute token allowance.
 * - `requests` — a Copilot provider; premium requests per month.
 */
export type UsageUnit = "tokens" | "usd" | "requests" | "percent";

/**
 * How much the `used` figure can be trusted.
 *
 * - `measured` — every contributing record carried a vendor-emitted figure,
 *   and that figure describes the present. `ClaudeRateLimitUsageProvider`
 *   reports it only for a reading observed during the run that is still
 *   streaming (Issue #709).
 * - `estimated` — at least one contributing record was priced from the local
 *   rate card rather than reported by the vendor, **or** the vendor's own
 *   figure is a cached last-seen reading rather than a live one. A percentage
 *   the vendor stated an hour ago is an estimate of now; `observedAt` carries
 *   the as-of so a surface can say how old it is.
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
   * family. Absent means the window covers every model the adapter ran, which
   * is every window produced today: local telemetry has no per-family ceiling
   * to measure against, and the Claude `rate_limit_event` channel names a
   * *window* (five-hour, daily, seven-day) rather than a model, so Issue #709
   * deliberately leaves this unset rather than inventing a breakdown.
   */
  modelFamily?: string;
  /**
   * Consumption in `unit` — an absolute figure the producer observed, never a
   * ratio this model derived. When `unit` is `"percent"` the figure is one the
   * provider itself reported.
   *
   * `null` means the window EXISTS but nothing has been observed for it yet
   * (#808): the operator has declared a subscription plan, so we know which
   * windows their plan has, and no reading has arrived to fill them.
   *
   * This was widened from a bare `number` deliberately, and #808 asked for the
   * finding before the reshape. It is the finding: a declared-but-unobserved
   * window is not representable otherwise. `used: 0` renders as "0% used",
   * which is a fabricated utilization — precisely what ADR 018 and
   * `ClaudeRateLimitStore`'s drop-expired-readings rule exist to prevent — and
   * `limit: null` alone only suppresses the BAR, not the figure beside it.
   * Making the illegal state unrepresentable beats flagging it.
   *
   * `null` MUST render as "awaiting a reading" or an equivalent absence. It is
   * never a zero, never a full bar, and never an input to a percentage.
   */
  used: number | null;
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
  /**
   * When the provider actually observed this figure, when that is older than
   * the snapshot's `capturedAt` (Issue #709).
   *
   * Absent means "observed as the snapshot was derived" — the case for every
   * window local telemetry produces, since it reduces the history on demand.
   * Present means the provider is serving a cached last-seen reading, and a
   * surface rendering it MUST show the as-of rather than presenting a stale
   * percentage as current. `capturedAt` cannot carry this: it is when the
   * snapshot was assembled, not when the number was true.
   */
  observedAt?: Date;
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
  /**
   * Empty exactly when `plan.kind === "unknown"`.
   *
   * A declared subscription plan with no reading yet is NOT empty: it carries
   * its plan's windows with `used: null` (#808), because "your plan has a
   * 5-hour and a 7-day allowance and we have not measured them" is a different
   * statement from "we know nothing".
   */
  windows: UsageWindow[];
  /**
   * How the Claude usage feed is doing, when the adapter is `claude` (#810).
   *
   * Carried on the snapshot so the status-bar tooltip, the Dashboard Usage
   * panel and the enable command all read ONE verdict. They used to infer the
   * feed's state independently from `plan.kind`, which is why two of them could
   * offer to enable a feed while the third called it already enabled, in the
   * same session.
   *
   * Undefined for every other adapter, and while the first probe is in flight.
   */
  claudeFeedHealth?: ClaudeFeedHealth;
}

/**
 * A source of usage for some subset of adapters.
 *
 * `supports` is a cheap, synchronous, adapter-only predicate so the registry
 * can resolve without I/O. `getSnapshot` returns `null` — not an empty
 * snapshot — when the provider supports the adapter in principle but has no
 * data to describe it.
 *
 * `null` is a **fall-through**, not a verdict: `AdapterUsageService` asks the
 * next provider that claims the same adapter, and only emits the unknown
 * snapshot once every one of them has declined. That is what lets a provider
 * claim an adapter on the strength of a signal it has not observed yet
 * (Issue #709 — `ClaudeRateLimitUsageProvider` claims `claude` but says
 * nothing until a `rate_limit_event` arrives, leaving the dollar windows to
 * answer meanwhile) without a `supports` predicate that lies about what it
 * can describe.
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
