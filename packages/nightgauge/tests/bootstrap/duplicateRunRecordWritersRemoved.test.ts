/**
 * duplicateRunRecordWritersRemoved.test.ts
 *
 * Issue #141 — a pipeline run must produce exactly one history record.
 *
 * Three writers were producing run records for the same run:
 *
 *   1. `Scheduler.recordV2History` (internal/orchestrator/scheduler.go) — the
 *      authoritative one. Carries run_id, repo, per-stage tokens/cache, gate
 *      results and the outcome prediction, and writes into the run's OWN repo
 *      root (`runRoot(item.Repo)`).
 *   2. The extension's `pipeline.complete` handler — a strict subset of (1)
 *      with no run_id, no repo, and hardcoded zero cache/file counters,
 *      written to `telemetryStore`, which is rooted at workspaceFolders[0].
 *   3. `Dashboard.writeBackupHistoryRecord` — same shape as (2), but fired from
 *      `onStateChanged`, whose guard was a single last-issue-number slot on the
 *      Dashboard singleton. With two runs in flight the slot thrashed, so one
 *      run could be re-recorded on every alternation.
 *
 * (2) and (3) could not be repaired: neither has the run's repo or run_id
 * available (`PipelineState` carries neither), so their records were both
 * unattributable and undedupable — the idempotency key fell back to
 * issue+started_at, which each producer formats differently. They were deleted,
 * leaving the Go writer as the single producer, exactly as the #319 outcome
 * writer was deleted rather than repaired.
 *
 * Both handlers are registered inline inside VSCode-API-dependent bootstrap /
 * webview classes that are impractical to instantiate here (the same constraint
 * tests/bootstrap/legacyOutcomeWriterRemoved.test.ts documents). Since the fix
 * is a deletion there is no runnable logic to exercise, so this asserts against
 * the sources that no run-record write remains outside the Go path.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";

const SERVICES_PATH = path.resolve(__dirname, "../../src/bootstrap/services.ts");
const PIPELINE_COMPLETE_PATH = path.resolve(__dirname, "../../src/bootstrap/pipelineComplete.ts");
const DASHBOARD_PATH = path.resolve(__dirname, "../../src/views/dashboard/Dashboard.ts");
const WRITER_PATH = path.resolve(__dirname, "../../src/utils/executionHistoryWriter.ts");
const TELEMETRY_STORE_PATH = path.resolve(__dirname, "../../src/services/TelemetryStore.ts");

const servicesSource = readFileSync(SERVICES_PATH, "utf-8");
const pipelineCompleteSource = readFileSync(PIPELINE_COMPLETE_PATH, "utf-8");
const dashboardSource = readFileSync(DASHBOARD_PATH, "utf-8");
const writerSource = readFileSync(WRITER_PATH, "utf-8");
const telemetryStoreSource = readFileSync(TELEMETRY_STORE_PATH, "utf-8");

/**
 * The handler moved to its own module (#500) so a test can execute it; the
 * bootstrap now only registers it. The deletion pin therefore reads the
 * extracted module, and separately asserts the registration still points at it.
 */
function pipelineCompleteRegistration(source: string): string {
  const start = source.indexOf('ipc.on("pipeline.complete"');
  expect(start).toBeGreaterThan(-1); // registration must still exist
  const next = source.indexOf('ipc.on("pipeline.modelFallback"', start);
  expect(next).toBeGreaterThan(start);
  return source.slice(start, next);
}

describe("run-record producers — duplicates removed (Issue #141)", () => {
  describe("bootstrap pipeline.complete handler", () => {
    const handlerBody = pipelineCompleteSource;

    it("is still wired up from the bootstrap", () => {
      // Execution coverage lives in tests/bootstrap/pipelineComplete.test.ts;
      // that test cannot see whether the extracted handler is still registered.
      expect(pipelineCompleteRegistration(servicesSource)).toContain("handleGoPipelineComplete");
    });

    it("appends no history record", () => {
      expect(handlerBody).not.toContain("appendRunRecord");
      expect(handlerBody).not.toContain("appendRecord(");
    });

    it("does not assemble a run record", () => {
      // The tell-tale fields of the deleted builder. Their absence is what
      // stops a subset record being reintroduced by copy-paste.
      expect(handlerBody).not.toContain('record_type: "run"');
      expect(handlerBody).not.toContain("total_cache_read");
      expect(handlerBody).not.toContain("read_count");
    });

    it("still performs the post-run work only the extension can do", () => {
      expect(handlerBody).toContain("pipelineCompleteIssues.add");
      expect(handlerBody).toContain("recordHealthSnapshotForRun");
      expect(handlerBody).toContain("dashboardHistoryReloader");
      expect(handlerBody).toContain("telemetryUploaderService?.onPipelineCompleted()");
    });
  });

  describe("Dashboard backup writer", () => {
    it("no longer exists", () => {
      expect(dashboardSource).not.toContain("writeBackupHistoryRecord");
      // The thrashing guard it depended on is gone with it.
      expect(dashboardSource).not.toContain("lastHistoryWriteIssueNumber");
    });

    it("appends no history record anywhere in the dashboard", () => {
      expect(dashboardSource).not.toContain("appendRunRecord");
      expect(dashboardSource).not.toContain("ExecutionHistoryWriter.buildRunRecord");
    });

    it("still backfills from disk artifacts when the run completed while closed", () => {
      expect(dashboardSource).toContain("backfillHistoryFromArtifacts");
    });
  });

  it("leaves exactly zero run-record producers in the extension sources", () => {
    for (const [name, source] of [
      ["services.ts", servicesSource],
      ["pipelineComplete.ts", pipelineCompleteSource],
      ["Dashboard.ts", dashboardSource],
      ["TelemetryStore.ts", telemetryStoreSource],
      ["executionHistoryWriter.ts", writerSource],
    ] as const) {
      expect(
        source.includes("appendRunRecord"),
        `${name} must not write run records — the Go scheduler is the authoritative writer`
      ).toBe(false);
    }
  });

  it("deletes the unused TypeScript run-record writer surface (Issue #451)", () => {
    expect(writerSource).not.toContain("static buildRunRecord(");
    expect(writerSource).not.toContain("static async appendRecord(");
    expect(telemetryStoreSource).not.toContain("appendRunRecord(");
  });
});
