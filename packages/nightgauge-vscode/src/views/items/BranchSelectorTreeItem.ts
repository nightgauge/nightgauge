/**
 * BranchSelectorTreeItem - Tree item for target branch selection
 *
 * Shows the current target branch and allows quick access to change it.
 * Matches the RepositorySwitcher pattern for consistent UX.
 *
 * @see Issue #102 - VSCode Branch Selection UX
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";

/**
 * BranchSelectorTreeItem - Shows and allows changing target branch
 *
 * @example
 * ```typescript
 * const branchSelector = new BranchSelectorTreeItem('main');
 * branchSelector.update('develop'); // Update to new target branch
 * ```
 */
export class BranchSelectorTreeItem extends BaseTreeItem {
  private currentBranch: string;
  private isProtected: boolean;

  constructor(baseBranch: string = "main", isProtected: boolean = false) {
    super("Target Branch", vscode.TreeItemCollapsibleState.None);

    this.currentBranch = baseBranch;
    this.isProtected = isProtected;

    this.contextValue = "branch-selector";
    this.updateDisplay();

    // Click to change branch
    this.command = {
      command: "nightgauge.selectTargetBranch",
      title: "Select Target Branch",
      arguments: [],
    };
  }

  /**
   * Update the display based on current state
   */
  private updateDisplay(): void {
    // Show branch name with appropriate icon
    const protectedIcon = this.isProtected ? "$(lock) " : "";
    this.label = `Target: ${protectedIcon}${this.currentBranch}`;
    this.description = "Click to change";

    // Use git-branch icon
    this.setIcon("git-branch");

    this.tooltip = this.createTooltip();
  }

  /**
   * Create tooltip with branch details
   */
  private createTooltip(): vscode.MarkdownString {
    const md = new vscode.MarkdownString();
    md.appendMarkdown(`**Target Branch**\n\n`);
    md.appendMarkdown(`Current: \`${this.currentBranch}\`\n\n`);

    if (this.isProtected) {
      md.appendMarkdown(`$(lock) This branch is protected\n\n`);
    }

    md.appendMarkdown(`---\n\n`);
    md.appendMarkdown(`Click to select a different target branch`);

    return md;
  }

  /**
   * Update the branch display
   */
  update(baseBranch: string, isProtected: boolean = false): void {
    this.currentBranch = baseBranch;
    this.isProtected = isProtected;
    this.updateDisplay();
  }

  /**
   * Get the current branch name
   */
  getBranch(): string {
    return this.currentBranch;
  }
}
