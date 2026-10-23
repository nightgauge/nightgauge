/**
 * Recalibrate Health command
 *
 * Allows users to reset the health score trend baseline after a batch of
 * systemic infrastructure fixes. Writes a recalibration marker to
 * health-history.jsonl so that health trend components only use data after
 * the marker timestamp, preventing old failures from dragging down scores.
 *
 * The recalibration is intentional and logged with a user-provided reason so
 * the audit trail is preserved.
 *
 * @see Issue #1262 - Add health score baseline recalibration after systemic fixes
 */

import * as vscode from "vscode";
import { HealthScoreHistoryWriter } from "../utils/healthScoreHistory";
import type { Logger } from "../utils/logger";

/**
 * Register the Recalibrate Health command.
 *
 * @param workspaceRoot - Workspace root where health-history.jsonl lives
 * @param logger - Logger instance
 */
export function registerRecalibrateHealthCommand(
  workspaceRoot: string,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.recalibrateHealth", async () => {
    // Confirm the user understands what recalibration does
    const confirm = await vscode.window.showWarningMessage(
      "Recalibrate health score baseline? Health trends will only use data recorded after this point. Previous failures will no longer affect the trend.",
      { modal: true },
      "Recalibrate"
    );

    if (confirm !== "Recalibrate") {
      return;
    }

    // Prompt for an optional reason to keep the audit trail meaningful
    const reason = await vscode.window.showInputBox({
      prompt: "Reason for recalibration (optional)",
      placeHolder: "e.g. Systemic schema validation fixes completed (#1236–#1256)",
      ignoreFocusOut: true,
    });

    // undefined means the user pressed Escape — treat as cancel
    if (reason === undefined) {
      return;
    }

    try {
      await HealthScoreHistoryWriter.appendRecalibrationMarker(
        workspaceRoot,
        reason.trim() || undefined
      );

      logger.info("Health score baseline recalibrated", {
        reason: reason.trim() || "(no reason provided)",
        timestamp: new Date().toISOString(),
      });

      vscode.window.showInformationMessage(
        "Health score baseline recalibrated. Trends now reflect only data recorded after this point."
      );
    } catch (error) {
      const msg = error instanceof Error ? error.message : "Unknown error";
      logger.error("Failed to recalibrate health score baseline", error as Error);
      vscode.window.showErrorMessage(`Failed to recalibrate health score baseline: ${msg}`);
    }
  });
}
