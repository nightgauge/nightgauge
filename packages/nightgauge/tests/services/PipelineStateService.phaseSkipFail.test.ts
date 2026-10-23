/**
 * Issue #1026: `skipPhase` sent no IPC and `failPhase` was an empty body.
 *
 * Both had live callers. The consequence is the disagreement this epic is
 * named for: the live GUI knew about a skipped phase and the durable record —
 * the thing a retro or a survival verdict reads afterwards — did not, and a
 * phase that FAILED was indistinguishable from one still running. `feature-dev`
 * left `sync-project-status` in status "running" for twenty-four minutes.
 *
 * The assertion that matters is that the transition reaches the WIRE. A test
 * that only checks local state passes just as happily with no IPC at all —
 * which is exactly the state this code was in.
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

async function makeServiceWithRun(issueNumber: number) {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  const { uuidV7 } = await import("@nightgauge/sdk");
  PipelineStateService.resetInstance();
  const svc = PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
  svc.beginRun(uuidV7(), "nightgauge/nightgauge", issueNumber);
  // applyRuntimeSnapshot is what populates _lastState; `stage` creates the
  // per-stage entry that skipPhase reads (it returns early without one).
  svc.applyRuntimeSnapshot({ issueNumber, stage: "feature-dev" });
  return svc;
}

function phaseCalls() {
  return ipcCalls.filter((c) => c.method === "pipeline.notifyPhaseTransition");
}

describe("phase skip and fail reach the durable record (#1026)", () => {
  beforeEach(() => {
    ipcCalls.length = 0;
  });

  it("skipPhase notifies the wire with eventType 'skip'", async () => {
    const svc = await makeServiceWithRun(1026);
    ipcCalls.length = 0;

    await svc.skipPhase("feature-dev", "run-tests", 18);

    const calls = phaseCalls();
    expect(
      calls.length,
      "skipPhase sent nothing at all before #1026 — the record could never learn about a skip"
    ).toBe(1);
    expect(calls[0].params).toMatchObject({
      stage: "feature-dev",
      name: "run-tests",
      total: 18,
      eventType: "skip",
    });
  });

  it("failPhase notifies the wire with eventType 'fail'", async () => {
    const svc = await makeServiceWithRun(1026);
    await svc.startPhase("feature-dev", "sync-project-status", 18);
    ipcCalls.length = 0;

    await svc.failPhase("feature-dev", "sync-project-status", "boom", 18);

    const calls = phaseCalls();
    expect(calls.length, "failPhase was an empty body with a live caller").toBe(1);
    expect(calls[0].params).toMatchObject({
      stage: "feature-dev",
      name: "sync-project-status",
      eventType: "fail",
    });
  });

  it("failPhase also books the failure locally, with its reason", async () => {
    const svc = await makeServiceWithRun(1026);
    await svc.startPhase("feature-dev", "sync-project-status", 18);

    await svc.failPhase("feature-dev", "sync-project-status", "boom", 18);

    const state = await svc.getState();
    const phase = state?.stages["feature-dev"]?.phases?.find(
      (p) => p.name === "sync-project-status"
    );
    expect(phase?.status).toBe("failed");
    expect(phase?.error).toBe("boom");
  });

  it("skipPhase still records locally, and stays idempotent", async () => {
    const svc = await makeServiceWithRun(1026);

    await svc.skipPhase("feature-dev", "run-tests", 18);
    await svc.skipPhase("feature-dev", "run-tests", 18);

    const state = await svc.getState();
    const matching = (state?.stages["feature-dev"]?.phases ?? []).filter(
      (p) => p.name === "run-tests"
    );
    expect(matching).toHaveLength(1);
    expect(matching[0].status).toBe("skipped");
  });
});
