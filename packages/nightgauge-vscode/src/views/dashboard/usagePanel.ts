/**
 * Usage & quota panel state derivation (Issue #661).
 *
 * The dashboard webview's counterpart to the status-bar meter (#659). Both
 * render the *same* `UsageSnapshot` produced by `AdapterUsageService` (#658) —
 * this module derives a view model from that snapshot and never re-aggregates
 * quota figures from anything else. Collapsing the two surfaces onto one
 * snapshot is the whole point of the epic; a second derivation path here would
 * put two different numbers in front of the operator for the same question.
 *
 * ## What comes from where
 *
 * | Figure                       | Source                                    |
 * | ---------------------------- | ----------------------------------------- |
 * | every window's used / limit / reset / confidence | `UsageSnapshot` only |
 * | burn rate, projected exhaustion | `DashboardState` run history           |
 * | recent-history strip         | `DashboardState` run history              |
 *
 * The split is #661's own instruction ("Reuse `DashboardState`'s existing
 * history and aggregate reads for the burn-rate and history strip; the quota
 * figures themselves come only from the snapshot").
 *
 * ## The burn rate is workspace-wide, not adapter-attributed
 *
 * `PipelineRunSummary` carries no adapter field — adapter identity exists only
 * on the raw history record's `tokens.per_stage[*].adapter`, which is why
 * `LocalTelemetryUsageProvider` reads raw records rather than
 * `DashboardState.getAggregates()` (ADR 018, "Finding"). So a rate derived
 * from `DashboardState` history is every adapter's spend, while the window it
 * is projected against is one adapter's. On a single-adapter workspace they
 * are the same number; on a mixed one the rate is an upper bound and the
 * projected exhaustion is therefore conservative (early). The panel says so
 * out loud rather than implying an attribution the data cannot support.
 *
 * @see docs/decisions/018-adapter-usage-quota-model.md
 * @see Issue #661 - Adapter usage & quota panel in the dashboard webview
 */

import type { ExecutionAdapter } from "../../config/schema";
import type {
  UsageConfidence,
  UsagePlanKind,
  UsageSnapshot,
  UsageUnit,
  UsageWindow,
  UsageWindowScope,
} from "../../services/usage/types";
import type { PipelineRunStatus, PipelineRunSummary } from "./DashboardState";
import { BurnRateProjector } from "../../utils/budgetIntelligence";

/**
 * How far back the burn rate looks.
 *
 * 7 days is this tree's *live* window convention — `DEFAULT_TREND_RANGE`,
 * the cost tab's default `CostDateRange`, and `DashboardAggregates`'
 * recent-vs-prior success-rate split all use it (30 days is the audit-side
 * convention). No fourth window is invented here.
 *
 * This is a *lookback for a rate*, not a usage window: ADR 018's "no new
 * window constant" rule governs `UsageWindow` boundaries, every one of which
 * still comes from the snapshot untouched.
 */
export const BURN_RATE_LOOKBACK_DAYS = 7;

/**
 * Fewest runs in the lookback before a rate is stated at all.
 *
 * Two, because two timestamped observations are the minimum from which an
 * elapsed-time rate exists at all — below that `BurnRateProjector` reports 0,
 * which would render as "you are burning nothing" rather than "we cannot say".
 * `BurnRateProjector`'s own default of 5 guards a *live warning* that can
 * interrupt a run, where a noisy early reading has a cost; this panel triggers
 * nothing, so the same guard would only withhold a real figure.
 */
const BURN_RATE_MIN_SAMPLES = 2;

/** How many runs the recent-history strip shows. */
export const RECENT_RUN_STRIP_SIZE = 10;

/**
 * Fill thresholds for a window's severity, matching the status-bar meter's
 * `usageThresholdColor` and the pre-existing budget section — one set of
 * numbers across every usage surface.
 */
const WARNING_PCT = 80;
const CRITICAL_PCT = 90;

const HOUR_MS = 60 * 60 * 1000;
const DAY_MS = 24 * HOUR_MS;

/**
 * How a window should read.
 *
 * - `unknown` — `confidence: "unknown"`: `used` is a floor, not a total, so
 *   the figure is labelled unknown rather than drawn as a plain bar. Takes
 *   precedence over every fill threshold: a percentage computed from a floor
 *   is not an "ok" percentage.
 * - `unmeasured` — no ceiling is known, so there is nothing to fill. Renders
 *   as an absolute figure, never a zero-width or full bar (ADR 018).
 * - `ok` / `warning` / `critical` — a trustworthy fill against a real ceiling.
 */
