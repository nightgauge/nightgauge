/**
 * Unit tests for the V4 workflow-outcome fold (Issue #3915).
 *
 * Proves the consumer-side ingestion of the canonical schemaVersion-4
 * WorkflowEvent node tree: a tree with nested agents[]/judgeVerdict folds into
 * the expected outcome record, aggregate usage equals the summed agent + judge
 * usage, and the learning signals (judge-rejection rate, fan-out efficiency,
 * native-vs-fanout cost delta) are derived correctly.
 */

import { describe, it, expect } from "vitest";
import {
  foldWorkflowOutcome,
  foldWorkflowOutcomes,
  summarizeWorkflowOutcomes,
} from "../WorkflowOutcomeAnalyzer.js";
import {
  runSdkFanout,
  ArrayWorkflowEventSink,
  WORKFLOW_SCHEMA_VERSION,
  type WorkflowEvent,
  type WorkflowAgentUsage,
  type WorkflowSpec,
  type WorkflowExecutorBindings,
  type AgentExecutionResult,
  type JudgeExecutionResult,
} from "../../cli/workflow/index.js";

// ── Helpers ──────────────────────────────────────────────────────────

function usage(over: Partial<WorkflowAgentUsage> = {}): WorkflowAgentUsage {
  return {
    inputTokens: 100,
    outputTokens: 50,
    cacheReadTokens: 0,
    cacheCreationTokens: 0,
    costUsd: 0.01,
    estimated: false,
    ...over,
  };
}

/** Build a minimal but realistic V4 event stream by hand. */
function handBuiltTree(): WorkflowEvent[] {
  const v = WORKFLOW_SCHEMA_VERSION;
  let seq = 0;
  const ts = "2026-06-03T00:00:00.000Z";
  const runId = "run-1";
  const runNodeId = `run:${runId}`;
  const phaseNodeId = `phase:${runId}:0`;
  const ev: WorkflowEvent[] = [];

  ev.push({
    schemaVersion: v,
    kind: "run",
    nodeId: runNodeId,
    parentId: null,
    seq: seq++,
    ts,
    status: "running",
    runId,
    issueNumber: 42,
    stage: "feature-dev",
    backend: "sdk-fanout",
    startedAt: ts,
  });
  ev.push({
    schemaVersion: v,
    kind: "phase",
    nodeId: phaseNodeId,
    parentId: runNodeId,
    seq: seq++,
    ts,
    status: "running",
    name: "find",
    index: 0,
    total: 1,
  });
  // Two agents: one succeeds, one fails.
  ev.push({
    schemaVersion: v,
    kind: "agent",
    nodeId: `agent:${runId}:0:0`,
    parentId: phaseNodeId,
    seq: seq++,
    ts,
    status: "succeeded",
    agentId: "a0",
    provider: "claude",
    usage: usage({ inputTokens: 200, outputTokens: 100, costUsd: 0.02 }),
    terminalKind: "success",
  });
  ev.push({
    schemaVersion: v,
    kind: "agent",
    nodeId: `agent:${runId}:0:1`,
    parentId: phaseNodeId,
    seq: seq++,
    ts,
    status: "failed",
    agentId: "a1",
    provider: "codex",
    usage: usage({ inputTokens: 50, outputTokens: 10, costUsd: 0.005, estimated: true }),
    terminalKind: "error",
  });
  // Two judges: one pass, one fail.
  ev.push({
    schemaVersion: v,
    kind: "judge",
    nodeId: `judge:${runId}:0:0`,
    parentId: phaseNodeId,
    seq: seq++,
    ts,
    status: "succeeded",
    judgeId: "j0",
    provider: "claude",
    target: `agent:${runId}:0:0`,
    verdict: "pass",
    usage: usage({ inputTokens: 30, outputTokens: 5, costUsd: 0.001 }),
  });
  ev.push({
    schemaVersion: v,
    kind: "judge",
    nodeId: `judge:${runId}:0:1`,
    parentId: phaseNodeId,
    seq: seq++,
    ts,
    status: "succeeded",
    judgeId: "j1",
    provider: "claude",
    target: `agent:${runId}:0:0`,
    verdict: "fail",
    usage: usage({ inputTokens: 30, outputTokens: 5, costUsd: 0.001 }),
  });
  ev.push({
    schemaVersion: v,
    kind: "phase",
    nodeId: phaseNodeId,
    parentId: runNodeId,
    seq: seq++,
    ts,
    status: "failed",
    name: "find",
    index: 0,
    total: 1,
  });
  ev.push({
    schemaVersion: v,
    kind: "run",
    nodeId: runNodeId,
    parentId: null,
    seq,
    ts,
    status: "failed",
    runId,
    issueNumber: 42,
    stage: "feature-dev",
    backend: "sdk-fanout",
    startedAt: ts,
    finishedAt: ts,
  });
  return ev;
}

