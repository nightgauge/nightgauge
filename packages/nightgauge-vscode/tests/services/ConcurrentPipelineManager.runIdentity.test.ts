/**
 * ConcurrentPipelineManager.runIdentity.test.ts
 *
 * ADR-017 step 3 (#370) — the dispatch "generation" becomes THE run identity.
 *
 * #307 minted a per-dispatch token (`${issue}:${counter}:${now}`) and kept it
 * deliberately extension-internal, because Go's runtime registry was keyed by
 * bare issue number and re-keying it was ADR-scale work. This step is that
 * work's third instalment: the token becomes a UUIDv7, one value now reaches
 * the reservation, the slot AND the slot's own `PipelineStateService` (which
 * puts it on every `pipeline.*` call), and the abort tombstone keys on it.
 *
 * What these tests pin:
 *   1. the minted id is a canonical run identity — the Go side will key on it
 *      and use it as a `runtime-{issue}-{runId}.json` filename component, so
 *      a value the Go validator would reject must never be minted here;
 *   2. ONE id reaches the reservation, the slot and the slot's state service
 *      (via `beginRun`) — two ids would put the tombstone and the wire in
 *      disagreement about which run a late event belongs to;
 *   3. the force-clear tombstone holds run ids and has NO release path;
 *   4. force-clearing a predecessor does NOT tombstone a live successor of the
 *      same issue — the property the whole re-key exists for.
 *
 * `pipeline.abandonRun` (telling the SERVER a force-cleared dispatch is over)
 * lands in step 6; there is deliberately nothing about it here.
 *
 * Harness: ConcurrentPipelineManager.abortTimeout.test.ts's controllable
 * factory + wedged worktree gate, which is what drives the deadline into both
 * force-clear arms.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

const { worktreeGate } = vi.hoisted(() => ({
  worktreeGate: { blockCreate: null as Promise<void> | null },
}));

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
      create: vi.fn().mockImplementation(async (issueNumber: number, branchName: string) => {
        if (worktreeGate.blockCreate) await worktreeGate.blockCreate;
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
      getWorktreePath: vi.fn((n: number) => `/test-repo/.worktrees/issue-${n}`),
    };
  }),
}));

vi.mock("../../src/utils/incrediConfig", () => ({
  getConcurrentPipelineConfig: vi.fn().mockReturnValue({
    maxConcurrent: 2,
    worktreeBase: ".worktrees",
  }),
}));

vi.mock("../../src/utils/skillRunner", () => ({
  killAllActiveProcesses: vi.fn(),
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      attentionRaise: vi.fn().mockResolvedValue({ outcome: "created", id: "dr_test" }),
    }),
  },
}));

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";
import { isRunIdentity } from "../../src/services/ipcNotifyParams";

function makeQueueItem(issueNumber: number) {
  return {
    issueNumber,
    title: `Issue #${issueNumber}`,
    position: 1,
    status: "pending",
    addedAt: new Date().toISOString(),
    repoName: "octocat/acme",
  };
}

function createMockQueueService() {
  return {
    dequeueIndependent: vi.fn().mockResolvedValue([]),
    updateActiveSlots: vi.fn().mockResolvedValue(undefined),
    drainBlockedSuccessors: vi.fn().mockResolvedValue([]),
    enqueue: vi.fn().mockResolvedValue(null),
    clear: vi.fn().mockResolvedValue(undefined),
    getQueue: vi.fn().mockResolvedValue({ items: [], status: "idle" }),
    complete: vi.fn().mockResolvedValue(undefined),
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

interface SlotHandle {
  resolveRun: (result: any) => void;
  beginRun: ReturnType<typeof vi.fn>;
}

/** Factory whose runPipeline promise is held open until the test settles it. */
function createControllableFactory() {
  const handles = new Map<number, SlotHandle[]>();
  const factory = vi.fn().mockImplementation((_workDir: string, issueNumber: number) => {
    let resolveRun!: (result: any) => void;
    const runPromise = new Promise<any>((res) => {
      resolveRun = res;
    });
    const beginRun = vi.fn();
    const list = handles.get(issueNumber) ?? [];
    list.push({ resolveRun, beginRun });
    handles.set(issueNumber, list);
    return {
      orchestrator: {
        setWorktreeOverride: vi.fn(),
        setRepoOverride: vi.fn(),
        setRunRepoRoot: vi.fn(),
        setUnattended: vi.fn(),
        runPipeline: vi.fn().mockReturnValue(runPromise),
        stop: vi.fn(),
        dispose: vi.fn(),
      },
      stateService: {
        onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
        onPhaseStart: vi.fn().mockReturnValue({ dispose: vi.fn() }),
        onPhaseComplete: vi.fn().mockReturnValue({ dispose: vi.fn() }),
        onUnifiedTokenUsage: vi.fn().mockReturnValue({ dispose: vi.fn() }),
        getState: vi.fn().mockResolvedValue({ tokens: { estimated_cost_usd: 1.25 } }),
        beginRun,
        endRun: vi.fn(),
        getRunId: vi.fn().mockReturnValue(null),
        initEmpty: vi.fn(),
        initializePipeline: vi.fn().mockResolvedValue(undefined),
        setMeta: vi.fn(),
        dispose: vi.fn(),
      },
    };
  });
  return { factory, handles };
}

/** The run ids the manager tombstoned, read off its own force-clear log lines. */
function tombstonedRunIds(logger: ReturnType<typeof createMockLogger>): string[] {
  return logger.warn.mock.calls
    .filter(([message]) => String(message).startsWith("Force-clearing"))
    .map(([, meta]) => (meta as { runId: string }).runId);
}

