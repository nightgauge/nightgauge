/**
 * Tests for dialog utilities
 */

import { describe, it, expect } from "vitest";
import { formatIssueStatusSummary, type IssueWarningData } from "../../src/utils/dialogs";

describe("dialogs", () => {
  describe("formatIssueStatusSummary", () => {
    it("formats single issue with In Progress status", () => {
      const issues: IssueWarningData[] = [
        {
          number: 42,
          title: "Add user authentication",
          status: "in-progress",
          url: "https://github.com/org/repo/issues/42",
        },
      ];

      const result = formatIssueStatusSummary(issues);

      expect(result).toContain("#42");
      expect(result).toContain("$(sync~spin)");
      expect(result).toContain("Add user authentication");
    });

    it("formats single issue with In Review status", () => {
      const issues: IssueWarningData[] = [
        {
          number: 101,
          title: "Fix login bug",
          status: "in-review",
          url: "https://github.com/org/repo/issues/101",
        },
      ];

      const result = formatIssueStatusSummary(issues);

      expect(result).toContain("#101");
      expect(result).toContain("$(git-pull-request)");
      expect(result).toContain("Fix login bug");
    });

    it("formats multiple issues", () => {
      const issues: IssueWarningData[] = [
        {
          number: 42,
          title: "Add user authentication",
          status: "in-progress",
          url: "https://github.com/org/repo/issues/42",
        },
        {
          number: 101,
          title: "Fix login bug",
          status: "in-review",
          url: "https://github.com/org/repo/issues/101",
        },
      ];

      const result = formatIssueStatusSummary(issues);

      expect(result).toContain("#42");
      expect(result).toContain("#101");
      expect(result).toContain("$(sync~spin)");
      expect(result).toContain("$(git-pull-request)");
    });

    it("truncates long titles", () => {
      const issues: IssueWarningData[] = [
        {
          number: 42,
          title: "This is a very long issue title that exceeds fifty characters",
          status: "in-progress",
          url: "https://github.com/org/repo/issues/42",
        },
      ];

      const result = formatIssueStatusSummary(issues);

      expect(result).toContain("...");
      expect(result.length).toBeLessThan(100); // Reasonable upper bound
    });

    it("includes PR info when available", () => {
      const issues: IssueWarningData[] = [
        {
          number: 101,
          title: "Fix login bug",
          status: "in-review",
          url: "https://github.com/org/repo/issues/101",
          prInfo: {
            number: 123,
            url: "https://github.com/org/repo/pull/123",
            title: "Fix: login validation",
          },
        },
      ];

      const result = formatIssueStatusSummary(issues);

      // PR info doesn't appear in summary (appears in buttons)
      expect(result).toContain("#101");
      expect(result).toContain("Fix login bug");
    });
  });
});
