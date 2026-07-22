import { describe, it, expect } from "vitest";
import { Parser, parse, validate, isValid } from "../parser.js";
import type { ComparisonNode, BinaryNode, UnaryNode } from "../types.js";

describe("Parser", () => {
  describe("simple comparisons", () => {
    it("parses field:value expression", () => {
      const result = parse("status:ready");
      expect(result.errors).toEqual([]);
      expect(result.ast).toEqual({
        type: "comparison",
        field: "status",
        operator: ":",
        value: "ready",
      });
    });

    it("rejects = operator on fields that only support :", () => {
      // priority only allows : and != operators
      const result = parse("priority=P0");
      expect(result.ast).toBeNull();
      expect(result.errors.length).toBeGreaterThan(0);
      expect(result.errors[0].message).toContain("not valid for field");
    });

    it("parses field!=value expression", () => {
      const result = parse("status!=done");
      expect(result.errors).toEqual([]);
      expect(result.ast).toEqual({
        type: "comparison",
        field: "status",
        operator: "!=",
        value: "done",
      });
    });

    it("parses comparison operators for date fields", () => {
      const result = parse("updated<7d");
      expect(result.errors).toEqual([]);
      expect(result.ast).toEqual({
        type: "comparison",
        field: "updated",
        operator: "<",
        value: "7d",
      });
    });

    it("parses comparison operators for number fields", () => {
      const result = parse("number>100");
      expect(result.errors).toEqual([]);
      expect(result.ast).toEqual({
        type: "comparison",
        field: "number",
        operator: ">",
        value: "100",
      });
    });

    it("parses wildcard operator", () => {
      const result = parse('title~"auth*"');
      expect(result.errors).toEqual([]);
      expect(result.ast).toEqual({
        type: "comparison",
        field: "title",
        operator: "~",
        value: "auth*",
      });
    });

    it("normalizes field names to lowercase", () => {
      const result = parse("Status:ready");
      expect(result.errors).toEqual([]);
      expect((result.ast as ComparisonNode).field).toBe("status");
    });
  });

  describe("boolean operators", () => {
    it("parses AND expression", () => {
      const result = parse("status:ready AND priority:P0");
      expect(result.errors).toEqual([]);
      const ast = result.ast as BinaryNode;
      expect(ast.type).toBe("binary");
      expect(ast.operator).toBe("AND");
      expect((ast.left as ComparisonNode).field).toBe("status");
      expect((ast.right as ComparisonNode).field).toBe("priority");
    });

    it("parses OR expression", () => {
      const result = parse("priority:P0 OR priority:P1");
      expect(result.errors).toEqual([]);
      const ast = result.ast as BinaryNode;
      expect(ast.type).toBe("binary");
      expect(ast.operator).toBe("OR");
    });

    it("parses NOT expression", () => {
      const result = parse("NOT status:done");
      expect(result.errors).toEqual([]);
      const ast = result.ast as UnaryNode;
      expect(ast.type).toBe("unary");
      expect(ast.operator).toBe("NOT");
      expect((ast.operand as ComparisonNode).field).toBe("status");
    });
  });

  describe("operator precedence", () => {
    it("AND binds tighter than OR", () => {
      // "a AND b OR c" should parse as "(a AND b) OR c"
      const result = parse("status:ready AND priority:P0 OR priority:P1");
      expect(result.errors).toEqual([]);

      const ast = result.ast as BinaryNode;
      expect(ast.operator).toBe("OR");
      expect((ast.left as BinaryNode).operator).toBe("AND");
    });

    it("NOT binds tighter than AND", () => {
      // "NOT a AND b" should parse as "(NOT a) AND b"
      const result = parse("NOT status:done AND priority:P0");
      expect(result.errors).toEqual([]);

      const ast = result.ast as BinaryNode;
      expect(ast.operator).toBe("AND");
      expect((ast.left as UnaryNode).type).toBe("unary");
      expect((ast.left as UnaryNode).operator).toBe("NOT");
    });

    it("precedence matches issue acceptance criteria", () => {
      // "status:ready AND priority:high OR priority:critical"
      // should parse as "(status:ready AND priority:high) OR priority:critical"
      const result = parse("status:ready AND priority:high OR priority:critical");
      expect(result.errors).toEqual([]);

      const ast = result.ast as BinaryNode;
      expect(ast.operator).toBe("OR");
      expect((ast.left as BinaryNode).operator).toBe("AND");
      expect((ast.right as ComparisonNode).value).toBe("critical");
    });
  });

  describe("parentheses", () => {
    it("overrides operator precedence with parentheses", () => {
      // "(status:ready OR status:in-progress) AND priority:P0"
      const result = parse("(status:ready OR status:in-progress) AND priority:P0");
      expect(result.errors).toEqual([]);

      const ast = result.ast as BinaryNode;
      expect(ast.operator).toBe("AND");
      expect((ast.left as BinaryNode).operator).toBe("OR");
    });

    it("handles nested parentheses", () => {
      const result = parse("((status:ready))");
      expect(result.errors).toEqual([]);
      expect((result.ast as ComparisonNode).field).toBe("status");
    });

    it("handles complex nested parentheses", () => {
      const result = parse("(status:ready OR status:in-progress) AND (priority:P0 OR priority:P1)");
      expect(result.errors).toEqual([]);

      const ast = result.ast as BinaryNode;
      expect(ast.operator).toBe("AND");
      expect((ast.left as BinaryNode).operator).toBe("OR");
      expect((ast.right as BinaryNode).operator).toBe("OR");
    });
  });

  describe("chained operators", () => {
    it("parses multiple AND operators", () => {
      const result = parse("status:ready AND priority:P0 AND size:S");
      expect(result.errors).toEqual([]);

      const ast = result.ast as BinaryNode;
      expect(ast.operator).toBe("AND");
      expect((ast.left as BinaryNode).operator).toBe("AND");
    });

    it("parses multiple OR operators", () => {
      const result = parse("priority:P0 OR priority:P1 OR priority:P2");
      expect(result.errors).toEqual([]);

      const ast = result.ast as BinaryNode;
      expect(ast.operator).toBe("OR");
    });
  });

  describe("error handling", () => {
    it("returns error for empty query", () => {
      const result = parse("");
      expect(result.ast).toBeNull();
      expect(result.errors).toHaveLength(1);
      expect(result.errors[0].message).toBe("Empty query");
    });

    it("returns error for unknown field", () => {
      const result = parse("unknownfield:value");
      expect(result.ast).toBeNull();
      expect(result.errors).toHaveLength(1);
      expect(result.errors[0].message).toContain("Unknown field");
    });

    it("returns error for invalid operator on field", () => {
      const result = parse("status>ready");
      expect(result.ast).toBeNull();
      expect(result.errors).toHaveLength(1);
      expect(result.errors[0].message).toContain("not valid for field");
    });

    it("returns error for missing closing parenthesis", () => {
      const result = parse("(status:ready AND priority:P0");
      expect(result.ast).toBeNull();
      expect(result.errors.length).toBeGreaterThan(0);
    });

    it("returns error for missing value after operator", () => {
      const result = parse("status:");
      expect(result.ast).toBeNull();
      expect(result.errors.length).toBeGreaterThan(0);
    });

    it("returns error for missing operator after field", () => {
      const result = parse("status ready");
      expect(result.ast).toBeNull();
      expect(result.errors.length).toBeGreaterThan(0);
    });

    it("returns error for trailing tokens", () => {
      const result = parse("status:ready status:done");
      expect(result.ast).toBeNull();
      expect(result.errors.length).toBeGreaterThan(0);
    });
  });

  describe("validate function", () => {
    it("returns empty array for valid query", () => {
      expect(validate("status:ready")).toEqual([]);
    });

    it("returns errors for invalid query", () => {
      const errors = validate("unknownfield:value");
      expect(errors.length).toBeGreaterThan(0);
    });
  });

  describe("isValid function", () => {
    it("returns true for valid query", () => {
      expect(isValid("status:ready")).toBe(true);
    });

    it("returns false for invalid query", () => {
      expect(isValid("")).toBe(false);
    });

    it("returns false for unknown field", () => {
      expect(isValid("unknownfield:value")).toBe(false);
    });
  });

  describe("Parser class", () => {
    it("can be instantiated directly", () => {
      const parser = new Parser("status:ready");
      const result = parser.parse();
      expect(result.ast).not.toBeNull();
    });
  });
});
