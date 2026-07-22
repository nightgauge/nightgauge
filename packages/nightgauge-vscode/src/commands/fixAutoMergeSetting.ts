/**
 * Fix Auto-Merge Setting command
 *
 * One-click command to disable `allow_auto_merge` on the active repository.
 * The pipeline's pr-merge stage requires exclusive control over PR merging.
 * When auto-merge is enabled, PRs merge automatically once CI passes, bypassing
 * the pipeline's watch/resolve loop and recovery mechanisms.
 *
 * @see Issue #2720 — Detect and disable repo auto-merge
 * @see docs/GIT_WORKFLOW.md — Auto-Merge and Pipeline Control section
 */

import * as vscode from "vscode";
import type { Logger } from "../utils/logger";
import type { RepositorySettingsService } from "../services/RepositorySettingsService";
import { getWorkspaceRoot } from "../config/settings";
import { execFileSync } from "child_process";

/**
 * Resolves the GitHub owner and repo from the current workspace's git remote.
 * Returns null if the workspace has no git remote or the remote is not GitHub.
 */
function resolveOwnerAndRepo(workspaceRoot: string): { owner: string; repo: string } | null {
  try {
    const remoteUrl = execFileSync("git", ["remote", "get-url", "origin"], {
      cwd: workspaceRoot,
      encoding: "utf8",
      stdio: ["pipe", "pipe", "pipe"],
    }).trim();

    // Match SSH: git@github.com:owner/repo.git
    const sshMatch = remoteUrl.match(/github\.com[:/]([^/]+)\/([^/.]+)/);
    if (sshMatch) {
      return { owner: sshMatch[1], repo: sshMatch[2] };
    }

    // Match HTTPS: https://github.com/owner/repo.git
    const httpsMatch = remoteUrl.match(/github\.com\/([^/]+)\/([^/.]+)/);
    if (httpsMatch) {
      return { owner: httpsMatch[1], repo: httpsMatch[2] };
    }

    return null;
  } catch {
    return null;
  }
}

/**
 * Register the Fix Auto-Merge Setting command.
 *
 * Command ID: nightgauge.fixAutoMergeSetting
 */
export function registerFixAutoMergeSettingCommand(
  logger: Logger,
  repositorySettingsService: RepositorySettingsService
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.fixAutoMergeSetting", async () => {
    logger.debug("[fixAutoMergeSetting] command invoked");

    const workspaceRoot = getWorkspaceRoot();
    if (!workspaceRoot) {
      vscode.window.showErrorMessage(
        "Nightgauge: No workspace folder open. Cannot determine repository."
      );
      return;
    }

    const repoInfo = resolveOwnerAndRepo(workspaceRoot);
    if (!repoInfo) {
      vscode.window.showErrorMessage(
        "Nightgauge: Could not detect GitHub repository from git remote. " +
          "Ensure the workspace has a GitHub remote configured."
      );
      return;
    }

    const { owner, repo } = repoInfo;

    // Confirm with user before making changes
    const choice = await vscode.window.showWarningMessage(
      `Disable auto-merge on ${owner}/${repo}?\n\n` +
        "The pipeline's pr-merge stage requires exclusive control over PR merging. " +
        "Auto-merge bypasses pipeline recovery and self-healing mechanisms.",
      { modal: true },
      "Disable Auto-Merge",
      "Cancel"
    );

    if (choice !== "Disable Auto-Merge") {
      logger.debug("[fixAutoMergeSetting] user cancelled");
      return;
    }

    try {
      await repositorySettingsService.disableAutoMerge(owner, repo);
      logger.info(`[fixAutoMergeSetting] auto-merge disabled on ${owner}/${repo}`);
      vscode.window.showInformationMessage(
        `✓ Auto-merge disabled on ${owner}/${repo}. The pipeline now has exclusive control over PR merging.`
      );
    } catch (error) {
      const msg = error instanceof Error ? error.message : String(error);
      logger.error("[fixAutoMergeSetting] failed to disable auto-merge", { error });
      vscode.window.showErrorMessage(`Failed to disable auto-merge on ${owner}/${repo}: ${msg}`);
    }
  });
}
