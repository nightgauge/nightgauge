/**
 * #2770: a runaway_progress kill must not halt the repository, and an issue
 * the runaway monitor keeps killing must not retry forever: the halt is
 * skipped for the first RUNAWAY_CONSECUTIVE_RETRY_CAP - 1 consecutive runaway
 * kills of an issue and applies on the CAP-th. Any other outcome resets.
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

vi.mock("../../src/utils/nightgaugeConfig", () => ({
  getConcurrentPipelineConfig: vi.fn().mockReturnValue({ maxConcurrent: 2 }),
}));

const mockAutonomousPause = vi.fn().mockResolvedValue(undefined);
// #1148: the halt is repo-scoped now. A skip test that watched only the
// fleet-wide verb would keep passing while the skip quietly stopped applying
// to the verb the halt actually uses.
const mockAutonomousPauseRepo = vi.fn().mockResolvedValue(undefined);
const mockAutonomousStatus = vi.fn();

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
      autonomousPauseRepo: mockAutonomousPauseRepo,
    }),
  },
}));

import {
  ConcurrentPipelineManager,
  RUNAWAY_CONSECUTIVE_RETRY_CAP,
  nextRunawayStreak,
  runawayKillSkipsHalt,
} from "../../src/services/ConcurrentPipelineManager";
import { fakeCloneLayout } from "../helpers/cloneLayout";

// Per-clone data resolves under the git directory (ADR-024 § 7); map the
// fake roots (and their worktrees) to a clone without running git.
beforeEach(() => {
  fakeCloneLayout("/test-repo");
});

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
        setRunRepoRoot: vi.fn(),
        setUnattended: vi.fn(),
        // ADR-017 step 3 (#370): the slot resolves its repo through the
        // orchestrator when the queue item and the workspace manifest cannot
        // name one, BEFORE beginRun installs the identity.
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
    dequeueIndependent: vi.fn().mockResolvedValue([]),
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
    { maxConcurrent: 1 },
    workspaceManager as any
  );
  /** Dispatch the issue once more and end that run with errMessage. */
  const runAndFail = async (errMessage: string) => {
    queueService.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(issueNumber)]);
    await manager.fillSlots();
    controllable.failIssue(issueNumber, "feature-validate", errMessage);
    await manager.settleForTest(issueNumber);
  };
  return { manager, controllable, queueClear, queueService, runAndFail };
}

const RUNAWAY = "[runaway-progress-exceeded] feature-validate made no progress for 45m";
const DEFECT = "Schema validation failed: missing field 'plan'";

describe("runaway streak policy", () => {
  it("counts consecutive runaway kills and resets on anything else", () => {
    expect(nextRunawayStreak(0, "runaway_progress")).toBe(1);
    expect(nextRunawayStreak(2, "runaway_progress")).toBe(3);
    expect(nextRunawayStreak(2, "stall_kill")).toBe(0);
    expect(nextRunawayStreak(2, undefined)).toBe(0);
  });

  it("skips the halt below the cap only", () => {
    expect(RUNAWAY_CONSECUTIVE_RETRY_CAP).toBe(3);
    expect(runawayKillSkipsHalt(1)).toBe(true);
    expect(runawayKillSkipsHalt(2)).toBe(true);
    expect(runawayKillSkipsHalt(3)).toBe(false);
    expect(runawayKillSkipsHalt(0)).toBe(false);
  });
});

describe("ConcurrentPipelineManager — runaway kills (#2770)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    execFileCalls.length = 0;
    mockAutonomousStatus.mockResolvedValue({ status: "running" });
  });

  it("skips the halt for the 1st and 2nd consecutive runaway kill and halts on the 3rd", async () => {
    const { runAndFail, queueClear } = makeManager(178);
    await runAndFail(RUNAWAY);
    await runAndFail(RUNAWAY);
    expect(mockAutonomousPauseRepo).not.toHaveBeenCalled();
    expect(queueClear).not.toHaveBeenCalled();
    expect(mockShowWarningMessage).toHaveBeenCalledTimes(2);

    await runAndFail(RUNAWAY);
    expect(mockAutonomousPauseRepo).toHaveBeenCalledTimes(1);
    expect(mockAutonomousPause).not.toHaveBeenCalled();
  });

  it("a different outcome between runaway kills resets the count", async () => {
    const { runAndFail } = makeManager(178);
    await runAndFail(RUNAWAY);
    await runAndFail(RUNAWAY);
    await runAndFail(DEFECT); // halts on its own, and resets the streak
    expect(mockAutonomousPauseRepo).toHaveBeenCalledTimes(1);
    mockAutonomousPauseRepo.mockClear();

    await runAndFail(RUNAWAY);
    await runAndFail(RUNAWAY);
    expect(mockAutonomousPauseRepo).not.toHaveBeenCalled();
  });
});
