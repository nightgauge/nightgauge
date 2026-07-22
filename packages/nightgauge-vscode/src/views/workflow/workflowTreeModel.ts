/**
 * workflowTreeModel — pure folding logic for the live workflow event tree.
 *
 * The extension subscribes to the SDK EventBus node-tree stream (re-served over
 * SSE) and folds the append-only {@link WorkflowEvent} emissions into a live
 * `run → phase → agent → judge` hierarchy. Folding is last-write-wins by `seq`
 * per `nodeId`: a later emission for the same node replaces an earlier one, an
 * out-of-order (lower-seq) emission is ignored, and parent/child links are
 * resolved by `parentId`. The result is a snapshot the {@link WorkflowTreeProvider}
 * renders directly.
 *
 * This module is intentionally free of any `vscode` dependency so the folding is
 * unit-tested in isolation (the #3919 tree-folding tests target it directly).
 *
 * @see Issue #3919 — live workflow sidebar tree
 * @see @nightgauge/sdk — WorkflowEvent canonical contract
 */

import {
  isWorkflowRun,
  isSubAgentNode,
  isJudgeVerdict,
  type WorkflowEvent,
  type WorkflowNode,
  type WorkflowRun,
  type WorkflowPhase,
  type SubAgentNode,
  type JudgeVerdict,
  type WorkflowAgentUsage,
} from "@nightgauge/sdk";

/** An agent node plus the judge verdicts that target it. */
export interface FoldedAgent {
  node: SubAgentNode;
  /** Judges whose `target` is this agent's `nodeId`, ordered by `seq`. */
  judges: JudgeVerdict[];
}

/** A phase node plus its child agents (each with its judges). */
export interface FoldedPhase {
  node: WorkflowPhase;
  agents: FoldedAgent[];
}

/**
 * Aggregate counters surfaced on the run row.
 *
 * - `totalAgents` / `succeededAgents` / `failedAgents` drive the fan-out counter
 *   ("7/7 agents, 2 rejected").
 * - `runningAgents` / `concurrencyCeiling` drive the lanes-busy gauge
 *   ("N of 16 lanes busy"). For `sdk-fanout` runs the busy count is the real
 *   lower bound, so the gauge is honest about portable-floor concurrency.
 * - `rejectedByJudge` counts agents whose latest targeting judge verdict is
 *   `fail` (a judge rejection that typically triggers a re-spawn).
 */
export interface RunAggregate {
  totalAgents: number;
  runningAgents: number;
  succeededAgents: number;
  failedAgents: number;
  rejectedByJudge: number;
  totalCostUsd: number;
  estimatedCost: boolean;
}

/** A fully folded run: the root node, its phases, and derived aggregates. */
export interface FoldedRun {
  node: WorkflowRun;
  phases: FoldedPhase[];
  aggregate: RunAggregate;
}

const ZERO_USAGE: WorkflowAgentUsage = {
  inputTokens: 0,
  outputTokens: 0,
  cacheReadTokens: 0,
  cacheCreationTokens: 0,
  costUsd: 0,
  estimated: false,
};

/**
 * Folds a {@link WorkflowEvent} stream into live run trees, last-write-wins by
 * `seq` per `nodeId`. Apply each emission as it arrives; read the snapshot via
 * {@link runs}. Multiple concurrent runs are supported — each root `run` node is
 * folded independently and keyed by its `nodeId`.
 */
export class WorkflowTreeModel {
  /** Latest emission per nodeId (last-write-wins by seq). */
  private readonly latest = new Map<string, WorkflowNode>();
  /** Highest seq seen per nodeId — guards against out-of-order replay. */
  private readonly maxSeq = new Map<string, number>();

  /**
   * Apply one emission. Returns `true` when it changed the model (newer seq for
   * a node), `false` when it was a stale/out-of-order duplicate and was ignored.
   */
  apply(event: WorkflowEvent): boolean {
    const prevSeq = this.maxSeq.get(event.nodeId);
    if (prevSeq !== undefined && event.seq < prevSeq) {
      return false; // stale, out-of-order emission — keep the newer state
    }
    this.latest.set(event.nodeId, event);
    this.maxSeq.set(event.nodeId, event.seq);
    return true;
  }

  /** Drop all folded state (e.g. when the view is cleared). */
  clear(): void {
    this.latest.clear();
    this.maxSeq.clear();
  }

  /** Number of distinct nodes currently folded. */
  get size(): number {
    return this.latest.size;
  }

