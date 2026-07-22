/**
 * IssueDragAndDropController - Drag and drop controller for issue tree items
 *
 * Enables dragging issues from ProjectBoardTreeProvider to PipelineTreeProvider
 * to add them to the pipeline. Supports both single-item and multi-item drag operations.
 *
 * @see docs/ARCHITECTURE.md - Deterministic vs Probabilistic Architecture
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./items/BaseTreeItem";
import { ReadyIssueTreeItem } from "./items/ReadyIssueTreeItem";
import { EpicGroupTreeItem } from "./items/EpicGroupTreeItem";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { IssueQueueService } from "../services/IssueQueueService";
import type { ConcurrentPipelineManager } from "../services/ConcurrentPipelineManager";
import type { ProjectBoardTreeProvider } from "./ProjectBoardTreeProvider";
import type { ProjectBoardService } from "../services/ProjectBoardService";
import { getWarningSettings } from "../config/warningSettings";
import {
  showStatusWarningDialog,
  openUrl,
  type IssueWarningData,
  type IssueStatus,
} from "../utils/dialogs";
import { getPRForIssue } from "../utils/prDetection";
import type { BlockingIssue } from "../services/ProjectBoardService";
import { filterEligibleSubIssues, summarizeSkipped } from "../services/EpicQueueFilter";
import { getEpicQueueFilterConfig } from "../utils/incrediConfig";
import { updateProjectItemStatus, type ProjectStatusValue } from "../utils/projectFieldWriter";
import { Logger } from "../utils/logger";
import { IpcClient } from "../services/IpcClient";

/**
 * MIME type for issue drag operations
 * Format: application/vnd.code.tree.nightgauge-issue
 */
const ISSUE_MIME_TYPE = "application/vnd.code.tree.nightgauge-issue";

/**
 * Serialized issue data for drag operations
 */
export interface SerializedIssue {
  issueNumber: number;
  title: string;
  labels: string[];
  url: string;
  blockedBy?: BlockingIssue[];
  /** Status of the tab the drag originated from (e.g. 'Backlog', 'Ready') */
  sourceTabStatus?: string;
  /** True when dragging an epic — triggers sub-issue cascade on drop */
  isEpic?: boolean;
  /** Sub-issue numbers for epic cascade (populated when isEpic=true) */
  subIssueNumbers?: number[];
  /** Repository owner (e.g. 'nightgauge') — for cross-repo drag-and-drop */
  repoOwner?: string;
  /** Repository name (e.g. 'nightgauge') — for cross-repo drag-and-drop */
  repoName?: string;
}

/**
 * IssueDragAndDropController - Handles drag and drop for issue tree items
 *
 * Implements VSCode TreeDragAndDropController to enable dragging issues
 * from ProjectBoardTreeProvider (drag source) to PipelineTreeProvider (drop target).
 *
 * @example
 * ```typescript
 * const controller = new IssueDragAndDropController();
 * controller.setProjectBoardProvider(projectBoardProvider);
 * controller.setStateService(pipelineStateService);
 * controller.setQueueService(issueQueueService);
 *
 * // Register with tree view
 * const treeView = vscode.window.createTreeView('nightgauge.projectBoard.ready', {
 *   treeDataProvider: provider,
 *   dragAndDropController: controller,
 * });
 * ```
 */
export class IssueDragAndDropController implements vscode.TreeDragAndDropController<BaseTreeItem> {
  /**
   * MIME types this controller accepts for drops
   */
  dropMimeTypes = [ISSUE_MIME_TYPE, "text/plain"];

  /**
   * MIME types this controller provides for drags
   * text/plain is included because VSCode doesn't forward custom MIME types
   * across different tree views — only standard types propagate.
   */
  dragMimeTypes = [ISSUE_MIME_TYPE, "text/plain"];

  private projectBoardProvider: ProjectBoardTreeProvider | null = null;
  private stateService: PipelineStateService | null = null;
  private queueService: IssueQueueService | null = null;
  private statusBarItem: vscode.StatusBarItem | null = null;
  private context: vscode.ExtensionContext | null = null;
  /** ProjectBoardService for cross-column status updates */
  private boardService: ProjectBoardService | null = null;
  /** Workspace root for GraphQL mutations via projectFieldWriter */
  private workspaceRoot: string | null = null;
  private logger: Logger | null = null;
  /** ConcurrentPipelineManager for checking active pipeline slots */
  private concurrentPipelineManager: ConcurrentPipelineManager | null = null;

