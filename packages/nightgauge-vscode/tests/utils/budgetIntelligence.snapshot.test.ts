/**
 * #198 — the pre-flight estimator must compute under PINNED inputs.
 *
 * The math was always deterministic; the inputs were not: calibration was
 * re-loaded from disk per call, labels passed live, and the performance
 * mode re-read from disk. Two estimates for the same issue seconds apart
 * differed by 83% (the dogfood pr-merge deadlock). A snapshot captured at pipeline start
 * must make repeat estimates identical even when the live inputs change.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";

const { liveMode, liveCalibration } = vi.hoisted(() => ({
  liveMode: { value: "elevated" as string },
  liveCalibration: { value: null as unknown },
}));

vi.mock("@nightgauge/sdk", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@nightgauge/sdk")>();
  return {
    ...actual,
    CalibrationService: {
      ...actual.CalibrationService,
      getDefaultPath: vi.fn().mockReturnValue("/tmp/calibration.json"),
      load: vi.fn().mockImplementation(async () => liveCalibration.value),
    },
  };
});

vi.mock("../../src/utils/resolvers/monitoringResolver", () => ({
  getPerformanceMode: vi.fn(() => liveMode.value),
}));

vi.mock("../../src/utils/executionHistoryReader", () => ({
  ExecutionHistoryReader: {
    getCostByIssue: vi.fn().mockResolvedValue([]),
  },
}));

import {
  runPreFlightBudgetCheck,
  captureEstimatorInputs,
  computeHistoricalCalibration,
} from "../../src/utils/budgetIntelligence";
import { ExecutionHistoryReader } from "../../src/utils/executionHistoryReader";

const METADATA = { labels: ["size:M", "type:feature"], title: "Test issue" };

/** Build `getCostByIssue` aggregations with the given costs and size label. */
function historyRuns(sizeLabel: string | null, costs: number[]) {
  return costs.map((totalCostUsd, i) => ({
    issueNumber: 1000 + i,
    totalCostUsd,
    runCount: 1,
    backtrackCount: 0,
    issueType: "feature",
    sizeLabel,
    firstRunAt: new Date(),
    lastRunAt: new Date(),
  }));
}

const mockedHistory = vi.mocked(ExecutionHistoryReader.getCostByIssue);

describe("pre-flight estimator input snapshot (#198)", () => {
  beforeEach(() => {
    liveMode.value = "elevated";
    liveCalibration.value = null;
    vi.clearAllMocks();
    mockedHistory.mockResolvedValue([]);
  });

  it("captures labels defensively — later label mutation cannot shift the estimate", async () => {
    const metadata = { labels: ["size:XS"], title: "Tiny" };
    const snapshot = await captureEstimatorInputs(metadata, "/workspace");

    // issue-pickup applies size labels mid-run — the snapshot must not see it.
    metadata.labels.push("size:XL");
    metadata.labels.shift();

    expect(snapshot.metadata.labels).toEqual(["size:XS"]);
    expect(snapshot.capturedAt).toBeTruthy();
  });

  it("two checks under the SAME snapshot produce identical estimates despite live-input drift", async () => {
    const snapshot = await captureEstimatorInputs(METADATA, "/workspace");

    const first = await runPreFlightBudgetCheck(
      METADATA,
      20,
      "/workspace",
      undefined,
      undefined,
      snapshot
    );

    // A run finishing in between rewrites the calibration bucket and the
    // operator flips the performance mode — the pinned snapshot must win.
    liveMode.value = "maximum";
    liveCalibration.value = {
      version: 1,
      buckets: {
        "maximum:M": { medianCostUsd: 99, sampleCount: 50 },
      },
    };

    const second = await runPreFlightBudgetCheck(
      METADATA,
      20,
      "/workspace",
      undefined,
      undefined,
      snapshot
    );

    expect(second.estimatedCost).toBe(first.estimatedCost);
    expect(second.complexity).toBe(first.complexity);
    expect(second.snapshot.capturedAt).toBe(snapshot.capturedAt);
    expect(second.snapshot.mode).toBe("elevated");
  });

  it("without a caller snapshot, the check still pins one consistent set and returns it", async () => {
    const result = await runPreFlightBudgetCheck(METADATA, 20, "/workspace");
    expect(result.snapshot).toBeTruthy();
    expect(result.snapshot.mode).toBe("elevated");
    expect(result.snapshot.metadata.title).toBe("Test issue");
  });
});

