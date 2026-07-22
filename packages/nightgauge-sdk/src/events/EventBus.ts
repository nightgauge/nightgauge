/**
 * EventBus — in-process {@link WorkflowEventSink} for the canonical workflow
 * event-tree contract.
 *
 * The bus carries the `schemaVersion-4` node-tree {@link WorkflowEvent} union
 * (WorkflowRun / WorkflowPhase / SubAgentNode / JudgeVerdict — each with
 * `nodeId` / `parentId`, a monotonic `seq`, and ISO-8601 `ts`) instead of a
 * flat, Date-stamped `stage:*` union. Every emission is one node's current
 * state; consumers fold the stream by (nodeId, max seq) into the live tree.
 *
 * Subscription is keyed on {@link WorkflowNodeKind} (`run` | `phase` | `agent`
 * | `judge`) rather than a stage-lifecycle string, plus an `onAny` firehose for
 * forwarders that need every emission. High-frequency `agent` progress
 * emissions (same node, still `running`) are coalesced to ~1 Hz per node so a
 * chatty backend cannot flood the UI.
 *
 * The existing six linear pipeline stages are EXPRESSED on this tree by
 * {@link PipelineRunEmitter}: a pipeline run is a {@link WorkflowRun} whose six
 * stages are first-level {@link WorkflowPhase} nodes, each driving a single
 * depth-1 {@link SubAgentNode} (single-agent stage = depth-1 chain).
 *
 * @see cli/workflow/WorkflowEvent.ts — the canonical contract
 * @see cli/workflow/WorkflowEventSink.ts — the write boundary this implements
 * @see docs/WORKFLOW_ORCHESTRATION.md
 */

import {
  WORKFLOW_SCHEMA_VERSION,
  zeroUsage,
  type WorkflowEvent,
  type WorkflowNode,
  type WorkflowNodeKind,
  type WorkflowNodeStatus,
  type WorkflowTerminalKind,
  type WorkflowAgentUsage,
  type OrchestrationCapability,
} from "../cli/workflow/WorkflowEvent.js";
import type { WorkflowEventSink } from "../cli/workflow/WorkflowEventSink.js";

/**
 * Pipeline stage identifiers.
 *
 * This is the stable stage-name enum, fully DECOUPLED from the event shape — it
 * names the six skill stages (plus deterministic bookends) and is reused across
 * the SDK and the VSCode extension for routing, status, and display. It does
 * NOT appear in the {@link WorkflowEvent} tree, which carries phase nodes whose
 * `name` happens to be the stage name.
 *
 * Includes bookend stages (pipeline-start, pipeline-finish) for reliable
 * synchronization points. These are deterministic orchestration stages that
 * execute synchronously with zero AI token consumption.
 */
export type PipelineStage =
  | "pipeline-start"
  | "issue-pickup"
  | "feature-planning"
  | "feature-dev"
  | "feature-validate"
  | "pr-create"
  | "pr-merge"
  | "pipeline-finish";

/**
 * Canonical ordered list of pipeline stages — runtime constant for code that
 * needs to enumerate all stages (e.g., terminal-state checks). Mirrors the
 * `PipelineStage` union; keep both in sync.
 */
export const PIPELINE_STAGE_ORDER: readonly PipelineStage[] = [
  "pipeline-start",
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
  "pipeline-finish",
] as const;

/**
 * Handler for a single node kind. Receives the narrowed node emission for that
 * kind (last-write-wins state, not a delta).
 */
export type WorkflowNodeHandler<K extends WorkflowNodeKind> = (
  node: Extract<WorkflowNode, { kind: K }>
) => void;

/** Handler for the `onAny` firehose — receives every emission, all kinds. */
export type WorkflowAnyHandler = (node: WorkflowNode) => void;

/** Internal storage type — handlers are stored kind-erased. */
type GenericHandler = (node: WorkflowNode) => void;

/**
 * Minimum interval between coalesced `agent` progress emissions for a single
 * node (~1 Hz). A status CHANGE (e.g. running → succeeded) always flushes
 * immediately regardless of this window — only consecutive `running`-state
 * emissions of the same agent node are throttled.
 */
