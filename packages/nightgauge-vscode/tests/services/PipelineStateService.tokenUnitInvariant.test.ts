/**
 * Regression tests for #193: `tokens.total_input` must always be the
 * COMBINED total (raw input + total_cache_read), never an alias of the
 * non-cached `input` accumulator alone.
 *
 * Two writer sites independently accumulate this field:
 *   Writer A — updateTokens() (HeadlessOrchestrator / concurrent slot path)
 *   Writer B — the stage.complete IPC handler (Go-driven pipeline path)
 *
 * Both must satisfy the invariant, and it must hold regardless of which
 * writer runs first when they interleave on the same _lastState.
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

function assertInvariant(tokens: {
  total_input?: number;
  input?: number;
  total_cache_read?: number;
}) {
  expect(tokens.total_input).toBe((tokens.input ?? 0) + (tokens.total_cache_read ?? 0));
}

describe("PipelineStateService — total_input unit invariant (#193)", () => {
  beforeEach(() => {
    vi.resetModules();
    ipcHandlers.clear();
  });

  it("Writer A (updateTokens) produces the combined total", async () => {
    const svc = await makeService(400);
    await svc.initializePipeline(400, "Writer A", "feat/writer-a");

    await svc.updateTokens({
      stage: "feature-dev",
      inputTokens: 10000,
      outputTokens: 1000,
      cacheReadTokens: 8000,
      costUsd: 0.1,
    });

    const state = await svc.getState();
    assertInvariant(state!.tokens!);
    expect(state!.tokens!.total_input).toBe(18000);
  });

  it("Writer B (stage.complete IPC handler) produces the combined total", async () => {
    const svc = await makeService(401);
    await svc.initializePipeline(401, "Writer B", "feat/writer-b");

    fireIpcEvent("stage.complete", {
      issueNumber: 401,
      stage: "feature-dev",
      repo: "test/repo",
      error: "",
      inputTokens: 500,
      outputTokens: 30000,
      cacheReadTokens: 11000000,
      costUsd: 2.5,
      model: "claude-sonnet-4-6",
    });

    const state = await svc.getState();
    assertInvariant(state!.tokens!);
    expect(state!.tokens!.total_input).toBe(11000500);
  });

  it("holds when Writer A runs, then Writer B (interleaving order 1)", async () => {
    const svc = await makeService(402);
    await svc.initializePipeline(402, "Interleave 1", "feat/interleave-1");

    await svc.updateTokens({
      stage: "issue-pickup",
      inputTokens: 5000,
      outputTokens: 500,
      cacheReadTokens: 3000,
      costUsd: 0.05,
    });

    fireIpcEvent("stage.complete", {
      issueNumber: 402,
      stage: "feature-planning",
      repo: "test/repo",
      error: "",
      inputTokens: 10000,
      outputTokens: 1000,
      cacheReadTokens: 8000,
      costUsd: 0.1,
      model: "claude-sonnet-4-6",
    });

    const state = await svc.getState();
    assertInvariant(state!.tokens!);
    // input: 5000+10000=15000, cache_read: 3000+8000=11000
    expect(state!.tokens!.total_input).toBe(26000);
  });

  it("holds when Writer B runs, then Writer A (interleaving order 2)", async () => {
    const svc = await makeService(403);
    await svc.initializePipeline(403, "Interleave 2", "feat/interleave-2");

    fireIpcEvent("stage.complete", {
      issueNumber: 403,
      stage: "feature-planning",
      repo: "test/repo",
      error: "",
      inputTokens: 10000,
      outputTokens: 1000,
      cacheReadTokens: 8000,
      costUsd: 0.1,
      model: "claude-sonnet-4-6",
    });

    await svc.updateTokens({
      stage: "issue-pickup",
      inputTokens: 5000,
      outputTokens: 500,
      cacheReadTokens: 3000,
      costUsd: 0.05,
    });

    const state = await svc.getState();
    assertInvariant(state!.tokens!);
    expect(state!.tokens!.total_input).toBe(26000);
  });
});
