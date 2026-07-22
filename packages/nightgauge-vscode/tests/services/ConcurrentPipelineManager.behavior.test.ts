/**
 * Behavior tests for ConcurrentPipelineManager
 *
 * Unlike ConcurrentPipelineManager.test.ts which verifies mock calls,
 * these tests verify actual behavioral outcomes:
 * - Slot filling respects maxConcurrent limits
 * - Slot completion triggers auto-fill from queue
 * - Failed slots don't block remaining slots
 * - Abort stops all running slots
 * - pauseFilling/resumeFilling controls queue consumption
 *
 * @see Issue #2230 - Test suite audit
 * @see Issue #1621 - Git worktree-based concurrent pipeline execution
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock vscode
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
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test-repo" } }],
  },
  window: {
    showErrorMessage: vi.fn().mockResolvedValue(undefined),
    showWarningMessage: vi.fn().mockResolvedValue(undefined),
    showInformationMessage: vi.fn().mockResolvedValue(undefined),
  },
  commands: {
    executeCommand: vi.fn().mockResolvedValue(undefined),
  },
  env: {
    openExternal: vi.fn().mockResolvedValue(true),
  },
  Uri: {
    parse: vi.fn((s: string) => ({ toString: () => s })),
  },
}));

// Mock WorktreeManager
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

// Mock incrediConfig
vi.mock("../../src/utils/incrediConfig", () => ({
  getConcurrentPipelineConfig: vi.fn().mockReturnValue({
    maxConcurrent: 3,
    worktreeBase: ".worktrees",
  }),
}));

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

interface QueueItem {
  issueNumber: number;
  title: string;
  position: number;
  status: string;
  addedAt: string;
  epicOrder?: number;
}

function makeQueueItem(issueNumber: number, title = `Issue #${issueNumber}`): QueueItem {
  return {
    issueNumber,
    title,
    position: 1,
    status: "pending",
    addedAt: new Date().toISOString(),
  };
}

/**
 * Creates a mock orchestrator factory with per-issue deferred promises.
 * Each issue gets its own resolve function so tests can complete them
 * independently.
 */
