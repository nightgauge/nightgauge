/**
 * PipelineTreeProvider - Main TreeDataProvider for pipeline sidebar
 *
 * Manages the tree state and subscribes to SDK events for real-time updates.
 * Now supports PipelineStateService as single source of truth (Issue #154).
 *
 * @see docs/ARCHITECTURE_DIAGRAMS.md - State Management Architecture
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import { PHASE_REGISTRY, type ExecutionStage } from "@nightgauge/sdk";
import {
  BaseTreeItem,
  IssueTreeItem,
  StageTreeItem,
  ActionTreeItem,
  QueueSectionTreeItem,
  BranchSelectorTreeItem,
  PhaseTreeItem,
  TeamSectionTreeItem,
  SubscriptionSectionTreeItem,
  WorkspaceSyncSidebarItem,
  type IssueInfo,
  type StageStatus,
  type WorkspaceSyncSidebarState,
} from "./items";
import type { SessionManager } from "../platform/SessionManager";
import type { TierGate } from "../platform/TierGate";
import type { LicensePreflight } from "../platform/LicensePreflight";
import type { Tier } from "../platform/types";
import type { IpcClientGenerated } from "../services/IpcClient.generated";
import { CompletedIssueTreeItem } from "./items/CompletedIssueTreeItem";
import { FailedIssueTreeItem } from "./items/FailedIssueTreeItem";
import { ConcurrentSlotTreeItem } from "./items/ConcurrentSlotTreeItem";
import type { IssueQueueService } from "../services/IssueQueueService";
import type { QueueState } from "../types/queue";

import type { PipelineStateService, PipelineState } from "../services/PipelineStateService";
import type { StagePhase } from "../schemas/pipelineState";
import type { CompletedIssuesService } from "../services/CompletedIssuesService";
import type { CompletedIssuesState } from "../types/completedIssues";
import { IpcClient } from "../services/IpcClient";

/**
 * Default stage order for display
 *
 * Includes bookend stages (pipeline-start, pipeline-finish) for reliable
 * synchronization points. These are deterministic orchestration stages
 * that execute synchronously with zero AI token consumption.
 */
const STAGE_ORDER: PipelineStage[] = [
  "pipeline-start",
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
  "pipeline-finish",
];

/**
 * Build a minimal phases array directly from a phase event payload.
 *
 * Produces `phaseIndex` "complete" synthetic phases (using PHASE_REGISTRY names
 * where available, falling back to "step-N") followed by the current phase with
 * status "running" (or "complete" when currentComplete=true).  This lets the
 * tree view show live progress ("Implementation [7/17]") immediately from the
 * event without an async getState() round-trip to the persisted state file.
 */
function buildSyntheticPhases(
  stage: string,
  phaseName: string,
  phaseIndex: number,
  totalPhases: number,
  currentComplete = false
): StagePhase[] {
  const registryPhases = (PHASE_REGISTRY as Record<string, Array<{ name: string }>>)[stage] ?? [];
  const phases: StagePhase[] = [];
  for (let i = 0; i < phaseIndex; i++) {
    phases.push({ name: registryPhases[i]?.name ?? `step-${i + 1}`, status: "complete" });
  }
  phases.push({ name: phaseName, status: currentComplete ? "complete" : "running" });
  return phases;
}

/**
 * PipelineTreeProvider - TreeDataProvider for the pipeline sidebar
 *
 * @example
 * ```typescript
 * const provider = new PipelineTreeProvider();
 *
 * // Connect to state service for unified state management
 * provider.setStateService(pipelineStateService);
 *
 * // Register the tree view
 * const treeView = vscode.window.createTreeView('nightgauge.pipelineView', {
 *   treeDataProvider: provider,
 *   showCollapseAll: true,
 * });
 *
 * // Set the current issue
 * provider.setIssue({
 *   number: 92,
 *   title: 'Pipeline Sidebar',
 *   branch: 'feat/92-pipeline-sidebar',
 * });
 * ```
 */
