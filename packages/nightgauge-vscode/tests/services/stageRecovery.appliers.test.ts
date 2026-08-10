/**
 * A stage that failed and then succeeded on retry renders as plain success (#407).
 *
 * THE INPUT IS NOT AUTHORED HERE. `recovered-stage-snapshot.json` is the
 * `pipeline.stateChanged` envelope the REAL Go binary emitted for a real
 * fail→retry→succeed run, captured verbatim by
 * `scripts/capture-stage-recovery-fixture.sh` (see the fixture's README for
 * provenance and the regeneration command). Hand-writing `stageErrors: {}`
 * would be this file asserting its own belief about Go's output — the #166
 * failure mode, and precisely the belief that was wrong before #407: Go used to
 * emit the recovered stage in `stageErrors` AND in `completedStages`, and every
 * applier below faithfully rendered it failed for the rest of the run.
 *
 * The appliers are unchanged by #407 and stay that way. They still apply
 * `stageErrors` AFTER `completedStages` on purpose: with Go's contract in place
 * ("an entry ⇔ the stage's MOST RECENT attempt failed"), a stage in both maps
 * is the legitimate backtrack case — completed earlier, re-run later, failed —
 * and the latest attempt must win. The negative control at the bottom pins that
 * direction so the fix cannot be mistaken for "stop trusting stageErrors".
 */

import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { countFailedStages, outcomeDisplay } from "../../src/services/DiscordService";
import { PipelineSlotsTracker } from "../../src/views/dashboard/PipelineSlotsTracker";

// Mirrored from DiscordService for assertion clarity, as DiscordService.test.ts does.
const COLOR_COMPLETE = 0x57f287;
const COLOR_WARNING = 0xfee75c;

// ---------------------------------------------------------------------------
// IPC mock — PipelineStateService and PipelineSlotsTracker both subscribe.
// ---------------------------------------------------------------------------

type EventHandler = (data: unknown) => void;
const ipcHandlers: Map<string, EventHandler[]> = new Map();

function fireIpcEvent(event: string, data: unknown): void {
  for (const h of ipcHandlers.get(event) ?? []) h(data);
}

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn((event: string, handler: EventHandler) => {
        const list = ipcHandlers.get(event) ?? [];
        list.push(handler);
        ipcHandlers.set(event, list);
        return { dispose: vi.fn() };
      }),
      call: vi.fn().mockRejectedValue(new Error("IPC not connected")),
    }),
  },
}));

// ---------------------------------------------------------------------------
// The captured fixture
// ---------------------------------------------------------------------------

interface CapturedEnvelope {
  event: string;
  data: {
    issueNumber: number;
    repo: string;
    runId: string;
    state: Record<string, unknown> & {
      stage?: string;
      completedStages?: Array<{ stage: string; exitCode: number }>;
      stageErrors?: Record<string, string> | null;
    };
  };
}

const FIXTURE_PATH = join(
  __dirname,
  "..",
  "fixtures",
  "stage-recovery",
  "recovered-stage-snapshot.json"
);

function loadCapture(): CapturedEnvelope {
  return JSON.parse(readFileSync(FIXTURE_PATH, "utf8")) as CapturedEnvelope;
}

const RECOVERED_STAGE = "feature-validate";

async function makeService(envelope: CapturedEnvelope) {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  PipelineStateService.resetInstance();
  ipcHandlers.clear();
  const svc = PipelineStateService.createForWorktree("/tmp/repo", envelope.data.issueNumber);
  // Identity is not ambient (ADR-017 step 3): the service filters stateChanged
  // by run, so the harness installs the SAME identity the capture carries.
  svc.beginRun(envelope.data.runId, envelope.data.repo, envelope.data.issueNumber);
  return svc;
}

