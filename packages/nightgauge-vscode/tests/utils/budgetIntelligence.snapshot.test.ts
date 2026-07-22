/**
 * #198 — the pre-flight estimator must compute under PINNED inputs.
 *
 * The math was always deterministic; the inputs were not: calibration was
 * re-loaded from disk per call, labels passed live, and the performance
 * mode re-read from disk. Two estimates for the same issue seconds apart
 * differed by 83% (bowlsheet#233). A snapshot captured at pipeline start
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
} from "../../src/utils/budgetIntelligence";

const METADATA = { labels: ["size:M", "type:feature"], title: "Test issue" };

describe("pre-flight estimator input snapshot (#198)", () => {
  beforeEach(() => {
    liveMode.value = "elevated";
    liveCalibration.value = null;
    vi.clearAllMocks();
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
