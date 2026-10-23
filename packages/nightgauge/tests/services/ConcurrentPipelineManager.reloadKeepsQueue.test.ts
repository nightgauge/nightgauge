/**
 * #2396: a window reload or close ends the window's runs and nothing more.
 *
 * `deactivate()` used to call `abortAll()`, which begins by clearing the
 * queue, so a reload with one pipeline running emptied the daemon's
 * persisted queue: every waiting issue, platform-triggered runs included,
 * was gone when the window came back. A reload now passes `keepQueued`: the
 * items the ending dispatches took ("processing") are dropped, since nothing
 * else would release them once the window is gone, and every waiting item
 * stays for the next window. Stop All still clears the queue.
 */

import { describe, it, expect, vi, beforeEach, type Mock } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";

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

/**
 * Worktree creations a test holds open, by issue number, so it can close the
 * window while a fill is still starting its batch (see {@link holdWorktree}).
 */
const heldWorktrees = new Map<number, { entered: () => void; released: Promise<void> }>();

vi.mock("../../src/utils/WorktreeManager", () => ({
  WorktreeManager: vi.fn(function () {
    return {
      create: vi.fn().mockImplementation(async (issueNumber: number, branchName: string) => {
        const held = heldWorktrees.get(issueNumber);
        if (held) {
          held.entered();
          await held.released;
        }
        return {
          path: `/test-repo/.worktrees/issue-${issueNumber}`,
          branch: branchName,
          issueNumber,
          exists: true,
        };
      }),
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

/**
 * #889 — `startSlot` now asks the Go binary for THE branch name over
 * `git.composeBranchName` and fails closed if it cannot. These tests are about
 * dispatch, not naming, so they need a stand-in that answers; a faithful one
 * (prefix from labels, number once) keeps any name assertions meaningful.
 */
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
  repoName?: string;
  remoteRunId?: string;
}

function makeQueueItem(issueNumber: number, fields: Partial<QueueItem> = {}): QueueItem {
  return {
    issueNumber,
    title: `Issue #${issueNumber}`,
    position: 1,
    status: "pending",
    addedAt: new Date().toISOString(),
    ...fields,
  };
}

/** Hold an issue's worktree creation open until `release()`. */
function holdWorktree(issueNumber: number): { entered: Promise<void>; release: () => void } {
  let entered!: () => void;
  let release!: () => void;
  const enteredPromise = new Promise<void>((resolve) => (entered = resolve));
  const released = new Promise<void>((resolve) => (release = resolve));
  heldWorktrees.set(issueNumber, { entered, released });
  return { entered: enteredPromise, release };
}

function createControllableFactory() {
  const resolvers = new Map<number, (result: any) => void>();
  const stops = new Map<number, Mock>();
  const factory = vi.fn().mockImplementation((_workDir: string, issueNumber: number) => {
    const promise = new Promise((resolve) => resolvers.set(issueNumber, resolve));
    const stop = vi.fn();
    stops.set(issueNumber, stop);
    return {
      orchestrator: {
        setWorktreeOverride: vi.fn(),
        setRunRepoRoot: vi.fn(),
        setUnattended: vi.fn(),
        // ADR-017 step 3 (#370): the slot resolves its repo through the
        // orchestrator when the queue item and the workspace manifest cannot
        // name one, BEFORE beginRun installs the identity.
        resolveRunRepoSlug: vi.fn().mockResolvedValue("nightgauge/nightgauge"),
        runPipeline: vi.fn().mockReturnValue(promise),
        stop,
        dispose: vi.fn(),
      },
      stateService: {
        onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
        onPhaseStart: vi.fn().mockReturnValue({ dispose: vi.fn() }),
        onPhaseComplete: vi.fn().mockReturnValue({ dispose: vi.fn() }),
        onUnifiedTokenUsage: vi.fn().mockReturnValue({ dispose: vi.fn() }),
        getState: vi.fn().mockResolvedValue(null),
        // ADR-017 step 3 (#370): the manager installs the dispatch's run
        // identity on the slot's own state service before anything emits.
        beginRun: vi.fn(),
        endRun: vi.fn(),
        getRunId: vi.fn().mockReturnValue(null),
        initEmpty: vi.fn(),
        setMeta: vi.fn(),
        dispose: vi.fn(),
      },
    };
  });
  return {
    factory,
    finishWith: (issueNumber: number, payload: any) => resolvers.get(issueNumber)?.(payload),
    stopMockFor: (issueNumber: number) => stops.get(issueNumber),
  };
}

function buildManager(issueNumbers: number[], items = issueNumbers.map((n) => makeQueueItem(n))) {
  const queueService = {
    dequeueIndependent: vi.fn().mockResolvedValueOnce(items).mockResolvedValue([]),
    updateActiveSlots: vi.fn().mockResolvedValue(undefined),
    drainBlockedSuccessors: vi.fn().mockResolvedValue([]),
    enqueue: vi.fn().mockResolvedValue(null),
    clear: vi.fn().mockResolvedValue(undefined),
    dropProcessing: vi.fn().mockResolvedValue({ dropped: issueNumbers.length, kept: 0 }),
    complete: vi.fn().mockResolvedValue(undefined),
    removeRemoteRun: vi.fn().mockResolvedValue(false),
    getQueue: vi.fn().mockResolvedValue({ items: [], status: "idle" }),
  };

  const controllable = createControllableFactory();
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
    { maxConcurrent: Math.max(1, issueNumbers.length) }
  );
  manager.setCallbacks({ onSlotFailed: vi.fn(), onSlotCompleted: vi.fn() });
  return { manager, queueService, controllable };
}

const STOPPED = {
  success: false,
  completedStages: [],
  skippedStages: [],
  deferredStages: [],
  failedStage: "feature-dev",
  totalDurationMs: 1000,
};

describe("ConcurrentPipelineManager — a window reload keeps the queue (#2396)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    heldWorktrees.clear();
    mockAutonomousStatus.mockResolvedValue({ status: "running" });
  });

  it("drops only the dispatched items, before its first await, and never clears the queue", async () => {
    const { manager, queueService, controllable } = buildManager([41]);
    await manager.fillSlots();

    const abort = manager.abortAll({ keepQueued: true });
    // Sent synchronously: a reloading window may be gone before anything
    // after the first await runs. #41's run started, so nothing goes back.
    expect(queueService.dropProcessing).toHaveBeenCalledTimes(1);
    expect(queueService.dropProcessing).toHaveBeenCalledWith([]);
    expect(queueService.clear).not.toHaveBeenCalled();

    // Every microtask the abort chains runs before a setImmediate callback.
    await new Promise((resolve) => setImmediate(resolve));
    expect(controllable.stopMockFor(41)).toHaveBeenCalledTimes(1);
    controllable.finishWith(41, STOPPED);
    await abort;

    expect(queueService.clear).not.toHaveBeenCalled();
    expect(queueService.dropProcessing).toHaveBeenCalledTimes(1);
    expect(manager.activeSlotCount).toBe(0);
  });

  it("keeps listing the platform runs the queue carries", async () => {
    const { manager, queueService, controllable } = buildManager([42]);
    queueService.getQueue.mockResolvedValue({
      items: [{ issueNumber: 7, title: "Issue #7", status: "pending", remoteRunId: "run-queued" }],
      status: "waiting",
    });
    await manager.syncQueuedRemoteRuns();
    await manager.fillSlots();
    expect(manager.heldRemoteRunIds()).toContain("run-queued");

    const abort = manager.abortAll({ keepQueued: true });
    controllable.finishWith(42, STOPPED);
    await abort;

    expect(manager.heldRemoteRunIds()).toContain("run-queued");
  });

  it("Stop All still clears the queue and drops nothing separately", async () => {
    const { manager, queueService, controllable } = buildManager([43]);
    await manager.fillSlots();

    const abort = manager.abortAll();
    expect(queueService.clear).toHaveBeenCalledTimes(1);
    controllable.finishWith(43, STOPPED);
    await abort;

    expect(queueService.dropProcessing).not.toHaveBeenCalled();
  });

  it("hands back the dispatches of the fill not begun yet, with the platform runs they serve", async () => {
    // One dequeue marks its whole batch processing, and the batch starts one
    // item at a time: a reload during #41's worktree creation must not take
    // #42 and #43, which never began, with it.
    const { manager, queueService, controllable } = buildManager(
      [41, 42, 43],
      [
        makeQueueItem(41),
        makeQueueItem(42, { repoName: "o/r" }),
        makeQueueItem(43, { remoteRunId: "run-43" }),
      ]
    );
    const worktree41 = holdWorktree(41);
    const fill = manager.fillSlots();
    await worktree41.entered;

    const abort = manager.abortAll({ keepQueued: true });
    expect(queueService.dropProcessing).toHaveBeenCalledWith([
      { repo: "o/r", issueNumber: 42 },
      { repo: "", issueNumber: 43, remoteRunId: "run-43" },
    ]);

    worktree41.release();
    expect(await fill).toBe(0);
    await abort;

    // #41's start was under way: the drop removed its item, and a closing
    // window neither re-queues it nor releases a mark the drop handed back.
    expect(controllable.factory).not.toHaveBeenCalled();
    expect(queueService.enqueue).not.toHaveBeenCalled();
    expect(queueService.complete).not.toHaveBeenCalled();
    expect(manager.activeSlotCount).toBe(0);
  });

  it("hands back an attached run with the operator's item, and not a run the platform cancelled", async () => {
    const { manager, queueService } = buildManager(
      [41, 42, 43],
      [
        makeQueueItem(41),
        makeQueueItem(42, { repoName: "o/r" }),
        makeQueueItem(43, { repoName: "o/r", remoteRunId: "run-43" }),
      ]
    );
    const worktree41 = holdWorktree(41);
    const fill = manager.fillSlots();
    await worktree41.entered;

    // A trigger for #42 attaches its run to the operator's waiting dispatch,
    // and the platform cancels #43's own run while it waits.
    expect(
      await manager.placeRemoteRun(
        { remoteRunId: "run-42", issueNumber: 42, repo: "o/r" },
        async () => true
      )
    ).toBe("attached");
    expect(await manager.cancelByRemoteRunId("run-43")).toBe("applied");

    const abort = manager.abortAll({ keepQueued: true });
    expect(queueService.dropProcessing).toHaveBeenCalledWith([
      { repo: "o/r", issueNumber: 42, remoteRunId: "run-42", remoteRunAttached: true },
    ]);
    worktree41.release();
    await fill;
    await abort;
  });

  it("a fill that waited for its queue turn while the window closed dequeues nothing", async () => {
    const { manager, queueService } = buildManager([44]);
    let finishEnqueue!: (queued: boolean) => void;
    // A trigger's placement holds the queue turn the fill's dequeue needs.
    const placing = manager.placeRemoteRun(
      { remoteRunId: "run-9", issueNumber: 9, repo: "o/r" },
      () => new Promise<boolean>((resolve) => (finishEnqueue = resolve))
    );
    const fill = manager.fillSlots();
    await new Promise((resolve) => setImmediate(resolve));

    const abort = manager.abortAll({ keepQueued: true });
    finishEnqueue(true);
    await placing;
    expect(await fill).toBe(0);
    await abort;

    expect(queueService.dequeueIndependent).not.toHaveBeenCalled();
  });

  it("sends the drop with nothing in flight, and the window dispatches nothing more", async () => {
    // The drop also releases what the main orchestrator took, so it goes out
    // whatever this manager is doing.
    const { manager, queueService } = buildManager([45]);
    await manager.abortAll({ keepQueued: true });
    expect(queueService.dropProcessing).toHaveBeenCalledWith([]);

    expect(await manager.fillSlots()).toBe(0);
    expect(queueService.dequeueIndependent).not.toHaveBeenCalled();
  });
});

describe("deactivate() takes the reload path (#2396)", () => {
  const source = readFileSync(path.resolve(__dirname, "../../src/extension.ts"), "utf-8");
  const deactivate = source.slice(source.indexOf("export function deactivate()"));

  it("always keeps the queue, before the main orchestrator stops, and never clears it", () => {
    expect(deactivate).toMatch(
      /\n {2}concurrentPipelineManager\?\.abortAll\(\{ keepQueued: true \}\)/
    );
    expect(deactivate.indexOf("abortAll({ keepQueued: true })")).toBeLessThan(
      deactivate.indexOf("headlessOrchestrator.stop()")
    );
    expect(deactivate).not.toMatch(/\.abortAll\(\)/);
  });
});
