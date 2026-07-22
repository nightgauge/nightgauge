/**
 * BaseStage - Abstract base class for pipeline stages
 *
 * Provides common infrastructure for:
 * - Reading SKILL.md files as prompt templates
 * - Building structured prompts with context injection
 * - Input/output context validation via Zod schemas
 * - Integration with StageExecutor for SDK execution
 *
 * @see docs/ARCHITECTURE.md for context-isolated pipeline architecture
 */

import { z } from "zod";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import type { PipelineStage } from "../events/EventBus.js";
import { ContextManager } from "../context/ContextManager.js";
import type {
  StageExecutor,
  StageExecutorOptions,
  SDKMessage,
} from "../orchestrator/StageExecutor.js";

/**
 * Configuration for a pipeline stage
 */
export interface StageConfig<TInput, TOutput> {
  /** Stage identifier matching PipelineStage type */
  name: PipelineStage;
  /** Relative path to the SKILL.md file */
  skillPath: string;
  /** Zod schema for input context validation (optional for first stage) */
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  inputSchema?: z.ZodType<TInput, any, any>;
  /** Zod schema for output context validation */
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  outputSchema: z.ZodType<TOutput, any, any>;
  /** Whether this stage requires user approval before completing */
  requiresApproval?: boolean;
  /** Context file type for input (e.g., 'issue', 'planning') */
  inputContextType?: "issue" | "planning" | "dev" | "pr";
  /** Context file type for output */
  outputContextType: "issue" | "planning" | "dev" | "pr";
}

/**
 * Options for stage execution
 */
export interface StageExecuteOptions {
  /** Issue number being processed */
  issueNumber: number;
  /** Model to use (defaults to sonnet) */
  model?: "sonnet" | "opus" | "haiku";
  /** Maximum turns for SDK execution */
  maxTurns?: number;
  /** Working directory for SDK calls */
  cwd?: string;
  /** Base path for skill files */
  skillsBasePath?: string;
}

/**
 * Result of stage execution
 */
export interface StageExecuteResult<TOutput> {
  /** Whether execution succeeded */
  success: boolean;
  /** Output context (if successful) */
  output?: TOutput;
  /** Error (if failed) */
  error?: Error;
  /** All SDK messages received */
  messages: SDKMessage[];
  /** Duration in milliseconds */
  durationMs: number;
}

/**
 * Abstract base class for pipeline stages
 *
 * @example
 * ```typescript
 * class IssuePickupStage extends BaseStage<void, IssueContext> {
 *   readonly config: StageConfig<void, IssueContext> = {
 *     name: 'issue-pickup',
 *     skillPath: 'skills/nightgauge-issue-pickup/SKILL.md',
 *     outputSchema: IssueContextSchema,
 *     outputContextType: 'issue',
 *   };
 * }
 * ```
 */
export abstract class BaseStage<TInput, TOutput> {
  /**
   * Stage configuration - must be defined by subclasses
   */
  abstract readonly config: StageConfig<TInput, TOutput>;

  /**
   * Execute the stage
   *
   * @param executor - StageExecutor instance for SDK calls
   * @param contextManager - ContextManager for reading/writing context files
   * @param options - Execution options
   */
  async execute(
    executor: StageExecutor,
    contextManager: ContextManager,
    options: StageExecuteOptions
  ): Promise<StageExecuteResult<TOutput>> {
    const startTime = Date.now();
    const messages: SDKMessage[] = [];

    try {
      // Read input context if this stage requires it
      let inputContext: TInput | undefined;
      if (this.config.inputSchema && this.config.inputContextType) {
        inputContext = await this.readInputContext(contextManager, options.issueNumber);
      }

      // Build the prompt
      const prompt = await this.buildPrompt(
        options.issueNumber,
        inputContext,
        options.skillsBasePath
      );

      // Execute via StageExecutor
      const executorOptions: StageExecutorOptions = {
        stage: this.config.name,
        issueNumber: options.issueNumber,
        prompt,
        model: options.model,
        maxTurns: options.maxTurns,
        cwd: options.cwd,
      };

      for await (const message of executor.execute(executorOptions)) {
        messages.push(message);
      }

      // Read and validate output context
      const output = await this.readOutputContext(contextManager, options.issueNumber);

      return {
        success: true,
        output,
        messages,
        durationMs: Date.now() - startTime,
      };
    } catch (error) {
      return {
        success: false,
        error: error instanceof Error ? error : new Error(String(error)),
        messages,
        durationMs: Date.now() - startTime,
      };
    }
  }

