/**
 * ProjectBoardService - IPC-backed project board service.
 *
 * Thin UI-layer service that delegates all GitHub Project Board operations
 * to the Go binary via JSON-over-stdio IPC. Reads project configuration
 * from .nightgauge/config.yaml.
 *
 * Phase 5: All business logic removed. Go binary owns board data.
 *
 * @see internal/github/board.go — Go-side board operations
 * @see internal/ipc/server.go — IPC method handlers
 */

import * as vscode from "vscode";
import { IpcClient, type BoardItem } from "./IpcClient";
import { IpcClientBase } from "./IpcClient";
import type { StatusCounts } from "./IpcClientBase";
import { getGitHubUser } from "../utils/incrediConfig";
import type { IWorkItemProvider } from "./types/WorkItemProvider";

// ---------------------------------------------------------------------------
// Output channel
// ---------------------------------------------------------------------------

let _outputChannel: vscode.OutputChannel | null = null;
function log(message: string): void {
  if (!_outputChannel) {
    try {
      _outputChannel = vscode.window.createOutputChannel("Nightgauge Pipeline");
    } catch {
      // Not in a VS Code host
    }
  }
  const ts = new Date().toISOString();
  const line = `[${ts}] [ProjectBoardService] ${message}`;
  if (_outputChannel) {
    _outputChannel.appendLine(line);
  } else {
    console.log(line);
  }
}

// ---------------------------------------------------------------------------
// Types — preserved for backward compatibility with tree views and commands
// ---------------------------------------------------------------------------

export interface ProjectConfig {
  name: string;
  number: number;
  id?: string;
  statusFieldId?: string;
  priorityFieldId?: string;
  sizeFieldId?: string;
  syncFilter?: string;
  default?: boolean;
}

