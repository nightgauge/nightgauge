/**
 * Stage Effectiveness Dimension Analyzer
 *
 * Evaluates per-stage success rates, retry patterns, duration trends,
 * and bottleneck identification across pipeline stages.
 *
 * @see Issue #1101 - Multi-Dimensional Health Analysis Engine
 */

import type { ExecutionHistoryRecord } from "../../types.js";
import {
  type HealthAnalysisInput,
  type HealthAnalysisConfig,
  type DimensionResult,
  type Finding,
  type Severity,
  type Confidence,
  getHealthStatus,
} from "../types.js";
import { computeTrend, mean, clamp, hasEnoughData, buildPeriodComparison } from "../statistics.js";

// ── Internal Types ─────────────────────────────────────────────────

interface StageMetrics {
  stage: string;
  records: ExecutionHistoryRecord[];
  successRate: number;
  firstAttemptPassRate: number;
  avgRetries: number;
  avgDurationMs: number;
  /** Durations in chronological order for trend analysis */
  durationTimeSeries: number[];
}

// ── Stage Grouping ─────────────────────────────────────────────────

function groupByStage(records: ExecutionHistoryRecord[]): Map<string, ExecutionHistoryRecord[]> {
  const groups = new Map<string, ExecutionHistoryRecord[]>();
  for (const record of records) {
    const existing = groups.get(record.stage);
    if (existing) {
      existing.push(record);
    } else {
      groups.set(record.stage, [record]);
    }
  }
  return groups;
}

