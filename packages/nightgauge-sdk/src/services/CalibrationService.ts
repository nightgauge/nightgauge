/**
 * CalibrationService - Pipeline cost/duration/token calibration from execution history
 *
 * Builds and maintains a calibration table that aggregates median cost, duration,
 * and token usage per (mode, size) bucket from completed pipeline runs. Used by
 * issue-pickup to validate size estimates and flag outliers, and by AutoModelSelector
 * to anchor pipeline cost estimates against real history.
 *
 * Schema v2 (Issue #3216): table is keyed by `(mode, size)` rather than `size`
 * alone. `mode` is one of efficiency/elevated/maximum (legacy `supercharge`
 * folds into `maximum`); records without a mode tag bucket as `elevated`.
 *
 * @see Issue #1589 - Calibrate complexity estimator using pipeline outcome history
 * @see Issue #3216 - Calibration bucketing by (size, mode)
 * @see docs/ARCHITECTURE.md - SDK utility pattern (pure TS, no VSCode deps)
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";

/** Size buckets matching the Fibonacci complexity scoring */
export type SizeBucket = "XS" | "S" | "M" | "L" | "XL";

/** Valid size bucket values for runtime validation */
const VALID_SIZE_BUCKETS: SizeBucket[] = ["XS", "S", "M", "L", "XL"];

/**
 * Calibration mode bucket. `elevated` is the baseline / fallback when a more
 * specific bucket is empty. `supercharge` (legacy synonym for `maximum`) is
 * normalized to `maximum` at ingestion. `frontier` is the premium Fable-tier
 * mode — bucketed separately so its very different cost profile never pollutes
 * the elevated baseline.
 */
export type CalibrationMode = "efficiency" | "elevated" | "maximum" | "frontier";

const VALID_CALIBRATION_MODES: CalibrationMode[] = [
  "efficiency",
  "elevated",
  "maximum",
  "frontier",
];

/** Per-bucket calibration statistics */
export interface BucketCalibration {
  /** Median cost in USD across completed pipeline runs */
  median_cost_usd: number;
  /** Median duration in milliseconds */
  median_duration_ms: number;
  /** Median total tokens (input + output) */
  median_total_tokens: number;
  /** Number of completed pipeline runs contributing to this bucket */
  sample_count: number;
  /** 25th percentile cost (for IQR outlier detection) */
  p25_cost_usd: number;
  /** 75th percentile cost (for IQR outlier detection) */
  p75_cost_usd: number;
  /** 25th percentile duration */
  p25_duration_ms: number;
  /** 75th percentile duration */
  p75_duration_ms: number;
  /** 25th percentile tokens */
  p25_total_tokens: number;
  /** 75th percentile tokens */
  p75_total_tokens: number;
  /** Last updated ISO timestamp */
  last_updated: string;
}

/**
 * The full calibration table stored in `.nightgauge/pipeline/calibration.json`.
 *
 * Schema v2 nests buckets under a mode key first, then size, so per-mode
 * calibration baselines are tracked independently.
 */
export interface CalibrationTable {
  schema_version: "2";
  /** ISO timestamp of last rebuild */
  updated_at: string;
  /** Total number of completed pipeline runs analyzed */
  total_runs_analyzed: number;
  /** Per-mode → per-size-bucket calibration data */
  buckets: Partial<Record<CalibrationMode, Partial<Record<SizeBucket, BucketCalibration>>>>;
}

/** Legacy v1 shape — read by `load()` for one-time migration. */
interface CalibrationTableV1 {
  schema_version: "1";
  updated_at?: string;
  total_runs_analyzed?: number;
  buckets: Partial<Record<SizeBucket, BucketCalibration>>;
}

/** Minimal record shape consumed by CalibrationService */
export interface CalibrationInput {
  outcome: string;
  size: string | null | undefined;
  cost_usd: number;
  duration_ms: number;
  total_tokens: number;
  /**
   * Performance mode tag. Bucketed as `efficiency` / `elevated` / `maximum`;
   * legacy `supercharge` folds into `maximum`; missing/unknown → `elevated`.
   */
  pipeline_mode?: string | null;
}

