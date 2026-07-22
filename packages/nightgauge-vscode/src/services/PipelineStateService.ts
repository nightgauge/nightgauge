/**
 * PipelineStateService — Go-backed state relay.
 *
 * Pipeline state is owned by the Go binary. This module provides:
 * 1. Type exports consumed by UI components and commands
 * 2. A thin relay class that routes lifecycle calls to Go IPC
 *    and fires VS Code events from Go IPC notifications
 *
 * State is updated ONLY from Go IPC events (pipeline.stateChanged).
 * Lifecycle methods (startStage, failStage, etc.) notify Go via IPC;
 * Go emits pipeline.stateChanged, which updates _lastState here.
 *
 * @see Issue #1899 — Consolidate pipeline state into Go
 */

import * as vscode from "vscode";
import { IpcClient } from "./IpcClient";

// ---------------------------------------------------------------------------
// Stage label mapping for display
// ---------------------------------------------------------------------------

const STAGE_LABELS: Record<string, string> = {
  "pipeline-start": "Pipeline Start",
  "issue-pickup": "Issue Pickup",
  "feature-planning": "Feature Planning",
  "feature-dev": "Feature Development",
  "feature-validate": "Feature Validation",
  "pr-create": "PR Creation",
  "pr-merge": "PR Merge",
  "pipeline-finish": "Pipeline Finish",
};

/**
 * Canonical pipeline stage order. Used both for `current_stage_position`
 * tracking and the `stage.start` reconciliation safeguard (#3244) that marks
 * any prior stage stuck at "running" as "complete". Mirrors
 * `PIPELINE_STAGE_ORDER` from the SDK; kept local here to avoid a runtime
 * import dependency from the IPC relay.
 */
const PIPELINE_STAGE_ORDER: readonly string[] = [
  "pipeline-start",
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
  "pipeline-finish",
] as const;

// ---------------------------------------------------------------------------
// Types — preserved for backward compatibility
// ---------------------------------------------------------------------------

export type PipelineStageStatus =
  "pending" | "running" | "complete" | "failed" | "skipped" | "deferred";

export type PipelineOutcomeType =
  | "success"
  | "failure"
  | "partial"
  | "cancelled"
  | "productive"
  | "verify-and-close"
  | "already-resolved"
  | "budget-ceiling"
  // Stage was budget-killed (typically pr-merge) but the PR actually merged
  // out-of-band, so the work shipped. The pipeline reports success and the
  // queue is NOT cleared. See #3108.
  | "shipped-but-overbudget"
  // The pipeline skill(s) exited 0 but the post-condition gate detected
  // nothing actually happened (missing context file, branch not created,
  // PR still OPEN, etc.). Distinct from `failure` because the skill
  // didn't error — it just didn't do the work. The outcome classifier
  // emits this when ANY stage's gate failed with kind=no_op. See #3267.
  | "skill-no-op"
  // The run ended with the PR unmerged because of a repo-config blocker
  // (branch protection / required-check config mismatch) that no retry can
  // clear — a human must change repo config. Distinct from `failure` so
  // outcome telemetry and the learning loop see the recurrence, and so the
  // run can never present as complete (#190).
  | "blocked"
  // The issue was dispatched but its native `blockedBy` dependencies are
  // still open, so pickup DEFERRED before spending any tokens (#189/#305).
  // This is NOT a failure — nothing crashed, no work was attempted — so it
  // must never be booked as `failure`/subagent_crash. The issue stays
  // eligible; the Go blocker-close requeue re-dispatches it once the blocker
  // closes.
  | "deferred";

export type StageExecutionMode = "headless" | "interactive" | "automatic" | "manual";

export interface StagePhase {
  name: string;
  index: number;
  total: number;
  status?: "pending" | "running" | "complete" | "failed" | "skipped";
  started_at?: string;
  completed_at?: string;
}

export interface PTCMetrics {
  toolCallCount: number;
  successCount: number;
  failureCount: number;
}

export interface BacktrackRecord {
  stage: string;
  reason: string;
  timestamp: string;
  from_stage: string;
  to_stage: string;
  signal_type: string;
  rationale: string;
  attempt_number: number;
}

export interface ModelEscalationRecord {
  stage: string;
  fromModel: string;
  toModel: string;
  reason: string;
}

/** Event payload emitted when a tool call is recorded during pipeline execution. */
export interface ToolCallRecordedEvent {
  tool: string;
  target: string;
  timestamp: Date;
  durationMs?: number;
  args?: Record<string, unknown>;
  result?: string;
  error?: string;
}

/** Model selection details persisted per-stage for analytics. */
export interface ModelStageSelection {
  model: string;
  source: string;
  confidence?: number;
  complexity?: string;
  mode?: string;
  effort?: string;
}

/**
 * Per-stage served-model + adapter attribution threaded into completeStage
 * (#268). Both optional: `model` is the model that actually served the stage
 * (skillRunner servedModel, falling back to the requested modelDecision.model);
 * `adapter` is the executing adapter (claude | codex | gemini | …). Forwarded to
 * the Go notify handler, which records them so BuildV2Record attributes the
 * V2 record's per-stage ModelSelection and token Adapter — the source data for
 * the dashboard's by-model cost breakdown and Adapter Mix donut.
 */
export interface StageAttribution {
  model?: string;
  adapter?: string;
}

export type ExtendedStageState = {
  status: PipelineStageStatus;
  startTime?: number;
  endTime?: number;
  error?: string;
  executionMode?: StageExecutionMode;
  phase?: StagePhase;
  ptcMetrics?: PTCMetrics;
  started_at?: string;
  process_pid?: number;
  duration_ms?: number;
  phases?: StagePhase[];
  current_phase?: string;
  total_phases?: number;
  model_selection?: {
    model: string;
    source: string;
    confidence?: number;
    complexity?: string;
    mode?: string;
    effort?: string;
  };
  /** Performance mode active at this stage's start (Issue #3215). */
  performance_mode?: "efficiency" | "elevated" | "maximum" | "frontier";
  /**
   * Adapter that executed this stage (Issue #3224).
   *
   * Populated by the per-stage adapter resolver (Issue #3221). Until that
   * lands, the field is left undefined and the history writer falls back to
   * the run-level default adapter passed via `BuildRunRecordOptions`.
   */
  adapter?: import("../config/schema").ExecutionAdapter;
  /**
   * Source step that produced the resolved adapter (Issue #3223).
   *
   * Mirrors `model_selection.source` so analytics can distinguish per-stage
   * env / stage-config / fallback / global routing. Populated by
   * `HeadlessOrchestrator.onStageComplete` from `result.adapterDecision.source`
   * when the SkillRunner reports it. Absent on stages run before #3223.
   */
  adapter_source?: import("../utils/resolvers/adapterResolver").AdapterSource;
  /**
   * Adapters tried at stage start when fallback walked (Issue #3231).
   *
   * Populated by `setStageAdapter` from `AdapterDecision.adapterFallbackChainUsed`
   * when the walker attempted at least one fallback candidate. Length 1 is
   * never persisted (primary-success is implicit on `adapter`). The history
   * writer mirrors this onto `HistoryStageTokenUsageSchema.adapter_fallback_chain_used`.
   */
  adapter_fallback_chain_used?: Array<import("../config/schema").ExecutionAdapter>;
  auto_retry_count?: number;
  is_retrying?: boolean;
  next_retry_at?: string;
  completed_at?: string;
};

export interface TokenUsageUpdate {
  inputTokens: number;
  outputTokens: number;
  stage?: string;
  cacheReadTokens?: number;
  cacheCreationTokens?: number;
  costUsd?: number;
  /** Issue number for per-slot token routing (Issue #2815) */
  issueNumber?: number;
  /**
   * How `costUsd` was resolved (Issue #3228). Forwarded from
   * `TokenAccumulator.getTotal()` when the accumulator knows the active
   * adapter+model. Carried through `updateTokens` so per-stage records
   * can attribute billed cost vs. rate-card-computed cost.
   */
  costSource?: "native" | "computed" | "unknown";
}

export interface PipelineStageTokens {
  input: number;
  output: number;
  cost_usd?: number;
  cache_read?: number;
  cache_creation?: number;
  model?: string;
  /**
   * Resolution step that produced `cost_usd` (Issue #3228).
   *
   * `'native'`   — vendor-emitted cost (Claude `total_cost_usd`).
   * `'computed'` — derived from the rate-card pricing table.
   * `'unknown'`  — adapter+model has no pricing entry.
   *
   * Absent on records emitted before #3228; pre-#3228 readers normalize
   * undefined to `'native'` only when `cost_usd > 0`.
   */
  cost_source?: "native" | "computed" | "unknown";
}

