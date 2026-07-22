/**
 * RepositoryTreeItem - Tree item for displaying a repository in the Repositories view
 *
 * Displays repository name, role, path, and GitHub info. Supports expand/collapse
 * for child items (issue counts, pipeline status).
 *
 * @see Issue #329 - Repositories Tree View
 * @see docs/MULTI_REPO_WORKSPACE.md - Multi-Repository Workspace Support
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";
import type { Repository } from "../../models/Repository";

/**
 * Tree item representing a repository in the workspace
 *
 * @example
 * ```typescript
 * const repo = new Repository('frontend', '/path/to/frontend', 'primary');
 * const item = new RepositoryTreeItem(repo, true);
 * ```
 */
export class RepositoryTreeItem extends BaseTreeItem {
  /** The repository this item represents */
  readonly repository: Repository;

  /** Whether this is the currently active repository */
  readonly isActive: boolean;

  /**
   * Whether this repo is currently included in the autonomous scheduler's
   * scan set. Drives the inline checkbox visibility and state. When
   * undefined, no checkbox is rendered (e.g. workspace folder with no
   * GitHub config — we can't map it to autonomous.enabled_repos).
   */
  readonly inAutonomousScan: boolean | undefined;

  /**
   * Whether this repo is configured for sequential autonomous execution
   * (at most 1 concurrent pipeline at a time). Drives the context value
   * used by the toggle command.
   *
   * For backward compatibility, this is `true` when either the legacy
   * `sequential: true` flag is set OR when the resolved per-repo cap
   * (`maxConcurrent`) is exactly 1.
   */
  readonly isSequential: boolean;

  /**
   * Resolved per-repo concurrency cap from `MaxForRepo()` semantics.
   * `undefined` means "no per-repo cap" (defers to global). Issue #2987.
   */
  readonly maxConcurrent: number | undefined;

  /**
   * Current git branch for the working tree backing this row. Issue #3051.
   * `undefined` when the lookup hasn't completed or failed (silent degrade).
   * Detached HEAD is rendered as `(detached @<sha7>)` by the provider.
   */
  readonly currentBranch: string | undefined;

  /**
   * True when this repository was auto-derived from a shared project (N:1 topology)
   * rather than explicitly listed in the workspace manifest.
   */
  readonly isDerivedFromProject: boolean;

  constructor(
    repository: Repository,
    isActive: boolean = false,
    inAutonomousScan?: boolean,
    isSequential: boolean = false,
    maxConcurrent: number | undefined = undefined,
    currentBranch: string | undefined = undefined,
    isDerivedFromProject: boolean = false
  ) {
    // Set label and collapsible state
    super(repository.name, vscode.TreeItemCollapsibleState.Expanded);

    this.repository = repository;
    this.isActive = isActive;
    this.inAutonomousScan = inAutonomousScan;
    // Treat numeric cap of 1 as sequential for back-compat.
    this.isSequential = isSequential || maxConcurrent === 1;
    this.maxConcurrent = maxConcurrent;
    this.currentBranch = currentBranch;
    this.isDerivedFromProject = isDerivedFromProject;

    // Set contextValue for context menu visibility — includes sequential state
    // so the toggle command can target sequential vs concurrent repos.
    // Uses the resolved this.isSequential (true when either the legacy bool
    // is set or maxConcurrent === 1) so the existing toggle/menu wiring still
    // applies when users opt into the numeric cap form.
    if (isActive) {
      this.contextValue = this.isSequential ? "repository-active-sequential" : "repository-active";
    } else {
      this.contextValue = this.isSequential ? "repository-sequential" : "repository";
    }

    // Set icon based on active state and role
    this.setRepositoryIcon();

    // Set description (role and path)
    this.setDescription();

    // Set tooltip with full details
    this.setTooltipText();

    // Inline checkbox controls autonomous.enabled_repos for this repo.
    // Only rendered when the caller supplied an autonomous-scan state —
    // workspace folders that can't be mapped to a GitHub repo (no
    // .nightgauge/config.yaml) don't get a checkbox.
    if (typeof inAutonomousScan === "boolean") {
      this.checkboxState = {
        state: inAutonomousScan
          ? vscode.TreeItemCheckboxState.Checked
          : vscode.TreeItemCheckboxState.Unchecked,
        tooltip: inAutonomousScan
          ? "Uncheck to exclude this repo from autonomous board scans"
          : "Check to include this repo in autonomous board scans",
      };
    }

    // No click command — clicking expands/collapses the tree item.
    // Use the status bar repo switcher or context menu to change active repo.
  }

