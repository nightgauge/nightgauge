/**
 * The health series, extension side (#1898).
 *
 * There is ONE persisted health time-series: the checkout's
 * `health/trends.jsonl`, one `HealthTrendEntry` (schema 2) per pipeline run,
 * computed by the SDK's `HealthAnalysisEngine`. This module is its only writer
 * in the extension (`recordHealthForRun`, called once per completed run) and
 * the dashboard's reader. `nightgauge health trends` and `loopverdicts` read
 * the same file.
 *
 * The dashboard's headline score is the latest entry, so what a user reads and
 * what is persisted are the same number from the same engine. A run whose
 * every dimension was starved is recorded as `null` and shown as N/A; it is
 * never coerced to a number, and the trend chart skips it rather than plotting
 * it as zero.
 *
 * The legacy 4-component `pipeline/health-history.jsonl` is retired and not
 * migrated: it scored a different metric, so its entries cannot be placed on
 * this series. See docs/HEALTH_MONITORING.md § The health series.
 */

import {
  ControlBandLogWriter,
  HealthAnalysisEngine,
  HealthTrendsWriter,
  recordHealthTrend,
  type HealthTrendEntry,
} from "@nightgauge/sdk";
import { DataAggregator } from "../services/DataAggregator";
import { buildHealthInput } from "./buildHealthInput";
import { isUsableWorkspaceRoot } from "./cloneLayout";
import type { TrendAnalysis, TrendChartDay } from "../views/dashboard/HealthWidgetTypes";

/** The window each run's health entry is computed over. */
export const HEALTH_SERIES_PERIOD_DAYS = 30;

/** An entry that carries a score. Unscored (null) entries are not points. */
export type ScoredHealthEntry = HealthTrendEntry & { overall_score: number };

export function isScored(entry: HealthTrendEntry): entry is ScoredHealthEntry {
  return entry.overall_score !== null;
}

/**
 * Compute this run's health with the SDK engine, append it to the series, and
 * return the entry written. Returns null only when `repoRoot` is not a usable
 * checkout. Never throws.
 */
export async function recordHealthForRun(
  repoRoot: string,
  run: { issueNumber: number; runId: string }
): Promise<HealthTrendEntry | null> {
  if (!isUsableWorkspaceRoot(repoRoot)) return null;
  try {
    const filter = DataAggregator.parseDateFilter({ period: HEALTH_SERIES_PERIOD_DAYS });
    const dataset = await DataAggregator.aggregate(repoRoot, filter);
    const result = new HealthAnalysisEngine().analyze(buildHealthInput(dataset));
    const band = result.dimensions["cost-health"]?.controlBand;
    if (band) {
      // Every tier persists its excursion; `none` is skipped by the writer (#1286).
      await ControlBandLogWriter.append(repoRoot, {
        timestamp: new Date().toISOString(),
        run_id: run.runId,
        issue_number: run.issueNumber,
        ...band,
      });
    }
    return await recordHealthTrend(repoRoot, result, run);
  } catch (error) {
    console.warn(`[Nightgauge] Failed to record health for run ${run.runId}: ${error}`);
    return null;
  }
}

/** Series entries in `[startDate, endDate]`, oldest first. */
export async function readHealthSeries(
  workspaceRoot: string,
  opts: { startDate?: Date; endDate?: Date; limit?: number } = {}
): Promise<HealthTrendEntry[]> {
  if (!isUsableWorkspaceRoot(workspaceRoot)) return [];
  return HealthTrendsWriter.read(workspaceRoot, opts);
}

/** The most recent entry, scored or not — what the dashboard headline shows. */
export async function latestHealthEntry(workspaceRoot: string): Promise<HealthTrendEntry | null> {
  const [latest] = await readHealthSeries(workspaceRoot, { limit: 1 });
  return latest ?? null;
}

function aggregate(entries: HealthTrendEntry[], keyOf: (ts: string) => string): TrendChartDay[] {
  const buckets = new Map<string, { total: number; count: number }>();
  for (const e of entries) {
    if (!isScored(e)) continue;
    const key = keyOf(e.timestamp);
    const b = buckets.get(key) ?? { total: 0, count: 0 };
    b.total += e.overall_score;
    b.count++;
    buckets.set(key, b);
  }
  return [...buckets]
    .map(([date, b]) => ({ date, avgScore: Math.round(b.total / b.count), count: b.count }))
    .sort((a, b) => a.date.localeCompare(b.date));
}

/** Daily average of the scored entries. A day of only null entries has no point. */
export function aggregateHealthByDay(entries: HealthTrendEntry[]): TrendChartDay[] {
  return aggregate(entries, (ts) => ts.split("T")[0]);
}

/** Hourly average of the scored entries, for the 24h range. */
export function aggregateHealthByHour(entries: HealthTrendEntry[]): TrendChartDay[] {
  return aggregate(entries, (ts) => ts.slice(0, 13));
}

/**
 * Compare the recent half against the prior half of the bucketed series.
 *
 * - 24h: recent 12h vs prior 12h
 * - 7d: recent 3d vs prior 3d
 * - 30d: recent 7d vs prior 7d
 * - 90d: recent 14d vs prior 14d
 */
export function analyzeHealthTrend(
  dailyData: TrendChartDay[],
  comparisonBuckets: number = 7
): TrendAnalysis {
  const flat = (message: string): TrendAnalysis => ({
    direction: "stable",
    message,
    periodDays: comparisonBuckets,
    percentChange: 0,
  });
  if (dailyData.length === 0) return flat("Not enough data for trend analysis");
  if (dailyData.length === 1) return flat("Tracking started");

  const sorted = [...dailyData].sort((a, b) => b.date.localeCompare(a.date));
  const recent = sorted.slice(0, comparisonBuckets);
  const prior = sorted.slice(comparisonBuckets, comparisonBuckets * 2);
  if (prior.length === 0) return flat("Not enough history for trend comparison");

  const recentAvg = recent.reduce((sum, d) => sum + d.avgScore, 0) / recent.length;
  const priorAvg = prior.reduce((sum, d) => sum + d.avgScore, 0) / prior.length;
  if (priorAvg === 0) return flat("Insufficient baseline data");

  const percentChange = Math.round(((recentAvg - priorAvg) / priorAvg) * 100);

  // The analysis is a recent-vs-prior split, so the message names the
  // comparison rather than a single contiguous window: a 7-day chart with a
  // 3-day comparison must not read "over last 3 days".
  const windowLabel =
    comparisonBuckets < 1
      ? "the recent period"
      : comparisonBuckets === 12
        ? "prior 12 hours"
        : `prior ${comparisonBuckets} day${comparisonBuckets === 1 ? "" : "s"}`;

  if (percentChange > 2) {
    return {
      direction: "improving",
      message: `Health improved ${percentChange}% vs ${windowLabel}`,
      periodDays: comparisonBuckets,
      percentChange,
    };
  }
  if (percentChange < -2) {
    return {
      direction: "declining",
      message: `Health declined ${Math.abs(percentChange)}% vs ${windowLabel}`,
      periodDays: comparisonBuckets,
      percentChange,
    };
  }
  return {
    direction: "stable",
    message: `Health stable vs ${windowLabel}`,
    periodDays: comparisonBuckets,
    percentChange,
  };
}
