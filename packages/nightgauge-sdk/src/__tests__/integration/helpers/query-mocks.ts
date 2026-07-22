/**
 * SDK query function mocks for integration tests
 *
 * Provides deterministic, fast mock implementations of the SDKQueryFunction
 * used by PipelineOrchestrator. These avoid real Claude API calls while
 * exercising orchestration logic.
 */

import type {
  SDKQueryFunction,
  SDKMessage,
  SDKQueryOptions,
} from "../../../orchestrator/StageExecutor.js";

/**
 * Create a mock query function that immediately returns a successful result.
 * Returns the minimum messages needed to satisfy the orchestrator contract.
 */
export function createSuccessQueryFn(customMessages: SDKMessage[] = []): SDKQueryFunction {
  return async function* (_options: SDKQueryOptions) {
    // Yield any custom messages first
    for (const msg of customMessages) {
      yield msg;
    }
    // Always end with a result message
    yield {
      type: "result",
      subtype: "success",
      usage: {
        input_tokens: 500,
        output_tokens: 200,
        cache_read_input_tokens: 0,
        cache_creation_input_tokens: 0,
      },
      total_cost_usd: 0.005,
      modelUsage: {
        "claude-sonnet-4-5": { inputTokens: 500, outputTokens: 200 },
      },
      is_error: false,
      result: "Stage completed successfully",
      session_id: `mock-session-${Date.now()}`,
    } as SDKMessage;
  };
}

/**
 * Create a mock query function that yields a failure result.
 * Used to test error handling paths in the orchestrator.
 */
export function createFailureQueryFn(errorMessage: string = "Stage failed"): SDKQueryFunction {
  return async function* (_options: SDKQueryOptions) {
    yield {
      type: "result",
      subtype: "error_during_execution",
      is_error: true,
      result: errorMessage,
      usage: {
        input_tokens: 100,
        output_tokens: 50,
        cache_read_input_tokens: 0,
        cache_creation_input_tokens: 0,
      },
      total_cost_usd: 0.001,
      session_id: `mock-error-session-${Date.now()}`,
    } as SDKMessage;
  };
}

/**
 * Create a mock query function that emits token usage events through messages.
 */
export function createTokenEmittingQueryFn(): SDKQueryFunction {
  return async function* (_options: SDKQueryOptions) {
    yield {
      type: "result",
      subtype: "success",
      is_error: false,
      result: "Done",
      usage: {
        input_tokens: 2000,
        output_tokens: 1000,
        cache_read_input_tokens: 500,
        cache_creation_input_tokens: 100,
      },
      total_cost_usd: 0.025,
      modelUsage: {
        "claude-sonnet-4-5": { inputTokens: 2000, outputTokens: 1000 },
      },
      session_id: `mock-token-session-${Date.now()}`,
    } as SDKMessage;
  };
}
