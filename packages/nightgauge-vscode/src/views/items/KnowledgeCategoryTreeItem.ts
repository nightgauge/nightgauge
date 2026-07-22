/**
 * KnowledgeCategoryTreeItem - Tree item representing a knowledge category
 *
 * Displays a top-level category (Epics, Features, Architecture, Glossary)
 * in the Knowledge tree view. Contains KnowledgeEntryTreeItem children.
 *
 * @see Issue #1686 - Implement KnowledgeTreeProvider
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";

export class KnowledgeCategoryTreeItem extends BaseTreeItem {
  readonly categoryKey: string;

  constructor(
    label: string,
    categoryKey: string,
    entryCount: number,
    collapsibleState = vscode.TreeItemCollapsibleState.Collapsed
  ) {
    super(label, collapsibleState);
    this.categoryKey = categoryKey;
    this.description = entryCount > 0 ? `(${entryCount})` : "";
    this.contextValue = "knowledgeCategory";
    this.setIcon(entryCount > 0 ? "folder-opened" : "folder");
  }
}
