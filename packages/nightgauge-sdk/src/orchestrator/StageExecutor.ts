/**
 * StageExecutor - Wraps Claude Agent SDK query() calls
 *
 * Provides a thin wrapper around the SDK's query() function that:
 * - Emits stage lifecycle as canonical workflow nodes via PipelineRunEmitter
 *   (phase + depth-1 agent node), folded by the EventBus tree sink
 * - Records token usage via TokenTracker
 * - Ensures context isolation (each call is fresh)
 *
 * @see docs/ARCHITECTURE.md for context isolation architecture
 * @see events/EventBus.ts — PipelineRunEmitter maps stages onto the node tree
 */

import { access, readFile } from "node:fs/promises";
import path from "node:path";
import type { PipelineStage, PipelineRunEmitter } from "../events/EventBus.js";
import type { TokenTracker, SDKResultMessage } from "../tracking/TokenTracker.js";
import type { CustomToolDefinition } from "../tools/ToolDefinition.js";
import { GeminiContextGenerator } from "../context/GeminiContextGenerator.js";
import { CodexContextGenerator } from "../context/CodexContextGenerator.js";
import { CodexMcpProvisioner } from "../context/CodexMcpProvisioner.js";
import { systemPromptPresetForAdapter } from "./providerSteering.js";
import { withBehavioralPreamble } from "./behavioralPreamble.js";

/**
 * Configuration options for executing a pipeline stage
 */
export interface StageExecutorOptions {
  stage: PipelineStage;
  issueNumber: number;
  prompt: string;
  allowedTools?: string[];
  /** Custom tool definitions for future PTC executor consumption (Issue #1066) */
  toolDefinitions?: CustomToolDefinition[];
  maxTurns?: number;
  model?: "sonnet" | "opus" | "haiku";
  cwd?: string;
  /** Timeout in milliseconds (0 = no timeout) */
  timeoutMs?: number;
  /** Adapter name for context generation (e.g., 'gemini', 'gemini-sdk') @see Issue #1055 */
  adapter?: string;
  /** Codex thread ID for `exec resume` on backtrack retry. @see Issue #1659 */
  resumeSessionId?: string;
}

/**
 * Error thrown when a stage times out
 */
export class StageTimeoutError extends Error {
  constructor(
    public readonly stage: PipelineStage,
    public readonly timeoutMs: number
  ) {
    super(`Stage '${stage}' timed out after ${timeoutMs}ms`);
    this.name = "StageTimeoutError";
  }
}

/**
 * SDK message types (subset of what the SDK provides)
 */
export interface SDKMessage {
  type: string;
  [key: string]: unknown;
}

/**
 * SDK query options interface
 */
export interface SDKQueryOptions {
  prompt: string;
  options?: {
    allowedTools?: string[];
    /** Custom tool definitions for future PTC executor consumption (Issue #1066) */
    toolDefinitions?: CustomToolDefinition[];
    maxTurns?: number;
    model?: string;
    settingSources?: string[];
    systemPrompt?: { type: string; preset?: string };
    cwd?: string;
    /** Codex thread ID for `exec resume` on backtrack retry. @see Issue #1659 */
    resumeSessionId?: string;
  };
}

/**
 * Type for the SDK query function
 */
export type SDKQueryFunction = (options: SDKQueryOptions) => AsyncGenerator<SDKMessage>;

/**
 * StageExecutor class for running individual pipeline stages
 *
 * @example
 * ```typescript
 * const executor = new StageExecutor(tokenTracker, eventBus, query);
 *
 * for await (const message of executor.execute({
 *   stage: 'issue-pickup',
 *   issueNumber: 42,
 *   prompt: 'Pick up issue #42 and extract requirements',
 * })) {
 *   console.log(message);
 * }
 * ```
 */
export class StageExecutor {
  /** Codex thread ID from the most recently completed stage. @see Issue #1659 */
  private lastSessionId: string | null = null;

  constructor(
    private tokenTracker: TokenTracker,
    private emitter: PipelineRunEmitter,
    private queryFn: SDKQueryFunction
  ) {}

  /**
   * Swap the per-run workflow emitter. The orchestrator creates a fresh
   * {@link PipelineRunEmitter} for each run / standalone stage so node ids and
   * the monotonic `seq` are scoped to that run; the executor emits through it.
   */
  setEmitter(emitter: PipelineRunEmitter): void {
    this.emitter = emitter;
  }

  /**
   * Return the Codex thread ID captured from the last stage's result message.
   *
   * Returns `null` when no Codex session ID was emitted (e.g., non-Codex adapters
   * or Codex runs that completed before `thread.started` was emitted).
   * Used by PipelineOrchestrator to pass session ID on backtrack retry.
   * @see Issue #1659
   */
  getLastSessionId(): string | null {
    return this.lastSessionId;
  }

