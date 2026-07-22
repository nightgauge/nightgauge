/**
 * Tests for stream-json envelope filtering utility
 *
 * Issue #792: Filter raw JSON tool result envelopes from pipeline output
 */

import { describe, it, expect } from "vitest";
import { isStreamJsonEnvelope, isEnvelopeFragment } from "../../src/utils/streamJsonFilter";

describe("isStreamJsonEnvelope", () => {
  describe("envelopes that SHOULD be filtered", () => {
    it("should detect user tool-result envelope", () => {
      const line = JSON.stringify({
        type: "user",
        message: {
          role: "user",
          content: [
            {
              tool_use_id: "toolu_abc123",
              type: "tool_result",
              content: "File written successfully",
            },
          ],
        },
      });
      expect(isStreamJsonEnvelope(line)).toBe(true);
    });

    it("should detect simple user envelope", () => {
      expect(isStreamJsonEnvelope('{"type":"user","message":{"role":"user"}}')).toBe(true);
    });

    it("should detect system envelope", () => {
      const line = JSON.stringify({
        type: "system",
        message: { role: "system", content: "You are an AI assistant." },
      });
      expect(isStreamJsonEnvelope(line)).toBe(true);
    });

    it("should detect content_block_stop envelope", () => {
      expect(isStreamJsonEnvelope('{"type":"content_block_stop","index":0}')).toBe(true);
    });

    it("should detect message_start envelope", () => {
      const line = JSON.stringify({
        type: "message_start",
        message: { id: "msg_abc", role: "assistant" },
      });
      expect(isStreamJsonEnvelope(line)).toBe(true);
    });

    it("should detect message_stop envelope", () => {
      expect(isStreamJsonEnvelope('{"type":"message_stop"}')).toBe(true);
    });

    it("should detect message_delta envelope", () => {
      const line = JSON.stringify({
        type: "message_delta",
        delta: { stop_reason: "end_turn" },
        usage: { output_tokens: 42 },
      });
      expect(isStreamJsonEnvelope(line)).toBe(true);
    });

    it("should handle envelopes with leading/trailing whitespace", () => {
      expect(isStreamJsonEnvelope('  {"type":"user","message":{}}  ')).toBe(true);
    });
  });

  describe("lines that should NOT be filtered", () => {
    it("should keep assistant messages", () => {
      const line = JSON.stringify({
        type: "assistant",
        message: {
          role: "assistant",
          content: [{ type: "text", text: "Hello" }],
        },
      });
      expect(isStreamJsonEnvelope(line)).toBe(false);
    });

    it("should keep content_block_delta messages", () => {
      const line = JSON.stringify({
        type: "content_block_delta",
        delta: { type: "text_delta", text: "Hello world" },
      });
      expect(isStreamJsonEnvelope(line)).toBe(false);
    });

    it("should keep content_block_start messages", () => {
      const line = JSON.stringify({
        type: "content_block_start",
        content_block: { type: "tool_use", name: "Read" },
      });
      expect(isStreamJsonEnvelope(line)).toBe(false);
    });

    it("should keep result messages (token usage)", () => {
      const line = JSON.stringify({
        type: "result",
        usage: { input_tokens: 100, output_tokens: 50 },
      });
      expect(isStreamJsonEnvelope(line)).toBe(false);
    });

    it("should keep error messages", () => {
      const line = JSON.stringify({
        type: "error",
        error: { message: "Rate limit exceeded" },
      });
      expect(isStreamJsonEnvelope(line)).toBe(false);
    });

    it("should keep plain text lines", () => {
      expect(isStreamJsonEnvelope("Starting feature-dev...")).toBe(false);
      expect(isStreamJsonEnvelope("✓ Tests passed")).toBe(false);
      expect(isStreamJsonEnvelope("npm run build")).toBe(false);
    });

    it("should keep empty lines", () => {
      expect(isStreamJsonEnvelope("")).toBe(false);
      expect(isStreamJsonEnvelope("   ")).toBe(false);
    });

    it("should not filter partial/broken JSON that starts with a filtered prefix", () => {
      // A partial buffer chunk that starts with a filtered prefix but isn't valid JSON
      expect(
        isStreamJsonEnvelope(
          '{"type":"user","message":{"role":"user","content":[{"tool_use_id":"to'
        )
      ).toBe(false);
    });

    it("should keep legitimate JSON that is not a stream-json envelope", () => {
      expect(isStreamJsonEnvelope('{"status":"ok"}')).toBe(false);
      expect(isStreamJsonEnvelope('{"name":"test","value":42}')).toBe(false);
    });

    it("should keep token:usage messages", () => {
      const line = JSON.stringify({
        type: "token:usage",
        inputTokens: 100,
        outputTokens: 50,
      });
      expect(isStreamJsonEnvelope(line)).toBe(false);
    });
  });
});

describe("isEnvelopeFragment", () => {
  describe("fragments that SHOULD be detected", () => {
    it("should detect fragment containing tool_use_id", () => {
      expect(
        isEnvelopeFragment('ol_abc123","type":"tool_result","content":"File written"}]}')
      ).toBe(true);
    });

    it("should detect fragment containing tool_result type", () => {
      expect(isEnvelopeFragment('"type":"tool_result","content":"success"')).toBe(true);
    });

    it("should detect fragment with tool_use_id at start", () => {
      expect(isEnvelopeFragment('"tool_use_id":"toolu_abc","type":"tool_result"')).toBe(true);
    });

    it("should handle fragments with whitespace", () => {
      expect(isEnvelopeFragment('  "tool_use_id":"toolu_123"  ')).toBe(true);
    });
  });

  describe("lines that should NOT be detected as fragments", () => {
    it("should not detect plain text", () => {
      expect(isEnvelopeFragment("Starting pipeline...")).toBe(false);
      expect(isEnvelopeFragment("Build successful")).toBe(false);
    });

    it("should not detect empty lines", () => {
      expect(isEnvelopeFragment("")).toBe(false);
      expect(isEnvelopeFragment("   ")).toBe(false);
    });

    it("should not detect valid JSON output", () => {
      expect(isEnvelopeFragment('{"status":"ok"}')).toBe(false);
    });

    it("should not detect assistant message text", () => {
      expect(isEnvelopeFragment("I will read the file and check the implementation.")).toBe(false);
    });
  });

  describe("tool result metadata fragments (Issue #873)", () => {
    it('should detect fragment with "interrupted" field', () => {
      expect(isEnvelopeFragment('","stderr":"","interrupted":false,"isImage":false}')).toBe(true);
    });

    it('should detect fragment with "isImage" field', () => {
      expect(isEnvelopeFragment('"isImage":false,"noOutputExpected":false}}')).toBe(true);
    });

    it('should detect fragment with "noOutputExpected" field', () => {
      expect(isEnvelopeFragment('"noOutputExpected":false}}')).toBe(true);
    });

    it("should detect combined metadata trailing fragment", () => {
      expect(
        isEnvelopeFragment('","interrupted":false,"isImage":false,"noOutputExpected":false}}')
      ).toBe(true);
    });

    it("should not filter plain text mentioning interrupted without quotes", () => {
      expect(isEnvelopeFragment("The process was interrupted by the user")).toBe(false);
    });
  });
});