function sortByTimestamp(records: ExecutionHistoryRecord[]): ExecutionHistoryRecord[] {
  return [...records].sort(
    (a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime()
  );
}

function computeStageMetrics(stage: string, records: ExecutionHistoryRecord[]): StageMetrics {
  const sorted = sortByTimestamp(records);
  const total = sorted.length;

  const successCount = sorted.filter((r) => r.success).length;
  const successRate = total > 0 ? successCount / total : 0;

  const firstAttemptSuccesses = sorted.filter((r) => r.retries === 0 && r.success).length;
  const firstAttemptPassRate = total > 0 ? firstAttemptSuccesses / total : 0;

  const avgRetries = mean(sorted.map((r) => r.retries));
  const avgDurationMs = mean(sorted.map((r) => r.durationMs));
  const durationTimeSeries = sorted.map((r) => r.durationMs);

  return {
    stage,
    records: sorted,
    successRate,
    firstAttemptPassRate,
    avgRetries,
    avgDurationMs,
    durationTimeSeries,
  };
}

// ── Bottleneck Detection ───────────────────────────────────────────

/**
 * A stage is a bottleneck if either:
 * - Its success rate is more than 20 percentage points below the overall rate, or
 * - Its average duration is more than 2× the mean across all stages.
 */
function identifyBottlenecks(
  stageMetricsList: StageMetrics[],
  overallSuccessRate: number
): StageMetrics[] {
  if (stageMetricsList.length === 0) return [];

  const avgDuration = mean(stageMetricsList.map((s) => s.avgDurationMs));
  const durationThreshold = avgDuration * 2;

  return stageMetricsList.filter((s) => {
    const successRateDrop = overallSuccessRate - s.successRate;
    const isDurationOutlier = avgDuration > 0 && s.avgDurationMs > durationThreshold;
    return successRateDrop > 0.2 || isDurationOutlier;
  });
}

// ── Scoring ────────────────────────────────────────────────────────

interface ScoreBreakdown {
  score: number;
  lowSuccessRateDeduction: number;
  highRetryRateDeduction: number;
  worseningTrendDeduction: number;
  bottleneckDeduction: number;
}

function computeScore(
  overallSuccessRate: number,
  overallAvgRetries: number,
  hasWorseningTrend: boolean,
  hasBottleneck: boolean
): ScoreBreakdown {
  let score = 100;
  let lowSuccessRateDeduction = 0;
  let highRetryRateDeduction = 0;
  let worseningTrendDeduction = 0;
  let bottleneckDeduction = 0;

  if (overallSuccessRate < 0.7) {
    lowSuccessRateDeduction = 20;
    score -= lowSuccessRateDeduction;
  }

  if (overallAvgRetries > 0.5) {
    highRetryRateDeduction = 10;
    score -= highRetryRateDeduction;
  }

  if (hasWorseningTrend) {
    worseningTrendDeduction = 10;
    score -= worseningTrendDeduction;
  }

  if (hasBottleneck) {
    bottleneckDeduction = 10;
    score -= bottleneckDeduction;
  }

  return {
    score: clamp(score, 0, 100),
    lowSuccessRateDeduction,
    highRetryRateDeduction,
    worseningTrendDeduction,
    bottleneckDeduction,
  };
}

// ── Finding Builders ───────────────────────────────────────────────

function buildFailingStageFindings(stageMetricsList: StageMetrics[], startId: number): Finding[] {
  const findings: Finding[] = [];
  let id = startId;

  for (const s of stageMetricsList) {
    if (s.successRate < 0.7 && s.records.length >= 2) {
      const successPct = (s.successRate * 100).toFixed(1);
      const severity: Severity = s.successRate < 0.5 ? "high" : "medium";
      const confidence: Confidence = s.records.length >= 10 ? "high" : "medium";

      findings.push({
        id: `se-${id++}`,
        dimension: "stage-effectiveness",
        severity,
        title: `Low success rate in stage "${s.stage}"`,
        description: `Stage "${s.stage}" has a ${successPct}% success rate across ${s.records.length} executions, below the 70% threshold.`,
        impact: "Failed stages increase total pipeline cost via retries and may block PR delivery.",
        recommendation: `Investigate root causes of failures in "${s.stage}". Review error logs and consider increasing resource limits or prompt quality.`,
        evidence: {
          stage: s.stage,
          successRate: s.successRate,
          failureCount: s.records.filter((r) => !r.success).length,
          totalRuns: s.records.length,
          avgRetries: s.avgRetries,
        },
        confidence,
      });
    }
  }

  return findings;
}

function buildBottleneckFindings(
  bottlenecks: StageMetrics[],
  allStageMetrics: StageMetrics[],
  startId: number
): Finding[] {
  const findings: Finding[] = [];
  let id = startId;

  const avgDurationAcrossStages = mean(allStageMetrics.map((s) => s.avgDurationMs));

  for (const s of bottlenecks) {
    const confidence: Confidence = s.records.length >= 10 ? "high" : "medium";
    const isDurationBottleneck =
      avgDurationAcrossStages > 0 && s.avgDurationMs > avgDurationAcrossStages * 2;

    const reasons: string[] = [];
    if (s.successRate < 0.7) {
      reasons.push(`success rate ${(s.successRate * 100).toFixed(1)}% (below 70%)`);
    }
    if (isDurationBottleneck) {
      const avgSec = (avgDurationAcrossStages / 1000).toFixed(1);
      const stageSec = (s.avgDurationMs / 1000).toFixed(1);
      reasons.push(`duration ${stageSec}s vs avg ${avgSec}s across stages`);
    }

    findings.push({
      id: `se-${id++}`,
      dimension: "stage-effectiveness",
      severity: "high",
      title: `Bottleneck identified in stage "${s.stage}"`,
      description: `Stage "${s.stage}" is a pipeline bottleneck: ${reasons.join("; ")}.`,
      impact: "Bottleneck stages slow overall throughput and inflate total pipeline cost.",
      recommendation: `Profile stage "${s.stage}" for timeout issues, overly large context files, or inefficient prompting. Consider splitting into sub-tasks if scope is too broad.`,
      evidence: {
        stage: s.stage,
        successRate: s.successRate,
        avgDurationMs: s.avgDurationMs,
        avgDurationAcrossStagesMs: avgDurationAcrossStages,
        isDurationBottleneck,
        isSuccessBottleneck: s.successRate < 0.7,
        totalRuns: s.records.length,
      },
      confidence,
    });
  }

  return findings;
}

function buildHighRetryFinding(
  overallAvgRetries: number,
  stageMetricsList: StageMetrics[],
  id: number
): Finding | null {
  if (overallAvgRetries <= 0.5) return null;

  const highRetryStages = stageMetricsList
    .filter((s) => s.avgRetries > 0.5)
    .sort((a, b) => b.avgRetries - a.avgRetries);

  const totalRecords = stageMetricsList.reduce((n, s) => n + s.records.length, 0);
  const confidence: Confidence =
    totalRecords >= 20 ? "high" : totalRecords >= 10 ? "medium" : "low";

  return {
    id: `se-${id}`,
    dimension: "stage-effectiveness",
    severity: overallAvgRetries > 1.0 ? "high" : "medium",
    title: "High retry rate across pipeline stages",
    description: `The average retry count per execution is ${overallAvgRetries.toFixed(2)}, exceeding the 0.5 threshold. Stages with elevated retries: ${highRetryStages.map((s) => `"${s.stage}" (avg ${s.avgRetries.toFixed(2)})`).join(", ")}.`,
    impact: "High retry rates multiply token usage and cost per successful pipeline run.",
    recommendation:
      "Review stages with the highest retry rates for prompt clarity, context size, and tool reliability issues.",
    evidence: {
      overallAvgRetries,
      highRetryStages: highRetryStages.map((s) => ({
        stage: s.stage,
        avgRetries: s.avgRetries,
        runs: s.records.length,
      })),
      totalRecords,
    },
    confidence,
  };
}

function buildDurationDriftFinding(stageMetricsList: StageMetrics[], id: number): Finding | null {
  // Collect all records globally sorted by time, then compute a global duration time series
  const allSorted = stageMetricsList
    .flatMap((s) => s.records)
    .sort((a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime());

  if (allSorted.length < 5) return null;

  const globalDurationSeries = allSorted.map((r) => r.durationMs);
  const { direction, slope } = computeTrend(globalDurationSeries, 50); // 50ms/record threshold

  if (direction !== "degrading") return null;

  // Find which individual stages are also trending longer
  const driftingStages = stageMetricsList.filter((s) => {
    if (s.durationTimeSeries.length < 3) return false;
    const { direction: stageDir } = computeTrend(s.durationTimeSeries, 50);
    return stageDir === "degrading";
  });

  const totalRecords = allSorted.length;
  const confidence: Confidence =
    totalRecords >= 20 ? "high" : totalRecords >= 10 ? "medium" : "low";

  return {
    id: `se-${id}`,
    dimension: "stage-effectiveness",
    severity: "medium",
    title: "Pipeline stage durations trending longer over time",
    description: `Overall pipeline execution duration shows a degrading trend (slope: ${slope.toFixed(1)} ms/record). ${driftingStages.length > 0 ? `Stages drifting longer: ${driftingStages.map((s) => `"${s.stage}"`).join(", ")}.` : "The drift appears across multiple stages."}`,
    impact:
      "Increasing stage duration raises wall-clock delivery time and may correlate with growing context file sizes or model degradation.",
    recommendation:
      "Audit context handoff file sizes and review whether accumulated history is inflating stage input. Consider pruning or summarizing context files.",
    evidence: {
      trendSlope: slope,
      driftingStages: driftingStages.map((s) => s.stage),
      totalRecords,
      oldestTimestamp: allSorted[0].timestamp,
      latestTimestamp: allSorted[allSorted.length - 1].timestamp,
    },
    confidence,
  };
}

// ── Main Analyzer ──────────────────────────────────────────────────

export function analyzeStageEffectiveness(
  dataset: HealthAnalysisInput,
  config: HealthAnalysisConfig,
  baseline?: HealthAnalysisInput
): DimensionResult {
  const records = dataset.executionHistory;
  const sampleSize = records.length;
  const enoughData = hasEnoughData(sampleSize, config.minimumSampleSizes.basic);

  // Early return when there is not enough data to form meaningful findings
  if (!enoughData) {
    return {
      dimension: "stage-effectiveness",
      score: 100,
      status: getHealthStatus(100),
      findings: [],
      metrics: { sampleSize },
      hasEnoughData: false,
      sampleSize,
    };
  }

  // ── Per-stage breakdown ──────────────────────────────────────────
  const grouped = groupByStage(records);
  const stageMetricsList: StageMetrics[] = Array.from(grouped.entries()).map(([stage, recs]) =>
    computeStageMetrics(stage, recs)
  );

  // ── Overall metrics ──────────────────────────────────────────────
  const overallSuccessRate =
    records.length > 0 ? records.filter((r) => r.success).length / records.length : 0;

  const overallFirstAttemptPassRate =
    records.length > 0
      ? records.filter((r) => r.retries === 0 && r.success).length / records.length
      : 0;

  const overallAvgRetries = mean(records.map((r) => r.retries));

  // ── Duration trend across all records ───────────────────────────
  const globalSorted = sortByTimestamp(records);
  const globalDurationSeries = globalSorted.map((r) => r.durationMs);
  const { direction: durationTrendDirection } = computeTrend(globalDurationSeries, 50);
  const hasWorseningTrend = durationTrendDirection === "degrading";

  // ── Bottleneck identification ────────────────────────────────────
  const bottlenecks = identifyBottlenecks(stageMetricsList, overallSuccessRate);
  const hasBottleneck = bottlenecks.length > 0;

  // ── Score ────────────────────────────────────────────────────────
  const breakdown = computeScore(
    overallSuccessRate,
    overallAvgRetries,
    hasWorseningTrend,
    hasBottleneck
  );

  // ── Findings ─────────────────────────────────────────────────────
  const findings: Finding[] = [];
  let nextId = 1;

  // 1. Failing stages
  const failingFindings = buildFailingStageFindings(stageMetricsList, nextId);
  findings.push(...failingFindings);
  nextId += failingFindings.length;

  // 2. Bottleneck stages (only those not already covered by failing-stage findings)
  const failingStageNames = new Set(failingFindings.map((f) => f.evidence["stage"] as string));
  const uniqueBottlenecks = bottlenecks.filter((b) => !failingStageNames.has(b.stage));
  const bottleneckFindings = buildBottleneckFindings(uniqueBottlenecks, stageMetricsList, nextId);
  findings.push(...bottleneckFindings);
  nextId += bottleneckFindings.length;

  // 3. High retry rates
  const retryFinding = buildHighRetryFinding(overallAvgRetries, stageMetricsList, nextId);
  if (retryFinding) {
    findings.push(retryFinding);
    nextId++;
  }

  // 4. Duration drift
  const driftFinding = buildDurationDriftFinding(stageMetricsList, nextId);
  if (driftFinding) {
    findings.push(driftFinding);
  }

  // ── Period Comparison ────────────────────────────────────────────
  let periodComparison: DimensionResult["periodComparison"];
  if (
    baseline &&
    hasEnoughData(baseline.executionHistory.length, config.minimumSampleSizes.basic)
  ) {
    const baselineSuccessRate =
      baseline.executionHistory.length > 0
        ? baseline.executionHistory.filter((r) => r.success).length /
          baseline.executionHistory.length
        : 0;

    periodComparison = buildPeriodComparison(
      overallSuccessRate,
      baselineSuccessRate,
      sampleSize,
      false, // lowerIsBetter = false — higher success rate is better
      config.confidenceThreshold
    );
  }

  // ── Metrics ──────────────────────────────────────────────────────
  const stageCount = stageMetricsList.length;
  const perStageSucessRates = Object.fromEntries(
    stageMetricsList.map((s) => [`successRate_${s.stage}`, s.successRate])
  );
  const perStageAvgRetries = Object.fromEntries(
    stageMetricsList.map((s) => [`avgRetries_${s.stage}`, s.avgRetries])
  );
  const perStageAvgDuration = Object.fromEntries(
    stageMetricsList.map((s) => [`avgDurationMs_${s.stage}`, s.avgDurationMs])
  );

  const metrics: Record<string, number> = {
    sampleSize,
    stageCount,
    overallSuccessRate,
    overallFirstAttemptPassRate,
    overallAvgRetries,
    bottleneckCount: bottlenecks.length,
    scoreDeductionLowSuccess: breakdown.lowSuccessRateDeduction,
    scoreDeductionHighRetries: breakdown.highRetryRateDeduction,
    scoreDeductionWorseningTrend: breakdown.worseningTrendDeduction,
    scoreDeductionBottleneck: breakdown.bottleneckDeduction,
    ...perStageSucessRates,
    ...perStageAvgRetries,
    ...perStageAvgDuration,
  };

  return {
    dimension: "stage-effectiveness",
    score: breakdown.score,
    status: getHealthStatus(breakdown.score),
    findings,
    metrics,
    hasEnoughData: true,
    sampleSize,
    periodComparison,
  };
}
