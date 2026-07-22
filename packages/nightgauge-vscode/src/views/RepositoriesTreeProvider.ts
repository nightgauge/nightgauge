/**
 * RepositoriesTreeProvider - TreeDataProvider for the Repositories view
 *
 * Displays all repositories in the workspace with their issues, pipeline status,
 * and quick navigation. Part of the Multi-Repository Workspace Support epic.
 *
 * @see Issue #329 - Repositories Tree View
 * @see docs/MULTI_REPO_WORKSPACE.md - Multi-Repository Workspace Support
 */

import { exec } from "child_process";
import { promisify } from "util";
import * as vscode from "vscode";
import { BaseTreeItem } from "./items/BaseTreeItem";
import { RepositoryTreeItem } from "./items/RepositoryTreeItem";
import { IssueSummaryTreeItem } from "./items/IssueSummaryTreeItem";
import type { WorkspaceManager } from "../services/WorkspaceManager";
import type { Repository } from "../models/Repository";
import { ProjectBoardService, type ReadyIssue } from "../services/ProjectBoardService";
import type { IWorkItemProvider } from "../services/types/WorkItemProvider";
import { ReadyIssueTreeItem } from "./items/ReadyIssueTreeItem";
import { EpicGroupTreeItem, groupIssuesByEpic } from "./items/EpicGroupTreeItem";
import { getProjectBoardSettings } from "../config/projectBoardSettings";
import { IpcClient } from "../services/IpcClient";
import type { IpcQueueState } from "../services/IpcClientBase";
import { withCallSource } from "../services/callSource";
import { ConfigBridge } from "../services/ConfigBridge";
import { Logger } from "../utils/logger";
import type { SortBy, SortDirection } from "../services/ProjectBoardService";
import {
  type FilterPriority,
  type FilterSize,
  type FilterComponent,
  type FilterState,
  DEFAULT_FILTER_STATE,
  matchesPriorityFilter,
  matchesSizeFilter,
  matchesComponentFilter,
  matchesSearchText,
} from "../types/FilterConfig";
import { isBlocked } from "../utils/dependencyUtils";
import {
  isRepoEnabledForAutonomous,
  type EnabledReposConfigService,
} from "../utils/enabledReposConfig";
import type { SequentialRepoConfigService } from "../utils/sequentialRepoConfig";
import { resolveActiveRepository } from "../utils/resolveActiveRepository";
import { AutonomousActivityState } from "../utils/autonomousActivityState";

const execAsync = promisify(exec);

/**
 * Sorted-string equality check for two `enabled_repos` arrays. Used by the
 * self-write suppression guard (Issue #3051) — the on-disk YAML value may
 * arrive in a different order than the array we wrote, so compare on the
 * normalized representation.
 */
function arraysEqualSorted(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false;
  const sortedA = [...a].sort();
  const sortedB = [...b].sort();
  for (let i = 0; i < sortedA.length; i++) {
    if (sortedA[i] !== sortedB[i]) return false;
  }
  return true;
}

/** Extended filter state including searchText and hideBlocked */
interface RepoFilterState extends FilterState {
  searchText: string;
  hideBlocked: boolean;
}

const DEFAULT_SORT_BY: SortBy = "board";
const DEFAULT_SORT_DIRECTION: SortDirection = "asc";

const DEFAULT_REPO_FILTER_STATE: RepoFilterState = {
  ...DEFAULT_FILTER_STATE,
  searchText: "",
  hideBlocked: false,
};

/**
 * Action tree item for displaying messages/actions in the tree
 */
class RepositoriesActionItem extends BaseTreeItem {
  constructor(label: string, icon: string, command?: vscode.Command) {
    super(label, vscode.TreeItemCollapsibleState.None);
    this.setIcon(icon);
    if (command) {
      this.command = command;
    }
  }
}

/** Shape of a single config-coherence warning from the autonomous scheduler. */
interface ConfigWarningEntry {
  severity: string;
  kind: string;
  message: string;
}

/**
 * Tree item for a single config-coherence warning (child of ConfigWarningSectionItem).
 * Issue #3640.
 */
class ConfigWarningItem extends BaseTreeItem {
  constructor(warning: ConfigWarningEntry) {
    super(warning.message, vscode.TreeItemCollapsibleState.None);
    this.description = `[${warning.kind}]`;
    this.tooltip = `${warning.severity.toUpperCase()} — ${warning.message}`;
    if (warning.severity === "info") {
      this.setIconWithColor("info", new vscode.ThemeColor("notificationsInfoIcon.foreground"));
    } else {
      this.setIconWithColor("warning", new vscode.ThemeColor("problemsWarningIcon.foreground"));
    }
    this.contextValue = "config-warning";
  }
}

/**
 * Collapsible section header for config-coherence warnings. Shown at the top
 * of the Repositories tree when the autonomous scheduler reports warnings.
 * Issue #3640.
 */
class ConfigWarningSectionItem extends BaseTreeItem {
  constructor(warnings: ConfigWarningEntry[]) {
    super(`Config Warnings (${warnings.length})`, vscode.TreeItemCollapsibleState.Expanded);
    this.setIconWithColor("warning", new vscode.ThemeColor("problemsWarningIcon.foreground"));
    this.tooltip =
      "Autonomous scheduler config-coherence warnings. " +
      "Fix these to ensure all per-repo policies are applied as expected.";
    this.contextValue = "config-warnings-section";
    for (const w of warnings) {
      this.addChild(new ConfigWarningItem(w));
    }
  }
}

/**
 * RepositoriesTreeProvider - TreeDataProvider for workspace repositories
 *
 * @example
 * ```typescript
 * const workspaceManager = WorkspaceManager.getInstance(workspaceRoot);
 * await workspaceManager.initialize();
 *
 * const provider = new RepositoriesTreeProvider(workspaceManager);
 * const treeView = vscode.window.createTreeView('nightgauge.repositoriesView', {
 *   treeDataProvider: provider,
 *   showCollapseAll: true,
 * });
 * ```
 */
