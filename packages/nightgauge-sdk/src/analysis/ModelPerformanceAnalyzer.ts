/**
 * ModelPerformanceAnalyzer - Compares AI model effectiveness across pipeline stages
 *
 * Aggregates execution history records to compute per-model, per-stage metrics
 * including success rates, effective costs (factoring retries), and duration.
 * Generates routing recommendations (downgrade, upgrade, complexity-based, A/B).
 *
 * All analysis is deterministic — no AI interpretation. Follows the architecture's
 * deterministic vs probabilistic separation.
 *
 * @see types.ts for all interface definitions
 * @see #653 for feature requirements
 * @see #649 for ExecutionHistoryRecord schema (prerequisite)
 */

import type {
  ExecutionHistoryRecord,
  ModelAnalyzerConfig,
  ModelIdentifier,
  ModelRoutingAnalysis,
  ModelStagePerformance,
  RoutingRecommendation,
  StageModelComparison,
  AutoSelectionAnalysis,
  AutoSelectionStageOutcome,
  UnderRoutingPattern,
  OverRoutingPattern,
  ThresholdRecommendation,
} from "./types.js";
import type { ExperimentReport } from "./experiment-types.js";
import { DEFAULT_MODEL_COST_RATES } from "./types.js";
import { AutoModelSelector } from "./AutoModelSelector.js";
import type { ComplexityLabel, ModelTier } from "./AutoModelSelector.js";

const DEFAULT_CONFIG: Required<
  Pick<ModelAnalyzerConfig, "minSamplesPerModelPerStage" | "recencyWeight" | "qualityThreshold">
> = {
  minSamplesPerModelPerStage: 10,
  recencyWeight: 0.1,
  qualityThreshold: 0.5,
};

export class ModelPerformanceAnalyzer {
  private readonly config: ModelAnalyzerConfig;

  constructor(config?: Partial<ModelAnalyzerConfig>) {
    this.config = {
      minSamplesPerModelPerStage:
        config?.minSamplesPerModelPerStage ?? DEFAULT_CONFIG.minSamplesPerModelPerStage,
      recencyWeight: config?.recencyWeight ?? DEFAULT_CONFIG.recencyWeight,
      qualityThreshold: config?.qualityThreshold ?? DEFAULT_CONFIG.qualityThreshold,
      costRates: config?.costRates ?? DEFAULT_MODEL_COST_RATES,
      dateRange: config?.dateRange,
    };
  }

  /**
   * Main entry point: analyze records and produce full routing analysis report.
   */
  analyze(
    records: ExecutionHistoryRecord[],
    currentDefaults?: Record<string, ModelIdentifier>
  ): ModelRoutingAnalysis {
    const filtered = ModelPerformanceAnalyzer.filterByDateRange(
      records,
      this.config.dateRange?.since,
      this.config.dateRange?.until
    );

    const stageModelMap = this.aggregatePerformance(filtered);
    const stageComparisons: StageModelComparison[] = [];

    for (const [stage, modelMap] of stageModelMap) {
      const performances = Array.from(modelMap.values());
      stageComparisons.push(this.compareModelsForStage(performances, stage));
    }

    const recommendations = this.generateRecommendations(stageComparisons, currentDefaults ?? {});

    const stagesWithSufficientData = stageComparisons.filter(
      (c) => c.recommendedModel !== null
    ).length;

    const stagesNeedingMoreData = stageComparisons
      .filter((c) => c.recommendedModel === null)
      .map((c) => c.stage);

    const totalPotentialSavingsUsd = recommendations.reduce(
      (sum, r) => sum + r.estimatedSavingsUsd,
      0
    );

    let overallRecommendation: string;
    if (filtered.length === 0) {
      overallRecommendation = "No execution history records to analyze.";
    } else if (recommendations.length === 0) {
      overallRecommendation =
        stagesNeedingMoreData.length > 0
          ? `Insufficient data for recommendations. Need more samples for: ${stagesNeedingMoreData.join(", ")}.`
          : "Current model routing appears optimal based on available data.";
    } else {
      overallRecommendation = `${recommendations.length} routing optimization(s) identified with potential savings of $${totalPotentialSavingsUsd.toFixed(4)}/run.`;
    }

    // Auto-selection feedback loop analysis (Issue #734)
    const autoSelectionAnalysis = this.analyzeAutoSelectionOutcomes(filtered);

    return {
      analyzedAt: new Date().toISOString(),
      recordsAnalyzed: filtered.length,
      stageComparisons,
      recommendations,
      autoSelectionAnalysis:
        autoSelectionAnalysis.totalAutoSelectedRecords > 0 ? autoSelectionAnalysis : undefined,
      summary: {
        totalPotentialSavingsUsd,
        stagesWithSufficientData,
        stagesNeedingMoreData,
        overallRecommendation,
      },
    };
  }

