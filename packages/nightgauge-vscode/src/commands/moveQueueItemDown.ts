/**
 * Move Queue Item Down command
 *
 * Moves a queue item down one position (increases position number).
 * Cannot move item at last position or items with status 'processing'.
 *
 * @see Issue #300 - Add Visual Queue Indicators and Manual Reordering
 */

import * as vscode from "vscode";
import type { IssueQueueService } from "../services/IssueQueueService";
import type { QueuedIssueTreeItem } from "../views/items/QueuedIssueTreeItem";
import type { Logger } from "../utils/logger";

/**
 * Register the Move Queue Item Down command
 */
export function registerMoveQueueItemDownCommand(
  queueService: IssueQueueService | null,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.moveQueueItemDown",
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

      // Get current queue to check length
      const queueState = await queueService.getQueue();
      const queueLength = queueState?.items.length || 0;

      // Validation: Cannot move item at last position
      if (position >= queueLength) {
        vscode.window.showWarningMessage(
          `Cannot move issue #${issueNumber} down - already at bottom of queue.`
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

      logger.info("Moving queue item down", { issueNumber, position });

      try {
        // Calculate new position (move down means increase position number)
        const newPosition = position + 1;

        // Call queue service to reorder
        const success = await queueService.reorder(issueNumber, newPosition);

        if (!success) {
          throw new Error("Failed to reorder queue item");
        }

        logger.info("Queue item moved down", {
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
        logger.error("Failed to move queue item down", error instanceof Error ? error : undefined);
        vscode.window.showErrorMessage(`Failed to reorder queue items: ${message}`);
      }
    }
  );
}