  /**
   * Execute a pipeline stage
   *
   * Wraps the SDK query() call with:
   * 1. phase + agent `running` node emission (stage start)
   * 2. Token tracking on result, folded into the agent node's usage
   * 3. Timeout enforcement (agent terminal kind `timeout`)
   * 4. phase + agent terminal node emission (succeeded / failed)
   */
  async *execute(options: StageExecutorOptions): AsyncGenerator<SDKMessage> {
    const startTime = Date.now();
    const timeoutMs = options.timeoutMs ?? 0;
    let timeoutId: ReturnType<typeof setTimeout> | null = null;
    let isTimedOut = false;

    this.emitter.stageStarted(options.stage);

    // Setup timeout if configured. The timeout terminal is emitted in the catch
    // path (StageTimeoutError) so phase/agent terminals stay paired.
    if (timeoutMs > 0) {
      timeoutId = setTimeout(() => {
        isTimedOut = true;
      }, timeoutMs);
    }

    try {
      // Provision provider-aware system steering before execution. Each
      // generator self-guards by adapter, so this is a no-op for adapters that
      // steer another way: GEMINI.md for Gemini (#1055), AGENTS.md for Codex
      // (#4028), the claude_code preset (below) for Claude.
      if (options.adapter) {
        const steeringContext = {
          projectRoot: options.cwd ?? process.cwd(),
          stage: options.stage,
          issueNumber: options.issueNumber,
          adapter: options.adapter,
        };
        // Best-effort: a steering-generation failure must never fail the stage
        // (matches the cleanup path below and the MCP provisioner). (#4025 review #10)
        await new GeminiContextGenerator().generate(steeringContext).catch(() => {});
        await new CodexContextGenerator().generate(steeringContext).catch(() => {});
        // Make the pipeline's MCP servers reachable from Codex stages by
        // translating .mcp.json → ~/.codex/config.toml [mcp_servers.*]. Self-
        // guards by adapter; idempotent and intentionally PERSISTED (no cleanup).
        // @see Issue #4025
        await new CodexMcpProvisioner()
          .provision({ workspaceRoot: steeringContext.projectRoot, adapter: options.adapter })
          .catch(() => {});
      }

      const queryOptions: SDKQueryOptions = {
        // Behavioral preamble for the Haiku tier (#77 → #106): prepended
        // prompt-proximally when the resolved model is Haiku; measured skip
        // on Sonnet/Opus. Mirrors the Go scheduler injection.
        prompt: withBehavioralPreamble(options.prompt, options.model),
        options: {
          allowedTools: options.allowedTools,
          toolDefinitions: options.toolDefinitions,
          maxTurns: options.maxTurns,
          model: options.model,
          settingSources: ["project"],
          // Provider-aware: the claude_code preset only for Claude adapters;
          // others get undefined (steered via AGENTS.md / GEMINI.md / prompt). #4028
          systemPrompt: systemPromptPresetForAdapter(options.adapter),
          cwd: options.cwd,
          resumeSessionId: options.resumeSessionId,
        },
      };

      const query = this.queryFn(queryOptions);

      for await (const message of query) {
        // Check for timeout before yielding each message
        if (isTimedOut) {
          throw new StageTimeoutError(options.stage, timeoutMs);
        }

        yield message;

        if (message.type === "result") {
          const durationMs = Date.now() - startTime;
          this.tokenTracker.record(
            options.stage,
            message as unknown as SDKResultMessage,
            durationMs
          );

          // Capture Codex thread ID for session resume on backtrack retry.
          // cliQueryHelper sets `session_id` on result messages from Codex runs.
          // @see Issue #1659
          const sessionIdFromResult = (message as Record<string, unknown>).session_id;
          if (typeof sessionIdFromResult === "string") {
            this.lastSessionId = sessionIdFromResult;
          }

          const resultMessage = message as unknown as SDKResultMessage;
          const usage = resultMessage.usage ?? {};

          this.emitter.tokenUsage(options.stage, {
            inputTokens: usage.input_tokens ?? 0,
            outputTokens: usage.output_tokens ?? 0,
            cacheReadTokens: usage.cache_read_input_tokens ?? 0,
            cacheCreationTokens: usage.cache_creation_input_tokens ?? 0,
            costUsd: resultMessage.total_cost_usd ?? 0,
            estimated: false,
          });
        }
      }

      // Check for timeout after completion
      if (isTimedOut) {
        throw new StageTimeoutError(options.stage, timeoutMs);
      }

      // Fold the REAL recorded usage from the TokenTracker onto the terminal
      // agent node (the single source of truth). The progress-tick path above
      // already accrued it, but passing the tracker's record explicitly
      // guarantees the terminal node reflects exactly what was recorded — never
      // zeros when real usage exists (#3914).
      this.emitter.stageCompleted(options.stage, this.tokenTracker.getWorkflowUsage(options.stage));
    } catch (error) {
      // A stage can throw AFTER burning tokens (e.g. a timeout fires once the
      // result message already recorded usage, or a downstream error). Carry the
      // tracker's real usage onto the failed terminal node so a failed stage
      // never reports zeros, and classify the terminal kind from the outcome.
      this.emitter.stageFailed(
        options.stage,
        error instanceof StageTimeoutError ? "timeout" : "error",
        this.tokenTracker.getWorkflowUsage(options.stage)
      );
      throw error;
    } finally {
      // Clear timeout on completion or error
      if (timeoutId !== null) {
        clearTimeout(timeoutId);
      }

      // Cleanup provider-aware steering after stage completion. GEMINI.md is
      // fully generated so it is removed (#1055); AGENTS.md may be a committed
      // user file, so only the managed block is stripped (#4028).
      if (options.adapter) {
        const projectRoot = options.cwd ?? process.cwd();
        await new GeminiContextGenerator().cleanup(projectRoot).catch(() => {});
        await new CodexContextGenerator().cleanup(projectRoot).catch(() => {});
      }
    }
  }

