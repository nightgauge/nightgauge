/**
 * #423: pausing a concurrent-slot run must not terminate it.
 *
 * Before this fix, HeadlessOrchestrator's stage loop BROKE out on
 * `isPaused()`, returning `{success:false, failedStage:undefined}` from the
 * slot's `runPipeline()` call while the run was merely paused. That result
 * has no "paused" arm in ConcurrentPipelineManager.processSlot's terminal
 * classification, so it fell into the generic failure arm: `onSlotFailed`
 * fired, `haltQueueOnSlotFailure` paused autonomous, and `cleanupSlot`
 * deleted the slot and disposed its state service — after which Resume could
 * never target the run again.
 *
 * The fix makes the stage loop HOLD at the pause boundary instead of
 * breaking out of it: `isPaused()` is polled and the loop only continues
 * once it flips back to false. Concretely for ConcurrentPipelineManager, this
 * means the slot's own `orchestrator.runPipeline()` call simply does not
 * resolve while paused — so nothing here needs to know about "paused" as a
 * distinct terminal outcome. These tests pin exactly that consequence: while
 * a slot is paused (represented by its mocked runPipeline() call staying
 * pending), the slot remains in `getActiveSlots()` and `onSlotFailed` is
 * never called; once resumed (the promise finally resolves), the slot
 * completes through the normal success path.
 *
 * @see src/services/HeadlessOrchestrator.ts — the PAUSE_POLL_INTERVAL_MS hold
 * @see src/commands/resumePipeline.ts — the runIsHeld branch that relies on
 *      this same in-flight-promise invariant to avoid a duplicate dispatch
 */

import { describe, it, expect, vi, beforeEach, type Mock } from "vitest";

vi.mock("vscode", () => ({
  EventEmitter: class {
    private listeners: Array<(...args: any[]) => void> = [];
    event = (listener: (...args: any[]) => void) => {
      this.listeners.push(listener);
      return { dispose: () => {} };
    };
    fire = (data: any) => this.listeners.forEach((l) => l(data));
    dispose = vi.fn();
  },
  workspace: { workspaceFolders: [{ uri: { fsPath: "/test-repo" } }] },
  window: {
    showErrorMessage: vi.fn().mockResolvedValue(undefined),
    showWarningMessage: vi.fn().mockResolvedValue(undefined),
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
      getWorktreePath: vi
        .fn()
        .mockImplementation((n: number) => `/test-repo/.worktrees/issue-${n}`),
    };
  }),
}));

vi.mock("../../src/utils/nightgaugeConfig", () => ({
  getConcurrentPipelineConfig: vi.fn().mockReturnValue({ maxConcurrent: 2 }),
}));

const mockAutonomousPause = vi.fn().mockResolvedValue(undefined);
const mockAutonomousStatus = vi.fn().mockResolvedValue({ status: "running" });

const gitComposeBranchName = vi.fn(
  async (issueNumber: number, title: string, labels?: string[]) => {
    const prefix = labels?.some((l) => l.toLowerCase().replace(/^type:/, "") === "bug")
      ? "fix/"
      : "feat/";
    const slug = title
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-|-$/g, "")
      .replace(new RegExp(`^${issueNumber}-`), "")
      .substring(0, 50);
    return { name: `${prefix}${issueNumber}-${slug}` };
  }
);

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      gitComposeBranchName,
      autonomousStatus: mockAutonomousStatus,
      autonomousPause: mockAutonomousPause,
    }),
  },
}));

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";
import { fakeCloneLayout } from "../helpers/cloneLayout";

// Per-clone data resolves under the git directory (ADR-024 § 7); map the
// fake roots (and their worktrees) to a clone without running git.
beforeEach(() => {
  fakeCloneLayout("/test-repo");
});

interface QueueItem {
  issueNumber: number;
  title: string;
  position: number;
  status: string;
  addedAt: string;
  remoteRunId?: string;
}

/** A queued item; `remoteRunId` marks it queued for a platform trigger (#2344). */
function makeQueueItem(issueNumber: number, remoteRunId?: string): QueueItem {
  return {
    issueNumber,
    title: `Issue #${issueNumber}`,
    position: 1,
    status: "pending",
    addedAt: new Date().toISOString(),
    ...(remoteRunId ? { remoteRunId } : {}),
  };
}

