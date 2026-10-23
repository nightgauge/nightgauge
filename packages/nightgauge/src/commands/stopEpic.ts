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
import { epicRefKey, formatEpicRef, type EpicRef } from "../utils/epicRef";

/**
 * Register the Stop Epic command
 *
 * Stops all concurrent pipeline slots belonging to a specific epic.
 * Called from the inline action on ConcurrentSlotTreeItem (when it
 * has an epicNumber) or via command palette with epic selection.
 *
 * The epic is named by repository and number (`owner/repo#N`): another
 * repository's epic can share the number, and is left running (#2382).
 */
export function registerStopEpicCommand(
  logger: Logger,
  concurrentPipelineManager: ConcurrentPipelineManager | null
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.stopEpic",
    async (item?: { epicNumber?: number; epicRepo?: string }) => {
      if (!concurrentPipelineManager) {
        vscode.window.showErrorMessage("Concurrent pipeline manager not initialized.");
        return;
      }

      let epic: EpicRef | undefined = item?.epicNumber
        ? { repo: item.epicRepo, number: item.epicNumber }
        : undefined;

      // If no epic provided (e.g. command palette), discover running epics
      // from active slots and show a quick pick, one entry per repository's
      // epic.
      if (!epic) {
        const activeSlots = concurrentPipelineManager.getActiveSlots();
        const epicMap = new Map<string, { epic: EpicRef; issues: number[] }>();
        for (const slot of activeSlots) {
          if (slot.epicNumber) {
            const ref: EpicRef = { repo: slot.epicRepo, number: slot.epicNumber };
            const key = epicRefKey(ref);
            const entry = epicMap.get(key) ?? { epic: ref, issues: [] };
            entry.issues.push(slot.issueNumber);
            epicMap.set(key, entry);
          }
        }

        if (epicMap.size === 0) {
          vscode.window.showInformationMessage("No epics are currently running.");
          return;
        }

        if (epicMap.size === 1) {
          // Only one epic running — use it directly
          epic = epicMap.values().next().value!.epic;
        } else {
          // Multiple epics running — let user choose
          const picks = Array.from(epicMap.values()).map(({ epic: ref, issues }) => ({
            label: `Epic ${formatEpicRef(ref)}`,
            description: `${issues.length} running issue(s): ${issues.map((n) => `#${n}`).join(", ")}`,
            epic: ref,
          }));

          const selected = await vscode.window.showQuickPick(picks, {
            placeHolder: "Select an epic to stop",
          });

          if (!selected) return; // User cancelled
          epic = selected.epic;
        }
      }

      const epicName = formatEpicRef(epic);
      const epicSlots = concurrentPipelineManager.getSlotsByEpic(epic);

      if (epicSlots.length === 0) {
        vscode.window.showInformationMessage(`No running slots found for epic ${epicName}.`);
        return;
      }

      const issueList = epicSlots.map((s) => `#${s.issueNumber}`).join(", ");

      const confirm = await vscode.window.showWarningMessage(
        `Stop all pipelines for epic ${epicName}? This will stop ${epicSlots.length} running issue(s): ${issueList}, and remove queued epic items. State will be preserved — use Abort for full rollback.`,
        { modal: true },
        "Stop Epic"
      );

      if (confirm !== "Stop Epic") {
        return;
      }

      logger.info("Stopping all pipeline slots for epic (state preserved)", {
        epic: epicName,
        slotCount: epicSlots.length,
        issues: epicSlots.map((s) => s.issueNumber),
      });

      try {
        const stoppedCount = await concurrentPipelineManager.abortEpic(epic);

        // NOTE: GitHub status is intentionally NOT reset here.
        // Stop = pause. Issues stay at their current board status so they
        // aren't accidentally picked up by another pipeline run.
        // Use abortPipeline for full rollback (reopen + board reset).

        vscode.window.showInformationMessage(
          `Stopped ${stoppedCount} pipeline(s) for epic ${epicName}. State preserved.`
        );
        logger.info("Epic pipeline stopped by user (state preserved)", {
          epic: epicName,
          stoppedCount,
        });
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error occurred";
        logger.error("Failed to stop epic pipeline", {
          epic: epicName,
          error: message,
        });
        vscode.window.showErrorMessage(`Failed to stop epic ${epicName}: ${message}`);
      }
    }
  );
}
