/**
 * Unit tests for statistics.ts — Statistical Utility Functions
 *
 * Tests all pure math utilities used by the multi-dimensional health analysis
 * engine (Issue #1101): percentiles, trend detection, significance testing,
 * change percent, data sufficiency, period comparison, mean, std dev, and clamp.
 */

import { describe, it, expect } from "vitest";
import {
  computePercentile,
  computeTrend,
  isStatisticallySignificant,
  computeChangePercent,
  hasEnoughData,
  buildPeriodComparison,
  mean,
  standardDeviation,
  clamp,
} from "../../../src/analysis/health/statistics.js";

// ── computePercentile ─────────────────────────────────────────────

describe("computePercentile", () => {
  it("returns 0 for an empty array", () => {
    expect(computePercentile([], 50)).toBe(0);
  });

  it("returns 0 for empty array at any percentile", () => {
    expect(computePercentile([], 0)).toBe(0);
    expect(computePercentile([], 95)).toBe(0);
    expect(computePercentile([], 100)).toBe(0);
  });

  it("returns the single element for a one-element array at P0", () => {
    expect(computePercentile([42], 0)).toBe(42);
  });

  it("returns the single element for a one-element array at P50", () => {
    expect(computePercentile([42], 50)).toBe(42);
  });

  it("returns the single element for a one-element array at P100", () => {
    expect(computePercentile([42], 100)).toBe(42);
  });

  it("returns the single element regardless of percentile value", () => {
    expect(computePercentile([7], 25)).toBe(7);
    expect(computePercentile([7], 75)).toBe(7);
    expect(computePercentile([7], 99)).toBe(7);
  });

  it("[1,2,3,4,5] P50 → 3", () => {
    expect(computePercentile([1, 2, 3, 4, 5], 50)).toBe(3);
  });

  it("[1,2,3,4,5] P95 → 4.8", () => {
    expect(computePercentile([1, 2, 3, 4, 5], 95)).toBeCloseTo(4.8);
  });

  it("[1,2,3,4,5] P0 → 1 (minimum)", () => {
    expect(computePercentile([1, 2, 3, 4, 5], 0)).toBe(1);
  });

  it("[1,2,3,4,5] P100 → 5 (maximum)", () => {
    expect(computePercentile([1, 2, 3, 4, 5], 100)).toBe(5);
  });

  it("[1,2,3,4,5] P25 → 2 (exact lower quartile)", () => {
    expect(computePercentile([1, 2, 3, 4, 5], 25)).toBe(2);
  });

  it("[1,2,3,4,5] P75 → 4 (exact upper quartile)", () => {
    expect(computePercentile([1, 2, 3, 4, 5], 75)).toBe(4);
  });

  it("sorts unsorted input before computing percentile", () => {
    // Same values as [1,2,3,4,5] but shuffled
    expect(computePercentile([5, 3, 1, 4, 2], 50)).toBe(3);
  });

  it("handles duplicate values correctly", () => {
    expect(computePercentile([2, 2, 2, 2], 50)).toBe(2);
  });

  it("uses linear interpolation between elements", () => {
    // [10, 20]: P50 → index = 0.5 * (2-1) = 0.5 → 10 * 0.5 + 20 * 0.5 = 15
    expect(computePercentile([10, 20], 50)).toBeCloseTo(15);
  });

  it("interpolates correctly for [10,20] at P25", () => {
    // index = 0.25 * 1 = 0.25 → 10 * 0.75 + 20 * 0.25 = 12.5
    expect(computePercentile([10, 20], 25)).toBeCloseTo(12.5);
  });

  it("does not mutate the input array", () => {
    const input = [5, 3, 1, 4, 2];
    const original = [...input];
    computePercentile(input, 50);
    expect(input).toEqual(original);
  });
});

// ── computeTrend ──────────────────────────────────────────────────

