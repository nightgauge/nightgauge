/**
 * reasoningDetector.test.ts - Tests for reasoning line classification (Issue #796)
 *
 * Verifies that the isReasoningLine() heuristic correctly identifies
 * agent reasoning lines while never collapsing substantive content.
 */

import { describe, it, expect } from "vitest";
import { isReasoningLine } from "../../../src/views/outputWindow/reasoningDetector";

describe("isReasoningLine (Issue #796)", () => {
  describe("known reasoning patterns", () => {
    it('should detect "Let me..." lines', () => {
      expect(isReasoningLine("Let me read the file.")).toBe(true);
      expect(isReasoningLine("Let me check the tests.")).toBe(true);
      expect(isReasoningLine("Now let me look at the code.")).toBe(true);
    });

    it('should detect "I\'ll..." lines', () => {
      expect(isReasoningLine("I'll check the tests next.")).toBe(true);
      expect(isReasoningLine("Now I'll look at the implementation.")).toBe(true);
    });

    it('should detect "I will..." lines', () => {
      expect(isReasoningLine("I will check the tests.")).toBe(true);
    });

    it("should detect acknowledgments", () => {
      expect(isReasoningLine("Good.")).toBe(true);
      expect(isReasoningLine("Perfect.")).toBe(true);
      expect(isReasoningLine("Excellent.")).toBe(true);
      expect(isReasoningLine("Great.")).toBe(true);
      expect(isReasoningLine("OK.")).toBe(true);
      expect(isReasoningLine("Alright.")).toBe(true);
      expect(isReasoningLine("Done.")).toBe(true);
      expect(isReasoningLine("Right.")).toBe(true);
      expect(isReasoningLine("Understood.")).toBe(true);
    });

    it("should detect observation starters", () => {
      expect(isReasoningLine("Looking at the test results")).toBe(true);
      expect(isReasoningLine("Checking the configuration")).toBe(true);
      expect(isReasoningLine("Reading the plan file")).toBe(true);
      expect(isReasoningLine("Examining the output")).toBe(true);
    });

    it("should detect sequence words", () => {
      expect(isReasoningLine("First, I need to read the file.")).toBe(true);
      expect(isReasoningLine("Next, check the tests.")).toBe(true);
      expect(isReasoningLine("Now, implement the changes.")).toBe(true);
      expect(isReasoningLine("Then, write the tests.")).toBe(true);
      expect(isReasoningLine("Finally, commit the changes.")).toBe(true);
    });

    it('should detect "This/That/These" starters', () => {
      expect(isReasoningLine("This looks correct.")).toBe(true);
      expect(isReasoningLine("That should work.")).toBe(true);
      expect(isReasoningLine("These are the files.")).toBe(true);
    });

    it("should detect self-referential patterns", () => {
      expect(isReasoningLine("I can see the issue.")).toBe(true);
      expect(isReasoningLine("I need to fix this.")).toBe(true);
      expect(isReasoningLine("I should update the test.")).toBe(true);
      expect(isReasoningLine("I want to verify this.")).toBe(true);
      expect(isReasoningLine("I see the problem now.")).toBe(true);
      expect(isReasoningLine("I notice the pattern.")).toBe(true);
      expect(isReasoningLine("I found the issue.")).toBe(true);
    });

    it("should detect short transitional lines ending with colon", () => {
      expect(isReasoningLine("Here is the plan:")).toBe(true);
      expect(isReasoningLine("The changes needed:")).toBe(true);
    });
  });

  describe("substantive content preserved", () => {
    it("should NOT classify markdown headers as reasoning", () => {
      expect(isReasoningLine("# Section Header")).toBe(false);
      expect(isReasoningLine("## Subsection")).toBe(false);
      expect(isReasoningLine("### Details")).toBe(false);
    });

    it("should NOT classify markdown tables as reasoning", () => {
      expect(isReasoningLine("| Column 1 | Column 2 |")).toBe(false);
      expect(isReasoningLine("| --- | --- |")).toBe(false);
    });

    it("should NOT classify markdown checkboxes as reasoning", () => {
      expect(isReasoningLine("- [ ] Task to do")).toBe(false);
      expect(isReasoningLine("- [x] Completed task")).toBe(false);
    });

    it("should NOT classify code fences as reasoning", () => {
      expect(isReasoningLine("```typescript")).toBe(false);
      expect(isReasoningLine("```")).toBe(false);
    });

    it("should NOT classify error/warning lines as reasoning", () => {
      expect(isReasoningLine("Error: something went wrong")).toBe(false);
      expect(isReasoningLine("Warning: deprecated API")).toBe(false);
    });

    it("should NOT classify status icons as reasoning", () => {
      expect(isReasoningLine("✓ feature-dev completed")).toBe(false);
      expect(isReasoningLine("✗ feature-dev failed")).toBe(false);
    });

    it("should NOT classify stage status messages as reasoning", () => {
      expect(isReasoningLine("Starting feature-dev...")).toBe(false);
      expect(isReasoningLine("Pipeline completed successfully")).toBe(false);
      expect(isReasoningLine("Stage skipped")).toBe(false);
    });

    it("should NOT classify separator lines as reasoning", () => {
      expect(isReasoningLine("═".repeat(60))).toBe(false);
    });

    it("should NOT classify skillRunner metadata as reasoning", () => {
      expect(isReasoningLine("[skillRunner] Stage running")).toBe(false);
    });

    it("should NOT classify lines with file paths as reasoning", () => {
      expect(isReasoningLine("Let me check /src/index.ts")).toBe(false);
      expect(isReasoningLine("Reading file.json")).toBe(false);
    });

    it("should NOT classify lines with URLs as reasoning", () => {
      expect(isReasoningLine("See https://example.com for details")).toBe(false);
    });

    it("should NOT classify long lines as reasoning", () => {
      const longLine = "Let me " + "x".repeat(120);
      expect(isReasoningLine(longLine)).toBe(false);
    });

    it("should NOT classify JSON/object notation as reasoning", () => {
      expect(isReasoningLine('{ "key": "value" }')).toBe(false);
      expect(isReasoningLine("[1, 2, 3]")).toBe(false);
    });

    it("should NOT classify numbered lists as reasoning", () => {
      expect(isReasoningLine("1. First step in the plan")).toBe(false);
      expect(isReasoningLine("2. Second step")).toBe(false);
    });

    it("should NOT classify bullet points as reasoning", () => {
      expect(isReasoningLine("- Important item")).toBe(false);
      expect(isReasoningLine("* Another item")).toBe(false);
    });

    it("should NOT classify acceptance criteria lines as reasoning", () => {
      expect(isReasoningLine("This meets the acceptance criteria")).toBe(false);
      expect(isReasoningLine("Requirement fulfilled")).toBe(false);
    });

    it("should NOT classify lines with issue references as reasoning", () => {
      expect(isReasoningLine("This fixes #796")).toBe(false);
    });
  });

  describe("edge cases", () => {
    it("should NOT classify empty lines as reasoning", () => {
      expect(isReasoningLine("")).toBe(false);
      expect(isReasoningLine("   ")).toBe(false);
      expect(isReasoningLine("\t")).toBe(false);
    });

    it("should NOT classify unknown short lines as reasoning", () => {
      expect(isReasoningLine("Hello world")).toBe(false);
      expect(isReasoningLine("Some random text")).toBe(false);
    });

    it("should handle whitespace padding", () => {
      expect(isReasoningLine("  Good.  ")).toBe(true);
      expect(isReasoningLine("  Let me check.  ")).toBe(true);
    });

    it("should be case-sensitive for acknowledgments", () => {
      // Acknowledgments must start with capital letter
      expect(isReasoningLine("good.")).toBe(false);
      expect(isReasoningLine("perfect.")).toBe(false);
    });

    it("should not match partial acknowledgments", () => {
      // "Good." matches, but "Good work on the feature" does not end with just "."
      expect(isReasoningLine("Good work on the feature")).toBe(false);
    });
  });
});
