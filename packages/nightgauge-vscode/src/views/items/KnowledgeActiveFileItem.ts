/**
 * KnowledgeActiveFileItem — leaf for a single file under the "Active Issue"
 * section of KnowledgeTreeProvider. Distinct from the existing
 * ActiveIssueKnowledgeFileItem (which is owned by ActiveIssueKnowledgeProvider)
 * so the two panels can evolve independently.
 *
 * The `highlighted` flag is true when the file appears in `planning-{N}.json
 * .knowledge_read`. Highlighted items use the focus-border theme color so a
 * developer can see which files the agent already consumed.
 *
 * @see Issue #2964
 */

import * as vscode from "vscode";
import * as path from "node:path";
import { BaseTreeItem } from "./BaseTreeItem";

export class KnowledgeActiveFileItem extends BaseTreeItem {
  readonly filePath: string;
  readonly highlighted: boolean;

  constructor(filePath: string, highlighted: boolean) {
    const filename = path.basename(filePath);
    super(filename, vscode.TreeItemCollapsibleState.None);

    this.filePath = filePath;
    this.highlighted = highlighted;
    this.tooltip = highlighted ? `${filePath}\n(read during planning)` : filePath;
    this.contextValue = "knowledgeFile";
    this.description = highlighted ? "read" : undefined;

    const icon =
      filename === "PRD.md"
        ? "file-text"
        : filename === "decisions.md"
          ? "lightbulb"
          : filename === "outcomes.md"
            ? "checklist"
            : "file";
    if (highlighted) {
      this.iconPath = new vscode.ThemeIcon(icon, new vscode.ThemeColor("focusBorder"));
    } else {
      this.iconPath = new vscode.ThemeIcon(icon);
    }

    this.command = {
      command: "vscode.open",
      title: "Open Knowledge File",
      arguments: [vscode.Uri.file(filePath)],
    };
  }
}
