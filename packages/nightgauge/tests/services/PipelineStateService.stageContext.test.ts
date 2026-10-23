/**
 * PipelineStateService threads an editor-launched stage's context-window
 * telemetry (#1668) onto the "complete" notify: the per-stage peak is a max,
 * never a sum, and unobserved fields are omitted.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { uuidV7 } from "@nightgauge/sdk";

// Capture every ipc.call(method, params) — resolving so the service takes the
// real IPC path (not the local-fallback catch branch).
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

async function makeService(issueNumber: number, repo = "nightgauge/acmeapp") {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  PipelineStateService.resetInstance();
  const svc = PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
  // ADR-017 step 3 (#370): `beginRun` replaced `setRunRepo`. The repo is now
  // an attribute installed WITH the identity, never on its own — a repo with
  // no run to attach it to is what let `setPaused` mint an unattributed stub.
  svc.beginRun(uuidV7(), repo, issueNumber);
  return svc;
}

function callsTo(method: string) {
  return ipcCalls.filter((c) => c.method === method);
}

describe("PipelineStateService — stage context telemetry (#1668)", () => {
  beforeEach(() => {
    ipcCalls.length = 0;
  });

  it("keeps the max peak per stage, never the sum, and sends it on complete", async () => {
    const svc = await makeService(1668);
    svc.initEmpty();
    for (const peak of [7550, 12010, 9800]) {
      await svc.updateTokens({
        stage: "feature-dev",
        inputTokens: 0,
        outputTokens: 0,
        peakStepInputTokens: peak,
      });
    }
    await svc.updateTokens({
      stage: "feature-dev",
      inputTokens: 10,
      outputTokens: 5,
      contextWindowTokens: 131072,
    });
    await svc.completeStage("feature-dev");

    const complete = callsTo("pipeline.notifyStageTransition").find(
      (c) => c.params.status === "complete"
    );
    expect(complete?.params.peakStepInputTokens).toBe(12010);
    expect(complete?.params.contextWindowTokens).toBe(131072);
    expect(complete?.params).not.toHaveProperty("compactionCount");
    expect(complete?.params.inputTokens).toBe(10);
  });

  it("omits the fields when nothing was observed", async () => {
    const svc = await makeService(1669);
    svc.initEmpty();
    await svc.updateTokens({ stage: "feature-dev", inputTokens: 10, outputTokens: 5 });
    await svc.completeStage("feature-dev");
    const complete = callsTo("pipeline.notifyStageTransition").find(
      (c) => c.params.status === "complete"
    );
    for (const k of ["peakStepInputTokens", "contextWindowTokens", "compactionCount"]) {
      expect(complete?.params).not.toHaveProperty(k);
    }
  });

  it("carries a stage budget breach verbatim on the failed transition", async () => {
    const svc = await makeService(1670);
    await svc.failStage("feature-dev", "stage_budget_exceeded:turns observed=5 ceiling=5: stopped");
    const failed = callsTo("pipeline.notifyStageTransition").find(
      (c) => c.params.status === "failed"
    );
    expect(String(failed?.params.error)).toMatch(
      /^stage_budget_exceeded:turns observed=5 ceiling=5/
    );
  });
});
