import { describe, it, expect } from "vitest";
import { executeQuery, evaluate } from "../evaluator.js";
import { parse } from "../parser.js";
import type { QueryableIssue } from "../types.js";

/**
 * Evaluator tests using a realistic, production-representative dataset.
 *
 * Simulates a sprint board for a mid-sized software team with:
 * - Multiple developers with assigned issues
 * - Mixed priorities, sizes, and statuses
 * - Date-stamped updates for temporal queries
 * - Component and type labels following the project label schema
 */

const NOW = new Date();
const daysAgo = (n: number) => new Date(NOW.getTime() - n * 86400000).toISOString();

/** Realistic sprint dataset — 20 issues */
const SPRINT_ISSUES: QueryableIssue[] = [
  // P0 critical bugs
  {
    number: 1001,
    title: "Fix authentication token expiry crash",
    labels: ["type:bug", "component:auth", "critical"],
    priority: "P0",
    size: "S",
    url: "https://github.com/org/repo/issues/1001",
    status: "in-progress",
    assignee: "alice",
    updatedAt: daysAgo(1),
    createdAt: daysAgo(3),
  },
  {
    number: 1002,
    title: "Fix data corruption on concurrent writes",
    labels: ["type:bug", "component:database", "critical"],
    priority: "P0",
    size: "L",
    url: "https://github.com/org/repo/issues/1002",
    status: "ready",
    assignee: "bob",
    updatedAt: daysAgo(2),
    createdAt: daysAgo(5),
  },
  // P1 high priority features
  {
    number: 1003,
    title: "Add dark mode to settings panel",
    labels: ["type:feature", "component:ui"],
    priority: "P1",
    size: "M",
    url: "https://github.com/org/repo/issues/1003",
    status: "in-review",
    assignee: "charlie",
    updatedAt: daysAgo(0),
    createdAt: daysAgo(10),
  },
  {
    number: 1004,
    title: "Implement query result export",
    labels: ["type:feature", "component:api"],
    priority: "P1",
    size: "M",
    url: "https://github.com/org/repo/issues/1004",
    status: "ready",
    assignee: "alice",
    updatedAt: daysAgo(4),
    createdAt: daysAgo(15),
  },
  {
    number: 1005,
    title: "Optimize database connection pooling",
    labels: ["type:feature", "component:database", "performance"],
    priority: "P1",
    size: "L",
    url: "https://github.com/org/repo/issues/1005",
    status: "ready",
    assignee: "dave",
    updatedAt: daysAgo(6),
    createdAt: daysAgo(20),
  },
  // P2 standard work
  {
    number: 1006,
    title: "Update API documentation for v3",
    labels: ["type:docs", "component:api"],
    priority: "P2",
    size: "S",
    url: "https://github.com/org/repo/issues/1006",
    status: "ready",
    assignee: "charlie",
    updatedAt: daysAgo(8),
    createdAt: daysAgo(12),
  },
  {
    number: 1007,
    title: "Refactor auth middleware",
    labels: ["type:refactor", "component:auth"],
    priority: "P2",
    size: "M",
    url: "https://github.com/org/repo/issues/1007",
    status: "backlog",
    assignee: undefined,
    updatedAt: daysAgo(14),
    createdAt: daysAgo(30),
  },
  {
    number: 1008,
    title: "Add pagination to user list endpoint",
    labels: ["type:feature", "component:api"],
    priority: "P2",
    size: "S",
    url: "https://github.com/org/repo/issues/1008",
    status: "ready",
    assignee: "bob",
    updatedAt: daysAgo(3),
    createdAt: daysAgo(8),
  },
  // P3 low priority
  {
    number: 1009,
    title: "Add tooltip to all icon buttons",
    labels: ["type:feature", "component:ui"],
    priority: "P3",
    size: "XS",
    url: "https://github.com/org/repo/issues/1009",
    status: "backlog",
    assignee: undefined,
    updatedAt: daysAgo(20),
    createdAt: daysAgo(45),
  },
  {
    number: 1010,
    title: "Update copyright year in footer",
    labels: ["type:docs"],
    priority: "P3",
    size: "XS",
    url: "https://github.com/org/repo/issues/1010",
    status: "done",
    assignee: "charlie",
    updatedAt: daysAgo(25),
    createdAt: daysAgo(50),
  },
  // Done issues
  {
    number: 1011,
    title: "Fix CSS overflow in mobile view",
    labels: ["type:bug", "component:ui"],
    priority: "P1",
    size: "S",
    url: "https://github.com/org/repo/issues/1011",
    status: "done",
    assignee: "alice",
    updatedAt: daysAgo(5),
    createdAt: daysAgo(7),
  },
  {
    number: 1012,
    title: "Add integration test for auth flow",
    labels: ["type:feature", "component:auth"],
    priority: "P1",
    size: "M",
    url: "https://github.com/org/repo/issues/1012",
    status: "done",
    assignee: "bob",
    updatedAt: daysAgo(6),
    createdAt: daysAgo(9),
  },
  // XL sized epics/large stories
  {
    number: 1013,
    title: "Migrate to PostgreSQL 15",
    labels: ["type:refactor", "component:database"],
    priority: "P1",
    size: "XL",
    url: "https://github.com/org/repo/issues/1013",
    status: "backlog",
    assignee: "dave",
    updatedAt: daysAgo(30),
    createdAt: daysAgo(60),
  },
  {
    number: 1014,
    title: "Implement real-time notifications",
    labels: ["type:feature", "component:api", "component:ui"],
    priority: "P2",
    size: "XL",
    url: "https://github.com/org/repo/issues/1014",
    status: "backlog",
    assignee: undefined,
    updatedAt: daysAgo(45),
    createdAt: daysAgo(60),
  },
  // Issues with multiple components
  {
    number: 1015,
    title: "Add search to settings page",
    labels: ["type:feature", "component:ui", "component:api"],
    priority: "P2",
    size: "M",
    url: "https://github.com/org/repo/issues/1015",
    status: "ready",
    assignee: "charlie",
    updatedAt: daysAgo(2),
    createdAt: daysAgo(7),
  },
  {
    number: 1016,
    title: "Fix broken link in onboarding docs",
    labels: ["type:bug", "type:docs"],
    priority: "P2",
    size: "XS",
    url: "https://github.com/org/repo/issues/1016",
    status: "done",
    assignee: "dave",
    updatedAt: daysAgo(9),
    createdAt: daysAgo(10),
  },
  // No optional fields set
  {
    number: 1017,
    title: "Investigate memory leak in worker process",
    labels: ["type:bug"],
    priority: "P0",
    size: null,
    url: "https://github.com/org/repo/issues/1017",
    status: "ready",
    assignee: undefined,
    updatedAt: undefined,
    createdAt: undefined,
  },
  {
    number: 1018,
    title: "Add rate limiting to public API",
    labels: ["type:feature", "component:api"],
    priority: "P0",
    size: "M",
    url: "https://github.com/org/repo/issues/1018",
    status: "ready",
    assignee: "alice",
    updatedAt: daysAgo(1),
    createdAt: daysAgo(2),
  },
  {
    number: 1019,
    title: "Refactor frontend state management",
    labels: ["type:refactor", "component:ui"],
    priority: "P2",
    size: "L",
    url: "https://github.com/org/repo/issues/1019",
    status: "backlog",
    assignee: undefined,
    updatedAt: daysAgo(15),
    createdAt: daysAgo(30),
  },
  {
    number: 1020,
    title: "Write load testing plan",
    labels: ["type:docs"],
    priority: "P3",
    size: "S",
    url: "https://github.com/org/repo/issues/1020",
    status: "backlog",
    assignee: "dave",
    updatedAt: daysAgo(18),
    createdAt: daysAgo(20),
  },
];

