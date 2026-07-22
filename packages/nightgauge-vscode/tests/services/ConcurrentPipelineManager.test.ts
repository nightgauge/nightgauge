/**
 * Tests for ConcurrentPipelineManager
 *
 * Verifies concurrent pipeline slot management:
 * - Slot creation and tracking
 * - Fill slots from queue
 * - Slot completion and cleanup
 * - Abort all/single slots
 * - Concurrent mode detection
 *
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
}));

// Mock WorktreeManager
vi.mock("../../src/utils/WorktreeManager", () => ({
  WorktreeManager: vi.fn(function () {
    return {
      create: vi.fn().mockResolvedValue({
        path: "/test-repo/.worktrees/issue-42",
        branch: "feat/42-test",
        issueNumber: 42,
        exists: true,
      }),
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
    maxConcurrent: 2,
    worktreeBase: ".worktrees",
  }),
}));

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";

// Mock queue service
function createMockQueueService() {
  return {
    dequeueIndependent: vi.fn().mockResolvedValue([]),
    updateActiveSlots: vi.fn().mockResolvedValue(undefined),
  };
}

// Mock orchestrator factory - uses a deferred promise to prevent instant
// completion loops (fire-and-forget runSlotPipeline calls fillSlots recursively)
function createMockOrchestratorFactory() {
  let resolveRun: ((value: any) => void) | undefined;
  const runPromise = new Promise((resolve) => {
    resolveRun = resolve;
  });

  const mockOrchestrator = {
    setWorktreeOverride: vi.fn(),
    setUnattended: vi.fn(),
    runPipeline: vi.fn().mockReturnValue(runPromise),
    stop: vi.fn(),
    dispose: vi.fn(),
  };

  const mockStateService = {
    onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
    onBatchStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
    onPhaseStart: vi.fn().mockReturnValue({ dispose: vi.fn() }),
    onPhaseComplete: vi.fn().mockReturnValue({ dispose: vi.fn() }),
    onUnifiedTokenUsage: vi.fn().mockReturnValue({ dispose: vi.fn() }),
    getState: vi.fn().mockResolvedValue(null),
    initEmpty: vi.fn(),
    setMeta: vi.fn(),
    dispose: vi.fn(),
  };

  return {
    factory: vi.fn().mockReturnValue({
      orchestrator: mockOrchestrator,
      stateService: mockStateService,
    }),
    mockOrchestrator,
    /** Resolve the deferred runPipeline promise */
    completeRun: (result?: any) =>
      resolveRun?.(
        result ?? {
          success: true,
          completedStages: ["issue-pickup"],
          skippedStages: [],
          deferredStages: [],
          totalDurationMs: 5000,
        }
      ),
  };
}

// Mock logger
function createMockLogger() {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
    getChannel: vi.fn(),
  };
}

