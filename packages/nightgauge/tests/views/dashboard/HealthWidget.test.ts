/**
 * HealthWidget.test.ts
 *
 * Unit tests for HealthWidgetService class covering:
 * - Empty history returns isEmpty=true
 * - Health score with default weights returns 0-100
 * - Health score with all-perfect metrics yields a high score
 * - computeWeightedScore normalizes weights correctly
 * - Custom weights are applied correctly
 * - Sparkline data has correct structure
 * - Graceful degradation when SDK analysis throws
 * - Components have correct names
 *
 * @see Issue #655 - Pipeline Health Dashboard Widget
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock SDK analysis modules to simulate unavailability
vi.mock("@nightgauge/sdk/dist/analysis/FailurePatternDetector", () => {
  throw new Error("SDK not available in test");
});
vi.mock("@nightgauge/sdk/dist/analysis/TokenEfficiencyAnalyzer", () => {
  throw new Error("SDK not available in test");
});

// Mock vscode workspace configuration
vi.mock("vscode", () => ({
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn((_key: string, defaultValue: unknown) => defaultValue),
    })),
  },
}));

import { HealthWidgetService } from "../../../src/views/dashboard/HealthWidget";

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

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("HealthWidgetService", () => {
  describe("getData()", () => {
    it("returns isEmpty=true when history is empty", async () => {
      // Arrange
      const state = createMockDashboardState();
      const service = new HealthWidgetService(state as any, "/workspace");

      // Act
      const data = await service.getData();

      // Assert
      expect(data.isEmpty).toBe(true);
      expect(data.summary.score).toBe(0);
      expect(data.summary.components).toHaveLength(0);
      expect(data.sparklines).toHaveLength(0);
      expect(data.alerts).toHaveLength(0);
      expect(data.recommendations).toHaveLength(0);
    });

    it("returns isEmpty=false with valid data when history has runs", async () => {
      // Arrange
      const runs = [createMockRun(), createMockRun({ issueNumber: 2 })];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      // Act
      const data = await service.getData();

      // Assert
      expect(data.isEmpty).toBe(false);
      expect(data.summary.components.length).toBeGreaterThan(0);
      expect(data.lastUpdated).toBeDefined();
    });

    it("returns health score in valid 0-100 range with default weights", async () => {
      // Arrange
      const runs = [createMockRun(), createMockRun()];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      // Act
      const data = await service.getData();

      // Assert
      expect(data.summary.score).toBeGreaterThanOrEqual(0);
      expect(data.summary.score).toBeLessThanOrEqual(100);
    });

    it("produces a high score when all metrics are perfect", async () => {
      // Arrange — all-perfect: 100% success, improving efficiency, low cost, high cache
      const runs = Array.from({ length: 5 }, (_, i) =>
        createMockRun({
          issueNumber: i + 1,
          status: "complete",
          efficiency: {
            tokensPerMinute: 100,
            costPerMinute: 0.001,
            cacheHitRate: 1.0,
            avgStageDurationMs: 5000,
          },
        })
      );
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
        getAggregates: vi.fn().mockReturnValue({ successRate: 1.0, totalRuns: 5 }),
        getEfficiencyTrend: vi.fn().mockReturnValue({
          improving: true,
          percentChange: 20,
          hasEnoughData: true,
        }),
        getCostTrend: vi.fn().mockReturnValue({
          improving: true,
          percentChange: -10,
          hasEnoughData: true,
        }),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      // Act
      const data = await service.getData();

      // Assert — with perfect inputs, composite score should be high
      expect(data.summary.score).toBeGreaterThanOrEqual(70);
    });

    it("does not include prediction accuracy as a health component", async () => {
      // Prediction accuracy was removed as a health component in the simplification
      const runs = [createMockRun(), createMockRun({ issueNumber: 2 })];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      // Act
      const data = await service.getData();

      // Assert — predictionAccuracy is no longer a health component
      const predComp = data.summary.components.find((c) => c.name === "predictionAccuracy");
      expect(predComp).toBeUndefined();
    });
  });

  describe("computeWeightedScore()", () => {
    it("normalizes weights and returns a value between 0 and 100", () => {
      // Arrange — weights that do NOT sum to 1.0
      const state = createMockDashboardState();
      const service = new HealthWidgetService(state as any, "/workspace");
      const components = [
        {
          name: "a",
          score: 80,
          weight: 5,
          trend: "improving" as const,
          label: "A",
        },
        {
          name: "b",
          score: 60,
          weight: 5,
          trend: "stable" as const,
          label: "B",
        },
      ];

      // Act
      const score = service.computeWeightedScore(components);

      // Assert
      expect(score).toBeGreaterThanOrEqual(0);
      expect(score).toBeLessThanOrEqual(100);
      // Weighted average of 80 and 60 with equal weights = 70
      expect(score).toBe(70);
    });

    it("returns 0 for empty components array", () => {
      // Arrange
      const state = createMockDashboardState();
      const service = new HealthWidgetService(state as any, "/workspace");

      // Act
      const score = service.computeWeightedScore([]);

      // Assert
      expect(score).toBe(0);
    });

    it("applies different weights to produce different scores", () => {
      // Arrange
      const state = createMockDashboardState();
      const service = new HealthWidgetService(state as any, "/workspace");

      const componentsHighWeightOnGood = [
        {
          name: "a",
          score: 100,
          weight: 9,
          trend: "improving" as const,
          label: "A",
        },
        {
          name: "b",
          score: 0,
          weight: 1,
          trend: "degrading" as const,
          label: "B",
        },
      ];

      const componentsHighWeightOnBad = [
        {
          name: "a",
          score: 100,
          weight: 1,
          trend: "improving" as const,
          label: "A",
        },
        {
          name: "b",
          score: 0,
          weight: 9,
          trend: "degrading" as const,
          label: "B",
        },
      ];

      // Act
      const scoreHigh = service.computeWeightedScore(componentsHighWeightOnGood);
      const scoreLow = service.computeWeightedScore(componentsHighWeightOnBad);

      // Assert — weighting towards good metric gives higher score
      expect(scoreHigh).toBe(90);
      expect(scoreLow).toBe(10);
      expect(scoreHigh).toBeGreaterThan(scoreLow);
    });
  });

  describe("computeHealthComponents()", () => {
    it("returns all 4 expected components with correct names", () => {
      // Arrange
      const runs = [createMockRun()];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      // Act
      const components = service.computeHealthComponents();

      // Assert
      expect(components).toHaveLength(4);
      const names = components.map((c) => c.name);
      expect(names).toContain("successRate");
      expect(names).toContain("costTrend");
      expect(names).toContain("failureRate");
      expect(names).toContain("cacheHitRate");
    });

    it("each component has a score between 0 and 100", () => {
      // Arrange
      const runs = [createMockRun()];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      // Act
      const components = service.computeHealthComponents();

      // Assert
      for (const component of components) {
        expect(component.score).toBeGreaterThanOrEqual(0);
        expect(component.score).toBeLessThanOrEqual(100);
        expect(component.weight).toBeGreaterThanOrEqual(0);
        expect(component.weight).toBeLessThanOrEqual(1);
      }
    });
  });

  describe("getSparklines()", () => {
    it("returns sparklines with correct structure when data exists", () => {
      // Arrange
      const runs = [createMockRun(), createMockRun()];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
        getHistoricalData: vi.fn().mockReturnValue([10, 20, 30]),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      // Act
      const sparklines = service.getSparklines();

      // Assert — at minimum cache and success sparklines come from history
      expect(sparklines.length).toBeGreaterThanOrEqual(2);
      for (const sparkline of sparklines) {
        expect(sparkline).toHaveProperty("metric");
        expect(sparkline).toHaveProperty("label");
        expect(sparkline).toHaveProperty("data");
        expect(sparkline).toHaveProperty("trend");
        expect(sparkline).toHaveProperty("unit");
        expect(Array.isArray(sparkline.data)).toBe(true);
        expect(["up", "down", "stable"]).toContain(sparkline.trend);
        expect(typeof sparkline.label).toBe("string");
      }
    });

    it("returns empty array when no history and no historical data", () => {
      // Arrange
      const state = createMockDashboardState();
      const service = new HealthWidgetService(state as any, "/workspace");

      // Act
      const sparklines = service.getSparklines();

      // Assert
      expect(sparklines).toHaveLength(0);
    });

    // -----------------------------------------------------------------------
    // Polarity regression tests for the Overview "Cost per Run" bug.
    //
    // Before the fix, a falling cost was labelled trend="down" and rendered
    // in red; a rising cost was labelled trend="up" and rendered in green.
    // These tests pin the producer-side contract: the arrow tracks the data
    // direction and polarity tells the renderer how to color it.
    // -----------------------------------------------------------------------

    it("cost sparkline carries lower-is-better polarity and arrow tracks the data", () => {
      const runs = [createMockRun()];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
        getHistoricalData: vi.fn().mockReturnValue([1, 2, 3]),
        getCostTrend: vi
          .fn()
          .mockReturnValue({ improving: false, percentChange: 25.0, hasEnoughData: true }),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const cost = service.getSparklines().find((s) => s.metric === "cost");
      expect(cost).toBeDefined();
      expect(cost!.polarity).toBe("lower-is-better");
      // Cost rose 25% → arrow points up (the bad direction). Color comes from
      // (up, lower-is-better) → degrading at the renderer layer.
      expect(cost!.trend).toBe("up");
    });

    it("cost sparkline arrow points down when cost is falling", () => {
      const runs = [createMockRun()];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
        getHistoricalData: vi.fn().mockReturnValue([3, 2, 1]),
        getCostTrend: vi
          .fn()
          .mockReturnValue({ improving: true, percentChange: -25.0, hasEnoughData: true }),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const cost = service.getSparklines().find((s) => s.metric === "cost");
      expect(cost!.trend).toBe("down");
      expect(cost!.polarity).toBe("lower-is-better");
    });

    it("cost sparkline is stable when percentChange is within ±5%", () => {
      const runs = [createMockRun()];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
        getHistoricalData: vi.fn().mockReturnValue([1, 1, 1]),
        getCostTrend: vi
          .fn()
          .mockReturnValue({ improving: false, percentChange: 2.0, hasEnoughData: true }),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const cost = service.getSparklines().find((s) => s.metric === "cost");
      expect(cost!.trend).toBe("stable");
    });

    it("token sparkline carries lower-is-better polarity", () => {
      const runs = [createMockRun()];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
        getHistoricalData: vi.fn().mockReturnValue([100, 200, 300]),
        getTokenTrend: vi
          .fn()
          .mockReturnValue({ direction: "up", percentChange: 15.0, hasEnoughData: true }),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const tokens = service.getSparklines().find((s) => s.metric === "tokens");
      expect(tokens!.polarity).toBe("lower-is-better");
      expect(tokens!.trend).toBe("up");
    });

    // Cache hit rate and success rate sparklines were removed from the
    // Overview "Recent Activity" section to eliminate visual duplication
    // with the Pipeline Health component cards directly above. The values
    // these used to expose live on as composite scores in the health cards.
    it("no longer emits cacheHitRate or successRate sparklines on Overview", () => {
      const runs = [createMockRun(), createMockRun()];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
        getHistoricalData: vi.fn().mockReturnValue([]),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const sparklines = service.getSparklines();
      expect(sparklines.find((s) => s.metric === "cacheHitRate")).toBeUndefined();
      expect(sparklines.find((s) => s.metric === "successRate")).toBeUndefined();
    });

    it("only emits Cost per Run and Tokens per Run on the Recent Activity row", () => {
      const runs = [createMockRun()];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
        getHistoricalData: vi.fn().mockReturnValue([1, 2, 3]),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const sparklines = service.getSparklines();
      const metrics = sparklines.map((s) => s.metric).sort();
      expect(metrics).toEqual(["cost", "tokens"]);
    });
  });

  describe("graceful degradation", () => {
    it("getActiveAlerts returns empty array when SDK is unavailable", async () => {
      // Arrange
      const runs = [createMockRun(), createMockRun(), createMockRun(), createMockRun()];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      // Act
      const alerts = await service.getActiveAlerts();

      // Assert — SDK is mocked to throw, so we get empty array
      expect(alerts).toEqual([]);
    });

    it("getRecommendations returns empty array when SDK is unavailable", async () => {
      // Arrange
      const runs = [createMockRun(), createMockRun(), createMockRun(), createMockRun()];
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      // Act
      const recommendations = await service.getRecommendations();

      // Assert — SDK is mocked to throw, so we get empty array
      expect(recommendations).toEqual([]);
    });
  });

  describe("insufficientData (#991)", () => {
    it("sets insufficientData=true on cost component with < 6 runs", () => {
      const runs = Array.from({ length: 4 }, (_, i) => createMockRun({ issueNumber: i + 1 }));
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
        getCostTrend: vi.fn().mockReturnValue({
          improving: true,
          percentChange: 0,
          hasEnoughData: false,
        }),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const components = service.computeHealthComponents();

      const cost = components.find((c) => c.name === "costTrend");
      expect(cost?.insufficientData).toBe(true);
      expect(cost?.insufficientDataMessage).toBe("Need 2 more runs for trend data");
    });

    it("sets insufficientData=false on cost component with >= 6 runs", () => {
      const runs = Array.from({ length: 10 }, (_, i) => createMockRun({ issueNumber: i + 1 }));
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
        getCostTrend: vi.fn().mockReturnValue({
          improving: true,
          percentChange: -3,
          hasEnoughData: true,
        }),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const components = service.computeHealthComponents();

      const cost = components.find((c) => c.name === "costTrend");
      expect(cost?.insufficientData).toBe(false);
      expect(cost?.insufficientDataMessage).toBeUndefined();
    });

    it("defaults cost score to 100 when insufficient data (assume healthy)", () => {
      const runs = Array.from({ length: 2 }, (_, i) => createMockRun({ issueNumber: i + 1 }));
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
        getCostTrend: vi.fn().mockReturnValue({
          improving: true,
          percentChange: 0,
          hasEnoughData: false,
        }),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const components = service.computeHealthComponents();

      const cost = components.find((c) => c.name === "costTrend");
      expect(cost?.score).toBe(100);
    });

    it("clamps runsNeeded to at least 1", () => {
      // 5 runs: 6-5=1, should say "Need 1 more run" (singular)
      const runs = Array.from({ length: 5 }, (_, i) => createMockRun({ issueNumber: i + 1 }));
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
        getCostTrend: vi.fn().mockReturnValue({
          improving: true,
          percentChange: 0,
          hasEnoughData: false,
        }),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const components = service.computeHealthComponents();
      const cost = components.find((c) => c.name === "costTrend");
      expect(cost?.insufficientDataMessage).toBe("Need 1 more run for trend data");
    });
  });
});