// ── foldWorkflowOutcome ──────────────────────────────────────────────

describe("foldWorkflowOutcome", () => {
  it("folds a V4 tree with nested agents[]/judgeVerdict into the expected record", () => {
    const outcome = foldWorkflowOutcome(handBuiltTree());
    expect(outcome).not.toBeNull();
    const o = outcome!;

    expect(o.runId).toBe("run-1");
    expect(o.issueNumber).toBe(42);
    expect(o.stage).toBe("feature-dev");
    expect(o.backend).toBe("sdk-fanout");
    expect(o.status).toBe("failed");

    expect(o.agentCount).toBe(2);
    expect(o.agentsSucceeded).toBe(1);
    expect(o.agentsFailed).toBe(1);
    expect(o.judgeCount).toBe(2);
    expect(o.judgesPass).toBe(1);
    expect(o.judgesFail).toBe(1);
    expect(o.judgesUncertain).toBe(0);

    // judge-rejection rate = fail / total verdicts = 1/2
    expect(o.judgeRejectionRate).toBe(0.5);
    // fan-out efficiency = succeeded / total agents = 1/2
    expect(o.fanoutEfficiency).toBe(0.5);
  });

  it("aggregate usage equals the summed agent + judge usage", () => {
    const events = handBuiltTree();
    const o = foldWorkflowOutcome(events)!;

    // Sum the leaf usages independently from the latest emission per node.
    const latest = new Map<string, WorkflowEvent>();
    for (const ev of events) {
      const prior = latest.get(ev.nodeId);
      if (!prior || ev.seq >= prior.seq) latest.set(ev.nodeId, ev);
    }
    let inSum = 0;
    let outSum = 0;
    let costSum = 0;
    let anyEstimated = false;
    for (const node of latest.values()) {
      if (node.kind === "agent" || node.kind === "judge") {
        inSum += node.usage.inputTokens;
        outSum += node.usage.outputTokens;
        costSum += node.usage.costUsd;
        anyEstimated = anyEstimated || node.usage.estimated;
      }
    }

    expect(o.usage.inputTokens).toBe(inSum);
    expect(o.usage.outputTokens).toBe(outSum);
    expect(o.usage.costUsd).toBeCloseTo(costSum, 10);
    // One agent reported estimated usage → sticky-true.
    expect(o.usage.estimated).toBe(anyEstimated);
    expect(o.usage.estimated).toBe(true);
  });

  it("attributes agent + judge counts and usage to their phase", () => {
    const o = foldWorkflowOutcome(handBuiltTree())!;
    expect(o.phases).toHaveLength(1);
    const phase = o.phases[0];
    expect(phase.name).toBe("find");
    expect(phase.agentsSucceeded).toBe(1);
    expect(phase.agentsFailed).toBe(1);
    expect(phase.judgesPass).toBe(1);
    expect(phase.judgesFail).toBe(1);
    // Phase usage equals the run usage (single phase).
    expect(phase.usage.costUsd).toBeCloseTo(o.usage.costUsd, 10);
  });

  it("folds by (nodeId, max seq) so a stale earlier emission never wins", () => {
    const events = handBuiltTree();
    // Append a stale running re-emission of an agent at a LOWER seq — must lose.
    const succeededAgent = events.find(
      (e): e is Extract<WorkflowEvent, { kind: "agent" }> =>
        e.kind === "agent" && e.status === "succeeded"
    )!;
    events.push({
      ...succeededAgent,
      seq: -1,
      status: "running",
      usage: usage({ inputTokens: 9999, costUsd: 9.99 }),
    });
    const o = foldWorkflowOutcome(events)!;
    // The succeeded agent still counts as succeeded and its real usage stands.
    expect(o.agentsSucceeded).toBe(1);
    expect(o.usage.inputTokens).toBeLessThan(9999);
  });

  it("returns null when the stream has no root run node", () => {
    const events = handBuiltTree().filter((e) => e.kind !== "run");
    expect(foldWorkflowOutcome(events)).toBeNull();
  });

  it("reports null rates when no agent or judge ran", () => {
    const ts = "2026-06-03T00:00:00.000Z";
    const events: WorkflowEvent[] = [
      {
        schemaVersion: WORKFLOW_SCHEMA_VERSION,
        kind: "run",
        nodeId: "run:empty",
        parentId: null,
        seq: 0,
        ts,
        status: "succeeded",
        runId: "empty",
        backend: "native-workflow",
        startedAt: ts,
        finishedAt: ts,
      },
    ];
    const o = foldWorkflowOutcome(events)!;
    expect(o.agentCount).toBe(0);
    expect(o.judgeCount).toBe(0);
    expect(o.judgeRejectionRate).toBeNull();
    expect(o.fanoutEfficiency).toBeNull();
  });

  it("ignores malformed emissions without throwing", () => {
    const events = handBuiltTree();
    // @ts-expect-error — deliberately malformed (no nodeId/seq).
    events.push({ kind: "agent" });
    expect(() => foldWorkflowOutcome(events)).not.toThrow();
    const o = foldWorkflowOutcome(events)!;
    expect(o.agentCount).toBe(2); // malformed node ignored
  });
});

