/**
 * #888 — the served half of the #580 model envelope must reach Go.
 *
 * A complete, successful run recorded `stageModels` and `stageAdapters` and
 * left `stageServedModels`, `stageServedEfforts`, `stageServedThinking`,
 * `stageThinking` and `stageModelSelectionModes` null. Two of those Go derives
 * itself; the served ones only exist if the extension sends them, and it never
 * did — the executor's `result.servedModel` / `servedEffort` / `servedThinking`
 * were computed and dropped on the floor.
 *
 * The contract pinned here is deliberately narrow and is the whole point of
 * the issue: the served fields carry the executor's RAW report, never the
 * `servedModel ?? modelDecision.model` fallback that `model` carries. Sending
 * the fallback would manufacture an observation, and an invented observation
 * is worse than a null — the routing corpus cannot tell it from a real one.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { uuidV7 } from "@nightgauge/sdk";

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
  const svc = PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
  svc.beginRun(uuidV7(), "nightgauge/nightgauge", issueNumber);
  return svc;
}

function lastTransition(status: string): Record<string, unknown> | undefined {
  const calls = callMock.mock.calls.filter(
    (c) =>
      c[0] === "pipeline.notifyStageTransition" && (c[1] as { status?: string })?.status === status
  );
  return calls.at(-1)?.[1] as Record<string, unknown> | undefined;
}

describe("PipelineStateService — served model envelope (#888)", () => {
  beforeEach(() => {
    vi.resetModules();
    ipcHandlers.clear();
    callMock.mockClear();
  });

  it("puts the executor's served model, effort and thinking on the complete transition", async () => {
    const svc = await makeService(888);
    await svc.initializePipeline(888, "envelope", "fix/888");

    await svc.completeStage("feature-dev", {
      model: "claude-sonnet-5",
      adapter: "claude",
      servedModel: "claude-sonnet-5",
      servedEffort: "medium",
      servedThinking: "on",
    });

    expect(lastTransition("complete")).toMatchObject({
      stage: "feature-dev",
      status: "complete",
      model: "claude-sonnet-5",
      adapter: "claude",
      servedModel: "claude-sonnet-5",
      servedEffort: "medium",
      servedThinking: "on",
    });
  });

  it("omits the served fields entirely when the executor reported nothing", async () => {
    const svc = await makeService(889);
    await svc.initializePipeline(889, "no stream evidence", "fix/889");

    // The shape an interactive run produces: a resolved model is known, but
    // there is no stream-json to observe a served one from.
    await svc.completeStage("feature-dev", {
      model: "claude-sonnet-5",
      adapter: "claude",
    });

    const payload = lastTransition("complete")!;
    expect(payload.model).toBe("claude-sonnet-5");
    // Absent, NOT filled in from `model` — Go records absence as absence.
    expect(payload).not.toHaveProperty("servedModel");
    expect(payload).not.toHaveProperty("servedEffort");
    expect(payload).not.toHaveProperty("servedThinking");
  });

  it("does not let the requested model leak into servedModel", async () => {
    const svc = await makeService(890);
    await svc.initializePipeline(890, "divergence", "fix/890");

    // `model` is the request-or-served fallback; `servedModel` is the raw
    // observation. When they differ, both must survive to Go intact.
    await svc.completeStage("feature-dev", {
      model: "claude-opus-5",
      adapter: "claude",
      servedModel: "claude-sonnet-5",
    });

    const payload = lastTransition("complete")!;
    expect(payload.model).toBe("claude-opus-5");
    expect(payload.servedModel).toBe("claude-sonnet-5");
  });

  it("threads the served envelope onto a FAILED stage too", async () => {
    const svc = await makeService(891);
    await svc.initializePipeline(891, "killed", "fix/891");

    // The expensive tiers are disproportionately killed before they ever reach
    // completeStage, so attributing only on success measures the cheap stages
    // and nothing else.
    await svc.failStage("feature-dev", "[budget-exceeded] killed", {
      model: "claude-fable-5",
      adapter: "claude",
      servedModel: "claude-fable-5",
      servedThinking: "on",
    });

    expect(lastTransition("failed")).toMatchObject({
      status: "failed",
      servedModel: "claude-fable-5",
      servedThinking: "on",
    });
  });
});
