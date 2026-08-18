/**
 * LocalTelemetryUsageProvider — adapter usage derived from telemetry
 * nightgauge already persists (Issue #658).
 *
 * The first `UsageProvider`, and the only one that needs no provider quota
 * API: it reads the per-stage token/cost records written to
 * `.nightgauge/pipeline/history/YYYY-MM-DD.jsonl` and buckets the dollars
 * attributed to one adapter into session / daily / monthly windows.
 *
 * ## Why this does not reuse `DashboardState.getAggregates()`
 *
 * #658's technical notes suggested reusing it as the reducer, and told us to
 * stop and document if the observed API differs. It does, in two ways that
 * matter:
 *
 * 1. **No adapter dimension.** `getAggregates()` reduces over
 *    `PipelineRunSummary`, which has no adapter field at all — adapter
 *    identity lives only on `tokens.per_stage[*].adapter` in the raw history
 *    record. An adapter-aware figure cannot be recovered from its output.
 * 2. **No calendar windows.** It exposes all-time and "session" totals only.
 *
 * So the dollar reduction happens here, over raw records. What we *do* reuse
 * is the dashboard's notion of where a session starts — injected as
 * `UsageSessionClock`, which `DashboardState` satisfies structurally via
 * `getSessionStartTime()` — so the two surfaces agree on the boundary instead
 * of inventing a second one. We also bucket on the run's `started_at`, the
 * same field `DashboardState.computeRecentActivityDelta` buckets on.
 *
 * @see docs/decisions/018-adapter-usage-quota-model.md
 * @see Issue #658 - Provider-neutral adapter usage model
 */

import type { ExecutionAdapter } from "../../config/schema";
import type {
  ExecutionHistoryRecord,
  HistoryStageTokenUsage,
} from "../../schemas/executionHistory";
import { ExecutionHistoryReader } from "../../utils/executionHistoryReader";
import { getLimitsSettings } from "../../config/limitsSettings";
import type { UsageConfidence, UsageProvider, UsageSnapshot, UsageWindow } from "./types";

const DAY_MS = 24 * 60 * 60 * 1000;

/**
 * Adapters this provider will describe.
 *
 * Deliberately not "all of them". A dollar meter is only meaningful where
 * dollars are the meter:
 *
 * - `lm-studio` / `ollama` run locally against the user's own hardware. Their
 *   `cost_usd` is a genuine `$0` (the Go writer documents that it never marks
 *   those `cost_unstamped`), so a budget bar for them would sit at 0% forever
 *   — exactly the silently-zeroed bar #658 forbids.
 * - `copilot` is a flat seat subscription. Its real meter is premium requests
 *   per month, a number nothing in nightgauge's telemetry records; a dollar
 *   figure would be answering a different question than the one asked.
 *
 * For those three the registry resolves no provider and the snapshot is
 * `plan.kind: "unknown"` with no windows, which is the honest answer.
 */
export const LOCAL_TELEMETRY_METERED_ADAPTERS: readonly ExecutionAdapter[] = [
  "claude",
  "codex",
  "gemini",
  "gemini-sdk",
  "grok",
];

/**
 * Where history records come from.
 *
 * A port rather than a direct `ExecutionHistoryReader` call so the reduction
 * can be tested against exact records; `forWorkspace()` supplies the real
 * reader.
 */
export interface UsageHistorySource {
  readDateRange(startDate: Date, endDate: Date): Promise<ExecutionHistoryRecord[]>;
}

/**
 * Where the "session" window starts. `DashboardState` satisfies this.
 *
 * Required, not optional: falling back to local midnight would make the
 * session window a duplicate of the daily one, which is a window that looks
 * like it measures something it does not.
 */
export interface UsageSessionClock {
  getSessionStartTime(): Date;
}

/** One priced stage attributed to the adapter under inspection. */
interface CostEvent {
  at: Date;
  costUsd: number;
  confidence: UsageConfidence;
}

/**
 * How much a single stage's `cost_usd` can be trusted.
 *
 * `cost_unstamped` is checked first because it is the field the Go history
 * writer actually maintains for this purpose: true means `cost_usd` is a
 * placeholder zero because the (provider, model) pair missed the pricing
 * registry. It is never set for a legitimately free local-provider run, so
 * its absence on a zero-cost stage means the zero is real.
 *
 * `cost_source` is honoured when present: `"native"` is a vendor/CLI-reported
 * measurement, `"computed"` is a rate-card estimate, `"unknown"` is an
 * explicit "could not price this at all". Absent (Issue #682) means one of
 * two things: a pre-#682 record, or a Go write path that has not been taught
 * to set the field yet (RecordTerminatingStageTokens's failure-path
 * synthesis — see its doc comment in internal/state/runtime_state.go) — and
 * this function cannot tell those apart. Until #682 the READER backfilled
 * `"native"` for any absent field with `cost_usd > 0`; that manufactured a
 * confident answer from silence, which is exactly the bug #682 fixes. Absent
 * now honestly reports `"unknown"`, the same as the writer's own explicit
 * `"unknown"` label — silence and an explicit "I don't know" carry the same
 * weight here.
 */
export function stageCostConfidence(usage: HistoryStageTokenUsage): UsageConfidence {
  if (usage.cost_unstamped === true) {
    return "unknown";
  }
  switch (usage.cost_source) {
    case "native":
      return "measured";
    case "computed":
      return "estimated";
    case "unknown":
    default:
      return "unknown";
  }
}

/**
 * Fold per-event confidence into a window's confidence: the weakest input
 * decides. A window with no contributing events is `"measured"` — a measured
 * zero is a real answer ("you have not run this adapter today"), distinct
 * from having nothing to say at all, which is signalled by returning no
 * snapshot.
 */
