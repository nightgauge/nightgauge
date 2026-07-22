/**
 * AutomationService - Manages workflow automation triggers in VSCode extension
 *
 * Provides extension-level integration for the automation system:
 * - Subscribes to PipelineStateService events to fire triggers
 * - Watches automation log file for real-time updates
 * - Provides UI notifications for automation execution
 * - Exposes automation log to webviews and commands
 *
 * The actual automation execution happens in deterministic shell scripts
 * (automation-trigger.sh, automation-dispatch.sh). This service provides
 * the extension-level "wrapper" for rich notifications and UI integration.
 *
 * @consumers Extension activation (wired in bootstrap/services.ts)
 * @see Issue #137 - Workflow Automation Triggers
 * @see Issue #433 - config.yaml (formerly nightgauge.yaml)
 * @see docs/AUTOMATIONS.md - Automation configuration and usage
 */

import * as vscode from "vscode";
import * as fs from "node:fs/promises";
import { open as fsOpen } from "node:fs/promises";
import * as path from "node:path";
import type { PipelineStateService } from "./PipelineStateService";
import { resolveConfigPath, logDeprecationWarning } from "../utils/configPathResolver";

/**
 * Automation log entry (JSONL format from automation-dispatch.sh)
 */
export interface AutomationLogEntry {
  timestamp: string;
  trigger: string;
  action: string;
  status: "success" | "error";
  issue: number;
  message: string;
  dry_run: boolean;
}

/**
 * AutomationService - Extension integration for workflow automations
 *
 * @example
 * ```typescript
 * const automationService = new AutomationService(pipelineStateService, workspaceRoot);
 * await automationService.initialize();
 *
 * // Get recent log entries
 * const entries = await automationService.getLogEntries(10);
 *
 * // Watch for new entries
 * automationService.onLogEntry((entry) => {
 *   console.log(`Action ${entry.action}: ${entry.status}`);
 * });
 * ```
 */
export class AutomationService implements vscode.Disposable {
  private disposables: vscode.Disposable[] = [];
  private logWatcher: vscode.FileSystemWatcher | undefined;
  private logPath: string | undefined;
  private lastByteOffset = 0;
  private _onLogEntry = new vscode.EventEmitter<AutomationLogEntry>();

  /**
   * Event fired when a new automation log entry is written
   */
  readonly onLogEntry = this._onLogEntry.event;

  constructor(
    private readonly pipelineStateService: PipelineStateService,
    private readonly workspaceRoot: string
  ) {}

  /**
   * Initialize the automation service
   */
  async initialize(): Promise<void> {
    // Determine log file path from config
    this.logPath = await this.getLogFilePath();

    // Watch the log file for new entries
    if (this.logPath) {
      await this.setupLogWatcher();
    }

    // Subscribe to pipeline state changes
    this.subscribeToStateChanges();
  }

  /**
   * Get the automation log file path from config.yaml
   */
  private async getLogFilePath(): Promise<string | undefined> {
    const defaultLogPath = path.join(this.workspaceRoot, ".nightgauge", "logs", "automation.log");

    // Resolve config path with fallback to legacy
    const pathResult = await resolveConfigPath(this.workspaceRoot);

    if (!pathResult.exists) {
      return defaultLogPath;
    }

    // Log deprecation warning if using legacy path
    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    try {
      const configContent = await fs.readFile(pathResult.path, "utf-8");
      // Match log_file only under automations: section
      const match = configContent.match(
        /automations:\s*\n(?:\s+\w+:[^\n]*\n)*?\s+log_file:\s*["']?([^"'\n]+)["']?/
      );
      const logFile = match ? match[1].trim() : ".nightgauge/logs/automation.log";

      // Validate no path traversal
      if (logFile.includes("..") || path.isAbsolute(logFile)) {
        console.warn(
          "[AutomationService] log_file path contains traversal or is absolute, using default"
        );
        return defaultLogPath;
      }

      return path.join(this.workspaceRoot, logFile);
    } catch (error) {
      console.warn(
        "[AutomationService] Could not read config for log path:",
        error instanceof Error ? error.message : String(error)
      );
      return defaultLogPath;
    }
  }

