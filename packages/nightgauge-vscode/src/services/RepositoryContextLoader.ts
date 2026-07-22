/**
 * RepositoryContextLoader Service
 *
 * Singleton service that provides repository-scoped context paths for pipeline
 * operations. Ensures that context files, CLAUDE.md, docs/, and standards/ are
 * loaded from the correct repository in multi-repo workspace environments.
 *
 * @see Issue #327 - Repository-scoped context loading and pipeline isolation
 * @see docs/ARCHITECTURE.md - Multi-Repository Workspace Support
 */

import * as vscode from "vscode";
import * as fs from "fs/promises";
import * as path from "path";
import type { WorkspaceManager } from "./WorkspaceManager";
import type { Repository } from "../models/Repository";
import { resolveActiveRepository } from "../utils/resolveActiveRepository";

/**
 * Context file types supported by the pipeline
 */
export type ContextFileType =
  | "issue"
  | "planning"
  | "dev"
  | "validate"
  | "pr"
  | "feedback"
  | "state"
  | "batch-state"
  | "running"
  | "batch"
  | "planning-batch"
  | "dev-batch";

/**
 * Configuration precedence for merged config loading
 */
export interface ContextPrecedence {
  /** Effective config source: 'repository', 'workspace', or 'default' */
  source: "repository" | "workspace" | "default";
  /** Path to the config file used (if any) */
  configPath?: string;
}

/**
 * Result of loading a documentation file
 */
export interface DocFileResult {
  /** Whether the file was found */
  found: boolean;
  /** File content (if found) */
  content?: string;
  /** Source repository name */
  sourceRepository: string;
  /** Full path to the file */
  path: string;
}

/**
 * RepositoryContextLoader - Singleton service for repository-scoped context paths
 *
 * Coordinates with WorkspaceManager to provide correct paths for:
 * - Pipeline context files (.nightgauge/pipeline/)
 * - CLAUDE.md files
 * - docs/ documentation
 * - standards/ files
 *
 * @example
 * ```typescript
 * const loader = RepositoryContextLoader.getInstance();
 * await loader.initialize(workspaceManager);
 *
 * // Get context directory for current repository
 * const contextDir = loader.getContextDir();
 *
 * // Get specific context file path
 * const issuePath = loader.getContextFile('issue', 42);
 *
 * // Load CLAUDE.md with precedence
 * const claudeMd = await loader.loadClaudeMd();
 * ```
 */
export class RepositoryContextLoader implements vscode.Disposable {
  private static instance: RepositoryContextLoader | null = null;

  private workspaceManager: WorkspaceManager | null = null;
  private disposables: vscode.Disposable[] = [];
  private _initialized: boolean = false;

  // Cache for loaded content
  private claudeMdCache: Map<string, string | null> = new Map();

  // Event emitters
  private _onContextChanged = new vscode.EventEmitter<Repository | null>();

  /**
   * Fired when the context changes (repository switch or workspace reload)
   */
  readonly onContextChanged = this._onContextChanged.event;

  private constructor() {}

  /**
   * Get the singleton instance
   */
  static getInstance(): RepositoryContextLoader {
    if (!RepositoryContextLoader.instance) {
      RepositoryContextLoader.instance = new RepositoryContextLoader();
    }
    return RepositoryContextLoader.instance;
  }

  /**
   * Reset the singleton instance (for testing)
   */
  static resetInstance(): void {
    if (RepositoryContextLoader.instance) {
      RepositoryContextLoader.instance.dispose();
      RepositoryContextLoader.instance = null;
    }
  }

  /**
   * Initialize the context loader with WorkspaceManager
   *
   * Must be called after WorkspaceManager.initialize() completes.
   *
   * @param workspaceManager - The initialized WorkspaceManager instance
   */
  async initialize(workspaceManager: WorkspaceManager): Promise<void> {
    if (this._initialized) {
      return;
    }

    this.workspaceManager = workspaceManager;

    // Subscribe to workspace changes (config reload, workspace folder add/remove)
    const workspaceSubscription = workspaceManager.onWorkspaceChanged(() => {
      this.clearAllCaches();
      this._onContextChanged.fire(resolveActiveRepository(workspaceManager));
    });
    this.disposables.push(workspaceSubscription);

    // Re-fire context-changed when the user focuses a file in a different
    // repo — replaces the old onRepositoryChanged hook that tied to the
    // removed workspace-global current-repo pointer.
    const onDidChange = vscode.window?.onDidChangeActiveTextEditor;
    if (typeof onDidChange === "function") {
      this.disposables.push(
        onDidChange(() => {
          this.clearAllCaches();
          this._onContextChanged.fire(resolveActiveRepository(workspaceManager));
        })
      );
    }

    this._initialized = true;
  }

