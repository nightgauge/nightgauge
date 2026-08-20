/**
 * TelemetryStore.concurrentRebuild.test.ts — two rebuilds on one workspace
 * must not destroy each other's temp file (#777).
 *
 * Deliberately NOT mocking node:fs/promises, unlike TelemetryStore.test.ts:
 * the defect lives in the ordering of two real filesystem calls. `writeIndex`
 * used to write a fixed `index.json.tmp` and rename it, so N concurrent
 * `rebuildIndex()` calls all wrote the SAME temp file and then all renamed it
 * — the first rename moved the file away and the rest failed with
 * `ENOENT: no such file or directory, rename '.../index.json.tmp' -> '.../index.json'`.
 * `DashboardState.loadFromTelemetryStore` swallowed that into "0 runs", so the
 * dashboard showed an empty history and said nothing had gone wrong.
 *
 * Two concurrent rebuilds is the ordinary case: the Dashboard constructor
 * starts a background load that any second load overlaps. Against a mocked fs
 * the race cannot happen at all, which is why this file is separate.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

import { TelemetryStore } from "../../src/services/TelemetryStore";
import { ExecutionHistoryReader } from "../../src/utils/executionHistoryReader";

/** How many rebuilds race. Two reproduces it; more makes the ordering blunt. */
const CONCURRENT_REBUILDS = 8;
const RUN_COUNT = 6;

let workspaceRoot: string;
let historyDir: string;

function runRecordLine(issueNumber: number): string {
  const started = new Date(Date.UTC(2026, 7, 12, 10, issueNumber)).toISOString();
  return JSON.stringify({
    schema_version: "2",
    record_type: "run",
    run_id: `run-${issueNumber}`,
    issue_number: issueNumber,
    title: `Concurrent rebuild fixture ${issueNumber}`,
    branch: `feat/${issueNumber}-fixture`,
    base_branch: "main",
    execution_mode: "automatic",
    started_at: started,
    completed_at: started,
    total_duration_ms: 1000,
    outcome: "complete",
    stages: {
      "issue-pickup": { status: "complete" },
      "feature-dev": { status: "complete" },
    },
    // Non-zero cost/tokens, or isGhostEntry() filters the entry back out and
    // the count assertion below would pass for the wrong reason.
    tokens: {
      total_input: 100,
      total_output: 50,
      total_cache_read: 0,
      total_cache_creation: 0,
      estimated_cost_usd: 0.25,
    },
    files: { read_count: 1, written_count: 1 },
    routing: { complexity_score: 1, path: "standard", skip_stages: [] },
    recorded_at: started,
  });
}

beforeEach(() => {
  workspaceRoot = fs.mkdtempSync(path.join(os.tmpdir(), "ng-index-race-"));
  historyDir = path.join(workspaceRoot, ".nightgauge", "pipeline", "history");
  fs.mkdirSync(historyDir, { recursive: true });
  const lines = Array.from({ length: RUN_COUNT }, (_, i) => runRecordLine(700 + i));
  fs.writeFileSync(path.join(historyDir, "2026-08-12.jsonl"), lines.join("\n") + "\n", "utf-8");
  ExecutionHistoryReader.clearCache();
});

afterEach(() => {
  fs.rmSync(workspaceRoot, { recursive: true, force: true });
});

describe("TelemetryStore.rebuildIndex — concurrent rebuilds on one workspace", () => {
  it("all rebuilds succeed; none loses its temp file to another's rename", async () => {
    const stores = Array.from(
      { length: CONCURRENT_REBUILDS },
      () => new TelemetryStore(workspaceRoot)
    );

    const settled = await Promise.allSettled(stores.map((store) => store.rebuildIndex()));

    // Report the actual rejection rather than a bare count — an ENOENT on
    // rename is the specific failure this test exists to keep out.
    const rejected = settled.filter((r) => r.status === "rejected") as PromiseRejectedResult[];
    expect(rejected.map((r) => String(r.reason))).toEqual([]);

    for (const result of settled) {
      expect(result.status).toBe("fulfilled");
      if (result.status === "fulfilled") {
        expect(result.value.total_runs).toBe(RUN_COUNT);
      }
    }
  });

  it("leaves one readable index and no temp files behind", async () => {
    const stores = Array.from(
      { length: CONCURRENT_REBUILDS },
      () => new TelemetryStore(workspaceRoot)
    );

    await Promise.all(stores.map((store) => store.rebuildIndex()));

    const indexPath = path.join(historyDir, "index.json");
    const parsed = JSON.parse(fs.readFileSync(indexPath, "utf-8")) as {
      total_runs: number;
      entries: unknown[];
    };
    expect(parsed.total_runs).toBe(RUN_COUNT);
    expect(parsed.entries).toHaveLength(RUN_COUNT);

    // Unique temp names are only an improvement if the losers clean up after
    // themselves; otherwise the race trades one bug for a growing pile of
    // `index.json.<pid>.<rand>.tmp`.
    expect(fs.readdirSync(historyDir).filter((f) => f.endsWith(".tmp"))).toEqual([]);
  });

  it("a fresh reader sees every run after a concurrent rebuild", async () => {
    await Promise.all(
      Array.from({ length: CONCURRENT_REBUILDS }, () =>
        new TelemetryStore(workspaceRoot).rebuildIndex()
      )
    );

    // The user-visible consequence: the dashboard reads the index a rebuild
    // just wrote. Under the fixed temp path the loser's ENOENT propagated up
    // through getAllRunSummaries and the list came back empty.
    const summaries = await new TelemetryStore(workspaceRoot).getAllRunSummaries();
    expect(summaries).toHaveLength(RUN_COUNT);
  });
});
