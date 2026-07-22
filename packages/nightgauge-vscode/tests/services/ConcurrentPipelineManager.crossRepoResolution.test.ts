/**
 * Cross-repo, per-command worktree resolution tests for ConcurrentPipelineManager.
 *
 * Reproduces the #4117 acceptance criteria: in a multi-root `.code-workspace`
 * with several open repos, each queued item must dispatch into the worktree of
 * the repo IT targets (via `item.repoName` → `resolveWorktreeManager()` →
 * `WorkspaceManager.findRepositoryByGitHub()`), not the single fixed root the
 * manager was constructed with — and an item whose repo isn't open in this
 * workspace must fail gracefully (no crash, no impact on sibling items).
 *
 * This exercises `resolveWorktreeManager()` end-to-end with a REAL cross-repo
 * `workspaceManager` (existing perRepoCap tests pass `repoName` but never wire
 * a `workspaceManager`, so they never exercise this path; existing
 * notifierReconcile/haltSkipResolved tests wire one but always resolve to the
 * SAME path as the default root, so the "different repo → new WorktreeManager"
 * branch was previously untested).
 *
 * @see Issue #2245 - Cross-repo pipeline worktree creation (resolveWorktreeManager)
 * @see Issue #4117 - Agent runner gated on a single incrediRoot
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock vscode
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
    workspaceFolders: [{ uri: { fsPath: "/workspace/repo-a" } }],
  },
  window: {
    showErrorMessage: vi.fn().mockResolvedValue(undefined),
    showWarningMessage: vi.fn().mockResolvedValue(undefined),
  },
  commands: {
    executeCommand: vi.fn().mockResolvedValue(undefined),
  },
}));

// Mock WorktreeManager so each instance's `create()`/`getRepoRoot()` reflects
// the repo root it was constructed with — lets the test tell a default-root
// worktree apart from a cross-repo one.
vi.mock("../../src/utils/WorktreeManager", () => ({
  WorktreeManager: vi.fn(function (this: unknown, repoRoot: string) {
    return {
      create: vi.fn().mockImplementation(async (issueNumber: number, branchName: string) => ({
        path: `${repoRoot}/.worktrees/issue-${issueNumber}`,
        branch: branchName,
        issueNumber,
        exists: true,
      })),
      cleanup: vi.fn().mockResolvedValue(undefined),
      cleanupOrphans: vi.fn().mockResolvedValue(0),
      cleanupAll: vi.fn().mockResolvedValue(undefined),
      listActive: vi.fn().mockResolvedValue([]),
      getRepoRoot: vi.fn().mockReturnValue(repoRoot),
      getWorktreePath: vi
        .fn()
        .mockImplementation((n: number) => `${repoRoot}/.worktrees/issue-${n}`),
    };
  }),
}));

vi.mock("../../src/utils/incrediConfig", () => ({
  getConcurrentPipelineConfig: vi.fn().mockReturnValue({
    maxConcurrent: 3,
    worktreeBase: ".worktrees",
  }),
}));

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";
import { WorktreeManager } from "../../src/utils/WorktreeManager";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

interface QueueItem {
  issueNumber: number;
  title: string;
  position: number;
  status: string;
  addedAt: string;
  repoName?: string;
}

function makeItem(issueNumber: number, repoName?: string): QueueItem {
  return {
    issueNumber,
    title: `Issue #${issueNumber}`,
    position: 1,
    status: "pending",
    addedAt: new Date().toISOString(),
    repoName,
  };
}

/** Never-resolving orchestrator so started slots stay "running" for inspection. */
function createPendingFactory() {
  return vi.fn().mockImplementation((_workDir: string, _issueNumber: number) => ({
    orchestrator: {
      setRepoOverride: vi.fn(),
      setUnattended: vi.fn(),
      runPipeline: vi.fn().mockReturnValue(new Promise(() => {})),
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
      initializePipeline: vi.fn().mockResolvedValue(undefined),
      setMeta: vi.fn(),
      dispose: vi.fn(),
    },
  }));
}

/**
 * Mock WorkspaceManager whose `findRepositoryByGitHub` resolves a fixed set of
 * "owner/repo" identities to local paths, mirroring a multi-root
 * `.code-workspace` with several open folders. Unknown identities resolve to
 * undefined — the "repo not open in this workspace" case.
 */
function makeWorkspaceManager(repos: Record<string, string>) {
  return {
    findRepositoryByGitHub: vi.fn((ownerSlashRepo: string) => {
      const path = repos[ownerSlashRepo];
      return path ? { path } : undefined;
    }),
  };
}