  /**
   * Aggregate per-model, per-stage metrics from raw execution records.
   * Returns Map<stage, Map<model, ModelStagePerformance>>.
   */
  aggregatePerformance(
    records: ExecutionHistoryRecord[]
  ): Map<string, Map<string, ModelStagePerformance>> {
    const result = new Map<string, Map<string, ModelStagePerformance>>();

    // Group records by stage and model
    const groups = new Map<string, Map<string, ExecutionHistoryRecord[]>>();

    for (const record of records) {
      const modelId = ModelPerformanceAnalyzer.normalizeModelId(record.adapter, record.model);
      const stage = record.stage;

      if (!groups.has(stage)) {
        groups.set(stage, new Map());
      }
      const stageMap = groups.get(stage)!;

      if (!stageMap.has(modelId)) {
        stageMap.set(modelId, []);
      }
      stageMap.get(modelId)!.push(record);
    }

    // Compute aggregated metrics per group
    for (const [stage, modelMap] of groups) {
      const perfMap = new Map<string, ModelStagePerformance>();

      for (const [modelId, recs] of modelMap) {
        perfMap.set(modelId, this.computePerformance(modelId, stage, recs));
      }

      result.set(stage, perfMap);
    }

    return result;
  }

  /**
   * Compare models for a single stage and produce a comparison report.
   */
  compareModelsForStage(
    performances: ModelStagePerformance[],
    stage?: string
  ): StageModelComparison {
    const stageName = stage ?? performances[0]?.stage ?? "unknown";
    const minSamples = this.config.minSamplesPerModelPerStage;
    const qualityThreshold = this.config.qualityThreshold ?? DEFAULT_CONFIG.qualityThreshold;

    if (performances.length === 0) {
      return {
        stage: stageName,
        models: [],
        recommendedModel: null,
        recommendation: "No models to compare.",
        confidence: "low",
        estimatedSavingsUsd: 0,
      };
    }

    // Check if any model has sufficient samples
    const sufficientModels = performances.filter((p) => p.runs >= minSamples);

    if (sufficientModels.length === 0) {
      return {
        stage: stageName,
        models: performances,
        recommendedModel: null,
        recommendation: `Insufficient data: all models have fewer than ${minSamples} samples.`,
        confidence: "low",
        estimatedSavingsUsd: 0,
      };
    }

    if (sufficientModels.length === 1) {
      const model = sufficientModels[0];
      return {
        stage: stageName,
        models: performances,
        recommendedModel: model.successRate >= qualityThreshold ? model.model : null,
        recommendation:
          model.successRate >= qualityThreshold
            ? `Only ${model.model} has sufficient data (${model.runs} runs). Consider testing other models.`
            : `Only ${model.model} has data but success rate (${(model.successRate * 100).toFixed(1)}%) is below threshold.`,
        confidence: "low",
        estimatedSavingsUsd: 0,
      };
    }

    // Find optimal model among those meeting quality threshold
    const optimal = this.selectOptimalModel(sufficientModels, qualityThreshold);

    if (optimal === null) {
      return {
        stage: stageName,
        models: performances,
        recommendedModel: null,
        recommendation: `No models meet the quality threshold (${(qualityThreshold * 100).toFixed(0)}% success rate).`,
        confidence: "low",
        estimatedSavingsUsd: 0,
      };
    }

    const optimalPerf = sufficientModels.find((p) => p.model === optimal)!;
    const otherModels = sufficientModels.filter(
      (p) => p.model !== optimal && p.successRate >= qualityThreshold
    );
    const maxOtherCost =
      otherModels.length > 0
        ? Math.max(...otherModels.map((p) => p.effectiveCostPerSuccess))
        : optimalPerf.effectiveCostPerSuccess;

    const savings = maxOtherCost - optimalPerf.effectiveCostPerSuccess;

    const confidence = this.computeConfidence(sufficientModels);

    return {
      stage: stageName,
      models: performances,
      recommendedModel: optimal,
      recommendation: `${optimal} is most cost-effective at $${optimalPerf.effectiveCostPerSuccess.toFixed(4)}/success with ${(optimalPerf.successRate * 100).toFixed(1)}% success rate.`,
      confidence,
      estimatedSavingsUsd: Math.max(0, savings),
    };
  }

