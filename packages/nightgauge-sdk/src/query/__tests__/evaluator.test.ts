import { describe, it, expect } from "vitest";
import { evaluateNode, evaluate, executeQuery } from "../evaluator.js";
import { parse } from "../parser.js";
import { EvaluationError } from "../errors.js";
import type { QueryableIssue, ASTNode } from "../types.js";

/** Helper to create a test issue */
function createIssue(overrides: Partial<QueryableIssue> = {}): QueryableIssue {
  return {
    number: 1,
    title: "Test issue",
    labels: [],
    priority: null,
    size: null,
    url: "https://github.com/test/repo/issues/1",
    ...overrides,
  };
}

/** Helper to parse and get AST */
function getAST(query: string): ASTNode {
  const result = parse(query);
  if (!result.ast) {
    throw new Error(`Failed to parse: ${result.errors.map((e) => e.message).join(", ")}`);
  }
  return result.ast;
}

describe("evaluateNode", () => {
  describe("equality operators (: and =)", () => {
    it("matches status field", () => {
      const ast = getAST("status:ready");
      const issue = createIssue({ status: "ready" });
      expect(evaluateNode(ast, issue)).toBe(true);
    });

    it("is case-insensitive for equality", () => {
      const ast = getAST("status:Ready");
      const issue = createIssue({ status: "ready" });
      expect(evaluateNode(ast, issue)).toBe(true);
    });

    it("returns false when field doesn't match", () => {
      const ast = getAST("status:ready");
      const issue = createIssue({ status: "done" });
      expect(evaluateNode(ast, issue)).toBe(false);
    });

    it("returns false when field is null", () => {
      const ast = getAST("status:ready");
      const issue = createIssue({ status: undefined });
      expect(evaluateNode(ast, issue)).toBe(false);
    });

    it("matches priority field", () => {
      const ast = getAST("priority:P0");
      const issue = createIssue({ priority: "P0" });
      expect(evaluateNode(ast, issue)).toBe(true);
    });

    it("matches size field", () => {
      const ast = getAST("size:M");
      const issue = createIssue({ size: "M" });
      expect(evaluateNode(ast, issue)).toBe(true);
    });

    it("matches assignee field", () => {
      const ast = getAST("assignee:johndoe");
      const issue = createIssue({ assignee: "johndoe" });
      expect(evaluateNode(ast, issue)).toBe(true);
    });

    it("matches title field", () => {
      const ast = getAST("title:test");
      const issue = createIssue({ title: "test" });
      expect(evaluateNode(ast, issue)).toBe(true);
    });

    it("matches number field", () => {
      const ast = getAST("number:42");
      const issue = createIssue({ number: 42 });
      expect(evaluateNode(ast, issue)).toBe(true);
    });
  });

  describe("inequality operator (!=)", () => {
    it("matches when field is different", () => {
      const ast = getAST("status!=done");
      const issue = createIssue({ status: "ready" });
      expect(evaluateNode(ast, issue)).toBe(true);
    });

    it("returns false when field matches", () => {
      const ast = getAST("status!=ready");
      const issue = createIssue({ status: "ready" });
      expect(evaluateNode(ast, issue)).toBe(false);
    });

    it("returns true when field is null (null != value)", () => {
      const ast = getAST("status!=ready");
      const issue = createIssue({ status: undefined });
      expect(evaluateNode(ast, issue)).toBe(true);
    });
  });

  describe("wildcard operator (~)", () => {
    it("matches with trailing wildcard", () => {
      const ast = getAST('title~"auth*"');
      const issue = createIssue({ title: "authentication module" });
      expect(evaluateNode(ast, issue)).toBe(true);
    });

    it("matches with leading wildcard", () => {
      const ast = getAST('title~"*module"');
      const issue = createIssue({ title: "authentication module" });
      expect(evaluateNode(ast, issue)).toBe(true);
    });

    it("matches with both wildcards", () => {
      const ast = getAST('title~"*auth*"');
      const issue = createIssue({ title: "the authentication module" });
      expect(evaluateNode(ast, issue)).toBe(true);
    });

    it("returns false when pattern doesn't match", () => {
      const ast = getAST('title~"auth*"');
      const issue = createIssue({ title: "payment module" });
      expect(evaluateNode(ast, issue)).toBe(false);
    });
  });

  describe("comparison operators for numbers", () => {
    it("evaluates > for numbers", () => {
      const ast = getAST("number>50");
      expect(evaluateNode(ast, createIssue({ number: 100 }))).toBe(true);
      expect(evaluateNode(ast, createIssue({ number: 50 }))).toBe(false);
      expect(evaluateNode(ast, createIssue({ number: 10 }))).toBe(false);
    });

    it("evaluates < for numbers", () => {
      const ast = getAST("number<50");
      expect(evaluateNode(ast, createIssue({ number: 10 }))).toBe(true);
      expect(evaluateNode(ast, createIssue({ number: 50 }))).toBe(false);
    });

    it("evaluates >= for numbers", () => {
      const ast = getAST("number>=50");
      expect(evaluateNode(ast, createIssue({ number: 50 }))).toBe(true);
      expect(evaluateNode(ast, createIssue({ number: 51 }))).toBe(true);
      expect(evaluateNode(ast, createIssue({ number: 49 }))).toBe(false);
    });

    it("evaluates <= for numbers", () => {
      const ast = getAST("number<=50");
      expect(evaluateNode(ast, createIssue({ number: 50 }))).toBe(true);
      expect(evaluateNode(ast, createIssue({ number: 49 }))).toBe(true);
      expect(evaluateNode(ast, createIssue({ number: 51 }))).toBe(false);
    });
  });

  describe("comparison operators for sizes", () => {
    it("evaluates > for sizes (XS < S < M < L < XL)", () => {
      const ast = getAST("size>M");
      expect(evaluateNode(ast, createIssue({ size: "L" }))).toBe(true);
      expect(evaluateNode(ast, createIssue({ size: "XL" }))).toBe(true);
      expect(evaluateNode(ast, createIssue({ size: "M" }))).toBe(false);
      expect(evaluateNode(ast, createIssue({ size: "S" }))).toBe(false);
    });

    it("evaluates < for sizes", () => {
      const ast = getAST("size<M");
      expect(evaluateNode(ast, createIssue({ size: "S" }))).toBe(true);
      expect(evaluateNode(ast, createIssue({ size: "XS" }))).toBe(true);
      expect(evaluateNode(ast, createIssue({ size: "M" }))).toBe(false);
    });

    it("evaluates >= for sizes", () => {
      const ast = getAST("size>=M");
      expect(evaluateNode(ast, createIssue({ size: "M" }))).toBe(true);
      expect(evaluateNode(ast, createIssue({ size: "L" }))).toBe(true);
    });

    it("evaluates <= for sizes", () => {
      const ast = getAST("size<=M");
      expect(evaluateNode(ast, createIssue({ size: "M" }))).toBe(true);
      expect(evaluateNode(ast, createIssue({ size: "S" }))).toBe(true);
    });
  });

  describe("comparison operators for dates", () => {
    it("evaluates < for relative dates", () => {
      const ast = getAST("updated<7d");
      // "updated<7d" means issue's date is BEFORE the 7-day-ago threshold
      // compareDates returns issueDate - queryDate; < 0 means issue is older

      // 10 days ago IS before 7 days ago → true
      const tenDaysAgo = new Date();
      tenDaysAgo.setDate(tenDaysAgo.getDate() - 10);
      expect(evaluateNode(ast, createIssue({ updatedAt: tenDaysAgo.toISOString() }))).toBe(true);

      // 3 days ago is NOT before 7 days ago → false
      const threeDaysAgo = new Date();
      threeDaysAgo.setDate(threeDaysAgo.getDate() - 3);
      expect(evaluateNode(ast, createIssue({ updatedAt: threeDaysAgo.toISOString() }))).toBe(false);
    });

    it("evaluates > for ISO dates", () => {
      const ast = getAST("created>2026-01-01");
      const issue = createIssue({ createdAt: "2026-06-15T00:00:00Z" });
      expect(evaluateNode(ast, issue)).toBe(true);
    });

    it("returns false for null date fields", () => {
      const ast = getAST("updated<7d");
      const issue = createIssue({ updatedAt: undefined });
      expect(evaluateNode(ast, issue)).toBe(false);
    });
  });

  describe("array fields (labels)", () => {
    it("matches when label contains value", () => {
      const ast = getAST("labels:bug");
      const issue = createIssue({ labels: ["bug", "critical"] });
      expect(evaluateNode(ast, issue)).toBe(true);
    });

    it("returns false when no label matches", () => {
      const ast = getAST("labels:bug");
      const issue = createIssue({ labels: ["feature", "enhancement"] });
      expect(evaluateNode(ast, issue)).toBe(false);
    });

    it("returns false for empty labels array", () => {
      const ast = getAST("labels:bug");
      const issue = createIssue({ labels: [] });
      expect(evaluateNode(ast, issue)).toBe(false);
    });

    it("matches != when no label matches", () => {
      const ast = getAST("labels!=bug");
      const issue = createIssue({ labels: ["feature"] });
      expect(evaluateNode(ast, issue)).toBe(true);
    });

    it("returns false for != when label matches", () => {
      const ast = getAST("labels!=bug");
      const issue = createIssue({ labels: ["bug", "critical"] });
      expect(evaluateNode(ast, issue)).toBe(false);
    });
  });

  describe("component field", () => {
    it("matches component: prefixed labels", () => {
      const ast = getAST("component:api");
      const issue = createIssue({ labels: ["component:api", "feature"] });
      expect(evaluateNode(ast, issue)).toBe(true);
    });

    it("returns false when no component label matches", () => {
      const ast = getAST("component:api");
      const issue = createIssue({ labels: ["component:ui", "feature"] });
      expect(evaluateNode(ast, issue)).toBe(false);
    });
  });

  describe("type field", () => {
    it("matches type: prefixed labels", () => {
      const ast = getAST("type:bug");
      const issue = createIssue({ labels: ["type:bug", "critical"] });
      expect(evaluateNode(ast, issue)).toBe(true);
    });

    it("returns false when type doesn't match", () => {
      const ast = getAST("type:bug");
      const issue = createIssue({ labels: ["type:feature"] });
      expect(evaluateNode(ast, issue)).toBe(false);
    });

    it("returns false when no type label exists", () => {
      const ast = getAST("type:bug");
      const issue = createIssue({ labels: ["critical"] });
      expect(evaluateNode(ast, issue)).toBe(false);
    });
  });

  describe("boolean operators", () => {
    it("evaluates AND (both must match)", () => {
      const ast = getAST("status:ready AND priority:P0");
      expect(evaluateNode(ast, createIssue({ status: "ready", priority: "P0" }))).toBe(true);
      expect(evaluateNode(ast, createIssue({ status: "ready", priority: "P1" }))).toBe(false);
      expect(evaluateNode(ast, createIssue({ status: "done", priority: "P0" }))).toBe(false);
    });

    it("evaluates OR (either must match)", () => {
      const ast = getAST("priority:P0 OR priority:P1");
      expect(evaluateNode(ast, createIssue({ priority: "P0" }))).toBe(true);
      expect(evaluateNode(ast, createIssue({ priority: "P1" }))).toBe(true);
      expect(evaluateNode(ast, createIssue({ priority: "P2" }))).toBe(false);
    });

    it("evaluates NOT (negation)", () => {
      const ast = getAST("NOT status:done");
      expect(evaluateNode(ast, createIssue({ status: "ready" }))).toBe(true);
      expect(evaluateNode(ast, createIssue({ status: "done" }))).toBe(false);
    });

    it("short-circuits AND evaluation", () => {
      const ast = getAST("status:ready AND priority:P0");
      // When status doesn't match, priority shouldn't matter
      expect(evaluateNode(ast, createIssue({ status: "done", priority: "P0" }))).toBe(false);
    });

    it("short-circuits OR evaluation", () => {
      const ast = getAST("priority:P0 OR priority:P1");
      // When first matches, second shouldn't matter
      expect(evaluateNode(ast, createIssue({ priority: "P0" }))).toBe(true);
    });
  });
});

