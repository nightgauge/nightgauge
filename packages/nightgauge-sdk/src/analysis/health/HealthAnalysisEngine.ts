/**
 * HealthAnalysisEngine - Multi-Dimensional Pipeline Health Orchestrator
 *
 * Evaluates pipeline health across 7 dimensions, then runs a cross-referencing
 * pass to correlate findings. Produces a weighted overall score and actionable
 * insights.
 *
 * All analysis is deterministic — no AI interpretation.
 *
 * @see Issue #1101 - Multi-Dimensional Health Analysis Engine
 */

import type {
  HealthDimension,
  HealthAnalysisConfig,
  HealthAnalysisInput,
  HealthAnalysisResult,
  DimensionResult,
  OverallHealthStatus,
} from "./types.js";
import { ALL_DIMENSIONS, DEFAULT_HEALTH_CONFIG, getHealthStatus } from "./types.js";
import { clamp } from "./statistics.js";

/** The slice of a DimensionResult the overall score reads. */
export interface ScoredDimension {
  dimension: HealthDimension;
  score: number;
  hasEnoughData: boolean;
}

/**
 * Weighted overall score over the dimensions that HAVE data (#1197).
 *
 * A dimension with `hasEnoughData: false` is excluded and the surviving
 * weights are re-normalised, so a starved analyzer's placeholder `score`
 * (50 → "fair") never asserts mediocrity from absence of evidence. When no
 * dimension has data — or the ones that do all weigh 0 — the answer is
 * `null`, rendered as N/A, matching the `nightgauge-pipeline-health` SKILL.
 *
 * Engine and SKILL are pinned to one corpus:
 * `skills/nightgauge-pipeline-health/overall-score-corpus.json`
 * (tests/analysis/health/overallScore.corpusParity.test.ts). Change the rule
 * there first.
 */
export function computeOverallHealthScore(
  dimensions: Iterable<ScoredDimension>,
  weights: Partial<Record<HealthDimension, number>>
): number | null {
  let weightedSum = 0;
  let totalWeight = 0;

  for (const result of dimensions) {
    if (!result.hasEnoughData) continue;
    const weight = weights[result.dimension] ?? 0;
    weightedSum += result.score * weight;
    totalWeight += weight;
  }

  if (totalWeight === 0) return null;
  return clamp(Math.round(weightedSum / totalWeight), 0, 100);
}

/** Status for an overall score; `null` maps to `"no-data"`. */
export function getOverallHealthStatus(score: number | null): OverallHealthStatus {
  return score === null ? "no-data" : getHealthStatus(score);
}
import { analyzeTokenEconomics } from "./dimensions/tokenEconomics.js";
import { analyzeCostHealth } from "./dimensions/costHealth.js";
import { analyzeStageEffectiveness } from "./dimensions/stageEffectiveness.js";
import { analyzeModelRouting } from "./dimensions/modelRouting.js";
import { analyzeReliability } from "./dimensions/reliability.js";
import { analyzeLearningEffectiveness } from "./dimensions/learningEffectiveness.js";
import { analyzePipelineVelocity } from "./dimensions/pipelineVelocity.js";
import { analyzeSkillDrift } from "./dimensions/skillDrift.js";
import { crossReference } from "./crossReferencer.js";

type DimensionAnalyzer = (
  dataset: HealthAnalysisInput,
  config: HealthAnalysisConfig,
  baseline?: HealthAnalysisInput
) => DimensionResult;

const DIMENSION_ANALYZERS: Record<HealthDimension, DimensionAnalyzer> = {
  "token-economics": analyzeTokenEconomics,
  "cost-health": analyzeCostHealth,
  "stage-effectiveness": analyzeStageEffectiveness,
  "model-routing": analyzeModelRouting,
  reliability: analyzeReliability,
  "learning-effectiveness": analyzeLearningEffectiveness,
  "pipeline-velocity": analyzePipelineVelocity,
  "skill-drift": analyzeSkillDrift,
};

export class HealthAnalysisEngine {
  private readonly config: HealthAnalysisConfig;

