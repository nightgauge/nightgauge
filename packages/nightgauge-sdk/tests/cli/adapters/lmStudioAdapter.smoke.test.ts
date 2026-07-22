/**
 * LM Studio Adapter — Smoke, Integration, and Regression Tests
 *
 * Three sections:
 *
 * A. Mocked integration — always runs in CI.
 *    Exercises the full stage execution path through StageExecutor wired with
 *    LmStudioAdapter.createQueryFunction() and a fetch-mocked SSE response.
 *
 * B. Regression coverage — always runs in CI.
 *    Asserts that Claude, Codex, Gemini, and other adapter paths are unaffected
 *    by the LM Studio addition.
 *
 * C. Live smoke tests — skipped in CI (process.env.CI is truthy).
 *    Run locally against a real LM Studio server:
 *      NIGHTGAUGE_LM_STUDIO_MODEL=<loaded-model-name> \
 *        npx -w @nightgauge/sdk vitest run \
 *        tests/cli/adapters/lmStudioAdapter.smoke.test.ts
 *
 * @see Issue #2054 - Add LM Studio smoke coverage and regression validation
 * @see Issue #2058 - LM Studio adapter implementation
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { LmStudioAdapter } from "../../../src/cli/adapters/LmStudioAdapter.js";
import { defaultRegistry } from "../../../src/cli/adapters/AdapterRegistry.js";
import { StageExecutor } from "../../../src/orchestrator/StageExecutor.js";
import { EventBus, PipelineRunEmitter } from "../../../src/events/EventBus.js";
import { TokenTracker } from "../../../src/tracking/TokenTracker.js";
import type { WorkflowEvent, SubAgentNode } from "../../../src/cli/workflow/WorkflowEvent.js";

// ---------------------------------------------------------------------------
// SSE stream helper (shared across sections A and C)
// ---------------------------------------------------------------------------

function makeSseResponse(sseText: string): Response {
  const encoder = new TextEncoder();
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(encoder.encode(sseText));
      controller.close();
    },
  });
  return {
    ok: true,
    status: 200,
    statusText: "OK",
    body: stream,
  } as unknown as Response;
}

// ---------------------------------------------------------------------------
// Section A: Mocked integration tests (always runs in CI)
// ---------------------------------------------------------------------------

describe("LM Studio adapter — mocked integration (stage execution path)", () => {
  let eventBus: EventBus;
  let emitter: PipelineRunEmitter;
  let tokenTracker: TokenTracker;
  let savedModel: string | undefined;

  beforeEach(() => {
    savedModel = process.env.NIGHTGAUGE_LM_STUDIO_MODEL;
    process.env.NIGHTGAUGE_LM_STUDIO_MODEL = "integration-test-model";
    eventBus = new EventBus();
    emitter = new PipelineRunEmitter(eventBus, 1);
    tokenTracker = new TokenTracker();
  });

  afterEach(() => {
    vi.restoreAllMocks();
    if (savedModel !== undefined) {
      process.env.NIGHTGAUGE_LM_STUDIO_MODEL = savedModel;
    } else {
      delete process.env.NIGHTGAUGE_LM_STUDIO_MODEL;
    }
  });

  it("emits a running phase node when executing a stage through the adapter", async () => {
    // SSE response: one assistant delta + usage info + DONE
    const sse = [
      'data: {"choices":[{"delta":{"content":"Picked up issue #42."}}],"usage":{"prompt_tokens":20,"completion_tokens":8}}\n',
      "\n",
      "data: [DONE]\n",
      "\n",
    ].join("");

    vi.spyOn(global, "fetch").mockResolvedValue(makeSseResponse(sse) as Response);

    const adapter = new LmStudioAdapter();
    const queryFn = await adapter.createQueryFunction();
    const executor = new StageExecutor(tokenTracker, emitter, queryFn);

    const phaseStarts: WorkflowEvent[] = [];
    eventBus.on("phase", (node) => {
      if (node.status === "running") phaseStarts.push(node);
    });

    for await (const _ of executor.execute({
      stage: "issue-pickup",
      issueNumber: 42,
      prompt: "Pick up issue #42",
    })) {
      /* drain */
    }

    expect(phaseStarts).toHaveLength(1);
    expect(phaseStarts[0]).toMatchObject({
      kind: "phase",
      name: "issue-pickup",
      status: "running",
    });
  });

  it("emits a succeeded phase terminal on successful stage execution", async () => {
    const sse = 'data: {"choices":[{"delta":{"content":"Done."}}]}\n\ndata: [DONE]\n\n';
    vi.spyOn(global, "fetch").mockResolvedValue(makeSseResponse(sse) as Response);

    const adapter = new LmStudioAdapter();
    const queryFn = await adapter.createQueryFunction();
    const executor = new StageExecutor(tokenTracker, emitter, queryFn);

    const phaseCompletes: WorkflowEvent[] = [];
    eventBus.on("phase", (node) => {
      if (node.status === "succeeded") phaseCompletes.push(node);
    });

    for await (const _ of executor.execute({
      stage: "issue-pickup",
      issueNumber: 99,
      prompt: "Test prompt",
    })) {
      /* drain */
    }

    expect(phaseCompletes).toHaveLength(1);
    expect(phaseCompletes[0]).toMatchObject({
      kind: "phase",
      name: "issue-pickup",
      status: "succeeded",
    });
  });

  it("folds input_tokens and output_tokens from the result into the agent node usage", async () => {
    const sse = [
      'data: {"choices":[{"delta":{"content":"hi"}}],"usage":{"prompt_tokens":30,"completion_tokens":12}}\n',
      "\n",
      "data: [DONE]\n",
      "\n",
    ].join("");

    vi.spyOn(global, "fetch").mockResolvedValue(makeSseResponse(sse) as Response);

    const adapter = new LmStudioAdapter();
    const queryFn = await adapter.createQueryFunction();
    const executor = new StageExecutor(tokenTracker, emitter, queryFn);

    const agentTerminals: SubAgentNode[] = [];
    eventBus.on("agent", (node) => {
      if (node.status === "succeeded") agentTerminals.push(node);
    });

    for await (const _ of executor.execute({
      stage: "feature-planning",
      issueNumber: 1,
      prompt: "Plan",
    })) {
      /* drain */
    }

    expect(agentTerminals).toHaveLength(1);
    const { usage } = agentTerminals[0];
    expect(usage.inputTokens).toBe(30);
    expect(usage.outputTokens).toBe(12);
    expect(usage.costUsd).toBe(0); // local inference: no cost
  });

  it("output messages include the assistant content delta", async () => {
    const sse = [
      'data: {"choices":[{"delta":{"content":"Feature planning complete."}}]}\n',
      "\n",
      "data: [DONE]\n",
      "\n",
    ].join("");

    vi.spyOn(global, "fetch").mockResolvedValue(makeSseResponse(sse) as Response);

    const adapter = new LmStudioAdapter();
    const queryFn = await adapter.createQueryFunction();
    const executor = new StageExecutor(tokenTracker, emitter, queryFn);

    const messages = [];
    for await (const msg of executor.execute({
      stage: "feature-dev",
      issueNumber: 7,
      prompt: "Implement",
    })) {
      messages.push(msg);
    }

    const assistantMessages = messages.filter((m) => m.type === "assistant");
    expect(assistantMessages).toHaveLength(1);
    expect(assistantMessages[0].content).toBe("Feature planning complete.");
  });

  it("yields a result message as the final message in the stage output", async () => {
    const sse = [
      'data: {"choices":[{"delta":{"content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}\n',
      "\n",
      "data: [DONE]\n",
      "\n",
    ].join("");

    vi.spyOn(global, "fetch").mockResolvedValue(makeSseResponse(sse) as Response);

    const adapter = new LmStudioAdapter();
    const queryFn = await adapter.createQueryFunction();
    const executor = new StageExecutor(tokenTracker, emitter, queryFn);

    const messages = await executor.executeCollect({
      stage: "pr-create",
      issueNumber: 3,
      prompt: "Create PR",
    });

    const resultMessages = messages.filter((m) => m.type === "result");
    expect(resultMessages).toHaveLength(1);
    expect((resultMessages[0] as Record<string, unknown>).subtype).toBe("success");
    expect((resultMessages[0] as Record<string, unknown>).total_cost_usd).toBe(0);
    expect((resultMessages[0] as Record<string, unknown>).model).toBe("integration-test-model");
  });
});

