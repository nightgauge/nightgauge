/**
 * KnowledgeSearchResultItem — leaf showing one recall hit in the Search or
 * Related section of KnowledgeTreeProvider. Reuses the wire-level
 * `KnowledgeRecallHit` shape returned by the IPC server (#2964).
 *
 * Rows used to read `decisions 0.69` — the file's basename and a bare BM25
 * score, with nothing telling the operator what either was (#1207). The score
 * was the row's description; the label fell back to the basename because the
 * snippet was empty, and every knowledge base names its files the same two
 * things, so every row read `PRD` or `decisions`.
 *
 * A row now leads with WHAT it is (the knowledge base it belongs to, with the
 * issue number when there is one) and describes it with the matching line. The
 * score is a ranking artifact, not information the operator can act on, so it
 * moves into the tooltip with a label.
 *
 * @see Issue #2964, #1207
 */

import * as vscode from "vscode";
import * as path from "node:path";
import { BaseTreeItem } from "./BaseTreeItem";
import type { KnowledgeRecallHit } from "../../services/IpcClientBase";

const SNIPPET_MAX = 100;

/**
 * The knowledge base a hit belongs to, as a human would name it.
 *
 * Two shapes, because the informative part of the path is in a different place
 * in each:
 *
 *   features/390-cost-cache-token-counts/decisions.md  →  cost cache token counts
 *   workspace/architecture/forge-abstraction.md        →  forge abstraction
 *
 * An issue-scoped knowledge base puts the topic in the DIRECTORY and names
 * every file inside it `PRD` or `decisions`; a workspace document puts the
 * topic in the FILENAME and sits in a directory named for its category. Using
 * the filename everywhere gives `decisions` for half the corpus — which is
 * exactly what the rows read before this (#1207) — and using the directory
 * everywhere gives `architecture` for the other half.
 *
 * The `<digits>-` prefix is the discriminator: it is what the scaffold puts on
 * an issue-scoped directory and nothing else has it.
 */
export function knowledgeTopicOf(hitPath: string): string {
  const dir = path.basename(path.dirname(hitPath));
  if (/^\d+-/.test(dir)) {
    const slug = dir.replace(/^\d+-/, "");
    if (slug) return slug.replace(/-/g, " ");
    // A directory that is only a number strips to nothing; the filename is
    // then the most specific thing available.
  }
  return path.basename(hitPath, ".md").replace(/-/g, " ");
}

export class KnowledgeSearchResultItem extends BaseTreeItem {
  readonly hit: KnowledgeRecallHit;
  readonly absolutePath: string;

  constructor(hit: KnowledgeRecallHit, workspaceRoot: string) {
    const topic = knowledgeTopicOf(hit.path);
    const label = hit.issue_number ? `#${hit.issue_number} ${topic}` : topic;
    super(label, vscode.TreeItemCollapsibleState.None);

    this.hit = hit;
    this.absolutePath = path.isAbsolute(hit.path) ? hit.path : path.join(workspaceRoot, hit.path);

    const snippet = (hit.snippet ?? "").split("\n")[0].trim().slice(0, SNIPPET_MAX);
    // The quoted line IS the answer to "why is this related?", so it is the
    // description. When the snippet is missing, name the file rather than
    // falling back to a number the reader cannot interpret.
    this.description = snippet || path.basename(hit.path);

    this.tooltip = new vscode.MarkdownString(
      [
        `**${label}**`,
        "",
        snippet ? `> ${snippet}` : "_No matching line._",
        "",
        `\`${hit.path}\``,
        "",
        `BM25 relevance ${hit.score.toFixed(2)}`,
      ].join("\n")
    );

    this.contextValue = "knowledgeSearchResult";
    this.setIcon("history");

    this.command = {
      command: "vscode.open",
      title: "Open Knowledge File",
      arguments: [vscode.Uri.file(this.absolutePath)],
    };
  }
}