function createControllableFactory() {
  const resolvers = new Map<number, (result: any) => void>();
  const stops = new Map<number, Mock>();
  // #423: the per-slot PipelineStateService this fixture hands back is the
  // SAME object `getSlotStateService` will resolve to later — a stand-in for
  // the invariant runSelector.ts relies on (resolving a slot to its own
  // stateService instance).
  const stateServices = new Map<number, any>();
  const factory = vi.fn().mockImplementation((_workDir: string, issueNumber: number) => {
    const promise = new Promise((resolve) => resolvers.set(issueNumber, resolve));
    const stop = vi.fn();
    stops.set(issueNumber, stop);
    // The pause flag the real PipelineStateService keeps on its loaded state.
    let paused = false;
    const stateService = {
      // initEmpty() seeds the real service's state before the slot is live.
      hasRunState: vi.fn(() => true),
      isPaused: vi.fn(() => paused),
      pausePipeline: vi.fn(async () => {
        paused = true;
        return true;
      }),
      resumePipeline: vi.fn(async () => {
        paused = false;
        return true;
      }),
      onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onPhaseStart: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onPhaseComplete: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onUnifiedTokenUsage: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      getState: vi.fn().mockResolvedValue(null),
      beginRun: vi.fn(),
      endRun: vi.fn(),
      getRunId: vi.fn().mockReturnValue(`run-${issueNumber}`),
      getIssueNumber: vi.fn().mockReturnValue(issueNumber),
      initEmpty: vi.fn(),
      setMeta: vi.fn(),
      dispose: vi.fn(),
    };
    stateServices.set(issueNumber, stateService);
    return {
      orchestrator: {
        setWorktreeOverride: vi.fn(),
        setRunRepoRoot: vi.fn(),
        setUnattended: vi.fn(),
        resolveRunRepoSlug: vi.fn().mockResolvedValue("nightgauge/nightgauge"),
        runPipeline: vi.fn().mockReturnValue(promise),
        stop,
        gracefulStop: vi.fn().mockResolvedValue(undefined),
        dispose: vi.fn(),
      },
      stateService,
    };
  });
  return {
    factory,
    finishWith: (issueNumber: number, payload: any) => resolvers.get(issueNumber)?.(payload),
    stopMockFor: (issueNumber: number) => stops.get(issueNumber),
    stateServiceFor: (issueNumber: number) => stateServices.get(issueNumber),
  };
}

function buildManager(queued: Array<number | QueueItem>) {
  const items = queued.map((q) => (typeof q === "number" ? makeQueueItem(q) : q));
  const queueService = {
    dequeueIndependent: vi.fn().mockResolvedValueOnce(items).mockResolvedValue([]),
    updateActiveSlots: vi.fn().mockResolvedValue(undefined),
    drainBlockedSuccessors: vi.fn().mockResolvedValue([]),
    enqueue: vi.fn().mockResolvedValue(null),
    complete: vi.fn().mockResolvedValue(undefined),
    clear: vi.fn().mockResolvedValue(undefined),
    getQueue: vi.fn().mockResolvedValue({ items: [], status: "idle" }),
    removeRemoteRun: vi.fn().mockResolvedValue(false),
  };

  const controllable = createControllableFactory();
  const onSlotFailed = vi.fn();
  const onSlotCompleted = vi.fn();

  const manager = new ConcurrentPipelineManager(
    "/test-repo",
    queueService as any,
    controllable.factory,
    {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
      getChannel: vi.fn(),
    } as any,
    { maxConcurrent: Math.max(1, items.length) }
  );

  manager.setCallbacks({ onSlotFailed, onSlotCompleted });

  return { manager, queueService, controllable, onSlotFailed, onSlotCompleted };
}

