/**
 * Show Pipeline Summary command
 *
 * Displays the pipeline completion summary WebView panel.
 * Typically invoked automatically after pr-merge completes, but can also
 * be triggered manually via the command palette.
 *
 * @see docs/ARCHITECTURE.md - Context-Isolated Pipeline Architecture
 */

import * as vscode from "vscode";
import type { Logger } from "../utils/logger";
import type { PipelineStateService } from "../services/PipelineStateService";
import { PipelineSummary } from "../views/summary";

/**
 * Register the Show Pipeline Summary command
 *
 * This command can be invoked from:
 * 1. Automatically after pr-merge completes
 * 2. Command palette (Nightgauge: Show Pipeline Summary)
 */
export function registerShowPipelineSummaryCommand(
  extensionUri: vscode.Uri,
  pipelineStateService: PipelineStateService | null,
  logger: Logger
): vscode.Disposable {
  // Keep a reference to the summary panel for reuse
  let summaryPanel: PipelineSummary | null = null;

  return vscode.commands.registerCommand("nightgauge.showPipelineSummary", async () => {
    if (!pipelineStateService) {
      vscode.window.showErrorMessage("Pipeline service not available. Open a workspace first.");
      return;
    }

    try {
      const state = await pipelineStateService.getState();

      if (!state) {
        vscode.window.showWarningMessage(
          "No pipeline data available. Complete a pipeline run first."
        );
        return;
      }

      logger.info("Showing pipeline summary", {
        issueNumber: state.issue_number,
      });

      // Create or reuse summary panel
      if (!summaryPanel) {
        summaryPanel = new PipelineSummary(extensionUri);
      }

      await summaryPanel.show(state);
    } catch (error) {
      logger.error("Failed to show pipeline summary", { error });
      vscode.window.showErrorMessage("Failed to show pipeline summary");
    }
  });
}
