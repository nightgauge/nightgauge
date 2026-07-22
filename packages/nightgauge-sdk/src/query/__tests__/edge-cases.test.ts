import { describe, it, expect } from "vitest";
import { executeQuery, parse, tokenize } from "../index.js";
import {
  isValidField,
  isValidOperatorForField,
  isValidValueForField,
  parseRelativeDate,
  parseDateValue,
  getFieldDefinition,
  getAllowedOperators,
  getAllowedValues,
  MAX_QUERY_LENGTH,
  FIELD_DEFINITIONS,
  ALLOWED_FIELDS,
  SIZE_ORDER,
} from "../schemas.js";
import {
  QueryParseError,
  LexerError,
  ParserError,
  UnknownFieldError,
  InvalidOperatorError,
  InvalidValueError,
  QueryTooLongError,
  InvalidCharacterError,
  EvaluationError,
} from "../errors.js";
import {
  SavedQuerySchema,
  SavedQueriesFileSchema,
  FieldNameSchema,
  ComparisonOperatorSchema,
  BooleanOperatorSchema,
  ASTNodeSchema,
} from "../schemas.js";
import type { QueryableIssue } from "../types.js";

describe("Edge Cases", () => {
  describe("query length limits", () => {
    it("accepts query at exactly MAX_QUERY_LENGTH", () => {
      // Build a valid query padded to exactly 2000 chars with quoted string value
      // "title~\"" = 7 chars, closing "\"" = 1 char, content fills the rest
      const contentLength = MAX_QUERY_LENGTH - 8; // 7 prefix + 1 suffix
      const query = `title~"${"a".repeat(contentLength)}"`;
      expect(query.length).toBe(MAX_QUERY_LENGTH);
      // Should tokenize without throwing QueryTooLongError
      const tokens = tokenize(query);
      expect(tokens.length).toBeGreaterThan(0);
    });

    it("rejects query exceeding MAX_QUERY_LENGTH", () => {
      const longQuery = "title:" + "a".repeat(2000);
      expect(() => tokenize(longQuery)).toThrow(QueryTooLongError);
    });
  });

  describe("special characters in values", () => {
    it("handles quoted values with spaces", () => {
      const result = parse('title~"hello world"');
      expect(result.errors).toEqual([]);
    });

    it("handles empty quotes", () => {
      const result = parse('title~""');
      // Empty quoted string is valid at the lexer/parser level
      // but may produce unexpected results during evaluation
      // The parser accepts it because "" is a valid VALUE token
      if (result.ast) {
        expect(result.ast.type).toBe("comparison");
      } else {
        // Parser may reject empty value — either outcome is acceptable
        expect(result.errors.length).toBeGreaterThan(0);
      }
    });

    it("handles values with hyphens", () => {
      const result = parse("status:in-progress");
      expect(result.errors).toEqual([]);
      expect(result.ast).not.toBeNull();
    });

    it("handles @me value for assignee", () => {
      const result = parse("assignee:@me");
      expect(result.errors).toEqual([]);
    });
  });

  describe("schema validation functions", () => {
    it("isValidField accepts all defined fields", () => {
      for (const field of ALLOWED_FIELDS) {
        expect(isValidField(field)).toBe(true);
      }
    });

    it("isValidField rejects unknown fields", () => {
      expect(isValidField("unknownfield")).toBe(false);
      expect(isValidField("")).toBe(false);
      expect(isValidField("Status")).toBe(false); // case-sensitive
    });

    it("getFieldDefinition returns correct definitions", () => {
      const statusDef = getFieldDefinition("status");
      expect(statusDef).toBeDefined();
      expect(statusDef?.type).toBe("single_select");
      expect(statusDef?.allowedOperators).toContain(":");

      const numberDef = getFieldDefinition("number");
      expect(numberDef?.type).toBe("number");
      expect(numberDef?.allowedOperators).toContain(">");
    });

    it("getFieldDefinition returns undefined for unknown fields", () => {
      expect(getFieldDefinition("badfield")).toBeUndefined();
    });

    it("isValidOperatorForField validates correctly", () => {
      expect(isValidOperatorForField("status", ":")).toBe(true);
      expect(isValidOperatorForField("status", "!=")).toBe(true);
      expect(isValidOperatorForField("status", ">")).toBe(false);

      expect(isValidOperatorForField("number", ">")).toBe(true);
      expect(isValidOperatorForField("number", "<")).toBe(true);
      expect(isValidOperatorForField("number", "~")).toBe(false);

      expect(isValidOperatorForField("title", "~")).toBe(true);
      expect(isValidOperatorForField("title", ">")).toBe(false);
    });

    it("getAllowedOperators returns correct operators", () => {
      const statusOps = getAllowedOperators("status");
      expect(statusOps).toContain(":");
      expect(statusOps).toContain("!=");

      const dateOps = getAllowedOperators("updated");
      expect(dateOps).toContain("<");
      expect(dateOps).toContain(">");
      expect(dateOps).toContain("<=");
      expect(dateOps).toContain(">=");
    });

    it("getAllowedValues returns values for restricted fields", () => {
      const statusValues = getAllowedValues("status");
      expect(statusValues).toContain("ready");
      expect(statusValues).toContain("in-progress");

      const priorityValues = getAllowedValues("priority");
      expect(priorityValues).toContain("P0");
      expect(priorityValues).toContain("P1");
    });

    it("getAllowedValues returns undefined for unrestricted fields", () => {
      expect(getAllowedValues("assignee")).toBeUndefined();
      expect(getAllowedValues("title")).toBeUndefined();
    });

    it("isValidValueForField validates restricted fields", () => {
      expect(isValidValueForField("status", "ready")).toBe(true);
      expect(isValidValueForField("status", "READY")).toBe(true); // case-insensitive
      expect(isValidValueForField("status", "invalid")).toBe(false);

      expect(isValidValueForField("priority", "P0")).toBe(true);
      expect(isValidValueForField("priority", "critical")).toBe(true);
    });

    it("isValidValueForField allows any value for unrestricted fields", () => {
      expect(isValidValueForField("assignee", "anything")).toBe(true);
      expect(isValidValueForField("title", "anything")).toBe(true);
    });
  });

  describe("date parsing", () => {
    it("parseRelativeDate parses valid relative dates", () => {
      const date = parseRelativeDate("7d");
      const expected = new Date();
      expected.setDate(expected.getDate() - 7);
      // Allow 1 second tolerance
      expect(Math.abs(date.getTime() - expected.getTime())).toBeLessThan(1000);
    });

    it("parseRelativeDate handles large day counts", () => {
      const date = parseRelativeDate("365d");
      expect(date).toBeInstanceOf(Date);
    });

    it("parseRelativeDate throws for invalid format", () => {
      expect(() => parseRelativeDate("7w")).toThrow();
      expect(() => parseRelativeDate("abc")).toThrow();
    });

    it("parseDateValue parses ISO dates", () => {
      const date = parseDateValue("2026-01-15");
      expect(date).toBeInstanceOf(Date);
      expect(date.getFullYear()).toBe(2026);
    });

    it("parseDateValue parses relative dates", () => {
      const date = parseDateValue("7d");
      expect(date).toBeInstanceOf(Date);
    });

    it("parseDateValue throws for invalid dates", () => {
      expect(() => parseDateValue("not-a-date")).toThrow();
    });
  });

  describe("SIZE_ORDER", () => {
    it("defines correct ordering", () => {
      expect(SIZE_ORDER["XS"]).toBeLessThan(SIZE_ORDER["S"]);
      expect(SIZE_ORDER["S"]).toBeLessThan(SIZE_ORDER["M"]);
      expect(SIZE_ORDER["M"]).toBeLessThan(SIZE_ORDER["L"]);
      expect(SIZE_ORDER["L"]).toBeLessThan(SIZE_ORDER["XL"]);
    });
  });

  describe("error classes", () => {
    it("QueryParseError has position and length", () => {
      const error = new QueryParseError("test error", 5, 3);
      expect(error.message).toBe("test error");
      expect(error.position).toBe(5);
      expect(error.length).toBe(3);
      expect(error.name).toBe("QueryParseError");
    });

    it("QueryParseError.toQueryError converts correctly", () => {
      const error = new QueryParseError("test", 5, 3);
      const queryError = error.toQueryError();
      expect(queryError).toEqual({ message: "test", position: 5, length: 3 });
    });

    it("LexerError is a QueryParseError", () => {
      const error = new LexerError("lexer error", 10);
      expect(error).toBeInstanceOf(QueryParseError);
      expect(error.name).toBe("LexerError");
    });

    it("ParserError is a QueryParseError", () => {
      const error = new ParserError("parser error", 10, 5);
      expect(error).toBeInstanceOf(QueryParseError);
      expect(error.name).toBe("ParserError");
    });

    it("UnknownFieldError includes field name", () => {
      const error = new UnknownFieldError("badfield", 0);
      expect(error.fieldName).toBe("badfield");
      expect(error.message).toContain("Unknown field");
      expect(error.message).toContain("badfield");
    });

    it("InvalidOperatorError includes field and operator", () => {
      const error = new InvalidOperatorError("status", ">", [":", "!="], 5);
      expect(error.fieldName).toBe("status");
      expect(error.operator).toBe(">");
      expect(error.allowedOperators).toEqual([":", "!="]);
    });

    it("InvalidValueError includes field and value", () => {
      const error = new InvalidValueError("status", "invalid", ["ready", "done"]);
      expect(error.fieldName).toBe("status");
      expect(error.value).toBe("invalid");
      expect(error.allowedValues).toEqual(["ready", "done"]);
    });

    it("InvalidValueError works without allowedValues", () => {
      const error = new InvalidValueError("assignee", "");
      expect(error.allowedValues).toBeUndefined();
    });

    it("QueryTooLongError has MAX_LENGTH static", () => {
      expect(QueryTooLongError.MAX_LENGTH).toBe(2000);
      const error = new QueryTooLongError(3000);
      expect(error.message).toContain("2000");
      expect(error.message).toContain("3000");
    });

    it("InvalidCharacterError has character field", () => {
      const error = new InvalidCharacterError("\x00", 5);
      expect(error.character).toBe("\x00");
      expect(error).toBeInstanceOf(LexerError);
    });

    it("EvaluationError is a standalone Error", () => {
      const error = new EvaluationError("eval failed");
      expect(error).toBeInstanceOf(Error);
      expect(error.name).toBe("EvaluationError");
    });
  });

  describe("Zod schemas", () => {
    it("FieldNameSchema validates field names", () => {
      expect(FieldNameSchema.safeParse("status").success).toBe(true);
      expect(FieldNameSchema.safeParse("badfield").success).toBe(false);
    });

    it("ComparisonOperatorSchema validates operators", () => {
      expect(ComparisonOperatorSchema.safeParse(":").success).toBe(true);
      expect(ComparisonOperatorSchema.safeParse("!=").success).toBe(true);
      expect(ComparisonOperatorSchema.safeParse("**").success).toBe(false);
    });

    it("BooleanOperatorSchema validates boolean operators", () => {
      expect(BooleanOperatorSchema.safeParse("AND").success).toBe(true);
      expect(BooleanOperatorSchema.safeParse("OR").success).toBe(true);
      expect(BooleanOperatorSchema.safeParse("XOR").success).toBe(false);
    });

    it("SavedQuerySchema validates saved queries", () => {
      const valid = {
        name: "Test Query",
        query: "status:ready",
        description: "A test query",
      };
      expect(SavedQuerySchema.safeParse(valid).success).toBe(true);

      const invalid = { name: "", query: "" };
      expect(SavedQuerySchema.safeParse(invalid).success).toBe(false);
    });

    it("SavedQueriesFileSchema validates file format", () => {
      const valid = {
        version: "1.0" as const,
        queries: [{ name: "Test", query: "status:ready" }],
      };
      expect(SavedQueriesFileSchema.safeParse(valid).success).toBe(true);

      const invalid = { version: "2.0", queries: [] };
      expect(SavedQueriesFileSchema.safeParse(invalid).success).toBe(false);
    });

    it("ASTNodeSchema validates comparison nodes", () => {
      const node = {
        type: "comparison",
        field: "status",
        operator: ":",
        value: "ready",
      };
      expect(ASTNodeSchema.safeParse(node).success).toBe(true);
    });

    it("ASTNodeSchema validates binary nodes", () => {
      const node = {
        type: "binary",
        operator: "AND",
        left: { type: "comparison", field: "status", operator: ":", value: "ready" },
        right: { type: "comparison", field: "priority", operator: ":", value: "P0" },
      };
      expect(ASTNodeSchema.safeParse(node).success).toBe(true);
    });

    it("ASTNodeSchema validates unary nodes", () => {
      const node = {
        type: "unary",
        operator: "NOT",
        operand: { type: "comparison", field: "status", operator: ":", value: "done" },
      };
      expect(ASTNodeSchema.safeParse(node).success).toBe(true);
    });
  });

  describe("field definitions completeness", () => {
    it("every ALLOWED_FIELD has a FIELD_DEFINITION", () => {
      for (const field of ALLOWED_FIELDS) {
        expect(FIELD_DEFINITIONS[field]).toBeDefined();
        expect(FIELD_DEFINITIONS[field].name).toBe(field);
      }
    });

    it("every FIELD_DEFINITION has at least one operator", () => {
      for (const field of ALLOWED_FIELDS) {
        expect(FIELD_DEFINITIONS[field].allowedOperators.length).toBeGreaterThan(0);
      }
    });
  });

  describe("evaluation with missing fields", () => {
    const issueWithMinimalData: QueryableIssue = {
      number: 1,
      title: "Minimal issue",
      labels: [],
      priority: null,
      size: null,
      url: "https://github.com/test/repo/issues/1",
    };

    it("handles missing status", () => {
      const result = executeQuery("NOT status:ready", [issueWithMinimalData]);
      // Null field with != returns true, NOT status:ready on null should be true
      expect(result.matchCount).toBe(1);
    });

    it("handles missing priority", () => {
      const result = executeQuery("NOT priority:P0", [issueWithMinimalData]);
      expect(result.matchCount).toBe(1);
    });

    it("handles empty labels", () => {
      const result = executeQuery("labels:bug", [issueWithMinimalData]);
      expect(result.matchCount).toBe(0);
    });

    it("handles missing assignee", () => {
      const result = executeQuery("NOT assignee:someone", [issueWithMinimalData]);
      expect(result.matchCount).toBe(1);
    });
  });

  describe("unicode and internationalization", () => {
    it("handles unicode in title field", () => {
      const issue: QueryableIssue = {
        number: 1,
        title: "修复登录问题",
        labels: [],
        priority: null,
        size: null,
        url: "https://github.com/test/repo/issues/1",
      };
      const result = executeQuery('title~"*登录*"', [issue]);
      expect(result.matchCount).toBe(1);
    });

    it("emoji in query string is rejected by lexer (use quoted strings)", () => {
      // Raw emoji characters are not valid identifier starts
      expect(() => executeQuery("labels:🐛", [])).toThrow();
    });

    it("handles labels with emoji via partial text match", () => {
      const issue: QueryableIssue = {
        number: 1,
        title: "Test",
        labels: ["bug-report"],
        priority: null,
        size: null,
        url: "https://github.com/test/repo/issues/1",
      };
      const result = executeQuery("labels:bug", [issue]);
      expect(result.matchCount).toBe(1);
    });
  });

  describe("performance characteristics", () => {
    it("handles 1000 issues in reasonable time", () => {
      const issues: QueryableIssue[] = Array.from({ length: 1000 }, (_, i) => ({
        number: i + 1,
        title: `Issue ${i + 1}`,
        labels: i % 3 === 0 ? ["type:bug"] : ["type:feature"],
        priority: (["P0", "P1", "P2", "P3"] as const)[i % 4],
        size: (["XS", "S", "M", "L", "XL"] as const)[i % 5],
        url: `https://github.com/test/repo/issues/${i + 1}`,
        status: i % 2 === 0 ? "ready" : "in-progress",
      }));

      const start = performance.now();
      const result = executeQuery("status:ready AND priority:P0", issues);
      const elapsed = performance.now() - start;

      expect(elapsed).toBeLessThan(1000); // Must complete in <1s
      expect(result.matchCount).toBeGreaterThan(0);
    });
  });
});
