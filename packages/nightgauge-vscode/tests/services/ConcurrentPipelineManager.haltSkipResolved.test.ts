/**
 * #3835 / #3840: When a slot reports failure but the issue is already CLOSED on
 * the forge, the work landed (the pipeline closes the issue on merge) and the
 * "failure" is a phantom — a stage that exited non-zero AFTER its work shipped,
 * or a spurious/duplicate failure signal. haltQueueOnSlotFailure must reconcile
 * against the forge FIRST and skip the queue-clear + autonomous-pause in that
 * case, instead of paging the operator on completed work.
 *
 * Fail-safe: an OPEN issue (or any forge query error) still halts — a genuine
 * failure is never masked.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Mutable forge state the mocked `gh issue view` returns.
let mockGhIssueState = "OPEN";

vi.mock("child_process", async (importActual) => {
  const actual = await importActual<typeof import("child_process")>();
  return {
    ...actual,
    // `exec` (promisified to execAsync) — used for git ls-remote etc. Return empty.
    exec: (_cmd: string, optsOrCb: unknown, maybeCb?: unknown) => {
      const cb = (typeof optsOrCb === "function" ? optsOrCb : maybeCb) as (
        err: Error | null,
        out: { stdout: string; stderr: string }
      ) => void;
      cb(null, { stdout: "", stderr: "" });
    },
    // `execFile` (promisified to execFileAsync) — the forge issue-state probe.
    // promisify resolves to the object passed as the first post-error arg.
    execFile: (file: string, args: string[], optsOrCb: unknown, maybeCb?: unknown) => {
      const cb = (typeof optsOrCb === "function" ? optsOrCb : maybeCb) as (
        err: Error | null,
        out: { stdout: string; stderr: string }
      ) => void;
      if (file === "gh" && args.includes("issue") && args.includes("view")) {
        cb(null, { stdout: JSON.stringify({ state: mockGhIssueState }), stderr: "" });
      } else {
        cb(null, { stdout: "", stderr: "" });
      }
    },
  };
});

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

function makeQueueItem(issueNumber: number) {
  return {
    issueNumber,
    title: `Issue #${issueNumber}`,
    position: 1,
    status: "pending",
    addedAt: new Date().toISOString(),
    repoName: "nightgauge/nightgauge",
  };
}

function createControllableFactory() {
  const resolvers = new Map<number, (result: any) => void>();
  const factory = vi.fn().mockImplementation((_workDir: string, issueNumber: number) => {
    const promise = new Promise((resolve) => resolvers.set(issueNumber, resolve));
    return {
      orchestrator: {
        setWorktreeOverride: vi.fn(),
        setRepoOverride: vi.fn(),
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
    failIssue: (issueNumber: number, failedStage: string, errMessage: string) =>
      resolvers.get(issueNumber)?.({
        success: false,
        completedStages: ["issue-pickup"],
        skippedStages: [],
        deferredStages: [],
        failedStage,
        error: new Error(errMessage),
        totalDurationMs: 10000,
      }),
  };
}

function makeManager(issueNumber = 3806) {
  const queueClear = vi.fn().mockResolvedValue(undefined);
  const queueService = {
    dequeueIndependent: vi
      .fn()
      .mockResolvedValueOnce([makeQueueItem(issueNumber)])
      .mockResolvedValue([]),
    updateActiveSlots: vi.fn().mockResolvedValue(undefined),
    drainBlockedSuccessors: vi.fn().mockResolvedValue([]),
    enqueue: vi.fn().mockResolvedValue(null),
    clear: queueClear,
    getQueue: vi.fn().mockResolvedValue({
      items: [makeQueueItem(9999)],
      status: "idle",
    }),
  };
  // Minimal workspaceManager: the issue's repo maps to the primary checkout
  // (path === repoRoot), so the slot reuses the default WorktreeManager.
  const workspaceManager = {
    findRepositoryByGitHub: vi.fn().mockReturnValue({ path: "/test-repo" }),
  };
  const controllable = createControllableFactory();
  const manager = new ConcurrentPipelineManager(
    "/test-repo",
    queueService as any,
    controllable.factory,
    { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn(), getChannel: vi.fn() } as any,
    { maxConcurrent: 1, worktreeBase: ".worktrees" },
    workspaceManager as any
  );
  return { manager, controllable, queueClear };
}

describe("ConcurrentPipelineManager — skip halt when issue resolved on forge (#3835/#3840)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAutonomousStatus.mockResolvedValue({ status: "running" });
    mockGhIssueState = "OPEN";
  });

  it("skips halt when the issue is already CLOSED (phantom failure, work landed)", async () => {
    mockGhIssueState = "CLOSED";
    const { manager, controllable, queueClear } = makeManager(3806);
    await manager.fillSlots();
    // A failure with an empty/unclassifiable message that would normally halt.
    controllable.failIssue(3806, "pr-merge", "");
    await manager.settleForTest(3806);

    expect(mockAutonomousPause).not.toHaveBeenCalled();
    expect(queueClear).not.toHaveBeenCalled();
  });

  it("STILL halts when the issue is OPEN (genuine failure, fail-safe)", async () => {
    mockGhIssueState = "OPEN";
    const { manager, controllable } = makeManager(3806);
    await manager.fillSlots();
    controllable.failIssue(
      3806,
      "feature-validate",
      "Schema validation failed: missing required field 'plan'"
    );
    await manager.settleForTest(3806);

    expect(mockAutonomousPause).toHaveBeenCalledTimes(1);
    const [, triggeredBy] = mockAutonomousPause.mock.calls[0];
    expect(triggeredBy).toBe("haltQueueOnSlotFailure");
  });
});
