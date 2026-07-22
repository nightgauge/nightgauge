import { describe, it, expect } from "vitest";
import {
  PipelineOrchestrator,
  DEFAULT_STAGES,
  APPROVAL_STAGES,
} from "../../src/orchestrator/PipelineOrchestrator.js";
import { createMockQuery, createFailingQuery } from "../mocks/agent-sdk.js";

describe("PipelineOrchestrator", () => {
  describe("constructor", () => {
    it("should create with default config", () => {
      const orchestrator = new PipelineOrchestrator(createMockQuery());

      const config = orchestrator.getConfig();
      expect(config.contextPath).toBe(".nightgauge/pipeline");
      expect(config.defaultModel).toBe("sonnet");
      expect(config.stages).toEqual(DEFAULT_STAGES);
    });

    it("should accept custom config", () => {
      const orchestrator = new PipelineOrchestrator(createMockQuery(), {
        contextPath: "/custom/path",
        defaultModel: "opus",
        stages: ["issue-pickup", "feature-planning"],
      });

      const config = orchestrator.getConfig();
      expect(config.contextPath).toBe("/custom/path");
      expect(config.defaultModel).toBe("opus");
      expect(config.stages).toHaveLength(2);
    });
  });

  describe("events", () => {
    it("should expose EventBus instance", () => {
      const orchestrator = new PipelineOrchestrator(createMockQuery());
      expect(orchestrator.events).toBeDefined();
      expect(typeof orchestrator.events.on).toBe("function");
    });
  });

  describe("usage", () => {
    it("should expose TokenTracker instance", () => {
      const orchestrator = new PipelineOrchestrator(createMockQuery());
      expect(orchestrator.usage).toBeDefined();
      expect(typeof orchestrator.usage.getTotalUsage).toBe("function");
    });
  });

  describe("context", () => {
    it("should expose ContextManager instance", () => {
      const orchestrator = new PipelineOrchestrator(createMockQuery());
      expect(orchestrator.context).toBeDefined();
      expect(typeof orchestrator.context.read).toBe("function");
    });
  });

  describe("runStage", () => {
    it("should execute a single stage", async () => {
      const orchestrator = new PipelineOrchestrator(createMockQuery());

      const result = await orchestrator.runStage("issue-pickup", 42);

      expect(result.stage).toBe("issue-pickup");
      expect(result.issueNumber).toBe(42);
      expect(result.success).toBe(true);
      expect(result.durationMs).toBeGreaterThanOrEqual(0);
      expect(result.messages.length).toBeGreaterThan(0);
    });

    it("should return failure result on error", async () => {
      const orchestrator = new PipelineOrchestrator(createFailingQuery(new Error("Stage failed")));

      const result = await orchestrator.runStage("issue-pickup", 42);

      expect(result.success).toBe(false);
      expect(result.error).toBeDefined();
      expect(result.error!.message).toBe("Stage failed");
    });
  });

  describe("runStageStreaming", () => {
    it("should yield messages as they arrive", async () => {
      const orchestrator = new PipelineOrchestrator(createMockQuery());

      const messages = [];
      for await (const msg of orchestrator.runStageStreaming("issue-pickup", 42)) {
        messages.push(msg);
      }

      expect(messages.length).toBeGreaterThan(0);
    });
  });

  describe("run", () => {
    it("should run configured stages in order", async () => {
      const stageOrder: string[] = [];
      const orchestrator = new PipelineOrchestrator(createMockQuery(), {
        stages: ["issue-pickup"], // Just one stage for simplicity
      });

      // Stages map to first-level phase nodes; a running phase marks the start.
      orchestrator.events.on("phase", (node) => {
        if (node.status === "running") stageOrder.push(node.name);
      });

      const result = await orchestrator.run(42);

      expect(result.success).toBe(true);
      expect(result.stagesCompleted).toContain("issue-pickup");
      expect(stageOrder).toContain("issue-pickup");
    });

    it("should stop on stage failure", async () => {
      const orchestrator = new PipelineOrchestrator(createFailingQuery(new Error("Failed")), {
        stages: ["issue-pickup", "feature-planning"],
      });

      const result = await orchestrator.run(42);

      expect(result.success).toBe(false);
      expect(result.stagesFailed).toContain("issue-pickup");
      expect(result.stagesCompleted).not.toContain("feature-planning");
    });

    it("emits a terminal root run node when the pipeline completes", async () => {
      const orchestrator = new PipelineOrchestrator(createMockQuery(), {
        stages: ["issue-pickup"],
      });

      const runTerminals: Array<{ status: string; issueNumber?: number }> = [];
      orchestrator.events.on("run", (node) => {
        if (node.status !== "running") {
          runTerminals.push({ status: node.status, issueNumber: node.issueNumber });
        }
      });

      await orchestrator.run(42);

      expect(runTerminals).toHaveLength(1);
      expect(runTerminals[0]).toMatchObject({ status: "succeeded", issueNumber: 42 });
    });

    it("should throw if already running", async () => {
      const orchestrator = new PipelineOrchestrator(createMockQuery(), {
        stages: ["issue-pickup"],
      });

      const runPromise = orchestrator.run(42);

      await expect(orchestrator.run(43)).rejects.toThrow("Pipeline is already running");

      await runPromise;
    });

    it("should track token usage across stages", async () => {
      const orchestrator = new PipelineOrchestrator(createMockQuery(), {
        stages: ["issue-pickup"],
      });

      const result = await orchestrator.run(42);

      expect(result.usage.stageCount).toBe(1);
      expect(result.usage.inputTokens).toBeGreaterThan(0);
    });
  });

  describe("approval workflow", () => {
    it("blocks an approval-gated stage until approve() is called", async () => {
      const orchestrator = new PipelineOrchestrator(createMockQuery(), {
        stages: ["feature-planning"],
      });

      // Start run but don't await - it will block at the approval gate.
      const runPromise = orchestrator.run(42);

      // Give event loop a tick: the orchestrator is parked on feature-planning
      // and has NOT executed the stage (no phase started yet).
      await new Promise((resolve) => setTimeout(resolve, 10));

      expect(orchestrator.getIsRunning()).toBe(true);
      expect(orchestrator.getCurrentStage()).toBe("feature-planning");

      // Approve and finish
      orchestrator.approve();
      const result = await runPromise;
      expect(result.stagesCompleted).toContain("feature-planning");
    });

    it("should stop pipeline on reject", async () => {
      const orchestrator = new PipelineOrchestrator(createMockQuery(), {
        stages: ["feature-planning", "feature-dev"],
      });

      const runPromise = orchestrator.run(42);

      await new Promise((resolve) => setTimeout(resolve, 10));
      orchestrator.reject();

      const result = await runPromise;

      expect(result.stagesCompleted).not.toContain("feature-dev");
    });
  });

  describe("stop", () => {
    it("should abort running pipeline", async () => {
      const orchestrator = new PipelineOrchestrator(createMockQuery(), {
        stages: ["feature-planning"],
      });

      const runPromise = orchestrator.run(42);

      await new Promise((resolve) => setTimeout(resolve, 10));
      await orchestrator.stop();

      const result = await runPromise;
      expect(result.stagesCompleted).toHaveLength(0);
    });
  });

  describe("getIsRunning", () => {
    it("should return false initially", () => {
      const orchestrator = new PipelineOrchestrator(createMockQuery());
      expect(orchestrator.getIsRunning()).toBe(false);
    });
  });

  describe("getCurrentStage", () => {
    it("should return null when not running", () => {
      const orchestrator = new PipelineOrchestrator(createMockQuery());
      expect(orchestrator.getCurrentStage()).toBeNull();
    });
  });

  describe("cleanup", () => {
    it("should delegate to ContextManager", async () => {
      const orchestrator = new PipelineOrchestrator(createMockQuery());

      // This won't delete anything since there are no files, but it shouldn't throw
      const deleted = await orchestrator.cleanup(42);
      expect(Array.isArray(deleted)).toBe(true);
    });
  });

  describe("constants", () => {
    it("should export DEFAULT_STAGES", () => {
      expect(DEFAULT_STAGES).toEqual([
        "issue-pickup",
        "feature-planning",
        "feature-dev",
        "feature-validate",
        "pr-create",
        "pr-merge",
      ]);
    });

    it("should export APPROVAL_STAGES", () => {
      expect(APPROVAL_STAGES).toContain("feature-planning");
    });
  });
});
