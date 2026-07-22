/**
 * Types for the Selective Test Runner module.
 *
 * Defines configuration and result interfaces for graph-backed
 * selective test selection in the feature-validate pipeline stage.
 *
 * @see Issue #1973 - Selective Test Runner
 */

/** Configuration for the SelectiveTestRunner. */
export interface SelectiveTestRunnerConfig {
  /** Path to pre-built graph file. If missing, graph is built at runtime. */
  graphCachePath?: string;
  /** Root directory for graph building (defaults to process.cwd()). */
  projectRoot?: string;
  /** "auto" = use selective when isolated; "always" = always selective; "never" = always full. */
  mode: "auto" | "always" | "never";
  /** Minimum confidence level to include a test file. Default: "low". */
  minConfidence?: "high" | "medium" | "low";
}

/** Result of selective test selection. */
export interface SelectiveTestResult {
  /** Execution mode selected. */
  mode: "selective" | "full" | "skipped";
  /** Reason for the selected mode. */
  reason: string;
  /** Test files to pass to Vitest. null = run full suite. */
  testFiles: string[] | null;
  /** Impact level from the analyzer. */
  impactLevel: string;
  /** Total test files known in the graph. null if total count unknown. */
  totalTests: number | null;
  /** Number of test files selected for execution. */
  selectedTests: number;
  /** Number of test files skipped. null if full suite. */
  skippedTests: number | null;
  /** CLI arguments to append to the test command. */
  vitestArgs: string[];
  /**
   * Estimated tokens saved by skipping tests.
   * Populated by SelectiveTestMetricsCollector constants.
   * Optional — only set when skipped tests are known.
   */
  estimatedTokensSaved?: number;
  /**
   * Estimated wall-clock time saved (ms) by skipping tests.
   * Optional — only set when skipped tests are known.
   */
  estimatedTimeSavedMs?: number;
}