  /**
   * Clear all cached content
   */
  private clearAllCaches(): void {
    this.claudeMdCache.clear();
  }

  /**
   * Check if initialization has completed
   */
  isInitialized(): boolean {
    return this._initialized;
  }

  /**
   * Get the repository the user is currently looking at (derived from the
   * active editor). Replaces the old workspace-global current-repo pointer.
   *
   * @returns Active repository or null when no repos are loaded
   */
  getCurrentRepository(): Repository | null {
    return resolveActiveRepository(this.workspaceManager);
  }

  /**
   * Get the context directory for the current or specified repository
   *
   * Returns the path to .nightgauge/pipeline/ directory.
   *
   * @param repository - Optional repository (defaults to current)
   * @returns Absolute path to context directory
   */
  getContextDir(repository?: Repository): string {
    const repo = repository ?? this.getCurrentRepository();

    if (!repo) {
      // Fallback to workspace root if no repository
      const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? "";
      return path.join(workspaceRoot, ".nightgauge", "pipeline");
    }

    return path.join(repo.path, ".nightgauge", "pipeline");
  }

  /**
   * Get the path to a specific context file
   *
   * @param type - Type of context file
   * @param issueNumber - Issue number (required for most types)
   * @param repository - Optional repository (defaults to current)
   * @returns Absolute path to the context file
   */
  getContextFile(type: ContextFileType, issueNumber?: number, repository?: Repository): string {
    const contextDir = this.getContextDir(repository);

    switch (type) {
      case "state":
        return path.join(contextDir, "state.json");
      case "batch-state":
        return path.join(contextDir, "batch-state.json");
      case "issue":
        return path.join(contextDir, `issue-${issueNumber}.json`);
      case "planning":
        return path.join(contextDir, `planning-${issueNumber}.json`);
      case "dev":
        return path.join(contextDir, `dev-${issueNumber}.json`);
      case "validate":
        return path.join(contextDir, `validate-${issueNumber}.json`);
      case "pr":
        return path.join(contextDir, `pr-${issueNumber}.json`);
      case "feedback":
        return path.join(contextDir, `feedback-${issueNumber}.json`);
      case "running":
        return path.join(contextDir, `running-${issueNumber}.json`);
      case "batch":
        return path.join(contextDir, `batch-${issueNumber}.json`);
      case "planning-batch":
        return path.join(contextDir, `planning-batch-${issueNumber}.json`);
      case "dev-batch":
        return path.join(contextDir, `dev-batch-${issueNumber}.json`);
      default:
        throw new Error(`Unknown context file type: ${type}`);
    }
  }

  /**
   * Get the plans directory for the current or specified repository
   *
   * @param repository - Optional repository (defaults to current)
   * @returns Absolute path to .nightgauge/plans/ directory
   */
  getPlansDir(repository?: Repository): string {
    const repo = repository ?? this.getCurrentRepository();

    if (!repo) {
      const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? "";
      return path.join(workspaceRoot, ".nightgauge", "plans");
    }

    return path.join(repo.path, ".nightgauge", "plans");
  }

  /**
   * Get path to a plan file
   *
   * @param issueNumber - Issue number
   * @param slug - Optional slug for the plan file name
   * @param repository - Optional repository (defaults to current)
   * @returns Absolute path to the plan file
   */
  getPlanFile(issueNumber: number, slug?: string, repository?: Repository): string {
    const plansDir = this.getPlansDir(repository);
    const filename = slug ? `${issueNumber}-${slug}.md` : `${issueNumber}-plan.md`;
    return path.join(plansDir, filename);
  }

  /**
   * Load CLAUDE.md content with precedence handling
   *
   * Precedence: repository CLAUDE.md > workspace CLAUDE.md > null
   *
   * @param repository - Optional repository (defaults to current)
   * @returns CLAUDE.md content or null if not found
   */
  async loadClaudeMd(repository?: Repository): Promise<string | null> {
    const repo = repository ?? this.getCurrentRepository();
    const repoPath = repo?.path;

    // Check cache
    if (repoPath && this.claudeMdCache.has(repoPath)) {
      return this.claudeMdCache.get(repoPath) ?? null;
    }

    let content: string | null = null;

    // Try repository CLAUDE.md
    if (repoPath) {
      content = await this.readFileIfExists(path.join(repoPath, "CLAUDE.md"));
    }

    // Fallback to workspace root CLAUDE.md (for single-repo or shared workspace config)
    if (!content && this.workspaceManager) {
      const workspaceRoot = this.workspaceManager.getWorkspaceRoot();
      if (workspaceRoot !== repoPath) {
        content = await this.readFileIfExists(path.join(workspaceRoot, "CLAUDE.md"));
      }
    }

    // Cache the result
    if (repoPath) {
      this.claudeMdCache.set(repoPath, content);
    }

    return content;
  }

