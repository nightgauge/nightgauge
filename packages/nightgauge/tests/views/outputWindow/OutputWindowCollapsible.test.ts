/**
 * OutputWindowCollapsible.test.ts - Tests for content collapsing behavior
 *
 * Verifies that large content is properly collapsed in the Output Window,
 * including:
 * - Multi-line content over the 50-line threshold
 * - Single-line content over the 2000-character threshold
 * - Content with literal \n escape sequences (from Claude stream-json)
 * - Code blocks at the lower 8-line threshold
 * - Interaction between collapsing and issue prefixing
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { OutputWindow } from "../../../src/views/outputWindow/OutputWindow";
import {
  CHAR_COLLAPSE_THRESHOLD,
  CODE_COLLAPSE_THRESHOLD,
} from "../../../src/views/outputWindow/contentFormatter";

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

describe("OutputWindow Content Collapsing", () => {
  let outputWindow: OutputWindow;

  beforeEach(() => {
    outputWindow = new OutputWindow(createMockExtensionUri(), createMockWorkspaceState());
  });

  describe("multi-line content collapsing (>50 lines)", () => {
    it("should collapse content with more than 50 lines", () => {
      const lines = Array(60)
        .fill(0)
        .map((_, i) => `source line ${i + 1}`)
        .join("\n");

      outputWindow.appendLine(lines, "info");
      const entries = outputWindow.getState().getEntries();

      expect(entries).toHaveLength(1);
      expect(entries[0].collapsible).toBe(true);
      expect(entries[0].collapsed).toBe(true);
      expect(entries[0].details).toBe(lines);
      // Summary should be first 5 lines + "..."
      expect(entries[0].text).toContain("source line 1");
      expect(entries[0].text).toContain("...");
    });

    it("should NOT collapse content with 50 or fewer lines", () => {
      const lines = Array(50)
        .fill(0)
        .map((_, i) => `line ${i + 1}`)
        .join("\n");

      outputWindow.appendLine(lines, "info");
      const entries = outputWindow.getState().getEntries();

      expect(entries).toHaveLength(1);
      // Under 50 lines AND under char threshold — not collapsed
      expect(entries[0].collapsible).toBeUndefined();
    });
  });

  describe("character-count collapsing (>2000 chars)", () => {
    it("should collapse single-line content exceeding char threshold", () => {
      // Simulates a 5KB file arriving as a single line
      const longLine = "x".repeat(5000);

      outputWindow.appendLine(longLine, "info");
      const entries = outputWindow.getState().getEntries();

      expect(entries).toHaveLength(1);
      expect(entries[0].collapsible).toBe(true);
      expect(entries[0].collapsed).toBe(true);
      expect(entries[0].details).toBe(longLine);
      expect(entries[0].text).toContain("KB content)");
    });

    it("should collapse few-line content exceeding char threshold", () => {
      // 3 lines of 1000 chars each — under 50 lines but over 2000 chars
      const longContent = Array(3).fill("a".repeat(1000)).join("\n");

      outputWindow.appendLine(longContent, "info");
      const entries = outputWindow.getState().getEntries();

      expect(entries).toHaveLength(1);
      expect(entries[0].collapsible).toBe(true);
      expect(entries[0].collapsed).toBe(true);
    });

    it("should NOT collapse short content under both thresholds", () => {
      const shortContent = "A short message about pipeline progress.";

      outputWindow.appendLine(shortContent, "info");
      const entries = outputWindow.getState().getEntries();

      expect(entries).toHaveLength(1);
      expect(entries[0].collapsible).toBeUndefined();
      expect(entries[0].text).toBe(shortContent);
    });
  });

  describe("literal \\n normalization", () => {
    it("should convert literal \\n to real newlines in long content", () => {
      // Simulates Claude stream-json double-escaping: 200 lines joined by literal \n
      const escapedContent = Array(200).fill("const x = 1;").join("\\n");

      // Before normalization: single line, over char threshold
      expect(escapedContent.split("\n").length).toBe(1);
      expect(escapedContent.length).toBeGreaterThan(500);

      outputWindow.appendLine(escapedContent, "info");
      const entries = outputWindow.getState().getEntries();

      expect(entries).toHaveLength(1);
      expect(entries[0].collapsible).toBe(true);
      expect(entries[0].collapsed).toBe(true);
      // After normalization, details should have real newlines
      expect(entries[0].details).toContain("\n");
    });

    it("should NOT normalize short content even with literal \\n", () => {
      // Short content with literal \n should be left alone (under 500 char guard)
      const shortEscaped = "line1\\nline2\\nline3";

      outputWindow.appendLine(shortEscaped, "info");
      const entries = outputWindow.getState().getEntries();

      expect(entries).toHaveLength(1);
      // Should preserve literal \n for short content
      expect(entries[0].text).toContain("\\n");
    });
  });

  describe("realistic pipeline scenarios", () => {
    it("should collapse a 45KB documentation file read output", () => {
      // Simulates ARCHITECTURE.md content with escaped newlines (as seen in #789 log)
      const docContent = Array(500)
        .fill(
          "## Section\\n\\nThis is documentation about the architecture.\\n- Point 1\\n- Point 2"
        )
        .join("\\n\\n");

      outputWindow.appendLine(docContent, "info", "feature-planning");
      const entries = outputWindow.getState().getEntries();

      expect(entries).toHaveLength(1);
      expect(entries[0].collapsible).toBe(true);
      expect(entries[0].stage).toBe("feature-planning");
    });

    it("should collapse a large source file read output", () => {
      // Simulates a TypeScript source file with line numbers (as seen in #789 log)
      const sourceLines = Array(800)
        .fill(0)
        .map((_, i) => `  ${i + 1}→  const value${i} = computeSomething(${i});`)
        .join("\\n");

      outputWindow.appendLine(sourceLines, "info", "feature-dev");
      const entries = outputWindow.getState().getEntries();

      expect(entries).toHaveLength(1);
      expect(entries[0].collapsible).toBe(true);
    });

    it("should collapse content with issue prefix", () => {
      // Set issue number (adds [#789] prefix)
      outputWindow.getState().setIssueNumber(789);

      const longContent = "x".repeat(5000);
      outputWindow.appendLine(longContent, "info");
      const entries = outputWindow.getState().getEntries();

      // Should still collapse even with the issue prefix added
      expect(entries).toHaveLength(1);
      expect(entries[0].collapsible).toBe(true);
      expect(entries[0].text).toContain("KB content)");
    });

    it("should handle mixed collapsible and non-collapsible entries", () => {
      outputWindow.appendLine("Starting feature-planning...", "info");
      outputWindow.appendLine("Reading ARCHITECTURE.md...", "info");

      // Large file content
      const bigContent = "a".repeat(10000);
      outputWindow.appendLine(bigContent, "info", "feature-planning");

      outputWindow.appendLine("Plan created successfully.", "info");

      const entries = outputWindow.getState().getEntries();
      expect(entries).toHaveLength(4);

      // First two entries: not collapsed
      expect(entries[0].collapsible).toBeUndefined();
      expect(entries[1].collapsible).toBeUndefined();

      // Third entry: collapsed
      expect(entries[2].collapsible).toBe(true);
      expect(entries[2].collapsed).toBe(true);

      // Fourth entry: not collapsed
      expect(entries[3].collapsible).toBeUndefined();
    });
  });

  describe("code block collapsing (>8 lines)", () => {
    it("should collapse code blocks at lower threshold", () => {
      // Generate a TypeScript code block with >8 lines
      const codeBlock = [
        "export class OutputWindow {",
        "  private panel: vscode.WebviewPanel | undefined;",
        "  private disposables: vscode.Disposable[] = [];",
        "",
        "  constructor(private readonly extensionUri: vscode.Uri) {",
        "    this.config = { autoOpen: true };",
        "  }",
        "",
        "  show(): void {",
        "    if (this.panel) {",
        "      this.panel.reveal();",
        "      return;",
        "    }",
        "  }",
        "}",
      ].join("\n");

      outputWindow.appendLine(codeBlock, "info");
      const entries = outputWindow.getState().getEntries();

      expect(entries).toHaveLength(1);
      expect(entries[0].collapsible).toBe(true);
      expect(entries[0].collapsed).toBe(true);
      expect(entries[0].text).toContain("Code block");
      expect(entries[0].text).toContain("lines");
    });
  });
});