export interface PipelineIssueTokens {
  input: number;
  output: number;
  cache_read: number;
  cache_creation: number;
  cost_usd: number;
}

export interface PipelineStateTokens {
  input: number;
  output: number;
  cacheRead?: number;
  cacheCreation?: number;
  cost_usd?: number;
  total_input?: number;
  total_output?: number;
  total_cache_read?: number;
  total_cache_creation?: number;
  estimated_cost_usd?: number;
  per_stage?: Record<string, PipelineStageTokens>;
  /** Cumulative issue-level totals across all completed stages. Never reset mid-stage. */
  per_issue?: PipelineIssueTokens;
}

export interface PipelineState {
  issue_number: number;
  title: string;
  branch: string;
  base_branch?: string;
  stages: Record<string, ExtendedStageState>;
  started_at: string;
  updated_at?: string;
  paused?: boolean;
  target_branch?: string;
  adapter?: string;
  model?: string;
  labels?: string[];
  backtracks?: BacktrackRecord[];
  backtrack_count?: number;
  modelEscalations?: ModelEscalationRecord[];
  model_escalations?: ModelEscalationRecord[];
  tokens?: PipelineStateTokens;
  execution_mode?: StageExecutionMode;
  outcome_type?: PipelineOutcomeType;
  retry_count?: number;
  /** Currently active pipeline stage name */
  current_stage?: string;
  /** 0-based position of the current stage in STAGE_ORDER */
  current_stage_position?: number;
  /** Human-readable label for the current stage (e.g., "Feature Development") */
  current_stage_label?: string;
  escalation_history?: Array<{
    stage: string;
    fromModel: string;
    toModel: string;
    reason: string;
  }>;
  ralph_iterations?: Record<string, number>;
  gate_results?: Array<{
    gate_name: string;
    result: string;
    duration_ms?: number;
    error_summary?: string;
  }>;
  pr_url?: string;
  /** Enrichment metadata for Discord/UI — set by HeadlessOrchestrator */
  pipeline_meta?: PipelineMeta;
  /** Proactive model escalations applied before stages run (Issue #1394) */
  proactive_escalations?: import("../schemas/pipelineState").ProactiveEscalationRecord[];
}

/** Enrichment metadata that flows to Discord embeds and UI consumers */
export interface PipelineMeta {
  /** Issue complexity label (XS/S/M/L/XL) */
  complexity?: string;
  /** Total planned file count from planning stage */
  file_count?: number;
  /** Parent epic number (if sub-issue) */
  epic_number?: number;
  /** Total sub-issues in the parent epic */
  epic_total?: number;
  /** Position of this sub-issue within the epic (1-indexed) */
  epic_position?: number;
  /** Pre-flight budget estimate in USD */
  budget_estimate_usd?: number;
  /** Budget ceiling in USD */
  budget_ceiling_usd?: number;
  /** When the estimator's inputs were pinned (#198 — auditability) */
  budget_estimate_captured_at?: string;
  /** Performance mode the estimate was computed under (#198) */
  budget_estimate_mode?: string;
  /** Routing decision (standard/trivial/fast-track) */
  route?: string;
  /** Stages skipped by routing */
  skip_stages?: string[];
  /** Primary model used */
  model?: string;
  /** PR number once created */
  pr_number?: number;
  /** Pipeline health score (0-100) on completion */
  health_score?: number;
  /** True when supercharge mode (Opus + max effort) was active for this run.
   * @deprecated Issue #3009 — prefer `performance_mode`. Retained additively
   * for one release so dashboards and external consumers keep working. */
  is_supercharge?: boolean;
  /** Model name when supercharge is active (e.g. "opus")
   * @deprecated Issue #3009 — prefer `performance_mode`. */
  supercharge_model?: string;
  /** Active performance mode (`efficiency` / `elevated` / `maximum`) — Issue #3009. */
  performance_mode?: "efficiency" | "elevated" | "maximum" | "frontier";
  /** Fable → Opus graceful downgrades applied this run after a usage/quota limit
   * (Issue #26). Set via setMeta() from the orchestrator; preserved across Go
   * state syncs (TS-only enrichment overlay) and surfaced by the notifiers. */
  quota_fallbacks?: Array<{ stage: string; from: string; to: string }>;
}

export type PipelineStateInput = PipelineState;

export interface StageTransitionResult {
  valid: boolean;
  reason?: string;
  allowed?: boolean;
  requiresConfirmation?: boolean;
  confirmationMessage?: string;
  error?: string;
}

/**
 * Shape of the RuntimeState object sent by the Go binary via pipeline.stateChanged IPC events.
 * This is NOT the same as PipelineState — it uses Go naming conventions and is converted on receipt.
 */
interface GoRuntimeState {
  completedStages?: Array<{ stage: string; startedAt?: string; duration?: number }>;
  skippedStages?: string[];
  stageErrors?: Record<string, string>;
  stage?: string;
  stageStart?: string;
  issueNumber?: number;
  title?: string;
  // Flat phase log keyed by stage. Go's authoritative phase history (Issue
  // #3415) — used to rehydrate per-stage phase counts on snapshots that arrive
  // after _lastState was wiped (extension reload, late subscription, etc.).
  phaseHistory?: Array<{
    stage: string;
    name: string;
    index: number;
    total: number;
    status: string;
    startedAt?: string;
    completedAt?: string;
  }>;
  branch?: string;
  startedAt?: string;
  inputTokens?: number;
  outputTokens?: number;
  cacheReadTokens?: number;
  cacheCreationTokens?: number;
  totalCostUsd?: number;
  paused?: boolean;
  retryCount?: number;
  escalationHistory?: Array<{
    stage: string;
    fromModel: string;
    toModel: string;
    reason: string;
  }>;
  ralphIterations?: Record<string, number>;
  gateResults?: Array<{
    gate_name: string;
    result: string;
    duration_ms?: number;
    error_summary?: string;
  }>;
  prUrl?: string;
}

// ---------------------------------------------------------------------------
// PipelineStateService — Go-backed state relay
// ---------------------------------------------------------------------------

export class PipelineStateService implements vscode.Disposable {
  private static instance: PipelineStateService | null = null;
  private ipc: IpcClient;
  private workspaceRoot: string;
  private issueNumber: number | null;
  private _lastState: PipelineState | null = null;
  private disposables: vscode.Disposable[] = [];
  /**
   * Target repo ("owner/name") for the active run, set by the orchestrator at
   * run start via {@link setRunRepo}. Included in every stage transition so the
   * Go IPC layer can emit the platform's run-creation context (which
   * materialises the live `pipeline_runs` row). Empty until a run starts.
   */
  private runRepo = "";

  // Event emitters for UI subscribers
  private readonly _onStateChanged = new vscode.EventEmitter<PipelineState | null>();
  readonly onStateChanged = this._onStateChanged.event;

  private readonly _onStageStart = new vscode.EventEmitter<{
    stage: string;
    issueNumber: number;
  }>();
  readonly onStageStart = this._onStageStart.event;

  private readonly _onStageComplete = new vscode.EventEmitter<{
    stage: string;
  }>();
  readonly onStageComplete = this._onStageComplete.event;

  private readonly _onStageError = new vscode.EventEmitter<{
    stage: string;
    issueNumber: number;
    error: string;
  }>();
  readonly onStageError = this._onStageError.event;

  private readonly _onPhaseStart = new vscode.EventEmitter<{
    stage: string;
    phase: string;
    index: number;
    total: number;
    totalPhases?: number;
    issueNumber?: number;
  }>();
  readonly onPhaseStart = this._onPhaseStart.event;

  private readonly _onPhaseComplete = new vscode.EventEmitter<{
    stage: string;
    phase: string;
    index: number;
    total: number;
    totalPhases?: number;
    issueNumber?: number;
  }>();
  readonly onPhaseComplete = this._onPhaseComplete.event;

  private readonly _onTokenUsageUpdated = new vscode.EventEmitter<TokenUsageUpdate>();
  readonly onTokenUsageUpdated = this._onTokenUsageUpdated.event;

  private readonly _onBacktrackTriggered = new vscode.EventEmitter<{
    fromStage: string;
    toStage: string;
    reason: string;
  }>();
  readonly onBacktrackTriggered = this._onBacktrackTriggered.event;

  private readonly _onToolCallRecorded = new vscode.EventEmitter<ToolCallRecordedEvent>();
  readonly onToolCallRecorded = this._onToolCallRecorded.event;

  private readonly _onBacktrackBlocked = new vscode.EventEmitter<BacktrackRecord>();
  readonly onBacktrackBlocked = this._onBacktrackBlocked.event;

