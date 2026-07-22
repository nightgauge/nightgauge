/**
 * focusLensImpact.test.ts
 *
 * Unit tests for focus lens impact measurement (Issue #2460).
 *
 * Covers:
 * - focus_lens_active field persisted in run records
 * - focus_lens_active included in index entries
 * - getFocusLensComparison() A/B comparison logic
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import {
  ExecutionHistoryWriter,
  type HistoryIndexEntry,
} from "../../src/utils/executionHistoryWriter";
import {
  ExecutionHistoryReader,
  type FocusLensImpactComparison,
} from "../../src/utils/executionHistoryReader";
import type { ExecutionHistoryRunRecordV2 } from "../../src/schemas/executionHistory";

// Mock node:fs/promises
vi.mock("node:fs/promises");

describe("Focus Lens Impact Measurement (#2460)", () => {
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
    ExecutionHistoryReader.clearCache();
    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    vi.mocked(fs.appendFile).mockResolvedValue();
    vi.mocked(fs.writeFile).mockResolvedValue();
    vi.mocked(fs.rename).mockResolvedValue();
    vi.mocked(fs.readFile).mockRejectedValue(
      Object.assign(new Error("ENOENT"), { code: "ENOENT" })
    );
    // Default fs.stat mock — the reader's mtime-keyed parse cache (added
    // for the UNRESPONSIVE-host fix) stats each file before readFile. Tests
    // that simulate "file exists" mock readFile with content; they also
    // need stat to resolve. Unique mtime per call so cache entries never
    // collide across tests.
    let mtimeCounter = 0;
    vi.mocked(fs.stat).mockImplementation(
      async () =>
        ({
          mtimeMs: ++mtimeCounter,
          size: 1,
          isFile: () => true,
        }) as any
    );
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  // Helper to create a minimal pipeline state
  function createState(overrides: { started_at?: string } = {}) {
    return {
      issue_number: 42,
      title: "Test issue",
      branch: "feat/42-test",
      base_branch: "main",
      execution_mode: "automatic" as const,
      started_at: overrides.started_at ?? "2026-03-01T10:00:00.000Z",
      stages: {
        "issue-pickup": {
          status: "complete",
          started_at: "2026-03-01T10:00:00.000Z",
          completed_at: "2026-03-01T10:05:00.000Z",
          duration_ms: 300000,
        },
        "feature-dev": {
          status: "complete",
          started_at: "2026-03-01T10:05:00.000Z",
          completed_at: "2026-03-01T10:20:00.000Z",
          duration_ms: 900000,
        },
      },
      tokens: {
        total_input: 10000,
        total_output: 5000,
        total_cache_read: 2000,
        total_cache_creation: 1000,
        estimated_cost_usd: 0.1,
      },
    };
  }

  describe("buildRunRecord() with focus_lens_active", () => {
    it("includes focus_lens_active when provided", () => {
      const record = ExecutionHistoryWriter.buildRunRecord(createState(), undefined, undefined, {
        files: { read_count: 0, written_count: 0 },
        routing: { complexity_score: 2, path: "trivial", skip_stages: [] },
        focus_lens_active: {
          lens: "quality",
          set_at: "2026-03-01T09:00:00.000Z",
          set_by: "cli",
        },
      });

      expect(record.focus_lens_active).toEqual({
        lens: "quality",
        set_at: "2026-03-01T09:00:00.000Z",
        set_by: "cli",
      });
    });

    it("omits focus_lens_active when not provided", () => {
      const record = ExecutionHistoryWriter.buildRunRecord(createState(), undefined, undefined, {
        files: { read_count: 0, written_count: 0 },
        routing: { complexity_score: 2, path: "trivial", skip_stages: [] },
      });

      expect(record.focus_lens_active).toBeUndefined();
    });

    it("persists all focus_lens_active subfields", () => {
      const record = ExecutionHistoryWriter.buildRunRecord(createState(), undefined, undefined, {
        files: { read_count: 0, written_count: 0 },
        routing: { complexity_score: 3, path: "standard", skip_stages: [] },
        focus_lens_active: {
          lens: "security",
          set_by: "vscode",
        },
      });

      expect(record.focus_lens_active?.lens).toBe("security");
      expect(record.focus_lens_active?.set_by).toBe("vscode");
      expect(record.focus_lens_active?.set_at).toBeUndefined();
    });
  });

  describe("buildIndexEntry() with focus_lens_active", () => {
    it("includes focus_lens_active lens name in index entry", () => {
      const record = ExecutionHistoryWriter.buildRunRecord(createState(), undefined, undefined, {
        files: { read_count: 0, written_count: 0 },
        routing: { complexity_score: 2, path: "trivial", skip_stages: [] },
        focus_lens_active: { lens: "features" },
      });

      const entry: HistoryIndexEntry = ExecutionHistoryWriter.buildIndexEntry(record);
      expect(entry.focus_lens_active).toBe("features");
    });

    it("omits focus_lens_active from index entry when no lens was active", () => {
      const record = ExecutionHistoryWriter.buildRunRecord(createState(), undefined, undefined, {
        files: { read_count: 0, written_count: 0 },
        routing: { complexity_score: 2, path: "trivial", skip_stages: [] },
      });

      const entry: HistoryIndexEntry = ExecutionHistoryWriter.buildIndexEntry(record);
      expect(entry.focus_lens_active).toBeUndefined();
    });
  });

  describe("getFocusLensComparison()", () => {
    function makeRunRecord(
      overrides: Partial<ExecutionHistoryRunRecordV2>
    ): ExecutionHistoryRunRecordV2 {
      return {
        schema_version: "2",
        record_type: "run",
        issue_number: 42,
        title: "Test",
        branch: "feat/42-test",
        base_branch: "main",
        execution_mode: "automatic",
        started_at: "2026-03-01T10:00:00.000Z",
        completed_at: "2026-03-01T10:30:00.000Z",
        total_duration_ms: 1800000,
        outcome: "complete",
        stages: {
          "issue-pickup": { status: "complete" },
          "feature-dev": { status: "complete" },
        },
        tokens: {
          total_input: 10000,
          total_output: 5000,
          total_cache_read: 2000,
          total_cache_creation: 1000,
          estimated_cost_usd: 0.1,
        },
        files: { read_count: 5, written_count: 2 },
        routing: { complexity_score: 2, path: "trivial", skip_stages: [] },
        recorded_at: "2026-03-01T10:30:00.000Z",
        ...overrides,
      };
    }

    it("returns null when fewer than 2 run records exist", async () => {
      vi.mocked(fs.readdir).mockResolvedValue(["2026-03-01.jsonl"] as unknown as any);
      const singleRecord = makeRunRecord({});
      vi.mocked(fs.readFile).mockResolvedValue(JSON.stringify(singleRecord) + "\n");

      const result = await ExecutionHistoryReader.getFocusLensComparison(workspaceRoot);
      expect(result).toBeNull();
    });

    it("returns null when history directory is empty", async () => {
      vi.mocked(fs.readdir).mockResolvedValue([] as unknown as any);

      const result = await ExecutionHistoryReader.getFocusLensComparison(workspaceRoot);
      expect(result).toBeNull();
    });

    it("correctly separates focused vs unfocused runs", async () => {
      const focusedRun = makeRunRecord({
        focus_lens_active: { lens: "quality" },
        tokens: { ...makeRunRecord({}).tokens, estimated_cost_usd: 0.2 },
        total_duration_ms: 2000000,
        recorded_at: "2026-03-01T10:30:00.000Z",
      });
      const unfocusedRun = makeRunRecord({
        tokens: { ...makeRunRecord({}).tokens, estimated_cost_usd: 0.1 },
        total_duration_ms: 1000000,
        recorded_at: "2026-03-01T11:00:00.000Z",
      });
      const generalRun = makeRunRecord({
        focus_lens_active: { lens: "general" },
        tokens: { ...makeRunRecord({}).tokens, estimated_cost_usd: 0.15 },
        total_duration_ms: 1500000,
        recorded_at: "2026-03-01T12:00:00.000Z",
      });

      vi.mocked(fs.readdir).mockResolvedValue(["2026-03-01.jsonl"] as unknown as any);
      vi.mocked(fs.readFile).mockResolvedValue(
        [focusedRun, unfocusedRun, generalRun].map((r) => JSON.stringify(r)).join("\n") + "\n"
      );

      const result = await ExecutionHistoryReader.getFocusLensComparison(workspaceRoot);

      expect(result).not.toBeNull();
      const comparison = result as FocusLensImpactComparison;

      // "quality" is focused; "general" and no-lens are unfocused
      expect(comparison.focusedRunCount).toBe(1);
      expect(comparison.unfocusedRunCount).toBe(2);
    });

    it("computes correct average cost for each group", async () => {
      const runs = [
        makeRunRecord({
          focus_lens_active: { lens: "quality" },
          tokens: { ...makeRunRecord({}).tokens, estimated_cost_usd: 0.3 },
          recorded_at: "2026-03-01T10:30:00.000Z",
        }),
        makeRunRecord({
          focus_lens_active: { lens: "quality" },
          tokens: { ...makeRunRecord({}).tokens, estimated_cost_usd: 0.1 },
          recorded_at: "2026-03-01T11:00:00.000Z",
        }),
        makeRunRecord({
          tokens: { ...makeRunRecord({}).tokens, estimated_cost_usd: 0.05 },
          recorded_at: "2026-03-01T12:00:00.000Z",
        }),
      ];

      vi.mocked(fs.readdir).mockResolvedValue(["2026-03-01.jsonl"] as unknown as any);
      vi.mocked(fs.readFile).mockResolvedValue(
        runs.map((r) => JSON.stringify(r)).join("\n") + "\n"
      );

      const result = await ExecutionHistoryReader.getFocusLensComparison(workspaceRoot);

      expect(result?.avgCostFocused).toBeCloseTo(0.2); // (0.3 + 0.1) / 2
      expect(result?.avgCostUnfocused).toBeCloseTo(0.05);
    });

    it("computes per-lens breakdown correctly", async () => {
      const runs = [
        makeRunRecord({
          focus_lens_active: { lens: "quality" },
          tokens: { ...makeRunRecord({}).tokens, estimated_cost_usd: 0.2 },
          recorded_at: "2026-03-01T10:00:00.000Z",
        }),
        makeRunRecord({
          focus_lens_active: { lens: "security" },
          tokens: { ...makeRunRecord({}).tokens, estimated_cost_usd: 0.4 },
          recorded_at: "2026-03-01T11:00:00.000Z",
        }),
        makeRunRecord({
          tokens: { ...makeRunRecord({}).tokens, estimated_cost_usd: 0.1 },
          recorded_at: "2026-03-01T12:00:00.000Z",
        }),
      ];

      vi.mocked(fs.readdir).mockResolvedValue(["2026-03-01.jsonl"] as unknown as any);
      vi.mocked(fs.readFile).mockResolvedValue(
        runs.map((r) => JSON.stringify(r)).join("\n") + "\n"
      );

      const result = await ExecutionHistoryReader.getFocusLensComparison(workspaceRoot);

      expect(result?.byLens).toHaveLength(2);
      const qualityLens = result?.byLens.find((l) => l.lens === "quality");
      const securityLens = result?.byLens.find((l) => l.lens === "security");

      expect(qualityLens?.runCount).toBe(1);
      expect(qualityLens?.avgCostUsd).toBeCloseTo(0.2);
      expect(securityLens?.runCount).toBe(1);
      expect(securityLens?.avgCostUsd).toBeCloseTo(0.4);
    });

    it("computes success rate correctly", async () => {
      const runs = [
        makeRunRecord({
          focus_lens_active: { lens: "quality" },
          outcome: "complete",
          recorded_at: "2026-03-01T10:00:00.000Z",
        }),
        makeRunRecord({
          focus_lens_active: { lens: "quality" },
          outcome: "failed",
          recorded_at: "2026-03-01T11:00:00.000Z",
        }),
        makeRunRecord({
          outcome: "complete",
          recorded_at: "2026-03-01T12:00:00.000Z",
        }),
        makeRunRecord({
          outcome: "complete",
          recorded_at: "2026-03-01T13:00:00.000Z",
        }),
      ];

      vi.mocked(fs.readdir).mockResolvedValue(["2026-03-01.jsonl"] as unknown as any);
      vi.mocked(fs.readFile).mockResolvedValue(
        runs.map((r) => JSON.stringify(r)).join("\n") + "\n"
      );

      const result = await ExecutionHistoryReader.getFocusLensComparison(workspaceRoot);

      expect(result?.successRateFocused).toBeCloseTo(0.5); // 1 of 2 complete
      expect(result?.successRateUnfocused).toBeCloseTo(1.0); // 2 of 2 complete
    });

    it("returns null gracefully when readdir fails", async () => {
      vi.mocked(fs.readdir).mockRejectedValue(new Error("permission denied"));

      const result = await ExecutionHistoryReader.getFocusLensComparison(workspaceRoot);
      expect(result).toBeNull();
    });
  });
});
