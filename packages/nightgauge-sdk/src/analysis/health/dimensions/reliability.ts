/**
 * Reliability Dimension Analyzer
 *
 * Evaluates pipeline reliability including failure rates, MTBF calculation,
 * auto-recovery success, and failure trend analysis.
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
import { FAILURE_CATEGORY_WEIGHTS, classifyFailureCategory } from "../failureClassifier.js";
import { computeTrend, mean, clamp, hasEnoughData, buildPeriodComparison } from "../statistics.js";

// ── Internal helpers ──────────────────────────────────────────────

/** Group execution history records by ISO week bucket (YYYY-WNN). */
function getIsoWeek(isoTimestamp: string): string {
  const date = new Date(isoTimestamp);
  // Use UTC to be consistent across environments
  const dayOfWeek = date.getUTCDay(); // 0 = Sunday
  // Move to nearest Monday (ISO week starts Monday)
  const monday = new Date(date);
  monday.setUTCDate(date.getUTCDate() - ((dayOfWeek + 6) % 7));
  const year = monday.getUTCFullYear();
  const startOfYear = new Date(Date.UTC(year, 0, 1));
  const weekNumber = Math.ceil(((monday.getTime() - startOfYear.getTime()) / 86_400_000 + 1) / 7);
  return `${year}-W${String(weekNumber).padStart(2, "0")}`;
}

/**
 * Compute weekly failure rates in chronological order.
 *
 * Returns an array of failure rate values (0–1), one per week bucket,
 * sorted from earliest to latest week.
 */
function computeWeeklyFailureRates(records: HealthAnalysisInput["executionHistory"]): number[] {
  const weekBuckets = new Map<string, { total: number; failures: number }>();

  for (const record of records) {
    const week = getIsoWeek(record.timestamp);
    const existing = weekBuckets.get(week) ?? { total: 0, failures: 0 };
    existing.total += 1;
    if (!record.success) existing.failures += 1;
    weekBuckets.set(week, existing);
  }

  return [...weekBuckets.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([, bucket]) => bucket.failures / bucket.total);
}

/**
 * Compute Mean Time Between Failures (MTBF) in hours.
 *
 * Returns `undefined` when fewer than 2 failure records exist (not enough
 * data to measure a gap between consecutive failures).
 */