describe("computeTrend", () => {
  it('returns { slope: 0, direction: "stable" } for an empty array', () => {
    const result = computeTrend([]);
    expect(result).toEqual({ slope: 0, direction: "stable" });
  });

  it('returns { slope: 0, direction: "stable" } for a single-element array', () => {
    const result = computeTrend([5]);
    expect(result).toEqual({ slope: 0, direction: "stable" });
  });

  it('classifies a clearly increasing series as "degrading"', () => {
    const result = computeTrend([1, 2, 3, 4, 5]);
    expect(result.direction).toBe("degrading");
    expect(result.slope).toBeGreaterThan(0.05);
  });

  it('classifies a clearly decreasing series as "improving"', () => {
    const result = computeTrend([5, 4, 3, 2, 1]);
    expect(result.direction).toBe("improving");
    expect(result.slope).toBeLessThan(-0.05);
  });

  it('classifies a flat series as "stable"', () => {
    const result = computeTrend([3, 3, 3, 3, 3]);
    expect(result.direction).toBe("stable");
    expect(result.slope).toBe(0);
  });

  it("returns the exact computed slope value", () => {
    // [0, 1]: n=2, sumX=1, sumY=1, sumXY=1, sumXX=1
    // denom = 2*1 - 1*1 = 1, slope = (2*1 - 1*1)/1 = 1
    const result = computeTrend([0, 1]);
    expect(result.slope).toBeCloseTo(1);
  });

  it("uses the default slopeThreshold of 0.05", () => {
    // A series with slope exactly at 0.05 should still be "stable" (not > 0.05)
    // Slope between 0 and 0.05 exclusive should be stable
    const result = computeTrend([0, 0.04]);
    expect(result.direction).toBe("stable");
  });

  it('classifies as "degrading" when slope exceeds custom slopeThreshold', () => {
    // With threshold = 0.5, a slope of 1 is degrading
    const result = computeTrend([0, 1], 0.5);
    expect(result.direction).toBe("degrading");
  });

  it('classifies as "stable" when slope is within a high custom slopeThreshold', () => {
    // Slope = 1, threshold = 2: 1 is not > 2, so stable
    const result = computeTrend([0, 1], 2);
    expect(result.direction).toBe("stable");
  });

  it('classifies as "improving" when slope is below negative custom slopeThreshold', () => {
    // Slope = -1, threshold = 0.5: -1 < -0.5, so improving
    const result = computeTrend([1, 0], 0.5);
    expect(result.direction).toBe("improving");
  });

  it("handles a two-element increasing series correctly", () => {
    const result = computeTrend([10, 20]);
    expect(result.direction).toBe("degrading");
    expect(result.slope).toBeGreaterThan(0.05);
  });

  it("handles a two-element decreasing series correctly", () => {
    const result = computeTrend([20, 10]);
    expect(result.direction).toBe("improving");
    expect(result.slope).toBeLessThan(-0.05);
  });

  it('handles noisy but net-increasing data as "degrading"', () => {
    // Net upward trend despite noise
    const result = computeTrend([1, 3, 2, 4, 3, 5, 4, 6]);
    expect(result.direction).toBe("degrading");
  });

  it('handles noisy but net-decreasing data as "improving"', () => {
    const result = computeTrend([6, 4, 5, 3, 4, 2, 3, 1]);
    expect(result.direction).toBe("improving");
  });
});

// ── isStatisticallySignificant ────────────────────────────────────

