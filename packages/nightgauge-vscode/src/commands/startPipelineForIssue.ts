/**
 * Start Pipeline for Issue command
 *
 * Intermediate command triggered by clicking an issue in the tree view.
 * Supports queuing issues when a pipeline is already running.
 *
 * @see Issue #210 - Change default issue click action to start pipeline
 * @see Issue #236 - Queue Issues When Pipeline Active
 */

import * as vscode from "vscode";
import { ReadyIssueTreeItem } from "../views/items/ReadyIssueTreeItem";
import type { Logger } from "../utils/logger";
import type { HeadlessOrchestrator } from "../services/HeadlessOrchestrator";
import type { IssueQueueService } from "../services/IssueQueueService";
import type { ConcurrentPipelineManager } from "../services/ConcurrentPipelineManager";
import { registerMoveQueueItemUpCommand } from "./moveQueueItemUp";
import { registerMoveQueueItemDownCommand } from "./moveQueueItemDown";
import { registerRemoveQueueItemCommand } from "./removeQueueItem";
import { registerRetryQueueItemCommand } from "./retryQueueItem";

/**
 * Register the Start Pipeline for Issue command
 *
 * This command is triggered by clicking on an issue in the Pipeline Issues
 * tree view. When a pipeline is running, it queues the issue instead of
 * showing a blocking dialog.
 *
 * The existing pickupIssue command (inline play button) remains unchanged —
 * it works without confirmation for power users.
 */
export function registerStartPipelineForIssueCommand(
  logger: Logger,
  _headlessOrchestrator: HeadlessOrchestrator | null,
  queueService: IssueQueueService | null,
  concurrentPipelineManager?: ConcurrentPipelineManager | null
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.startPipelineForIssue",
    async (item?: ReadyIssueTreeItem) => {
      // Guard: validate tree item type
      if (!(item instanceof ReadyIssueTreeItem)) {
        logger.warn("startPipelineForIssue called without a ReadyIssueTreeItem");
        return;
      }

      const issueNumber = item.issueNumber;
      const issue = item.getIssue();
      const title = issue.title;
      const labels = issue.labels ?? [];

      // Resolve cross-repo identity from multiple sources:
      // 1. item.repoPath — filesystem path (set for board items)
      // 2. issue URL — parse owner/repo from GitHub URL
      // 3. item.repoOwner + item.repoName — from drag serialization
      // Used by both epic and single-issue paths.
      let repoOverride: { owner: string; repo: string } | undefined;
      if (item.repoPath) {
        const { getRepoIdentity } = await import("../utils/configPathResolver");
        const id = await getRepoIdentity(item.repoPath);
        if (id) repoOverride = { owner: id.owner, repo: id.repo };
      }
      if (!repoOverride) {
        const urlMatch = issue.url?.match(/github\.com\/([^/]+)\/([^/]+)\/issues\//);
        if (urlMatch) repoOverride = { owner: urlMatch[1], repo: urlMatch[2] };
      }
      if (!repoOverride && item.repoOwner && item.repoName) {
        repoOverride = { owner: item.repoOwner, repo: item.repoName };
      }

      // Shutdown pre-check — distinguish from missing-identity. Without this,
      // a stuck `isShuttingDown=true` makes every drag silently no-op with a
      // misleading "repo identity may be missing" warning. See Issue #3111.
      if (concurrentPipelineManager?.isShutdownInProgress) {
        logger.warn("Refusing enqueue — pipeline manager is shutting down", {
          issueNumber,
          isEpic: labels.includes("type:epic"),
        });
        vscode.window.showWarningMessage(
          "Pipeline manager is mid-shutdown — wait a few seconds and try again."
        );
        return;
      }

      // Epic routing — always queue via enqueueEpic (expands sub-issues)
      // instead of sending to pickupIssue which hits the epic pre-check and fails
      if (labels.includes("type:epic") && queueService) {
        logger.info("Routing epic to queue", {
          issueNumber,
          repoOverride: repoOverride
            ? `${repoOverride.owner}/${repoOverride.repo}`
            : "none (will use workspace default)",
          url: issue.url,
          repoPath: item.repoPath ?? "none",
        });

        try {
          const queuedItem = await queueService.enqueue(
            issueNumber,
            title,
            labels,
            issue.blockedBy,
            repoOverride ? { repoOverride } : undefined
          );
          if (queuedItem) {
            logger.info("Epic queued successfully", { issueNumber });
            vscode.window.showInformationMessage(
              `Epic #${issueNumber} queued — sub-issues will be processed.`
            );
          } else {
            logger.warn("Epic enqueue returned null — repo identity may be missing", {
              issueNumber,
              repoOverride,
            });
            vscode.window.showErrorMessage(
              `Failed to queue epic #${issueNumber} — could not resolve repository identity.`
            );
          }
        } catch (err) {
          const msg = err instanceof Error ? err.message : String(err);
          logger.error("Epic enqueue failed", { issueNumber, error: msg });
          vscode.window.showErrorMessage(`Failed to queue epic #${issueNumber}: ${msg}`);
        }
        return;
      }

      // Single issue — enqueue with repo identity (same path as epics).
      // ConcurrentPipelineManager picks it up and creates a worktree in
      // the correct repo. No separate "running vs idle" branching needed.
      if (queueService) {
        try {
          const isAlreadyQueued = await queueService.isQueued(issueNumber);
          if (isAlreadyQueued) {
            vscode.window.showInformationMessage(`Issue #${issueNumber} is already in the queue.`);
            return;
          }

          logger.info("Picking up issue", {
            issueNumber,
            repoOverride: repoOverride
              ? `${repoOverride.owner}/${repoOverride.repo}`
              : "none (workspace default)",
          });

          const queuedItem = await queueService.enqueue(
            issueNumber,
            title,
            labels,
            issue.blockedBy,
            repoOverride ? { repoOverride } : undefined
          );
          if (queuedItem) {
            logger.info("Issue enqueued for immediate pipeline start", {
              issueNumber,
            });
          }
        } catch (error) {
          const errorMessage = error instanceof Error ? error.message : "Unknown error";
          vscode.window.showErrorMessage(`Failed to queue issue #${issueNumber}: ${errorMessage}`);
          logger.error("Failed to queue issue", { issueNumber, error });
        }
      } else {
        // Fallback: no queue service — delegate to pickupIssue command
        logger.info("Starting pipeline for issue via click (no queue)", {
          issueNumber,
        });
        await vscode.commands.executeCommand("nightgauge.pickupIssue", item);
      }
    }
  );
}

