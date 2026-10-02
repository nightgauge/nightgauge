/**
 * QueueSectionTreeItem - Collapsible section for queued issues
 *
 * Provides a section header in the pipeline tree view that contains
 * all queued issues. Supports collapse/expand and shows queue count.
 *
 * @see Issue #236 - Queue Issues When Pipeline Active
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";
import { QueuedIssueTreeItem } from "./QueuedIssueTreeItem";
import type { QueueItem, QueueStatus } from "../../types/queue";
import {
  describeWorkspaceThrottle,
  type WorkspaceThrottle,
} from "../../services/WorkspaceThrottle";

/**
 * Get icon for queue status
 */
function getQueueStatusIcon(status: QueueStatus, itemCount: number): vscode.ThemeIcon {
  if (itemCount === 0) {
    return new vscode.ThemeIcon("list-unordered", new vscode.ThemeColor("disabledForeground"));
  }

  switch (status) {
    case "paused":
      return new vscode.ThemeIcon("debug-pause", new vscode.ThemeColor("charts.orange"));
    case "processing":
      return new vscode.ThemeIcon("sync~spin", new vscode.ThemeColor("charts.yellow"));
    case "waiting":
      return new vscode.ThemeIcon("list-ordered", new vscode.ThemeColor("charts.blue"));
    case "idle":
    default:
      return new vscode.ThemeIcon("list-unordered");
  }
}

/**
 * QueueSectionTreeItem - Section header for queued issues
 *
 * @example
 * ```typescript
 * const section = new QueueSectionTreeItem();
 * section.setItems(queueItems);
 * section.setStatus('waiting');
 * ```
 */
export class QueueSectionTreeItem extends BaseTreeItem {
  private items: QueuedIssueTreeItem[] = [];
  private queueItems: QueueItem[] = [];
  private queueStatus: QueueStatus = "idle";
  private pauseReason?: string;
  /** Items actually waiting for a slot (excludes `processing`). */
  private queuedCount = 0;
  /** Items dispatched and executing — marked in place, not removed (#232/#246). */
  private runningCount = 0;
  /** The platform's workspace throttle holding dispatch, or null (#2337). */
  private workspaceThrottle: WorkspaceThrottle | null = null;

  constructor() {
    super("Queued Issues", vscode.TreeItemCollapsibleState.Collapsed);

    this.description = "(0)";
    this.iconPath = getQueueStatusIcon("idle", 0);
    this.contextValue = "queueSection";

    // Tooltip with section description
    this.updateTooltip();
  }

  /**
   * Render the section header count (Issue #264).
   *
   * The header answers "how much work is WAITING?", so running work is
   * excluded from the queued count — it is already represented by the run
   * view, and counting it here read as double-dispatch to an operator.
   *
   * Running items are still counted, separately and visibly. Hiding them
   * would re-create the #232 blindness that the mark-in-place model
   * (#232/#246) exists to fix: before that change, dispatch deleted the only
   * record of the work and the queue reported `idle` while a pipeline ran.
   */
  private describeCounts(): string {
    // A throttle that holds dispatch says so where the waiting work is counted.
    const cap = this.workspaceThrottle
      ? ` · capped at ${this.workspaceThrottle.maxConcurrent}`
      : "";
    if (this.runningCount === 0) {
      return `(${this.queuedCount})${cap}`;
    }
    if (this.queuedCount === 0) {
      return `(0 queued, ${this.runningCount} running)${cap}`;
    }
    return `(${this.queuedCount} queued, ${this.runningCount} running)${cap}`;
  }

  /**
   * Update the tooltip based on current state
   */
  private updateTooltip(): void {
    let tooltipText =
      "**Queued Issues**\n\n" +
      "Issues waiting to be processed when the current pipeline completes.\n\n";

    if (this.items.length === 0) {
      tooltipText += "_No issues queued_";
    } else {
      tooltipText += `**${this.queuedCount}** issue(s) waiting\n`;
      if (this.runningCount > 0) {
        tooltipText += `**${this.runningCount}** currently running (shown here and in the run view — one record, two views)\n`;
      }
      tooltipText += `Status: ${this.queueStatus}`;
      if (this.pauseReason) {
        tooltipText += `\n\n⚠️ ${this.pauseReason}`;
      }
    }
    if (this.workspaceThrottle) {
      tooltipText +=
        `\n\nThe workspace's concurrency cap allows ` +
        `${describeWorkspaceThrottle(this.workspaceThrottle)}. Queued issues start ` +
        `when it is raised, cleared or ends.`;
    }

    this.tooltip = new vscode.MarkdownString(tooltipText);
  }

  /**
   * Set the queue items
   */
  setItems(queueItems: QueueItem[]): void {
    this.queueItems = queueItems;
    this.items = queueItems.map((item) => new QueuedIssueTreeItem(item));
    this.runningCount = queueItems.filter((item) => item.status === "processing").length;
    this.queuedCount = this.items.length - this.runningCount;
    this.description = this.describeCounts();
    this.iconPath = getQueueStatusIcon(this.queueStatus, this.items.length);
    this.updateTooltip();

    // Auto-expand when items are added
    if (this.items.length > 0) {
      this.collapsibleState = vscode.TreeItemCollapsibleState.Expanded;
    }
  }

  /** Show the workspace throttle that holds dispatch, or none (#2337). */
  setWorkspaceThrottle(throttle: WorkspaceThrottle | null): void {
    this.workspaceThrottle = throttle;
    this.description = this.describeCounts();
    this.updateTooltip();
  }

  /**
   * Set the queue status
   */
  setStatus(status: QueueStatus, pauseReason?: string): void {
    this.queueStatus = status;
    this.pauseReason = pauseReason;
    this.iconPath = getQueueStatusIcon(status, this.items.length);
    this.updateTooltip();
  }

  /**
   * Get queue item count
   */
  getItemCount(): number {
    return this.items.length;
  }

  /**
   * Get the current queue status
   */
  getStatus(): QueueStatus {
    return this.queueStatus;
  }

  /**
   * Get children (queued issue items)
   */
  override getChildren(): BaseTreeItem[] {
    return this.items;
  }

  /**
   * Clear all items
   */
  clear(): void {
    this.items = [];
    this.queueItems = [];
    this.queuedCount = 0;
    this.runningCount = 0;
    this.queueStatus = "idle";
    this.pauseReason = undefined;
    this.description = this.describeCounts();
    this.iconPath = getQueueStatusIcon("idle", 0);
    this.collapsibleState = vscode.TreeItemCollapsibleState.Collapsed;
    this.updateTooltip();
  }
}
