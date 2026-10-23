/**
 * Query-related types for the VSCode extension
 *
 * Extends SDK query types with UI-specific properties.
 */

import type { QueryableIssue, QueryResult, SavedQuery } from "@nightgauge/sdk";
import type { ReadyIssue } from "../services/ProjectBoardService";

/**
 * Extended issue type for query results with UI metadata
 */
export interface QueryResultIssue extends QueryableIssue {
  /** Original ReadyIssue if available */
  originalIssue?: ReadyIssue;
  /** Whether this issue is currently selected in the tree view */
  isSelected?: boolean;
}

/**
 * Query execution state
 */
export type QueryState =
  | "idle" // No query active
  | "parsing" // Parsing query
  | "executing" // Running query
  | "complete" // Query finished
  | "error"; // Query failed

/**
 * Query execution context with UI state
 */
export interface QueryContext {
  /** Current query string */
  query: string;
  /** Execution state */
  state: QueryState;
  /** Query result (if complete) */
  result?: QueryResult;
  /** Error message (if error state) */
  error?: string;
  /** Timestamp when query was executed */
  executedAt?: Date;
}

/**
 * Saved query with usage metadata
 */
export interface SavedQueryWithMeta extends SavedQuery {
  /** Number of times this query has been run */
  runCount?: number;
  /** Whether this is a built-in/default query */
  isBuiltIn?: boolean;
}

/**
 * Built-in queries provided by the extension
 */
export const BUILTIN_QUERIES: SavedQueryWithMeta[] = [
  {
    name: "High Priority Ready",
    query: "status:ready AND (priority:P0 OR priority:P1)",
    description: "Ready issues with P0 or P1 priority",
    isBuiltIn: true,
  },
  {
    name: "Small Tasks",
    query: "status:ready AND (size:XS OR size:S)",
    description: "Ready issues with XS or S size",
    isBuiltIn: true,
  },
  {
    name: "Recently Updated",
    query: "updated<7d",
    description: "Issues updated in the last 7 days",
    isBuiltIn: true,
  },
  {
    name: "My Issues",
    query: "assignee:@me",
    description: "Issues assigned to current user",
    isBuiltIn: true,
  },
  {
    name: "Bugs",
    query: "type:bug",
    description: "All bug-type issues",
    isBuiltIn: true,
  },
];

/**
 * Query history entry
 */
export interface QueryHistoryEntry {
  /** Query string */
  query: string;
  /** Timestamp when query was executed */
  executedAt: Date;
  /** Number of results returned */
  resultCount: number;
}

/**
 * Query service configuration
 */
export interface QueryServiceConfig {
  /** Maximum number of history entries to keep */
  maxHistoryEntries: number;
  /** Whether to show built-in queries */
  showBuiltInQueries: boolean;
  /** Default output format */
  defaultFormat: "tree" | "table";
}

/**
 * Default query service configuration
 */
export const DEFAULT_QUERY_CONFIG: QueryServiceConfig = {
  maxHistoryEntries: 20,
  showBuiltInQueries: true,
  defaultFormat: "tree",
};

/**
 * Convert ReadyIssue to QueryableIssue
 */
export function toQueryableIssue(issue: ReadyIssue): QueryableIssue {
  return {
    number: issue.number,
    title: issue.title,
    labels: issue.labels,
    priority: issue.priority,
    size: issue.size,
    url: issue.url,
    status: issue.status,
    assignee: undefined,
    updatedAt: undefined,
    createdAt: undefined,
  };
}

/**
 * Convert array of ReadyIssues to QueryableIssues
 */
export function toQueryableIssues(issues: ReadyIssue[]): QueryableIssue[] {
  return issues.map(toQueryableIssue);
}