export type UsageWindowSeverity = "ok" | "warning" | "critical" | "unknown" | "unmeasured";

/** One window of the snapshot, prepared for rendering. */
export interface UsagePanelWindowView {
  id: string;
  label: string;
  scope: UsageWindowScope;
  modelFamily?: string;
  used: number;
  limit: number | null;
  unit: UsageUnit;
  resetsAt: Date | null;
  confidence: UsageConfidence;
  /**
   * When the provider observed the figure, when that predates the snapshot
   * (Issue #709). Absent for a figure derived at snapshot time.
   */
  observedAt?: Date;
  /** Fill percentage, or null when no ceiling is known. Never clamped. */
  pct: number | null;
  /** `pct` clamped to [0, 100] for the bar's width only; null when `pct` is. */
  barPct: number | null;
  /** True when `used` is a floor rather than a total (`confidence: "unknown"`). */
  usedIsFloor: boolean;
  severity: UsageWindowSeverity;
}

/** Windows the provider bucketed to one model family. */
export interface UsagePanelFamilyGroup {
  modelFamily: string;
  windows: UsagePanelWindowView[];
}

/** Why no burn rate is shown. Exactly one of these or a rate, never both. */
export type BurnRateUnavailableReason = "no-window" | "non-dollar-window" | "insufficient-history";

/** Burn rate and projected exhaustion for the active window. */
export interface UsagePanelBurnRateView {
  /** The window this rate is projected against. */
  windowId: string;
  windowLabel: string;
  lookbackDays: number;
  /** Runs that contributed to the rate. */
  sampleCount: number;
  usdPerHour: number;
  usdPerDay: number;
  /** The ceiling projected against, or null when the window has none. */
  limitUsd: number | null;
  /** True when the window is already at or past its ceiling. */
  alreadyExhausted: boolean;
  /**
   * When the ceiling is reached at the current rate — null when there is no
   * ceiling, no observed burn, or the ceiling is already passed.
   */
  projectedExhaustionAt: Date | null;
  /** Hours until `projectedExhaustionAt`; null whenever that is. */
  hoursToExhaustion: number | null;
  /** True when the window's `used` is a floor, so the projection is optimistic. */
  usedIsFloor: boolean;
}

/** One run in the recent-history strip. */
export interface UsagePanelRunView {
  issueNumber: number;
  title: string;
  runId?: string;
  startedAt: Date;
  status: PipelineRunStatus;
  costUsd: number;
  tokens: number;
}

/** Everything the usage panel renders. */
export interface UsagePanelState {
  adapter: ExecutionAdapter;
  planKind: UsagePlanKind;
  /** When the snapshot was derived — the input to the operator's staleness call. */
  capturedAt: Date;
  /** Windows covering every model the adapter ran. Empty on an unknown plan. */
  windows: UsagePanelWindowView[];
  /** Per-model-family windows, absent entirely when the snapshot carries none. */
  familyGroups: UsagePanelFamilyGroup[];
  burnRate: UsagePanelBurnRateView | null;
  burnRateUnavailableReason: BurnRateUnavailableReason | null;
  recentRuns: UsagePanelRunView[];
  recentTotals: { costUsd: number; tokens: number; runCount: number };
  lookbackDays: number;
}

/** Total tokens a run consumed, across every token class the run recorded. */
function runTokens(run: PipelineRunSummary): number {
  const usage = run.usage;
  return (
    (usage.inputTokens ?? 0) +
    (usage.outputTokens ?? 0) +
    (usage.cacheReadTokens ?? 0) +
    (usage.cacheCreationTokens ?? 0)
  );
}

