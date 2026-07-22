/**
 * DashboardStatistics.audit.test.ts
 *
 * Issue #1041: Cross-validate dashboard statistics against independently
 * computed values from raw data. This audit confirms that Epic #982 fixes
 * are working (zero-token filtering, cost averaging, cache hit rates) and
 * no new discrepancies exist.
 *
 * Approach: Build PipelineRunSummary arrays from controlled test data,
 * feed them into DashboardState and HealthWidgetService, then independently
 * compute every metric and compare.
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
import { HealthWidgetService } from "../../../src/views/dashboard/HealthWidget";
import { DEFAULT_HEALTH_WEIGHTS } from "../../../src/views/dashboard/HealthWidgetTypes";

// ---------------------------------------------------------------------------
// Test Data Helpers
// ---------------------------------------------------------------------------

interface SerializedRunOverrides {
  issueNumber: number;
  costUsd: number;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens?: number;
  cacheCreationTokens?: number;
  durationMs?: number;
  status?: "complete" | "failed" | "cancelled";
  stages?: Array<{
    stage: string;
    status: string;
    durationMs?: number;
    tokenUsage?: {
      inputTokens: number;
      outputTokens: number;
      cacheReadTokens: number;
      cacheCreationTokens: number;
      costUsd: number;
      model?: string;
      timestamp: string;
    };
  }>;
}

function makeSerializedRun(overrides: SerializedRunOverrides) {
  const inputTokens = overrides.inputTokens;
  const outputTokens = overrides.outputTokens;
  const cacheReadTokens = overrides.cacheReadTokens ?? 0;
  const cacheCreationTokens = overrides.cacheCreationTokens ?? 0;
  const durationMs = overrides.durationMs ?? 3600000;
  const costUsd = overrides.costUsd;

  // Pre-compute efficiency (mirrors DashboardState.calculateEfficiency)
  const durationMinutes = durationMs / 60000;
  const totalTokens = inputTokens + outputTokens;
  const cacheableTokens = totalTokens + cacheReadTokens + cacheCreationTokens;
  const efficiency =
    durationMs > 0
      ? {
          tokensPerMinute: durationMinutes > 0 ? totalTokens / durationMinutes : 0,
          costPerMinute: durationMinutes > 0 ? costUsd / durationMinutes : 0,
          cacheHitRate: cacheableTokens > 0 ? cacheReadTokens / cacheableTokens : 0,
          avgStageDurationMs: 0,
        }
      : undefined;

  return {
    issueNumber: overrides.issueNumber,
    title: `Issue #${overrides.issueNumber}`,
    branch: `feat/${overrides.issueNumber}`,
    startedAt: "2026-02-01T00:00:00.000Z",
    completedAt: "2026-02-01T01:00:00.000Z",
    status: overrides.status ?? ("complete" as const),
    stages: overrides.stages ?? [],
    usage: {
      inputTokens,
      outputTokens,
      cacheReadTokens,
      cacheCreationTokens,
      costUsd,
      durationMs,
      stageCount: overrides.stages?.length ?? 1,
    },
    toolCalls: [],
    timeSavedMs: 7200000,
    efficiency,
  };
}

function createDashboardState(runs: ReturnType<typeof makeSerializedRun>[]): DashboardState {
  const workspaceState = createMockMemento(new Map([["nightgauge.dashboard.history", runs]]));
  return new DashboardState(workspaceState);
}

// ---------------------------------------------------------------------------
// Shared test data sets
// ---------------------------------------------------------------------------

/** A representative set of 10 runs with varying characteristics */
function buildRepresentativeRuns() {
  return [
    // Run 1: Normal run with cache data
    makeSerializedRun({
      issueNumber: 100,
      costUsd: 2.55,
      inputTokens: 395,
      outputTokens: 25805,
      cacheReadTokens: 3628413,
      cacheCreationTokens: 133633,
      durationMs: 794687,
      stages: [
        {
          stage: "issue-pickup",
          status: "complete",
          durationMs: 65597,
          tokenUsage: {
            inputTokens: 86,
            outputTokens: 4343,
            cacheReadTokens: 463680,
            cacheCreationTokens: 27236,
            costUsd: 0.11,
            timestamp: "2026-02-20T00:01:17.187Z",
          },
        },
        {
          stage: "feature-dev",
          status: "complete",
          durationMs: 405610,
          tokenUsage: {
            inputTokens: 170,
            outputTokens: 14848,
            cacheReadTokens: 2518607,
            cacheCreationTokens: 64869,
            costUsd: 2.16,
            timestamp: "2026-02-20T00:08:19.534Z",
          },
        },
      ],
    }),
    // Run 2: Normal run with different cost
    makeSerializedRun({
      issueNumber: 101,
      costUsd: 3.1,
      inputTokens: 500,
      outputTokens: 30000,
      cacheReadTokens: 4000000,
      cacheCreationTokens: 150000,
      durationMs: 900000,
    }),
    // Run 3: Zero-token run (should be filtered for cache hit rate)
    makeSerializedRun({
      issueNumber: 102,
      costUsd: 0,
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      durationMs: 1000,
    }),
    // Run 4: Failed run
    makeSerializedRun({
      issueNumber: 103,
      costUsd: 1.2,
      inputTokens: 200,
      outputTokens: 10000,
      cacheReadTokens: 1000000,
      cacheCreationTokens: 50000,
      durationMs: 400000,
      status: "failed",
    }),
    // Run 5: Normal complete
    makeSerializedRun({
      issueNumber: 104,
      costUsd: 4.5,
      inputTokens: 800,
      outputTokens: 40000,
      cacheReadTokens: 5000000,
      cacheCreationTokens: 200000,
      durationMs: 1200000,
    }),
    // Run 6: Small run
    makeSerializedRun({
      issueNumber: 105,
      costUsd: 0.5,
      inputTokens: 100,
      outputTokens: 5000,
      cacheReadTokens: 500000,
      cacheCreationTokens: 20000,
      durationMs: 200000,
    }),
    // Run 7: Normal
    makeSerializedRun({
      issueNumber: 106,
      costUsd: 2.8,
      inputTokens: 350,
      outputTokens: 22000,
      cacheReadTokens: 3200000,
      cacheCreationTokens: 120000,
      durationMs: 700000,
    }),
    // Run 8: Another normal
    makeSerializedRun({
      issueNumber: 107,
      costUsd: 3.5,
      inputTokens: 600,
      outputTokens: 35000,
      cacheReadTokens: 4500000,
      cacheCreationTokens: 180000,
      durationMs: 1000000,
    }),
    // Run 9: Low cost
    makeSerializedRun({
      issueNumber: 108,
      costUsd: 0.8,
      inputTokens: 150,
      outputTokens: 8000,
      cacheReadTokens: 800000,
      cacheCreationTokens: 30000,
      durationMs: 300000,
    }),
    // Run 10: Normal
    makeSerializedRun({
      issueNumber: 109,
      costUsd: 2.0,
      inputTokens: 300,
      outputTokens: 18000,
      cacheReadTokens: 2500000,
      cacheCreationTokens: 100000,
      durationMs: 600000,
    }),
  ];
}

