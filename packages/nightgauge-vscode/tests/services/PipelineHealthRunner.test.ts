/**
 * Tests for PipelineHealthRunner
 *
 * @see Issue #1104 - Pipeline Health VSCode Command & Dashboard Integration
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock node:fs/promises
vi.mock("node:fs/promises", () => ({
  mkdir: vi.fn(),
  writeFile: vi.fn(),
}));

// Mock DataAggregator
vi.mock("../../src/services/DataAggregator", () => ({
  DataAggregator: {
    parseDateFilter: vi.fn(),
    aggregate: vi.fn(),
  },
}));

// Mock SDK analyzers — graceful degradation by default
vi.mock("@nightgauge/sdk/dist/analysis/TokenEfficiencyAnalyzer", () => {
  throw new Error("SDK not available");
});
vi.mock("@nightgauge/sdk/dist/analysis/FailurePatternDetector", () => {
  throw new Error("SDK not available");
});

import * as fs from "node:fs/promises";
import { PipelineHealthRunner } from "../../src/services/PipelineHealthRunner";
import { DataAggregator } from "../../src/services/DataAggregator";
import type { AggregatedDataset } from "../../src/types/aggregation";
import type { HealthCheckParams } from "../../src/types/pipelineHealth";

// --- Test fixtures ---

function buildDataset(overrides?: Partial<AggregatedDataset>): AggregatedDataset {
  return {
    filter: {
      startDate: new Date("2026-01-01"),
      endDate: new Date("2026-02-01"),
    },
    executionHistory: [],
    healthScores: [],
    selfTuningLog: [],
    analysisReports: [],
    experimentResults: [],
    healthReports: [],
    summary: {
      totalRuns: 5,
      successfulRuns: 4,
      failedRuns: 1,
      successRate: 0.8,
      totalCostUsd: 12.5,
      avgCostPerRun: 2.5,
      totalInputTokens: 50000,
      totalOutputTokens: 10000,
      totalCacheReadTokens: 20000,
      totalCacheCreationTokens: 5000,
      avgDurationMs: 120000,
    },
    quality: {
      totalRecords: 10,
      sourcesFound: 3,
      sourcesMissing: 1,
      sources: [],
      dateRangeRequested: { start: "2026-01-01", end: "2026-02-01" },
      dateRangeCovered: { start: "2026-01-05", end: "2026-01-31" },
      gapDays: ["2026-01-15"],
    },
    ...overrides,
  };
}

function buildRunRecord(overrides?: Record<string, unknown>) {
  return {
    record_type: "run",
    issue_number: 100,
    outcome: "complete",
    tokens: {
      total_input: 10000,
      total_output: 2000,
      total_cache_read: 4000,
      total_cache_creation: 1000,
      estimated_cost_usd: 2.5,
    },
    total_duration_ms: 120000,
    recorded_at: "2026-01-10T00:00:00Z",
    ...overrides,
  };
}

const defaultParams: HealthCheckParams = {
  period: 30,
  severity: "info",
  dryRun: false,
};

describe("PipelineHealthRunner", () => {
  beforeEach(() => {
    vi.clearAllMocks();

    // Default mock implementations
    vi.mocked(DataAggregator.parseDateFilter).mockReturnValue({
      startDate: new Date("2026-01-01"),
      endDate: new Date("2026-02-01"),
    });
    vi.mocked(DataAggregator.aggregate).mockResolvedValue(buildDataset());
  });

  describe("run()", () => {
    it("returns a report with correct schema_version", async () => {
      const report = await PipelineHealthRunner.run("/workspace", defaultParams);

      expect(report.schema_version).toBe("1.0");
    });

    it("returns report with analysis_period matching params", async () => {
      const report = await PipelineHealthRunner.run("/workspace", {
        ...defaultParams,
        period: 7,
      });

      expect(report.analysis_period.period_days).toBe(7);
      expect(report.analysis_period.from).toBeDefined();
      expect(report.analysis_period.to).toBeDefined();
    });

    it("returns report with data_quality from dataset", async () => {
      const report = await PipelineHealthRunner.run("/workspace", defaultParams);

      expect(report.data_quality).toEqual({
        sources_found: 3,
        sources_missing: 1,
        gap_days: 1,
      });
    });

    it("returns report with summary computed from aggregated data", async () => {
      const report = await PipelineHealthRunner.run("/workspace", defaultParams);

      expect(report.summary.total_runs).toBe(5);
      expect(report.summary.success_rate).toBe(0.8);
      expect(report.summary.total_cost_usd).toBe(12.5);
      expect(report.summary.avg_cost_per_run).toBe(2.5);
      expect(report.summary.avg_duration_ms).toBe(120000);
    });

    it("computes cache_hit_rate from token data", async () => {
      // totalInput = 50000, cacheRead = 20000
      // rate = 20000 / (50000 + 20000) = 0.2857...
      const report = await PipelineHealthRunner.run("/workspace", defaultParams);

      expect(report.summary.cache_hit_rate).toBeCloseTo(0.2857, 3);
    });

    it("returns cache_hit_rate 0 when no tokens", async () => {
      const dataset = buildDataset({
        summary: {
          totalRuns: 0,
          successfulRuns: 0,
          failedRuns: 0,
          successRate: 0,
          totalCostUsd: 0,
          avgCostPerRun: 0,
          totalInputTokens: 0,
          totalOutputTokens: 0,
          totalCacheReadTokens: 0,
          totalCacheCreationTokens: 0,
          avgDurationMs: 0,
        },
      });
      vi.mocked(DataAggregator.aggregate).mockResolvedValue(dataset);

      const report = await PipelineHealthRunner.run("/workspace", defaultParams);

      expect(report.summary.cache_hit_rate).toBe(0);
    });

    it("has created_at timestamp", async () => {
      const report = await PipelineHealthRunner.run("/workspace", defaultParams);

      expect(report.created_at).toBeDefined();
      expect(new Date(report.created_at).getTime()).not.toBeNaN();
    });

    it("initializes findings_by_severity counts", async () => {
      const report = await PipelineHealthRunner.run("/workspace", defaultParams);

      expect(report.findings_by_severity).toHaveProperty("critical");
      expect(report.findings_by_severity).toHaveProperty("high");
      expect(report.findings_by_severity).toHaveProperty("warning");
      expect(report.findings_by_severity).toHaveProperty("info");
    });
  });

  describe("insufficient data", () => {
    it("returns insufficient-data finding when < 2 run records", async () => {
      const dataset = buildDataset({
        executionHistory: [buildRunRecord() as never],
      });
      vi.mocked(DataAggregator.aggregate).mockResolvedValue(dataset);

      const report = await PipelineHealthRunner.run("/workspace", defaultParams);

      expect(report.findings).toHaveLength(1);
      expect(report.findings[0].id).toBe("insufficient-data");
      expect(report.findings[0].dimension).toBe("data-quality");
      expect(report.findings[0].severity).toBe("info");
    });

    it("returns insufficient-data finding when 0 run records", async () => {
      const dataset = buildDataset({ executionHistory: [] });
      vi.mocked(DataAggregator.aggregate).mockResolvedValue(dataset);

      const report = await PipelineHealthRunner.run("/workspace", defaultParams);

      expect(report.findings.some((f) => f.id === "insufficient-data")).toBe(true);
    });
  });

  describe("severity filtering", () => {
    it("filters out info findings when severity is warning", async () => {
      // With < 2 records, the only finding is info-level 'insufficient-data'
      const dataset = buildDataset({
        executionHistory: [buildRunRecord() as never],
      });
      vi.mocked(DataAggregator.aggregate).mockResolvedValue(dataset);

      const report = await PipelineHealthRunner.run("/workspace", {
        ...defaultParams,
        severity: "warning",
      });

      // Info-level findings should be filtered out
      expect(report.findings.every((f) => f.severity !== "info")).toBe(true);
    });

    it("keeps all findings when severity is info", async () => {
      const dataset = buildDataset({
        executionHistory: [buildRunRecord() as never],
      });
      vi.mocked(DataAggregator.aggregate).mockResolvedValue(dataset);

      const report = await PipelineHealthRunner.run("/workspace", {
        ...defaultParams,
        severity: "info",
      });

      expect(report.findings.length).toBeGreaterThanOrEqual(1);
    });
  });

  describe("dryRun", () => {
    it("does not write report file when dryRun is true", async () => {
      await PipelineHealthRunner.run("/workspace", {
        ...defaultParams,
        dryRun: true,
      });

      expect(fs.mkdir).not.toHaveBeenCalled();
      expect(fs.writeFile).not.toHaveBeenCalled();
    });

    it("writes report file when dryRun is false", async () => {
      await PipelineHealthRunner.run("/workspace", {
        ...defaultParams,
        dryRun: false,
      });

      expect(fs.mkdir).toHaveBeenCalled();
      expect(fs.writeFile).toHaveBeenCalled();
    });

    it("writes to .nightgauge/pipeline/ directory", async () => {
      await PipelineHealthRunner.run("/workspace", defaultParams);

      const mkdirCall = vi.mocked(fs.mkdir).mock.calls[0];
      expect(mkdirCall[0]).toContain(".nightgauge/pipeline");
      expect(mkdirCall[1]).toEqual({ recursive: true });
    });
  });

  describe("DataAggregator integration", () => {
    it("calls parseDateFilter with the period", async () => {
      await PipelineHealthRunner.run("/workspace", {
        ...defaultParams,
        period: 90,
      });

      expect(DataAggregator.parseDateFilter).toHaveBeenCalledWith({
        period: 90,
      });
    });

    it("calls aggregate with workspaceRoot and filter", async () => {
      const filter = {
        startDate: new Date("2026-01-01"),
        endDate: new Date("2026-02-01"),
      };
      vi.mocked(DataAggregator.parseDateFilter).mockReturnValue(filter);

      await PipelineHealthRunner.run("/workspace", defaultParams);

      expect(DataAggregator.aggregate).toHaveBeenCalledWith("/workspace", filter);
    });
  });

  describe("SDK graceful degradation", () => {
    it("returns report even when SDK analyzers are unavailable", async () => {
      // SDK mocks throw by default — should not crash
      const dataset = buildDataset({
        executionHistory: [
          buildRunRecord() as never,
          buildRunRecord({ issue_number: 101 }) as never,
          buildRunRecord({ issue_number: 102 }) as never,
        ],
      });
      vi.mocked(DataAggregator.aggregate).mockResolvedValue(dataset);

      const report = await PipelineHealthRunner.run("/workspace", defaultParams);

      expect(report).toBeDefined();
      expect(report.schema_version).toBe("1.0");
    });
  });
});
