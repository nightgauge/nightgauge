/**
 * Recursive descent parser for the GQL (GitHub Query Language)
 *
 * Parses token streams into Abstract Syntax Trees (AST).
 * Implements operator precedence: NOT > AND > OR
 *
 * Grammar:
 *   query       := or_expr
 *   or_expr     := and_expr ('OR' and_expr)*
 *   and_expr    := not_expr ('AND' not_expr)*
 *   not_expr    := 'NOT'? atom
 *   atom        := '(' query ')' | comparison
 *   comparison  := field operator value
 *
 * @see docs/QUERY_LANGUAGE.md for syntax documentation
 */

import type {
  Token,
  TokenType,
  ASTNode,
  ComparisonNode,
  BinaryNode,
  UnaryNode,
  ParseResult,
  QueryError,
  ComparisonOperator,
  FieldName,
} from "./types.js";
import { tokenize } from "./lexer.js";
import { QueryParseError, ParserError, UnknownFieldError, InvalidOperatorError } from "./errors.js";
import { isValidField, isValidOperatorForField, getAllowedOperators } from "./schemas.js";

/**
 * Parser class for building AST from token stream
 *
 * @example
 * ```typescript
 * const parser = new Parser('status:ready AND priority:P0');
 * const result = parser.parse();
 *
 * if (result.ast) {
 *   console.log('Parsed successfully:', result.ast);
 * } else {
 *   console.log('Parse errors:', result.errors);
 * }
 * ```
 */
export class Parser {
  private tokens: Token[] = [];
  private position: number = 0;
  private errors: QueryError[] = [];

  constructor(private readonly input: string) {}

  /**
   * Get current token
   */
  private current(): Token {
    return this.tokens[this.position] ?? { type: "EOF", value: "", position: 0 };
  }

  /**
   * Peek at next token
   */
  private peek(): Token {
    return this.tokens[this.position + 1] ?? { type: "EOF", value: "", position: 0 };
  }

  /**
   * Check if current token matches expected type
   */
  private check(type: TokenType): boolean {
    return this.current().type === type;
  }

  /**
   * Check if current token matches any of the expected types
   */
  private checkAny(...types: TokenType[]): boolean {
    return types.includes(this.current().type);
  }

  /**
   * Advance to next token and return previous
   */
  private advance(): Token {
    const token = this.current();
    if (!this.check("EOF")) {
      this.position++;
    }
    return token;
  }

  /**
   * Consume a token of expected type or throw error
   */
  private consume(type: TokenType, message: string): Token {
    if (this.check(type)) {
      return this.advance();
    }
    throw new ParserError(message, this.current().position, this.current().value.length || 1);
  }

  /**
   * Add an error and continue parsing (error recovery)
   */
  private addError(error: QueryError): void {
    this.errors.push(error);
  }

  /**
   * Parse the input string
   *
   * @returns ParseResult with AST and any errors
   */
  parse(): ParseResult {
    try {
      // Tokenize the input
      this.tokens = tokenize(this.input);
      this.position = 0;
      this.errors = [];

      // Empty query
      if (this.check("EOF")) {
        return {
          ast: null,
          errors: [{ message: "Empty query", position: 0, length: 1 }],
        };
      }

      // Parse the expression
      const ast = this.parseOrExpr();

      // Check for trailing tokens
      if (!this.check("EOF")) {
        this.addError({
          message: `Unexpected token: "${this.current().value}"`,
          position: this.current().position,
          length: this.current().value.length || 1,
        });
      }

      return {
        ast: this.errors.length === 0 ? ast : null,
        errors: this.errors,
      };
    } catch (error) {
      if (error instanceof QueryParseError) {
        return {
          ast: null,
          errors: [error.toQueryError()],
        };
      }
      // Re-throw unexpected errors
      throw error;
    }
  }

