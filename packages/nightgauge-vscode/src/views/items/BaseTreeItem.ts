/**
 * BaseTreeItem - Base class for pipeline tree items
 *
 * Provides common functionality for all tree items in the pipeline sidebar.
 */

import * as vscode from "vscode";

/**
 * Base class for all pipeline tree items
 *
 * Extends vscode.TreeItem with common utility methods and type safety.
 */
export abstract class BaseTreeItem extends vscode.TreeItem {
  /**
   * Children of this tree item (for collapsible items)
   */
  protected children: BaseTreeItem[] = [];

  constructor(
    label: string,
    collapsibleState: vscode.TreeItemCollapsibleState = vscode.TreeItemCollapsibleState.None
  ) {
    super(label, collapsibleState);
  }

  /**
   * Get children of this tree item
   */
  getChildren(): BaseTreeItem[] {
    return this.children;
  }

  /**
   * Add a child tree item
   */
  addChild(child: BaseTreeItem): void {
    this.children.push(child);
  }

  /**
   * Clear all children
   */
  clearChildren(): void {
    this.children = [];
  }

  /**
   * Set the icon for this tree item using a codicon
   */
  protected setIcon(codicon: string): void {
    this.iconPath = new vscode.ThemeIcon(codicon);
  }

  /**
   * Set the icon with a color
   */
  protected setIconWithColor(codicon: string, color: vscode.ThemeColor): void {
    this.iconPath = new vscode.ThemeIcon(codicon, color);
  }
}
