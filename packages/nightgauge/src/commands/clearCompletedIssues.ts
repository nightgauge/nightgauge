/**
 * clearCompletedIssues - Clear all completed issues from history
 *
 * @see Issue #301 - Handle completed and failed issue states in pipeline
 */

import * as vscode from "vscode";
import { CompletedIssuesService } from "../services/CompletedIssuesService";

/**
 * Register the clearCompletedIssues command
 */
export function registerClearCompletedIssuesCommand(
  context: vscode.ExtensionContext
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.clearCompletedIssues", async () => {
    try {
      const service = CompletedIssuesService.getInstance(context.workspaceState);

      const completed = service.getCompleted();

      if (completed.length === 0) {
        vscode.window.showInformationMessage("No completed issues to clear.");
        return;
      }

      // Confirm before clearing
      const confirm = await vscode.window.showWarningMessage(
        `Clear ${completed.length} completed issue${completed.length > 1 ? "s" : ""}?`,
        { modal: true },
        "Clear"
      );

      if (confirm !== "Clear") {
        return;
      }

      service.clearCompleted();

      vscode.window.showInformationMessage(
        `Cleared ${completed.length} completed issue${completed.length > 1 ? "s" : ""}.`
      );
    } catch (error) {
      const message = error instanceof Error ? error.message : "Unknown error";
      vscode.window.showErrorMessage(`Failed to clear completed issues: ${message}`);
      console.error("[Nightgauge] Error clearing completed issues:", error);
    }
  });
}
