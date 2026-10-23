/**
 * budgetStreamEnforcement.test.ts
 *
 * Tests for streaming (mid-stage) budget enforcement (Issue #254).
 *
 * Budget/ceiling thresholds used to evaluate in one burst at STAGE END because
 * cost only arrived on the terminal Claude CLI `result` envelope. Since #233 the
 * extension harvests per-turn `assistant` usage into a LiveStageEstimator, so
 * cost can be estimated DURING a stage. These tests verify the pure helpers the
 * orchestrator uses to drive the SAME BudgetEnforcer / PipelineBudgetCeiling
 * decision logic from that live stream — proving:
 *
 *   1. thresholds fire mid-stage in order (wind-down → warn → terminate);
 *   2. no double-fire when the authoritative terminal cost re-evaluates;
 *   3. the run ceiling never double-counts a stage's cost; and
 *   4. final accounting is unchanged — live estimates never touch the
 *      authoritative TokenAccumulator.
 *
 * The helpers are pure; the integration blocks exercise them against the REAL
 * BudgetEnforcer, PipelineBudgetCeiling, TokenAccumulator, and LiveStageEstimator
 * so the tests bind to production behavior rather than a re-implementation.
 */

import { describe, it, expect } from "vitest";

vi.mock("vscode", () => ({
  workspace: { workspaceFolders: undefined },
}));

import {
  nextBudgetActions,
  livePipelineCostUsd,
  shouldEmitSnapshot,
  type BudgetLatchState,
} from "../../src/utils/budgetStreamEnforcement";
import { BudgetEnforcer } from "../../src/utils/budgetEnforcer";
import { PipelineBudgetCeiling } from "../../src/utils/pipelineBudgetCeiling";
import { TokenAccumulator, LiveStageEstimator } from "../../src/utils/tokenParser";
import type { ParsedTokenUsage } from "../../src/utils/tokenParser";

// ============================================================================
// nextBudgetActions — once-only phase actions
// ============================================================================

describe("nextBudgetActions (#254)", () => {
  const decision = (shouldWindDown: boolean, shouldWarn: boolean, shouldTerminate: boolean) => ({
    shouldWindDown,
    shouldWarn,
    shouldTerminate,
  });

  const unlatched: BudgetLatchState = {
    windDownFired: false,
    warnFired: false,
    terminated: false,
  };

  it("fires each action when the decision is set and the latch is clear", () => {
    expect(nextBudgetActions(decision(true, false, false), unlatched).fireWindDown).toBe(true);
    expect(nextBudgetActions(decision(false, true, false), unlatched).fireWarn).toBe(true);
    expect(nextBudgetActions(decision(false, false, true), unlatched).fireTerminate).toBe(true);
  });

  it("suppresses an action once its latch is set (no double-fire)", () => {
    expect(
      nextBudgetActions(decision(true, false, false), {
        windDownFired: true,
        warnFired: false,
        terminated: false,
      }).fireWindDown
    ).toBe(false);

    expect(
      nextBudgetActions(decision(false, true, false), {
        windDownFired: false,
        warnFired: true,
        terminated: false,
      }).fireWarn
    ).toBe(false);

    expect(
      nextBudgetActions(decision(false, false, true), {
        windDownFired: false,
        warnFired: false,
        terminated: true,
      }).fireTerminate
    ).toBe(false);
  });

  it("returns no actions when the decision is empty", () => {
    const actions = nextBudgetActions(decision(false, false, false), unlatched);
    expect(actions).toEqual({ fireWindDown: false, fireWarn: false, fireTerminate: false });
  });
});

// ============================================================================
// livePipelineCostUsd — live ceiling cost without double-counting
// ============================================================================

