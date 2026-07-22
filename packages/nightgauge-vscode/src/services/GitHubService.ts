/**
 * GitHubService - GitHub API integration via Go binary IPC
 *
 * Provides methods for interacting with GitHub's native sub-issues feature
 * and other GitHub operations not covered by ProjectBoardService.
 *
 * All operations go through the Go binary IPC server — no direct `gh` CLI calls.
 */

import { IpcClient } from "./IpcClient";
import { SubIssue } from "../utils/subIssueProgress";

/**
 * GitHubService - Handles GitHub API operations
 *
 * @example
 * ```typescript
 * const service = new GitHubService('nightgauge', 'nightgauge');
 * const subIssues = await service.fetchSubIssues(295);
 * ```
 */
export class GitHubService {
  private owner: string;
  private repo: string;
  private ipc: IpcClient;

  constructor(owner: string, repo: string) {
    this.owner = owner;
    this.repo = repo;
    this.ipc = IpcClient.getInstance();
  }

  /**
   * Fetch sub-issues for a parent issue using GitHub's native sub-issues API
   *
   * @param parentNumber - The parent issue number
   * @returns Array of sub-issues with number and state
   * @throws Error if IPC call fails
   */
  async fetchSubIssues(parentNumber: number): Promise<SubIssue[]> {
    if (!Number.isInteger(parentNumber) || parentNumber <= 0) {
      throw new Error(`Invalid parent issue number: ${parentNumber}. Must be a positive integer.`);
    }

    try {
      const issue = await this.ipc.issueView(this.owner, this.repo, parentNumber);

      if (!issue.subIssues || issue.subIssues.length === 0) {
        return [];
      }

      return issue.subIssues.map((sub) => ({
        number: sub.number,
        state: sub.state as "OPEN" | "CLOSED",
      }));
    } catch (error) {
      if (error instanceof Error) {
        if (error.message.includes("not found")) {
          throw new Error(`Parent issue #${parentNumber} not found`, { cause: error });
        }
        throw new Error(`Failed to fetch sub-issues for #${parentNumber}: ${error.message}`, {
          cause: error,
        });
      }
      throw new Error(`Failed to fetch sub-issues for #${parentNumber}`, { cause: error });
    }
  }

  /**
   * Link a sub-issue to a parent issue using GitHub's native sub-issues API
   *
   * @param childNumber - The child issue number to link
   * @param parentNumber - The parent issue number
   * @throws Error if IPC call fails or validation fails
   */
  async linkSubIssueToParent(childNumber: number, parentNumber: number): Promise<void> {
    if (!Number.isInteger(childNumber) || childNumber <= 0) {
      throw new Error(`Invalid child issue number: ${childNumber}. Must be a positive integer.`);
    }

    if (!Number.isInteger(parentNumber) || parentNumber <= 0) {
      throw new Error(`Invalid parent issue number: ${parentNumber}. Must be a positive integer.`);
    }

    if (childNumber === parentNumber) {
      throw new Error(
        `Cannot link issue to itself: child #${childNumber} === parent #${parentNumber}`
      );
    }

    try {
      await this.ipc.issueLinkSubIssue(this.owner, this.repo, parentNumber, childNumber);
    } catch (error) {
      if (error instanceof Error) {
        if (error.message.includes("not found")) {
          throw new Error(`Issue #${childNumber} or #${parentNumber} not found`, {
            cause: error,
          });
        }
        throw new Error(
          `Failed to link #${childNumber} to parent #${parentNumber}: ${error.message}`,
          { cause: error }
        );
      }
      throw new Error(`Failed to link #${childNumber} to parent #${parentNumber}`, {
        cause: error,
      });
    }
  }

  /**
   * Fetch issue metadata including title and parent reference
   *
   * @param issueNumber - The issue number
   * @returns Object with issue title and optional parent number
   * @throws Error if IPC call fails
   */
  async fetchIssueMetadata(issueNumber: number): Promise<{ title: string; parent?: number }> {
    if (!Number.isInteger(issueNumber) || issueNumber <= 0) {
      throw new Error(`Invalid issue number: ${issueNumber}. Must be a positive integer.`);
    }

    try {
      const issue = await this.ipc.issueView(this.owner, this.repo, issueNumber);

      return {
        title: issue.title,
        // IssueDetail doesn't expose parent directly — it's via subIssues on the parent
        // The Go backend returns parent info if available
        parent: undefined,
      };
    } catch (error) {
      if (error instanceof Error) {
        if (error.message.includes("not found")) {
          throw new Error(`Issue #${issueNumber} not found`, { cause: error });
        }
        throw new Error(`Failed to fetch metadata for #${issueNumber}: ${error.message}`, {
          cause: error,
        });
      }
      throw new Error(`Failed to fetch metadata for #${issueNumber}`, { cause: error });
    }
  }
}
