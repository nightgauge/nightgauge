/**
 * CompositeAdapter — IWorkItemProvider combining repo issues with optional board enrichment.
 *
 * Merges issue data from two sources:
 *   1. Board source (ProjectBoardService) — supplies Priority, Size, and board Status.
 *   2. Repo source (GitHubIssuesAdapter) — discovers all repository issues.
 *
 * Merge strategy:
 *   - Issues present on the board use board data directly (board is authoritative).
 *   - Issues not on the board are included from the repo source with inferred status.
 *   - Board lookups degrade gracefully: if the board source fails, repo issues still render.
 *
 * This allows consumers to switch from ProjectBoardService to CompositeAdapter without
 * changing issue semantics — board-enriched issues behave identically to before.
 *
 * Lazy initialization:
 *   The GitHubIssuesAdapter is created on first fetch when no repoSource is provided,
 *   because resolving owner/repo requires an async `getRepoIdentity()` call. The
 *   constructor is synchronous so `createWorkItemProvider()` in bootstrap/services.ts
 *   stays synchronous.
 *
 * Dependency injection:
 *   An explicit `repoSource` may be passed as the third constructor argument. When
 *   provided it is used directly, bypassing lazy init. This makes unit tests
 *   straightforward — tests pass mock IWorkItemProvider instances instead of relying
 *   on file-system detection or IPC connections.
 *
 * Design decisions:
 *   - Board source is injected at construction time (caller controls the instance).
 *   - Deduplication is by issue number: the board item wins when both sources supply
 *     the same number, ensuring board metadata (priority, size, status) is authoritative.
 *   - Events are forwarded from all active sources to composite emitters so tree views
 *     refresh when either source updates.
 *   - `clearCache()` clears both underlying caches plus the merged cache.
 *
 * @see Issue #2567
 * @see IWorkItemProvider — interface this class implements
 * @see GitHubIssuesAdapter — repo discovery source
 * @see ProjectBoardService — board enrichment source
 */

import * as vscode from "vscode";
import type { Event } from "vscode";
import type { SortBy, SortDirection } from "../ProjectBoardService";
import type { WorkItem, IWorkItemProvider } from "../types/WorkItemProvider";
import { GitHubIssuesAdapter } from "./GitHubIssuesAdapter";
import { IpcClient } from "../IpcClient";
import { getRepoIdentity } from "../../utils/configPathResolver";

// ---------------------------------------------------------------------------
// CompositeAdapter
// ---------------------------------------------------------------------------

export class CompositeAdapter implements IWorkItemProvider {
  private readonly _onDidChangeTreeData = new vscode.EventEmitter<void>();
  readonly onDidChangeTreeData: Event<void> = this._onDidChangeTreeData.event;

  private readonly _onItemsUpdated = new vscode.EventEmitter<void>();
  readonly onItemsUpdated: Event<void> = this._onItemsUpdated.event;

  /** Repo source — either injected directly or lazily created from workspaceRoot */
  private repoSource: IWorkItemProvider | null = null;
  /** Promise guarding a single in-flight lazy init (used when repoSource not injected) */
  private initPromise: Promise<IWorkItemProvider | null> | null = null;

  /** Merged all-items cache — invalidated when either source fires onItemsUpdated */
  private mergedAllItemsCache: WorkItem[] | null = null;

  /** In-flight deduplication for getAllItems() */
  private inFlightAllItems: Promise<WorkItem[]> | null = null;

  /** Subscriptions to forward upstream events */
  private readonly disposables: { dispose(): void }[] = [];

  constructor(
    private readonly workspaceRoot: string,
    private readonly boardSource: IWorkItemProvider | null = null,
    repoSource?: IWorkItemProvider
  ) {
    // Accept an injected repo source (used in tests to avoid IPC / file-system access).
    // When provided, no lazy init is needed.
    if (repoSource) {
      this.repoSource = repoSource;
      this.disposables.push(
        repoSource.onDidChangeTreeData(() => {
          this.mergedAllItemsCache = null;
          this._onDidChangeTreeData.fire();
        }),
        repoSource.onItemsUpdated(() => {
          this.mergedAllItemsCache = null;
          this._onItemsUpdated.fire();
        })
      );
    }

    // Forward board source events immediately (board source is always present at init)
    if (boardSource) {
      this.disposables.push(
        boardSource.onDidChangeTreeData(() => {
          this.mergedAllItemsCache = null;
          this._onDidChangeTreeData.fire();
        }),
        boardSource.onItemsUpdated(() => {
          this.mergedAllItemsCache = null;
          this._onItemsUpdated.fire();
        })
      );
    }
  }