function foldConfidence(events: readonly CostEvent[]): UsageConfidence {
  let seenEstimated = false;
  for (const event of events) {
    if (event.confidence === "unknown") {
      return "unknown";
    }
    if (event.confidence === "estimated") {
      seenEstimated = true;
    }
  }
  return seenEstimated ? "estimated" : "measured";
}

function startOfLocalDay(now: Date): Date {
  return new Date(now.getFullYear(), now.getMonth(), now.getDate());
}

function startOfNextLocalDay(now: Date): Date {
  return new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1);
}

function startOfLocalMonth(now: Date): Date {
  return new Date(now.getFullYear(), now.getMonth(), 1);
}

function startOfNextLocalMonth(now: Date): Date {
  return new Date(now.getFullYear(), now.getMonth() + 1, 1);
}

/**
 * Pull every stage attributed to `adapter` out of the records.
 *
 * Stages whose `adapter` field is absent are skipped, never defaulted: the
 * history schema is explicit that absence means adapter-unknown, and guessing
 * would credit one adapter with another's spend.
 */
function collectCostEvents(
  records: readonly ExecutionHistoryRecord[],
  adapter: ExecutionAdapter
): CostEvent[] {
  const events: CostEvent[] = [];
  for (const record of records) {
    if (record.record_type !== "run") {
      continue;
    }
    const perStage = record.tokens.per_stage;
    if (!perStage) {
      continue;
    }
    const at = new Date(record.started_at);
    if (Number.isNaN(at.getTime())) {
      continue;
    }
    for (const usage of Object.values(perStage)) {
      if (!usage || usage.adapter !== adapter) {
        continue;
      }
      events.push({ at, costUsd: usage.cost_usd, confidence: stageCostConfidence(usage) });
    }
  }
  return events;
}

function buildWindow(
  id: string,
  label: string,
  scope: UsageWindow["scope"],
  events: readonly CostEvent[],
  since: Date,
  limit: number | null,
  resetsAt: Date | null
): UsageWindow {
  const inWindow = events.filter((event) => event.at.getTime() >= since.getTime());
  return {
    id,
    label,
    scope,
    used: inWindow.reduce((sum, event) => sum + event.costUsd, 0),
    limit,
    unit: "usd",
    resetsAt,
    confidence: foldConfidence(inWindow),
  };
}

export class LocalTelemetryUsageProvider implements UsageProvider {
  readonly id = "local-telemetry";

  constructor(
    private readonly source: UsageHistorySource,
    private readonly sessionClock: UsageSessionClock
  ) {}

  /** Wire the provider to a workspace's on-disk history. */
  static forWorkspace(
    workspaceRoot: string,
    sessionClock: UsageSessionClock
  ): LocalTelemetryUsageProvider {
    return new LocalTelemetryUsageProvider(
      {
        readDateRange: (startDate, endDate) =>
          ExecutionHistoryReader.readDateRange(workspaceRoot, startDate, endDate),
      },
      sessionClock
    );
  }

  supports(adapter: ExecutionAdapter): boolean {
    return LOCAL_TELEMETRY_METERED_ADAPTERS.includes(adapter);
  }

  /**
   * Derive session / daily / monthly dollar windows for `adapter`.
   *
   * Returns `null` — leaving the caller to emit the unknown snapshot — when
   * the adapter is not metered in dollars, or when no record in the read
   * horizon attributes a single stage to it. The second case is not the same
   * as "$0 spent": with no attributed record we cannot tell a fresh install
   * from a quiet month, and drawing a 0% bar for either would be a claim we
   * cannot support.
   */
  async getSnapshot(adapter: ExecutionAdapter): Promise<UsageSnapshot | null> {
    if (!this.supports(adapter)) {
      return null;
    }

    const now = new Date();
    const sessionStart = this.sessionClock.getSessionStartTime();
    const dayStart = startOfLocalDay(now);
    const monthStart = startOfLocalMonth(now);

    // Read from the earliest window boundary we will need. No 7/30-day
    // constant is introduced: every boundary here is calendar-derived from
    // the windows #658 asks for. The one day of padding on each end absorbs
    // the skew between the writer's local-calendar filenames and the reader's
    // UTC day iteration; records outside a window are filtered by timestamp
    // regardless.
    const horizonStart = new Date(Math.min(sessionStart.getTime(), monthStart.getTime()) - DAY_MS);
    const horizonEnd = new Date(now.getTime() + DAY_MS);

    const records = await this.source.readDateRange(horizonStart, horizonEnd);
    const events = collectCostEvents(records, adapter);
    if (events.length === 0) {
      return null;
    }

    const monthlyBudgetUsd = getLimitsSettings().monthlyBudgetUsd;

    return {
      adapter,
      plan: { kind: "pay-per-token" },
      capturedAt: now,
      windows: [
        buildWindow(
          `${this.id}:session`,
          "This session",
          "session",
          events,
          sessionStart,
          null,
          // A session ends when the user's session ends; there is no clock
          // that resets it, so claiming a reset time would be an invention.
          null
        ),
        buildWindow(
          `${this.id}:daily`,
          "Today",
          "daily",
          events,
          dayStart,
          null,
          startOfNextLocalDay(now)
        ),
        buildWindow(
          `${this.id}:monthly`,
          "This month",
          "monthly",
          events,
          monthStart,
          // The configured budget is the only ceiling that exists — a local
          // policy, not a provider-reported limit. 0 means "not configured",
          // which is `null`, not a limit of zero.
          monthlyBudgetUsd > 0 ? monthlyBudgetUsd : null,
          startOfNextLocalMonth(now)
        ),
      ],
    };
  }
}
