/**
 * ProjectBoardTreeProvider - TreeDataProvider for project board issues by status
 *
 * Displays issues from the GitHub Project board filtered by status,
 * allowing developers to see work items across different stages without leaving VS Code.
 * Supports multiple status tabs: Ready, In Progress, In Review, Backlog.
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./items/BaseTreeItem";
import { ReadyIssueTreeItem } from "./items/ReadyIssueTreeItem";
import { EpicGroupTreeItem, groupIssuesByEpic } from "./items/EpicGroupTreeItem";
import { type SortBy, type SortDirection, type ReadyIssue } from "../services/ProjectBoardService";
import type { IWorkItemProvider } from "../services/types/WorkItemProvider";
import { type TabId, type TabConfig, getTabConfig } from "../types/TabConfig";
import { IpcClient } from "../services/IpcClient";
import { withCallSource } from "../services/callSource";
import {
  type FilterPriority,
  type FilterSize,
  type FilterComponent,
  matchesPriorityFilter,
  matchesSizeFilter,
  matchesComponentFilter,
  matchesSearchText,
  hasActiveFilters,
} from "../types/FilterConfig";
import { isBlocked } from "../utils/dependencyUtils";
import type { PipelineStateService } from "../services/PipelineStateService";
import { ConfigBridge } from "../services/ConfigBridge";
import { getReadyItemsSettings } from "../config/readyItemsSettings";
import { getProjectBoardSettings } from "../config/projectBoardSettings";
import { AutonomousActivityState } from "../utils/autonomousActivityState";

/**
 * Action tree item for displaying messages/actions in the tree
 */
class ProjectBoardActionItem extends BaseTreeItem {
  constructor(label: string, icon: string, command?: vscode.Command) {
    super(label, vscode.TreeItemCollapsibleState.None);
    this.setIcon(icon);
    if (command) {
      this.command = command;
    }
  }
}

/**
 * ProjectBoardTreeProvider - TreeDataProvider for issues by status
 *
 * @example
 * ```typescript
 * const service = new ProjectBoardService('/path/to/workspace');
 * const provider = new ProjectBoardTreeProvider(service, 'ready');
 *
 * const treeView = vscode.window.createTreeView('nightgauge.projectBoard.ready', {
 *   treeDataProvider: provider,
 *   showCollapseAll: false,
 * });
 * ```
 */
