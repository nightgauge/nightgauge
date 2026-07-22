/**
 * WorkItem — Universal work-item contract for SDK consumers.
 *
 * Pure type definitions (no vscode dependency). These mirror the
 * WorkItem types in the VSCode extension's services/types/WorkItemProvider.ts
 * but use a generic event type so SDK consumers don't need a vscode install.
 *
 * @see packages/nightgauge-vscode/src/services/types/WorkItemProvider.ts
 * @see Issue #2565
 */

// ---------------------------------------------------------------------------
// Scalar types — re-exported for SDK consumers
// ---------------------------------------------------------------------------

/** Issue priority from GitHub project board field */
export type Priority = "P0" | "P1" | "P2" | "P3" | null;

/** Issue size from GitHub project board field */
export type Size = "XS" | "S" | "M" | "L" | "XL" | null;

/** Sort field for work-item queries */
export type SortBy = "board" | "priority" | "number" | "size" | "dependencies" | "smart";

/** Sort direction */
export type SortDirection = "asc" | "desc";

/** An issue that blocks or is blocked by a WorkItem */
export interface BlockingIssue {
  number: number;
  title: string;
  url: string;
  state: "OPEN" | "CLOSED";
}

// ---------------------------------------------------------------------------
// WorkItem
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
 * Stable contract consumed by views, pipeline orchestration, and dependency
 * resolution. Decouples consumers from ReadyIssue internals and GitHub Projects.
 *
 * @see IWorkItemProvider — interface for fetching WorkItems
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
// IWorkItemProvider — generic event variant for SDK consumers
// ---------------------------------------------------------------------------

/**
 * Generic event subscription type compatible with vscode.Event<void>.
 * Used in IWorkItemProvider so SDK consumers don't need a vscode install.
 */
export type WorkItemEvent = (
  listener: (e: void) => unknown,
  thisArgs?: unknown,
  disposables?: Array<{ dispose(): unknown }>
) => { dispose(): unknown };

/**
 * Stable contract for work-item discovery across backends.
 *
 * SDK variant — uses WorkItemEvent instead of vscode.Event<void>.
 * Structurally compatible with the VSCode extension's IWorkItemProvider.
 *
 * @see packages/nightgauge-vscode/src/services/types/WorkItemProvider.ts
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

  /** Clear caches and fire change event for lazy re-fetch */
  invalidateAndRefresh(): void;

  /** Event fired when tree/board data changes (config, project switch) */
  onDidChangeTreeData: WorkItemEvent;

  /** Event fired when board items are updated (after fetch or refresh) */
  onItemsUpdated: WorkItemEvent;
}

// ---------------------------------------------------------------------------
// Type guards
// ---------------------------------------------------------------------------

/**
 * Returns true if the WorkItem is blocked by at least one open issue.
 */
export function isBlocked(item: WorkItem): boolean {
  return (item.blockedBy ?? []).some((b) => b.state === "OPEN");
}

/**
 * Type guard: returns true if item is an epic with sub-issues.
 */
export function isEpicItem(
  item: WorkItem
): item is WorkItem & { isEpic: true; subIssueNumbers: number[] } {
  return (
    item.isEpic === true && Array.isArray(item.subIssueNumbers) && item.subIssueNumbers.length > 0
  );
}
