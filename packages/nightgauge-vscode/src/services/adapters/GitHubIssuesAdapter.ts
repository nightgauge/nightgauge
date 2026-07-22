/**
 * GitHubIssuesAdapter — IWorkItemProvider implementation backed by repository issues.
 *
 * Queries repository issues directly via IpcClient.issueList(), converting
 * IssueDetail[] to WorkItem[] without requiring GitHub Project Board membership.
 * This makes every repository issue discoverable, regardless of whether it has
 * been added to the project board.
 *
 * Design decisions:
 * - No status filtering: all repo issues are returned for any requested status.
 *   CompositeAdapter (downstream #2428) will merge and filter across sources.
 * - Simple in-memory cache with 5-minute TTL (matches ProjectBoardService).
 * - In-flight request deduplication: concurrent callers share one IPC call.
 * - source metadata set to { provider: "github", repository: "owner/repo" }.
 *
 * @see Issue #2566
 * @see IWorkItemProvider — interface this class implements
 * @see ProjectBoardService — reference implementation for caching patterns
 */

import * as vscode from "vscode";
import type { Event } from "vscode";
import { IpcClient } from "../IpcClient";
import type { IssueDetail } from "../IpcClientBase";
import type { Priority, Size, SortBy, SortDirection, BlockingIssue } from "../ProjectBoardService";
import type { WorkItem, IWorkItemProvider } from "../types/WorkItemProvider";
import { inferWorkItemStatus } from "../../utils/statusInference";

// ---------------------------------------------------------------------------
// GitHubIssuesAdapter
// ---------------------------------------------------------------------------

export class GitHubIssuesAdapter implements IWorkItemProvider {
  private static readonly DEFAULT_CACHE_TTL_MS = 300_000; // 5 minutes

  private readonly cacheTtlMs: number;

  /** All-items cache for getAllItems() */
  private allItemsCache: WorkItem[] | null = null;
  private allItemsCacheTime = 0;

  /** In-flight deduplication for getAllItems() */
  private inFlightAllItems: Promise<WorkItem[]> | null = null;

  private readonly _onDidChangeTreeData = new vscode.EventEmitter<void>();
  readonly onDidChangeTreeData: Event<void> = this._onDidChangeTreeData.event;

  private readonly _onItemsUpdated = new vscode.EventEmitter<void>();
  readonly onItemsUpdated: Event<void> = this._onItemsUpdated.event;

  constructor(
    private readonly workspaceRoot: string,
    private readonly owner: string,
    private readonly repo: string,
    private readonly ipc: IpcClient,
    cacheTtlMs?: number
  ) {
    this.cacheTtlMs = cacheTtlMs ?? GitHubIssuesAdapter.DEFAULT_CACHE_TTL_MS;
  }

  dispose(): void {
    this._onDidChangeTreeData.dispose();
    this._onItemsUpdated.dispose();
  }

  // -------------------------------------------------------------------------
  // IWorkItemProvider — data fetching
  // -------------------------------------------------------------------------

  /**
   * Return all repository issues regardless of the requested status.
   *
   * Unlike ProjectBoardService, this adapter does not filter by GitHub Project
   * Board status. All repo issues are discoverable; CompositeAdapter (downstream
   * issue #2428) will decide how to merge and filter across sources.
   */
  async getIssuesByStatus(
    _status: string,
    sortBy?: SortBy,
    sortDirection?: SortDirection
  ): Promise<WorkItem[]> {
    const items = await this.getAllItems();
    return this.sortItems(items, sortBy, sortDirection);
  }

  /** Convenience: returns all repo issues (status parameter ignored). */
  async getReadyIssues(sortBy?: SortBy): Promise<WorkItem[]> {
    return this.getIssuesByStatus("ready", sortBy);
  }

  /** Fetch ALL repository issues across all statuses. */
  async getAllItems(): Promise<WorkItem[]> {
    // Cache hit
    if (this.allItemsCache && Date.now() - this.allItemsCacheTime < this.cacheTtlMs) {
      return this.allItemsCache;
    }

    // Deduplicate in-flight: multiple callers share one IPC call
    if (this.inFlightAllItems) {
      return this.inFlightAllItems;
    }

    const fetchPromise = this.fetchAllItemsInternal();
    this.inFlightAllItems = fetchPromise;
    try {
      return await fetchPromise;
    } finally {
      this.inFlightAllItems = null;
    }
  }

