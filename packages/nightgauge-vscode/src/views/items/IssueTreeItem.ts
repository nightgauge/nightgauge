/**
 * IssueTreeItem - Tree item representing the current issue
 *
 * Root item in the pipeline tree that shows issue number and title.
 * Collapsible to reveal stage children.
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";
import { isDependabotIssue, getDependabotType } from "../../utils/dependabotUtils";

/** Priority levels parsed from labels (matches ReadyIssueTreeItem) */
type Priority = "P0" | "P1" | "P2" | "P3";
/** Size levels parsed from labels */
type Size = "XS" | "S" | "M" | "L" | "XL";

/** Priority to icon/color mapping (mirrors ReadyIssueTreeItem) */
const PRIORITY_CONFIG: Record<Priority, { icon: string; color: string }> = {
  P0: { icon: "circle-filled", color: "problemsErrorIcon.foreground" },
  P1: { icon: "circle-filled", color: "problemsWarningIcon.foreground" },
  P2: { icon: "circle-filled", color: "charts.blue" },
  P3: { icon: "circle-filled", color: "charts.green" },
};

/** Map label priority values to P0–P3 */
const PRIORITY_LABEL_MAP: Record<string, Priority> = {
  critical: "P0",
  high: "P1",
  medium: "P2",
  low: "P3",
};

/**
 * Parse priority from labels array (e.g. "priority:high" → "P1").
 * Returns undefined if no matching label found.
 */
function parsePriority(labels: string[]): Priority | undefined {
  for (const label of labels) {
    if (label.startsWith("priority:")) {
      const value = label.slice("priority:".length).toLowerCase();
      return PRIORITY_LABEL_MAP[value];
    }
  }
  return undefined;
}

/**
 * Parse size from labels array (e.g. "size:M" → "M").
 * Returns undefined if no matching label found.
 */
function parseSize(labels: string[]): Size | undefined {
  const valid: Size[] = ["XS", "S", "M", "L", "XL"];
  for (const label of labels) {
    if (label.startsWith("size:")) {
      const value = label.slice("size:".length).toUpperCase() as Size;
      if (valid.includes(value)) {
        return value;
      }
    }
  }
  return undefined;
}

/**
 * Format priority for human-readable tooltip display.
 */
function formatPriority(priority: Priority): string {
  switch (priority) {
    case "P0":
      return "P0 (Critical)";
    case "P1":
      return "P1 (High)";
    case "P2":
      return "P2 (Medium)";
    case "P3":
      return "P3 (Low)";
  }
}

/**
 * Format size for human-readable tooltip display.
 */
function formatSize(size: Size): string {
  switch (size) {
    case "XS":
      return "XS (Extra Small)";
    case "S":
      return "S (Small)";
    case "M":
      return "M (Medium)";
    case "L":
      return "L (Large)";
    case "XL":
      return "XL (Extra Large)";
  }
}

/**
 * Issue data for display
 */
export interface IssueInfo {
  number: number;
  title: string;
  branch: string;
  /** Target branch for PR (e.g., main, develop) */
  baseBranch?: string;
  labels?: string[];
  /** GitHub issue URL */
  url?: string;
}

/**
 * IssueTreeItem - Represents the current issue being worked on
 *
 * @example
 * ```typescript
 * const issue = new IssueTreeItem({
 *   number: 92,
 *   title: 'Pipeline Sidebar',
 *   branch: 'feat/92-pipeline-sidebar',
 *   labels: ['feature', 'high-priority']
 * });
 * ```
 */
export class IssueTreeItem extends BaseTreeItem {
  readonly issueNumber: number;
  readonly issueUrl?: string;
  private issueInfo: IssueInfo;

  constructor(issueInfo: IssueInfo) {
    super(`#${issueInfo.number} - ${issueInfo.title}`, vscode.TreeItemCollapsibleState.Expanded);

    this.issueNumber = issueInfo.number;
    this.issueUrl = issueInfo.url;
    this.issueInfo = issueInfo;

    this.applyLabelBadges();
    this.contextValue = "issue";
    this.tooltip = this.createTooltip();
    this.description = this.createDescription();

    // Click to view on GitHub if URL is available (Issue #297)
    if (issueInfo.url) {
      this.command = {
        command: "nightgauge.viewIssueOnGitHub",
        title: "View on GitHub",
        arguments: [this],
      };
    }
  }

