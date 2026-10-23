/**
 * PipelineStateService.identityLessSkipsTheWire.test.ts
 *
 * ADR-017 step 4 (#370) — an identity-less service does its local work and
 * never touches the wire.
 *
 * Every `pipeline.*` notify verb is run-progress or terminal class now, so the
 * server refuses `runId: ""` with `run_id_required`. The emitters that used to
 * send one were never speaking for a run in the first place — the stale-slot
 * repair emitter (`issueNumber: 0`, `runId: ""`), since DELETED with #427 for
 * being inert end to end; the dead contextWatcher population, still wired — so
 * the honest answer is to skip the send, keep the local state
 * and events, and log the skip once. That is the guard `sendPaused` has had
 * since step 1, now applied to the whole notify surface; it is also what lets
 * the server keep a hole-free identity contract.
 *
 * @see docs/decisions/017-runtime-identity-keying.md — Decisions 3 and 10
 * @see src/services/PipelineStateService.ts — `wireIdentityOrSkip`
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

const ipcCalls: Array<{ method: string; params: Record<string, unknown> }> = [];

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
      call: vi.fn((method: string, params: Record<string, unknown>) => {
        ipcCalls.push({ method, params });
        return Promise.resolve({ status: "ok" });
      }),
    }),
  },
}));

vi.mock("vscode", () => ({
  EventEmitter: class {
    private _handlers: Array<(v: unknown) => void> = [];
    event = (cb: (v: unknown) => void) => {
      this._handlers.push(cb);
      return { dispose: () => {} };
    };
    fire(value: unknown) {
      for (const h of this._handlers) h(value);
    }
    dispose() {}
  },
  Disposable: class {
    dispose() {}
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
}));

function makeLogger() {
  return { debug: vi.fn(), info: vi.fn(), warn: vi.fn() };
}

async function makeService(issueNumber: number) {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  PipelineStateService.resetInstance();
  return PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
}

/** Real identity from the production minter — never hand-authored (#166). */
async function mint(): Promise<string> {
  const { uuidV7 } = await import("@nightgauge/sdk");
  return uuidV7();
}

const SKIP_MESSAGE = /no run identity installed; IPC notify skipped \(ADR-017 step 4\)/;

describe("PipelineStateService — an identity-less service skips the wire", () => {
  beforeEach(() => {
    ipcCalls.length = 0;
  });

  it("failStage sends nothing, logs the skip, and still books the failure locally", async () => {
    const svc = await makeService(370);
    const logger = makeLogger();
    svc.setRejectionLogger(logger);
    // Local state without a run: exactly the shape the repair emitters hold.
    svc.applyRuntimeSnapshot({ issueNumber: 370, stage: "feature-dev" });

    const stageErrors: Array<{ stage: string; error: string }> = [];
    svc.onStageError((e) => stageErrors.push({ stage: e.stage, error: e.error }));

    await svc.failStage("feature-dev", "boom");

    // Nothing reached Go.
    expect(ipcCalls).toHaveLength(0);

    // One log, naming the method and the stage — not a swallowed catch.
    expect(logger.warn).toHaveBeenCalledTimes(1);
    const [message, meta] = logger.warn.mock.calls[0];
    expect(String(message)).toMatch(SKIP_MESSAGE);
    expect(String(message)).toContain("pipeline.notifyStageTransition");
    expect(meta).toMatchObject({ stage: "feature-dev" });

    // The local work still happened: state moved and the event fired.
    const state = await svc.getState();
    expect(state?.stages["feature-dev"]).toMatchObject({ status: "failed", error: "boom" });
    expect(stageErrors).toEqual([{ stage: "feature-dev", error: "boom" }]);
  });

  it("every notify path skips, and each skip is logged exactly once", async () => {
    const svc = await makeService(370);
    const logger = makeLogger();
    svc.setRejectionLogger(logger);
    svc.applyRuntimeSnapshot({ issueNumber: 370, stage: "feature-dev" });

    await svc.startStage("feature-dev");
    await svc.completeStage("feature-dev");
    await svc.failStage("feature-dev", "boom");
    await svc.skipStage("feature-dev");
    await svc.deferStage("feature-dev");
    await svc.recordStageModel("feature-dev", { model: "fable" });
    await svc.startPhase("feature-dev", "implement", 3);
    await svc.completePhase("feature-dev", "implement", 3);
    await svc.notifyPipelineComplete({
      success: true,
      totalDurationMs: 1000,
      stagesRun: ["feature-dev"],
    });

    expect(ipcCalls).toHaveLength(0);
    expect(logger.warn).toHaveBeenCalledTimes(9);
    for (const [message] of logger.warn.mock.calls) {
      expect(String(message)).toMatch(SKIP_MESSAGE);
    }
    const methods = logger.warn.mock.calls.map(([message]) => String(message).split(" ")[0]);
    expect(methods).toEqual([
      "pipeline.notifyStageTransition",
      "pipeline.notifyStageTransition",
      "pipeline.notifyStageTransition",
      "pipeline.notifyStageTransition",
      "pipeline.notifyStageTransition",
      "pipeline.notifyStageTransition",
      "pipeline.notifyPhaseTransition",
      "pipeline.notifyPhaseTransition",
      "pipeline.notifyComplete",
    ]);
  });

  it("with an identity installed the same calls go out, carrying it", async () => {
    const svc = await makeService(370);
    const logger = makeLogger();
    svc.setRejectionLogger(logger);
    const runId = await mint();
    svc.beginRun(runId, "nightgauge/nightgauge", 370);

    await svc.failStage("feature-dev", "boom");
    await svc.startPhase("feature-dev", "implement", 3);
    await svc.notifyPipelineComplete({
      success: false,
      totalDurationMs: 10,
      stagesRun: ["feature-dev"],
    });

    expect(logger.warn).not.toHaveBeenCalled();
    expect(ipcCalls.map((c) => c.method)).toEqual([
      "pipeline.notifyStageTransition",
      "pipeline.notifyPhaseTransition",
      "pipeline.notifyComplete",
    ]);
    for (const call of ipcCalls) {
      expect(call.params.runId).toBe(runId);
    }
  });
});
