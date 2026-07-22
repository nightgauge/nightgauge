/**
 * HealthWidget.dimensionSparklines.test.ts
 *
 * Tests that getData() loads and includes per-dimension sparklines from
 * HealthTrendsWriter when trends.jsonl data is available.
 *
 * @see Issue #1411 - Health trend persistence and dashboard sparklines
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock SDK analysis modules (not available in test env)
vi.mock("@nightgauge/sdk/dist/analysis/FailurePatternDetector", () => {
  throw new Error("SDK not available in test");
});
vi.mock("@nightgauge/sdk/dist/analysis/TokenEfficiencyAnalyzer", () => {
  throw new Error("SDK not available in test");
});

// Mock vscode
vi.mock("vscode", () => ({
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn((_key: string, defaultValue: unknown) => defaultValue),
    })),
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
}));

// Mock mergeWithDefaults and config schema
vi.mock("../../../src/config/schema", () => ({
  mergeWithDefaults: vi.fn((c: unknown) => c ?? {}),
  DEFAULT_CONFIG: {},
  DEFAULT_HEALTH_WEIGHTS: {
    tokenEfficiencyTrend: 0.18,
    successRate: 0.22,
    costTrend: 0.18,
    cacheHitRate: 0.13,
    predictionAccuracy: 0.09,
    failureRate: 0.1,
    contextBudgetUtilization: 0.1,
  },
}));

// Mock IncrediYamlService
vi.mock("../../../src/views/settings/IncrediYamlService", () => ({
  IncrediYamlService: vi.fn(function () {
    return {
      read: vi.fn().mockResolvedValue({ success: false }),
      dispose: vi.fn(),
    };
  }),
}));

// Mock SelfTuningLogger
vi.mock("../../../src/services/SelfTuningLogger", () => ({
  SelfTuningLogger: { readAll: vi.fn().mockResolvedValue([]) },
}));

// Mock healthScoreHistory to avoid filesystem access
vi.mock("../../../src/utils/healthScoreHistory", () => ({
  HealthScoreHistoryWriter: { appendSnapshot: vi.fn() },
  HealthScoreHistoryReader: {
    readDateRange: vi.fn().mockResolvedValue([]),
    aggregateByDay: vi.fn().mockReturnValue([]),
    analyzeTrend: vi.fn().mockReturnValue(null),
    getMostRecentRecalibration: vi.fn().mockResolvedValue(null),
  },
}));

// Mock batchMetricsAttribution
vi.mock("../../../src/utils/batchMetricsAttribution", () => ({
  deduplicateBatchTokens: vi.fn((runs: unknown[]) => runs),
}));

// ── HealthTrendsWriter mock ──────────────────────────────────────────────────
const mockTrendsRead = vi.fn();

vi.mock("@nightgauge/sdk/dist/analysis/health/HealthTrendsWriter", () => ({
  HealthTrendsWriter: {
    read: (...args: unknown[]) => mockTrendsRead(...args),
  },
}));

import { HealthWidgetService } from "../../../src/views/dashboard/HealthWidget";

// ── Test helpers ─────────────────────────────────────────────────────────────

function makeTrendEntry(overrides: Record<string, unknown> = {}) {
  return {
    schema_version: "1",
    timestamp: "2026-01-15T10:00:00Z",
    run_id: "2026-01-15T10:00:00Z",
    issue_number: 100,
    overall_score: 75,
    dimensions: {
      "token-economics": 80,
      "cost-health": 70,
      "stage-effectiveness": 78,
      "model-routing": 65,
      reliability: 85,
      "learning-effectiveness": 60,
      "pipeline-velocity": 72,
    },
    significant_findings: [],
    ...overrides,
  };
}

const createMockRun = () => ({
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
});

const createMockState = () => ({
  getHistory: vi.fn().mockReturnValue([createMockRun()]),
  getEfficiencyTrend: vi.fn().mockReturnValue({
    improving: false,
    percentChange: 0,
    hasEnoughData: false,
  }),
  getCostTrend: vi.fn().mockReturnValue({
    improving: false,
    percentChange: 0,
    hasEnoughData: false,
  }),
  getTokenTrend: vi.fn().mockReturnValue({
    direction: "stable",
    percentChange: 0,
    hasEnoughData: false,
  }),
  getAggregates: vi.fn().mockReturnValue({ successRate: 1.0, totalRuns: 5 }),
  getHistoricalData: vi.fn().mockReturnValue([]),
  getVelocityInsights: vi.fn().mockResolvedValue(null),
  getAccuracyTrend: vi.fn().mockResolvedValue(null),
  getPredictionAccuracyFromModel: vi.fn().mockResolvedValue(null),
});

// ── Tests ────────────────────────────────────────────────────────────────────

describe("HealthWidgetService.getData() — dimension sparklines", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("includes dimensionSparklines when HealthTrendsWriter returns ≥2 entries with dimension data", async () => {
    // Provide 5 entries so buildDimensionSparklines can compute trend
    const entries = Array.from({ length: 5 }, (_, i) =>
      makeTrendEntry({
        timestamp: `2026-01-${String(i + 1).padStart(2, "0")}T10:00:00Z`,
        overall_score: 70 + i,
      })
    );
    mockTrendsRead.mockResolvedValueOnce(entries);

    const service = new HealthWidgetService(createMockState() as never, "/workspace");
    const data = await service.getData();

    expect(data.dimensionSparklines).toBeDefined();
    expect(data.dimensionSparklines!.length).toBeGreaterThan(0);
    // Each sparkline has the expected shape
    const first = data.dimensionSparklines![0];
    expect(first).toMatchObject({
      dimension: expect.any(String),
      label: expect.any(String),
      data: expect.arrayContaining([expect.any(Number)]),
      trend: expect.stringMatching(/^(improving|stable|declining)$/),
    });
  });

  it("returns no dimensionSparklines when HealthTrendsWriter returns empty array", async () => {
    mockTrendsRead.mockResolvedValueOnce([]);

    const service = new HealthWidgetService(createMockState() as never, "/workspace");
    const data = await service.getData();

    expect(data.dimensionSparklines).toBeUndefined();
  });

  it("does not throw when HealthTrendsWriter.read rejects", async () => {
    mockTrendsRead.mockRejectedValueOnce(new Error("File not found"));

    const service = new HealthWidgetService(createMockState() as never, "/workspace");
    await expect(service.getData()).resolves.toBeDefined();
    const data = await service.getData();
    expect(data.dimensionSparklines).toBeUndefined();
  });

  it("returns no dimensionSparklines when workspacePath is undefined", async () => {
    const service = new HealthWidgetService(createMockState() as never, undefined);
    const data = await service.getData();

    // HealthTrendsWriter should not be called — no workspace
    expect(mockTrendsRead).not.toHaveBeenCalled();
    expect(data.dimensionSparklines).toBeUndefined();
  });

  it("passes limit: 20 to HealthTrendsWriter.read", async () => {
    mockTrendsRead.mockResolvedValueOnce([]);

    const service = new HealthWidgetService(createMockState() as never, "/workspace");
    await service.getData();

    expect(mockTrendsRead).toHaveBeenCalledWith("/workspace", { limit: 20 });
  });
});