describe("ConcurrentPipelineManager — pause holds the slot instead of ending it (#423)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAutonomousStatus.mockResolvedValue({ status: "running" });
  });

  it("a paused slot stays in getActiveSlots(), reachable by its own stateService, with no failure booked", async () => {
    const { manager, controllable, onSlotFailed } = buildManager([423]);

    await manager.fillSlots();

    // Simulated pause: under the hold design this is exactly what happens to
    // the slot's own runPipeline() promise while HeadlessOrchestrator's
    // stage loop is polling isPaused() — it does not resolve.
    expect(manager.getActiveSlots().map((s) => s.issueNumber)).toEqual([423]);

    // A runSelector-style lookup must still resolve this slot and its state
    // service — the exact capability the paused run must stay reachable for.
    const active = manager.getActiveSlots();
    expect(active).toHaveLength(1);
    const slotService = manager.getSlotStateService(active[0].slotIndex);
    expect(slotService).toBe(controllable.stateServiceFor(423));
    expect(slotService?.getRunId()).toBe("run-423");

    // No terminal outcome has fired — the run isn't done, it's held.
    expect(onSlotFailed).not.toHaveBeenCalled();
    expect(mockAutonomousPause).not.toHaveBeenCalled();

    // Resume: the held call finally resolves (as it would once
    // HeadlessOrchestrator's poll loop observes isPaused() go false and the
    // stage loop runs the pipeline to completion).
    controllable.finishWith(423, {
      success: true,
      completedStages: [
        "pipeline-start",
        "issue-pickup",
        "feature-planning",
        "feature-dev",
        "feature-validate",
        "pr-create",
        "pr-merge",
        "pipeline-finish",
      ],
      skippedStages: [],
      deferredStages: [],
      totalDurationMs: 5000,
    });

    await manager.settleForTest(423);

    expect(onSlotFailed).not.toHaveBeenCalled();
    // The slot is cleaned up on its REAL completion, not on the pause.
    expect(manager.getActiveSlots()).toHaveLength(0);
  });
});

