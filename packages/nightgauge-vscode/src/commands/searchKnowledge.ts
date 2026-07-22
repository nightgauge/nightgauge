/**
 * `Nightgauge: Search Knowledge` command — prompts for a query, calls
 * `knowledge.search` over IPC, and pushes the hits into the KnowledgeTreeProvider's
 * Search section.
 *
 * @see Issue #2964
 */

import * as vscode from "vscode";
import type { IpcClient } from "../services/IpcClient";
import type { KnowledgeTreeProvider } from "../views/KnowledgeTreeProvider";

const SEARCH_LIMIT = 20;

export async function searchKnowledge(
  ipcClient: IpcClient,
  knowledgeTreeProvider: KnowledgeTreeProvider
): Promise<void> {
  const query = await vscode.window.showInputBox({
    prompt: "Search knowledge base",
    placeHolder: "authentication flow",
    ignoreFocusOut: true,
  });
  if (!query || !query.trim()) {
    return;
  }

  try {
    const result = await ipcClient.knowledgeSearch(
      query.trim(),
      undefined,
      undefined,
      SEARCH_LIMIT
    );
    const hits = result.hits ?? [];
    knowledgeTreeProvider.setSearchResults(hits);
    if (hits.length === 0) {
      vscode.window.showInformationMessage(`No knowledge matches for "${query}"`);
    } else {
      vscode.window.showInformationMessage(
        `${hits.length} knowledge match(es) for "${query}" — see the Knowledge sidebar`
      );
    }
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    vscode.window.showErrorMessage(`Knowledge search failed: ${msg}`);
  }
}
