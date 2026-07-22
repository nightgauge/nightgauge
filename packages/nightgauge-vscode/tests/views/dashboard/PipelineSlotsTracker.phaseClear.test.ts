/**
 * PipelineSlotsTracker — currentPhase clearing on stage transitions (#3240)
 *
 * Verifies that a stale phase from a previous stage does not bleed into a
 * concurrent slot card after the pipeline advances. Mirrors the clearing
 * pattern already implemented in OutputWindowState (Issue #3010).
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

describe("PipelineSlotsTracker — currentPhase clearing (#3240)", () => {
  let mockIpc: ReturnType<typeof createMockIpc>;
  let tracker: PipelineSlotsTracker;

  beforeEach(() => {
    mockIpc = createMockIpc();
    tracker = new PipelineSlotsTracker(mockIpc as any);
  });

  it("clears currentPhase when stage.start advances to a different stage", () => {
    mockIpc.fire("phase.start", {
      issueNumber: 3238,
      stage: "feature-planning",
      name: "Knowledge Base Read",
      index: 1,
      total: 5,
    });

    expect(tracker.getSnapshot(3238)?.currentPhase?.name).toBe("Knowledge Base Read");

    // Pipeline advances to feature-dev WITHOUT a phase.complete for the
    // previous phase (the bug scenario — the lost write).
    mockIpc.fire("stage.start", {
      issueNumber: 3238,
      stage: "feature-dev",
    });

    const snap = tracker.getSnapshot(3238);
    expect(snap?.currentStage).toBe("feature-dev");
    expect(snap?.currentPhase).toBeUndefined();
  });

  it("preserves currentPhase when stage.start re-fires for the same stage", () => {
    mockIpc.fire("phase.start", {
      issueNumber: 3238,
      stage: "feature-planning",
      name: "Knowledge Base Read",
      index: 1,
      total: 5,
    });

    // Same-stage re-fire (e.g. retry) should not erase live phase progress.
    mockIpc.fire("stage.start", {
      issueNumber: 3238,
      stage: "feature-planning",
    });

    expect(tracker.getSnapshot(3238)?.currentPhase?.name).toBe("Knowledge Base Read");
  });

  it("clears currentPhase when stage.complete fires for the active stage", () => {
    mockIpc.fire("phase.start", {
      issueNumber: 3238,
      stage: "pr-merge",
      name: "Wait For Reviews",
      index: 1,
      total: 3,
    });

    mockIpc.fire("stage.complete", {
      issueNumber: 3238,
      stage: "pr-merge",
    });

    expect(tracker.getSnapshot(3238)?.currentPhase).toBeUndefined();
  });

  // ---------------------------------------------------------------------
  // Issue #3244 — prior-stage reconciliation
  // ---------------------------------------------------------------------

  it("reconciles a prior stage stuck at 'running' when a later stage starts (#3244)", () => {
    mockIpc.fire("stage.start", { issueNumber: 3239, stage: "feature-planning" });
    expect(tracker.getSnapshot(3239)?.stages["feature-planning"].status).toBe("running");

    // Simulate a dropped stage.complete for feature-planning — orchestrator
    // jumps straight to feature-dev.
    mockIpc.fire("stage.start", { issueNumber: 3239, stage: "feature-dev" });

    const snap = tracker.getSnapshot(3239);
    expect(snap?.stages["feature-planning"].status).toBe("complete");
    expect(snap?.stages["feature-dev"].status).toBe("running");
    expect(snap?.currentStage).toBe("feature-dev");
  });

  it("reconciles MULTIPLE prior stages stuck at 'running' (#3244)", () => {
    mockIpc.fire("stage.start", { issueNumber: 3239, stage: "issue-pickup" });
    mockIpc.fire("stage.start", { issueNumber: 3239, stage: "feature-planning" });
    mockIpc.fire("stage.start", { issueNumber: 3239, stage: "feature-dev" });

    const snap = tracker.getSnapshot(3239);
    expect(snap?.stages["issue-pickup"].status).toBe("complete");
    expect(snap?.stages["feature-planning"].status).toBe("complete");
    expect(snap?.stages["feature-dev"].status).toBe("running");
  });

  it("does NOT downgrade non-running prior stages (skipped/failed/complete preserved) (#3244)", () => {
    mockIpc.fire("stage.start", { issueNumber: 3239, stage: "issue-pickup" });
    mockIpc.fire("stage.complete", {
      issueNumber: 3239,
      stage: "issue-pickup",
      error: "boom",
    });

    expect(tracker.getSnapshot(3239)?.stages["issue-pickup"].status).toBe("failed");

    // Later stage starts — failed prior must remain failed, not become complete.
    mockIpc.fire("stage.start", { issueNumber: 3239, stage: "feature-planning" });

    expect(tracker.getSnapshot(3239)?.stages["issue-pickup"].status).toBe("failed");
  });

  it("scopes reconciliation per-issue snapshot (#3244)", () => {
    mockIpc.fire("stage.start", { issueNumber: 100, stage: "feature-planning" });
    mockIpc.fire("stage.start", { issueNumber: 200, stage: "feature-planning" });

    // Issue 100 advances; issue 200 should be untouched.
    mockIpc.fire("stage.start", { issueNumber: 100, stage: "feature-dev" });

    expect(tracker.getSnapshot(100)?.stages["feature-planning"].status).toBe("complete");
    expect(tracker.getSnapshot(200)?.stages["feature-planning"].status).toBe("running");
  });
});
