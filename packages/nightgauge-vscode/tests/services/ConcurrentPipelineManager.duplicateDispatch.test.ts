/**
 * #188 — per-issue in-flight guard at the dispatch boundary.
 *
 * bowlsheet#233 ran runPipeline twice within 3 seconds for the same issue
 * (two pre-flight cost estimates, overlapping stage starts, races on the
 * same context files and worktree): IssueQueueService fires onItemAdded
 * from three entrypoints and nothing on the TS side stopped a second
 * fillSlots pass from dispatching an issue that already had a live slot.
 *
 * fillSlots must SKIP (not re-enqueue) any dequeued item whose issue
 * already has a live slot or an in-flight reservation.
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
    showErrorMessage: vi.fn().mockResolvedValue(undefined),
    showWarningMessage: vi.fn().mockResolvedValue(undefined),
    showInformationMessage: vi.fn().mockResolvedValue(undefined),
  },
  commands: { executeCommand: vi.fn().mockResolvedValue(undefined) },
}));

vi.mock("../../src/utils/WorktreeManager", () => ({
  WorktreeManager: vi.fn(function () {
    return {
      create: vi.fn(),
      cleanup: vi.fn().mockResolvedValue(undefined),
      cleanupOrphans: vi.fn().mockResolvedValue(0),
      cleanupAll: vi.fn().mockResolvedValue(undefined),
      listActive: vi.fn().mockResolvedValue([]),
      getRepoRoot: vi.fn().mockReturnValue("/test-repo"),
    };
  }),
}));

vi.mock("../../src/utils/incrediConfig", () => ({
  getConcurrentPipelineConfig: vi
    .fn()
    .mockReturnValue({ maxConcurrent: 2, worktreeBase: ".worktrees" }),
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      autonomousStatus: vi.fn().mockResolvedValue({ status: "running" }),
      autonomousPause: vi.fn().mockResolvedValue(undefined),
    }),
  },
}));

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";

function makeManager(dequeueBatches: any[][]) {
  let call = 0;
  const dequeueSpy = vi.fn().mockImplementation(async () => dequeueBatches[call++] ?? []);
  const enqueueSpy = vi.fn().mockResolvedValue(null);
  const warnSpy = vi.fn();
  const manager = new ConcurrentPipelineManager(
    "/test-repo",
    {
      dequeueIndependent: dequeueSpy,
      updateActiveSlots: vi.fn().mockResolvedValue(undefined),
      drainBlockedSuccessors: vi.fn().mockResolvedValue([]),
      enqueue: enqueueSpy,
      clear: vi.fn().mockResolvedValue(undefined),
      getQueue: vi.fn().mockResolvedValue({ items: [], status: "idle" }),
    } as any,
    vi.fn() as any,
    {
      info: vi.fn(),
      warn: warnSpy,
      error: vi.fn(),
      debug: vi.fn(),
      getChannel: vi.fn(),
    } as any,
    { maxConcurrent: 2, worktreeBase: ".worktrees" }
  );
  return { manager, dequeueSpy, enqueueSpy, warnSpy };
}

describe("ConcurrentPipelineManager.fillSlots — duplicate dispatch guard (#188)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("skips a dequeued item whose issue already has a live slot — without re-enqueueing", async () => {
    const { manager, enqueueSpy, warnSpy } = makeManager([
      [{ issueNumber: 233, title: "dup", labels: [] }],
    ]);

    // Simulate the live slot the first dispatch created.
    (manager as any).slots.set(233, { index: 0, issueNumber: 233, title: "dup" });

    const started = await manager.fillSlots();

    expect(started).toBe(0);
    // Skipped, NOT failed: a duplicate must never be re-enqueued (the issue
    // is already being worked) and never counted as a start.
    expect(enqueueSpy).not.toHaveBeenCalled();
    expect(warnSpy).toHaveBeenCalledWith(
      expect.stringContaining("duplicate dispatch"),
      expect.objectContaining({ issueNumber: 233, hasLiveSlot: true })
    );
  });

  it("skips a dequeued item whose issue has an in-flight reservation", async () => {
    const { manager, enqueueSpy, warnSpy } = makeManager([
      [{ issueNumber: 233, title: "dup", labels: [] }],
    ]);

    (manager as any).reservedSlots.set(233, { index: 0, repo: "" });

    const started = await manager.fillSlots();

    expect(started).toBe(0);
    expect(enqueueSpy).not.toHaveBeenCalled();
    expect(warnSpy).toHaveBeenCalledWith(
      expect.stringContaining("duplicate dispatch"),
      expect.objectContaining({ issueNumber: 233, hasReservation: true })
    );
  });
});
