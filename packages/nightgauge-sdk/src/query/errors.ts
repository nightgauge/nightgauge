/**
 * Custom error classes for the GQL query parser
 *
 * Provides structured error handling with position information
 * for helpful error messages to users.
 *
 * @see standards/security.md - Errors don't expose internals
 */

import type { QueryError } from "./types.js";

/**
 * Base class for query-related errors
 *
 * Provides position information for error highlighting in UI.
 */
export class QueryParseError extends Error {
  readonly position: number;
  readonly length: number;

  constructor(message: string, position: number = 0, length: number = 1) {
    super(message);
    this.name = "QueryParseError";
    this.position = position;
    this.length = length;

    // Maintains proper stack trace for where error was thrown
    if (Error.captureStackTrace) {
      Error.captureStackTrace(this, QueryParseError);
    }
  }

  /**
   * Convert to QueryError interface for consistent error handling
   */
  toQueryError(): QueryError {
    return {
      message: this.message,
      position: this.position,
      length: this.length,
    };
  }
}

/**
 * Error thrown when lexer encounters invalid characters or tokens
 */
export class LexerError extends QueryParseError {
  constructor(message: string, position: number = 0) {
    super(message, position, 1);
    this.name = "LexerError";
  }
}

/**
 * Error thrown when parser encounters unexpected tokens or invalid syntax
 */
export class ParserError extends QueryParseError {
  constructor(message: string, position: number = 0, length: number = 1) {
    super(message, position, length);
    this.name = "ParserError";
  }
}

/**
 * Error thrown when a field name is not recognized
 */
export class UnknownFieldError extends QueryParseError {
  readonly fieldName: string;

  constructor(fieldName: string, position: number = 0) {
    super(
      `Unknown field "${fieldName}". Valid fields: status, priority, size, component, assignee, title, number, updated, created, labels, type`,
      position,
      fieldName.length
    );
    this.name = "UnknownFieldError";
    this.fieldName = fieldName;
  }
}

/**
 * Error thrown when an operator is not valid for the field type
 */
export class InvalidOperatorError extends QueryParseError {
  readonly fieldName: string;
  readonly operator: string;
  readonly allowedOperators: string[];

  constructor(
    fieldName: string,
    operator: string,
    allowedOperators: string[],
    position: number = 0
  ) {
    super(
      `Operator "${operator}" is not valid for field "${fieldName}". Allowed: ${allowedOperators.join(", ")}`,
      position,
      operator.length
    );
    this.name = "InvalidOperatorError";
    this.fieldName = fieldName;
    this.operator = operator;
    this.allowedOperators = allowedOperators;
  }
}

/**
 * Error thrown when a value is not valid for the field
 */
export class InvalidValueError extends QueryParseError {
  readonly fieldName: string;
  readonly value: string;
  readonly allowedValues?: readonly string[];

  constructor(
    fieldName: string,
    value: string,
    allowedValues?: readonly string[],
    position: number = 0
  ) {
    const message = allowedValues
      ? `Invalid value "${value}" for field "${fieldName}". Allowed: ${allowedValues.join(", ")}`
      : `Invalid value "${value}" for field "${fieldName}"`;
    super(message, position, value.length);
    this.name = "InvalidValueError";
    this.fieldName = fieldName;
    this.value = value;
    this.allowedValues = allowedValues;
  }
}

/**
 * Error thrown when query exceeds maximum length
 */
export class QueryTooLongError extends QueryParseError {
  static readonly MAX_LENGTH = 2000;

  constructor(actualLength: number) {
    super(
      `Query exceeds maximum length of ${QueryTooLongError.MAX_LENGTH} characters (got ${actualLength})`,
      0,
      actualLength
    );
    this.name = "QueryTooLongError";
  }
}

/**
 * Error thrown when query contains invalid characters (null bytes, etc.)
 */
export class InvalidCharacterError extends LexerError {
  readonly character: string;

  constructor(character: string, position: number = 0) {
    // Don't expose actual character code in error message (security)
    super(`Invalid character in query at position ${position}`, position);
    this.name = "InvalidCharacterError";
    this.character = character;
  }
}

/**
 * Error thrown when query evaluation fails
 */
export class EvaluationError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "EvaluationError";

    if (Error.captureStackTrace) {
      Error.captureStackTrace(this, EvaluationError);
    }
  }
}
