/**
 * Mock implementations for @anthropic-ai/claude-agent-sdk
 *
 * Provides test doubles for SDK functions and types.
 */

import { vi } from "vitest";
import type { SDKMessage, SDKQueryFunction } from "../../src/orchestrator/StageExecutor.js";
import type { SDKResultMessage } from "../../src/tracking/TokenTracker.js";

/**
 * Create a mock SDK result message
 */
export function createMockResult(overrides?: Partial<SDKResultMessage>): SDKResultMessage {
  return {
    type: "result",
    usage: {
      input_tokens: 1000,
      output_tokens: 500,
      cache_read_input_tokens: 200,
      cache_creation_input_tokens: 100,
    },
    total_cost_usd: 0.05,
    modelUsage: {
      "claude-sonnet-4-6": {
        inputTokens: 1000,
        outputTokens: 500,
      },
    },
    ...overrides,
  };
}

/**
 * Create a mock init message
 */
export function createMockInit(sessionId: string = "test-session-123"): SDKMessage {
  return {
    type: "system",
    subtype: "init",
    session_id: sessionId,
  };
}

/**
 * Create a mock text message
 */
export function createMockText(text: string): SDKMessage {
  return {
    type: "assistant",
    subtype: "text",
    text,
  };
}

/**
 * Create a mock tool use message
 */
export function createMockToolUse(tool: string, input: unknown): SDKMessage {
  return {
    type: "assistant",
    subtype: "tool_use",
    tool,
    input,
  };
}

/**
 * Create a successful mock query function
 *
 * Returns an async generator that yields init, text, and result messages.
 */
export function createMockQuery(
  messages?: SDKMessage[],
  result?: SDKResultMessage
): SDKQueryFunction {
  return async function* mockQuery() {
    yield createMockInit();

    if (messages) {
      for (const message of messages) {
        yield message;
      }
    } else {
      yield createMockText("Processing request...");
      yield createMockToolUse("Read", { file_path: "test.md" });
      yield createMockText("Task completed successfully.");
    }

    yield result ?? createMockResult();
  };
}

/**
 * Create a failing mock query function
 */
export function createFailingQuery(error: Error): SDKQueryFunction {
  return async function* failingQuery() {
    yield createMockInit();
    throw error;
  };
}

/**
 * Create a mock query that never completes (for timeout testing)
 */
export function createHangingQuery(): SDKQueryFunction {
  return async function* hangingQuery() {
    yield createMockInit();
    await new Promise(() => {}); // Never resolves
  };
}

/**
 * Create a vi.fn() mock query with tracking
 */
export function createTrackedMockQuery(): SDKQueryFunction & ReturnType<typeof vi.fn> {
  const mockFn = vi.fn(createMockQuery());
  return mockFn as SDKQueryFunction & ReturnType<typeof vi.fn>;
}
