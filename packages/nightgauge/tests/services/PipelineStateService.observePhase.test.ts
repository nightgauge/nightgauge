/**
 * Issue #1887: `observePhase` is how the extension's legacy route records an
 * outcome `nightgauge phase reconcile` proved. The end-of-stage back-fill has
 * already written `unreported` by then, so the upgrade must replace that and
 * nothing else, and it must reach the wire as "observed" with its status:
 * local state alone would leave the durable record at the back-fill.
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

describe("observePhase upgrades only the unreported back-fill (#1887)", () => {
  beforeEach(() => {
    ipcCalls.length = 0;
  });

  it("replaces an unreported record and notifies the wire as observed", async () => {
    const svc = await makeServiceWithRun(1887);
    await svc.markPhaseUnreported("feature-dev", "testing", 17, 8);
    ipcCalls.length = 0;

    expect(await svc.observePhase("feature-dev", "testing", 17, 8, "complete")).toBe(true);

    const phase = (await svc.getState())?.stages["feature-dev"]?.phases?.find(
      (p) => p.name === "testing"
    );
    expect(phase?.status).toBe("complete");
    const calls = phaseCalls();
    expect(calls).toHaveLength(1);
    expect(calls[0].params).toMatchObject({
      stage: "feature-dev",
      name: "testing",
      index: 8,
      total: 17,
      eventType: "observed",
      status: "complete",
    });
  });

  it("appends a phase with no record at its registry index", async () => {
    const svc = await makeServiceWithRun(1887);

    expect(await svc.observePhase("feature-dev", "e2e-testing", 17, 9, "skipped")).toBe(true);

    const phases = (await svc.getState())?.stages["feature-dev"]?.phases ?? [];
    expect(phases).toEqual([
      expect.objectContaining({ name: "e2e-testing", index: 9, total: 17, status: "skipped" }),
    ]);
  });

  it.each(["complete", "skipped", "passed", "failed"] as const)(
    "leaves a %s record alone and sends nothing",
    async (status) => {
      const svc = await makeServiceWithRun(1887);
      if (status === "complete") {
        await svc.startPhase("feature-dev", "testing", 17, 8);
        await svc.completePhase("feature-dev", "testing", 17);
      } else if (status === "skipped") {
        await svc.skipPhase("feature-dev", "testing", 17, 8);
      } else if (status === "passed") {
        await svc.markPhasePassed("feature-dev", "testing", 17, 8);
      } else {
        await svc.startPhase("feature-dev", "testing", 17, 8);
        await svc.failPhase("feature-dev", "testing", "boom", 17);
      }
      ipcCalls.length = 0;

      expect(await svc.observePhase("feature-dev", "testing", 17, 8, "skipped")).toBe(false);

      const matching = ((await svc.getState())?.stages["feature-dev"]?.phases ?? []).filter(
        (p) => p.name === "testing"
      );
      expect(matching).toHaveLength(1);
      expect(matching[0].status).toBe(status);
      expect(phaseCalls()).toHaveLength(0);
    }
  );
});
