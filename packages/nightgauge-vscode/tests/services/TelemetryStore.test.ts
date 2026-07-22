/**
 * TelemetryStore.test.ts
 *
 * Unit tests for TelemetryStore service — JSONL-based history with
 * lightweight index for fast dashboard startup.
 *
 * @see Issue #1007 - Make JSONL the canonical data source
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import { TelemetryStore, isGhostEntry } from "../../src/services/TelemetryStore";
import { ExecutionHistoryReader } from "../../src/utils/executionHistoryReader";
import { ExecutionHistoryWriter } from "../../src/utils/executionHistoryWriter";
import type { HistoryIndex } from "../../src/utils/executionHistoryWriter";

// Mock node:fs/promises
vi.mock("node:fs/promises");

describe("TelemetryStore", () => {
  const workspaceRoot = "/test/workspace";
  const historyDir = `${workspaceRoot}/.nightgauge/pipeline/history`;
  const indexPath = `${historyDir}/index.json`;

  let store: TelemetryStore;

  /** Build a valid index for tests */
  function buildIndex(entries: HistoryIndex["entries"] = []): HistoryIndex {
    return {
      schema_version: "1",
      updated_at: new Date().toISOString(),
      total_runs: entries.length,
      entries,
    };
  }

  /** Build a valid JSONL run record line */
  function buildRunRecordLine(overrides: Record<string, unknown> = {}): string {
    return JSON.stringify({
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
      stages: {
        "issue-pickup": { status: "complete" },
        "feature-planning": { status: "complete" },
        "feature-dev": { status: "complete" },
        "feature-validate": { status: "complete" },
        "pr-create": { status: "complete" },
        "pr-merge": { status: "complete" },
      },
      tokens: {
        total_input: 10000,
        total_output: 5000,
        total_cache_read: 2000,
        total_cache_creation: 1000,
        estimated_cost_usd: 0.5,
      },
      files: { read_count: 10, written_count: 5 },
      routing: { complexity_score: 3, path: "standard", skip_stages: [] },
      recorded_at: "2026-02-13T10:30:01.000Z",
      ...overrides,
    });
  }

  beforeEach(() => {
    vi.clearAllMocks();
    ExecutionHistoryReader.clearCache();
    store = new TelemetryStore(workspaceRoot);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("getIndex()", () => {
    it("should load index from disk when available", async () => {
      const index = buildIndex([
        {
          issue_number: 42,
          title: "Test issue",
          outcome: "complete",
          cost_usd: 0.5,
          duration_ms: 1800000,
          stage_count: 6,
          started_at: "2026-02-13T10:00:00.000Z",
          recorded_at: "2026-02-13T10:30:01.000Z",
          branch: "feat/42-test",
        },
      ]);

      vi.mocked(fs.readFile).mockResolvedValue(JSON.stringify(index));
      // readdir returns files older than index (not stale)
      vi.mocked(fs.readdir).mockResolvedValue(["2026-02-12.jsonl"] as any);
      vi.mocked(fs.stat).mockResolvedValue({
        mtimeMs: new Date(index.updated_at).getTime() - 1000,
      } as any);

      const result = await store.getIndex();

      expect(result.total_runs).toBe(1);
      expect(result.entries[0].issue_number).toBe(42);
    });

    it("should rebuild index when file is missing", async () => {
      // Index read fails
      vi.mocked(fs.readFile).mockRejectedValueOnce(new Error("ENOENT"));
      // readAll: readdir returns a JSONL file
      vi.mocked(fs.readdir).mockResolvedValue(["2026-02-13.jsonl"] as any);
      // parseJsonlFile reads the content
      vi.mocked(fs.readFile).mockResolvedValueOnce(buildRunRecordLine() + "\n");
      // writeIndex: mkdir, writeFile, rename
      vi.mocked(fs.mkdir).mockResolvedValue(undefined);
      vi.mocked(fs.writeFile).mockResolvedValue(undefined);
      vi.mocked(fs.rename).mockResolvedValue(undefined);

      const result = await store.getIndex();

      expect(result.total_runs).toBe(1);
      expect(result.entries[0].issue_number).toBe(42);
      expect(fs.writeFile).toHaveBeenCalled();
    });

    it("should rebuild index when JSON is corrupt", async () => {
      vi.mocked(fs.readFile).mockResolvedValueOnce("not valid json");
      vi.mocked(fs.readdir).mockResolvedValue([] as any);
      vi.mocked(fs.mkdir).mockResolvedValue(undefined);
      vi.mocked(fs.writeFile).mockResolvedValue(undefined);
      vi.mocked(fs.rename).mockResolvedValue(undefined);

      const result = await store.getIndex();

      expect(result.total_runs).toBe(0);
      expect(result.entries).toEqual([]);
    });

    it("should cache index on subsequent calls", async () => {
      const index = buildIndex([]);
      vi.mocked(fs.readFile).mockResolvedValue(JSON.stringify(index));
      vi.mocked(fs.readdir).mockResolvedValue([] as any);
      vi.mocked(fs.stat).mockResolvedValue({
        mtimeMs: new Date(index.updated_at).getTime() - 1000,
      } as any);

      await store.getIndex();
      await store.getIndex();

      // readFile should only be called once (cached)
      expect(fs.readFile).toHaveBeenCalledTimes(1);
    });

    it("should rebuild when index has fewer entries than JSONL files", async () => {
      // Index has 1 entry but there are 3 JSONL files — clearly incomplete
      const index = buildIndex([
        {
          issue_number: 42,
          title: "Test issue",
          outcome: "complete",
          cost_usd: 0.5,
          duration_ms: 1800000,
          stage_count: 6,
          started_at: "2026-02-13T10:00:00.000Z",
          recorded_at: "2026-02-13T10:30:01.000Z",
          branch: "feat/42-test",
        },
      ]);

      vi.mocked(fs.readFile).mockResolvedValueOnce(JSON.stringify(index));
      // 3 JSONL files but only 1 index entry — stale
      vi.mocked(fs.readdir).mockResolvedValue([
        "2026-02-12.jsonl",
        "2026-02-13.jsonl",
        "2026-02-14.jsonl",
      ] as any);
      vi.mocked(fs.stat).mockResolvedValue({
        mtimeMs: new Date(index.updated_at).getTime() - 1000, // Not time-stale
      } as any);

      // For rebuild: readAll reads all JSONL files
      const record1 = buildRunRecordLine({
        issue_number: 41,
        started_at: "2026-02-12T10:00:00.000Z",
        recorded_at: "2026-02-12T10:30:00.000Z",
      });
      const record2 = buildRunRecordLine({
        issue_number: 42,
        started_at: "2026-02-13T10:00:00.000Z",
        recorded_at: "2026-02-13T10:30:00.000Z",
      });
      const record3 = buildRunRecordLine({
        issue_number: 43,
        started_at: "2026-02-14T10:00:00.000Z",
        recorded_at: "2026-02-14T10:30:00.000Z",
      });
      vi.mocked(fs.readFile)
        .mockResolvedValueOnce(record1 + "\n")
        .mockResolvedValueOnce(record2 + "\n")
        .mockResolvedValueOnce(record3 + "\n");
      vi.mocked(fs.mkdir).mockResolvedValue(undefined);
      vi.mocked(fs.writeFile).mockResolvedValue(undefined);
      vi.mocked(fs.rename).mockResolvedValue(undefined);

      const result = await store.getIndex();

      // Should have rebuilt with all 3 entries
      expect(result.entries.length).toBe(3);
    });

    it("should rebuild when index is stale (JSONL newer than index)", async () => {
      const index = buildIndex([]);
      vi.mocked(fs.readFile).mockResolvedValueOnce(JSON.stringify(index));
      // JSONL file is newer than index
      vi.mocked(fs.readdir).mockResolvedValue(["2026-02-15.jsonl"] as any);
      vi.mocked(fs.stat).mockResolvedValue({
        mtimeMs: new Date(index.updated_at).getTime() + 60000,
      } as any);

      // For rebuild: readAll calls readdir again then reads JSONL
      vi.mocked(fs.readFile).mockResolvedValueOnce(
        buildRunRecordLine({
          issue_number: 99,
          started_at: "2026-02-15T10:00:00.000Z",
          recorded_at: "2026-02-15T10:30:00.000Z",
        }) + "\n"
      );
      vi.mocked(fs.mkdir).mockResolvedValue(undefined);
      vi.mocked(fs.writeFile).mockResolvedValue(undefined);
      vi.mocked(fs.rename).mockResolvedValue(undefined);

      const result = await store.getIndex();

      expect(result.entries[0].issue_number).toBe(99);
    });
  });

  describe("rebuildIndex()", () => {
    it("should rebuild from multiple JSONL files", async () => {
      vi.mocked(fs.readdir).mockResolvedValue(["2026-02-12.jsonl", "2026-02-13.jsonl"] as any);

      const record1 = buildRunRecordLine({
        issue_number: 41,
        started_at: "2026-02-12T10:00:00.000Z",
        recorded_at: "2026-02-12T10:30:00.000Z",
      });
      const record2 = buildRunRecordLine({
        issue_number: 42,
        started_at: "2026-02-13T10:00:00.000Z",
        recorded_at: "2026-02-13T10:30:00.000Z",
      });

      vi.mocked(fs.readFile)
        .mockResolvedValueOnce(record1 + "\n")
        .mockResolvedValueOnce(record2 + "\n");
      vi.mocked(fs.mkdir).mockResolvedValue(undefined);
      vi.mocked(fs.writeFile).mockResolvedValue(undefined);
      vi.mocked(fs.rename).mockResolvedValue(undefined);

      const result = await store.rebuildIndex();

      expect(result.total_runs).toBe(2);
      // Most recent first
      expect(result.entries[0].issue_number).toBe(42);
      expect(result.entries[1].issue_number).toBe(41);
    });

    it("should write index atomically", async () => {
      vi.mocked(fs.readdir).mockResolvedValue([] as any);
      vi.mocked(fs.mkdir).mockResolvedValue(undefined);
      vi.mocked(fs.writeFile).mockResolvedValue(undefined);
      vi.mocked(fs.rename).mockResolvedValue(undefined);

      await store.rebuildIndex();

      // Should write to temp file first, then rename
      expect(fs.writeFile).toHaveBeenCalledWith(indexPath + ".tmp", expect.any(String), "utf-8");
      expect(fs.rename).toHaveBeenCalledWith(indexPath + ".tmp", indexPath);
    });
  });

  describe("getAllRunSummaries()", () => {
    it("should return entries from the index", async () => {
      const entries = [
        {
          issue_number: 42,
          title: "Test",
          outcome: "complete" as const,
          cost_usd: 0.5,
          duration_ms: 1800000,
          stage_count: 6,
          started_at: "2026-02-13T10:00:00.000Z",
          recorded_at: "2026-02-13T10:30:00.000Z",
          branch: "feat/42",
        },
      ];
      const index = buildIndex(entries);
      vi.mocked(fs.readFile).mockResolvedValue(JSON.stringify(index));
      vi.mocked(fs.readdir).mockResolvedValue([] as any);
      vi.mocked(fs.stat).mockResolvedValue({
        mtimeMs: new Date(index.updated_at).getTime() - 1000,
      } as any);

      const result = await store.getAllRunSummaries();

      expect(result).toHaveLength(1);
      expect(result[0].issue_number).toBe(42);
    });
  });

  describe("getRunSummariesPage()", () => {
    it("should return paginated entries", async () => {
      const entries = Array.from({ length: 5 }, (_, i) => ({
        issue_number: i + 1,
        title: `Issue ${i + 1}`,
        outcome: "complete" as const,
        cost_usd: 0.5,
        duration_ms: 1000,
        stage_count: 6,
        started_at: `2026-02-1${i}T10:00:00.000Z`,
        recorded_at: `2026-02-1${i}T10:30:00.000Z`,
        branch: `feat/${i + 1}`,
      }));
      const index = buildIndex(entries);
      vi.mocked(fs.readFile).mockResolvedValue(JSON.stringify(index));
      vi.mocked(fs.readdir).mockResolvedValue([] as any);
      vi.mocked(fs.stat).mockResolvedValue({
        mtimeMs: new Date(index.updated_at).getTime() - 1000,
      } as any);

      const page1 = await store.getRunSummariesPage(0, 2);
      expect(page1.items).toHaveLength(2);
      expect(page1.total).toBe(5);
      expect(page1.hasMore).toBe(true);

      const page3 = await store.getRunSummariesPage(4, 2);
      expect(page3.items).toHaveLength(1);
      expect(page3.hasMore).toBe(false);
    });
  });

  describe("getRunRecord()", () => {
    it("should load full run record from JSONL for a specific issue", async () => {
      const record = buildRunRecordLine({ issue_number: 42 });
      // readForIssue calls readAll which calls readdir then reads each file
      vi.mocked(fs.readdir).mockResolvedValue(["2026-02-13.jsonl"] as any);
      vi.mocked(fs.readFile).mockResolvedValue(record + "\n");

      const result = await store.getRunRecord(42);

      expect(result).toBeDefined();
      expect(result?.issue_number).toBe(42);
    });

    it("should return undefined when no record exists for issue", async () => {
      vi.mocked(fs.readdir).mockResolvedValue([] as any);

      const result = await store.getRunRecord(999);

      expect(result).toBeUndefined();
    });
  });

  describe("appendRunRecord()", () => {
    it("should delegate to ExecutionHistoryWriter and invalidate cache", async () => {
      const appendSpy = vi.spyOn(ExecutionHistoryWriter, "appendRecord").mockResolvedValue(true);

      const record = JSON.parse(buildRunRecordLine());

      // Prime the cache
      const index = buildIndex([]);
      vi.mocked(fs.readFile).mockResolvedValueOnce(JSON.stringify(index));
      vi.mocked(fs.readdir).mockResolvedValue([] as any);
      vi.mocked(fs.stat).mockResolvedValue({
        mtimeMs: new Date(index.updated_at).getTime() - 1000,
      } as any);
      await store.getIndex();

      // Append should invalidate cache
      await store.appendRunRecord(record);

      expect(appendSpy).toHaveBeenCalledWith(workspaceRoot, record);

      // Next getIndex call should re-read from disk (cache invalidated)
      vi.mocked(fs.readFile).mockResolvedValueOnce(JSON.stringify(index));
      await store.getIndex();
      // readFile called: once for initial index + once for re-read after invalidation
      expect(fs.readFile).toHaveBeenCalledTimes(2);
    });

    it("inherits ExecutionHistoryWriter's #307 identity guard — it is not a raw append (Issue #319 audit)", async () => {
      // TelemetryStore.appendRunRecord() must enforce the same
      // idempotency/identity contract as ExecutionHistoryWriter.appendRecord
      // (post-#316): a run record with a present-but-empty repo/run_id is
      // proof of the cross-contamination bug and must never be written,
      // regardless of which entry point it comes in through. This exercises
      // the REAL appendRecord (no spy) to prove the guard fires through the
      // delegation, not just when called directly.
      const record = JSON.parse(buildRunRecordLine({ repo: null, run_id: null }));

      const written = await store.appendRunRecord(record);

      expect(written).toBe(false);
      expect(fs.appendFile).not.toHaveBeenCalled();
    });
  });

  describe("cleanupOldFiles()", () => {
    it("should delegate to ExecutionHistoryWriter", async () => {
      const cleanupSpy = vi
        .spyOn(ExecutionHistoryWriter, "cleanupOldFiles")
        .mockResolvedValue({ deleted: ["2026-01-01.jsonl"] });

      const result = await store.cleanupOldFiles(30);

      expect(cleanupSpy).toHaveBeenCalledWith(workspaceRoot, 30);
      expect(result.deleted).toHaveLength(1);
    });

    it("should invalidate cache when files are deleted", async () => {
      vi.spyOn(ExecutionHistoryWriter, "cleanupOldFiles").mockResolvedValue({
        deleted: ["old.jsonl"],
      });

      // Prime cache
      const index = buildIndex([]);
      vi.mocked(fs.readFile).mockResolvedValueOnce(JSON.stringify(index));
      vi.mocked(fs.readdir).mockResolvedValue([] as any);
      vi.mocked(fs.stat).mockResolvedValue({
        mtimeMs: new Date(index.updated_at).getTime() - 1000,
      } as any);
      await store.getIndex();

      await store.cleanupOldFiles();

      // Cache should be invalidated — next read goes to disk
      vi.mocked(fs.readFile).mockResolvedValueOnce(JSON.stringify(index));
      await store.getIndex();
      expect(fs.readFile).toHaveBeenCalledTimes(2);
    });
  });

  describe("invalidateCache()", () => {
    it("should force next getIndex to reload from disk", async () => {
      const index = buildIndex([]);
      vi.mocked(fs.readFile).mockResolvedValue(JSON.stringify(index));
      vi.mocked(fs.readdir).mockResolvedValue([] as any);
      vi.mocked(fs.stat).mockResolvedValue({
        mtimeMs: new Date(index.updated_at).getTime() - 1000,
      } as any);

      await store.getIndex();
      store.invalidateCache();
      await store.getIndex();

      expect(fs.readFile).toHaveBeenCalledTimes(2);
    });
  });
});

