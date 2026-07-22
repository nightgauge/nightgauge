import { describe, it, expect } from "vitest";
import { executeQuery, parse, validate, isValid, tokenize } from "../index.js";
import type { QueryableIssue } from "../types.js";

/**
 * Integration tests for the full query pipeline: tokenize → parse → evaluate
 */

/** Create a realistic set of test issues */
function createTestIssues(): QueryableIssue[] {
  return [
    {
      number: 101,
      title: "Fix authentication bug in login flow",
      labels: ["type:bug", "component:auth", "critical"],
      priority: "P0",
      size: "M",
      url: "https://github.com/test/repo/issues/101",
      status: "ready",
      assignee: "alice",
      updatedAt: new Date(Date.now() - 2 * 24 * 60 * 60 * 1000).toISOString(),
      createdAt: "2026-01-10T00:00:00Z",
    },
    {
      number: 102,
      title: "Add dark mode support",
      labels: ["type:feature", "component:ui"],
      priority: "P1",
      size: "L",
      url: "https://github.com/test/repo/issues/102",
      status: "in-progress",
      assignee: "bob",
      updatedAt: new Date(Date.now() - 1 * 24 * 60 * 60 * 1000).toISOString(),
      createdAt: "2026-02-15T00:00:00Z",
    },
    {
      number: 103,
      title: "Refactor API endpoint naming",
      labels: ["type:refactor", "component:api"],
      priority: "P2",
      size: "S",
      url: "https://github.com/test/repo/issues/103",
      status: "ready",
      assignee: "alice",
      updatedAt: new Date(Date.now() - 10 * 24 * 60 * 60 * 1000).toISOString(),
      createdAt: "2026-01-05T00:00:00Z",
    },
    {
      number: 104,
      title: "Update documentation for v2.0",
      labels: ["type:docs"],
      priority: "P3",
      size: "XS",
      url: "https://github.com/test/repo/issues/104",
      status: "done",
      assignee: "charlie",
      updatedAt: new Date(Date.now() - 30 * 24 * 60 * 60 * 1000).toISOString(),
      createdAt: "2025-12-01T00:00:00Z",
    },
    {
      number: 105,
      title: "Performance optimization for dashboard",
      labels: ["type:feature", "component:ui", "performance"],
      priority: "P1",
      size: "XL",
      url: "https://github.com/test/repo/issues/105",
      status: "ready",
      assignee: "bob",
      updatedAt: new Date(Date.now() - 5 * 24 * 60 * 60 * 1000).toISOString(),
      createdAt: "2026-03-01T00:00:00Z",
    },
    {
      number: 106,
      title: "Fix CSS layout on mobile",
      labels: ["type:bug", "component:ui"],
      priority: "P0",
      size: "S",
      url: "https://github.com/test/repo/issues/106",
      status: "in-progress",
      assignee: "alice",
      updatedAt: new Date(Date.now() - 1 * 24 * 60 * 60 * 1000).toISOString(),
      createdAt: "2026-03-15T00:00:00Z",
    },
  ];
}

