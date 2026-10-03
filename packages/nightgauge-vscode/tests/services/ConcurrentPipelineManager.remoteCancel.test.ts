/**
 * #2344: a platform cancel of a triggered run that has no slot yet applies.
 *
 * The run can be anywhere between the queue and its slot: still queued,
 * dequeued by a fill and waiting for an earlier start, or reserved while its
 * worktree is created (or its repository is resolved, the last await before
 * the slot opens). Wherever it is, the cancel is acknowledged `applied`, no
 * slot ever opens for it, and its queue mark is released without queueing it
 * again. The tombstone is keyed by the platform run id, so a later trigger of
 * the same issue, under its own run id, runs.
 *
 * A trigger for an issue the operator already queued here attaches its run
 * to that work instead (#2344): to the waiting item, or to the dispatch
 * already on its way to a slot. Cancelling such a run detaches it, and the
 * operator's work goes on.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

/** Holds one issue's worktree creation, or its repository lookup, until released. */
const gate = vi.hoisted(() => ({
  worktreeIssue: null as number | null,
  slugIssue: null as number | null,
  reached: false,
  release: () => {},
}));
const worktreeCleanup = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));

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

/** Wait at the gate when `issueNumber` is the one it holds. */
async function maybeHold(issueNumber: number, held: number | null): Promise<void> {
  if (held !== issueNumber) return;
  gate.reached = true;
  await new Promise<void>((resolve) => (gate.release = resolve));
}

vi.mock("../../src/utils/WorktreeManager", () => ({
  WorktreeManager: vi.fn(function () {
    return {
      create: vi.fn().mockImplementation(async (issueNumber: number, branchName: string) => {
        await maybeHold(issueNumber, gate.worktreeIssue);
        return {
          path: `/test-repo/.worktrees/issue-${issueNumber}`,
          branch: branchName,
          issueNumber,
          exists: true,
        };
      }),
      cleanup: worktreeCleanup,
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
  getConcurrentPipelineConfig: vi.fn().mockReturnValue({ maxConcurrent: 3 }),
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      gitComposeBranchName: vi.fn(async (issueNumber: number) => ({
        name: `feat/${issueNumber}-issue`,
      })),
      autonomousStatus: vi.fn().mockResolvedValue({ status: "running" }),
      autonomousPause: vi.fn().mockResolvedValue(undefined),
    }),
  },
}));

import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";
import { RemoteRunLedger } from "../../src/services/RemoteRunLedger";
import { RunVerbCommandHandler } from "../../src/services/RunVerbCommandHandler";
import { fakeCloneLayout } from "../helpers/cloneLayout";

const SUCCESS = {
  success: true,
  completedStages: [],
  skippedStages: [],
  deferredStages: [],
  totalDurationMs: 1,
};

interface Item {
  issueNumber: number;
  title: string;
  position: number;
  status: string;
  addedAt: string;
  repoName?: string;
  remoteRunId?: string;
  remoteRunAttached?: boolean;
}

/** The repository every item and trigger below names. */
const REPO = "acme/api";

function item(issueNumber: number, remoteRunId?: string): Item {
  return {
    issueNumber,
    title: `Issue #${issueNumber}`,
    position: 1,
    status: "pending",
    addedAt: new Date().toISOString(),
    ...(remoteRunId ? { remoteRunId } : {}),
  };
}

/** An item the operator queued here for the issue: it serves no remote run. */
function local(issueNumber: number): Item {
  return { ...item(issueNumber), repoName: REPO };
}

/**
 * A manager over a queue that behaves as Go's does: a dequeue marks items
 * taken (processing) without removing them and hands out copies, `complete`
 * releases a taken item, `removeRemoteRun` removes a waiting item of that
 * run or detaches the run from an item it was attached to, and every change
 * is announced.
 */
