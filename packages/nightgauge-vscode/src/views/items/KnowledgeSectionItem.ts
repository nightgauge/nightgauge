/**
 * KnowledgeSectionItem — collapsible section header for the three-section
 * KnowledgeTreeProvider model (Active Issue / Related / Search).
 *
 * @see Issue #2964
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";

export type KnowledgeSectionKind = "active-issue" | "related" | "search";

const ICONS: Record<KnowledgeSectionKind, string> = {
  "active-issue": "pinned",
  related: "link",
  search: "search",
};

export class KnowledgeSectionItem extends BaseTreeItem {
  readonly sectionKind: KnowledgeSectionKind;

  constructor(
    label: string,
    kind: KnowledgeSectionKind,
    initialState: vscode.TreeItemCollapsibleState = vscode.TreeItemCollapsibleState.Expanded
  ) {
    super(label, initialState);
    this.sectionKind = kind;
    this.contextValue = `knowledgeSection.${kind}`;
    this.setIcon(ICONS[kind]);
  }
}
