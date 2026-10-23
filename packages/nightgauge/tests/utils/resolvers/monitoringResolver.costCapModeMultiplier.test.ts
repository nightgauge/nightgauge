/**
 * monitoringResolver.costCapModeMultiplier.test.ts
 *
 * Tests for the per-mode cost-cap multiplier (Issue #3217).
 *
 * The base caps in `DEFAULT_STAGE_COST_CAPS` are calibrated for Sonnet at
 * medium effort under elevated mode (1.0×). The mode multiplier composes
 * multiplicatively atop the existing (model, effort) scale so that:
 *   - efficiency mode (0.5×) tightens the ceiling for cheaper modes
 *   - elevated mode (1.0×) preserves the calibrated baseline exactly —
 *     guarantees no regression for default users (AC #5)
 *   - maximum mode (2.0×) widens the ceiling for best-effort runs
 *
 * Composition: effectiveCap = baseCap × modelScale × modeMultiplier
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { vi } from "vitest";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: undefined,
  },
}));

import {
  DEFAULT_COST_CAP_MODE_MULTIPLIER,
  getCostCapModeMultiplier,
  getEffectiveStageCostCap,
} from "../../../src/utils/resolvers/monitoringResolver";

const ENV_KEYS = [
  "NIGHTGAUGE_COST_CAP_MODE_MULTIPLIER_EFFICIENCY",
  "NIGHTGAUGE_COST_CAP_MODE_MULTIPLIER_ELEVATED",
  "NIGHTGAUGE_COST_CAP_MODE_MULTIPLIER_MAXIMUM",
];

beforeEach(() => {
  for (const k of ENV_KEYS) delete process.env[k];
});

afterEach(() => {
  for (const k of ENV_KEYS) delete process.env[k];
});

describe("DEFAULT_COST_CAP_MODE_MULTIPLIER table", () => {
  it("efficiency halves the calibrated baseline", () => {
    expect(DEFAULT_COST_CAP_MODE_MULTIPLIER.efficiency).toBe(0.5);
  });

  it("elevated is identity — preserves pre-#3217 math (AC #5 anchor)", () => {
    expect(DEFAULT_COST_CAP_MODE_MULTIPLIER.elevated).toBe(1.0);
  });

  it("maximum doubles the calibrated baseline", () => {
    expect(DEFAULT_COST_CAP_MODE_MULTIPLIER.maximum).toBe(2.0);
  });
});

describe("getCostCapModeMultiplier — defaults", () => {
  it("returns 1.0 when mode is undefined (defensive default)", () => {
    expect(getCostCapModeMultiplier(undefined)).toBe(1.0);
  });

  it("returns the efficiency default when mode=efficiency", () => {
    expect(getCostCapModeMultiplier("efficiency")).toBe(0.5);
  });

  it("returns the elevated default when mode=elevated", () => {
    expect(getCostCapModeMultiplier("elevated")).toBe(1.0);
  });

  it("returns the maximum default when mode=maximum", () => {
    expect(getCostCapModeMultiplier("maximum")).toBe(2.0);
  });
});

describe("getCostCapModeMultiplier — env-var overrides", () => {
  it("env override NIGHTGAUGE_COST_CAP_MODE_MULTIPLIER_MAXIMUM=3.0 wins", () => {
    process.env.NIGHTGAUGE_COST_CAP_MODE_MULTIPLIER_MAXIMUM = "3.0";
    expect(getCostCapModeMultiplier("maximum")).toBe(3.0);
  });

  it("env override NIGHTGAUGE_COST_CAP_MODE_MULTIPLIER_EFFICIENCY=0.75 wins", () => {
    process.env.NIGHTGAUGE_COST_CAP_MODE_MULTIPLIER_EFFICIENCY = "0.75";
    expect(getCostCapModeMultiplier("efficiency")).toBe(0.75);
  });

  it("ignores non-numeric env values, falls through to default", () => {
    process.env.NIGHTGAUGE_COST_CAP_MODE_MULTIPLIER_MAXIMUM = "not-a-number";
    expect(getCostCapModeMultiplier("maximum")).toBe(2.0);
  });

  it("ignores zero / negative env values (would zero-out the cap)", () => {
    process.env.NIGHTGAUGE_COST_CAP_MODE_MULTIPLIER_MAXIMUM = "0";
    expect(getCostCapModeMultiplier("maximum")).toBe(2.0);
    process.env.NIGHTGAUGE_COST_CAP_MODE_MULTIPLIER_MAXIMUM = "-1.5";
    expect(getCostCapModeMultiplier("maximum")).toBe(2.0);
  });

  it("env override for one mode does not leak to other modes", () => {
    process.env.NIGHTGAUGE_COST_CAP_MODE_MULTIPLIER_MAXIMUM = "5.0";
    expect(getCostCapModeMultiplier("maximum")).toBe(5.0);
    expect(getCostCapModeMultiplier("elevated")).toBe(1.0);
    expect(getCostCapModeMultiplier("efficiency")).toBe(0.5);
  });
});

describe("getEffectiveStageCostCap — 3 modes × 2 stages × 2 model-tiers matrix (AC #4)", () => {
  // Matrix definition (12 cells):
  //   modes:        efficiency (0.5×), elevated (1.0×), maximum (2.0×)
  //   stages:       feature-dev (base $23), pr-create (base $3)
  //   model tiers:  sonnet:medium (1.0×), opus:high (5.0×)
  //
  // Expected effectiveCap = base × modelScale × modeMultiplier.
  type Cell = {
    mode: "efficiency" | "elevated" | "maximum";
    stage: string;
    baseCap: number;
    model: string;
    effort: string;
    modelScale: number;
    modeMultiplier: number;
  };

  const matrix: Cell[] = [];
  const modes: Array<{
    name: "efficiency" | "elevated" | "maximum";
    multiplier: number;
  }> = [
    { name: "efficiency", multiplier: 0.5 },
    { name: "elevated", multiplier: 1.0 },
    { name: "maximum", multiplier: 2.0 },
  ];
  const stages: Array<{ name: string; baseCap: number }> = [
    { name: "feature-dev", baseCap: 23.0 },
    { name: "pr-create", baseCap: 3.0 },
  ];
  const tiers: Array<{ model: string; effort: string; scale: number }> = [
    { model: "claude-sonnet-4-6", effort: "medium", scale: 1.0 },
    { model: "claude-opus-4-7", effort: "high", scale: 5.0 },
  ];

  for (const m of modes) {
    for (const s of stages) {
      for (const t of tiers) {
        matrix.push({
          mode: m.name,
          stage: s.name,
          baseCap: s.baseCap,
          model: t.model,
          effort: t.effort,
          modelScale: t.scale,
          modeMultiplier: m.multiplier,
        });
      }
    }
  }

  it.each(matrix)(
    "[$mode][$stage][$model/$effort] effectiveCap = base($baseCap) × modelScale($modelScale) × modeMultiplier($modeMultiplier)",
    (cell) => {
      const result = getEffectiveStageCostCap(
        cell.stage,
        { model: cell.model, effort: cell.effort },
        undefined,
        cell.mode
      );
      const expected = cell.baseCap * cell.modelScale * cell.modeMultiplier;

      expect(result.baseCap).toBe(cell.baseCap);
      expect(result.scale).toBe(cell.modelScale);
      expect(result.modeMultiplier).toBe(cell.modeMultiplier);
      expect(result.effectiveCap).toBeCloseTo(expected, 6);
    }
  );

  it.each(matrix)(
    "[$mode][$stage][$model/$effort] is idempotent — calling twice returns the same value",
    (cell) => {
      const a = getEffectiveStageCostCap(
        cell.stage,
        { model: cell.model, effort: cell.effort },
        undefined,
        cell.mode
      );
      const b = getEffectiveStageCostCap(
        cell.stage,
        { model: cell.model, effort: cell.effort },
        undefined,
        cell.mode
      );
      expect(a).toEqual(b);
    }
  );
});

describe("getEffectiveStageCostCap — AC #5 regression guard", () => {
  // AC #5: elevated mode must produce identical numbers to omitting the
  // mode argument entirely. A 1.0× multiplier guarantees existing call
  // sites (which still pass three args) see no behavior change.
  const cases: Array<{ stage: string; model: string; effort: string }> = [
    { stage: "feature-dev", model: "claude-sonnet-4-6", effort: "medium" },
    { stage: "feature-dev", model: "claude-opus-4-7", effort: "high" },
    { stage: "pr-create", model: "claude-sonnet-4-6", effort: "medium" },
    { stage: "pr-create", model: "claude-opus-4-7", effort: "high" },
  ];

  it.each(cases)(
    "[$stage][$model/$effort] elevated mode == undefined mode (no regression)",
    ({ stage, model, effort }) => {
      const elevated = getEffectiveStageCostCap(stage, { model, effort }, undefined, "elevated");
      const omitted = getEffectiveStageCostCap(stage, { model, effort });

      expect(elevated.baseCap).toBe(omitted.baseCap);
      expect(elevated.scale).toBe(omitted.scale);
      expect(elevated.modeMultiplier).toBe(omitted.modeMultiplier);
      expect(elevated.effectiveCap).toBe(omitted.effectiveCap);
    }
  );
});

describe("getEffectiveStageCostCap — uncapped stages (baseCap === 0)", () => {
  it("returns modeMultiplier=1.0 even when caller passes maximum mode", () => {
    // A stage with no calibrated cap (e.g. pipeline-start) stays uncapped —
    // the mode multiplier never resurrects a disabled cap.
    const result = getEffectiveStageCostCap(
      "pipeline-start",
      { model: "claude-opus-4-7", effort: "high" },
      undefined,
      "maximum"
    );
    expect(result.baseCap).toBe(0);
    expect(result.scale).toBe(1.0);
    expect(result.modeMultiplier).toBe(1.0);
    expect(result.effectiveCap).toBe(0);
  });
});

describe("getEffectiveStageCostCap — env override composition", () => {
  it("env multiplier composes multiplicatively with model scale", () => {
    process.env.NIGHTGAUGE_COST_CAP_MODE_MULTIPLIER_MAXIMUM = "3.0";
    const result = getEffectiveStageCostCap(
      "feature-dev",
      { model: "claude-opus-4-7", effort: "high" },
      undefined,
      "maximum"
    );
    // base $23 × opus:high 5.0× × env override 3.0× = $345
    expect(result.baseCap).toBe(23.0);
    expect(result.scale).toBe(5.0);
    expect(result.modeMultiplier).toBe(3.0);
    expect(result.effectiveCap).toBeCloseTo(345.0, 6);
  });
});
