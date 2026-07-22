/**
 * CostSummaryCalculator - Pure function module for pipeline cost analysis
 *
 * Calculates per-stage cost breakdown with model attribution,
 * hypothetical "default model" comparison, savings percentage,
 * and historical cost trends.
 *
 * Pure functions with no side effects — easy to test and compose.
 *
 * @see Issue #945 - Per-Pipeline Cost Summary with Model-Per-Stage Breakdown
 */

import type { PipelineStage } from "@nightgauge/sdk";
import type { ModelCostRate } from "@nightgauge/sdk/dist/analysis/types";
import { DEFAULT_MODEL_COST_RATES } from "@nightgauge/sdk/dist/analysis/types";
import type { PipelineRunSummary, StageTokenUsage } from "./DashboardState";
import type { PerformanceMode } from "../../utils/modeProfiles";
import type { SizeAwareBudget } from "../../utils/budgetEnforcer";

/** Mode filter literal — `"all"` or any concrete performance mode (Issue #3218). */
export type ModeFilter = PerformanceMode | "all";

/**
 * Per-stage cost breakdown with model attribution
 */
export interface StageCostBreakdown {
  stage: PipelineStage;
  model: string;
  effortLevel: string;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  costUsd: number;
  percentOfTotal: number;
}

/**
 * Pipeline cost summary with hypothetical comparison
 */
export interface CostSummary {
  totalCostUsd: number;
  stages: StageCostBreakdown[];
  hypotheticalDefaultCostUsd: number;
  defaultModel: string;
  savingsUsd: number;
  savingsPercent: number;
  routingMode: string;
}

/**
 * Historical cost entry for trend visualization
 */
export interface CostHistoryEntry {
  issueNumber: number;
  costUsd: number;
  stageCount: number;
  timestamp: Date;
}

/**
 * Model selection data from execution history or state.json
 */
export interface StageModelInfo {
  stage: PipelineStage;
  model: string;
  effort?: string;
  source: "history" | "state" | "fallback";
}

const DEFAULT_MODEL = "sonnet";

/**
 * Calculate pipeline cost summary with per-stage model attribution
 *
 * @param run - Pipeline run summary with stage token data
 * @param stageModels - Model info per stage (from execution history or state.json)
 * @param costRates - Model cost rates (defaults to SDK rates)
 * @param defaultModel - Hypothetical comparison model (defaults to 'sonnet')
 * @param modeFilter - Optional performance-mode filter (Issue #3218). When set
 *   to a concrete mode, stages whose `performance_mode` does not match are
 *   excluded from the summary. `"all"` (default) preserves the prior behavior.
 *   Stages without a recorded `performance_mode` are excluded from filtered
 *   summaries (ADR-004) and included only when filter is `"all"` or undefined.
 * @returns Cost summary or null if no token data available
 */
