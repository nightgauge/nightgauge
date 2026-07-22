/**
 * Query evaluator for the GQL (GitHub Query Language)
 *
 * Evaluates AST nodes against issue data to filter results.
 * Handles all field types: single_select, label, text, number, date, array.
 *
 * @see docs/QUERY_LANGUAGE.md for supported operators and fields
 */

import type {
  ASTNode,
  ComparisonNode,
  BinaryNode,
  UnaryNode,
  QueryableIssue,
  QueryResult,
  ComparisonOperator,
  FieldName,
} from "./types.js";
import { EvaluationError } from "./errors.js";
import { SIZE_ORDER, parseDateValue, getFieldDefinition } from "./schemas.js";
import { parse } from "./parser.js";

/**
 * Match a wildcard pattern against a string
 * Supports * for any sequence of characters
 *
 * @param pattern - Pattern with optional wildcards
 * @param text - Text to match against
 * @returns True if the text matches the pattern
 */
function matchWildcard(pattern: string, text: string): boolean {
  // Escape special regex characters except *
  const escapedPattern = pattern.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*");

  const regex = new RegExp(`^${escapedPattern}$`, "i");
  return regex.test(text);
}

/**
 * Get a field value from an issue
 *
 * @param issue - The issue to get the value from
 * @param field - The field name
 * @returns The field value as a string, or null if not present
 */
function getFieldValue(issue: QueryableIssue, field: FieldName): string | string[] | null {
  switch (field) {
    case "status":
      return issue.status ?? null;
    case "priority":
      return issue.priority ?? null;
    case "size":
      return issue.size ?? null;
    case "assignee":
      return issue.assignee ?? null;
    case "title":
      return issue.title;
    case "number":
      return issue.number.toString();
    case "updated":
      return issue.updatedAt ?? null;
    case "created":
      return issue.createdAt ?? null;
    case "labels":
      return issue.labels;
    case "component":
      // Extract component: prefixed labels
      return issue.labels.filter((l) => l.startsWith("component:"));
    case "type": {
      // Extract type: prefixed labels
      const typeLabel = issue.labels.find((l) => l.startsWith("type:"));
      return typeLabel ? typeLabel.replace("type:", "") : null;
    }
    default:
      return null;
  }
}

/**
 * Compare two size values
 *
 * @returns Negative if a < b, 0 if equal, positive if a > b
 */
function compareSizes(a: string, b: string): number {
  const orderA = SIZE_ORDER[a.toUpperCase()] ?? 0;
  const orderB = SIZE_ORDER[b.toUpperCase()] ?? 0;
  return orderA - orderB;
}

/**
 * Compare two date values
 *
 * @param issueDate - Date from issue (ISO string)
 * @param queryValue - Date from query (relative or ISO)
 * @returns Negative if issue < query, 0 if equal, positive if issue > query
 */
function compareDates(issueDate: string, queryValue: string): number {
  const issueDateObj = new Date(issueDate);
  const queryDateObj = parseDateValue(queryValue);
  return issueDateObj.getTime() - queryDateObj.getTime();
}

/**
 * Evaluate a comparison operator
 */
function evaluateOperator(
  operator: ComparisonOperator,
  fieldValue: string,
  queryValue: string,
  field: FieldName
): boolean {
  // Handle special @me value for assignee
  if (queryValue === "@me") {
    // In a real implementation, this would be replaced with the current user
    // For now, we treat @me as a literal match
    // VSCode extension should substitute before evaluation
  }

  const definition = getFieldDefinition(field);

  switch (operator) {
    case ":":
    case "=":
      // Case-insensitive equality
      return fieldValue.toLowerCase() === queryValue.toLowerCase();

    case "!=":
      return fieldValue.toLowerCase() !== queryValue.toLowerCase();

    case "~":
      // Wildcard match
      return matchWildcard(queryValue, fieldValue);

    case ">":
      if (definition?.type === "date") {
        return compareDates(fieldValue, queryValue) > 0;
      }
      if (definition?.type === "number") {
        return parseInt(fieldValue, 10) > parseInt(queryValue, 10);
      }
      if (field === "size") {
        return compareSizes(fieldValue, queryValue) > 0;
      }
      return fieldValue > queryValue;

    case "<":
      if (definition?.type === "date") {
        return compareDates(fieldValue, queryValue) < 0;
      }
      if (definition?.type === "number") {
        return parseInt(fieldValue, 10) < parseInt(queryValue, 10);
      }
      if (field === "size") {
        return compareSizes(fieldValue, queryValue) < 0;
      }
      return fieldValue < queryValue;

    case ">=":
      if (definition?.type === "date") {
        return compareDates(fieldValue, queryValue) >= 0;
      }
      if (definition?.type === "number") {
        return parseInt(fieldValue, 10) >= parseInt(queryValue, 10);
      }
      if (field === "size") {
        return compareSizes(fieldValue, queryValue) >= 0;
      }
      return fieldValue >= queryValue;

    case "<=":
      if (definition?.type === "date") {
        return compareDates(fieldValue, queryValue) <= 0;
      }
      if (definition?.type === "number") {
        return parseInt(fieldValue, 10) <= parseInt(queryValue, 10);
      }
      if (field === "size") {
        return compareSizes(fieldValue, queryValue) <= 0;
      }
      return fieldValue <= queryValue;

    default:
      throw new EvaluationError(`Unknown operator: ${operator}`);
  }
}