describe("Full Query Pipeline Integration", () => {
  const issues = createTestIssues();

  describe("acceptance criteria queries", () => {
    it('filters "status:ready AND priority:high OR priority:critical"', () => {
      // Acceptance criteria: precedence should be (status:ready AND priority:high) OR priority:critical
      // Since our fields use P0/P1 etc., test with those
      const result = executeQuery("status:ready AND priority:P0 OR priority:P1", issues);
      // Precedence: (status:ready AND priority:P0) OR priority:P1
      // Matches: #101 (ready AND P0), #102 (P1), #105 (P1)
      expect(result.matchCount).toBe(3);
    });

    it('filters "(status:ready OR status:in-progress) AND priority:P0"', () => {
      const result = executeQuery("(status:ready OR status:in-progress) AND priority:P0", issues);
      // Should match: #101 (ready + P0), #106 (in-progress + P0)
      expect(result.matchCount).toBe(2);
      expect(result.items.map((i) => i.number).sort()).toEqual([101, 106]);
    });

    it("filters by assignee", () => {
      const result = executeQuery("assignee:alice", issues);
      expect(result.matchCount).toBe(3);
      expect(result.items.every((i) => i.assignee === "alice")).toBe(true);
    });

    it("filters by size", () => {
      const result = executeQuery("size:S OR size:XS", issues);
      expect(result.matchCount).toBe(3); // #103 (S), #104 (XS), #106 (S)
    });

    it("filters by type label", () => {
      const result = executeQuery("type:bug", issues);
      expect(result.matchCount).toBe(2); // #101, #106
    });

    it("filters by component label", () => {
      const result = executeQuery("component:ui", issues);
      expect(result.matchCount).toBe(3); // #102, #105, #106
    });

    it("filters with NOT", () => {
      const result = executeQuery("NOT status:done", issues);
      expect(result.matchCount).toBe(5);
      expect(result.items.every((i) => i.status !== "done")).toBe(true);
    });

    it("filters with wildcard on title", () => {
      const result = executeQuery('title~"Fix*"', issues);
      expect(result.matchCount).toBe(2); // #101, #106
    });

    it("filters by number range", () => {
      const result = executeQuery("number>103", issues);
      expect(result.matchCount).toBe(3); // #104, #105, #106
    });
  });

  describe("complex multi-condition queries", () => {
    it("handles three ANDed conditions", () => {
      const result = executeQuery("status:ready AND priority:P0 AND size:M", issues);
      expect(result.matchCount).toBe(1);
      expect(result.items[0].number).toBe(101);
    });

    it("handles mixed AND/OR with parentheses", () => {
      const result = executeQuery(
        "(priority:P0 OR priority:P1) AND (status:ready OR status:in-progress)",
        issues
      );
      // P0 or P1: #101, #102, #105, #106
      // ready or in-progress: #101, #102, #103, #105, #106
      // Intersection: #101, #102, #105, #106
      expect(result.matchCount).toBe(4);
    });

    it("handles NOT with AND", () => {
      const result = executeQuery("NOT status:done AND NOT status:in-progress", issues);
      // Should match ready issues: #101, #103, #105
      expect(result.matchCount).toBe(3);
    });
  });

  describe("end-to-end pipeline", () => {
    it("tokenize → parse → evaluate produces consistent results", () => {
      const query = "status:ready AND priority:P0";

      // Step 1: Tokenize
      const tokens = tokenize(query);
      expect(tokens.length).toBeGreaterThan(0);

      // Step 2: Parse
      const parseResult = parse(query);
      expect(parseResult.ast).not.toBeNull();
      expect(parseResult.errors).toEqual([]);

      // Step 3: Execute
      const result = executeQuery(query, issues);
      expect(result.matchCount).toBe(1);
      expect(result.items[0].number).toBe(101);
    });

    it("validate catches errors before execution", () => {
      const errors = validate("unknownfield:value");
      expect(errors.length).toBeGreaterThan(0);
    });

    it("isValid returns boolean for quick checks", () => {
      expect(isValid("status:ready")).toBe(true);
      expect(isValid("badfield:value")).toBe(false);
    });
  });

  describe("result metadata", () => {
    it("includes totalCount, matchCount, and executionTimeMs", () => {
      const result = executeQuery("status:ready", issues);
      expect(result.totalCount).toBe(6);
      expect(result.matchCount).toBe(3);
      expect(typeof result.executionTimeMs).toBe("number");
      expect(result.executionTimeMs).toBeGreaterThanOrEqual(0);
    });

    it("preserves all issue fields in results", () => {
      const result = executeQuery("number:101", issues);
      expect(result.matchCount).toBe(1);
      const item = result.items[0];
      expect(item.number).toBe(101);
      expect(item.title).toBe("Fix authentication bug in login flow");
      expect(item.labels).toContain("type:bug");
      expect(item.priority).toBe("P0");
      expect(item.size).toBe("M");
      expect(item.url).toContain("101");
      expect(item.status).toBe("ready");
      expect(item.assignee).toBe("alice");
    });
  });
});
