/**
 * Dependency Utilities - Shared functions for issue dependency handling
 *
 * Provides helper functions for checking if issues are blocked, counting blockers,
 * and extracting blocker information. Used by ProjectBoardService and ReadyIssueTreeItem.
 *
 * @see Issue #443 - Auto-selection and Ready View Should Skip Blocked Issues
 */

import type { ReadyIssue, BlockingIssue } from "../services/ProjectBoardService";

/**
 * Check if an issue is blocked by any open dependencies
 *
 * An issue is considered blocked if it has at least one open blocker
 * in its blockedBy array.
 *
 * @param issue - The issue to check
 * @returns True if the issue has at least one open blocker
 */
export function isBlocked(issue: ReadyIssue): boolean {
  if (!issue.blockedBy || issue.blockedBy.length === 0) {
    return false;
  }

  return issue.blockedBy.some((blocker) => blocker.state === "OPEN");
}

/**
 * Get the count of open blockers for an issue
 *
 * Only counts blockers with state === 'OPEN'.
 *
 * @param issue - The issue to check
 * @returns Number of open blockers (0 if none)
 */
export function getBlockerCount(issue: ReadyIssue): number {
  if (!issue.blockedBy || issue.blockedBy.length === 0) {
    return 0;
  }

  return issue.blockedBy.filter((blocker) => blocker.state === "OPEN").length;
}

/**
 * Get the titles of all open blockers for an issue
 *
 * Useful for displaying blocker information in tooltips.
 *
 * @param issue - The issue to check
 * @returns Array of blocker titles (empty array if none)
 */
export function getBlockerTitles(issue: ReadyIssue): string[] {
  if (!issue.blockedBy || issue.blockedBy.length === 0) {
    return [];
  }

  return issue.blockedBy
    .filter((blocker) => blocker.state === "OPEN")
    .map((blocker) => `#${blocker.number}: ${blocker.title}`);
}

/**
 * Get all open blockers for an issue
 *
 * @param issue - The issue to check
 * @returns Array of open BlockingIssue objects (empty array if none)
 */
export function getOpenBlockers(issue: ReadyIssue): BlockingIssue[] {
  if (!issue.blockedBy || issue.blockedBy.length === 0) {
    return [];
  }

  return issue.blockedBy.filter((blocker) => blocker.state === "OPEN");
}

/**
 * Filter an array of issues to only unblocked issues
 *
 * Returns issues that have no open blockers.
 *
 * @param issues - Array of issues to filter
 * @returns Array of unblocked issues
 */
export function filterUnblockedIssues(issues: ReadyIssue[]): ReadyIssue[] {
  return issues.filter((issue) => !isBlocked(issue));
}

/**
 * Filter an array of issues to only blocked issues
 *
 * Returns issues that have at least one open blocker.
 *
 * @param issues - Array of issues to filter
 * @returns Array of blocked issues
 */
export function filterBlockedIssues(issues: ReadyIssue[]): ReadyIssue[] {
  return issues.filter((issue) => isBlocked(issue));
}

/**
 * Find the "least-blocked" issue from an array
 *
 * Returns the issue with the fewest open blockers. If multiple issues
 * have the same number of blockers, returns the first one (preserving
 * priority order from the input).
 *
 * @param issues - Array of blocked issues
 * @returns The least-blocked issue, or null if array is empty
 */
export function findLeastBlockedIssue(issues: ReadyIssue[]): ReadyIssue | null {
  if (issues.length === 0) {
    return null;
  }

  return issues.reduce((leastBlocked, current) => {
    const leastBlockedCount = getBlockerCount(leastBlocked);
    const currentCount = getBlockerCount(current);
    return currentCount < leastBlockedCount ? current : leastBlocked;
  });
}

/**
 * Partition issues into blocked and unblocked groups
 *
 * Useful for separating issues for display (unblocked first).
 *
 * @param issues - Array of issues to partition
 * @returns Object with unblocked and blocked arrays
 */
export function partitionByBlockedStatus(issues: ReadyIssue[]): {
  unblocked: ReadyIssue[];
  blocked: ReadyIssue[];
} {
  const unblocked: ReadyIssue[] = [];
  const blocked: ReadyIssue[] = [];

  for (const issue of issues) {
    if (isBlocked(issue)) {
      blocked.push(issue);
    } else {
      unblocked.push(issue);
    }
  }

  return { unblocked, blocked };
}