  /**
   * Generate routing recommendations from stage comparisons and current defaults.
   */
  generateRecommendations(
    comparisons: StageModelComparison[],
    currentDefaults: Record<string, ModelIdentifier>
  ): RoutingRecommendation[] {
    const recommendations: RoutingRecommendation[] = [];
    const minSamples = this.config.minSamplesPerModelPerStage;
    const qualityThreshold = this.config.qualityThreshold ?? DEFAULT_CONFIG.qualityThreshold;

    for (const comparison of comparisons) {
      const sufficientModels = comparison.models.filter((m) => m.runs >= minSamples);
      if (sufficientModels.length < 2) {
        // Check for A/B comparison suggestion
        const insufficientModels = comparison.models.filter(
          (m) => m.runs > 0 && m.runs < minSamples
        );
        for (const model of insufficientModels) {
          recommendations.push({
            type: "ab-comparison",
            stage: comparison.stage,
            currentModel:
              currentDefaults[comparison.stage] ?? sufficientModels[0]?.model ?? "unknown",
            suggestedModel: model.model,
            rationale: `${model.model} has only ${model.runs} samples (need ${minSamples}). Run more executions for reliable comparison.`,
            estimatedSavingsUsd: 0,
            confidence: "low",
            evidence: {
              currentSuccessRate: sufficientModels[0]?.successRate ?? 0,
              suggestedSuccessRate: model.successRate,
              currentEffectiveCost: sufficientModels[0]?.effectiveCostPerSuccess ?? 0,
              suggestedEffectiveCost: model.effectiveCostPerSuccess,
              sampleSizes: Object.fromEntries(comparison.models.map((m) => [m.model, m.runs])),
            },
          });
        }
        continue;
      }

      const currentModel = currentDefaults[comparison.stage];
      if (!currentModel) continue;

      const currentPerf = sufficientModels.find((m) => m.model === currentModel);
      if (!currentPerf) continue;

      // Check for downgrade opportunity
      const cheaperModels = sufficientModels.filter(
        (m) =>
          m.model !== currentModel &&
          m.avgCostUsd < currentPerf.avgCostUsd &&
          m.successRate >= qualityThreshold
      );

      for (const cheaper of cheaperModels) {
        if (cheaper.effectiveCostPerSuccess < currentPerf.effectiveCostPerSuccess) {
          recommendations.push({
            type: "downgrade",
            stage: comparison.stage,
            currentModel,
            suggestedModel: cheaper.model,
            rationale: `${cheaper.model} is cheaper ($${cheaper.effectiveCostPerSuccess.toFixed(4)}/success vs $${currentPerf.effectiveCostPerSuccess.toFixed(4)}/success) with acceptable quality (${(cheaper.successRate * 100).toFixed(1)}% success).`,
            estimatedSavingsUsd:
              currentPerf.effectiveCostPerSuccess - cheaper.effectiveCostPerSuccess,
            confidence: this.computeConfidence([currentPerf, cheaper]),
            evidence: {
              currentSuccessRate: currentPerf.successRate,
              suggestedSuccessRate: cheaper.successRate,
              currentEffectiveCost: currentPerf.effectiveCostPerSuccess,
              suggestedEffectiveCost: cheaper.effectiveCostPerSuccess,
              sampleSizes: Object.fromEntries(comparison.models.map((m) => [m.model, m.runs])),
            },
          });
        }
      }

      // Check for upgrade opportunity (current model below quality threshold)
      if (currentPerf.successRate < qualityThreshold) {
        const betterModels = sufficientModels.filter(
          (m) => m.model !== currentModel && m.successRate >= qualityThreshold
        );

        for (const better of betterModels) {
          recommendations.push({
            type: "upgrade",
            stage: comparison.stage,
            currentModel,
            suggestedModel: better.model,
            rationale: `${currentModel} success rate (${(currentPerf.successRate * 100).toFixed(1)}%) is below threshold. ${better.model} achieves ${(better.successRate * 100).toFixed(1)}% success.`,
            estimatedSavingsUsd:
              currentPerf.effectiveCostPerSuccess - better.effectiveCostPerSuccess,
            confidence: this.computeConfidence([currentPerf, better]),
            evidence: {
              currentSuccessRate: currentPerf.successRate,
              suggestedSuccessRate: better.successRate,
              currentEffectiveCost: currentPerf.effectiveCostPerSuccess,
              suggestedEffectiveCost: better.effectiveCostPerSuccess,
              sampleSizes: Object.fromEntries(comparison.models.map((m) => [m.model, m.runs])),
            },
          });
        }
      }

      // Check for complexity-based routing
      this.checkComplexityBasedRouting(
        comparison,
        currentModel,
        currentPerf,
        sufficientModels,
        recommendations
      );
    }

    return recommendations;
  }