  /**
   * Execute a stage and collect all messages (non-streaming)
   */
  async executeCollect(options: StageExecutorOptions): Promise<SDKMessage[]> {
    const messages: SDKMessage[] = [];
    for await (const message of this.execute(options)) {
      messages.push(message);
    }
    return messages;
  }

  /**
   * Execute a stage and return just the result message
   */
  async executeResult(options: StageExecutorOptions): Promise<SDKResultMessage | null> {
    for await (const message of this.execute(options)) {
      if (message.type === "result") {
        return message as unknown as SDKResultMessage;
      }
    }
    return null;
  }
}

/** A located + read stage SKILL.md: its raw content and the logical path. */
export interface LoadedStageSkill {
  /** Raw SKILL.md content, including the leading YAML frontmatter fence. */
  skillContent: string;
  /** The logical (repo-relative) skill path used for prompts/diagnostics. */
  logicalSkillPath: string;
}

/**
 * Locate and read a pipeline stage's SKILL.md. Shared by {@link buildStagePrompt}
 * (which turns the content into a prompt) and `PipelineOrchestrator.selectExecutor`
 * (#3913, which reads the same content's `orchestration:` frontmatter to decide
 * the executor) so a stage's skill is read once, the same way, in both places.
 */
export async function loadStageSkill(
  stage: PipelineStage,
  skillsBasePath: string = "skills"
): Promise<LoadedStageSkill> {
  // Skill directory mapping for skill-based stages only
  // Bookend stages (pipeline-start, pipeline-finish) have no skill files
  const skillDirMap: Record<PipelineStage, string> = {
    "pipeline-start": "", // Bookend stage - no skill file
    "issue-pickup": "nightgauge-issue-pickup",
    "feature-planning": "nightgauge-feature-planning",
    "feature-dev": "nightgauge-feature-dev",
    "feature-validate": "nightgauge-feature-validate",
    "pr-create": "nightgauge-pr-create",
    "pr-merge": "nightgauge-pr-merge",
    "pipeline-finish": "", // Bookend stage - no skill file
  };

  const skillDir = skillDirMap[stage];
  if (!skillDir) {
    throw new Error(`Stage '${stage}' does not map to a skill file.`);
  }

  const logicalSkillPath = `${skillsBasePath}/${skillDir}/SKILL.md`;
  const skillCandidates = [
    logicalSkillPath,
    path.join(process.cwd(), logicalSkillPath),
    path.join(process.cwd(), "..", "..", logicalSkillPath),
  ];

  let skillPath: string | null = null;
  for (const candidate of skillCandidates) {
    try {
      await access(candidate);
      skillPath = candidate;
      break;
    } catch {
      // Try next candidate path.
    }
  }

  if (!skillPath) {
    throw new Error(`Unable to locate skill file: ${logicalSkillPath}`);
  }

  const skillContent = await readFile(skillPath, "utf-8");
  return { skillContent, logicalSkillPath };
}

/**
 * Build a prompt for a pipeline stage by loading the skill file
 */
export async function buildStagePrompt(
  stage: PipelineStage,
  issueNumber: number,
  skillsBasePath: string = "skills"
): Promise<string> {
  const { skillContent, logicalSkillPath } = await loadStageSkill(stage, skillsBasePath);
  const invocationLines: string[] = [
    "Execution mode: non-interactive headless stage execution.",
    `Equivalent slash command arguments: $ARGUMENTS="${issueNumber}".`,
    "Do not invoke AskUserQuestion in this mode.",
    "If a required decision cannot be inferred deterministically, fail fast with explicit remediation commands.",
  ];

  if (stage === "issue-pickup") {
    invocationLines.push(
      `Issue #${issueNumber} was explicitly provided by the user.`,
      "Skip any auto-selection or interactive issue selection flow."
    );
  }

  // Stable-prefix-first ordering (#3805): the skill body is byte-identical
  // across issues for a given stage, so leading with it lets the auto-enabled
  // prefix cache hit. All variable content (issue number, mode) trails the
  // stable span. Skills self-derive the issue number from the branch name, so
  // leading with the body strips no required context — the trailer retains it
  // for redundancy.
  return `${skillContent}

---

Execute the above Nightgauge pipeline skill for issue #${issueNumber}.

Skill source: ${logicalSkillPath}
Issue number: ${issueNumber}

Invocation context:
${invocationLines.map((line) => `- ${line}`).join("\n")}`;
}
