/**
 * WorkItemProvider — Universal work-item contract for Nightgauge.
 *
 * This module defines the stable interface that decouples issue discovery
 * from GitHub Projects, enabling future non-GitHub adapters and simplifying
 * test mocking.
 *
 * WorkItem is the source of truth for work-item data consumed by views,
 * pipeline orchestration, and dependency resolution.
 *
 * @see Issue #2565
 * @see ProjectBoardService — primary IWorkItemProvider implementation
 */

import type { Event } from "vscode";
import type {
  Priority,
  Size,
  SortBy,
  SortDirection,
  BlockingIssue,
  ReadyIssue,
  RateLimitState,
} from "../ProjectBoardService";

// ---------------------------------------------------------------------------
// WorkItem — universal work-item type
// ---------------------------------------------------------------------------

/**
 * Identifies which backend provider supplied this work item.
 * Supports future non-GitHub adapters (Jira, Linear, Azure DevOps).
 */
export interface WorkItemSource {
  /** Provider identifier (e.g. "github", "jira", "linear") */
  provider: string;
  /** Repository in "owner/repo" format (GitHub) or workspace identifier */
  repository?: string;
  /** Project board identifier */
  projectId?: string | number;
}

/**
 * Universal work-item type.
 *
 * Decouples views and pipeline orchestration from ReadyIssue internals.
 * Must remain structurally compatible with ReadyIssue (all ReadyIssue
 * fields are present here so normalizeToWorkItem() is lossless).
 *
 * @see ReadyIssue — concrete type still used by ProjectBoardService internals
 * @see normalizeToWorkItem — converts ReadyIssue to WorkItem
 */
export interface WorkItem {
  /** GitHub issue number */
  number: number;
  /** Issue title */
  title: string;
  /** GitHub labels (string slugs) */
  labels: string[];
  /** Issue priority from project board field */
  priority: Priority;
  /** Issue size from project board field */
  size: Size;
  /** GitHub issue URL */
  url: string;
  /** Project board status (e.g. "Ready", "In progress", "Backlog") */
  status?: string;
  /** Parent epic issue number (when this issue is a sub-issue) */
  epicRef?: number;
  /** Parent epic title (for display when epic is in a different board status tab) */
  epicTitle?: string;
  /** Issues that must be resolved before this one can proceed */
  blockedBy?: BlockingIssue[];
  /** Issues blocked by this one */
  blocks?: BlockingIssue[];
  /** True when this item is an epic (type:epic label + has sub-issues) */
  isEpic?: boolean;
  /** Issue numbers of native GitHub sub-issues (epics only) */
  subIssueNumbers?: number[];
  /** Source metadata — identifies the backend provider */
  source?: WorkItemSource;
}

// ---------------------------------------------------------------------------
// IWorkItemProvider — stable interface for issue discovery
// ---------------------------------------------------------------------------

/**
 * Stable contract for work-item discovery across backends.
 *
 * ProjectBoardService is the primary implementation. Alternative backends
 * (Jira, Linear, etc.) will implement this interface to integrate with views
 * and pipeline commands without requiring changes to consumers.
 *
 * All methods mirror ProjectBoardService's existing public API so that
 * `implements IWorkItemProvider` can be added to ProjectBoardService without
 * any breaking changes (tracked in parent epic #2428).
 */
export interface IWorkItemProvider {
  /** Fetch work items for a given board status */
  getIssuesByStatus(
    status: string,
    sortBy?: SortBy,
    sortDirection?: SortDirection
  ): Promise<WorkItem[]>;

  /** Convenience: fetch items with status "Ready" */
  getReadyIssues(sortBy?: SortBy): Promise<WorkItem[]>;

  /** Fetch ALL items across all statuses (for epic grouping and search) */
  getAllItems(): Promise<WorkItem[]>;

  /** Read-only cache access for a status (no network call) */
  getItemsByStatusFromCache(
    status: string,
    sortBy?: SortBy,
    sortDirection?: SortDirection
  ): WorkItem[];

  /** Build epic metadata lookup from cache */
  getEpicMetadataFromCache(
    extraIssues?: WorkItem[]
  ): Map<number, { number: number; title: string; url: string }>;

  /** Fetch issue counts per board status */
  getAggregatedStatusCounts(): Promise<Record<string, number>>;

  /** Eagerly prefetch and cache all items */
  prefetchAllItems(options?: { force?: boolean }): Promise<void>;

  /** Clear all caches */
  clearCache(): void;

  /**
   * Invalidate cache timestamps without discarding stale data.
   * Stale issue lists remain as fallback when the GitHub API is rate-limited.
   * Use for user-triggered refreshes; clearCache() is for workspace/adapter switches.
   */
  softInvalidate(): void;

  /** Clear caches and fire change event for lazy re-fetch */
  invalidateAndRefresh(): void;

  /** Event fired when tree/board data changes (config, project switch) */
  onDidChangeTreeData: Event<void>;

  /** Event fired when board items are updated (after fetch or refresh) */
  onItemsUpdated: Event<void>;

  /**
   * Optional event fired when GitHub rate-limit state changes. Tree providers
   * subscribe to pause / resume their auto-refresh timers so multiple VSCode
   * windows don't race to exhaust the shared per-user quota. Adapters that do
   * not consume GitHub API quota (e.g., Jira) may omit this.
   */
  onRateLimitState?: Event<RateLimitState>;

  /** Returns the most recent rate-limit snapshot, or null if unavailable. */
  getRateLimitState?(): RateLimitState | null;
}

// ---------------------------------------------------------------------------
// Normalization helper
// ---------------------------------------------------------------------------

/**
 * Normalize a ReadyIssue to a WorkItem.
 *
 * This is a lossless mapping — all ReadyIssue fields are preserved.
 * Use this when an adapter receives a ReadyIssue and needs to return
 * a stable WorkItem contract.
 *
 * @param issue - The ReadyIssue to convert
 * @param source - Optional backend source metadata
 * @returns WorkItem with all fields from the input issue
 */
export function normalizeToWorkItem(issue: ReadyIssue, source?: WorkItemSource): WorkItem {
  return {
    number: issue.number,
    title: issue.title,
    labels: issue.labels,
    priority: issue.priority,
    size: issue.size,
    url: issue.url,
    status: issue.status,
    epicRef: issue.epicRef,
    epicTitle: issue.epicTitle,
    blockedBy: issue.blockedBy,
    blocks: issue.blocks,
    isEpic: issue.isEpic,
    subIssueNumbers: issue.subIssueNumbers,
    source,
  };
}

// ---------------------------------------------------------------------------
// Type guards
// ---------------------------------------------------------------------------

/**
 * Returns true if the WorkItem is blocked by at least one open issue.
 *
 * @see isBlocked in utils/dependencyUtils.ts — equivalent for ReadyIssue
 */
export function isBlocked(item: WorkItem): boolean {
  return (item.blockedBy ?? []).some((b) => b.state === "OPEN");
}

/**
 * Type guard: returns true if item is an epic with sub-issues.
 *
 * Narrows the type to `WorkItem & { isEpic: true; subIssueNumbers: number[] }`
 * so callers can safely access subIssueNumbers without null checks.
 */
export function isEpicItem(
  item: WorkItem
): item is WorkItem & { isEpic: true; subIssueNumbers: number[] } {
  return (
    item.isEpic === true && Array.isArray(item.subIssueNumbers) && item.subIssueNumbers.length > 0
  );
}