describe("ExecutionHistoryWriter.buildIndexEntry()", () => {
  it("should extract correct fields from a v2 run record", () => {
    const record = {
      schema_version: "2" as const,
      record_type: "run" as const,
      issue_number: 42,
      title: "Test issue",
      branch: "feat/42-test",
      base_branch: "main",
      execution_mode: "automatic" as const,
      started_at: "2026-02-13T10:00:00.000Z",
      completed_at: "2026-02-13T10:30:00.000Z",
      total_duration_ms: 1800000,
      outcome: "complete" as const,
      labels: ["type:feature", "size:M"],
      size: "M",
      type: "feature",
      stages: {
        "issue-pickup": { status: "complete" as const },
        "feature-planning": { status: "complete" as const },
        "feature-dev": { status: "complete" as const },
        "feature-validate": { status: "skipped" as const },
        "pr-create": { status: "complete" as const },
        "pr-merge": { status: "complete" as const },
      },
      tokens: {
        total_input: 10000,
        total_output: 5000,
        total_cache_read: 2000,
        total_cache_creation: 1000,
        estimated_cost_usd: 0.5,
      },
      outcome_type: "productive" as const,
      files: { read_count: 10, written_count: 5 },
      routing: { complexity_score: 3, path: "standard", skip_stages: [] },
      recorded_at: "2026-02-13T10:30:01.000Z",
    };

    const entry = ExecutionHistoryWriter.buildIndexEntry(record);

    expect(entry.issue_number).toBe(42);
    expect(entry.title).toBe("Test issue");
    expect(entry.outcome).toBe("complete");
    expect(entry.outcome_type).toBe("productive");
    expect(entry.cost_usd).toBe(0.5);
    expect(entry.duration_ms).toBe(1800000);
    expect(entry.stage_count).toBe(6); // 5 complete + 1 skipped
    expect(entry.started_at).toBe("2026-02-13T10:00:00.000Z");
    expect(entry.recorded_at).toBe("2026-02-13T10:30:01.000Z");
    expect(entry.labels).toEqual(["type:feature", "size:M"]);
    expect(entry.size).toBe("M");
    expect(entry.type).toBe("feature");
    expect(entry.branch).toBe("feat/42-test");
  });

  it("should count only complete and skipped stages", () => {
    const record = {
      schema_version: "2" as const,
      record_type: "run" as const,
      issue_number: 43,
      title: "Partial run",
      branch: "feat/43",
      base_branch: "main",
      execution_mode: "automatic" as const,
      started_at: "2026-02-13T10:00:00.000Z",
      completed_at: "2026-02-13T10:15:00.000Z",
      total_duration_ms: 900000,
      outcome: "failed" as const,
      stages: {
        "issue-pickup": { status: "complete" as const },
        "feature-planning": { status: "complete" as const },
        "feature-dev": { status: "failed" as const },
        "feature-validate": { status: "pending" as const },
        "pr-create": { status: "pending" as const },
        "pr-merge": { status: "pending" as const },
      },
      tokens: {
        total_input: 5000,
        total_output: 2000,
        total_cache_read: 0,
        total_cache_creation: 0,
        estimated_cost_usd: 0.2,
      },
      files: { read_count: 0, written_count: 0 },
      routing: { complexity_score: 0, path: "unknown", skip_stages: [] },
      recorded_at: "2026-02-13T10:15:01.000Z",
    };

    const entry = ExecutionHistoryWriter.buildIndexEntry(record);

    expect(entry.stage_count).toBe(2); // Only complete stages
    expect(entry.outcome).toBe("failed");
  });
});

