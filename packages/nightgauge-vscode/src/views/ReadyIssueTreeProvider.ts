/**
 * ReadyIssueTreeProvider — stable delegation layer for work-item discovery.
 *
 * Provides a named architectural boundary between the Ready view consumers
 * (ProjectBoardTreeProvider, RepositoriesTreeProvider) and the underlying
 * work-item source (ProjectBoardService, CompositeAdapter, or any
 * IWorkItemProvider implementation).
 *
 * Design:
 *   - Thin delegation wrapper: all methods delegate to the injected provider.
 *   - No in-memory caching: relies entirely on the underlying provider's cache.
 *   - Events are forwarded from the provider, not re-emitted.
 *   - Stable interface enables independent unit testing without IPC dependencies.
 *
 * @see Issue #2568 — migrate Ready view to IWorkItemProvider
 * @see IWorkItemProvider — interface this class implements
 * @see CompositeAdapter — preferred provider for repo-only issue support
 * @see ProjectBoardService — board-only provider (existing behavior)
 */

import type { Event } from "vscode";
import type { SortBy, SortDirection } from "../services/ProjectBoardService";
import type { WorkItem, IWorkItemProvider } from "../services/types/WorkItemProvider";

// ---------------------------------------------------------------------------
// ReadyIssueTreeProvider
// ---------------------------------------------------------------------------

/**
 * Delegation wrapper that decouples the Ready view from a specific work-item
 * backend.
 *
 * Consumers (e.g. ProjectBoardTreeProvider for the "ready" tab) inject this
 * provider instead of calling ProjectBoardService directly. The underlying
 * source can be switched — from board-only to composite board+repo — without
 * changing any consumer code.
 *
 * @example
 * ```typescript
 * // Board-only (existing behavior)
 * const provider = new ReadyIssueTreeProvider(new ProjectBoardService(workspaceRoot));
 *
 * // Composite board + repo (enables repo-only issue discovery)
 * const provider = new ReadyIssueTreeProvider(
 *   new CompositeAdapter(workspaceRoot, new ProjectBoardService(workspaceRoot))
 * );
 *
 * // In tests — inject any mock that implements IWorkItemProvider
 * const provider = new ReadyIssueTreeProvider(mockWorkItemProvider);
 * ```
 */
export class ReadyIssueTreeProvider implements IWorkItemProvider {
  private readonly delegate: IWorkItemProvider;

  /**
   * @param workItemProvider - The underlying work-item source to delegate to.
   *   Can be any IWorkItemProvider implementation.
   */
  constructor(workItemProvider: IWorkItemProvider) {
    this.delegate = workItemProvider;
  }

  // -------------------------------------------------------------------------
  // Events — forwarded directly from the underlying provider
  // -------------------------------------------------------------------------

  /** Forwarded from the underlying provider — fired on config/project change */
  get onDidChangeTreeData(): Event<void> {
    return this.delegate.onDidChangeTreeData;
  }

  /** Forwarded from the underlying provider — fired after each fetch/refresh */
  get onItemsUpdated(): Event<void> {
    return this.delegate.onItemsUpdated;
  }

  // -------------------------------------------------------------------------
  // IWorkItemProvider — data fetching
  // -------------------------------------------------------------------------

  /**
   * Fetch work items for the requested board status.
   * Delegates directly to the underlying provider.
   */
  getIssuesByStatus(
    status: string,
    sortBy?: SortBy,
    sortDirection?: SortDirection
  ): Promise<WorkItem[]> {
    return this.delegate.getIssuesByStatus(status, sortBy, sortDirection);
  }

  /**
   * Convenience: fetch items with status "Ready".
   * Delegates to the underlying provider's getReadyIssues().
   */
  getReadyIssues(sortBy?: SortBy): Promise<WorkItem[]> {
    return this.delegate.getReadyIssues(sortBy);
  }

  /**
   * Fetch ALL items across all statuses.
   * Used for epic metadata resolution and search.
   */
  getAllItems(): Promise<WorkItem[]> {
    return this.delegate.getAllItems();
  }

  // -------------------------------------------------------------------------
  // IWorkItemProvider — cache access
  // -------------------------------------------------------------------------

  /**
   * Read-only cache access for a status (no network call).
   */
  getItemsByStatusFromCache(
    status: string,
    sortBy?: SortBy,
    sortDirection?: SortDirection
  ): WorkItem[] {
    return this.delegate.getItemsByStatusFromCache(status, sortBy, sortDirection);
  }

  /**
   * Build epic metadata lookup from cache.
   * Used by epic grouping logic in tree providers.
   */
  getEpicMetadataFromCache(
    extraIssues?: WorkItem[]
  ): Map<number, { number: number; title: string; url: string }> {
    return this.delegate.getEpicMetadataFromCache(extraIssues);
  }

  /**
   * Return issue counts keyed by board status.
   */
  getAggregatedStatusCounts(): Promise<Record<string, number>> {
    return this.delegate.getAggregatedStatusCounts();
  }

  // -------------------------------------------------------------------------
  // IWorkItemProvider — prefetch and cache management
  // -------------------------------------------------------------------------

  /**
   * Eagerly prefetch and cache all items.
   */
  prefetchAllItems(options?: { force?: boolean }): Promise<void> {
    return this.delegate.prefetchAllItems(options);
  }

  /**
   * Clear all caches in the underlying provider.
   */
  clearCache(): void {
    this.delegate.clearCache();
  }

  softInvalidate(): void {
    this.delegate.softInvalidate();
  }

  /**
   * Clear caches and fire change events for lazy re-fetch.
   */
  invalidateAndRefresh(): void {
    this.delegate.invalidateAndRefresh();
  }
}