export class PipelineTreeProvider
  implements vscode.TreeDataProvider<BaseTreeItem>, vscode.Disposable
{
  private _onDidChangeTreeData = new vscode.EventEmitter<BaseTreeItem | undefined | void>();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;

  private stateService: PipelineStateService | null = null;
  private queueService: IssueQueueService | null = null;
  private completedIssuesService: CompletedIssuesService | null = null;
  private currentIssue: IssueTreeItem | null = null;
  private branchSelector: BranchSelectorTreeItem = new BranchSelectorTreeItem();
  private stages: Map<PipelineStage, StageTreeItem> = new Map();
  private queueSection: QueueSectionTreeItem = new QueueSectionTreeItem();
  private completedIssuesState: CompletedIssuesState | null = null;
  private concurrentSlots: Map<number, ConcurrentSlotTreeItem> = new Map();
  private preparingSlots: Map<number, ActionTreeItem> = new Map();
  private disposables: vscode.Disposable[] = [];
  private teamSection: TeamSectionTreeItem = new TeamSectionTreeItem();
  private subscriptionSection: SubscriptionSectionTreeItem = new SubscriptionSectionTreeItem();
  private workspaceSyncSidebarItem: WorkspaceSyncSidebarItem = new WorkspaceSyncSidebarItem();
  private tierGate: TierGate | null = null;
  private currentTier: Tier = "community";

  /**
   * Master switch mirror of `nightgauge.cloud.enabled` (default false).
   * Cloud features are not offered yet, so the subscription/team sidebar
   * sections stay hidden until the setting is turned on. Set from
   * bootstrap/services.ts via setCloudEnabled().
   */
  private cloudEnabled = false;

  /**
   * Cached running stage for synchronous access in getRootChildren().
   * Updated whenever state changes are detected.
   */
  private cachedRunningStage: PipelineStage | null = null;

  // Drag and drop controller for issue tree items (Issue #296)
  dragAndDropController: vscode.TreeDragAndDropController<BaseTreeItem> | undefined;

  // TreeView reference for updating title with counts (Issue #306)
  private treeView: vscode.TreeView<BaseTreeItem> | undefined;

  constructor() {
    // Initialize stages
    for (const stage of STAGE_ORDER) {
      this.stages.set(stage, new StageTreeItem(stage));
    }
    this.subscribeToIpcEvents();
  }

  private subscribeToIpcEvents(): void {
    const ipc = IpcClient.getInstance();
    const subs = [
      ipc.on("pipeline.stateChanged", (_data) => {
        this._onDidChangeTreeData.fire();
      }),
      ipc.on("stage.complete", (_data) => {
        this._onDidChangeTreeData.fire();
      }),
    ];
    this.disposables.push(...subs);
  }

  /**
   * Connect to PipelineStateService for unified state management
   *
   * When connected, the TreeProvider subscribes to state changes and
   * automatically syncs UI from the authoritative state file.
   *
   * @param stateService - The PipelineStateService singleton
   */
  setStateService(stateService: PipelineStateService): void {
    this.stateService = stateService;

    // Subscribe to state changes
    const disposable = stateService.onStateChanged((state) => {
      // During concurrent mode, the singleton IPC dispatches ALL slot state
      // events to this main provider. Ignore them — each ConcurrentSlotTreeItem
      // has its own subscription with issue-number filtering. Without this
      // guard, cross-slot state events trigger clearIssue()/syncFromState()
      // which causes visual flickering and interferes with slot rendering.
      if (this.concurrentSlots.size > 0 || this.preparingSlots.size > 0) {
        return;
      }

      // Always update running stage cache (handles both state and null)
      this.updateRunningStageCache(state);

      if (state) {
        this.syncFromState(state);
      } else {
        // State cleared (e.g., after PR merge)
        this.clearIssue();
      }
    });
    this.disposables.push(disposable);

    // Subscribe to phase events for real-time phase progress (Issue #1028, #3486)
    // Apply phase data directly from the event payload — no async getState() round-trip.
    // The event already contains { stage, phase, index, total, totalPhases } which is
    // everything setPhases() needs. In concurrent mode each ConcurrentSlotTreeItem has
    // its own phase subscriptions — skip to avoid cross-slot contamination.
    const phaseStartDisposable = stateService.onPhaseStart((event) => {
      if (this.concurrentSlots.size > 0 || this.preparingSlots.size > 0) return;
      const stageItem = this.stages.get(event.stage as PipelineStage);
      if (stageItem) {
        // A phase event is definitive proof the stage is running. Set status
        // before setPhases() so formatDescription() returns "PhaseLabel [X/Y]"
        // even if pipeline.stateChanged hasn't propagated yet.
        if (stageItem.getStatus() === "pending") {
          stageItem.setStatus("running");
        }
        const totalPhases = event.totalPhases ?? event.total;
        const phases = buildSyntheticPhases(event.stage, event.phase, event.index, totalPhases);
        stageItem.setPhases(phases, event.phase, totalPhases);
        this.refresh(stageItem);
      }
    });
    this.disposables.push(phaseStartDisposable);

    const phaseCompleteDisposable = stateService.onPhaseComplete((event) => {
      if (this.concurrentSlots.size > 0 || this.preparingSlots.size > 0) return;
      const stageItem = this.stages.get(event.stage as PipelineStage);
      if (stageItem) {
        const totalPhases = event.totalPhases ?? event.total;
        const phases = buildSyntheticPhases(
          event.stage,
          event.phase,
          event.index,
          totalPhases,
          true
        );
        stageItem.setPhases(phases, undefined, totalPhases);
        this.refresh(stageItem);
      }
    });
    this.disposables.push(phaseCompleteDisposable);

    // Subscribe to unified token events (Issue #404, #3486)
    // Accumulate per-stage token deltas directly on the StageTreeItem so cost/token
    // counts update in real time during execution rather than waiting for the next
    // Go state flush (which only happens at phase/stage boundaries).
    const tokenDisposable = stateService.onTokenUsageUpdated((tokenUpdate) => {
      if (this.concurrentSlots.size > 0 || this.preparingSlots.size > 0) {
        // Concurrent mode: each slot accumulates its own tokens via its own subscription
        this.refreshAll();
        return;
      }
      if (tokenUpdate.stage) {
        const stageItem = this.stages.get(tokenUpdate.stage as PipelineStage);
        if (stageItem) {
          const current = stageItem.getTokenInfo();
          stageItem.setTokenUsage({
            inputTokens: (current?.inputTokens ?? 0) + tokenUpdate.inputTokens,
            outputTokens: (current?.outputTokens ?? 0) + tokenUpdate.outputTokens,
            costUsd: (current?.costUsd ?? 0) + (tokenUpdate.costUsd ?? 0),
          });
        }
      }
      this.refreshAll();
    });
    this.disposables.push(tokenDisposable);

    // Initial sync
    stateService.getState().then((state) => {
      if (state) {
        this.syncFromState(state);
      }
    });
  }

  /**
   * Connect to IssueQueueService for queue state management
   *
   * When connected, the TreeProvider subscribes to queue changes and
   * automatically syncs the queue section from the queue state.
   *
   * @param queueService - The IssueQueueService singleton
   */
  setQueueService(queueService: IssueQueueService): void {
    this.queueService = queueService;

    // Subscribe to queue changes
    const disposable = queueService.onQueueChanged((state) => {
      this.syncQueueFromState(state);
    });
    this.disposables.push(disposable);

    // Initial sync
    queueService.getQueue().then((state) => {
      if (state) {
        this.syncQueueFromState(state);
      }
    });
  }

  /**
   * Sync queue section from QueueState
   *
   * Called whenever IssueQueueService emits a queue change.
   */
  private syncQueueFromState(state: QueueState | null): void {
    if (!state || state.items.length === 0) {
      this.queueSection.clear();
    } else {
      this.queueSection.setItems(state.items);
      this.queueSection.setStatus(state.status, state.pauseReason);
    }
    this.refreshAll();
  }

  /**
   * Connect to CompletedIssuesService for completed/failed issue tracking
   *
   * When connected, the TreeProvider subscribes to state changes and
   * automatically shows completed/failed sections in the tree.
   *
   * @param completedIssuesService - The CompletedIssuesService singleton
   */
  setCompletedIssuesService(completedIssuesService: CompletedIssuesService): void {
    this.completedIssuesService = completedIssuesService;

    // Subscribe to state changes
    const disposable = completedIssuesService.onStateChanged((state) => {
      this.completedIssuesState = state;
      this.refreshAll();
    });
    this.disposables.push(disposable);

    // Initial sync
    this.completedIssuesState = completedIssuesService.getState();
    this.refreshAll();
  }

  /**
   * Connect to SessionManager to track the current tier for feature gating.
   *
   * @param sessionManager - The SessionManager singleton
   */
  setSessionManager(sessionManager: SessionManager): void {
    const disposable = sessionManager.onSessionChanged((evt) => {
      // Track current tier for lock icon rendering (Issue #1472)
      this.currentTier = (evt.data.userTier as Tier) ?? "community";
    });
    this.disposables.push(disposable);
  }

  /**
   * Connect LicensePreflight so the subscription section updates on session
   * events (#4156 — re-wires SubscriptionSectionTreeItem, previously fully
   * built but never instantiated by any tree data provider; the earlier
   * removal consolidated subscription display into the Platform status bar
   * account hub, but a persistently-visible sidebar row is complementary,
   * not a replacement, for that on-demand quick pick).
   *
   * Listens to session changes: when the user authenticates, runs
   * preflight.validate() and pushes the result to subscriptionSection. When
   * signed out, resets to null. Unlike the pre-removal version, status
   * comes straight from LicensePreflightResult.status (#4156) instead of
   * being guessed from allowed/actionUrl heuristics.
   *
   * @param sessionManager - The SessionManager singleton
   * @param preflight - The LicensePreflight singleton
   * @see Issue #1477 - Add subscription status display to dashboard sidebar
   * @see Issue #4156 - License enforcement integrity + tier-gating correctness
   */
  setLicensePreflight(sessionManager: SessionManager, preflight: LicensePreflight): void {
    const disposable = sessionManager.onSessionChanged(async (evt) => {
      if (evt.current === "authenticated") {
        try {
          const result = await preflight.validate();
          this.subscriptionSection.update({
            tier: result.tier,
            status: result.status,
            expiresAt: result.expiresAt,
            offline: result.offline,
            lastUpdated: new Date(),
            machineBound: result.machineBound,
            machineCount: result.machineCount,
          });
        } catch {
          // Network failure — subscription section keeps stale data (no change)
        }
      } else {
        // Signed out or error — reset to unauthenticated state
        this.subscriptionSection.update(null);
      }
      this.refresh(this.subscriptionSection);
    });
    this.disposables.push(disposable);
  }

  /**
   * Connect team member data for Team+ tier users.
   *
   * Listens to session changes: when the user authenticates with a Team+ tier,
   * fetches members via IPC and pushes to teamSection.
   * When signed out or on a non-team tier, resets to null.
   *
   * @param sessionManager - The SessionManager singleton
   * @param ipcClient - The IPC client for platform API calls
   * @see Issue #1482 - Implement team member list view for Team+ tier
   * @see Issue #2091 - Migrated from PlatformApiClient HTTP to Go IPC
   */
  setTeamMembers(sessionManager: SessionManager, ipcClient: IpcClientGenerated): void {
    const disposable = sessionManager.onSessionChanged(async (evt) => {
      if (evt.current === "authenticated") {
        const tierAllowed =
          this.tierGate?.check("team-dashboard", this.currentTier).allowed ?? false;
        if (!tierAllowed) {
          // Non-team tier: keep section hidden
          return;
        }
        try {
          const results = await ipcClient.platformGetTeamMembers();
          this.teamSection.update({
            members: results.map((m) => ({
              memberId: m.userId,
              accountId: m.userId,
              role: m.role as "owner" | "admin" | "developer" | "viewer",
              joinedAt: m.joinedAt,
              name: m.name,
              email: m.email,
            })),
            offline: false,
            lastUpdated: new Date(),
          });
        } catch {
          // IPC failure — team section keeps stale data (no change)
        }
      } else {
        this.teamSection.update(null);
      }
      this.refresh(this.teamSection);
    });
    this.disposables.push(disposable);
  }

  /**
   * Set the TierGate for feature gating in tree item rendering.
   *
   * Also injects the same instance into teamSection (#4156) — it previously
   * fell back to its own module-local TierGate instead of the shared
   * singleton constructed once in bootstrap/services.ts.
   *
   * @param tierGate - The TierGate singleton
   * @see Issue #1472 - Add tier-aware feature gating throughout extension UI
   */
  setTierGate(tierGate: TierGate): void {
    this.tierGate = tierGate;
    this.teamSection.setTierGate(tierGate);
  }

  /**
   * Toggle cloud features on/off (mirrors `nightgauge.cloud.enabled`).
   *
   * When cloud is off (the default free-local configuration) the
   * subscription and team sidebar sections are hidden entirely — they only
   * make sense with an account against the hosted platform. Refreshes the
   * tree so the change is visible immediately when the setting flips.
   */
  setCloudEnabled(enabled: boolean): void {
    if (this.cloudEnabled === enabled) return;
    this.cloudEnabled = enabled;
    this.refreshAll();
  }

  setWorkspaceSyncStatus(state: WorkspaceSyncSidebarState): void {
    this.workspaceSyncSidebarItem.setState(state);
    this.refresh(this.workspaceSyncSidebarItem);
  }

  /**
   * Sync tree state from PipelineState
   *
   * Called whenever PipelineStateService emits a state change.
   * Updates issue info and all stage statuses from the authoritative state.
   */
  private syncFromState(state: PipelineState): void {
    // Update running stage cache first (before any early returns or UI updates)
    // This ensures getRootChildren() can detect running state even without currentIssue
    this.updateRunningStageCache(state);

    // Update issue if changed
    if (!this.currentIssue || this.currentIssue.issueNumber !== state.issue_number) {
      this.setIssue({
        number: state.issue_number,
        title: state.title,
        branch: state.branch,
        baseBranch: state.base_branch,
        labels: state.labels,
      });
    } else if (this.currentIssue) {
      // Update base branch on existing issue if changed
      const currentInfo = this.currentIssue.getInfo();
      if (currentInfo.baseBranch !== state.base_branch) {
        this.currentIssue.update({ baseBranch: state.base_branch });
      }
    }

    // Update branch selector with current base branch
    if (state.base_branch && this.branchSelector.getBranch() !== state.base_branch) {
      this.branchSelector.update(state.base_branch);
    }

    // Sync stage statuses from state
    for (const [stageName, stageState] of Object.entries(state.stages)) {
      const stage = stageName as PipelineStage;
      const stageItem = this.stages.get(stage);
      if (stageItem) {
        // Map state status to tree status
        const status = this.mapStateStatus(stageState.status);
        stageItem.setStatus(status);

        // Set duration if available
        if (stageState.duration_ms) {
          stageItem.setDuration(stageState.duration_ms);
        }

        // Set error if failed
        if (stageState.status === "failed" && stageState.error) {
          stageItem.setError(stageState.error);
        }

        // Set execution mode for token display (Issue #498)
        // When mode is 'interactive', tokens display as N/A
        const executionMode =
          (stageState as { execution_mode?: "headless" | "interactive" }).execution_mode ?? null;
        stageItem.setExecutionMode(executionMode);

        // Sync model selection metadata for tooltip display
        if (stageState.model_selection) {
          stageItem.setModelInfo({
            model: stageState.model_selection.model,
            source: stageState.model_selection.source,
            confidence: stageState.model_selection.confidence,
            complexity: stageState.model_selection.complexity,
            mode: stageState.model_selection.mode,
            effort: stageState.model_selection.effort,
          });
        }

        // Sync phase data from state (Issue #1028)
        // Handles extension reload / recovery from persisted state
        if (stageState.phases && stageState.phases.length > 0) {
          const registryPhases = PHASE_REGISTRY[stage as ExecutionStage] ?? [];

          // Always use registry length as the authoritative total.
          // The marker's total in state.json drifts when skills add phases
          // without updating the registry. Fall back to state.total_phases
          // only for stages with no registry entry.
          const totalForDisplay =
            registryPhases.length > 0 ? registryPhases.length : stageState.total_phases;

          // For completed/failed stages, fill in any registry phases that
          // are absent from state.json (e.g. due to startPhase write
          // failures or partial auto-skip) so the count shows X/X instead
          // of a smaller number over the registry total.
          let phasesForDisplay: StagePhase[] = (stageState.phases ?? []) as StagePhase[];

          if (stageState.status === "complete" || stageState.status === "failed") {
            // Downgrade phases stuck at "running" — the parent stage has
            // ended, so a "running" phase is a stale write from a missed
            // phase.complete event. For a completed stage, treat as
            // complete; for a failed stage, the in-flight phase is where
            // the failure landed (Issue #3240).
            const terminalPhaseStatus = stageState.status === "complete" ? "complete" : "failed";
            phasesForDisplay = phasesForDisplay.map((p) =>
              p.status === "running" ? { ...p, status: terminalPhaseStatus } : p
            );

            if (registryPhases.length > 0) {
              const recorded = new Set(stageState.phases.map((p) => p.name));
              const missing = registryPhases.filter((r) => !recorded.has(r.name));
              if (missing.length > 0) {
                phasesForDisplay = [
                  ...phasesForDisplay,
                  ...missing.map((r): StagePhase => ({
                    name: r.name,
                    status: "skipped",
                  })),
                ];
              }
            }
          }

          // When a stage is complete/failed, the displayed `current_phase`
          // should not point to anything (no live phase). Otherwise
          // StageTreeItem renders a "(N/M)" label off the stale value.
          const currentPhaseForDisplay =
            stageState.status === "complete" || stageState.status === "failed"
              ? undefined
              : stageState.current_phase;

          stageItem.setPhases(phasesForDisplay, currentPhaseForDisplay, totalForDisplay);
        } else if (stageItem.getPhaseCount() > 0 && stageState.status !== "running") {
          // Stage has no phases in state but tree still has children — clear them.
          // Skip for running stages: stateChanged events strip phase data from Go's
          // snapshot, so clearing here would wipe live phase progress mid-stage.
          stageItem.clearPhases();
        }
      }
    }

    // Update token usage display on stages using per-stage breakdown
    if (state.tokens?.per_stage) {
      for (const [stageName, stageTokens] of Object.entries(state.tokens.per_stage)) {
        const stageItem = this.stages.get(stageName as PipelineStage);
        if (stageItem && stageTokens) {
          stageItem.setTokenUsage({
            inputTokens: stageTokens.input,
            outputTokens: stageTokens.output,
            costUsd: stageTokens.cost_usd ?? 0,
          });
        }
      }
    }

    this.refreshAll();
  }

  /**
   * Map PipelineStateService status to StageStatus
   */
  private mapStateStatus(
    stateStatus: "pending" | "running" | "complete" | "failed" | "skipped" | "deferred"
  ): StageStatus {
    const mapping: Record<string, StageStatus> = {
      pending: "pending",
      running: "running",
      complete: "complete",
      failed: "failed",
      skipped: "skipped",
      deferred: "deferred",
    };
    return mapping[stateStatus] || "pending";
  }

  /**
   * Update cached running stage from state.
   *
   * Called when state changes are detected. This cache allows
   * the synchronous getRootChildren() to know if a stage is running
   * even when currentIssue hasn't been set yet.
   */
  private updateRunningStageCache(state: PipelineState | null): void {
    this.cachedRunningStage = null;

    if (state) {
      for (const [stageName, stageState] of Object.entries(state.stages)) {
        if (stageState.status === "running") {
          this.cachedRunningStage = stageName as PipelineStage;
          break;
        }
      }
    }
  }

  /**
   * Get the cached running stage for synchronous access.
   */
  private getRunningStageFromCache(): PipelineStage | null {
    return this.cachedRunningStage;
  }

  /**
   * Get tree item for VS Code
   */
  getTreeItem(element: BaseTreeItem): vscode.TreeItem {
    return element;
  }

  /**
   * Get children for a tree item
   */
  getChildren(element?: BaseTreeItem): Thenable<BaseTreeItem[]> {
    if (!element) {
      // Root level - show issue or placeholder
      return Promise.resolve(this.getRootChildren());
    }

    // Handle completed section
    if (element.contextValue === "completed-section") {
      if (this.completedIssuesState) {
        return Promise.resolve(
          this.completedIssuesState.completed.map((issue) => new CompletedIssueTreeItem(issue))
        );
      }
      return Promise.resolve([]);
    }

    // Handle failed section
    if (element.contextValue === "failed-section") {
      if (this.completedIssuesState) {
        const items: BaseTreeItem[] = this.completedIssuesState.failed.map(
          (issue) => new FailedIssueTreeItem(issue)
        );
        return Promise.resolve(items);
      }
      return Promise.resolve([]);
    }

    // Handle failed issue error details
    if (element instanceof FailedIssueTreeItem) {
      const children: BaseTreeItem[] = element.getChildren();
      return Promise.resolve(children);
    }

    // Return children of the element
    return Promise.resolve(element.getChildren());
  }

  /**
   * Get the parent of a tree item
   */
  getParent(element: BaseTreeItem): vscode.ProviderResult<BaseTreeItem> {
    // Check concurrent slots first — their stages/phases are self-contained
    if (this.concurrentSlots.size > 0) {
      if (element instanceof PhaseTreeItem) {
        for (const slot of this.concurrentSlots.values()) {
          for (const stage of STAGE_ORDER) {
            const stageItem = slot.getStage(stage);
            if (stageItem?.getChildren().includes(element)) {
              return stageItem;
            }
          }
        }
      }
      if (element instanceof StageTreeItem) {
        for (const slot of this.concurrentSlots.values()) {
          if (slot.getChildren().includes(element)) {
            return slot;
          }
        }
      }
      return undefined;
    }

    // Phase items have their stage as parent
    if (element instanceof PhaseTreeItem) {
      for (const stageItem of this.stages.values()) {
        if (stageItem.getChildren().includes(element)) {
          return stageItem;
        }
      }
      return undefined;
    }

    // Stage items have the issue as parent
    if (element instanceof StageTreeItem) {
      return this.currentIssue;
    }
    return undefined;
  }

  /**
   * Get root level children
   */
  private getRootChildren(): BaseTreeItem[] {
    const items: BaseTreeItem[] = [];

    // Prepend workspace sync status row when workspace is configured
    if (this.workspaceSyncSidebarItem.getState().status !== "hidden") {
      items.push(this.workspaceSyncSidebarItem);
    }

    // Debug: trace concurrent slot rendering (Issue #1888)
    if (this.concurrentSlots.size > 0 || this.preparingSlots.size > 0) {
      console.log(
        `[PipelineTree] getRootChildren concurrent mode: slots=${this.concurrentSlots.size}, preparing=${this.preparingSlots.size}, queue=${this.queueSection.getItemCount()}`
      );
    }

    // Concurrent mode: show preparing and active slots at root level
    if (this.concurrentSlots.size > 0 || this.preparingSlots.size > 0) {
      for (const slot of this.concurrentSlots.values()) {
        items.push(slot);
      }
      // Show preparing slots (worktree being created) below active slots
      for (const slot of this.preparingSlots.values()) {
        items.push(slot);
      }
      // Still show queue, completed, failed sections below
      return this.appendSharedSections(items);
    }

    if (!this.currentIssue) {
      // Check if a stage is running even without currentIssue set.
      // This handles the race condition between stage start and issue sync.
      const runningStage = this.getRunningStageFromCache();

      if (runningStage) {
        // Show running indicator when a stage is executing
        items.push(ActionTreeItem.createRunning(runningStage));
      } else {
        // Show placeholder when no issue is active and nothing is running
        items.push(ActionTreeItem.createNoIssue());
      }
    } else {
      // Single issue mode: add branch selector and stages as children of the issue
      this.currentIssue.clearChildren();
      // Add branch selector as first child (Issue #102)
      this.currentIssue.addChild(this.branchSelector);
      // Add stages
      for (const stage of STAGE_ORDER) {
        const stageItem = this.stages.get(stage);
        if (stageItem) {
          this.currentIssue.addChild(stageItem);
        }
      }
      items.push(this.currentIssue);
    }

    return this.appendSharedSections(items);
  }

  /**
   * Append shared sections (queue, completed, failed) to the root items.
   * Used by all modes (single, concurrent) to avoid duplication.
   */
  private appendSharedSections(items: BaseTreeItem[]): BaseTreeItem[] {
    // Always add queue section if there are queued items
    if (this.queueSection.getItemCount() > 0) {
      items.push(this.queueSection);
    }

    // Add completed issues section (collapsed by default)
    if (this.completedIssuesState && this.completedIssuesState.completed.length > 0) {
      const completedSection = new vscode.TreeItem(
        `Completed Issues (${this.completedIssuesState.completed.length})`,
        vscode.TreeItemCollapsibleState.Collapsed
      );
      completedSection.iconPath = new vscode.ThemeIcon(
        "check-all",
        new vscode.ThemeColor("testing.iconPassed")
      );
      completedSection.contextValue = "completed-section";
      completedSection.id = "completed-section";

      // Add completed issues as children (stored in getChildren logic)
      items.push(completedSection as BaseTreeItem);
    }

    // Add failed issues section (expanded by default)
    if (this.completedIssuesState && this.completedIssuesState.failed.length > 0) {
      const failedSection = new vscode.TreeItem(
        `Failed Issues (${this.completedIssuesState.failed.length})`,
        vscode.TreeItemCollapsibleState.Expanded
      );
      failedSection.iconPath = new vscode.ThemeIcon(
        "error",
        new vscode.ThemeColor("testing.iconFailed")
      );
      failedSection.contextValue = "failed-section";
      failedSection.id = "failed-section";

      // Add failed issues as children (stored in getChildren logic)
      items.push(failedSection as BaseTreeItem);
    }

    // Cloud sidebar sections (subscription + team) are only meaningful with an
    // account against the hosted platform. Cloud is not offered yet, so both
    // stay hidden until `nightgauge.cloud.enabled` is turned on. The local
    // free product never surfaces subscription/team status.
    if (this.cloudEnabled) {
      // Team section (Team+ tier only — hidden for community/pro)
      const teamAllowed = this.tierGate?.check("team-dashboard", this.currentTier).allowed ?? false;
      if (teamAllowed) {
        items.push(this.teamSection);
      }

      // Subscription section (#4156) — shown for every tier (including
      // community/unauthenticated) so subscription status is always visible
      // without opening the status bar quick pick. The Auth section stays
      // consolidated into the Platform status bar account hub — only
      // subscription display was re-added here.
      items.push(this.subscriptionSection);
    }

    return items;
  }

  /**
   * Set the current issue being worked on
   */
  setIssue(issueInfo: IssueInfo): void {
    this.currentIssue = new IssueTreeItem(issueInfo);
    this.refreshAll();
  }

  /**
   * Clear the current issue (return to placeholder state)
   */
  clearIssue(): void {
    this.currentIssue = null;
    this.resetAllStages();
    this.refreshAll();
  }

  /**
   * Update a stage's status
   */
  updateStageStatus(stage: PipelineStage, status: StageStatus): void {
    const stageItem = this.stages.get(stage);
    if (stageItem) {
      stageItem.setStatus(status);
      this.refresh(stageItem);
    }
  }

  /**
   * Reset all stages to pending
   */
  resetAllStages(): void {
    for (const stageItem of this.stages.values()) {
      stageItem.reset();
    }
  }

  /**
   * Refresh a specific item
   */
  refresh(item?: BaseTreeItem): void {
    this._onDidChangeTreeData.fire(item);
  }

  /**
   * Refresh the entire tree
   */
  refreshAll(): void {
    this._onDidChangeTreeData.fire();
    this.updateViewTitle();
  }

  /**
   * Set the TreeView reference for title updates (Issue #306)
   *
   * This enables the provider to update the view's title property
   * to show queue counts dynamically.
   */
  setTreeView(treeView: vscode.TreeView<BaseTreeItem>): void {
    this.treeView = treeView;
    this.updateViewTitle();
  }

  /**
   * Update the TreeView title to show total pipeline item count (Issue #306)
   *
   * Called whenever the tree data changes to keep the count in sync.
   * Format: "Pipeline (N)" where N is the active issue (if any) + queued items.
   */
  updateViewTitle(): void {
    if (!this.treeView) {
      return;
    }

    if (this.concurrentSlots.size > 0 || this.preparingSlots.size > 0) {
      const total = this.concurrentSlots.size + this.preparingSlots.size;
      this.treeView.title = `Pipeline (${total} concurrent)`;
      return;
    }
    const activeCount = this.currentIssue ? 1 : 0;
    const queuedCount = this.queueSection.getItemCount();
    this.treeView.title = `Pipeline (${activeCount + queuedCount})`;
  }

  /**
   * Get the current issue number if set
   */
  getCurrentIssueNumber(): number | undefined {
    return this.currentIssue?.issueNumber;
  }

  /**
   * Check if an issue is currently set
   */
  hasIssue(): boolean {
    return this.currentIssue !== null;
  }

  /**
   * Get a specific stage item
   */
  getStage(stage: PipelineStage): StageTreeItem | undefined {
    return this.stages.get(stage);
  }

  /**
   * Add a lightweight "preparing" placeholder for a concurrent slot.
   *
   * Shown immediately when an issue is dequeued, before worktree creation.
   * Gives instant visual feedback so the user knows the pipeline is starting.
   * Replaced by the full ConcurrentSlotTreeItem once onSlotStarted fires.
   */
  addPreparingSlot(issueNumber: number, title: string, epicNumber?: number): void {
    // Don't add if a real slot already exists for this issue
    if (this.concurrentSlots.has(issueNumber)) return;

    const maxLen = 32;
    const truncTitle = title.length > maxLen ? title.slice(0, maxLen - 1) + "…" : title;
    const item = ActionTreeItem.createLoading(`#${issueNumber} — ${truncTitle}`);
    item.id = `preparing-slot-${issueNumber}`;
    item.tooltip = `#${issueNumber} — ${title}`;
    if (epicNumber) {
      item.description = `Epic #${epicNumber} · Creating worktree…`;
    } else {
      item.description = "Creating worktree…";
    }
    item.contextValue = "concurrentSlot.preparing";
    this.preparingSlots.set(issueNumber, item);
    this.refreshAll();
  }

  /**
   * Remove a preparing slot placeholder.
   * Called when onSlotStarted replaces it with the full slot, or on failure.
   */
  removePreparingSlot(issueNumber: number): void {
    if (this.preparingSlots.delete(issueNumber)) {
      this.refreshAll();
    }
  }

  /**
   * Add a concurrent pipeline slot to the tree.
   * The slot subscribes to its own PipelineStateService for stage updates.
   */
  addConcurrentSlot(
    slotIndex: number,
    issueNumber: number,
    title: string,
    stateService: PipelineStateService,
    epicNumber?: number
  ): void {
    // Remove preparing placeholder if present
    this.preparingSlots.delete(issueNumber);

    const slot = new ConcurrentSlotTreeItem(
      slotIndex,
      issueNumber,
      title,
      stateService,
      epicNumber,
      () => this.refreshAll()
    );
    this.concurrentSlots.set(issueNumber, slot);
    console.log(
      `[PipelineTree] addConcurrentSlot #${issueNumber} slot=${slotIndex} (total=${this.concurrentSlots.size})`
    );
    this.refreshAll();
  }

  /**
   * Return the ConcurrentSlotTreeItem for a given issue number, or undefined.
   * Used to look up a slot by issue number.
   */
  getConcurrentSlot(issueNumber: number): ConcurrentSlotTreeItem | undefined {
    return this.concurrentSlots.get(issueNumber);
  }

  /**
   * Find the parent ConcurrentSlotTreeItem for a StageTreeItem.
   * Returns undefined if the stage is not inside a concurrent slot.
   */
  findParentSlot(stageItem: StageTreeItem): ConcurrentSlotTreeItem | undefined {
    for (const slot of this.concurrentSlots.values()) {
      if (slot.getChildren().includes(stageItem)) {
        return slot;
      }
    }
    return undefined;
  }

  /**
   * Mark a concurrent slot as completed or failed.
   */
  updateConcurrentSlotStatus(issueNumber: number, status: "completed" | "failed"): void {
    const slot = this.concurrentSlots.get(issueNumber);
    if (slot) {
      slot.setSlotStatus(status);
      this.refreshAll();
    }
  }

  /**
   * Remove a concurrent slot from the tree.
   */
  removeConcurrentSlot(issueNumber: number): void {
    const slot = this.concurrentSlots.get(issueNumber);
    if (slot) {
      slot.dispose();
      this.concurrentSlots.delete(issueNumber);
      console.log(
        `[PipelineTree] removeConcurrentSlot #${issueNumber} (remaining=${this.concurrentSlots.size})`
      );
      this.refreshAll();
    }
  }

  /**
   * Remove all concurrent slots (safety net for onAllComplete).
   */
  clearConcurrentSlots(): void {
    console.log(
      `[PipelineTree] clearConcurrentSlots (had=${this.concurrentSlots.size}, preparing=${this.preparingSlots.size})`,
      new Error().stack
    );
    for (const slot of this.concurrentSlots.values()) {
      slot.dispose();
    }
    this.concurrentSlots.clear();
    this.preparingSlots.clear();
    this.refreshAll();
  }

  /**
   * Check if concurrent mode is active.
   */
  get isConcurrentMode(): boolean {
    return this.concurrentSlots.size > 0 || this.preparingSlots.size > 0;
  }

  /**
   * Dispose of resources
   */
  dispose(): void {
    this._onDidChangeTreeData.dispose();
    for (const slot of this.concurrentSlots.values()) {
      slot.dispose();
    }
    this.concurrentSlots.clear();
    this.preparingSlots.clear();
    for (const disposable of this.disposables) {
      disposable.dispose();
    }
  }
}
