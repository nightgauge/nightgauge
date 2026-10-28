/**
 * Control bands over per-run cost (#1286): stability gate, tiers, diagnosis.
 */

import { describe, it, expect } from "vitest";
import {
  CONTROL_BAND_DEFINITION,
  ControlBandDefinitionSchema,
  assessBaselineStability,
  buildControlBandFinding,
  diagnoseCostExcursion,
  evaluateControlBand,
  type ControlBandDefinition,
} from "../../../src/analysis/health/controlBands.js";
import { standardDeviation } from "../../../src/analysis/health/statistics.js";
import shipped from "../../../src/analysis/health/control-bands.json";
import { makeExecutionRecord } from "./fixtures.js";

/** The shipped tiers, switched on, so a test can make each one fire. */
const ENABLED: ControlBandDefinition = { ...CONTROL_BAND_DEFINITION, enabled: true };

/** A stable 20-run baseline: mean 10, CV ~0.1, no drift. */
const BASELINE = Array.from({ length: 20 }, (_, i) => (i % 2 === 0 ? 9 : 11));
const SD = standardDeviation(BASELINE);
const at = (z: number) => [...BASELINE, 10 + z * SD];

describe("shipped tier file", () => {
  it("validates against the schema", () => {
    expect(ControlBandDefinitionSchema.safeParse(shipped).success).toBe(true);
  });

  it("ships dormant: the cost baseline has not been proven stable", () => {
    expect(CONTROL_BAND_DEFINITION.enabled).toBe(false);
    expect(evaluateControlBand(at(5)).tier).toBe("none");
    expect(evaluateControlBand(at(5)).reason).toBe("disabled");
  });

  it("rejects tiers out of order or not widening", () => {
    const swapped = { ...shipped, tiers: [...shipped.tiers].reverse() };
    expect(ControlBandDefinitionSchema.safeParse(swapped).success).toBe(false);
    const flat = { ...shipped, tiers: shipped.tiers.map((t) => ({ ...t, min_sigma: 2 })) };
    expect(ControlBandDefinitionSchema.safeParse(flat).success).toBe(false);
  });
});

describe("evaluateControlBand — tiers", () => {
  it.each([
    [1.5, "innermost", "record"],
    [2.5, "middle", "diagnose"],
    [3.5, "outermost", "finding"],
  ])("z=%s fires only the %s tier", (z, tier, action) => {
    const e = evaluateControlBand(at(z), ENABLED);
    expect(e.tier).toBe(tier);
    expect(e.action).toBe(action);
    expect(e.z).toBeCloseTo(z, 6);
  });

  it("does not fire inside the innermost band or below the mean", () => {
    expect(evaluateControlBand(at(0.5), ENABLED).tier).toBe("none");
    expect(evaluateControlBand(at(-4), ENABLED).tier).toBe("none");
  });

  it("evaluates against the rolling window, not all history", () => {
    // Old expensive runs fall outside the 20-run window.
    const e = evaluateControlBand([100, 100, 100, ...at(3.5)], ENABLED);
    expect(e.tier).toBe("outermost");
  });
});

describe("evaluateControlBand — never over an unproven baseline", () => {
  it("too few samples", () => {
    const e = evaluateControlBand([...BASELINE.slice(0, 10), 50], ENABLED);
    expect(e.tier).toBe("none");
    expect(e.reason).toMatch(/samples/);
  });

  it("zero variance", () => {
    const e = evaluateControlBand([...Array(20).fill(10), 50], ENABLED);
    expect(e.reason).toMatch(/zero variance/);
  });

  it("high coefficient of variation", () => {
    const noisy = Array.from({ length: 20 }, (_, i) => (i % 2 === 0 ? 1 : 19));
    const e = evaluateControlBand([...noisy, 500], ENABLED);
    expect(e.tier).toBe("none");
    expect(e.reason).toMatch(/cv/);
  });

  it("drifting baseline", () => {
    const drifting = [...Array(10).fill(9), ...Array(10).fill(11)].map((v, i) => v + (i % 2) * 0.1);
    const e = evaluateControlBand([...drifting, 50], ENABLED);
    expect(e.tier).toBe("none");
    expect(e.reason).toMatch(/drift/);
  });

  it("assessBaselineStability reports the stable case", () => {
    const s = assessBaselineStability(BASELINE, ENABLED);
    expect(s.stable).toBe(true);
    expect(s.mean).toBe(10);
    expect(s.driftSigma).toBe(0);
  });
});

describe("diagnoseCostExcursion — middle tier, read-only", () => {
  const run = [
    makeExecutionRecord({
      issueNumber: 9,
      stage: "feature-dev",
      costUsd: 8,
      retries: 2,
      model: "opus",
    }),
    makeExecutionRecord({ issueNumber: 9, stage: "pr-create", costUsd: 1, retries: 0 }),
  ];
  const baseline = [1, 2].flatMap((n) => [
    makeExecutionRecord({ issueNumber: n, stage: "feature-dev", costUsd: 2 }),
    makeExecutionRecord({ issueNumber: n, stage: "pr-create", costUsd: 1 }),
  ]);

  it("ranks stages by cost delta and counts retries", () => {
    const d = diagnoseCostExcursion(run, baseline);
    expect(d.stages[0]).toMatchObject({
      stage: "feature-dev",
      deltaUsd: 6,
      retries: 2,
      models: ["opus"],
    });
    expect(d.totalRetries).toBe(2);
  });

  it("does not mutate its inputs", () => {
    const before = JSON.stringify([run, baseline]);
    diagnoseCostExcursion(Object.freeze([...run]), Object.freeze([...baseline]));
    expect(JSON.stringify([run, baseline])).toBe(before);
  });

  it("feeds the outermost finding", () => {
    const e = evaluateControlBand(at(3.5), ENABLED);
    const f = buildControlBandFinding(e, diagnoseCostExcursion(run, baseline), "ch-9");
    expect(f).toMatchObject({ id: "ch-9", dimension: "cost-health", severity: "high" });
    expect(f.recommendation).toContain("feature-dev");
  });
});
