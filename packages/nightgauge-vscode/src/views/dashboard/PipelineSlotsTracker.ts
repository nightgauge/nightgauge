/**
 * PipelineSlotsTracker - Per-issue runtime state for the Overview slot cards.
 *
 * The shared `PipelineStateService` only retains state for a single issue
 * because its `_lastState` field is overwritten by every event. When the
 * pipeline runs concurrent slots (autonomous mode, multi-repo), the dashboard
 * needs a snapshot of every active issue at once.
 *
 * This tracker subscribes directly to the IPC `pipeline.stateChanged` event
 * stream and builds a `Map<issueNumber, SlotRuntimeSnapshot>` keyed by the
 * issueNumber that travels on each event. The Overview renderer joins this
 * with `IssueQueueService.getQueue()` to produce slot cards.
 */
import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import { IpcClient } from "../../services/IpcClient";
import type { SlotPhaseSummary, SlotStageStatus } from "./SlotCardTypes";

/**
 * Canonical pipeline stage order, used by the `stage.start` reconciliation
 * safeguard (#3244) to mark prior stages stuck at "running" as "complete"
 * when the orchestrator's `stage.complete` IPC event was lost.
 */
const PIPELINE_STAGE_ORDER: readonly PipelineStage[] = [
  "pipeline-start",
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
  "pipeline-finish",
] as const;

interface RawCompletedStage {
  stage: string;
  startedAt?: string;
  duration?: number;
}

interface RawGoState {
  completedStages?: RawCompletedStage[];
  skippedStages?: string[];
  stageErrors?: Record<string, string>;
  stage?: string;
  stageStart?: string;
  issueNumber?: number;
  title?: string;
  branch?: string;
  startedAt?: string;
  inputTokens?: number;
  outputTokens?: number;
  cacheReadTokens?: number;
  cacheCreationTokens?: number;
  totalCostUsd?: number;
  paused?: boolean;
  retryCount?: number;
}

interface RawStateChanged {
  issueNumber: number;
  repo?: string;
  state: RawGoState;
}

interface RawPhaseEvent {
  issueNumber: number;
  repo?: string;
  stage: string;
  name: string;
  index: number;
  total: number;
}

interface RawStageCompleteEvent {
  issueNumber: number;
  stage: string;
  repo?: string;
  error?: string;
  inputTokens?: number;
  outputTokens?: number;
  cacheReadTokens?: number;
  cacheCreationTokens?: number;
  costUsd?: number;
}

interface RawStageStartEvent {
  issueNumber: number;
  stage: string;
}

export interface SlotRuntimeStageEntry {
  status: SlotStageStatus;
  durationMs?: number;
  inputTokens?: number;
  outputTokens?: number;
  costUsd?: number;
}

export interface SlotRuntimeSnapshot {
  issueNumber: number;
  title?: string;
  branch?: string;
  repo?: string;
  startedAt?: string;
  paused?: boolean;
  hasIssues?: boolean;
  currentStage?: PipelineStage;
  currentPhase?: SlotPhaseSummary;
  stages: Record<string, SlotRuntimeStageEntry>;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  costUsd: number;
}

/**
 * Tracks per-issue pipeline runtime state independently of the shared
 * PipelineStateService singleton, so multiple concurrent slots can be
 * rendered as cards on the dashboard.
 */
export class PipelineSlotsTracker implements vscode.Disposable {
  private snapshots: Map<number, SlotRuntimeSnapshot> = new Map();
  private disposables: vscode.Disposable[] = [];
  private readonly _onChanged = new vscode.EventEmitter<void>();
  readonly onChanged = this._onChanged.event;
  private readonly ipc: IpcClient;
  /** Issues whose cumulative token/cost state has been hydrated from Go. */
  private readonly hydratedIssues = new Set<number>();

