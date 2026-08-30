/**
 * #1170: a run REFUSED AT `pipeline-start` — zero tokens, no AI stage — must
 * not provoke either of the two durable reactions the manager fires on a
 * pipeline failure:
 *
 *   1. the "safety net" board move to "In review", which claims there is work
 *      for a human to review when the run produced none, and
 *   2. the generic failure comment, which writes an operator-local environment
 *      lapse (a logged-out `claude` CLI) into the permanent public history of
 *      an issue that has nothing wrong with it.
 *
 * The incident: five issues across five repositories were dispatched with the
 * adapter CLI logged out, refused by the auth pre-flight for zero tokens, and
 * each was moved to "In review" AND commented on.
 *
 * The second test here matters as much as the first: this narrows existing
 * behaviour, it does not remove it. A run that genuinely STARTED and then
 * failed still moves the board and still comments.
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

vi.mock("../../src/utils/nightgaugeConfig", () => ({
  getConcurrentPipelineConfig: vi
    .fn()
    .mockReturnValue({ maxConcurrent: 2, worktreeBase: ".worktrees" }),
}));

/** The two durable reactions under test. */
const updateProjectItemStatus = vi.fn().mockResolvedValue({ success: true });
vi.mock("../../src/utils/projectFieldWriter", () => ({
  updateProjectItemStatus: (...args: unknown[]) => updateProjectItemStatus(...args),
}));

const postFailureComment = vi.fn().mockResolvedValue(undefined);
vi.mock("../../src/utils/failureComment", () => ({
  postFailureComment: (...args: unknown[]) => postFailureComment(...args),
  ARCHITECTURE_APPROVAL_REQUIRED_MARKER: "ARCHITECTURE APPROVAL REQUIRED",
  BLOCKED_DEPENDENCY_MARKER: "[blocked-dependency]",
}));

const mockAutonomousPause = vi.fn().mockResolvedValue(undefined);
const mockAutonomousStatus = vi.fn().mockResolvedValue({ status: "paused" });

const gitComposeBranchName = vi.fn(async (issueNumber: number, title: string) => ({
  name: `feat/${issueNumber}-${title
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "")
    .substring(0, 50)}`,
}));

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

function makeQueueItem(issueNumber: number) {
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
        setRunRepoRoot: vi.fn(),
        setUnattended: vi.fn(),
        resolveRunRepoSlug: vi.fn().mockResolvedValue("nightgauge/nightgauge"),
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

  manager.setCallbacks({ onSlotFailed });

  return { manager, queueService, controllable, onSlotFailed };
}

describe("ConcurrentPipelineManager — zero-token pipeline-start refusal (#1170)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    updateProjectItemStatus.mockResolvedValue({ success: true });
    postFailureComment.mockResolvedValue(undefined);
    mockAutonomousStatus.mockResolvedValue({ status: "paused" });
  });

  it("does not move the board and does not comment when the adapter auth pre-flight refused the run", async () => {
    const { manager, controllable, onSlotFailed } = buildManager([1170]);

    await manager.fillSlots();
    // Exactly the shape HeadlessOrchestrator's adapter auth gate returns: a
    // failure at pipeline-start with zero tokens and the typed refusal.
    controllable.finishWith(1170, {
      success: false,
      completedStages: [],
      skippedStages: [],
      deferredStages: [],
      failedStage: "pipeline-start",
      startRefusal: "adapter-auth-failed",
      error: new Error(
        "[adapter-auth-failed] Auth pre-flight failed — adapter not authenticated. " +
          "Pipeline halted before AI stages (zero tokens spent)."
      ),
      totalDurationMs: 900,
    });

    await manager.settleForTest(1170);

    // The two reactions the incident produced — neither may fire.
    expect(updateProjectItemStatus).not.toHaveBeenCalled();
    expect(postFailureComment).not.toHaveBeenCalled();

    // It is still a failure for accounting purposes — the run did not succeed.
    // Suppressing the board move and the comment must not swallow the outcome.
    expect(onSlotFailed).toHaveBeenCalledTimes(1);
  });

  it.each([
    ["github-auth-failed", "GitHub auth pre-check failed — pipeline halted before AI stages."],
    ["github-quota-low", "[github-quota-low] GitHub API quota too low"],
    ["github-network-outage", "[github-network-outage] GitHub API unreachable"],
    ["issue-closed", "Issue #1170 is already CLOSED. Pipeline halted — zero AI tokens consumed."],
    ["epic-with-open-sub-issues", "Issue #1170 is an epic (type:epic). Pipeline halted"],
    ["budget-cancelled-by-user", "Pipeline cancelled by user after pre-flight budget warning."],
  ])(
    "suppresses both reactions for the %s refusal too — the class, not one instance",
    async (refusal, message) => {
      const { manager, controllable } = buildManager([1170]);

      await manager.fillSlots();
      controllable.finishWith(1170, {
        success: false,
        completedStages: [],
        skippedStages: [],
        deferredStages: [],
        failedStage: "pipeline-start",
        startRefusal: refusal,
        error: new Error(message),
        totalDurationMs: 900,
      });

      await manager.settleForTest(1170);

      expect(updateProjectItemStatus).not.toHaveBeenCalled();
      expect(postFailureComment).not.toHaveBeenCalled();
    }
  );

  it("STILL moves the board to In review and STILL comments when a run that genuinely started fails", async () => {
    const { manager, controllable, onSlotFailed } = buildManager([1171]);

    await manager.fillSlots();
    // A real run: issue-pickup and planning completed, feature-dev crashed.
    // No `startRefusal` — the run was never refused, it was attempted.
    controllable.finishWith(1171, {
      success: false,
      completedStages: ["issue-pickup", "feature-planning"],
      skippedStages: [],
      deferredStages: [],
      failedStage: "feature-dev",
      error: new Error("exit 1: feature-dev subagent crashed"),
      totalDurationMs: 420_000,
    });

    await manager.settleForTest(1171);

    expect(onSlotFailed).toHaveBeenCalledTimes(1);

    // The safety net fires, with the same status it always used.
    expect(updateProjectItemStatus).toHaveBeenCalledTimes(1);
    const [issueNumber, status] = updateProjectItemStatus.mock.calls[0];
    expect(issueNumber).toBe(1171);
    expect(status).toBe("In review");

    // And the diagnostic comment is posted.
    expect(postFailureComment).toHaveBeenCalledTimes(1);
    expect(postFailureComment.mock.calls[0][0]).toMatchObject({ issueNumber: 1171 });
  });
});
