/**
 * DashboardState.costTrend.test.ts
 *
 * Comprehensive cost trend tests covering:
 * - Median-based outlier resistance (replaces mean)
 * - Supercharge-mode exclusion (Issue #2433)
 * - computeCostTrendScore scoring function
 * - Edge cases (insufficient data, zero baselines)
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";

vi.mock("vscode", () => ({
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn().mockReturnValue(undefined),
    })),
  },
  EventEmitter: class EventEmitter {
    event = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
  },
}));

import { DashboardState } from "../../../src/views/dashboard/DashboardState";
import { computeCostTrendScore } from "../../../src/views/dashboard/HealthWidget";

/** Build a serialized PipelineRunSummary for storage */
function makeRun(overrides: {
  issueNumber: number;
  costUsd: number;
  is_recovery?: boolean;
  is_supercharge?: boolean;
}) {
  return {
    issueNumber: overrides.issueNumber,
    title: `Issue #${overrides.issueNumber}`,
    branch: `feat/${overrides.issueNumber}`,
    startedAt: "2026-03-01T00:00:00.000Z",
    completedAt: "2026-03-01T01:00:00.000Z",
    status: "complete" as const,
    stages: [],
    usage: {
      inputTokens: 10000,
      outputTokens: 5000,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: overrides.costUsd,
      durationMs: 3600000,
      stageCount: 6,
    },
    toolCalls: [],
    timeSavedMs: 7200000,
    is_recovery: overrides.is_recovery,
    is_supercharge: overrides.is_supercharge,
  };
}

function createState(runs: ReturnType<typeof makeRun>[]): DashboardState {
  const workspaceState = createMockMemento(new Map([["nightgauge.dashboard.history", runs]]));
  return new DashboardState(workspaceState);
}

// ============================================================================
// Median-based outlier resistance
// ============================================================================

