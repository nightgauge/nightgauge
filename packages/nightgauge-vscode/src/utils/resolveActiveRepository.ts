/**
 * Derive the "active" repository from the current editor context rather than
 * tracking a mutable `_currentRepository` pointer on WorkspaceManager.
 *
 * History: the extension used to carry an explicit active-repo pointer set
 * by a "Switch to Repository" arrow button in the Repositories view plus a
 * status bar switcher. That model pre-dated cross-repo pipeline routing, and
 * most call sites now pass their target repo explicitly (via issue/PR data).
 * A handful of defaults still need to answer "which repo is the user
 * looking at right now?" — this helper replaces the pointer for those cases.
 *
 * Resolution order:
 *   1. The repo whose path contains the active editor's file (best match
 *      wins — longest matching path prefix, so nested checkouts resolve to
 *      the correct sub-repo).
 *   2. The repo whose path contains the workspace folder of the active
 *      editor (covers files opened outside any repo).
 *   3. The repo with `role === "primary"`.
 *   4. The first repo in the workspace.
 *   5. `null` when no repos are loaded yet.
 *
 * No persistence, no events — callers that care about editor-change
 * reactivity subscribe to `vscode.window.onDidChangeActiveTextEditor` and
 * call the helper again.
 */

import * as vscode from "vscode";
import type { WorkspaceManager } from "../services/WorkspaceManager";
import type { Repository } from "../models/Repository";

/**
 * Resolve the repository the user is "currently working in."
 *
 * `null` only when the workspace has no repos at all — callers that want a
 * strict non-null should have loaded repos before asking.
 */
export function resolveActiveRepository(
  workspaceManager: WorkspaceManager | null | undefined
): Repository | null {
  if (!workspaceManager) return null;
  const repos = workspaceManager.getAllRepositories();
  if (repos.length === 0) return null;
  if (repos.length === 1) return repos[0];

  const editor = (vscode as typeof vscode | undefined)?.window?.activeTextEditor ?? undefined;
  const activeUri = editor?.document?.uri;

  if (activeUri && activeUri.scheme === "file") {
    const best = pickByPathPrefix(repos, activeUri.fsPath);
    if (best) return best;

    const folder = vscode.workspace.getWorkspaceFolder(activeUri);
    if (folder) {
      const folderMatch = pickByPathPrefix(repos, folder.uri.fsPath);
      if (folderMatch) return folderMatch;
    }
  }

  const primary = repos.find((r) => r.role === "primary");
  if (primary) return primary;

  return repos[0];
}

/**
 * Return the repo whose `path` is the longest prefix of `filePath`, or
 * undefined when none match. Longest-prefix wins so a file inside a nested
 * repo resolves to the inner repo rather than a parent.
 */
function pickByPathPrefix(repos: Repository[], filePath: string): Repository | undefined {
  let best: Repository | undefined;
  let bestLen = -1;
  for (const r of repos) {
    if (!r.path) continue;
    if (!isPathInside(filePath, r.path)) continue;
    if (r.path.length > bestLen) {
      best = r;
      bestLen = r.path.length;
    }
  }
  return best;
}

function isPathInside(filePath: string, candidateDir: string): boolean {
  if (!filePath.startsWith(candidateDir)) return false;
  if (filePath.length === candidateDir.length) return true;
  const next = filePath.charAt(candidateDir.length);
  return next === "/" || next === "\\";
}
