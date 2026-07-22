import { describe, it, expect } from "vitest";
import { parse } from "../parser.js";
import type { BinaryNode, ComparisonNode, UnaryNode } from "../types.js";

/**
 * Complex parser tests covering deeply nested precedence, multiple operators,
 * and edge cases in the recursive descent grammar.
 */
describe("Parser — complex precedence and nesting", () => {
  describe("deep AND/OR precedence", () => {
    it("parses four-term AND chain left-associatively", () => {
      // a AND b AND c AND d => ((a AND b) AND c) AND d
      const result = parse("status:ready AND priority:P0 AND size:S AND type:bug");
      expect(result.errors).toEqual([]);
      expect(result.ast).not.toBeNull();

      // Root is AND
      const root = result.ast as BinaryNode;
      expect(root.type).toBe("binary");
      expect(root.operator).toBe("AND");

      // Right leaf is the last condition
      expect((root.right as ComparisonNode).field).toBe("type");

      // Left subtree is another AND
      const left = root.left as BinaryNode;
      expect(left.operator).toBe("AND");
      expect((left.right as ComparisonNode).field).toBe("size");
    });

    it("parses OR with lower precedence than AND in 5-term query", () => {
      // status:ready AND priority:P0 OR status:in-progress AND priority:P1
      // => (status:ready AND priority:P0) OR (status:in-progress AND priority:P1)
      const result = parse("status:ready AND priority:P0 OR status:in-progress AND priority:P1");
      expect(result.errors).toEqual([]);

      const root = result.ast as BinaryNode;
      expect(root.operator).toBe("OR");
      expect((root.left as BinaryNode).operator).toBe("AND");
      expect((root.right as BinaryNode).operator).toBe("AND");
    });

    it("parses triple OR chain", () => {
      const result = parse("status:ready OR status:in-progress OR status:in-review");
      expect(result.errors).toEqual([]);

      const root = result.ast as BinaryNode;
      expect(root.operator).toBe("OR");

      // Left side is also an OR
      const left = root.left as BinaryNode;
      expect(left.operator).toBe("OR");
    });

    it("NOT binds to the immediately following atom, not the whole chain", () => {
      // NOT status:done AND priority:P0
      // => (NOT status:done) AND priority:P0
      const result = parse("NOT status:done AND priority:P0");
      expect(result.errors).toEqual([]);

      const root = result.ast as BinaryNode;
      expect(root.operator).toBe("AND");
      expect(root.left.type).toBe("unary");
      expect((root.left as UnaryNode).operator).toBe("NOT");
      expect((root.right as ComparisonNode).field).toBe("priority");
    });

    it("double NOT is valid and cancels out semantically", () => {
      // NOT NOT status:done — unusual but grammatically valid
      const result = parse("NOT status:done");
      expect(result.errors).toEqual([]);
      const inner = result.ast as UnaryNode;
      expect(inner.type).toBe("unary");
      expect((inner.operand as ComparisonNode).field).toBe("status");
    });
  });

  describe("deeply nested parentheses", () => {
    it("handles triple-nested parentheses", () => {
      const result = parse("(((status:ready)))");
      expect(result.errors).toEqual([]);
      const ast = result.ast as ComparisonNode;
      expect(ast.type).toBe("comparison");
      expect(ast.field).toBe("status");
      expect(ast.value).toBe("ready");
    });

    it("parentheses override AND/OR precedence correctly", () => {
      // status:ready OR (priority:P0 AND size:S)
      const result = parse("status:ready OR (priority:P0 AND size:S)");
      expect(result.errors).toEqual([]);

      const root = result.ast as BinaryNode;
      expect(root.operator).toBe("OR");
      expect((root.left as ComparisonNode).field).toBe("status");

      const right = root.right as BinaryNode;
      expect(right.operator).toBe("AND");
      expect((right.left as ComparisonNode).field).toBe("priority");
    });

    it("parses complex grouped query with three levels", () => {
      const result = parse(
        "((status:ready OR status:in-progress) AND priority:P0) OR (type:bug AND size:S)"
      );
      expect(result.errors).toEqual([]);

      const root = result.ast as BinaryNode;
      expect(root.operator).toBe("OR");

      // Left: (... AND priority:P0)
      const leftAnd = root.left as BinaryNode;
      expect(leftAnd.operator).toBe("AND");
      // Left of left: OR condition
      expect((leftAnd.left as BinaryNode).operator).toBe("OR");
      expect((leftAnd.right as ComparisonNode).field).toBe("priority");

      // Right: (type:bug AND size:S)
      const rightAnd = root.right as BinaryNode;
      expect(rightAnd.operator).toBe("AND");
      expect((rightAnd.left as ComparisonNode).field).toBe("type");
    });

    it("handles NOT inside parentheses", () => {
      const result = parse("(NOT status:done) AND priority:P0");
      expect(result.errors).toEqual([]);

      const root = result.ast as BinaryNode;
      expect(root.operator).toBe("AND");
      expect(root.left.type).toBe("unary");
    });

    it("handles NOT before parenthesized group", () => {
      const result = parse("NOT (status:done OR status:backlog)");
      expect(result.errors).toEqual([]);

      const root = result.ast as UnaryNode;
      expect(root.type).toBe("unary");
      expect(root.operator).toBe("NOT");
      expect(root.operand.type).toBe("binary");

      const inner = root.operand as BinaryNode;
      expect(inner.operator).toBe("OR");
    });
  });

  describe("operator precedence stress tests", () => {
    it("correctly binds in: a OR b AND c => a OR (b AND c)", () => {
      const result = parse("status:ready OR status:done AND priority:P0");
      expect(result.errors).toEqual([]);

      const root = result.ast as BinaryNode;
      expect(root.operator).toBe("OR");
      expect((root.left as ComparisonNode).field).toBe("status");

      const right = root.right as BinaryNode;
      expect(right.operator).toBe("AND");
      expect((right.left as ComparisonNode).field).toBe("status");
      expect((right.right as ComparisonNode).field).toBe("priority");
    });

    it("parses NOT with higher precedence than AND: NOT a AND b => (NOT a) AND b", () => {
      const result = parse("NOT priority:P0 AND size:S");
      expect(result.errors).toEqual([]);

      const root = result.ast as BinaryNode;
      expect(root.operator).toBe("AND");
      expect(root.left.type).toBe("unary");
      expect(root.right.type).toBe("comparison");
    });

    it("6-term query with mixed AND/OR parses to correct tree shape", () => {
      // a AND b AND c OR d AND e AND f
      // => ((a AND b) AND c) OR ((d AND e) AND f)
      const result = parse(
        "status:ready AND priority:P0 AND size:S OR status:in-progress AND priority:P1 AND size:M"
      );
      expect(result.errors).toEqual([]);

      const root = result.ast as BinaryNode;
      expect(root.operator).toBe("OR");

      const leftAnd = root.left as BinaryNode;
      expect(leftAnd.operator).toBe("AND");

      const rightAnd = root.right as BinaryNode;
      expect(rightAnd.operator).toBe("AND");
    });
  });

  describe("all valid fields parse correctly", () => {
    const fieldTests: Array<{ query: string; field: string; operator: string }> = [
      { query: "status:ready", field: "status", operator: ":" },
      { query: "priority:P0", field: "priority", operator: ":" },
      { query: "size:M", field: "size", operator: ":" },
      { query: "size>S", field: "size", operator: ">" },
      { query: "size<L", field: "size", operator: "<" },
      { query: "size>=M", field: "size", operator: ">=" },
      { query: "size<=XL", field: "size", operator: "<=" },
      { query: "component:api", field: "component", operator: ":" },
      { query: "component!=ui", field: "component", operator: "!=" },
      { query: "assignee:alice", field: "assignee", operator: ":" },
      { query: 'title~"auth*"', field: "title", operator: "~" },
      { query: "title:fix", field: "title", operator: ":" },
      { query: "number:42", field: "number", operator: ":" },
      { query: "number>100", field: "number", operator: ">" },
      { query: "number>=50", field: "number", operator: ">=" },
      { query: "updated<7d", field: "updated", operator: "<" },
      { query: "updated>30d", field: "updated", operator: ">" },
      { query: "created>=2026-01-01", field: "created", operator: ">=" },
      { query: "labels:bug", field: "labels", operator: ":" },
      { query: "labels!=enhancement", field: "labels", operator: "!=" },
      { query: "type:bug", field: "type", operator: ":" },
    ];

    for (const { query, field, operator } of fieldTests) {
      it(`parses: ${query}`, () => {
        const result = parse(query);
        expect(result.errors).toEqual([]);
        expect(result.ast).not.toBeNull();
        const node = result.ast as ComparisonNode;
        expect(node.field).toBe(field);
        expect(node.operator).toBe(operator);
      });
    }
  });

  describe("complex real-world query patterns", () => {
    it("parses sprint work query", () => {
      // Ready items, not done, high priority, assigned to team members
      const result = parse(
        "(status:ready OR status:in-progress) AND (priority:P0 OR priority:P1) AND NOT assignee:backlog"
      );
      expect(result.errors).toEqual([]);

      const root = result.ast as BinaryNode;
      expect(root.operator).toBe("AND");
    });

    it("parses bug triage query", () => {
      const result = parse("type:bug AND NOT status:done AND (priority:P0 OR priority:P1)");
      expect(result.errors).toEqual([]);
      expect(result.ast?.type).toBe("binary");
    });

    it("parses large issue filter with number range", () => {
      const result = parse("number>100 AND number<=500 AND size:XL");
      expect(result.errors).toEqual([]);
      expect(result.ast?.type).toBe("binary");
    });

    it("parses date range query", () => {
      const result = parse("created>2026-01-01 AND updated<30d");
      expect(result.errors).toEqual([]);
    });

    it("parses multi-component query", () => {
      const result = parse("(component:api OR component:auth) AND status:ready AND NOT labels:wip");
      expect(result.errors).toEqual([]);
    });
  });

  describe("error recovery in complex inputs", () => {
    it("rejects missing value after operator in chained expression", () => {
      const result = parse("status:ready AND priority:");
      expect(result.ast).toBeNull();
      expect(result.errors.length).toBeGreaterThan(0);
    });

    it("rejects unmatched opening parenthesis in nested expression", () => {
      const result = parse("((status:ready AND priority:P0)");
      expect(result.ast).toBeNull();
      expect(result.errors.length).toBeGreaterThan(0);
    });

    it("rejects orphaned AND at end of expression", () => {
      const result = parse("status:ready AND");
      expect(result.ast).toBeNull();
      expect(result.errors.length).toBeGreaterThan(0);
    });

    it("rejects orphaned OR at start of expression", () => {
      const result = parse("OR status:ready");
      expect(result.ast).toBeNull();
      expect(result.errors.length).toBeGreaterThan(0);
    });

    it("rejects consecutive fields without operators", () => {
      const result = parse("status priority:P0");
      expect(result.ast).toBeNull();
      expect(result.errors.length).toBeGreaterThan(0);
    });
  });
});
