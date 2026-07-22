/**
 * ReadyIssueTreeItem - Tree item representing a ready issue from project board
 *
 * Displays issue number, title, priority badge, and size in the tree view.
 * Provides click-to-view and context menu actions.
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";
import { DependencySectionTreeItem } from "./DependencySectionTreeItem";
import type { ReadyIssue, Priority, Size } from "../../services/ProjectBoardService";
import { isBlocked, getBlockerTitles } from "../../utils/dependencyUtils";
import {
  isDependabotIssue,
  getDependabotType,
  getDependencyPackageInfo,
} from "../../utils/dependabotUtils";

/**
 * Priority icon display configuration
 */
interface PriorityDisplayConfig {
  icon: string;
  color: string;
}

/**
 * Priority to icon/color mapping
 * P0 = Critical (red), P1 = High (orange/warning), P2 = Medium/Low (default themed)
 */
const PRIORITY_CONFIG: Record<NonNullable<Priority>, PriorityDisplayConfig> = {
  P0: { icon: "circle-filled", color: "problemsErrorIcon.foreground" },
  P1: { icon: "circle-filled", color: "problemsWarningIcon.foreground" },
  P2: { icon: "circle-filled", color: "charts.blue" },
  P3: { icon: "circle-filled", color: "charts.green" },
};

/**
 * Options for ReadyIssueTreeItem
 */
export interface ReadyIssueTreeItemOptions {
  showDependencies?: boolean;
  /** Enable checkbox for batch selection (Issue #125) */
  enableCheckbox?: boolean;
  /** Initial checked state */
  checked?: boolean;
  /** Filesystem path to the repo root (for cross-repo commands) */
  repoPath?: string;
  /** Human-readable repo name for display */
  repoName?: string;
  /** Repository owner (e.g. 'nightgauge') — for cross-repo drag-and-drop */
  repoOwner?: string;
}

/**
 * ReadyIssueTreeItem - Represents a ready issue in the tree view
 *
 * Supports checkbox selection for batch pipeline execution (Issue #125).
 *
 * @example
 * ```typescript
 * const item = new ReadyIssueTreeItem({
 *   number: 110,
 *   title: 'Add Ready items list view',
 *   labels: ['type:feature', 'priority:medium'],
 *   priority: 'P2',
 *   size: 'M',
 *   url: 'https://github.com/nightgauge/nightgauge/issues/110'
 * }, { enableCheckbox: true });
 * ```
 */
export class ReadyIssueTreeItem extends BaseTreeItem {
  readonly issueNumber: number;
  readonly issueUrl: string;
  /** Filesystem path to the repo root (for cross-repo commands, Issue #2188) */
  readonly repoPath: string | undefined;
  /** Human-readable repo name (for cross-repo display, Issue #2188) */
  readonly repoName: string | undefined;
  /** Repository owner (e.g. 'nightgauge') — for cross-repo drag-and-drop */
  readonly repoOwner: string | undefined;
  private issue: ReadyIssue;
  private _checked: boolean = false;
  private _checkboxEnabled: boolean = false;