export function calculateCostSummary(
  run: PipelineRunSummary,
  stageModels: StageModelInfo[],
  costRates: Record<string, ModelCostRate> = DEFAULT_MODEL_COST_RATES,
  defaultModel: string = DEFAULT_MODEL,
  modeFilter?: ModeFilter
): CostSummary | null {
  // Build model lookup map
  const modelMap = new Map<PipelineStage, StageModelInfo>();
  for (const sm of stageModels) {
    modelMap.set(sm.stage, sm);
  }

  // Collect stages with token data
  let stagesWithTokens = run.stages.filter((s) => s.tokenUsage);

  // Per-mode filtering (Issue #3218). When filter is set to a concrete mode,
  // exclude stages whose stage-level performance_mode is missing or different
  // (ADR-004 — pre-#3215 records lack the field and aggregate to "unknown").
  if (modeFilter && modeFilter !== "all") {
    stagesWithTokens = stagesWithTokens.filter((s) => s.performance_mode === modeFilter);
  }

  if (stagesWithTokens.length === 0) return null;

  // Calculate per-stage breakdown
  const stages: StageCostBreakdown[] = [];
  let totalCostUsd = 0;
  let hypotheticalCostUsd = 0;

  for (const stageProgress of stagesWithTokens) {
    const usage = stageProgress.tokenUsage!;
    const modelInfo = modelMap.get(stageProgress.stage);
    const model = modelInfo?.model ?? defaultModel;
    const effort = modelInfo?.effort ?? "medium";

    // Use actual cost from token tracking
    const actualCost = usage.costUsd;
    totalCostUsd += actualCost;

    // Calculate hypothetical cost with default model
    const defaultRates = costRates[defaultModel] ?? costRates["sonnet"];
    if (defaultRates) {
      hypotheticalCostUsd += calculateTokenCost(usage, defaultRates);
    } else {
      hypotheticalCostUsd += actualCost;
    }

    stages.push({
      stage: stageProgress.stage,
      model,
      effortLevel: effort,
      inputTokens: usage.inputTokens,
      outputTokens: usage.outputTokens,
      cacheReadTokens: usage.cacheReadTokens,
      cacheCreationTokens: usage.cacheCreationTokens,
      costUsd: actualCost,
      percentOfTotal: 0, // calculated below
    });
  }

  // Calculate percentages
  for (const stage of stages) {
    stage.percentOfTotal = totalCostUsd > 0 ? (stage.costUsd / totalCostUsd) * 100 : 0;
  }

  // Calculate savings
  const savingsUsd = hypotheticalCostUsd - totalCostUsd;
  const savingsPercent = hypotheticalCostUsd > 0 ? (savingsUsd / hypotheticalCostUsd) * 100 : 0;

  // Determine routing mode from run data
  const routingMode = inferRoutingMode(stageModels);

  return {
    totalCostUsd,
    stages,
    hypotheticalDefaultCostUsd: hypotheticalCostUsd,
    defaultModel,
    savingsUsd: Math.max(0, savingsUsd),
    savingsPercent: Math.max(0, savingsPercent),
    routingMode,
  };
}

/**
 * Calculate cost history from completed runs
 *
 * @param runs - Pipeline run history (most recent first)
 * @param limit - Maximum entries to return
 * @returns Array of cost history entries (oldest first for charting)
 */
export function calculateCostHistory(
  runs: PipelineRunSummary[],
  limit: number = 10
): CostHistoryEntry[] {
  const completedRuns = runs
    .filter((r) => r.status === "complete" && r.usage.costUsd > 0)
    .slice(0, limit);

  return completedRuns
    .map((run) => ({
      issueNumber: run.issueNumber,
      costUsd: run.usage.costUsd,
      stageCount: run.usage.stageCount,
      timestamp: run.completedAt ?? run.startedAt,
    }))
    .reverse(); // oldest first for charting
}

/** Per-stage, per-execution-path budget vs actual stats (Issue #3269). */
export interface BudgetVsActualStageStat {
  stage: string;
  executionPath: "deterministic" | "llm" | "unknown";
  sampleCount: number;
  p50CostUsd: number;
  p90CostUsd: number;
  /** Configured cap for size=M (most common) */
  capUsd: number;
  /** p90 / cap. NaN when cap=0 (unlimited) */
  ratioToCap: number;
  /** Whether cap is more than 2× p90 (over-provisioned) */
  isOverProvisioned: boolean;
}

/**
 * Compute per-stage, per-execution-path budget vs actual stats.
 *
 * Buckets each stage's cost by (stage, execution_path ?? "unknown"), computes
 * p50/p90, and evaluates provisioning relative to the configured M-size cap.
 * Stages with fewer than 3 samples are excluded.
 *
 * @see Issue #3269 - Re-baseline pipeline budget caps
 */
