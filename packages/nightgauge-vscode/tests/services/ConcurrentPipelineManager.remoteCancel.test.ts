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

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";
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
  remoteRunId?: string;
}

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

/**
 * A manager over a queue that behaves as Go's does: a dequeue marks items
 * taken (processing) without removing them, `complete` releases a taken
 * item, and `removeRemoteRun` removes only a waiting item of that run.
 */
function buildManager(queued: Item[]) {
  const waiting = [...queued];
  const taken: Item[] = [];
  const queueService = {
    dequeueIndependent: vi.fn(async (n: number) => {
      const out = waiting.splice(0, Math.max(0, n));
      taken.push(...out);
      return out;
    }),
    updateActiveSlots: vi.fn().mockResolvedValue(undefined),
    drainBlockedSuccessors: vi.fn().mockResolvedValue([]),
    enqueue: vi.fn().mockResolvedValue(null),
    complete: vi.fn(async (_repo: string, issueNumber: number) => {
      const i = taken.findIndex((t) => t.issueNumber === issueNumber);
      if (i >= 0) taken.splice(i, 1);
    }),
    clear: vi.fn().mockResolvedValue(undefined),
    getQueue: vi.fn(async () => ({ items: [...waiting, ...taken], status: "waiting" })),
    removeRemoteRun: vi.fn(async (remoteRunId: string) => {
      const i = waiting.findIndex((w) => w.remoteRunId === remoteRunId);
      if (i < 0) return false;
      waiting.splice(i, 1);
      return true;
    }),
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
  return { manager, queueService, factory, built, onSlotFailed, waiting, taken, finish };
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
    manager.acceptRemoteRun("run-500", 500, "acme/api");
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

  // #2357: the machine's other windows read which runs this one holds.
  it("publishes the platform runs it holds as they come and go", async () => {
    const { manager, waiting, finish } = buildManager([]);
    const published: string[][] = [];
    manager.onHeldRemoteRunsChanged((runIds) => published.push(runIds));

    manager.acceptRemoteRun("run-600", 600, "acme/api");
    expect(manager.heldRemoteRunIds()).toEqual(["run-600"]);
    waiting.push(item(600, "run-600"));
    gate.worktreeIssue = 600;
    const fill = manager.fillSlots();
    await vi.waitFor(() => expect(gate.reached).toBe(true));
    gate.release();
    await fill;
    // The slot carries the run now: the same set, so nothing more is published.
    expect(manager.findSlotByRemoteRunId("run-600")).toBe(600);
    expect(published).toEqual([["run-600"]]);

    manager.acceptRemoteRun("run-601", 601, "acme/api");
    manager.forgetRemoteRun("run-601");
    await finish(600);
    expect(published).toEqual([["run-600"], ["run-600", "run-601"], ["run-600"], []]);
  });
});