/**
 * #112 — every run-history record written through the IPC path carried
 * `"size": null`, so the same-size join never matched, the historical override
 * stayed null, and the projection collapsed to the raw static estimate.
 * Estimates then ran a median 3.9x under actual across 112 runs.
 */
describe("pre-flight historical cost calibration (#112)", () => {
  beforeEach(() => {
    liveMode.value = "elevated";
    liveCalibration.value = null;
    vi.clearAllMocks();
    mockedHistory.mockResolvedValue([]);
  });

  it("projects ABOVE the static estimate when history for the size label is expensive", async () => {
    mockedHistory.mockResolvedValue(historyRuns("M", [20, 30, 40, 50, 107]));

    const result = await runPreFlightBudgetCheck(METADATA, 75, "/workspace");

    expect(result.historicalSource).toBe("same-size");
    expect(result.historicalSampleCount).toBe(5);
    expect(result.historicalCostUsd).toBe(50); // p75 of the cohort
    // The whole point of the fix: the ceiling ratio reflects the historical
    // cost, not the (much cheaper) static estimate.
    expect(result.historicalCostUsd!).toBeGreaterThan(result.estimatedCost);
    expect(result.ceilingRatio).toBeCloseTo(50 / 75, 5);
    expect(result.summary).toContain("Historical p75 (M): $50.00 (5 runs)");
  });

  it("falls back to the cross-size cohort when legacy records carry a null size", async () => {
    mockedHistory.mockResolvedValue(historyRuns(null, [10, 20, 30, 40]));

    const result = await runPreFlightBudgetCheck(METADATA, 75, "/workspace");

    expect(result.historicalSource).toBe("all-sizes");
    expect(result.historicalSampleCount).toBe(4);
    expect(result.historicalCostUsd).toBe(30);
    expect(result.summary).toContain("Historical p75 (all sizes): $30.00 (4 runs)");
  });

  it("says UNCALIBRATED out loud instead of silently omitting the segment", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    mockedHistory.mockResolvedValue(historyRuns("M", [12]));

    const result = await runPreFlightBudgetCheck(METADATA, 75, "/workspace");

    expect(result.historicalCostUsd).toBeNull();
    expect(result.historicalSource).toBe("none");
    expect(result.summary).toContain("Historical p75: UNCALIBRATED (1 usable runs)");
    expect(warn).toHaveBeenCalledWith(expect.stringContaining("calibration OFF"));
    // Uncalibrated must still fall through to the static estimate, not to zero.
    expect(result.ceilingRatio).toBeCloseTo(result.estimatedCost / 75, 5);
    warn.mockRestore();
  });

  it("prefers p75 over the mean so the long right tail is not averaged away", () => {
    const costs = [1, 1, 1, 1, 1, 1, 1, 1, 1, 107];
    const mean = costs.reduce((a, b) => a + b, 0) / costs.length; // 11.6
    const calibrated = computeHistoricalCalibration(
      costs.map((totalCostUsd) => ({ sizeLabel: "M", totalCostUsd })),
      "M"
    );

    // Symmetric case: the mean is dragged by the single outlier, p75 is not.
    expect(calibrated.costUsd).toBe(1);
    expect(calibrated.costUsd!).toBeLessThan(mean);

    // …and on the realistic long-tail shape, p75 sits ABOVE the mean.
    const tailed = [2, 3, 4, 5, 6, 40, 60, 107];
    const tailedMean = tailed.reduce((a, b) => a + b, 0) / tailed.length;
    const tailedCalibrated = computeHistoricalCalibration(
      tailed.map((totalCostUsd) => ({ sizeLabel: "M", totalCostUsd })),
      "M"
    );
    expect(tailedCalibrated.costUsd!).toBeGreaterThan(tailedMean);
  });

  it("ignores zero-cost records so they cannot deflate the percentile", () => {
    const calibrated = computeHistoricalCalibration(
      [
        { sizeLabel: "M", totalCostUsd: 0 },
        { sizeLabel: "M", totalCostUsd: 0 },
        { sizeLabel: "M", totalCostUsd: 10 },
        { sizeLabel: "M", totalCostUsd: 20 },
        { sizeLabel: "M", totalCostUsd: 30 },
      ],
      "M"
    );

    expect(calibrated.sampleCount).toBe(3);
    expect(calibrated.costUsd).toBe(20);
  });
});
