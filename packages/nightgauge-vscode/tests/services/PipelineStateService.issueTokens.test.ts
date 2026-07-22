/**
 * Tests for per_issue accumulator logic in PipelineStateService.
 * Verifies that issue-level token totals persist across stage transitions
 * and are never reset mid-stage.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ---------------------------------------------------------------------------
// IPC event handler capture
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

async function makeService(issueNumber: number) {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  PipelineStateService.resetInstance();
  ipcHandlers.clear();
  return PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
}

function stageComplete(
  issueNumber: number,
  stage: string,
  tokens: { input: number; output: number; cacheRead?: number; cost: number }
) {
  fireIpcEvent("stage.complete", {
    issueNumber,
    stage,
    repo: "test/repo",
    error: "",
    inputTokens: tokens.input,
    outputTokens: tokens.output,
    cacheReadTokens: tokens.cacheRead ?? 0,
    cacheCreationTokens: 0,
    costUsd: tokens.cost,
    model: "claude-sonnet-4-6",
  });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("PipelineStateService — per_issue accumulator", () => {
  beforeEach(() => {
    vi.resetModules();
    ipcHandlers.clear();
  });

  it("populates per_issue on first stage.complete", async () => {
    const svc = await makeService(200);
    await svc.initializePipeline(200, "Test", "feat/test");

    fireIpcEvent("stage.start", {
      issueNumber: 200,
      stage: "feature-planning",
      repo: "test/repo",
      title: "Test",
    });
    stageComplete(200, "feature-planning", { input: 10000, output: 1000, cost: 0.1 });

    const state = await svc.getState();
    expect(state?.tokens?.per_issue).toEqual({
      input: 10000,
      output: 1000,
      cache_read: 0,
      cache_creation: 0,
      cost_usd: 0.1,
    });
  });

  it("accumulates per_issue across multiple stage completions", async () => {
    const svc = await makeService(201);
    await svc.initializePipeline(201, "Multi", "feat/multi");

    fireIpcEvent("stage.start", { issueNumber: 201, stage: "issue-pickup", repo: "r", title: "T" });
    stageComplete(201, "issue-pickup", { input: 5000, output: 500, cost: 0.05 });

    fireIpcEvent("stage.start", {
      issueNumber: 201,
      stage: "feature-planning",
      repo: "r",
      title: "T",
    });
    stageComplete(201, "feature-planning", { input: 10000, output: 1000, cost: 0.1 });

    const state = await svc.getState();
    expect(state?.tokens?.per_issue?.input).toBe(15000);
    expect(state?.tokens?.per_issue?.output).toBe(1500);
    expect(state?.tokens?.per_issue?.cost_usd).toBeCloseTo(0.15);
  });

  it("preserves per_issue totals when a new stage starts (does not reset mid-stage)", async () => {
    const svc = await makeService(202);
    await svc.initializePipeline(202, "Persist", "feat/persist");

    // Complete stage 1
    fireIpcEvent("stage.start", {
      issueNumber: 202,
      stage: "feature-planning",
      repo: "r",
      title: "T",
    });
    stageComplete(202, "feature-planning", { input: 8000, output: 800, cost: 0.08 });

    const afterStage1 = await svc.getState();
    expect(afterStage1?.tokens?.per_issue?.cost_usd).toBeCloseTo(0.08);

    // Start stage 2 — per_issue must still hold stage 1 total
    fireIpcEvent("stage.start", {
      issueNumber: 202,
      stage: "feature-dev",
      repo: "r",
      title: "T",
    });

    const midStage2 = await svc.getState();
    expect(midStage2?.tokens?.per_issue?.cost_usd).toBeCloseTo(0.08);
    expect(midStage2?.tokens?.per_issue?.input).toBe(8000);
  });

  it("preserves per_issue through pipeline.stateChanged snapshots", async () => {
    const svc = await makeService(203);
    await svc.initializePipeline(203, "Snapshot", "feat/snap");

    fireIpcEvent("stage.start", {
      issueNumber: 203,
      stage: "feature-planning",
      repo: "r",
      title: "T",
    });
    stageComplete(203, "feature-planning", { input: 7000, output: 700, cost: 0.07 });

    // Simulate Go sending a stateChanged snapshot (which doesn't include per_issue)
    fireIpcEvent("pipeline.stateChanged", {
      issueNumber: 203,
      repo: "r",
      state: {
        issueNumber: 203,
        stage: "feature-dev",
        stageStart: new Date().toISOString(),
        completedStages: [{ stage: "feature-planning", startedAt: new Date().toISOString() }],
        skippedStages: [],
        stageErrors: {},
        inputTokens: 0,
        outputTokens: 0,
        totalCostUsd: 0,
      },
    });

    const state = await svc.getState();
    // per_issue must survive the Go snapshot overwrite
    expect(state?.tokens?.per_issue?.input).toBe(7000);
    expect(state?.tokens?.per_issue?.cost_usd).toBeCloseTo(0.07);
  });

  it("accumulates per_issue via updateTokens() mid-stage", async () => {
    const svc = await makeService(204);
    await svc.initializePipeline(204, "UpdateTokens", "feat/upd");

    fireIpcEvent("stage.start", {
      issueNumber: 204,
      stage: "feature-dev",
      repo: "r",
      title: "T",
    });

    await svc.updateTokens({
      stage: "feature-dev",
      inputTokens: 3000,
      outputTokens: 300,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0.03,
    });

    await svc.updateTokens({
      stage: "feature-dev",
      inputTokens: 2000,
      outputTokens: 200,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0.02,
    });

    const state = await svc.getState();
    expect(state?.tokens?.per_issue?.input).toBe(5000);
    expect(state?.tokens?.per_issue?.output).toBe(500);
    expect(state?.tokens?.per_issue?.cost_usd).toBeCloseTo(0.05);
  });
});

describe("PipelineStateService — current stage tracking", () => {
  beforeEach(() => {
    vi.resetModules();
    ipcHandlers.clear();
  });

  it("sets current_stage and current_stage_label on stage.start", async () => {
    const svc = await makeService(300);
    await svc.initializePipeline(300, "Stage Track", "feat/track");

    fireIpcEvent("stage.start", {
      issueNumber: 300,
      stage: "feature-dev",
      repo: "r",
      title: "T",
    });

    const state = await svc.getState();
    expect(state?.current_stage).toBe("feature-dev");
    expect(state?.current_stage_label).toBe("Feature Development");
  });

  it("sets current_stage_position matching expected 0-based index in STAGE_ORDER", async () => {
    const svc = await makeService(301);
    await svc.initializePipeline(301, "Pos Track", "feat/pos");

    fireIpcEvent("stage.start", {
      issueNumber: 301,
      stage: "feature-planning",
      repo: "r",
      title: "T",
    });

    const state = await svc.getState();
    // feature-planning is index 2 in ["pipeline-start","issue-pickup","feature-planning",...]
    expect(state?.current_stage_position).toBe(2);
  });
});
