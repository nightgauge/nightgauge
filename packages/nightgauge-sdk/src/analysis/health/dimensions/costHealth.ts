/**
 * Cost Health Dimension Analyzer
 *
 * Evaluates pipeline cost efficiency including per-run trends,
 * anomaly detection, and cost distribution across stages.
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
  standardDeviation,
  clamp,
  hasEnoughData,
  buildPeriodComparison,
} from "../statistics.js";

// ── Internal helpers ───────────────────────────────────────────────

/** Partition records into local (zero-cost inference) vs cloud runs. */
function partitionByProvider(records: HealthAnalysisInput["executionHistory"]): {
  cloud: typeof records;
  local: typeof records;
} {
  const cloud = records.filter((r) => !r.isLocalModel);
  const local = records.filter((r) => r.isLocalModel);
  return { cloud, local };
}

/** Sum costUsd for all records sharing the same issueNumber. */
function groupCostByRun(records: HealthAnalysisInput["executionHistory"]): number[] {
  const runTotals = new Map<number, number>();
  for (const r of records) {
    runTotals.set(r.issueNumber, (runTotals.get(r.issueNumber) ?? 0) + r.costUsd);
  }
  return Array.from(runTotals.values());
}

/** Compute the share of total cost consumed by each stage. */
function stageCostShares(records: HealthAnalysisInput["executionHistory"]): Map<string, number> {
  const stageTotals = new Map<string, number>();
  let grandTotal = 0;
  for (const r of records) {
    stageTotals.set(r.stage, (stageTotals.get(r.stage) ?? 0) + r.costUsd);
    grandTotal += r.costUsd;
  }
  const shares = new Map<string, number>();
  if (grandTotal === 0) return shares;
  for (const [stage, total] of stageTotals) {
    shares.set(stage, total / grandTotal);
  }
  return shares;
}

/** Identify the issueNumbers whose per-run cost exceeds mean + 2*stdDev. */
function detectAnomalousRuns(records: HealthAnalysisInput["executionHistory"]): {
  anomalyCount: number;
  anomalyRate: number;
  threshold: number;
} {
  const runTotals = new Map<number, number>();
  for (const r of records) {
    runTotals.set(r.issueNumber, (runTotals.get(r.issueNumber) ?? 0) + r.costUsd);
  }
  const costs = Array.from(runTotals.values());
  if (costs.length < 2) return { anomalyCount: 0, anomalyRate: 0, threshold: 0 };

  const avg = mean(costs);
  const sd = standardDeviation(costs);
  const threshold = avg + 2 * sd;
  const anomalyCount = costs.filter((c) => c > threshold).length;
  return { anomalyCount, anomalyRate: anomalyCount / costs.length, threshold };
}

/** Compute per-stage unique run counts and XL presence flag. */
function stageRunMetrics(records: HealthAnalysisInput["executionHistory"]): {
  runCounts: Map<string, number>;
  hasXL: Map<string, boolean>;
} {
  const runCounts = new Map<string, number>();
  const hasXL = new Map<string, boolean>();
  const runSets = new Map<string, Set<number>>();

  for (const r of records) {
    if (!runSets.has(r.stage)) runSets.set(r.stage, new Set());
    runSets.get(r.stage)!.add(r.issueNumber);
    // XL = complexityScore >= 8 (Fibonacci: XS=1, S=2, M=3, L=5, XL=8)
    if ((r.complexityScore ?? 0) >= 8) {
      hasXL.set(r.stage, true);
    } else if (!hasXL.has(r.stage)) {
      hasXL.set(r.stage, false);
    }
  }
  for (const [stage, runSet] of runSets) {
    runCounts.set(stage, runSet.size);
  }
  return { runCounts, hasXL };
}

/** Cost per successful run vs overall average cost per run. */
function costEfficiencyRatio(records: HealthAnalysisInput["executionHistory"]): number {
  // Build per-run success flag: a run is successful if every stage succeeded.
  const runSuccess = new Map<number, boolean>();
  const runCost = new Map<number, number>();
  for (const r of records) {
    runCost.set(r.issueNumber, (runCost.get(r.issueNumber) ?? 0) + r.costUsd);
    // Once a stage fails, the run is unsuccessful.
    if (runSuccess.get(r.issueNumber) !== false) {
      runSuccess.set(r.issueNumber, r.success);
    }
  }
  const allCosts = Array.from(runCost.values());
  const successCosts = Array.from(runSuccess.entries())
    .filter(([, ok]) => ok)
    .map(([issue]) => runCost.get(issue) ?? 0);

  const avgOverall = mean(allCosts);
  const avgSuccess = mean(successCosts);

  if (avgOverall === 0) return 1;
  // Ratio > 1 means successful runs cost MORE than average (unfavorable).
  return avgSuccess / avgOverall;
}

