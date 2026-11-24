/**
 * Issue #2845: the Go snapshot carries the run's backtrack history, and
 * applyRuntimeSnapshot derives backtrack_count and conflict_redispatch_count
 * from it so a resumed run keeps both bounds.
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
  return svc;
}

beforeEach(() => {
  ipcCalls.length = 0;
});

describe("applyRuntimeSnapshot backtrack history (#2845)", () => {
  it("derives backtracks, backtrack_count and conflict_redispatch_count", async () => {
    const svc = await makeService(2845);
    svc.applyRuntimeSnapshot({
      issueNumber: 2845,
      backtracks: [
        {
          from_stage: "feature-validate",
          to_stage: "feature-dev",
          signal_type: "TEST_FAILURE",
          reason: "r",
          at: "2026-01-01T00:00:00Z",
        },
        {
          from_stage: "pr-merge",
          to_stage: "feature-dev",
          signal_type: "CONFLICT_RESOLUTION_NEEDED",
          at: "2026-01-01T00:01:00Z",
        },
        {
          from_stage: "pr-merge",
          to_stage: "feature-dev",
          signal_type: "CONFLICT_RESOLUTION_NEEDED",
          at: "2026-01-01T00:02:00Z",
        },
        {
          from_stage: "pr-merge",
          to_stage: "feature-dev",
          signal_type: "MERGE_QUEUE_CODE_FAILURE",
          at: "2026-01-01T00:03:00Z",
        },
      ],
    });
    const state = await svc.getState();
    expect(state?.backtracks).toHaveLength(4);
    expect(state?.backtracks?.[0]).toMatchObject({
      from_stage: "feature-validate",
      to_stage: "feature-dev",
      rationale: "r",
      timestamp: "2026-01-01T00:00:00Z",
    });
    expect(state?.backtrack_count).toBe(1);
    expect(state?.conflict_redispatch_count).toBe(2);
  });

  it("yields zero counts when the snapshot has no backtracks", async () => {
    const svc = await makeService(2845);
    svc.applyRuntimeSnapshot({ issueNumber: 2845 });
    const state = await svc.getState();
    expect(state?.backtrack_count).toBe(0);
    expect(state?.conflict_redispatch_count).toBe(0);
  });
});
