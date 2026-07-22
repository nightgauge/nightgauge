/**
 * TokenEfficiencyAnalyzer - Identifies token waste patterns in pipeline execution history
 *
 * Analyzes ExecutionHistoryRecord[] to detect 5 categories of waste:
 * redundant file reads, oversized context, cache miss patterns,
 * tool call inefficiency, and context window underutilization.
 *
 * All analysis is deterministic — no AI interpretation. Follows the architecture's
 * deterministic vs probabilistic separation.
 *
 * @see types.ts for all interface definitions
 * @see #651 for feature requirements
 * @see #649 for ExecutionHistoryRecord schema (prerequisite)
 */

import type {
  ExecutionHistoryRecord,
  ExecutionHistoryRecordExtended,
  ModelCostRate,
  TokenEfficiencyAnalysis,
  TokenEfficiencyConfig,
  WasteCategory,
  WastePattern,
  WasteSeverity,
} from "./types.js";
import { DEFAULT_MODEL_COST_RATES } from "./types.js";

const WASTE_CATEGORIES: WasteCategory[] = [
  "redundant-file-reads",
  "oversized-context",
  "cache-miss-patterns",
  "tool-call-inefficiency",
  "context-window-utilization",
  "zero-change-run",
];

interface ResolvedThresholds {
  redundantReadMinOccurrences: number;
  oversizedContextPercentile: number;
  cacheMissRateThreshold: number;
  toolCallsPercentile: number;
  contextUtilizationMinimum: number;
}

const DEFAULT_THRESHOLDS: ResolvedThresholds = {
  redundantReadMinOccurrences: 3,
  oversizedContextPercentile: 90,
  cacheMissRateThreshold: 0.5,
  toolCallsPercentile: 90,
  contextUtilizationMinimum: 0.3,
};

const DEFAULT_MIN_SAMPLES = 5;

const DEFAULT_COST_RATE: ModelCostRate = {
  inputPerMillion: 3.0,
  outputPerMillion: 15.0,
  cacheReadPerMillion: 0.3,
};

/** Estimated tokens consumed per file read (average file context size) */
const ESTIMATED_TOKENS_PER_FILE_READ = 500;

/** Estimated token overhead per tool call (request/response framing) */
const ESTIMATED_TOKENS_PER_TOOL_CALL = 200;

/** Discount factor for context utilization savings (conservative estimate) */
const UTILIZATION_SAVINGS_FACTOR = 0.5;

/** Input-to-output ratio above which context over-injection is likely */
const HIGH_INPUT_OUTPUT_RATIO = 10;

/** Per-stage cost threshold above which a model is considered expensive */
const EXPENSIVE_MODEL_COST_THRESHOLD = 0.1;

export class TokenEfficiencyAnalyzer {
  private readonly thresholds: ResolvedThresholds;
  private readonly minSamplesForOutliers: number;
  private readonly defaultCostRate: ModelCostRate;
  private readonly costRates: Record<string, ModelCostRate>;
  private readonly dateRange?: { since?: string; until?: string };

  constructor(config?: Partial<TokenEfficiencyConfig>) {
    this.thresholds = {
      redundantReadMinOccurrences:
        config?.thresholds?.redundantReadMinOccurrences ??
        DEFAULT_THRESHOLDS.redundantReadMinOccurrences,
      oversizedContextPercentile:
        config?.thresholds?.oversizedContextPercentile ??
        DEFAULT_THRESHOLDS.oversizedContextPercentile,
      cacheMissRateThreshold:
        config?.thresholds?.cacheMissRateThreshold ?? DEFAULT_THRESHOLDS.cacheMissRateThreshold,
      toolCallsPercentile:
        config?.thresholds?.toolCallsPercentile ?? DEFAULT_THRESHOLDS.toolCallsPercentile,
      contextUtilizationMinimum:
        config?.thresholds?.contextUtilizationMinimum ??
        DEFAULT_THRESHOLDS.contextUtilizationMinimum,
    };
    this.minSamplesForOutliers = config?.minSamplesForOutliers ?? DEFAULT_MIN_SAMPLES;
    this.defaultCostRate = config?.defaultCostRate ?? DEFAULT_COST_RATE;
    this.costRates = { ...DEFAULT_MODEL_COST_RATES, ...config?.costRates };
    this.dateRange = config?.dateRange;
  }

