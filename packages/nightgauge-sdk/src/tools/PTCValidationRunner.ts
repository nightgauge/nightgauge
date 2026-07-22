/**
 * PTCValidationRunner - Validation-Specific PTC Orchestration
 *
 * Constructs the validation prompt, invokes PTCExecutor, and maps
 * results to the validate-{N}.json schema (v1.4 compatible).
 *
 * The runner tells Claude to write Python code that programmatically
 * calls validation tools (`run_build`, `run_tests`, `run_lint`,
 * `run_typecheck`) with short-circuiting on build failure.
 *
 * @see Issue #1069 - Refactor feature-validate for PTC
 */

import { PTCExecutor, type PTCExecutorOptions, type PTCResult } from "./PTCExecutor.js";
import { VALIDATION_TOOLS } from "./definitions/index.js";
import { createValidationHandlers } from "./tool-handlers.js";
import { mapSourceToTestFiles, isCrossCuttingChange } from "../analysis/testFileMapper.js";

/** Dev context needed to construct the validation prompt */
export interface DevContextInput {
  issueNumber: number;
  commitSha: string;
  filesCreated: string[];
  filesModified: string[];
  buildAlreadyPassed: boolean;
  unitTestsPassed: number;
  unitTestsFailed: number;
}

/** Structured validation result mapped to validate-{N}.json fields */
export interface ValidationResult {
  success: boolean;
  build: {
    ran: boolean;
    passed: boolean;
    command?: string;
    skippedReason?: string;
  };
  lint: {
    ran: boolean;
    passed: boolean;
    warningCount: number;
    errorCount: number;
  };
  typecheck: {
    ran: boolean;
    passed: boolean;
    errorCount: number;
  };
  tests: {
    ran: boolean;
    passed: number;
    failed: number;
    skipped: number;
    coverage?: number;
  };
  tokenUsage: {
    inputTokens: number;
    outputTokens: number;
  };
  turns: number;
  /** Total tool calls made during PTC validation (Issue #1071) */
  toolCallCount: number;
  /** Code execution blocks run (Issue #1071) */
  codeExecutionCount: number;
  error?: string;
}

/** Targeted test selection mode */
export type TargetedTestsMode = "auto" | "always" | "never";

/** Options for the validation runner */
export interface PTCValidationRunnerOptions {
  /** Anthropic API key */
  apiKey: string;
  /** Model to use (defaults to claude-sonnet-4-5-20250929) */
  model?: string;
  /** Working directory */
  cwd: string;
  /** Dev context from feature-dev stage */
  devContext: DevContextInput;
  /** Targeted test selection mode (default: 'auto') @since Issue #1046 */
  targetedTests?: TargetedTestsMode;
}

/**
 * Orchestrates PTC validation: builds the prompt, runs the executor,
 * and parses results into a structured ValidationResult.
 */
export class PTCValidationRunner {
  private readonly options: PTCValidationRunnerOptions;

  constructor(options: PTCValidationRunnerOptions) {
    this.options = options;
  }