describe("livePipelineCostUsd (#254)", () => {
  it("adds the stage's live estimate to prior booked cost mid-stage (nothing booked yet)", () => {
    // prior stages booked $60; this stage's live estimate $30; 0 booked so far
    expect(livePipelineCostUsd(60, 30, 0)).toBe(90);
  });

  it("collapses to the booked total at stage end (stage fully booked → no double-count)", () => {
    // At terminal the booked pipeline total already includes this $45 stage, so
    // the extra term must be 0 — not $105 + $45 = $150.
    expect(livePipelineCostUsd(105, 45, 45)).toBe(105);
  });

  it("never subtracts when the booked cost lands above the final live estimate", () => {
    // Authoritative booked stage cost ($50) slightly exceeds the last live
    // estimate ($45) — Math.max floor keeps the extra term at 0.
    expect(livePipelineCostUsd(110, 45, 50)).toBe(110);
  });

  it("treats non-finite inputs as 0", () => {
    expect(livePipelineCostUsd(Number.NaN, 30, 0)).toBe(30);
    expect(livePipelineCostUsd(60, Number.POSITIVE_INFINITY, 0)).toBe(60);
  });
});

// ============================================================================
// shouldEmitSnapshot — throttle gate
// ============================================================================

describe("shouldEmitSnapshot (#254)", () => {
  it("emits once at least cadence has elapsed", () => {
    expect(shouldEmitSnapshot(5000, 0, 5000)).toBe(true);
    expect(shouldEmitSnapshot(5001, 0, 5000)).toBe(true);
  });

  it("suppresses before cadence elapses", () => {
    expect(shouldEmitSnapshot(4999, 0, 5000)).toBe(false);
  });

  it("a seeded past emit time makes the first snapshot fire promptly at t=0", () => {
    // Orchestrator seeds lastEmitMs = -cadence so the first observation emits.
    expect(shouldEmitSnapshot(0, -5000, 5000)).toBe(true);
  });

  it("cadence <= 0 disables throttling (always emit)", () => {
    expect(shouldEmitSnapshot(0, 0, 0)).toBe(true);
    expect(shouldEmitSnapshot(1, 1, -1)).toBe(true);
  });
});

// ============================================================================
// Integration: real BudgetEnforcer + latch — ordered, once-only firing
// ============================================================================

describe("streaming budget evaluation with real BudgetEnforcer (#254)", () => {
  // stageOverride 10 → wind-down > $8 (80%), warn > $10, terminate > $15 (×1.5 grace)
  const makeEnforcer = () =>
    new BudgetEnforcer({ mode: "hard", stageOverrides: { "feature-dev": 10 } });

  /** Drive a rising live-cost stream through the enforcer + latch, mirroring
   *  the orchestrator's evaluateBudgetAndCeiling flow. */
  function drive(costs: number[]) {
    const enforcer = makeEnforcer();
    const latch: BudgetLatchState = {
      windDownFired: false,
      warnFired: false,
      terminated: false,
    };
    const fired: string[] = [];
    for (const cost of costs) {
      const decision = enforcer.checkBudget("feature-dev", cost);
      const actions = nextBudgetActions(decision, latch);
      if (actions.fireWindDown) {
        latch.windDownFired = true;
        fired.push(`windDown@${cost}`);
      }
      if (actions.fireWarn) {
        latch.warnFired = true;
        fired.push(`warn@${cost}`);
      }
      if (actions.fireTerminate) {
        latch.terminated = true;
        fired.push(`terminate@${cost}`);
      }
    }
    return fired;
  }

  it("fires wind-down → warn → terminate in order as live cost rises", () => {
    // $5 (idle) → $9 (>$8 wind-down) → $12 (>$10 warn) → $16 (>$15 terminate)
    const fired = drive([5, 9, 12, 16]);
    expect(fired).toEqual(["windDown@9", "warn@12", "terminate@16"]);
  });

  it("does NOT re-fire when the authoritative terminal cost re-evaluates the same thresholds", () => {
    // Live stream crosses all three, then the terminal envelope re-runs the
    // SAME evaluation at the authoritative (equal-or-higher) cost — no new
    // actions, because every phase is latched.
    const fired = drive([9, 12, 16, /* terminal re-feed */ 16, 20]);
    expect(fired).toEqual(["windDown@9", "warn@12", "terminate@16"]);
  });

  it("a direct jump straight to the terminate band fires terminate only (never a spurious wind-down)", () => {
    // Coarse cadence can jump $5 → $16 in one snapshot: only terminate fires.
    const fired = drive([5, 16]);
    expect(fired).toEqual(["terminate@16"]);
  });

  it("each phase fires at most once across many snapshots in the same band", () => {
    const fired = drive([9, 9.2, 9.5, 12, 12.5, 13, 16, 17, 18]);
    expect(fired).toEqual(["windDown@9", "warn@12", "terminate@16"]);
  });
});

