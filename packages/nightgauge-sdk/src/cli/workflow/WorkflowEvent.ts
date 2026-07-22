/**
 * WorkflowEvent — the canonical `schemaVersion-4` workflow event-tree contract.
 *
 * This is the single source of truth for the provider-neutral multi-agent
 * orchestration capability (epic #3899). Every execution backend — the portable
 * `SdkFanoutRunner` floor and the native Claude Dynamic Workflows offload alike
 * — emits this exact shape, and every UI surface (VSCode sidebar, dashboard
 * canvas, Flutter) renders it identically.
 *
 * Tree model: an append-only stream of node emissions. Each emission carries a
 * node's current state plus `nodeId` / `parentId` and a monotonic `seq`, so a
 * consumer folds the stream into the live tree by (nodeId, latest seq). The
 * four node kinds form the tree:
 *
 *   WorkflowRun (root)
 *     └─ WorkflowPhase
 *          ├─ SubAgentNode      (a fanned-out agent)
 *          └─ JudgeVerdict      (an adversarial judge's verdict on a claim)
 *
 * The canonical contract is intended to live in `@nightgauge/shared-types`
 * (platform #1024); it is defined SDK-side first (forward-only, no backcompat)
 * so the SDK Wave-1 work is not blocked on the platform publish. The platform
 * package re-exports this shape later.
 *
 * @see docs/WORKFLOW_ORCHESTRATION.md
 * @see Issue #3904 — SDK-side canonical contract
 */

/** Schema version for the workflow event tree. Bumped V3→V4 by epic #3899. */
export const WORKFLOW_SCHEMA_VERSION = 4 as const;
export type WorkflowSchemaVersion = typeof WORKFLOW_SCHEMA_VERSION;

/**
 * How an adapter participates in orchestration. Replaces the verified-dead
 * 4-boolean `AdapterCapabilities` (reworked onto `ICliAdapter` in #3902).
 *
 * - `native-workflow` — the adapter exposes a `runWorkflow?()` offload backend
 *   (e.g. Claude Dynamic Workflows, version-gated ≥ v2.1.154).
 * - `sdk-fanout` — the adapter is driven by the engine through the portable
 *   `SdkFanoutRunner` floor (Codex / Gemini / Copilot / LM Studio / Ollama).
 */
export type OrchestrationCapability = "native-workflow" | "sdk-fanout";

/** Discriminant for the four node kinds in the tree. */
export type WorkflowNodeKind = "run" | "phase" | "agent" | "judge";

/** Lifecycle state shared by every node. */
export type WorkflowNodeStatus =
  "pending" | "running" | "succeeded" | "failed" | "skipped" | "cancelled";

/** Why a leaf node reached its terminal state. */
export type WorkflowTerminalKind =
  "success" | "error" | "timeout" | "killed" | "budget-exceeded" | "cancelled";

/** Verdict of an adversarial judge on a "done" claim. */
export type WorkflowJudgeVerdict = "pass" | "fail" | "uncertain";

/**
 * Per-agent resource usage. `tokens` fields and `costUsd` are REQUIRED and
 * populated at emit time — the acmeapp "zeros + category:unknown" gap (#3914)
 * came from these being optional and left unset. `estimated` is `true` when the
 * provider cannot report real costs (non-Claude fan-out participants).
 */
export interface WorkflowAgentUsage {
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  costUsd: number;
  estimated: boolean;
}

/** Fields common to every node emission. */
interface WorkflowNodeBase {
  /** Always the current schema version; lets consumers reject drift. */
  schemaVersion: WorkflowSchemaVersion;
  kind: WorkflowNodeKind;
  /** Stable id for this node, unique within the run. */
  nodeId: string;
  /** Parent node id; `null` only for the root `WorkflowRun`. */
  parentId: string | null;
  /** Monotonic sequence number within the run (ordering + last-write-wins). */
  seq: number;
  /** ISO 8601 timestamp of this emission. */
  ts: string;
  status: WorkflowNodeStatus;
  /** Optional human-readable label for UIs. */
  label?: string;
}

/** Root node for one orchestrated run. */
export interface WorkflowRun extends WorkflowNodeBase {
  kind: "run";
  parentId: null;
  runId: string;
  issueNumber?: number;
  /** Owning pipeline stage, when the run is nested under one. */
  stage?: string;
  /** Which backend is executing this run. */
  backend: OrchestrationCapability;
  startedAt: string;
  finishedAt?: string;
}

/** A phase within a run (e.g. "find", "verify"). */
export interface WorkflowPhase extends WorkflowNodeBase {
  kind: "phase";
  name: string;
  index: number;
  total: number;
}

/** One fanned-out agent. */
export interface SubAgentNode extends WorkflowNodeBase {
  kind: "agent";
  agentId: string;
  /** Optional role within the phase (e.g. "finder", "refuter"). */
  role?: string;
  /** Provider that ran the agent (claude | codex | gemini | ...). */
  provider: string;
  model?: string;
  /** REQUIRED usage — never left unset (see WorkflowAgentUsage). */
  usage: WorkflowAgentUsage;
  terminalKind?: WorkflowTerminalKind;
  /** Sandboxed handle for replaying this agent's output (durable resume). */
  outputRef?: string;
}

/** An adversarial judge's verdict on another node's claim. */
export interface JudgeVerdict extends WorkflowNodeBase {
  kind: "judge";
  judgeId: string;
  provider: string;
  /** nodeId of the claim being judged. */
  target: string;
  verdict: WorkflowJudgeVerdict;
  /** Confidence in [0, 1], when the judge reports one. */
  confidence?: number;
  rationale?: string;
  /** REQUIRED usage — judges consume budget like any agent. */
  usage: WorkflowAgentUsage;
}

/** The discriminated union of all node kinds. */
export type WorkflowNode = WorkflowRun | WorkflowPhase | SubAgentNode | JudgeVerdict;

/**
 * A `WorkflowEvent` is one node emission on the append-only stream. It is the
 * node's current state; consumers fold by (nodeId, max seq) into the tree.
 */
export type WorkflowEvent = WorkflowNode;

/** Type guard narrowing a node to the root run. */
export function isWorkflowRun(node: WorkflowNode): node is WorkflowRun {
  return node.kind === "run";
}

/** Type guard narrowing a node to a sub-agent. */
export function isSubAgentNode(node: WorkflowNode): node is SubAgentNode {
  return node.kind === "agent";
}

/** Type guard narrowing a node to a judge verdict. */
export function isJudgeVerdict(node: WorkflowNode): node is JudgeVerdict {
  return node.kind === "judge";
}

/** A zeroed usage record. Use when a real measurement is genuinely unavailable. */
export function zeroUsage(estimated = false): WorkflowAgentUsage {
  return {
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheCreationTokens: 0,
    costUsd: 0,
    estimated,
  };
}
