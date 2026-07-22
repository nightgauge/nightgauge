/**
 * Evaluator unit tests
 *
 * Tests query execution against issue data.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { evaluate, evaluateNode, executeQuery } from "../../src/query/evaluator.js";
import { parse } from "../../src/query/parser.js";
import type { QueryableIssue, ASTNode } from "../../src/query/types.js";
import { EvaluationError } from "../../src/query/errors.js";

// Mock issues for testing
const mockIssues: QueryableIssue[] = [
  {
    number: 1,
    title: "Fix authentication bug",
    labels: ["type:bug", "priority:high", "component:auth"],
    priority: "P0",
    size: "M",
    url: "https://github.com/org/repo/issues/1",
    status: "ready",
    assignee: "alice",
    updatedAt: new Date().toISOString(),
    createdAt: "2026-01-01T00:00:00Z",
  },
  {
    number: 2,
    title: "Add login form",
    labels: ["type:feature", "priority:medium", "component:ui"],
    priority: "P1",
    size: "L",
    url: "https://github.com/org/repo/issues/2",
    status: "ready",
    assignee: "bob",
    updatedAt: "2026-01-15T00:00:00Z",
    createdAt: "2026-01-10T00:00:00Z",
  },
  {
    number: 3,
    title: "Refactor database layer",
    labels: ["type:refactor", "priority:low"],
    priority: "P2",
    size: "XL",
    url: "https://github.com/org/repo/issues/3",
    status: "in-progress",
    assignee: "alice",
    updatedAt: "2026-01-20T00:00:00Z",
    createdAt: "2026-01-15T00:00:00Z",
  },
  {
    number: 4,
    title: "Documentation update",
    labels: ["type:docs", "priority:low"],
    priority: null,
    size: "S",
    url: "https://github.com/org/repo/issues/4",
    status: "done",
    assignee: undefined,
    updatedAt: "2026-01-25T00:00:00Z",
    createdAt: "2026-01-20T00:00:00Z",
  },
];

describe("Evaluator", () => {
  describe("evaluateNode()", () => {
    describe("comparison nodes", () => {
      it("should match status equality", () => {
        const { ast } = parse("status:ready");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        expect(matches).toHaveLength(2);
        expect(matches.map((i) => i.number)).toEqual([1, 2]);
      });

      it("should match priority equality", () => {
        const { ast } = parse("priority:P0");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        expect(matches).toHaveLength(1);
        expect(matches[0].number).toBe(1);
      });

      it("should match size equality", () => {
        const { ast } = parse("size:M");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        expect(matches).toHaveLength(1);
        expect(matches[0].number).toBe(1);
      });

      it("should match assignee equality", () => {
        const { ast } = parse("assignee:alice");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        expect(matches).toHaveLength(2);
        expect(matches.map((i) => i.number)).toEqual([1, 3]);
      });

      it("should match != operator", () => {
        const { ast } = parse("status!=done");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        expect(matches).toHaveLength(3);
        expect(matches.every((i) => i.status !== "done")).toBe(true);
      });

      it("should handle case-insensitive matching", () => {
        const { ast } = parse("status:READY");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        expect(matches).toHaveLength(2);
      });
    });

    describe("size comparison operators", () => {
      it("should match size > operator", () => {
        const { ast } = parse("size>M");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        // L and XL are > M
        expect(matches).toHaveLength(2);
        expect(matches.map((i) => i.size)).toEqual(["L", "XL"]);
      });

      it("should match size < operator", () => {
        const { ast } = parse("size<M");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        // S is < M
        expect(matches).toHaveLength(1);
        expect(matches[0].size).toBe("S");
      });

      it("should match size >= operator", () => {
        const { ast } = parse("size>=M");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        // M, L, XL are >= M
        expect(matches).toHaveLength(3);
      });

      it("should match size <= operator", () => {
        const { ast } = parse("size<=M");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        // S, M are <= M
        expect(matches).toHaveLength(2);
      });
    });

    describe("number comparison operators", () => {
      it("should match number > operator", () => {
        const { ast } = parse("number>2");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        expect(matches).toHaveLength(2);
        expect(matches.map((i) => i.number)).toEqual([3, 4]);
      });

      it("should match number < operator", () => {
        const { ast } = parse("number<3");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        expect(matches).toHaveLength(2);
        expect(matches.map((i) => i.number)).toEqual([1, 2]);
      });
    });

    describe("title wildcard matching", () => {
      it("should match title with wildcard at end", () => {
        const { ast } = parse("title~Fix*");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        expect(matches).toHaveLength(1);
        expect(matches[0].number).toBe(1);
      });

      it("should match title with wildcard at start", () => {
        const { ast } = parse("title~*bug");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        expect(matches).toHaveLength(1);
        expect(matches[0].number).toBe(1);
      });

      it("should match title with wildcard in middle", () => {
        const { ast } = parse("title~Add*form");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        expect(matches).toHaveLength(1);
        expect(matches[0].number).toBe(2);
      });
    });

    describe("label matching", () => {
      it("should match labels containing value", () => {
        const { ast } = parse("labels:bug");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        expect(matches).toHaveLength(1);
        expect(matches[0].labels).toContain("type:bug");
      });

      it("should match type label", () => {
        const { ast } = parse("type:feature");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        expect(matches).toHaveLength(1);
        expect(matches[0].number).toBe(2);
      });
    });

    describe("binary expressions", () => {
      it("should evaluate AND expression", () => {
        const { ast } = parse("status:ready AND priority:P0");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        expect(matches).toHaveLength(1);
        expect(matches[0].number).toBe(1);
      });

      it("should evaluate OR expression", () => {
        const { ast } = parse("priority:P0 OR priority:P1");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        expect(matches).toHaveLength(2);
        expect(matches.map((i) => i.number)).toEqual([1, 2]);
      });

      it("should evaluate complex AND/OR expression", () => {
        const { ast } = parse("status:ready AND (priority:P0 OR priority:P1)");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        expect(matches).toHaveLength(2);
        expect(matches.map((i) => i.number)).toEqual([1, 2]);
      });
    });

    describe("NOT expressions", () => {
      it("should evaluate NOT expression", () => {
        const { ast } = parse("NOT status:done");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        expect(matches).toHaveLength(3);
        expect(matches.every((i) => i.status !== "done")).toBe(true);
      });

      it("should evaluate complex NOT expression", () => {
        const { ast } = parse("status:ready AND NOT priority:P0");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        expect(matches).toHaveLength(1);
        expect(matches[0].number).toBe(2);
      });
    });

    describe("null field handling", () => {
      it("should not match null fields with equality", () => {
        const { ast } = parse("priority:P0");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        // Issue 4 has null priority, should not match
        expect(matches.every((i) => i.priority !== null)).toBe(true);
      });

      it("should match null fields with != operator", () => {
        const { ast } = parse("priority!=P0");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        // All issues that are not P0, including null
        expect(matches).toHaveLength(3);
      });

      it("should not match missing assignee", () => {
        const { ast } = parse("assignee:alice");
        const matches = mockIssues.filter((issue) => evaluateNode(ast!, issue));

        // Issue 4 has no assignee
        expect(matches.every((i) => i.assignee !== undefined)).toBe(true);
      });
    });
  });

  describe("evaluate()", () => {
    it("should return QueryResult with all fields", () => {
      const { ast } = parse("status:ready");
      const result = evaluate(ast!, mockIssues);

      expect(result.items).toHaveLength(2);
      expect(result.totalCount).toBe(4);
      expect(result.matchCount).toBe(2);
      expect(result.executionTimeMs).toBeGreaterThanOrEqual(0);
    });

    it("should handle empty result", () => {
      const { ast } = parse("priority:P3");
      const result = evaluate(ast!, mockIssues);

      expect(result.items).toHaveLength(0);
      expect(result.matchCount).toBe(0);
      expect(result.totalCount).toBe(4);
    });

    it("should handle empty input", () => {
      const { ast } = parse("status:ready");
      const result = evaluate(ast!, []);

      expect(result.items).toHaveLength(0);
      expect(result.totalCount).toBe(0);
      expect(result.matchCount).toBe(0);
    });
  });

  describe("executeQuery()", () => {
    it("should parse and execute query in one call", () => {
      const result = executeQuery("status:ready AND priority:P0", mockIssues);

      expect(result.matchCount).toBe(1);
      expect(result.items[0].number).toBe(1);
    });

    it("should throw on invalid query", () => {
      expect(() => executeQuery("unknownfield:value", mockIssues)).toThrow(EvaluationError);
    });

    it("should throw with error message", () => {
      try {
        executeQuery("", mockIssues);
        expect.fail("Should have thrown");
      } catch (error) {
        expect(error).toBeInstanceOf(EvaluationError);
        expect((error as Error).message).toContain("Invalid query");
      }
    });
  });
});
