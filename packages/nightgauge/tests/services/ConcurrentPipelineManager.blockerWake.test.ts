/**
 * ConcurrentPipelineManager.blockerWake.test.ts
 *
 * #3487, Part G: successors held behind a partial-delivery no-op start when
 * that issue closes. Go refreshes blocker states only inside a dequeue, so the
 * fake queue does too: nothing starts here unless the manager dequeues.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";
import type { QueueItem } from "../../src/types/queue";
import { fakeCloneLayout } from "../helpers/cloneLayout";

vi.mock("vscode", () => ({
  EventEmitter: class {
    event = () => ({ dispose: () => {} });
    fire() {}
    dispose() {}
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
      create: async (issueNumber: number, branch: string) => ({
        path: `/test-repo/.worktrees/issue-${issueNumber}`,
        branch,
        issueNumber,
        exists: true,
      }),
      cleanup: async () => undefined,
      getRepoRoot: () => "/test-repo",
      getWorktreePath: (n: number) => `/test-repo/.worktrees/issue-${n}`,
    };
  }),
}));
vi.mock("../../src/utils/nightgaugeConfig", () => ({
  getConcurrentPipelineConfig: () => ({ maxConcurrent: 1 }),
}));
vi.mock("../../src/utils/inReviewGuard", () => ({
  moveToInReviewIfPRBacked: async () => "skipped",
}));
// No git and no forge: an empty answer is "no PR, not closed, nothing landed".
vi.mock("child_process", async (importActual) => {
  const answer = (...args: unknown[]) => {
    const done = args[args.length - 1] as (err: null, out: object) => void;
    done(null, { stdout: "", stderr: "" });
  };
  return { ...(await importActual<object>()), exec: answer, execFile: answer };
});
vi.mock("../../src/utils/preservedWip", () => ({
  listPreservedWip: async () => [],
  describePreservedWip: () => "",
}));
vi.mock("../../src/utils/prDetection", () => ({ getPRForIssue: async () => null }));
vi.mock("../../src/utils/failureComment", () => ({
  postFailureComment: async () => undefined,
  ARCHITECTURE_APPROVAL_REQUIRED_MARKER: "ARCHITECTURE APPROVAL REQUIRED",
  BLOCKED_DEPENDENCY_MARKER: "[blocked-dependency]",
  HOST_SLEPT_MARKER: "[host-slept]",
}));

const attention = vi.hoisted(() => ({ handlers: new Set<(data: unknown) => void>() }));
vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      gitComposeBranchName: async (n: number) => ({ name: `fix/${n}-wake` }),
      autonomousStatus: async () => ({ status: "stopped" }),
      on: (event: string, handler: (data: unknown) => void) => {
        if (event === "attention.event") attention.handlers.add(handler);
        return { dispose: () => attention.handlers.delete(handler) };
      },
    }),
  },
}));

const REPO = "nightgauge/nightgauge";
const BLOCKER = 3487;
const FIRST = 3488;
const SECOND = 3489;
/** The manager's poll interval. */
const POLL_MS = 30_000;
/** Longer than the debounced fill a queue change schedules (#2717). */
const DEBOUNCE_MS = 200;

const item = (issueNumber: number, extra: Partial<QueueItem> = {}): QueueItem => ({
  issueNumber,
  title: `issue ${issueNumber}`,
  position: 1,
  status: "pending",
  addedAt: "2026-11-22T00:00:00Z",
  repoName: REPO,
  ...extra,
});

/** An item queued behind the no-op issue; a blocker with no repo is in the item's. */
const successor = (issueNumber: number) =>
  item(issueNumber, {
    blockedBy: [{ number: BLOCKER, title: "no-op", url: "", state: "OPEN" }],
  });