function computeMtbfHours(
  failedRecords: HealthAnalysisInput["executionHistory"]
): number | undefined {
  if (failedRecords.length < 2) return undefined;

  const sorted = [...failedRecords].sort(
    (a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime()
  );

  const gapsHours: number[] = [];
  for (let i = 1; i < sorted.length; i++) {
    const prev = new Date(sorted[i - 1].timestamp).getTime();
    const curr = new Date(sorted[i].timestamp).getTime();
    gapsHours.push((curr - prev) / 3_600_000);
  }

  return mean(gapsHours);
}

// ── Per-stage failure analysis ────────────────────────────────────

interface StageFailureStats {
  stage: string;
  total: number;
  failures: number;
  failureRate: number;
}

function computeStageFailureStats(
  records: HealthAnalysisInput["executionHistory"]
): StageFailureStats[] {
  const stageMap = new Map<string, { total: number; failures: number }>();

  for (const record of records) {
    const existing = stageMap.get(record.stage) ?? { total: 0, failures: 0 };
    existing.total += 1;
    if (!record.success) existing.failures += 1;
    stageMap.set(record.stage, existing);
  }

  return [...stageMap.entries()].map(([stage, stats]) => ({
    stage,
    total: stats.total,
    failures: stats.failures,
    failureRate: stats.failures / stats.total,
  }));
}

// ── Finding builders ──────────────────────────────────────────────

function buildHighFailureRateFinding(
  findingIndex: number,
  failureRate: number,
  sampleSize: number
): Finding {
  const severity: Severity = failureRate >= 0.5 ? "critical" : "high";
  const confidence: Confidence = sampleSize >= 20 ? "high" : sampleSize >= 10 ? "medium" : "low";

  return {
    id: `rel-${findingIndex}`,
    dimension: "reliability",
    severity,
    title: "High Pipeline Failure Rate",
    description: `${(failureRate * 100).toFixed(1)}% weighted failure rate detected (infrastructure failures counted at 5%, agent failures at 50%, organic at 100%), which is above the acceptable threshold.`,
    impact:
      "Frequent failures increase developer wait time, consume unnecessary compute budget, and erode confidence in the automated pipeline.",
    recommendation:
      "Investigate the most-failing stages first. Review recent error logs, check for flaky test infrastructure, and consider adding retry logic or pre-flight checks.",
    evidence: {
      failureRate,
      failurePercent: failureRate * 100,
      sampleSize,
    },
    confidence,
  };
}

function buildWorseningTrendFinding(
  findingIndex: number,
  slope: number,
  weekCount: number
): Finding {
  return {
    id: `rel-${findingIndex}`,
    dimension: "reliability",
    severity: "high",
    title: "Failure Rate Is Worsening Over Time",
    description: `Linear trend analysis over ${weekCount} weeks shows an increasing failure rate (slope: ${slope.toFixed(4)} per week).`,
    impact:
      "A rising failure trend suggests an unaddressed regression or accumulating technical debt that will compound if left unchecked.",
    recommendation:
      "Review recent commits and configuration changes that correlate with the onset of increased failures. Prioritize stabilization over new feature work.",
    evidence: {
      trendSlope: slope,
      weekCount,
    },
    confidence: weekCount >= 4 ? "medium" : "low",
  };
}

function buildLowMtbfFinding(
  findingIndex: number,
  mtbfHours: number,
  failureCount: number
): Finding {
  const severity: Severity = mtbfHours < 1 ? "critical" : mtbfHours < 6 ? "high" : "medium";

  return {
    id: `rel-${findingIndex}`,
    dimension: "reliability",
    severity,
    title: "Low Mean Time Between Failures (MTBF)",
    description: `Failures are occurring approximately every ${mtbfHours.toFixed(1)} hours on average across ${failureCount} recorded failures.`,
    impact:
      "A low MTBF means the pipeline spends a disproportionate amount of time recovering from failures rather than delivering value.",
    recommendation:
      "Focus on the most frequently failing stages. Adding circuit-breaker logic, improving input validation, or increasing retry budgets may extend MTBF.",
    evidence: {
      mtbfHours,
      failureCount,
    },
    confidence: failureCount >= 10 ? "high" : failureCount >= 5 ? "medium" : "low",
  };
}

function buildHighStageFailureFinding(
  findingIndex: number,
  stageStats: StageFailureStats[]
): Finding {
  const stageList = stageStats
    .map((s) => `${s.stage} (${(s.failureRate * 100).toFixed(1)}%)`)
    .join(", ");

  const worst = stageStats.reduce((a, b) => (a.failureRate > b.failureRate ? a : b));
  const severity: Severity = worst.failureRate >= 0.5 ? "critical" : "high";

  return {
    id: `rel-${findingIndex}`,
    dimension: "reliability",
    severity,
    title: "High Failure Concentration in Pipeline Stages",
    description: `One or more pipeline stages have failure rates exceeding 30%: ${stageList}.`,
    impact:
      "Stage-concentrated failures indicate specific points of fragility that can block the entire pipeline and inflate overall failure metrics.",
    recommendation:
      "Audit the highest-failure stage for error patterns, missing validation, or external dependency instability. Consider isolated retry policies per stage.",
    evidence: {
      affectedStages: stageStats.map((s) => ({
        stage: s.stage,
        failureRate: s.failureRate,
        failures: s.failures,
        total: s.total,
      })),
    },
    confidence: worst.total >= 10 ? "high" : worst.total >= 5 ? "medium" : "low",
  };
}

// ── Main analyzer ─────────────────────────────────────────────────

export function analyzeReliability(
  dataset: HealthAnalysisInput,
  config: HealthAnalysisConfig,
  baseline?: HealthAnalysisInput
): DimensionResult {
  const records = dataset.executionHistory;
  const sampleSize = records.length;
  const dataEnough = hasEnoughData(sampleSize, config.minimumSampleSizes.basic);

  // ── Edge case: no records ────────────────────────────────────────
  if (sampleSize === 0) {
    return {
      dimension: "reliability",
      score: 100,
      status: getHealthStatus(100),
      findings: [],
      metrics: {
        failureRate: 0,
        successRate: 1,
        autoRecoveryRate: 0,
        failureCount: 0,
        sampleSize: 0,
      },
      hasEnoughData: false,
      sampleSize: 0,
    };
  }

  // ── Core metrics ─────────────────────────────────────────────────
  const failedRecords = records.filter((r) => !r.success);
  const failureCount = failedRecords.length;
  const failureRate = failureCount / sampleSize;
  const successRate = 1 - failureRate;

  // Weighted failure rate — infrastructure failures count 5%, agent 50%, organic 100%
  const weightedFailureCount = failedRecords.reduce((sum, r) => {
    const category = r.failure_category ?? classifyFailureCategory(undefined, r.stage);
    return sum + FAILURE_CATEGORY_WEIGHTS[category];
  }, 0);
  const weightedFailureRate = weightedFailureCount / sampleSize;

  // Auto-recovery: records that retried AND succeeded
  const retriedRecords = records.filter((r) => r.retries > 0);
  const autoRecoveredRecords = retriedRecords.filter((r) => r.success);
  const autoRecoveryRate =
    retriedRecords.length > 0 ? autoRecoveredRecords.length / retriedRecords.length : 0;

  // MTBF
  const mtbfHours = computeMtbfHours(failedRecords);

  // Weekly failure trend
  const weeklyRates = computeWeeklyFailureRates(records);
  const trend = computeTrend(weeklyRates);

  // Per-stage failure stats
  const stageStats = computeStageFailureStats(records);
  const highFailureStages = stageStats.filter((s) => s.failureRate > 0.3);

  // ── Scoring ───────────────────────────────────────────────────────
  let score = (1 - weightedFailureRate) * 100;

  // Bonus: good auto-recovery
  if (autoRecoveryRate > 0.5) {
    score += 10;
  }

  // Penalty: worsening trend
  if (trend.direction === "degrading") {
    score -= 10;
  }

  // Penalty: high-failure stages (up to -15)
  const stageDeduction = Math.min(highFailureStages.length * 5, 15);
  score -= stageDeduction;

  score = clamp(score, 0, 100);

  // ── Findings ──────────────────────────────────────────────────────
  const findings: Finding[] = [];
  let findingIndex = 1;

  // 1. High overall failure rate (threshold: > 20%, using weighted rate)
  if (weightedFailureRate > 0.2) {
    findings.push(buildHighFailureRateFinding(findingIndex++, weightedFailureRate, sampleSize));
  }

  // 2. Worsening trend
  if (trend.direction === "degrading" && weeklyRates.length >= 2) {
    findings.push(buildWorseningTrendFinding(findingIndex++, trend.slope, weeklyRates.length));
  }

  // 3. Low MTBF — report when MTBF is defined and below 24 hours
  if (mtbfHours !== undefined && mtbfHours < 24) {
    findings.push(buildLowMtbfFinding(findingIndex++, mtbfHours, failureCount));
  }

  // 4. High per-stage failure concentration
  if (highFailureStages.length > 0) {
    findings.push(buildHighStageFailureFinding(findingIndex, highFailureStages));
  }

  // ── Period comparison ─────────────────────────────────────────────
  let periodComparison: DimensionResult["periodComparison"];
  if (baseline && baseline.executionHistory.length > 0) {
    const baselineRecords = baseline.executionHistory;
    const baselineFailureRate =
      baselineRecords.filter((r) => !r.success).length / baselineRecords.length;

    periodComparison = buildPeriodComparison(
      failureRate,
      baselineFailureRate,
      Math.min(sampleSize, baselineRecords.length),
      /* lowerIsBetter */ true,
      config.confidenceThreshold
    );
  }

  // ── Metrics record ────────────────────────────────────────────────
  const metrics: Record<string, number> = {
    failureRate,
    successRate,
    failureCount,
    weightedFailureRate,
    weightedFailureCount,
    autoRecoveryRate,
    retriedCount: retriedRecords.length,
    autoRecoveredCount: autoRecoveredRecords.length,
    weeklyRateCount: weeklyRates.length,
    trendSlope: trend.slope,
    highFailureStageCount: highFailureStages.length,
    sampleSize,
  };

  if (mtbfHours !== undefined) {
    metrics["mtbfHours"] = mtbfHours;
  }

  return {
    dimension: "reliability",
    score,
    status: getHealthStatus(score),
    findings,
    metrics,
    hasEnoughData: dataEnough,
    sampleSize,
    periodComparison,
  };
}
