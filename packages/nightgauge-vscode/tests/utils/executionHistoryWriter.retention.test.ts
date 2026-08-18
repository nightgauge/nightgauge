/**
 * executionHistoryWriter.retention.test.ts (Issue #674)
 *
 * Verifies that `pipeline.logs.history_retention_days` (config.yaml) actually
 * drives ExecutionHistoryWriter.cleanupOldFiles, and that pruning a daily
 * JSONL file also drops its matching index.json entry.
 *
 * Uses REAL disk I/O (not the module-level `vi.mock("node:fs/promises")` in
 * executionHistoryWriter.test.ts) so the synchronous merged-config reader
 * (configPathResolver / mergedConfigReader) — which reads via `require("fs")`,
 * a different registration than `node:fs/promises` — sees genuine files.
 * Mirrors the real-temp-dir pattern used by tests/utils/customStageModels.test.ts.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "fs";
import * as fsp from "fs/promises";
import * as os from "os";
import * as path from "path";
import {
  ExecutionHistoryWriter,
  type HistoryIndex,
  type HistoryIndexEntry,
} from "../../src/utils/executionHistoryWriter";

function writeProjectConfig(root: string, contents: string): void {
  const dir = path.join(root, ".nightgauge");
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, "config.yaml"), contents, "utf-8");
}

function historyDir(root: string): string {
  return path.join(root, ".nightgauge", "pipeline", "history");
}

function daysAgo(n: number): Date {
  return new Date(Date.now() - n * 24 * 60 * 60 * 1000);
}

function minimalEntry(overrides: Partial<HistoryIndexEntry>): HistoryIndexEntry {
  return {
    issue_number: 1,
    title: "test run",
    outcome: "complete",
    cost_usd: 0,
    total_input_tokens: 0,
    total_output_tokens: 0,
    total_cache_read_tokens: 0,
    total_cache_creation_tokens: 0,
    duration_ms: 0,
    stage_count: 1,
    started_at: new Date().toISOString(),
    recorded_at: new Date().toISOString(),
    branch: "feat/test",
    ...overrides,
  };
}

describe("ExecutionHistoryWriter retention config threading (#674)", () => {
  let root: string;

  beforeEach(() => {
    root = fs.mkdtempSync(path.join(os.tmpdir(), "history-retention-"));
  });

  afterEach(() => {
    fs.rmSync(root, { recursive: true, force: true });
  });

  it("reads pipeline.logs.history_retention_days from config.yaml instead of the hardcoded 90-day default", async () => {
    writeProjectConfig(
      root,
      "schema_version: '2'\nowner: nightgauge\npipeline:\n  logs:\n    history_retention_days: 10\n"
    );

    const dir = historyDir(root);
    fs.mkdirSync(dir, { recursive: true });

    const oldFile = ExecutionHistoryWriter.getFilenameForDate(daysAgo(15));
    const recentFile = ExecutionHistoryWriter.getFilenameForDate(daysAgo(5));
    fs.writeFileSync(path.join(dir, oldFile), "");
    fs.writeFileSync(path.join(dir, recentFile), "");

    // No retentionDays argument — must resolve from config.yaml (10 days),
    // NOT the hardcoded 90-day default.
    const result = await ExecutionHistoryWriter.cleanupOldFiles(root);

    expect(result.deleted).toContain(oldFile);
    expect(result.deleted).not.toContain(recentFile);
    expect(fs.existsSync(path.join(dir, oldFile))).toBe(false);
    expect(fs.existsSync(path.join(dir, recentFile))).toBe(true);
  });

  it("falls back to the 90-day default when history_retention_days is unset", async () => {
    writeProjectConfig(root, "schema_version: '2'\nowner: nightgauge\n");

    const dir = historyDir(root);
    fs.mkdirSync(dir, { recursive: true });

    // 15 days old: pruned under a 10-day retention, but must SURVIVE the
    // 90-day default this test asserts.
    const recentFile = ExecutionHistoryWriter.getFilenameForDate(daysAgo(15));
    fs.writeFileSync(path.join(dir, recentFile), "");

    const result = await ExecutionHistoryWriter.cleanupOldFiles(root);

    expect(result.deleted).toEqual([]);
    expect(fs.existsSync(path.join(dir, recentFile))).toBe(true);
  });

  it("drops index.json entries whose daily file aged out, keeps entries whose file survives", async () => {
    writeProjectConfig(
      root,
      "schema_version: '2'\nowner: nightgauge\npipeline:\n  logs:\n    history_retention_days: 10\n"
    );

    const dir = historyDir(root);
    fs.mkdirSync(dir, { recursive: true });

    const oldDate = daysAgo(15);
    const recentDate = daysAgo(5);
    const oldFile = ExecutionHistoryWriter.getFilenameForDate(oldDate);
    const recentFile = ExecutionHistoryWriter.getFilenameForDate(recentDate);
    fs.writeFileSync(path.join(dir, oldFile), "");
    fs.writeFileSync(path.join(dir, recentFile), "");

    const index: HistoryIndex = {
      schema_version: "2",
      updated_at: new Date().toISOString(),
      total_runs: 2,
      entries: [
        minimalEntry({
          issue_number: 9001,
          recorded_at: oldDate.toISOString(),
          started_at: oldDate.toISOString(),
        }),
        minimalEntry({
          issue_number: 9002,
          recorded_at: recentDate.toISOString(),
          started_at: recentDate.toISOString(),
        }),
      ],
    };
    fs.writeFileSync(path.join(dir, "index.json"), JSON.stringify(index, null, 2), "utf-8");

    await ExecutionHistoryWriter.cleanupOldFiles(root);

    const raw = await fsp.readFile(path.join(dir, "index.json"), "utf-8");
    const updated = JSON.parse(raw) as HistoryIndex;

    expect(updated.entries.map((e) => e.issue_number)).not.toContain(9001);
    expect(updated.entries.map((e) => e.issue_number)).toContain(9002);
    expect(updated.total_runs).toBe(updated.entries.length);
  });
});
