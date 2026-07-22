/**
 * PTCContextGatherer - Context Gathering PTC Orchestration
 *
 * Constructs a context-gathering prompt, invokes PTCExecutor, and maps
 * results to a structured ContextGatherResult. This enables batch reading
 * of all pipeline context files and git operations in a single PTC session,
 * reducing round-trips from 6+ to 1.
 *
 * @see Issue #1070 - Optimize context file and git batch operations
 */

import { PTCExecutor, type PTCExecutorOptions, type PTCResult } from "./PTCExecutor.js";
import { CONTEXT_TOOLS, GIT_TOOLS } from "./definitions/index.js";
import { createContextHandlers } from "./context-handlers.js";
import { createGitHandlers } from "./git-handlers.js";

/** Input for context gathering */
export interface ContextGatherInput {
  /** Issue number to gather context for */
  issueNumber: number;
  /** Base branch for git diff comparison */
  baseBranch: string;
  /** Pipeline stages to read context for (e.g., ["issue", "planning", "dev", "validate"]) */
  stages: string[];
  /** Whether to read batch context files */
  batchMode?: boolean;
  /** Epic number for batch mode */
  epicNumber?: number;
}

/** Result from context gathering */
export interface ContextGatherResult {
  /** Whether gathering completed successfully */
  success: boolean;
  /** Parsed context files keyed by stage name */
  contexts: Record<string, unknown>;
  /** Git operation results */
  git: {
    diff: Record<string, unknown>;
    log: Record<string, unknown>;
    status: Record<string, unknown>;
  };
  /** Token usage for the PTC session */
  tokenUsage: { inputTokens: number; outputTokens: number };
  /** Number of conversation turns */
  turns: number;
  /** Total tool calls made during PTC context gathering (Issue #1071) */
  toolCallCount: number;
  /** Code execution blocks run (Issue #1071) */
  codeExecutionCount: number;
  /** Error message if gathering failed */
  error?: string;
}

/** Options for the context gatherer */
export interface PTCContextGathererOptions {
  /** Anthropic API key */
  apiKey: string;
  /** Model to use (defaults to claude-sonnet-4-5-20250929) */
  model?: string;
  /** Working directory */
  cwd: string;
  /** Context gather input */
  gatherInput: ContextGatherInput;
}

/**
 * Orchestrates PTC context gathering: builds the prompt, runs the executor,
 * and parses results into a structured ContextGatherResult.
 */
export class PTCContextGatherer {
  private readonly options: PTCContextGathererOptions;

  constructor(options: PTCContextGathererOptions) {
    this.options = options;
  }

  /**
   * Run the PTC context gathering session.
   *
   * Returns a structured ContextGatherResult whether PTC succeeds or fails.
   * On PTC failure, the caller should fall back to individual file reads.
   */
  async run(): Promise<ContextGatherResult> {
    const contextHandlers = createContextHandlers();
    const gitHandlers = createGitHandlers();

    // Combine handler maps
    const allHandlers = new Map([...contextHandlers, ...gitHandlers]);

    // Exclude write_context_file from tools (not needed for reads)
    const readOnlyContextTools = CONTEXT_TOOLS.filter((t) => t.name !== "write_context_file");

    const executorOpts: PTCExecutorOptions = {
      apiKey: this.options.apiKey,
      model: this.options.model,
      tools: [...readOnlyContextTools, ...GIT_TOOLS],
      toolHandlers: allHandlers,
      cwd: this.options.cwd,
      maxTurns: 10,
    };

    const executor = new PTCExecutor(executorOpts);
    const prompt = this.buildPrompt();

    let result: PTCResult;
    try {
      result = await executor.execute(prompt);
    } catch (err: unknown) {
      const errMsg = err instanceof Error ? err.message : "PTC execution failed";
      return this.errorResult(errMsg);
    }

    if (!result.success) {
      return this.errorResult(result.error ?? "PTC execution returned failure");
    }

    return this.parseResult(result);
  }