  dispose(): void {
    for (const d of this.disposables) {
      d.dispose();
    }
    this._onDidChangeTreeData.dispose();
    this._onItemsUpdated.dispose();
    // Only dispose the repo source if it exposes a dispose method (GitHubIssuesAdapter does)
    (this.repoSource as { dispose?: () => void } | null)?.dispose?.();
  }

  // -------------------------------------------------------------------------
  // Lazy initialization of repo source
  // -------------------------------------------------------------------------

  /**
   * Ensure the repo source is initialized, lazily creating a GitHubIssuesAdapter
   * from workspaceRoot when no repoSource was injected at construction time.
   * Returns null when repo identity cannot be determined — callers degrade gracefully.
   */
  private async ensureRepoSource(): Promise<IWorkItemProvider | null> {
    // Already initialized (either injected or previously resolved)
    if (this.repoSource) {
      return this.repoSource;
    }

    // Deduplicate concurrent init calls
    if (this.initPromise) {
      return this.initPromise;
    }

    this.initPromise = (async () => {
      try {
        const identity = await getRepoIdentity(this.workspaceRoot);
        if (!identity?.owner || !identity?.repo) {
          console.warn(
            "[CompositeAdapter] Could not determine owner/repo from workspace — " +
              "repo source will be skipped"
          );
          return null;
        }

        const adapter = new GitHubIssuesAdapter(
          this.workspaceRoot,
          identity.owner,
          identity.repo,
          IpcClient.getInstance()
        );

        // Wire up events from the lazily created repo source
        this.disposables.push(
          adapter.onDidChangeTreeData(() => {
            this.mergedAllItemsCache = null;
            this._onDidChangeTreeData.fire();
          }),
          adapter.onItemsUpdated(() => {
            this.mergedAllItemsCache = null;
            this._onItemsUpdated.fire();
          })
        );

        this.repoSource = adapter;
        return adapter;
      } catch (err) {
        console.error(`[CompositeAdapter] Failed to initialize repo source: ${err}`);
        return null;
      } finally {
        this.initPromise = null;
      }
    })();

    return this.initPromise;
  }

  // -------------------------------------------------------------------------
  // IWorkItemProvider — data fetching
  // -------------------------------------------------------------------------

  /**
   * Fetch work items for the requested board status.
   *
   * Returns the merged issue set filtered to the given status. Board-enriched
   * issues appear under their actual board status; repo-only issues appear under
   * their inferred status.
   */
  async getIssuesByStatus(
    status: string,
    sortBy?: SortBy,
    sortDirection?: SortDirection
  ): Promise<WorkItem[]> {
    const all = await this.getAllItems();
    const filtered = all.filter(
      (item) => (item.status ?? "").toLowerCase() === status.toLowerCase()
    );
    return this.sortItems(filtered, sortBy, sortDirection);
  }

  /** Convenience: fetch items with status "Ready". */
  async getReadyIssues(sortBy?: SortBy): Promise<WorkItem[]> {
    return this.getIssuesByStatus("Ready", sortBy);
  }

  /**
   * Fetch ALL items merged across repo and board sources.
   *
   * Merge rules:
   *   1. Fetch all repo issues and all board items concurrently.
   *   2. Build a map keyed by issue number from repo items.
   *   3. For each board item: upsert into the map — board data wins (authoritative
   *      priority, size, status, and blocking relationships).
   *   4. Remaining map entries are repo-only issues with inferred status.
   *
   * Board fetch failure is non-fatal: if the board source throws, the merged
   * result contains only repo issues with inferred status.
   * Repo fetch failure is non-fatal: if owner/repo cannot be resolved or the IPC
   * call fails, only board items are returned.
   */
  async getAllItems(): Promise<WorkItem[]> {
    if (this.mergedAllItemsCache) {
      return this.mergedAllItemsCache;
    }

    if (this.inFlightAllItems) {
      return this.inFlightAllItems;
    }

    const fetchPromise = this.fetchAndMerge();
    this.inFlightAllItems = fetchPromise;
    try {
      return await fetchPromise;
    } finally {
      this.inFlightAllItems = null;
    }
  }