  /**
   * Determine the optimal model for a stage based on effective cost per success,
   * filtered by the quality threshold.
   */
  selectOptimalModel(
    performances: ModelStagePerformance[],
    qualityThreshold: number
  ): ModelIdentifier | null {
    const qualifying = performances.filter((p) => p.successRate >= qualityThreshold);
    if (qualifying.length === 0) return null;

    // Sort by effective cost per success (ascending) — lower is better
    qualifying.sort((a, b) => a.effectiveCostPerSuccess - b.effectiveCostPerSuccess);

    return qualifying[0].model;
  }

  /**
   * Normalize adapter + model into a composite identifier.
   * e.g. ("claude", "sonnet") → "claude:sonnet"
   */
  static normalizeModelId(adapter?: string, model?: string): ModelIdentifier {
    const normalizedAdapter = (adapter ?? "unknown").toLowerCase().trim();
    const normalizedModel = (model ?? "unknown").toLowerCase().trim();
    return `${normalizedAdapter}:${normalizedModel}`;
  }

  /**
   * Filter records by date range.
   */
  static filterByDateRange(
    records: ExecutionHistoryRecord[],
    since?: string,
    until?: string
  ): ExecutionHistoryRecord[] {
    if (!since && !until) return records;

    const sinceMs = since ? new Date(since).getTime() : -Infinity;
    const untilMs = until ? new Date(until).getTime() : Infinity;

    return records.filter((r) => {
      const ts = new Date(r.timestamp).getTime();
      return ts >= sinceMs && ts <= untilMs;
    });
  }

  // --- Auto-selection feedback loop methods (Issue #734) ---

  /**
   * Analyze auto-selection outcomes across all records.
   *
   * Filters to records where selectionSource === 'auto', computes
   * per-stage success rates, and compares to overall success rates.
   */
  analyzeAutoSelectionOutcomes(records: ExecutionHistoryRecord[]): AutoSelectionAnalysis {
    const autoRecords = records.filter((r) => r.selectionSource === "auto");

    if (autoRecords.length === 0) {
      return {
        totalAutoSelectedRecords: 0,
        overallAutoSuccessRate: 0,
        perStageOutcomes: [],
        underRoutingPatterns: this.detectUnderRouting(records),
        overRoutingPatterns: this.detectOverRouting(records),
        thresholdRecommendations: this.generateThresholdRecommendations(records),
        costSavingsVsStaticUsd: 0,
      };
    }

    const autoSuccesses = autoRecords.filter((r) => r.success).length;
    const overallAutoSuccessRate = autoSuccesses / autoRecords.length;

    // Per-stage breakdown
    const stageMap = new Map<string, ExecutionHistoryRecord[]>();
    for (const record of autoRecords) {
      const existing = stageMap.get(record.stage) ?? [];
      existing.push(record);
      stageMap.set(record.stage, existing);
    }

    const perStageOutcomes: AutoSelectionStageOutcome[] = [];
    for (const [stage, stageRecords] of stageMap) {
      const successes = stageRecords.filter((r) => r.success).length;
      const failures = stageRecords.length - successes;
      const confidences = stageRecords
        .map((r) => r.autoSelectorConfidence)
        .filter((c): c is number => c !== undefined);
      const avgConfidence =
        confidences.length > 0
          ? confidences.reduce((sum, c) => sum + c, 0) / confidences.length
          : 0;

      // Count models used
      const modelsUsed: Record<string, number> = {};
      for (const r of stageRecords) {
        const model = r.selectedModel ?? r.model ?? "unknown";
        modelsUsed[model] = (modelsUsed[model] ?? 0) + 1;
      }

      perStageOutcomes.push({
        stage,
        totalAutoSelected: stageRecords.length,
        successCount: successes,
        failureCount: failures,
        successRate: stageRecords.length > 0 ? successes / stageRecords.length : 0,
        avgConfidence,
        modelsUsed,
      });
    }

    // Estimate cost savings vs static defaults
    // Compare auto-selected cost to what the default model (sonnet) would have cost
    const costSavingsVsStaticUsd = this.estimateCostSavingsVsStatic(autoRecords);

    return {
      totalAutoSelectedRecords: autoRecords.length,
      overallAutoSuccessRate,
      perStageOutcomes,
      underRoutingPatterns: this.detectUnderRouting(records),
      overRoutingPatterns: this.detectOverRouting(records),
      thresholdRecommendations: this.generateThresholdRecommendations(records),
      costSavingsVsStaticUsd,
    };
  }

