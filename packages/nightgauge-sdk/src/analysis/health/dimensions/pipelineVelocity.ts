/**
 * Pipeline Velocity Dimension Analyzer
 *
 * Evaluates pipeline throughput and speed including duration trends,
 * critical path analysis, and throughput metrics.
 *
 * @see Issue #1101 - Multi-Dimensional Health Analysis Engine
 */

import type {
  HealthAnalysisInput,
  HealthAnalysisConfig,
  DimensionResult,
  Finding,
  Severity,
  Confidence,
} from "../types.js";
import { getHealthStatus } from "../types.js";
import {
  computePercentile,
  computeTrend,
  mean,
  clamp,
  hasEnoughData,
  buildPeriodComparison,
} from "../statistics.js";

// ── Internal types ─────────────────────────────────────────────────

interface StageDurationStats {
  stage: string;
  durations: number[];
  avgDuration: number;
  p95Duration: number;
  medianDuration: number;
}

interface WeeklyThroughput {
  week: string; // "YYYY-WW"
  count: number;
}

// ── Internal helpers ───────────────────────────────────────────────

/**
 * Derive an ISO week string ("YYYY-WW") from an ISO 8601 timestamp.
 *
 * Uses the Thursday-of-the-week rule (ISO 8601 week numbering) to keep
 * week boundaries consistent across year boundaries.
 */
function isoWeek(timestamp: string): string {
  const date = new Date(timestamp);
  // Copy date so we don't mutate the original
  const d = new Date(Date.UTC(date.getUTCFullYear(), date.getUTCMonth(), date.getUTCDate()));
  // Set to Thursday of the current week (day 4) — ISO 8601 week-year anchor
  d.setUTCDate(d.getUTCDate() + 4 - (d.getUTCDay() || 7));
  const yearStart = new Date(Date.UTC(d.getUTCFullYear(), 0, 1));
  const weekNumber = Math.ceil(((d.getTime() - yearStart.getTime()) / 86_400_000 + 1) / 7);
  const year = d.getUTCFullYear();
  return `${year}-${String(weekNumber).padStart(2, "0")}`;
}

/**
 * Compute per-stage duration statistics from execution records.
 */
function computeStageDurationStats(
  records: HealthAnalysisInput["executionHistory"]
): StageDurationStats[] {
  const byStage = new Map<string, number[]>();
  for (const r of records) {
    const existing = byStage.get(r.stage) ?? [];
    existing.push(r.durationMs);
    byStage.set(r.stage, existing);
  }

  const stats: StageDurationStats[] = [];
  for (const [stage, durations] of byStage) {
    stats.push({
      stage,
      durations,
      avgDuration: mean(durations),
      p95Duration: computePercentile(durations, 95),
      medianDuration: computePercentile(durations, 50),
    });
  }
  return stats;
}

/**
 * Compute total pipeline duration per issue run.
 *
 * Each unique issueNumber represents one pipeline run. The total duration
 * is the sum of all stage durations for that issue.
 */
function computeRunDurations(records: HealthAnalysisInput["executionHistory"]): number[] {
  const runTotals = new Map<number, number>();
  for (const r of records) {
    runTotals.set(r.issueNumber, (runTotals.get(r.issueNumber) ?? 0) + r.durationMs);
  }
  return [...runTotals.values()];
}

/**
 * Compute weekly throughput (runs per week) from execution records.
 *
 * Groups by the earliest timestamp per issueNumber to assign each run to
 * a single week, then counts runs per ISO week.
 */
function computeWeeklyThroughput(
  records: HealthAnalysisInput["executionHistory"]
): WeeklyThroughput[] {
  if (records.length === 0) return [];

  // Find the earliest timestamp for each issueNumber
  const firstSeenMs = new Map<number, number>();
  for (const r of records) {
    const ts = new Date(r.timestamp).getTime();
    const current = firstSeenMs.get(r.issueNumber);
    if (current === undefined || ts < current) {
      firstSeenMs.set(r.issueNumber, ts);
    }
  }

  // Map each issueNumber's first timestamp to an ISO week and count
  const weekCounts = new Map<string, number>();
  for (const [, ms] of firstSeenMs) {
    const week = isoWeek(new Date(ms).toISOString());
    weekCounts.set(week, (weekCounts.get(week) ?? 0) + 1);
  }

  // Return sorted chronologically
  return [...weekCounts.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([week, count]) => ({ week, count }));
}

// ── Main Analyzer ──────────────────────────────────────────────────

