/**
 * NotificationService - Manages alerts, sounds, and system notifications
 *
 * Handles three notification triggers:
 * - User input needed (approval:needed event)
 * - Pipeline success (pipeline:complete event)
 * - Pipeline error (stage:error, stage:timeout events)
 *
 * Platform-aware: Uses afplay on macOS.
 * Cross-platform support (Windows/Linux) can be added in the future.
 */

import * as vscode from "vscode";
import { spawn, exec } from "child_process";
import { promisify } from "util";
import type { PipelineStage } from "@nightgauge/sdk";
import {
  getNotificationSettings,
  getSoundForType,
  type NotificationSettings,
  type NotificationType,
} from "../config/notificationSettings";
import type { GitHubUserEvent } from "./ProjectEventSubscriber";

const execAsync = promisify(exec);

/**
 * Debounce tracker to prevent notification spam.
 * Uses string keys to support composite keys (e.g. "issue.assigned:42") in addition
 * to NotificationType literals.
 */
interface DebounceState {
  lastNotification: Map<string, number>;
  debounceMs: number;
}

/**
 * NotificationService - Manages alerts, sounds, and system notifications
 *
 * @example
 * ```typescript
 * const notificationService = new NotificationService();
 *
 * // When user input is needed
 * await notificationService.notifyUserInputNeeded('feature-planning', 42);
 *
 * // When pipeline completes
 * await notificationService.notifyPipelineComplete(42);
 *
 * // When pipeline fails
 * await notificationService.notifyPipelineError('feature-dev', 'Build failed');
 * ```
 */
export class NotificationService implements vscode.Disposable {
  private debounce: DebounceState;
  private isMacOS: boolean;

  constructor() {
    this.isMacOS = process.platform === "darwin";
    this.debounce = {
      lastNotification: new Map(),
      debounceMs: 2000, // 2 second debounce between same notification types
    };
  }

  /**
   * Notify that user input is needed (e.g., plan approval)
   *
   * Plays alert sound, shows banner, and bounces Dock icon.
   */
  async notifyUserInputNeeded(stage: PipelineStage, issueNumber: number): Promise<void> {
    const settings = getNotificationSettings();

    if (!settings.enabled) {
      return;
    }

    if (!this.shouldNotify("alert")) {
      return;
    }

    const shouldSuppress = await this.shouldSuppressForDoNotDisturb(settings);
    if (shouldSuppress) {
      return;
    }

    const message = `Nightgauge: Approval needed for #${issueNumber} (${stage})`;

    // Play alert sound
    await this.playSound("alert", settings);

    // Show VS Code notification
    if (settings.banner.enabled) {
      vscode.window.showWarningMessage(message, "Review Plan").then((selection) => {
        if (selection === "Review Plan") {
          vscode.commands.executeCommand("nightgauge.viewContext");
        }
      });
    }

    // Request window attention (Dock bounce)
    if (settings.dockBounce.enabled) {
      this.requestWindowAttention();
    }

    this.updateDebounce("alert");
  }

  /**
   * Notify that the pipeline completed successfully
   */
  async notifyPipelineComplete(issueNumber: number): Promise<void> {
    const settings = getNotificationSettings();

    if (!settings.enabled) {
      return;
    }

    if (!settings.events.includes("pipeline.completed")) {
      return;
    }

    if (!this.shouldNotify("success")) {
      return;
    }

    const shouldSuppress = await this.shouldSuppressForDoNotDisturb(settings);
    if (shouldSuppress) {
      return;
    }

    const message = `Nightgauge: Pipeline complete for #${issueNumber}`;

    // Play success sound
    await this.playSound("success", settings);

    // Show VS Code notification
    if (settings.banner.enabled) {
      vscode.window.showInformationMessage(message, "View Dashboard").then((selection) => {
        if (selection === "View Dashboard") {
          vscode.commands.executeCommand("nightgauge.showDashboard");
        }
      });
    }

    this.updateDebounce("success");
  }

