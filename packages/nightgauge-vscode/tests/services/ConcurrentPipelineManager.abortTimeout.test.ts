/**
 * Issue #3111 — abortAll() must bound waitForIdle() with a hard deadline.
 * Without it, a slot stuck mid-stop strands isShuttingDown=true forever, and
 * the IssueQueueService shutdownGuard silently refuses every subsequent
 * enqueue (looking like "drag-to-queue does nothing after disconnect").
 *
 * Issue #307 — and that deadline must BOOK the dead run's terminal state.
 * `this.slots.clear()` used to be the branch's only mutation: no queue-mark
 * release (so the issue stayed `processing` and became permanently
 * undispatchable — #254's outcome through a second door), no terminal outcome
 * (so bootstrap's autonomousComplete never freed the Go scheduler's
 * running-slot entry), no slot teardown. The fix books all three and then
 * TOMBSTONES the dispatch generation, so the wedged run settling later cannot
 * double-book or — once the operator has re-queued the issue — book against
 * the successor.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";

const { showWarningMessage, worktreeCleanupCalls, worktreeGate } = vi.hoisted(() => ({
  showWarningMessage: vi.fn().mockResolvedValue(undefined),
  worktreeCleanupCalls: [] as { issueNumber: number; deleteBranch: boolean }[],
  // When `blockCreate` is set, WorktreeManager.create awaits it — the shape of a
  // dispatch wedged inside worktree creation, which is how a re-dispatched issue
  // comes to sit in `reservedSlots` without a live slot.
  worktreeGate: { blockCreate: null as Promise<void> | null },
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
      create: vi.fn().mockImplementation(async (issueNumber: number, branchName: string) => {
        if (worktreeGate.blockCreate) await worktreeGate.blockCreate;
        return {
          path: `/test-repo/.worktrees/issue-${issueNumber}`,
          branch: branchName,
          issueNumber,
          exists: true,
        };
      }),
      cleanup: vi.fn().mockImplementation(async (issueNumber: number, deleteBranch?: boolean) => {
        worktreeCleanupCalls.push({ issueNumber, deleteBranch: deleteBranch === true });
      }),
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

/**
 * Real terminal-run shapes (#166) — see tests/fixtures/terminal/README.md. A
 * late settlement's duration/cost/token shape is the shape a real run carries;
 * inventing `{ totalDurationMs: 1000, cost: 0 }` lets these assertions keep
 * passing while the real shape drifts.
 */
const RUN_OUTCOMES = JSON.parse(
  readFileSync(path.join(__dirname, "../fixtures/terminal/run-outcomes.json"), "utf-8")
) as {
  complete: { cost_usd: number; duration_ms: number; stage_count: number };
  cancelled: { cost_usd: number; duration_ms: number; stage_count: number };
};

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
    // #254/#307: the terminal counterpart to dequeueIndependent. Main's version
    // of this fixture did not even define it — direct proof that nothing on the
    // deadline path called it.
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

interface SlotHandle {
  resolveRun: (result: any) => void;
  rejectRun: (error: Error) => void;
  getState: ReturnType<typeof vi.fn>;
  stateDispose: ReturnType<typeof vi.fn>;
}

/**
 * Factory whose runPipeline promise is held open until the test settles it, and
 * whose stop() is a no-op — the wedged slot the abort deadline exists for.
 */
function createControllableFactory() {
  const handles = new Map<number, SlotHandle>();
  const factory = vi.fn().mockImplementation((_workDir: string, issueNumber: number) => {
    let resolveRun!: (result: any) => void;
    let rejectRun!: (error: Error) => void;
    const runPromise = new Promise<any>((res, rej) => {
      resolveRun = res;
      rejectRun = rej;
    });
    const getState = vi.fn().mockResolvedValue({
      tokens: { estimated_cost_usd: RUN_OUTCOMES.cancelled.cost_usd, input: 0, output: 0 },
    });
    const stateDispose = vi.fn();
    handles.set(issueNumber, { resolveRun, rejectRun, getState, stateDispose });
    return {
      orchestrator: {
        setWorktreeOverride: vi.fn(),
        setRepoOverride: vi.fn(),
        setUnattended: vi.fn(),
        runPipeline: vi.fn().mockReturnValue(runPromise),
        stop: vi.fn(), // no-op — the stop that does not unblock runPipeline
        dispose: vi.fn(),
      },
      stateService: {
        onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
        onPhaseStart: vi.fn().mockReturnValue({ dispose: vi.fn() }),
        onPhaseComplete: vi.fn().mockReturnValue({ dispose: vi.fn() }),
        onUnifiedTokenUsage: vi.fn().mockReturnValue({ dispose: vi.fn() }),
        getState,
        initEmpty: vi.fn(),
        initializePipeline: vi.fn().mockResolvedValue(undefined),
        setMeta: vi.fn(),
        setDispatchToken: vi.fn(),
        dispose: stateDispose,
      },
    };
  });
  return { factory, handles };
}

