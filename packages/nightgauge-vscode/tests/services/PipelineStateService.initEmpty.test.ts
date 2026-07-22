/**
 * Regression tests for Issue #3704: PipelineStateService.initEmpty()
 *
 * Before the fix, updateTokens() returned early when _lastState was null.
 * For concurrent worktree slots (no IPC pipeline.notifyStageTransition),
 * all token deltas were silently dropped.
 *
 * initEmpty() seeds a zero-valued _lastState so the first token delta is
 * accumulated and _onTokenUsageUpdated fires.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
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

async function makeWorktreeService(issueNumber: number) {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  PipelineStateService.resetInstance();
  return PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
}

describe("PipelineStateService.initEmpty()", () => {
  beforeEach(() => {
    vi.resetModules();
  });

  it("accumulates token deltas after initEmpty() and fires _onTokenUsageUpdated", async () => {
    const svc = await makeWorktreeService(3704);

    // Without initEmpty, updateTokens is a no-op
    const stateBefore = await svc.getState();
    expect(stateBefore).toBeNull();

    svc.initEmpty();

    const stateAfterInit = await svc.getState();
    expect(stateAfterInit).not.toBeNull();
    expect(stateAfterInit!.tokens?.input).toBe(0);
    expect(stateAfterInit!.tokens?.output).toBe(0);

    const updates: unknown[] = [];
    svc.onTokenUsageUpdated((v) => updates.push(v));

    await svc.updateTokens({
      inputTokens: 100,
      outputTokens: 50,
      costUsd: 0.01,
    });

    const state = await svc.getState();
    expect(state!.tokens!.input).toBe(100);
    expect(state!.tokens!.output).toBe(50);
    expect(state!.tokens!.estimated_cost_usd).toBeGreaterThan(0);
    expect(updates.length).toBeGreaterThan(0);
  });

  it("is idempotent — second initEmpty() does not reset accumulated tokens", async () => {
    const svc = await makeWorktreeService(3704);
    svc.initEmpty();

    await svc.updateTokens({ inputTokens: 200, outputTokens: 80, costUsd: 0.02 });
    svc.initEmpty(); // should be a no-op now

    const state = await svc.getState();
    expect(state!.tokens!.input).toBe(200);
    expect(state!.tokens!.output).toBe(80);
  });

  it("without initEmpty, updateTokens is a no-op and state stays null", async () => {
    const svc = await makeWorktreeService(3704);

    const fired: unknown[] = [];
    svc.onTokenUsageUpdated((v) => fired.push(v));

    await svc.updateTokens({ inputTokens: 50, outputTokens: 20, costUsd: 0.005 });

    const state = await svc.getState();
    expect(state).toBeNull();
    expect(fired).toHaveLength(0);
  });
});