export function analyzePipelineVelocity(
  dataset: HealthAnalysisInput,
  config: HealthAnalysisConfig,
  baseline?: HealthAnalysisInput
): DimensionResult {
  const records = dataset.executionHistory;
  const sampleSize = records.length;
  const minSamples = config.minimumSampleSizes.basic;

  // Early return — no data at all
  if (sampleSize === 0) {
    return {
      dimension: "pipeline-velocity",
      score: 70,
      status: getHealthStatus(70),
      findings: [],
      metrics: {},
      hasEnoughData: false,
      sampleSize: 0,
    };
  }

  // Early return — insufficient data for meaningful analysis
  if (!hasEnoughData(sampleSize, minSamples)) {
    const runDurations = computeRunDurations(records);
    return {
      dimension: "pipeline-velocity",
      score: 70,
      status: getHealthStatus(70),
      findings: [],
      metrics: {
        avgRunDurationMs: mean(runDurations),
        uniqueRuns: runDurations.length,
        sampleSize,
      },
      hasEnoughData: false,
      sampleSize,
    };
  }

  const findings: Finding[] = [];
  let findingIndex = 0;
  let score = 70; // moderate baseline — velocity is relative

  // ── 1. Throughput trend (runs per week) ──────────────────────────

  const weeklyThroughput = computeWeeklyThroughput(records);
  const throughputSeries = weeklyThroughput.map((w) => w.count);
  const avgWeeklyThroughput = mean(throughputSeries);
  const { direction: throughputDirection } = computeTrend(throughputSeries);
  const uniqueRuns = new Set(records.map((r) => r.issueNumber)).size;

  if (throughputDirection === "improving") {
    score += 10;
  } else if (throughputDirection === "degrading") {
    score -= 10;
    findings.push({
      id: `pv-${++findingIndex}`,
      dimension: "pipeline-velocity",
      severity: "medium" as Severity,
      title: "Pipeline throughput is declining",
      description: `Weekly pipeline throughput is trending downward. Average throughput over the analysis window is ${avgWeeklyThroughput.toFixed(1)} runs/week.`,
      impact:
        "Declining throughput means fewer issues are being resolved per week, reducing the overall velocity of the development cycle.",
      recommendation:
        "Review whether pipeline queue depth is growing, whether agent failures are causing retries, or whether issue intake has slowed. Check stage-level failure rates for bottlenecks that block completion.",
      evidence: {
        weeklyThroughput,
        avgWeeklyThroughput,
        uniqueRuns,
        throughputDirection,
      },
      confidence: (throughputSeries.length >= config.minimumSampleSizes.trend
        ? "high"
        : "medium") as Confidence,
    });
  }

  // ── 2. Duration trend (chronological per-stage analysis) ─────────

  // Sort records chronologically for time-series analysis
  const sorted = [...records].sort(
    (a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime()
  );

  // Compute an overall duration time series across all stages
  const overallDurationSeries = sorted.map((r) => r.durationMs);
  const { slope: durationSlope, direction: durationDirection } =
    computeTrend(overallDurationSeries);
  const avgDurationMs = mean(overallDurationSeries);
  const normalisedDurationSlope = avgDurationMs > 0 ? durationSlope / avgDurationMs : 0;

  if (durationDirection === "improving") {
    // Stages are getting faster
    score += 15;
  } else if (durationDirection === "degrading" && normalisedDurationSlope > 0.01) {
    score -= 15;
    findings.push({
      id: `pv-${++findingIndex}`,
      dimension: "pipeline-velocity",
      severity: (normalisedDurationSlope > 0.05 ? "high" : "medium") as Severity,
      title: "Pipeline stage durations are worsening over time",
      description: `Stage execution times are trending upward (normalised slope: ${(normalisedDurationSlope * 100).toFixed(2)}% per record). Average stage duration is ${(avgDurationMs / 1000).toFixed(1)}s.`,
      impact:
        "Increasing stage durations slow overall pipeline throughput, delay PR delivery, and consume more compute resources per run.",
      recommendation:
        "Profile the slowest growing stages. Look for accumulating context file sizes, increasing model input sizes, or stages that are doing more work over time (e.g. larger diffs, more files changed).",
      evidence: {
        durationSlope,
        normalisedDurationSlope,
        avgDurationMs,
        durationDirection,
        sampleSize,
      },
      confidence: (sampleSize >= config.minimumSampleSizes.trend ? "high" : "medium") as Confidence,
    });
  }

  // ── 3. Critical path analysis — slowest stage ────────────────────

  const stageDurationStats = computeStageDurationStats(records);

  // Identify the bottleneck stage (highest average duration)
  const bottleneck = stageDurationStats.reduce<StageDurationStats | null>(
    (max, s) => (max === null || s.avgDuration > max.avgDuration ? s : max),
    null
  );

  if (bottleneck !== null && stageDurationStats.length > 1) {
    const otherAvgs = stageDurationStats
      .filter((s) => s.stage !== bottleneck.stage)
      .map((s) => s.avgDuration);
    const avgOfOthers = mean(otherAvgs);

    // Only report as a finding if the bottleneck is meaningfully slower than peers
    if (avgOfOthers > 0 && bottleneck.avgDuration > avgOfOthers * 2) {
      findings.push({
        id: `pv-${++findingIndex}`,
        dimension: "pipeline-velocity",
        severity: (bottleneck.avgDuration > avgOfOthers * 4 ? "high" : "medium") as Severity,
        title: `Critical path bottleneck: "${bottleneck.stage}" stage`,
        description: `The "${bottleneck.stage}" stage has an average duration of ${(bottleneck.avgDuration / 1000).toFixed(1)}s, which is ${(bottleneck.avgDuration / avgOfOthers).toFixed(1)}× slower than the average of other stages (${(avgOfOthers / 1000).toFixed(1)}s).`,
        impact:
          "A single slow stage on the critical path delays the entire pipeline, regardless of how fast other stages run.",
        recommendation: `Investigate "${bottleneck.stage}" for opportunities to parallelise work, reduce context size, cache intermediate outputs, or route to a faster model for routine sub-tasks.`,
        evidence: {
          bottleneckStage: bottleneck.stage,
          bottleneckAvgDurationMs: bottleneck.avgDuration,
          avgOfOtherStagesMs: avgOfOthers,
          slowdownFactor: bottleneck.avgDuration / avgOfOthers,
          stageCount: stageDurationStats.length,
        },
        confidence: (bottleneck.durations.length >= minSamples ? "high" : "medium") as Confidence,
      });
    }
  }

  // ── 4. P95 duration outlier detection per stage ──────────────────

  const p95OutlierStages: Array<{
    stage: string;
    p95: number;
    median: number;
    ratio: number;
  }> = [];

  for (const stats of stageDurationStats) {
    if (stats.durations.length < 3) continue; // insufficient data for reliable P95
    const ratio = stats.medianDuration > 0 ? stats.p95Duration / stats.medianDuration : 0;
    if (ratio > 3) {
      p95OutlierStages.push({
        stage: stats.stage,
        p95: stats.p95Duration,
        median: stats.medianDuration,
        ratio,
      });
    }
  }

  // Scoring: +5 if no P95 outliers, -5 per outlier stage (max -15)
  if (p95OutlierStages.length === 0) {
    score += 5;
  } else {
    const deduction = Math.min(p95OutlierStages.length * 5, 15);
    score -= deduction;

    for (const outlier of p95OutlierStages) {
      findings.push({
        id: `pv-${++findingIndex}`,
        dimension: "pipeline-velocity",
        severity: (outlier.ratio > 5 ? "high" : "medium") as Severity,
        title: `P95 duration spike in "${outlier.stage}" stage`,
        description: `The P95 duration for "${outlier.stage}" is ${(outlier.p95 / 1000).toFixed(1)}s — ${outlier.ratio.toFixed(1)}× the median of ${(outlier.median / 1000).toFixed(1)}s. Occasional runs are dramatically slower than typical.`,
        impact:
          "High P95 duration variability creates unpredictable pipeline completion times and indicates reliability risk. Users or downstream automation cannot depend on consistent throughput.",
        recommendation: `Investigate "${outlier.stage}" for tail-latency causes: model timeouts, large context spikes on specific issues, retry storms, or external API rate limits. Consider adding a stage-level timeout circuit-breaker.`,
        evidence: {
          stage: outlier.stage,
          p95DurationMs: outlier.p95,
          medianDurationMs: outlier.median,
          ratio: outlier.ratio,
          sampleCount: stageDurationStats.find((s) => s.stage === outlier.stage)?.durations.length,
        },
        confidence: "medium" as Confidence,
      });
    }
  }

  // ── 5. Overall run duration assessment ───────────────────────────

  const runDurations = computeRunDurations(records);
  const avgRunDurationMs = mean(runDurations);
  const p95RunDurationMs = computePercentile(runDurations, 95);
  const medianRunDurationMs = computePercentile(runDurations, 50);

  // ── Clamp final score ────────────────────────────────────────────

  score = clamp(score, 0, 100);

  // ── Metrics payload ──────────────────────────────────────────────

  const metrics: Record<string, number> = {
    avgRunDurationMs,
    p95RunDurationMs,
    medianRunDurationMs,
    avgWeeklyThroughput,
    uniqueRuns,
    stageCount: stageDurationStats.length,
    p95OutlierStageCount: p95OutlierStages.length,
    durationSlope,
    normalisedDurationSlope,
    sampleSize,
  };

  // Include bottleneck metrics when a bottleneck stage exists
  if (bottleneck !== null) {
    metrics["bottleneckAvgDurationMs"] = bottleneck.avgDuration;
    metrics["bottleneckP95DurationMs"] = bottleneck.p95Duration;
  }

  // ── Period comparison (baseline) ─────────────────────────────────

  let periodComparison = undefined;
  if (baseline !== undefined && baseline.executionHistory.length > 0) {
    const baselineRunDurations = computeRunDurations(baseline.executionHistory);
    const baselineAvgRunDurationMs = mean(baselineRunDurations);
    periodComparison = buildPeriodComparison(
      avgRunDurationMs,
      baselineAvgRunDurationMs,
      sampleSize,
      /* lowerIsBetter */ true,
      config.confidenceThreshold
    );
  }

  return {
    dimension: "pipeline-velocity",
    score,
    status: getHealthStatus(score),
    findings,
    metrics,
    hasEnoughData: true,
    sampleSize,
    periodComparison,
  };
}