/** Prepare one snapshot window for rendering. Quota figures pass through untouched. */
export function toWindowView(window: UsageWindow): UsagePanelWindowView {
  const hasCeiling = window.limit !== null && window.limit > 0;
  const pct = hasCeiling ? (window.used / (window.limit as number)) * 100 : null;
  const usedIsFloor = window.confidence === "unknown";

  let severity: UsageWindowSeverity;
  if (usedIsFloor) {
    severity = "unknown";
  } else if (pct === null) {
    severity = "unmeasured";
  } else if (pct >= CRITICAL_PCT) {
    severity = "critical";
  } else if (pct >= WARNING_PCT) {
    severity = "warning";
  } else {
    severity = "ok";
  }

  return {
    id: window.id,
    label: window.label,
    scope: window.scope,
    modelFamily: window.modelFamily,
    used: window.used,
    limit: window.limit,
    unit: window.unit,
    resetsAt: window.resetsAt,
    confidence: window.confidence,
    observedAt: window.observedAt,
    pct,
    barPct: pct === null ? null : Math.max(0, Math.min(100, pct)),
    usedIsFloor,
    severity,
  };
}

/**
 * Group the snapshot's `modelFamily` windows by family, preserving snapshot
 * order within each group and first-appearance order between groups.
 *
 * Returns an empty list when no window carries a family — the panel then omits
 * the whole section rather than rendering an empty heading. Local telemetry
 * emits no family windows today (ADR 018 lists `modelFamily` as reserved), so
 * that is the current path; a provider that buckets per family lights this up
 * with no change here.
 */
export function groupByModelFamily(windows: readonly UsageWindow[]): UsagePanelFamilyGroup[] {
  const groups = new Map<string, UsagePanelWindowView[]>();
  for (const window of windows) {
    if (window.modelFamily === undefined) {
      continue;
    }
    const existing = groups.get(window.modelFamily);
    if (existing) {
      existing.push(toWindowView(window));
    } else {
      groups.set(window.modelFamily, [toWindowView(window)]);
    }
  }
  return [...groups].map(([modelFamily, familyWindows]) => ({
    modelFamily,
    windows: familyWindows,
  }));
}

/**
 * The window a burn rate is worth projecting against: the first with a real
 * ceiling, since that is the only one an operator can be exhausted out of.
 * Falls back to the first window so a rate is still shown when nothing has a
 * ceiling (with no projection — there is nothing to exhaust).
 *
 * Per-family windows are excluded: they re-slice the same spend, so projecting
 * one would report a fraction of the adapter's usage as if it were the whole.
 */
export function selectActiveWindow(windows: readonly UsageWindow[]): UsageWindow | null {
  const overall = windows.filter((w) => w.modelFamily === undefined);
  const pool = overall.length > 0 ? overall : windows;
  return pool.find((w) => w.limit !== null && w.limit > 0) ?? pool[0] ?? null;
}

/**
 * Burn rate and projected exhaustion for one window, measured over the last
 * `BURN_RATE_LOOKBACK_DAYS` of run history.
 *
 * Reuses `BurnRateProjector` — the projector the headless orchestrator already
 * runs mid-stage — rather than adding a second rate/projection implementation.
 * The only generalization it needed was accepting the sample's own timestamp,
 * so historical runs can be replayed into it instead of being stamped `now`.
 *
 * Returns `null` when no honest rate exists:
 *
 * - the window is not denominated in dollars (`unit !== "usd"`). Run history
 *   records dollars; projecting a vendor-reported percentage or a request
 *   allowance from a dollar rate would be arithmetic across two different
 *   things. Reserved units (ADR 018) reach this branch the day a provider
 *   emits them.
 * - fewer than `BURN_RATE_MIN_SAMPLES` runs fall inside the lookback.
 */
