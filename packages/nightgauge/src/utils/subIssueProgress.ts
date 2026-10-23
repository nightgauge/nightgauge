/**
 * Sub-issue progress calculation utilities for parent/epic tracking.
 *
 * This module provides pure functions to calculate completion percentages
 * based on the state of child issues in GitHub.
 */

/**
 * Represents a GitHub sub-issue with its current state.
 */
export interface SubIssue {
  /** GitHub issue number */
  number: number;
  /** Current state of the issue */
  state: "OPEN" | "CLOSED";
}

/**
 * Progress statistics for a set of sub-issues.
 */
export interface Progress {
  /** Number of open sub-issues */
  open: number;
  /** Number of closed sub-issues */
  closed: number;
  /** Total number of sub-issues */
  total: number;
}

/**
 * Calculates progress statistics from a collection of sub-issues.
 *
 * @param subIssues - Array of sub-issues to analyze
 * @returns Progress object with open, closed, and total counts
 *
 * @example
 * const progress = calculateProgress([
 *   { number: 1, state: 'CLOSED' },
 *   { number: 2, state: 'OPEN' },
 *   { number: 3, state: 'CLOSED' }
 * ]);
 * // Returns: { open: 1, closed: 2, total: 3 }
 */
export function calculateProgress(subIssues: SubIssue[]): Progress {
  const closed = subIssues.filter((issue) => issue.state === "CLOSED").length;
  const total = subIssues.length;
  const open = total - closed;

  return {
    open,
    closed,
    total,
  };
}

/**
 * Formats progress into a human-readable percentage string.
 *
 * @param progress - Progress object with completion statistics
 * @returns Formatted string like "60% (3/5)" or "0% (0/0)" for empty
 *
 * @example
 * formatProgressText({ open: 1, closed: 2, total: 3 })
 * // Returns: "67% (2/3)"
 *
 * formatProgressText({ open: 0, closed: 0, total: 0 })
 * // Returns: "0% (0/0)"
 */
export function formatProgressText(progress: Progress): string {
  if (progress.total === 0) {
    return "0% (0/0)";
  }

  const percentage = Math.round((progress.closed / progress.total) * 100);
  return `${percentage}% (${progress.closed}/${progress.total})`;
}
