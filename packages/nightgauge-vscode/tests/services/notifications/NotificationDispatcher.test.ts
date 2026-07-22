import { describe, it, expect, vi, beforeEach } from "vitest";

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
import { NotificationRouter } from "../../../src/services/notifications/NotificationRouter";
import type { Notifier, PipelineEventContext } from "../../../src/services/notifications/types";
import type { PipelineStateService } from "../../../src/services/PipelineStateService";

interface FakeNotifierCalls {
  initialize: number;
  onPipelineStart: PipelineEventContext[];
  onPipelineUpdate: PipelineEventContext[];
  subscribeToSlot: Array<{ issueNumber: number; repoSlug?: string }>;
  unsubscribeFromSlot: number[];
  dispose: number;
}

class FakeNotifier implements Notifier {
  calls: FakeNotifierCalls = {
    initialize: 0,
    onPipelineStart: [],
    onPipelineUpdate: [],
    subscribeToSlot: [],
    unsubscribeFromSlot: [],
    dispose: 0,
  };

  initializeReject?: Error;
  onStartThrow?: Error;
  subscribeThrow?: Error;
  disposeThrow?: Error;

  async initialize(): Promise<void> {
    this.calls.initialize += 1;
    if (this.initializeReject) throw this.initializeReject;
  }

  onPipelineStart(ctx: PipelineEventContext): void {
    this.calls.onPipelineStart.push(ctx);
    if (this.onStartThrow) throw this.onStartThrow;
  }

  onPipelineUpdate(ctx: PipelineEventContext): void {
    this.calls.onPipelineUpdate.push(ctx);
  }

  subscribeToSlot(
    issueNumber: number,
    _slotStateService: PipelineStateService,
    repoSlug?: string
  ): void {
    if (this.subscribeThrow) throw this.subscribeThrow;
    this.calls.subscribeToSlot.push({ issueNumber, repoSlug });
  }

  unsubscribeFromSlot(issueNumber: number): void {
    this.calls.unsubscribeFromSlot.push(issueNumber);
  }

  dispose(): void {
    this.calls.dispose += 1;
    if (this.disposeThrow) throw this.disposeThrow;
  }
}

function makeLogger() {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as any;
}

const FAKE_STATE_SERVICE = {} as unknown as PipelineStateService;

describe("NotificationDispatcher", () => {
  let logger: ReturnType<typeof makeLogger>;

  beforeEach(() => {
    logger = makeLogger();
  });

  it("initialize() calls every notifier's initialize()", async () => {
    const a = new FakeNotifier();
    const b = new FakeNotifier();
    const dispatcher = new NotificationDispatcher([a, b], logger);

    await dispatcher.initialize();

    expect(a.calls.initialize).toBe(1);
    expect(b.calls.initialize).toBe(1);
  });

  it("initialize() resolves even when one notifier rejects", async () => {
    const a = new FakeNotifier();
    a.initializeReject = new Error("boom");
    const b = new FakeNotifier();
    const dispatcher = new NotificationDispatcher([a, b], logger);

    await expect(dispatcher.initialize()).resolves.toBeUndefined();
    expect(b.calls.initialize).toBe(1);
    expect(logger.warn).toHaveBeenCalled();
  });

  it("onPipelineStart fans the same ctx out to every notifier", () => {
    const a = new FakeNotifier();
    const b = new FakeNotifier();
    const dispatcher = new NotificationDispatcher([a, b], logger);
    const ctx: PipelineEventContext = { issueNumber: 42, stage: "feature-dev" };

    dispatcher.onPipelineStart(ctx);

    expect(a.calls.onPipelineStart).toEqual([ctx]);
    expect(b.calls.onPipelineStart).toEqual([ctx]);
  });

  it("onPipelineStart continues fan-out when one notifier throws", () => {
    const a = new FakeNotifier();
    a.onStartThrow = new Error("a is angry");
    const b = new FakeNotifier();
    const dispatcher = new NotificationDispatcher([a, b], logger);

    dispatcher.onPipelineStart({ issueNumber: 1 });

    expect(b.calls.onPipelineStart).toHaveLength(1);
    expect(logger.warn).toHaveBeenCalled();
  });

  it("onPipelineUpdate fans out", () => {
    const a = new FakeNotifier();
    const b = new FakeNotifier();
    const dispatcher = new NotificationDispatcher([a, b], logger);
    const ctx: PipelineEventContext = { issueNumber: 7 };

    dispatcher.onPipelineUpdate(ctx);

    expect(a.calls.onPipelineUpdate).toEqual([ctx]);
    expect(b.calls.onPipelineUpdate).toEqual([ctx]);
  });

  it("subscribeToSlot records subscriptions per notifier and forwards args", () => {
    const a = new FakeNotifier();
    const b = new FakeNotifier();
    const dispatcher = new NotificationDispatcher([a, b], logger);

    dispatcher.subscribeToSlot(99, FAKE_STATE_SERVICE, "owner/repo");

    expect(a.calls.subscribeToSlot).toEqual([{ issueNumber: 99, repoSlug: "owner/repo" }]);
    expect(b.calls.subscribeToSlot).toEqual([{ issueNumber: 99, repoSlug: "owner/repo" }]);
  });

  it("unsubscribeFromSlot is symmetric — only notifiers that subscribed are unsubscribed", () => {
    const a = new FakeNotifier();
    a.subscribeThrow = new Error("a refuses to subscribe");
    const b = new FakeNotifier();
    const dispatcher = new NotificationDispatcher([a, b], logger);

    dispatcher.subscribeToSlot(123, FAKE_STATE_SERVICE);
    dispatcher.unsubscribeFromSlot(123);

    expect(a.calls.unsubscribeFromSlot).toEqual([]);
    expect(b.calls.unsubscribeFromSlot).toEqual([123]);
  });

  it("unsubscribeFromSlot is a no-op when no prior subscribe was recorded", () => {
    const a = new FakeNotifier();
    const dispatcher = new NotificationDispatcher([a], logger);

    dispatcher.unsubscribeFromSlot(404);

    expect(a.calls.unsubscribeFromSlot).toEqual([]);
  });

  it("dispose() calls every notifier even when one throws", () => {
    const a = new FakeNotifier();
    a.disposeThrow = new Error("a refuses to dispose");
    const b = new FakeNotifier();
    const dispatcher = new NotificationDispatcher([a, b], logger);

    dispatcher.dispose();

    expect(a.calls.dispose).toBe(1);
    expect(b.calls.dispose).toBe(1);
    expect(logger.warn).toHaveBeenCalled();
  });
});

