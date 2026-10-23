/**
 * ProjectBoardTypes - Type definitions for Project Board Dashboard Widget
 *
 * Defines interfaces for the project board summary widget in the dashboard.
 * Includes status counts, ready issues, and sprint information.
 *
 * Multi-Project Support (Issue #135):
 * - Added ProjectConfig re-export for use in dashboard
 * - Added selectedProject field to ProjectBoardData
 * - Added multiProjectMode flag for UI decisions
 *
 * @see Issue #134 - Project Board Dashboard Widget
 * @see Issue #135 - Multi-Project Support
 * @see docs/ARCHITECTURE.md for WebView patterns
 */

import type { ReadyIssue, Priority, ProjectConfig } from "../../services/ProjectBoardService";
import type { Iteration } from "../../services/types/iteration";

// Re-export ProjectConfig for dashboard use
export type { ProjectConfig };

/**
 * Status values for project board issues
 */
export type ProjectBoardStatus = "Ready" | "In progress" | "In review" | "Done" | "Backlog";

/**
 * Count of issues for each status
 */
export interface StatusCounts {
  ready: number;
  inProgress: number;
  inReview: number;
  done: number;
  backlog: number;
}

/**
 * Ready issue for display in the widget (subset of ReadyIssue)
 */
export interface ReadyIssueDisplay {
  number: number;
  title: string;
  priority: Priority;
  url: string;
}

/**
 * Lifecycle state for the project board fetch.
 *
 * Disambiguates the three visually-identical zero states the previous render
 * code conflated:
 *  - "loading":  initial open, fetch in flight
 *  - "error":    fetch threw (auth, network, GraphQL error)
 *  - "loaded":   fetch returned; counts are authoritative even if all zero
 *
 * "idle" is the pre-open state. The widget renders nothing for "idle" so a
 * fresh window doesn't briefly flash an empty board.
 */
export type ProjectBoardLoadingState = "idle" | "loading" | "loaded" | "error";

/**
 * Per-fetch diagnostics so the widget can distinguish:
 *  - Board is genuinely empty (rawItemCount === 0)
 *  - Board has items, but none belong to the workspace's repo
 *    (rawItemCount > 0 && filteredItemCount === 0) — usually a misconfigured
 *    `project.repo`, not a real "nothing to do" state.
 */
export interface ProjectBoardDiagnostics {
  /** Items returned by `boardList()` before any client-side filtering. */
  rawItemCount: number;
  /** Items remaining after the `owner/repo` filter. */
  filteredItemCount: number;
  /** The `owner/repo` string used as the filter (null if not configured). */
  expectedRepo: string | null;
}

/**
 * Project board data for the dashboard widget
 */
export interface ProjectBoardData {
  /** Count of issues by status */
  statusCounts: StatusCounts;
  /** Top N ready issues ordered by priority */
  topReadyIssues: ReadyIssueDisplay[];
  /** Current sprint/iteration (null if not configured) */
  currentSprint: Iteration | null;
  /** Timestamp of last data refresh */
  lastRefreshed: Date;
  /** URL to open the project board in browser */
  projectUrl: string | null;
  /** Whether the project board is configured */
  isConfigured: boolean;
  /** Error message if data fetch failed */
  error?: string;
  /** Multi-project mode: list of all configured projects */
  projects?: ProjectConfig[];
  /** Multi-project mode: currently selected project (null = aggregate) */
  selectedProject?: string | null;
  /** Whether multi-project mode is active (more than 1 project configured) */
  multiProjectMode?: boolean;
  /** Lifecycle of the most recent fetch — drives loading / empty / error UI. */
  loadingState?: ProjectBoardLoadingState;
  /**
   * Optional diagnostics from the last successful prefetch. Present when the
   * fetch completed (success path); used to distinguish "truly empty board"
   * from "items returned but filtered out by repo match".
   */
  diagnostics?: ProjectBoardDiagnostics;
}

/**
 * Configuration for the project board widget
 */
export interface ProjectBoardWidgetConfig {
  /** Whether the widget is enabled */
  enabled: boolean;
  /** Cache TTL in minutes */
  cacheTtlMinutes: number;
  /** Maximum number of ready issues to display */
  maxReadyIssues: number;
}

/**
 * Default configuration for the project board widget
 */
export const DEFAULT_PROJECT_BOARD_CONFIG: ProjectBoardWidgetConfig = {
  enabled: true,
  cacheTtlMinutes: 5,
  maxReadyIssues: 5,
};