  /**
   * Apply priority-colored icon and size badge from labels.
   * Dependabot issues get shield (security) or package (dependency) icons.
   * Falls back to plain 'issues' icon when labels are absent.
   *
   * Issue #2486: Dependabot icon treatment
   */
  private applyLabelBadges(): void {
    const labels = this.issueInfo.labels ?? [];
    const depType = getDependabotType(labels);
    if (depType === "security") {
      this.setIconWithColor("shield", new vscode.ThemeColor("testing.iconFailed"));
    } else if (depType === "dependency") {
      this.setIconWithColor("package", new vscode.ThemeColor("charts.blue"));
    } else {
      const priority = parsePriority(labels);
      if (priority) {
        const config = PRIORITY_CONFIG[priority];
        this.setIconWithColor(config.icon, new vscode.ThemeColor(config.color));
      } else {
        this.setIcon("issues");
      }
    }
  }

  /**
   * Build description: branch name with optional size badge appended.
   * Format: "feat/92-pipeline-sidebar [M]"
   */
  private createDescription(): string {
    const labels = this.issueInfo.labels ?? [];
    const size = parseSize(labels);
    return size ? `${this.issueInfo.branch} [${size}]` : this.issueInfo.branch;
  }

  /**
   * Create a tooltip with issue details
   */
  private createTooltip(): vscode.MarkdownString {
    const md = new vscode.MarkdownString();
    md.isTrusted = true;
    md.appendMarkdown(`**Issue #${this.issueInfo.number}**\n\n`);
    md.appendMarkdown(`${this.issueInfo.title}\n\n`);
    md.appendMarkdown(`Branch: \`${this.issueInfo.branch}\`\n\n`);

    if (this.issueInfo.baseBranch) {
      md.appendMarkdown(`Target: \`${this.issueInfo.baseBranch}\`\n\n`);
    }

    const labels = this.issueInfo.labels ?? [];
    const priority = parsePriority(labels);
    const size = parseSize(labels);

    // Issue #2486: Dependabot-specific tooltip section
    if (isDependabotIssue(labels)) {
      const depType = getDependabotType(labels);
      if (depType === "security") {
        md.appendMarkdown(`🛡️ **Security Fix** — Review advisory before merging\n\n`);
      } else {
        md.appendMarkdown(`📦 **Dependency Update**\n\n`);
      }
      md.appendMarkdown(`**Pipeline:** Simplified (validate → merge only)\n\n`);
    }

    if (priority) {
      md.appendMarkdown(`**Priority:** ${formatPriority(priority)}\n\n`);
    }
    if (size) {
      md.appendMarkdown(`**Size:** ${formatSize(size)}\n\n`);
    }

    if (labels.length > 0) {
      const displayLabels = labels
        .filter(
          (l) => !l.startsWith("priority:") && !l.startsWith("size:") && !l.startsWith("status:")
        )
        .map((l) => `\`${l}\``);
      if (displayLabels.length > 0) {
        md.appendMarkdown(`Labels: ${displayLabels.join(", ")}\n\n`);
      }
    }

    if (this.issueInfo.url) {
      md.appendMarkdown(`---\n\n`);
      md.appendMarkdown(`Click to view on GitHub`);
    }

    return md;
  }

  /**
   * Update the issue information
   */
  update(issueInfo: Partial<IssueInfo>): void {
    if (issueInfo.title) {
      this.issueInfo.title = issueInfo.title;
      this.label = `#${this.issueNumber} - ${issueInfo.title}`;
    }
    if (issueInfo.branch) {
      this.issueInfo.branch = issueInfo.branch;
    }
    if (issueInfo.baseBranch !== undefined) {
      this.issueInfo.baseBranch = issueInfo.baseBranch;
    }
    if (issueInfo.labels) {
      this.issueInfo.labels = issueInfo.labels;
    }
    this.applyLabelBadges();
    this.description = this.createDescription();
    this.tooltip = this.createTooltip();
  }

  /**
   * Get the issue info
   */
  getInfo(): IssueInfo {
    return { ...this.issueInfo };
  }
}