  /**
   * Look up cost rates for a specific model, falling back to defaultCostRate.
   *
   * @param model - Model name (e.g. 'haiku', 'sonnet', 'opus')
   * @returns Cost rates for the model
   */
  getCostRateForModel(model?: string): ModelCostRate {
    if (model && this.costRates[model]) {
      return this.costRates[model];
    }
    return this.defaultCostRate;
  }

  /**
   * Main entry point: analyze records and produce a full token efficiency report.
   */
  analyze(records: ExecutionHistoryRecord[]): TokenEfficiencyAnalysis {
    const filtered = this.filterByDateRange(records);

    const patterns: WastePattern[] = [
      ...this.detectRedundantFileReads(filtered),
      ...this.detectOversizedContext(filtered),
      ...this.detectCacheMissPatterns(filtered),
      ...this.detectToolCallInefficiency(filtered),
      ...this.detectContextWindowUtilization(filtered),
      ...this.detectZeroChangeRuns(filtered),
    ];

    // Sort by estimated savings descending
    patterns.sort((a, b) => b.estimatedSavingsUsd - a.estimatedSavingsUsd);

    const totalWastedTokens = patterns.reduce((sum, p) => sum + p.wastedTokens, 0);
    const totalEstimatedSavingsUsd = patterns.reduce((sum, p) => sum + p.estimatedSavingsUsd, 0);

    const totalTokens = filtered.reduce((sum, r) => sum + r.inputTokens + r.outputTokens, 0);
    const overallEfficiencyScore =
      totalTokens > 0
        ? Math.max(0, Math.min(100, 100 - (totalWastedTokens / totalTokens) * 100))
        : 100;

    const categorySummary = this.buildCategorySummary(patterns);

    const topRecommendation =
      patterns.length > 0
        ? patterns[0].recommendation
        : "No waste patterns detected. Token usage appears efficient.";

    return {
      analyzedAt: new Date().toISOString(),
      recordsAnalyzed: filtered.length,
      wastePatterns: patterns,
      summary: {
        totalWastedTokens,
        totalEstimatedSavingsUsd,
        categorySummary,
        overallEfficiencyScore,
        topRecommendation,
      },
    };
  }

  /**
   * Category 1: Detect files read repeatedly across stages in the same run.
   */
  detectRedundantFileReads(records: ExecutionHistoryRecord[]): WastePattern[] {
    const extended = records.filter(
      (r): r is ExecutionHistoryRecordExtended =>
        "filesRead" in r && Array.isArray((r as ExecutionHistoryRecordExtended).filesRead)
    );

    if (extended.length === 0) return [];

    // Group by issueNumber (same pipeline run)
    const runs = new Map<number, ExecutionHistoryRecordExtended[]>();
    for (const r of extended) {
      const existing = runs.get(r.issueNumber) ?? [];
      existing.push(r);
      runs.set(r.issueNumber, existing);
    }

    const patterns: WastePattern[] = [];

    for (const [issueNumber, runRecords] of runs) {
      // Count file occurrences across stages
      const fileCounts = new Map<string, Set<string>>();
      for (const record of runRecords) {
        for (const file of record.filesRead ?? []) {
          const stages = fileCounts.get(file) ?? new Set();
          stages.add(record.stage);
          fileCounts.set(file, stages);
        }
      }

      // Find files appearing in threshold+ stages
      const threshold = this.thresholds.redundantReadMinOccurrences;
      const redundantFiles: Array<{
        file: string;
        stageCount: number;
        stages: string[];
      }> = [];
      for (const [file, stages] of fileCounts) {
        if (stages.size >= threshold) {
          redundantFiles.push({
            file,
            stageCount: stages.size,
            stages: Array.from(stages),
          });
        }
      }

      if (redundantFiles.length === 0) continue;

      const totalRedundantReads = redundantFiles.reduce((sum, f) => sum + (f.stageCount - 1), 0);
      const wastedTokens = totalRedundantReads * ESTIMATED_TOKENS_PER_FILE_READ;
      const savingsUsd = this.tokensToUsd(wastedTokens, "input");
      const affectedStages = [...new Set(redundantFiles.flatMap((f) => f.stages))];

      patterns.push({
        category: "redundant-file-reads",
        severity: TokenEfficiencyAnalyzer.classifySeverity(
          wastedTokens /
            Math.max(
              1,
              runRecords.reduce((s, r) => s + r.inputTokens, 0)
            ),
          savingsUsd
        ),
        title: `Redundant file reads in run #${issueNumber}`,
        description: `${redundantFiles.length} file(s) read in ${threshold}+ stages within the same pipeline run.`,
        affectedStages,
        wastedTokens,
        estimatedSavingsUsd: savingsUsd,
        recommendation:
          "Cache frequently-read files in context handoff to avoid re-reading across stages.",
        evidence: {
          issueNumber,
          redundantFiles: redundantFiles.map((f) => ({
            file: f.file,
            stageCount: f.stageCount,
            stages: f.stages,
          })),
        },
        action: {
          type: "config-patch",
          configPath: "pipeline.skip_checks.lint",
          suggestedValue: false,
          label: "Enable context isolation",
        },
      });
    }

    return patterns;
  }

