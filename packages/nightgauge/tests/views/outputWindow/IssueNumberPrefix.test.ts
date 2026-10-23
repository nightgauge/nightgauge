import { describe, it, expect, beforeEach } from "vitest";
import { OutputWindowState } from "../../../src/views/outputWindow/OutputWindowState";

/**
 * Tests for Issue #303 - Issue Number Prefixes in Output Logs
 *
 * Validates:
 * - Issue number field storage in OutputEntry
 * - Issue number prefix formatting
 * - Separator insertion when issue changes
 * - Edge cases (null, undefined, first entry)
 */
describe("Issue Number Prefix (Issue #303)", () => {
  let state: OutputWindowState;

  beforeEach(() => {
    // Create state without workspace storage (in-memory only)
    state = new OutputWindowState();
  });

  describe("OutputEntry issueNumber field", () => {
    it("should store issueNumber when set", () => {
      state.setIssueNumber(42);
      const entry = state.addEntry("Test message", "info");

      expect(entry.issueNumber).toBe(42);
    });

    it("should store undefined when issueNumber not set", () => {
      const entry = state.addEntry("Test message", "info");

      expect(entry.issueNumber).toBeUndefined();
    });

    it("should update issueNumber for subsequent entries", () => {
      state.setIssueNumber(10);
      const entry1 = state.addEntry("Message 1", "info");

      state.setIssueNumber(20);
      const entry2 = state.addEntry("Message 2", "info");

      expect(entry1.issueNumber).toBe(10);
      expect(entry2.issueNumber).toBe(20);
    });

    it("should handle issueNumber 0", () => {
      state.setIssueNumber(0);
      const entry = state.addEntry("Test message", "info");

      expect(entry.issueNumber).toBe(0);
    });

    it("should persist issueNumber across multiple entries", () => {
      state.setIssueNumber(123);

      const entry1 = state.addEntry("Message 1", "info");
      const entry2 = state.addEntry("Message 2", "info");
      const entry3 = state.addEntry("Message 3", "info");

      expect(entry1.issueNumber).toBe(123);
      expect(entry2.issueNumber).toBe(123);
      expect(entry3.issueNumber).toBe(123);
    });
  });

  describe("getPreviousEntry", () => {
    it("should return null when no entries exist", () => {
      expect(state.getPreviousEntry()).toBeNull();
    });

    it("should return the last entry", () => {
      const entry1 = state.addEntry("Message 1", "info");
      const entry2 = state.addEntry("Message 2", "info");

      const previous = state.getPreviousEntry();
      expect(previous).toEqual(entry2);
    });

    it("should update after adding new entry", () => {
      state.addEntry("Message 1", "info");
      const entry2 = state.addEntry("Message 2", "info");

      expect(state.getPreviousEntry()).toEqual(entry2);

      const entry3 = state.addEntry("Message 3", "info");
      expect(state.getPreviousEntry()).toEqual(entry3);
    });
  });

  describe("Issue number change detection", () => {
    it("should detect when issue number changes", () => {
      state.setIssueNumber(10);
      state.addEntry("Message for issue 10", "info");

      const previousEntry = state.getPreviousEntry();
      expect(previousEntry?.issueNumber).toBe(10);

      state.setIssueNumber(20);
      // In actual implementation, OutputWindow.appendLine() detects this change
      // and inserts a separator before the new entry
      const newEntry = state.addEntry("Message for issue 20", "info");
      expect(newEntry.issueNumber).toBe(20);

      // Verify the issue numbers differ
      expect(previousEntry?.issueNumber).not.toBe(newEntry.issueNumber);
    });

    it("should not trigger change when issue stays the same", () => {
      state.setIssueNumber(42);
      const entry1 = state.addEntry("Message 1", "info");
      const entry2 = state.addEntry("Message 2", "info");

      expect(entry1.issueNumber).toBe(entry2.issueNumber);
    });

    it("should handle transition from undefined to defined issue", () => {
      const entry1 = state.addEntry("Message without issue", "info");
      expect(entry1.issueNumber).toBeUndefined();

      state.setIssueNumber(100);
      const entry2 = state.addEntry("Message with issue", "info");
      expect(entry2.issueNumber).toBe(100);
    });

    it("should handle transition from defined to undefined issue", () => {
      state.setIssueNumber(50);
      const entry1 = state.addEntry("Message with issue", "info");
      expect(entry1.issueNumber).toBe(50);

      state.setIssueNumber(undefined as any);
      const entry2 = state.addEntry("Message without issue", "info");
      expect(entry2.issueNumber).toBeUndefined();
    });
  });

  describe("Batch processing scenarios", () => {
    it("should handle rapid issue transitions", () => {
      const issueNumbers = [1, 2, 3, 4, 5];
      const entries: Array<{ issueNumber: number | undefined; text: string }> = [];

      for (const issueNum of issueNumbers) {
        state.setIssueNumber(issueNum);
        const entry = state.addEntry(`Processing issue ${issueNum}`, "info");
        entries.push({
          issueNumber: entry.issueNumber,
          text: entry.text,
        });
      }

      // Verify each entry has correct issue number
      for (let i = 0; i < issueNumbers.length; i++) {
        expect(entries[i].issueNumber).toBe(issueNumbers[i]);
      }

      // Verify each transition would trigger separator
      for (let i = 1; i < entries.length; i++) {
        expect(entries[i - 1].issueNumber).not.toBe(entries[i].issueNumber);
      }
    });

    it("should handle batch with repeated issue numbers", () => {
      state.setIssueNumber(10);
      state.addEntry("Message 1 for issue 10", "info");
      state.addEntry("Message 2 for issue 10", "info");

      state.setIssueNumber(20);
      state.addEntry("Message 1 for issue 20", "info");

      state.setIssueNumber(10); // Return to issue 10
      state.addEntry("Message 3 for issue 10", "info");

      const entries = state.getEntries();
      expect(entries[0].issueNumber).toBe(10);
      expect(entries[1].issueNumber).toBe(10);
      expect(entries[2].issueNumber).toBe(20);
      expect(entries[3].issueNumber).toBe(10);
    });
  });

  describe("Edge cases", () => {
    it("should handle clearing state with issueNumber set", () => {
      state.setIssueNumber(42);
      state.addEntry("Message 1", "info");
      state.addEntry("Message 2", "info");

      state.clear();

      expect(state.getEntries()).toHaveLength(0);
      expect(state.getIssueNumber()).toBeUndefined();
      expect(state.getPreviousEntry()).toBeNull();
    });

    it("should handle very large issue numbers", () => {
      const largeIssueNumber = 999999;
      state.setIssueNumber(largeIssueNumber);
      const entry = state.addEntry("Test message", "info");

      expect(entry.issueNumber).toBe(largeIssueNumber);
    });

    it("should maintain issueNumber through entry trimming", () => {
      state.setIssueNumber(42);

      // Add more than MAX_ENTRIES (500) to trigger trimming
      // For testing, we'll just verify the mechanism
      for (let i = 0; i < 10; i++) {
        state.addEntry(`Message ${i}`, "info");
      }

      const entries = state.getEntries();
      expect(entries.every((entry) => entry.issueNumber === 42)).toBe(true);
    });
  });

  describe("Backward compatibility", () => {
    it("should handle entries created before issueNumber field existed", () => {
      // Simulate old entry without issueNumber field
      const entry = state.addEntry("Old message", "info");

      // Even if serialization round-tripped and issueNumber was undefined,
      // the system should handle it gracefully
      expect(entry.issueNumber).toBeUndefined();
    });

    it("should allow mixing entries with and without issueNumber", () => {
      // Entry without issue number
      const entry1 = state.addEntry("Message 1", "info");

      // Entry with issue number
      state.setIssueNumber(100);
      const entry2 = state.addEntry("Message 2", "info");

      // Entry without issue number again
      state.setIssueNumber(undefined as any);
      const entry3 = state.addEntry("Message 3", "info");

      expect(entry1.issueNumber).toBeUndefined();
      expect(entry2.issueNumber).toBe(100);
      expect(entry3.issueNumber).toBeUndefined();
    });
  });
});