export function parseEpicReference(body: string | undefined): number | undefined {
  if (!body) return undefined;
  const hashMatch = body.match(/part\s+of\s+(?:\w+\s+)*#(\d+)/i);
  if (hashMatch) return parseInt(hashMatch[1], 10);
  const urlMatch = body.match(
    /part\s+of\s+(?:\w+\s+)*https:\/\/github\.com\/[^/]+\/[^/]+\/issues\/(\d+)/i
  );
  if (urlMatch) return parseInt(urlMatch[1], 10);
  return undefined;
}

export type Priority = "P0" | "P1" | "P2" | "P3" | null;
export type Size = "XS" | "S" | "M" | "L" | "XL" | null;

export interface BlockingIssue {
  number: number;
  title: string;
  url: string;
  state: "OPEN" | "CLOSED";
}

export interface ReadyIssue {
  number: number;
  title: string;
  labels: string[];
  priority: Priority;
  size: Size;
  url: string;
  status?: string;
  epicRef?: number;
  /** Title of the parent epic (for cross-status resolution when epic is in a different tab) */
  epicTitle?: string;
  blockedBy?: BlockingIssue[];
  blocks?: BlockingIssue[];
  /** True when this issue is an epic (type:epic label + has sub-issues) */
  isEpic?: boolean;
  /** Issue numbers of native sub-issues (populated for epics only) */
  subIssueNumbers?: number[];
}

export type SortBy = "board" | "priority" | "number" | "size" | "dependencies" | "smart";

export type SortDirection = "asc" | "desc";

/**
 * Snapshot of the authenticated user's GitHub GraphQL API rate-limit state.
 * Emitted by ProjectBoardService.onRateLimitState whenever a fresh reading
 * arrives from the Go binary's shared tracker. Consumers (tree providers)
 * use this to pause or throttle their auto-refresh timers before quota runs
 * out.
 */
export interface RateLimitState {
  remaining: number;
  limit: number;
  /** Unix seconds — when the current rate-limit window resets. */
  resetAt: number;
  /** True when remaining drops to 0. Views should pause all refreshes. */
  exhausted: boolean;
  /** True when remaining < RATE_LIMIT_WARNING_THRESHOLD. Views should back off. */
  low: boolean;
}

// ---------------------------------------------------------------------------
// ProjectBoardService
// ---------------------------------------------------------------------------

/**
 * Board enrichment provider — fetches and caches issue metadata from the
 * GitHub Projects board (via IPC to Go binary). Implements IWorkItemProvider
 * so it can be substituted by CompositeAdapter or future non-GitHub adapters.
 *
 * Responsibilities: board status fetching, cache management, epic metadata
 * resolution, aggregated status counts.
 *
 * NOT responsible for: primary issue discovery across the full repository
 * (use CompositeAdapter + GitHubIssuesAdapter for that).
 */
export class ProjectBoardService implements vscode.Disposable, IWorkItemProvider {
  private workspaceRoot: string;
  private owner: string | null = null;
  private ownerType: string | undefined = undefined;
  private projectNumber: number | null = null;
  private repo: string | null = null;
  private projects: ProjectConfig[] = [];
  private selectedProject: string | null = null;
  private configLoaded = false;
  private cache = new Map<string, ReadyIssue[]>();
  private cacheTimes = new Map<string, number>();
  private allItemsCache: ReadyIssue[] | null = null;
  private allItemsCacheTime = 0;
  private boardCountsCache: StatusCounts | null = null;
  private boardCountsCacheTime = 0;
  private readonly cacheTtlMs: number;
  private static readonly DEFAULT_CACHE_TTL_MS = 300_000; // 5 minutes
  private ipc: IpcClient;
  private githubUser: string | undefined = undefined;
  private disposables: vscode.Disposable[] = [];
  /** In-flight deduplication: callers waiting on the same status share one promise */
  private inFlightRequests = new Map<string, Promise<ReadyIssue[]>>();
  private inFlightAllItems: Promise<ReadyIssue[]> | null = null;
  /** Rate limit tracking — avoids making API calls when quota is exhausted */
  private rateLimitRemaining: number | null = null;
  private rateLimitResetAt: number = 0; // Unix timestamp
  private rateLimitWarningShown = false;
  private static readonly RATE_LIMIT_WARNING_THRESHOLD = 100;
  /**
   * Diagnostic record of the most recent prefetchAllItems call. Lets callers
   * (the dashboard widget) distinguish "board genuinely empty" from "items
   * returned, all filtered out by repo match" without changing the existing
   * void-returning interface contract. Reset each call.
   */
  private lastPrefetchDiagnostics: {
    rawItemCount: number;
    filteredItemCount: number;
    expectedRepo: string | null;
  } | null = null;
  private lastPrefetchError: string | null = null;

  private readonly _onDidChangeTreeData = new vscode.EventEmitter<void>();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;

  private readonly _onItemsUpdated = new vscode.EventEmitter<void>();
  readonly onItemsUpdated = this._onItemsUpdated.event;

  private readonly _onStatusChanged = new vscode.EventEmitter<{
    repoSlug: string;
    statuses: string[];
  }>();
  readonly onStatusChanged = this._onStatusChanged.event;

  /**
   * Fires whenever a fresh rate-limit reading is observed. Tree providers
   * subscribe to pause / resume their auto-refresh timers so multiple VSCode
   * windows don't race to exhaust the shared per-user quota.
   */
  private readonly _onRateLimitState = new vscode.EventEmitter<RateLimitState>();
  readonly onRateLimitState = this._onRateLimitState.event;

  constructor(workspaceRoot: string, _cacheTtlMs?: number) {
    this.workspaceRoot = workspaceRoot;
    this.ipc = IpcClient.getInstance();
    this.cacheTtlMs = _cacheTtlMs ?? ProjectBoardService.DEFAULT_CACHE_TTL_MS;
  }

  dispose(): void {
    this._onDidChangeTreeData.dispose();
    this._onItemsUpdated.dispose();
    this._onStatusChanged.dispose();
    this._onRateLimitState.dispose();
    for (const d of this.disposables) d.dispose();
  }

  // -------------------------------------------------------------------------
  // Config
  // -------------------------------------------------------------------------

  async loadConfig(): Promise<void> {
    if (this.configLoaded) return;
    try {
      const result = await this.ipc.configGetProjectConfig(this.workspaceRoot);
      this.owner = result.owner || null;
      this.ownerType = result.ownerType || undefined;
      this.projectNumber = result.projectNumber || null;
      this.repo = result.defaultRepo || null;
      try {
        this.githubUser = getGitHubUser(this.workspaceRoot) ?? undefined;
      } catch {
        // Config read failed (e.g., test environment) — use default auth
      }
      if (result.projectNumber) {
        this.projects = [{ name: "Default", number: result.projectNumber, default: true }];
        this.selectedProject = "Default";
      }
      this.configLoaded = true;
      log(
        `Config loaded via IPC: owner=${this.owner}, repo=${this.repo}, project=${this.projectNumber}`
      );
      // Surface missing config to the user — silent empty views are confusing
      if (!this.owner || !this.projectNumber) {
        const missing = [
          !this.owner ? "project.owner (or top-level owner)" : "",
          !this.projectNumber ? "project.number" : "",
        ]
          .filter(Boolean)
          .join(", ");
        log(
          `WARNING: Incomplete project config — missing: ${missing}. ` +
            "Board views will be empty. Run /nightgauge:repo-init to fix."
        );
        vscode.window.showWarningMessage(
          `Nightgauge: project config incomplete — missing ${missing}. ` +
            "Issue views will be empty until config is fixed."
        );
      }
    } catch (err) {
      log(`Failed to load config via IPC: ${err}`);
    }
  }

  updateWorkspaceRoot(newRoot: string): void {
    this.workspaceRoot = newRoot;
    this.configLoaded = false;
    this.owner = null;
    this.projectNumber = null;
    this.repo = null;
    this.projects = [];
    this.selectedProject = null;
    this.cache.clear();
    this.cacheTimes.clear();
    this.allItemsCache = null;
    this.allItemsCacheTime = 0;
  }

  getOwner(): string | null {
    return this.owner;
  }

  getProjectNumber(): number | null {
    return this.projectNumber;
  }

  getProjects(): ProjectConfig[] {
    return [...this.projects];
  }

  getSelectedProject(): string | null {
    return this.selectedProject;
  }

  setSelectedProject(name: string): void {
    const project = this.projects.find((p) => p.name === name);
    if (project) {
      this.selectedProject = name;
      this.projectNumber = project.number;
      this.cache.clear();
      this.cacheTimes.clear();
      this.allItemsCache = null;
      this.allItemsCacheTime = 0;
      this._onDidChangeTreeData.fire();
    }
  }

  // -------------------------------------------------------------------------
  // Rate limit awareness
  // -------------------------------------------------------------------------

  /**
   * Last-known rate-limit state for this service. Consumers that attach late
   * (after checkRateLimit has already run once) can read this instead of
   * missing the initial event.
   */
  private lastRateLimitState: RateLimitState | null = null;

  /**
   * Returns the most recent rate-limit snapshot observed, or null if no
   * reading has occurred yet.
   */
  getRateLimitState(): RateLimitState | null {
    return this.lastRateLimitState;
  }

  /**
   * Check GitHub API rate limit. Returns true if safe to proceed.
   * When rate limit is exhausted, logs a warning and returns cached data.
   */
  private async checkRateLimit(): Promise<boolean> {
    // If we know we're rate-limited and the reset hasn't passed, skip the check
    if (this.rateLimitRemaining === 0 && Date.now() / 1000 < this.rateLimitResetAt) {
      return false;
    }

    try {
      const info = await this.ipc.githubRateLimit(this.githubUser);
      this.rateLimitRemaining = info.remaining;
      this.rateLimitResetAt = info.resetAt;

      const state: RateLimitState = {
        remaining: info.remaining,
        limit: info.limit,
        resetAt: info.resetAt,
        exhausted: info.remaining === 0,
        low: info.remaining < ProjectBoardService.RATE_LIMIT_WARNING_THRESHOLD,
      };
      this.lastRateLimitState = state;
      this._onRateLimitState.fire(state);

      if (info.remaining === 0) {
        const resetDate = new Date(info.resetAt * 1000);
        const minutes = Math.ceil((resetDate.getTime() - Date.now()) / 60_000);
        log(`GitHub API rate limit exhausted (0/${info.limit}). Resets in ${minutes} min.`);
        if (!this.rateLimitWarningShown) {
          this.rateLimitWarningShown = true;
          vscode.window.showWarningMessage(
            `Nightgauge: GitHub API rate limit exhausted. ` +
              `Issue views will show cached data until reset (~${minutes} min). ` +
              `Auto-refresh paused until reset.`
          );
        }
        return false;
      }

      if (info.remaining < ProjectBoardService.RATE_LIMIT_WARNING_THRESHOLD) {
        log(`GitHub API rate limit low: ${info.remaining}/${info.limit} remaining`);
      }

      // Reset warning flag when quota recovers
      if (info.remaining > ProjectBoardService.RATE_LIMIT_WARNING_THRESHOLD) {
        this.rateLimitWarningShown = false;
      }

      return true;
    } catch {
      // Can't check rate limit — proceed optimistically
      return true;
    }
  }

  // -------------------------------------------------------------------------
  // Data fetching — delegates to Go binary via IPC
  // -------------------------------------------------------------------------

  async getIssuesByStatus(
    status: string,
    sortBy?: SortBy,
    sortDirection?: SortDirection
  ): Promise<ReadyIssue[]> {
    await this.loadConfig();
    if (!this.owner || !this.projectNumber) {
      log(
        `getIssuesByStatus("${status}"): skipped — owner=${this.owner}, project=${this.projectNumber}`
      );
      return [];
    }

    // Check cache
    const cacheKey = `${this.projectNumber}:${status}`;
    const cached = this.cache.get(cacheKey);
    const cacheTime = this.cacheTimes.get(cacheKey) ?? 0;
    if (cached && Date.now() - cacheTime < this.cacheTtlMs) {
      return this.sortIssues(cached, sortBy, sortDirection);
    }

    // Deduplicate in-flight requests: if another caller is already fetching
    // the same status, piggyback on their promise instead of making a second
    // GitHub API call.
    const existing = this.inFlightRequests.get(cacheKey);
    if (existing) {
      const issues = await existing;
      return this.sortIssues(issues, sortBy, sortDirection);
    }

    const fetchPromise = this.fetchIssuesForStatus(status, cacheKey, cached);
    this.inFlightRequests.set(cacheKey, fetchPromise);
    try {
      const issues = await fetchPromise;
      return this.sortIssues(issues, sortBy, sortDirection);
    } finally {
      this.inFlightRequests.delete(cacheKey);
    }
  }

  private async fetchIssuesForStatus(
    status: string,
    cacheKey: string,
    cached: ReadyIssue[] | undefined
  ): Promise<ReadyIssue[]> {
    // Check rate limit before making API calls — return stale cache if exhausted
    const canProceed = await this.checkRateLimit();
    if (!canProceed) {
      return cached ?? [];
    }

    try {
      const statusMap: Record<string, string> = {
        ready: "Ready",
        "in-progress": "In progress",
        "in-review": "In review",
        done: "Done",
        backlog: "Backlog",
      };
      const apiStatus = statusMap[status] || status;

      const items = await this.ipc.boardList(
        this.owner!,
        this.projectNumber!,
        apiStatus,
        this.ownerType,
        this.githubUser
      );

      const issues = this.boardItemsToReadyIssues(items);
      this.cache.set(cacheKey, issues);
      this.cacheTimes.set(cacheKey, Date.now());

      return issues;
    } catch (err) {
      log(`IPC board.list failed: ${err}`);
      return cached ?? [];
    }
  }

  async getReadyIssues(sortBy?: SortBy): Promise<ReadyIssue[]> {
    return this.getIssuesByStatus("ready", sortBy);
  }

  async getAllItems(): Promise<ReadyIssue[]> {
    await this.loadConfig();
    if (!this.owner || !this.projectNumber) {
      log(`getAllItems: skipped — owner=${this.owner}, project=${this.projectNumber}`);
      return [];
    }

    // Return cached result if fresh — avoids redundant 7-page unfiltered
    // fetches when multiple tabs call this for epic grouping.
    if (this.allItemsCache && Date.now() - this.allItemsCacheTime < this.cacheTtlMs) {
      return this.allItemsCache;
    }

    // Deduplicate in-flight: multiple callers share one unfiltered fetch
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

  private async fetchAllItemsInternal(): Promise<ReadyIssue[]> {
    // Check rate limit before making API calls — return stale cache if exhausted
    const canProceed = await this.checkRateLimit();
    if (!canProceed) {
      return this.allItemsCache ?? [];
    }

    try {
      const items = await this.ipc.boardList(
        this.owner!,
        this.projectNumber!,
        undefined,
        this.ownerType,
        this.githubUser
      );
      const issues = this.boardItemsToReadyIssues(items);
      this.allItemsCache = issues;
      this.allItemsCacheTime = Date.now();
      return issues;
    } catch (err) {
      log(`IPC board.list (all) failed: ${err}`);
      return this.allItemsCache ?? [];
    }
  }

  async prefetchAllItems(options?: { force?: boolean }): Promise<void> {
    if (options?.force) {
      this.cache.clear();
      this.cacheTimes.clear();
      this.allItemsCache = null;
      this.allItemsCacheTime = 0;
    }
    this.lastPrefetchError = null;
    this.lastPrefetchDiagnostics = null;

    await this.loadConfig();
    if (!this.owner || !this.projectNumber) {
      log(`prefetchAllItems: skipped — owner=${this.owner}, project=${this.projectNumber}`);
      return;
    }

    const expectedRepo =
      this.owner && this.repo ? `${this.owner}/${this.repo}`.toLowerCase() : null;

    try {
      const items = await this.ipc.boardList(
        this.owner,
        this.projectNumber,
        undefined,
        this.ownerType,
        this.githubUser
      );
      const allIssues = this.boardItemsToReadyIssues(items);

      // Populate all-items cache so epic grouping gets a cache hit
      this.allItemsCache = allIssues;
      this.allItemsCacheTime = Date.now();

      // Group by status and cache per-status.
      // Iterate allIssues (post-filter) — not the raw items array — so indices
      // stay aligned after boardItemsToReadyIssues() filters out other-repo items.
      const byStatus = new Map<string, ReadyIssue[]>();
      for (const issue of allIssues) {
        const status = (issue.status || "Backlog").toLowerCase();
        const key = `${this.projectNumber}:${status}`;
        if (!byStatus.has(key)) byStatus.set(key, []);
        byStatus.get(key)!.push(issue);
      }

      const now = Date.now();
      for (const [key, issues] of byStatus) {
        this.cache.set(key, issues);
        this.cacheTimes.set(key, now);
      }

      this.lastPrefetchDiagnostics = {
        rawItemCount: items.length,
        filteredItemCount: allIssues.length,
        expectedRepo,
      };

      this._onItemsUpdated.fire();
    } catch (err) {
      log(`Prefetch failed: ${err}`);
      this.lastPrefetchError = err instanceof Error ? err.message : String(err);
    }
  }

  /**
   * Diagnostics from the most recent successful `prefetchAllItems` call.
   * Returns null if the last call was skipped (not configured) or failed.
   */
  getLastPrefetchDiagnostics(): {
    rawItemCount: number;
    filteredItemCount: number;
    expectedRepo: string | null;
  } | null {
    return this.lastPrefetchDiagnostics;
  }

  /**
   * Error message captured by the most recent `prefetchAllItems` call (was
   * previously swallowed via `log()` only). Returns null if the last call
   * succeeded or was skipped.
   */
  getLastPrefetchError(): string | null {
    return this.lastPrefetchError;
  }

  getItemsByStatusFromCache(
    status: string,
    _sortBy?: SortBy,
    _sortDirection?: SortDirection
  ): ReadyIssue[] {
    const statusMap: Record<string, string> = {
      Ready: "ready",
      "In progress": "in progress",
      "In review": "in review",
      Done: "done",
      Backlog: "backlog",
    };
    const normalizedStatus = statusMap[status] || status.toLowerCase();
    const cacheKey = `${this.projectNumber}:${normalizedStatus}`;
    return this.cache.get(cacheKey) ?? [];
  }

  clearCache(): void {
    this.cache.clear();
    this.cacheTimes.clear();
    this.allItemsCache = null;
    this.allItemsCacheTime = 0;
    this.boardCountsCache = null;
    this.boardCountsCacheTime = 0;
    this.inFlightRequests.clear();
    this.inFlightAllItems = null;
    // Reset configLoaded so the next fetch re-reads project config from the
    // Go binary. This is critical after adapter switches or config.yaml edits
    // — without it, loadConfig() returns immediately with stale (or null)
    // owner/projectNumber values and the tree shows empty.
    this.configLoaded = false;
  }

  /**
   * Invalidate cache timestamps without discarding stale data.
   *
   * Unlike clearCache(), this keeps every cached issue list intact so that
   * fetchIssuesForStatus can return stale data when the GitHub API is
   * rate-limited. Only the TTL timestamps are cleared, forcing the next
   * getIssuesByStatus() call to attempt a fresh fetch while still having a
   * non-empty fallback if the fetch fails.
   *
   * Use this for user-triggered refreshes (refreshAllBoards) instead of
   * clearCache(). clearCache() is reserved for situations where stale data
   * must not be served (workspace switch, adapter change).
   */
  softInvalidate(): void {
    this.cacheTimes.clear();
    this.allItemsCacheTime = 0;
    this.boardCountsCacheTime = 0;
    this.inFlightRequests.clear();
    this.inFlightAllItems = null;
    this.configLoaded = false;
  }

  /**
   * Invalidate all cached data and notify tree providers to re-render.
   *
   * Unlike `prefetchAllItems({ force: true })`, this does NOT eagerly fetch
   * all items (which is slow — fetches 400+ items across multiple pages).
   * Instead, it clears the cache and fires the update event so each tree
   * provider lazily re-fetches only its own status tab on next render.
   */
  invalidateAndRefresh(): void {
    this.clearCache();
    this._onItemsUpdated.fire();
  }

  /**
   * Invalidate only the specified statuses' cache entries for a repo and fire
   * onStatusChanged so RepositoriesTreeProvider can refresh just those nodes.
   */
  invalidateStatusCache(repoSlug: string, statuses: string[]): void {
    for (const status of statuses) {
      const cacheKey = `${this.projectNumber}:${status.toLowerCase()}`;
      this.cache.delete(cacheKey);
      this.cacheTimes.delete(cacheKey);
      this.inFlightRequests.delete(cacheKey);
    }
    // counts changed — force fresh fetch on next getAggregatedStatusCounts()
    this.boardCountsCache = null;
    this.boardCountsCacheTime = 0;
    this._onStatusChanged.fire({ repoSlug, statuses });
  }

  clearPerStatusCache(): void {
    this.clearCache();
  }

  /**
   * Build an epic metadata map from all cached per-status data.
   *
   * Scans every cached status bucket for issues with isEpic=true and returns
   * a Map<epicNumber, { number, title, url }>. This avoids the expensive
   * unfiltered getAllItems() call (537 items, 11s+) — instead reusing the
   * small per-status responses already in cache.
   *
   * @param extraIssues - Additional issues to scan (e.g., current tab data
   *                      that may not be cached yet)
   */
  getEpicMetadataFromCache(
    extraIssues?: ReadyIssue[]
  ): Map<number, { number: number; title: string; url: string }> {
    const map = new Map<number, { number: number; title: string; url: string }>();

    // Scan all cached per-status buckets
    for (const issues of this.cache.values()) {
      for (const issue of issues) {
        if (issue.isEpic) {
          map.set(issue.number, {
            number: issue.number,
            title: issue.title,
            url: issue.url,
          });
        }
      }
    }

    // Scan extra issues (e.g., freshly fetched tab data not yet cached)
    if (extraIssues) {
      for (const issue of extraIssues) {
        if (issue.isEpic) {
          map.set(issue.number, {
            number: issue.number,
            title: issue.title,
            url: issue.url,
          });
        }
      }
    }

    // Fallback: resolve epic titles from sub-issues' epicRef + epicTitle
    // when the epic itself is in a different status tab not yet cached.
    const allIssues = [...(extraIssues ?? []), ...[...this.cache.values()].flat()];
    for (const issue of allIssues) {
      if (issue.epicRef && issue.epicTitle && !map.has(issue.epicRef)) {
        map.set(issue.epicRef, {
          number: issue.epicRef,
          title: issue.epicTitle,
          url: "", // URL not available from sub-issue; click-through will be empty
        });
      }
    }

    return map;
  }

  async getAggregatedStatusCounts(): Promise<Record<string, number>> {
    await this.loadConfig();
    if (!this.owner || !this.projectNumber) {
      log(
        `getAggregatedStatusCounts: skipped — owner=${this.owner}, project=${this.projectNumber}`
      );
      return {};
    }

    if (this.boardCountsCache && Date.now() - this.boardCountsCacheTime < this.cacheTtlMs) {
      return { ...this.boardCountsCache };
    }

    try {
      // Uses board.counts IPC method — a single GraphQL query with aliases
      // that returns only totalCount per status. No item data fetched.
      const counts = await this.ipc.boardCounts(
        this.owner,
        this.projectNumber,
        this.ownerType,
        this.githubUser
      );
      this.boardCountsCache = counts;
      this.boardCountsCacheTime = Date.now();
      return { ...counts };
    } catch (err) {
      log(`getAggregatedStatusCounts failed: ${err}`);
      return {};
    }
  }

  // -------------------------------------------------------------------------
  // Sorting
  // -------------------------------------------------------------------------

  topologicalSort(issues: ReadyIssue[]): ReadyIssue[] {
    // Simple topological sort: unblocked first, then blocked
    const unblocked: ReadyIssue[] = [];
    const blocked: ReadyIssue[] = [];

    for (const issue of issues) {
      const hasOpenBlockers = issue.blockedBy?.some((b) => b.state === "OPEN");
      if (hasOpenBlockers) {
        blocked.push(issue);
      } else {
        unblocked.push(issue);
      }
    }

    return [...unblocked, ...blocked];
  }

  private sortIssues(
    issues: ReadyIssue[],
    sortBy?: SortBy,
    sortDirection?: SortDirection
  ): ReadyIssue[] {
    if (!sortBy || sortBy === "board") return issues;

    const dir = sortDirection === "desc" ? -1 : 1;
    const sorted = [...issues];

    switch (sortBy) {
      case "priority":
        sorted.sort((a, b) => {
          const pa = priorityRank(a.priority);
          const pb = priorityRank(b.priority);
          return (pa - pb) * dir;
        });
        break;
      case "number":
        sorted.sort((a, b) => (a.number - b.number) * dir);
        break;
      case "size":
        sorted.sort((a, b) => {
          const sa = sizeRank(a.size);
          const sb = sizeRank(b.size);
          return (sa - sb) * dir;
        });
        break;
      case "dependencies":
      case "smart":
        return this.topologicalSort(sorted);
    }

    return sorted;
  }

  // -------------------------------------------------------------------------
  // Conversion
  // -------------------------------------------------------------------------

  /**
   * Convert BoardItem[] to ReadyIssue[], deriving epicRef from
   * GitHub's native sub-issue relationships on epic items.
   *
   * epicRef is resolved in priority order:
   * 1. BoardItem.parentNumber — set by Go from the item's own `parent` field.
   *    Works even when the parent epic is in a different status tab.
   * 2. Sub-issue cross-reference within the current batch (epic is in same tab).
   * 3. Sub-issue cross-reference from other cached status tabs (fallback).
   */
  private boardItemsToReadyIssues(items: BoardItem[]): ReadyIssue[] {
    // Filter out items from other repositories. The org-level project board
    // contains issues from all nightgauge repos; only show items belonging to
    // the repo this workspace is configured for.
    const expectedRepo =
      this.owner && this.repo ? `${this.owner}/${this.repo}`.toLowerCase() : null;
    if (expectedRepo) {
      items = items.filter((item) => item.repo?.toLowerCase() === expectedRepo);
    }

    // Build parent lookup: child issue number → parent epic number.
    // Scan both the incoming items AND all cached status buckets so that
    // sub-issues whose parent epic is in a different status tab (e.g., epic
    // is "Ready" but sub-issue is "In progress") still get epicRef set.
    const parentMap = new Map<number, number>();
    const parentTitleMap = new Map<number, string>(); // epic number → title
    // Priority 1: parentIssueNumber field from Go (cross-tab, always accurate)
    for (const item of items) {
      if (item.parentIssueNumber) {
        parentMap.set(item.number, item.parentIssueNumber);
        if (item.parentIssueTitle) {
          parentTitleMap.set(item.parentIssueNumber, item.parentIssueTitle);
        }
      }
    }
    // Priority 2: sub-issue cross-reference within current batch
    for (const item of items) {
      if (item.isEpic && item.subIssues) {
        for (const sub of item.subIssues) {
          if (!parentMap.has(sub.number)) {
            parentMap.set(sub.number, item.number);
          }
        }
        if (!parentTitleMap.has(item.number)) {
          parentTitleMap.set(item.number, item.title);
        }
      }
    }
    // Priority 3: cached entries from other status tabs
    for (const cached of this.cache.values()) {
      for (const issue of cached) {
        if (issue.isEpic && issue.subIssueNumbers) {
          for (const subNum of issue.subIssueNumbers) {
            if (!parentMap.has(subNum)) {
              parentMap.set(subNum, issue.number);
            }
          }
        }
      }
    }

    return items.map((item) => ({
      number: item.number,
      title: item.title,
      labels: item.labels ?? [],
      priority: this.parsePriority(item.priority),
      size: this.parseSize(item.size),
      url: item.url,
      status: item.status,
      epicRef: parentMap.get(item.number),
      epicTitle: parentTitleMap.get(parentMap.get(item.number) ?? -1),
      isEpic: item.isEpic,
      subIssueNumbers: item.subIssues?.map((s) => s.number),
      blockedBy: item.blockedBy?.map((b) => ({
        number: b.number,
        title: b.title,
        url: "",
        state: b.state as "OPEN" | "CLOSED",
      })),
      blocks: item.blocking?.map((b) => ({
        number: b.number,
        title: b.title,
        url: "",
        state: b.state as "OPEN" | "CLOSED",
      })),
    }));
  }

  private parsePriority(p: string | undefined): Priority {
    if (!p) return null;
    if (p === "P0" || p === "P1" || p === "P2" || p === "P3") return p;
    // Map label-style priorities
    const map: Record<string, Priority> = {
      critical: "P0",
      high: "P1",
      medium: "P2",
      low: "P3",
    };
    return map[p.toLowerCase()] ?? null;
  }

  private parseSize(s: string | undefined): Size {
    if (!s) return null;
    const upper = s.toUpperCase();
    if (["XS", "S", "M", "L", "XL"].includes(upper)) return upper as Size;
    return null;
  }
}

// ---------------------------------------------------------------------------
// Helpers
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
      return 4;
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
      return 5;
  }
}
