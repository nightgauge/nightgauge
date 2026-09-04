/**
 * KnowledgeActiveFileItem — leaf for a single file under the "Active Issue"
 * section of KnowledgeTreeProvider. Distinct from the existing
 * ActiveIssueKnowledgeFileItem (removed with its provider in #1206)
 * so the two panels can evolve independently.
 *
 * The `highlighted` flag is true when the file appears in `planning-{N}.json
 * .knowledge_read`. Highlighted items use the focus-border theme color so a
 * developer can see which files the agent already consumed.
 *
 * `trustTier` is derived from the entry's `verified` log and shown in the
 * description, so the pile of entries nobody has checked is a reviewable list
 * rather than indistinguishable from confirmed ones.
 *
 * @see Issue #2964
 */

import * as vscode from "vscode";
import * as path from "node:path";
import { TRUST_UNVERIFIED, type TrustTier } from "@nightgauge/sdk";
import { BaseTreeItem } from "./BaseTreeItem";

export class KnowledgeActiveFileItem extends BaseTreeItem {
  readonly filePath: string;
  readonly highlighted: boolean;
  readonly trustTier: TrustTier;

  constructor(filePath: string, highlighted: boolean, trustTier: TrustTier = TRUST_UNVERIFIED) {
    const filename = path.basename(filePath);
    super(filename, vscode.TreeItemCollapsibleState.None);

    this.filePath = filePath;
    this.highlighted = highlighted;
    this.trustTier = trustTier;

    // The tier is the point of this row: it says whether a model wrote the
    // entry and nobody checked. It leads the description so the unverified
    // backlog is scannable without opening anything.
    const parts: string[] = [trustTier];
    if (highlighted) parts.push("read");
    this.description = parts.join(" · ");

    const tierNote =
      trustTier === TRUST_UNVERIFIED
        ? "\n(unverified — nothing has confirmed this entry)"
        : `\n(${trustTier})`;
    this.tooltip = highlighted
      ? `${filePath}${tierNote}\n(read during planning)`
      : `${filePath}${tierNote}`;
    this.contextValue =
      trustTier === TRUST_UNVERIFIED ? "knowledgeFileUnverified" : "knowledgeFile";

    // An unverified entry gets its own icon rather than only a colour, so the
    // distinction survives a colour-blind reader and a high-contrast theme.
    const baseIcon =
      filename === "PRD.md" ? "file-text" : filename === "decisions.md" ? "lightbulb" : "file";
    const icon = trustTier === TRUST_UNVERIFIED ? "question" : baseIcon;
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
