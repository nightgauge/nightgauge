/**
 * parseOrchestrationFrontmatter — compile a stage SKILL.md `orchestration:`
 * frontmatter block into the provider-neutral {@link WorkflowSpec} that the
 * `WorkflowExecutor` (#3908) consumes.
 *
 * This is the bridge between the skill author surface (a declarative YAML block,
 * added in #3917) and the engine: `PipelineOrchestrator.selectExecutor` (#3913)
 * parses the block here and, when present, routes the stage through the
 * multi-agent fan-out instead of the single-agent `StageExecutor` path.
 *
 * The frontmatter is the PORTABLE description of a fan-out — every key is a
 * provider-neutral name, never a Claude / Codex tool. The resolved
 * {@link WorkflowSpec} is what makes the engine (not the adapter) own
 * orchestration.
 *
 * @see docs/WORKFLOW_ORCHESTRATION.md § Selection & routing
 * @see Issue #3913
 */

import * as yaml from "js-yaml";
import { WORKFLOW_SCHEMA_VERSION } from "./WorkflowEvent.js";
import {
  CLAUDE_CEILING,
  FANOUT_CEILING,
  type WorkflowAgentSpec,
  type WorkflowConcurrencyCeiling,
  type WorkflowJudgeSpec,
  type WorkflowPhaseSpec,
  type WorkflowSpec,
} from "./WorkflowSpec.js";

/** One fanned-out unit as written in the SKILL frontmatter. */
interface RawOrchestrationUnit {
  id?: unknown;
  role?: unknown;
  promptRef?: unknown;
  provider?: unknown;
  model?: unknown;
}

/** The optional adversarial judge block as written in the SKILL frontmatter. */
interface RawOrchestrationJudge {
  mode?: unknown;
  gate?: unknown;
  quorum?: unknown;
  promptRef?: unknown;
  provider?: unknown;
}

/** The `orchestration:` block as written in the SKILL frontmatter. */
interface RawOrchestrationBlock {
  mode?: unknown;
  phase?: unknown;
  /** A provider keyword (`fanout` | `claude`) selecting the safety ceiling. */
  ceiling?: unknown;
  units?: unknown;
  judge?: unknown;
}

/**
 * Inputs the selection point folds into the spec at parse time: the run's
 * identity (so the spec's `runId` / `issueNumber` / `stage` are well-formed) and
 * the `prefer_native_offload` flag resolved for THIS stage from config (#3901).
 */
export interface OrchestrationFrontmatterContext {
  runId: string;
  issueNumber?: number;
  stage: string;
  /** Resolved `prefer_native_offload[stage]` — folds onto the spec verbatim. */
  preferNativeOffload?: boolean;
  /**
   * Total USD budget for the run (`config.max_usd`, `0` = uncapped). Folds onto
   * `WorkflowSpec.budgetUsd` so the executor enforces it. `0`/undefined leaves
   * the spec uncapped.
   */
  budgetUsd?: number;
}

/**
 * Map a `ceiling:` keyword to the matching provider safety ceiling. An unknown
 * or missing keyword falls back to the conservative portable floor
 * (`FANOUT_CEILING`) — never the larger Claude ceiling — so a typo can only
 * narrow the fan-out, never widen it.
 */
function resolveCeiling(raw: unknown): WorkflowConcurrencyCeiling {
  if (raw === "claude") return CLAUDE_CEILING;
  return FANOUT_CEILING;
}

/** Coerce a frontmatter scalar to a trimmed non-empty string, or `undefined`. */
function asString(raw: unknown): string | undefined {
  if (typeof raw !== "string") return undefined;
  const trimmed = raw.trim();
  return trimmed.length > 0 ? trimmed : undefined;
}

/** Compile a raw unit into a {@link WorkflowAgentSpec}, or `null` when invalid. */
function toAgentSpec(raw: unknown, index: number): WorkflowAgentSpec | null {
  if (typeof raw !== "object" || raw === null) return null;
  const unit = raw as RawOrchestrationUnit;
  const agentId = asString(unit.id);
  if (!agentId) return null;
  const promptRef = asString(unit.promptRef);
  return {
    agentId,
    role: asString(unit.role),
    // The promptRef is a portable handle (an `_includes/*.md` path or an
    // in-body section anchor). The engine resolves the actual prompt text; here
    // we carry the handle through as the agent's prompt seed so the spec is
    // self-describing. A unit without a promptRef still fans out (it inherits
    // the stage prompt), so default to a stable per-unit marker.
    prompt: promptRef ?? `unit:${agentId}#${index}`,
    provider: asString(unit.provider),
    model: asString(unit.model),
  };
}

