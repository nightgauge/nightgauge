/**
 * Tree item types for the Active Issue Knowledge panel.
 *
 * Three node types:
 *   - ActiveIssueKnowledgeSectionItem  — collapsible section header
 *   - ActiveIssueKnowledgeFileItem     — leaf that opens a markdown file
 *   - ActiveIssueKnowledgeRecallItem   — leaf showing a recall hit snippet
 *   - ActiveIssueKnowledgeEmptyItem    — informational leaf for empty states
 *
 * @see Issue #3599
 */

import * as vscode from "vscode";
import * as path from "node:path";
import { BaseTreeItem } from "../../views/items/BaseTreeItem";

// ---------------------------------------------------------------------------
// Section header
// ---------------------------------------------------------------------------

export type KnowledgeSectionKind = "prd" | "decisions" | "recall";

export class ActiveIssueKnowledgeSectionItem extends BaseTreeItem {
  readonly sectionKind: KnowledgeSectionKind;

  constructor(label: string, kind: KnowledgeSectionKind) {
    super(label, vscode.TreeItemCollapsibleState.Expanded);
    this.sectionKind = kind;
    this.contextValue = `activeKnowledgeSection.${kind}`;

    switch (kind) {
      case "prd":
        this.setIcon("file-text");
        break;
      case "decisions":
        this.setIcon("lightbulb");
        break;
      case "recall":
        this.setIcon("search");
        break;
    }
  }
}

// ---------------------------------------------------------------------------
// File leaf (PRD.md, decisions.md)
// ---------------------------------------------------------------------------

export class ActiveIssueKnowledgeFileItem extends BaseTreeItem {
  readonly filePath: string;

  constructor(filePath: string) {
    const filename = path.basename(filePath);
    super(filename, vscode.TreeItemCollapsibleState.None);
    this.filePath = filePath;
    this.tooltip = filePath;
    this.contextValue = "activeKnowledgeFile";

    if (filename === "PRD.md") {
      this.setIcon("file-text");
    } else if (filename === "decisions.md") {
      this.setIcon("lightbulb");
    } else {
      this.setIcon("file");
    }

    this.command = {
      command: "nightgauge.activeKnowledge.openFile",
      title: "Open Knowledge File",
      arguments: [filePath],
    };
  }
}

// ---------------------------------------------------------------------------
// Recall hit leaf
// ---------------------------------------------------------------------------

export interface RecallHit {
  path: string;
  snippet: string;
  score: number;
  issue_number?: number;
}

export class ActiveIssueKnowledgeRecallItem extends BaseTreeItem {
  readonly hit: RecallHit;

  constructor(hit: RecallHit) {
    const snippet = hit.snippet.split("\n")[0].slice(0, 80);
    const label = snippet || path.basename(hit.path, ".md");
    super(label, vscode.TreeItemCollapsibleState.None);
    this.hit = hit;
    this.tooltip = hit.snippet;
    this.description = hit.issue_number ? `#${hit.issue_number}` : undefined;
    this.contextValue = "activeKnowledgeRecallHit";
    this.setIcon("history");

    this.command = {
      command: "nightgauge.activeKnowledge.openFile",
      title: "Open Knowledge File",
      arguments: [hit.path],
    };
  }
}

// ---------------------------------------------------------------------------
// Empty / informational leaf
// ---------------------------------------------------------------------------

export class ActiveIssueKnowledgeEmptyItem extends BaseTreeItem {
  constructor(message: string) {
    super(message, vscode.TreeItemCollapsibleState.None);
    this.contextValue = "activeKnowledgeEmpty";
    this.setIcon("info");
  }
}
