/**
 * Abort Pipeline command — Full Rollback
 *
 * Returns the issue to its pre-pipeline state as if the pipeline never ran:
 * 1. Stops all orchestrators (single + concurrent)
 * 2. Reopens the issue if it was closed during the pipeline
 * 3. Resets project board status to "Ready"
 * 4. Deletes local AND remote feature branches (unless an open PR exists)
 * 5. Removes worktrees (concurrent mode)
 * 6. Clears all context files, plan files, and empty knowledge entries
 *
 * For "pause" semantics (preserve state for resumption), use stopPipeline instead.
 *
 * @see Issue #119 - Add Abort Pipeline Command with GitHub Status Reset
 * @see docs/ARCHITECTURE.md - Context-Isolated Pipeline Architecture
 */

import * as vscode from "vscode";
import type { HeadlessOrchestrator } from "../services/HeadlessOrchestrator";
import type { ConcurrentPipelineManager } from "../services/ConcurrentPipelineManager";
import type { Logger } from "../utils/logger";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { PipelineTreeProvider } from "../views";
import type { StatusBarManager } from "../utils/statusBar";
import { getWorkspaceRoot } from "../config/settings";
import { fullResetGitHubIssue } from "../utils/githubStatusSync";
import { IpcClient } from "../services/IpcClient";
import { getRepoIdentity } from "../utils/configPathResolver";
import { KnowledgeService } from "@nightgauge/sdk";

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
 * Clean up local git state: checkout base branch, delete feature branch
 * (local + remote) unless an open PR exists.
 */
async function cleanupGitState(
  featureBranch: string,
  baseBranch: string,
  deleteBranch: boolean,
  hasOpenPR: boolean,
  cwd: string,
  logger: Logger
): Promise<{ success: boolean; error?: string }> {
  try {
    const ipc = IpcClient.getInstance();

    // Check for uncommitted changes if we're going to switch branches
    if (await hasUncommittedChanges(cwd)) {
      return {
        success: false,
        error: "Uncommitted changes detected. Please commit or stash your changes first.",
      };
    }

    // Checkout base branch
    logger.info("Checking out base branch", { baseBranch });
    await ipc.gitCheckout(baseBranch, cwd);

    // Fetch latest (graceful failure if network unavailable)
    logger.info("Fetching latest from origin", { baseBranch });
    try {
      await ipc.gitFetch(false, cwd);
    } catch {
      logger.warn("Failed to fetch latest, continuing with local state");
    }

    // Delete feature branch (local + remote) if requested and no open PR
    if (deleteBranch) {
      if (hasOpenPR) {
        logger.info("Skipping remote branch deletion — open PR exists", {
          featureBranch,
        });
        // Still delete local branch
        try {
          await ipc.gitBranchDelete(featureBranch, cwd);
        } catch {
          logger.warn("Could not delete local feature branch", {
            featureBranch,
          });
        }
      } else {
        // Delete both local and remote via BranchCleanup
        logger.info("Deleting feature branch (local + remote)", {
          featureBranch,
        });
        try {
          await ipc.gitBranchCleanup(featureBranch, cwd);
        } catch (error) {
          logger.warn("Could not fully clean up feature branch (may already be deleted)", {
            featureBranch,
            error,
          });
          // Fallback: try local-only deletion
          try {
            await ipc.gitBranchDelete(featureBranch, cwd);
          } catch {
            // Already logged above
          }
        }
      }
    }

    return { success: true };
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    return { success: false, error: message };
  }
}

/**
 * Register the Abort Pipeline command
 *
 * This command can be invoked from:
 * 1. Command palette (Nightgauge: Abort Pipeline)
 * 2. Pipeline view title menu (when pipeline has state)
 */
