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
import type { PipelineStage } from "@nightgauge/sdk";
import type { Logger } from "../utils/logger";
import type { StatusBarManager } from "../utils/statusBar";
import type { StageTreeItem, OutputWindow } from "../views";
import type {
  HeadlessOrchestrator,
  PipelineCallbacks,
  StageRunResult,
} from "../services/HeadlessOrchestrator";
import type { PipelineStateService } from "../services/PipelineStateService";
import { MAX_STAGE_RETRIES } from "../utils/stageTransitionValidator";
import { createStreamOutputHandler } from "../utils/streamOutputHandler";
import { createPhaseTracker } from "../utils/phaseTracker";
import { parsePositiveIssueNumber, validatePositiveIssueNumber } from "../utils/issue-number-input";
import { getRetryStatusMessage, isRecoveryShapedError } from "./retry-error-presentation";
import type { RecoveryPresenter } from "../orchestrator/recovery/RecoveryCoordinator";
import { completeSuccessfulRetry } from "./retry-success";

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
  outputWindow?: OutputWindow | null,
  presentRecovery?: RecoveryPresenter
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
        validateInput: validatePositiveIssueNumber,
      });

      if (!input) {
        return;
      }

      const parsedIssueNumber = parsePositiveIssueNumber(input);
      if (parsedIssueNumber === null) {
        vscode.window.showErrorMessage("Please enter a valid positive issue number");
        return;
      }
      issueNumber = parsedIssueNumber;
    }

    // Clear stage error before retry (if item was provided)
    if (item && "clearError" in item) {
      item.clearError();
    }

    logger.info("Retrying stage", { stage, issueNumber });
    statusBar.showRunning(stage);

    let recoveryPresented = false;
    let presentDerivedRecovery = (_error: unknown): boolean => false;
    const finishSuccess = (durationMs?: number) =>
      completeSuccessfulRetry({
        stage,
        issueNumber,
        durationMs,
        stateService,
        logger,
        statusBar,
        source: "retry",
      });
    try {
      // Phase tracking for pipeline tree view progress (@see Issue #1115)
      const phaseTracker = stateService ? createPhaseTracker(stateService) : null;
      const streamHandler = outputWindow
        ? createStreamOutputHandler(outputWindow, {
            onPhaseDetected: phaseTracker?.onPhaseDetected,
          })
        : null;

      // Use HeadlessOrchestrator.runStage for proper execution
      const callbacks: PipelineCallbacks = {
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
        onRecoveryRequired: presentRecovery
          ? (payload) => {
              presentRecovery(payload);
              recoveryPresented = true;
            }
          : undefined,
      };
      presentDerivedRecovery = (error: unknown): boolean => {
        if (!presentRecovery) return false;
        try {
          const payload = orchestrator.getRecoveryShape(error, issueNumber, stage);
          if (!payload) return false;
          presentRecovery(payload);
          recoveryPresented = true;
          return true;
        } catch (shapeError) {
          logger.warn("Failed to derive retry recovery shape", { error: shapeError });
          return false;
        }
      };
      const result = await orchestrator.runStage(stage, issueNumber, callbacks);

      if (result.success) {
        await finishSuccess(result.durationMs);
      } else {
        // Recovery-shaped failures are surfaced through the callback when the
        // orchestrator emits it, or derived from the returned error below.
        // Suppress the flat toast to avoid duplicate UI noise.
        const recoveryShaped = isRecoveryShapedError(result.error);
        if (recoveryShaped && !recoveryPresented) {
          presentDerivedRecovery(result.error);
        }
        logger.warn("Stage retry failed", {
          stage,
          issueNumber,
          error: result.error,
          recoveryShaped,
        });
        statusBar.showError(getRetryStatusMessage(result.error, "Stage failed"));
        if (recoveryShaped && !recoveryPresented) {
          vscode.window.showErrorMessage(
            `Stage "${stage}" requires recovery. Check extension logs for details.`
          );
        } else if (!recoveryShaped) {
          vscode.window.showErrorMessage(
            `Stage "${stage}" failed: ${result.error || "Unknown error"}`
          );
        }
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : "Unknown error occurred";
      const recoveryShaped = isRecoveryShapedError(error);
      if (recoveryShaped && !recoveryPresented) {
        presentDerivedRecovery(error);
      }
      logger.error("Stage retry error", {
        stage,
        issueNumber,
        error: message,
        recoveryShaped,
      });
      statusBar.showError(getRetryStatusMessage(error, message));
      if (recoveryShaped && !recoveryPresented) {
        vscode.window.showErrorMessage(
          "Stage retry requires recovery. Check extension logs for details."
        );
      } else if (!recoveryShaped) {
        vscode.window.showErrorMessage(`Stage retry error: ${message}`);
      }
    }
  });
}
