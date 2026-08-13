/**
 * Unit tests for the live ExecutionHistoryWriter history utilities.
 *
 * @see Issue #649 - Execution History Persistence
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as fs from "node:fs/promises";
import { ExecutionHistoryWriter } from "../../src/utils/executionHistoryWriter";

vi.mock("node:fs/promises");

describe("ExecutionHistoryWriter", () => {
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("getFilenameForDate()", () => {
    it("generates a UTC YYYY-MM-DD.jsonl filename", () => {
      expect(ExecutionHistoryWriter.getFilenameForDate(new Date("2026-12-31T23:59:59Z"))).toBe(
        "2026-12-31.jsonl"
      );
      expect(ExecutionHistoryWriter.getFilenameForDate(new Date("2026-01-01T00:00:00Z"))).toBe(
        "2026-01-01.jsonl"
      );
    });

    it("uses the current date when no date is provided", () => {
      const today = new Date().toISOString().split("T")[0];
      expect(ExecutionHistoryWriter.getFilenameForDate()).toBe(`${today}.jsonl`);
    });
  });

  it("returns the workspace history directory", () => {
    expect(ExecutionHistoryWriter.getHistoryDir(workspaceRoot)).toBe(
      "/test/workspace/.nightgauge/pipeline/history"
    );
  });

  it("does not expose deleted TypeScript record writers", () => {
    const writer = ExecutionHistoryWriter as unknown as Record<string, unknown>;
    expect(writer.buildRunRecord).toBeUndefined();
    expect(writer.appendRecord).toBeUndefined();
    expect(writer.buildOutcomeRecord).toBeUndefined();
  });

  describe("cleanupOldFiles()", () => {
    it("deletes only parseable JSONL files older than retention", async () => {
      const today = new Date().toISOString().split("T")[0];
      vi.mocked(fs.readdir).mockResolvedValue([
        "2020-01-01.jsonl",
        `${today}.jsonl`,
        "invalid-date.jsonl",
        "notes.txt",
      ] as never);
      vi.mocked(fs.unlink).mockResolvedValue();

      const result = await ExecutionHistoryWriter.cleanupOldFiles(workspaceRoot, 90);

      expect(result.deleted).toEqual(["2020-01-01.jsonl"]);
      expect(fs.unlink).toHaveBeenCalledTimes(1);
      expect(fs.unlink).toHaveBeenCalledWith(expect.stringContaining("2020-01-01.jsonl"));
    });

    it("treats a missing history directory as empty", async () => {
      vi.mocked(fs.readdir).mockRejectedValue(
        Object.assign(new Error("ENOENT"), { code: "ENOENT" })
      );

      await expect(ExecutionHistoryWriter.cleanupOldFiles(workspaceRoot, 90)).resolves.toEqual({
        deleted: [],
      });
    });
  });
});
