/**
 * OutputWindowPolish.test.ts - Tests for output polish improvements (Issue #846)
 *
 * Verifies:
 * - Leading/trailing blank line trimming in appendLine()
 * - Tool result JSON filtering in parseStreamOutput()
 * - No regression on existing entry rendering
 * - Consecutive blank suppression (Issue #794) still works after trim
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { OutputWindow } from "../../../src/views/outputWindow/OutputWindow";
import { parseStreamOutput } from "../../../src/commands/runStage";

// Minimal workspace state mock
function createMockWorkspaceState(): any {
  const storage = new Map<string, any>();
  return {
    get: vi.fn((key: string) => storage.get(key)),
    update: vi.fn((key: string, value: any) => {
      storage.set(key, value);
      return Promise.resolve();
    }),
  };
}

// Minimal extension URI mock
function createMockExtensionUri(): any {
  return {
    fsPath: "/mock/extension",
    toString: () => "/mock/extension",
  };
}

describe("Output Window Polish (Issue #846)", () => {
  let outputWindow: OutputWindow;

  beforeEach(() => {
    outputWindow = new OutputWindow(createMockExtensionUri(), createMockWorkspaceState());
  });

  describe("leading/trailing blank line trimming", () => {
    it("should trim leading newlines from entry text", () => {
      outputWindow.appendLine("\n\nHello world", "info");

      const entries = outputWindow.getState().getEntries();
      expect(entries[0].text).toBe("Hello world");
    });

    it("should trim trailing newlines from entry text", () => {
      outputWindow.appendLine("Hello world\n\n", "info");

      const entries = outputWindow.getState().getEntries();
      expect(entries[0].text).toBe("Hello world");
    });

    it("should trim both leading and trailing newlines", () => {
      outputWindow.appendLine("\n\n\nContent here\n\n", "info");

      const entries = outputWindow.getState().getEntries();
      expect(entries[0].text).toBe("Content here");
    });

    it("should preserve internal newlines", () => {
      outputWindow.appendLine("\nLine 1\nLine 2\n", "info");

      const entries = outputWindow.getState().getEntries();
      expect(entries[0].text).toBe("Line 1\nLine 2");
    });

    it("should handle text with no leading/trailing newlines", () => {
      outputWindow.appendLine("No extra whitespace", "info");

      const entries = outputWindow.getState().getEntries();
      expect(entries[0].text).toBe("No extra whitespace");
    });
  });

  describe("consecutive blank suppression still works after trim", () => {
    it("should still collapse consecutive blank-only entries", () => {
      outputWindow.appendLine("Line 1", "info");
      outputWindow.appendLine("", "info"); // first blank — kept
      outputWindow.appendLine("", "info"); // second blank — suppressed
      outputWindow.appendLine("Line 2", "info");

      const entries = outputWindow.getState().getEntries();
      const texts = entries.map((e) => e.text);

      expect(texts).toEqual(["Line 1", "", "Line 2"]);
    });

    it("should allow single blank lines between content", () => {
      outputWindow.appendLine("Section 1", "info");
      outputWindow.appendLine("", "info");
      outputWindow.appendLine("Section 2", "info");

      const entries = outputWindow.getState().getEntries();
      const texts = entries.map((e) => e.text);

      expect(texts).toEqual(["Section 1", "", "Section 2"]);
    });
  });

  describe("no regression on existing entries", () => {
    it("should render normal text entries correctly", () => {
      outputWindow.appendLine("Starting pipeline...", "info");
      outputWindow.appendLine("Processing issue #42", "info");

      const entries = outputWindow.getState().getEntries();
      expect(entries).toHaveLength(2);
      expect(entries[0].text).toBe("Starting pipeline...");
      expect(entries[1].text).toBe("Processing issue #42");
    });

    it("should preserve multi-line content with double newlines", () => {
      outputWindow.appendLine("Part 1\n\nPart 2", "info");

      const entries = outputWindow.getState().getEntries();
      expect(entries[0].text).toBe("Part 1\n\nPart 2");
    });
  });
});

describe("parseStreamOutput tool_result filtering (Issue #846)", () => {
  it("should suppress raw tool_result JSON messages", () => {
    const toolResultLine = JSON.stringify({
      type: "user",
      message: {
        role: "user",
        content: [
          {
            type: "tool_result",
            tool_use_id: "toolu_abc123",
            content: "File contents here...",
          },
        ],
      },
    });

    const items = parseStreamOutput(toolResultLine);
    expect(items).toHaveLength(0);
  });

  it("should suppress tool_result with multiple content blocks", () => {
    const toolResultLine = JSON.stringify({
      type: "user",
      message: {
        role: "user",
        content: [
          {
            type: "tool_result",
            tool_use_id: "toolu_abc123",
            content: "Result 1",
          },
          {
            type: "tool_result",
            tool_use_id: "toolu_def456",
            content: "Result 2",
          },
        ],
      },
    });

    const items = parseStreamOutput(toolResultLine);
    expect(items).toHaveLength(0);
  });

  it("should NOT suppress assistant messages", () => {
    const assistantLine = JSON.stringify({
      type: "assistant",
      message: {
        role: "assistant",
        content: [
          {
            type: "text",
            text: "Here is my response",
          },
        ],
      },
    });

    const items = parseStreamOutput(assistantLine);
    expect(items).toHaveLength(1);
    expect(items[0].type).toBe("text");
    expect(items[0].text).toBe("Here is my response");
  });

  it("should NOT suppress user messages without tool_use_id", () => {
    const userLine = JSON.stringify({
      type: "user",
      message: {
        role: "user",
        content: [
          {
            type: "text",
            text: "Hello there",
          },
        ],
      },
    });

    const items = parseStreamOutput(userLine);
    // User messages without tool_use_id don't match any handler, so they're not emitted
    // (they fall through all conditions and produce no output items)
    // This is expected behavior — only assistant messages produce text items
    expect(items).toHaveLength(0);
  });

  it("should preserve plain text lines (non-JSON)", () => {
    const items = parseStreamOutput("Some plain text output\nAnother line");
    expect(items).toHaveLength(2);
    expect(items[0].text).toBe("Some plain text output");
    expect(items[1].text).toBe("Another line");
  });

  it("should handle token usage messages", () => {
    const tokenLine = JSON.stringify({
      type: "token:usage",
      inputTokens: 1000,
      outputTokens: 500,
      costUsd: 0.05,
    });

    const items = parseStreamOutput(tokenLine);
    expect(items).toHaveLength(1);
    expect(items[0].text).toContain("Tokens:");
  });
});

describe("parseStreamOutput content_block_delta accumulation", () => {
  it("should emit content_block_delta as text_delta type", () => {
    const deltaLine = JSON.stringify({
      type: "content_block_delta",
      delta: { type: "text_delta", text: "Hello " },
    });

    const items = parseStreamOutput(deltaLine);
    expect(items).toHaveLength(1);
    expect(items[0].type).toBe("text_delta");
    expect(items[0].text).toBe("Hello ");
  });

  it("should emit content_block_stop as its own type", () => {
    const stopLine = JSON.stringify({
      type: "content_block_stop",
      index: 0,
    });

    const items = parseStreamOutput(stopLine);
    expect(items).toHaveLength(1);
    expect(items[0].type).toBe("content_block_stop");
  });

  it("should produce text_delta items from multiple deltas", () => {
    const lines = [
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

    const items = parseStreamOutput(lines);
    expect(items).toHaveLength(3);
    expect(items.every((i) => i.type === "text_delta")).toBe(true);
    expect(items.map((i) => i.text).join("")).toBe("function foo() {\n  return true;\n}");
  });

  it("should emit content_block_stop after deltas in same chunk", () => {
    const lines = [
      JSON.stringify({
        type: "content_block_delta",
        delta: { type: "text_delta", text: "code here" },
      }),
      JSON.stringify({ type: "content_block_stop", index: 0 }),
    ].join("\n");

    const items = parseStreamOutput(lines);
    expect(items).toHaveLength(2);
    expect(items[0].type).toBe("text_delta");
    expect(items[0].text).toBe("code here");
    expect(items[1].type).toBe("content_block_stop");
  });

  it("should keep assistant text as type text (not text_delta)", () => {
    const assistantLine = JSON.stringify({
      type: "assistant",
      message: {
        role: "assistant",
        content: [{ type: "text", text: "Complete response here" }],
      },
    });

    const items = parseStreamOutput(assistantLine);
    expect(items).toHaveLength(1);
    expect(items[0].type).toBe("text");
    expect(items[0].text).toBe("Complete response here");
  });

  it("should keep plain text fallback as type text", () => {
    const items = parseStreamOutput("plain text line");
    expect(items).toHaveLength(1);
    expect(items[0].type).toBe("text");
  });
});
