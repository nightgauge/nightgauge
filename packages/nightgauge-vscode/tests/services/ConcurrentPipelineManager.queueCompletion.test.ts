/**
 * #254 — every autonomous run leaked a permanent "processing" queue item.
 *
 * #232/#246 changed `DequeueIndependent` from splicing an item out of the Go
 * queue to marking it `status: "processing"`, so an in-flight run stays visible
 * to `queueStatusLocked()` and cloud sync. Removal moved to
 * `CompleteQueueItem`, called from a terminal defer in Go's
 * `Scheduler.runPipeline()`.
 *
 * But the extension never enters `runPipeline`. It dequeues over IPC
 * (`queue.dequeueIndependent` → `ConcurrentPipelineManager.fillSlots`) and then
 * runs the stages itself. So the mark was applied on the extension path and the
 * sweep was wired only to the Go path: #240 finished all six stages, merged its
 * PR, and sat in the queue "processing" forever. The re-dispatch guard
 * (`if item.Status == "processing" { continue }`) then made that issue
 * permanently undispatchable, and `queueStatusLocked` reported `processing`
 * with nothing running — inverting the very signal #232 set out to fix.
 *
 * These tests assert the invariant #246 created: an item marked `processing`
 * must be completed on EVERY path where dispatch does not lead to a terminal
 * run — success, failure, duplicate-skip, and slot-start failure.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

const mockWorktreeCreate = vi.fn().mockImplementation((issueNumber: number, branchName: string) =>
  Promise.resolve({
    path: `/test-repo/.worktrees/issue-${issueNumber}`,
    branch: branchName,
    issueNumber,
    exists: true,
  })
);

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
      create: mockWorktreeCreate,
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

vi.mock("../../src/utils/incrediConfig", () => ({
  getConcurrentPipelineConfig: vi
    .fn()
    .mockReturnValue({ maxConcurrent: 1, worktreeBase: ".worktrees" }),
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      autonomousStatus: vi.fn().mockResolvedValue({ status: "paused" }),
      autonomousPause: vi.fn().mockResolvedValue(undefined),
    }),
  },
}));

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";

const REPO = "nightgauge/nightgauge";

function makeQueueItem(issueNumber: number) {
  return {
    issueNumber,
    title: `Issue #${issueNumber}`,
    position: 1,
    status: "pending",
    addedAt: new Date().toISOString(),
    labels: [],
    repoName: REPO,
  };
}

function createControllableFactory() {
  const resolvers = new Map<number, (result: any) => void>();
  const rejecters = new Map<number, (error: any) => void>();
  const factory = vi.fn().mockImplementation((_workDir: string, issueNumber: number) => {
    const promise = new Promise((resolve, reject) => {
      resolvers.set(issueNumber, resolve);
      rejecters.set(issueNumber, reject);
    });
    return {
      orchestrator: {
        setWorktreeOverride: vi.fn(),
        setUnattended: vi.fn(),
        setRepoOverride: vi.fn(),
        setRunRepoRoot: vi.fn(),
        runPipeline: vi.fn().mockReturnValue(promise),
        stop: vi.fn(),
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
        initializePipeline: vi.fn().mockResolvedValue(undefined),
        setMeta: vi.fn(),
        dispose: vi.fn(),
      },
    };
  });
  return {
    factory,
    resolve: (issueNumber: number, result: any) => resolvers.get(issueNumber)?.(result),
    reject: (issueNumber: number, error: any) => rejecters.get(issueNumber)?.(error),
  };
}

function makeManager(batches: any[][], maxConcurrent = 1) {
  let call = 0;
  const queueService = {
    dequeueIndependent: vi.fn().mockImplementation(async () => batches[call++] ?? []),
    complete: vi.fn().mockResolvedValue(undefined),
    updateActiveSlots: vi.fn().mockResolvedValue(undefined),
    drainBlockedSuccessors: vi.fn().mockResolvedValue([]),
    enqueue: vi.fn().mockResolvedValue(null),
    clear: vi.fn().mockResolvedValue(undefined),
    getQueue: vi.fn().mockResolvedValue({ items: [], status: "idle" }),
  };
  const controllable = createControllableFactory();
  const manager = new ConcurrentPipelineManager(
    "/test-repo",
    queueService as any,
    controllable.factory,
    { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn(), getChannel: vi.fn() } as any,
    { maxConcurrent, worktreeBase: ".worktrees" }
  );
  return { manager, queueService, controllable };
}

const SUCCESS = {
  success: true,
  completedStages: [
    "issue-pickup",
    "feature-planning",
    "feature-dev",
    "feature-validate",
    "pr-create",
    "pr-merge",
  ],
  skippedStages: [],
  deferredStages: [],
  totalDurationMs: 30000,
};

const FAILURE = {
  success: false,
  completedStages: ["issue-pickup"],
  skippedStages: [],
  deferredStages: [],
  failedStage: "feature-dev",
  error: new Error("boom"),
  totalDurationMs: 10000,
};

describe("ConcurrentPipelineManager — queue completion on the extension path (#254)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockWorktreeCreate.mockImplementation((issueNumber: number, branchName: string) =>
      Promise.resolve({
        path: `/test-repo/.worktrees/issue-${issueNumber}`,
        branch: branchName,
        issueNumber,
        exists: true,
      })
    );
  });

  it("completes the dequeued item after a successful run", async () => {
    const { manager, queueService, controllable } = makeManager([[makeQueueItem(240)]]);

    await manager.fillSlots();
    expect(queueService.complete).not.toHaveBeenCalled(); // still in flight

    controllable.resolve(240, SUCCESS);
    await manager.settleForTest(240);

    // This is the exact leak #240 hit: six stages green, PR merged, and the
    // item still marked "processing" in the Go queue.
    expect(queueService.complete).toHaveBeenCalledWith(REPO, 240);
  });

  it("completes the dequeued item after a failed run", async () => {
    const { manager, queueService, controllable } = makeManager([[makeQueueItem(241)]]);

    await manager.fillSlots();
    controllable.resolve(241, FAILURE);
    await manager.settleForTest(241);

    expect(queueService.complete).toHaveBeenCalledWith(REPO, 241);
  });

  it("completes the dequeued item after a throwing run without an unhandled rejection", async () => {
    const { manager, queueService, controllable } = makeManager([[makeQueueItem(242)]]);

    await manager.fillSlots();
    controllable.reject(242, new Error("boom"));
    await manager.settleForTest(242);

    expect(queueService.complete).toHaveBeenCalledWith(REPO, 242);
  });

  it("completes a duplicate-skipped item (#188) — it was marked processing but never run", async () => {
    // maxConcurrent 2 so the occupied slot below still leaves capacity to
    // dequeue; at 1 the fill short-circuits before dequeueing and the
    // duplicate-skip branch is never reached.
    const { manager, queueService } = makeManager([[makeQueueItem(243)]], 2);

    // Simulate the live slot a prior dispatch created.
    (manager as any).slots.set(243, { index: 0, issueNumber: 243, title: "dup" });

    const started = await manager.fillSlots();

    expect(started).toBe(0);
    // Skipping without completing would strand the mark forever: no run will
    // ever reach a terminal state for this dequeue.
    expect(queueService.complete).toHaveBeenCalledWith(REPO, 243);
    expect(queueService.enqueue).not.toHaveBeenCalled();
  });

  it("completes before re-enqueueing when the slot fails to start", async () => {
    const { manager, queueService } = makeManager([[makeQueueItem(244)], []]);
    mockWorktreeCreate.mockRejectedValue(new Error("worktree add failed"));

    await manager.fillSlots();

    // The re-enqueue adds a fresh PENDING item. Without completing the
    // processing mark first, the queue accumulates both — and the processing
    // one blocks the pending one from ever dispatching.
    expect(queueService.complete).toHaveBeenCalledWith(REPO, 244);
    expect(queueService.enqueue).toHaveBeenCalled();
    const completeOrder = queueService.complete.mock.invocationCallOrder[0];
    const enqueueOrder = queueService.enqueue.mock.invocationCallOrder[0];
    expect(completeOrder).toBeLessThan(enqueueOrder);
  });
});
