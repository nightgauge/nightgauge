/**
 * #2403 — concurrent slots are held by repository and issue number.
 *
 * `example-org/platform#21` and `example-org/app#21` are different issues. The
 * Go scheduler already tells them apart, so with one running it hands out the
 * other; the extension used to key its slots by number alone, treat the second
 * as a duplicate dispatch of the first and drop it from the queue. Stop Slot,
 * `isRunning` and the slot callbacks now name the repository too.
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
    workspaceFolders: [{ uri: { fsPath: "/test-repo" } }],
  },
  window: {
    showErrorMessage: vi.fn().mockResolvedValue(undefined),
    showWarningMessage: vi.fn().mockResolvedValue(undefined),
    showInformationMessage: vi.fn().mockResolvedValue(undefined),
  },
  commands: {
    executeCommand: vi.fn().mockResolvedValue(undefined),
  },
  env: {
    openExternal: vi.fn().mockResolvedValue(true),
  },
  Uri: {
    parse: vi.fn((s: string) => ({ toString: () => s })),
  },
}));

// Mock WorktreeManager
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
    };
  }),
}));

// Mock nightgaugeConfig
vi.mock("../../src/utils/nightgaugeConfig", () => ({
  getConcurrentPipelineConfig: vi.fn().mockReturnValue({
    maxConcurrent: 3,
  }),
}));

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
    }),
  },
}));

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";
import { fakeCloneLayout } from "../helpers/cloneLayout";
import type { QueueItem as QueueItemType } from "../../src/types/queue";

// Per-clone data resolves under the git directory (ADR-024 § 7); map the
// fake roots (and their worktrees) to a clone without running git.
beforeEach(() => {
  fakeCloneLayout("/test-repo");
});

const PLATFORM = "example-org/platform";
const APP = "example-org/app";

/** An orchestrator factory whose runs are told apart by call order. */
function createFactory() {
  const runs: Array<{ issueNumber: number; orchestrator: any; resolve: (r: any) => void }> = [];
  const factory = vi.fn().mockImplementation((_workDir: string, issueNumber: number) => {
    let resolve!: (r: any) => void;
    const promise = new Promise((r) => (resolve = r));
    const orchestrator = {
      setWorktreeOverride: vi.fn(),
      setRunRepoRoot: vi.fn(),
      setRepoOverride: vi.fn(),
      setUnattended: vi.fn(),
      resolveRunRepoSlug: vi.fn().mockResolvedValue(""),
      runPipeline: vi.fn().mockReturnValue(promise),
      stop: vi.fn(),
      dispose: vi.fn(),
    };
    runs.push({ issueNumber, orchestrator, resolve });
    const stateService = {
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
    };
    return { orchestrator, stateService };
  });
  return { factory, runs };
}

function item(issueNumber: number, repoName: string): QueueItemType {
  return {
    issueNumber,
    title: `${repoName}#${issueNumber}`,
    position: 1,
    status: "pending",
    addedAt: new Date().toISOString(),
    repoName,
  };
}

function setup() {
  const queue = {
    dequeueIndependent: vi.fn().mockResolvedValue([]),
    updateActiveSlots: vi.fn().mockResolvedValue(undefined),
    drainBlockedSuccessors: vi.fn().mockResolvedValue([]),
    enqueue: vi.fn().mockResolvedValue(null),
    complete: vi.fn().mockResolvedValue(undefined),
    clear: vi.fn().mockResolvedValue(undefined),
    getQueue: vi.fn().mockResolvedValue({ items: [], status: "idle" }),
  };
  const logger = {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
    getChannel: vi.fn(),
  };
  const { factory, runs } = createFactory();
  const manager = new ConcurrentPipelineManager(
    "/test-repo",
    queue as any,
    factory,
    logger as any,
    {
      maxConcurrent: 3,
    }
  );
  return { manager, queue, logger, runs };
}

const DONE = {
  success: true,
  completedStages: ["issue-pickup"],
  skippedStages: [],
  deferredStages: [],
  totalDurationMs: 10,
};

