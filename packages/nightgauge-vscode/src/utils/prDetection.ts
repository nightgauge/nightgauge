/**
 * PR Detection Helpers
 *
 * Utilities for detecting and fetching PR information associated with issues.
 * Uses `gh` CLI for authenticated access to GitHub API.
 */

import * as vscode from "vscode";
import { exec } from "child_process";
import { promisify } from "util";

const execAsync = promisify(exec);

/**
 * PR information structure
 */
export interface PRInfo {
  /** PR number */
  number: number;
  /** Full PR URL */
  url: string;
  /** PR title */
  title?: string;
}

/**
 * Get PR information for an issue
 *
 * Uses `gh pr list` command to find PRs associated with the issue.
 * This is a best-effort check - returns null if no PR found or on error.
 *
 * @param issueNumber - Issue number to search for
 * @param workspaceRoot - Workspace root directory for git operations
 * @returns PR info if found, null otherwise
 */
export async function getPRForIssue(
  issueNumber: number,
  workspaceRoot: string
): Promise<PRInfo | null> {
  try {
    // Use gh CLI to search for PRs linked to this issue
    // Search by issue number in PR body/title
    const { stdout } = await execAsync(
      `gh pr list --json number,url,title --search "${issueNumber} in:body,title"`,
      {
        cwd: workspaceRoot,
        timeout: 5000, // 5 second timeout
      }
    );

    return parsePRFromGHCLI(stdout);
  } catch (error) {
    // Graceful degradation - gh CLI not available, network issues, etc.
    // Don't log as error since this is optional enrichment
    return null;
  }
}

/**
 * Parse PR information from `gh pr list` JSON output
 *
 * @param output - JSON output from gh CLI
 * @returns First PR found, or null if none
 */
export function parsePRFromGHCLI(output: string): PRInfo | null {
  try {
    const prs = JSON.parse(output);

    if (!Array.isArray(prs) || prs.length === 0) {
      return null;
    }

    // Return first PR (most relevant)
    const pr = prs[0];

    // Validate structure
    if (typeof pr.number !== "number" || typeof pr.url !== "string") {
      return null;
    }

    return {
      number: pr.number,
      url: pr.url,
      title: typeof pr.title === "string" ? pr.title : undefined,
    };
  } catch (error) {
    // Malformed JSON - return null
    return null;
  }
}

/**
 * Check if an issue has status:in-review label (heuristic for PR existence)
 *
 * This is a fast, deterministic check that doesn't require API calls.
 *
 * @param labels - Array of label strings from issue
 * @returns True if issue likely has a PR (has in-review label)
 */
export function hasInReviewLabel(labels: string[]): boolean {
  return labels.some((label) => label.toLowerCase().includes("status:in-review"));
}
