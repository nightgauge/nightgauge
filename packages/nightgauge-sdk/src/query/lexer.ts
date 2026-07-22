/**
 * Lexer for the GQL (GitHub Query Language) parser
 *
 * Tokenizes query strings into a stream of tokens for the parser.
 * Handles:
 * - Field names (status, priority, etc.)
 * - Operators (:, =, !=, <, >, <=, >=, ~)
 * - Values (quoted strings, unquoted words)
 * - Boolean keywords (AND, OR, NOT)
 * - Parentheses for grouping
 *
 * @see docs/QUERY_LANGUAGE.md for syntax documentation
 */

import type { Token, TokenType } from "./types.js";
import { LexerError, InvalidCharacterError, QueryTooLongError } from "./errors.js";
import { MAX_QUERY_LENGTH } from "./schemas.js";

/**
 * Keywords that have special meaning
 */
const KEYWORDS: Record<string, TokenType> = {
  AND: "AND",
  and: "AND",
  OR: "OR",
  or: "OR",
  NOT: "NOT",
  not: "NOT",
};

/**
 * Check if a character is a valid identifier start (letter or underscore)
 */
function isIdentifierStart(char: string): boolean {
  return /[a-zA-Z_]/.test(char);
}

/**
 * Check if a character is a valid identifier part (letter, digit, underscore, hyphen)
 */
function isIdentifierPart(char: string): boolean {
  return /[a-zA-Z0-9_-]/.test(char);
}

/**
 * Check if a character is whitespace
 */
function isWhitespace(char: string): boolean {
  return /\s/.test(char);
}

/**
 * Check if a character is an operator start
 */
function isOperatorStart(char: string): boolean {
  // Note: ''.includes('') returns true, so we need to check for empty string
  return char !== "" && ":=!><~".includes(char);
}

/**
 * Check if a character is a control character or null byte
 */
function isInvalidCharacter(char: string): boolean {
  const code = char.charCodeAt(0);
  // Reject null bytes and control characters except whitespace
  return code === 0 || (code < 32 && !isWhitespace(char));
}

/**
 * Lexer class for tokenizing GQL queries
 *
 * @example
 * ```typescript
 * const lexer = new Lexer('status:ready AND priority:P0');
 * const tokens = lexer.tokenize();
 * // [
 * //   { type: 'FIELD', value: 'status', position: 0 },
 * //   { type: 'OPERATOR', value: ':', position: 6 },
 * //   { type: 'VALUE', value: 'ready', position: 7 },
 * //   { type: 'AND', value: 'AND', position: 13 },
 * //   { type: 'FIELD', value: 'priority', position: 17 },
 * //   { type: 'OPERATOR', value: ':', position: 25 },
 * //   { type: 'VALUE', value: 'P0', position: 26 },
 * //   { type: 'EOF', value: '', position: 28 }
 * // ]
 * ```
 */
export class Lexer {
  private readonly input: string;
  private position: number = 0;
  private tokens: Token[] = [];

  constructor(input: string) {
    // Security: Enforce max query length
    if (input.length > MAX_QUERY_LENGTH) {
      throw new QueryTooLongError(input.length);
    }
    this.input = input;
  }

  /**
   * Get current character
   */
  private current(): string {
    return this.input[this.position] ?? "";
  }

  /**
   * Peek at next character
   */
  private peek(): string {
    return this.input[this.position + 1] ?? "";
  }

  /**
   * Advance position and return current character
   */
  private advance(): string {
    const char = this.current();
    this.position++;
    return char;
  }

  /**
   * Check if at end of input
   */
  private isAtEnd(): boolean {
    return this.position >= this.input.length;
  }

  /**
   * Skip whitespace characters
   */
  private skipWhitespace(): void {
    while (!this.isAtEnd() && isWhitespace(this.current())) {
      this.position++;
    }
  }

  /**
   * Read a quoted string value
   * Handles escape sequences within quotes
   */
  private readQuotedString(): Token {
    const startPosition = this.position;
    const quote = this.advance(); // Skip opening quote
    let value = "";

    while (!this.isAtEnd() && this.current() !== quote) {
      const char = this.current();

      // Check for invalid characters
      if (isInvalidCharacter(char)) {
        throw new InvalidCharacterError(char, this.position);
      }

      // Handle escape sequences
      if (char === "\\" && this.peek() === quote) {
        this.advance(); // Skip backslash
        value += this.advance(); // Add escaped quote
      } else {
        value += this.advance();
      }
    }

    if (this.isAtEnd()) {
      throw new LexerError("Unterminated string. Expected closing quote.", startPosition);
    }

    this.advance(); // Skip closing quote

    return {
      type: "VALUE",
      value,
      position: startPosition,
    };
  }

