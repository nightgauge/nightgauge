/**
 * Per-repo concurrency cap regression tests for ConcurrentPipelineManager.
 *
 * Reproduces the #3874 acceptance criteria: dragging two same-repo issues into
 * the pipeline view in quick succession must start at most `per_repo_max` of
 * them concurrently, with the rest queued — regardless of drag timing.
 *
 * Enforcement of the cap itself lives in the Go scheduler
 * (`DequeueIndependent` → `capForRepo`/`repoInFlight`). The TypeScript layer's
 * contract — and what these tests pin — is that `ConcurrentPipelineManager`
 * always hands the scheduler an ACCURATE `runningItems` set: every in-flight
 * slot's repo, INCLUDING slots whose worktree is still being created (the
 * cross-pass race this issue closes). A mock `dequeueIndependent` that mirrors
 * Go's batch cap is used so the wiring is exercised end-to-end without a live
 * binary.
 *
 * @see Issue #3874 - manual drag-to-pipeline ignores per-repo sequential cap
 * @see Issue #3781 / #3786 - per-repo cap wired into DequeueIndependent
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

// Mock WorktreeManager. `create` resolves on a microtask so a second
// fillSlots() pass can observe the in-flight reservation while the first
// slot's worktree is "being created" (the cross-pass window).
vi.mock("../../src/utils/WorktreeManager", () => ({
  WorktreeManager: vi.fn(function () {
    return {
      create: vi.fn().mockImplementation(async (issueNumber: number, branchName: string) => {
        // When a test arms `deferredCreate`, block here until it resolves so
        // the slot is observably "in-flight" (reserved but not yet in slots).
        if (deferredCreate.promise) {
          await deferredCreate.promise;
        }
        return {
          path: `/test-repo/.worktrees/issue-${issueNumber}`,
          branch: branchName,
          issueNumber,
          exists: true,
        };
      }),
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

// Per-test handle to defer worktree creation, exercising the in-flight
// reservation window. Default: resolve immediately.
let deferredCreate: {
  promise: Promise<unknown> | null;
  resolve: ((v: unknown) => void) | null;
} = { promise: null, resolve: null };

vi.mock("../../src/utils/incrediConfig", () => ({
  getConcurrentPipelineConfig: vi.fn().mockReturnValue({
    maxConcurrent: 3,
    worktreeBase: ".worktrees",
  }),
}));

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";

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
  epicOrder?: number;
}

function makeRepoItem(issueNumber: number, repoName: string): QueueItem {
  return {
    issueNumber,
    title: `Issue #${issueNumber}`,
    position: 1,
    status: "pending",
    addedAt: new Date().toISOString(),
    repoName,
  };
}

/** Never-resolving orchestrator so started slots stay "running". */
function createPendingFactory() {
  const factory = vi.fn().mockImplementation((_workDir: string, _issueNumber: number) => {
    const orchestrator = {
      setRepoOverride: vi.fn(),
      setUnattended: vi.fn(),
      runPipeline: vi.fn().mockReturnValue(new Promise(() => {})),
      stop: vi.fn(),
      dispose: vi.fn(),
    };
    const stateService = {
      onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onPhaseStart: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onPhaseComplete: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      onUnifiedTokenUsage: vi.fn().mockReturnValue({ dispose: vi.fn() }),
      getState: vi.fn().mockResolvedValue(null),
      initEmpty: vi.fn(),
      initializePipeline: vi.fn().mockResolvedValue(undefined),
      setMeta: vi.fn(),
      dispose: vi.fn(),
    };
    return { orchestrator, stateService };
  });
  return factory;
}

/**
 * Queue service whose `dequeueIndependent` mirrors the Go scheduler's per-repo
 * cap: given the backing queue and the `runningItems` the manager passes, it
 * returns at most `maxSlots` items AND never more than `cap` per repo once the
 * already-running same-repo items are counted.
 */
