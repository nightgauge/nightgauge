/**
 * DependencyTreeItem - Tree item representing a blocking issue dependency
 *
 * Displays a blocking issue that must be completed before work can start
 * on a dependent issue. Shows issue number, title, and state (open/closed).
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";

/**
 * Blocking issue data from GitHub API
 */
export interface BlockingIssue {
  number: number;
  title: string;
  url: string;
  state: "OPEN" | "CLOSED";
}

/**
 * DependencyTreeItem - Represents a blocking issue in the tree view
 *
 * @example
 * ```typescript
 * const dependency = new DependencyTreeItem({
 *   number: 120,
 *   title: 'Complete user authentication',
 *   url: 'https://github.com/org/repo/issues/120',
 *   state: 'OPEN'
 * });
 * ```
 */
export class DependencyTreeItem extends BaseTreeItem {
  readonly issueNumber: number;
  readonly issueUrl: string;
  private blockingIssue: BlockingIssue;

  constructor(blockingIssue: BlockingIssue) {
    super(
      `#${blockingIssue.number} - ${blockingIssue.title}`,
      vscode.TreeItemCollapsibleState.None
    );

    this.issueNumber = blockingIssue.number;
    this.issueUrl = blockingIssue.url;
    this.blockingIssue = blockingIssue;

    this.setIconBasedOnState();
    this.contextValue = "dependency";
    this.description = this.createDescription();
    this.tooltip = this.createTooltip();

    // Click to view on GitHub
    this.command = {
      command: "nightgauge.viewIssueOnGitHub",
      title: "View on GitHub",
      arguments: [this],
    };
  }

  /**
   * Set icon based on issue state
   */
  private setIconBasedOnState(): void {
    if (this.blockingIssue.state === "CLOSED") {
      // Green checkmark for closed issues
      this.setIconWithColor("pass", new vscode.ThemeColor("testing.iconPassed"));
    } else {
      // Red circle for open (blocking) issues
      this.setIconWithColor("circle-filled", new vscode.ThemeColor("testing.iconFailed"));
    }
  }

  /**
   * Create description showing state
   */
  private createDescription(): string {
    return this.blockingIssue.state === "OPEN" ? "Open" : "Closed";
  }

  /**
   * Create rich tooltip with issue details
   */
  private createTooltip(): vscode.MarkdownString {
    const md = new vscode.MarkdownString();
    md.isTrusted = true;

    md.appendMarkdown(`**Blocking Issue:** #${this.blockingIssue.number}\n\n`);
    md.appendMarkdown(`**Title:** ${this.blockingIssue.title}\n\n`);
    md.appendMarkdown(`**State:** ${this.blockingIssue.state}\n\n`);

    if (this.blockingIssue.state === "OPEN") {
      md.appendMarkdown(`⚠️ This issue must be completed before work can begin.\n\n`);
    } else {
      md.appendMarkdown(`✅ This issue is closed and no longer blocks work.\n\n`);
    }

    md.appendMarkdown(`---\n\n`);
    md.appendMarkdown(`Click to view on GitHub`);

    return md;
  }

  /**
   * Get the underlying blocking issue data
   */
  getBlockingIssue(): BlockingIssue {
    return { ...this.blockingIssue };
  }
}
