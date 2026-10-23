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
import type { OutputWindow } from "../views";
import type {
  HeadlessOrchestrator,
  PipelineCallbacks,
  StageRunResult,
} from "../services/HeadlessOrchestrator";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { PhaseTreeItem } from "../views/items/PhaseTreeItem";
import { createStreamOutputHandler } from "../utils/streamOutputHandler";
import { createPhaseTracker } from "../utils/phaseTracker";
import { getRetryStatusMessage, isRecoveryShapedError } from "./retry-error-presentation";
import type { RecoveryPresenter } from "../orchestrator/recovery/RecoveryCoordinator";
import { completeSuccessfulRetry } from "./retry-success";

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
  outputWindow?: OutputWindow | null,
  presentRecovery?: RecoveryPresenter
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
      const targetStage = item.stage;
      if (!targetStage) {
        vscode.window.showErrorMessage(
          `Could not determine the owning stage for phase "${phaseName}".`
        );
        return;
      }

      // Find which stage the failed phase belongs to using state service
      if (!stateService) {
        vscode.window.showErrorMessage(
          "Pipeline state service not available. Cannot determine stage for phase."
        );
        return;
      }

      let state: Awaited<ReturnType<PipelineStateService["getState"]>>;
      let recoveryPresented = false;
      try {
        state = await stateService.getState();
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error occurred";
        logger.error("Retry from phase state error", {
          stage: targetStage,
          phase: phaseName,
          error: message,
        });
        statusBar.showError("Unable to read pipeline state");
        vscode.window.showErrorMessage(
          "Unable to read pipeline state. Check extension logs for details."
        );
        return;
      }
      if (!state) {
        vscode.window.showErrorMessage(
          "No pipeline state found. Cannot determine stage for phase."
        );
        return;
      }

      const targetStageState = state.stages[targetStage];
      if (!targetStageState?.phases?.some((phase) => phase.name === phaseName)) {
        vscode.window.showErrorMessage(
          `Could not find phase "${phaseName}" in stage "${targetStage}".`
        );
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

      let presentDerivedRecovery = (_error: unknown): boolean => false;
      const finishSuccess = (durationMs?: number) =>
        completeSuccessfulRetry({
          stage: targetStage,
          issueNumber,
          durationMs,
          stateService,
          logger,
          statusBar,
          source: "retry-from-phase",
        });
      try {
        // Phase tracking for pipeline tree view progress (@see Issue #1115)
        const phaseTracker = stateService ? createPhaseTracker(stateService) : null;
        const streamHandler = outputWindow
          ? createStreamOutputHandler(outputWindow, {
              onPhaseDetected: phaseTracker?.onPhaseDetected,
            })
          : null;

        // Use HeadlessOrchestrator.runStage with skipToPhase
        const callbacks: PipelineCallbacks = {
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
            const payload = orchestrator.getRecoveryShape(error, issueNumber, targetStage);
            if (!payload) return false;
            presentRecovery(payload);
            recoveryPresented = true;
            return true;
          } catch (shapeError) {
            logger.warn("Failed to derive phase retry recovery shape", { error: shapeError });
            return false;
          }
        };

        const result = await orchestrator.runStage(targetStage, issueNumber, callbacks, phaseName);

        if (result.success) {
          await finishSuccess(result.durationMs);
        } else {
          const recoveryShaped = isRecoveryShapedError(result.error);
          if (recoveryShaped && !recoveryPresented) {
            presentDerivedRecovery(result.error);
          }
          logger.warn("Retry from phase failed", {
            stage: targetStage,
            phase: phaseName,
            issueNumber,
            error: result.error,
            recoveryShaped,
          });
          statusBar.showError(getRetryStatusMessage(result.error, "Stage failed"));
          if (recoveryShaped && !recoveryPresented) {
            vscode.window.showErrorMessage(
              `Stage "${targetStage}" requires recovery. Check extension logs for details.`
            );
          } else if (!recoveryShaped) {
            vscode.window.showErrorMessage(
              `Stage "${targetStage}" failed: ${result.error || "Unknown error"}`
            );
          }
        }
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error occurred";
        const recoveryShaped = isRecoveryShapedError(error);
        if (recoveryShaped && !recoveryPresented) {
          presentDerivedRecovery(error);
        }
        logger.error("Retry from phase error", {
          stage: targetStage,
          phase: phaseName,
          issueNumber,
          error: message,
          recoveryShaped,
        });
        statusBar.showError(getRetryStatusMessage(error, message));
        if (recoveryShaped && !recoveryPresented) {
          vscode.window.showErrorMessage(
            "Retry from phase requires recovery. Check extension logs for details."
          );
        } else if (!recoveryShaped) {
          vscode.window.showErrorMessage(`Retry from phase error: ${message}`);
        }
      }
    }
  );
}