describe("isStatisticallySignificant", () => {
  // sampleSize < 5: always not significant, confidence: 'low'

  it('returns { isSignificant: false, confidence: "low" } for sampleSize 0', () => {
    expect(isStatisticallySignificant(10, 5, 0)).toEqual({
      isSignificant: false,
      confidence: "low",
    });
  });

  it('returns { isSignificant: false, confidence: "low" } for sampleSize 1', () => {
    expect(isStatisticallySignificant(100, 1, 1)).toEqual({
      isSignificant: false,
      confidence: "low",
    });
  });

  it('returns { isSignificant: false, confidence: "low" } for sampleSize 4 (boundary below 5)', () => {
    expect(isStatisticallySignificant(10, 5, 4)).toEqual({
      isSignificant: false,
      confidence: "low",
    });
  });

  // sampleSize 5-9: threshold * 3, confidence: 'low'

  it('returns confidence "low" for sampleSize 5', () => {
    const result = isStatisticallySignificant(10, 5, 5);
    expect(result.confidence).toBe("low");
  });

  it('returns confidence "low" for sampleSize 9 (boundary below 10)', () => {
    const result = isStatisticallySignificant(10, 5, 9);
    expect(result.confidence).toBe("low");
  });

  it("is not significant with sampleSize 5 when changePercent <= threshold * 3", () => {
    // changePercent = |10-10|/10 = 0 → not significant
    const result = isStatisticallySignificant(10, 10, 5, 0.05);
    expect(result.isSignificant).toBe(false);
    expect(result.confidence).toBe("low");
  });

  it("is significant with sampleSize 5 when changePercent > threshold * 3", () => {
    // changePercent = |20-10|/10 = 1.0 > 0.15
    const result = isStatisticallySignificant(20, 10, 5, 0.05);
    expect(result.isSignificant).toBe(true);
    expect(result.confidence).toBe("low");
  });

  it("is not significant with sampleSize 7 when change is within threshold * 3", () => {
    // changePercent = |10.1 - 10| / 10 = 0.01, threshold * 3 = 0.15
    const result = isStatisticallySignificant(10.1, 10, 7, 0.05);
    expect(result.isSignificant).toBe(false);
    expect(result.confidence).toBe("low");
  });

  // sampleSize 10-19: threshold * 2, confidence: 'medium'

  it('returns confidence "medium" for sampleSize 10', () => {
    const result = isStatisticallySignificant(10, 5, 10);
    expect(result.confidence).toBe("medium");
  });

  it('returns confidence "medium" for sampleSize 19 (boundary below 20)', () => {
    const result = isStatisticallySignificant(10, 5, 19);
    expect(result.confidence).toBe("medium");
  });

  it("is not significant with sampleSize 15 when changePercent <= threshold * 2", () => {
    // changePercent = |10.05 - 10| / 10 = 0.005, threshold * 2 = 0.10
    const result = isStatisticallySignificant(10.05, 10, 15, 0.05);
    expect(result.isSignificant).toBe(false);
    expect(result.confidence).toBe("medium");
  });

  it("is significant with sampleSize 15 when changePercent > threshold * 2", () => {
    // changePercent = |12 - 10| / 10 = 0.2 > 0.10
    const result = isStatisticallySignificant(12, 10, 15, 0.05);
    expect(result.isSignificant).toBe(true);
    expect(result.confidence).toBe("medium");
  });

  // sampleSize >= 20: threshold as-is, confidence: 'high'

  it('returns confidence "high" for sampleSize 20 (boundary)', () => {
    const result = isStatisticallySignificant(10, 5, 20);
    expect(result.confidence).toBe("high");
  });

  it('returns confidence "high" for sampleSize 100', () => {
    const result = isStatisticallySignificant(10, 5, 100);
    expect(result.confidence).toBe("high");
  });

  it("is not significant with sampleSize 20 when changePercent <= threshold", () => {
    // changePercent = |10.04 - 10| / 10 = 0.004 ≤ 0.05
    const result = isStatisticallySignificant(10.04, 10, 20, 0.05);
    expect(result.isSignificant).toBe(false);
    expect(result.confidence).toBe("high");
  });

  it("is significant with sampleSize 20 when changePercent > threshold", () => {
    // changePercent = |10.6 - 10| / 10 = 0.06 > 0.05
    const result = isStatisticallySignificant(10.6, 10, 20, 0.05);
    expect(result.isSignificant).toBe(true);
    expect(result.confidence).toBe("high");
  });

  // Special cases: zero baseline

  it("returns changePercent = 1 (100%) when baseline is 0 and current is non-zero", () => {
    // Should be significant with large enough sampleSize (1.0 > 0.05)
    const result = isStatisticallySignificant(5, 0, 20, 0.05);
    expect(result.isSignificant).toBe(true);
    expect(result.confidence).toBe("high");
  });

  it("returns changePercent = 0 when both baseline and current are 0", () => {
    // changePercent = 0, never significant
    const result = isStatisticallySignificant(0, 0, 20, 0.05);
    expect(result.isSignificant).toBe(false);
    expect(result.confidence).toBe("high");
  });

  it("uses the default confidenceThreshold of 0.05", () => {
    // changePercent = 0.06, default threshold 0.05 → significant with sampleSize 20
    const result = isStatisticallySignificant(10.6, 10, 20);
    expect(result.isSignificant).toBe(true);
    expect(result.confidence).toBe("high");
  });

  it("handles a custom confidenceThreshold of 0.10", () => {
    // changePercent = 0.06, threshold 0.10 → not significant with sampleSize 20
    const result = isStatisticallySignificant(10.6, 10, 20, 0.1);
    expect(result.isSignificant).toBe(false);
    expect(result.confidence).toBe("high");
  });

  it("handles negative change (current < baseline) using absolute value", () => {
    // changePercent = |8 - 10| / 10 = 0.2 > 0.05
    const result = isStatisticallySignificant(8, 10, 20, 0.05);
    expect(result.isSignificant).toBe(true);
    expect(result.confidence).toBe("high");
  });
});

