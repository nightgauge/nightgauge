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
import type { AutonomousRepoPause } from "../../services/IpcClientBase";

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
  isActive: boolean;

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
  isSequential: boolean;

  /**
   * Resolved per-repo concurrency cap from `MaxForRepo()` semantics.
   * `undefined` means "no per-repo cap" (defers to global). Issue #2987.
   */
  maxConcurrent: number | undefined;

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

  /**
   * This repository's autonomous halt record (#1148), when one is in force.
   *
   * A repo-scoped halt leaves the FLEET status reading "running" — every
   * other repository keeps dispatching — so nothing in the status bar or the
   * global Resume button says that this row is stopped. Without a per-row
   * marker the only symptom is an absence: a Ready issue that never gets
   * picked up, indistinguishable from "unchecked", "nothing on the board" or
   * "the scheduler is busy elsewhere". Hence the warning badge.
   *
   * Mutable so a live `autonomous.repoHaltChanged` event can repaint the
   * cached row in place rather than forcing a full tree rebuild — see
   * {@link applyHaltState}.
   */
  halt: AutonomousRepoPause | undefined;

  constructor(
    repository: Repository,
    isActive: boolean = false,
    inAutonomousScan?: boolean,
    isSequential: boolean = false,
    maxConcurrent: number | undefined = undefined,
    currentBranch: string | undefined = undefined,
    isDerivedFromProject: boolean = false,
    halt: AutonomousRepoPause | undefined = undefined
  ) {
    // Set label and collapsible state.
    //
    // A repo excluded from the autonomous scan set renders COLLAPSED. This is
    // not cosmetic: VSCode only asks for an expanded row's children, and each
    // of those asks is a `board.counts` GraphQL call once the service cache
    // lapses. Rendering every row expanded meant an unchecked repo kept
    // spending GitHub quota on a schedule — the opposite of what unchecking
    // it says. Expanding one by hand still loads its counts on demand.
    super(
      repository.name,
      inAutonomousScan === false
        ? vscode.TreeItemCollapsibleState.Collapsed
        : vscode.TreeItemCollapsibleState.Expanded
    );

    // Stable identity (#1277). VS Code keys expansion state on `id` when one
    // is set and on `parentHandle/index:label` when it is not. Every refresh
    // rebuilds this object, so without an id the row and everything under it
    // collapses whenever anything above or in the label moves. Repository
    // names are unique within the provider's cache, so the name alone is the
    // identity; nothing volatile (branch, cap, halt) may appear here.
    this.id = `repo:${repository.name}`;

    this.repository = repository;
    this.isActive = isActive;
    this.inAutonomousScan = inAutonomousScan;
    // Treat numeric cap of 1 as sequential for back-compat.
    this.isSequential = isSequential || maxConcurrent === 1;
    this.maxConcurrent = maxConcurrent;
    this.currentBranch = currentBranch;
    this.isDerivedFromProject = isDerivedFromProject;
    this.halt = halt;

    // Set contextValue for context menu visibility.
    this.setContextValue();

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
   * Apply (or clear) this repository's autonomous halt and repaint every
   * derived visual: icon, description, tooltip and contextValue.
   *
   * Separate from the constructor because a halt is raised and released while
   * the row already exists. The provider caches `RepositoryTreeItem`s and
   * refreshes them by firing `onDidChangeTreeData` with the SAME object, so a
   * halt that only changed provider-side state would repaint nothing.
   */
  applyHaltState(halt: AutonomousRepoPause | undefined): void {
    this.halt = halt;
    this.setContextValue();
    this.setRepositoryIcon();
    this.setDescription();
    this.setTooltipText();
  }

  /**
   * Repaint this row as active / inactive in place (#1277).
   *
   * The active repository follows the active editor, which changes far more
   * often than anything else in this tree. Rebuilding the whole tree for an
   * icon swap threw away every expansion below every row; repainting the two
   * rows whose state changed costs nothing and touches nothing else.
   */
  applyActiveState(isActive: boolean): void {
    if (this.isActive === isActive) return;
    this.isActive = isActive;
    this.setContextValue();
    this.setRepositoryIcon();
    this.setTooltipText();
  }

  /**
   * Repaint this row's per-repo concurrency cap in place (#1277) — the same
   * discipline as {@link applyHaltState}, for the same reason: the provider
   * refreshes cached rows by firing with the SAME object.
   */
  applyConcurrency(isSequential: boolean, maxConcurrent: number | undefined): void {
    this.isSequential = isSequential || maxConcurrent === 1;
    this.maxConcurrent = maxConcurrent;
    this.setContextValue();
    this.setDescription();
    this.setTooltipText();
  }

  /** True when autonomous dispatch is halted for this repository (#1148). */
  get isHalted(): boolean {
    return this.halt !== undefined;
  }

  /**
   * The fully-qualified `owner/repo` key the halt is recorded under, or
   * `undefined` when this repo is not halted. The inline Resume action reads
   * this off the tree item so it resumes exactly the repo that was clicked,
   * without having to re-derive the key from config.
   */
  get haltedRepoKey(): string | undefined {
    return this.halt?.repo;
  }

  /**
   * Set contextValue for context-menu / inline-action visibility.
   *
   * Includes sequential state so the toggle command can target sequential vs
   * concurrent repos, and a `-halted` suffix so the inline "Resume Repository"
   * action appears only on rows that actually have something to resume.
   * Uses the resolved this.isSequential (true when either the legacy bool is
   * set or maxConcurrent === 1) so the existing toggle/menu wiring still
   * applies when users opt into the numeric cap form. Every menu contribution
   * matches on the `repository` PREFIX, so appending suffixes is safe.
   */
  private setContextValue(): void {
    const base = this.isActive
      ? this.isSequential
        ? "repository-active-sequential"
        : "repository-active"
      : this.isSequential
        ? "repository-sequential"
        : "repository";
    this.contextValue = this.isHalted ? `${base}-halted` : base;
  }

  /**
   * Set the appropriate icon for this repository
   */
  private setRepositoryIcon(): void {
    if (this.isHalted) {
      // A halted repo outranks "active" for the icon slot: the fact that this
      // repository has silently stopped dispatching is the thing the operator
      // needs to see first, and which row has focus is already obvious.
      this.setIconWithColor("warning", new vscode.ThemeColor("list.warningForeground"));
    } else if (this.isActive) {
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

    // Halt leads the line, and carries its own glyph: a themed icon colour is
    // the only other signal and it is invisible to anyone reading the tree in
    // a high-contrast theme or a screenshot.
    if (this.isHalted) {
      parts.push("\u26A0 Autonomous halted");
    }

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

    if (this.halt) {
      lines.push("");
      lines.push("\u26A0\ufe0f **Autonomous dispatch is halted for this repository**");
      const cause = this.halt.issue
        ? `Issue #${this.halt.issue} failed at ${this.halt.stage || "an unknown stage"}`
        : this.halt.reason || "unknown";
      lines.push(`Cause: ${cause}`);
      if (this.halt.issue && this.halt.reason) {
        lines.push(`Reason: ${this.halt.reason}`);
      }
      if (this.halt.triggeredBy) {
        lines.push(`Raised by: ${this.halt.triggeredBy}`);
      }
      if (this.halt.pausedAt) {
        lines.push(`Halted at: ${this.halt.pausedAt}`);
      }
      lines.push("");
      lines.push(
        "Other repositories keep dispatching. Triage the failure, then use the inline \u25B6 Resume action on this row."
      );
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