describe("ConcurrentPipelineManager — slot identity (#2403)", () => {
  it("dispatches another repository's same-numbered issue while the first runs", async () => {
    const { manager, queue, logger } = setup();
    queue.dequeueIndependent.mockResolvedValueOnce([item(21, PLATFORM)]);
    await manager.fillSlots();
    queue.dequeueIndependent.mockResolvedValueOnce([item(21, APP)]);
    await manager.fillSlots();

    expect(manager.activeSlotCount).toBe(2);
    expect(manager.getActiveSlots().map((s) => s.repo)).toEqual([PLATFORM, APP]);
    expect(manager.isRunning(21, PLATFORM)).toBe(true);
    expect(manager.isRunning(21, "Example-Org/App")).toBe(true);
    expect(manager.isRunning(21, "example-org/other")).toBe(false);
    expect(manager.isIssueInSlots(21, APP)).toBe(true);
    // Neither was skipped as a duplicate, nor released from the queue.
    expect(logger.warn).not.toHaveBeenCalledWith(
      expect.stringContaining("duplicate dispatch"),
      expect.anything()
    );
    expect(queue.complete).not.toHaveBeenCalled();
  });

  it("still skips the same repository's issue as a duplicate dispatch", async () => {
    const { manager, queue, logger } = setup();
    queue.dequeueIndependent.mockResolvedValueOnce([item(21, PLATFORM)]);
    await manager.fillSlots();
    queue.dequeueIndependent.mockResolvedValueOnce([item(21, "Example-Org/Platform")]);
    await manager.fillSlots();

    expect(manager.activeSlotCount).toBe(1);
    expect(logger.warn).toHaveBeenCalledWith(
      expect.stringContaining("duplicate dispatch"),
      expect.objectContaining({ issueNumber: 21, hasLiveSlot: true })
    );
  });

  it("stops only the named repository's slot", async () => {
    const { manager, queue, runs } = setup();
    queue.dequeueIndependent.mockResolvedValueOnce([item(21, PLATFORM), item(21, APP)]);
    await manager.fillSlots();
    expect(runs).toHaveLength(2);

    // The number alone names two running slots: nothing is stopped.
    expect(manager.abortSlot(21)).toBe(false);
    expect(runs[0].orchestrator.stop).not.toHaveBeenCalled();
    expect(runs[1].orchestrator.stop).not.toHaveBeenCalled();

    expect(manager.abortSlot(21, APP)).toBe(true);
    expect(runs[1].orchestrator.stop).toHaveBeenCalledTimes(1);
    expect(runs[0].orchestrator.stop).not.toHaveBeenCalled();
  });

  it("names the repository in every slot callback, and tears down only its own slot", async () => {
    const { manager, queue, runs } = setup();
    const started = vi.fn();
    const cleaned = vi.fn();
    const preparing = vi.fn();
    manager.setCallbacks({
      onSlotStarted: started,
      onSlotCleaned: cleaned,
      onSlotPreparing: preparing,
    });
    queue.dequeueIndependent.mockResolvedValueOnce([item(21, PLATFORM), item(21, APP)]);
    await manager.fillSlots();

    expect(preparing.mock.calls.map((c) => c[3])).toEqual([PLATFORM, APP]);
    expect(started.mock.calls.map((c) => c[5])).toEqual([PLATFORM, APP]);

    runs[0].resolve(DONE);
    await vi.waitFor(() => expect(cleaned).toHaveBeenCalledWith(expect.any(Number), 21, PLATFORM));
    expect(manager.isRunning(21, PLATFORM)).toBe(false);
    expect(manager.isRunning(21, APP)).toBe(true);
    runs[1].resolve(DONE);
    await manager.settleForTest();
  });

  it("keeps the single-repository case: the number alone names the slot", async () => {
    const { manager, queue, runs } = setup();
    queue.dequeueIndependent.mockResolvedValueOnce([
      { ...item(42, PLATFORM), repoName: undefined },
    ]);
    await manager.fillSlots();
    expect(manager.isRunning(42)).toBe(true);
    expect(manager.abortSlot(42)).toBe(true);
    expect(runs[0].orchestrator.stop).toHaveBeenCalledTimes(1);
  });
});
