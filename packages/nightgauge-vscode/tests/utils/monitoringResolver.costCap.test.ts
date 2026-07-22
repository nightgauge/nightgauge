/**
 * monitoringResolver.costCap.test.ts
 *
 * Pinning tests for the Issue #3208 cost-cap calibration.
 *
 * The base values in `DEFAULT_STAGE_COST_CAPS` were tuned to p95 × 2 over the
 * last 90 days of `complete | cancelled` runs. These tests pin the resulting
 * effective caps for the (stage, model, effort) combinations that drive
 * production decisions — Sonnet medium (the calibration anchor) and Opus high
 * (the heaviest scaling path). If the multiplier table or base values move,
 * these tests surface the drift so reviewers can re-justify the change.
 *
 * @see Issue #3208 — Tune per-stage cost cap defaults
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: undefined,
  },
}));

import {
  DEFAULT_STAGE_COST_CAPS,
  getEffectiveStageCostCap,
} from "../../src/utils/resolvers/monitoringResolver";

// Reset every COST_CAP env override so a developer's shell doesn't poison
// these assertions.
const COST_CAP_ENV_KEYS = [
  "NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_ISSUE_PICKUP",
  "NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_PLANNING",
  "NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_DEV",
  "NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_VALIDATE",
  "NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PR_CREATE",
  "NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PR_MERGE",
  "NIGHTGAUGE_BUDGET_MODEL_SCALE_OPUS",
  "NIGHTGAUGE_BUDGET_MODEL_SCALE_OPUS_HIGH",
  "NIGHTGAUGE_BUDGET_MODEL_SCALE_SONNET",
  "NIGHTGAUGE_BUDGET_MODEL_SCALE_SONNET_HIGH",
];

describe("getEffectiveStageCostCap — Issue #3208 calibrated defaults", () => {
  const saved: Record<string, string | undefined> = {};

  beforeEach(() => {
    for (const k of COST_CAP_ENV_KEYS) {
      saved[k] = process.env[k];
      delete process.env[k];
    }
  });

  afterEach(() => {
    for (const k of COST_CAP_ENV_KEYS) {
      if (saved[k] === undefined) {
        delete process.env[k];
      } else {
        process.env[k] = saved[k];
      }
    }
  });

  it("feature-dev on Sonnet medium uses the p95 × 2 base ($23) directly (1.0×)", () => {
    // Calibration anchor: Sonnet medium runs are the dominant case in the
    // history JSONL, so the base cap IS the effective cap for them.
    const result = getEffectiveStageCostCap("feature-dev", {
      model: "claude-sonnet-4-6",
      effort: "medium",
    });
    expect(result.baseCap).toBe(23.0);
    expect(result.scale).toBe(1.0);
    expect(result.effectiveCap).toBe(23.0);
  });

  it("feature-dev on Opus high scales to $115 (5.0× heavy-mode multiplier)", () => {
    // Heavy-mode runs legitimately run hotter; the multiplier widens the
    // cap proportionally so MAXIMUM mode isn't terminated mid-run on
    // realistic spends. Operators can override via env if they want a
    // tighter ceiling.
    const result = getEffectiveStageCostCap("feature-dev", {
      model: "claude-opus-4-7",
      effort: "high",
    });
    expect(result.baseCap).toBe(23.0);
    expect(result.scale).toBe(5.0);
    expect(result.effectiveCap).toBe(115.0);
  });

  it("pr-create on Sonnet medium pins at the calibrated $3 base", () => {
    // Second representative anchor (smaller stage). p95 over 90d was $1.56,
    // p95 × 2 = $3 (rounded). Sonnet medium scale is 1.0 so effective = $3.
    const result = getEffectiveStageCostCap("pr-create", {
      model: "claude-sonnet-4-6",
      effort: "medium",
    });
    expect(result.baseCap).toBe(3.0);
    expect(result.scale).toBe(1.0);
    expect(result.effectiveCap).toBe(3.0);
  });

  it("pr-create on Opus high scales to $15 (3 × 5.0)", () => {
    const result = getEffectiveStageCostCap("pr-create", {
      model: "claude-opus-4-7",
      effort: "high",
    });
    expect(result.baseCap).toBe(3.0);
    expect(result.scale).toBe(5.0);
    expect(result.effectiveCap).toBe(15.0);
  });

  it("DEFAULT_STAGE_COST_CAPS covers every productive pipeline stage", () => {
    // Drift guard: if a stage is added or removed from the pipeline the
    // calibration block must be revisited. We intentionally pin the exact
    // set of keys so a silent omission can't slip in unnoticed.
    expect(Object.keys(DEFAULT_STAGE_COST_CAPS).sort()).toEqual(
      [
        "feature-dev",
        "feature-planning",
        "feature-validate",
        "issue-pickup",
        "pr-create",
        "pr-merge",
      ].sort()
    );
  });

  it("returns uncapped (0) for stages with no default — pipeline-start has no productive cost", () => {
    // Pinning the n<20-equivalent branch: stages with no historical data
    // (here, none recorded for pipeline-start) keep their previous behavior
    // of being uncapped rather than being assigned a guessed value. This
    // mirrors the audit script's "n<20 — keep current" rule.
    const result = getEffectiveStageCostCap("pipeline-start", {
      model: "claude-opus-4-7",
      effort: "high",
    });
    expect(result.baseCap).toBe(0);
    expect(result.scale).toBe(1.0);
    expect(result.effectiveCap).toBe(0);
  });

  it("env override on the base value flows through the scale multiplier", () => {
    // Operators with deep-pocket workflows can raise the base via env; the
    // multiplier still applies on top. Pinning this contract so env-based
    // tuning stays a one-knob operation.
    process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_DEV = "10";
    const result = getEffectiveStageCostCap("feature-dev", {
      model: "claude-opus-4-7",
      effort: "high",
    });
    expect(result.baseCap).toBe(10.0);
    expect(result.scale).toBe(5.0);
    expect(result.effectiveCap).toBe(50.0);
  });
});
