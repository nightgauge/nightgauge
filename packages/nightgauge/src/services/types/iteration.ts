/**
 * Iteration Types for ProjectIterationService
 *
 * Type definitions for GitHub Project iteration (sprint) management.
 *
 * @see Issue #132 - Rewrite sync-project-iteration.sh in TypeScript
 */

/**
 * Iteration target for syncIteration()
 * - '@current': Current iteration (contains today's date)
 * - '@next': First iteration starting after today
 * - 'none': Clear the iteration field
 * - string: Explicit iteration ID
 */
export type IterationTarget = "@current" | "@next" | "none" | string;

/**
 * A single iteration from GitHub Project
 */
export interface Iteration {
  /** Unique iteration ID (e.g., "abc123") */
  id: string;
  /** Human-readable iteration title (e.g., "Sprint 5") */
  title: string;
  /** Start date in ISO format (YYYY-MM-DD) */
  startDate: string;
  /** Duration in days */
  duration: number;
}

/**
 * Result of a successful iteration sync operation
 */
export interface SyncSuccess {
  success: true;
  /** Issue number that was synced */
  issue: number;
  /** Project number */
  project: number;
  /** Project item ID */
  item_id: string;
  /** Iteration details (null if cleared) */
  iteration: {
    id: string;
    title: string;
  } | null;
  /** Action performed */
  action: "assigned" | "cleared";
}

/**
 * Result when sync is skipped (graceful skip, not an error)
 */
export interface SyncSkipped {
  skipped: true;
  /** Reason for skipping */
  reason: string;
}

/**
 * Combined result type for syncIteration()
 */
export type SyncResult = SyncSuccess | SyncSkipped;

/**
 * Configuration loaded from .nightgauge/config.yaml
 */
export interface IterationConfig {
  /** Project number from project.number */
  projectNumber: number;
  /** Whether sprint feature is enabled (project.sprint.enabled) */
  sprintEnabled: boolean;
  /** Iteration field name (project.sprint.field_name, defaults to "Sprint") */
  fieldName: string;
}

/**
 * GraphQL response types for iteration queries
 */
export interface GraphQLIterationFieldConfig {
  iterations: Iteration[];
}

export interface GraphQLIterationField {
  id: string;
  configuration: GraphQLIterationFieldConfig;
}

export interface GraphQLProjectField {
  id: string;
  name: string;
  type: string;
}

export interface GraphQLProjectItem {
  id: string;
  content: {
    number: number;
    repository: {
      nameWithOwner: string;
    };
  } | null;
}

export interface GraphQLPageInfo {
  hasNextPage: boolean;
  endCursor: string | null;
}

/**
 * Type guard to check if result is a success
 */
export function isSyncSuccess(result: SyncResult): result is SyncSuccess {
  return "success" in result && result.success === true;
}

/**
 * Type guard to check if result is skipped
 */
export function isSyncSkipped(result: SyncResult): result is SyncSkipped {
  return "skipped" in result && result.skipped === true;
}