// ── Producer → consumer round-trip via runSdkFanout ──────────────────

describe("foldWorkflowOutcome — round-trips the SdkFanoutRunner emissions", () => {
  it("matches the runner's own summary for the same run", async () => {
    const spec: WorkflowSpec = {
      schemaVersion: WORKFLOW_SCHEMA_VERSION,
      runId: "rt-1",
      issueNumber: 7,
      stage: "feature-validate",
      ceiling: { maxConcurrent: 4, maxTotal: 16 },
      phases: [
        {
          name: "verify",
          agents: [
            { agentId: "x0", provider: "claude", prompt: "find" },
            { agentId: "x1", provider: "claude", prompt: "find" },
            { agentId: "x2", provider: "claude", prompt: "find" },
          ],
          judges: [{ judgeId: "jv", provider: "claude", prompt: "judge" }],
        },
      ],
    };

    const bindings: WorkflowExecutorBindings = {
      async runAgent(agent): Promise<AgentExecutionResult> {
        // x2 fails; the rest succeed.
        if (agent.agentId === "x2") {
          return { usage: usage({ costUsd: 0.003 }), terminalKind: "error" };
        }
        return { usage: usage({ costUsd: 0.01 }), terminalKind: "success" };
      },
      async runJudge(): Promise<JudgeExecutionResult> {
        return { verdict: "fail", usage: usage({ costUsd: 0.002 }) };
      },
    };

    const sink = new ArrayWorkflowEventSink();
    const summary = await runSdkFanout(spec, sink, bindings);
    const o = foldWorkflowOutcome(sink.getEvents())!;

    expect(o.agentCount).toBe(summary.agentCount);
    expect(o.agentsSucceeded).toBe(summary.agentsSucceeded);
    expect(o.agentsFailed).toBe(summary.agentsFailed);
    expect(o.judgeCount).toBe(summary.judgeCount);
    expect(o.usage.costUsd).toBeCloseTo(summary.usage.costUsd, 10);
    expect(o.usage.inputTokens).toBe(summary.usage.inputTokens);
    // 2/3 agents succeeded.
    expect(o.fanoutEfficiency).toBeCloseTo(2 / 3, 10);
    // The single judge failed → 100% rejection.
    expect(o.judgeRejectionRate).toBe(1);
  });
});

// ── Multi-run fold + calibration summary ─────────────────────────────

describe("foldWorkflowOutcomes + summarizeWorkflowOutcomes", () => {
  it("buckets a multi-run stream into one outcome per run", () => {
    const a = handBuiltTree();
    const b = handBuiltTree().map((e) => ({
      ...e,
      nodeId: e.nodeId.replace("run-1", "run-2"),
      parentId: e.parentId?.replace("run-1", "run-2") ?? null,
      ...(e.kind === "run" ? { runId: "run-2" } : {}),
    })) as WorkflowEvent[];

    const outcomes = foldWorkflowOutcomes([...a, ...b]);
    expect(outcomes).toHaveLength(2);
    expect(new Set(outcomes.map((o) => o.runId))).toEqual(new Set(["run-1", "run-2"]));
  });

  it("computes the native-vs-fanout cost delta only when both backends ran", () => {
    const native = foldWorkflowOutcome(
      handBuiltTree().map((e) => (e.kind === "run" ? { ...e, backend: "native-workflow" } : e))
    )!;
    const fanout = foldWorkflowOutcome(handBuiltTree())!;

    // Only native runs → delta is null (one-sided).
    const oneSided = summarizeWorkflowOutcomes([native]);
    expect(oneSided.nativeVsFanoutCostDeltaUsd).toBeNull();
    expect(oneSided.nativeRunCount).toBe(1);
    expect(oneSided.fanoutRunCount).toBe(0);

    // Both backends → delta = nativeMean − fanoutMean.
    const both = summarizeWorkflowOutcomes([native, fanout]);
    expect(both.nativeRunCount).toBe(1);
    expect(both.fanoutRunCount).toBe(1);
    expect(both.nativeVsFanoutCostDeltaUsd).toBeCloseTo(
      native.usage.costUsd - fanout.usage.costUsd,
      10
    );
    // Both runs have one failing judge of two → mean rejection rate 0.5.
    expect(both.meanJudgeRejectionRate).toBeCloseTo(0.5, 10);
    expect(both.meanFanoutEfficiency).toBeCloseTo(0.5, 10);
    expect(both.totalAgents).toBe(4);
    expect(both.totalJudges).toBe(4);
  });

  it("yields an empty array for a stream with no root run", () => {
    expect(foldWorkflowOutcomes([])).toEqual([]);
  });
});
