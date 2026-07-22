/**
 * Tests for the live workflow tree fold (#3919).
 *
 * Covers the last-write-wins-by-seq fold from a flat {@link WorkflowEvent}
 * stream into the run → phase → agent → judge hierarchy, the out-of-order
 * guard, and the run aggregate (fan-out counter, lanes-busy gauge inputs,
 * judge-rejection count, rolled-up cost + estimate flag).
 */

import { describe, it, expect } from "vitest";
import type { WorkflowEvent, WorkflowAgentUsage } from "@nightgauge/sdk";
import {
  WorkflowTreeModel,
  aggregateRun,
  latestJudge,
} from "../../src/views/workflow/workflowTreeModel";

function usage(costUsd: number, estimated = false): WorkflowAgentUsage {
  return {
    inputTokens: 100,
    outputTokens: 50,
    cacheReadTokens: 0,
    cacheCreationTokens: 0,
    costUsd,
    estimated,
  };
}

const ISSUE = 42;
const RUN_ID = `run:${ISSUE}`;

function runEvent(
  seq: number,
  status: WorkflowEvent["status"],
  backend = "sdk-fanout"
): WorkflowEvent {
  return {
    schemaVersion: 4,
    kind: "run",
    nodeId: RUN_ID,
    parentId: null,
    seq,
    ts: new Date(seq * 1000).toISOString(),
    status,
    runId: RUN_ID,
    issueNumber: ISSUE,
    backend: backend as "sdk-fanout" | "native-workflow",
    startedAt: "2026-05-23T10:00:00Z",
  };
}

function phaseEvent(
  seq: number,
  name: string,
  index: number,
  status: WorkflowEvent["status"]
): WorkflowEvent {
  return {
    schemaVersion: 4,
    kind: "phase",
    nodeId: `phase:${ISSUE}:${name}`,
    parentId: RUN_ID,
    seq,
    ts: new Date(seq * 1000).toISOString(),
    status,
    name,
    index,
    total: 6,
  };
}

function agentEvent(
  seq: number,
  phase: string,
  agentId: string,
  status: WorkflowEvent["status"],
  u: WorkflowAgentUsage
): WorkflowEvent {
  return {
    schemaVersion: 4,
    kind: "agent",
    nodeId: `agent:${ISSUE}:${agentId}`,
    parentId: `phase:${ISSUE}:${phase}`,
    seq,
    ts: new Date(seq * 1000).toISOString(),
    status,
    agentId,
    provider: "codex",
    usage: u,
  };
}

function judgeEvent(
  seq: number,
  judgeId: string,
  target: string,
  verdict: "pass" | "fail" | "uncertain",
  u: WorkflowAgentUsage
): WorkflowEvent {
  return {
    schemaVersion: 4,
    kind: "judge",
    nodeId: `judge:${ISSUE}:${judgeId}`,
    parentId: `phase:${ISSUE}:find`,
    seq,
    ts: new Date(seq * 1000).toISOString(),
    status: "succeeded",
    judgeId,
    provider: "claude",
    target,
    verdict,
    usage: u,
  };
}