function buildManager(queued: Item[]) {
  const waiting = [...queued];
  const taken: Item[] = [];
  const listeners: Array<(state: unknown) => void> = [];
  const announce = () => {
    const state = { items: [...waiting, ...taken].map((i) => ({ ...i })), status: "waiting" };
    for (const l of listeners) l(state);
  };
  /** queue.add for a trigger, as Go's QueueAddItem does it. */
  const addLikeGo = (repo: string, issueNumber: number, remoteRunId: string): void => {
    const existing = [...waiting, ...taken].find(
      (i) => i.issueNumber === issueNumber && (i.repoName ?? "") === repo
    );
    if (!existing) {
      waiting.push({ ...item(issueNumber, remoteRunId), repoName: repo });
    } else if (!existing.remoteRunId && waiting.includes(existing)) {
      existing.remoteRunId = remoteRunId;
      existing.remoteRunAttached = true;
    }
    announce();
  };
  const queueService = {
    dequeueIndependent: vi.fn(async (n: number) => {
      const out = waiting.splice(0, Math.max(0, n));
      taken.push(...out);
      announce();
      return out.map((i) => ({ ...i }));
    }),
    updateActiveSlots: vi.fn().mockResolvedValue(undefined),
    drainBlockedSuccessors: vi.fn().mockResolvedValue([]),
    enqueue: vi.fn().mockResolvedValue(null),
    complete: vi.fn(async (_repo: string, issueNumber: number) => {
      const i = taken.findIndex((t) => t.issueNumber === issueNumber);
      if (i >= 0) taken.splice(i, 1);
      announce();
    }),
    clear: vi.fn().mockResolvedValue(undefined),
    getQueue: vi.fn(async () => ({
      items: [...waiting, ...taken].map((i) => ({ ...i })),
      status: "waiting",
    })),
    removeRemoteRun: vi.fn(async (remoteRunId: string) => {
      const attached = [...waiting, ...taken].find(
        (i) => i.remoteRunId === remoteRunId && i.remoteRunAttached
      );
      if (attached) {
        delete attached.remoteRunId;
        delete attached.remoteRunAttached;
        announce();
        return true;
      }
      const i = waiting.findIndex((w) => w.remoteRunId === remoteRunId);
      if (i < 0) return false;
      waiting.splice(i, 1);
      announce();
      return true;
    }),
    onQueueChanged: (listener: (state: unknown) => void) => {
      listeners.push(listener);
      return { dispose: () => {} };
    },
  };
  const finishers = new Map<number, (result: unknown) => void>();
  const built = new Map<number, { orchestrator: any; stateService: any }>();
  const factory = vi.fn().mockImplementation((_dir: string, issueNumber: number) => {
    const run = new Promise((resolve) => finishers.set(issueNumber, resolve));
    const parts = {
      orchestrator: {
        setWorktreeOverride: vi.fn(),
        setRunRepoRoot: vi.fn(),
        setUnattended: vi.fn(),
        setRepoOverride: vi.fn(),
        resolveRunRepoSlug: vi.fn(async () => {
          await maybeHold(issueNumber, gate.slugIssue);
          return "acme/api";
        }),
        runPipeline: vi.fn().mockReturnValue(run),
        stop: vi.fn(),
        gracefulStop: vi.fn().mockResolvedValue(undefined),
        dispose: vi.fn(),
      },
      stateService: {
        hasRunState: vi.fn(() => true),
        isPaused: vi.fn(() => false),
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
      },
    };
    built.set(issueNumber, parts);
    return parts;
  });
  const onSlotFailed = vi.fn();
  const manager = new ConcurrentPipelineManager(
    "/test-repo",
    queueService as any,
    factory,
    { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn(), getChannel: vi.fn() } as any,
    { maxConcurrent: 3 }
  );
  manager.setCallbacks({ onSlotCompleted: vi.fn(), onSlotFailed });
  const finish = async (issueNumber: number) => {
    finishers.get(issueNumber)?.(SUCCESS);
    await manager.settleForTest(issueNumber);
  };
  /**
   * A trigger's placement, its enqueue done as Go's queue.add does it.
   * `pinned`: the trigger asks for its own adapter and model (#1656).
   */
  const place = (issueNumber: number, remoteRunId: string, repo = REPO, pinned = false) => {
    const enqueue = vi.fn(async () => {
      addLikeGo(repo, issueNumber, remoteRunId);
      return true;
    });
    const placed = manager.placeRemoteRun(
      { remoteRunId, issueNumber, repo, ...(pinned ? { pinned } : {}) },
      enqueue
    );
    return { placed, enqueue };
  };
  return {
    manager,
    queueService,
    factory,
    built,
    onSlotFailed,
    waiting,
    taken,
    finish,
    place,
    addLikeGo,
  };
}

/** Whether a slot was ever started for the issue. */
function started(built: Map<number, { orchestrator: any }>, issueNumber: number): boolean {
  return (built.get(issueNumber)?.orchestrator.runPipeline.mock.calls.length ?? 0) > 0;
}

