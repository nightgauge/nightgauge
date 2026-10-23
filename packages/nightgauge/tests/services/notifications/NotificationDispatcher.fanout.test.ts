import { describe, it, expect, vi, beforeEach } from "vitest";
import { FakeNotifier } from "./_helpers";

vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  workspace: {
    getConfiguration: vi.fn(() => ({ get: vi.fn() })),
  },
}));

import { NotificationDispatcher } from "../../../src/services/notifications/NotificationDispatcher";
import type { PipelineStateService } from "../../../src/services/PipelineStateService";

function makeLogger() {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as any;
}

const FAKE_STATE_SERVICE = {} as unknown as PipelineStateService;

// ─── Fan-out across 3 notifiers ────────────────────────────────────────────

describe("NotificationDispatcher.fanout — fan-out across N≥3 notifiers", () => {
  let logger: ReturnType<typeof makeLogger>;
  let n1: FakeNotifier;
  let n2: FakeNotifier;
  let n3: FakeNotifier;
  let dispatcher: NotificationDispatcher;

  beforeEach(() => {
    logger = makeLogger();
    n1 = new FakeNotifier();
    n2 = new FakeNotifier();
    n3 = new FakeNotifier();
    dispatcher = new NotificationDispatcher([n1, n2, n3], logger);
  });

  it("onPipelineStart delivers to all 3 notifiers", () => {
    const ctx = { issueNumber: 42, stage: "issue-pickup" };
    dispatcher.onPipelineStart(ctx);

    expect(n1.calls.onPipelineStart).toHaveLength(1);
    expect(n1.calls.onPipelineStart[0]).toEqual(ctx);
    expect(n2.calls.onPipelineStart).toHaveLength(1);
    expect(n2.calls.onPipelineStart[0]).toEqual(ctx);
    expect(n3.calls.onPipelineStart).toHaveLength(1);
    expect(n3.calls.onPipelineStart[0]).toEqual(ctx);
  });

  it("onPipelineUpdate delivers to all 3 notifiers", () => {
    const ctx = { issueNumber: 7 };
    dispatcher.onPipelineUpdate(ctx);

    expect(n1.calls.onPipelineUpdate).toHaveLength(1);
    expect(n2.calls.onPipelineUpdate).toHaveLength(1);
    expect(n3.calls.onPipelineUpdate).toHaveLength(1);
  });

  it("multiple events each reach all 3 notifiers independently", () => {
    dispatcher.onPipelineStart({ issueNumber: 1 });
    dispatcher.onPipelineStart({ issueNumber: 2 });
    dispatcher.onPipelineUpdate({ issueNumber: 1 });

    expect(n1.calls.onPipelineStart).toHaveLength(2);
    expect(n2.calls.onPipelineStart).toHaveLength(2);
    expect(n3.calls.onPipelineStart).toHaveLength(2);
    expect(n1.calls.onPipelineUpdate).toHaveLength(1);
    expect(n2.calls.onPipelineUpdate).toHaveLength(1);
    expect(n3.calls.onPipelineUpdate).toHaveLength(1);
  });
});

// ─── Error isolation ───────────────────────────────────────────────────────

describe("NotificationDispatcher.fanout — isolation: one failure does not block others", () => {
  let logger: ReturnType<typeof makeLogger>;

  beforeEach(() => {
    logger = makeLogger();
  });

  it("n1 throws onPipelineStart → n2 and n3 still receive the event", () => {
    const n1 = new FakeNotifier();
    n1.onStartThrow = new Error("n1 broke");
    const n2 = new FakeNotifier();
    const n3 = new FakeNotifier();
    const dispatcher = new NotificationDispatcher([n1, n2, n3], logger);

    dispatcher.onPipelineStart({ issueNumber: 42 });

    expect(n1.calls.onPipelineStart).toHaveLength(1);
    expect(n2.calls.onPipelineStart).toHaveLength(1);
    expect(n3.calls.onPipelineStart).toHaveLength(1);
    expect(logger.warn).toHaveBeenCalled();
  });

  it("n2 throws onPipelineUpdate → n1 and n3 still receive the event", () => {
    const n1 = new FakeNotifier();
    const n2 = new FakeNotifier();
    n2.onUpdateThrow = new Error("n2 update error");
    const n3 = new FakeNotifier();
    const dispatcher = new NotificationDispatcher([n1, n2, n3], logger);

    dispatcher.onPipelineUpdate({ issueNumber: 99 });

    expect(n1.calls.onPipelineUpdate).toHaveLength(1);
    expect(n2.calls.onPipelineUpdate).toHaveLength(1);
    expect(n3.calls.onPipelineUpdate).toHaveLength(1);
    expect(logger.warn).toHaveBeenCalled();
  });

  it("n1 initialize() rejects → n2 and n3 still initialize successfully", async () => {
    const n1 = new FakeNotifier();
    n1.initializeReject = new Error("init failed");
    const n2 = new FakeNotifier();
    const n3 = new FakeNotifier();
    const dispatcher = new NotificationDispatcher([n1, n2, n3], logger);

    await expect(dispatcher.initialize()).resolves.toBeUndefined();

    expect(n1.calls.initialize).toBe(1);
    expect(n2.calls.initialize).toBe(1);
    expect(n3.calls.initialize).toBe(1);
    expect(logger.warn).toHaveBeenCalled();
  });

  it("dispose() calls all 3 notifiers even when n2 throws", () => {
    const n1 = new FakeNotifier();
    const n2 = new FakeNotifier();
    n2.disposeThrow = new Error("n2 dispose failed");
    const n3 = new FakeNotifier();
    const dispatcher = new NotificationDispatcher([n1, n2, n3], logger);

    dispatcher.dispose();

    expect(n1.calls.dispose).toBe(1);
    expect(n2.calls.dispose).toBe(1);
    expect(n3.calls.dispose).toBe(1);
    expect(logger.warn).toHaveBeenCalled();
  });

  it("subscribeToSlot reaches all 3 notifiers when none throw", () => {
    const n1 = new FakeNotifier();
    const n2 = new FakeNotifier();
    const n3 = new FakeNotifier();
    const dispatcher = new NotificationDispatcher([n1, n2, n3], logger);

    dispatcher.subscribeToSlot(55, FAKE_STATE_SERVICE, "owner/repo");

    expect(n1.calls.subscribeToSlot).toEqual([{ issueNumber: 55, repoSlug: "owner/repo" }]);
    expect(n2.calls.subscribeToSlot).toEqual([{ issueNumber: 55, repoSlug: "owner/repo" }]);
    expect(n3.calls.subscribeToSlot).toEqual([{ issueNumber: 55, repoSlug: "owner/repo" }]);
  });

  it("n1 subscribeToSlot throws → n2 and n3 still subscribe; only n2+n3 unsubscribe", () => {
    const n1 = new FakeNotifier();
    n1.subscribeThrow = new Error("n1 refuses to subscribe");
    const n2 = new FakeNotifier();
    const n3 = new FakeNotifier();
    const dispatcher = new NotificationDispatcher([n1, n2, n3], logger);

    dispatcher.subscribeToSlot(77, FAKE_STATE_SERVICE);
    dispatcher.unsubscribeFromSlot(77);

    // n1 failed to subscribe so it must NOT be unsubscribed (symmetric)
    expect(n1.calls.unsubscribeFromSlot).toEqual([]);
    expect(n2.calls.unsubscribeFromSlot).toEqual([77]);
    expect(n3.calls.unsubscribeFromSlot).toEqual([77]);
  });
});