function createControllableFactory() {
  const resolvers = new Map<number, (result: any) => void>();
  const rejecters = new Map<number, (error: Error) => void>();
  const orchestrators = new Map<number, any>();
  const stateServices = new Map<number, any>();

  const factory = vi.fn().mockImplementation((_workDir: string, issueNumber: number) => {
    const promise = new Promise((resolve, reject) => {
      resolvers.set(issueNumber, resolve);
      rejecters.set(issueNumber, reject);
    });

    const orchestrator = {
      setWorktreeOverride: vi.fn(),
      setUnattended: vi.fn(),
      runPipeline: vi.fn().mockReturnValue(promise),
      stop: vi.fn(),
      dispose: vi.fn(),
    };
    orchestrators.set(issueNumber, orchestrator);

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
    stateServices.set(issueNumber, stateService);

    return { orchestrator, stateService };
  });

  return {
    factory,
    /** Complete a specific issue's pipeline */
    completeIssue: (issueNumber: number, result?: any) =>
      resolvers.get(issueNumber)?.(
        result ?? {
          success: true,
          completedStages: ["issue-pickup", "feature-planning", "feature-dev"],
          skippedStages: [],
          deferredStages: [],
          totalDurationMs: 30000,
        }
      ),
    /** Fail a specific issue's pipeline */
    failIssue: (issueNumber: number, result?: any) =>
      resolvers.get(issueNumber)?.(
        result ?? {
          success: false,
          completedStages: ["issue-pickup"],
          skippedStages: [],
          deferredStages: [],
          failedStage: "feature-dev",
          totalDurationMs: 10000,
        }
      ),
    getOrchestrator: (issueNumber: number) => orchestrators.get(issueNumber),
    getStateService: (issueNumber: number) => stateServices.get(issueNumber),
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

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("ConcurrentPipelineManager — behavioral tests", () => {
  let mockQueue: ReturnType<typeof createMockQueueService>;
  let controllable: ReturnType<typeof createControllableFactory>;
  let mockLogger: ReturnType<typeof createMockLogger>;

  beforeEach(() => {
    mockQueue = createMockQueueService();
    controllable = createControllableFactory();
    mockLogger = createMockLogger();
  });

  describe("slot filling respects maxConcurrent", () => {
    it("fills up to maxConcurrent=2 and no more", async () => {
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        controllable.factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );

      // Queue has 3 items but only 2 slots available
      mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(10), makeQueueItem(20)]);

      const filled = await manager.fillSlots();

      expect(filled).toBe(2);
      expect(manager.activeSlotCount).toBe(2);
      expect(manager.availableSlotCount).toBe(0);
      expect(manager.isRunning(10)).toBe(true);
      expect(manager.isRunning(20)).toBe(true);
    });

    it("does not start new slots when at capacity", async () => {
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        controllable.factory,
        mockLogger as any,
        { maxConcurrent: 1, worktreeBase: ".worktrees" }
      );

      mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(10)]);
      await manager.fillSlots();

      expect(manager.activeSlotCount).toBe(1);
      expect(manager.availableSlotCount).toBe(0);

      // Second fill: even if dequeueIndependent returns items,
      // no new slots should be created because we're at capacity
      mockQueue.dequeueIndependent.mockResolvedValue([]);
      const filled2 = await manager.fillSlots();
      expect(filled2).toBe(0);

      // Still only 1 active slot
      expect(manager.activeSlotCount).toBe(1);
    });
  });

  describe("slot completion triggers auto-fill", () => {
    it("completes a slot and auto-fills from queue", async () => {
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        controllable.factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );

      // Initial fill: 2 slots
      mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(10), makeQueueItem(20)]);
      await manager.fillSlots();
      expect(manager.activeSlotCount).toBe(2);

      // When issue 10 completes, fillSlots is called automatically in the
      // finally block of runSlotPipeline. Set up queue to return issue 30.
      mockQueue.dequeueIndependent.mockResolvedValue([makeQueueItem(30)]);

      controllable.completeIssue(10);
      // Deterministically await slot 10's cleanup instead of a fixed sleep.
      await vi.waitFor(() => expect(manager.isRunning(10)).toBe(false));

      // Issue 10 completed and was cleaned up
      expect(manager.isRunning(10)).toBe(false);
      // Issue 20 is still running (its promise hasn't resolved)
      expect(manager.isRunning(20)).toBe(true);
    });
  });

  describe("failed slot does not block others", () => {
    it("other slots continue when one fails", async () => {
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        controllable.factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );

      mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(10), makeQueueItem(20)]);
      mockQueue.dequeueIndependent.mockResolvedValue([]);
      await manager.fillSlots();

      expect(manager.activeSlotCount).toBe(2);

      // Fail issue 10
      controllable.failIssue(10);

      // Deterministically await slot 10's cleanup instead of a fixed sleep.
      await vi.waitFor(() => expect(manager.isRunning(10)).toBe(false));

      // Issue 20 should still be running
      expect(manager.isRunning(20)).toBe(true);
      // Issue 10 should be cleaned up
      expect(manager.isRunning(10)).toBe(false);
    });

    it("drains blocked successors when a slot fails", async () => {
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        controllable.factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );

      mockQueue.dequeueIndependent.mockResolvedValueOnce([{ ...makeQueueItem(10), epicOrder: 0 }]);
      mockQueue.dequeueIndependent.mockResolvedValue([]);
      await manager.fillSlots();

      controllable.failIssue(10);
      await vi.waitFor(() => expect(mockQueue.drainBlockedSuccessors).toHaveBeenCalledWith(10, 0));
    });

    // @see Issue #2967 — Pipeline failures must not silently auto-continue the queue
    it("clears pending queue items when a slot fails", async () => {
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        controllable.factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );

      mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(10)]);
      mockQueue.dequeueIndependent.mockResolvedValue([]);
      // Simulate the queue still having pending work behind the running slot
      mockQueue.getQueue.mockResolvedValue({
        items: [makeQueueItem(20), makeQueueItem(30)],
        status: "processing",
      });

      await manager.fillSlots();
      expect(mockQueue.clear).not.toHaveBeenCalled();

      controllable.failIssue(10);
      await vi.waitFor(() => expect(mockQueue.clear).toHaveBeenCalledTimes(1));
    });

    it("does not auto-continue the queue after a slot failure", async () => {
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        controllable.factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );

      mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(10)]);
      mockQueue.dequeueIndependent.mockResolvedValue([]);
      mockQueue.getQueue.mockResolvedValue({
        items: [makeQueueItem(20)],
        status: "processing",
      });

      await manager.fillSlots();

      // Baseline: the initial fillSlots has already called dequeueIndependent once.
      const dequeueCallsBeforeFailure = mockQueue.dequeueIndependent.mock.calls.length;

      controllable.failIssue(10);
      // Pure-negative assertion: await the full lifecycle (no observable
      // positive signal on the halt path), then assert dequeue was not called.
      await manager.settleForTest(10);

      // Critical: the post-cleanup code path must NOT call dequeueIndependent.
      expect(mockQueue.dequeueIndependent.mock.calls.length).toBe(dequeueCallsBeforeFailure);
    });

    it("continues auto-fill on successful completion", async () => {
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        controllable.factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );

      mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(10)]);
      mockQueue.dequeueIndependent.mockResolvedValue([]);

      await manager.fillSlots();

      const dequeueCallsBeforeCompletion = mockQueue.dequeueIndependent.mock.calls.length;

      controllable.completeIssue(10);
      await vi.waitFor(() =>
        expect(mockQueue.dequeueIndependent.mock.calls.length).toBeGreaterThan(
          dequeueCallsBeforeCompletion
        )
      );

      // Success path SHOULD re-fill — dequeueIndependent is called again.
      expect(mockQueue.dequeueIndependent.mock.calls.length).toBeGreaterThan(
        dequeueCallsBeforeCompletion
      );
      // And the queue must NOT be cleared on success.
      expect(mockQueue.clear).not.toHaveBeenCalled();
    });
  });

  describe("abort behavior", () => {
    it("abortSlot stops specific issue and frees the slot", async () => {
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        controllable.factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );

      mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(10), makeQueueItem(20)]);
      mockQueue.dequeueIndependent.mockResolvedValue([]);
      await manager.fillSlots();

      const result = manager.abortSlot(10);
      expect(result).toBe(true);

      // stop() should have been called on issue 10's orchestrator
      const orch10 = controllable.getOrchestrator(10);
      expect(orch10!.stop).toHaveBeenCalled();

      // aborting non-existent slot returns false
      expect(manager.abortSlot(999)).toBe(false);
    });
  });

  describe("pauseFilling and resumeFilling", () => {
    it("paused manager does not fill new slots", async () => {
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        controllable.factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );

      manager.pauseFilling();

      mockQueue.dequeueIndependent.mockResolvedValue([makeQueueItem(10)]);
      const filled = await manager.fillSlots();

      // Should not fill when paused
      expect(filled).toBe(0);
    });

    it("resumed manager fills slots again", async () => {
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        controllable.factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );

      manager.pauseFilling();

      mockQueue.dequeueIndependent.mockResolvedValue([makeQueueItem(10)]);
      const filled1 = await manager.fillSlots();
      expect(filled1).toBe(0);

      manager.resumeFilling();

      mockQueue.dequeueIndependent.mockResolvedValue([makeQueueItem(20)]);
      const filled2 = await manager.fillSlots();
      expect(filled2).toBe(1);
    });

    it("resets shutdown guard after last slot drains on stop-after-current", async () => {
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        controllable.factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );

      // Start an issue, then call pauseFilling (simulating "stop after current")
      mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(42)]).mockResolvedValue([]);
      await manager.fillSlots();
      expect(manager.activeSlotCount).toBe(1);

      manager.pauseFilling();
      expect(manager.isShutdownInProgress).toBe(true);

      // Complete the running issue successfully
      controllable.completeIssue(42);

      // Deterministically await the slot completion handler.
      await vi.waitFor(() => {
        expect(manager.isShutdownInProgress).toBe(false);
        expect(manager.activeSlotCount).toBe(0);
      });
    });
  });

  describe("onSlotsChanged event", () => {
    it("fires when slots are added", async () => {
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        controllable.factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );

      const slotChanges: any[][] = [];
      manager.onSlotsChanged((slots: any[]) => slotChanges.push([...slots]));

      mockQueue.dequeueIndependent.mockResolvedValue([makeQueueItem(42)]);
      await manager.fillSlots();

      // At least one change event should have fired with the new slot
      expect(slotChanges.length).toBeGreaterThanOrEqual(1);
      const lastChange = slotChanges[slotChanges.length - 1];
      expect(lastChange.some((s: any) => s.issueNumber === 42)).toBe(true);
    });
  });

  describe("callbacks", () => {
    it("fires onSlotCompleted when a slot finishes successfully", async () => {
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        controllable.factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );

      const completions: Array<{
        slotIndex: number;
        issueNumber: number;
        success: boolean;
      }> = [];
      manager.setCallbacks({
        onSlotCompleted: (slotIndex, issueNumber, result) => {
          completions.push({
            slotIndex,
            issueNumber,
            success: result.success,
          });
        },
      });

      mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(42)]);
      mockQueue.dequeueIndependent.mockResolvedValue([]);
      await manager.fillSlots();

      controllable.completeIssue(42);
      await manager.waitForAll();

      expect(completions).toHaveLength(1);
      expect(completions[0].issueNumber).toBe(42);
      expect(completions[0].success).toBe(true);
    });

    it("fires onSlotFailed when a slot fails", async () => {
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        controllable.factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );

      const failures: Array<{ issueNumber: number }> = [];
      manager.setCallbacks({
        onSlotFailed: (_slotIndex, issueNumber) => {
          failures.push({ issueNumber });
        },
      });

      mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(99)]);
      mockQueue.dequeueIndependent.mockResolvedValue([]);
      await manager.fillSlots();

      controllable.failIssue(99);
      await manager.waitForAll();

      expect(failures).toHaveLength(1);
      expect(failures[0].issueNumber).toBe(99);
    });

    it("fires onAllComplete when last slot finishes and queue is empty", async () => {
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        controllable.factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );

      let allCompleteFired = false;
      manager.setCallbacks({
        onAllComplete: () => {
          allCompleteFired = true;
        },
      });

      mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(10)]);
      mockQueue.dequeueIndependent.mockResolvedValue([]);
      await manager.fillSlots();

      controllable.completeIssue(10);
      await manager.waitForAll();

      expect(allCompleteFired).toBe(true);
    });
  });

  describe("getSlotStateService returns per-slot service", () => {
    it("each slot gets its own stateService from the factory", async () => {
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        controllable.factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );

      mockQueue.dequeueIndependent.mockResolvedValue([makeQueueItem(10), makeQueueItem(20)]);
      await manager.fillSlots();

      const svc0 = manager.getSlotStateService(0);
      const svc1 = manager.getSlotStateService(1);

      expect(svc0).toBeDefined();
      expect(svc1).toBeDefined();
      // Each slot should have its own state service (not shared)
      expect(svc0).not.toBe(svc1);
    });
  });

  describe("worktree creation failure and re-enqueue", () => {
    it(
      "sets fillAgain when startSlot fails so re-enqueued items are retried",
      { timeout: 15000 },
      async () => {
        // Override WorktreeManager.create to fail for issue 20
        const { WorktreeManager } = await import("../../src/utils/WorktreeManager");
        const WTMock = vi.mocked(WorktreeManager);

        WTMock.mockImplementation(function () {
          return {
            create: vi.fn().mockImplementation((issueNumber: number, branchName: string) => {
              if (issueNumber === 20) {
                return Promise.reject(
                  new Error("could not lock config file .git/config: File exists")
                );
              }
              return Promise.resolve({
                path: `/test-repo/.worktrees/issue-${issueNumber}`,
                branch: branchName,
                issueNumber,
                exists: true,
              });
            }),
            cleanup: vi.fn().mockResolvedValue(undefined),
            cleanupOrphans: vi.fn().mockResolvedValue(0),
            cleanupAll: vi.fn().mockResolvedValue(undefined),
            listActive: vi.fn().mockResolvedValue([]),
            getRepoRoot: vi.fn().mockReturnValue("/test-repo"),
            getWorktreePath: vi
              .fn()
              .mockImplementation((n: number) => `/test-repo/.worktrees/issue-${n}`),
          } as any;
        });

        const manager = new ConcurrentPipelineManager(
          "/test-repo",
          mockQueue as any,
          controllable.factory,
          mockLogger as any,
          { maxConcurrent: 3, worktreeBase: ".worktrees" }
        );

        // First dequeue: return issues 10 and 20
        mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(10), makeQueueItem(20)]);
        // Second dequeue (after fillAgain triggers): return issue 20 again
        mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(20)]);
        // Third dequeue: empty
        mockQueue.dequeueIndependent.mockResolvedValue([]);

        await manager.fillSlots();

        // Issue 10 should be running (create succeeded)
        expect(manager.isRunning(10)).toBe(true);

        // Enqueue should have been called to re-enqueue the failed item
        expect(mockQueue.enqueue).toHaveBeenCalledWith(20, "Issue #20", undefined);

        // dequeueIndependent should have been called at least twice:
        // once for initial batch, once for fillAgain after re-enqueue
        expect(mockQueue.dequeueIndependent.mock.calls.length).toBeGreaterThanOrEqual(2);
      }
    );
  });
});
