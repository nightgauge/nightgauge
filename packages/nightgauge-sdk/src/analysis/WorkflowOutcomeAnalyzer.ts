/**
 * WorkflowOutcomeAnalyzer — the consumer-side fold from the canonical
 * `schemaVersion-4` {@link WorkflowEvent} node tree into a flat, learning-loop
 * outcome record (epic #3899, #3915).
 *
 * The producer side ({@link SdkFanoutRunner}, the native Claude offload) emits
 * an append-only stream of node emissions through a {@link WorkflowEventSink}.
 * The outcome-recording + self-improvement consumers (PostPipelineAnalyzer, the
 * health dimensions) used to read a flat per-stage event shape that the V4
 * rework deleted. This module is the single forward-only ingestion path: it
 * folds the V4 tree — nested {@link SubAgentNode} `agents[]` plus
 * {@link JudgeVerdict}s — into a {@link WorkflowOutcome} that those consumers
 * record for calibration.
 *
 * The fold is deliberately tolerant of partial streams (a crashed run, a
 * resumed journal): it folds by `(nodeId, max seq)` exactly like the live tree
 * sink, ignores orphan nodes, and never throws on a malformed emission so a
 * single bad record can't break the non-critical post-pipeline loop.
 *
 * @see docs/WORKFLOW_ORCHESTRATION.md
 * @see docs/OUTCOME_RECORDING.md
 * @see Issue #3915 — V4 outcome-recording + learning-loop consumers
 */

import {
  isWorkflowRun,
  isSubAgentNode,
  isJudgeVerdict,
  zeroUsage,
  type WorkflowEvent,
  type WorkflowNode,
  type WorkflowAgentUsage,
  type WorkflowNodeStatus,
  type OrchestrationCapability,
  type SubAgentNode,
  type JudgeVerdict,
  type WorkflowPhase,
} from "../cli/workflow/WorkflowEvent.js";

/** Per-phase fold of a workflow run's agent + judge leaves. */
export interface WorkflowPhaseOutcome {
  /** Phase node id (stable within the run). */
  nodeId: string;
  /** Human-readable phase name (e.g. "find", "verify"), when emitted. */
  name: string;
  index: number;
  agentsSucceeded: number;
  agentsFailed: number;
  judgesPass: number;
  judgesFail: number;
  judgesUncertain: number;
  /** Aggregate usage across this phase's agents and judges. */
  usage: WorkflowAgentUsage;
}

/**
 * Flat, learning-loop-ready outcome folded from one V4 workflow run.
 *
 * This is the record the outcome recorder + health dimensions ingest; it
 * surfaces the WORKFLOW-level signals epic #3899 calibrates on:
 *
 * - `usage` — aggregate of every agent + judge (matches the summed leaves)
 * - `judgeRejectionRate` — fail / total judge verdicts (the adversarial signal)
 * - `fanoutEfficiency` — succeeded agents / total agents (wasted-spawn signal)
 * - `backend` — native-workflow vs sdk-fanout (for the cost-delta comparison)
 */
export interface WorkflowOutcome {
  runId: string;
  issueNumber?: number;
  /** Owning pipeline stage, when the run was nested under one. */
  stage?: string;
  /** Which backend executed the run — drives the native-vs-fanout cost delta. */
  backend: OrchestrationCapability;
  /** Terminal run status (`running` if the stream never reached a terminal). */
  status: WorkflowNodeStatus;
  /** Total agents fanned out across every phase. */
  agentCount: number;
  /** Total judges run across every phase. */
  judgeCount: number;
  agentsSucceeded: number;
  agentsFailed: number;
  judgesPass: number;
  judgesFail: number;
  judgesUncertain: number;
  /**
   * Adversarial-judge rejection rate in [0, 1]: failing verdicts over all
   * verdicts. `null` when no judge ran (no signal rather than a fake 0).
   */
  judgeRejectionRate: number | null;
  /**
   * Fan-out efficiency in [0, 1]: succeeded agents over all agents — a low
   * value flags wasted spawns. `null` when no agent ran.
   */
  fanoutEfficiency: number | null;
  /** Aggregate usage across every agent and judge in the run. */
  usage: WorkflowAgentUsage;
  /** Per-phase breakdown, in phase `index` order. */
  phases: WorkflowPhaseOutcome[];
}

/** Sum two usage records; `estimated` is sticky-true if either side estimated. */
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

/**
 * Fold an append-only V4 event stream into the latest state per node.
 *
 * Mirrors the live tree sink: last write wins by `seq` within a node. Malformed
 * emissions (missing `nodeId`/`seq`) are skipped rather than thrown on, so a
 * single bad journal line can't break the non-critical post-pipeline loop.
 */
