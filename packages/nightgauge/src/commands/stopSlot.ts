/**
 * Stop Slot command — Pause a single concurrent slot
 *
 * Stops a single concurrent pipeline slot by issue number.
 * Preserves all GitHub state (issue stays open, board status unchanged).
 * Other running slots and the queue are unaffected.
 *
 * For full rollback, use abortPipeline.
 *
 * @see Issue #2261 - Per-slot / per-epic pipeline controls
 */

import * as vscode from "vscode";
import type { ConcurrentPipelineManager } from "../services/ConcurrentPipelineManager";
import type { Logger } from "../utils/logger";

/**
 * Register the Stop Slot command
 *
 * Stops a single concurrent pipeline slot. Called from the inline action
 * on ConcurrentSlotTreeItem (contextValue = concurrentSlot.running).
 *
 * The slot is named by repository and number (`item.repo`, `owner/name`):
 * two repositories' issues with one number can run at once, and only the
 * named one stops (#2403).
 */
export function registerStopSlotCommand(
  logger: Logger,
  concurrentPipelineManager: ConcurrentPipelineManager | null
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.stopSlot",
    async (item?: { issueNumber?: number; repo?: string }) => {
      if (!concurrentPipelineManager) {
        vscode.window.showErrorMessage("Concurrent pipeline manager not initialized.");
        return;
      }

      const issueNumber = item?.issueNumber;
      if (!issueNumber) {
        vscode.window.showWarningMessage(
          "No issue number provided. Use the inline stop button on a running slot."
        );
        return;
      }
      const repo = item?.repo || undefined;
      const name = `${repo ?? ""}#${issueNumber}`;

      if (!concurrentPipelineManager.isRunning(issueNumber, repo)) {
        vscode.window.showInformationMessage(`Issue ${name} is not currently running.`);
        return;
      }

      const confirm = await vscode.window.showWarningMessage(
        `Stop the pipeline for issue ${name}? State will be preserved — use Abort for full rollback.`,
        { modal: true },
        "Stop Issue"
      );

      if (confirm !== "Stop Issue") {
        return;
      }

      logger.info("Stopping individual pipeline slot (state preserved)", {
        issueNumber,
        repo: repo ?? "",
      });

      try {
        const stopped = concurrentPipelineManager.abortSlot(issueNumber, repo);

        if (!stopped) {
          vscode.window.showWarningMessage(
            `Could not stop issue ${name} — the slot may have already completed, or ` +
              `the number names a running issue in more than one repository.`
          );
          return;
        }

        // NOTE: GitHub status is intentionally NOT reset here.
        // Stop = pause. The issue stays at its current board status so it
        // isn't accidentally picked up by another pipeline run.
        // Use abortPipeline for full rollback (reopen + board reset).

        vscode.window.showInformationMessage(
          `Pipeline stopped for issue ${name}. State preserved.`
        );
        logger.info("Individual pipeline slot stopped by user (state preserved)", {
          issueNumber,
          repo: repo ?? "",
        });
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error occurred";
        logger.error("Failed to stop pipeline slot", {
          issueNumber,
          error: message,
        });
        vscode.window.showErrorMessage(`Failed to stop issue ${name}: ${message}`);
      }
    }
  );
}
