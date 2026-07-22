/**
 * Unit tests for StageDurationAnalyzer
 *
 * Uses synthetic JSONL data via mocked ExecutionHistoryReader to cover
 * all acceptance criteria and edge cases from Issue #2651.
 *
 * @see Issue #2651 - Compute per-stage duration percentiles from JSONL history
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { ExecutionHistoryRecord } from "../../src/schemas/executionHistory";

// Mock ExecutionHistoryReader before importing the module under test
vi.mock("../../src/utils/executionHistoryReader", () => ({
  ExecutionHistoryReader: {
    readAll: vi.fn(),
  },
}));

import { StageDurationAnalyzer } from "../../src/utils/StageDurationAnalyzer";
import { ExecutionHistoryReader } from "../../src/utils/executionHistoryReader";

// ============================================================================
// Test Helpers
// ============================================================================

const WORKSPACE_ROOT = "/test/workspace";

const ALL_STAGES = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
] as const;

const RECENT_BASE_DATE = new Date(Date.now() - 5 * 24 * 60 * 60 * 1000).toISOString();

/**
 * Build a minimal v2 run record with the given stage durations.
 * Only stages in `stageDurations` are included; all have status 'complete'.
 */
function makeRunRecord(opts: {
  issueNumber?: number;
  outcome?: "complete" | "failed" | "cancelled" | "abandoned";
  stageDurations?: Partial<Record<(typeof ALL_STAGES)[number], number>>;
  recordedAt?: string;
}): ExecutionHistoryRecord {
  const {
    issueNumber = 100,
    outcome = "complete",
    stageDurations = {},
    recordedAt = RECENT_BASE_DATE,
  } = opts;

  const stages: Record<string, { status: "complete" | "failed"; duration_ms?: number }> = {};
  for (const [stage, durationMs] of Object.entries(stageDurations)) {
    stages[stage] = {
      status: "complete",
      duration_ms: durationMs,
      started_at: recordedAt,
      completed_at: recordedAt,
    };
  }

  return {
    schema_version: "2",
    record_type: "run",
    issue_number: issueNumber,
    title: `Test issue #${issueNumber}`,
    branch: `feat/test-${issueNumber}`,
    base_branch: "main",
    execution_mode: "automatic",
    started_at: recordedAt,
    completed_at: recordedAt,
    total_duration_ms: Object.values(stageDurations).reduce((s, v) => s + (v ?? 0), 0),
    outcome,
    stages,
    tokens: {
      total_input: 1000,
      total_output: 500,
      total_cache_read: 0,
      total_cache_creation: 0,
      estimated_cost_usd: 0.1,
    },
    files: { read_count: 10, written_count: 3 },
    routing: { complexity_score: 3, path: "standard", skip_stages: [] },
    recorded_at: recordedAt,
  } as unknown as ExecutionHistoryRecord;
}

/**
 * Build a set of N run records with predictable stage durations for a single
 * stage, making percentile assertions straightforward.
 *
 * durations[i] is the duration_ms for run i.
 */
function makeRunsForStage(
  stage: (typeof ALL_STAGES)[number],
  durations: number[],
  baseDate = RECENT_BASE_DATE
): ExecutionHistoryRecord[] {
  return durations.map((d, i) =>
    makeRunRecord({
      issueNumber: 200 + i,
      stageDurations: { [stage]: d },
      recordedAt: new Date(new Date(baseDate).getTime() + i * 1000).toISOString(),
    })
  );
}

function mockReadAll(records: ExecutionHistoryRecord[]) {
  vi.mocked(ExecutionHistoryReader.readAll).mockResolvedValue(records);
}

// ============================================================================
// Tests
// ============================================================================

