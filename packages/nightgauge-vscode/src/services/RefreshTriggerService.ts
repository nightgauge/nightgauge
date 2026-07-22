/**
 * RefreshTriggerService - Watches .nightgauge/.refresh-trigger for refresh signals
 *
 * Enables CLI tools and hooks to trigger VSCode extension refresh by touching
 * the .nightgauge/.refresh-trigger file. When detected, all tree providers are
 * refreshed to show updated GitHub issues and project board state.
 *
 * @see Issue #308 - Add auto-refresh when GitHub issues are created via CLI
 */

import * as vscode from "vscode";
import type { Logger } from "../utils/logger";

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
 * // CLI script triggers refresh by touching .nightgauge/.refresh-trigger
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
    if (workspaceRoot) {
      this.initializeWatcher();
    }
  }

  /**
   * Initialize file system watcher for .refresh-trigger file
   */
  private initializeWatcher(): void {
    try {
      const triggerFile = ".nightgauge/.refresh-trigger";

      // Use RelativePattern to watch only .nightgauge/.refresh-trigger
      // This prevents false positives from workspace-wide file changes
      const pattern = new vscode.RelativePattern(this.workspaceRoot, triggerFile);
      this.watcher = vscode.workspace.createFileSystemWatcher(pattern);

      // Use unified event handler for onCreate and onChange (delete not needed)
      // Follows IncrediYamlService pattern
      const handleTrigger = (uri: vscode.Uri) => this.handleRefreshTrigger(uri);
      this.watcher.onDidCreate(handleTrigger);
      this.watcher.onDidChange(handleTrigger);

      this.logger.debug("RefreshTriggerService initialized", {
        workspaceRoot: this.workspaceRoot,
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
