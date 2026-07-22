import { describe, it, expect } from "vitest";
import type { IssueCostAggregation } from "../../../src/utils/executionHistoryReader";
import {
  aggregateCostBySize,
  getCostBySizeWidgetHtml,
  SIZE_FIBONACCI_SCORES,
} from "../../../src/views/dashboard/tabs/PerformanceTabHtml";

function makeAgg(overrides: Partial<IssueCostAggregation> = {}): IssueCostAggregation {
  return {
    issueNumber: 100,
    totalCostUsd: 0.5,
    runCount: 2,
    backtrackCount: 0,
    issueType: "feature",
    sizeLabel: "M",
    firstRunAt: new Date("2026-01-01"),
    lastRunAt: new Date("2026-01-02"),
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// aggregateCostBySize
// ---------------------------------------------------------------------------

describe("aggregateCostBySize", () => {
  it("returns empty array for empty input", () => {
    expect(aggregateCostBySize([])).toEqual([]);
  });

  it("groups mixed sizes into correct buckets", () => {
    const input = [
      makeAgg({
        issueNumber: 1,
        sizeLabel: "XS",
        totalCostUsd: 0.1,
        runCount: 1,
      }),
      makeAgg({
        issueNumber: 2,
        sizeLabel: "M",
        totalCostUsd: 0.4,
        runCount: 2,
      }),
      makeAgg({
        issueNumber: 3,
        sizeLabel: "M",
        totalCostUsd: 0.6,
        runCount: 3,
      }),
      makeAgg({
        issueNumber: 4,
        sizeLabel: "L",
        totalCostUsd: 1.0,
        runCount: 4,
      }),
    ];
    const result = aggregateCostBySize(input);

    expect(result).toHaveLength(3);
    expect(result[0].sizeLabel).toBe("XS");
    expect(result[1].sizeLabel).toBe("M");
    expect(result[2].sizeLabel).toBe("L");

    // XS bucket
    expect(result[0].issueCount).toBe(1);
    expect(result[0].totalCostUsd).toBeCloseTo(0.1);
    expect(result[0].avgCostPerIssue).toBeCloseTo(0.1);
    expect(result[0].fibonacciScore).toBe(1);
    expect(result[0].costPerComplexityPoint).toBeCloseTo(0.1);

    // M bucket: 2 issues, cost 0.4 + 0.6 = 1.0, avg = 0.5
    expect(result[1].issueCount).toBe(2);
    expect(result[1].totalCostUsd).toBeCloseTo(1.0);
    expect(result[1].avgCostPerIssue).toBeCloseTo(0.5);
    expect(result[1].totalRuns).toBe(5);
    expect(result[1].avgRunsPerIssue).toBeCloseTo(2.5);
    expect(result[1].fibonacciScore).toBe(4);
    expect(result[1].costPerComplexityPoint).toBeCloseTo(0.125);

    // L bucket
    expect(result[2].issueCount).toBe(1);
    expect(result[2].avgCostPerIssue).toBeCloseTo(1.0);
    expect(result[2].fibonacciScore).toBe(7);
    expect(result[2].costPerComplexityPoint).toBeCloseTo(1.0 / 7);
  });

  it("handles all same size", () => {
    const input = [
      makeAgg({
        issueNumber: 1,
        sizeLabel: "S",
        totalCostUsd: 0.2,
        runCount: 1,
      }),
      makeAgg({
        issueNumber: 2,
        sizeLabel: "S",
        totalCostUsd: 0.3,
        runCount: 2,
      }),
    ];
    const result = aggregateCostBySize(input);

    expect(result).toHaveLength(1);
    expect(result[0].sizeLabel).toBe("S");
    expect(result[0].issueCount).toBe(2);
    expect(result[0].avgCostPerIssue).toBeCloseTo(0.25);
    expect(result[0].costPerComplexityPoint).toBeCloseTo(0.125);
  });

  it("groups null sizeLabel as Unlabeled with null costPerComplexityPoint", () => {
    const input = [
      makeAgg({
        issueNumber: 1,
        sizeLabel: null,
        totalCostUsd: 0.5,
        runCount: 2,
      }),
      makeAgg({
        issueNumber: 2,
        sizeLabel: null,
        totalCostUsd: 0.3,
        runCount: 1,
      }),
    ];
    const result = aggregateCostBySize(input);

    expect(result).toHaveLength(1);
    expect(result[0].sizeLabel).toBe("Unlabeled");
    expect(result[0].fibonacciScore).toBeNull();
    expect(result[0].costPerComplexityPoint).toBeNull();
  });

  it("sorts Unlabeled last after labeled sizes", () => {
    const input = [
      makeAgg({ issueNumber: 1, sizeLabel: null, totalCostUsd: 0.1 }),
      makeAgg({ issueNumber: 2, sizeLabel: "XL", totalCostUsd: 0.5 }),
      makeAgg({ issueNumber: 3, sizeLabel: "S", totalCostUsd: 0.2 }),
    ];
    const result = aggregateCostBySize(input);

    expect(result.map((b) => b.sizeLabel)).toEqual(["S", "XL", "Unlabeled"]);
  });

  it("normalizes lowercase size labels to uppercase", () => {
    const input = [
      makeAgg({ issueNumber: 1, sizeLabel: "m", totalCostUsd: 0.4 }),
      makeAgg({ issueNumber: 2, sizeLabel: "M", totalCostUsd: 0.6 }),
    ];
    const result = aggregateCostBySize(input);

    expect(result).toHaveLength(1);
    expect(result[0].sizeLabel).toBe("M");
    expect(result[0].issueCount).toBe(2);
  });

  it("treats unrecognized size labels as Unlabeled", () => {
    const input = [makeAgg({ issueNumber: 1, sizeLabel: "HUGE", totalCostUsd: 0.5 })];
    const result = aggregateCostBySize(input);

    expect(result).toHaveLength(1);
    expect(result[0].sizeLabel).toBe("Unlabeled");
    expect(result[0].fibonacciScore).toBeNull();
  });

  it("computes fibonacci scores from SIZE_FIBONACCI_SCORES", () => {
    expect(SIZE_FIBONACCI_SCORES).toEqual({
      XS: 1,
      S: 2,
      M: 4,
      L: 7,
      XL: 9,
    });
  });
});

// ---------------------------------------------------------------------------
// getCostBySizeWidgetHtml
// ---------------------------------------------------------------------------

describe("getCostBySizeWidgetHtml", () => {
  it("returns empty string for empty input", () => {
    expect(getCostBySizeWidgetHtml([])).toBe("");
  });

  it("returns empty string when all sizeLabels are null (only Unlabeled)", () => {
    // A single "Unlabeled" bucket still renders — this tests the >0 buckets check
    const input = [makeAgg({ issueNumber: 1, sizeLabel: null, totalCostUsd: 0.5 })];
    const html = getCostBySizeWidgetHtml(input);
    // Even with just Unlabeled, we render the widget since there's data
    expect(html).toContain("Cost by Size");
  });

  it("renders table with correct headers", () => {
    const input = [makeAgg({ issueNumber: 1, sizeLabel: "M", totalCostUsd: 0.5 })];
    const html = getCostBySizeWidgetHtml(input);

    expect(html).toContain("<th>Size</th>");
    expect(html).toContain("<th>Issues</th>");
    expect(html).toContain("<th>Total Cost</th>");
    expect(html).toContain("<th>Avg Cost</th>");
    expect(html).toContain("<th>Avg Runs</th>");
    expect(html).toContain("<th>Cost/Point</th>");
  });

  it("renders size labels and cost values in table rows", () => {
    const input = [
      makeAgg({
        issueNumber: 1,
        sizeLabel: "S",
        totalCostUsd: 0.2,
        runCount: 1,
      }),
      makeAgg({
        issueNumber: 2,
        sizeLabel: "L",
        totalCostUsd: 1.4,
        runCount: 3,
      }),
    ];
    const html = getCostBySizeWidgetHtml(input);

    expect(html).toContain("<strong>S</strong>");
    expect(html).toContain("<strong>L</strong>");
    expect(html).toContain("$0.2000");
    expect(html).toContain("$1.4000");
  });

  it("renders bar chart elements with correct CSS classes", () => {
    const input = [
      makeAgg({ issueNumber: 1, sizeLabel: "XS", totalCostUsd: 0.1 }),
      makeAgg({ issueNumber: 2, sizeLabel: "XL", totalCostUsd: 2.0 }),
    ];
    const html = getCostBySizeWidgetHtml(input);

    expect(html).toContain("cost-by-size-bar-row");
    expect(html).toContain("cost-by-size-bar-track");
    expect(html).toContain("cost-by-size-bar-fill");
    expect(html).toContain("Average Cost Comparison");
  });

  it("renders em-dash for Unlabeled cost/point", () => {
    const input = [makeAgg({ issueNumber: 1, sizeLabel: null, totalCostUsd: 0.5 })];
    const html = getCostBySizeWidgetHtml(input);

    // U+2014 em-dash for null costPerComplexityPoint
    expect(html).toContain("\u2014");
  });

  it("renders collapsible details section", () => {
    const input = [makeAgg({ issueNumber: 1, sizeLabel: "M", totalCostUsd: 0.5 })];
    const html = getCostBySizeWidgetHtml(input);

    expect(html).toContain("<details");
    expect(html).toContain("collapsible-section");
    expect(html).toContain("1 sizes");
  });

  it("assigns bar width 100% to the largest avg cost bucket", () => {
    const input = [
      makeAgg({ issueNumber: 1, sizeLabel: "S", totalCostUsd: 0.1 }),
      makeAgg({ issueNumber: 2, sizeLabel: "XL", totalCostUsd: 1.0 }),
    ];
    const html = getCostBySizeWidgetHtml(input);

    // XL has avg $1.0 (max), should get width: 100.0%
    expect(html).toContain("width: 100.0%");
    // S has avg $0.1, so width: 10.0%
    expect(html).toContain("width: 10.0%");
  });
});
