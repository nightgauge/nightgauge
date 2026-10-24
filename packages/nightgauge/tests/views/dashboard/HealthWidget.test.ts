/**
 * HealthWidget.test.ts
 *
 * Unit tests for HealthWidgetService class covering:
 * - Empty history returns isEmpty=true with no score (N/A, not 0)
 * - The headline is the latest health-series entry, verbatim (#1898)
 * - A null latest entry reads as no-data even when older entries scored
 * - Sparkline data has correct structure
 * - Graceful degradation when SDK analysis throws
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

const readHealthSeries = vi.fn();
vi.mock("../../../src/utils/healthSeries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../src/utils/healthSeries")>()),
  readHealthSeries: (...args: unknown[]) => readHealthSeries(...args),
}));

import type { HealthTrendEntry } from "@nightgauge/sdk";
import { HealthWidgetService } from "../../../src/views/dashboard/HealthWidget";

function seriesEntry(overrides: Partial<HealthTrendEntry> = {}): HealthTrendEntry {
  return {
    schema_version: "2",
    timestamp: new Date().toISOString(),
    run_id: "r",
    issue_number: 1,
    overall_score: 83,
    status: "good",
    dimensions: { reliability: 92, "cost-health": 75 },
    excluded_dimensions: { "skill-drift": "no skill-effectiveness records" },
    significant_findings: [],
    ...overrides,
  };
}

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
  beforeEach(() => {
    readHealthSeries.mockReset();
    readHealthSeries.mockResolvedValue([]);
  });

  describe("getData()", () => {
    it("returns isEmpty=true with no score — not 0, not critical — when history is empty", async () => {
      const state = createMockDashboardState();
      const service = new HealthWidgetService(state as any, "/workspace");

      const data = await service.getData();

      expect(data.isEmpty).toBe(true);
      expect(data.summary.score).toBeNull();
      expect(data.summary.status).toBe("no-data");
      expect(data.sparklines).toHaveLength(0);
      expect(data.alerts).toHaveLength(0);
      expect(data.recommendations).toHaveLength(0);
    });

    it("reads no-data when runs exist but no health entry has been recorded", async () => {
      const runs = [createMockRun(), createMockRun({ issueNumber: 2 })];
      const state = createMockDashboardState({ getHistory: vi.fn().mockReturnValue(runs) });
      const service = new HealthWidgetService(state as any, "/workspace");

      const data = await service.getData();

      expect(data.isEmpty).toBe(false);
      expect(data.summary.score).toBeNull();
      expect(data.summary.status).toBe("no-data");
      expect(data.summary.dimensions).toHaveLength(8);
      expect(data.summary.contributing).toBe(0);
      expect(data.lastUpdated).toBeDefined();
    });

    it("shows the latest series entry's score verbatim — the number persisted (#1898)", async () => {
      readHealthSeries.mockResolvedValue([
        seriesEntry({ overall_score: 40, status: "poor", run_id: "older" }),
        seriesEntry({ overall_score: 83, status: "good", run_id: "latest", issue_number: 7 }),
      ]);
      const runs = [createMockRun()];
      const state = createMockDashboardState({ getHistory: vi.fn().mockReturnValue(runs) });
      const service = new HealthWidgetService(state as any, "/workspace");

      const data = await service.getData();

      expect(readHealthSeries).toHaveBeenCalledWith("/workspace");
      expect(data.summary.score).toBe(83);
      expect(data.summary.status).toBe("good");
      expect(data.summary.issueNumber).toBe(7);
      expect(data.summary.contributing).toBe(2);
      const drift = data.summary.dimensions.find((d) => d.dimension === "skill-drift");
      expect(drift).toMatchObject({
        score: null,
        excludedReason: "no skill-effectiveness records",
      });
    });

    it("a latest entry recorded as null reads N/A, not the last scored entry", async () => {
      readHealthSeries.mockResolvedValue([
        seriesEntry({ overall_score: 90, status: "excellent" }),
        seriesEntry({
          overall_score: null,
          status: "no-data",
          dimensions: {},
          excluded_dimensions: { reliability: "no runs" },
        }),
      ]);
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue([createMockRun()]),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const data = await service.getData();

      expect(data.summary.score).toBeNull();
      expect(data.summary.status).toBe("no-data");
      expect(data.summary.recordedAt).toBeDefined();
    });

    it("does not read a series when there is no workspace path", async () => {
      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue([createMockRun()]),
      });
      const service = new HealthWidgetService(state as any, undefined);

      const data = await service.getData();

      expect(readHealthSeries).not.toHaveBeenCalled();
      expect(data.summary.score).toBeNull();
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
});