describe("getCostTrend — median-based outlier resistance", () => {
  it("should use median, not mean — a single outlier must not tank the score", () => {
    // Recent 5: 4 normal runs at $5 + 1 outlier at $15
    // Mean = $7, Median = $5
    // Older 5: all $5
    // With mean: +40% → significant degradation
    // With median: 0% → stable (correct — the outlier is noise)
    const runs = [
      // Recent 5 (most recent first)
      makeRun({ issueNumber: 10, costUsd: 15.0 }), // outlier
      makeRun({ issueNumber: 9, costUsd: 5.0 }),
      makeRun({ issueNumber: 8, costUsd: 5.0 }),
      makeRun({ issueNumber: 7, costUsd: 5.0 }),
      makeRun({ issueNumber: 6, costUsd: 5.0 }),
      // Older 5
      makeRun({ issueNumber: 5, costUsd: 5.0 }),
      makeRun({ issueNumber: 4, costUsd: 5.0 }),
      makeRun({ issueNumber: 3, costUsd: 5.0 }),
      makeRun({ issueNumber: 2, costUsd: 5.0 }),
      makeRun({ issueNumber: 1, costUsd: 5.0 }),
    ];

    const state = createState(runs);
    const trend = state.getCostTrend();

    expect(trend.hasEnoughData).toBe(true);
    // Median of recent = $5, median of older = $5 → 0% change
    expect(trend.percentChange).toBe(0);
    expect(trend.improving).toBe(false);
  });

  it("should detect real cost increases even with median", () => {
    // Recent 5: all $8
    // Older 5: all $4
    // Median-based: +100%
    const runs = [
      makeRun({ issueNumber: 10, costUsd: 8.0 }),
      makeRun({ issueNumber: 9, costUsd: 8.0 }),
      makeRun({ issueNumber: 8, costUsd: 8.0 }),
      makeRun({ issueNumber: 7, costUsd: 8.0 }),
      makeRun({ issueNumber: 6, costUsd: 8.0 }),
      makeRun({ issueNumber: 5, costUsd: 4.0 }),
      makeRun({ issueNumber: 4, costUsd: 4.0 }),
      makeRun({ issueNumber: 3, costUsd: 4.0 }),
      makeRun({ issueNumber: 2, costUsd: 4.0 }),
      makeRun({ issueNumber: 1, costUsd: 4.0 }),
    ];

    const state = createState(runs);
    const trend = state.getCostTrend();

    expect(trend.hasEnoughData).toBe(true);
    expect(trend.percentChange).toBe(100);
    expect(trend.improving).toBe(false);
  });

  it("should detect cost improvements", () => {
    // Recent 5: all $3 (costs went down)
    // Older 5: all $6
    const runs = [
      makeRun({ issueNumber: 10, costUsd: 3.0 }),
      makeRun({ issueNumber: 9, costUsd: 3.0 }),
      makeRun({ issueNumber: 8, costUsd: 3.0 }),
      makeRun({ issueNumber: 7, costUsd: 3.0 }),
      makeRun({ issueNumber: 6, costUsd: 3.0 }),
      makeRun({ issueNumber: 5, costUsd: 6.0 }),
      makeRun({ issueNumber: 4, costUsd: 6.0 }),
      makeRun({ issueNumber: 3, costUsd: 6.0 }),
      makeRun({ issueNumber: 2, costUsd: 6.0 }),
      makeRun({ issueNumber: 1, costUsd: 6.0 }),
    ];

    const state = createState(runs);
    const trend = state.getCostTrend();

    expect(trend.hasEnoughData).toBe(true);
    expect(trend.percentChange).toBe(-50);
    expect(trend.improving).toBe(true);
  });

  it("should handle even-count windows with median of two middle values", () => {
    // 4 recent runs (even count): $2, $4, $6, $8 → median = ($4+$6)/2 = $5
    // 4 older runs: $3, $3, $3, $3 → median = $3
    const runs = [
      makeRun({ issueNumber: 8, costUsd: 2.0 }),
      makeRun({ issueNumber: 7, costUsd: 4.0 }),
      makeRun({ issueNumber: 6, costUsd: 6.0 }),
      makeRun({ issueNumber: 5, costUsd: 8.0 }),
      makeRun({ issueNumber: 4, costUsd: 3.0 }),
      makeRun({ issueNumber: 3, costUsd: 3.0 }),
      makeRun({ issueNumber: 2, costUsd: 3.0 }),
      makeRun({ issueNumber: 1, costUsd: 3.0 }),
    ];

    const state = createState(runs);
    const trend = state.getCostTrend(4, 4);

    expect(trend.hasEnoughData).toBe(true);
    // Median of [2,4,6,8] sorted = [2,4,6,8] → (4+6)/2 = 5
    // Median of [3,3,3,3] = 3
    // Change = (5-3)/3 * 100 = 66.7%
    expect(trend.percentChange).toBeCloseTo(66.7, 0);
  });

  it("should survive the exact scenario that triggered the bug: $14.82 outlier", () => {
    // Reproduce the production data that caused cost trend = 0
    const runs = [
      // Recent 5 (production values)
      makeRun({ issueNumber: 2446, costUsd: 4.64 }),
      makeRun({ issueNumber: 2445, costUsd: 14.82 }), // 2.5× outlier
      makeRun({ issueNumber: 2444, costUsd: 7.15 }),
      makeRun({ issueNumber: 2434, costUsd: 8.89 }),
      makeRun({ issueNumber: 2437, costUsd: 5.83 }),
      // Older 5
      makeRun({ issueNumber: 2433, costUsd: 4.29 }),
      makeRun({ issueNumber: 2436, costUsd: 3.97 }),
      makeRun({ issueNumber: 2435, costUsd: 2.16 }),
      makeRun({ issueNumber: 2423, costUsd: 2.24 }),
      makeRun({ issueNumber: 2422, costUsd: 4.24 }),
    ];

    const state = createState(runs);
    const trend = state.getCostTrend();

    expect(trend.hasEnoughData).toBe(true);
    // With mean: recentAvg=$8.27, olderAvg=$3.38 → +144% → score 0 (BUG)
    // With median: recentMedian=$7.15, olderMedian=$3.97 → +80% → score ≈ 26
    // The outlier $14.82 no longer dominates the calculation
    const score = computeCostTrendScore(trend.percentChange);
    expect(score).toBeGreaterThan(0);
    // Verify the percent change is reasonable (not 144%)
    expect(trend.percentChange).toBeLessThan(100);
  });
});

