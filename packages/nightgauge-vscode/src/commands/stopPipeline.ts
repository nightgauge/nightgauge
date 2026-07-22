/**
 * Stop Pipeline command — Pause / Preserve State
 *
 * Stops the currently running pipeline (single or concurrent) while
 * PRESERVING all state for potential resumption or inspection:
 * - GitHub issue state is NOT changed (stays open, board stays at current status)
 * - Context files are preserved
 * - Feature branches are preserved
 * - Worktrees are preserved (concurrent mode)
 *
 * For full rollback (reopen issue, reset board, delete branches), use
 * abortPipeline instead.
 *
 * @see Issue #1187 - Cancel pipeline with outcome tracking
 */

import * as vscode from "vscode";
import * as path from "node:path";
import type { HeadlessOrchestrator } from "../services/HeadlessOrchestrator";
import type { ConcurrentPipelineManager } from "../services/ConcurrentPipelineManager";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { Logger } from "../utils/logger";
import type { StatusBarManager } from "../utils/statusBar";
import { hasActiveProcess, killAllActiveProcesses } from "../utils/skillRunner";
import { RunStateManager } from "@nightgauge/sdk";
import { getWorkspaceRoot } from "../config/settings";

/**
 * Register the Stop Pipeline command
 *
 * This command handles both single pipeline and concurrent pipeline stops.
 * It preserves all GitHub and local state — use abortPipeline for full rollback.
 */
export function registerStopPipelineCommand(
  orchestrator: HeadlessOrchestrator | null,
  logger: Logger,
  statusBar: StatusBarManager,
  pipelineStateService?: PipelineStateService | null,
  concurrentPipelineManager?: ConcurrentPipelineManager | null
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.stopPipeline", async () => {
    // Check if orchestrator is available
    if (!orchestrator) {
      vscode.window.showErrorMessage(
        "Nightgauge SDK not initialized. Check extension logs for details."
      );
      return;
    }

    const isSingleRunning = orchestrator.getIsRunning();
    const hasConcurrentSlots = (concurrentPipelineManager?.activeSlotCount ?? 0) > 0;
    const hasOrphanedProcess = hasActiveProcess();

    // Check if anything is running
    if (!isSingleRunning && !hasConcurrentSlots && !hasOrphanedProcess) {
      vscode.window.showInformationMessage("No pipeline is currently running.");
      return;
    }

    // If orchestrator says nothing is running but a stage process is still alive,
    // offer a targeted cleanup path instead of reporting a false idle state.
    // Only enter this branch when there are NO concurrent slots — otherwise
    // fall through to the concurrent pipeline stop which calls abortAll().
    if (!isSingleRunning && !hasConcurrentSlots && hasOrphanedProcess) {
      const confirm = await vscode.window.showWarningMessage(
        "A stage process is still running even though pipeline state is idle. Stop it now?",
        { modal: true },
        "Stop Process"
      );

      if (confirm !== "Stop Process") {
        return;
      }

      killAllActiveProcesses();
      statusBar.showIdle();
      vscode.window.showInformationMessage("Stale stage process stopped.");
      logger.warn("Stopped orphaned stage process without active pipeline");
      return;
    }

    // Handle concurrent pipeline stop (check first — concurrent mode
    // uses ConcurrentPipelineManager, not HeadlessOrchestrator batch mode)
    if (concurrentPipelineManager && concurrentPipelineManager.activeSlotCount > 0) {
      const activeCount = concurrentPipelineManager.activeSlotCount;

      // Pause slot filling BEFORE showing the dialog. Without this, dying
      // slots refill from the queue while the user is reading the modal,
      // making it impossible to actually stop the pipeline.
      concurrentPipelineManager.pauseFilling();

      const confirm = await vscode.window.showWarningMessage(
        `Stop the pipeline? ${activeCount} concurrent slot(s) running. All running and queued issues will be stopped. GitHub status and branches are preserved — use Abort for full rollback.`,
        { modal: true },
        "Stop All"
      );

      if (confirm !== "Stop All") {
        concurrentPipelineManager.resumeFilling();
        return;
      }

      logger.info("Stopping concurrent pipeline via abortAll()", {
        activeSlots: activeCount,
      });

      try {
        await concurrentPipelineManager.abortAll();

        // Set cancelled outcome (Issue #1187)
        if (pipelineStateService) {
          try {
            await pipelineStateService.setOutcomeType("cancelled");
          } catch (error) {
            logger.warn("Failed to set cancelled outcome type", { error });
          }
        }

        // Update context for UI
        vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", false);

        statusBar.showIdle();
        vscode.window.showInformationMessage(
          "All concurrent pipelines stopped. State preserved — use Abort Pipeline for full rollback."
        );
        logger.info("Concurrent pipeline stopped by user (state preserved)");
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error occurred";
        logger.error("Failed to stop concurrent pipeline", {
          error: message,
        });
        vscode.window.showErrorMessage(`Failed to stop concurrent pipeline: ${message}`);
      }
      return;
    }

    // Handle single pipeline stop
    const currentStage = orchestrator.getCurrentStage();
    const confirm = await vscode.window.showWarningMessage(
      `Stop the pipeline? Currently running: ${currentStage || "unknown"}. State will be preserved — use Abort for full rollback.`,
      { modal: true },
      "Stop Pipeline"
    );

    if (confirm !== "Stop Pipeline") {
      return; // User cancelled
    }

    logger.info("Stopping pipeline (state preserved)", { currentStage });

    try {
      orchestrator.stop();

      // Persist the lifecycle transition: running → paused (Issue #3238).
      // Stop NEVER deletes branches, worktrees, or context files — discard
      // is the only destructive transition. ADR-001.
      try {
        const workspaceRoot = getWorkspaceRoot();
        if (workspaceRoot) {
          const rsm = new RunStateManager(path.join(workspaceRoot, ".nightgauge", "pipeline"));
          const existing = await rsm.read();
          if (existing && existing.state === "running") {
            await rsm.markPaused(
              "user clicked stop",
              (currentStage as "issue-pickup" | undefined) ?? undefined
            );
          }
        }
      } catch (error) {
        // Non-fatal — the rest of the stop flow still preserves in-memory
        // state. Surface only in logs so the user sees a clean Stop result.
        logger.warn("Failed to write run-state.json paused transition", { error });
      }

      // Set cancelled outcome (Issue #1187)
      if (pipelineStateService) {
        try {
          await pipelineStateService.setOutcomeType("cancelled");
        } catch (error) {
          logger.warn("Failed to set cancelled outcome type", { error });
        }
      }

      // NOTE: GitHub status is intentionally NOT reset here.
      // Stop = pause. The issue stays "In progress" on the board so it
      // isn't accidentally picked up by another pipeline run.
      // Use abortPipeline for full rollback (reopen + board reset).

      // Update context for UI
      vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", false);

      statusBar.showIdle();
      vscode.window.showInformationMessage(
        "Pipeline stopped. State preserved — use Abort Pipeline for full rollback."
      );
      logger.info("Pipeline stopped by user (state preserved)", {
        issueNumber: (await pipelineStateService?.getState())?.issue_number,
      });
    } catch (error) {
      const message = error instanceof Error ? error.message : "Unknown error occurred";
      logger.error("Failed to stop pipeline", error instanceof Error ? error : undefined);
      vscode.window.showErrorMessage(`Failed to stop pipeline: ${message}`);
    }
  });
}
