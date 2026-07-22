/**
 * Lexer unit tests
 *
 * Tests tokenization of GQL query strings.
 */

import { describe, it, expect } from "vitest";
import { Lexer, tokenize } from "../../src/query/lexer.js";
import { LexerError, QueryTooLongError, InvalidCharacterError } from "../../src/query/errors.js";

describe("Lexer", () => {
  describe("tokenize()", () => {
    describe("simple queries", () => {
      it("should tokenize a simple field:value query", () => {
        const tokens = tokenize("status:ready");

        expect(tokens).toHaveLength(4);
        expect(tokens[0]).toEqual({
          type: "FIELD",
          value: "status",
          position: 0,
        });
        expect(tokens[1]).toEqual({
          type: "OPERATOR",
          value: ":",
          position: 6,
        });
        expect(tokens[2]).toEqual({
          type: "VALUE",
          value: "ready",
          position: 7,
        });
        expect(tokens[3]).toEqual({ type: "EOF", value: "", position: 12 });
      });

      it("should tokenize field:value with spaces", () => {
        const tokens = tokenize("status : ready");

        expect(tokens).toHaveLength(4);
        expect(tokens[0]).toEqual({
          type: "FIELD",
          value: "status",
          position: 0,
        });
        expect(tokens[1]).toEqual({
          type: "OPERATOR",
          value: ":",
          position: 7,
        });
        expect(tokens[2]).toEqual({
          type: "VALUE",
          value: "ready",
          position: 9,
        });
      });

      it("should tokenize priority values", () => {
        const tokens = tokenize("priority:P0");

        expect(tokens[0].type).toBe("FIELD");
        expect(tokens[0].value).toBe("priority");
        expect(tokens[2].type).toBe("VALUE");
        expect(tokens[2].value).toBe("P0");
      });
    });

    describe("operators", () => {
      it("should tokenize equality operator :", () => {
        const tokens = tokenize("status:ready");
        expect(tokens[1]).toEqual({
          type: "OPERATOR",
          value: ":",
          position: 6,
        });
      });

      it("should tokenize equality operator =", () => {
        const tokens = tokenize("status=ready");
        expect(tokens[1]).toEqual({
          type: "OPERATOR",
          value: "=",
          position: 6,
        });
      });

      it("should tokenize not-equal operator !=", () => {
        const tokens = tokenize("status!=done");
        expect(tokens[1]).toEqual({
          type: "OPERATOR",
          value: "!=",
          position: 6,
        });
      });

      it("should tokenize greater-than operator >", () => {
        const tokens = tokenize("size>M");
        expect(tokens[1]).toEqual({
          type: "OPERATOR",
          value: ">",
          position: 4,
        });
      });

      it("should tokenize less-than operator <", () => {
        const tokens = tokenize("updated<7d");
        expect(tokens[1]).toEqual({
          type: "OPERATOR",
          value: "<",
          position: 7,
        });
      });

      it("should tokenize greater-than-or-equal operator >=", () => {
        const tokens = tokenize("size>=M");
        expect(tokens[1]).toEqual({
          type: "OPERATOR",
          value: ">=",
          position: 4,
        });
      });

      it("should tokenize less-than-or-equal operator <=", () => {
        const tokens = tokenize("size<=L");
        expect(tokens[1]).toEqual({
          type: "OPERATOR",
          value: "<=",
          position: 4,
        });
      });

      it("should tokenize wildcard operator ~", () => {
        const tokens = tokenize("title~auth");
        expect(tokens[1]).toEqual({
          type: "OPERATOR",
          value: "~",
          position: 5,
        });
      });
    });

    describe("boolean keywords", () => {
      it("should tokenize AND keyword", () => {
        const tokens = tokenize("status:ready AND priority:P0");

        expect(tokens[3]).toEqual({ type: "AND", value: "AND", position: 13 });
      });

      it("should tokenize OR keyword", () => {
        const tokens = tokenize("status:ready OR status:done");

        expect(tokens[3]).toEqual({ type: "OR", value: "OR", position: 13 });
      });

      it("should tokenize NOT keyword", () => {
        const tokens = tokenize("NOT status:done");

        expect(tokens[0]).toEqual({ type: "NOT", value: "NOT", position: 0 });
      });

      it("should handle lowercase keywords", () => {
        const tokens = tokenize("status:ready and priority:P0");

        expect(tokens[3]).toEqual({ type: "AND", value: "AND", position: 13 });
      });

      it("should handle mixed case keywords", () => {
        const tokensOr = tokenize("status:ready or priority:P0");
        expect(tokensOr[3].type).toBe("OR");

        const tokensNot = tokenize("not status:done");
        expect(tokensNot[0].type).toBe("NOT");
      });
    });

    describe("parentheses", () => {
      it("should tokenize parentheses", () => {
        const tokens = tokenize("(status:ready)");

        expect(tokens[0]).toEqual({ type: "LPAREN", value: "(", position: 0 });
        expect(tokens[4]).toEqual({ type: "RPAREN", value: ")", position: 13 });
      });

      it("should tokenize nested parentheses", () => {
        const tokens = tokenize("((status:ready))");

        expect(tokens[0].type).toBe("LPAREN");
        expect(tokens[1].type).toBe("LPAREN");
        expect(tokens[5].type).toBe("RPAREN");
        expect(tokens[6].type).toBe("RPAREN");
      });
    });

    describe("quoted strings", () => {
      it("should tokenize double-quoted strings", () => {
        const tokens = tokenize('title:"fix bug"');

        expect(tokens[2]).toEqual({
          type: "VALUE",
          value: "fix bug",
          position: 6,
        });
      });

      it("should tokenize single-quoted strings", () => {
        const tokens = tokenize("title:'fix bug'");

        expect(tokens[2]).toEqual({
          type: "VALUE",
          value: "fix bug",
          position: 6,
        });
      });

      it("should handle escaped quotes in strings", () => {
        const tokens = tokenize('title:"fix \\"bug\\""');

        expect(tokens[2].value).toBe('fix "bug"');
      });

      it("should throw on unterminated string", () => {
        expect(() => tokenize('title:"fix bug')).toThrow(LexerError);
        expect(() => tokenize('title:"fix bug')).toThrow("Unterminated string");
      });
    });

    describe("special values", () => {
      it("should tokenize @me value", () => {
        const tokens = tokenize("assignee:@me");

        expect(tokens[2]).toEqual({ type: "VALUE", value: "@me", position: 9 });
      });

      it("should tokenize issue number with #", () => {
        const tokens = tokenize("number:#42");

        expect(tokens[2]).toEqual({ type: "VALUE", value: "#42", position: 7 });
      });

      it("should tokenize numeric values", () => {
        const tokens = tokenize("number:42");

        expect(tokens[2]).toEqual({ type: "VALUE", value: "42", position: 7 });
      });

      it("should tokenize relative date values", () => {
        const tokens = tokenize("updated<7d");

        expect(tokens[2]).toEqual({ type: "VALUE", value: "7d", position: 8 });
      });
    });

    describe("complex queries", () => {
      it("should tokenize query with AND and OR", () => {
        const tokens = tokenize("status:ready AND (priority:P0 OR priority:P1)");

        const types = tokens.map((t) => t.type);
        expect(types).toEqual([
          "FIELD",
          "OPERATOR",
          "VALUE",
          "AND",
          "LPAREN",
          "FIELD",
          "OPERATOR",
          "VALUE",
          "OR",
          "FIELD",
          "OPERATOR",
          "VALUE",
          "RPAREN",
          "EOF",
        ]);
      });

      it("should tokenize query with NOT", () => {
        const tokens = tokenize("status:ready AND NOT priority:P2");

        expect(tokens[3].type).toBe("AND");
        expect(tokens[4].type).toBe("NOT");
      });
    });
  });

  describe("error handling", () => {
    it("should throw on query exceeding max length", () => {
      const longQuery = "a".repeat(2001);

      expect(() => tokenize(longQuery)).toThrow(QueryTooLongError);
    });

    it("should throw on null byte in query", () => {
      expect(() => tokenize("status:\x00ready")).toThrow(InvalidCharacterError);
    });

    it("should throw on control characters", () => {
      expect(() => tokenize("status:\x01ready")).toThrow(InvalidCharacterError);
    });

    it("should throw on unexpected character", () => {
      expect(() => tokenize("status:ready $ invalid")).toThrow(LexerError);
    });
  });

  describe("edge cases", () => {
    it("should handle empty query", () => {
      const tokens = tokenize("");

      expect(tokens).toHaveLength(1);
      expect(tokens[0].type).toBe("EOF");
    });

    it("should handle whitespace-only query", () => {
      const tokens = tokenize("   ");

      expect(tokens).toHaveLength(1);
      expect(tokens[0].type).toBe("EOF");
    });

    it("should handle query with tabs and newlines", () => {
      const tokens = tokenize("status:ready\tAND\npriority:P0");

      expect(tokens[0].value).toBe("status");
      expect(tokens[3].type).toBe("AND");
      expect(tokens[4].value).toBe("priority");
    });

    it("should preserve field case in values", () => {
      const tokens = tokenize("priority:P0");

      expect(tokens[2].value).toBe("P0");
    });

    it("should handle hyphenated values", () => {
      const tokens = tokenize("status:in-progress");

      expect(tokens[2].value).toBe("in-progress");
    });
  });
});
