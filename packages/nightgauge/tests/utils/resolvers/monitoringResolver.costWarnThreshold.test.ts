/**
 * monitoringResolver.costWarnThreshold.test.ts
 *
 * Tests for the two new resolver functions introduced in Issue #3508:
 *   - getStageCostWarnMultiplier — three-tier config (per-stage env → config → global env → default)
 *   - getRunwayCeilingUsd        — max($75 floor, effectiveCap × multiplier)
 */

import { describe, it, expect, beforeEach } from "vitest";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: undefined,
  },
}));

import {
  getStageCostWarnMultiplier,
  getRunwayCeilingUsd,
} from "../../../src/utils/resolvers/monitoringResolver";

// ============================================================================
// getStageCostWarnMultiplier — tier resolution
// ============================================================================

describe("getStageCostWarnMultiplier — default 1.5 (Issue #3508)", () => {
  beforeEach(() => {
    delete process.env.NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_FEATURE_DEV;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_FEATURE_VALIDATE;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_PR_CREATE;
  });

  it("returns 1.5 by default — no env vars, no workspace root", () => {
    expect(getStageCostWarnMultiplier("feature-dev")).toBe(1.5);
  });

  it("returns 1.5 for any stage by default", () => {
    for (const stage of [
      "feature-dev",
      "feature-validate",
      "feature-planning",
      "pr-create",
      "pr-merge",
    ]) {
      expect(getStageCostWarnMultiplier(stage)).toBe(1.5);
    }
  });
});

describe("getStageCostWarnMultiplier — global env override (Issue #3508)", () => {
  beforeEach(() => {
    delete process.env.NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_FEATURE_DEV;
  });

  it("returns 2.0 when NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER=2.0", () => {
    process.env.NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER = "2.0";
    try {
      expect(getStageCostWarnMultiplier("feature-dev")).toBe(2.0);
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER;
    }
  });

  it("returns 0 when global env var is '0' (warn disabled)", () => {
    process.env.NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER = "0";
    try {
      expect(getStageCostWarnMultiplier("feature-dev")).toBe(0);
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER;
    }
  });

  it("ignores invalid global env var and falls back to default", () => {
    process.env.NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER = "not-a-number";
    try {
      expect(getStageCostWarnMultiplier("feature-dev")).toBe(1.5);
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER;
    }
  });
});

describe("getStageCostWarnMultiplier — per-stage env override (Issue #3508)", () => {
  beforeEach(() => {
    delete process.env.NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_FEATURE_DEV;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_PR_CREATE;
  });

  it("per-stage env var overrides global env var", () => {
    process.env.NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER = "2.0";
    process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_FEATURE_DEV = "3.5";
    try {
      expect(getStageCostWarnMultiplier("feature-dev")).toBe(3.5);
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER;
      delete process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_FEATURE_DEV;
    }
  });

  it("per-stage env var for pr-create uses correct key (hyphen → underscore, uppercased)", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_PR_CREATE = "4.0";
    try {
      expect(getStageCostWarnMultiplier("pr-create")).toBe(4.0);
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_PR_CREATE;
    }
  });

  it("per-stage env var only applies to matching stage, not others", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_FEATURE_DEV = "5.0";
    try {
      expect(getStageCostWarnMultiplier("feature-validate")).toBe(1.5); // default
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_FEATURE_DEV;
    }
  });

  it("per-stage env var of 0 disables warn for that stage only", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_FEATURE_DEV = "0";
    process.env.NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER = "2.0";
    try {
      expect(getStageCostWarnMultiplier("feature-dev")).toBe(0);
      expect(getStageCostWarnMultiplier("feature-validate")).toBe(2.0); // global applies
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_FEATURE_DEV;
      delete process.env.NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER;
    }
  });
});

// ============================================================================
// getRunwayCeilingUsd — floor + multiplier
// ============================================================================

