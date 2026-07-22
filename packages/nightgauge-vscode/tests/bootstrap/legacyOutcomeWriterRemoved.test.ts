/**
 * legacyOutcomeWriterRemoved.test.ts
 *
 * Regression guard for Issue #319: the Issue #649 "outcome record" writer
 * appended a second, separate history record on pr-merge context-cleanup,
 * keyed off bootstrap-level shared state (`incrediRoot`/`telemetryStore`)
 * instead of the completing run's own identity. In a multi-repo/multi-run
 * workspace that shared state can resolve to a DIFFERENT repo than the one
 * that just finished, so the record landed in a sibling repo's history file
 * with no `repo`/`run_id` — invisible to every identity/idempotency guard
 * (#307, #313, #316), because those guards key off fields this writer never
 * populated.
 *
 * The fix deletes the writer outright rather than repairing it: the Go
 * authoritative writer (internal/state/history.go) already records the
 * merged outcome on its own "run" record for the completing pipeline, so
 * the second writer added no information.
 *
 * The `onIssueCleared` handler that used to host this writer is registered
 * inline inside the (VSCode-API-dependent) `registerServices()` bootstrap in
 * services.ts, which is impractical to instantiate in a unit test — there is
 * no existing test harness for the bootstrap's callback wiring (see
 * tests/bootstrap/goHistoryBridge.test.ts, which takes the same approach of
 * testing the inline handler's logic out of band rather than invoking
 * `registerServices()`). Since there is no runnable logic left to replicate
 * (the fix is a deletion), this test instead asserts — by reading the
 * handler's own source — that the pr-merge completion cleanup path no
 * longer appends any history record outside the Go authoritative path.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";

const SERVICES_PATH = path.resolve(__dirname, "../../src/bootstrap/services.ts");

/** Extracts the body of the `contextWatcher.onIssueCleared(...)` handler —
 * the pr-merge completion cleanup path — bounded by the next sibling
 * `contextWatcher.on*(` registration that follows it in the bootstrap. */
function extractOnIssueClearedHandler(source: string): string {
  const start = source.indexOf("contextWatcher.onIssueCleared(");
  expect(start).toBeGreaterThan(-1); // handler must still exist

  const nextRegistration = source.indexOf("contextWatcher.onStageComplete(", start);
  expect(nextRegistration).toBeGreaterThan(start);

  return source.slice(start, nextRegistration);
}

describe("pr-merge completion cleanup — legacy outcome writer removed (Issue #319)", () => {
  const source = readFileSync(SERVICES_PATH, "utf-8");
  const handlerBody = extractOnIssueClearedHandler(source);

  it("does not call ExecutionHistoryWriter.buildOutcomeRecord (deleted entirely)", () => {
    expect(handlerBody).not.toContain("buildOutcomeRecord");
    // Defense in depth: the builder must be gone from the whole bootstrap
    // file, not merely out of this one handler.
    expect(source).not.toContain("buildOutcomeRecord");
  });

  it("does not append any history record via telemetryStore or ExecutionHistoryWriter", () => {
    expect(handlerBody).not.toContain("appendRunRecord");
    expect(handlerBody).not.toContain("appendRecord(");
  });

  it("still marks pr-merge complete on the tree/output UI (cleanup behavior preserved)", () => {
    expect(handlerBody).toContain('treeProvider.updateStageStatus("pr-merge", "complete")');
    expect(handlerBody).toContain('outputWindow.updateStageStatus("pr-merge", "complete")');
  });
});
