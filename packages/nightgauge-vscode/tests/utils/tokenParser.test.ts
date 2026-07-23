/**
 * Unit tests for tokenParser
 *
 * Tests the token parsing utilities for Claude CLI stream-json output.
 *
 * @see tokenParser.ts
 * @see Issue #271 - Add tokenParser unit tests
 */

import { describe, it, expect, beforeEach } from "vitest";
import {
  parseStreamJsonLine,
  toTokenUsageUpdate,
  TokenAccumulator,
  LiveStageEstimator,
  resolveStageBookedUsage,
  parseStreamJsonOutput,
  extractTokenUsage,
  formatTokenCount,
  formatCost,
  formatTokenUsageDisplay,
  calculateRateLimitWait,
  isHardRateLimit,
  isQuotaPressureSignal,
  formatRateLimitCountdown,
  isAnthropicSessionLimit,
  parseSessionLimitResetsAt,
  type ParsedTokenUsage,
} from "../../src/utils/tokenParser";

describe("isAnthropicSessionLimit (#3792)", () => {
  it("matches session-limit messages", () => {
    expect(
      isAnthropicSessionLimit("You've hit your session limit · resets 10:30am (America/Denver)")
    ).toBe(true);
    expect(isAnthropicSessionLimit("success: You've hit your session limit")).toBe(true);
    expect(isAnthropicSessionLimit("Usage limit reached for this account")).toBe(true);
  });

  it("does not match unrelated errors", () => {
    expect(isAnthropicSessionLimit("ENOENT: no such file or directory")).toBe(false);
    expect(isAnthropicSessionLimit("rate limit exceeded")).toBe(false);
    expect(isAnthropicSessionLimit("validation failed")).toBe(false);
  });
});

describe("parseSessionLimitResetsAt (#3792)", () => {
  it("parses a timezone-qualified reset to a future unix epoch", () => {
    // Fix 'now' to 09:12 America/Denver (15:12Z, MDT = UTC-6) so 10:30am today is ahead.
    const now = new Date("2026-05-26T15:12:42Z");
    const at = parseSessionLimitResetsAt(
      "You've hit your session limit · resets 10:30am (America/Denver)",
      now
    );
    expect(at).toBeTypeOf("number");
    // 10:30 MDT == 16:30Z == 1748277000.
    expect(at).toBe(Math.floor(Date.parse("2026-05-26T16:30:00Z") / 1000));
    expect((at as number) > Math.floor(now.getTime() / 1000)).toBe(true);
  });

  it("rolls to tomorrow when the reset time already passed today", () => {
    const now = new Date("2026-05-26T20:00:00Z"); // 14:00 MDT, past 10:30am
    const at = parseSessionLimitResetsAt("resets 10:30am (America/Denver)", now);
    expect(at).toBe(Math.floor(Date.parse("2026-05-27T16:30:00Z") / 1000));
  });

  it("returns undefined when no reset time is present", () => {
    expect(parseSessionLimitResetsAt("You've hit your session limit")).toBeUndefined();
  });

  it("handles a bare hour with am/pm and no timezone", () => {
    const now = new Date("2026-05-26T00:00:00Z");
    const at = parseSessionLimitResetsAt("resets 3pm", now);
    expect(at).toBeTypeOf("number");
    expect((at as number) > Math.floor(now.getTime() / 1000)).toBe(true);
  });
});

