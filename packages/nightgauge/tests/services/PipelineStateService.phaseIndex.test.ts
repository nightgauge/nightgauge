/**
 * Issue #1008, the recording half.
 *
 * phaseTracker deriving the registry index is only useful if the state service
 * actually records it. Before #1008 `startPhase` overwrote whatever it was told
 * with `phases.length` — a running count of how many phases happened to have
 * been recorded — so the number drifted by exactly as many phases as went
 * unrecorded.
 *
 * The tracker-side tests cannot see this: they assert what is PASSED to
 * startPhase, and pass just as happily when startPhase discards it.
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
    private _h: Array<(v: unknown) => void> = [];
    event = (cb: (v: unknown) => void) => {
      this._h.push(cb);
      return { dispose: () => {} };
    };
    fire(v: unknown) {
      for (const h of this._h) h(v);
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

async function makeService(issueNumber: number) {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  const { uuidV7 } = await import("@nightgauge/sdk");
  PipelineStateService.resetInstance();
  const svc = PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
  svc.beginRun(uuidV7(), "nightgauge/nightgauge", issueNumber);
  svc.applyRuntimeSnapshot({ issueNumber, stage: "feature-dev" });
  return svc;
}

describe("startPhase records the registry index it is given (#1008)", () => {
  beforeEach(() => {
    ipcCalls.length = 0;
  });

  it("stores the registry position, not the arrival order", async () => {
    const svc = await makeService(1008);

    // Two earlier phases, then sync-project-status — which the registry places
    // at 15. Its arrival order is 2.
    await svc.startPhase("feature-dev", "validate-environment", 18, 0);
    await svc.startPhase("feature-dev", "implementation", 18, 8);
    await svc.startPhase("feature-dev", "sync-project-status", 18, 15);

    const state = await svc.getState();
    const phases = state?.stages["feature-dev"]?.phases ?? [];
    const sync = phases.find((p) => p.name === "sync-project-status");

    expect(sync, "sync-project-status should be recorded").toBeDefined();
    expect(
      sync!.index,
      "the record carried arrival order (2), so the tree view — which renders " +
        "the name's registry position — showed 15 of 18 for a third-recorded phase"
    ).toBe(15);
    expect(phases.find((p) => p.name === "implementation")!.index).toBe(8);
  });

  it("sends the same index on the wire, so record and display cannot diverge", async () => {
    const svc = await makeService(1008);
    ipcCalls.length = 0;

    await svc.startPhase("feature-dev", "sync-project-status", 18, 15);

    const phase = ipcCalls.find((c) => c.method === "pipeline.notifyPhaseTransition");
    expect(phase?.params).toMatchObject({ name: "sync-project-status", index: 15, total: 18 });
  });

  it("falls back to arrival order when no registry index is supplied", async () => {
    const svc = await makeService(1008);

    await svc.startPhase("feature-dev", "first", 3);
    await svc.startPhase("feature-dev", "second", 3);

    const state = await svc.getState();
    const phases = state?.stages["feature-dev"]?.phases ?? [];
    expect(phases.find((p) => p.name === "second")!.index).toBe(1);
  });
});
