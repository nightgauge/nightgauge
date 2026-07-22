/**
 * KnowledgeEntryTreeItem - Tree item representing a single knowledge entry
 *
 * Leaf item in the Knowledge tree view. Clicking opens the markdown file
 * in the editor. Shows title from frontmatter or filename, with issue
 * number and type-based icon.
 *
 * @see Issue #1686 - Implement KnowledgeTreeProvider
 */

import * as vscode from "vscode";
import * as path from "node:path";
import { BaseTreeItem } from "./BaseTreeItem";
import type { KnowledgeListEntry } from "@nightgauge/sdk";

export class KnowledgeEntryTreeItem extends BaseTreeItem {
  readonly filePath: string;
  readonly entry: KnowledgeListEntry;

  /**
   * @param listEntry - Knowledge list entry to display
   * @param activeRepoSlug - Slug of the currently active repo (for workspace scoping highlight)
   */
  constructor(listEntry: KnowledgeListEntry, activeRepoSlug?: string) {
    const label = listEntry.entry?.title ?? path.basename(listEntry.filePath, ".md");
    super(label, vscode.TreeItemCollapsibleState.None);

    this.filePath = listEntry.filePath;
    this.entry = listEntry;

    if (listEntry.entry === null) {
      // No frontmatter — scaffolded files are plain markdown; show neutral icon
      this.tooltip = listEntry.relativePath;
      this.setIcon("file");
      this.contextValue = "knowledgeEntry";
    } else {
      const type = listEntry.entry.type;

      // Build description: issue number + repo badges
      const issuePart = (() => {
        const m = listEntry.relativePath.match(/\/(\d+)-[^/]+\//);
        return m ? `#${m[1]}` : "";
      })();

      const repos: string[] = Array.isArray(listEntry.entry.repos) ? listEntry.entry.repos : [];
      const reposPart = repos.length > 0 ? `[${repos.join(", ")}]` : "";
      const desc = [issuePart, reposPart].filter(Boolean).join(" ");
      if (desc) this.description = desc;

      this.tooltip = listEntry.relativePath;

      // Highlight when active editor is in a scoped repo
      const isHighlighted = activeRepoSlug !== undefined && repos.includes(activeRepoSlug);

      if (isHighlighted) {
        this.setIconWithColor(
          type === "prd"
            ? "file-text"
            : type === "decision" || type === "adr"
              ? "lightbulb"
              : "file",
          new vscode.ThemeColor("focusBorder")
        );
      } else {
        this.setIcon(
          type === "prd"
            ? "file-text"
            : type === "decision" || type === "adr"
              ? "lightbulb"
              : type === "note"
                ? "note"
                : "file"
        );
      }
      this.contextValue = "knowledgeEntry";
    }

    // Click opens PRD.md when available, otherwise the file itself
    const targetFile = this.resolveOpenTarget(listEntry);
    this.command = {
      command: "vscode.open",
      title: "Open Knowledge Entry",
      arguments: [vscode.Uri.file(targetFile)],
    };
  }

  /** Resolve the best file to open: PRD.md if this is a directory entry, otherwise the file */
  private resolveOpenTarget(listEntry: KnowledgeListEntry): string {
    // If the entry is already PRD.md, open it directly
    if (path.basename(listEntry.filePath) === "PRD.md") {
      return listEntry.filePath;
    }
    // If a sibling PRD.md exists at the same level, prefer it
    const prdPath = path.join(path.dirname(listEntry.filePath), "PRD.md");
    // Return PRD.md path speculatively — VSCode will fall back gracefully if missing
    return prdPath;
  }
}
