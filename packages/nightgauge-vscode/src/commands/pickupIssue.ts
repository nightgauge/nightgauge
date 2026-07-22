/**
 * Pickup Issue command
 *
 * Picks up an issue by enqueuing it into the IssueQueueService.
 * ConcurrentPipelineManager.fillSlots() then claims the issue, creates a
 * worktree, and runs the full pipeline — regardless of maxConcurrent (1 or N).
 *
 * @see docs/ARCHITECTURE.md - Context-Isolated Pipeline Architecture
 * @see Issue #1831 - Unify pipeline worktree path
 */

import * as vscode from "vscode";
import { ReadyIssueTreeItem } from "../views/items/ReadyIssueTreeItem";
import { IssueTreeItem } from "../views/items/IssueTreeItem";
import type { Logger } from "../utils/logger";
import type { StatusBarManager } from "../utils/statusBar";
import type { PipelineTreeProvider } from "../views";
import type { OutputWindow } from "../views";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { ConcurrentPipelineManager } from "../services/ConcurrentPipelineManager";
import { IpcClient } from "../services/IpcClient";
import { getRepoIdentity } from "../utils/configPathResolver";

/**
 * Issue info fetched from GitHub
 */
interface IssueInfo {
  number: number;
  title: string;
  labels: string[];
}

/**
 * Fetch issue info from GitHub via Go binary IPC
 *
 * When no repoPath is provided, tries each workspace folder to find which
 * repository actually contains the issue. This prevents prompt-entered issue
 * numbers from defaulting to the first workspace folder (which may be wrong
 * in multi-repo workspaces).
 *
 * @param issueNumber - The issue number to fetch
 * @param repoPath - Optional repo path for cross-repo fetch
 * @returns Issue info (with repoIdentity) or null if fetch fails
 */
async function fetchIssueInfo(
  issueNumber: number,
  repoPath?: string
): Promise<(IssueInfo & { repoIdentity?: { owner: string; repo: string } }) | null> {
  const ipc = IpcClient.getInstance();

  // If an explicit repoPath is provided, use it directly.
  if (repoPath) {
    try {
      const identity = await getRepoIdentity(repoPath);
      if (!identity) return null;
      const issue = await ipc.issueView(identity.owner, identity.repo, issueNumber);
      return {
        number: issue.number,
        title: issue.title,
        labels: issue.labels || [],
        repoIdentity: identity,
      };
    } catch {
      return null;
    }
  }

  // No explicit path — try each workspace folder to find the issue.
  const folders = vscode.workspace.workspaceFolders ?? [];
  for (const folder of folders) {
    try {
      const identity = await getRepoIdentity(folder.uri.fsPath);
      if (!identity) continue;
      const issue = await ipc.issueView(identity.owner, identity.repo, issueNumber);
      return {
        number: issue.number,
        title: issue.title,
        labels: issue.labels || [],
        repoIdentity: identity,
      };
    } catch {
      // Issue not found in this repo — try the next one
      continue;
    }
  }
  return null;
}

/**
 * Register the Pickup Issue command
 *
 * This command can be invoked from:
 * 1. Context menu on a ready issue item
 * 2. Command palette (will prompt for issue number)
 *
 * Routes through IssueQueueService.enqueue() → onItemAdded → fillSlots()
 * for unified worktree-based execution. ConcurrentPipelineManager handles
 * worktree creation, pipeline execution, status bar, and output routing.
 *
 * @see Issue #1831 - Unify pipeline worktree path
 * @see Issue #531 - Unify single-issue and batch pipeline execution paths
 */