  /**
   * Category 2: Flag stages with token usage above the Nth percentile.
   */
  detectOversizedContext(records: ExecutionHistoryRecord[]): WastePattern[] {
    // Group by stage
    const stageGroups = new Map<string, ExecutionHistoryRecord[]>();
    for (const r of records) {
      const existing = stageGroups.get(r.stage) ?? [];
      existing.push(r);
      stageGroups.set(r.stage, existing);
    }

    const patterns: WastePattern[] = [];

    for (const [stage, stageRecords] of stageGroups) {
      if (stageRecords.length < this.minSamplesForOutliers) continue;

      const inputTokens = stageRecords.map((r) => r.inputTokens);
      const threshold = TokenEfficiencyAnalyzer.percentile(
        inputTokens,
        this.thresholds.oversizedContextPercentile
      );
      const median = TokenEfficiencyAnalyzer.percentile(inputTokens, 50);

      const outliers = stageRecords.filter((r) => r.inputTokens > threshold);
      if (outliers.length === 0) continue;

      // Guard: skip patterns with zero median (meaningless recommendations)
      if (median === 0) continue;

      for (const outlier of outliers) {
        const wastedTokens = Math.max(0, outlier.inputTokens - median);
        const savingsUsd = this.tokensToUsd(wastedTokens, "input");

        // Cross-check: high input with low output suggests over-injection
        const inputOutputRatio =
          outlier.outputTokens > 0 ? outlier.inputTokens / outlier.outputTokens : Infinity;

        // Include context file size when available (Issue #1009)
        const contextFileSizeBytes = outlier.contextFileSizeBytes;

        patterns.push({
          category: "oversized-context",
          severity: TokenEfficiencyAnalyzer.classifySeverity(
            wastedTokens / Math.max(1, outlier.inputTokens),
            savingsUsd
          ),
          title: `Fresh input token spike in ${stage}`,
          description: `Fresh (uncached) input tokens (${outlier.inputTokens}) exceed ${this.thresholds.oversizedContextPercentile}th percentile (${Math.round(threshold)}) for stage ${stage}. Median is ${Math.round(median)} tokens. This likely indicates a cache-cold run or a larger-than-usual context file being injected without cache coverage.`,
          affectedStages: [stage],
          wastedTokens,
          estimatedSavingsUsd: savingsUsd,
          recommendation:
            inputOutputRatio > HIGH_INPUT_OUTPUT_RATIO
              ? "High fresh-input-to-output ratio suggests the cache was cold or a large file was injected without cache coverage. Check whether the context file for this stage grew significantly."
              : "This run had more uncached content than usual. Check context file size or whether the prompt cache was warm.",
          evidence: {
            issueNumber: outlier.issueNumber,
            inputTokens: outlier.inputTokens,
            outputTokens: outlier.outputTokens,
            threshold: Math.round(threshold),
            median: Math.round(median),
            inputOutputRatio: Math.round(inputOutputRatio * 10) / 10,
            ...(contextFileSizeBytes !== undefined ? { contextFileSizeBytes } : {}),
          },
          action: {
            type: "config-patch",
            configPath: `pipeline.context_budgets.stage_limits.${stage}`,
            suggestedValue: Math.round(median),
            label: `Set fresh input token alert threshold at ${Math.round(median)} tokens for ${stage}`,
          },
        });
      }
    }

    return patterns;
  }