  /**
   * Notify that the pipeline failed with an error
   */
  async notifyPipelineError(stage: PipelineStage, errorMessage: string): Promise<void> {
    const settings = getNotificationSettings();

    if (!settings.enabled) {
      return;
    }

    if (!this.shouldNotify("error")) {
      return;
    }

    const shouldSuppress = await this.shouldSuppressForDoNotDisturb(settings);
    if (shouldSuppress) {
      return;
    }

    // Truncate error message if too long
    const truncatedError =
      errorMessage.length > 100 ? errorMessage.substring(0, 97) + "..." : errorMessage;

    const message = `Nightgauge: Error in ${stage}: ${truncatedError}`;

    // Play error sound
    await this.playSound("error", settings);

    // Show VS Code notification
    if (settings.banner.enabled) {
      vscode.window.showErrorMessage(message, "View Output").then((selection) => {
        if (selection === "View Output") {
          vscode.commands.executeCommand("nightgauge.showOutputWindow");
        }
      });
    }

    // Request window attention for errors too
    if (settings.dockBounce.enabled) {
      this.requestWindowAttention();
    }

    this.updateDebounce("error");
  }

  /**
   * Notify batch progress (issue completed in batch)
   *
   * Only shows notification if nightgauge.batch.notifyOnEachIssue is enabled.
   *
   * @param issueNumber - Current issue number
   * @param currentIndex - Current issue index (1-based)
   * @param totalCount - Total issues in batch
   * @param metrics - Optional resource metrics
   */
  async notifyBatchProgress(
    issueNumber: number,
    currentIndex: number,
    totalCount: number,
    metrics?: {
      tokens?: number;
      cost?: number;
      time?: number;
    }
  ): Promise<void> {
    // Check if per-issue notifications are enabled
    const config = vscode.workspace.getConfiguration("nightgauge.batch");
    const notifyOnEachIssue = config.get<boolean>("notifyOnEachIssue", false);

    if (!notifyOnEachIssue) {
      return;
    }

    const settings = getNotificationSettings();

    if (!settings.enabled) {
      return;
    }

    if (!this.shouldNotify("success")) {
      return;
    }

    const shouldSuppress = await this.shouldSuppressForDoNotDisturb(settings);
    if (shouldSuppress) {
      return;
    }

    // Build message
    let message = `Nightgauge: Batch progress ${currentIndex}/${totalCount} (#${issueNumber} complete)`;

    if (metrics) {
      const parts: string[] = [];
      if (metrics.cost !== undefined && metrics.cost > 0) {
        parts.push(`$${metrics.cost.toFixed(2)}`);
      }
      if (metrics.tokens !== undefined && metrics.tokens > 0) {
        parts.push(`${(metrics.tokens / 1000).toFixed(0)}k tokens`);
      }
      if (parts.length > 0) {
        message += ` • ${parts.join(", ")}`;
      }
    }

    // Play subtle success sound
    await this.playSound("success", settings);

    // Show VS Code notification
    if (settings.banner.enabled) {
      vscode.window.showInformationMessage(message);
    }

    this.updateDebounce("success");
  }

  /**
   * Notify batch completion
   *
   * Shows comprehensive summary with results and metrics.
   * Honors nightgauge.batch.notifyOnComplete and nightgauge.batch.showSummary settings.
   *
   * @param result - Batch execution result
   */
  async notifyBatchComplete(result: {
    totalIssues: number;
    successfulIssues: number;
    failedIssues: number;
    skippedIssues: number;
    metrics?: {
      tokens: number;
      cost: number;
      time: number;
    };
  }): Promise<void> {
    // Check if batch completion notifications are enabled
    const config = vscode.workspace.getConfiguration("nightgauge.batch");
    const notifyOnComplete = config.get<boolean>("notifyOnComplete", true);

    if (!notifyOnComplete) {
      return;
    }

    const settings = getNotificationSettings();

    if (!settings.enabled) {
      return;
    }

    if (!this.shouldNotify("success")) {
      return;
    }

    const shouldSuppress = await this.shouldSuppressForDoNotDisturb(settings);
    if (shouldSuppress) {
      return;
    }

    // Build message
    const { totalIssues, successfulIssues, failedIssues, skippedIssues } = result;
    let message = `Nightgauge: Batch complete • ${successfulIssues}/${totalIssues} succeeded`;

    if (failedIssues > 0) {
      message += ` • ${failedIssues} failed`;
    }
    if (skippedIssues > 0) {
      message += ` • ${skippedIssues} skipped`;
    }

    // Play success or error sound based on result
    const soundType = failedIssues > 0 ? "error" : "success";
    await this.playSound(soundType, settings);

    // Show VS Code notification with actions
    const showSummary = config.get<boolean>("showSummary", true);

    if (settings.banner.enabled) {
      const actions: string[] = [];
      if (showSummary) {
        actions.push("View Summary");
      }
      actions.push("View Dashboard");

      const notifyMethod =
        failedIssues > 0 ? vscode.window.showWarningMessage : vscode.window.showInformationMessage;

      notifyMethod(message, ...actions).then((selection) => {
        if (selection === "View Summary") {
          this.showBatchSummary(result);
        } else if (selection === "View Dashboard") {
          vscode.commands.executeCommand("nightgauge.showDashboard");
        }
      });
    }

    this.updateDebounce("success");
  }