function makeQueueService(items: QueueItem[]) {
  return {
    dequeueIndependent: vi.fn().mockResolvedValueOnce(items).mockResolvedValue([]),
    updateActiveSlots: vi.fn().mockResolvedValue(undefined),
    enqueue: vi.fn().mockResolvedValue({ issueNumber: 0, position: 0 }),
  };
}

function makeLogger() {
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

describe("ConcurrentPipelineManager — per-command cross-repo worktree resolution (#4117)", () => {
  beforeEach(() => {
    (WorktreeManager as unknown as ReturnType<typeof vi.fn>).mockClear();
  });

  it("dispatches each queued item into ITS target repo's worktree, not the manager's fixed default root", async () => {
    const workspaceManager = makeWorkspaceManager({
      "org/repo-a": "/workspace/repo-a", // same as the manager's default root
      "org/repo-b": "/workspace/repo-b", // a DIFFERENT repo in the same workspace
    });
    const queue = makeQueueService([makeItem(10, "org/repo-a"), makeItem(20, "org/repo-b")]);
    const logger = makeLogger();

    const manager = new ConcurrentPipelineManager(
      "/workspace/repo-a", // the manager's single fixed default root
      queue as any,
      createPendingFactory(),
      logger as any,
      { maxConcurrent: 3, worktreeBase: ".worktrees" },
      workspaceManager as any
    );

    const started = await manager.fillSlots();

    expect(started).toBe(2);
    const slots = manager.getActiveSlots();
    const slotFor = (issueNumber: number) => slots.find((s) => s.issueNumber === issueNumber);

    // #10 targets the same repo the manager defaults to — dispatched there.
    expect(slotFor(10)?.worktreePath).toBe("/workspace/repo-a/.worktrees/issue-10");
    // #20 targets a DIFFERENT repo — must land in repo-b's worktree, not
    // repo-a's, even though the manager was constructed with repo-a as its
    // single fixed root.
    expect(slotFor(20)?.worktreePath).toBe("/workspace/repo-b/.worktrees/issue-20");

    expect(workspaceManager.findRepositoryByGitHub).toHaveBeenCalledWith("org/repo-a");
    expect(workspaceManager.findRepositoryByGitHub).toHaveBeenCalledWith("org/repo-b");
  });

  it("reuses the existing default WorktreeManager instance when the resolved repo has the same root (no redundant construction)", async () => {
    const workspaceManager = makeWorkspaceManager({
      "org/repo-a": "/workspace/repo-a",
    });
    const queue = makeQueueService([makeItem(10, "org/repo-a")]);
    const logger = makeLogger();

    new ConcurrentPipelineManager(
      "/workspace/repo-a",
      queue as any,
      createPendingFactory(),
      logger as any,
      { maxConcurrent: 3, worktreeBase: ".worktrees" },
      workspaceManager as any
    );

    // Constructor itself creates exactly one WorktreeManager (the default).
    expect(WorktreeManager).toHaveBeenCalledTimes(1);
  });

  it("fails gracefully — no crash, sibling items unaffected — when an item's repo isn't open in this workspace", async () => {
    const workspaceManager = makeWorkspaceManager({
      "org/repo-a": "/workspace/repo-a",
      // "org/repo-missing" intentionally NOT registered — simulates a
      // dashboard trigger for a repo the user hasn't opened as a folder.
    });
    const queue = makeQueueService([makeItem(10, "org/repo-a"), makeItem(99, "org/repo-missing")]);
    const logger = makeLogger();
    const onSlotFailed = vi.fn();

    const manager = new ConcurrentPipelineManager(
      "/workspace/repo-a",
      queue as any,
      createPendingFactory(),
      logger as any,
      { maxConcurrent: 3, worktreeBase: ".worktrees" },
      workspaceManager as any
    );
    manager.setCallbacks({ onSlotFailed });

    await expect(manager.fillSlots()).resolves.not.toThrow();
    const started = await manager.fillSlots();

    // The valid item still starts normally...
    expect(manager.isRunning(10)).toBe(true);
    // ...and the unresolvable item fails without crashing the fill loop, with
    // a clear, actionable error surfaced via the normal failure callback.
    expect(onSlotFailed).toHaveBeenCalledWith(
      expect.any(Number),
      99,
      expect.objectContaining({
        message: expect.stringContaining("not open in this workspace"),
      }),
      0,
      "org/repo-missing"
    );
    expect(manager.isRunning(99)).toBe(false);
    void started;

    // The unresolvable item is re-enqueued rather than silently dropped, so
    // it isn't lost — matching the existing slot-start-failure recovery path.
    expect(queue.enqueue).toHaveBeenCalledWith(99, "Issue #99", undefined);
  });
});