  /**
   * Get the precedence information for the current context
   *
   * @param repository - Optional repository (defaults to current)
   * @returns Information about which config source is being used
   */
  async getPrecedence(repository?: Repository): Promise<ContextPrecedence> {
    const repo = repository ?? this.getCurrentRepository();

    if (!repo) {
      return { source: "default" };
    }

    // Check for repository CLAUDE.md
    const repoClaudeMd = path.join(repo.path, "CLAUDE.md");
    if (await this.fileExists(repoClaudeMd)) {
      return { source: "repository", configPath: repoClaudeMd };
    }

    // Check for workspace CLAUDE.md
    if (this.workspaceManager) {
      const workspaceRoot = this.workspaceManager.getWorkspaceRoot();
      const workspaceClaudeMd = path.join(workspaceRoot, "CLAUDE.md");
      if (await this.fileExists(workspaceClaudeMd)) {
        return { source: "workspace", configPath: workspaceClaudeMd };
      }
    }

    return { source: "default" };
  }

  /**
   * Load a documentation file from docs/
   *
   * @param docPath - Relative path within docs/ (e.g., 'GIT_WORKFLOW.md')
   * @param repository - Optional repository (defaults to current)
   * @returns DocFileResult with content and metadata
   */
  async loadDocsFile(docPath: string, repository?: Repository): Promise<DocFileResult> {
    const repo = repository ?? this.getCurrentRepository();
    const repoName = repo?.name ?? "default";

    // Prevent path traversal
    const normalizedPath = this.sanitizePath(docPath);

    let fullPath: string;

    if (repo) {
      fullPath = path.join(repo.path, "docs", normalizedPath);
    } else {
      const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? "";
      fullPath = path.join(workspaceRoot, "docs", normalizedPath);
    }

    const content = await this.readFileIfExists(fullPath);

    return {
      found: content !== null,
      content: content ?? undefined,
      sourceRepository: repoName,
      path: fullPath,
    };
  }

  /**
   * Load a standards file from standards/
   *
   * @param standardsPath - Relative path within standards/ (e.g., 'security.md')
   * @param repository - Optional repository (defaults to current)
   * @returns DocFileResult with content and metadata
   */
  async loadStandardsFile(standardsPath: string, repository?: Repository): Promise<DocFileResult> {
    const repo = repository ?? this.getCurrentRepository();
    const repoName = repo?.name ?? "default";

    // Prevent path traversal
    const normalizedPath = this.sanitizePath(standardsPath);

    let fullPath: string;

    if (repo) {
      fullPath = path.join(repo.path, "standards", normalizedPath);
    } else {
      const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? "";
      fullPath = path.join(workspaceRoot, "standards", normalizedPath);
    }

    const content = await this.readFileIfExists(fullPath);

    return {
      found: content !== null,
      content: content ?? undefined,
      sourceRepository: repoName,
      path: fullPath,
    };
  }

  /**
   * Get the working directory for skill execution
   *
   * This is the directory where skills should be executed (the repository root).
   *
   * @param repository - Optional repository (defaults to current)
   * @returns Absolute path to working directory
   */
  getWorkingDirectory(repository?: Repository): string {
    const repo = repository ?? this.getCurrentRepository();

    if (!repo) {
      return vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? process.cwd();
    }

    return repo.path;
  }

  /**
   * Sanitize a relative path to prevent directory traversal
   *
   * @param relativePath - Path to sanitize
   * @returns Sanitized path without traversal attempts
   */
  private sanitizePath(relativePath: string): string {
    // Remove any leading slashes
    let sanitized = relativePath.replace(/^[/\\]+/, "");

    // Replace backslashes with forward slashes
    sanitized = sanitized.replace(/\\/g, "/");

    // Remove any .. segments
    const segments = sanitized.split("/").filter((s) => s !== "..");

    // Rejoin without empty segments
    return segments.filter((s) => s.length > 0).join("/");
  }

  /**
   * Check if a file exists
   */
  private async fileExists(filePath: string): Promise<boolean> {
    try {
      await fs.access(filePath);
      return true;
    } catch {
      return false;
    }
  }

  /**
   * Read a file if it exists, returning null otherwise
   */
  private async readFileIfExists(filePath: string): Promise<string | null> {
    try {
      const content = await fs.readFile(filePath, "utf-8");
      return content;
    } catch {
      return null;
    }
  }

  /**
   * Dispose resources
   */
  dispose(): void {
    this._onContextChanged.dispose();
    for (const disposable of this.disposables) {
      disposable.dispose();
    }
    this.claudeMdCache.clear();
  }
}
