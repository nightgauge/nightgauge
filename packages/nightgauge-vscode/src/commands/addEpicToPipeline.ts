/**
 * Add Epic to Pipeline Command
 *
 * Adds all sub-issues of an epic to the pipeline queue.
 * Mirrors addIssueToPipeline but operates on EpicGroupTreeItem.
 *
 * @see addIssueToPipeline.ts for the single-issue equivalent
 */

import * as vscode from "vscode";
import { EpicGroupTreeItem } from "../views/items/EpicGroupTreeItem";
import type { IssueQueueService } from "../services/IssueQueueService";
import type { TierGate } from "../platform/TierGate";
import type { LicensePreflight } from "../platform/LicensePreflight";
import type { Logger } from "../utils/logger";

/**
 * Register the Add Epic to Pipeline command
 *
 * Queues all sub-issues of an epic via enqueueEpic(),
 * which handles assessment, strategy selection, and insertion ordering.
 */
export function registerAddEpicToPipelineCommand(
  queueService: IssueQueueService,
  logger: Logger,
  tierGate?: TierGate | null,
  licensePreflight?: LicensePreflight | null
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.addEpicToPipeline",
    async (item?: EpicGroupTreeItem) => {
      try {
        // Tier gate: batch-processing requires pro tier (Issue #1472)
        if (tierGate && licensePreflight) {
          const preflightResult = await licensePreflight.validate();
          const gate = tierGate.check("batch-processing", preflightResult.tier);
          if (!gate.allowed) {
            const action = await vscode.window.showInformationMessage(
              `Batch pipeline requires ${gate.requiredTier} tier. Upgrade to unlock all features.`,
              "View Plans"
            );
            if (action === "View Plans") {
              void vscode.env.openExternal(vscode.Uri.parse(gate.upgradeUrl));
            }
            return;
          }
        }

        if (!item || !(item instanceof EpicGroupTreeItem) || !item.epic) {
          logger.error("addEpicToPipeline: Invalid item parameter");
          vscode.window.showErrorMessage("Please select a valid epic group");
          return;
        }

        const { number: epicNumber, title } = item.epic;
        const childCount = item.getChildIssueNumbers().length;

        logger.info("Adding epic to pipeline queue", {
          epicNumber,
          childCount,
        });

        const result = await queueService.enqueue(epicNumber, title, ["type:epic"]);

        // User cancelled from a dialog (e.g. blocked-issue warning)
        if (result === null) {
          return;
        }

        vscode.window.showInformationMessage(
          `Epic #${epicNumber} (${childCount} issues) added to pipeline`
        );

        logger.info("Successfully added epic to pipeline", {
          epicNumber,
          childCount,
        });

        // Focus pipeline view to show the newly added items
        await vscode.commands.executeCommand("nightgauge.pipelineView.focus");
      } catch (error) {
        logger.error("Failed to add epic to pipeline", { error });
        vscode.window.showErrorMessage("Failed to add epic to pipeline");
      }
    }
  );
}
