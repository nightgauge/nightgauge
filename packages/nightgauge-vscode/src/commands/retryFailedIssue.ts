/**
 * retryFailedIssue - Retry a failed pipeline issue from the failed stage
 *
 * Resumes pipeline execution from the stage where failure occurred, reusing
 * existing context files for efficiency.
 *
 * @see Issue #301 - Handle completed and failed issue states in pipeline
 */

import * as vscode from "vscode";
import * as fs from "node:fs/promises";
import * as path from "path";
import { uuidV7, type PipelineStage } from "@nightgauge/sdk";
import { CompletedIssuesService } from "../services/CompletedIssuesService";
import type { HeadlessOrchestrator, StageRunResult } from "../services/HeadlessOrchestrator";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { FailedIssueTreeItem } from "../views/items/FailedIssueTreeItem";
import { getRepoIdentity } from "../utils/configPathResolver";

/**
 * Maximum retry attempts (circuit breaker)
 * Follows StageTreeItem pattern: MAX_RETRIES = 3
 */
const MAX_RETRIES = 3;

/**
 * The "owner/repo" a retry dispatch belongs to (ADR-017 Decision 10).
 *
 * Read from the same `.nightgauge/config.yaml` identity every other repo
 * resolver in the extension uses, with its own git-remote fallback. Returns
 * "" when the workspace has no resolvable identity — the run is then
 * unattributed, which is the state it was in before this step, not a new one.
 */
async function resolveRetryRepoSlug(): Promise<string> {
  const root = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) return "";
  const identity = await getRepoIdentity(root);
  return identity ? `${identity.owner}/${identity.repo}` : "";
}

/**
 * Register the retryFailedIssue command
 *
 * @param context - VSCode extension context
 * @param orchestrator - Pipeline orchestrator for running stages
 * @param stateService - Pipeline state service
 */
export function registerRetryFailedIssueCommand(
  context: vscode.ExtensionContext,
  orchestrator: HeadlessOrchestrator,
  stateService: PipelineStateService
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.retryFailedIssue",
    async (arg?: number | FailedIssueTreeItem) => {
      try {
        // Extract issue number from tree item (inline button) or direct argument
        let issueNumber: number | undefined;
        if (typeof arg === "number") {
          issueNumber = arg;
        } else if (arg && typeof arg === "object" && "issue" in arg) {
          issueNumber = (arg as FailedIssueTreeItem).issue.issue_number;
        }

        // Validate issue number
        if (issueNumber === undefined || !Number.isInteger(issueNumber) || issueNumber <= 0) {
          vscode.window.showErrorMessage("Invalid issue number for retry.");
          return;
        }

        const service = CompletedIssuesService.getInstance(context.workspaceState);

        // Get failed issue details
        const failedIssue = service.getFailedIssue(issueNumber);
        if (!failedIssue) {
          vscode.window.showErrorMessage(`Issue #${issueNumber} not found in failed issues.`);
          return;
        }

        // Check retry circuit breaker
        if (failedIssue.retry_count >= MAX_RETRIES) {
          vscode.window.showErrorMessage(
            `Issue #${issueNumber} has reached maximum retry attempts (${MAX_RETRIES}). Manual intervention required.`
          );
          return;
        }

        // Verify context files exist
        const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
        if (!workspaceRoot) {
          vscode.window.showErrorMessage("No workspace folder found.");
          return;
        }

        const contextFile = path.join(
          workspaceRoot,
          ".nightgauge",
          "pipeline",
          `${failedIssue.failed_stage.replace("feature-", "")}-${issueNumber}.json`
        );

        try {
          await fs.access(contextFile);
        } catch {
          vscode.window.showWarningMessage(
            `Context file not found for issue #${issueNumber}. Pipeline will start from the failed stage but may need to recreate context.`
          );
        }

        // Auto-clear completed pipeline from different issue (Issue #870)
        if (stateService) {
          const existingState = await stateService.getState();
          if (existingState && existingState.issue_number !== issueNumber) {
            if (stateService.isPipelineComplete(existingState)) {
              // Move completed issue to completed list before clearing
              service.addCompleted(
                existingState.issue_number,
                existingState.title,
                existingState.branch,
                existingState.labels
              );
              await stateService.clearPipeline();
              console.log(
                `[Nightgauge] Auto-cleared completed pipeline for #${existingState.issue_number} before retrying #${issueNumber}`
              );
            } else {
              // Different issue, not complete — block
              vscode.window.showErrorMessage(
                `Pipeline is locked to in-progress issue #${existingState.issue_number}. ` +
                  `Complete or clear that pipeline before retrying #${issueNumber}.`
              );
              return;
            }
          }
        }

        // Re-initialize pipeline for the retry issue.
        //
        // A retry IS a dispatch, so it mints its own identity (ADR-017
        // Decision 10). The auto-clear above only fires for a DIFFERENT
        // issue; retrying the SAME issue while its first run is still
        // executing fell straight through to initializePipeline, the last
        // minter won, and the first run's remaining transitions were booked
        // under the second run's identity while it went silent forever
        // (F23). `beginRun` refuses that now, and the refusal is surfaced to
        // the operator verbatim rather than swallowed — there is deliberately
        // no pre-clear here, because clearing a LIVE run to make room for its
        // own retry is the corruption, not the fix.
        if (stateService) {
          stateService.beginRun(uuidV7(), await resolveRetryRepoSlug(), issueNumber);
          await stateService.initializePipeline(issueNumber, failedIssue.title, failedIssue.branch);
        }

        // Remove from failed list
        service.removeFromFailed(issueNumber);

        // Show info message
        vscode.window.showInformationMessage(
          `Retrying issue #${issueNumber} from ${failedIssue.failed_stage}...`
        );

        // Run pipeline from failed stage
        const stage = failedIssue.failed_stage as PipelineStage;

        // Run the stage and let the orchestrator handle the rest
        const result = await orchestrator.runStage(stage, issueNumber, {
          onStageComplete: (completedStage: PipelineStage, stageResult: StageRunResult) => {
            if (stageResult.success) {
              vscode.window.showInformationMessage(
                `Stage ${completedStage} completed successfully.`
              );
            }
          },
          onStageError: (errorStage: PipelineStage, error: Error) => {
            vscode.window.showErrorMessage(`Stage ${errorStage} failed: ${error.message ?? error}`);
          },
        });

        if (!result.success) {
          // Re-add to failed list with incremented retry count
          service.addFailed(
            issueNumber,
            failedIssue.title,
            failedIssue.branch,
            stage,
            result.error instanceof Error
              ? result.error.message
              : String(result.error ?? "Unknown error"),
            failedIssue.labels
          );
        }
      } catch (error) {
        const message = error instanceof Error ? error.message : "Unknown error";
        vscode.window.showErrorMessage(`Failed to retry issue: ${message}`);
        console.error("[Nightgauge] Error retrying failed issue:", error);
      }
    }
  );
}
