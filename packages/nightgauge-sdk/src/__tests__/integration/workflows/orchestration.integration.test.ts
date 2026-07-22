/**
 * Integration tests — Pipeline Initialization & Execution
 *
 * Workflow 1: PipelineOrchestrator constructor → events → usage → runStage()
 *
 * Tests orchestrator initialization, configuration defaults, event bus
 * integration, and single-stage execution with mocked query function.
 * Uses a temp workspace with minimal skill files to avoid real API calls.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as path from "node:path";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import { PipelineOrchestrator } from "../../../orchestrator/PipelineOrchestrator.js";
import { EventBus } from "../../../events/EventBus.js";
import { TokenTracker } from "../../../tracking/TokenTracker.js";
import { createSuccessQueryFn, createTokenEmittingQueryFn } from "../helpers/query-mocks.js";

/** Minimal SKILL.md content for test stages */
const MINIMAL_SKILL_MD = `# Test Stage\nExecute the following test stage.\n`;

async function createSkillWorkspace(): Promise<{ dir: string; cleanup: () => Promise<void> }> {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "sdk-orch-test-"));
  const stages = [
    "nightgauge-issue-pickup",
    "nightgauge-feature-planning",
    "nightgauge-feature-dev",
    "nightgauge-feature-validate",
    "nightgauge-pr-create",
    "nightgauge-pr-merge",
  ];
  for (const stage of stages) {
    const skillDir = path.join(dir, "skills", stage);
    await fs.mkdir(skillDir, { recursive: true });
    await fs.writeFile(path.join(skillDir, "SKILL.md"), MINIMAL_SKILL_MD, "utf-8");
  }
  // Create pipeline and plans directories
  await fs.mkdir(path.join(dir, ".nightgauge", "pipeline"), { recursive: true });
  await fs.mkdir(path.join(dir, ".nightgauge", "plans"), { recursive: true });

  return {
    dir,
    cleanup: async () => {
      await fs.rm(dir, { recursive: true, force: true });
    },
  };
}

