/**
 * Integration test: Token/Cost data flow from IPC → State → OutputWindow
 *
 * Verifies the end-to-end path that broke in production: token costs not
 * appearing in the pipeline output despite having unit tests for each
 * individual component. The gap was that no test verified the full chain:
 *
 *   IPC stage.complete event
 *     → PipelineStateService.tokens accumulation
 *       → onTokenUsageUpdated event emission
 *         → OutputWindow receives event
 *           → OutputWindowState.setTokenUsage()
 *             → createTokenUpdateMessage() for WebView
 *
 * @see Issue #2230 - Test suite audit
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ---------------------------------------------------------------------------
// IPC event handler capture (same pattern as PipelineStateService tests)
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

async function makeStateService(issueNumber: number) {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  PipelineStateService.resetInstance();
  ipcHandlers.clear();
  return PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("Token/Cost end-to-end flow integration", () => {
  beforeEach(() => {
    vi.resetModules();
    ipcHandlers.clear();
  });

  it("stage.complete → state → onTokenUsageUpdated fires with correct totals", async () => {
    const svc = await makeStateService(500);
    await svc.initializePipeline(500, "Token Flow Test", "feat/token-flow");

    // Track all token events received
    const tokenEvents: Array<{
      inputTokens: number;
      outputTokens: number;
      stage?: string;
      costUsd?: number;
    }> = [];
    svc.onTokenUsageUpdated(
      (update: { inputTokens: number; outputTokens: number; stage?: string; costUsd?: number }) => {
        tokenEvents.push({
          inputTokens: update.inputTokens,
          outputTokens: update.outputTokens,
          stage: update.stage,
          costUsd: update.costUsd,
        });
      }
    );

    // Simulate 3 stages completing (typical pipeline: pickup → plan → dev)
    const stages = [
      {
        stage: "issue-pickup",
        inputTokens: 15000,
        outputTokens: 2000,
        cacheReadTokens: 10000,
        costUsd: 0.08,
        model: "claude-sonnet-4-6",
      },
      {
        stage: "feature-planning",
        inputTokens: 45000,
        outputTokens: 8000,
        cacheReadTokens: 35000,
        costUsd: 0.42,
        model: "claude-sonnet-4-6",
      },
      {
        stage: "feature-dev",
        inputTokens: 120000,
        outputTokens: 25000,
        cacheReadTokens: 90000,
        costUsd: 1.85,
        model: "claude-sonnet-4-6",
      },
    ];

    for (const stage of stages) {
      fireIpcEvent("stage.complete", {
        issueNumber: 500,
        repo: "test/repo",
        error: "",
        ...stage,
      });
    }

    // Verify: 3 token events fired (one per stage)
    expect(tokenEvents).toHaveLength(3);
    expect(tokenEvents[0].stage).toBe("issue-pickup");
    expect(tokenEvents[1].stage).toBe("feature-planning");
    expect(tokenEvents[2].stage).toBe("feature-dev");

    // Verify: state has correct per-stage and aggregate totals
    const state = await svc.getState();
    expect(state!.tokens!.per_stage).toBeTruthy();
    expect(Object.keys(state!.tokens!.per_stage!)).toHaveLength(3);

    // Per-stage values preserved
    expect(state!.tokens!.per_stage!["issue-pickup"]!.input).toBe(15000);
    expect(state!.tokens!.per_stage!["feature-planning"]!.input).toBe(45000);
    expect(state!.tokens!.per_stage!["feature-dev"]!.input).toBe(120000);

    // Totals are cumulative sums
    expect(state!.tokens!.total_input).toBe(15000 + 45000 + 120000); // 180000
    expect(state!.tokens!.total_output).toBe(2000 + 8000 + 25000); // 35000
    expect(state!.tokens!.estimated_cost_usd).toBeCloseTo(
      0.08 + 0.42 + 1.85 // 2.35
    );
  });

  it("token events contain data needed by OutputWindow.setTokenUsage()", async () => {
    const svc = await makeStateService(501);
    await svc.initializePipeline(501, "OutputWindow Compat", "feat/ow");

    // This test verifies that after stage.complete, getState() returns
    // the exact fields OutputWindow reads: total_input, total_output,
    // total_cache_read, estimated_cost_usd

    fireIpcEvent("stage.complete", {
      issueNumber: 501,
      stage: "feature-dev",
      repo: "test/repo",
      error: "",
      inputTokens: 50000,
      outputTokens: 8000,
      cacheReadTokens: 40000,
      costUsd: 0.65,
      model: "claude-sonnet-4-6",
    });

    const state = await svc.getState();
    const tokens = state!.tokens!;

    // OutputWindow reads these exact fields (see OutputWindow.ts lines 296-330):
    // currentState.tokens.total_input
    // currentState.tokens.total_output
    // currentState.tokens.total_cache_read
    // currentState.tokens.estimated_cost_usd
    expect(tokens.total_input).toBe(50000);
    expect(tokens.total_output).toBe(8000);
    expect(tokens.total_cache_read).toBe(40000);
    expect(tokens.estimated_cost_usd).toBe(0.65);

    // Per-stage cache_read also correctly set
    expect(tokens.per_stage!["feature-dev"]!.cache_read).toBe(40000);

    // Verify the shape matches what OutputWindowState.setTokenUsage expects
    // (OutputWindow.ts line 320-324 constructs this exact shape)
    const tokenUsage = {
      inputTokens: tokens.total_input ?? 0,
      outputTokens: tokens.total_output ?? 0,
      cacheReadTokens: tokens.total_cache_read ?? 0,
      cacheCreationTokens: tokens.total_cache_creation ?? 0,
      costUsd: tokens.estimated_cost_usd ?? 0,
    };
    expect(tokenUsage.inputTokens).toBe(50000);
    expect(tokenUsage.outputTokens).toBe(8000);
    expect(tokenUsage.cacheReadTokens).toBe(40000);
    expect(tokenUsage.costUsd).toBe(0.65);
  });

  it("createTokenUpdateMessage produces correct WebView message shape", async () => {
    const { createTokenUpdateMessage } =
      await import("../../src/views/outputWindow/OutputWindowMessageHandler");

    const usage = {
      inputTokens: 180000,
      outputTokens: 35000,
      cacheReadTokens: 125000,
      cacheCreationTokens: 0,
      costUsd: 2.35,
    };

    const msg = createTokenUpdateMessage(usage);

    expect(msg.type).toBe("update-tokens");
    expect((msg as any).usage).toEqual(usage);
    expect((msg as any).usage.costUsd).toBe(2.35);
  });

  it("multiple updateTokens deltas accumulate correctly for OutputWindow", async () => {
    const svc = await makeStateService(502);
    await svc.initializePipeline(502, "Delta Accumulation", "feat/deltas");

    // Simulate HeadlessOrchestrator sending incremental deltas
    // (this is the concurrent slot path where tokens arrive as deltas)
    await svc.updateTokens({
      stage: "feature-dev",
      inputTokens: 30000,
      outputTokens: 5000,
      cacheReadTokens: 20000,
      costUsd: 0.35,
    });

    await svc.updateTokens({
      stage: "feature-dev",
      inputTokens: 25000,
      outputTokens: 4000,
      cacheReadTokens: 18000,
      costUsd: 0.28,
    });

    const state = await svc.getState();
    const tokens = state!.tokens!;

    // Deltas should have accumulated
    expect(tokens.total_input).toBe(55000);
    expect(tokens.total_output).toBe(9000);
    expect(tokens.total_cache_read).toBe(38000);
    expect(tokens.estimated_cost_usd).toBeCloseTo(0.63);

    // Per-stage should also accumulate
    expect(tokens.per_stage!["feature-dev"]!.input).toBe(55000);
    expect(tokens.per_stage!["feature-dev"]!.output).toBe(9000);
  });

  it("mixed IPC and updateTokens paths produce consistent totals", async () => {
    const svc = await makeStateService(503);
    await svc.initializePipeline(503, "Mixed Paths", "feat/mixed");

    // Path A: IPC stage.complete (Go-driven pipeline)
    fireIpcEvent("stage.complete", {
      issueNumber: 503,
      stage: "issue-pickup",
      repo: "test/repo",
      error: "",
      inputTokens: 20000,
      outputTokens: 3000,
      cacheReadTokens: 15000,
      costUsd: 0.12,
      model: "claude-sonnet-4-6",
    });

    // Path B: updateTokens (HeadlessOrchestrator legacy path)
    await svc.updateTokens({
      stage: "feature-planning",
      inputTokens: 40000,
      outputTokens: 6000,
      cacheReadTokens: 30000,
      costUsd: 0.45,
    });

    const state = await svc.getState();

    // Both paths should contribute to totals
    expect(state!.tokens!.total_input).toBe(60000);
    expect(state!.tokens!.total_output).toBe(9000);
    expect(state!.tokens!.estimated_cost_usd).toBeCloseTo(0.57);

    // Both stages should appear in per_stage
    expect(state!.tokens!.per_stage!["issue-pickup"]).toBeTruthy();
    expect(state!.tokens!.per_stage!["feature-planning"]).toBeTruthy();
  });

  it("pipeline.complete with totalCostUSD=0 preserves TS-accumulated cost (Issue #2777)", async () => {
    // Regression: pipeline.complete handler must not reset estimated_cost_usd to 0
    // when Go sends totalCostUSD=0 (e.g., budget-terminated before cost was calculated).
    // The cost accumulated from stage.complete events must be preserved.
    const svc = await makeStateService(505);
    await svc.initializePipeline(505, "Cost Preservation", "feat/cost-fix");

    // Three stages complete via stage.complete with real costs
    for (const { stage, costUsd, inputTokens, outputTokens } of [
      { stage: "issue-pickup", costUsd: 0.08, inputTokens: 15000, outputTokens: 2000 },
      { stage: "feature-planning", costUsd: 0.42, inputTokens: 45000, outputTokens: 8000 },
      { stage: "feature-dev", costUsd: 1.85, inputTokens: 120000, outputTokens: 25000 },
    ]) {
      fireIpcEvent("stage.complete", {
        issueNumber: 505,
        repo: "test/repo",
        error: "",
        stage,
        inputTokens,
        outputTokens,
        cacheReadTokens: 0,
        costUsd,
        model: "claude-sonnet-4-6",
      });
    }

    // Confirm accumulated cost is correct before pipeline.complete fires
    const stateBefore = await svc.getState();
    expect(stateBefore!.tokens!.estimated_cost_usd).toBeCloseTo(0.08 + 0.42 + 1.85);

    // Simulate pipeline.complete from Go with totalCostUSD=0 (budget termination
    // where Go never called CompleteStageWithCost for the failed stage)
    fireIpcEvent("pipeline.complete", {
      issueNumber: 505,
      success: false,
      totalInputTokens: 0,
      totalOutputTokens: 0,
      totalCostUSD: 0,
      startedAt: new Date().toISOString(),
      durationMs: 60000,
      perStage: [],
    });

    // estimated_cost_usd must NOT be reset to 0 — it should retain the
    // value accumulated from stage.complete events (the TS fallback).
    const stateAfter = await svc.getState();
    expect(stateAfter!.tokens!.estimated_cost_usd).toBeCloseTo(0.08 + 0.42 + 1.85);
  });

  it("pipeline.complete with totalCostUSD>0 uses Go's authoritative total (Issue #2777)", async () => {
    // When Go sends a valid totalCostUSD, it should take precedence over
    // the TS-accumulated value (Go's total includes cache-read discount).
    const svc = await makeStateService(506);
    await svc.initializePipeline(506, "Go Cost Override", "feat/go-cost");

    // Stage completes with TS-accumulated cost
    fireIpcEvent("stage.complete", {
      issueNumber: 506,
      repo: "test/repo",
      error: "",
      stage: "feature-dev",
      inputTokens: 100000,
      outputTokens: 20000,
      cacheReadTokens: 80000,
      costUsd: 1.5,
      model: "claude-sonnet-4-6",
    });

    // pipeline.complete from Go with a more precise total (accounts for cache pricing)
    fireIpcEvent("pipeline.complete", {
      issueNumber: 506,
      success: true,
      totalInputTokens: 100000,
      totalOutputTokens: 20000,
      totalCostUSD: 1.35, // Go's actual calculation with cache discount
      startedAt: new Date().toISOString(),
      durationMs: 30000,
      perStage: [
        {
          stage: "feature-dev",
          inputTokens: 100000,
          outputTokens: 20000,
          cacheRead: 80000,
          costUsd: 1.35,
        },
      ],
    });

    // Go's totalCostUSD should win because it's > 0
    const state = await svc.getState();
    expect(state!.tokens!.estimated_cost_usd).toBeCloseTo(1.35);
  });

  it("zero-token stages do not emit events or pollute state", async () => {
    const svc = await makeStateService(504);
    await svc.initializePipeline(504, "Zero Tokens", "feat/zero");

    const events: unknown[] = [];
    svc.onTokenUsageUpdated((u: unknown) => events.push(u));

    // pr-merge often completes with zero tokens (no AI invocation)
    fireIpcEvent("stage.complete", {
      issueNumber: 504,
      stage: "pr-merge",
      repo: "test/repo",
      error: "",
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      costUsd: 0,
      model: "",
    });

    expect(events).toHaveLength(0);

    // But a real stage after that should still work
    fireIpcEvent("stage.complete", {
      issueNumber: 504,
      stage: "feature-dev",
      repo: "test/repo",
      error: "",
      inputTokens: 100000,
      outputTokens: 20000,
      cacheReadTokens: 80000,
      costUsd: 1.5,
      model: "claude-sonnet-4-6",
    });

    expect(events).toHaveLength(1);
    const state = await svc.getState();
    expect(state!.tokens!.total_input).toBe(100000);
    expect(state!.tokens!.estimated_cost_usd).toBe(1.5);
  });
});
