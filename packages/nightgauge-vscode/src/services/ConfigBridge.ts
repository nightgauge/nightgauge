/**
 * ConfigBridge - Singleton service for accessing merged configuration
 *
 * Provides the resolved effective configuration from all 6 tiers:
 * defaults < global < project < local < env < cli
 *
 * This service bridges the gap between the 6-tier config merge engine and
 * VSCode extension services that previously called vscode.workspace.getConfiguration()
 * directly, bypassing the merge engine.
 *
 * @example
 * ```typescript
 * const configBridge = ConfigBridge.getInstance();
 * await configBridge.initialize(workspaceManager, workspaceRoot);
 *
 * // Get full merged config
 * const result = configBridge.getEffectiveConfig();
 *
 * // Get typed section
 * const pipeline = configBridge.getPipeline();
 * console.log(pipeline?.ci_timeout);
 *
 * // Check where a value came from
 * const source = configBridge.getSource('pipeline.ci_timeout');
 * // => 'project' | 'global' | 'default' | etc.
 *
 * // Subscribe to changes
 * configBridge.onConfigChanged((result) => {
 *   console.log('Config updated:', result.config);
 * });
 * ```
 *
 * @see Issue #473 - ConfigBridge service for unified config access
 * @see docs/CONFIGURATION.md - 6-tier config system documentation
 */

import * as vscode from "vscode";
import type { WorkspaceManager } from "./WorkspaceManager";
import { IncrediYamlService } from "../views/settings/IncrediYamlService";
import { type ConfigMergeResult, getValueAtPath } from "../config/configMergeEngine";
import type { ConfigSource } from "../config/schema";
import type { RuntimeStateStore } from "../config/RuntimeStateStore";
import type {
  IncrediConfig,
  ProjectConfig,
  PullRequestConfig,
  BranchConfig,
  IssueConfig,
  PipelineConfig,
  RoutingConfig,
  EnforcementConfig,
  CommandsConfig,
  ValidationConfig,
  SanitizationConfig,
  HumanInTheLoopConfig,
  RalphLoopConfig,
  AutomationsConfig,
  UIConfig,
  PlatformConfig,
  LmStudioConfig,
  ConfigValidationError,
} from "../config/schema";
import { resolvePlatformHostKey } from "../config/schema";

/** Payload emitted when the effective platform host key changes after reload(). */
export interface PlatformHostChangedEvent {
  previousHost: string;
  newHost: string;
}

/**
 * Debounce delay for file watcher events (matches IncrediYamlService)
 */
const FILE_WATCHER_DEBOUNCE_MS = 100;

/**
 * ConfigBridge - Singleton service for unified configuration access
 *
 * Responsibilities:
 * - Initialize with WorkspaceManager and workspace root
 * - Load effective config via IncrediYamlService.readEffective()
 * - Cache merged config with source annotations
 * - Watch config files for changes (primary, legacy, local)
 * - Fire onConfigChanged events on updates
 * - Provide typed section getters (getProject(), getPipeline(), etc.)
 * - Provide getSource(path) for provenance queries
 */
export class ConfigBridge implements vscode.Disposable {
  private static instance: ConfigBridge | null = null;

  private workspaceRoot: string | null = null;
  private workspaceManager: WorkspaceManager | null = null;
  private yamlService: IncrediYamlService | null = null;
  private runtimeStore: RuntimeStateStore | null = null;
  private disposables: vscode.Disposable[] = [];

  /** Cached merge result */
  private cachedResult: ConfigMergeResult | null = null;

  /** Whether initialization has completed */
  private _initialized = false;

  /** Debounce timer for file change events */
  private debounceTimer: ReturnType<typeof setTimeout> | null = null;

  // Event emitters
  private _onConfigChanged = new vscode.EventEmitter<ConfigMergeResult>();
  private _onValidationError = new vscode.EventEmitter<ConfigValidationError[]>();
  private readonly _onPlatformHostChanged = new vscode.EventEmitter<PlatformHostChangedEvent>();

  /**
   * Fired when configuration changes (file edit, repository switch, etc.)
   */
  readonly onConfigChanged = this._onConfigChanged.event;

  /**
   * Fired when configuration validation fails
   */
  readonly onValidationError = this._onValidationError.event;

  /**
   * Fired when the effective platform host key changes after reload().
   * Subscribers (SessionManager, TokenRefreshManager) use this to reset auth state.
   */
  readonly onPlatformHostChanged = this._onPlatformHostChanged.event;

  private constructor() {
    // Private constructor for singleton
  }

  /**
   * Get the singleton instance
   */
  static getInstance(): ConfigBridge {
    if (!ConfigBridge.instance) {
      ConfigBridge.instance = new ConfigBridge();
    }
    return ConfigBridge.instance;
  }