  /**
   * Last serialized drag payload, stashed in `handleDrag` and used as a
   * last-resort fallback in `handleDrop` when the DataTransfer arrives empty.
   *
   * Why static: the drag source (project board tabs) and the drop target
   * (pipeline view) are separate controller instances, so a per-instance
   * field on the source is invisible to the target. VSCode does not reliably
   * forward `text/plain` across different tree views — on those drops only an
   * empty custom-MIME entry survives, producing `JSON.parse("")` →
   * "Unexpected end of JSON input". The static stash, written by whichever
   * instance just ran `handleDrag`, lets the target recover the payload.
   *
   * Recency-guarded (see DRAG_STASH_TTL_MS) so a stale stash from an earlier
   * drag can never be injected into an unrelated drop.
   */
  private static lastDragPayload: { json: string; at: number } | null = null;

  /** Max age for the static drag-payload stash to be considered valid. */
  private static readonly DRAG_STASH_TTL_MS = 10_000;

  constructor() {
    // Create status bar item for drag feedback
    this.statusBarItem = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 100);
  }

  /**
   * Set the Logger for debug output
   */
  setLogger(logger: Logger): void {
    this.logger = logger;
  }

  /**
   * Set the ConcurrentPipelineManager for active slot validation
   */
  setConcurrentPipelineManager(manager: ConcurrentPipelineManager): void {
    this.concurrentPipelineManager = manager;
  }

  /**
   * Set the ProjectBoardTreeProvider for accessing multi-select state
   */
  setProjectBoardProvider(provider: ProjectBoardTreeProvider): void {
    this.projectBoardProvider = provider;
  }

  /**
   * Set the PipelineStateService for validation
   */
  setStateService(service: PipelineStateService): void {
    this.stateService = service;
  }

  /**
   * Set the IssueQueueService for validation
   */
  setQueueService(service: IssueQueueService): void {
    this.queueService = service;
  }

  /**
   * Set the ExtensionContext for globalState persistence
   */
  setContext(context: vscode.ExtensionContext): void {
    this.context = context;
  }

  /**
   * Set the ProjectBoardService for cross-column status updates
   */
  setBoardService(service: ProjectBoardService): void {
    this.boardService = service;
  }

  /**
   * Set the workspace root for GraphQL mutations
   */
  setWorkspaceRoot(root: string): void {
    this.workspaceRoot = root;
  }

  /**
   * Handle drag operation - serialize issue data
   *
   * Implements smart hybrid drag:
   * - If multi-select enabled and items checked: Drag all checked items
   * - Otherwise: Drag only the item being dragged
   *
   * @param source - Tree items being dragged (may be single or multiple)
   * @param dataTransfer - Data transfer object to populate
   * @param token - Cancellation token
   */
  async handleDrag(
    source: readonly BaseTreeItem[],
    dataTransfer: vscode.DataTransfer,
    token: vscode.CancellationToken
  ): Promise<void> {
    if (token.isCancellationRequested) {
      return;
    }

    // Separate issue items from epic group items
    const issueItems = source.filter(
      (item): item is ReadyIssueTreeItem => item instanceof ReadyIssueTreeItem
    );
    const epicGroupItems = source.filter(
      (item): item is EpicGroupTreeItem => item instanceof EpicGroupTreeItem
    );

    // If only epic groups were dragged (no individual issues), serialize them directly
    if (epicGroupItems.length > 0 && issueItems.length === 0) {
      const sourceStatus = this.resolveSourceTabStatus();
      const serialized = this.serializeEpicGroups(epicGroupItems, sourceStatus);
      this.stashDragPayload(serialized);
      dataTransfer.set(ISSUE_MIME_TYPE, new vscode.DataTransferItem(serialized));
      dataTransfer.set("text/plain", new vscode.DataTransferItem(serialized));

      if (this.statusBarItem) {
        const count = epicGroupItems.length;
        const msg =
          count === 1
            ? `Drag epic #${epicGroupItems[0].epic?.number ?? "?"} to move all sub-issues`
            : `Drag ${count} epics to move all sub-issues`;
        this.statusBarItem.text = `$(debug-alt) ${msg}`;
        this.statusBarItem.show();
        setTimeout(() => this.statusBarItem?.hide(), 3000);
      }
      return;
    }

    if (issueItems.length === 0) {
      return;
    }

    let issuesToSerialize: ReadyIssueTreeItem[];

    // Smart hybrid: Check if multi-select is enabled and has selections
    if (this.projectBoardProvider && this.projectBoardProvider.isMultiSelectEnabled()) {
      const selectedIssues = this.projectBoardProvider.getSelectedIssues();
      if (selectedIssues.length > 0) {
        // Multi-select mode: Use all checked items
        // Filter issueItems to only those that are selected
        const selectedSet = new Set(selectedIssues);
        issuesToSerialize = issueItems.filter((item) => selectedSet.has(item.issueNumber));

        // If none of the dragged items are selected, fall back to single item
        if (issuesToSerialize.length === 0) {
          issuesToSerialize = [issueItems[0]];
        }
      } else {
        // Multi-select enabled but nothing checked: Drag single item
        issuesToSerialize = [issueItems[0]];
      }
    } else {
      // Multi-select not enabled: Drag single item
      issuesToSerialize = [issueItems[0]];
    }

    // Serialize issues to JSON array, including source tab status
    const sourceStatus = this.resolveSourceTabStatus();
    const serialized = this.serializeIssues(issuesToSerialize, sourceStatus);
    this.stashDragPayload(serialized);
    dataTransfer.set(ISSUE_MIME_TYPE, new vscode.DataTransferItem(serialized));
    dataTransfer.set("text/plain", new vscode.DataTransferItem(serialized));

    // Show status bar feedback
    const count = issuesToSerialize.length;
    const message =
      count === 1
        ? `Drag to Pipeline view to add issue #${issuesToSerialize[0].issueNumber}`
        : `Drag to Pipeline view to add ${count} issues`;

    if (this.statusBarItem) {
      this.statusBarItem.text = `$(debug-alt) ${message}`;
      this.statusBarItem.show();

      // Clear status bar after 3 seconds or when drag ends
      setTimeout(() => {
        if (this.statusBarItem) {
          this.statusBarItem.hide();
        }
      }, 3000);
    }
  }

  /**
   * Handle drop operation - validate and execute pipeline start
   *
   * @param target - Drop target tree item (should be PipelineTreeProvider root)
   * @param dataTransfer - Data transfer object with issue data
   * @param token - Cancellation token
   */
  async handleDrop(
    target: BaseTreeItem | undefined,
    dataTransfer: vscode.DataTransfer,
    token: vscode.CancellationToken
  ): Promise<void> {
    if (token.isCancellationRequested) {
      this.logger?.debug("handleDrop: cancelled");
      return;
    }

    // Hide status bar feedback
    if (this.statusBarItem) {
      this.statusBarItem.hide();
    }

    // Log all available MIME types for debugging
    const availableMimes: string[] = [];
    dataTransfer.forEach((_item, mime) => availableMimes.push(mime));
    this.logger?.debug("handleDrop: received data transfer", {
      targetType: target?.constructor?.name ?? "undefined (root)",
      availableMimes,
    });

    // Get issue data from data transfer
    // Try custom MIME first, fall back to text/plain for cross-view drops
    // (VSCode doesn't forward custom MIME types between different tree views)
    const customData = dataTransfer.get(ISSUE_MIME_TYPE);
    const plainData = dataTransfer.get("text/plain");

    // Prefer custom MIME, but only if it has actual data.
    // VSCode sets the custom MIME entry for cross-view drops but leaves
    // .value empty — the real payload arrives via text/plain.
    const hasCustomValue =
      customData &&
      customData.value !== undefined &&
      customData.value !== null &&
      customData.value !== "";
    const issueData = hasCustomValue ? customData : (plainData ?? customData);

    this.logger?.debug("handleDrop: MIME lookup", {
      hasCustomMime: !!customData,
      customHasValue: hasCustomValue,
      hasTextPlain: !!plainData,
      selected: hasCustomValue ? "custom" : plainData ? "text/plain" : "custom-fallback",
    });

    if (!issueData) {
      this.logger?.warn("handleDrop: no MIME data found", { availableMimes });
      return;
    }

    let issues: SerializedIssue[];
    try {
      // DataTransferItem may provide value as string, object, or via asString()
      let rawValue = issueData.value;
      if (rawValue === undefined || rawValue === null || rawValue === "") {
        rawValue = await issueData.asString();
        this.logger?.debug("handleDrop: used asString() fallback", {
          length: typeof rawValue === "string" ? rawValue.length : "N/A",
        });
      }
      // Cross-view drops sometimes lose the text/plain payload entirely,
      // leaving only an empty custom-MIME entry. Recover from the static
      // stash written by handleDrag rather than failing with a JSON error.
      if (typeof rawValue !== "string" || rawValue.trim() === "") {
        const stashed = this.consumeStashedDragPayload();
        if (stashed) {
          this.logger?.warn("handleDrop: empty transfer payload — recovering from drag stash", {
            length: stashed.length,
          });
          rawValue = stashed;
        }
      }
      issues = this.deserializeIssues(rawValue);
      this.logger?.debug("handleDrop: deserialized issues", {
        count: issues.length,
        issueNumbers: issues.map((i) => i.issueNumber),
      });
    } catch (error) {
      const msg = error instanceof Error ? error.message : String(error);
      this.logger?.error("handleDrop: deserialization failed", {
        error: msg,
        valueType: typeof issueData.value,
        valuePreview: String(issueData.value).substring(0, 200),
      });
      void vscode.window.showErrorMessage(`Unable to process dropped issues: ${msg}`);
      return;
    }

    if (issues.length === 0) {
      return;
    }

    // --- Cross-column drop detection ---
    // Check if this drop targets a project board column (handled by ColumnDragAndDropController)
    const targetColumnStatus = this.resolveTargetColumnStatus(target);
    const dropRoot = this.resolveDropWorkspaceRoot(issues);
    if (targetColumnStatus && dropRoot) {
      // Filter out issues already in the target status (no-op same-column drops)
      const issuesToMove = issues.filter((i) => i.sourceTabStatus !== targetColumnStatus);
      if (issuesToMove.length > 0) {
        await this.handleCrossColumnDrop(issuesToMove, targetColumnStatus, dropRoot);
      }
      return;
    }

    // --- Existing pipeline drop path ---
    // Validate drop target (must be undefined for root, or explicitly a pipeline root item)
    if (!this.validateDropTarget(target)) {
      // Silent rejection - drop on invalid target
      return;
    }

    // Check for status warnings before processing
    const warningResult = await this.checkStatusWarnings(issues);
    if (warningResult === "cancel") {
      // User cancelled the drop operation
      return;
    } else if (warningResult === "view") {
      // User wants to view the issue(s) on GitHub
      // Open first issue URL
      if (issues.length > 0) {
        await openUrl(issues[0].url);
      }
      return;
    }
    // If 'add' or 'dont-ask', continue to validation

    // Validate issues before processing
    const validIssues: SerializedIssue[] = [];
    const invalidReasons: string[] = [];

    for (const issue of issues) {
      // Check if issue is already in pipeline
      const inPipeline = await this.isIssueInPipeline(issue.issueNumber);
      if (inPipeline) {
        invalidReasons.push(`Issue #${issue.issueNumber} is already in the pipeline`);
        continue;
      }

      validIssues.push(issue);
    }

    // Show error for invalid issues
    if (invalidReasons.length > 0 && validIssues.length === 0) {
      void vscode.window.showErrorMessage(
        `Unable to add issues to pipeline: ${invalidReasons.join(", ")}`
      );
      return;
    }

    // Process valid issues by calling existing command
    for (const issue of validIssues) {
      // Epic-specific path: pre-filter sub-issues by board status and open-PR
      // before enqueue so we never queue a Backlog sub-issue or one whose
      // branch already exists on the remote. See Issue #2992.
      if (
        issue.isEpic &&
        (issue.subIssueNumbers?.length ?? 0) > 0 &&
        this.queueService &&
        this.boardService
      ) {
        const handled = await this.enqueueEpicWithFilter(issue);
        if (handled) continue;
        // Fallthrough to command path on failure — never silently drop the epic.
      }

      // Create a minimal ReadyIssueTreeItem for command execution
      const issueItem = this.createIssueTreeItem(issue);

      // Call existing startPipelineForIssue command
      // This handles all validation, queuing, and user feedback
      await vscode.commands.executeCommand("nightgauge.startPipelineForIssue", issueItem);
    }

    // Show warning for partially successful drops
    if (invalidReasons.length > 0 && validIssues.length > 0) {
      void vscode.window.showWarningMessage(
        `Added ${validIssues.length} issue(s) to pipeline. ${invalidReasons.length} issue(s) skipped.`
      );
    }
  }

  /**
   * Pre-filter a dropped epic's sub-issues by project-board status and
   * open-PR presence, then enqueue the eligible set directly via IPC.
   *
   * Surfaces a summary toast:
   *   - All skipped → warning with the skipped breakdown, nothing enqueued.
   *   - Some skipped → info toast listing queued vs. skipped counts.
   *   - None skipped → fall through to the normal command path (no toast).
   *
   * Returns `true` if the epic was enqueued through this path (caller should
   * skip the default `startPipelineForIssue` command). Returns `false` when
   * the fall-through path should run (e.g. a cold cache skipped nothing so
   * the normal enqueue flow is equivalent).
   *
   * @see Issue #2992
   */
  private async enqueueEpicWithFilter(issue: SerializedIssue): Promise<boolean> {
    if (!this.queueService || !this.boardService || !this.workspaceRoot) {
      return false;
    }
    const subIssueNumbers = issue.subIssueNumbers ?? [];
    if (subIssueNumbers.length === 0) {
      return false;
    }

    const config = getEpicQueueFilterConfig(this.workspaceRoot);

    let result: Awaited<ReturnType<typeof filterEligibleSubIssues>>;
    try {
      result = await filterEligibleSubIssues({
        subIssueNumbers,
        workspaceRoot: this.workspaceRoot,
        projectBoardService: this.boardService,
        eligibleStatuses: config.eligibleStatuses,
        skipIfOpenPR: config.skipIssuesWithOpenPR,
      });
    } catch (err) {
      this.logger?.warn("Epic queue filter failed — falling back to unfiltered enqueue", {
        epicNumber: issue.issueNumber,
        error: err instanceof Error ? err.message : String(err),
      });
      return false;
    }

    const { eligible, skipped } = result;
    const repoOverride =
      issue.repoOwner && issue.repoName
        ? { owner: issue.repoOwner, repo: issue.repoName }
        : undefined;

    if (eligible.length === 0) {
      const breakdown = summarizeSkipped(skipped);
      void vscode.window.showWarningMessage(
        `Epic #${issue.issueNumber}: nothing to queue (${skipped.length} sub-issue${skipped.length === 1 ? "" : "s"} skipped — ${breakdown}).`
      );
      return true; // handled — do not fall through
    }

    try {
      await this.queueService.enqueueEpicFiltered(
        issue.issueNumber,
        issue.title,
        issue.labels,
        eligible,
        repoOverride
      );
    } catch (err) {
      this.logger?.error("enqueueEpicFiltered failed", {
        epicNumber: issue.issueNumber,
        error: err instanceof Error ? err.message : String(err),
      });
      void vscode.window.showErrorMessage(
        `Failed to queue epic #${issue.issueNumber}: ${err instanceof Error ? err.message : String(err)}`
      );
      return true;
    }

    if (skipped.length > 0) {
      void vscode.window.showInformationMessage(
        `Queued ${eligible.length} sub-issue${eligible.length === 1 ? "" : "s"} of epic #${issue.issueNumber}; skipped ${skipped.length} (${summarizeSkipped(skipped)}).`
      );
    }
    return true;
  }

  /**
   * Serialize issues to JSON string
   * @param issues - Issue tree items to serialize
   * @param sourceStatus - Status of the tab the drag originated from (for cross-column detection)
   */
  private serializeIssues(issues: ReadyIssueTreeItem[], sourceStatus?: string): string {
    const serialized: SerializedIssue[] = issues.map((item) => {
      const issue = item.getIssue();
      // Extract repo owner/name from URL when repoName isn't set directly
      let repoOwner: string | undefined = undefined;
      let repoName: string | undefined = item.repoName;
      const urlMatch = item.issueUrl?.match(/github\.com\/([^/]+)\/([^/]+)\/issues\//);
      if (urlMatch) {
        repoOwner = repoOwner ?? urlMatch[1];
        repoName = repoName ?? urlMatch[2];
      }
      return {
        issueNumber: item.issueNumber,
        title: item.label as string,
        labels: issue.labels ?? [],
        url: item.issueUrl,
        blockedBy: issue.blockedBy,
        sourceTabStatus: sourceStatus,
        isEpic: issue.isEpic ?? false,
        subIssueNumbers: issue.subIssueNumbers,
        repoOwner,
        repoName,
      };
    });

    return JSON.stringify(serialized);
  }

  /**
   * Serialize EpicGroupTreeItem instances to JSON string for cross-column drag
   */
  private serializeEpicGroups(epics: EpicGroupTreeItem[], sourceStatus?: string): string {
    const serialized: SerializedIssue[] = epics
      .filter((item) => item.epic !== null)
      .map((item) => ({
        issueNumber: item.epic!.number,
        title: item.epic!.title,
        labels: ["type:epic"],
        url: item.epic!.url,
        sourceTabStatus: sourceStatus,
        isEpic: true,
        subIssueNumbers: item.getChildIssueNumbers(),
        repoOwner: item.repoOwner,
        repoName: item.repoName,
      }));

    return JSON.stringify(serialized);
  }

  /**
   * Resolve the source tab status for the current drag operation.
   * Overridden by ColumnDragAndDropController to return the tab's status.
   */
  protected resolveSourceTabStatus(): string | undefined {
    return undefined;
  }

  /**
   * Resolve the target column status for a drop operation.
   * Returns null in the base class — only ColumnDragAndDropController returns a value.
   * When non-null, handleDrop routes to handleCrossColumnDrop instead of the pipeline path.
   */
  protected resolveTargetColumnStatus(
    _target: BaseTreeItem | undefined
  ): ProjectStatusValue | null {
    return null;
  }

  /**
   * Resolve the working directory used as the `gh` cwd for a cross-column
   * status move. Defaults to the controller's configured workspace root.
   *
   * Multi-repo views (e.g. the Repositories tree) override this to resolve the
   * cwd from the dragged issue's own repository, since a single static root
   * cannot serve every repo. Returning null/empty skips the cross-column path.
   */
  protected resolveDropWorkspaceRoot(_issues: SerializedIssue[]): string | null {
    return this.workspaceRoot;
  }

  /**
   * Refresh the affected view(s) after a cross-column drop completes.
   * Base implementation refreshes the single project board provider; multi-repo
   * views override this to refresh only the repositories that changed.
   */
  protected refreshAfterCrossColumnDrop(_issues: SerializedIssue[]): void {
    this.projectBoardProvider?.refresh();
  }

  /**
   * Handle a cross-column drop: update issue status on the GitHub project board.
   * For epics with sub-issues, cascades status change to all sub-issues.
   */
  private async handleCrossColumnDrop(
    issues: SerializedIssue[],
    targetStatus: ProjectStatusValue,
    cwd: string
  ): Promise<void> {
    const logger = new Logger("IssueDragAndDropController");

    for (const issue of issues) {
      if (issue.isEpic) {
        // Epic cascade: deterministic Go function moves epic + all sub-issues
        const owner = this.boardService?.getOwner();
        const projectNumber = this.boardService?.getProjectNumber();
        // Extract repo name from issue URL (e.g. https://github.com/nightgauge/nightgauge/issues/1452)
        const repoMatch = issue.url?.match(/github\.com\/[^/]+\/([^/]+)\//);
        const repo = repoMatch?.[1];

        if (owner && projectNumber && repo) {
          const subCount = issue.subIssueNumbers?.length ?? 0;
          await vscode.window.withProgress(
            {
              location: vscode.ProgressLocation.Notification,
              title: `Moving epic #${issue.issueNumber}${subCount > 0 ? ` and ${subCount} sub-issue(s)` : ""} to ${targetStatus}...`,
              cancellable: false,
            },
            async () => {
              try {
                const ipc = IpcClient.getInstance();
                const result = await ipc.epicTransitionStatus(
                  owner,
                  repo,
                  issue.issueNumber,
                  projectNumber,
                  targetStatus
                );
                // failures array is present in the Go response but not in the generated type
                const failures = (result as Record<string, unknown>).failures as
                  { number: number; error: string }[] | undefined;
                if (failures?.length) {
                  void vscode.window.showWarningMessage(
                    `Moved epic #${issue.issueNumber} to ${targetStatus}. ${failures.length} sub-issue(s) failed to update.`
                  );
                }
              } catch (err) {
                const msg = err instanceof Error ? err.message : String(err);
                void vscode.window.showErrorMessage(
                  `Failed to move epic #${issue.issueNumber}: ${msg}`
                );
              }
            }
          );
        } else {
          // Fallback: move epic only via projectFieldWriter (no board config)
          logger.warn("Missing board config for epic cascade, moving epic only", {
            owner,
            projectNumber,
            repo,
          });
          const result = await updateProjectItemStatus(
            issue.issueNumber,
            targetStatus,
            cwd,
            logger
          );
          if (!result.success) {
            void vscode.window.showErrorMessage(
              `Failed to move epic #${issue.issueNumber}: ${result.error}`
            );
          }
        }
      } else {
        // Individual issue
        vscode.window.setStatusBarMessage(`Moving #${issue.issueNumber} to ${targetStatus}`, 3000);
        const result = await updateProjectItemStatus(issue.issueNumber, targetStatus, cwd, logger);
        if (!result.success) {
          void vscode.window.showErrorMessage(
            `Failed to move #${issue.issueNumber}: ${result.error}`
          );
        }
      }
    }

    // Refresh the affected view(s) to reflect the new status
    this.refreshAfterCrossColumnDrop(issues);
  }

  /**
   * Stash the just-serialized drag payload so a drop whose DataTransfer
   * arrives empty (cross-view text/plain loss) can recover it. Static so the
   * pipeline-view drop target can read what the board-tab drag source wrote.
   */
  private stashDragPayload(json: string): void {
    IssueDragAndDropController.lastDragPayload = { json, at: Date.now() };
  }

  /**
   * Return the stashed drag payload if it is recent enough to belong to the
   * in-flight drag, then clear it (single-use). Returns null when absent or
   * stale, so a leftover stash can never be injected into an unrelated drop.
   */
  private consumeStashedDragPayload(): string | null {
    const stash = IssueDragAndDropController.lastDragPayload;
    IssueDragAndDropController.lastDragPayload = null;
    if (!stash) {
      return null;
    }
    if (Date.now() - stash.at > IssueDragAndDropController.DRAG_STASH_TTL_MS) {
      return null;
    }
    return stash.json;
  }

  /**
   * Deserialize issues from JSON string
   * @throws Error if JSON is malformed
   */
  private deserializeIssues(json: string | unknown): SerializedIssue[] {
    const parsed = typeof json === "string" ? JSON.parse(json) : json;

    // Validate structure
    if (!Array.isArray(parsed)) {
      throw new Error("Expected array of issues");
    }

    // Validate each issue
    for (const issue of parsed) {
      if (
        typeof issue !== "object" ||
        typeof issue.issueNumber !== "number" ||
        typeof issue.title !== "string"
      ) {
        throw new Error("Invalid issue structure");
      }
    }

    return parsed;
  }

  /**
   * Validate drop target
   *
   * Valid targets:
   * - undefined (dropping on tree root)
   * - PipelineTreeProvider root items (issue, stages)
   *
   * Invalid targets:
   * - Individual stage items (can't drop on specific stage)
   * - Queue section items (queuing handled by command)
   */
  protected validateDropTarget(target: BaseTreeItem | undefined): boolean {
    // Dropping on tree root is valid (target will be undefined)
    if (target === undefined) {
      return true;
    }

    // For now, reject drops on specific items
    // Future enhancement: Allow drops on queue section
    return false;
  }

  /**
   * Check if an issue is currently in the pipeline
   *
   * An issue is considered "in pipeline" if:
   * 1. It's the currently active issue in PipelineStateService, OR
   * 2. It's being processed in ConcurrentPipelineManager slots
   */
  private async isIssueInPipeline(issueNumber: number): Promise<boolean> {
    if (!this.stateService) {
      return false;
    }

    // Check active state service
    const state = await this.stateService.getState();
    if (state?.issue_number === issueNumber) {
      return true;
    }

    // Check concurrent slots
    if (this.concurrentPipelineManager?.isIssueInSlots(issueNumber)) {
      return true;
    }

    return false;
  }

  /**
   * Create a ReadyIssueTreeItem from serialized data
   *
   * Used to construct tree items for command execution after deserialization.
   */
  private createIssueTreeItem(issue: SerializedIssue): ReadyIssueTreeItem {
    // Create a minimal ReadyIssue object for tree item construction
    const readyIssue = {
      number: issue.issueNumber,
      title: issue.title,
      labels: issue.labels,
      url: issue.url,
      priority: null, // Will be inferred from labels if needed
      size: null, // Will be inferred from labels if needed
      blockedBy: issue.blockedBy ?? [],
      blocks: [],
    };

    return new ReadyIssueTreeItem(readyIssue, {
      repoName: issue.repoName,
      repoOwner: issue.repoOwner,
    });
  }

  /**
   * Check if issues require status warnings
   *
   * Shows warning dialog for issues that are In Progress or In Review.
   * Returns user's choice: 'add', 'cancel', 'view', or 'dont-ask'.
   *
   * @param issues - Issues being dropped
   * @returns Dialog result indicating user's choice
   */
  private async checkStatusWarnings(
    issues: SerializedIssue[]
  ): Promise<"add" | "cancel" | "view" | "dont-ask"> {
    // Load warning settings
    const settings = getWarningSettings();

    // If warnings disabled, skip check
    if (!settings.enabled) {
      return "add";
    }

    // Check each issue for status labels
    const issuesNeedingWarning: IssueWarningData[] = [];

    for (const issue of issues) {
      const status = this.extractIssueStatus(issue.labels);

      // Check if we should warn for this status
      if (
        (status === "in-progress" && settings.warnOnInProgress) ||
        (status === "in-review" && settings.warnOnInReview)
      ) {
        // Get workspace root for PR detection
        const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;

        // Try to fetch PR info if in review
        let prInfo = null;
        if (status === "in-review" && workspaceRoot) {
          prInfo = await getPRForIssue(issue.issueNumber, workspaceRoot);
        }

        issuesNeedingWarning.push({
          number: issue.issueNumber,
          title: issue.title,
          status,
          url: issue.url,
          prInfo,
        });
      }
    }

    // If no issues need warnings, continue
    if (issuesNeedingWarning.length === 0) {
      return "add";
    }

    // Show warning dialog
    if (!this.context) {
      // Context not set, skip warning (shouldn't happen in normal flow)
      return "add";
    }

    return await showStatusWarningDialog(issuesNeedingWarning, settings, this.context);
  }

  /**
   * Extract issue status from labels
   *
   * @param labels - Issue labels
   * @returns Status type or null if not in progress/review
   */
  private extractIssueStatus(labels: string[]): IssueStatus | null {
    for (const label of labels) {
      const normalized = label.toLowerCase();
      if (normalized.includes("status:in-progress")) {
        return "in-progress";
      }
      if (normalized.includes("status:in-review")) {
        return "in-review";
      }
    }
    return null;
  }

  /**
   * Dispose resources
   */
  dispose(): void {
    if (this.statusBarItem) {
      this.statusBarItem.dispose();
      this.statusBarItem = null;
    }
  }
}

/**
 * ColumnDragAndDropController - Per-tab drag-and-drop controller for cross-column moves.
 *
 * Each Backlog and Ready tab gets its own instance with `tabStatus` pre-configured.
 * When a drop occurs on the tab's tree root (target=undefined), `resolveTargetColumnStatus`
 * returns this tab's status so `handleDrop` routes to `handleCrossColumnDrop` instead of
 * the pipeline path.
 *
 * Dragging FROM this tab includes `sourceTabStatus` in the serialized payload via
 * `resolveSourceTabStatus`, enabling the target controller to detect same-column no-ops.
 *
 * @example
 * ```typescript
 * const backlogController = new ColumnDragAndDropController('Backlog');
 * backlogController.setBoardService(projectBoardService);
 * backlogController.setWorkspaceRoot(workspaceRoot);
 * ```
 */
export class ColumnDragAndDropController extends IssueDragAndDropController {
  constructor(private readonly tabStatus: ProjectStatusValue) {
    super();
  }

  protected override resolveSourceTabStatus(): string {
    return this.tabStatus;
  }

  protected override resolveTargetColumnStatus(
    _target: BaseTreeItem | undefined
  ): ProjectStatusValue {
    // Any drop on this tab's tree view (including undefined/root) targets this tab's column
    return this.tabStatus;
  }
}
