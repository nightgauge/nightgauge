/**
 * RefreshTriggerService - Watches the checkout's `.refresh-trigger` for refresh signals
 *
 * Enables CLI tools and hooks to trigger VSCode extension refresh by touching
 * `.refresh-trigger` in the checkout's per-checkout directory
 * (`.git/nightgauge-worktree/.refresh-trigger` for a main checkout, ADR-024 § 7).
 * When detected, all tree providers are refreshed to show updated GitHub
 * issues and project board state.
 *
 * @see Issue #308 - Add auto-refresh when GitHub issues are created via CLI
 */

import * as fs from "fs";
import * as vscode from "vscode";
import type { Logger } from "../utils/logger";
import { CHECKOUT_ENTRIES, checkoutDir, isUsableWorkspaceRoot } from "../utils/cloneLayout";

/**
 * Tree provider interface for refresh
 */
interface RefreshableTreeProvider {
  refresh(): void;
}

/**
 * RefreshTriggerService - Watches for refresh trigger file
 *
 * @example
 * ```typescript
 * const service = new RefreshTriggerService(workspaceRoot, logger);
 *
 * // Register tree providers to refresh
 * service.registerTreeProvider(readyItemsProvider);
 * service.registerTreeProvider(projectBoardProvider);
 * service.registerTreeProvider(pipelineProvider);
 *
 * // CLI script triggers refresh by touching <checkout dir>/.refresh-trigger
 * // Service automatically refreshes all registered providers
 * ```
 */
export class RefreshTriggerService implements vscode.Disposable {
  private watcher: vscode.FileSystemWatcher | null = null;
  private treeProviders: RefreshableTreeProvider[] = [];
  private debounceTimer: NodeJS.Timeout | null = null;
  private disposables: vscode.Disposable[] = [];

  /**
   * Debounce delay for file watcher events (matches PipelineStateService pattern)
   */
  private static readonly DEBOUNCE_MS = 100;

  constructor(
    private workspaceRoot: string,
    private logger: Logger
  ) {
    this.initializeWatcher();
  }

  /**
   * Initialize file system watcher for the checkout's .refresh-trigger file.
   * Skipped when the workspace root is not a usable checkout (unset, relative,
   * or not inside a git repository).
   */
  private initializeWatcher(): void {
    if (!isUsableWorkspaceRoot(this.workspaceRoot)) {
      this.logger.debug("RefreshTriggerService skipped: workspace root is not a git checkout", {
        workspaceRoot: this.workspaceRoot,
      });
      return;
    }
    try {
      const triggerFile = CHECKOUT_ENTRIES.refreshTrigger;
      const dir = checkoutDir(this.workspaceRoot);
      // The watcher needs its base directory to exist; the per-checkout
      // directory is inside the git directory, so creating it is harmless.
      fs.mkdirSync(dir, { recursive: true });

      // A RelativePattern over the per-checkout directory (outside the
      // workspace folders: files.exclude hides .git from workspace-wide
      // watchers and findFiles) watches only the trigger file.
      const pattern = new vscode.RelativePattern(vscode.Uri.file(dir), triggerFile);
      this.watcher = vscode.workspace.createFileSystemWatcher(pattern);

      // Use unified event handler for onCreate and onChange (delete not needed)
      // Follows NightgaugeYamlService pattern
      const handleTrigger = (uri: vscode.Uri) => this.handleRefreshTrigger(uri);
      this.watcher.onDidCreate(handleTrigger);
      this.watcher.onDidChange(handleTrigger);

      this.logger.debug("RefreshTriggerService initialized", {
        workspaceRoot: this.workspaceRoot,
        base: dir,
        pattern: triggerFile,
      });
    } catch (error) {
      // Graceful degradation: Log error but don't block extension activation
      this.logger.warn("Failed to initialize RefreshTriggerService", {
        error: error instanceof Error ? error.message : "Unknown error",
      });
    }
  }

  /**
   * Handle refresh trigger file change
   *
   * Debounces multiple rapid triggers to prevent excessive refreshes
   * (e.g., when scripts write multiple files in quick succession).
   */
  private handleRefreshTrigger(uri: vscode.Uri): void {
    this.logger.debug("Refresh trigger detected", { path: uri.fsPath });

    // Clear existing debounce timer
    if (this.debounceTimer) {
      clearTimeout(this.debounceTimer);
    }

    // Debounce: Wait 100ms before refreshing (matches PipelineStateService pattern)
    this.debounceTimer = setTimeout(() => {
      this.refreshAllProviders();
      this.debounceTimer = null;
    }, RefreshTriggerService.DEBOUNCE_MS);
  }

  /**
   * Refresh all registered tree providers
   *
   * Decision: Refresh all providers for simplicity and consistency
   * Issue operations can affect multiple views (Ready items, Project board, Pipeline)
   */
  private refreshAllProviders(): void {
    this.logger.debug("Refreshing all tree providers", {
      count: this.treeProviders.length,
    });

    for (const provider of this.treeProviders) {
      try {
        provider.refresh();
      } catch (error) {
        this.logger.warn("Failed to refresh tree provider", {
          error: error instanceof Error ? error.message : "Unknown error",
        });
      }
    }
  }

  /**
   * Register a tree provider to refresh on trigger
   *
   * @param provider - Tree provider with refresh() method
   */
  registerTreeProvider(provider: RefreshableTreeProvider): void {
    this.treeProviders.push(provider);
    this.logger.debug("Tree provider registered", {
      totalProviders: this.treeProviders.length,
    });
  }

  /**
   * Dispose watcher and cleanup timers
   */
  dispose(): void {
    if (this.watcher) {
      this.watcher.dispose();
      this.watcher = null;
    }

    if (this.debounceTimer) {
      clearTimeout(this.debounceTimer);
      this.debounceTimer = null;
    }

    for (const disposable of this.disposables) {
      disposable.dispose();
    }
  }
}
