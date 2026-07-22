/**
 * ContextWatcherService - Watches .nightgauge/pipeline/ for pipeline context files
 *
 * Emits events when context files are created, modified, or deleted,
 * allowing the extension to react to pipeline state changes from the
 * Claude Code terminal (e.g., when /nightgauge:issue-pickup completes).
 */

import * as vscode from "vscode";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import type { IssueInfo } from "../views/items/IssueTreeItem";
import type { PipelineStage } from "@nightgauge/sdk";
import type { Logger } from "../utils/logger";

/**
 * Schema for issue context files created by /nightgauge:issue-pickup
 * See: docs/CONTEXT_ARCHITECTURE.md
 */
interface IssueContextFile {
  schema_version: string;
  issue_number: number;
  title: string;
  type: string;
  branch: string;
  base_branch: string;
  requirements: {
    summary: string;
    user_story?: string;
    acceptance_criteria?: string[];
    technical_notes?: string[];
  };
  labels: string[];
  milestone?: string;
  created_at: string;
}

/**
 * Mapping of context file prefixes to pipeline stages
 *
 * Each skill creates a context file when it completes:
 * - issue-{N}.json → issue-pickup
 * - planning-{N}.json → feature-planning
 * - dev-{N}.json → feature-dev
 * - validate-{N}.json → feature-validate
 * - pr-{N}.json → pr-create
 * - merge-{N}.json → pr-merge
 */
const CONTEXT_FILE_TO_STAGE: Record<string, PipelineStage> = {
  issue: "issue-pickup",
  planning: "feature-planning",
  dev: "feature-dev",
  validate: "feature-validate",
  pr: "pr-create",
  merge: "pr-merge",
};

/**
 * ContextWatcherService - Watches for pipeline context files
 *
 * @example
 * ```typescript
 * const watcher = new ContextWatcherService(workspaceRoot, logger);
 *
 * watcher.onIssuePickedUp((issueInfo) => {
 *   treeProvider.setIssue(issueInfo);
 * });
 *
 * // Scan for existing context on startup
 * await watcher.scanExistingContext();
 * ```
 */
export class ContextWatcherService implements vscode.Disposable {
  private watchers: vscode.FileSystemWatcher[] = [];
  private disposables: vscode.Disposable[] = [];

  /**
   * When true, all event firing is suppressed.
   *
   * In concurrent pipeline mode each slot has its own PipelineStateService
   * and writes context files inside its git worktree. The main repo's
   * `.nightgauge/pipeline/` directory may contain stale files from
   * previous runs. Firing events for those files would corrupt the
   * singleton PipelineStateService and show ghost entries in the tree view.
   *
   * @see Issue #1540 — ContextWatcher shows stale "completed" items in concurrent mode
   */
  private _suspended = false;

  private _onIssuePickedUp = new vscode.EventEmitter<IssueInfo>();
  private _onIssueCleared = new vscode.EventEmitter<number>();
  private _onStageComplete = new vscode.EventEmitter<{
    issueNumber: number;
    stage: PipelineStage;
  }>();

  /**
   * Fired when an issue-*.json context file is created or modified
   */
  readonly onIssuePickedUp = this._onIssuePickedUp.event;

  /**
   * Fired when an issue context file is deleted
   */
  readonly onIssueCleared = this._onIssueCleared.event;

  /**
   * Fired when any stage context file is created (for stage tracking)
   */
  readonly onStageComplete = this._onStageComplete.event;

  constructor(
    private workspaceRoot: string,
    private logger: Logger
  ) {
    if (workspaceRoot) {
      this.initializeWatchers();
    }
  }

  /**
   * Suspend event firing (concurrent pipeline mode).
   *
   * When concurrent mode is active, per-slot PipelineStateService instances
   * manage state inside worktrees. The main-repo context watcher must not
   * fire events that would corrupt the singleton state or tree view.
   */
  suspend(): void {
    this._suspended = true;
    this.logger.debug("ContextWatcherService suspended (concurrent mode)");
  }

  /**
   * Resume event firing (sequential pipeline mode).
   */
  resume(): void {
    this._suspended = false;
    this.logger.debug("ContextWatcherService resumed (sequential mode)");
  }

  /**
   * Whether the watcher is currently suspended.
   */
  get isSuspended(): boolean {
    return this._suspended;
  }

