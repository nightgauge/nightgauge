/**
 * StageModelCalibrationService - Per-(stage, model) cost calibration from execution history
 *
 * Builds and maintains a calibration table that aggregates observed cost and
 * token usage per `(stage, model)` cell from completed pipeline runs. Used by
 * `AutoModelSelector.estimatePipelineCost()` to replace the static
 * `TOKEN_BASELINES` per-stage cost with an honest observed p75 once enough
 * samples exist for a cell — closing the estimation → execution → learning
 * loop at the stage level instead of rescaling a single whole-run total.
 *
 * Mirrors `CalibrationService`'s percentile math, atomic save, and
 * `load()`-returns-`null`-on-malformed-file pattern, bucketed by `(stage,
 * model)` instead of `(mode, size)`. `computePercentile` is intentionally
 * duplicated rather than imported — `CalibrationService.computePercentile`
 * is `private static`, and `StageDurationAnalyzer` already duplicates the
 * same static method for the same reason.
 *
 * @see Issue #142 - Calibrate cost estimate per (stage, model)
 * @see packages/nightgauge-sdk/src/services/CalibrationService.ts - pattern mirrored
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";

/** Minimum samples required before a (stage, model) cell overrides the static baseline. */
export const MIN_CALIBRATION_SAMPLES = 5;

/** Per-(stage, model) calibration statistics */
export interface StageModelBucketCalibration {
  /** Median cost in USD across completed runs for this cell */
  median_cost_usd: number;
  /** 25th percentile cost (reserved for future outlier detection) */
  p25_cost_usd: number;
  /** 75th percentile cost — the value used to override TOKEN_BASELINES */
  p75_cost_usd: number;
  /** Median input tokens (billed, includes cache) */
  median_input_tokens: number;
  /** Median output tokens */
  median_output_tokens: number;
  /** Number of completed stage executions contributing to this cell */
  sample_count: number;
  /** Last updated ISO timestamp */
  last_updated: string;
}

/**
 * The full calibration table stored in
 * `.nightgauge/pipeline/stage-model-calibration.json`.
 */
export interface StageModelCalibrationTable {
  schema_version: "1";
  /** ISO timestamp of last rebuild */
  updated_at: string;
  /** Total number of (stage, model) records analyzed across all cells */
  total_records_analyzed: number;
  /** stage -> model -> bucket */
  buckets: Record<string, Record<string, StageModelBucketCalibration>>;
}

/** Minimal record shape consumed by StageModelCalibrationService */
export interface StageModelCalibrationInput {
  stage: string;
  model: string;
  cost_usd: number;
  input_tokens: number;
  output_tokens: number;
}

export class StageModelCalibrationService {
  /**
   * Build a calibration table from per-(stage, model) execution history
   * records. Groups by `${stage}|${model}` and computes p25/median/p75 cost
   * plus median tokens for each cell.
   */
  static buildFromHistory(records: StageModelCalibrationInput[]): StageModelCalibrationTable {
    const now = new Date().toISOString();

    const grouped = new Map<string, StageModelCalibrationInput[]>();
    for (const record of records) {
      if (!record.stage || !record.model) continue;
      const key = `${record.stage}|${record.model}`;
      const existing = grouped.get(key);
      if (existing) {
        existing.push(record);
      } else {
        grouped.set(key, [record]);
      }
    }

    const buckets: StageModelCalibrationTable["buckets"] = {};
    let totalAnalyzed = 0;

    for (const [key, group] of grouped) {
      const [stage, model] = key.split("|");
      totalAnalyzed += group.length;

      if (!buckets[stage]) buckets[stage] = {};
      buckets[stage][model] = StageModelCalibrationService.computeBucketStats(group, now);
    }

    return {
      schema_version: "1",
      updated_at: now,
      total_records_analyzed: totalAnalyzed,
      buckets,
    };
  }

