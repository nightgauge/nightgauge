/**
 * HealthWidget.filter.test.ts
 *
 * Tests for zero-token stage filtering in HealthWidgetService (#986).
 *
 * Verifies that stages without tokenUsage are excluded from records
 * passed to SDK analyzers in getActiveAlerts() and getRecommendations().
 *
 * Uses working SDK mocks (unlike HealthWidget.test.ts which tests graceful
 * degradation with throwing SDK mocks).
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Track records passed to SDK analyzers
const analyzeRecordsSpy = vi.fn();
const analyzeTokenRecordsSpy = vi.fn();

// Mock SDK modules with working implementations
vi.mock("@nightgauge/sdk/dist/analysis/FailurePatternDetector", () => ({
  FailurePatternDetector: class {
    analyze(records: unknown[]) {
      analyzeRecordsSpy(records);
      return { findings: [] };
    }
  },
}));

vi.mock("@nightgauge/sdk/dist/analysis/TokenEfficiencyAnalyzer", () => ({
  TokenEfficiencyAnalyzer: class {
    analyze(records: unknown[]) {
      analyzeTokenRecordsSpy(records);
      return { wastePatterns: [] };
    }
  },
}));

// Mock vscode
vi.mock("vscode", () => ({
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn((_key: string, defaultValue: unknown) => defaultValue),
    })),
  },
}));

import { HealthWidgetService } from "../../../src/views/dashboard/HealthWidget";

// ---------------------------------------------------------------------------
// Helpers
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

describe("HealthWidgetService — zero-token filtering (#986)", () => {
  beforeEach(() => {
    analyzeRecordsSpy.mockClear();
    analyzeTokenRecordsSpy.mockClear();
  });

  describe("getActiveAlerts()", () => {
    it("should exclude stages without tokenUsage from failure analysis records", async () => {
      const runs = Array.from({ length: 4 }, (_, i) =>
        createMockRun({
          issueNumber: i + 1,
          stages: [
            {
              stage: "issue-pickup",
              status: "complete",
              completedAt: new Date(),
              durationMs: 5000,
              tokenUsage: {
                stage: "issue-pickup",
                inputTokens: 1000,
                outputTokens: 500,
                cacheReadTokens: 0,
                cacheCreationTokens: 0,
                costUsd: 0.01,
                timestamp: new Date(),
              },
            },
            {
              stage: "feature-planning",
              status: "complete",
              completedAt: new Date(),
              durationMs: 3000,
              // No tokenUsage — should be filtered out
            },
            {
              stage: "feature-dev",
              status: "complete",
              completedAt: new Date(),
              durationMs: 10000,
              tokenUsage: {
                stage: "feature-dev",
                inputTokens: 5000,
                outputTokens: 2000,
                cacheReadTokens: 100,
                cacheCreationTokens: 50,
                costUsd: 0.05,
                timestamp: new Date(),
              },
            },
          ],
        })
      );

      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      await service.getActiveAlerts();

      // Verify analyzer received only stages WITH tokenUsage
      expect(analyzeRecordsSpy).toHaveBeenCalledTimes(1);
      const records = analyzeRecordsSpy.mock.calls[0][0] as Array<{
        stage: string;
        inputTokens: number;
      }>;

      // 4 runs × 2 stages with tokenUsage = 8 records (not 12)
      expect(records).toHaveLength(8);

      // No feature-planning records (they had no tokenUsage)
      const planningRecords = records.filter((r) => r.stage === "feature-planning");
      expect(planningRecords).toHaveLength(0);

      // All records have non-zero token data
      for (const record of records) {
        expect(record.inputTokens).toBeGreaterThan(0);
      }
    });

    it("should return empty alerts when no stages have token data", async () => {
      const runs = Array.from({ length: 4 }, (_, i) =>
        createMockRun({
          issueNumber: i + 1,
          stages: [
            {
              stage: "issue-pickup",
              status: "complete",
              completedAt: new Date(),
              durationMs: 5000,
              // No tokenUsage
            },
          ],
        })
      );

      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const alerts = await service.getActiveAlerts();

      // Analyzer should still be called with empty records
      expect(analyzeRecordsSpy).toHaveBeenCalledTimes(1);
      expect(analyzeRecordsSpy.mock.calls[0][0]).toHaveLength(0);
      expect(alerts).toEqual([]);
    });
  });

  describe("getRecommendations()", () => {
    it("should exclude stages without tokenUsage from efficiency analysis records", async () => {
      const runs = Array.from({ length: 4 }, (_, i) =>
        createMockRun({
          issueNumber: i + 1,
          stages: [
            {
              stage: "issue-pickup",
              status: "complete",
              completedAt: new Date(),
              durationMs: 5000,
              tokenUsage: {
                stage: "issue-pickup",
                inputTokens: 1000,
                outputTokens: 500,
                cacheReadTokens: 0,
                cacheCreationTokens: 0,
                costUsd: 0.01,
                timestamp: new Date(),
              },
            },
            {
              stage: "feature-planning",
              status: "complete",
              completedAt: new Date(),
              durationMs: 3000,
              // No tokenUsage — should be filtered out
            },
          ],
        })
      );

      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      await service.getRecommendations();

      // Verify analyzer received only stages WITH tokenUsage
      expect(analyzeTokenRecordsSpy).toHaveBeenCalledTimes(1);
      const records = analyzeTokenRecordsSpy.mock.calls[0][0] as Array<{
        stage: string;
      }>;

      // 4 runs × 1 stage with tokenUsage = 4 records (not 8)
      expect(records).toHaveLength(4);

      // No feature-planning records
      const planningRecords = records.filter((r) => r.stage === "feature-planning");
      expect(planningRecords).toHaveLength(0);
    });

    it("should preserve stages with tokenUsage that has zero values", async () => {
      // Zero inputTokens but tokenUsage object IS present (e.g., cached stage)
      const runs = Array.from({ length: 4 }, (_, i) =>
        createMockRun({
          issueNumber: i + 1,
          stages: [
            {
              stage: "issue-pickup",
              status: "complete",
              completedAt: new Date(),
              durationMs: 5000,
              tokenUsage: {
                stage: "issue-pickup",
                inputTokens: 0,
                outputTokens: 0,
                cacheReadTokens: 100,
                cacheCreationTokens: 0,
                costUsd: 0,
                timestamp: new Date(),
              },
            },
          ],
        })
      );

      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      await service.getRecommendations();

      // Stages with tokenUsage present (even zeros) should be kept
      expect(analyzeTokenRecordsSpy).toHaveBeenCalledTimes(1);
      const records = analyzeTokenRecordsSpy.mock.calls[0][0] as Array<{
        stage: string;
        inputTokens: number;
      }>;
      expect(records).toHaveLength(4);
      expect(records[0].inputTokens).toBe(0); // Zero is valid data
    });

    it("should return empty recommendations when no stages have token data", async () => {
      const runs = Array.from({ length: 4 }, (_, i) =>
        createMockRun({
          issueNumber: i + 1,
          stages: [
            {
              stage: "feature-dev",
              status: "complete",
              completedAt: new Date(),
              durationMs: 10000,
              // No tokenUsage
            },
          ],
        })
      );

      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      const recommendations = await service.getRecommendations();

      expect(analyzeTokenRecordsSpy).toHaveBeenCalledTimes(1);
      expect(analyzeTokenRecordsSpy.mock.calls[0][0]).toHaveLength(0);
      expect(recommendations).toEqual([]);
    });
  });
});