// ---------------------------------------------------------------------------
// Section B: Regression coverage (always runs in CI)
// ---------------------------------------------------------------------------

describe("Adapter regression — Claude, Codex, Gemini paths unaffected by LM Studio addition", () => {
  it("defaultRegistry includes all expected adapters", () => {
    // Verify all 6 adapters are registered
    expect(() => defaultRegistry.get("claude-sdk")).not.toThrow();
    expect(() => defaultRegistry.get("claude-headless")).not.toThrow();
    expect(() => defaultRegistry.get("codex")).not.toThrow();
    expect(() => defaultRegistry.get("gemini")).not.toThrow();
    expect(() => defaultRegistry.get("gemini-sdk")).not.toThrow();
    expect(() => defaultRegistry.get("lm-studio")).not.toThrow();
  });

  it("validateAuth resolves to passed for claude-headless, codex, gemini, lm-studio", async () => {
    // These adapters do not require a running server or CLI for auth validation
    const noServerAdapters = [
      defaultRegistry.get("claude-headless"),
      defaultRegistry.get("codex"),
      defaultRegistry.get("gemini"),
      defaultRegistry.get("lm-studio"),
    ];

    for (const adapter of noServerAdapters) {
      await expect(adapter.validateAuth()).resolves.toBe("passed");
    }
  });

  it("requiresDirectApiKey is false for all non-SDK adapters including lm-studio", () => {
    const nonSdkAdapters = [
      defaultRegistry.get("claude-headless"),
      defaultRegistry.get("codex"),
      defaultRegistry.get("gemini"),
      defaultRegistry.get("lm-studio"),
    ];

    for (const adapter of nonSdkAdapters) {
      expect(
        adapter.requiresDirectApiKey(),
        `${adapter.name} should NOT require a direct API key`
      ).toBe(false);
    }
  });

  it("SDK adapters (claude-sdk, gemini-sdk) still require a direct API key", () => {
    expect(defaultRegistry.get("claude-sdk").requiresDirectApiKey()).toBe(true);
    expect(defaultRegistry.get("gemini-sdk").requiresDirectApiKey()).toBe(true);
  });

  it("all 6 adapters have unique names", () => {
    const names = defaultRegistry.getNames();
    const uniqueNames = new Set(names);
    expect(uniqueNames.size).toBe(names.length);
    expect(names).toContain("lm-studio");
  });

  it("lm-studio declares sdk-fanout while claude-headless declares native-workflow", () => {
    // Regression: verify the LM Studio addition did not change other adapters' capability.
    expect(defaultRegistry.get("lm-studio").getOrchestrationCapability()).toBe("sdk-fanout");
    expect(defaultRegistry.get("claude-headless").getOrchestrationCapability()).toBe(
      "native-workflow"
    );
  });
});