  private async fetchAndMerge(): Promise<WorkItem[]> {
    // Resolve repo source (may be null if owner/repo unknown)
    const repoSource = await this.ensureRepoSource();

    // Fetch repo issues and board items concurrently — both failures are non-fatal
    const [repoItems, boardItems] = await Promise.all([
      repoSource
        ? repoSource.getAllItems().catch((err) => {
            console.error(`[CompositeAdapter] repo source getAllItems failed: ${err}`);
            return [] as WorkItem[];
          })
        : Promise.resolve([] as WorkItem[]),
      this.boardSource
        ? this.boardSource.getAllItems().catch((err) => {
            console.warn(
              `[CompositeAdapter] board source getAllItems failed (degraded mode): ${err}`
            );
            return [] as WorkItem[];
          })
        : Promise.resolve([] as WorkItem[]),
    ]);

    // Build a map from repo items (repo is the starting point — all issues discoverable)
    const merged = new Map<number, WorkItem>(repoItems.map((item) => [item.number, item]));

    // Upsert board items — board data is authoritative for enriched fields
    for (const boardItem of boardItems) {
      merged.set(boardItem.number, boardItem);
    }

    const result = Array.from(merged.values());
    this.mergedAllItemsCache = result;
    return result;
  }

  // -------------------------------------------------------------------------
  // IWorkItemProvider — cache access
  // -------------------------------------------------------------------------

  /**
   * Read-only cache access filtered by status.
   * Returns empty array if the merged cache is cold (no prior getAllItems call).
   */
  getItemsByStatusFromCache(
    status: string,
    sortBy?: SortBy,
    sortDirection?: SortDirection
  ): WorkItem[] {
    const all = this.mergedAllItemsCache ?? [];
    const filtered = all.filter(
      (item) => (item.status ?? "").toLowerCase() === status.toLowerCase()
    );
    return this.sortItems(filtered, sortBy, sortDirection);
  }

  /**
   * Build an epic metadata map from the merged cache plus any extra issues.
   *
   * Prefers board source for epic metadata (richer sub-issue relationships).
   * Supplements with repo source epics not present in the board map.
   */
  getEpicMetadataFromCache(
    extraIssues?: WorkItem[]
  ): Map<number, { number: number; title: string; url: string }> {
    if (this.boardSource) {
      const boardMap = this.boardSource.getEpicMetadataFromCache(extraIssues);
      // Supplement with repo source epics not in the board map (if already initialized)
      if (this.repoSource) {
        const repoMap = this.repoSource.getEpicMetadataFromCache(extraIssues);
        for (const [num, meta] of repoMap) {
          if (!boardMap.has(num)) {
            boardMap.set(num, meta);
          }
        }
      }
      return boardMap;
    }

    if (this.repoSource) {
      return this.repoSource.getEpicMetadataFromCache(extraIssues);
    }

    // Neither source initialized — build from merged cache + extras
    const map = new Map<number, { number: number; title: string; url: string }>();
    const allItems = [...(this.mergedAllItemsCache ?? []), ...(extraIssues ?? [])];
    for (const item of allItems) {
      if (item.isEpic) {
        map.set(item.number, { number: item.number, title: item.title, url: item.url });
      }
    }
    return map;
  }

  /**
   * Return issue counts keyed by status, merged across both sources.
   */
  async getAggregatedStatusCounts(): Promise<Record<string, number>> {
    const all = await this.getAllItems();
    const counts: Record<string, number> = {};
    for (const item of all) {
      const status = item.status ?? "unknown";
      counts[status] = (counts[status] ?? 0) + 1;
    }
    return counts;
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
    this.mergedAllItemsCache = null;
    this.inFlightAllItems = null;
    this.repoSource?.clearCache();
    this.boardSource?.clearCache();
  }

  softInvalidate(): void {
    this.mergedAllItemsCache = null;
    this.inFlightAllItems = null;
    this.repoSource?.softInvalidate();
    this.boardSource?.softInvalidate();
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
// Sorting helpers
// ---------------------------------------------------------------------------

function priorityRank(p: WorkItem["priority"]): number {
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

function sizeRank(s: WorkItem["size"]): number {
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