// ===========================================================================
// Audit Tests
// ===========================================================================

describe("Dashboard Statistics Audit (Issue #1041)", () => {
  // -------------------------------------------------------------------------
  // 1. Aggregate Token Counts
  // -------------------------------------------------------------------------
  describe("Aggregate token counts", () => {
    it("should sum input + output only for totalTokens (cache reads excluded)", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const aggregates = state.getAggregates("all");

      // Cache reads are an implementation detail — users see generated tokens only
      let expectedTokens = 0;
      for (const run of runs) {
        expectedTokens += run.usage.inputTokens + run.usage.outputTokens;
      }

      expect(aggregates.totalTokens).toBe(expectedTokens);
    });

    it("should include only input+output tokens in historical data tokens metric", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const tokenData = state.getHistoricalData("tokens", 20);

      // Zero-token runs are filtered out for cost/token sparklines
      const nonZeroRuns = runs.filter((r) => r.usage.inputTokens + r.usage.outputTokens > 0);
      const reversedRuns = [...nonZeroRuns].reverse();
      expect(tokenData.length).toBe(reversedRuns.length);
      for (let i = 0; i < tokenData.length; i++) {
        const run = reversedRuns[i];
        const expected = run.usage.inputTokens + run.usage.outputTokens;
        expect(tokenData[i]).toBe(expected);
      }
    });
  });

  // -------------------------------------------------------------------------
  // 2. Cost Calculations
  // -------------------------------------------------------------------------
  describe("Cost calculations", () => {
    it("should sum costUsd across all runs for totalCostUsd", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const aggregates = state.getAggregates("all");

      let expectedCost = 0;
      for (const run of runs) {
        expectedCost += run.usage.costUsd;
      }

      expect(aggregates.totalCostUsd).toBeCloseTo(expectedCost, 10);
    });

    it("should compute avgCostPerRun excluding zero-cost runs (#988)", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const aggregates = state.getAggregates("all");

      // Independent: average over runs with cost > 0
      const runsWithCost = runs.filter((r) => r.usage.costUsd > 0);
      let totalCost = 0;
      for (const run of runs) {
        totalCost += run.usage.costUsd;
      }
      const expectedAvg = runsWithCost.length > 0 ? totalCost / runsWithCost.length : 0;

      expect(aggregates.avgCostPerRun).toBeCloseTo(expectedAvg, 10);
    });

    it("should return per-run cost data in historical cost metric", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const costData = state.getHistoricalData("cost", 20);

      // Zero-token runs are filtered out for cost/token sparklines
      const nonZeroRuns = runs.filter((r) => r.usage.inputTokens + r.usage.outputTokens > 0);
      const reversedRuns = [...nonZeroRuns].reverse();
      expect(costData.length).toBe(reversedRuns.length);
      for (let i = 0; i < costData.length; i++) {
        expect(costData[i]).toBe(reversedRuns[i].usage.costUsd);
      }
    });
  });

  // -------------------------------------------------------------------------
  // 3. Success Rate
  // -------------------------------------------------------------------------
  describe("Success rate", () => {
    it("should compute successRate as completed / total", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const aggregates = state.getAggregates("all");

      const completed = runs.filter((r) => r.status === "complete").length;
      const expectedRate = completed / runs.length;

      expect(aggregates.successRate).toBeCloseTo(expectedRate, 10);
    });

    it("should return 0 for empty history", () => {
      const state = createDashboardState([]);
      const aggregates = state.getAggregates("all");
      expect(aggregates.successRate).toBe(0);
    });
  });

  // -------------------------------------------------------------------------
  // 4. Cache Hit Rate Calculation
  // -------------------------------------------------------------------------
  describe("Cache hit rate (#989 zero-token filtering)", () => {
    it("should compute cacheHitRate as cacheRead / (input + output + cacheRead + cacheCreation)", () => {
      const run = makeSerializedRun({
        issueNumber: 200,
        costUsd: 2.0,
        inputTokens: 400,
        outputTokens: 20000,
        cacheReadTokens: 3000000,
        cacheCreationTokens: 100000,
        durationMs: 600000,
      });

      const state = createDashboardState([run]);
      const history = state.getHistory();
      const eff = history[0].efficiency;

      // Independent calculation
      const totalTokens = 400 + 20000;
      const cacheTokens = 3000000 + 100000;
      const cacheableTokens = totalTokens + cacheTokens;
      const expectedCacheHitRate = 3000000 / cacheableTokens;

      expect(eff).toBeDefined();
      expect(eff!.cacheHitRate).toBeCloseTo(expectedCacheHitRate, 10);
    });

    it("should exclude zero-token runs from health cache hit rate average", () => {
      // Build runs: 4 with data, 1 zero-token
      const runs = [
        makeSerializedRun({
          issueNumber: 201,
          costUsd: 2.0,
          inputTokens: 400,
          outputTokens: 20000,
          cacheReadTokens: 3000000,
          cacheCreationTokens: 100000,
          durationMs: 600000,
        }),
        makeSerializedRun({
          issueNumber: 202,
          costUsd: 3.0,
          inputTokens: 500,
          outputTokens: 25000,
          cacheReadTokens: 4000000,
          cacheCreationTokens: 150000,
          durationMs: 800000,
        }),
        // Zero-token run
        makeSerializedRun({
          issueNumber: 203,
          costUsd: 0,
          inputTokens: 0,
          outputTokens: 0,
          cacheReadTokens: 0,
          cacheCreationTokens: 0,
          durationMs: 1000,
        }),
        makeSerializedRun({
          issueNumber: 204,
          costUsd: 1.5,
          inputTokens: 300,
          outputTokens: 15000,
          cacheReadTokens: 2000000,
          cacheCreationTokens: 80000,
          durationMs: 500000,
        }),
        makeSerializedRun({
          issueNumber: 205,
          costUsd: 2.5,
          inputTokens: 450,
          outputTokens: 22000,
          cacheReadTokens: 3500000,
          cacheCreationTokens: 120000,
          durationMs: 700000,
        }),
      ];

      const state = createDashboardState(runs);
      const widget = new HealthWidgetService(state, undefined);
      const components = widget.computeHealthComponents();
      const cacheComp = components.find((c) => c.name === "cacheHitRate");

      expect(cacheComp).toBeDefined();

      // Independent: compute average cache hit rate from last 5, excluding zero-token
      const history = state.getHistory();
      const recentRuns = history.slice(0, 5);
      const validRuns = recentRuns.filter((r) => {
        const u = r.usage;
        return (
          u.inputTokens + u.outputTokens + (u.cacheReadTokens ?? 0) + (u.cacheCreationTokens ?? 0) >
          0
        );
      });

      const avgCacheHitRate =
        validRuns.length > 0
          ? validRuns.reduce((sum, r) => sum + (r.efficiency?.cacheHitRate ?? 0), 0) /
            validRuns.length
          : 0;
      const expectedScore = Math.round(avgCacheHitRate * 100);

      expect(cacheComp!.score).toBe(expectedScore);
      // Zero-token run should NOT be included — 4 valid runs, not 5
      expect(validRuns.length).toBe(4);
    });
  });

  // -------------------------------------------------------------------------
  // 5. Health Score Formula
  // -------------------------------------------------------------------------
  describe("Health score formula", () => {
    it("should compute weighted score = sum(score * weight) / sum(weights)", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const widget = new HealthWidgetService(state, undefined);
      const components = widget.computeHealthComponents();
      const score = widget.computeWeightedScore(components);

      // Independent weighted average calculation
      let weightedSum = 0;
      let totalWeight = 0;
      for (const c of components) {
        weightedSum += c.score * c.weight;
        totalWeight += c.weight;
      }
      const expectedScore = totalWeight > 0 ? Math.round(weightedSum / totalWeight) : 0;

      expect(score).toBe(expectedScore);
    });

    it("should return 0 for empty components", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const widget = new HealthWidgetService(state, undefined);
      expect(widget.computeWeightedScore([])).toBe(0);
    });

    it("should produce individual component scores within 0-100", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const widget = new HealthWidgetService(state, undefined);
      const components = widget.computeHealthComponents();

      for (const c of components) {
        expect(c.score).toBeGreaterThanOrEqual(0);
        expect(c.score).toBeLessThanOrEqual(100);
      }
    });

    it("should include all 4 health components", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const widget = new HealthWidgetService(state, undefined);
      const components = widget.computeHealthComponents();

      const expectedNames = ["successRate", "costTrend", "failureRate", "cacheHitRate"];
      const componentNames = components.map((c) => c.name);
      expect(componentNames).toEqual(expectedNames);
    });
  });

  // -------------------------------------------------------------------------
  // 6. Success Rate Component
  // -------------------------------------------------------------------------
  describe("Health component: success rate", () => {
    it("should match independently computed success rate", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const widget = new HealthWidgetService(state, undefined);
      const components = widget.computeHealthComponents();
      const successComp = components.find((c) => c.name === "successRate");

      const completed = runs.filter((r) => r.status === "complete").length;
      const expectedScore = Math.round((completed / runs.length) * 100);

      expect(successComp!.score).toBe(expectedScore);
    });
  });

  // -------------------------------------------------------------------------
  // 7. Reliability Component (failure rate inverse)
  // -------------------------------------------------------------------------
  describe("Health component: reliability", () => {
    it("should equal (1 - failureRate) * 100", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const widget = new HealthWidgetService(state, undefined);
      const components = widget.computeHealthComponents();
      const reliabilityComp = components.find((c) => c.name === "failureRate");

      const history = state.getHistory();
      const failed = history.filter((r) => r.status === "failed").length;
      const failureRate = history.length > 0 ? failed / history.length : 0;
      const expectedScore = Math.round((1 - failureRate) * 100);

      expect(reliabilityComp!.score).toBe(expectedScore);
    });
  });

  // -------------------------------------------------------------------------
  // 8. Cost Trend
  // -------------------------------------------------------------------------
  describe("Cost trend", () => {
    it("should compare median cost of recent 5 vs older 5 runs", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const trend = state.getCostTrend();

      // History is stored most-recent-first, so slice(0,5) is recent
      // Cost trend now filters recovery and supercharge runs, then uses median
      const history = state.getHistory();
      const trendHistory = history.filter((r) => !r.is_recovery && !r.is_supercharge);
      const recent = trendHistory.slice(0, 5);
      const older = trendHistory.slice(5, 10);

      const medianCost = (rs: typeof history) => {
        if (rs.length === 0) return 0;
        const sorted = rs.map((r) => r.usage.costUsd).sort((a, b) => a - b);
        const mid = Math.floor(sorted.length / 2);
        return sorted.length % 2 === 0 ? (sorted[mid - 1] + sorted[mid]) / 2 : sorted[mid];
      };

      const recentMedian = medianCost(recent);
      const olderMedian = medianCost(older);

      if (recent.length >= 3 && older.length >= 3 && olderMedian > 0) {
        const expectedChange =
          Math.round(((recentMedian - olderMedian) / olderMedian) * 100 * 10) / 10;
        expect(trend.percentChange).toBe(expectedChange);
        expect(trend.improving).toBe(expectedChange < 0);
        expect(trend.hasEnoughData).toBe(true);
      }
    });
  });

  // -------------------------------------------------------------------------
  // 9. Token Trend
  // -------------------------------------------------------------------------
  describe("Token trend", () => {
    it("should compare median (input + output) of recent 5 vs older 5", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const trend = state.getTokenTrend();

      const history = state.getHistory();
      const recent = history.slice(0, 5);
      const older = history.slice(5, 10);

      const medianTokens = (rs: typeof history) => {
        if (rs.length === 0) return 0;
        const sorted = rs
          .map((r) => r.usage.inputTokens + r.usage.outputTokens)
          .sort((a, b) => a - b);
        const mid = Math.floor(sorted.length / 2);
        return sorted.length % 2 === 0 ? (sorted[mid - 1] + sorted[mid]) / 2 : sorted[mid];
      };

      const recentMedian = medianTokens(recent);
      const olderMedian = medianTokens(older);

      if (recent.length >= 3 && older.length >= 3 && olderMedian > 0) {
        const percentChange =
          Math.round(((recentMedian - olderMedian) / olderMedian) * 100 * 10) / 10;
        expect(trend.percentChange).toBe(percentChange);

        let expectedDir: "up" | "down" | "stable" = "stable";
        if (Math.abs(percentChange) >= 5) {
          expectedDir = percentChange > 0 ? "up" : "down";
        }
        expect(trend.direction).toBe(expectedDir);
      }
    });
  });

  // -------------------------------------------------------------------------
  // 10. Efficiency Trend
  // -------------------------------------------------------------------------
  describe("Efficiency trend", () => {
    it("should compare cost-per-stage of recent 5 vs older 5", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const trend = state.getEfficiencyTrend();

      const history = state.getHistory();
      // Filter non-recovery runs with stageCount > 0 (matches implementation)
      const valid = history.filter((r) => {
        const sc = r.usage.stageCount ?? r.stages.filter((s) => s.status === "complete").length;
        return !(r as any).is_recovery && sc > 0;
      });
      const recent = valid.slice(0, 5);
      const older = valid.slice(5, 10);

      const avgCostPerStage = (rs: typeof valid) => {
        if (rs.length === 0) return 0;
        return (
          rs.reduce((sum, r) => {
            const sc = r.usage.stageCount ?? r.stages.filter((s) => s.status === "complete").length;
            return sum + (sc > 0 ? r.usage.costUsd / sc : 0);
          }, 0) / rs.length
        );
      };

      const recentAvg = avgCostPerStage(recent);
      const olderAvg = avgCostPerStage(older);

      if (recent.length >= 3 && older.length >= 3 && olderAvg > 0) {
        const expectedChange = Math.round(((recentAvg - olderAvg) / olderAvg) * 100 * 10) / 10;
        expect(trend.percentChange).toBe(expectedChange);
        // Lower cost = improving (percentChange < 0)
        expect(trend.improving).toBe(expectedChange < 0);
      }
    });
  });

  // -------------------------------------------------------------------------
  // 11. Sparkline Data Verification
  // -------------------------------------------------------------------------
  describe("Sparkline data points", () => {
    it("should return cost sparkline matching historical cost data", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const widget = new HealthWidgetService(state, undefined);
      const sparklines = widget.getSparklines(10);
      const costSparkline = sparklines.find((s) => s.metric === "cost");

      const costData = state.getHistoricalData("cost", 10);
      expect(costSparkline).toBeDefined();
      expect(costSparkline!.data).toEqual(costData);
    });

    it("should return token sparkline matching historical token data", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const widget = new HealthWidgetService(state, undefined);
      const sparklines = widget.getSparklines(10);
      const tokenSparkline = sparklines.find((s) => s.metric === "tokens");

      const tokenData = state.getHistoricalData("tokens", 10);
      expect(tokenSparkline).toBeDefined();
      expect(tokenSparkline!.data).toEqual(tokenData);
    });

    // The Recent Activity row on the Overview tab no longer emits cache-hit
    // or success-rate sparklines — both values are already shown as composite
    // scores in the Pipeline Health component cards directly above. The old
    // "#989 zero-token mapping" rule continues to apply to the score path
    // (verified by HealthWidget.cacheFilter.test.ts), just not to a sparkline
    // that doesn't exist anymore.
    it("does not emit cache-hit or success-rate sparklines on the Overview tab", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const widget = new HealthWidgetService(state, undefined);
      const sparklines = widget.getSparklines(10);

      expect(sparklines.find((s) => s.metric === "cacheHitRate")).toBeUndefined();
      expect(sparklines.find((s) => s.metric === "successRate")).toBeUndefined();
    });
  });

  // -------------------------------------------------------------------------
  // 12. Per-Stage Breakdown
  // -------------------------------------------------------------------------
  describe("Per-stage breakdown", () => {
    it("should compute per-stage average cost, tokens, and duration", () => {
      const stageData = {
        stage: "issue-pickup",
        status: "complete",
        durationMs: 60000,
        tokenUsage: {
          inputTokens: 100,
          outputTokens: 5000,
          cacheReadTokens: 500000,
          cacheCreationTokens: 20000,
          costUsd: 0.15,
          timestamp: "2026-02-20T00:01:00.000Z",
        },
      };

      const stageData2 = {
        stage: "issue-pickup",
        status: "complete",
        durationMs: 80000,
        tokenUsage: {
          inputTokens: 120,
          outputTokens: 6000,
          cacheReadTokens: 600000,
          cacheCreationTokens: 25000,
          costUsd: 0.2,
          timestamp: "2026-02-20T01:01:00.000Z",
        },
      };

      const runs = [
        makeSerializedRun({
          issueNumber: 400,
          costUsd: 2.0,
          inputTokens: 400,
          outputTokens: 20000,
          cacheReadTokens: 3000000,
          cacheCreationTokens: 100000,
          durationMs: 600000,
          stages: [stageData],
        }),
        makeSerializedRun({
          issueNumber: 401,
          costUsd: 3.0,
          inputTokens: 500,
          outputTokens: 25000,
          cacheReadTokens: 4000000,
          cacheCreationTokens: 150000,
          durationMs: 800000,
          stages: [stageData2],
        }),
      ];

      const state = createDashboardState(runs);
      const averages = state.getPerStageAverages("all");

      const issuePickup = averages.find((a) => a.stage === "issue-pickup");
      expect(issuePickup).toBeDefined();

      // Independent calculation
      expect(issuePickup!.avgCostUsd).toBeCloseTo((0.15 + 0.2) / 2, 10);
      expect(issuePickup!.avgInputTokens).toBeCloseTo((100 + 120) / 2, 10);
      expect(issuePickup!.avgOutputTokens).toBeCloseTo((5000 + 6000) / 2, 10);
      expect(issuePickup!.avgCacheReadTokens).toBeCloseTo((500000 + 600000) / 2, 10);
      expect(issuePickup!.avgCacheCreationTokens).toBeCloseTo((20000 + 25000) / 2, 10);
      expect(issuePickup!.avgDurationMs).toBeCloseTo((60000 + 80000) / 2, 10);
      expect(issuePickup!.runCount).toBe(2);
    });

    it("should skip stages with no token data and no duration", () => {
      const runs = [
        makeSerializedRun({
          issueNumber: 410,
          costUsd: 2.0,
          inputTokens: 400,
          outputTokens: 20000,
          cacheReadTokens: 3000000,
          cacheCreationTokens: 100000,
          durationMs: 600000,
          stages: [
            {
              stage: "feature-planning",
              status: "skipped",
              // No tokenUsage, no durationMs
            },
          ],
        }),
      ];

      const state = createDashboardState(runs);
      const averages = state.getPerStageAverages("all");
      const planning = averages.find((a) => a.stage === "feature-planning");
      expect(planning).toBeUndefined();
    });
  });

  // -------------------------------------------------------------------------
  // 13. Edge Cases
  // -------------------------------------------------------------------------
  describe("Edge cases", () => {
    it("should handle single run correctly", () => {
      const runs = [
        makeSerializedRun({
          issueNumber: 500,
          costUsd: 2.0,
          inputTokens: 400,
          outputTokens: 20000,
          cacheReadTokens: 3000000,
          cacheCreationTokens: 100000,
          durationMs: 600000,
        }),
      ];
      const state = createDashboardState(runs);
      const aggregates = state.getAggregates("all");

      expect(aggregates.totalRuns).toBe(1);
      expect(aggregates.totalCostUsd).toBe(2.0);
      expect(aggregates.successRate).toBe(1.0);
      expect(aggregates.totalTokens).toBe(400 + 20000); // cache reads excluded
    });

    it("should handle no runs", () => {
      const state = createDashboardState([]);
      const aggregates = state.getAggregates("all");

      expect(aggregates.totalRuns).toBe(0);
      expect(aggregates.totalCostUsd).toBe(0);
      expect(aggregates.successRate).toBe(0);
      expect(aggregates.totalTokens).toBe(0);
      expect(aggregates.avgCostPerRun).toBe(0);
    });

    it("should handle all failed runs", () => {
      const runs = [
        makeSerializedRun({
          issueNumber: 510,
          costUsd: 1.0,
          inputTokens: 100,
          outputTokens: 5000,
          durationMs: 300000,
          status: "failed",
        }),
        makeSerializedRun({
          issueNumber: 511,
          costUsd: 1.5,
          inputTokens: 200,
          outputTokens: 8000,
          durationMs: 400000,
          status: "failed",
        }),
      ];
      const state = createDashboardState(runs);
      const aggregates = state.getAggregates("all");

      expect(aggregates.successRate).toBe(0);
    });

    it("should handle runs with missing cacheReadTokens/cacheCreationTokens", () => {
      const run = makeSerializedRun({
        issueNumber: 520,
        costUsd: 2.0,
        inputTokens: 400,
        outputTokens: 20000,
        durationMs: 600000,
        // No cacheReadTokens or cacheCreationTokens
      });
      const state = createDashboardState([run]);
      const aggregates = state.getAggregates("all");

      // Cache reads excluded — only input + output shown
      expect(aggregates.totalTokens).toBe(400 + 20000);
    });
  });

  // -------------------------------------------------------------------------
  // 14. Epic #982 Fix Verification
  // -------------------------------------------------------------------------
  describe("Epic #982 fix verification", () => {
    it("#984: zero-token stages are not counted in per-stage averages if they have no duration", () => {
      const runs = [
        makeSerializedRun({
          issueNumber: 700,
          costUsd: 2.0,
          inputTokens: 400,
          outputTokens: 20000,
          cacheReadTokens: 3000000,
          cacheCreationTokens: 100000,
          durationMs: 600000,
          stages: [
            {
              stage: "feature-planning",
              status: "skipped",
              // No tokenUsage, no durationMs = zero-token stage
            },
            {
              stage: "feature-dev",
              status: "complete",
              durationMs: 400000,
              tokenUsage: {
                inputTokens: 200,
                outputTokens: 15000,
                cacheReadTokens: 2500000,
                cacheCreationTokens: 80000,
                costUsd: 1.8,
                timestamp: "2026-02-20T00:05:00.000Z",
              },
            },
          ],
        }),
      ];
      const state = createDashboardState(runs);
      const averages = state.getPerStageAverages("all");
      const planning = averages.find((a) => a.stage === "feature-planning");
      expect(planning).toBeUndefined();
    });

    it("#988: avgCostPerRun excludes $0 runs", () => {
      const runs = [
        makeSerializedRun({
          issueNumber: 710,
          costUsd: 2.0,
          inputTokens: 400,
          outputTokens: 20000,
          durationMs: 600000,
        }),
        makeSerializedRun({
          issueNumber: 711,
          costUsd: 0,
          inputTokens: 0,
          outputTokens: 0,
          durationMs: 1000,
        }),
        makeSerializedRun({
          issueNumber: 712,
          costUsd: 4.0,
          inputTokens: 800,
          outputTokens: 40000,
          durationMs: 1200000,
        }),
      ];
      const state = createDashboardState(runs);
      const aggregates = state.getAggregates("all");

      // Total cost = 6.0, runs with cost > 0 = 2
      // Average = 6.0 / 2 = 3.0
      expect(aggregates.avgCostPerRun).toBeCloseTo(3.0, 10);
    });

    it("#989: zero-token runs get cacheHitRate 0 in efficiency", () => {
      const run = makeSerializedRun({
        issueNumber: 720,
        costUsd: 0,
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        durationMs: 1000,
      });
      const state = createDashboardState([run]);
      const history = state.getHistory();

      // Zero-token run: cacheableTokens = 0, so cacheHitRate should be 0
      expect(history[0].efficiency?.cacheHitRate ?? 0).toBe(0);
    });
  });

  // -------------------------------------------------------------------------
  // 15. Historical Data Metric Verification
  // -------------------------------------------------------------------------
  describe("Historical data metrics", () => {
    it("should return duration data matching run durationMs", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const durationData = state.getHistoricalData("duration", 20);

      const reversedRuns = [...runs].reverse();
      for (let i = 0; i < durationData.length; i++) {
        expect(durationData[i]).toBe(reversedRuns[i].usage.durationMs);
      }
    });

    it("should respect limit parameter", () => {
      const runs = buildRepresentativeRuns();
      const state = createDashboardState(runs);
      const costData = state.getHistoricalData("cost", 3);
      expect(costData.length).toBe(3);
    });
  });
});