function foldLatestByNode(events: readonly WorkflowEvent[]): Map<string, WorkflowNode> {
  const latest = new Map<string, WorkflowNode>();
  for (const ev of events) {
    if (!ev || typeof ev.nodeId !== "string" || typeof ev.seq !== "number") continue;
    const prior = latest.get(ev.nodeId);
    if (!prior || ev.seq >= prior.seq) {
      latest.set(ev.nodeId, ev);
    }
  }
  return latest;
}

/** True when a folded agent node reached its `succeeded` terminal. */
function agentSucceeded(node: SubAgentNode): boolean {
  return node.status === "succeeded";
}

/**
 * Fold a V4 workflow event tree into a {@link WorkflowOutcome}.
 *
 * Accepts the raw append-only emission stream (e.g. an `ArrayWorkflowEventSink`
 * capture or a durable journal's lines) for a SINGLE run. When the stream
 * carries more than one run, pass each run's events separately (see
 * {@link foldWorkflowOutcomes}).
 *
 * Aggregate `usage` is the sum of every folded agent + judge leaf, so it
 * matches `sum(agents[].usage) + sum(judges[].usage)` by construction.
 *
 * Returns `null` when the stream contains no root `run` node (nothing to fold).
 */
export function foldWorkflowOutcome(events: readonly WorkflowEvent[]): WorkflowOutcome | null {
  const latest = foldLatestByNode(events);
  const nodes = [...latest.values()];

  const runNode = nodes.find(isWorkflowRun);
  if (!runNode) return null;

  // Index phases by nodeId so leaves can attribute to their parent phase.
  const phaseNodes = nodes.filter((n): n is WorkflowPhase => n.kind === "phase");
  const phaseByNodeId = new Map<string, WorkflowPhaseOutcome>();
  for (const phase of phaseNodes) {
    phaseByNodeId.set(phase.nodeId, {
      nodeId: phase.nodeId,
      name: phase.name,
      index: phase.index,
      agentsSucceeded: 0,
      agentsFailed: 0,
      judgesPass: 0,
      judgesFail: 0,
      judgesUncertain: 0,
      usage: zeroUsage(),
    });
  }

  let totalUsage = zeroUsage();
  let agentCount = 0;
  let agentsSucceeded = 0;
  let agentsFailed = 0;
  let judgeCount = 0;
  let judgesPass = 0;
  let judgesFail = 0;
  let judgesUncertain = 0;

  const attributeToPhase = (
    parentId: string | null,
    apply: (phase: WorkflowPhaseOutcome) => void
  ): void => {
    if (parentId === null) return;
    const phase = phaseByNodeId.get(parentId);
    if (phase) apply(phase);
  };

  for (const node of nodes) {
    if (isSubAgentNode(node)) {
      agentCount++;
      totalUsage = addUsage(totalUsage, node.usage);
      const succeeded = agentSucceeded(node);
      if (succeeded) agentsSucceeded++;
      else agentsFailed++;
      attributeToPhase(node.parentId, (phase) => {
        if (succeeded) phase.agentsSucceeded++;
        else phase.agentsFailed++;
        phase.usage = addUsage(phase.usage, node.usage);
      });
    } else if (isJudgeVerdict(node)) {
      judgeCount++;
      totalUsage = addUsage(totalUsage, node.usage);
      const verdict = (node as JudgeVerdict).verdict;
      if (verdict === "pass") judgesPass++;
      else if (verdict === "fail") judgesFail++;
      else judgesUncertain++;
      attributeToPhase(node.parentId, (phase) => {
        if (verdict === "pass") phase.judgesPass++;
        else if (verdict === "fail") phase.judgesFail++;
        else phase.judgesUncertain++;
        phase.usage = addUsage(phase.usage, node.usage);
      });
    }
  }

  const phases = [...phaseByNodeId.values()].sort((a, b) => a.index - b.index);

  return {
    runId: runNode.runId,
    issueNumber: runNode.issueNumber,
    stage: runNode.stage,
    backend: runNode.backend,
    status: runNode.status,
    agentCount,
    judgeCount,
    agentsSucceeded,
    agentsFailed,
    judgesPass,
    judgesFail,
    judgesUncertain,
    judgeRejectionRate: judgeCount > 0 ? judgesFail / judgeCount : null,
    fanoutEfficiency: agentCount > 0 ? agentsSucceeded / agentCount : null,
    usage: totalUsage,
    phases,
  };
}

/**
 * Fold a mixed event stream that may carry MULTIPLE runs (e.g. a shared journal
 * or a batch of pipeline runs) into one outcome per run.
 *
 * Each emission is bucketed by its run's `nodeId` via the parent chain so runs
 * never bleed into one another, then each bucket is folded independently.
 * Streams with no root run yield an empty array.
 */