// ============================================================================
// Supercharge-mode exclusion
// ============================================================================

describe("getCostTrend — supercharge-mode exclusion (Issue #2433)", () => {
  it("should exclude supercharge runs from cost trend calculation", () => {
    // Supercharge runs at $15 mixed in with normal runs at $5
    // Without filtering: recent avg inflated by supercharge
    // With filtering: supercharge runs removed, trend based on normal runs only
    const runs = [
      // Recent (2 supercharge + 3 normal)
      makeRun({ issueNumber: 10, costUsd: 15.0, is_supercharge: true }),
      makeRun({ issueNumber: 9, costUsd: 15.0, is_supercharge: true }),
      makeRun({ issueNumber: 8, costUsd: 5.0 }),
      makeRun({ issueNumber: 7, costUsd: 5.0 }),
      makeRun({ issueNumber: 6, costUsd: 5.0 }),
      makeRun({ issueNumber: 5, costUsd: 5.0 }),
      makeRun({ issueNumber: 4, costUsd: 5.0 }),
      // Older (after supercharge removal, these become the "older" window)
      makeRun({ issueNumber: 3, costUsd: 5.0 }),
      makeRun({ issueNumber: 2, costUsd: 5.0 }),
      makeRun({ issueNumber: 1, costUsd: 5.0 }),
    ];

    const state = createState(runs);
    const trend = state.getCostTrend();

    expect(trend.hasEnoughData).toBe(true);
    // After excluding supercharge, all remaining runs cost $5 → 0% change
    expect(trend.percentChange).toBe(0);
  });

  it("should report insufficient data when supercharge removal leaves too few runs", () => {
    // All recent runs are supercharge — after filtering, not enough data
    const runs = [
      makeRun({ issueNumber: 8, costUsd: 20.0, is_supercharge: true }),
      makeRun({ issueNumber: 7, costUsd: 20.0, is_supercharge: true }),
      makeRun({ issueNumber: 6, costUsd: 20.0, is_supercharge: true }),
      makeRun({ issueNumber: 5, costUsd: 5.0 }),
      makeRun({ issueNumber: 4, costUsd: 5.0 }),
      makeRun({ issueNumber: 3, costUsd: 5.0 }),
      makeRun({ issueNumber: 2, costUsd: 5.0 }),
      makeRun({ issueNumber: 1, costUsd: 5.0 }),
    ];

    const state = createState(runs);
    const trend = state.getCostTrend();

    // Only 5 non-supercharge runs → recent=5, older=0 → insufficient
    expect(trend.hasEnoughData).toBe(false);
  });

  it("should exclude both recovery AND supercharge runs simultaneously", () => {
    const runs = [
      makeRun({ issueNumber: 12, costUsd: 20.0, is_supercharge: true }),
      makeRun({ issueNumber: 11, costUsd: 15.0, is_recovery: true }),
      makeRun({ issueNumber: 10, costUsd: 5.0 }),
      makeRun({ issueNumber: 9, costUsd: 5.0 }),
      makeRun({ issueNumber: 8, costUsd: 5.0 }),
      makeRun({ issueNumber: 7, costUsd: 5.0 }),
      makeRun({ issueNumber: 6, costUsd: 5.0 }),
      makeRun({ issueNumber: 5, costUsd: 4.0 }),
      makeRun({ issueNumber: 4, costUsd: 4.0 }),
      makeRun({ issueNumber: 3, costUsd: 4.0 }),
      makeRun({ issueNumber: 2, costUsd: 4.0 }),
      makeRun({ issueNumber: 1, costUsd: 4.0 }),
    ];

    const state = createState(runs);
    const trend = state.getCostTrend();

    expect(trend.hasEnoughData).toBe(true);
    // After filtering: recent = [5,5,5,5,5], older = [4,4,4,4,4]
    // Median recent = 5, median older = 4 → +25%
    expect(trend.percentChange).toBe(25);
  });

  it("should treat is_supercharge=false as a normal run (not excluded)", () => {
    const runs = [
      makeRun({ issueNumber: 10, costUsd: 5.0, is_supercharge: false }),
      makeRun({ issueNumber: 9, costUsd: 5.0, is_supercharge: false }),
      makeRun({ issueNumber: 8, costUsd: 5.0 }),
      makeRun({ issueNumber: 7, costUsd: 5.0 }),
      makeRun({ issueNumber: 6, costUsd: 5.0 }),
      makeRun({ issueNumber: 5, costUsd: 4.0 }),
      makeRun({ issueNumber: 4, costUsd: 4.0 }),
      makeRun({ issueNumber: 3, costUsd: 4.0 }),
      makeRun({ issueNumber: 2, costUsd: 4.0 }),
      makeRun({ issueNumber: 1, costUsd: 4.0 }),
    ];

    const state = createState(runs);
    const trend = state.getCostTrend();

    expect(trend.hasEnoughData).toBe(true);
    expect(trend.percentChange).toBe(25);
  });
});

