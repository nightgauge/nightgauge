/**
 * HealthWidget.audit.test.ts
 *
 * Audit tests for health score calculation accuracy, trending, and edge cases.
 * Validates the formula against known inputs, verifies trend analysis behavior,
 * and tests edge cases (0 runs, 1 run, all failures, all successes).
 *
 * @see Issue #1044 - Audit: Validate health score calculation, trending, and actionability
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock SDK analysis modules to simulate unavailability
vi.mock("@nightgauge/sdk/dist/analysis/FailurePatternDetector", () => {
  throw new Error("SDK not available in test");
});
vi.mock("@nightgauge/sdk/dist/analysis/TokenEfficiencyAnalyzer", () => {
  throw new Error("SDK not available in test");
});

vi.mock("vscode", () => ({
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn((_key: string, defaultValue: unknown) => defaultValue),
    })),
  },
}));

import { HealthWidgetService } from "../../../src/views/dashboard/HealthWidget";
import {
  DEFAULT_HEALTH_WEIGHTS,
  getHealthStatus,
} from "../../../src/views/dashboard/HealthWidgetTypes";
import { HealthScoreHistoryReader } from "../../../src/utils/healthScoreHistory";
import type { HealthScoreSnapshot } from "../../../src/schemas/healthScoreHistory";

// ---------------------------------------------------------------------------
// Mock factories
// ---------------------------------------------------------------------------

const createMockDashboardState = (overrides?: Record<string, unknown>) => ({
  getHistory: vi.fn().mockReturnValue([]),
  getEfficiencyTrend: vi.fn().mockReturnValue({
    improving: true,
    percentChange: 0,
    hasEnoughData: false,
  }),
  getCostTrend: vi.fn().mockReturnValue({
    improving: true,
    percentChange: 0,
    hasEnoughData: false,
  }),
  getTokenTrend: vi.fn().mockReturnValue({
    direction: "stable",
    percentChange: 0,
    hasEnoughData: false,
  }),
  getAggregates: vi.fn().mockReturnValue({ successRate: 1.0, totalRuns: 10 }),
  getHistoricalData: vi.fn().mockReturnValue([]),
  getVelocityInsights: vi.fn().mockResolvedValue(null),
  getAccuracyTrend: vi.fn().mockResolvedValue(null),
  ...overrides,
});

const createMockRun = (overrides?: Record<string, unknown>) => ({
  issueNumber: 1,
  title: "Test",
  branch: "feat/1",
  startedAt: new Date(),
  completedAt: new Date(),
  status: "complete",
  stages: [],
  usage: {
    inputTokens: 1000,
    outputTokens: 500,
    cacheReadTokens: 200,
    cacheCreationTokens: 100,
    costUsd: 0.05,
    durationMs: 60000,
    stageCount: 6,
  },
  toolCalls: [],
  efficiency: {
    tokensPerMinute: 100,
    costPerMinute: 0.001,
    cacheHitRate: 0.5,
    avgStageDurationMs: 10000,
  },
  ...overrides,
});

function createValidSnapshot(overrides: Partial<HealthScoreSnapshot> = {}): HealthScoreSnapshot {
  return {
    schema_version: "1",
    timestamp: "2026-02-15T10:00:00Z",
    score: 75,
    status: "good",
    components: { successRate: 80, costTrend: 70 },
    cacheHitRate: 0.45,
    costUsd: 0.12,
    issueNumber: 42,
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Audit Tests
// ---------------------------------------------------------------------------

describe("Health Score Audit (#1044)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe("Formula Verification: weighted average matches expected output", () => {
    it("computes correct score from known component values", () => {
      // Arrange: 4 components with new weights: successRate=0.30, costTrend=0.30,
      //          failureRate=0.25, cacheHitRate=0.15
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue([createMockRun()]),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const components = [
        {
          name: "successRate",
          score: 100,
          weight: DEFAULT_HEALTH_WEIGHTS.successRate,
          trend: "improving" as const,
          label: "Success Rate",
        },
        {
          name: "costTrend",
          score: 18.6,
          weight: DEFAULT_HEALTH_WEIGHTS.costTrend,
          trend: "stable" as const,
          label: "Cost Trend",
        },
        {
          name: "failureRate",
          score: 100,
          weight: DEFAULT_HEALTH_WEIGHTS.failureRate,
          trend: "improving" as const,
          label: "Reliability",
        },
        {
          name: "cacheHitRate",
          score: 77,
          weight: DEFAULT_HEALTH_WEIGHTS.cacheHitRate,
          trend: "improving" as const,
          label: "Cache Hit Rate",
        },
      ];

      // Act
      const score = service.computeWeightedScore(components);

      // Assert: manual calculation
      // 100*0.30 + 18.6*0.30 + 100*0.25 + 77*0.15
      // = 30.0 + 5.58 + 25.0 + 11.55 = 72.13 → rounds to 72
      expect(score).toBe(72);
    });

    it("computes correct score for entry with some zeroed components", () => {
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue([createMockRun()]),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const components = [
        {
          name: "successRate",
          score: 100,
          weight: DEFAULT_HEALTH_WEIGHTS.successRate,
          trend: "improving" as const,
          label: "Success Rate",
        },
        {
          name: "costTrend",
          score: 100,
          weight: DEFAULT_HEALTH_WEIGHTS.costTrend,
          trend: "improving" as const,
          label: "Cost Trend",
        },
        {
          name: "failureRate",
          score: 100,
          weight: DEFAULT_HEALTH_WEIGHTS.failureRate,
          trend: "improving" as const,
          label: "Reliability",
        },
        {
          name: "cacheHitRate",
          score: 0,
          weight: DEFAULT_HEALTH_WEIGHTS.cacheHitRate,
          trend: "degrading" as const,
          label: "Cache Hit Rate",
        },
      ];

      const score = service.computeWeightedScore(components);

      // 100*0.30 + 100*0.30 + 100*0.25 + 0*0.15
      // = 30.0 + 30.0 + 25.0 + 0 = 85.0 → rounds to 85
      expect(score).toBe(85);
    });

    it("computes correct score for high-score entry", () => {
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue([createMockRun()]),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const components = [
        {
          name: "successRate",
          score: 96,
          weight: DEFAULT_HEALTH_WEIGHTS.successRate,
          trend: "improving" as const,
          label: "Success Rate",
        },
        {
          name: "costTrend",
          score: 67.1,
          weight: DEFAULT_HEALTH_WEIGHTS.costTrend,
          trend: "improving" as const,
          label: "Cost Trend",
        },
        {
          name: "failureRate",
          score: 100,
          weight: DEFAULT_HEALTH_WEIGHTS.failureRate,
          trend: "improving" as const,
          label: "Reliability",
        },
        {
          name: "cacheHitRate",
          score: 94,
          weight: DEFAULT_HEALTH_WEIGHTS.cacheHitRate,
          trend: "improving" as const,
          label: "Cache Hit Rate",
        },
      ];

      const score = service.computeWeightedScore(components);

      // 96*0.30 + 67.1*0.30 + 100*0.25 + 94*0.15
      // = 28.8 + 20.13 + 25.0 + 14.1 = 88.03 → rounds to 88
      expect(score).toBe(88);
    });
  });

  describe("Weight normalization", () => {
    it("default weights sum to 1.0", () => {
      const sum =
        DEFAULT_HEALTH_WEIGHTS.successRate +
        DEFAULT_HEALTH_WEIGHTS.costTrend +
        DEFAULT_HEALTH_WEIGHTS.failureRate +
        DEFAULT_HEALTH_WEIGHTS.cacheHitRate;

      expect(sum).toBeCloseTo(1.0, 10);
    });

    it("auto-normalizes non-1.0 weights to produce same result as manual normalization", () => {
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue([createMockRun()]),
      });

      // Double all weights — should produce identical score
      const doubledWeights = {
        successRate: 0.6,
        costTrend: 0.6,
        failureRate: 0.5,
        cacheHitRate: 0.3,
      };

      const normalService = new HealthWidgetService(state as any, "/workspace");
      const doubledService = new HealthWidgetService(state as any, "/workspace", doubledWeights);

      const normalComponents = normalService.computeHealthComponents();
      const doubledComponents = doubledService.computeHealthComponents();

      const normalScore = normalService.computeWeightedScore(normalComponents);
      const doubledScore = doubledService.computeWeightedScore(doubledComponents);

      expect(normalScore).toBe(doubledScore);
    });
  });

  describe("getHealthStatus thresholds", () => {
    const cases: [number, string][] = [
      [100, "excellent"],
      [90, "excellent"],
      [89, "good"],
      [70, "good"],
      [69, "fair"],
      [50, "fair"],
      [49, "poor"],
      [30, "poor"],
      [29, "critical"],
      [0, "critical"],
    ];

    cases.forEach(([score, expected]) => {
      it(`score ${score} maps to "${expected}"`, () => {
        expect(getHealthStatus(score)).toBe(expected);
      });
    });
  });

  describe("Edge cases: extreme run scenarios", () => {
    it("all failures: successRate=0 and reliability=0 produce low score", () => {
      const failedRuns = Array.from({ length: 5 }, (_, i) =>
        createMockRun({
          issueNumber: i + 1,
          status: "failed",
          efficiency: {
            tokensPerMinute: 0,
            costPerMinute: 0,
            cacheHitRate: 0,
            avgStageDurationMs: 0,
          },
        })
      );
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(failedRuns),
        getAggregates: vi.fn().mockReturnValue({ successRate: 0, totalRuns: 5 }),
        getEfficiencyTrend: vi.fn().mockReturnValue({
          improving: false,
          percentChange: -50,
          hasEnoughData: true,
        }),
        getCostTrend: vi.fn().mockReturnValue({
          improving: false,
          percentChange: 50,
          hasEnoughData: true,
        }),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const components = service.computeHealthComponents();
      const score = service.computeWeightedScore(components);

      // successRate=0 (weight 0.22) + failureRate=0 (weight 0.10) = 32% of score is 0
      expect(score).toBeLessThanOrEqual(40);

      const successComp = components.find((c) => c.name === "successRate");
      const reliabilityComp = components.find((c) => c.name === "failureRate");
      expect(successComp?.score).toBe(0);
      expect(reliabilityComp?.score).toBe(0);
    });

    it("all successes: successRate=100 and reliability=100", () => {
      const successRuns = Array.from({ length: 5 }, (_, i) =>
        createMockRun({
          issueNumber: i + 1,
          status: "complete",
        })
      );
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(successRuns),
        getAggregates: vi.fn().mockReturnValue({ successRate: 1.0, totalRuns: 5 }),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const components = service.computeHealthComponents();

      const successComp = components.find((c) => c.name === "successRate");
      const reliabilityComp = components.find((c) => c.name === "failureRate");
      expect(successComp?.score).toBe(100);
      expect(reliabilityComp?.score).toBe(100);
    });

    it("single run: components still compute valid scores", () => {
      const runs = [createMockRun()];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
        getAggregates: vi.fn().mockReturnValue({ successRate: 1.0, totalRuns: 1 }),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const components = service.computeHealthComponents();
      const score = service.computeWeightedScore(components);

      expect(components).toHaveLength(4);
      expect(score).toBeGreaterThanOrEqual(0);
      expect(score).toBeLessThanOrEqual(100);

      // With insufficient trend data, cost defaults to 100 (assume healthy)
      const cost = components.find((c) => c.name === "costTrend");
      expect(cost?.score).toBe(100);
      expect(cost?.insufficientData).toBe(true);
    });

    it("0 runs: getData returns isEmpty state", async () => {
      const state = createMockDashboardState();
      const service = new HealthWidgetService(state as any, "/workspace");

      const data = await service.getData();

      expect(data.isEmpty).toBe(true);
      expect(data.summary.score).toBe(0);
      expect(data.summary.status).toBe("critical");
      expect(data.summary.components).toHaveLength(0);
    });
  });

  describe("Trend analysis verification", () => {
    // Use deterministic UTC date strings to avoid DST boundary collisions.
    // new Date() + setDate() + toISOString() can produce duplicate UTC dates
    // when a DST transition falls within the 14-day window (e.g., US Central
    // DST on March 8 causes i=7 and i=6 to both map to "2026-03-08" UTC).
    function makeDays(
      count: number,
      scoreFn: (i: number) => number
    ): Array<{ date: string; avgScore: number; count: number }> {
      const days = [];
      for (let i = count - 1; i >= 0; i--) {
        // Fixed base date far from any DST boundary
        const d = new Date(Date.UTC(2026, 0, 15 + (count - 1 - i))); // Jan 15+
        days.push({
          date: d.toISOString().split("T")[0],
          avgScore: scoreFn(i),
          count: 1,
        });
      }
      return days;
    }

    it("detects improving trend with >2% increase", () => {
      const days = makeDays(14, (i) => (i >= 7 ? 50 : 55)); // 10% increase
      const result = HealthScoreHistoryReader.analyzeTrend(days);
      expect(result.direction).toBe("improving");
      expect(result.percentChange).toBe(10);
    });

    it("detects declining trend with <-2% decrease", () => {
      const days = makeDays(14, (i) => (i >= 7 ? 80 : 70)); // -12.5% decrease
      const result = HealthScoreHistoryReader.analyzeTrend(days);
      expect(result.direction).toBe("declining");
      expect(result.percentChange).toBeLessThan(-2);
    });

    it("reports stable within ±2% threshold", () => {
      const days = makeDays(14, (i) => (i >= 7 ? 75 : 76)); // ~1.3% change
      const result = HealthScoreHistoryReader.analyzeTrend(days);
      expect(result.direction).toBe("stable");
      expect(Math.abs(result.percentChange)).toBeLessThanOrEqual(2);
    });

    it("handles exactly 7 days (no prior period)", () => {
      const days = makeDays(7, () => 70);
      const result = HealthScoreHistoryReader.analyzeTrend(days);
      expect(result.direction).toBe("stable");
      expect(result.message).toBe("Not enough history for trend comparison");
    });

    it("handles prior period with 0 average (division by zero protection)", () => {
      const days = makeDays(14, (i) => (i >= 7 ? 0 : 50));
      const result = HealthScoreHistoryReader.analyzeTrend(days);
      // When priorAvg is 0, should return stable with insufficient baseline
      expect(result.direction).toBe("stable");
      expect(result.message).toBe("Insufficient baseline data");
    });
  });

  describe("recordSnapshot persists component scores (#1044)", () => {
    it("persists the 4 health component scores in the snapshot", async () => {
      const { HealthScoreHistoryWriter } = await import("../../../src/utils/healthScoreHistory");
      const appendSpy = vi.spyOn(HealthScoreHistoryWriter, "appendSnapshot").mockResolvedValue();

      const runs = [createMockRun(), createMockRun({ issueNumber: 2 })];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      await service.recordSnapshot(42, 0.15);

      expect(appendSpy).toHaveBeenCalledTimes(1);
      const snapshot = appendSpy.mock.calls[0][1];
      // Snapshot should contain the 4 kept component scores
      expect(snapshot.components.successRate).toBeDefined();
      expect(snapshot.components.costTrend).toBeDefined();
      // predictionAccuracy is no longer a health component
      expect(snapshot.components.predictionAccuracy).toBeUndefined();

      appendSpy.mockRestore();
    });
  });

  describe("aggregateByDay correctness", () => {
    it("correctly averages multiple scores on the same day", () => {
      const snapshots: HealthScoreSnapshot[] = [
        createValidSnapshot({ timestamp: "2026-02-20T01:10:47Z", score: 81 }),
        createValidSnapshot({ timestamp: "2026-02-20T01:48:05Z", score: 69 }),
        createValidSnapshot({ timestamp: "2026-02-20T02:13:45Z", score: 66 }),
      ];

      const result = HealthScoreHistoryReader.aggregateByDay(snapshots);

      expect(result).toHaveLength(1);
      expect(result[0].date).toBe("2026-02-20");
      // (81 + 69 + 66) / 3 = 72
      expect(result[0].avgScore).toBe(72);
      expect(result[0].count).toBe(3);
    });

    it("handles snapshots across multiple days", () => {
      const snapshots: HealthScoreSnapshot[] = [
        createValidSnapshot({ timestamp: "2026-02-17T22:08:25Z", score: 66 }),
        createValidSnapshot({ timestamp: "2026-02-17T22:17:28Z", score: 66 }),
        createValidSnapshot({ timestamp: "2026-02-18T00:12:31Z", score: 60 }),
        createValidSnapshot({ timestamp: "2026-02-19T16:12:37Z", score: 66 }),
      ];

      const result = HealthScoreHistoryReader.aggregateByDay(snapshots);

      expect(result).toHaveLength(3);
      expect(result[0].date).toBe("2026-02-17");
      expect(result[0].avgScore).toBe(66);
      expect(result[0].count).toBe(2);
      expect(result[1].date).toBe("2026-02-18");
      expect(result[1].avgScore).toBe(60);
      expect(result[2].date).toBe("2026-02-19");
      expect(result[2].avgScore).toBe(66);
    });
  });

  describe("Component score scaling (dead-band Gaussian)", () => {
    // Stable or decreasing costs score 100. Only sustained increases
    // beyond a 15% dead band reduce the score via Gaussian decay.

    it("costTrend: decreasing costs score 100", () => {
      const runs = [createMockRun()];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
        getCostTrend: vi.fn().mockReturnValue({
          improving: true,
          percentChange: -20, // Cost decreased 20%
          hasEnoughData: true,
        }),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const components = service.computeHealthComponents();
      const cost = components.find((c) => c.name === "costTrend");

      expect(cost?.score).toBe(100);
    });

    it("costTrend: flat costs (0% change) score 100", () => {
      const runs = [createMockRun()];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
        getCostTrend: vi.fn().mockReturnValue({
          improving: false,
          percentChange: 0,
          hasEnoughData: true,
        }),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const components = service.computeHealthComponents();
      const cost = components.find((c) => c.name === "costTrend");

      expect(cost?.score).toBe(100);
    });

    it("costTrend: within 15% dead band scores 100", () => {
      const runs = [createMockRun()];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
        getCostTrend: vi.fn().mockReturnValue({
          improving: false,
          percentChange: 14, // Just under dead band
          hasEnoughData: true,
        }),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const components = service.computeHealthComponents();
      const cost = components.find((c) => c.name === "costTrend");

      expect(cost?.score).toBe(100);
    });

    it("costTrend: moderate increase (+35%) scores ~85", () => {
      const runs = [createMockRun()];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
        getCostTrend: vi.fn().mockReturnValue({
          improving: false,
          percentChange: 35,
          hasEnoughData: true,
        }),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const components = service.computeHealthComponents();
      const cost = components.find((c) => c.name === "costTrend");

      // Gaussian: 100 * exp(-((35-15)/50)^2) ≈ 85
      expect(cost?.score).toBeGreaterThanOrEqual(83);
      expect(cost?.score).toBeLessThanOrEqual(87);
    });

    it("costTrend: large increase (+65%) scores ~37", () => {
      const runs = [createMockRun()];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
        getCostTrend: vi.fn().mockReturnValue({
          improving: false,
          percentChange: 65,
          hasEnoughData: true,
        }),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const components = service.computeHealthComponents();
      const cost = components.find((c) => c.name === "costTrend");

      // Gaussian: 100 * exp(-((65-15)/50)^2) ≈ 37
      expect(cost?.score).toBeGreaterThanOrEqual(35);
      expect(cost?.score).toBeLessThanOrEqual(39);
    });
  });
});
