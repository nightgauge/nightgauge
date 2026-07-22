/**
 * monitoringResolver.costCapWarning.test.ts
 *
 * Tests for `checkCostCapTightness()` (Issue #3276).
 *
 * Covers: shouldWarn logic, guard conditions, message content, decision fields.
 */

import { describe, it, expect } from "vitest";

vi.mock("vscode", () => ({
  workspace: { workspaceFolders: undefined },
}));

import { checkCostCapTightness } from "../../../src/utils/resolvers/monitoringResolver";

describe("checkCostCapTightness — shouldWarn logic", () => {
  it("warns when cap is below median × multiplier with sufficient samples", () => {
    const d = checkCostCapTightness("feature-dev", 5, 20, 1.2, 5);
    expect(d.shouldWarn).toBe(true);
  });

  it("does not warn when cap is above threshold", () => {
    const d = checkCostCapTightness("feature-dev", 30, 20, 1.2, 5);
    expect(d.shouldWarn).toBe(false);
  });

  it("does not warn when cap equals threshold exactly", () => {
    // effectiveCap = 24 = 20 × 1.2 — not strictly less than
    const d = checkCostCapTightness("feature-dev", 24, 20, 1.2, 5);
    expect(d.shouldWarn).toBe(false);
  });

  it("warns when cap is just below threshold", () => {
    const d = checkCostCapTightness("feature-dev", 23.99, 20, 1.2, 5);
    expect(d.shouldWarn).toBe(true);
  });

  it("does not warn when effectiveCap is 0 (uncapped)", () => {
    const d = checkCostCapTightness("feature-dev", 0, 20, 1.2, 5);
    expect(d.shouldWarn).toBe(false);
  });

  it("does not warn when historicalMedian is 0 (no history)", () => {
    const d = checkCostCapTightness("feature-dev", 5, 0, 1.2, 5);
    expect(d.shouldWarn).toBe(false);
  });

  it("does not warn when sampleCount < MIN_SAMPLES (3)", () => {
    const d = checkCostCapTightness("feature-dev", 5, 20, 1.2, 2);
    expect(d.shouldWarn).toBe(false);
  });

  it("warns when sampleCount equals MIN_SAMPLES (3)", () => {
    const d = checkCostCapTightness("feature-dev", 5, 20, 1.2, 3);
    expect(d.shouldWarn).toBe(true);
  });

  it("does not warn when multiplier is 0 (warning disabled)", () => {
    const d = checkCostCapTightness("feature-dev", 5, 20, 0, 5);
    expect(d.shouldWarn).toBe(false);
  });

  it("uses default multiplier of 1.2 when omitted", () => {
    // 5 < 20 × 1.2 = 24 → should warn
    const d = checkCostCapTightness("feature-dev", 5, 20, undefined, 5);
    expect(d.shouldWarn).toBe(true);
    expect(d.multiplier).toBe(1.2);
  });
});

describe("checkCostCapTightness — acceptance criteria (Issue #3276)", () => {
  it("synthetic: $5 cap, $20 median → warning with correct env key and config path", () => {
    const d = checkCostCapTightness("feature-dev", 5, 20, 1.2, 5);
    expect(d.shouldWarn).toBe(true);
    expect(d.message).toContain("NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_DEV");
    expect(d.message).toContain("pipeline.stage_cost_caps.feature-dev");
  });
});

describe("checkCostCapTightness — decision fields", () => {
  it("returns correct threshold = median × multiplier", () => {
    const d = checkCostCapTightness("feature-dev", 5, 20, 1.2, 5);
    expect(d.threshold).toBeCloseTo(24);
  });

  it("returns correct capEnvKey for hyphenated stage name", () => {
    const d = checkCostCapTightness("feature-dev", 5, 20, 1.2, 5);
    expect(d.capEnvKey).toBe("NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_DEV");
  });

  it("returns correct capConfigPath", () => {
    const d = checkCostCapTightness("pr-create", 1, 10, 1.2, 5);
    expect(d.capConfigPath).toBe("pipeline.stage_cost_caps.pr-create");
  });

  it("message is empty string when shouldWarn is false", () => {
    const d = checkCostCapTightness("feature-dev", 30, 20, 1.2, 5);
    expect(d.shouldWarn).toBe(false);
    expect(d.message).toBe("");
  });

  it("message contains cap, threshold, and median values when warning", () => {
    const d = checkCostCapTightness("feature-dev", 5, 20, 1.2, 5);
    expect(d.message).toContain("$5.00");
    expect(d.message).toContain("$24.00");
    expect(d.message).toContain("$20.00");
  });

  it("returns all expected fields on the decision object", () => {
    const d = checkCostCapTightness("pr-merge", 2, 8, 1.5, 10);
    expect(d).toMatchObject({
      stage: "pr-merge",
      effectiveCap: 2,
      historicalMedian: 8,
      threshold: 12,
      multiplier: 1.5,
      capEnvKey: "NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PR_MERGE",
      capConfigPath: "pipeline.stage_cost_caps.pr-merge",
      shouldWarn: true,
    });
  });
});
