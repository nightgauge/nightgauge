/**
 * WorkflowSpec — the provider-neutral plan the `WorkflowEngine` always produces
 * before any backend executes. The portable `SdkFanoutRunner` (#3905) and the
 * native Claude offload consume the *same* spec; the spec is what makes the
 * engine — not the adapter — the owner of orchestration.
 *
 * @see docs/WORKFLOW_ORCHESTRATION.md
 * @see Issue #3904
 */

import { WORKFLOW_SCHEMA_VERSION, type WorkflowSchemaVersion } from "./WorkflowEvent.js";

/**
 * Hard process / concurrency ceiling for a fan-out. This is a **safety**
 * control, not merely a budget knob: the runner must never exceed it. Defaults
 * are provider-specific (see `CLAUDE_CEILING` / `FANOUT_CEILING`).
 */
export interface WorkflowConcurrencyCeiling {
  /** Max agents running at once. */
  maxConcurrent: number;
  /** Max agents spawned over the whole run. */
  maxTotal: number;
}

/** Claude native ceiling (16 concurrent / 1000 total). */
export const CLAUDE_CEILING: WorkflowConcurrencyCeiling = { maxConcurrent: 16, maxTotal: 1000 };

/** Portable fan-out ceiling for Codex / other providers (6 concurrent / 32 total). */
export const FANOUT_CEILING: WorkflowConcurrencyCeiling = { maxConcurrent: 6, maxTotal: 32 };

/**
 * Absolute, un-overridable upper bound on any spec's `ceiling`. The per-spec
 * `ceiling` is caller-supplied, so without this an over-large (misconfigured or
 * adversarial) ceiling — e.g. `maxTotal: 1_000_000` — would pass
 * `validateWorkflowSpec` as long as the planned count fit under it, defeating
 * the "hard process/concurrency ceiling" safety control. This cap is the real
 * hard limit: no caller-supplied ceiling may exceed it, and it equals the
 * largest documented provider ceiling (`CLAUDE_CEILING`: 16 concurrent / 1000
 * total). Raising it is a deliberate, reviewed code change — not a runtime knob.
 *
 * Security review #3916 — see docs/security/WORKFLOW_FANOUT_SECURITY.md (F1).
 */
export const ABSOLUTE_CEILING: WorkflowConcurrencyCeiling = {
  maxConcurrent: CLAUDE_CEILING.maxConcurrent,
  maxTotal: CLAUDE_CEILING.maxTotal,
};

/** One agent to fan out within a phase. */
export interface WorkflowAgentSpec {
  agentId: string;
  /** Optional role within the phase (e.g. "finder", "refuter"). */
  role?: string;
  prompt: string;
  /** Optional provider pin; when omitted the engine routes the agent. */
  provider?: string;
  model?: string;
}

/** One adversarial judge to run against a phase's claims. */
export interface WorkflowJudgeSpec {
  judgeId: string;
  prompt: string;
  provider?: string;
  /** Number of passing judges required to accept the claim (default 1). */
  quorum?: number;
}

/** One phase of the run: a fan-out of agents, optionally judged. */
export interface WorkflowPhaseSpec {
  name: string;
  agents: WorkflowAgentSpec[];
  judges?: WorkflowJudgeSpec[];
}

/** The full plan for one orchestrated run. */
export interface WorkflowSpec {
  schemaVersion: WorkflowSchemaVersion;
  runId: string;
  issueNumber?: number;
  /** Owning pipeline stage, when nested under one. */
  stage?: string;
  phases: WorkflowPhaseSpec[];
  /** Safety ceiling enforced by the runner. */
  ceiling: WorkflowConcurrencyCeiling;
  /**
   * Prefer an adapter's native `runWorkflow?()` offload over the portable floor
   * when the resolved adapter declares `native-workflow`. Maps to the
   * `prefer_native_offload` config knob (#3901).
   */
  preferNativeOffload?: boolean;
  /** Optional total USD budget for the run. */
  budgetUsd?: number;
}

/**
 * Total planned agent count across all phases — used to pre-validate a spec
 * against its `ceiling.maxTotal` before execution.
 */
export function plannedAgentCount(spec: WorkflowSpec): number {
  return spec.phases.reduce((sum, p) => sum + p.agents.length + (p.judges?.length ?? 0), 0);
}

/**
 * Validate a spec against its own ceiling and schema version. Returns the list
 * of problems (empty = valid). The runner rejects a spec with any problem
 * rather than silently truncating a fan-out.
 */
export function validateWorkflowSpec(spec: WorkflowSpec): string[] {
  const problems: string[] = [];
  if (spec.schemaVersion !== WORKFLOW_SCHEMA_VERSION) {
    problems.push(`schemaVersion ${spec.schemaVersion} != expected ${WORKFLOW_SCHEMA_VERSION}`);
  }
  if (spec.phases.length === 0) {
    problems.push("spec has no phases");
  }
  if (
    !Number.isInteger(spec.ceiling.maxConcurrent) ||
    !Number.isInteger(spec.ceiling.maxTotal) ||
    spec.ceiling.maxConcurrent <= 0 ||
    spec.ceiling.maxTotal <= 0
  ) {
    problems.push("ceiling.maxConcurrent and ceiling.maxTotal must be positive integers");
  }
  // The per-spec ceiling is caller-supplied; clamp it to the absolute, un-
  // overridable hard cap so a misconfigured or adversarial spec cannot raise
  // the fan-out beyond the documented maximum. (Security review #3916, F1.)
  if (spec.ceiling.maxConcurrent > ABSOLUTE_CEILING.maxConcurrent) {
    problems.push(
      `ceiling.maxConcurrent ${spec.ceiling.maxConcurrent} exceeds the absolute hard cap ${ABSOLUTE_CEILING.maxConcurrent}`
    );
  }
  if (spec.ceiling.maxTotal > ABSOLUTE_CEILING.maxTotal) {
    problems.push(
      `ceiling.maxTotal ${spec.ceiling.maxTotal} exceeds the absolute hard cap ${ABSOLUTE_CEILING.maxTotal}`
    );
  }
  const planned = plannedAgentCount(spec);
  if (planned > spec.ceiling.maxTotal) {
    problems.push(`planned ${planned} agents exceeds ceiling.maxTotal ${spec.ceiling.maxTotal}`);
  }
  return problems;
}
