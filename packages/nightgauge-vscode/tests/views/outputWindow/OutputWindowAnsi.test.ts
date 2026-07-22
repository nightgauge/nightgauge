/**
 * OutputWindowAnsi.test.ts - Tests for ANSI stripping and JSON metadata filtering (Issue #873)
 *
 * Verifies that:
 * 1. ANSI escape codes are stripped from appendLine() input
 * 2. OSC sequences (hyperlinks, title) are stripped
 * 3. Combined ANSI + real content preserves the content
 * 4. Realistic Claude CLI output is cleaned
 */

import { describe, it, expect, beforeEach } from "vitest";
import { vi } from "vitest";
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

describe("OutputWindow ANSI Stripping (Issue #873)", () => {
  let outputWindow: OutputWindow;

  beforeEach(() => {
    outputWindow = new OutputWindow(createMockExtensionUri(), createMockWorkspaceState());
  });

  describe("basic ANSI code stripping in appendLine", () => {
    it("should strip color codes from output", () => {
      outputWindow.appendLine("\x1b[31mRed text\x1b[0m", "info");

      const entries = outputWindow.getState().getEntries();
      expect(entries[0].text).toBe("Red text");
    });

    it("should strip bold/italic codes from output", () => {
      outputWindow.appendLine("\x1b[1mBold\x1b[0m \x1b[3mItalic\x1b[0m", "info");

      const entries = outputWindow.getState().getEntries();
      expect(entries[0].text).toBe("Bold Italic");
    });

    it("should preserve text with no ANSI codes", () => {
      outputWindow.appendLine("Normal text with [brackets]", "info");

      const entries = outputWindow.getState().getEntries();
      expect(entries[0].text).toBe("Normal text with [brackets]");
    });
  });

  describe("OSC sequence stripping in appendLine", () => {
    it("should strip OSC terminal title sequences", () => {
      outputWindow.appendLine("\x1b]0;My Title\x07Output text", "info");

      const entries = outputWindow.getState().getEntries();
      expect(entries[0].text).toBe("Output text");
    });

    it("should strip OSC hyperlink sequences", () => {
      outputWindow.appendLine("\x1b]8;;https://example.com\x07Click\x1b]8;;\x07 here", "info");

      const entries = outputWindow.getState().getEntries();
      expect(entries[0].text).toBe("Click here");
    });
  });

  describe("mixed ANSI + content", () => {
    it("should handle mixed color and OSC sequences", () => {
      outputWindow.appendLine("\x1b]0;Build\x07\x1b[32m✓\x1b[0m Build complete", "info");

      const entries = outputWindow.getState().getEntries();
      expect(entries[0].text).toBe("✓ Build complete");
    });

    it("should handle realistic Claude CLI spinner output", () => {
      outputWindow.appendLine("\x1b[?25l\x1b[2K\x1b[1G⠋ Thinking...\x1b[?25h", "info");

      const entries = outputWindow.getState().getEntries();
      expect(entries[0].text).toBe("⠋ Thinking...");
    });

    it("should strip ANSI from collapsible entry details", () => {
      outputWindow.appendLine("Summary", "info", undefined, {
        collapsible: true,
        details: "\x1b[33mYellow detail\x1b[0m",
      });

      const entries = outputWindow.getState().getEntries();
      expect(entries[0].details).toBe("Yellow detail");
    });
  });

  describe("realistic pipeline output cleaning", () => {
    it("should clean colored test pass output", () => {
      outputWindow.appendLine("\x1b[32m✓\x1b[0m 15 tests passed", "info");

      const entries = outputWindow.getState().getEntries();
      expect(entries[0].text).toBe("✓ 15 tests passed");
    });

    it("should clean colored warning output", () => {
      outputWindow.appendLine("\x1b[33m⚠\x1b[0m \x1b[2mDeprecation warning\x1b[0m", "info");

      const entries = outputWindow.getState().getEntries();
      expect(entries[0].text).toBe("⚠ Deprecation warning");
    });

    it("should clean multi-color formatted output", () => {
      outputWindow.appendLine("\x1b[1;31mERROR:\x1b[0m \x1b[37mFile not found\x1b[0m", "info");

      const entries = outputWindow.getState().getEntries();
      expect(entries[0].text).toBe("ERROR: File not found");
    });
  });
});