  /**
   * Detect under-routing patterns: auto-selection chose a lighter model
   * for a complex task that then failed.
   *
   * Looks for records where source=auto AND model was haiku/sonnet AND
   * complexity was L/XL AND the stage failed.
   */
  detectUnderRouting(records: ExecutionHistoryRecord[]): UnderRoutingPattern[] {
    const autoRecords = records.filter((r) => r.selectionSource === "auto");
    const patterns: UnderRoutingPattern[] = [];

    // Group by stage + model + complexity
    const groups = new Map<
      string,
      { stage: string; model: string; complexity: string; failures: number }
    >();

    for (const record of autoRecords) {
      if (record.success) continue;
      const model = (record.selectedModel ?? record.model ?? "").toLowerCase();
      const complexity = record.autoSelectorComplexity ?? "";

      // Under-routing: lightweight model used for complex task
      const isLightModel = model.includes("haiku") || model.includes("sonnet");
      const isComplexTask = ["L", "XL"].includes(complexity);

      if (isLightModel && isComplexTask) {
        const key = `${record.stage}|${model}|${complexity}`;
        const existing = groups.get(key) ?? {
          stage: record.stage,
          model,
          complexity,
          failures: 0,
        };
        existing.failures++;
        groups.set(key, existing);
      }
    }

    for (const group of groups.values()) {
      if (group.failures >= 2) {
        patterns.push({
          stage: group.stage,
          model: group.model,
          complexity: group.complexity,
          failureCount: group.failures,
          suggestion: `Consider upgrading from ${group.model} for ${group.complexity}-complexity tasks in ${group.stage}. ${group.failures} failures detected.`,
        });
      }
    }

    return patterns;
  }

  /**
   * Detect over-routing patterns: auto-selection chose a more capable model
   * for a simple task that succeeded easily (wasted cost).
   *
   * Looks for records where source=auto AND model was opus AND
   * complexity was XS/S AND the stage succeeded on first attempt.
   */
  detectOverRouting(records: ExecutionHistoryRecord[]): OverRoutingPattern[] {
    const autoRecords = records.filter((r) => r.selectionSource === "auto");
    const patterns: OverRoutingPattern[] = [];

    const groups = new Map<
      string,
      {
        stage: string;
        model: string;
        complexity: string;
        successes: number;
        totalCost: number;
      }
    >();

    for (const record of autoRecords) {
      if (!record.success || record.retries > 0) continue;
      const model = (record.selectedModel ?? record.model ?? "").toLowerCase();
      const complexity = record.autoSelectorComplexity ?? "";

      // Over-routing: heavy model used for simple task. Fable is the heaviest
      // tier (premium frontier) — flag it even more strongly than Opus here.
      const isHeavyModel = model.includes("opus") || model.includes("fable");
      const isSimpleTask = ["XS", "S"].includes(complexity);

      if (isHeavyModel && isSimpleTask) {
        const key = `${record.stage}|${model}|${complexity}`;
        const existing = groups.get(key) ?? {
          stage: record.stage,
          model,
          complexity,
          successes: 0,
          totalCost: 0,
        };
        existing.successes++;
        existing.totalCost += record.costUsd;
        groups.set(key, existing);
      }
    }

    // Estimate waste: difference between opus cost and sonnet cost
    const costRates = this.config.costRates ?? DEFAULT_MODEL_COST_RATES;
    const opusRate = costRates["opus"];
    const sonnetRate = costRates["sonnet"];

    for (const group of groups.values()) {
      if (group.successes >= 2 && opusRate && sonnetRate) {
        // Rough waste estimate: (opus_cost - sonnet_cost) per run
        const costRatio =
          opusRate.inputPerMillion > 0 && sonnetRate.inputPerMillion > 0
            ? sonnetRate.inputPerMillion / opusRate.inputPerMillion
            : 0.2;
        const estimatedWaste = group.totalCost * (1 - costRatio);

        patterns.push({
          stage: group.stage,
          model: group.model,
          complexity: group.complexity,
          successCount: group.successes,
          estimatedWasteUsd: Math.round(estimatedWaste * 10000) / 10000,
          suggestion: `Consider downgrading from ${group.model} for ${group.complexity}-complexity tasks in ${group.stage}. ${group.successes} easy successes detected with estimated waste of $${estimatedWaste.toFixed(4)}.`,
        });
      }
    }

    return patterns;
  }