  constructor(ipc: IpcClient = IpcClient.getInstance()) {
    this.ipc = ipc;
    this.disposables.push(
      ipc.on("pipeline.stateChanged", (data) => {
        const event = data as RawStateChanged;
        if (typeof event?.issueNumber !== "number") return;
        this.applyStateChanged(event);
        this._onChanged.fire();
      })
    );

    this.disposables.push(
      ipc.on("phase.start", (data) => {
        const event = data as RawPhaseEvent;
        if (typeof event?.issueNumber !== "number") return;
        const snap = this.ensureSnapshot(event.issueNumber);
        snap.currentStage = event.stage as PipelineStage;
        snap.currentPhase = {
          name: event.name,
          index: event.index,
          total: event.total,
        };
        // If this snapshot has no cost data yet (dashboard opened mid-pipeline),
        // fetch current accumulated state from Go so cost/tokens reflect prior stages.
        if (
          !this.hydratedIssues.has(event.issueNumber) &&
          snap.costUsd === 0 &&
          snap.inputTokens === 0 &&
          event.repo
        ) {
          this.hydratedIssues.add(event.issueNumber);
          this.fetchAndApplyState(event.issueNumber, event.repo).catch(() => {});
        }
        this._onChanged.fire();
      })
    );

    this.disposables.push(
      ipc.on("phase.complete", (data) => {
        const event = data as RawPhaseEvent;
        if (typeof event?.issueNumber !== "number") return;
        const snap = this.snapshots.get(event.issueNumber);
        if (!snap) return;
        // Clear the active phase only if it's the one that just completed —
        // a later phase.start may already have landed.
        if (
          snap.currentPhase?.name === event.name &&
          snap.currentStage === (event.stage as PipelineStage)
        ) {
          snap.currentPhase = undefined;
        }
        this._onChanged.fire();
      })
    );

    this.disposables.push(
      ipc.on("stage.start", (data) => {
        const event = data as RawStageStartEvent;
        if (typeof event?.issueNumber !== "number") return;
        const snap = this.ensureSnapshot(event.issueNumber);
        const nextStage = event.stage as PipelineStage;
        // Clear any lingering phase from the previous stage so a stale
        // label (e.g. "Knowledge Base Read") doesn't bleed into the new
        // stage's card. Mirrors `OutputWindowState.updateSlotStage`
        // (Issue #3010 / #3240).
        if (snap.currentStage !== nextStage) {
          snap.currentPhase = undefined;
        }
        // Reconcile prior stages stuck at "running". A sequential pipeline
        // can have at most one stage running at a time, so any prior stage
        // still flagged "running" here had its `stage.complete` IPC event
        // dropped — mark it complete so the slot card stops showing two
        // concurrent running stages (Issue #3244).
        const stagePos = PIPELINE_STAGE_ORDER.indexOf(nextStage);
        if (stagePos > 0) {
          for (let i = 0; i < stagePos; i++) {
            const priorStage = PIPELINE_STAGE_ORDER[i];
            const prior = snap.stages[priorStage];
            if (prior?.status === "running") {
              snap.stages[priorStage] = { ...prior, status: "complete" };
            }
          }
        }
        snap.currentStage = nextStage;
        snap.stages[event.stage] = { status: "running" };
        this._onChanged.fire();
      })
    );

    this.disposables.push(
      ipc.on("stage.complete", (data) => {
        const event = data as RawStageCompleteEvent;
        if (typeof event?.issueNumber !== "number") return;
        const snap = this.ensureSnapshot(event.issueNumber);
        const stageEntry: SlotRuntimeStageEntry = {
          status: event.error ? "failed" : "complete",
        };
        if (event.inputTokens) stageEntry.inputTokens = event.inputTokens;
        if (event.outputTokens) stageEntry.outputTokens = event.outputTokens;
        if (event.costUsd) stageEntry.costUsd = event.costUsd;
        snap.stages[event.stage] = stageEntry;

        // Stage ended — no phase can be active. Clear if it was tied to
        // this stage so the slot card doesn't keep flashing a stale phase
        // label for the final stage of the pipeline (Issue #3240).
        if (snap.currentStage === (event.stage as PipelineStage)) {
          snap.currentPhase = undefined;
        }

        // The per-issue totals from pipeline.stateChanged are authoritative,
        // but stage.complete fires before the next stateChanged event arrives.
        // Apply the increment so the card cost reflects this stage immediately.
        if (event.inputTokens) snap.inputTokens += event.inputTokens;
        if (event.outputTokens) snap.outputTokens += event.outputTokens;
        if (event.cacheReadTokens) snap.cacheReadTokens += event.cacheReadTokens;
        if (event.costUsd) snap.costUsd += event.costUsd;
        if (event.error) snap.hasIssues = true;
        this._onChanged.fire();
      })
    );
  }

