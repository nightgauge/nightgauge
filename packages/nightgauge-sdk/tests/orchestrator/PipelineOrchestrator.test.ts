import { describe, it, expect } from "vitest";
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  PipelineOrchestrator,
  DEFAULT_STAGES,
  APPROVAL_STAGES,
} from "../../src/orchestrator/PipelineOrchestrator.js";
import { createMockQuery, createFailingQuery, createMockResult } from "../mocks/agent-sdk.js";
import type { SDKQueryFunction } from "../../src/orchestrator/StageExecutor.js";

/**
 * Wait until the orchestrator is parked on its approval gate (#1423).
 *
 * These tests used `await new Promise(r => setTimeout(r, 10))` and then called
 * approve()/reject(). That is a BET that the gate has armed, and approve() /
 * reject() are silent no-ops when it has not — they check `approvalResolver`
 * and return. When the bet lost under load the call landed on nothing, the run
 * promise never settled, and the test died on vitest's 5s budget rather than on
 * an assertion, naming nothing about what went wrong.
 *
 * Polling the real state makes the ordering enforced instead of assumed. The
 * cap exists so a genuine hang still fails as a test failure rather than
 * spinning to the suite timeout.
 */
async function untilAwaitingApproval(
  orchestrator: PipelineOrchestrator,
  timeoutMs = 2000
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!orchestrator.isAwaitingApproval()) {
    if (Date.now() > deadline) {
      throw new Error(
        `orchestrator never reached its approval gate within ${timeoutMs}ms ` +
          `(isRunning=${orchestrator.getIsRunning()}, stage=${orchestrator.getCurrentStage()})`
      );
    }
    await new Promise((resolve) => setTimeout(resolve, 1));
  }
}

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

    it("gives every stage's query its stage, its turn budget and the run's one identity (#1648)", async () => {
      const seen: Array<{ stage?: string; maxTurns?: number; runId?: string }> = [];
      const query: SDKQueryFunction = async function* (q) {
        seen.push({
          stage: q.options?.stage,
          maxTurns: q.options?.maxTurns,
          runId: q.options?.runId,
        });
        yield createMockResult();
      };
      const orchestrator = new PipelineOrchestrator(query, {
        stages: ["issue-pickup", "feature-planning"],
        autoApprove: true,
        maxTurnsPerStage: 33,
      });
      const result = await orchestrator.run(42);

      expect(result.success).toBe(true);
      expect(seen.map((s) => s.stage)).toEqual(["issue-pickup", "feature-planning"]);
      expect(seen.map((s) => s.maxTurns)).toEqual([33, 33]);
      // One run identity for the whole run, a canonical UUIDv7 when no
      // run-state names one.
      expect(seen[0].runId).toMatch(
        /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
      );
      expect(seen[1].runId).toBe(seen[0].runId);
    });

    it("deletes an opencode run's shared per-run root when the run ends, through the Go verb (#1648)", async () => {
      const dir = mkdtempSync(join(tmpdir(), "po-clean-"));
      const bin = join(dir, "nightgauge");
      writeFileSync(bin, `#!/bin/sh\necho "$@" >> '${dir}/calls'\n`);
      chmodSync(bin, 0o755);
      const prev = process.env.NIGHTGAUGE_BIN;
      process.env.NIGHTGAUGE_BIN = bin;
      try {
        let runId: string | undefined;
        const query: SDKQueryFunction = async function* (q) {
          runId = q.options?.runId;
          yield createMockResult();
        };
        for (const adapter of ["claude-headless", "opencode"]) {
          await new PipelineOrchestrator(query, { stages: ["issue-pickup"], adapter }).run(42);
        }
        const calls = readFileSync(join(dir, "calls"), "utf-8").trim().split("\n");
        // Only the opencode run, once, for the identity its stages were given.
        expect(calls).toEqual([`opencode cleanup --run-id ${runId}`]);
      } finally {
        if (prev === undefined) delete process.env.NIGHTGAUGE_BIN;
        else process.env.NIGHTGAUGE_BIN = prev;
        rmSync(dir, { recursive: true, force: true });
      }
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
      await untilAwaitingApproval(orchestrator);

      expect(orchestrator.getIsRunning()).toBe(true);
      expect(orchestrator.getCurrentStage()).toBe("feature-planning");

      // Approve and finish
      orchestrator.approve();
      const result = await runPromise;
      expect(result.stagesCompleted).toContain("feature-planning");
    });

    // The mechanism the sleep-based version was betting against (#1423).
    //
    // approve()/reject()/skip() all check `approvalResolver` and return
    // silently when it is null. So acting before the gate arms is not an
    // error — it is nothing, and the run then never continues. That is why the
    // old tests failed as a 5s TIMEOUT rather than as an assertion: the reject
    // landed on nothing and the promise never settled.
    it("reject() before the gate arms is a silent no-op — which is why the wait exists", async () => {
      const orchestrator = new PipelineOrchestrator(createMockQuery(), {
        stages: ["feature-planning", "feature-dev"],
      });

      // Before run() there is no gate at all.
      expect(orchestrator.isAwaitingApproval()).toBe(false);
      expect(() => orchestrator.reject()).not.toThrow();

      const runPromise = orchestrator.run(42);
      await untilAwaitingApproval(orchestrator);

      // Only now does it do anything.
      expect(orchestrator.isAwaitingApproval()).toBe(true);
      orchestrator.reject();
      expect(orchestrator.isAwaitingApproval()).toBe(false);

      const result = await runPromise;
      expect(result.stagesCompleted).not.toContain("feature-dev");
    });

    it("should stop pipeline on reject", async () => {
      const orchestrator = new PipelineOrchestrator(createMockQuery(), {
        stages: ["feature-planning", "feature-dev"],
      });

      const runPromise = orchestrator.run(42);

      await untilAwaitingApproval(orchestrator);
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

      await untilAwaitingApproval(orchestrator);
      await orchestrator.stop();

      const result = await runPromise;
      expect(result.stagesCompleted).toHaveLength(0);
    });
  });

  describe("stop reaches the running stage's query (#1637)", () => {
    /** A query that runs until its abort signal fires; without one it succeeds after 3 s. */
    function abortableQuery(): { query: SDKQueryFunction; started: Promise<void> } {
      let markStarted!: () => void;
      const started = new Promise<void>((r) => (markStarted = r));
      const query: SDKQueryFunction = async function* (opts) {
        const signal = opts.options?.abortSignal;
        markStarted();
        let aborted = false;
        await new Promise<void>((resolve) => {
          const timer = setTimeout(resolve, 3000);
          signal?.addEventListener("abort", () => {
            clearTimeout(timer);
            aborted = true;
            resolve();
          });
        });
        if (aborted) throw new Error("query aborted");
        yield createMockResult();
      };
      return { query, started };
    }

    it("for a stage run on its own", async () => {
      const { query, started } = abortableQuery();
      const orchestrator = new PipelineOrchestrator(query);
      const pending = orchestrator.runStage("issue-pickup", 42);
      await started;
      await orchestrator.stop();
      const result = await pending;
      expect(result.success).toBe(false);
      expect(result.error?.message).toBe("query aborted");
    });

    it("for a stage of a full run", async () => {
      const { query, started } = abortableQuery();
      const orchestrator = new PipelineOrchestrator(query, { stages: ["issue-pickup"] });
      const pending = orchestrator.run(42);
      await started;
      await orchestrator.stop();
      const result = await pending;
      expect(result.stagesFailed).toEqual(["issue-pickup"]);
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
