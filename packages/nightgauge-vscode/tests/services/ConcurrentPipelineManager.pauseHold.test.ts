/**
 * #423: pausing a concurrent-slot run must not terminate it.
 *
 * Before this fix, HeadlessOrchestrator's stage loop BROKE out on
 * `isPaused()`, returning `{success:false, failedStage:undefined}` from the
 * slot's `runPipeline()` call while the run was merely paused. That result
 * has no "paused" arm in ConcurrentPipelineManager.processSlot's terminal
 * classification, so it fell into the generic failure arm: `onSlotFailed`
 * fired, `haltQueueOnSlotFailure` paused autonomous, and `cleanupSlot`
 * deleted the slot and disposed its state service — after which Resume could
 * never target the run again.
 *
 * The fix makes the stage loop HOLD at the pause boundary instead of
 * breaking out of it: `isPaused()` is polled and the loop only continues
 * once it flips back to false. Concretely for ConcurrentPipelineManager, this
 * means the slot's own `orchestrator.runPipeline()` call simply does not
 * resolve while paused — so nothing here needs to know about "paused" as a
 * distinct terminal outcome. These tests pin exactly that consequence: while
 * a slot is paused (represented by its mocked runPipeline() call staying
 * pending), the slot remains in `getActiveSlots()` and `onSlotFailed` is
 * never called; once resumed (the promise finally resolves), the slot
 * completes through the normal success path.
 *
 * @see src/services/HeadlessOrchestrator.ts — the PAUSE_POLL_INTERVAL_MS hold
 * @see src/commands/resumePipeline.ts — the runIsHeld branch that relies on
 *      this same in-flight-promise invariant to avoid a duplicate dispatch
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
  getConcurrentPipelineConfig: vi
    .fn()
    .mockReturnValue({ maxConcurrent: 2, worktreeBase: ".worktrees" }),
}));

const mockAutonomousPause = vi.fn().mockResolvedValue(undefined);
const mockAutonomousStatus = vi.fn().mockResolvedValue({ status: "running" });

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
  const stops = new Map<number, Mock>();
  // #423: the per-slot PipelineStateService this fixture hands back is the
  // SAME object `getSlotStateService` will resolve to later — a stand-in for
  // the invariant runSelector.ts relies on (resolving a slot to its own
  // stateService instance).
  const stateServices = new Map<number, any>();
  const factory = vi.fn().mockImplementation((_workDir: string, issueNumber: number) => {
    const promise = new Promise((resolve) => resolvers.set(issueNumber, resolve));
    const stop = vi.fn();
    stops.set(issueNumber, stop);
    const stateService = {
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
    };
    stateServices.set(issueNumber, stateService);
    return {
      orchestrator: {
        setWorktreeOverride: vi.fn(),
        setRunRepoRoot: vi.fn(),
        setUnattended: vi.fn(),
        resolveRunRepoSlug: vi.fn().mockResolvedValue("nightgauge/nightgauge"),
        runPipeline: vi.fn().mockReturnValue(promise),
        stop,
        dispose: vi.fn(),
      },
      stateService,
    };
  });
  return {
    factory,
    finishWith: (issueNumber: number, payload: any) => resolvers.get(issueNumber)?.(payload),
    stopMockFor: (issueNumber: number) => stops.get(issueNumber),
    stateServiceFor: (issueNumber: number) => stateServices.get(issueNumber),
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

  manager.setCallbacks({ onSlotFailed, onSlotCompleted });

  return { manager, queueService, controllable, onSlotFailed, onSlotCompleted };
}

describe("ConcurrentPipelineManager — pause holds the slot instead of ending it (#423)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAutonomousStatus.mockResolvedValue({ status: "running" });
  });

  it("a paused slot stays in getActiveSlots(), reachable by its own stateService, with no failure booked", async () => {
    const { manager, controllable, onSlotFailed } = buildManager([423]);

    await manager.fillSlots();

    // Simulated pause: under the hold design this is exactly what happens to
    // the slot's own runPipeline() promise while HeadlessOrchestrator's
    // stage loop is polling isPaused() — it does not resolve.
    expect(manager.getActiveSlots().map((s) => s.issueNumber)).toEqual([423]);

    // A runSelector-style lookup must still resolve this slot and its state
    // service — the exact capability the paused run must stay reachable for.
    const active = manager.getActiveSlots();
    expect(active).toHaveLength(1);
    const slotService = manager.getSlotStateService(active[0].slotIndex);
    expect(slotService).toBe(controllable.stateServiceFor(423));
    expect(slotService?.getRunId()).toBe("run-423");

    // No terminal outcome has fired — the run isn't done, it's held.
    expect(onSlotFailed).not.toHaveBeenCalled();
    expect(mockAutonomousPause).not.toHaveBeenCalled();

    // Resume: the held call finally resolves (as it would once
    // HeadlessOrchestrator's poll loop observes isPaused() go false and the
    // stage loop runs the pipeline to completion).
    controllable.finishWith(423, {
      success: true,
      completedStages: [
        "pipeline-start",
        "issue-pickup",
        "feature-planning",
        "feature-dev",
        "feature-validate",
        "pr-create",
        "pr-merge",
        "pipeline-finish",
      ],
      skippedStages: [],
      deferredStages: [],
      totalDurationMs: 5000,
    });

    await manager.settleForTest(423);

    expect(onSlotFailed).not.toHaveBeenCalled();
    // The slot is cleaned up on its REAL completion, not on the pause.
    expect(manager.getActiveSlots()).toHaveLength(0);
  });
});