describe("Evaluator — realistic sprint dataset", () => {
  describe("sprint planning queries", () => {
    it("finds all ready work", () => {
      const result = executeQuery("status:ready", SPRINT_ISSUES);
      const readyNumbers = result.items.map((i) => i.number);
      expect(readyNumbers).toContain(1002);
      expect(readyNumbers).toContain(1004);
      expect(readyNumbers).toContain(1005);
      expect(readyNumbers).toContain(1008);
      expect(readyNumbers).toContain(1015);
      expect(readyNumbers).toContain(1006);
      expect(readyNumbers).toContain(1017);
      expect(readyNumbers).toContain(1018);
      expect(result.matchCount).toBe(8);
    });

    it("finds critical in-progress and ready bugs", () => {
      const result = executeQuery(
        "(status:ready OR status:in-progress) AND priority:P0",
        SPRINT_ISSUES
      );
      expect(result.items.map((i) => i.number).sort()).toEqual([1001, 1002, 1017, 1018]);
    });

    it("finds alice's active work", () => {
      const result = executeQuery("assignee:alice AND NOT status:done", SPRINT_ISSUES);
      const numbers = result.items.map((i) => i.number).sort();
      expect(numbers).toEqual([1001, 1004, 1018]);
    });

    it("finds unassigned ready issues", () => {
      const result = executeQuery(
        "status:ready AND NOT assignee:alice AND NOT assignee:bob AND NOT assignee:charlie AND NOT assignee:dave",
        SPRINT_ISSUES
      );
      // Issue 1017 is ready and unassigned
      expect(result.items.map((i) => i.number)).toContain(1017);
    });
  });

  describe("bug triage queries", () => {
    it("finds all bugs not done", () => {
      const result = executeQuery("type:bug AND NOT status:done", SPRINT_ISSUES);
      // 1001, 1002 (bugs, not done), 1017 (bug, ready)
      const numbers = result.items.map((i) => i.number).sort();
      expect(numbers).toContain(1001);
      expect(numbers).toContain(1002);
      expect(numbers).toContain(1017);
    });

    it("finds high-priority bugs", () => {
      const result = executeQuery("type:bug AND (priority:P0 OR priority:P1)", SPRINT_ISSUES);
      // P0 bugs: 1001, 1002, 1017; P1 bugs: 1011 (done)
      expect(result.matchCount).toBe(4);
    });

    it("finds auth component bugs specifically", () => {
      const result = executeQuery("type:bug AND component:auth", SPRINT_ISSUES);
      expect(result.items.map((i) => i.number)).toEqual([1001]);
    });
  });

  describe("size-based filtering", () => {
    it("finds small and extra-small ready items", () => {
      const result = executeQuery("status:ready AND (size:S OR size:XS)", SPRINT_ISSUES);
      const numbers = result.items.map((i) => i.number).sort();
      expect(numbers).not.toContain(1002); // 1002 is L size — should not match
      expect(numbers).toContain(1008); // S, ready
    });

    it("finds large and extra-large backlog items", () => {
      const result = executeQuery("status:backlog AND size>=L", SPRINT_ISSUES);
      const numbers = result.items.map((i) => i.number).sort();
      expect(numbers).toContain(1013); // XL, backlog
      expect(numbers).toContain(1014); // XL, backlog
      expect(numbers).toContain(1019); // L, backlog
    });

    it("excludes XL items from quick-win filter", () => {
      const result = executeQuery("size<L AND status:ready", SPRINT_ISSUES);
      // Should not include any XL or L items
      for (const item of result.items) {
        expect(["XS", "S", "M"]).toContain(item.size);
      }
    });
  });

  describe("date-based queries", () => {
    it("finds recently updated issues (last 3 days)", () => {
      const result = executeQuery("updated>3d", SPRINT_ISSUES);
      // Issues updated within last 3 days: 1001 (1d), 1003 (0d), 1008 (3d), 1015 (2d), 1018 (1d)
      // Note: "updated>3d" means updatedAt IS AFTER the 3-days-ago threshold
      // Issues updated 0d, 1d, 2d ago should match
      const updatedRecently = result.items.filter((i) => i.updatedAt !== undefined);
      for (const item of updatedRecently) {
        const updatedDate = new Date(item.updatedAt!);
        const threeDaysAgo = new Date(NOW.getTime() - 3 * 86400000);
        expect(updatedDate.getTime()).toBeGreaterThan(threeDaysAgo.getTime());
      }
    });

    it("finds stale issues not updated in 2+ weeks", () => {
      const result = executeQuery("updated<14d", SPRINT_ISSUES);
      // Issues updated more than 14 days ago are "older" than 14d threshold
      for (const item of result.items) {
        if (item.updatedAt) {
          const updatedDate = new Date(item.updatedAt);
          const fourteenDaysAgo = new Date(NOW.getTime() - 14 * 86400000);
          expect(updatedDate.getTime()).toBeLessThanOrEqual(fourteenDaysAgo.getTime());
        }
      }
    });

    it("handles issues with missing date fields gracefully", () => {
      // Issue 1017 has undefined updatedAt — should not match date queries
      const result = executeQuery("updated>1d", SPRINT_ISSUES);
      expect(result.items.map((i) => i.number)).not.toContain(1017);
    });
  });

  describe("label and component queries", () => {
    it("finds all database-related work", () => {
      const result = executeQuery("component:database", SPRINT_ISSUES);
      const numbers = result.items.map((i) => i.number).sort();
      expect(numbers).toContain(1002);
      expect(numbers).toContain(1005);
      expect(numbers).toContain(1013);
    });

    it("finds issues touching both UI and API", () => {
      // Issues that have BOTH component:ui AND component:api labels
      const result = executeQuery("component:ui AND component:api", SPRINT_ISSUES);
      const numbers = result.items.map((i) => i.number).sort();
      // Issue 1014 has both, issue 1015 has both
      expect(numbers).toContain(1014);
      expect(numbers).toContain(1015);
    });

    it("finds docs-type issues", () => {
      const result = executeQuery("type:docs", SPRINT_ISSUES);
      const numbers = result.items.map((i) => i.number).sort();
      expect(numbers).toContain(1006);
      expect(numbers).toContain(1010);
      expect(numbers).toContain(1020);
      // 1016 has labels ["type:bug","type:docs"]; the type field extracts the
      // first type: label, so type:docs does NOT match 1016 (it returns "bug")
      expect(numbers).not.toContain(1016);
    });

    it("finds all issues with performance label", () => {
      const result = executeQuery("labels:performance", SPRINT_ISSUES);
      expect(result.items.map((i) => i.number)).toEqual([1005]);
    });
  });

  describe("number range queries", () => {
    it("finds issues in number range 1001-1005", () => {
      const result = executeQuery("number>=1001 AND number<=1005", SPRINT_ISSUES);
      expect(result.matchCount).toBe(5);
      expect(result.items.map((i) => i.number).sort()).toEqual([1001, 1002, 1003, 1004, 1005]);
    });

    it("finds issues in upper range", () => {
      const result = executeQuery("number>1015", SPRINT_ISSUES);
      expect(result.matchCount).toBe(5);
      const numbers = result.items.map((i) => i.number).sort();
      expect(numbers).toEqual([1016, 1017, 1018, 1019, 1020]);
    });
  });

  describe("title wildcard queries", () => {
    it("finds issues with 'auth' in title", () => {
      const result = executeQuery('title~"*auth*"', SPRINT_ISSUES);
      const numbers = result.items.map((i) => i.number).sort();
      expect(numbers).toContain(1001); // "Fix authentication token..."
    });

    it("finds issues starting with 'Fix'", () => {
      const result = executeQuery('title~"Fix*"', SPRINT_ISSUES);
      const numbers = result.items.map((i) => i.number).sort();
      expect(numbers).toContain(1001);
      expect(numbers).toContain(1002);
      expect(numbers).toContain(1011);
      expect(numbers).toContain(1016);
    });

    it("wildcard matching is case-insensitive", () => {
      const result = executeQuery('title~"*Database*"', SPRINT_ISSUES);
      // "Migrate to PostgreSQL 15" shouldn't match but "Fix data corruption on concurrent writes" won't either
      // "Optimize database connection pooling" should match (case-insensitive)
      expect(result.items.map((i) => i.number)).toContain(1005);
    });
  });

  describe("NOT operator with real data", () => {
    it("excludes done and backlog items", () => {
      const result = executeQuery("NOT status:done AND NOT status:backlog", SPRINT_ISSUES);
      for (const item of result.items) {
        expect(item.status).not.toBe("done");
        expect(item.status).not.toBe("backlog");
      }
    });

    it("excludes specific assignee", () => {
      const result = executeQuery(
        "status:ready AND NOT assignee:alice AND NOT assignee:bob",
        SPRINT_ISSUES
      );
      for (const item of result.items) {
        expect(item.assignee).not.toBe("alice");
        expect(item.assignee).not.toBe("bob");
      }
    });
  });

  describe("result metadata", () => {
    it("totalCount reflects full dataset size", () => {
      const result = executeQuery("status:ready", SPRINT_ISSUES);
      expect(result.totalCount).toBe(SPRINT_ISSUES.length);
    });

    it("executionTimeMs is non-negative", () => {
      const result = executeQuery("status:ready AND priority:P0", SPRINT_ISSUES);
      expect(result.executionTimeMs).toBeGreaterThanOrEqual(0);
    });

    it("items preserve all original fields", () => {
      const result = executeQuery("number:1001", SPRINT_ISSUES);
      expect(result.matchCount).toBe(1);
      const item = result.items[0];
      expect(item.number).toBe(1001);
      expect(item.title).toBe("Fix authentication token expiry crash");
      expect(item.labels).toContain("type:bug");
      expect(item.priority).toBe("P0");
      expect(item.size).toBe("S");
      expect(item.status).toBe("in-progress");
      expect(item.assignee).toBe("alice");
    });
  });

  describe("performance with full dataset", () => {
    it("evaluates complex query against 20 issues in <50ms", () => {
      const parseResult = parse(
        "(status:ready OR status:in-progress) AND (priority:P0 OR priority:P1) AND NOT status:done"
      );
      expect(parseResult.ast).not.toBeNull();

      const start = performance.now();
      const result = evaluate(parseResult.ast!, SPRINT_ISSUES);
      const elapsed = performance.now() - start;

      expect(elapsed).toBeLessThan(50);
      expect(result.matchCount).toBeGreaterThan(0);
    });

    it("handles 500 issues efficiently", () => {
      // Generate 500 issues based on sprint dataset pattern
      const largeDataset: QueryableIssue[] = Array.from({ length: 500 }, (_, i) => ({
        number: 2000 + i,
        title: `Issue ${i + 1} — ${["Fix", "Add", "Update", "Refactor"][i % 4]} something`,
        labels: [
          `type:${["bug", "feature", "docs", "refactor"][i % 4]}`,
          `component:${["auth", "api", "ui", "database"][i % 4]}`,
        ],
        priority: (["P0", "P1", "P2", "P3"] as const)[i % 4],
        size: (["XS", "S", "M", "L", "XL"] as const)[i % 5],
        url: `https://github.com/org/repo/issues/${2000 + i}`,
        status: (["ready", "in-progress", "backlog", "done", "in-review"] as const)[i % 5],
        assignee: i % 3 === 0 ? "alice" : i % 3 === 1 ? "bob" : undefined,
        updatedAt: daysAgo(i % 30),
        createdAt: daysAgo(i % 60),
      }));

      const start = performance.now();
      const result = executeQuery(
        "(status:ready OR status:in-progress) AND (priority:P0 OR priority:P1)",
        largeDataset
      );
      const elapsed = performance.now() - start;

      expect(elapsed).toBeLessThan(1000); // <1s for 500 items
      expect(result.totalCount).toBe(500);
      expect(result.matchCount).toBeGreaterThan(0);
    });
  });
});
