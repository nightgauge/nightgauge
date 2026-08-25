/**
 * #889 — there must be exactly ONE branch-name composer, and it is the Go
 * one. `ConcurrentPipelineManager` used to build the name itself:
 *
 *   `feat/${item.issueNumber}-${this.slugify(item.title)}`
 *
 * which hardcoded `feat/` (so every `type:bug` issue was branded a feature),
 * doubled an issue number the queue item's display title already carried, and
 * truncated at 40 chars where Go truncates at 50.
 *
 * These tests pin the DELEGATION, not a name. Asserting a literal name here
 * would just be the second composer again, written in a test file — the Go
 * side owns what the name looks like (internal/git/testdata/branch_name_cases.json).
 */

import { describe, it, expect, vi, beforeEach, type Mock } from "vitest";

const gitComposeBranchName = vi.fn();
const worktreeCreate = vi.fn();

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
}));

vi.mock("../../src/utils/WorktreeManager", () => ({
  WorktreeManager: vi.fn(function () {
    return {
      create: worktreeCreate,
      cleanup: vi.fn().mockResolvedValue(undefined),
      cleanupOrphans: vi.fn().mockResolvedValue(0),
      cleanupAll: vi.fn().mockResolvedValue(undefined),
      listActive: vi.fn().mockResolvedValue([]),
      getRepoRoot: vi.fn().mockReturnValue("/test-repo"),
    };
  }),
}));

vi.mock("../../src/utils/nightgaugeConfig", () => ({
  getConcurrentPipelineConfig: vi
    .fn()
    .mockReturnValue({ maxConcurrent: 2, worktreeBase: ".worktrees" }),
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      gitComposeBranchName,
      autonomousStatus: vi.fn().mockResolvedValue({ status: "running" }),
      autonomousPause: vi.fn().mockResolvedValue(undefined),
    }),
  },
}));

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";

function makeManager(): ConcurrentPipelineManager {
  return new ConcurrentPipelineManager(
    "/test-repo",
    {
      dequeueIndependent: vi.fn().mockResolvedValue([]),
      updateActiveSlots: vi.fn().mockResolvedValue(undefined),
      drainBlockedSuccessors: vi.fn().mockResolvedValue([]),
      enqueue: vi.fn().mockResolvedValue(null),
      clear: vi.fn().mockResolvedValue(undefined),
      getQueue: vi.fn().mockResolvedValue({ items: [], status: "idle" }),
    } as any,
    vi.fn() as any,
    {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
      getChannel: vi.fn(),
    } as any,
    { maxConcurrent: 2, worktreeBase: ".worktrees" }
  );
}

const BUG_ITEM = {
  issueNumber: 227,
  title: "227 Per-operation error isolation and transactions in /sync/push",
  position: 1,
  status: "pending" as const,
  addedAt: new Date(0).toISOString(),
  labels: ["type:bug"],
};

async function startSlot(item: any): Promise<void> {
  const manager = makeManager();
  // startSlot is private; the delegation it performs is the whole subject.
  await (manager as any).startSlot(item).catch(() => undefined);
}

describe("ConcurrentPipelineManager — one branch-name composer (#889)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // Stop the dispatch right after the name is used, so these tests observe
    // composition without standing up the rest of the slot lifecycle.
    worktreeCreate.mockRejectedValue(new Error("stop here"));
    gitComposeBranchName.mockResolvedValue({ name: "fix/227-per-operation-error-isolation" });
  });

  it("asks the Go composer for the name, passing number, title AND labels", async () => {
    await startSlot(BUG_ITEM);

    expect(gitComposeBranchName).toHaveBeenCalledTimes(1);
    expect(gitComposeBranchName).toHaveBeenCalledWith(227, BUG_ITEM.title, ["type:bug"]);
  });

  it("creates the worktree on exactly the name the composer returned", async () => {
    gitComposeBranchName.mockResolvedValue({ name: "fix/227-whatever-go-decided" });

    await startSlot(BUG_ITEM);

    expect(worktreeCreate).toHaveBeenCalled();
    expect((worktreeCreate as Mock).mock.calls[0][1]).toBe("fix/227-whatever-go-decided");
  });

  it("does not re-derive, re-prefix or re-truncate the composed name", async () => {
    // Deliberately unlike anything a local composer would produce: a prefix
    // the TypeScript never knew about, and a slug past its old 40-char budget.
    const exotic = "refactor/227-a-slug-far-longer-than-the-forty-character-budget-typescript-used";
    gitComposeBranchName.mockResolvedValue({ name: exotic });

    await startSlot(BUG_ITEM);

    expect((worktreeCreate as Mock).mock.calls[0][1]).toBe(exotic);
  });

  it("passes undefined labels through rather than inventing a prefix", async () => {
    const { labels: _labels, ...unlabelled } = BUG_ITEM;

    await startSlot(unlabelled);

    expect(gitComposeBranchName).toHaveBeenCalledWith(227, BUG_ITEM.title, undefined);
  });

  it("fails closed when the composer is unreachable — no local fallback name", async () => {
    gitComposeBranchName.mockRejectedValue(new Error("ipc down"));

    await startSlot(BUG_ITEM);

    // The whole defect was a second composer standing in for the first. A
    // fallback here would BE that composer, so the dispatch must not proceed.
    expect(worktreeCreate).not.toHaveBeenCalled();
  });
});