  private static computeBucketStats(
    group: StageModelCalibrationInput[],
    now: string
  ): StageModelBucketCalibration {
    const costs = group.map((r) => r.cost_usd).sort((a, b) => a - b);
    const inputTokens = group.map((r) => r.input_tokens).sort((a, b) => a - b);
    const outputTokens = group.map((r) => r.output_tokens).sort((a, b) => a - b);

    return {
      median_cost_usd: StageModelCalibrationService.computePercentile(costs, 50),
      p25_cost_usd: StageModelCalibrationService.computePercentile(costs, 25),
      p75_cost_usd: StageModelCalibrationService.computePercentile(costs, 75),
      median_input_tokens: StageModelCalibrationService.computePercentile(inputTokens, 50),
      median_output_tokens: StageModelCalibrationService.computePercentile(outputTokens, 50),
      sample_count: group.length,
      last_updated: now,
    };
  }

  /**
   * Look up the calibration cell for a `(stage, model)` pair.
   *
   * No mode-style fallback: a wrong model's cost distribution is not a
   * meaningful default for a different model, so an empty cell returns
   * `null` and the caller falls back to `TOKEN_BASELINES` for that stage.
   */
  static lookupBucket(
    table: StageModelCalibrationTable | null | undefined,
    stage: string,
    model: string
  ): { cell: StageModelBucketCalibration | null; sample_count: number } {
    const cell = table?.buckets[stage]?.[model];
    if (cell && cell.sample_count > 0) {
      return { cell, sample_count: cell.sample_count };
    }
    return { cell: null, sample_count: 0 };
  }

  /**
   * Compute the p-th percentile from a sorted array using linear interpolation.
   * Duplicated from `CalibrationService.computePercentile` (private static,
   * not exported) — kept in sync deliberately rather than adding a
   * cross-service dependency for one static method.
   *
   * @param sortedValues - Pre-sorted array of numbers (ascending)
   * @param percentile - Percentile to compute (0-100)
   */
  static computePercentile(sortedValues: number[], percentile: number): number {
    if (sortedValues.length === 0) return 0;
    if (sortedValues.length === 1) return sortedValues[0];

    const index = (percentile / 100) * (sortedValues.length - 1);
    const lower = Math.floor(index);
    const upper = Math.ceil(index);

    if (lower === upper) return sortedValues[lower];

    const fraction = index - lower;
    return sortedValues[lower] + fraction * (sortedValues[upper] - sortedValues[lower]);
  }

  /**
   * Load calibration table from disk.
   *
   * Returns `null` if the file does not exist, is malformed, or carries an
   * unknown schema version.
   */
  static async load(calibrationPath: string): Promise<StageModelCalibrationTable | null> {
    let raw: string;
    try {
      raw = await fs.readFile(calibrationPath, "utf-8");
    } catch {
      return null;
    }

    let parsed: unknown;
    try {
      parsed = JSON.parse(raw);
    } catch {
      return null;
    }

    if (!parsed || typeof parsed !== "object") return null;
    const obj = parsed as { schema_version?: string; buckets?: unknown };

    if (obj.schema_version !== "1") return null;
    if (!obj.buckets || typeof obj.buckets !== "object") return null;

    return parsed as StageModelCalibrationTable;
  }

  /**
   * Save calibration table to disk with atomic write.
   */
  static async save(calibrationPath: string, table: StageModelCalibrationTable): Promise<void> {
    const dir = path.dirname(calibrationPath);
    await fs.mkdir(dir, { recursive: true });

    const tempPath = `${calibrationPath}.tmp`;
    const json = JSON.stringify(table, null, 2);

    await fs.writeFile(tempPath, json, "utf-8");
    await fs.rename(tempPath, calibrationPath);
  }

  /**
   * Get the default calibration file path for a workspace.
   */
  static getDefaultPath(workspaceRoot: string): string {
    return path.join(workspaceRoot, ".nightgauge", "pipeline", "stage-model-calibration.json");
  }
}
