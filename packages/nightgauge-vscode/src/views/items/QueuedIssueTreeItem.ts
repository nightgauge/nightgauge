/**
 * QueuedIssueTreeItem - Tree item for queued issues
 *
 * Represents a single issue in the queue, displayed in the pipeline tree view.
 * Shows position, title, and provides context menu actions for queue management.
 *
 * @see Issue #236 - Queue Issues When Pipeline Active
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";
import type { QueueItem, QueueItemPausedReason, QueueItemStatus } from "../../types/queue";

/**
 * Human-readable description of a paused-reason kind for screen readers.
 */
function pausedReasonAriaLabel(r: QueueItemPausedReason): string {
  if (r.kind === "upstream_failure") return "upstream pipeline failure";
  if (r.kind === "baseline_ci_red") return "baseline CI red on main";
  if (r.kind === "blocked_dependency") return "blocked by an open dependency";
  return (r as { kind: string }).kind;
}
import { isBlocked, getBlockerTitles } from "../../utils/dependencyUtils";
import type { ReadyIssue } from "../../services/ProjectBoardService";

/**
 * Get icon for queue item status
 */
function getStatusIcon(status: QueueItemStatus): vscode.ThemeIcon {
  switch (status) {
    case "processing":
      return new vscode.ThemeIcon("sync~spin", new vscode.ThemeColor("charts.yellow"));
    case "ready":
      return new vscode.ThemeIcon("play-circle", new vscode.ThemeColor("charts.green"));
    case "completed":
      return new vscode.ThemeIcon("check", new vscode.ThemeColor("charts.green"));
    case "failed":
      return new vscode.ThemeIcon("error", new vscode.ThemeColor("charts.red"));
    // Issue #3001: paused = item waiting behind a terminal failure. Use the
    // debug-pause codicon (matches the queue-section pause icon) tinted orange
    // to distinguish from "blocked" (red lock) and "ready" (green).
    case "paused":
      return new vscode.ThemeIcon("debug-pause", new vscode.ThemeColor("charts.orange"));
    case "pending":
    default:
      return new vscode.ThemeIcon("clock", new vscode.ThemeColor("charts.blue"));
  }
}

/**
 * QueuedIssueTreeItem - Tree item representing a queued issue
 *
 * @example
 * ```typescript
 * const item = new QueuedIssueTreeItem({
 *   issueNumber: 42,
 *   title: 'Add dark mode',
 *   position: 1,
 *   status: 'pending',
 *   addedAt: new Date().toISOString(),
 * });
 * ```
 */
export class QueuedIssueTreeItem extends BaseTreeItem {
  readonly issueNumber: number;
  private queueItem: QueueItem;

  constructor(item: QueueItem) {
    // Check blocked status (Issue #823)
    const itemIsBlocked = QueuedIssueTreeItem.isItemBlocked(item);

    const labelSuffix = itemIsBlocked ? " (blocked)" : "";
    const label = `#${item.issueNumber} - ${item.title}${labelSuffix}`;

    super(label, vscode.TreeItemCollapsibleState.None);

    this.queueItem = item;
    this.issueNumber = item.issueNumber;

    // Set description: show blocker count if blocked, then queue position (Issue #823)
    this.description = this.createDescription(item, itemIsBlocked);

    // Set tooltip with details and blocker info (Issue #823)
    this.tooltip = this.createTooltip(item, itemIsBlocked);

    // Set icon: lock for blocked, otherwise status-based (Issue #823)
    if (itemIsBlocked) {
      this.setIconWithColor("lock", new vscode.ThemeColor("problemsErrorIcon.foreground"));
    } else {
      this.iconPath = getStatusIcon(item.status);
    }

    // Set context value for context menu (status-specific for contextual actions)
    this.contextValue = `queuedIssue.${item.status}`;

    // Add ARIA-friendly accessible description for screen readers (Issue #304)
    this.accessibilityInformation = {
      label: this.createAccessibilityLabel(),
      role: "treeitem",
    };

    // Set command for clicking (view issue on GitHub)
    this.command = {
      title: "View Issue on GitHub",
      command: "nightgauge.viewQueuedIssue",
      arguments: [this],
    };
  }

