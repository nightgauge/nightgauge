/**
 * KnowledgeSearchResultItem — leaf showing one recall hit in the Search or
 * Related section of KnowledgeTreeProvider. Reuses the wire-level
 * `KnowledgeRecallHit` shape returned by the IPC server (#2964).
 *
 * @see Issue #2964
 */

import * as vscode from "vscode";
import * as path from "node:path";
import { BaseTreeItem } from "./BaseTreeItem";
import type { KnowledgeRecallHit } from "../../services/IpcClientBase";

export class KnowledgeSearchResultItem extends BaseTreeItem {
  readonly hit: KnowledgeRecallHit;
  readonly absolutePath: string;

  constructor(hit: KnowledgeRecallHit, workspaceRoot: string) {
    const snippet = (hit.snippet ?? "").split("\n")[0].slice(0, 80);
    const label = snippet || path.basename(hit.path, ".md");
    super(label, vscode.TreeItemCollapsibleState.None);

    this.hit = hit;
    this.absolutePath = path.isAbsolute(hit.path) ? hit.path : path.join(workspaceRoot, hit.path);

    this.tooltip = hit.snippet || hit.path;
    this.description = hit.issue_number
      ? `#${hit.issue_number} • ${hit.score.toFixed(2)}`
      : hit.score.toFixed(2);
    this.contextValue = "knowledgeSearchResult";
    this.setIcon("history");

    this.command = {
      command: "vscode.open",
      title: "Open Knowledge File",
      arguments: [vscode.Uri.file(this.absolutePath)],
    };
  }
}
