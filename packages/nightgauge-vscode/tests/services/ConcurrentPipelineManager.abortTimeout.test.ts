/**
 * Issue #3111 — abortAll() must bound waitForIdle() with a hard deadline.
 * Without it, a slot stuck mid-stop strands isShuttingDown=true forever, and
 * the IssueQueueService shutdownGuard silently refuses every subsequent
 * enqueue (looking like "drag-to-queue does nothing after disconnect").
 *
 * Issue #307 — and that deadline must BOOK the dead dispatch's terminal state.
 * `this.slots.clear()` used to be the branch's only mutation: no queue-mark
 * release (so the issue stayed `processing` and became permanently
 * undispatchable — #254's outcome through a second door), no terminal outcome
 * (so bootstrap's autonomousComplete never freed the Go scheduler's
 * running-slot entry), no slot teardown. The fix books all three — for slots
 * AND for stranded reservations, which hold a Go scheduler seat from dispatch
 * time even though they never became a slot — and then TOMBSTONES the dispatch
 * run id, so the wedged run settling later cannot double-book or, once the
 * operator has re-queued the issue, book against the successor.
 *
 * The exactly-once invariant is the point of races (a)–(f) below: a dispatch
 * fires exactly one terminal outcome callback, because bootstrap turns each
 * into `autonomousComplete`, which is not idempotent (cascade breaker,
 * per-issue lifetime cap, Go running-seat release).
 */

import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from "vitest";
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