  /**
   * Initialize file system watchers for context files
   */
  private initializeWatchers(): void {
    const contextDir = path.join(this.workspaceRoot, ".nightgauge", "pipeline");

    // Watch for issue context files
    const issuePattern = new vscode.RelativePattern(contextDir, "issue-*.json");
    const issueWatcher = vscode.workspace.createFileSystemWatcher(issuePattern);

    issueWatcher.onDidCreate((uri) => this.handleIssueFileCreated(uri));
    issueWatcher.onDidChange((uri) => this.handleIssueFileChanged(uri));
    issueWatcher.onDidDelete((uri) => this.handleIssueFileDeleted(uri));

    this.watchers.push(issueWatcher);

    // Watch for other stage context files (planning, dev, validate, pr, merge)
    for (const prefix of ["planning", "dev", "validate", "pr", "merge"]) {
      const pattern = new vscode.RelativePattern(contextDir, `${prefix}-*.json`);
      const watcher = vscode.workspace.createFileSystemWatcher(pattern);

      watcher.onDidCreate((uri) => this.handleStageFileCreated(uri, prefix));
      watcher.onDidChange((uri) => this.handleStageFileCreated(uri, prefix));

      this.watchers.push(watcher);
    }

    this.logger.debug("Context watchers initialized", { contextDir });
  }

  /**
   * Handle issue context file creation
   */
  private async handleIssueFileCreated(uri: vscode.Uri): Promise<void> {
    if (this._suspended) return;
    this.logger.debug("Issue context file created", { path: uri.fsPath });

    const issueInfo = await this.parseIssueFile(uri.fsPath);
    if (issueInfo) {
      this._onIssuePickedUp.fire(issueInfo);
      this._onStageComplete.fire({
        issueNumber: issueInfo.number,
        stage: "issue-pickup",
      });
    }
  }

  /**
   * Handle issue context file modification
   */
  private async handleIssueFileChanged(uri: vscode.Uri): Promise<void> {
    if (this._suspended) return;
    this.logger.debug("Issue context file changed", { path: uri.fsPath });

    const issueInfo = await this.parseIssueFile(uri.fsPath);
    if (issueInfo) {
      this._onIssuePickedUp.fire(issueInfo);
    }
  }

  /**
   * Handle issue context file deletion
   */
  private handleIssueFileDeleted(uri: vscode.Uri): void {
    if (this._suspended) return;
    this.logger.debug("Issue context file deleted", { path: uri.fsPath });

    const issueNumber = this.extractIssueNumber(uri.fsPath);
    if (issueNumber !== null) {
      this._onIssueCleared.fire(issueNumber);
    }
  }

  /**
   * Handle stage context file creation (planning, dev, pr)
   */
  private async handleStageFileCreated(uri: vscode.Uri, prefix: string): Promise<void> {
    if (this._suspended) return;
    this.logger.debug("Stage context file created", {
      path: uri.fsPath,
      prefix,
    });

    const issueNumber = this.extractIssueNumber(uri.fsPath);
    const stage = CONTEXT_FILE_TO_STAGE[prefix];

    if (issueNumber !== null && stage) {
      this._onStageComplete.fire({ issueNumber, stage });
    }
  }

  /**
   * Parse an issue context file and return IssueInfo
   */
  private async parseIssueFile(filePath: string): Promise<IssueInfo | null> {
    const maxAttempts = 3;
    const retryDelayMs = 75;
    let lastError: unknown;

    for (let attempt = 1; attempt <= maxAttempts; attempt += 1) {
      try {
        const content = await fs.readFile(filePath, "utf-8");
        const context = JSON.parse(content) as IssueContextFile;

        // Validate required fields
        if (!context.issue_number || !context.title || !context.branch) {
          this.logger.warn("Invalid issue context file - missing required fields", {
            filePath,
          });
          return null;
        }

        return {
          number: context.issue_number,
          title: context.title,
          branch: context.branch,
          baseBranch: context.base_branch,
          labels: context.labels,
        };
      } catch (error) {
        lastError = error;

        if ((error as NodeJS.ErrnoException).code === "ENOENT") {
          this.logger.debug("Issue context file not found", { filePath });
          return null;
        }

        const message = error instanceof Error ? error.message : "";
        const isTransientPartialJson = message.includes("Unexpected end of JSON input");
        const hasRetriesRemaining = attempt < maxAttempts;

        if (isTransientPartialJson && hasRetriesRemaining) {
          // File watcher can fire while file is still being written; retry shortly.
          await new Promise((resolve) => setTimeout(resolve, retryDelayMs));
          continue;
        }
      }
    }

    this.logger.warn("Failed to parse issue context file", {
      filePath,
      error: lastError instanceof Error ? lastError.message : "Unknown error",
    });
    return null;
  }

  /**
   * Extract issue number from a context file path
   *
   * Handles patterns like:
   * - issue-42.json -> 42
   * - planning-42.json -> 42
   * - dev-42.json -> 42
   * - validate-42.json -> 42
   * - pr-42.json -> 42
   * - merge-42.json -> 42
   */
  private extractIssueNumber(filePath: string): number | null {
    const filename = path.basename(filePath);
    const match = filename.match(/^(?:issue|planning|dev|validate|pr|merge)-(\d+)\.json$/);

    if (match) {
      return parseInt(match[1], 10);
    }
    return null;
  }

