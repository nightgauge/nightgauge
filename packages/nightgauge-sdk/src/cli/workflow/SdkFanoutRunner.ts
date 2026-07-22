/**
 * SdkFanoutRunner — the portable, universal-floor reference runner for the
 * canonical `WorkflowEvent` contract (epic #3899, #3905).
 *
 * This is the path the native Claude backend must MATCH, not the reverse
 * (portable-first policy). It consumes a provider-neutral `WorkflowSpec`, emits
 * the `schemaVersion-4` node tree through a `WorkflowEventSink`, and drives
 * provider execution through an INJECTED executor so the runner is fully
 * unit-testable without spawning real CLIs.
 *
 * Tree shape emitted, in order, per the contract:
 *
 *   WorkflowRun (root, backend "sdk-fanout")
 *     └─ WorkflowPhase                          (one per spec phase)
 *          ├─ SubAgentNode  running → terminal  (one per fanned-out agent)
 *          └─ JudgeVerdict                       (one per adversarial judge)
 *
 * Safety: the runner enforces the spec's HARD process/concurrency ceiling as a
 * safety control, NOT merely a budget knob — never more than
 * `spec.ceiling.maxConcurrent` agents running at once and never more than
 * `spec.ceiling.maxTotal` agents spawned over the whole run. The caller-supplied
 * `ceiling` is itself clamped to the un-overridable `ABSOLUTE_CEILING` by
 * `validateWorkflowSpec`, so a misconfigured or adversarial spec can never raise
 * the cap (security review #3916). A tiny internal concurrency limiter does the
 * in-flight cap with no npm dependency. The spec is validated up front and the
 * runner THROWS on any problem rather than silently truncating a fan-out.
 *
 * @see docs/WORKFLOW_ORCHESTRATION.md
 * @see Issue #3905
 */

import {
  WORKFLOW_SCHEMA_VERSION,
  zeroUsage,
  type WorkflowAgentUsage,
  type WorkflowJudgeVerdict,
  type WorkflowNodeStatus,
  type WorkflowTerminalKind,
} from "./WorkflowEvent.js";
import {
  validateWorkflowSpec,
  type WorkflowAgentSpec,
  type WorkflowJudgeSpec,
  type WorkflowSpec,
} from "./WorkflowSpec.js";
import { createSeqCounter, type WorkflowEventSink } from "./WorkflowEventSink.js";
import {
  gateWorkflowFanout,
  DEFAULT_LARGE_FANOUT_THRESHOLD,
  type QuotaGateDecision,
  type QuotaStateProvider,
} from "./WorkflowQuotaGate.js";

/** Result an injected executor returns after running one fanned-out agent. */
export interface AgentExecutionResult {
  /** REQUIRED per-agent usage; carried straight onto the `SubAgentNode`. */
  usage: WorkflowAgentUsage;
  /** Why the agent reached its terminal state. */
  terminalKind: WorkflowTerminalKind;
  /** Optional sandboxed handle for replaying this agent's output. */
  outputRef?: string;
  /** Optional model the executor actually used (echoed onto the node). */
  model?: string;
}

/**
 * Error an injected executor may throw to fail an agent/judge while still
 * reporting the tokens it already burned and the PRECISE terminal kind.
 *
 * A bare `throw` is treated as a generic `error` with estimated-zero usage, but
 * an agent that timed out, was killed, hit a budget cap, or was cancelled after
 * consuming real tokens should throw this so the terminal node carries honest
 * usage and a classified {@link WorkflowTerminalKind} rather than zeros +
 * `error` (the #3914 "zeros + category:unknown" gap, fan-out side).
 */
export class AgentExecutionError extends Error {
  constructor(
    public readonly terminalKind: WorkflowTerminalKind,
    /** Real usage burned before the failure; folded onto the terminal node. */
    public readonly usage: WorkflowAgentUsage,
    message?: string
  ) {
    super(message ?? `agent failed with terminal kind '${terminalKind}'`);
    this.name = "AgentExecutionError";
  }
}