export function computeBudgetVsActual(
  runs: PipelineRunSummary[],
  budgets: Record<string, SizeAwareBudget>
): BudgetVsActualStageStat[] {
  // bucket: key = "stage::path"
  const buckets = new Map<string, number[]>();

  for (const run of runs) {
    for (const stage of run.stages) {
      const cost = stage.tokenUsage?.costUsd ?? null;
      if (cost === null) continue;
      const path: BudgetVsActualStageStat["executionPath"] =
        stage.execution_path === "deterministic"
          ? "deterministic"
          : stage.execution_path === "llm"
            ? "llm"
            : "unknown";
      const key = `${stage.stage}::${path}`;
      let arr = buckets.get(key);
      if (!arr) {
        arr = [];
        buckets.set(key, arr);
      }
      arr.push(cost);
    }
  }

  const result: BudgetVsActualStageStat[] = [];
  for (const [key, costs] of buckets.entries()) {
    if (costs.length < 3) continue;
    const [stageName, path] = key.split("::") as [string, BudgetVsActualStageStat["executionPath"]];
    const sorted = [...costs].sort((a, b) => a - b);
    const p50 = percentile(sorted, 0.5);
    const p90 = percentile(sorted, 0.9);
    const capUsd = budgets[stageName]?.M ?? 0;
    const ratioToCap = capUsd > 0 ? p90 / capUsd : NaN;
    result.push({
      stage: stageName,
      executionPath: path,
      sampleCount: costs.length,
      p50CostUsd: p50,
      p90CostUsd: p90,
      capUsd,
      ratioToCap,
      isOverProvisioned: capUsd > 0 && capUsd > 2 * p90,
    });
  }

  // Sort: stage alphabetical, then path
  result.sort(
    (a, b) => a.stage.localeCompare(b.stage) || a.executionPath.localeCompare(b.executionPath)
  );
  return result;
}

/**
 * Per-stage p50/p95 cost summary within a single performance-mode bucket.
 */
export interface PerModeStageStat {
  stage: PipelineStage;
  p50CostUsd: number;
  p95CostUsd: number;
  sampleCount: number;
}

/**
 * Aggregated cost rollup for a single performance-mode bucket (Issue #3218).
 */
export interface PerModeCostBucket {
  totalCostUsd: number;
  perStageP50Usd: PerModeStageStat[];
  perStageP95Usd: PerModeStageStat[];
  runCount: number;
  /** Number of stage rows excluded because their `performance_mode` was missing. */
  excludedUnknownStageCount: number;
}

/**
 * Per-mode cost rollup keyed by `PerformanceMode` (Issue #3218).
 *
 * Stages without a recorded `performance_mode` are excluded from the three
 * concrete-mode buckets (ADR-004) — they are counted in
 * `excludedUnknownStageCount` per bucket so the UI can disclose the gap.
 */
export type PerModeCostRollup = Record<PerformanceMode, PerModeCostBucket>;

/**
 * Compute the percentile of a sorted numeric array using linear interpolation.
 * Returns 0 for an empty array. `percentile` is in [0, 1].
 */
function percentile(sortedValues: number[], p: number): number {
  if (sortedValues.length === 0) return 0;
  if (sortedValues.length === 1) return sortedValues[0];
  const index = (sortedValues.length - 1) * p;
  const lower = Math.floor(index);
  const upper = Math.ceil(index);
  if (lower === upper) return sortedValues[lower];
  const weight = index - lower;
  return sortedValues[lower] * (1 - weight) + sortedValues[upper] * weight;
}

/**
 * Group runs into per-mode buckets and compute total cost, p50/p95 per-stage,
 * and run counts (Issue #3218).
 *
 * Mode attribution uses each stage's `performance_mode` (set per-stage from
 * `HistoryStageDetail.performance_mode`). Stages lacking the field are
 * excluded from the three concrete buckets and counted in
 * `excludedUnknownStageCount`. A run contributes to a bucket's `runCount` if at
 * least one of its stages with token data is attributed to that bucket.
 *
 * @param runs - Runs to aggregate (typically `state.getHistory()`)
 * @param _stageModels - Model info per stage (currently unused — reserved for
 *   future per-model extension; kept to preserve the public signature).
 * @param _costRates - Model cost rates (reserved for future).
 * @param _defaultModel - Hypothetical comparison model (reserved for future).
 */