  constructor(issue: ReadyIssue, options?: ReadyIssueTreeItemOptions) {
    const showDependencies = options?.showDependencies ?? true;
    const enableCheckbox = options?.enableCheckbox ?? false;
    const initialChecked = options?.checked ?? false;

    // Make collapsible if issue has dependencies (blocked by or blocks others)
    const hasBlockingDeps = showDependencies && issue.blockedBy && issue.blockedBy.length > 0;
    const hasBlockedDeps = showDependencies && issue.blocks && issue.blocks.length > 0;
    const hasDependencies = hasBlockingDeps || hasBlockedDeps;

    const collapsibleState = hasDependencies
      ? vscode.TreeItemCollapsibleState.Collapsed
      : vscode.TreeItemCollapsibleState.None;

    // Issue #443: Add "(blocked)" suffix to label for blocked issues
    const issueIsBlocked = isBlocked(issue);
    const labelSuffix = issueIsBlocked ? " (blocked)" : "";
    super(`#${issue.number} - ${issue.title}${labelSuffix}`, collapsibleState);

    this.issueNumber = issue.number;
    this.issueUrl = issue.url;
    this.issue = issue;
    this.repoPath = options?.repoPath;
    this.repoName = options?.repoName;
    this.repoOwner = options?.repoOwner;
    this._checkboxEnabled = enableCheckbox;
    this._checked = initialChecked;

    // Set checkbox state if enabled (Issue #125 - Batch Pipeline Execution)
    if (enableCheckbox) {
      this.checkboxState = {
        state: initialChecked
          ? vscode.TreeItemCheckboxState.Checked
          : vscode.TreeItemCheckboxState.Unchecked,
        tooltip: initialChecked
          ? "Click to deselect for batch processing"
          : "Click to select for batch processing",
      };
    }

    // Set icon: Dependabot issues get shield (security) or package (dependency) icons.
    // Blocked issues get a lock icon. Otherwise use priority or generic icon.
    // Issue #2486: Dependabot icon treatment
    // Issue #443: Blocked issues get a lock icon with error color
    const depType = getDependabotType(issue.labels);
    if (depType === "security") {
      this.setIconWithColor("shield", new vscode.ThemeColor("testing.iconFailed"));
    } else if (depType === "dependency") {
      this.setIconWithColor("package", new vscode.ThemeColor("charts.blue"));
    } else if (issueIsBlocked) {
      this.setIconWithColor("lock", new vscode.ThemeColor("problemsErrorIcon.foreground"));
    } else if (hasBlockingDeps) {
      // Has blocking deps but they're all closed - show info icon
      this.setIconWithColor("circle-filled", new vscode.ThemeColor("charts.green"));
    } else if (issue.priority) {
      const config = PRIORITY_CONFIG[issue.priority];
      this.setIconWithColor(config.icon, new vscode.ThemeColor(config.color));
    } else {
      this.setIcon("issues");
    }

    this.contextValue = "readyIssue";
    this.description = this.createDescription(showDependencies);
    this.tooltip = this.createTooltip(showDependencies);

    // Add ARIA-friendly accessible description for screen readers (Issue #304)
    this.accessibilityInformation = {
      label: this.createAccessibilityLabel(),
      role: "treeitem",
    };

    // Add dependency section children if present
    if (showDependencies) {
      if (issue.blockedBy && issue.blockedBy.length > 0) {
        this.addChild(new DependencySectionTreeItem("blockedBy", issue.blockedBy));
      }
      if (issue.blocks && issue.blocks.length > 0) {
        this.addChild(new DependencySectionTreeItem("blocks", issue.blocks));
      }
    }

    // Click to view on GitHub (Issue #297)
    this.command = {
      command: "nightgauge.viewIssueOnGitHub",
      title: "View on GitHub",
      arguments: [this],
    };
  }

  /**
   * Create the description showing dependency count and size badge
   * Format: "🔒N blockers [SIZE]" for blocked issues, "[SIZE]" for unblocked
   * Priority is conveyed via the icon color, so description focuses on dependencies and size
   *
   * Issue #443: Enhanced description for blocked issues to show blocker count
   */
  private createDescription(showDependencies: boolean = true): string {
    const parts: string[] = [];

    // Add blocked count if issue has open blockers
    if (showDependencies && isBlocked(this.issue)) {
      const blockerTitles = getBlockerTitles(this.issue);
      const count = blockerTitles.length;
      parts.push(`🔒${count} blocker${count === 1 ? "" : "s"}`);
    }

    // Issue #2486: Show package version delta for Dependabot issues
    if (isDependabotIssue(this.issue.labels)) {
      const pkgInfo = getDependencyPackageInfo(this.issue.title);
      if (pkgInfo) {
        parts.push(`${pkgInfo.name}: ${pkgInfo.from} → ${pkgInfo.to}`);
      }
    }

    // Add size badge
    if (this.issue.size) {
      parts.push(`[${this.issue.size}]`);
    }

    return parts.join(" ");
  }