describe("isGhostEntry()", () => {
  it("should detect ghost orchestration entries (zero cost + zero tokens)", () => {
    const ghost = {
      issue_number: 48,
      title: "Issue #30",
      outcome: "complete" as const,
      cost_usd: 0,
      total_input_tokens: 0,
      total_output_tokens: 0,
      total_cache_read_tokens: 0,
      total_cache_creation_tokens: 0,
      duration_ms: 676799,
      stage_count: 3,
      started_at: "2026-03-19T05:45:58.559Z",
      recorded_at: "2026-03-19T11:57:15.358Z",
      branch: "",
    };

    expect(isGhostEntry(ghost)).toBe(true);
  });

  it("should NOT flag real entries with actual cost and tokens", () => {
    const real = {
      issue_number: 48,
      title: "Issue #48",
      outcome: "complete" as const,
      cost_usd: 10.84,
      total_input_tokens: 514,
      total_output_tokens: 99810,
      total_cache_read_tokens: 13453973,
      total_cache_creation_tokens: 499049,
      duration_ms: 3195406,
      stage_count: 8,
      started_at: "2026-03-19T05:45:58.559Z",
      recorded_at: "2026-03-19T12:39:13.965Z",
      branch: "feat/48",
    };

    expect(isGhostEntry(real)).toBe(false);
  });

  it("should NOT flag failed runs with cost but zero tokens", () => {
    const failedRun = {
      issue_number: 99,
      title: "Failed early",
      outcome: "failed" as const,
      cost_usd: 0.05,
      total_input_tokens: 0,
      total_output_tokens: 0,
      total_cache_read_tokens: 0,
      total_cache_creation_tokens: 0,
      duration_ms: 10000,
      stage_count: 2,
      started_at: "2026-03-19T10:00:00.000Z",
      recorded_at: "2026-03-19T10:00:10.000Z",
      branch: "feat/99",
    };

    expect(isGhostEntry(failedRun)).toBe(false);
  });

  it("should NOT flag entries with cache tokens but zero cost", () => {
    const cachedRun = {
      issue_number: 100,
      title: "Cached run",
      outcome: "complete" as const,
      cost_usd: 0,
      total_input_tokens: 0,
      total_output_tokens: 0,
      total_cache_read_tokens: 50000,
      total_cache_creation_tokens: 0,
      duration_ms: 30000,
      stage_count: 6,
      started_at: "2026-03-19T10:00:00.000Z",
      recorded_at: "2026-03-19T10:00:30.000Z",
      branch: "feat/100",
    };

    expect(isGhostEntry(cachedRun)).toBe(false);
  });

  it("should handle missing token fields gracefully", () => {
    const minimal = {
      issue_number: 101,
      title: "Minimal",
      outcome: "complete" as const,
      cost_usd: 0,
      duration_ms: 1000,
      stage_count: 3,
      started_at: "2026-03-19T10:00:00.000Z",
      recorded_at: "2026-03-19T10:00:01.000Z",
      branch: "",
    };

    // Missing token fields default to 0, so this is a ghost
    expect(isGhostEntry(minimal)).toBe(true);
  });
});

