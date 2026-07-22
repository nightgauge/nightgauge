/**
 * Resume Pipeline command
 *
 * Resumes a paused pipeline, continuing from the next pending stage.
 * Uses runPipeline() for unified execution path per Issue #531.
 *
 * @see Issue #239 - Pipeline pause/resume with cross-session recovery
 * @see Issue #535 - Fix resume to use runPipeline() instead of runStage()
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { HeadlessOrchestrator, PipelineCallbacks } from "../services/HeadlessOrchestrator";
import type { ConcurrentPipelineManager } from "../services/ConcurrentPipelineManager";
import type { Logger } from "../utils/logger";
import type { StatusBarManager } from "../utils/statusBar";
import { getStageLabel } from "../utils/skillRunner";

/**
 * Pipeline stages in order for finding next stage
 */
const PIPELINE_STAGES: PipelineStage[] = [
  "pipeline-start",
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
  "pipeline-finish",
];

/**
 * Register the Resume Pipeline command
 *
 * This command clears the paused flag and triggers the full pipeline
 * via runPipeline(). The orchestrator handles skipping completed stages
 * and continues from the next pending stage.
 *
 * IMPORTANT: Uses runPipeline() for unified execution path (Issue #531).
 * This ensures pause checks, routing, and auto-continue work correctly.
 */
export function registerResumePipelineCommand(
  orchestrator: HeadlessOrchestrator | null,
  stateService: PipelineStateService | null,
  logger: Logger,
  statusBar: StatusBarManager,
  concurrentPipelineManager?: ConcurrentPipelineManager | null
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.resumePipeline", async () => {
    // Check if state service is available
    if (!stateService) {
      vscode.window.showErrorMessage(
        "Nightgauge SDK not initialized. Check extension logs for details."
      );
      return;
    }

    // Check if orchestrator is available
    if (!orchestrator) {
      vscode.window.showErrorMessage(
        "Pipeline orchestrator not initialized. Check extension logs for details."
      );
      return;
    }

    // Check if there's an active pipeline
    const state = await stateService.getState();
    if (!state) {
      vscode.window.showInformationMessage("No active pipeline to resume.");
      return;
    }

    // Check if actually paused
    if (!state.paused) {
      vscode.window.showInformationMessage("Pipeline is not paused.");
      return;
    }

    // Find the next pending stage for logging
    let nextStage: PipelineStage | null = null;
    let lastCompletedStage: PipelineStage | null = null;

    for (const stage of PIPELINE_STAGES) {
      const stageState = state.stages[stage];
      if (stageState.status === "complete" || stageState.status === "skipped") {
        lastCompletedStage = stage;
      } else if (stageState.status === "pending" && !nextStage) {
        nextStage = stage;
        break;
      } else if (stageState.status === "running") {
        // If a stage is still running, it will complete and check paused flag
        nextStage = null;
        break;
      }
    }

    logger.info("Resuming pipeline", {
      issueNumber: state.issue_number,
      lastCompletedStage,
      nextStage,
    });

    try {
      // Clear paused flag in state service
      await stateService.resumePipeline();

      // Update context for UI
      vscode.commands.executeCommand("setContext", "nightgauge.pipelinePaused", false);

      // Set pipeline running context
      vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", true);

      // Check if the Go-driven concurrent pipeline is active.
      // When it is, the Go scheduler drives stage progression — we just
      // need to restore the UI state and let it continue naturally.
      const goActive = concurrentPipelineManager && concurrentPipelineManager.activeSlotCount > 0;

      if (goActive) {
        // Go scheduler is driving — restore UI without calling
        // HeadlessOrchestrator.runPipeline() (which would create a
        // duplicate legacy execution path and reset pipelineRunning
        // to false on completion, hiding the control buttons).
        // Show running state — use the next pending stage or fallback to pipeline-start
        const goResumeStage = nextStage ?? "pipeline-start";
        statusBar.showRunning(goResumeStage);
        vscode.window.showInformationMessage("Pipeline resumed.");
        logger.info("Pipeline resumed (Go-driven path)", {
          issueNumber: state.issue_number,
          activeSlots: concurrentPipelineManager.activeSlotCount,
        });
      } else if (nextStage) {
        // Legacy HeadlessOrchestrator path — no Go slots active
        statusBar.showRunning(nextStage);

        const stageLabel = getStageLabel(nextStage);
        vscode.window.showInformationMessage(`Pipeline resumed. Running ${stageLabel}...`);

        // Use runPipeline() for unified execution path (Issue #531, #535)
        logger.info("Calling runPipeline for resume", {
          issueNumber: state.issue_number,
          nextStage,
        });

        // Run pipeline asynchronously - don't await to allow UI to update
        orchestrator
          .runPipeline(state.issue_number)
          .then((result) => {
            if (result.success) {
              logger.info("Pipeline resumed and completed successfully", {
                issueNumber: state.issue_number,
                completedStages: result.completedStages,
              });
            } else {
              logger.error("Pipeline resumed but failed", {
                issueNumber: state.issue_number,
                failedStage: result.failedStage,
                error: result.error,
              });
            }
          })
          .catch((error) => {
            logger.error("Pipeline resume error", {
              issueNumber: state.issue_number,
              error: error instanceof Error ? error.message : String(error),
            });
            vscode.window.showErrorMessage(
              `Pipeline error: ${error instanceof Error ? error.message : "Unknown error"}`
            );
          });

        logger.info("Pipeline resumed via runPipeline", { nextStage });
      } else {
        // No pending stages - pipeline may be complete or a stage is still running
        statusBar.showIdle();
        vscode.window.showInformationMessage(
          "Pipeline resumed. Waiting for current stage to complete."
        );
        logger.info("Pipeline resumed, no pending stages");
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : "Unknown error occurred";
      logger.error("Failed to resume pipeline", error instanceof Error ? error : undefined);
      vscode.window.showErrorMessage(`Failed to resume pipeline: ${message}`);
    }
  });
}
