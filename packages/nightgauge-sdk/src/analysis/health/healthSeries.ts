/**
 * The health series — one persisted health time-series, one schema, one
 * writer (#1898).
 *
 * Every pipeline run appends exactly one `HealthTrendEntry` to the checkout's
 * `health/trends.jsonl`, computed by `HealthAnalysisEngine`. The dashboard's
 * headline score is the latest entry, so the number a user reads and the
 * number persisted are the same number from the same engine.
 *
 * An entry records what the engine measured and nothing else:
 *
 * - a dimension without enough data is NOT written as its placeholder score;
 *   it is listed in `excluded_dimensions` with the reason (#1900);
 * - a run whose every dimension was starved is written with
 *   `overall_score: null` and `status: "no-data"` — not omitted, and not
 *   coerced to a number (#1197).
 *
 * @see docs/HEALTH_MONITORING.md § The health series
 */

import type { HealthAnalysisResult, HealthDimension, HealthTrendEntry } from "./types.js";
import { SEVERITY_ORDER } from "./severityMapping.js";
import { HealthTrendsWriter } from "./HealthTrendsWriter.js";
import { excludedDimensionReason } from "./HealthAnalysisEngine.js";

/** Identity of the run an entry scores. */
export interface HealthTrendRunMeta {
  issueNumber: number;
  runId: string;
}

/** Build the series entry for one analysis. Pure. */
export function buildHealthTrendEntry(
  result: HealthAnalysisResult,
  meta: HealthTrendRunMeta
): HealthTrendEntry {
  const dimensions: Partial<Record<HealthDimension, number>> = {};
  const excluded: Partial<Record<HealthDimension, string>> = {};
  for (const [key, dim] of Object.entries(result.dimensions)) {
    if (!dim) continue;
    const name = key as HealthDimension;
    if (dim.hasEnoughData) {
      dimensions[name] = dim.score;
    } else {
      excluded[name] = excludedDimensionReason(dim);
    }
  }

  const significantFindings = Object.values(result.dimensions)
    .filter((d) => d !== undefined && d.hasEnoughData)
    .flatMap((d) => d!.findings)
    .sort((a, b) => (SEVERITY_ORDER[b.severity] ?? 0) - (SEVERITY_ORDER[a.severity] ?? 0))
    .slice(0, 3)
    .map((f) => f.title);

  return {
    schema_version: "2",
    timestamp: result.analyzedAt,
    run_id: meta.runId,
    issue_number: meta.issueNumber,
    overall_score: result.overallScore,
    status: result.overallStatus,
    dimensions,
    excluded_dimensions: excluded,
    significant_findings: significantFindings,
  };
}

/**
 * Append one run's entry to the series and return it. The caller displays
 * the returned entry, never a separately computed number.
 *
 * Non-throwing: a failed write logs and still returns the entry.
 */
export async function recordHealthTrend(
  workspaceRoot: string,
  result: HealthAnalysisResult,
  meta: HealthTrendRunMeta,
  retentionDays = 90
): Promise<HealthTrendEntry> {
  const entry = buildHealthTrendEntry(result, meta);
  await HealthTrendsWriter.append(workspaceRoot, entry);
  // Awaited, not fired and forgotten: a prune rewrites the file from a read,
  // so one still in flight when the next run appends would rename a stale copy
  // over that run's entry.
  await HealthTrendsWriter.pruneOldEntries(workspaceRoot, retentionDays).catch(() => 0);
  return entry;
}