describe("evaluate", () => {
  const issues: QueryableIssue[] = [
    createIssue({ number: 1, status: "ready", priority: "P0", size: "S" }),
    createIssue({ number: 2, status: "ready", priority: "P1", size: "M" }),
    createIssue({ number: 3, status: "in-progress", priority: "P0", size: "L" }),
    createIssue({ number: 4, status: "done", priority: "P2", size: "XS" }),
    createIssue({ number: 5, status: "ready", priority: "P2", size: "XL" }),
  ];

  it("filters issues by single condition", () => {
    const ast = getAST("status:ready");
    const result = evaluate(ast, issues);
    expect(result.matchCount).toBe(3);
    expect(result.totalCount).toBe(5);
    expect(result.items.map((i) => i.number)).toEqual([1, 2, 5]);
  });

  it("filters issues by AND condition", () => {
    const ast = getAST("status:ready AND priority:P0");
    const result = evaluate(ast, issues);
    expect(result.matchCount).toBe(1);
    expect(result.items[0].number).toBe(1);
  });

  it("filters issues by OR condition", () => {
    const ast = getAST("priority:P0 OR priority:P1");
    const result = evaluate(ast, issues);
    expect(result.matchCount).toBe(3);
    expect(result.items.map((i) => i.number)).toEqual([1, 2, 3]);
  });

  it("filters issues with NOT", () => {
    const ast = getAST("NOT status:done");
    const result = evaluate(ast, issues);
    expect(result.matchCount).toBe(4);
    expect(result.items.every((i) => i.status !== "done")).toBe(true);
  });

  it("returns execution time in milliseconds", () => {
    const ast = getAST("status:ready");
    const result = evaluate(ast, issues);
    expect(result.executionTimeMs).toBeGreaterThanOrEqual(0);
  });

  it("returns empty result for no matches", () => {
    const ast = getAST("status:cancelled");
    const result = evaluate(ast, issues);
    expect(result.matchCount).toBe(0);
    expect(result.items).toEqual([]);
  });

  it("handles empty issue array", () => {
    const ast = getAST("status:ready");
    const result = evaluate(ast, []);
    expect(result.matchCount).toBe(0);
    expect(result.totalCount).toBe(0);
  });
});

describe("executeQuery", () => {
  const issues: QueryableIssue[] = [
    createIssue({ number: 1, status: "ready", priority: "P0" }),
    createIssue({ number: 2, status: "done", priority: "P1" }),
  ];

  it("parses and evaluates in one call", () => {
    const result = executeQuery("status:ready", issues);
    expect(result.matchCount).toBe(1);
    expect(result.items[0].number).toBe(1);
  });

  it("throws EvaluationError for invalid query", () => {
    expect(() => executeQuery("", issues)).toThrow(EvaluationError);
  });

  it("throws EvaluationError for unknown field", () => {
    expect(() => executeQuery("badfield:value", issues)).toThrow(EvaluationError);
  });
});