describe("ConcurrentPipelineManager — a platform cancel before the slot opens (#2344)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    fakeCloneLayout("/test-repo");
    gate.worktreeIssue = null;
    gate.slugIssue = null;
    gate.reached = false;
    gate.release = () => {};
  });

  it("removes a run still waiting in the queue, and no slot opens for it", async () => {
    const { manager, queueService, factory, waiting } = buildManager([item(500, "run-500")]);
    expect(await manager.holdsRemoteRun("run-500")).toBe(true);

    expect(await manager.cancelByRemoteRunId("run-500")).toBe("applied");
    expect(queueService.removeRemoteRun).toHaveBeenCalledWith("run-500");
    expect(waiting).toEqual([]);

    await manager.fillSlots();
    expect(factory).not.toHaveBeenCalled();
    expect(manager.getActiveSlots()).toEqual([]);
    expect(await manager.holdsRemoteRun("run-500")).toBe(false);
    // A pause or resume of it now finds no run here.
    expect(await manager.pauseByRemoteRunId("run-500")).toBe("no-active-run");
  });

  it("drops a run a fill dequeued while it waited for an earlier start", async () => {
    const { manager, queueService, built, finish } = buildManager([item(1), item(501, "run-501")]);
    gate.worktreeIssue = 1;
    const fill = manager.fillSlots();
    await vi.waitFor(() => expect(gate.reached).toBe(true));
    // 501 is dequeued, marked taken, and waits for 1's start.
    expect(await manager.holdsRemoteRun("run-501")).toBe(true);

    expect(await manager.cancelByRemoteRunId("run-501")).toBe("applied");
    // A fill already took it, so it could not be removed from the queue.
    expect(queueService.removeRemoteRun).toHaveBeenCalledWith("run-501");
    expect(await queueService.removeRemoteRun.mock.results[0].value).toBe(false);

    gate.release();
    await fill;
    expect(started(built, 1)).toBe(true);
    expect(started(built, 501)).toBe(false);
    expect(built.has(501)).toBe(false);
    // Its processing mark is released, and it is never queued again.
    expect(queueService.complete).toHaveBeenCalledWith("", 501);
    expect(queueService.enqueue).not.toHaveBeenCalled();
    expect(await manager.holdsRemoteRun("run-501")).toBe(false);
    await finish(1);
  });

  it("tears down a dispatch cancelled while its worktree is created", async () => {
    const { manager, queueService, built, onSlotFailed } = buildManager([item(502, "run-502")]);
    gate.worktreeIssue = 502;
    const fill = manager.fillSlots();
    await vi.waitFor(() => expect(gate.reached).toBe(true));
    expect(await manager.holdsRemoteRun("run-502")).toBe(true);

    expect(await manager.cancelByRemoteRunId("run-502")).toBe("applied");

    gate.release();
    await fill;
    expect(manager.getActiveSlots()).toEqual([]);
    expect(manager.findSlotByRemoteRunId("run-502")).toBeNull();
    expect(started(built, 502)).toBe(false);
    expect(built.get(502)?.stateService.beginRun).not.toHaveBeenCalled();
    expect(built.get(502)?.orchestrator.dispose).toHaveBeenCalled();
    expect(worktreeCleanup).toHaveBeenCalledWith(502, true);
    // Reported as the operator's cancellation, once, with no spend.
    expect(onSlotFailed).toHaveBeenCalledTimes(1);
    expect(onSlotFailed.mock.calls[0][1]).toBe(502);
    expect(onSlotFailed.mock.calls[0][2].message).toBe("Cancelled by user");
    expect(onSlotFailed.mock.calls[0][3]).toBe(0);
    expect(queueService.complete).toHaveBeenCalledWith("", 502);
    expect(queueService.enqueue).not.toHaveBeenCalled();
    expect(await manager.holdsRemoteRun("run-502")).toBe(false);
  });

  it("opens no slot for a run cancelled during the last await before the slot", async () => {
    const { manager, built, onSlotFailed } = buildManager([item(503, "run-503")]);
    gate.slugIssue = 503;
    const fill = manager.fillSlots();
    await vi.waitFor(() => expect(gate.reached).toBe(true));

    expect(await manager.cancelByRemoteRunId("run-503")).toBe("applied");

    gate.release();
    await fill;
    expect(manager.getActiveSlots()).toEqual([]);
    expect(started(built, 503)).toBe(false);
    expect(built.get(503)?.stateService.beginRun).not.toHaveBeenCalled();
    expect(onSlotFailed).toHaveBeenCalledTimes(1);
  });

  it("does not stop a later trigger of the same issue, under its own run id", async () => {
    const { manager, built, waiting, finish } = buildManager([item(504, "run-504a")]);
    expect(await manager.cancelByRemoteRunId("run-504a")).toBe("applied");
    await manager.fillSlots();
    expect(built.has(504)).toBe(false);

    // The issue is triggered again: a new item, a new platform run id.
    waiting.push(item(504, "run-504b"));
    await manager.fillSlots();
    expect(started(built, 504)).toBe(true);
    expect(built.get(504)?.stateService.beginRun.mock.calls[0][3]).toBe("run-504b");
    expect(manager.findSlotByRemoteRunId("run-504b")).toBe(504);
    expect(await manager.holdsRemoteRun("run-504b")).toBe(true);
    await finish(504);
  });

  it("answers no-active-run for a run that is not here, and cancels nothing", async () => {
    const { manager, queueService, built, waiting } = buildManager([item(505, "run-505")]);
    expect(await manager.cancelByRemoteRunId("run-elsewhere")).toBe("no-active-run");
    expect(waiting).toHaveLength(1);
    await manager.fillSlots();
    expect(started(built, 505)).toBe(true);
    expect(queueService.complete).not.toHaveBeenCalledWith("", 505);
  });

  // A queue that cannot be read (the daemon restarting) is judged by its
  // latest known state, as for a pause or resume: the cancel applies, and the
  // run is dropped once the queue answers again.
  it("applies a cancel the queue cannot answer for a run it last showed queued", async () => {
    const { manager, queueService, built } = buildManager([item(511, "run-511")]);
    expect(await manager.holdsRemoteRun("run-511")).toBe(true);
    queueService.removeRemoteRun.mockRejectedValueOnce(new Error("daemon restarting"));
    queueService.getQueue.mockRejectedValueOnce(new Error("daemon restarting"));

    expect(await manager.cancelByRemoteRunId("run-511")).toBe("applied");

    await manager.fillSlots();
    expect(built.has(511)).toBe(false);
    expect(queueService.complete).toHaveBeenCalledWith("", 511);
  });

  // A refusal says the run is not here, so it leaves no tombstone behind: a
  // run the cancel could not see, which does start, is not dropped unseen.
  it("refuses a cancel for a run it cannot find, and does not drop that run later", async () => {
    const { manager, queueService, built, finish } = buildManager([item(512, "run-512")]);
    queueService.removeRemoteRun.mockRejectedValueOnce(new Error("daemon restarting"));
    queueService.getQueue.mockRejectedValueOnce(new Error("daemon restarting"));

    expect(await manager.cancelByRemoteRunId("run-512")).toBe("no-active-run");

    await manager.fillSlots();
    expect(started(built, 512)).toBe(true);
    expect(manager.findSlotByRemoteRunId("run-512")).toBe(512);
    await finish(512);
  });

  it("re-queues a dequeued remote run with its run id after a failed start", async () => {
    const { manager, queueService } = buildManager([item(506, "run-506")]);
    // The first start fails to build the worktree; the item goes back, still
    // serving the same remote run, so the platform's verbs still reach it.
    const { WorktreeManager } = await import("../../src/utils/WorktreeManager");
    const instance = vi.mocked(WorktreeManager).mock.results[0].value;
    instance.create.mockRejectedValueOnce(new Error("disk full"));
    queueService.dequeueIndependent.mockImplementationOnce(async () => [item(506, "run-506")]);
    queueService.dequeueIndependent.mockImplementation(async () => []);
    await manager.fillSlots();
    expect(queueService.enqueue).toHaveBeenCalledWith(506, "Issue #506", undefined, undefined, {
      remoteRunId: "run-506",
    });
  });

  // #2357: the machine's other windows read which runs this one holds: every
  // run it answers for, from the trigger's placement to the slot's end.
  it("publishes the platform runs it holds as they come and go", async () => {
    const { manager, finish, place } = buildManager([]);
    const published: string[][] = [];
    manager.onHeldRemoteRunsChanged((runIds) => published.push(runIds));

    expect(await place(600, "run-600").placed).toBe("queued");
    expect(manager.heldRemoteRunIds()).toEqual(["run-600"]);
    gate.worktreeIssue = 600;
    const fill = manager.fillSlots();
    await vi.waitFor(() => expect(gate.reached).toBe(true));
    gate.release();
    await fill;
    // The slot carries the run now: the same set, so nothing more is published.
    expect(manager.findSlotByRemoteRunId("run-600")).toBe(600);
    expect(published).toEqual([["run-600"]]);

    // A run queued here and nowhere else is listed from the queue's state.
    expect(await place(601, "run-601").placed).toBe("queued");
    expect(manager.heldRemoteRunIds()).toEqual(["run-600", "run-601"]);
    await finish(600);
    expect(published).toEqual([["run-600"], ["run-600", "run-601"], ["run-601"]]);
  });

  // After a reload the queue the daemon kept still carries the run, and no
  // queue change announces it: the window reads the queue once.
  it("lists a queued run no queue change announced once it reads the queue", async () => {
    const { manager } = buildManager([{ ...item(602, "run-602"), repoName: REPO }]);
    expect(manager.heldRemoteRunIds()).toEqual([]);
    await manager.syncQueuedRemoteRuns();
    expect(manager.heldRemoteRunIds()).toEqual(["run-602"]);
  });

  // The daemon announces queue changes concurrently, so an older state can
  // arrive after a newer one; it must not bring back a run that left.
  it("ignores a queue state older than the one it already has", async () => {
    const listeners: Array<(state: unknown) => void> = [];
    const manager = new ConcurrentPipelineManager(
      "/test-repo",
      {
        onQueueChanged: (l: (state: unknown) => void) => {
          listeners.push(l);
          return { dispose: () => {} };
        },
      } as any,
      vi.fn(),
      { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn(), getChannel: vi.fn() } as any,
      { maxConcurrent: 1 }
    );
    const at = (second: number) => new Date(Date.UTC(2026, 9, 2, 12, 0, second)).toISOString();
    const announce = (runIds: string[], updatedAt: string) => {
      const items = runIds.map((id, n) => ({ ...item(610 + n, id), repoName: REPO }));
      for (const l of listeners) l({ items, status: "waiting", updated_at: updatedAt });
    };
    announce(["run-610"], at(1));
    expect(manager.heldRemoteRunIds()).toEqual(["run-610"]);
    announce([], at(3));
    expect(manager.heldRemoteRunIds()).toEqual([]);
    announce(["run-610"], at(2));
    expect(manager.heldRemoteRunIds()).toEqual([]);
  });
});

