/**
 * Reset Pipeline command
 *
 * Allows users to manually reset the pipeline state when stuck or after completion.
 * Clears all context files, plan files, resets the TreeView, and cleans up git state.
 *
 * @see docs/ARCHITECTURE.md - Context-Isolated Pipeline Architecture
 * @see Issue #115 - Reset pipeline should verify PR merged and cleanup local branch
 */

import * as vscode from "vscode";
import type { Logger } from "../utils/logger";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { HeadlessOrchestrator } from "../services/HeadlessOrchestrator";
import type { PipelineTreeProvider } from "../views";
import type { StatusBarManager } from "../utils/statusBar";
import type { CompletedIssuesService } from "../services/CompletedIssuesService";
import { getWorkspaceRoot } from "../config/settings";
import { resetGitHubStatus } from "../utils/githubStatusSync";
import { hasActiveProcess, killAllActiveProcesses } from "../utils/skillRunner";
import { IpcClient } from "../services/IpcClient";
import { getRepoIdentity } from "../utils/configPathResolver";
import { KnowledgeService } from "@nightgauge/sdk";

/**
 * Register the Reset Pipeline command
 *
 * This command can be invoked from:
 * 1. Command palette (Nightgauge: Reset Pipeline)
 * 2. Pipeline view title menu
 */
/**
 * Options for reset pipeline command
 */
interface ResetPipelineOptions {
  /** Skip the confirmation dialog (for programmatic calls after user already confirmed) */
  skipConfirm?: boolean;
  /** Skip git cleanup (for cases where we just want to clear state) */
  skipGitCleanup?: boolean;
}

/**
 * Check if there's an open PR for the given branch
 */
async function getOpenPRForBranch(
  branch: string,
  cwd: string
): Promise<{ number: number; url: string } | null> {
  try {
    const identity = await getRepoIdentity(cwd);
    if (!identity) return null;
    const ipc = IpcClient.getInstance();
    const prs = await ipc.prList(identity.owner, identity.repo, {
      state: "open",
      headRef: branch,
    });
    if (prs.length > 0) {
      return { number: prs[0].number, url: prs[0].url };
    }
    return null;
  } catch {
    return null;
  }
}

/**
 * Get the current git branch
 */
async function getCurrentBranch(cwd: string): Promise<string | null> {
  try {
    const ipc = IpcClient.getInstance();
    return await ipc.gitCurrentBranch(cwd);
  } catch {
    return null;
  }
}

/**
 * Check if the working directory has uncommitted changes
 */
async function hasUncommittedChanges(cwd: string): Promise<boolean> {
  try {
    const ipc = IpcClient.getInstance();
    const status = await ipc.gitStatus(cwd);
    return !status.isClean;
  } catch {
    return false;
  }
}

/**
 * Clean up git state: checkout main, fetch, delete feature branch
 */
async function cleanupGitState(
  featureBranch: string,
  baseBranch: string,
  cwd: string,
  logger: Logger
): Promise<{ success: boolean; error?: string }> {
  try {
    const ipc = IpcClient.getInstance();

    // Check for uncommitted changes
    if (await hasUncommittedChanges(cwd)) {
      return {
        success: false,
        error: "Uncommitted changes detected. Please commit or stash your changes first.",
      };
    }

    // Checkout base branch
    logger.info("Checking out base branch", { baseBranch });
    await ipc.gitCheckout(baseBranch, cwd);

    // Fetch latest
    logger.info("Fetching latest from origin", { baseBranch });
    try {
      await ipc.gitFetch(false, cwd);
    } catch {
      // Fetch may fail if no network - continue anyway
      logger.warn("Failed to fetch latest, continuing with local state");
    }

    // Delete local feature branch
    logger.info("Deleting local feature branch", { featureBranch });
    try {
      await ipc.gitBranchDelete(featureBranch, cwd);
    } catch (error) {
      // Branch may already be deleted or not exist
      logger.warn("Could not delete feature branch (may already be deleted)", {
        featureBranch,
        error,
      });
    }

    return { success: true };
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    return { success: false, error: message };
  }
}

