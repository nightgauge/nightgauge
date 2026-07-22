/**
 * monitoringResolver.costCapModelScale.test.ts
 *
 * Tests for the mode-aware cost-cap multiplier (Issue #3180, recalibrated
 * post-2026-05-04 incident).
 *
 * The base caps in `DEFAULT_STAGE_COST_CAPS` are calibrated for Sonnet at
 * medium effort. When the model router escalates (Opus, Maximum mode, etc.)
 * the multiplier widens the effective cap so heavier modes get proportional
 * headroom. The current calibration is anchored to two confirmed real-world
 * MAXIMUM-mode terminations: feature-dev hit $25.31 final / $23.03 at-kill
 * (issue #871) and pr-create hit $5.74 against $4.50 (issue #331).
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { vi } from "vitest";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: undefined,
  },
}));

import {
  COST_CAP_MODEL_SCALE,
  getCostCapModelScale,
  getEffectiveStageCostCap,
} from "../../../src/utils/resolvers/monitoringResolver";

describe("COST_CAP_MODEL_SCALE table", () => {
  it("scales Sonnet medium at 1.0× (the base calibration)", () => {
    expect(COST_CAP_MODEL_SCALE["sonnet"]).toBe(1.0);
  });

  it("scales Sonnet high at 1.3× (modest extended-thinking premium)", () => {
    expect(COST_CAP_MODEL_SCALE["sonnet:high"]).toBe(1.3);
  });

  it("scales Opus medium at 3.5× (Opus token premium, recalibrated)", () => {
    expect(COST_CAP_MODEL_SCALE["opus"]).toBe(3.5);
  });

  it("scales Opus high at 5.0× — heavy-mode headroom over the calibrated base", () => {
    // Pre-#3208 anchor: 2026-05-04 #871 hit $25.31 final on a $5 base × 5.0×
    // = $25 effective ceiling (just at the peak). Post-#3208 the base widened
    // to $23 (p95 × 2), so the same 5.0× now yields $115 effective for opus:high.
    expect(COST_CAP_MODEL_SCALE["opus:high"]).toBe(5.0);
  });

  it("scales Haiku at 1.0× (smaller model, cap not the bottleneck)", () => {
    expect(COST_CAP_MODEL_SCALE["haiku"]).toBe(1.0);
  });

  it("scales Fable medium at 7.0× and Fable high at 10.0× (~2× Opus premium)", () => {
    // Fable 5 is ~2× Opus pricing, so its cost-cap headroom is ~2× Opus's —
    // otherwise a Sonnet-calibrated cap would kill legitimate frontier runs.
    expect(COST_CAP_MODEL_SCALE["fable"]).toBe(7.0);
    expect(COST_CAP_MODEL_SCALE["fable:high"]).toBe(10.0);
  });

  it("gives xhigh MORE headroom than high — never less (#73)", () => {
    // xhigh thinks longer than high on the same per-token pricing. If its
    // multiplier were lower, a legitimately deeper run would be killed
    // earlier than a shallower one — inverted incentives.
    expect(COST_CAP_MODEL_SCALE["opus:xhigh"]).toBeGreaterThan(COST_CAP_MODEL_SCALE["opus:high"]);
    expect(COST_CAP_MODEL_SCALE["fable:xhigh"]).toBeGreaterThan(COST_CAP_MODEL_SCALE["fable:high"]);
  });
});

describe("getCostCapModelScale — model family resolution", () => {
  it("matches sonnet via family substring (e.g. claude-sonnet-4-6)", () => {
    expect(getCostCapModelScale("claude-sonnet-4-6")).toBe(1.0);
    expect(getCostCapModelScale("claude-sonnet-4-6", "high")).toBe(1.3);
  });

  it("matches opus via family substring", () => {
    expect(getCostCapModelScale("claude-opus-4-7")).toBe(3.5);
    expect(getCostCapModelScale("claude-opus-4-7", "high")).toBe(5.0);
  });

  it("matches fable via family substring (e.g. claude-fable-5)", () => {
    expect(getCostCapModelScale("claude-fable-5")).toBe(7.0);
    expect(getCostCapModelScale("claude-fable-5", "high")).toBe(10.0);
  });

  it("resolves xhigh via the effort-keyed lookup (#73)", () => {
    expect(getCostCapModelScale("claude-fable-5", "xhigh")).toBe(12.0);
    expect(getCostCapModelScale("claude-opus-4-8", "xhigh")).toBe(6.0);
    // An effort with no keyed entry still falls back to the family value.
    expect(getCostCapModelScale("claude-sonnet-5", "xhigh")).toBe(1.0);
  });

  it("matches haiku via family substring", () => {
    expect(getCostCapModelScale("claude-haiku-4-5")).toBe(1.0);
  });

  it("is case-insensitive on model and effort", () => {
    expect(getCostCapModelScale("CLAUDE-OPUS-4-7", "HIGH")).toBe(5.0);
    expect(getCostCapModelScale("Sonnet", "High")).toBe(1.3);
  });

  it("returns 1.0 for unknown families (gpt-5, lm-studio, etc.)", () => {
    expect(getCostCapModelScale("gpt-5")).toBe(1.0);
    expect(getCostCapModelScale("lmstudio-community/Meta-Llama-3.1-8B")).toBe(1.0);
    expect(getCostCapModelScale("gemini-2.0-pro", "high")).toBe(1.0);
  });

  it("returns 1.0 when model is undefined", () => {
    expect(getCostCapModelScale(undefined)).toBe(1.0);
    expect(getCostCapModelScale(undefined, "high")).toBe(1.0);
  });

  it("returns the family scale (not high-tier) when effort is missing or non-high", () => {
    expect(getCostCapModelScale("opus")).toBe(3.5);
    expect(getCostCapModelScale("opus", undefined)).toBe(3.5);
    expect(getCostCapModelScale("opus", "")).toBe(3.5);
    expect(getCostCapModelScale("opus", "medium")).toBe(3.5);
    expect(getCostCapModelScale("opus", "low")).toBe(3.5);
  });
});

describe("getCostCapModelScale — env-var overrides", () => {
  const ENV_KEYS = [
    "NIGHTGAUGE_BUDGET_MODEL_SCALE_OPUS_HIGH",
    "NIGHTGAUGE_BUDGET_MODEL_SCALE_OPUS",
    "NIGHTGAUGE_BUDGET_MODEL_SCALE_SONNET_HIGH",
    "NIGHTGAUGE_BUDGET_MODEL_SCALE_SONNET",
    "NIGHTGAUGE_BUDGET_MODEL_SCALE_HAIKU",
  ];

  beforeEach(() => {
    for (const k of ENV_KEYS) delete process.env[k];
  });

  afterEach(() => {
    for (const k of ENV_KEYS) delete process.env[k];
  });

  it("env override NIGHTGAUGE_BUDGET_MODEL_SCALE_OPUS_HIGH wins over the table", () => {
    process.env.NIGHTGAUGE_BUDGET_MODEL_SCALE_OPUS_HIGH = "7.5";
    expect(getCostCapModelScale("opus", "high")).toBe(7.5);
  });

  it("family-level env override applies to non-high effort", () => {
    process.env.NIGHTGAUGE_BUDGET_MODEL_SCALE_OPUS = "4.0";
    expect(getCostCapModelScale("opus", "medium")).toBe(4.0);
  });

  it("ignores non-numeric env values, falling back to the table", () => {
    process.env.NIGHTGAUGE_BUDGET_MODEL_SCALE_OPUS_HIGH = "not-a-number";
    expect(getCostCapModelScale("opus", "high")).toBe(5.0);
  });

  it("ignores zero / negative env values (would zero-out the cap)", () => {
    process.env.NIGHTGAUGE_BUDGET_MODEL_SCALE_OPUS_HIGH = "0";
    expect(getCostCapModelScale("opus", "high")).toBe(5.0);
    process.env.NIGHTGAUGE_BUDGET_MODEL_SCALE_OPUS_HIGH = "-2";
    expect(getCostCapModelScale("opus", "high")).toBe(5.0);
  });

  it("high-effort env override does NOT leak to non-high effort", () => {
    process.env.NIGHTGAUGE_BUDGET_MODEL_SCALE_OPUS_HIGH = "9.0";
    expect(getCostCapModelScale("opus", "medium")).toBe(3.5);
  });
});

describe("getEffectiveStageCostCap", () => {
  // Issue #3208 calibration: feature-dev base = $23 (p95 $11.25 × 2 over 90d).
  it("returns baseCap × scale × effectiveCap for Opus high (recalibrated #3208)", () => {
    const result = getEffectiveStageCostCap("feature-dev", {
      model: "claude-opus-4-7",
      effort: "high",
    });
    expect(result.baseCap).toBe(23.0);
    expect(result.scale).toBe(5.0);
    expect(result.effectiveCap).toBe(115.0);
  });

  it("returns baseCap × 1.0 × baseCap for Sonnet medium (no scaling)", () => {
    const result = getEffectiveStageCostCap("feature-dev", {
      model: "claude-sonnet-4-6",
      effort: "medium",
    });
    expect(result.baseCap).toBe(23.0);
    expect(result.scale).toBe(1.0);
    expect(result.effectiveCap).toBe(23.0);
  });

  it("returns the calibrated $3 base for pr-create (Issue #3208)", () => {
    // Pre-#3208 pr-create was uncapped (base 0). Now it ships a $3 default
    // (p95 $1.56 × 2). On Opus high (5.0×) the effective ceiling is $15.
    const result = getEffectiveStageCostCap("pr-create", {
      model: "claude-opus-4-7",
      effort: "high",
    });
    expect(result.baseCap).toBe(3.0);
    expect(result.scale).toBe(5.0);
    expect(result.effectiveCap).toBe(15.0);
  });

  it("returns 0/1.0/0 for stages with no default (no resurrection)", () => {
    // pipeline-start has no productive cost recorded — stays uncapped.
    const result = getEffectiveStageCostCap("pipeline-start", {
      model: "claude-opus-4-7",
      effort: "high",
    });
    expect(result.baseCap).toBe(0);
    expect(result.scale).toBe(1.0);
    expect(result.effectiveCap).toBe(0);
  });

  it("treats missing modelInfo as 1.0× (defensive default)", () => {
    const result = getEffectiveStageCostCap("feature-dev");
    expect(result.baseCap).toBe(23.0);
    expect(result.scale).toBe(1.0);
    expect(result.effectiveCap).toBe(23.0);
  });

  it("Sonnet medium effective cap stays at the calibrated base", () => {
    // Drift guard: a Sonnet medium feature-dev run cannot inadvertently get
    // scaled up — the multiplier is 1.0× for the calibration anchor.
    const sonnet = getEffectiveStageCostCap("feature-dev", {
      model: "sonnet",
      effort: "medium",
    });
    expect(sonnet.effectiveCap).toBe(sonnet.baseCap);
    expect(sonnet.effectiveCap).toBe(23.0);
  });

  it("Opus-high feature-dev effective cap clears the #871 actual cost ($25.31)", () => {
    // 2026-05-04 anchor: feature-dev terminated at $23.03 (cost-at-kill)
    // and slot reported $25.31 final on Opus high-effort. The new $115
    // effective cap leaves substantial headroom over that anchor; runaways
    // beyond $115 still kill.
    const opusHigh = getEffectiveStageCostCap("feature-dev", {
      model: "opus",
      effort: "high",
    });
    expect(opusHigh.effectiveCap).toBeGreaterThanOrEqual(25.31);
  });

  it("Opus-high feature-dev effective cap still terminates extreme runaways", () => {
    // Defensive ceiling: a $216 spend (worst observed in 90d window) must
    // still trigger the kill. The $115 ceiling guarantees this — anything
    // > 2× worst-case observed wouldn't.
    const opusHigh = getEffectiveStageCostCap("feature-dev", {
      model: "opus",
      effort: "high",
    });
    expect(opusHigh.effectiveCap).toBeLessThan(200.0);
  });
});