  /**
   * Category 3: Identify runs with low cache hit rates.
   */
  detectCacheMissPatterns(records: ExecutionHistoryRecord[]): WastePattern[] {
    // Only consider records that have cache data
    const withCache = records.filter(
      (r) => r.cacheReadTokens !== undefined && r.cacheReadTokens !== null
    );

    if (withCache.length === 0) return [];

    // Group by stage
    const stageGroups = new Map<string, ExecutionHistoryRecord[]>();
    for (const r of withCache) {
      const existing = stageGroups.get(r.stage) ?? [];
      existing.push(r);
      stageGroups.set(r.stage, existing);
    }

    const patterns: WastePattern[] = [];
    const threshold = this.thresholds.cacheMissRateThreshold;

    for (const [stage, stageRecords] of stageGroups) {
      const totalCacheRead = stageRecords.reduce((sum, r) => sum + (r.cacheReadTokens ?? 0), 0);
      const totalInput = stageRecords.reduce((sum, r) => sum + r.inputTokens, 0);
      const totalWithCacheInput = totalCacheRead + totalInput;
      if (totalWithCacheInput === 0) continue;

      const cacheHitRate = totalCacheRead / totalWithCacheInput;

      if (cacheHitRate >= threshold) continue;

      // Estimate savings: if cache hit rate were at threshold, how many tokens saved?
      const potentialCacheReads = totalWithCacheInput * threshold;
      const additionalCacheReads = Math.max(0, potentialCacheReads - totalCacheRead);
      // Savings = tokens that could be cache reads instead of full input
      const inputRate = this.getCostPerToken("input");
      const cacheRate = this.getCostPerToken("cache_read");
      const savingsUsd = additionalCacheReads * (inputRate - cacheRate);

      const wastedTokens = Math.round(additionalCacheReads);

      patterns.push({
        category: "cache-miss-patterns",
        severity: TokenEfficiencyAnalyzer.classifySeverity(1 - cacheHitRate, savingsUsd),
        title: `Low cache hit rate for ${stage}`,
        description: `Cache hit rate is ${(cacheHitRate * 100).toFixed(1)}% (threshold: ${(threshold * 100).toFixed(0)}%) across ${stageRecords.length} records.`,
        affectedStages: [stage],
        wastedTokens,
        estimatedSavingsUsd: savingsUsd,
        recommendation:
          "Improve prompt caching by using stable system prompts and keeping context structure consistent across runs.",
        evidence: {
          cacheHitRate: Math.round(cacheHitRate * 1000) / 1000,
          totalCacheReadTokens: totalCacheRead,
          totalInputTokens: totalInput,
          recordCount: stageRecords.length,
        },
        action: {
          type: "config-patch",
          configPath: "pipeline.auto_fix",
          suggestedValue: true,
          label: "Enable aggressive caching",
        },
      });
    }

    return patterns;
  }

