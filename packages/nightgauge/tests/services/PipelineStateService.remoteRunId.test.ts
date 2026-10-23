/**
 * PipelineStateService.remoteRunId.test.ts
 *
 * #1656: a run that serves a dashboard trigger carries the trigger's platform
 * run id on its `initialized` and `running` transitions, so Go records that
 * trigger's remote run request pin on THIS run and on no other run of the
 * same issue. A run that serves no trigger sends no remoteRunId.
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

async function makeService(issueNumber: number) {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  PipelineStateService.resetInstance();
  return PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
}

async function mint(): Promise<string> {
  const { uuidV7 } = await import("@nightgauge/sdk");
  return uuidV7();
}

const transitions = () =>
  ipcCalls.filter((c) => c.method === "pipeline.notifyStageTransition").map((c) => c.params);

describe("PipelineStateService — remoteRunId on transitions (#1656)", () => {
  beforeEach(() => {
    ipcCalls.length = 0;
  });

  it("a run serving a trigger sends its remoteRunId on initialized and running", async () => {
    const svc = await makeService(7);
    svc.beginRun(await mint(), "o/r", 7, "platform-run-7");
    await svc.initializePipeline(7, "t", "feat/7");
    await svc.startStage("issue-pickup");
    const sent = transitions();
    expect(sent.map((p) => p.status)).toEqual(["initialized", "running"]);
    for (const p of sent) expect(p.remoteRunId).toBe("platform-run-7");
  });

  it("a run serving no trigger sends no remoteRunId", async () => {
    const svc = await makeService(8);
    svc.beginRun(await mint(), "o/r", 8);
    await svc.initializePipeline(8, "t", "feat/8");
    await svc.startStage("issue-pickup");
    for (const p of transitions()) expect("remoteRunId" in p).toBe(false);
  });
});
