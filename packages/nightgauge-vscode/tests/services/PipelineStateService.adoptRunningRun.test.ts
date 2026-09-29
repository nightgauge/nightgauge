/**
 * PipelineStateService.adoptRunningRun — a run already in flight at connect
 * reaches the relay (#2105).
 *
 * The first IPC call of an activation starts the daemon before the relay
 * subscribes, so no `pipeline.stateChanged` for a run that was already running
 * can reach it. `adoptRunningRun` asks the daemon's run registry
 * (`pipeline.runningSummary`) instead. These cases pin when it adopts and,
 * as important, when it must not.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { RunningPipelinesResult } from "../../src/services/IpcClientBase";

const handlers = new Map<string, (data: unknown) => void>();
const runningSummary = vi.fn<() => Promise<RunningPipelinesResult>>();

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn((event: string, handler: (data: unknown) => void) => {
        handlers.set(event, handler);
        return { dispose: vi.fn() };
      }),
      call: vi.fn(() => Promise.resolve({ status: "ok" })),
      pipelineRunningSummary: runningSummary,
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

function run(issueNumber: number, stage = "feature-dev") {
  return {
    runId: `run-${issueNumber}`,
    repo: "acme/widgets",
    issueNumber,
    title: `Issue ${issueNumber} title`,
    stage,
    startedAt: "2026-01-15T09:00:00.000Z",
    lastProgressAt: "2026-01-15T09:30:00.000Z",
    stale: false,
    source: "manual",
  };
}

function summary(...runs: ReturnType<typeof run>[]): RunningPipelinesResult {
  return { count: runs.length, runs, reloadSafe: runs.length === 0 };
}

async function singleton() {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  PipelineStateService.resetInstance();
  return PipelineStateService.getInstance("/tmp/repo");
}

describe("PipelineStateService.adoptRunningRun (#2105)", () => {
  beforeEach(() => {
    handlers.clear();
    runningSummary.mockReset();
  });

  it("adopts the one run in flight into an empty relay and announces it", async () => {
    const service = await singleton();
    runningSummary.mockResolvedValue(summary(run(112)));
    const seen: unknown[] = [];
    service.onStateChanged((state) => seen.push(state));

    expect(await service.adoptRunningRun()).toBe(true);

    const state = await service.getState();
    expect(state?.issue_number).toBe(112);
    expect(state?.title).toBe("Issue 112 title");
    expect(state?.stages["feature-dev"]?.status).toBe("running");
    expect(state?.current_stage).toBe("feature-dev");
    // The summary does not say how the earlier stages ended: none is invented.
    expect(state?.stages["feature-planning"]).toBeUndefined();
    expect(seen).toEqual([state]);
  });

  it("does not ask the daemon when the relay already holds a state", async () => {
    const service = await singleton();
    service.applyRuntimeSnapshot({ issueNumber: 7, stage: "issue-pickup" });

    expect(await service.adoptRunningRun()).toBe(false);
    expect(runningSummary).not.toHaveBeenCalled();
    expect((await service.getState())?.issue_number).toBe(7);
  });

  it("adopts nothing when no run is in flight", async () => {
    const service = await singleton();
    runningSummary.mockResolvedValue(summary());

    expect(await service.adoptRunningRun()).toBe(false);
    expect(await service.getState()).toBeNull();
  });

  it("does not pick one of several concurrent runs for the single-run relay", async () => {
    const service = await singleton();
    runningSummary.mockResolvedValue(summary(run(112), run(113)));

    expect(await service.adoptRunningRun()).toBe(false);
    expect(await service.getState()).toBeNull();
  });

  it("adopts only its own issue into an issue-filtered relay", async () => {
    const { PipelineStateService } = await import("../../src/services/PipelineStateService");
    const slot = PipelineStateService.createForWorktree("/tmp/repo-wt", 113);
    runningSummary.mockResolvedValue(summary(run(112), run(113, "pr-create")));

    expect(await slot.adoptRunningRun()).toBe(true);
    const state = await slot.getState();
    expect(state?.issue_number).toBe(113);
    expect(state?.stages["pr-create"]?.status).toBe("running");
  });

  it("keeps a snapshot that arrived while the summary was in flight", async () => {
    const service = await singleton();
    let answer: (value: RunningPipelinesResult) => void = () => {};
    runningSummary.mockReturnValue(new Promise((resolve) => (answer = resolve)));

    const adopting = service.adoptRunningRun();
    handlers.get("pipeline.stateChanged")?.({
      issueNumber: 112,
      repo: "acme/widgets",
      runId: "run-112",
      state: {
        issueNumber: 112,
        title: "Issue 112 title",
        stage: "feature-validate",
        completedStages: [{ stage: "feature-dev" }],
      },
    });
    answer(summary(run(112, "feature-dev")));

    expect(await adopting).toBe(false);
    const state = await service.getState();
    expect(state?.stages["feature-validate"]?.status).toBe("running");
    expect(state?.stages["feature-dev"]?.status).toBe("complete");
  });

  it("surfaces a failed summary to the caller instead of adopting", async () => {
    const service = await singleton();
    runningSummary.mockRejectedValue(new Error("Go backend not connected"));

    await expect(service.adoptRunningRun()).rejects.toThrow(/not connected/);
    expect(await service.getState()).toBeNull();
  });
});
