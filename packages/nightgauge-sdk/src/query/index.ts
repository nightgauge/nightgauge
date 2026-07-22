/**
 * GQL (GitHub Query Language) Module
 *
 * Provides a JQL-style query language for filtering GitHub Project items.
 * Supports boolean logic (AND, OR, NOT), multiple field types, and
 * various comparison operators.
 *
 * @example
 * ```typescript
 * import { executeQuery, parse, validate } from '@nightgauge/sdk/query';
 *
 * // Execute a query against issues
 * const result = executeQuery('status:ready AND priority:P0', issues);
 * console.log(`Found ${result.matchCount} matching issues`);
 *
 * // Validate a query without executing
 * const errors = validate('invalid query');
 * if (errors.length > 0) {
 *   console.log('Query errors:', errors);
 * }
 *
 * // Parse to AST for advanced use cases
 * const { ast, errors } = parse('status:ready AND priority:P0');
 * ```
 *
 * @see docs/QUERY_LANGUAGE.md for user documentation
 *
 * @packageDocumentation
 */

// Types
export type {
  // Token types
  TokenType,
  Token,
  // AST types
  ASTNode,
  ASTNodeType,
  BaseASTNode,
  ComparisonNode,
  BinaryNode,
  UnaryNode,
  // Operator types
  ComparisonOperator,
  BooleanOperator,
  // Field types
  FieldName,
  FieldType,
  FieldDefinition,
  // Result types
  ParseResult,
  QueryError,
  QueryableIssue,
  QueryResult,
  // Saved queries
  SavedQuery,
  SavedQueriesFile,
  // Pagination and execution options
  QueryPaginationOptions,
  PaginatedQueryResult,
  QueryExecutionOptions,
} from "./types.js";

// Errors
export {
  QueryParseError,
  LexerError,
  ParserError,
  UnknownFieldError,
  InvalidOperatorError,
  InvalidValueError,
  QueryTooLongError,
  InvalidCharacterError,
  EvaluationError,
} from "./errors.js";

// Schemas and validation
export {
  // Constants
  MAX_QUERY_LENGTH,
  ALLOWED_FIELDS,
  FIELD_DEFINITIONS,
  SIZE_ORDER,
  // Zod schemas
  FieldNameSchema,
  ComparisonOperatorSchema,
  BooleanOperatorSchema,
  RelativeDateSchema,
  ISODateSchema,
  DateValueSchema,
  IssueNumberSchema,
  ComparisonNodeSchema,
  ASTNodeSchema,
  SavedQuerySchema,
  SavedQueriesFileSchema,
  // Validation functions
  isValidField,
  getFieldDefinition,
  isValidOperatorForField,
  getAllowedOperators,
  getAllowedValues,
  isValidValueForField,
  parseRelativeDate,
  parseDateValue,
} from "./schemas.js";

// Lexer
export { Lexer, tokenize } from "./lexer.js";

// Parser
export { Parser, parse, validate, isValid } from "./parser.js";

// Evaluator
export { evaluateNode, evaluate, executeQuery, evaluateWithTimeout } from "./evaluator.js";