  /**
   * Set up file system watcher for the log file
   */
  private async setupLogWatcher(): Promise<void> {
    if (!this.logPath) {
      return;
    }

    // Ensure log directory exists
    const logDir = path.dirname(this.logPath);
    try {
      await fs.mkdir(logDir, { recursive: true });
    } catch (error) {
      console.warn(
        "[AutomationService] Could not create log directory:",
        error instanceof Error ? error.message : String(error)
      );
    }

    // Create watcher for the log file
    const pattern = new vscode.RelativePattern(
      vscode.Uri.file(logDir),
      path.basename(this.logPath)
    );

    this.logWatcher = vscode.workspace.createFileSystemWatcher(pattern);

    this.logWatcher.onDidChange(async () => {
      await this.readNewLogEntries();
    });

    this.logWatcher.onDidCreate(async () => {
      this.lastByteOffset = 0;
      await this.readNewLogEntries();
    });

    this.disposables.push(this.logWatcher);

    // Get initial file size (byte offset)
    try {
      const stats = await fs.stat(this.logPath);
      this.lastByteOffset = stats.size;
    } catch {
      // File doesn't exist yet — start from beginning
    }
  }

  /**
   * Read new log entries since last byte position using byte-level file reads
   */
  private async readNewLogEntries(): Promise<void> {
    if (!this.logPath) {
      return;
    }

    let handle;
    try {
      handle = await fsOpen(this.logPath, "r");
      const stats = await handle.stat();

      if (stats.size <= this.lastByteOffset) {
        return;
      }

      // Read only the new bytes
      const newBytes = Buffer.alloc(stats.size - this.lastByteOffset);
      await handle.read(newBytes, 0, newBytes.length, this.lastByteOffset);
      this.lastByteOffset = stats.size;

      const newContent = newBytes.toString("utf-8");
      if (!newContent.trim()) {
        return;
      }

      // Parse JSONL entries
      const lines = newContent.trim().split("\n");
      for (const line of lines) {
        if (!line.trim()) {
          continue;
        }

        try {
          const entry = JSON.parse(line) as AutomationLogEntry;
          this._onLogEntry.fire(entry);
          this.notifyAutomationExecution(entry);
        } catch {
          // Skip malformed JSONL lines
          console.warn("[AutomationService] Skipping malformed log entry:", line.slice(0, 100));
        }
      }
    } catch (error) {
      console.warn(
        "[AutomationService] Error reading log entries:",
        error instanceof Error ? error.message : String(error)
      );
    } finally {
      await handle?.close();
    }
  }

  /**
   * Subscribe to pipeline state changes to show automation notifications
   */
  private subscribeToStateChanges(): void {
    const subscription = this.pipelineStateService.onStageComplete((event) => {
      this.checkForAutomationTriggers(event.stage);
    });
    this.disposables.push(subscription);
  }

  /**
   * Check if a stage completion might trigger automations (informational)
   */
  private checkForAutomationTriggers(stage: string): void {
    // Map stages to likely status labels
    const stageToStatus: Record<string, string> = {
      "issue-pickup": "status:in-progress",
      "pr-create": "status:in-review",
      "pr-merge": "status:done",
    };

    const status = stageToStatus[stage];
    if (status) {
      // Informational — actual detection happens in automation-trigger.sh
      console.debug(`[AutomationService] Stage ${stage} may trigger automations for ${status}`);
    }
  }

  /**
   * Show notification for automation execution
   */
  private notifyAutomationExecution(entry: AutomationLogEntry): void {
    const dryRunPrefix = entry.dry_run ? "[DRY-RUN] " : "";
    const statusIcon = entry.status === "success" ? "✓" : "✗";

    const message = `${dryRunPrefix}Automation ${statusIcon} ${entry.action} for #${entry.issue}`;

    if (entry.status === "success") {
      // Show subtle information message for success
      if (!entry.dry_run) {
        vscode.window.setStatusBarMessage(`$(zap) ${message}`, 5000);
      }
    } else {
      // Show warning for errors
      vscode.window
        .showWarningMessage(
          `Automation Error: ${entry.action} failed for #${entry.issue}: ${entry.message}`,
          "View Log"
        )
        .then((selection) => {
          if (selection === "View Log") {
            this.showLogFile().catch(() => {
              // Silently handle if log file was deleted between notification and click
            });
          }
        });
    }
  }

