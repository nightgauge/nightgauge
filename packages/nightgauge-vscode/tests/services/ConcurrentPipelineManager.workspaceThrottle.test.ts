/**
 * #2337: the platform's workspace throttle caps this window's dispatch.
 *
 * While a throttle is in force, dispatch opens no slot above min(configured
 * max_concurrent, throttle.maxConcurrent); a slot already running is never
 * stopped. Clearing the throttle, raising it, or reaching its resumeAt fills
 * slots from the queue, as a finished slot does.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

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

function queueItem(issueNumber: number) {
  return {
    issueNumber,
    title: `Issue #${issueNumber}`,
    position: 1,
    status: "pending",
    addedAt: new Date().toISOString(),
  };
}

/** A manager over a queue holding `queued`, each slot's run held until finished. */
function buildManager(queued: number[], maxConcurrent = 3) {
  const waiting = queued.map(queueItem);
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
  return { manager, running, finish, waiting };
}

describe("ConcurrentPipelineManager — workspace throttle (#2337)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    fakeCloneLayout("/test-repo");
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
});
