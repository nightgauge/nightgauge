/**
 * Type definitions for the GQL (GitHub Query Language) parser
 *
 * Defines AST node types, operators, and field types for the query language.
 * This module contains only type definitions - no runtime code.
 *
 * @see docs/QUERY_LANGUAGE.md for user documentation
 */

/**
 * Token types produced by the lexer
 */
export type TokenType =
  | "FIELD" // Field name (status, priority, etc.)
  | "OPERATOR" // Comparison operator (:, =, !=, <, >, <=, >=, ~)
  | "VALUE" // Value (quoted or unquoted)
  | "AND" // AND keyword
  | "OR" // OR keyword
  | "NOT" // NOT keyword
  | "LPAREN" // (
  | "RPAREN" // )
  | "EOF"; // End of input

/**
 * Token produced by the lexer
 */
export interface Token {
  type: TokenType;
  value: string;
  position: number;
}

/**
 * Comparison operators supported by the query language
 */
export type ComparisonOperator =
  | ":" // Equality (field:value)
  | "=" // Alternative equality
  | "!=" // Not equal
  | ">" // Greater than
  | "<" // Less than
  | ">=" // Greater than or equal
  | "<=" // Less than or equal
  | "~"; // Wildcard/pattern match

/**
 * Boolean operators for combining conditions
 */
export type BooleanOperator = "AND" | "OR";

/**
 * Field names supported by the query language
 */
export type FieldName =
  | "status"
  | "priority"
  | "size"
  | "component"
  | "assignee"
  | "title"
  | "number"
  | "updated"
  | "created"
  | "labels"
  | "type";

/**
 * Field type categories for operator validation
 */
export type FieldType =
  | "single_select" // status, priority, size
  | "label" // component, type
  | "text" // assignee, title
  | "number" // number
  | "date" // updated, created
  | "array"; // labels

/**
 * Field metadata including type and allowed operators
 */
export interface FieldDefinition {
  name: FieldName;
  type: FieldType;
  allowedOperators: ComparisonOperator[];
  allowedValues?: readonly string[];
}

/**
 * AST node types
 */
export type ASTNodeType = "comparison" | "binary" | "unary";

/**
 * Base AST node
 */
export interface BaseASTNode {
  type: ASTNodeType;
}

/**
 * Comparison node: field operator value
 * Example: status:ready, priority!=P0, updated<7d
 */
export interface ComparisonNode extends BaseASTNode {
  type: "comparison";
  field: FieldName;
  operator: ComparisonOperator;
  value: string;
}

/**
 * Binary node: left operator right
 * Example: status:ready AND priority:high
 */
export interface BinaryNode extends BaseASTNode {
  type: "binary";
  operator: BooleanOperator;
  left: ASTNode;
  right: ASTNode;
}

/**
 * Unary node: operator operand
 * Example: NOT status:ready
 */
export interface UnaryNode extends BaseASTNode {
  type: "unary";
  operator: "NOT";
  operand: ASTNode;
}

/**
 * Union type for all AST nodes
 */
export type ASTNode = ComparisonNode | BinaryNode | UnaryNode;

/**
 * Result of parsing a query string
 */
export interface ParseResult {
  /** Parsed AST, null if parsing failed */
  ast: ASTNode | null;
  /** Parsing errors, empty if successful */
  errors: QueryError[];
}

/**
 * Query error with position information
 */
export interface QueryError {
  message: string;
  position: number;
  length: number;
}

/**
 * Issue data type for query evaluation
 * Matches ReadyIssue from ProjectBoardService
 */
export interface QueryableIssue {
  number: number;
  title: string;
  labels: string[];
  priority: "P0" | "P1" | "P2" | "P3" | null;
  size: "XS" | "S" | "M" | "L" | "XL" | null;
  url: string;
  body?: string;
  assignee?: string;
  status?: string;
  updatedAt?: string;
  createdAt?: string;
}

/**
 * Query execution result
 */
export interface QueryResult {
  /** Issues matching the query */
  items: QueryableIssue[];
  /** Total count before filtering */
  totalCount: number;
  /** Number of items matching */
  matchCount: number;
  /** Query execution time in milliseconds */
  executionTimeMs: number;
}

/**
 * Saved query definition
 */
export interface SavedQuery {
  /** Unique name for the query */
  name: string;
  /** Query expression */
  query: string;
  /** Optional description */
  description?: string;
  /** Creation timestamp */
  createdAt?: string;
  /** Last used timestamp */
  lastUsedAt?: string;
}

/**
 * Saved queries file format (.nightgauge/saved-queries.yaml)
 */
export interface SavedQueriesFile {
  /** Schema version for forward compatibility */
  version: "1.0";
  /** Array of saved queries */
  queries: SavedQuery[];
}

/**
 * Pagination options for query execution
 */
export interface QueryPaginationOptions {
  /** Starting offset (0-based index into result set) */
  offset: number;
  /** Maximum number of items to return */
  limit: number;
}

/**
 * Paginated query result — extends QueryResult with page metadata
 */
export interface PaginatedQueryResult extends QueryResult {
  /** Current page offset */
  offset: number;
  /** Items per page limit */
  limit: number;
  /** Whether more results are available beyond this page */
  hasMore: boolean;
  /** Total number of results across all pages */
  totalMatchCount: number;
}

/**
 * Query execution options
 */
export interface QueryExecutionOptions {
  /** Timeout in milliseconds (default: no timeout) */
  timeoutMs?: number;
  /** Pagination options (default: no pagination — return all results) */
  pagination?: QueryPaginationOptions;
}
