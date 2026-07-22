/**
 * #3251: When haltQueueOnSlotFailure pauses Go-side autonomous, it must pass
 * a non-empty reason + structured triggeredBy so the on-disk pause provenance
 * survives investigation. Without this, future stuck-badge incidents force log
 * archeology to figure out who paused autonomous.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

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

vi.mock("../../src/utils/incrediConfig", () => ({
  getConcurrentPipelineConfig: vi
    .fn()
    .mockReturnValue({ maxConcurrent: 2, worktreeBase: ".worktrees" }),
}));

const mockAutonomousPause = vi.fn().mockResolvedValue(undefined);
const mockAutonomousStatus = vi.fn();

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      autonomousStatus: mockAutonomousStatus,
      autonomousPause: mockAutonomousPause,
    }),
  },
}));

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";

interface QueueItem {
  issueNumber: number;
  title: string;
  position: number;
  status: string;
  addedAt: string;
}

function makeQueueItem(issueNumber: number): QueueItem {
  return {
    issueNumber,
    title: `Issue #${issueNumber}`,
    position: 1,
    status: "pending",
    addedAt: new Date().toISOString(),
  };
}

function createControllableFactory() {
  const resolvers = new Map<number, (result: any) => void>();
  const factory = vi.fn().mockImplementation((_workDir: string, issueNumber: number) => {
    const promise = new Promise((resolve) => resolvers.set(issueNumber, resolve));
    return {
      orchestrator: {
        setWorktreeOverride: vi.fn(),
        setUnattended: vi.fn(),
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
        initEmpty: vi.fn(),
        setMeta: vi.fn(),
        dispose: vi.fn(),
      },
    };
  });
  return {
    factory,
    failIssue: (issueNumber: number, failedStage = "feature-dev") =>
      resolvers.get(issueNumber)?.({
        success: false,
        completedStages: ["issue-pickup"],
        skippedStages: [],
        deferredStages: [],
        failedStage,
        totalDurationMs: 10000,
      }),
  };
}

describe("ConcurrentPipelineManager — haltQueueOnSlotFailure pause reason (#3251)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAutonomousStatus.mockResolvedValue({ status: "running" });
  });

  it("passes a structured reason + triggeredBy to autonomousPause when a slot fails", async () => {
    const manager = new ConcurrentPipelineManager(
      "/test-repo",
      {
        dequeueIndependent: vi
          .fn()
          .mockResolvedValueOnce([makeQueueItem(3239)])
          .mockResolvedValue([]),
        updateActiveSlots: vi.fn().mockResolvedValue(undefined),
        drainBlockedSuccessors: vi.fn().mockResolvedValue([]),
        enqueue: vi.fn().mockResolvedValue(null),
        clear: vi.fn().mockResolvedValue(undefined),
        getQueue: vi.fn().mockResolvedValue({ items: [], status: "idle" }),
      } as any,
      ((): any => {
        const c = createControllableFactory();
        // Stash on the manager so the test can fire failure later.
        (globalThis as any).__controllable = c;
        return c.factory;
      })(),
      { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn(), getChannel: vi.fn() } as any,
      { maxConcurrent: 1, worktreeBase: ".worktrees" }
    );

    await manager.fillSlots();
    (globalThis as any).__controllable.failIssue(3239, "pr-merge");
    // Await the real slot-lifecycle completion (incl. haltQueueOnSlotFailure's
    // pause) instead of a fixed sleep that raced the async chain under load (#100).
    await manager.settleForTest(3239);

    expect(mockAutonomousPause).toHaveBeenCalledTimes(1);
    const [reason, triggeredBy] = mockAutonomousPause.mock.calls[0];
    expect(reason).toContain("haltQueueOnSlotFailure");
    expect(reason).toContain("3239");
    expect(reason).toContain("pr-merge");
    expect(triggeredBy).toBe("haltQueueOnSlotFailure");
  });

  it("does not call autonomousPause when Go autonomous is not running", async () => {
    mockAutonomousStatus.mockResolvedValue({ status: "paused" });

    const manager = new ConcurrentPipelineManager(
      "/test-repo",
      {
        dequeueIndependent: vi
          .fn()
          .mockResolvedValueOnce([makeQueueItem(7)])
          .mockResolvedValue([]),
        updateActiveSlots: vi.fn().mockResolvedValue(undefined),
        drainBlockedSuccessors: vi.fn().mockResolvedValue([]),
        enqueue: vi.fn().mockResolvedValue(null),
        clear: vi.fn().mockResolvedValue(undefined),
        getQueue: vi.fn().mockResolvedValue({ items: [], status: "idle" }),
      } as any,
      ((): any => {
        const c = createControllableFactory();
        (globalThis as any).__controllable = c;
        return c.factory;
      })(),
      { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn(), getChannel: vi.fn() } as any,
      { maxConcurrent: 1, worktreeBase: ".worktrees" }
    );

    await manager.fillSlots();
    (globalThis as any).__controllable.failIssue(7);
    await manager.settleForTest(7);

    expect(mockAutonomousPause).not.toHaveBeenCalled();
  });
});
