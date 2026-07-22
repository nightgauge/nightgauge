/**
 * Remove Queue Item command
 *
 * Removes an item from the queue.
 * Cannot remove items with status 'processing'.
 *
 * @see Issue #300 - Add Visual Queue Indicators and Manual Reordering
 */

import * as vscode from "vscode";
import type { IssueQueueService } from "../services/IssueQueueService";
import type { QueuedIssueTreeItem } from "../views/items/QueuedIssueTreeItem";
import type { Logger } from "../utils/logger";

/**
 * Register the Remove Queue Item command
 */
export function registerRemoveQueueItemCommand(
  queueService: IssueQueueService | null,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.removeQueueItem",
    async (item: QueuedIssueTreeItem) => {
      // Validate service availability
      if (!queueService) {
        vscode.window.showErrorMessage(
          "Queue service not initialized. Check extension logs for details."
        );
        return;
      }

      const queueItem = item.getQueueItem();
      const { issueNumber, title, status } = queueItem;

      // Validation: Cannot remove processing items
      if (status === "processing") {
        vscode.window.showWarningMessage(
          `Cannot remove issue #${issueNumber} while it is processing. Wait for it to complete or fail.`
        );
        return;
      }

      // Confirm removal
      const confirmation = await vscode.window.showWarningMessage(
        `Remove issue #${issueNumber} - "${title}" from queue?`,
        { modal: true },
        "Remove"
      );

      if (confirmation !== "Remove") {
        logger.debug("Queue item removal cancelled by user", { issueNumber });
        return;
      }

      logger.info("Removing queue item", { issueNumber });

      try {
        // Call queue service to remove
        const success = await queueService.remove(issueNumber);

        if (!success) {
          throw new Error("Issue not found in queue");
        }

        logger.info("Queue item removed", { issueNumber });

        // Success feedback via status bar
        vscode.window.setStatusBarMessage(`✓ Removed issue #${issueNumber} from queue`, 3000);
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error occurred";
        logger.error("Failed to remove queue item", error instanceof Error ? error : undefined);
        vscode.window.showErrorMessage(`Failed to remove queue item: ${message}`);
      }
    }
  );
}