/** Result of validating an estimate against calibration data */
export interface EstimateValidation {
  /** Whether the estimate is an outlier */
  is_outlier: boolean;
  /** The size bucket being validated */
  size_bucket: SizeBucket;
  /** Estimated cost / median cost ratio (null if no calibration data) */
  cost_ratio: number | null;
  /** Which metric(s) triggered outlier detection */
  outlier_reasons: string[];
  /** Human-readable summary for pipeline log output */
  summary: string;
  /** Calibration data used for the comparison */
  calibration: BucketCalibration | null;
  /**
   * Mode bucket that actually supplied the calibration data, when found.
   * Equals the requested mode when present, or `"elevated"` when the
   * requested mode bucket was empty and the elevated fallback was used.
   */
  mode_used: CalibrationMode | null;
}

/** IQR multiplier for outlier fences */
const IQR_MULTIPLIER = 1.5;

/** Fallback multiplier when sample count < MIN_SAMPLES_FOR_IQR */
const SIMPLE_OUTLIER_MULTIPLIER = 2.0;

/** Minimum samples required for IQR-based outlier detection */
const MIN_SAMPLES_FOR_IQR = 5;

/** Suffix appended to the pre-migration backup file. */
const MIGRATION_BACKUP_SUFFIX = ".bak-pre-mode-bucketing";

/**
 * Normalize a raw pipeline_mode string to a CalibrationMode bucket.
 * - `efficiency` / `elevated` / `maximum` / `frontier` → themselves
 * - `supercharge` (legacy) → `maximum`
 * - anything else (null, undefined, "normal", unknown strings) → `elevated`
 */
function normalizeMode(raw: string | null | undefined): CalibrationMode {
  if (raw === "supercharge") return "maximum";
  if (raw === "efficiency" || raw === "elevated" || raw === "maximum" || raw === "frontier") {
    return raw;
  }
  return "elevated";
}

export class CalibrationService {
  /**
   * Build a calibration table from execution history records.
   *
   * Groups completed runs by `(mode, size)` and computes p25/median/p75
   * for cost, duration, and tokens. Records without `pipeline_mode` are
   * bucketed under `elevated` (the conservative default).
   */
  static buildFromHistory(records: CalibrationInput[]): CalibrationTable {
    const now = new Date().toISOString();
    const completed = records.filter(
      (r) =>
        r.outcome === "complete" &&
        r.size != null &&
        VALID_SIZE_BUCKETS.includes(r.size as SizeBucket)
    );

    // Group by (mode, size)
    const grouped = new Map<string, CalibrationInput[]>();
    for (const record of completed) {
      const mode = normalizeMode(record.pipeline_mode);
      const size = record.size as SizeBucket;
      const key = `${mode}|${size}`;
      const existing = grouped.get(key);
      if (existing) {
        existing.push(record);
      } else {
        grouped.set(key, [record]);
      }
    }

    const buckets: CalibrationTable["buckets"] = {};
    let totalAnalyzed = 0;

    for (const mode of VALID_CALIBRATION_MODES) {
      for (const size of VALID_SIZE_BUCKETS) {
        const group = grouped.get(`${mode}|${size}`);
        if (!group || group.length === 0) continue;

        totalAnalyzed += group.length;

        if (!buckets[mode]) buckets[mode] = {};
        buckets[mode]![size] = CalibrationService.computeBucketStats(group, now);
      }
    }

    return {
      schema_version: "2",
      updated_at: now,
      total_runs_analyzed: totalAnalyzed,
      buckets,
    };
  }