  private async fetchAllItemsInternal(): Promise<WorkItem[]> {
    try {
      const details = await this.ipc.issueList(this.owner, this.repo);
      const items = details.map((d) => this.convertIssueDetailToWorkItem(d));
      this.allItemsCache = items;
      this.allItemsCacheTime = Date.now();
      this._onItemsUpdated.fire();
      return items;
    } catch (err) {
      console.error(`[GitHubIssuesAdapter] issueList failed: ${err}`);
      return this.allItemsCache ?? [];
    }
  }

  // -------------------------------------------------------------------------
  // IWorkItemProvider — cache access
  // -------------------------------------------------------------------------

  /**
   * Read-only cache access — returns cached items or empty array.
   * Status parameter is ignored (all items are cached together).
   */
  getItemsByStatusFromCache(
    _status: string,
    sortBy?: SortBy,
    sortDirection?: SortDirection
  ): WorkItem[] {
    const cached = this.allItemsCache ?? [];
    return this.sortItems(cached, sortBy, sortDirection);
  }

  /**
   * Build an epic metadata map from cached items.
   * Also resolves epic metadata from sub-issues' epicRef + epicTitle fields.
   */
  getEpicMetadataFromCache(
    extraIssues?: WorkItem[]
  ): Map<number, { number: number; title: string; url: string }> {
    const map = new Map<number, { number: number; title: string; url: string }>();

    const allIssues = [...(this.allItemsCache ?? []), ...(extraIssues ?? [])];

    for (const item of allIssues) {
      if (item.isEpic) {
        map.set(item.number, { number: item.number, title: item.title, url: item.url });
      }
    }

    // Fallback: resolve epic titles from sub-issues' epicRef + epicTitle
    for (const item of allIssues) {
      if (item.epicRef && item.epicTitle && !map.has(item.epicRef)) {
        map.set(item.epicRef, {
          number: item.epicRef,
          title: item.epicTitle,
          url: "", // URL not available from sub-issue reference
        });
      }
    }

    return map;
  }

  /**
   * Return issue counts keyed by status.
   * Since this adapter has no board status, all items are counted under "repo".
   */
  async getAggregatedStatusCounts(): Promise<Record<string, number>> {
    const items = await this.getAllItems();
    return { repo: items.length };
  }

  // -------------------------------------------------------------------------
  // IWorkItemProvider — prefetch and cache management
  // -------------------------------------------------------------------------

  async prefetchAllItems(options?: { force?: boolean }): Promise<void> {
    if (options?.force) {
      this.clearCache();
    }
    await this.getAllItems();
  }

  clearCache(): void {
    this.allItemsCache = null;
    this.allItemsCacheTime = 0;
    this.inFlightAllItems = null;
  }

  softInvalidate(): void {
    // Expire the timestamp so next fetch is forced, but keep stale data as fallback.
    this.allItemsCacheTime = 0;
    this.inFlightAllItems = null;
  }

  /**
   * Clear cache and fire change events so tree providers lazily re-fetch.
   */
  invalidateAndRefresh(): void {
    this.clearCache();
    this._onDidChangeTreeData.fire();
    this._onItemsUpdated.fire();
  }

  // -------------------------------------------------------------------------
  // Conversion — IssueDetail → WorkItem
  // -------------------------------------------------------------------------

  private convertIssueDetailToWorkItem(detail: IssueDetail): WorkItem {
    const blockedBy: BlockingIssue[] | undefined = detail.blockedBy?.map((b) => ({
      number: b.number,
      title: b.title,
      url: "",
      state: b.state as "OPEN" | "CLOSED",
    }));

    const blocks: BlockingIssue[] | undefined = detail.blocking?.map((b) => ({
      number: b.number,
      title: b.title,
      url: "",
      state: b.state as "OPEN" | "CLOSED",
    }));

    const subIssueNumbers: number[] | undefined = detail.subIssues?.map((s) => s.number);
    const labels = detail.labels ?? [];

    return {
      number: detail.number,
      title: detail.title,
      labels,
      priority: inferPriorityFromLabels(labels),
      size: inferSizeFromLabels(labels),
      url: detail.url,
      status: inferWorkItemStatus(
        {
          number: detail.number,
          labels: detail.labels ?? [],
          issueState: detail.state as "OPEN" | "CLOSED" | undefined,
          blockedBy: detail.blockedBy?.map((b) => ({ state: b.state })),
        },
        undefined // No pipeline state available at construction time — Step 1 is skipped
      ),
      epicRef: detail.parentIssueNumber,
      isEpic: detail.isEpic,
      subIssueNumbers,
      blockedBy: blockedBy?.length ? blockedBy : undefined,
      blocks: blocks?.length ? blocks : undefined,
      source: {
        provider: "github",
        repository: `${this.owner}/${this.repo}`,
      },
    };
  }

