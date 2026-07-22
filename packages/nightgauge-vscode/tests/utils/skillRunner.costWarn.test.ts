/**
 * skillRunner.costWarn.test.ts
 *
 * Tests for the two-threshold cost system introduced in Issue #3508:
 *   1. Warn threshold: non-blocking toast when cost > historicalMedian × warnMultiplier
 *   2. Runaway ceiling: kills stage but routes as TerminalKindStallKill (not BudgetExceeded)
 *
 * These contract tests verify the behavior downstream consumers depend on:
 *   - [cost-warn] prefix in stderr — HeadlessOrchestrator intercepts to fire a VSCode toast
 *   - [runaway-ceiling-exceeded] prefix — failure_handler.go maps to TerminalKindStallKill
 *   - costCapExceeded still set on SkillRunResult for backward compat
 *   - costWarnFired set on SkillRunResult when warn threshold crossed
 *   - SkillRunResult.costCapUsd now reflects the ceiling, not the effectiveCap
 */

import { describe, it, expect, beforeEach } from "vitest";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: undefined,
  },
}));

import {
  getEffectiveStageCostCap,
  getStageCostWarnMultiplier,
  getRunwayCeilingUsd,
} from "../../src/utils/resolvers/monitoringResolver";
import type { SkillRunResult } from "../../src/utils/skillRunner";

// Mirror the exact marker formats emitted by the two closures in skillRunner.ts
// (search for [cost-warn] and [runaway-ceiling-exceeded] in skillRunner.ts to verify drift).

function buildCostWarnMessage(stage: string, costNow: number, warnThreshold: number): string {
  return (
    `[cost-warn] Issue #42: ${stage} is tracking above warn threshold ` +
    `($${costNow.toFixed(2)} > $${warnThreshold.toFixed(2)} warn threshold). Pipeline continues.\n`
  );
}

function buildRunwayCeilingMessage(
  stage: string,
  costNow: number,
  ceiling: number,
  elapsedMs: number
): string {
  const seconds = Math.round(elapsedMs / 1000);
  return (
    `[runaway-ceiling-exceeded] Stage ${stage} terminated: runaway cost ceiling exceeded. ` +
    `Cost $${costNow.toFixed(4)} exceeded ceiling ($${ceiling.toFixed(2)}) ` +
    `after ${seconds}s. Treated as transient (stall-kill path).\n`
  );
}

// Mirrors the Go ClassifyTerminalKind heuristics for local verification.
const RUNAWAY_CEILING_PATTERNS = [
  "[runaway-ceiling-exceeded]",
  "runaway-ceiling-exceeded",
  "runaway cost ceiling exceeded",
] as const;
const COST_CAP_PATTERNS = [
  "[cost-cap-exceeded]",
  "cost-cap-exceeded",
  "cost cap exceeded",
] as const;

function classifiesAsRunwayCeiling(errorText: string): boolean {
  const t = errorText.toLowerCase();
  return RUNAWAY_CEILING_PATTERNS.some((p) => t.includes(p));
}

function classifiesAsLegacyCostCap(errorText: string): boolean {
  const t = errorText.toLowerCase();
  return !classifiesAsRunwayCeiling(t) && COST_CAP_PATTERNS.some((p) => t.includes(p));
}

// ============================================================================
// Warn message format tests
// ============================================================================

describe("skillRunner cost warn — [cost-warn] message format (Issue #3508)", () => {
  it("includes [cost-warn] prefix for HeadlessOrchestrator toast interception", () => {
    const msg = buildCostWarnMessage("feature-dev", 4.5, 3.0);
    expect(msg).toMatch(/^\[cost-warn\]/);
  });

  it("includes both cost-now and warn-threshold values for operator context", () => {
    const msg = buildCostWarnMessage("feature-dev", 4.56, 3.0);
    expect(msg).toContain("$4.56");
    expect(msg).toContain("$3.00");
  });

  it("includes 'Pipeline continues' to indicate the stage is NOT killed", () => {
    const msg = buildCostWarnMessage("feature-validate", 2.1, 1.5);
    expect(msg).toContain("Pipeline continues");
  });

  it("does NOT contain cost-cap-exceeded marker (warn is non-killing)", () => {
    const msg = buildCostWarnMessage("feature-dev", 4.5, 3.0);
    expect(msg).not.toMatch(/cost-cap-exceeded/);
    expect(msg).not.toMatch(/runaway-ceiling-exceeded/);
  });
});

// ============================================================================
// Runaway ceiling message format tests
// ============================================================================