  /**
   * Category 4: Flag stages with abnormally high tool call counts.
   *
   * When records contain `toolCallDetails` (from JSONL v2), also computes
   * per-tool breakdown including failure rates and highest-duration tools.
   */
  detectToolCallInefficiency(records: ExecutionHistoryRecord[]): WastePattern[] {
    const extended = records.filter(
      (r): r is ExecutionHistoryRecordExtended =>
        "toolCalls" in r && typeof (r as ExecutionHistoryRecordExtended).toolCalls === "number"
    );

    if (extended.length === 0) return [];

    // Group by stage
    const stageGroups = new Map<string, ExecutionHistoryRecordExtended[]>();
    for (const r of extended) {
      const existing = stageGroups.get(r.stage) ?? [];
      existing.push(r);
      stageGroups.set(r.stage, existing);
    }

    const patterns: WastePattern[] = [];

    for (const [stage, stageRecords] of stageGroups) {
      if (stageRecords.length < this.minSamplesForOutliers) continue;

      const toolCalls = stageRecords.map((r) => r.toolCalls!);
      const threshold = TokenEfficiencyAnalyzer.percentile(
        toolCalls,
        this.thresholds.toolCallsPercentile
      );
      const median = TokenEfficiencyAnalyzer.percentile(toolCalls, 50);

      const outliers = stageRecords.filter((r) => r.toolCalls! > threshold);
      if (outliers.length === 0) continue;

      // Guard: skip patterns with zero median (meaningless recommendations)
      if (median === 0) continue;

      for (const outlier of outliers) {
        // High tool calls AND high output tokens suggests exploratory/retrying behavior
        const excessToolCalls = outlier.toolCalls! - median;
        const wastedTokens = Math.round(excessToolCalls * ESTIMATED_TOKENS_PER_TOOL_CALL);
        const savingsUsd = this.tokensToUsd(wastedTokens, "output");

        // Build per-tool breakdown when detailed records available (Issue #1004)
        const toolBreakdown = this.buildToolBreakdown(outlier);

        patterns.push({
          category: "tool-call-inefficiency",
          severity: TokenEfficiencyAnalyzer.classifySeverity(
            excessToolCalls / Math.max(1, outlier.toolCalls!),
            savingsUsd
          ),
          title: `Excessive tool calls in ${stage}`,
          description: `Tool calls (${outlier.toolCalls}) exceed ${this.thresholds.toolCallsPercentile}th percentile (${Math.round(threshold)}) for stage ${stage}. Median is ${Math.round(median)}.`,
          affectedStages: [stage],
          wastedTokens,
          estimatedSavingsUsd: savingsUsd,
          recommendation:
            outlier.outputTokens >
            TokenEfficiencyAnalyzer.percentile(
              stageRecords.map((r) => r.outputTokens),
              this.thresholds.toolCallsPercentile
            )
              ? "High tool calls with high output suggests exploratory behavior. Consider more targeted tool usage."
              : "Reduce unnecessary tool calls by batching operations or using more specific queries.",
          evidence: {
            issueNumber: outlier.issueNumber,
            toolCalls: outlier.toolCalls,
            outputTokens: outlier.outputTokens,
            threshold: Math.round(threshold),
            median: Math.round(median),
            ...(toolBreakdown ? { toolBreakdown } : {}),
          },
          action: {
            type: "info-only",
            configPath: "",
            suggestedValue: null,
            label: "Review tool call patterns",
          },
        });
      }
    }

    return patterns;
  }

  /**
   * Category 5: Flag stages using very little of available context window.
   */
  detectContextWindowUtilization(records: ExecutionHistoryRecord[]): WastePattern[] {
    const extended = records.filter(
      (r): r is ExecutionHistoryRecordExtended =>
        "contextWindowUtilization" in r &&
        typeof (r as ExecutionHistoryRecordExtended).contextWindowUtilization === "number"
    );

    if (extended.length === 0) return [];

    // Group by stage
    const stageGroups = new Map<string, ExecutionHistoryRecordExtended[]>();
    for (const r of extended) {
      const existing = stageGroups.get(r.stage) ?? [];
      existing.push(r);
      stageGroups.set(r.stage, existing);
    }

    const patterns: WastePattern[] = [];
    const minUtilization = this.thresholds.contextUtilizationMinimum;

    for (const [stage, stageRecords] of stageGroups) {
      const avgUtilization =
        stageRecords.reduce((sum, r) => sum + (r.contextWindowUtilization ?? 0), 0) /
        stageRecords.length;

      if (avgUtilization >= minUtilization) continue;

      // Low utilization on expensive models is higher severity
      const avgCost = stageRecords.reduce((sum, r) => sum + r.costUsd, 0) / stageRecords.length;

      // Waste = using an expensive model slot when a cheaper one would suffice
      // Estimated savings: if avg utilization is 15% on a $0.50 stage,
      // a proportionally cheaper model could save ~50%
      const potentialSavingsRatio = 1 - avgUtilization / minUtilization;
      const totalCost = stageRecords.reduce((sum, r) => sum + r.costUsd, 0);
      const savingsUsd =
        totalCost * Math.max(0, potentialSavingsRatio) * UTILIZATION_SAVINGS_FACTOR;
      const wastedTokens = Math.round(
        stageRecords.reduce((sum, r) => sum + r.inputTokens, 0) * (1 - avgUtilization)
      );

      patterns.push({
        category: "context-window-utilization",
        severity: TokenEfficiencyAnalyzer.classifySeverity(1 - avgUtilization, savingsUsd),
        title: `Low context window utilization in ${stage}`,
        description: `Average context window utilization is ${(avgUtilization * 100).toFixed(1)}% (minimum: ${(minUtilization * 100).toFixed(0)}%) across ${stageRecords.length} records.`,
        affectedStages: [stage],
        wastedTokens,
        estimatedSavingsUsd: savingsUsd,
        recommendation:
          avgCost > EXPENSIVE_MODEL_COST_THRESHOLD
            ? "Low utilization on an expensive model. Consider using a smaller, cheaper model for this stage."
            : "Low context utilization detected. This stage may benefit from a smaller model.",
        evidence: {
          avgUtilization: Math.round(avgUtilization * 1000) / 1000,
          avgCostUsd: Math.round(avgCost * 10000) / 10000,
          recordCount: stageRecords.length,
        },
        action: {
          type: "info-only",
          configPath: "",
          suggestedValue: null,
          label: "Review model selection",
        },
      });
    }

    return patterns;
  }

