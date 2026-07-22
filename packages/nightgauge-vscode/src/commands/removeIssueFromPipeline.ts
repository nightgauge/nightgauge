/**
 * Remove Issue from Pipeline Command
 *
 * Removes a queued issue from the pipeline.
 * Provides keyboard alternative for queue management.
 *
 * @see Issue #304 - Add Accessibility Alternatives for Drag & Drop
 */

import * as vscode from "vscode";
import { QueuedIssueTreeItem } from "../views/items/QueuedIssueTreeItem";
import type { IssueQueueService } from "../services/IssueQueueService";
import type { Logger } from "../utils/logger";

/**
 * Register the Remove Issue from Pipeline command
 *
 * Removes a queued issue from the pipeline, providing keyboard-accessible
 * queue management.
 */
export function registerRemoveIssueFromPipelineCommand(
  queueService: IssueQueueService,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.removeIssueFromPipeline",
    async (item?: QueuedIssueTreeItem) => {
      try {
        // Validation
        if (!item || !(item instanceof QueuedIssueTreeItem)) {
          logger.error("removeIssueFromPipeline: Invalid item parameter");
          vscode.window.showErrorMessage("Please select a valid queued issue");
          return;
        }

        const issueNumber = item.issueNumber;

        logger.info("Removing issue from pipeline queue", { issueNumber });

        // Remove from queue
        const removed = await queueService.remove(issueNumber);

        if (!removed) {
          logger.warn("Issue not found in queue", { issueNumber });
          vscode.window.showWarningMessage(`Issue #${issueNumber} was not in the queue`);
          return;
        }

        // Show success message (screen reader will announce)
        vscode.window.showInformationMessage(`Issue #${issueNumber} removed from pipeline`);

        logger.info("Successfully removed issue from pipeline", {
          issueNumber,
        });
      } catch (error) {
        logger.error("Failed to remove issue from pipeline", { error });
        vscode.window.showErrorMessage("Failed to remove issue from pipeline");
      }
    }
  );
}