function successResult() {
  return {
    success: true,
    totalDurationMs: RUN_OUTCOMES.complete.duration_ms,
    stagesRun: [],
    outcomeType: "completed",
  };
}

function failureResult() {
  return {
    success: false,
    failedStage: "feature-validate",
    totalDurationMs: RUN_OUTCOMES.cancelled.duration_ms,
    error: new Error("stage exited 1"),
  };
}

describe("ConcurrentPipelineManager.abortAll — deadline (#3111) and force-clear bookkeeping (#307)", () => {
  let mockQueue: ReturnType<typeof createMockQueueService>;
  let mockLogger: ReturnType<typeof createMockLogger>;
  let callbacks: {
    onSlotFailed: ReturnType<typeof vi.fn>;
    onSlotCompleted: ReturnType<typeof vi.fn>;
    onSlotDeferred: ReturnType<typeof vi.fn>;
    onSlotCleaned: ReturnType<typeof vi.fn>;
  };

  beforeEach(() => {
    vi.useFakeTimers();
    mockQueue = createMockQueueService();
    mockLogger = createMockLogger();
    callbacks = {
      onSlotFailed: vi.fn(),
      onSlotCompleted: vi.fn(),
      onSlotDeferred: vi.fn(),
      onSlotCleaned: vi.fn(),
    };
    showWarningMessage.mockClear();
    worktreeCleanupCalls.length = 0;
    worktreeGate.blockCreate = null;
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  function newManager(factory: any) {
    const manager = new ConcurrentPipelineManager(
      "/test-repo",
      mockQueue as any,
      factory,
      mockLogger as any,
      { maxConcurrent: 2, worktreeBase: ".worktrees" }
    );
    manager.setCallbacks(callbacks as any);
    return manager;
  }

  // ---------------------------------------------------------------- #3111 base

  it("force-clears slots and resets isShutdownInProgress when waitForIdle exceeds deadline", async () => {
    const { factory } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
    await manager.fillSlots();
    expect(manager.activeSlotCount).toBe(1);

    const abortPromise = manager.abortAll();
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
    const { factory } = createControllableFactory();
    const manager = newManager(factory);

    await manager.abortAll();

    expect(manager.isShutdownInProgress).toBe(false);
    expect(mockLogger.warn).not.toHaveBeenCalledWith(
      "abortAll exceeded deadline — force-clearing slots",
      expect.anything()
    );
    expect(showWarningMessage).not.toHaveBeenCalled();
  });

  // -------------------------------------------------- #307 terminal bookkeeping

  it("books the dead run's terminal state: queue mark released, outcome notified, slot torn down", async () => {
    const { factory } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
    await manager.fillSlots();

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    // 1. the dequeue's `processing` mark — without this the issue is
    //    permanently undispatchable (#254's outcome through a second door).
    expect(mockQueue.complete).toHaveBeenCalledWith("", 282);
    // 2. the terminal outcome — bootstrap turns this into autonomousComplete,
    //    which frees the Go scheduler's running-slot entry.
    expect(callbacks.onSlotFailed).toHaveBeenCalledTimes(1);
    expect(callbacks.onSlotFailed.mock.calls[0][1]).toBe(282);
    // Byte-identical to the SETTLED Stop All path: both Stop paths must book
    // the same thing (see terminal_behaviors.json).
    expect((callbacks.onSlotFailed.mock.calls[0][2] as Error).message).toBe("Cancelled by user");
    // 3. slot teardown — tree item + subscriptions released, worktree PRESERVED
    //    (a killed process may still hold it, and #66 keeps failed-run context).
    expect(callbacks.onSlotCleaned).toHaveBeenCalledWith(expect.any(Number), 282);
    expect(worktreeCleanupCalls).toEqual([]);
  });

  // ------------------------------------------------------------ race (a)

  it("(a) a force-cleared run that later SUCCEEDS books nothing a second time", async () => {
    const { factory, handles } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
    await manager.fillSlots();

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    mockQueue.complete.mockClear();
    callbacks.onSlotFailed.mockClear();
    callbacks.onSlotCleaned.mockClear();

    // The wedged adapter finally dies — with a MERGED PR, the worst case: on
    // main this fires onSlotCompleted and runs the whole finally.
    handles.get(282)!.resolveRun(successResult());
    await vi.advanceTimersByTimeAsync(1_000);
    await manager.settleForTest(282);

    expect(callbacks.onSlotCompleted).not.toHaveBeenCalled();
    expect(callbacks.onSlotFailed).not.toHaveBeenCalled();
    expect(callbacks.onSlotCleaned).not.toHaveBeenCalled();
    expect(mockQueue.complete).not.toHaveBeenCalled();
    expect(worktreeCleanupCalls).toEqual([]);
    expect(mockLogger.debug).toHaveBeenCalledWith(
      "Force-cleared run settled — outcome dropped (#307)",
      expect.objectContaining({ issueNumber: 282 })
    );
  });

  // ------------------------------------------------------------ race (b)

  it("(b) a force-cleared run that later FAILS or THROWS books nothing a second time", async () => {
    const { factory, handles } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282), makeQueueItem(283)]);
    await manager.fillSlots();
    expect(manager.activeSlotCount).toBe(2);

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    mockQueue.complete.mockClear();
    callbacks.onSlotFailed.mockClear();

    // #282 settles with a failure result; #283 rejects outright.
    handles.get(282)!.resolveRun(failureResult());
    handles.get(283)!.rejectRun(new Error("adapter died"));
    await vi.advanceTimersByTimeAsync(1_000);
    await manager.settleForTest(282).catch(() => undefined);
    await manager.settleForTest(283).catch(() => undefined);

    expect(callbacks.onSlotFailed).not.toHaveBeenCalled();
    expect(mockQueue.complete).not.toHaveBeenCalled();
    expect(worktreeCleanupCalls).toEqual([]);
    expect(mockLogger.debug).toHaveBeenCalledWith(
      "Force-cleared run threw — failure dropped (#307)",
      expect.objectContaining({ issueNumber: 283 })
    );
  });

  // ------------------------------------------------------------ race (c)

  it("(c) a late settlement never touches a successor that is still a reservation", async () => {
    const { factory, handles } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
    await manager.fillSlots();

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    // The operator re-queues #282. Its dispatch wedges inside worktree
    // creation, so the successor holds a RESERVATION and is absent from
    // `slots` — the window a supersede check that reads `slots` alone misses.
    worktreeGate.blockCreate = new Promise<void>(() => {});
    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
    void manager.fillSlots();
    await vi.advanceTimersByTimeAsync(10);
    expect(manager.activeSlotCount).toBe(0);
    expect(manager.availableSlotCount).toBe(1); // the reservation holds a seat

    mockQueue.complete.mockClear();
    callbacks.onSlotFailed.mockClear();

    // The dead run settles — successfully, which on main credits the
    // successor's issue, releases the successor's queue mark and deletes the
    // worktree the successor is being created in.
    handles.get(282)!.resolveRun(successResult());
    await vi.advanceTimersByTimeAsync(1_000);
    await manager.settleForTest(282);

    expect(callbacks.onSlotCompleted).not.toHaveBeenCalled();
    expect(callbacks.onSlotFailed).not.toHaveBeenCalled();
    // The successor's queue mark survives …
    expect(mockQueue.complete).not.toHaveBeenCalled();
    // … its worktree survives …
    expect(worktreeCleanupCalls).toEqual([]);
    // … and its reservation still holds the slot, so the #188 duplicate-dispatch
    // guard and the per-repo cap still see it in flight.
    expect(manager.availableSlotCount).toBe(1);
  });

  // ------------------------------------------------------------ race (c2)

  it("(c2) a run already past the tombstone check stands down rather than evicting a successor", async () => {
    // The narrow interleaving the tombstone alone cannot cover: the run settles
    // BEFORE the deadline and is inside its own finally — past the tombstone
    // check — when the force-clear lands. Its remaining steps must not run
    // against a successor that appears while it is still awaiting.
    const { factory, handles } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
    await manager.fillSlots();

    // Hold the finally's first step (completeQueueItem) open.
    let releaseQueueComplete!: () => void;
    mockQueue.complete.mockImplementationOnce(
      () =>
        new Promise<void>((res) => {
          releaseQueueComplete = () => res();
        })
    );

    handles.get(282)!.resolveRun(successResult());
    await vi.advanceTimersByTimeAsync(10);
    // The outcome was dispatched; the finally is now parked on the queue call.
    expect(callbacks.onSlotCompleted).toHaveBeenCalledTimes(1);

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    // The force-clear must NOT book a second terminal outcome for a run that
    // already booked its own — autonomousComplete is not idempotent.
    expect(callbacks.onSlotFailed).not.toHaveBeenCalled();

    // A successor is dispatched and wedges in worktree creation, WHILE the old
    // run is still parked inside its own finally.
    worktreeGate.blockCreate = new Promise<void>(() => {});
    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
    void manager.fillSlots();
    await vi.advanceTimersByTimeAsync(10);
    expect(manager.availableSlotCount).toBe(1); // successor holds a reservation

    worktreeCleanupCalls.length = 0;
    releaseQueueComplete();
    await vi.advanceTimersByTimeAsync(1_000);
    await manager.settleForTest(282);

    // The old run's finally resumed into cleanupSlot with a successor holding
    // the issue: its worktree teardown must not fire.
    expect(worktreeCleanupCalls).toEqual([]);
    expect(mockLogger.warn).toHaveBeenCalledWith(
      "cleanupSlot stood down — a newer dispatch owns this issue (#307)",
      expect.objectContaining({ issueNumber: 282 })
    );
  });

  // ------------------------------------------------------------ race (d)

  it("(d) abortAll called twice force-clears once and stays clean", async () => {
    const { factory } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
    await manager.fillSlots();

    const first = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await first;

    expect(callbacks.onSlotFailed).toHaveBeenCalledTimes(1);
    expect(mockQueue.complete).toHaveBeenCalledTimes(1);

    const second = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await second;

    // Nothing left to force-clear: no second outcome, no second queue release.
    expect(callbacks.onSlotFailed).toHaveBeenCalledTimes(1);
    expect(mockQueue.complete).toHaveBeenCalledTimes(1);
    expect(manager.isShutdownInProgress).toBe(false);
    expect(manager.activeSlotCount).toBe(0);
  });

  // ------------------------------------------------------------ race (e)

  it("(e) an ordinary run whose getState() rejects still fires onSlotFailed exactly once", async () => {
    // Differential probe for the regression round 1 introduced: a settle-once
    // latch claimed by THIS invocation before the un-guarded getState() await
    // made the catch block treat its own claim as "a force-clear booked it" and
    // skip onSlotFailed entirely — #307's own symptom, on a path with no
    // relationship to abortAll. No abort happens in this test at all.
    const { factory, handles } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(291)]);
    await manager.fillSlots();

    handles.get(291)!.getState.mockRejectedValue(
      Object.assign(new Error("ENOENT: no such file or directory, open 'state.json'"), {
        code: "ENOENT",
      })
    );
    handles.get(291)!.resolveRun(failureResult());
    await vi.advanceTimersByTimeAsync(1_000);
    await manager.settleForTest(291).catch(() => undefined);

    expect(callbacks.onSlotFailed).toHaveBeenCalledTimes(1);
    expect(callbacks.onSlotFailed.mock.calls[0][1]).toBe(291);
    // And the ordinary terminal funnel still ran.
    expect(mockQueue.complete).toHaveBeenCalledWith("", 291);
  });

  // ------------------------------------------------- stranded reservations

  it("releases the queue mark of a reservation whose dispatch never became a slot", async () => {
    const { factory } = createControllableFactory();
    const manager = newManager(factory);

    // The deadline can fire with isFilling=true and ZERO slots: the item was
    // dequeued (and marked `processing`) but its startSlot wedged inside
    // worktree creation.
    worktreeGate.blockCreate = new Promise<void>(() => {});
    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
    void manager.fillSlots();
    await vi.advanceTimersByTimeAsync(10);
    expect(manager.activeSlotCount).toBe(0);

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    expect(mockQueue.complete).toHaveBeenCalledWith("", 282);
    expect(manager.isShutdownInProgress).toBe(false);
    // The reservation is deliberately NOT cleared: it is what stops a
    // re-dispatch from colliding with the still-running startSlot, and that
    // dispatch removes its own entry when it unwinds.
    expect(manager.availableSlotCount).toBe(1);
  });

  it("a force-cleared reservation whose dispatch unwinds is not re-enqueued", async () => {
    const { factory } = createControllableFactory();
    const manager = newManager(factory);

    let releaseCreate!: () => void;
    worktreeGate.blockCreate = new Promise<void>((res) => {
      releaseCreate = () => res();
    });
    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
    const filling = manager.fillSlots();
    await vi.advanceTimersByTimeAsync(10);

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    mockQueue.complete.mockClear();
    releaseCreate();
    await vi.advanceTimersByTimeAsync(10);
    await filling;

    // Re-enqueueing would silently undo the operator's stop, and clearing the
    // queue mark again could strip a successor's.
    expect(mockQueue.enqueue).not.toHaveBeenCalled();
    expect(mockQueue.complete).not.toHaveBeenCalled();
    expect(manager.activeSlotCount).toBe(0);
    // The reservation is released by its own dispatch, so the seat comes back.
    expect(manager.availableSlotCount).toBe(2);
  });
});
