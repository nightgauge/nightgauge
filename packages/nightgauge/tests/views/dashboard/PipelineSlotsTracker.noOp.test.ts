/**
 * PipelineSlotsTracker.markNoOp — a partial-delivery no-op's card mark (#3487).
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

describe("PipelineSlotsTracker.markNoOp (#3487)", () => {
  let mockIpc: ReturnType<typeof createMockIpc>;
  let tracker: PipelineSlotsTracker;
  let changes: number;

  beforeEach(() => {
    mockIpc = createMockIpc();
    tracker = new PipelineSlotsTracker(mockIpc as any);
    changes = 0;
    tracker.onChanged(() => changes++);
    for (const repo of ["example-org/platform", "example-org/flutter"]) {
      mockIpc.fire("stage.start", { repo, issueNumber: 21, stage: "feature-dev" });
    }
    changes = 0;
  });

  it("marks only the named repository's slot and fires once", () => {
    tracker.markNoOp(21, "example-org/platform");
    expect(tracker.getSnapshot(21, "example-org/platform")?.noOp).toBe(true);
    expect(tracker.getSnapshot(21, "example-org/flutter")?.noOp).toBeUndefined();
    expect(changes).toBe(1);
    tracker.markNoOp(21, "example-org/platform");
    expect(changes).toBe(1);
  });

  it("ignores an unknown slot", () => {
    tracker.markNoOp(99, "example-org/platform");
    expect(changes).toBe(0);
    expect(tracker.getSnapshot(99, "example-org/platform")).toBeUndefined();
  });

  it("drops the mark with the snapshot, so a later run starts unmarked", () => {
    tracker.markNoOp(21, "example-org/platform");
    tracker.forget(21, "example-org/platform");
    mockIpc.fire("stage.start", {
      repo: "example-org/platform",
      issueNumber: 21,
      stage: "issue-pickup",
    });
    expect(tracker.getSnapshot(21, "example-org/platform")?.noOp).toBeUndefined();
  });
});
