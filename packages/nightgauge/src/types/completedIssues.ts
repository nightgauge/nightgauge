/**
 * Type definitions for completed and failed issue tracking
 *
 * Completed/failed issues are persisted in workspace storage (survives VS Code restarts)
 * and displayed in the pipeline tree view with retry/clear functionality.
 *
 * @see Issue #301 - Handle completed and failed issue states in pipeline
 */

/**
 * Maximum number of completed/failed issues to keep in history
 * Follows DashboardState pattern (limit: 50)
 */
export const MAX_COMPLETED_ISSUES = 50;

/**
 * Reference to an issue with minimal metadata
 */
export interface IssueReference {
  /** Issue number */
  issue_number: number;
  /** Issue title */
  title: string;
  /** Branch name */
  branch: string;
  /** Timestamp when action occurred (ISO 8601) */
  timestamp: string;
  /** GitHub labels (e.g. ["size:M", "priority:high"]) — optional for backward compat */
  labels?: string[];
  /** True when this run's cost exceeded the anomaly threshold (Issue #1335) */
  cost_anomaly_exceeded?: boolean;
}

/**
 * Reference to a failed issue with error context
 */
export interface FailedIssueReference extends IssueReference {
  /** Stage where failure occurred */
  failed_stage: string;
  /** Error message (sanitized for display) */
  error: string;
  /** Number of retry attempts (circuit breaker) */
  retry_count: number;
}

/**
 * Persisted state for completed/failed issues
 * Stored in workspace storage, survives VS Code restarts
 */
export interface CompletedIssuesState {
  /** Schema version for future migrations */
  schema_version: "1.0";
  /** Completed issues (most recent first, limit: 50) */
  completed: IssueReference[];
  /** Failed issues (most recent first, limit: 50) */
  failed: FailedIssueReference[];
  /** Last updated timestamp */
  updated_at: string;
}

/**
 * Create initial state
 */
export function createInitialState(): CompletedIssuesState {
  return {
    schema_version: "1.0",
    completed: [],
    failed: [],
    updated_at: new Date().toISOString(),
  };
}

/**
 * Create issue reference from pipeline state
 */
export function createIssueReference(
  issueNumber: number,
  title: string,
  branch: string,
  labels?: string[],
  costAnomalyExceeded?: boolean
): IssueReference {
  return {
    issue_number: issueNumber,
    title,
    branch,
    timestamp: new Date().toISOString(),
    ...(labels && labels.length > 0 ? { labels } : {}),
    ...(costAnomalyExceeded ? { cost_anomaly_exceeded: true } : {}),
  };
}

/**
 * Create failed issue reference with error context
 */
export function createFailedIssueReference(
  issueNumber: number,
  title: string,
  branch: string,
  failedStage: string,
  error: string,
  retryCount: number = 0,
  labels?: string[]
): FailedIssueReference {
  return {
    issue_number: issueNumber,
    title,
    branch,
    failed_stage: failedStage,
    error: sanitizeError(error),
    retry_count: retryCount,
    timestamp: new Date().toISOString(),
    ...(labels && labels.length > 0 ? { labels } : {}),
  };
}

/**
 * Sanitize error message for display (remove sensitive details)
 */
function sanitizeError(error: string): string {
  // Remove file paths with user home directories
  let sanitized = error.replace(/\/Users\/[^/]+/g, "~");
  sanitized = sanitized.replace(/\/home\/[^/]+/g, "~");

  // Truncate very long errors
  if (sanitized.length > 500) {
    sanitized = sanitized.substring(0, 497) + "...";
  }

  return sanitized;
}
