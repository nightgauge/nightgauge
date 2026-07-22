/**
 * PipelineStateService — prior-stage reconciliation on stage.start (#3244)
 *
 * Sequential pipelines can have at most one stage running at a time. If a
 * `stage.complete` IPC event from the orchestrator is dropped, the prior
 * stage stays at "running" while the next stage's `stage.start` arrives,
 * leaving the sidebar tree showing two concurrent running stages with the
 * earlier one's last phase spinning forever.
 *
 * The defensive safeguard reconciles any prior stage stuck at "running"
 * the moment a later `stage.start` arrives.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

type EventHandler = (data: unknown) => void;
const ipcHandlers: Map<string, EventHandler> = new Map();

function fireIpcEvent(event: string, data: unknown): void {
  ipcHandlers.get(event)?.(data);
}

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn((event: string, handler: EventHandler) => {
        ipcHandlers.set(event, handler);
        return { dispose: vi.fn() };
      }),
      call: vi.fn().mockRejectedValue(new Error("IPC not connected")),
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

describe("PipelineStateService — prior-stage reconciliation (#3244)", () => {
  beforeEach(() => {
    vi.resetModules();
    ipcHandlers.clear();
  });

  it("marks a prior stage stuck at 'running' as 'complete' when a later stage starts", async () => {
    const svc = await makeService(3239);
    await svc.initializePipeline(3239, "Test", "feat/test");

    fireIpcEvent("stage.start", {
      issueNumber: 3239,
      stage: "feature-planning",
      repo: "test/repo",
      title: "Feature Planning",
    });

    // Drop the stage.complete event for feature-planning to simulate the
    // bug — orchestrator advances directly to feature-dev.
    fireIpcEvent("stage.start", {
      issueNumber: 3239,
      stage: "feature-dev",
      repo: "test/repo",
      title: "Feature Development",
    });

    const state = await svc.getState();
    expect(state!.stages["feature-planning"].status).toBe("complete");
    expect(state!.stages["feature-planning"].completed_at).toBeTruthy();
    expect(state!.stages["feature-dev"].status).toBe("running");
  });

  it("downgrades phases stuck at 'running' when reconciling the prior stage", async () => {
    const svc = await makeService(3239);
    await svc.initializePipeline(3239, "Test", "feat/test");

    fireIpcEvent("stage.start", {
      issueNumber: 3239,
      stage: "feature-planning",
      repo: "test/repo",
      title: "Feature Planning",
    });

    fireIpcEvent("phase.start", {
      issueNumber: 3239,
      stage: "feature-planning",
      name: "produce-plan",
      index: 12,
      total: 13,
    });

    // Drop both phase.complete and stage.complete — feature-dev simply starts.
    fireIpcEvent("stage.start", {
      issueNumber: 3239,
      stage: "feature-dev",
      repo: "test/repo",
      title: "Feature Development",
    });

    const state = await svc.getState();
    const planningStage = state!.stages["feature-planning"];
    expect(planningStage.status).toBe("complete");
    expect(planningStage.current_phase).toBeUndefined();
    const stuckPhase = planningStage.phases?.find((p) => p.name === "produce-plan");
    expect(stuckPhase, "produce-plan phase should still exist").toBeDefined();
    expect(stuckPhase!.status).toBe("complete");
  });

  it("reconciles MULTIPLE prior stages stuck at 'running'", async () => {
    const svc = await makeService(42);
    await svc.initializePipeline(42, "Multi-prior", "feat/multi");

    // Two priors stuck at "running" when feature-dev starts.
    fireIpcEvent("stage.start", {
      issueNumber: 42,
      stage: "issue-pickup",
      repo: "test/repo",
      title: "x",
    });
    fireIpcEvent("stage.start", {
      issueNumber: 42,
      stage: "feature-planning",
      repo: "test/repo",
      title: "x",
    });
    fireIpcEvent("stage.start", {
      issueNumber: 42,
      stage: "feature-dev",
      repo: "test/repo",
      title: "x",
    });

    const state = await svc.getState();
    // Each stage.start reconciles the priors that were "running" before it.
    expect(state!.stages["issue-pickup"].status).toBe("complete");
    expect(state!.stages["feature-planning"].status).toBe("complete");
    expect(state!.stages["feature-dev"].status).toBe("running");
  });

  it("emits a synthetic _onStageComplete for each reconciled prior", async () => {
    const svc = await makeService(7);
    await svc.initializePipeline(7, "Cascade", "feat/cascade");

    const completeEvents: string[] = [];
    svc.onStageComplete(({ stage }) => completeEvents.push(stage));

    fireIpcEvent("stage.start", {
      issueNumber: 7,
      stage: "feature-planning",
      repo: "test/repo",
      title: "x",
    });
    fireIpcEvent("stage.start", {
      issueNumber: 7,
      stage: "feature-dev",
      repo: "test/repo",
      title: "x",
    });

    expect(completeEvents).toContain("feature-planning");
  });

  it("does NOT touch stages at non-running statuses (pending, complete, skipped, failed)", async () => {
    const svc = await makeService(8);
    await svc.initializePipeline(8, "Mixed", "feat/mixed");

    // Walk through stage.start + stage.complete normally for issue-pickup.
    fireIpcEvent("stage.start", {
      issueNumber: 8,
      stage: "issue-pickup",
      repo: "test/repo",
      title: "x",
    });
    fireIpcEvent("stage.complete", {
      issueNumber: 8,
      stage: "issue-pickup",
      repo: "test/repo",
      error: "",
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      costUsd: 0,
      model: "test",
    });

    // feature-planning fails (not "running" — should be untouched on reconcile).
    fireIpcEvent("stage.start", {
      issueNumber: 8,
      stage: "feature-planning",
      repo: "test/repo",
      title: "x",
    });
    fireIpcEvent("stage.complete", {
      issueNumber: 8,
      stage: "feature-planning",
      repo: "test/repo",
      error: "boom",
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      costUsd: 0,
      model: "test",
    });

    // feature-dev starts after a real failure — reconciler shouldn't
    // re-write feature-planning's status to "complete".
    fireIpcEvent("stage.start", {
      issueNumber: 8,
      stage: "feature-dev",
      repo: "test/repo",
      title: "x",
    });

    const state = await svc.getState();
    expect(state!.stages["issue-pickup"].status).toBe("complete");
    expect(state!.stages["feature-planning"].status).toBe("failed");
    expect(state!.stages["feature-dev"].status).toBe("running");
  });

  it("is a no-op when stage.start fires for the very first stage (no priors)", async () => {
    const svc = await makeService(9);
    await svc.initializePipeline(9, "First", "feat/first");

    fireIpcEvent("stage.start", {
      issueNumber: 9,
      stage: "issue-pickup",
      repo: "test/repo",
      title: "x",
    });

    const state = await svc.getState();
    expect(state!.stages["issue-pickup"].status).toBe("running");
    // pipeline-start is BEFORE issue-pickup but was never running, so nothing to reconcile.
    expect(state!.stages["pipeline-start"]).toBeUndefined();
  });

  it("is a no-op when the same stage fires stage.start again (retry)", async () => {
    const svc = await makeService(10);
    await svc.initializePipeline(10, "Retry", "feat/retry");

    fireIpcEvent("stage.start", {
      issueNumber: 10,
      stage: "feature-planning",
      repo: "test/repo",
      title: "x",
    });
    // Same-stage re-fire (e.g. retry).
    fireIpcEvent("stage.start", {
      issueNumber: 10,
      stage: "feature-planning",
      repo: "test/repo",
      title: "x",
    });

    const state = await svc.getState();
    expect(state!.stages["feature-planning"].status).toBe("running");
  });
});