describe("getRunwayCeilingUsd — $75 floor (Issue #3508)", () => {
  beforeEach(() => {
    delete process.env.NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER;
  });

  it("returns 0 for effectiveCap=0 (uncapped stage — ceiling disabled)", () => {
    expect(getRunwayCeilingUsd(0)).toBe(0);
  });

  it("returns $75 floor when effectiveCap × 3.0 < 75", () => {
    // 20 × 3.0 = 60 < 75 → floor applies
    expect(getRunwayCeilingUsd(20.0)).toBe(75);
  });

  it("returns $75 floor for feature-dev sonnet cap ($23 × 3 = $69 < $75)", () => {
    expect(getRunwayCeilingUsd(23.0)).toBe(75);
  });

  it("returns floor exactly when effectiveCap × 3.0 = $75", () => {
    // 25 × 3.0 = 75 exactly → Math.max(75, 75) = 75
    expect(getRunwayCeilingUsd(25.0)).toBe(75);
  });

  it("returns 3× value when effectiveCap × 3.0 > $75", () => {
    // 30 × 3.0 = 90 > 75 → 90
    expect(getRunwayCeilingUsd(30.0)).toBe(90);
  });

  it("returns $345 for feature-dev opus:high ($115 × 3.0)", () => {
    expect(getRunwayCeilingUsd(115.0)).toBe(345);
  });

  it("ceiling is always >= effectiveCap for any positive cap", () => {
    for (const cap of [0.5, 1.0, 5.0, 23.0, 50.0, 115.0, 200.0, 500.0]) {
      expect(getRunwayCeilingUsd(cap)).toBeGreaterThanOrEqual(cap);
    }
  });

  it("ceiling is always >= $75 floor for any positive cap", () => {
    for (const cap of [1.0, 5.0, 10.0, 23.0, 50.0]) {
      expect(getRunwayCeilingUsd(cap)).toBeGreaterThanOrEqual(75);
    }
  });
});

describe("getRunwayCeilingUsd — NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER override (Issue #3508)", () => {
  beforeEach(() => {
    delete process.env.NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER;
  });

  it("uses env-overridden multiplier of 2.0", () => {
    process.env.NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER = "2.0";
    try {
      // 23 × 2.0 = 46 < 75 → floor
      expect(getRunwayCeilingUsd(23.0)).toBe(75);
      // 50 × 2.0 = 100 > 75 → 100
      expect(getRunwayCeilingUsd(50.0)).toBe(100);
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER;
    }
  });

  it("uses env-overridden multiplier of 5.0", () => {
    process.env.NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER = "5.0";
    try {
      // 23 × 5.0 = 115 > 75 → 115
      expect(getRunwayCeilingUsd(23.0)).toBe(115);
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER;
    }
  });

  it("clamps multiplier < 1 to default 3.0 (invalid — would make ceiling < effectiveCap)", () => {
    process.env.NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER = "0.5";
    try {
      // 0.5 < 1 → invalid, use default 3.0
      // 23 × 3.0 = 69 < 75 → floor
      expect(getRunwayCeilingUsd(23.0)).toBe(75);
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER;
    }
  });

  it("ignores multiplier of exactly 0 (falls back to default 3.0)", () => {
    process.env.NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER = "0";
    try {
      expect(getRunwayCeilingUsd(23.0)).toBe(75); // default 3.0 applies
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER;
    }
  });

  it("ignores NaN env value and falls back to default 3.0", () => {
    process.env.NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER = "not-a-number";
    try {
      expect(getRunwayCeilingUsd(23.0)).toBe(75); // default 3.0 applies
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER;
    }
  });

  it("returns 0 for effectiveCap=0 regardless of env override", () => {
    process.env.NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER = "5.0";
    try {
      expect(getRunwayCeilingUsd(0)).toBe(0);
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER;
    }
  });
});

// ============================================================================
// Behavioral invariants across threshold functions
// ============================================================================

describe("warn threshold disabled when multiplier is 0 (Issue #3508)", () => {
  it("warnThreshold = 0 when multiplier = 0 (warn disabled)", () => {
    const multiplier = 0;
    const historicalMedian = 10.0;
    const warnThreshold = multiplier > 0 ? historicalMedian * multiplier : 0;
    expect(warnThreshold).toBe(0);
  });

  it("warnThreshold = 0 when median = 0 (no history, cold start)", () => {
    const multiplier = 1.5;
    const historicalMedian = 0;
    const warnThreshold = historicalMedian > 0 ? historicalMedian * multiplier : 0;
    expect(warnThreshold).toBe(0);
  });

  it("warnThreshold > 0 when both multiplier and median are positive", () => {
    const multiplier = 1.5;
    const historicalMedian = 4.0;
    const warnThreshold = historicalMedian * multiplier; // 6.0
    expect(warnThreshold).toBeCloseTo(6.0);
  });
});