export function calculatePerModeCostRollup(
  runs: PipelineRunSummary[],
  _stageModels: StageModelInfo[] = [],
  _costRates: Record<string, ModelCostRate> = DEFAULT_MODEL_COST_RATES,
  _defaultModel: string = DEFAULT_MODEL
): PerModeCostRollup {
  const modes: PerformanceMode[] = ["efficiency", "elevated", "maximum", "frontier"];

  const stageCosts: Record<PerformanceMode, Map<PipelineStage, number[]>> = {
    efficiency: new Map(),
    elevated: new Map(),
    maximum: new Map(),
    frontier: new Map(),
  };
  const totals: Record<PerformanceMode, number> = {
    efficiency: 0,
    elevated: 0,
    maximum: 0,
    frontier: 0,
  };
  const runsContributing: Record<PerformanceMode, Set<number>> = {
    efficiency: new Set(),
    elevated: new Set(),
    maximum: new Set(),
    frontier: new Set(),
  };
  const excludedUnknown: Record<PerformanceMode, number> = {
    efficiency: 0,
    elevated: 0,
    maximum: 0,
    frontier: 0,
  };

  for (const run of runs) {
    const stagesWithTokens = run.stages.filter((s) => s.tokenUsage);
    let unknownInThisRun = 0;
    for (const stage of stagesWithTokens) {
      const mode = stage.performance_mode;
      const cost = stage.tokenUsage!.costUsd;
      if (!mode) {
        unknownInThisRun += 1;
        continue;
      }
      let stageMap = stageCosts[mode].get(stage.stage);
      if (!stageMap) {
        stageMap = [];
        stageCosts[mode].set(stage.stage, stageMap);
      }
      stageMap.push(cost);
      totals[mode] += cost;
      runsContributing[mode].add(run.issueNumber);
    }
    // Distribute unknown count to every concrete bucket so the dashboard can
    // disclose how many rows were excluded per filter.
    if (unknownInThisRun > 0) {
      for (const mode of modes) {
        excludedUnknown[mode] += unknownInThisRun;
      }
    }
  }

  const result = {} as PerModeCostRollup;
  for (const mode of modes) {
    const perStageP50: PerModeStageStat[] = [];
    const perStageP95: PerModeStageStat[] = [];
    for (const [stage, costs] of stageCosts[mode].entries()) {
      const sorted = [...costs].sort((a, b) => a - b);
      perStageP50.push({
        stage,
        p50CostUsd: percentile(sorted, 0.5),
        p95CostUsd: percentile(sorted, 0.95),
        sampleCount: sorted.length,
      });
      perStageP95.push({
        stage,
        p50CostUsd: percentile(sorted, 0.5),
        p95CostUsd: percentile(sorted, 0.95),
        sampleCount: sorted.length,
      });
    }
    result[mode] = {
      totalCostUsd: totals[mode],
      perStageP50Usd: perStageP50,
      perStageP95Usd: perStageP95,
      runCount: runsContributing[mode].size,
      excludedUnknownStageCount: excludedUnknown[mode],
    };
  }
  return result;
}

/**
 * Calculate token cost given usage and rates
 */
function calculateTokenCost(usage: StageTokenUsage, rates: ModelCostRate): number {
  return (
    (usage.inputTokens / 1_000_000) * rates.inputPerMillion +
    (usage.outputTokens / 1_000_000) * rates.outputPerMillion +
    (usage.cacheReadTokens / 1_000_000) * (rates.cacheReadPerMillion ?? 0) +
    (usage.cacheCreationTokens / 1_000_000) * (rates.cacheCreationPerMillion ?? 0)
  );
}

/**
 * Infer routing mode from stage model sources
 */
function inferRoutingMode(stageModels: StageModelInfo[]): string {
  if (stageModels.length === 0) return "manual";

  const sources = new Set(stageModels.map((sm) => sm.source));
  const models = new Set(stageModels.map((sm) => sm.model));

  // If all models come from history (auto-selector), it's automatic
  if (sources.has("history") && !sources.has("state")) return "automatic";
  // If mix of sources, it's hybrid
  if (sources.size > 1) return "hybrid";
  // If all same model and all from state/fallback, it's manual
  if (models.size === 1) return "manual";

  return "manual";
}
