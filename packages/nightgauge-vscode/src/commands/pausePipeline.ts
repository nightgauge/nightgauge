/**
 * Pause Pipeline command
 *
 * Pauses the currently running pipeline at the next stage boundary.
 * The current stage will complete normally, then the pipeline holds
 * until the user resumes.
 *
 * Targets the run it acts on rather than the singleton PipelineStateService
 * (#423, ADR-017 follow-up to #370 step 3 / PR #421): when a concurrent-slot
 * run is the only thing live, the singleton holds no run identity, so the
 * command resolves the intended target via {@link resolveTargetRunService}
 * instead of hard-targeting the singleton.
 *
 * @see Issue #239 - Pipeline pause/resume with cross-session recovery
 * @see Issue #423 - retarget pause/resume at the run they act on
 */

import * as vscode from "vscode";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { HeadlessOrchestrator } from "../services/HeadlessOrchestrator";
import type { ConcurrentPipelineManager } from "../services/ConcurrentPipelineManager";
import type { Logger } from "../utils/logger";
import type { StatusBarManager } from "../utils/statusBar";
import { resolveTargetRunService } from "./runSelector";

/**
 * Register the Pause Pipeline command
 *
 * This command sets the paused flag in the resolved run's
 * PipelineStateService. The orchestrator checks this flag after each stage
 * completes and stops progressing if paused.
 */
export function registerPausePipelineCommand(
  orchestrator: HeadlessOrchestrator | null,
  stateService: PipelineStateService | null,
  logger: Logger,
  statusBar: StatusBarManager,
  concurrentPipelineManager?: ConcurrentPipelineManager | null
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.pausePipeline", async () => {
    // Check if state service is available
    if (!stateService) {
      vscode.window.showErrorMessage(
        "Nightgauge SDK not initialized. Check extension logs for details."
      );
      return;
    }

    // Resolve which live run this command should act on — the singleton
    // when it holds a run, one of the active concurrent slots otherwise, or
    // a QuickPick when more than one run is live. `null` means no run
    // anywhere holds an identity to pause.
    const target = await resolveTargetRunService(stateService, concurrentPipelineManager);
    if (!target) {
      vscode.window.showInformationMessage("No active pipeline to pause.");
      return;
    }
    const { service, issueNumber } = target;

    // Check if there's an active pipeline
    const state = await service.getState();
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
      issueNumber,
      runningStage,
    });

    try {
      // Set paused flag in the resolved service. It reports whether the
      // pause was PERSISTED to Go — `false` when that service holds no run
      // identity (every concurrent-slot run, ADR-017 Decision 10), in which
      // case the pause is in-memory only and does not survive a reload.
      const persisted = await service.pausePipeline();

      // Update status bar to show paused state
      statusBar.showPaused(runningStage || undefined);

      // Update context for UI
      vscode.commands.executeCommand("setContext", "nightgauge.pipelinePaused", true);

      // Show notification with Resume action. Say plainly when the pause was
      // not written to Go — telling the operator "Pipeline paused." while
      // nothing persisted is how #239's cross-session recovery silently stops
      // working for slot runs.
      const base = runningStage
        ? `Pipeline will pause after ${runningStage} completes.`
        : "Pipeline paused.";
      const message = persisted
        ? base
        : `${base} This session only — not persisted (no run identity; ADR-017 step 8).`;

      const selection = await vscode.window.showInformationMessage(message, "Resume");

      if (selection === "Resume") {
        vscode.commands.executeCommand("nightgauge.resumePipeline");
      }

      logger.info("Pipeline paused by user", { issueNumber });
    } catch (error) {
      const message = error instanceof Error ? error.message : "Unknown error occurred";
      logger.error("Failed to pause pipeline", error instanceof Error ? error : undefined);
      vscode.window.showErrorMessage(`Failed to pause pipeline: ${message}`);
    }
  });
}
