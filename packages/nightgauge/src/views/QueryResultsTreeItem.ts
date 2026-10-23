/**
 * QueryResultsTreeItem - Tree item types for displaying query results
 *
 * Provides specialized tree item classes for the QueryResultsTreeProvider:
 * - QueryResultSummaryItem: Shows match count and execution time
 * - QueryResultIssueItem: Shows a single matched issue
 * - QueryResultGroupItem: Groups results by status, priority, or component
 * - QueryResultActionItem: Displays status messages and actions
 */

import * as vscode from "vscode";
import type { QueryableIssue } from "@nightgauge/sdk";
import { BaseTreeItem } from "./items/BaseTreeItem";

/**
 * Tree item displaying a summary of query results
 *
 * Shows: match count, total count, execution time, query string.
 */
export class QueryResultSummaryItem extends BaseTreeItem {
  constructor(matchCount: number, totalCount: number, executionTimeMs: number, query: string) {
    super(`${matchCount} of ${totalCount} issues matched`, vscode.TreeItemCollapsibleState.None);

    this.description = `${executionTimeMs}ms`;
    this.tooltip = new vscode.MarkdownString();
    this.tooltip.appendMarkdown(`**Query:** \`${query}\`\n\n`);
    this.tooltip.appendMarkdown(`**Results:** ${matchCount} matched of ${totalCount} total\n\n`);
    this.tooltip.appendMarkdown(`**Execution time:** ${executionTimeMs}ms`);

    this.setIcon("list-flat");
    this.contextValue = "queryResultSummary";
  }
}

/**
 * Tree item representing a single issue in query results
 *
 * Displays priority icon, issue number, title, and metadata description.
 * Clicking opens the issue URL in the browser.
 */
export class QueryResultIssueItem extends BaseTreeItem {
  constructor(public readonly issue: QueryableIssue) {
    super(`#${issue.number} ${issue.title}`, vscode.TreeItemCollapsibleState.None);

    // Build description: priority · size · status
    const parts: string[] = [];
    if (issue.priority) {
      parts.push(issue.priority);
    }
    if (issue.size) {
      parts.push(issue.size);
    }
    if (issue.status) {
      parts.push(issue.status);
    }
    this.description = parts.join(" · ");

    // Priority-based icon
    switch (issue.priority) {
      case "P0":
        this.setIcon("flame");
        break;
      case "P1":
        this.setIcon("arrow-up");
        break;
      case "P2":
        this.setIcon("circle-filled");
        break;
      default:
        this.setIcon("circle-outline");
    }

    // Rich tooltip
    const tooltip = new vscode.MarkdownString();
    tooltip.appendMarkdown(`**#${issue.number}** ${issue.title}\n\n`);
    if (issue.priority) {
      tooltip.appendMarkdown(`**Priority:** ${issue.priority}\n\n`);
    }
    if (issue.size) {
      tooltip.appendMarkdown(`**Size:** ${issue.size}\n\n`);
    }
    if (issue.status) {
      tooltip.appendMarkdown(`**Status:** ${issue.status}\n\n`);
    }
    if (issue.assignee) {
      tooltip.appendMarkdown(`**Assignee:** ${issue.assignee}\n\n`);
    }
    if (issue.labels.length > 0) {
      tooltip.appendMarkdown(`**Labels:** ${issue.labels.join(", ")}\n\n`);
    }
    if (issue.updatedAt) {
      const updated = new Date(issue.updatedAt).toLocaleDateString();
      tooltip.appendMarkdown(`**Updated:** ${updated}\n\n`);
    }
    this.tooltip = tooltip;

    this.contextValue = "queryResultIssue";

    // Open issue on click
    this.command = {
      command: "vscode.open",
      title: "Open Issue",
      arguments: [vscode.Uri.parse(issue.url)],
    };
  }
}

/**
 * Tree item for grouping query results by a field value
 *
 * Example: group by status → "ready (3)", "in-progress (2)"
 */
export class QueryResultGroupItem extends BaseTreeItem {
  constructor(
    public readonly groupLabel: string,
    public readonly count: number,
    icon: string
  ) {
    super(groupLabel, vscode.TreeItemCollapsibleState.Expanded);
    this.description = `${count} issue${count !== 1 ? "s" : ""}`;
    this.setIcon(icon);
    this.contextValue = "queryResultGroup";
  }
}

/**
 * Tree item for displaying a status or action message
 *
 * Used for: idle state prompt, loading indicator, error messages, empty results.
 */
export class QueryResultActionItem extends BaseTreeItem {
  constructor(label: string, icon: string, command?: vscode.Command) {
    super(label, vscode.TreeItemCollapsibleState.None);
    this.setIcon(icon);
    if (command) {
      this.command = command;
    }
    this.contextValue = "queryResultAction";
  }
}

/**
 * Tree item showing a parse/evaluation error with detail
 *
 * Provides a clickable action to re-open the query dialog.
 */
export class QueryResultErrorItem extends BaseTreeItem {
  constructor(errorMessage: string) {
    super("Query Error", vscode.TreeItemCollapsibleState.None);
    this.description = errorMessage;

    const tooltip = new vscode.MarkdownString();
    tooltip.appendMarkdown(`**Error:** ${errorMessage}\n\n`);
    tooltip.appendMarkdown("Click to open the query dialog and try again.");
    this.tooltip = tooltip;

    this.setIcon("error");
    this.contextValue = "queryResultError";

    this.command = {
      command: "nightgauge.queryProjectItems",
      title: "Try Again",
    };
  }
}

/**
 * Tree item for pagination controls when result count exceeds the display limit
 *
 * Shows how many results are hidden and provides a "Load More" action.
 */
export class QueryResultLoadMoreItem extends BaseTreeItem {
  constructor(
    public readonly hiddenCount: number,
    public readonly totalCount: number,
    onLoadMore: () => void
  ) {
    super(`Show ${hiddenCount} more results`, vscode.TreeItemCollapsibleState.None);
    this.description = `${totalCount} total`;
    this.setIcon("chevron-down");
    this.contextValue = "queryResultLoadMore";

    // Inline command handler
    this.command = {
      command: "nightgauge.loadMoreQueryResults",
      title: "Load More",
    };
  }
}