  private static computeBucketStats(group: CalibrationInput[], now: string): BucketCalibration {
    const costs = group.map((r) => r.cost_usd).sort((a, b) => a - b);
    const durations = group.map((r) => r.duration_ms).sort((a, b) => a - b);
    const tokens = group.map((r) => r.total_tokens).sort((a, b) => a - b);

    return {
      median_cost_usd: CalibrationService.computePercentile(costs, 50),
      median_duration_ms: CalibrationService.computePercentile(durations, 50),
      median_total_tokens: CalibrationService.computePercentile(tokens, 50),
      sample_count: group.length,
      p25_cost_usd: CalibrationService.computePercentile(costs, 25),
      p75_cost_usd: CalibrationService.computePercentile(costs, 75),
      p25_duration_ms: CalibrationService.computePercentile(durations, 25),
      p75_duration_ms: CalibrationService.computePercentile(durations, 75),
      p25_total_tokens: CalibrationService.computePercentile(tokens, 25),
      p75_total_tokens: CalibrationService.computePercentile(tokens, 75),
      last_updated: now,
    };
  }

  /**
   * Look up the calibration cell for a `(mode, size)` pair, falling back to
   * the same-size `elevated` bucket when the requested cell is empty.
   *
   * Returns `{ cell, mode_used }` so callers can report which bucket was
   * actually consulted. When neither the requested mode nor `elevated` has
   * data for the size, returns `{ cell: null, mode_used: null }`.
   */
  static lookupBucket(
    table: CalibrationTable,
    mode: CalibrationMode,
    size: SizeBucket
  ): { cell: BucketCalibration | null; mode_used: CalibrationMode | null } {
    const direct = table.buckets[mode]?.[size];
    if (direct && direct.sample_count > 0) {
      return { cell: direct, mode_used: mode };
    }
    if (mode !== "elevated") {
      const elevated = table.buckets["elevated"]?.[size];
      if (elevated && elevated.sample_count > 0) {
        return { cell: elevated, mode_used: "elevated" };
      }
    }
    return { cell: null, mode_used: null };
  }

  /**
   * Validate a size estimate against calibration history.
   *
   * Uses the `(mode, size)` bucket when available; falls back to the same-size
   * `elevated` bucket (the natural baseline) when the requested mode bucket is
   * empty. Uses IQR-based outlier detection when sample count >= 5; falls back
   * to a 2× median threshold for smaller samples.
   */
  static validateEstimate(
    table: CalibrationTable,
    mode: CalibrationMode,
    sizeBucket: SizeBucket,
    estimatedCost?: number
  ): EstimateValidation {
    const { cell: cal, mode_used } = CalibrationService.lookupBucket(table, mode, sizeBucket);

    if (!cal) {
      return {
        is_outlier: false,
        size_bucket: sizeBucket,
        cost_ratio: null,
        outlier_reasons: [],
        summary: `No calibration data for (${mode}, ${sizeBucket}) (insufficient history)`,
        calibration: null,
        mode_used: null,
      };
    }

    const reasons: string[] = [];
    let costRatio: number | null = null;

    if (estimatedCost != null && cal.median_cost_usd > 0) {
      costRatio = estimatedCost / cal.median_cost_usd;

      if (cal.sample_count >= MIN_SAMPLES_FOR_IQR) {
        const iqr = cal.p75_cost_usd - cal.p25_cost_usd;
        const upperFence = cal.p75_cost_usd + IQR_MULTIPLIER * iqr;
        const lowerFence = cal.p25_cost_usd - IQR_MULTIPLIER * iqr;

        if (estimatedCost > upperFence) {
          reasons.push(
            `estimated cost $${estimatedCost.toFixed(2)} exceeds upper fence $${upperFence.toFixed(2)} (IQR: $${iqr.toFixed(2)})`
          );
        } else if (estimatedCost < lowerFence && lowerFence > 0) {
          reasons.push(
            `estimated cost $${estimatedCost.toFixed(2)} below lower fence $${lowerFence.toFixed(2)} (IQR: $${iqr.toFixed(2)})`
          );
        }
      } else {
        if (estimatedCost > SIMPLE_OUTLIER_MULTIPLIER * cal.median_cost_usd) {
          reasons.push(
            `estimated cost $${estimatedCost.toFixed(2)} exceeds ${SIMPLE_OUTLIER_MULTIPLIER}x median $${cal.median_cost_usd.toFixed(2)}`
          );
        }
      }
    }

    const isOutlier = reasons.length > 0;
    const modeNote = mode_used && mode_used !== mode ? ` (via ${mode_used} fallback)` : "";
    const summary = isOutlier
      ? `Outlier detected for ${sizeBucket}${modeNote}: ${reasons.join("; ")}`
      : `${sizeBucket} estimate within calibrated range${modeNote} (median cost: $${cal.median_cost_usd.toFixed(2)}, n=${cal.sample_count})`;

    return {
      is_outlier: isOutlier,
      size_bucket: sizeBucket,
      cost_ratio: costRatio,
      outlier_reasons: reasons,
      summary,
      calibration: cal,
      mode_used,
    };
  }

