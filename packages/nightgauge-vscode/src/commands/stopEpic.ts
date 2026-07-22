/**
 * Stop Epic command — Pause all slots for a specific epic
 *
 * Stops all running pipeline slots belonging to a specific epic and drains
 * queued successor issues from that epic. Preserves all GitHub state
 * (issues stay open, board status unchanged). Other epics are unaffected.
 *
 * For full rollback, use abortPipeline.
 *
 * @see Issue #2261 - Per-slot / per-epic pipeline controls
 */

import * as vscode from "vscode";
import type { ConcurrentPipelineManager } from "../services/ConcurrentPipelineManager";
import type { Logger } from "../utils/logger";

/**
 * Register the Stop Epic command
 *
 * Stops all concurrent pipeline slots belonging to a specific epic.
 * Called from the inline action on ConcurrentSlotTreeItem (when it
 * has an epicNumber) or via command palette with epic selection.
 */
export function registerStopEpicCommand(
  logger: Logger,
  concurrentPipelineManager: ConcurrentPipelineManager | null
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.stopEpic",
    async (item?: { epicNumber?: number }) => {
      if (!concurrentPipelineManager) {
        vscode.window.showErrorMessage("Concurrent pipeline manager not initialized.");
        return;
      }

      let epicNumber = item?.epicNumber;

      // If no epic number provided (e.g. command palette), discover
      // running epics from active slots and show a quick pick.
      if (!epicNumber) {
        const activeSlots = concurrentPipelineManager.getActiveSlots();
        const epicMap = new Map<number, { issues: number[] }>();
        for (const slot of activeSlots) {
          if (slot.epicNumber) {
            const entry = epicMap.get(slot.epicNumber) ?? { issues: [] };
            entry.issues.push(slot.issueNumber);
            epicMap.set(slot.epicNumber, entry);
          }
        }

        if (epicMap.size === 0) {
          vscode.window.showInformationMessage("No epics are currently running.");
          return;
        }

        if (epicMap.size === 1) {
          // Only one epic running — use it directly
          epicNumber = epicMap.keys().next().value!;
        } else {
          // Multiple epics running — let user choose
          const picks = Array.from(epicMap.entries()).map(([num, { issues }]) => ({
            label: `Epic #${num}`,
            description: `${issues.length} running issue(s): ${issues.map((n) => `#${n}`).join(", ")}`,
            epicNumber: num,
          }));

          const selected = await vscode.window.showQuickPick(picks, {
            placeHolder: "Select an epic to stop",
          });

          if (!selected) return; // User cancelled
          epicNumber = selected.epicNumber;
        }
      }

      const epicSlots = concurrentPipelineManager.getSlotsByEpic(epicNumber);

      if (epicSlots.length === 0) {
        vscode.window.showInformationMessage(`No running slots found for epic #${epicNumber}.`);
        return;
      }

      const issueList = epicSlots.map((s) => `#${s.issueNumber}`).join(", ");

      const confirm = await vscode.window.showWarningMessage(
        `Stop all pipelines for epic #${epicNumber}? This will stop ${epicSlots.length} running issue(s): ${issueList}, and remove queued epic items. State will be preserved — use Abort for full rollback.`,
        { modal: true },
        "Stop Epic"
      );

      if (confirm !== "Stop Epic") {
        return;
      }

      logger.info("Stopping all pipeline slots for epic (state preserved)", {
        epicNumber,
        slotCount: epicSlots.length,
        issues: epicSlots.map((s) => s.issueNumber),
      });

      try {
        const stoppedCount = await concurrentPipelineManager.abortEpic(epicNumber);

        // NOTE: GitHub status is intentionally NOT reset here.
        // Stop = pause. Issues stay at their current board status so they
        // aren't accidentally picked up by another pipeline run.
        // Use abortPipeline for full rollback (reopen + board reset).

        vscode.window.showInformationMessage(
          `Stopped ${stoppedCount} pipeline(s) for epic #${epicNumber}. State preserved.`
        );
        logger.info("Epic pipeline stopped by user (state preserved)", {
          epicNumber,
          stoppedCount,
        });
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error occurred";
        logger.error("Failed to stop epic pipeline", {
          epicNumber,
          error: message,
        });
        vscode.window.showErrorMessage(`Failed to stop epic #${epicNumber}: ${message}`);
      }
    }
  );
}
