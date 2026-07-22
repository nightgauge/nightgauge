/**
 * Server-Side Tool Result Handlers
 *
 * Handles execution of validation tool calls from the code_execution sandbox.
 * When Claude's Python code invokes `run_build()` etc. inside PTC, the executor
 * intercepts the `tool_use` content block and delegates to these handlers.
 *
 * Each handler runs a shell command via `child_process.execSync` and returns
 * structured JSON matching the tool definition's described output format.
 *
 * @see Issue #1069 - Refactor feature-validate for PTC
 * @see packages/nightgauge-sdk/src/tools/definitions/validation.ts
 */

import { execSync } from "child_process";

export interface ToolResult {
  success: boolean;
  output: Record<string, unknown>;
}

export interface ToolHandler {
  name: string;
  execute(input: Record<string, unknown>, cwd: string): Promise<ToolResult>;
}

/**
 * Run a shell command and capture output with timing.
 * Returns structured result regardless of exit code.
 */
function runCommand(
  command: string,
  cwd: string
): { exitCode: number; stdout: string; stderr: string; durationMs: number } {
  const startMs = Date.now();
  try {
    const stdout = execSync(command, {
      cwd,
      encoding: "utf-8",
      timeout: 300_000, // 5 min max
      stdio: ["pipe", "pipe", "pipe"],
      maxBuffer: 10 * 1024 * 1024, // 10 MB
    });
    return {
      exitCode: 0,
      stdout: stdout ?? "",
      stderr: "",
      durationMs: Date.now() - startMs,
    };
  } catch (error: unknown) {
    const execError = error as {
      status?: number;
      stdout?: string;
      stderr?: string;
    };
    return {
      exitCode: execError.status ?? 1,
      stdout: (execError.stdout as string) ?? "",
      stderr: (execError.stderr as string) ?? "",
      durationMs: Date.now() - startMs,
    };
  }
}

/** Handler for `run_build` tool */
export class RunBuildHandler implements ToolHandler {
  readonly name = "run_build";

  async execute(input: Record<string, unknown>, cwd: string): Promise<ToolResult> {
    const command = typeof input.command === "string" ? input.command : "npm run build";
    const workDir = typeof input.cwd === "string" ? input.cwd : cwd;
    const result = runCommand(command, workDir);

    return {
      success: result.exitCode === 0,
      output: {
        success: result.exitCode === 0,
        exit_code: result.exitCode,
        stdout: result.stdout,
        stderr: result.stderr,
        duration_ms: result.durationMs,
      },
    };
  }
}

/** Handler for `run_lint` tool */
export class RunLintHandler implements ToolHandler {
  readonly name = "run_lint";

  async execute(input: Record<string, unknown>, cwd: string): Promise<ToolResult> {
    let command = typeof input.command === "string" ? input.command : "npm run lint";
    if (input.fix === true && !command.includes("--fix")) {
      command += " -- --fix";
    }
    const workDir = typeof input.cwd === "string" ? input.cwd : cwd;
    const result = runCommand(command, workDir);

    // Parse warning/error counts from output if available
    let warningCount = 0;
    let errorCount = 0;
    const countsMatch = result.stdout.match(/(\d+)\s+error.*?(\d+)\s+warning/i);
    if (countsMatch) {
      errorCount = parseInt(countsMatch[1], 10);
      warningCount = parseInt(countsMatch[2], 10);
    }

    return {
      success: result.exitCode === 0,
      output: {
        success: result.exitCode === 0,
        exit_code: result.exitCode,
        stdout: result.stdout,
        stderr: result.stderr,
        warning_count: warningCount,
        error_count: errorCount,
        duration_ms: result.durationMs,
      },
    };
  }
}

/** Handler for `run_tests` tool */
export class RunTestsHandler implements ToolHandler {
  readonly name = "run_tests";

  async execute(input: Record<string, unknown>, cwd: string): Promise<ToolResult> {
    let command = typeof input.command === "string" ? input.command : "npm test";
    if (typeof input.pattern === "string") {
      if (!/^[A-Za-z0-9_./:@*?\[\]-]{1,512}$/.test(input.pattern)) {
        return { success: false, output: { error: "Invalid test pattern" } };
      }
      command += ` -- '${input.pattern}'`;
    }
    if (input.coverage === true) {
      command += " -- --coverage";
    }
    const workDir = typeof input.cwd === "string" ? input.cwd : cwd;
    const result = runCommand(command, workDir);

    // Parse test counts from output
    let passed = 0;
    let failed = 0;
    let skipped = 0;
    let coverage: number | undefined;

    // Vitest/Jest pattern: "Tests  X passed | Y failed | Z skipped"
    const testsMatch = result.stdout.match(/Tests?\s+(\d+)\s+passed.*?(\d+)\s+failed/i);
    if (testsMatch) {
      passed = parseInt(testsMatch[1], 10);
      failed = parseInt(testsMatch[2], 10);
    }
    const skipMatch = result.stdout.match(/(\d+)\s+skipped/i);
    if (skipMatch) {
      skipped = parseInt(skipMatch[1], 10);
    }
    const covMatch = result.stdout.match(/All files[^|]*\|\s*([\d.]+)/);
    if (covMatch) {
      coverage = parseFloat(covMatch[1]);
    }

    return {
      success: result.exitCode === 0,
      output: {
        success: result.exitCode === 0,
        exit_code: result.exitCode,
        passed,
        failed,
        skipped,
        coverage,
        stdout: result.stdout,
        stderr: result.stderr,
        duration_ms: result.durationMs,
      },
    };
  }
}

/** Handler for `run_typecheck` tool */
export class RunTypecheckHandler implements ToolHandler {
  readonly name = "run_typecheck";

  async execute(input: Record<string, unknown>, cwd: string): Promise<ToolResult> {
    const command = typeof input.command === "string" ? input.command : "npx tsc --noEmit";
    const workDir = typeof input.cwd === "string" ? input.cwd : cwd;
    const result = runCommand(command, workDir);

    // Count TS errors from output
    let errorCount = 0;
    const errorLines = result.stdout.match(/error TS\d+/g);
    if (errorLines) {
      errorCount = errorLines.length;
    }

    return {
      success: result.exitCode === 0,
      output: {
        success: result.exitCode === 0,
        exit_code: result.exitCode,
        stdout: result.stdout,
        stderr: result.stderr,
        error_count: errorCount,
        duration_ms: result.durationMs,
      },
    };
  }
}

/**
 * Create the default validation tool handler map.
 * Maps tool names to their server-side handler implementations.
 */
// Re-export context and git handler factories for convenience
export { createContextHandlers } from "./context-handlers.js";
export { createGitHandlers } from "./git-handlers.js";

/**
 * Create the default validation tool handler map.
 * Maps tool names to their server-side handler implementations.
 */
export function createValidationHandlers(): Map<string, ToolHandler> {
  const handlers = new Map<string, ToolHandler>();
  const instances: ToolHandler[] = [
    new RunBuildHandler(),
    new RunLintHandler(),
    new RunTestsHandler(),
    new RunTypecheckHandler(),
  ];
  for (const handler of instances) {
    handlers.set(handler.name, handler);
  }
  return handlers;
}
