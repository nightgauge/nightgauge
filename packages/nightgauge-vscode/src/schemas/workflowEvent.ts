/**
 * Zod schema for the canonical `schemaVersion-4` {@link WorkflowEvent} node tree.
 *
 * This is the SINGLE parse boundary for the live workflow event stream that the
 * extension consumes off the SDK EventBus (re-served over SSE by the platform).
 * It replaces the hand-rolled `PipelineEvent` discriminated union + string
 * matching that the old `EventStreamService` mirror carried (reversing #3714):
 * one `WorkflowEventSchema.safeParse(payload)` validates every emission against
 * the contract, and the extension forwards the parsed node verbatim — `nodeId` /
 * `parentId` / `seq` / `ts` intact — to the sidebar tree.
 *
 * The runtime shape MUST stay in lock-step with the SDK contract in
 * `@nightgauge/sdk` (`WorkflowEvent` / `WorkflowNode`). The
 * compile-time `satisfies` assertions fail the build if the schema and the
 * imported types drift apart — there is exactly one source of truth.
 *
 * @see @nightgauge/sdk — WorkflowEvent canonical contract
 * @see Issue #3919 — live workflow sidebar tree + EventStreamService mirror removal
 * @see Issue #3714 — the local mirror this reverses
 */

import { z } from "zod";
import {
  WORKFLOW_SCHEMA_VERSION,
  type WorkflowEvent,
  type WorkflowRun,
  type WorkflowPhase,
  type SubAgentNode,
  type JudgeVerdict,
  type WorkflowAgentUsage,
} from "@nightgauge/sdk";

const NodeStatusSchema = z.enum([
  "pending",
  "running",
  "succeeded",
  "failed",
  "skipped",
  "cancelled",
]);

const TerminalKindSchema = z.enum([
  "success",
  "error",
  "timeout",
  "killed",
  "budget-exceeded",
  "cancelled",
]);

const JudgeVerdictValueSchema = z.enum(["pass", "fail", "uncertain"]);

const BackendSchema = z.enum(["native-workflow", "sdk-fanout"]);

/** Per-agent / per-judge resource usage — every field REQUIRED (#3914). */
const AgentUsageSchema = z.object({
  inputTokens: z.number(),
  outputTokens: z.number(),
  cacheReadTokens: z.number(),
  cacheCreationTokens: z.number(),
  costUsd: z.number(),
  estimated: z.boolean(),
}) satisfies z.ZodType<WorkflowAgentUsage>;

/** Fields shared by every node emission. */
const NodeBaseShape = {
  schemaVersion: z.literal(WORKFLOW_SCHEMA_VERSION),
  nodeId: z.string().min(1),
  seq: z.number(),
  ts: z.string(),
  status: NodeStatusSchema,
  label: z.string().optional(),
};

const RunNodeSchema = z.object({
  ...NodeBaseShape,
  kind: z.literal("run"),
  parentId: z.null(),
  runId: z.string(),
  issueNumber: z.number().optional(),
  stage: z.string().optional(),
  backend: BackendSchema,
  startedAt: z.string(),
  finishedAt: z.string().optional(),
}) satisfies z.ZodType<WorkflowRun>;

const PhaseNodeSchema = z.object({
  ...NodeBaseShape,
  kind: z.literal("phase"),
  parentId: z.string(),
  name: z.string(),
  index: z.number(),
  total: z.number(),
}) satisfies z.ZodType<WorkflowPhase>;

const AgentNodeSchema = z.object({
  ...NodeBaseShape,
  kind: z.literal("agent"),
  parentId: z.string(),
  agentId: z.string(),
  role: z.string().optional(),
  provider: z.string(),
  model: z.string().optional(),
  usage: AgentUsageSchema,
  terminalKind: TerminalKindSchema.optional(),
  outputRef: z.string().optional(),
}) satisfies z.ZodType<SubAgentNode>;

const JudgeNodeSchema = z.object({
  ...NodeBaseShape,
  kind: z.literal("judge"),
  parentId: z.string(),
  judgeId: z.string(),
  provider: z.string(),
  target: z.string(),
  verdict: JudgeVerdictValueSchema,
  confidence: z.number().optional(),
  rationale: z.string().optional(),
  usage: AgentUsageSchema,
}) satisfies z.ZodType<JudgeVerdict>;

/**
 * The single Zod parse boundary for the live workflow event stream. A
 * `safeParse` failure means the payload is not a v4 node emission (audit event,
 * keepalive, schema drift) and is dropped — no string matching, no local mirror.
 */
export const WorkflowEventSchema = z.discriminatedUnion("kind", [
  RunNodeSchema,
  PhaseNodeSchema,
  AgentNodeSchema,
  JudgeNodeSchema,
]) satisfies z.ZodType<WorkflowEvent>;

/**
 * Parse one SSE payload as a {@link WorkflowEvent}, returning `null` when it is
 * not a valid v4 node emission. The returned node is the SDK contract type
 * verbatim — the caller forwards it with its `seq` untouched.
 */
export function parseWorkflowEvent(payload: unknown): WorkflowEvent | null {
  const result = WorkflowEventSchema.safeParse(payload);
  return result.success ? (result.data as WorkflowEvent) : null;
}