  /**
   * Create a rich tooltip with issue details
   */
  private createTooltip(showDependencies: boolean = true): vscode.MarkdownString {
    const md = new vscode.MarkdownString();
    md.isTrusted = true;

    md.appendMarkdown(`**#${this.issue.number}** - ${this.issue.title}\n\n`);

    // Issue #2486: Dependabot-specific tooltip section
    const tooltipDepType = getDependabotType(this.issue.labels);
    if (tooltipDepType) {
      const typeLabel = tooltipDepType === "security" ? "Security Fix" : "Dependency Update";
      md.appendMarkdown(`**Type:** ${typeLabel}\n\n`);

      const pkgInfo = getDependencyPackageInfo(this.issue.title);
      if (pkgInfo) {
        md.appendMarkdown(
          `**Package:** \`${pkgInfo.name}\` — \`${pkgInfo.from}\` → \`${pkgInfo.to}\`\n\n`
        );
      }

      if (tooltipDepType === "security") {
        md.appendMarkdown(`🛡️ **Security Fix** — Review advisory before merging\n\n`);
      }
    }

    if (this.issue.status) {
      md.appendMarkdown(`**Status:** ${this.issue.status}\n\n`);
    }

    if (this.issue.priority) {
      md.appendMarkdown(`**Priority:** ${this.formatPriority(this.issue.priority)}\n\n`);
    }

    if (this.issue.size) {
      md.appendMarkdown(`**Size:** ${this.formatSize(this.issue.size)}\n\n`);
    }

    // Add "Blocked By" section if issue has blockers
    if (showDependencies && this.issue.blockedBy && this.issue.blockedBy.length > 0) {
      md.appendMarkdown(`**🔒 Blocked By:**\n\n`);
      for (const blockingIssue of this.issue.blockedBy) {
        const stateIcon = blockingIssue.state === "OPEN" ? "🔴" : "✅";
        md.appendMarkdown(`- ${stateIcon} #${blockingIssue.number}: ${blockingIssue.title}\n`);
      }
      md.appendMarkdown(`\n`);
    }

    // Add "Blocks" section if this issue blocks others
    if (showDependencies && this.issue.blocks && this.issue.blocks.length > 0) {
      md.appendMarkdown(`**🔗 Blocks:**\n\n`);
      for (const blockedIssue of this.issue.blocks) {
        const stateIcon = blockedIssue.state === "OPEN" ? "🔴" : "✅";
        md.appendMarkdown(`- ${stateIcon} #${blockedIssue.number}: ${blockedIssue.title}\n`);
      }
      md.appendMarkdown(`\n`);
    }

    if (this.issue.labels.length > 0) {
      const displayLabels = this.issue.labels
        .filter(
          (l) => !l.startsWith("priority:") && !l.startsWith("size:") && !l.startsWith("status:")
        )
        .map((l) => `\`${l}\``);
      if (displayLabels.length > 0) {
        md.appendMarkdown(`**Labels:** ${displayLabels.join(", ")}\n\n`);
      }
    }

    md.appendMarkdown(`---\n\n`);
    md.appendMarkdown(`Click to view on GitHub, drag to add to pipeline\n\n`);
    md.appendMarkdown(`Right-click for more options`);

    return md;
  }

  /**
   * Format priority for display
   */
  private formatPriority(priority: Priority): string {
    switch (priority) {
      case "P0":
        return "P0 (Critical)";
      case "P1":
        return "P1 (High)";
      case "P2":
        return "P2 (Medium/Low)";
      default:
        return "Not set";
    }
  }

  /**
   * Format size for display
   */
  private formatSize(size: Size): string {
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
      default:
        return "Not set";
    }
  }

  /**
   * Create accessibility label for screen readers (Issue #304)
   *
   * Format: "Issue #304: Add accessibility alternatives. Priority P1, Size M. Press Ctrl+Shift+A to add to pipeline."
   */
  private createAccessibilityLabel(): string {
    const parts: string[] = [`Issue #${this.issue.number}: ${this.issue.title}.`];

    if (this.issue.priority) {
      parts.push(`Priority ${this.issue.priority}.`);
    }

    if (this.issue.size) {
      parts.push(`Size ${this.issue.size}.`);
    }

    // Add blocked status if applicable
    if (this.issue.blockedBy && this.issue.blockedBy.length > 0) {
      parts.push(
        `Blocked by ${this.issue.blockedBy.length} issue${this.issue.blockedBy.length === 1 ? "" : "s"}.`
      );
    }

    // Add keyboard hint
    parts.push("Press Ctrl+Shift+A to add to pipeline.");

    return parts.join(" ");
  }

  /**
   * Get the underlying issue data
   */
  getIssue(): ReadyIssue {
    return { ...this.issue };
  }

  /**
   * Check if checkbox selection is enabled
   */
  isCheckboxEnabled(): boolean {
    return this._checkboxEnabled;
  }

  /**
   * Get the current checked state
   */
  isChecked(): boolean {
    return this._checked;
  }

  /**
   * Set the checked state
   */
  setChecked(checked: boolean): void {
    this._checked = checked;
    if (this._checkboxEnabled) {
      this.checkboxState = {
        state: checked
          ? vscode.TreeItemCheckboxState.Checked
          : vscode.TreeItemCheckboxState.Unchecked,
        tooltip: checked
          ? "Click to deselect for batch processing"
          : "Click to select for batch processing",
      };
    }
  }

  /**
   * Toggle the checked state
   */
  toggleChecked(): boolean {
    this.setChecked(!this._checked);
    return this._checked;
  }
}