  /**
   * Read an identifier (field name or keyword)
   * Also handles wildcard characters (*) for pattern matching
   */
  private readIdentifier(): Token {
    const startPosition = this.position;
    let value = "";

    // Read identifier characters, plus * for wildcard patterns
    while (!this.isAtEnd() && (isIdentifierPart(this.current()) || this.current() === "*")) {
      value += this.advance();
    }

    // Check if it's a keyword
    const keyword = KEYWORDS[value];
    if (keyword) {
      return {
        type: keyword,
        value: value.toUpperCase(),
        position: startPosition,
      };
    }

    // Determine if this is a field or value based on context
    // If followed by an operator, it's a field
    this.skipWhitespace();
    const isField = isOperatorStart(this.current());

    return {
      type: isField ? "FIELD" : "VALUE",
      value,
      position: startPosition,
    };
  }

  /**
   * Read an unquoted value (until whitespace or special character)
   */
  private readUnquotedValue(): Token {
    const startPosition = this.position;
    let value = "";

    // Read until we hit whitespace, operator, or parenthesis
    while (!this.isAtEnd() && !isWhitespace(this.current()) && !"()".includes(this.current())) {
      const char = this.current();

      // Check for invalid characters
      if (isInvalidCharacter(char)) {
        throw new InvalidCharacterError(char, this.position);
      }

      value += this.advance();
    }

    return {
      type: "VALUE",
      value,
      position: startPosition,
    };
  }

  /**
   * Read an operator (:, =, !=, <, >, <=, >=, ~)
   */
  private readOperator(): Token {
    const startPosition = this.position;
    let value = this.advance();

    // Check for two-character operators
    if ((value === "!" || value === "<" || value === ">") && this.current() === "=") {
      value += this.advance();
    }

    return {
      type: "OPERATOR",
      value,
      position: startPosition,
    };
  }

  /**
   * Tokenize the input string
   *
   * @returns Array of tokens
   * @throws LexerError on invalid input
   */
  tokenize(): Token[] {
    this.tokens = [];
    this.position = 0;

    while (!this.isAtEnd()) {
      this.skipWhitespace();

      if (this.isAtEnd()) {
        break;
      }

      const char = this.current();

      // Check for invalid characters
      if (isInvalidCharacter(char)) {
        throw new InvalidCharacterError(char, this.position);
      }

      // Parentheses
      if (char === "(") {
        this.tokens.push({
          type: "LPAREN",
          value: "(",
          position: this.position,
        });
        this.advance();
        continue;
      }

      if (char === ")") {
        this.tokens.push({
          type: "RPAREN",
          value: ")",
          position: this.position,
        });
        this.advance();
        continue;
      }

      // Quoted strings
      if (char === '"' || char === "'") {
        this.tokens.push(this.readQuotedString());
        continue;
      }

      // Operators
      if (isOperatorStart(char)) {
        this.tokens.push(this.readOperator());
        continue;
      }

      // Identifiers (fields/keywords) or values
      if (isIdentifierStart(char)) {
        this.tokens.push(this.readIdentifier());
        continue;
      }

      // Unquoted values (starting with special chars like @, #, or * for wildcards)
      if (char === "@" || char === "#" || char === "*" || /[0-9]/.test(char)) {
        this.tokens.push(this.readUnquotedValue());
        continue;
      }

      // Unknown character
      throw new LexerError(`Unexpected character: "${char}"`, this.position);
    }

    // Add EOF token
    this.tokens.push({
      type: "EOF",
      value: "",
      position: this.position,
    });

    return this.tokens;
  }
}

/**
 * Convenience function to tokenize a query string
 *
 * @param query - The query string to tokenize
 * @returns Array of tokens
 * @throws LexerError on invalid input
 */
export function tokenize(query: string): Token[] {
  const lexer = new Lexer(query);
  return lexer.tokenize();
}
