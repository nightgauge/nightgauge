/**
 * Tests for model/adapter attribution on the "failed" stage transition.
 *
 * Fable is the most expensive tier and keeps its budget ceiling enabled, so
 * Fable-floored stages are disproportionately KILLED (cost-cap / stall / budget
 * / retry-exhaustion) via failStage() before a clean completeStage — which was
 * the only transition that carried a model. With no model on the "failed"
 * transition either, the Go notify handler recorded no StageModel and the
 * platform bucketed the stage's (real, expensive) cost as
 * cost_events.model_id = 'unknown'. failStage now forwards the same attribution
 * completeStage does, so an early kill still attributes to its resolved model.
 *
 * The Go handler records p.Model on EVERY transition and ignores empty strings
 * (latest-wins), so the contract we pin here is: model/adapter appear on the
 * "failed" transition when provided, and are omitted from the wire when absent.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

type EventHandler = (data: unknown) => void;
const ipcHandlers: Map<string, EventHandler> = new Map();

// Shared, capturable IPC-call mock. Rejecting mirrors the "IPC not connected"
// harness the other PipelineStateService tests use; the payload is still
// recorded on the mock BEFORE the rejection, which is what we assert.
const callMock = vi.fn().mockRejectedValue(new Error("IPC not connected"));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn((event: string, handler: EventHandler) => {
        ipcHandlers.set(event, handler);
        return { dispose: vi.fn() };
      }),
      call: callMock,
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
  ipcHandlers.clear();
  return PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
}

/** The payload of the last notifyStageTransition call with status "failed". */
function lastFailedTransition(): Record<string, unknown> | undefined {
  const calls = callMock.mock.calls.filter(
    (c) =>
      c[0] === "pipeline.notifyStageTransition" &&
      (c[1] as { status?: string })?.status === "failed"
  );
  return calls.at(-1)?.[1] as Record<string, unknown> | undefined;
}

describe("PipelineStateService.failStage — model attribution", () => {
  beforeEach(() => {
    vi.resetModules();
    ipcHandlers.clear();
    callMock.mockClear();
  });

  it("threads model + adapter onto the failed transition when attribution is provided", async () => {
    const svc = await makeService(700);
    await svc.initializePipeline(700, "Fable kill", "feat/700");

    await svc.failStage("feature-dev", "[budget-exceeded] killed", {
      model: "claude-fable-5",
      adapter: "claude",
    });

    const payload = lastFailedTransition();
    expect(payload).toMatchObject({
      stage: "feature-dev",
      status: "failed",
      error: "[budget-exceeded] killed",
      model: "claude-fable-5",
      adapter: "claude",
    });
  });

  it("omits model/adapter from the wire when attribution is absent (Go ignores empties)", async () => {
    const svc = await makeService(701);
    await svc.initializePipeline(701, "No attribution", "feat/701");

    await svc.failStage("feature-dev", "generic failure");

    const payload = lastFailedTransition();
    expect(payload).toBeDefined();
    expect(payload).not.toHaveProperty("model");
    expect(payload).not.toHaveProperty("adapter");
  });

  it("omits model when the resolved model is an empty string", async () => {
    const svc = await makeService(702);
    await svc.initializePipeline(702, "Empty served model", "feat/702");

    // servedModel unobserved AND modelDecision missing → attribution.model = "".
    await svc.failStage("feature-dev", "killed early", { model: "", adapter: "" });

    const payload = lastFailedTransition();
    expect(payload).not.toHaveProperty("model");
    expect(payload).not.toHaveProperty("adapter");
  });
});
