/**
 * streamOutputHandler.test.ts - Tests for streaming delta accumulation
 *
 * Verifies that content_block_delta fragments are accumulated into
 * complete text blocks before being sent to appendLine(), ensuring
 * reliable content-type detection for code vs text classification.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { createStreamOutputHandler } from "../../src/utils/streamOutputHandler";

// Mock OutputWindow that records appendLine calls
function createMockOutputWindow() {
  const calls: Array<{ text: string; level: string; stage?: string }> = [];
  return {
    appendLine: vi.fn((text: string, level: string, stage?: string) => {
      calls.push({ text, level, stage });
    }),
    calls,
  };
}

describe("createStreamOutputHandler", () => {
  let mockOutputWindow: ReturnType<typeof createMockOutputWindow>;
  let handler: ReturnType<typeof createStreamOutputHandler>;

  beforeEach(() => {
    mockOutputWindow = createMockOutputWindow();
    handler = createStreamOutputHandler(mockOutputWindow as any);
  });

  describe("delta accumulation", () => {
    it("should accumulate content_block_delta fragments", () => {
      const deltas = [
        JSON.stringify({
          type: "content_block_delta",
          delta: { type: "text_delta", text: "function foo() {\n" },
        }),
        JSON.stringify({
          type: "content_block_delta",
          delta: { type: "text_delta", text: "  return true;\n" },
        }),
        JSON.stringify({
          type: "content_block_delta",
          delta: { type: "text_delta", text: "}" },
        }),
      ].join("\n");

      handler.onStdout("feature-dev", deltas);

      // Nothing should be emitted yet — still accumulating
      expect(mockOutputWindow.calls).toHaveLength(0);
    });

    it("should flush accumulated text on content_block_stop", () => {
      // Send deltas
      handler.onStdout(
        "feature-dev",
        [
          JSON.stringify({
            type: "content_block_delta",
            delta: { type: "text_delta", text: "const x = 1;\n" },
          }),
          JSON.stringify({
            type: "content_block_delta",
            delta: { type: "text_delta", text: "const y = 2;" },
          }),
        ].join("\n")
      );

      expect(mockOutputWindow.calls).toHaveLength(0);

      // Send content_block_stop
      handler.onStdout("feature-dev", JSON.stringify({ type: "content_block_stop", index: 0 }));

      // Now the accumulated text should be flushed as a single entry
      expect(mockOutputWindow.calls).toHaveLength(1);
      expect(mockOutputWindow.calls[0].text).toBe("const x = 1;\nconst y = 2;");
      expect(mockOutputWindow.calls[0].level).toBe("info");
      expect(mockOutputWindow.calls[0].stage).toBe("feature-dev");
    });

    it("should flush on manual flush() call", () => {
      handler.onStdout(
        "feature-dev",
        JSON.stringify({
          type: "content_block_delta",
          delta: { type: "text_delta", text: "buffered content" },
        })
      );

      expect(mockOutputWindow.calls).toHaveLength(0);

      handler.flush();

      expect(mockOutputWindow.calls).toHaveLength(1);
      expect(mockOutputWindow.calls[0].text).toBe("buffered content");
    });
  });

  describe("real code sample - complete block detection", () => {
    it("should accumulate full code sample as one appendLine call", () => {
      // Simulate Claude streaming a code block in fragments
      const codeFragments = [
        "if (recent.length < 3 || older.length < 3) {\n",
        "  return { improving: true, percentChange: 0, hasEnoughData: false };\n",
        "}\n\n",
        "const avgCost = (runs: PipelineRunSummary[]): number => {\n",
        "  if (runs.length === 0) return 0;\n",
        "  return runs.reduce((sum, r) => sum + r.usage.costUsd, 0) / runs.length;\n",
        "};\n\n",
        "// Lower cost = improving\n",
        "return {\n",
        "  improving: percentChange < 0,\n",
        "  hasEnoughData: true,\n",
        "};\n",
        "  }\n\n\n",
        "  /**\n",
        "   * Get token usage trend analysis\n",
        "   */\n",
        "  getTokenTrend(\n",
        "    recentCount: number = 5\n",
        "  ): {\n",
        "    direction: 'up' | 'down' | 'stable';\n",
        "  } {\n",
      ];

      // Send each fragment as a content_block_delta
      for (const fragment of codeFragments) {
        handler.onStdout(
          "feature-dev",
          JSON.stringify({
            type: "content_block_delta",
            delta: { type: "text_delta", text: fragment },
          })
        );
      }

      // Nothing emitted yet
      expect(mockOutputWindow.calls).toHaveLength(0);

      // Send content_block_stop
      handler.onStdout("feature-dev", JSON.stringify({ type: "content_block_stop", index: 0 }));

      // Should produce exactly ONE appendLine call with all content
      expect(mockOutputWindow.calls).toHaveLength(1);
      expect(mockOutputWindow.calls[0].text).toContain("getTokenTrend");
      expect(mockOutputWindow.calls[0].text).toContain("if (recent.length < 3");
      // All fragments should be concatenated
      expect(mockOutputWindow.calls[0].text).toBe(codeFragments.join(""));
    });
  });

  describe("non-delta messages", () => {
    it("should pass assistant text directly (not accumulated)", () => {
      const assistantLine = JSON.stringify({
        type: "assistant",
        message: {
          role: "assistant",
          content: [{ type: "text", text: "Complete response" }],
        },
      });

      handler.onStdout("feature-dev", assistantLine);

      expect(mockOutputWindow.calls).toHaveLength(1);
      expect(mockOutputWindow.calls[0].text).toBe("Complete response");
    });

    it("should flush pending delta before emitting assistant text", () => {
      // Accumulate some delta
      handler.onStdout(
        "feature-dev",
        JSON.stringify({
          type: "content_block_delta",
          delta: { type: "text_delta", text: "pending delta" },
        })
      );

      // Then assistant message arrives
      handler.onStdout(
        "feature-dev",
        JSON.stringify({
          type: "assistant",
          message: {
            content: [{ type: "text", text: "assistant text" }],
          },
        })
      );

      // Delta should be flushed first, then assistant text
      expect(mockOutputWindow.calls).toHaveLength(2);
      expect(mockOutputWindow.calls[0].text).toBe("pending delta");
      expect(mockOutputWindow.calls[1].text).toBe("assistant text");
    });

    it("should pass plain text (non-JSON) directly", () => {
      handler.onStdout("feature-dev", "some plain text");

      expect(mockOutputWindow.calls).toHaveLength(1);
      expect(mockOutputWindow.calls[0].text).toBe("some plain text");
    });

    it("should filter stream-json envelopes", () => {
      handler.onStdout("feature-dev", JSON.stringify({ type: "message_start", message: {} }));

      expect(mockOutputWindow.calls).toHaveLength(0);
    });
  });

  describe("skip duplicate stage messages (Issue #770)", () => {
    it('should skip delta text that starts with "Starting "', () => {
      handler.onStdout(
        "feature-dev",
        JSON.stringify({
          type: "content_block_delta",
          delta: {
            type: "text_delta",
            text: "Starting Feature Development...",
          },
        })
      );

      // Send stop to flush
      handler.onStdout("feature-dev", JSON.stringify({ type: "content_block_stop", index: 0 }));

      // Should be empty — the "Starting" text was skipped
      expect(mockOutputWindow.calls).toHaveLength(0);
    });
  });

  describe("tool_result phase marker detection (Issue #3748)", () => {
    const makeUserEnvelope = (content: unknown) =>
      JSON.stringify({
        type: "user",
        message: {
          role: "user",
          content: [{ type: "tool_result", content }],
        },
      });

    it("detects phase marker in tool_result string content", () => {
      const onPhaseDetected = vi.fn();
      const h = createStreamOutputHandler(mockOutputWindow as any, { onPhaseDetected });
      const marker =
        '<!-- phase:start name="implementation" index=8 total=18 stage="feature-dev" -->';

      h.onStdout("feature-dev", makeUserEnvelope(marker));

      expect(onPhaseDetected).toHaveBeenCalledTimes(1);
      expect(onPhaseDetected).toHaveBeenCalledWith("feature-dev", {
        name: "implementation",
        index: 8,
        total: 18,
        stage: "feature-dev",
      });
      expect(mockOutputWindow.calls).toHaveLength(0);
    });

    it("detects phase marker in tool_result array content", () => {
      const onPhaseDetected = vi.fn();
      const h = createStreamOutputHandler(mockOutputWindow as any, { onPhaseDetected });
      const markerText = '<!-- phase:start name="testing" index=9 total=18 stage="feature-dev" -->';
      const envelope = JSON.stringify({
        type: "user",
        message: {
          role: "user",
          content: [
            {
              type: "tool_result",
              content: [{ type: "text", text: markerText }],
            },
          ],
        },
      });

      h.onStdout("feature-dev", envelope);

      expect(onPhaseDetected).toHaveBeenCalledTimes(1);
      expect(onPhaseDetected).toHaveBeenCalledWith("feature-dev", {
        name: "testing",
        index: 9,
        total: 18,
        stage: "feature-dev",
      });
    });

    it("does not fire callback for non-phase tool_result content", () => {
      const onPhaseDetected = vi.fn();
      const h = createStreamOutputHandler(mockOutputWindow as any, { onPhaseDetected });

      h.onStdout("feature-dev", makeUserEnvelope("some bash output without markers"));

      expect(onPhaseDetected).not.toHaveBeenCalled();
    });

    it("warns and does not fire callback when phase:start is malformed", () => {
      const onPhaseDetected = vi.fn();
      const consoleSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
      const h = createStreamOutputHandler(mockOutputWindow as any, { onPhaseDetected });

      h.onStdout("feature-dev", makeUserEnvelope("phase:start without proper format"));

      expect(onPhaseDetected).not.toHaveBeenCalled();
      expect(consoleSpy).toHaveBeenCalledWith(expect.stringContaining("format drift"));
      consoleSpy.mockRestore();
    });

    it("warns when no callback wired and phase:start text in plain content", () => {
      const consoleSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
      const h = createStreamOutputHandler(mockOutputWindow as any);

      h.onStdout(
        "feature-dev",
        JSON.stringify({
          type: "content_block_delta",
          delta: { text: '<!-- phase:start name="foo" index=1 total=5 stage="feature-dev" -->' },
        })
      );
      h.onStdout("feature-dev", JSON.stringify({ type: "content_block_stop" }));

      expect(consoleSpy).toHaveBeenCalledWith(expect.stringContaining("callback not wired"));
      consoleSpy.mockRestore();
    });
  });

  describe("phase marker detection (Issue #3453)", () => {
    it("single marker: callback fires once, appendLine not called", () => {
      const onPhaseDetected = vi.fn();
      const handlerWithPhase = createStreamOutputHandler(mockOutputWindow as any, {
        onPhaseDetected,
      });
      const text =
        '<!-- phase:start name="plan-verification" index=4 total=17 stage="feature-dev" -->';

      handlerWithPhase.onStdout(
        "feature-dev",
        JSON.stringify({ type: "content_block_delta", delta: { text } })
      );
      handlerWithPhase.onStdout("feature-dev", JSON.stringify({ type: "content_block_stop" }));

      expect(onPhaseDetected).toHaveBeenCalledTimes(1);
      expect(onPhaseDetected).toHaveBeenCalledWith("feature-dev", {
        name: "plan-verification",
        index: 4,
        total: 17,
        stage: "feature-dev",
      });
      expect(mockOutputWindow.calls).toHaveLength(0);
    });

    it("3 bundled markers: callback fires 3 times in document order, appendLine not called", () => {
      const onPhaseDetected = vi.fn();
      const handlerWithPhase = createStreamOutputHandler(mockOutputWindow as any, {
        onPhaseDetected,
      });
      const text = [
        '<!-- phase:start name="plan-verification" index=4 total=17 stage="feature-dev" -->',
        '<!-- phase:start name="implementation" index=7 total=17 stage="feature-dev" -->',
        '<!-- phase:start name="testing" index=8 total=17 stage="feature-dev" -->',
      ].join("\n");

      handlerWithPhase.onStdout(
        "feature-dev",
        JSON.stringify({ type: "content_block_delta", delta: { text } })
      );
      handlerWithPhase.onStdout("feature-dev", JSON.stringify({ type: "content_block_stop" }));

      expect(onPhaseDetected).toHaveBeenCalledTimes(3);
      const calls = vi.mocked(onPhaseDetected).mock.calls;
      expect(calls[0][1].name).toBe("plan-verification");
      expect(calls[1][1].name).toBe("implementation");
      expect(calls[2][1].name).toBe("testing");
      expect(mockOutputWindow.calls).toHaveLength(0);
    });

    it("text with no marker: callback not called, appendLine called once", () => {
      const onPhaseDetected = vi.fn();
      const handlerWithPhase = createStreamOutputHandler(mockOutputWindow as any, {
        onPhaseDetected,
      });
      const text = "Just regular output with no phase markers";

      handlerWithPhase.onStdout(
        "feature-dev",
        JSON.stringify({ type: "content_block_delta", delta: { text } })
      );
      handlerWithPhase.onStdout("feature-dev", JSON.stringify({ type: "content_block_stop" }));

      expect(onPhaseDetected).not.toHaveBeenCalled();
      expect(mockOutputWindow.calls).toHaveLength(1);
    });

    it("replay of issue #3453: bundled plan-verification + implementation markers both fire in order", () => {
      const onPhaseDetected = vi.fn();
      const handlerWithPhase = createStreamOutputHandler(mockOutputWindow as any, {
        onPhaseDetected,
      });
      const text = [
        '<!-- phase:start name="plan-verification" index=4 total=17 stage="feature-dev" -->',
        "",
        '<!-- phase:start name="implementation" index=7 total=17 stage="feature-dev" -->',
      ].join("\n");

      handlerWithPhase.onStdout(
        "feature-dev",
        JSON.stringify({ type: "content_block_delta", delta: { text } })
      );
      handlerWithPhase.onStdout("feature-dev", JSON.stringify({ type: "content_block_stop" }));

      expect(onPhaseDetected).toHaveBeenCalledTimes(2);
      const calls = vi.mocked(onPhaseDetected).mock.calls;
      expect(calls[0][1].name).toBe("plan-verification");
      expect(calls[1][1].name).toBe("implementation");
      expect(mockOutputWindow.calls).toHaveLength(0);
    });
  });
});