describe("StageDurationAnalyzer", () => {
  beforeEach(async () => {
    vi.clearAllMocks();
    // Always invalidate cache between tests to avoid cross-contamination
    await StageDurationAnalyzer.invalidateCache();
  });

  // -----------------------------------------------------------------------
  // analyzeStageDurations — happy path
  // -----------------------------------------------------------------------

  describe("analyzeStageDurations - basic analysis with 5 runs", () => {
    it("returns stats for every stage present in completed runs", async () => {
      const records = [
        makeRunRecord({
          stageDurations: {
            "issue-pickup": 120000,
            "feature-planning": 300000,
            "feature-dev": 900000,
            "feature-validate": 180000,
            "pr-create": 60000,
            "pr-merge": 120000,
          },
        }),
        makeRunRecord({
          issueNumber: 101,
          stageDurations: {
            "issue-pickup": 100000,
            "feature-planning": 280000,
            "feature-dev": 850000,
            "feature-validate": 160000,
            "pr-create": 50000,
            "pr-merge": 110000,
          },
        }),
      ];
      mockReadAll(records);

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      expect(result.schema_version).toBe("1");
      expect(result.total_runs_analyzed).toBe(2);
      for (const stage of ALL_STAGES) {
        expect(result.stages[stage]).toBeDefined();
        expect(result.stages[stage].count).toBe(2);
      }
    });

    it("sets correct stage name on each StageStats object", async () => {
      mockReadAll(makeRunsForStage("feature-dev", [600000]));

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      expect(result.stages["feature-dev"].stage).toBe("feature-dev");
    });

    it("computes mean correctly", async () => {
      mockReadAll(makeRunsForStage("feature-dev", [200000, 400000]));

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      expect(result.stages["feature-dev"].mean_ms).toBe(300000);
    });

    it("sets min and max correctly", async () => {
      mockReadAll(makeRunsForStage("feature-dev", [100000, 300000, 500000]));

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);
      const stats = result.stages["feature-dev"];

      expect(stats.min_ms).toBe(100000);
      expect(stats.max_ms).toBe(500000);
    });
  });

  // -----------------------------------------------------------------------
  // analyzeStageDurations — filtering
  // -----------------------------------------------------------------------

  describe("analyzeStageDurations - filters to complete outcome only", () => {
    it("excludes records with outcome !== 'complete'", async () => {
      const records = [
        makeRunRecord({ outcome: "complete", stageDurations: { "feature-dev": 600000 } }),
        makeRunRecord({
          issueNumber: 101,
          outcome: "failed",
          stageDurations: { "feature-dev": 100000 },
        }),
        makeRunRecord({
          issueNumber: 102,
          outcome: "cancelled",
          stageDurations: { "feature-dev": 50000 },
        }),
      ];
      mockReadAll(records);

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      // Only the completed run should contribute
      expect(result.total_runs_analyzed).toBe(1);
      expect(result.stages["feature-dev"].count).toBe(1);
      expect(result.stages["feature-dev"].mean_ms).toBe(600000);
    });

    it("excludes records with record_type !== 'run'", async () => {
      const outcomeRecord = {
        schema_version: "2",
        record_type: "outcome",
        issue_number: 200,
        outcome: "merged",
        recorded_at: "2026-04-01T10:00:00Z",
      } as unknown as ExecutionHistoryRecord;

      const records = [makeRunRecord({ stageDurations: { "feature-dev": 600000 } }), outcomeRecord];
      mockReadAll(records);

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      expect(result.total_runs_analyzed).toBe(1);
    });
  });

  describe("analyzeStageDurations - skips failed and cancelled runs", () => {
    it("produces empty stages when all runs are failed", async () => {
      const records = [
        makeRunRecord({ outcome: "failed", stageDurations: { "feature-dev": 300000 } }),
        makeRunRecord({
          issueNumber: 101,
          outcome: "failed",
          stageDurations: { "feature-dev": 400000 },
        }),
      ];
      mockReadAll(records);

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      expect(result.total_runs_analyzed).toBe(0);
      expect(Object.keys(result.stages)).toHaveLength(0);
    });
  });

  // -----------------------------------------------------------------------
  // analyzeStageDurations — edge cases
  // -----------------------------------------------------------------------

  describe("analyzeStageDurations - handles missing duration_ms gracefully", () => {
    it("skips stage entries where duration_ms is undefined", async () => {
      const record = makeRunRecord({
        stageDurations: { "feature-dev": 600000 },
      });
      // Inject a stage with undefined duration
      (record as any).stages["issue-pickup"] = { status: "complete" };
      mockReadAll([record]);

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      // feature-dev has a valid duration → present
      expect(result.stages["feature-dev"]).toBeDefined();
      // issue-pickup has no duration → absent
      expect(result.stages["issue-pickup"]).toBeUndefined();
    });

    it("skips stage entries where duration_ms is negative", async () => {
      const record = makeRunRecord({ stageDurations: {} });
      (record as any).stages["feature-dev"] = { status: "complete", duration_ms: -1 };
      mockReadAll([record]);

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      expect(result.stages["feature-dev"]).toBeUndefined();
    });

    it("includes a note when malformed entries are skipped", async () => {
      const record = makeRunRecord({ stageDurations: { "feature-dev": 600000 } });
      (record as any).stages["pr-create"] = { status: "complete" }; // no duration_ms
      mockReadAll([record]);

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      const hasSkipNote = result.analysis_notes.some((n) => n.includes("Skipped"));
      expect(hasSkipNote).toBe(true);
    });
  });

  describe("analyzeStageDurations - handles empty history gracefully", () => {
    it("returns empty result with note when no history files exist", async () => {
      mockReadAll([]);

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      expect(result.total_runs_analyzed).toBe(0);
      expect(Object.keys(result.stages)).toHaveLength(0);
      expect(result.analysis_notes.length).toBeGreaterThan(0);
    });

    it("never throws even when readAll rejects", async () => {
      vi.mocked(ExecutionHistoryReader.readAll).mockRejectedValue(new Error("ENOENT"));

      await expect(
        StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT)
      ).resolves.toBeDefined();
    });
  });

  describe("Edge case: single run (all percentiles equal)", () => {
    it("returns identical p50/p75/p95/p99 when only one sample exists", async () => {
      mockReadAll(makeRunsForStage("feature-dev", [600000]));

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);
      const stats = result.stages["feature-dev"];

      expect(stats.p50_ms).toBe(600000);
      expect(stats.p75_ms).toBe(600000);
      expect(stats.p95_ms).toBe(600000);
      expect(stats.p99_ms).toBe(600000);
      expect(stats.min_ms).toBe(600000);
      expect(stats.max_ms).toBe(600000);
    });

    it("returns stddev_ms of 0 for a single sample", async () => {
      mockReadAll(makeRunsForStage("feature-dev", [600000]));

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      expect(result.stages["feature-dev"].stddev_ms).toBe(0);
    });
  });

  // -----------------------------------------------------------------------
  // analyzeStageDurations — percentile accuracy
  // -----------------------------------------------------------------------

  describe("analyzeStageDurations - computes percentiles accurately", () => {
    /**
     * Known values for linear-interpolation percentile formula:
     * values = [100, 200, 300, 400, 500], n=5
     * index(p50) = 0.5 * 4 = 2.0 → sorted[2] = 300 (exact)
     * index(p75) = 0.75 * 4 = 3.0 → sorted[3] = 400 (exact)
     * index(p95) = 0.95 * 4 = 3.8 → 400*(1-0.8) + 500*0.8 = 80 + 400 = 480
     * index(p99) = 0.99 * 4 = 3.96 → 400*(1-0.96) + 500*0.96 = 16 + 480 = 496
     */
    it("computes p50 via linear interpolation (exact index)", async () => {
      mockReadAll(makeRunsForStage("feature-dev", [100, 200, 300, 400, 500]));

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      expect(result.stages["feature-dev"].p50_ms).toBe(300);
    });

    it("computes p75 via linear interpolation (exact index)", async () => {
      mockReadAll(makeRunsForStage("feature-dev", [100, 200, 300, 400, 500]));

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      expect(result.stages["feature-dev"].p75_ms).toBe(400);
    });

    it("computes p95 via linear interpolation (fractional index)", async () => {
      mockReadAll(makeRunsForStage("feature-dev", [100, 200, 300, 400, 500]));

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      // 480 rounded
      expect(result.stages["feature-dev"].p95_ms).toBe(480);
    });

    it("computes p99 via linear interpolation (fractional index)", async () => {
      mockReadAll(makeRunsForStage("feature-dev", [100, 200, 300, 400, 500]));

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      // 400*(1-0.96) + 500*0.96 = 16 + 480 = 496 → rounded to 496
      expect(result.stages["feature-dev"].p99_ms).toBe(496);
    });

    it("handles two-element array: p50 uses interpolation", async () => {
      // n=2, sorted=[100, 200]
      // p50: index = 0.5*(2-1) = 0.5 → 100*0.5 + 200*0.5 = 150
      mockReadAll(makeRunsForStage("feature-dev", [100, 200]));

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      expect(result.stages["feature-dev"].p50_ms).toBe(150);
    });

    it("computes correct standard deviation for known data", async () => {
      // [100, 200, 300] → mean=200, variance=((100^2+0+100^2)/2)=10000, stddev=100
      mockReadAll(makeRunsForStage("feature-dev", [100, 200, 300]));

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      expect(result.stages["feature-dev"].stddev_ms).toBe(100);
    });
  });

  // -----------------------------------------------------------------------
  // analyzeStageDurations — data staleness window
  // -----------------------------------------------------------------------

  describe("analyzeStageDurations - respects stale duration cutoff", () => {
    it("excludes runs older than the staleness window", async () => {
      const oldRun = makeRunRecord({
        stageDurations: { "feature-dev": 900000 },
        // 90 days ago
        recordedAt: new Date(Date.now() - 90 * 24 * 60 * 60 * 1000).toISOString(),
      });
      const recentRun = makeRunRecord({
        issueNumber: 101,
        stageDurations: { "feature-dev": 600000 },
        recordedAt: new Date(Date.now() - 5 * 24 * 60 * 60 * 1000).toISOString(),
      });
      mockReadAll([oldRun, recentRun]);

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT, 30);

      // Only the recent run should be included
      expect(result.total_runs_analyzed).toBe(1);
      expect(result.stages["feature-dev"].mean_ms).toBe(600000);
    });

    it("includes all runs when staleDurationDays is 0", async () => {
      const oldRun = makeRunRecord({
        stageDurations: { "feature-dev": 900000 },
        recordedAt: new Date(Date.now() - 365 * 24 * 60 * 60 * 1000).toISOString(),
      });
      const recentRun = makeRunRecord({
        issueNumber: 101,
        stageDurations: { "feature-dev": 600000 },
        recordedAt: new Date().toISOString(),
      });
      mockReadAll([oldRun, recentRun]);

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT, 0);

      expect(result.total_runs_analyzed).toBe(2);
    });

    it("adds a note when runs are excluded by staleness", async () => {
      const oldRun = makeRunRecord({
        stageDurations: { "feature-dev": 900000 },
        recordedAt: new Date(Date.now() - 60 * 24 * 60 * 60 * 1000).toISOString(),
      });
      mockReadAll([oldRun]);

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT, 30);

      const hasExcludeNote = result.analysis_notes.some((n) => n.includes("Excluded"));
      expect(hasExcludeNote).toBe(true);
    });
  });

  // -----------------------------------------------------------------------
  // getStageStats
  // -----------------------------------------------------------------------

  describe("getStageStats - returns stats for specific stage", () => {
    it("returns StageStats for a stage that has data", async () => {
      mockReadAll(makeRunsForStage("feature-dev", [600000, 700000]));

      const stats = await StageDurationAnalyzer.getStageStats(WORKSPACE_ROOT, "feature-dev");

      expect(stats).toBeDefined();
      expect(stats?.stage).toBe("feature-dev");
      expect(stats?.count).toBe(2);
    });

    it("returns only the requested stage, not others", async () => {
      const records = [
        makeRunRecord({
          stageDurations: {
            "feature-dev": 600000,
            "pr-create": 50000,
          },
        }),
      ];
      mockReadAll(records);

      const stats = await StageDurationAnalyzer.getStageStats(WORKSPACE_ROOT, "pr-create");

      expect(stats).toBeDefined();
      expect(stats?.stage).toBe("pr-create");
      expect(stats?.mean_ms).toBe(50000);
    });
  });

  describe("getStageStats - returns undefined for unknown stage", () => {
    it("returns undefined for a stage name not in history", async () => {
      mockReadAll(makeRunsForStage("feature-dev", [600000]));

      const stats = await StageDurationAnalyzer.getStageStats(WORKSPACE_ROOT, "nonexistent-stage");

      expect(stats).toBeUndefined();
    });

    it("returns undefined when history is empty", async () => {
      mockReadAll([]);

      const stats = await StageDurationAnalyzer.getStageStats(WORKSPACE_ROOT, "feature-dev");

      expect(stats).toBeUndefined();
    });

    it("never throws when stage is missing", async () => {
      mockReadAll([]);

      await expect(
        StageDurationAnalyzer.getStageStats(WORKSPACE_ROOT, "any-stage")
      ).resolves.toBeUndefined();
    });
  });

  // -----------------------------------------------------------------------
  // invalidateCache
  // -----------------------------------------------------------------------

  describe("invalidateCache - clears cached results", () => {
    it("forces recomputation after invalidation", async () => {
      mockReadAll(makeRunsForStage("feature-dev", [600000]));
      await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      // Change the mock data and invalidate
      mockReadAll(makeRunsForStage("feature-dev", [900000]));
      await StageDurationAnalyzer.invalidateCache();

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      // Should reflect the new data, not the cached old data
      expect(result.stages["feature-dev"].mean_ms).toBe(900000);
    });

    it("readAll is called again after cache invalidation", async () => {
      mockReadAll(makeRunsForStage("feature-dev", [600000]));

      await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);
      const callCountBefore = vi.mocked(ExecutionHistoryReader.readAll).mock.calls.length;

      await StageDurationAnalyzer.invalidateCache();
      await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      expect(vi.mocked(ExecutionHistoryReader.readAll).mock.calls.length).toBeGreaterThan(
        callCountBefore
      );
    });

    it("cache hit skips readAll on second call without invalidation", async () => {
      mockReadAll(makeRunsForStage("feature-dev", [600000]));

      // Use staleDurationDays=0 so cache never expires during the test
      await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT, 0);
      await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT, 0);

      // Should only have called readAll once
      expect(vi.mocked(ExecutionHistoryReader.readAll)).toHaveBeenCalledTimes(1);
    });

    it("invalidateCache resolves without error", async () => {
      await expect(StageDurationAnalyzer.invalidateCache()).resolves.toBeUndefined();
    });
  });

  // -----------------------------------------------------------------------
  // Output shape
  // -----------------------------------------------------------------------

  describe("analyzeStageDurations - output shape", () => {
    it("includes last_updated ISO timestamp on each StageStats", async () => {
      mockReadAll(makeRunsForStage("feature-dev", [600000]));

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);
      const ts = result.stages["feature-dev"].last_updated;

      expect(ts).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/);
    });

    it("includes computed_at ISO timestamp on the result", async () => {
      mockReadAll([]);

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      expect(result.computed_at).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/);
    });

    it("reflects the requested staleDurationDays in the result", async () => {
      mockReadAll([]);

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT, 14);

      expect(result.data_window_days).toBe(14);
    });

    it("stages with 0 completed runs are absent from result.stages", async () => {
      // Run with failed feature-dev stage — status 'failed' not 'complete'
      const record = makeRunRecord({ stageDurations: {} });
      (record as any).stages["feature-dev"] = { status: "failed", duration_ms: 300000 };
      mockReadAll([record]);

      const result = await StageDurationAnalyzer.analyzeStageDurations(WORKSPACE_ROOT);

      expect(result.stages["feature-dev"]).toBeUndefined();
    });
  });
});
