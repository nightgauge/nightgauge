/**
 * Retry Stage command
 *
 * Retries a failed or aborted pipeline stage.
 *
 * Enhanced for Issue #212:
 * - Auto-detects issue number from PipelineStateService (no prompt when state exists)
 * - Detects aborted stages (running but no active process)
 * - Clears stage error before retry
 * - Respects circuit breaker (MAX_STAGE_RETRIES=3)
 */

import * as vscode from "vscode";
import { PipelineStateError, type PipelineStage } from "@nightgauge/sdk";
import type { Logger } from "../utils/logger";
import type { StatusBarManager } from "../utils/statusBar";
import type { StageTreeItem, OutputWindow } from "../views";
import type { HeadlessOrchestrator, StageRunResult } from "../services/HeadlessOrchestrator";
import type { PipelineStateService } from "../services/PipelineStateService";

/**
 * Codes for failures the Recovery Dialog handles. When a stage retry
 * surfaces one of these, the dispatcher already emitted
 * `onRecoveryRequired` — we suppress the duplicate flat-error toast.
 */
const RECOVERY_ERROR_CODES = new Set([
  "MISSING_INPUT_FILE",
  "CONTEXT_SCHEMA_ERROR",
  "WORKTREE_MISSING",
  "RUN_STATE_MISSING",
]);

function isRecoveryShapedError(err: unknown): boolean {
  return err instanceof PipelineStateError && RECOVERY_ERROR_CODES.has(err.code);
}
import { MAX_STAGE_RETRIES } from "../utils/stageTransitionValidator";
import { getNextStage, getStageLabel } from "../utils/skillRunner";
import { createStreamOutputHandler } from "../utils/streamOutputHandler";
import { createPhaseTracker } from "../utils/phaseTracker";

/**
 * Register the Retry Stage command
 *
 * @param orchestrator - HeadlessOrchestrator for running stages
 * @param stateService - PipelineStateService for context detection
 * @param logger - Logger instance
 * @param statusBar - Status bar manager
 * @param outputWindow - OutputWindow for stream output and phase detection
 */
