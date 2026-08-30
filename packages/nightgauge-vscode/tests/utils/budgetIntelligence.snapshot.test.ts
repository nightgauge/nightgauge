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
    // The whole point of #112: the ceiling ratio reflects the historical cost,
    // not the (much cheaper) static estimate. Compared against
    // `staticEstimatedCost` because #1213 made `estimatedCost` the PUBLISHED
    // projection — which is now the p75 itself, so the two are equal by
    // construction. Same claim, against the figure that still means "static".
    expect(result.historicalCostUsd!).toBeGreaterThan(result.staticEstimatedCost);
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

describe("the PUBLISHED estimate is the calibrated one (#1213)", () => {
  beforeEach(() => {
    liveMode.value = "elevated";
    liveCalibration.value = null;
    vi.clearAllMocks();
    mockedHistory.mockResolvedValue([]);
  });

  // `estimatedCost` was ALWAYS `estimate.totalEstimatedCost` from the static
  // table, while the p75 whose own comment calls it "the correction that
  // carries the whole gate" (#112) was computed two lines above and used only
  // for the ceiling warning. So the number the notification rendered never
  // improved with history, and could not: an L/55-file issue estimated at
  // $14.62 landed at $63.86 (4.4x), an S/3-file docs issue estimated at $3.65
  // landed at $6.74 (1.8x), always in the same direction.

  it("publishes the historical p75, not the static figure, when the cohort qualifies", async () => {
    mockedHistory.mockResolvedValue(historyRuns("M", [20, 30, 40, 50, 107]));

    const result = await runPreFlightBudgetCheck(METADATA, 75, "/workspace");

    expect(result.estimateSource).toBe("historical-p75");
    expect(result.estimatedCost).toBe(50); // the p75, not the static table
    expect(result.staticEstimatedCost).toBeLessThan(50);
    // Published number and ceiling ratio are now the SAME number. They were
    // not: the gate warned on one figure and the notification rendered another.
    expect(result.ceilingRatio).toBeCloseTo(result.estimatedCost / 75, 5);
    expect(result.summary).toContain("(historical-p75)");
  });

  it("falls back to the static figure below the sample threshold", async () => {
    // Two comparable runs is below MIN_CALIBRATION_SAMPLES (3).
    mockedHistory.mockResolvedValue(historyRuns("M", [40, 60]));

    const result = await runPreFlightBudgetCheck(METADATA, 75, "/workspace");

    expect(result.estimateSource).toBe("static");
    expect(result.estimatedCost).toBe(result.staticEstimatedCost);
    expect(result.estimatedCost).toBeGreaterThan(0);
  });

  it("keeps the static figure when the cohort p75 is BELOW it", async () => {
    // The static table has never over-estimated in this corpus, so a p75 under
    // it means the cohort is not comparable — not that runs got cheap.
    mockedHistory.mockResolvedValue(historyRuns("M", [0.01, 0.02, 0.03, 0.04, 0.05]));

    const result = await runPreFlightBudgetCheck(METADATA, 75, "/workspace");

    expect(result.estimateSource).toBe("static");
    expect(result.estimatedCost).toBe(result.staticEstimatedCost);
  });

  it("always retains the static figure alongside the published one", async () => {
    mockedHistory.mockResolvedValue(historyRuns("M", [20, 30, 40, 50, 107]));
    const result = await runPreFlightBudgetCheck(METADATA, 75, "/workspace");
    // Without it, nothing downstream can measure how far off the uncalibrated
    // path was — which is the question `cost accuracy` exists to answer.
    expect(result.staticEstimatedCost).toBeGreaterThan(0);
    expect(result.staticEstimatedCost).not.toBe(result.estimatedCost);
  });

  it("pins the provider and per-stage efforts in the snapshot", async () => {
    // Both are read from config and env, so an estimate taken after a config
    // change would price a run against a provider it never dispatched to.
    const snap = await captureEstimatorInputs(METADATA, "/workspace");
    expect(snap.provider).toBeTruthy();
    expect(snap.adapter).toBeTruthy();
    expect(snap.stageEfforts).toBeDefined();
  });
});