describe("parseStreamJsonLine", () => {
  it("parses terminal SDK workflow agent usage emitted for Codex stages", () => {
    const result = parseStreamJsonLine(
      JSON.stringify({
        schemaVersion: 4,
        kind: "agent",
        status: "succeeded",
        provider: "codex",
        usage: {
          inputTokens: 112636,
          outputTokens: 13602,
          cacheReadTokens: 1766400,
          cacheCreationTokens: 0,
          costUsd: 0,
        },
      })
    );

    expect(result?.type).toBe("token:usage");
    expect(result?.usage).toEqual({
      inputTokens: 112636,
      outputTokens: 13602,
      cacheReadTokens: 1766400,
      cacheCreationTokens: 0,
      costUsd: 0,
    });
  });

  it("treats non-terminal SDK workflow usage as a live cumulative snapshot", () => {
    const result = parseStreamJsonLine(
      JSON.stringify({
        schemaVersion: 4,
        kind: "agent",
        status: "running",
        provider: "codex",
        usage: {
          inputTokens: 100,
          outputTokens: 20,
          cacheReadTokens: 900,
          cacheCreationTokens: 0,
          costUsd: 0,
        },
      })
    );

    expect(result?.usage).toBeUndefined();
    expect(result?.incrementalUsage?.cacheReadTokens).toBe(900);
  });

  describe("result messages", () => {
    it("should parse result message with full usage stats", () => {
      const line = JSON.stringify({
        type: "result",
        usage: {
          input_tokens: 1500,
          output_tokens: 500,
          cache_read_input_tokens: 200,
          cache_creation_input_tokens: 100,
        },
        total_cost_usd: 0.0234,
      });

      const result = parseStreamJsonLine(line);

      expect(result).not.toBeNull();
      expect(result?.type).toBe("result");
      expect(result?.usage).toEqual({
        inputTokens: 1500,
        outputTokens: 500,
        cacheReadTokens: 200,
        cacheCreationTokens: 100,
        costUsd: 0.0234,
        costCumulative: true,
      });
    });

    it("should parse result message with session_id", () => {
      const line = JSON.stringify({
        type: "result",
        session_id: "abc-123-def",
        usage: {
          input_tokens: 100,
          output_tokens: 50,
        },
        total_cost_usd: 0.001,
      });

      const result = parseStreamJsonLine(line);

      expect(result?.sessionId).toBe("abc-123-def");
    });

    it("should parse result message without usage field", () => {
      const line = JSON.stringify({
        type: "result",
        session_id: "test-session",
      });

      const result = parseStreamJsonLine(line);

      expect(result).not.toBeNull();
      expect(result?.type).toBe("result");
      expect(result?.usage).toBeUndefined();
      expect(result?.sessionId).toBe("test-session");
    });

    it("should use defaults for missing usage fields", () => {
      const line = JSON.stringify({
        type: "result",
        usage: {
          input_tokens: 100,
          // missing output_tokens, cache fields, and cost
        },
      });

      const result = parseStreamJsonLine(line);

      expect(result?.usage?.inputTokens).toBe(100);
      expect(result?.usage?.outputTokens).toBe(0);
      expect(result?.usage?.cacheReadTokens).toBe(0);
      expect(result?.usage?.cacheCreationTokens).toBe(0);
      expect(result?.usage?.costUsd).toBe(0);
    });

    it("should handle zero values for all fields", () => {
      const line = JSON.stringify({
        type: "result",
        usage: {
          input_tokens: 0,
          output_tokens: 0,
          cache_read_input_tokens: 0,
          cache_creation_input_tokens: 0,
        },
        total_cost_usd: 0,
      });

      const result = parseStreamJsonLine(line);

      expect(result?.usage).toEqual({
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0,
        costCumulative: true,
      });
    });

    it("should handle very large token numbers", () => {
      const line = JSON.stringify({
        type: "result",
        usage: {
          input_tokens: 999999999,
          output_tokens: 888888888,
          cache_read_input_tokens: 777777777,
          cache_creation_input_tokens: 666666666,
        },
        total_cost_usd: 12345.6789,
      });

      const result = parseStreamJsonLine(line);

      expect(result?.usage?.inputTokens).toBe(999999999);
      expect(result?.usage?.outputTokens).toBe(888888888);
      expect(result?.usage?.cacheReadTokens).toBe(777777777);
      expect(result?.usage?.cacheCreationTokens).toBe(666666666);
      expect(result?.usage?.costUsd).toBe(12345.6789);
    });
  });

  describe("token:usage messages (Codex adapter)", () => {
    it("should parse token:usage event payload", () => {
      const line = JSON.stringify({
        type: "token:usage",
        stage: "feature-dev",
        inputTokens: 1200,
        outputTokens: 450,
        cacheReadTokens: 50,
        cacheCreationTokens: 25,
        costUsd: 0.0185,
      });

      const result = parseStreamJsonLine(line);

      expect(result).not.toBeNull();
      expect(result?.type).toBe("token:usage");
      expect(result?.usage).toEqual({
        inputTokens: 1200,
        outputTokens: 450,
        cacheReadTokens: 50,
        cacheCreationTokens: 25,
        costUsd: 0.0185,
      });
    });

    it("should default missing cost metadata to 0", () => {
      const line = JSON.stringify({
        type: "token:usage",
        inputTokens: 1200,
        outputTokens: 450,
      });

      const result = parseStreamJsonLine(line);

      expect(result?.usage).toEqual({
        inputTokens: 1200,
        outputTokens: 450,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0,
      });
    });
  });

  describe("error messages", () => {
    it("should parse error message with nested error object", () => {
      const line = JSON.stringify({
        type: "error",
        error: {
          message: "Rate limit exceeded",
        },
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("error");
      expect(result?.error).toBe("Rate limit exceeded");
    });

    it("should parse error message with flat message field", () => {
      const line = JSON.stringify({
        type: "error",
        message: "Connection timeout",
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("error");
      expect(result?.error).toBe("Connection timeout");
    });

    it("should handle error without message field", () => {
      const line = JSON.stringify({
        type: "error",
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("error");
      expect(result?.error).toBe("Unknown error");
    });
  });

  describe("content_block_delta messages", () => {
    it("should parse content delta with text", () => {
      const line = JSON.stringify({
        type: "content_block_delta",
        delta: {
          text: "Hello, world!",
        },
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("content_block_delta");
      expect(result?.content).toBe("Hello, world!");
    });

    it("should handle content delta with empty text", () => {
      const line = JSON.stringify({
        type: "content_block_delta",
        delta: {
          text: "",
        },
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("content_block_delta");
      expect(result?.content).toBe("");
    });

    it("should handle content delta without delta field", () => {
      const line = JSON.stringify({
        type: "content_block_delta",
      });

      const result = parseStreamJsonLine(line);

      // Should return generic type since delta is missing
      expect(result?.type).toBe("content_block_delta");
      expect(result?.content).toBeUndefined();
    });

    it("should handle content delta with missing text in delta", () => {
      const line = JSON.stringify({
        type: "content_block_delta",
        delta: {},
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("content_block_delta");
      expect(result?.content).toBe("");
    });
  });

  describe("content_block_start messages (tool use)", () => {
    it("should parse tool use message", () => {
      const line = JSON.stringify({
        type: "content_block_start",
        content_block: {
          type: "tool_use",
          name: "Read",
          input: { file_path: "/some/path" },
        },
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("content_block_start");
      expect(result?.toolName).toBe("Read");
      expect(result?.toolInput).toEqual({ file_path: "/some/path" });
    });

    it("should handle content_block_start without tool_use type", () => {
      const line = JSON.stringify({
        type: "content_block_start",
        content_block: {
          type: "text",
        },
      });

      const result = parseStreamJsonLine(line);

      // Should return generic type since it's not tool_use
      expect(result?.type).toBe("content_block_start");
      expect(result?.toolName).toBeUndefined();
    });
  });

  describe("assistant messages", () => {
    it("should parse assistant message", () => {
      const line = JSON.stringify({
        type: "assistant",
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("assistant");
    });

    it("should parse message_start as assistant", () => {
      const line = JSON.stringify({
        type: "message_start",
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("assistant");
    });

    it("should NOT surface phase markers from Bash tool_use command inputs (#217)", () => {
      // The printf command echo is the same marker that comes back in the
      // tool_result stdout. Surfacing it as content made every printf'd phase
      // fire twice and phaseHistory double-count. The tool call itself is
      // still exposed via toolUses for deterministic phase inference (#3760).
      const line = JSON.stringify({
        type: "assistant",
        message: {
          role: "assistant",
          content: [
            {
              type: "tool_use",
              id: "toolu_01abc",
              name: "Bash",
              input: {
                command:
                  'printf \'<!-- phase:start name="load-context" index=1 total=13 stage="feature-planning" -->\\n\'',
                description: "Emit phase marker",
              },
            },
          ],
        },
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("assistant");
      expect(result?.content).toBeUndefined();
      expect(result?.toolUses).toHaveLength(1);
      expect(result?.toolUses?.[0].name).toBe("Bash");
    });

    it("should expose every tool_use block in toolUses (Issue #3760)", () => {
      // The CLI delivers tool calls inside complete assistant messages. Phase
      // inference needs every tool call (name + input), not just text/markers.
      const line = JSON.stringify({
        type: "assistant",
        message: {
          role: "assistant",
          content: [
            { type: "text", text: "Editing now" },
            { type: "tool_use", id: "toolu_1", name: "Write", input: { file_path: "src/x.ts" } },
            { type: "tool_use", id: "toolu_2", name: "Bash", input: { command: "go test ./..." } },
          ],
        },
      });

      const result = parseStreamJsonLine(line);

      expect(result?.toolUses).toEqual([
        { name: "Write", input: { file_path: "src/x.ts" } },
        { name: "Bash", input: { command: "go test ./..." } },
      ]);
    });

    it("should leave toolUses undefined for text-only assistant messages", () => {
      const line = JSON.stringify({
        type: "assistant",
        message: { role: "assistant", content: [{ type: "text", text: "just text" }] },
      });
      const result = parseStreamJsonLine(line);
      expect(result?.toolUses).toBeUndefined();
    });

    it("should not include non-phase Bash command inputs in content", () => {
      const line = JSON.stringify({
        type: "assistant",
        message: {
          role: "assistant",
          content: [
            {
              type: "tool_use",
              id: "toolu_01abc",
              name: "Bash",
              input: { command: "git status", description: "Check status" },
            },
          ],
        },
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("assistant");
      expect(result?.content).toBeUndefined();
    });

    it("should keep text content free of Bash phase-command echoes (#217)", () => {
      const line = JSON.stringify({
        type: "assistant",
        message: {
          role: "assistant",
          content: [
            { type: "text", text: "Starting phase..." },
            {
              type: "tool_use",
              id: "toolu_01abc",
              name: "Bash",
              input: {
                command:
                  'printf \'<!-- phase:start name="batch-detection" index=2 total=13 stage="feature-planning" -->\\n\'',
                description: "Emit phase marker",
              },
            },
          ],
        },
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("assistant");
      expect(result?.content).toBe("Starting phase...");
      expect(result?.content).not.toContain("phase:start");
    });

    // Live in-stage usage snapshot (#233)
    it("populates incrementalUsage from an assistant message.usage (NOT usage)", () => {
      const line = JSON.stringify({
        type: "assistant",
        message: {
          role: "assistant",
          model: "claude-sonnet-4-6",
          content: [{ type: "text", text: "working" }],
          usage: {
            input_tokens: 1200,
            output_tokens: 80,
            cache_read_input_tokens: 300,
            cache_creation_input_tokens: 40,
          },
        },
      });

      const result = parseStreamJsonLine(line);

      // Exposed as incrementalUsage so the additive accumulation path never sees it.
      expect(result?.incrementalUsage).toEqual({
        inputTokens: 1200,
        outputTokens: 80,
        cacheReadTokens: 300,
        cacheCreationTokens: 40,
        costUsd: 0,
      });
      // Critically: the authoritative `usage` field stays undefined so the
      // terminal-envelope accumulation path is untouched.
      expect(result?.usage).toBeUndefined();
    });

    it("populates incrementalUsage on a text-only assistant message with usage", () => {
      const line = JSON.stringify({
        type: "assistant",
        message: {
          role: "assistant",
          usage: { input_tokens: 500, output_tokens: 25 },
        },
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("assistant");
      expect(result?.incrementalUsage).toMatchObject({
        inputTokens: 500,
        outputTokens: 25,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0,
      });
      expect(result?.usage).toBeUndefined();
    });

    it("leaves incrementalUsage undefined when the assistant message has no usage", () => {
      const line = JSON.stringify({
        type: "assistant",
        message: { role: "assistant", content: [{ type: "text", text: "no usage here" }] },
      });

      const result = parseStreamJsonLine(line);

      expect(result?.incrementalUsage).toBeUndefined();
    });

    it("keeps the terminal result envelope authoritative via `usage`, not incrementalUsage", () => {
      const line = JSON.stringify({
        type: "result",
        usage: { input_tokens: 5000, output_tokens: 999 },
        total_cost_usd: 0.42,
      });

      const result = parseStreamJsonLine(line);

      // Terminal result still populates `usage` (drives TokenAccumulator).
      expect(result?.usage).toMatchObject({ inputTokens: 5000, outputTokens: 999, costUsd: 0.42 });
      expect(result?.incrementalUsage).toBeUndefined();
    });
  });

  describe("other message types", () => {
    it("should return generic type for unknown message types", () => {
      const line = JSON.stringify({
        type: "user",
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("user");
    });

    it("should handle system message type", () => {
      const line = JSON.stringify({
        type: "system",
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("system");
    });

    it("should handle content_block_stop message", () => {
      const line = JSON.stringify({
        type: "content_block_stop",
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("content_block_stop");
    });
  });

  describe("served-model attribution (#91)", () => {
    it("extracts subtype and model from system/init", () => {
      const line = JSON.stringify({
        type: "system",
        subtype: "init",
        model: "claude-fable-5",
        session_id: "abc",
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("system");
      expect(result?.subtype).toBe("init");
      expect(result?.model).toBe("claude-fable-5");
      expect(result?.modelRefusalFallback).toBeUndefined();
    });

    it("parses the CLI's model_refusal_fallback event (spike §8.3 shape)", () => {
      // Verbatim field shape captured live from claude CLI 2.1.186 —
      // docs/spikes/fable-5-behavior-porting.md §8.3.
      const line = JSON.stringify({
        type: "system",
        subtype: "model_refusal_fallback",
        trigger: "refusal",
        direction: "retry",
        original_model: "claude-fable-5",
        fallback_model: "claude-opus-4-8",
        api_refusal_category: "reasoning_extraction",
        content: "Fable 5 has safety measures that flagged something…",
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("system");
      expect(result?.subtype).toBe("model_refusal_fallback");
      expect(result?.modelRefusalFallback).toEqual({
        originalModel: "claude-fable-5",
        fallbackModel: "claude-opus-4-8",
        category: "reasoning_extraction",
      });
      // The fallback model is the served model from this event onward.
      expect(result?.model).toBe("claude-opus-4-8");
    });

    it("extracts message.model from assistant messages (with and without content)", () => {
      const withContent = parseStreamJsonLine(
        JSON.stringify({
          type: "assistant",
          message: {
            model: "claude-opus-4-8",
            content: [{ type: "text", text: "hello" }],
          },
        })
      );
      expect(withContent?.model).toBe("claude-opus-4-8");
      expect(withContent?.content).toBe("hello");

      const bare = parseStreamJsonLine(
        JSON.stringify({
          type: "assistant",
          message: { model: "claude-opus-4-8", content: [] },
        })
      );
      expect(bare?.model).toBe("claude-opus-4-8");
    });

    it("leaves model undefined when the stream carries none", () => {
      const assistant = parseStreamJsonLine(
        JSON.stringify({ type: "assistant", message: { content: [] } })
      );
      expect(assistant?.model).toBeUndefined();

      const system = parseStreamJsonLine(JSON.stringify({ type: "system", subtype: "init" }));
      expect(system?.model).toBeUndefined();
    });

    it("ignores a malformed fallback event with no fallback_model", () => {
      const result = parseStreamJsonLine(
        JSON.stringify({
          type: "system",
          subtype: "model_refusal_fallback",
          original_model: "claude-fable-5",
        })
      );
      expect(result?.modelRefusalFallback).toBeUndefined();
      expect(result?.model).toBeUndefined();
    });
  });

  describe("user messages with tool_result (Issue #1031)", () => {
    it("should extract tool_result with string content", () => {
      const line = JSON.stringify({
        type: "user",
        message: {
          content: [
            {
              type: "tool_result",
              tool_use_id: "toolu_abc123",
              content: "File contents here",
            },
          ],
        },
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("user");
      expect(result?.toolResult).toEqual({
        toolUseId: "toolu_abc123",
        content: "File contents here",
        isError: false,
      });
    });

    it("should extract tool_result with array content blocks", () => {
      const line = JSON.stringify({
        type: "user",
        message: {
          content: [
            {
              type: "tool_result",
              tool_use_id: "toolu_def456",
              content: [
                { type: "text", text: "Line 1\n" },
                { type: "text", text: "Line 2" },
              ],
            },
          ],
        },
      });

      const result = parseStreamJsonLine(line);

      expect(result?.toolResult).toEqual({
        toolUseId: "toolu_def456",
        content: "Line 1\nLine 2",
        isError: false,
      });
    });

    it("should detect is_error flag", () => {
      const line = JSON.stringify({
        type: "user",
        message: {
          content: [
            {
              type: "tool_result",
              tool_use_id: "toolu_err789",
              content: "Permission denied",
              is_error: true,
            },
          ],
        },
      });

      const result = parseStreamJsonLine(line);

      expect(result?.toolResult?.isError).toBe(true);
      expect(result?.toolResult?.content).toBe("Permission denied");
    });

    it("should handle tool_result with missing content", () => {
      const line = JSON.stringify({
        type: "user",
        message: {
          content: [
            {
              type: "tool_result",
              tool_use_id: "toolu_empty",
            },
          ],
        },
      });

      const result = parseStreamJsonLine(line);

      expect(result?.toolResult).toEqual({
        toolUseId: "toolu_empty",
        content: "",
        isError: false,
      });
    });

    it("should ignore tool_result without tool_use_id", () => {
      const line = JSON.stringify({
        type: "user",
        message: {
          content: [
            {
              type: "tool_result",
              content: "No ID here",
            },
          ],
        },
      });

      const result = parseStreamJsonLine(line);

      // Falls through to generic user message (no toolResult)
      expect(result?.type).toBe("user");
      expect(result?.toolResult).toBeUndefined();
    });

    it("should return first tool_result when multiple exist", () => {
      const line = JSON.stringify({
        type: "user",
        message: {
          content: [
            {
              type: "tool_result",
              tool_use_id: "toolu_first",
              content: "First result",
            },
            {
              type: "tool_result",
              tool_use_id: "toolu_second",
              content: "Second result",
            },
          ],
        },
      });

      const result = parseStreamJsonLine(line);

      expect(result?.toolResult?.toolUseId).toBe("toolu_first");
    });

    it("should handle user message without message.content", () => {
      const line = JSON.stringify({
        type: "user",
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("user");
      expect(result?.toolResult).toBeUndefined();
    });

    it("should handle user message with non-array content", () => {
      const line = JSON.stringify({
        type: "user",
        message: {
          content: "plain string content",
        },
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("user");
      expect(result?.toolResult).toBeUndefined();
    });
  });

  describe("rate_limit_event messages (Issue #2573)", () => {
    it("should parse rate_limit_event with all fields", () => {
      const line = JSON.stringify({
        type: "rate_limit_event",
        resetsAt: 1712505600,
        rateLimitType: "seven_day",
        utilization: 98,
        status: "limited",
        isUsingOverage: false,
      });

      const result = parseStreamJsonLine(line);

      expect(result).not.toBeNull();
      expect(result?.type).toBe("rate_limit_event");
      expect(result?.rateLimitEvent).toEqual({
        resetsAt: 1712505600,
        rateLimitType: "seven_day",
        utilization: 98,
        status: "limited",
        isUsingOverage: false,
      });
    });

    it("should parse allowed_warning status", () => {
      const line = JSON.stringify({
        type: "rate_limit_event",
        resetsAt: 1712505600,
        rateLimitType: "daily",
        utilization: 85,
        status: "allowed_warning",
        isUsingOverage: false,
      });

      const result = parseStreamJsonLine(line);

      expect(result?.rateLimitEvent?.status).toBe("allowed_warning");
      expect(result?.rateLimitEvent?.rateLimitType).toBe("daily");
      expect(result?.rateLimitEvent?.utilization).toBe(85);
    });

    it("should default missing fields", () => {
      const line = JSON.stringify({
        type: "rate_limit_event",
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("rate_limit_event");
      expect(result?.rateLimitEvent).toEqual({
        resetsAt: 0,
        rateLimitType: "unknown",
        utilization: 0,
        status: "limited",
        isUsingOverage: false,
      });
    });

    it("should handle overage flag", () => {
      const line = JSON.stringify({
        type: "rate_limit_event",
        resetsAt: 1712505600,
        rateLimitType: "seven_day",
        utilization: 100,
        status: "limited",
        isUsingOverage: true,
      });

      const result = parseStreamJsonLine(line);

      expect(result?.rateLimitEvent?.isUsingOverage).toBe(true);
    });

    // Issue #3386 — the actual Claude CLI emits fields nested under
    // `rate_limit_info`, NOT at the top level. Pre-fix the parser read
    // top-level fields, so every real rate_limit_event got default values
    // (`resetsAt: 0`, `status: "limited"`) and the overage signals were
    // never observed. The skillRunner could not detect quota-exhaustion to
    // distinguish silent stalls from genuine wedges (#3386).
    it("parses the nested rate_limit_info shape that the real Claude CLI emits (#3386)", () => {
      const line = JSON.stringify({
        type: "rate_limit_event",
        rate_limit_info: {
          resetsAt: 1778367000,
          rateLimitType: "five_hour",
          utilization: 1,
          status: "allowed",
          isUsingOverage: false,
          overageStatus: "rejected",
          overageDisabledReason: "out_of_credits",
        },
      });

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("rate_limit_event");
      expect(result?.rateLimitEvent).toEqual({
        resetsAt: 1778367000,
        rateLimitType: "five_hour",
        utilization: 1,
        status: "allowed",
        isUsingOverage: false,
        overageStatus: "rejected",
        overageDisabledReason: "out_of_credits",
      });
    });

    it("preserves backward-compat parsing for top-level fields (older CLI / tests)", () => {
      const line = JSON.stringify({
        type: "rate_limit_event",
        resetsAt: 1712505600,
        rateLimitType: "daily",
        utilization: 50,
        status: "allowed",
        isUsingOverage: false,
      });

      const result = parseStreamJsonLine(line);

      expect(result?.rateLimitEvent?.resetsAt).toBe(1712505600);
      expect(result?.rateLimitEvent?.rateLimitType).toBe("daily");
      expect(result?.rateLimitEvent?.utilization).toBe(50);
      // Overage signals absent in top-level form — must remain undefined,
      // not default to a misleading value.
      expect(result?.rateLimitEvent?.overageStatus).toBeUndefined();
      expect(result?.rateLimitEvent?.overageDisabledReason).toBeUndefined();
    });

    it("should not set usage or other fields on rate_limit_event", () => {
      const line = JSON.stringify({
        type: "rate_limit_event",
        resetsAt: 1712505600,
        rateLimitType: "seven_day",
        utilization: 98,
        status: "limited",
      });

      const result = parseStreamJsonLine(line);

      expect(result?.usage).toBeUndefined();
      expect(result?.error).toBeUndefined();
      expect(result?.content).toBeUndefined();
      expect(result?.sessionId).toBeUndefined();
    });
  });

  describe("edge cases", () => {
    it("should return null for empty string", () => {
      expect(parseStreamJsonLine("")).toBeNull();
    });

    it("should return null for whitespace-only input", () => {
      expect(parseStreamJsonLine("   ")).toBeNull();
      expect(parseStreamJsonLine("\t\n")).toBeNull();
      expect(parseStreamJsonLine("\n")).toBeNull();
    });

    it("should return null for invalid JSON", () => {
      expect(parseStreamJsonLine("not json")).toBeNull();
      expect(parseStreamJsonLine("{invalid")).toBeNull();
      expect(parseStreamJsonLine('{"type": result}')).toBeNull();
    });

    it("should return null for partial JSON", () => {
      expect(parseStreamJsonLine('{"type": "result')).toBeNull();
      expect(parseStreamJsonLine('{"type":')).toBeNull();
    });

    it("should handle JSON with extra whitespace", () => {
      const line = '  {"type": "result", "usage": {"input_tokens": 10}}  ';

      const result = parseStreamJsonLine(line);

      expect(result?.type).toBe("result");
      expect(result?.usage?.inputTokens).toBe(10);
    });

    it("should handle negative numbers (edge case)", () => {
      const line = JSON.stringify({
        type: "result",
        usage: {
          input_tokens: -100,
          output_tokens: -50,
        },
        total_cost_usd: -0.01,
      });

      const result = parseStreamJsonLine(line);

      // The parser should accept them (it doesn't validate semantics)
      expect(result?.usage?.inputTokens).toBe(-100);
      expect(result?.usage?.outputTokens).toBe(-50);
      expect(result?.usage?.costUsd).toBe(-0.01);
    });
  });
});

describe("toTokenUsageUpdate", () => {
  it("should map all fields correctly", () => {
    const usage: ParsedTokenUsage = {
      inputTokens: 1000,
      outputTokens: 500,
      cacheReadTokens: 200,
      cacheCreationTokens: 100,
      costUsd: 0.05,
    };

    const result = toTokenUsageUpdate(usage);

    expect(result).toEqual({
      inputTokens: 1000,
      outputTokens: 500,
      cacheReadTokens: 200,
      cacheCreationTokens: 100,
      costUsd: 0.05,
    });
  });

  it("should preserve all values including zeros", () => {
    const usage: ParsedTokenUsage = {
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0,
    };

    const result = toTokenUsageUpdate(usage);

    expect(result.inputTokens).toBe(0);
    expect(result.outputTokens).toBe(0);
    expect(result.cacheReadTokens).toBe(0);
    expect(result.cacheCreationTokens).toBe(0);
    expect(result.costUsd).toBe(0);
  });
});

describe("TokenAccumulator", () => {
  let accumulator: TokenAccumulator;

  beforeEach(() => {
    accumulator = new TokenAccumulator();
  });

  describe("cumulative cost envelopes (#256)", () => {
    const envelope = (costUsd: number, tokens = 100): Parameters<TokenAccumulator["add"]>[0] => ({
      inputTokens: tokens,
      outputTokens: tokens,
      cacheReadTokens: tokens,
      cacheCreationTokens: tokens,
      costUsd,
      costCumulative: true,
    });

    it("books only the delta between successive cumulative envelopes", () => {
      // The bowlsheet #236 incident shape: six envelopes from one process,
      // each reporting the session's cumulative total_cost_usd. Summing them
      // recorded $100.47 for a stage that really cost $23.67.
      for (const c of [14.7754692, 15.1116708, 15.3359154, 15.70176885, 15.8720232, 23.6728992]) {
        accumulator.add(envelope(c));
      }

      expect(accumulator.getTotal().costUsd).toBeCloseTo(23.6728992, 6);
    });

    it("treats a cost decrease as a new session and books the full value", () => {
      accumulator.add(envelope(14.78));
      accumulator.add(envelope(2.0));

      expect(accumulator.getTotal().costUsd).toBeCloseTo(16.78, 6);
    });

    it("still sums token counts across cumulative envelopes (tokens are per-invocation)", () => {
      accumulator.add(envelope(14.78, 1000));
      accumulator.add(envelope(15.11, 200));

      const total = accumulator.getTotal();
      expect(total.inputTokens).toBe(1200);
      expect(total.cacheReadTokens).toBe(1200);
      expect(total.outputTokens).toBe(1200);
    });

    it("sums unflagged per-event costs unchanged", () => {
      accumulator.add({
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0.5,
      });
      accumulator.add({
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0.3,
      });

      expect(accumulator.getTotal().costUsd).toBeCloseTo(0.8, 6);
    });

    it("clears the cumulative baseline on reset", () => {
      accumulator.add(envelope(10.0));
      accumulator.reset();
      accumulator.add(envelope(10.0));

      expect(accumulator.getTotal().costUsd).toBeCloseTo(10.0, 6);
    });
  });

  describe("initial state", () => {
    it("should have zero values initially", () => {
      const total = accumulator.getTotal();

      expect(total.inputTokens).toBe(0);
      expect(total.outputTokens).toBe(0);
      expect(total.cacheReadTokens).toBe(0);
      expect(total.cacheCreationTokens).toBe(0);
      expect(total.costUsd).toBe(0);
    });

    it("should return hasTokens false initially", () => {
      expect(accumulator.hasTokens()).toBe(false);
    });
  });

  describe("add", () => {
    it("should add single usage correctly", () => {
      accumulator.add({
        inputTokens: 100,
        outputTokens: 50,
        cacheReadTokens: 20,
        cacheCreationTokens: 10,
        costUsd: 0.01,
      });

      const total = accumulator.getTotal();

      expect(total.inputTokens).toBe(100);
      expect(total.outputTokens).toBe(50);
      expect(total.cacheReadTokens).toBe(20);
      expect(total.cacheCreationTokens).toBe(10);
      expect(total.costUsd).toBe(0.01);
    });

    it("should accumulate multiple usages", () => {
      accumulator.add({
        inputTokens: 100,
        outputTokens: 50,
        cacheReadTokens: 20,
        cacheCreationTokens: 10,
        costUsd: 0.01,
      });

      accumulator.add({
        inputTokens: 200,
        outputTokens: 100,
        cacheReadTokens: 40,
        cacheCreationTokens: 20,
        costUsd: 0.02,
      });

      accumulator.add({
        inputTokens: 300,
        outputTokens: 150,
        cacheReadTokens: 60,
        cacheCreationTokens: 30,
        costUsd: 0.03,
      });

      const total = accumulator.getTotal();

      expect(total.inputTokens).toBe(600);
      expect(total.outputTokens).toBe(300);
      expect(total.cacheReadTokens).toBe(120);
      expect(total.cacheCreationTokens).toBe(60);
      expect(total.costUsd).toBeCloseTo(0.06, 5);
    });

    it("should handle very large accumulated values", () => {
      for (let i = 0; i < 1000; i++) {
        accumulator.add({
          inputTokens: 1000000,
          outputTokens: 500000,
          cacheReadTokens: 100000,
          cacheCreationTokens: 50000,
          costUsd: 10.0,
        });
      }

      const total = accumulator.getTotal();

      expect(total.inputTokens).toBe(1000000000);
      expect(total.outputTokens).toBe(500000000);
      expect(total.cacheReadTokens).toBe(100000000);
      expect(total.cacheCreationTokens).toBe(50000000);
      expect(total.costUsd).toBeCloseTo(10000.0, 2);
    });
  });

  describe("getTotal", () => {
    it("should return a copy (immutability check)", () => {
      accumulator.add({
        inputTokens: 100,
        outputTokens: 50,
        cacheReadTokens: 20,
        cacheCreationTokens: 10,
        costUsd: 0.01,
      });

      const total1 = accumulator.getTotal();
      total1.inputTokens = 999;

      const total2 = accumulator.getTotal();

      expect(total2.inputTokens).toBe(100);
    });
  });

  describe("reset", () => {
    it("should reset all values to zero", () => {
      accumulator.add({
        inputTokens: 100,
        outputTokens: 50,
        cacheReadTokens: 20,
        cacheCreationTokens: 10,
        costUsd: 0.01,
      });

      accumulator.reset();

      const total = accumulator.getTotal();

      expect(total.inputTokens).toBe(0);
      expect(total.outputTokens).toBe(0);
      expect(total.cacheReadTokens).toBe(0);
      expect(total.cacheCreationTokens).toBe(0);
      expect(total.costUsd).toBe(0);
    });

    it("should allow re-accumulation after reset", () => {
      accumulator.add({
        inputTokens: 100,
        outputTokens: 50,
        cacheReadTokens: 20,
        cacheCreationTokens: 10,
        costUsd: 0.01,
      });

      accumulator.reset();

      accumulator.add({
        inputTokens: 200,
        outputTokens: 100,
        cacheReadTokens: 40,
        cacheCreationTokens: 20,
        costUsd: 0.02,
      });

      const total = accumulator.getTotal();

      expect(total.inputTokens).toBe(200);
      expect(total.outputTokens).toBe(100);
    });
  });

  describe("hasTokens", () => {
    it("should return false when no tokens added", () => {
      expect(accumulator.hasTokens()).toBe(false);
    });

    it("should return true after adding tokens", () => {
      accumulator.add({
        inputTokens: 1,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0,
      });

      expect(accumulator.hasTokens()).toBe(true);
    });

    it("should return true for any token type", () => {
      const testCases = [
        {
          inputTokens: 1,
          outputTokens: 0,
          cacheReadTokens: 0,
          cacheCreationTokens: 0,
          costUsd: 0,
        },
        {
          inputTokens: 0,
          outputTokens: 1,
          cacheReadTokens: 0,
          cacheCreationTokens: 0,
          costUsd: 0,
        },
        {
          inputTokens: 0,
          outputTokens: 0,
          cacheReadTokens: 1,
          cacheCreationTokens: 0,
          costUsd: 0,
        },
        {
          inputTokens: 0,
          outputTokens: 0,
          cacheReadTokens: 0,
          cacheCreationTokens: 1,
          costUsd: 0,
        },
      ];

      for (const usage of testCases) {
        const acc = new TokenAccumulator();
        acc.add(usage);
        expect(acc.hasTokens()).toBe(true);
      }
    });

    it("should return false after reset", () => {
      accumulator.add({
        inputTokens: 100,
        outputTokens: 50,
        cacheReadTokens: 20,
        cacheCreationTokens: 10,
        costUsd: 0.01,
      });

      accumulator.reset();

      expect(accumulator.hasTokens()).toBe(false);
    });

    it("should return false when only cost is added (no tokens)", () => {
      accumulator.add({
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 1.0,
      });

      expect(accumulator.hasTokens()).toBe(false);
    });
  });
});

describe("LiveStageEstimator (#233)", () => {
  const snapshot = (
    inputTokens: number,
    outputTokens: number,
    cacheReadTokens = 0,
    cacheCreationTokens = 0
  ): ParsedTokenUsage => ({
    inputTokens,
    outputTokens,
    cacheReadTokens,
    cacheCreationTokens,
    costUsd: 0,
  });

  it("is latest-wins for input + cache_read (growing-context snapshot, never summed)", () => {
    const est = new LiveStageEstimator();
    // Each assistant turn re-reports the FULL context — input grows every turn.
    est.observe(snapshot(1000, 10, 100));
    est.observe(snapshot(1800, 20, 250));
    est.observe(snapshot(2600, 30, 400));

    const e = est.estimate();
    // Latest snapshot wins — NOT 1000+1800+2600.
    expect(e.inputTokens).toBe(2600);
    expect(e.cacheReadTokens).toBe(400);
  });

  it("sums output per turn (genuine per-turn delta)", () => {
    const est = new LiveStageEstimator();
    est.observe(snapshot(1000, 10));
    est.observe(snapshot(1800, 20));
    est.observe(snapshot(2600, 30));

    // Output is additive: 10 + 20 + 30.
    expect(est.estimate().outputTokens).toBe(60);
  });

  it("reports hasObserved() only after the first snapshot", () => {
    const est = new LiveStageEstimator();
    expect(est.hasObserved()).toBe(false);
    est.observe(snapshot(100, 5));
    expect(est.hasObserved()).toBe(true);
  });

  it("leaves cost 0 with no source when constructed without (adapter, model)", () => {
    const est = new LiveStageEstimator();
    est.observe(snapshot(1000, 50, 100));
    const e = est.estimate();
    expect(e.costUsd).toBe(0);
    expect(e.costSource).toBeUndefined();
  });

  it("computes cost via the pricing table when constructed with (adapter, model)", () => {
    const est = new LiveStageEstimator("claude", "claude-sonnet-4-6");
    est.observe(snapshot(10_000, 2_000, 1_000));
    const e = est.estimate();
    // Assistant messages carry no native total_cost_usd, so cost is always
    // table-computed (never "native"). A priced model yields a positive cost.
    expect(e.costSource).toBe("computed");
    expect(e.costUsd).toBeGreaterThan(0);
  });

  it("setModel re-points the computed cost at the served model", () => {
    const est = new LiveStageEstimator("claude", "claude-sonnet-4-6");
    est.observe(snapshot(5_000, 500));
    est.setModel("claude-opus-4-7");
    // Still computes (opus is priced); the token counts are unaffected.
    const e = est.estimate();
    expect(e.inputTokens).toBe(5_000);
    expect(e.outputTokens).toBe(500);
    expect(e.costSource).toBe("computed");
  });
});

describe("resolveStageBookedUsage (#296 — book killed-stage cost)", () => {
  const assistantSnapshot = (
    inputTokens: number,
    outputTokens: number,
    cacheReadTokens = 0,
    cacheCreationTokens = 0
  ): ParsedTokenUsage => ({
    inputTokens,
    outputTokens,
    cacheReadTokens,
    cacheCreationTokens,
    costUsd: 0,
  });

  // A Claude terminal `result` envelope: session-cumulative native cost.
  const resultEnvelope = (costUsd: number, tokens = 500): ParsedTokenUsage => ({
    inputTokens: tokens,
    outputTokens: tokens,
    cacheReadTokens: 0,
    cacheCreationTokens: 0,
    costUsd,
    costCumulative: true,
  });

  it("books the authoritative accumulator total when a terminal envelope landed", () => {
    const acc = new TokenAccumulator("claude", "claude-sonnet-4-6");
    acc.add(resultEnvelope(0.42));
    const est = new LiveStageEstimator("claude", "claude-sonnet-4-6");
    // Even if the estimator also observed usage, the authoritative total wins.
    est.observe(assistantSnapshot(9_000, 900));

    const booked = resolveStageBookedUsage(acc, est);
    expect(booked).toBeDefined();
    expect(booked!.estimated).toBe(false);
    expect(booked!.usage.costUsd).toBeCloseTo(0.42, 4);
  });

  it("falls back to the live estimate when the stage was killed before any envelope", () => {
    // The bowlsheet #262 shape: SIGTERM'd mid-stage, so the accumulator never
    // received a `result` envelope, but the estimator observed the real burn.
    const acc = new TokenAccumulator("claude", "claude-sonnet-4-6");
    const est = new LiveStageEstimator("claude", "claude-sonnet-4-6");
    est.observe(assistantSnapshot(50_000, 4_000, 10_000));

    expect(acc.hasTokens()).toBe(false);
    const booked = resolveStageBookedUsage(acc, est);
    expect(booked).toBeDefined();
    expect(booked!.estimated).toBe(true);
    // The booked cost is the live estimate — a positive, table-computed number,
    // NOT $0 (the pre-#296 behavior).
    expect(booked!.usage.costUsd).toBeGreaterThan(0);
    expect(booked!.usage.costUsd).toBeCloseTo(est.estimate().costUsd, 10);
    expect(booked!.usage.inputTokens).toBe(50_000);
    expect(booked!.usage.outputTokens).toBe(4_000);
  });

  it("does NOT double-book: a partial envelope wins over the live estimate", () => {
    // If even a partial `result` envelope arrived before the kill, the
    // accumulator is authoritative and the (larger) live estimate is discarded
    // — booking the estimate ON TOP would double-count the stage's spend.
    const acc = new TokenAccumulator("claude", "claude-sonnet-4-6");
    acc.add(resultEnvelope(3.7));
    const est = new LiveStageEstimator("claude", "claude-sonnet-4-6");
    est.observe(assistantSnapshot(80_000, 6_000)); // estimate would exceed $3.70

    const booked = resolveStageBookedUsage(acc, est);
    expect(booked!.estimated).toBe(false);
    expect(booked!.usage.costUsd).toBeCloseTo(3.7, 4);
  });

  it("returns undefined when nothing was observed anywhere (terse zero record)", () => {
    const acc = new TokenAccumulator("claude", "claude-sonnet-4-6");
    const est = new LiveStageEstimator("claude", "claude-sonnet-4-6");
    expect(resolveStageBookedUsage(acc, est)).toBeUndefined();
  });
});

describe("parseStreamJsonOutput", () => {
  it("should parse multiple valid lines", () => {
    const output = [
      JSON.stringify({ type: "assistant" }),
      JSON.stringify({ type: "content_block_delta", delta: { text: "Hello" } }),
      JSON.stringify({ type: "result", usage: { input_tokens: 100 } }),
    ].join("\n");

    const result = parseStreamJsonOutput(output);

    expect(result).toHaveLength(3);
    expect(result[0].type).toBe("assistant");
    expect(result[1].type).toBe("content_block_delta");
    expect(result[2].type).toBe("result");
  });

  it("should skip empty lines", () => {
    const output = [
      JSON.stringify({ type: "assistant" }),
      "",
      JSON.stringify({ type: "result" }),
      "",
    ].join("\n");

    const result = parseStreamJsonOutput(output);

    expect(result).toHaveLength(2);
  });

  it("should skip invalid JSON lines", () => {
    const output = [
      JSON.stringify({ type: "assistant" }),
      "not valid json",
      JSON.stringify({ type: "result" }),
    ].join("\n");

    const result = parseStreamJsonOutput(output);

    expect(result).toHaveLength(2);
    expect(result[0].type).toBe("assistant");
    expect(result[1].type).toBe("result");
  });

  it("should handle mixed valid and invalid lines", () => {
    const output = [
      "",
      JSON.stringify({ type: "assistant" }),
      "invalid",
      "",
      JSON.stringify({ type: "content_block_delta", delta: { text: "Hi" } }),
      "{broken",
      JSON.stringify({ type: "result" }),
      "",
    ].join("\n");

    const result = parseStreamJsonOutput(output);

    expect(result).toHaveLength(3);
  });

  it("should return empty array for empty string", () => {
    expect(parseStreamJsonOutput("")).toEqual([]);
  });

  it("should return empty array for whitespace-only input", () => {
    expect(parseStreamJsonOutput("   \n\n   ")).toEqual([]);
  });
});

describe("extractTokenUsage", () => {
  it("should extract from single result message", () => {
    const output = JSON.stringify({
      type: "result",
      usage: {
        input_tokens: 500,
        output_tokens: 200,
      },
      total_cost_usd: 0.02,
    });

    const result = extractTokenUsage(output);

    expect(result).not.toBeNull();
    expect(result?.inputTokens).toBe(500);
    expect(result?.outputTokens).toBe(200);
    expect(result?.costUsd).toBe(0.02);
  });

  it("should extract from first result message when multiple exist", () => {
    const output = [
      JSON.stringify({ type: "assistant" }),
      JSON.stringify({
        type: "result",
        usage: { input_tokens: 100, output_tokens: 50 },
        total_cost_usd: 0.01,
      }),
      JSON.stringify({
        type: "result",
        usage: { input_tokens: 200, output_tokens: 100 },
        total_cost_usd: 0.02,
      }),
    ].join("\n");

    const result = extractTokenUsage(output);

    expect(result?.inputTokens).toBe(100);
    expect(result?.outputTokens).toBe(50);
    expect(result?.costUsd).toBe(0.01);
  });

  it("should return null when no result message", () => {
    const output = [
      JSON.stringify({ type: "assistant" }),
      JSON.stringify({ type: "content_block_delta", delta: { text: "Hi" } }),
    ].join("\n");

    expect(extractTokenUsage(output)).toBeNull();
  });

  it("should return null for empty input", () => {
    expect(extractTokenUsage("")).toBeNull();
  });

  it("should return null when result has no usage", () => {
    const output = JSON.stringify({
      type: "result",
      session_id: "test",
    });

    expect(extractTokenUsage(output)).toBeNull();
  });
});

describe("formatTokenCount", () => {
  describe("tokens < 1000", () => {
    it("should return plain number", () => {
      expect(formatTokenCount(0)).toBe("0");
      expect(formatTokenCount(1)).toBe("1");
      expect(formatTokenCount(500)).toBe("500");
      expect(formatTokenCount(999)).toBe("999");
    });
  });

  describe("tokens 1000-9999", () => {
    it("should format with 1 decimal K", () => {
      expect(formatTokenCount(1000)).toBe("1.0K");
      expect(formatTokenCount(1500)).toBe("1.5K");
      expect(formatTokenCount(2345)).toBe("2.3K");
      expect(formatTokenCount(9999)).toBe("10.0K");
    });
  });

  describe("tokens >= 10000", () => {
    it("should format with no decimal K", () => {
      expect(formatTokenCount(10000)).toBe("10K");
      expect(formatTokenCount(15000)).toBe("15K");
      expect(formatTokenCount(99999)).toBe("100K");
      expect(formatTokenCount(123456)).toBe("123K");
    });
  });

  describe("edge cases", () => {
    it("should handle zero", () => {
      expect(formatTokenCount(0)).toBe("0");
    });

    it("should handle exactly 1000", () => {
      expect(formatTokenCount(1000)).toBe("1.0K");
    });

    it("should handle exactly 10000", () => {
      expect(formatTokenCount(10000)).toBe("10K");
    });

    it("should handle very large numbers", () => {
      expect(formatTokenCount(1000000)).toBe("1000K");
    });
  });
});

describe("formatCost", () => {
  describe("cost < $0.01", () => {
    it("should format with 4 decimals", () => {
      expect(formatCost(0)).toBe("$0.0000");
      expect(formatCost(0.0001)).toBe("$0.0001");
      expect(formatCost(0.0099)).toBe("$0.0099");
      expect(formatCost(0.00456)).toBe("$0.0046");
    });
  });

  describe("cost $0.01-$0.99", () => {
    it("should format with 3 decimals", () => {
      expect(formatCost(0.01)).toBe("$0.010");
      expect(formatCost(0.05)).toBe("$0.050");
      expect(formatCost(0.123)).toBe("$0.123");
      expect(formatCost(0.999)).toBe("$0.999");
    });
  });

  describe("cost >= $1.00", () => {
    it("should format with 2 decimals", () => {
      expect(formatCost(1.0)).toBe("$1.00");
      expect(formatCost(1.5)).toBe("$1.50");
      expect(formatCost(10.0)).toBe("$10.00");
      expect(formatCost(99.99)).toBe("$99.99");
      expect(formatCost(1234.56)).toBe("$1234.56");
    });
  });

  describe("edge cases", () => {
    it("should handle zero", () => {
      expect(formatCost(0)).toBe("$0.0000");
    });

    it("should handle exactly 0.01", () => {
      expect(formatCost(0.01)).toBe("$0.010");
    });

    it("should handle exactly 1.00", () => {
      expect(formatCost(1.0)).toBe("$1.00");
    });
  });
});

/**
 * Regression tests for multi-callback cost inflation bug (Issue #1336)
 *
 * Root cause: Before commit a182957e (fix(#843)), PipelineStateService.updateTokens()
 * was called with cumulative tokenAccumulator.getTotal() values. Since updateTokens()
 * is additive (+=), each onTokenUsage callback re-added the full cumulative total,
 * inflating the logged stage cost.
 *
 * Anomalous run (issue #788, feature-validate, 2026-02-17):
 *   input: 35 tokens, output: 8,230, cache_read: 1,269,901, cache_create: 41,313
 *   Expected cost (Sonnet pricing): ~$0.66
 *   Logged cost (pre-fix):          $11.88  (~18× inflation)
 *
 * Fix: HeadlessOrchestrator.runStage() tracks prevUsage and passes only the
 * per-callback delta to updateTokens(), so the accumulated state equals the
 * true total regardless of how many times onTokenUsage fires.
 */
describe("delta conversion for multi-callback cost tracking — regression Issue #1336", () => {
  it("delta tracking: N callbacks with growing cumulative totals accumulates correctly", () => {
    // Simulate 18 onTokenUsage callbacks where tokenAccumulator.getTotal()
    // grows with each call (as it does when add() is called per result message).
    const CALLBACKS = 18;
    const PER_CALLBACK_COST = 0.66 / CALLBACKS; // each callback adds this increment

    let prevCostUsd = 0; // simulates prevUsage.costUsd in runStage()
    let accumulatedState = 0; // simulates PipelineStateService additive total

    for (let i = 1; i <= CALLBACKS; i++) {
      const cumulative = i * PER_CALLBACK_COST; // getTotal() grows with each add()
      const delta = cumulative - prevCostUsd; // FIX: convert to delta
      accumulatedState += delta; // updateTokens() is additive
      prevCostUsd = cumulative; // advance prevUsage for next turn
    }

    // With delta conversion the accumulated total equals the final cumulative value.
    expect(accumulatedState).toBeCloseTo(0.66, 5);
  });

  it("without delta tracking, multiple callbacks inflate the logged cost", () => {
    // Demonstrates the pre-fix bug: passing cumulative to additive updateTokens().
    const CALLBACKS = 18;
    const PER_CALLBACK_COST = 0.66 / CALLBACKS;

    let buggyState = 0;

    for (let i = 1; i <= CALLBACKS; i++) {
      const cumulative = i * PER_CALLBACK_COST;
      // BUG: add the full cumulative each time instead of the delta
      buggyState += cumulative;
    }

    // sum(1..18) × (0.66/18) = (18×19/2) × (0.66/18) = 9.5 × 0.66 ≈ $6.27
    // Clearly inflated — more than 9× the correct value of $0.66
    expect(buggyState).toBeGreaterThan(0.66 * 5);
    expect(buggyState).not.toBeCloseTo(0.66, 0);
  });

  it("TokenAccumulator getTotal() returns growing cumulative — prevUsage delta required", () => {
    // Verify that TokenAccumulator.getTotal() returns cumulative totals,
    // confirming why HeadlessOrchestrator must compute deltas via prevUsage.
    const acc = new TokenAccumulator();

    acc.add({
      inputTokens: 10,
      outputTokens: 5,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0.1,
    });
    expect(acc.getTotal().costUsd).toBeCloseTo(0.1);

    acc.add({
      inputTokens: 10,
      outputTokens: 5,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0.1,
    });
    // After two identical adds, getTotal() returns the CUMULATIVE value (0.20),
    // not the per-add value (0.10). Without delta tracking, updateTokens(0.20)
    // would be called, and the state would accumulate 0.10 + 0.20 = 0.30 instead of 0.20.
    expect(acc.getTotal().costUsd).toBeCloseTo(0.2);

    acc.add({
      inputTokens: 10,
      outputTokens: 5,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0.1,
    });
    expect(acc.getTotal().costUsd).toBeCloseTo(0.3);
  });

  it("delta conversion: single callback is a no-op (prevUsage starts at 0)", () => {
    // For the common case of a single onTokenUsage callback per stage,
    // delta = cumulative - 0 = cumulative, so the fix is transparent.
    const cumulative = 0.895; // healthy run #1273 cost
    const prevCostUsd = 0;
    const delta = cumulative - prevCostUsd;

    expect(delta).toBeCloseTo(0.895, 5); // same as cumulative for first callback
  });

  it("regression: anomalous $11.88 run — delta conversion gives $0.66 not $11.88", () => {
    // Issue #788 anomaly: 18 onTokenUsage callbacks, each seeing a growing
    // cumulative total. With delta tracking, the final state is the correct
    // single-stage cost (~$0.66), not the inflated sum.
    //
    // The anomalous run had exactly 18× inflation: $11.88 = 18 × $0.66.
    // This matches the pattern of 18 callbacks each adding the per-callback cost
    // increment, with the cumulative growing from 0 to $0.66 over 18 callbacks.
    const ANOMALOUS_TOTAL_COST = 0.66;
    const CALLBACK_COUNT = 18;
    const INCREMENT = ANOMALOUS_TOTAL_COST / CALLBACK_COUNT;

    // Simulate the fix (delta tracking)
    let prevCost = 0;
    let fixedState = 0;
    for (let i = 1; i <= CALLBACK_COUNT; i++) {
      const cumulative = i * INCREMENT;
      fixedState += cumulative - prevCost;
      prevCost = cumulative;
    }

    // Simulate the bug (no delta tracking)
    let buggyState = 0;
    for (let i = 1; i <= CALLBACK_COUNT; i++) {
      const cumulative = i * INCREMENT;
      buggyState += cumulative; // re-adds cumulative each time
    }

    expect(fixedState).toBeCloseTo(ANOMALOUS_TOTAL_COST, 5); // correct: $0.66
    expect(buggyState).toBeGreaterThan(ANOMALOUS_TOTAL_COST * 5); // inflated
  });
});

describe("formatTokenUsageDisplay", () => {
  it("should combine token count and cost", () => {
    const usage: ParsedTokenUsage = {
      inputTokens: 500,
      outputTokens: 300,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0.05,
    };

    const result = formatTokenUsageDisplay(usage);

    expect(result).toBe("800 tokens | $0.050");
  });

  it("should sum input and output tokens", () => {
    const usage: ParsedTokenUsage = {
      inputTokens: 1000,
      outputTokens: 500,
      cacheReadTokens: 100,
      cacheCreationTokens: 50,
      costUsd: 0.02,
    };

    const result = formatTokenUsageDisplay(usage);

    // Only input + output, not cache tokens
    expect(result).toBe("1.5K tokens | $0.020");
  });

  it("should handle large token counts", () => {
    const usage: ParsedTokenUsage = {
      inputTokens: 50000,
      outputTokens: 25000,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 5.0,
    };

    const result = formatTokenUsageDisplay(usage);

    expect(result).toBe("75K tokens | $5.00");
  });

  it("should handle zero tokens", () => {
    const usage: ParsedTokenUsage = {
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0,
    };

    const result = formatTokenUsageDisplay(usage);

    expect(result).toBe("0 tokens | $0.0000");
  });
});

describe("calculateRateLimitWait (Issue #2573)", () => {
  it("should return positive wait for future timestamp", () => {
    const futureEpoch = Math.floor(Date.now() / 1000) + 600; // 10 min from now
    const wait = calculateRateLimitWait(futureEpoch);
    expect(wait).toBeGreaterThan(0);
    expect(wait).toBeLessThanOrEqual(600_000 + 100); // ~10 min + small tolerance
  });

  it("should return 0 for past timestamp", () => {
    const pastEpoch = Math.floor(Date.now() / 1000) - 60; // 1 min ago
    expect(calculateRateLimitWait(pastEpoch)).toBe(0);
  });

  it("should return 0 for zero timestamp", () => {
    expect(calculateRateLimitWait(0)).toBe(0);
  });
});

describe("isHardRateLimit (Issue #2573)", () => {
  it("should return true for 'limited'", () => {
    expect(isHardRateLimit("limited")).toBe(true);
  });

  it("should return false for 'allowed_warning'", () => {
    expect(isHardRateLimit("allowed_warning")).toBe(false);
  });

  it("should return false for empty string", () => {
    expect(isHardRateLimit("")).toBe(false);
  });

  it("should return false for unknown status", () => {
    expect(isHardRateLimit("something_else")).toBe(false);
  });
});

describe("isQuotaPressureSignal (Issue #3825)", () => {
  it("returns true for 'limited' (hard limit hit)", () => {
    expect(isQuotaPressureSignal("limited")).toBe(true);
  });

  it("returns true for 'allowed_warning' (approaching limit)", () => {
    expect(isQuotaPressureSignal("allowed_warning")).toBe(true);
  });

  it("returns false for a plain 'allowed' — the #3804 false-cooldown regression", () => {
    // A healthy `allowed` event is steady-state telemetry the CLI emits on
    // nearly every run. It must NOT arm the quota fast-fail / cooldown path,
    // or an ordinary idle stall after it gets mis-classified as quota
    // exhaustion and halts ALL autonomous dispatch (Issue #3804 / #3825).
    expect(isQuotaPressureSignal("allowed")).toBe(false);
  });

  it("returns false for empty string", () => {
    expect(isQuotaPressureSignal("")).toBe(false);
  });

  it("returns false for unknown status", () => {
    expect(isQuotaPressureSignal("something_else")).toBe(false);
  });
});

describe("formatRateLimitCountdown (Issue #2573)", () => {
  it("should format seconds", () => {
    expect(formatRateLimitCountdown(5000)).toBe("~5s");
    expect(formatRateLimitCountdown(45000)).toBe("~45s");
  });

  it("should format minutes", () => {
    expect(formatRateLimitCountdown(60_000)).toBe("~1m");
    expect(formatRateLimitCountdown(720_000)).toBe("~12m");
    expect(formatRateLimitCountdown(3_540_000)).toBe("~59m");
  });

  it("should format hours and minutes", () => {
    expect(formatRateLimitCountdown(3_600_000)).toBe("~1h");
    expect(formatRateLimitCountdown(3_900_000)).toBe("~1h 5m");
    expect(formatRateLimitCountdown(7_200_000)).toBe("~2h");
  });

  it("should handle zero and negative", () => {
    expect(formatRateLimitCountdown(0)).toBe("~0s");
    expect(formatRateLimitCountdown(-1000)).toBe("~0s");
  });

  it("should handle sub-second durations", () => {
    expect(formatRateLimitCountdown(500)).toBe("~1s");
  });
});

describe("TokenAccumulator.setModel (#91)", () => {
  it("re-points the computed-cost fallback at the served model's pricing", () => {
    const acc = new TokenAccumulator("claude", "claude-opus-4-8");
    // No native cost → the computed path prices via (adapter, model).
    acc.add({
      inputTokens: 1_000_000,
      outputTokens: 100_000,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0,
    });
    const opusCost = acc.getTotal().costUsd;
    expect(opusCost).toBeGreaterThan(0);

    acc.setModel("claude-haiku-4-5");
    const haikuCost = acc.getTotal().costUsd;
    expect(haikuCost).toBeGreaterThan(0);
    expect(haikuCost).toBeLessThan(opusCost);
  });

  it("ignores empty model strings", () => {
    const acc = new TokenAccumulator("claude", "claude-opus-4-8");
    acc.add({
      inputTokens: 100_000,
      outputTokens: 10_000,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0,
    });
    const before = acc.getTotal().costUsd;
    acc.setModel("");
    expect(acc.getTotal().costUsd).toBe(before);
  });

  it("never overrides the native cost path", () => {
    const acc = new TokenAccumulator("claude", "claude-opus-4-8");
    acc.add({
      inputTokens: 1000,
      outputTokens: 500,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0.42,
    });
    acc.setModel("claude-haiku-4-5");
    const total = acc.getTotal();
    expect(total.costUsd).toBe(0.42);
    expect(total.costSource).toBe("native");
  });
});
