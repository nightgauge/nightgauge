/**
 * EpicGroupTreeItem - Tree item representing an epic group in the project board
 *
 * Displays epics as collapsible groups containing their sub-issues,
 * with progress indicators showing completion status.
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";
import { ReadyIssueTreeItem } from "./ReadyIssueTreeItem";
import type { ReadyIssue } from "../../services/ProjectBoardService";

/**
 * Epic information for display
 */
export interface EpicInfo {
  number: number;
  title: string;
  url: string;
}

/**
 * Group structure for organizing issues by epic
 */
export interface EpicGroup {
  epic: EpicInfo | null;
  issues: ReadyIssue[];
}

/**
 * Result of groupIssuesByEpic function
 */
export interface GroupByEpicResult {
  /** Groups of sub-issues organized by their parent epic */
  groups: EpicGroup[];
}

/**
 * EpicGroupTreeItem - Collapsible epic group in the tree view
 *
 * @example
 * ```typescript
 * const epicGroup = new EpicGroupTreeItem(
 *   { number: 124, title: 'User Authentication', url: '...' },
 *   [issue1, issue2, issue3],
 *   { showDependencies: true, defaultCollapsed: false }
 * );
 * ```
 */
export class EpicGroupTreeItem extends BaseTreeItem {
  readonly epic: EpicInfo | null;
  /** Repository owner for cross-repo drag-and-drop (e.g. 'nightgauge') */
  readonly repoOwner: string | undefined;
  /** Repository name for cross-repo drag-and-drop (e.g. 'nightgauge') */
  readonly repoName: string | undefined;
  private completedCount: number;
  private totalCount: number;

  constructor(
    epic: EpicInfo | null,
    issues: ReadyIssue[],
    options?: {
      showDependencies?: boolean;
      defaultCollapsed?: boolean;
      /** Enable checkbox for batch selection (Issue #125) */
      enableCheckbox?: boolean;
      /** Selected issue numbers for checkbox state */
      selectedIssueNumbers?: Set<number>;
      /** Repository owner (e.g. 'nightgauge') */
      repoOwner?: string;
      /** Repository name (e.g. 'acme-dashboard') */
      repoName?: string;
    }
  ) {
    const showDependencies = options?.showDependencies ?? true;
    const defaultCollapsed = options?.defaultCollapsed ?? false;
    const enableCheckbox = options?.enableCheckbox ?? false;
    const selectedIssueNumbers = options?.selectedIssueNumbers ?? new Set();

    const label = epic ? `Epic #${epic.number}: ${epic.title}` : "No Epic";
    // Empty epic (no sub-issues yet, Issue #3329): render as a leaf so VSCode
    // doesn't show an empty expand chevron. The user still sees the epic
    // exists; sub-issues added later will give it children on next refresh.
    const collapsibleState =
      issues.length === 0
        ? vscode.TreeItemCollapsibleState.None
        : defaultCollapsed
          ? vscode.TreeItemCollapsibleState.Collapsed
          : vscode.TreeItemCollapsibleState.Expanded;

    super(label, collapsibleState);

    this.epic = epic;
    this.repoOwner = options?.repoOwner;
    this.repoName = options?.repoName;
    this.totalCount = issues.length;

    // Calculate completed count (issues with status:done or closed state)
    // For now, we consider issues that are not in the current status view as completed
    // This is a simplification - in practice, we'd check the actual issue state
    this.completedCount = 0;

    this.contextValue = epic ? "epicGroup" : "noEpicGroup";

    // Set description with progress
    this.description = `(${this.completedCount}/${this.totalCount} complete)`;

    // Set icon - purple project icon for epics, folder for "No Epic"
    if (epic) {
      this.setIconWithColor("project", new vscode.ThemeColor("charts.purple"));
    } else {
      this.setIconWithColor("folder", new vscode.ThemeColor("foreground"));
    }

    // Create tooltip
    this.tooltip = this.createTooltip(issues);

    // Add child issues with checkbox support
    for (const issue of issues) {
      this.addChild(
        new ReadyIssueTreeItem(issue, {
          showDependencies,
          enableCheckbox,
          checked: selectedIssueNumbers.has(issue.number),
        })
      );
    }

    // Set command to view epic on GitHub if it exists
    if (epic && epic.url) {
      this.command = {
        command: "vscode.open",
        title: "View Epic on GitHub",
        arguments: [vscode.Uri.parse(epic.url)],
      };
    }
  }