function setup(initial: QueueItem[], maxConcurrent = 1) {
  let items = initial;
  let announce: (state: unknown) => void = () => {};
  const forge = new Map([[BLOCKER, "OPEN"]]);
  const config = { autoStart: true };
  const state = () => ({
    // As Go's queueStatusLocked: any paused item makes the whole queue paused.
    status: items.some((i) => i.status === "processing")
      ? "processing"
      : items.some((i) => i.status === "paused")
        ? "paused"
        : "waiting",
    items: items.map((i) => ({ ...i })),
  });
  const dequeue = vi.fn(async (slots: number) => {
    const ready = items
      .filter(
        (i) =>
          i.status === "pending" &&
          (i.blockedBy ?? []).every((b) => forge.get(b.number) === "CLOSED")
      )
      .slice(0, slots);
    for (const i of ready) i.status = "processing";
    return ready.map((i) => ({ ...i }));
  });
  const drain = vi.fn(async (): Promise<number[]> => []);
  const queue = {
    onQueueChanged: (listener: (s: unknown) => void) => {
      announce = listener;
      return { dispose: () => {} };
    },
    getConfig: () => config,
    getQueue: vi.fn(async () => state()),
    dequeueIndependent: dequeue,
    updateActiveSlots: async () => undefined,
    drainBlockedSuccessors: drain,
    complete: async (_repo: string, n: number) => {
      items = items.filter((i) => i.issueNumber !== n || i.status !== "processing");
    },
    remove: async (n: number) => {
      items = items.filter((i) => i.issueNumber !== n);
    },
    clear: async () => {
      items = [];
    },
    enqueue: async () => null,
  };

  const started: number[] = [];
  const finish = new Map<number, (result: unknown) => void>();
  const factory = () => ({
    orchestrator: {
      setWorktreeOverride: vi.fn(),
      setRunRepoRoot: vi.fn(),
      setRepoOverride: vi.fn(),
      setUnattended: vi.fn(),
      resolveRunRepoSlug: async () => REPO,
      runPipeline: (n: number) => {
        started.push(n);
        return new Promise((resolve) => finish.set(n, resolve));
      },
      stop: vi.fn(),
      dispose: vi.fn(),
    },
    stateService: {
      onStateChanged: () => ({ dispose: vi.fn() }),
      onPhaseStart: () => ({ dispose: vi.fn() }),
      onPhaseComplete: () => ({ dispose: vi.fn() }),
      onUnifiedTokenUsage: () => ({ dispose: vi.fn() }),
      getState: async () => null,
      beginRun: vi.fn(),
      endRun: vi.fn(),
      getRunId: () => null,
      initEmpty: vi.fn(),
      setMeta: vi.fn(),
      dispose: vi.fn(),
    },
  });
  const manager = new ConcurrentPipelineManager(
    "/test-repo",
    queue as never,
    factory as never,
    { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() } as never,
    { maxConcurrent }
  );

  return {
    manager,
    config,
    queue,
    dequeue,
    drain,
    started,
    pending: () => items.filter((i) => i.status === "pending").map((i) => i.issueNumber),
    closeOnForge: () => forge.set(BLOCKER, "CLOSED"),
    /** Announce the queue as it stands and let the debounced fill run. */
    async announce() {
      announce(state());
      await vi.advanceTimersByTimeAsync(DEBOUNCE_MS);
    },
    /** The daemon's push after a card was resolved. */
    async resolveCard({
      option = "close",
      verb = "issue.closeRemainder",
      repo = REPO,
      issue = BLOCKER,
      action = "resolved",
    } = {}) {
      const event = {
        action,
        request: {
          context: { repo, issue },
          options: [
            { id: "ack", verb: "noop" },
            { id: "close", verb },
          ],
          lifecycle: { state: "resolved", resolved: { option_id: option } },
        },
      };
      for (const handler of attention.handlers) handler(event);
      await vi.advanceTimersByTimeAsync(0);
    },
    end(issueNumber: number, result: Record<string, unknown>) {
      finish.get(issueNumber)?.({
        success: false,
        completedStages: [],
        skippedStages: [],
        deferredStages: [],
        failedStage: "feature-dev",
        totalDurationMs: 20_000,
        ...result,
      });
    },
  };
}

const NO_OP = {
  partialNoOp: {},
  terminalKind: "not_pipeline_actionable",
  error: new Error("[not-pipeline-actionable] wrote no changes"),
};