  /**
   * Category 6: Flag pipeline runs with zero file changes (Issue #709)
   *
   * Detects runs where outcomeType is 'verify-and-close' or 'already-resolved',
   * meaning the entire pipeline cost was spent without producing any code changes.
   */
  detectZeroChangeRuns(records: ExecutionHistoryRecord[]): WastePattern[] {
    const zeroChangeRecords = records.filter(
      (r) => r.outcomeType === "verify-and-close" || r.outcomeType === "already-resolved"
    );

    if (zeroChangeRecords.length === 0) return [];

    // Group by issueNumber to produce one pattern per pipeline run
    const runs = new Map<number, ExecutionHistoryRecord[]>();
    for (const r of zeroChangeRecords) {
      const existing = runs.get(r.issueNumber) ?? [];
      existing.push(r);
      runs.set(r.issueNumber, existing);
    }

    const patterns: WastePattern[] = [];

    for (const [issueNumber, runRecords] of runs) {
      const totalTokens = runRecords.reduce((sum, r) => sum + r.inputTokens + r.outputTokens, 0);
      const totalCostUsd = runRecords.reduce((sum, r) => sum + r.costUsd, 0);
      const affectedStages = [...new Set(runRecords.map((r) => r.stage))];
      const outcomeType = runRecords[0].outcomeType!;

      patterns.push({
        category: "zero-change-run",
        severity: TokenEfficiencyAnalyzer.classifySeverity(1.0, totalCostUsd),
        title: `Zero-change pipeline run #${issueNumber}`,
        description:
          outcomeType === "already-resolved"
            ? `Pipeline run #${issueNumber} was already resolved — all tokens wasted.`
            : `Pipeline run #${issueNumber} produced zero file changes — all tokens wasted.`,
        affectedStages,
        wastedTokens: totalTokens,
        estimatedSavingsUsd: totalCostUsd,
        recommendation:
          outcomeType === "already-resolved"
            ? "Add pre-pipeline checks to detect already-resolved issues before spending tokens."
            : "Consider adding early exit detection for issues that require no code changes.",
        evidence: {
          issueNumber,
          outcomeType,
          totalCostUsd: Math.round(totalCostUsd * 10000) / 10000,
          stageCount: runRecords.length,
        },
        action: {
          type: "config-patch",
          configPath: "pipeline.skip_checks.build",
          suggestedValue: false,
          label: "Enable zero-change detection",
        },
      });
    }

    return patterns;
  }

  /**
   * Filter records by outcome type (Issue #709)
   *
   * Returns only records matching the specified outcome types.
   * Useful for isolating productive vs wasteful runs in analysis.
   */
  filterByOutcomeType(
    records: ExecutionHistoryRecord[],
    outcomeTypes: Array<"productive" | "verify-and-close" | "already-resolved">
  ): ExecutionHistoryRecord[] {
    return records.filter(
      (r) => r.outcomeType !== undefined && outcomeTypes.includes(r.outcomeType)
    );
  }

