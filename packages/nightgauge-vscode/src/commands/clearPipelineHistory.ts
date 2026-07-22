/**
 * clearPipelineHistory - Clear completed and failed issues from history
 *
 * @see Issue #302 - Update refresh button to clear completed and failed issues
 */

import * as vscode from "vscode";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { Logger } from "../utils/logger";

/**
 * Register the clearPipelineHistory command
 *
 * This command provides manual cleanup of completed and failed issues
 * via keyboard shortcut (Ctrl/Cmd+Shift+X).
 */
export function registerClearPipelineHistoryCommand(
  stateService: PipelineStateService | null,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.clearPipelineHistory", async () => {
    if (!stateService) {
      vscode.window.showErrorMessage(
        "Pipeline state service not available. Ensure workspace is initialized."
      );
      return;
    }

    try {
      // Batch state was removed — pipeline history is managed via
      // execution history files. Inform the user there's nothing to clear.
      vscode.window.showInformationMessage("No pipeline history to clear.");
      logger.debug("clearPipelineHistory invoked — batch state no longer available");
    } catch (error) {
      const message = error instanceof Error ? error.message : "Unknown error";
      vscode.window.showErrorMessage(`Failed to clear pipeline history: ${message}`);
      logger.error("Error clearing pipeline history", { error });
    }
  });
}
