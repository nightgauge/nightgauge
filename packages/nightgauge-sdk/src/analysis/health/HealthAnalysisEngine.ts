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
} from "./types.js";
import { ALL_DIMENSIONS, DEFAULT_HEALTH_CONFIG, getHealthStatus } from "./types.js";
import { clamp } from "./statistics.js";
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

    // Compute weighted overall score
    const overallScore = this.computeOverallScore(dimensionResults);
    const overallStatus = getHealthStatus(overallScore);

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
   * Compute weighted overall score from dimension results.
   * Uses auto-normalized weights so the sum doesn't need to equal 1.0.
   */
  private computeOverallScore(dimensionResults: Map<HealthDimension, DimensionResult>): number {
    let weightedSum = 0;
    let totalWeight = 0;

    for (const [dimension, result] of dimensionResults) {
      const weight = this.config.weights[dimension] ?? 0;
      weightedSum += result.score * weight;
      totalWeight += weight;
    }

    if (totalWeight === 0) return 0;
    return clamp(Math.round(weightedSum / totalWeight), 0, 100);
  }

  /**
   * Generate a human-readable summary of the analysis.
   */
  private generateSummary(
    dimensionResults: Map<HealthDimension, DimensionResult>,
    crossReferences: ReturnType<typeof crossReference>,
    overallScore: number,
    overallStatus: string
  ): string {
    const parts: string[] = [];

    parts.push(`Pipeline health: ${overallStatus} (${overallScore}/100).`);

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
