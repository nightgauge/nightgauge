/**
 * IssueQueueService - IPC delegation wrapper for the Go queue
 *
 * All queue state and logic now lives in the Go binary. This service
 * delegates every operation via IPC and relays `queue.changed` events
 * to VSCode UI consumers.
 *
 * @see Issue #1898 - Consolidate Queue into Go
 * @see Issue #236 - Queue Issues When Pipeline Active
 * @see docs/ARCHITECTURE.md - Context-Isolated Pipeline Architecture
 */

import * as vscode from "vscode";
import { IpcClient, type IpcQueueItem, type IpcQueueState } from "./IpcClient";
import { getRepoIdentity } from "../utils/configPathResolver";
import type { QueueState, QueueItem, QueueConfig, QueueCallbacks } from "../types/queue";
import { DEFAULT_QUEUE_CONFIG } from "../types/queue";
import { isBlocked, getBlockerTitles } from "../utils/dependencyUtils";
import type { BlockingIssue, ReadyIssue } from "./ProjectBoardService";

/**
 * IssueQueueService - Singleton IPC delegation wrapper
 *
 * @example
 * ```typescript
 * const queueService = IssueQueueService.getInstance(workspaceRoot);
 * await queueService.enqueue(42, 'Add dark mode feature', ['type:feature']);
 * ```
 */
export class IssueQueueService implements vscode.Disposable {
  private static instance: IssueQueueService | null = null;

  private workspaceRoot: string;
  private callbacks: QueueCallbacks = {};
  private config: Required<QueueConfig>;
  private readonly disposables: vscode.Disposable[] = [];

  /**
   * Optional shutdown predicate injected by the ConcurrentPipelineManager.
   *
   * When this returns `true`, any incoming enqueue() call is rejected with
   * a warning log and a null return. This blocks the window between a Stop
   * control being pressed and the manager resetting its shutdown flag —
   * the window during which delayed autonomous.dispatch events or other
   * asynchronous enqueue attempts would otherwise re-populate the queue
   * that the user just cleared. See fix/stop-controls-drain-queue.
   */
  private shutdownGuard: (() => boolean) | null = null;

  private readonly _onQueueChanged = new vscode.EventEmitter<QueueState | null>();
  readonly onQueueChanged = this._onQueueChanged.event;

  private constructor(
    workspaceRoot: string,
    config?: QueueConfig,
    _workspaceState?: vscode.Memento
  ) {
    this.workspaceRoot = workspaceRoot;
    this.config = { ...DEFAULT_QUEUE_CONFIG, ...config };

    // Relay queue.changed events from Go to VSCode consumers
    const ipc = IpcClient.getInstance();
    const sub = ipc.on("queue.changed", (data) => {
      const state = this.ipcStateToQueueState(data as IpcQueueState);
      this._onQueueChanged.fire(state);
      this.callbacks.onStatusChanged?.(state.status);
    });
    this.disposables.push(sub);
  }

  static getInstance(
    workspaceRoot: string,
    config?: QueueConfig,
    workspaceState?: vscode.Memento
  ): IssueQueueService {
    if (!IssueQueueService.instance) {
      IssueQueueService.instance = new IssueQueueService(workspaceRoot, config, workspaceState);
    }
    return IssueQueueService.instance;
  }

  static resetInstance(): void {
    if (IssueQueueService.instance) {
      IssueQueueService.instance.dispose();
      IssueQueueService.instance = null;
    }
  }

  setCallbacks(callbacks: QueueCallbacks): void {
    this.callbacks = callbacks;
  }

  /**
   * Install a predicate that decides whether enqueue() should be rejected
   * because a stop control is currently in effect. Wired from the bootstrap
   * layer to `ConcurrentPipelineManager.isShutdownInProgress`.
   *
   * Passing `null` clears the guard.
   */
  setShutdownGuard(guard: (() => boolean) | null): void {
    this.shutdownGuard = guard;
  }

  // ---------------------------------------------------------------------------
  // Queue Operations — all delegate to Go via IPC
  // ---------------------------------------------------------------------------

