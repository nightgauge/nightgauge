/**
 * Ralph Loop Controller - Deterministic loop control for self-healing pipeline stages
 *
 * The Ralph Wiggum Loop is an agentic pattern where an AI agent:
 * 1. Executes a task (e.g., build, test)
 * 2. Evaluates the result (checks exit code)
 * 3. Self-diagnoses failures (parses error output)
 * 4. Attempts correction (AI generates fix)
 * 5. Repeats until success OR safety limits reached
 *
 * This module provides DETERMINISTIC loop control. The iteration count, token budget,
 * and timeout enforcement are all handled by code, not AI decisions. Only the "fix attempt"
 * step is probabilistic (AI-driven).
 *
 * @see docs/RALPH_LOOP.md - Complete documentation
 * @see docs/ARCHITECTURE.md - Deterministic vs Probabilistic Architecture
 * @see Issue #83 - Ralph Wiggum Loop integration
 */

import type { ClassifiedError } from "./errorClassifier";

/**
 * Configuration for Ralph Loop behavior
 *
 * All values have safe defaults. Override via .nightgauge/config.yaml or environment.
 */
export interface RalphLoopConfig {
  /** Enable/disable Ralph Loop (default: true for feature-validate) */
  enabled: boolean;

  /** Enable for build phase (default: true) */
  build_enabled: boolean;

  /** Enable for test phase (default: true) */
  tests_enabled: boolean;

  /** Enable for lint phase (default: false, future) */
  lint_enabled: boolean;

  /** Maximum fix attempts per error type (default: 3) */
  max_iterations: number;

  /** Token budget per iteration in tokens (default: 2000) */
  token_budget_per_iteration: number;

  /** Total token budget for all iterations (default: 10000) */
  total_token_budget: number;

  /** Timeout per iteration in ms (default: 60000 = 1 minute) */
  iteration_timeout_ms: number;

  /** Total timeout for all iterations in ms (default: 300000 = 5 minutes) */
  total_timeout_ms: number;

  /** Patterns that immediately abort Ralph Loop (requires human) */
  abort_patterns: string[];
}

/**
 * Default configuration values
 *
 * Safe, conservative defaults that work for most projects.
 */
export const DEFAULT_RALPH_LOOP_CONFIG: RalphLoopConfig = {
  enabled: true,
  build_enabled: true,
  tests_enabled: true,
  lint_enabled: false,
  max_iterations: 3,
  token_budget_per_iteration: 2000,
  total_token_budget: 10000,
  iteration_timeout_ms: 60000,
  total_timeout_ms: 300000,
  abort_patterns: [
    "Module not found",
    "Cannot find module",
    "ENOENT",
    "Permission denied",
    "Out of memory",
    "Segmentation fault",
    "EACCES",
    "EPERM",
    "ENOMEM",
    "npm ERR! code ERESOLVE",
    "npm ERR! code E404",
    "pip._vendor.urllib3.exceptions",
    "ModuleNotFoundError",
  ],
};

/**
 * State of a Ralph Loop iteration
 */
export interface RalphLoopIteration {
  /** Iteration number (1-based) */
  iteration: number;

  /** Error being fixed in this iteration */
  error: ClassifiedError;

  /** Tokens consumed in this iteration */
  tokens_consumed: number;

  /** Duration in ms */
  duration_ms: number;

  /** Whether the fix was successful */
  success: boolean;

  /** Fix description (from AI) */
  fix_description?: string;

  /** Files modified */
  files_modified?: string[];
}

/**
 * Result of a Ralph Loop execution
 */
export interface RalphLoopResult {
  /** Whether the overall operation succeeded */
  success: boolean;

  /** Total iterations executed */
  iterations_count: number;

  /** Detailed iteration history */
  iterations: RalphLoopIteration[];

  /** Total tokens consumed across all iterations */
  total_tokens: number;

  /** Total time spent in ms */
  total_time_ms: number;

  /** Final status message */
  message: string;

  /** Abort reason if stopped early */
  abort_reason?: "max_iterations" | "token_budget" | "timeout" | "abort_pattern" | "user_cancelled";

  /** The pattern that triggered abort (if abort_reason === 'abort_pattern') */
  abort_pattern?: string;
}

/**
 * Loop state for tracking progress
 */
export interface RalphLoopState {
  /** Loop ID for logging */
  loop_id: string;

  /** Phase being fixed (build, tests, lint) */
  phase: "build" | "tests" | "lint";

  /** Current iteration (1-based) */
  current_iteration: number;

  /** Total tokens consumed so far */
  tokens_consumed: number;

  /** Start time of the loop */
  start_time: number;

  /** Iteration history */
  iterations: RalphLoopIteration[];

  /** Whether the loop is still running */
  is_running: boolean;

  /** Whether the loop was aborted */
  was_aborted: boolean;
}

/**
 * Check if Ralph Loop is enabled for a given phase
 *
 * @param config - Ralph Loop configuration
 * @param phase - The phase to check (build, tests, lint)
 * @returns True if Ralph Loop is enabled for this phase
 */