export function registerPickupIssueCommand(
  logger: Logger,
  _statusBar: StatusBarManager,
  _treeProvider: PipelineTreeProvider,
  _outputWindow: OutputWindow,
  _pipelineStateService: PipelineStateService,
  queueService?: {
    enqueue(
      issueNumber: number,
      title: string,
      labels?: string[],
      blockedBy?: unknown,
      options?: { repoOverride?: { owner: string; repo: string } }
    ): Promise<{ position: number } | null>;
    isQueued(issueNumber: number): Promise<boolean>;
    getQueueLength(): Promise<number>;
  },
  concurrentPipelineManager?: ConcurrentPipelineManager
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.pickupIssue",
    async (item?: ReadyIssueTreeItem) => {
      let issueNumber: number | undefined;

      if (item instanceof ReadyIssueTreeItem) {
        issueNumber = item.issueNumber;
      } else {
        // Prompt for issue number if not provided
        const input = await vscode.window.showInputBox({
          prompt: "Enter issue number to pick up",
          placeHolder: "123",
          validateInput: (value) => {
            const num = parseInt(value, 10);
            if (isNaN(num) || num <= 0) {
              return "Please enter a valid issue number";
            }
            return null;
          },
        });

        if (!input) {
          return; // User cancelled
        }

        issueNumber = parseInt(input, 10);
      }

      logger.info("Picking up issue", { issueNumber });

      // Show progress while fetching issue info and enqueuing
      await vscode.window.withProgress(
        {
          location: vscode.ProgressLocation.Notification,
          title: `Picking up issue #${issueNumber}...`,
          cancellable: false,
        },
        async () => {
          try {
            // Check if already running in a concurrent slot
            if (concurrentPipelineManager?.isRunning(issueNumber!)) {
              vscode.window.showInformationMessage(
                `Issue #${issueNumber} is already running in a pipeline.`
              );
              logger.info("Attempted to pick up issue already running", {
                issueNumber,
              });
              return;
            }

            // Check if already queued
            if (queueService && (await queueService.isQueued(issueNumber!))) {
              vscode.window.showInformationMessage(
                `Issue #${issueNumber} is already in the queue.`
              );
              return;
            }

            if (!queueService) {
              vscode.window.showErrorMessage(
                "Queue service not available — cannot start pipeline."
              );
              return;
            }

            // Fetch issue info for title, labels, and resolved repo identity.
            // fetchIssueInfo tries each workspace folder when no repoPath is
            // provided, so prompt-entered issues resolve to the correct repo.
            const issueInfo = await fetchIssueInfo(
              issueNumber!,
              item instanceof ReadyIssueTreeItem ? item.repoPath : undefined
            );
            const title = issueInfo?.title || `Issue #${issueNumber}`;
            const labels = issueInfo?.labels || [];

            // Resolve repo context for cross-repo items (Issue #2188)
            // Use the repo identity discovered by fetchIssueInfo, or fall back
            // to the tree item's repoPath for ReadyIssueTreeItem clicks.
            let repoOverride: { owner: string; repo: string } | undefined;
            if (issueInfo?.repoIdentity) {
              repoOverride = issueInfo.repoIdentity;
            } else if (item instanceof ReadyIssueTreeItem && item.repoPath) {
              const crossRepoIdentity = await getRepoIdentity(item.repoPath);
              if (crossRepoIdentity) {
                repoOverride = {
                  owner: crossRepoIdentity.owner,
                  repo: crossRepoIdentity.repo,
                };
              }
            }

            // Enqueue the issue — onItemAdded callback triggers fillSlots()
            const queuedItem = await queueService.enqueue(
              issueNumber!,
              title,
              labels,
              undefined,
              repoOverride ? { repoOverride } : undefined
            );
            if (!queuedItem) {
              vscode.window.showErrorMessage(`Failed to queue issue #${issueNumber}.`);
              return;
            }

            // Show appropriate message based on slot availability
            const activeSlots = concurrentPipelineManager?.activeSlotCount ?? 0;
            if (activeSlots > 0) {
              const queueLength = await queueService.getQueueLength();
              vscode.window.showInformationMessage(
                `Issue #${issueNumber} added to queue (position ${queuedItem.position}). ` +
                  `${queueLength} issue(s) queued.`
              );
              logger.info("Issue queued while pipeline running", {
                issueNumber,
                position: queuedItem.position,
                activeSlots,
              });
            } else {
              vscode.window.showInformationMessage(
                `Pipeline starting for issue #${issueNumber} — ${title}`
              );
              logger.info("Issue enqueued for immediate pipeline start", {
                issueNumber,
              });
            }
          } catch (error) {
            logger.error("Failed to pickup issue", { issueNumber, error });
            vscode.window.showErrorMessage(
              `Failed to pickup issue #${issueNumber}: ${error instanceof Error ? error.message : "Unknown error"}`
            );
          }
        }
      );
    }
  );
}

/**
 * Register the View Issue on GitHub command
 *
 * Opens the issue URL in the default browser.
 */
export function registerViewIssueOnGitHubCommand(logger: Logger): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.viewIssueOnGitHub",
    async (item?: ReadyIssueTreeItem | IssueTreeItem) => {
      // Type guard: accept ReadyIssueTreeItem or IssueTreeItem
      if (!(item instanceof ReadyIssueTreeItem) && !(item instanceof IssueTreeItem)) {
        logger.warn("viewIssueOnGitHub called without a valid tree item");
        return;
      }

      // IssueTreeItem may not have URL (optional)
      if (!item.issueUrl) {
        logger.warn("viewIssueOnGitHub called on tree item without URL", {
          issueNumber: item.issueNumber,
        });
        return;
      }

      logger.debug("Opening issue on GitHub", {
        issueNumber: item.issueNumber,
      });
      await vscode.env.openExternal(vscode.Uri.parse(item.issueUrl));
    }
  );
}
