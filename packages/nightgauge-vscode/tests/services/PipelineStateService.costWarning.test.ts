/**
 * Regression tests: PipelineStateService warns when stage.complete carries tokens but costUsd=0.
 *
 * @see PipelineStateService.ts
 * @see Issue #2845 - Cost field may not update in OutputWindow
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

describe("PipelineStateService — zero-cost warning on stage.complete", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.resetModules();
    ipcHandlers.clear();
    vi.spyOn(console, "warn").mockImplementation(() => {});
  });

  it("should warn when stage.complete has tokens but costUsd is 0", async () => {
    const svc = await makeService(42);
    await svc.initializePipeline(42, "Test Issue", "feat/2845-test");

    fireIpcEvent("stage.complete", {
      issueNumber: 42,
      stage: "feature-planning",
      repo: "nightgauge/nightgauge",
      error: "",
      inputTokens: 1000,
      outputTokens: 500,
      cacheReadTokens: 0,
      costUsd: 0,
      model: "claude-sonnet-4-6",
    });

    expect(console.warn).toHaveBeenCalledWith(
      expect.stringContaining("stage.complete with tokens but costUsd=0")
    );
    expect(console.warn).toHaveBeenCalledWith(expect.stringContaining("feature-planning"));
  });

  it("should not warn when stage.complete has costUsd > 0", async () => {
    const svc = await makeService(42);
    await svc.initializePipeline(42, "Test Issue", "feat/2845-test");

    fireIpcEvent("stage.complete", {
      issueNumber: 42,
      stage: "feature-dev",
      repo: "nightgauge/nightgauge",
      error: "",
      inputTokens: 1000,
      outputTokens: 500,
      cacheReadTokens: 0,
      costUsd: 0.0234,
      model: "claude-sonnet-4-6",
    });

    expect(console.warn).not.toHaveBeenCalledWith(expect.stringContaining("costUsd=0"));
  });

  it("should not warn when stage.complete has zero tokens and zero cost", async () => {
    const svc = await makeService(42);
    await svc.initializePipeline(42, "Test Issue", "feat/2845-test");

    fireIpcEvent("stage.complete", {
      issueNumber: 42,
      stage: "issue-pickup",
      repo: "nightgauge/nightgauge",
      error: "",
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      costUsd: 0,
      model: "claude-sonnet-4-6",
    });

    expect(console.warn).not.toHaveBeenCalledWith(expect.stringContaining("costUsd=0"));
  });

  it("should accumulate estimated_cost_usd even when costUsd is 0", async () => {
    const svc = await makeService(99);
    await svc.initializePipeline(99, "Test Issue", "feat/2845-test");

    // First stage with real cost
    fireIpcEvent("stage.complete", {
      issueNumber: 99,
      stage: "issue-pickup",
      repo: "nightgauge/nightgauge",
      error: "",
      inputTokens: 500,
      outputTokens: 200,
      cacheReadTokens: 0,
      costUsd: 0.0012,
      model: "claude-sonnet-4-6",
    });

    // Second stage with zero cost (missing from CLI output)
    fireIpcEvent("stage.complete", {
      issueNumber: 99,
      stage: "feature-planning",
      repo: "nightgauge/nightgauge",
      error: "",
      inputTokens: 1000,
      outputTokens: 500,
      cacheReadTokens: 0,
      costUsd: 0,
      model: "claude-sonnet-4-6",
    });

    const state = await svc.getState();
    // First stage cost is preserved (second stage added 0 cost)
    expect(state?.tokens?.estimated_cost_usd).toBeCloseTo(0.0012);
  });
});