  private readonly _onModelEscalated = new vscode.EventEmitter<
    import("../schemas/pipelineState").ModelEscalationRecord
  >();
  readonly onModelEscalated = this._onModelEscalated.event;

  private readonly _onHistoryRecorded = new vscode.EventEmitter<{
    issueNumber: number;
    success: boolean;
  }>();
  readonly onHistoryRecorded = this._onHistoryRecorded.event;

  private constructor(workspaceRoot: string, issueNumber: number | null = null) {
    this.workspaceRoot = workspaceRoot;
    this.issueNumber = issueNumber;
    this.ipc = IpcClient.getInstance();
    this.subscribeToEvents();
  }

  static getInstance(workspaceRoot?: string): PipelineStateService {
    if (!PipelineStateService.instance) {
      PipelineStateService.instance = new PipelineStateService(workspaceRoot ?? "");
    }
    return PipelineStateService.instance;
  }

  static createForWorktree(worktreePath: string, issueNumber?: number): PipelineStateService {
    return new PipelineStateService(worktreePath, issueNumber ?? null);
  }

  /**
   * Seed _lastState with a zero-valued object so updateTokens() does not
   * no-op for concurrent worktree slots that never receive an IPC
   * pipeline.notifyStageTransition call before the first token delta arrives.
   * Issue #3704.
   */
  initEmpty(): void {
    if (this._lastState) return;
    this._lastState = {
      issue_number: this.issueNumber ?? 0,
      stages: {},
      tokens: { input: 0, output: 0 },
    } as PipelineState;
  }

  static resetInstance(): void {
    PipelineStateService.instance = null;
  }

  dispose(): void {
    for (const d of this.disposables) {
      d.dispose();
    }
    this._onStateChanged.dispose();
    this._onStageStart.dispose();
    this._onStageComplete.dispose();
    this._onStageError.dispose();
    this._onPhaseStart.dispose();
    this._onPhaseComplete.dispose();
    this._onTokenUsageUpdated.dispose();
    this._onBacktrackTriggered.dispose();
    this._onToolCallRecorded.dispose();
    this._onBacktrackBlocked.dispose();
    this._onModelEscalated.dispose();
    this._onHistoryRecorded.dispose();
  }

  // -------------------------------------------------------------------------
  // State access
  // -------------------------------------------------------------------------

  async getState(): Promise<PipelineState | null> {
    return this._lastState;
  }

  getStatePath(): string {
    return `${this.workspaceRoot}/.nightgauge/pipeline/state.json`;
  }

  // -------------------------------------------------------------------------
  // Stage lifecycle — routed to Go IPC
  // -------------------------------------------------------------------------

  /**
   * Set the target repo ("owner/name") for the active run. Called by the
   * orchestrator at run start so every subsequent stage transition carries the
   * repo the platform needs to materialise a live run row. Idempotent.
   */
  setRunRepo(repo: string): void {
    this.runRepo = repo ?? "";
  }

  async initializePipeline(
    issueNumber: number,
    title: string,
    branch: string,
    baseBranch?: string
  ): Promise<void> {
    this.issueNumber = issueNumber;
    try {
      await this.ipc.call("pipeline.notifyStageTransition", {
        repo: this.runRepo,
        issueNumber,
        stage: "init",
        status: "initialized",
        title,
        branch,
        baseBranch: baseBranch ?? "",
      });
    } catch {
      // IPC not connected — create local state as fallback
      this._lastState = {
        issue_number: issueNumber,
        title,
        branch,
        base_branch: baseBranch,
        stages: {},
        started_at: new Date().toISOString(),
        tokens: { input: 0, output: 0 },
        execution_mode: "headless",
      };
      this._onStateChanged.fire(this._lastState);
    }
  }

  async startStage(stage: string, _options?: { forceBackward?: boolean }): Promise<void> {
    try {
      await this.ipc.call("pipeline.notifyStageTransition", {
        repo: this.runRepo,
        issueNumber: this.issueNumber ?? 0,
        stage,
        status: "running",
      });
    } catch {
      if (this._lastState) {
        this._lastState.stages[stage] = {
          status: "running",
          startTime: Date.now(),
        };
        // Fire onStageStart so DiscordService creates embeds even when
        // the Go IPC call fails (fallback path).
        this._onStageStart.fire({
          stage,
          issueNumber: this._lastState.issue_number,
        });
        this._onStateChanged.fire(this._lastState);
      }
    }
  }

  async completeStage(stage: string, attribution?: StageAttribution): Promise<void> {
    // Thread the per-stage usage accumulated during streaming (#227) so the Go
    // notify handler stops recording hardcoded zeros. `per_stage` is populated
    // by updateTokens() as the CLI streams, so by completion it holds the
    // stage total. input excludes cache reads (cacheReadTokens is separate);
    // the Go side (CompleteStageWithCost) combines them, matching the scheduler.
    const usage = this._lastState?.tokens?.per_stage?.[stage];
    // Thread the served model + executing adapter (#268) so the Go notify
    // handler records them as the runtime's per-stage StageModel/StageAdapter.
    // BuildV2Record then attributes the V2 record's per-stage ModelSelection
    // (→ StageMetric.model → cost_events.model_id, the by-model breakdown) and
    // token Adapter (→ StageMetric.provider → pipeline_events.adapter, the
    // Adapter Mix donut). Empty/undefined values are omitted from the wire and
    // ignored by the Go recorders. See the caller in HeadlessOrchestrator /
    // stage onComplete callbacks, where result.servedModel ?? modelDecision.model
    // and adapterDecision.adapter are in scope.
    try {
      await this.ipc.call("pipeline.notifyStageTransition", {
        repo: this.runRepo,
        issueNumber: this.issueNumber ?? 0,
        stage,
        status: "complete",
        inputTokens: usage?.input ?? 0,
        outputTokens: usage?.output ?? 0,
        cacheReadTokens: usage?.cache_read ?? 0,
        costUsd: usage?.cost_usd ?? 0,
        ...(attribution?.model ? { model: attribution.model } : {}),
        ...(attribution?.adapter ? { adapter: attribution.adapter } : {}),
      });
    } catch {
      if (this._lastState) {
        this._lastState.stages[stage] = {
          ...this._lastState.stages[stage],
          status: "complete",
          endTime: Date.now(),
        };
        this._onStateChanged.fire(this._lastState);
      }
    }
  }

  async failStage(stage: string, error: string, attribution?: StageAttribution): Promise<void> {
    // Thread the resolved model/adapter (#268-style attribution) on the
    // "failed" transition too, not just "complete". The Go notify handler
    // records p.Model on EVERY transition (server.go, before the status
    // switch), ignoring empties and taking latest-wins. Fable is the most
    // expensive tier and keeps its budget ceiling enabled, so Fable stages are
    // disproportionately killed (cost-cap / stall / budget / retry-exhaustion)
    // via failStage BEFORE a clean completeStage — the only path that used to
    // carry a model. With no model on either the "running" or "failed"
    // transition, those stages recorded no StageModel and the platform bucketed
    // them as cost_events.model_id = 'unknown'. Callers pass
    // result.servedModel ?? result.modelDecision?.model (the exact value handed
    // to `--model`), so an early kill still attributes to 'fable'.
    try {
      await this.ipc.call("pipeline.notifyStageTransition", {
        repo: this.runRepo,
        issueNumber: this.issueNumber ?? 0,
        stage,
        status: "failed",
        error,
        ...(attribution?.model ? { model: attribution.model } : {}),
        ...(attribution?.adapter ? { adapter: attribution.adapter } : {}),
      });
    } catch {
      if (this._lastState) {
        this._lastState.stages[stage] = {
          ...this._lastState.stages[stage],
          status: "failed",
          error,
          endTime: Date.now(),
        };
        this._onStateChanged.fire(this._lastState);
        this._onStageError.fire({
          stage,
          issueNumber: this._lastState.issue_number,
          error,
        });
      }
    }
  }