// ── computeChangePercent ──────────────────────────────────────────

describe("computeChangePercent", () => {
  it("returns 0 when both baseline and current are 0", () => {
    expect(computeChangePercent(0, 0)).toBe(0);
  });

  it("returns 100 when baseline is 0 and current is positive", () => {
    expect(computeChangePercent(5, 0)).toBe(100);
  });

  it("returns 100 when baseline is 0 and current is negative", () => {
    expect(computeChangePercent(-5, 0)).toBe(100);
  });

  it("computes positive change correctly: (current - baseline) / |baseline| * 100", () => {
    // (120 - 100) / 100 * 100 = 20
    expect(computeChangePercent(120, 100)).toBeCloseTo(20);
  });

  it("computes negative change correctly", () => {
    // (80 - 100) / 100 * 100 = -20
    expect(computeChangePercent(80, 100)).toBeCloseTo(-20);
  });

  it("computes 0% change when current equals baseline", () => {
    expect(computeChangePercent(50, 50)).toBe(0);
  });

  it("handles a negative baseline correctly using absolute value", () => {
    // (−80 − (−100)) / |−100| * 100 = 20 / 100 * 100 = 20
    expect(computeChangePercent(-80, -100)).toBeCloseTo(20);
  });

  it("computes 100% increase from 1 to 2", () => {
    expect(computeChangePercent(2, 1)).toBeCloseTo(100);
  });

  it("computes -50% decrease from 10 to 5", () => {
    expect(computeChangePercent(5, 10)).toBeCloseTo(-50);
  });

  it("computes 200% increase from 1 to 3", () => {
    expect(computeChangePercent(3, 1)).toBeCloseTo(200);
  });
});

// ── hasEnoughData ─────────────────────────────────────────────────

describe("hasEnoughData", () => {
  it("returns true when sampleSize equals minimum", () => {
    expect(hasEnoughData(5, 5)).toBe(true);
  });

  it("returns true when sampleSize exceeds minimum", () => {
    expect(hasEnoughData(10, 5)).toBe(true);
  });

  it("returns false when sampleSize is one below minimum", () => {
    expect(hasEnoughData(4, 5)).toBe(false);
  });

  it("returns false when sampleSize is 0 and minimum is 1", () => {
    expect(hasEnoughData(0, 1)).toBe(false);
  });

  it("returns true when sampleSize is 0 and minimum is 0", () => {
    expect(hasEnoughData(0, 0)).toBe(true);
  });

  it("returns false when sampleSize is much less than minimum", () => {
    expect(hasEnoughData(1, 100)).toBe(false);
  });

  it("returns true when sampleSize is much greater than minimum", () => {
    expect(hasEnoughData(1000, 5)).toBe(true);
  });

  it("uses the SDK default basic threshold of 5 correctly", () => {
    expect(hasEnoughData(5, 5)).toBe(true);
    expect(hasEnoughData(4, 5)).toBe(false);
  });

  it("uses the SDK trend threshold of 10 correctly", () => {
    expect(hasEnoughData(10, 10)).toBe(true);
    expect(hasEnoughData(9, 10)).toBe(false);
  });

  it("uses the SDK significance threshold of 20 correctly", () => {
    expect(hasEnoughData(20, 20)).toBe(true);
    expect(hasEnoughData(19, 20)).toBe(false);
  });
});

// ── buildPeriodComparison ─────────────────────────────────────────

