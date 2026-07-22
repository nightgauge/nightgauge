/**
 * Producer-side regression for the acmeapp "zeros + category:unknown" gap
 * (#3914). Drives a REAL {@link StageExecutor} through a REAL
 * {@link PipelineRunEmitter} + {@link EventBus} + {@link TokenTracker} with a
 * fake query function, then asserts the emitted terminal SubAgentNode:
 *
 *  - carries NON-ZERO usage that MATCHES what the tracker recorded (never the
 *    seeded zeros) on a completed stage,
 *  - carries the correct `terminalKind` per outcome (success / error / timeout),
 *  - still carries the real burned usage on a stage that THREW after recording
 *    a result (a failed stage must not report zeros),
 *  - flags `estimated: true` for a local-inference stage (real tokens, zero
 *    cost) so the cost is honestly marked an estimate.
 */

import { describe, it, expect } from "vitest";
import { EventBus, PipelineRunEmitter, type PipelineStage } from "../../events/EventBus.js";
import { TokenTracker } from "../../tracking/TokenTracker.js";
import {
  StageExecutor,
  type SDKMessage,
  type SDKQueryFunction,
  type SDKQueryOptions,
} from "../../orchestrator/StageExecutor.js";
import { isSubAgentNode, type SubAgentNode, type WorkflowEvent } from "../../cli/workflow/index.js";

const STAGE: PipelineStage = "feature-dev";
const ISSUE = 42;

/** Collect every emitted node off a real EventBus for assertion. */
function captureBus(): { bus: EventBus; events: WorkflowEvent[] } {
  const bus = new EventBus();
  const events: WorkflowEvent[] = [];
  bus.onAny((node) => events.push(node));
  return { bus, events };
}

/** The terminal (non-running) agent node for the stage, or undefined. */
function terminalAgent(events: WorkflowEvent[]): SubAgentNode | undefined {
  return events
    .filter((e): e is SubAgentNode => isSubAgentNode(e))
    .filter((a) => a.status !== "running")
    .at(-1);
}

/** A query fn that yields one result message carrying the given usage/cost. */
function resultQueryFn(
  usage: {
    input_tokens: number;
    output_tokens: number;
    cache_read_input_tokens?: number;
    cache_creation_input_tokens?: number;
  },
  totalCostUsd: number
): SDKQueryFunction {
  return async function* (_options: SDKQueryOptions) {
    yield {
      type: "result",
      subtype: "success",
      usage,
      total_cost_usd: totalCostUsd,
      modelUsage: {
        "claude-sonnet-4-5": { inputTokens: usage.input_tokens, outputTokens: usage.output_tokens },
      },
    } as SDKMessage;
  };
}