  async enqueue(
    issueNumber: number,
    title: string,
    labels?: string[],
    blockedBy?: BlockingIssue[],
    _options?: {
      lazyExpand?: boolean;
      repoOverride?: { owner: string; repo: string };
      /**
       * Platform-assigned run_id from a dashboard-trigger ack. Threaded into the
       * Go queue item's RemoteRunID so the scheduler adopts it instead of minting
       * a fresh run id — keeps the command ack runId and the synced pipeline-run
       * id identical so the dashboard run deep-link resolves (#4120).
       */
      remoteRunId?: string;
    }
  ): Promise<QueueItem | null> {
    // Stop-control guard — refuse new items while a stop is in progress.
    // Without this, a delayed autonomous.dispatch event (or any other
    // asynchronous enqueue path) could re-populate the queue that the
    // user just cleared via Stop All Issues / Stop Queue After Current.
    if (this.shutdownGuard?.()) {
      console.warn(`[IssueQueueService] Refusing enqueue of #${issueNumber} — stop in progress`);
      return null;
    }

    // Blocked-issue warning (UI dialog — must remain in TypeScript)
    if (blockedBy && blockedBy.length > 0) {
      const blockerCheck: ReadyIssue = {
        number: issueNumber,
        title,
        labels: labels ?? [],
        url: "",
        priority: null,
        size: null,
        blockedBy,
      };
      if (isBlocked(blockerCheck)) {
        if (this.callbacks.onBlockedWarning) {
          const proceed = await this.callbacks.onBlockedWarning(
            issueNumber,
            title,
            getBlockerTitles(blockerCheck)
          );
          if (!proceed) return null;
        }
      }
    }

    // Epic routing — delegate to Go's queue.enqueueEpic
    const resolvedLabels = labels ?? [];
    if (resolvedLabels.some((l) => l === "type:epic")) {
      return this.enqueueEpic(issueNumber, title, resolvedLabels, _options?.repoOverride);
    }

    const identity = _options?.repoOverride ?? (await getRepoIdentity(this.workspaceRoot));
    if (!identity) return null;

    const ipc = IpcClient.getInstance();
    await ipc.queueAdd(
      identity.owner,
      identity.repo,
      issueNumber,
      title,
      resolvedLabels,
      undefined,
      _options?.remoteRunId
    );

    const item: QueueItem = {
      issueNumber,
      title,
      position: 0, // Go assigns the real position
      status: "pending",
      addedAt: new Date().toISOString(),
      labels: resolvedLabels,
      blockedBy,
    };

    this.callbacks.onItemAdded?.(item);
    return item;
  }

  async enqueueEpic(
    epicNumber: number,
    title: string,
    labels: string[],
    repoOverride?: { owner: string; repo: string }
  ): Promise<QueueItem | null> {
    // Stop-control guard — mirror enqueue() so direct calls to enqueueEpic()
    // (not routed through enqueue's label detection) are also blocked.
    if (this.shutdownGuard?.()) {
      console.warn(`[IssueQueueService] Refusing enqueueEpic of #${epicNumber} — stop in progress`);
      return null;
    }

    const identity = repoOverride ?? (await getRepoIdentity(this.workspaceRoot));
    if (!identity) {
      console.warn(
        `[IssueQueueService] enqueueEpic #${epicNumber}: no repo identity — repoOverride=${JSON.stringify(repoOverride)}, workspaceRoot=${this.workspaceRoot}`
      );
      return null;
    }

    console.log(
      `[IssueQueueService] enqueueEpic #${epicNumber} via IPC → ${identity.owner}/${identity.repo}`
    );
    const ipc = IpcClient.getInstance();
    await ipc.queueEnqueueEpic(identity.owner, identity.repo, epicNumber, title, labels);
    console.log(`[IssueQueueService] enqueueEpic #${epicNumber}: IPC call completed`);

    const item: QueueItem = {
      issueNumber: epicNumber,
      title,
      position: 0,
      status: "pending",
      addedAt: new Date().toISOString(),
      labels,
    };

    this.callbacks.onItemAdded?.(item);
    return item;
  }