  /**
   * Scan for existing context files on extension activation
   *
   * This handles the case where VS Code is restarted mid-pipeline.
   */
  async scanExistingContext(): Promise<void> {
    if (!this.workspaceRoot || this._suspended) {
      return;
    }

    const contextDir = path.join(this.workspaceRoot, ".nightgauge", "pipeline");

    try {
      const files = await fs.readdir(contextDir);

      // Guard: If state.json does not exist, the pipeline was already completed
      // and cleared. Do not re-initialize from stale issue-*.json context files.
      // This prevents reload from resurrecting a completed pipeline. (Issue #1205)
      if (!files.includes("state.json")) {
        this.logger.debug(
          "No state.json found — pipeline previously completed, skipping context scan"
        );
        return;
      }

      // Find the most recent issue-*.json file
      const issueFiles = files.filter((f) => f.startsWith("issue-"));

      if (issueFiles.length === 0) {
        this.logger.debug("No existing issue context files found");
        return;
      }

      // If multiple issue files exist, find the most recently modified
      let mostRecentFile: string | null = null;
      let mostRecentTime = 0;

      for (const file of issueFiles) {
        const filePath = path.join(contextDir, file);
        try {
          const stat = await fs.stat(filePath);
          if (stat.mtimeMs > mostRecentTime) {
            mostRecentTime = stat.mtimeMs;
            mostRecentFile = filePath;
          }
        } catch {
          // File may have been deleted, skip
        }
      }

      if (mostRecentFile) {
        const issueInfo = await this.parseIssueFile(mostRecentFile);
        if (issueInfo) {
          this.logger.info("Found existing issue context", {
            issueNumber: issueInfo.number,
          });
          this._onIssuePickedUp.fire(issueInfo);

          // Also check which stages are complete based on existing files
          const issueNumber = issueInfo.number;
          this._onStageComplete.fire({ issueNumber, stage: "issue-pickup" });

          if (files.includes(`planning-${issueNumber}.json`)) {
            this._onStageComplete.fire({
              issueNumber,
              stage: "feature-planning",
            });
          }
          if (files.includes(`dev-${issueNumber}.json`)) {
            this._onStageComplete.fire({ issueNumber, stage: "feature-dev" });
          }
          if (files.includes(`validate-${issueNumber}.json`)) {
            this._onStageComplete.fire({
              issueNumber,
              stage: "feature-validate",
            });
          }
          if (files.includes(`pr-${issueNumber}.json`)) {
            this._onStageComplete.fire({ issueNumber, stage: "pr-create" });
          }
          if (files.includes(`merge-${issueNumber}.json`)) {
            this._onStageComplete.fire({ issueNumber, stage: "pr-merge" });
          }
        }
      }
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === "ENOENT") {
        this.logger.debug("Context directory does not exist yet");
      } else {
        this.logger.warn("Failed to scan existing context", {
          error: error instanceof Error ? error.message : "Unknown error",
        });
      }
    }
  }

  /**
   * Remove stale pipeline context files from the main repo.
   *
   * In concurrent mode, each slot writes context files to its own worktree.
   * The main repo's `.nightgauge/pipeline/` may contain leftover files
   * from previous runs that confuse the tree view and state service.
   *
   * Removes: issue-*.json, planning-*.json, dev-*.json, validate-*.json,
   * pr-*.json, merge-*.json, and state.json.
   * Preserves: queue-state.json, health-history.jsonl, calibration.json,
   * history/, and other non-context files.
   */
  async cleanStaleContextFiles(): Promise<number> {
    if (!this.workspaceRoot) return 0;

    const contextDir = path.join(this.workspaceRoot, ".nightgauge", "pipeline");

    const CONTEXT_PREFIXES = [
      "issue-",
      "planning-",
      "dev-",
      "validate-",
      "pr-",
      "merge-",
      "checkpoint-signal-",
      "winddown-signal-",
      "budget-overrun-",
    ];

    let cleaned = 0;
    try {
      const files = await fs.readdir(contextDir);
      for (const file of files) {
        const isContextFile =
          CONTEXT_PREFIXES.some((prefix) => file.startsWith(prefix) && file.endsWith(".json")) ||
          file === "state.json";

        if (isContextFile) {
          try {
            await fs.unlink(path.join(contextDir, file));
            cleaned++;
          } catch {
            // File may already be deleted
          }
        }
      }

      if (cleaned > 0) {
        this.logger.info("Cleaned stale context files from main repo", {
          cleaned,
        });
      }
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") {
        this.logger.warn("Failed to clean stale context files", {
          error: error instanceof Error ? error.message : "Unknown error",
        });
      }
    }

    return cleaned;
  }

  /**
   * Dispose all watchers and event emitters
   */
  dispose(): void {
    for (const watcher of this.watchers) {
      watcher.dispose();
    }
    for (const disposable of this.disposables) {
      disposable.dispose();
    }
    this._onIssuePickedUp.dispose();
    this._onIssueCleared.dispose();
    this._onStageComplete.dispose();
  }
}
