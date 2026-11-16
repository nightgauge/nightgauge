/**
 * PipelineStateService.recordBacktrack.test.ts
 *
 * #3081 — a backtrack the extension's orchestrator takes is sent to Go through
 * `pipeline.recordBacktrack`, so it reaches the durable run record instead of
 * living only in the rebuilt TS PipelineState.
 *
 * @see internal/ipc/server.go — `pipeline.recordBacktrack`
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

const ipcCalls: Array<{ method: string; params: Record<string, unknown> }> = [];
let failNext = false;

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
      call: vi.fn((method: string, params: Record<string, unknown>) => {
        ipcCalls.push({ method, params });
        if (failNext) {
          failNext = false;
          return Promise.reject(new Error("rpc error -32000: run_closed"));
        }
        return Promise.resolve({ status: "ok" });
      }),
    }),
  },
}));

vi.mock("vscode", () => ({
  EventEmitter: class {
    event = () => ({ dispose: () => {} });
    fire() {}
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

async function makeService(issueNumber: number, runId: string) {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  PipelineStateService.resetInstance();
  const svc = PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
  svc.beginRun(runId, "nightgauge/nightgauge", issueNumber);
  return svc;
}

const record = {
  from_stage: "feature-validate",
  to_stage: "feature-dev",
  signal_type: "TEST_FAILURE",
  rationale: "unit tests red",
  timestamp: "2026-03-01T00:00:00.000Z",
  attempt_number: 1,
} as const;

function backtrackCalls() {
  return ipcCalls.filter((c) => c.method === "pipeline.recordBacktrack");
}

describe("PipelineStateService.recordBacktrack (#3081)", () => {
  beforeEach(() => {
    ipcCalls.length = 0;
    failNext = false;
  });

  it("sends the backtrack to Go under the run's identity", async () => {
    const { uuidV7 } = await import("@nightgauge/sdk");
    const runId = uuidV7();
    const svc = await makeService(3081, runId);
    await svc.recordBacktrack(record, ["feature-dev"]);
    expect(backtrackCalls()).toEqual([
      {
        method: "pipeline.recordBacktrack",
        params: {
          repo: "nightgauge/nightgauge",
          issueNumber: 3081,
          fromStage: "feature-validate",
          toStage: "feature-dev",
          signalType: "TEST_FAILURE",
          reason: "unit tests red",
          runId,
        },
      },
    ]);
  });

  it("swallows a refusal rather than failing the backtrack", async () => {
    const { uuidV7 } = await import("@nightgauge/sdk");
    const svc = await makeService(3081, uuidV7());
    failNext = true;
    await expect(svc.recordBacktrack(record)).resolves.toBeUndefined();
    expect(backtrackCalls()).toHaveLength(1);
  });
});
