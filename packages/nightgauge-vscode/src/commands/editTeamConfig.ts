/**
 * editTeamConfig — Open .nightgauge/config.yaml in the editor
 *
 * Provides a dedicated command palette entry for intentional project-tier
 * config edits. Displays a status bar reminder that the file is git-tracked.
 *
 * @see Issue #3337 — Phase 4: Promote Machine Tier to First-Class
 * @see ADR-004 in .nightgauge/knowledge/features/3337-.../decisions.md
 */

import * as vscode from "vscode";
import { getWorkspaceRoot } from "../config/settings";

export function registerEditTeamConfigCommand(): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.editTeamConfig", async () => {
    const workspaceRoot = getWorkspaceRoot();
    if (!workspaceRoot) {
      vscode.window.showErrorMessage("No workspace folder open. Please open a workspace first.");
      return;
    }

    const configUri = vscode.Uri.file(`${workspaceRoot}/.nightgauge/config.yaml`);

    let doc: vscode.TextDocument;
    try {
      doc = await vscode.workspace.openTextDocument(configUri);
    } catch {
      const create = await vscode.window.showInformationMessage(
        "No .nightgauge/config.yaml found. Create one?",
        "Create",
        "Cancel"
      );
      if (create !== "Create") return;
      await vscode.workspace.fs.writeFile(configUri, Buffer.from("", "utf-8"));
      doc = await vscode.workspace.openTextDocument(configUri);
    }

    await vscode.window.showTextDocument(doc);

    const statusItem = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 100);
    statusItem.text = "$(git-commit) Team config — commit through your normal review workflow";
    statusItem.tooltip =
      "Edits to this file modify a tracked repo file. Use git review before merging.";
    statusItem.show();

    const disposable = vscode.window.onDidChangeActiveTextEditor((editor) => {
      if (!editor || editor.document.uri.fsPath !== configUri.fsPath) {
        statusItem.dispose();
        disposable.dispose();
      }
    });
  });
}
