import { describe, it, expect, beforeEach } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import {
  StageExecutor,
  buildStagePrompt,
  type SDKQueryFunction,
  type SDKQueryOptions,
} from "../../src/orchestrator/StageExecutor.js";
import { EventBus, PipelineRunEmitter } from "../../src/events/EventBus.js";
import { TokenTracker } from "../../src/tracking/TokenTracker.js";
import type { WorkflowEvent } from "../../src/cli/workflow/WorkflowEvent.js";
import {
  createMockQuery,
  createFailingQuery,
  createMockResult,
  createMockText,
} from "../mocks/agent-sdk.js";

describe("StageExecutor", () => {
  let executor: StageExecutor;
  let eventBus: EventBus;
  let emitter: PipelineRunEmitter;
  let tokenTracker: TokenTracker;

  beforeEach(() => {
    eventBus = new EventBus();
    emitter = new PipelineRunEmitter(eventBus, 42, "native-workflow");
    tokenTracker = new TokenTracker();
  });

  describe("execute", () => {
    it("emits a running phase + agent node at stage start", async () => {
      const mockQuery = createMockQuery();
      executor = new StageExecutor(tokenTracker, emitter, mockQuery);

      const phases: WorkflowEvent[] = [];
      const agents: WorkflowEvent[] = [];
      eventBus.on("phase", (n) => phases.push(n));
      eventBus.on("agent", (n) => agents.push(n));

      const messages = [];
      for await (const msg of executor.execute({
        stage: "issue-pickup",
        issueNumber: 42,
        prompt: "Test prompt",
      })) {
        messages.push(msg);
      }

      expect(phases[0]).toMatchObject({ kind: "phase", name: "issue-pickup", status: "running" });
      expect(agents[0]).toMatchObject({
        kind: "agent",
        agentId: "issue-pickup",
        status: "running",
      });
    });

    it("applies the claude_code preset for the default (Claude) path but not for Codex (#4028)", async () => {
      const captured: Array<SDKQueryOptions["options"]> = [];
      const capturingQuery: SDKQueryFunction = async function* (queryOptions) {
        captured.push(queryOptions.options);
        yield createMockResult();
      };
      // Use a tmp cwd so Codex AGENTS.md provisioning never touches the real repo.
      const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "stage-steer-"));
      try {
        const exec = new StageExecutor(tokenTracker, emitter, capturingQuery);

        for await (const _ of exec.execute({
          stage: "issue-pickup",
          issueNumber: 42,
          prompt: "p",
          cwd: tmpDir,
        })) {
          /* default path = Claude */
        }
        expect(captured[0]?.systemPrompt).toEqual({ type: "preset", preset: "claude_code" });

        for await (const _ of exec.execute({
          stage: "issue-pickup",
          issueNumber: 42,
          prompt: "p",
          adapter: "codex",
          cwd: tmpDir,
        })) {
          /* Codex steers via AGENTS.md, not the preset */
        }
        expect(captured[1]?.systemPrompt).toBeUndefined();
      } finally {
        fs.rmSync(tmpDir, { recursive: true, force: true });
      }
    });

    it("emits succeeded phase + agent terminals on success", async () => {
      const mockQuery = createMockQuery();
      executor = new StageExecutor(tokenTracker, emitter, mockQuery);

      const phases: WorkflowEvent[] = [];
      const agents: WorkflowEvent[] = [];
      eventBus.on("phase", (n) => phases.push(n));
      eventBus.on("agent", (n) => agents.push(n));

      for await (const _ of executor.execute({
        stage: "issue-pickup",
        issueNumber: 42,
        prompt: "Test prompt",
      })) {
        // Consume all messages
      }

      expect(phases.at(-1)).toMatchObject({ name: "issue-pickup", status: "succeeded" });
      expect(agents.at(-1)).toMatchObject({ status: "succeeded", terminalKind: "success" });
    });

    it("emits failed phase + agent terminals on failure", async () => {
      const error = new Error("Test error");
      const mockQuery = createFailingQuery(error);
      executor = new StageExecutor(tokenTracker, emitter, mockQuery);

      const phases: WorkflowEvent[] = [];
      const agents: WorkflowEvent[] = [];
      eventBus.on("phase", (n) => phases.push(n));
      eventBus.on("agent", (n) => agents.push(n));

      await expect(async () => {
        for await (const _ of executor.execute({
          stage: "issue-pickup",
          issueNumber: 42,
          prompt: "Test prompt",
        })) {
          // Consume all messages
        }
      }).rejects.toThrow("Test error");

      expect(phases.at(-1)).toMatchObject({ name: "issue-pickup", status: "failed" });
      expect(agents.at(-1)).toMatchObject({ status: "failed", terminalKind: "error" });
    });

    it("should record token usage on result", async () => {
      const mockQuery = createMockQuery([], createMockResult());
      executor = new StageExecutor(tokenTracker, emitter, mockQuery);

      for await (const _ of executor.execute({
        stage: "issue-pickup",
        issueNumber: 42,
        prompt: "Test prompt",
      })) {
        // Consume all messages
      }

      const usage = tokenTracker.getStageUsage("issue-pickup");
      expect(usage).toBeDefined();
      expect(usage!.inputTokens).toBe(1000);
      expect(usage!.outputTokens).toBe(500);
    });

    it("folds token usage into the stage agent node", async () => {
      const mockQuery = createMockQuery();
      executor = new StageExecutor(tokenTracker, emitter, mockQuery);

      const agents: WorkflowEvent[] = [];
      eventBus.on("agent", (n) => agents.push(n));

      for await (const _ of executor.execute({
        stage: "issue-pickup",
        issueNumber: 42,
        prompt: "Test prompt",
      })) {
        // Consume all messages
      }

      // The succeeded agent terminal carries the accrued usage from the result.
      const terminal = agents.at(-1)!;
      expect(terminal).toMatchObject({ kind: "agent", agentId: "issue-pickup" });
      expect(terminal).toHaveProperty("usage");
      const usage = (terminal as { usage: { inputTokens: number; outputTokens: number } }).usage;
      expect(usage.inputTokens).toBe(1000);
      expect(usage.outputTokens).toBe(500);
    });

    it("should yield all SDK messages", async () => {
      const customMessages = [createMockText("Message 1"), createMockText("Message 2")];
      const mockQuery = createMockQuery(customMessages);
      executor = new StageExecutor(tokenTracker, emitter, mockQuery);

      const messages = [];
      for await (const msg of executor.execute({
        stage: "issue-pickup",
        issueNumber: 42,
        prompt: "Test prompt",
      })) {
        messages.push(msg);
      }

      // init + 2 custom messages + result
      expect(messages.length).toBeGreaterThanOrEqual(4);
    });
  });

  describe("executeCollect", () => {
    it("should return all messages as array", async () => {
      const mockQuery = createMockQuery();
      executor = new StageExecutor(tokenTracker, emitter, mockQuery);

      const messages = await executor.executeCollect({
        stage: "issue-pickup",
        issueNumber: 42,
        prompt: "Test prompt",
      });

      expect(Array.isArray(messages)).toBe(true);
      expect(messages.length).toBeGreaterThan(0);
    });
  });

  describe("executeResult", () => {
    it("should return the result message", async () => {
      const mockQuery = createMockQuery();
      executor = new StageExecutor(tokenTracker, emitter, mockQuery);

      const result = await executor.executeResult({
        stage: "issue-pickup",
        issueNumber: 42,
        prompt: "Test prompt",
      });

      expect(result).toBeDefined();
      expect(result!.type).toBe("result");
    });
  });

  describe("buildStagePrompt", () => {
    it("should generate prompt for issue-pickup", async () => {
      const prompt = await buildStagePrompt("issue-pickup", 42);

      expect(prompt).toContain("skills/nightgauge-issue-pickup/SKILL.md");
      expect(prompt).toContain("42");
      expect(prompt).toContain('$ARGUMENTS="42"');
      expect(prompt).toContain("Skip any auto-selection or interactive issue selection flow.");
      expect(prompt).toContain("Do not invoke AskUserQuestion in this mode.");
    });

    it("should place the stable skill body before the variable invocation block (#3805)", async () => {
      const prompt = await buildStagePrompt("issue-pickup", 42);

      // The skill body (stable, cacheable prefix) must precede the variable
      // invocation trailer. The skill markdown opens with a top-level heading;
      // the trailer is anchored by the literal "Invocation context:" marker.
      const skillIdx = prompt.indexOf("# ");
      const invocationIdx = prompt.indexOf("Invocation context:");
      expect(skillIdx).toBeGreaterThanOrEqual(0);
      expect(invocationIdx).toBeGreaterThan(skillIdx);
      // The lead-in references the skill "above", not "following" — confirms
      // the variable block trails the stable body.
      expect(prompt).toContain("Execute the above Nightgauge pipeline skill");
    });

    it("should generate prompt for all stages", async () => {
      const stages = [
        "issue-pickup",
        "feature-planning",
        "feature-dev",
        "feature-validate",
        "pr-create",
        "pr-merge",
      ] as const;

      for (const stage of stages) {
        const prompt = await buildStagePrompt(stage, 99);
        expect(prompt).toContain("99");
        expect(prompt).toContain("SKILL.md");
        // Stable-prefix-first ordering holds for every stage (#3805).
        expect(prompt.indexOf("# ")).toBeLessThan(prompt.indexOf("Invocation context:"));
      }
    });
  });
});