describe("TelemetryStore ghost filtering", () => {
  const workspaceRoot = "/test/workspace";
  let store: TelemetryStore;

  beforeEach(() => {
    vi.clearAllMocks();
    ExecutionHistoryReader.clearCache();
    store = new TelemetryStore(workspaceRoot);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("getAllRunSummaries should filter out ghost entries", async () => {
    const entries = [
      {
        issue_number: 48,
        title: "Issue #48",
        outcome: "complete" as const,
        cost_usd: 10.84,
        total_input_tokens: 514,
        total_output_tokens: 99810,
        total_cache_read_tokens: 13453973,
        total_cache_creation_tokens: 499049,
        duration_ms: 3195406,
        stage_count: 8,
        started_at: "2026-03-19T05:45:58.559Z",
        recorded_at: "2026-03-19T12:39:13.965Z",
        branch: "feat/48",
      },
      {
        issue_number: 48,
        title: "Issue #30",
        outcome: "complete" as const,
        cost_usd: 0,
        total_input_tokens: 0,
        total_output_tokens: 0,
        total_cache_read_tokens: 0,
        total_cache_creation_tokens: 0,
        duration_ms: 676799,
        stage_count: 3,
        started_at: "2026-03-19T05:45:58.559Z",
        recorded_at: "2026-03-19T11:57:15.358Z",
        branch: "",
      },
    ];
    const index: HistoryIndex = {
      schema_version: "1",
      updated_at: new Date().toISOString(),
      total_runs: entries.length,
      entries,
    };
    vi.mocked(fs.readFile).mockResolvedValue(JSON.stringify(index));
    vi.mocked(fs.readdir).mockResolvedValue([] as any);
    vi.mocked(fs.stat).mockResolvedValue({
      mtimeMs: new Date(index.updated_at).getTime() - 1000,
    } as any);

    const result = await store.getAllRunSummaries();

    expect(result).toHaveLength(1);
    expect(result[0].title).toBe("Issue #48");
    expect(result[0].cost_usd).toBe(10.84);
  });

  it("getRunSummariesPage should filter ghosts before pagination", async () => {
    const entries = [
      {
        issue_number: 48,
        title: "Real issue",
        outcome: "complete" as const,
        cost_usd: 5.0,
        total_input_tokens: 100,
        total_output_tokens: 500,
        duration_ms: 100000,
        stage_count: 8,
        started_at: "2026-03-19T10:00:00.000Z",
        recorded_at: "2026-03-19T10:30:00.000Z",
        branch: "feat/48",
      },
      {
        issue_number: 48,
        title: "Ghost",
        outcome: "complete" as const,
        cost_usd: 0,
        total_input_tokens: 0,
        total_output_tokens: 0,
        total_cache_read_tokens: 0,
        total_cache_creation_tokens: 0,
        duration_ms: 50000,
        stage_count: 3,
        started_at: "2026-03-19T10:00:00.000Z",
        recorded_at: "2026-03-19T10:10:00.000Z",
        branch: "",
      },
      {
        issue_number: 47,
        title: "Another real",
        outcome: "complete" as const,
        cost_usd: 3.0,
        total_input_tokens: 50,
        total_output_tokens: 200,
        duration_ms: 80000,
        stage_count: 8,
        started_at: "2026-03-19T09:00:00.000Z",
        recorded_at: "2026-03-19T09:30:00.000Z",
        branch: "feat/47",
      },
    ];
    const index: HistoryIndex = {
      schema_version: "1",
      updated_at: new Date().toISOString(),
      total_runs: entries.length,
      entries,
    };
    vi.mocked(fs.readFile).mockResolvedValue(JSON.stringify(index));
    vi.mocked(fs.readdir).mockResolvedValue([] as any);
    vi.mocked(fs.stat).mockResolvedValue({
      mtimeMs: new Date(index.updated_at).getTime() - 1000,
    } as any);

    const page = await store.getRunSummariesPage(0, 10);

    // Ghost filtered: 2 real entries, not 3
    expect(page.total).toBe(2);
    expect(page.items).toHaveLength(2);
    expect(page.items[0].title).toBe("Real issue");
    expect(page.items[1].title).toBe("Another real");
  });
});
