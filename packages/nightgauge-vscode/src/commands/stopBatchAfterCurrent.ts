/**
 * Stop Batch After Current command
 *
 * Gracefully stops the concurrent pipeline after the current issue(s) complete.
 * Allows developers to handle urgent tasks (fly-in items, hotfixes)
 * without interrupting the current issue's work.
 *
 * Pauses slot filling in ConcurrentPipelineManager so no new items dequeue,
 * but lets current slots finish their work.
 *
 * @see Issue #320 - Stop After Current Issue Button for Batch Mode
 */

import * as vscode from "vscode";
import type { HeadlessOrchestrator } from "../services/HeadlessOrchestrator";
import type { ConcurrentPipelineManager } from "../services/ConcurrentPipelineManager";
import type { Logger } from "../utils/logger";
import type { StatusBarManager } from "../utils/statusBar";

/**
 * Register the Stop Batch After Current command
 *
 * Pauses slot filling in ConcurrentPipelineManager so no new issues
 * are dequeued after the current issue(s) complete. The current
 * issue(s) complete normally with all stages.
 */
export function registerStopBatchAfterCurrentCommand(
  orchestrator: HeadlessOrchestrator | null,
  logger: Logger,
  statusBar: StatusBarManager,
  concurrentPipelineManager?: ConcurrentPipelineManager | null
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.stopBatchAfterCurrent", async () => {
    // Check if orchestrator is available
    if (!orchestrator) {
      vscode.window.showErrorMessage(
        "Nightgauge SDK not initialized. Check extension logs for details."
      );
      return;
    }

    const hasConcurrentSlots = (concurrentPipelineManager?.activeSlotCount ?? 0) > 0;

    // Check if anything is running
    if (!hasConcurrentSlots) {
      vscode.window.showInformationMessage("No pipeline is currently running.");
      return;
    }

    // Handle concurrent mode: pause filling so no new items dequeue,
    // but let current slots finish their work.
    if (concurrentPipelineManager) {
      const activeSlots = concurrentPipelineManager.getActiveSlots();
      const issueNumbers = activeSlots.map((s) => `#${s.issueNumber}`);

      logger.info("Stopping concurrent pipeline after current issues complete", {
        activeSlots: activeSlots.length,
        issueNumbers: issueNumbers.join(", "),
      });

      try {
        concurrentPipelineManager.pauseFilling();

        // Update context for UI (disables button)
        vscode.commands.executeCommand("setContext", "nightgauge.stopAfterCurrentBatch", true);

        const issueText =
          issueNumbers.length === 1
            ? `issue ${issueNumbers[0]}`
            : `issues ${issueNumbers.join(", ")}`;

        statusBar.showStoppingAfterCurrent(activeSlots[0].issueNumber);

        vscode.window.showInformationMessage(
          `Pipeline will stop after ${issueText} complete(s). No new issues will be dequeued.`
        );

        logger.info("Concurrent pipeline paused — draining active slots", {
          issueNumbers: issueNumbers.join(", "),
        });
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error occurred";
        logger.error(
          "Failed to pause concurrent pipeline",
          error instanceof Error ? error : undefined
        );
        vscode.window.showErrorMessage(`Failed to stop after current: ${message}`);
      }
    }
  });
}