export function registerRetryStageCommand(
  orchestrator: HeadlessOrchestrator | null,
  stateService: PipelineStateService | null,
  logger: Logger,
  statusBar: StatusBarManager,
  outputWindow?: OutputWindow | null
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.retryStage", async (item?: StageTreeItem) => {
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

    // Get the stage to retry
    let stage: PipelineStage | undefined;

    if (item && "stage" in item) {
      stage = item.stage;

      // Check if the stage is retryable (handles aborted case)
      const isPipelineRunning = orchestrator.getIsRunning();
      if (!item.isRetryable(isPipelineRunning)) {
        // Check if circuit breaker is the reason
        const retryCount = item.getRetryCount();
        if (retryCount !== null && retryCount >= MAX_STAGE_RETRIES) {
          vscode.window.showErrorMessage(
            `Stage "${stage}" has been retried ${retryCount} times. ` +
              `Maximum retries (${MAX_STAGE_RETRIES}) exceeded. ` +
              `Use "Reset Pipeline" to clear retry counts.`
          );
        } else {
          vscode.window.showWarningMessage(`Stage "${stage}" is not in a retryable state.`);
        }
        return;
      }
    } else {
      // Prompt user to select a stage
      const selection = await vscode.window.showQuickPick(
        [
          { label: "Issue Pickup", value: "issue-pickup" as PipelineStage },
          {
            label: "Feature Planning",
            value: "feature-planning" as PipelineStage,
          },
          {
            label: "Feature Development",
            value: "feature-dev" as PipelineStage,
          },
          {
            label: "Feature Validation",
            value: "feature-validate" as PipelineStage,
          },
          { label: "PR Creation", value: "pr-create" as PipelineStage },
          { label: "PR Merge", value: "pr-merge" as PipelineStage },
        ],
        { placeHolder: "Select stage to retry" }
      );

      if (!selection) {
        return;
      }

      stage = selection.value;
    }

    // Get issue number from state service or prompt
    let issueNumber: number | undefined;

    // Try to get from PipelineStateService first (preferred)
    if (stateService) {
      try {
        const state = await stateService.getState();
        if (state?.issue_number) {
          issueNumber = state.issue_number;
          logger.debug("Issue number auto-detected from state", {
            issueNumber,
          });
        }
      } catch (error) {
        logger.warn("Failed to get issue number from state service", {
          error,
        });
      }
    }

    // Fall back to prompt if state not available
    if (!issueNumber) {
      const input = await vscode.window.showInputBox({
        prompt: "Enter issue number",
        placeHolder: "42",
        validateInput: (value) => {
          const num = parseInt(value, 10);
          if (isNaN(num) || num <= 0) {
            return "Please enter a valid positive issue number";
          }
          return null;
        },
      });

      if (!input) {
        return;
      }

      issueNumber = parseInt(input, 10);
    }

    // Clear stage error before retry (if item was provided)
    if (item && "clearError" in item) {
      item.clearError();
    }

    logger.info("Retrying stage", { stage, issueNumber });
    statusBar.showRunning(stage);

    try {
      // Phase tracking for pipeline tree view progress (@see Issue #1115)
      const phaseTracker = stateService ? createPhaseTracker(stateService) : null;
      const streamHandler = outputWindow
        ? createStreamOutputHandler(outputWindow, {
            onPhaseDetected: phaseTracker?.onPhaseDetected,
          })
        : null;

      // Use HeadlessOrchestrator.runStage for proper execution
      const result = await orchestrator.runStage(stage, issueNumber, {
        onStageStart: (s: PipelineStage) => {
          logger.debug("Retry stage started", { stage: s, issueNumber });
        },
        onStageComplete: (s: PipelineStage, r: StageRunResult) => {
          // Flush stream buffer and complete phases before marking done
          streamHandler?.flushStage(s);
          phaseTracker?.completeStagePhases(s);

          logger.debug("Retry stage completed", {
            stage: s,
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
          logger.error("Retry stage error", {
            stage: s,
            issueNumber,
            error: error instanceof Error ? error.message : String(error ?? "Unknown error"),
          });
        },
        onBackwardTransitionConfirm: async (s: PipelineStage, message: string) => {
          // Show confirmation dialog for backward transitions
          const selection = await vscode.window.showWarningMessage(
            message,
            { modal: true },
            "Continue"
          );
          return selection === "Continue";
        },
      });

      if (result.success) {
        logger.info("Stage retry completed successfully", {
          stage,
          issueNumber,
          durationMs: result.durationMs,
        });
        statusBar.showComplete(stage);

        // Auto-continue to next stage, respecting execution mode
        // This mirrors the logic in runStage.ts so retried stages
        // resume the pipeline instead of stopping after one stage.
        const nextStage = getNextStage(stage);
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
                logger.info("Auto-continuing after retry (automatic mode)", {
                  stage,
                  nextStage,
                  issueNumber,
                });
                setTimeout(() => {
                  vscode.commands.executeCommand("nightgauge.runStage", nextStage);
                }, delay);
              } else {
                logger.info("Auto-continuing after retry (manual mode)", {
                  stage,
                  nextStage,
                  issueNumber,
                });
                setTimeout(() => {
                  vscode.window
                    .showInformationMessage(
                      `${getStageLabel(stage)} complete. Continue to ${getStageLabel(nextStage)}?`,
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
                `Stage "${stage}" completed successfully. Pipeline is paused.`
              );
            }
          } else {
            vscode.window.showInformationMessage(`Stage "${stage}" completed successfully`);
          }
        } else {
          vscode.window.showInformationMessage(`Stage "${stage}" completed successfully`);
        }
      } else {
        // Recovery-shaped failures (MissingInputFile, ContextSchemaError,
        // WorktreeMissing, RunStateMissing) are surfaced via the Recovery
        // Dialog by HeadlessOrchestrator's dispatcher. Suppress the flat
        // error toast here to avoid duplicate UI noise (Issue #3239).
        const recoveryShaped = isRecoveryShapedError(result.error);
        logger.warn("Stage retry failed", {
          stage,
          issueNumber,
          error: result.error,
          recoveryShaped,
        });
        statusBar.showError(
          result.error instanceof Error
            ? result.error.message
            : String(result.error ?? "Stage failed")
        );
        if (!recoveryShaped) {
          vscode.window.showErrorMessage(
            `Stage "${stage}" failed: ${result.error || "Unknown error"}`
          );
        }
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : "Unknown error occurred";
      const recoveryShaped = isRecoveryShapedError(error);
      logger.error("Stage retry error", {
        stage,
        issueNumber,
        error: message,
        recoveryShaped,
      });
      statusBar.showError(message);
      if (!recoveryShaped) {
        vscode.window.showErrorMessage(`Stage retry error: ${message}`);
      }
    }
  });
}