describe("recovered stage (#407) — the captured Go snapshot", () => {
  let capture: CapturedEnvelope;

  beforeEach(() => {
    capture = loadCapture();
    ipcHandlers.clear();
  });

  it("is a genuine recovery: the stage ran twice, failed then succeeded, and Go cleared its error", () => {
    // A guard on the EVIDENCE, not on the code under test. If a regenerated
    // fixture ever stops showing a recovery, every assertion below would pass
    // vacuously — this is the tripwire for that.
    const attempts = (capture.data.state.completedStages ?? []).filter(
      (s) => s.stage === RECOVERED_STAGE
    );
    expect(attempts.map((a) => a.exitCode)).toEqual([1, 0]);
    expect(capture.data.state.stageErrors ?? {}).toEqual({});
  });

  it("applyRuntimeSnapshot renders it complete, not failed", async () => {
    const svc = await makeService(capture);

    svc.applyRuntimeSnapshot(capture.data.state as never);

    const state = await svc.getState();
    expect(state!.stages[RECOVERED_STAGE].status).toBe("complete");
    expect(state!.stages[RECOVERED_STAGE].error).toBeUndefined();
    expect(Object.values(state!.stages).filter((s) => s?.status === "failed")).toHaveLength(0);
  });

  it("the stateChanged applier renders it complete, not failed", async () => {
    const svc = await makeService(capture);

    fireIpcEvent("pipeline.stateChanged", capture.data);

    const state = await svc.getState();
    expect(state!.stages[RECOVERED_STAGE].status).toBe("complete");
    expect(state!.stages[RECOVERED_STAGE].error).toBeUndefined();
  });

  it("the dashboard slot card renders it complete and flags no issues", () => {
    const tracker = new PipelineSlotsTracker({
      on: (event: string, cb: EventHandler) => {
        const list = ipcHandlers.get(event) ?? [];
        list.push(cb);
        ipcHandlers.set(event, list);
        return { dispose: vi.fn() };
      },
    } as never);

    fireIpcEvent("pipeline.stateChanged", capture.data);

    const snap = tracker.getSnapshot(capture.data.issueNumber);
    expect(snap?.stages?.[RECOVERED_STAGE]?.status).toBe("complete");
    expect(snap?.hasIssues).toBeFalsy();
  });

  it("the notifiers count zero failed stages and say plainly Complete", async () => {
    const svc = await makeService(capture);
    svc.applyRuntimeSnapshot(capture.data.state as never);
    const state = await svc.getState();

    const failed = countFailedStages({ stages: state!.stages } as never);
    expect(failed).toBe(0);

    const logger = { warn: vi.fn() };
    const display = outcomeDisplay("productive", { failedStageCount: failed, logger });

    expect(display.color).toBe(COLOR_COMPLETE);
    expect(display.label).toBe("Complete ✓");
    expect(display.label).not.toMatch(/stage[s]? failed/);
    // The cross-check warning is the "success outcome contradicted by the run's
    // own stage list" line — a green run must not produce it.
    expect(logger.warn).not.toHaveBeenCalled();
  });

  // -------------------------------------------------------------------------
  // Negative controls — the assertions above must have teeth in BOTH
  // directions. Both mutate the captured envelope rather than inventing one.
  // -------------------------------------------------------------------------

  it("would fail the same way it used to if Go regressed and kept the entry", async () => {
    // The pre-#407 emission, reconstructed by putting back the one key the fix
    // removes. Everything else is the real capture.
    const regressed = loadCapture();
    regressed.data.state.stageErrors = { [RECOVERED_STAGE]: "exit 1: 2 tests failed" };

    const svc = await makeService(regressed);
    svc.applyRuntimeSnapshot(regressed.data.state as never);
    const state = await svc.getState();

    expect(state!.stages[RECOVERED_STAGE].status).toBe("failed");

    const failed = countFailedStages({ stages: state!.stages } as never);
    expect(failed).toBe(1);

    const logger = { warn: vi.fn() };
    const display = outcomeDisplay("productive", { failedStageCount: failed, logger });
    expect(display.color).toBe(COLOR_WARNING);
    expect(display.label).toBe("Complete — 1 stage failed ⚠️");
    expect(logger.warn).toHaveBeenCalled();
  });

  it("still lets a genuine LATER failure win over an earlier completion", async () => {
    // The backtrack case the applier ordering exists for: pr-create completed
    // in the capture, and a later re-run of it failed. Under the Go contract
    // that stage is legitimately in both maps, and "most recent attempt failed"
    // must be what the UI shows.
    const backtracked = loadCapture();
    backtracked.data.state.stageErrors = { "pr-create": "exit 1: push rejected" };

    const svc = await makeService(backtracked);
    svc.applyRuntimeSnapshot(backtracked.data.state as never);
    const state = await svc.getState();

    expect(state!.stages["pr-create"].status).toBe("failed");
    expect(state!.stages["pr-create"].error).toBe("exit 1: push rejected");
    // …and the recovered stage is untouched by that.
    expect(state!.stages[RECOVERED_STAGE].status).toBe("complete");
  });
});
