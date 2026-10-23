/**
 * KnowledgeRepositoryTreeItem - Tree item for per-repo grouping in aggregate mode
 *
 * Displays a repository as a collapsible node in the Knowledge tree when
 * `knowledge.aggregate: true` is configured in a multi-repo workspace. Children
 * are loaded lazily on first expand to avoid scanning all repos at startup.
 *
 * @see Issue #1698 - Aggregate multi-repo knowledge in VSCode Knowledge Explorer
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";

export class KnowledgeRepositoryTreeItem extends BaseTreeItem {
  readonly repoName: string;
  readonly repoPath: string;

  private lazyLoader?: () => Promise<BaseTreeItem[]>;
  private loadedChildren: BaseTreeItem[] | null = null;

  constructor(repoName: string, repoPath: string) {
    super(repoName, vscode.TreeItemCollapsibleState.Collapsed);
    this.repoName = repoName;
    this.repoPath = repoPath;
    this.contextValue = "knowledgeRepository";
    this.setIcon("repo");
    this.tooltip = repoPath;
  }

  /**
   * Set a lazy loader that provides children on first expand.
   * Called by KnowledgeTreeProvider after constructing the item.
   */
  setLazyLoader(loader: () => Promise<BaseTreeItem[]>): void {
    this.lazyLoader = loader;
  }

  /**
   * Load children asynchronously, caching after first load.
   *
   * Called by KnowledgeTreeProvider.getChildren() when this item is the element.
   * Using a separate async method instead of overriding getChildren() (which is
   * synchronous in BaseTreeItem) avoids a return-type mismatch.
   */
  async loadChildren(): Promise<BaseTreeItem[]> {
    if (this.loadedChildren !== null) return this.loadedChildren;
    if (this.lazyLoader) {
      this.loadedChildren = await this.lazyLoader();
      const total = this.loadedChildren.reduce((sum, item) => sum + item.getChildren().length, 0);
      this.description = total > 0 ? `(${total})` : "";
    }
    return this.loadedChildren ?? [];
  }

  /**
   * Clear the cached children so the next loadChildren() call re-fetches.
   */
  clearCache(): void {
    this.loadedChildren = null;
    this.description = "";
  }
}
