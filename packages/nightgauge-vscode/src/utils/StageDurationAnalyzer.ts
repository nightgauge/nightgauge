/**
 * StageDurationAnalyzer - Per-stage duration percentile computation
 *
 * Reads JSONL execution history, filters to successful completed runs,
 * and computes per-stage duration statistics (p50, p75, p95, p99, max,
 * count, mean, stddev). Results are cached in-memory and invalidated
 * on explicit demand.
 *
 * Downstream consumers (Issues #2652, #2662) use getStageStats() to
 * derive calibrated stall thresholds from real pipeline data. As of
 * Issue #3216, callers can also request stats filtered to a single
 * `performance_mode` via getStageStatsByMode() — populated in the same
 * read pass, so calling both for the same workspace incurs no extra I/O.
 *
 * @see Issue #2651 - Compute per-stage duration percentiles from JSONL history
 * @see Issue #2652 - Replace hardcoded stall thresholds with data-driven p95
 * @see Issue #2662 - Adaptive stall detection
 * @see Issue #3216 - Calibration bucketing by (size, mode)
 */

import type { ExecutionHistoryRunRecordV2 } from "../schemas/executionHistory";
import type { PerformanceMode } from "./modeProfiles";
import { ExecutionHistoryReader } from "./executionHistoryReader";

// ============================================================================
// Types and Interfaces
// ============================================================================

/**
 * Per-stage duration statistics computed from completed pipeline runs.
 */
export interface StageStats {
  /** Pipeline stage name (e.g., 'feature-dev') */
  stage: string;

  /** Number of completed runs with duration data for this stage */
  count: number;

  /** Mean duration in milliseconds */
  mean_ms: number;

  /** 50th percentile (median) duration in milliseconds */
  p50_ms: number;

  /** 75th percentile duration in milliseconds */
  p75_ms: number;

  /** 95th percentile duration in milliseconds */
  p95_ms: number;

  /** 99th percentile duration in milliseconds */
  p99_ms: number;

  /** Maximum duration observed in milliseconds */
  max_ms: number;

  /** Minimum duration observed in milliseconds */
  min_ms: number;

  /** Standard deviation of durations in milliseconds */
  stddev_ms: number;

  /** ISO timestamp when stats were last computed */
  last_updated: string;
}

/**
 * Per-mode → per-stage stats map. Populated by `analyzeStageDurations`
 * alongside the unfiltered `stages` map so per-mode lookups do not require
 * a second read of the JSONL history.
 *
 * Stage records lacking `performance_mode` (records predating issue #3215)
 * are bucketed under `elevated` only — the conservative default that matches
 * the calibration migration policy.
 *
 * @see Issue #3216
 */
export type StagesByMode = Record<PerformanceMode, Record<string, StageStats>>;

/**
 * Complete analysis result containing per-stage statistics for all stages
 * with at least one completed run.
 */
export interface StageDurationAnalysisResult {
  schema_version: "1";
  /** ISO timestamp when analysis was computed */
  computed_at: string;
  /** Data staleness window in days — runs older than this were excluded */
  data_window_days: number;
  /** Per-stage statistics keyed by stage name (no mode filter) */
  stages: Record<string, StageStats>;
  /**
   * Per-mode per-stage statistics. Same data as `stages` but partitioned
   * by `performance_mode`. Records lacking the field land in `elevated`.
   *
   * @see Issue #3216
   */
  stagesByMode: StagesByMode;
  /** Total number of completed run records that were analyzed */
  total_runs_analyzed: number;
  /** Human-readable notes about data quality or analysis limitations */
  analysis_notes: string[];
}

// ============================================================================
// Internal Cache
// ============================================================================

/**
 * Module-level cache for the last analysis result.
 * Keyed by workspaceRoot so multi-workspace scenarios are handled correctly.
 */
interface CacheEntry {
  result: StageDurationAnalysisResult;
  computedAt: number; // Date.now() ms
  staleDurationDays: number;
}