  /**
   * Enqueue an epic with a pre-filtered whitelist of sub-issues.
   *
   * Used by the drag-to-queue path where TypeScript has already filtered
   * sub-issues by project-board status and open-PR presence (see
   * `EpicQueueFilter`). Passes the eligible set through to Go's
   * `queue.enqueueEpic` IPC as the `eligibleSubIssues` param, so Go's
   * existing ordering / blockedBy computation stays in one place.
   *
   * When `eligibleSubIssueNumbers` is empty this method does nothing and
   * returns null — the caller should surface a "nothing to queue" toast
   * rather than enqueuing the whole epic.
   *
   * @see Issue #2992
   */
  async enqueueEpicFiltered(
    epicNumber: number,
    title: string,
    labels: string[],
    eligibleSubIssueNumbers: number[],
    repoOverride?: { owner: string; repo: string }
  ): Promise<QueueItem | null> {
    if (this.shutdownGuard?.()) {
      console.warn(
        `[IssueQueueService] Refusing enqueueEpicFiltered of #${epicNumber} — stop in progress`
      );
      return null;
    }

    if (eligibleSubIssueNumbers.length === 0) {
      console.log(
        `[IssueQueueService] enqueueEpicFiltered #${epicNumber}: no eligible sub-issues — skipping IPC`
      );
      return null;
    }

    const identity = repoOverride ?? (await getRepoIdentity(this.workspaceRoot));
    if (!identity) {
      console.warn(
        `[IssueQueueService] enqueueEpicFiltered #${epicNumber}: no repo identity — repoOverride=${JSON.stringify(repoOverride)}, workspaceRoot=${this.workspaceRoot}`
      );
      return null;
    }

    console.log(
      `[IssueQueueService] enqueueEpicFiltered #${epicNumber} via IPC → ${identity.owner}/${identity.repo} (${eligibleSubIssueNumbers.length} eligible)`
    );
    const ipc = IpcClient.getInstance();
    await ipc.queueEnqueueEpic(
      identity.owner,
      identity.repo,
      epicNumber,
      title,
      labels,
      eligibleSubIssueNumbers
    );

    const item: QueueItem = {
      issueNumber: epicNumber,
      title,
      position: 0,
      status: "pending",
      addedAt: new Date().toISOString(),
      labels,
    };
    this.callbacks.onItemAdded?.(item);
    return item;
  }

  async dequeue(): Promise<QueueItem | null> {
    const items = await this.dequeueIndependent(1, []);
    return items.length > 0 ? items[0] : null;
  }

  /**
   * Dequeue up to `maxSlots` independent issues. `runningItems` MUST carry each
   * in-flight issue's repo so the scheduler can enforce per-repo concurrency
   * caps (concurrency.per_repo_max / repository_overrides) — the global
   * maxSlots alone does not prevent two same-repo issues from dispatching.
   */
  async dequeueIndependent(
    maxSlots: number,
    runningItems: Array<{ repo: string; number: number }>
  ): Promise<QueueItem[]> {
    const ipc = IpcClient.getInstance();
    const ipcItems = await ipc.queueDequeueIndependent(maxSlots, runningItems);
    const items = ipcItems.map((i) => this.ipcItemToQueueItem(i));
    for (const item of items) {
      this.callbacks.onItemRemoved?.(item.issueNumber);
    }
    return items;
  }

  async remove(issueNumber: number): Promise<boolean> {
    const ipc = IpcClient.getInstance();
    await ipc.queueRemove(issueNumber);
    this.callbacks.onItemRemoved?.(issueNumber);
    return true;
  }

  async clear(): Promise<void> {
    const ipc = IpcClient.getInstance();
    await ipc.queueClear();
    this.callbacks.onQueueCleared?.();
  }

  async getQueue(): Promise<QueueState | null> {
    const ipc = IpcClient.getInstance();
    const ipcState = await ipc.queueList();
    return this.ipcStateToQueueState(ipcState);
  }

  async getQueueLength(): Promise<number> {
    const state = await this.getQueue();
    return state?.items.length ?? 0;
  }

  async isQueued(issueNumber: number): Promise<boolean> {
    const state = await this.getQueue();
    return state?.items.some((item) => item.issueNumber === issueNumber) ?? false;
  }

  async getStatus(): Promise<string> {
    const state = await this.getQueue();
    return state?.status ?? "idle";
  }

  async resume(): Promise<void> {
    // With Go owning the queue, resume is a client-side status observation.
    // The Go scheduler continues processing independently.
    // Fire status change callback for UI update.
    this.callbacks.onStatusChanged?.("waiting");
  }

