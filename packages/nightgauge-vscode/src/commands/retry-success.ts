import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { Logger } from "../utils/logger";
import type { StatusBarManager } from "../utils/statusBar";
import { getNextStage, getStageLabel } from "../utils/skillRunner";

interface CompleteRetryOptions {
  stage: PipelineStage;
  issueNumber: number;
  durationMs?: number;
  stateService: PipelineStateService | null;
  logger: Logger;
  statusBar: StatusBarManager;
  source: "retry" | "retry-from-phase";
}

/** Apply the user-visible success and continuation contract for every retry path. */
export async function completeSuccessfulRetry({
  stage,
  issueNumber,
  durationMs,
  stateService,
  logger,
  statusBar,
  source,
}: CompleteRetryOptions): Promise<void> {
  logger.info(
    source === "retry"
      ? "Stage retry completed successfully"
      : "Retry from phase completed successfully",
    {
      stage,
      issueNumber,
      durationMs,
    }
  );
  statusBar.showComplete(stage);

  const nextStage = getNextStage(stage);
  if (!stateService || !nextStage || nextStage === "pipeline-finish") {
    vscode.window.showInformationMessage(`Stage "${stage}" completed successfully`);
    return;
  }

  const autoContinue = vscode.workspace
    .getConfiguration("nightgauge.pipeline")
    .get("autoContinue", true);
  if (!autoContinue) {
    vscode.window.showInformationMessage(`Stage "${stage}" completed successfully`);
    return;
  }

  const isPaused = await stateService.isPaused();
  if (isPaused) {
    vscode.window.showInformationMessage(
      `Stage "${stage}" completed successfully. Pipeline is paused.`
    );
    return;
  }

  const executionMode = await stateService.getExecutionMode();
  const delay = vscode.workspace
    .getConfiguration("nightgauge.pipeline")
    .get("autoContinueDelay", 1000);

  if (executionMode === "automatic") {
    logger.info(`Auto-continuing after ${source} (automatic mode)`, {
      stage,
      nextStage,
      issueNumber,
    });
    setTimeout(() => {
      void vscode.commands.executeCommand("nightgauge.runStage", nextStage);
    }, delay);
    return;
  }

  logger.info(`Auto-continuing after ${source} (manual mode)`, {
    stage,
    nextStage,
    issueNumber,
  });
  setTimeout(() => {
    void vscode.window
      .showInformationMessage(
        `${getStageLabel(stage)} complete. Continue to ${getStageLabel(nextStage)}?`,
        "Run Now",
        "Yes to All",
        "Pause"
      )
      .then(async (selection) => {
        if (selection === "Run Now") {
          await stateService.resumePipeline();
          await vscode.commands.executeCommand("nightgauge.runStage", nextStage);
        } else if (selection === "Yes to All") {
          await stateService.setExecutionMode("automatic");
          await stateService.resumePipeline();
          await vscode.commands.executeCommand("nightgauge.runStage", nextStage);
        } else {
          await stateService.pausePipeline();
          await vscode.window.showInformationMessage(
            `Pipeline paused. Run "${getStageLabel(nextStage)}" to continue.`
          );
        }
      });
  }, delay);
}
