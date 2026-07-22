/**
 * executionHistoryWriter.identityGuard.test.ts
 *
 * #307 — the history writer must reject run records with an empty (present but
 * null/"") repo or run_id. Such a record is assembled from shared/cleared
 * per-run state and mis-routed into a sibling repo's history JSONL by the
 * concurrent cross-contamination bug (live dogfood 2026-07-19: three
 * null-identity "issue 209" rows). A record without identity is never
 * legitimate telemetry, so it must fail loudly and never be appended — even
 * though the #2249 policy otherwise writes schema-imperfect run records anyway.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import { ExecutionHistoryWriter } from "../../src/utils/executionHistoryWriter";
import type { ExecutionHistoryRecord } from "../../src/schemas/executionHistory";

vi.mock("node:fs/promises");

const WS = "/ws/platform";

/** Minimal run record; identity fields overridden per test. */
function runRecord(overrides: Record<string, unknown>): ExecutionHistoryRecord {
  return {
    record_type: "run",
    schema_version: 2,
    issue_number: 209,
    recorded_at: new Date("2026-07-19T17:31:34.563Z").toISOString(),
    outcome: "complete",
    stages: {},
    tokens: {
      total_input: 0,
      total_output: 0,
      total_cache_read: 0,
      total_cache_creation: 0,
      estimated_cost_usd: 0,
    },
    ...overrides,
  } as unknown as ExecutionHistoryRecord;
}

describe("ExecutionHistoryWriter identity guard (#307)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    vi.mocked(fs.appendFile).mockResolvedValue();
    vi.mocked(fs.readFile).mockResolvedValue("" as unknown as Buffer);
    vi.mocked(fs.writeFile).mockResolvedValue();
    ExecutionHistoryWriter.lastValidationError = null;
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("rejects a run record with run_id: null and never appends it", async () => {
    const ok = await ExecutionHistoryWriter.appendRecord(
      WS,
      runRecord({ repo: null, run_id: null })
    );
    expect(ok).toBe(false);
    expect(fs.appendFile).not.toHaveBeenCalled();
    expect(ExecutionHistoryWriter.lastValidationError).toMatch(/#307/);
  });

  it("rejects a run record with an empty-string repo", async () => {
    const ok = await ExecutionHistoryWriter.appendRecord(
      WS,
      runRecord({ repo: "", run_id: "d25ec02e" })
    );
    expect(ok).toBe(false);
    expect(fs.appendFile).not.toHaveBeenCalled();
  });

  it("rejects a run record with an empty-string run_id", async () => {
    const ok = await ExecutionHistoryWriter.appendRecord(
      WS,
      runRecord({ repo: "acme/platform", run_id: "" })
    );
    expect(ok).toBe(false);
    expect(fs.appendFile).not.toHaveBeenCalled();
  });

  it("appends a run record that carries a real identity", async () => {
    await ExecutionHistoryWriter.appendRecord(
      WS,
      runRecord({ repo: "acme/platform", run_id: "d25ec02e-4368-43f1-a3b6-c7f669dfa95b" })
    );
    expect(fs.appendFile).toHaveBeenCalledTimes(1);
    const [filePath, line] = vi.mocked(fs.appendFile).mock.calls[0];
    expect(String(filePath)).toContain("/ws/platform/.nightgauge/pipeline/history/");
    expect(String(line)).toContain("d25ec02e-4368-43f1-a3b6-c7f669dfa95b");
  });

  it("still appends a legit record whose identity fields are simply absent (undefined)", async () => {
    // The current builder omits repo/run_id entirely; absent must NOT be
    // treated as the empty-identity corruption signature.
    await ExecutionHistoryWriter.appendRecord(WS, runRecord({}));
    expect(fs.appendFile).toHaveBeenCalledTimes(1);
  });
});