describe("buildPeriodComparison", () => {
  it("returns the correct shape with all expected fields", () => {
    const result = buildPeriodComparison(120, 100, 20);
    expect(result).toHaveProperty("currentValue");
    expect(result).toHaveProperty("baselineValue");
    expect(result).toHaveProperty("changePercent");
    expect(result).toHaveProperty("direction");
    expect(result).toHaveProperty("isSignificant");
  });

  it("preserves currentValue and baselineValue exactly", () => {
    const result = buildPeriodComparison(120, 100, 20);
    expect(result.currentValue).toBe(120);
    expect(result.baselineValue).toBe(100);
  });

  it("computes changePercent correctly", () => {
    const result = buildPeriodComparison(120, 100, 20);
    expect(result.changePercent).toBeCloseTo(20);
  });

  // Direction: stable when |changePercent| < 1

  it('direction is "stable" when changePercent is 0', () => {
    const result = buildPeriodComparison(100, 100, 20);
    expect(result.direction).toBe("stable");
  });

  it('direction is "stable" when |changePercent| < 1', () => {
    // changePercent = (100.5 - 100) / 100 * 100 = 0.5
    const result = buildPeriodComparison(100.5, 100, 20);
    expect(result.direction).toBe("stable");
  });

  it('direction is "stable" at exactly |changePercent| = 0.99 (just below 1)', () => {
    // (100.99 - 100) / 100 * 100 = 0.99
    const result = buildPeriodComparison(100.99, 100, 20);
    expect(result.direction).toBe("stable");
  });

  // lowerIsBetter = false (default): positive change → 'improving'

  it('direction is "improving" for positive change when lowerIsBetter is false', () => {
    const result = buildPeriodComparison(120, 100, 20, false);
    expect(result.direction).toBe("improving");
  });

  it('direction is "degrading" for negative change when lowerIsBetter is false', () => {
    const result = buildPeriodComparison(80, 100, 20, false);
    expect(result.direction).toBe("degrading");
  });

  // lowerIsBetter = true: negative change → 'improving', positive → 'degrading'

  it('direction is "improving" for negative change when lowerIsBetter is true', () => {
    const result = buildPeriodComparison(80, 100, 20, true);
    expect(result.direction).toBe("improving");
  });

  it('direction is "degrading" for positive change when lowerIsBetter is true', () => {
    const result = buildPeriodComparison(120, 100, 20, true);
    expect(result.direction).toBe("degrading");
  });

  // isSignificant delegates to isStatisticallySignificant

  it("isSignificant is false for small sampleSize (< 5)", () => {
    const result = buildPeriodComparison(120, 100, 3);
    expect(result.isSignificant).toBe(false);
  });

  it("isSignificant is true for large enough sampleSize with notable change", () => {
    const result = buildPeriodComparison(120, 100, 25, false, 0.05);
    expect(result.isSignificant).toBe(true);
  });

  it("isSignificant is false when change is negligible even with large sampleSize", () => {
    // changePercent = (100.01 - 100) / 100 * 100 = 0.01% → changeRatio = 0.0001 < 0.05
    const result = buildPeriodComparison(100.01, 100, 25, false, 0.05);
    expect(result.isSignificant).toBe(false);
  });

  it("stable direction takes precedence over lowerIsBetter flag", () => {
    // changePercent = 0.5, below the 1% threshold → should be stable regardless
    const resultLower = buildPeriodComparison(100.5, 100, 25, true);
    const resultHigher = buildPeriodComparison(100.5, 100, 25, false);
    expect(resultLower.direction).toBe("stable");
    expect(resultHigher.direction).toBe("stable");
  });

  it("uses default lowerIsBetter = false when omitted", () => {
    const resultDefault = buildPeriodComparison(120, 100, 20);
    const resultExplicit = buildPeriodComparison(120, 100, 20, false);
    expect(resultDefault.direction).toBe(resultExplicit.direction);
    expect(resultDefault.changePercent).toBe(resultExplicit.changePercent);
  });

  it('handles zero baseline: changePercent is 100, direction is "improving" (lowerIsBetter=false)', () => {
    const result = buildPeriodComparison(5, 0, 20, false);
    expect(result.changePercent).toBe(100);
    expect(result.direction).toBe("improving");
  });

  it('handles zero baseline and zero current: changePercent is 0, direction is "stable"', () => {
    const result = buildPeriodComparison(0, 0, 20, false);
    expect(result.changePercent).toBe(0);
    expect(result.direction).toBe("stable");
  });
});

// ── mean ──────────────────────────────────────────────────────────

describe("mean", () => {
  it("returns 0 for an empty array", () => {
    expect(mean([])).toBe(0);
  });

  it("returns the single element for a one-element array", () => {
    expect(mean([7])).toBe(7);
  });

  it("[2, 4, 6] → 4", () => {
    expect(mean([2, 4, 6])).toBe(4);
  });

  it("computes mean of [1, 2, 3, 4, 5] → 3", () => {
    expect(mean([1, 2, 3, 4, 5])).toBe(3);
  });

  it("handles all-zero array", () => {
    expect(mean([0, 0, 0])).toBe(0);
  });

  it("handles negative values", () => {
    expect(mean([-3, -1, -2])).toBeCloseTo(-2);
  });

  it("handles a mix of positive and negative values", () => {
    expect(mean([-5, 5])).toBeCloseTo(0);
  });

  it("handles floating point values", () => {
    expect(mean([1.5, 2.5, 3.0])).toBeCloseTo(7 / 3);
  });

  it("handles large arrays without overflow (sanity check)", () => {
    const values = Array.from({ length: 100 }, (_, i) => i + 1);
    // mean of 1..100 = 50.5
    expect(mean(values)).toBeCloseTo(50.5);
  });
});

