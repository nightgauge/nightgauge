/**
 * DataAggregator.test.ts
 *
 * Unit tests for DataAggregator — unified data aggregation across all
 * telemetry sources. Covers date filter parsing, per-source loading,
 * summary computation, quality metrics, and graceful error handling.
 *
 * @see Issue #1100 - Build Comprehensive Data Aggregation Layer
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as fs from "node:fs/promises";
import { DataAggregator } from "../../src/services/DataAggregator";
import { ExecutionHistoryReader } from "../../src/utils/executionHistoryReader";
import { HealthScoreHistoryReader } from "../../src/utils/healthScoreHistory";
import { ExperimentManager } from "@nightgauge/sdk";
import type { ExecutionHistoryRunRecordV2 } from "../../src/schemas/executionHistory";
import type { HealthScoreSnapshot } from "../../src/schemas/healthScoreHistory";
import type { ExperimentOutcome } from "@nightgauge/sdk";
import type { DateRangeFilter } from "../../src/types/aggregation";

vi.mock("node:fs/promises");

const WORKSPACE = "/test/workspace";

/** Build a mock v2 execution history run record */
function buildRunRecord(
  overrides: Partial<ExecutionHistoryRunRecordV2> = {}
): ExecutionHistoryRunRecordV2 {
  return {
    schema_version: "2",
    record_type: "run",
    issue_number: 100,
    title: "Test issue",
    branch: "feat/100-test",
    base_branch: "main",
    execution_mode: "automatic",
    started_at: "2026-02-15T10:00:00.000Z",
    completed_at: "2026-02-15T10:30:00.000Z",
    total_duration_ms: 1800000,
    outcome: "complete",
    stages: {},
    tokens: {
      total_input: 10000,
      total_output: 5000,
      total_cache_read: 3000,
      total_cache_creation: 1000,
      estimated_cost_usd: 0.5,
    },
    files: { read_count: 10, written_count: 5 },
    routing: { complexity_score: 3, path: "standard", skip_stages: [] },
    recorded_at: "2026-02-15T10:30:00.000Z",
    ...overrides,
  };
}

/** Build a mock health score snapshot */
function buildHealthSnapshot(overrides: Partial<HealthScoreSnapshot> = {}): HealthScoreSnapshot {
  return {
    schema_version: "1",
    timestamp: "2026-02-15T11:00:00.000Z",
    score: 85,
    status: "good",
    components: { cost: 80, success: 90 },
    cacheHitRate: 0.75,
    costUsd: 0.5,
    issueNumber: 100,
    ...overrides,
  };
}

/** Build a mock experiment outcome */
function buildExperimentOutcome(overrides: Partial<ExperimentOutcome> = {}): ExperimentOutcome {
  return {
    experiment_name: "test-exp",
    group: "control",
    issue_number: 100,
    stage: "feature-dev",
    model: "sonnet",
    success: true,
    cost_usd: 0.25,
    duration_ms: 60000,
    retry_count: 0,
    recorded_at: "2026-02-15T10:30:00.000Z",
    ...overrides,
  };
}

/** Build a date filter for a known range */
function buildFilter(startStr: string, endStr: string): DateRangeFilter {
  const startDate = new Date(startStr);
  startDate.setUTCHours(0, 0, 0, 0);
  const endDate = new Date(endStr);
  endDate.setUTCHours(23, 59, 59, 999);
  return { startDate, endDate };
}