  /**
   * Parse OR expression (lowest precedence)
   * or_expr := and_expr ('OR' and_expr)*
   */
  private parseOrExpr(): ASTNode {
    let left = this.parseAndExpr();

    while (this.check("OR")) {
      this.advance(); // consume OR
      const right = this.parseAndExpr();
      left = {
        type: "binary",
        operator: "OR",
        left,
        right,
      } as BinaryNode;
    }

    return left;
  }

  /**
   * Parse AND expression
   * and_expr := not_expr ('AND' not_expr)*
   */
  private parseAndExpr(): ASTNode {
    let left = this.parseNotExpr();

    while (this.check("AND")) {
      this.advance(); // consume AND
      const right = this.parseNotExpr();
      left = {
        type: "binary",
        operator: "AND",
        left,
        right,
      } as BinaryNode;
    }

    return left;
  }

  /**
   * Parse NOT expression (highest precedence)
   * not_expr := 'NOT'? atom
   */
  private parseNotExpr(): ASTNode {
    if (this.check("NOT")) {
      this.advance(); // consume NOT
      const operand = this.parseAtom();
      return {
        type: "unary",
        operator: "NOT",
        operand,
      } as UnaryNode;
    }

    return this.parseAtom();
  }

  /**
   * Parse atom (comparison or parenthesized expression)
   * atom := '(' query ')' | comparison
   */
  private parseAtom(): ASTNode {
    // Parenthesized expression
    if (this.check("LPAREN")) {
      this.advance(); // consume (
      const expr = this.parseOrExpr();
      this.consume("RPAREN", 'Expected closing parenthesis ")"');
      return expr;
    }

    // Comparison expression
    return this.parseComparison();
  }

  /**
   * Parse comparison expression
   * comparison := field operator value
   */
  private parseComparison(): ComparisonNode {
    // Expect field
    if (!this.check("FIELD")) {
      throw new ParserError(
        `Expected field name, got "${this.current().value || this.current().type}"`,
        this.current().position,
        this.current().value.length || 1
      );
    }

    const fieldToken = this.advance();
    const fieldName = fieldToken.value.toLowerCase();

    // Validate field name
    if (!isValidField(fieldName)) {
      throw new UnknownFieldError(fieldName, fieldToken.position);
    }

    // Expect operator
    if (!this.check("OPERATOR")) {
      throw new ParserError(
        `Expected operator after field "${fieldName}"`,
        this.current().position,
        1
      );
    }

    const operatorToken = this.advance();
    const operator = operatorToken.value as ComparisonOperator;

    // Validate operator for field type
    if (!isValidOperatorForField(fieldName as FieldName, operator)) {
      const allowed = getAllowedOperators(fieldName as FieldName);
      throw new InvalidOperatorError(fieldName, operator, allowed, operatorToken.position);
    }

    // Expect value
    if (!this.check("VALUE")) {
      throw new ParserError(
        `Expected value after operator "${operator}"`,
        this.current().position,
        1
      );
    }

    const valueToken = this.advance();

    return {
      type: "comparison",
      field: fieldName as FieldName,
      operator,
      value: valueToken.value,
    };
  }
}

/**
 * Convenience function to parse a query string
 *
 * @param query - The query string to parse
 * @returns ParseResult with AST and any errors
 *
 * @example
 * ```typescript
 * const result = parse('status:ready AND priority:P0');
 *
 * if (result.ast) {
 *   // Use the AST
 * } else {
 *   // Handle errors
 *   console.log(result.errors);
 * }
 * ```
 */
export function parse(query: string): ParseResult {
  const parser = new Parser(query);
  return parser.parse();
}

/**
 * Validate a query string without returning the AST
 *
 * @param query - The query string to validate
 * @returns Array of errors, empty if valid
 */
export function validate(query: string): QueryError[] {
  const result = parse(query);
  return result.errors;
}

/**
 * Check if a query string is valid
 *
 * @param query - The query string to check
 * @returns True if the query is valid
 */
export function isValid(query: string): boolean {
  const result = parse(query);
  return result.ast !== null && result.errors.length === 0;
}
