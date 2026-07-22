/**
 * FailedIssueTreeItem - Tree item for failed pipeline issues
 *
 * Shows failed issues with error icon, failed stage, and error details.
 * Expanded by default to show error information.
 * Includes retry button in context menu.
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";
import type { FailedIssueReference } from "../../types/completedIssues";

/**
 * ErrorDetailsTreeItem - Simple tree item for error details display
 */
class ErrorDetailsTreeItem extends BaseTreeItem {
  constructor(message: string, fullError: string) {
    super(message, vscode.TreeItemCollapsibleState.None);
    this.setIconWithColor("info", new vscode.ThemeColor("errorForeground"));
    this.tooltip = fullError;
    this.contextValue = "error-details";
  }
}

/**
 * Maximum retry attempts before hard block (circuit breaker)
 * Follows StageTreeItem pattern: MAX_RETRIES = 3
 */
const MAX_RETRIES = 3;

/**
 * FailedIssueTreeItem - Represents a failed issue in the tree
 *
 * @example
 * ```typescript
 * const item = new FailedIssueTreeItem({
 *   issue_number: 43,
 *   title: 'Add auth',
 *   branch: 'feat/43-auth',
 *   failed_stage: 'pr-create',
 *   error: 'PR creation failed: unauthorized',
 *   retry_count: 0,
 *   timestamp: '2026-02-06T10:30:00Z'
 * });
 * ```
 */
export class FailedIssueTreeItem extends BaseTreeItem {
  readonly issue: FailedIssueReference;

  constructor(issue: FailedIssueReference) {
    super(`#${issue.issue_number}: ${issue.title}`, vscode.TreeItemCollapsibleState.Expanded);

    this.issue = issue;

    // Set icon with red theme color
    this.setIconWithColor("error", new vscode.ThemeColor("testing.iconFailed"));

    // Show failed stage and size badge in description
    this.description = this.createDescription();

    // Set tooltip
    this.tooltip = this.createTooltip();

    // Context value for menu visibility (includes retry button)
    // Block retry if max attempts reached (circuit breaker)
    this.contextValue =
      issue.retry_count >= MAX_RETRIES ? "failed-issue-max-retries" : "failed-issue";
  }

  /**
   * Build description: failed stage with optional size badge.
   * Format: "failed at PR Creation [M]"
   */
  private createDescription(): string {
    const base = `failed at ${this.formatStageName(this.issue.failed_stage)}`;
    const size = this.parseSize();
    return size ? `${base} [${size}]` : base;
  }

  /**
   * Parse size from labels (e.g. "size:M" → "M").
   */
  private parseSize(): string | undefined {
    const labels = this.issue.labels ?? [];
    for (const label of labels) {
      if (label.startsWith("size:")) {
        const value = label.slice("size:".length).toUpperCase();
        if (["XS", "S", "M", "L", "XL"].includes(value)) {
          return value;
        }
      }
    }
    return undefined;
  }

  /**
   * Format stage name for display
   */
  private formatStageName(stage: string): string {
    const labels: Record<string, string> = {
      "pipeline-start": "Initialize",
      "issue-pickup": "Issue Pickup",
      "feature-planning": "Feature Planning",
      "feature-dev": "Feature Development",
      "feature-validate": "Feature Validation",
      "pr-create": "PR Creation",
      "pr-merge": "PR Merge",
      "pipeline-finish": "Completion",
    };
    return (
      labels[stage] ??
      stage
        .split("-")
        .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
        .join(" ")
    );
  }

  /**
   * Create tooltip with full details
   */
  private createTooltip(): vscode.MarkdownString {
    const md = new vscode.MarkdownString();
    md.isTrusted = true;

    md.appendMarkdown(`**Issue #${this.issue.issue_number}** ❌\n\n`);
    md.appendMarkdown(`${this.issue.title}\n\n`);
    md.appendMarkdown(`**Branch:** ${this.issue.branch}\n\n`);
    md.appendMarkdown(`**Failed Stage:** ${this.formatStageName(this.issue.failed_stage)}\n\n`);
    md.appendMarkdown(`**Error:** ${this.issue.error}\n\n`);
    md.appendMarkdown(`**Failed:** ${this.issue.timestamp}\n\n`);

    if (this.issue.retry_count > 0) {
      md.appendMarkdown(`**Retry Count:** ${this.issue.retry_count}\n\n`);
    }

    if (this.issue.retry_count >= MAX_RETRIES) {
      md.appendMarkdown(
        `⚠️ Max retry attempts reached (${MAX_RETRIES}). Manual intervention required.`
      );
    } else {
      md.appendMarkdown(`Right-click to retry from ${this.issue.failed_stage}`);
    }

    return md;
  }

  /**
   * Get children (error details)
   */
  override getChildren(): BaseTreeItem[] {
    const errorItem = new ErrorDetailsTreeItem(
      `Error: ${this.truncateError(this.issue.error)}`,
      this.issue.error
    );
    return [errorItem];
  }

  /**
   * Truncate error for display
   */
  private truncateError(error: string): string {
    if (error.length > 80) {
      return error.substring(0, 77) + "...";
    }
    return error;
  }
}