  /**
   * Reset the singleton instance (for testing)
   */
  static resetInstance(): void {
    if (ConfigBridge.instance) {
      ConfigBridge.instance.dispose();
      ConfigBridge.instance = null;
    }
  }

  /**
   * Initialize the ConfigBridge service
   *
   * Must be called after WorkspaceManager is initialized. Loads initial
   * configuration and sets up file watchers for changes.
   *
   * @param workspaceManager - The WorkspaceManager instance
   * @param workspaceRoot - The workspace root path
   */
  async initialize(
    workspaceManager: WorkspaceManager,
    workspaceRoot: string,
    runtimeStore?: RuntimeStateStore
  ): Promise<void> {
    if (this._initialized) {
      return;
    }

    this.workspaceManager = workspaceManager;
    this.workspaceRoot = workspaceRoot;
    this.runtimeStore = runtimeStore ?? null;

    // Create IncrediYamlService for file I/O (with runtime tier wired in)
    this.yamlService = new IncrediYamlService(workspaceRoot, runtimeStore);
    this.disposables.push(this.yamlService);

    // Subscribe to file changes from IncrediYamlService
    const fileChangeSubscription = this.yamlService.onDidChange(
      this.handleConfigFileChange.bind(this)
    );
    this.disposables.push(fileChangeSubscription);

    // The old `onRepositoryChanged` subscription was removed along with the
    // workspace-global current-repo pointer. ConfigBridge is now keyed to
    // whichever repo the workspace root points at; cross-repo callers pass
    // an explicit repo path when they need a different one.

    // Load initial configuration
    await this.reload();

    this._initialized = true;
  }

  /**
   * Check if initialization has completed
   */
  isInitialized(): boolean {
    return this._initialized;
  }

  /**
   * Reload configuration from all sources
   *
   * Called on initialization and when file changes are detected.
   * Uses IncrediYamlService.readEffective() which internally uses
   * the 6-tier merge engine.
   */
  async reload(): Promise<void> {
    if (!this.yamlService) {
      console.warn("[Nightgauge] ConfigBridge: Cannot reload - not initialized");
      return;
    }

    try {
      const oldHostKey = resolvePlatformHostKey(this.getPlatform());

      const result = await this.yamlService.readEffective();

      // Check for validation errors
      if (!result.validation.valid) {
        this._onValidationError.fire(result.validation.errors);
        console.warn(
          "[Nightgauge] ConfigBridge: Validation errors in config",
          result.validation.errors
        );
      }

      // Update cache
      this.cachedResult = result;

      // Fire change event
      this._onConfigChanged.fire(result);

      // Detect host key changes and notify subscribers (#3723)
      const newHostKey = resolvePlatformHostKey(this.getPlatform());
      if (oldHostKey !== newHostKey) {
        this._onPlatformHostChanged.fire({ previousHost: oldHostKey, newHost: newHostKey });
      }
    } catch (error) {
      console.error("[Nightgauge] ConfigBridge: Failed to load config", error);
      // Don't update cache on error - keep previous valid config
    }
  }

  /**
   * Handle config file change (debounced)
   *
   * Called when IncrediYamlService detects a file change.
   */
  private handleConfigFileChange(): void {
    // Debounce rapid changes
    if (this.debounceTimer) {
      clearTimeout(this.debounceTimer);
    }

    this.debounceTimer = setTimeout(async () => {
      await this.reload();
    }, FILE_WATCHER_DEBOUNCE_MS);
  }

  /**
   * Re-target the ConfigBridge at a different repository root.
   *
   * Previously called automatically by the onRepositoryChanged hook. With
   * the workspace-global current-repo pointer gone, callers that need to
   * follow a repo switch (e.g. a command that operates on a specific repo)
   * should call this directly. Invalidates cache and reloads configuration
   * for the new repository.
   */
  async retargetToRepository(repoPath: string): Promise<void> {
    // Update workspace root
    this.workspaceRoot = repoPath;

    // Recreate IncrediYamlService for the new repository
    if (this.yamlService) {
      // Find and remove old yaml service from disposables
      const idx = this.disposables.indexOf(this.yamlService);
      if (idx !== -1) {
        this.disposables.splice(idx, 1);
      }
      this.yamlService.dispose();
    }

    this.yamlService = new IncrediYamlService(repoPath, this.runtimeStore ?? undefined);
    this.disposables.push(this.yamlService);

    // Subscribe to file changes from new IncrediYamlService
    const fileChangeSubscription = this.yamlService.onDidChange(
      this.handleConfigFileChange.bind(this)
    );
    this.disposables.push(fileChangeSubscription);

    // Invalidate cache and reload
    this.cachedResult = null;
    await this.reload();
  }

