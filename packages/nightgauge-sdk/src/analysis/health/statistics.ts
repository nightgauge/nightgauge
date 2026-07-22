/**
 * Statistical Utility Functions for Health Analysis
 *
 * Pure utility functions for percentiles, trends, significance testing,
 * and period comparison. Used by all dimension analyzers.
 *
 * @see Issue #1101 - Multi-Dimensional Health Analysis Engine
 */

import type { PeriodComparison, TrendDirection } from "./types.js";

/**
 * Compute the Nth percentile of a numeric array using linear interpolation.
 * Returns 0 for empty arrays.
 */
export function computePercentile(values: number[], p: number): number {
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
 * Compute linear trend direction from a time series using simple linear regression.
 *
 * @param timeSeries - Array of values in chronological order
 * @param slopeThreshold - Minimum absolute slope to classify as non-stable (default 0.05)
 * @returns Slope value and classified direction
 */
export function computeTrend(
  timeSeries: number[],
  slopeThreshold: number = 0.05
): { slope: number; direction: TrendDirection } {
  if (timeSeries.length < 2) return { slope: 0, direction: "stable" };

  const n = timeSeries.length;
  let sumX = 0;
  let sumY = 0;
  let sumXY = 0;
  let sumXX = 0;

  for (let i = 0; i < n; i++) {
    sumX += i;
    sumY += timeSeries[i];
    sumXY += i * timeSeries[i];
    sumXX += i * i;
  }

  const denominator = n * sumXX - sumX * sumX;
  if (denominator === 0) return { slope: 0, direction: "stable" };

  const slope = (n * sumXY - sumX * sumY) / denominator;

  let direction: TrendDirection;
  if (slope < -slopeThreshold) direction = "improving";
  else if (slope > slopeThreshold) direction = "degrading";
  else direction = "stable";

  return { slope, direction };
}

/**
 * Test whether the difference between current and baseline values is
 * statistically significant using a simple z-test approximation.
 *
 * For small sample sizes typical of pipeline data (5-50 runs), this uses
 * conservative thresholds rather than full hypothesis testing.
 *
 * @returns Significance assessment with confidence level
 */
export function isStatisticallySignificant(
  currentValue: number,
  baselineValue: number,
  sampleSize: number,
  confidenceThreshold: number = 0.05
): { isSignificant: boolean; confidence: "high" | "medium" | "low" } {
  if (sampleSize < 5) {
    return { isSignificant: false, confidence: "low" };
  }

  const changePercent =
    baselineValue !== 0
      ? Math.abs((currentValue - baselineValue) / baselineValue)
      : currentValue !== 0
        ? 1
        : 0;

  // Conservative significance thresholds based on sample size
  if (sampleSize >= 20) {
    return {
      isSignificant: changePercent > confidenceThreshold,
      confidence: "high",
    };
  }
  if (sampleSize >= 10) {
    return {
      isSignificant: changePercent > confidenceThreshold * 2,
      confidence: "medium",
    };
  }
  // 5-9 samples: require large change
  return {
    isSignificant: changePercent > confidenceThreshold * 3,
    confidence: "low",
  };
}

/**
 * Compute percentage change between current and baseline values.
 * Returns 0 when baseline is 0 and current is also 0.
 * Returns 100 when baseline is 0 and current is non-zero.
 */
export function computeChangePercent(current: number, baseline: number): number {
  if (baseline === 0) return current === 0 ? 0 : 100;
  return ((current - baseline) / Math.abs(baseline)) * 100;
}

/**
 * Check whether a sample size meets the minimum data requirement.
 */
export function hasEnoughData(sampleSize: number, minimum: number): boolean {
  return sampleSize >= minimum;
}

/**
 * Build a PeriodComparison between current and baseline metric values.
 */
export function buildPeriodComparison(
  currentValue: number,
  baselineValue: number,
  sampleSize: number,
  lowerIsBetter: boolean = false,
  confidenceThreshold: number = 0.05
): PeriodComparison {
  const changePercent = computeChangePercent(currentValue, baselineValue);
  const sig = isStatisticallySignificant(
    currentValue,
    baselineValue,
    sampleSize,
    confidenceThreshold
  );

  let direction: TrendDirection;
  if (Math.abs(changePercent) < 1) {
    direction = "stable";
  } else if (lowerIsBetter) {
    direction = changePercent < 0 ? "improving" : "degrading";
  } else {
    direction = changePercent > 0 ? "improving" : "degrading";
  }

  return {
    currentValue,
    baselineValue,
    changePercent,
    direction,
    isSignificant: sig.isSignificant,
  };
}

/**
 * Compute the mean of a numeric array. Returns 0 for empty arrays.
 */
export function mean(values: number[]): number {
  if (values.length === 0) return 0;
  return values.reduce((sum, v) => sum + v, 0) / values.length;
}

/**
 * Compute standard deviation of a numeric array. Returns 0 for arrays with < 2 elements.
 */
export function standardDeviation(values: number[]): number {
  if (values.length < 2) return 0;
  const avg = mean(values);
  const squaredDiffs = values.map((v) => (v - avg) ** 2);
  return Math.sqrt(squaredDiffs.reduce((sum, v) => sum + v, 0) / (values.length - 1));
}

/**
 * Clamp a value between min and max bounds.
 */
export function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}
