/**
 * #3396: User-initiated slot cancellation must NOT be classified as a
 * pipeline failure. Pre-#3396, abortSlot()/abortAll() resulted in the slot's
 * orchestrator returning success=false, and the manager surfaced a
 * "Pipeline failed at <stage>" modal + paused autonomous, even though the
 * user was the one who pressed Stop. The cancellation now sets a per-slot
 * `userCancelled` flag that suppresses the failure-as-such bookkeeping.
 */

import { describe, it, expect, vi, beforeEach, type Mock } from "vitest";

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
  getConcurrentPipelineConfig: vi.fn().mockReturnValue({ maxConcurrent: 2 }),
}));

const mockAutonomousPause = vi.fn().mockResolvedValue(undefined);
const mockAutonomousStatus = vi.fn().mockResolvedValue({ status: "running" });

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

const { mockPipelineCancelForOperatorStop } = vi.hoisted(() => ({
  mockPipelineCancelForOperatorStop: vi.fn().mockResolvedValue({ cancelledIssues: [] }),
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      pipelineCancelForOperatorStop: mockPipelineCancelForOperatorStop,
      gitComposeBranchName,
      autonomousStatus: mockAutonomousStatus,
      autonomousPause: mockAutonomousPause,
    }),
  },
}));

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";
import { fakeCloneLayout } from "../helpers/cloneLayout";

// Per-clone data resolves under the git directory (ADR-024 § 7); map the
// fake roots (and their worktrees) to a clone without running git.
beforeEach(() => {
  fakeCloneLayout("/test-repo");
});

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
  const stops = new Map<number, Mock>();
  const factory = vi.fn().mockImplementation((_workDir: string, issueNumber: number) => {
    const promise = new Promise((resolve) => resolvers.set(issueNumber, resolve));
    const stop = vi.fn();
    stops.set(issueNumber, stop);
    return {
      orchestrator: {
        setWorktreeOverride: vi.fn(),
        setRunRepoRoot: vi.fn(),
        setUnattended: vi.fn(),
        // ADR-017 step 3 (#370): the slot resolves its repo through the
        // orchestrator when the queue item and the workspace manifest cannot
        // name one, BEFORE beginRun installs the identity.
        resolveRunRepoSlug: vi.fn().mockResolvedValue("nightgauge/nightgauge"),
        runPipeline: vi.fn().mockReturnValue(promise),
        stop,
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
    finishWith: (issueNumber: number, payload: any) => resolvers.get(issueNumber)?.(payload),
    stopMockFor: (issueNumber: number) => stops.get(issueNumber),
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

  const logger = {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
    getChannel: vi.fn(),
  };
  const manager = new ConcurrentPipelineManager(
    "/test-repo",
    queueService as any,
    controllable.factory,
    logger as any,
    { maxConcurrent: issueNumbers.length }
  );

  manager.setCallbacks({ onSlotFailed, onSlotCompleted });

  return { manager, queueService, controllable, onSlotFailed, onSlotCompleted, logger };
}

describe("ConcurrentPipelineManager — user-initiated cancellation (#3396)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAutonomousStatus.mockResolvedValue({ status: "running" });
  });

  it("abortSlot routes through the cancellation path: no halt modal, no autonomous pause, surfaced as 'Cancelled by user'", async () => {
    const { manager, controllable, onSlotFailed } = buildManager([885]);

    await manager.fillSlots();
    // User clicks Stop on the running slot.
    expect(manager.abortSlot(885)).toBe(true);
    // The orchestrator's stop() got called — after Go was told it is an
    // operator stop, so the stage's context carries the stop before its
    // process dies (#1848).
    const stopMock = controllable.stopMockFor(885);
    await vi.waitFor(() => expect(stopMock).toHaveBeenCalledTimes(1));
    expect(mockPipelineCancelForOperatorStop).toHaveBeenCalledWith(undefined, 885);
    expect(mockPipelineCancelForOperatorStop.mock.invocationCallOrder[0]).toBeLessThan(
      stopMock!.mock.invocationCallOrder[0]
    );
    // The orchestrator unwinds and reports success=false (typical of a stopped run).
    controllable.finishWith(885, {
      success: false,
      completedStages: [],
      skippedStages: [],
      deferredStages: [],
      failedStage: "feature-planning",
      totalDurationMs: 1234,
    });

    // Await the real slot-lifecycle completion instead of a fixed sleep that
    // intermittently saw 0 failure-path calls under full-suite load (#243).
    await manager.settleForTest(885);

    // onSlotFailed fires (so UI bookkeeping clears the slot), but with the
    // cancellation message — never the misleading "Pipeline failed at X".
    expect(onSlotFailed).toHaveBeenCalledTimes(1);
    const [, , err, , , terminalKind] = onSlotFailed.mock.calls[0];
    expect(err).toBeInstanceOf(Error);
    expect((err as Error).message).toBe("Cancelled by user");
    // #1848: the stop is named structurally, so Go never classifies the text.
    expect(terminalKind).toBe("operator_stop");
    expect((err as Error).message).not.toContain("Pipeline failed");

    // Autonomous must NOT be paused — the user already chose to stop. Pausing
    // on top of that produces the "Autonomous mode paused. Resume from the
    // Autonomous panel after triage." modal that the user is forced to
    // dismiss for their own deliberate action.
    expect(mockAutonomousPause).not.toHaveBeenCalled();
  });

  it("real pipeline failure (not user-cancelled) still surfaces the failure path", async () => {
    const { manager, controllable, onSlotFailed } = buildManager([7000]);

    await manager.fillSlots();
    // No abortSlot — the orchestrator dies on its own.
    controllable.finishWith(7000, {
      success: false,
      completedStages: ["issue-pickup"],
      skippedStages: [],
      deferredStages: [],
      failedStage: "feature-dev",
      totalDurationMs: 10000,
    });

    await manager.settleForTest(7000);

    expect(onSlotFailed).toHaveBeenCalledTimes(1);
    const [, , err, , , terminalKind] = onSlotFailed.mock.calls[0];
    expect((err as Error).message).toContain("Pipeline failed at feature-dev");
    expect(terminalKind).not.toBe("operator_stop");
    expect((err as Error).message).not.toBe("Cancelled by user");

    // Real failure → autonomous IS paused so the user can triage.
    expect(mockAutonomousPause).toHaveBeenCalledTimes(1);
  });
});