  /**
   * Show detailed batch summary in modal
   *
   * @param result - Batch execution result
   */
  private showBatchSummary(result: {
    totalIssues: number;
    successfulIssues: number;
    failedIssues: number;
    skippedIssues: number;
    metrics?: {
      tokens: number;
      cost: number;
      time: number;
    };
  }): void {
    const { totalIssues, successfulIssues, failedIssues, skippedIssues, metrics } = result;

    const lines: string[] = [];
    lines.push("# Batch Pipeline Summary\n");
    lines.push(`**Total Issues**: ${totalIssues}`);
    lines.push(`**Successful**: ${successfulIssues}`);
    if (failedIssues > 0) {
      lines.push(`**Failed**: ${failedIssues}`);
    }
    if (skippedIssues > 0) {
      lines.push(`**Skipped**: ${skippedIssues}`);
    }

    if (metrics) {
      lines.push("\n## Resource Usage\n");
      lines.push(`**Tokens**: ${metrics.tokens.toLocaleString()}`);
      lines.push(`**Cost**: $${metrics.cost.toFixed(2)}`);
      lines.push(`**Time**: ${metrics.time.toFixed(1)} minutes`);
      lines.push(
        `**Avg per issue**: ${(metrics.cost / totalIssues).toFixed(2)} USD, ${Math.round(metrics.tokens / totalIssues).toLocaleString()} tokens`
      );
    }

    const summaryText = lines.join("\n");

    // Use VS Code's native markdown preview for better formatting
    vscode.window
      .showInformationMessage(
        `Batch complete: ${successfulIssues}/${totalIssues} succeeded`,
        "View Details",
        "Close"
      )
      .then((selection) => {
        if (selection === "View Details") {
          // Create a read-only document with the summary
          vscode.workspace
            .openTextDocument({
              content: summaryText,
              language: "markdown",
            })
            .then((doc) => {
              vscode.window.showTextDocument(doc, {
                preview: true,
                viewColumn: vscode.ViewColumn.Beside,
              });
            });
        }
      });
  }

  /**
   * Play a sound for the given notification type
   */
  private async playSound(type: NotificationType, settings: NotificationSettings): Promise<void> {
    const soundName = getSoundForType(settings, type);

    if (!soundName) {
      return;
    }

    if (this.isMacOS) {
      await this.playMacOSSound(soundName, settings.sounds.volume);
    }
    // Future: Add Windows/Linux sound support here
  }

  /**
   * Play a macOS system sound using afplay
   */
  private async playMacOSSound(soundName: string, volume: number): Promise<void> {
    const soundPath = `/System/Library/Sounds/${soundName}.aiff`;

    return new Promise((resolve) => {
      const proc = spawn("afplay", ["-v", String(volume), soundPath]);

      proc.on("close", () => {
        resolve();
      });

      proc.on("error", () => {
        // Silently fail if sound can't be played
        resolve();
      });
    });
  }

  /**
   * Request window attention (Dock bounce on macOS)
   */
  private requestWindowAttention(): void {
    if (!this.isMacOS) {
      return;
    }

    // Use AppleScript to request attention
    // This will bounce the Dock icon if VS Code is not focused
    exec(
      `osascript -e 'tell application "System Events" to tell process "Code" to set frontmost to false' 2>/dev/null`,
      () => {
        // Ignore errors - this is best effort
      }
    );
  }