// ── standardDeviation ─────────────────────────────────────────────

describe("standardDeviation", () => {
  it("returns 0 for an empty array", () => {
    expect(standardDeviation([])).toBe(0);
  });

  it("returns 0 for a single-element array", () => {
    expect(standardDeviation([42])).toBe(0);
  });

  it("returns 0 for two identical values", () => {
    expect(standardDeviation([5, 5])).toBe(0);
  });

  it("uses sample standard deviation (N-1 denominator) for [2, 4]", () => {
    // mean = 3, squared diffs = [1, 1], sum = 2, variance = 2/1 = 2, std = sqrt(2)
    expect(standardDeviation([2, 4])).toBeCloseTo(Math.sqrt(2));
  });

  it("computes sample std dev for [2, 4, 4, 4, 5, 5, 7, 9]", () => {
    // mean = 5, squared diffs = [9,1,1,1,0,0,4,16] = 32, variance = 32/7, std = sqrt(32/7)
    expect(standardDeviation([2, 4, 4, 4, 5, 5, 7, 9])).toBeCloseTo(Math.sqrt(32 / 7));
  });

  it("returns a positive value for [1, 2, 3, 4, 5]", () => {
    expect(standardDeviation([1, 2, 3, 4, 5])).toBeGreaterThan(0);
  });

  it("[1, 2, 3, 4, 5] sample std dev ≈ sqrt(2.5)", () => {
    // mean = 3, diffs^2 = [4, 1, 0, 1, 4] = 10, variance = 10/4 = 2.5
    expect(standardDeviation([1, 2, 3, 4, 5])).toBeCloseTo(Math.sqrt(2.5));
  });

  it("returns 0 for an array of identical values (any size)", () => {
    expect(standardDeviation([7, 7, 7, 7, 7])).toBeCloseTo(0);
  });

  it("handles negative values correctly", () => {
    // same spread as [1, 2, 3] shifted by -2: std should be same
    expect(standardDeviation([-1, 0, 1])).toBeCloseTo(standardDeviation([1, 2, 3]));
  });
});

// ── clamp ─────────────────────────────────────────────────────────

describe("clamp", () => {
  it("returns value when within range", () => {
    expect(clamp(5, 0, 10)).toBe(5);
  });

  it("returns min when value is below min", () => {
    expect(clamp(-5, 0, 10)).toBe(0);
  });

  it("returns max when value is above max", () => {
    expect(clamp(15, 0, 10)).toBe(10);
  });

  it("returns value equal to min (boundary — at min)", () => {
    expect(clamp(0, 0, 10)).toBe(0);
  });

  it("returns value equal to max (boundary — at max)", () => {
    expect(clamp(10, 0, 10)).toBe(10);
  });

  it("returns min when min equals max and value is less", () => {
    expect(clamp(-1, 5, 5)).toBe(5);
  });

  it("returns max when min equals max and value is greater", () => {
    expect(clamp(10, 5, 5)).toBe(5);
  });

  it("clamps score values to 0-100 range correctly (typical health score use)", () => {
    expect(clamp(105, 0, 100)).toBe(100);
    expect(clamp(-5, 0, 100)).toBe(0);
    expect(clamp(75, 0, 100)).toBe(75);
  });

  it("handles floating point values", () => {
    expect(clamp(5.7, 0.0, 10.0)).toBeCloseTo(5.7);
    expect(clamp(-0.1, 0.0, 1.0)).toBeCloseTo(0.0);
    expect(clamp(1.1, 0.0, 1.0)).toBeCloseTo(1.0);
  });

  it("handles negative ranges", () => {
    expect(clamp(-5, -10, -1)).toBe(-5);
    expect(clamp(0, -10, -1)).toBe(-1);
    expect(clamp(-15, -10, -1)).toBe(-10);
  });
});