export function registerResetPipelineCommand(
  logger: Logger,
  pipelineStateService: PipelineStateService | null,
  orchestrator: HeadlessOrchestrator | null,
  treeProvider: PipelineTreeProvider,
  statusBar: StatusBarManager,
  completedIssuesService?: CompletedIssuesService | null
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.resetPipeline",
    async (options?: ResetPipelineOptions) => {
      const workspaceRoot = getWorkspaceRoot();
      if (!workspaceRoot) {
        vscode.window.showErrorMessage("No workspace folder open");
        return;
      }

      // Get pipeline state before we clear it (needed for git cleanup)
      const pipelineState = await pipelineStateService?.getState();
      const issueNumber = pipelineState?.issue_number;
      const featureBranch = pipelineState?.branch;
      const baseBranch = pipelineState?.base_branch || "main";

      // Check current git branch
      const currentBranch = await getCurrentBranch(workspaceRoot);
      const isOnFeatureBranch = currentBranch && featureBranch && currentBranch === featureBranch;

      // Start PR check early (needed for confirmation dialog text)
      // Skip the network call entirely when skipConfirm is set — no dialog means
      // no need to check for open PRs, and this is the main source of reset latency.
      const openPRPromise =
        !options?.skipConfirm && isOnFeatureBranch && featureBranch
          ? getOpenPRForBranch(featureBranch, workspaceRoot)
          : Promise.resolve(null);

      // Await for confirmation dialog text (null when skipConfirm is set)
      const openPR = await openPRPromise;

      // Confirm with user unless skipConfirm is set
      if (!options?.skipConfirm) {
        const completedCount = completedIssuesService?.getCompleted().length ?? 0;
        const failedCount = completedIssuesService?.getFailed().length ?? 0;
        const hasHistory = completedCount > 0 || failedCount > 0;

        let message = "Reset pipeline? This will clear all context files and pipeline state.";

        if (hasHistory) {
          message += ` Also clears: issue history (${completedCount} completed, ${failedCount} failed).`;
        }

        if (openPR) {
          message = `PR #${openPR.number} is still open! Reset will NOT merge it. Continue anyway?`;
        } else if (isOnFeatureBranch) {
          message = `Reset pipeline and switch to ${baseBranch}? This will delete branch "${featureBranch}".`;
        }

        const confirmButton = openPR ? "Reset Anyway" : "Reset";
        const confirm = await vscode.window.showWarningMessage(
          message,
          { modal: true },
          confirmButton
        );

        if (confirm !== confirmButton) {
          return;
        }
      }

      try {
        logger.info("Resetting pipeline", {
          issueNumber,
          featureBranch,
          baseBranch,
          openPR: openPR?.number,
        });

        // Always stop any running orchestrator/stage process before file cleanup.
        // This prevents stale background runs from recreating state right after reset.
        if (orchestrator?.getIsRunning()) {
          orchestrator.stop();
        } else if (hasActiveProcess()) {
          killAllActiveProcesses();
          logger.warn("Killed orphaned stage process during reset");
        }

        // Immediately clear UI (instant perceived reset)
        treeProvider.clearIssue();
        treeProvider.resetAllStages();
        statusBar.showIdle();

        // Clear completed/failed issue history (synchronous workspace storage)
        if (completedIssuesService) {
          completedIssuesService.clearCompleted();
          completedIssuesService.clearFailed();
        }

        // Parallelize independent network operations:
        // - resetGitHubStatus() and cleanupGitState() are independent
        // Both use graceful degradation (continue on failure)
        const [githubResult, gitResult] = await Promise.all([
          issueNumber
            ? resetGitHubStatus(issueNumber, workspaceRoot, logger)
            : Promise.resolve({ success: true } as {
                success: boolean;
                error?: string;
              }),
          !options?.skipGitCleanup && isOnFeatureBranch && featureBranch
            ? cleanupGitState(featureBranch, baseBranch, workspaceRoot, logger)
            : Promise.resolve({ success: true } as {
                success: boolean;
                error?: string;
              }),
        ]);

        const githubSyncSuccess = githubResult.success;
        if (!githubResult.success) {
          logger.warn("GitHub sync failed, continuing with local cleanup", {
            error: githubResult.error,
          });
        }

        if (!gitResult.success && !options?.skipGitCleanup && isOnFeatureBranch && featureBranch) {
          // Show error but allow user to continue with file cleanup
          const continueAnyway = await vscode.window.showWarningMessage(
            `Git cleanup failed: ${gitResult.error}\n\nContinue with file cleanup only?`,
            { modal: true },
            "Continue"
          );

          if (continueAnyway !== "Continue") {
            return;
          }
        }

        // Find context, plan, and corrupt backup files concurrently
        const contextDir = `${workspaceRoot}/.nightgauge/pipeline`;
        const plansDir = `${workspaceRoot}/.nightgauge/plans`;
        // Always scan ALL *.json files in the pipeline dir so that stale context
        // files from previously completed issues (#1209) are also removed.
        // Files are filtered below to target only pipeline context files.
        const [allContextFiles, planFiles, corruptFiles] = await Promise.all([
          vscode.workspace.findFiles(new vscode.RelativePattern(contextDir, "*.json")),
          issueNumber
            ? vscode.workspace.findFiles(
                new vscode.RelativePattern(plansDir, `${issueNumber}-*.md`)
              )
            : Promise.resolve([]),
          // Delete ALL corrupt backup files on reset (Issue #872)
          vscode.workspace.findFiles(new vscode.RelativePattern(contextDir, "*.corrupt-*")),
        ]);

        // Keep only pipeline context files: issue-N, planning-N, dev-N,
        // validate-N, pr-N, merge-N, dev-batch-N, planning-batch-N.
        // Preserves state.json, queue-state.json, health-history.jsonl, etc.
        const PIPELINE_CONTEXT_PATTERN =
          /^(?:issue|planning|dev|validate|pr|merge|dev-batch|planning-batch)-\d+\.json$/;
        const contextFiles = allContextFiles.filter((f) => {
          const name = f.fsPath.split("/").pop() ?? "";
          return PIPELINE_CONTEXT_PATTERN.test(name);
        });

        // Run clearPipeline and file deletions concurrently.
        // clearPipeline handles state.json; PIPELINE_CONTEXT_PATTERN already
        // excludes state.json so there is no conflict.
        await Promise.all([
          pipelineStateService?.clearPipeline(),
          // Delete all pipeline context files in parallel (current + stale issues)
          ...contextFiles.map((f) => vscode.workspace.fs.delete(f).then(undefined, () => {})),
          // Delete plan files in parallel
          ...planFiles.map((f) => vscode.workspace.fs.delete(f).then(undefined, () => {})),
          // Delete corrupt backup files in parallel (Issue #872)
          ...corruptFiles.map((f) => vscode.workspace.fs.delete(f).then(undefined, () => {})),
          // Prune non-substantive knowledge entries (empty boilerplate from
          // scaffolding that was never enriched by planning). Substantive
          // entries are preserved for future reference.
          (async () => {
            try {
              const knowledgeSvc = new KnowledgeService(workspaceRoot);
              const pruned = await knowledgeSvc.pruneEmpty();
              if (pruned.length > 0) {
                logger.info("Pruned empty knowledge entries on reset", {
                  pruned,
                });
              }
            } catch {
              // Knowledge cleanup is best-effort — don't block reset
            }
          })(),
        ]);

        logger.info("Pipeline manually reset", { issueNumber });

        // Show appropriate completion message
        if (openPR) {
          vscode.window.showWarningMessage(
            `Pipeline reset. Note: PR #${openPR.number} is still open.${!githubSyncSuccess ? " GitHub status sync failed - please update manually." : ""}`
          );
        } else if (!githubSyncSuccess) {
          vscode.window.showWarningMessage(
            "Pipeline reset. Local state cleared, but GitHub status sync failed. Please update issue label manually."
          );
        } else {
          vscode.window.showInformationMessage("Pipeline reset complete");
        }
      } catch (error) {
        logger.error("Failed to reset pipeline", { error });
        vscode.window.showErrorMessage("Failed to reset pipeline");
      }
    }
  );
}