// ============================================================================
// Integration: real PipelineBudgetCeiling fed live pipeline cost
// ============================================================================

describe("streaming run-ceiling evaluation with real PipelineBudgetCeiling (#254)", () => {
  it("fires warn → checkpoint → stop mid-stage as the live pipeline total rises", () => {
    // ceiling $100 → warn 70%, checkpoint 85%, stop 100%. Prior stages booked $60.
    const ceiling = new PipelineBudgetCeiling({ ceilingUsd: 100, warnThresholdUsd: 0 });
    const booked = 60;

    const at = (liveStage: number) => ceiling.check(livePipelineCostUsd(booked, liveStage, 0));

    expect(at(5).shouldWarn).toBe(false); // $65 — under 70%
    expect(at(15).shouldWarn).toBe(true); // $75 — over 70%
    expect(at(30).shouldCheckpoint).toBe(true); // $90 — over 85%
    expect(at(45).shouldStop).toBe(true); // $105 — over the ceiling
  });

  it("does not double-count the stage once it is booked at stage end", () => {
    const ceiling = new PipelineBudgetCeiling({ ceilingUsd: 100, warnThresholdUsd: 0 });
    // Mid-stage: $60 booked + $45 live = $105 → stop.
    expect(ceiling.check(livePipelineCostUsd(60, 45, 0)).shouldStop).toBe(true);
    // At terminal the $45 stage is booked into the $105 total; feeding the same
    // stage cost must NOT push it to $150 — it stays $105.
    const terminalInput = livePipelineCostUsd(105, 45, 45);
    expect(terminalInput).toBe(105);
    expect(ceiling.check(terminalInput).effectiveCeilingUsd).toBe(100);
  });
});

// ============================================================================
// Invariant: live estimates never book — final accounting unchanged
// ============================================================================

describe("no double-booking: LiveStageEstimator is independent of TokenAccumulator (#233/#254)", () => {
  const snap = (input: number, output: number): ParsedTokenUsage => ({
    inputTokens: input,
    outputTokens: output,
    cacheReadTokens: 0,
    cacheCreationTokens: 0,
    costUsd: 0,
  });

  it("live cost snapshots do not mutate the authoritative accumulator total", () => {
    const accumulator = new TokenAccumulator();
    const estimator = new LiveStageEstimator("claude", "sonnet");

    // Many live snapshots drive rising estimates used ONLY for enforcement.
    // Input is latest-wins (growing context), output is summed per turn (#233).
    estimator.observe(snap(1000, 100));
    estimator.observe(snap(1800, 250));
    estimator.observe(snap(2600, 400));
    expect(estimator.estimate().inputTokens).toBe(2600);
    expect(estimator.estimate().outputTokens).toBe(750);

    // The accumulator has seen nothing — the estimator never feeds it.
    expect(accumulator.getTotal().costUsd).toBe(0);
    expect(accumulator.hasTokens()).toBe(false);

    // Only the terminal `result` envelope books cost (session-cumulative, #256).
    accumulator.add({
      inputTokens: 2600,
      outputTokens: 400,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 3.5,
      costCumulative: true,
    });

    // Final booked total equals the terminal envelope's cost — the live
    // estimates (however many) contributed nothing.
    expect(accumulator.getTotal().costUsd).toBe(3.5);
  });

  it("re-running enforcement on the live stream does not change the booked terminal total", () => {
    const accumulator = new TokenAccumulator();
    const estimator = new LiveStageEstimator("claude", "sonnet");

    // Simulate a full stage: N live observations (enforcement only) + one
    // terminal booking. Booked total must equal the terminal cost regardless of
    // how many live evaluations ran.
    for (let i = 1; i <= 20; i++) {
      estimator.observe(snap(1000 + i * 50, i * 20));
    }
    accumulator.add({
      inputTokens: 2000,
      outputTokens: 400,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 4.2,
      costCumulative: true,
    });

    expect(accumulator.getTotal().costUsd).toBe(4.2);
  });
});
