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

  constructor() {
    super("Queued Issues", vscode.TreeItemCollapsibleState.Collapsed);

    this.description = "(0)";
    this.iconPath = getQueueStatusIcon("idle", 0);
    this.contextValue = "queueSection";

    // Tooltip with section description
    this.updateTooltip();
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
      tooltipText += `**${this.items.length}** issue(s) in queue\n`;
      tooltipText += `Status: ${this.queueStatus}`;
      if (this.pauseReason) {
        tooltipText += `\n\n⚠️ ${this.pauseReason}`;
      }
    }

    this.tooltip = new vscode.MarkdownString(tooltipText);
  }

  /**
   * Set the queue items
   */
  setItems(queueItems: QueueItem[]): void {
    this.queueItems = queueItems;
    this.items = queueItems.map((item) => new QueuedIssueTreeItem(item));
    this.description = `(${this.items.length})`;
    this.iconPath = getQueueStatusIcon(this.queueStatus, this.items.length);
    this.updateTooltip();

    // Auto-expand when items are added
    if (this.items.length > 0) {
      this.collapsibleState = vscode.TreeItemCollapsibleState.Expanded;
    }
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
    this.queueStatus = "idle";
    this.pauseReason = undefined;
    this.description = "(0)";
    this.iconPath = getQueueStatusIcon("idle", 0);
    this.collapsibleState = vscode.TreeItemCollapsibleState.Collapsed;
    this.updateTooltip();
  }
}