describe("ConcurrentPipelineManager — a trigger for an issue already queued here (#2344)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    fakeCloneLayout("/test-repo");
    gate.worktreeIssue = null;
    gate.slugIssue = null;
    gate.reached = false;
    gate.release = () => {};
  });

  it("attaches a trigger accepted while the operator's dispatch creates its worktree", async () => {
    const { manager, built, place, finish } = buildManager([local(700)]);
    gate.worktreeIssue = 700;
    const fill = manager.fillSlots();
    await vi.waitFor(() => expect(gate.reached).toBe(true));

    expect(await manager.remoteTriggerConflict(700, REPO, false)).toBeNull();
    const { placed, enqueue } = place(700, "run-700");
    expect(await placed).toBe("attached");
    // The issue is dispatched already; queueing it again would be skipped.
    expect(enqueue).not.toHaveBeenCalled();
    expect(await manager.holdsRemoteRun("run-700")).toBe(true);
    expect(manager.heldRemoteRunIds()).toEqual(["run-700"]);
    expect(await manager.pauseByRemoteRunId("run-700")).toBe("not-started");

    gate.release();
    await fill;
    // The slot serves the run: it reports under the run id, and the
    // platform's verbs reach it.
    expect(built.get(700)?.stateService.beginRun.mock.calls[0][3]).toBe("run-700");
    expect(manager.findSlotByRemoteRunId("run-700")).toBe(700);
    expect(await manager.cancelByRemoteRunId("run-700")).toBe("applied");
    expect(built.get(700)?.orchestrator.gracefulStop).toHaveBeenCalled();
    await finish(700);
  });

  it("attaches a trigger accepted while the operator's item waits in the batch for an earlier start", async () => {
    const { manager, built, place, finish } = buildManager([local(1), local(701)]);
    gate.worktreeIssue = 1;
    const fill = manager.fillSlots();
    await vi.waitFor(() => expect(gate.reached).toBe(true));

    expect(await place(701, "run-701").placed).toBe("attached");
    expect(await manager.holdsRemoteRun("run-701")).toBe(true);

    gate.release();
    await fill;
    expect(built.get(1)?.stateService.beginRun.mock.calls[0][3]).toBeUndefined();
    expect(built.get(701)?.stateService.beginRun.mock.calls[0][3]).toBe("run-701");
    expect(manager.findSlotByRemoteRunId("run-701")).toBe(701);
    await finish(1);
    await finish(701);
  });

  it("detaches a cancelled run from the operator's dispatch, which runs as queued", async () => {
    const { manager, built, place, onSlotFailed, finish } = buildManager([local(702)]);
    gate.worktreeIssue = 702;
    const fill = manager.fillSlots();
    await vi.waitFor(() => expect(gate.reached).toBe(true));
    expect(await place(702, "run-702").placed).toBe("attached");

    expect(await manager.cancelByRemoteRunId("run-702")).toBe("applied");
    expect(await manager.holdsRemoteRun("run-702")).toBe(false);
    expect(manager.heldRemoteRunIds()).toEqual([]);

    gate.release();
    await fill;
    expect(started(built, 702)).toBe(true);
    expect(built.get(702)?.stateService.beginRun.mock.calls[0][3]).toBeUndefined();
    expect(manager.findSlotByRemoteRunId("run-702")).toBeNull();
    expect(onSlotFailed).not.toHaveBeenCalled();
    await finish(702);
  });

  it("detaches a cancelled run from the operator's waiting item, which stays queued", async () => {
    const { manager, built, waiting, place, finish } = buildManager([local(703)]);
    expect(await place(703, "run-703").placed).toBe("queued");
    expect(waiting).toHaveLength(1);
    expect(waiting[0]).toMatchObject({ remoteRunId: "run-703", remoteRunAttached: true });

    expect(await manager.cancelByRemoteRunId("run-703")).toBe("applied");
    expect(waiting).toHaveLength(1);
    expect(waiting[0].remoteRunId).toBeUndefined();

    await manager.fillSlots();
    expect(started(built, 703)).toBe(true);
    expect(built.get(703)?.stateService.beginRun.mock.calls[0][3]).toBeUndefined();
    await finish(703);
  });

  it("detaches a cancelled run from a dequeued item it was attached to before the dequeue", async () => {
    const { manager, built, place, finish } = buildManager([local(1), local(704)]);
    expect(await place(704, "run-704").placed).toBe("queued");
    gate.worktreeIssue = 1;
    const fill = manager.fillSlots();
    await vi.waitFor(() => expect(gate.reached).toBe(true));

    expect(await manager.cancelByRemoteRunId("run-704")).toBe("applied");

    gate.release();
    await fill;
    expect(started(built, 704)).toBe(true);
    expect(built.get(704)?.stateService.beginRun.mock.calls[0][3]).toBeUndefined();
    await finish(1);
    await finish(704);
  });

  it("refuses a trigger for an issue queued or dispatched here for another platform run", async () => {
    const queued = buildManager([{ ...local(705), remoteRunId: "run-705a" }]);
    expect(await queued.manager.remoteTriggerConflict(705, REPO, false)).toBe("busy");
    expect(await queued.place(705, "run-705b").placed).toBe("busy");
    expect(await queued.manager.holdsRemoteRun("run-705b")).toBe(false);
    expect(queued.manager.heldRemoteRunIds()).toEqual(["run-705a"]);

    const dispatched = buildManager([{ ...local(706), remoteRunId: "run-706a" }]);
    gate.worktreeIssue = 706;
    const fill = dispatched.manager.fillSlots();
    await vi.waitFor(() => expect(gate.reached).toBe(true));
    expect(await dispatched.manager.remoteTriggerConflict(706, REPO, false)).toBe("busy");
    const { placed, enqueue } = dispatched.place(706, "run-706b");
    expect(await placed).toBe("busy");
    expect(enqueue).not.toHaveBeenCalled();
    gate.release();
    await fill;
    expect(dispatched.built.get(706)?.stateService.beginRun.mock.calls[0][3]).toBe("run-706a");
    await dispatched.finish(706);
  });

  // #1656: a trigger that asks for its own adapter and model is never served
  // by the operator's work for the issue, which runs on the operator's. It is
  // refused before the ack, and a placement that raced the operator's
  // dispatch does not attach to it.
  it("refuses a pinned trigger for an issue the operator queued or is dispatching here", async () => {
    const queued = buildManager([local(708)]);
    expect(await queued.manager.remoteTriggerConflict(708, REPO, true)).toBe("pinned");
    expect(await queued.manager.remoteTriggerConflict(708, REPO, false)).toBeNull();

    const dispatched = buildManager([local(709)]);
    gate.worktreeIssue = 709;
    const fill = dispatched.manager.fillSlots();
    await vi.waitFor(() => expect(gate.reached).toBe(true));
    expect(await dispatched.manager.remoteTriggerConflict(709, REPO, true)).toBe("pinned");
    const { placed, enqueue } = dispatched.place(709, "run-709", REPO, true);
    expect(await placed).toBe("pinned");
    expect(enqueue).not.toHaveBeenCalled();
    expect(await dispatched.manager.holdsRemoteRun("run-709")).toBe(false);
    gate.release();
    await fill;
    // The operator's dispatch runs as queued, under no platform run id.
    expect(dispatched.built.get(709)?.stateService.beginRun.mock.calls[0][3]).toBeUndefined();
    expect(dispatched.manager.findSlotByRemoteRunId("run-709")).toBeNull();
    await dispatched.finish(709);
  });

  it("does not place a trigger whose issue's slot is open", async () => {
    const { manager, place, finish } = buildManager([local(707)]);
    await manager.fillSlots();
    expect(await manager.remoteTriggerConflict(707, REPO, false)).toBe("running");
    const { placed, enqueue } = place(707, "run-707");
    expect(await placed).toBe("running");
    expect(enqueue).not.toHaveBeenCalled();
    expect(await manager.holdsRemoteRun("run-707")).toBe(false);
    await finish(707);
  });

  // The fill's dequeue and the trigger's placement take turns, so the dequeue
  // never takes the issue between the trigger's check and its enqueue.
  it("makes a fill wait for a trigger's enqueue, so the dispatch carries the run", async () => {
    const { manager, queueService, built, finish, addLikeGo } = buildManager([local(708)]);
    let release!: () => void;
    const enqueueReached = new Promise<void>((resolve) => (release = resolve));
    const placing = manager.placeRemoteRun(
      { remoteRunId: "run-708", issueNumber: 708, repo: REPO },
      async () => {
        await enqueueReached;
        addLikeGo(REPO, 708, "run-708");
        return true;
      }
    );
    const fill = manager.fillSlots();
    await new Promise((resolve) => setImmediate(resolve));
    expect(queueService.dequeueIndependent).not.toHaveBeenCalled();

    release();
    // The item was still waiting, so the queue attached the run to it.
    expect(await placing).toBe("queued");
    await fill;
    expect(built.get(708)?.stateService.beginRun.mock.calls[0][3]).toBe("run-708");
    expect(manager.findSlotByRemoteRunId("run-708")).toBe(708);
    await finish(708);
  });
});