  /**
   * Record the resolved stage model up-front, before the stage executes (#367),
   * so ANY termination path attributes the correct model. #365 threaded
   * attribution onto completeStage/failStage, but that only covers the four
   * kill callsites that pass it; a stage that exits some other way still lost
   * its model and the platform bucketed its cost as
   * `cost_events.model_id = 'unknown'`. Recording once here — as soon as the
   * model is resolved — makes attribution independent of the exit path.
   *
   * Uses a dedicated, non-terminal `"model-resolved"` status: the Go notify
   * handler runs RecordStageModel/RecordStageAdapter on EVERY transition
   * BEFORE the status switch (server.go), so the model is captured, while the
   * novel status falls through the switch as a no-op — crucially NOT hitting
   * `"running"`, which would call BeginStage and reset the stage clock. It also
   * emits no platform telemetry event (buildStageTelemetryEvent's default).
   * Latest-wins: a concrete `servedModel` on completeStage still overrides it.
   * Fire-and-forget — telemetry must never block or fail the run.
   */
  async recordStageModel(stage: string, attribution: StageAttribution): Promise<void> {
    try {
      await this.ipc.call("pipeline.notifyStageTransition", {
        repo: this.runRepo,
        issueNumber: this.issueNumber ?? 0,
        stage,
        status: "model-resolved",
        ...(attribution.model ? { model: attribution.model } : {}),
        ...(attribution.adapter ? { adapter: attribution.adapter } : {}),
      });
    } catch {
      // Telemetry only — no local-state fallback. The model is re-asserted on
      // the stage's terminal transition (complete/failed) regardless.
    }
  }

  async skipStage(stage: string, _reason?: string): Promise<void> {
    try {
      await this.ipc.call("pipeline.notifyStageTransition", {
        repo: this.runRepo,
        issueNumber: this.issueNumber ?? 0,
        stage,
        status: "skipped",
      });
    } catch {
      if (this._lastState) {
        this._lastState.stages[stage] = {
          ...this._lastState.stages[stage],
          status: "skipped",
        };
        this._onStateChanged.fire(this._lastState);
      }
    }
  }

  async deferStage(stage: string): Promise<void> {
    try {
      await this.ipc.call("pipeline.notifyStageTransition", {
        repo: this.runRepo,
        issueNumber: this.issueNumber ?? 0,
        stage,
        status: "deferred",
      });
    } catch {
      if (this._lastState) {
        this._lastState.stages[stage] = {
          ...this._lastState.stages[stage],
          status: "deferred",
        };
        this._onStateChanged.fire(this._lastState);
      }
    }
  }

  /**
   * Signal that a full pipeline run has terminated (success, failure, or
   * cancellation). Routes to the Go IPC layer, which emits the platform's
   * terminal `pipeline_done` event so the live Pipelines view transitions the
   * run from 'running' to 'complete'/'failed'. Fire-and-forget — telemetry must
   * never block or fail the run. The Go side resolves the run's stable UUID and
   * filters `stagesRun` to the platform's canonical stage set.
   */
  async notifyPipelineComplete(result: {
    success: boolean;
    totalDurationMs: number;
    stagesRun: string[];
    /**
     * #266: true when the run's PR was forge-confirmed MERGED. The Go
     * notifyComplete handler uses this to record a merged run as complete even
     * if a late per-stage kill reported failure at pr-merge.
     */
    prMerged?: boolean;
    /**
     * #305: true when pickup DEFERRED because the issue's native blockedBy
     * dependencies are still open. Tells the Go notifyComplete handler to book
     * the run as a non-failure deferral — outcome="cancelled",
     * outcome_type="deferred", NO terminal_failure_kind — rather than
     * failed/subagent_crash.
     */
    deferred?: boolean;
    /**
     * #297/#309: per-stage execution-path decisions
     * ("deterministic" | "llm") the orchestrator's stageExecutionPaths map
     * captured for the run. The Go notifyComplete handler replays these onto
     * the run's RuntimeState so `execution_path` lands on the authoritative
     * history stage record — the Go runtime never observed these dogfood-path
     * decisions otherwise.
     */
    stageExecutionPaths?: Record<string, string>;
    /**
     * #297/#309: per-stage punt reasons, keyed by stage, for stages whose
     * deterministic-first hook declined and fell through to the LLM path. Only
     * present alongside stageExecutionPaths[stage]==="llm". Replayed onto the
     * RuntimeState so `punt_reason` lands on the history stage record.
     */
    stagePuntReasons?: Record<string, string>;
  }): Promise<void> {
    try {
      await this.ipc.call("pipeline.notifyComplete", {
        repo: this.runRepo,
        issueNumber: this.issueNumber ?? 0,
        success: result.success,
        totalDurationMs: Math.max(0, Math.round(result.totalDurationMs)),
        stagesRun: result.stagesRun,
        prMerged: result.prMerged ?? false,
        deferred: result.deferred ?? false,
        stageExecutionPaths: result.stageExecutionPaths ?? {},
        stagePuntReasons: result.stagePuntReasons ?? {},
      });
    } catch {
      // IPC not connected — telemetry is best-effort; the run's outcome is
      // still recorded locally in history JSONL and uploaded separately.
    }
  }

  /**
   * Merge enrichment metadata into the pipeline state.
   * Called by HeadlessOrchestrator at key lifecycle points (pre-flight,
   * post-routing, post-planning, epic context) so downstream consumers
   * like DiscordService can surface richer information.
   */
  setMeta(meta: Partial<PipelineMeta>): void {
    if (!this._lastState) return;
    this._lastState.pipeline_meta = {
      ...this._lastState.pipeline_meta,
      ...meta,
    };
    this._onStateChanged.fire(this._lastState);
  }

  async clearPipeline(): Promise<void> {
    this._lastState = null;
    this._onStateChanged.fire(null);
  }

  async pausePipeline(): Promise<void> {
    if (this._lastState) {
      this._lastState.paused = true;
      this._onStateChanged.fire(this._lastState);
    }
    // Persist pause state to disk via Go IPC (best-effort)
    if (this.issueNumber !== null) {
      try {
        await this.ipc.call("pipeline.setPaused", {
          issueNumber: this.issueNumber,
          paused: true,
        });
      } catch {
        // Non-critical: in-memory flag still set; UI still updates
      }
    }
  }

  async resumePipeline(): Promise<void> {
    if (this._lastState) {
      this._lastState.paused = false;
      this._onStateChanged.fire(this._lastState);
    }
    // Clear persisted pause state via Go IPC (best-effort)
    if (this.issueNumber !== null) {
      try {
        await this.ipc.call("pipeline.setPaused", {
          issueNumber: this.issueNumber,
          paused: false,
        });
      } catch {
        // Non-critical: in-memory flag still cleared; UI still updates
      }
    }
  }

  // -------------------------------------------------------------------------
  // Phase tracking — routed to Go IPC
  // -------------------------------------------------------------------------

  async startPhase(stage: string, phaseName: string, total: number): Promise<void> {
    // Always update local state and fire events immediately so the tree
    // view shows phase progress without depending on the IPC round-trip
    // (TS → Go → phase.start event → TS). The IPC notification to Go is
    // best-effort — it keeps Go's RuntimeState in sync but the UI must
    // not depend on the event coming back.
    if (this._lastState) {
      const stageState = this._lastState.stages[stage];
      if (stageState) {
        const phases = stageState.phases ?? [];
        if (!phases.some((p) => p.name === phaseName)) {
          phases.push({
            name: phaseName,
            index: phases.length,
            total,
            status: "running",
            started_at: new Date().toISOString(),
          });
        }
        stageState.phases = phases;
        stageState.current_phase = phaseName;
        stageState.total_phases = total;
      }
    }

    const index =
      this._lastState?.stages[stage]?.phases?.findIndex((p) => p.name === phaseName) ?? 0;

    this._onPhaseStart.fire({
      stage,
      phase: phaseName,
      index,
      total,
      totalPhases: total,
      issueNumber: this.issueNumber ?? undefined,
    });
    if (this._lastState) {
      this._onStateChanged.fire(this._lastState);
    }

    // Best-effort IPC notification to keep Go's RuntimeState in sync
    this.ipc
      .call("pipeline.notifyPhaseTransition", {
        repo: this.runRepo,
        issueNumber: this.issueNumber ?? 0,
        stage,
        name: phaseName,
        index,
        total,
        eventType: "start",
      })
      .catch(() => {
        // IPC failure is non-fatal — local state is already updated
      });
  }

  async completePhase(stage: string, phaseName: string, total: number): Promise<void> {
    // Always update local state and fire events immediately (same rationale
    // as startPhase — UI must not depend on IPC round-trip).
    let phaseIndex = 0;
    if (this._lastState) {
      const stageState = this._lastState.stages[stage];
      if (stageState?.phases) {
        const phase = stageState.phases.find((p) => p.name === phaseName && p.status === "running");
        if (phase) {
          phase.status = "complete";
          phase.completed_at = new Date().toISOString();
          phaseIndex = phase.index ?? 0;
        }
      }
    }

    this._onPhaseComplete.fire({
      stage,
      phase: phaseName,
      index: phaseIndex,
      total,
      totalPhases: total,
      issueNumber: this.issueNumber ?? undefined,
    });
    if (this._lastState) {
      this._onStateChanged.fire(this._lastState);
    }

    // Best-effort IPC notification to keep Go's RuntimeState in sync
    this.ipc
      .call("pipeline.notifyPhaseTransition", {
        repo: this.runRepo,
        issueNumber: this.issueNumber ?? 0,
        stage,
        name: phaseName,
        index: phaseIndex,
        total,
        eventType: "complete",
      })
      .catch(() => {
        // IPC failure is non-fatal — local state is already updated
      });
  }