export class ProjectBoardTreeProvider
  implements vscode.TreeDataProvider<BaseTreeItem>, vscode.Disposable
{
  private _onDidChangeTreeData = new vscode.EventEmitter<BaseTreeItem | undefined | void>();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;

  private projectBoardService: IWorkItemProvider;
  private tabId: TabId;
  private tabConfig: TabConfig;
  private sortBy: SortBy = "board";
  private sortDirection: SortDirection = "asc";
  private filterPriority: FilterPriority = "all";
  private filterSize: FilterSize = "all";
  private filterComponent: FilterComponent = "all";
  private searchText: string = "";
  private hideBlocked: boolean = false;
  private autoRefreshEnabled: boolean = false;
  private autoRefreshInterval: number = 300000; // 5 minutes default
  private autoRefreshTimer: NodeJS.Timeout | null = null;
  /**
   * When the GitHub rate-limit is exhausted or low, the auto-refresh timer is
   * paused until this Unix-ms timestamp (aligned with the rate-limit resetAt).
   * Zero means "not paused". Consulted inside the timer callback so resumes
   * happen lazily without having to reschedule the interval.
   */
  private autoRefreshPausedUntilMs: number = 0;
  private showDependencies: boolean = true;
  private groupByEpic: boolean = true;
  private defaultEpicCollapsed: boolean = false;
  private isLoading: boolean = false;
  /**
   * Monotonically increasing fetch ID, incremented by refresh().
   * fetchIssuesByStatus() captures this at the start of each fetch and
   * discards results if the ID has changed (i.e., refresh() was called
   * mid-fetch due to a repo switch), preventing stale data from rendering.
   */
  private fetchId: number = 0;
  private lastError: string | null = null;
  private cachedItems: ReadyIssueTreeItem[] = [];
  private cachedEpicGroups: EpicGroupTreeItem[] = [];
  private disposables: vscode.Disposable[] = [];

  // Multi-select state (Issue #125)
  private multiSelectEnabled: boolean = false;
  private selectedIssueNumbers: Set<number> = new Set();

  // State service for auto-refresh on stage completion (Issue #151)
  private stateService: PipelineStateService | null = null;
  /** Shared debounce timer for stage-complete board refresh (all instances). */
  private static stageRefreshTimer: NodeJS.Timeout | null = null;
  private static readonly STAGE_REFRESH_DEBOUNCE_MS = 300;
  // Cached pipeline issue number and backtrack count for tree item decoration (Issue #1349)
  private _activePipelineIssueNumber: number | null = null;
  private _activeBacktrackCount: number = 0;

  // Drag and drop controller for issue tree items (Issue #296)
  dragAndDropController: vscode.TreeDragAndDropController<BaseTreeItem> | undefined;

  // TreeView reference for updating title with counts (Issue #306)
  private treeView: vscode.TreeView<BaseTreeItem> | undefined;

  constructor(projectBoardService: IWorkItemProvider, tabId: TabId) {
    this.projectBoardService = projectBoardService;
    this.tabId = tabId;

    const config = getTabConfig(tabId);
    if (!config) {
      throw new Error(`Invalid tab ID: ${tabId}`);
    }
    this.tabConfig = config;

    this.loadSettings();
    this.watchSettings();

    // Subscribe to progressive rendering updates from the service.
    // When a new page of items arrives during pagination, re-render the tree
    // with the partial data so issues appear immediately instead of waiting
    // for all pages to complete.
    if (projectBoardService.onItemsUpdated) {
      this.disposables.push(
        projectBoardService.onItemsUpdated(() => {
          if (this.isLoading) {
            this.isLoading = false;
          }
          this._onDidChangeTreeData.fire();
        })
      );
    }

    // Pause auto-refresh when the shared rate-limit tracker reports the quota
    // is exhausted or running low. Without this, N VSCode windows × M tabs
    // each × auto-refresh all fire independent fetches and collectively burn
    // through the 5000/hr GraphQL quota in minutes (see issue #2834).
    if (projectBoardService.onRateLimitState) {
      // Some mocked vscode shims in tests return undefined from Event.event();
      // only track the disposable when it is actually present so dispose()
      // does not hit an undefined entry.
      const sub = projectBoardService.onRateLimitState((state) => this.applyRateLimitState(state));
      if (sub) {
        this.disposables.push(sub);
      }
    }
    const initial = projectBoardService.getRateLimitState?.();
    if (initial) {
      this.applyRateLimitState(initial);
    }

    this.subscribeToTreeUpdates();

    // #360 — start/stop the background auto-refresh timer when autonomous mode
    // flips. updateAutoRefresh() consults AutonomousActivityState, so simply
    // re-running it on every transition is enough. Guarded like onRateLimitState
    // above: some vscode test shims return undefined from Event.event().
    const autonomousSub = AutonomousActivityState.instance.onDidChange(() =>
      this.updateAutoRefresh()
    );
    if (autonomousSub) {
      this.disposables.push(autonomousSub);
    }
  }

  /**
   * Adjust auto-refresh behavior based on the latest rate-limit reading.
   * Exhausted → pause until resetAt. Low → pause until resetAt. Healthy →
   * clear any prior pause so the next tick refreshes normally.
   */
  private applyRateLimitState(state: { exhausted: boolean; low: boolean; resetAt: number }): void {
    if (state.exhausted || state.low) {
      // +5s buffer past reset to avoid racing the server clock.
      this.autoRefreshPausedUntilMs = state.resetAt * 1000 + 5000;
    } else {
      this.autoRefreshPausedUntilMs = 0;
    }
  }

  private subscribeToTreeUpdates(): void {
    const ipc = IpcClient.getInstance();
    const eventName = `tree.${this.tabId}.update`;
    const sub = ipc.on(eventName, (_data) => {
      this.fetchId++;
      this.isLoading = false;
      this._onDidChangeTreeData.fire();
    });
    this.disposables.push(sub);
  }

  /**
   * Get the current tab ID
   */
  getTabId(): TabId {
    return this.tabId;
  }

  /**
   * Get the status value for this tab (used for API queries)
   */
  getStatus(): string {
    return this.tabConfig.status;
  }

  /**
   * Get the display label for this tab
   */
  getLabel(): string {
    return this.tabConfig.label;
  }

  /**
   * Get the count of items currently in the view
   */
  getItemCount(): number {
    return this.cachedItems.length;
  }

  /**
   * Set the TreeView reference for title updates (Issue #306)
   *
   * This enables the provider to update the view's title property
   * to show item counts dynamically.
   */
  setTreeView(treeView: vscode.TreeView<BaseTreeItem>): void {
    this.treeView = treeView;
    this.updateViewTitle();
  }

  /**
   * Get the TreeView reference (Issue #394)
   *
   * Used to determine which view is visible when detecting
   * the active batch-enabled provider.
   */
  getTreeView(): vscode.TreeView<BaseTreeItem> | undefined {
    return this.treeView;
  }

  /**
   * Get the underlying ProjectBoardService instance.
   *
   * Exposed for refresh commands that need direct cache management
   * (e.g., prefetching before firing tree data change events).
   */
  getProjectBoardService(): IWorkItemProvider {
    return this.projectBoardService;
  }

  /**
   * Update the TreeView title to show item count (Issue #306)
   *
   * Called whenever the tree data changes to keep the count in sync.
   * Format: "Status (N)" where N is the count of items.
   */
  updateViewTitle(): void {
    if (!this.treeView) {
      return;
    }

    const count = this.getItemCount();
    if (this.hasActiveFilters() && this.totalUnfilteredCount > count) {
      this.treeView.title = `${this.tabConfig.label} (${count}/${this.totalUnfilteredCount})`;
    } else {
      this.treeView.title = `${this.tabConfig.label} (${count})`;
    }
  }

  /**
   * Eagerly update the view title count from the service cache.
   *
   * VSCode only calls getChildren() on expanded views, so collapsed views
   * keep stale counts in their titles. This method queries the service
   * directly (using the warm cache from prefetchAllItems) and updates the
   * title without waiting for getChildren().
   */
  async refreshTitleCount(): Promise<void> {
    if (!this.treeView) {
      return;
    }

    try {
      const issues = await withCallSource(`board-title:${this.tabId}`, () =>
        this.projectBoardService.getIssuesByStatus(
          this.tabConfig.status,
          this.sortBy,
          this.sortDirection
        )
      );
      this.treeView.title = `${this.tabConfig.label} (${issues.length})`;
    } catch {
      // Don't update title if fetch fails — keep existing count
    }
  }

  /**
   * Load settings from ConfigBridge (6-tier merged configuration)
   *
   * @see Issue #476 - Refactor to use ConfigBridge instead of direct VSCode reads
   */
  private loadSettings(): void {
    // Load ready items settings via ConfigBridge
    const readyItemsSettings = getReadyItemsSettings();
    this.autoRefreshEnabled = readyItemsSettings.autoRefresh;
    this.autoRefreshInterval = readyItemsSettings.refreshInterval * 1000;
    this.sortBy = readyItemsSettings.sortBy;
    this.sortDirection = readyItemsSettings.sortDirection;

    // Load filter settings
    this.filterPriority = readyItemsSettings.filters.priority as FilterPriority;
    this.filterSize = readyItemsSettings.filters.size as FilterSize;
    this.filterComponent = readyItemsSettings.filters.component as FilterComponent;

    // Load search text
    this.searchText = readyItemsSettings.searchText;

    // Load hide blocked filter (Issue #822)
    this.hideBlocked = readyItemsSettings.filters.hideBlocked;

    // Load show dependencies setting
    this.showDependencies = readyItemsSettings.showDependencies;

    // Load epic grouping settings via ConfigBridge
    const projectBoardSettings = getProjectBoardSettings();
    this.groupByEpic = projectBoardSettings.groupByEpic;
    this.defaultEpicCollapsed = projectBoardSettings.defaultEpicCollapsed;

    this.updateAutoRefresh();
  }

  /**
   * Watch for configuration changes via ConfigBridge.
   *
   * Uses ConfigBridge.onConfigChanged instead of vscode.workspace.onDidChangeConfiguration
   * to align with the 6-tier config system.
   *
   * Only refreshes when one of the settings this provider actually consumes
   * changes (sort, filters, search text, hide-blocked, show-dependencies,
   * epic grouping, auto-refresh interval). Config events that touch unrelated
   * keys (e.g. `autonomous.enabled_repos` toggled from the Repositories
   * checkbox view) are no-ops here so a single repo-toggle doesn't cascade
   * a cache-clear + re-fetch through every status tab. Issues #476, #3432.
   *
   * @see Issue #476  - Refactor to use ConfigBridge instead of direct VSCode reads
   * @see Issue #3432 - Single repo toggle must not cascade refreshes elsewhere
   */
  private watchSettings(): void {
    const configBridge = ConfigBridge.getInstance();

    const disposable = configBridge.onConfigChanged(async () => {
      // Snapshot the values this provider actually depends on so we can
      // detect whether the incoming event affects us at all. Without this
      // the provider used to clear its cache and re-fetch on every config
      // change — including unrelated `autonomous.enabled_repos` writes —
      // turning a single checkbox toggle into 4 status-tab refreshes.
      const before = {
        autoRefreshEnabled: this.autoRefreshEnabled,
        autoRefreshInterval: this.autoRefreshInterval,
        sortBy: this.sortBy,
        sortDirection: this.sortDirection,
        filterPriority: this.filterPriority,
        filterSize: this.filterSize,
        filterComponent: this.filterComponent,
        searchText: this.searchText,
        hideBlocked: this.hideBlocked,
        showDependencies: this.showDependencies,
        groupByEpic: this.groupByEpic,
        defaultEpicCollapsed: this.defaultEpicCollapsed,
      };
      this.loadSettings();
      const changed =
        before.autoRefreshEnabled !== this.autoRefreshEnabled ||
        before.autoRefreshInterval !== this.autoRefreshInterval ||
        before.sortBy !== this.sortBy ||
        before.sortDirection !== this.sortDirection ||
        before.filterPriority !== this.filterPriority ||
        before.filterSize !== this.filterSize ||
        before.filterComponent !== this.filterComponent ||
        before.searchText !== this.searchText ||
        before.hideBlocked !== this.hideBlocked ||
        before.showDependencies !== this.showDependencies ||
        before.groupByEpic !== this.groupByEpic ||
        before.defaultEpicCollapsed !== this.defaultEpicCollapsed;
      const identityChanged =
        (await this.projectBoardService.reloadConfigIfChanged?.()) ?? false;
      if (changed || identityChanged) {
        this.refresh();
      }
    });
    this.disposables.push(disposable);
  }

  /**
   * Update auto-refresh timer based on settings
   */
  private updateAutoRefresh(): void {
    if (this.autoRefreshTimer) {
      clearInterval(this.autoRefreshTimer);
      this.autoRefreshTimer = null;
    }

    // #360 — demand-driven fetch policy: never run the background auto-refresh
    // timer while autonomous mode is off. With the scheduler idle there is no
    // autonomous work to keep the board warm for, and N windows × M tabs each
    // polling independently is exactly the traffic issue #360 eliminates. The
    // board still refreshes on activation, view expand, manual refresh, and
    // SSE status events. When autonomous flips on, the constructor's
    // subscription re-invokes this and the timer starts.
    if (
      this.autoRefreshEnabled &&
      this.autoRefreshInterval > 0 &&
      AutonomousActivityState.instance.isActive()
    ) {
      this.autoRefreshTimer = setInterval(() => {
        // Skip this tick if the GitHub rate-limit tracker told us to hold
        // off until the quota window resets. The pause naturally clears
        // itself the first time this check runs past autoRefreshPausedUntilMs.
        if (this.autoRefreshPausedUntilMs > 0 && Date.now() < this.autoRefreshPausedUntilMs) {
          return;
        }
        if (this.autoRefreshPausedUntilMs > 0) {
          this.autoRefreshPausedUntilMs = 0;
        }
        // The actual fetch (fetchIssuesByStatus) self-labels its GitHub calls
        // via withCallSource, so we no longer set the leaky global here — it was
        // never restored and mislabeled later calls (#360).
        this.refresh();
      }, this.autoRefreshInterval);
    }
  }

  /**
   * Get tree item for VS Code
   */
  getTreeItem(element: BaseTreeItem): vscode.TreeItem {
    return element;
  }

  /**
   * Get children for a tree item
   */
  async getChildren(element?: BaseTreeItem): Promise<BaseTreeItem[]> {
    if (element) {
      // If element has children (e.g., ReadyIssueTreeItem with dependencies),
      // return them
      return element.getChildren();
    }

    // Root level - fetch and return issues for this status
    return this.fetchIssuesByStatus();
  }

  /**
   * Check if an issue matches all active filters
   */
  private matchesFilters(issue: ReadyIssue): boolean {
    // Priority filter
    if (!matchesPriorityFilter(issue.priority, this.filterPriority)) {
      return false;
    }

    // Size filter
    if (!matchesSizeFilter(issue.size, this.filterSize)) {
      return false;
    }

    // Component filter
    if (!matchesComponentFilter(issue.labels, this.filterComponent)) {
      return false;
    }

    // Search text filter
    if (!matchesSearchText(issue.title, issue.number, this.searchText)) {
      return false;
    }

    // Hide blocked filter (Issue #822)
    if (this.hideBlocked && isBlocked(issue)) {
      return false;
    }

    return true;
  }

  /**
   * Get filtered count information for display
   */
  getFilteredCount(): { shown: number; total: number } {
    // This requires the unfiltered count which we track during fetch
    return {
      shown: this.cachedItems.length,
      total: this.totalUnfilteredCount,
    };
  }

  private totalUnfilteredCount: number = 0;

  /**
   * Check if filters or search are currently active
   */
  hasActiveFilters(): boolean {
    return (
      hasActiveFilters({
        priority: this.filterPriority,
        size: this.filterSize,
        component: this.filterComponent,
      }) ||
      this.hideBlocked ||
      this.hasActiveSearch()
    );
  }

  /**
   * Check if search is currently active
   */
  hasActiveSearch(): boolean {
    return Boolean(this.searchText && this.searchText.trim() !== "");
  }

  /**
   * Get the current search text
   */
  getSearchText(): string {
    return this.searchText;
  }

  /**
   * Fetch issues from the project board for this status
   */
  private async fetchIssuesByStatus(): Promise<BaseTreeItem[]> {
    if (this.isLoading) {
      return [new ProjectBoardActionItem("Loading...", "loading~spin")];
    }

    // Capture fetchId before going async. If refresh() is called mid-fetch
    // (e.g., an explicit refresh or a workspace reload), fetchId increments
    // and we discard this result to avoid rendering stale data.
    const myFetchId = this.fetchId;
    this.isLoading = true;
    this.lastError = null;

    try {
      // #360 — tag the GitHub calls with a real caller label so a storm names
      // its origin (`src=board-tab:<tab>`) instead of `src=unknown`.
      const issues = await withCallSource(`board-tab:${this.tabId}`, () =>
        this.projectBoardService.getIssuesByStatus(
          this.tabConfig.status,
          this.sortBy,
          this.sortDirection
        )
      );

      // Discard result if a newer refresh() was called while we were fetching
      if (myFetchId !== this.fetchId) {
        return [new ProjectBoardActionItem("Loading...", "loading~spin")];
      }

      // Track total count before filtering
      this.totalUnfilteredCount = issues.length;

      // Apply filters
      const filteredIssues = issues.filter((issue) => this.matchesFilters(issue));

      if (filteredIssues.length === 0) {
        this.cachedItems = [];
        this.cachedEpicGroups = [];
        this.updateViewTitle();

        // Different message based on whether filters are active
        if (this.hasActiveFilters() && issues.length > 0) {
          return [
            new ProjectBoardActionItem(
              `No issues match filters (${issues.length} hidden)`,
              "filter",
              {
                command: "nightgauge.filterProjectBoard",
                title: "Modify Filters",
              }
            ),
          ];
        }

        return [
          new ProjectBoardActionItem(
            `No ${this.tabConfig.label.toLowerCase()} issues found`,
            "info",
            {
              command: "nightgauge.refreshProjectBoard",
              title: "Refresh",
            }
          ),
        ];
      }

      // Build common options for tree items
      const treeItemOptions = {
        showDependencies: this.showDependencies,
        enableCheckbox: this.multiSelectEnabled,
      };

      // Check if epic grouping is enabled
      if (this.groupByEpic) {
        // Build epic metadata from per-status caches + current tab data.
        // This avoids the expensive getAllItems() call (537 items, 11s+).
        const epicMetadata = this.projectBoardService.getEpicMetadataFromCache(filteredIssues);
        const { groups: epicGroups } = groupIssuesByEpic(filteredIssues, epicMetadata);

        // Create epic group tree items
        this.cachedEpicGroups = epicGroups.map(
          (group) =>
            new EpicGroupTreeItem(group.epic, group.issues, {
              showDependencies: this.showDependencies,
              defaultCollapsed: this.defaultEpicCollapsed,
              enableCheckbox: this.multiSelectEnabled,
              selectedIssueNumbers: this.selectedIssueNumbers,
            })
        );

        // Build display array from epic groups (epic trees already show all sub-issues)
        const displayItems: BaseTreeItem[] = [...this.cachedEpicGroups];

        // Cache flat items for getItemCount()
        const displayedIssues = epicGroups.flatMap((g) => g.issues);
        this.cachedItems = displayedIssues.map(
          (issue) =>
            new ReadyIssueTreeItem(issue, {
              ...treeItemOptions,
              checked: this.selectedIssueNumbers.has(issue.number),
            })
        );

        // Check for empty state AFTER grouping (Issue #516)
        // If no groups AND no epic issues, show empty state
        if (displayItems.length === 0) {
          this.updateViewTitle();
          return [
            new ProjectBoardActionItem(
              `No ${this.tabConfig.label.toLowerCase()} issues found`,
              "info",
              {
                command: "nightgauge.refreshProjectBoard",
                title: "Refresh",
              }
            ),
          ];
        }

        this.updateViewTitle();
        return displayItems;
      }

      // Flat list (no epic grouping)
      this.cachedItems = filteredIssues.map((issue) => {
        const item = new ReadyIssueTreeItem(issue, {
          ...treeItemOptions,
          checked: this.selectedIssueNumbers.has(issue.number),
        });
        // Add backtrack indicator when this is the active pipeline issue (Issue #1349)
        if (this._activePipelineIssueNumber === issue.number && this._activeBacktrackCount > 0) {
          item.description = `↩${this._activeBacktrackCount} ${item.description ?? ""}`.trim();
          item.iconPath = new vscode.ThemeIcon(
            "debug-restart",
            new vscode.ThemeColor("editorWarning.foreground")
          );
        }
        return item;
      });
      this.cachedEpicGroups = [];
      this.updateViewTitle();
      return this.cachedItems;
    } catch (error) {
      // Discard error if a newer refresh() was called while we were fetching —
      // the new fetch will display the correct state.
      if (myFetchId !== this.fetchId) {
        return [new ProjectBoardActionItem("Loading...", "loading~spin")];
      }

      this.lastError = error instanceof Error ? error.message : "Unknown error";

      return [
        new ProjectBoardActionItem(`Error: ${this.lastError}`, "error"),
        new ProjectBoardActionItem("Click to retry", "refresh", {
          command: "nightgauge.refreshProjectBoard",
          title: `Refresh ${this.tabConfig.label}`,
        }),
      ];
    } finally {
      this.isLoading = false;
    }
  }

  /**
   * Get the current epic grouping setting
   */
  isGroupByEpicEnabled(): boolean {
    return this.groupByEpic;
  }

  /**
   * Set epic grouping enabled/disabled and refresh
   */
  setGroupByEpic(enabled: boolean): void {
    this.groupByEpic = enabled;
    this.refresh();
  }

  /**
   * Get cached epic groups (for expand/collapse all commands)
   */
  getCachedEpicGroups(): EpicGroupTreeItem[] {
    return [...this.cachedEpicGroups];
  }

  /**
   * Enter loading state and fire tree change event to show spinner.
   * Call this before starting an async data fetch so the user sees
   * immediate visual feedback when they click refresh.
   */
  setLoading(loading: boolean): void {
    this.isLoading = loading;
    this._onDidChangeTreeData.fire();
  }

  /**
   * Refresh the tree view display without clearing cache.
   * Use this for filter changes and other client-side-only updates.
   * The cached data is re-filtered/re-sorted without hitting the API.
   */
  refreshDisplay(): void {
    this._onDidChangeTreeData.fire();
  }

  /**
   * Refresh the tree view with full cache invalidation.
   * Clears the shared cache so all views get fresh data from the next API call.
   *
   * Resets isLoading and increments fetchId before firing the event:
   * - isLoading reset ensures the next getChildren() call starts a real fetch
   *   instead of returning "Loading..." from a stale in-progress flag.
   * - fetchId increment causes any in-flight fetchIssuesByStatus() to discard
   *   its result, preventing stale data from a pre-switch fetch from rendering.
   */
  refresh(): void {
    this.fetchId++;
    this.isLoading = false;
    this.projectBoardService.clearCache();
    this._onDidChangeTreeData.fire();
  }

  /**
   * Re-render the tree without clearing the shared cache.
   *
   * Use this after `ProjectBoardService.updateWorkspaceRoot()` has already
   * cleared all caches. Calling full `refresh()` from every provider would
   * null out `fetchAllItemsInFlight` N times, defeating the deduplication
   * and spawning N concurrent GitHub API calls instead of 1.
   */
  refreshView(): void {
    this.fetchId++;
    this.isLoading = false;
    this._onDidChangeTreeData.fire();
  }

  /**
   * Force refresh all data by clearing all caches.
   * Alias for refresh() — kept for API compatibility.
   */
  forceRefreshAll(): void {
    this.refresh();
  }

  /**
   * Debounced board refresh shared across all provider instances.
   * Coalesces rapid stage-complete events (4 providers × 1 event) into a
   * single `refreshProjectBoard` command — the same efficient parallel
   * pre-fetch path the manual refresh button uses.
   */
  private static debouncedStageRefresh(): void {
    if (ProjectBoardTreeProvider.stageRefreshTimer) {
      clearTimeout(ProjectBoardTreeProvider.stageRefreshTimer);
    }
    ProjectBoardTreeProvider.stageRefreshTimer = setTimeout(() => {
      ProjectBoardTreeProvider.stageRefreshTimer = null;
      void vscode.commands.executeCommand("nightgauge.refreshProjectBoard");
    }, ProjectBoardTreeProvider.STAGE_REFRESH_DEBOUNCE_MS);
  }

  /**
   * Set the sort order and refresh
   */
  setSortBy(sortBy: SortBy): void {
    this.sortBy = sortBy;
    this.refresh();
  }

  /**
   * Get the current sort order
   */
  getSortBy(): SortBy {
    return this.sortBy;
  }

  /**
   * Set the sort direction and refresh
   */
  setSortDirection(direction: SortDirection): void {
    this.sortDirection = direction;
    this.refresh();
  }

  /**
   * Set sort field and direction atomically — avoids double refresh vs
   * calling setSortBy() + setSortDirection() separately.
   */
  setSort(sortBy: SortBy, direction: SortDirection): void {
    this.sortBy = sortBy;
    this.sortDirection = direction;
    this.refresh();
  }

  /**
   * Get the current sort direction
   */
  getSortDirection(): SortDirection {
    return this.sortDirection;
  }

  /**
   * Get cached items (for commands that need access to items)
   */
  getCachedItems(): ReadyIssueTreeItem[] {
    return [...this.cachedItems];
  }

  /**
   * Check if there are any issues in this status
   */
  hasItems(): boolean {
    return this.cachedItems.length > 0;
  }

  /**
   * Get the last error if any
   */
  getLastError(): string | null {
    return this.lastError;
  }

  // =====================
  // MULTI-SELECT METHODS (Issue #125)
  // =====================

  /**
   * Enable or disable multi-select mode for batch pipeline
   *
   * When enabled, tree items show checkboxes for selection.
   */
  setMultiSelectEnabled(enabled: boolean): void {
    this.multiSelectEnabled = enabled;
    if (!enabled) {
      // Clear selections when disabling
      this.selectedIssueNumbers.clear();
    }
    this.refreshDisplay();
  }

  /**
   * Check if multi-select mode is enabled
   */
  isMultiSelectEnabled(): boolean {
    return this.multiSelectEnabled;
  }

  /**
   * Toggle selection of an issue
   *
   * @param issueNumber - The issue number to toggle
   * @returns The new selection state
   */
  toggleIssueSelection(issueNumber: number): boolean {
    if (this.selectedIssueNumbers.has(issueNumber)) {
      this.selectedIssueNumbers.delete(issueNumber);
      return false;
    } else {
      this.selectedIssueNumbers.add(issueNumber);
      return true;
    }
  }

  /**
   * Set the selection state for an issue
   *
   * @param issueNumber - The issue number
   * @param selected - Whether to select (true) or deselect (false)
   */
  setIssueSelected(issueNumber: number, selected: boolean): void {
    if (selected) {
      this.selectedIssueNumbers.add(issueNumber);
    } else {
      this.selectedIssueNumbers.delete(issueNumber);
    }
  }

  /**
   * Check if an issue is selected
   */
  isIssueSelected(issueNumber: number): boolean {
    return this.selectedIssueNumbers.has(issueNumber);
  }

  /**
   * Get all selected issue numbers
   */
  getSelectedIssues(): number[] {
    return Array.from(this.selectedIssueNumbers);
  }

  /**
   * Get the count of selected issues
   */
  getSelectedCount(): number {
    return this.selectedIssueNumbers.size;
  }

  /**
   * Select all visible issues
   */
  selectAll(): void {
    for (const item of this.cachedItems) {
      this.selectedIssueNumbers.add(item.issueNumber);
    }
    this.refreshDisplay();
  }

  /**
   * Clear all selections
   */
  clearSelection(): void {
    this.selectedIssueNumbers.clear();
    this.refreshDisplay();
  }

  /**
   * Handle checkbox state change from VS Code tree view
   *
   * This method should be called when the tree view's onDidChangeCheckboxState fires.
   * The event items may include ReadyIssueTreeItem instances which have issueNumber.
   *
   * @param event - The checkbox state change event
   */
  handleCheckboxChange(event: vscode.TreeCheckboxChangeEvent<BaseTreeItem>): void {
    for (const [item, state] of event.items) {
      // Only process ReadyIssueTreeItem instances
      if ("issueNumber" in item && typeof item.issueNumber === "number") {
        const selected = state === vscode.TreeItemCheckboxState.Checked;
        this.setIssueSelected(item.issueNumber, selected);
      }
    }
  }

  // =====================
  // STATE SERVICE INTEGRATION (Issue #151)
  // =====================

  /**
   * Connect to PipelineStateService for auto-refresh on stage completion
   *
   * When connected, the provider subscribes to onStageComplete events
   * and automatically refreshes when issue-pickup completes (for 'ready' tab only).
   * This ensures the picked-up issue disappears from the Ready list immediately.
   *
   * @param stateService - The PipelineStateService singleton
   */
  setStateService(stateService: PipelineStateService): void {
    this.stateService = stateService;

    // Subscribe to stage complete events — debounce into a single
    // refreshProjectBoard command so all provider instances coalesce
    // into one efficient parallel pre-fetch (same path as the button).
    const stageCompleteDisposable = stateService.onStageComplete(({ stage }) => {
      if (
        stage === "issue-pickup" ||
        stage === "feature-planning" ||
        stage === "feature-dev" ||
        stage === "feature-validate" ||
        stage === "pr-create" ||
        stage === "pr-merge" ||
        stage === "pipeline-finish"
      ) {
        ProjectBoardTreeProvider.debouncedStageRefresh();
      }
    });

    // Cache pipeline state fields used by tree item decoration (Issue #1349)
    // No disk reads — data is pushed from the file-watcher event.
    const stateChangedDisposable = stateService.onStateChanged((state) => {
      this._activePipelineIssueNumber = state?.issue_number ?? null;
      this._activeBacktrackCount = state?.backtrack_count ?? 0;
    });

    // Refresh tree when a new backtrack is detected (Issue #1349)
    const backtrackDisposable = stateService.onBacktrackTriggered(() => {
      ProjectBoardTreeProvider.debouncedStageRefresh();
    });

    this.disposables.push(stageCompleteDisposable, stateChangedDisposable, backtrackDisposable);
  }

  /**
   * Dispose of resources
   */
  dispose(): void {
    if (this.autoRefreshTimer) {
      clearInterval(this.autoRefreshTimer);
    }
    if (ProjectBoardTreeProvider.stageRefreshTimer) {
      clearTimeout(ProjectBoardTreeProvider.stageRefreshTimer);
      ProjectBoardTreeProvider.stageRefreshTimer = null;
    }
    this._onDidChangeTreeData.dispose();
    for (const disposable of this.disposables) {
      disposable.dispose();
    }
  }
}
