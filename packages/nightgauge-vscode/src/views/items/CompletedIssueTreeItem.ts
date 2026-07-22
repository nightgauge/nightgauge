/**
 * CompletedIssueTreeItem - Tree item for completed pipeline issues
 *
 * Shows completed issues with checkmark icon and timestamp.
 * Collapsed by default to reduce UI clutter.
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";
import type { IssueReference } from "../../types/completedIssues";

/**
 * CompletedIssueTreeItem - Represents a completed issue in the tree
 *
 * @example
 * ```typescript
 * const item = new CompletedIssueTreeItem({
 *   issue_number: 42,
 *   title: 'Add dark mode',
 *   branch: 'feat/42-dark-mode',
 *   timestamp: '2026-02-06T10:30:00Z'
 * });
 * ```
 */
export class CompletedIssueTreeItem extends BaseTreeItem {
  readonly issue: IssueReference;

  constructor(issue: IssueReference) {
    super(`#${issue.issue_number}: ${issue.title}`, vscode.TreeItemCollapsibleState.None);

    this.issue = issue;

    // Set icon with green theme color
    this.setIconWithColor("check-all", new vscode.ThemeColor("testing.iconPassed"));

    // Show timestamp and size badge in description
    this.description = this.createDescription();

    // Set tooltip
    this.tooltip = this.createTooltip();

    // Context value for menu visibility
    this.contextValue = "completed-issue";
  }

  /**
   * Build description: relative timestamp with optional size badge and cost anomaly badge.
   * Format: "2h ago [M]" or "2h ago [M] [$]" when cost anomaly exceeded
   */
  private createDescription(): string {
    const ts = this.formatTimestamp(this.issue.timestamp);
    const size = this.parseSize();
    const costBadge = this.issue.cost_anomaly_exceeded ? " [$]" : "";
    return size ? `${ts} [${size}]${costBadge}` : `${ts}${costBadge}`;
  }

  /**
   * Parse size from labels (e.g. "size:M" → "M").
   */
  private parseSize(): string | undefined {
    const labels = this.issue.labels ?? [];
    for (const label of labels) {
      if (label.startsWith("size:")) {
        const value = label.slice("size:".length).toUpperCase();
        if (["XS", "S", "M", "L", "XL"].includes(value)) {
          return value;
        }
      }
    }
    return undefined;
  }

  /**
   * Format timestamp as relative time
   */
  private formatTimestamp(timestamp: string): string {
    try {
      const date = new Date(timestamp);
      const now = new Date();
      const diffMs = now.getTime() - date.getTime();
      const diffMinutes = Math.floor(diffMs / 60000);

      if (diffMinutes < 1) {
        return "just now";
      } else if (diffMinutes < 60) {
        return `${diffMinutes}m ago`;
      } else if (diffMinutes < 1440) {
        const hours = Math.floor(diffMinutes / 60);
        return `${hours}h ago`;
      } else {
        const days = Math.floor(diffMinutes / 1440);
        return `${days}d ago`;
      }
    } catch {
      return "";
    }
  }

  /**
   * Create tooltip with full details
   */
  private createTooltip(): vscode.MarkdownString {
    const md = new vscode.MarkdownString();
    md.isTrusted = true;

    md.appendMarkdown(`**Issue #${this.issue.issue_number}**\n\n`);
    md.appendMarkdown(`${this.issue.title}\n\n`);
    md.appendMarkdown(`**Branch:** ${this.issue.branch}\n\n`);
    md.appendMarkdown(`**Completed:** ${this.issue.timestamp}\n\n`);
    md.appendMarkdown(`✓ Pipeline completed successfully`);

    if (this.issue.cost_anomaly_exceeded) {
      md.appendMarkdown(`\n\n⚠️ Cost exceeded anomaly threshold`);
    }

    return md;
  }
}