  /**
   * Create a rich tooltip with epic details
   */
  private createTooltip(issues: ReadyIssue[]): vscode.MarkdownString {
    const md = new vscode.MarkdownString();
    md.isTrusted = true;

    if (this.epic) {
      md.appendMarkdown(`**Epic #${this.epic.number}** - ${this.epic.title}\n\n`);
    } else {
      md.appendMarkdown(`**Standalone Issues**\n\n`);
      md.appendMarkdown(`Issues not linked to any epic\n\n`);
    }

    md.appendMarkdown(`**Progress:** ${this.completedCount}/${this.totalCount} complete\n\n`);

    if (issues.length === 0) {
      md.appendMarkdown(`_No sub-issues yet. Add them via \`nightgauge issue create-sub\`._\n`);
    } else {
      md.appendMarkdown(`**Issues:**\n\n`);
      for (const issue of issues.slice(0, 5)) {
        const priorityIcon = issue.priority === "P0" ? "🔴" : issue.priority === "P1" ? "🟠" : "🔵";
        md.appendMarkdown(`- ${priorityIcon} #${issue.number}: ${issue.title}\n`);
      }

      if (issues.length > 5) {
        md.appendMarkdown(`- ... and ${issues.length - 5} more\n`);
      }
    }

    md.appendMarkdown(`\n---\n\n`);
    if (this.epic) {
      md.appendMarkdown(`Click to view epic on GitHub`);
    } else {
      md.appendMarkdown(`Expand to see standalone issues`);
    }

    return md;
  }

  /**
   * Get the total count of issues in this group
   */
  getTotalCount(): number {
    return this.totalCount;
  }

  /**
   * Get the completed count of issues in this group
   */
  getCompletedCount(): number {
    return this.completedCount;
  }

  /**
   * Get issue numbers from all child ReadyIssueTreeItems (Issue #213)
   *
   * This method extracts the issue numbers from the epic's child items,
   * useful for batch pipeline operations.
   *
   * @returns Array of issue numbers contained in this epic group
   */
  getChildIssueNumbers(): number[] {
    const children = this.getChildren();
    return children
      .filter((child): child is ReadyIssueTreeItem => child instanceof ReadyIssueTreeItem)
      .map((child) => child.issueNumber);
  }
}

/**
 * Group issues by their epic reference.
 *
 * @param issues - Status-filtered issues to group (only these appear in the tab)
 * @param epicMetadata - Map of epic number → EpicInfo for resolving titles.
 *        Built from per-status caches — avoids the expensive 11s+ getAllItems() call
 *        that fetched 537 items just to look up a handful of epic titles.
 * @returns Object with groups of sub-issues organized by parent epic.
 *          Each issue appears in exactly one tab matching its status.
 *          Epic issues (type:epic) are skipped since they are already
 *          represented by the EpicGroupTreeItem headers.
 */
export function groupIssuesByEpic(
  issues: ReadyIssue[],
  epicMetadata: Map<number, EpicInfo>
): GroupByEpicResult {
  const groups = new Map<number | null, ReadyIssue[]>();

  // Group each issue by its epicRef. Only include issues that are in the
  // current status-filtered set — an issue appears in exactly one tab.
  // The epic header appears in any tab where at least one sub-issue matches,
  // OR (Issue #3329) in the epic's own status tab when it has no sub-issues
  // yet — otherwise a freshly-created epic would be invisible.
  for (const issue of issues) {
    if (issue.labels.includes("type:epic")) {
      if (!groups.has(issue.number)) {
        groups.set(issue.number, []);
      }
      if (!epicMetadata.has(issue.number)) {
        epicMetadata.set(issue.number, {
          number: issue.number,
          title: issue.title,
          url: issue.url,
        });
      }
      continue;
    }
    const key = issue.epicRef ?? null;
    if (!groups.has(key)) {
      groups.set(key, []);
    }
    groups.get(key)!.push(issue);
  }

  const result: EpicGroup[] = [];

  // Epic groups first (sorted by epic number)
  const epicNumbers = [...groups.keys()]
    .filter((k): k is number => k !== null)
    .sort((a, b) => a - b);

  for (const epicNum of epicNumbers) {
    const epicInfo = epicMetadata.get(epicNum);
    result.push({
      epic: epicInfo ?? {
        number: epicNum,
        title: "(loading...)",
        url: "",
      },
      issues: groups.get(epicNum)!,
    });
  }

  // "No Epic" group last
  if (groups.has(null)) {
    result.push({
      epic: null,
      issues: groups.get(null)!,
    });
  }

  return { groups: result };
}