  async skipPhase(stage: string, phaseName: string, total: number): Promise<void> {
    if (!this._lastState) return;
    const stageState = this._lastState.stages[stage];
    if (!stageState) return;
    const phases = stageState.phases ?? [];
    if (phases.some((p) => p.name === phaseName)) return;
    phases.push({
      name: phaseName,
      index: phases.length,
      total,
      status: "skipped",
    });
    stageState.phases = phases;
    stageState.total_phases = total;
    this._onStateChanged.fire(this._lastState);
  }

  async failPhase(
    _stage: string,
    _phaseName: string,
    _error: string,
    _total: number
  ): Promise<void> {}

  // -------------------------------------------------------------------------
  // Compatibility accessors
  // -------------------------------------------------------------------------

  isPaused(): boolean {
    return this._lastState?.paused ?? false;
  }

  getExecutionMode(): StageExecutionMode {
    return "headless";
  }

  setExecutionMode(_mode: StageExecutionMode): void {}

  setStageExecutionMode(
    _stageOrMode: string | StageExecutionMode,
    _mode?: StageExecutionMode
  ): void {}

  getBaseBranch(): string | null {
    return this._lastState?.base_branch ?? null;
  }

  setBaseBranch(branch: string): void {
    if (this._lastState) {
      this._lastState.base_branch = branch;
      this._onStateChanged.fire(this._lastState);
    }
  }

  getActiveIssueBlockingPickup(): number | null {
    return this._lastState?.issue_number ?? null;
  }

  async validateStageTransition(
    _stage: string | number,
    _fromStage?: string | number
  ): Promise<StageTransitionResult> {
    return { valid: true, allowed: true };
  }

  async updateTokens(update: TokenUsageUpdate): Promise<void> {
    if (!this._lastState) return;
    if (
      (update.inputTokens ?? 0) === 0 &&
      (update.outputTokens ?? 0) === 0 &&
      (update.cacheReadTokens ?? 0) === 0 &&
      (update.cacheCreationTokens ?? 0) === 0 &&
      (update.costUsd ?? 0) === 0
    ) {
      return;
    }
    if (!this._lastState.tokens) {
      this._lastState.tokens = { input: 0, output: 0 };
    }
    // Update per-stage token data so tree view displays costs
    if (update.stage) {
      if (!this._lastState.tokens.per_stage) {
        this._lastState.tokens.per_stage = {};
      }
      const existing = this._lastState.tokens.per_stage[update.stage];
      this._lastState.tokens.per_stage[update.stage] = {
        input: (existing?.input ?? 0) + (update.inputTokens ?? 0),
        output: (existing?.output ?? 0) + (update.outputTokens ?? 0),
        cost_usd: (existing?.cost_usd ?? 0) + (update.costUsd ?? 0),
        cache_read: (existing?.cache_read ?? 0) + (update.cacheReadTokens ?? 0),
        cache_creation: (existing?.cache_creation ?? 0) + (update.cacheCreationTokens ?? 0),
        // Issue #3228: stamp the cost_source label. Last write wins per stage —
        // within a single stage all chunks resolve via the same accumulator,
        // so they all carry the same source value.
        cost_source: update.costSource ?? existing?.cost_source,
      };
    }
    // Update totals (HeadlessOrchestrator sends deltas, so accumulate)
    this._lastState.tokens.input = (this._lastState.tokens.input ?? 0) + (update.inputTokens ?? 0);
    this._lastState.tokens.output =
      (this._lastState.tokens.output ?? 0) + (update.outputTokens ?? 0);
    this._lastState.tokens.total_input = this._lastState.tokens.input;
    this._lastState.tokens.total_output = this._lastState.tokens.output;
    this._lastState.tokens.total_cache_read =
      (this._lastState.tokens.total_cache_read ?? 0) + (update.cacheReadTokens ?? 0);
    this._lastState.tokens.total_cache_creation =
      (this._lastState.tokens.total_cache_creation ?? 0) + (update.cacheCreationTokens ?? 0);
    this._lastState.tokens.estimated_cost_usd =
      (this._lastState.tokens.estimated_cost_usd ?? 0) + (update.costUsd ?? 0);

    // Accumulate into per_issue (never resets mid-stage)
    const prevIssue = this._lastState.tokens.per_issue ?? {
      input: 0,
      output: 0,
      cache_read: 0,
      cache_creation: 0,
      cost_usd: 0,
    };
    this._lastState.tokens.per_issue = {
      input: prevIssue.input + (update.inputTokens ?? 0),
      output: prevIssue.output + (update.outputTokens ?? 0),
      cache_read: prevIssue.cache_read + (update.cacheReadTokens ?? 0),
      cache_creation: prevIssue.cache_creation + (update.cacheCreationTokens ?? 0),
      cost_usd: prevIssue.cost_usd + (update.costUsd ?? 0),
    };

    this._onTokenUsageUpdated.fire({
      stage: update.stage,
      inputTokens: update.inputTokens ?? 0,
      outputTokens: update.outputTokens ?? 0,
      cacheReadTokens: update.cacheReadTokens ?? 0,
      cacheCreationTokens: update.cacheCreationTokens ?? 0,
      costUsd: update.costUsd ?? 0,
      issueNumber: this._lastState?.issue_number,
    });
    this._onStateChanged.fire(this._lastState);
  }

  isPipelineComplete(_state?: PipelineState): boolean {
    const s = _state ?? this._lastState;
    if (!s) return false;
    const stages = Object.values(s.stages);
    return (
      stages.length > 0 && stages.every((st) => st.status === "complete" || st.status === "skipped")
    );
  }

  getCurrentIssueNumber(): Promise<number | null> {
    return Promise.resolve(this._lastState?.issue_number ?? null);
  }

  // -------------------------------------------------------------------------
  // State mutation no-ops — Go binary handles via IPC
  // -------------------------------------------------------------------------

  async recoverFromCrash(): Promise<void> {}
  async reconcileWithGitHub(): Promise<void> {}
  async cleanOrphanedFiles(_maxAgeHours: number): Promise<{ cleaned: string[]; errors: string[] }> {
    return { cleaned: [], errors: [] };
  }
  setContextLoader(_loader: unknown): void {}
  setOutcomeType(outcome: PipelineOutcomeType): void {
    // HeadlessOrchestrator calls this when the pipeline finishes.
    // Must update _lastState and fire onStateChanged so DiscordService
    // can send the final completion PATCH with the outcome status.
    if (this._lastState) {
      this._lastState.outcome_type = outcome;
      this._onStateChanged.fire(this._lastState);
    }
  }
  async setStageContextFileSize(_stage: string, _sizeBytes: number): Promise<void> {}
  async recordModelEscalation(
    _record: import("../schemas/pipelineState").ModelEscalationRecord
  ): Promise<void> {}
  async recordProactiveEscalation(
    _record: import("../schemas/pipelineState").ProactiveEscalationRecord
  ): Promise<void> {}
  async recordBacktrack(
    _record: import("../schemas/pipelineState").BacktrackRecord,
    _intermediateStages?: string[]
  ): Promise<void> {}
  async batchUpdate(_updater: (state: PipelineState) => PipelineState): Promise<void> {}
  async recordExecutionOutcome(_outcome: string): Promise<{ success: boolean; error?: string }> {
    return { success: true };
  }
  async setLabels(_labels: string[]): Promise<void> {}
  async setStageModelSelection(_stage: string, _selection: ModelStageSelection): Promise<void> {}
  async setStageAdapter(
    stage: string,
    decision: import("../utils/resolvers/adapterResolver").AdapterDecision
  ): Promise<void> {
    if (this._lastState?.stages[stage]) {
      this._lastState.stages[stage].adapter = decision.adapter as
        import("../config/schema").ExecutionAdapter | undefined;
      this._lastState.stages[stage].adapter_source = decision.source;
      // Issue #3231 — persist the fallback audit trail when the walker
      // attempted at least one fallback candidate (length ≥ 2). The
      // length-1 case is the common primary-success path; omitting keeps
      // state.json terse and preserves pre-#3231 record shapes.
      if (decision.adapterFallbackChainUsed && decision.adapterFallbackChainUsed.length >= 2) {
        this._lastState.stages[stage].adapter_fallback_chain_used =
          decision.adapterFallbackChainUsed as Array<import("../config/schema").ExecutionAdapter>;
      } else {
        delete this._lastState.stages[stage].adapter_fallback_chain_used;
      }
      this._onStateChanged.fire(this._lastState);
    }
  }
  async setStageProcessPid(_stage: string, _pid: number): Promise<void> {}
  recordToolCall(
    _stageOrRecord: string | ToolCallRecordedEvent,
    _toolName?: string,
    _success?: boolean,
    _durationMs?: number
  ): void {}

