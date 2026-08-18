/**
 * EpicGroupTreeItem - Tree item representing an epic group in the project board
 *
 * Displays epics as collapsible groups containing their sub-issues,
 * with progress indicators showing completion status.
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";
import { ReadyIssueTreeItem } from "./ReadyIssueTreeItem";
import type { ReadyIssue, BlockingIssue } from "../../services/ProjectBoardService";
import { isBlocked, getBlockerTitles } from "../../utils/dependencyUtils";

/**
 * Epic information for display
 */
export interface EpicInfo {
  number: number;
  title: string;
  url: string;
  /**
   * Open/closed issues blocking the epic itself (Issue #656, Gap 3).
   * Distinct from the epic's sub-issues — this is the epic's *own*
   * blockedBy, threaded through from the underlying ReadyIssue so the
   * group header can render the same blocked state as a blocked
   * ReadyIssueTreeItem leaf.
   */
  blockedBy?: BlockingIssue[];
  /**
   * The epic issue's own GitHub labels (Issue #656, AC 1 remainder —
   * distinguishing "mislabelled" from "not yet decomposed" for an empty
   * epic requires reading `needs-decomposition` off the epic itself).
   *
   * `undefined` means UNKNOWN, not "no labels": some `EpicInfo` records
   * (e.g. the epicRef/epicTitle fallback in
   * `ProjectBoardService.getEpicMetadataFromCache`) are built without
   * ever seeing the epic's own issue object, so they cannot report its
   * labels either way. A defined array — even `[]` — means the epic's
   * own issue WAS read. Callers must not collapse "unknown" and "known,
   * label absent" into the same case: only a defined array that
   * actually contains `needs-decomposition` may assert the confirmed
   * "awaiting decomposition" state; unknown and known-absent both fall
   * back to the honest, cause-agnostic "(empty)" rendering.
   */
  labels?: string[];
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
 * Whether an empty epic has been confirmed by a human as genuinely
 * epic-shaped and simply not decomposed yet, vs. still ambiguous
 * (Issue #656, AC 1 remainder).
 *
 * Requires BOTH zero sub-issues AND a confirmed `needs-decomposition`
 * read off the epic's own labels — `epic.labels` being `undefined`
 * (unknown, see `EpicInfo.labels`) or a defined array without the label
 * both return `false` here, on purpose: absence of the label is not
 * evidence of anything, and this function must never let the caller
 * treat "we don't know" the same as "confirmed awaiting decomposition".
 */
function epicAwaitingDecomposition(epic: EpicInfo | null, subIssueCount: number): boolean {
  return (
    epic !== null && subIssueCount === 0 && epic.labels?.includes("needs-decomposition") === true
  );
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

    // Issue #656: an epic can be blocked (Gap 3) and/or have zero sub-issues
    // (Gap 1) — both need to be visually distinguished from a normal epic,
    // and from each other, so the label/icon carry both independently. An
    // empty epic further splits into "confirmed awaiting decomposition"
    // (needs-decomposition label present) vs. the honest, cause-agnostic
    // "empty" state (AC 1 remainder) — see epicAwaitingDecomposition().
    const epicIsBlocked = epic ? isBlocked(epic) : false;
    const epicIsEmpty = epic !== null && issues.length === 0;
    const epicNeedsDecomposition = epicAwaitingDecomposition(epic, issues.length);

    const labelSuffixes: string[] = [];
    if (epicIsBlocked) labelSuffixes.push("blocked");
    if (epicNeedsDecomposition) {
      labelSuffixes.push("awaiting decomposition");
    } else if (epicIsEmpty) {
      labelSuffixes.push("empty");
    }
    const labelSuffix = labelSuffixes.length > 0 ? ` (${labelSuffixes.join(", ")})` : "";

    const label = epic ? `Epic #${epic.number}: ${epic.title}${labelSuffix}` : "No Epic";
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

    // Set description with progress. Issue #656 (Gap 3): prefix with the
    // open-blocker count when the epic itself is blocked, mirroring
    // ReadyIssueTreeItem's "🔒N blockers" convention.
    const descriptionParts: string[] = [];
    if (epicIsBlocked && epic) {
      const blockerCount = getBlockerTitles(epic).length;
      descriptionParts.push(`🔒${blockerCount} blocker${blockerCount === 1 ? "" : "s"}`);
    }
    descriptionParts.push(`(${this.completedCount}/${this.totalCount} complete)`);
    this.description = descriptionParts.join(" ");

    // Set icon. Issue #656: blocked takes precedence over both empty
    // states, which take precedence over the normal epic/standalone
    // icons — a blocked epic needs the same lock treatment as a blocked
    // ReadyIssueTreeItem leaf (Gap 3). Of the two empty states, a
    // confirmed "awaiting decomposition" (needs-decomposition label) gets
    // a distinct, calmer icon from the ambiguous "empty" warning (AC 1
    // remainder) — it is a known, expected state, not one needing triage.
    if (epicIsBlocked) {
      this.setIconWithColor("lock", new vscode.ThemeColor("problemsErrorIcon.foreground"));
    } else if (epicNeedsDecomposition) {
      this.setIconWithColor("checklist", new vscode.ThemeColor("charts.blue"));
    } else if (epicIsEmpty) {
      this.setIconWithColor("warning", new vscode.ThemeColor("problemsWarningIcon.foreground"));
    } else if (epic) {
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

    // Issue #656 (Gap 3): surface the epic's own blocked state, mirroring
    // ReadyIssueTreeItem's "Blocked By" tooltip section so blocked epics
    // and blocked sub-issues read the same way.
    if (this.epic && isBlocked(this.epic) && this.epic.blockedBy) {
      md.appendMarkdown(`**🔒 Blocked By:**\n\n`);
      for (const blocker of this.epic.blockedBy) {
        const stateIcon = blocker.state === "OPEN" ? "🔴" : "✅";
        md.appendMarkdown(`- ${stateIcon} #${blocker.number}: ${blocker.title}\n`);
      }
      md.appendMarkdown(`\n`);
    }

    if (issues.length === 0 && this.epic && epicAwaitingDecomposition(this.epic, issues.length)) {
      // Issue #656 (AC 1 remainder): the maintainer has confirmed via the
      // needs-decomposition label that this is genuinely epic-shaped and
      // simply hasn't been broken down yet — a different operator action
      // from the ambiguous case below, so say so plainly rather than
      // repeating the "mislabelled or unpopulated" hedge.
      md.appendMarkdown(
        `_No sub-issues linked. \`needs-decomposition\` confirms this is genuinely ` +
          `epic-shaped and simply not broken down yet — decompose it into sub-issues._\n`
      );
    } else if (issues.length === 0 && this.epic) {
      // Issue #656 (Gap 1): an empty epic without a confirmed
      // needs-decomposition read is always one of two states — mislabelled
      // (rescoped out of epic-shaped work, `type:epic` never removed) or
      // unpopulated (never decomposed). Absence of the label is not
      // evidence of either — it only means no one has made the call — so
      // the treeview still has no reliable signal to tell them apart here,
      // and says that plainly instead of guessing.
      md.appendMarkdown(
        `_No sub-issues linked. This is either **mislabelled** (rescoped away from ` +
          `epic-shaped work, but \`type:epic\` was never removed — remove the label) or ` +
          `**unpopulated** (created as an epic, never decomposed — link sub-issues or ` +
          `demote it). Check the issue itself to tell which applies._\n`
      );
    } else if (issues.length === 0) {
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
        // Issue #656 (Gap 3 / AC 1 remainder): thread the epic's own
        // blockedBy and labels through so a blocked epic renders its
        // blocked state, and an empty-but-confirmed epic renders
        // "awaiting decomposition", even when this fallback (epic present
        // in the current batch but missing from the pre-built
        // epicMetadata map) is the one that fires. `issue` here IS the
        // epic's own issue object, so — unlike the epicRef/epicTitle
        // fallback in ProjectBoardService.getEpicMetadataFromCache —
        // labels are genuinely known, not "unknown".
        epicMetadata.set(issue.number, {
          number: issue.number,
          title: issue.title,
          url: issue.url,
          blockedBy: issue.blockedBy,
          labels: issue.labels,
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
      // `labels` intentionally omitted (stays `undefined`, i.e. unknown —
      // see EpicInfo.labels): no epic issue has been read yet at all here.
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