  /**
   * Compute the p-th percentile from a sorted array using linear interpolation.
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
   *
   * Migration: when a v1-shape file is encountered, every existing
   * size-keyed cell is placed under the `elevated` mode bucket (records
   * predating Issue #3215 lack `performance_mode`, so `elevated` is the
   * conservative default). The migrated table is rewritten in place after
   * a one-time backup at `${path}.bak-pre-mode-bucketing` (only created if
   * a backup does not already exist — idempotent).
   */
  static async load(calibrationPath: string): Promise<CalibrationTable | null> {
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

    if (obj.schema_version === "2") {
      if (!obj.buckets || typeof obj.buckets !== "object") return null;
      return parsed as CalibrationTable;
    }

    if (obj.schema_version === "1") {
      const v1 = parsed as CalibrationTableV1;
      if (!v1.buckets || typeof v1.buckets !== "object") return null;
      const migrated = CalibrationService.migrateV1ToV2(v1);
      try {
        await CalibrationService.writeBackupIfMissing(calibrationPath, raw);
        await CalibrationService.save(calibrationPath, migrated);
      } catch (err) {
        // Migration write failure is non-fatal — return the in-memory v2
        // shape so the caller still gets correct lookups for this session.
        // The next successful pipeline run will retry the on-disk migration.
        console.warn(
          `[Nightgauge] CalibrationService: failed to persist v1→v2 migration at ${calibrationPath}:`,
          err
        );
      }
      return migrated;
    }

    return null;
  }

  /**
   * Migrate a v1-shape table to v2 by placing every size-keyed cell under
   * the `elevated` bucket. Pure / no I/O.
   */
  private static migrateV1ToV2(v1: CalibrationTableV1): CalibrationTable {
    const elevatedBuckets: Partial<Record<SizeBucket, BucketCalibration>> = {};
    let totalAnalyzed = 0;
    for (const size of VALID_SIZE_BUCKETS) {
      const cell = v1.buckets?.[size];
      if (cell) {
        elevatedBuckets[size] = cell;
        totalAnalyzed += cell.sample_count;
      }
    }
    return {
      schema_version: "2",
      updated_at: v1.updated_at ?? new Date().toISOString(),
      total_runs_analyzed: v1.total_runs_analyzed ?? totalAnalyzed,
      buckets: Object.keys(elevatedBuckets).length > 0 ? { elevated: elevatedBuckets } : {},
    };
  }

  /**
   * Write a one-time pre-migration backup. No-op if the backup already exists,
   * so the call is idempotent across re-reads.
   */
  private static async writeBackupIfMissing(
    calibrationPath: string,
    rawContent: string
  ): Promise<void> {
    const backupPath = `${calibrationPath}${MIGRATION_BACKUP_SUFFIX}`;
    try {
      await fs.access(backupPath);
      return; // backup already exists
    } catch {
      // backup missing — write it
    }
    await fs.writeFile(backupPath, rawContent, "utf-8");
  }

  /**
   * Save calibration table to disk with atomic write.
   */
  static async save(calibrationPath: string, table: CalibrationTable): Promise<void> {
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
    return path.join(workspaceRoot, ".nightgauge", "pipeline", "calibration.json");
  }
}