/** A queue holding only the successors of an issue that just ended as a no-op. */
async function afterNoOp() {
  const h = setup([item(BLOCKER), successor(FIRST), successor(SECOND)]);
  await h.manager.fillSlots();
  expect(h.started).toEqual([BLOCKER]);
  await h.announce();
  h.end(BLOCKER, NO_OP);
  await h.manager.settleForTest(BLOCKER);
  await h.announce();
  expect(h.pending()).toEqual([FIRST, SECOND]);
  return h;
}

describe("a closed blocker wakes the items queued behind it (#3487, Part G)", () => {
  let current: ConcurrentPipelineManager | undefined;
  const track = <T extends { manager: ConcurrentPipelineManager }>(h: T): T => {
    current = h.manager;
    return h;
  };

  beforeEach(() => {
    vi.useFakeTimers();
    attention.handlers.clear();
    fakeCloneLayout("/test-repo");
  });
  afterEach(() => {
    current?.dispose();
    vi.useRealTimers();
  });

  it("starts the first successor when the card is resolved with close, before any poll", async () => {
    const h = track(await afterNoOp());
    h.closeOnForge();
    await h.resolveCard();
    await vi.waitFor(() => expect(h.started).toEqual([BLOCKER, FIRST]));
    expect(h.pending()).toEqual([SECOND]);
  });

  it("starts the first successor within one poll of a close by hand", async () => {
    const h = track(await afterNoOp());
    h.closeOnForge();
    await vi.advanceTimersByTimeAsync(POLL_MS);
    await vi.waitFor(() => expect(h.started).toEqual([BLOCKER, FIRST]));
    expect(h.pending()).toEqual([SECOND]);
  });

  it("keeps a successor whose blocker is open queued across polls", async () => {
    const h = track(await afterNoOp());
    h.dequeue.mockClear();
    await vi.advanceTimersByTimeAsync(3 * POLL_MS);
    expect(h.dequeue).toHaveBeenCalledTimes(3);
    expect(h.started).toEqual([BLOCKER]);
    expect(h.pending()).toEqual([FIRST, SECOND]);
  });

  it("wakes successors found at activation", async () => {
    const h = track(setup([successor(FIRST)]));
    await h.manager.syncQueuedRemoteRuns();
    await vi.advanceTimersByTimeAsync(DEBOUNCE_MS);
    h.closeOnForge();
    await vi.advanceTimersByTimeAsync(POLL_MS);
    await vi.waitFor(() => expect(h.started).toEqual([FIRST]));
  });

  it("wakes a successor whose blocker is in another repository", async () => {
    const other = "nightgauge/other";
    const h = track(
      setup([
        item(FIRST, {
          blockedBy: [{ number: BLOCKER, title: "", url: "", state: "OPEN", repo: other }],
        }),
      ])
    );
    await h.announce();
    h.closeOnForge();
    await h.resolveCard({ repo: other.toUpperCase() });
    await vi.waitFor(() => expect(h.started).toEqual([FIRST]));
  });

  it.each([
    ["an ack", { option: "ack" }],
    ["an update", { action: "updated" }],
    ["another card's close", { verb: "issue.close" }],
    ["a close of another issue", { issue: 1 }],
    ["a close of the same number in another repository", { repo: "nightgauge/other" }],
  ])("does not wake at once on %s", async (_name, event) => {
    const h = track(await afterNoOp());
    h.dequeue.mockClear();
    await h.resolveCard(event);
    expect(h.dequeue).not.toHaveBeenCalled();
  });

  it("polls past a paused item beside the successor", async () => {
    const h = track(setup([item(1, { status: "paused" }), successor(FIRST)]));
    await h.announce();
    h.closeOnForge();
    await vi.advanceTimersByTimeAsync(POLL_MS);
    await vi.waitFor(() => expect(h.started).toEqual([FIRST]));
  });

  it("does not poll while a slot runs, and still takes the card's close", async () => {
    const h = track(setup([item(1), successor(FIRST)], 2));
    await h.manager.fillSlots();
    await h.announce();
    h.dequeue.mockClear();
    h.closeOnForge();
    await vi.advanceTimersByTimeAsync(2 * POLL_MS);
    expect(h.dequeue).not.toHaveBeenCalled();

    await h.resolveCard();
    await vi.waitFor(() => expect(h.started).toEqual([1, FIRST]));
  });

  it("does not start work a halt is about to remove", async () => {
    const h = track(setup([item(1), successor(FIRST)]));
    await h.manager.fillSlots();
    await h.announce();
    let drained: (issues: number[]) => void = () => {};
    h.drain.mockReturnValue(new Promise((resolve) => (drained = resolve)));
    h.end(1, { error: new Error("stage exited 1") });
    await vi.waitFor(() => expect(h.drain).toHaveBeenCalled());
    expect(h.manager.activeSlotCount).toBe(0);

    h.dequeue.mockClear();
    h.closeOnForge();
    await h.resolveCard();
    await vi.advanceTimersByTimeAsync(POLL_MS);
    expect(h.dequeue).not.toHaveBeenCalled();

    drained([]);
    await h.manager.settleForTest(1);
    await vi.advanceTimersByTimeAsync(POLL_MS);
    expect(h.started).toEqual([1]);
    expect(h.pending()).toEqual([]);
  });

  it.each([
    ["another fill", (m: ConcurrentPipelineManager) => m.fillSlots()],
    ["a resume", (m: ConcurrentPipelineManager) => m.resumeFilling()],
  ])("leaves a stop-after-current drain stopped until %s", async (_name, resume) => {
    const h = track(setup([item(BLOCKER), successor(FIRST)]));
    await h.manager.fillSlots();
    await h.announce();
    h.manager.pauseFilling();
    h.end(BLOCKER, NO_OP);
    await h.manager.settleForTest(BLOCKER);
    h.dequeue.mockClear();
    await h.resolveCard();
    await vi.advanceTimersByTimeAsync(2 * POLL_MS);
    expect(h.dequeue).not.toHaveBeenCalled();

    // Closed only after the resume, so the resume itself starts nothing.
    await resume(h.manager);
    h.closeOnForge();
    await vi.advanceTimersByTimeAsync(POLL_MS);
    await vi.waitFor(() => expect(h.started).toEqual([BLOCKER, FIRST]));
  });

  it("does not wake with autoStart off, and does once it is on again", async () => {
    const h = track(setup([successor(FIRST)]));
    h.config.autoStart = false;
    await h.announce();
    h.closeOnForge();
    await h.resolveCard();
    await vi.advanceTimersByTimeAsync(POLL_MS);
    expect(h.dequeue).not.toHaveBeenCalled();

    h.config.autoStart = true;
    await vi.advanceTimersByTimeAsync(POLL_MS);
    await vi.waitFor(() => expect(h.started).toEqual([FIRST]));
  });

  it("keeps polling after a queue read fails", async () => {
    const h = track(await afterNoOp());
    h.closeOnForge();
    h.queue.getQueue.mockRejectedValueOnce(new Error("socket closed"));
    await vi.advanceTimersByTimeAsync(POLL_MS);
    expect(h.pending()).toEqual([FIRST, SECOND]);
    await vi.advanceTimersByTimeAsync(POLL_MS);
    await vi.waitFor(() => expect(h.started).toEqual([BLOCKER, FIRST]));
  });

  it("stops polling once nothing waits on a blocker", async () => {
    const h = track(setup([successor(FIRST)]));
    await h.announce();
    await h.queue.clear();
    await vi.advanceTimersByTimeAsync(POLL_MS);
    h.queue.getQueue.mockClear();
    await vi.advanceTimersByTimeAsync(3 * POLL_MS);
    expect(h.queue.getQueue).not.toHaveBeenCalled();
  });

  it("stops both wakes on dispose, also during a poll's queue read", async () => {
    const h = track(await afterNoOp());
    h.dequeue.mockClear();
    h.queue.getQueue.mockImplementationOnce(async () => {
      h.manager.dispose();
      return { status: "waiting", items: [successor(FIRST)] };
    });
    await vi.advanceTimersByTimeAsync(POLL_MS);
    expect(vi.getTimerCount()).toBe(0);
    expect(attention.handlers.size).toBe(0);
    expect(h.dequeue).not.toHaveBeenCalled();
  });
});