export function computeUsageBurnRate(
  window: UsageWindow,
  history: readonly PipelineRunSummary[],
  now: Date = new Date()
): UsagePanelBurnRateView | null {
  if (window.unit !== "usd") {
    return null;
  }

  const since = now.getTime() - BURN_RATE_LOOKBACK_DAYS * DAY_MS;
  const samples = history
    .filter((run) => {
      const at = run.startedAt?.getTime();
      return (
        typeof at === "number" &&
        !Number.isNaN(at) &&
        at >= since &&
        at <= now.getTime() &&
        Number.isFinite(run.usage?.costUsd ?? Number.NaN)
      );
    })
    .sort((a, b) => a.startedAt.getTime() - b.startedAt.getTime());

  if (samples.length < BURN_RATE_MIN_SAMPLES) {
    return null;
  }

  const ceilingUsd = window.limit !== null && window.limit > 0 ? window.limit : 0;
  // earlyWarningRatio is the orchestrator's notification trigger and is unused
  // here (the panel warns nobody); passed as its own default so the value
  // carries no meaning it does not have.
  const projector = new BurnRateProjector(ceilingUsd, 0.7, BURN_RATE_MIN_SAMPLES);
  let cumulativeUsd = 0;
  for (const run of samples) {
    cumulativeUsd += run.usage.costUsd;
    projector.recordSample(cumulativeUsd, run.startedAt.getTime());
  }

  const usdPerMinute = projector.getBurnRatePerMinute();
  const alreadyExhausted = ceilingUsd > 0 && window.used >= ceilingUsd;
  const projection = alreadyExhausted ? null : projector.getProjection(window.used);
  const minutesToExhaustion =
    projection && projection.projectedMinutesRemaining > 0
      ? projection.projectedMinutesRemaining
      : null;

  return {
    windowId: window.id,
    windowLabel: window.label,
    lookbackDays: BURN_RATE_LOOKBACK_DAYS,
    sampleCount: samples.length,
    usdPerHour: usdPerMinute * 60,
    usdPerDay: usdPerMinute * 60 * 24,
    limitUsd: ceilingUsd > 0 ? ceilingUsd : null,
    alreadyExhausted,
    projectedExhaustionAt:
      minutesToExhaustion === null
        ? null
        : new Date(now.getTime() + minutesToExhaustion * 60 * 1000),
    hoursToExhaustion: minutesToExhaustion === null ? null : minutesToExhaustion / 60,
    usedIsFloor: window.confidence === "unknown",
  };
}

/**
 * Derive everything the usage panel renders.
 *
 * `null` means "there is no usage service at all" — no workspace root, so no
 * `.nightgauge/pipeline/history/` to read and nothing to name. The panel is
 * omitted entirely in that case. That is distinct from a snapshot whose
 * `plan.kind` is `"unknown"`, which *is* an answer ("nothing can describe this
 * adapter") and renders as an explanatory empty state naming the adapter, per
 * ADR 018's rule that "cannot say" has exactly one representation.
 */
export function buildUsagePanelState(
  snapshot: UsageSnapshot | null,
  history: readonly PipelineRunSummary[],
  now: Date = new Date()
): UsagePanelState | null {
  if (snapshot === null) {
    return null;
  }

  const overallWindows = snapshot.windows.filter((w) => w.modelFamily === undefined);
  const familyGroups = groupByModelFamily(snapshot.windows);

  const activeWindow = selectActiveWindow(snapshot.windows);
  const burnRate = activeWindow === null ? null : computeUsageBurnRate(activeWindow, history, now);
  let burnRateUnavailableReason: BurnRateUnavailableReason | null = null;
  if (burnRate === null) {
    if (activeWindow === null) {
      burnRateUnavailableReason = "no-window";
    } else if (activeWindow.unit !== "usd") {
      burnRateUnavailableReason = "non-dollar-window";
    } else {
      burnRateUnavailableReason = "insufficient-history";
    }
  }

  // An unknown plan carries no windows, so there is no adapter-attributed
  // usage to put a run strip beside. Showing an adapter-blind spend list under
  // "we cannot describe this adapter" would read as if it were that adapter's.
  const recentRuns =
    snapshot.plan.kind === "unknown"
      ? []
      : [...history]
          .filter((run) => run.startedAt instanceof Date && !Number.isNaN(run.startedAt.getTime()))
          .sort((a, b) => b.startedAt.getTime() - a.startedAt.getTime())
          .slice(0, RECENT_RUN_STRIP_SIZE)
          .map((run) => ({
            issueNumber: run.issueNumber,
            title: run.title,
            runId: run.runId,
            startedAt: run.startedAt,
            status: run.status,
            costUsd: run.usage.costUsd ?? 0,
            tokens: runTokens(run),
          }));

  return {
    adapter: snapshot.adapter,
    planKind: snapshot.plan.kind,
    capturedAt: snapshot.capturedAt,
    windows: overallWindows.map(toWindowView),
    familyGroups,
    burnRate,
    burnRateUnavailableReason,
    recentRuns,
    recentTotals: {
      costUsd: recentRuns.reduce((sum, run) => sum + run.costUsd, 0),
      tokens: recentRuns.reduce((sum, run) => sum + run.tokens, 0),
      runCount: recentRuns.length,
    },
    lookbackDays: BURN_RATE_LOOKBACK_DAYS,
  };
}
