/**
 * ConcurrentPipelineManager.abandonedDispatchCard.test.ts
 *
 * Issue #305 — the force-clear funnel could not TELL anyone.
 *
 * #307 gave a wedged dispatch exactly-once terminal bookkeeping: the queue mark
 * is released, the Go scheduler seat is freed, the slot is torn down and the
 * worktree preserved. What it could not do is produce anything durable an
 * operator would find later — its only surfacing was a transient Stop toast and
 * a warn log, and its own ledger in terminal_behaviors.json named the reason:
 * "no `raise` verb exists on IPC or the CLI (#305)".
 *
 * Both arms of the funnel now raise the `abandoned-dispatch` card. The harness
 * (mocks + controllable factory) is the one from
 * ConcurrentPipelineManager.abortTimeout.test.ts, which already drives the
 * deadline to force-clear both a slot and a stranded reservation.
 *
 * @see internal/orchestrator/attention_wiring.go — BuildAbandonedDispatch
 * @see internal/orchestrator/testdata/terminal_behaviors.json — the parity row
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
  /**
   * When set, `stateService.getState()` never settles. That is the ONE await
   * inside `runSlotPipeline` that sits AFTER terminal boundary 1 takes the
   * claim (`slot.terminalOutcomeDispatched = true`) and BEFORE the outcome
   * callback fires — i.e. the exact shape of a run that reported its own
   * outcome and then wedged in teardown.
   */
  wedgeAfterClaim: () => void;
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
    const settledState = {
      tokens: { estimated_cost_usd: RUN_OUTCOMES.cancelled.cost_usd, input: 0, output: 0 },
    };
    let stateWedged = false;
    const getState = vi
      .fn()
      .mockImplementation(() =>
        stateWedged ? new Promise<never>(() => {}) : Promise.resolve(settledState)
      );
    const stateDispose = vi.fn();
    handles.set(issueNumber, {
      resolveRun,
      rejectRun,
      getState,
      stateDispose,
      wedgeAfterClaim: () => {
        stateWedged = true;
      },
    });
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

// The attention.raise seam (#305). Captured so the tests can assert the exact
// producer + scalars that crossed the wire.
const { attentionRaise } = vi.hoisted(() => ({
  attentionRaise: vi.fn().mockResolvedValue({ outcome: "created", id: "dr_test" }),
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: { getInstance: () => ({ attentionRaise }) },
}));

/**
 * Positional-arg decoder for the generated `attentionRaise` signature:
 * (producer, repo, issue, runId, pr, prState, mergeable, mergeStateStatus,
 * reviewDecision, checks, stage, situation). There is deliberately no cost or
 * ceiling in that list — both are derived daemon-side (#305 review), so no
 * caller can choose the magnitude of an option the daemon will execute.
 * `situation` is the one thing the daemon CANNOT derive: which force-clear arm
 * this is and whether the dispatch's terminal outcome was booked. It selects
 * prose, never an option.
 */
function decodeRaise(call: unknown[]) {
  const [producer, repo, issue, runId, , , , , , , stage, situation] = call as [
    string,
    string,
    number,
    string | undefined,
    unknown,
    unknown,
    unknown,
    unknown,
    unknown,
    unknown,
    string | undefined,
    string | undefined,
  ];
  return { producer, repo, issue, runId, stage, situation };
}

