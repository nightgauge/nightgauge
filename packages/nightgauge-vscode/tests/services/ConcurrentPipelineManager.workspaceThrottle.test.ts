/**
 * #2337: the platform's workspace throttle caps this window's dispatch.
 *
 * While a throttle is in force, dispatch opens no slot above min(configured
 * max_concurrent, throttle.maxConcurrent); a slot already running is never
 * stopped. Clearing the throttle, raising it, or reaching its resumeAt fills
 * slots from the queue, as a finished slot does.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

/** Holds one issue's worktree creation until released, to act mid-fill. */
const worktreeGate = vi.hoisted(() => ({
  issue: null as number | null,
  reached: false,
  release: () => {},
}));

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
      create: vi.fn().mockImplementation(async (issueNumber: number, branchName: string) => {
        if (worktreeGate.issue === issueNumber) {
          worktreeGate.reached = true;
          await new Promise<void>((resolve) => (worktreeGate.release = resolve));
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

function queueItem(issueNumber: number, extra: Record<string, unknown> = {}) {
  return {
    issueNumber,
    title: `Issue #${issueNumber}`,
    position: 1,
    status: "pending",
    addedAt: new Date().toISOString(),
    ...extra,
  };
}

/** A manager over a queue holding `queued`, each slot's run held until finished. */
function buildManager(queued: Array<number | ReturnType<typeof queueItem>>, maxConcurrent = 3) {
  const waiting = queued.map((q) => (typeof q === "number" ? queueItem(q) : q));
  const queueService = {
    dequeueIndependent: vi.fn(async (n: number) => waiting.splice(0, Math.max(0, n))),
    updateActiveSlots: vi.fn().mockResolvedValue(undefined),
    drainBlockedSuccessors: vi.fn().mockResolvedValue([]),
    enqueue: vi.fn().mockResolvedValue(null),
    complete: vi.fn().mockResolvedValue(undefined),
    clear: vi.fn().mockResolvedValue(undefined),
    getQueue: vi.fn().mockResolvedValue({ items: [], status: "idle" }),
  };
  const finishers = new Map<number, (result: unknown) => void>();
  const factory = vi.fn().mockImplementation((_dir: string, issueNumber: number) => {
    const run = new Promise((resolve) => finishers.set(issueNumber, resolve));
    return {
      orchestrator: {
        setWorktreeOverride: vi.fn(),
        setRunRepoRoot: vi.fn(),
        setUnattended: vi.fn(),
        resolveRunRepoSlug: vi.fn().mockResolvedValue("acme/api"),
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
  });
  const manager = new ConcurrentPipelineManager(
    "/test-repo",
    queueService as any,
    factory,
    { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn(), getChannel: vi.fn() } as any,
    { maxConcurrent }
  );
  manager.setCallbacks({ onSlotCompleted: vi.fn(), onSlotFailed: vi.fn() });
  const running = () =>
    manager
      .getActiveSlots()
      .map((s) => s.issueNumber)
      .sort((a, b) => a - b);
  /** Let `issueNumber`'s run finish, and its slot's whole lifecycle settle. */
  const finish = async (issueNumber: number) => {
    finishers.get(issueNumber)?.(SUCCESS);
    await manager.settleForTest(issueNumber);
  };
  return { manager, running, finish, waiting, queueService };
}

describe("ConcurrentPipelineManager — workspace throttle (#2337)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    fakeCloneLayout("/test-repo");
    worktreeGate.issue = null;
    worktreeGate.reached = false;
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("opens no slot above min(configured, throttle), and a raise fills up to the new cap", async () => {
    const { manager, running, finish } = buildManager([1, 2, 3, 4]);
    manager.setWorkspaceThrottle({ maxConcurrent: 1, resumeAt: null });

    expect(manager.availableSlotCount).toBe(1);
    await manager.fillSlots();
    expect(running()).toEqual([1]);
    expect(manager.availableSlotCount).toBe(0);
    await manager.fillSlots();
    expect(running()).toEqual([1]);

    // Above the configured ceiling, the configured ceiling binds.
    manager.setWorkspaceThrottle({ maxConcurrent: 5, resumeAt: null });
    await vi.waitFor(() => expect(running()).toEqual([1, 2, 3]));
    expect(manager.availableSlotCount).toBe(0);

    for (const n of [1, 2, 3]) await finish(n);
    await vi.waitFor(() => expect(running()).toEqual([4]));
    await finish(4);
    manager.dispose();
  });

  it("stops no running slot when the cap drops below them, and opens none until they are under it", async () => {
    const { manager, running, finish } = buildManager([1, 2, 3, 4]);
    await manager.fillSlots();
    expect(running()).toEqual([1, 2, 3]);

    manager.setWorkspaceThrottle({ maxConcurrent: 1, resumeAt: null });
    expect(running()).toEqual([1, 2, 3]);
    expect(manager.availableSlotCount).toBe(-2);

    await finish(1);
    expect(running()).toEqual([2, 3]);
    await finish(2);
    expect(running()).toEqual([3]);
    await finish(3);
    // Under the cap at last: the next queued issue starts.
    await vi.waitFor(() => expect(running()).toEqual([4]));
    await finish(4);
    manager.dispose();
  });

  it("holds every new slot at a cap of 0, and a clear restores the configured ceiling", async () => {
    const { manager, running, finish } = buildManager([1, 2, 3, 4]);
    manager.setWorkspaceThrottle({ maxConcurrent: 0, resumeAt: null });
    await manager.fillSlots();
    expect(running()).toEqual([]);
    expect(manager.getWorkspaceThrottle()).toEqual({ maxConcurrent: 0, resumeAt: null });

    manager.setWorkspaceThrottle(null);
    expect(manager.getWorkspaceThrottle()).toBeNull();
    await vi.waitFor(() => expect(running()).toEqual([1, 2, 3]));

    for (const n of [1, 2, 3]) await finish(n);
    await vi.waitFor(() => expect(running()).toEqual([4]));
    await finish(4);
    manager.dispose();
  });

  it("lifts the throttle at its resumeAt and fills from the queue", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    vi.setSystemTime(new Date("2026-10-02T12:00:00.000Z"));
    const { manager, running, finish } = buildManager([1, 2]);
    manager.setWorkspaceThrottle({ maxConcurrent: 0, resumeAt: "2026-10-02T12:10:00.000Z" });
    await manager.fillSlots();
    expect(running()).toEqual([]);

    await vi.advanceTimersByTimeAsync(9 * 60_000);
    expect(running()).toEqual([]);
    expect(manager.getWorkspaceThrottle()).not.toBeNull();

    await vi.advanceTimersByTimeAsync(60_000);
    await vi.waitFor(() => expect(running()).toEqual([1, 2]));
    expect(manager.getWorkspaceThrottle()).toBeNull();

    for (const n of [1, 2]) await finish(n);
    manager.dispose();
  });

  it("ignores a throttle whose resumeAt has already passed", async () => {
    const { manager, running, finish } = buildManager([1, 2, 3]);
    manager.setWorkspaceThrottle({ maxConcurrent: 1, resumeAt: "2000-01-01T00:00:00.000Z" });
    expect(manager.getWorkspaceThrottle()).toBeNull();
    expect(manager.availableSlotCount).toBe(3);
    await manager.fillSlots();
    expect(running()).toEqual([1, 2, 3]);
    for (const n of [1, 2, 3]) await finish(n);
    manager.dispose();
  });

  it("drops the lift timer when disposed", () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    vi.setSystemTime(new Date("2026-10-02T12:00:00.000Z"));
    const { manager } = buildManager([]);
    manager.setWorkspaceThrottle({ maxConcurrent: 1, resumeAt: "2026-10-02T13:00:00.000Z" });
    expect(vi.getTimerCount()).toBe(1);
    manager.setWorkspaceThrottle({ maxConcurrent: 1, resumeAt: "2026-10-02T14:00:00.000Z" });
    expect(vi.getTimerCount()).toBe(1);
    manager.dispose();
    expect(vi.getTimerCount()).toBe(0);
  });

  // #2337 review: a throttle that lands while a fill is starting the batch it
  // dequeued under the old ceiling must stop the rest of that batch.
  it("opens no slot above a cap lowered while a fill is starting its batch", async () => {
    const { manager, running, finish, queueService } = buildManager([
      1,
      queueItem(2, {
        repoName: "acme/api",
        remoteRunId: "platform-run-2",
        requestedAdapter: "codex",
        requestedModel: "gpt-5",
      }),
      3,
    ]);
    worktreeGate.issue = 1;
    const fill = manager.fillSlots();
    await vi.waitFor(() => expect(worktreeGate.reached).toBe(true));

    manager.setWorkspaceThrottle({ maxConcurrent: 1, resumeAt: null });
    worktreeGate.release();
    await fill;

    expect(running()).toEqual([1]);
    expect(manager.availableSlotCount).toBe(0);
    // The items the old ceiling admitted go back to the queue, with what they
    // were dequeued with, and their processing marks are cleared.
    expect(queueService.complete).toHaveBeenCalledWith("acme/api", 2);
    expect(queueService.complete).toHaveBeenCalledWith("", 3);
    expect(queueService.enqueue).toHaveBeenCalledWith(2, "Issue #2", undefined, undefined, {
      repoOverride: { owner: "acme", repo: "api" },
      remoteRunId: "platform-run-2",
      requestedAdapter: "codex",
      requestedModel: "gpt-5",
    });
    expect(queueService.enqueue).toHaveBeenCalledWith(3, "Issue #3", undefined, undefined, {});
    // Issue 2 is queued again with its run id, so this window still holds
    // that run, and the slot that opens for it later adopts the id.
    queueService.getQueue.mockResolvedValue({
      items: [queueItem(2, { repoName: "acme/api", remoteRunId: "platform-run-2" })],
      status: "waiting",
    });
    expect(manager.findSlotByRemoteRunId("platform-run-2")).toBeNull();
    expect(await manager.holdsRemoteRun("platform-run-2")).toBe(true);

    await finish(1);
    manager.dispose();
  });

  it("changes nothing, and keeps the lift timer, when the same throttle is applied again", () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    vi.setSystemTime(new Date("2026-10-02T12:00:00.000Z"));
    const { manager } = buildManager([]);
    const onWorkspaceThrottleChanged = vi.fn();
    manager.setCallbacks({ onWorkspaceThrottleChanged });
    const throttle = { maxConcurrent: 1, resumeAt: "2026-10-02T13:00:00.000Z" };

    manager.setWorkspaceThrottle(throttle);
    manager.setWorkspaceThrottle({ ...throttle });
    expect(onWorkspaceThrottleChanged).toHaveBeenCalledTimes(1);
    expect(onWorkspaceThrottleChanged).toHaveBeenLastCalledWith(throttle);
    expect(vi.getTimerCount()).toBe(1);

    manager.setWorkspaceThrottle(null);
    manager.setWorkspaceThrottle(null);
    expect(onWorkspaceThrottleChanged).toHaveBeenCalledTimes(2);
    expect(onWorkspaceThrottleChanged).toHaveBeenLastCalledWith(null);
    expect(vi.getTimerCount()).toBe(0);
    manager.dispose();
  });

  it("reports the lift at resumeAt to the throttle callback", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    vi.setSystemTime(new Date("2026-10-02T12:00:00.000Z"));
    const { manager } = buildManager([]);
    const onWorkspaceThrottleChanged = vi.fn();
    manager.setCallbacks({ onWorkspaceThrottleChanged });
    manager.setWorkspaceThrottle({ maxConcurrent: 0, resumeAt: "2026-10-02T12:10:00.000Z" });

    await vi.advanceTimersByTimeAsync(10 * 60_000);
    expect(onWorkspaceThrottleChanged).toHaveBeenLastCalledWith(null);
    manager.dispose();
  });
});
