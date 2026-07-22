/**
 * Model Routing Dimension Analyzer
 *
 * Evaluates model selection effectiveness including auto-selection accuracy,
 * under/over-routing detection, and per-model cost efficiency.
 *
 * @see Issue #1101 - Multi-Dimensional Health Analysis Engine
 */

import type { ExecutionHistoryRecord } from "../../types.js";
import type {
  HealthAnalysisInput,
  HealthAnalysisConfig,
  DimensionResult,
  Finding,
  Severity,
  Confidence,
} from "../types.js";
import { getHealthStatus } from "../types.js";
import { mean, clamp, hasEnoughData, buildPeriodComparison } from "../statistics.js";

// ── Internal helpers ──────────────────────────────────────────────

interface ModelStats {
  model: string;
  total: number;
  successes: number;
  totalCostUsd: number;
}

function modelKey(record: ExecutionHistoryRecord): string {
  return (record.model ?? "unknown").toLowerCase();
}

function isAutoSelected(record: ExecutionHistoryRecord): boolean {
  return record.selectionSource === "auto";
}

function isLightweightModel(model: string): boolean {
  return model.includes("haiku") || model.includes("sonnet");
}

function isHeavyweightModel(model: string): boolean {
  return model.includes("opus");
}

function isHighComplexity(complexity: string | undefined): boolean {
  return complexity === "L" || complexity === "XL";
}

function isLowComplexity(complexity: string | undefined): boolean {
  return complexity === "XS" || complexity === "S";
}

function computeAutoSelectionSuccessRate(records: ExecutionHistoryRecord[]): number {
  const autoRecords = records.filter(isAutoSelected);
  if (autoRecords.length === 0) return 1; // No auto-selection data → treat as neutral
  return mean(autoRecords.map((r) => (r.success ? 1 : 0)));
}

function buildModelStatsMap(records: ExecutionHistoryRecord[]): Map<string, ModelStats> {
  const map = new Map<string, ModelStats>();

  for (const record of records) {
    const key = modelKey(record);
    let stats = map.get(key);
    if (!stats) {
      stats = { model: key, total: 0, successes: 0, totalCostUsd: 0 };
      map.set(key, stats);
    }
    stats.total++;
    if (record.success) stats.successes++;
    stats.totalCostUsd += record.costUsd;
  }

  return map;
}

function detectUnderRouting(records: ExecutionHistoryRecord[]): ExecutionHistoryRecord[] {
  return records.filter((r) => {
    const model = modelKey(r);
    return (
      isAutoSelected(r) &&
      isLightweightModel(model) &&
      isHighComplexity(r.autoSelectorComplexity) &&
      r.success === false
    );
  });
}

function detectOverRouting(records: ExecutionHistoryRecord[]): ExecutionHistoryRecord[] {
  return records.filter((r) => {
    const model = modelKey(r);
    return (
      isAutoSelected(r) &&
      isHeavyweightModel(model) &&
      isLowComplexity(r.autoSelectorComplexity) &&
      r.success === true &&
      r.retries === 0
    );
  });
}

// ── Main export ───────────────────────────────────────────────────