  /**
   * Build the prompt by reading SKILL.md and injecting context
   */
  protected async buildPrompt(
    issueNumber: number,
    inputContext?: TInput,
    skillsBasePath: string = "skills"
  ): Promise<string> {
    // Resolve the skill path
    const skillPath = this.config.skillPath.startsWith("skills/")
      ? this.config.skillPath
      : path.join(skillsBasePath, this.config.skillPath);

    // Read the SKILL.md content
    let skillContent: string;
    try {
      skillContent = await fs.readFile(skillPath, "utf-8");
    } catch (error) {
      throw new Error(`Failed to read skill file: ${skillPath}`, { cause: error });
    }

    // Build structured prompt
    const sections: string[] = [
      `# Pipeline Stage: ${this.config.name}`,
      "",
      "## Issue",
      `Issue number: ${issueNumber}`,
      "",
    ];

    // Add input context if provided (Issue #638 - compact to reduce tokens)
    if (inputContext !== undefined) {
      sections.push("## Input Context");
      sections.push("```json");
      sections.push(compactContextJson(inputContext));
      sections.push("```");
      sections.push("");
    }

    // Add skill instructions
    sections.push("## Skill Instructions");
    sections.push("");
    sections.push(skillContent);
    sections.push("");

    // Add execution requirements
    sections.push("## Execution Requirements");
    sections.push("");
    sections.push("1. Follow the skill instructions exactly");
    sections.push("2. Write the output context file to .nightgauge/pipeline/");
    sections.push("3. Output must be valid JSON matching the expected schema");
    sections.push(
      `4. Output file should be named: ${this.config.outputContextType}-${issueNumber}.json`
    );

    return sections.join("\n");
  }

  /**
   * Read and validate input context from file
   */
  protected async readInputContext(
    contextManager: ContextManager,
    issueNumber: number
  ): Promise<TInput> {
    if (!this.config.inputSchema || !this.config.inputContextType) {
      throw new Error(
        `Stage ${this.config.name} has no input schema defined but readInputContext was called`
      );
    }

    const filename = ContextManager.getFilename(this.config.inputContextType, issueNumber);

    return contextManager.read(this.config.inputSchema, filename);
  }

  /**
   * Read and validate output context from file
   */
  protected async readOutputContext(
    contextManager: ContextManager,
    issueNumber: number
  ): Promise<TOutput> {
    const filename = ContextManager.getFilename(this.config.outputContextType, issueNumber);

    return contextManager.read(this.config.outputSchema, filename);
  }

  /**
   * Write validated output context to file
   */
  protected async writeOutputContext(
    contextManager: ContextManager,
    issueNumber: number,
    output: TOutput
  ): Promise<void> {
    const filename = ContextManager.getFilename(this.config.outputContextType, issueNumber);

    await contextManager.write(this.config.outputSchema, filename, output);
  }

  /**
   * Get the stage name
   */
  getName(): PipelineStage {
    return this.config.name;
  }

  /**
   * Check if this stage requires approval
   */
  requiresApproval(): boolean {
    return this.config.requiresApproval ?? false;
  }
}

/**
 * Maximum character length for individual string values in context JSON.
 * Strings exceeding this are truncated with a marker.
 */
const CONTEXT_STRING_MAX_LENGTH = 2000;

/**
 * Compact context JSON for prompt injection to reduce token usage.
 *
 * Strategies:
 * 1. Use single-line JSON (no 2-space indentation) — saves ~30% tokens on typical context
 * 2. Truncate excessively long string values (issue bodies, descriptions)
 * 3. Preserve all keys and structure for schema compatibility
 *
 * @param context - The input context object
 * @returns Compact JSON string
 *
 * @see Issue #638 - Pipeline token efficiency
 */
function compactContextJson(context: unknown): string {
  return JSON.stringify(context, (_key: string, value: unknown) => {
    if (typeof value === "string" && value.length > CONTEXT_STRING_MAX_LENGTH) {
      return value.slice(0, CONTEXT_STRING_MAX_LENGTH) + "... [truncated]";
    }
    return value;
  });
}