  /**
   * Generate threshold adjustment recommendations based on outcome data.
   *
   * Analyzes the distribution of auto-selected records by complexity
   * and suggests threshold adjustments when clear patterns emerge.
   */
  generateThresholdRecommendations(records: ExecutionHistoryRecord[]): ThresholdRecommendation[] {
    const autoRecords = records.filter((r) => r.selectionSource === "auto");
    const recommendations: ThresholdRecommendation[] = [];

    if (autoRecords.length < 10) {
      return recommendations;
    }

    // Group by complexity and check success rates
    const complexityGroups = new Map<
      string,
      { successes: number; total: number; stages: Set<string> }
    >();

    for (const record of autoRecords) {
      const complexity = record.autoSelectorComplexity ?? "unknown";
      const existing = complexityGroups.get(complexity) ?? {
        successes: 0,
        total: 0,
        stages: new Set<string>(),
      };
      existing.total++;
      if (record.success) existing.successes++;
      existing.stages.add(record.stage);
      complexityGroups.set(complexity, existing);
    }

    // Check if XS/S tasks routed to haiku have low success rate
    // → suggest increasing haiku_max threshold
    const xsGroup = complexityGroups.get("XS");
    const sGroup = complexityGroups.get("S");
    const lightTotal = (xsGroup?.total ?? 0) + (sGroup?.total ?? 0);
    const lightSuccesses = (xsGroup?.successes ?? 0) + (sGroup?.successes ?? 0);

    if (lightTotal >= 5) {
      const lightSuccessRate = lightSuccesses / lightTotal;
      if (lightSuccessRate < 0.7) {
        const affectedStages = [...(xsGroup?.stages ?? []), ...(sGroup?.stages ?? [])];
        recommendations.push({
          field: "complexity_thresholds.haiku_max",
          currentValue: 3,
          suggestedValue: 2,
          rationale: `XS/S tasks have ${(lightSuccessRate * 100).toFixed(0)}% success rate with current routing. Lowering haiku_max would route more tasks to sonnet.`,
          confidence: lightTotal >= 20 ? "high" : lightTotal >= 10 ? "medium" : "low",
          evidence: {
            sampleSize: lightTotal,
            affectedStages: [...new Set(affectedStages)],
          },
        });
      }
    }

    // Check if L/XL tasks routed to sonnet have low success rate
    // → suggest lowering sonnet_max threshold
    const lGroup = complexityGroups.get("L");
    const xlGroup = complexityGroups.get("XL");
    const heavyTotal = (lGroup?.total ?? 0) + (xlGroup?.total ?? 0);
    const heavySuccesses = (lGroup?.successes ?? 0) + (xlGroup?.successes ?? 0);

    if (heavyTotal >= 5) {
      const heavySuccessRate = heavySuccesses / heavyTotal;
      if (heavySuccessRate < 0.6) {
        const affectedStages = [...(lGroup?.stages ?? []), ...(xlGroup?.stages ?? [])];
        recommendations.push({
          field: "complexity_thresholds.sonnet_max",
          currentValue: 6,
          suggestedValue: 5,
          rationale: `L/XL tasks have ${(heavySuccessRate * 100).toFixed(0)}% success rate with current routing. Lowering sonnet_max would route more complex tasks to opus.`,
          confidence: heavyTotal >= 20 ? "high" : heavyTotal >= 10 ? "medium" : "low",
          evidence: {
            sampleSize: heavyTotal,
            affectedStages: [...new Set(affectedStages)],
          },
        });
      }
    }

    return recommendations;
  }