describe("ConcurrentPipelineManager force-clear raises the abandoned-dispatch card (#305)", () => {
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
    attentionRaise.mockReset();
    attentionRaise.mockResolvedValue({ outcome: "created", id: "dr_test" });
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

  /** A queue item carrying a cross-repo identity — the card needs owner/name. */
  function repoQueueItem(issueNumber: number) {
    return { ...makeQueueItem(issueNumber), repoName: "octocat/acme" };
  }

  it("raises the card for a force-cleared SLOT, carrying repo, issue and stage", async () => {
    const { factory } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([repoQueueItem(282)]);
    await manager.fillSlots();

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    const raises = attentionRaise.mock.calls.map(decodeRaise);
    expect(raises).toHaveLength(1);
    expect(raises[0].producer).toBe("abandoned-dispatch");
    expect(raises[0].repo).toBe("octocat/acme");
    expect(raises[0].issue).toBe(282);
    // NO run id, deliberately: Go mints the RunID and the extension has no verb
    // to ask for one, and the wedged run's run-state.json may already be
    // archived. Synthesising one would put a fabricated identity into the audit
    // trail. #370 is what improves this later.
    expect(raises[0].runId).toBeUndefined();
    // The SLOT arm with the force-clear booking the outcome: the preserved
    // worktree is the true residue, and it is the only situation whose body may
    // claim one.
    expect(raises[0].situation).toBe("slot-worktree-preserved");

    // The #307 bookkeeping is untouched by the addition.
    expect(mockQueue.complete).toHaveBeenCalledWith("octocat/acme", 282);
    expect(callbacks.onSlotFailed).toHaveBeenCalledTimes(1);
    expect(worktreeCleanupCalls).toEqual([]);
  });

  it("raises the card for a force-cleared RESERVATION too — both arms or neither", async () => {
    // A dispatch wedged inside worktree creation never becomes a slot, but it
    // holds a Go scheduler seat from dispatch time and is abandoned exactly as
    // much. Giving only one arm the notification is the asymmetry the funnel
    // fence exists to prevent.
    const { factory } = createControllableFactory();
    const manager = newManager(factory);

    worktreeGate.blockCreate = new Promise<void>(() => {});
    mockQueue.dequeueIndependent.mockResolvedValueOnce([repoQueueItem(282)]);
    void manager.fillSlots();
    await vi.advanceTimersByTimeAsync(10);
    expect(manager.activeSlotCount).toBe(0);

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    const raises = attentionRaise.mock.calls.map(decodeRaise);
    expect(raises).toHaveLength(1);
    expect(raises[0].producer).toBe("abandoned-dispatch");
    expect(raises[0].issue).toBe(282);
    // No stage: the pipeline never started one.
    expect(raises[0].stage).toBeUndefined();
    // …and the situation says so. Round 3 sent this arm down the slot body,
    // which promised an operator a worktree that may hold uncommitted work and
    // Go-side state that may be stale — neither of which can exist for a
    // dispatch that wedged inside worktree creation.
    expect(raises[0].situation).toBe("reservation-never-started");
  });

  it("does not raise when slots drain normally — no force-clear, no card", async () => {
    const { factory } = createControllableFactory();
    const manager = newManager(factory);

    await manager.abortAll();

    expect(attentionRaise).not.toHaveBeenCalled();
  });

  it("swallows a raise failure without aborting the exactly-once bookkeeping", async () => {
    // The strongest constraint on this call site: a throw here would abort the
    // terminal bookkeeping #307 made exactly-once, turning a missing
    // notification into a leaked queue mark and a held scheduler seat.
    attentionRaise.mockRejectedValue(new Error("daemon not connected"));

    const { factory } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([repoQueueItem(282)]);
    await manager.fillSlots();

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    expect(mockQueue.complete).toHaveBeenCalledWith("octocat/acme", 282);
    expect(callbacks.onSlotFailed).toHaveBeenCalledTimes(1);
    expect(callbacks.onSlotCleaned).toHaveBeenCalledWith(expect.any(Number), 282);
    expect(manager.isShutdownInProgress).toBe(false);
    expect(mockLogger.warn).toHaveBeenCalledWith(
      "Force-clear: attention.raise failed (fail-open) — no card for this wedge",
      expect.objectContaining({ issueNumber: 282 })
    );
  });

  it("STILL cards a run that took the claim and wedged BEFORE its outcome callback fired", async () => {
    // The round-2 finding, made executable. `terminalOutcomeDispatched` is set
    // at terminal boundary 1 BEFORE `await slot.stateService.getState()`; the
    // outcome callback fires after that await. A run wedged in that window
    // holds the claim and has reported NOTHING:
    //
    //   - no onSlotCompleted / onSlotFailed, so bootstrap never called
    //     autonomousComplete and the Go scheduler's running-slot seat is held;
    //   - step 2 of the force-clear stands down on the claim, so nobody books
    //     one on its behalf either.
    //
    // Gating the card on the claim (round 2's fix for a different case)
    // therefore made this condition silent END TO END — the exact thing #305
    // exists to stop. The card is now gated on `ownTerminalOutcomeBooked`, set
    // only after a callback actually returns, so this wedge speaks up.
    const { factory, handles } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([repoQueueItem(282)]);
    await manager.fillSlots();

    const handle = handles.get(282)!;
    handle.wedgeAfterClaim();
    handle.resolveRun({ success: true, completedStages: [], failedStage: undefined });
    await vi.advanceTimersByTimeAsync(0);

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    // Premise: the claim WAS taken, and the outcome callback never fired.
    expect(mockLogger.warn).toHaveBeenCalledWith(
      "Force-clearing stuck slot — booking its terminal state (#307)",
      expect.objectContaining({
        issueNumber: 282,
        runAlreadyClaimedItsOwnOutcome: true,
        runReportedItsOwnOutcome: false,
      })
    );
    expect(callbacks.onSlotCompleted).not.toHaveBeenCalled();
    expect(callbacks.onSlotFailed).not.toHaveBeenCalled();

    // …so the card is the only signal left, and it is raised — as the
    // claim-taken situation, because step 2 stood down and NOBODY booked the
    // terminal outcome. This is the case round 3's single body told the
    // operator "NOTHING IS BLOCKED and no action is required" while the Go
    // scheduler's seat for the issue was still held.
    const raises = attentionRaise.mock.calls.map(decodeRaise);
    expect(raises).toHaveLength(1);
    expect(raises[0].producer).toBe("abandoned-dispatch");
    expect(raises[0].issue).toBe(282);
    expect(raises[0].situation).toBe("claim-taken-then-wedged");
  });

  it("does NOT card a run whose own outcome callback fired before it wedged in teardown", async () => {
    // The case round 2 was right about, gated on the right signal. Here the run
    // reached its terminal branch, FIRED onSlotCompleted (bootstrap turned that
    // into autonomousComplete — the Go seat is freed, the cascade breaker is
    // charged) and only then wedged, in the finally's queue-mark release. The
    // card would assert the run reported nothing, which is false.
    const { factory, handles } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([repoQueueItem(282)]);
    await manager.fillSlots();

    // The run's OWN completeQueueItem never settles; the force-clear's later
    // one does. That wedges the slot strictly after the outcome callback.
    mockQueue.complete.mockImplementationOnce(() => new Promise<void>(() => {}));

    const handle = handles.get(282)!;
    handle.resolveRun({ success: true, completedStages: [], failedStage: undefined });
    await vi.advanceTimersByTimeAsync(0);
    expect(callbacks.onSlotCompleted).toHaveBeenCalledTimes(1);

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    expect(mockLogger.warn).toHaveBeenCalledWith(
      "Force-clearing stuck slot — booking its terminal state (#307)",
      expect.objectContaining({ issueNumber: 282, runReportedItsOwnOutcome: true })
    );
    expect(attentionRaise).not.toHaveBeenCalled();
    // Step 2 also stood down — the outcome was already booked, once.
    expect(callbacks.onSlotFailed).not.toHaveBeenCalled();
  });

  it("issues the raise CONCURRENTLY with the queue-mark release, not chained after it", async () => {
    // Both are IPC round-trips bounded by IpcClientBase's 30s request timeout,
    // and the daemon is unresponsive by definition when this deadline fires.
    // Serialising them would grow the worst-case `isShuttingDown` hold from
    // ~30s to ~60s on top of the 30s deadline already spent — the #3111
    // condition arriving through the fix for it, and the bound
    // `forceClearStuckSlots`' own doc comment still states.
    const { factory } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([repoQueueItem(282)]);
    await manager.fillSlots();

    let releaseComplete!: () => void;
    mockQueue.complete.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          releaseComplete = () => resolve();
        })
    );

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);

    // The queue-mark release is STILL in flight, and the raise has already been
    // issued. Chained, this would be zero.
    expect(attentionRaise).toHaveBeenCalledTimes(1);

    releaseComplete();
    await vi.advanceTimersByTimeAsync(0);
    await abortPromise;
  });

  it("skips the card when the slot has no owner/name identity", async () => {
    // A slot with no resolvable repo has no card identity. A legitimate local
    // state, not a card with a malformed repo in it.
    const { factory } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(282)]);
    await manager.fillSlots();

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    expect(attentionRaise).not.toHaveBeenCalled();
    // …and the bookkeeping still happened.
    expect(callbacks.onSlotFailed).toHaveBeenCalledTimes(1);
  });
});
