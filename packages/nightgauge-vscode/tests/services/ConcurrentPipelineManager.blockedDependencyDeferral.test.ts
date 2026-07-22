/**
 * #305: A blockedBy deferral (issue dispatched while its native `blockedBy`
 * dependencies are still open) must NOT be classified as a pipeline failure.
 * The slot orchestrator returns `{ success: false, deferred: true }`; the
 * manager must route it to `onSlotDeferred` — NOT `onSlotFailed` — and must
 * never pause autonomous or halt the queue. The issue stays eligible for a
 * later tick.
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
const mockAutonomousStatus = vi.fn().mockResolvedValue({ status: "running" });

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
    finishWith: (issueNumber: number, payload: any) => resolvers.get(issueNumber)?.(payload),
  };
}

function buildManager(issueNumbers: number[]) {
  const queueService = {
    dequeueIndependent: vi
      .fn()
      .mockResolvedValueOnce(issueNumbers.map(makeQueueItem))
      .mockResolvedValue([]),
    updateActiveSlots: vi.fn().mockResolvedValue(undefined),
    drainBlockedSuccessors: vi.fn().mockResolvedValue([]),
    enqueue: vi.fn().mockResolvedValue(null),
    clear: vi.fn().mockResolvedValue(undefined),
    getQueue: vi.fn().mockResolvedValue({ items: [], status: "idle" }),
  };

  const controllable = createControllableFactory();
  const onSlotFailed = vi.fn();
  const onSlotCompleted = vi.fn();
  const onSlotDeferred = vi.fn();

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
    { maxConcurrent: issueNumbers.length, worktreeBase: ".worktrees" }
  );

  manager.setCallbacks({ onSlotFailed, onSlotCompleted, onSlotDeferred });

  return { manager, queueService, controllable, onSlotFailed, onSlotCompleted, onSlotDeferred };
}

describe("ConcurrentPipelineManager — blockedBy deferral (#305)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAutonomousStatus.mockResolvedValue({ status: "running" });
  });

  it("routes a deferred result to onSlotDeferred, never onSlotFailed, and does not pause autonomous", async () => {
    const { manager, controllable, onSlotFailed, onSlotDeferred, queueService } = buildManager([
      304,
    ]);

    await manager.fillSlots();
    // The orchestrator's #189 fail-closed guard defers: success=false but
    // deferred=true, with NO failedStage / error.
    controllable.finishWith(304, {
      success: false,
      deferred: true,
      completedStages: [],
      skippedStages: [],
      deferredStages: ["issue-pickup"],
      outcomeType: "deferred",
      totalDurationMs: 3400,
    });

    await manager.settleForTest(304);

    // Routed to the deferral callback — NOT the failure callback.
    expect(onSlotDeferred).toHaveBeenCalledTimes(1);
    expect(onSlotFailed).not.toHaveBeenCalled();

    // No autonomous pause and no successor drain — a deferral is not a failure.
    expect(mockAutonomousPause).not.toHaveBeenCalled();
    expect(queueService.drainBlockedSuccessors).not.toHaveBeenCalled();

    // The deferral callback receives the deferred result.
    const [, issueNumber, result] = onSlotDeferred.mock.calls[0];
    expect(issueNumber).toBe(304);
    expect(result.deferred).toBe(true);
    expect(result.failedStage).toBeUndefined();
  });
});