  /** Construct the context gathering prompt for the PTC sandbox */
  private buildPrompt(): string {
    const input = this.options.gatherInput;
    const contextFiles = input.stages.map((stage) => `${stage}-${input.issueNumber}.json`);

    let batchSection = "";
    if (input.batchMode && input.epicNumber) {
      const batchFiles = input.stages.map((stage) => `${stage}-batch-${input.epicNumber}.json`);
      batchSection = `
## Batch Mode
Also read these batch context files:
${batchFiles.map((f) => `- "${f}"`).join("\n")}

Include batch results under the "batch_contexts" key in output.`;
    }

    return `You are a context gatherer for issue #${input.issueNumber}.

Your task: Read all pipeline context files and git state, then return a single JSON summary.
Write Python code that calls the available tools to gather all data.

## Context Files to Read
${contextFiles.map((f) => `- "${f}"`).join("\n")}
${batchSection}

## Git Operations
1. Call git_diff_summary(base="${input.baseBranch}")
2. Call git_log_structured(count=20)
3. Call git_status_structured()

## Instructions

For each context file, call read_context_file(filename=<name>).
If a file is not found, record it as null (do not fail).

After all tools complete, output a single JSON object to stdout:
{
  "contexts": {
    "<stage>": <parsed content or null>,
    ...
  },
  "git": {
    "diff": <git_diff_summary result>,
    "log": <git_log_structured result>,
    "status": <git_status_structured result>
  }
}

Output ONLY the JSON object, no additional text.`;
  }

  /** Parse PTC result into a structured ContextGatherResult */
  private parseResult(ptcResult: PTCResult): ContextGatherResult {
    const usage = {
      inputTokens: ptcResult.usage.inputTokens,
      outputTokens: ptcResult.usage.outputTokens,
    };

    // Try to parse structured output
    const parsed = this.extractOutput(ptcResult);
    if (!parsed) {
      return {
        ...this.errorResult("Failed to parse PTC output as JSON"),
        tokenUsage: usage,
        turns: ptcResult.turns,
        toolCallCount: ptcResult.toolCallCount,
        codeExecutionCount: ptcResult.codeExecutionCount,
      };
    }

    return {
      success: true,
      contexts: this.parseContexts(parsed.contexts),
      git: {
        diff: this.parseGitSection(parsed.git?.diff),
        log: this.parseGitSection(parsed.git?.log),
        status: this.parseGitSection(parsed.git?.status),
      },
      tokenUsage: usage,
      turns: ptcResult.turns,
      toolCallCount: ptcResult.toolCallCount,
      codeExecutionCount: ptcResult.codeExecutionCount,
    };
  }

  /** Extract parsed output from PTCResult (object or text) */
  private extractOutput(
    ptcResult: PTCResult
  ): { contexts?: unknown; git?: Record<string, unknown> } | null {
    if (ptcResult.output && typeof ptcResult.output === "object") {
      return ptcResult.output as {
        contexts?: unknown;
        git?: Record<string, unknown>;
      };
    }

    try {
      return JSON.parse(ptcResult.textOutput.trim());
    } catch {
      return null;
    }
  }

  /** Parse contexts from output, defaulting to empty object */
  private parseContexts(raw: unknown): Record<string, unknown> {
    if (raw && typeof raw === "object" && !Array.isArray(raw)) {
      return raw as Record<string, unknown>;
    }
    return {};
  }

  /** Parse a git section, defaulting to empty object */
  private parseGitSection(raw: unknown): Record<string, unknown> {
    if (raw && typeof raw === "object" && !Array.isArray(raw)) {
      return raw as Record<string, unknown>;
    }
    return {};
  }

  /** Create a failure result with error message */
  private errorResult(error: string): ContextGatherResult {
    return {
      success: false,
      contexts: {},
      git: { diff: {}, log: {}, status: {} },
      tokenUsage: { inputTokens: 0, outputTokens: 0 },
      turns: 0,
      toolCallCount: 0,
      codeExecutionCount: 0,
      error,
    };
  }
}
