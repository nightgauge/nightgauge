/**
 * Tests for ConcurrentPipelineManager branch-collision handling.
 *
 * Covers the three behaviours #2992 adds:
 *   1. Branch already exists + open PR → actionable error referencing pr-merge.
 *   2. Branch already exists + no PR → actionable error with `git branch -D` hint.
 *   3. Worktree failure always includes `repoSlug` on `onSlotFailed`.
 *   4. Re-enqueue failure surfaces via `onReEnqueueFailed`.
 *
 * @see Issue #2992
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ---------------------------------------------------------------------------
// Mocks — mirrors ConcurrentPipelineManager.test.ts setup
// ---------------------------------------------------------------------------

vi.mock("vscode", () => ({
  EventEmitter: class {
    private listeners: Array<(...args: any[]) => void> = [];
    event = (listener: (...args: any[]) => void) => {
      this.listeners.push(listener);
      return {
        dispose: () => {
          this.listeners = this.listeners.filter((l) => l !== listener);
        },
      };
    };
    fire = (data: any) => {
      this.listeners.forEach((l) => l(data));
    };
    dispose = vi.fn();
  },
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test-repo" } }],
  },
}));

// Controllable worktree mock: each test sets what `create` should do.
const worktreeCreateMock = vi.fn();

vi.mock("../../src/utils/WorktreeManager", () => ({
  WorktreeManager: vi.fn(function () {
    return {
      create: worktreeCreateMock,
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

// getPRForIssue is the PR-lookup shim the branch-collision handler calls.
const prLookupMock = vi.fn();
vi.mock("../../src/utils/prDetection", () => ({
  getPRForIssue: (issue: number, ws: string) => prLookupMock(issue, ws),
}));

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";

// Mock queue service — tests opt in to enqueue-throw behaviour.
function createMockQueueService(overrides?: Partial<Record<string, any>>) {
  return {
    dequeueIndependent: vi.fn().mockResolvedValue([]),
    updateActiveSlots: vi.fn().mockResolvedValue(undefined),
    enqueue: vi.fn().mockResolvedValue({ issueNumber: 0 }),
    getQueue: vi.fn().mockResolvedValue({ items: [] }),
    drainBlockedSuccessors: vi.fn().mockResolvedValue([]),
    clear: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
}

function createMockOrchestratorFactory() {
  const runPromise = new Promise(() => {}); // never resolves; we never reach it in these tests
  const mockOrchestrator = {
    setWorktreeOverride: vi.fn(),
    setUnattended: vi.fn(),
    setRepoOverride: vi.fn(),
    runPipeline: vi.fn().mockReturnValue(runPromise),
    stop: vi.fn(),
    dispose: vi.fn(),
  };
  const mockStateService = {
    onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
    onBatchStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
    onPhaseStart: vi.fn().mockReturnValue({ dispose: vi.fn() }),
    onPhaseComplete: vi.fn().mockReturnValue({ dispose: vi.fn() }),
    onUnifiedTokenUsage: vi.fn().mockReturnValue({ dispose: vi.fn() }),
    getState: vi.fn().mockResolvedValue(null),
    initializePipeline: vi.fn().mockResolvedValue(undefined),
    setMeta: vi.fn(),
    dispose: vi.fn(),
  };
  return {
    factory: vi.fn().mockReturnValue({
      orchestrator: mockOrchestrator,
      stateService: mockStateService,
    }),
  };
}

function createMockLogger() {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
    getChannel: vi.fn(),
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("ConcurrentPipelineManager — branch collision handling (#2992)", () => {
  let manager: ConcurrentPipelineManager;
  let mockQueue: ReturnType<typeof createMockQueueService>;
  let orchestratorMocks: ReturnType<typeof createMockOrchestratorFactory>;
  let mockLogger: ReturnType<typeof createMockLogger>;

  beforeEach(() => {
    worktreeCreateMock.mockReset();
    prLookupMock.mockReset();
    mockQueue = createMockQueueService();
    orchestratorMocks = createMockOrchestratorFactory();
    mockLogger = createMockLogger();

    manager = new ConcurrentPipelineManager(
      "/test-repo",
      mockQueue as any,
      orchestratorMocks.factory,
      mockLogger as any,
      { maxConcurrent: 2, worktreeBase: ".worktrees" }
    );
  });

  it("surfaces an actionable error (with PR url + pr-merge hint) when worktree fails on an existing branch and a PR is open", async () => {
    worktreeCreateMock.mockRejectedValue(
      new Error("fatal: a branch named 'feat/100-foo' already exists")
    );
    prLookupMock.mockResolvedValue({
      number: 42,
      url: "https://github.com/test/repo/pull/42",
    });

    const onSlotFailed = vi.fn();
    manager.setCallbacks({ onSlotFailed });

    mockQueue.dequeueIndependent.mockResolvedValueOnce([
      {
        issueNumber: 100,
        title: "foo",
        labels: [],
        position: 1,
        status: "pending",
        addedAt: new Date().toISOString(),
        repoName: "test/repo",
      },
    ]);

    await manager.fillSlots();

    expect(onSlotFailed).toHaveBeenCalledTimes(1);
    const [, issueNumber, error, cost, repoSlug] = onSlotFailed.mock.calls[0];
    expect(issueNumber).toBe(100);
    expect(error).toBeInstanceOf(Error);
    expect(error.name).toBe("BranchCollisionError");
    expect(error.message).toContain("feat/100-");
    expect(error.message).toContain("https://github.com/test/repo/pull/42");
    expect(error.message).toContain("pr-merge");
    expect(cost).toBe(0);
    expect(repoSlug).toBe("test/repo");
  });

  it("surfaces a `git branch -D` remediation when branch exists but no open PR is found", async () => {
    worktreeCreateMock.mockRejectedValue(
      new Error("fatal: a branch named 'feat/101-bar' already exists")
    );
    prLookupMock.mockResolvedValue(null);

    const onSlotFailed = vi.fn();
    manager.setCallbacks({ onSlotFailed });

    mockQueue.dequeueIndependent.mockResolvedValueOnce([
      {
        issueNumber: 101,
        title: "bar",
        labels: [],
        position: 1,
        status: "pending",
        addedAt: new Date().toISOString(),
        repoName: "test/repo",
      },
    ]);

    await manager.fillSlots();

    expect(onSlotFailed).toHaveBeenCalledTimes(1);
    const [, , error, , repoSlug] = onSlotFailed.mock.calls[0];
    expect(error.message).toContain("git branch -D");
    expect(error.message).toContain("feat/101-");
    expect(repoSlug).toBe("test/repo");
  });

  it("leaves non-collision worktree errors unchanged and still passes repoSlug", async () => {
    const origError = new Error("permission denied: /test-repo/.worktrees");
    worktreeCreateMock.mockRejectedValue(origError);

    const onSlotFailed = vi.fn();
    manager.setCallbacks({ onSlotFailed });

    mockQueue.dequeueIndependent.mockResolvedValueOnce([
      {
        issueNumber: 102,
        title: "baz",
        labels: [],
        position: 1,
        status: "pending",
        addedAt: new Date().toISOString(),
        repoName: "test/repo",
      },
    ]);

    await manager.fillSlots();

    // Non-collision errors must not trigger the PR lookup (pointless cost).
    expect(prLookupMock).not.toHaveBeenCalled();

    expect(onSlotFailed).toHaveBeenCalledTimes(1);
    const [, , error, , repoSlug] = onSlotFailed.mock.calls[0];
    expect(error).toBe(origError);
    expect(repoSlug).toBe("test/repo");
  });

  it("invokes onReEnqueueFailed (with stack) when the re-enqueue after a slot failure throws", async () => {
    // Simulate a shutdown-guard rejection on re-enqueue.
    worktreeCreateMock.mockRejectedValue(
      new Error("fatal: a branch named 'feat/103-qux' already exists")
    );
    prLookupMock.mockResolvedValue(null);

    // startSlot returning false means fillSlots attempts to re-enqueue.
    // Make that throw with a known error.
    const enqueueError = new Error("Refusing enqueue — stop in progress");
    mockQueue.enqueue.mockRejectedValue(enqueueError);

    const onSlotFailed = vi.fn();
    const onReEnqueueFailed = vi.fn();
    manager.setCallbacks({ onSlotFailed, onReEnqueueFailed });

    mockQueue.dequeueIndependent.mockResolvedValueOnce([
      {
        issueNumber: 103,
        title: "qux",
        labels: [],
        position: 1,
        status: "pending",
        addedAt: new Date().toISOString(),
        repoName: "test/repo",
      },
    ]);

    await manager.fillSlots();

    expect(onReEnqueueFailed).toHaveBeenCalledTimes(1);
    const [issueNumber, err] = onReEnqueueFailed.mock.calls[0];
    expect(issueNumber).toBe(103);
    expect(err).toBe(enqueueError);
    // Logger received a structured error message with the stack field.
    expect(mockLogger.error).toHaveBeenCalledWith(
      "Failed to re-enqueue item after slot failure",
      expect.objectContaining({ issueNumber: 103, stack: expect.any(String) })
    );
  });
});