  /**
   * Snapshot of every folded run, each as a `run → phase → agent → judge` tree
   * with derived aggregates. Runs are ordered by root `seq` (emission order);
   * within a run, phases by `index`, agents and judges by `seq`.
   */
  runs(): FoldedRun[] {
    const nodes = [...this.latest.values()];

    const runNodes = nodes.filter(isWorkflowRun).sort((a, b) => a.seq - b.seq);
    const phaseNodes = nodes.filter((n): n is WorkflowPhase => n.kind === "phase");
    const agentNodes = nodes.filter(isSubAgentNode);
    const judgeNodes = nodes.filter(isJudgeVerdict);

    // Judges grouped by the nodeId they target (an agent or a phase claim).
    const judgesByTarget = new Map<string, JudgeVerdict[]>();
    for (const judge of judgeNodes) {
      const list = judgesByTarget.get(judge.target) ?? [];
      list.push(judge);
      judgesByTarget.set(judge.target, list);
    }
    for (const list of judgesByTarget.values()) {
      list.sort((a, b) => a.seq - b.seq);
    }

    // Agents grouped by their parent phase nodeId.
    const agentsByPhase = new Map<string, SubAgentNode[]>();
    for (const agent of agentNodes) {
      if (agent.parentId === null) continue;
      const list = agentsByPhase.get(agent.parentId) ?? [];
      list.push(agent);
      agentsByPhase.set(agent.parentId, list);
    }
    for (const list of agentsByPhase.values()) {
      list.sort((a, b) => a.seq - b.seq);
    }

    // Phases grouped by their parent run nodeId.
    const phasesByRun = new Map<string, WorkflowPhase[]>();
    for (const phase of phaseNodes) {
      if (phase.parentId === null) continue;
      const list = phasesByRun.get(phase.parentId) ?? [];
      list.push(phase);
      phasesByRun.set(phase.parentId, list);
    }
    for (const list of phasesByRun.values()) {
      list.sort((a, b) => a.index - b.index || a.seq - b.seq);
    }

    return runNodes.map((runNode) => {
      const phases: FoldedPhase[] = (phasesByRun.get(runNode.nodeId) ?? []).map((phaseNode) => {
        const agents: FoldedAgent[] = (agentsByPhase.get(phaseNode.nodeId) ?? []).map(
          (agentNode) => ({
            node: agentNode,
            judges: judgesByTarget.get(agentNode.nodeId) ?? [],
          })
        );
        return { node: phaseNode, agents };
      });
      return { node: runNode, phases, aggregate: aggregateRun(phases) };
    });
  }
}

/** The latest (highest-seq) judge verdict targeting an agent, or `null`. */
export function latestJudge(agent: FoldedAgent): JudgeVerdict | null {
  if (agent.judges.length === 0) return null;
  return agent.judges.reduce((acc, j) => (j.seq >= acc.seq ? j : acc));
}

/** Sum two usage records (used to roll per-agent cost up to the run). */
function addUsage(a: WorkflowAgentUsage, b: WorkflowAgentUsage): WorkflowAgentUsage {
  return {
    inputTokens: a.inputTokens + b.inputTokens,
    outputTokens: a.outputTokens + b.outputTokens,
    cacheReadTokens: a.cacheReadTokens + b.cacheReadTokens,
    cacheCreationTokens: a.cacheCreationTokens + b.cacheCreationTokens,
    costUsd: a.costUsd + b.costUsd,
    estimated: a.estimated || b.estimated,
  };
}

/** Derive the run-level fan-out / lanes / cost aggregate from its phases. */
export function aggregateRun(phases: FoldedPhase[]): RunAggregate {
  let totalAgents = 0;
  let runningAgents = 0;
  let succeededAgents = 0;
  let failedAgents = 0;
  let rejectedByJudge = 0;
  let usage = ZERO_USAGE;

  for (const phase of phases) {
    for (const agent of phase.agents) {
      totalAgents += 1;
      if (agent.node.status === "running" || agent.node.status === "pending") runningAgents += 1;
      else if (agent.node.status === "succeeded") succeededAgents += 1;
      else if (agent.node.status === "failed") failedAgents += 1;

      const judge = latestJudge(agent);
      if (judge?.verdict === "fail") rejectedByJudge += 1;

      usage = addUsage(usage, agent.node.usage);
      for (const j of agent.judges) usage = addUsage(usage, j.usage);
    }
  }

  return {
    totalAgents,
    runningAgents,
    succeededAgents,
    failedAgents,
    rejectedByJudge,
    totalCostUsd: usage.costUsd,
    estimatedCost: usage.estimated,
  };
}
