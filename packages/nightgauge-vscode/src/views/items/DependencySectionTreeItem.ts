/**
 * DependencySectionTreeItem - Tree item representing a dependency section header
 *
 * Displays "Blocked by:" or "Blocks:" section headers in the dependency tree,
 * grouping blocking and blocked issues separately.
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";
import { DependencyTreeItem, type BlockingIssue } from "./DependencyTreeItem";

/**
 * Section type for dependency grouping
 */
export type DependencySectionType = "blockedBy" | "blocks";

/**
 * DependencySectionTreeItem - Section header for dependency groups
 *
 * @example
 * ```typescript
 * const blockedBySection = new DependencySectionTreeItem('blockedBy', blockingIssues);
 * const blocksSection = new DependencySectionTreeItem('blocks', blockedIssues);
 * ```
 */
export class DependencySectionTreeItem extends BaseTreeItem {
  readonly sectionType: DependencySectionType;

  constructor(sectionType: DependencySectionType, issues: BlockingIssue[]) {
    const label = sectionType === "blockedBy" ? "Blocked by" : "Blocks";
    super(label, vscode.TreeItemCollapsibleState.Expanded);

    this.sectionType = sectionType;
    this.contextValue = "dependencySection";
    this.description = `(${issues.length})`;

    // Set icon based on section type
    if (sectionType === "blockedBy") {
      this.setIconWithColor("lock", new vscode.ThemeColor("problemsWarningIcon.foreground"));
    } else {
      this.setIconWithColor("link", new vscode.ThemeColor("charts.blue"));
    }

    // Create tooltip
    this.tooltip = this.createTooltip(sectionType, issues);

    // Add dependency children
    for (const issue of issues) {
      this.addChild(new DependencyTreeItem(issue));
    }
  }

  /**
   * Create tooltip explaining the section
   */
  private createTooltip(
    sectionType: DependencySectionType,
    issues: BlockingIssue[]
  ): vscode.MarkdownString {
    const md = new vscode.MarkdownString();
    md.isTrusted = true;

    if (sectionType === "blockedBy") {
      md.appendMarkdown(`**Blocked by ${issues.length} issue(s)**\n\n`);
      md.appendMarkdown(`These issues must be completed first:\n\n`);
    } else {
      md.appendMarkdown(`**Blocks ${issues.length} issue(s)**\n\n`);
      md.appendMarkdown(`Complete this issue to unblock:\n\n`);
    }

    for (const issue of issues) {
      const stateIcon = issue.state === "OPEN" ? "🔴" : "✅";
      md.appendMarkdown(`${stateIcon} #${issue.number}: ${issue.title}\n`);
    }

    return md;
  }
}