/**
 * Register queue management commands
 *
 * Provides commands for managing the issue queue:
 * - nightgauge.removeFromQueue - Remove an issue from the queue
 * - nightgauge.clearQueue - Clear all queued issues
 * - nightgauge.resumeQueue - Resume a paused queue
 * - nightgauge.viewQueuedIssue - View a queued issue on GitHub
 */
export function registerQueueCommands(
  logger: Logger,
  queueService: IssueQueueService | null,
  _headlessOrchestrator?: HeadlessOrchestrator | null,
  concurrentPipelineManager?: ConcurrentPipelineManager | null
): vscode.Disposable[] {
  const disposables: vscode.Disposable[] = [];

  // Remove from queue
  disposables.push(
    vscode.commands.registerCommand(
      "nightgauge.removeFromQueue",
      async (item?: { issueNumber: number }) => {
        if (!queueService || !item?.issueNumber) {
          return;
        }

        const removed = await queueService.remove(item.issueNumber);
        if (removed) {
          vscode.window.showInformationMessage(`Issue #${item.issueNumber} removed from queue.`);
          logger.info("Issue removed from queue", {
            issueNumber: item.issueNumber,
          });
        }
      }
    )
  );

  // Clear queue
  disposables.push(
    vscode.commands.registerCommand("nightgauge.clearQueue", async () => {
      if (!queueService) {
        return;
      }

      const queueLength = await queueService.getQueueLength();
      if (queueLength === 0) {
        vscode.window.showInformationMessage("Queue is already empty.");
        return;
      }

      const selection = await vscode.window.showWarningMessage(
        `Clear all ${queueLength} queued issue(s)?`,
        { modal: true },
        "Clear Queue"
      );

      if (selection === "Clear Queue") {
        await queueService.clear();
        vscode.window.showInformationMessage("Queue cleared.");
        logger.info("Queue cleared", { previousLength: queueLength });
      }
    })
  );

  // Resume queue
  disposables.push(
    vscode.commands.registerCommand("nightgauge.resumeQueue", async () => {
      if (!queueService) {
        return;
      }

      const status = await queueService.getStatus();

      // Resume paused queue first (Issue #490)
      if (status === "paused") {
        await queueService.resume();
        logger.info("Queue resumed");
      }

      // If nothing is running, immediately start the next queued item (Issue #490)
      // This handles both: resuming a paused queue AND starting a waiting queue
      // in a single click instead of requiring two.
      const currentStatus = await queueService.getStatus();
      const hasActiveSlots = (concurrentPipelineManager?.activeSlotCount ?? 0) > 0;

      if (currentStatus === "waiting" && !hasActiveSlots) {
        // Route through ConcurrentPipelineManager.fillSlots() so pipelines
        // execute via the Go IPC path (worktree + scheduler). The legacy
        // HeadlessOrchestrator.startNextQueuedIssue() path is not wired to
        // the Go scheduler and silently hangs.
        if (concurrentPipelineManager && concurrentPipelineManager.availableSlotCount > 0) {
          const queueLength = await queueService.getQueueLength();
          logger.info("Resuming queue via ConcurrentPipelineManager.fillSlots", {
            queueLength,
            availableSlots: concurrentPipelineManager.availableSlotCount,
          });
          vscode.window.showInformationMessage(
            `Resuming pipeline queue (${queueLength} issue(s) waiting)...`
          );
          // Fire-and-forget so the command returns immediately
          concurrentPipelineManager.fillSlots().catch((error) => {
            logger.error("Failed to fill slots from manual resume", {
              error: error instanceof Error ? error.message : "Unknown error",
            });
            vscode.window.showErrorMessage(
              `Failed to resume queue: ${error instanceof Error ? error.message : "Unknown error"}`
            );
          });
          return;
        }

        // Fallback: no ConcurrentPipelineManager available — should not happen
        // in normal operation but guard defensively.
        logger.warn("resumeQueue: no ConcurrentPipelineManager available, cannot start");
        vscode.window.showErrorMessage(
          "Cannot resume queue — pipeline manager not available. Try reloading the window."
        );
        return;
      }

      // If a pipeline is running, just inform the user the queue is active
      if (status === "paused") {
        // We resumed above but can't start yet — pipeline is running
        vscode.window.showInformationMessage(
          "Queue resumed. Next issue will start when current pipeline completes."
        );
      } else if (currentStatus === "waiting" && hasActiveSlots) {
        vscode.window.showInformationMessage(
          "Queue is active. Next issue will start when current pipeline completes."
        );
      } else {
        vscode.window.showInformationMessage("Queue has no items to process.");
      }
    })
  );

  // View queued issue on GitHub
  disposables.push(
    vscode.commands.registerCommand(
      "nightgauge.viewQueuedIssue",
      async (item?: { issueNumber: number }) => {
        if (!item?.issueNumber) {
          return;
        }

        // Get repo info from workspace to construct URL
        const workspaceFolder = vscode.workspace.workspaceFolders?.[0];
        if (!workspaceFolder) {
          return;
        }

        try {
          const { exec } = await import("child_process");
          const { promisify } = await import("util");
          const execAsync = promisify(exec);

          const { stdout } = await execAsync("gh repo view --json url", {
            cwd: workspaceFolder.uri.fsPath,
          });
          const { url } = JSON.parse(stdout);
          const issueUrl = `${url}/issues/${item.issueNumber}`;
          await vscode.env.openExternal(vscode.Uri.parse(issueUrl));
        } catch (error) {
          logger.warn("Failed to open issue URL", { error });
        }
      }
    )
  );

  // Register queue management commands
  disposables.push(registerMoveQueueItemUpCommand(queueService, logger));
  disposables.push(registerMoveQueueItemDownCommand(queueService, logger));
  disposables.push(registerRemoveQueueItemCommand(queueService, logger));
  disposables.push(registerRetryQueueItemCommand(queueService, logger));

  return disposables;
}