  async clearRetrying(stage: string): Promise<void> {
    if (this._lastState?.stages[stage]) {
      this._lastState.stages[stage].is_retrying = false;
      this._lastState.stages[stage].next_retry_at = undefined;
      this._onStateChanged.fire(this._lastState);
    }
  }

  // -------------------------------------------------------------------------
  // Resume / interrupted pipeline support
  // -------------------------------------------------------------------------

  getInterruptedPipelineInfo(): {
    issueNumber: number;
    title: string;
    branch: string;
    lastCompletedStage: string;
    interruptedStage?: string;
    nextResumeStage?: string;
    stagesCompleted?: string[];
    stagesRemaining?: string[];
  } | null {
    return null;
  }

  async prepareForResume(): Promise<void> {}

  // -------------------------------------------------------------------------
  // Event subscription from Go binary
  // -------------------------------------------------------------------------

  private subscribeToEvents(): void {
    this.disposables.push(
      this.ipc.on("pipeline.stateChanged", (data: unknown) => {
        const d = data as {
          issueNumber: number;
          repo: string;
          // Go RuntimeState has a different shape than PipelineState — see GoRuntimeState above.
          state: GoRuntimeState;
        };
        if (this.issueNumber !== null && d.issueNumber !== this.issueNumber) {
          return;
        }

        // Convert Go RuntimeState to TypeScript PipelineState format.
        // Go sends: { issueNumber, stage, completedStages, skippedStages, stageErrors, ... }
        // TS expects: { issue_number, stages: { "stage-name": { status, ... } }, ... }
        const goState = d.state;
        const stages: PipelineState["stages"] = {};

        // Group Go's authoritative phase history per stage (Issue #3415).
        // _lastState may be empty (extension reload, first subscription after a
        // stage finished) — Go ships PhaseHistory in every snapshot, so use it
        // as the source of truth and fall back to in-memory accumulation when
        // Go didn't include it (older binaries, partial snapshots).
        const phasesByStage = new Map<
          string,
          { phases: StagePhase[]; current_phase?: string; total_phases?: number }
        >();
        if (Array.isArray(goState.phaseHistory)) {
          for (const p of goState.phaseHistory) {
            let entry = phasesByStage.get(p.stage);
            if (!entry) {
              entry = { phases: [], total_phases: p.total };
              phasesByStage.set(p.stage, entry);
            }
            entry.phases.push({
              name: p.name,
              index: p.index,
              total: p.total,
              status: p.status as StagePhase["status"],
              started_at: p.startedAt,
              completed_at: p.completedAt,
            });
            entry.total_phases = p.total || entry.total_phases;
            if (p.status === "running") {
              entry.current_phase = p.name;
            }
          }
        }
        const phaseCarryForward = (stage: string): Partial<ExtendedStageState> => {
          const fromGo = phasesByStage.get(stage);
          if (fromGo && fromGo.phases.length > 0) {
            return {
              phases: fromGo.phases,
              current_phase: fromGo.current_phase,
              total_phases: fromGo.total_phases,
            };
          }
          const prev = this._lastState?.stages[stage];
          if (prev?.phases) {
            return {
              phases: prev.phases,
              current_phase: prev.current_phase,
              total_phases: prev.total_phases,
            };
          }
          return {};
        };
        // Issue #3419: when a stage transitions to a terminal status, any
        // phase still in `status: "running"` is logically wrong — a phase
        // can't outlive its stage. The phase tracker's normal completion
        // path (next phase starts → previous closes) doesn't fire for the
        // terminal phase of the terminal stage (e.g. self-assessment on
        // pr-merge), and Go never emits a phase:complete marker for the
        // last phase. Without this sweep, the stage list shows an orphan
        // spinner indefinitely. Mirrors the per-phase reconciliation done
        // by priorStageReconcile when the next stage starts.
        const sealPhases = (
          stage: string,
          terminalStatus: "complete" | "failed" | "skipped"
        ): Partial<ExtendedStageState> => {
          const carried = phaseCarryForward(stage);
          if (!carried.phases || carried.phases.length === 0) {
            return carried;
          }
          const completedAt = new Date().toISOString();
          let mutated = false;
          const sealed = carried.phases.map((p) => {
            if (p.status === "running") {
              mutated = true;
              return {
                ...p,
                status: terminalStatus as StagePhase["status"],
                completed_at: p.completed_at ?? completedAt,
              };
            }
            return p;
          });
          if (!mutated) return carried;
          return {
            ...carried,
            phases: sealed,
            // No phase is current once the stage has ended.
            current_phase: undefined,
          };
        };

        // Completed stages
        if (Array.isArray(goState.completedStages)) {
          for (const sr of goState.completedStages) {
            stages[sr.stage] = {
              status: "complete",
              startTime: sr.startedAt ? new Date(sr.startedAt).getTime() : undefined,
              endTime:
                sr.startedAt && sr.duration
                  ? new Date(sr.startedAt).getTime() + sr.duration / 1_000_000 // Go duration is in nanoseconds
                  : undefined,
              started_at: sr.startedAt,
              completed_at:
                sr.startedAt && sr.duration
                  ? new Date(
                      new Date(sr.startedAt).getTime() + sr.duration / 1_000_000
                    ).toISOString()
                  : undefined,
              // Carry forward phase data accumulated during execution.
              // Go's stateChanged snapshot lists timing in completedStages but
              // keeps phases in the flat phaseHistory array. Without this, the
              // phase count display ("16/16 phases") is wiped as soon as a
              // stage finishes — and on extension reload, lost permanently.
              // sealPhases also closes any trailing `running` phase so the
              // self-assessment spinner doesn't outlive the stage (#3419).
              ...sealPhases(sr.stage, "complete"),
            };
          }
        }

        // Skipped stages
        if (Array.isArray(goState.skippedStages)) {
          for (const name of goState.skippedStages) {
            stages[name] = {
              status: "skipped",
              ...sealPhases(name, "skipped"),
            };
          }
        }

        // Failed stages
        if (goState.stageErrors) {
          for (const [name, errMsg] of Object.entries(goState.stageErrors)) {
            stages[name] = {
              status: "failed",
              error: errMsg as string,
              ...sealPhases(name, "failed"),
            };
          }
        }

        // Currently running stage
        if (goState.stage && !stages[goState.stage]) {
          stages[goState.stage] = {
            status: "running",
            startTime: goState.stageStart ? new Date(goState.stageStart).getTime() : Date.now(),
            started_at: goState.stageStart || new Date().toISOString(),
            // Preserve accumulated phase progress so stateChanged events don't
            // wipe phase counts mid-stage (Issue #3415: prefer Go's authoritative
            // phaseHistory; fall back to our in-memory accumulation for
            // older binaries or partial snapshots).
            ...phaseCarryForward(goState.stage),
          };
        }

        // Defense-in-depth: preserve terminal stages from the previous state
        // that the Go snapshot doesn't include. This handles the case where
        // Go's runtime was deleted and recreated (IsComplete threshold hit
        // before all HeadlessOrchestrator bookend stages finished), causing
        // the new runtime to lose history of earlier completed stages.
        if (
          this._lastState &&
          this._lastState.issue_number === (goState.issueNumber ?? d.issueNumber)
        ) {
          for (const [name, prev] of Object.entries(this._lastState.stages)) {
            if (
              !stages[name] &&
              (prev.status === "complete" || prev.status === "skipped" || prev.status === "failed")
            ) {
              stages[name] = prev;
            }
          }
        }

        const converted: PipelineState = {
          issue_number: goState.issueNumber ?? d.issueNumber,
          title: goState.title || this._lastState?.title || `Issue #${d.issueNumber}`,
          branch: goState.branch || this._lastState?.branch || "",
          stages,
          started_at: goState.startedAt || this._lastState?.started_at || new Date().toISOString(),
          tokens: {
            // Prefer Go's values when non-zero (Go-driven pipeline path).
            // When zero/undefined (TS HeadlessOrchestrator concurrent path),
            // preserve TS-accumulated values from updateTokens().
            // Issue #2249: Go stateChanged overwrote all token fields to 0.
            input:
              goState.inputTokens && goState.inputTokens > 0
                ? goState.inputTokens
                : (this._lastState?.tokens?.input ?? 0),
            output:
              goState.outputTokens && goState.outputTokens > 0
                ? goState.outputTokens
                : (this._lastState?.tokens?.output ?? 0),
            total_input:
              goState.inputTokens && goState.inputTokens > 0
                ? goState.inputTokens
                : (this._lastState?.tokens?.total_input ?? 0),
            total_output:
              goState.outputTokens && goState.outputTokens > 0
                ? goState.outputTokens
                : (this._lastState?.tokens?.total_output ?? 0),
            total_cache_read:
              goState.cacheReadTokens && goState.cacheReadTokens > 0
                ? goState.cacheReadTokens
                : (this._lastState?.tokens?.total_cache_read ?? 0),
            total_cache_creation:
              goState.cacheCreationTokens && goState.cacheCreationTokens > 0
                ? goState.cacheCreationTokens
                : (this._lastState?.tokens?.total_cache_creation ?? 0),
            estimated_cost_usd:
              goState.totalCostUsd && goState.totalCostUsd > 0
                ? goState.totalCostUsd
                : (this._lastState?.tokens?.estimated_cost_usd ?? 0),
            // Preserve per_stage data accumulated by updateTokens() / stage.complete
            per_stage: this._lastState?.tokens?.per_stage,
            // Preserve per_issue accumulator — never overwritten by Go snapshots
            per_issue: this._lastState?.tokens?.per_issue,
          },
          execution_mode: this._lastState?.execution_mode ?? "headless",
          // Preserve fields that only exist in _lastState (set by other methods)
          base_branch: this._lastState?.base_branch,
          outcome_type: this._lastState?.outcome_type,
          paused: (goState.paused as boolean | undefined) ?? this._lastState?.paused,
          // Orchestration metadata from Go RuntimeState
          retry_count: goState.retryCount ?? this._lastState?.retry_count,
          escalation_history:
            Array.isArray(goState.escalationHistory) && goState.escalationHistory.length > 0
              ? goState.escalationHistory
              : this._lastState?.escalation_history,
          ralph_iterations: goState.ralphIterations ?? this._lastState?.ralph_iterations,
          gate_results:
            Array.isArray(goState.gateResults) && goState.gateResults.length > 0
              ? goState.gateResults.map(
                  (g: {
                    gate_name: string;
                    result: string;
                    duration_ms?: number;
                    error_summary?: string;
                  }) => ({
                    gate_name: g.gate_name,
                    result: g.result,
                    duration_ms: g.duration_ms,
                    error_summary: g.error_summary,
                  })
                )
              : this._lastState?.gate_results,
          pr_url: goState.prUrl || this._lastState?.pr_url,
          // Preserve enrichment metadata set by HeadlessOrchestrator.setMeta()
          pipeline_meta: this._lastState?.pipeline_meta,
          // Track currently running stage for display (derived from Go state)
          current_stage: goState.stage || this._lastState?.current_stage,
          current_stage_position: this._lastState?.current_stage_position,
          current_stage_label: this._lastState?.current_stage_label,
        };

        // Detect newly-running stages by comparing previous and incoming state.
        // Fire _onStageStart so DiscordService creates/updates embeds.
        const prevStages = this._lastState?.stages ?? {};
        for (const [stage, info] of Object.entries(converted.stages)) {
          if (info?.status === "running" && prevStages[stage]?.status !== "running") {
            this._onStageStart.fire({
              stage,
              issueNumber: d.issueNumber,
            });
          }
        }

        this._lastState = converted;
        this._onStateChanged.fire(this._lastState);
      })
    );

    this.disposables.push(
      this.ipc.on("stage.start", (data: unknown) => {
        const d = data as {
          issueNumber: number;
          stage: string;
          repo: string;
          title: string;
        };
        if (this.issueNumber !== null && d.issueNumber !== this.issueNumber) {
          return;
        }
        if (!this._lastState || this._lastState.issue_number !== d.issueNumber) {
          this._lastState = {
            issue_number: d.issueNumber,
            title: d.title || `Issue #${d.issueNumber}`,
            branch: "",
            stages: {},
            started_at: new Date().toISOString(),
            tokens: { input: 0, output: 0 },
            execution_mode: "headless",
          };
        }
        const stagePos = PIPELINE_STAGE_ORDER.indexOf(d.stage);

        // Reconcile prior stages stuck at "running" before recording the
        // new stage. In a sequential pipeline only one stage can be
        // running at a time; a prior stage at "running" means its
        // stage.complete IPC event was lost. Without this safeguard the
        // sidebar tree shows the prior stage's last phase spinning
        // forever (Issue #3244 — follow-up to #3240).
        const reconciledPriors: string[] = [];
        if (stagePos > 0) {
          const completedAt = new Date().toISOString();
          for (let i = 0; i < stagePos; i++) {
            const priorStage = PIPELINE_STAGE_ORDER[i];
            const prior = this._lastState.stages[priorStage];
            if (prior?.status === "running") {
              const reconciledPhases = prior.phases?.map((p) =>
                p.status === "running"
                  ? { ...p, status: "complete" as const, completed_at: completedAt }
                  : p
              );
              this._lastState.stages[priorStage] = {
                ...prior,
                status: "complete",
                endTime: Date.now(),
                completed_at: completedAt,
                ...(reconciledPhases ? { phases: reconciledPhases } : {}),
                current_phase: undefined,
              };
              reconciledPriors.push(priorStage);
            }
          }
        }

        this._lastState.stages[d.stage] = {
          status: "running",
          startTime: Date.now(),
          started_at: new Date().toISOString(),
        };
        // Track current stage context for display
        this._lastState.current_stage = d.stage;
        this._lastState.current_stage_position = stagePos >= 0 ? stagePos : undefined;
        this._lastState.current_stage_label = STAGE_LABELS[d.stage] ?? d.stage;

        // Emit synthetic stage.complete events for reconciled priors so
        // downstream listeners (Dashboard, OutputWindow) finalize their
        // per-stage UI just as if the orchestrator had delivered them.
        for (const priorStage of reconciledPriors) {
          this._onStageComplete.fire({ stage: priorStage });
        }

        this._onStageStart.fire({
          stage: d.stage,
          issueNumber: d.issueNumber,
        });
        this._onStateChanged.fire(this._lastState);
      })
    );

    this.disposables.push(
      this.ipc.on("stage.complete", (data: unknown) => {
        const d = data as {
          issueNumber: number;
          stage: string;
          repo: string;
          error: string;
          inputTokens: number;
          outputTokens: number;
          cacheReadTokens: number;
          cacheCreationTokens?: number;
          costUsd: number;
          model: string;
        };
        if (this.issueNumber !== null && d.issueNumber !== this.issueNumber) {
          return;
        }
        if (this._lastState && this._lastState.issue_number === d.issueNumber) {
          if (d.error) {
            this._lastState.stages[d.stage] = {
              ...this._lastState.stages[d.stage],
              status: "failed",
              error: d.error,
              endTime: Date.now(),
              completed_at: new Date().toISOString(),
            };
            this._onStageError.fire({
              stage: d.stage,
              issueNumber: d.issueNumber,
              error: d.error,
            });
          } else {
            this._lastState.stages[d.stage] = {
              ...this._lastState.stages[d.stage],
              status: "complete",
              endTime: Date.now(),
              completed_at: new Date().toISOString(),
            };
            this._onStageComplete.fire({ stage: d.stage });
          }
          // Update per-stage token data so tree view displays costs immediately.
          // Include cache-only stages (inputTokens=0 but cacheReadTokens>0).
          if (
            d.inputTokens > 0 ||
            d.outputTokens > 0 ||
            d.cacheReadTokens > 0 ||
            (d.cacheCreationTokens ?? 0) > 0
          ) {
            if (!this._lastState.tokens) {
              this._lastState.tokens = { input: 0, output: 0 };
            }
            if (!this._lastState.tokens.per_stage) {
              this._lastState.tokens.per_stage = {};
            }
            this._lastState.tokens.per_stage[d.stage] = {
              input: d.inputTokens ?? 0,
              output: d.outputTokens ?? 0,
              cost_usd: d.costUsd ?? 0,
              cache_read: d.cacheReadTokens ?? 0,
              cache_creation: d.cacheCreationTokens ?? 0,
              ...(d.model ? { model: d.model } : {}),
            };
            // Update totals
            this._lastState.tokens.input =
              (this._lastState.tokens.input ?? 0) + (d.inputTokens ?? 0);
            this._lastState.tokens.output =
              (this._lastState.tokens.output ?? 0) + (d.outputTokens ?? 0);
            this._lastState.tokens.total_input = this._lastState.tokens.input;
            this._lastState.tokens.total_output = this._lastState.tokens.output;
            this._lastState.tokens.total_cache_read =
              (this._lastState.tokens.total_cache_read ?? 0) + (d.cacheReadTokens ?? 0);
            this._lastState.tokens.total_cache_creation =
              (this._lastState.tokens.total_cache_creation ?? 0) + (d.cacheCreationTokens ?? 0);
            this._lastState.tokens.estimated_cost_usd =
              (this._lastState.tokens.estimated_cost_usd ?? 0) + (d.costUsd ?? 0);
            // Accumulate per_issue totals (never resets across stage transitions)
            const prevIssueTokens = this._lastState.tokens.per_issue ?? {
              input: 0,
              output: 0,
              cache_read: 0,
              cache_creation: 0,
              cost_usd: 0,
            };
            this._lastState.tokens.per_issue = {
              input: prevIssueTokens.input + (d.inputTokens ?? 0),
              output: prevIssueTokens.output + (d.outputTokens ?? 0),
              cache_read: prevIssueTokens.cache_read + (d.cacheReadTokens ?? 0),
              cache_creation: prevIssueTokens.cache_creation + (d.cacheCreationTokens ?? 0),
              cost_usd: prevIssueTokens.cost_usd + (d.costUsd ?? 0),
            };
            if ((d.inputTokens > 0 || d.outputTokens > 0) && (d.costUsd ?? 0) === 0) {
              console.warn(
                `[PipelineStateService] WARNING: stage.complete with tokens but costUsd=0. ` +
                  `Stage: ${d.stage}, input: ${d.inputTokens}, output: ${d.outputTokens}`
              );
            }

            this._onTokenUsageUpdated.fire({
              stage: d.stage,
              inputTokens: d.inputTokens ?? 0,
              outputTokens: d.outputTokens ?? 0,
              cacheReadTokens: d.cacheReadTokens ?? 0,
              cacheCreationTokens: d.cacheCreationTokens ?? 0,
              costUsd: d.costUsd ?? 0,
              issueNumber: d.issueNumber,
            });
          }
          this._onStateChanged.fire(this._lastState);
        }
      })
    );

    this.disposables.push(
      this.ipc.on("stage.failed", (data: unknown) => {
        const d = data as {
          issueNumber: number;
          stage: string;
          error: string;
        };
        if (this.issueNumber !== null && d.issueNumber !== this.issueNumber) {
          return;
        }
        this._onStageError.fire(d);
      })
    );

    this.disposables.push(
      this.ipc.on("phase.start", (data: unknown) => {
        const d = data as {
          issueNumber: number;
          stage: string;
          name: string;
          index: number;
          total: number;
        };
        console.log(
          `[PipelineStateService] phase.start EVENT: stage=${d.stage} phase=${d.name} issueNumber=${d.issueNumber} myIssue=${this.issueNumber} hasState=${!!this._lastState} hasStage=${!!this._lastState?.stages[d.stage]}`
        );
        if (this.issueNumber !== null && d.issueNumber !== this.issueNumber) {
          console.log(
            `[PipelineStateService] phase.start FILTERED OUT: myIssue=${this.issueNumber} eventIssue=${d.issueNumber}`
          );
          return;
        }
        if (this._lastState) {
          const stageState = this._lastState.stages[d.stage];
          if (stageState) {
            const phases = stageState.phases ?? [];
            if (!phases.some((p) => p.name === d.name)) {
              phases.push({
                name: d.name,
                index: d.index,
                total: d.total,
                status: "running",
                started_at: new Date().toISOString(),
              });
            }
            stageState.phases = phases;
            stageState.current_phase = d.name;
            stageState.total_phases = d.total;
            console.log(
              `[PipelineStateService] phase.start RECORDED: stage=${d.stage} phase=${d.name} totalRecorded=${phases.length}`
            );
          } else {
            console.log(
              `[PipelineStateService] phase.start DROPPED: no stageState for ${d.stage} (available stages: ${Object.keys(this._lastState.stages).join(",")})`
            );
          }
        } else {
          console.log(`[PipelineStateService] phase.start DROPPED: _lastState is null`);
        }
        this._onPhaseStart.fire({
          stage: d.stage,
          phase: d.name,
          index: d.index,
          total: d.total,
          totalPhases: d.total,
        });
        if (this._lastState) {
          this._onStateChanged.fire(this._lastState);
        }
      })
    );

    this.disposables.push(
      this.ipc.on("phase.complete", (data: unknown) => {
        const d = data as {
          issueNumber: number;
          stage: string;
          name: string;
          index: number;
          total: number;
        };
        if (this.issueNumber !== null && d.issueNumber !== this.issueNumber) {
          return;
        }
        if (this._lastState) {
          const stageState = this._lastState.stages[d.stage];
          if (stageState?.phases) {
            const phase = stageState.phases.find(
              (p) => p.name === d.name && p.status === "running"
            );
            if (phase) {
              phase.status = "complete";
              phase.completed_at = new Date().toISOString();
            }
          }
        }
        this._onPhaseComplete.fire({
          stage: d.stage,
          phase: d.name,
          index: d.index,
          total: d.total,
          totalPhases: d.total,
        });
        if (this._lastState) {
          this._onStateChanged.fire(this._lastState);
        }
      })
    );

    this.disposables.push(
      this.ipc.on("pipeline.complete", (data: unknown) => {
        const d = data as {
          issueNumber: number;
          success: boolean;
          totalInputTokens: number;
          totalOutputTokens: number;
          totalCostUSD: number;
          perStage: Array<{
            stage: string;
            inputTokens: number;
            outputTokens: number;
            costUsd: number;
          }>;
        };
        if (this.issueNumber !== null && d.issueNumber !== this.issueNumber) {
          return;
        }
        if (!this._lastState || this._lastState.issue_number !== d.issueNumber) {
          return;
        }
        // Merge Go's per-stage data with existing per_stage (which has cache
        // fields from stage.complete). Go's pipeline.complete only sends
        // inputTokens/outputTokens/costUsd — not cache fields.
        const existingPerStage = this._lastState.tokens?.per_stage ?? {};
        const perStageRecord: Record<string, PipelineStageTokens> = {
          ...existingPerStage,
        };
        for (const s of d.perStage) {
          const existing = existingPerStage[s.stage];
          perStageRecord[s.stage] = {
            input: s.inputTokens,
            output: s.outputTokens,
            cost_usd: s.costUsd ?? 0,
            cache_read: existing?.cache_read ?? 0,
            cache_creation: existing?.cache_creation ?? 0,
          };
        }

        // Go's RuntimeState only accumulates non-cached input tokens, which
        // are ~0 when Claude uses heavy prompt caching. Prefer the values
        // accumulated by stage.complete if Go sends 0.
        const prevTokens = this._lastState.tokens;
        const totalInput =
          d.totalInputTokens > 0
            ? d.totalInputTokens
            : (prevTokens?.total_input ?? prevTokens?.input ?? 0);
        const totalOutput =
          d.totalOutputTokens > 0
            ? d.totalOutputTokens
            : (prevTokens?.total_output ?? prevTokens?.output ?? 0);
        // Prefer Go's total cost when available; fall back to TS-accumulated value.
        // Go may send 0 when a pipeline is budget-terminated before stage cost is
        // calculated, which would otherwise overwrite the cost accumulated from
        // stage.complete IPC events. @see Issue #2777
        const estimatedCostUsd =
          d.totalCostUSD > 0 ? d.totalCostUSD : (prevTokens?.estimated_cost_usd ?? 0);

        this._lastState.tokens = {
          input: totalInput,
          output: totalOutput,
          total_input: totalInput,
          total_output: totalOutput,
          total_cache_read: prevTokens?.total_cache_read ?? 0,
          total_cache_creation: prevTokens?.total_cache_creation ?? 0,
          estimated_cost_usd: estimatedCostUsd,
          per_stage: perStageRecord,
          // Preserve issue-level accumulator through pipeline completion
          per_issue: prevTokens?.per_issue,
        };
        this._onStateChanged.fire(this._lastState);
      })
    );

    this.disposables.push(
      this.ipc.on("pipeline.historyRecorded", (data: unknown) => {
        const d = data as { issueNumber: number; success: boolean };
        this._onHistoryRecorded.fire(d);
      })
    );
  }
}
