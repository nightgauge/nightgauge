/**
 * Regression tests for Issue #3704: ConcurrentPipelineManager passes full
 * token breakdown (not just costUsd) to onSlotCompleted callback.
 *
 * Before the fix:
 *   onSlotCompleted received a bare costUsd: number (always 0 because
 *   updateTokens was a no-op before initEmpty).
 * After the fix:
 *   onSlotCompleted receives { input, output, cacheRead, cacheCreation,
 *   estimated_cost_usd } sourced from the slot's PipelineStateService.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => ({
  EventEmitter: class {
    private listeners: Array<(...args: unknown[]) => void> = [];
    event = (listener: (...args: unknown[]) => void) => {
      this.listeners.push(listener);
      return { dispose: () => {} };
    };
    fire = (data: unknown) => {
      this.listeners.forEach((l) => l(data));
    };
    dispose = vi.fn();
  },
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test-repo" } }],
  },
}));

vi.mock("../../src/utils/WorktreeManager", () => ({
  WorktreeManager: vi.fn(function () {
    return {
      create: vi.fn().mockResolvedValue({
        path: "/test-repo/.worktrees/issue-3704",
        branch: "feat/3704-test",
        issueNumber: 3704,
        exists: true,
      }),
      cleanup: vi.fn().mockResolvedValue(undefined),
      cleanupOrphans: vi.fn().mockResolvedValue(0),
      cleanupAll: vi.fn().mockResolvedValue(undefined),
      listActive: vi.fn().mockResolvedValue([]),
      getRepoRoot: vi.fn().mockReturnValue("/test-repo"),
    };
  }),
}));

vi.mock("../../src/utils/incrediConfig", () => ({
  getConcurrentPipelineConfig: vi.fn().mockReturnValue({
    maxConcurrent: 2,
    worktreeBase: ".worktrees",
  }),
}));

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";

function createMockQueueService() {
  return {
    dequeueIndependent: vi.fn().mockResolvedValue([]),
    updateActiveSlots: vi.fn().mockResolvedValue(undefined),
  };
}

function createMockLogger() {
  return { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn(), getChannel: vi.fn() };
}

describe("ConcurrentPipelineManager — token passthrough (#3704)", () => {
  let manager: ConcurrentPipelineManager;
  let mockQueue: ReturnType<typeof createMockQueueService>;
  let mockLogger: ReturnType<typeof createMockLogger>;

  const SLOT_TOKENS = {
    input: 1500,
    output: 800,
    cacheRead: 200,
    cacheCreation: 0,
    estimated_cost_usd: 0.042,
  };

  let resolveRun: ((v: unknown) => void) | undefined;

  beforeEach(() => {
    mockQueue = createMockQueueService();
    mockLogger = createMockLogger();

    const runPromise = new Promise((resolve) => {
      resolveRun = resolve;
    });

    const mockStateService = {
      onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onBatchStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onPhaseStart: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onPhaseComplete: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onUnifiedTokenUsage: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      initEmpty: vi.fn(),
      setMeta: vi.fn(),
      getState: vi.fn().mockResolvedValue({
        tokens: {
          input: SLOT_TOKENS.input,
          output: SLOT_TOKENS.output,
          cacheRead: SLOT_TOKENS.cacheRead,
          cacheCreation: SLOT_TOKENS.cacheCreation,
          estimated_cost_usd: SLOT_TOKENS.estimated_cost_usd,
        },
      }),
      dispose: vi.fn(),
    };

    const mockOrchestrator = {
      setWorktreeOverride: vi.fn(),
      setUnattended: vi.fn(),
      setRepoOverride: vi.fn(),
      runPipeline: vi.fn().mockReturnValue(runPromise),
      stop: vi.fn(),
      dispose: vi.fn(),
    };

    manager = new ConcurrentPipelineManager(
      "/test-repo",
      mockQueue as any,
      vi.fn().mockReturnValue({ orchestrator: mockOrchestrator, stateService: mockStateService }),
      mockLogger as any,
      { maxConcurrent: 2, worktreeBase: ".worktrees" }
    );
  });

  it("onSlotCompleted receives full token object with non-zero input/output tokens", async () => {
    const completedArgs: unknown[] = [];
    manager.setCallbacks({
      onSlotCompleted: (...args) => completedArgs.push(args),
    });

    mockQueue.dequeueIndependent.mockResolvedValueOnce([
      { issueNumber: 3704, title: "Fix tokens", priority: 1 },
    ]);

    await manager.fillSlots();

    // Resolve the pipeline run
    resolveRun?.({
      success: true,
      completedStages: ["issue-pickup"],
      skippedStages: [],
      deferredStages: [],
      totalDurationMs: 30_000,
    });

    // Await the real slot-lifecycle completion instead of a fixed sleep.
    await manager.settleForTest(3704);

    expect(completedArgs).toHaveLength(1);
    const [, , , tokens] = completedArgs[0] as [
      number,
      number,
      unknown,
      typeof SLOT_TOKENS,
      string?,
    ];
    expect(tokens.input).toBe(SLOT_TOKENS.input);
    expect(tokens.output).toBe(SLOT_TOKENS.output);
    expect(tokens.cacheRead).toBe(SLOT_TOKENS.cacheRead);
    expect(tokens.estimated_cost_usd).toBe(SLOT_TOKENS.estimated_cost_usd);
  });

  it("calls stateService.initEmpty() when slot is created", async () => {
    const initEmptyCalls: number[] = [];

    // Re-create manager with a spy that records initEmpty calls
    const mockStateServiceWithSpy = {
      onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onBatchStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onPhaseStart: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onPhaseComplete: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onUnifiedTokenUsage: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      initEmpty: vi.fn().mockImplementation(() => initEmptyCalls.push(1)),
      setMeta: vi.fn(),
      getState: vi.fn().mockResolvedValue(null),
      dispose: vi.fn(),
    };

    let resolveSpyRun: ((v: unknown) => void) | undefined;
    const spyRunPromise = new Promise((resolve) => {
      resolveSpyRun = resolve;
    });

    const spyManager = new ConcurrentPipelineManager(
      "/test-repo",
      mockQueue as any,
      vi.fn().mockReturnValue({
        orchestrator: {
          setWorktreeOverride: vi.fn(),
          setUnattended: vi.fn(),
          setRepoOverride: vi.fn(),
          runPipeline: vi.fn().mockReturnValue(spyRunPromise),
          stop: vi.fn(),
          dispose: vi.fn(),
        },
        stateService: mockStateServiceWithSpy,
      }),
      mockLogger as any,
      { maxConcurrent: 2, worktreeBase: ".worktrees" }
    );

    mockQueue.dequeueIndependent.mockResolvedValueOnce([
      { issueNumber: 3704, title: "Fix tokens", priority: 1 },
    ]);

    await spyManager.fillSlots();

    expect(initEmptyCalls).toHaveLength(1);

    // Clean up — resolve run and drain its lifecycle to avoid a leaked promise.
    resolveSpyRun?.({
      success: false,
      completedStages: [],
      skippedStages: [],
      deferredStages: [],
      totalDurationMs: 0,
    });
    await spyManager.settleForTest(3704);
  });
});