/**
 * Recover an agent terminal outcome from a thrown error. An
 * {@link AgentExecutionError} carries the executor's real usage + terminal kind
 * through unchanged; any other throw is a generic failure with estimated-zero
 * usage (`estimated: true` because no real measurement was reported).
 */
function agentOutcomeFromError(err: unknown): {
  terminalKind: WorkflowTerminalKind;
  usage: WorkflowAgentUsage;
} {
  if (err instanceof AgentExecutionError) {
    return { terminalKind: err.terminalKind, usage: err.usage };
  }
  return { terminalKind: "error", usage: zeroUsage(true) };
}

/** Result an injected executor returns after running one adversarial judge. */
export interface JudgeExecutionResult {
  verdict: WorkflowJudgeVerdict;
  /** Confidence in [0, 1], when the judge reports one. */
  confidence?: number;
  rationale?: string;
  /** REQUIRED usage — judges consume budget like any agent. */
  usage: WorkflowAgentUsage;
}

/**
 * Injected provider execution. Keeping execution pluggable behind this seam is
 * what makes provider participation swappable (Codex / Gemini / Copilot / …)
 * and the runner unit-testable with a fake executor.
 */
export interface WorkflowExecutorBindings {
  /** Run one fanned-out agent and return its usage + terminal kind. */
  runAgent(agent: WorkflowAgentSpec): Promise<AgentExecutionResult>;
  /** Run one adversarial judge against a target node's "done" claim. */
  runJudge(judge: WorkflowJudgeSpec, targetNodeId: string): Promise<JudgeExecutionResult>;
}

/** Per-phase counts in the run summary. */
export interface WorkflowPhaseSummary {
  name: string;
  agentsSucceeded: number;
  agentsFailed: number;
  judgesPass: number;
  judgesFail: number;
  judgesUncertain: number;
}

/** Aggregated outcome of a completed (or failed) fan-out run. */
export interface WorkflowRunSummary {
  runId: string;
  status: WorkflowNodeStatus;
  /** Total agents fanned out across all phases. */
  agentCount: number;
  /** Total judges run across all phases. */
  judgeCount: number;
  agentsSucceeded: number;
  agentsFailed: number;
  phases: WorkflowPhaseSummary[];
  /** Aggregated usage across every agent and judge. */
  usage: WorkflowAgentUsage;
  /**
   * The quota-gate decision when a `quotaProvider` was supplied (#3909). When
   * `deferred` is true the run was a no-op: no agents spawned and `status` is
   * "skipped". Absent when no provider was supplied.
   */
  quotaGate?: QuotaGateDecision;
}

/** Optional knobs for {@link runSdkFanout}. */
export interface RunSdkFanoutOptions {
  /**
   * Injected bridge to the Go ratelimit/cooldown quota state (#3909). When
   * supplied, the runner consults it BEFORE spawning any agent and DEFERS the
   * whole fan-out (status "skipped", zero agents spawned) when the bridged
   * signal indicates a large fan-out would dispatch into an exhausted quota.
   * Omit to run unconditionally (the hard concurrency ceiling still applies).
   */
  quotaProvider?: QuotaStateProvider;
  /**
   * Planned-agent count at/above which a fan-out is "large" and gated against
   * quota. Defaults to {@link DEFAULT_LARGE_FANOUT_THRESHOLD}.
   */
  largeFanoutThreshold?: number;
}

/**
 * A minimal concurrency limiter — runs at most `max` tasks at once. Written
 * inline so the runner takes NO npm dependency (deliberate: the hard ceiling is
 * a safety control and must not hinge on a third-party package). Tasks queue in
 * FIFO order and start as in-flight slots free up.
 */
