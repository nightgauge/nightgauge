/**
 * OutputWindowNormalize.test.ts - Tests for output normalization (Issue #794)
 *
 * Verifies that consecutive blank lines are collapsed, duplicate separators
 * are prevented, and intentional formatting is preserved.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { OutputWindow } from "../../../src/views/outputWindow/OutputWindow";

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

describe("OutputWindow Output Normalization (Issue #794)", () => {
  let outputWindow: OutputWindow;

  beforeEach(() => {
    outputWindow = new OutputWindow(createMockExtensionUri(), createMockWorkspaceState());
  });

  describe("consecutive blank line collapsing", () => {
    it("should collapse consecutive blank-only entries to one", () => {
      outputWindow.appendLine("Line 1", "info");
      outputWindow.appendLine("", "info"); // first blank — kept
      outputWindow.appendLine("", "info"); // second blank — suppressed
      outputWindow.appendLine("", "info"); // third blank — suppressed
      outputWindow.appendLine("Line 2", "info");

      const entries = outputWindow.getState().getEntries();
      const texts = entries.map((e) => e.text);

      expect(texts).toEqual(["Line 1", "", "Line 2"]);
    });

    it("should allow single blank lines between content", () => {
      outputWindow.appendLine("Section 1", "info");
      outputWindow.appendLine("", "info");
      outputWindow.appendLine("Section 2", "info");
      outputWindow.appendLine("", "info");
      outputWindow.appendLine("Section 3", "info");

      const entries = outputWindow.getState().getEntries();
      const texts = entries.map((e) => e.text);

      expect(texts).toEqual(["Section 1", "", "Section 2", "", "Section 3"]);
    });

    it("should suppress leading blank entries", () => {
      // lastLineWasBlank starts as false, so the first blank IS kept.
      // But two leading blanks should still collapse to one.
      outputWindow.appendLine("", "info");
      outputWindow.appendLine("", "info");
      outputWindow.appendLine("Content", "info");

      const entries = outputWindow.getState().getEntries();
      const texts = entries.map((e) => e.text);

      expect(texts).toEqual(["", "Content"]);
    });

    it("should collapse multi-newline text within a single entry", () => {
      outputWindow.appendLine("Line A\n\n\n\nLine B", "info");

      const entries = outputWindow.getState().getEntries();
      // Internal runs of 3+ newlines are collapsed to \n\n
      expect(entries[0].text).toBe("Line A\n\nLine B");
    });

    it("should reset blank tracking after clear()", () => {
      outputWindow.appendLine("", "info");
      outputWindow.clear();
      outputWindow.appendLine("", "info"); // should be kept (reset)
      outputWindow.appendLine("", "info"); // should be suppressed

      const entries = outputWindow.getState().getEntries();
      expect(entries).toHaveLength(1);
    });
  });

  describe("separator deduplication", () => {
    it("should not insert duplicate separator lines", () => {
      const state = outputWindow.getState();
      state.setIssueNumber(100);
      outputWindow.appendLine("First issue output", "info");

      // Change issue number to trigger separator
      state.setIssueNumber(200);
      outputWindow.appendLine("Second issue output", "info");

      // Change again to trigger another separator
      state.setIssueNumber(300);
      outputWindow.appendLine("Third issue output", "info");

      const entries = state.getEntries();
      const separatorEntries = entries.filter((e) => e.text.startsWith("═"));

      // Each issue transition should produce exactly one separator
      expect(separatorEntries).toHaveLength(2);
    });
  });

  describe("intentional formatting preserved", () => {
    it("should preserve single blank line separators in markdown", () => {
      outputWindow.appendLine("## Section Header", "info");
      outputWindow.appendLine("", "info");
      outputWindow.appendLine("Paragraph text here.", "info");

      const entries = outputWindow.getState().getEntries();
      const texts = entries.map((e) => e.text);

      expect(texts).toEqual(["## Section Header", "", "Paragraph text here."]);
    });

    it("should preserve double newlines within multi-line content", () => {
      outputWindow.appendLine("Part 1\n\nPart 2", "info");

      const entries = outputWindow.getState().getEntries();
      expect(entries[0].text).toBe("Part 1\n\nPart 2");
    });
  });

  describe("whitespace-only entries", () => {
    it("should treat whitespace-only entries as blank", () => {
      outputWindow.appendLine("Content", "info");
      outputWindow.appendLine("   ", "info"); // spaces only — treated as blank
      outputWindow.appendLine("\t", "info"); // tab only — treated as blank (suppressed)
      outputWindow.appendLine("More content", "info");

      const entries = outputWindow.getState().getEntries();
      const texts = entries.map((e) => e.text);

      expect(texts).toEqual(["Content", "   ", "More content"]);
    });
  });
});