describe("DataAggregator", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // Default mocks — no data available
    vi.mocked(fs.readdir).mockRejectedValue(Object.assign(new Error("ENOENT"), { code: "ENOENT" }));
    vi.mocked(fs.readFile).mockRejectedValue(
      Object.assign(new Error("ENOENT"), { code: "ENOENT" })
    );

    vi.spyOn(ExecutionHistoryReader, "readDateRange").mockResolvedValue([]);
    vi.spyOn(HealthScoreHistoryReader, "readDateRange").mockResolvedValue([]);
    vi.spyOn(ExperimentManager, "readOutcomes").mockReturnValue([]);
  });

  // ===========================================================================
  // parseDateFilter
  // ===========================================================================

  describe("parseDateFilter", () => {
    it("should default to 30-day period ending today", () => {
      const filter = DataAggregator.parseDateFilter({});
      // Compare date portions (start is 00:00, end is 23:59, so diff ~N+1 days)
      const startDay = filter.startDate.toISOString().split("T")[0];
      const endDay = filter.endDate.toISOString().split("T")[0];
      const dayDiff =
        (new Date(endDay).getTime() - new Date(startDay).getTime()) / (1000 * 60 * 60 * 24);
      expect(dayDiff).toBe(30);
    });

    it("should parse explicit period", () => {
      const filter = DataAggregator.parseDateFilter({ period: 7 });
      const startDay = filter.startDate.toISOString().split("T")[0];
      const endDay = filter.endDate.toISOString().split("T")[0];
      const dayDiff =
        (new Date(endDay).getTime() - new Date(startDay).getTime()) / (1000 * 60 * 60 * 24);
      expect(dayDiff).toBe(7);
    });

    it("should parse since date", () => {
      const filter = DataAggregator.parseDateFilter({
        since: "2026-02-01",
      });
      expect(filter.startDate.toISOString().split("T")[0]).toBe("2026-02-01");
      expect(filter.startDate.getUTCHours()).toBe(0);
    });

    it("should parse until date", () => {
      const filter = DataAggregator.parseDateFilter({
        until: "2026-02-15",
      });
      expect(filter.endDate.toISOString().split("T")[0]).toBe("2026-02-15");
      expect(filter.endDate.getUTCHours()).toBe(23);
    });

    it("should parse since and until together", () => {
      const filter = DataAggregator.parseDateFilter({
        since: "2026-01-01",
        until: "2026-01-31",
      });
      expect(filter.startDate.toISOString().split("T")[0]).toBe("2026-01-01");
      expect(filter.endDate.toISOString().split("T")[0]).toBe("2026-01-31");
    });

    it("should prioritize since over period", () => {
      const filter = DataAggregator.parseDateFilter({
        since: "2026-02-01",
        period: 7,
        until: "2026-02-15",
      });
      // since takes priority over period
      expect(filter.startDate.toISOString().split("T")[0]).toBe("2026-02-01");
      expect(filter.endDate.toISOString().split("T")[0]).toBe("2026-02-15");
    });
  });

  // ===========================================================================
  // aggregate — empty/missing sources
  // ===========================================================================

  describe("aggregate — empty sources", () => {
    it("should return empty dataset when no sources have data", async () => {
      const filter = buildFilter("2026-02-10", "2026-02-15");
      const result = await DataAggregator.aggregate(WORKSPACE, filter);

      expect(result.executionHistory).toEqual([]);
      expect(result.healthScores).toEqual([]);
      expect(result.analysisReports).toEqual([]);
      expect(result.experimentResults).toEqual([]);
      expect(result.healthReports).toEqual([]);
      expect(result.filter).toBe(filter);
    });

    it("should report zero summary for empty dataset", async () => {
      const filter = buildFilter("2026-02-10", "2026-02-15");
      const result = await DataAggregator.aggregate(WORKSPACE, filter);

      expect(result.summary.totalRuns).toBe(0);
      expect(result.summary.successfulRuns).toBe(0);
      expect(result.summary.failedRuns).toBe(0);
      expect(result.summary.successRate).toBe(0);
      expect(result.summary.totalCostUsd).toBe(0);
      expect(result.summary.avgCostPerRun).toBe(0);
      expect(result.summary.avgDurationMs).toBe(0);
    });

    it("should report quality metrics for empty dataset", async () => {
      const filter = buildFilter("2026-02-10", "2026-02-15");
      const result = await DataAggregator.aggregate(WORKSPACE, filter);

      expect(result.quality.totalRecords).toBe(0);
      expect(result.quality.dateRangeCovered).toBeNull();
      // No data means no gap days reported (avoid flooding with all days as gaps)
      expect(result.quality.gapDays).toEqual([]);
    });
  });

  // ===========================================================================
  // aggregate — with data
  // ===========================================================================

  describe("aggregate — with execution history", () => {
    it("should include execution history records from reader", async () => {
      const records = [
        buildRunRecord({
          issue_number: 100,
          recorded_at: "2026-02-12T10:00:00.000Z",
        }),
        buildRunRecord({
          issue_number: 101,
          recorded_at: "2026-02-13T10:00:00.000Z",
        }),
      ];
      vi.spyOn(ExecutionHistoryReader, "readDateRange").mockResolvedValue(records);

      const filter = buildFilter("2026-02-10", "2026-02-15");
      const result = await DataAggregator.aggregate(WORKSPACE, filter);

      expect(result.executionHistory).toHaveLength(2);
      expect(ExecutionHistoryReader.readDateRange).toHaveBeenCalledWith(
        WORKSPACE,
        filter.startDate,
        filter.endDate
      );
    });

    it("should compute summary from run records", async () => {
      const records = [
        buildRunRecord({
          outcome: "complete",
          tokens: {
            total_input: 10000,
            total_output: 5000,
            total_cache_read: 3000,
            total_cache_creation: 1000,
            estimated_cost_usd: 0.5,
          },
          total_duration_ms: 1800000,
          recorded_at: "2026-02-12T10:00:00.000Z",
        }),
        buildRunRecord({
          outcome: "failed",
          tokens: {
            total_input: 8000,
            total_output: 4000,
            total_cache_read: 2000,
            total_cache_creation: 500,
            estimated_cost_usd: 0.3,
          },
          total_duration_ms: 600000,
          recorded_at: "2026-02-13T10:00:00.000Z",
        }),
      ];
      vi.spyOn(ExecutionHistoryReader, "readDateRange").mockResolvedValue(records);

      const filter = buildFilter("2026-02-10", "2026-02-15");
      const result = await DataAggregator.aggregate(WORKSPACE, filter);

      expect(result.summary.totalRuns).toBe(2);
      expect(result.summary.successfulRuns).toBe(1);
      expect(result.summary.failedRuns).toBe(1);
      expect(result.summary.successRate).toBe(0.5);
      expect(result.summary.totalCostUsd).toBeCloseTo(0.8);
      expect(result.summary.avgCostPerRun).toBeCloseTo(0.4);
      expect(result.summary.totalInputTokens).toBe(18000);
      expect(result.summary.totalOutputTokens).toBe(9000);
      expect(result.summary.totalCacheReadTokens).toBe(5000);
      expect(result.summary.totalCacheCreationTokens).toBe(1500);
      expect(result.summary.avgDurationMs).toBe(1200000);
    });
  });

  describe("aggregate — with health scores", () => {
    it("should include health scores from reader", async () => {
      const snapshots = [buildHealthSnapshot({ timestamp: "2026-02-12T11:00:00.000Z" })];
      vi.spyOn(HealthScoreHistoryReader, "readDateRange").mockResolvedValue(snapshots);

      const filter = buildFilter("2026-02-10", "2026-02-15");
      const result = await DataAggregator.aggregate(WORKSPACE, filter);

      expect(result.healthScores).toHaveLength(1);
      expect(HealthScoreHistoryReader.readDateRange).toHaveBeenCalledWith(
        WORKSPACE,
        filter.startDate,
        filter.endDate
      );
    });
  });

  describe("aggregate — with analysis reports", () => {
    it("should read and filter analysis reports by created_at", async () => {
      const analysisDir = `${WORKSPACE}/.nightgauge/analysis`;
      vi.mocked(fs.readdir).mockImplementation(async (dirPath) => {
        const p = typeof dirPath === "string" ? dirPath : dirPath.toString();
        if (p === analysisDir) {
          return [
            "analysis-2026-02-12T10-00-00-000Z.json",
            "analysis-2026-02-20T10-00-00-000Z.json",
            "latest.json",
          ] as unknown as Awaited<ReturnType<typeof fs.readdir>>;
        }
        throw Object.assign(new Error("ENOENT"), { code: "ENOENT" });
      });

      const inRangeReport = JSON.stringify({
        issue_number: 100,
        pipeline_completion_time: "2026-02-12T10:00:00.000Z",
        analysis: {},
        auto_tune_applied: [],
        created_at: "2026-02-12T10:00:00.000Z",
      });
      const outOfRangeReport = JSON.stringify({
        issue_number: 101,
        pipeline_completion_time: "2026-02-20T10:00:00.000Z",
        analysis: {},
        auto_tune_applied: [],
        created_at: "2026-02-20T10:00:00.000Z",
      });

      vi.mocked(fs.readFile).mockImplementation(async (filePath) => {
        const p = typeof filePath === "string" ? filePath : filePath.toString();
        if (p.includes("2026-02-12")) return inRangeReport;
        if (p.includes("2026-02-20")) return outOfRangeReport;
        throw Object.assign(new Error("ENOENT"), { code: "ENOENT" });
      });

      const filter = buildFilter("2026-02-10", "2026-02-15");
      const result = await DataAggregator.aggregate(WORKSPACE, filter);

      expect(result.analysisReports).toHaveLength(1);
      expect(result.analysisReports[0].issue_number).toBe(100);
    });

    it("should skip latest.json", async () => {
      const analysisDir = `${WORKSPACE}/.nightgauge/analysis`;
      vi.mocked(fs.readdir).mockImplementation(async (dirPath) => {
        const p = typeof dirPath === "string" ? dirPath : dirPath.toString();
        if (p === analysisDir) {
          return ["latest.json"] as unknown as Awaited<ReturnType<typeof fs.readdir>>;
        }
        throw Object.assign(new Error("ENOENT"), { code: "ENOENT" });
      });

      const filter = buildFilter("2026-02-10", "2026-02-15");
      const result = await DataAggregator.aggregate(WORKSPACE, filter);

      expect(result.analysisReports).toHaveLength(0);
      // readFile should not have been called for latest.json
      expect(fs.readFile).not.toHaveBeenCalled();
    });
  });

  describe("aggregate — with experiment results", () => {
    it("should read and filter experiment outcomes", async () => {
      const experimentsDir = `${WORKSPACE}/.nightgauge/analysis/experiments`;
      vi.mocked(fs.readdir).mockImplementation(async (dirPath) => {
        const p = typeof dirPath === "string" ? dirPath : dirPath.toString();
        if (p === experimentsDir) {
          return ["model-test.jsonl"] as unknown as Awaited<ReturnType<typeof fs.readdir>>;
        }
        throw Object.assign(new Error("ENOENT"), { code: "ENOENT" });
      });

      vi.spyOn(ExperimentManager, "readOutcomes").mockReturnValue([
        buildExperimentOutcome({ recorded_at: "2026-02-12T10:00:00.000Z" }),
        buildExperimentOutcome({ recorded_at: "2026-02-20T10:00:00.000Z" }), // outside range
      ]);

      const filter = buildFilter("2026-02-10", "2026-02-15");
      const result = await DataAggregator.aggregate(WORKSPACE, filter);

      expect(result.experimentResults).toHaveLength(1);
      expect(ExperimentManager.readOutcomes).toHaveBeenCalledWith(WORKSPACE, "model-test");
    });
  });

  describe("aggregate — with health reports", () => {
    it("should read and filter health reports by period overlap", async () => {
      const pipelineDir = `${WORKSPACE}/.nightgauge/pipeline`;
      vi.mocked(fs.readdir).mockImplementation(async (dirPath) => {
        const p = typeof dirPath === "string" ? dirPath : dirPath.toString();
        if (p === pipelineDir) {
          return [
            "health-report-2026-02-12.json",
            "health-report-2026-01-01.json",
          ] as unknown as Awaited<ReturnType<typeof fs.readdir>>;
        }
        throw Object.assign(new Error("ENOENT"), { code: "ENOENT" });
      });

      const overlappingReport = JSON.stringify({
        schema_version: "1.0",
        analysis_period: {
          from: "2026-02-01",
          to: "2026-02-12",
          period_days: 12,
          data_sources_found: 5,
          data_sources_missing: 0,
        },
        summary: {
          total_cost_usd: 10.0,
          avg_cost_per_run: 0.5,
          total_runs: 20,
          success_rate: 0.95,
          avg_duration_minutes: 30,
          total_tokens: 500000,
          cache_hit_rate: 0.75,
        },
        findings: [],
        recommendations: [],
        created_at: "2026-02-12T10:00:00.000Z",
      });

      const nonOverlappingReport = JSON.stringify({
        schema_version: "1.0",
        analysis_period: {
          from: "2025-12-01",
          to: "2025-12-31",
          period_days: 31,
          data_sources_found: 3,
          data_sources_missing: 2,
        },
        summary: {
          total_cost_usd: 5.0,
          avg_cost_per_run: 0.5,
          total_runs: 10,
          success_rate: 0.8,
          avg_duration_minutes: 25,
          total_tokens: 200000,
          cache_hit_rate: 0.6,
        },
        findings: [],
        recommendations: [],
        created_at: "2025-12-31T10:00:00.000Z",
      });

      vi.mocked(fs.readFile).mockImplementation(async (filePath) => {
        const p = typeof filePath === "string" ? filePath : filePath.toString();
        if (p.includes("2026-02-12")) return overlappingReport;
        if (p.includes("2026-01-01")) return nonOverlappingReport;
        throw Object.assign(new Error("ENOENT"), { code: "ENOENT" });
      });

      const filter = buildFilter("2026-02-10", "2026-02-15");
      const result = await DataAggregator.aggregate(WORKSPACE, filter);

      expect(result.healthReports).toHaveLength(1);
      expect(result.healthReports[0].analysis_period.from).toBe("2026-02-01");
    });
  });

  // ===========================================================================
  // aggregate — quality metrics
  // ===========================================================================

  describe("quality metrics", () => {
    it("should report per-source availability", async () => {
      vi.spyOn(ExecutionHistoryReader, "readDateRange").mockResolvedValue([
        buildRunRecord({ recorded_at: "2026-02-12T10:00:00.000Z" }),
      ]);
      vi.spyOn(HealthScoreHistoryReader, "readDateRange").mockResolvedValue([
        buildHealthSnapshot({ timestamp: "2026-02-12T11:00:00.000Z" }),
      ]);

      const filter = buildFilter("2026-02-10", "2026-02-15");
      const result = await DataAggregator.aggregate(WORKSPACE, filter);

      const execSource = result.quality.sources.find((s) => s.name === "Execution History");
      expect(execSource?.available).toBe(true);
      expect(execSource?.recordCount).toBe(1);

      const healthSource = result.quality.sources.find((s) => s.name === "Health Scores");
      expect(healthSource?.available).toBe(true);
      expect(healthSource?.recordCount).toBe(1);

      // All 5 sources are "available" (empty but no error = available)
      expect(result.quality.sourcesFound).toBe(5);
      // Only 2 sources have actual records
      expect(result.quality.totalRecords).toBe(2);
    });

    it("should compute date range coverage", async () => {
      vi.spyOn(ExecutionHistoryReader, "readDateRange").mockResolvedValue([
        buildRunRecord({ recorded_at: "2026-02-11T10:00:00.000Z" }),
        buildRunRecord({ recorded_at: "2026-02-14T10:00:00.000Z" }),
      ]);

      const filter = buildFilter("2026-02-10", "2026-02-15");
      const result = await DataAggregator.aggregate(WORKSPACE, filter);

      expect(result.quality.dateRangeCovered).not.toBeNull();
      expect(result.quality.dateRangeCovered!.start).toBe("2026-02-11T10:00:00.000Z");
      expect(result.quality.dateRangeCovered!.end).toBe("2026-02-14T10:00:00.000Z");
    });

    it("should detect gap days", async () => {
      vi.spyOn(ExecutionHistoryReader, "readDateRange").mockResolvedValue([
        buildRunRecord({ recorded_at: "2026-02-10T10:00:00.000Z" }),
        buildRunRecord({ recorded_at: "2026-02-13T10:00:00.000Z" }),
      ]);

      const filter = buildFilter("2026-02-10", "2026-02-13");
      const result = await DataAggregator.aggregate(WORKSPACE, filter);

      expect(result.quality.gapDays).toContain("2026-02-11");
      expect(result.quality.gapDays).toContain("2026-02-12");
      expect(result.quality.gapDays).not.toContain("2026-02-10");
      expect(result.quality.gapDays).not.toContain("2026-02-13");
    });

    it("should report requested date range", async () => {
      const filter = buildFilter("2026-02-10", "2026-02-15");
      const result = await DataAggregator.aggregate(WORKSPACE, filter);

      expect(result.quality.dateRangeRequested.start).toBe("2026-02-10");
      expect(result.quality.dateRangeRequested.end).toBe("2026-02-15");
    });
  });

  // ===========================================================================
  // aggregate — error handling
  // ===========================================================================

  describe("error handling", () => {
    it("should handle reader failure gracefully", async () => {
      vi.spyOn(ExecutionHistoryReader, "readDateRange").mockRejectedValue(
        new Error("Disk read error")
      );
      // Other sources still work
      vi.spyOn(HealthScoreHistoryReader, "readDateRange").mockResolvedValue([
        buildHealthSnapshot({ timestamp: "2026-02-12T11:00:00.000Z" }),
      ]);

      const filter = buildFilter("2026-02-10", "2026-02-15");
      const result = await DataAggregator.aggregate(WORKSPACE, filter);

      // Execution history failed but health scores still loaded
      expect(result.executionHistory).toEqual([]);
      expect(result.healthScores).toHaveLength(1);

      // Quality should report the failure
      const execSource = result.quality.sources.find((s) => s.name === "Execution History");
      expect(execSource?.available).toBe(false);
      expect(execSource?.warnings).toHaveLength(1);
      expect(execSource?.warnings[0]).toContain("Disk read error");
    });

    it("should handle malformed analysis report files", async () => {
      const analysisDir = `${WORKSPACE}/.nightgauge/analysis`;
      vi.mocked(fs.readdir).mockImplementation(async (dirPath) => {
        const p = typeof dirPath === "string" ? dirPath : dirPath.toString();
        if (p === analysisDir) {
          return ["analysis-corrupt.json"] as unknown as Awaited<ReturnType<typeof fs.readdir>>;
        }
        throw Object.assign(new Error("ENOENT"), { code: "ENOENT" });
      });

      vi.mocked(fs.readFile).mockResolvedValue("not valid json {{{");

      const filter = buildFilter("2026-02-10", "2026-02-15");
      const result = await DataAggregator.aggregate(WORKSPACE, filter);

      // Should not throw, just return empty
      expect(result.analysisReports).toHaveLength(0);
    });

    it("should continue when some sources fail", async () => {
      vi.spyOn(ExecutionHistoryReader, "readDateRange").mockRejectedValue(new Error("fail"));
      vi.spyOn(HealthScoreHistoryReader, "readDateRange").mockRejectedValue(new Error("fail"));

      const filter = buildFilter("2026-02-10", "2026-02-15");
      // Should not throw
      const result = await DataAggregator.aggregate(WORKSPACE, filter);

      expect(result.executionHistory).toEqual([]);
      expect(result.healthScores).toEqual([]);
      expect(result.quality.sourcesMissing).toBeGreaterThan(0);
    });
  });

  // ===========================================================================
  // aggregate — all sources populated
  // ===========================================================================

  describe("aggregate — full dataset", () => {
    it("should aggregate all sources together", async () => {
      // Set up all sources with data
      vi.spyOn(ExecutionHistoryReader, "readDateRange").mockResolvedValue([
        buildRunRecord({ recorded_at: "2026-02-12T10:00:00.000Z" }),
      ]);
      vi.spyOn(HealthScoreHistoryReader, "readDateRange").mockResolvedValue([
        buildHealthSnapshot({ timestamp: "2026-02-12T11:00:00.000Z" }),
      ]);
      // Analysis reports
      const analysisDir = `${WORKSPACE}/.nightgauge/analysis`;
      const experimentsDir = `${WORKSPACE}/.nightgauge/analysis/experiments`;
      const pipelineDir = `${WORKSPACE}/.nightgauge/pipeline`;

      vi.mocked(fs.readdir).mockImplementation(async (dirPath) => {
        const p = typeof dirPath === "string" ? dirPath : dirPath.toString();
        if (p === analysisDir) {
          return ["analysis-2026-02-12.json"] as unknown as Awaited<ReturnType<typeof fs.readdir>>;
        }
        if (p === experimentsDir) {
          return ["test-exp.jsonl"] as unknown as Awaited<ReturnType<typeof fs.readdir>>;
        }
        if (p === pipelineDir) {
          return ["health-report-2026-02-12.json"] as unknown as Awaited<
            ReturnType<typeof fs.readdir>
          >;
        }
        throw Object.assign(new Error("ENOENT"), { code: "ENOENT" });
      });

      vi.mocked(fs.readFile).mockImplementation(async (filePath) => {
        const p = typeof filePath === "string" ? filePath : filePath.toString();
        if (p.includes("analysis-2026-02-12")) {
          return JSON.stringify({
            issue_number: 100,
            pipeline_completion_time: "2026-02-12T10:00:00.000Z",
            analysis: {},
            auto_tune_applied: [],
            created_at: "2026-02-12T10:00:00.000Z",
          });
        }
        if (p.includes("health-report-2026-02-12")) {
          return JSON.stringify({
            schema_version: "1.0",
            analysis_period: {
              from: "2026-02-01",
              to: "2026-02-12",
              period_days: 12,
              data_sources_found: 5,
              data_sources_missing: 0,
            },
            summary: {
              total_cost_usd: 10.0,
              avg_cost_per_run: 0.5,
              total_runs: 20,
              success_rate: 0.95,
              avg_duration_minutes: 30,
              total_tokens: 500000,
              cache_hit_rate: 0.75,
            },
            findings: [],
            recommendations: [],
            created_at: "2026-02-12T10:00:00.000Z",
          });
        }
        throw Object.assign(new Error("ENOENT"), { code: "ENOENT" });
      });

      vi.spyOn(ExperimentManager, "readOutcomes").mockReturnValue([
        buildExperimentOutcome({ recorded_at: "2026-02-12T10:00:00.000Z" }),
      ]);

      const filter = buildFilter("2026-02-10", "2026-02-15");
      const result = await DataAggregator.aggregate(WORKSPACE, filter);

      expect(result.executionHistory).toHaveLength(1);
      expect(result.healthScores).toHaveLength(1);
      expect(result.analysisReports).toHaveLength(1);
      expect(result.experimentResults).toHaveLength(1);
      expect(result.healthReports).toHaveLength(1);

      expect(result.quality.totalRecords).toBe(5);
      expect(result.quality.sourcesFound).toBe(5);
      expect(result.quality.sourcesMissing).toBe(0);
    });
  });
});
