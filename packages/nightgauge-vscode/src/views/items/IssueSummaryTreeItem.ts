/**
 * IssueSummaryTreeItem - Tree item for displaying issue count summaries
 *
 * Shows issue counts per status (Ready, In Progress, Done) as non-expandable
 * leaf nodes under a repository in the Repositories tree view.
 *
 * @see Issue #329 - Repositories Tree View
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";

/**
 * Issue count summary for display
 */
export interface IssueCounts {
  ready: number;
  inProgress: number;
  done?: number;
}

/**
 * Tree item displaying issue count summary
 *
 * @example
 * ```typescript
 * const item = new IssueSummaryTreeItem('ready', 3);
 * // Displays: "Ready: 3 issues"
 * ```
 */
export class IssueSummaryTreeItem extends BaseTreeItem {
  /** The status type this item represents */
  readonly statusType: "ready" | "inProgress" | "done" | "backlog" | "pipeline";

  /** The repository name this item belongs to */
  readonly repoName: string;

  /** The count of issues */
  readonly count: number;

  /** Optional pipeline stage info */
  readonly pipelineStage?: string;

  constructor(
    statusType: "ready" | "inProgress" | "done" | "backlog" | "pipeline",
    repoName: string,
    countOrStage: number | string,
    sortLabel?: string
  ) {
    const label = IssueSummaryTreeItem.getLabel(statusType, countOrStage);

    const collapsibleState =
      statusType === "pipeline"
        ? vscode.TreeItemCollapsibleState.None
        : vscode.TreeItemCollapsibleState.Collapsed;

    super(label, collapsibleState);

    this.repoName = repoName;

    this.statusType = statusType;

    if (typeof countOrStage === "number") {
      this.count = countOrStage;
    } else {
      this.count = 0;
      this.pipelineStage = countOrStage;
    }

    // Set contextValue for potential future actions
    this.contextValue = `issueSummary-${statusType}`;

    // Show sort indicator as description when non-default sort is active
    if (sortLabel) {
      this.description = `Sorted: ${sortLabel}`;
    }

    // Set icon based on status type
    this.setStatusIcon();

    // Set tooltip
    this.setTooltipText();
  }

  /**
   * Generate label for the tree item
   */
  private static getLabel(
    statusType: "ready" | "inProgress" | "done" | "backlog" | "pipeline",
    countOrStage: number | string
  ): string {
    if (statusType === "pipeline") {
      return `Pipeline: ${countOrStage}`;
    }

    const count = countOrStage as number;
    const statusLabel = IssueSummaryTreeItem.getStatusLabel(statusType);
    const issueWord = count === 1 ? "issue" : "issues";

    return `${statusLabel}: ${count} ${issueWord}`;
  }

  /**
   * Get human-readable status label
   */
  private static getStatusLabel(
    statusType: "ready" | "inProgress" | "done" | "backlog" | "pipeline"
  ): string {
    switch (statusType) {
      case "ready":
        return "Ready";
      case "inProgress":
        return "In Progress";
      case "done":
        return "Done";
      case "backlog":
        return "Backlog";
      case "pipeline":
        return "Pipeline";
    }
  }

  /**
   * Set the appropriate icon for this status type
   */
  private setStatusIcon(): void {
    switch (this.statusType) {
      case "ready":
        this.setIconWithColor("checklist", new vscode.ThemeColor("charts.green"));
        break;
      case "inProgress":
        this.setIconWithColor("sync", new vscode.ThemeColor("charts.blue"));
        break;
      case "done":
        this.setIconWithColor("check-all", new vscode.ThemeColor("testing.iconPassed"));
        break;
      case "backlog":
        this.setIconWithColor("list-unordered", new vscode.ThemeColor("charts.orange"));
        break;
      case "pipeline":
        if (this.pipelineStage && this.pipelineStage !== "idle") {
          this.setIconWithColor("sync~spin", new vscode.ThemeColor("charts.yellow"));
        } else {
          this.setIcon("circle-slash");
        }
        break;
    }
  }

  /**
   * Set the tooltip with context
   */
  private setTooltipText(): void {
    switch (this.statusType) {
      case "ready":
        this.tooltip = `${this.count} issues ready for development`;
        break;
      case "inProgress":
        this.tooltip = `${this.count} issues currently in progress`;
        break;
      case "done":
        this.tooltip = `${this.count} issues completed`;
        break;
      case "backlog":
        this.tooltip = `${this.count} issues in backlog`;
        break;
      case "pipeline":
        if (this.pipelineStage && this.pipelineStage !== "idle") {
          this.tooltip = `Pipeline is running: ${this.pipelineStage}`;
        } else {
          this.tooltip = "No active pipeline";
        }
        break;
    }
  }

  /**
   * Get the issue count
   */
  getCount(): number {
    return this.count;
  }

  /**
   * Get the status type
   */
  getStatusType(): string {
    return this.statusType;
  }

  /**
   * Get the pipeline stage (if applicable)
   */
  getPipelineStage(): string | undefined {
    return this.pipelineStage;
  }
}