  /**
   * Apply a mid-stage token delta to the snapshot for the given issue.
   *
   * Called by Dashboard when a per-slot PipelineStateService fires
   * onTokenUsageUpdated during an active stage. This keeps the slot card's
   * cost/token display live (matching the treeview) rather than waiting for
   * the end-of-stage stage.complete event.
   *
   * Does NOT fire _onChanged — the Dashboard caller drives the UI update
   * via updatePanel("slot:onTokenUsageUpdated"). The next pipeline.stateChanged
   * event will SET the authoritative cumulative total, correcting any drift.
   */
  applyTokenDelta(
    issueNumber: number,
    delta: {
      inputTokens?: number;
      outputTokens?: number;
      cacheReadTokens?: number;
      costUsd?: number;
    }
  ): void {
    const snap = this.snapshots.get(issueNumber);
    if (!snap) return;
    if (delta.inputTokens) snap.inputTokens += delta.inputTokens;
    if (delta.outputTokens) snap.outputTokens += delta.outputTokens;
    if (delta.cacheReadTokens) snap.cacheReadTokens += delta.cacheReadTokens;
    if (delta.costUsd) snap.costUsd += delta.costUsd;
  }

  /**
   * Drop a slot — called when Go reports the pipeline has finished and the
   * slot is no longer in `activeSlots`.
   */
  forget(issueNumber: number): void {
    if (this.snapshots.delete(issueNumber)) {
      this._onChanged.fire();
    }
  }

  /**
   * Return a defensive copy of all current per-issue snapshots.
   */
  getSnapshots(): Map<number, SlotRuntimeSnapshot> {
    return new Map(this.snapshots);
  }

  getSnapshot(issueNumber: number): SlotRuntimeSnapshot | undefined {
    return this.snapshots.get(issueNumber);
  }

  /**
   * Reset all tracking — used between extension reloads / dashboard rebuilds.
   */
  reset(): void {
    this.hydratedIssues.clear();
    if (this.snapshots.size > 0) {
      this.snapshots.clear();
      this._onChanged.fire();
    }
  }

  /**
   * Fetch the current RuntimeState from Go for an active issue and apply the
   * accumulated token/cost totals to the snapshot. Called once per issue when
   * the dashboard opens mid-pipeline and has missed prior stateChanged events.
   */
  private async fetchAndApplyState(issueNumber: number, fullRepo: string): Promise<void> {
    const parts = fullRepo.split("/");
    if (parts.length !== 2) return;
    const [owner, repo] = parts;
    const state = await this.ipc.call<RawGoState | null>("pipeline.getState", {
      owner,
      repo,
      issueNumber,
    });
    if (!state) return;
    this.applyStateChanged({ issueNumber, repo: fullRepo, state });
    this._onChanged.fire();
  }

  dispose(): void {
    for (const d of this.disposables) d.dispose();
    this.disposables = [];
    this._onChanged.dispose();
  }

  private ensureSnapshot(issueNumber: number): SlotRuntimeSnapshot {
    let snap = this.snapshots.get(issueNumber);
    if (!snap) {
      snap = {
        issueNumber,
        stages: {},
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        costUsd: 0,
      };
      this.snapshots.set(issueNumber, snap);
    }
    return snap;
  }

  private applyStateChanged(event: RawStateChanged): void {
    const snap = this.ensureSnapshot(event.issueNumber);
    const go = event.state ?? {};

    if (go.title) snap.title = go.title;
    if (go.branch) snap.branch = go.branch;
    if (event.repo) snap.repo = event.repo;
    if (go.startedAt) snap.startedAt = go.startedAt;
    snap.paused = go.paused === true;
    snap.hasIssues = (go.retryCount ?? 0) > 0;

    if (typeof go.inputTokens === "number") snap.inputTokens = go.inputTokens;
    if (typeof go.outputTokens === "number") snap.outputTokens = go.outputTokens;
    if (typeof go.cacheReadTokens === "number") snap.cacheReadTokens = go.cacheReadTokens;
    if (typeof go.totalCostUsd === "number") snap.costUsd = go.totalCostUsd;

    // Rebuild stages from completedStages + skippedStages + the current stage.
    const stages: Record<string, SlotRuntimeStageEntry> = {};
    for (const sr of go.completedStages ?? []) {
      stages[sr.stage] = {
        status: "complete",
        durationMs: sr.duration ? sr.duration / 1_000_000 : undefined,
      };
    }
    for (const stageName of go.skippedStages ?? []) {
      stages[stageName] = { status: "skipped" };
    }
    for (const [stageName, errMsg] of Object.entries(go.stageErrors ?? {})) {
      stages[stageName] = { status: "failed" };
      if (errMsg) snap.hasIssues = true;
    }
    if (go.stage && !stages[go.stage]) {
      stages[go.stage] = { status: "running" };
    }
    snap.stages = stages;

    if (go.stage) snap.currentStage = go.stage as PipelineStage;
  }
}