  /**
   * Run the PTC validation session.
   *
   * Returns a structured ValidationResult whether PTC succeeds or fails.
   * On PTC failure, the caller should fall back to direct tool calling.
   */
  async run(): Promise<ValidationResult> {
    const handlers = createValidationHandlers();
    const executorOpts: PTCExecutorOptions = {
      apiKey: this.options.apiKey,
      model: this.options.model,
      tools: VALIDATION_TOOLS,
      toolHandlers: handlers,
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

  /** Construct the validation prompt for the PTC sandbox */
  private buildPrompt(): string {
    const ctx = this.options.devContext;
    const skipBuild = ctx.buildAlreadyPassed;
    const skipUnitTests = ctx.unitTestsPassed > 0 && ctx.unitTestsFailed === 0;

    // Targeted test selection (Issue #1046)
    const targetedTestsMode = this.options.targetedTests ?? "auto";
    let testInstruction: string;
    let targetedTestPattern: string | undefined;

    if (skipUnitTests) {
      testInstruction = "SKIP run_tests (already passed in dev)";
    } else if (targetedTestsMode !== "never") {
      const allChangedFiles = [...ctx.filesCreated, ...ctx.filesModified];
      const crossCutting = isCrossCuttingChange(allChangedFiles);

      if (!crossCutting || targetedTestsMode === "always") {
        const testFiles = mapSourceToTestFiles(allChangedFiles, this.options.cwd);
        if (testFiles.length > 0) {
          targetedTestPattern = testFiles.join(" ");
          testInstruction = `Call run_tests({ pattern: "${targetedTestPattern}" }). Record result. (Targeted: ${testFiles.length} test file(s) matching changed sources)`;
        } else {
          testInstruction =
            "Call run_tests(). Record result. (No targeted test files found — running full suite)";
        }
      } else {
        testInstruction =
          "Call run_tests(). Record result. (Cross-cutting change detected — running full suite)";
      }
    } else {
      testInstruction = "Call run_tests(). Record result.";
    }

    return `You are a validation executor for issue #${ctx.issueNumber}.

Your task: Run validation tools and return a structured JSON summary.
Write Python code that calls the available tools in sequence.

## Context
- Commit: ${ctx.commitSha}
- Files created: ${JSON.stringify(ctx.filesCreated)}
- Files modified: ${JSON.stringify(ctx.filesModified)}
${skipBuild ? "- Build already passed in feature-dev stage (SKIP build)" : "- Build has not been verified yet (RUN build)"}
${skipUnitTests ? `- Unit tests already passed (${ctx.unitTestsPassed} passed, 0 failed) in feature-dev (SKIP unit tests)` : `- Unit tests need re-running (${ctx.unitTestsPassed} passed, ${ctx.unitTestsFailed} failed)`}

## Instructions

Call the validation tools in this order. Short-circuit on build failure.

1. ${skipBuild ? "SKIP run_build (already passed)" : "Call run_build(). If it fails, stop and report failure."}
2. Call run_typecheck(). Record result but continue.
3. Call run_lint(). Record result but continue.
4. ${testInstruction}

After all tools complete, output a single JSON object to stdout with this structure:
{
  "build": { "ran": boolean, "passed": boolean, "command": "..." },
  "typecheck": { "ran": boolean, "passed": boolean, "error_count": number },
  "lint": { "ran": boolean, "passed": boolean, "warning_count": number, "error_count": number },
  "tests": { "ran": boolean, "passed": number, "failed": number, "skipped": number }
}

Output ONLY the JSON object, no additional text.`;
  }

  /** Parse PTC result into a structured ValidationResult */
  private parseResult(ptcResult: PTCResult): ValidationResult {
    const usage = {
      inputTokens: ptcResult.usage.inputTokens,
      outputTokens: ptcResult.usage.outputTokens,
    };

    // Try to parse structured output
    if (ptcResult.output && typeof ptcResult.output === "object") {
      const out = ptcResult.output as Record<string, unknown>;
      return {
        success: true,
        build: this.parseBuildResult(out.build),
        lint: this.parseLintResult(out.lint),
        typecheck: this.parseTypecheckResult(out.typecheck),
        tests: this.parseTestsResult(out.tests),
        tokenUsage: usage,
        turns: ptcResult.turns,
        toolCallCount: ptcResult.toolCallCount,
        codeExecutionCount: ptcResult.codeExecutionCount,
      };
    }

    // Fallback: try to parse from text output
    try {
      const parsed = JSON.parse(ptcResult.textOutput.trim());
      return {
        success: true,
        build: this.parseBuildResult(parsed.build),
        lint: this.parseLintResult(parsed.lint),
        typecheck: this.parseTypecheckResult(parsed.typecheck),
        tests: this.parseTestsResult(parsed.tests),
        tokenUsage: usage,
        turns: ptcResult.turns,
        toolCallCount: ptcResult.toolCallCount,
        codeExecutionCount: ptcResult.codeExecutionCount,
      };
    } catch {
      return {
        ...this.errorResult("Failed to parse PTC output as JSON"),
        tokenUsage: usage,
        turns: ptcResult.turns,
        toolCallCount: ptcResult.toolCallCount,
        codeExecutionCount: ptcResult.codeExecutionCount,
      };
    }
  }

  private parseBuildResult(raw: unknown): ValidationResult["build"] {
    if (!raw || typeof raw !== "object") {
      return { ran: false, passed: false, skippedReason: "no data" };
    }
    const obj = raw as Record<string, unknown>;
    return {
      ran: obj.ran === true,
      passed: obj.passed === true,
      command: typeof obj.command === "string" ? obj.command : undefined,
      skippedReason: typeof obj.skipped_reason === "string" ? obj.skipped_reason : undefined,
    };
  }

  private parseLintResult(raw: unknown): ValidationResult["lint"] {
    if (!raw || typeof raw !== "object") {
      return { ran: false, passed: false, warningCount: 0, errorCount: 0 };
    }
    const obj = raw as Record<string, unknown>;
    return {
      ran: obj.ran === true,
      passed: obj.passed === true,
      warningCount: typeof obj.warning_count === "number" ? obj.warning_count : 0,
      errorCount: typeof obj.error_count === "number" ? obj.error_count : 0,
    };
  }

  private parseTypecheckResult(raw: unknown): ValidationResult["typecheck"] {
    if (!raw || typeof raw !== "object") {
      return { ran: false, passed: false, errorCount: 0 };
    }
    const obj = raw as Record<string, unknown>;
    return {
      ran: obj.ran === true,
      passed: obj.passed === true,
      errorCount: typeof obj.error_count === "number" ? obj.error_count : 0,
    };
  }

  private parseTestsResult(raw: unknown): ValidationResult["tests"] {
    if (!raw || typeof raw !== "object") {
      return { ran: false, passed: 0, failed: 0, skipped: 0 };
    }
    const obj = raw as Record<string, unknown>;
    return {
      ran: obj.ran === true,
      passed: typeof obj.passed === "number" ? obj.passed : 0,
      failed: typeof obj.failed === "number" ? obj.failed : 0,
      skipped: typeof obj.skipped === "number" ? obj.skipped : 0,
      coverage: typeof obj.coverage === "number" ? obj.coverage : undefined,
    };
  }

  /** Create a failure result with error message */
  private errorResult(error: string): ValidationResult {
    return {
      success: false,
      build: { ran: false, passed: false },
      lint: { ran: false, passed: false, warningCount: 0, errorCount: 0 },
      typecheck: { ran: false, passed: false, errorCount: 0 },
      tests: { ran: false, passed: 0, failed: 0, skipped: 0 },
      tokenUsage: { inputTokens: 0, outputTokens: 0 },
      turns: 0,
      toolCallCount: 0,
      codeExecutionCount: 0,
      error,
    };
  }
}

/**
 * Check if PTC is available in the current environment.
 *
 * PTC requires:
 * 1. ANTHROPIC_API_KEY environment variable set
 * 2. Validation tool definitions available
 *
 * @returns true if PTC can be used, false for fallback to direct calling
 */
export function isPTCAvailable(): boolean {
  return (
    typeof process.env.ANTHROPIC_API_KEY === "string" && process.env.ANTHROPIC_API_KEY.length > 0
  );
}