/** A promise and the function that settles it. */
function deferred<T = void>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

/**
 * Make the next dequeue wait at a gate, as Go's does while it reads the
 * issues' blockers from GitHub: the fill holds the queue turn meanwhile.
 */
function gateNextDequeue(queueService: ReturnType<typeof buildManager>["queueService"]) {
  const gate = deferred();
  const dequeue = queueService.dequeueIndependent.getMockImplementation()!;
  queueService.dequeueIndependent.mockImplementationOnce(async (n: number) => {
    await gate.promise;
    return dequeue(n);
  });
  return gate;
}

describe("ConcurrentPipelineManager — a cancel while the run is placed or goes back (#2344, #2357)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    fakeCloneLayout("/test-repo");
    gate.worktreeIssue = null;
    gate.slugIssue = null;
    gate.reached = false;
    gate.release = () => {};
  });

  // A fill's dequeue holds the queue turn the trigger's placement waits for.
  // The ack is out, so the platform can already send verbs for the run.
  it("holds a trigger's run from the ack on, while a fill's dequeue holds the queue turn", async () => {
    const { manager, queueService, built, place } = buildManager([]);
    const dequeue = gateNextDequeue(queueService);
    const fill = manager.fillSlots();
    await vi.waitFor(() => expect(queueService.dequeueIndependent).toHaveBeenCalled());

    const { placed, enqueue } = place(900, "run-900");
    expect(await manager.holdsRemoteRun("run-900")).toBe(true);
    expect(manager.heldRemoteRunIds()).toEqual(["run-900"]);
    expect(await manager.pauseByRemoteRunId("run-900")).toBe("not-started");

    expect(await manager.cancelByRemoteRunId("run-900")).toBe("applied");
    expect(await manager.holdsRemoteRun("run-900")).toBe(false);

    dequeue.resolve();
    expect(await placed).toBe("cancelled");
    await fill;
    // Nothing was queued for it, and no slot opens for it.
    expect(enqueue).not.toHaveBeenCalled();
    await manager.fillSlots();
    expect(built.has(900)).toBe(false);
    expect(manager.heldRemoteRunIds()).toEqual([]);
  });

  // The same gap seen from the platform: one window, the real run-verb
  // handler and ledger. Before the fix the cancel was refused no-active-run
  // after the grace, and the run was then queued and started.
  it("gives the platform an applied cancel for a run acked while a fill holds the queue turn", async () => {
    const { manager, queueService, built, place } = buildManager([]);
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "remote-cancel-turn-"));
    try {
      const dequeue = gateNextDequeue(queueService);
      const fill = manager.fillSlots();
      await vi.waitFor(() => expect(queueService.dequeueIndependent).toHaveBeenCalled());
      const { placed } = place(901, "run-901");

      const platform = { agentAcknowledgeCommand: vi.fn().mockResolvedValue({ runId: "" }) };
      const handler = new RunVerbCommandHandler(
        manager,
        platform as any,
        { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() } as any,
        undefined,
        undefined,
        { ledger: new RemoteRunLedger(dir, { windowId: 101, isAlive: () => true }), graceMs: 30 }
      );
      handler.setAgentId("agent-machine");
      await handler.consume(
        {
          id: "cmd-cancel-901",
          type: "cancel",
          payload: { runId: "run-901" },
          createdAt: "2026-10-02T00:00:00.000Z",
          owner: "acme",
          repo: "api",
        },
        "cancel"
      );
      expect(platform.agentAcknowledgeCommand).toHaveBeenCalledTimes(1);
      expect(platform.agentAcknowledgeCommand.mock.calls[0].slice(1, 3)).toEqual([
        "cmd-cancel-901",
        "applied",
      ]);

      dequeue.resolve();
      expect(await placed).toBe("cancelled");
      await fill;
      await manager.fillSlots();
      expect(built.has(901)).toBe(false);
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
  });

  // Go marks a dequeued item taken before its reply reaches the window, and
  // leaves a taken item to its dispatch, so a cancel that arrives meanwhile
  // cannot remove it. The dispatch drops it when the reply lands, which can
  // be before the cancel reads the queue: the cancel still applied.
  it("applies a cancel whose run a fill was dequeuing, though the fill dropped it before the cancel read the queue", async () => {
    const { manager, queueService, built, waiting, taken } = buildManager([item(902, "run-902")]);
    const dequeueReply = deferred();
    const dequeue = queueService.dequeueIndependent.getMockImplementation()!;
    queueService.dequeueIndependent.mockImplementationOnce(async (n: number) => {
      const out = await dequeue(n);
      await dequeueReply.promise;
      return out;
    });
    const removalReply = deferred();
    const remove = queueService.removeRemoteRun.getMockImplementation()!;
    queueService.removeRemoteRun.mockImplementationOnce(async (remoteRunId: string) => {
      const removed = await remove(remoteRunId);
      await removalReply.promise;
      return removed;
    });

    const fill = manager.fillSlots();
    await vi.waitFor(() => expect(taken.map((t) => t.issueNumber)).toEqual([902]));
    const cancel = manager.cancelByRemoteRunId("run-902");
    await vi.waitFor(() => expect(queueService.removeRemoteRun).toHaveBeenCalled());
    dequeueReply.resolve();
    await fill;
    // The dispatch found the run tombstoned and released it.
    expect(queueService.complete).toHaveBeenCalledWith("", 902);
    expect(taken).toEqual([]);
    removalReply.resolve();

    expect(await cancel).toBe("applied");
    expect(built.has(902)).toBe(false);
    expect(waiting).toEqual([]);
  });

  // A cancel that arrives while the enqueue is in flight finds nothing queued
  // yet; the placement takes the run back out, or a reload would start it.
  it("takes a run cancelled during its enqueue back out of the queue", async () => {
    const { manager, queueService, waiting, built, addLikeGo } = buildManager([]);
    const enqueueGate = deferred();
    let enqueueReached = false;
    const placing = manager.placeRemoteRun(
      { remoteRunId: "run-902", issueNumber: 902, repo: REPO },
      async () => {
        enqueueReached = true;
        await enqueueGate.promise;
        addLikeGo(REPO, 902, "run-902");
        return true;
      }
    );
    await vi.waitFor(() => expect(enqueueReached).toBe(true));

    expect(await manager.cancelByRemoteRunId("run-902")).toBe("applied");
    enqueueGate.resolve();
    expect(await placing).toBe("cancelled");
    expect(queueService.removeRemoteRun).toHaveBeenLastCalledWith("run-902");
    expect(waiting).toEqual([]);
    await manager.fillSlots();
    expect(built.has(902)).toBe(false);
  });

  // The ceiling drops while the batch starts, and the platform cancels a run
  // the fill already took: the run is dropped, not handed back to the queue,
  // which outlives the in-memory tombstone across a window reload.
  it("never hands a cancelled run back to the queue when the ceiling dropped during the fill", async () => {
    const { manager, queueService, built, finish } = buildManager([
      local(1),
      { ...item(903, "run-903"), repoName: REPO },
    ]);
    gate.worktreeIssue = 1;
    const fill = manager.fillSlots();
    await vi.waitFor(() => expect(gate.reached).toBe(true));

    expect(await manager.cancelByRemoteRunId("run-903")).toBe("applied");
    manager.setWorkspaceThrottle({ maxConcurrent: 1, resumeAt: null });
    gate.release();
    await fill;

    expect(started(built, 903)).toBe(false);
    expect(queueService.complete).toHaveBeenCalledWith(REPO, 903);
    expect(queueService.enqueue).not.toHaveBeenCalled();
    await finish(1);
    manager.dispose();
  });

  it("never hands back a run cancelled while it goes back to the queue", async () => {
    const { manager, queueService, built, finish } = buildManager([
      local(1),
      { ...item(904, "run-904"), repoName: REPO },
    ]);
    gate.worktreeIssue = 1;
    const fill = manager.fillSlots();
    await vi.waitFor(() => expect(gate.reached).toBe(true));
    manager.setWorkspaceThrottle({ maxConcurrent: 1, resumeAt: null });

    // 904's processing mark is being released when the cancel arrives.
    const completeGate = deferred();
    let completeReached = false;
    const complete = queueService.complete.getMockImplementation()!;
    queueService.complete.mockImplementationOnce(async (repo: string, issueNumber: number) => {
      completeReached = true;
      await completeGate.promise;
      return complete(repo, issueNumber);
    });
    gate.release();
    await vi.waitFor(() => expect(completeReached).toBe(true));
    expect(await manager.cancelByRemoteRunId("run-904")).toBe("applied");
    completeGate.resolve();
    await fill;

    expect(started(built, 904)).toBe(false);
    expect(queueService.enqueue).not.toHaveBeenCalled();
    await finish(1);
    manager.dispose();
  });

  it("drops a cancelled run whose slot failed to start, instead of queueing it again", async () => {
    const { manager, queueService, built } = buildManager([item(905, "run-905")]);
    const { WorktreeManager } = await import("../../src/utils/WorktreeManager");
    const instance = vi.mocked(WorktreeManager).mock.results[0].value;
    const createGate = deferred();
    let createReached = false;
    instance.create.mockImplementationOnce(async () => {
      createReached = true;
      await createGate.promise;
      throw new Error("disk full");
    });
    const fill = manager.fillSlots();
    await vi.waitFor(() => expect(createReached).toBe(true));

    expect(await manager.cancelByRemoteRunId("run-905")).toBe("applied");
    createGate.resolve();
    await fill;

    expect(started(built, 905)).toBe(false);
    expect(queueService.complete).toHaveBeenCalledWith("", 905);
    expect(queueService.enqueue).not.toHaveBeenCalled();
    expect(await manager.holdsRemoteRun("run-905")).toBe(false);
  });
});
