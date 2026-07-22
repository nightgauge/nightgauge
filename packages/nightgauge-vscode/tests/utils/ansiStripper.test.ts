/**
 * Tests for ANSI escape code stripping utility
 *
 * Issue #496: Dual-mode output window rendering
 * Interactive mode output may contain ANSI escape codes that need
 * to be stripped for clean display in the webview.
 */

import { describe, it, expect } from "vitest";
import { stripAnsi, hasAnsi } from "../../src/utils/ansiStripper";

describe("ansiStripper", () => {
  describe("stripAnsi", () => {
    it("should return empty string for empty input", () => {
      expect(stripAnsi("")).toBe("");
    });

    it("should return empty string for null/undefined input", () => {
      expect(stripAnsi(null as unknown as string)).toBe("");
      expect(stripAnsi(undefined as unknown as string)).toBe("");
    });

    it("should return text unchanged if no ANSI codes present", () => {
      expect(stripAnsi("Hello, World!")).toBe("Hello, World!");
      expect(stripAnsi("Line 1\nLine 2")).toBe("Line 1\nLine 2");
      expect(stripAnsi("Special chars: !@#$%^&*()")).toBe("Special chars: !@#$%^&*()");
    });

    it("should strip basic color codes", () => {
      // Red text
      expect(stripAnsi("\x1b[31mRed text\x1b[0m")).toBe("Red text");
      // Green text
      expect(stripAnsi("\x1b[32mGreen text\x1b[0m")).toBe("Green text");
      // Blue text
      expect(stripAnsi("\x1b[34mBlue text\x1b[0m")).toBe("Blue text");
    });

    it("should strip bold, italic, underline codes", () => {
      // Bold
      expect(stripAnsi("\x1b[1mBold\x1b[0m")).toBe("Bold");
      // Italic
      expect(stripAnsi("\x1b[3mItalic\x1b[0m")).toBe("Italic");
      // Underline
      expect(stripAnsi("\x1b[4mUnderline\x1b[0m")).toBe("Underline");
    });

    it("should strip combined style codes", () => {
      // Bold red
      expect(stripAnsi("\x1b[1;31mBold Red\x1b[0m")).toBe("Bold Red");
      // Background color
      expect(stripAnsi("\x1b[44;37mWhite on Blue\x1b[0m")).toBe("White on Blue");
    });

    it("should strip 256-color codes", () => {
      // Foreground 256-color
      expect(stripAnsi("\x1b[38;5;196mBright Red\x1b[0m")).toBe("Bright Red");
      // Background 256-color
      expect(stripAnsi("\x1b[48;5;21mBlue Background\x1b[0m")).toBe("Blue Background");
    });

    it("should strip RGB/TrueColor codes", () => {
      // RGB foreground
      expect(stripAnsi("\x1b[38;2;255;0;0mRGB Red\x1b[0m")).toBe("RGB Red");
      // RGB background
      expect(stripAnsi("\x1b[48;2;0;0;255mRGB Blue BG\x1b[0m")).toBe("RGB Blue BG");
    });

    it("should strip cursor movement codes", () => {
      // Cursor up
      expect(stripAnsi("\x1b[5AUp 5 lines")).toBe("Up 5 lines");
      // Cursor down
      expect(stripAnsi("\x1b[3BDown 3 lines")).toBe("Down 3 lines");
      // Cursor position
      expect(stripAnsi("\x1b[10;20HAt position")).toBe("At position");
    });

    it("should strip screen clear codes", () => {
      // Clear screen
      expect(stripAnsi("\x1b[2JCleared")).toBe("Cleared");
      // Clear line
      expect(stripAnsi("\x1b[KLine cleared")).toBe("Line cleared");
    });

    it("should handle multiple ANSI codes in one string", () => {
      const input = "\x1b[31mRed\x1b[0m normal \x1b[32mGreen\x1b[0m \x1b[1mBold\x1b[0m";
      expect(stripAnsi(input)).toBe("Red normal Green Bold");
    });

    it("should handle nested ANSI codes", () => {
      // Bold within color
      expect(stripAnsi("\x1b[31m\x1b[1mBold Red\x1b[0m\x1b[0m")).toBe("Bold Red");
    });

    it("should handle ANSI codes with no text between", () => {
      expect(stripAnsi("\x1b[31m\x1b[0m")).toBe("");
    });

    it("should handle realistic Claude CLI output", () => {
      // Simulated Claude CLI spinner output
      const spinnerOutput = "\x1b[?25l\x1b[2K\x1b[1G⠋ Thinking...\x1b[?25h";
      expect(stripAnsi(spinnerOutput)).toBe("⠋ Thinking...");

      // Progress indicator
      const progress = "\x1b[32m✓\x1b[0m Task completed";
      expect(stripAnsi(progress)).toBe("✓ Task completed");
    });

    it("should strip OSC sequences (terminal title)", () => {
      // Set terminal title: ESC ] 0 ; title BEL
      const titleSet = "\x1b]0;My Terminal Title\x07Some text";
      expect(stripAnsi(titleSet)).toBe("Some text");
    });

    it("should strip OSC hyperlink sequences", () => {
      // Hyperlink: ESC ] 8 ; ; url BEL text ESC ] 8 ; ; BEL
      const hyperlink = "\x1b]8;;https://example.com\x07Click here\x1b]8;;\x07";
      expect(stripAnsi(hyperlink)).toBe("Click here");
    });

    it("should strip OSC sequences terminated with ST (ESC backslash)", () => {
      // Some terminals use ESC \ (ST) instead of BEL to terminate OSC
      const oscST = "\x1b]0;Title\x1b\\Content after";
      expect(stripAnsi(oscST)).toBe("Content after");
    });

    it("should handle mixed OSC and CSI sequences", () => {
      const mixed = "\x1b]0;Building\x07\x1b[32m✓\x1b[0m Build complete";
      expect(stripAnsi(mixed)).toBe("✓ Build complete");
    });
  });

  describe("hasAnsi", () => {
    it("should return false for empty input", () => {
      expect(hasAnsi("")).toBe(false);
    });

    it("should return false for null/undefined input", () => {
      expect(hasAnsi(null as unknown as string)).toBe(false);
      expect(hasAnsi(undefined as unknown as string)).toBe(false);
    });

    it("should return false for plain text", () => {
      expect(hasAnsi("Hello, World!")).toBe(false);
      expect(hasAnsi("No ANSI codes here")).toBe(false);
    });

    it("should return true for text with color codes", () => {
      expect(hasAnsi("\x1b[31mRed\x1b[0m")).toBe(true);
      expect(hasAnsi("Some \x1b[32mgreen\x1b[0m text")).toBe(true);
    });

    it("should return true for text with cursor codes", () => {
      expect(hasAnsi("\x1b[5A")).toBe(true);
      expect(hasAnsi("\x1b[2J")).toBe(true);
    });
  });
});