describe("Pipeline Orchestration Workflow", () => {
  let workspace: { dir: string; cleanup: () => Promise<void> };

  beforeEach(async () => {
    workspace = await createSkillWorkspace();
  });

  afterEach(async () => {
    await workspace.cleanup();
  });

  describe("initialization", () => {
    it("creates an orchestrator with default config", () => {
      const queryFn = createSuccessQueryFn();
      const orchestrator = new PipelineOrchestrator(queryFn);

      expect(orchestrator).toBeDefined();
      expect(orchestrator.events).toBeInstanceOf(EventBus);
      expect(orchestrator.usage).toBeInstanceOf(TokenTracker);
    });

    it("applies provided config values correctly", () => {
      const queryFn = createSuccessQueryFn();
      const orchestrator = new PipelineOrchestrator(queryFn, {
        defaultModel: "opus",
        maxTurnsPerStage: 10,
        autoApprove: true,
        stageTimeoutMs: 30_000,
        adapter: "codex",
      });

      const config = orchestrator.getConfig();
      expect(config.defaultModel).toBe("opus");
      expect(config.maxTurnsPerStage).toBe(10);
      expect(config.autoApprove).toBe(true);
      expect(config.stageTimeoutMs).toBe(30_000);
      // #4038: the provider id is captured so it reaches StageExecutor.execute
      // (which provisions provider-aware steering + the correct preset).
      expect(config.adapter).toBe("codex");
    });

    it("leaves adapter undefined by default (Claude path — no provider preset forced)", () => {
      const config = new PipelineOrchestrator(createSuccessQueryFn()).getConfig();
      // Undefined → StageExecutor uses Claude defaults; a non-Claude run only
      // gets provider steering when an adapter is explicitly threaded (#4038).
      expect(config.adapter).toBeUndefined();
    });

    it("uses sensible default config values", () => {
      const orchestrator = new PipelineOrchestrator(createSuccessQueryFn());
      const config = orchestrator.getConfig();

      expect(config.defaultModel).toBe("sonnet");
      expect(config.autoApprove).toBe(false);
      expect(config.maxBacktracks).toBeGreaterThanOrEqual(1);
    });

    it("is not running on initialization", () => {
      const orchestrator = new PipelineOrchestrator(createSuccessQueryFn());
      expect(orchestrator.getIsRunning()).toBe(false);
    });

    it("has no current stage on initialization", () => {
      const orchestrator = new PipelineOrchestrator(createSuccessQueryFn());
      expect(orchestrator.getCurrentStage()).toBeNull();
    });
  });

  describe("event bus integration", () => {
    it("events property is the same EventBus instance across calls", () => {
      const orchestrator = new PipelineOrchestrator(createSuccessQueryFn());
      const bus1 = orchestrator.events;
      const bus2 = orchestrator.events;
      expect(bus1).toBe(bus2);
    });

    it("subscribers registered before runStage() receive stage events", async () => {
      const orchestrator = new PipelineOrchestrator(createSuccessQueryFn(), {
        cwd: workspace.dir,
        skillsPath: path.join(workspace.dir, "skills"),
        contextPath: path.join(workspace.dir, ".nightgauge", "pipeline"),
        stageTimeoutMs: 10_000,
      });

      const startEvents: string[] = [];
      const completeEvents: string[] = [];

      // Stages map to first-level phase nodes: a `running` phase is the start,
      // a `succeeded` phase is the completion.
      orchestrator.events.on("phase", (node) => {
        if (node.status === "running") startEvents.push(node.name);
        else if (node.status === "succeeded") completeEvents.push(node.name);
      });

      await orchestrator.runStage("issue-pickup", 42);

      expect(startEvents).toContain("issue-pickup");
      expect(completeEvents).toContain("issue-pickup");
    });

    it("phase running fires before phase succeeded for same stage", async () => {
      const orchestrator = new PipelineOrchestrator(createSuccessQueryFn(), {
        cwd: workspace.dir,
        skillsPath: path.join(workspace.dir, "skills"),
        contextPath: path.join(workspace.dir, ".nightgauge", "pipeline"),
        stageTimeoutMs: 10_000,
      });

      const eventOrder: string[] = [];
      orchestrator.events.on("phase", (node) => {
        if (node.status === "running") eventOrder.push("start");
        else if (node.status === "succeeded") eventOrder.push("complete");
      });

      await orchestrator.runStage("issue-pickup", 42);

      const startIdx = eventOrder.indexOf("start");
      const completeIdx = eventOrder.indexOf("complete");
      expect(startIdx).toBeLessThan(completeIdx);
    });
  });

  describe("token usage integration", () => {
    it("usage tracker records stage after runStage() completes", async () => {
      const orchestrator = new PipelineOrchestrator(createTokenEmittingQueryFn(), {
        cwd: workspace.dir,
        skillsPath: path.join(workspace.dir, "skills"),
        contextPath: path.join(workspace.dir, ".nightgauge", "pipeline"),
        stageTimeoutMs: 10_000,
      });

      await orchestrator.runStage("issue-pickup", 42);

      const total = orchestrator.usage.getTotalUsage();
      expect(total.stageCount).toBeGreaterThanOrEqual(1);
    });
  });

  describe("runStage() result", () => {
    it("returns StageResult with success=true for a passing query", async () => {
      const orchestrator = new PipelineOrchestrator(createSuccessQueryFn(), {
        cwd: workspace.dir,
        skillsPath: path.join(workspace.dir, "skills"),
        contextPath: path.join(workspace.dir, ".nightgauge", "pipeline"),
        stageTimeoutMs: 10_000,
      });

      const result = await orchestrator.runStage("issue-pickup", 42);

      expect(result).toBeDefined();
      expect(result.stage).toBe("issue-pickup");
      expect(result.issueNumber).toBe(42);
      expect(result.success).toBe(true);
      expect(result.durationMs).toBeGreaterThanOrEqual(0);
    });
  });
});
