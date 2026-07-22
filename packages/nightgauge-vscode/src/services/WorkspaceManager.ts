/**
 * WorkspaceManager Service
 *
 * Singleton service that loads all repositories in the workspace and fires
 * events when the loaded set changes. Intentionally does NOT track a
 * mutable "current" repository — call sites that need "which repo is the
 * user looking at?" should use `resolveActiveRepository()` from
 * `utils/resolveActiveRepository.ts`, which derives it from the active
 * editor. See that file for the history of the removed pointer.
 *
 * @see Issue #324 - WorkspaceManager service and repository loading
 * @see docs/ARCHITECTURE.md - Multi-Repository Workspace Support (Phase 1)
 */

import * as vscode from "vscode";
import * as path from "node:path";
import { exec } from "node:child_process";
import { promisify } from "node:util";
import { Repository } from "../models/Repository";
import { detectWorkspaceType, loadWorkspaceConfig } from "../utils/workspaceDetection";
import { getRepoIdentity } from "../utils/configPathResolver";
import type { WorkspaceConfig, WorkspaceDetectionResult } from "../types/WorkspaceConfig";

const execAsync = promisify(exec);

/**
 * Workspace mode detected
 */
export type WorkspaceMode = "single" | "multi-workspace";

/**
 * WorkspaceManager - Singleton service for workspace state management
 *
 * Coordinates repository loading, switching, and state events. Uses lazy loading
 * with caching for optimal performance.
 *
 * @example
 * ```typescript
 * const manager = WorkspaceManager.getInstance(incrediRoot);
 * await manager.initialize();
 *
 * // Iterate over all loaded repositories
 * for (const repo of manager.getAllRepositories()) {
 *   await repo.loadConfig();
 * }
 *
 * // Figure out "which repo is the user looking at?"
 * const active = resolveActiveRepository(manager);
 * ```
 */
export class WorkspaceManager implements vscode.Disposable {
  private static instance: WorkspaceManager | null = null;

  private workspaceRoot: string;
  private disposables: vscode.Disposable[] = [];

  /** Workspace mode (single or multi-workspace) */
  private _mode: WorkspaceMode = "single";

  /** Loaded workspace configuration (null for single-repo mode) */
  private _workspaceConfig: WorkspaceConfig | null = null;

  /** Detection method used */
  private _detectionMethod: string = "single-repo";

  /** All loaded repositories */
  private _repositories: Map<string, Repository> = new Map();

  /** Whether initialization has completed */
  private _initialized: boolean = false;

  // Event emitters
  private _onWorkspaceChanged = new vscode.EventEmitter<Repository[]>();

  /**
   * Fired when the workspace repositories change
   * (e.g., on initialization or when workspace config is reloaded)
   */
  readonly onWorkspaceChanged = this._onWorkspaceChanged.event;

  private constructor(workspaceRoot: string, _workspaceState?: vscode.Memento) {
    // workspaceState arg preserved for call-site compatibility during the
    // removal of the persisted "last active repo" pointer — intentionally
    // unused. Safe to drop once all callers are updated.
    this.workspaceRoot = workspaceRoot;
  }

  /**
   * Get the singleton instance
   *
   * @param workspaceRoot - The workspace root path (git root)
   * @param workspaceState - Optional VSCode workspace state for persistence
   */
  static getInstance(workspaceRoot: string, workspaceState?: vscode.Memento): WorkspaceManager {
    if (!WorkspaceManager.instance) {
      WorkspaceManager.instance = new WorkspaceManager(workspaceRoot, workspaceState);
    }
    return WorkspaceManager.instance;
  }

  /**
   * Reset the singleton instance (for testing)
   */
  static resetInstance(): void {
    if (WorkspaceManager.instance) {
      WorkspaceManager.instance.dispose();
      WorkspaceManager.instance = null;
    }
  }

  /**
   * Initialize the workspace manager
   *
   * Detects workspace mode, loads repositories, and restores last active repository.
   * Must be called after getInstance() before using other methods.
   */
  async initialize(): Promise<void> {
    if (this._initialized) {
      return;
    }

    try {
      // Detect workspace type
      const detection = await detectWorkspaceType(this.workspaceRoot);
      this._mode = detection.type;
      this._workspaceConfig = detection.config;
      this._detectionMethod = detection.detection_method;

      // Load repositories based on mode
      await this.loadRepositories();

      this._initialized = true;
    } catch (error) {
      console.error(`[Nightgauge] WorkspaceManager initialization failed: ${error}`);
      // Initialize with single-repo fallback
      this._mode = "single";
      this._initialized = true;
    }

    this._onWorkspaceChanged.fire(this.getAllRepositories());
  }