  // ============================================================================
  // Core API
  // ============================================================================

  /**
   * Get the effective merged configuration
   *
   * Returns the cached ConfigMergeResult which includes the merged config,
   * source annotations, validation result, and tier metadata.
   *
   * @returns The full merge result, or null if not initialized
   */
  getEffectiveConfig(): ConfigMergeResult | null {
    return this.cachedResult;
  }

  /**
   * Get the source of a configuration value
   *
   * Returns which tier a specific config path came from.
   *
   * @param path - Dot-notation path to the value (e.g., 'pipeline.ci_timeout')
   * @returns The source tier, or 'default' if not found
   */
  getSource(path: string): ConfigSource {
    if (!this.cachedResult) {
      return "default";
    }
    return (this.cachedResult.sources[path] as ConfigSource) || "default";
  }

  /**
   * Get a configuration value by path
   *
   * @param path - Dot-notation path to the value
   * @returns The value at the path, or undefined if not found
   */
  getValue<T = unknown>(path: string): T | undefined {
    if (!this.cachedResult) {
      return undefined;
    }
    return getValueAtPath(this.cachedResult.config as Record<string, unknown>, path) as
      T | undefined;
  }

  // ============================================================================
  // Typed Section Getters
  // ============================================================================

  /**
   * Get project configuration section
   */
  getProject(): ProjectConfig | undefined {
    return this.cachedResult?.config.project;
  }

  /**
   * Get pull request configuration section
   *
   * Checks both 'pull_request' and 'pr' keys for compatibility.
   */
  getPullRequest(): PullRequestConfig | undefined {
    const config = this.cachedResult?.config;
    return config?.pull_request ?? config?.pr;
  }

  /**
   * Get branch configuration section
   */
  getBranch(): BranchConfig | undefined {
    return this.cachedResult?.config.branch;
  }

  /**
   * Get issue configuration section
   */
  getIssue(): IssueConfig | undefined {
    return this.cachedResult?.config.issue;
  }

  /**
   * Get pipeline configuration section
   */
  getPipeline(): PipelineConfig | undefined {
    return this.cachedResult?.config.pipeline;
  }

  /**
   * Get routing configuration section
   */
  getRouting(): RoutingConfig | undefined {
    return this.cachedResult?.config.routing;
  }

  /**
   * Get enforcement configuration section
   */
  getEnforcement(): EnforcementConfig | undefined {
    return this.cachedResult?.config.enforcement;
  }

  /**
   * Get commands configuration section
   */
  getCommands(): CommandsConfig | undefined {
    return this.cachedResult?.config.commands;
  }

  /**
   * Get validation configuration section
   */
  getValidation(): ValidationConfig | undefined {
    return this.cachedResult?.config.validation;
  }

  /**
   * Get sanitization configuration section
   */
  getSanitization(): SanitizationConfig | undefined {
    return this.cachedResult?.config.sanitization;
  }

  /**
   * Get human-in-the-loop configuration section
   */
  getHumanInTheLoop(): HumanInTheLoopConfig | undefined {
    return this.cachedResult?.config.human_in_the_loop;
  }

  /**
   * Get ralph loop configuration section
   */
  getRalphLoop(): RalphLoopConfig | undefined {
    return this.cachedResult?.config.ralph_loop;
  }

  /**
   * Get automations configuration section
   */
  getAutomations(): AutomationsConfig | undefined {
    return this.cachedResult?.config.automations;
  }

  /**
   * Get UI configuration section
   */
  getUI(): UIConfig | undefined {
    return this.cachedResult?.config.ui;
  }

  /**
   * Get platform cloud API configuration section (Issue #1458)
   *
   * @see Issue #1461 - Platform connection status indicator
   */
  getPlatform(): PlatformConfig | undefined {
    return this.cachedResult?.config.platform;
  }

  /**
   * Get LM Studio local inference configuration section
   *
   * @see Issue #2058 - LM Studio adapter and config contract
   */
  getLmStudio(): LmStudioConfig | undefined {
    return this.cachedResult?.config.lm_studio;
  }

  // ============================================================================
  // Lifecycle
  // ============================================================================

  /**
   * Dispose resources
   */
  dispose(): void {
    if (this.debounceTimer) {
      clearTimeout(this.debounceTimer);
    }

    this._onConfigChanged.dispose();
    this._onValidationError.dispose();
    this._onPlatformHostChanged.dispose();

    for (const disposable of this.disposables) {
      disposable.dispose();
    }
    this.disposables = [];

    this.cachedResult = null;
    this._initialized = false;
    this.workspaceManager = null;
    this.workspaceRoot = null;
    this.yamlService = null;
  }
}