const AGENT_PROGRESS_COALESCE_MS = 1000;

/**
 * EventBus — pub/sub sink for the canonical workflow event tree.
 *
 * @example
 * ```typescript
 * const bus = new EventBus();
 *
 * bus.on("phase", (node) => {
 *   console.log(`Phase ${node.name} → ${node.status}`);
 * });
 *
 * bus.emit({
 *   schemaVersion: WORKFLOW_SCHEMA_VERSION,
 *   kind: "phase",
 *   nodeId: "phase:42:0",
 *   parentId: "run:42",
 *   seq: 1,
 *   ts: new Date().toISOString(),
 *   status: "running",
 *   name: "issue-pickup",
 *   index: 0,
 *   total: 6,
 * });
 * ```
 */
export class EventBus implements WorkflowEventSink {
  private handlers: Map<WorkflowNodeKind, Set<GenericHandler>> = new Map();
  private anyHandlers: Set<WorkflowAnyHandler> = new Set();

  /** Last emit time (ms) for an `agent` node, keyed by nodeId — coalescing window. */
  private lastAgentProgressAt: Map<string, number> = new Map();

  /**
   * Subscribe to emissions of a specific node kind.
   */
  on<K extends WorkflowNodeKind>(kind: K, handler: WorkflowNodeHandler<K>): void {
    if (!this.handlers.has(kind)) {
      this.handlers.set(kind, new Set());
    }
    this.handlers.get(kind)!.add(handler as unknown as GenericHandler);
  }

  /**
   * Unsubscribe a handler from a specific node kind.
   */
  off<K extends WorkflowNodeKind>(kind: K, handler: WorkflowNodeHandler<K>): void {
    const kindHandlers = this.handlers.get(kind);
    if (kindHandlers) {
      kindHandlers.delete(handler as unknown as GenericHandler);
    }
  }

  /**
   * Subscribe to every emission, across all kinds. Used by forwarders (SSE,
   * dashboard) that mirror the whole tree.
   */
  onAny(handler: WorkflowAnyHandler): void {
    this.anyHandlers.add(handler);
  }

  /** Unsubscribe an `onAny` firehose handler. */
  offAny(handler: WorkflowAnyHandler): void {
    this.anyHandlers.delete(handler);
  }

  /**
   * Append one node emission to the stream and dispatch it.
   *
   * Consecutive `agent` emissions for the same node that remain in the
   * `running` state are coalesced to ~1 Hz; a status change always flushes.
   */
  emit(event: WorkflowEvent): void {
    if (this.shouldCoalesce(event)) {
      return;
    }

    const kindHandlers = this.handlers.get(event.kind);
    if (kindHandlers) {
      for (const handler of kindHandlers) {
        this.invoke(handler, event);
      }
    }
    for (const handler of this.anyHandlers) {
      this.invoke(handler, event);
    }
  }

  /**
   * Decide whether an emission is a throttled `agent` progress tick that should
   * be dropped. Updates the coalescing clock when the emission passes.
   */
  private shouldCoalesce(event: WorkflowEvent): boolean {
    if (event.kind !== "agent" || event.status !== "running") {
      // Non-progress emission (different kind, or a status transition): always
      // forwarded. Reset the window so the next running tick is allowed through.
      if (event.kind === "agent") {
        this.lastAgentProgressAt.delete(event.nodeId);
      }
      return false;
    }

    const now = Date.now();
    const last = this.lastAgentProgressAt.get(event.nodeId);
    if (last !== undefined && now - last < AGENT_PROGRESS_COALESCE_MS) {
      return true; // within the 1 Hz window → drop
    }
    this.lastAgentProgressAt.set(event.nodeId, now);
    return false;
  }

  /** Invoke a handler, isolating consumer errors from the hot path. */
  private invoke(handler: GenericHandler | WorkflowAnyHandler, event: WorkflowNode): void {
    try {
      handler(event);
    } catch (error) {
      console.error(`Error in workflow event handler for ${event.kind}:`, error);
    }
  }