  /**
   * Load repositories based on workspace mode
   */
  private async loadRepositories(): Promise<void> {
    this._repositories.clear();

    if (this._mode === "single") {
      // Single repository mode — auto-detect name from config, git, or folder
      const name = await this.resolveRepoName(this.workspaceRoot);
      const repo = new Repository(name, this.workspaceRoot);
      this._repositories.set(repo.name, repo);
    } else if (this._workspaceConfig) {
      // Multi-workspace mode with explicit config
      for (const repoConfig of this._workspaceConfig.repositories) {
        const repo = Repository.fromWorkspaceConfig(repoConfig, this.workspaceRoot);
        this._repositories.set(repo.name, repo);
      }

      // N:1 topology: when repos list is empty and shared_project_number is set,
      // derive the repo list from the GitHub project's linked repositories.
      const sharedProjectNumber = this._workspaceConfig.workspace.shared_project_number;
      if (sharedProjectNumber && this._repositories.size === 0) {
        const derived = await this.deriveReposFromProject(sharedProjectNumber);
        for (const repo of derived) {
          this._repositories.set(repo.name, repo);
        }
      }
    } else {
      // Auto-detected multi-workspace - create repositories from workspace folders
      const folders = vscode.workspace.workspaceFolders || [];
      for (const folder of folders) {
        const name = folder.name;
        const repo = new Repository(name, folder.uri.fsPath);
        this._repositories.set(name, repo);
      }
    }
  }

  /**
   * Derive the repository list from a GitHub ProjectV2's linked repositories.
   *
   * Calls `nightgauge workspace repos-from-project --project N --json` via
   * the Go binary. Repositories created this way are marked as derived (path
   * set to workspaceRoot — they have no local checkout path to map to until the
   * user opens them, so we use the workspace root as a stable fallback).
   *
   * Fails gracefully: on error, logs a warning and returns an empty list so the
   * extension degrades to "No repositories configured" rather than crashing.
   */
  private async deriveReposFromProject(projectNumber: number): Promise<Repository[]> {
    try {
      const { stdout } = await execAsync(
        `nightgauge workspace repos-from-project --project ${projectNumber} --json`,
        { cwd: this.workspaceRoot, timeout: 15_000 }
      );
      const parsed = JSON.parse(stdout.trim()) as Array<{ name: string; owner: string }>;
      return parsed.map((r) => {
        const repo = new Repository(r.name, this.workspaceRoot);
        // Mark this repo as derived from the project so tree items can annotate it.
        repo._workspaceProjectNumber = projectNumber;
        return repo;
      });
    } catch (err) {
      console.warn(
        `[Nightgauge] WorkspaceManager: failed to derive repos from project #${projectNumber}: ${err}. ` +
          `Add repositories manually to .vscode/nightgauge-workspace.yaml or ensure the Go binary is installed.`
      );
      return [];
    }
  }

  /**
   * Returns the shared project number from the workspace config when all
   * repositories use the same project, or undefined otherwise.
   * Used by RepositoriesTreeProvider to annotate the view title.
   */
  getSharedProjectNumber(): number | undefined {
    return this._workspaceConfig?.workspace?.shared_project_number;
  }

  /**
   * Returns true when the repository list was derived from the GitHub project
   * (N:1 auto-derivation path) rather than from an explicit manifest list.
   * Used by RepositoriesTreeProvider to annotate individual repo tree items.
   */
  areReposDerivedFromProject(): boolean {
    return (
      !!this._workspaceConfig?.workspace?.shared_project_number &&
      this._workspaceConfig.repositories.length === 0
    );
  }

