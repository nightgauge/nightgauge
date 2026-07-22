/**
 * #3300 — ConcurrentPipelineManager.fillSlots must consult the dispatch gate
 * BEFORE dequeuing items, and refuse to start new slots when the gate
 * returns a refusal reason. In-flight slots are unaffected; this only stops
 * NEW dispatches.
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

function makeManager(opts: { dequeue?: any[] } = {}): {
  manager: ConcurrentPipelineManager;
  dequeueSpy: ReturnType<typeof vi.fn>;
} {
  // Default to empty dequeue so fillSlots exits cleanly after the gate check
  // and dequeue call. We only need to assert WHETHER dequeue was reached, not
  // whether a slot actually started — the gate is the only thing under test
  // here.
  const dequeueSpy = vi.fn().mockResolvedValue(opts.dequeue ?? []);
  const manager = new ConcurrentPipelineManager(
    "/test-repo",
    {
      dequeueIndependent: dequeueSpy,
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
  return { manager, dequeueSpy };
}

describe("ConcurrentPipelineManager.fillSlots — dispatch gate (#3300)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("dequeues normally when no dispatch gate is set", async () => {
    const { manager, dequeueSpy } = makeManager();
    await manager.fillSlots();
    expect(dequeueSpy).toHaveBeenCalled();
  });

  it("dequeues normally when the dispatch gate returns null (allow)", async () => {
    const { manager, dequeueSpy } = makeManager();
    manager.setDispatchGate(() => null);
    await manager.fillSlots();
    expect(dequeueSpy).toHaveBeenCalled();
  });

  it("does NOT dequeue when the dispatch gate returns a refusal reason", async () => {
    const { manager, dequeueSpy } = makeManager();
    manager.setDispatchGate(() => "extension stale on critical paths");
    const started = await manager.fillSlots();
    expect(started).toBe(0);
    expect(dequeueSpy).not.toHaveBeenCalled();
  });

  it("recovers when the gate flips from refusal to allow on a later call", async () => {
    const { manager, dequeueSpy } = makeManager();
    let refusal: string | null = "blocked: stale";
    manager.setDispatchGate(() => refusal);

    await manager.fillSlots();
    expect(dequeueSpy).not.toHaveBeenCalled();

    refusal = null; // user refreshed the extension
    await manager.fillSlots();
    expect(dequeueSpy).toHaveBeenCalled();
  });

  it("setDispatchGate(null) removes any prior gate", async () => {
    const { manager, dequeueSpy } = makeManager();
    manager.setDispatchGate(() => "blocked");
    manager.setDispatchGate(null);
    await manager.fillSlots();
    expect(dequeueSpy).toHaveBeenCalled();
  });
});
