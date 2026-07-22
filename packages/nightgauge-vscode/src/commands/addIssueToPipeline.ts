/**
 * Add Issue to Pipeline Command
 *
 * Adds a ready issue from the project board to the pipeline queue.
 * Provides keyboard alternative to drag & drop for accessibility.
 *
 * @see Issue #304 - Add Accessibility Alternatives for Drag & Drop
 */

import * as vscode from "vscode";
import { ReadyIssueTreeItem } from "../views/items/ReadyIssueTreeItem";
import type { IssueQueueService } from "../services/IssueQueueService";
import type { Logger } from "../utils/logger";
import { getRepoIdentity } from "../utils/configPathResolver";

/**
 * Register the Add Issue to Pipeline command
 *
 * Adds a ready issue to the pipeline queue, providing keyboard-accessible
 * alternative to drag & drop interaction.
 */
export function registerAddIssueToPipelineCommand(
  queueService: IssueQueueService,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.addIssueToPipeline",
    async (item?: ReadyIssueTreeItem) => {
      try {
        // Validation
        if (!item || !(item instanceof ReadyIssueTreeItem)) {
          logger.error("addIssueToPipeline: Invalid item parameter");
          vscode.window.showErrorMessage("Please select a valid issue");
          return;
        }

        const issueNumber = item.issueNumber;
        const issue = item.getIssue();

        logger.info("Adding issue to pipeline queue", { issueNumber });

        // Resolve repo context for cross-repo items (Issue #2188)
        let repoOverride: { owner: string; repo: string } | undefined;
        if (item.repoPath) {
          const crossRepoIdentity = await getRepoIdentity(item.repoPath);
          if (crossRepoIdentity) {
            repoOverride = {
              owner: crossRepoIdentity.owner,
              repo: crossRepoIdentity.repo,
            };
          }
        }

        // Add to pipeline queue (pass blockedBy for blocked-issue warning, Issue #820)
        const result = await queueService.enqueue(
          issueNumber,
          issue.title,
          issue.labels,
          issue.blockedBy,
          repoOverride ? { repoOverride } : undefined
        );

        // User cancelled from blocked warning dialog
        if (result === null) {
          return;
        }

        // Get updated queue to determine position
        const queue = await queueService.getQueue();
        const queueItem = queue?.items.find((i) => i.issueNumber === issueNumber);
        const position = queueItem?.position ?? 1;

        // Show success message (screen reader will announce)
        vscode.window.showInformationMessage(
          `Issue #${issueNumber} added to pipeline at position ${position}`
        );

        logger.info("Successfully added issue to pipeline", {
          issueNumber,
          position,
        });

        // Focus pipeline view to show the newly added issue
        await vscode.commands.executeCommand("nightgauge.pipelineView.focus");
      } catch (error) {
        logger.error("Failed to add issue to pipeline", { error });
        vscode.window.showErrorMessage("Failed to add issue to pipeline");
      }
    }
  );
}