  // -------------------------------------------------------------------------
  // Sorting
  // -------------------------------------------------------------------------

  private sortItems(items: WorkItem[], sortBy?: SortBy, sortDirection?: SortDirection): WorkItem[] {
    if (!sortBy || sortBy === "board") return items;

    const dir = sortDirection === "desc" ? -1 : 1;
    const sorted = [...items];

    switch (sortBy) {
      case "priority":
        sorted.sort((a, b) => (priorityRank(a.priority) - priorityRank(b.priority)) * dir);
        break;
      case "number":
        sorted.sort((a, b) => (a.number - b.number) * dir);
        break;
      case "size":
        sorted.sort((a, b) => (sizeRank(a.size) - sizeRank(b.size)) * dir);
        break;
      case "dependencies":
      case "smart":
        return topologicalSort(sorted);
    }

    return sorted;
  }
}

// ---------------------------------------------------------------------------
// Fallback metadata inference (module-level, shared with tests)
// ---------------------------------------------------------------------------

/**
 * Infer issue priority from GitHub labels.
 *
 * Repo-only issues have no project board priority field. This function parses
 * `priority:*` labels to produce a best-effort priority value so that
 * ReadyIssueTreeItem can render badges even for issues not on the board.
 *
 * Label → Priority mapping:
 *   priority:critical → P0
 *   priority:high     → P1
 *   priority:medium   → P2 (default when no match)
 *   priority:low      → P3
 *
 * When multiple priority labels are present, the first matching label wins
 * (array order). GitHub label order is not semantically meaningful, so
 * callers should not rely on tie-breaking behavior.
 *
 * @param labels - Array of label slug strings from the GitHub issue
 * @returns Inferred Priority, defaulting to "P2" when no priority label is found
 */
export function inferPriorityFromLabels(labels: string[]): Priority {
  for (const label of labels) {
    const lower = label.toLowerCase();
    if (lower === "priority:critical") return "P0";
    if (lower === "priority:high") return "P1";
    if (lower === "priority:medium") return "P2";
    if (lower === "priority:low") return "P3";
  }
  return "P2"; // Default fallback
}

/**
 * Infer issue size from GitHub labels.
 *
 * Repo-only issues have no project board size field. This function parses
 * `size:*` labels to produce a best-effort size value for badge rendering.
 *
 * Label → Size mapping:
 *   size:xs → XS
 *   size:s  → S
 *   size:m  → M (default when no match)
 *   size:l  → L
 *   size:xl → XL
 *
 * @param labels - Array of label slug strings from the GitHub issue
 * @returns Inferred Size, defaulting to "M" when no size label is found
 */
export function inferSizeFromLabels(labels: string[]): Size {
  for (const label of labels) {
    const lower = label.toLowerCase();
    if (lower === "size:xs") return "XS";
    if (lower === "size:s") return "S";
    if (lower === "size:m") return "M";
    if (lower === "size:l") return "L";
    if (lower === "size:xl") return "XL";
  }
  return "M"; // Default fallback
}

// ---------------------------------------------------------------------------
// Sorting helpers (module-level, shared with tests)
// ---------------------------------------------------------------------------

function priorityRank(p: Priority): number {
  switch (p) {
    case "P0":
      return 0;
    case "P1":
      return 1;
    case "P2":
      return 2;
    case "P3":
      return 3;
    default:
      return 99;
  }
}

function sizeRank(s: Size): number {
  switch (s) {
    case "XS":
      return 0;
    case "S":
      return 1;
    case "M":
      return 2;
    case "L":
      return 3;
    case "XL":
      return 4;
    default:
      return 99;
  }
}

function topologicalSort(items: WorkItem[]): WorkItem[] {
  const unblocked: WorkItem[] = [];
  const blocked: WorkItem[] = [];
  for (const item of items) {
    const hasOpenBlockers = item.blockedBy?.some((b) => b.state === "OPEN");
    if (hasOpenBlockers) {
      blocked.push(item);
    } else {
      unblocked.push(item);
    }
  }
  return [...unblocked, ...blocked];
}