  /**
   * Create accessibility label for screen readers (Issue #304)
   *
   * Format: "Issue #304 in pipeline queue. Position 2 of 5. Status: pending. Press Ctrl+Shift+Up to move up, Ctrl+Shift+R to remove."
   */
  private createAccessibilityLabel(): string {
    const parts: string[] = [
      `Issue #${this.queueItem.issueNumber} in pipeline queue.`,
      `Position ${this.queueItem.position}.`,
      `Status: ${this.queueItem.status}.`,
      ...(QueuedIssueTreeItem.isItemBlocked(this.queueItem)
        ? [
            `Blocked by ${this.queueItem.blockedBy!.filter((b) => b.state === "OPEN").length} issue${this.queueItem.blockedBy!.filter((b) => b.state === "OPEN").length === 1 ? "" : "s"}.`,
          ]
        : []),
      // Issue #3001 — surface paused-reason for screen reader users.
      ...(this.queueItem.status === "paused" && this.queueItem.pausedReason
        ? [
            `Paused due to ${pausedReasonAriaLabel(this.queueItem.pausedReason)}. ${
              this.queueItem.pausedReason.kind === "baseline_ci_red"
                ? "Auto-resumes when the baseline goes green."
                : this.queueItem.pausedReason.kind === "blocked_dependency"
                  ? "Auto-resumes when its blockers close."
                  : "Resumes only via operator action."
            }`,
          ]
        : []),
    ];

    // Add keyboard hints based on status
    if (this.queueItem.status === "pending" || this.queueItem.status === "ready") {
      const hints: string[] = [];
      if (this.queueItem.position > 1) {
        hints.push("Ctrl+Shift+Up to move up");
      }
      hints.push("Ctrl+Shift+Down to move down");
      hints.push("Ctrl+Shift+R to remove");

      parts.push(`Press ${hints.join(", ")}.`);
    }

    return parts.join(" ");
  }

  /**
   * Get the underlying queue item
   */
  getQueueItem(): QueueItem {
    return this.queueItem;
  }

  /**
   * Create description for single queue items
   * Shows blocker count prefix when blocked (Issue #823)
   * Shows [repo-name] prefix for cross-repo items (Issue #2188)
   * Shows paused indicator when status is paused (Issue #3001)
   */
  private createDescription(item: QueueItem, itemIsBlocked: boolean): string {
    const parts: string[] = [];

    if (item.repoName) {
      parts.push(`[${item.repoName}]`);
    }

    if (itemIsBlocked) {
      const asReadyIssue = QueuedIssueTreeItem.toReadyIssue(item);
      const blockerTitles = getBlockerTitles(asReadyIssue);
      const count = blockerTitles.length;
      parts.push(`🔒${count} blocker${count === 1 ? "" : "s"}`);
    }

    // Issue #3001 / #3004 — paused item: surface the cause inline so operators
    // don't have to expand the tooltip to see why dispatch is blocked.
    if (item.status === "paused" && item.pausedReason) {
      const r = item.pausedReason;
      let label: string;
      if (r.kind === "upstream_failure") {
        label = "paused: upstream failure";
      } else if (r.kind === "baseline_ci_red") {
        label = "paused: baseline-CI red";
      } else if (r.kind === "blocked_dependency") {
        const first = r.blockingIssues[0];
        label = first ? `paused: blocked by #${first.number}` : "paused: blocked by dependency";
      } else {
        // Defensive: future variants should fall through to a generic label.
        label = `paused: ${(r as { kind: string }).kind}`;
      }
      parts.push(label);
    }

    parts.push(`Position ${item.position}`);

    return parts.join(" · ");
  }