describe("NotificationDispatcher — routing integration (NotifierEntry + NotificationRouter)", () => {
  let logger: ReturnType<typeof makeLogger>;

  beforeEach(() => {
    logger = makeLogger();
  });

  it("delivers to all notifiers when router has no rules (DEFAULT_ROUTER)", () => {
    const a = new FakeNotifier();
    const b = new FakeNotifier();
    const router = new NotificationRouter([]);
    const dispatcher = new NotificationDispatcher(
      [
        { id: "discord", notifier: a },
        { id: "mattermost", notifier: b },
      ],
      logger,
      router
    );

    dispatcher.onPipelineStart({ issueNumber: 1, eventKey: "pipeline.start" });

    expect(a.calls.onPipelineStart).toHaveLength(1);
    expect(b.calls.onPipelineStart).toHaveLength(1);
  });

  it("skips notifier when router blocks its event", () => {
    const alertsNotifier = new FakeNotifier();
    const successNotifier = new FakeNotifier();
    const router = new NotificationRouter([
      { id: "alerts", type: "discord", events: ["pipeline.failure", "stall.warning"] },
      { id: "success", type: "mattermost", events: ["pipeline.complete"] },
    ]);
    const dispatcher = new NotificationDispatcher(
      [
        { id: "alerts", notifier: alertsNotifier },
        { id: "success", notifier: successNotifier },
      ],
      logger,
      router
    );

    dispatcher.onPipelineStart({ issueNumber: 2, eventKey: "pipeline.failure" });

    // alerts receives pipeline.failure; success does not
    expect(alertsNotifier.calls.onPipelineStart).toHaveLength(1);
    expect(successNotifier.calls.onPipelineStart).toHaveLength(0);
  });

  it("delivers to notifier with absent eventKey in ctx (backward compat — default to pipeline.start)", () => {
    const a = new FakeNotifier();
    const router = new NotificationRouter([
      { id: "a", type: "discord", events: ["pipeline.start"] },
    ]);
    const dispatcher = new NotificationDispatcher([{ id: "a", notifier: a }], logger, router);

    // No eventKey in ctx — should default to "pipeline.start"
    dispatcher.onPipelineStart({ issueNumber: 3 });

    expect(a.calls.onPipelineStart).toHaveLength(1);
  });

  it("legacy plain Notifier array (no ids) receives all events regardless of router", () => {
    const a = new FakeNotifier();
    const router = new NotificationRouter([
      // Rule for "discord" id — but 'a' gets an auto-generated id, not in routing table
      { id: "discord", type: "discord", events: ["pipeline.failure"] },
    ]);
    const dispatcher = new NotificationDispatcher([a], logger, router);

    dispatcher.onPipelineStart({ issueNumber: 4, eventKey: "pipeline.start" });

    // Unknown id → default deliver
    expect(a.calls.onPipelineStart).toHaveLength(1);
  });
});