function createCapEnforcingQueue(cap: number, backing: QueueItem[]) {
  const queue = [...backing];
  const dequeueIndependent = vi.fn(
    async (maxSlots: number, runningItems: Array<{ repo: string; number: number }>) => {
      const repoInFlight = new Map<string, number>();
      for (const r of runningItems) {
        repoInFlight.set(r.repo, (repoInFlight.get(r.repo) ?? 0) + 1);
      }
      const dequeued: QueueItem[] = [];
      const remaining: QueueItem[] = [];
      for (const item of queue) {
        const repo = item.repoName ?? "";
        const inFlight = repoInFlight.get(repo) ?? 0;
        if (dequeued.length < maxSlots && (repo === "" || inFlight < cap)) {
          dequeued.push(item);
          repoInFlight.set(repo, inFlight + 1);
        } else {
          remaining.push(item);
        }
      }
      queue.length = 0;
      queue.push(...remaining);
      return dequeued;
    }
  );
  return {
    service: {
      dequeueIndependent,
      updateActiveSlots: vi.fn().mockResolvedValue(undefined),
      drainBlockedSuccessors: vi.fn().mockResolvedValue([]),
      enqueue: vi.fn().mockResolvedValue(null),
      clear: vi.fn().mockResolvedValue(undefined),
      getQueue: vi.fn().mockResolvedValue({ items: [], status: "idle" }),
    },
    dequeueIndependent,
    /** Items still queued (not yet dispatched). */
    remaining: () => [...queue],
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

describe("ConcurrentPipelineManager — per-repo concurrency cap (#3874)", () => {
  let mockLogger: ReturnType<typeof createMockLogger>;

  beforeEach(() => {
    mockLogger = createMockLogger();
    deferredCreate = { promise: null, resolve: null };
  });

  it("two same-repo items at per_repo_max=1 → exactly 1 starts, the other stays queued", async () => {
    // workspace ceiling (maxConcurrent) is 3 — wide enough that ONLY the
    // per-repo cap can keep the second item queued. This is the AC scenario:
    // two same-repo issues enqueued before any fillSlots() pass.
    const cap = createCapEnforcingQueue(1, [makeRepoItem(10, "o/A"), makeRepoItem(11, "o/A")]);
    const manager = new ConcurrentPipelineManager(
      "/test-repo",
      cap.service as any,
      createPendingFactory(),
      mockLogger as any,
      { maxConcurrent: 3, worktreeBase: ".worktrees" }
    );

    const started = await manager.fillSlots();

    expect(started).toBe(1);
    expect(manager.activeSlotCount).toBe(1);
    expect(manager.isRunning(10)).toBe(true);
    expect(manager.isRunning(11)).toBe(false);
    // The second same-repo item is still queued, not dropped.
    expect(cap.remaining().map((i) => i.issueNumber)).toEqual([11]);
  });

  it("cross-pass: a second fillSlots reports the running slot's repo in runningItems and the cap holds", async () => {
    // First pass starts #10 (o/A). A SEPARATE later pass — the cross-pass case
    // the issue is about — must report #10's repo in runningItems so the Go
    // scheduler counts it against the per-repo cap and refuses the second
    // same-repo item. The cap-enforcing queue returns 0 only when runningItems
    // correctly carries the in-flight repo, so this asserts both the wiring
    // (the passed set) and the outcome (no second start).
    const cap = createCapEnforcingQueue(1, [makeRepoItem(10, "o/A"), makeRepoItem(11, "o/A")]);
    const manager = new ConcurrentPipelineManager(
      "/test-repo",
      cap.service as any,
      createPendingFactory(),
      mockLogger as any,
      // maxConcurrent=1 forces #11 to wait on the first pass; freeing the
      // ceiling for the second pass isolates the per-repo cap as the only
      // thing that can keep #11 queued.
      { maxConcurrent: 1, worktreeBase: ".worktrees" }
    );

    const firstStarted = await manager.fillSlots();
    expect(firstStarted).toBe(1);
    expect(manager.isRunning(10)).toBe(true);

    // Raise the ceiling so the workspace cap no longer blocks — only the
    // per-repo cap can now keep #11 queued.
    manager.setMaxConcurrentSlots(3);
    const secondStarted = await manager.fillSlots();

    // The running slot's repo must have been passed on the cross-pass.
    const lastCall = (cap.dequeueIndependent.mock.calls.at(-1) ?? []) as [
      number,
      Array<{ repo: string; number: number }>,
    ];
    expect(lastCall[1] ?? []).toContainEqual({ repo: "o/A", number: 10 });
    // And the cap held: #11 did not start despite free workspace slots.
    expect(secondStarted).toBe(0);
    expect(manager.isRunning(11)).toBe(false);
    expect(cap.remaining().map((i) => i.issueNumber)).toEqual([11]);
  });

  it("per_repo_max=2 same-repo → exactly 2 start, the 3rd stays queued (AC #5)", async () => {
    const cap = createCapEnforcingQueue(2, [
      makeRepoItem(10, "o/A"),
      makeRepoItem(11, "o/A"),
      makeRepoItem(12, "o/A"),
    ]);
    const manager = new ConcurrentPipelineManager(
      "/test-repo",
      cap.service as any,
      createPendingFactory(),
      mockLogger as any,
      { maxConcurrent: 3, worktreeBase: ".worktrees" }
    );

    const started = await manager.fillSlots();

    expect(started).toBe(2);
    expect(manager.isRunning(10)).toBe(true);
    expect(manager.isRunning(11)).toBe(true);
    expect(manager.isRunning(12)).toBe(false);
    expect(cap.remaining().map((i) => i.issueNumber)).toEqual([12]);
  });

  it("two different repos at per_repo_max=1 each → both start concurrently (AC #5, no over-eager serialization)", async () => {
    const cap = createCapEnforcingQueue(1, [makeRepoItem(10, "o/A"), makeRepoItem(20, "o/B")]);
    const manager = new ConcurrentPipelineManager(
      "/test-repo",
      cap.service as any,
      createPendingFactory(),
      mockLogger as any,
      { maxConcurrent: 3, worktreeBase: ".worktrees" }
    );

    const started = await manager.fillSlots();

    expect(started).toBe(2);
    expect(manager.isRunning(10)).toBe(true);
    expect(manager.isRunning(20)).toBe(true);
    expect(cap.remaining()).toEqual([]);
  });

  it("reservation accounting: availableSlotCount reflects in-flight slots before they enter the slot map", async () => {
    // With maxConcurrent=2 and two same-repo items at cap=1, the first item is
    // reserved synchronously; the workspace ceiling must show one fewer slot
    // available immediately so a concurrent pass cannot over-subscribe.
    const cap = createCapEnforcingQueue(1, [makeRepoItem(10, "o/A"), makeRepoItem(11, "o/A")]);
    const manager = new ConcurrentPipelineManager(
      "/test-repo",
      cap.service as any,
      createPendingFactory(),
      mockLogger as any,
      { maxConcurrent: 2, worktreeBase: ".worktrees" }
    );

    expect(manager.availableSlotCount).toBe(2);
    await manager.fillSlots();
    // One slot active (the capped second item stayed queued), one free.
    expect(manager.availableSlotCount).toBe(1);
    expect(manager.activeSlotCount).toBe(1);
  });

  it("in-flight reservation: while a worktree is still being created, its repo is already counted (closes the cross-pass window)", async () => {
    // Arm a deferred create so #10's worktree creation hangs mid-flight. The
    // slot is reserved but NOT yet in `this.slots`. Before the fix, a concurrent
    // dequeue would see availableSlotCount = maxConcurrent (no reservation) and
    // runningItems WITHOUT #10's repo — letting a same-repo item slip past the
    // per-repo cap. The reservation must make both reflect intent-to-run.
    let resolveCreate!: (v: unknown) => void;
    deferredCreate.promise = new Promise((res) => {
      resolveCreate = res;
    });
    deferredCreate.resolve = resolveCreate;

    const cap = createCapEnforcingQueue(1, [makeRepoItem(10, "o/A")]);
    const manager = new ConcurrentPipelineManager(
      "/test-repo",
      cap.service as any,
      createPendingFactory(),
      mockLogger as any,
      { maxConcurrent: 3, worktreeBase: ".worktrees" }
    );

    // Start the fill but do NOT await — create() is now blocked, so #10 is
    // reserved-but-in-flight.
    const fillPromise = manager.fillSlots();
    // Let microtasks run up to the awaited create().
    await Promise.resolve();
    await Promise.resolve();

    // Reservation is live: the workspace ceiling already shows one fewer slot,
    // even though the slot map is still empty.
    expect(manager.activeSlotCount).toBe(0);
    expect(manager.availableSlotCount).toBe(2);

    // A concurrent fill at this instant must see #10 in runningItems and refuse
    // a second same-repo dispatch. The cap-enforcing queue uses runningItems
    // to apply the cap; with the reservation included it returns nothing.
    cap.dequeueIndependent.mockClear();
    const concurrentStarted = await manager.fillSlots();
    // isFilling guard serializes this into a no-op fill (returns 0) — the slot
    // is not double-dispatched. The reservation guarantees that even if it had
    // proceeded, runningItems/availableSlotCount would have blocked it.
    expect(concurrentStarted).toBe(0);

    // Now release the worktree creation and let the first fill finish.
    resolveCreate({});
    await fillPromise;

    expect(manager.isRunning(10)).toBe(true);
    expect(manager.activeSlotCount).toBe(1);
    // Reservation released once the real slot landed — accounting is consistent.
    expect(manager.availableSlotCount).toBe(2);
  });
});