describe("ConcurrentPipelineManager", () => {
  let manager: ConcurrentPipelineManager;
  let mockQueue: ReturnType<typeof createMockQueueService>;
  let orchestratorMocks: ReturnType<typeof createMockOrchestratorFactory>;
  let mockLogger: ReturnType<typeof createMockLogger>;

  beforeEach(() => {
    mockQueue = createMockQueueService();
    orchestratorMocks = createMockOrchestratorFactory();
    mockLogger = createMockLogger();

    manager = new ConcurrentPipelineManager(
      "/test-repo",
      mockQueue as any,
      orchestratorMocks.factory,
      mockLogger as any,
      { maxConcurrent: 2, worktreeBase: ".worktrees" }
    );
  });

  describe("isConcurrentEnabled", () => {
    it("returns true when maxConcurrent > 1", () => {
      expect(manager.isConcurrentEnabled).toBe(true);
    });

    it("returns true when maxConcurrent is 1 (unified worktree path, #1831)", () => {
      const singleSlot = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        orchestratorMocks.factory,
        mockLogger as any,
        { maxConcurrent: 1, worktreeBase: ".worktrees" }
      );
      expect(singleSlot.isConcurrentEnabled).toBe(true);
    });
  });

  describe("maxConcurrentSlots", () => {
    it("returns configured maxConcurrent value", () => {
      expect(manager.maxConcurrentSlots).toBe(2);
    });

    it("returns 1 for single-slot mode", () => {
      const singleSlot = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        orchestratorMocks.factory,
        mockLogger as any,
        { maxConcurrent: 1, worktreeBase: ".worktrees" }
      );
      expect(singleSlot.maxConcurrentSlots).toBe(1);
    });
  });

  describe("slot counting", () => {
    it("starts with zero active slots", () => {
      expect(manager.activeSlotCount).toBe(0);
      expect(manager.availableSlotCount).toBe(2);
    });
  });

  describe("fillSlots", () => {
    it("returns 0 when no items in queue", async () => {
      mockQueue.dequeueIndependent.mockResolvedValue([]);
      const filled = await manager.fillSlots();
      expect(filled).toBe(0);
    });

    it("dequeues independent issues from queue", async () => {
      mockQueue.dequeueIndependent.mockResolvedValue([
        {
          issueNumber: 42,
          title: "Test issue",
          position: 1,
          status: "pending",
          addedAt: new Date().toISOString(),
        },
      ]);

      const filled = await manager.fillSlots();
      expect(filled).toBe(1);

      expect(mockQueue.dequeueIndependent).toHaveBeenCalledWith(2, []);
    });

    it("passes running items with repo to dequeueIndependent (per-repo cap)", async () => {
      // First fill with one item
      mockQueue.dequeueIndependent.mockResolvedValueOnce([
        {
          issueNumber: 42,
          title: "First",
          position: 1,
          status: "pending",
          addedAt: new Date().toISOString(),
        },
      ]);
      await manager.fillSlots();

      // Second fill should pass the running item as {repo, number} so the
      // scheduler can enforce per-repo concurrency caps.
      mockQueue.dequeueIndependent.mockResolvedValueOnce([]);
      await manager.fillSlots();

      expect(mockQueue.dequeueIndependent).toHaveBeenLastCalledWith(1, [{ repo: "", number: 42 }]);
    });
  });

  describe("getActiveSlots", () => {
    it("returns empty array initially", () => {
      expect(manager.getActiveSlots()).toEqual([]);
    });

    it("returns slot info after filling", async () => {
      mockQueue.dequeueIndependent.mockResolvedValue([
        {
          issueNumber: 42,
          title: "Test",
          position: 1,
          status: "pending",
          addedAt: new Date().toISOString(),
        },
      ]);

      await manager.fillSlots();
      const slots = manager.getActiveSlots();

      expect(slots).toHaveLength(1);
      expect(slots[0].issueNumber).toBe(42);
      expect(slots[0].slotIndex).toBe(0);
    });
  });

  describe("isRunning", () => {
    it("returns false for non-running issues", () => {
      expect(manager.isRunning(42)).toBe(false);
    });

    it("returns true for running issues", async () => {
      mockQueue.dequeueIndependent.mockResolvedValue([
        {
          issueNumber: 42,
          title: "Test",
          position: 1,
          status: "pending",
          addedAt: new Date().toISOString(),
        },
      ]);

      await manager.fillSlots();
      expect(manager.isRunning(42)).toBe(true);
      expect(manager.isRunning(99)).toBe(false);
    });
  });

  describe("abortSlot", () => {
    it("returns false for non-existent slot", () => {
      expect(manager.abortSlot(999)).toBe(false);
    });
  });

  describe("callbacks", () => {
    it("fires onSlotStarted when slot begins", async () => {
      const onSlotStarted = vi.fn();
      manager.setCallbacks({ onSlotStarted });

      mockQueue.dequeueIndependent.mockResolvedValue([
        {
          issueNumber: 42,
          title: "Test Issue",
          position: 1,
          status: "pending",
          addedAt: new Date().toISOString(),
        },
      ]);

      await manager.fillSlots();

      expect(onSlotStarted).toHaveBeenCalledWith(
        0,
        42,
        "Test Issue",
        expect.anything(),
        undefined,
        undefined
      );
    });

    it("marks the slot orchestrator unattended so budget prompts auto-escalate", async () => {
      mockQueue.dequeueIndependent.mockResolvedValue([
        {
          issueNumber: 42,
          title: "Test Issue",
          position: 1,
          status: "pending",
          addedAt: new Date().toISOString(),
        },
      ]);

      await manager.fillSlots();

      // Autonomous concurrent slots have no human at the modal — the
      // orchestrator must be told so budget/ceiling escalations resolve
      // automatically instead of blocking on showWarningMessage.
      expect(orchestratorMocks.mockOrchestrator.setUnattended).toHaveBeenCalledWith(true);
    });
  });

  describe("single-slot mode (maxConcurrent=1, #1831)", () => {
    let singleSlotManager: ConcurrentPipelineManager;
    let singleSlotQueue: ReturnType<typeof createMockQueueService>;
    let singleSlotMocks: ReturnType<typeof createMockOrchestratorFactory>;

    beforeEach(() => {
      singleSlotQueue = createMockQueueService();
      singleSlotMocks = createMockOrchestratorFactory();

      singleSlotManager = new ConcurrentPipelineManager(
        "/test-repo",
        singleSlotQueue as any,
        singleSlotMocks.factory,
        mockLogger as any,
        { maxConcurrent: 1, worktreeBase: ".worktrees" }
      );
    });

    it("fills single slot with one issue", async () => {
      singleSlotQueue.dequeueIndependent.mockResolvedValue([
        {
          issueNumber: 42,
          title: "Single slot test",
          position: 1,
          status: "pending",
          addedAt: new Date().toISOString(),
        },
      ]);

      const filled = await singleSlotManager.fillSlots();
      expect(filled).toBe(1);
      expect(singleSlotManager.activeSlotCount).toBe(1);
      expect(singleSlotManager.availableSlotCount).toBe(0);
    });

    it("has no available slots when one issue is running", async () => {
      singleSlotQueue.dequeueIndependent.mockResolvedValue([
        {
          issueNumber: 42,
          title: "First",
          position: 1,
          status: "pending",
          addedAt: new Date().toISOString(),
        },
      ]);

      await singleSlotManager.fillSlots();
      expect(singleSlotManager.availableSlotCount).toBe(0);

      // Second fill should dequeue nothing (no slots available)
      singleSlotQueue.dequeueIndependent.mockResolvedValue([]);
      const filled2 = await singleSlotManager.fillSlots();
      expect(filled2).toBe(0);
    });
  });

  describe("getSlotStateService (#1634)", () => {
    it("returns undefined when no slot is active at the given index", () => {
      expect(manager.getSlotStateService(0)).toBeUndefined();
      expect(manager.getSlotStateService(1)).toBeUndefined();
    });

    it("returns the stateService for an active slot index", async () => {
      mockQueue.dequeueIndependent.mockResolvedValue([
        {
          issueNumber: 42,
          title: "Test Issue",
          position: 1,
          status: "pending",
          addedAt: new Date().toISOString(),
        },
      ]);

      await manager.fillSlots();

      const service = manager.getSlotStateService(0);
      expect(service).toBeDefined();
      expect(service).toBe(orchestratorMocks.factory.mock.results[0].value.stateService);
    });

    it("returns undefined for an inactive slot index when another slot is active", async () => {
      mockQueue.dequeueIndependent.mockResolvedValue([
        {
          issueNumber: 42,
          title: "Test Issue",
          position: 1,
          status: "pending",
          addedAt: new Date().toISOString(),
        },
      ]);

      await manager.fillSlots();

      expect(manager.getSlotStateService(0)).toBeDefined();
      expect(manager.getSlotStateService(1)).toBeUndefined();
    });
  });

  describe("stateService disposal on slot cleanup (#1634)", () => {
    it("calls dispose on stateService when slot pipeline completes", async () => {
      mockQueue.dequeueIndependent.mockResolvedValueOnce([
        {
          issueNumber: 42,
          title: "Test Issue",
          position: 1,
          status: "pending",
          addedAt: new Date().toISOString(),
        },
      ]);
      // No more items after slot completes
      mockQueue.dequeueIndependent.mockResolvedValue([]);
      mockQueue.updateActiveSlots = vi.fn().mockResolvedValue(undefined);
      // Add drainBlockedSuccessors mock (called after failed runs)
      (mockQueue as any).drainBlockedSuccessors = vi.fn().mockResolvedValue([]);

      await manager.fillSlots();

      const { stateService } = orchestratorMocks.factory.mock.results[0].value;

      // Complete the pipeline run to trigger cleanup
      orchestratorMocks.completeRun();
      await manager.waitForAll();

      expect(stateService.dispose).toHaveBeenCalled();
    });
  });

  describe("already-resolved issues skip successor drain", () => {
    it("does not drain blocked successors when issue was already closed", async () => {
      mockQueue.dequeueIndependent.mockResolvedValueOnce([
        {
          issueNumber: 99,
          title: "Already Closed Issue",
          position: 1,
          status: "pending",
          addedAt: new Date().toISOString(),
          epicOrder: 1,
        },
      ]);
      mockQueue.dequeueIndependent.mockResolvedValue([]);
      mockQueue.updateActiveSlots = vi.fn().mockResolvedValue(undefined);
      const drainMock = vi.fn().mockResolvedValue([]);
      (mockQueue as any).drainBlockedSuccessors = drainMock;

      await manager.fillSlots();

      // Resolve with already-resolved outcome (issue was already closed)
      orchestratorMocks.completeRun({
        success: false,
        completedStages: [],
        skippedStages: [],
        deferredStages: [],
        failedStage: "pipeline-start",
        outcomeType: "already-resolved",
        totalDurationMs: 100,
      });
      await manager.waitForAll();

      expect(drainMock).not.toHaveBeenCalled();
    });

    it("still drains blocked successors on real failures", async () => {
      mockQueue.dequeueIndependent.mockResolvedValueOnce([
        {
          issueNumber: 100,
          title: "Real Failure",
          position: 1,
          status: "pending",
          addedAt: new Date().toISOString(),
          epicOrder: 1,
        },
      ]);
      mockQueue.dequeueIndependent.mockResolvedValue([]);
      mockQueue.updateActiveSlots = vi.fn().mockResolvedValue(undefined);
      const drainMock = vi.fn().mockResolvedValue([]);
      (mockQueue as any).drainBlockedSuccessors = drainMock;

      await manager.fillSlots();

      // Resolve with a real failure (no outcomeType)
      orchestratorMocks.completeRun({
        success: false,
        completedStages: ["issue-pickup"],
        skippedStages: [],
        deferredStages: [],
        failedStage: "feature-dev",
        totalDurationMs: 30000,
      });
      await manager.waitForAll();

      expect(drainMock).toHaveBeenCalledWith(100, 1);
    });
  });

  describe("dispose", () => {
    it("aborts all slots on dispose", async () => {
      mockQueue.dequeueIndependent.mockResolvedValue([
        {
          issueNumber: 42,
          title: "Test",
          position: 1,
          status: "pending",
          addedAt: new Date().toISOString(),
        },
      ]);

      await manager.fillSlots();
      manager.dispose();

      expect(orchestratorMocks.mockOrchestrator.stop).toHaveBeenCalled();
    });
  });
});