  /**
   * Check if we should suppress notifications due to Do Not Disturb mode
   */
  private async shouldSuppressForDoNotDisturb(settings: NotificationSettings): Promise<boolean> {
    if (!settings.respectDoNotDisturb) {
      return false;
    }

    if (!this.isMacOS) {
      return false;
    }

    return await this.isDoNotDisturbEnabled();
  }

  /**
   * Check if macOS Do Not Disturb / Focus mode is enabled
   */
  private async isDoNotDisturbEnabled(): Promise<boolean> {
    try {
      // Check for Focus mode status on macOS Monterey+
      // This checks if the Focus Mode indicator is visible in the menu bar
      const { stdout } = await execAsync(
        'defaults read com.apple.controlcenter "NSStatusItem Visible FocusModes" 2>/dev/null'
      );

      return stdout.trim() === "1";
    } catch {
      // If we can't determine DND status, assume it's not enabled
      return false;
    }
  }

  /**
   * Check if we should show a notification (debounce check).
   * Accepts a string key to support composite keys for user-event deduplication.
   */
  private shouldNotify(key: string): boolean {
    const lastTime = this.debounce.lastNotification.get(key);

    if (!lastTime) {
      return true;
    }

    const now = Date.now();
    return now - lastTime >= this.debounce.debounceMs;
  }

  /**
   * Update debounce timestamp for a notification key.
   */
  private updateDebounce(key: string): void {
    this.debounce.lastNotification.set(key, Date.now());
  }

  /**
   * Notify user when pipeline cost approaches or exceeds the monthly budget (Issue #1333)
   *
   * Warning fires at ≥80% (default), critical at ≥90% (default).
   * Uses the existing 2000ms debounce guard to prevent spam on rapid polls.
   * Offers "Reset Counter" and "Open Dashboard" action buttons.
   *
   * @param level - 'warning' or 'critical'
   * @param usagePct - Current usage percentage (0-100+)
   * @param costUsd - Accumulated cost since last reset
   * @param budgetUsd - Configured monthly budget
   */
  notifyUsageWarning(
    level: "warning" | "critical",
    usagePct: number,
    costUsd: number,
    budgetUsd: number
  ): void {
    const settings = getNotificationSettings();
    if (!settings.enabled || !settings.banner.enabled) {
      return;
    }

    // Reuse existing debounce with the 'alert' type for usage warnings
    if (!this.shouldNotify("alert")) {
      return;
    }
    this.updateDebounce("alert");

    const remaining = budgetUsd - costUsd;
    const pctRounded = Math.round(usagePct);

    const message =
      level === "critical"
        ? `⚠️ Critical: Claude usage at ${pctRounded}% ($${costUsd.toFixed(2)} / $${budgetUsd.toFixed(2)}). $${remaining.toFixed(2)} remaining.`
        : `Claude usage at ${pctRounded}% of monthly budget ($${costUsd.toFixed(2)} / $${budgetUsd.toFixed(2)}).`;

    const showFn =
      level === "critical" ? vscode.window.showErrorMessage : vscode.window.showWarningMessage;

    showFn(message, "Reset Counter", "Open Dashboard").then((action) => {
      if (action === "Reset Counter") {
        vscode.commands.executeCommand("nightgauge.resetUsageCounter");
      } else if (action === "Open Dashboard") {
        vscode.commands.executeCommand("nightgauge.showDashboard");
      }
    });
  }

