/**
 * Retry Queue Item command
 *
 * Retries a failed queue item by resetting its status to 'pending'
 * and placing it back at the start of the queue.
 * Only available for items with status 'failed'.
 *
 * @see Issue #300 - Add Visual Queue Indicators and Manual Reordering
 */

import * as vscode from "vscode";
import type { IssueQueueService } from "../services/IssueQueueService";
import type { QueuedIssueTreeItem } from "../views/items/QueuedIssueTreeItem";
import type { Logger } from "../utils/logger";

/**
 * Register the Retry Queue Item command
 */
export function registerRetryQueueItemCommand(
  queueService: IssueQueueService | null,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.retryQueueItem",
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

      // Validation: Only failed items can be retried
      if (status !== "failed") {
        vscode.window.showWarningMessage(
          `Issue #${issueNumber} is not in failed state. Status: ${status}`
        );
        return;
      }

      logger.info("Retrying failed queue item", { issueNumber });

      try {
        // Remove from queue and re-add at the front (position 1)
        const removed = await queueService.remove(issueNumber);

        if (!removed) {
          throw new Error("Failed to remove item from queue");
        }

        // Re-add to queue - it will be placed according to priority rules
        // and reset to 'pending' status
        const added = await queueService.enqueue(issueNumber, title, queueItem.labels);

        if (!added) {
          // If re-add fails, item couldn't be queued
          throw new Error("Failed to re-add item to queue");
        }

        logger.info("Queue item retry queued", { issueNumber });

        // Success feedback
        vscode.window.showInformationMessage(
          `✓ Issue #${issueNumber} - "${title}" has been reset and moved to the queue.`
        );
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error occurred";
        logger.error("Failed to retry queue item", error instanceof Error ? error : undefined);
        vscode.window.showErrorMessage(`Failed to retry queue item: ${message}`);
      }
    }
  );
}
