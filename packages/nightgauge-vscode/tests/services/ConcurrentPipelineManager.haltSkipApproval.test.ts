/**
 * Architecture-approval pause must NOT halt the autonomous queue (#4222),
 * and the human gets a one-click GUI approval affordance.
 *
 * The approval gate halts a pipeline BEFORE feature-dev when a high-impact
 * decision needs human sign-off. That is a deliberate, per-issue pause — the
 * gate's own message says "This is NOT a failure" — yet the slot resolution
 * carried it through the generic failure path, tripping haltQueueOnSlotFailure:
 * queue cleared, autonomous paused, operator paged (observed in production).
 * These tests pin the fix: the ARCHITECTURE_APPROVAL_REQUIRED marker skips the
 * halt (no pause, no queue clear) and shows an actionable notification whose
 * "Approve & Re-queue" button adds the approval label, moves the board item to
 * Ready, and re-enqueues the issue. A genuine failure still halts (fail-safe).
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Records every execFile invocation so tests can assert the gh approval calls.
const execFileCalls = vi.hoisted(() => [] as Array<{ file: string; args: string[] }>);

vi.mock("child_process", async (importActual) => {
  const actual = await importActual<typeof import("child_process")>();
  return {
    ...actual,
    exec: (_cmd: string, optsOrCb: unknown, maybeCb?: unknown) => {
      const cb = (typeof optsOrCb === "function" ? optsOrCb : maybeCb) as (
        err: Error | null,
        out: { stdout: string; stderr: string }
      ) => void;
      cb(null, { stdout: "", stderr: "" });
    },
    // Forge issue-state probe: always OPEN — the approval skip must fire on
    // the marker alone, BEFORE the forge reconcile (an approval-halted issue
    // is open by definition, so the reconcile would never suppress it).
    execFile: (file: string, args: string[], optsOrCb: unknown, maybeCb?: unknown) => {
      execFileCalls.push({ file, args });
      const cb = (typeof optsOrCb === "function" ? optsOrCb : maybeCb) as (
        err: Error | null,
        out: { stdout: string; stderr: string }
      ) => void;
      if (file === "gh" && args.includes("issue") && args.includes("view")) {
        cb(null, { stdout: JSON.stringify({ state: "OPEN" }), stderr: "" });
      } else {
        cb(null, { stdout: "", stderr: "" });
      }
    },
  };
});

// Board-status writes are asserted, not executed.
const mockUpdateProjectItemStatus = vi.hoisted(() => vi.fn().mockResolvedValue({ success: true }));
vi.mock("../../src/utils/projectFieldWriter", () => ({
  updateProjectItemStatus: mockUpdateProjectItemStatus,
}));

const mockShowWarningMessage = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));

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
    showWarningMessage: mockShowWarningMessage,
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
import { ARCHITECTURE_APPROVAL_REQUIRED_MARKER } from "../../src/utils/failureComment";

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
        completedStages: ["issue-pickup", "feature-planning"],
        skippedStages: [],
        deferredStages: [],
        failedStage,
        error: new Error(errMessage),
        totalDurationMs: 10000,
      }),
  };
}

function makeManager(issueNumber = 178) {
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
  return { manager, controllable, queueClear, queueService };
}

/** The real gate message shape from HeadlessOrchestrator.verifyArchitectureApproval. */
const APPROVAL_MESSAGE =
  `${ARCHITECTURE_APPROVAL_REQUIRED_MARKER} — issue #178 is a high-impact decision that ` +
  `must be human-approved before feature-dev implements it. This is NOT a failure and NO ` +
  `development or validation cost was incurred — the pipeline halted before implementation.`;

describe("ConcurrentPipelineManager — approval pause must not halt the queue (#4222)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    execFileCalls.length = 0;
    mockAutonomousStatus.mockResolvedValue({ status: "running" });
    mockUpdateProjectItemStatus.mockResolvedValue({ success: true });
  });

  it("skips halt on the marker and shows the actionable approval notification", async () => {
    const { manager, controllable, queueClear } = makeManager(178);
    await manager.fillSlots();
    controllable.failIssue(178, "feature-dev", APPROVAL_MESSAGE);
    // showWarningMessage is invoked synchronously inside haltQueueOnSlotFailure's
    // approval-skip branch, so settling the lifecycle guarantees it fired and
    // that neither pause nor clear was called.
    await manager.settleForTest(178);

    expect(mockAutonomousPause).not.toHaveBeenCalled();
    expect(queueClear).not.toHaveBeenCalled();
    // The notification must carry the one-click actions, not just prose.
    expect(mockShowWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("needs architecture approval"),
      "Approve & Re-queue",
      "Open Issue"
    );
  });

  it("'Approve & Re-queue' adds the label, moves the board to Ready, and re-enqueues", async () => {
    mockShowWarningMessage.mockResolvedValueOnce("Approve & Re-queue");
    const { manager, controllable, queueService } = makeManager(178);
    await manager.fillSlots();
    controllable.failIssue(178, "feature-dev", APPROVAL_MESSAGE);
    // The approval actions run in offerArchitectureApproval — fire-and-forget,
    // after the lifecycle resolves — so poll for its terminal effect (re-enqueue)
    // rather than guessing a fixed sleep. Label + board move precede it.
    await vi.waitFor(() =>
      expect(queueService.enqueue).toHaveBeenCalledWith(
        178,
        expect.any(String),
        undefined,
        undefined,
        expect.objectContaining({ repoOverride: expect.anything() })
      )
    );

    // gh applied the approval label (the deterministic gate's evidence).
    const labelEdit = execFileCalls.find(
      (c) =>
        c.file === "gh" &&
        c.args.includes("edit") &&
        c.args.includes("--add-label") &&
        c.args.includes("approved:architecture")
    );
    expect(labelEdit).toBeDefined();
    expect(labelEdit!.args).toContain("178");
    // Board back to Ready so the autonomous scheduler can redispatch.
    expect(mockUpdateProjectItemStatus).toHaveBeenCalledWith(
      178,
      "Ready",
      expect.any(String),
      expect.anything()
    );
    // Local queue re-entry.
    expect(queueService.enqueue).toHaveBeenCalledWith(
      178,
      expect.any(String),
      undefined,
      undefined,
      expect.objectContaining({ repoOverride: expect.anything() })
    );
    // Still no queue halt.
    expect(mockAutonomousPause).not.toHaveBeenCalled();
  });

  it("dismissing the notification approves nothing and re-queues nothing", async () => {
    mockShowWarningMessage.mockResolvedValueOnce(undefined);
    const { manager, controllable, queueService } = makeManager(178);
    await manager.fillSlots();
    controllable.failIssue(178, "feature-dev", APPROVAL_MESSAGE);
    // Dismissal (showWarningMessage → undefined) can never reach the approve
    // branch, so once the lifecycle settles neither label nor enqueue can fire.
    await manager.settleForTest(178);

    const labelEdit = execFileCalls.find((c) => c.file === "gh" && c.args.includes("--add-label"));
    expect(labelEdit).toBeUndefined();
    expect(queueService.enqueue).not.toHaveBeenCalled();
  });

  it("STILL halts on a genuine feature-dev failure (fail-safe preserved)", async () => {
    const { manager, controllable } = makeManager(178);
    await manager.fillSlots();
    controllable.failIssue(178, "feature-dev", "Schema validation failed: missing field 'plan'");
    await manager.settleForTest(178);

    expect(mockAutonomousPause).toHaveBeenCalledTimes(1);
    const [, triggeredBy] = mockAutonomousPause.mock.calls[0];
    expect(triggeredBy).toBe("haltQueueOnSlotFailure");
  });
});
