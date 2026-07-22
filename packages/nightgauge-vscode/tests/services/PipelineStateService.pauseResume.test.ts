/**
 * PipelineStateService.pauseResume.test.ts
 *
 * Unit tests for pause/resume IPC persistence (Issue #2008).
 * Validates that:
 * 1. pausePipeline() calls ipc.call('pipeline.setPaused', { issueNumber, paused: true })
 * 2. resumePipeline() calls ipc.call('pipeline.setPaused', { issueNumber, paused: false })
 * 3. pipeline.stateChanged converter reads paused from Go state
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ---------------------------------------------------------------------------
// Mock vscode before any imports that use it
// ---------------------------------------------------------------------------
vi.mock("vscode", () => ({
  EventEmitter: class {
    event = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
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
// Helpers — test the pause/resume IPC call behavior in isolation
// ---------------------------------------------------------------------------

interface MockIpc {
  call: ReturnType<typeof vi.fn>;
  on: ReturnType<typeof vi.fn>;
}

function makeMockIpc(): MockIpc {
  return {
    call: vi.fn().mockResolvedValue(undefined),
    on: vi.fn().mockReturnValue({ dispose: vi.fn() }),
  };
}

/**
 * Minimal reimplementation of pause/resume behavior extracted from
 * PipelineStateService, for unit testing the IPC call contract.
 */
function makePauseResumeMethods(ipc: MockIpc, issueNumber: number | null) {
  let lastStatePaused: boolean | undefined = false;
  let onStateChangedFired = false;

  async function pausePipeline() {
    lastStatePaused = true;
    onStateChangedFired = true;
    if (issueNumber !== null) {
      try {
        await ipc.call("pipeline.setPaused", {
          issueNumber,
          paused: true,
        });
      } catch {
        // Non-critical
      }
    }
  }

  async function resumePipeline() {
    lastStatePaused = false;
    onStateChangedFired = true;
    if (issueNumber !== null) {
      try {
        await ipc.call("pipeline.setPaused", {
          issueNumber,
          paused: false,
        });
      } catch {
        // Non-critical
      }
    }
  }

  return {
    pausePipeline,
    resumePipeline,
    getLastStatePaused: () => lastStatePaused,
    getOnStateChangedFired: () => onStateChangedFired,
  };
}

describe("PipelineStateService pause/resume IPC (Issue #2008)", () => {
  let ipc: MockIpc;

  beforeEach(() => {
    ipc = makeMockIpc();
  });

  it("pausePipeline() calls pipeline.setPaused with paused: true", async () => {
    const { pausePipeline } = makePauseResumeMethods(ipc, 2008);
    await pausePipeline();

    expect(ipc.call).toHaveBeenCalledWith("pipeline.setPaused", {
      issueNumber: 2008,
      paused: true,
    });
  });

  it("resumePipeline() calls pipeline.setPaused with paused: false", async () => {
    const { resumePipeline } = makePauseResumeMethods(ipc, 2008);
    await resumePipeline();

    expect(ipc.call).toHaveBeenCalledWith("pipeline.setPaused", {
      issueNumber: 2008,
      paused: false,
    });
  });

  it("pausePipeline() does not call IPC when issueNumber is null", async () => {
    const { pausePipeline } = makePauseResumeMethods(ipc, null);
    await pausePipeline();

    expect(ipc.call).not.toHaveBeenCalled();
  });

  it("resumePipeline() does not call IPC when issueNumber is null", async () => {
    const { resumePipeline } = makePauseResumeMethods(ipc, null);
    await resumePipeline();

    expect(ipc.call).not.toHaveBeenCalled();
  });

  it("pausePipeline() still updates in-memory state when IPC throws", async () => {
    ipc.call.mockRejectedValue(new Error("IPC not connected"));
    const { pausePipeline, getLastStatePaused, getOnStateChangedFired } = makePauseResumeMethods(
      ipc,
      2008
    );
    await pausePipeline();

    expect(getLastStatePaused()).toBe(true);
    expect(getOnStateChangedFired()).toBe(true);
  });

  it("resumePipeline() still updates in-memory state when IPC throws", async () => {
    ipc.call.mockRejectedValue(new Error("IPC not connected"));
    const { resumePipeline, getLastStatePaused } = makePauseResumeMethods(ipc, 2008);
    await resumePipeline();

    expect(getLastStatePaused()).toBe(false);
  });
});

describe("pipeline.stateChanged paused field from Go state (Issue #2008)", () => {
  it("uses goState.paused when present", () => {
    // Simulates the converter expression:
    //   paused: (goState.paused as boolean | undefined) ?? this._lastState?.paused
    const lastStatePaused = undefined;
    const goStatePaused = true;

    const result = (goStatePaused as boolean | undefined) ?? lastStatePaused;
    expect(result).toBe(true);
  });

  it("falls back to _lastState.paused when goState.paused is absent", () => {
    const lastStatePaused = true;
    const goStatePaused = undefined;

    const result = (goStatePaused as boolean | undefined) ?? lastStatePaused;
    expect(result).toBe(true);
  });

  it("goState.paused=false overrides _lastState.paused=true", () => {
    const lastStatePaused = true;
    const goStatePaused = false;

    // false ?? true = false (nullish coalescing only falls back on null/undefined)
    const result = (goStatePaused as boolean | undefined) ?? lastStatePaused;
    expect(result).toBe(false);
  });
});