const _cache = new Map<string, CacheEntry>();

// ============================================================================
// Internal Helpers
// ============================================================================

/**
 * Compute the Nth percentile of a sorted numeric array using linear interpolation.
 * Returns 0 for empty arrays.
 *
 * Uses the same algorithm as CalibrationService / statistics.ts for consistency.
 * Formula: index = (p/100) * (n-1), interpolate between floor and ceil.
 */
function computePercentile(sortedValues: number[], p: number): number {
  if (sortedValues.length === 0) return 0;
  const index = (p / 100) * (sortedValues.length - 1);
  const lower = Math.floor(index);
  const upper = Math.ceil(index);
  if (lower === upper) return sortedValues[lower];
  const weight = index - lower;
  return sortedValues[lower] * (1 - weight) + sortedValues[upper] * weight;
}

/**
 * Compute the arithmetic mean of a numeric array.
 * Returns 0 for empty arrays.
 */
function computeMean(values: number[]): number {
  if (values.length === 0) return 0;
  return values.reduce((sum, v) => sum + v, 0) / values.length;
}

/**
 * Compute sample standard deviation of a numeric array.
 * Returns 0 for arrays with fewer than 2 elements.
 */
function computeStdDev(values: number[], mean: number): number {
  if (values.length < 2) return 0;
  const squaredDiffs = values.map((v) => (v - mean) ** 2);
  return Math.sqrt(squaredDiffs.reduce((sum, v) => sum + v, 0) / (values.length - 1));
}

/**
 * Compute StageStats from an array of raw duration values for one stage.
 * The values array must be non-empty.
 */
function buildStageStats(stage: string, durations: number[]): StageStats {
  const sorted = [...durations].sort((a, b) => a - b);
  const mean_ms = computeMean(durations);
  return {
    stage,
    count: durations.length,
    mean_ms: Math.round(mean_ms),
    p50_ms: Math.round(computePercentile(sorted, 50)),
    p75_ms: Math.round(computePercentile(sorted, 75)),
    p95_ms: Math.round(computePercentile(sorted, 95)),
    p99_ms: Math.round(computePercentile(sorted, 99)),
    max_ms: sorted[sorted.length - 1],
    min_ms: sorted[0],
    stddev_ms: Math.round(computeStdDev(durations, mean_ms)),
    last_updated: new Date().toISOString(),
  };
}

/**
 * Determine the cutoff Date for a staleness window.
 * Runs with recorded_at before this date are excluded.
 * When staleDurationDays is 0, all records are included.
 */
function computeCutoffDate(staleDurationDays: number): Date | null {
  if (staleDurationDays <= 0) return null;
  const cutoff = new Date();
  cutoff.setUTCDate(cutoff.getUTCDate() - staleDurationDays);
  return cutoff;
}

/** Empty per-mode stage map factory. */
function emptyStagesByMode(): StagesByMode {
  return {
    efficiency: {},
    elevated: {},
    maximum: {},
    frontier: {},
  };
}

// ============================================================================
// StageDurationAnalyzer
// ============================================================================

/**
 * Analyzes JSONL pipeline execution history to compute per-stage duration
 * percentiles. All public methods are static; caching is module-level.
 *
 * @example
 * ```typescript
 * const result = await StageDurationAnalyzer.analyzeStageDurations('/path/to/repo');
 * console.log(result.stages['feature-dev'].p95_ms); // e.g. 540000
 *
 * const stats = await StageDurationAnalyzer.getStageStats('/path/to/repo', 'feature-dev');
 * if (stats) {
 *   console.log(`Median feature-dev duration: ${stats.p50_ms}ms`);
 * }
 *
 * const efficiencyStats = await StageDurationAnalyzer.getStageStatsByMode(
 *   '/path/to/repo',
 *   'feature-dev',
 *   'efficiency'
 * );
 * ```
 */
