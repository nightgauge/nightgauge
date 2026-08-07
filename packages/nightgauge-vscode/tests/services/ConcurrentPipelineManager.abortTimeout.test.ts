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

const { worktreeCreate } = vi.hoisted(() => ({
  // Swappable so a test can wedge `git worktree add` — the state in which
  // abortAll's deadline fires with isFilling=true and zero slots (#307).
  worktreeCreate: {
    impl: (issueNumber: number, branchName: string): Promise<unknown> =>
      Promise.resolve({
        path: `/test-repo/.worktrees/issue-${issueNumber}`,
        branch: branchName,
        issueNumber,
        exists: true,
      }),
  },
}));

vi.mock("../../src/utils/WorktreeManager", () => ({
  WorktreeManager: vi.fn(function () {
    return {
      create: vi
        .fn()
        .mockImplementation((issueNumber: number, branchName: string) =>
          worktreeCreate.impl(issueNumber, branchName)
        ),
      cleanup: vi.fn().mockResolvedValue(undefined),
      cleanupOrphans: vi.fn().mockResolvedValue(0),
      cleanupAll: vi.fn().mockResolvedValue(undefined),
      listActive: vi.fn().mockResolvedValue([]),
      getRepoRoot: vi.fn().mockReturnValue("/test-repo"),
      getWorktreePath: vi.fn((n: number) => `/test-repo/.worktrees/issue-${n}`),
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
    // #307: the dequeue marks each item `processing`; `complete` is the only
    // thing that clears it. Absent from this mock before #307 because nothing
    // on the deadline path ever called it — which was the defect.
    complete: vi.fn().mockResolvedValue(undefined),
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

/**
 * Same as {@link createStuckFactory}, but the run promise is resolvable from
 * the test — so a slot can be wedged past abortAll's deadline and then allowed
 * to settle, exercising the settles-after-force-clear race (#307).
 */
function createDeferredFactory(opts: { gateGetState?: boolean } = {}) {
  let resolveRun: ((result: unknown) => void) | undefined;
  let openGetStateGate: (() => void) | undefined;
  // When gated, `getState()` parks the settled run inside runSlotPipeline's
  // outcome dispatch — the exact window in which the abort deadline can fire
  // against a slot that is still in `this.slots` and has ALREADY claimed the
  // terminal-bookkeeping latch.
  const getStateGate = opts.gateGetState
    ? new Promise<void>((resolve) => {
        openGetStateGate = resolve;
      })
    : undefined;
  const factory = vi.fn().mockImplementation((_workDir: string, _issueNumber: number) => {
    const orchestrator = {
      setWorktreeOverride: vi.fn(),
      setUnattended: vi.fn(),
      runPipeline: vi.fn().mockReturnValue(
        new Promise((resolve) => {
          resolveRun = resolve;
        })
      ),
      stop: vi.fn(),
      dispose: vi.fn(),
    };
    const stateService = {
      onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onPhaseStart: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onPhaseComplete: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onUnifiedTokenUsage: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      getState: vi.fn().mockImplementation(async () => {
        if (getStateGate) await getStateGate;
        return null;
      }),
      initEmpty: vi.fn(),
      initializePipeline: vi.fn().mockResolvedValue(undefined),
      setMeta: vi.fn(),
      dispose: vi.fn(),
    };
    return { orchestrator, stateService };
  });
  return {
    factory,
    settleRun: (result: unknown) => {
      if (!resolveRun) throw new Error("run promise was never created");
      resolveRun(result);
    },
    openGetState: () => {
      if (!openGetStateGate) throw new Error("factory was not created with gateGetState");
      openGetStateGate();
    },
  };
}

describe("ConcurrentPipelineManager.abortAll — timeout (Issue #3111)", () => {
  let mockQueue: ReturnType<typeof createMockQueueService>;
  let mockLogger: ReturnType<typeof createMockLogger>;

  beforeEach(() => {
    vi.useFakeTimers();
    mockQueue = createMockQueueService();
    mockLogger = createMockLogger();
    showWarningMessage.mockClear();
    worktreeCreate.impl = (issueNumber: number, branchName: string) =>
      Promise.resolve({
        path: `/test-repo/.worktrees/issue-${issueNumber}`,
        branch: branchName,
        issueNumber,
        exists: true,
      });
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
    // #307 negative control: nothing was force-cleared, so no force-settle
    // bookkeeping may fire either.
    expect(mockQueue.complete).not.toHaveBeenCalled();
  });

  /**
   * Issue #307 — a force-cleared slot's terminal bookkeeping must run EXACTLY
   * once.
   *
   * `runSlotPipeline`'s finally block is the only place a slot's terminal
   * bookkeeping normally happens, and it is unreachable when the run promise
   * never settles. Before #307 the deadline branch did nothing but
   * `slots.clear()`: the queue item stayed `processing` forever (the #254
   * outcome through a second door) and the Go scheduler's running-slot entry
   * for the issue was never freed.
   */
  describe("terminal bookkeeping on the force-clear path (Issue #307)", () => {
    it("books a force-cleared slot's terminal state exactly once", async () => {
      const factory = createStuckFactory();
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );
      const onSlotFailed = vi.fn();
      const onSlotCleaned = vi.fn();
      manager.setCallbacks({ onSlotFailed, onSlotCleaned });

      mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
      await manager.fillSlots();
      expect(manager.activeSlotCount).toBe(1);

      const abortPromise = manager.abortAll();
      await vi.advanceTimersByTimeAsync(31_000);
      await abortPromise;

      // The dequeue's `processing` mark is released (#254 invariant).
      expect(mockQueue.complete).toHaveBeenCalledTimes(1);
      expect(mockQueue.complete).toHaveBeenCalledWith("", 282);

      // The terminal outcome callback fired — this is what bootstrap wires to
      // IpcClient.autonomousComplete, which frees the Go scheduler's slot.
      expect(onSlotFailed).toHaveBeenCalledTimes(1);
      expect(onSlotFailed.mock.calls[0][1]).toBe(282);
      expect((onSlotFailed.mock.calls[0][2] as Error).message).toMatch(/abort deadline/);

      // The slot was disposed through the normal cleanup path, not just
      // dropped from the map.
      expect(onSlotCleaned).toHaveBeenCalledTimes(1);
      expect(onSlotCleaned).toHaveBeenCalledWith(0, 282);

      expect(manager.activeSlotCount).toBe(0);
      expect(manager.isShutdownInProgress).toBe(false);
    });

    it("does not double-settle when the run promise settles after the force-clear", async () => {
      const { factory, settleRun } = createDeferredFactory();
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );
      const onSlotFailed = vi.fn();
      const onSlotCleaned = vi.fn();
      manager.setCallbacks({ onSlotFailed, onSlotCleaned });

      mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
      await manager.fillSlots();
      expect(manager.activeSlotCount).toBe(1);

      const abortPromise = manager.abortAll();
      await vi.advanceTimersByTimeAsync(31_000);
      await abortPromise;

      // Pre-condition for this test to mean anything: the force-clear really
      // did happen, and it really did book the slot.
      expect(onSlotFailed).toHaveBeenCalledTimes(1);
      expect(onSlotCleaned).toHaveBeenCalledTimes(1);

      // Now the wedged adapter finally dies and runPipeline resolves — AFTER
      // abortAll returned. runSlotPipeline's outcome dispatch and cleanup must
      // both no-op: `onSlotFailed` routes to autonomousComplete →
      // AutonomousScheduler.NotifyComplete → onPipelineComplete, which charges
      // the per-issue lifetime failure cap and feeds the cascade breaker. It is
      // NOT idempotent, so a second fire double-charges one run.
      settleRun({ success: false, failedStage: "feature-dev", totalDurationMs: 1_000 });
      await manager.settleForTest(282);

      expect(onSlotFailed).toHaveBeenCalledTimes(1);
      expect(onSlotCleaned).toHaveBeenCalledTimes(1);
      expect(manager.activeSlotCount).toBe(0);
      // `completeQueueItem` DOES run again from the (content-pinned) finally
      // block. That one is genuinely idempotent — Go's completeQueueItemLocked
      // no-ops when the item is not `processing` — so it is left alone rather
      // than gated inside the parity fence.
      expect(mockQueue.complete).toHaveBeenCalledTimes(2);
      expect(mockQueue.complete).toHaveBeenNthCalledWith(2, "", 282);
    });

    it("does not double-settle when the run settles just BEFORE the deadline fires", async () => {
      // Mirror of the previous case. Here the normal settlement path wins the
      // race and claims the latch, but is still mid-dispatch (parked on
      // getState) when the deadline fires against a slot that is still in
      // `this.slots`. The force-settle must recognize the claim and stand down
      // — "already-booked", not a second booking — and the normal path must
      // still complete its own cleanup afterwards.
      const { factory, settleRun, openGetState } = createDeferredFactory({ gateGetState: true });
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );
      const onSlotFailed = vi.fn();
      const onSlotCleaned = vi.fn();
      manager.setCallbacks({ onSlotFailed, onSlotCleaned });

      mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
      await manager.fillSlots();
      expect(manager.activeSlotCount).toBe(1);

      const abortPromise = manager.abortAll();
      // Run settles first — runSlotPipeline claims the latch, then parks.
      settleRun({ success: false, failedStage: "feature-dev", totalDurationMs: 1_000 });
      await vi.advanceTimersByTimeAsync(1);
      expect(manager.activeSlotCount).toBe(1); // still mid-dispatch

      // Deadline fires against the still-present, already-claimed slot. The
      // extra 6s covers the per-slot bookkeeping budget: if the force-settle
      // wrongly proceeded it would park on the same gate and only unwind when
      // that budget expires, so this keeps the failure an assertion rather
      // than a test-level hang.
      await vi.advanceTimersByTimeAsync(31_000);
      await vi.advanceTimersByTimeAsync(6_000);
      await abortPromise;

      expect(mockLogger.warn).toHaveBeenCalledWith(
        "abortAll force-clear complete",
        expect.objectContaining({ booked: 0, alreadyBooked: 1, bookkeepingFailed: 0 })
      );

      // Let the normal path finish.
      openGetState();
      await manager.settleForTest(282);

      // Exactly one terminal outcome, carrying the settled-cancel
      // classification the normal path owns — not the force-settle wording.
      expect(onSlotFailed).toHaveBeenCalledTimes(1);
      expect((onSlotFailed.mock.calls[0][2] as Error).message).toBe("Cancelled by user");
      // Standing down must not cost the slot its teardown.
      expect(onSlotCleaned).toHaveBeenCalledTimes(1);
      expect(manager.activeSlotCount).toBe(0);
      expect(manager.isShutdownInProgress).toBe(false);
    });

    it("stays bounded when the bookkeeping itself wedges, and says so", async () => {
      // The bookkeeping makes IPC calls against a manager that is already
      // wedged. If it could block, abortAll would sit past its own deadline
      // with isShuttingDown=true — the #3111 condition, reintroduced through
      // the fix for #307. It must give up, report `bookkeeping-failed`, and
      // still force-clear.
      const factory = createStuckFactory();
      mockQueue.complete.mockImplementation(() => new Promise(() => {}));
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );
      const onSlotFailed = vi.fn();
      manager.setCallbacks({ onSlotFailed });

      mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
      await manager.fillSlots();

      const abortPromise = manager.abortAll();
      // 30s deadline, then the 5s per-slot bookkeeping budget on top.
      await vi.advanceTimersByTimeAsync(31_000);
      await vi.advanceTimersByTimeAsync(6_000);
      await abortPromise;

      expect(mockLogger.warn).toHaveBeenCalledWith(
        "abortAll force-clear complete",
        expect.objectContaining({ booked: 0, alreadyBooked: 0, bookkeepingFailed: 1 })
      );
      expect(mockLogger.error).toHaveBeenCalledWith(
        expect.stringContaining("TIMED OUT"),
        expect.objectContaining({ issueNumber: 282, completedQueueItem: false })
      );
      // Partial progress is honestly reported: the outcome callback never fired.
      expect(onSlotFailed).not.toHaveBeenCalled();
      // The abort still finishes and still frees the pipeline.
      expect(manager.isShutdownInProgress).toBe(false);
      expect(manager.activeSlotCount).toBe(0);
      expect(showWarningMessage).toHaveBeenCalledWith(
        expect.stringContaining("could not be fully cleaned")
      );
    });

    it("releases the processing mark for items still mid-dispatch at the deadline", async () => {
      // Wedge `git worktree add` so startSlot never returns: the item has been
      // dequeued (and marked `processing`) and holds a reservation, but never
      // becomes a slot. abortAll's deadline fires with isFilling=true and zero
      // slots — before #307 nothing released that mark either.
      worktreeCreate.impl = () => new Promise(() => {});
      const factory = createStuckFactory();
      const manager = new ConcurrentPipelineManager(
        "/test-repo",
        mockQueue as any,
        factory,
        mockLogger as any,
        { maxConcurrent: 2, worktreeBase: ".worktrees" }
      );

      mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
      // Deliberately not awaited — fillSlots is wedged inside startSlot.
      void manager.fillSlots();
      await Promise.resolve();
      expect(manager.activeSlotCount).toBe(0);

      const abortPromise = manager.abortAll();
      await vi.advanceTimersByTimeAsync(31_000);
      await abortPromise;

      expect(mockQueue.complete).toHaveBeenCalledTimes(1);
      expect(mockQueue.complete).toHaveBeenCalledWith("", 282);
      expect(manager.isShutdownInProgress).toBe(false);
      // Capacity is reclaimed — the whole point of the branch is to declare
      // the pipeline ready for new work.
      expect(manager.availableSlotCount).toBe(2);
    });
  });
});