  /**
   * Get recent log entries
   *
   * @param limit - Maximum number of entries to return (default: 50)
   * @returns Array of log entries, newest first
   */
  async getLogEntries(limit = 50): Promise<AutomationLogEntry[]> {
    if (!this.logPath) {
      return [];
    }

    try {
      const content = await fs.readFile(this.logPath, "utf-8");
      const lines = content
        .trim()
        .split("\n")
        .filter((l) => l.trim());

      // Parse and return newest first
      const entries: AutomationLogEntry[] = [];
      for (let i = lines.length - 1; i >= 0 && entries.length < limit; i--) {
        try {
          entries.push(JSON.parse(lines[i]) as AutomationLogEntry);
        } catch {
          // Skip malformed entries
        }
      }

      return entries;
    } catch {
      return [];
    }
  }

  /**
   * Get entries filtered by issue number
   *
   * @param issueNumber - Issue number to filter by
   * @param limit - Maximum entries to return (scans up to limit*5 total entries)
   * @returns Filtered log entries
   */
  async getEntriesForIssue(issueNumber: number, limit = 20): Promise<AutomationLogEntry[]> {
    const entries = await this.getLogEntries(limit * 5); // Get more to filter
    return entries.filter((e) => e.issue === issueNumber).slice(0, limit);
  }

  /**
   * Get entries filtered by action type
   *
   * @param actionType - Action type to filter by (e.g., 'post_slack')
   * @param limit - Maximum entries to return (scans up to limit*5 total entries)
   * @returns Filtered log entries
   */
  async getEntriesByAction(actionType: string, limit = 20): Promise<AutomationLogEntry[]> {
    const entries = await this.getLogEntries(limit * 5);
    return entries.filter((e) => e.action === actionType).slice(0, limit);
  }

  /**
   * Show the automation log file in editor
   */
  async showLogFile(): Promise<void> {
    if (!this.logPath) {
      vscode.window.showErrorMessage("No automation log file configured");
      return;
    }

    try {
      const doc = await vscode.workspace.openTextDocument(vscode.Uri.file(this.logPath));
      await vscode.window.showTextDocument(doc, {
        preview: true,
        viewColumn: vscode.ViewColumn.Beside,
      });
    } catch (error) {
      console.error("[AutomationService] Could not open log file:", error);
      vscode.window.showErrorMessage("Could not open automation log file");
    }
  }

  /**
   * Clear the automation log
   */
  async clearLog(): Promise<void> {
    if (!this.logPath) {
      return;
    }

    const confirm = await vscode.window.showWarningMessage(
      "Clear automation log? This cannot be undone.",
      { modal: true },
      "Clear",
      "Cancel"
    );

    if (confirm !== "Clear") {
      return;
    }

    try {
      await fs.writeFile(this.logPath, "", "utf-8");
      this.lastByteOffset = 0;
      vscode.window.showInformationMessage("Automation log cleared");
    } catch (error) {
      console.error("[AutomationService] Failed to clear log:", error);
      vscode.window.showErrorMessage("Failed to clear automation log");
    }
  }

  /**
   * Check if automations are enabled in config
   */
  async isEnabled(): Promise<boolean> {
    // Resolve config path with fallback to legacy
    const pathResult = await resolveConfigPath(this.workspaceRoot);

    if (!pathResult.exists) {
      return false;
    }

    try {
      const content = await fs.readFile(pathResult.path, "utf-8");

      // Check for automations.enabled: false
      if (/automations:\s*\n\s*enabled:\s*false/i.test(content)) {
        return false;
      }

      // Check if automations section exists with triggers
      if (/automations:\s*\n\s*triggers:/i.test(content)) {
        return true;
      }

      // Default: disabled if no triggers defined
      return false;
    } catch {
      return false;
    }
  }

  /**
   * Dispose resources
   */
  dispose(): void {
    this._onLogEntry.dispose();
    this.disposables.forEach((d) => d.dispose());
  }
}