  /**
   * Convert experiment report data into routing recommendations.
   *
   * Uses `ab-comparison` when data is insufficient, or `downgrade`/`upgrade`
   * when the experiment has enough data and shows a clear winner.
   *
   * @see Issue #949 - A/B Testing Framework
   */
  consumeExperimentData(report: ExperimentReport): RoutingRecommendation[] {
    const recommendations: RoutingRecommendation[] = [];
    const controlModel = `experiment:${report.experiment_name}:control`;
    const treatmentModel = `experiment:${report.experiment_name}:treatment`;

    if (!report.sufficient_data) {
      recommendations.push({
        type: "ab-comparison",
        stage: "*",
        currentModel: controlModel,
        suggestedModel: treatmentModel,
        rationale:
          `Experiment "${report.experiment_name}" has insufficient data ` +
          `(${report.total_runs.control} control, ${report.total_runs.treatment} treatment runs). ` +
          `Continue experiment to gather more data.`,
        estimatedSavingsUsd: 0,
        confidence: "low",
        evidence: {
          currentSuccessRate: report.metrics.control.success_rate,
          suggestedSuccessRate: report.metrics.treatment.success_rate,
          currentEffectiveCost: report.metrics.control.avg_cost_usd,
          suggestedEffectiveCost: report.metrics.treatment.avg_cost_usd,
          sampleSizes: {
            [controlModel]: report.total_runs.control,
            [treatmentModel]: report.total_runs.treatment,
          },
        },
      });
      return recommendations;
    }

    const { comparison, metrics } = report;
    const savingsPerRun = metrics.control.avg_cost_usd - metrics.treatment.avg_cost_usd;

    if (comparison.success_rate_delta >= 0 && comparison.cost_savings_percent > 10) {
      recommendations.push({
        type: "downgrade",
        stage: "*",
        currentModel: controlModel,
        suggestedModel: treatmentModel,
        rationale: comparison.recommendation,
        estimatedSavingsUsd: Math.max(0, savingsPerRun),
        confidence:
          report.total_runs.control >= 50 && report.total_runs.treatment >= 50 ? "high" : "medium",
        evidence: {
          currentSuccessRate: metrics.control.success_rate,
          suggestedSuccessRate: metrics.treatment.success_rate,
          currentEffectiveCost: metrics.control.avg_cost_usd,
          suggestedEffectiveCost: metrics.treatment.avg_cost_usd,
          sampleSizes: {
            [controlModel]: report.total_runs.control,
            [treatmentModel]: report.total_runs.treatment,
          },
        },
      });
    } else if (comparison.success_rate_delta < -0.05) {
      recommendations.push({
        type: "upgrade",
        stage: "*",
        currentModel: treatmentModel,
        suggestedModel: controlModel,
        rationale: comparison.recommendation,
        estimatedSavingsUsd: 0,
        confidence: "medium",
        evidence: {
          currentSuccessRate: metrics.treatment.success_rate,
          suggestedSuccessRate: metrics.control.success_rate,
          currentEffectiveCost: metrics.treatment.avg_cost_usd,
          suggestedEffectiveCost: metrics.control.avg_cost_usd,
          sampleSizes: {
            [controlModel]: report.total_runs.control,
            [treatmentModel]: report.total_runs.treatment,
          },
        },
      });
    }

    return recommendations;
  }

  // --- Private helpers ---

  /**
   * Estimate cost savings of auto-selection vs using the static default (sonnet).
   */
  private estimateCostSavingsVsStatic(autoRecords: ExecutionHistoryRecord[]): number {
    const costRates = this.config.costRates ?? DEFAULT_MODEL_COST_RATES;
    const sonnetRate = costRates["sonnet"];
    if (!sonnetRate) return 0;

    let actualCost = 0;
    let hypotheticalSonnetCost = 0;

    for (const record of autoRecords) {
      actualCost += record.costUsd;
      // Estimate what sonnet would have cost for the same token usage
      const sonnetCost =
        (record.inputTokens * sonnetRate.inputPerMillion +
          record.outputTokens * sonnetRate.outputPerMillion) /
        1_000_000;
      hypotheticalSonnetCost += sonnetCost;
    }

    // Positive means auto-selection saved money
    return Math.round((hypotheticalSonnetCost - actualCost) * 10000) / 10000;
  }

