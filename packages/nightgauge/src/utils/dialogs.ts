/**
 * Dialog Utilities for Nightgauge Pipeline
 *
 * Reusable dialog functions for user prompts and confirmations.
 */

import * as vscode from "vscode";
import type { WarningSettings } from "../config/warningSettings";
import type { PRInfo } from "./prDetection";

/**
 * Dialog result type
 */
export type DialogResult = "add" | "cancel" | "view" | "dont-ask";

/**
 * Issue status for warnings
 */
export type IssueStatus = "in-progress" | "in-review";

/**
 * Issue data for status warning dialog
 */
export interface IssueWarningData {
  /** Issue number */
  number: number;
  /** Issue title */
  title: string;
  /** Issue status (in-progress or in-review) */
  status: IssueStatus;
  /** GitHub issue URL */
  url: string;
  /** PR information if available */
  prInfo?: PRInfo | null;
}

/**
 * Show status warning dialog for dragged issues
 *
 * Modal dialog that warns users when dragging issues that are already
 * In Progress or In Review, preventing duplicate work.
 *
 * @param issues - Array of issues requiring warnings
 * @param settings - Warning settings
 * @param context - Extension context for globalState persistence
 * @returns User's choice: 'add', 'cancel', 'view', or 'dont-ask'
 */
export async function showStatusWarningDialog(
  issues: IssueWarningData[],
  settings: WarningSettings,
  context: vscode.ExtensionContext
): Promise<DialogResult> {
  // Check if user previously dismissed this warning
  const dismissalKey = "nightgauge.warnings.dismissed";
  const dismissed = context.globalState.get<boolean>(dismissalKey, false);

  if (dismissed) {
    return "add"; // Skip warning
  }

  // Format message based on number of issues
  const message = formatWarningMessage(issues);

  // Build button array
  const buttons = buildDialogButtons(issues);

  // Show modal dialog
  const choice = await vscode.window.showWarningMessage(message, { modal: true }, ...buttons);

  // Handle user choice
  if (choice === "Don't ask again") {
    // Persist dismissal
    await context.globalState.update(dismissalKey, true);
    return "dont-ask";
  } else if (choice === "Add Anyway") {
    return "add";
  } else if (choice === "View on GitHub" || choice === "View PR") {
    return "view";
  } else {
    // User closed dialog or clicked Cancel
    return "cancel";
  }
}

/**
 * Format warning message for dialog
 */
function formatWarningMessage(issues: IssueWarningData[]): string {
  if (issues.length === 1) {
    const issue = issues[0];
    const statusLabel = formatStatusLabel(issue.status);

    return `Issue #${issue.number} is already ${statusLabel}.\n\nThis may indicate someone else is working on it. Adding it to the pipeline could cause duplicate work or merge conflicts.`;
  } else {
    const inProgressCount = issues.filter((i) => i.status === "in-progress").length;
    const inReviewCount = issues.filter((i) => i.status === "in-review").length;

    const summary = formatIssueStatusSummary(issues);

    return `${issues.length} issues are already in progress or under review:\n\n${summary}\n\nAdding these to the pipeline may cause duplicate work or conflicts.`;
  }
}

/**
 * Build dialog button array
 */
function buildDialogButtons(issues: IssueWarningData[]): string[] {
  const buttons: string[] = ["Add Anyway"];

  if (issues.length === 1) {
    const issue = issues[0];

    // Add view button
    if (issue.prInfo && issue.prInfo.url) {
      buttons.splice(1, 0, "View PR");
    } else {
      buttons.splice(1, 0, "View on GitHub");
    }
  } else {
    // For multiple issues, show view button that opens first issue
    buttons.splice(1, 0, "View Details");
  }

  // Add "Don't ask again" option
  buttons.push("Don't ask again");

  return buttons;
}

/**
 * Format issue status label for display
 */
function formatStatusLabel(status: IssueStatus): string {
  switch (status) {
    case "in-progress":
      return "In Progress";
    case "in-review":
      return "In Review";
    default:
      return status;
  }
}

/**
 * Format summary of issues with status badges
 *
 * @param issues - Issues to format
 * @returns Formatted string with issue list
 */
export function formatIssueStatusSummary(issues: IssueWarningData[]): string {
  return issues
    .map((issue) => {
      const statusBadge = formatStatusBadge(issue.status);
      const title = truncateTitle(issue.title, 50);
      return `  • #${issue.number} ${statusBadge} ${title}`;
    })
    .join("\n");
}

/**
 * Format status badge for display
 */
function formatStatusBadge(status: IssueStatus): string {
  switch (status) {
    case "in-progress":
      return "$(sync~spin)";
    case "in-review":
      return "$(git-pull-request)";
    default:
      return "$(info)";
  }
}

/**
 * Truncate issue title if too long
 */
function truncateTitle(title: string, maxLength: number): string {
  if (title.length <= maxLength) {
    return title;
  }
  return title.substring(0, maxLength - 3) + "...";
}

/**
 * Open URL in browser
 *
 * Helper for opening GitHub issue or PR URLs.
 *
 * @param url - URL to open
 */
export async function openUrl(url: string): Promise<void> {
  await vscode.env.openExternal(vscode.Uri.parse(url));
}

/**
 * Reset "Don't ask again" preference
 *
 * Utility function for tests or user command to reset dismissal.
 *
 * @param context - Extension context
 */
export async function resetWarningDismissal(context: vscode.ExtensionContext): Promise<void> {
  await context.globalState.update("nightgauge.warnings.dismissed", undefined);
}