  /**
   * Notify user when platform quota approaches or exceeds the tier limit (Issue #1479)
   *
   * Warning fires at ≥80% (default), critical at ≥90% (default), block at 100%.
   * Uses the existing 2000ms debounce guard to prevent spam on rapid opens.
   *
   * @param level - 'warning', 'critical', or 'block'
   * @param pct - Current quota usage percentage (0-100+)
   * @param metric - The metric name (e.g., 'pipeline runs')
   */
  notifyQuotaWarning(level: "warning" | "critical" | "block", pct: number, metric: string): void {
    const settings = getNotificationSettings();
    if (!settings.enabled || !settings.banner.enabled) {
      return;
    }

    if (!this.shouldNotify("alert")) {
      return;
    }
    this.updateDebounce("alert");

    const pctRounded = Math.round(pct);

    let message: string;
    let showFn: typeof vscode.window.showInformationMessage;

    if (level === "block") {
      message = `Platform ${metric} quota exhausted (${pctRounded}%). Pipeline runs may be blocked by the platform.`;
      showFn = vscode.window.showErrorMessage;
    } else if (level === "critical") {
      message = `Critical: Platform ${metric} quota at ${pctRounded}%.`;
      showFn = vscode.window.showWarningMessage;
    } else {
      message = `Platform ${metric} quota at ${pctRounded}% of tier limit.`;
      showFn = vscode.window.showInformationMessage;
    }

    showFn(message, "Open Dashboard").then((action) => {
      if (action === "Open Dashboard") {
        vscode.commands.executeCommand("nightgauge.showDashboard");
      }
    });
  }

  /**
   * Notify the user when a GitHub event targets them (issue.assigned or PR review requested).
   *
   * Respects the opt-in `events` config gate, user-targeting filter, and per-type+issue debounce.
   *
   * @param event - The user-targeted GitHub event from ProjectEventSubscriber
   * @param currentUserEmail - The authenticated user's email from SessionManager
   */
  async notifyUserEvent(event: GitHubUserEvent, currentUserEmail: string | null): Promise<void> {
    const settings = getNotificationSettings();

    if (!settings.enabled) {
      return;
    }

    if (!settings.events.includes(event.type)) {
      return;
    }

    if (!this._isTargetedAtUser(event, currentUserEmail)) {
      return;
    }

    const debounceKey = `${event.type}:${event.issueNumber}`;
    // Use a 5s debounce for user events to coalesce burst assignments
    const prevMs = this.debounce.debounceMs;
    this.debounce.debounceMs = 5000;
    const shouldFire = this.shouldNotify(debounceKey);
    this.debounce.debounceMs = prevMs;
    if (!shouldFire) {
      return;
    }

    const shouldSuppress = await this.shouldSuppressForDoNotDisturb(settings);
    if (shouldSuppress) {
      return;
    }

    const { message, action, command } = this._buildUserEventMessage(event);

    if (settings.banner.enabled) {
      vscode.window.showInformationMessage(message, action).then((sel) => {
        if (sel === action && command && event.url) {
          void vscode.env.openExternal(vscode.Uri.parse(event.url));
        } else if (sel === action && command) {
          void vscode.commands.executeCommand(command);
        }
      });
    }

    // Record at 5s debounce window
    const prevMs2 = this.debounce.debounceMs;
    this.debounce.debounceMs = 5000;
    this.updateDebounce(debounceKey);
    this.debounce.debounceMs = prevMs2;
  }

  /**
   * Returns true when the event targets the authenticated user.
   *
   * Strategy: exact email match OR case-insensitive login match against the local-part
   * of currentUserEmail (before `@`). GitHub login ≠ email — this is a best-effort
   * heuristic documented as a known limitation.
   */
  private _isTargetedAtUser(event: GitHubUserEvent, currentUserEmail: string | null): boolean {
    if (!currentUserEmail) {
      return false;
    }

    if (event.targetEmail && event.targetEmail.toLowerCase() === currentUserEmail.toLowerCase()) {
      return true;
    }

    const localPart = currentUserEmail.split("@")[0]?.toLowerCase() ?? "";
    if (localPart && event.targetLogin.toLowerCase() === localPart) {
      return true;
    }

    return false;
  }

  /**
   * Build the toast message, action label, and command for a user-targeted event.
   */
  private _buildUserEventMessage(event: GitHubUserEvent): {
    message: string;
    action: string;
    command: string;
  } {
    if (event.type === "issue.assigned") {
      return {
        message: `Nightgauge: You were assigned to #${event.issueNumber}`,
        action: "View Issue",
        command: "nightgauge.openIssue",
      };
    }
    return {
      message: `Nightgauge: Review requested on #${event.issueNumber}`,
      action: "View PR",
      command: "nightgauge.openPR",
    };
  }

  /**
   * Dispose resources
   */
  dispose(): void {
    this.debounce.lastNotification.clear();
  }
}