export class RepositoriesTreeProvider
  implements vscode.TreeDataProvider<BaseTreeItem>, vscode.Disposable
{
  private _onDidChangeTreeData = new vscode.EventEmitter<BaseTreeItem | undefined | void>();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;

  private workspaceManager: WorkspaceManager;
  private disposables: vscode.Disposable[] = [];
  private cachedRepositories: Map<string, RepositoryTreeItem> = new Map();
  /** One IWorkItemProvider per repo for live count fetching */
  private perRepoServices: Map<string, IWorkItemProvider> = new Map();
  /** Factory for creating per-repo providers (injected from bootstrap, falls back to ProjectBoardService) */
  private readonly createPerRepoProvider: (path: string) => IWorkItemProvider;
  private groupByEpic: boolean = true;
  private defaultEpicCollapsed: boolean = false;
  private logger = new Logger("RepositoriesTree");

  /** Per-repo, per-status sort state */
  private sortStateMap: Map<string, Map<string, { sortBy: SortBy; sortDirection: SortDirection }>> =
    new Map();

  /** Per-repo, per-status filter state (includes searchText, hideBlocked) */
  private filterStateMap: Map<string, Map<string, RepoFilterState>> = new Map();

  /** Cache of IssueSummaryTreeItem instances for targeted refresh */
  private issueSummaryCache: Map<string, Map<string, IssueSummaryTreeItem>> = new Map();

  // TreeView reference for updating title with counts (Issue #306)
  private treeView: vscode.TreeView<BaseTreeItem> | undefined;

  /** Debounce timer for coalescing rapid refreshAll() calls */
  private refreshDebounceTimer: NodeJS.Timeout | null = null;
  private static readonly REFRESH_DEBOUNCE_MS = 500;

  /** Debounce timer for queue.changed — coalesces burst dispatches into one refresh */
  private queueChangedDebounceTimer: NodeJS.Timeout | null = null;
  private static readonly QUEUE_CHANGED_DEBOUNCE_MS = 5000;

  /** Per-repo-status debounce timers for targeted refreshes */
  private targetedRefreshTimers: Map<string, NodeJS.Timeout> = new Map();

  /**
   * Last set of repo slugs seen in a queue.changed event. Used to scope
   * queue-driven refreshes to only the affected repos (the ones that had or
   * have a queue item) instead of re-fetching every repository on every
   * autonomous dispatch/dequeue. Issue #2984.
   */
  private lastQueueRepoSlugs: Set<string> = new Set();

  /**
   * Guard flag: true while a getChildren() fetch cycle is in progress.
   * Prevents onItemsUpdated → refreshAll() feedback loops.
   */
  private isFetching = false;

  /**
   * When false, automatic refresh triggers (IPC events, per-repo service
   * `onItemsUpdated` callbacks) are suppressed. Workspace/config changes and
   * explicit `refreshAll()` calls from commands still fire so the view
   * stays correct on structural changes. Primary motivation: let users
   * pause live refresh to conserve GitHub API quota.
   */
  private autoRefreshEnabled: boolean = true;

  /**
   * Digest of the last-seen merged config (Issue #360). The
   * `ConfigBridge.onConfigChanged` handler's Path 3 used to clear every
   * per-repo board cache and `refreshAll()` on EVERY reload event — including
   * no-op reloads produced by a watched config file being touched without any
   * content change. In a shared-project multi-repo workspace that turned each
   * such event into a cold-cache `board.list × 3` (one per status tab) plus a
   * rate-limit probe, i.e. the ~3s GitHub refresh storm. We now short-circuit
   * Path 3 when the merged config is byte-identical to the last one we acted on.
   */
  private lastConfigDigest: string | undefined;

  /** Cached `autonomous.enabled_repos` — refreshed when writing or when config changes fire. */
  private enabledRepos: string[] = [];

  /**
   * Cached config-coherence warnings from the autonomous scheduler, set by
   * `setConfigWarnings()`. When non-empty, a "Config Warnings" section is
   * prepended to the root of the tree. Issue #3640.
   */
  private cachedConfigWarnings: ConfigWarningEntry[] = [];

  /**
   * Per-repo current git branch (path → branch name). Populated by
   * `getCurrentBranch()` and consumed when constructing tree items.
   * Cleared on `refreshAll()` and `onWorkspaceChanged`. Issue #3051.
   */
  private branchCache: Map<string, string> = new Map();

  /**
   * Recent self-originated `enabled_repos` writes, with their expected
   * post-merge values and expiration timestamps. When a
   * `ConfigBridge.onConfigChanged` echo arrives whose merged
   * `enabled_repos` matches any non-expired entry, the listener treats it
   * as our own echo and skips the cascade.
   *
   * Each `writeEnabledReposSelf` call produces TWO echoes in production:
   *   1. A synchronous echo from our own `await ConfigBridge.reload()` in
   *      `handleCheckboxChange` (so the read-back is fresh and the
   *      checkbox doesn't bounce — #3429).
   *   2. A debounced echo ~100ms later from the runtime-store write
   *      flowing through `IncrediYamlService.handleFileChange()` →
   *      `ConfigBridge.handleConfigFileChange()` → 100ms debounced
   *      `reload()`.
   *
   * The previous counter scheme (`pendingSelfWriteCount += 1`) only
   * absorbed one of those two echoes; the second always landed with the
   * counter at 0 and `incoming === enabledRepos` (already synced),
   * fell into Path 3, and triggered a tree-wide `clearCache()` +
   * `refreshAll()` — visibly the "all spinners spin" cascade. Counting
   * by 2 would brick on debounce coalescing (3 rapid writes can produce
   * 3 sync echoes + 1 coalesced debounced echo, leaving the counter
   * stuck at +2 and absorbing real external changes for a while).
   *
   * Value+window suppression is robust to any echo count and any
   * coalescing pattern. Stale entries (older than 1s) are pruned on
   * every read so a manual YAML edit that happens to land on a recently
   * self-written value is suppressed only briefly — well past the 100ms
   * debounce window but short enough that adapter switches and other
   * external changes are still picked up promptly.
   *
   * Issues #3051, #3432, #3435.
   */
  private recentSelfWrites: Array<{ sortedValue: string[]; expiresAt: number }> = [];

  /** TTL for entries in `recentSelfWrites`. Generous margin past the
   *  100ms `FILE_WATCHER_DEBOUNCE_MS` to absorb slow event loops. */
  private static readonly SELF_WRITE_ECHO_TTL_MS = 1000;

  /**
   * Phase 3 of #3313 (#3336) — runtime-tier services injected from
   * `bootstrap/services.ts`. Optional so existing constructor-only test
   * sites still compile; when absent the read/write paths fall back to
   * safe no-op behavior (empty enabled_repos list, false sequential,
   * undefined max_concurrent).
   */
  private readonly sequentialRepoConfigService: SequentialRepoConfigService | null;
  private readonly enabledReposConfigService: EnabledReposConfigService | null;

  constructor(
    workspaceManager: WorkspaceManager,
    createPerRepoProvider?: (path: string) => IWorkItemProvider,
    services?: {
      sequentialRepoConfigService?: SequentialRepoConfigService | null;
      enabledReposConfigService?: EnabledReposConfigService | null;
    }
  ) {
    this.workspaceManager = workspaceManager;
    this.createPerRepoProvider = createPerRepoProvider ?? ((path) => new ProjectBoardService(path));
    this.sequentialRepoConfigService = services?.sequentialRepoConfigService ?? null;
    this.enabledReposConfigService = services?.enabledReposConfigService ?? null;

    // Subscribe to workspace changes
    const workspaceDisposable = workspaceManager.onWorkspaceChanged(() => {
      this.cachedRepositories.clear();
      this.issueSummaryCache.clear();
      this.branchCache.clear();
      this.refreshAll();
    });
    this.disposables.push(workspaceDisposable);

    // Refresh the "active repo" icon when the user focuses a different
    // editor (the helper derives active repo from the active editor).
    // Guarded against tests that mock `vscode.window` without the event API.
    const onDidChange = vscode.window?.onDidChangeActiveTextEditor;
    if (typeof onDidChange === "function") {
      this.disposables.push(
        onDidChange(() => {
          if (!this.autoRefreshEnabled) return;
          this._onDidChangeTreeData.fire();
        })
      );
    }

    // Subscribe to IPC events for reactive updates
    this.subscribeToIpcEvents();

    // Subscribe to config changes (adapter switches, config edits) so the
    // Repositories view refreshes when project config changes.
    //
    // Cascade gating (Issues #3051, #3432, #3435):
    //   1. When the incoming `enabled_repos` matches a recent self-write
    //      (within the 1s TTL — covers both the synchronous `reload()`
    //      echo and the debounced 100ms `runtimeStore.onDidChange`-
    //      triggered echo), this is OUR write coming back. Sync the in-
    //      memory list and short-circuit. The toggling row was already
    //      repainted by `fireRepoRowRefresh()`.
    //   2. When `enabled_repos` actually changed (external edit, no
    //      matching self-write), update the in-memory list and fire one
    //      scoped row refresh per row whose checkbox state actually
    //      flipped. Do NOT clear per-repo `ProjectBoardService` caches
    //      (those caches are status-keyed, not allowlist-keyed) and do
    //      NOT call `refreshAll()` — both are wasteful for an allowlist-
    //      only change and were the visible "spinner cascade".
    //   3. When `enabled_repos` is unchanged AND no recent self-write is
    //      pending, this echo carries some other config change (adapter
    //      switch, other keys edited by hand). Clear caches and refresh
    //      the tree.
    const configDisposable = ConfigBridge.getInstance().onConfigChanged(() => {
      const incoming = this.readEnabledReposFromService();

      // Path 1 — self-write echo (matches by VALUE within a 1s window so
      // both the synchronous and debounced echoes are absorbed, and so 3
      // rapid writes whose debounced echoes coalesce into 1 are still
      // suppressed without leaving the suppression "owed" to future
      // external changes). #3435.
      if (this.matchesRecentSelfWrite(incoming)) {
        this.enabledRepos = incoming;
        return;
      }

      // Path 2 — `enabled_repos` actually changed (external edit). Update
      // the in-memory list and fire one scoped refresh per row whose
      // checkbox state flipped. No per-repo cache clears (those caches
      // are status-keyed, not allowlist-keyed) and no `refreshAll()`.
      const enabledChanged = !arraysEqualSorted(incoming, this.enabledRepos);
      if (enabledChanged) {
        const previous = this.enabledRepos;
        this.enabledRepos = incoming;
        for (const repo of this.workspaceManager.getAllRepositories()) {
          const wasEnabled = isRepoEnabledForAutonomous(repo.name, previous);
          const isEnabled = isRepoEnabledForAutonomous(repo.name, incoming);
          if (wasEnabled !== isEnabled) {
            this.fireRepoRowRefresh(repo.name);
          }
        }
        return;
      }

      // Path 3 — non-`enabled_repos` config change (adapter switch, other
      // config keys edited by hand). Clear caches and refresh the tree to
      // pick up the new state. Pre-#3432 behavior preserved here.
      //
      // #360 — but ONLY when the merged config actually changed. A watched
      // config file being touched (mtime bump, editor autosave, a Go-side
      // writer re-serializing identical content) fires a no-op reload whose
      // merged config is byte-identical to the last one. Without this guard
      // each such event nukes every per-repo board cache and re-fetches, i.e.
      // the ~3s `board.list × 3` GitHub refresh storm. Skip when unchanged.
      if (!this.mergedConfigChanged()) {
        return;
      }
      for (const service of this.perRepoServices.values()) {
        service.clearCache();
      }
      this.issueSummaryCache.clear();
      this.refreshAll();
    });
    this.disposables.push(configDisposable);

    // Establish the config-change baseline so the first no-op reload after
    // activation is recognised as unchanged (#360).
    this.mergedConfigChanged();

    // Re-render once when autonomous flips ON so the view catches up on any
    // board movement that happened while background polling was gated off
    // (#360). Turning OFF requires no action — demand-driven renders continue.
    // Guarded: some vscode test shims return undefined from Event.event().
    const autonomousSub = AutonomousActivityState.instance.onDidChange((active) => {
      if (active) this.refreshAll();
    });
    if (autonomousSub) {
      this.disposables.push(autonomousSub);
    }

    // Prime enabled_repos from disk so the initial render shows correct
    // checkbox state without waiting for a config event.
    this.reloadEnabledRepos();

    // Load epic grouping settings (constructor-time, matches ProjectBoardTreeProvider pattern)
    const settings = getProjectBoardSettings();
    this.groupByEpic = settings.groupByEpic;
    this.defaultEpicCollapsed = settings.defaultEpicCollapsed;
  }

  /**
   * Map status keys from service format (e.g. "in-progress") to the cache key
   * format used by issueSummaryCache (e.g. "inProgress").
   */
  private static statusToCacheKey(status: string): string {
    const map: Record<string, string> = {
      "in-progress": "inProgress",
      "in-review": "inReview",
    };
    return map[status] ?? status;
  }

  /**
   * Subscribe to both `onItemsUpdated` (scoped to this repo's row) and
   * `onStatusChanged` (targeted per-status refresh) for a service.
   * Requires repoName so onItemsUpdated refreshes only that repo's tree item
   * instead of the entire tree.
   */
  private subscribeToServiceEvents(service: IWorkItemProvider, repoName: string): void {
    if (service.onItemsUpdated) {
      this.disposables.push(
        service.onItemsUpdated(() => {
          if (this.isFetching) return;
          if (!this.autoRefreshEnabled) return;
          // #360 — `onItemsUpdated` is a service-internal progressive-render
          // echo; treating it as a refresh trigger is background polling.
          // Suppress it when autonomous is off so an idle workspace makes no
          // background GitHub traffic. Demand renders (view expand, manual
          // refresh) and SSE status events (below) are unaffected.
          if (!AutonomousActivityState.instance.isActive()) return;
          this.refreshRepository(repoName);
        })
      );
    }
    if (service instanceof ProjectBoardService) {
      this.disposables.push(
        service.onStatusChanged(({ repoSlug, statuses }) => {
          if (this.isFetching) return;
          if (!this.autoRefreshEnabled) return;
          for (const status of statuses) {
            this.fireTargetedRefresh(repoSlug, RepositoriesTreeProvider.statusToCacheKey(status));
          }
        })
      );
    }
  }

  private subscribeToIpcEvents(): void {
    const ipc = IpcClient.getInstance();
    const subs = [
      ipc.on("ipc.ready", (_data) => {
        // ipc.ready fires once at connection — always refresh so the tree
        // renders initial board counts even when auto-refresh is paused.
        this.refreshAll();
      }),
      ipc.on("queue.changed", (data) => {
        if (!this.autoRefreshEnabled) return;
        // #360 — the queue only mutates while autonomous is dispatching, but
        // gate explicitly so a stray event can't drive board fetches when the
        // scheduler is off.
        if (!AutonomousActivityState.instance.isActive()) return;
        // Debounce queue.changed: autonomous dispatch/dequeue can fire 15–30
        // events/min. Without coalescing, each event triggers a GraphQL board
        // count query per affected repo, burning ~900–7200 calls/hour.
        // A 5s window coalesces burst dispatches into one refresh while still
        // reflecting queue state changes within a UI-acceptable delay.
        if (this.queueChangedDebounceTimer) clearTimeout(this.queueChangedDebounceTimer);
        this.queueChangedDebounceTimer = setTimeout(() => {
          this.queueChangedDebounceTimer = null;
          this.handleQueueChanged(data as IpcQueueState);
        }, RepositoriesTreeProvider.QUEUE_CHANGED_DEBOUNCE_MS);
      }),
    ];
    this.disposables.push(...subs);
  }

  /**
   * Provide one ProjectBoardService per repository for live count display.
   * Called from extension.ts after both WorkspaceManager and ProjectBoardService
   * are initialized. Each service instance is pre-pointed at its repo's path so
   * counts are fetched independently and cached per repo.
   */
  setProjectBoardServices(services: Map<string, IWorkItemProvider>): void {
    this.perRepoServices = services;

    // Subscribe to board data updates from each service so the tree refreshes
    // when issues change status (e.g. after pipeline completion or PR merge).
    // Guard: skip if we triggered the fetch ourselves (prevents feedback loop).
    for (const [repoName, service] of services.entries()) {
      this.subscribeToServiceEvents(service, repoName);
    }

    this.refreshAll();
  }

  /**
   * Set the TreeView reference for title updates
   */
  setTreeView(treeView: vscode.TreeView<BaseTreeItem>): void {
    this.treeView = treeView;
    this.updateViewTitle();
  }

  /**
   * Update the TreeView title to show repository count and shared project number.
   * When all repositories share a project (N:1 topology), appends `· Project #N`.
   */
  updateViewTitle(): void {
    if (!this.treeView) {
      return;
    }

    const count = this.workspaceManager.getRepositoryCount();
    const sharedProject = this.workspaceManager.getSharedProjectNumber();

    if (sharedProject) {
      this.treeView.title = `Repositories (${count}) · Project #${sharedProject}`;
    } else {
      this.treeView.title = `Repositories (${count})`;
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
      // Handle RepositoryTreeItem children (issue counts, pipeline status)
      if (element instanceof RepositoryTreeItem) {
        return this.getRepositoryChildren(element);
      }
      // Handle IssueSummaryTreeItem children (actual issues for that status)
      if (element instanceof IssueSummaryTreeItem) {
        return this.getIssueSummaryChildren(element);
      }
      // Other tree items don't have children
      return element.getChildren();
    }

    // Root level - return repositories
    return this.getRootChildren();
  }

  /**
   * Get the parent of a tree item
   */
  getParent(element: BaseTreeItem): vscode.ProviderResult<BaseTreeItem> {
    // IssueSummaryTreeItem has repository as parent
    if (element instanceof IssueSummaryTreeItem) {
      // Find the parent repository
      for (const [, repoItem] of this.cachedRepositories) {
        const children = repoItem.getChildren();
        if (children.includes(element)) {
          return repoItem;
        }
      }
    }
    return undefined;
  }

  /**
   * Get root level children (repositories)
   */
  private async getRootChildren(): Promise<BaseTreeItem[]> {
    // Check if workspace manager is initialized
    if (!this.workspaceManager.isInitialized()) {
      return [new RepositoriesActionItem("Initializing...", "loading~spin")];
    }

    const repositories = this.workspaceManager.getAllRepositories();

    if (repositories.length === 0) {
      return [new RepositoriesActionItem("No repositories configured", "warning")];
    }

    // "Active" now means "the repo the active editor is inside" — derived
    // each render instead of stored on WorkspaceManager.
    const activeRepo = resolveActiveRepository(this.workspaceManager);

    // Resolve the current git branch for every repo in parallel before
    // building items so the first paint already shows branches. Uses
    // `execAsync` (never `execSync`) — this code runs on every refresh
    // including paths triggered by `onWorkspaceChanged` and extension
    // activation, where blocking calls froze every other extension for
    // minutes (Issue #1328 / project memory). Issue #3051.
    await Promise.all(
      repositories.map(async (repo) => {
        if (this.branchCache.has(repo.path)) return;
        const branch = await this.getCurrentBranch(repo.path);
        if (branch) {
          this.branchCache.set(repo.path, branch);
        }
      })
    );

    // Create repository tree items
    const items: RepositoryTreeItem[] = [];
    const reposDerived = this.workspaceManager.areReposDerivedFromProject();

    for (const repo of repositories) {
      const isActive = activeRepo?.name === repo.name;

      // Load config if not already loaded
      if (!repo.isConfigLoaded) {
        await repo.loadConfig();
      }

      // Checkbox matches against autonomous.enabled_repos by short folder
      // name (the same name the Go side expands via the top-level `owner`).
      // We deliberately don't gate on `repo.github?.repo` — many configs use
      // top-level `owner`/`repo` keys rather than a nested `github:` block,
      // which would leave `repo.github` undefined and suppress the checkbox
      // even though the repo is valid for scanning.
      const inAutonomousScan = isRepoEnabledForAutonomous(repo.name, this.enabledRepos);
      const isSequential = this.sequentialRepoConfigService?.readSequentialRepo(repo.name) ?? false;
      const maxConcurrent = this.sequentialRepoConfigService?.readMaxConcurrentRepo(repo.name);
      const currentBranch = this.branchCache.get(repo.path);

      const item = new RepositoryTreeItem(
        repo,
        isActive,
        inAutonomousScan,
        isSequential,
        maxConcurrent,
        currentBranch,
        reposDerived
      );
      this.cachedRepositories.set(repo.name, item);
      items.push(item);
    }

    this.updateViewTitle();

    const prefixItems: BaseTreeItem[] = [];

    // Prepend config-coherence warnings when present. Issue #3640.
    if (this.cachedConfigWarnings.length > 0) {
      prefixItems.push(new ConfigWarningSectionItem(this.cachedConfigWarnings));
    }

    // Informational banner when repos were derived from a shared project (N:1 topology).
    const sharedProject = this.workspaceManager.getSharedProjectNumber();
    if (sharedProject && this.workspaceManager.isMultiWorkspace()) {
      prefixItems.push(
        new RepositoriesActionItem(
          `Repos derived from Project #${sharedProject} · Edit manifest to customize`,
          "info"
        )
      );
    }

    if (prefixItems.length > 0) {
      return [...prefixItems, ...items];
    }
    return items;
  }

  /**
   * Resolve the current git branch for a working tree at `repoPath`. Returns
   * `(detached @<sha7>)` for detached HEADs and `undefined` on any error
   * (silent degrade — the row simply renders without a branch segment).
   *
   * Uses `execAsync` with a 5s timeout. NEVER use `execSync` here — this
   * runs on every tree refresh including startup paths (Issue #1328).
   * Issue #3051.
   */
  private async getCurrentBranch(repoPath: string): Promise<string | undefined> {
    try {
      const { stdout } = await execAsync("git branch --show-current", {
        cwd: repoPath,
        timeout: 5_000,
      });
      const branch = stdout.trim();
      if (branch) return branch;
      // Empty stdout → detached HEAD; resolve the short sha instead.
      const { stdout: shaOut } = await execAsync("git rev-parse --short HEAD", {
        cwd: repoPath,
        timeout: 5_000,
      });
      const sha = shaOut.trim();
      return sha ? `(detached @${sha})` : undefined;
    } catch {
      return undefined;
    }
  }

  /**
   * Lazily create per-repo ProjectBoardService instances if not already wired.
   * This handles the race condition where the tree renders before bootstrap
   * wires the services via setProjectBoardServices().
   */
  private ensurePerRepoServices(): void {
    if (this.perRepoServices.size > 0) return;

    const repos = this.workspaceManager.getAllRepositories();
    if (repos.length === 0) return;

    for (const repo of repos) {
      const service = this.createPerRepoProvider(repo.path);
      this.perRepoServices.set(repo.name, service);
      this.subscribeToServiceEvents(service, repo.name);
    }
    this.logger.info("Lazily created per-repo services", {
      repos: repos.map((r) => r.name),
    });
  }

  /**
   * Get children for an IssueSummaryTreeItem (actual issues for that status)
   */
  private async getIssueSummaryChildren(item: IssueSummaryTreeItem): Promise<BaseTreeItem[]> {
    // pipeline type has no expandable issues
    if (item.statusType === "pipeline") {
      return [];
    }

    this.ensurePerRepoServices();
    const service = this.perRepoServices.get(item.repoName);
    if (!service) {
      this.logger.warn("No service found for IssueSummaryTreeItem", {
        repoName: item.repoName,
      });
      return [new RepositoriesActionItem("Service not available", "warning")];
    }

    const statusKeyMap: Record<string, string> = {
      ready: "ready",
      inProgress: "in-progress",
      done: "done",
      backlog: "backlog",
    };
    const statusKey = statusKeyMap[item.statusType] ?? item.statusType;

    const { sortBy, sortDirection } = this.getSortForStatus(item.repoName, item.statusType);
    const filterState = this.getFilterForStatus(item.repoName, item.statusType);

    try {
      // #360 — label the drilldown fetch so its GitHub call is attributable.
      let issues = await withCallSource(`repositories:${item.repoName}:${statusKey}`, () =>
        service.getIssuesByStatus(statusKey, sortBy, sortDirection)
      );

      // Apply client-side filters (mirrors ProjectBoardTreeProvider.fetchIssuesByStatus)
      if (filterState.priority !== "all") {
        issues = issues.filter((i) => matchesPriorityFilter(i.priority, filterState.priority));
      }
      if (filterState.size !== "all") {
        issues = issues.filter((i) => matchesSizeFilter(i.size, filterState.size));
      }
      if (filterState.component !== "all") {
        issues = issues.filter((i) =>
          matchesComponentFilter(i.labels ?? [], filterState.component)
        );
      }
      if (filterState.searchText) {
        issues = issues.filter((i) => matchesSearchText(i.title, i.number, filterState.searchText));
      }
      if (filterState.hideBlocked) {
        issues = issues.filter((i) => !isBlocked(i));
      }

      // Update the parent item's label to reflect the filtered count.
      // When epic grouping is on, exclude type:epic issues from the count
      // since groupIssuesByEpic() skips them (they're group headers, not items).
      const displayCount = this.groupByEpic
        ? issues.filter((i) => !i.labels?.includes("type:epic")).length
        : issues.length;
      const statusLabel =
        item.statusType === "ready"
          ? "Ready"
          : item.statusType === "inProgress"
            ? "In Progress"
            : item.statusType === "backlog"
              ? "Backlog"
              : item.statusType === "done"
                ? "Done"
                : item.statusType;
      const issueWord = displayCount === 1 ? "issue" : "issues";
      item.label = `${statusLabel}: ${displayCount} ${issueWord}`;

      if (issues.length === 0) {
        return [new RepositoriesActionItem("No issues", "info")];
      }
      if (this.groupByEpic) {
        const epicMetadata = service.getEpicMetadataFromCache(issues);
        const { groups: epicGroups } = groupIssuesByEpic(issues, epicMetadata);
        if (epicGroups.length === 0) {
          return [new RepositoriesActionItem("No issues", "info")];
        }
        // getOwner is ProjectBoardService-specific; use instanceof guard so
        // alternative IWorkItemProvider implementations (e.g. CompositeAdapter)
        // degrade gracefully to undefined repoOwner.
        const owner =
          service instanceof ProjectBoardService ? (service.getOwner() ?? undefined) : undefined;
        return epicGroups.map(
          (group) =>
            new EpicGroupTreeItem(group.epic, group.issues, {
              defaultCollapsed: this.defaultEpicCollapsed,
              repoOwner: owner,
              repoName: item.repoName,
            })
        );
      }
      const repo = this.workspaceManager.getRepository(item.repoName);
      return issues.map(
        (issue) =>
          new ReadyIssueTreeItem(issue, {
            repoPath: repo?.path,
            repoName: item.repoName,
            enableCheckbox: item.statusType === "ready",
          })
      );
    } catch (err) {
      this.logger.warn("Failed to fetch issues for status node", {
        repoName: item.repoName,
        status: statusKey,
        error: String(err),
      });
      return [new RepositoriesActionItem("Failed to load issues", "error")];
    }
  }

  /**
   * Get children for a repository (issue counts, pipeline status)
   */
  private async getRepositoryChildren(repoItem: RepositoryTreeItem): Promise<BaseTreeItem[]> {
    const children: BaseTreeItem[] = [];

    let readyCount = 0;
    let inProgressCount = 0;
    let backlogCount = 0;

    const repoName = repoItem.repository.name;
    this.ensurePerRepoServices();
    const service = this.perRepoServices.get(repoName);
    if (service) {
      // Set isFetching guard to prevent onItemsUpdated → refreshAll() feedback loop
      this.isFetching = true;
      try {
        // Check whether any filters are active for any status in this repo.
        // When no filters: use lightweight boardCounts (1 API call, ~200 bytes).
        // When filters active: fetch full data and filter locally (Issue #2252).
        const hasActiveFilters = ["ready", "inProgress", "backlog"].some((st) => {
          const fs = this.getFilterForStatus(repoName, st);
          return (
            fs.priority !== "all" ||
            fs.size !== "all" ||
            fs.component !== "all" ||
            fs.searchText !== "" ||
            fs.hideBlocked ||
            this.groupByEpic // epic grouping excludes type:epic from count
          );
        });

        // #360 — tag every GitHub call this fetch makes with a real source so
        // the log names the caller (`src=repositories:<repo>`) instead of
        // `src=unknown`. Spans both the rate-limit probe and the board.list(s).
        await withCallSource(`repositories:${repoName}`, async () => {
          if (hasActiveFilters) {
            // Full data fetch needed for accurate filtered counts
            const applyFilters = (issues: ReadyIssue[], statusType: string): ReadyIssue[] => {
              const fs = this.getFilterForStatus(repoName, statusType);
              let filtered = issues;
              if (fs.priority !== "all") {
                filtered = filtered.filter((i) => matchesPriorityFilter(i.priority, fs.priority));
              }
              if (fs.size !== "all") {
                filtered = filtered.filter((i) => matchesSizeFilter(i.size, fs.size));
              }
              if (fs.component !== "all") {
                filtered = filtered.filter((i) =>
                  matchesComponentFilter(i.labels ?? [], fs.component)
                );
              }
              if (fs.searchText) {
                filtered = filtered.filter((i) =>
                  matchesSearchText(i.title, i.number, fs.searchText)
                );
              }
              if (fs.hideBlocked) {
                filtered = filtered.filter((i) => !isBlocked(i));
              }
              if (this.groupByEpic) {
                filtered = filtered.filter((i) => !i.labels?.includes("type:epic"));
              }
              return filtered;
            };

            const [readyIssues, inProgressIssues, backlogIssues] = await Promise.all([
              service.getIssuesByStatus("ready"),
              service.getIssuesByStatus("in-progress"),
              service.getIssuesByStatus("backlog"),
            ]);
            readyCount = applyFilters(readyIssues, "ready").length;
            inProgressCount = applyFilters(inProgressIssues, "inProgress").length;
            backlogCount = applyFilters(backlogIssues, "backlog").length;
          } else {
            // No filters active — use lightweight counts-only API (1 GraphQL call
            // with aliases returning only totalCount, ~200 bytes per repo).
            const counts = await service.getAggregatedStatusCounts();
            readyCount = counts["ready"] ?? 0;
            inProgressCount = counts["inProgress"] ?? 0;
            backlogCount = counts["backlog"] ?? 0;
          }
        });
      } catch (err) {
        this.logger.warn("Failed to fetch counts for repo", {
          repo: repoItem.repository.name,
          error: String(err),
        });
      } finally {
        this.isFetching = false;
      }
    } else {
      this.logger.warn("No ProjectBoardService found for repo", {
        repo: repoItem.repository.name,
        available: Array.from(this.perRepoServices.keys()),
      });
    }

    // Build sort label helpers for non-default sorts
    const getSortLabelForStatus = (statusType: string): string | undefined => {
      const { sortBy, sortDirection } = this.getSortForStatus(repoName, statusType);
      if (sortBy === DEFAULT_SORT_BY && sortDirection === DEFAULT_SORT_DIRECTION) {
        return undefined;
      }
      return `${sortBy} ${sortDirection}`;
    };

    const readyItem = new IssueSummaryTreeItem(
      "ready",
      repoName,
      readyCount,
      getSortLabelForStatus("ready")
    );
    const inProgressItem = new IssueSummaryTreeItem(
      "inProgress",
      repoName,
      inProgressCount,
      getSortLabelForStatus("inProgress")
    );
    const backlogItem = new IssueSummaryTreeItem(
      "backlog",
      repoName,
      backlogCount,
      getSortLabelForStatus("backlog")
    );

    // Cache for targeted refresh
    if (!this.issueSummaryCache.has(repoName)) {
      this.issueSummaryCache.set(repoName, new Map());
    }
    const repoCache = this.issueSummaryCache.get(repoName)!;
    repoCache.set("ready", readyItem);
    repoCache.set("inProgress", inProgressItem);
    repoCache.set("backlog", backlogItem);

    children.push(readyItem);
    children.push(inProgressItem);
    children.push(backlogItem);

    // Store children in the parent for getChildren() calls
    repoItem.clearChildren();
    for (const child of children) {
      repoItem.addChild(child);
    }

    return children;
  }

  /**
   * Get the sort state for a specific repo + status combination.
   * Returns the default (board/asc) if no custom sort is set.
   */
  getSortForStatus(
    repoName: string,
    statusType: string
  ): { sortBy: SortBy; sortDirection: SortDirection } {
    return (
      this.sortStateMap.get(repoName)?.get(statusType) ?? {
        sortBy: DEFAULT_SORT_BY,
        sortDirection: DEFAULT_SORT_DIRECTION,
      }
    );
  }

  /**
   * Set the sort state for a specific repo + status combination.
   * Fires a targeted tree data change for that IssueSummaryTreeItem.
   */
  setSortForStatus(
    repoName: string,
    statusType: string,
    sortBy: SortBy,
    sortDirection: SortDirection
  ): void {
    if (!this.sortStateMap.has(repoName)) {
      this.sortStateMap.set(repoName, new Map());
    }
    this.sortStateMap.get(repoName)!.set(statusType, { sortBy, sortDirection });
    this.fireTargetedRefresh(repoName, statusType);
  }

  /**
   * Get the filter state for a specific repo + status combination.
   * Returns the default (all/no-filter) if no custom filter is set.
   */
  getFilterForStatus(repoName: string, statusType: string): RepoFilterState {
    return (
      this.filterStateMap.get(repoName)?.get(statusType) ?? {
        ...DEFAULT_REPO_FILTER_STATE,
      }
    );
  }

  /**
   * Set the filter state for a specific repo + status combination.
   */
  setFilterForStatus(
    repoName: string,
    statusType: string,
    filterState: Partial<RepoFilterState>
  ): void {
    if (!this.filterStateMap.has(repoName)) {
      this.filterStateMap.set(repoName, new Map());
    }
    const existing = this.getFilterForStatus(repoName, statusType);
    this.filterStateMap.get(repoName)!.set(statusType, { ...existing, ...filterState });
    this.fireTargetedRefresh(repoName, statusType);
  }

  /**
   * Set the search text for a specific repo + status combination.
   */
  setSearchForStatus(repoName: string, statusType: string, searchText: string): void {
    this.setFilterForStatus(repoName, statusType, { searchText });
  }

  /**
   * Fire a targeted tree data change for a specific IssueSummaryTreeItem.
   * Debounced per repo+status to coalesce rapid back-to-back events.
   * Falls back to full refresh if the item is not cached.
   */
  fireTargetedRefresh(repoName: string, statusType: string): void {
    const key = `${repoName}:${statusType}`;
    const existing = this.targetedRefreshTimers.get(key);
    if (existing) clearTimeout(existing);
    this.targetedRefreshTimers.set(
      key,
      setTimeout(() => {
        this.targetedRefreshTimers.delete(key);
        const cached = this.issueSummaryCache.get(repoName)?.get(statusType);
        if (cached) {
          this._onDidChangeTreeData.fire(cached);
        } else {
          this._onDidChangeTreeData.fire();
        }
      }, RepositoriesTreeProvider.REFRESH_DEBOUNCE_MS)
    );
  }

  /**
   * Handle a `queue.changed` IPC event by refreshing only the repos whose
   * queue items actually changed (Issue #2984).
   *
   * Prior behavior: every queue.changed call triggered refreshAll(), which
   * re-fetched counts for every repo in the workspace — lighting up spinners
   * on unrelated repos on every autonomous dispatch.
   *
   * New behavior: diff the old and new sets of queue-owning repo slugs and
   * fire targeted refreshes only for repos in (previous ∪ new). Repos that
   * never had and still don't have queue items are untouched.
   *
   * Falls back to refreshAll() defensively when the payload is malformed.
   */
  private handleQueueChanged(state: IpcQueueState | undefined): void {
    if (!state || !Array.isArray(state.items)) {
      // Malformed payload — fall back to the safe (if noisy) global refresh
      // so the tree doesn't drift out of sync.
      this.refreshAll();
      return;
    }

    const newSlugs = new Set<string>();
    for (const item of state.items) {
      if (item && typeof item.repo === "string" && item.repo.length > 0) {
        newSlugs.add(item.repo);
      }
    }

    const affectedSlugs = new Set<string>([...this.lastQueueRepoSlugs, ...newSlugs]);
    this.lastQueueRepoSlugs = newSlugs;

    if (affectedSlugs.size === 0) {
      return; // Nothing in the queue now, nothing was before — no-op
    }

    // Queue items move through the board between these status buckets. Refresh
    // both so the counts on the affected repo cards stay consistent.
    const queueStatusKeys = ["ready", "inProgress"];
    for (const slug of affectedSlugs) {
      const repo = this.workspaceManager.findRepositoryByGitHub(slug);
      const repoName = repo?.name ?? slug;
      for (const statusKey of queueStatusKeys) {
        this.fireTargetedRefresh(repoName, statusKey);
      }
    }
  }

  /**
   * Refresh the entire tree (debounced).
   *
   * Multiple rapid-fire triggers (workspace change, config change, IPC ready,
   * onItemsUpdated) are coalesced into a single re-render. This prevents
   * cascading API calls when several events fire within the debounce window.
   */
  refreshAll(): void {
    if (this.refreshDebounceTimer) {
      clearTimeout(this.refreshDebounceTimer);
    }
    // Drop the branch cache so the next render re-resolves branches (rides
    // the existing refresh cadence — ~5s SLA per Issue #3051 AC).
    this.branchCache.clear();
    this.refreshDebounceTimer = setTimeout(() => {
      this.refreshDebounceTimer = null;
      this._onDidChangeTreeData.fire();
      this.updateViewTitle();
    }, RepositoriesTreeProvider.REFRESH_DEBOUNCE_MS);
  }

  /**
   * Invalidate a specific repo's ProjectBoardService cache and refresh.
   *
   * Unlike refreshAll() which only fires onDidChangeTreeData (causing a
   * re-render from cached data), this clears the affected repo's cache
   * first so the next getChildren() call fetches fresh data from GitHub.
   *
   * @param repoSlug - "owner/repo" slug (e.g. "acme/platform").
   *   Falls back to invalidating all caches if the slug doesn't match any service.
   * Issue #2340.
   */
  invalidateAndRefreshRepo(repoSlug?: string): void {
    if (repoSlug) {
      const repo = this.workspaceManager.findRepositoryByGitHub(repoSlug);
      const repoName = repo?.name ?? repoSlug;
      const service = this.perRepoServices.get(repoName);
      if (service) {
        service.clearCache();
        this.issueSummaryCache.delete(repoName);
        this.refreshRepository(repoName);
        return;
      }
    }
    // Fallback: no slug or slug not found — clear all caches
    for (const service of this.perRepoServices.values()) {
      service.clearCache();
    }
    this.refreshAll();
  }

  /**
   * Resolve the working-tree path for a repository by name. When `repoName` is
   * omitted (or unresolved), returns the active repository's path. Used by the
   * drag-and-drop controller to run cross-status `gh` mutations against the
   * correct repo in this multi-repo view. Returns undefined if nothing resolves.
   */
  getRepositoryPath(repoName?: string): string | undefined {
    if (repoName) {
      const repo = this.workspaceManager.getRepository(repoName);
      if (repo) {
        return repo.path;
      }
    }
    return resolveActiveRepository(this.workspaceManager)?.path;
  }

  /**
   * Refresh a specific repository item
   */
  refreshRepository(repoName: string): void {
    const item = this.cachedRepositories.get(repoName);
    if (item) {
      this._onDidChangeTreeData.fire(item);
    }
  }

  // ───────────────────────────────────────────────────────────────────────
  // Autonomous enabled_repos (per-repo checkbox) + auto-refresh toggle
  // ───────────────────────────────────────────────────────────────────────

  /** Current auto-refresh state for the Repositories view. */
  isAutoRefreshEnabled(): boolean {
    return this.autoRefreshEnabled;
  }

  /**
   * Toggle the auto-refresh gate. When off, IPC-driven and per-repo service
   * `onItemsUpdated`-driven refreshes are suppressed; workspace/config
   * changes and manual `refreshAll()` calls still fire. Returns the new
   * state so callers can update context keys / status bar.
   */
  setAutoRefreshEnabled(enabled: boolean): void {
    if (this.autoRefreshEnabled === enabled) return;
    this.autoRefreshEnabled = enabled;
    this.logger.info("Repositories view auto-refresh", { enabled });
    // When re-enabling, trigger one refresh so the view catches up on
    // whatever events were suppressed while paused.
    if (enabled) {
      this.refreshAll();
    }
  }

  /**
   * Read `autonomous.enabled_repos` through the runtime-tier service. When
   * no service is wired (test sites that construct without DI), fall back to
   * an empty list — meaning "scan all" per the helper's semantics.
   */
  private readEnabledReposFromService(): string[] {
    return this.enabledReposConfigService?.readEnabledRepos() ?? [];
  }

  /**
   * True when the merged effective config differs from the last one this
   * provider acted on, updating the stored digest as a side effect. Used to
   * suppress no-op config-reload storms in the `onConfigChanged` handler's
   * Path 3 (#360). A stable JSON serialization of `result.config` is a good
   * enough digest: the merge engine produces a deterministic object, so an
   * unchanged config serializes identically.
   */
  private mergedConfigChanged(): boolean {
    let digest: string;
    try {
      const merged = ConfigBridge.getInstance().getEffectiveConfig();
      digest = JSON.stringify(merged?.config ?? null);
    } catch {
      // If we can't read the config, treat it as changed so we don't
      // wrongly suppress a real update.
      this.lastConfigDigest = undefined;
      return true;
    }
    if (digest === this.lastConfigDigest) {
      return false;
    }
    this.lastConfigDigest = digest;
    return true;
  }

  /** Re-read autonomous.enabled_repos from the runtime tier. */
  private reloadEnabledRepos(): void {
    this.enabledRepos = this.readEnabledReposFromService();
  }

  /**
   * Write `enabled_repos` and record the post-merge value we expect to
   * see in echoes so the listener can suppress them. See `recentSelfWrites`
   * docs for why the counter approach was wrong and value-window
   * suppression is correct (#3435).
   *
   * The "expected post-merge value" is computed best-effort: when writing
   * a non-empty list, the runtime overlay always wins, so the merged
   * value equals what we wrote. When writing `[]` to delete the runtime
   * overlay, the merged view falls through to lower tiers — we record
   * what we read AFTER the write (post-`ConfigBridge.reload()`) so the
   * subsequent debounced echo carrying the same merged value still
   * matches. The caller in `handleCheckboxChange` invokes
   * `markRecentSelfWrite()` after the synchronous reload to capture that
   * post-merge view.
   */
  private async writeEnabledReposSelf(value: string[]): Promise<void> {
    if (this.enabledReposConfigService) {
      await this.enabledReposConfigService.writeEnabledRepos(value);
    }
  }

  /**
   * Record `value` as a recent self-write so subsequent `onConfigChanged`
   * echoes carrying that exact merged value are suppressed.
   *
   * Called from `handleCheckboxChange` and `setAllReposEnabledForAutonomous`
   * AFTER the synchronous `ConfigBridge.reload()` so the recorded value
   * reflects the actual post-merge view (handles the `[]`-to-delete +
   * lower-tier-fallback case correctly).
   */
  private markRecentSelfWrite(mergedValue: string[]): void {
    const sorted = [...mergedValue].sort();
    const now = Date.now();
    // Prune expired entries first so the array stays bounded.
    this.recentSelfWrites = this.recentSelfWrites.filter((w) => w.expiresAt > now);
    this.recentSelfWrites.push({
      sortedValue: sorted,
      expiresAt: now + RepositoriesTreeProvider.SELF_WRITE_ECHO_TTL_MS,
    });
  }

  /**
   * #3437 — diagnostic snapshot of every input that drives checkbox state.
   * Public so a debug command can dump it on demand.
   */
  dumpAutonomousState(): Record<string, unknown> {
    const bridge = ConfigBridge.getInstance();
    const merged = bridge.getEffectiveConfig();
    const mergedAutonomous = (
      merged?.config as { autonomous?: Record<string, unknown> } | undefined
    )?.autonomous;
    const mergedEnabled =
      (merged?.config as { autonomous?: { enabled_repos?: unknown } } | undefined)?.autonomous
        ?.enabled_repos ?? null;
    const sourceForKey = (path: string): string | undefined => {
      try {
        return (bridge as unknown as { getSource?: (p: string) => string }).getSource?.(path);
      } catch {
        return undefined;
      }
    };
    const cachedItems: Array<Record<string, unknown>> = [];
    for (const [name, item] of this.cachedRepositories) {
      const cb = (item as unknown as { checkboxState?: { state?: number } }).checkboxState;
      cachedItems.push({
        name,
        checkboxState: cb?.state,
        computedFromEnabledRepos: isRepoEnabledForAutonomous(name, this.enabledRepos),
      });
    }
    return {
      enabledRepos_inMemory: [...this.enabledRepos],
      enabledRepos_fromMergedConfig: mergedEnabled,
      enabledRepos_fromService: this.readEnabledReposFromService(),
      mergedAutonomous_fullSection: mergedAutonomous ?? null,
      mergedConfig_isNull: merged === null,
      mergedConfig_hasConfig: merged?.config !== undefined,
      mergedConfig_autonomousKeys: mergedAutonomous ? Object.keys(mergedAutonomous) : null,
      sourceTier_enabled_repos: sourceForKey("autonomous.enabled_repos"),
      sourceTier_autonomous: sourceForKey("autonomous"),
      mergedConfig_topLevelKeys: merged?.config ? Object.keys(merged.config as object) : null,
      recentSelfWrites: this.recentSelfWrites.map((w) => ({
        sortedValue: w.sortedValue,
        msUntilExpiry: w.expiresAt - Date.now(),
      })),
      workspaceRepos: this.workspaceManager.getAllRepositories().map((r) => r.name),
      cachedItems,
    };
  }

  /**
   * Update the cached config-coherence warnings from the autonomous scheduler.
   * When warnings change, fires a tree-data change so the root re-renders.
   * Pass an empty array or undefined to clear warnings. Issue #3640.
   */
  setConfigWarnings(
    warnings: { severity: string; kind: string; message: string }[] | undefined
  ): void {
    const next = warnings ?? [];
    const changed =
      next.length !== this.cachedConfigWarnings.length ||
      next.some(
        (w, i) =>
          w.kind !== this.cachedConfigWarnings[i]?.kind ||
          w.message !== this.cachedConfigWarnings[i]?.message
      );
    if (!changed) return;
    this.cachedConfigWarnings = next;
    this._onDidChangeTreeData.fire(undefined);
  }

  /**
   * Returns true if `incoming` matches any non-expired self-write entry.
   * Prunes expired entries as a side effect.
   */
  private matchesRecentSelfWrite(incoming: string[]): boolean {
    const now = Date.now();
    this.recentSelfWrites = this.recentSelfWrites.filter((w) => w.expiresAt > now);
    if (this.recentSelfWrites.length === 0) return false;
    const sortedIncoming = [...incoming].sort();
    return this.recentSelfWrites.some((w) => arraysEqualSorted(w.sortedValue, sortedIncoming));
  }

  /**
   * Update a single repository row's checkbox visual + emit a scoped refresh
   * for just that row. Used by checkbox-toggle paths to avoid firing a
   * tree-wide refresh (which previously cascaded loading spinners onto every
   * repository on every single-checkbox toggle — Issue #2988, mirrors the
   * scoped-refresh pattern from #2984).
   *
   * Mutates the cached RepositoryTreeItem's `checkboxState` in place and
   * fires `_onDidChangeTreeData.fire(item)` so VSCode re-renders only that
   * one row. The per-repo `ProjectBoardService` already caches counts, so
   * the re-fetch triggered by `getRepositoryChildren` is a cache hit (no
   * extra GitHub API calls).
   *
   * No-op when the repo isn't in the cache yet — the next root render will
   * pick up the new state from `enabledRepos` naturally.
   */
  private fireRepoRowRefresh(repoName: string): void {
    const item = this.cachedRepositories.get(repoName);
    if (!item) return;

    // Recompute the checkbox state from the freshly-reloaded enabledRepos
    // so the visual matches the on-disk source of truth.
    const inAutonomousScan = isRepoEnabledForAutonomous(repoName, this.enabledRepos);
    if (typeof item.checkboxState === "object" && item.checkboxState !== null) {
      item.checkboxState = {
        state: inAutonomousScan
          ? vscode.TreeItemCheckboxState.Checked
          : vscode.TreeItemCheckboxState.Unchecked,
        tooltip: inAutonomousScan
          ? "Uncheck to exclude this repo from autonomous board scans"
          : "Check to include this repo in autonomous board scans",
      };
    }

    this._onDidChangeTreeData.fire(item);
  }

  /**
   * Handle a checkbox state change from the TreeView's
   * `onDidChangeCheckboxState` event. The event batches multiple toggles
   * when the user multi-selects — we accumulate them then make a single
   * write to config.yaml.
   *
   * Semantics:
   *   - Compute the post-toggle set of enabled repos.
   *   - When every workspace repo is checked, write `[]` so the key is
   *     removed (scheduler reverts to default "scan all").
   *   - When nothing is checked, treat it as a reset to scan-all with a
   *     status message — scanning nothing is almost never intentional and
   *     we don't want the scheduler to silently go dark.
   *   - Otherwise write the selected short names.
   */
  async handleCheckboxChange(event: vscode.TreeCheckboxChangeEvent<BaseTreeItem>): Promise<void> {
    const changed: Array<{ repo: Repository; checked: boolean }> = [];
    for (const [item, state] of event.items) {
      if (!(item instanceof RepositoryTreeItem)) continue;
      changed.push({
        repo: item.repository,
        checked: state === vscode.TreeItemCheckboxState.Checked,
      });
    }
    if (changed.length === 0) return;

    // Build the new enabled list from the *current* workspace repo set + the
    // toggle deltas. This guarantees we don't lose repos that the user hasn't
    // touched, and handles the "all checked = scan all" reset cleanly.
    const workspaceRepos = this.workspaceManager.getAllRepositories();
    const nextChecked = new Map<string, boolean>();
    for (const r of workspaceRepos) {
      nextChecked.set(r.name, isRepoEnabledForAutonomous(r.name, this.enabledRepos));
    }
    for (const c of changed) {
      nextChecked.set(c.repo.name, c.checked);
    }

    const selectedShortNames = workspaceRepos
      .filter((r) => nextChecked.get(r.name))
      .map((r) => r.name);

    // Uncheck-all: reset to scan-all rather than silently scanning nothing.
    if (selectedShortNames.length === 0) {
      // Pre-mark the value we're writing so the SYNCHRONOUS echo from
      // ConfigBridge.reload() below is recognized as our own self-write
      // even though `recentSelfWrites` looks at the post-merge value.
      // For non-empty writes, runtime overlay always wins so toWrite ===
      // merged. For [] writes the merged view may fall through to a
      // lower tier — the post-reload mark below covers that case. #3435.
      this.markRecentSelfWrite([]);
      await this.writeEnabledReposSelf([]);
      // CRITICAL (#3429): force a synchronous ConfigBridge cache refresh
      // BEFORE reading enabledRepos. Without this the read sees the still-
      // stale cache (only updated by a debounced 100ms reload) and the
      // checkbox repaints as Checked, producing the visible "bounce".
      await ConfigBridge.getInstance().reload();
      this.reloadEnabledRepos();
      // Also mark the actual post-merge value so the debounced echo
      // ~100ms later (which carries the merged view, possibly differing
      // from the literal `[]` we wrote when lower tiers exist) is also
      // suppressed. #3435.
      this.markRecentSelfWrite(this.enabledRepos);
      // Scope the refresh to only the rows the user actually toggled —
      // `_onDidChangeTreeData.fire()` (no arg) used to cascade loading
      // spinners onto every repo in the workspace, which was the visible
      // symptom behind "checkbox didn't save" (Issue #2988).
      for (const c of changed) {
        this.fireRepoRowRefresh(c.repo.name);
      }
      // Push the new allowlist to a running scheduler so the change is
      // live (no Stop/Start required). Best-effort; safe when scheduler
      // is stopped/paused.
      void this.applyLiveAllowlistUpdate();
      vscode.window.showInformationMessage(
        "Autonomous scan set cleared — reverted to scan all workspace repos. (Uncheck-all is treated as a reset.)"
      );
      return;
    }

    // All workspace repos checked: remove enabled_repos key (scan-all default).
    const allSelected = selectedShortNames.length === workspaceRepos.length;
    const toWrite = allSelected ? [] : selectedShortNames;

    try {
      // Pre-mark the intended value so the synchronous reload() echo is
      // recognized as our own. See uncheck-all branch comment above. #3435.
      this.markRecentSelfWrite(toWrite);
      await this.writeEnabledReposSelf(toWrite);
      // CRITICAL (#3429): refresh ConfigBridge cache synchronously before
      // recomputing the row visuals — see uncheck-all branch above. The
      // debounced ~100ms file-watcher reload would otherwise repaint the
      // checkbox from stale cache and the user sees a "bounce".
      await ConfigBridge.getInstance().reload();
      this.reloadEnabledRepos();
      // Also mark the actual post-merge value (covers the [] → lower-
      // tier-fallback case that the pre-write mark can't predict). #3435.
      this.markRecentSelfWrite(this.enabledRepos);
      // Scope the refresh to only the rows the user actually toggled — see
      // the explanation above for the uncheck-all branch (Issue #2988).
      for (const c of changed) {
        this.fireRepoRowRefresh(c.repo.name);
      }

      // Live-apply the new allowlist to a running scheduler instead of
      // prompting the user with a blocking "Restart Autonomous?" modal
      // (#3429). The Go scheduler's FilterRepos accepts repeated
      // narrowing/widening calls without restart — the modal added
      // friction with zero benefit. Non-blocking toast confirms.
      const summary = allSelected
        ? "Autonomous will scan all workspace repos."
        : `Autonomous scoped to: ${selectedShortNames.join(", ")}.`;

      const liveResult = await this.applyLiveAllowlistUpdate();
      if (liveResult.applied) {
        // Brief, non-blocking status bar message — doesn't steal focus.
        vscode.window.setStatusBarMessage(`Autonomous allowlist updated — ${summary}`, 5000);
      } else {
        vscode.window.showInformationMessage(`${summary} Takes effect on next Start.`);
      }
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      this.logger.error("Failed to write autonomous.enabled_repos from checkbox", {
        error: msg,
      });
      vscode.window.showErrorMessage(`Failed to update autonomous scan set: ${msg}`);
      // Revert the tree view's checkbox visuals to the on-disk state.
      // Re-read from disk so `enabledRepos` matches whatever actually
      // persisted, then refresh just the rows we attempted to toggle.
      await ConfigBridge.getInstance().reload();
      this.reloadEnabledRepos();
      // Even on the error path, the runtime store may have partially
      // applied (or not) — record the post-merge value so the debounced
      // echo doesn't cascade. #3435.
      this.markRecentSelfWrite(this.enabledRepos);
      for (const c of changed) {
        this.fireRepoRowRefresh(c.repo.name);
      }
    }
  }

  /**
   * Live-apply the current `autonomous.enabled_repos` selection to a running
   * scheduler via the `autonomous.updateAllowlist` IPC method. Best-effort —
   * silently no-ops when no scheduler is running so the caller doesn't have
   * to branch on status.
   *
   * Returns `{ applied: true }` only when the IPC reported a non-stopped
   * scheduler picked up the change. The caller uses that to decide between
   * a transient "allowlist updated" status-bar message and a
   * "takes effect on next Start" toast.
   *
   * #3429 — replaces the previous "Restart Autonomous?" blocking modal.
   */
  private async applyLiveAllowlistUpdate(): Promise<{ applied: boolean }> {
    try {
      const ipc = IpcClient.getInstance();
      const status = await ipc.autonomousStatus();
      if (status.status === "stopped" || status.status === "complete") {
        return { applied: false };
      }
      // Build the workspaceRepos list in the same shape used by
      // autonomous.start / resume — fully-qualified "owner/repo" — so the
      // server's resolveAutonomousAllowlist intersects with the user's
      // YAML-tier enabled_repos identically.
      const workspaceFqdn: string[] = [];
      for (const r of this.workspaceManager.getAllRepositories()) {
        const gh = r.github;
        if (gh?.owner && gh?.repo) {
          workspaceFqdn.push(`${gh.owner}/${gh.repo}`);
        }
      }
      await ipc.autonomousUpdateAllowlist(workspaceFqdn);
      return { applied: true };
    } catch (err) {
      // Best-effort — don't block the checkbox UX on IPC failure. The
      // change is already persisted to runtime state and will take
      // effect on the next manual Start.
      const msg = err instanceof Error ? err.message : String(err);
      this.logger.warn("autonomous.updateAllowlist failed (non-fatal)", { error: msg });
      return { applied: false };
    }
  }

  /**
   * Bulk include or exclude every workspace repo in/from the autonomous
   * scan set. Used by the view-title "Include All Repos" / "Exclude All
   * Repos" buttons (Issue #2988).
   *
   * Semantics:
   *   - `include = true`  → write [] (scan-all default; clears the explicit
   *     allowlist so newly-added repos are picked up automatically too)
   *   - `include = false` → write a sentinel non-existent name. The Go
   *     scheduler treats `enabled_repos` as "exact-match" and has no
   *     intersection with the workspace, so all repos are excluded. We use
   *     a clearly synthetic value so it's obvious in config.yaml what
   *     happened. (We can't write [] for "exclude all" because [] means
   *     scan-all in this schema.)
   *
   * Returns the number of repos affected so the caller can show a clear
   * confirmation message.
   */
  async setAllReposEnabledForAutonomous(include: boolean): Promise<number> {
    const workspaceRepos = this.workspaceManager.getAllRepositories();
    if (workspaceRepos.length === 0) {
      vscode.window.showInformationMessage("No workspace repositories to update.");
      return 0;
    }

    try {
      const intended: string[] = include ? [] : ["__none__"];
      // Pre-mark so the synchronous reload echo is recognized. #3435.
      this.markRecentSelfWrite(intended);
      if (include) {
        // Scan-all default: removes the key entirely so future repos are
        // included automatically without further user action.
        await this.writeEnabledReposSelf([]);
      } else {
        // Sentinel value that intentionally matches no real repo. Names
        // beginning with `__` are reserved by config convention so a real
        // repo will never collide.
        await this.writeEnabledReposSelf(["__none__"]);
      }
      // CRITICAL (#3429): synchronous ConfigBridge cache refresh before
      // recomputing checkbox visuals — same bounce-prevention rationale as
      // the per-row handleCheckboxChange path.
      await ConfigBridge.getInstance().reload();
      this.reloadEnabledRepos();
      // Also mark the post-merge value (handles the [] → lower-tier
      // fallback case the pre-mark can't predict). #3435.
      this.markRecentSelfWrite(this.enabledRepos);

      // Live-apply to a running scheduler so the bulk include/exclude is
      // immediate (no Stop/Start required). Best-effort — see
      // applyLiveAllowlistUpdate for failure semantics.
      void this.applyLiveAllowlistUpdate();

      // Targeted refresh per row — never fall back to a tree-wide refresh
      // here (that's the bug this change is fixing — Issue #2988).
      for (const r of workspaceRepos) {
        this.fireRepoRowRefresh(r.name);
      }

      return workspaceRepos.length;
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      this.logger.error("Failed to bulk-update autonomous.enabled_repos", { error: msg });
      vscode.window.showErrorMessage(`Failed to update autonomous scan set: ${msg}`);
      return 0;
    }
  }

  /**
   * Write a per-repo concurrency cap to `concurrency.repository_overrides`
   * (machine tier). Used by the inline tree button to change the per-repo
   * cap without editing YAML by hand.
   *
   * `value` is the explicit cap. `undefined` means "as many as the workspace
   * allows" → resolves to `concurrency.workspace_max`. There is no separate
   * `sequential` flag anymore; a cap of 1 IS sequential.
   *
   * Triggers a tree refresh so the description suffix updates.
   *
   * Issue #2987; concurrency model #3781.
   */
  async setRepoMaxConcurrent(item: RepositoryTreeItem, value: number | undefined): Promise<void> {
    const repoName = item.repository.name;
    const service = this.sequentialRepoConfigService;
    if (!service) {
      this.logger.warn("Cannot setRepoMaxConcurrent: per-repo concurrency service not wired", {
        repoName,
      });
      return;
    }
    try {
      // `undefined` (the "Workspace max" preset) caps the repo at the
      // workspace-wide ceiling — the most a single repo can ever run.
      const cap = typeof value === "number" ? value : service.readWorkspaceMax();
      await service.writeRepoConcurrencyCap(repoName, cap);
      this._onDidChangeTreeData.fire();
      const label =
        cap === 1 ? "sequential (1 pipeline at a time)" : `up to ${cap} concurrent pipelines`;
      vscode.window.showInformationMessage(
        `${repoName}: per-repo concurrency set to ${label}. Takes effect on next scan cycle.`
      );
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      this.logger.error("Failed to write concurrency.repository_overrides", {
        error: msg,
      });
      vscode.window.showErrorMessage(`Failed to update concurrency cap for ${repoName}: ${msg}`);
    }
  }

  /**
   * Toggle this repo between sequential (cap 1) and concurrent (cap =
   * `concurrency.workspace_max`) by writing `concurrency.repository_overrides`.
   * Reads the current state from the tree item and writes the inverse.
   * Triggers a tree refresh so the context value and tooltip update.
   */
  async toggleSequentialRepo(item: RepositoryTreeItem): Promise<void> {
    const repoName = item.repository.name;
    const goSequential = !item.isSequential;
    const service = this.sequentialRepoConfigService;
    if (!service) {
      this.logger.warn("Cannot toggleSequentialRepo: per-repo concurrency service not wired", {
        repoName,
      });
      return;
    }
    try {
      const cap = goSequential ? 1 : service.readWorkspaceMax();
      await service.writeRepoConcurrencyCap(repoName, cap);
      this._onDidChangeTreeData.fire();
      const label = goSequential
        ? "sequential (1 pipeline at a time)"
        : `concurrent (up to ${cap})`;
      vscode.window.showInformationMessage(
        `${repoName}: per-repo concurrency set to ${label}. Takes effect on next scan cycle.`
      );
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      this.logger.error("Failed to write concurrency.repository_overrides", { error: msg });
      vscode.window.showErrorMessage(`Failed to update concurrency mode for ${repoName}: ${msg}`);
    }
  }

  /**
   * Get the current repository count
   */
  getRepositoryCount(): number {
    return this.workspaceManager.getRepositoryCount();
  }

  /**
   * Check if any repositories are available
   */
  hasRepositories(): boolean {
    return this.workspaceManager.getRepositoryCount() > 0;
  }

  /**
   * Get cached repository items
   */
  getCachedRepositories(): RepositoryTreeItem[] {
    return Array.from(this.cachedRepositories.values());
  }

  /**
   * Dispose of resources
   */
  dispose(): void {
    if (this.refreshDebounceTimer) {
      clearTimeout(this.refreshDebounceTimer);
    }
    for (const timer of this.targetedRefreshTimers.values()) {
      clearTimeout(timer);
    }
    this.targetedRefreshTimers.clear();
    this._onDidChangeTreeData.dispose();
    for (const disposable of this.disposables) {
      disposable.dispose();
    }
  }
}