  /**
   * Resolve the repository name for single-repo mode.
   *
   * Priority:
   * 1. .nightgauge/config.yaml → repo field (last path segment)
   * 2. vscode.workspace.workspaceFolders[0].name
   * 3. git remote origin URL (last path segment, strip .git)
   * 4. path.basename(root) as last resort
   */
  private async resolveRepoName(root: string): Promise<string> {
    // 1. Try .nightgauge/config.yaml (uses getRepoIdentity which
    //    also falls back to git remote internally)
    try {
      const identity = await getRepoIdentity(root);
      if (identity?.repo) {
        return identity.repo;
      }
    } catch {
      // Config read failed — continue to fallbacks
    }

    // 2. Try VS Code workspace folder name
    try {
      const folder = vscode.workspace.getWorkspaceFolder(vscode.Uri.file(root));
      if (folder?.name) {
        return folder.name;
      }
    } catch {
      // getWorkspaceFolder may not be available in all contexts
    }

    // 3. Last resort — directory basename
    const basename = path.basename(root);
    console.warn(
      `[Nightgauge] Could not detect repo name from config or git; falling back to directory name: ${basename}`
    );
    return basename;
  }

  /**
   * Detect workspace mode
   *
   * Delegates to workspaceDetection utility.
   *
   * @returns Current workspace mode ('single' or 'multi-workspace')
   */
  detectWorkspaceMode(): WorkspaceMode {
    return this._mode;
  }

  /**
   * Get a repository by name
   *
   * @param name - Repository name
   * @returns Repository instance or undefined if not found
   */
  getRepository(name: string): Repository | undefined {
    return this._repositories.get(name);
  }

  /**
   * Get all loaded repositories
   *
   * @returns Array of all Repository instances
   */
  getAllRepositories(): Repository[] {
    return Array.from(this._repositories.values());
  }

  /**
   * Get repository names
   *
   * @returns Array of repository names
   */
  getRepositoryNames(): string[] {
    return Array.from(this._repositories.keys());
  }

  /**
   * Get the number of repositories
   */
  getRepositoryCount(): number {
    return this._repositories.size;
  }

  /**
   * Find a repository by its GitHub "owner/repo" identity.
   *
   * Tries a fast name-based lookup first (repo portion of "owner/repo"),
   * then falls back to iterating all repos and matching their loaded
   * GitHub config. Returns undefined if no match is found.
   *
   * @see Issue #2245 - Cross-repo pipeline worktree creation
   */
  findRepositoryByGitHub(ownerSlashRepo: string): Repository | undefined {
    // Fast path: match by short name (e.g., "acme-mobile")
    const shortName = ownerSlashRepo.includes("/") ? ownerSlashRepo.split("/")[1] : ownerSlashRepo;
    const byName = this._repositories.get(shortName);
    if (byName) return byName;

    // Fast path retry: case-insensitive (GitHub API may return mixed case)
    const shortNameLower = shortName.toLowerCase();
    for (const [key, r] of this._repositories.entries()) {
      if (key.toLowerCase() === shortNameLower) return r;
    }

    // Slow path: iterate and match by github config (case-insensitive)
    const [owner, repo] = ownerSlashRepo.split("/");
    const ownerLower = owner?.toLowerCase();
    const repoLower = repo?.toLowerCase();
    for (const r of this._repositories.values()) {
      const gh = r.github;
      if (gh && gh.owner?.toLowerCase() === ownerLower && gh.repo?.toLowerCase() === repoLower) {
        return r;
      }
    }
    return undefined;
  }

  /**
   * Check if workspace is in multi-repository mode
   */
  isMultiWorkspace(): boolean {
    return this._mode === "multi-workspace";
  }

  /**
   * Get the workspace configuration (null for single-repo mode)
   */
  getWorkspaceConfig(): WorkspaceConfig | null {
    return this._workspaceConfig;
  }

  /**
   * Get the detection method used
   */
  getDetectionMethod(): string {
    return this._detectionMethod;
  }

  /**
   * Check if initialization has completed
   */
  isInitialized(): boolean {
    return this._initialized;
  }

  /**
   * Reload workspace configuration
   *
   * Re-detects workspace mode and reloads all repositories.
   * Useful when workspace configuration files change.
   */
  async reload(): Promise<void> {
    this._initialized = false;
    await this.initialize();
  }

  /**
   * Get workspace root path
   */
  getWorkspaceRoot(): string {
    return this.workspaceRoot;
  }

  /**
   * Dispose resources
   */
  dispose(): void {
    this._onWorkspaceChanged.dispose();
    for (const disposable of this.disposables) {
      disposable.dispose();
    }
  }
}