// ============================================================================
// computeCostTrendScore
// ============================================================================

describe("computeCostTrendScore", () => {
  it("should return 100 for stable or decreasing costs (≤15% change)", () => {
    expect(computeCostTrendScore(-50)).toBe(100);
    expect(computeCostTrendScore(-10)).toBe(100);
    expect(computeCostTrendScore(0)).toBe(100);
    expect(computeCostTrendScore(10)).toBe(100);
    expect(computeCostTrendScore(15)).toBe(100);
  });

  it("should decay gracefully for moderate increases", () => {
    const score25 = computeCostTrendScore(25);
    const score35 = computeCostTrendScore(35);
    const score50 = computeCostTrendScore(50);

    // Scores should decrease monotonically
    expect(score25).toBeGreaterThan(score35);
    expect(score35).toBeGreaterThan(score50);

    // Approximate expected values from Gaussian curve
    expect(score25).toBeGreaterThanOrEqual(90);
    expect(score35).toBeGreaterThanOrEqual(80);
    expect(score50).toBeGreaterThanOrEqual(50);
  });

  it("should approach 0 for extreme increases", () => {
    expect(computeCostTrendScore(100)).toBeLessThan(20);
    expect(computeCostTrendScore(150)).toBeLessThanOrEqual(2);
  });

  it("should never return negative scores", () => {
    expect(computeCostTrendScore(500)).toBe(0);
    expect(computeCostTrendScore(1000)).toBe(0);
  });

  it("should not score 0 for the production scenario after median fix", () => {
    // With median: recent=$7.15, older=$3.97 → +80%
    const percentChange = ((7.15 - 3.97) / 3.97) * 100; // ≈ 80.1%
    const score = computeCostTrendScore(percentChange);
    // Score should be non-zero (the old mean-based 144% would give 0)
    // With median: ~80% → excess=65 → score ≈ 18
    expect(score).toBeGreaterThan(0);
    expect(score).toBeGreaterThanOrEqual(15);
  });
});

// ============================================================================
// Edge cases
// ============================================================================

