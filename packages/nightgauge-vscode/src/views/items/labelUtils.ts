/**
 * Shared utilities for parsing size and priority from GitHub issue labels
 *
 * Used by IssueTreeItem, CompletedIssueTreeItem, and FailedIssueTreeItem
 * to display size badges and priority icons in the pipeline sidebar.
 *
 * @see Issue #1611 - Show size labels and priority in Pipeline sidebar
 */

import * as vscode from "vscode";

/** Size values parsed from labels */
export type Size = "XS" | "S" | "M" | "L" | "XL";

/** Priority values parsed from labels */
export type Priority = "P0" | "P1" | "P2" | "P3";

/**
 * Priority icon display configuration
 * Matches ReadyIssueTreeItem's PRIORITY_CONFIG
 */
const PRIORITY_CONFIG: Record<Priority, { icon: string; color: string }> = {
  P0: { icon: "circle-filled", color: "problemsErrorIcon.foreground" },
  P1: { icon: "circle-filled", color: "problemsWarningIcon.foreground" },
  P2: { icon: "circle-filled", color: "charts.blue" },
  P3: { icon: "circle-filled", color: "charts.green" },
};

/**
 * Extract size from labels array
 *
 * Looks for labels matching "size:XS", "size:S", "size:M", "size:L", "size:XL"
 */
export function extractSize(labels: string[]): Size | null {
  const sizeLabel = labels.find((l) => /^size:(XS|S|M|L|XL)$/i.test(l));
  if (!sizeLabel) return null;
  return sizeLabel.split(":")[1].toUpperCase() as Size;
}

/**
 * Extract priority from labels array
 *
 * Maps "priority:critical" → P0, "priority:high" → P1,
 * "priority:medium" → P2, "priority:low" → P3
 */
export function extractPriority(labels: string[]): Priority | null {
  const priorityLabel = labels.find((l) => /^priority:(critical|high|medium|low)$/i.test(l));
  if (!priorityLabel) return null;

  const level = priorityLabel.split(":")[1].toLowerCase();
  const mapping: Record<string, Priority> = {
    critical: "P0",
    high: "P1",
    medium: "P2",
    low: "P3",
  };
  return mapping[level] ?? null;
}

/**
 * Get the ThemeIcon for a priority level
 */
export function getPriorityIcon(priority: Priority): vscode.ThemeIcon {
  const config = PRIORITY_CONFIG[priority];
  return new vscode.ThemeIcon(config.icon, new vscode.ThemeColor(config.color));
}

/**
 * Format priority for tooltip display
 */
export function formatPriority(priority: Priority): string {
  const names: Record<Priority, string> = {
    P0: "P0 (Critical)",
    P1: "P1 (High)",
    P2: "P2 (Medium)",
    P3: "P3 (Low)",
  };
  return names[priority];
}

/**
 * Format size for tooltip display
 */
export function formatSize(size: Size): string {
  const names: Record<Size, string> = {
    XS: "XS (Extra Small)",
    S: "S (Small)",
    M: "M (Medium)",
    L: "L (Large)",
    XL: "XL (Extra Large)",
  };
  return names[size];
}
