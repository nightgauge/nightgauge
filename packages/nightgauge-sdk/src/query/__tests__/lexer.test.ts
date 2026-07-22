import { describe, it, expect } from "vitest";
import { Lexer, tokenize } from "../lexer.js";
import { LexerError, InvalidCharacterError, QueryTooLongError } from "../errors.js";

describe("Lexer", () => {
  describe("basic tokenization", () => {
    it("tokenizes simple field:value expression", () => {
      const tokens = tokenize("status:ready");
      expect(tokens).toEqual([
        { type: "FIELD", value: "status", position: 0 },
        { type: "OPERATOR", value: ":", position: 6 },
        { type: "VALUE", value: "ready", position: 7 },
        { type: "EOF", value: "", position: 12 },
      ]);
    });

    it("tokenizes field=value with equals operator", () => {
      const tokens = tokenize("status=ready");
      expect(tokens).toEqual([
        { type: "FIELD", value: "status", position: 0 },
        { type: "OPERATOR", value: "=", position: 6 },
        { type: "VALUE", value: "ready", position: 7 },
        { type: "EOF", value: "", position: 12 },
      ]);
    });

    it("tokenizes field!=value with not-equal operator", () => {
      const tokens = tokenize("status!=done");
      expect(tokens).toEqual([
        { type: "FIELD", value: "status", position: 0 },
        { type: "OPERATOR", value: "!=", position: 6 },
        { type: "VALUE", value: "done", position: 8 },
        { type: "EOF", value: "", position: 12 },
      ]);
    });

    it("tokenizes comparison operators (<, >, <=, >=)", () => {
      const tokensLt = tokenize("updated<7d");
      expect(tokensLt[1]).toEqual({ type: "OPERATOR", value: "<", position: 7 });

      const tokensGt = tokenize("number>100");
      expect(tokensGt[1]).toEqual({ type: "OPERATOR", value: ">", position: 6 });

      const tokensLte = tokenize("updated<=30d");
      expect(tokensLte[1]).toEqual({ type: "OPERATOR", value: "<=", position: 7 });

      const tokensGte = tokenize("number>=50");
      expect(tokensGte[1]).toEqual({ type: "OPERATOR", value: ">=", position: 6 });
    });

    it("tokenizes wildcard operator (~)", () => {
      const tokens = tokenize('title~"auth*"');
      expect(tokens[1]).toEqual({ type: "OPERATOR", value: "~", position: 5 });
    });
  });

  describe("boolean keywords", () => {
    it("tokenizes AND keyword", () => {
      const tokens = tokenize("status:ready AND priority:P0");
      expect(tokens[3]).toEqual({ type: "AND", value: "AND", position: 13 });
    });

    it("tokenizes OR keyword", () => {
      const tokens = tokenize("status:ready OR priority:P0");
      expect(tokens[3]).toEqual({ type: "OR", value: "OR", position: 13 });
    });

    it("tokenizes NOT keyword", () => {
      const tokens = tokenize("NOT status:done");
      expect(tokens[0]).toEqual({ type: "NOT", value: "NOT", position: 0 });
    });

    it("is case-insensitive for keywords", () => {
      const tokensLower = tokenize("status:ready and priority:P0");
      expect(tokensLower[3]).toEqual({ type: "AND", value: "AND", position: 13 });

      const tokensOr = tokenize("status:ready or priority:P0");
      expect(tokensOr[3]).toEqual({ type: "OR", value: "OR", position: 13 });

      const tokensNot = tokenize("not status:done");
      expect(tokensNot[0]).toEqual({ type: "NOT", value: "NOT", position: 0 });
    });
  });

  describe("parentheses", () => {
    it("tokenizes parentheses", () => {
      const tokens = tokenize("(status:ready)");
      expect(tokens[0]).toEqual({ type: "LPAREN", value: "(", position: 0 });
      expect(tokens[4]).toEqual({ type: "RPAREN", value: ")", position: 13 });
    });

    it("tokenizes nested parentheses", () => {
      const tokens = tokenize("((status:ready))");
      expect(tokens[0].type).toBe("LPAREN");
      expect(tokens[1].type).toBe("LPAREN");
    });
  });

  describe("quoted strings", () => {
    it("tokenizes double-quoted strings", () => {
      const tokens = tokenize('title~"hello world"');
      expect(tokens[2]).toEqual({ type: "VALUE", value: "hello world", position: 6 });
    });

    it("tokenizes single-quoted strings", () => {
      const tokens = tokenize("title~'hello world'");
      expect(tokens[2]).toEqual({ type: "VALUE", value: "hello world", position: 6 });
    });

    it("handles escaped quotes within strings", () => {
      const tokens = tokenize('title~"say \\"hello\\""');
      expect(tokens[2].value).toBe('say "hello"');
    });

    it("throws on unterminated string", () => {
      expect(() => tokenize('title~"unterminated')).toThrow(LexerError);
      expect(() => tokenize('title~"unterminated')).toThrow("Unterminated string");
    });
  });

  describe("special values", () => {
    it("tokenizes @me value", () => {
      const tokens = tokenize("assignee:@me");
      expect(tokens[2]).toEqual({ type: "VALUE", value: "@me", position: 9 });
    });

    it("tokenizes numeric values", () => {
      const tokens = tokenize("number:42");
      expect(tokens[2]).toEqual({ type: "VALUE", value: "42", position: 7 });
    });

    it("tokenizes relative date values", () => {
      const tokens = tokenize("updated<7d");
      expect(tokens[2]).toEqual({ type: "VALUE", value: "7d", position: 8 });
    });

    it("tokenizes wildcard patterns", () => {
      const tokens = tokenize('title~"auth*"');
      expect(tokens[2].value).toBe("auth*");
    });

    it("tokenizes hyphenated values as identifiers", () => {
      const tokens = tokenize("status:in-progress");
      expect(tokens[2]).toEqual({ type: "VALUE", value: "in-progress", position: 7 });
    });
  });

  describe("whitespace handling", () => {
    it("skips leading and trailing whitespace", () => {
      const tokens = tokenize("  status:ready  ");
      expect(tokens[0]).toEqual({ type: "FIELD", value: "status", position: 2 });
    });

    it("handles multiple spaces between tokens", () => {
      const tokens = tokenize("status:ready   AND   priority:P0");
      expect(tokens).toHaveLength(8); // FIELD OP VALUE AND FIELD OP VALUE EOF
    });

    it("handles tab characters", () => {
      const tokens = tokenize("status:ready\tAND\tpriority:P0");
      expect(tokens).toHaveLength(8);
    });
  });

  describe("complex queries", () => {
    it("tokenizes multi-condition query", () => {
      const tokens = tokenize("status:ready AND priority:P0 OR priority:P1");
      expect(tokens).toHaveLength(12);
      expect(tokens.map((t) => t.type)).toEqual([
        "FIELD",
        "OPERATOR",
        "VALUE",
        "AND",
        "FIELD",
        "OPERATOR",
        "VALUE",
        "OR",
        "FIELD",
        "OPERATOR",
        "VALUE",
        "EOF",
      ]);
    });

    it("tokenizes query with parentheses and NOT", () => {
      const tokens = tokenize("NOT (status:done OR status:backlog)");
      expect(tokens[0].type).toBe("NOT");
      expect(tokens[1].type).toBe("LPAREN");
    });
  });

  describe("error handling", () => {
    it("throws QueryTooLongError for oversized queries", () => {
      const longQuery = "a".repeat(2001);
      expect(() => tokenize(longQuery)).toThrow(QueryTooLongError);
    });

    it("throws InvalidCharacterError for null bytes", () => {
      expect(() => tokenize("status\x00:ready")).toThrow(InvalidCharacterError);
    });

    it("throws InvalidCharacterError for control characters", () => {
      expect(() => tokenize("status\x01:ready")).toThrow(InvalidCharacterError);
    });

    it("throws LexerError for unexpected characters", () => {
      expect(() => tokenize("status:ready $ priority:P0")).toThrow(LexerError);
    });
  });

  describe("Lexer class", () => {
    it("can be reused by creating new instances", () => {
      const lexer1 = new Lexer("status:ready");
      const tokens1 = lexer1.tokenize();
      expect(tokens1).toHaveLength(4);

      const lexer2 = new Lexer("priority:P0");
      const tokens2 = lexer2.tokenize();
      expect(tokens2).toHaveLength(4);
    });

    it("resets position on re-tokenize", () => {
      const lexer = new Lexer("status:ready");
      const tokens1 = lexer.tokenize();
      const tokens2 = lexer.tokenize();
      expect(tokens1).toEqual(tokens2);
    });
  });

  describe("empty and minimal input", () => {
    it("tokenizes empty string to just EOF", () => {
      const tokens = tokenize("");
      expect(tokens).toEqual([{ type: "EOF", value: "", position: 0 }]);
    });

    it("tokenizes whitespace-only string to just EOF", () => {
      const tokens = tokenize("   ");
      expect(tokens).toEqual([{ type: "EOF", value: "", position: 3 }]);
    });
  });
});