function createLimiter(max: number): <T>(task: () => Promise<T>) => Promise<T> {
  if (max < 1) {
    throw new Error(`concurrency limit must be >= 1 (got ${max})`);
  }
  let active = 0;
  const queue: Array<() => void> = [];

  const release = (): void => {
    active--;
    const next = queue.shift();
    if (next) next();
  };

  return <T>(task: () => Promise<T>): Promise<T> => {
    const run = (): Promise<T> => {
      active++;
      // Release the slot on settle, regardless of outcome, so a rejecting task
      // never wedges the pool.
      return task().finally(release);
    };
    if (active < max) {
      return run();
    }
    return new Promise<T>((resolve, reject) => {
      queue.push(() => run().then(resolve, reject));
    });
  };
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
 * Run a `WorkflowSpec` against an injected executor, emitting the canonical
 * `schemaVersion-4` node tree through `sink` and returning a run summary.
 *
 * Phases run sequentially (the contract's `index`/`total` ordering); within a
 * phase the agents fan out CONCURRENTLY under the ceiling, then — after the
 * fan-out barrier — the judges run (also under the ceiling).
 *
 * When `options.quotaProvider` is supplied, the bridged Go ratelimit/cooldown
 * state is consulted BEFORE any agent spawns (#3909): a large fan-out into an
 * exhausted quota is deferred (no agents spawned, run terminates "skipped")
 * rather than dispatched, distinguishing genuine exhaustion from a transient
 * status=allowed stall. The hard concurrency ceiling still applies regardless.
 *
 * @throws if `validateWorkflowSpec(spec)` reports any problem (e.g. an
 * over-ceiling fan-out) — the runner never silently truncates.
 */
export async function runSdkFanout(
  spec: WorkflowSpec,
  sink: WorkflowEventSink,
  executor: WorkflowExecutorBindings,
  options: RunSdkFanoutOptions = {}
): Promise<WorkflowRunSummary> {
  const problems = validateWorkflowSpec(spec);
  if (problems.length > 0) {
    throw new Error(`invalid WorkflowSpec: ${problems.join("; ")}`);
  }

  const nextSeq = createSeqCounter();
  const now = (): string => new Date().toISOString();

  // Carries the non-deferred quota-gate decision onto the final summary when a
  // provider was supplied; stays undefined when no provider gated the run.
  let quotaGateDecision: QuotaGateDecision | undefined;

  // Quota gate (#3909): consult the Go-bridged ratelimit/cooldown state before
  // spawning anything. A deferred large fan-out emits a single run node that
  // opens and immediately terminates "skipped" so the event tree stays
  // well-formed, then returns a deferred summary with zero agents spawned.
  if (options.quotaProvider) {
    const decision = await gateWorkflowFanout(
      spec,
      options.quotaProvider,
      options.largeFanoutThreshold ?? DEFAULT_LARGE_FANOUT_THRESHOLD
    );
    if (decision.deferred) {
      const deferredRunNodeId = `run:${spec.runId}`;
      const deferTs = now();
      sink.emit({
        schemaVersion: WORKFLOW_SCHEMA_VERSION,
        kind: "run",
        nodeId: deferredRunNodeId,
        parentId: null,
        seq: nextSeq(),
        ts: deferTs,
        status: "skipped",
        runId: spec.runId,
        issueNumber: spec.issueNumber,
        stage: spec.stage,
        backend: "sdk-fanout",
        startedAt: deferTs,
        finishedAt: deferTs,
      });
      if (sink.flush) {
        await sink.flush();
      }
      return {
        runId: spec.runId,
        status: "skipped",
        agentCount: 0,
        judgeCount: 0,
        agentsSucceeded: 0,
        agentsFailed: 0,
        phases: [],
        usage: zeroUsage(),
        quotaGate: decision,
      };
    }
    // Proceed — record the (non-deferred) decision on the summary below.
    quotaGateDecision = decision;
  }

  // Hard ceiling: concurrency limiter caps simultaneous in-flight executions,
  // and a spawn budget caps the lifetime total. Both are independent of any USD
  // budget — a misconfigured budget can never spawn unbounded processes.
  const limit = createLimiter(spec.ceiling.maxConcurrent);
  let spawned = 0;
  const reserveSpawn = (): void => {
    if (spawned >= spec.ceiling.maxTotal) {
      // validateWorkflowSpec already guarantees planned <= maxTotal, so this is
      // a defense-in-depth guard, not an expected path.
      throw new Error(
        `hard ceiling hit: would exceed maxTotal ${spec.ceiling.maxTotal} spawned agents`
      );
    }
    spawned++;
  };

  const runId = spec.runId;
  const runNodeId = `run:${runId}`;
  const startedAt = now();

  // Root WorkflowRun (running).
  sink.emit({
    schemaVersion: WORKFLOW_SCHEMA_VERSION,
    kind: "run",
    nodeId: runNodeId,
    parentId: null,
    seq: nextSeq(),
    ts: now(),
    status: "running",
    runId,
    issueNumber: spec.issueNumber,
    stage: spec.stage,
    backend: "sdk-fanout",
    startedAt,
  });

  let totalUsage = zeroUsage();
  let agentCount = 0;
  let judgeCount = 0;
  let agentsSucceeded = 0;
  let agentsFailed = 0;
  const phaseSummaries: WorkflowPhaseSummary[] = [];

  const totalPhases = spec.phases.length;

  for (let pIndex = 0; pIndex < totalPhases; pIndex++) {
    const phase = spec.phases[pIndex];
    const phaseNodeId = `phase:${runId}:${pIndex}`;

    sink.emit({
      schemaVersion: WORKFLOW_SCHEMA_VERSION,
      kind: "phase",
      nodeId: phaseNodeId,
      parentId: runNodeId,
      seq: nextSeq(),
      ts: now(),
      status: "running",
      name: phase.name,
      index: pIndex,
      total: totalPhases,
      label: phase.name,
    });

    const phaseSummary: WorkflowPhaseSummary = {
      name: phase.name,
      agentsSucceeded: 0,
      agentsFailed: 0,
      judgesPass: 0,
      judgesFail: 0,
      judgesUncertain: 0,
    };

    // Map each agent to its emitted nodeId so judges can target it.
    const agentNodeIds = new Map<string, string>();

    // Fan out the phase's agents CONCURRENTLY under the ceiling. We launch every
    // agent through the limiter up front (Promise.all over the semaphore) — the
    // limiter, not a sequential await chain, governs how many run at once.
    await Promise.all(
      phase.agents.map((agent, aIndex) => {
        const agentNodeId = `agent:${runId}:${pIndex}:${aIndex}`;
        agentNodeIds.set(agent.agentId, agentNodeId);
        const provider = agent.provider ?? "unknown";

        return limit(async () => {
          reserveSpawn();
          agentCount++;

          // running emission
          sink.emit({
            schemaVersion: WORKFLOW_SCHEMA_VERSION,
            kind: "agent",
            nodeId: agentNodeId,
            parentId: phaseNodeId,
            seq: nextSeq(),
            ts: now(),
            status: "running",
            agentId: agent.agentId,
            role: agent.role,
            provider,
            model: agent.model,
            usage: zeroUsage(),
            label: agent.agentId,
          });

          let result: AgentExecutionResult;
          let status: WorkflowNodeStatus;
          let terminalKind: WorkflowTerminalKind;
          let usage: WorkflowAgentUsage;
          try {
            result = await executor.runAgent(agent);
            terminalKind = result.terminalKind;
            usage = result.usage;
            status = terminalKind === "success" ? "succeeded" : "failed";
          } catch (err) {
            // An executor that throws is an agent error, not a runner crash —
            // record it as a failed terminal node and keep the fan-out alive.
            // Preserve any real usage + precise terminal kind the executor
            // reported via AgentExecutionError so a failed agent that already
            // burned tokens never emits zeros + a generic "error" (#3914).
            const outcome = agentOutcomeFromError(err);
            terminalKind = outcome.terminalKind;
            usage = outcome.usage;
            status = "failed";
            result = { usage, terminalKind, model: agent.model };
          }

          if (status === "succeeded") {
            agentsSucceeded++;
            phaseSummary.agentsSucceeded++;
          } else {
            agentsFailed++;
            phaseSummary.agentsFailed++;
          }
          totalUsage = addUsage(totalUsage, usage);

          // terminal emission (REQUIRED usage carried from the executor)
          sink.emit({
            schemaVersion: WORKFLOW_SCHEMA_VERSION,
            kind: "agent",
            nodeId: agentNodeId,
            parentId: phaseNodeId,
            seq: nextSeq(),
            ts: now(),
            status,
            agentId: agent.agentId,
            role: agent.role,
            provider,
            model: result.model ?? agent.model,
            usage,
            terminalKind,
            outputRef: result.outputRef,
            label: agent.agentId,
          });
        });
      })
    );

    // Fan-out barrier reached: every agent in the phase has terminated. Now run
    // the adversarial judges against the phase's claims, also under the ceiling.
    const judges = phase.judges ?? [];
    await Promise.all(
      judges.map((judge, jIndex) => {
        const judgeNodeId = `judge:${runId}:${pIndex}:${jIndex}`;
        // A judge targets the first agent's node when present, else the phase.
        const firstAgentNodeId = agentNodeIds.values().next().value;
        const target = firstAgentNodeId ?? phaseNodeId;
        const provider = judge.provider ?? "unknown";

        return limit(async () => {
          reserveSpawn();
          judgeCount++;

          let result: JudgeExecutionResult;
          let status: WorkflowNodeStatus;
          try {
            result = await executor.runJudge(judge, target);
            status = "succeeded";
          } catch (err) {
            result = { verdict: "uncertain", usage: zeroUsage(true) };
            status = "failed";
            void err;
          }

          if (result.verdict === "pass") phaseSummary.judgesPass++;
          else if (result.verdict === "fail") phaseSummary.judgesFail++;
          else phaseSummary.judgesUncertain++;
          totalUsage = addUsage(totalUsage, result.usage);

          sink.emit({
            schemaVersion: WORKFLOW_SCHEMA_VERSION,
            kind: "judge",
            nodeId: judgeNodeId,
            parentId: phaseNodeId,
            seq: nextSeq(),
            ts: now(),
            status,
            judgeId: judge.judgeId,
            provider,
            target,
            verdict: result.verdict,
            confidence: result.confidence,
            rationale: result.rationale,
            usage: result.usage,
            label: judge.judgeId,
          });
        });
      })
    );

    phaseSummaries.push(phaseSummary);

    // Phase terminal: succeeded unless an agent in it failed.
    sink.emit({
      schemaVersion: WORKFLOW_SCHEMA_VERSION,
      kind: "phase",
      nodeId: phaseNodeId,
      parentId: runNodeId,
      seq: nextSeq(),
      ts: now(),
      status: phaseSummary.agentsFailed > 0 ? "failed" : "succeeded",
      name: phase.name,
      index: pIndex,
      total: totalPhases,
      label: phase.name,
    });
  }

  const runStatus: WorkflowNodeStatus = agentsFailed > 0 ? "failed" : "succeeded";
  const finishedAt = now();

  // Root WorkflowRun terminal.
  sink.emit({
    schemaVersion: WORKFLOW_SCHEMA_VERSION,
    kind: "run",
    nodeId: runNodeId,
    parentId: null,
    seq: nextSeq(),
    ts: finishedAt,
    status: runStatus,
    runId,
    issueNumber: spec.issueNumber,
    stage: spec.stage,
    backend: "sdk-fanout",
    startedAt,
    finishedAt,
  });

  if (sink.flush) {
    await sink.flush();
  }

  return {
    runId,
    status: runStatus,
    agentCount,
    judgeCount,
    agentsSucceeded,
    agentsFailed,
    phases: phaseSummaries,
    usage: totalUsage,
    quotaGate: quotaGateDecision,
  };
}
