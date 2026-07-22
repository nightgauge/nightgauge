/**
 * Pause Pipeline command
 *
 * Pauses the currently running pipeline at the next stage boundary.
 * The current stage will complete normally, then the pipeline holds
 * until the user resumes.
 *
 * @see Issue #239 - Pipeline pause/resume with cross-session recovery
 */

import * as vscode from "vscode";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { HeadlessOrchestrator } from "../services/HeadlessOrchestrator";
import type { Logger } from "../utils/logger";
import type { StatusBarManager } from "../utils/statusBar";

/**
 * Register the Pause Pipeline command
 *
 * This command sets the paused flag in PipelineStateService.
 * The orchestrator checks this flag after each stage completes
 * and stops progressing if paused.
 */
export function registerPausePipelineCommand(
  orchestrator: HeadlessOrchestrator | null,
  stateService: PipelineStateService | null,
  logger: Logger,
  statusBar: StatusBarManager
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.pausePipeline", async () => {
    // Check if state service is available
    if (!stateService) {
      vscode.window.showErrorMessage(
        "Nightgauge SDK not initialized. Check extension logs for details."
      );
      return;
    }

    // Check if there's an active pipeline
    const state = await stateService.getState();
    if (!state) {
      vscode.window.showInformationMessage("No active pipeline to pause.");
      return;
    }

    // Check if already paused
    if (state.paused) {
      vscode.window.showInformationMessage(
        'Pipeline is already paused. Click "Resume" to continue.'
      );
      return;
    }

    // Find the currently running stage (if any)
    let runningStage: string | null = null;
    for (const [stageName, stageState] of Object.entries(state.stages)) {
      if (stageState.status === "running") {
        runningStage = stageName;
        break;
      }
    }

    logger.info("Pausing pipeline", {
      issueNumber: state.issue_number,
      runningStage,
    });

    try {
      // Set paused flag in state service
      await stateService.pausePipeline();

      // Update status bar to show paused state
      statusBar.showPaused(runningStage || undefined);

      // Update context for UI
      vscode.commands.executeCommand("setContext", "nightgauge.pipelinePaused", true);

      // Show notification with Resume action
      const message = runningStage
        ? `Pipeline will pause after ${runningStage} completes.`
        : "Pipeline paused.";

      const selection = await vscode.window.showInformationMessage(message, "Resume");

      if (selection === "Resume") {
        vscode.commands.executeCommand("nightgauge.resumePipeline");
      }

      logger.info("Pipeline paused by user");
    } catch (error) {
      const message = error instanceof Error ? error.message : "Unknown error occurred";
      logger.error("Failed to pause pipeline", error instanceof Error ? error : undefined);
      vscode.window.showErrorMessage(`Failed to pause pipeline: ${message}`);
    }
  });
}
