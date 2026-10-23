/**
 * staleSlotScannerRemoved.test.ts
 *
 * Issue #427 — the extension must not carry a stale-slot scanner of its own.
 *
 * `StaleSlotRecoveryService` (#1643) scanned every worktree on activation for
 * `<worktree>/.nightgauge/pipeline/state.json`, looked for a stage still marked
 * `running`, and "repaired" it. It was inert end to end: nothing in the tree has
 * ever written that `state.json`, so the scan returned `[]` on every activation,
 * and the repair built an identity-less `PipelineStateService` whose `failStage`
 * reached neither the wire (ADR-017 step-4 guard) nor the disk. It was deleted
 * with #427, together with its `setStageProcessPid` writer stub and the
 * `process_pid` state field it fed.
 *
 * The hazard this pins is RE-ADDITION, not the deletion: closing a stale run is
 * the Go orphan ladder's job (`internal/ipc/pipeline_orphan_reconcile.go`, ADR-017
 * §7.2 arm 3, which probes the stage child's pid delivered over the wire as
 * `stagePid`). A second TypeScript scanner over the same condition is exactly the
 * single-scanner hazard #323 settled for worktrees — so a re-added scanner is the
 * defect, and re-adding one is not a fix for anything.
 *
 * The fix is a deletion, so there is no runnable logic to exercise; as in
 * tests/bootstrap/legacyOutcomeWriterRemoved.test.ts and
 * tests/bootstrap/duplicateRunRecordWritersRemoved.test.ts, this asserts against
 * the sources themselves.
 */

import { describe, it, expect } from "vitest";
import { existsSync, readFileSync } from "node:fs";
import path from "node:path";

const STALE_SLOT_SERVICE_PATH = path.resolve(
  __dirname,
  "../../src/services/StaleSlotRecoveryService.ts"
);
const SERVICES_PATH = path.resolve(__dirname, "../../src/bootstrap/services.ts");
const PIPELINE_STATE_SERVICE_PATH = path.resolve(
  __dirname,
  "../../src/services/PipelineStateService.ts"
);
const PIPELINE_STATE_SCHEMA_PATH = path.resolve(__dirname, "../../src/schemas/pipelineState.ts");

const servicesSource = readFileSync(SERVICES_PATH, "utf-8");
const pipelineStateServiceSource = readFileSync(PIPELINE_STATE_SERVICE_PATH, "utf-8");
const pipelineStateSchemaSource = readFileSync(PIPELINE_STATE_SCHEMA_PATH, "utf-8");

/**
 * A `process_pid` DECLARATION — `process_pid:` or `process_pid?:` — as opposed to
 * a prose mention. The #427 tombstone comments legitimately name the field to say
 * what used to live there and where the pid travels instead; a declaration is the
 * violation, because only a declaration gives a re-added scanner something to read.
 */
const PROCESS_PID_DECLARATION = /process_pid\??\s*:/;

describe("TypeScript stale-slot scanner — removed and must stay removed (Issue #427)", () => {
  it("has no StaleSlotRecoveryService source file", () => {
    expect(
      existsSync(STALE_SLOT_SERVICE_PATH),
      "the Go orphan ladder closes stale runs (ADR-017 §7.2); a second TS scanner is the #323 hazard"
    ).toBe(false);
  });

  it("does not construct or call a stale-slot scanner from the bootstrap", () => {
    expect(servicesSource).not.toContain("StaleSlotRecoveryService");
    expect(servicesSource).not.toContain("recoverStaleSlots");
  });

  it("leaves PipelineStateService with no process_pid writer or declaration", () => {
    expect(pipelineStateServiceSource).not.toContain("setStageProcessPid");
    expect(pipelineStateServiceSource).not.toMatch(PROCESS_PID_DECLARATION);
  });

  it("leaves the pipeline-state schema with no process_pid declaration", () => {
    expect(pipelineStateSchemaSource).not.toMatch(PROCESS_PID_DECLARATION);
  });

  it("still starts the surviving discovery-only CLI reconciler", () => {
    // Positive control: without this, the pin above could be satisfied by
    // deleting the wrong scanner. CliPipelineReconciliationService polls
    // .nightgauge/pipeline/ and probes the CLI sidecar pid, but it only mirrors
    // runs into the tree view — it emits no terminal event and removes no
    // snapshot, so it is not a second closer.
    expect(servicesSource).toContain("new CliPipelineReconciliationService(");
  });
});
