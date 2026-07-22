/**
 * Tests for the WorkflowQuotaGate bridge consumer (#3909).
 *
 * Proves the SDK-side gate that the WorkflowExecutor (#3908) uses to consult the
 * Go-bridged ratelimit/cooldown quota state before a large fan-out:
 *  (a) a large fan-out DEFERS when the bridged signal reports exhaustion,
 *  (b) a large fan-out PROCEEDS on a status=allowed stall (not exhausted),
 *  (c) a small fan-out is never gated,
 *  (d) runSdkFanout({ quotaProvider }) spawns ZERO agents when deferred,
 *  (e) the gate fails OPEN when the provider/IPC bridge is unavailable.
 */

import { describe, it, expect, vi } from "vitest";
import {
  WORKFLOW_SCHEMA_VERSION,
  CLAUDE_CEILING,
  zeroUsage,
  ArrayWorkflowEventSink,
  runSdkFanout,
  evaluateQuotaGate,
  gateWorkflowFanout,
  DEFAULT_LARGE_FANOUT_THRESHOLD,
  isWorkflowRun,
  type WorkflowSpec,
  type WorkflowExecutorBindings,
  type WorkflowQuotaState,
} from "../../cli/workflow/index.js";

/** A spec with `agents` agents in one phase (judges add to the planned count). */
function makeSpec(agents: number, over: Partial<WorkflowSpec> = {}): WorkflowSpec {
  return {
    schemaVersion: WORKFLOW_SCHEMA_VERSION,
    runId: "run-quota",
    issueNumber: 3909,
    phases: [
      {
        name: "fan-out",
        agents: Array.from({ length: agents }, (_, i) => ({
          agentId: `a${i}`,
          prompt: `agent ${i}`,
          provider: "claude",
        })),
      },
    ],
    // CLAUDE_CEILING.maxTotal is 1000, so a large fan-out validates fine.
    ceiling: CLAUDE_CEILING,
    ...over,
  };
}

const healthyState: WorkflowQuotaState = {
  remaining: 4800,
  limit: 5000,
  resetsAt: 0,
  exhausted: false,
};

const exhaustedBucketState: WorkflowQuotaState = {
  remaining: 0,
  limit: 5000,
  resetsAt: 1_780_000_000,
  bucket: "github-rest",
  exhausted: true,
};

const exhaustedCooldownState: WorkflowQuotaState = {
  remaining: 200,
  limit: 5000,
  resetsAt: 0,
  cooldownUntil: "2026-06-03T18:00:00Z",
  cooldownReason: "Anthropic API quota exhausted — dispatch suspended",
  bucket: "anthropic-five-hour",
  exhausted: true,
};

describe("evaluateQuotaGate", () => {
  it("defers a large fan-out when the bridged signal is exhausted (bucket)", () => {
    const d = evaluateQuotaGate(exhaustedBucketState, 100);
    expect(d.action).toBe("defer");
    expect(d.deferred).toBe(true);
    expect(d.large).toBe(true);
    // Retry hint derives from the GitHub bucket reset when no cooldown is set.
    expect(d.retryAfter).toBe(new Date(exhaustedBucketState.resetsAt * 1000).toISOString());
    expect(d.reason).toContain("github-rest");
  });

  it("defers a large fan-out on an active cooldown, preferring cooldownUntil", () => {
    const d = evaluateQuotaGate(exhaustedCooldownState, DEFAULT_LARGE_FANOUT_THRESHOLD);
    expect(d.deferred).toBe(true);
    expect(d.retryAfter).toBe(exhaustedCooldownState.cooldownUntil);
    expect(d.reason).toContain("Anthropic API quota exhausted");
  });

  it("proceeds with a large fan-out when not exhausted (status=allowed stall)", () => {
    const d = evaluateQuotaGate(healthyState, 100);
    expect(d.action).toBe("proceed");
    expect(d.deferred).toBe(false);
    expect(d.large).toBe(true);
  });

  it("never gates a small fan-out even when exhausted", () => {
    const d = evaluateQuotaGate(exhaustedBucketState, DEFAULT_LARGE_FANOUT_THRESHOLD - 1);
    expect(d.action).toBe("proceed");
    expect(d.deferred).toBe(false);
    expect(d.large).toBe(false);
  });
});

describe("gateWorkflowFanout", () => {
  it("sizes the fan-out from the spec and defers when exhausted", async () => {
    const provider = vi.fn().mockResolvedValue(exhaustedBucketState);
    const d = await gateWorkflowFanout(makeSpec(50), provider);
    expect(provider).toHaveBeenCalledOnce();
    expect(d.plannedAgents).toBe(50);
    expect(d.deferred).toBe(true);
  });

  it("fails OPEN (proceeds) when the provider throws (IPC unavailable)", async () => {
    const provider = vi.fn().mockRejectedValue(new Error("ipc down"));
    const d = await gateWorkflowFanout(makeSpec(50), provider);
    expect(d.action).toBe("proceed");
    expect(d.deferred).toBe(false);
    expect(d.reason).toContain("failing open");
  });
});

describe("runSdkFanout with quotaProvider", () => {
  /** A fake executor that records how many agents it actually ran. */
  function makeCountingExecutor(): {
    bindings: WorkflowExecutorBindings;
    agentRuns: () => number;
  } {
    let runs = 0;
    return {
      agentRuns: () => runs,
      bindings: {
        async runAgent() {
          runs++;
          return { usage: zeroUsage(true), terminalKind: "success" as const };
        },
        async runJudge() {
          return { verdict: "pass" as const, usage: zeroUsage(true) };
        },
      },
    };
  }

  it("defers a large fan-out into an exhausted quota WITHOUT spawning any agent", async () => {
    const sink = new ArrayWorkflowEventSink();
    const exec = makeCountingExecutor();
    const provider = vi.fn().mockResolvedValue(exhaustedBucketState);

    const summary = await runSdkFanout(makeSpec(40), sink, exec.bindings, {
      quotaProvider: provider,
    });

    // Zero agents spawned — the whole point of the gate.
    expect(exec.agentRuns()).toBe(0);
    expect(summary.agentCount).toBe(0);
    expect(summary.status).toBe("skipped");
    expect(summary.quotaGate?.deferred).toBe(true);

    // The event tree is still well-formed: exactly one run node, status skipped.
    const runEvents = sink.getEvents().filter(isWorkflowRun);
    expect(runEvents).toHaveLength(1);
    expect(runEvents[0]?.status).toBe("skipped");
  });

  it("runs the full fan-out when the quota is healthy and records the decision", async () => {
    const sink = new ArrayWorkflowEventSink();
    const exec = makeCountingExecutor();
    const provider = vi.fn().mockResolvedValue(healthyState);

    const summary = await runSdkFanout(makeSpec(40), sink, exec.bindings, {
      quotaProvider: provider,
    });

    expect(exec.agentRuns()).toBe(40);
    expect(summary.agentCount).toBe(40);
    expect(summary.status).toBe("succeeded");
    expect(summary.quotaGate?.deferred).toBe(false);
    expect(summary.quotaGate?.action).toBe("proceed");
  });

  it("runs unconditionally when no quotaProvider is supplied", async () => {
    const sink = new ArrayWorkflowEventSink();
    const exec = makeCountingExecutor();

    const summary = await runSdkFanout(makeSpec(40), sink, exec.bindings);

    expect(exec.agentRuns()).toBe(40);
    expect(summary.quotaGate).toBeUndefined();
  });
});
