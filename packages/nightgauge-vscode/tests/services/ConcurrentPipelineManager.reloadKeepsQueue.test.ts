/**
 * #2396: a window reload or close ends the window's runs and nothing more.
 *
 * `deactivate()` used to call `abortAll()`, which begins by clearing the
 * queue, so a reload with one pipeline running emptied the daemon's
 * persisted queue: every waiting issue, platform-triggered runs included,
 * was gone when the window came back. A reload now passes `keepQueued`: the
 * items the ending dispatches took ("processing") are dropped, since nothing
 * else would release them once the window is gone, and every waiting item
 * stays for the next window. Stop All still clears the queue.
 */

import { describe, it, expect, vi, beforeEach, type Mock } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";

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
    dropProcessing: vi.fn().mockResolvedValue(issueNumbers.length),
    complete: vi.fn().mockResolvedValue(undefined),
    getQueue: vi.fn().mockResolvedValue({ items: [], status: "idle" }),
  };

  const controllable = createControllableFactory();
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
    { maxConcurrent: Math.max(1, issueNumbers.length) }
  );
  manager.setCallbacks({ onSlotFailed: vi.fn(), onSlotCompleted: vi.fn() });
  return { manager, queueService, controllable };
}

const STOPPED = {
  success: false,
  completedStages: [],
  skippedStages: [],
  deferredStages: [],
  failedStage: "feature-dev",
  totalDurationMs: 1000,
};

describe("ConcurrentPipelineManager — a window reload keeps the queue (#2396)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAutonomousStatus.mockResolvedValue({ status: "running" });
  });

  it("drops only the dispatched items, before its first await, and never clears the queue", async () => {
    const { manager, queueService, controllable } = buildManager([41]);
    await manager.fillSlots();
    expect(manager.hasDispatchInFlight).toBe(true);

    const abort = manager.abortAll({ keepQueued: true });
    // Sent synchronously: a reloading window may be gone before anything
    // after the first await runs.
    expect(queueService.dropProcessing).toHaveBeenCalledTimes(1);
    expect(queueService.clear).not.toHaveBeenCalled();

    // Every microtask the abort chains runs before a setImmediate callback.
    await new Promise((resolve) => setImmediate(resolve));
    expect(controllable.stopMockFor(41)).toHaveBeenCalledTimes(1);
    controllable.finishWith(41, STOPPED);
    await abort;

    expect(queueService.clear).not.toHaveBeenCalled();
    expect(queueService.dropProcessing).toHaveBeenCalledTimes(1);
    expect(manager.activeSlotCount).toBe(0);
  });

  it("keeps listing the platform runs the queue carries", async () => {
    const { manager, queueService, controllable } = buildManager([42]);
    queueService.getQueue.mockResolvedValue({
      items: [{ issueNumber: 7, title: "Issue #7", status: "pending", remoteRunId: "run-queued" }],
      status: "waiting",
    });
    await manager.syncQueuedRemoteRuns();
    await manager.fillSlots();
    expect(manager.heldRemoteRunIds()).toContain("run-queued");

    const abort = manager.abortAll({ keepQueued: true });
    controllable.finishWith(42, STOPPED);
    await abort;

    expect(manager.heldRemoteRunIds()).toContain("run-queued");
  });

  it("Stop All still clears the queue and drops nothing separately", async () => {
    const { manager, queueService, controllable } = buildManager([43]);
    await manager.fillSlots();

    const abort = manager.abortAll();
    expect(queueService.clear).toHaveBeenCalledTimes(1);
    controllable.finishWith(43, STOPPED);
    await abort;

    expect(queueService.dropProcessing).not.toHaveBeenCalled();
  });

  it("has no dispatch in flight when idle", () => {
    const { manager } = buildManager([]);
    expect(manager.hasDispatchInFlight).toBe(false);
  });
});

describe("deactivate() takes the reload path (#2396)", () => {
  const source = readFileSync(path.resolve(__dirname, "../../src/extension.ts"), "utf-8");
  const deactivate = source.slice(source.indexOf("export function deactivate()"));

  it("keeps the queue whenever a dispatch is in flight, and never clears it", () => {
    expect(deactivate).toMatch(
      /if \(concurrentPipelineManager\?\.hasDispatchInFlight\) \{\s*concurrentPipelineManager\.abortAll\(\{ keepQueued: true \}\)/
    );
    expect(deactivate).not.toMatch(/\.abortAll\(\)/);
  });
});
