/**
 * Tests for the SdkFanoutRunner reference floor (#3905).
 *
 * Drives the runner with a FAKE executor (no real CLIs) and proves:
 *  (a) concurrency never exceeds spec.ceiling.maxConcurrent (semaphore proven by
 *      observed peak in-flight),
 *  (b) the emitted tree shape — run → phase → agents (running then terminal) →
 *      judges — with correct parentId / monotonic seq,
 *  (c) an over-ceiling spec is rejected (the runner throws, never truncates),
 *  (d) the hard process ceiling (maxTotal) is independent of any USD budget.
 */

import { describe, it, expect } from "vitest";
import {
  WORKFLOW_SCHEMA_VERSION,
  FANOUT_CEILING,
  zeroUsage,
  isWorkflowRun,
  isSubAgentNode,
  isJudgeVerdict,
  ArrayWorkflowEventSink,
  runSdkFanout,
  AgentExecutionError,
  type WorkflowSpec,
  type WorkflowAgentUsage,
  type WorkflowExecutorBindings,
  type WorkflowEvent,
  type SubAgentNode,
  type JudgeVerdict,
  type WorkflowRun,
} from "../../cli/workflow/index.js";

const usage = (over: Partial<WorkflowAgentUsage> = {}): WorkflowAgentUsage => ({
  ...zeroUsage(true),
  inputTokens: 100,
  outputTokens: 50,
  costUsd: 0.01,
  ...over,
});

/** A spec with `agentsPerPhase` agents and `judgesPerPhase` judges per phase. */
function makeSpec(
  agentsPerPhase: number,
  judgesPerPhase = 0,
  over: Partial<WorkflowSpec> = {}
): WorkflowSpec {
  return {
    schemaVersion: WORKFLOW_SCHEMA_VERSION,
    runId: "run-1",
    issueNumber: 42,
    phases: [
      {
        name: "find",
        agents: Array.from({ length: agentsPerPhase }, (_, i) => ({
          agentId: `a${i}`,
          prompt: `agent ${i}`,
          provider: "codex",
        })),
        judges: Array.from({ length: judgesPerPhase }, (_, i) => ({
          judgeId: `j${i}`,
          prompt: `judge ${i}`,
          provider: "claude",
        })),
      },
    ],
    ceiling: FANOUT_CEILING,
    ...over,
  };
}

/**
 * A fake executor that tracks how many agents are running CONCURRENTLY. Each
 * agent holds a slot for one macrotask tick so overlap is observable.
 */
function makeTrackingExecutor(): {
  bindings: WorkflowExecutorBindings;
  peakConcurrent: () => number;
} {
  let active = 0;
  let peak = 0;
  const tick = (): Promise<void> => new Promise((r) => setTimeout(r, 5));
  return {
    peakConcurrent: () => peak,
    bindings: {
      async runAgent() {
        active++;
        peak = Math.max(peak, active);
        await tick();
        active--;
        return { usage: usage(), terminalKind: "success" as const };
      },
      async runJudge() {
        active++;
        peak = Math.max(peak, active);
        await tick();
        active--;
        return { verdict: "fail" as const, confidence: 0.8, usage: usage() };
      },
    },
  };
}

