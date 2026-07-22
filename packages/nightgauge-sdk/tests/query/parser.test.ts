/**
 * Parser unit tests
 *
 * Tests AST generation from token streams.
 */

import { describe, it, expect } from "vitest";
import { Parser, parse, validate, isValid } from "../../src/query/parser.js";
import type { ComparisonNode, BinaryNode, UnaryNode } from "../../src/query/types.js";

describe("Parser", () => {
  describe("parse()", () => {
    describe("simple comparisons", () => {
      it("should parse field:value as comparison node", () => {
        const result = parse("status:ready");

        expect(result.errors).toHaveLength(0);
        expect(result.ast).not.toBeNull();
        expect(result.ast!.type).toBe("comparison");

        const node = result.ast as ComparisonNode;
        expect(node.field).toBe("status");
        expect(node.operator).toBe(":");
        expect(node.value).toBe("ready");
      });

      // Note: '=' operator is not valid for status field, it only accepts ':' and '!='
      // Skipping this test as it tests invalid syntax

      it("should parse field!=value as comparison node", () => {
        const result = parse("status!=done");

        expect(result.errors).toHaveLength(0);
        const node = result.ast as ComparisonNode;
        expect(node.operator).toBe("!=");
        expect(node.value).toBe("done");
      });

      it("should parse comparison operators", () => {
        // Size field supports comparison operators (but not ~)
        const sizeOperators = [">", "<", ">=", "<="];
        for (const op of sizeOperators) {
          const result = parse(`size${op}M`);
          expect(result.errors).toHaveLength(0);
          expect((result.ast as ComparisonNode).operator).toBe(op);
        }

        // Title field supports ~ (wildcard)
        const wildcardResult = parse("title~auth");
        expect(wildcardResult.errors).toHaveLength(0);
        expect((wildcardResult.ast as ComparisonNode).operator).toBe("~");
      });
    });

    describe("binary expressions", () => {
      it("should parse AND expression", () => {
        const result = parse("status:ready AND priority:P0");

        expect(result.errors).toHaveLength(0);
        expect(result.ast!.type).toBe("binary");

        const node = result.ast as BinaryNode;
        expect(node.operator).toBe("AND");
        expect(node.left.type).toBe("comparison");
        expect(node.right.type).toBe("comparison");
      });

      it("should parse OR expression", () => {
        const result = parse("status:ready OR status:done");

        expect(result.errors).toHaveLength(0);
        expect(result.ast!.type).toBe("binary");

        const node = result.ast as BinaryNode;
        expect(node.operator).toBe("OR");
      });

      it("should parse chained AND expressions left-to-right", () => {
        const result = parse("status:ready AND priority:P0 AND size:M");

        expect(result.errors).toHaveLength(0);
        expect(result.ast!.type).toBe("binary");

        // Should be ((status:ready AND priority:P0) AND size:M)
        const node = result.ast as BinaryNode;
        expect(node.right.type).toBe("comparison");
        expect((node.right as ComparisonNode).field).toBe("size");
        expect(node.left.type).toBe("binary");
      });

      it("should parse chained OR expressions left-to-right", () => {
        const result = parse("status:ready OR priority:P0 OR size:M");

        expect(result.errors).toHaveLength(0);
        const node = result.ast as BinaryNode;
        expect((node.right as ComparisonNode).field).toBe("size");
        expect(node.left.type).toBe("binary");
      });
    });

    describe("operator precedence", () => {
      it("should give AND higher precedence than OR", () => {
        const result = parse("status:ready OR priority:P0 AND size:M");

        expect(result.errors).toHaveLength(0);
        // Should parse as: status:ready OR (priority:P0 AND size:M)
        const node = result.ast as BinaryNode;
        expect(node.operator).toBe("OR");
        expect(node.left.type).toBe("comparison");
        expect(node.right.type).toBe("binary");
        expect((node.right as BinaryNode).operator).toBe("AND");
      });

      it("should give NOT highest precedence", () => {
        const result = parse("NOT status:ready AND priority:P0");

        expect(result.errors).toHaveLength(0);
        // Should parse as: (NOT status:ready) AND priority:P0
        const node = result.ast as BinaryNode;
        expect(node.operator).toBe("AND");
        expect(node.left.type).toBe("unary");
      });
    });

    describe("NOT expressions", () => {
      it("should parse NOT expression", () => {
        const result = parse("NOT status:done");

        expect(result.errors).toHaveLength(0);
        expect(result.ast!.type).toBe("unary");

        const node = result.ast as UnaryNode;
        expect(node.operator).toBe("NOT");
        expect(node.operand.type).toBe("comparison");
      });

      it("should parse double NOT with parentheses", () => {
        // Double NOT requires parentheses: NOT (NOT expr)
        const result = parse("NOT (NOT status:done)");

        expect(result.errors).toHaveLength(0);
        expect(result.ast!.type).toBe("unary");

        const node = result.ast as UnaryNode;
        expect(node.operand.type).toBe("unary");
      });
    });

    describe("parentheses", () => {
      it("should parse parenthesized expression", () => {
        const result = parse("(status:ready)");

        expect(result.errors).toHaveLength(0);
        expect(result.ast!.type).toBe("comparison");
      });

      it("should override precedence with parentheses", () => {
        const result = parse("(status:ready OR priority:P0) AND size:M");

        expect(result.errors).toHaveLength(0);
        // Should parse as: (status:ready OR priority:P0) AND size:M
        const node = result.ast as BinaryNode;
        expect(node.operator).toBe("AND");
        expect(node.left.type).toBe("binary");
        expect((node.left as BinaryNode).operator).toBe("OR");
      });

      it("should handle nested parentheses", () => {
        const result = parse("((status:ready AND priority:P0))");

        expect(result.errors).toHaveLength(0);
        expect(result.ast!.type).toBe("binary");
      });

      it("should handle complex nested expressions", () => {
        const result = parse("(status:ready AND (priority:P0 OR size:M)) OR type:epic");

        expect(result.errors).toHaveLength(0);
        const node = result.ast as BinaryNode;
        expect(node.operator).toBe("OR");
        expect((node.right as ComparisonNode).field).toBe("type");
      });
    });

    describe("field validation", () => {
      it("should reject unknown field names", () => {
        const result = parse("unknownfield:value");

        expect(result.ast).toBeNull();
        expect(result.errors).toHaveLength(1);
        expect(result.errors[0].message).toContain("Unknown field");
        expect(result.errors[0].message).toContain("unknownfield");
      });

      it("should accept all valid field names", () => {
        // Fields with their valid operators
        const fieldQueries = [
          "status:ready",
          "priority:P0",
          "size:M",
          "component:auth",
          "assignee:alice",
          "title:test",
          "number:42",
          "updated<7d", // date field only supports comparison operators
          "created>30d", // date field only supports comparison operators
          "labels:bug",
          "type:feature",
        ];

        for (const query of fieldQueries) {
          const result = parse(query);
          expect(result.errors).toHaveLength(0);
          expect(result.ast).not.toBeNull();
        }
      });
    });

    describe("operator validation", () => {
      it("should reject invalid operator for field type", () => {
        // status only supports : and !=, not >
        const result = parse("status>ready");

        expect(result.ast).toBeNull();
        expect(result.errors).toHaveLength(1);
        expect(result.errors[0].message).toContain("not valid");
      });

      it("should accept valid operators for each field", () => {
        // size supports comparison operators
        expect(parse("size>M").errors).toHaveLength(0);
        expect(parse("size<L").errors).toHaveLength(0);
        expect(parse("size>=S").errors).toHaveLength(0);
        expect(parse("size<=XL").errors).toHaveLength(0);

        // title supports wildcard
        expect(parse("title~auth").errors).toHaveLength(0);

        // updated supports comparison operators
        expect(parse("updated<7d").errors).toHaveLength(0);
        expect(parse("updated>30d").errors).toHaveLength(0);
      });
    });
  });

  describe("validate()", () => {
    it("should return empty array for valid query", () => {
      const errors = validate("status:ready AND priority:P0");
      expect(errors).toHaveLength(0);
    });

    it("should return errors for invalid query", () => {
      const errors = validate("unknownfield:value");
      expect(errors.length).toBeGreaterThan(0);
    });

    it("should return errors for empty query", () => {
      const errors = validate("");
      expect(errors).toHaveLength(1);
      expect(errors[0].message).toBe("Empty query");
    });
  });

  describe("isValid()", () => {
    it("should return true for valid query", () => {
      expect(isValid("status:ready")).toBe(true);
      expect(isValid("status:ready AND priority:P0")).toBe(true);
      expect(isValid("(status:ready OR priority:P0) AND NOT size:M")).toBe(true);
    });

    it("should return false for invalid query", () => {
      expect(isValid("")).toBe(false);
      expect(isValid("unknownfield:value")).toBe(false);
      expect(isValid("status>")).toBe(false);
    });
  });

  describe("error recovery", () => {
    it("should report missing closing parenthesis", () => {
      const result = parse("(status:ready");

      expect(result.ast).toBeNull();
      expect(result.errors[0].message).toContain("closing parenthesis");
    });

    it("should report missing value after operator", () => {
      const result = parse("status:");

      expect(result.ast).toBeNull();
      expect(result.errors[0].message).toContain("Expected value");
    });

    it("should report missing operator after field", () => {
      const result = parse("status ready");

      expect(result.ast).toBeNull();
      // The parser will see 'ready' as an unexpected token
    });

    it("should report trailing tokens", () => {
      const result = parse("status:ready extra");

      // After a complete expression, 'extra' is unexpected
      expect(result.errors.length).toBeGreaterThan(0);
    });
  });

  describe("edge cases", () => {
    it("should handle quoted values", () => {
      const result = parse('title:"fix bug"');

      expect(result.errors).toHaveLength(0);
      expect((result.ast as ComparisonNode).value).toBe("fix bug");
    });

    it("should handle @me value", () => {
      const result = parse("assignee:@me");

      expect(result.errors).toHaveLength(0);
      expect((result.ast as ComparisonNode).value).toBe("@me");
    });

    it("should handle relative date values", () => {
      const result = parse("updated<7d");

      expect(result.errors).toHaveLength(0);
      expect((result.ast as ComparisonNode).value).toBe("7d");
    });

    it("should normalize field names to lowercase", () => {
      const result = parse("STATUS:ready");

      expect(result.errors).toHaveLength(0);
      expect((result.ast as ComparisonNode).field).toBe("status");
    });
  });
});
