/**
 * HeadlessOrchestrator.queueAutoStartRelease.test.ts
 *
 * #2397: the main orchestrator's queue auto-start dequeues an item, which
 * marks it `processing` in the daemon's queue, and nothing released it. The
 * issue then could not be queued again (the queue dedups by repository and
 * issue), Remove from Queue refused it and a remote trigger read `busy` until
 * the window reloaded. The orchestrator now sends `queue.complete` for the
 * item, exactly once, on every path where its run ends or never starts.
 *
 * The queue here is a small model of the daemon's: a dequeue marks an item
 * processing, `queue.complete` removes the processing item of that repository
 * and issue, and an add is refused while the issue is still queued. "Can be
 * queued again" is that add succeeding.
 */
import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";

vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "fable", source: "default" }),
}));

const REPO = "nightgauge/acmeapp";

interface Item {
  repo: string;
  issueNumber: number;
  status: "pending" | "processing";
}

/** The daemon queue's dedup, dequeue and complete rules, in miniature. */
class FakeQueue {
  items: Item[] = [];
  autoStartDelay = 0;
  complete = vi.fn(async (repo: string, issueNumber: number) => {
    const i = this.items.findIndex(
      (it) => it.status === "processing" && it.repo === repo && it.issueNumber === issueNumber
    );
    if (i >= 0) this.items.splice(i, 1);
  });
  dequeue = vi.fn(async () => {
    const next = this.items.find((it) => it.status === "pending");
    if (!next) return null;
    next.status = "processing";
    return { issueNumber: next.issueNumber, title: "t", repoName: next.repo };
  });
  onPipelineComplete = vi.fn(async () => {});
  getConfig = () => ({ autoStartDelay: this.autoStartDelay });

  /** QueueAdd: refused while the repository's issue is still in the queue. */
  add(repo: string, issueNumber: number): boolean {
    if (this.items.some((it) => it.repo === repo && it.issueNumber === issueNumber)) return false;
    this.items.push({ repo, issueNumber, status: "pending" });
    return true;
  }
}

type Internals = {
  handleQueueAutoStart: (success: boolean, completedIssueNumber: number) => Promise<void>;
  startNextQueuedIssue: (item: Record<string, unknown>) => Promise<void>;
  runPipeline: (...args: unknown[]) => Promise<unknown>;
  stop: () => void;
  setQueueService: (q: unknown) => void;
};

function makeOrch(): Internals {
  const orch = new HeadlessOrchestrator(
    null as never,
    { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() } as unknown as Logger,
    { contextFileWaitMs: 0 } as never
  );
  vi.spyOn(
    orch as never as { getWorkingDirectory: () => string },
    "getWorkingDirectory"
  ).mockReturnValue("/tmp/ws");
  return orch as unknown as Internals;
}

describe("HeadlessOrchestrator — the auto-started queue item is released (#2397)", () => {
  let orch: Internals;
  let queue: FakeQueue;

  beforeEach(() => {
    vi.clearAllMocks();
    orch = makeOrch();
    queue = new FakeQueue();
    orch.setQueueService(queue);
    queue.add(REPO, 9);
  });

  it("when the run finishes: released by the terminal hook, before the next dequeue", async () => {
    const order: string[] = [];
    queue.complete.mockImplementation(async (repo: string, n: number) => {
      order.push(`complete #${n}`);
      const i = queue.items.findIndex(
        (it) => it.status === "processing" && it.repo === repo && it.issueNumber === n
      );
      if (i >= 0) queue.items.splice(i, 1);
    });
    queue.dequeue.mockImplementation(async () => {
      order.push("dequeue");
      const next = queue.items.find((it) => it.status === "pending");
      if (!next) return null;
      next.status = "processing";
      return { issueNumber: next.issueNumber, title: "t", repoName: next.repo };
    });
    // runPipeline's every terminal path calls handleQueueAutoStart for its issue.
    vi.spyOn(orch, "runPipeline").mockImplementation(async (issueNumber: unknown) => {
      await orch.handleQueueAutoStart(false, issueNumber as number);
      return { success: false };
    });

    await orch.handleQueueAutoStart(true, 1);

    expect(queue.complete).toHaveBeenCalledTimes(1);
    expect(queue.complete).toHaveBeenCalledWith(REPO, 9);
    // Started #9 (dequeue), its end released #9, then looked for the next one.
    expect(order).toEqual(["dequeue", "complete #9", "dequeue"]);
    expect(queue.add(REPO, 9)).toBe(true);
  });

  it("when the run ends on a path that never reaches the terminal hook", async () => {
    vi.spyOn(orch, "runPipeline").mockResolvedValue({ success: false });

    await orch.handleQueueAutoStart(true, 1);

    expect(queue.complete).toHaveBeenCalledTimes(1);
    expect(queue.complete).toHaveBeenCalledWith(REPO, 9);
    expect(queue.add(REPO, 9)).toBe(true);
  });

  it("when the start throws", async () => {
    vi.spyOn(orch, "startNextQueuedIssue").mockRejectedValue(new Error("state service down"));

    await orch.handleQueueAutoStart(true, 1);

    expect(queue.complete).toHaveBeenCalledTimes(1);
    expect(queue.complete).toHaveBeenCalledWith(REPO, 9);
    expect(queue.add(REPO, 9)).toBe(true);
  });

  it("when a stop lands during the auto-start delay: not started, and released at once", async () => {
    queue.autoStartDelay = 60_000;
    const start = vi.spyOn(orch, "startNextQueuedIssue").mockResolvedValue();

    const pending = orch.handleQueueAutoStart(true, 1);
    await vi.waitFor(() => expect(queue.dequeue).toHaveBeenCalled());
    orch.stop();
    await pending;

    expect(start).not.toHaveBeenCalled();
    expect(queue.complete).toHaveBeenCalledTimes(1);
    expect(queue.complete).toHaveBeenCalledWith(REPO, 9);
    expect(queue.add(REPO, 9)).toBe(true);
  });

  it("a successor run of the same issue keeps its own processing mark", async () => {
    // While #9 runs, the operator queues it again; the terminal hook releases
    // the first run's item and dequeues the second, and the first auto-start's
    // backstop must not release the successor's mark.
    let runs = 0;
    vi.spyOn(orch, "runPipeline").mockImplementation(async (issueNumber: unknown) => {
      runs++;
      if (runs === 1) {
        expect(queue.add(REPO, 9)).toBe(false); // still processing: deduplicated
        queue.items.push({ repo: REPO, issueNumber: 9, status: "pending" }); // a re-queue the daemon spares
        void orch.handleQueueAutoStart(true, issueNumber as number);
        await vi.waitFor(() => expect(queue.dequeue).toHaveBeenCalledTimes(2));
      }
      return { success: true };
    });

    await orch.handleQueueAutoStart(true, 1);
    await vi.waitFor(() => expect(runs).toBe(2));
    await vi.waitFor(() => expect(queue.complete).toHaveBeenCalledTimes(2));

    // One release per dequeue, never two for one item.
    expect(queue.complete).toHaveBeenCalledTimes(2);
    expect(queue.items).toEqual([]);
  });
});