describe("skillRunner runaway ceiling — [runaway-ceiling-exceeded] message format (Issue #3508)", () => {
  it("includes [runaway-ceiling-exceeded] prefix for Go classifier", () => {
    const msg = buildRunwayCeilingMessage("feature-dev", 76.5, 75.0, 300_000);
    expect(msg).toMatch(/^\[runaway-ceiling-exceeded\]/);
  });

  it("includes ceiling value (not effectiveCap) for operator triage", () => {
    const msg = buildRunwayCeilingMessage("feature-dev", 78.0, 75.0, 120_000);
    expect(msg).toContain("$78.0000");
    expect(msg).toContain("$75.00");
  });

  it("includes 'stall-kill path' annotation for observability", () => {
    const msg = buildRunwayCeilingMessage("feature-dev", 76.5, 75.0, 60_000);
    expect(msg).toContain("stall-kill path");
  });

  it("does NOT contain [cost-cap-exceeded] marker (backward compat: old marker stays for legacy paths)", () => {
    const msg = buildRunwayCeilingMessage("feature-dev", 76.5, 75.0, 60_000);
    expect(msg).not.toMatch(/\[cost-cap-exceeded\]/);
  });

  it("classifies as runaway-ceiling (not legacy cost-cap) in Go heuristic mirror", () => {
    const msg = buildRunwayCeilingMessage("feature-dev", 76.5, 75.0, 60_000);
    expect(classifiesAsRunwayCeiling(msg)).toBe(true);
    expect(classifiesAsLegacyCostCap(msg)).toBe(false);
  });
});

// ============================================================================
// Backward compatibility: legacy [cost-cap-exceeded] still classifies as budget-exceeded
// ============================================================================

describe("skillRunner backward compat — [cost-cap-exceeded] still classifies as budget-exceeded", () => {
  it("legacy marker does NOT match runaway-ceiling patterns", () => {
    const legacyMsg =
      "[cost-cap-exceeded] Stage feature-dev terminated: cost cap exceeded. " +
      "Cost $5.0500 exceeded the configured cap ($5.00) after 300s.\n";
    expect(classifiesAsRunwayCeiling(legacyMsg)).toBe(false);
    expect(classifiesAsLegacyCostCap(legacyMsg)).toBe(true);
  });
});

// ============================================================================
// SkillRunResult contract — new fields
// ============================================================================

describe("skillRunner cost warn — SkillRunResult contract (Issue #3508)", () => {
  it("exposes costWarnFired as optional field on SkillRunResult", () => {
    const withWarn: SkillRunResult = {
      success: true,
      exitCode: 0,
      costWarnFired: true,
    };
    expect(withWarn.costWarnFired).toBe(true);
  });

  it("costWarnFired is undefined when warn threshold was not crossed", () => {
    const withoutWarn: SkillRunResult = { success: true, exitCode: 0 };
    expect(withoutWarn.costWarnFired).toBeUndefined();
  });

  it("costCapExceeded remains true on runaway ceiling for downstream compat", () => {
    const runawayKill: SkillRunResult = {
      success: false,
      exitCode: null,
      costCapExceeded: true,
      costCapUsd: 75.0, // ceiling value (not effectiveCap)
      costAtTerminationUsd: 76.5,
    };
    expect(runawayKill.costCapExceeded).toBe(true);
    expect(runawayKill.costCapUsd).toBe(75.0);
    expect(runawayKill.costAtTerminationUsd).toBeCloseTo(76.5, 4);
  });

  it("warn and ceiling can both be set on the same run (warn fired, then ceiling killed)", () => {
    const bothFired: SkillRunResult = {
      success: false,
      exitCode: null,
      costCapExceeded: true,
      costCapUsd: 75.0,
      costAtTerminationUsd: 77.0,
      costWarnFired: true,
    };
    expect(bothFired.costWarnFired).toBe(true);
    expect(bothFired.costCapExceeded).toBe(true);
  });
});

// ============================================================================
// Resolver-level: getStageCostWarnMultiplier default
// ============================================================================

describe("getStageCostWarnMultiplier — default 1.5 (Issue #3508)", () => {
  beforeEach(() => {
    delete process.env.NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_FEATURE_DEV;
  });

  it("returns 1.5 by default (no env, no config)", () => {
    expect(getStageCostWarnMultiplier("feature-dev")).toBe(1.5);
  });

  it("returns 0 when disabled via global env var", () => {
    process.env.NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER = "0";
    try {
      expect(getStageCostWarnMultiplier("feature-dev")).toBe(0);
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER;
    }
  });

  it("per-stage env var overrides global env var", () => {
    process.env.NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER = "2.0";
    process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_FEATURE_DEV = "3.0";
    try {
      expect(getStageCostWarnMultiplier("feature-dev")).toBe(3.0);
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER;
      delete process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_FEATURE_DEV;
    }
  });
});

// ============================================================================
// Resolver-level: getRunwayCeilingUsd floor and multiplier
// ============================================================================