describe("WorkflowTreeModel.apply (last-write-wins by seq)", () => {
  it("folds run → phase → agent → judge into one tree", () => {
    const model = new WorkflowTreeModel();
    model.apply(runEvent(0, "running"));
    model.apply(phaseEvent(1, "find", 0, "running"));
    model.apply(agentEvent(2, "find", "finder", "running", usage(0)));
    model.apply(judgeEvent(3, "j1", `agent:${ISSUE}:finder`, "pass", usage(0.001)));

    const runs = model.runs();
    expect(runs).toHaveLength(1);
    expect(runs[0].node.nodeId).toBe(RUN_ID);
    expect(runs[0].phases).toHaveLength(1);
    expect(runs[0].phases[0].node.name).toBe("find");
    expect(runs[0].phases[0].agents).toHaveLength(1);
    expect(runs[0].phases[0].agents[0].node.agentId).toBe("finder");
    expect(runs[0].phases[0].agents[0].judges).toHaveLength(1);
    expect(runs[0].phases[0].agents[0].judges[0].verdict).toBe("pass");
  });

  it("keeps the highest-seq emission per nodeId (state, not delta)", () => {
    const model = new WorkflowTreeModel();
    model.apply(runEvent(0, "running"));
    model.apply(phaseEvent(1, "find", 0, "running"));
    model.apply(agentEvent(2, "find", "finder", "running", usage(0.001)));
    // newer emission for the same agent — succeeded with final usage
    expect(model.apply(agentEvent(7, "find", "finder", "succeeded", usage(0.05)))).toBe(true);

    const agent = model.runs()[0].phases[0].agents[0];
    expect(agent.node.status).toBe("succeeded");
    expect(agent.node.usage.costUsd).toBeCloseTo(0.05);
  });

  it("ignores an out-of-order (lower-seq) emission", () => {
    const model = new WorkflowTreeModel();
    model.apply(runEvent(0, "running"));
    model.apply(phaseEvent(1, "find", 0, "running"));
    model.apply(agentEvent(7, "find", "finder", "succeeded", usage(0.05)));
    // stale replay arrives late with a lower seq — must be dropped
    expect(model.apply(agentEvent(2, "find", "finder", "running", usage(0.001)))).toBe(false);

    const agent = model.runs()[0].phases[0].agents[0];
    expect(agent.node.status).toBe("succeeded");
    expect(agent.node.usage.costUsd).toBeCloseTo(0.05);
  });

  it("orders phases by index and agents/judges by seq", () => {
    const model = new WorkflowTreeModel();
    model.apply(runEvent(0, "running"));
    model.apply(phaseEvent(10, "verify", 1, "running"));
    model.apply(phaseEvent(1, "find", 0, "running"));
    model.apply(agentEvent(3, "find", "b", "running", usage(0)));
    model.apply(agentEvent(2, "find", "a", "running", usage(0)));

    const runs = model.runs();
    expect(runs[0].phases.map((p) => p.node.name)).toEqual(["find", "verify"]);
    expect(runs[0].phases[0].agents.map((a) => a.node.agentId)).toEqual(["a", "b"]);
  });

  it("supports multiple concurrent runs keyed by root nodeId", () => {
    const model = new WorkflowTreeModel();
    model.apply(runEvent(0, "running"));
    model.apply({ ...runEvent(0, "running"), nodeId: "run:99", runId: "run:99", issueNumber: 99 });

    expect(
      model
        .runs()
        .map((r) => r.node.nodeId)
        .sort()
    ).toEqual(["run:42", "run:99"]);
  });

  it("clear() drops all folded state", () => {
    const model = new WorkflowTreeModel();
    model.apply(runEvent(0, "running"));
    expect(model.size).toBe(1);
    model.clear();
    expect(model.size).toBe(0);
    expect(model.runs()).toHaveLength(0);
  });
});

describe("aggregateRun (fan-out counter + lanes gauge + cost)", () => {
  it("counts succeeded / failed / running agents and rejections", () => {
    const model = new WorkflowTreeModel();
    model.apply(runEvent(0, "running"));
    model.apply(phaseEvent(1, "find", 0, "running"));
    model.apply(agentEvent(2, "find", "a", "succeeded", usage(0.01)));
    model.apply(agentEvent(3, "find", "b", "failed", usage(0.02)));
    model.apply(agentEvent(4, "find", "c", "running", usage(0.005)));
    // a judge rejects agent "a"
    model.apply(judgeEvent(5, "j", `agent:${ISSUE}:a`, "fail", usage(0.001)));

    const agg = model.runs()[0].aggregate;
    expect(agg.totalAgents).toBe(3);
    expect(agg.succeededAgents).toBe(1);
    expect(agg.failedAgents).toBe(1);
    expect(agg.runningAgents).toBe(1);
    expect(agg.rejectedByJudge).toBe(1);
    expect(agg.totalCostUsd).toBeCloseTo(0.01 + 0.02 + 0.005 + 0.001);
  });

  it("flags estimated cost when any agent reports an estimate", () => {
    const phases = [
      {
        node: phaseEvent(1, "find", 0, "running") as never,
        agents: [
          {
            node: agentEvent(2, "find", "a", "succeeded", usage(0.01, true)) as never,
            judges: [],
          },
        ],
      },
    ];
    const agg = aggregateRun(phases as never);
    expect(agg.estimatedCost).toBe(true);
  });
});

describe("latestJudge", () => {
  it("returns the highest-seq judge verdict for an agent", () => {
    const agent = {
      node: agentEvent(2, "find", "a", "succeeded", usage(0)) as never,
      judges: [
        judgeEvent(3, "j1", `agent:${ISSUE}:a`, "fail", usage(0)),
        judgeEvent(8, "j2", `agent:${ISSUE}:a`, "pass", usage(0)),
      ] as never,
    };
    expect(latestJudge(agent as never)?.verdict).toBe("pass");
  });

  it("returns null when an agent has no judges", () => {
    const agent = { node: agentEvent(2, "find", "a", "running", usage(0)) as never, judges: [] };
    expect(latestJudge(agent as never)).toBeNull();
  });
});
