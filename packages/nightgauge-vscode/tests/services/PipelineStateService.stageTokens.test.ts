/**
 * Regression tests: stage.complete IPC event must populate per-stage token data
 * in _lastState.tokens.per_stage so the pipeline tree view shows costs.
 *
 * Without this, StageTreeItem only displays "11/11 phases" with no cost/token
 * info, because PipelineTreeProvider.syncFromState reads state.tokens.per_stage.
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

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("PipelineStateService — stage.complete token propagation", () => {
  beforeEach(() => {
    vi.resetModules();
    ipcHandlers.clear();
  });

  it("populates _lastState.tokens.per_stage from stage.complete event", async () => {
    const svc = await makeService(100);
    await svc.initializePipeline(100, "Test Issue", "feat/test");

    // Simulate stage starting
    fireIpcEvent("stage.start", {
      issueNumber: 100,
      stage: "feature-planning",
      repo: "test/repo",
      title: "Feature Planning",
    });

    // Simulate stage completing with token data
    fireIpcEvent("stage.complete", {
      issueNumber: 100,
      stage: "feature-planning",
      repo: "test/repo",
      error: "",
      inputTokens: 50000,
      outputTokens: 5000,
      cacheReadTokens: 40000,
      costUsd: 0.4734,
      model: "claude-sonnet-4-6",
    });

    const state = await svc.getState();
    expect(state).toBeTruthy();

    // Per-stage tokens must be populated for tree view
    expect(state!.tokens).toBeTruthy();
    expect(state!.tokens!.per_stage).toBeTruthy();
    expect(state!.tokens!.per_stage!["feature-planning"]).toEqual({
      input: 50000,
      output: 5000,
      cost_usd: 0.4734,
      cache_read: 40000,
      cache_creation: 0,
      model: "claude-sonnet-4-6",
    });

    // Totals must be updated
    expect(state!.tokens!.total_input).toBe(50000);
    expect(state!.tokens!.total_output).toBe(5000);
    expect(state!.tokens!.estimated_cost_usd).toBe(0.4734);
  });

  it("accumulates tokens across multiple stages", async () => {
    const svc = await makeService(101);
    await svc.initializePipeline(101, "Multi Stage", "feat/multi");

    // Stage 1
    fireIpcEvent("stage.complete", {
      issueNumber: 101,
      stage: "issue-pickup",
      repo: "test/repo",
      error: "",
      inputTokens: 20000,
      outputTokens: 2000,
      cacheReadTokens: 15000,
      costUsd: 0.1,
      model: "claude-sonnet-4-6",
    });

    // Stage 2
    fireIpcEvent("stage.complete", {
      issueNumber: 101,
      stage: "feature-planning",
      repo: "test/repo",
      error: "",
      inputTokens: 50000,
      outputTokens: 5000,
      cacheReadTokens: 40000,
      costUsd: 0.47,
      model: "claude-sonnet-4-6",
    });

    const state = await svc.getState();

    // Both stages should have per-stage data
    expect(Object.keys(state!.tokens!.per_stage!)).toHaveLength(2);
    expect(state!.tokens!.per_stage!["issue-pickup"]!.cost_usd).toBe(0.1);
    expect(state!.tokens!.per_stage!["feature-planning"]!.cost_usd).toBe(0.47);

    // Totals should be cumulative
    expect(state!.tokens!.total_input).toBe(70000);
    expect(state!.tokens!.total_output).toBe(7000);
    expect(state!.tokens!.estimated_cost_usd).toBeCloseTo(0.57);
  });

  it("fires onTokenUsageUpdated event with stage data", async () => {
    const svc = await makeService(102);
    await svc.initializePipeline(102, "Token Event", "feat/event");

    const tokenEvents: Array<{ stage?: string; costUsd?: number }> = [];
    svc.onTokenUsageUpdated((update) => {
      tokenEvents.push({ stage: update.stage, costUsd: update.costUsd });
    });

    fireIpcEvent("stage.complete", {
      issueNumber: 102,
      stage: "feature-dev",
      repo: "test/repo",
      error: "",
      inputTokens: 30000,
      outputTokens: 3000,
      cacheReadTokens: 0,
      costUsd: 0.25,
      model: "claude-sonnet-4-6",
    });

    expect(tokenEvents).toHaveLength(1);
    expect(tokenEvents[0].stage).toBe("feature-dev");
    expect(tokenEvents[0].costUsd).toBe(0.25);
  });

  it("does not fire token event when tokens are zero", async () => {
    const svc = await makeService(103);
    await svc.initializePipeline(103, "Zero Tokens", "feat/zero");

    const tokenEvents: unknown[] = [];
    svc.onTokenUsageUpdated((update) => tokenEvents.push(update));

    fireIpcEvent("stage.complete", {
      issueNumber: 103,
      stage: "pr-merge",
      repo: "test/repo",
      error: "",
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      costUsd: 0,
      model: "",
    });

    expect(tokenEvents).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------
// updateTokens() path (HeadlessOrchestrator / concurrent slot mode)
// ---------------------------------------------------------------------------

describe("PipelineStateService — updateTokens (HeadlessOrchestrator path)", () => {
  beforeEach(() => {
    vi.resetModules();
    ipcHandlers.clear();
  });

  it("populates per_stage and totals from updateTokens deltas", async () => {
    const svc = await makeService(200);
    await svc.initializePipeline(200, "Concurrent Slot", "feat/slot");

    await svc.updateTokens({
      stage: "issue-pickup",
      inputTokens: 20000,
      outputTokens: 3000,
      cacheReadTokens: 15000,
      costUsd: 0.15,
    });

    const state = await svc.getState();
    expect(state!.tokens!.per_stage!["issue-pickup"]).toEqual({
      input: 20000,
      output: 3000,
      cost_usd: 0.15,
      cache_read: 15000,
      cache_creation: 0,
    });
    expect(state!.tokens!.total_input).toBe(20000);
    expect(state!.tokens!.total_output).toBe(3000);
    expect(state!.tokens!.total_cache_creation).toBe(0);
    expect(state!.tokens!.estimated_cost_usd).toBe(0.15);
  });

  it("accumulates deltas for the same stage across multiple calls", async () => {
    const svc = await makeService(201);
    await svc.initializePipeline(201, "Multi Delta", "feat/multi");

    // Two delta updates for the same stage (HeadlessOrchestrator sends
    // incremental deltas on each onTokenUsage callback)
    await svc.updateTokens({
      stage: "feature-dev",
      inputTokens: 10000,
      outputTokens: 1000,
      costUsd: 0.1,
    });
    await svc.updateTokens({
      stage: "feature-dev",
      inputTokens: 5000,
      outputTokens: 500,
      costUsd: 0.05,
    });

    const state = await svc.getState();
    const featureDev = state!.tokens!.per_stage!["feature-dev"]!;
    expect(featureDev.input).toBe(15000);
    expect(featureDev.output).toBe(1500);
    expect(featureDev.cost_usd).toBeCloseTo(0.15);
    expect(featureDev.cache_read).toBe(0);
    expect(state!.tokens!.total_input).toBe(15000);
    expect(state!.tokens!.total_output).toBe(1500);
    expect(state!.tokens!.estimated_cost_usd).toBeCloseTo(0.15);
  });

  it("fires onTokenUsageUpdated for each delta", async () => {
    const svc = await makeService(202);
    await svc.initializePipeline(202, "Events", "feat/events");

    const events: Array<{ stage?: string; costUsd?: number }> = [];
    svc.onTokenUsageUpdated((u) => events.push({ stage: u.stage, costUsd: u.costUsd }));

    await svc.updateTokens({
      stage: "feature-planning",
      inputTokens: 30000,
      outputTokens: 5000,
      costUsd: 0.47,
    });

    expect(events).toHaveLength(1);
    expect(events[0]).toEqual({ stage: "feature-planning", costUsd: 0.47 });
  });

  it("skips update when tokens are zero", async () => {
    const svc = await makeService(203);
    await svc.initializePipeline(203, "Zero", "feat/zero");

    const events: unknown[] = [];
    svc.onTokenUsageUpdated((u) => events.push(u));

    await svc.updateTokens({
      stage: "pr-create",
      inputTokens: 0,
      outputTokens: 0,
      costUsd: 0,
    });

    expect(events).toHaveLength(0);
    const state = await svc.getState();
    expect(state!.tokens?.per_stage).toBeUndefined();
  });
});

// ---------------------------------------------------------------------------
// pipeline.complete must not overwrite stage.complete accumulated data
// ---------------------------------------------------------------------------

describe("PipelineStateService — pipeline.complete merges with stage.complete data", () => {
  beforeEach(() => {
    vi.resetModules();
    ipcHandlers.clear();
  });

  it("preserves per_stage cache fields when pipeline.complete sends no cache data", async () => {
    const svc = await makeService(300);
    await svc.initializePipeline(300, "Cache Heavy", "feat/cache");

    // stage.complete fires with cache data (the correct data)
    fireIpcEvent("stage.complete", {
      issueNumber: 300,
      stage: "feature-dev",
      repo: "test/repo",
      error: "",
      inputTokens: 500,
      outputTokens: 30000,
      cacheReadTokens: 11000000,
      cacheCreationTokens: 350000,
      costUsd: 2.5,
      model: "claude-sonnet-4-6",
    });

    // pipeline.complete fires with Go's data (no cache fields in per_stage)
    fireIpcEvent("pipeline.complete", {
      issueNumber: 300,
      totalInputTokens: 0, // Go reports 0 for non-cached input
      totalOutputTokens: 0, // Go reports 0
      totalCostUSD: 2.5,
      perStage: [{ stage: "feature-dev", inputTokens: 0, outputTokens: 0, costUsd: 2.5 }],
    });

    const state = await svc.getState();

    // per_stage must still have cache_read and cache_creation from stage.complete
    const dev = state!.tokens!.per_stage!["feature-dev"];
    expect(dev).toBeTruthy();
    expect(dev!.cache_read).toBe(11000000);
    expect(dev!.cache_creation).toBe(350000);

    // total_input/total_output must fall back to stage.complete accumulated values (not 0)
    expect(state!.tokens!.total_input).toBe(500);
    expect(state!.tokens!.total_output).toBe(30000);
    expect(state!.tokens!.total_cache_read).toBe(11000000);
    expect(state!.tokens!.total_cache_creation).toBe(350000);
  });

  it("uses Go values when pipeline.complete sends non-zero totals", async () => {
    const svc = await makeService(301);
    await svc.initializePipeline(301, "Go Has Data", "feat/go-data");

    // stage.complete accumulates some data
    fireIpcEvent("stage.complete", {
      issueNumber: 301,
      stage: "issue-pickup",
      repo: "test/repo",
      error: "",
      inputTokens: 10000,
      outputTokens: 1000,
      cacheReadTokens: 5000,
      costUsd: 0.1,
      model: "claude-sonnet-4-6",
    });

    // pipeline.complete sends non-zero totals (Go had data)
    fireIpcEvent("pipeline.complete", {
      issueNumber: 301,
      totalInputTokens: 50000,
      totalOutputTokens: 8000,
      totalCostUSD: 0.5,
      perStage: [
        {
          stage: "issue-pickup",
          inputTokens: 50000,
          outputTokens: 8000,
          costUsd: 0.5,
        },
      ],
    });

    const state = await svc.getState();

    // Should use Go's non-zero values
    expect(state!.tokens!.total_input).toBe(50000);
    expect(state!.tokens!.total_output).toBe(8000);
  });

  it("handles cache-only stage.complete (inputTokens=0 but cacheReadTokens>0)", async () => {
    const svc = await makeService(302);
    await svc.initializePipeline(302, "Cache Only", "feat/cache-only");

    // stage.complete with zero input/output but large cache
    fireIpcEvent("stage.complete", {
      issueNumber: 302,
      stage: "feature-dev",
      repo: "test/repo",
      error: "",
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 5000000,
      costUsd: 1.0,
      model: "claude-sonnet-4-6",
    });

    const state = await svc.getState();

    // per_stage should be populated even with zero input/output
    expect(state!.tokens!.per_stage!["feature-dev"]).toBeTruthy();
    expect(state!.tokens!.per_stage!["feature-dev"]!.cache_read).toBe(5000000);
    expect(state!.tokens!.total_cache_read).toBe(5000000);
  });
});