  /**
   * Create tooltip for single queue items
   * Includes blocker details when blocked (Issue #823)
   */
  private createTooltip(item: QueueItem, itemIsBlocked: boolean): vscode.MarkdownString {
    let text = `**Issue #${item.issueNumber}**\n\n` + `${item.title}\n\n`;

    if (itemIsBlocked && item.blockedBy) {
      text += `**🔒 Blocked By:**\n\n`;
      for (const blocker of item.blockedBy) {
        const stateIcon = blocker.state === "OPEN" ? "🔴" : "✅";
        text += `- ${stateIcon} #${blocker.number}: ${blocker.title}\n`;
      }
      text += `\n`;
    }

    // Issue #3001 / #3004 — paused-reason details. Each `kind` carries different
    // evidence; render per-variant so operators get actionable context.
    if (item.status === "paused" && item.pausedReason) {
      const r = item.pausedReason;
      text += `**⏸ Paused**\n\n`;
      if (r.kind === "upstream_failure") {
        text += `Reason: upstream failure\n`;
        text += `Failed run: \`${r.failed_run_id}\`\n`;
        if (r.summary) {
          text += `Summary: ${r.summary}\n`;
        }
        text += `\nResumes only via explicit operator action (Retry / Skip / Discard).\n\n`;
      } else if (r.kind === "baseline_ci_red") {
        text += `Reason: baseline-CI red\n`;
        text += `Workflow: \`${r.workflow}\`\n`;
        if (r.job) {
          text += `Job: \`${r.job}\`\n`;
        }
        if (typeof r.failed_runs === "number" && typeof r.lookback_runs === "number") {
          text += `Failed: ${r.failed_runs}/${r.lookback_runs} recent runs\n`;
        }
        if (r.summary) {
          text += `Summary: ${r.summary}\n`;
        }
        text += `\nDaily \`baseline-defer-sweep\` cron auto-resumes this item when the baseline goes green.\n\n`;
      } else if (r.kind === "blocked_dependency") {
        text += `Reason: blocked by open dependency\n`;
        for (const b of r.blockingIssues) {
          text += `- 🔴 #${b.number}${b.title ? `: ${b.title}` : ""}\n`;
        }
        if (r.summary) {
          text += `Summary: ${r.summary}\n`;
        }
        text += `\n\`deps-gate promote\` (and the autonomous cascade) auto-resumes this item when its blockers close.\n\n`;
      }
    }

    text +=
      `Queue Position: ${item.position}\n` +
      `Status: ${item.status}\n` +
      `Added: ${new Date(item.addedAt).toLocaleString()}`;

    return new vscode.MarkdownString(text);
  }

  /**
   * Convert a QueueItem to a minimal ReadyIssue shape for dependency utils
   */
  private static toReadyIssue(item: QueueItem): ReadyIssue {
    return {
      number: item.issueNumber,
      title: item.title,
      labels: item.labels ?? [],
      url: "",
      priority: null,
      size: null,
      blockedBy: item.blockedBy,
    };
  }

  /**
   * Check if a queue item is blocked
   */
  private static isItemBlocked(item: QueueItem): boolean {
    if (!item.blockedBy || item.blockedBy.length === 0) {
      return false;
    }
    return isBlocked(QueuedIssueTreeItem.toReadyIssue(item));
  }

  /**
   * Update the queue item
   */
  update(item: QueueItem): void {
    this.queueItem = item;
    const itemIsBlocked = QueuedIssueTreeItem.isItemBlocked(item);
    const labelSuffix = itemIsBlocked ? " (blocked)" : "";
    this.label = `#${item.issueNumber} - ${item.title}${labelSuffix}`;
    this.description = this.createDescription(item, itemIsBlocked);
    this.tooltip = this.createTooltip(item, itemIsBlocked);

    if (itemIsBlocked) {
      this.setIconWithColor("lock", new vscode.ThemeColor("problemsErrorIcon.foreground"));
    } else {
      this.iconPath = getStatusIcon(item.status);
    }

    this.contextValue = `queuedIssue.${item.status}`;
  }
}