  /**
   * Subscribe to a node kind once (auto-unsubscribe after first emission).
   */
  once<K extends WorkflowNodeKind>(kind: K, handler: WorkflowNodeHandler<K>): void {
    const wrapped: WorkflowNodeHandler<K> = (node) => {
      this.off(kind, wrapped);
      handler(node);
    };
    this.on(kind, wrapped);
  }

  /**
   * Remove all handlers for a node kind, or all handlers (including `onAny`)
   * when no kind is given.
   */
  removeAllListeners(kind?: WorkflowNodeKind): void {
    if (kind) {
      this.handlers.delete(kind);
    } else {
      this.handlers.clear();
      this.anyHandlers.clear();
    }
  }

  /**
   * Count of handlers for a node kind.
   */
  listenerCount(kind: WorkflowNodeKind): number {
    return this.handlers.get(kind)?.size ?? 0;
  }
}

/**
 * Maps the six linear pipeline stages onto the canonical workflow event tree.
 *
 * A pipeline run is a single {@link WorkflowRun}; each executed stage is a
 * first-level {@link WorkflowPhase} whose `index` is its position in
 * {@link PIPELINE_STAGE_ORDER} (bookends excluded) and whose work is a single
 * depth-1 {@link SubAgentNode}. This is the bridge that lets the orchestrator
 * express stage lifecycle as node emissions without scattering node-id / seq
 * bookkeeping across {@link StageExecutor} and {@link PipelineOrchestrator}.
 *
 * Construct one per pipeline run, emit through it, and the EventBus folds the
 * stream into the live tree exactly like any other backend.
 */
export class PipelineRunEmitter {
  private seqCounter = 0;
  private readonly runNodeId: string;
  /** Cumulative usage per stage agent node — tokens accrue across `tokenUsage`. */
  private readonly agentUsage: Map<PipelineStage, WorkflowAgentUsage> = new Map();

  constructor(
    private readonly sink: WorkflowEventSink,
    private readonly issueNumber: number,
    private readonly backend: OrchestrationCapability = "sdk-fanout"
  ) {
    this.runNodeId = `run:${issueNumber}`;
  }

  private nextSeq(): number {
    return this.seqCounter++;
  }

  private now(): string {
    return new Date().toISOString();
  }

  private phaseNodeId(stage: PipelineStage): string {
    return `phase:${this.issueNumber}:${stage}`;
  }

  private agentNodeId(stage: PipelineStage): string {
    return `agent:${this.issueNumber}:${stage}`;
  }

  private stageIndex(stage: PipelineStage): number {
    return PIPELINE_STAGE_ORDER.indexOf(stage);
  }

  /** Total executed stages (bookends carry no phase node). */
  private get totalPhases(): number {
    return PIPELINE_STAGE_ORDER.filter((s) => s !== "pipeline-start" && s !== "pipeline-finish")
      .length;
  }

  /** Emit the root {@link WorkflowRun} in the `running` state. */
  runStarted(): void {
    this.sink.emit({
      schemaVersion: WORKFLOW_SCHEMA_VERSION,
      kind: "run",
      nodeId: this.runNodeId,
      parentId: null,
      seq: this.nextSeq(),
      ts: this.now(),
      status: "running",
      runId: this.runNodeId,
      issueNumber: this.issueNumber,
      backend: this.backend,
      startedAt: this.now(),
    });
  }

  /** Emit the root {@link WorkflowRun} terminal. */
  runFinished(status: Extract<WorkflowNodeStatus, "succeeded" | "failed" | "cancelled">): void {
    this.sink.emit({
      schemaVersion: WORKFLOW_SCHEMA_VERSION,
      kind: "run",
      nodeId: this.runNodeId,
      parentId: null,
      seq: this.nextSeq(),
      ts: this.now(),
      status,
      runId: this.runNodeId,
      issueNumber: this.issueNumber,
      backend: this.backend,
      startedAt: this.now(),
      finishedAt: this.now(),
    });
  }

  /** Emit a stage's {@link WorkflowPhase} + its depth-1 {@link SubAgentNode}, both `running`. */
  stageStarted(stage: PipelineStage): void {
    this.agentUsage.set(stage, zeroUsage());
    this.sink.emit({
      schemaVersion: WORKFLOW_SCHEMA_VERSION,
      kind: "phase",
      nodeId: this.phaseNodeId(stage),
      parentId: this.runNodeId,
      seq: this.nextSeq(),
      ts: this.now(),
      status: "running",
      name: stage,
      index: this.stageIndex(stage),
      total: this.totalPhases,
      label: stage,
    });
    this.emitAgent(stage, "running");
  }

