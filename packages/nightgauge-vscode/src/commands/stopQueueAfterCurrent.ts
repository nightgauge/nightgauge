/**
 * Stop Queue After Current command
 *
 * Gracefully stops queue processing after the current issue completes.
 * The current issue finishes all stages normally; the queue simply
 * does not auto-start the next item.
 *
 * Supports both legacy single-run mode (HeadlessOrchestrator.isRunning)
 * and concurrent mode (ConcurrentPipelineManager).
 *
 * Mirrors stopBatchAfterCurrent.ts for the batch path.
 */

import * as vscode from "vscode";
import type { HeadlessOrchestrator } from "../services/HeadlessOrchestrator";
import type { ConcurrentPipelineManager } from "../services/ConcurrentPipelineManager";
import type { IssueQueueService } from "../services/IssueQueueService";
import type { Logger } from "../utils/logger";
import type { StatusBarManager } from "../utils/statusBar";

/**
 * Register the Stop Queue After Current command
 *
 * Sets a flag that prevents handleQueueAutoStart() from starting the
 * next queued issue after the current pipeline completes, and drains
 * any pending queued items so they cannot be dequeued by a later
 * fillSlots() cycle (e.g. one triggered by an autonomous.dispatch event).
 */
export function registerStopQueueAfterCurrentCommand(
  orchestrator: HeadlessOrchestrator | null,
  logger: Logger,
  statusBar: StatusBarManager,
  concurrentPipelineManager?: ConcurrentPipelineManager | null,
  queueService?: IssueQueueService | null
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.stopQueueAfterCurrent", async () => {
    if (!orchestrator) {
      vscode.window.showErrorMessage(
        "Nightgauge SDK not initialized. Check extension logs for details."
      );
      return;
    }

    const isRunning = orchestrator.getIsRunning();
    const hasConcurrentSlots = (concurrentPipelineManager?.activeSlotCount ?? 0) > 0;

    if (!isRunning && !hasConcurrentSlots) {
      vscode.window.showInformationMessage("No pipeline is currently running.");
      return;
    }

    // Handle concurrent mode
    if (hasConcurrentSlots && concurrentPipelineManager) {
      const activeSlots = concurrentPipelineManager.getActiveSlots();
      const issueNumbers = activeSlots.map((s) => `#${s.issueNumber}`);

      logger.info("Stopping queue after current issues complete", {
        activeSlots: activeSlots.length,
        issueNumbers: issueNumbers.join(", "),
      });

      try {
        concurrentPipelineManager.pauseFilling();

        // Drain any pending queued items so they cannot be dequeued by a
        // subsequent fillSlots() cycle. Without this, an autonomous.dispatch
        // event that arrives after the user clicks "Stop Queue After Current"
        // can re-populate the queue, and the next fill would dequeue it once
        // the active slot completes. The user expected "stop after current"
        // to mean "finish what's running, then idle" — clearing the pending
        // queue enforces that semantics.
        if (queueService) {
          try {
            await queueService.clear();
          } catch (clearError) {
            logger.warn("Failed to clear queue while stopping after current", {
              error: clearError instanceof Error ? clearError.message : "Unknown error",
            });
          }
        }

        // Update context for UI (disables button)
        vscode.commands.executeCommand("setContext", "nightgauge.stopAfterCurrentQueue", true);

        if (activeSlots[0]) {
          statusBar.showStoppingQueueAfterCurrent(activeSlots[0].issueNumber);
        }

        const issueText =
          issueNumbers.length === 1
            ? `issue ${issueNumbers[0]}`
            : `issues ${issueNumbers.join(", ")}`;

        vscode.window.showInformationMessage(`Queue will stop after ${issueText} complete(s).`);

        logger.info("Queue paused and drained — active slots will complete then idle", {
          issueNumbers: issueNumbers.join(", "),
        });
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error occurred";
        logger.error(
          "Failed to pause queue after current",
          error instanceof Error ? error : undefined
        );
        vscode.window.showErrorMessage(`Failed to stop queue after current: ${message}`);
      }
      return;
    }

    // Legacy single-run mode path
    const currentIssueNumber = await orchestrator.getCurrentIssueNumber();

    logger.info("Stopping queue after current issue", {
      currentIssue: currentIssueNumber,
    });

    try {
      orchestrator.stopQueueAfterCurrent();

      // Update context for UI (disables button)
      vscode.commands.executeCommand("setContext", "nightgauge.stopAfterCurrentQueue", true);

      // Update status bar
      if (currentIssueNumber) {
        statusBar.showStoppingQueueAfterCurrent(currentIssueNumber);
      }

      const issueRef = currentIssueNumber
        ? ` after issue #${currentIssueNumber} completes`
        : " after the current issue completes";

      vscode.window.showInformationMessage(`Queue will stop${issueRef}.`);

      logger.info("Stop-queue-after-current flag set", {
        currentIssue: currentIssueNumber,
      });
    } catch (error) {
      const message = error instanceof Error ? error.message : "Unknown error occurred";
      logger.error(
        "Failed to set stop-queue-after-current flag",
        error instanceof Error ? error : undefined
      );
      vscode.window.showErrorMessage(`Failed to stop queue after current: ${message}`);
    }
  });
}