  /**
   * Compute the Nth percentile of a numeric array.
   * Returns 0 for empty arrays.
   */
  static percentile(values: number[], p: number): number {
    if (values.length === 0) return 0;
    const sorted = [...values].sort((a, b) => a - b);
    const index = (p / 100) * (sorted.length - 1);
    const lower = Math.floor(index);
    const upper = Math.ceil(index);
    if (lower === upper) return sorted[lower];
    const weight = index - lower;
    return sorted[lower] * (1 - weight) + sorted[upper] * weight;
  }

  /**
   * Classify severity from waste ratio and USD savings.
   * Matches /pipeline-audit severity levels.
   */
  static classifySeverity(wasteRatio: number, savingsUsd: number): WasteSeverity {
    if (wasteRatio > 0.5 || savingsUsd > 1.0) return "critical";
    if (wasteRatio > 0.25 || savingsUsd > 0.25) return "high";
    if (wasteRatio > 0.1 || savingsUsd > 0.1) return "medium";
    if (wasteRatio > 0 || savingsUsd > 0) return "low";
    return "info";
  }

  // --- Private helpers ---

  /**
   * Build per-tool breakdown from detailed tool call records (Issue #1004).
   * Returns null when toolCallDetails is not available (older records).
   */
  private buildToolBreakdown(
    record: ExecutionHistoryRecordExtended
  ): Record<string, { count: number; failures: number; totalDurationMs: number }> | null {
    if (!record.toolCallDetails || record.toolCallDetails.length === 0) {
      return null;
    }

    const breakdown: Record<string, { count: number; failures: number; totalDurationMs: number }> =
      {};

    for (const tc of record.toolCallDetails) {
      if (!breakdown[tc.tool]) {
        breakdown[tc.tool] = { count: 0, failures: 0, totalDurationMs: 0 };
      }
      breakdown[tc.tool].count++;
      if (tc.error) breakdown[tc.tool].failures++;
      if (tc.duration_ms) breakdown[tc.tool].totalDurationMs += tc.duration_ms;
    }

    return breakdown;
  }

  private filterByDateRange(records: ExecutionHistoryRecord[]): ExecutionHistoryRecord[] {
    const since = this.dateRange?.since;
    const until = this.dateRange?.until;
    if (!since && !until) return records;

    const sinceMs = since ? new Date(since).getTime() : -Infinity;
    const untilMs = until ? new Date(until).getTime() : Infinity;

    return records.filter((r) => {
      const ts = new Date(r.timestamp).getTime();
      return ts >= sinceMs && ts <= untilMs;
    });
  }

  private buildCategorySummary(
    patterns: WastePattern[]
  ): Record<
    WasteCategory,
    { patternCount: number; totalWastedTokens: number; totalSavingsUsd: number }
  > {
    const summary = {} as Record<
      WasteCategory,
      {
        patternCount: number;
        totalWastedTokens: number;
        totalSavingsUsd: number;
      }
    >;

    for (const cat of WASTE_CATEGORIES) {
      summary[cat] = {
        patternCount: 0,
        totalWastedTokens: 0,
        totalSavingsUsd: 0,
      };
    }

    for (const p of patterns) {
      summary[p.category].patternCount++;
      summary[p.category].totalWastedTokens += p.wastedTokens;
      summary[p.category].totalSavingsUsd += p.estimatedSavingsUsd;
    }

    return summary;
  }

  private tokensToUsd(tokens: number, type: "input" | "output" | "cache_read"): number {
    const rate = this.getCostPerToken(type);
    return tokens * rate;
  }

  private getCostPerToken(type: "input" | "output" | "cache_read"): number {
    const costRate = this.defaultCostRate;
    switch (type) {
      case "input":
        return costRate.inputPerMillion / 1_000_000;
      case "output":
        return costRate.outputPerMillion / 1_000_000;
      case "cache_read":
        return (costRate.cacheReadPerMillion ?? 0.3) / 1_000_000;
    }
  }
}
