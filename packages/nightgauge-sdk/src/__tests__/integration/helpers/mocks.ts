/**
 * Mock factories for SDK integration tests
 *
 * Provides deterministic mocks for components that would require
 * external services (Claude API, GitHub API) in production.
 */

import type { SDKResultMessage } from "../../../tracking/TokenTracker.js";

/**
 * Build a mock SDKResultMessage representing a successful stage result.
 */
export function buildMockResultMessage(
  overrides: Partial<SDKResultMessage> = {}
): SDKResultMessage {
  return {
    type: "result",
    usage: {
      input_tokens: 1000,
      output_tokens: 500,
      cache_read_input_tokens: 0,
      cache_creation_input_tokens: 0,
    },
    total_cost_usd: 0.01,
    modelUsage: {
      "claude-sonnet-4-5": { inputTokens: 1000, outputTokens: 500 },
    },
    ...overrides,
  };
}

/**
 * Build a mock result with zero tokens (e.g., skipped or no-op stage).
 */
export function buildZeroUsageResult(): SDKResultMessage {
  return buildMockResultMessage({
    usage: {
      input_tokens: 0,
      output_tokens: 0,
      cache_read_input_tokens: 0,
      cache_creation_input_tokens: 0,
    },
    total_cost_usd: 0,
    modelUsage: undefined,
  });
}

/**
 * Build a mock result with large token counts (stress test).
 */
export function buildLargeUsageResult(): SDKResultMessage {
  return buildMockResultMessage({
    usage: {
      input_tokens: 100_000,
      output_tokens: 50_000,
      cache_read_input_tokens: 20_000,
      cache_creation_input_tokens: 10_000,
    },
    total_cost_usd: 5.0,
    modelUsage: {
      "claude-opus-4-6": { inputTokens: 100_000, outputTokens: 50_000 },
    },
  });
}