describe("StageExecutor emit path — per-agent usage + terminalKind (#3914)", () => {
  it("a completed stage agent node carries NON-ZERO usage matching the tracker", async () => {
    const { bus, events } = captureBus();
    const tracker = new TokenTracker();
    const emitter = new PipelineRunEmitter(bus, ISSUE);
    emitter.runStarted();
    const executor = new StageExecutor(
      tracker,
      emitter,
      resultQueryFn(
        {
          input_tokens: 1500,
          output_tokens: 600,
          cache_read_input_tokens: 200,
          cache_creation_input_tokens: 50,
        },
        0.012
      )
    );

    await executor.executeCollect({ stage: STAGE, issueNumber: ISSUE, prompt: "go" });

    const node = terminalAgent(events);
    expect(node).toBeDefined();
    expect(node!.status).toBe("succeeded");
    expect(node!.terminalKind).toBe("success");

    // The terminal usage is the REAL recorded usage, not the seeded zeros.
    const tracked = tracker.getWorkflowUsage(STAGE);
    expect(node!.usage).toEqual(tracked);
    expect(node!.usage.inputTokens).toBe(1500);
    expect(node!.usage.outputTokens).toBe(600);
    expect(node!.usage.cacheReadTokens).toBe(200);
    expect(node!.usage.cacheCreationTokens).toBe(50);
    expect(node!.usage.costUsd).toBeCloseTo(0.012, 6);
    expect(node!.usage.estimated).toBe(false);
  });

  it("a stage that THREW after recording a result still carries the real burned usage (terminalKind error)", async () => {
    const { bus, events } = captureBus();
    const tracker = new TokenTracker();
    const emitter = new PipelineRunEmitter(bus, ISSUE);
    emitter.runStarted();

    // Yield a real result (records usage), THEN throw — the failed terminal must
    // fold the tracker's real usage rather than reporting zeros.
    const throwingQuery: SDKQueryFunction = async function* (_o: SDKQueryOptions) {
      yield {
        type: "result",
        subtype: "success",
        usage: { input_tokens: 900, output_tokens: 300 },
        total_cost_usd: 0.008,
      } as SDKMessage;
      throw new Error("post-result explosion");
    };
    const executor = new StageExecutor(tracker, emitter, throwingQuery);

    await expect(
      executor.executeCollect({ stage: STAGE, issueNumber: ISSUE, prompt: "go" })
    ).rejects.toThrow(/explosion/);

    const node = terminalAgent(events);
    expect(node).toBeDefined();
    expect(node!.status).toBe("failed");
    expect(node!.terminalKind).toBe("error");
    expect(node!.usage.inputTokens).toBe(900);
    expect(node!.usage.outputTokens).toBe(300);
    expect(node!.usage.costUsd).toBeCloseTo(0.008, 6);
    expect(node!.usage).toEqual(tracker.getWorkflowUsage(STAGE));
  });

  it("classifies a timed-out stage as terminalKind 'timeout'", async () => {
    const { bus, events } = captureBus();
    const tracker = new TokenTracker();
    const emitter = new PipelineRunEmitter(bus, ISSUE);
    emitter.runStarted();

    // A query that never yields a result and outlives the timeout window.
    const hangingQuery: SDKQueryFunction = async function* (_o: SDKQueryOptions) {
      await new Promise((r) => setTimeout(r, 50));
      yield { type: "assistant" } as SDKMessage;
      await new Promise((r) => setTimeout(r, 50));
      yield { type: "assistant" } as SDKMessage;
    };
    const executor = new StageExecutor(tracker, emitter, hangingQuery);

    await expect(
      executor.executeCollect({ stage: STAGE, issueNumber: ISSUE, prompt: "go", timeoutMs: 10 })
    ).rejects.toThrow(/timed out/);

    const node = terminalAgent(events);
    expect(node).toBeDefined();
    expect(node!.status).toBe("failed");
    expect(node!.terminalKind).toBe("timeout");
    // No result arrived → zeroed but never blank; the field stays REQUIRED.
    expect(node!.usage).toEqual(tracker.getWorkflowUsage(STAGE));
  });

  it("marks a local-inference stage (real tokens, zero cost) as estimated", async () => {
    const { bus, events } = captureBus();
    const tracker = new TokenTracker();
    const emitter = new PipelineRunEmitter(bus, ISSUE);
    emitter.runStarted();
    // Nonzero tokens with total_cost_usd 0 → TokenTracker flags isLocalModel.
    const executor = new StageExecutor(
      tracker,
      emitter,
      resultQueryFn({ input_tokens: 700, output_tokens: 250 }, 0)
    );

    await executor.executeCollect({ stage: STAGE, issueNumber: ISSUE, prompt: "go" });

    const node = terminalAgent(events);
    expect(node).toBeDefined();
    expect(node!.status).toBe("succeeded");
    expect(node!.usage.inputTokens).toBe(700);
    expect(node!.usage.outputTokens).toBe(250);
    expect(node!.usage.costUsd).toBe(0);
    // Cost cannot be measured for local inference → honestly an estimate.
    expect(node!.usage.estimated).toBe(true);
  });
});
