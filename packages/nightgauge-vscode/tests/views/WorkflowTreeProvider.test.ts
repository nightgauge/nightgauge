/**
 * Tests for WorkflowTreeProvider rendering + live updates (#3919).
 *
 * Drives the provider with a fake {@link WorkflowEventSource} (no real SSE) and
 * asserts the rendered tree: the run row's fan-out counter + lanes-busy gauge,
 * the per-agent token/cost description, and the judge verdict badges. Uses the
 * global `vscode` mock from tests/setup.ts.
 */

import { describe, it, expect } from "vitest";
import type { WorkflowEvent, WorkflowAgentUsage } from "@nightgauge/sdk";
import {
  WorkflowTreeProvider,
  type WorkflowEventSource,
} from "../../src/views/workflow/WorkflowTreeProvider";
import type { WorkflowTreeItem } from "../../src/views/workflow/workflowTreeItems";

/** A controllable event source: tests call `emit` to push nodes synchronously. */
class FakeSource implements WorkflowEventSource {
  private listeners: Array<(e: WorkflowEvent) => void> = [];
  readonly onWorkflowEvent = (listener: (e: WorkflowEvent) => void) => {
    this.listeners.push(listener);
    return { dispose: () => {} };
  };
  emit(event: WorkflowEvent): void {
    for (const l of this.listeners) l(event);
  }
}

function usage(costUsd: number, estimated = false): WorkflowAgentUsage {
  return {
    inputTokens: 1200,
    outputTokens: 300,
    cacheReadTokens: 0,
    cacheCreationTokens: 0,
    costUsd,
    estimated,
  };
}

const ISSUE = 42;
const RUN_ID = `run:${ISSUE}`;

const runNode: WorkflowEvent = {
  schemaVersion: 4,
  kind: "run",
  nodeId: RUN_ID,
  parentId: null,
  seq: 0,
  ts: "2026-05-23T10:00:00Z",
  status: "running",
  runId: RUN_ID,
  issueNumber: ISSUE,
  backend: "sdk-fanout",
  startedAt: "2026-05-23T10:00:00Z",
};

const phaseNode: WorkflowEvent = {
  schemaVersion: 4,
  kind: "phase",
  nodeId: `phase:${ISSUE}:find`,
  parentId: RUN_ID,
  seq: 1,
  ts: "2026-05-23T10:00:01Z",
  status: "running",
  name: "find",
  index: 0,
  total: 6,
};

function agent(
  id: string,
  seq: number,
  status: WorkflowEvent["status"],
  u: WorkflowAgentUsage
): WorkflowEvent {
  return {
    schemaVersion: 4,
    kind: "agent",
    nodeId: `agent:${ISSUE}:${id}`,
    parentId: `phase:${ISSUE}:find`,
    seq,
    ts: "2026-05-23T10:00:02Z",
    status,
    agentId: id,
    provider: "codex",
    usage: u,
  };
}

async function children(provider: WorkflowTreeProvider, el?: WorkflowTreeItem) {
  return (await provider.getChildren(el)) as WorkflowTreeItem[];
}

describe("WorkflowTreeProvider", () => {
  it("shows a placeholder before any run emits", async () => {
    const provider = new WorkflowTreeProvider();
    const roots = await children(provider);
    expect(roots).toHaveLength(1);
    expect(roots[0].contextValue).toBe("workflow.empty");
    provider.dispose();
  });

  it("renders run → phase → agent after emissions, with the lanes gauge", async () => {
    const provider = new WorkflowTreeProvider();
    const source = new FakeSource();
    provider.attach(source);

    source.emit(runNode);
    source.emit(phaseNode);
    source.emit(agent("a", 2, "succeeded", usage(0.02)));
    source.emit(agent("b", 3, "running", usage(0.01)));

    const roots = await children(provider);
    expect(roots).toHaveLength(1);
    const run = roots[0];
    expect(run.contextValue).toBe("workflow.run");
    // fan-out counter: 1 of 2 agents succeeded; lanes gauge uses the fan-out
    // ceiling (6) and reports the real busy lower bound (1 running).
    expect(String(run.description)).toContain("1/2 agents");
    expect(String(run.description)).toContain("1 of 6 lanes busy");

    const phases = await children(provider, run);
    expect(phases).toHaveLength(1);
    expect(phases[0].contextValue).toBe("workflow.phase");

    const agents = await children(provider, phases[0]);
    expect(agents.map((a) => a.contextValue)).toEqual(["workflow.agent", "workflow.agent"]);
    // per-agent token + cost in the description
    expect(String(agents[0].description)).toContain("codex");
    expect(String(agents[0].description)).toContain("tok");
    expect(String(agents[0].description)).toContain("$");

    provider.dispose();
  });

  it("labels sdk-fanout costs as estimates and shows a judge fail badge", async () => {
    const provider = new WorkflowTreeProvider();
    const source = new FakeSource();
    provider.attach(source);

    source.emit(runNode);
    source.emit(phaseNode);
    source.emit(agent("a", 2, "succeeded", usage(0.02, true)));
    source.emit({
      schemaVersion: 4,
      kind: "judge",
      nodeId: `judge:${ISSUE}:j1`,
      parentId: `phase:${ISSUE}:find`,
      seq: 3,
      ts: "2026-05-23T10:00:03Z",
      status: "succeeded",
      judgeId: "j1",
      provider: "claude",
      target: `agent:${ISSUE}:a`,
      verdict: "fail",
      rationale: "missing test coverage",
      usage: usage(0.001),
    });

    const run = (await children(provider))[0];
    // a judge rejected the agent → fan-out counter surfaces it
    expect(String(run.description)).toContain("1 rejected");

    const phase = (await children(provider, run))[0];
    const agentItem = (await children(provider, phase))[0];
    // estimate label + fan-out "gate" verdict badge
    expect(String(agentItem.description)).toContain("est");
    expect(String(agentItem.description)).toContain("gate");

    // judge leaf renders as a "gate verification" with the verdict
    const judges = await children(provider, agentItem);
    expect(judges).toHaveLength(1);
    expect(judges[0].contextValue).toBe("workflow.judge");
    expect(String(judges[0].label)).toContain("gate verification");

    provider.dispose();
  });

  it("uses the native ceiling (16 lanes) for native-workflow runs", async () => {
    const provider = new WorkflowTreeProvider();
    const source = new FakeSource();
    provider.attach(source);

    source.emit({ ...runNode, backend: "native-workflow" });
    source.emit(phaseNode);
    source.emit(agent("a", 2, "running", usage(0.02)));

    const run = (await children(provider))[0];
    expect(String(run.description)).toContain("1 of 16 lanes busy");
    provider.dispose();
  });

  it("attach is idempotent for the same source", async () => {
    const provider = new WorkflowTreeProvider();
    const source = new FakeSource();
    provider.attach(source);
    provider.attach(source); // second attach must not double-subscribe

    source.emit(runNode);
    source.emit(phaseNode);
    source.emit(agent("a", 2, "succeeded", usage(0.02)));

    const run = (await children(provider))[0];
    // one agent, not duplicated by a second subscription
    expect(String(run.description)).toContain("1/1 agents");
    provider.dispose();
  });

  it("reset clears the folded tree back to the placeholder", async () => {
    const provider = new WorkflowTreeProvider();
    const source = new FakeSource();
    provider.attach(source);
    source.emit(runNode);

    expect((await children(provider))[0].contextValue).toBe("workflow.run");
    provider.reset();
    expect((await children(provider))[0].contextValue).toBe("workflow.empty");
    provider.dispose();
  });
});
