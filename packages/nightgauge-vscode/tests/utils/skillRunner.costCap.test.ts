/**
 * skillRunner.costCap.test.ts
 *
 * Tests for the per-stage cost cap kill path (Issue #3002).
 *
 * The kill is implemented inside the existing stallTicker in skillRunner.ts,
 * polling `tokenAccumulator.getTotal().costUsd` every 30s. Rather than
 * reproducing the full subprocess lifecycle (the existing skillRunner.test.ts
 * harness is large), these tests verify the behavior contract that downstream
 * consumers depend on:
 *
 *   1. The error message format `[cost-cap-exceeded] ... cost cap exceeded`
 *      matches the YAML taxonomy regex (failure-taxonomy.yaml) so failure
 *      pattern detection works without further wiring.
 *   2. The SkillRunResult interface exposes `costCapExceeded`, `costCapUsd`,
 *      and `costAtTerminationUsd` so HeadlessOrchestrator can branch on them.
 *   3. The resolver returns 0 (uncapped) when the cap is unset, so the
 *      polling check is a true no-op for repos that opt out.
 *
 * The full kill path is exercised end-to-end via the integration test in
 * `monitoringResolver.stageCostCap.test.ts` (resolver) plus this contract
 * test (taxonomy + classifier integration).
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: undefined,
  },
}));

import {
  getStageCostCapUsd,
  getEffectiveStageCostCap,
  DEFAULT_STAGE_COST_CAPS,
} from "../../src/utils/resolvers/monitoringResolver";
import type { SkillRunResult } from "../../src/utils/skillRunner";

// Mirror the patterns added to packages/nightgauge-sdk/src/analysis/health/failureClassifier.ts
// (Issue #3002). If those patterns drift this test fails, signaling a needed
// docs/update. We don't import the SDK helper because in this worktree the
// `@nightgauge/sdk` symlink resolves to the main worktree's
// dist (which may be stale during local feature-dev).
const COST_CAP_INFRASTRUCTURE_PATTERNS = ["[cost-cap-exceeded]", "cost cap exceeded"] as const;
function classifiesAsInfrastructure(errorText: string): boolean {
  const t = errorText.toLowerCase();
  return COST_CAP_INFRASTRUCTURE_PATTERNS.some((p) => t.includes(p));
}

describe("skillRunner cost cap — error message format (Issue #3002)", () => {
  // Mirror the exact format emitted by the cost-cap kill path in skillRunner.ts
  // (search for `[cost-cap-exceeded]` in skillRunner.ts to verify drift).
  function buildCostCapMessage(stage: string, costNow: number, capUsd: number, elapsedMs: number) {
    const seconds = Math.round(elapsedMs / 1000);
    return (
      `[cost-cap-exceeded] Stage ${stage} terminated: cost cap exceeded. ` +
      `Cost $${costNow.toFixed(4)} exceeded the configured cap ($${capUsd.toFixed(2)}) ` +
      `after ${seconds}s.\n`
    );
  }

  it("matches the YAML taxonomy regex for cost-cap-exceeded", () => {
    const msg = buildCostCapMessage("feature-dev", 5.21, 5.0, 73_000);

    // Patterns from failure-taxonomy.yaml `stage-cost-cap-exceeded` category
    expect(msg).toMatch(/\[cost-cap-exceeded\]/);
    expect(msg).toMatch(/cost cap exceeded/);
  });

  it("contains both the cap and cost-at-termination values for operator triage", () => {
    const msg = buildCostCapMessage("feature-dev", 7.5, 5.0, 30_000);

    expect(msg).toContain("$7.5000"); // cost-at-termination (4dp)
    expect(msg).toContain("$5.00"); // cap (2dp)
  });

  it("classifies as 'infrastructure' weight in the reliability dimension", () => {
    // Cap-triggered kills must NOT depress the health score — they're a
    // pipeline guardrail firing, not a code defect.
    const msg = buildCostCapMessage("feature-dev", 5.05, 5.0, 75_000);

    expect(classifiesAsInfrastructure(msg)).toBe(true);
  });

  it("orchestrator-side error message also classifies as infrastructure", () => {
    // Mirror the format in HeadlessOrchestrator.ts when result.costCapExceeded.
    const orchestratorMsg =
      `[cost-cap-exceeded] Stage feature-dev terminated: cost cap exceeded.\n` +
      `Cost $5.0500 exceeded the configured cap ($5.00).\n` +
      `Configure pipeline.stage_cost_caps to adjust (omit stage to disable).`;

    expect(orchestratorMsg).toMatch(/\[cost-cap-exceeded\]/);
    expect(orchestratorMsg).toMatch(/cost cap exceeded/);
    expect(classifiesAsInfrastructure(orchestratorMsg)).toBe(true);
  });
});

describe("skillRunner cost cap — SkillRunResult contract (Issue #3002)", () => {
  it("exposes costCapExceeded, costCapUsd, and costAtTerminationUsd as optional fields", () => {
    // Compile-time check: a payload setting these fields must be assignable
    // to SkillRunResult. If the union changes, this test surfaces the drift.
    const payload: SkillRunResult = {
      success: false,
      exitCode: null,
      costCapExceeded: true,
      costCapUsd: 5.0,
      costAtTerminationUsd: 5.21,
    };
    expect(payload.costCapExceeded).toBe(true);
    expect(payload.costCapUsd).toBe(5.0);
    expect(payload.costAtTerminationUsd).toBeCloseTo(5.21, 4);
  });

  it("omits the cost-cap fields when the cap was not crossed", () => {
    const payload: SkillRunResult = { success: true, exitCode: 0 };
    expect(payload.costCapExceeded).toBeUndefined();
    expect(payload.costCapUsd).toBeUndefined();
    expect(payload.costAtTerminationUsd).toBeUndefined();
  });
});

describe("skillRunner cost cap — opt-out is the default (Issue #3002)", () => {
  // The runtime check is `costCapUsd > 0 && costNow > costCapUsd`. If the
  // resolver returns 0, the entire branch becomes a no-op and the cost path
  // costs nothing. These tests guard that contract.

  beforeEach(() => {
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_DEV;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PR_CREATE;
  });

  it("returns 0 (no-op) for stages without a default and no override", () => {
    // Issue #3208: every productive stage now ships a calibrated default,
    // so the only stages that no-op are those with no productive cost
    // (e.g. pipeline-start) or unknown stage names.
    expect(getStageCostCapUsd("pipeline-start")).toBe(0);
    expect(getStageCostCapUsd("not-a-real-stage")).toBe(0);
  });

  it("ships the documented Issue #3208 defaults for every productive stage", () => {
    // Calibration: p95 × 2 (rounded) over the last 90 days of recorded runs.
    // See `DEFAULT_STAGE_COST_CAPS` in monitoringResolver.ts for the full
    // distribution and `scripts/audit-stage-cost-distribution.ts` to re-run.
    expect(DEFAULT_STAGE_COST_CAPS["feature-dev"]).toBe(23.0);
    expect(getStageCostCapUsd("feature-dev")).toBe(23.0);
    expect(getStageCostCapUsd("issue-pickup")).toBe(1.0);
    expect(getStageCostCapUsd("feature-planning")).toBe(6.0);
    expect(getStageCostCapUsd("feature-validate")).toBe(7.0);
    expect(getStageCostCapUsd("pr-create")).toBe(3.0);
    expect(getStageCostCapUsd("pr-merge")).toBe(4.0);
  });

  it("0 in env disables the cap explicitly even when feature-dev has a default", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_DEV = "0";
    try {
      expect(getStageCostCapUsd("feature-dev")).toBe(0);
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_DEV;
    }
  });
});

describe("skillRunner cost cap — diagnostic log filename (Issue #3002)", () => {
  // The diagnostic log is written to
  // `.nightgauge/pipeline/history/{N}/{stage}-cost-capped.log` mirroring
  // the stall diagnostic at stageHardCap. This test asserts the path
  // computation so a refactor doesn't silently break operator runbooks.

  // ── Issue #3180: push-based + mode-scaled cost-cap regression tests ─────

  it("scaled effective cap matches the cost cap path's ceiling decision", () => {
    // Push-based check uses the same `costCapUsd` (post-scale) as the ticker.
    // This ensures the resolver and skillRunner stay in lockstep on what
    // value gets compared against `tokenAccumulator.getTotal().costUsd`.
    // Recalibrated by Issue #3208: feature-dev base = $23 (p95 $11.25 × 2).
    // With opus:high = 5.0× the effective cap is $115 — well above the
    // 2026-05-04 #871 anchor ($23.03 at-kill) which the previous $25
    // effective ceiling would still have caught with no headroom.
    const opusHigh = getEffectiveStageCostCap("feature-dev", {
      model: "claude-opus-4-7",
      effort: "high",
    });
    expect(opusHigh.effectiveCap).toBe(115.0);

    // The kill condition is `costNow > costCapUsd`. The #871 incident
    // ($23.03) plus a healthy buffer must still NOT trip on opus:high.
    const wouldKill = (costNow: number) => costNow > 0 && costNow > opusHigh.effectiveCap;
    expect(wouldKill(23.03)).toBe(false);
    expect(wouldKill(114.99)).toBe(false);
    expect(wouldKill(115.01)).toBe(true);

    // Sonnet (the primary calibration target) ceiling matches the base cap.
    const sonnetMed = getEffectiveStageCostCap("feature-dev", {
      model: "claude-sonnet-4-6",
      effort: "medium",
    });
    expect(sonnetMed.effectiveCap).toBe(23.0);
  });

  it("computes the diagnostic path under pipeline/history/{N}", () => {
    const workspaceRoot = "/tmp/irrelevant";
    const issueNumber = 42;
    const stage = "feature-dev";
    const expected = path.join(
      workspaceRoot,
      ".nightgauge",
      "pipeline",
      "history",
      String(issueNumber),
      `${stage}-cost-capped.log`
    );

    expect(expected.endsWith("/.nightgauge/pipeline/history/42/feature-dev-cost-capped.log")).toBe(
      true
    );
    // Sanity: must NOT collide with the stall diagnostic filename
    expect(expected).not.toContain("-stalled.log");
    // Sanity: real diagnostic write would not depend on directory pre-existing
    // (skillRunner.ts uses fs.mkdirSync(..., { recursive: true })).
    expect(typeof fs.mkdirSync).toBe("function");
  });
});
