/**
 * PTCMetrics - Programmatic Tool Calling metrics aggregation
 *
 * Defines the ProgrammaticToolMetrics interface and provides utilities
 * for aggregating per-stage PTC data into summary metrics.
 *
 * @see Issue #1071 - Track PTC metrics
 */

/**
 * Per-stage PTC usage data reported by PTCExecutor/ValidationRunner/ContextGatherer
 */
export interface PTCStageUsage {
  /** Pipeline stage name */
  stage: string;
  /** Number of tool calls made programmatically (via PTC) */
  programmaticCalls: number;
  /** Number of tool calls made directly (via headless CLI) */
  directCalls: number;
  /** Estimated tokens saved by using PTC vs direct calls */
  estimatedTokensSaved: number;
  /** Number of code_execution blocks run in the PTC session */
  codeExecutionCount: number;
  /** Number of container reuses (turns that reused the sandbox) */
  containerReuseCount: number;
  /** Input tokens consumed by the PTC session */
  inputTokens: number;
  /** Output tokens consumed by the PTC session */
  outputTokens: number;
  /** Estimated cost in USD for the PTC session */
  estimatedCostUsd: number;
}

/**
 * Aggregated PTC metrics across all stages in a pipeline run
 */
export interface ProgrammaticToolMetrics {
  /** Total tool calls across all PTC stages */
  totalToolCalls: number;
  /** Total programmatic (PTC) calls */
  programmaticCalls: number;
  /** Total direct (headless CLI) calls */
  directCalls: number;
  /** Ratio of programmatic to total calls (0-1) */
  programmaticRatio: number;
  /** Total estimated tokens saved by using PTC */
  estimatedTokensSaved: number;
  /** Total code_execution blocks across all PTC stages */
  codeExecutionCount: number;
  /** Total container reuses across all PTC stages */
  containerReuseCount: number;
  /** Total PTC input tokens */
  totalInputTokens: number;
  /** Total PTC output tokens */
  totalOutputTokens: number;
  /** Total estimated PTC cost in USD */
  totalCostUsd: number;
  /** Per-stage breakdown */
  perStage: PTCStageUsage[];
}

/**
 * Aggregate per-stage PTC usage into summary metrics
 */
export function aggregatePTCMetrics(stages: PTCStageUsage[]): ProgrammaticToolMetrics {
  let programmaticCalls = 0;
  let directCalls = 0;
  let estimatedTokensSaved = 0;
  let codeExecutionCount = 0;
  let containerReuseCount = 0;
  let totalInputTokens = 0;
  let totalOutputTokens = 0;
  let totalCostUsd = 0;

  for (const stage of stages) {
    programmaticCalls += stage.programmaticCalls;
    directCalls += stage.directCalls;
    estimatedTokensSaved += stage.estimatedTokensSaved;
    codeExecutionCount += stage.codeExecutionCount;
    containerReuseCount += stage.containerReuseCount;
    totalInputTokens += stage.inputTokens;
    totalOutputTokens += stage.outputTokens;
    totalCostUsd += stage.estimatedCostUsd;
  }

  const totalToolCalls = programmaticCalls + directCalls;

  return {
    totalToolCalls,
    programmaticCalls,
    directCalls,
    programmaticRatio: totalToolCalls > 0 ? programmaticCalls / totalToolCalls : 0,
    estimatedTokensSaved,
    codeExecutionCount,
    containerReuseCount,
    totalInputTokens,
    totalOutputTokens,
    totalCostUsd,
    perStage: stages,
  };
}
