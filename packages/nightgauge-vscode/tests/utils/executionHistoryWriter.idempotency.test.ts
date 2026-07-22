/**
 * executionHistoryWriter.idempotency.test.ts
 *
 * #313 — the Go binary's pipeline.notifyComplete handler is the authoritative
 * run-record writer, but several TS "backup" paths (dashboard-sync, the
 * Go-scheduler pipeline.complete handler) can also fire for the same
 * completion. appendRecord must be idempotent per run so a run ends up with
 * exactly ONE full-fidelity record: the first full write wins, a later
 * duplicate or degraded skeleton is dropped, and only a strictly richer record
 * upgrades. Uses a real temp directory (no fs mock) to exercise the
 * read-then-skip path faithfully.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { ExecutionHistoryWriter } from "../../src/utils/executionHistoryWriter";
import type { ExecutionHistoryRecord } from "../../src/schemas/executionHistory";

const RECORDED_AT = "2026-07-19T10:00:00.000Z";
const DAY = "2026-07-19";

function stages(...names: string[]): Record<string, { status: "complete" }> {
  return Object.fromEntries(names.map((n) => [n, { status: "complete" as const }]));
}

/** A run record that carries a real identity (passes the #307 guard). */
function runRecord(overrides: Record<string, unknown>): ExecutionHistoryRecord {
  return {
    record_type: "run",
    schema_version: 2,
    issue_number: 313,
    repo: "nightgauge/nightgauge",
    run_id: "run-abc",
    title: "Idempotency",
    branch: "fix/313",
    base_branch: "main",
    execution_mode: "automatic",
    started_at: "2026-07-19T09:00:00.000Z",
    completed_at: RECORDED_AT,
    total_duration_ms: 1000,
    recorded_at: RECORDED_AT,
    outcome: "complete",
    stages: stages("issue-pickup", "feature-dev"),
    tokens: {
      total_input: 10,
      total_output: 5,
      total_cache_read: 0,
      total_cache_creation: 0,
      estimated_cost_usd: 0.1,
      per_stage: {},
    },
    files: { read_count: 0, written_count: 0 },
    routing: { complexity_score: 0, path: "standard", skip_stages: [] },
    ...overrides,
  } as unknown as ExecutionHistoryRecord;
}

describe("ExecutionHistoryWriter run-record idempotency (#313)", () => {
  let ws: string;

  beforeEach(async () => {
    ws = await fs.mkdtemp(path.join(os.tmpdir(), "ng-hist-idem-"));
  });

  afterEach(async () => {
    await fs.rm(ws, { recursive: true, force: true });
  });

  async function dailyLines(): Promise<Record<string, unknown>[]> {
    const file = path.join(ws, ".nightgauge", "pipeline", "history", `${DAY}.jsonl`);
    const content = await fs.readFile(file, "utf-8");
    return content
      .split("\n")
      .filter((l) => l.trim())
      .map((l) => JSON.parse(l) as Record<string, unknown>);
  }

  async function readIndex(): Promise<{ total_runs: number; entries: Record<string, unknown>[] }> {
    const file = path.join(ws, ".nightgauge", "pipeline", "history", "index.json");
    return JSON.parse(await fs.readFile(file, "utf-8"));
  }

  it("drops a duplicate run record with the same run_id", async () => {
    const rec = runRecord({ run_id: "run-dup" });
    for (let i = 0; i < 4; i++) {
      expect(await ExecutionHistoryWriter.appendRecord(ws, rec)).toBe(true);
    }
    expect(await dailyLines()).toHaveLength(1);
    const idx = await readIndex();
    expect(idx.total_runs).toBe(1);
    expect(idx.entries).toHaveLength(1);
    expect(idx.entries[0].run_id).toBe("run-dup");
  });

  it("drops a skeleton written after a full record for the same run", async () => {
    const full = runRecord({ run_id: "run-xyz", stages: stages("issue-pickup", "feature-dev") });
    const skeleton = runRecord({ run_id: "run-xyz", stages: {} });
    await ExecutionHistoryWriter.appendRecord(ws, full);
    await ExecutionHistoryWriter.appendRecord(ws, skeleton);

    const lines = await dailyLines();
    expect(lines).toHaveLength(1);
    expect(Object.keys(lines[0].stages as object)).toHaveLength(2);
  });

  it("appends a strictly richer record as an upgrade and keeps the index deduped", async () => {
    const skeleton = runRecord({ run_id: "run-up", stages: {} });
    const full = runRecord({
      run_id: "run-up",
      stages: stages("issue-pickup", "feature-dev", "pr-merge"),
    });
    await ExecutionHistoryWriter.appendRecord(ws, skeleton);
    await ExecutionHistoryWriter.appendRecord(ws, full);

    // Append-only JSONL keeps both lines; the index collapses to the full one.
    expect(await dailyLines()).toHaveLength(2);
    const idx = await readIndex();
    expect(idx.entries).toHaveLength(1);
    expect(idx.entries[0].stage_count).toBe(3);
    expect(idx.entries[0].run_id).toBe("run-up");
  });

  it("records two distinct runs independently", async () => {
    await ExecutionHistoryWriter.appendRecord(ws, runRecord({ run_id: "run-1", issue_number: 1 }));
    await ExecutionHistoryWriter.appendRecord(ws, runRecord({ run_id: "run-2", issue_number: 2 }));
    expect(await dailyLines()).toHaveLength(2);
    const idx = await readIndex();
    expect(idx.total_runs).toBe(2);
  });

  it("defers to a record already on disk that lacks a run_id (issue+started_at key)", async () => {
    const a = runRecord({
      run_id: undefined,
      issue_number: 500,
      started_at: "2026-07-19T08:00:00.000Z",
    });
    const b = runRecord({
      run_id: undefined,
      issue_number: 500,
      started_at: "2026-07-19T08:00:00.000Z",
    });
    await ExecutionHistoryWriter.appendRecord(ws, a);
    await ExecutionHistoryWriter.appendRecord(ws, b);
    expect(await dailyLines()).toHaveLength(1);
  });
});