/** Compile the optional judge block into a {@link WorkflowJudgeSpec}, or `undefined`. */
function toJudgeSpec(raw: unknown): WorkflowJudgeSpec | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const judge = raw as RawOrchestrationJudge;
  const promptRef = asString(judge.promptRef);
  const mode = asString(judge.mode) ?? "merge";
  const quorum =
    typeof judge.quorum === "number" && Number.isInteger(judge.quorum) && judge.quorum > 0
      ? judge.quorum
      : 1;
  return {
    judgeId: `judge:${mode}`,
    prompt: promptRef ?? `judge:${mode}`,
    provider: asString(judge.provider),
    quorum,
  };
}

/**
 * Extract the `orchestration:` block from a SKILL.md's YAML frontmatter.
 * Returns the raw block, or `null` when the file has no frontmatter or no
 * orchestration block (the single-agent path).
 */
function extractOrchestrationBlock(skillContent: string): RawOrchestrationBlock | null {
  // Frontmatter is the leading `---\n…\n---` fence. A skill without one (or with
  // no orchestration key) has no fan-out plan.
  if (!skillContent.startsWith("---")) return null;
  const end = skillContent.indexOf("\n---", 3);
  if (end === -1) return null;
  const frontmatterYaml = skillContent.slice(3, end);

  let parsed: unknown;
  try {
    parsed = yaml.load(frontmatterYaml);
  } catch {
    // Malformed frontmatter is treated as "no orchestration block" — the stage
    // falls back to the single-agent path rather than hard-failing the run.
    return null;
  }
  if (typeof parsed !== "object" || parsed === null) return null;
  const block = (parsed as { orchestration?: unknown }).orchestration;
  if (typeof block !== "object" || block === null) return null;
  return block as RawOrchestrationBlock;
}

/**
 * Parse a stage SKILL.md's `orchestration:` frontmatter into a single-phase
 * {@link WorkflowSpec}, or return `null` when the skill declares no fan-out
 * (no frontmatter, no orchestration block, or no usable units).
 *
 * The resulting spec has exactly one phase (the stage's fan-out); the phase's
 * agents come from `units`, judged by the optional `judge` block. The provider
 * `ceiling` keyword selects the safety ceiling, and the caller-supplied context
 * folds in run identity, `prefer_native_offload`, and the budget cap.
 *
 * A skill with an orchestration block but zero usable units resolves to `null`
 * (nothing to fan out) so the stage takes the single-agent path — the prose
 * body remains the portability floor.
 */
export function parseOrchestrationFrontmatter(
  skillContent: string,
  context: OrchestrationFrontmatterContext
): WorkflowSpec | null {
  const block = extractOrchestrationBlock(skillContent);
  if (!block) return null;

  const rawUnits = Array.isArray(block.units) ? block.units : [];
  const agents: WorkflowAgentSpec[] = [];
  for (let i = 0; i < rawUnits.length; i++) {
    const agent = toAgentSpec(rawUnits[i], i);
    if (agent) agents.push(agent);
  }
  // An orchestration block that yields no agents is not a fan-out.
  if (agents.length === 0) return null;

  const judge = toJudgeSpec(block.judge);
  const phase: WorkflowPhaseSpec = {
    name: asString(block.phase) ?? context.stage,
    agents,
    judges: judge ? [judge] : undefined,
  };

  return {
    schemaVersion: WORKFLOW_SCHEMA_VERSION,
    runId: context.runId,
    issueNumber: context.issueNumber,
    stage: context.stage,
    phases: [phase],
    ceiling: resolveCeiling(block.ceiling),
    preferNativeOffload: context.preferNativeOffload ?? false,
    // `0`/undefined leaves the spec uncapped; the executor's config cap still
    // applies independently.
    budgetUsd: context.budgetUsd && context.budgetUsd > 0 ? context.budgetUsd : undefined,
  };
}
