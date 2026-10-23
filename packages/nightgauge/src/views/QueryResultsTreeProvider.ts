/**
 * QueryResultsTreeProvider - TreeDataProvider for query results
 *
 * Displays issues that match the current query in a tree view.
 * Supports grouping by epic, filtering, and multi-select.
 */

import * as vscode from "vscode";
import type { QueryResult, QueryableIssue } from "@nightgauge/sdk";
import { BaseTreeItem } from "./items/BaseTreeItem";
import { ReadyIssueTreeItem } from "./items/ReadyIssueTreeItem";
import type { QueryService } from "../services/QueryService";
import type { QueryContext } from "../types/QueryTypes";

/**
 * Action tree item for displaying status messages
 */
class QueryResultActionItem extends BaseTreeItem {
  constructor(label: string, icon: string, command?: vscode.Command) {
    super(label, vscode.TreeItemCollapsibleState.None);
    this.setIcon(icon);
    if (command) {
      this.command = command;
    }
  }
}

/**
 * Tree item for displaying a matched issue
 */
class QueryResultIssueItem extends BaseTreeItem {
  constructor(public readonly issue: QueryableIssue) {
    super(`#${issue.number} ${issue.title}`, vscode.TreeItemCollapsibleState.None);

    // Set description with priority and size
    const parts: string[] = [];
    if (issue.priority) parts.push(issue.priority);
    if (issue.size) parts.push(issue.size);
    if (issue.status) parts.push(issue.status);
    this.description = parts.join(" · ");

    // Set icon based on priority
    if (issue.priority === "P0") {
      this.setIcon("flame");
    } else if (issue.priority === "P1") {
      this.setIcon("arrow-up");
    } else {
      this.setIcon("circle-outline");
    }

    // Set tooltip
    this.tooltip = new vscode.MarkdownString();
    this.tooltip.appendMarkdown(`**#${issue.number}** ${issue.title}\n\n`);
    if (issue.priority) {
      this.tooltip.appendMarkdown(`Priority: ${issue.priority}\n\n`);
    }
    if (issue.size) {
      this.tooltip.appendMarkdown(`Size: ${issue.size}\n\n`);
    }
    if (issue.status) {
      this.tooltip.appendMarkdown(`Status: ${issue.status}\n\n`);
    }
    if (issue.labels.length > 0) {
      this.tooltip.appendMarkdown(`Labels: ${issue.labels.join(", ")}\n\n`);
    }

    // Set context value for menu contributions
    this.contextValue = "queryResultIssue";

    // Set command to open issue
    this.command = {
      command: "vscode.open",
      title: "Open Issue",
      arguments: [vscode.Uri.parse(issue.url)],
    };
  }
}

/**
 * QueryResultsTreeProvider - TreeDataProvider for query results
 *
 * @example
 * ```typescript
 * const provider = new QueryResultsTreeProvider(queryService);
 *
 * const treeView = vscode.window.createTreeView('nightgauge.queryResults', {
 *   treeDataProvider: provider,
 *   showCollapseAll: true,
 * });
 * ```
 */
export class QueryResultsTreeProvider
  implements vscode.TreeDataProvider<BaseTreeItem>, vscode.Disposable
{
  private _onDidChangeTreeData = new vscode.EventEmitter<BaseTreeItem | undefined | void>();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;

  private result: QueryResult | null = null;
  private query: string = "";
  private state: QueryContext["state"] = "idle";
  private error: string | undefined;
  private disposables: vscode.Disposable[] = [];

  constructor(private readonly queryService: QueryService) {
    // Listen for query state changes
    this.disposables.push(
      queryService.onQueryStateChanged((context) => {
        this.query = context.query;
        this.state = context.state;
        this.result = context.result ?? null;
        this.error = context.error;
        this.refresh();
      })
    );
  }

  /**
   * Refresh the tree view
   */
  refresh(): void {
    this._onDidChangeTreeData.fire();
  }

  /**
   * Get tree item for display
   */
  getTreeItem(element: BaseTreeItem): vscode.TreeItem {
    return element;
  }

  /**
   * Get children of an element
   */
  getChildren(element?: BaseTreeItem): Thenable<BaseTreeItem[]> {
    if (element) {
      return Promise.resolve([]);
    }

    return Promise.resolve(this.getRootChildren());
  }

  /**
   * Get root level children
   */
  private getRootChildren(): BaseTreeItem[] {
    // Handle different states
    switch (this.state) {
      case "idle":
        return [
          new QueryResultActionItem("Run a query to see results", "search", {
            command: "nightgauge.queryProjectItems",
            title: "Run Query",
          }),
        ];

      case "parsing":
      case "executing":
        return [new QueryResultActionItem("Executing query...", "sync~spin")];

      case "error":
        return [
          new QueryResultActionItem(`Error: ${this.error ?? "Unknown error"}`, "error"),
          new QueryResultActionItem("Try again", "refresh", {
            command: "nightgauge.queryProjectItems",
            title: "Run Query",
          }),
        ];

      case "complete":
        return this.getResultChildren();

      default:
        return [];
    }
  }

  /**
   * Get children for completed query results
   */
  private getResultChildren(): BaseTreeItem[] {
    if (!this.result) {
      return [];
    }

    const items: BaseTreeItem[] = [];

    // Add summary item
    const summary = new QueryResultActionItem(
      `${this.result.matchCount} of ${this.result.totalCount} issues`,
      "list-flat"
    );
    summary.description = `Query: ${this.query}`;
    items.push(summary);

    // Add separator
    if (this.result.items.length > 0) {
      // Add result items
      for (const issue of this.result.items) {
        items.push(new QueryResultIssueItem(issue));
      }
    } else {
      items.push(
        new QueryResultActionItem("No matching issues", "info", {
          command: "nightgauge.queryProjectItems",
          title: "Try Different Query",
        })
      );
    }

    return items;
  }

  /**
   * Get parent of an element
   */
  getParent(element: BaseTreeItem): vscode.ProviderResult<BaseTreeItem> {
    return null;
  }

  /**
   * Dispose of resources
   */
  dispose(): void {
    this._onDidChangeTreeData.dispose();
    this.disposables.forEach((d) => d.dispose());
  }
}
