/**
 * #3444: When a pipeline slot fails due to an ENVIRONMENTAL terminal kind
 * (rate-limit-quota-exhausted, stream idle timeout, network unavailable),
 * haltQueueOnSlotFailure must skip the queue-clear + autonomous-pause + modal.
 *
 * The autonomous scheduler already auto-recovers from these via per-issue
 * backoff + the global quota cooldown set in onPipelineComplete. Halting the
 * queue forces the user to manually click Resume after the cooldown expires
 * (~4h for a quota miss), which defeats the purpose of the environmental
 * classification path.
 *
 * Real failures (validation_error, subagent_crash, stall_kill, gate failures)
 * MUST still trigger haltQueueOnSlotFailure — that is its purpose.
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
    showErrorMessage: (...args: any[]) => mockShowErrorMessage(...args),
    showWarningMessage: (...args: any[]) => mockShowWarningMessage(...args),
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
const mockShowWarningMessage = vi.fn().mockResolvedValue(undefined);
const mockShowErrorMessage = vi.fn().mockResolvedValue(undefined);

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

function makeManager(queueClear = vi.fn().mockResolvedValue(undefined), issueNumber = 3375) {
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
      items: [makeQueueItem(9999), makeQueueItem(9998)],
      status: "idle",
    }),
  };
  const controllable = createControllableFactory();
  const manager = new ConcurrentPipelineManager(
    "/test-repo",
    queueService as any,
    controllable.factory,
    { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn(), getChannel: vi.fn() } as any,
    { maxConcurrent: 1, worktreeBase: ".worktrees" }
  );
  return { manager, controllable, queueService, queueClear };
}

describe("ConcurrentPipelineManager — skip haltQueueOnSlotFailure on environmental kinds (#3444)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAutonomousStatus.mockResolvedValue({ status: "running" });
  });

  it("skips halt for rate-limit-quota-exhausted failure", async () => {
    const { manager, controllable, queueClear } = makeManager();
    await manager.fillSlots();
    controllable.failIssue(
      3375,
      "feature-dev",
      "[stall-killed] feature-dev terminated.\nUpstream signal: [rate-limit-quota-exhausted] resetsAt=1715399460"
    );
    await manager.settleForTest(3375);

    expect(mockAutonomousPause).not.toHaveBeenCalled();
    expect(queueClear).not.toHaveBeenCalled();
  });

  it("skips halt for stream idle timeout failure", async () => {
    const { manager, controllable, queueClear } = makeManager();
    await manager.fillSlots();
    controllable.failIssue(
      3375,
      "feature-dev",
      "API Error: Stream idle timeout - partial response received"
    );
    await manager.settleForTest(3375);

    expect(mockAutonomousPause).not.toHaveBeenCalled();
    expect(queueClear).not.toHaveBeenCalled();
  });

  it("skips halt for extended GitHub network outage failure", async () => {
    const { manager, controllable, queueClear } = makeManager();
    await manager.fillSlots();
    controllable.failIssue(
      3375,
      "feature-dev",
      "network unavailable: extended GitHub connectivity loss (12 consecutive failures over 8m)"
    );
    await manager.settleForTest(3375);

    expect(mockAutonomousPause).not.toHaveBeenCalled();
    expect(queueClear).not.toHaveBeenCalled();
  });

  it("skips halt for Anthropic API 529 overload (transient capacity blip)", async () => {
    const { manager, controllable, queueClear } = makeManager();
    await manager.fillSlots();
    controllable.failIssue(
      3375,
      "feature-planning",
      "API Error: 529 Overloaded. This is a server-side issue; please retry."
    );
    await manager.settleForTest(3375);

    expect(mockAutonomousPause).not.toHaveBeenCalled();
    expect(queueClear).not.toHaveBeenCalled();
  });

  it("shows a non-blocking warning toast (not a modal) for API 529 overload", async () => {
    const { manager, controllable } = makeManager();
    await manager.fillSlots();
    controllable.failIssue(3375, "feature-dev", "API Error: Overloaded");
    await manager.settleForTest(3375);

    expect(mockShowWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("overload (529) at feature-dev")
    );
    expect(mockShowErrorMessage).not.toHaveBeenCalled();
  });

  it("STILL triggers halt for validation_error failure (real bug)", async () => {
    const { manager, controllable } = makeManager();
    await manager.fillSlots();
    controllable.failIssue(
      3375,
      "feature-validate",
      "Schema validation failed for context.json: missing required field 'plan'"
    );
    await manager.settleForTest(3375);

    expect(mockAutonomousPause).toHaveBeenCalledTimes(1);
    const [reason, triggeredBy] = mockAutonomousPause.mock.calls[0];
    expect(reason).toContain("haltQueueOnSlotFailure");
    expect(triggeredBy).toBe("haltQueueOnSlotFailure");
  });

  it("STILL triggers halt for subagent_crash failure (real bug)", async () => {
    const { manager, controllable } = makeManager();
    await manager.fillSlots();
    controllable.failIssue(
      3375,
      "feature-dev",
      "subagent crash: exit 1: TypeError: undefined is not a function"
    );
    await manager.settleForTest(3375);

    expect(mockAutonomousPause).toHaveBeenCalledTimes(1);
    const [, triggeredBy] = mockAutonomousPause.mock.calls[0];
    expect(triggeredBy).toBe("haltQueueOnSlotFailure");
  });

  it("skips halt for idle-threshold stall-kill at feature-validate (transient infra event)", async () => {
    const { manager, controllable, queueClear } = makeManager(undefined, 3499);
    await manager.fillSlots();
    controllable.failIssue(
      3499,
      "feature-validate",
      "exceeded stall idle threshold (20m 0s without output)"
    );
    await manager.settleForTest(3499);

    expect(mockAutonomousPause).not.toHaveBeenCalled();
    expect(queueClear).not.toHaveBeenCalled();
  });

  it("skips halt for hard-cap stall-kill (transient infra event)", async () => {
    const { manager, controllable, queueClear } = makeManager();
    await manager.fillSlots();
    controllable.failIssue(
      3375,
      "feature-dev",
      "[stall-killed] feature-dev terminated: exceeded stage_hard_cap (60m)"
    );
    await manager.settleForTest(3375);

    expect(mockAutonomousPause).not.toHaveBeenCalled();
    expect(queueClear).not.toHaveBeenCalled();
  });

  it("shows a warning toast (not a blocking modal) for stall-kills", async () => {
    const { manager, controllable } = makeManager(undefined, 3499);
    await manager.fillSlots();
    controllable.failIssue(
      3499,
      "feature-validate",
      "[stall-killed] exceeded stall idle threshold (20m 0s without output)"
    );
    await manager.settleForTest(3499);

    // Non-blocking warning — not showErrorMessage
    expect(mockShowWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("stalled at feature-validate")
    );
    expect(mockShowErrorMessage).not.toHaveBeenCalled();
  });
});