describe("ConcurrentPipelineManager — operator stop reaches Go first (#1848)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.useRealTimers();
    mockAutonomousStatus.mockResolvedValue({ status: "running" });
    mockPipelineCancelForOperatorStop.mockResolvedValue({ cancelledIssues: [] });
  });

  it("still stops the slot when Go rejects the notice, and logs it", async () => {
    const { manager, controllable, logger } = buildManager([11]);
    await manager.fillSlots();
    mockPipelineCancelForOperatorStop.mockRejectedValueOnce(new Error("ipc down"));

    expect(manager.abortSlot(11)).toBe(true);
    await vi.waitFor(() => expect(controllable.stopMockFor(11)).toHaveBeenCalledTimes(1));
    expect(logger.warn).toHaveBeenCalledWith(
      "Operator stop was not delivered to the Go scheduler",
      expect.objectContaining({ issueNumber: 11, error: "ipc down" })
    );
  });

  it("still stops the slot when the IPC client throws synchronously", async () => {
    const { manager, controllable, logger } = buildManager([12]);
    await manager.fillSlots();
    mockPipelineCancelForOperatorStop.mockImplementationOnce(() => {
      throw new Error("not connected");
    });

    expect(manager.abortSlot(12)).toBe(true);
    await vi.waitFor(() => expect(controllable.stopMockFor(12)).toHaveBeenCalledTimes(1));
    expect(logger.warn).toHaveBeenCalledWith(
      "Operator stop was not delivered to the Go scheduler",
      expect.objectContaining({ issueNumber: 12, error: "not connected" })
    );
  });

  it("does not wait on Go past the bound when the notice never settles", async () => {
    const { manager, controllable } = buildManager([13]);
    await manager.fillSlots();
    vi.useFakeTimers();
    try {
      mockPipelineCancelForOperatorStop.mockReturnValueOnce(new Promise(() => {}));
      expect(manager.abortSlot(13)).toBe(true);
      await vi.advanceTimersByTimeAsync(999);
      expect(controllable.stopMockFor(13)).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(2);
      expect(controllable.stopMockFor(13)).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("Stop All tells Go about every run before any slot is stopped", async () => {
    const { manager, controllable } = buildManager([21, 22]);
    await manager.fillSlots();

    const aborting = manager.abortAll();
    await vi.waitFor(() => {
      expect(controllable.stopMockFor(21)).toHaveBeenCalledTimes(1);
      expect(controllable.stopMockFor(22)).toHaveBeenCalledTimes(1);
    });

    expect(mockPipelineCancelForOperatorStop).toHaveBeenCalledWith(undefined, undefined);
    const notified = mockPipelineCancelForOperatorStop.mock.invocationCallOrder[0];
    for (const n of [21, 22]) {
      expect(notified).toBeLessThan(controllable.stopMockFor(n)!.mock.invocationCallOrder[0]);
      controllable.finishWith(n, {
        success: false,
        completedStages: [],
        skippedStages: [],
        deferredStages: [],
        failedStage: "feature-dev",
        totalDurationMs: 10,
      });
    }
    await aborting;
  });
});