// ---------------------------------------------------------------------------
// Section C: Live smoke tests (skipped in CI)
// Run locally with:
//   NIGHTGAUGE_LM_STUDIO_MODEL=<loaded-model-name> \
//     npx -w @nightgauge/sdk vitest run \
//     tests/cli/adapters/lmStudioAdapter.smoke.test.ts
// ---------------------------------------------------------------------------

describe.skipIf(!!process.env.CI || !process.env.NIGHTGAUGE_LM_STUDIO_MODEL)(
  "LM Studio live smoke tests — requires running LM Studio server",
  () => {
    it("validateAuth resolves to passed", async () => {
      const adapter = new LmStudioAdapter();
      await expect(adapter.validateAuth()).resolves.toBe("passed");
    });

    it("createQueryFunction with real server yields at least one assistant message", async () => {
      const adapter = new LmStudioAdapter();
      const queryFn = await adapter.createQueryFunction();

      const messages = [];
      for await (const msg of queryFn({ prompt: "Reply with exactly: ok" })) {
        messages.push(msg);
      }

      const assistantMessages = messages.filter((m) => m.type === "assistant");
      const resultMessages = messages.filter((m) => m.type === "result");

      expect(assistantMessages.length).toBeGreaterThanOrEqual(1);
      expect(resultMessages).toHaveLength(1);
    });

    it("result message has non-zero token counts from real inference", async () => {
      const adapter = new LmStudioAdapter();
      const queryFn = await adapter.createQueryFunction();

      const messages = [];
      for await (const msg of queryFn({ prompt: "Reply with: hello" })) {
        messages.push(msg);
      }

      const result = messages.find((m) => m.type === "result") as Record<string, unknown>;
      expect(result).toBeDefined();
      const usage = result.usage as Record<string, number>;
      // Local inference should produce nonzero token counts
      expect(usage.input_tokens + usage.output_tokens).toBeGreaterThan(0);
      // Local inference should have zero cost
      expect(result.total_cost_usd).toBe(0);
    });

    it.todo(
      "six-stage smoke workflow: pipeline can execute all stages with lm-studio adapter — " +
        "manual verification: run each pipeline stage skill file against a real LM Studio server " +
        "with NIGHTGAUGE_ADAPTER=lm-studio and NIGHTGAUGE_LM_STUDIO_MODEL=<model-name>. " +
        "Automated harness pending headless pipeline smoke infrastructure."
    );
  }
);