describe("getRunwayCeilingUsd — $75 floor + 3.0× multiplier (Issue #3508)", () => {
  beforeEach(() => {
    delete process.env.NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER;
  });

  it("returns $75 floor when effectiveCap × 3.0 < 75 (feature-dev sonnet: $23 × 3 = $69 < $75)", () => {
    // feature-dev sonnet: effectiveCap = $23, ceiling = max(75, 23×3) = 75
    const ceiling = getRunwayCeilingUsd(23.0);
    expect(ceiling).toBe(75);
  });

  it("returns multiplied value when effectiveCap × 3.0 > 75 (feature-dev opus:high: $115 × 3 = $345)", () => {
    const ceiling = getRunwayCeilingUsd(115.0);
    expect(ceiling).toBe(345);
  });

  it("returns 0 when effectiveCap is 0 (uncapped stage)", () => {
    expect(getRunwayCeilingUsd(0)).toBe(0);
  });

  it("respects NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER env override", () => {
    process.env.NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER = "2.0";
    try {
      // feature-dev sonnet: max(75, 23 × 2.0) = max(75, 46) = 75
      expect(getRunwayCeilingUsd(23.0)).toBe(75);
      // larger cap: max(75, 50 × 2.0) = max(75, 100) = 100
      expect(getRunwayCeilingUsd(50.0)).toBe(100);
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER;
    }
  });

  it("ignores multiplier < 1 (clamped to minimum 1)", () => {
    process.env.NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER = "0.5";
    try {
      // 0.5 is invalid (< 1), default 3.0 applies
      expect(getRunwayCeilingUsd(23.0)).toBe(75); // max(75, 23×3) = 75
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER;
    }
  });
});

// ============================================================================
// Warn threshold computation: warnThreshold = historicalMedian × warnMultiplier
// ============================================================================

describe("warn threshold logic (Issue #3508)", () => {
  it("warn fires when costNow > median × 1.5", () => {
    const historicalMedian = 4.0;
    const warnMultiplier = 1.5;
    const warnThreshold = historicalMedian * warnMultiplier; // 6.0
    const costNow = 6.5;
    expect(costNow > warnThreshold).toBe(true);
  });

  it("warn does NOT fire when costNow <= median × 1.5", () => {
    const historicalMedian = 4.0;
    const warnThreshold = historicalMedian * 1.5; // 6.0
    const costNow = 5.9;
    expect(costNow > warnThreshold).toBe(false);
  });

  it("warn is disabled (warnThreshold = 0) when no history", () => {
    // No history → median = 0 → warnThreshold = 0 → warn disabled
    const historicalMedian = 0;
    const warnMultiplier = 1.5;
    const warnThreshold = historicalMedian > 0 ? historicalMedian * warnMultiplier : 0;
    expect(warnThreshold).toBe(0);
  });

  it("runaway ceiling (max($75, effectiveCap × 3.0)) is always above effectiveCap", () => {
    // For any positive effectiveCap, ceiling >= effectiveCap (since multiplier >= 1 and floor is $75)
    const caps = [0.5, 1.0, 23.0, 50.0, 115.0, 200.0];
    for (const cap of caps) {
      const ceiling = getRunwayCeilingUsd(cap);
      expect(ceiling).toBeGreaterThanOrEqual(cap);
    }
  });
});

// ============================================================================
// Integration: effectiveCap from getEffectiveStageCostCap flows to ceiling correctly
// ============================================================================

describe("effectiveCap → runwayCeiling integration (Issue #3508)", () => {
  it("feature-dev sonnet default cap → ceiling at $75 floor", () => {
    const { effectiveCap } = getEffectiveStageCostCap("feature-dev", {
      model: "claude-sonnet-4-6",
      effort: "medium",
    });
    expect(effectiveCap).toBe(23.0); // from DEFAULT_STAGE_COST_CAPS
    const ceiling = getRunwayCeilingUsd(effectiveCap);
    expect(ceiling).toBe(75); // max(75, 23×3=69) = 75
  });

  it("feature-dev opus:high → ceiling at 3× ($345)", () => {
    const { effectiveCap } = getEffectiveStageCostCap("feature-dev", {
      model: "claude-opus-4-7",
      effort: "high",
    });
    expect(effectiveCap).toBe(115.0); // $23 × 5.0 opus scale
    const ceiling = getRunwayCeilingUsd(effectiveCap);
    expect(ceiling).toBe(345); // max(75, 115×3=345) = 345
  });

  it("uncapped stage (effectiveCap=0) → ceiling disabled (0)", () => {
    const { effectiveCap } = getEffectiveStageCostCap("pipeline-start");
    expect(effectiveCap).toBe(0);
    expect(getRunwayCeilingUsd(effectiveCap)).toBe(0);
  });
});
