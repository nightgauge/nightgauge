/**
 * Work time estimation based on size labels and complexity model
 *
 * Provides deterministic time estimates for issues based on size labels.
 * Reads calibrated averages from complexity-model.yaml when available,
 * falls back to sensible defaults.
 *
 * @see Issue #310 - Add actual work time feedback loop
 */

import * as fs from "node:fs/promises";
import * as yaml from "js-yaml";
import type { SizeLabel } from "./changeAnalyzer";

/**
 * Default time estimates per size (in minutes)
 * Used when complexity model doesn't exist or has insufficient data
 */
const DEFAULT_ESTIMATES: Record<NonNullable<SizeLabel>, number> = {
  XS: 30, // < 1 hour
  S: 120, // 1-4 hours (midpoint: 2.5 hours)
  M: 600, // 1-2 days (midpoint: 1.5 days = 12 hours)
  L: 1920, // 3-5 days (midpoint: 4 days = 32 hours)
  XL: 4800, // > 1 week (1 week = 80 hours)
};

/**
 * Work-time feedback structure from complexity-model.yaml
 */
interface WorkTimeFeedback {
  enabled: boolean;
  observations: Array<{
    size: SizeLabel;
    actual_work_minutes: number;
  }>;
  size_averages?: Partial<
    Record<
      NonNullable<SizeLabel>,
      {
        estimated: number;
        actual_average: number;
        observation_count: number;
      }
    >
  >;
}

/**
 * Get estimated work time for a given size label
 *
 * Reads from complexity-model.yaml if available, falls back to defaults.
 * Returns 0 if size is null (unknown).
 *
 * @param size - Size label from issue (XS, S, M, L, XL)
 * @param workspaceRoot - Workspace root path for finding complexity-model.yaml
 * @returns Estimated work time in minutes
 */
export async function getEstimatedMinutes(size: SizeLabel, workspaceRoot: string): Promise<number> {
  if (!size) {
    return 0; // Unknown size
  }

  // Try to read calibrated estimate from complexity model
  const calibratedEstimate = await readCalibratedEstimate(size, workspaceRoot);
  if (calibratedEstimate !== null) {
    return calibratedEstimate;
  }

  // Fall back to default estimates
  return DEFAULT_ESTIMATES[size];
}

/**
 * Read calibrated estimate from complexity-model.yaml
 *
 * Returns null if:
 * - File doesn't exist
 * - work_time_feedback section missing
 * - No observations for this size
 * - Insufficient sample count (< 3)
 *
 * @param size - Size label
 * @param workspaceRoot - Workspace root path
 * @returns Calibrated average or null
 */
async function readCalibratedEstimate(
  size: NonNullable<SizeLabel>,
  workspaceRoot: string
): Promise<number | null> {
  try {
    const yamlPath = `${workspaceRoot}/.nightgauge/complexity-model.yaml`;
    const content = await fs.readFile(yamlPath, "utf-8");
    const parsed = yaml.load(content) as Record<string, unknown>;

    const feedback = parsed.work_time_feedback as WorkTimeFeedback | undefined;
    if (!feedback || !feedback.enabled) {
      return null;
    }

    // Check if we have size_averages with sufficient data
    const sizeAverage = feedback.size_averages?.[size];
    if (sizeAverage && sizeAverage.observation_count >= 3) {
      // Use actual_average if we have enough data
      return Math.round(sizeAverage.actual_average);
    }

    return null;
  } catch {
    // File doesn't exist or is corrupted - use defaults
    return null;
  }
}

/**
 * Get default estimate for a size (doesn't read complexity model)
 *
 * Useful for testing or when you know the model doesn't exist.
 *
 * @param size - Size label
 * @returns Default estimate in minutes
 */
export function getDefaultEstimate(size: SizeLabel): number {
  if (!size) {
    return 0;
  }
  return DEFAULT_ESTIMATES[size];
}
