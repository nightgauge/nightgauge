/**
 * clearFailedIssues - Clear all failed issues from history and Go scheduler
 *
 * Clears both the VSCode completion history (CompletedIssuesService) and the
 * Go autonomous scheduler's lifetime failure counters (via IPC). The lifetime
 * counter is what triggers the safety:lifetime-failure-cap trip — the VSCode
 * history alone is not sufficient to unblock autonomous.
 *
 * @see Issue #301 - Handle completed and failed issue states in pipeline
 */

import * as vscode from "vscode";
import { CompletedIssuesService } from "../services/CompletedIssuesService";
import { IpcClient } from "../services/IpcClient";

/**
 * Register the clearFailedIssues command
 */
export function registerClearFailedIssuesCommand(
  context: vscode.ExtensionContext
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.clearFailedIssues", async () => {
    try {
      const service = CompletedIssuesService.getInstance(context.workspaceState);
      const vscodeFailedCount = service.getFailed().length;

      // Clear Go scheduler lifetime failure counters (the safety:lifetime-failure-cap
      // source). Returns how many issue counters were cleared. Falls back to 0 when
      // the Go binary is not running (IPC unavailable).
      let goCleared = 0;
      try {
        const result = await IpcClient.getInstance().autonomousClearIssueFailures();
        goCleared = result.cleared;
      } catch {
        // Go binary not running — only VSCode history can be cleared.
      }

      const total = vscodeFailedCount + goCleared;
      if (total === 0) {
        vscode.window.showInformationMessage("No failed issues to clear.");
        return;
      }

      service.clearFailed();

      const label = `Cleared ${total} failed issue${total !== 1 ? "s" : ""}.`;

      if (goCleared > 0) {
        // Autonomous was safety-tripped by lifetime failures — offer to resume.
        const action = await vscode.window.showInformationMessage(
          label + " Autonomous can now be resumed.",
          "Resume Autonomous"
        );
        if (action === "Resume Autonomous") {
          await vscode.commands.executeCommand("nightgauge.autonomousResume");
        }
      } else {
        vscode.window.showInformationMessage(label);
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : "Unknown error";
      vscode.window.showErrorMessage(`Failed to clear failed issues: ${message}`);
      console.error("[Nightgauge] Error clearing failed issues:", error);
    }
  });
}