export function foldWorkflowOutcomes(events: readonly WorkflowEvent[]): WorkflowOutcome[] {
  // Resolve every node's owning run by walking parentId to the root. We need the
  // full id→node map first because a child can arrive before its parent in a
  // resumed/out-of-order stream.
  const byNodeId = new Map<string, WorkflowNode>();
  for (const ev of events) {
    if (!ev || typeof ev.nodeId !== "string" || typeof ev.seq !== "number") continue;
    const prior = byNodeId.get(ev.nodeId);
    if (!prior || ev.seq >= prior.seq) byNodeId.set(ev.nodeId, ev);
  }

  const runIdForNode = (nodeId: string): string | null => {
    let current = byNodeId.get(nodeId);
    const seen = new Set<string>();
    while (current && !seen.has(current.nodeId)) {
      seen.add(current.nodeId);
      if (isWorkflowRun(current)) return current.nodeId;
      if (current.parentId === null) return null;
      current = byNodeId.get(current.parentId);
    }
    return null;
  };

  const buckets = new Map<string, WorkflowEvent[]>();
  for (const ev of events) {
    if (!ev || typeof ev.nodeId !== "string" || typeof ev.seq !== "number") continue;
    const runNodeId = isWorkflowRun(ev) ? ev.nodeId : runIdForNode(ev.nodeId);
    if (runNodeId === null) continue;
    const bucket = buckets.get(runNodeId) ?? [];
    bucket.push(ev);
    buckets.set(runNodeId, bucket);
  }

  const outcomes: WorkflowOutcome[] = [];
  for (const bucket of buckets.values()) {
    const outcome = foldWorkflowOutcome(bucket);
    if (outcome) outcomes.push(outcome);
  }
  return outcomes;
}

/**
 * Aggregate calibration signal across many folded workflow outcomes — the shape
 * the learning loop records for the WORKFLOW-level calibration epic #3899 calls
 * for: judge-rejection rate, fan-out efficiency, and the native-vs-fanout cost
 * delta. Runs with no agent/judge contribute nothing to the rates (no fake
 * zeros) but still count toward the per-backend cost totals.
 */
export interface WorkflowCalibrationSignal {
  runCount: number;
  /** Total agents / judges fanned out across all runs. */
  totalAgents: number;
  totalJudges: number;
  /** Mean judge-rejection rate across runs that ran ≥1 judge; `null` if none. */
  meanJudgeRejectionRate: number | null;
  /** Mean fan-out efficiency across runs that ran ≥1 agent; `null` if none. */
  meanFanoutEfficiency: number | null;
  /** Total cost (USD) of native-workflow runs. */
  nativeCostUsd: number;
  /** Total cost (USD) of sdk-fanout runs. */
  fanoutCostUsd: number;
  /** Runs executed per backend (for averaging the cost delta downstream). */
  nativeRunCount: number;
  fanoutRunCount: number;
  /**
   * native-vs-fanout per-run mean cost delta (nativeMean − fanoutMean), in USD.
   * Positive ⇒ native costs more per run; `null` until BOTH backends have ≥1
   * run so a one-sided sample never fabricates a delta.
   */
  nativeVsFanoutCostDeltaUsd: number | null;
}

/**
 * Reduce many {@link WorkflowOutcome}s into a single
 * {@link WorkflowCalibrationSignal} the learning loop persists for calibration.
 */
export function summarizeWorkflowOutcomes(
  outcomes: readonly WorkflowOutcome[]
): WorkflowCalibrationSignal {
  let totalAgents = 0;
  let totalJudges = 0;
  let nativeCostUsd = 0;
  let fanoutCostUsd = 0;
  let nativeRunCount = 0;
  let fanoutRunCount = 0;

  const judgeRates: number[] = [];
  const fanoutRates: number[] = [];

  for (const o of outcomes) {
    totalAgents += o.agentCount;
    totalJudges += o.judgeCount;
    if (o.judgeRejectionRate !== null) judgeRates.push(o.judgeRejectionRate);
    if (o.fanoutEfficiency !== null) fanoutRates.push(o.fanoutEfficiency);
    if (o.backend === "native-workflow") {
      nativeCostUsd += o.usage.costUsd;
      nativeRunCount++;
    } else {
      fanoutCostUsd += o.usage.costUsd;
      fanoutRunCount++;
    }
  }

  const meanOf = (xs: number[]): number | null =>
    xs.length > 0 ? xs.reduce((sum, x) => sum + x, 0) / xs.length : null;

  const nativeVsFanoutCostDeltaUsd =
    nativeRunCount > 0 && fanoutRunCount > 0
      ? nativeCostUsd / nativeRunCount - fanoutCostUsd / fanoutRunCount
      : null;

  return {
    runCount: outcomes.length,
    totalAgents,
    totalJudges,
    meanJudgeRejectionRate: meanOf(judgeRates),
    meanFanoutEfficiency: meanOf(fanoutRates),
    nativeCostUsd,
    fanoutCostUsd,
    nativeRunCount,
    fanoutRunCount,
    nativeVsFanoutCostDeltaUsd,
  };
}
