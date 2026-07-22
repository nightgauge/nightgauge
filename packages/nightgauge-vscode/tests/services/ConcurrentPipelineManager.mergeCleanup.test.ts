/**
 * #3969: After a SUCCESSFUL pipeline (PR merged), the slot's worktree is
 * removed AND the local feature branch is deleted — merged branches must not
 * accumulate (142 stale locals piled up across AcmeApp). On FAILURE the
 * branch is preserved (deleteBranch=false) so a re-queue can resume/recover;
 * on a pr-merge failure the whole worktree is preserved.
 *
 * The assertion is on the second arg of WorktreeManager.cleanup(issueNumber,
 * deleteBranch).
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

const mockWorktreeCleanup = vi.fn().mockResolvedValue(undefined);

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
      // Shared spy across all instances so the test can assert the deleteBranch arg.
      cleanup: mockWorktreeCleanup,
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
    resolve: (issueNumber: number, result: any) => resolvers.get(issueNumber)?.(result),
  };
}

function makeManager(issueNumber: number) {
  const queueService = {
    dequeueIndependent: vi
      .fn()
      .mockResolvedValueOnce([makeQueueItem(issueNumber)])
      .mockResolvedValue([]),
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
    { maxConcurrent: 1, worktreeBase: ".worktrees" }
  );
  return { manager, controllable };
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

describe("ConcurrentPipelineManager — merge cleanup deletes branch on success (#3969)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockWorktreeCleanup.mockResolvedValue(undefined);
  });

  it("deletes the local branch (deleteBranch=true) on a successful merge", async () => {
    const { manager, controllable } = makeManager(42);
    await manager.fillSlots();
    controllable.resolve(42, SUCCESS);
    await manager.settleForTest(42);

    expect(mockWorktreeCleanup).toHaveBeenCalledWith(42, true);
  });

  it("preserves the branch (deleteBranch=false) on a non-preserving failure", async () => {
    const { manager, controllable } = makeManager(43);
    await manager.fillSlots();
    controllable.resolve(43, {
      success: false,
      completedStages: ["issue-pickup"],
      skippedStages: [],
      deferredStages: [],
      failedStage: "feature-dev",
      error: new Error("boom"),
      totalDurationMs: 10000,
    });
    await manager.settleForTest(43);

    // Worktree still removed, but the branch is kept for resume/recovery.
    expect(mockWorktreeCleanup).toHaveBeenCalledWith(43, false);
  });

  it("preserves the whole worktree (no cleanup) when pr-merge fails", async () => {
    const { manager, controllable } = makeManager(44);
    await manager.fillSlots();
    controllable.resolve(44, {
      success: false,
      completedStages: [
        "issue-pickup",
        "feature-planning",
        "feature-dev",
        "feature-validate",
        "pr-create",
      ],
      skippedStages: [],
      deferredStages: [],
      failedStage: "pr-merge",
      error: new Error("merge failed"),
      totalDurationMs: 20000,
    });
    await manager.settleForTest(44);

    expect(mockWorktreeCleanup).not.toHaveBeenCalled();
  });
});
