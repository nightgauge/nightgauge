/**
 * Tests for PipelineStateService.setStageAdapter — adapter routing audit
 * trail persistence (Issue #3231).
 *
 * The setStageAdapter method is called by HeadlessOrchestrator after each
 * stage to record which adapter actually ran, its source, and (when fallback
 * walked) the full audit trail. The audit trail flows downstream into the
 * execution-history JSONL record.
 *
 * Contract:
 *   - decision.adapterFallbackChainUsed length ≥ 2 → persisted as-is.
 *   - length 1 (or absent) → field cleared (primary-success implicit).
 *   - adapter and adapter_source always persisted from the decision.
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

async function makeService(issueNumber: number) {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  PipelineStateService.resetInstance();
  ipcHandlers.clear();
  return PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
}

describe("PipelineStateService.setStageAdapter (Issue #3231)", () => {
  beforeEach(() => {
    vi.resetModules();
    ipcHandlers.clear();
  });

  it("persists adapter and adapter_source on the stage state", async () => {
    const svc = await makeService(3231);
    await svc.initializePipeline(3231, "Test", "feat/3231-test");
    fireIpcEvent("stage.start", {
      issueNumber: 3231,
      stage: "feature-dev",
      repo: "test/repo",
      title: "feature-dev",
    });

    await svc.setStageAdapter("feature-dev", {
      adapter: "claude",
      source: "default",
    });

    const state = await svc.getState();
    expect(state!.stages["feature-dev"].adapter).toBe("claude");
    expect(state!.stages["feature-dev"].adapter_source).toBe("default");
    expect(state!.stages["feature-dev"].adapter_fallback_chain_used).toBeUndefined();
  });

  it("persists adapter_fallback_chain_used when length ≥ 2", async () => {
    const svc = await makeService(3231);
    await svc.initializePipeline(3231, "Test", "feat/3231-test");
    fireIpcEvent("stage.start", {
      issueNumber: 3231,
      stage: "feature-dev",
      repo: "test/repo",
      title: "feature-dev",
    });

    await svc.setStageAdapter("feature-dev", {
      adapter: "gemini",
      source: "fallback",
      adapterFallbackChainUsed: ["claude", "codex", "gemini"],
    });

    const state = await svc.getState();
    expect(state!.stages["feature-dev"].adapter).toBe("gemini");
    expect(state!.stages["feature-dev"].adapter_source).toBe("fallback");
    expect(state!.stages["feature-dev"].adapter_fallback_chain_used).toEqual([
      "claude",
      "codex",
      "gemini",
    ]);
  });

  it("clears adapter_fallback_chain_used when length is 1 (primary success)", async () => {
    const svc = await makeService(3231);
    await svc.initializePipeline(3231, "Test", "feat/3231-test");
    fireIpcEvent("stage.start", {
      issueNumber: 3231,
      stage: "feature-dev",
      repo: "test/repo",
      title: "feature-dev",
    });

    // First call writes a multi-hop trail
    await svc.setStageAdapter("feature-dev", {
      adapter: "gemini",
      source: "fallback",
      adapterFallbackChainUsed: ["claude", "gemini"],
    });
    let state = await svc.getState();
    expect(state!.stages["feature-dev"].adapter_fallback_chain_used).toEqual(["claude", "gemini"]);

    // Second call (e.g. retry with primary success) clears the trail.
    await svc.setStageAdapter("feature-dev", {
      adapter: "claude",
      source: "default",
      adapterFallbackChainUsed: ["claude"],
    });
    state = await svc.getState();
    expect(state!.stages["feature-dev"].adapter_fallback_chain_used).toBeUndefined();
  });

  it("clears adapter_fallback_chain_used when the field is absent on the decision", async () => {
    const svc = await makeService(3231);
    await svc.initializePipeline(3231, "Test", "feat/3231-test");
    fireIpcEvent("stage.start", {
      issueNumber: 3231,
      stage: "feature-dev",
      repo: "test/repo",
      title: "feature-dev",
    });

    await svc.setStageAdapter("feature-dev", {
      adapter: "claude",
      source: "default",
      adapterFallbackChainUsed: ["claude", "codex"],
    });

    await svc.setStageAdapter("feature-dev", {
      adapter: "claude",
      source: "default",
      // No adapterFallbackChainUsed field — clears any prior value.
    });

    const state = await svc.getState();
    expect(state!.stages["feature-dev"].adapter_fallback_chain_used).toBeUndefined();
  });
});