describe("ConcurrentPipelineManager — the dispatch generation is the run identity (#370)", () => {
  let mockQueue: ReturnType<typeof createMockQueueService>;
  let mockLogger: ReturnType<typeof createMockLogger>;
  let callbacks: {
    onSlotFailed: ReturnType<typeof vi.fn>;
    onSlotCompleted: ReturnType<typeof vi.fn>;
    onSlotDeferred: ReturnType<typeof vi.fn>;
    onSlotCleaned: ReturnType<typeof vi.fn>;
  };

  beforeEach(() => {
    vi.useFakeTimers();
    mockQueue = createMockQueueService();
    mockLogger = createMockLogger();
    callbacks = {
      onSlotFailed: vi.fn(),
      onSlotCompleted: vi.fn(),
      onSlotDeferred: vi.fn(),
      onSlotCleaned: vi.fn(),
    };
    worktreeGate.blockCreate = null;
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  function newManager(factory: any) {
    const manager = new ConcurrentPipelineManager(
      "/test-repo",
      mockQueue as any,
      factory,
      mockLogger as any,
      { maxConcurrent: 2, worktreeBase: ".worktrees" }
    );
    manager.setCallbacks(callbacks as any);
    return manager;
  }

  it("mints a canonical run identity and hands it to the slot's state service", async () => {
    const { factory, handles } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(370)]);
    await manager.fillSlots();

    const beginRun = handles.get(370)![0].beginRun;
    expect(beginRun).toHaveBeenCalledTimes(1);
    const [runId, repo, issueNumber] = beginRun.mock.calls[0] as [string, string, number];

    // The Go side keys its registry on this value and interpolates it into a
    // filename. A non-canonical id would be refused as `run_id_invalid` at
    // step 4 — i.e. a run that cannot book anything.
    expect(isRunIdentity(runId)).toBe(true);
    expect(repo).toBe("octocat/acme");
    expect(issueNumber).toBe(370);
  });

  it("mints a DISTINCT identity per dispatch", async () => {
    const { factory, handles } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(370), makeQueueItem(371)]);
    await manager.fillSlots();

    const a = handles.get(370)![0].beginRun.mock.calls[0][0] as string;
    const b = handles.get(371)![0].beginRun.mock.calls[0][0] as string;
    expect(a).not.toBe(b);
    expect(isRunIdentity(a)).toBe(true);
    expect(isRunIdentity(b)).toBe(true);
  });

  it("the tombstone records the SAME id the slot's state service was given", async () => {
    const { factory, handles } = createControllableFactory();
    const manager = newManager(factory);

    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(370)]);
    await manager.fillSlots();
    const installed = handles.get(370)![0].beginRun.mock.calls[0][0] as string;

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    // One id for the reservation, the slot, the tombstone and the wire. Two
    // would let a late event be "the same run" to one of them and "a dead
    // predecessor" to the other.
    expect(tombstonedRunIds(mockLogger)).toEqual([installed]);
  });

  it("tombstones a STRANDED RESERVATION under a canonical identity too", async () => {
    const { factory } = createControllableFactory();
    const manager = newManager(factory);

    // Wedge inside worktree creation: the dispatch never becomes a slot, so
    // the reservation arm of the funnel is the one that books it.
    worktreeGate.blockCreate = new Promise<void>(() => {});
    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(370)]);
    void manager.fillSlots();
    await vi.advanceTimersByTimeAsync(10);
    expect(manager.activeSlotCount).toBe(0);

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;

    const tombstoned = tombstonedRunIds(mockLogger);
    expect(tombstoned).toHaveLength(1);
    expect(isRunIdentity(tombstoned[0])).toBe(true);
  });

  it("has NO release path for a tombstone — permanence is the design", () => {
    // A tombstone that can be revoked expires exactly when the wedge is worst
    // (#307 round 1). Nothing on this class may hand an id back.
    const surface = [
      ...Object.getOwnPropertyNames(ConcurrentPipelineManager.prototype),
      ...Object.keys(
        new ConcurrentPipelineManager("/test-repo", {} as any, vi.fn(), {
          info: vi.fn(),
          warn: vi.fn(),
          error: vi.fn(),
          debug: vi.fn(),
        } as any)
      ),
    ];
    for (const name of surface) {
      expect(name).not.toMatch(/release|untombstone|clearForceCleared|forgetRun/i);
    }
  });

  it("force-clearing a predecessor does NOT tombstone the live successor", async () => {
    const { factory, handles } = createControllableFactory();
    const manager = newManager(factory);

    // Predecessor wedges and is force-cleared.
    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(370)]);
    await manager.fillSlots();
    const predecessorRunId = handles.get(370)![0].beginRun.mock.calls[0][0] as string;

    const abortPromise = manager.abortAll();
    await vi.advanceTimersByTimeAsync(31_000);
    await abortPromise;
    callbacks.onSlotFailed.mockClear();

    // The operator re-queues the SAME issue. Keyed by issue number, the
    // tombstone would swallow this run's outcome entirely — the run would
    // finish, book nothing, and hold its Go scheduler seat forever.
    mockQueue.dequeueIndependent.mockResolvedValueOnce([makeQueueItem(370)]);
    await manager.fillSlots();
    const successor = handles.get(370)![1];
    const successorRunId = successor.beginRun.mock.calls[0][0] as string;
    expect(successorRunId).not.toBe(predecessorRunId);

    successor.resolveRun({ success: true, totalDurationMs: 1000, stagesRun: [] });
    await manager.settleForTest(370);

    expect(callbacks.onSlotCompleted).toHaveBeenCalledTimes(1);
    expect(callbacks.onSlotCompleted.mock.calls[0][1]).toBe(370);
    // And the successor's id was never added to the tombstone set.
    expect(tombstonedRunIds(mockLogger)).toEqual([predecessorRunId]);
  });
});