export function isLoopEnabledForPhase(
  config: RalphLoopConfig,
  phase: "build" | "tests" | "lint"
): boolean {
  if (!config.enabled) {
    return false;
  }

  switch (phase) {
    case "build":
      return config.build_enabled;
    case "tests":
      return config.tests_enabled;
    case "lint":
      return config.lint_enabled;
  }
}

/**
 * Check if an error matches any abort patterns
 *
 * Abort patterns are errors that require human intervention and should not be
 * auto-fixed. Examples: missing dependencies, permission issues, out of memory.
 *
 * @param error - The classified error to check
 * @param config - Ralph Loop configuration with abort patterns
 * @returns The matching abort pattern if found, null otherwise
 */
export function matchesAbortPattern(
  error: ClassifiedError,
  config: RalphLoopConfig
): string | null {
  const errorText = `${error.message} ${error.rawOutput}`.toLowerCase();

  for (const pattern of config.abort_patterns) {
    if (errorText.includes(pattern.toLowerCase())) {
      return pattern;
    }
  }

  return null;
}

/**
 * Check if an error should trigger the Ralph Loop
 *
 * Only 'fixable' errors trigger the loop. Architectural, configuration,
 * and unknown errors are escalated to human.
 *
 * @param error - The classified error to check
 * @returns True if the error should trigger self-healing
 */
export function shouldAttemptFix(error: ClassifiedError): boolean {
  return error.severity === "fixable";
}

/**
 * Check if the loop can continue (hasn't exceeded limits)
 *
 * This is the core safety function that enforces deterministic limits.
 *
 * @param state - Current loop state
 * @param config - Ralph Loop configuration
 * @returns Object with canContinue flag and reason if blocked
 */
export function canContinueLoop(
  state: RalphLoopState,
  config: RalphLoopConfig
): { canContinue: boolean; reason?: string } {
  // Check iteration limit
  if (state.current_iteration >= config.max_iterations) {
    return {
      canContinue: false,
      reason: `Maximum iterations (${config.max_iterations}) reached`,
    };
  }

  // Check total token budget
  if (state.tokens_consumed >= config.total_token_budget) {
    return {
      canContinue: false,
      reason: `Total token budget (${config.total_token_budget}) exhausted`,
    };
  }

  // Check total timeout
  const elapsed = Date.now() - state.start_time;
  if (elapsed >= config.total_timeout_ms) {
    return {
      canContinue: false,
      reason: `Total timeout (${config.total_timeout_ms}ms) exceeded`,
    };
  }

  return { canContinue: true };
}

/**
 * Check if an iteration timed out
 *
 * @param startTime - Iteration start time (ms since epoch)
 * @param config - Ralph Loop configuration
 * @returns True if the iteration has exceeded its timeout
 */
export function hasIterationTimedOut(startTime: number, config: RalphLoopConfig): boolean {
  return Date.now() - startTime >= config.iteration_timeout_ms;
}

/**
 * Create a new loop state for tracking
 *
 * @param phase - The phase being fixed (build, tests, lint)
 * @returns Fresh loop state
 */
export function createLoopState(phase: "build" | "tests" | "lint"): RalphLoopState {
  return {
    loop_id: `ralph-${phase}-${Date.now()}`,
    phase,
    current_iteration: 0,
    tokens_consumed: 0,
    start_time: Date.now(),
    iterations: [],
    is_running: true,
    was_aborted: false,
  };
}

/**
 * Record an iteration in the loop state
 *
 * @param state - Current loop state (mutated)
 * @param iteration - The completed iteration
 */
export function recordIteration(state: RalphLoopState, iteration: RalphLoopIteration): void {
  state.iterations.push(iteration);
  state.tokens_consumed += iteration.tokens_consumed;
  state.current_iteration = iteration.iteration;
}

/**
 * Create a final result from the loop state
 *
 * @param state - Final loop state
 * @param success - Whether the overall operation succeeded
 * @param abortReason - Reason for early abort (if any)
 * @param abortPattern - The pattern that triggered abort (if abort_reason === 'abort_pattern')
 * @returns Complete loop result
 */
export function createLoopResult(
  state: RalphLoopState,
  success: boolean,
  abortReason?: RalphLoopResult["abort_reason"],
  abortPattern?: string
): RalphLoopResult {
  const totalTimeMs = Date.now() - state.start_time;

  let message: string;
  if (success) {
    message = `Fixed after ${state.iterations.length} iteration(s)`;
  } else if (abortReason) {
    switch (abortReason) {
      case "max_iterations":
        message = `Failed after maximum ${state.iterations.length} iterations`;
        break;
      case "token_budget":
        message = `Failed: token budget exhausted (${state.tokens_consumed} tokens)`;
        break;
      case "timeout":
        message = `Failed: timeout exceeded (${totalTimeMs}ms)`;
        break;
      case "abort_pattern":
        message = `Aborted: matched abort pattern "${abortPattern}"`;
        break;
      case "user_cancelled":
        message = "Cancelled by user";
        break;
    }
  } else {
    message = "Failed to fix after all attempts";
  }

  return {
    success,
    iterations_count: state.iterations.length,
    iterations: state.iterations,
    total_tokens: state.tokens_consumed,
    total_time_ms: totalTimeMs,
    message,
    abort_reason: abortReason,
    abort_pattern: abortPattern,
  };
}

