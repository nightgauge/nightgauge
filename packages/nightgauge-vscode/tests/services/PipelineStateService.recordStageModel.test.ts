/**
 * Tests for the up-front model attribution transition (#367).
 *
 * recordStageModel() emits a dedicated non-terminal "model-resolved" transition
 * as soon as the stage's model is resolved — BEFORE execution — so a stage that
 * is killed before completeStage/failStage still attributes its true model
 * rather than the platform bucketing its cost as cost_events.model_id =
 * 'unknown'. The Go handler records p.Model on every transition (latest-wins,
 * empties ignored) and treats the novel status as a no-op that does NOT reset
 * the stage clock, so a concrete servedModel at completion still overrides it.
 *
 * Contract pinned here: the transition carries status "model-resolved" (never
 * "running"), with model/adapter present when provided and omitted when absent.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

type EventHandler = (data: unknown) => void;
const ipcHandlers: Map<string, EventHandler> = new Map();

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

/** The payload of the last notifyStageTransition call with status "model-resolved". */
function lastModelResolvedTransition(): Record<string, unknown> | undefined {
  const calls = callMock.mock.calls.filter(
    (c) =>
      c[0] === "pipeline.notifyStageTransition" &&
      (c[1] as { status?: string })?.status === "model-resolved"
  );
  return calls.at(-1)?.[1] as Record<string, unknown> | undefined;
}

describe("PipelineStateService.recordStageModel — up-front model attribution", () => {
  beforeEach(() => {
    vi.resetModules();
    ipcHandlers.clear();
    callMock.mockClear();
  });

  it("emits a model-resolved transition with model + adapter, never 'running'", async () => {
    const svc = await makeService(700);
    await svc.initializePipeline(700, "Fable up-front", "feat/700");

    await svc.recordStageModel("feature-dev", {
      model: "claude-fable-5",
      adapter: "claude",
    });

    const payload = lastModelResolvedTransition();
    expect(payload).toMatchObject({
      stage: "feature-dev",
      status: "model-resolved",
      model: "claude-fable-5",
      adapter: "claude",
    });

    // Must never emit a "running" transition (that would reset the stage clock).
    const runningCalls = callMock.mock.calls.filter(
      (c) =>
        c[0] === "pipeline.notifyStageTransition" &&
        (c[1] as { status?: string })?.status === "running"
    );
    expect(runningCalls).toHaveLength(0);
  });

  it("omits model/adapter from the wire when empty (Go ignores empties)", async () => {
    const svc = await makeService(701);
    await svc.initializePipeline(701, "No attribution", "feat/701");

    await svc.recordStageModel("feature-dev", { model: "", adapter: "" });

    const payload = lastModelResolvedTransition();
    expect(payload).toBeDefined();
    expect(payload).not.toHaveProperty("model");
    expect(payload).not.toHaveProperty("adapter");
  });
});
