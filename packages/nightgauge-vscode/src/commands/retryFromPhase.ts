/**
 * Retry From Phase command (Issue #1187)
 *
 * Retries a pipeline stage starting from a specific failed phase,
 * skipping all phases that completed successfully before the failure.
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import type { Logger } from "../utils/logger";
import type { StatusBarManager } from "../utils/statusBar";
import type { StageTreeItem, OutputWindow } from "../views";
import type { HeadlessOrchestrator, StageRunResult } from "../services/HeadlessOrchestrator";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { PhaseTreeItem } from "../views/items/PhaseTreeItem";
import { getNextStage, getStageLabel } from "../utils/skillRunner";
import { createStreamOutputHandler } from "../utils/streamOutputHandler";
import { createPhaseTracker } from "../utils/phaseTracker";

/**
 * Register the Retry From Phase command (Issue #1187)
 *
 * Allows retrying a pipeline stage from a specific failed phase via
 * the tree view context menu, using the `skipToPhase` parameter of
 * `HeadlessOrchestrator.runStage()`.
 *
 * @param orchestrator - HeadlessOrchestrator for running stages
 * @param stateService - PipelineStateService for context detection
 * @param logger - Logger instance
 * @param statusBar - Status bar manager
 * @param outputWindow - OutputWindow for stream output and phase detection
 */