  private computePerformance(
    modelId: ModelIdentifier,
    stage: string,
    records: ExecutionHistoryRecord[]
  ): ModelStagePerformance {
    const runs = records.length;
    const successes = records.filter((r) => r.success).length;
    const successRate = runs > 0 ? successes / runs : 0;

    const totalCost = records.reduce((sum, r) => sum + r.costUsd, 0);
    const effectiveCostPerSuccess = successes > 0 ? totalCost / successes : Infinity;

    const avgInputTokens = runs > 0 ? records.reduce((sum, r) => sum + r.inputTokens, 0) / runs : 0;
    const avgOutputTokens =
      runs > 0 ? records.reduce((sum, r) => sum + r.outputTokens, 0) / runs : 0;
    const avgCostUsd = runs > 0 ? totalCost / runs : 0;
    const avgDurationMs = runs > 0 ? records.reduce((sum, r) => sum + r.durationMs, 0) / runs : 0;
    const retryRate = runs > 0 ? records.reduce((sum, r) => sum + r.retries, 0) / runs : 0;

    // First attempt success = records where retries === 0 and success === true
    const firstAttemptSuccesses = records.filter((r) => r.retries === 0 && r.success).length;
    const firstAttemptSuccessRate = runs > 0 ? firstAttemptSuccesses / runs : 0;

    const timestamps = records.map((r) => r.timestamp).sort();
    const earliest = timestamps[0] ?? "";
    const latest = timestamps[timestamps.length - 1] ?? "";

    return {
      model: modelId,
      stage,
      runs,
      successRate,
      avgInputTokens,
      avgOutputTokens,
      avgCostUsd,
      avgDurationMs,
      retryRate,
      effectiveCostPerSuccess,
      qualityIndicators: {
        firstAttemptSuccessRate,
      },
      samplePeriod: { earliest, latest },
    };
  }

  private computeConfidence(models: ModelStagePerformance[]): "low" | "medium" | "high" {
    const minRuns = Math.min(...models.map((m) => m.runs));
    if (minRuns >= 50) return "high";
    if (minRuns >= 20) return "medium";
    return "low";
  }

  /**
   * Check if complexity-based routing would be beneficial.
   * Uses AutoModelSelector to determine if different complexity levels
   * would route to different models for this stage, suggesting dynamic
   * model selection when differentiation exists.
   */
  private checkComplexityBasedRouting(
    comparison: StageModelComparison,
    currentModel: ModelIdentifier,
    currentPerf: ModelStagePerformance,
    sufficientModels: ModelStagePerformance[],
    recommendations: RoutingRecommendation[]
  ): void {
    const selector = new AutoModelSelector();
    const complexityLevels: ComplexityLabel[] = ["XS", "S", "M", "L", "XL"];

    // Determine what model AutoModelSelector would pick for each complexity level
    const complexityToModel = new Map<ComplexityLabel, ModelTier>();
    for (const complexity of complexityLevels) {
      const result = selector.selectModel(comparison.stage, {
        labels: [],
        title: "",
        size: complexity,
      });
      complexityToModel.set(complexity, result.model);
    }

    // Check if there is model differentiation across complexity levels
    const uniqueModels = new Set(complexityToModel.values());
    if (uniqueModels.size < 2) {
      // All complexity levels map to the same model — no benefit from complexity-based routing
      return;
    }

    // Get the recommended model for medium complexity (the "default" case)
    const mediumModel = complexityToModel.get("M")!;

    // Check if the current model already matches the optimal routing for medium complexity
    // The currentModel format is "adapter:model" (e.g., "claude:sonnet"), so check if it ends with the tier
    const currentModelMatchesMedium = currentModel.toLowerCase().includes(mediumModel);
    if (currentModelMatchesMedium) {
      // Current model already matches the stage-aware recommendation for medium complexity.
      // No recommendation needed.
      return;
    }

    // Build the rationale describing the complexity-to-model mapping
    const mappingDescriptions: string[] = [];
    for (const [complexity, model] of complexityToModel) {
      mappingDescriptions.push(`${complexity}→${model}`);
    }

    recommendations.push({
      type: "complexity-based",
      stage: comparison.stage,
      currentModel,
      suggestedModel: "auto-select",
      rationale:
        `Stage '${comparison.stage}' benefits from complexity-based model routing. ` +
        `AutoModelSelector maps: ${mappingDescriptions.join(", ")}. ` +
        `Current default '${currentModel}' does not match optimal for medium complexity ('${mediumModel}'). ` +
        `Using dynamic selection would route simpler issues to cheaper models and complex issues to more capable ones.`,
      estimatedSavingsUsd: 0,
      confidence: "low",
      evidence: {
        currentSuccessRate: currentPerf.successRate,
        suggestedSuccessRate: currentPerf.successRate,
        currentEffectiveCost: currentPerf.effectiveCostPerSuccess,
        suggestedEffectiveCost: currentPerf.effectiveCostPerSuccess,
        sampleSizes: Object.fromEntries(sufficientModels.map((m) => [m.model, m.runs])),
      },
    });
  }
}
