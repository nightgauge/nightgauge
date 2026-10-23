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

  it("rescales a mixed-size cohort into the target size instead of projecting it raw", () => {
    // The #1229 shape: a corpus whose expensive runs are expensive BECAUSE
    // they are large. Projected onto an S issue, the raw p75 lands on an L run.
    const cohort = [
      { sizeLabel: "S", totalCostUsd: 3 },
      { sizeLabel: "S", totalCostUsd: 4 },
      { sizeLabel: "M", totalCostUsd: 10 },
      { sizeLabel: "M", totalCostUsd: 12 },
      { sizeLabel: "L", totalCostUsd: 60 },
    ];
    // static(S)=2.70, static(M)=4.46, static(L)=7.68 — the shape of the real
    // TOKEN_BASELINES table, stubbed so the test states the weighting it relies on.
    const staticFor = (size: string) =>
      ({ XS: 2.7, S: 2.7, M: 4.46, L: 7.68, XL: 7.68 })[size] ?? null;

    const raw = computeHistoricalCalibration(cohort, "S", undefined);
    const scaled = computeHistoricalCalibration(cohort, "S", undefined, staticFor);

    expect(raw.source).toBe("all-sizes");
    expect(scaled.source).toBe("all-sizes-scaled");
    // Same cohort, same sample count — only the projection moved.
    expect(scaled.sampleCount).toBe(raw.sampleCount);
    // The L run enters an S projection at 60 * 2.70/7.68 = $21.09 rather than
    // $60, so it no longer out-ranks the runs it should sit below.
    expect(scaled.costUsd!).toBeLessThan(raw.costUsd!);
    expect(scaled.costUsd!).toBeCloseTo((12 * 2.7) / 4.46, 5);
  });

  it("rescales UP when the target size is larger than the cohort's typical run", () => {
    // The same defect in the other direction, and the one that actually matters
    // for a budget gate: a raw cross-size p75 UNDER-projects a large issue, so
    // the gate waves through the run most likely to blow the ceiling.
    const cohort = [
      { sizeLabel: "M", totalCostUsd: 8 },
      { sizeLabel: "M", totalCostUsd: 10 },
      { sizeLabel: "M", totalCostUsd: 12 },
      { sizeLabel: "S", totalCostUsd: 3 },
    ];
    const staticFor = (size: string) =>
      ({ XS: 2.7, S: 2.7, M: 4.46, L: 7.68, XL: 7.68 })[size] ?? null;

    const raw = computeHistoricalCalibration(cohort, "L", undefined);
    const scaled = computeHistoricalCalibration(cohort, "L", undefined, staticFor);

    expect(scaled.source).toBe("all-sizes-scaled");
    expect(scaled.costUsd!).toBeGreaterThan(raw.costUsd!);
  });

  it("leaves the projection alone when the cohort's typical size IS the target", () => {
    // Two sized M runs is below MIN_CALIBRATION_SAMPLES, so the cohort widens
    // to include the unsized runs — but its median sized run is still M.
    const cohort = [
      { sizeLabel: "M", totalCostUsd: 10 },
      { sizeLabel: "M", totalCostUsd: 20 },
      { sizeLabel: null, totalCostUsd: 30 },
      { sizeLabel: null, totalCostUsd: 40 },
    ];
    const staticFor = (size: string) =>
      ({ XS: 2.7, S: 2.7, M: 4.46, L: 7.68, XL: 7.68 })[size] ?? null;

    // Every run rescales from M to M, so the figure must be identical to the
    // unscaled one — a rescale that is a no-op must actually be a no-op.
    const raw = computeHistoricalCalibration(cohort, "M", undefined);
    const scaled = computeHistoricalCalibration(cohort, "M", undefined, staticFor);

    expect(scaled.source).toBe("all-sizes-scaled");
    expect(scaled.costUsd).toBe(raw.costUsd);
  });

  it("keeps unsized runs in the cohort, rescaled from the median sized run", () => {
    // 32 of 54 scored runs carry size:null in the real corpus — dropping them
    // would gut the cohort the fallback exists to provide.
    const cohort = [
      { sizeLabel: "M", totalCostUsd: 10 },
      { sizeLabel: null, totalCostUsd: 20 },
      { sizeLabel: null, totalCostUsd: 30 },
      { sizeLabel: null, totalCostUsd: 40 },
    ];
    const staticFor = (size: string) =>
      ({ XS: 2.7, S: 2.7, M: 4.46, L: 7.68, XL: 7.68 })[size] ?? null;

    const scaled = computeHistoricalCalibration(cohort, "S", undefined, staticFor);

    expect(scaled.sampleCount).toBe(4);
    // Every run rescales M -> S, including the three unsized ones.
    expect(scaled.costUsd!).toBeCloseTo((30 * 2.7) / 4.46, 5);
  });

  it("reports all-sizes UNSCALED rather than pretending, when no size can anchor it", () => {
    // Every run unsized: there is no median size to rescale from, so the
    // honest answer is the raw p75 labelled as raw — never a silent 1.0 factor
    // reported as though a rescale happened.
    const cohort = [
      { sizeLabel: null, totalCostUsd: 10 },
      { sizeLabel: null, totalCostUsd: 20 },
      { sizeLabel: null, totalCostUsd: 30 },
    ];
    const staticFor = (size: string) =>
      ({ XS: 2.7, S: 2.7, M: 4.46, L: 7.68, XL: 7.68 })[size] ?? null;

    const scaled = computeHistoricalCalibration(cohort, "S", undefined, staticFor);

    expect(scaled.source).toBe("all-sizes");
    expect(scaled.costUsd).toBe(20);
  });

  it("names the rescale in the operator-facing summary", async () => {
    mockedHistory.mockResolvedValue([...historyRuns("L", [60, 62]), ...historyRuns("M", [10, 12])]);

    const result = await runPreFlightBudgetCheck(
      { labels: ["size:S", "type:feature"], title: "Small docs fix" },
      75,
      "/workspace"
    );

    // "all sizes" alone would hide that the figure was moved into S's terms.
    expect(result.historicalSource).toBe("all-sizes-scaled");
    expect(result.summary).toContain("all sizes → S");
  });

  it("the shared size weighting varies by size and ignores the calibration table", async () => {
    // The weighting is the RELATIVE cost of sizes, so it must come from the
    // static baseline. Reading the calibrated total instead would feed the
    // calibrated number back into the correction that is supposed to be
    // independent of it — and would flatten the ratio to 1 whenever a cell is
    // calibrated, silently turning the rescale into a no-op.
    const { staticSizeWeighting } = await import("../../src/utils/budgetIntelligence");
    const { AutoModelSelector } = await import("@nightgauge/sdk");

    const snap = {
      metadata: { labels: [], title: "" },
      // A table whose cells are wildly cheap. If the weighting reads through to
      // it, every size collapses to the same figure and the ratios go flat.
      stageModelCalibration: {
        schema_version: "1" as const,
        updated_at: new Date().toISOString(),
        total_records_analyzed: 999,
        buckets: {
          "feature-dev": {
            sonnet: {
              median_cost_usd: 0.01,
              p25_cost_usd: 0.01,
              p75_cost_usd: 0.01,
              median_input_tokens: 1,
              median_output_tokens: 1,
              sample_count: 500,
              last_updated: new Date().toISOString(),
            },
          },
        },
      },
      mode: "elevated" as const,
      capturedAt: new Date().toISOString(),
      adapter: "claude",
      provider: "anthropic" as const,
      stageEfforts: {},
    };

    const weight = staticSizeWeighting(new AutoModelSelector(), snap, undefined);
    const s = weight("S");
    const m = weight("M");
    const l = weight("L");

    for (const v of [s, m, l]) expect(v).not.toBeNull();
    // Strictly ordered: this is the whole property the rescale depends on.
    expect(s!).toBeLessThan(m!);
    expect(m!).toBeLessThan(l!);
    // …and well clear of the $0.01 cell, proving the baseline was used.
    expect(s!).toBeGreaterThan(1);
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