export class StageDurationAnalyzer {
  /**
   * Analyze all completed pipeline runs in the JSONL history and compute
   * per-stage duration statistics.
   *
   * Results are cached in-memory. Subsequent calls within the staleness window
   * return the cached result instantly. Call `invalidateCache()` to force
   * recomputation.
   *
   * Only runs with `outcome === 'complete'` are included. Stages that have no
   * completed runs with valid `duration_ms` data are omitted from the result.
   *
   * Issue #3216: a per-mode partition (`stagesByMode`) is populated in the
   * same read pass. Stage records lacking `performance_mode` fall through to
   * the `elevated` bucket only.
   *
   * @param workspaceRoot - Absolute path to the repository root
   * @param staleDurationDays - Exclude runs older than this many days (default: 30).
   *   Set to 0 to include all history regardless of age.
   * @returns Analysis result with per-stage stats and metadata. Never throws —
   *   returns an empty result with analysis notes if history is unavailable.
   */
  static async analyzeStageDurations(
    workspaceRoot: string,
    staleDurationDays: number = 30
  ): Promise<StageDurationAnalysisResult> {
    const cacheKey = `${workspaceRoot}:${staleDurationDays}`;
    const cached = _cache.get(cacheKey);
    if (cached) {
      const cacheAgeMs = Date.now() - cached.computedAt;
      const cacheTtlMs = cached.staleDurationDays * 24 * 60 * 60 * 1000;
      if (cacheAgeMs < cacheTtlMs || cached.staleDurationDays <= 0) {
        return cached.result;
      }
    }

    try {
      const allRecords = await ExecutionHistoryReader.readAll(workspaceRoot);
      const notes: string[] = [];

      const completedRuns = allRecords.filter(
        (r): r is ExecutionHistoryRunRecordV2 => r.record_type === "run" && r.outcome === "complete"
      );

      const cutoff = computeCutoffDate(staleDurationDays);
      const windowedRuns = cutoff
        ? completedRuns.filter((r) => {
            const recordedAt = new Date(r.recorded_at);
            return recordedAt >= cutoff;
          })
        : completedRuns;

      if (completedRuns.length > windowedRuns.length) {
        notes.push(
          `Excluded ${completedRuns.length - windowedRuns.length} run(s) older than ${staleDurationDays} days from analysis.`
        );
      }

      // Per-stage durations (mode-agnostic) AND per-mode partition.
      // Both are populated in a single pass — calling getStageStatsByMode
      // for any mode therefore costs no extra I/O.
      const stageDurations = new Map<string, number[]>();
      const stageDurationsByMode: Record<PerformanceMode, Map<string, number[]>> = {
        efficiency: new Map(),
        elevated: new Map(),
        maximum: new Map(),
        frontier: new Map(),
      };

      let skippedStageEntries = 0;
      for (const run of windowedRuns) {
        if (!run.stages) continue;
        for (const [stageName, stageDetail] of Object.entries(run.stages)) {
          if (stageDetail.status !== "complete") continue;
          const durationMs = stageDetail.duration_ms;
          if (typeof durationMs !== "number" || !isFinite(durationMs) || durationMs < 0) {
            skippedStageEntries++;
            continue;
          }

          const existing = stageDurations.get(stageName) ?? [];
          existing.push(durationMs);
          stageDurations.set(stageName, existing);

          // Records predating Wave A2 (#3215) lack `performance_mode` —
          // bucket them under `elevated` only (conservative default,
          // matches calibration migration policy).
          const mode: PerformanceMode = stageDetail.performance_mode ?? "elevated";
          const modeMap = stageDurationsByMode[mode];
          const existingForMode = modeMap.get(stageName) ?? [];
          existingForMode.push(durationMs);
          modeMap.set(stageName, existingForMode);
        }
      }

      if (skippedStageEntries > 0) {
        notes.push(
          `Skipped ${skippedStageEntries} stage entry(ies) with missing or invalid duration_ms values.`
        );
      }

      // Build mode-agnostic per-stage stats
      const stages: Record<string, StageStats> = {};
      for (const [stageName, durations] of stageDurations) {
        if (durations.length === 0) continue;
        stages[stageName] = buildStageStats(stageName, durations);

        if (durations.length < 20) {
          notes.push(
            `Stage '${stageName}' has only ${durations.length} sample(s) — p99 estimate may be unreliable.`
          );
        }
      }

      // Build per-mode per-stage stats
      const stagesByMode: StagesByMode = emptyStagesByMode();
      for (const mode of Object.keys(stagesByMode) as PerformanceMode[]) {
        const modeMap = stageDurationsByMode[mode];
        for (const [stageName, durations] of modeMap) {
          if (durations.length === 0) continue;
          stagesByMode[mode][stageName] = buildStageStats(stageName, durations);
        }
      }

      if (stageDurations.size === 0 && windowedRuns.length === 0) {
        notes.push("No completed pipeline runs found in history. Returning empty result.");
      }

      const result: StageDurationAnalysisResult = {
        schema_version: "1",
        computed_at: new Date().toISOString(),
        data_window_days: staleDurationDays,
        stages,
        stagesByMode,
        total_runs_analyzed: windowedRuns.length,
        analysis_notes: notes,
      };

      _cache.set(cacheKey, {
        result,
        computedAt: Date.now(),
        staleDurationDays,
      });

      return result;
    } catch (err) {
      console.warn("[Nightgauge] StageDurationAnalyzer.analyzeStageDurations failed:", err);
      return {
        schema_version: "1",
        computed_at: new Date().toISOString(),
        data_window_days: staleDurationDays,
        stages: {},
        stagesByMode: emptyStagesByMode(),
        total_runs_analyzed: 0,
        analysis_notes: ["Analysis failed due to unexpected error — see console for details."],
      };
    }
  }

