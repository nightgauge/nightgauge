/**
 * Move Queue Item Up command
 *
 * Moves a queue item up one position (decreases position number).
 * Cannot move item at position 1 or items with status 'processing'.
 *
 * @see Issue #300 - Add Visual Queue Indicators and Manual Reordering
 */

import * as vscode from "vscode";
import type { IssueQueueService } from "../services/IssueQueueService";
import type { QueuedIssueTreeItem } from "../views/items/QueuedIssueTreeItem";
import type { Logger } from "../utils/logger";

/**
 * Register the Move Queue Item Up command
 */
export function registerMoveQueueItemUpCommand(
  queueService: IssueQueueService | null,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.moveQueueItemUp",
    async (item: QueuedIssueTreeItem) => {
      // Validate service availability
      if (!queueService) {
        vscode.window.showErrorMessage(
          "Queue service not initialized. Check extension logs for details."
        );
        return;
      }

      const queueItem = item.getQueueItem();
      const { issueNumber, position, status } = queueItem;

      // Validation: Cannot move item at position 1
      if (position <= 1) {
        vscode.window.showWarningMessage(
          `Cannot move issue #${issueNumber} up - already at top of queue.`
        );
        return;
      }

      // Validation: Cannot move processing items
      if (status === "processing") {
        vscode.window.showWarningMessage(
          `Cannot reorder issue #${issueNumber} while it is processing.`
        );
        return;
      }

      logger.info("Moving queue item up", { issueNumber, position });

      try {
        // Calculate new position (move up means decrease position number)
        const newPosition = position - 1;

        // Call queue service to reorder
        const success = await queueService.reorder(issueNumber, newPosition);

        if (!success) {
          throw new Error("Failed to reorder queue item");
        }

        logger.info("Queue item moved up", {
          issueNumber,
          oldPosition: position,
          newPosition,
        });

        // Success feedback via status bar
        vscode.window.setStatusBarMessage(
          `✓ Moved issue #${issueNumber} to position ${newPosition}`,
          3000
        );
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error occurred";
        logger.error("Failed to move queue item up", error instanceof Error ? error : undefined);
        vscode.window.showErrorMessage(`Failed to reorder queue items: ${message}`);
      }
    }
  );
}
