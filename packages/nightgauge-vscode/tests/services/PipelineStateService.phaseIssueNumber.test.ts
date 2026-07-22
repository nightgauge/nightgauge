/**
 * PipelineStateService — issueNumber in phase event payloads (#3486)
 *
 * Verifies that startPhase() and completePhase() include the service's
 * issueNumber in the fired event payloads so subscribers can filter
 * by issue and avoid cross-slot contamination.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ---------------------------------------------------------------------------
// IPC mock
// ---------------------------------------------------------------------------

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
      call: vi.fn().mockResolvedValue({}),
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
  return PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("PipelineStateService — issueNumber in phase event payloads (#3486)", () => {
  beforeEach(() => {
    vi.resetModules();
  });

  it("includes issueNumber in the onPhaseStart event payload", async () => {
    const svc = await makeService(42);

    const events: unknown[] = [];
    svc.onPhaseStart((e) => events.push(e));

    await svc.startPhase("feature-dev", "implementation", 8);

    expect(events).toHaveLength(1);
    expect((events[0] as any).issueNumber).toBe(42);
    expect((events[0] as any).stage).toBe("feature-dev");
    expect((events[0] as any).phase).toBe("implementation");
  });

  it("includes issueNumber in the onPhaseComplete event payload", async () => {
    const svc = await makeService(99);

    // Start first so completePhase has a valid phase to complete
    await svc.startPhase("feature-dev", "implementation", 8);

    const events: unknown[] = [];
    svc.onPhaseComplete((e) => events.push(e));

    await svc.completePhase("feature-dev", "implementation", 8);

    expect(events).toHaveLength(1);
    expect((events[0] as any).issueNumber).toBe(99);
    expect((events[0] as any).stage).toBe("feature-dev");
    expect((events[0] as any).phase).toBe("implementation");
  });

  it("emits issueNumber=undefined (as undefined, not null) when no issue is set", async () => {
    const { PipelineStateService } = await import("../../src/services/PipelineStateService");
    PipelineStateService.resetInstance();
    const svc = PipelineStateService.getInstance("/tmp/repo");

    const events: unknown[] = [];
    svc.onPhaseStart((e) => events.push(e));

    await svc.startPhase("feature-dev", "implementation", 8);

    expect(events).toHaveLength(1);
    // null issueNumber converts to undefined in payload so guard `!== undefined` passes through
    expect((events[0] as any).issueNumber).toBeUndefined();
  });
});
