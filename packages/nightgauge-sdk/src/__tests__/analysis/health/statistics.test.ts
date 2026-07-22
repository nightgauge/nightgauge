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
} from "../../../analysis/health/statistics.js";

describe("computePercentile", () => {
  it("returns 0 for an empty array", () => {
    expect(computePercentile([], 50)).toBe(0);
  });

  it("returns the single element for a one-element array", () => {
    expect(computePercentile([42], 50)).toBe(42);
  });

  it("returns median (P50) of [1,2,3,4,5]", () => {
    expect(computePercentile([1, 2, 3, 4, 5], 50)).toBe(3);
  });

  it("returns near 4.8 for P95 of [1,2,3,4,5]", () => {
    expect(computePercentile([1, 2, 3, 4, 5], 95)).toBeCloseTo(4.8, 5);
  });

  it("returns first element for P0", () => {
    expect(computePercentile([10, 20, 30], 0)).toBe(10);
  });

  it("returns last element for P100", () => {
    expect(computePercentile([10, 20, 30], 100)).toBe(30);
  });
});

describe("computeTrend", () => {
  it("returns slope 0 and direction stable for fewer than 2 values", () => {
    expect(computeTrend([])).toEqual({ slope: 0, direction: "stable" });
    expect(computeTrend([5])).toEqual({ slope: 0, direction: "stable" });
  });

  it("returns positive slope and direction degrading for increasing series", () => {
    const result = computeTrend([1, 2, 3, 4, 5]);
    expect(result.slope).toBeGreaterThan(0);
    expect(result.direction).toBe("degrading");
  });

  it("returns negative slope and direction improving for decreasing series", () => {
    const result = computeTrend([5, 4, 3, 2, 1]);
    expect(result.slope).toBeLessThan(0);
    expect(result.direction).toBe("improving");
  });

  it("returns slope near 0 and direction stable for flat series", () => {
    const result = computeTrend([3, 3, 3, 3]);
    expect(result.slope).toBeCloseTo(0, 10);
    expect(result.direction).toBe("stable");
  });
});

describe("isStatisticallySignificant", () => {
  it("returns low confidence and not significant for sample size < 5", () => {
    const result = isStatisticallySignificant(110, 100, 3);
    expect(result.confidence).toBe("low");
    expect(result.isSignificant).toBe(false);
  });

  it("returns high confidence and significant for large sample with 10% change", () => {
    // sampleSize >= 20, changePercent = 0.10, threshold default 0.05
    const result = isStatisticallySignificant(110, 100, 20);
    expect(result.confidence).toBe("high");
    expect(result.isSignificant).toBe(true);
  });

  it("returns medium confidence for medium sample (10-19) requiring larger change", () => {
    // sampleSize 10, threshold 0.05*2 = 0.10; change of 5% should NOT be significant
    const smallChange = isStatisticallySignificant(105, 100, 10);
    expect(smallChange.confidence).toBe("medium");
    expect(smallChange.isSignificant).toBe(false);

    // change of 15% SHOULD be significant at medium sample
    const largeChange = isStatisticallySignificant(115, 100, 10);
    expect(largeChange.confidence).toBe("medium");
    expect(largeChange.isSignificant).toBe(true);
  });
});

describe("computeChangePercent", () => {
  it("returns 0 when current and baseline are both 0", () => {
    expect(computeChangePercent(0, 0)).toBe(0);
  });

  it("returns 100 when baseline is 0 and current is nonzero", () => {
    expect(computeChangePercent(5, 0)).toBe(100);
  });

  it("returns 0 when current equals baseline", () => {
    expect(computeChangePercent(50, 50)).toBe(0);
  });

  it("returns 100 for a doubling (50 to 100)", () => {
    expect(computeChangePercent(100, 50)).toBeCloseTo(100, 5);
  });

  it("returns -50 for a halving (100 to 50)", () => {
    expect(computeChangePercent(50, 100)).toBeCloseTo(-50, 5);
  });
});

describe("hasEnoughData", () => {
  it("returns true when sampleSize meets minimum", () => {
    expect(hasEnoughData(5, 5)).toBe(true);
  });

  it("returns false when sampleSize is below minimum", () => {
    expect(hasEnoughData(4, 5)).toBe(false);
  });
});

describe("mean", () => {
  it("returns 0 for an empty array", () => {
    expect(mean([])).toBe(0);
  });

  it("returns the correct average for [2,4,6]", () => {
    expect(mean([2, 4, 6])).toBe(4);
  });
});

describe("standardDeviation", () => {
  it("returns 0 for arrays with fewer than 2 elements", () => {
    expect(standardDeviation([])).toBe(0);
    expect(standardDeviation([5])).toBe(0);
  });

  it("returns correct sample standard deviation for [2,4,6]", () => {
    // sample stddev of [2,4,6]: mean=4, diffs=[-2,0,2], sq=[4,0,4], sum=8, /2=4, sqrt=2
    expect(standardDeviation([2, 4, 6])).toBeCloseTo(2, 10);
  });
});

describe("clamp", () => {
  it("returns the value unchanged when within range", () => {
    expect(clamp(5, 0, 10)).toBe(5);
  });

  it("returns min when value is below min", () => {
    expect(clamp(-3, 0, 10)).toBe(0);
  });

  it("returns max when value is above max", () => {
    expect(clamp(15, 0, 10)).toBe(10);
  });
});

describe("buildPeriodComparison", () => {
  it("shows improving direction when lowerIsBetter=true and value decreases", () => {
    // baseline 100 → current 80, change = -20%
    const result = buildPeriodComparison(80, 100, 25, true);
    expect(result.direction).toBe("improving");
    expect(result.changePercent).toBeCloseTo(-20, 5);
  });

  it("shows improving direction when lowerIsBetter=false and value increases", () => {
    // baseline 80 → current 100, change = +25%
    const result = buildPeriodComparison(100, 80, 25, false);
    expect(result.direction).toBe("improving");
    expect(result.changePercent).toBeCloseTo(25, 5);
  });

  it("shows degrading direction when lowerIsBetter=true and value increases", () => {
    const result = buildPeriodComparison(120, 100, 25, true);
    expect(result.direction).toBe("degrading");
  });

  it("shows stable direction when change is negligible (< 1%)", () => {
    // baseline 100, current 100.5 → changePercent 0.5%
    const result = buildPeriodComparison(100.5, 100, 25, false);
    expect(result.direction).toBe("stable");
  });

  it("populates currentValue, baselineValue, and isSignificant in result", () => {
    const result = buildPeriodComparison(110, 100, 25);
    expect(result.currentValue).toBe(110);
    expect(result.baselineValue).toBe(100);
    expect(typeof result.isSignificant).toBe("boolean");
  });
});