/**
 * Evaluate a comparison node against an issue
 */
function evaluateComparison(node: ComparisonNode, issue: QueryableIssue): boolean {
  const fieldValue = getFieldValue(issue, node.field);

  // Null field value never matches (except for != operator)
  if (fieldValue === null) {
    return node.operator === "!=";
  }

  // Handle array fields (labels, component)
  if (Array.isArray(fieldValue)) {
    if (node.operator === ":" || node.operator === "=") {
      // Any label matches
      return fieldValue.some((v) => v.toLowerCase().includes(node.value.toLowerCase()));
    }
    if (node.operator === "!=") {
      // No label matches
      return !fieldValue.some((v) => v.toLowerCase().includes(node.value.toLowerCase()));
    }
    if (node.operator === "~") {
      // Any label matches wildcard
      return fieldValue.some((v) => matchWildcard(node.value, v));
    }
    // Other operators don't apply to arrays
    return false;
  }

  return evaluateOperator(node.operator, fieldValue, node.value, node.field);
}

/**
 * Evaluate a binary (AND/OR) node
 */
function evaluateBinary(node: BinaryNode, issue: QueryableIssue): boolean {
  if (node.operator === "AND") {
    return evaluateNode(node.left, issue) && evaluateNode(node.right, issue);
  } else {
    return evaluateNode(node.left, issue) || evaluateNode(node.right, issue);
  }
}

/**
 * Evaluate a unary (NOT) node
 */
function evaluateUnary(node: UnaryNode, issue: QueryableIssue): boolean {
  return !evaluateNode(node.operand, issue);
}

/**
 * Evaluate an AST node against an issue
 *
 * @param node - The AST node to evaluate
 * @param issue - The issue to evaluate against
 * @returns True if the issue matches the query
 */
export function evaluateNode(node: ASTNode, issue: QueryableIssue): boolean {
  switch (node.type) {
    case "comparison":
      return evaluateComparison(node, issue);
    case "binary":
      return evaluateBinary(node, issue);
    case "unary":
      return evaluateUnary(node, issue);
    default:
      throw new EvaluationError(`Unknown node type: ${(node as ASTNode).type}`);
  }
}

/**
 * Evaluate a query against a list of issues
 *
 * @param ast - Parsed AST from the parser
 * @param issues - Array of issues to filter
 * @param timeoutMs - Optional timeout in milliseconds; throws if exceeded
 * @returns QueryResult with matching items
 */
export function evaluate(ast: ASTNode, issues: QueryableIssue[], timeoutMs?: number): QueryResult {
  const startTime = performance.now();
  const deadline = timeoutMs !== undefined ? startTime + timeoutMs : undefined;

  const items = issues.filter((issue) => {
    // Check timeout on each issue to avoid runaway evaluation
    if (deadline !== undefined && performance.now() > deadline) {
      throw new EvaluationError(
        `Query execution timed out after ${timeoutMs}ms. ` +
          `Processed ${issues.indexOf(issue)} of ${issues.length} issues. ` +
          `Try narrowing your query.`
      );
    }

    try {
      return evaluateNode(ast, issue);
    } catch (error) {
      if (error instanceof EvaluationError && error.message.includes("timed out")) {
        throw error;
      }
      // Log error but don't fail entire query for individual issue errors
      console.warn(
        `Error evaluating issue #${issue.number}:`,
        error instanceof Error ? error.message : error
      );
      return false;
    }
  });

  const endTime = performance.now();

  return {
    items,
    totalCount: issues.length,
    matchCount: items.length,
    executionTimeMs: Math.round(endTime - startTime),
  };
}

/**
 * Execute a query string against a list of issues
 *
 * Convenience function that parses and evaluates in one call.
 *
 * @param query - Query string to execute
 * @param issues - Array of issues to filter
 * @param timeoutMs - Optional timeout in milliseconds (default: no timeout)
 * @returns QueryResult with matching items
 * @throws If the query is invalid or execution times out
 *
 * @example
 * ```typescript
 * const result = executeQuery('status:ready AND priority:P0', issues);
 * console.log(`Found ${result.matchCount} matching issues`);
 *
 * // With timeout
 * const result = executeQuery('status:ready', issues, 30000);
 * ```
 */
export function executeQuery(
  query: string,
  issues: QueryableIssue[],
  timeoutMs?: number
): QueryResult {
  const parseResult = parse(query);

  if (!parseResult.ast) {
    const errorMessages = parseResult.errors.map((e) => e.message).join("; ");
    throw new EvaluationError(`Invalid query: ${errorMessages}`);
  }

  return evaluate(parseResult.ast, issues, timeoutMs);
}

/**
 * Evaluate a query against a list of issues with an optional timeout
 *
 * When timeoutMs is provided, evaluation will abort and throw if it exceeds
 * the limit. This protects against runaway queries on very large datasets.
 *
 * @param ast - Parsed AST from the parser
 * @param issues - Array of issues to filter
 * @param timeoutMs - Optional timeout in milliseconds
 * @returns QueryResult with matching items
 * @throws EvaluationError if execution time exceeds timeoutMs
 */
export function evaluateWithTimeout(
  ast: ASTNode,
  issues: QueryableIssue[],
  timeoutMs: number
): QueryResult {
  return evaluate(ast, issues, timeoutMs);
}
