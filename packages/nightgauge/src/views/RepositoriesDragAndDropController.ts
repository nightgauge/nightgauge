/**
 * RepositoriesDragAndDropController - Drag and drop controller for the Repositories tree view
 *
 * Enables cross-status drag-and-drop within the same repository. Extends
 * IssueDragAndDropController with dynamic source/target resolution based on
 * tree item context rather than a static tab status.
 *
 * - Source status: read from the dragged ReadyIssueTreeItem's parent IssueSummaryTreeItem
 * - Target status: resolved from the hovered IssueSummaryTreeItem
 * - Repo boundary: cross-repo drops are silently rejected
 *
 * @see Issue #2189 - Port sorting, filtering, and drag-and-drop to nested repository views
 */

import { IssueDragAndDropController, type SerializedIssue } from "./IssueDragAndDropController";
import { IssueSummaryTreeItem } from "./items/IssueSummaryTreeItem";
import type { BaseTreeItem } from "./items/BaseTreeItem";
import type { RepositoriesTreeProvider } from "./RepositoriesTreeProvider";
import type { ProjectStatusValue } from "../utils/projectFieldWriter";

/**
 * Map from IssueSummaryTreeItem statusType to ProjectStatusValue used by projectFieldWriter.
 */
const STATUS_TYPE_TO_PROJECT_STATUS: Record<string, ProjectStatusValue> = {
  ready: "Ready",
  inProgress: "In progress",
  backlog: "Backlog",
  done: "Done",
};

/**
 * RepositoriesDragAndDropController - Drag and drop for Repositories tree view.
 *
 * Unlike ColumnDragAndDropController (which has a static tab status), this
 * controller infers source and target status dynamically from the tree items
 * involved in the drag operation.
 *
 * @example
 * ```typescript
 * const controller = new RepositoriesDragAndDropController(repositoriesProvider);
 * const treeView = vscode.window.createTreeView('nightgauge.repositoriesView', {
 *   treeDataProvider: provider,
 *   dragAndDropController: controller,
 * });
 * ```
 */
export class RepositoriesDragAndDropController extends IssueDragAndDropController {
  private repositoriesProvider: RepositoriesTreeProvider;

  /**
   * The source IssueSummaryTreeItem captured during handleDrag for use in
   * resolveSourceTabStatus(). Set by calling setSourceContext() before handleDrag
   * runs serializeIssues (which reads resolveSourceTabStatus).
   *
   * Note: VSCode does not pass the full parent context during drag, so we rely on
   * the ReadyIssueTreeItem having repoName set and use context clues from the
   * provider's issueSummaryCache to infer source status.
   */
  private _lastSourceStatusType: string | undefined;

  constructor(repositoriesProvider: RepositoriesTreeProvider) {
    super();
    this.repositoriesProvider = repositoriesProvider;
  }

  /**
   * Called by the drag source to set source status context before serialization.
   */
  setSourceStatusType(statusType: string): void {
    this._lastSourceStatusType = statusType;
  }

  /**
   * Resolve source tab status from the last known source context.
   * Maps IssueSummaryTreeItem statusType to the ProjectStatusValue string.
   */
  protected override resolveSourceTabStatus(): string | undefined {
    if (!this._lastSourceStatusType) return undefined;
    return STATUS_TYPE_TO_PROJECT_STATUS[this._lastSourceStatusType] ?? this._lastSourceStatusType;
  }

  /**
   * Resolve target column status from the drop target.
   * Returns null unless the target is an IssueSummaryTreeItem with a known status.
   */
  protected override resolveTargetColumnStatus(
    target: BaseTreeItem | undefined
  ): ProjectStatusValue | null {
    if (!(target instanceof IssueSummaryTreeItem)) {
      return null;
    }
    return (STATUS_TYPE_TO_PROJECT_STATUS[target.statusType] as ProjectStatusValue) ?? null;
  }

  /**
   * Resolve the `gh` cwd from the dragged issue's own repository so cross-status
   * moves run against the correct repo in the multi-repo Repositories view.
   * Falls back to the configured workspace root when the repo can't be resolved.
   */
  protected override resolveDropWorkspaceRoot(issues: SerializedIssue[]): string | null {
    const repoName = issues[0]?.repoName;
    const repoPath = repoName ? this.repositoriesProvider.getRepositoryPath(repoName) : undefined;
    return repoPath ?? this.repositoriesProvider.getRepositoryPath() ?? null;
  }

  /**
   * After a cross-status drop, refresh only the repositories that changed
   * rather than a single (non-existent) project board provider.
   */
  protected override refreshAfterCrossColumnDrop(issues: SerializedIssue[]): void {
    const repoNames = new Set(
      issues.map((i) => i.repoName).filter((name): name is string => Boolean(name))
    );
    if (repoNames.size === 0) {
      this.repositoriesProvider.refreshAll();
      return;
    }
    for (const name of repoNames) {
      this.refreshRepoAfterDrop(name);
    }
  }

  /**
   * The Repositories view manages status, not pipeline pickup. A drop that
   * doesn't resolve to a status column (e.g. on the empty tree root) must be a
   * no-op — never silently start a pipeline like the base controller does.
   */
  protected override validateDropTarget(_target: BaseTreeItem | undefined): boolean {
    return false;
  }

  /**
   * After a successful cross-status drop, refresh the affected repository.
   * The base class handleCrossColumnDrop updates the GitHub project status;
   * this override additionally fires a targeted tree refresh for the repo.
   *
   * We accomplish this by overriding handleDrop's post-drop refresh:
   * the base class refreshes projectBoardProvider; we also refresh the
   * repositories provider for the affected repo.
   */
  setRepoRefreshCallback(callback: (repoName: string) => void): void {
    this._repoRefreshCallback = callback;
  }

  private _repoRefreshCallback: ((repoName: string) => void) | null = null;

  /**
   * After a cross-column drop completes, refresh the affected repository in
   * the Repositories tree. Called by the drop handler in the repositories view.
   */
  refreshRepoAfterDrop(repoName: string): void {
    if (this._repoRefreshCallback) {
      this._repoRefreshCallback(repoName);
    } else {
      this.repositoriesProvider.refreshRepository(repoName);
    }
  }
}