  async reorder(issueNumber: number, _newPosition: number): Promise<boolean> {
    // Reorder is not yet implemented in Go IPC (queue.reorder protocol type
    // exists but handler is not wired). For now, return false.
    // TODO: Wire up queue.reorder IPC handler in Go
    void issueNumber;
    return false;
  }

  /**
   * Remove all queued items that belong to a specific epic.
   *
   * Used by abortEpic() to prevent queued epic sub-issues from being
   * dequeued after the user stops an epic.
   *
   * @param epicNumber - The parent epic issue number
   * @returns Array of removed queue items
   *
   * @see Issue #2261 - Per-slot / per-epic pipeline controls
   */
  async drainEpicItems(epicNumber: number): Promise<QueueItem[]> {
    const state = await this.getQueue();
    if (!state) return [];

    const drained: QueueItem[] = [];
    for (const item of state.items) {
      if (item.epicNumber === epicNumber) {
        await this.remove(item.issueNumber);
        drained.push(item);
      }
    }
    return drained;
  }

  async drainBlockedSuccessors(
    failedIssueNumber: number,
    _failedEpicOrder?: number
  ): Promise<QueueItem[]> {
    // With Go owning the queue, blocked successor draining should eventually
    // move to Go. For now, remove items blocked by the failed issue.
    const state = await this.getQueue();
    if (!state) return [];

    const drained: QueueItem[] = [];
    for (const item of state.items) {
      if (item.blockedBy?.some((b) => b.number === failedIssueNumber)) {
        await this.remove(item.issueNumber);
        drained.push(item);
      }
    }
    return drained;
  }

  async updateActiveSlots(
    _slots: Array<{
      slotIndex: number;
      issueNumber: number;
      worktreePath: string;
      branch: string;
      startedAt: string;
      currentStage?: string;
    }>
  ): Promise<void> {
    // Active slot tracking is managed by ConcurrentPipelineManager.
    // With Go owning queue state, this is a no-op — slot info stays client-side.
  }

  async onPipelineComplete(_success: boolean, _completedIssueNumber?: number): Promise<void> {
    // Pipeline completion handling is driven by Go callbacks.
    // This method is kept for API compatibility.
  }

  async peek(): Promise<QueueItem | null> {
    const state = await this.getQueue();
    return state?.items[0] ?? null;
  }

  getConfig(): Required<QueueConfig> {
    return this.config;
  }

  setConfig(config: Partial<QueueConfig>): void {
    this.config = { ...this.config, ...config };
  }

  async loadState(): Promise<void> {
    // Go loads queue state on startup — this is a no-op in the delegation wrapper.
  }

  async clearAllState(): Promise<void> {
    await this.clear();
  }

  updateWorkspaceRoot(newRoot: string): void {
    this.workspaceRoot = newRoot;
  }

  async estimateQueueTokens(): Promise<number> {
    // Token estimation stays in the SDK/intelligence layer.
    // Return 0 — callers use this for informational display only.
    return 0;
  }

  // ---------------------------------------------------------------------------
  // Conversion helpers — IPC types → TypeScript queue types
  // ---------------------------------------------------------------------------

  private ipcItemToQueueItem(item: IpcQueueItem): QueueItem {
    return {
      issueNumber: item.issueNumber,
      title: item.title,
      position: item.position,
      status: (item.status as QueueItem["status"]) || "pending",
      addedAt: item.addedAt,
      labels: item.labels,
      blockedBy: item.blockedBy?.map((b) => ({
        number: b.number,
        title: b.title,
        url: "",
        state: b.state as "OPEN" | "CLOSED",
      })),
      epicOrder: item.epicOrder,
      epicNumber: item.epicNumber,
      repoName: item.repo || undefined,
    };
  }

  private ipcStateToQueueState(ipcState: IpcQueueState): QueueState {
    return {
      schema_version: ipcState.schema_version,
      status: ipcState.status as QueueState["status"],
      items: (ipcState.items ?? []).map((i) => this.ipcItemToQueueItem(i)),
      updated_at: ipcState.updated_at,
    };
  }

  // ---------------------------------------------------------------------------
  // Lifecycle
  // ---------------------------------------------------------------------------

  dispose(): void {
    for (const d of this.disposables) {
      d.dispose();
    }
    this._onQueueChanged.dispose();
    IssueQueueService.instance = null;
  }
}