  /**
   * Coalesce token usage into the stage's agent node and re-emit it `running`.
   * The bus throttles these to ~1 Hz per node.
   */
  tokenUsage(stage: PipelineStage, usage: WorkflowAgentUsage): void {
    const prior = this.agentUsage.get(stage) ?? zeroUsage();
    this.agentUsage.set(stage, {
      inputTokens: prior.inputTokens + usage.inputTokens,
      outputTokens: prior.outputTokens + usage.outputTokens,
      cacheReadTokens: prior.cacheReadTokens + usage.cacheReadTokens,
      cacheCreationTokens: prior.cacheCreationTokens + usage.cacheCreationTokens,
      costUsd: prior.costUsd + usage.costUsd,
      estimated: prior.estimated || usage.estimated,
    });
    this.emitAgent(stage, "running");
  }

  /**
   * Emit the stage agent + phase terminal as `succeeded`.
   *
   * `finalUsage`, when supplied, is the authoritative per-agent usage from the
   * {@link TokenTracker} (the single source of truth). It overrides the usage
   * accrued from progress ticks so the terminal SubAgentNode carries the REAL
   * tokens/costUsd recorded for the stage — never zeros when real usage exists
   * (the #3914 "zeros + category:unknown" gap).
   */
  stageCompleted(stage: PipelineStage, finalUsage?: WorkflowAgentUsage): void {
    this.emitAgent(stage, "succeeded", "success", finalUsage);
    this.emitPhaseTerminal(stage, "succeeded");
  }

  /**
   * Emit the stage agent + phase terminal as `failed` with the given terminal
   * kind. `finalUsage` (authoritative tracker usage) folds the real
   * tokens/costUsd onto the terminal agent node even when the stage threw before
   * a progress tick reached the emitter — a failed stage that already burned
   * tokens must not report zeros.
   */
  stageFailed(
    stage: PipelineStage,
    terminalKind: WorkflowTerminalKind = "error",
    finalUsage?: WorkflowAgentUsage
  ): void {
    this.emitAgent(stage, "failed", terminalKind, finalUsage);
    this.emitPhaseTerminal(stage, "failed");
  }

  /** Emit a stage's phase as `skipped` (routing / approval skip). */
  stageSkipped(stage: PipelineStage): void {
    this.emitPhaseTerminal(stage, "skipped");
  }

  private emitAgent(
    stage: PipelineStage,
    status: WorkflowNodeStatus,
    terminalKind?: WorkflowTerminalKind,
    finalUsage?: WorkflowAgentUsage
  ): void {
    // The authoritative tracker usage (when supplied at terminal time) wins over
    // the usage accrued from progress ticks and becomes the node's recorded
    // usage, so a later read can never regress to a stale lower value.
    if (finalUsage) {
      this.agentUsage.set(stage, finalUsage);
    }
    this.sink.emit({
      schemaVersion: WORKFLOW_SCHEMA_VERSION,
      kind: "agent",
      nodeId: this.agentNodeId(stage),
      parentId: this.phaseNodeId(stage),
      seq: this.nextSeq(),
      ts: this.now(),
      status,
      agentId: stage,
      provider: "claude",
      usage: this.agentUsage.get(stage) ?? zeroUsage(),
      terminalKind,
      label: stage,
    });
  }

  private emitPhaseTerminal(
    stage: PipelineStage,
    status: Extract<WorkflowNodeStatus, "succeeded" | "failed" | "skipped">
  ): void {
    this.sink.emit({
      schemaVersion: WORKFLOW_SCHEMA_VERSION,
      kind: "phase",
      nodeId: this.phaseNodeId(stage),
      parentId: this.runNodeId,
      seq: this.nextSeq(),
      ts: this.now(),
      status,
      name: stage,
      index: this.stageIndex(stage),
      total: this.totalPhases,
      label: stage,
    });
  }
}
