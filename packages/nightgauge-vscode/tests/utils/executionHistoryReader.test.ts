/**
 * executionHistoryReader.test.ts
 *
 * Unit tests for ExecutionHistoryReader utility class.
 *
 * @see Issue #649 - Execution History Persistence
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import { ExecutionHistoryReader } from "../../src/utils/executionHistoryReader";

// Mock node:fs/promises
vi.mock("node:fs/promises");

describe("ExecutionHistoryReader", () => {
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
    ExecutionHistoryReader.clearCache();
    // Default fs.stat mock — returns a stable mtime/size per path with a
    // random seed so each test starts from a clean cache state. Tests that
    // need to exercise the mtime-based cache-invalidation path can override
    // this with their own mockResolvedValue.
    let seed = Math.random();
    vi.mocked(fs.stat).mockImplementation(async (p: any) => {
      return {
        mtimeMs: seed++ * 1e10,
        size: 1,
        isFile: () => true,
      } as any;
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  const validRunLine = JSON.stringify({
    schema_version: "1",
    record_type: "run",
    issue_number: 42,
    title: "Test issue",
    branch: "feat/42-test",
    base_branch: "main",
    execution_mode: "automatic",
    started_at: "2026-02-13T10:00:00.000Z",
    completed_at: "2026-02-13T10:30:00.000Z",
    total_duration_ms: 1800000,
    outcome: "complete",
    stages: {},
    tokens: {
      total_input: 10000,
      total_output: 5000,
      total_cache_read: 2000,
      total_cache_creation: 1000,
      estimated_cost_usd: 0.1,
    },
    recorded_at: "2026-02-13T10:30:00.000Z",
  });

  const validOutcomeLine = JSON.stringify({
    schema_version: "1",
    record_type: "outcome",
    issue_number: 42,
    pr_number: 100,
    outcome: "merged",
    merged_at: "2026-02-13T12:00:00.000Z",
    recorded_at: "2026-02-13T12:00:01.000Z",
  });

  describe("parseJsonlFile()", () => {
    it("should parse a single JSONL file with valid records", async () => {
      vi.mocked(fs.readFile).mockResolvedValue(`${validRunLine}\n${validOutcomeLine}\n`);

      const records = await ExecutionHistoryReader.parseJsonlFile("/test/history/2026-02-13.jsonl");

      expect(records).toHaveLength(2);
      expect(records[0].record_type).toBe("run");
      expect(records[1].record_type).toBe("outcome");
    });

    it("should skip malformed lines gracefully", async () => {
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
      vi.mocked(fs.readFile).mockResolvedValue(
        `${validRunLine}\n{bad json}\n${validOutcomeLine}\n`
      );

      const records = await ExecutionHistoryReader.parseJsonlFile("/test/history/2026-02-13.jsonl");

      expect(records).toHaveLength(2);
      expect(warnSpy).toHaveBeenCalledWith(expect.stringContaining("Skipping unparseable"));

      warnSpy.mockRestore();
    });

    it("should skip lines that parse but fail schema validation", async () => {
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
      // Record has enough fields to pass the phantom filter (non-zero cost)
      // but fails schema validation due to missing required fields.
      const invalidRecord = JSON.stringify({
        schema_version: "1",
        record_type: "run",
        tokens: { estimated_cost_usd: 0.5 },
        stages: {
          "issue-pickup": { status: "complete" },
          "feature-dev": { status: "complete" },
        },
        // Missing all other required fields
      });
      vi.mocked(fs.readFile).mockResolvedValue(`${invalidRecord}\n`);

      const records = await ExecutionHistoryReader.parseJsonlFile("/test/history/2026-02-13.jsonl");

      expect(records).toHaveLength(0);
      expect(warnSpy).toHaveBeenCalledWith(expect.stringContaining("Skipping malformed"));

      warnSpy.mockRestore();
    });

    it("should return empty array for missing files", async () => {
      vi.mocked(fs.readFile).mockRejectedValue(
        Object.assign(new Error("ENOENT"), { code: "ENOENT" })
      );

      const records = await ExecutionHistoryReader.parseJsonlFile(
        "/test/history/nonexistent.jsonl"
      );

      expect(records).toHaveLength(0);
    });

    it("should handle empty files", async () => {
      vi.mocked(fs.readFile).mockResolvedValue("");

      const records = await ExecutionHistoryReader.parseJsonlFile("/test/history/empty.jsonl");

      expect(records).toHaveLength(0);
    });

    it("should handle trailing whitespace and blank lines", async () => {
      vi.mocked(fs.readFile).mockResolvedValue(`\n${validRunLine}\n\n  \n${validOutcomeLine}\n\n`);

      const records = await ExecutionHistoryReader.parseJsonlFile("/test/history/2026-02-13.jsonl");

      expect(records).toHaveLength(2);
    });

    // ─── mtime-keyed parse cache (Issue: parseJsonlFile still top CPU hog) ─
    describe("file-level parse cache", () => {
      it("reuses parsed results when mtime+size are unchanged", async () => {
        vi.mocked(fs.stat).mockResolvedValue({
          mtimeMs: 1_000_000,
          size: validRunLine.length,
          isFile: () => true,
        } as any);
        vi.mocked(fs.readFile).mockResolvedValue(`${validRunLine}\n`);

        const first = await ExecutionHistoryReader.parseJsonlFile("/test/history/cache.jsonl");
        const second = await ExecutionHistoryReader.parseJsonlFile("/test/history/cache.jsonl");

        expect(first).toHaveLength(1);
        expect(second).toHaveLength(1);
        // readFile must only run once — second call hits the cache.
        expect(fs.readFile).toHaveBeenCalledTimes(1);
      });

      it("re-parses when mtime changes (file has been appended to)", async () => {
        vi.mocked(fs.readFile)
          .mockResolvedValueOnce(`${validRunLine}\n`)
          .mockResolvedValueOnce(`${validRunLine}\n${validOutcomeLine}\n`);
        vi.mocked(fs.stat)
          .mockResolvedValueOnce({
            mtimeMs: 1_000_000,
            size: 100,
            isFile: () => true,
          } as any)
          .mockResolvedValueOnce({
            mtimeMs: 2_000_000, // bumped by the "write"
            size: 200,
            isFile: () => true,
          } as any);

        const first = await ExecutionHistoryReader.parseJsonlFile("/test/history/append.jsonl");
        const second = await ExecutionHistoryReader.parseJsonlFile("/test/history/append.jsonl");

        expect(first).toHaveLength(1);
        expect(second).toHaveLength(2);
        expect(fs.readFile).toHaveBeenCalledTimes(2);
      });

      it("re-parses when size changes even if mtime matches", async () => {
        // Defense-in-depth — filesystems with coarse mtime resolution can
        // report the same mtime for two rapid writes.
        vi.mocked(fs.readFile)
          .mockResolvedValueOnce(`${validRunLine}\n`)
          .mockResolvedValueOnce(`${validRunLine}\n${validOutcomeLine}\n`);
        vi.mocked(fs.stat)
          .mockResolvedValueOnce({ mtimeMs: 1, size: 100, isFile: () => true } as any)
          .mockResolvedValueOnce({ mtimeMs: 1, size: 200, isFile: () => true } as any);

        const first = await ExecutionHistoryReader.parseJsonlFile("/test/history/rapid.jsonl");
        const second = await ExecutionHistoryReader.parseJsonlFile("/test/history/rapid.jsonl");

        expect(first).toHaveLength(1);
        expect(second).toHaveLength(2);
      });

      it("evicts the oldest entry when exceeding PARSE_CACHE_MAX_ENTRIES", async () => {
        vi.mocked(fs.readFile).mockResolvedValue(`${validRunLine}\n`);
        // Advance mtime per-path so each path lands a fresh cache entry.
        let counter = 0;
        vi.mocked(fs.stat).mockImplementation(
          async () =>
            ({
              mtimeMs: ++counter,
              size: 1,
              isFile: () => true,
            }) as any
        );

        // Populate 61 distinct paths (1 over the 60-entry cap).
        for (let i = 0; i < 61; i++) {
          await ExecutionHistoryReader.parseJsonlFile(`/test/history/evict-${i}.jsonl`);
        }
        // First inserted path should have been evicted — re-parsing it forces
        // another readFile.
        const readFileCountBefore = vi.mocked(fs.readFile).mock.calls.length;
        await ExecutionHistoryReader.parseJsonlFile("/test/history/evict-0.jsonl");
        const readFileCountAfter = vi.mocked(fs.readFile).mock.calls.length;
        expect(readFileCountAfter).toBe(readFileCountBefore + 1);
      });

      it("clearCache() drops parse cache along with readAll cache", async () => {
        vi.mocked(fs.stat).mockResolvedValue({
          mtimeMs: 1,
          size: 1,
          isFile: () => true,
        } as any);
        vi.mocked(fs.readFile).mockResolvedValue(`${validRunLine}\n`);

        await ExecutionHistoryReader.parseJsonlFile("/test/history/clear.jsonl");
        ExecutionHistoryReader.clearCache();
        await ExecutionHistoryReader.parseJsonlFile("/test/history/clear.jsonl");

        expect(fs.readFile).toHaveBeenCalledTimes(2);
      });
    });

    it("should normalize v1 run records to v2 shape", async () => {
      vi.mocked(fs.readFile).mockResolvedValue(validRunLine + "\n");

      const records = await ExecutionHistoryReader.parseJsonlFile("/test/history/2026-02-13.jsonl");

      expect(records).toHaveLength(1);
      const run = records[0];
      expect(run.record_type).toBe("run");
      if (run.record_type === "run") {
        expect(run.schema_version).toBe("2");
        expect(run.files).toEqual({ read_count: 0, written_count: 0 });
        expect(run.routing).toEqual({
          complexity_score: 0,
          path: "unknown",
          skip_stages: [],
        });
      }
    });

    it("should parse v2 run records as-is", async () => {
      const v2RunLine = JSON.stringify({
        schema_version: "2",
        record_type: "run",
        issue_number: 42,
        title: "Test issue",
        branch: "feat/42-test",
        base_branch: "main",
        execution_mode: "automatic",
        started_at: "2026-02-13T10:00:00.000Z",
        completed_at: "2026-02-13T10:30:00.000Z",
        total_duration_ms: 1800000,
        outcome: "complete",
        stages: {},
        tokens: {
          total_input: 10000,
          total_output: 5000,
          total_cache_read: 2000,
          total_cache_creation: 1000,
          estimated_cost_usd: 0.1,
        },
        files: { read_count: 15, written_count: 8 },
        routing: {
          complexity_score: 5,
          path: "standard",
          skip_stages: ["feature-validate"],
        },
        outcome_type: "productive",
        tool_calls: [{ tool: "Read", target: "src/index.ts" }],
        recorded_at: "2026-02-13T10:30:00.000Z",
      });
      vi.mocked(fs.readFile).mockResolvedValue(v2RunLine + "\n");

      const records = await ExecutionHistoryReader.parseJsonlFile("/test/history/2026-02-13.jsonl");

      expect(records).toHaveLength(1);
      const run = records[0];
      expect(run.record_type).toBe("run");
      if (run.record_type === "run") {
        expect(run.schema_version).toBe("2");
        expect(run.files).toEqual({ read_count: 15, written_count: 8 });
        expect(run.routing).toEqual({
          complexity_score: 5,
          path: "standard",
          skip_stages: ["feature-validate"],
        });
      }
    });

    it("should handle mixed v1 and v2 records", async () => {
      const v2RunLine = JSON.stringify({
        schema_version: "2",
        record_type: "run",
        issue_number: 43,
        title: "V2 issue",
        branch: "feat/43",
        base_branch: "main",
        execution_mode: "automatic",
        started_at: "2026-02-13T11:00:00.000Z",
        completed_at: "2026-02-13T11:30:00.000Z",
        total_duration_ms: 1800000,
        outcome: "complete",
        stages: {},
        tokens: {
          total_input: 10000,
          total_output: 5000,
          total_cache_read: 2000,
          total_cache_creation: 1000,
          estimated_cost_usd: 0.1,
        },
        files: { read_count: 10, written_count: 5 },
        routing: { complexity_score: 3, path: "standard", skip_stages: [] },
        recorded_at: "2026-02-13T11:30:00.000Z",
      });
      // validRunLine is v1, v2RunLine is v2, validOutcomeLine is v1 outcome
      vi.mocked(fs.readFile).mockResolvedValue(
        `${validRunLine}\n${v2RunLine}\n${validOutcomeLine}\n`
      );

      const records = await ExecutionHistoryReader.parseJsonlFile("/test/history/2026-02-13.jsonl");

      expect(records).toHaveLength(3);
      // First record: v1 run normalized to v2
      expect(records[0].record_type).toBe("run");
      if (records[0].record_type === "run") {
        expect(records[0].schema_version).toBe("2");
      }
      // Second record: v2 run as-is
      expect(records[1].record_type).toBe("run");
      if (records[1].record_type === "run") {
        expect(records[1].schema_version).toBe("2");
      }
      // Third record: outcome
      expect(records[2].record_type).toBe("outcome");
    });

    it("should normalize v1 run record with existing files/routing to v2", async () => {
      const v1WithOptionals = JSON.stringify({
        schema_version: "1",
        record_type: "run",
        issue_number: 42,
        title: "Test issue",
        branch: "feat/42-test",
        base_branch: "main",
        execution_mode: "automatic",
        started_at: "2026-02-13T10:00:00.000Z",
        completed_at: "2026-02-13T10:30:00.000Z",
        total_duration_ms: 1800000,
        outcome: "complete",
        stages: {},
        tokens: {
          total_input: 10000,
          total_output: 5000,
          total_cache_read: 2000,
          total_cache_creation: 1000,
          estimated_cost_usd: 0.1,
        },
        files: { read_count: 20, written_count: 10 },
        routing: {
          complexity_score: 7,
          path: "complex",
          skip_stages: ["feature-validate"],
        },
        recorded_at: "2026-02-13T10:30:00.000Z",
      });
      vi.mocked(fs.readFile).mockResolvedValue(v1WithOptionals + "\n");

      const records = await ExecutionHistoryReader.parseJsonlFile("/test/history/2026-02-13.jsonl");

      expect(records).toHaveLength(1);
      const run = records[0];
      if (run.record_type === "run") {
        expect(run.schema_version).toBe("2");
        // Preserves existing values from v1
        expect(run.files).toEqual({ read_count: 20, written_count: 10 });
        expect(run.routing).toEqual({
          complexity_score: 7,
          path: "complex",
          skip_stages: ["feature-validate"],
        });
      }
    });
  });

  describe("listHistoryFiles()", () => {
    it("should list .jsonl files sorted by date ascending", async () => {
      vi.mocked(fs.readdir).mockResolvedValue([
        "2026-02-13.jsonl",
        "2026-02-10.jsonl",
        ".gitkeep",
        "2026-02-11.jsonl",
      ] as any);

      const files = await ExecutionHistoryReader.listHistoryFiles(workspaceRoot);

      expect(files).toEqual(["2026-02-10.jsonl", "2026-02-11.jsonl", "2026-02-13.jsonl"]);
    });

    it("should return empty array for missing directory", async () => {
      vi.mocked(fs.readdir).mockRejectedValue(
        Object.assign(new Error("ENOENT"), { code: "ENOENT" })
      );

      const files = await ExecutionHistoryReader.listHistoryFiles(workspaceRoot);
      expect(files).toEqual([]);
    });
  });

  describe("readAll()", () => {
    it("should read all history files and return sorted records", async () => {
      vi.mocked(fs.readdir).mockResolvedValue(["2026-02-10.jsonl", "2026-02-13.jsonl"] as any);

      const earlyRecord = JSON.stringify({
        schema_version: "1",
        record_type: "run",
        issue_number: 40,
        title: "Earlier issue",
        branch: "feat/40",
        base_branch: "main",
        execution_mode: "automatic",
        started_at: "2026-02-10T10:00:00.000Z",
        completed_at: "2026-02-10T10:30:00.000Z",
        total_duration_ms: 1800000,
        outcome: "complete",
        stages: {
          "issue-pickup": { status: "complete" },
          "feature-dev": { status: "complete" },
        },
        tokens: {
          total_input: 5000,
          total_output: 2000,
          total_cache_read: 0,
          total_cache_creation: 0,
          estimated_cost_usd: 0.05,
        },
        recorded_at: "2026-02-10T10:30:00.000Z",
      });

      vi.mocked(fs.readFile).mockImplementation(async (filePath) => {
        const p = String(filePath);
        if (p.includes("2026-02-10")) return earlyRecord + "\n";
        if (p.includes("2026-02-13")) return validRunLine + "\n";
        throw new Error("ENOENT");
      });

      const records = await ExecutionHistoryReader.readAll(workspaceRoot);

      expect(records).toHaveLength(2);
      expect(records[0].issue_number).toBe(40); // Earlier date first
      expect(records[1].issue_number).toBe(42);
    });
  });

  describe("readDateRange()", () => {
    it("should read records within the specified date range", async () => {
      vi.mocked(fs.readFile).mockImplementation(async (filePath) => {
        const p = String(filePath);
        if (p.includes("2026-02-12")) return validRunLine + "\n";
        if (p.includes("2026-02-13")) return validOutcomeLine + "\n";
        throw new Error("ENOENT");
      });

      const records = await ExecutionHistoryReader.readDateRange(
        workspaceRoot,
        new Date("2026-02-12"),
        new Date("2026-02-13")
      );

      expect(records).toHaveLength(2);
    });

    it("should handle single-day range", async () => {
      vi.mocked(fs.readFile).mockImplementation(async (filePath) => {
        if (String(filePath).includes("2026-02-13")) return validRunLine + "\n";
        throw new Error("ENOENT");
      });

      const records = await ExecutionHistoryReader.readDateRange(
        workspaceRoot,
        new Date("2026-02-13"),
        new Date("2026-02-13")
      );

      expect(records).toHaveLength(1);
    });

    it("should return empty array for ranges with no files", async () => {
      vi.mocked(fs.readFile).mockRejectedValue(new Error("ENOENT"));

      const records = await ExecutionHistoryReader.readDateRange(
        workspaceRoot,
        new Date("2025-01-01"),
        new Date("2025-01-03")
      );

      expect(records).toHaveLength(0);
    });
  });

  describe("getCostByIssue()", () => {
    // Helper: build a minimal v2 run JSONL line
    function makeV2RunLine(
      overrides: {
        issue_number?: number;
        estimated_cost_usd?: number;
        is_recovery?: boolean;
        type?: string | null;
        size?: string | null;
        recorded_at?: string;
      } = {}
    ): string {
      return JSON.stringify({
        schema_version: "2",
        record_type: "run",
        issue_number: overrides.issue_number ?? 42,
        title: "Test issue",
        branch: "feat/42-test",
        base_branch: "main",
        execution_mode: "automatic",
        started_at: overrides.recorded_at ?? "2026-02-13T10:00:00.000Z",
        completed_at: overrides.recorded_at ?? "2026-02-13T10:30:00.000Z",
        total_duration_ms: 1800000,
        outcome: "complete",
        stages: {},
        tokens: {
          total_input: 10000,
          total_output: 5000,
          total_cache_read: 2000,
          total_cache_creation: 1000,
          estimated_cost_usd: overrides.estimated_cost_usd ?? 0.1,
        },
        files: { read_count: 5, written_count: 3 },
        routing: { complexity_score: 3, path: "standard", skip_stages: [] },
        is_recovery: overrides.is_recovery ?? false,
        type: overrides.type !== undefined ? overrides.type : "feature",
        size: overrides.size !== undefined ? overrides.size : "M",
        recorded_at: overrides.recorded_at ?? "2026-02-13T10:30:00.000Z",
      });
    }

    it("should return aggregation for single issue with one run", async () => {
      vi.mocked(fs.readdir).mockResolvedValue(["2026-02-13.jsonl"] as any);
      vi.mocked(fs.readFile).mockResolvedValue(makeV2RunLine() + "\n");

      const result = await ExecutionHistoryReader.getCostByIssue(workspaceRoot);

      expect(result).toHaveLength(1);
      expect(result[0].issueNumber).toBe(42);
      expect(result[0].totalCostUsd).toBeCloseTo(0.1);
      expect(result[0].runCount).toBe(1);
      expect(result[0].backtrackCount).toBe(0);
      expect(result[0].issueType).toBe("feature");
      expect(result[0].sizeLabel).toBe("M");
    });

    it("should sum costs across multiple runs for the same issue", async () => {
      const line1 = makeV2RunLine({
        estimated_cost_usd: 0.1,
        recorded_at: "2026-02-13T10:00:00.000Z",
      });
      const line2 = makeV2RunLine({
        estimated_cost_usd: 0.25,
        recorded_at: "2026-02-13T12:00:00.000Z",
      });
      vi.mocked(fs.readdir).mockResolvedValue(["2026-02-13.jsonl"] as any);
      vi.mocked(fs.readFile).mockResolvedValue(`${line1}\n${line2}\n`);

      const result = await ExecutionHistoryReader.getCostByIssue(workspaceRoot);

      expect(result).toHaveLength(1);
      expect(result[0].totalCostUsd).toBeCloseTo(0.35);
      expect(result[0].runCount).toBe(2);
    });

    it("should count backtrackCount for is_recovery runs", async () => {
      const normal = makeV2RunLine({
        is_recovery: false,
        recorded_at: "2026-02-13T10:00:00.000Z",
      });
      const recovery = makeV2RunLine({
        is_recovery: true,
        recorded_at: "2026-02-13T11:00:00.000Z",
      });
      vi.mocked(fs.readdir).mockResolvedValue(["2026-02-13.jsonl"] as any);
      vi.mocked(fs.readFile).mockResolvedValue(`${normal}\n${recovery}\n`);

      const result = await ExecutionHistoryReader.getCostByIssue(workspaceRoot);

      expect(result[0].backtrackCount).toBe(1);
      expect(result[0].runCount).toBe(2);
    });

    it("should filter by issueType", async () => {
      const bug = makeV2RunLine({ issue_number: 10, type: "bug" });
      const feature = makeV2RunLine({ issue_number: 20, type: "feature" });
      vi.mocked(fs.readdir).mockResolvedValue(["2026-02-13.jsonl"] as any);
      vi.mocked(fs.readFile).mockResolvedValue(`${bug}\n${feature}\n`);

      const result = await ExecutionHistoryReader.getCostByIssue(workspaceRoot, 20, {
        issueType: "bug",
      });

      expect(result).toHaveLength(1);
      expect(result[0].issueNumber).toBe(10);
    });

    it("should filter by sizeLabel", async () => {
      const small = makeV2RunLine({ issue_number: 10, size: "S" });
      const large = makeV2RunLine({ issue_number: 20, size: "L" });
      vi.mocked(fs.readdir).mockResolvedValue(["2026-02-13.jsonl"] as any);
      vi.mocked(fs.readFile).mockResolvedValue(`${small}\n${large}\n`);

      const result = await ExecutionHistoryReader.getCostByIssue(workspaceRoot, 20, {
        sizeLabel: "L",
      });

      expect(result).toHaveLength(1);
      expect(result[0].issueNumber).toBe(20);
    });

    it("should cap results at limit", async () => {
      const lines = Array.from({ length: 5 }, (_, i) =>
        makeV2RunLine({
          issue_number: i + 1,
          recorded_at: `2026-02-1${i + 3}T10:00:00.000Z`,
        })
      ).join("\n");
      vi.mocked(fs.readdir).mockResolvedValue(["2026-02-13.jsonl"] as any);
      vi.mocked(fs.readFile).mockResolvedValue(lines + "\n");

      const result = await ExecutionHistoryReader.getCostByIssue(workspaceRoot, 3);

      expect(result).toHaveLength(3);
    });

    it("should return [] and not throw when readAll throws", async () => {
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
      // Spy on readAll so the outer try/catch in getCostByIssue fires
      const readAllSpy = vi
        .spyOn(ExecutionHistoryReader, "readAll")
        .mockRejectedValue(new Error("disk error"));

      const result = await ExecutionHistoryReader.getCostByIssue(workspaceRoot);

      expect(result).toEqual([]);
      expect(warnSpy).toHaveBeenCalledWith(
        expect.stringContaining("getCostByIssue aggregation failed"),
        expect.any(Error)
      );
      warnSpy.mockRestore();
      readAllSpy.mockRestore();
    });

    it("should exclude issues with totalCostUsd === 0", async () => {
      const zeroCost = makeV2RunLine({
        issue_number: 99,
        estimated_cost_usd: 0,
      });
      const withCost = makeV2RunLine({
        issue_number: 42,
        estimated_cost_usd: 0.1,
      });
      vi.mocked(fs.readdir).mockResolvedValue(["2026-02-13.jsonl"] as any);
      vi.mocked(fs.readFile).mockResolvedValue(`${zeroCost}\n${withCost}\n`);

      const result = await ExecutionHistoryReader.getCostByIssue(workspaceRoot);

      expect(result).toHaveLength(1);
      expect(result[0].issueNumber).toBe(42);
    });

    it("should sort by lastRunAt descending", async () => {
      const older = makeV2RunLine({
        issue_number: 1,
        recorded_at: "2026-02-10T10:00:00.000Z",
      });
      const newer = makeV2RunLine({
        issue_number: 2,
        recorded_at: "2026-02-14T10:00:00.000Z",
      });
      vi.mocked(fs.readdir).mockResolvedValue(["2026-02-13.jsonl"] as any);
      vi.mocked(fs.readFile).mockResolvedValue(`${older}\n${newer}\n`);

      const result = await ExecutionHistoryReader.getCostByIssue(workspaceRoot);

      expect(result[0].issueNumber).toBe(2); // newer first
      expect(result[1].issueNumber).toBe(1);
    });
  });

  describe("readForIssue()", () => {
    it("should filter records by issue number", async () => {
      const issue43Line = JSON.stringify({
        schema_version: "1",
        record_type: "run",
        issue_number: 43,
        title: "Other issue",
        branch: "feat/43",
        base_branch: "main",
        execution_mode: "automatic",
        started_at: "2026-02-13T11:00:00.000Z",
        completed_at: "2026-02-13T11:30:00.000Z",
        total_duration_ms: 1800000,
        outcome: "complete",
        stages: {},
        tokens: {
          total_input: 0,
          total_output: 0,
          total_cache_read: 0,
          total_cache_creation: 0,
          estimated_cost_usd: 0,
        },
        recorded_at: "2026-02-13T11:30:00.000Z",
      });

      vi.mocked(fs.readdir).mockResolvedValue(["2026-02-13.jsonl"] as any);
      vi.mocked(fs.readFile).mockResolvedValue(
        `${validRunLine}\n${issue43Line}\n${validOutcomeLine}\n`
      );

      const records = await ExecutionHistoryReader.readForIssue(workspaceRoot, 42);

      expect(records).toHaveLength(2); // run + outcome for issue 42
      expect(records.every((r) => r.issue_number === 42)).toBe(true);
    });
  });
});