// ── Main analyzer ──────────────────────────────────────────────────

export function analyzeCostHealth(
  dataset: HealthAnalysisInput,
  config: HealthAnalysisConfig,
  baseline?: HealthAnalysisInput
): DimensionResult {
  const records = dataset.executionHistory;
  const minimumSamples = config.minimumSampleSizes.basic;

  // Partition local vs cloud records to prevent zero-cost LM Studio runs from
  // distorting cloud cost statistics (Issue #2055).
  const { cloud: cloudRecords, local: localRecords } = partitionByProvider(records);
  const hasMixedProviders = localRecords.length > 0 && cloudRecords.length > 0;

  // Use cloud-only records for statistics when both types are present;
  // use all records when all are the same type (avoids silent data loss).
  const statsRecords = hasMixedProviders ? cloudRecords : records;

  // Group costs by run (issueNumber) for run-level analysis.
  const perRunCosts = groupCostByRun(statsRecords);
  const sampleSize = perRunCosts.length;
  const enoughData = hasEnoughData(sampleSize, minimumSamples);

  // ── Short-circuit when data is insufficient ────────────────────
  if (!enoughData) {
    return {
      dimension: "cost-health",
      score: 50,
      status: getHealthStatus(50),
      findings: [],
      metrics: { sampleSize },
      hasEnoughData: false,
      sampleSize,
    };
  }

  // ── Core statistics ────────────────────────────────────────────
  const avgCostPerRun = mean(perRunCosts);
  const medianCostPerRun = computePercentile(perRunCosts, 50);
  const p95CostPerRun = computePercentile(perRunCosts, 95);
  const p99CostPerRun = computePercentile(perRunCosts, 99);
  const sdCostPerRun = standardDeviation(perRunCosts);
  const coefficientOfVariation = avgCostPerRun > 0 ? sdCostPerRun / avgCostPerRun : 0;

  // Chronological trend uses per-run costs ordered by first stage timestamp.
  // Build a map of issueNumber → earliest timestamp to get chronological order.
  const runEarliestTs = new Map<number, number>();
  for (const r of statsRecords) {
    const ts = new Date(r.timestamp).getTime();
    const current = runEarliestTs.get(r.issueNumber);
    if (current === undefined || ts < current) {
      runEarliestTs.set(r.issueNumber, ts);
    }
  }
  const chronologicalRunCosts = Array.from(runEarliestTs.entries())
    .sort(([, tsA], [, tsB]) => tsA - tsB)
    .map(([issue]) => {
      // Re-sum cost for this issue from statsRecords.
      return statsRecords
        .filter((r) => r.issueNumber === issue)
        .reduce((sum, r) => sum + r.costUsd, 0);
    });

  const trend = computeTrend(chronologicalRunCosts);

  // Stage distribution.
  const shares = stageCostShares(statsRecords);
  const maxStageShare = shares.size > 0 ? Math.max(...shares.values()) : 0;
  const dominantStage =
    Array.from(shares.entries()).sort(([, a], [, b]) => b - a)[0]?.[0] ?? "unknown";

  // Anomaly detection.
  const {
    anomalyCount,
    anomalyRate,
    threshold: anomalyThreshold,
  } = detectAnomalousRuns(statsRecords);

  // Cost efficiency ratio.
  const efficiencyRatio = costEfficiencyRatio(statsRecords);

  // ── Scoring ────────────────────────────────────────────────────
  let score = 100;

  // Worsening cost trend.
  if (trend.direction === "degrading") score -= 15;

  // High anomaly rate (>10% of runs are outliers).
  if (anomalyRate > 0.1) score -= 15;

  // High cost per success relative to average (successful runs cost >30% more than average).
  if (efficiencyRatio > 1.3) score -= 10;

  // High cost variance (CV > 0.5 signals unpredictable cost).
  if (coefficientOfVariation > 0.5) score -= 10;

  score = clamp(score, 0, 100);

  // ── Findings ───────────────────────────────────────────────────
  const findings: Finding[] = [];
  let findingIndex = 1;

  // Finding: cost anomalies detected.
  if (anomalyCount > 0) {
    const severity: Severity = anomalyRate > 0.2 ? "high" : "medium";
    const confidence: Confidence =
      sampleSize >= config.minimumSampleSizes.significance
        ? "high"
        : sampleSize >= config.minimumSampleSizes.trend
          ? "medium"
          : "low";

    findings.push({
      id: `ch-${findingIndex++}`,
      dimension: "cost-health",
      severity,
      title: "Cost Anomalies Detected",
      description: `${anomalyCount} of ${sampleSize} pipeline run(s) exceeded the anomaly threshold (mean + 2σ = $${anomalyThreshold.toFixed(4)}).`,
      impact: `Anomalous runs inflate average cost and may indicate runaway retries, oversized context, or model mis-routing.`,
      recommendation:
        "Inspect high-cost runs for excessive retries or unexpectedly large stages. Consider model routing adjustments or context-window limits.",
      evidence: {
        anomalyCount,
        anomalyRate: parseFloat((anomalyRate * 100).toFixed(1)),
        threshold: anomalyThreshold,
        avgCostPerRun,
        sdCostPerRun,
      },
      confidence,
    });
  }

  // Finding: worsening cost trend.
  if (trend.direction === "degrading") {
    const confidence: Confidence = sampleSize >= config.minimumSampleSizes.trend ? "medium" : "low";
    findings.push({
      id: `ch-${findingIndex++}`,
      dimension: "cost-health",
      severity: "high",
      title: "Cost Trend Worsening",
      description: `Per-run cost shows a degrading trend (slope: ${trend.slope.toFixed(4)} USD/run).`,
      impact:
        "Increasing costs over time compound quickly; unchecked growth can exhaust budget allocations.",
      recommendation:
        "Review recent configuration or complexity changes that may be driving up token usage. Consider enabling caching or routing more stages to lighter models.",
      evidence: {
        slope: trend.slope,
        direction: trend.direction,
        recentRunCount: sampleSize,
        avgCostPerRun,
        p95CostPerRun,
      },
      confidence,
    });
  }

  // Finding: mixed provider fleet (informational).
  if (hasMixedProviders) {
    findings.push({
      id: `ch-${findingIndex++}`,
      dimension: "cost-health",
      severity: "low",
      title: "Mixed Provider Fleet Detected",
      description: `${localRecords.length} local-model run(s) (LM Studio) detected alongside ${cloudRecords.length} cloud-billed run(s). Cost statistics exclude local runs.`,
      impact:
        "Local runs have no monetary cost; mixing with cloud runs would distort trend and variance metrics.",
      recommendation:
        "Review the ratio of local vs. cloud model usage to ensure model routing aligns with cost policies.",
      evidence: {
        localRunCount: localRecords.length,
        cloudRunCount: cloudRecords.length,
      },
      confidence: "high",
    });
  }

  // Finding: dominant stage cost concentration (>60% of total cost in one stage).
  if (maxStageShare > 0.6) {
    const { runCounts, hasXL } = stageRunMetrics(statsRecords);
    findings.push({
      id: `ch-${findingIndex}`,
      dimension: "cost-health",
      severity: "medium",
      title: "High Stage Cost Concentration",
      description: `Stage "${dominantStage}" accounts for ${(maxStageShare * 100).toFixed(1)}% of total pipeline spend.`,
      impact:
        "Concentrated spend in a single stage creates a bottleneck and amplifies the cost impact of failures in that stage.",
      recommendation: `Profile the "${dominantStage}" stage for token waste, redundant reads, or unnecessary model upgrades. Consider caching or a lighter model for this stage.`,
      evidence: {
        dominantStage,
        stageShare: parseFloat((maxStageShare * 100).toFixed(1)),
        allStageShares: Object.fromEntries(
          Array.from(shares.entries()).map(([s, sh]) => [s, parseFloat((sh * 100).toFixed(1))])
        ),
        stageRunCounts: Object.fromEntries(runCounts),
        stageHasXL: Object.fromEntries(hasXL),
      },
      confidence: "high",
    });
  }

  // ── Period comparison (baseline) ───────────────────────────────
  let periodComparison;
  if (baseline !== undefined) {
    const baselinePerRunCosts = groupCostByRun(baseline.executionHistory);
    const baselineAvg = mean(baselinePerRunCosts);
    periodComparison = buildPeriodComparison(
      avgCostPerRun,
      baselineAvg,
      sampleSize,
      /* lowerIsBetter */ true,
      config.confidenceThreshold
    );
  }

  // ── Assemble result ────────────────────────────────────────────
  return {
    dimension: "cost-health",
    score,
    status: getHealthStatus(score),
    findings,
    metrics: {
      avgCostPerRun,
      medianCostPerRun,
      p95CostPerRun,
      p99CostPerRun,
      sdCostPerRun,
      coefficientOfVariation,
      trendSlope: trend.slope,
      anomalyCount,
      anomalyRate,
      anomalyThreshold,
      maxStageShare,
      efficiencyRatio,
      sampleSize,
      localRunCount: localRecords.length,
      cloudRunCount: cloudRecords.length,
      hasMixedProviders: hasMixedProviders ? 1 : 0,
    },
    hasEnoughData: true,
    sampleSize,
    ...(periodComparison !== undefined ? { periodComparison } : {}),
  };
}
