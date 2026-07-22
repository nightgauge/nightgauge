/**
 * KnowledgeDocumentLinkProvider - DocumentLinkProvider for [[wiki-link]] references
 *
 * Makes [[wiki-links]] in markdown files clickable by resolving them to knowledge
 * base entries. Uses the SDK's wikiLinkResolver for extraction and resolution.
 * Unresolved links are reported as warning diagnostics (squiggly underline).
 *
 * Supports cross-repo wiki-links (`[[repo-name:path]]`) when a workspace config is
 * provided via the optional third constructor parameter.
 *
 * @see Issue #1687 - Implement KnowledgeDocumentLinkProvider
 * @see Issue #1697 - Cross-repo wiki-link resolution
 * @see packages/nightgauge-sdk/src/utils/wikiLinkResolver.ts
 */

import * as vscode from "vscode";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import { extractWikiLinks, resolveWikiLink } from "@nightgauge/sdk";
import { Logger } from "../utils/logger";
import type { WorkspaceConfig } from "../types/WorkspaceConfig";

const KNOWLEDGE_DIR = path.join(".nightgauge", "knowledge");

export class KnowledgeDocumentLinkProvider
  implements vscode.DocumentLinkProvider, vscode.Disposable
{
  private readonly diagnostics: vscode.DiagnosticCollection;

  constructor(
    private readonly workspaceRoot: string,
    private readonly logger: Logger,
    private readonly workspaceConfig?: WorkspaceConfig
  ) {
    this.diagnostics = vscode.languages.createDiagnosticCollection("nightgauge-wiki-links");
  }

  async provideDocumentLinks(
    document: vscode.TextDocument,
    token: vscode.CancellationToken
  ): Promise<vscode.DocumentLink[]> {
    // Only activate when knowledge directory exists
    const knowledgeRoot = path.join(this.workspaceRoot, KNOWLEDGE_DIR);
    try {
      await fs.access(knowledgeRoot);
    } catch {
      this.diagnostics.delete(document.uri);
      return [];
    }

    const content = document.getText();
    const wikiLinks = extractWikiLinks(content);

    if (wikiLinks.length === 0) {
      this.diagnostics.delete(document.uri);
      return [];
    }

    const links: vscode.DocumentLink[] = [];
    const diagnostics: vscode.Diagnostic[] = [];

    for (const wikiLink of wikiLinks) {
      if (token.isCancellationRequested) break;

      const startPos = document.positionAt(wikiLink.index);
      const endPos = document.positionAt(wikiLink.index + wikiLink.match.length);
      const range = new vscode.Range(startPos, endPos);

      const resolved = await resolveWikiLink(
        wikiLink.raw,
        document.uri.fsPath,
        this.workspaceRoot,
        this.workspaceConfig ?? undefined
      );

      if (resolved.exists) {
        const link = new vscode.DocumentLink(range, vscode.Uri.file(resolved.resolvedPath));
        link.tooltip = resolved.resolvedPath;
        links.push(link);

        if (resolved.isAmbiguous) {
          this.logger.info(
            `Ambiguous wiki-link '${wikiLink.raw}' resolved to first of ${resolved.candidates.length} candidates`,
            { resolvedPath: resolved.resolvedPath }
          );
        }
      } else {
        const message =
          resolved.isCrossRepo && resolved.repoName
            ? `Wiki-link '[[${wikiLink.raw}]]' — repo '${resolved.repoName}' not found in workspace config`
            : `Wiki-link '${wikiLink.raw}' could not be resolved in knowledge base`;
        const diagnostic = new vscode.Diagnostic(range, message, vscode.DiagnosticSeverity.Warning);
        diagnostic.source = "nightgauge";
        diagnostics.push(diagnostic);
      }
    }

    this.diagnostics.set(document.uri, diagnostics);
    return links;
  }

  dispose(): void {
    this.diagnostics.dispose();
  }
}