  /**
   * Get duration statistics for a specific pipeline stage (across all modes).
   *
   * Returns `undefined` when the stage has no completed runs in history
   * or when the stage name is not recognized. Never throws.
   *
   * @param workspaceRoot - Absolute path to the repository root
   * @param stage - Pipeline stage name (e.g., 'feature-dev', 'pr-create')
   * @param staleDurationDays - Passed through to analyzeStageDurations (default: 30)
   * @returns Stats for the stage, or undefined if no data is available
   */
  static async getStageStats(
    workspaceRoot: string,
    stage: string,
    staleDurationDays: number = 30
  ): Promise<StageStats | undefined> {
    const result = await this.analyzeStageDurations(workspaceRoot, staleDurationDays);
    return result.stages[stage];
  }

  /**
   * Get duration statistics for a specific pipeline stage filtered to a
   * single performance mode.
   *
   * Backed by the same cached analysis as `getStageStats` — calling both for
   * the same workspace incurs no extra I/O. Returns `undefined` when the
   * (stage, mode) pair has no completed runs.
   *
   * @param workspaceRoot - Absolute path to the repository root
   * @param stage - Pipeline stage name (e.g., 'feature-dev')
   * @param mode - Performance mode to filter on (efficiency / elevated / maximum)
   * @param staleDurationDays - Passed through to analyzeStageDurations (default: 30)
   * @returns Stats for the (stage, mode) bucket, or undefined if empty
   *
   * @see Issue #3216 - Calibration bucketing by (size, mode)
   */
  static async getStageStatsByMode(
    workspaceRoot: string,
    stage: string,
    mode: PerformanceMode,
    staleDurationDays: number = 30
  ): Promise<StageStats | undefined> {
    const result = await this.analyzeStageDurations(workspaceRoot, staleDurationDays);
    return result.stagesByMode[mode]?.[stage];
  }

  /**
   * Invalidate all cached analysis results.
   *
   * Call this when you know the history has been updated and want to force
   * a fresh computation on the next call to analyzeStageDurations or
   * getStageStats.
   *
   * Invalidation is global — all workspaceRoot cache entries are cleared.
   */
  static async invalidateCache(): Promise<void> {
    _cache.clear();
  }
}