  /**
   * Set the appropriate icon for this repository
   */
  private setRepositoryIcon(): void {
    if (this.isActive) {
      // Active repository gets a filled icon with accent color
      this.setIconWithColor("repo", new vscode.ThemeColor("charts.blue"));
    } else {
      // Inactive repositories get a subtle icon
      this.setIcon("repo");
    }
  }

  /**
   * Set the description showing the current git branch and the per-repo
   * concurrency cap (Issue #3051). The role and GitHub `owner/repo` were
   * relocated to the tooltip — at-a-glance branch is the high-signal piece.
   *
   *   - branch present → `<branch>` (or `(detached @<sha7>)` when detached)
   *   - cap suffix appended when present
   *
   * Cap suffix shape (Issue #2987 + #3051 unification):
   *   - numeric N≥2 → ` [max: N]`
   *   - sequential / cap == 1 → ` [max: 1]`
   *   - no per-repo cap → no suffix
   */
  private setDescription(): void {
    const parts: string[] = [];

    if (this.currentBranch) {
      parts.push(this.currentBranch);
    }

    const capSuffix = this.formatCapSuffix();
    if (capSuffix) {
      parts.push(capSuffix);
    }

    this.description = parts.join(" • ");
  }

  /**
   * Format the concurrency cap as a short suffix for the description line.
   * Sequential / cap=1 renders as `[max: 1]` for parity with `[max: N]`
   * (Issue #3051) — the legacy `[seq]` literal was inconsistent with the
   * numeric form and forced readers to translate it back.
   */
  private formatCapSuffix(): string {
    if (typeof this.maxConcurrent === "number" && this.maxConcurrent >= 2) {
      return `[max: ${this.maxConcurrent}]`;
    }
    if (this.isSequential) {
      return "[max: 1]";
    }
    return "";
  }

  /**
   * Set the tooltip with full repository details
   */
  private setTooltipText(): void {
    const lines: string[] = [];

    lines.push(`**${this.repository.name}**`);

    if (this.isActive) {
      lines.push("*(Active)*");
    }

    lines.push("");
    lines.push(`Path: ${this.repository.path}`);

    if (this.repository.role) {
      lines.push(`Role: ${this.repository.role}`);
    }

    const github = this.repository.github;
    if (github) {
      lines.push(`GitHub: ${github.owner}/${github.repo}`);
      if (github.project_number) {
        const projectLine = `Project: #${github.project_number}`;
        lines.push(this.isDerivedFromProject ? `${projectLine} *(via project link)*` : projectLine);
      }
    } else if (this.isDerivedFromProject && this.repository.effectiveProjectNumber) {
      lines.push(`Project: #${this.repository.effectiveProjectNumber} *(via project link)*`);
    }

    if (typeof this.maxConcurrent === "number" && this.maxConcurrent >= 2) {
      lines.push("");
      lines.push(`⚙️ Per-repo cap — at most ${this.maxConcurrent} pipelines at a time`);
    } else if (this.isSequential) {
      lines.push("");
      lines.push("⚙️ Sequential mode — at most 1 pipeline at a time");
    }

    this.tooltip = new vscode.MarkdownString(lines.join("\n"));
  }

  /**
   * Get the repository name
   */
  getName(): string {
    return this.repository.name;
  }

  /**
   * Get the repository path
   */
  getPath(): string {
    return this.repository.path;
  }

  /**
   * Get the repository role
   */
  getRole(): string | undefined {
    return this.repository.role;
  }

  /**
   * Check if this repository is currently active
   */
  getIsActive(): boolean {
    return this.isActive;
  }
}