describe("SdkFanoutRunner (#3905)", () => {
  it("rejects an over-ceiling spec instead of truncating the fan-out", async () => {
    // 5 agents but maxTotal 4 → planned > ceiling → validateWorkflowSpec fails.
    const spec = makeSpec(5, 0, { ceiling: { maxConcurrent: 2, maxTotal: 4 } });
    const sink = new ArrayWorkflowEventSink();
    const { bindings } = makeTrackingExecutor();
    await expect(runSdkFanout(spec, sink, bindings)).rejects.toThrow(
      /exceeds ceiling|invalid WorkflowSpec/i
    );
    // Nothing should have been emitted — rejected before any spawn.
    expect(sink.getEvents()).toHaveLength(0);
  });

  it("rejects an empty-phase / wrong-schema spec", async () => {
    const sink = new ArrayWorkflowEventSink();
    const { bindings } = makeTrackingExecutor();
    await expect(runSdkFanout(makeSpec(0, 0, { phases: [] }), sink, bindings)).rejects.toThrow(
      /no phases/i
    );
  });

  it("never exceeds maxConcurrent agents running at once (semaphore enforced)", async () => {
    // 20 agents, ceiling maxConcurrent 3 → peak in-flight must stay <= 3.
    const spec = makeSpec(20, 0, { ceiling: { maxConcurrent: 3, maxTotal: 1000 } });
    const sink = new ArrayWorkflowEventSink();
    const { bindings, peakConcurrent } = makeTrackingExecutor();

    const summary = await runSdkFanout(spec, sink, bindings);

    expect(peakConcurrent()).toBeGreaterThan(1); // really did run in parallel
    expect(peakConcurrent()).toBeLessThanOrEqual(3); // never over the ceiling
    expect(summary.agentCount).toBe(20);
    expect(summary.agentsSucceeded).toBe(20);
    expect(summary.status).toBe("succeeded");
  });

  it("fans out agents CONCURRENTLY (all six overlap in-flight), not sequentially", async () => {
    // 6 agents, each holding a slot for one tick, with maxConcurrent 6. If the
    // runner ran them sequentially (or even two-at-a-time), `active` would
    // never reach 6 simultaneously and peakConcurrent() would top out below
    // that — a deterministic, wall-clock-independent proof of genuine
    // concurrency. This replaces a `Date.now()` timing bound (#96) that
    // duplicated this exact invariant with only a ~5ms margin and flaked
    // under scheduler jitter/CPU contention; the in-flight counter below
    // cannot flake since it never touches the clock.
    const spec = makeSpec(6, 0, { ceiling: { maxConcurrent: 6, maxTotal: 1000 } });
    const sink = new ArrayWorkflowEventSink();
    const { bindings, peakConcurrent } = makeTrackingExecutor();

    await runSdkFanout(spec, sink, bindings);

    expect(peakConcurrent()).toBe(6); // all six agents were in-flight at once
  });

  it("enforces the hard maxTotal ceiling independent of a generous USD budget", async () => {
    // A misconfigured huge budget must not let the spawn count exceed maxTotal.
    // 9 agents but maxTotal 8 → still rejected, budget notwithstanding.
    const spec = makeSpec(9, 0, {
      ceiling: { maxConcurrent: 4, maxTotal: 8 },
      budgetUsd: 1_000_000,
    });
    const sink = new ArrayWorkflowEventSink();
    const { bindings } = makeTrackingExecutor();
    await expect(runSdkFanout(spec, sink, bindings)).rejects.toThrow(/exceeds ceiling/i);
  });

  it("emits the canonical tree: run → phase → agents (running+terminal) → judges, parentId/seq correct", async () => {
    const spec = makeSpec(2, 1, { ceiling: { maxConcurrent: 2, maxTotal: 1000 } });
    const sink = new ArrayWorkflowEventSink();
    const { bindings } = makeTrackingExecutor();

    const summary = await runSdkFanout(spec, sink, bindings);
    const events = sink.getEvents();

    // seq is monotonic and dense across the whole stream.
    const seqs = events.map((e) => e.seq);
    expect(seqs).toEqual([...seqs].sort((a, b) => a - b));
    expect(seqs[0]).toBe(0);
    for (let i = 1; i < seqs.length; i++) {
      expect(seqs[i]).toBe(seqs[i - 1] + 1);
    }

    // Every event carries the right schema version and ISO ts.
    for (const e of events) {
      expect(e.schemaVersion).toBe(WORKFLOW_SCHEMA_VERSION);
      expect(e.ts).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/);
    }

    // First emission is the root run (running), backend sdk-fanout, parentId null.
    const first = events[0] as WorkflowRun;
    expect(isWorkflowRun(first)).toBe(true);
    expect(first.parentId).toBeNull();
    expect(first.backend).toBe("sdk-fanout");
    expect(first.status).toBe("running");
    const runNodeId = first.nodeId;

    // Last emission is the root run terminal (succeeded, finishedAt set).
    const last = events[events.length - 1] as WorkflowRun;
    expect(isWorkflowRun(last)).toBe(true);
    expect(last.nodeId).toBe(runNodeId);
    expect(last.status).toBe("succeeded");
    expect(last.finishedAt).toBeTruthy();

    // The phase node parents to the run.
    const phaseEvents = events.filter((e) => e.kind === "phase");
    expect(phaseEvents.length).toBe(2); // running + terminal
    const phaseNodeId = phaseEvents[0].nodeId;
    for (const p of phaseEvents) {
      expect(p.parentId).toBe(runNodeId);
    }

    // Agents: two nodes, each emitted twice (running then terminal), parenting
    // to the phase, with REQUIRED non-zero usage on the terminal emission.
    const agentEvents = events.filter((e): e is SubAgentNode => isSubAgentNode(e));
    expect(agentEvents.length).toBe(4); // 2 agents × (running + terminal)
    const agentNodeIds = new Set(agentEvents.map((a) => a.nodeId));
    expect(agentNodeIds.size).toBe(2);
    for (const a of agentEvents) {
      expect(a.parentId).toBe(phaseNodeId);
      expect(a.provider).toBe("codex");
    }
    const terminalAgents = agentEvents.filter((a) => a.status !== "running");
    expect(terminalAgents).toHaveLength(2);
    for (const a of terminalAgents) {
      expect(a.terminalKind).toBe("success");
      expect(a.usage.costUsd).toBeGreaterThan(0); // REQUIRED usage populated
      expect(a.usage.estimated).toBe(true); // executor's estimated flag carried
    }

    // Each agent's running emission precedes its terminal emission.
    for (const nodeId of agentNodeIds) {
      const forNode = agentEvents.filter((a) => a.nodeId === nodeId);
      expect(forNode[0].status).toBe("running");
      expect(forNode[forNode.length - 1].status).not.toBe("running");
    }

    // Judge: one verdict, parenting to the phase, targeting an agent node, with
    // REQUIRED usage. It is emitted AFTER both agents terminated (the barrier).
    const judgeEvents = events.filter((e): e is JudgeVerdict => isJudgeVerdict(e));
    expect(judgeEvents).toHaveLength(1);
    const judge = judgeEvents[0];
    expect(judge.parentId).toBe(phaseNodeId);
    expect(judge.verdict).toBe("fail");
    expect(agentNodeIds.has(judge.target)).toBe(true);
    expect(judge.usage.costUsd).toBeGreaterThan(0);
    const lastAgentSeq = Math.max(...terminalAgents.map((a) => a.seq));
    expect(judge.seq).toBeGreaterThan(lastAgentSeq);

    // Summary aggregates everything.
    expect(summary.agentCount).toBe(2);
    expect(summary.judgeCount).toBe(1);
    expect(summary.phases).toHaveLength(1);
    expect(summary.phases[0].judgesFail).toBe(1);
    // Aggregated usage: 2 agents + 1 judge, each costUsd 0.01.
    expect(summary.usage.costUsd).toBeCloseTo(0.03, 5);
    expect(summary.usage.estimated).toBe(true);
  });

  it("folds the stream into a consistent tree via latestByNode", async () => {
    const spec = makeSpec(3, 1, { ceiling: { maxConcurrent: 3, maxTotal: 1000 } });
    const sink = new ArrayWorkflowEventSink();
    const { bindings } = makeTrackingExecutor();
    await runSdkFanout(spec, sink, bindings);

    const latest = sink.latestByNode();
    // 1 run + 1 phase + 3 agents + 1 judge = 6 distinct nodes.
    expect(latest.size).toBe(6);
    // Folded agent state is the terminal one (last write wins by seq).
    const agents = [...latest.values()].filter((n) => n.kind === "agent");
    expect(agents).toHaveLength(3);
    for (const a of agents) {
      expect(a.status).toBe("succeeded");
    }
  });

  it("records a failed terminal when an agent executor throws, keeping the fan-out alive", async () => {
    const spec = makeSpec(3, 0, { ceiling: { maxConcurrent: 3, maxTotal: 1000 } });
    const sink = new ArrayWorkflowEventSink();
    let calls = 0;
    const bindings: WorkflowExecutorBindings = {
      async runAgent() {
        const mine = calls++;
        if (mine === 1) throw new Error("boom");
        return { usage: usage(), terminalKind: "success" as const };
      },
      async runJudge() {
        return { verdict: "pass" as const, usage: usage() };
      },
    };

    const summary = await runSdkFanout(spec, sink, bindings);
    expect(summary.agentsFailed).toBe(1);
    expect(summary.agentsSucceeded).toBe(2);
    expect(summary.status).toBe("failed");

    const failed = sink
      .getEvents()
      .filter((e): e is SubAgentNode => isSubAgentNode(e) && e.status === "failed");
    expect(failed).toHaveLength(1);
    expect(failed[0].terminalKind).toBe("error");
    // A bare throw has no real measurement → estimated-zero usage, never blank.
    expect(failed[0].usage).toEqual(zeroUsage(true));
  });

  it("preserves real usage + precise terminalKind when an agent throws AgentExecutionError (#3914)", async () => {
    const spec = makeSpec(2, 0, { ceiling: { maxConcurrent: 2, maxTotal: 1000 } });
    const sink = new ArrayWorkflowEventSink();
    // The partial usage burned before the agent hit its budget cap.
    const burned = usage({ inputTokens: 1234, outputTokens: 567, costUsd: 0.042 });
    let calls = 0;
    const bindings: WorkflowExecutorBindings = {
      async runAgent() {
        const mine = calls++;
        if (mine === 0) {
          throw new AgentExecutionError("budget-exceeded", burned, "hit cap");
        }
        return { usage: usage(), terminalKind: "success" as const };
      },
      async runJudge() {
        return { verdict: "pass" as const, usage: usage() };
      },
    };

    const summary = await runSdkFanout(spec, sink, bindings);
    expect(summary.agentsFailed).toBe(1);

    const failed = sink
      .getEvents()
      .filter((e): e is SubAgentNode => isSubAgentNode(e) && e.status === "failed");
    expect(failed).toHaveLength(1);
    // Terminal node carries the executor's classified kind and REAL burned usage
    // — never zeros + a generic "error" (the #3914 fan-out-side gap).
    expect(failed[0].terminalKind).toBe("budget-exceeded");
    expect(failed[0].usage).toEqual(burned);
    expect(failed[0].usage.costUsd).toBeGreaterThan(0);
    // The aggregate run usage includes the failed agent's burned tokens.
    expect(summary.usage.inputTokens).toBeGreaterThanOrEqual(1234);
  });

  it("emits judge nodes with a structured verdict (pass/fail/uncertain + confidence + usage) at completion time", async () => {
    const spec = makeSpec(1, 1, { ceiling: { maxConcurrent: 2, maxTotal: 1000 } });
    const sink = new ArrayWorkflowEventSink();
    const judgeUsage = usage({ inputTokens: 80, outputTokens: 20, costUsd: 0.003 });
    const bindings: WorkflowExecutorBindings = {
      async runAgent() {
        return { usage: usage(), terminalKind: "success" as const };
      },
      async runJudge() {
        return {
          verdict: "fail" as const,
          confidence: 0.91,
          rationale: "claim unproven",
          usage: judgeUsage,
        };
      },
    };

    await runSdkFanout(spec, sink, bindings);

    const judges = sink.getEvents().filter((e): e is JudgeVerdict => isJudgeVerdict(e));
    expect(judges).toHaveLength(1);
    expect(judges[0].verdict).toBe("fail");
    expect(judges[0].confidence).toBe(0.91);
    expect(judges[0].rationale).toBe("claim unproven");
    expect(judges[0].usage).toEqual(judgeUsage);
    // Judges consume budget like agents — usage is REQUIRED, not blank.
    expect(judges[0].usage.costUsd).toBeGreaterThan(0);
    expect(judges[0].status).toBe("succeeded");
  });

  it("emits an uncertain judge verdict with estimated-zero usage when the judge throws", async () => {
    const spec = makeSpec(1, 1, { ceiling: { maxConcurrent: 2, maxTotal: 1000 } });
    const sink = new ArrayWorkflowEventSink();
    const bindings: WorkflowExecutorBindings = {
      async runAgent() {
        return { usage: usage(), terminalKind: "success" as const };
      },
      async runJudge() {
        throw new Error("judge crashed");
      },
    };

    await runSdkFanout(spec, sink, bindings);

    const judges = sink.getEvents().filter((e): e is JudgeVerdict => isJudgeVerdict(e));
    expect(judges).toHaveLength(1);
    expect(judges[0].verdict).toBe("uncertain");
    expect(judges[0].status).toBe("failed");
    expect(judges[0].usage).toEqual(zeroUsage(true));
  });

  it("runs phases sequentially with correct index/total wiring", async () => {
    const spec: WorkflowSpec = {
      schemaVersion: WORKFLOW_SCHEMA_VERSION,
      runId: "run-2",
      phases: [
        { name: "find", agents: [{ agentId: "a", prompt: "p", provider: "codex" }] },
        { name: "verify", agents: [{ agentId: "b", prompt: "p", provider: "codex" }] },
      ],
      ceiling: { maxConcurrent: 2, maxTotal: 1000 },
    };
    const sink = new ArrayWorkflowEventSink();
    const { bindings } = makeTrackingExecutor();
    await runSdkFanout(spec, sink, bindings);

    const phaseRunning = sink
      .getEvents()
      .filter((e): e is Extract<WorkflowEvent, { kind: "phase" }> => e.kind === "phase")
      .filter((p) => p.status === "running");
    expect(phaseRunning.map((p) => [p.name, p.index, p.total])).toEqual([
      ["find", 0, 2],
      ["verify", 1, 2],
    ]);
  });
});