export function registerRetryFromPhaseCommand(
  orchestrator: HeadlessOrchestrator | null,
  stateService: PipelineStateService | null,
  logger: Logger,
  statusBar: StatusBarManager,
  outputWindow?: OutputWindow | null
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.retryFromPhase",
    async (item?: PhaseTreeItem) => {
      // Check if orchestrator is available
      if (!orchestrator) {
        vscode.window.showErrorMessage(
          "Nightgauge orchestrator not initialized. Check extension logs for details."
        );
        return;
      }

      // Check if already running
      if (orchestrator.getIsRunning()) {
        vscode.window.showWarningMessage(
          "Pipeline is already running. Stop it first or wait for completion."
        );
        return;
      }

      // Extract phase name from the tree item
      if (!item || !("phaseName" in item)) {
        vscode.window.showErrorMessage("Retry From Phase must be invoked from a phase tree item.");
        return;
      }

      const phaseName = item.phaseName;

      // Find which stage the failed phase belongs to using state service
      if (!stateService) {
        vscode.window.showErrorMessage(
          "Pipeline state service not available. Cannot determine stage for phase."
        );
        return;
      }

      const state = await stateService.getState();
      if (!state) {
        vscode.window.showErrorMessage(
          "No pipeline state found. Cannot determine stage for phase."
        );
        return;
      }

      let targetStage: PipelineStage | undefined;
      for (const [stageName, stageState] of Object.entries(state.stages)) {
        if (stageState.phases?.some((p) => p.name === phaseName)) {
          targetStage = stageName as PipelineStage;
          break;
        }
      }

      if (!targetStage) {
        vscode.window.showErrorMessage(`Could not find stage containing phase "${phaseName}".`);
        return;
      }

      // Get issue number from state
      const issueNumber = state.issue_number;
      if (!issueNumber) {
        vscode.window.showErrorMessage("No issue number found in pipeline state.");
        return;
      }

      logger.info("Retrying stage from phase", {
        stage: targetStage,
        phase: phaseName,
        issueNumber,
      });
      statusBar.showRunning(targetStage);

      try {
        // Phase tracking for pipeline tree view progress (@see Issue #1115)
        const phaseTracker = stateService ? createPhaseTracker(stateService) : null;
        const streamHandler = outputWindow
          ? createStreamOutputHandler(outputWindow, {
              onPhaseDetected: phaseTracker?.onPhaseDetected,
            })
          : null;

        // Use HeadlessOrchestrator.runStage with skipToPhase
        const result = await orchestrator.runStage(
          targetStage,
          issueNumber,
          {
            onStageStart: (s: PipelineStage) => {
              logger.debug("Retry-from-phase stage started", {
                stage: s,
                phase: phaseName,
                issueNumber,
              });
            },
            onStageComplete: (s: PipelineStage, r: StageRunResult) => {
              // Flush stream buffer and complete phases before marking done
              streamHandler?.flushStage(s);
              phaseTracker?.completeStagePhases(s);

              logger.debug("Retry-from-phase stage completed", {
                stage: s,
                phase: phaseName,
                issueNumber,
                success: r.success,
              });
            },
            onStdout: (s: PipelineStage, data: string) => {
              streamHandler?.onStdout(s, data);
            },
            onStderr: (s: PipelineStage, data: string) => {
              streamHandler?.onStderr(s, data);
            },
            onStageError: (s: PipelineStage, error: Error) => {
              logger.error("Retry-from-phase stage error", {
                stage: s,
                phase: phaseName,
                issueNumber,
                error: error instanceof Error ? error.message : String(error ?? "Unknown error"),
              });
            },
            onBackwardTransitionConfirm: async (s: PipelineStage, message: string) => {
              const selection = await vscode.window.showWarningMessage(
                message,
                { modal: true },
                "Continue"
              );
              return selection === "Continue";
            },
          },
          phaseName
        );

        if (result.success) {
          logger.info("Retry from phase completed successfully", {
            stage: targetStage,
            phase: phaseName,
            issueNumber,
            durationMs: result.durationMs,
          });
          statusBar.showComplete(targetStage);

          // Auto-continue to next stage, respecting execution mode
          const nextStage = getNextStage(targetStage);
          if (stateService && nextStage && nextStage !== "pipeline-finish") {
            const autoContinue = vscode.workspace
              .getConfiguration("nightgauge.pipeline")
              .get("autoContinue", true);

            if (autoContinue) {
              const isPaused = await stateService.isPaused();
              if (!isPaused) {
                const executionMode = await stateService.getExecutionMode();
                const delay = vscode.workspace
                  .getConfiguration("nightgauge.pipeline")
                  .get("autoContinueDelay", 1000);

                if (executionMode === "automatic") {
                  logger.info("Auto-continuing after retry-from-phase (automatic mode)", {
                    stage: targetStage,
                    nextStage,
                    issueNumber,
                  });
                  setTimeout(() => {
                    vscode.commands.executeCommand("nightgauge.runStage", nextStage);
                  }, delay);
                } else {
                  logger.info("Auto-continuing after retry-from-phase (manual mode)", {
                    stage: targetStage,
                    nextStage,
                    issueNumber,
                  });
                  setTimeout(() => {
                    vscode.window
                      .showInformationMessage(
                        `${getStageLabel(targetStage)} complete. Continue to ${getStageLabel(nextStage)}?`,
                        "Run Now",
                        "Yes to All",
                        "Pause"
                      )
                      .then(async (selection) => {
                        if (selection === "Run Now") {
                          await stateService.resumePipeline();
                          vscode.commands.executeCommand("nightgauge.runStage", nextStage);
                        } else if (selection === "Yes to All") {
                          await stateService.setExecutionMode("automatic");
                          await stateService.resumePipeline();
                          vscode.commands.executeCommand("nightgauge.runStage", nextStage);
                        } else {
                          await stateService.pausePipeline();
                          vscode.window.showInformationMessage(
                            `Pipeline paused. Run "${getStageLabel(nextStage)}" to continue.`
                          );
                        }
                      });
                  }, delay);
                }
              } else {
                vscode.window.showInformationMessage(
                  `Stage "${targetStage}" completed successfully. Pipeline is paused.`
                );
              }
            } else {
              vscode.window.showInformationMessage(`Stage "${targetStage}" completed successfully`);
            }
          } else {
            vscode.window.showInformationMessage(`Stage "${targetStage}" completed successfully`);
          }
        } else {
          logger.warn("Retry from phase failed", {
            stage: targetStage,
            phase: phaseName,
            issueNumber,
            error: result.error,
          });
          statusBar.showError(
            result.error instanceof Error
              ? result.error.message
              : String(result.error ?? "Stage failed")
          );
          vscode.window.showErrorMessage(
            `Stage "${targetStage}" failed: ${result.error || "Unknown error"}`
          );
        }
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error occurred";
        logger.error("Retry from phase error", {
          stage: targetStage,
          phase: phaseName,
          issueNumber,
          error: message,
        });
        statusBar.showError(message);
        vscode.window.showErrorMessage(`Retry from phase error: ${message}`);
      }
    }
  );
}
