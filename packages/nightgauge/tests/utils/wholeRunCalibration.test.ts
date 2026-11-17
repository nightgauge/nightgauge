import { describe, it, expect } from "vitest";
import {
  backtestWholeRunCalibration,
  computeWholeRunCalibration,
  modelBand,
  routingSignature,
  type CompletedRunCost,
} from "../../src/utils/wholeRunCalibration";

const OPUS = { "feature-planning": "claude-opus-5-5", "feature-dev": "claude-opus-5-5" };
const PLANNED = { "feature-planning": "opus", "feature-dev": "opus", "issue-pickup": "haiku" };

function run(
  size: string | null,
  cost: number,
  day: number,
  stageModels: Record<string, string> = OPUS
): CompletedRunCost {
  const d = String(day).padStart(2, "0");
  return { totalCostUsd: cost, sizeLabel: size, stageModels, recordedAt: `2026-10-${d}T00:00:00Z` };
}

describe("computeWholeRunCalibration (#3364)", () => {
  it("is the p75 of whole-run totals, so it never exceeds the costliest run", () => {
    // Three stages, each expensive (10) in a different run and cheap (1)
    // otherwise: per-stage p75s are 10 each and sum to 30, yet every run is 12.
    const runs = [run("M", 12, 1), run("M", 12, 2), run("M", 12, 3), run("M", 3, 4)];
    expect(computeWholeRunCalibration(runs, "M", PLANNED).costUsd).toBe(12);
  });

  it("does not match routing for runs with no models or an unplanned stage", () => {
    const runs = [1, 2, 3, 4, 5].map((d) => run("M", 2, d, { "pr-merge": "claude-opus-5-5" }));
    expect(computeWholeRunCalibration(runs, "M", PLANNED).source).toBe("same-size");
    const empty = [1, 2, 3, 4, 5].map((d) => run("M", 2, d, {}));
    expect(computeWholeRunCalibration(empty, "M", PLANNED).source).toBe("same-size");
  });

  it("differs by size on identical routing", () => {
    const runs = [
      ...[5, 6, 7].map((c, i) => run("S", c, i + 1)),
      ...[12, 13, 14].map((c, i) => run("M", c, i + 4)),
    ];
    const s = computeWholeRunCalibration(runs, "S", PLANNED);
    const m = computeWholeRunCalibration(runs, "M", PLANNED);
    expect(s.costUsd).toBe(7);
    expect(m.costUsd).toBe(14);
  });

  it("narrows to the same routing only at 5 samples", () => {
    const sonnet = { "feature-dev": "claude-sonnet-5-5" };
    const four = [1, 2, 3, 4].map((d) => run("M", 2, d));
    const others = [5, 6, 7].map((d) => run("M", 50, d, sonnet));
    expect(computeWholeRunCalibration([...four, ...others], "M", PLANNED).source).toBe("same-size");
    const five = [...four, run("M", 2, 8)];
    const est = computeWholeRunCalibration([...five, ...others], "M", PLANNED);
    expect(est.source).toBe("same-routing");
    expect(est.costUsd).toBe(2);
    expect(est.sampleCount).toBe(5);
  });

  it("reports none below 3 same-size runs, ignoring unsized runs", () => {
    const est = computeWholeRunCalibration(
      [run("M", 5, 1), run("M", 5, 2), run(null, 5, 3)],
      "M",
      PLANNED
    );
    expect(est).toEqual({ costUsd: null, sampleCount: 2, source: "none" });
  });

  it("keeps only the most recent window of runs", () => {
    const old = [1, 2, 3].map((d) => run("M", 100, d));
    const recent = [10, 11, 12].map((d) => run("M", 4, d));
    const est = computeWholeRunCalibration([...old, ...recent], "M", PLANNED, { window: 3 });
    expect(est.costUsd).toBe(4);
  });

  it("matches selector bands against recorded concrete model ids", () => {
    expect(modelBand("claude-opus-5-5")).toBe("opus");
    expect(modelBand("opus")).toBe("opus");
    expect(routingSignature({ b: "claude-sonnet-5-5", a: "opus", c: null })).toBe(
      "a=opus,b=sonnet"
    );
  });
});

describe("backtestWholeRunCalibration (#3364)", () => {
  it("replays each run against prior runs only", () => {
    const runs = [10, 10, 10, 10, 40].map((c, i) => run("M", c, i + 1));
    const bt = backtestWholeRunCalibration(runs);
    // Runs 4 and 5 have ≥ 3 priors (estimate 10): ratios 1.0 and 0.25.
    expect(bt.paired).toBe(2);
    expect(bt.withinTwoX).toBe(0.5);
    expect(bt.medianRatio).toBeCloseTo(0.625, 10);
  });

  it("honours since", () => {
    const runs = [10, 10, 10, 10, 40].map((c, i) => run("M", c, i + 1));
    expect(backtestWholeRunCalibration(runs, { since: "2026-10-05" }).paired).toBe(1);
  });

  it("returns a null median with nothing paired", () => {
    expect(backtestWholeRunCalibration([run("M", 1, 1)])).toEqual({
      paired: 0,
      withinTwoX: 0,
      medianRatio: null,
    });
  });
});