/**
 * Calculate remaining token budget
 *
 * @param state - Current loop state
 * @param config - Ralph Loop configuration
 * @returns Remaining tokens available
 */
export function getRemainingTokenBudget(state: RalphLoopState, config: RalphLoopConfig): number {
  const remaining = config.total_token_budget - state.tokens_consumed;
  // Don't exceed per-iteration budget
  return Math.min(remaining, config.token_budget_per_iteration);
}

/**
 * Calculate remaining time budget
 *
 * @param state - Current loop state
 * @param config - Ralph Loop configuration
 * @returns Remaining time in ms
 */
export function getRemainingTimeBudget(state: RalphLoopState, config: RalphLoopConfig): number {
  const elapsed = Date.now() - state.start_time;
  const totalRemaining = config.total_timeout_ms - elapsed;
  // Don't exceed per-iteration timeout
  return Math.min(totalRemaining, config.iteration_timeout_ms);
}

/**
 * Format loop result for logging/display
 *
 * @param result - The loop result to format
 * @returns Human-readable summary
 */
export function formatLoopResult(result: RalphLoopResult): string {
  const lines: string[] = [];

  if (result.success) {
    lines.push(`✓ Ralph Loop: ${result.message}`);
  } else {
    lines.push(`✗ Ralph Loop: ${result.message}`);
  }

  lines.push(`  Iterations: ${result.iterations_count}`);
  lines.push(`  Tokens: ${result.total_tokens}`);
  lines.push(`  Time: ${result.total_time_ms}ms`);

  if (result.abort_reason) {
    lines.push(`  Abort reason: ${result.abort_reason}`);
  }

  return lines.join("\n");
}

/**
 * Merge user config with defaults
 *
 * @param userConfig - Partial user configuration
 * @returns Complete configuration with defaults
 */
export function mergeConfig(userConfig: Partial<RalphLoopConfig>): RalphLoopConfig {
  return {
    ...DEFAULT_RALPH_LOOP_CONFIG,
    ...userConfig,
    abort_patterns: [
      ...DEFAULT_RALPH_LOOP_CONFIG.abort_patterns,
      ...(userConfig.abort_patterns ?? []),
    ],
  };
}

/**
 * Load config from environment variables
 *
 * Environment variables override config.yaml values.
 *
 * @returns Partial config from environment
 */
export function loadConfigFromEnv(): Partial<RalphLoopConfig> {
  const config: Partial<RalphLoopConfig> = {};

  if (process.env.NIGHTGAUGE_RALPH_LOOP_ENABLED !== undefined) {
    config.enabled = process.env.NIGHTGAUGE_RALPH_LOOP_ENABLED !== "false";
  }

  if (process.env.NIGHTGAUGE_RALPH_LOOP_MAX_ITERATIONS !== undefined) {
    const value = parseInt(process.env.NIGHTGAUGE_RALPH_LOOP_MAX_ITERATIONS, 10);
    if (!isNaN(value) && value > 0) {
      config.max_iterations = value;
    }
  }

  if (process.env.NIGHTGAUGE_RALPH_LOOP_TOKEN_BUDGET !== undefined) {
    const value = parseInt(process.env.NIGHTGAUGE_RALPH_LOOP_TOKEN_BUDGET, 10);
    if (!isNaN(value) && value > 0) {
      config.token_budget_per_iteration = value;
    }
  }

  if (process.env.NIGHTGAUGE_RALPH_LOOP_TOTAL_TOKEN_BUDGET !== undefined) {
    const value = parseInt(process.env.NIGHTGAUGE_RALPH_LOOP_TOTAL_TOKEN_BUDGET, 10);
    if (!isNaN(value) && value > 0) {
      config.total_token_budget = value;
    }
  }

  if (process.env.NIGHTGAUGE_RALPH_LOOP_ITERATION_TIMEOUT !== undefined) {
    const value = parseInt(process.env.NIGHTGAUGE_RALPH_LOOP_ITERATION_TIMEOUT, 10);
    if (!isNaN(value) && value > 0) {
      config.iteration_timeout_ms = value;
    }
  }

  if (process.env.NIGHTGAUGE_RALPH_LOOP_TOTAL_TIMEOUT !== undefined) {
    const value = parseInt(process.env.NIGHTGAUGE_RALPH_LOOP_TOTAL_TIMEOUT, 10);
    if (!isNaN(value) && value > 0) {
      config.total_timeout_ms = value;
    }
  }

  return config;
}
