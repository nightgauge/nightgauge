/**
 * PipelineSlotsTracker — snapshots keyed by repository and issue number (#2412).
 *
 * Two repositories' issues with one number run in two slots; their IPC events
 * carry `repo`, and each must land on its own slot card.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { PipelineSlotsTracker } from "../../../src/views/dashboard/PipelineSlotsTracker";

vi.mock("vscode", () => ({
  EventEmitter: class {
    private listeners: Array<(...a: unknown[]) => void> = [];
    event = (cb: (...a: unknown[]) => void) => {
      this.listeners.push(cb);
      return { dispose: vi.fn() };
    };
    fire(...args: unknown[]) {
      this.listeners.forEach((l) => l(...args));
    }
    dispose() {}
  },
  Disposable: class {
    static from(...d: unknown[]) {
      return { dispose: vi.fn() };
    }
    dispose() {}
  },
}));

type Handler = (data: unknown) => void;

function createMockIpc() {
  const handlers: Record<string, Handler[]> = {};
  return {
    on: vi.fn((event: string, cb: Handler) => {
      (handlers[event] ??= []).push(cb);
      return { dispose: vi.fn() };
    }),
    fire(event: string, data: unknown) {
      (handlers[event] ?? []).forEach((cb) => cb(data));
    },
  };
}

describe("PipelineSlotsTracker — repository and number (#2412)", () => {
  let mockIpc: ReturnType<typeof createMockIpc>;
  let tracker: PipelineSlotsTracker;

  beforeEach(() => {
    mockIpc = createMockIpc();
    tracker = new PipelineSlotsTracker(mockIpc as any);
  });

  it("keeps two same-numbered issues in different repositories apart", () => {
    mockIpc.fire("stage.start", {
      repo: "example-org/platform",
      issueNumber: 21,
      stage: "feature-dev",
    });
    mockIpc.fire("stage.start", {
      repo: "example-org/app",
      issueNumber: 21,
      stage: "issue-pickup",
    });
    mockIpc.fire("stage.complete", {
      repo: "example-org/app",
      issueNumber: 21,
      stage: "issue-pickup",
      inputTokens: 100,
      costUsd: 0.25,
    });

    const platform = tracker.getSnapshot(21, "example-org/platform");
    const app = tracker.getSnapshot(21, "example-org/app");
    expect(platform).toBeDefined();
    expect(app).toBeDefined();
    expect(platform).not.toBe(app);
    expect(platform!.currentStage).toBe("feature-dev");
    expect(platform!.costUsd).toBe(0);
    expect(platform!.stages["issue-pickup"]).toBeUndefined();
    expect(app!.stages["issue-pickup"]?.status).toBe("complete");
    expect(app!.costUsd).toBe(0.25);
    expect(tracker.getSnapshots().size).toBe(2);
  });

  it("applies a token delta and forgets by repository", () => {
    mockIpc.fire("stage.start", {
      repo: "example-org/platform",
      issueNumber: 21,
      stage: "feature-dev",
    });
    mockIpc.fire("stage.start", { repo: "example-org/app", issueNumber: 21, stage: "feature-dev" });

    tracker.applyTokenDelta(21, { costUsd: 1 }, "example-org/app");
    expect(tracker.getSnapshot(21, "example-org/app")!.costUsd).toBe(1);
    expect(tracker.getSnapshot(21, "example-org/platform")!.costUsd).toBe(0);

    // Without a repository the number names two snapshots: no guess.
    tracker.applyTokenDelta(21, { costUsd: 5 });
    expect(tracker.getSnapshot(21)).toBeUndefined();
    expect(tracker.getSnapshot(21, "example-org/app")!.costUsd).toBe(1);

    tracker.forget(21, "example-org/platform");
    expect(tracker.getSnapshot(21, "example-org/platform")).toBeUndefined();
    expect(tracker.getSnapshot(21, "example-org/app")).toBeDefined();
    expect(tracker.getSnapshot(21)?.repo).toBe("example-org/app");
  });

  it("adopts a snapshot recorded without a repository once an event names it", () => {
    mockIpc.fire("stage.start", { issueNumber: 34, stage: "issue-pickup" });
    mockIpc.fire("stage.complete", {
      repo: "example-org/app",
      issueNumber: 34,
      stage: "issue-pickup",
    });

    expect(tracker.getSnapshots().size).toBe(1);
    const snap = tracker.getSnapshot(34, "example-org/app");
    expect(snap?.repo).toBe("example-org/app");
    expect(snap?.stages["issue-pickup"]?.status).toBe("complete");
  });

  it("keeps the single-repository case unchanged", () => {
    mockIpc.fire("stage.start", { repo: "example-org/app", issueNumber: 7, stage: "feature-dev" });
    expect(tracker.getSnapshot(7)?.currentStage).toBe("feature-dev");
    expect(tracker.getSnapshot(7, "example-org/app")?.currentStage).toBe("feature-dev");
    tracker.forget(7);
    expect(tracker.getSnapshots().size).toBe(0);
  });
});
