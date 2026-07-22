/**
 * GitHub Project Board Status Sync Utility
 *
 * Provides two abort strategies:
 * - `resetGitHubStatus()` — Board-only reset to "Ready" (used by stop/pause)
 * - `fullResetGitHubIssue()` — Full rollback: reopen issue + reset board (used by abort)
 *
 * @see Issue #119 - Abort Pipeline with GitHub Status Reset
 * @see Issue #1718 - Clean up deprecated label sync code
 */

import { updateProjectItemStatus } from "./projectFieldWriter";
import { IpcClient } from "../services/IpcClient";
import { getRepoIdentity } from "./configPathResolver";
import type { Logger } from "./logger";

// ============================================================================
// Public Interface (preserved for callers)
// ============================================================================

/**
 * Result of a GitHub status sync operation
 */
export interface GitHubSyncResult {
  success: boolean;
  error?: string;
}

// ============================================================================
// Public API
// ============================================================================

/**
 * Reset GitHub issue status to "Ready" on the project board.
 *
 * Board-only reset — does NOT reopen closed issues. Use this for
 * "stop/pause" semantics where you want to preserve issue state.
 *
 * @param issueNumber - The GitHub issue number to reset
 * @param cwd - The workspace root directory
 * @param logger - Logger instance for structured logging
 * @returns Success status and optional error message
 */
export async function resetGitHubStatus(
  issueNumber: number,
  cwd: string,
  logger: Logger
): Promise<GitHubSyncResult> {
  try {
    if (!Number.isInteger(issueNumber) || issueNumber <= 0) {
      return { success: false, error: `Invalid issue number: ${issueNumber}` };
    }

    const result = await updateProjectItemStatus(issueNumber, "Ready", cwd, logger);

    if (result.success) {
      logger.info("GitHub status reset to Ready", { issueNumber });
    } else {
      logger.warn("Failed to reset GitHub status", {
        issueNumber,
        error: result.error,
      });
    }

    return result;
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    logger.warn("Failed to reset GitHub status (will continue with local cleanup)", {
      issueNumber,
      error: message,
    });
    return { success: false, error: message };
  }
}

/**
 * Full rollback of a GitHub issue to pre-pipeline state.
 *
 * Used by "abort" — the nuclear option that returns an issue to a clean
 * state as if the pipeline never ran. This:
 * 1. Reopens the issue if it was closed during the pipeline
 * 2. Resets the project board status to "Ready"
 *
 * Both operations use graceful degradation — failures are logged but
 * don't block the abort flow.
 *
 * @param issueNumber - The GitHub issue number to reset
 * @param cwd - The workspace root directory
 * @param logger - Logger instance for structured logging
 * @returns Success status and optional error message
 */
export async function fullResetGitHubIssue(
  issueNumber: number,
  cwd: string,
  logger: Logger
): Promise<GitHubSyncResult> {
  if (!Number.isInteger(issueNumber) || issueNumber <= 0) {
    return { success: false, error: `Invalid issue number: ${issueNumber}` };
  }

  const errors: string[] = [];

  // Step 1: Reopen the issue if it's closed
  try {
    const identity = await getRepoIdentity(cwd);
    if (identity) {
      const ipc = IpcClient.getInstance();
      // issueReopen is idempotent — safe to call on an already-open issue
      // (GitHub's reopenIssue mutation is a no-op if issue is already open)
      await ipc.issueReopen(identity.owner, identity.repo, issueNumber);
      logger.info("Reopened issue on abort", { issueNumber });
    } else {
      errors.push("Could not determine repo identity for issue reopen");
      logger.warn("Could not determine repo identity for issue reopen", {
        issueNumber,
      });
    }
  } catch (error) {
    const msg = error instanceof Error ? error.message : String(error);
    errors.push(`Issue reopen failed: ${msg}`);
    logger.warn("Failed to reopen issue on abort (continuing)", {
      issueNumber,
      error: msg,
    });
  }

  // Step 2: Reset project board status to "Ready"
  const boardResult = await resetGitHubStatus(issueNumber, cwd, logger);
  if (!boardResult.success) {
    errors.push(`Board reset failed: ${boardResult.error}`);
  }

  if (errors.length === 0) {
    return { success: true };
  }

  return {
    success: boardResult.success, // Partial success if board updated
    error: errors.join("; "),
  };
}
