/**
 * HealthWidget.cacheFilter.test.ts
 *
 * Tests for zero-token run filtering in cache hit rate calculations (#989).
 *
 * Verifies that computeHealthComponents(), getSparklines(), and recordSnapshot()
 * exclude zero-token runs from cache hit rate averages.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock SDK modules (throw to prevent actual SDK usage)
vi.mock("@nightgauge/sdk/dist/analysis/FailurePatternDetector", () => ({
  FailurePatternDetector: class {
    analyze() {
      return { findings: [] };
    }
  },
}));

vi.mock("@nightgauge/sdk/dist/analysis/TokenEfficiencyAnalyzer", () => ({
  TokenEfficiencyAnalyzer: class {
    analyze() {
      return { wastePatterns: [] };
    }
  },
}));

vi.mock("vscode", () => ({
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn((_key: string, defaultValue: unknown) => defaultValue),
    })),
  },
}));

const appendSnapshotSpy = vi.fn();
vi.mock("../../../src/utils/healthScoreHistory", () => ({
  HealthScoreHistoryWriter: {
    appendSnapshot: (...args: unknown[]) => appendSnapshotSpy(...args),
  },
  HealthScoreHistoryReader: {
    readDateRange: vi.fn().mockResolvedValue([]),
    aggregateByDay: vi.fn().mockReturnValue([]),
    analyzeTrend: vi.fn().mockReturnValue(null),
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

const createZeroTokenRun = (issueNumber: number) =>
  createMockRun({
    issueNumber,
    usage: {
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0,
      durationMs: 0,
      stageCount: 0,
    },
    efficiency: {
      tokensPerMinute: 0,
      costPerMinute: 0,
      cacheHitRate: 0,
      avgStageDurationMs: 0,
    },
  });

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("HealthWidgetService — cache hit rate zero-token filtering (#989)", () => {
  beforeEach(() => {
    appendSnapshotSpy.mockClear();
  });

  describe("computeHealthComponents()", () => {
    it("should exclude zero-token runs from cache hit rate average", () => {
      // 3 runs with 50% cache hit rate + 2 zero-token runs
      const runs = [
        createMockRun({
          issueNumber: 1,
          efficiency: {
            cacheHitRate: 0.5,
            tokensPerMinute: 100,
            costPerMinute: 0.001,
            avgStageDurationMs: 10000,
          },
        }),
        createMockRun({
          issueNumber: 2,
          efficiency: {
            cacheHitRate: 0.5,
            tokensPerMinute: 100,
            costPerMinute: 0.001,
            avgStageDurationMs: 10000,
          },
        }),
        createMockRun({
          issueNumber: 3,
          efficiency: {
            cacheHitRate: 0.5,
            tokensPerMinute: 100,
            costPerMinute: 0.001,
            avgStageDurationMs: 10000,
          },
        }),
        createZeroTokenRun(4),
        createZeroTokenRun(5),
      ];

      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
      });
      const service = new HealthWidgetService(state as any, "/workspace");
      const components = service.computeHealthComponents();

      const cacheComponent = components.find((c) => c.name === "cacheHitRate");
      expect(cacheComponent).toBeDefined();
      // Average should be 50 (from 3 valid runs at 0.5), not 30 (from 5 runs)
      expect(cacheComponent!.score).toBe(50);
    });

    it("reflects token-bearing runs even when the most recent 5 are zero-token (filter-then-slice)", () => {
      // Regression: the dashboard sliced the recent 5 FIRST then filtered, so a
      // burst of recent zero-token records (early-failed / no-op runs) wiped the
      // window to empty → Cache Hit Rate showed 0 even though real runs cache at
      // ~99%. Filtering before slicing must surface the recent token-bearing runs.
      const runs = [
        createZeroTokenRun(10),
        createZeroTokenRun(11),
        createZeroTokenRun(12),
        createZeroTokenRun(13),
        createZeroTokenRun(14),
        createMockRun({
          issueNumber: 15,
          efficiency: {
            cacheHitRate: 0.99,
            tokensPerMinute: 100,
            costPerMinute: 0.001,
            avgStageDurationMs: 10000,
          },
        }),
        createMockRun({
          issueNumber: 16,
          efficiency: {
            cacheHitRate: 0.99,
            tokensPerMinute: 100,
            costPerMinute: 0.001,
            avgStageDurationMs: 10000,
          },
        }),
      ];

      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
      });
      const service = new HealthWidgetService(state as any, "/workspace");
      const components = service.computeHealthComponents();

      const cacheComponent = components.find((c) => c.name === "cacheHitRate");
      // Old (slice-then-filter): 0. New (filter-then-slice): ~99.
      expect(cacheComponent!.score).toBe(99);
    });

    it("should return score 0 when all runs are zero-token", () => {
      const runs = [createZeroTokenRun(1), createZeroTokenRun(2), createZeroTokenRun(3)];

      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
      });
      const service = new HealthWidgetService(state as any, "/workspace");
      const components = service.computeHealthComponents();

      const cacheComponent = components.find((c) => c.name === "cacheHitRate");
      expect(cacheComponent!.score).toBe(0);
    });

    it("should produce correct average with mixed cache rates", () => {
      // Runs with 50% and 80% + one zero-token → avg should be (50+80)/2 = 65%
      const runs = [
        createMockRun({
          issueNumber: 1,
          efficiency: {
            cacheHitRate: 0.5,
            tokensPerMinute: 100,
            costPerMinute: 0.001,
            avgStageDurationMs: 10000,
          },
        }),
        createZeroTokenRun(2),
        createMockRun({
          issueNumber: 3,
          efficiency: {
            cacheHitRate: 0.8,
            tokensPerMinute: 100,
            costPerMinute: 0.001,
            avgStageDurationMs: 10000,
          },
        }),
      ];

      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
      });
      const service = new HealthWidgetService(state as any, "/workspace");
      const components = service.computeHealthComponents();

      const cacheComponent = components.find((c) => c.name === "cacheHitRate");
      // (0.5 + 0.8) / 2 * 100 = 65
      expect(cacheComponent!.score).toBe(65);
    });
  });

  describe("getSparklines()", () => {
    // Issue #989 wanted zero-token runs to map to 0 in the cache sparkline.
    // The cache sparkline has since been removed from the Overview tab — its
    // information is already shown in the Pipeline Health "Cache Hit Rate"
    // component card directly above the sparkline grid. The zero-token
    // filtering still applies to the *score* computation (above), which is
    // what the user actually sees today.
    it("no longer emits a cache hit rate sparkline (now in the health component card)", () => {
      const runs = [
        createMockRun({
          issueNumber: 1,
          efficiency: {
            cacheHitRate: 0.6,
            tokensPerMinute: 100,
            costPerMinute: 0.001,
            avgStageDurationMs: 10000,
          },
        }),
        createZeroTokenRun(2),
      ];

      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
        getHistoricalData: vi.fn().mockReturnValue([]),
      });
      const service = new HealthWidgetService(state as any, "/workspace");
      const sparklines = service.getSparklines();

      expect(sparklines.find((s) => s.metric === "cacheHitRate")).toBeUndefined();
    });
  });

  describe("recordSnapshot()", () => {
    it("should use filtered cache hit rate in snapshot", async () => {
      const runs = [
        createMockRun({
          issueNumber: 1,
          efficiency: {
            cacheHitRate: 0.7,
            tokensPerMinute: 100,
            costPerMinute: 0.001,
            avgStageDurationMs: 10000,
          },
        }),
        createZeroTokenRun(2),
        createMockRun({
          issueNumber: 3,
          efficiency: {
            cacheHitRate: 0.3,
            tokensPerMinute: 100,
            costPerMinute: 0.001,
            avgStageDurationMs: 10000,
          },
        }),
      ];

      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      await service.recordSnapshot(1, 0.05);

      expect(appendSnapshotSpy).toHaveBeenCalledTimes(1);
      const snapshot = appendSnapshotSpy.mock.calls[0][1];
      // (0.7 + 0.3) / 2 = 0.5, not (0.7 + 0 + 0.3) / 3 = 0.333
      expect(snapshot.cacheHitRate).toBeCloseTo(0.5, 5);
    });

    it("should write cacheHitRate 0 when all runs are zero-token", async () => {
      const runs = [createZeroTokenRun(1), createZeroTokenRun(2)];

      const state = createMockDashboardState({
        getHistory: vi.fn().mockReturnValue(runs),
      });
      const service = new HealthWidgetService(state as any, "/workspace");

      await service.recordSnapshot(1, 0);

      expect(appendSnapshotSpy).toHaveBeenCalledTimes(1);
      const snapshot = appendSnapshotSpy.mock.calls[0][1];
      expect(snapshot.cacheHitRate).toBe(0);
    });
  });
});