describe("getCostTrend — edge cases", () => {
  it("should return hasEnoughData=false with fewer than 6 runs", () => {
    const runs = [
      makeRun({ issueNumber: 5, costUsd: 5.0 }),
      makeRun({ issueNumber: 4, costUsd: 5.0 }),
      makeRun({ issueNumber: 3, costUsd: 5.0 }),
      makeRun({ issueNumber: 2, costUsd: 4.0 }),
      makeRun({ issueNumber: 1, costUsd: 4.0 }),
    ];

    const state = createState(runs);
    const trend = state.getCostTrend();

    // 5 runs → recent=5, older=0 → not enough older data
    expect(trend.hasEnoughData).toBe(false);
  });

  it("should return hasEnoughData=false when older baseline is all zeros", () => {
    const runs = [
      makeRun({ issueNumber: 10, costUsd: 5.0 }),
      makeRun({ issueNumber: 9, costUsd: 5.0 }),
      makeRun({ issueNumber: 8, costUsd: 5.0 }),
      makeRun({ issueNumber: 7, costUsd: 5.0 }),
      makeRun({ issueNumber: 6, costUsd: 5.0 }),
      makeRun({ issueNumber: 5, costUsd: 0.0 }),
      makeRun({ issueNumber: 4, costUsd: 0.0 }),
      makeRun({ issueNumber: 3, costUsd: 0.0 }),
      makeRun({ issueNumber: 2, costUsd: 0.0 }),
      makeRun({ issueNumber: 1, costUsd: 0.0 }),
    ];

    const state = createState(runs);
    const trend = state.getCostTrend();

    // Older median = 0 → division by zero guard
    expect(trend.hasEnoughData).toBe(false);
  });

  it("should handle empty history gracefully", () => {
    const state = createState([]);
    const trend = state.getCostTrend();

    expect(trend.hasEnoughData).toBe(false);
    expect(trend.percentChange).toBe(0);
  });
});

// ============================================================================
// getTokenTrend — median parity with getCostTrend
// ============================================================================

/**
 * Build a run with explicit token counts for trend testing. Cost is held
 * constant so getCostTrend is unaffected by these fixtures.
 */
function makeRunWithTokens(overrides: {
  issueNumber: number;
  inputTokens: number;
  outputTokens?: number;
  is_recovery?: boolean;
  is_supercharge?: boolean;
}) {
  return {
    issueNumber: overrides.issueNumber,
    title: `Issue #${overrides.issueNumber}`,
    branch: `feat/${overrides.issueNumber}`,
    startedAt: "2026-03-01T00:00:00.000Z",
    completedAt: "2026-03-01T01:00:00.000Z",
    status: "complete" as const,
    stages: [],
    usage: {
      inputTokens: overrides.inputTokens,
      outputTokens: overrides.outputTokens ?? 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 5.0,
      durationMs: 3600000,
      stageCount: 6,
    },
    toolCalls: [],
    timeSavedMs: 7200000,
    is_recovery: overrides.is_recovery,
    is_supercharge: overrides.is_supercharge,
  };
}