export function analyzeModelRouting(
  dataset: HealthAnalysisInput,
  config: HealthAnalysisConfig,
  baseline?: HealthAnalysisInput
): DimensionResult {
  const records = dataset.executionHistory;
  const sampleSize = records.length;
  const enoughData = hasEnoughData(sampleSize, config.minimumSampleSizes.basic);

  const findings: Finding[] = [];
  const metrics: Record<string, number> = {};
  let findingIndex = 1;

  if (!enoughData) {
    return {
      dimension: "model-routing",
      score: 50,
      status: getHealthStatus(50),
      findings: [],
      metrics: { sampleSize },
      hasEnoughData: false,
      sampleSize,
    };
  }

  // ── Auto-selection accuracy ────────────────────────────────────

  const autoRecords = records.filter(isAutoSelected);
  const autoTotal = autoRecords.length;
  const autoSuccessRate = computeAutoSelectionSuccessRate(records);
  const autoFailureRate = 1 - autoSuccessRate;

  metrics["autoSelectionTotal"] = autoTotal;
  metrics["autoSelectionSuccessRate"] = autoSuccessRate;
  metrics["autoSelectionFailureRate"] = autoFailureRate;

  // ── Per-model success rates and cost effectiveness ─────────────

  const modelStatsMap = buildModelStatsMap(records);
  const modelNames = [...modelStatsMap.keys()];
  const modelCount = modelNames.length;

  metrics["distinctModelCount"] = modelCount;

  const underperformingModels: string[] = [];
  const costInefficientModels: Array<{
    model: string;
    effectiveCostPerSuccess: number;
    successRate: number;
    sampleSize: number;
  }> = [];

  for (const [model, stats] of modelStatsMap) {
    const successRate = stats.total > 0 ? stats.successes / stats.total : 0;
    const effectiveCostPerSuccess =
      stats.successes > 0 ? stats.totalCostUsd / stats.successes : stats.totalCostUsd;

    metrics[`model.${model}.successRate`] = successRate;
    metrics[`model.${model}.effectiveCostPerSuccess`] = effectiveCostPerSuccess;
    metrics[`model.${model}.sampleSize`] = stats.total;

    if (successRate < 0.5 && stats.total >= 3) {
      underperformingModels.push(model);
    }

    // Flag models where effective cost per success is notably high (> 2x average)
    if (stats.total >= 3) {
      costInefficientModels.push({
        model,
        effectiveCostPerSuccess,
        successRate,
        sampleSize: stats.total,
      });
    }
  }

  // Detect cost-inefficient models relative to the mean effective cost.
  // Only include paid (nonzero-cost) models in the fleet mean computation —
  // zero-cost models (LM Studio / local inference) would drag the mean toward
  // zero and cause all cloud models to appear cost-inefficient. (Issue #2055)
  const localModelCount = costInefficientModels.filter(
    (m) => m.effectiveCostPerSuccess === 0
  ).length;
  metrics["localModelCount"] = localModelCount;

  const paidCostModels = costInefficientModels.filter((m) => m.effectiveCostPerSuccess > 0);
  const allEffectiveCosts = paidCostModels.map((m) => m.effectiveCostPerSuccess);
  const meanEffectiveCost = mean(allEffectiveCosts);
  metrics["meanEffectiveCostPerSuccess"] = meanEffectiveCost;

  const costInefficientThreshold = meanEffectiveCost * 2;
  // Only flag paid models as inefficient — local models are never "cost-inefficient"
  const highCostModels = paidCostModels.filter(
    (m) => m.effectiveCostPerSuccess > costInefficientThreshold && meanEffectiveCost > 0
  );

  // ── Under-routing detection ────────────────────────────────────

  const underRoutingRecords = detectUnderRouting(records);
  const hasUnderRouting = underRoutingRecords.length > 0;
  metrics["underRoutingCount"] = underRoutingRecords.length;

  // ── Over-routing detection ─────────────────────────────────────

  const overRoutingRecords = detectOverRouting(records);
  const hasOverRouting = overRoutingRecords.length > 0;
  metrics["overRoutingCount"] = overRoutingRecords.length;

  // ── Scoring ────────────────────────────────────────────────────

  let score = 100;

  if (autoFailureRate > 0.2) {
    score -= 15;
  }
  if (hasUnderRouting) {
    score -= 10;
  }
  if (hasOverRouting) {
    score -= 10;
  }
  if (modelCount === 1) {
    score -= 5;
  }

  score = clamp(score, 0, 100);
  metrics["score"] = score;

  // ── Findings ──────────────────────────────────────────────────

  // Under-routing finding
  if (hasUnderRouting) {
    const stages = [...new Set(underRoutingRecords.map((r) => r.stage))];
    const models = [...new Set(underRoutingRecords.map(modelKey))];
    const severity: Severity = underRoutingRecords.length >= 5 ? "high" : "medium";
    const confidence: Confidence = underRoutingRecords.length >= 10 ? "high" : "medium";

    findings.push({
      id: `mr-${findingIndex++}`,
      dimension: "model-routing",
      severity,
      title: "Under-Routing Detected: Lightweight Models Failing on Complex Tasks",
      description:
        `${underRoutingRecords.length} auto-selected execution(s) used lightweight models ` +
        `(${models.join(", ")}) on high-complexity tasks (L/XL) and failed. ` +
        `Affected stages: ${stages.join(", ")}.`,
      impact:
        "Failed executions on complex tasks increase retries, cost, and pipeline latency. " +
        "The auto-selector is routing complex work to under-powered models.",
      recommendation:
        "Review auto-selector complexity thresholds. Consider raising the complexity boundary " +
        "that triggers Haiku/Sonnet selection, or add stage-specific overrides for known heavy stages.",
      evidence: {
        underRoutingCount: underRoutingRecords.length,
        affectedStages: stages,
        affectedModels: models,
        complexityLevels: [
          ...new Set(underRoutingRecords.map((r) => r.autoSelectorComplexity).filter(Boolean)),
        ],
      },
      confidence,
    });
  }

  // Over-routing finding
  if (hasOverRouting) {
    const stages = [...new Set(overRoutingRecords.map((r) => r.stage))];
    const totalWasteCost = overRoutingRecords.reduce((sum, r) => sum + r.costUsd, 0);
    const severity: Severity = overRoutingRecords.length >= 10 ? "high" : "medium";
    const confidence: Confidence = overRoutingRecords.length >= 10 ? "high" : "medium";

    findings.push({
      id: `mr-${findingIndex++}`,
      dimension: "model-routing",
      severity,
      title: "Over-Routing Detected: Heavyweight Models on Simple Tasks",
      description:
        `${overRoutingRecords.length} auto-selected execution(s) used Opus on low-complexity ` +
        `tasks (XS/S) and succeeded on the first attempt. ` +
        `Affected stages: ${stages.join(", ")}.`,
      impact:
        `Unnecessary use of heavyweight models inflates cost. Estimated excess spend: $${totalWasteCost.toFixed(4)}. ` +
        "These tasks likely succeed with a cheaper model tier.",
      recommendation:
        "Lower the complexity threshold for Opus selection, or configure stage-level model caps " +
        "for stages that consistently succeed with lighter models.",
      evidence: {
        overRoutingCount: overRoutingRecords.length,
        affectedStages: stages,
        estimatedWasteCostUsd: totalWasteCost,
        complexityLevels: [
          ...new Set(overRoutingRecords.map((r) => r.autoSelectorComplexity).filter(Boolean)),
        ],
      },
      confidence,
    });
  }

  // Low auto-selection accuracy finding
  if (autoTotal >= config.minimumSampleSizes.basic && autoFailureRate > 0.2) {
    const severity: Severity = autoFailureRate > 0.4 ? "high" : "medium";
    const confidence: Confidence =
      autoTotal >= config.minimumSampleSizes.significance
        ? "high"
        : autoTotal >= config.minimumSampleSizes.trend
          ? "medium"
          : "low";

    findings.push({
      id: `mr-${findingIndex++}`,
      dimension: "model-routing",
      severity,
      title: "Low Auto-Selection Accuracy",
      description:
        `Auto-selected model executions have a ${(autoFailureRate * 100).toFixed(1)}% failure rate ` +
        `across ${autoTotal} auto-routed execution(s). ` +
        `Overall success rate for auto-selection: ${(autoSuccessRate * 100).toFixed(1)}%.`,
      impact:
        "Frequent auto-selection failures increase retry counts, cost, and pipeline duration. " +
        "The selection heuristic may not be calibrated correctly for current workloads.",
      recommendation:
        "Audit auto-selector confidence scores and complexity mappings. " +
        "Consider enabling hybrid mode or adding manual overrides for problematic stages.",
      evidence: {
        autoTotal,
        autoSuccessRate,
        autoFailureRate,
      },
      confidence,
    });
  }

  // Cost-ineffective model finding
  if (highCostModels.length > 0) {
    const worstModel = highCostModels.reduce((a, b) =>
      a.effectiveCostPerSuccess > b.effectiveCostPerSuccess ? a : b
    );
    const severity: Severity = "medium";
    const confidence: Confidence =
      worstModel.sampleSize >= config.minimumSampleSizes.trend ? "medium" : "low";

    findings.push({
      id: `mr-${findingIndex}`,
      dimension: "model-routing",
      severity,
      title: "Cost-Ineffective Model(s) Identified",
      description:
        `${highCostModels.length} model(s) have an effective cost-per-success more than 2x the ` +
        `fleet average ($${meanEffectiveCost.toFixed(4)}). ` +
        `Highest offender: "${worstModel.model}" at $${worstModel.effectiveCostPerSuccess.toFixed(4)} per success ` +
        `(success rate: ${(worstModel.successRate * 100).toFixed(1)}%, n=${worstModel.sampleSize}).`,
      impact:
        "Cost-ineffective models disproportionately increase pipeline operating costs " +
        "without delivering proportional success improvements.",
      recommendation:
        "Review model assignments for the identified stages. " +
        "Consider routing these workloads to a more cost-effective tier or " +
        "investigating root-cause failures driving the high effective cost.",
      evidence: {
        meanEffectiveCostPerSuccess: meanEffectiveCost,
        highCostModels: highCostModels.map((m) => ({
          model: m.model,
          effectiveCostPerSuccess: m.effectiveCostPerSuccess,
          successRate: m.successRate,
          sampleSize: m.sampleSize,
        })),
      },
      confidence,
    });
  }

  // ── Period comparison ──────────────────────────────────────────

  let periodComparison = undefined;
  if (baseline) {
    const baselineAutoSuccessRate = computeAutoSelectionSuccessRate(baseline.executionHistory);
    const baselineAutoTotal = baseline.executionHistory.filter(isAutoSelected).length;
    const comparisonSampleSize = Math.min(autoTotal, baselineAutoTotal);

    if (comparisonSampleSize >= config.minimumSampleSizes.basic) {
      periodComparison = buildPeriodComparison(
        autoSuccessRate,
        baselineAutoSuccessRate,
        comparisonSampleSize,
        false, // lowerIsBetter=false — higher auto-selection success rate is better
        config.confidenceThreshold
      );
    }
  }

  return {
    dimension: "model-routing",
    score,
    status: getHealthStatus(score),
    findings,
    metrics,
    hasEnoughData: enoughData,
    sampleSize,
    periodComparison,
  };
}