export function registerAbortPipelineCommand(
  orchestrator: HeadlessOrchestrator | null,
  logger: Logger,
  statusBar: StatusBarManager,
  pipelineStateService: PipelineStateService | null,
  treeProvider: PipelineTreeProvider,
  concurrentPipelineManager?: ConcurrentPipelineManager | null
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.abortPipeline", async () => {
    const workspaceRoot = getWorkspaceRoot();
    if (!workspaceRoot) {
      vscode.window.showErrorMessage("No workspace folder open");
      return;
    }

    // Check if orchestrator is available
    if (!orchestrator) {
      vscode.window.showErrorMessage(
        "Nightgauge SDK not initialized. Check extension logs for details."
      );
      return;
    }

    // Check if pipeline is running OR has state (abort works for both)
    const isSingleRunning = orchestrator.getIsRunning();
    const hasConcurrentSlots = (concurrentPipelineManager?.activeSlotCount ?? 0) > 0;
    const pipelineState = await pipelineStateService?.getState();
    const hasState = !!pipelineState?.issue_number;

    if (!isSingleRunning && !hasConcurrentSlots && !hasState) {
      vscode.window.showInformationMessage("No pipeline is running or has state to abort.");
      return;
    }

    // Collect ALL affected issue numbers BEFORE stopping anything.
    // After abortAll(), the slots map is empty and we lose this info.
    const affectedIssues: number[] = [];
    if (hasConcurrentSlots && concurrentPipelineManager) {
      for (const slot of concurrentPipelineManager.getActiveSlots()) {
        affectedIssues.push(slot.issueNumber);
      }
    }

    // Get context for confirmation dialog
    const issueNumber = pipelineState?.issue_number;
    const featureBranch = pipelineState?.branch;
    const baseBranch = pipelineState?.base_branch || "main";
    const currentBranch = await getCurrentBranch(workspaceRoot);
    const isOnFeatureBranch = currentBranch && featureBranch && currentBranch === featureBranch;

    // Add single-pipeline issue to affected list
    if (issueNumber && !affectedIssues.includes(issueNumber)) {
      affectedIssues.push(issueNumber);
    }

    // Start PR check early (needed for confirmation dialog text)
    const openPRPromise =
      isOnFeatureBranch && featureBranch
        ? getOpenPRForBranch(featureBranch, workspaceRoot)
        : Promise.resolve(null);

    // Await for confirmation dialog text
    const openPR = await openPRPromise;

    // Pause slot filling BEFORE showing the dialog so dying slots don't
    // refill from the queue while the user is reading the confirmation.
    if (hasConcurrentSlots && concurrentPipelineManager) {
      concurrentPipelineManager.pauseFilling();
    }

    // Build context-aware confirmation message
    let message: string;
    if (hasConcurrentSlots) {
      message = `Abort pipeline? This will stop ${concurrentPipelineManager!.activeSlotCount} concurrent slot(s), reopen affected issues, reset board status, and delete all local state. This is a FULL ROLLBACK.`;
    } else if (openPR) {
      message = `PR #${openPR.number} is still open! Abort will NOT merge it. The issue will be reopened and board status reset. Continue anyway?`;
    } else if (isSingleRunning) {
      const currentStage = orchestrator.getCurrentStage();
      message = `Abort pipeline? Currently running: ${currentStage || "unknown"}. Issue will be reopened and board status reset to "Ready".`;
    } else {
      message =
        "Abort pipeline? This will reopen the issue, reset board status, and clear all local state.";
    }

    const result = await vscode.window.showWarningMessage(message, { modal: true }, "Abort");

    if (result !== "Abort") {
      // Resume filling if user cancelled
      if (hasConcurrentSlots && concurrentPipelineManager) {
        concurrentPipelineManager.resumeFilling();
      }
      return; // User cancelled
    }

    // Ask about branch deletion separately to avoid complex multi-select dialog
    let deleteBranch = false;
    if (isOnFeatureBranch && featureBranch) {
      const branchChoice = await vscode.window.showQuickPick(["Keep branch", "Delete branch"], {
        title: "Delete feature branch (local + remote)?",
        placeHolder: `Choose what to do with branch "${featureBranch}"`,
      });
      deleteBranch = branchChoice === "Delete branch";
    }

    try {
      logger.info("Aborting pipeline — full rollback", {
        issueNumber,
        affectedIssues,
        featureBranch,
        baseBranch,
        openPR: openPR?.number,
        isSingleRunning,
        hasConcurrentSlots,
        deleteBranch,
      });

      // Step 1: Stop orchestrator and concurrent pipeline (if running)
      if (hasConcurrentSlots && concurrentPipelineManager) {
        logger.info("Aborting concurrent pipeline slots", {
          activeSlots: concurrentPipelineManager.activeSlotCount,
        });
        await concurrentPipelineManager.abortAll();
      }
      if (isSingleRunning) {
        logger.info("Stopping single orchestrator", {
          issueNumber,
        });
        orchestrator.stop();
      }

      // Immediately clear UI (instant perceived abort)
      treeProvider.clearIssue();
      treeProvider.resetAllStages();
      statusBar.showIdle();

      // Update context for UI
      vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", false);

      // Step 2: Full GitHub reset for ALL affected issues (reopen + board)
      // Run in parallel — each issue is independent
      const githubPromises = affectedIssues.map((num) =>
        fullResetGitHubIssue(num, workspaceRoot, logger).catch((error) => {
          logger.warn("GitHub reset failed for issue, continuing", {
            issueNumber: num,
            error: error instanceof Error ? error.message : String(error),
          });
          return { success: false, error: String(error) };
        })
      );

      // Step 3: Clean up git state (checkout base, delete branches)
      const gitPromise =
        isOnFeatureBranch && featureBranch
          ? cleanupGitState(
              featureBranch,
              baseBranch,
              deleteBranch,
              !!openPR,
              workspaceRoot,
              logger
            )
          : Promise.resolve({ success: true } as {
              success: boolean;
              error?: string;
            });

      const [githubResults, gitResult] = await Promise.all([
        Promise.all(githubPromises),
        gitPromise,
      ]);

      const githubAllSuccess = githubResults.every((r) => r.success);
      if (!githubAllSuccess) {
        logger.warn("Some GitHub resets failed, continuing with local cleanup");
      }

      if (!gitResult.success && isOnFeatureBranch && featureBranch) {
        vscode.window.showWarningMessage(
          `Git cleanup failed: ${gitResult.error}. Local state has been cleared.`
        );
      }

      // Step 4: Delete context files, plan files, prune knowledge
      const contextDir = `${workspaceRoot}/.nightgauge/pipeline`;
      const plansDir = `${workspaceRoot}/.nightgauge/plans`;

      // Collect file lists for ALL affected issues
      const allPlanPatterns = affectedIssues.map((num) =>
        vscode.workspace.findFiles(new vscode.RelativePattern(plansDir, `${num}-*.md`))
      );

      const [contextFiles, ...planFileArrays] = await Promise.all([
        vscode.workspace.findFiles(new vscode.RelativePattern(contextDir, "*.json")),
        ...allPlanPatterns,
      ]);

      const allPlanFiles = planFileArrays.flat();

      // Run clearPipeline and file deletions concurrently
      // clearPipeline handles state.json; file deletion skips state.json — no conflict
      await Promise.all([
        pipelineStateService?.clearPipeline(),
        // Delete context files in parallel (skip state.json)
        ...contextFiles
          .filter((f) => !f.fsPath.endsWith("state.json"))
          .map((f) => vscode.workspace.fs.delete(f).then(undefined, () => {})),
        // Delete plan files in parallel
        ...allPlanFiles.map((f) => vscode.workspace.fs.delete(f).then(undefined, () => {})),
        // Prune non-substantive knowledge entries (empty boilerplate from
        // scaffolding that was never enriched by planning). Substantive
        // entries are preserved — they have value for future reference.
        (async () => {
          try {
            const knowledgeSvc = new KnowledgeService(workspaceRoot);
            const pruned = await knowledgeSvc.pruneEmpty();
            if (pruned.length > 0) {
              logger.info("Pruned empty knowledge entries on abort", {
                pruned,
              });
            }
          } catch {
            // Knowledge cleanup is best-effort — don't block abort
          }
        })(),
      ]);

      logger.info("Pipeline aborted successfully — full rollback complete", {
        affectedIssues,
        githubAllSuccess,
      });

      // Show completion message
      const issueList = affectedIssues.map((n) => `#${n}`).join(", ");
      if (openPR) {
        vscode.window.showWarningMessage(
          `Pipeline aborted. Issue(s) ${issueList} reopened and reset. Note: PR #${openPR.number} is still open.${!githubAllSuccess ? " Some GitHub resets failed — check issue status manually." : ""}`
        );
      } else if (!githubAllSuccess) {
        vscode.window.showWarningMessage(
          `Pipeline aborted. Local state cleared, but some GitHub resets failed. Check issue status for ${issueList} manually.`
        );
      } else {
        vscode.window.showInformationMessage(
          `Pipeline aborted. Issue(s) ${issueList} reopened, board reset to Ready, local state cleared.`
        );
      }
    } catch (error) {
      logger.error("Failed to abort pipeline", { error });
      vscode.window.showErrorMessage("Failed to abort pipeline");
    }
  });
}