// A platform pause and resume (#2334) act on the slot the platform's run id
// names, through the same per-slot pause flag `Nightgauge: Pause Pipeline`
// sets, so the stage loop holds at the next boundary and a resume continues
// the same run. Each verb reports what it did, for its ack.
describe("ConcurrentPipelineManager — platform verbs on a remote run id (#2334)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAutonomousStatus.mockResolvedValue({ status: "running" });
  });

  it("pauses and resumes the slot through its own state service, and reports each outcome", async () => {
    const { manager, controllable, onSlotFailed } = buildManager([
      makeQueueItem(423, "platform-run-423"),
    ]);
    await manager.fillSlots();
    const state = controllable.stateServiceFor(423);

    expect(await manager.resumeByRemoteRunId("platform-run-423")).toBe("not-paused");
    expect(await manager.pauseByRemoteRunId("platform-run-423")).toBe("applied");
    expect(state.pausePipeline).toHaveBeenCalledTimes(1);
    expect(state.isPaused()).toBe(true);
    expect(await manager.pauseByRemoteRunId("platform-run-423")).toBe("already-paused");
    expect(state.pausePipeline).toHaveBeenCalledTimes(1);

    // Held, not ended: the slot is still live and reachable by the verb.
    expect(manager.getActiveSlots().map((s) => s.issueNumber)).toEqual([423]);
    expect(onSlotFailed).not.toHaveBeenCalled();

    expect(await manager.resumeByRemoteRunId("platform-run-423")).toBe("applied");
    expect(state.resumePipeline).toHaveBeenCalledTimes(1);
    expect(state.isPaused()).toBe(false);

    controllable.finishWith(423, {
      success: true,
      completedStages: [],
      skippedStages: [],
      deferredStages: [],
      totalDurationMs: 1,
    });
    await manager.settleForTest(423);
  });

  // A pause and a resume replayed in one stream chunk are handled in the same
  // tick: the resume clears the flag while the pause is still persisting it.
  // Both took effect, in order, and the pause must say so rather than report
  // that the run had no state to pause (#2334 review).
  it("reports a pause and a resume handled in the same tick as both applied", async () => {
    const { manager, controllable } = buildManager([makeQueueItem(425, "platform-run-425")]);
    await manager.fillSlots();
    const state = controllable.stateServiceFor(425);
    const setFlag = state.pausePipeline.getMockImplementation();
    state.pausePipeline.mockImplementation(async () => {
      const persisted = setFlag(); // the flag moves at once, as in the real service
      await new Promise((resolve) => setTimeout(resolve, 5)); // the IPC persist
      return persisted;
    });

    const pause = manager.pauseByRemoteRunId("platform-run-425");
    const resume = manager.resumeByRemoteRunId("platform-run-425");

    expect(await pause).toBe("applied");
    expect(await resume).toBe("applied");
    expect(state.isPaused()).toBe(false);

    controllable.finishWith(425, {
      success: true,
      completedStages: [],
      skippedStages: [],
      deferredStages: [],
      totalDurationMs: 1,
    });
    await manager.settleForTest(425);
  });

  it("reports a pause of a slot with no loaded state as no-run-state, and exposes the run's state", async () => {
    const { manager, controllable } = buildManager([makeQueueItem(426, "platform-run-426")]);
    await manager.fillSlots();
    const state = controllable.stateServiceFor(426);
    state.getState.mockResolvedValue({ issue_number: 426 });

    expect(await manager.remoteRunState("platform-run-426")).toEqual({ issue_number: 426 });
    expect(await manager.remoteRunState("elsewhere")).toBeNull();

    state.hasRunState.mockReturnValue(false);
    expect(await manager.pauseByRemoteRunId("platform-run-426")).toBe("no-run-state");
    expect(state.pausePipeline).not.toHaveBeenCalled();

    controllable.finishWith(426, {
      success: true,
      completedStages: [],
      skippedStages: [],
      deferredStages: [],
      totalDurationMs: 1,
    });
    await manager.settleForTest(426);
  });

  // #2340: only the window that holds a run answers a verb for it. It holds
  // the run while a slot carries the platform run id, and while an item
  // queued for the run is on its way to a slot (#2344).
  it("holds a run its slot carries or its queue holds, and no other", async () => {
    const { manager, controllable, queueService } = buildManager([
      makeQueueItem(427, "platform-run-427"),
    ]);
    // Queued for its own run, behind 427: no slot for it yet.
    queueService.getQueue.mockResolvedValue({
      items: [makeQueueItem(427, "platform-run-427"), makeQueueItem(428, "platform-run-428")],
      status: "waiting",
    });

    expect(await manager.holdsRemoteRun("platform-run-427")).toBe(true);
    await manager.fillSlots();
    expect(manager.findSlotByRemoteRunId("platform-run-427")).toBe(427);
    expect(await manager.holdsRemoteRun("platform-run-427")).toBe(true);
    expect(await manager.holdsRemoteRun("platform-run-428")).toBe(true);
    expect(await manager.holdsRemoteRun("elsewhere")).toBe(false);

    // A pause or resume of the queued run says it has not started, not that
    // no run here carries it.
    expect(await manager.pauseByRemoteRunId("platform-run-428")).toBe("not-started");
    expect(await manager.resumeByRemoteRunId("platform-run-428")).toBe("not-started");

    // An item that left the queue no longer holds its run.
    queueService.getQueue.mockResolvedValue({
      items: [makeQueueItem(427, "platform-run-427")],
      status: "waiting",
    });
    expect(await manager.holdsRemoteRun("platform-run-428")).toBe(false);

    controllable.finishWith(427, {
      success: true,
      completedStages: [],
      skippedStages: [],
      deferredStages: [],
      totalDurationMs: 1,
    });
    await manager.settleForTest(427);
    // The run ended and its queue item was completed: no window holds it any more.
    queueService.getQueue.mockResolvedValue({ items: [], status: "idle" });
    expect(await manager.holdsRemoteRun("platform-run-427")).toBe(false);
  });

  // An accepted trigger whose issue left the queue since (Clear Queue, Remove
  // from Queue, a halt's drain) will never start here: the window must not
  // claim the run, and must not answer that it is queued here.
  it("does not hold a triggered run whose issue was removed from the queue", async () => {
    const { manager, queueService } = buildManager([]);
    const queued = { items: [makeQueueItem(430, "platform-run-430")], status: "waiting" };
    const placed = await manager.placeRemoteRun(
      { remoteRunId: "platform-run-430", issueNumber: 430, repo: "" },
      async () => {
        queueService.getQueue.mockResolvedValue(queued);
        return true;
      }
    );
    expect(placed).toBe("queued");
    expect(await manager.holdsRemoteRun("platform-run-430")).toBe(true);
    // When the queue cannot be read, its latest state still carries the run.
    queueService.getQueue.mockRejectedValueOnce(new Error("IPC closed"));
    expect(await manager.holdsRemoteRun("platform-run-430")).toBe(true);

    // The same issue queued again locally is not the platform's run.
    queueService.getQueue.mockResolvedValue({ items: [makeQueueItem(430)], status: "waiting" });
    expect(await manager.holdsRemoteRun("platform-run-430")).toBe(false);

    queueService.getQueue.mockResolvedValue({ items: [], status: "idle" });
    expect(await manager.holdsRemoteRun("platform-run-430")).toBe(false);

    // Nor once the queue cannot be read: its latest state no longer has it.
    queueService.getQueue.mockRejectedValue(new Error("IPC closed"));
    expect(await manager.holdsRemoteRun("platform-run-430")).toBe(false);
  });

  it("stops holding a queued run once Stop All clears the queue", async () => {
    const { manager, queueService } = buildManager([]);
    await manager.placeRemoteRun(
      { remoteRunId: "platform-run-429", issueNumber: 429, repo: "" },
      async () => {
        queueService.getQueue.mockResolvedValue({
          items: [makeQueueItem(429, "platform-run-429")],
          status: "waiting",
        });
        return true;
      }
    );
    queueService.clear.mockImplementation(async () => {
      queueService.getQueue.mockResolvedValue({ items: [], status: "idle" });
    });
    expect(await manager.holdsRemoteRun("platform-run-429")).toBe(true);

    await manager.abortAll();

    expect(await manager.holdsRemoteRun("platform-run-429")).toBe(false);
    // Nor when the queue cannot be read afterwards.
    queueService.getQueue.mockRejectedValue(new Error("IPC closed"));
    expect(await manager.holdsRemoteRun("platform-run-429")).toBe(false);
  });

  it("reports a run id no local slot carries as a no-op", async () => {
    const { manager, controllable } = buildManager([makeQueueItem(424, "platform-run-424")]);
    await manager.fillSlots();

    for (const verb of [
      () => manager.cancelByRemoteRunId("elsewhere"),
      () => manager.pauseByRemoteRunId("elsewhere"),
      () => manager.resumeByRemoteRunId("elsewhere"),
    ]) {
      expect(await verb()).toBe("no-active-run");
    }

    controllable.finishWith(424, {
      success: true,
      completedStages: [],
      skippedStages: [],
      deferredStages: [],
      totalDurationMs: 1,
    });
    await manager.settleForTest(424);
  });

  // #2339: a window reload ends the held runPipeline() call of a paused run;
  // the paused snapshot names its platform run id. The window that finds it
  // holds the run and says why a platform resume cannot continue it.
  it("holds a paused run a reload ended, and refuses a resume of it with the reason", async () => {
    const { manager } = buildManager([]);
    const published: string[][] = [];
    manager.onHeldRemoteRunsChanged((runIds) => published.push(runIds));

    manager.holdReloadInterruptedRun("platform-run-2339", 2339);
    expect(await manager.holdsRemoteRun("platform-run-2339")).toBe(true);
    expect(manager.heldRemoteRunIds()).toEqual(["platform-run-2339"]);
    expect(await manager.resumeByRemoteRunId("platform-run-2339")).toBe("resume-in-window");
    expect(await manager.pauseByRemoteRunId("platform-run-2339")).toBe("already-paused");

    // The window's Resume prompt starts a new run from the snapshot: the
    // platform run is no longer held here.
    manager.dropReloadInterruptedRun("platform-run-2339");
    expect(await manager.holdsRemoteRun("platform-run-2339")).toBe(false);
    expect(await manager.resumeByRemoteRunId("platform-run-2339")).toBe("no-active-run");
    expect(published).toEqual([["platform-run-2339"], []]);
  });

  // A platform cancel ends such a run: its paused snapshot is consumed, so
  // the platform run is over and the window no longer holds it.
  it("cancels a paused run a reload ended by consuming its snapshot", async () => {
    const { manager } = buildManager([]);
    const end = vi.fn().mockResolvedValue(undefined);
    manager.holdReloadInterruptedRun("platform-run-2340", 2340, end);

    expect(await manager.cancelByRemoteRunId("platform-run-2340")).toBe("applied");
    expect(end).toHaveBeenCalledTimes(1);
    expect(await manager.holdsRemoteRun("platform-run-2340")).toBe(false);
    expect(manager.heldRemoteRunIds()).toEqual([]);
    expect(await manager.cancelByRemoteRunId("platform-run-2340")).toBe("no-active-run");

    // A snapshot that cannot be consumed keeps the hold, and the cancel fails.
    const stuck = vi.fn().mockRejectedValue(new Error("EACCES"));
    manager.holdReloadInterruptedRun("platform-run-2341", 2341, stuck);
    await expect(manager.cancelByRemoteRunId("platform-run-2341")).rejects.toThrow("EACCES");
    expect(await manager.holdsRemoteRun("platform-run-2341")).toBe(true);
  });
});