describe("getTokenTrend — median-based outlier resistance", () => {
  it("should use median, not mean — a single token outlier must not flip the trend", () => {
    // Recent 5: four runs at 10k tokens + one outlier at 100k
    //   Mean = 28k, Median = 10k
    // Older 5: all 10k tokens
    // With mean: +180% (bogus). With median: 0% → stable (correct).
    const runs = [
      makeRunWithTokens({ issueNumber: 10, inputTokens: 100000 }), // outlier
      makeRunWithTokens({ issueNumber: 9, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 8, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 7, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 6, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 5, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 4, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 3, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 2, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 1, inputTokens: 10000 }),
    ];

    const state = createState(runs);
    const trend = state.getTokenTrend();

    expect(trend.hasEnoughData).toBe(true);
    expect(trend.percentChange).toBe(0);
    expect(trend.direction).toBe("stable");
  });

  it("should detect real token usage increases", () => {
    // Recent 5 all 20k input, older 5 all 10k input → +100%
    const runs = [
      makeRunWithTokens({ issueNumber: 10, inputTokens: 20000 }),
      makeRunWithTokens({ issueNumber: 9, inputTokens: 20000 }),
      makeRunWithTokens({ issueNumber: 8, inputTokens: 20000 }),
      makeRunWithTokens({ issueNumber: 7, inputTokens: 20000 }),
      makeRunWithTokens({ issueNumber: 6, inputTokens: 20000 }),
      makeRunWithTokens({ issueNumber: 5, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 4, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 3, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 2, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 1, inputTokens: 10000 }),
    ];

    const state = createState(runs);
    const trend = state.getTokenTrend();

    expect(trend.hasEnoughData).toBe(true);
    expect(trend.percentChange).toBe(100);
    expect(trend.direction).toBe("up");
  });

  it("should detect token usage decreases", () => {
    const runs = [
      makeRunWithTokens({ issueNumber: 10, inputTokens: 3000 }),
      makeRunWithTokens({ issueNumber: 9, inputTokens: 3000 }),
      makeRunWithTokens({ issueNumber: 8, inputTokens: 3000 }),
      makeRunWithTokens({ issueNumber: 7, inputTokens: 3000 }),
      makeRunWithTokens({ issueNumber: 6, inputTokens: 3000 }),
      makeRunWithTokens({ issueNumber: 5, inputTokens: 6000 }),
      makeRunWithTokens({ issueNumber: 4, inputTokens: 6000 }),
      makeRunWithTokens({ issueNumber: 3, inputTokens: 6000 }),
      makeRunWithTokens({ issueNumber: 2, inputTokens: 6000 }),
      makeRunWithTokens({ issueNumber: 1, inputTokens: 6000 }),
    ];

    const state = createState(runs);
    const trend = state.getTokenTrend();

    expect(trend.hasEnoughData).toBe(true);
    expect(trend.percentChange).toBe(-50);
    expect(trend.direction).toBe("down");
  });

  it("should sum input + output tokens for the trend", () => {
    // Recent: 5k input + 5k output = 10k total. Older: 2k + 3k = 5k.
    // Change = +100%.
    const runs = [
      makeRunWithTokens({ issueNumber: 10, inputTokens: 5000, outputTokens: 5000 }),
      makeRunWithTokens({ issueNumber: 9, inputTokens: 5000, outputTokens: 5000 }),
      makeRunWithTokens({ issueNumber: 8, inputTokens: 5000, outputTokens: 5000 }),
      makeRunWithTokens({ issueNumber: 7, inputTokens: 5000, outputTokens: 5000 }),
      makeRunWithTokens({ issueNumber: 6, inputTokens: 5000, outputTokens: 5000 }),
      makeRunWithTokens({ issueNumber: 5, inputTokens: 2000, outputTokens: 3000 }),
      makeRunWithTokens({ issueNumber: 4, inputTokens: 2000, outputTokens: 3000 }),
      makeRunWithTokens({ issueNumber: 3, inputTokens: 2000, outputTokens: 3000 }),
      makeRunWithTokens({ issueNumber: 2, inputTokens: 2000, outputTokens: 3000 }),
      makeRunWithTokens({ issueNumber: 1, inputTokens: 2000, outputTokens: 3000 }),
    ];

    const state = createState(runs);
    const trend = state.getTokenTrend();

    expect(trend.percentChange).toBe(100);
  });

  it("should return stable + insufficient data with fewer than 6 runs", () => {
    const runs = Array.from({ length: 4 }, (_, i) =>
      makeRunWithTokens({ issueNumber: i + 1, inputTokens: 10000 })
    );
    const state = createState(runs);
    const trend = state.getTokenTrend();

    expect(trend.hasEnoughData).toBe(false);
    expect(trend.direction).toBe("stable");
  });

  it("should exclude supercharge and recovery runs", () => {
    // Supercharge + recovery runs with huge token counts must not pollute
    // the normal-mode baseline. Without filtering recent median would jump.
    const runs = [
      makeRunWithTokens({ issueNumber: 12, inputTokens: 200000, is_supercharge: true }),
      makeRunWithTokens({ issueNumber: 11, inputTokens: 200000, is_recovery: true }),
      makeRunWithTokens({ issueNumber: 10, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 9, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 8, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 7, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 6, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 5, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 4, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 3, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 2, inputTokens: 10000 }),
      makeRunWithTokens({ issueNumber: 1, inputTokens: 10000 }),
    ];

    const state = createState(runs);
    const trend = state.getTokenTrend();

    expect(trend.hasEnoughData).toBe(true);
    expect(trend.percentChange).toBe(0);
    expect(trend.direction).toBe("stable");
  });
});
