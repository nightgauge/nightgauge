/**
 * Issue #3111 — abortAll() must bound waitForIdle() with a hard deadline.
 * Without it, a slot stuck mid-stop strands isShuttingDown=true forever, and
 * the IssueQueueService shutdownGuard silently refuses every subsequent
 * enqueue (looking like "drag-to-queue does nothing after disconnect").
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

const { showWarningMessage } = vi.hoisted(() => ({
  showWarningMessage: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("vscode", () => ({
  EventEmitter: class {
    private listeners: Array<(...args: any[]) => void> = [];
    event = (listener: (...args: any[]) => void) => {
      this.listeners.push(listener);
      return {
        dispose: () => {
          this.listeners = this.listeners.filter((l) => l !== listener);
        },
      };
    };
    fire = (data: any) => {
      this.listeners.forEach((l) => l(data));
    };
    dispose = vi.fn();
  },
  workspace: { workspaceFolders: [{ uri: { fsPath: "/test-repo" } }] },
  window: {
    showErrorMessage: vi.fn().mockResolvedValue(undefined),
    showWarningMessage,
    showInformationMessage: vi.fn().mockResolvedValue(undefined),
  },
  commands: { executeCommand: vi.fn().mockResolvedValue(undefined) },
  env: { openExternal: vi.fn().mockResolvedValue(true) },
  Uri: { parse: vi.fn((s: string) => ({ toString: () => s })) },
}));

vi.mock("../../src/utils/WorktreeManager", () => ({
  WorktreeManager: vi.fn(function () {
    return {
      create: vi.fn().mockImplementation((issueNumber: number, branchName: string) =>
        Promise.resolve({
          path: `/test-repo/.worktrees/issue-${issueNumber}`,
          branch: branchName,
          issueNumber,
          exists: true,
        })
      ),
      cleanup: vi.fn().mockResolvedValue(undefined),
      cleanupOrphans: vi.fn().mockResolvedValue(0),
      cleanupAll: vi.fn().mockResolvedValue(undefined),
      listActive: vi.fn().mockResolvedValue([]),
      getRepoRoot: vi.fn().mockReturnValue("/test-repo"),
    };
  }),
}));

vi.mock("../../src/utils/incrediConfig", () => ({
  getConcurrentPipelineConfig: vi.fn().mockReturnValue({
    maxConcurrent: 2,
    worktreeBase: ".worktrees",
  }),
}));

vi.mock("../../src/utils/skillRunner", () => ({
  killAllActiveProcesses: vi.fn(),
}));

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";

function makeQueueItem(issueNumber: number) {
  return {
    issueNumber,
    title: `Issue #${issueNumber}`,
    position: 1,
    status: "pending",
    addedAt: new Date().toISOString(),
  };
}

function createMockQueueService() {
  return {
    dequeueIndependent: vi.fn().mockResolvedValue([]),
    updateActiveSlots: vi.fn().mockResolvedValue(undefined),
    drainBlockedSuccessors: vi.fn().mockResolvedValue([]),
    enqueue: vi.fn().mockResolvedValue(null),
    clear: vi.fn().mockResolvedValue(undefined),
    getQueue: vi.fn().mockResolvedValue({ items: [], status: "idle" }),
  };
}

function createMockLogger() {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
    getChannel: vi.fn(),
  };
}

/**
 * Factory that returns orchestrators whose runPipeline() never resolves and
 * whose stop() is a no-op — simulating a slot stuck mid-execution that does
 * not honor the abort signal (e.g. wedged subprocess, lost network during
 * worktree cleanup).
 */
function createStuckFactory() {
  return vi.fn().mockImplementation((_workDir: string, _issueNumber: number) => {
    const orchestrator = {
      setWorktreeOverride: vi.fn(),
      setUnattended: vi.fn(),
      runPipeline: vi.fn().mockReturnValue(new Promise(() => {})), // never resolves
      stop: vi.fn(), // no-op — simulates a stop that doesn't actually unblock runPipeline
      dispose: vi.fn(),
    };
    const stateService = {
      onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onPhaseStart: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onPhaseComplete: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onUnifiedTokenUsage: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      getState: vi.fn().mockResolvedValue(null),
      initEmpty: vi.fn(),
      setMeta: vi.fn(),
      dispose: vi.fn(),
    };
    return { orchestrator, stateService };
  });
}

describe("ConcurrentPipelineManager.abortAll — timeout (Issue #3111)", () => {
  let mockQueue: ReturnType<typeof createMockQueueService>;
  let mockLogger: ReturnType<typeof createMockLogger>;

  beforeEach(() => {
    vi.useFakeTimers();
    mockQueue = createMockQueueService();
    mockLogger = createMockLogger();
    showWarningMessage.mockClear();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("force-clears slots and resets isShutdownInProgress when waitForIdle exceeds deadline", async () => {
    const factory = createStuckFactory();
    const manager = new ConcurrentPipelineManager(
      "/test-repo",
      mockQueue as any,
      factory,
      mockLogger as any,
      { maxConcurrent: 2, worktreeBase: ".worktrees" }
    );

    // Fill a slot with a stuck pipeline.
    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
    await manager.fillSlots();
    expect(manager.activeSlotCount).toBe(1);

    // Kick off abort — without the timeout fix this would never resolve.
    const abortPromise = manager.abortAll();

    // Advance past the 30s deadline. Use runAllTimersAsync so the timeout
    // fires AND the chained microtasks settle.
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    expect(manager.isShutdownInProgress).toBe(false);
    expect(manager.activeSlotCount).toBe(0);
    expect(mockLogger.warn).toHaveBeenCalledWith(
      "abortAll exceeded deadline — force-clearing slots",
      expect.objectContaining({ stuckIssues: [282] })
    );
    expect(showWarningMessage).toHaveBeenCalledWith(expect.stringContaining("force-cleared"));
  });

  it("does not warn or force-clear when slots drain normally", async () => {
    // No slots filled — abortAll should complete instantly via waitForIdle.
    const factory = createStuckFactory();
    const manager = new ConcurrentPipelineManager(
      "/test-repo",
      mockQueue as any,
      factory,
      mockLogger as any,
      { maxConcurrent: 2, worktreeBase: ".worktrees" }
    );

    const abortPromise = manager.abortAll();
    // No timer advancement needed — empty slot map drains immediately.
    await abortPromise;

    expect(manager.isShutdownInProgress).toBe(false);
    expect(mockLogger.warn).not.toHaveBeenCalledWith(
      "abortAll exceeded deadline — force-clearing slots",
      expect.anything()
    );
    expect(showWarningMessage).not.toHaveBeenCalled();
  });
});