  constructor(config?: Partial<HealthAnalysisConfig>) {
    this.config = {
      dimensions: config?.dimensions ?? DEFAULT_HEALTH_CONFIG.dimensions,
      minimumSampleSizes: {
        ...DEFAULT_HEALTH_CONFIG.minimumSampleSizes,
        ...config?.minimumSampleSizes,
      },
      confidenceThreshold: config?.confidenceThreshold ?? DEFAULT_HEALTH_CONFIG.confidenceThreshold,
      weights: { ...DEFAULT_HEALTH_CONFIG.weights, ...config?.weights },
      cacheThresholds: config?.cacheThresholds ?? DEFAULT_HEALTH_CONFIG.cacheThresholds,
    };
  }

  /**
   * Run all (or filtered) dimensions and cross-referencing pass.
   *
   * @param dataset - Current period data
   * @param baseline - Optional baseline period data for comparison
   * @returns Complete health analysis result
   */
  analyze(dataset: HealthAnalysisInput, baseline?: HealthAnalysisInput): HealthAnalysisResult {
    const dimensionsToRun = this.config.dimensions.filter((d) => ALL_DIMENSIONS.includes(d));

    // Run all dimension analyzers
    const dimensionResults = new Map<HealthDimension, DimensionResult>();
    const resultRecord: Partial<Record<HealthDimension, DimensionResult>> = {};

    for (const dimension of dimensionsToRun) {
      const analyzer = DIMENSION_ANALYZERS[dimension];
      const result = analyzer(dataset, this.config, baseline);
      dimensionResults.set(dimension, result);
      resultRecord[dimension] = result;
    }

    // Cross-referencing second pass
    const crossReferences = crossReference(dimensionResults);

    // Weighted overall score over the dimensions that have data (#1197)
    const overallScore = computeOverallHealthScore(dimensionResults.values(), this.config.weights);
    const overallStatus = getOverallHealthStatus(overallScore);

    // Generate summary
    const summary = this.generateSummary(
      dimensionResults,
      crossReferences,
      overallScore,
      overallStatus
    );

    return {
      dimensions: resultRecord,
      crossReferences,
      overallScore,
      overallStatus,
      summary,
      analyzedAt: new Date().toISOString(),
      config: this.config,
    };
  }

  /**
   * Analyze a single dimension (for --dimensions filtering).
   */
  analyzeDimension(
    dimension: HealthDimension,
    dataset: HealthAnalysisInput,
    baseline?: HealthAnalysisInput
  ): DimensionResult {
    const analyzer = DIMENSION_ANALYZERS[dimension];
    return analyzer(dataset, this.config, baseline);
  }

  /**
   * Generate a human-readable summary of the analysis.
   */
  private generateSummary(
    dimensionResults: Map<HealthDimension, DimensionResult>,
    crossReferences: ReturnType<typeof crossReference>,
    overallScore: number | null,
    overallStatus: OverallHealthStatus
  ): string {
    const parts: string[] = [];

    parts.push(
      overallScore === null
        ? "Pipeline health: no-data (N/A — no dimension had enough data)."
        : `Pipeline health: ${overallStatus} (${overallScore}/100).`
    );

    // Highlight dimensions with data
    const withData = [...dimensionResults.values()].filter((r) => r.hasEnoughData);
    const withoutData = [...dimensionResults.values()].filter((r) => !r.hasEnoughData);

    if (withData.length > 0) {
      parts.push(`${withData.length} dimension(s) analyzed.`);
    }
    if (withoutData.length > 0) {
      parts.push(`${withoutData.length} dimension(s) have insufficient data.`);
    }

    // Worst dimension
    const worst = [...dimensionResults.entries()]
      .filter(([, r]) => r.hasEnoughData)
      .sort(([, a], [, b]) => a.score - b.score)[0];

    if (worst && worst[1].score < 50) {
      parts.push(`Weakest area: ${worst[0]} (${worst[1].score}/100).`);
    }

    // Total findings
    const totalFindings = [...dimensionResults.values()].reduce(
      (sum, r) => sum + r.findings.length,
      0
    );
    if (totalFindings > 0) {
      parts.push(`${totalFindings} finding(s) detected.`);
    }

    // Cross-references
    if (crossReferences.length > 0) {
      parts.push(`${crossReferences.length} cross-dimension correlation(s) identified.`);
    }

    return parts.join(" ");
  }
}