vi.mock("../../src/utils/nightgaugeConfig", () => ({
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

/**
 * Attach a terminal no-op rejection handler to a fixture promise and hand the
 * ORIGINAL promise back (#618).
 *
 * Several tests here build a promise, park production code on it, and reject it
 * from the test body to drive an error path. Whether production code is parked
 * on that promise at the instant the test rejects it is not something the test
 * controls: the dispatch reaches `WorktreeManager.create()` only after
 * `startSlotInner` awaits a REAL `fs.access()` for the conflict-restart signal,
 * and that ENOENT comes back from the libuv threadpool on wall-clock time that
 * the fake-timer advances do not govern. On an unloaded machine it lands first
 * and the `await` inside the mock observes the rejection; on a loaded CI runner
 * it does not, the rejection reaches the process with no handler attached, and
 * Vitest reports `Errors 1 error` while every test passes — a red job with a
 * green test summary, on PRs that never touched this code.
 *
 * The `.catch()` derives a NEW promise and discards it; the original keeps
 * whatever handlers production code attaches, so every assertion still runs
 * against the real rejection. It only guarantees the rejection is observed
 * whether or not the awaiter has arrived yet.
 */
function settleObserved<T>(promise: Promise<T>): Promise<T> {
  void promise.catch(() => undefined);
  return promise;
}

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
  getState: Mock;
  stateDispose: Mock;
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
    // Same hazard as the worktree gate (#618): `rejectRun` is called from the
    // test body, and the manager only parks on this promise once
    // `runSlotPipeline` has worked its way down to `orchestrator.runPipeline()`.
    const runPromise = settleObserved(
      new Promise<any>((res, rej) => {
        resolveRun = res;
        rejectRun = rej;
      })
    );
    const getState = vi.fn().mockResolvedValue({
      tokens: { estimated_cost_usd: RUN_OUTCOMES.cancelled.cost_usd, input: 0, output: 0 },
    });
    const stateDispose = vi.fn();
    handles.set(issueNumber, { resolveRun, rejectRun, getState, stateDispose });
    return {
      orchestrator: {
        setWorktreeOverride: vi.fn(),
        setRepoOverride: vi.fn(),
        setRunRepoRoot: vi.fn(),
        setUnattended: vi.fn(),
        // ADR-017 step 3 (#370): the slot resolves its repo through the
        // orchestrator when the queue item and the workspace manifest cannot
        // name one, BEFORE beginRun installs the identity.
        resolveRunRepoSlug: vi.fn().mockResolvedValue("nightgauge/nightgauge"),
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
        // ADR-017 step 3 (#370): the manager installs the dispatch's run
        // identity on the slot's own state service before anything emits.
        beginRun: vi.fn(),
        endRun: vi.fn(),
        getRunId: vi.fn().mockReturnValue(null),
        initEmpty: vi.fn(),
        initializePipeline: vi.fn().mockResolvedValue(undefined),
        setMeta: vi.fn(),
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
    onSlotFailed: Mock;
    onSlotCompleted: Mock;
    onSlotDeferred: Mock;
    onSlotCleaned: Mock;
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
    expect(showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("force-cleared 1 stuck dispatch(es)")
    );
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

  // ------------------------------------------------------------ race (c3)

  it("(c3) a run already past the tombstone check tears its slot down exactly once", async () => {
    // Same interleaving as (c2) but with NO successor, so `stillOwnsIssue`
    // answers "nobody holds the issue → true" for both callers. Without the
    // at-most-once latch the finally's second cleanupSlot fires onSlotCleaned
    // again and — because the run SUCCEEDED, so preserveWorktree is false —
    // deletes the tree the force-clear deliberately preserved for a process
    // that may still hold it.
    const { factory, handles } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
    await manager.fillSlots();

    let releaseQueueComplete!: () => void;
    mockQueue.complete.mockImplementationOnce(
      () =>
        new Promise<void>((res) => {
          releaseQueueComplete = () => res();
        })
    );

    handles.get(282)!.resolveRun(successResult());
    await vi.advanceTimersByTimeAsync(10);
    expect(callbacks.onSlotCompleted).toHaveBeenCalledTimes(1);

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    // The force-clear tore the slot down, preserving the worktree.
    expect(callbacks.onSlotCleaned).toHaveBeenCalledTimes(1);
    expect(worktreeCleanupCalls).toEqual([]);

    releaseQueueComplete();
    await vi.advanceTimersByTimeAsync(1_000);
    await manager.settleForTest(282);

    expect(callbacks.onSlotCleaned).toHaveBeenCalledTimes(1);
    expect(worktreeCleanupCalls).toEqual([]);
    expect(mockLogger.debug).toHaveBeenCalledWith(
      "cleanupSlot skipped — this slot was already torn down (#307)",
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

  // ------------------------------------------------------------ race (f)

  it("(f) the deadline landing INSIDE a settled run's terminal boundary does not double-book", async () => {
    // The TOCTOU the round-3 shape lost: the run passes the tombstone check at
    // terminal boundary 1 and then AWAITS getState() before dispatching its
    // outcome. If the claim is taken after that await, the deadline sees an
    // unclaimed slot, books its own onSlotFailed, and the run books a SECOND
    // outcome on resume — two autonomousComplete calls for one run (round 3
    // measured `final: failed=1 completed=1`). The claim is now taken
    // synchronously with the check, so the force-clear reads it and stands down.
    const { factory, handles } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
    await manager.fillSlots();

    // Park the run inside terminal boundary 1's getState() await.
    let releaseState!: () => void;
    handles
      .get(282)!
      .getState.mockReturnValueOnce(
        new Promise((res) => (releaseState = () => res({ tokens: { estimated_cost_usd: 1.5 } })))
      );
    handles.get(282)!.resolveRun(successResult());
    await vi.advanceTimersByTimeAsync(10);

    // Nothing dispatched yet — the run holds the claim and is mid-await.
    expect(callbacks.onSlotCompleted).not.toHaveBeenCalled();
    expect(callbacks.onSlotFailed).not.toHaveBeenCalled();

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    // The force-clear read the claim and did NOT book a second outcome.
    expect(callbacks.onSlotFailed).not.toHaveBeenCalled();

    releaseState();
    await vi.advanceTimersByTimeAsync(1_000);
    await manager.settleForTest(282);

    // Exactly one terminal outcome for this dispatch, total.
    expect(callbacks.onSlotCompleted).toHaveBeenCalledTimes(1);
    expect(callbacks.onSlotFailed).not.toHaveBeenCalled();
  });

  it("(f2) the same interleaving on the CATCH path books onSlotFailed exactly once", async () => {
    // Round 3's PROBE-Y: reject the run, park in the catch's getState() await,
    // fire the deadline. Observed then: `final onSlotFailed=2`.
    const { factory, handles } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(283)]);
    await manager.fillSlots();

    let releaseState!: () => void;
    handles
      .get(283)!
      .getState.mockReturnValueOnce(
        new Promise((res) => (releaseState = () => res({ tokens: { estimated_cost_usd: 0.5 } })))
      );
    handles.get(283)!.rejectRun(new Error("adapter died"));
    await vi.advanceTimersByTimeAsync(10);
    expect(callbacks.onSlotFailed).not.toHaveBeenCalled();

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    expect(callbacks.onSlotFailed).not.toHaveBeenCalled();

    releaseState();
    await vi.advanceTimersByTimeAsync(1_000);
    await manager.settleForTest(283).catch(() => undefined);

    expect(callbacks.onSlotFailed).toHaveBeenCalledTimes(1);
    expect(callbacks.onSlotFailed.mock.calls[0][1]).toBe(283);
    // The run's REAL error, not the force-clear's "Cancelled by user": the
    // claim was the run's, so the run books what actually happened.
    expect((callbacks.onSlotFailed.mock.calls[0][2] as Error).message).toBe("adapter died");
  });

  it("books the force-cleared slot's real spend so the health snapshot is recorded", async () => {
    // bootstrap gates dashboard.recordHealthSnapshotForRun behind costUsd > 0
    // and paints the Output Window badge with it, so a hard-coded 0 silently
    // drops the reliability snapshot for every force-cleared run.
    const { factory } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
    await manager.fillSlots();

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    expect(callbacks.onSlotFailed).toHaveBeenCalledTimes(1);
    expect(callbacks.onSlotFailed.mock.calls[0][3]).toBe(RUN_OUTCOMES.cancelled.cost_usd);
  });

  // ------------------------------------------------- stranded reservations

  it("books a stranded reservation's terminal state: queue mark AND the Go scheduler seat", async () => {
    const { factory } = createControllableFactory();
    const manager = newManager(factory);

    // The deadline can fire with isFilling=true and ZERO slots: the item was
    // dequeued (and marked `processing`) but its startSlot wedged inside
    // worktree creation.
    worktreeGate.blockCreate = new Promise<void>(() => {});
    mockQueue.dequeueIndependent.mockResolvedValueOnce([
      { ...makeQueueItem(282), repoName: "nightgauge/acmeapp" },
    ]);
    void manager.fillSlots();
    await vi.advanceTimersByTimeAsync(10);
    expect(manager.activeSlotCount).toBe(0);

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    // 1. the dequeue's `processing` mark.
    expect(mockQueue.complete).toHaveBeenCalledWith("nightgauge/acmeapp", 282);
    // 2. THE GO SCHEDULER SEAT. AutonomousScheduler.enqueueItem appended this
    //    item to state.Running at DISPATCH time; the only in-session release is
    //    OnPipelineComplete, reachable solely through IpcClient.autonomousComplete,
    //    which bootstrap calls from onSlotCompleted / onSlotFailed /
    //    onSlotDeferred. Releasing only the queue mark leaves one MaxConcurrent
    //    seat gone for the life of the scheduler and makes the issue permanently
    //    ineligible via isRunning() — the leaked-seat defect #307 is named after.
    expect(callbacks.onSlotFailed).toHaveBeenCalledTimes(1);
    expect(callbacks.onSlotFailed.mock.calls[0][1]).toBe(282);
    // The repo slug is what bootstrap splits into owner/repo for
    // autonomousComplete; without it the seat is not released at all.
    expect(callbacks.onSlotFailed.mock.calls[0][4]).toBe("nightgauge/acmeapp");
    expect((callbacks.onSlotFailed.mock.calls[0][2] as Error).message).toBe("Cancelled by user");

    expect(manager.isShutdownInProgress).toBe(false);
    // The reservation is deliberately NOT cleared: it is what stops a
    // re-dispatch from colliding with the still-running startSlot, and that
    // dispatch removes its own entry when it unwinds.
    expect(manager.availableSlotCount).toBe(1);
  });

  it("a force-cleared reservation whose dispatch unwinds is not re-enqueued and books no second outcome", async () => {
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

    expect(callbacks.onSlotFailed).toHaveBeenCalledTimes(1);
    mockQueue.complete.mockClear();
    releaseCreate();
    await vi.advanceTimersByTimeAsync(10);
    await filling;

    // Re-enqueueing would silently undo the operator's stop, and clearing the
    // queue mark again could strip a successor's.
    expect(mockQueue.enqueue).not.toHaveBeenCalled();
    expect(mockQueue.complete).not.toHaveBeenCalled();
    // And the seat is released exactly once — the dispatch's own unwind must
    // not fire a second autonomousComplete for the same run id.
    expect(callbacks.onSlotFailed).toHaveBeenCalledTimes(1);
    expect(manager.activeSlotCount).toBe(0);
    // The reservation is released by its own dispatch, so the seat comes back.
    expect(manager.availableSlotCount).toBe(2);
  });

  it("a reservation whose worktree creation FAILS after the force-clear books no second outcome", async () => {
    // startSlotInner's own worktree-failure exit also fires onSlotFailed. Once
    // the deadline has booked the reservation, that exit must stand down or the
    // Go scheduler's lifetime cap is charged twice for one dispatch.
    const { factory } = createControllableFactory();
    const manager = newManager(factory);

    let failCreate!: () => void;
    worktreeGate.blockCreate = settleObserved(
      new Promise<void>((_res, rej) => {
        failCreate = () => rej(new Error("fatal: could not lock .git/config"));
      })
    );
    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
    const filling = manager.fillSlots();
    await vi.advanceTimersByTimeAsync(10);

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    expect(callbacks.onSlotFailed).toHaveBeenCalledTimes(1);

    failCreate();
    // Worktree creation retries with backoff before giving up.
    await vi.advanceTimersByTimeAsync(120_000);
    await filling;

    // The worktree-failure exit really ran. Without this the surviving count
    // below would also be satisfied by a dispatch that never reached
    // `WorktreeManager.create()` at all — the exact state the #618 fix makes
    // survivable, and therefore the state that must not be allowed to make
    // this assertion vacuous.
    expect(mockLogger.error).toHaveBeenCalledWith(
      "Failed to create worktree for concurrent pipeline",
      expect.objectContaining({ issueNumber: 282 })
    );
    expect(callbacks.onSlotFailed).toHaveBeenCalledTimes(1);
  });
});
