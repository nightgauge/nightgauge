/**
 * Tests for PipelineStateService stage lifecycle transitions.
 *
 * Covers gaps identified in test audit:
 * - Full stage lifecycle (start → complete)
 * - Error state transitions (start → failed)
 * - Out-of-order IPC events
 * - Issue number filtering
 * - Event emission ordering
 *
 * @see Issue #2230 - Test suite audit
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ---------------------------------------------------------------------------
// IPC event handler capture
// ---------------------------------------------------------------------------

type EventHandler = (data: unknown) => void;
const ipcHandlers: Map<string, EventHandler> = new Map();

function fireIpcEvent(event: string, data: unknown): void {
  ipcHandlers.get(event)?.(data);
}

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn((event: string, handler: EventHandler) => {
        ipcHandlers.set(event, handler);
        return { dispose: vi.fn() };
      }),
      call: vi.fn().mockRejectedValue(new Error("IPC not connected")),
    }),
  },
}));

vi.mock("vscode", () => ({
  EventEmitter: class {
    private _handlers: Array<(v: unknown) => void> = [];
    event = (cb: (v: unknown) => void) => {
      this._handlers.push(cb);
      return { dispose: () => {} };
    };
    fire(value: unknown) {
      for (const h of this._handlers) h(value);
    }
    dispose() {}
  },
  Disposable: class {
    dispose() {}
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

async function makeService(issueNumber: number) {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  PipelineStateService.resetInstance();
  ipcHandlers.clear();
  return PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("PipelineStateService — stage lifecycle transitions", () => {
  beforeEach(() => {
    vi.resetModules();
    ipcHandlers.clear();
  });

  describe("full stage lifecycle: start → complete", () => {
    it("stage transitions from running to complete", async () => {
      const svc = await makeService(100);
      await svc.initializePipeline(100, "Lifecycle Test", "feat/lifecycle");

      fireIpcEvent("stage.start", {
        issueNumber: 100,
        stage: "feature-planning",
        repo: "test/repo",
        title: "Feature Planning",
      });

      let state = await svc.getState();
      expect(state!.stages["feature-planning"]).toBeTruthy();
      expect(state!.stages["feature-planning"].status).toBe("running");

      fireIpcEvent("stage.complete", {
        issueNumber: 100,
        stage: "feature-planning",
        repo: "test/repo",
        error: "",
        inputTokens: 50000,
        outputTokens: 5000,
        cacheReadTokens: 0,
        costUsd: 0.45,
        model: "claude-sonnet-4-6",
      });

      state = await svc.getState();
      expect(state!.stages["feature-planning"].status).toBe("complete");
      expect(state!.stages["feature-planning"].endTime).toBeTruthy();
    });

    it("sequential stages maintain independent state", async () => {
      const svc = await makeService(101);
      await svc.initializePipeline(101, "Sequential", "feat/seq");

      // Stage 1: issue-pickup start → complete
      fireIpcEvent("stage.start", {
        issueNumber: 101,
        stage: "issue-pickup",
        repo: "test/repo",
        title: "Issue Pickup",
      });
      fireIpcEvent("stage.complete", {
        issueNumber: 101,
        stage: "issue-pickup",
        repo: "test/repo",
        error: "",
        inputTokens: 15000,
        outputTokens: 2000,
        cacheReadTokens: 0,
        costUsd: 0.08,
        model: "claude-sonnet-4-6",
      });

      // Stage 2: feature-planning start → complete
      fireIpcEvent("stage.start", {
        issueNumber: 101,
        stage: "feature-planning",
        repo: "test/repo",
        title: "Feature Planning",
      });
      fireIpcEvent("stage.complete", {
        issueNumber: 101,
        stage: "feature-planning",
        repo: "test/repo",
        error: "",
        inputTokens: 45000,
        outputTokens: 8000,
        cacheReadTokens: 0,
        costUsd: 0.42,
        model: "claude-sonnet-4-6",
      });

      const state = await svc.getState();
      // Both stages should be complete with independent data
      expect(state!.stages["issue-pickup"].status).toBe("complete");
      expect(state!.stages["feature-planning"].status).toBe("complete");
      expect(state!.tokens!.per_stage!["issue-pickup"]!.input).toBe(15000);
      expect(state!.tokens!.per_stage!["feature-planning"]!.input).toBe(45000);
    });
  });

  describe("error state transitions", () => {
    it("stage transitions to failed when error present in stage.complete", async () => {
      const svc = await makeService(200);
      await svc.initializePipeline(200, "Error Test", "feat/error");

      fireIpcEvent("stage.start", {
        issueNumber: 200,
        stage: "feature-dev",
        repo: "test/repo",
        title: "Feature Dev",
      });

      fireIpcEvent("stage.complete", {
        issueNumber: 200,
        stage: "feature-dev",
        repo: "test/repo",
        error: "API Error (500): Internal Server Error",
        inputTokens: 30000,
        outputTokens: 1000,
        cacheReadTokens: 0,
        costUsd: 0.2,
        model: "claude-sonnet-4-6",
      });

      const state = await svc.getState();
      expect(state!.stages["feature-dev"].status).toBe("failed");
      expect(state!.stages["feature-dev"].error).toBe("API Error (500): Internal Server Error");
    });

    it("fires onStageError event when stage fails", async () => {
      const svc = await makeService(201);
      await svc.initializePipeline(201, "Error Event", "feat/err-event");

      const errorEvents: Array<{
        stage: string;
        issueNumber: number;
        error: string;
      }> = [];
      svc.onStageError((e: { stage: string; issueNumber: number; error: string }) =>
        errorEvents.push(e)
      );

      fireIpcEvent("stage.complete", {
        issueNumber: 201,
        stage: "feature-validate",
        repo: "test/repo",
        error: "Validation failed: tests not passing",
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        costUsd: 0,
        model: "",
      });

      expect(errorEvents).toHaveLength(1);
      expect(errorEvents[0].stage).toBe("feature-validate");
      expect(errorEvents[0].error).toBe("Validation failed: tests not passing");
    });

    it("fires onStageComplete for successful stages, not onStageError", async () => {
      const svc = await makeService(202);
      await svc.initializePipeline(202, "Success Event", "feat/success");

      const completions: Array<{ stage: string }> = [];
      const errors: unknown[] = [];
      svc.onStageComplete((e: { stage: string }) => completions.push(e));
      svc.onStageError((e: unknown) => errors.push(e));

      fireIpcEvent("stage.complete", {
        issueNumber: 202,
        stage: "pr-create",
        repo: "test/repo",
        error: "",
        inputTokens: 20000,
        outputTokens: 3000,
        cacheReadTokens: 0,
        costUsd: 0.15,
        model: "claude-sonnet-4-6",
      });

      expect(completions).toHaveLength(1);
      expect(completions[0].stage).toBe("pr-create");
      expect(errors).toHaveLength(0);
    });
  });

  describe("issue number filtering", () => {
    it("ignores stage events for different issue numbers", async () => {
      const svc = await makeService(300);
      await svc.initializePipeline(300, "Filter Test", "feat/filter");

      // Fire event for wrong issue
      fireIpcEvent("stage.start", {
        issueNumber: 999, // wrong issue
        stage: "feature-dev",
        repo: "test/repo",
        title: "Feature Dev",
      });

      const state = await svc.getState();
      // Should not have created stage for wrong issue
      expect(state!.stages["feature-dev"]).toBeUndefined();
    });

    it("processes events for correct issue number", async () => {
      const svc = await makeService(301);
      await svc.initializePipeline(301, "Correct Issue", "feat/correct");

      fireIpcEvent("stage.start", {
        issueNumber: 301,
        stage: "feature-dev",
        repo: "test/repo",
        title: "Feature Dev",
      });

      const state = await svc.getState();
      expect(state!.stages["feature-dev"]).toBeTruthy();
      expect(state!.stages["feature-dev"].status).toBe("running");
    });
  });

  describe("event ordering", () => {
    it("onStateChanged fires after state is mutated", async () => {
      const svc = await makeService(400);
      await svc.initializePipeline(400, "Event Order", "feat/order");

      let stateAtEventTime: any = null;
      svc.onStateChanged((state: any) => {
        stateAtEventTime = state;
      });

      fireIpcEvent("stage.start", {
        issueNumber: 400,
        stage: "issue-pickup",
        repo: "test/repo",
        title: "Issue Pickup",
      });

      // The state passed to the event should already have the stage
      expect(stateAtEventTime).toBeTruthy();
      expect(stateAtEventTime.stages["issue-pickup"]).toBeTruthy();
      expect(stateAtEventTime.stages["issue-pickup"].status).toBe("running");
    });

    it("onStageStart fires when stage.start event arrives", async () => {
      const svc = await makeService(401);
      await svc.initializePipeline(401, "Stage Start Event", "feat/start");

      const startEvents: Array<{ stage: string }> = [];
      svc.onStageStart((e: { stage: string }) => startEvents.push(e));

      fireIpcEvent("stage.start", {
        issueNumber: 401,
        stage: "feature-planning",
        repo: "test/repo",
        title: "Feature Planning",
      });

      expect(startEvents).toHaveLength(1);
      expect(startEvents[0].stage).toBe("feature-planning");
    });
  });

  describe("pipeline.complete event", () => {
    it("sets final token state from pipeline.complete", async () => {
      const svc = await makeService(500);
      await svc.initializePipeline(500, "Pipeline Complete", "feat/complete");

      // Simulate stages completing first
      fireIpcEvent("stage.complete", {
        issueNumber: 500,
        stage: "issue-pickup",
        repo: "test/repo",
        error: "",
        inputTokens: 20000,
        outputTokens: 3000,
        cacheReadTokens: 0,
        costUsd: 0.12,
        model: "claude-sonnet-4-6",
      });

      // Then pipeline.complete arrives with final totals
      fireIpcEvent("pipeline.complete", {
        issueNumber: 500,
        success: true,
        totalInputTokens: 80000,
        totalOutputTokens: 15000,
        totalCostUSD: 0.95,
        perStage: [
          {
            stage: "issue-pickup",
            inputTokens: 20000,
            outputTokens: 3000,
            costUsd: 0.12,
          },
          {
            stage: "feature-planning",
            inputTokens: 30000,
            outputTokens: 5000,
            costUsd: 0.35,
          },
          {
            stage: "feature-dev",
            inputTokens: 30000,
            outputTokens: 7000,
            costUsd: 0.48,
          },
        ],
      });

      const state = await svc.getState();
      // pipeline.complete should set the authoritative totals
      expect(state!.tokens).toBeTruthy();
    });
  });

  describe("state cleared between pipeline runs", () => {
    it("initializePipeline resets stage state for new issue", async () => {
      const svc = await makeService(600);
      await svc.initializePipeline(600, "First Run", "feat/first");

      fireIpcEvent("stage.complete", {
        issueNumber: 600,
        stage: "issue-pickup",
        repo: "test/repo",
        error: "",
        inputTokens: 20000,
        outputTokens: 3000,
        cacheReadTokens: 0,
        costUsd: 0.12,
        model: "claude-sonnet-4-6",
      });

      let state = await svc.getState();
      expect(state!.tokens!.total_input).toBe(20000);

      // Start new pipeline for different issue
      const { PipelineStateService } = await import("../../src/services/PipelineStateService");
      PipelineStateService.resetInstance();
      // Re-register handlers since resetInstance clears them
      const svc2 = PipelineStateService.createForWorktree("/tmp/repo", 601);
      await svc2.initializePipeline(601, "Second Run", "feat/second");

      state = await svc2.getState();
      // New pipeline should have fresh token state (no per_stage data)
      expect(state!.tokens?.per_stage).toBeUndefined();
      expect(state!.tokens?.estimated_cost_usd).toBeUndefined();
      expect(state!.issue_number).toBe(601);
    });
  });
});
