/**
 * HeadlessOrchestrator - Orchestrate full pipeline execution via headless CLI
 *
 * Provides an alternative to SDK-based orchestration by running each stage
 * through the Claude CLI in headless mode. This maintains context isolation
 * while enabling full pipeline runs without requiring SDK integration.
 *
 * Each stage:
 * 1. Reads SKILL.md instructions
 * 2. Passes them as a prompt to `claude -p --output-format stream-json`
 * 3. Waits for completion (or approval gates)
 * 4. Proceeds to next stage
 *
 * @see docs/ARCHITECTURE_DIAGRAMS.md - Pipeline Execution Architecture
 * @see Issue #327 - Repository-scoped context loading
 */

import * as vscode from "vscode";
import * as fs from "fs";
import * as path from "path";
import { exec, execFile } from "child_process";
import { promisify } from "util";

// #2884: All subprocess calls in this file MUST use async variants.
// `execFileSync` / `execSync` block the VSCode extension host event loop
// for up to their timeout (typically 30s); a single rate-limited `gh`
// call cascades into the "Window is not responding" dialog. The eslint
// rule in eslint.config.js blocks re-introducing the sync variants for
// any file under packages/nightgauge-vscode/src/services/.
const execFileAsync = promisify(execFile);
import { BinaryResolver } from "./BinaryResolver";
import { IpcClient } from "./IpcClient";
import {
  isGithubRateLimitError,
  noteRateLimitOk,
  tripBreakerIfRateLimited,
} from "../utils/rateLimitCircuitBreaker";
import { isConnectivityError } from "../utils/networkOutageCircuitBreaker";
import { StageRunnerRegistry } from "../orchestrator/stages/StageRunnerRegistry";
import type { StageRunContext, StageRunResult } from "../orchestrator/stages/StageRunner";
import { SkillLoader } from "../orchestrator/skills/SkillLoader";
import {
  ContextAssembler,
  STAGE_OUTPUT_CONTEXT_TYPE,
  STAGE_OUTPUT_SCHEMA,
  STAGE_INPUT_PREREQUISITES,
  OPTIONAL_CONTEXT_STAGES,
} from "../orchestrator/context/ContextAssembler";
import { OrchestratorEventDispatcher } from "../orchestrator/events/OrchestratorEventDispatcher";

const execAsync = promisify(exec);
import {
  updateProjectItemStatus,
  ensureIssueOnProject,
  getProjectItemStatus,
  type ProjectStatusValue,
} from "../utils/projectFieldWriter";
import type { PipelineStage, IssueMetadata } from "@nightgauge/sdk";
import {
  IssueContextSchema,
  PlanningContextSchema,
  DevContextSchema,
  ValidateContextSchema,
  PRContextSchema,
  FeedbackContextSchema,
  type PipelineFeedbackSignal,
  ComplexityModelService,
  FeedbackLearningService,
  AuditEventClient,
  MissingInputFile,
  ContextSchemaError,
  StageGraph,
  loadStageGraphFromManifests,
  runAdapterAuthPreflight,
  createDefaultPreflightRunner,
  type IncrediAdapter,
  type RecoveryAction,
  type RecoveryRequiredPayload,
} from "@nightgauge/sdk";
import type { ExecutionAdapter } from "../utils/resolvers/modelResolver";
import { computeRecoveryRequired } from "../orchestrator/recovery/computeRecoveryRequired";
import { type ZodSchema, type ZodError } from "zod";
import {
  runStageSkillHeadless,
  resolveModel,
  getNextStage,
  getStageLabel,
  hasActiveProcess,
  killAllActiveProcesses,
  findSkillFile,
  type SkillRunResult,
  type SkillProcessHandle,
} from "../utils/skillRunner";
import type { PipelineStateService, PipelineOutcomeType } from "./PipelineStateService";
import type { RepositoryContextLoader, ContextFileType } from "./RepositoryContextLoader";
import type { Logger } from "../utils/logger";
import type { IssueQueueService } from "./IssueQueueService";
import type { ProjectBoardService } from "./ProjectBoardService";
import type { RoutingDecision } from "../utils/routingDecision";
import {
  makeRoutingDecision,
  buildPickupRecommendation,
  DEFAULT_ROUTING_CONFIG,
  type RoutingConfig,
} from "../utils/routingDecision";
import type { ChangeAnalysis } from "../utils/changeAnalyzer";
import {
  isRetryableApiError,
  isRateLimitError,
  calculateBackoffDelay,
  sanitizeApiError,
} from "../utils/retryHelpers";
import {
  getRetryConfig,
  getBudgetEnforcementConfig,
  getOutputTokenLimitOverrides,
  getContextBudgetConfig,
  getAlertingConfig,
  getExecutionAdapter,
  getPipelineCeilingConfig,
  getMaxBacktracks,
  getMaxEscalationsPerStage,
  getEscalatedModel,
  getAuditConfig,
  getContextSchemaRepairConfig,
  getSkipAuthPreflight,
  isAdaptiveBudgetEnabled,
  type ContextSchemaRepairConfig,
  type PipelineModelOverride,
} from "../utils/incrediConfig";
import { formatZodErrorsForPrompt } from "../utils/zodErrorFormatter";
import {
  BudgetEnforcer,
  DEFAULT_SIZE_AWARE_BUDGETS,
  resolveEffectiveSize,
  resolveStageCostUsd,
  type PlanningBudgetHint,
  type SizeAwareBudget,
  type SizeLabel,
} from "../utils/budgetEnforcer";
import { ContextBudgetEnforcer } from "../utils/contextBudgetEnforcer";
import { ARCHITECTURE_APPROVAL_REQUIRED_MARKER } from "../utils/failureComment";
import { loadAdaptiveBudgetOverrides } from "../utils/adaptiveBudgetLoader";
import type { EstimateSource } from "../utils/adaptiveBudgetLoader";
import { PipelineBudgetCeiling } from "../utils/pipelineBudgetCeiling";
import { nextBudgetActions, livePipelineCostUsd } from "../utils/budgetStreamEnforcement";
import type { ParsedTokenUsage } from "../utils/tokenParser";
import {
  runPreFlightBudgetCheck,
  captureEstimatorInputs,
  buildBudgetRetro,
  BurnRateProjector,
} from "../utils/budgetIntelligence";
import { ExecutionHistoryReader } from "../utils/executionHistoryReader";
import { analyzeChange } from "../utils/changeAnalyzer";
import type { StallEvent } from "../schemas/stallEvents";
import type { ToolCallRecord } from "../schemas/executionHistory";
import type { ExecutionHistoryRecord } from "../schemas/executionHistory";
import { PostPipelineAnalyzer, type PostPipelineAnalysisResult } from "./PostPipelineAnalyzer";
import { HealthActionService } from "./HealthActionService";
import { AutoRetroService } from "./AutoRetroService";
import { checkPipelineAlerts } from "../utils/pipelineAlertChecker";
import { sanitizeToolCallArgs } from "../utils/toolCallSanitizer";
import {
  checkCostCapTightness,
  getCostCapWarningMultiplier,
  getStageCostWarnMultiplier,
} from "../utils/resolvers/monitoringResolver";

/**
 * Defensive cap on per-stage stall event retention. A stuck stage that
 * re-enters stall detection across retries/backtracks could otherwise
 * accumulate unbounded events — we only need a representative tail for
 * the history record.
 */
const MAX_STALL_EVENTS_PER_STAGE = 50;

/**
 * First-output watchdog (#252): a stage that produces NO session output at all
 * within this window is presumed wedged (an unbounded pre-spawn await in the
 * skill-runner preamble, or a session that spawned but never streamed). Every
 * other kill gate is cost- or event-driven and structurally blind to a silent
 * stage — in the 2026-07-18 incident three runs hung 9+ hours this way. The
 * pre-spawn preamble takes seconds and a live session streams its first event
 * within seconds, so 10 minutes is generous.
 */
const STAGE_NO_OUTPUT_TIMEOUT_MS = 10 * 60 * 1000;

/**
 * Ceilings on the post-loop completion path's network-bound awaits (#253).
 * reconcileCompletionSideEffects / diagnosePrMergeBlocker call `gh` with no
 * timeout of their own; a wedge there stranded run #236 for 8.5 hours with no
 * terminal record. Degrading (skip closure sync / null diagnosis) is always
 * better than never completing the run.
 */
const COMPLETION_RECONCILE_TIMEOUT_MS = 240 * 1000;
const PR_MERGE_DIAGNOSIS_TIMEOUT_MS = 120 * 1000;

/**
 * Reject `promise` if it hasn't settled within `timeoutMs`. The underlying
 * operation is NOT cancelled (there is no generic way to abort a wedged `gh`
 * exec from here) — the caller degrades and moves on, which is the point:
 * completing the run in a degraded mode beats stranding it forever (#253).
 */
function promiseWithTimeout<T>(promise: Promise<T>, timeoutMs: number, label: string): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error(`${label} timed out after ${Math.round(timeoutMs / 1000)}s`)),
      timeoutMs
    );
    promise.then(
      (value) => {
        clearTimeout(timer);
        resolve(value);
      },
      (err) => {
        clearTimeout(timer);
        reject(err);
      }
    );
  });
}

/**
 * Orphan threshold for `pendingToolCalls` entries — tool_use blocks that
 * never received a matching tool_result. Anything older than this at the
 * end of a stage is swept out of the map so it doesn't leak into the next
 * stage.
 */
const PENDING_TOOL_CALL_MAX_AGE_MS = 15 * 60 * 1000; // 15 minutes
import {
  PhaseTimeoutManager,
  DEFAULT_PHASE_TIMEOUT_CONFIG,
  type PhaseTimeoutEvent,
  type PhaseStaleEvent,
} from "../utils/PhaseTimeoutManager";
import { TelemetryService } from "./TelemetryService";

/**
 * Result of a full pipeline run
 */
/**
 * First-class blocked terminal state (#190): the run ended with the PR
 * unmerged because of a blocker no retry can clear (repo-config: branch
 * protection, required-check config mismatch). Distinct from success and
 * from crash — carries the classification, the human remediation, and the
 * PR ref so the operator can never mistake the run for complete.
 */
export interface BlockedTerminalState {
  /** Blocker classification, e.g. "repo-config: required-check-config-mismatch:Sentry Smoke" */
  blocker: string;
  /** Human-readable action that unblocks the run */
  remediation?: string;
  /** The unmerged PR */
  prNumber?: number;
}

export interface PipelineRunResult {
  success: boolean;
  completedStages: PipelineStage[];
  skippedStages: PipelineStage[];
  /** Stages deferred for human action (e.g., pr-merge awaiting review) */
  deferredStages: PipelineStage[];
  failedStage?: PipelineStage;
  error?: Error;
  /** Set when the run ended blocked — PR unmerged behind a non-retryable blocker (#190) */
  blocked?: BlockedTerminalState;
  /**
   * True when pickup DEFERRED because the issue's native blockedBy
   * dependencies are still open (#189/#305). A deferral is NOT a failure:
   * `success` is false but `failedStage`/`error` are unset, and the run is
   * booked with outcome="cancelled" + outcome_type="deferred" and NO
   * terminal_failure_kind. Consumers must route this away from the failure
   * path (no failure notification, no autonomous pause) and keep the issue
   * eligible — the Go blocker-close requeue re-dispatches it later.
   */
  deferred?: boolean;
  totalDurationMs: number;
  /** Pipeline outcome classification for cost tracking (Issue #709) */
  outcomeType?: PipelineOutcomeType;
  /** Post-pipeline model routing analysis result (Issue #943) */
  analysisResult?: PostPipelineAnalysisResult | null;
  /** Active health policies applied during this run (Issue #1395) */
  activePolicies?: import("./PipelinePolicyOverrides").PipelinePolicyOverrides | null;
  /**
   * True when the pipeline failed due to budget exhaustion and the user
   * chose "Save Work & Stop". The worktree should be preserved so the
   * user can inspect or resume work.
   *
   * @see Issue #1935 - Budget-pause instead of budget-kill
   */
  budgetExceeded?: boolean;
}

/**
 * Result of a single stage run
 */
/**
 * Re-exported from StageRunner to avoid a circular import chain.
 * @see packages/nightgauge-vscode/src/orchestrator/stages/StageRunner.ts
 */
export type { StageRunResult } from "../orchestrator/stages/StageRunner";

interface CompletionReconciliationResult {
  verified: boolean;
  issueClosed: boolean;
  mergedPrNumber?: number;
  epicSweepClosed?: number;
  /** True when issue intentionally stays open (epic sub-issue deferred to epic merge). */
  epicDeferred?: boolean;
  error?: string;
}

/**
 * Callbacks for pipeline execution events
 */
/**
 * Tool call data extracted from stream-json output (Issue #639)
 */
export interface ToolCallData {
  tool: string;
  target: string;
  durationMs?: number;
  args?: Record<string, unknown>;
  result?: string;
  error?: string;
}

export interface PipelineCallbacks {
  onStageStart?: (stage: PipelineStage) => void;
  onStageComplete?: (stage: PipelineStage, result: StageRunResult) => void;
  onStageError?: (stage: PipelineStage, error: Error) => void;
  onStageSkipped?: (stage: PipelineStage, reason: string) => void;
  onStdout?: (stage: PipelineStage, data: string) => void;
  onStderr?: (stage: PipelineStage, data: string) => void;
  onApprovalRequired?: (stage: PipelineStage) => Promise<boolean>;
  onPipelineComplete?: (result: PipelineRunResult) => void;
  /** Called when a backward stage transition requires user confirmation */
  onBackwardTransitionConfirm?: (stage: PipelineStage, message: string) => Promise<boolean>;
  /** Called when routing decision is loaded after issue-pickup */
  onRoutingDecisionLoaded?: (decision: RoutingDecision) => void;
  /** Called when a tool call is detected during stage execution (Issue #639) */
  onToolCall?: (stage: PipelineStage, toolCall: ToolCallData) => void;
  /** Called when pipeline short-circuits due to early-exit signal (Issue #708) */
  onEarlyExit?: (issueNumber: number, reason: string) => void;
  /** Called when a stage completes after a stall warning was shown (Issue #797) */
  onStallWarningClear?: (stage: PipelineStage) => void;
  /** Called when a phase starts within a stage (Issue #1029) */
  onPhaseStart?: (
    stage: PipelineStage,
    phaseName: string,
    phaseIndex: number,
    totalPhases: number
  ) => void;
  /** Called when a phase completes within a stage (Issue #1029) */
  onPhaseComplete?: (
    stage: PipelineStage,
    phaseName: string,
    phaseIndex: number,
    totalPhases: number,
    durationMs: number
  ) => void;
  /** Called when a backtrack is triggered by a feedback signal (Issue #1342) */
  onBacktrackTriggered?: (record: import("../schemas/pipelineState").BacktrackRecord) => void;
  /** Called when a backtrack is blocked (limit exceeded or oscillation) (Issue #1342) */
  onBacktrackBlocked?: (
    reason: string,
    signal: import("@nightgauge/sdk").PipelineFeedbackSignal
  ) => void;
  /** Called when a model escalation is triggered (Issue #1343) */
  onModelEscalated?: (record: import("../schemas/pipelineState").ModelEscalationRecord) => void;
  /** Called when model escalation is blocked (ceiling or limit exceeded) (Issue #1343) */
  onEscalationBlocked?: (
    reason: string,
    signal: import("@nightgauge/sdk").PipelineFeedbackSignal
  ) => void;
  /** Called when a proactive model escalation is applied before a stage runs (Issue #1394) */
  onProactiveEscalation?: (
    record: import("../schemas/pipelineState").ProactiveEscalationRecord
  ) => void;
  /** Called when health-gated policies are activated at pipeline start (Issue #1395) */
  onHealthPoliciesApplied?: (
    policies: import("./PipelinePolicyOverrides").PipelinePolicyOverrides
  ) => void;
  /**
   * Called when a stage cannot start because its required input context
   * file is missing, schema-invalid, or run-state is unrecoverable. The
   * payload carries the deterministically computed action set; UI should
   * present the Recovery Dialog instead of a flat error toast (Issue #3239).
   */
  onRecoveryRequired?: (payload: import("@nightgauge/sdk").RecoveryRequiredPayload) => void;
}

/**
 * Configuration for pipeline execution
 */
export interface PipelineExecutionConfig {
  /** Whether to auto-continue between stages (default: true) */
  autoContinue?: boolean;
  /** Delay between stages in ms (default: 1000) */
  stageContinueDelay?: number;
  /** Stages that require approval before continuing */
  approvalGates?: PipelineStage[];
  /** Whether to skip validation stage (default: false) */
  skipValidation?: boolean;
  /** Routing configuration for complexity-based stage skipping */
  routing?: RoutingConfig;
  /** Force full pipeline regardless of routing (overrides routing.skip_stages) */
  forceFullPipeline?: boolean;
  /**
   * Maximum time to wait for a context file after a stage reports success.
   * Helps absorb short file-system/eventual-write races.
   * Applies to all skill stages that produce output context files.
   * @default 5000
   * @see Issue #637 - Generalized from issuePickupContextWaitMs
   */
  contextFileWaitMs?: number;
  /**
   * Whether to defer pr-merge stage (requires manual PR review before merge).
   * When false (default), pr-merge runs automatically as part of the pipeline.
   * When true, pr-merge is deferred and the user must run it manually.
   * Maps to config.yaml pr.auto_merge (inverted: auto_merge=true → deferMerge=false).
   * @default false
   * @see Issue #628 - Queue falsely pauses when pr-merge awaits review
   */
  deferMerge?: boolean;
  /**
   * Whether to force a re-run even if the issue is already closed.
   * When false (default), the pipeline halts immediately for closed issues
   * with zero AI tokens consumed. When true, the closed-issue guard is
   * bypassed for legitimate re-runs.
   * @default false
   * @see Issue #696 - Orchestrator must pre-check issue state before running pipeline
   */
  forceRerun?: boolean;
}

/**
 * Default execution configuration
 */
const DEFAULT_CONFIG: Required<PipelineExecutionConfig> = {
  autoContinue: true,
  stageContinueDelay: 200,
  approvalGates: [],
  skipValidation: false,
  routing: DEFAULT_ROUTING_CONFIG,
  forceFullPipeline: false,
  contextFileWaitMs: 5000,
  deferMerge: false,
  forceRerun: false,
};

/**
 * Pipeline stages in execution order
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
 * Skill-based stages that run through Claude CLI
 * Excludes bookend stages which are executed synchronously
 */
const SKILL_STAGES: PipelineStage[] = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
];

// STAGE_OUTPUT_CONTEXT_TYPE, STAGE_OUTPUT_SCHEMA, STAGE_INPUT_PREREQUISITES, OPTIONAL_CONTEXT_STAGES
// moved to ContextAssembler (Issue #2770 — Part 3).

/**
 * Stages that require human action and are deferred when deferMerge is enabled.
 * When deferMerge is false (default), these stages run automatically.
 * When deferMerge is true, they are not failures — they simply can't proceed
 * without manual review/approval.
 * @see Issue #628 - Queue falsely pauses when pr-merge awaits review
 */
const DEFERRABLE_STAGES: PipelineStage[] = ["pr-merge", "pipeline-finish"];

/**
 * Maps pipeline stages to project board Status field values.
 * Replaces the old STAGE_STATUS_LABELS that wrote labels.
 * @see Issue #1714 - Write project fields directly
 */
const STAGE_STATUS_VALUES: Partial<Record<PipelineStage, ProjectStatusValue>> = {
  "issue-pickup": "In progress",
  "feature-planning": "In progress",
  "feature-dev": "In progress",
  "feature-validate": "In progress",
  "pr-create": "In review",
  "pr-merge": "Done",
};

/**
 * Check if a stage is a bookend stage (synchronous, zero AI tokens)
 */
/**
 * Build the synthetic Error used when a stage is forcibly terminated by the
 * idle-stall watchdog. Preserves any upstream kill marker text (e.g.
 * `[rate-limit-quota-exhausted]` from #3386) that arrived on the original
 * skillRunner error so downstream classifiers (bootstrap/services.ts
 * terminalFailureKind regex, Go ClassifyTerminalKind fallback) can still
 * match. See Issue #3442 — pre-fix this code discarded `result.error` and
 * synthesized a generic message, which broke the global quota cooldown
 * (#3434) by routing every quota-exhausted kill into the GENERIC failure
 * branch.
 *
 * @internal Exported for testing only.
 */
export function composeStallKilledError(
  stage: string,
  durationMs: number,
  upstreamError: Error | undefined
): Error {
  const upstreamMarker = upstreamError?.message?.trim() ?? "";
  const carriesRecognizedMarker =
    upstreamMarker.length > 0 &&
    (upstreamMarker.includes("[rate-limit-quota-exhausted]") ||
      upstreamMarker.includes("rate-limit-quota-exhausted") ||
      /stream\s+idle\s+timeout/i.test(upstreamMarker));
  const stallExplanation =
    `[stall-killed] ${stage} terminated: subagent process exceeded stall kill threshold. ` +
    `The process ran for ${Math.round(durationMs / 1000)}s without completing. ` +
    `Configure pipeline.stall_kill_multiplier or pipeline.stage_hard_caps to adjust (0 to disable).`;
  return new Error(
    carriesRecognizedMarker
      ? `${stallExplanation}\nUpstream signal: ${upstreamMarker}`
      : stallExplanation
  );
}

function isBookendStage(stage: PipelineStage): boolean {
  return stage === "pipeline-start" || stage === "pipeline-finish";
}

/**
 * Detect an Anthropic usage/session/quota-limit signal in a stage error message.
 *
 * Matches the canonical `[rate-limit-quota-exhausted]` marker (skillRunner #3386)
 * and the plain session/usage-limit phrasings the CLI surfaces. Used to decide
 * whether a Fable stage should degrade to Opus (a separate Max-plan quota bucket)
 * rather than routing straight to the global quota cooldown.
 *
 * @internal Exported for testing only.
 */
export function isUsageLimitError(message: string | undefined): boolean {
  if (!message) return false;
  return (
    message.includes("rate-limit-quota-exhausted") ||
    /session limit|usage limit|reached your[^.\n]*limit|limit reached/i.test(message)
  );
}

/**
 * Render a deterministically-known pr-merge blocker into a single,
 * classifier-friendly line (#3924). A PR can be OPEN-but-unmergeable for
 * reasons that are correct to decline — a failing non-required check
 * (mergeStateStatus=UNSTABLE), a required review, or a merge conflict. The
 * pre-#3924 path discarded this and surfaced a generic "reported success but
 * not merged" alarm; naming the blocker lets the operator and the retro
 * classifier (`merge-blocked`, not `unknown`) see the real reason.
 *
 * Mirrors the reason strings the Go `prmerge.Decide()` punt path emits so
 * classification is consistent across the deterministic and IPC paths.
 */
export function describeMergeBlocker(
  mergeable: string,
  mergeStateStatus: string,
  failedChecks: Array<{ name: string; conclusion: string }>
): string {
  if (failedChecks.length > 0) {
    const names = failedChecks.map((c) => `"${c.name}"`).join(", ");
    return `blocked by failing check ${names} (mergeStateStatus=${mergeStateStatus}).`;
  }
  if (mergeable === "CONFLICTING" || mergeStateStatus === "DIRTY") {
    return `blocked by merge conflict (mergeStateStatus=${mergeStateStatus}).`;
  }
  if (mergeStateStatus === "BEHIND") {
    return `blocked by non-mergeable state: branch is BEHIND base (mergeStateStatus=${mergeStateStatus}).`;
  }
  if (mergeStateStatus === "BLOCKED") {
    return `blocked by required review or branch protection (mergeStateStatus=${mergeStateStatus}).`;
  }
  return `blocked by non-mergeable state (mergeable=${mergeable}, mergeStateStatus=${mergeStateStatus}).`;
}

/**
 * Result of reconcileBookkeepingFromDiskState — the in-memory arrays plus
 * a list of stages that were recovered from disk and a flag indicating
 * whether anything changed.
 *
 * @internal Exported for testing.
 */
export interface BookkeepingReconciliationResult {
  completedStages: PipelineStage[];
  skippedStages: PipelineStage[];
  deferredStages: PipelineStage[];
  recovered: PipelineStage[];
  changed: boolean;
}

/**
 * Reconcile in-memory pipeline bookkeeping against the authoritative state
 * stored on disk (state.json), for the runPipeline() success-classification
 * path. Returns updated arrays that include any stages that completed/skipped/
 * deferred on disk but were missed by the in-memory pushes.
 *
 * The in-memory `completedStages`/`skippedStages`/`deferredStages` arrays are
 * built by pushes inside the runPipeline() stage loop. The loop has many
 * early-exit paths (abort signal, isPaused trip after pr-merge, post-merge
 * verify, backtrack, approval gates, deferral) and several of those drop the
 * pipeline out before pipeline-finish runs without ever setting `failedStage`.
 *
 * Without reconciliation, the post-loop `pipelineComplete` evaluation
 * (`!failedStage && (completed + skipped + deferred).length === STAGE_ORDER.length`)
 * yields `false` even when the pipeline actually finished — producing an
 * impossible `{ success: false, failedStage: undefined }` return that
 * ConcurrentPipelineManager.processSlot routes through haltQueueOnSlotFailure
 * as "failed at unknown" and that the autonomous scheduler counts toward the
 * lifetime failure cap.
 *
 * This helper trusts state.json as the source of truth for stage completion.
 * It NEVER overrides a real failure — callers must skip this when
 * `failedStage !== undefined`. If state.json doesn't show every stage
 * terminal, no reconciliation happens (changed=false).
 *
 * @see Issue #3450 — $21.26 successful run misclassified as "failed at unknown"
 */
export function reconcileBookkeepingFromDiskState(
  inMemory: {
    completedStages: PipelineStage[];
    skippedStages: PipelineStage[];
    deferredStages: PipelineStage[];
  },
  stagesOnDisk: Record<string, { status?: string }>,
  stageOrder: PipelineStage[]
): BookkeepingReconciliationResult {
  const isTerminal = (s: { status?: string } | undefined) =>
    s != null && (s.status === "complete" || s.status === "skipped" || s.status === "deferred");

  // Only reconcile when state.json reports every canonical stage terminal —
  // matches ExecutionHistoryWriter.buildRunRecord's outcome="complete"
  // condition (defense-in-depth, see #2994). If even one stage is still
  // running/pending/failed on disk, fall through with no changes so the
  // existing failure/pause/abort handling stays authoritative.
  const allTerminalOnDisk = stageOrder.every((stage) => isTerminal(stagesOnDisk[stage]));
  if (!allTerminalOnDisk) {
    return {
      completedStages: inMemory.completedStages,
      skippedStages: inMemory.skippedStages,
      deferredStages: inMemory.deferredStages,
      recovered: [],
      changed: false,
    };
  }

  // Mutable copies — never mutate the caller's arrays directly so the helper
  // is side-effect free and trivially testable.
  const completedStages = [...inMemory.completedStages];
  const skippedStages = [...inMemory.skippedStages];
  const deferredStages = [...inMemory.deferredStages];

  const inMemorySet = new Set<PipelineStage>([
    ...completedStages,
    ...skippedStages,
    ...deferredStages,
  ]);

  const recovered: PipelineStage[] = [];
  for (const stage of stageOrder) {
    if (inMemorySet.has(stage)) continue;
    const onDisk = stagesOnDisk[stage];
    if (onDisk?.status === "complete") {
      completedStages.push(stage);
      recovered.push(stage);
    } else if (onDisk?.status === "skipped") {
      skippedStages.push(stage);
      recovered.push(stage);
    } else if (onDisk?.status === "deferred") {
      deferredStages.push(stage);
      recovered.push(stage);
    }
    // No else: isTerminal guard above already rejected non-terminal stages.
  }

  return {
    completedStages,
    skippedStages,
    deferredStages,
    recovered,
    changed: recovered.length > 0,
  };
}

/**
 * HeadlessOrchestrator - Orchestrate pipeline execution via headless CLI
 *
 * @example
 * ```typescript
 * const orchestrator = new HeadlessOrchestrator(stateService, logger);
 *
 * // Run full pipeline
 * const result = await orchestrator.runPipeline(42, {
 *   onStageComplete: (stage, result) => console.log(`${stage} done`),
 *   onApprovalRequired: async (stage) => {
 *     // Show approval dialog and return result
 *     return await showApprovalDialog();
 *   },
 * });
 *
 * // Or run a single stage
 * const stageResult = await orchestrator.runStage('feature-dev', 42, callbacks);
 * ```
 */

/**
 * Convert a VSCode `ExecutionAdapter` into the SDK `IncrediAdapter` form.
 *
 * Centralized so the per-stage resolver landing in epic #3212 (B2) can swap
 * its single call site without touching the auth pre-flight.
 *
 * @see Issue #3222 - validateAdapterAuth pre-flight checker per adapter
 */
export function toIncrediAdapter(
  adapter: ExecutionAdapter,
  _env: NodeJS.ProcessEnv = process.env
): IncrediAdapter {
  switch (adapter) {
    case "claude":
      // The Marketplace extension never embeds Anthropic's commercially
      // licensed Agent SDK. Claude runs through the user's separately installed
      // and authenticated CLI; direct Agent SDK mode remains available only to
      // SDK-library consumers who explicitly install the optional peer.
      return "claude-headless";
    case "codex":
      return "codex";
    case "gemini":
      return "gemini";
    case "gemini-sdk":
      return "gemini-sdk";
    case "lm-studio":
      return "lm-studio";
    case "ollama":
      return "ollama";
    case "copilot":
      return "copilot";
  }
}

/**
 * Decide whether an unattended budget/ceiling escalation should proceed
 * (Issue #3851).
 *
 * #3811's feature-dev burned $112 because the unattended auto-escalation
 * doubled the budget/ceiling UNCONDITIONALLY (gated only on the escalation
 * count). This gates it on PRODUCTIVE progress instead: the caller snapshots
 * the stage's productive-progress counter (commits / new-file writes / phase
 * markers / CI progress) at the previous escalation and passes it as
 * `lastSnapshot`; `currentProductive` is the value now.
 *
 * - `lastSnapshot < 0` → no prior escalation baseline; the FIRST escalation is
 *   always permitted (a single ceiling hit on healthy work should not be
 *   blocked).
 * - `delta > 0` → the stage made real forward progress since the last
 *   escalation; escalate (the spend is buying work).
 * - `delta <= 0` → flat productive progress = churn; do NOT escalate (stop and
 *   save work instead — the proximate fix for the $112 burn).
 *
 * @param lastSnapshot       productive-progress count at the previous escalation, or <0 if none
 * @param currentProductive  productive-progress count now (from `handle.getProductiveProgressDelta()`)
 */
export function decideProgressGatedEscalation(
  lastSnapshot: number,
  currentProductive: number
): { escalate: boolean; current: number; delta: number } {
  const current = Number.isFinite(currentProductive) ? currentProductive : 0;
  if (lastSnapshot < 0) {
    return { escalate: true, current, delta: current };
  }
  const delta = current - lastSnapshot;
  return { escalate: delta > 0, current, delta };
}

export class HeadlessOrchestrator implements vscode.Disposable {
  private isRunning = false;
  private currentStage: PipelineStage | null = null;
  private currentProcess: SkillProcessHandle | null = null;
  private abortController: AbortController | null = null;
  private disposables: vscode.Disposable[] = [];

  private approvalResolve: ((approved: boolean) => void) | null = null;
  private config: Required<PipelineExecutionConfig>;

  /**
   * Tracks stages that have already been completed in the current pipeline run.
   * Prevents duplicate "Stage completed" log emissions when a stage's onComplete
   * callback fires multiple times (e.g., during retry/recovery).
   * @see Issue #698 - Duplicate completed log emissions
   */
  private completedStageSet: Set<PipelineStage> = new Set();

  private shouldStopQueueAfterCurrent = false;

  // Routing decision for complexity-based stage skipping
  private currentRoutingDecision: RoutingDecision | null = null;

  // Cached routing telemetry for JSONL execution history (Issue #1005)
  // Set when loadRoutingDecision succeeds; cleared in the finally block.
  private cachedRoutingTelemetry: {
    complexity_score: number;
    path: string;
    skip_stages: string[];
  } | null = null;

  // Queue service for auto-start on pipeline completion
  private queueService: IssueQueueService | null = null;

  // Context loader for repository-scoped paths (Issue #327)
  private contextLoader: RepositoryContextLoader | null = null;

  // Extracted classes (Issue #2770 — Part 3 decomposition)
  private skillLoader: SkillLoader;
  private contextAssembler: ContextAssembler;
  private eventDispatcher: OrchestratorEventDispatcher;

  /**
   * Stage producer/consumer graph derived from skill manifests, used by
   * the Recovery Dialog (Issue #3239) to map a missing context file to
   * its producing stage. Lazily built on first use; falls back to the
   * in-process producer table when manifests are unreachable.
   */
  private stageGraph: StageGraph | null = null;

  // Project board service for dependency-aware operations (Issue #443)
  private projectBoardService: ProjectBoardService | null = null;

  // Default callbacks applied to queue-initiated pipeline runs
  private defaultPipelineCallbacks: PipelineCallbacks | null = null;

  // Cached issue metadata for AutoModelSelector (Issue #732)
  private cachedIssueMetadata: IssueMetadata | null = null;

  /** Whether a ceiling checkpoint signal file was written during this pipeline run (Issue #1047) */
  private ceilingCheckpointWritten = false;

  /** Whether the last pipeline run's cost exceeded the anomaly threshold (Issue #1335) */
  private _lastCostAnomalyExceeded = false;

  /** Accumulated tool call records for JSONL persistence (Issue #1004) */
  private accumulatedToolCalls: ToolCallRecord[] = [];

  /**
   * Zod schema validation errors captured per stage during the current run.
   * Populated when a context file fails validation; passed to the execution
   * history writer so SkillAmendmentDetector can surface recurring patterns.
   */
  private stageValidationErrors = new Map<
    string,
    Array<{
      path: string;
      code: string;
      message: string;
      received?: string;
      expected?: string[];
    }>
  >();

  /**
   * Tracks context schema repair attempts per stage during the current run.
   * Used for execution history recording and to prevent duplicate repairs.
   * @see Issue #2552 - Pipeline context schema self-correction
   */
  private stageRepairAttempts = new Map<
    string,
    { attempted: boolean; succeeded: boolean; attempts_count: number }
  >();

  /**
   * Stall events accumulated per stage during the current run (Issue #2652).
   * Populated from SkillRunResult.stallEvents in the onComplete callback;
   * passed to ExecutionHistoryWriter.buildRunRecord() at pipeline-finish.
   */
  private stageStallEvents = new Map<string, StallEvent[]>();

  /**
   * Execution-path decision per stage for the current run (Issue #297). The
   * deterministic-first pr-create/pr-merge hooks record whether the stage was
   * completed deterministically (`path: "deterministic"`) or fell through to
   * the LLM skill (`path: "llm"`, with the machine-readable `puntReason`).
   * Passed to ExecutionHistoryWriter.buildRunRecord() so `execution_path` +
   * `punt_reason` land on the history stage record — the TS-path counterpart of
   * Go's RecordExecutionPath / RecordStagePuntReason. Without this the legacy
   * HeadlessOrchestrator path recorded NEITHER field (the schema allowed
   * execution_path but the writer never populated it), so every worktree-mode
   * dogfood run's pr-stage decision was unobservable.
   */
  private stageExecutionPaths = new Map<
    string,
    { path: "deterministic" | "llm"; puntReason?: string }
  >();

  /**
   * True when the current runPipeline call is a resume of a previously-failed
   * pipeline run. Set at the start of runPipeline, read in runBookendStage
   * (pipeline-finish) to tag the execution history record (Issue #1261).
   */
  private currentRunIsRecovery = false;

  /** Pending tool calls awaiting result for duration/result backfill (Issue #1031) */
  private pendingToolCalls: Map<string, { index: number; startTime: number }> = new Map();

  /** Tracks whether pr-create validation retry has been attempted per issue (Issue #1139) */
  private prCreateRetryAttempted: Set<number> = new Set();

  /** Phase timeout manager for detecting stuck/stale phases (Issue #1187) */
  private phaseTimeoutManager: PhaseTimeoutManager | null = null;

  /**
   * Backtrack engine state (Issue #1342)
   * Reset at the start of each runPipeline() call and in the finally block.
   */
  private backtrackCount = 0;
  private traversedEdges: Set<string> = new Set();

  /**
   * Escalation engine state (Issue #1343)
   * Reset at the start of each runPipeline() call and in the finally block.
   */
  /** Per-stage escalation counts for current pipeline run (Issue #1343) */
  private stageEscalationCounts = new Map<PipelineStage, number>();

  /** Per-stage model overrides set by escalation (Issue #1343) or user override (Issue #1610) */
  private stageModelOverrides = new Map<
    PipelineStage,
    import("../utils/incrediConfig").DefaultModel
  >();

  /**
   * Stages that already fell back Fable → Opus after a usage-limit this run.
   * Guards the downgrade-and-retry to once per stage (a genuine account-wide
   * limit falls through to the global quota cooldown on the second hit).
   */
  private fableQuotaFallbackApplied = new Set<PipelineStage>();

  /**
   * Fable → Opus usage-limit fallbacks applied this run, in order. Mirrored into
   * pipeline_meta via setMeta() so the notifiers (Discord/Mattermost) surface the
   * graceful downgrade in real time (Issue #26). Reset at the same points as
   * {@link fableQuotaFallbackApplied}.
   */
  private fableFallbacks: Array<{ stage: string; from: string; to: string }> = [];

  /** Whether the current run has a user-initiated model override (Issue #1610) */
  private userModelOverride: PipelineModelOverride | null = null;

  /** Pending model override set via setNextRunModelOverride(), consumed by runPipeline() (Issue #1610) */
  private pendingUserModelOverride: PipelineModelOverride | null = null;

  /** Whether a proactive escalation has already been applied this run (Issue #1394) */
  private proactiveEscalationApplied = false;

  /**
   * Per-run pipeline-ceiling override (#253). Set when the operator (or the
   * unattended escalation path) confirms "Increase Ceiling & Continue".
   * Consulted by every subsequent ceiling construction/check in the run so the
   * override actually survives stage boundaries — before #253 it only muted
   * warnings for the current stage and the next stage stopped the pipeline
   * anyway. Cleared in runPipeline()'s finally.
   */
  private ceilingOverrideUsd: number | null = null;

  /** Health-gated policy overrides for current pipeline run (Issue #1395) */
  private policyRetryBudgetIncrease = 0;
  private pauseAutoRouting = false;
  /**
   * Workspace root pinned at the start of runPipeline(). Used as CWD for all
   * stages so that a repo switch mid-pipeline doesn't break the run.
   * @see Issue #1592
   */
  private pinnedWorkspaceRoot: string | undefined;
  /**
   * Worktree override — when set, this path is used as the working directory
   * instead of auto-detecting from workspace folders. Used for concurrent
   * pipeline execution where each orchestrator runs in its own git worktree.
   * @see Issue #1621 - Git worktree-based concurrent pipeline execution
   */
  private worktreeOverride: string | undefined;
  /**
   * Main repository root for writing execution history and other persistent
   * data that must survive worktree cleanup. When running in a worktree,
   * getWorkingDirectory() returns the worktree path (correct for skill
   * execution) but history must be written to the main repo so the dashboard
   * can read it.
   */
  private mainRepoRoot: string | undefined;
  /**
   * Cross-repo override — when set, all `gh issue view` calls use `--repo`
   * flag instead of relying on CWD-based repo detection. Required for
   * multi-repo pipelines where the worktree belongs to repo A but the issue
   * is in repo B (e.g., acme-mobile issues run in nightgauge
   * worktrees).
   * Format: "owner/repo" (e.g., "acme/mobile")
   */
  private repoOverride: string | undefined;
  /**
   * True when this orchestrator runs unattended (autonomous concurrent slot).
   * Gates interactive prompts into auto-resolving behavior. @see setUnattended
   */
  private unattended = false;
  /**
   * Blocked terminal state for the current run (#190). Set by the pr-merge
   * dead-end paths (non-retryable repo-config blocker, or retry exhausted
   * with the PR still OPEN); consumed by outcome classification and the
   * final PipelineRunResult so a run that did not deliver its PR can never
   * present as complete. Reset at every runPipeline() entry.
   */
  private blockedTerminalState: BlockedTerminalState | null = null;
  /**
   * Ground-truth flag (#266): set true the instant the run's PR is confirmed
   * MERGED on the forge — either by the post-merge verification gate passing or
   * by the post-merge kill override (a late runaway/stall/budget kill that
   * fired AFTER the merge already landed). Threaded into pipeline.notifyComplete
   * so the Go recording boundary honors merge ground truth and never books a
   * merged run as a phantom stall_kill failure. Reset at every runPipeline()
   * entry.
   */
  private prMergedGroundTruth = false;
  /**
   * Estimator input snapshot pinned at pipeline start (#198): calibration
   * table, labels/title, and performance mode as-of run start. Reused for
   * every estimate and post-run comparison in the run. Reset per run.
   */
  private estimatorSnapshot: import("../utils/budgetIntelligence").EstimatorInputSnapshot | null =
    null;
  private activePolicies: import("./PipelinePolicyOverrides").PipelinePolicyOverrides | null = null;

  /**
   * Audit event client for emitting structured pipeline audit events.
   * Instantiated at the start of runPipeline() when audit.enabled is true.
   * Disposed in the finally block. Null when audit is disabled.
   *
   * @see Issue #1582 - Pipeline execution audit trail emission
   */
  private auditClient: AuditEventClient | null = null;

  /**
   * Stable UUID for correlating all audit events within a single pipeline run.
   * Generated at the start of runPipeline(), cleared in finally block.
   *
   * @see Issue #1582 - Pipeline execution audit trail emission
   */
  private currentPipelineRunId: string | null = null;

  constructor(
    private stateService: PipelineStateService | null,
    private logger: Logger,
    config?: PipelineExecutionConfig
  ) {
    this.config = { ...DEFAULT_CONFIG, ...config };
    this.skillLoader = new SkillLoader(logger);
    this.contextAssembler = new ContextAssembler(
      logger,
      () => this.getWorkingDirectory(),
      null, // contextLoader set later via setContextLoader()
      this.skillLoader
    );
    this.eventDispatcher = new OrchestratorEventDispatcher(undefined, logger);
  }

  /**
   * Lazy-initialize the phase timeout manager (Issue #1187).
   * Deferred from constructor to avoid requiring vscode.EventEmitter in test mocks.
   * Returns null if EventEmitter is unavailable (test environments).
   */
  private getPhaseTimeoutManager(): PhaseTimeoutManager | null {
    if (!this.phaseTimeoutManager) {
      try {
        this.phaseTimeoutManager = new PhaseTimeoutManager(DEFAULT_PHASE_TIMEOUT_CONFIG);
        this.disposables.push(this.phaseTimeoutManager);
      } catch {
        // vscode.EventEmitter not available (test environment)
        return null;
      }
    }
    return this.phaseTimeoutManager;
  }

  /**
   * Set the queue service for auto-start on pipeline completion
   *
   * @param queueService - The IssueQueueService singleton
   */
  setQueueService(queueService: IssueQueueService): void {
    this.queueService = queueService;
  }

  /**
   * Set the context loader for repository-scoped paths
   *
   * When set, context files and working directory will be resolved from
   * the current repository context.
   *
   * @param contextLoader - The RepositoryContextLoader instance
   * @see Issue #327 - Repository-scoped context loading
   */
  setContextLoader(contextLoader: RepositoryContextLoader): void {
    this.contextLoader = contextLoader;
    this.contextAssembler.setContextLoader(contextLoader);
  }

  /**
   * Set the project board service for dependency-aware operations
   *
   * @param projectBoardService - The ProjectBoardService instance
   * @see Issue #443 - Auto-selection and Ready View Should Skip Blocked Issues
   */
  setProjectBoardService(projectBoardService: ProjectBoardService): void {
    this.projectBoardService = projectBoardService;
  }

  /**
   * Set default callbacks for queue-initiated pipeline runs
   *
   * These callbacks are merged into pipeline runs triggered by queue
   * auto-start or manual resume, providing OutputWindow integration
   * without the orchestrator needing a direct OutputWindow reference.
   */
  setDefaultPipelineCallbacks(callbacks: PipelineCallbacks): void {
    this.defaultPipelineCallbacks = callbacks;
  }

  /**
   * Set a worktree override path for concurrent pipeline execution.
   *
   * When set, this path is used as the working directory instead of
   * auto-detecting from workspace folders or context loader.
   *
   * @see Issue #1621 - Git worktree-based concurrent pipeline execution
   */
  setWorktreeOverride(worktreePath: string): void {
    this.worktreeOverride = worktreePath;
  }

  /**
   * Set the main repository root for persistent data writes (execution
   * history, retros, etc.) that must survive worktree cleanup.
   */
  setMainRepoRoot(repoRoot: string): void {
    this.mainRepoRoot = repoRoot;
  }

  /**
   * Set cross-repo override for multi-repo pipelines.
   * When set, all `gh issue view` calls include `--repo owner/repo`.
   * @param ownerSlashRepo - e.g. "acme/mobile"
   */
  setRepoOverride(ownerSlashRepo: string): void {
    this.repoOverride = ownerSlashRepo;
  }

  /**
   * Mark this orchestrator as running unattended (an autonomous concurrent
   * slot — see ConcurrentPipelineManager). In unattended mode, decisions that
   * would otherwise block on an interactive `vscode.window.showWarningMessage`
   * (notably the per-stage budget-escalation prompt) instead resolve
   * automatically — escalating up to the existing cap and surfacing every
   * step in the log + a non-blocking notification — because there is no human
   * watching the modal. Interactive single-issue runs leave this false and
   * keep the prompt. @see Issue: autonomous self-heal (acmeapp #8)
   */
  setUnattended(unattended: boolean): void {
    this.unattended = unattended;
  }

  /**
   * Return the cross-repo override slug ("owner/repo") when set, or undefined.
   * Callers (e.g. pipeline-complete refresh hooks) use this to scope GitHub
   * cache invalidation to just the pipeline's target repo — without it, the
   * extension falls back to detecting the repo from the active workspace
   * root, which may not match in multi-repo workspaces.
   */
  getRepoOverride(): string | undefined {
    return this.repoOverride;
  }

  /** Return ['--repo', override] when a repo override is set, or [] otherwise. */
  private ghRepoArgs(): string[] {
    return this.repoOverride ? ["--repo", this.repoOverride] : [];
  }

  /**
   * Single funnel for pipeline-completion: dispatches the in-process callback
   * (UI refresh, queue auto-start) and emits the terminal platform telemetry
   * (`pipeline_done`) via the run's state service. Every completion path —
   * success, early-exit (closed/quota/auth/preflight), cancellation, epic —
   * routes through here so the live Pipelines view always transitions the run
   * out of 'running'. Telemetry is fire-and-forget and never blocks the run.
   */
  private firePipelineComplete(result: PipelineRunResult): void {
    this.eventDispatcher.onPipelineComplete(result);
    // #297/#309: flatten the per-stage execution-path decisions accumulated
    // across the run into plain records for the IPC wire, so the Go
    // notifyComplete handler can stamp execution_path / punt_reason onto the
    // authoritative history stage records. The map is fully populated by the
    // time this single completion funnel fires (all stages have run); it is
    // cleared at the next runPipeline start.
    const stageExecutionPaths: Record<string, string> = {};
    const stagePuntReasons: Record<string, string> = {};
    for (const [stage, decision] of this.stageExecutionPaths) {
      stageExecutionPaths[stage] = decision.path;
      if (decision.puntReason) {
        stagePuntReasons[stage] = decision.puntReason;
      }
    }
    // Best-effort terminal telemetry — must never throw out of the completion
    // path (which runs on every exit, including failures).
    try {
      void this.stateService?.notifyPipelineComplete({
        success: result.success,
        totalDurationMs: result.totalDurationMs,
        stagesRun: result.completedStages,
        // #266: forge-confirmed merge ground truth. Lets the Go recording
        // boundary book a MERGED run as complete even if a late per-stage kill
        // reported the run failed at pr-merge.
        prMerged: this.prMergedGroundTruth,
        // #305: a blockedBy deferral is a non-failure. The Go notifyComplete
        // handler books it as outcome="cancelled" with outcome_type="deferred"
        // and NO terminal_failure_kind, instead of failed/subagent_crash.
        deferred: result.deferred ?? false,
        stageExecutionPaths,
        stagePuntReasons,
      });
    } catch {
      /* telemetry is best-effort; the run outcome is recorded regardless */
    }
  }

  /**
   * Resolve the run's target repo slug ("owner/name"). Prefers the explicit
   * cross-repo override (set for autonomous concurrent slots); otherwise reads
   * the active workspace's git remote via `gh`. Returns "" when unresolvable —
   * callers treat that as "no run-creation context" rather than emitting a
   * malformed repo. Best-effort and non-throwing.
   */
  private async resolveRunRepoSlug(): Promise<string> {
    if (this.repoOverride?.includes("/")) {
      return this.repoOverride;
    }
    try {
      const { stdout } = await execFileAsync(
        "gh",
        ["repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner"],
        {
          encoding: "utf-8" as const,
          cwd: this.pinnedWorkspaceRoot ?? this.getWorkingDirectory(),
          timeout: 15_000,
        }
      );
      const nwo = stdout.trim();
      return nwo.includes("/") ? nwo : "";
    } catch {
      return "";
    }
  }

  /**
   * Get the root directory for persistent data that must survive worktree
   * cleanup. Returns mainRepoRoot if set, otherwise falls back to
   * getWorkingDirectory().
   */
  private getPersistentRoot(): string {
    return this.mainRepoRoot ?? this.getWorkingDirectory();
  }

  /**
   * Whether the last completed pipeline run's cost exceeded the anomaly threshold.
   * Reset to false at the start of each runPipeline() call.
   * @see Issue #1335 - Cost anomaly alerting
   */
  getLastCostAnomalyExceeded(): boolean {
    return this._lastCostAnomalyExceeded;
  }

  /**
   * Get the working directory for skill execution
   *
   * Priority:
   * 1. Worktree override (for concurrent pipeline execution)
   * 2. Context loader (for multi-repo workspace support)
   * 3. First workspace folder
   * 4. process.cwd() fallback
   */
  /**
   * Auto-commit any uncommitted work in the worktree before killing a stage.
   * This preserves work-in-progress on the branch so it isn't lost when the
   * worktree is cleaned up after pipeline failure.
   *
   * @see Issue #1935 - Budget-pause: save work before stopping
   */
  private async autoCommitWorktreeWIP(issueNumber: number, stage: string): Promise<void> {
    const workDir = this.getWorkingDirectory();
    try {
      // Check if there are any changes to commit
      const { stdout: statusOut } = await execAsync("git status --porcelain", {
        cwd: workDir,
        timeout: 10_000,
      });

      if (!statusOut.trim()) {
        this.logger.info("No uncommitted work to save in worktree", {
          issueNumber,
          stage,
          workDir,
        });
        return;
      }

      // Stage all changes and commit
      await execAsync("git add -A", { cwd: workDir, timeout: 10_000 });
      const commitMsg =
        `WIP: budget-exceeded checkpoint for #${issueNumber} (${stage})\n\n` +
        `Auto-committed by pipeline budget-pause (Issue #1935).\n` +
        `This commit preserves work-in-progress that would otherwise be lost.`;
      await execFileAsync("git", ["commit", "-m", commitMsg], {
        cwd: workDir,
        timeout: 15_000,
      });

      this.logger.info("Auto-committed WIP before budget termination", {
        issueNumber,
        stage,
        workDir,
      });

      // Push to remote so partial work survives for retry (Issue #2338)
      try {
        await execAsync("git push", { cwd: workDir, timeout: 30_000 });
        this.logger.info("Pushed WIP commit to remote", {
          issueNumber,
          stage,
        });
      } catch (pushErr) {
        this.logger.warn("Failed to push WIP commit (non-fatal)", {
          issueNumber,
          stage,
          error: pushErr instanceof Error ? pushErr.message : "Unknown error",
        });
      }
    } catch (err) {
      this.logger.warn("Failed to auto-commit WIP (non-fatal)", {
        issueNumber,
        stage,
        error: err instanceof Error ? err.message : "Unknown error",
      });
    }
  }

  private getWorkingDirectory(): string {
    // Worktree override takes highest priority (Issue #1621)
    if (this.worktreeOverride) {
      return this.worktreeOverride;
    }

    if (this.contextLoader) {
      return this.contextLoader.getWorkingDirectory();
    }

    const workspaceFolders = vscode.workspace.workspaceFolders;
    return workspaceFolders?.[0]?.uri.fsPath ?? process.cwd();
  }

  /**
   * Deterministically sync GitHub label/project status for stage transitions.
   *
   * This ensures status progression is adapter-agnostic:
   * - issue-pickup      -> "In progress"
   * - feature-planning  -> "In progress" (reinforcement, idempotent)
   * - feature-dev       -> "In progress" (reinforcement)
   * - feature-validate  -> "In progress" (reinforcement)
   * - pr-create         -> "In review"
   * - pr-merge          -> "Done"
   *
   * @see Issue #1714 - Write project fields directly via GraphQL
   */
  private async syncStageStatusTransition(
    stage: PipelineStage,
    issueNumber: number
  ): Promise<void> {
    const statusValue = STAGE_STATUS_VALUES[stage];
    if (!statusValue) {
      return;
    }

    // Unit tests run without workspace folders; skip external side effects.
    if (
      !this.contextLoader &&
      (!vscode.workspace.workspaceFolders || vscode.workspace.workspaceFolders.length === 0)
    ) {
      this.logger.debug("Skipping status sync (no workspace folder)", {
        stage,
        issueNumber,
        statusValue,
      });
      return;
    }

    // Use main repo root for config resolution — worktree paths don't
    // have .nightgauge/config.yaml.
    const workspaceRoot = this.getPersistentRoot();

    // Defense-in-depth: don't downgrade a closed issue's project status.
    // The pre-check in runPipeline() should prevent this, but if a re-run
    // bypasses it (e.g., forceRerun), we still guard here. @see Issue #699
    //
    // Optimization: only run the guard on status-changing transitions
    // (issue-pickup, pr-create). Mid-pipeline stages that repeat the same
    // value (feature-planning/dev/validate all set "In progress") skip
    // this API call — the issue can't have been closed mid-pipeline.
    const isStatusChange = stage === "issue-pickup" || stage === "pr-create";
    if (isStatusChange && statusValue !== "Done") {
      try {
        const currentStatus = await getProjectItemStatus(
          issueNumber,
          workspaceRoot,
          this.logger,
          this.repoOverride
        );
        if (currentStatus === "Done") {
          this.logger.info("Skipping status sync — issue is CLOSED with Done status", {
            stage,
            issueNumber,
            statusValue,
          });
          return;
        }
      } catch (err) {
        // Non-fatal: if we can't check, proceed with the update.
        this.logger.debug("Could not pre-check issue state for status guard", {
          issueNumber,
          err: err instanceof Error ? err.message : String(err),
        });
      }
    }

    // Cross-repo identity: when the pipeline is running on an issue that
    // lives outside the workspace's primary repo (e.g. an
    // acme-platform issue resolved from nightgauge), the
    // project-board lookups must target THAT repo, not the config default.
    // Falls back to undefined for single-repo runs (preserves prior behavior).
    // @see Issue #2867
    const issueRepo = this.repoOverride;

    // On issue-pickup, ensure the issue is on the project board first.
    if (stage === "issue-pickup") {
      try {
        await ensureIssueOnProject(issueNumber, workspaceRoot, this.logger, issueRepo);
      } catch (err) {
        this.logger.warn("ensureIssueOnProject failed (non-blocking)", {
          issueNumber,
          err: err instanceof Error ? err.message : String(err),
        });
      }
    }

    // Update project board Status field via GraphQL
    try {
      const result = await updateProjectItemStatus(
        issueNumber,
        statusValue,
        workspaceRoot,
        this.logger,
        issueRepo
      );
      if (result.success) {
        this.logger.info("Synced stage status transition", {
          stage,
          issueNumber,
          statusValue,
          method: "graphql",
        });
      } else {
        this.logger.warn("Failed to sync stage status transition", {
          stage,
          issueNumber,
          statusValue,
          error: result.error,
        });
      }
    } catch (err) {
      this.logger.warn("Failed to sync stage status transition", {
        stage,
        issueNumber,
        statusValue,
        err: err instanceof Error ? err.message : String(err),
      });
    }
  }

  /**
   * Move the failed issue to "In review" on the project board so it leaves the
   * Ready tree (not eligible for re-dequeue or drag pickup) without getting
   * permanently stuck at "In progress".
   *
   * Prior behavior reverted to "Ready", which silently put failed issues back
   * in line for re-processing. That produced cross-issue conflicts when some
   * epic children completed and others failed — see Issue #2967.
   *
   * Non-blocking: failures are logged but never throw.
   * @see Issue #1115 - Stale status after pipeline failure
   * @see Issue #2967 - Failed pipelines must not silently return issues to Ready
   */
  private async markStatusInReviewOnFailure(issueNumber: number): Promise<void> {
    if (
      !this.contextLoader &&
      (!vscode.workspace.workspaceFolders || vscode.workspace.workspaceFolders.length === 0)
    ) {
      return;
    }
    // Use the main repo root (not worktree path) so config.yaml is found.
    const workspaceRoot = this.getPersistentRoot();
    const issueRepo = this.repoOverride;
    for (let attempt = 0; attempt < 2; attempt++) {
      try {
        const result = await updateProjectItemStatus(
          issueNumber,
          "In review",
          workspaceRoot,
          this.logger,
          issueRepo
        );
        if (result.success) {
          this.logger.info(
            "Moved project status to In review after failure (awaiting human review)",
            { issueNumber }
          );
          return;
        }
        this.logger.warn("Failed to move project status to In review after failure", {
          issueNumber,
          error: result.error,
          attempt,
        });
      } catch (err) {
        this.logger.warn("Failed to move project status to In review after failure", {
          issueNumber,
          err: err instanceof Error ? err.message : String(err),
          attempt,
        });
      }
      // Brief delay before retry
      if (attempt === 0) {
        await new Promise((resolve) => setTimeout(resolve, 2000));
      }
    }
  }

  /**
   * Get the path to a pipeline context file.
   *
   * Uses context loader if available for repository-scoped paths.
   *
   * @param type - The context file type (e.g., 'issue', 'planning', 'dev', 'pr')
   * @param issueNumber - The issue number
   * @returns Absolute path to the context file
   * @see Issue #637 - Generalized from getIssueContextPath()
   */
  private getContextPath(type: ContextFileType, issueNumber: number): string {
    return this.contextAssembler.getContextPath(type, issueNumber);
  }

  /**
   * Get the path to an issue context file.
   * Thin wrapper around getContextPath() for backward compatibility.
   */
  private getIssueContextPath(issueNumber: number): string {
    return this.getContextPath("issue", issueNumber);
  }

  /**
   * Wait briefly for a stage output context file to appear after the stage reports success.
   *
   * This covers races where the stage process exits before the context file is fully
   * materialized on disk. Returns immediately when the file is present.
   *
   * @param type - The context file type to wait for
   * @param issueNumber - The issue number
   * @param maxWaitMs - Maximum time to wait in milliseconds
   * @returns File path if found, null if timeout
   * @see Issue #637 - Generalized from waitForIssueContextFile()
   */
  private async waitForContextFile(
    type: ContextFileType,
    issueNumber: number,
    maxWaitMs: number
  ): Promise<string | null> {
    return this.contextAssembler.waitForContextFile(type, issueNumber, maxWaitMs);
  }

  /**
   * Validate that a stage wrote its expected output context file.
   *
   * Delegates to ContextAssembler.validateStageContextOutput() and merges
   * validation error details into this.stageValidationErrors and
   * this.stageRepairAttempts for execution history recording.
   *
   * @returns null if validation passed or not applicable; Error if validation failed
   * @see Issue #637 - Context file handoff validation
   * @see Issue #2770 - Extracted to ContextAssembler (Part 3)
   */
  private async validateStageContextOutput(
    stage: PipelineStage,
    issueNumber: number
  ): Promise<Error | null> {
    const repairConfig = getContextSchemaRepairConfig(
      this.pinnedWorkspaceRoot ?? this.getWorkingDirectory()
    );
    this.contextAssembler.setContextFileWaitMs(this.config.contextFileWaitMs);

    const result = await this.contextAssembler.validateStageContextOutput(
      stage,
      issueNumber,
      repairConfig
    );

    // Merge validation errors into HO's map for SkillAmendmentDetector
    if (result.validationErrors && result.validationErrors.length > 0) {
      this.stageValidationErrors.set(stage, [
        ...(this.stageValidationErrors.get(stage) ?? []),
        ...result.validationErrors,
      ]);
    }
    if (result.repairAttempt?.succeeded) {
      // Clear errors when repair succeeded
      this.stageValidationErrors.delete(stage);
    }

    // Merge repair attempt into HO's map
    if (result.repairAttempt) {
      this.stageRepairAttempts.set(stage, result.repairAttempt);
    }

    return result.error;
  }

  /**
   * Deterministic post-pr-merge verification gate.
   *
   * After the pr-merge skill reports success, verify that:
   * 1. The PR was actually merged (state === MERGED)
   * 2. CI checks passed (all checks concluded SUCCESS)
   *
   * This catches cases where the AI agent chose to merge despite failing CI
   * or where it reported success without actually merging. The skill is
   * AI-interpreted and can make bad decisions — this is the safety net.
   *
   * @see Issue #1819 - PRs merged with failing CI checks
   */
  private async verifyPostMergeState(
    issueNumber: number,
    attempt: number = 1,
    maxAttempts: number = 2
  ): Promise<Error | null> {
    // Issue #3266: this body is now a thin shell over the Go binary's
    // pr-merge stage gate (`nightgauge gate verify pr-merge <N>`).
    // The gate logic lives in `internal/orchestrator/gates/pr_merge_gate.go`
    // — single source of truth, used by the Go scheduler stage loop too.
    // The function's external shape is preserved exactly (`Promise<Error |
    // null>` plus a "state: OPEN" substring in the error message so the
    // call site at runPipeline() can decide to retry pr-merge).
    try {
      const prContextPath = this.getContextPath("pr", issueNumber);
      if (!fs.existsSync(prContextPath)) {
        this.logger.warn("Post-merge verification: no pr context file, skipping check", {
          issueNumber,
        });
        return null;
      }

      const prContext = JSON.parse(fs.readFileSync(prContextPath, "utf-8"));
      const prNumber = prContext.pr_number;
      if (!prNumber) {
        this.logger.warn("Post-merge verification: no PR number in context, skipping check", {
          issueNumber,
        });
        return null;
      }

      const cwd = this.pinnedWorkspaceRoot ?? this.getWorkingDirectory();

      // Resolve binary. When unavailable (developer build, fresh checkout),
      // skip the gate rather than blocking — the legacy behaviour pre-#3266
      // was to do nothing in that case and the orchestrator does not retry.
      const resolver = BinaryResolver.fromVSCode();
      const binary = await resolver.resolve();
      if (!binary) {
        this.logger.warn(
          "Post-merge verification: nightgauge binary not resolved — skipping deterministic gate",
          { issueNumber, prNumber }
        );
        return null;
      }

      // Spawn `nightgauge gate verify pr-merge <N> --workdir <cwd> --json`.
      // The binary exits 0 on passed=true and 2 on passed=false, with the
      // JSON GateResult on stdout in either case. Other exit codes mean the
      // CLI itself failed (no gate registered, IO error, etc.).
      let gateResult: {
        passed: boolean;
        reason: string;
        gate_name?: string;
        evidence?: string[];
      } | null = null;
      let binaryError: unknown = null;
      try {
        const { stdout } = await execFileAsync(
          binary,
          [
            "gate",
            "verify",
            "pr-merge",
            String(issueNumber),
            "--workdir",
            cwd,
            "--json",
            "--timeout",
            "60",
          ],
          { encoding: "utf-8", cwd, timeout: 90_000 }
        );
        gateResult = JSON.parse(stdout);
      } catch (err) {
        const e = err as { stdout?: string; code?: number };
        // Exit code 2 with stdout = gate ran and reported passed=false.
        if (e.stdout && e.code === 2) {
          try {
            gateResult = JSON.parse(e.stdout);
          } catch {
            binaryError = err;
          }
        } else {
          binaryError = err;
        }
      }

      if (!gateResult) {
        // Binary blew up entirely — escalate as unverified per the existing
        // path. The PR is already merged on GitHub by hypothesis; we just
        // can't confirm it programmatically.
        this.logger.error(
          "Post-merge verification: gate binary produced no parseable result — escalating",
          { issueNumber, prNumber, error: binaryError }
        );
        await this.escalateUnverifiedMerge(
          prNumber,
          issueNumber,
          cwd,
          binaryError ?? new Error("gate binary produced no output")
        );
        return null;
      }

      if (gateResult.passed) {
        this.logger.info("Post-merge verification passed", {
          issueNumber,
          prNumber,
          gateName: gateResult.gate_name,
        });
        return null;
      }

      // Gate failed. Extract the PR state from the evidence array (the gate
      // emits "state=OPEN" / "state=CLOSED" etc. as evidence) so we can
      // preserve the existing call-site contract that looks for "state: OPEN"
      // to decide whether to retry pr-merge.
      const evidenceJoined = (gateResult.evidence ?? []).join(" ");
      const stateMatch = evidenceJoined.match(/state=([A-Z_]+)/);
      const prState = stateMatch ? stateMatch[1] : "UNKNOWN";
      const isTerminal = attempt >= maxAttempts;

      if (isTerminal) {
        this.logger.error("Post-merge verification FAILED: PR not merged (terminal)", {
          issueNumber,
          prNumber,
          prState,
          gateReason: gateResult.reason,
          attempt,
          maxAttempts,
          isTerminal,
        });
      } else {
        this.logger.warn(
          "Post-merge verification: pr-merge exited without merging — retrying once",
          {
            issueNumber,
            prNumber,
            prState,
            gateReason: gateResult.reason,
            attempt,
            maxAttempts,
            isTerminal,
          }
        );
      }

      // Issue #3259: deterministic merge fallback. The gate confirmed the
      // PR is OPEN; if the PR is independently clean, run the merge here
      // rather than reporting failure. This still needs `gh pr view` for
      // mergeable/mergeStateStatus which the gate doesn't expose — the
      // gate only answers "is it MERGED yet".
      let blockerReason: string | undefined;
      if (isTerminal && prState === "OPEN") {
        const fb = await this.tryDeterministicMergeFallback(prNumber, issueNumber, cwd);
        if (fb.merged) {
          // The deterministic path (not the LLM) actually merged this PR —
          // record it so the history stage record's execution_path is truthful
          // (Issue #297).
          this.stageExecutionPaths.set("pr-merge", { path: "deterministic" });
          return null;
        }
        blockerReason = fb.blocker;
        // The deterministic merge declined; the LLM path is what ran. Capture
        // the machine-readable blocker as the punt reason so history/telemetry
        // answer WHY the expensive path was needed (Issue #297).
        if (blockerReason) {
          this.stageExecutionPaths.set("pr-merge", { path: "llm", puntReason: blockerReason });
        }
      }

      return new Error(
        `pr-merge reported success but PR #${prNumber} is not merged (state: ${prState}). ` +
          // #3924 — when the PR is held open by a deterministically-known
          // blocker (failing non-required check → UNSTABLE, review, conflict),
          // name it so the operator and the retro classifier see the real
          // reason instead of a generic "not merged" alarm.
          (blockerReason ? `${blockerReason} ` : "") +
          (isTerminal
            ? `Pipeline halted after ${maxAttempts} verification attempts.`
            : `The skill may have exited without actually merging. Retrying once.`)
      );
    } catch (err) {
      this.logger.warn("Post-merge verification failed with unexpected error, continuing", {
        issueNumber,
        error: err,
      });
      return null;
    }
  }

  /**
   * Classify whether an OPEN-PR merge blocker is worth an automatic pr-merge
   * retry (#185). Repo-config blockers — unresolved ruleset blockers, or a
   * required check produced by a `continue-on-error` job that is currently
   * failing (#184's config-mismatch probe) — are deterministically
   * unwinnable: re-running the stage re-derives the same dead end at full
   * token cost and ends in the same rejection. Delegates to the Go binary's
   * `pr ruleset-precheck --json` (single source of truth for ruleset state).
   *
   * Fails open: if the binary is unavailable or the probe errors, returns
   * retryable=true so genuinely transient conditions keep the legacy retry.
   */
  private async classifyMergeBlockerRetryability(
    issueNumber: number,
    cwd: string
  ): Promise<{ retryable: boolean; reason?: string }> {
    try {
      const prContextPath = this.getContextPath("pr", issueNumber);
      if (!fs.existsSync(prContextPath)) {
        return { retryable: true };
      }
      const prNumber = JSON.parse(fs.readFileSync(prContextPath, "utf-8")).pr_number;
      if (!prNumber) {
        return { retryable: true };
      }

      const resolver = BinaryResolver.fromVSCode();
      const binary = await resolver.resolve();
      if (!binary) {
        return { retryable: true };
      }

      const args = ["pr", "ruleset-precheck", String(prNumber), "--json"];
      const repoSlug = await this.resolveRunRepoSlug();
      if (repoSlug.includes("/")) {
        const [owner, repo] = repoSlug.split("/");
        args.push("--owner", owner, "--repo", repo);
      }

      const { stdout } = await execFileAsync(binary, args, {
        encoding: "utf-8",
        cwd,
        timeout: 90_000,
      });
      const precheck = JSON.parse(stdout) as {
        blockers?: string[];
        allowed_to_merge?: boolean;
        config_mismatches?: Array<{ check: string; failing?: boolean; remediation?: string }>;
      };

      const failingMismatch = (precheck.config_mismatches ?? []).find((m) => m.failing);
      if (failingMismatch) {
        return {
          retryable: false,
          reason:
            failingMismatch.remediation ??
            `required check "${failingMismatch.check}" is continue-on-error in the workflow but required by branch rules — repo config must change`,
        };
      }
      if (precheck.allowed_to_merge === false) {
        return {
          retryable: false,
          reason: `unresolved branch ruleset blockers: ${(precheck.blockers ?? []).join(", ")}`,
        };
      }
      return { retryable: true };
    } catch (err) {
      this.logger.warn(
        "Merge blocker classification failed — defaulting to retryable (legacy behavior)",
        { issueNumber, error: err }
      );
      return { retryable: true };
    }
  }

  /**
   * Read the structured blocker record from pr-{N}.json (#190). This is the
   * stage contract that replaces the agent-invented
   * `requires_manual_intervention` convention (which had zero consumers —
   * the remediation the agent computed died in the JSON file). The pr-merge
   * skill writes `blocker: { classification, remediation, non_retryable }`
   * when it hits a non-retryable merge blocker; the orchestrator reads it to
   * populate the blocked terminal state. Returns null when absent/unreadable.
   */
  private readPrBlockerRecord(
    issueNumber: number
  ): { classification?: string; remediation?: string; prNumber?: number } | null {
    try {
      const prContextPath = this.getContextPath("pr", issueNumber);
      if (!fs.existsSync(prContextPath)) {
        return null;
      }
      const prContext = JSON.parse(fs.readFileSync(prContextPath, "utf-8")) as {
        pr_number?: number;
        blocker?: { classification?: string; remediation?: string };
      };
      if (!prContext.blocker) {
        return null;
      }
      return {
        classification: prContext.blocker.classification,
        remediation: prContext.blocker.remediation,
        prNumber: prContext.pr_number,
      };
    } catch {
      return null;
    }
  }

  /**
   * Merge a PR_MERGE_RETRY signal into feedback-{N}.json so the retried
   * pr-merge stage sees attempt 1's blocker instead of re-deriving everything
   * from scratch (#185 — the retry prompt was previously rebuilt
   * byte-identical with zero feedback carryover). severity is "warning" so
   * the Go RetryEngine's backtrack evaluation (blocking-only) ignores it.
   * Best-effort: failures log and never block the retry.
   */
  private writePrMergeRetryFeedback(
    issueNumber: number,
    rationale: string,
    evidence: string[]
  ): void {
    try {
      const feedbackPath = path.join(
        path.dirname(this.getContextPath("pr", issueNumber)),
        `feedback-${issueNumber}.json`
      );
      let ctx: { schema_version?: string; issue_number?: number; signals?: unknown[] } = {};
      if (fs.existsSync(feedbackPath)) {
        try {
          ctx = JSON.parse(fs.readFileSync(feedbackPath, "utf-8"));
        } catch {
          ctx = {};
        }
      }
      const signals = Array.isArray(ctx.signals) ? ctx.signals : [];
      signals.push({
        signal_type: "PR_MERGE_RETRY",
        emitted_by_stage: "pr-merge",
        backtrack_target_stage: "pr-merge",
        rationale,
        evidence,
        severity: "warning",
      });
      fs.writeFileSync(
        feedbackPath,
        JSON.stringify(
          {
            schema_version: ctx.schema_version ?? "1.1",
            issue_number: ctx.issue_number ?? issueNumber,
            signals,
          },
          null,
          2
        )
      );
    } catch (err) {
      this.logger.warn("Failed to write pr-merge retry feedback (non-blocking)", {
        issueNumber,
        error: err,
      });
    }
  }

  /**
   * Post-condition verification for pr-create (Issue #3869). Confirms a PR
   * actually exists on GitHub after the pr-create skill reports success.
   *
   * The pr-create skill is AI-interpreted: when a push is blocked (e.g. the
   * destructive-git hook rejects a heredoc body) or an interactive prompt is
   * dismissed in autonomous mode, it can exit 0 having written a context /
   * assessment file but never actually opened a PR. The Go scheduler runs
   * `PrCreateGate` inline on a clean exit, but this legacy TS path did not —
   * so a false `success=true` slipped through (AcmeApp #42 / #3867:
   * pr-create "success" with no pr-{N}.json and no PR → pr-merge then failed →
   * autonomous paused). This is the TS-side mirror of the pr-merge gate.
   *
   * Thin shell over `nightgauge gate verify pr-create <N>` — the gate
   * logic stays single-sourced in `internal/orchestrator/gates/pr_create_gate.go`.
   * The binary exits 0 on passed=true and 2 on passed=false, emitting the JSON
   * GateResult on stdout either way.
   *
   * Returns an Error on a confirmed false success (no OPEN PR / missing pr
   * context), or null when the PR is verified OPEN or the gate cannot run
   * (binary unresolved → skip rather than block, matching verifyPostMergeState).
   */
  /**
   * Resolve the pipeline's GitHub token via the Go binary and export it to
   * process.env (GH_TOKEN + GITHUB_TOKEN) so every `gh`/`gh api` subprocess this
   * extension host spawns authenticates as the configured identity rather than
   * the machine's ambient active gh account (#3892).
   *
   * Idempotent and best-effort: if GH_TOKEN is already set we leave it; if the
   * binary or resolution fails we log debug and proceed (subprocesses fall back
   * to whatever they used before — no regression).
   */
  private async ensureGitHubTokenInProcessEnv(cwd: string): Promise<void> {
    if (process.env.GH_TOKEN && process.env.GITHUB_TOKEN) {
      return;
    }
    try {
      const binary = await BinaryResolver.fromVSCode().resolve();
      if (!binary) {
        this.logger.debug("Token export skipped — nightgauge binary not resolved");
        return;
      }
      const { stdout } = await execFileAsync(binary, ["forge", "auth", "token"], {
        encoding: "utf-8",
        cwd,
        timeout: 15_000,
      });
      const token = stdout.toString().trim();
      if (!token) {
        this.logger.debug("Token export skipped — forge auth token resolved empty");
        return;
      }
      process.env.GH_TOKEN = token;
      process.env.GITHUB_TOKEN = token;
      this.logger.info(
        "Exported pipeline GitHub token to process env for gh subprocesses (board status, gates)"
      );
    } catch (err) {
      this.logger.debug("Token export skipped — forge auth token failed", {
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }

  /**
   * Architecture-approval pre-check for feature-dev (Issue #4222). A high-impact
   * architectural decision stays human-owned: feature-dev must not implement it
   * until a human approves it (the `approved:architecture` label or an approval
   * file). The gate also runs inside feature-dev's own preflight, but a block
   * there exits the subagent without a context file — which the
   * deterministic-context fallback papered over, so the run then bled into
   * feature-validate (~$20) and surfaced as a generic `missing-implementation`
   * (#4220). Running the deterministic `approval-gate` binary HERE, before
   * feature-dev launches, halts cleanly with an actionable "awaiting approval"
   * alert and ZERO dev/validate spend.
   *
   * Deterministic (a Go binary, not the LLM) → model-independent. Fail-open: an
   * unresolved binary or an unparseable/errored result returns null (the skill's
   * inline gate and the post-validate gate remain as backstops). Returns an
   * Error carrying {@link ARCHITECTURE_APPROVAL_REQUIRED_MARKER} ONLY when the
   * gate positively reports the decision requires approval.
   */
  private async verifyArchitectureApproval(issueNumber: number): Promise<Error | null> {
    try {
      const cwd = this.pinnedWorkspaceRoot ?? this.getWorkingDirectory();
      const resolver = BinaryResolver.fromVSCode();
      const binary = await resolver.resolve();
      if (!binary) {
        this.logger.debug(
          "Architecture-approval pre-check: binary not resolved — skipping (skill inline gate remains)",
          { issueNumber }
        );
        return null;
      }

      let result: { requires_approval?: boolean; reasons?: string[] } | null = null;
      try {
        // Exit 0 → proceed (not high-impact, already approved, or gate disabled).
        const { stdout } = await execFileAsync(
          binary,
          ["approval-gate", String(issueNumber), "--workdir", cwd, "--json"],
          { encoding: "utf-8", cwd, timeout: 30_000 }
        );
        try {
          result = JSON.parse(stdout);
        } catch {
          // Exit code 0 IS the verdict (proceed) — an unparseable stdout is a
          // formatting quirk, not a binary error. Older binaries printed plain
          // text on the gate-disabled path, which this parse used to mislabel
          // as "binary error" on every run.
          return null;
        }
      } catch (err) {
        const e = err as { stdout?: string; stderr?: string; code?: number | string };
        // Exit 1 → requires approval; the JSON verdict is on stdout.
        if (e.stdout && e.code === 1) {
          try {
            result = JSON.parse(e.stdout);
          } catch {
            return null; // unparseable — fail open
          }
        } else {
          this.logger.debug("Architecture-approval pre-check: binary error — skipping", {
            issueNumber,
            code: e.code,
            stderr: typeof e.stderr === "string" ? e.stderr.slice(0, 300) : undefined,
          });
          return null; // binary/other error — fail open
        }
      }

      if (!result?.requires_approval) {
        return null;
      }

      const reasons =
        Array.isArray(result.reasons) && result.reasons.length > 0
          ? result.reasons.join("; ")
          : "high-impact architectural decision";
      return new Error(
        `${ARCHITECTURE_APPROVAL_REQUIRED_MARKER} — issue #${issueNumber} is a high-impact ` +
          `decision that must be human-approved before feature-dev implements it. Why: ${reasons}. ` +
          `This is NOT a failure and NO development or validation cost was incurred — the pipeline ` +
          `halted before implementation. To proceed: add the \`approved:architecture\` label to the ` +
          `issue (or write .nightgauge/pipeline/approval-${issueNumber}.json with ` +
          `{"approved": true}), then re-queue. To turn the gate off entirely, set ` +
          `pipeline.architecture_approval.enabled: false.`
      );
    } catch (err) {
      this.logger.debug("Architecture-approval pre-check skipped (unexpected error)", {
        issueNumber,
        error: err instanceof Error ? err.message : String(err),
      });
      return null; // fail-open
    }
  }

  /**
   * Post-condition verification for feature-validate (Issue #4220). The
   * validate skill exits 0 even on a hard-gate failure: it writes
   * `validation_status: "failed"` (+ an `errorCategory`) and leaves the code
   * uncommitted for retry rather than exiting non-zero, delegating the halt
   * decision to the orchestrator (the commit lives in feature-validate Phase 5
   * and is skipped when validation fails). This gate reads that verdict and
   * converts a failed validation into a real stage failure so the pipeline
   * does NOT advance into a doomed pr-create (no commit to push → the
   * no-commits-ahead gate fails it anyway, but only after wasting the
   * pr-create spend and, once the worktree is pruned, the uncommitted code).
   * The Go scheduler runs FeatureValidateGate inline; this is the TS mirror.
   *
   * `validation_status` is read directly from the JSON (and matched
   * case-insensitively) so this gate is unaffected by any `errorCategory`
   * enum drift — the failure signal does not depend on the strict enum.
   *
   * Fail-open: a missing / unreadable / verdict-less context returns null (the
   * pre-condition and post-create gates remain as backstops). It returns an
   * Error ONLY when it positively reads a failed verdict.
   */
  private verifyPostValidateState(issueNumber: number): Error | null {
    try {
      const cwd = this.pinnedWorkspaceRoot ?? this.getWorkingDirectory();
      const ctxPath = path.join(cwd, ".nightgauge", "pipeline", `validate-${issueNumber}.json`);
      if (!fs.existsSync(ctxPath)) {
        // No verdict to judge — the pre-condition gate handles a missing file.
        return null;
      }
      const parsed = JSON.parse(fs.readFileSync(ctxPath, "utf-8")) as {
        validation_status?: string;
        errorCategory?: string | null;
      };
      const status = (parsed.validation_status ?? "").trim().toLowerCase();
      if (status === "failed") {
        const category = parsed.errorCategory ? ` (${parsed.errorCategory})` : "";
        // Stable `[validation-failed]` marker (#326) so ClassifyTerminalKind /
        // classifyTerminalKind record the honest organic quality-gate failure
        // instead of falling through to the generic subagent_crash fallback —
        // this is feature-validate correctly catching a real implementation
        // defect, not a process crash.
        return new Error(
          `[validation-failed] feature-validate reported validation_status="failed"${category}. ` +
            `The validated code was intentionally NOT committed or pushed — the ` +
            `skill leaves it on disk for retry. Advancing to pr-create would push ` +
            `an empty branch and fail the no-commits-ahead gate. Halting at ` +
            `feature-validate so the failure is surfaced for retry/triage instead.`
        );
      }
      return null;
    } catch (err) {
      // Unreadable/invalid context — fail open (do not block on a parse error;
      // the pre-condition and post-create gates remain as backstops).
      this.logger.warn("Post-validate verification skipped — context unreadable", {
        issueNumber,
        error: err instanceof Error ? err.message : String(err),
      });
      return null;
    }
  }

  /**
   * Deterministic backstop for the Issue #1608 commit contract: feature-validate
   * owns "commit and push validated code" (its Phase 5), but that phase is an
   * LLM step and can be skipped — observed in a production autonomous run, where the
   * validate skill saw a *prior issue's* similarly-titled commit on main,
   * concluded "feature-dev already committed" (feature-dev never commits), and
   * skipped Phase 5. The branch went to pr-create with zero commits ahead of
   * base, pr-create pushed an empty branch and confabulated "already merged",
   * and the validated-but-uncommitted implementation was destroyed with the
   * worktree — a total loss of the run's spend.
   *
   * Runs ONLY after a passed validate verdict (verifyPostValidateState). Logic:
   *   1. If dev context claims no implemented files → pass through (protects
   *      already-resolved / no-op flows). Fail-open on unreadable context.
   *   2. If the branch has commits ahead of origin/<base> → contract satisfied.
   *   3. Zero commits ahead + source changes still on disk → REMEDIATE: commit
   *      the exact tree that validation approved (git add -A excluding
   *      .nightgauge/) and push (push failure non-fatal — pr-create pushes
   *      again). The tree that passed validation is the tree that ships.
   *   4. Zero commits ahead + clean tree → the implementation is gone; return
   *      an Error so the stage fails HERE with a precise message instead of
   *      two stages later as a confusing pr-create mystery.
   *
   * The Go FeatureValidateGate checks gate-metrics only and shares this gap —
   * TS-path fix first (concurrent slot pipelines run this path); Go parity is
   * a follow-up.
   */
  private async enforceValidateCommitContract(issueNumber: number): Promise<Error | null> {
    const cwd = this.pinnedWorkspaceRoot ?? this.getWorkingDirectory();
    const pipelineDir = path.join(cwd, ".nightgauge", "pipeline");

    // 1. What did feature-dev claim to implement?
    let claimedFiles: string[];
    try {
      const devPath = path.join(pipelineDir, `dev-${issueNumber}.json`);
      if (!fs.existsSync(devPath)) return null; // fail-open — nothing to judge
      const dev = JSON.parse(fs.readFileSync(devPath, "utf-8")) as {
        files_created?: unknown;
        files_modified?: unknown;
      };
      const toList = (v: unknown): string[] =>
        Array.isArray(v) ? v.filter((f): f is string => typeof f === "string") : [];
      claimedFiles = [...toList(dev.files_created), ...toList(dev.files_modified)].filter(
        (f) => !f.startsWith(".nightgauge/")
      );
    } catch {
      return null; // fail-open
    }
    if (claimedFiles.length === 0) return null;

    // 2. Base branch from issue context (default main).
    let baseBranch = "main";
    try {
      const issueCtx = JSON.parse(
        fs.readFileSync(path.join(pipelineDir, `issue-${issueNumber}.json`), "utf-8")
      ) as { base_branch?: string; title?: string };
      if (typeof issueCtx.base_branch === "string" && issueCtx.base_branch.trim()) {
        baseBranch = issueCtx.base_branch.trim();
      }
    } catch {
      // default main
    }

    // 3. Contract satisfied when the branch already has commits ahead of base.
    try {
      const { stdout } = await execFileAsync(
        "git",
        ["rev-list", "--count", `origin/${baseBranch}..HEAD`],
        { cwd, timeout: 10_000 }
      );
      if (parseInt(stdout.trim(), 10) > 0) return null;
    } catch (err) {
      // Can't determine (no origin ref, detached state) — fail open.
      this.logger.warn("Validate commit-contract check skipped — rev-list failed", {
        issueNumber,
        baseBranch,
        error: err instanceof Error ? err.message : String(err),
      });
      return null;
    }

    // 4. Zero commits ahead — is the validated tree still on disk?
    let hasSourceChanges: boolean;
    try {
      const { stdout } = await execFileAsync("git", ["status", "--porcelain"], {
        cwd,
        timeout: 10_000,
      });
      hasSourceChanges = stdout
        .split("\n")
        .some((l) => l.trim().length > 0 && !l.includes(".nightgauge"));
    } catch {
      return null; // fail-open
    }

    if (!hasSourceChanges) {
      return new Error(
        `feature-validate reported success but the commit contract (#1608) is unmet: ` +
          `the branch has no commits ahead of origin/${baseBranch} AND the working tree ` +
          `has no source changes, while the dev context lists ${claimedFiles.length} ` +
          `implemented file(s) (e.g. ${claimedFiles[0]}). The implementation was lost or ` +
          `never written — advancing to pr-create would push an empty branch. ` +
          `Re-run this issue's pipeline from feature-dev.`
      );
    }

    // 5. Remediate: commit the exact tree that validation approved, then push.
    this.logger.warn(
      "feature-validate skipped its commit-and-push phase (#1608) — committing the validated tree deterministically",
      { issueNumber, baseBranch, claimedFiles: claimedFiles.length }
    );
    try {
      await execFileAsync("git", ["add", "-A", "--", ".", ":(exclude).nightgauge"], {
        cwd,
        timeout: 15_000,
      });
      const commitMsg =
        `feat(#${issueNumber}): commit validated implementation (deterministic backstop)\n\n` +
        `feature-validate passed but skipped its commit-and-push phase (Issue #1608 ` +
        `contract). The orchestrator committed the exact tree that validation approved ` +
        `so the work is preserved and pr-create can proceed.\n\n` +
        `Refs: #${issueNumber}`;
      await execFileAsync("git", ["commit", "-m", commitMsg], { cwd, timeout: 15_000 });
      this.logger.info("Deterministically committed validated tree", { issueNumber });
    } catch (err) {
      return new Error(
        `feature-validate skipped its commit (#1608) and the deterministic backstop ` +
          `commit failed: ${err instanceof Error ? err.message : String(err)}. Halting ` +
          `before pr-create so the uncommitted work is preserved in the worktree.`
      );
    }
    try {
      await execFileAsync("git", ["push", "-u", "origin", "HEAD"], { cwd, timeout: 60_000 });
      this.logger.info("Pushed deterministic validate commit", { issueNumber });
    } catch (err) {
      // Non-fatal — pr-create's Phase 3 pushes the branch again.
      this.logger.warn("Push of deterministic validate commit failed (pr-create will retry)", {
        issueNumber,
        error: err instanceof Error ? err.message : String(err),
      });
    }
    return null;
  }

  private async verifyPostCreateState(issueNumber: number): Promise<Error | null> {
    try {
      const cwd = this.pinnedWorkspaceRoot ?? this.getWorkingDirectory();

      const resolver = BinaryResolver.fromVSCode();
      const binary = await resolver.resolve();
      if (!binary) {
        this.logger.warn(
          "Post-create verification: nightgauge binary not resolved — skipping deterministic gate",
          { issueNumber }
        );
        return null;
      }

      let gateResult: {
        passed: boolean;
        reason: string;
        gate_name?: string;
        evidence?: string[];
      } | null = null;
      let binaryError: unknown = null;
      try {
        const { stdout } = await execFileAsync(
          binary,
          [
            "gate",
            "verify",
            "pr-create",
            String(issueNumber),
            "--workdir",
            cwd,
            "--json",
            "--timeout",
            "60",
          ],
          { encoding: "utf-8", cwd, timeout: 90_000 }
        );
        gateResult = JSON.parse(stdout);
      } catch (err) {
        const e = err as { stdout?: string; code?: number };
        // Exit code 2 with stdout = gate ran and reported passed=false.
        if (e.stdout && e.code === 2) {
          try {
            gateResult = JSON.parse(e.stdout);
          } catch {
            binaryError = err;
          }
        } else {
          binaryError = err;
        }
      }

      if (!gateResult) {
        // Binary failed entirely — can't verify. Skip rather than fabricate a
        // failure (matches the post-merge path's stance for an unverifiable run).
        this.logger.error(
          "Post-create verification: gate binary produced no parseable result — skipping",
          { issueNumber, error: binaryError }
        );
        return null;
      }

      if (gateResult.passed) {
        this.logger.info("Post-create verification passed", {
          issueNumber,
          gateName: gateResult.gate_name,
        });
        return null;
      }

      // Gate failed → pr-create reported success but no OPEN PR exists.
      const reason = gateResult.reason || "no open PR found for the feature branch";

      // #3927 — deterministic create fallback. Mirror of the pr-merge fallback
      // (#3259): rather than fail the stage and depend on an LLM retry that may
      // no-op again for the same reason (a dismissed prompt in autonomous mode,
      // or the destructive-git hook rejecting a heredoc PR body), push the
      // feature branch and open the PR ourselves. The gate already confirmed
      // no PR exists; tryDeterministicCreateFallback re-checks git/gh state.
      const fallback = await this.tryDeterministicCreateFallback(issueNumber, cwd);
      if (fallback.created) {
        this.logger.info(
          "Post-create verification: deterministic create fallback opened the PR — recovering",
          { issueNumber }
        );
        // The deterministic path opened the PR, not the LLM — record it so the
        // history stage record's execution_path is truthful (Issue #297).
        this.stageExecutionPaths.set("pr-create", { path: "deterministic" });
        return null;
      }
      // The deterministic create declined; the LLM path is what ran. Capture the
      // reason as the punt reason for history/telemetry observability (#297).
      if (fallback.reason) {
        this.stageExecutionPaths.set("pr-create", { path: "llm", puntReason: fallback.reason });
      }

      this.logger.error(
        "Post-create verification FAILED: pr-create reported success but no PR exists",
        {
          issueNumber,
          gateReason: reason,
          evidence: gateResult.evidence,
          fallback: fallback.reason,
        }
      );
      return new Error(
        `pr-create reported success but no open PR exists (${reason}). ` +
          (fallback.reason
            ? `Deterministic fallback could not open one: ${fallback.reason}. `
            : "") +
          `The skill may have exited without pushing the branch or opening the PR.`
      );
    } catch (err) {
      this.logger.warn("Post-create verification failed with unexpected error, continuing", {
        issueNumber,
        error: err,
      });
      return null;
    }
  }

  /**
   * Invokes the Go binary's post-merge hook to explicitly close the merged
   * sub-issue and auto-close its parent epic if all sub-issues are now
   * closed. Mirrors `internal/orchestrator/epic.go::checkEpicCompletion` so
   * the legacy TS path and the Go-scheduler path both close parent epics on
   * the same trigger.
   *
   * Non-blocking by design: any failure logs a warning but never throws.
   * The merge itself has already succeeded by the time we get here.
   */
  private async invokePostMergeHook(issueNumber: number): Promise<void> {
    const cwd = this.pinnedWorkspaceRoot ?? this.getWorkingDirectory();
    try {
      const resolver = BinaryResolver.fromVSCode();
      const binary = await resolver.resolve();
      if (!binary) {
        this.logger.warn("Post-merge hook: binary unresolved — skipping epic auto-close", {
          issueNumber,
        });
        return;
      }

      const { stdout: nwoRaw } = await execFileAsync(
        "gh",
        ["repo", "view", "--json", "owner,name", "-q", '.owner.login + "/" + .name'],
        { cwd, encoding: "utf-8", timeout: 10_000 }
      );
      const [owner, repo] = nwoRaw.trim().split("/");
      if (!owner || !repo) {
        this.logger.warn("Post-merge hook: could not resolve owner/repo", {
          issueNumber,
          nwo: nwoRaw.trim(),
        });
        return;
      }

      const { stdout } = await execFileAsync(
        binary,
        [
          "hook",
          "post-merge",
          "--issue",
          String(issueNumber),
          "--owner",
          owner,
          "--repo",
          repo,
          "--json",
        ],
        { cwd, encoding: "utf-8", timeout: 30_000 }
      );
      this.logger.info("Post-merge hook completed", {
        issueNumber,
        result: stdout.trim(),
      });
    } catch (err) {
      this.logger.warn("Post-merge hook failed (non-blocking)", {
        issueNumber,
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }

  /**
   * Issue #3259 deterministic merge fallback. The gate confirmed the PR is
   * still OPEN despite the skill claiming success; if it's independently
   * mergeable, run the merge ourselves rather than failing the pipeline.
   *
   * Returns true when the fallback merged the PR (caller should treat
   * verification as passed); false when it was either not eligible or the
   * merge attempt itself failed (caller should fall through to the failure
   * path so the pipeline-failed label still gets applied).
   */
  private async tryDeterministicMergeFallback(
    prNumber: number,
    issueNumber: number,
    cwd: string
  ): Promise<{ merged: boolean; blocker?: string }> {
    const EC_POLL_INTERVAL_MS = 2_000;
    const EC_MAX_POLLS = 4;

    let mergeable: string;
    let mergeStateStatus: string;
    let checkConclusions: Array<{ name: string; conclusion: string }>;

    const fetchPrData = async () => {
      const { stdout } = await execFileAsync(
        "gh",
        [
          "pr",
          "view",
          String(prNumber),
          "--json",
          "state,statusCheckRollup,mergeable,mergeStateStatus",
        ],
        { encoding: "utf-8", cwd, timeout: 30_000 }
      );
      const data = JSON.parse(stdout);
      return {
        state: data.state as string,
        checks: (
          (data.statusCheckRollup ?? []) as Array<{ name?: string; conclusion?: string }>
        ).map((c) => ({ name: c.name || "unknown", conclusion: c.conclusion || "UNKNOWN" })),
        mergeable: (data.mergeable as string) || "UNKNOWN",
        mergeStateStatus: (data.mergeStateStatus as string) || "UNKNOWN",
      };
    };

    try {
      const data = await fetchPrData();
      mergeable = data.mergeable;
      mergeStateStatus = data.mergeStateStatus;
      checkConclusions = data.checks;
    } catch (fetchErr) {
      if (isGithubRateLimitError(fetchErr)) {
        await tripBreakerIfRateLimited(fetchErr, this.logger, {
          source: "deterministic merge fallback (eligibility check)",
          issueNumber,
        });
      }
      this.logger.warn(
        "Post-merge verification: deterministic merge fallback eligibility check failed — falling through",
        { issueNumber, prNumber, error: fetchErr }
      );
      return { merged: false };
    }

    const failedChecks = checkConclusions.filter(
      (c) => c.conclusion === "FAILURE" || c.conclusion === "ERROR"
    );
    const eligible =
      mergeable === "MERGEABLE" && mergeStateStatus === "CLEAN" && failedChecks.length === 0;
    if (!eligible) {
      // #3924 — the PR has a deterministically-known blocker (failing
      // non-required check → UNSTABLE, review required, conflict). Declining
      // to merge is correct; render the reason so the caller can surface it
      // (and the retro classifier can emit `merge-blocked`, not `unknown`).
      const blocker = describeMergeBlocker(mergeable, mergeStateStatus, failedChecks);
      this.logger.info("Post-merge verification: deterministic merge fallback NOT eligible", {
        issueNumber,
        prNumber,
        mergeable,
        mergeStateStatus,
        failedCheckCount: failedChecks.length,
        blocker,
      });
      return { merged: false, blocker };
    }

    this.logger.info("Post-merge verification: deterministic merge fallback triggered", {
      issueNumber,
      prNumber,
      mergeable,
      mergeStateStatus,
      checkCount: checkConclusions.length,
      reason: "skill exited without merging; PR is independently clean",
    });

    try {
      await execFileAsync("gh", ["pr", "merge", String(prNumber), "--squash", "--delete-branch"], {
        encoding: "utf-8",
        cwd,
        timeout: 60_000,
      });
    } catch (mergeErr) {
      const stderr = (mergeErr as { stderr?: string } | undefined)?.stderr ?? String(mergeErr);
      this.logger.warn(
        "Post-merge verification: deterministic merge fallback failed — falling through to existing failure path",
        { issueNumber, prNumber, stderr: stderr.slice(0, 500) }
      );
      return { merged: false };
    }

    for (let poll = 1; poll <= EC_MAX_POLLS; poll++) {
      try {
        const data = await fetchPrData();
        if (data.state === "MERGED") {
          this.logger.info("Post-merge verification: deterministic merge fallback succeeded", {
            issueNumber,
            prNumber,
            polls: poll,
          });
          return { merged: true };
        }
      } catch {
        // ignore — next poll will retry
      }
      if (poll < EC_MAX_POLLS) {
        await new Promise((r) => setTimeout(r, EC_POLL_INTERVAL_MS));
      }
    }

    this.logger.warn(
      "Deterministic merge fallback: gh pr merge accepted but PR not yet MERGED — falling through",
      { issueNumber, prNumber }
    );
    return { merged: false };
  }

  /**
   * Deterministic pr-create fallback (#3927) — the mirror of
   * tryDeterministicMergeFallback (#3259) for the create stage. When the
   * pr-create skill exits 0 but the post-create gate finds no open PR, push
   * the feature branch and open the PR ourselves rather than failing the stage
   * and depending on an LLM retry that may no-op again for the same reason (a
   * dismissed prompt in autonomous mode, or the destructive-git hook rejecting
   * a heredoc PR body).
   *
   * LAST-RESORT safety net: it opens a minimal PR (title from the issue, body
   * `Closes #N`) purely to unblock the pipeline — the rich description is the
   * pr-create stage's job. It runs `gh`/`git` via execFile (no shell, no
   * heredoc), so it cannot trip the destructive-git hook that may have blocked
   * the skill in the first place.
   *
   * Idempotent and conservative: short-circuits to created:true if an open PR
   * already exists, and refuses to act on a base/detached branch or a branch
   * with no commits ahead of the base.
   */
  private async tryDeterministicCreateFallback(
    issueNumber: number,
    cwd: string
  ): Promise<{ created: boolean; reason?: string }> {
    const repoArgs = this.repoOverride ? ["--repo", this.repoOverride] : [];

    // 1. Resolve the feature branch (current HEAD in the worktree).
    let branch: string;
    try {
      const { stdout } = await execFileAsync("git", ["rev-parse", "--abbrev-ref", "HEAD"], {
        encoding: "utf-8",
        cwd,
        timeout: 15_000,
      });
      branch = stdout.trim();
    } catch (err) {
      return {
        created: false,
        reason: `could not resolve feature branch: ${err instanceof Error ? err.message : String(err)}`,
      };
    }
    if (!branch || branch === "HEAD" || branch === "main" || branch === "master") {
      return {
        created: false,
        reason: `refusing to open a PR from base/detached branch "${branch || "?"}"`,
      };
    }

    // 2. Short-circuit if an open PR for this branch already exists (the gate
    //    may have raced, or the skill opened one after the gate ran).
    const openPrExists = async (): Promise<boolean> => {
      const { stdout } = await execFileAsync(
        "gh",
        ["pr", "list", "--head", branch, "--state", "open", "--json", "number", ...repoArgs],
        { encoding: "utf-8", cwd, timeout: 30_000 }
      );
      const prs = JSON.parse(stdout) as Array<{ number: number }>;
      return Array.isArray(prs) && prs.length > 0;
    };
    try {
      if (await openPrExists()) {
        this.logger.info("Deterministic create fallback: open PR already exists for branch", {
          issueNumber,
          branch,
        });
        return { created: true };
      }
    } catch (err) {
      if (isGithubRateLimitError(err)) {
        await tripBreakerIfRateLimited(err, this.logger, {
          source: "deterministic create fallback (pre-check)",
          issueNumber,
        });
        return { created: false, reason: "GitHub rate-limited during pre-check" };
      }
      // Non-fatal: fall through and attempt to create.
    }

    // 3. Resolve the base branch and confirm the feature branch has commits
    //    ahead of it (else there is genuinely nothing to open a PR for).
    let base = "main";
    try {
      const { stdout } = await execFileAsync(
        "gh",
        ["repo", "view", "--json", "defaultBranchRef", "-q", ".defaultBranchRef.name", ...repoArgs],
        { encoding: "utf-8", cwd, timeout: 30_000 }
      );
      if (stdout.trim()) base = stdout.trim();
    } catch {
      // keep default "main"
    }
    try {
      const { stdout } = await execFileAsync(
        "git",
        ["rev-list", "--count", `origin/${base}..HEAD`],
        { encoding: "utf-8", cwd, timeout: 15_000 }
      );
      const ahead = parseInt(stdout.trim(), 10);
      if (Number.isFinite(ahead) && ahead <= 0) {
        // Genuinely confirmed zero — not an inconclusive count (that's the
        // catch block below). Stamp the stable `[no-changes-produced]`
        // marker (#317) so ClassifyTerminalKind/classifyTerminalKind record
        // the honest "nothing to commit" kind instead of falling through to
        // subagent_crash — this is the exact shape of a human-only issue
        // (e.g. labeled `owner-action`) that correctly produced no code.
        return {
          created: false,
          reason: `[no-changes-produced] feature branch "${branch}" has no commits ahead of ${base}`,
        };
      }
      if (!Number.isFinite(ahead)) {
        return {
          created: false,
          reason: `could not parse commit count for "${branch}" vs ${base}: ${stdout.trim()}`,
        };
      }
    } catch {
      // If we cannot count (e.g. origin/base not fetched), be conservative and
      // do not fabricate a PR — surface as a non-creation with a clear reason.
      return {
        created: false,
        reason: `could not confirm "${branch}" has commits ahead of ${base}`,
      };
    }

    // 4. Push the branch.
    try {
      await execFileAsync("git", ["push", "-u", "origin", branch], {
        encoding: "utf-8",
        cwd,
        timeout: 60_000,
      });
    } catch (pushErr) {
      if (isGithubRateLimitError(pushErr)) {
        await tripBreakerIfRateLimited(pushErr, this.logger, {
          source: "deterministic create fallback (push)",
          issueNumber,
        });
      }
      const stderr = (pushErr as { stderr?: string } | undefined)?.stderr ?? String(pushErr);
      return { created: false, reason: `branch push failed: ${stderr.slice(0, 200)}` };
    }

    // 5. Open the PR. Title from the issue; minimal body with the closing
    //    keyword. Passed as execFile args (no shell, no heredoc) so the
    //    destructive-git hook that may have blocked the skill never applies.
    let title = `pr-create fallback for #${issueNumber}`;
    try {
      const { stdout } = await execFileAsync(
        "gh",
        ["issue", "view", String(issueNumber), "--json", "title", "-q", ".title", ...repoArgs],
        { encoding: "utf-8", cwd, timeout: 30_000 }
      );
      if (stdout.trim()) title = stdout.trim();
    } catch {
      // keep default title
    }
    const body =
      `Closes #${issueNumber}\n\n` +
      `_Opened by the deterministic pr-create fallback (#3927) after the pr-create stage ` +
      `exited without creating a PR. The full description is normally authored by the ` +
      `pr-create stage; this is a safety-net PR to unblock the pipeline._`;
    this.logger.info("Deterministic create fallback: opening PR", { issueNumber, branch, base });
    try {
      await execFileAsync(
        "gh",
        [
          "pr",
          "create",
          "--base",
          base,
          "--head",
          branch,
          "--title",
          title,
          "--body",
          body,
          ...repoArgs,
        ],
        { encoding: "utf-8", cwd, timeout: 60_000 }
      );
    } catch (createErr) {
      if (isGithubRateLimitError(createErr)) {
        await tripBreakerIfRateLimited(createErr, this.logger, {
          source: "deterministic create fallback (create)",
          issueNumber,
        });
      }
      const stderr = (createErr as { stderr?: string } | undefined)?.stderr ?? String(createErr);
      return { created: false, reason: `gh pr create failed: ${stderr.slice(0, 200)}` };
    }

    // 6. Confirm an open PR now exists.
    try {
      if (await openPrExists()) {
        this.logger.info("Deterministic create fallback succeeded — PR opened", {
          issueNumber,
          branch,
        });
        return { created: true };
      }
    } catch {
      // fall through
    }
    return { created: false, reason: "PR not visible after create" };
  }

  /**
   * Deterministic-FIRST execution for pr-create / pr-merge on the TS
   * (VSCode-dogfood) execution path (Issue #300).
   *
   * The autonomous / concurrent VSCode runs execute THIS orchestrator's
   * `runPipeline` — one instance per `ConcurrentPipelineManager` slot, inside
   * each issue's worktree — NOT the Go scheduler's stage loop. The scheduler's
   * deterministic-first hooks (`tryDeterministicPRCreate` /
   * `tryDeterministicPRMerge`) therefore never fired here, so every dogfood run
   * paid for an LLM pr-create + pr-merge session (#297/#299 root-cause). This
   * method invokes the SAME Go runners via `nightgauge pr-stage <create|merge>`
   * BEFORE the LLM skill, mirroring the scheduler contract exactly rather than
   * maintaining a second, divergent decision matrix / render / CI-wait in TS.
   *
   * Outcomes (mirrors the Go scheduler):
   * - created/merged → record `execution_path="deterministic"`, complete the
   *   stage, and return `handled` with a synthetic success `StageRunResult`. The
   *   caller SKIPS `runStage` (the LLM) but still flows through the normal
   *   post-success gates (verifyPostCreate/Merge, context validation, outcome
   *   recording) — so no post-success side effect is lost. ~$0 stage cost.
   * - punt → record `execution_path="llm"` + `punt_reason` and return `llm`; the
   *   caller runs the LLM skill exactly as today (no behavior regression).
   * - rate-limited → return `deferred`: DO NOT run the LLM (mirror the Go #3976
   *   semantics — re-shelling `gh` into an exhausted bucket burns tokens for a
   *   near-certain re-failure). Trips the rate-limit breaker and fails the stage.
   *
   * Fails OPEN: any binary/resolution/parse error returns `llm` so the pipeline
   * degrades to today's behavior rather than stalling.
   *
   * The worktree matters (#288): the deterministic runner reads pr-{N}.json and
   * the dev/validate context from the worktree, where those gitignored files
   * live on concurrent runs. `getWorkingDirectory()` returns the
   * `worktreeOverride` when one is set, and it is passed as `--workdir` and the
   * subprocess cwd.
   */
  private async runDeterministicPrStage(
    stage: PipelineStage,
    issueNumber: number,
    stageStartTime: number
  ): Promise<
    | { kind: "llm" }
    | { kind: "handled"; result: StageRunResult }
    | { kind: "deferred"; error: Error }
  > {
    if (stage !== "pr-create" && stage !== "pr-merge") {
      return { kind: "llm" };
    }

    // Resolve the Go binary. Unavailable → run the LLM path (safe default).
    // Record execution_path="llm" + a reason on EVERY fail-open return (#309):
    // an operator must be able to distinguish a legitimate runner punt (with a
    // DecideCreate/DecideMerge reason) from the deterministic hook never firing
    // at all. Without this the history record was silent on both.
    let binary: string | null;
    try {
      binary = await BinaryResolver.fromVSCode().resolve();
    } catch (err) {
      this.logger.warn("Deterministic pr-stage: binary unresolved — running LLM path", {
        stage,
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      this.stageExecutionPaths.set(stage, { path: "llm", puntReason: "binary-unresolved" });
      return { kind: "llm" };
    }
    if (!binary) {
      this.logger.warn("Deterministic pr-stage: binary unresolved (null) — running LLM path", {
        stage,
        issueNumber,
      });
      this.stageExecutionPaths.set(stage, { path: "llm", puntReason: "binary-unresolved" });
      return { kind: "llm" };
    }

    // Worktree-first workdir (#288). getWorkingDirectory() prefers worktreeOverride.
    const workdir = this.getWorkingDirectory();
    const repo = await this.resolveRunRepoSlug();
    if (stage === "pr-create" && !repo.includes("/")) {
      // The create runner needs owner/repo; without it, fall through to the LLM.
      this.logger.warn("Deterministic pr-create: could not resolve owner/repo — running LLM path", {
        issueNumber,
      });
      this.stageExecutionPaths.set(stage, { path: "llm", puntReason: "repo-unresolved" });
      return { kind: "llm" };
    }

    const verb = stage === "pr-create" ? "create" : "merge";
    const args = ["pr-stage", verb, String(issueNumber), "--workdir", workdir, "--json"];
    if (repo.includes("/")) {
      args.push("--repo", repo);
    }
    // pr-merge waits out in-flight CI (bounded ~15 min in the runner); give the
    // subprocess headroom above that. pr-create is fast.
    const timeoutMs = stage === "pr-merge" ? 20 * 60_000 : 3 * 60_000;

    // Signal start + mark running (runStage would do this; we skip runStage).
    // onStageStart is idempotent; this shows the spinner during the CI-wait.
    this.eventDispatcher.onStageStart(stage);
    this.currentStage = stage;
    if (this.stateService) {
      try {
        await this.stateService.startStage(stage);
      } catch (err) {
        this.logger.warn("Deterministic pr-stage: failed to mark stage running", { stage, err });
      }
    }

    let parsed: {
      stage?: string;
      path?: string;
      pr_number?: number;
      pr_url?: string;
      pr_state?: string;
      reason?: string;
      rate_limited?: boolean;
      duration_ms?: number;
    };
    try {
      const { stdout } = await execFileAsync(binary, args, {
        encoding: "utf-8" as const,
        cwd: workdir,
        timeout: timeoutMs,
      });
      parsed = JSON.parse(stdout.trim());
    } catch (err) {
      // Binary errored / timed out / emitted non-JSON → LLM fallthrough (safe).
      this.logger.warn("Deterministic pr-stage runner errored — running LLM path", {
        stage,
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      this.stageExecutionPaths.set(stage, { path: "llm", puntReason: "runner-error" });
      return { kind: "llm" };
    }

    const reason = parsed.reason ?? "unknown";

    // Rate-limited → DEFER. Never run the LLM into an exhausted bucket (#3976).
    if (parsed.rate_limited) {
      await tripBreakerIfRateLimited(
        new Error(`GitHub rate limit during deterministic ${stage}: ${reason}`),
        this.logger,
        { source: `deterministic ${stage} (#300)`, issueNumber }
      );
      const deferErr = new Error(
        `[github-quota-low] deterministic ${stage} deferred — GitHub API rate limit ` +
          `(${reason}); not running the LLM into an exhausted bucket (#3976). ` +
          `Retry after the GitHub rate-limit bucket resets.`
      );
      this.logger.warn("Deterministic pr-stage rate-limited — deferring (no LLM fallback)", {
        stage,
        issueNumber,
        reason,
      });
      if (this.stateService) {
        try {
          await this.stateService.failStage(stage, deferErr.message);
        } catch {
          // Non-critical — the caller still breaks the loop.
        }
      }
      return { kind: "deferred", error: deferErr };
    }

    const created = stage === "pr-create" && parsed.path === "created";
    const merged = stage === "pr-merge" && parsed.path === "merged";
    if (created || merged) {
      this.stageExecutionPaths.set(stage, { path: "deterministic" });
      if (this.stateService) {
        try {
          await this.stateService.completeStage(stage);
        } catch (err) {
          this.logger.warn("Deterministic pr-stage: failed to mark stage complete", { stage, err });
        }
      }
      const durationMs = Date.now() - stageStartTime;
      // Deterministic stage.completed audit — cost $0, source=deterministic
      // (mirrors the issue-pickup deterministic short-circuit).
      this.auditClient?.enqueue({
        action: "stage.completed",
        resourceType: "stage",
        resourceId: `${issueNumber}:${stage}`,
        metadata: {
          pipelineRunId: this.currentPipelineRunId,
          stage,
          issueNumber,
          model: "none",
          outcome: "success",
          executionSource: "deterministic",
          durationMs,
          timestamp: new Date().toISOString(),
        },
      });
      void this.auditClient?.flush();
      this.eventDispatcher.onStageComplete(stage, { success: true, stage, durationMs });
      this.logger.info(
        `${stage}: deterministic path ${parsed.path} — skipping LLM skill (${reason})`,
        { issueNumber, prNumber: parsed.pr_number }
      );
      return { kind: "handled", result: { success: true, stage, durationMs } };
    }

    // Punt → record execution_path=llm + punt_reason; caller runs the LLM skill.
    this.stageExecutionPaths.set(stage, { path: "llm", puntReason: reason });
    this.logger.info(`${stage}: deterministic path punted (${reason}) — running LLM skill`, {
      issueNumber,
    });
    return { kind: "llm" };
  }

  /**
   * Escalate a PR whose post-merge verification could not complete after
   * retry. The PR is already merged, so we can't roll back — but we MUST
   * surface the unverified state so a human can audit it. Failing silently
   * here was the path that hid the failed-CI merges in #2868.
   *
   * Adds the `pipeline-failed` label and posts a diagnostic comment.
   * Both operations are best-effort — if `gh` itself is rate-limited we
   * still log an error so the failure dashboard surfaces it.
   *
   * @see Issue #2869
   */
  private async escalateUnverifiedMerge(
    prNumber: number,
    issueNumber: number,
    cwd: string,
    cause: unknown
  ): Promise<void> {
    const causeMessage = cause instanceof Error ? cause.message : String(cause);
    this.logger.error(
      "Post-merge verification: gh pr view failed after retry — escalating unverified merge",
      new Error(
        `PR #${prNumber} (issue #${issueNumber}) was merged but verification could not complete: ${causeMessage}`
      )
    );

    const labelArgs = ["pr", "edit", String(prNumber), "--add-label", "pipeline-failed"];
    if (this.repoOverride) labelArgs.push("--repo", this.repoOverride);
    try {
      await execFileAsync("gh", labelArgs, { encoding: "utf-8", cwd, timeout: 30_000 });
    } catch (labelErr) {
      this.logger.warn("Failed to label PR pipeline-failed during escalation", {
        prNumber,
        issueNumber,
        error: labelErr instanceof Error ? labelErr.message : String(labelErr),
      });
    }

    const commentBody =
      `:warning: **Pipeline post-merge verification skipped.** ` +
      `Two attempts to fetch PR status via \`gh pr view\` failed; this PR was merged but its CI ` +
      `outcome could not be confirmed. Manual review recommended.\n\n` +
      `Last error: \`${causeMessage}\``;
    const commentArgs = ["pr", "comment", String(prNumber), "--body", commentBody];
    if (this.repoOverride) commentArgs.push("--repo", this.repoOverride);
    try {
      await execFileAsync("gh", commentArgs, { encoding: "utf-8", cwd, timeout: 30_000 });
    } catch (commentErr) {
      this.logger.warn("Failed to post escalation comment on PR", {
        prNumber,
        issueNumber,
        error: commentErr instanceof Error ? commentErr.message : String(commentErr),
      });
    }
  }

  /**
   * Check whether the PR associated with the given issue is in MERGED state.
   *
   * Used by the shipped-but-overbudget override (#3108): when pr-merge dies
   * for budget overrun, the PR may have already been merged out-of-band by
   * a previous push — in which case the work shipped and we should report
   * success rather than failure. Returns null when state can't be determined
   * so callers can fail-closed.
   */
  private async checkPrMergedForIssue(issueNumber: number): Promise<{ prNumber: number } | null> {
    try {
      const cwd = this.getWorkingDirectory();
      const prContextPath = path.join(cwd, ".nightgauge", "pipeline", `pr-${issueNumber}.json`);
      let prNumber: number | undefined;
      try {
        if (fs.existsSync(prContextPath)) {
          const raw = fs.readFileSync(prContextPath, "utf-8");
          const parsed = JSON.parse(raw) as { pr_number?: unknown };
          if (typeof parsed.pr_number === "number") {
            prNumber = parsed.pr_number;
          }
        }
      } catch {
        // Fall through — try gh CLI as a backup
      }

      if (!prNumber) {
        // Backup: ask gh for the PR number for the current branch
        try {
          const { stdout } = await execFileAsync(
            "gh",
            ["pr", "view", "--json", "number", "-q", ".number", ...this.ghRepoArgs()],
            { encoding: "utf-8", cwd, timeout: 15_000 }
          );
          const parsed = Number.parseInt(stdout.trim(), 10);
          if (Number.isFinite(parsed)) prNumber = parsed;
        } catch {
          return null;
        }
      }
      if (!prNumber) return null;

      const { stdout } = await execFileAsync(
        "gh",
        ["pr", "view", String(prNumber), "--json", "state", "-q", ".state", ...this.ghRepoArgs()],
        { encoding: "utf-8", cwd, timeout: 15_000 }
      );
      const state = stdout.trim().toUpperCase();
      return state === "MERGED" ? { prNumber } : null;
    } catch (err) {
      this.logger.debug("checkPrMergedForIssue: state lookup failed", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      return null;
    }
  }

  /**
   * Issue #3782: Pre-merge branch-behind guard.
   *
   * Mirrors Go's `BranchOutOfDate` recovery action but runs PRE-STAGE so no
   * LLM tokens are wasted on a BEHIND branch.
   *
   * Returns:
   *   { status: "clean" }            — branch was already up-to-date, no-op
   *   { status: "rebased" }          — branch was behind, successfully rebased
   *   { status: "conflict", files }  — true merge conflict; aborted; surface to operator
   *   { status: "error", message }   — unexpected git failure; caller falls through to skill
   */
  private async checkAndRebaseBehindBranch(
    prNumber: number,
    issueNumber: number,
    cwd: string
  ): Promise<
    | { status: "clean" | "rebased" }
    | { status: "conflict"; files: string[] }
    | { status: "error"; message: string }
  > {
    let mergeStateStatus: string;
    try {
      const { stdout } = await execFileAsync(
        "gh",
        ["pr", "view", String(prNumber), "--json", "mergeStateStatus", "-q", ".mergeStateStatus"],
        { encoding: "utf-8", cwd, timeout: 20_000 }
      );
      mergeStateStatus = stdout.trim().toUpperCase();
    } catch (err) {
      this.logger.warn("[pre-merge guard] gh pr view failed — skipping branch-behind check", {
        issueNumber,
        prNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      return { status: "error", message: err instanceof Error ? err.message : String(err) };
    }

    if (mergeStateStatus !== "BEHIND") {
      this.logger.debug("[pre-merge guard] branch not BEHIND — no-op", {
        issueNumber,
        prNumber,
        mergeStateStatus,
      });
      return { status: "clean" };
    }

    this.logger.info("[pre-merge guard] branch is BEHIND origin/main — rebasing", {
      issueNumber,
      prNumber,
    });

    try {
      await execFileAsync("git", ["fetch", "origin", "main"], {
        encoding: "utf-8",
        cwd,
        timeout: 60_000,
      });
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      this.logger.warn("[pre-merge guard] git fetch failed", { issueNumber, prNumber, err: msg });
      return { status: "error", message: `git fetch origin main: ${msg}` };
    }

    try {
      await execFileAsync("git", ["rebase", "origin/main"], {
        encoding: "utf-8",
        cwd,
        timeout: 120_000,
      });
    } catch {
      let conflictFiles: string[] = [];
      try {
        const { stdout: statusOut } = await execFileAsync(
          "git",
          ["diff", "--name-only", "--diff-filter=U"],
          { encoding: "utf-8", cwd, timeout: 10_000 }
        );
        conflictFiles = statusOut
          .trim()
          .split("\n")
          .map((f) => f.trim())
          .filter(Boolean);
      } catch {
        // Best-effort — proceed with empty list
      }
      try {
        await execFileAsync("git", ["rebase", "--abort"], {
          encoding: "utf-8",
          cwd,
          timeout: 30_000,
        });
      } catch {
        // Ignore abort errors — same as Go implementation
      }
      this.logger.warn("[pre-merge guard] rebase conflict detected — aborted", {
        issueNumber,
        prNumber,
        conflictFiles,
      });
      return { status: "conflict", files: conflictFiles };
    }

    try {
      await execFileAsync("git", ["push", "--force-with-lease"], {
        encoding: "utf-8",
        cwd,
        timeout: 60_000,
      });
    } catch (pushErr) {
      const msg = pushErr instanceof Error ? pushErr.message : String(pushErr);
      this.logger.warn("[pre-merge guard] push --force-with-lease failed", {
        issueNumber,
        prNumber,
        err: msg,
      });
      return { status: "error", message: `git push --force-with-lease: ${msg}` };
    }

    this.logger.info("[pre-merge guard] rebased and pushed — pr-merge may proceed", {
      issueNumber,
      prNumber,
    });
    return { status: "rebased" };
  }

  /**
   * Verify both that the PR for the given issue is MERGED and that the issue
   * itself is CLOSED. Used by the broadened shipped-but-overbudget override
   * (#3274): when pr-merge reports failure for any reason — budget overrun,
   * stale "Failure cleanup complete" exit text, etc. — and both deterministic
   * gates pass, we treat the run as a ship rather than a failure.
   *
   * Fail-closed: any uncertainty (gh error, rate limit, parse failure) returns
   * null and the original failure path remains intact.
   */
  private async checkPrMergedAndIssueClosed(
    issueNumber: number
  ): Promise<{ prNumber: number; issueClosed: boolean } | null> {
    const merged = await this.checkPrMergedForIssue(issueNumber);
    if (!merged) return null;

    try {
      const cwd = this.getWorkingDirectory();
      const { stdout } = await execFileAsync(
        "gh",
        [
          "issue",
          "view",
          String(issueNumber),
          "--json",
          "state",
          "-q",
          ".state",
          ...this.ghRepoArgs(),
        ],
        { encoding: "utf-8", cwd, timeout: 15_000 }
      );
      const issueClosed = stdout.trim().toUpperCase() === "CLOSED";
      return { prNumber: merged.prNumber, issueClosed };
    } catch (err) {
      this.logger.debug("checkPrMergedAndIssueClosed: issue state lookup failed", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      return null;
    }
  }

  /**
   * Deterministic check that pr-create's work product actually shipped — a PR
   * exists for this issue in an OPEN or MERGED state. Used to reclassify a
   * pr-create budget kill as recoverable (#3666): when the agent successfully
   * opened the PR before the budget enforcer fired, the work is real and
   * pr-merge should pick up from where pr-create left off rather than the
   * autonomous scheduler counting it as a hard failure.
   *
   * Reads `pr-{N}.json` for the PR number (written by pr-create Phase 4) and
   * confirms with `gh` that the PR actually exists. Returns null on any
   * uncertainty (missing file, gh error, parse failure) so callers fail-closed
   * — never reclassify a real failure as success.
   *
   * @see Issue #3666
   */
  private async checkPrCreatedForIssue(
    issueNumber: number
  ): Promise<{ prNumber: number; state: "OPEN" | "MERGED" } | null> {
    try {
      const cwd = this.getWorkingDirectory();
      const prContextPath = path.join(cwd, ".nightgauge", "pipeline", `pr-${issueNumber}.json`);
      let prNumber: number | undefined;
      try {
        if (fs.existsSync(prContextPath)) {
          const raw = fs.readFileSync(prContextPath, "utf-8");
          const parsed = JSON.parse(raw) as { pr_number?: unknown };
          if (typeof parsed.pr_number === "number") {
            prNumber = parsed.pr_number;
          }
        }
      } catch {
        // Fall through — pr-{N}.json missing or unreadable means pr-create
        // probably never reached Phase 4. That is a genuine failure, not a
        // budget false-alarm — return null.
      }
      if (!prNumber) return null;

      const { stdout } = await execFileAsync(
        "gh",
        ["pr", "view", String(prNumber), "--json", "state", "-q", ".state", ...this.ghRepoArgs()],
        { encoding: "utf-8", cwd, timeout: 15_000 }
      );
      const state = stdout.trim().toUpperCase();
      if (state === "OPEN") return { prNumber, state: "OPEN" };
      if (state === "MERGED") return { prNumber, state: "MERGED" };
      return null;
    } catch (err) {
      this.logger.debug("checkPrCreatedForIssue: PR state lookup failed", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      return null;
    }
  }

  /**
   * Deterministic post-stage diagnostic for pr-merge "completed but PR didn't
   * merge" runs (#3691).
   *
   * The pr-merge skill agent can end its Claude session cleanly (exit 0) after
   * exhausting RALPH auto-fix attempts without actually merging the PR. The
   * stage exit-record then shows `success=true` while the work was not done.
   * Pre-#3691 the reconciliation surfaced this as a generic
   * "Pipeline completed but issue #N is still open" — zero actionable detail,
   * a fresh `LifetimeIssueFailures` increment on every recurrence, and the
   * operator had no idea why it happened.
   *
   * This diagnostic interrogates GitHub directly and returns a structured
   * blocker classification so the reconciliation can emit a marker-tagged
   * error text the Go-side `ClassifyTerminalKind` routes through a
   * recoverable terminal kind, plus a rich pause-reason naming the actual
   * problem (failing checks, merge conflict, etc).
   *
   * Fail-closed: any failure to read PR state returns `null` and the original
   * generic-failure path runs — we never reclassify a real failure as
   * something recoverable based on incomplete data.
   */
  private async diagnosePrMergeBlocker(issueNumber: number): Promise<{
    prNumber: number;
    prUrl: string;
    prState: "OPEN" | "CLOSED" | "MERGED";
    mergeable: string;
    failingChecks: string[];
    blocker:
      | "ci_failures"
      | "merge_conflict"
      | "review_required"
      | "pr_closed_without_merge"
      | "agent_gave_up"
      | "unknown";
    summary: string;
  } | null> {
    try {
      const cwd = this.getWorkingDirectory();
      const prContextPath = path.join(cwd, ".nightgauge", "pipeline", `pr-${issueNumber}.json`);
      let prNumber: number | undefined;
      try {
        if (fs.existsSync(prContextPath)) {
          const raw = fs.readFileSync(prContextPath, "utf-8");
          const parsed = JSON.parse(raw) as { pr_number?: unknown };
          if (typeof parsed.pr_number === "number" && parsed.pr_number > 0) {
            prNumber = parsed.pr_number;
          }
        }
      } catch {
        // Fall through — diagnose without prNumber via gh pr view --search
      }
      if (!prNumber) {
        try {
          const { stdout } = await execFileAsync(
            "gh",
            ["pr", "view", "--json", "number", "-q", ".number", ...this.ghRepoArgs()],
            { encoding: "utf-8", cwd, timeout: 15_000 }
          );
          const parsed = Number.parseInt(stdout.trim(), 10);
          if (Number.isFinite(parsed)) prNumber = parsed;
        } catch {
          return null;
        }
      }
      if (!prNumber) return null;

      // Single gh call pulls everything we need to classify.
      const { stdout: prJson } = await execFileAsync(
        "gh",
        [
          "pr",
          "view",
          String(prNumber),
          "--json",
          "state,mergeable,url,reviewDecision,statusCheckRollup",
          ...this.ghRepoArgs(),
        ],
        { encoding: "utf-8", cwd, timeout: 15_000 }
      );
      const pr = JSON.parse(prJson) as {
        state: string;
        mergeable: string;
        url: string;
        reviewDecision?: string;
        statusCheckRollup?: Array<{
          name?: string;
          conclusion?: string;
          status?: string;
        }>;
      };
      const prState = pr.state?.toUpperCase() as "OPEN" | "CLOSED" | "MERGED";
      const mergeable = pr.mergeable ?? "UNKNOWN";
      const checks = pr.statusCheckRollup ?? [];
      const failingChecks = checks
        .filter(
          (c) =>
            (c.conclusion ?? "").toUpperCase() === "FAILURE" ||
            (c.conclusion ?? "").toUpperCase() === "TIMED_OUT" ||
            (c.conclusion ?? "").toUpperCase() === "ACTION_REQUIRED"
        )
        .map((c) => c.name ?? "(unnamed)")
        .filter(Boolean);

      // Classify the actual blocker in priority order: closed-without-merge
      // wins over merge-conflict (because the PR isn't even open), which wins
      // over ci-failures (because no amount of CI fixing helps a conflicted
      // branch), which wins over review-required.
      let blocker:
        | "ci_failures"
        | "merge_conflict"
        | "review_required"
        | "pr_closed_without_merge"
        | "agent_gave_up"
        | "unknown" = "unknown";
      let summary = "";
      if (prState === "CLOSED") {
        blocker = "pr_closed_without_merge";
        summary = `PR #${prNumber} was CLOSED without merging.`;
      } else if (prState === "MERGED") {
        // This case shouldn't reach here — the upstream
        // checkPrMergedAndIssueClosed already covers it — but classify
        // defensively so callers can short-circuit if they see it.
        blocker = "unknown";
        summary = `PR #${prNumber} actually MERGED.`;
      } else if (mergeable === "CONFLICTING") {
        blocker = "merge_conflict";
        summary = `PR #${prNumber} has unresolved merge conflicts against the base branch.`;
      } else if (failingChecks.length > 0) {
        blocker = "ci_failures";
        summary = `PR #${prNumber} has ${failingChecks.length} failing CI check(s): ${failingChecks.slice(0, 5).join(", ")}${failingChecks.length > 5 ? `, +${failingChecks.length - 5} more` : ""}.`;
      } else if (
        pr.reviewDecision === "REVIEW_REQUIRED" ||
        pr.reviewDecision === "CHANGES_REQUESTED"
      ) {
        blocker = "review_required";
        summary = `PR #${prNumber} is blocked on review (${pr.reviewDecision}).`;
      } else {
        // PR is open, mergeable, checks green, reviews ok — but the agent
        // didn't merge it anyway. Most likely the RALPH loop ran out of
        // patience or the merge call itself errored without a re-attempt.
        blocker = "agent_gave_up";
        summary = `PR #${prNumber} appears mergeable but the pr-merge agent did not complete the merge. Manual review recommended.`;
      }

      return {
        prNumber,
        prUrl: pr.url,
        prState,
        mergeable,
        failingChecks,
        blocker,
        summary,
      };
    } catch (err) {
      this.logger.debug("diagnosePrMergeBlocker: PR lookup failed", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      return null;
    }
  }

  /**
   * Deterministic auto-merge recovery for the `agent_gave_up` blocker.
   *
   * When `diagnosePrMergeBlocker` confirms a PR is genuinely mergeable
   * (state OPEN, mergeable MERGEABLE, no failing checks, no blocking review)
   * but the pr-merge skill agent ended its session without merging, the
   * correct outcome is not "pause for a human" — the work is done and the PR
   * is ready. This reproduces, on the legacy TS `runPipeline()` path, what the
   * Go scheduler's `SkillExitedWithoutMerging` recovery action does on the
   * scheduler path: re-run the deterministic merge. The extension's autonomous
   * slots run through this TS path and never round-trip the Go recovery
   * registry, so without this the mergeable PR just sat open (Issue: acmeapp
   * #8 / PR #18 — $2.88 spent, paused, then merged by hand).
   *
   * This is distinct from (and a backstop to) the in-stage #3259 fallback in
   * `verifyPostMergeState`/`tryDeterministicMergeFallback`: that path depends
   * on the Go binary's pr-merge gate resolving, and returns null (skips
   * entirely) when the binary is unavailable — at which point pr-merge is
   * treated as "passed" and sails through to this reconciliation diagnostic.
   * This net depends only on `gh` (via the already-computed diagnosis), so it
   * still recovers when the binary gate never ran (the acmeapp #8 case).
   *
   * Safety: this is ONLY ever called for the `agent_gave_up` classification,
   * which already requires MERGEABLE + CLEAN + green checks + no review block.
   * We use a plain `gh pr merge --squash` (never `--admin`) so branch
   * protection still has the final say — if anything has changed since the
   * diagnostic (a check flipped red, a new commit, a protection rule), the
   * merge errors and we return false, falling through to the pre-existing
   * pause-with-diagnostic path. We never destroy or hide a real failure.
   *
   * Returns true only when the PR is confirmed MERGED afterwards.
   */
  private async attemptDeterministicPrMerge(
    prNumber: number,
    issueNumber: number
  ): Promise<boolean> {
    const cwd = this.getWorkingDirectory();
    try {
      this.logger.info("[pr-merge auto-recovery] attempting deterministic merge", {
        prNumber,
        issueNumber,
      });
      await execFileAsync(
        "gh",
        ["pr", "merge", String(prNumber), "--squash", "--delete-branch", ...this.ghRepoArgs()],
        { encoding: "utf-8", cwd, timeout: 60_000 }
      );
    } catch (err) {
      this.logger.warn("[pr-merge auto-recovery] deterministic merge failed — falling through", {
        prNumber,
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      return false;
    }

    // Confirm the merge actually landed before reporting success — never
    // trust the merge command's exit code alone (the #3691 lesson).
    try {
      const { stdout } = await execFileAsync(
        "gh",
        ["pr", "view", String(prNumber), "--json", "state", "-q", ".state", ...this.ghRepoArgs()],
        { encoding: "utf-8", cwd, timeout: 15_000 }
      );
      if (stdout.trim().toUpperCase() !== "MERGED") {
        this.logger.warn("[pr-merge auto-recovery] merge command returned but PR not MERGED", {
          prNumber,
          state: stdout.trim(),
        });
        return false;
      }
    } catch {
      return false;
    }

    // Best-effort: if the PR body lacked a closing keyword, the linked issue
    // may still be open. Close it so the pipeline reports a clean completion.
    try {
      const { stdout: issueState } = await execFileAsync(
        "gh",
        [
          "issue",
          "view",
          String(issueNumber),
          "--json",
          "state",
          "-q",
          ".state",
          ...this.ghRepoArgs(),
        ],
        { encoding: "utf-8", cwd, timeout: 15_000 }
      );
      if (issueState.trim().toUpperCase() === "OPEN") {
        await execFileAsync(
          "gh",
          [
            "issue",
            "close",
            String(issueNumber),
            "--reason",
            "completed",
            "--comment",
            `Closed by deterministic auto-merge of PR #${prNumber} (pr-merge agent left it open; recovered automatically).`,
            ...this.ghRepoArgs(),
          ],
          { encoding: "utf-8", cwd, timeout: 15_000 }
        );
      }
    } catch (err) {
      // Non-fatal — the merge is what matters. Log and continue.
      this.logger.debug("[pr-merge auto-recovery] post-merge issue close best-effort failed", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
    }

    this.logger.info("[pr-merge auto-recovery] PR merged the agent left open", {
      prNumber,
      issueNumber,
    });
    return true;
  }

  /**
   * Detect an existing open PR for this issue's branch from a previous failed
   * pipeline run. If found, generates synthetic context files so the resume
   * logic can skip directly to pr-merge instead of re-running all stages.
   *
   * This is a safety net for when a pipeline fails at pr-merge and the
   * worktree was cleaned up (losing context files). Without this, re-queuing
   * the issue wastes ~$7+ re-running all stages when only the merge is needed.
   *
   * @see Issue #500 - pr-merge failure causes full pipeline re-run
   */
  private async detectAndRestoreExistingPr(issueNumber: number): Promise<void> {
    // Only relevant when state shows stages completed but context files missing
    if (!this.stateService) return;

    const state = await this.stateService.getState();
    if (!state) return;

    // Check if pr-create was already completed in a previous run
    const prCreateState = state.stages["pr-create"];
    if (prCreateState?.status !== "complete") return;

    // Check if the pr context file is missing (worktree was cleaned up)
    const prContextPath = this.getContextPath("pr", issueNumber);
    if (fs.existsSync(prContextPath)) return; // Already have context, resume will work

    this.logger.info(
      "Detected pr-create complete but pr context missing — checking for existing PR",
      { issueNumber }
    );

    // Use contextAssembler.generateDeterministicContext() which handles PR detection + context writing
    // (searches both open and merged PRs)
    const restored = await this.contextAssembler.generateDeterministicContext(
      "pr-create",
      issueNumber
    );
    if (restored.generated) {
      // Check if the restored context indicates the PR is already merged.
      // This happens when gh pr merge succeeded on GitHub but exited non-zero
      // due to a local git worktree conflict, causing the pipeline to think
      // pr-merge failed when it actually succeeded.
      const restoredContext = JSON.parse(fs.readFileSync(prContextPath, "utf-8"));
      if (restoredContext.status === "merged") {
        this.logger.info(
          "PR is already merged on GitHub — marking pr-merge as complete to skip re-run",
          { issueNumber, prNumber: restoredContext.pr_number }
        );
        await this.stateService.completeStage("pr-merge");
      }

      this.logger.info("Restored pr context from existing PR — resume will skip to pr-merge", {
        issueNumber,
      });

      // Also generate minimal context files for earlier stages so their
      // resume checks don't trigger re-runs. These are stub files — the
      // real data was in the destroyed worktree, but all we need is for
      // fs.existsSync() to pass in the resume check.
      const stageContextTypes: Array<{
        stage: string;
        type: string;
      }> = [
        { stage: "issue-pickup", type: "issue" },
        { stage: "feature-planning", type: "planning" },
        { stage: "feature-dev", type: "dev" },
        { stage: "feature-validate", type: "validate" },
      ];

      const wsRoot = this.getWorkingDirectory();
      for (const { stage, type } of stageContextTypes) {
        const stageState = state.stages[stage as PipelineStage];
        if (stageState?.status === "complete") {
          const contextPath = `${wsRoot}/.nightgauge/pipeline/${type}-${issueNumber}.json`;
          if (!fs.existsSync(contextPath)) {
            try {
              fs.writeFileSync(
                contextPath,
                JSON.stringify(
                  {
                    schema_version: "1.0",
                    issue_number: issueNumber,
                    _restored: true,
                    _restored_at: new Date().toISOString(),
                    _note:
                      "Stub context file restored from previous pipeline run. " +
                      "Real data was in a cleaned-up worktree.",
                  },
                  null,
                  2
                )
              );
              this.logger.debug(`Restored stub context for ${stage}`, {
                issueNumber,
                contextPath,
              });
            } catch {
              // Non-critical — resume will re-run this stage
            }
          }
        }
      }
    } else {
      this.logger.info(
        "No existing PR found (open or merged) — pipeline will re-run from earliest missing stage",
        { issueNumber }
      );
    }
  }

  /**
   * Validate that a stage's input pre-conditions are met before launching
   * the subagent. Checks that the prerequisite stage's context file exists
   * on disk and passes Zod schema validation.
   *
   * When the prerequisite stage was skipped (via routing), walks backward
   * through the prerequisite chain to find the nearest non-skipped ancestor
   * that produces a context file.
   *
   * @returns null if preconditions are met or not applicable; Error otherwise
   * @see Issue #1181 - Stage pre-condition validation
   */
  private validateStagePreconditions(
    stage: PipelineStage,
    issueNumber: number,
    skippedStages: PipelineStage[]
  ): Error | null {
    let prereq = STAGE_INPUT_PREREQUISITES[stage];
    if (!prereq) {
      return null; // No prerequisites (issue-pickup, bookends)
    }

    // Walk backward through the chain if prerequisite was skipped
    const skippedSet = new Set(skippedStages);
    while (prereq && skippedSet.has(prereq.stage)) {
      prereq = STAGE_INPUT_PREREQUISITES[prereq.stage];
    }

    if (!prereq) {
      // All ancestors were skipped — no context file to check
      return null;
    }

    const contextPath = this.getContextPath(prereq.contextType, issueNumber);

    // Check file exists
    if (!fs.existsSync(contextPath)) {
      this.logger.error(`${stage} pre-condition failed: prerequisite context file missing`, {
        stage,
        prerequisiteStage: prereq.stage,
        expectedPath: contextPath,
        issueNumber,
      });
      const missingErr = new MissingInputFile(contextPath, stage, prereq.stage);
      this.emitRecoveryRequired(missingErr, issueNumber, stage).catch(() => {
        /* dispatch errors are logged by the dispatcher */
      });
      return missingErr;
    }

    // Validate content against Zod schema
    const schema = STAGE_OUTPUT_SCHEMA[prereq.stage];
    if (schema) {
      let parsed: unknown;
      try {
        const raw = fs.readFileSync(contextPath, "utf-8");
        parsed = JSON.parse(raw);
      } catch (err) {
        this.logger.error(
          `${stage} pre-condition failed: prerequisite context file contains invalid JSON`,
          { stage, prerequisiteStage: prereq.stage, contextPath, issueNumber }
        );
        const detail = err instanceof Error ? err.message : String(err);
        const schemaErr = new ContextSchemaError(contextPath, `invalid JSON: ${detail}`);
        this.emitRecoveryRequired(schemaErr, issueNumber, stage).catch(() => {
          /* dispatch errors are logged by the dispatcher */
        });
        return schemaErr;
      }

      const result = schema.safeParse(parsed);
      if (!result.success) {
        const issues = result.error.issues
          .map((i) => `  - ${i.path.join(".")}: ${i.message}`)
          .join("\n");

        // Capture errors against the stage that WROTE this file (prereq.stage)
        const captured = result.error.issues.map((i) => ({
          path: i.path.join("."),
          code: i.code,
          message: i.message,
          received: "received" in i ? String((i as { received: unknown }).received) : undefined,
          expected: "options" in i ? (i as { options: unknown[] }).options.map(String) : undefined,
        }));
        this.stageValidationErrors.set(prereq.stage, [
          ...(this.stageValidationErrors.get(prereq.stage) ?? []),
          ...captured,
        ]);

        // Schema mismatches are warn-only — the file exists and is valid JSON.
        // LLM agents produce field-name variations that don't affect the next
        // stage's ability to read what it needs.
        this.logger.warn(
          "Prerequisite context file has schema mismatches (non-fatal, continuing)",
          {
            stage,
            prerequisiteStage: prereq.stage,
            contextPath,
            issueNumber,
            issues: `\n${issues}`,
          }
        );
      }
    }

    this.logger.info("Stage pre-condition validation passed", {
      stage,
      prerequisiteStage: prereq.stage,
      contextPath,
      issueNumber,
    });
    return null;
  }

  // ---------------------------------------------------------------------------
  // Recovery (Issue #3239)
  // ---------------------------------------------------------------------------

  /**
   * Lazily build the stage graph from skill manifests. Falls back to the
   * in-process producer table when the skills directory cannot be located.
   */
  private getStageGraph(): StageGraph {
    if (this.stageGraph) return this.stageGraph;
    try {
      const skillsDir = this.locateSkillsDir();
      if (skillsDir) {
        const fsView = {
          existsSync: (p: string) => fs.existsSync(p),
          readFileSync: (p: string) => fs.readFileSync(p, "utf-8"),
        };
        this.stageGraph = loadStageGraphFromManifests(skillsDir, fsView);
        return this.stageGraph;
      }
    } catch (err) {
      this.logger.warn("StageGraph: failed to load from manifests, using fallback", {
        error: err instanceof Error ? err.message : String(err),
      });
    }
    this.stageGraph = StageGraph.fromFallback();
    return this.stageGraph;
  }

  /**
   * Locate a `skills/` directory that contains pipeline manifests. We try
   * the workspace root first; failing that, we walk up from the
   * extension's source directory. Returns null when not found.
   */
  private locateSkillsDir(): string | null {
    const candidates: string[] = [];
    try {
      const ws = this.getWorkingDirectory();
      candidates.push(path.join(ws, "skills"));
    } catch {
      /* getWorkingDirectory may not be ready */
    }
    candidates.push(path.join(__dirname, "..", "..", "..", "..", "..", "skills"));
    for (const candidate of candidates) {
      try {
        if (fs.existsSync(path.join(candidate, "nightgauge-feature-planning", "SKILL.md"))) {
          return candidate;
        }
      } catch {
        /* probe failure — try next */
      }
    }
    return null;
  }

  /**
   * Build the on-disk run-state view used by `computeRecoveryRequired`.
   * Reads run-state.json directly so this code can run without taking a
   * dependency on PipelineStateService internals.
   */
  private readRecoveryRunStateView(issueNumber: number): {
    lifecycle: "running" | "paused" | "aborted" | "none";
    pausedContextIntact: boolean;
  } {
    const ws = this.getWorkingDirectory();
    const runStatePath = path.join(ws, ".nightgauge", "pipeline", "run-state.json");
    if (!fs.existsSync(runStatePath)) {
      return { lifecycle: "none", pausedContextIntact: false };
    }
    let parsed: {
      state?: string;
      resume_from_stage?: string;
      completed_stages?: string[];
      issue_number?: number;
    } | null;
    try {
      const raw = fs.readFileSync(runStatePath, "utf-8");
      parsed = JSON.parse(raw);
    } catch {
      return { lifecycle: "none", pausedContextIntact: false };
    }
    if (!parsed) return { lifecycle: "none", pausedContextIntact: false };
    if (parsed.issue_number !== undefined && parsed.issue_number !== issueNumber) {
      return { lifecycle: "none", pausedContextIntact: false };
    }

    const lifecycle = mapLifecycle(parsed.state);

    let pausedContextIntact = false;
    if (lifecycle === "paused" && parsed.resume_from_stage) {
      const resumeStage = parsed.resume_from_stage as PipelineStage;
      const prereq = STAGE_INPUT_PREREQUISITES[resumeStage];
      if (prereq) {
        const ctxPath = this.getContextPath(prereq.contextType, issueNumber);
        pausedContextIntact = fs.existsSync(ctxPath);
      } else {
        pausedContextIntact = true;
      }
    }

    return { lifecycle, pausedContextIntact };
  }

  /**
   * Read `resume_from_stage` from the durable run-state.json. Returns null
   * when the file is missing, malformed, belongs to a different issue, or
   * does not declare a resume target.
   */
  private readResumeFromStage(issueNumber: number): PipelineStage | null {
    const ws = this.getWorkingDirectory();
    const runStatePath = path.join(ws, ".nightgauge", "pipeline", "run-state.json");
    if (!fs.existsSync(runStatePath)) return null;
    try {
      const parsed = JSON.parse(fs.readFileSync(runStatePath, "utf-8")) as {
        resume_from_stage?: string;
        issue_number?: number;
      };
      if (parsed.issue_number !== undefined && parsed.issue_number !== issueNumber) {
        return null;
      }
      return (parsed.resume_from_stage ?? null) as PipelineStage | null;
    } catch {
      return null;
    }
  }

  /**
   * Build a `RecoveryRequiredPayload` and dispatch it. Suppresses errors
   * from the dispatcher; the caller still receives the original error and
   * is expected to surface it via the existing flat-error path when no
   * recovery callback is wired.
   */
  private async emitRecoveryRequired(
    error: unknown,
    issueNumber: number,
    triggeringStage: string
  ): Promise<RecoveryRequiredPayload | null> {
    try {
      const view = this.readRecoveryRunStateView(issueNumber);
      const graph = this.getStageGraph();
      const payload = computeRecoveryRequired(error, issueNumber, triggeringStage, view, graph);
      if (!payload) return null;
      this.eventDispatcher.onRecoveryRequired(payload);
      return payload;
    } catch (err) {
      this.logger.warn("emitRecoveryRequired failed", {
        error: err instanceof Error ? err.message : String(err),
      });
      return null;
    }
  }

  /**
   * Public accessor: probe the most recent recovery shape for a stage so
   * the retryStage command can decide whether to route to the dialog.
   * Returns null when no recovery is needed.
   */
  getRecoveryShape(
    error: unknown,
    issueNumber: number,
    triggeringStage: PipelineStage
  ): RecoveryRequiredPayload | null {
    const view = this.readRecoveryRunStateView(issueNumber);
    const graph = this.getStageGraph();
    return computeRecoveryRequired(error, issueNumber, triggeringStage, view, graph);
  }

  /**
   * Execute a chosen recovery action. Single entry point so the dialog
   * has one call irrespective of how the action is implemented.
   *
   * Open / cancel are no-ops. Resume, run-producer, restart, and discard
   * are routed to the existing primitives in PipelineStateService /
   * runStage / runPipeline. Returns true when the action ran without
   * throwing; the caller decides whether to chain into a follow-up
   * recovery dialog if a new error surfaces.
   */
  async runRecoveryAction(
    action: RecoveryAction,
    issueNumber: number,
    options: {
      triggeringStage: PipelineStage;
      producingStage?: PipelineStage | null;
      callbacks?: PipelineCallbacks;
    }
  ): Promise<{ success: boolean; error?: Error }> {
    const { triggeringStage, producingStage, callbacks } = options;
    try {
      switch (action) {
        case "cancel":
          return { success: true };

        case "open-run-state-directory": {
          const ws = this.getWorkingDirectory();
          const dir = path.join(ws, ".nightgauge", "pipeline");
          await vscode.commands.executeCommand("revealFileInOS", vscode.Uri.file(dir));
          return { success: true };
        }

        case "resume-from-paused-stage": {
          if (!this.stateService) {
            return {
              success: false,
              error: new Error("Cannot resume: PipelineStateService not initialized"),
            };
          }
          await this.stateService.resumePipeline();
          const resumeStage = this.readResumeFromStage(issueNumber) ?? triggeringStage;
          const result = await this.runStage(resumeStage, issueNumber, callbacks);
          return { success: result.success };
        }

        case "run-producing-stage": {
          if (!producingStage) {
            return {
              success: false,
              error: new Error("Cannot run producing stage: producer is unknown"),
            };
          }
          const result = await this.runStage(producingStage, issueNumber, callbacks);
          return { success: result.success };
        }

        case "restart-from-beginning": {
          await this.archiveExistingRunState(issueNumber);
          const result = await this.runPipeline(issueNumber, callbacks);
          return { success: result.success };
        }

        case "discard-run":
          return await this.discardRunForRecovery(issueNumber);

        default:
          return { success: false, error: new Error(`Unknown recovery action: ${action}`) };
      }
    } catch (err) {
      return {
        success: false,
        error: err instanceof Error ? err : new Error(String(err)),
      };
    }
  }

  /**
   * Archive the current run-state.json + per-stage context files under
   * `history/<runId>/` so a restart can run cleanly without losing audit
   * data. Best-effort: archival failures do not block restart.
   */
  private async archiveExistingRunState(issueNumber: number): Promise<void> {
    const ws = this.getWorkingDirectory();
    const pipelineDir = path.join(ws, ".nightgauge", "pipeline");
    const runStatePath = path.join(pipelineDir, "run-state.json");
    if (!fs.existsSync(runStatePath)) return;

    let runId = `unknown-${Date.now()}`;
    try {
      const raw = fs.readFileSync(runStatePath, "utf-8");
      const parsed = JSON.parse(raw) as { run_id?: string };
      if (parsed.run_id) runId = parsed.run_id;
    } catch {
      /* keep timestamp fallback */
    }

    const archiveDir = path.join(pipelineDir, "history", runId);
    try {
      fs.mkdirSync(archiveDir, { recursive: true });
    } catch (err) {
      this.logger.warn("Recovery archive: failed to create archive directory", {
        archiveDir,
        error: err instanceof Error ? err.message : String(err),
      });
      return;
    }

    const candidates = [
      "run-state.json",
      `issue-${issueNumber}.json`,
      `planning-${issueNumber}.json`,
      `dev-${issueNumber}.json`,
      `validate-${issueNumber}.json`,
      `pr-${issueNumber}.json`,
    ];
    for (const filename of candidates) {
      const src = path.join(pipelineDir, filename);
      if (!fs.existsSync(src)) continue;
      try {
        fs.renameSync(src, path.join(archiveDir, filename));
      } catch (err) {
        this.logger.warn("Recovery archive: failed to move file", {
          file: filename,
          error: err instanceof Error ? err.message : String(err),
        });
      }
    }
  }

  /**
   * Discard a run by deleting branch + worktree + per-issue context
   * files. Best-effort cleanup; errors are reported back to the caller
   * so the dialog can surface them.
   */
  private async discardRunForRecovery(
    issueNumber: number
  ): Promise<{ success: boolean; error?: Error }> {
    const ws = this.getWorkingDirectory();
    const pipelineDir = path.join(ws, ".nightgauge", "pipeline");
    const errors: string[] = [];

    for (const filename of [
      "run-state.json",
      `issue-${issueNumber}.json`,
      `planning-${issueNumber}.json`,
      `dev-${issueNumber}.json`,
      `validate-${issueNumber}.json`,
      `pr-${issueNumber}.json`,
    ]) {
      const target = path.join(pipelineDir, filename);
      if (fs.existsSync(target)) {
        try {
          fs.unlinkSync(target);
        } catch (err) {
          errors.push(`${filename}: ${err instanceof Error ? err.message : String(err)}`);
        }
      }
    }

    if (errors.length > 0) {
      return {
        success: false,
        error: new Error(`Discard partially failed: ${errors.join("; ")}`),
      };
    }
    return { success: true };
  }

  /**
   * Measure the context handoff file size after a stage completes.
   *
   * Stores the size in PipelineStateService for JSONL telemetry records.
   * If the file size exceeds the configured threshold, logs a warning.
   *
   * Non-critical: failures are logged as warnings, never break the pipeline.
   *
   * @see Issue #1009 - Track context handoff file sizes
   */
  private async measureContextFileSize(stage: PipelineStage, issueNumber: number): Promise<void> {
    try {
      const expectedType = STAGE_OUTPUT_CONTEXT_TYPE[stage];
      if (!expectedType) {
        return; // Stage doesn't produce a context file
      }

      const contextPath = this.getContextPath(expectedType, issueNumber);
      if (!fs.existsSync(contextPath)) {
        return; // File doesn't exist (already handled by validation)
      }

      const stat = fs.statSync(contextPath);
      const sizeBytes = stat.size;

      // Store in pipeline state
      if (this.stateService) {
        await this.stateService.setStageContextFileSize(stage, sizeBytes);
      }

      this.logger.info("Context file size measured", {
        stage,
        issueNumber,
        sizeBytes,
        contextPath,
      });

      // Check threshold and warn if exceeded
      const threshold = this.getContextFileSizeAlertThreshold();
      if (threshold > 0 && sizeBytes > threshold) {
        this.logger.warn("Context file size exceeds alert threshold", {
          stage,
          issueNumber,
          sizeBytes,
          threshold,
          contextPath,
        });
      }
    } catch (err) {
      this.logger.warn("Failed to measure context file size (non-critical)", {
        stage,
        issueNumber,
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }

  /**
   * Get the configured context file size alert threshold.
   *
   * @returns Threshold in bytes (default: 102400 = 100KB), or 0 if disabled
   * @see Issue #1009
   */
  private getContextFileSizeAlertThreshold(): number {
    try {
      const { getContextFileSizeAlertThreshold } =
        require("../utils/incrediConfig") as typeof import("../utils/incrediConfig");
      return getContextFileSizeAlertThreshold();
    } catch {
      return 102400; // Default: 100KB
    }
  }

  /**
   * Resolve the active performance mode (Issue #3009).
   * Used to tag history / state meta so cost trend and outcome calibration
   * can segment runs by mode.
   */
  private getActivePerformanceMode(): "efficiency" | "elevated" | "maximum" | "frontier" {
    try {
      const { getPerformanceMode } =
        require("../utils/incrediConfig") as typeof import("../utils/incrediConfig");
      return getPerformanceMode(this.getPersistentRoot());
    } catch {
      return "elevated";
    }
  }

  /**
   * @deprecated Issue #3009 — prefer `getActivePerformanceMode()`. Returns
   * `true` only when the resolved mode is `maximum`. Retained for one
   * release so additive `is_supercharge` tagging keeps working.
   */
  private isSuperchargeActive(): boolean {
    return this.getActivePerformanceMode() === "maximum";
  }

  /**
   * Resolve the (model, effort) pair the runner is most likely to pick for a
   * stage so {@link BudgetEnforcer.checkBudget} can apply the matching
   * mode-aware multiplier. Lookup chain mirrors what skillRunner does at
   * spawn time:
   *   1. Mode profile (Maximum → opus/high; Efficiency → haiku/low; etc.)
   *   2. Stage-level model/effort config overrides
   *   3. Pipeline-default model
   * If nothing resolves we return `undefined` and the BudgetEnforcer treats
   * the configured base limit as the literal ceiling (scale = 1.0).
   */
  private resolveBudgetModelInfo(stage: PipelineStage): { model?: string; effort?: string } {
    try {
      const { getModeStageProfile } =
        require("../utils/modeProfiles") as typeof import("../utils/modeProfiles");
      const mode = this.getActivePerformanceMode();
      const profile = getModeStageProfile(mode, stage);
      if (profile?.model) {
        return { model: profile.model, effort: profile.effort };
      }
    } catch {
      // Fall through to config-level resolution.
    }
    try {
      const { getStageModel, getStageEffort } =
        require("../utils/resolvers/stageResolver") as typeof import("../utils/resolvers/stageResolver");
      const { getDefaultModel } =
        require("../utils/resolvers/modelResolver") as typeof import("../utils/resolvers/modelResolver");
      const root = this.getPersistentRoot();
      const stageModel = getStageModel(stage, root);
      const model = stageModel ?? getDefaultModel(root);
      const effort = getStageEffort(stage, root);
      if (model) return { model, effort };
    } catch {
      // Best-effort — fall through to undefined.
    }
    return {};
  }

  /**
   * Validate that pipeline state correctly reflects pr-create completion.
   *
   * Called after pr-create stage succeeds and context file validation passes.
   * Detects the case where the PR was created but state.json was not updated,
   * and attempts recovery from the pr-{N}.json context file.
   *
   * Recovery matrix:
   * | State OK | pr-{N}.json exists | Action                           |
   * |----------|-------------------|----------------------------------|
   * | Yes      | Yes               | No-op (happy path)               |
   * | No       | Yes (valid PR URL)| Reconstruct state from context   |
   * | No       | No                | Retry with upgraded model         |
   * | Yes      | No                | Log warning, continue            |
   *
   * @returns 'ok' | 'recovered' | 'retry-needed' | 'warning'
   * @see Issue #1139 - pr-create state validation and retry
   */
  private async validatePrCreateState(
    issueNumber: number
  ): Promise<"ok" | "recovered" | "retry-needed" | "warning"> {
    // Skip validation when no state service (e.g., lightweight/test runs)
    if (!this.stateService) {
      return "ok";
    }

    // Check if state reflects pr-create completion
    const state = await this.stateService.getState();

    const stateValid =
      state !== null &&
      state.issue_number === issueNumber &&
      state.stages["pr-create"]?.status === "complete";

    // Check if pr-{N}.json context file exists with valid PR URL
    const contextPath = this.getContextPath("pr", issueNumber);
    let contextValid = false;
    let prUrl: string | undefined;

    if (fs.existsSync(contextPath)) {
      try {
        const content = JSON.parse(fs.readFileSync(contextPath, "utf-8")) as {
          pr_url?: string;
        };
        prUrl = content.pr_url;
        contextValid = typeof prUrl === "string" && prUrl.startsWith("https://");
      } catch {
        // Unparseable context — treat as missing
      }
    }

    // Happy path: both state and context are valid
    if (stateValid && contextValid) {
      return "ok";
    }

    // State valid but context missing — unusual but not fatal
    if (stateValid && !contextValid) {
      this.logger.warn("pr-create state is valid but context file is missing or invalid", {
        issueNumber,
        contextPath,
      });
      return "warning";
    }

    // State missing/invalid but context exists with valid PR URL — recover
    if (!stateValid && contextValid) {
      this.logger.info("PR created but state lost — recovering from pr-{N}.json", {
        issueNumber,
        prUrl,
      });

      try {
        await this.stateService.completeStage("pr-create");
        return "recovered";
      } catch (err) {
        this.logger.error("Failed to recover pr-create state from context file", {
          issueNumber,
          error: err instanceof Error ? err.message : String(err),
        });
        return "retry-needed";
      }
    }

    // Neither state nor valid context — full failure
    this.logger.error("PR creation failed entirely — neither state nor context file found", {
      issueNumber,
      contextPath,
    });
    return "retry-needed";
  }

  /**
   * Read expected branch name from issue context file.
   *
   * Returns null when the context cannot be parsed or branch is missing.
   */
  private getExpectedBranchFromContext(contextPath: string): string | null {
    try {
      const raw = fs.readFileSync(contextPath, "utf-8");
      const parsed = JSON.parse(raw) as { branch?: unknown };
      const branch = typeof parsed.branch === "string" ? parsed.branch.trim() : "";
      return branch.length > 0 ? branch : null;
    } catch (err) {
      this.logger.warn("Failed to read expected branch from context", {
        contextPath,
        err: err instanceof Error ? err.message : String(err),
      });
      return null;
    }
  }

  /**
   * Deterministic post-validation: if this issue is a sub-issue of an epic,
   * ensure base_branch points to the epic branch (not main). This prevents
   * the pr-create/pr-merge stages from targeting main, which would bypass the
   * epic merge flow (sub-issues accumulate on the epic branch, then the epic
   * branch merges to main as one unit) and break epic completion detection.
   *
   * Runs after issue-pickup regardless of whether the subagent or the
   * fallback generated the context file.
   *
   * Fail-closed contract: when this IS a confirmed epic sub-issue but no epic
   * branch exists, we CREATE it (idempotent `nightgauge epic
   * create-branch`) rather than silently leaving base_branch=main. If creation
   * fails, we return `{ ok: false }` so the caller fails the stage instead of
   * letting the sub-issue merge straight to main. Historically the Go scheduler
   * created the branch and this TS method only retargeted to it — but the
   * extension's autonomous slots never run the Go scheduler, so the branch was
   * never created, this method fell open to main, and sub-issues landed on main
   * individually (acmeapp-platform#6/#7 even pushed directly to main).
   *
   * @see Issue #1452 — #1463 merged to main instead of epic branch
   */
  private async enforceEpicBaseBranch(
    issueNumber: number
  ): Promise<{ ok: boolean; error?: string }> {
    const contextPath = this.getContextPath("issue", issueNumber);
    const workspaceRoot = this.getWorkingDirectory();
    const execOptions = {
      encoding: "utf-8" as const,
      cwd: workspaceRoot,
      timeout: 15_000,
    };

    try {
      const raw = fs.readFileSync(contextPath, "utf-8");
      const ctx = JSON.parse(raw);
      const currentBase: string = ctx.base_branch ?? "main";

      // Already targeting an epic branch — nothing to do
      if (currentBase.startsWith("epic/")) return { ok: true };

      // Detect parent epic from the context or via GitHub sub-issues API
      let parentNumber: number | null = ctx.native_parent ?? null;

      // Resolve owner/repo from repoOverride or CWD's git remote (also used by
      // the epic-branch create command below).
      let gqlOwner = "nightgauge";
      let gqlRepo = "nightgauge";
      if (this.repoOverride?.includes("/")) {
        [gqlOwner, gqlRepo] = this.repoOverride.split("/");
      } else {
        try {
          const { stdout: nwoRaw } = await execFileAsync(
            "gh",
            ["repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner"],
            execOptions
          );
          const nwo = nwoRaw.trim();
          if (nwo.includes("/")) {
            [gqlOwner, gqlRepo] = nwo.split("/");
          }
        } catch {
          // Use defaults
        }
      }

      if (parentNumber === null) {
        // Query GitHub for this issue's parent (native sub-issues API)
        try {
          const { stdout: parentRaw } = await execFileAsync(
            "gh",
            [
              "api",
              "graphql",
              "-f",
              `query=query { repository(owner: "${gqlOwner}", name: "${gqlRepo}") { issue(number: ${issueNumber}) { parent { number } } } }`,
              "--jq",
              ".data.repository.issue.parent.number",
            ],
            execOptions
          );
          const parentJson = parentRaw.trim();
          if (parentJson && parentJson !== "null") {
            parentNumber = parseInt(parentJson, 10);
          }
        } catch {
          // Non-critical: parent detection failed (transient). We can't confirm
          // this is an epic sub-issue, so don't block — fall through to ok.
        }
      }

      // Not an epic sub-issue (or couldn't confirm) — nothing to enforce.
      if (parentNumber === null) return { ok: true };

      // From here on this IS a confirmed epic sub-issue. Resolve the epic
      // branch, creating it if necessary; never fall open to main.
      let epicBranch = await this.findRemoteEpicBranch(parentNumber, execOptions);

      if (!epicBranch) {
        if (!this.isAutoCreateEpicBranchEnabled(workspaceRoot)) {
          // Operator explicitly disabled auto-create — preserve the historical
          // behavior of deferring to the subagent's base_branch.
          this.logger.warn(
            "Epic sub-issue has no epic branch and auto_create_epic_branch is disabled — leaving base_branch as-is",
            { issueNumber, parentNumber, currentBase }
          );
          return { ok: true };
        }
        epicBranch = await this.createEpicBranch(parentNumber, gqlOwner, gqlRepo, workspaceRoot);
      }

      if (!epicBranch) {
        // Fail-closed: confirmed epic sub-issue but we could neither find nor
        // create the epic branch. Do NOT let it merge to main.
        const msg =
          `Issue #${issueNumber} is a sub-issue of epic #${parentNumber} but the epic ` +
          `branch could not be found or created. Refusing to fall back to main (that ` +
          `bypasses the epic merge flow). Create epic/${parentNumber}-* and re-run.`;
        this.logger.error("Epic base_branch enforcement failed-closed", {
          issueNumber,
          parentNumber,
        });
        return { ok: false, error: msg };
      }

      // Patch the context file to target the epic branch.
      ctx.base_branch = epicBranch;
      if (!ctx.native_parent) ctx.native_parent = parentNumber;
      fs.writeFileSync(contextPath, JSON.stringify(ctx, null, 2) + "\n");

      this.logger.info(
        'Enforced epic base_branch — subagent wrote "main" but issue is a sub-issue of an epic',
        { issueNumber, parentNumber, epicBranch, previousBase: currentBase }
      );
      return { ok: true };
    } catch (err) {
      // Could not read/parse the context file — can't confirm epic membership,
      // so don't block the pipeline on it.
      this.logger.warn("Failed to enforce epic base_branch", {
        issueNumber,
        err,
      });
      return { ok: true };
    }
  }

  /**
   * Return the existing `epic/<parentNumber>-*` branch name on origin, or "".
   */
  private async findRemoteEpicBranch(
    parentNumber: number,
    execOptions: { encoding: "utf-8"; cwd: string; timeout: number }
  ): Promise<string> {
    try {
      const { stdout } = await execFileAsync(
        "git",
        ["ls-remote", "--heads", "origin", `epic/${parentNumber}-*`],
        execOptions
      );
      return (stdout.trim().split("\n")[0]?.split(/\s+/)[1] ?? "").replace("refs/heads/", "");
    } catch {
      return "";
    }
  }

  /**
   * Create the epic branch via the deterministic Go binary command
   * (`nightgauge epic create-branch`), reusing the same EnsureEpicBranch
   * logic the Go scheduler uses. Idempotent. Returns the branch name, or "" if
   * the binary is unavailable or creation failed.
   */
  private async createEpicBranch(
    parentNumber: number,
    owner: string,
    repo: string,
    workspaceRoot: string
  ): Promise<string> {
    try {
      const binary = await BinaryResolver.fromVSCode().resolve();
      if (!binary) {
        this.logger.warn("Epic branch create: nightgauge binary unresolved", { parentNumber });
        return "";
      }
      const { stdout } = await execFileAsync(
        binary,
        ["epic", "create-branch", String(parentNumber), "--owner", owner, "--repo", repo, "--json"],
        { encoding: "utf-8", cwd: workspaceRoot, timeout: 60_000 }
      );
      const parsed = JSON.parse(stdout.trim()) as { branch?: string; created?: boolean };
      if (parsed.branch) {
        this.logger.info("Epic branch ensured via deterministic runner", {
          parentNumber,
          branch: parsed.branch,
          created: parsed.created ?? false,
        });
        return parsed.branch;
      }
      return "";
    } catch (err) {
      this.logger.error("Epic branch create failed", {
        parentNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      return "";
    }
  }

  /**
   * Mirror of the Go scheduler's getAutoCreateEpicBranch: env var wins, else
   * `pipeline.auto_create_epic_branch` in .nightgauge/config.yaml, default
   * true.
   */
  private isAutoCreateEpicBranchEnabled(workspaceRoot: string): boolean {
    const env = process.env.NIGHTGAUGE_PIPELINE_AUTO_CREATE_EPIC_BRANCH;
    if (env) return env !== "false" && env !== "0";
    try {
      const configPath = path.join(workspaceRoot, ".nightgauge", "config.yaml");
      const data = fs.readFileSync(configPath, "utf-8");
      let inPipeline = false;
      for (const line of data.split("\n")) {
        const trimmed = line.trim();
        if (trimmed === "pipeline:") {
          inPipeline = true;
          continue;
        }
        if (inPipeline) {
          // Left the pipeline block at the next top-level key.
          if (trimmed && !line.startsWith(" ") && !line.startsWith("\t")) break;
          const m = trimmed.match(/^auto_create_epic_branch:\s*(\S+)/);
          if (m) return m[1] !== "false" && m[1] !== "0";
        }
      }
    } catch {
      // No config — default enabled.
    }
    return true;
  }

  /**
   * Get current git branch for the working directory.
   *
   * Returns null if git state cannot be determined.
   */
  private async getCurrentGitBranch(): Promise<string | null> {
    try {
      const workspaceRoot = this.getWorkingDirectory();
      const { stdout } = await execFileAsync("git", ["branch", "--show-current"], {
        encoding: "utf-8",
        cwd: workspaceRoot,
        timeout: 5000,
      });
      const output = stdout.trim();
      return output.length > 0 ? output : null;
    } catch (err) {
      this.logger.warn("Failed to determine current git branch", {
        err: err instanceof Error ? err.message : String(err),
      });
      return null;
    }
  }

  /**
   * Attempt deterministic branch recovery after issue-pickup mismatch.
   *
   * Recovery order:
   * 1. Checkout existing local branch
   * 2. Checkout tracking branch from origin (if it exists)
   * 3. Create local branch from current HEAD
   */
  private async tryRecoverExpectedBranch(
    issueNumber: number,
    expectedBranch: string,
    currentBranch: string
  ): Promise<boolean> {
    if (!this.isValidBranchName(expectedBranch)) {
      this.logger.error("Invalid expected branch name in context", {
        issueNumber,
        expectedBranch,
      });
      return false;
    }

    const workspaceRoot = this.getWorkingDirectory();
    const execOptions = {
      encoding: "utf-8" as const,
      cwd: workspaceRoot,
      timeout: 10000,
    };

    this.logger.warn("Attempting branch recovery after issue-pickup mismatch", {
      issueNumber,
      expectedBranch,
      currentBranch,
    });

    try {
      await execFileAsync("git", ["checkout", expectedBranch], execOptions);
      this.logger.info("Recovered branch by checking out local branch", {
        issueNumber,
        expectedBranch,
      });
      return true;
    } catch {
      // Continue to remote tracking fallback.
    }

    try {
      await execFileAsync(
        "git",
        ["show-ref", "--verify", "--quiet", `refs/remotes/origin/${expectedBranch}`],
        execOptions
      );
      await execFileAsync(
        "git",
        ["checkout", "-b", expectedBranch, "--track", `origin/${expectedBranch}`],
        execOptions
      );
      this.logger.info("Recovered branch by creating tracking branch", {
        issueNumber,
        expectedBranch,
      });
      return true;
    } catch {
      // Continue to local branch creation fallback.
    }

    try {
      await execFileAsync("git", ["checkout", "-b", expectedBranch], execOptions);
      this.logger.info("Recovered branch by creating local branch", {
        issueNumber,
        expectedBranch,
      });
      return true;
    } catch (err) {
      this.logger.error("Failed branch recovery after issue-pickup mismatch", {
        issueNumber,
        expectedBranch,
        currentBranch,
        err: err instanceof Error ? err.message : String(err),
      });
      return false;
    }
  }

  /**
   * Validate a branch name for safe git command usage.
   */
  private isValidBranchName(name: string): boolean {
    if (!name || typeof name !== "string") {
      return false;
    }

    const invalidPatterns = [
      /\.\./, // No consecutive dots
      /^[./]/, // Can't start with dot or slash
      /[/.]$/, // Can't end with slash or dot
      /@\{/, // No @{
      /\\/, // No backslash
      // eslint-disable-next-line no-control-regex
      /[\x00-\x1f]/, // No control characters
      /[\x7f]/, // No DEL
      // eslint-disable-next-line no-useless-escape
      /[ ~^:?*\[]/, // No shell/meta-confusing characters
      /\.lock$/, // Can't end with .lock
    ];

    for (const pattern of invalidPatterns) {
      if (pattern.test(name)) {
        return false;
      }
    }

    return true;
  }

  /**
   * Deterministic pre-check: Fetch issue labels and state in a single gh call.
   *
   * Returns `{ isEpic, isClosed }` to gate the pipeline before any AI stages
   * run. Combines the epic check (Issue #525) and closed-issue check (Issue #696)
   * into one network round-trip for efficiency.
   *
   * @param issueNumber - The GitHub issue number to check
   * @returns Object with isEpic and isClosed booleans (both default false on failure)
   *
   * @see Issue #525 - Halt pipeline when epic detected
   * @see Issue #696 - Orchestrator must pre-check issue state before running pipeline
   */
  private async preCheckIssue(issueNumber: number): Promise<{
    isEpic: boolean;
    isClosed: boolean;
    /** Labels from the issue, available for pre-flight budget estimation */
    labels: string[];
    /** Issue title, available for pre-flight budget estimation */
    title: string;
  }> {
    try {
      const workspaceRoot = this.getWorkingDirectory();
      const { stdout } = await execFileAsync(
        "gh",
        [
          "issue",
          "view",
          String(issueNumber),
          "--json",
          "labels,state,title",
          ...this.ghRepoArgs(),
        ],
        {
          encoding: "utf-8",
          cwd: workspaceRoot,
          timeout: 15000,
        }
      );
      const data = JSON.parse(stdout.trim());
      const labels: string[] = (data.labels || []).map((l: { name: string }) => l.name);
      const state: string = data.state || "";
      const title: string = data.title || "";
      return {
        isEpic: labels.includes("type:epic"),
        isClosed: state === "CLOSED",
        labels,
        title,
      };
    } catch (err) {
      this.logger.warn("Failed to pre-check issue", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      return { isEpic: false, isClosed: false, labels: [], title: "" };
    }
  }

  /**
   * Pre-check GitHub CLI authentication, required token scopes, and remaining
   * API rate-limit quota. Catches expired tokens, missing scopes, and
   * exhausted quota before AI stages consume tokens.
   *
   * #3020 — added rate-limit headroom check. The original incident burned
   * 3× $21.59 spinning pr-merge runs that launched into a fully-exhausted
   * GitHub quota and failed at the first gh call. Refusing to start the
   * pipeline below MIN_RATE_LIMIT_HEADROOM is far cheaper than discovering
   * exhaustion mid-stage. Threshold sized for one full pipeline run
   * (board reads + status updates + PR ops + post-merge verify).
   *
   * @see Issue #1141 (auth check) · Issue #3020 (rate-limit headroom)
   */
  private async preCheckAuth(): Promise<{
    isAuthenticated: boolean;
    hasRequiredScopes: boolean;
    // rateLimited is a SEPARATE, transient condition from auth/scope failures
    // (#3896). Pre-fix it was conflated with hasRequiredScopes:false, which
    // both produced a misleading "hasRequiredScopes=false" log and routed a
    // 1-minute quota dip into the terminal pipeline-start failure path that
    // burned the issue. When rateLimited is true, auth and scopes are fine —
    // only the GitHub REST/GraphQL quota is momentarily below headroom.
    rateLimited?: boolean;
    // networkOutage is the connectivity sibling of rateLimited (#4002).
    // `gh auth status` exits non-zero BOTH when not authenticated AND when
    // api.github.com is unreachable (DNS down, no route). Pre-fix the two were
    // conflated: a seconds-long network blip was reported as "not
    // authenticated — run `gh auth login`" and burned the issue at
    // pipeline-start. When networkOutage is true, NO auth state is known —
    // isAuthenticated/hasRequiredScopes are presumptive (the next preflight
    // after the cooldown re-validates for real).
    networkOutage?: boolean;
    retryAfterSec?: number;
    errorMessage?: string;
  }> {
    const MIN_RATE_LIMIT_HEADROOM = 200;
    try {
      const workspaceRoot = this.getWorkingDirectory();
      // `gh auth status` writes diagnostic output to stderr, so we need a
      // shell for the 2>&1 redirect. execAsync (promisified exec) uses a
      // shell; execFileAsync does not.
      const { stdout: rawOutput } = await execAsync("gh auth status 2>&1", {
        encoding: "utf-8",
        cwd: workspaceRoot,
        timeout: 15000,
      });
      const output = rawOutput.trim();

      // Check for active authentication
      const isAuthenticated = output.includes("Logged in to");

      // Check for repo scope (required for PR creation/merge)
      const hasRepoScope = /\brepo\b/.test(output);

      if (!isAuthenticated) {
        return {
          isAuthenticated: false,
          hasRequiredScopes: false,
          errorMessage: "GitHub CLI is not authenticated. Run `gh auth login` to authenticate.",
        };
      }

      if (!hasRepoScope) {
        return {
          isAuthenticated: true,
          hasRequiredScopes: false,
          errorMessage:
            "GitHub token lacks required `repo` scope for PR operations. " +
            "Run `gh auth refresh -s repo` to add the scope.",
        };
      }

      // Rate-limit headroom check (best-effort — IPC failure should not
      // block the pipeline, since auth is already validated above).
      try {
        const ipc = IpcClient.getInstance();
        const info = await ipc.githubRateLimit();
        if (info.remaining < MIN_RATE_LIMIT_HEADROOM) {
          const resetMs = info.resetAt * 1000 - Date.now();
          const minutes = Math.max(1, Math.ceil(resetMs / 60_000));
          const retryAfterSec = Math.max(1, Math.ceil(resetMs / 1000));
          // Auth + scopes are fine (we passed the repo-scope check above) — this
          // is purely a transient quota dip. Report it as rateLimited, NOT as a
          // scope failure, so the caller defers/retries instead of burning the
          // issue at pipeline-start (#3896).
          return {
            isAuthenticated: true,
            hasRequiredScopes: true,
            rateLimited: true,
            retryAfterSec,
            errorMessage:
              `GitHub API quota too low to start pipeline (${info.remaining}/${info.limit} remaining, ` +
              `need ≥${MIN_RATE_LIMIT_HEADROOM}). Resets in ~${minutes} min.`,
          };
        }
        // Healthy quota — re-arm the rate-limit circuit breaker so a future
        // 429 outage trips it again. (Breaker is module-level; without
        // re-arming it would stay tripped after a successful recovery.)
        noteRateLimitOk();
      } catch (rlErr) {
        // Non-fatal — log and continue. Auth is good; if quota is actually
        // exhausted the first gh call will surface it and the rate-limit
        // circuit breaker (#3020) will pause autonomous mode.
        this.logger.debug("Rate-limit pre-check skipped (IPC error)", {
          error: rlErr instanceof Error ? rlErr.message : String(rlErr),
        });
      }

      return { isAuthenticated: true, hasRequiredScopes: true };
    } catch (err) {
      // `gh auth status` exits non-zero when not authenticated — but ALSO
      // when api.github.com is unreachable (the status check validates the
      // token over the network). Distinguish the two from gh's own
      // diagnostic ("error connecting to api.github.com", DNS errors, …),
      // captured on the exec error's combined output, using the same
      // patterns the network-outage breaker matches. A blip must defer the
      // pipeline (transient, auto-retry), not page the operator to
      // re-authenticate. #4002.
      const combined = [
        err instanceof Error ? err.message : String(err),
        (err as { stdout?: string })?.stdout ?? "",
        (err as { stderr?: string })?.stderr ?? "",
      ].join("\n");
      if (isConnectivityError(combined)) {
        return {
          isAuthenticated: true,
          hasRequiredScopes: true,
          networkOutage: true,
          retryAfterSec: 120,
          errorMessage:
            "GitHub API unreachable — `gh auth status` could not connect to api.github.com. " +
            "This is a network outage, not an auth failure; the pipeline will retry automatically.",
        };
      }
      return {
        isAuthenticated: false,
        hasRequiredScopes: false,
        errorMessage:
          "GitHub auth check failed: `gh auth status` returned a non-zero exit code. " +
          "Run `gh auth login` to authenticate.",
      };
    }
  }

  /**
   * Handle an epic issue — check sub-issues and auto-close if all complete.
   *
   * Returns a PipelineRunResult immediately without consuming any AI tokens.
   * If all sub-issues are closed, auto-closes the epic via gh CLI and syncs
   * the project board. If sub-issues are still open, returns a clear error
   * listing what's remaining.
   *
   * @param issueNumber - The epic issue number
   * @param startTime - Pipeline start timestamp for duration calculation
   * @returns PipelineRunResult indicating the outcome
   *
   * @see Issue #525 - Halt pipeline when epic detected
   */
  private async handleEpicIssue(
    issueNumber: number,
    startTime: number
  ): Promise<PipelineRunResult> {
    const workspaceRoot = this.getWorkingDirectory();

    // Fetch epic title and body for sub-issue reference parsing
    // Note: gh `subIssues` field is not available in all gh CLI versions,
    // so we parse issue references from the body text (matches #123, GH-123).
    // This mirrors the Go binary epic check-completion fallback approach.
    interface SubIssue {
      number: number;
      title: string;
      state: string;
    }
    let subIssues: SubIssue[] = [];
    let epicTitle = `Issue #${issueNumber}`;

    try {
      const { stdout: outputRaw } = await execFileAsync(
        "gh",
        ["issue", "view", String(issueNumber), "--json", "title,body", ...this.ghRepoArgs()],
        {
          encoding: "utf-8",
          cwd: workspaceRoot,
          timeout: 30000,
        }
      );
      const data = JSON.parse(outputRaw.trim());
      epicTitle = data.title || epicTitle;
      const body: string = data.body || "";

      // Extract issue references from body (#123, GH-123)
      const issueRefs = new Set<number>();
      const refPattern = /(?:#|GH-)(\d+)/g;
      let match;
      while ((match = refPattern.exec(body)) !== null) {
        const refNum = parseInt(match[1], 10);
        // Skip self-references
        if (refNum !== issueNumber) {
          issueRefs.add(refNum);
        }
      }

      this.logger.debug("Parsed epic body references", {
        issueNumber,
        subIssueCount: issueRefs.size,
        refs: Array.from(issueRefs),
      });

      // Check state of each referenced issue
      for (const ref of issueRefs) {
        try {
          const { stdout: issueOutputRaw } = await execFileAsync(
            "gh",
            ["issue", "view", String(ref), "--json", "number,title,state", ...this.ghRepoArgs()],
            {
              encoding: "utf-8",
              cwd: workspaceRoot,
              timeout: 15000,
            }
          );
          const issueData = JSON.parse(issueOutputRaw.trim());

          // Skip sub-issues that are themselves epics (circular ref prevention)
          try {
            const { stdout: labelsOutputRaw } = await execFileAsync(
              "gh",
              [
                "issue",
                "view",
                String(ref),
                "--json",
                "labels",
                "-q",
                ".labels[].name",
                ...this.ghRepoArgs(),
              ],
              {
                encoding: "utf-8",
                cwd: workspaceRoot,
                timeout: 15000,
              }
            );
            const labelsOutput = labelsOutputRaw.trim();
            if (labelsOutput.split("\n").includes("type:epic")) {
              this.logger.debug("Skipping epic sub-reference", { ref });
              continue;
            }
          } catch {
            // If label check fails, don't skip
          }

          subIssues.push({
            number: issueData.number,
            title: issueData.title || `Issue #${ref}`,
            state: issueData.state || "UNKNOWN",
          });
        } catch {
          // If we can't fetch the issue, treat it as open to be safe
          subIssues.push({
            number: ref,
            title: `Issue #${ref}`,
            state: "UNKNOWN",
          });
        }
      }
    } catch (err) {
      this.logger.warn("Failed to fetch epic details", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
    }

    const openSubIssues = subIssues.filter((s) => s.state !== "CLOSED");
    const closedSubIssues = subIssues.filter((s) => s.state === "CLOSED");

    // If all sub-issues are closed, auto-close the epic
    if (openSubIssues.length === 0 && closedSubIssues.length > 0) {
      this.logger.info("All sub-issues complete — auto-closing epic", {
        issueNumber,
        closedCount: closedSubIssues.length,
      });

      try {
        const comment = `All ${closedSubIssues.length} sub-issues have been completed. Auto-closing epic.`;
        await execFileAsync(
          "gh",
          ["issue", "close", String(issueNumber), "--comment", comment, ...this.ghRepoArgs()],
          {
            encoding: "utf-8",
            cwd: workspaceRoot,
            timeout: 15000,
          }
        );

        // Update project board status to Done via GraphQL
        try {
          await updateProjectItemStatus(issueNumber, "Done", workspaceRoot, this.logger);
        } catch {
          // Project board sync is best-effort
        }

        this.logger.info("Epic auto-closed successfully", { issueNumber });

        // Create epic→main PR via Go binary
        try {
          const binary = await BinaryResolver.fromVSCode().resolve();
          if (!binary) throw new Error("nightgauge binary not found");

          const epicPrExecOpts = {
            encoding: "utf-8" as const,
            cwd: workspaceRoot,
            timeout: 60000,
          };

          // Find epic branch: origin/epic/{issueNumber}-*
          let epicBranch = "";
          try {
            const { stdout: branchOutRaw } = await execFileAsync(
              "git",
              ["branch", "-r", "--list", `origin/epic/${issueNumber}-*`],
              { ...epicPrExecOpts, timeout: 10000 }
            );
            const branchOut = branchOutRaw.trim();
            if (branchOut) {
              epicBranch = branchOut
                .split("\n")[0]
                .trim()
                .replace(/^origin\//, "");
            }
          } catch {
            this.logger.warn("Could not determine epic branch", {
              issueNumber,
            });
          }

          if (!epicBranch) {
            this.logger.warn("No epic branch found — skipping PR creation", {
              issueNumber,
            });
          } else {
            // Build PR title and body from existing epic data
            const subIssueList = closedSubIssues
              .map((s) => `- #${s.number}: ${s.title}`)
              .join("\n");
            const prBody = `Epic #${issueNumber} completion: all sub-issues are closed.\n\n## Sub-issues\n\n${subIssueList}`;
            const prTitle = `feat(#${issueNumber}): ${epicTitle}`;

            // Check for existing PR
            let prAlreadyExists = false;
            try {
              await execFileAsync(
                "gh",
                ["pr", "view", epicBranch, "--json", "number", "-q", ".number"],
                { ...epicPrExecOpts, timeout: 10000 }
              );
              prAlreadyExists = true;
            } catch {
              // PR does not exist — proceed to create
            }

            if (prAlreadyExists) {
              this.logger.info("Epic PR already exists", {
                issueNumber,
                epicBranch,
              });
            } else {
              const epicPrArgs = [
                "pr",
                "create",
                "--title",
                prTitle,
                "--head",
                epicBranch,
                "--base",
                "main",
                "--body",
                prBody,
                "--json",
              ];

              let prResult = "";
              let lastErr: unknown;

              // Attempt with a single retry on transient failures
              for (let attempt = 0; attempt < 2; attempt++) {
                try {
                  const { stdout: prResultRaw } = await execFileAsync(
                    binary,
                    epicPrArgs,
                    epicPrExecOpts
                  );
                  prResult = prResultRaw.trim();
                  lastErr = undefined;
                  break;
                } catch (err) {
                  lastErr = err;
                  if (attempt === 0) {
                    this.logger.warn("Epic PR creation failed, retrying in 3s...", {
                      issueNumber,
                      attempt,
                      err: err instanceof Error ? err.message : String(err),
                    });
                    await new Promise((r) => setTimeout(r, 3000));
                  }
                }
              }

              if (lastErr) {
                this.logger.warn("Failed to create epic PR after retry", {
                  issueNumber,
                  err: lastErr instanceof Error ? lastErr.message : String(lastErr),
                });
              } else {
                try {
                  const prData = JSON.parse(prResult) as {
                    action?: string;
                    pr_url?: string;
                    pr_number?: number;
                  };
                  this.logger.info("Epic PR creation result", {
                    issueNumber,
                    action: prData.action ?? "created",
                    prUrl: prData.pr_url,
                    prNumber: prData.pr_number,
                  });
                } catch {
                  this.logger.warn("Epic PR binary returned non-JSON output", {
                    issueNumber,
                    output: prResult.slice(0, 500),
                  });
                }
              }
            }
          }
        } catch (prErr) {
          // PR creation failure is non-fatal — epic is already closed
          this.logger.warn("Failed to create/merge epic PR", {
            issueNumber,
            err: prErr instanceof Error ? prErr.message : String(prErr),
          });
        }
      } catch (err) {
        this.logger.error("Failed to auto-close epic", {
          issueNumber,
          err: err instanceof Error ? err.message : String(err),
        });
      }

      return {
        success: true,
        completedStages: [],
        skippedStages: [],
        deferredStages: [],
        error: new Error(
          `Epic #${issueNumber} auto-closed — all ${closedSubIssues.length} sub-issues are complete.`
        ),
        totalDurationMs: Date.now() - startTime,
      };
    }

    // Epic has open sub-issues — halt with details
    const openList = openSubIssues
      .map((s) => `  - #${s.number}: ${s.title} (${s.state})`)
      .join("\n");
    const closedCount = closedSubIssues.length;
    const totalCount = subIssues.length;

    let message =
      `Issue #${issueNumber} is an epic (type:epic). ` +
      `Pipeline halted — zero AI tokens consumed.\n\n`;

    if (subIssues.length > 0) {
      message +=
        `Progress: ${closedCount}/${totalCount} sub-issues complete.\n\n` +
        `Open sub-issues:\n${openList}\n\n` +
        `Pick up a sub-issue instead: /nightgauge:issue-pickup <number>`;
    } else {
      message += `No sub-issues found. Pick up a sub-issue instead or close this epic manually.`;
    }

    this.eventDispatcher.onStderr(
      "pipeline-start",
      `[pipeline-start-failure] epic-with-open-sub-issues: ${message}\n`
    );

    return {
      success: false,
      completedStages: [],
      skippedStages: [],
      deferredStages: [],
      failedStage: "pipeline-start",
      error: new Error(message),
      totalDurationMs: Date.now() - startTime,
    };
  }

  /**
   * Migrate context schema from older versions to current (v1.3)
   *
   * This function performs lazy, in-memory migration of context files.
   * It does NOT write to disk - context files are transient and deleted
   * after PR merge, so in-memory migration is the lowest-risk approach.
   *
   * Migration behavior:
   * - v1.0: No routing field → adds default routing with 'feature' task type
   * - v1.1: Has routing but no task_type → adds 'feature' task type
   * - v1.2: Has task_type but skip_stages may be limited → expands to 4-stage schema
   * - v1.3: Current schema, returned as-is
   *
   * @param context - The parsed issue context object
   * @returns Migrated context object (v1.3 schema) or null if invalid
   *
   * @see Issue #418 - Schema migration for pipeline routing
   * @see docs/CONTEXT_ARCHITECTURE.md - Schema version history
   */
  private migrateContextSchema(context: Record<string, unknown>): Record<string, unknown> | null {
    // Validate schema_version exists
    const schemaVersion = context.schema_version;
    if (typeof schemaVersion !== "string") {
      this.logger.warn("Invalid schema_version in context file", {
        schemaVersion,
      });
      return null;
    }

    // Handle known versions
    switch (schemaVersion) {
      default: {
        // Parse major.minor version for forward-compatible handling
        const parts = schemaVersion.split(".");
        const major = parseInt(parts[0], 10);
        const minor = parseInt(parts[1], 10);

        if (isNaN(major) || isNaN(minor)) {
          this.logger.warn("Unparseable schema version in context file", {
            schemaVersion,
          });
          return null;
        }

        // Future versions (>= 1.3) are forward-compatible — accept as-is
        if (major > 1 || (major === 1 && minor >= 3)) {
          this.logger.debug("Accepting forward-compatible schema version", {
            schemaVersion,
          });
          return context;
        }

        // v1.2: Has task_type, but skip_stages might not include pr-create/pr-merge
        if (major === 1 && minor === 2) {
          this.logger.debug("Migrating context from v1.2 to v1.3", {
            hasRouting: !!context.routing,
          });
          return {
            ...context,
            schema_version: "1.3",
          };
        }

        // v1.1: Has routing but no task_type
        if (major === 1 && minor === 1) {
          this.logger.debug("Migrating context from v1.1 to v1.3", {
            hasRouting: !!context.routing,
          });
          if (context.routing && typeof context.routing === "object") {
            return {
              ...context,
              schema_version: "1.3",
              routing: {
                ...(context.routing as Record<string, unknown>),
                task_type: (context.routing as Record<string, unknown>).task_type ?? "feature",
              },
            };
          }
          return {
            ...context,
            schema_version: "1.3",
          };
        }

        // v1.0: No routing field
        if (major === 1 && minor === 0) {
          this.logger.debug("Migrating context from v1.0 to v1.3", {
            hasRouting: false,
          });
          return {
            ...context,
            schema_version: "1.3",
          };
        }

        // Major version 0 or unknown — reject
        this.logger.warn("Unknown schema version in context file", {
          schemaVersion,
        });
        return null;
      }
    }
  }

  /**
   * Load routing decision from issue context file
   *
   * Reads the issue-{N}.json file and extracts routing information.
   * If routing is not present (schema v1.0), returns null.
   *
   * @param issueNumber - The issue number to load routing for
   * @returns RoutingDecision or null if not available
   */
  private async loadRoutingDecision(issueNumber: number): Promise<RoutingDecision | null> {
    try {
      // Get issue context path (uses context loader if available)
      const issueContextPath = this.getIssueContextPath(issueNumber);

      // Check if file exists
      if (!fs.existsSync(issueContextPath)) {
        this.logger.debug("Issue context file not found", { issueContextPath });
        return null;
      }

      // Read and parse the file
      const content = fs.readFileSync(issueContextPath, "utf-8");
      const rawContext = JSON.parse(content);

      // Migrate schema to v1.3 if needed (Issue #418)
      const issueContext = this.migrateContextSchema(rawContext);
      if (!issueContext) {
        this.logger.debug("Failed to migrate context schema", { issueNumber });
        return null;
      }

      // Check for routing field (schema v1.1+)
      if (!issueContext.routing) {
        this.logger.debug("No routing field in issue context (schema v1.0)", {
          issueNumber,
        });
        return null;
      }

      // Type assertion for routing object after migration
      const routing = issueContext.routing as Record<string, unknown>;

      // Convert routing from context to ChangeAnalysis format
      // Normalize labels — subagents may write {name: string} objects
      const rawLabels = Array.isArray(issueContext.labels) ? issueContext.labels : [];
      const labels = this.normalizeLabels(rawLabels);

      const analysis: ChangeAnalysis = {
        changeType: routing.change_type as ChangeAnalysis["changeType"],
        taskType: (routing.task_type ?? "feature") as ChangeAnalysis["taskType"], // Default for backward compatibility
        sizeLabel: this.extractSizeLabel(labels),
        typeLabel: this.extractTypeLabel(labels),
        priorityLabel: this.extractPriorityLabel(labels),
        complexityScore: routing.complexity_score as number,
        suggestedRoute: routing.suggested_route as ChangeAnalysis["suggestedRoute"],
        skipStages: routing.skip_stages as ChangeAnalysis["skipStages"],
        rationale: routing.rationale as string,
        estimatedTimeMinutes: routing.estimated_time_minutes as number,
        foundationTask: (routing.foundation_task as boolean) ?? false,
        // Risk dimension (#4093) — persisted by the Go router; default false for
        // pre-#4093 context files.
        riskHigh: (routing.risk_high as boolean) ?? false,
        riskReasons: Array.isArray(routing.risk_reasons) ? (routing.risk_reasons as string[]) : [],
      };

      // Apply config overrides and make decision
      const routingConfig: RoutingConfig = {
        ...DEFAULT_ROUTING_CONFIG,
        ...this.config.routing,
        forceFullPipeline: this.config.forceFullPipeline,
      };

      const decision = makeRoutingDecision(analysis, routingConfig);

      // Cache routing telemetry for JSONL execution history (Issue #1005)
      this.cachedRoutingTelemetry = {
        complexity_score: analysis.complexityScore,
        path: decision.route,
        skip_stages: decision.skipStages.map(String),
      };

      this.logger.info("Loaded routing decision", {
        issueNumber,
        route: decision.route,
        skipStages: decision.skipStages,
        wasOverridden: decision.wasOverridden,
      });

      return decision;
    } catch (error) {
      this.logger.warn("Failed to load routing decision", {
        issueNumber,
        error: error instanceof Error ? error.message : "Unknown error",
      });
      return null;
    }
  }

  /**
   * Persist a stage-exit diagnostic record via the Go binary's IPC handler.
   *
   * Issue #3619: PR #3608 wired `WriteStageExitRecord` only to the Go
   * scheduler's `runPipeline()` path. The user's autonomous workflow runs
   * through this TS-side `HeadlessOrchestrator.runPipeline()` which never
   * round-trips Go's scheduler, so `.nightgauge/pipeline/exit-records/`
   * never got written. Every IPC-mode failure went into a black box — see
   * the #3340 retro where four Anthropic-500 retries burned $2.41 with no
   * persisted record. This helper closes that gap by calling the
   * `diagnostics.recordStageExit` IPC method after every stage exit.
   *
   * The on-disk JSONL line is byte-equivalent to what the Go-scheduler path
   * writes, so `nightgauge exit-records tail` sees a unified stream
   * regardless of which dispatch path produced any given record.
   *
   * Fire-and-forget: a failure inside this method logs but never throws —
   * the pipeline's success/failure semantics never depend on diagnostic
   * persistence. Fields the TS layer doesn't have (rate-limit remaining at
   * exit, concurrent sibling pipelines) are left empty; the record is
   * strictly better than the prior "nothing written" status quo.
   */
  private async recordStageExitDiagnostic(
    stage: PipelineStage,
    issueNumber: number,
    result: StageRunResult,
    stageStartTime: number
  ): Promise<void> {
    try {
      const ipc = IpcClient.getInstance();
      const elapsedMs = result.durationMs ?? Date.now() - stageStartTime;
      const errorText = result.error?.message ?? "";
      // Pre-classify the common terminal kinds we already detect at this layer.
      // Empty string defers to the Go-side `ClassifyTerminalKind` fallback so
      // both write paths produce consistent `terminal_kind` values.
      let terminalKind = "";
      if (result.budgetExceeded) {
        // #3666: when the stage shipped its work product before being killed
        // (e.g. pr-create successfully opened the PR), classify as the
        // recoverable budget_ceiling_hit kind so the Go-side autonomous
        // scheduler does NOT increment LifetimeIssueFailures or trip the
        // cascade circuit breaker. The actual reclassification gate is in
        // HeadlessOrchestrator's budget-kill path and only sets
        // `shippedPartially` when a deterministic check (`checkPrCreatedForIssue`)
        // confirms the work product exists.
        terminalKind = result.shippedPartially ? "budget_ceiling_hit" : "budget_exceeded";
      }
      const repo = this.repoOverride ?? "";
      const model = this.stageModelOverrides.get(stage) ?? "";
      await ipc.diagnosticsRecordStageExit(
        repo,
        issueNumber,
        stage,
        result.success,
        this.currentPipelineRunId ?? undefined,
        new Date(stageStartTime).toISOString(),
        model || undefined,
        undefined, // exitCode — not exposed at this layer
        terminalKind || undefined,
        errorText || undefined,
        elapsedMs,
        undefined, // idleMsAtExit — captured deeper in SkillRunner only
        undefined, // inputTokens — captured via tokenAccumulator, not surfaced here
        undefined, // outputTokens
        undefined, // cacheReadTokens
        undefined, // cacheCreationTokens
        undefined // costUsd
      );
    } catch (err) {
      // Never block pipeline progress on a diagnostic write failure. Logged
      // for observability — see retros if the daily JSONL is unexpectedly empty.
      this.logger.warn("recordStageExitDiagnostic failed (non-fatal)", {
        stage,
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
    }
  }

  /**
   * Load issue metadata for AutoModelSelector (Issue #732)
   *
   * Reads issue-{N}.json context file and extracts labels and title.
   * Caches the result for the duration of the pipeline run.
   *
   * @param issueNumber - The issue number
   * @returns IssueMetadata or null if context file not found
   */
  private loadIssueMetadata(issueNumber: number): IssueMetadata | null {
    // Return cached metadata if available for this issue
    if (this.cachedIssueMetadata) {
      return this.cachedIssueMetadata;
    }

    try {
      const issueContextPath = this.getIssueContextPath(issueNumber);
      if (!fs.existsSync(issueContextPath)) {
        return null;
      }

      const content = fs.readFileSync(issueContextPath, "utf-8");
      const rawContext = JSON.parse(content);
      const issueContext = this.migrateContextSchema(rawContext);
      if (!issueContext) {
        return null;
      }

      const rawLabels = Array.isArray(issueContext.labels) ? issueContext.labels : [];
      const labels = this.normalizeLabels(rawLabels);
      const title = (issueContext.title ?? `Issue #${issueNumber}`) as string;
      const sizeLabel = this.extractSizeLabel(labels);

      const metadata: IssueMetadata = {
        labels,
        title,
        ...(sizeLabel ? { size: sizeLabel as IssueMetadata["size"] } : {}),
      };

      this.cachedIssueMetadata = metadata;
      return metadata;
    } catch (error) {
      this.logger.warn("Failed to load issue metadata for model selection", {
        issueNumber,
        error: error instanceof Error ? error.message : "Unknown error",
      });
      return null;
    }
  }

  /**
   * Normalize labels that may be strings or {name: string} objects.
   * Subagents sometimes write raw GitHub label objects to context files.
   */
  private normalizeLabels(labels: unknown[]): string[] {
    return labels
      .map((l) =>
        typeof l === "string"
          ? l
          : l && typeof l === "object" && "name" in l
            ? String((l as { name: unknown }).name)
            : ""
      )
      .filter((l) => l.length > 0);
  }

  /**
   * Extract size label from labels array
   */
  private extractSizeLabel(labels: string[]): "XS" | "S" | "M" | "L" | "XL" | null {
    for (const label of labels) {
      const match = label.match(/^size:(XS|S|M|L|XL)$/i);
      if (match) {
        return match[1].toUpperCase() as "XS" | "S" | "M" | "L" | "XL";
      }
    }
    return null;
  }

  /**
   * Extract type label from labels array
   */
  private extractTypeLabel(
    labels: string[]
  ): "feature" | "bug" | "docs" | "refactor" | "chore" | "test" | "verification" | null {
    for (const label of labels) {
      const match = label.match(/^type:(feature|bug|docs|refactor|chore|test|verification)$/i);
      if (match) {
        return match[1].toLowerCase() as
          "feature" | "bug" | "docs" | "refactor" | "chore" | "test" | "verification";
      }
    }
    return null;
  }

  /**
   * Extract priority label from labels array
   */
  private extractPriorityLabel(labels: string[]): "critical" | "high" | "medium" | "low" | null {
    for (const label of labels) {
      const match = label.match(/^priority:(critical|high|medium|low)$/i);
      if (match) {
        return match[1].toLowerCase() as "critical" | "high" | "medium" | "low";
      }
    }
    return null;
  }

  /**
   * Check if a stage should be skipped based on routing decision
   *
   * @param stage - The stage to check
   * @returns True if the stage should be skipped
   */
  private shouldSkipStage(stage: PipelineStage): boolean {
    // If no routing decision, don't skip anything
    if (!this.currentRoutingDecision) {
      return false;
    }

    // Check if stage is in the skip list
    return this.currentRoutingDecision.skipStages.includes(stage);
  }

  /**
   * Reconcile deterministic completion side effects after a successful pipeline:
   * - Ensure issue is closed when merged PR exists
   * - Ensure status label/project status are synced to done
   * - Run targeted epic completion check for this issue
   * - Trigger VS Code refresh for project/ready views
   *
   * Returns verified=false if GitHub state cannot be queried (non-blocking).
   */

  /**
   * Check if feature-planning signaled that the issue is already resolved.
   *
   * Reads `planning-{N}.json` and checks for the `verify-and-close` signal:
   * 1. `approach === "verify-and-close"`
   * 2. `files_to_create` is empty
   * 3. `files_to_modify` is empty
   *
   * This is a pure deterministic check — no AI tokens consumed.
   *
   * @param issueNumber - The issue number
   * @returns Signal object if early-exit detected, null otherwise
   * @see Issue #708
   */
  private checkPlanningEarlyExit(issueNumber: number): { approach: string } | null {
    try {
      const planningContextPath = this.getContextPath("planning", issueNumber);
      if (!fs.existsSync(planningContextPath)) {
        // Missing planning context — fail-open for backwards compatibility
        return null;
      }

      const content = fs.readFileSync(planningContextPath, "utf-8");
      const planningContext = JSON.parse(content);

      const approach = planningContext.approach;
      if (approach !== "verify-and-close") {
        return null;
      }

      // Safety guard: verify file lists are empty
      const filesToCreate = Array.isArray(planningContext.files_to_create)
        ? planningContext.files_to_create
        : [];
      const filesToModify = Array.isArray(planningContext.files_to_modify)
        ? planningContext.files_to_modify
        : [];

      if (filesToCreate.length > 0 || filesToModify.length > 0) {
        this.logger.warn(
          "Planning approach is verify-and-close but file lists are non-empty — continuing pipeline",
          {
            issueNumber,
            filesToCreate: filesToCreate.length,
            filesToModify: filesToModify.length,
          }
        );
        return null;
      }

      return { approach };
    } catch (err) {
      this.logger.warn("Failed to read planning context for early-exit check", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      // Fail-open: continue pipeline if we can't read the file
      return null;
    }
  }

  /**
   * Read planning context to extract budget hint for post-planning stages.
   *
   * Returns null if the planning context doesn't exist or can't be parsed
   * (fail-open for backwards compatibility).
   *
   * @param issueNumber - The issue number
   * @returns PlanningBudgetHint or null
   * @see Issue #1333 - Planning-aware budget enforcement
   */
  private readPlanningBudgetHint(issueNumber: number): PlanningBudgetHint | null {
    try {
      const planningContextPath = this.getContextPath("planning", issueNumber);
      if (!fs.existsSync(planningContextPath)) {
        return null;
      }

      const content = fs.readFileSync(planningContextPath, "utf-8");
      const planningContext = JSON.parse(content);

      const hint: PlanningBudgetHint = {};

      const assessedSize = planningContext.complexity_assessment?.size_label;
      if (assessedSize && ["XS", "S", "M", "L", "XL"].includes(assessedSize)) {
        hint.assessedSize = assessedSize as SizeLabel;
      }

      const filesToCreate = Array.isArray(planningContext.files_to_create)
        ? planningContext.files_to_create.length
        : 0;
      const filesToModify = Array.isArray(planningContext.files_to_modify)
        ? planningContext.files_to_modify.length
        : 0;
      hint.totalFileCount = filesToCreate + filesToModify;

      return hint;
    } catch (err) {
      this.logger.warn("Failed to read planning context for budget hint", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      return null;
    }
  }

  /**
   * Lightweight close path for already-resolved issues detected after planning.
   *
   * Performs best-effort cleanup:
   * 1. Close the GitHub issue with an explanatory comment
   * 2. Sync project board status to Done
   * 3. Sync project board status via hook script
   * 4. Clean up context files and branches via hook script
   * 5. Run epic completion check via hook script
   *
   * Follows the existing `reconcileCompletionSideEffects()` pattern but
   * targeted for the early-exit case (no merged PR exists).
   *
   * @param issueNumber - The issue number
   * @param callbacks - Pipeline callbacks for UI notification
   * @see Issue #708
   */
  private async runAlreadyResolvedClosePath(
    issueNumber: number,
    callbacks?: PipelineCallbacks
  ): Promise<void> {
    const workspaceRoot = this.getWorkingDirectory();
    // Resolve GITHUB_TOKEN for Go binary (VSCode doesn't inherit shell env)
    const execEnv = { ...process.env };
    if (!execEnv.GITHUB_TOKEN) {
      try {
        const { stdout: tokenRaw } = await execFileAsync("gh", ["auth", "token"], {
          timeout: 5_000,
        });
        execEnv.GITHUB_TOKEN = tokenRaw.toString().trim();
      } catch {
        /* gh CLI not available — proceed without */
      }
    }
    const ghExecOptions = {
      encoding: "utf-8" as const,
      cwd: workspaceRoot,
      timeout: 30000,
      env: execEnv,
    };

    // 1. Close the issue with an explanatory comment
    try {
      await execFileAsync(
        "gh",
        [
          "issue",
          "close",
          String(issueNumber),
          "--comment",
          "Pipeline detected this issue is already resolved during planning. All acceptance criteria are met — no code changes needed. Closing automatically.",
          ...this.ghRepoArgs(),
        ],
        ghExecOptions
      );
      this.logger.info("Closed already-resolved issue", { issueNumber });
    } catch (err) {
      this.logger.warn("Failed to close already-resolved issue", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
    }

    // 2. Update project board status to Done via GraphQL
    try {
      await updateProjectItemStatus(issueNumber, "Done", workspaceRoot, this.logger);
    } catch (err) {
      this.logger.warn("Project status sync failed during early-exit close", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
    }

    // 4. Clean up context files and branches via hook script
    const cleanupScript = path.join(
      workspaceRoot,
      "claude-plugins/nightgauge/hooks/lib/cleanup-context-files.sh"
    );
    if (fs.existsSync(cleanupScript)) {
      try {
        await execFileAsync("bash", [cleanupScript, String(issueNumber)], {
          ...ghExecOptions,
          timeout: 45000,
        });
      } catch (err) {
        this.logger.warn("Context cleanup failed during early-exit close", {
          issueNumber,
          err: err instanceof Error ? err.message : String(err),
        });
      }
    }

    // 5. Run epic completion check (this issue may be the last sub-issue)
    try {
      const binary = await BinaryResolver.fromVSCode().resolve();
      if (!binary) throw new Error("nightgauge binary not found");
      await execFileAsync(binary, ["epic", "check-completion", String(issueNumber), "--json"], {
        ...ghExecOptions,
        timeout: 60000,
      });
    } catch (err) {
      this.logger.warn("Epic completion check failed during early-exit close", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
    }
  }

  /**
   * Classify pipeline outcome for cost tracking (Issue #709, #3267).
   *
   * Resolution order:
   *   1. If ANY persisted stage gate result has `kind === "no_op"`, return
   *      `"skill-no-op"` — the gate framework detected a stage where the
   *      skill exited 0 but did not produce its expected post-state. This
   *      takes precedence over file-change heuristics because it is a
   *      stronger signal: the skill claimed success but the framework
   *      contradicted it. (Issue #3267)
   *   2. If feature-dev never ran, fall through to `"productive"` (legacy
   *      behaviour — no other classification information available).
   *   3. Otherwise read `dev-{N}.json` and emit `"verify-and-close"` when
   *      no files were created/modified, else `"productive"` (Issue #709).
   *
   * @param issueNumber - The issue number
   * @param completedStages - Stages that completed successfully
   * @returns The pipeline outcome classification
   */
  private classifyPipelineOutcome(
    issueNumber: number,
    completedStages: PipelineStage[]
  ): PipelineOutcomeType {
    // Issue #3267: gate-driven classification first. If any stage's gate
    // detected a no-op (kind=no_op), the run is `skill-no-op` regardless
    // of what the dev context records. Reads the runtime state snapshot
    // persisted next to the per-stage context files.
    if (
      HeadlessOrchestrator.classifyOutcomeFromGateResults(
        this.readStageGateResultsForRun(issueNumber)
      )
    ) {
      return "skill-no-op";
    }

    // If feature-dev never ran, we can't classify based on file changes
    if (!completedStages.includes("feature-dev")) {
      return "productive";
    }

    try {
      const devContextPath = this.getContextPath("dev", issueNumber);
      if (!fs.existsSync(devContextPath)) {
        // No dev context file means we can't determine — default to productive
        return "productive";
      }

      const devContext = JSON.parse(fs.readFileSync(devContextPath, "utf-8"));
      const filesChanged = devContext.files_changed;

      if (!filesChanged) {
        return "productive";
      }

      const created = Array.isArray(filesChanged.created) ? filesChanged.created : [];
      const modified = Array.isArray(filesChanged.modified) ? filesChanged.modified : [];

      if (created.length === 0 && modified.length === 0) {
        return "verify-and-close";
      }

      return "productive";
    } catch {
      // If we can't read the dev context, default to productive
      return "productive";
    }
  }

  /**
   * Read the persisted stage_gate_results map for an in-flight pipeline.
   *
   * The Go scheduler writes runtime state to `pipeline/state.json` after each
   * stage. The TS-side legacy path doesn't yet write this map, so this helper
   * returns an empty array when the file is missing or the field is absent —
   * the classifier falls through to the legacy heuristics in that case.
   * Exposed as a method so subclasses / tests can override the read path.
   */
  private readStageGateResultsForRun(
    issueNumber: number
  ): Array<{ kind?: string; passed: boolean; gate_name?: string; reason?: string }> {
    try {
      const cwd = this.pinnedWorkspaceRoot ?? this.getWorkingDirectory();
      const statePath = path.join(cwd, ".nightgauge", "pipeline", "state.json");
      if (!fs.existsSync(statePath)) return [];
      const stateBlob = JSON.parse(fs.readFileSync(statePath, "utf-8"));
      const map = stateBlob?.stageGateResults;
      if (!map || typeof map !== "object") return [];
      const out: Array<{ kind?: string; passed: boolean; gate_name?: string; reason?: string }> =
        [];
      for (const stageKey of Object.keys(map)) {
        const arr = map[stageKey];
        if (!Array.isArray(arr)) continue;
        for (const entry of arr) {
          if (entry && typeof entry === "object" && typeof entry.passed === "boolean") {
            out.push({
              kind: typeof entry.kind === "string" ? entry.kind : undefined,
              passed: entry.passed,
              gate_name: typeof entry.gate_name === "string" ? entry.gate_name : undefined,
              reason: typeof entry.reason === "string" ? entry.reason : undefined,
            });
          }
        }
      }
      // Issue #3267: only the runtime state.json restricted to this issue
      // matters. The Go scheduler writes per-issue state under pipeline/<N>/
      // historically, so guard against cross-issue contamination by also
      // checking issueNumber from the blob when present.
      const blobIssue = stateBlob?.issueNumber;
      if (typeof blobIssue === "number" && blobIssue !== issueNumber) {
        return [];
      }
      return out;
    } catch {
      return [];
    }
  }

  /**
   * Attempt to deterministically generate the context file a stage was
   * supposed to write but didn't (Issue #3267 — extracted from the
   * five-stage if/else cascade in runPipeline).
   *
   * For each of the five stages with a registered fallback generator
   * (issue-pickup, feature-planning, feature-dev, feature-validate,
   * pr-create), this helper:
   *   1. Calls `ContextAssembler.generateDeterministicContext(stage, N)` to
   *      synthesize the missing context from GitHub metadata, git diff,
   *      build/test results, etc.
   *   2. If the generator produced a file, re-runs `validateStageContextOutput`
   *      to confirm the new file is schema-valid.
   *
   * Returns:
   *   - { recovered: true }  on successful recovery (file generated AND
   *     re-validation passed). Caller should continue the pipeline.
   *   - { recovered: false, error } on failure (no fallback for this stage,
   *     generator returned false, or re-validation failed). The returned
   *     `error` carries the original validation failure message so the
   *     caller can surface it to the UI / state service.
   *
   * Pre-#3267 the same logic was duplicated five times inline. The behaviour
   * is preserved exactly; only the dispatch site changed.
   */
  private async attemptContextRecovery(
    stage: PipelineStage,
    issueNumber: number,
    originalError: Error
  ): Promise<{ recovered: true } | { recovered: false; error: Error }> {
    const recoverableStages: PipelineStage[] = [
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
    ];
    if (!recoverableStages.includes(stage)) {
      return { recovered: false, error: originalError };
    }

    this.logger.info(`${stage} context not written by subagent — generating deterministically`, {
      issueNumber,
    });
    const fallbackGenerated = await this.contextAssembler.generateDeterministicContext(
      stage,
      issueNumber
    );
    if (!fallbackGenerated.generated) {
      // Generator declined / failed (or, for issue-pickup, failed closed on
      // open blockedBy dependencies — #189) — surface the original error.
      return { recovered: false, error: originalError };
    }

    const retryError = await this.validateStageContextOutput(stage, issueNumber);
    if (!retryError) {
      this.logger.info("Fallback context file passed validation — continuing pipeline", {
        issueNumber,
        stage,
      });
      return { recovered: true };
    }
    return { recovered: false, error: retryError };
  }

  /**
   * Pure helper: returns true when ANY gate result is a no-op (Issue #3267).
   *
   * Static so the backfill script (`scripts/backfill-skill-no-op-outcomes.ts`)
   * can re-classify historical run records without instantiating a full
   * HeadlessOrchestrator. The script feeds the per-stage gate_results array
   * from the V2 record and gets back the same boolean.
   */
  static classifyOutcomeFromGateResults(
    gateResults: Array<{ kind?: string; passed: boolean }>
  ): boolean {
    if (!Array.isArray(gateResults)) return false;
    for (const r of gateResults) {
      if (!r) continue;
      // Explicit kind discriminator — preferred path (Issue #3267).
      if (r.kind === "no_op") return true;
      // Pre-#3267 records have no `kind`. Without it we cannot
      // disambiguate no-op from hard-fail without regex-matching the
      // Reason string, which the plan explicitly rejects. Pre-#3267
      // records therefore never produce skill-no-op retroactively
      // unless backfilled by the dedicated script.
    }
    return false;
  }

  private async reconcileCompletionSideEffects(
    issueNumber: number
  ): Promise<CompletionReconciliationResult> {
    const workspaceRoot = this.getWorkingDirectory();
    // Resolve GITHUB_TOKEN for Go binary (VSCode doesn't inherit shell env)
    const execEnv = { ...process.env };
    if (!execEnv.GITHUB_TOKEN) {
      try {
        const { stdout: tokenRaw } = await execFileAsync("gh", ["auth", "token"], {
          timeout: 5_000,
        });
        execEnv.GITHUB_TOKEN = tokenRaw.toString().trim();
      } catch {
        /* gh CLI not available — proceed without */
      }
    }
    const ghExecOptions = {
      encoding: "utf-8" as const,
      cwd: workspaceRoot,
      timeout: 30000,
      env: execEnv,
    };

    // Epic branch sub-issues: PRs merge to the epic branch, not main.
    // The issue stays open until the epic→main PR merges (which GitHub
    // auto-closes via "Closes #N"). Do NOT treat this as a failure.
    // NOTE: Read from state.json (persists after pipeline-finish) rather than
    // issue-{N}.json (gets cleaned up before reconciliation runs).
    try {
      const fs = require("fs");
      // Primary source: state.json (always persists)
      const statePath = `${workspaceRoot}/.nightgauge/pipeline/state.json`;
      let baseBranch = "";
      if (fs.existsSync(statePath)) {
        const state = JSON.parse(fs.readFileSync(statePath, "utf-8"));
        baseBranch = state.base_branch ?? "";
      }
      // Fallback: issue-{N}.json (if it still exists)
      if (!baseBranch) {
        const contextPath = `${workspaceRoot}/.nightgauge/pipeline/issue-${issueNumber}.json`;
        if (fs.existsSync(contextPath)) {
          const ctx = JSON.parse(fs.readFileSync(contextPath, "utf-8"));
          baseBranch = ctx.base_branch ?? "";
        }
      }
      // Fallback: pr-{N}.json has the actual PR base branch from pr-create,
      // which is always correct even when state.json has stale "main".
      // @see Issue #137 — state.json base_branch stays "main" when
      // create-feature-branch.sh detects an epic branch after state init.
      if (!baseBranch.startsWith("epic/")) {
        const prContextPath = `${workspaceRoot}/.nightgauge/pipeline/pr-${issueNumber}.json`;
        if (fs.existsSync(prContextPath)) {
          const prCtx = JSON.parse(fs.readFileSync(prContextPath, "utf-8"));
          if (prCtx.base_branch?.startsWith("epic/")) {
            baseBranch = prCtx.base_branch;
          }
        }
      }
      // Fallback: search for the merged PR's base branch via GitHub API.
      if (!baseBranch.startsWith("epic/")) {
        try {
          const { stdout: prBaseRaw } = await execFileAsync(
            "gh",
            [
              "pr",
              "list",
              "--state",
              "merged",
              "--search",
              `#${issueNumber} in:title`,
              "--json",
              "baseRefName",
              "-q",
              ".[0].baseRefName",
              ...this.ghRepoArgs(),
            ],
            ghExecOptions
          );
          const prBase = prBaseRaw.trim();
          if (prBase?.startsWith("epic/")) {
            baseBranch = prBase;
          }
        } catch {
          // Non-critical: fall through
        }
      }
      if (baseBranch.startsWith("epic/")) {
        // Sub-issues are closed when their PR merges to the epic branch.
        // Only the epic itself stays open until the epic→main PR merges.
        this.logger.info("Epic sub-issue — closing after merge to epic branch", {
          issueNumber,
          baseBranch,
        });
        try {
          await execFileAsync(
            "gh",
            [
              "issue",
              "close",
              String(issueNumber),
              "--comment",
              `Closing: PR merged to epic branch (${baseBranch}).`,
              ...this.ghRepoArgs(),
            ],
            ghExecOptions
          );
        } catch (closeErr) {
          this.logger.warn("Failed to close epic sub-issue", {
            issueNumber,
            err: closeErr instanceof Error ? closeErr.message : String(closeErr),
          });
        }

        // Delete the feature branch from remote. GitHub only auto-deletes
        // branches when a PR merges to the default branch (main). Epic
        // sub-issue PRs merge to epic/* branches, so the feature branch
        // lingers on the remote indefinitely. This deterministic cleanup
        // runs after every epic sub-issue merge.
        // @see Issue #1452 — stale feat/1462-* branch left on remote
        try {
          const { stdout: headRefRaw } = await execFileAsync(
            "gh",
            [
              "pr",
              "list",
              "--state",
              "merged",
              "--search",
              `#${issueNumber} in:title`,
              "--json",
              "headRefName",
              "-q",
              ".[0].headRefName",
              ...this.ghRepoArgs(),
            ],
            ghExecOptions
          );
          const headRef = headRefRaw.trim();
          if (headRef && headRef.startsWith("feat/")) {
            // Check if remote ref still exists (GitHub auto-deletes head branches)
            let refExists = false;
            try {
              await execFileAsync(
                "git",
                ["ls-remote", "--exit-code", "origin", `refs/heads/${headRef}`],
                ghExecOptions
              );
              refExists = true;
            } catch {
              refExists = false;
            }
            if (refExists) {
              await execFileAsync("git", ["push", "origin", "--delete", headRef], ghExecOptions);
              this.logger.info("Deleted feature branch after epic sub-issue merge", {
                issueNumber,
                branch: headRef,
                epicBranch: baseBranch,
              });
            } else {
              this.logger.debug(
                "Feature branch already deleted by GitHub (auto-delete head branches)",
                { issueNumber, branch: headRef }
              );
            }
          }
        } catch (branchErr) {
          // Non-critical: branch may already be deleted or protected
          this.logger.warn("Failed to delete feature branch after epic merge", {
            issueNumber,
            err: branchErr instanceof Error ? branchErr.message : String(branchErr),
          });
        }

        // After closing the sub-issue, attempt full epic completion.
        // Extract parent epic number from branch name (e.g., "epic/1650-..." → 1650).
        const epicMatch = baseBranch.match(/^epic\/(\d+)/);
        if (epicMatch) {
          const epicNumber = parseInt(epicMatch[1], 10);
          try {
            const binary = await BinaryResolver.fromVSCode().resolve();
            if (!binary) throw new Error("nightgauge binary not found");
            const { stdout: epicOutputRaw } = await execFileAsync(
              binary,
              ["epic", "complete", String(epicNumber), "--json"],
              { ...ghExecOptions, timeout: 120000 }
            );
            const epicOutput = epicOutputRaw.trim();
            if (epicOutput) {
              const epicResult = JSON.parse(epicOutput);
              if (epicResult.action === "closed_and_merged") {
                this.logger.info("Epic completed — PR merged and branches cleaned", {
                  issueNumber,
                  epicNumber,
                  prUrl: epicResult.prUrl,
                  prNumber: epicResult.prNumber,
                });
              } else if (epicResult.action === "closed_pr_created") {
                this.logger.info("Epic complete — PR created, manual merge required", {
                  issueNumber,
                  epicNumber,
                  prUrl: epicResult.prUrl,
                });
              } else if (epicResult.action === "not_complete") {
                this.logger.info("Epic not yet complete", {
                  issueNumber,
                  epicNumber,
                  closed: epicResult.closed,
                  total: epicResult.total,
                  open: epicResult.open,
                });
              }
            }
          } catch (epicErr) {
            this.logger.warn("Epic completion check failed", {
              issueNumber,
              epicNumber: epicMatch[1],
              err: epicErr instanceof Error ? epicErr.message : String(epicErr),
            });
          }
        }

        return { verified: true, issueClosed: true, epicDeferred: true };
      }
    } catch {
      // Non-critical: fall through to standard reconciliation
    }

    let issueState: string;
    try {
      const { stdout: stateRaw } = await execFileAsync(
        "gh",
        [
          "issue",
          "view",
          String(issueNumber),
          "--json",
          "state",
          "-q",
          ".state",
          ...this.ghRepoArgs(),
        ],
        ghExecOptions
      );
      issueState = stateRaw.trim();
    } catch (err) {
      this.logger.warn("Could not verify issue state after pipeline", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      return { verified: false, issueClosed: false };
    }

    let mergedPrNumber: number | undefined;
    if (issueState === "OPEN") {
      try {
        // Primary: search for closing keywords ("closes #N", "fixes #N")
        const { stdout: primaryRaw } = await execFileAsync(
          "gh",
          [
            "pr",
            "list",
            "--state",
            "merged",
            "--search",
            `closes #${issueNumber}`,
            "--json",
            "number",
            "-q",
            ".[0].number",
            ...this.ghRepoArgs(),
          ],
          ghExecOptions
        );
        let mergedPr = primaryRaw.trim();
        // Fallback: search for issue number in PR title (e.g., "feat(#110): ...")
        // Handles epic sub-issue PRs that use "Part of #PARENT" instead of "Closes #N"
        if (!mergedPr) {
          const { stdout: fallbackRaw } = await execFileAsync(
            "gh",
            [
              "pr",
              "list",
              "--state",
              "merged",
              "--search",
              `#${issueNumber} in:title`,
              "--json",
              "number",
              "-q",
              ".[0].number",
              ...this.ghRepoArgs(),
            ],
            ghExecOptions
          );
          mergedPr = fallbackRaw.trim();
        }
        if (mergedPr) {
          mergedPrNumber = Number.parseInt(mergedPr, 10);
        }
      } catch (err) {
        this.logger.warn("Failed to check merged PR linkage", {
          issueNumber,
          err: err instanceof Error ? err.message : String(err),
        });
      }

      if (mergedPrNumber) {
        try {
          await execFileAsync(
            "gh",
            [
              "issue",
              "close",
              String(issueNumber),
              "--comment",
              `Closing automatically: merged PR #${mergedPrNumber} completed this issue.`,
              ...this.ghRepoArgs(),
            ],
            ghExecOptions
          );
          issueState = "CLOSED";
          this.logger.info("Closed issue after successful pipeline", {
            issueNumber,
            mergedPrNumber,
          });
        } catch (err) {
          const errorMessage = err instanceof Error ? err.message : String(err);
          this.logger.error("Failed to auto-close issue after merge", {
            issueNumber,
            mergedPrNumber,
            err: errorMessage,
          });
          return {
            verified: true,
            issueClosed: false,
            mergedPrNumber,
            error: `Issue remains open after successful pipeline: failed to close via merged PR #${mergedPrNumber}.`,
          };
        }
      }

      // Fallback: check if commits referencing this issue exist on main
      // This handles cases where work was committed directly without a PR
      // (e.g., audit tasks, docs-only changes). See Issue #779
      if (!mergedPrNumber && issueState === "OPEN") {
        try {
          const { stdout: commitLogRaw } = await execFileAsync(
            "git",
            ["log", "main", "--oneline", `--grep=#${issueNumber}`, "--format=%h %s"],
            ghExecOptions
          );
          const commitLog = commitLogRaw.trim();
          if (commitLog) {
            const commitLines = commitLog.split("\n");
            const commitHashes = commitLines.map((line) => line.split(" ")[0]).join(", ");
            const commitSummary =
              commitLines.length === 1
                ? `Commit: ${commitLines[0]}`
                : `${commitLines.length} commits found (${commitHashes})`;
            try {
              await execFileAsync(
                "gh",
                [
                  "issue",
                  "close",
                  String(issueNumber),
                  "--comment",
                  `Closing automatically: work committed directly to main (no PR). ${commitSummary}`,
                  ...this.ghRepoArgs(),
                ],
                ghExecOptions
              );
              issueState = "CLOSED";
              this.logger.info("Closed issue via commit-based detection (no PR)", {
                issueNumber,
                commitCount: commitLines.length,
                commitHashes,
              });
            } catch (closeErr) {
              const errorMessage = closeErr instanceof Error ? closeErr.message : String(closeErr);
              this.logger.error("Failed to auto-close issue via commit detection", {
                issueNumber,
                err: errorMessage,
              });
              return {
                verified: true,
                issueClosed: false,
                error: `Issue remains open: commits found on main but failed to close issue. ${commitSummary}`,
              };
            }
          }
        } catch (err) {
          this.logger.warn("Failed to check commits on main for issue", {
            issueNumber,
            err: err instanceof Error ? err.message : String(err),
          });
        }
      }
    }

    if (issueState !== "CLOSED") {
      return {
        verified: true,
        issueClosed: false,
        mergedPrNumber,
        error:
          mergedPrNumber === undefined
            ? 'Issue remains open after successful pipeline and no merged PR or commits with "#N" were found on main.'
            : undefined,
      };
    }

    // Best-effort: ensure project board status is synced to Done.
    try {
      await updateProjectItemStatus(issueNumber, "Done", workspaceRoot, this.logger);
    } catch (err) {
      this.logger.warn("Project status sync after completion failed", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
    }

    let epicSweepClosed = 0;
    try {
      const binary = await BinaryResolver.fromVSCode().resolve();
      if (!binary) {
        this.logger.debug("Epic completion sweep skipped — binary not found");
      } else {
        const { stdout: outputRaw } = await execFileAsync(
          binary,
          ["epic", "check-completion", "--sweep", "--json"],
          { ...ghExecOptions, timeout: 60000 }
        );
        const output = outputRaw.trim();
        if (output) {
          // --sweep returns [{epicNumber, complete, ...}] array, but may return
          // {"skipped":true,...} when rate-limited — guard before calling .filter()
          const parsed = JSON.parse(output);
          const results = Array.isArray(parsed) ? (parsed as Array<{ complete?: boolean }>) : [];
          epicSweepClosed = results.filter((r) => r.complete).length;
        }
      }
    } catch (err) {
      this.logger.warn("Epic completion check after pipeline failed", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
        stack: err instanceof Error ? err.stack : undefined,
      });
    }

    // Best-effort: sync CLOSED sub-issues' project board status to Done.
    // When an epic->main PR merges, GitHub auto-closes sub-issues but their
    // project board Status field is never updated (the sub-issue pipeline
    // returns early at the epicDeferred guard above). This fills that gap.
    try {
      const { stdout: nwoRaw } = await execFileAsync(
        "gh",
        ["repo", "view", "--json", "owner,name", "-q", '.owner.login + "/" + .name'],
        { ...ghExecOptions, timeout: 10000 }
      );
      const nwo = nwoRaw.trim();
      const [owner, repo] = nwo.split("/");
      if (owner && repo) {
        const { stdout: subIssuesRaw } = await execFileAsync(
          "gh",
          [
            "api",
            "graphql",
            "-f",
            `query={ repository(owner: "${owner}", name: "${repo}") { issue(number: ${issueNumber}) { subIssues(first: 50) { nodes { number state } } } } }`,
          ],
          { ...ghExecOptions, timeout: 15000 }
        );
        const parsed = JSON.parse(subIssuesRaw.trim());
        const subIssues: { number: number; state: string }[] =
          parsed?.data?.repository?.issue?.subIssues?.nodes ?? [];
        const closedSubIssues = subIssues.filter((si) => si.state === "CLOSED");
        for (const si of closedSubIssues) {
          try {
            await updateProjectItemStatus(si.number, "Done", workspaceRoot, this.logger);
          } catch {
            // Best-effort per sub-issue
          }
        }
        if (closedSubIssues.length > 0) {
          this.logger.info("Synced closed sub-issues to Done on project board", {
            issueNumber,
            subIssueCount: closedSubIssues.length,
            subIssues: closedSubIssues.map((si) => si.number),
          });
        }
      }
    } catch {
      // Non-blocking: sub-issue board sync is best-effort
    }

    const refreshScript = path.join(
      workspaceRoot,
      "claude-plugins/nightgauge/hooks/lib/trigger-refresh.sh"
    );
    if (fs.existsSync(refreshScript)) {
      try {
        await execFileAsync("bash", [refreshScript], {
          ...ghExecOptions,
          timeout: 10000,
        });
      } catch {
        // Non-blocking: manual refresh command remains available.
      }
    }

    return {
      verified: true,
      issueClosed: true,
      mergedPrNumber,
      epicSweepClosed,
    };
  }

  /**
   * Get the current routing decision
   */
  getRoutingDecision(): RoutingDecision | null {
    return this.currentRoutingDecision;
  }

  // ===================================================================
  // MODEL ESCALATION ENGINE (Issue #1343)
  //
  // Reads MODEL_ESCALATION_NEEDED signals from completed stage context
  // files, evaluates escalation eligibility, and executes same-stage
  // retries with a more capable model (haiku → sonnet → opus).
  // Escalation fires BEFORE backtrack evaluation.
  // ===================================================================

  /**
   * Read a MODEL_ESCALATION_NEEDED signal from a completed stage's context file.
   *
   * Returns the first blocking MODEL_ESCALATION_NEEDED signal found, or null.
   * Unlike readFeedbackSignals(), this does NOT require backtrack_target_stage.
   *
   * @see Issue #1343 - Dynamic Model Escalation Engine
   */
  private readEscalationSignal(
    stage: PipelineStage,
    issueNumber: number
  ): PipelineFeedbackSignal | null {
    const contextTypeMap: Partial<Record<PipelineStage, ContextFileType>> = {
      "feature-dev": "dev",
      "feature-validate": "validate",
    };
    const contextType = contextTypeMap[stage];
    if (!contextType) return null;

    try {
      const contextPath = this.getContextPath(contextType, issueNumber);
      if (!fs.existsSync(contextPath)) return null;
      const parsed = JSON.parse(fs.readFileSync(contextPath, "utf-8"));
      if (!Array.isArray(parsed.feedback)) return null;

      return (
        (parsed.feedback as PipelineFeedbackSignal[]).find(
          (s) => s.signal_type === "MODEL_ESCALATION_NEEDED" && s.severity === "blocking"
        ) ?? null
      );
    } catch {
      return null;
    }
  }

  /**
   * Decide whether a failed stage should fall back Fable → Opus.
   *
   * True only when the error is a usage/quota-limit signal, the stage's effective
   * model was Fable, and we haven't already fallen back for this stage this run.
   * Fable has a separate Max-plan usage bucket from Opus/Sonnet, so a Fable-only
   * exhaustion should retry on Opus rather than pause the whole pipeline for the
   * global cooldown.
   */
  private shouldFallbackFableToOpus(stage: PipelineStage, error: Error | undefined): boolean {
    if (this.fableQuotaFallbackApplied.has(stage)) return false;
    if (!isUsageLimitError(error?.message)) return false;
    const effectiveModel =
      this.stageModelOverrides.get(stage) ?? resolveModel(stage, this.getWorkingDirectory()).model;
    return effectiveModel === "fable";
  }

  /**
   * Evaluate whether a model escalation is allowed.
   *
   * Checks:
   * 1. Ceiling guard (getEscalatedModel returns null when at opus)
   * 2. max_escalations_per_stage limit
   *
   * Returns the next model if escalation is allowed, null if blocked.
   *
   * @see Issue #1343 - Dynamic Model Escalation Engine
   */
  private evaluateEscalation(
    stage: PipelineStage,
    signal: PipelineFeedbackSignal,
    issueNumber: number,
    callbacks?: PipelineCallbacks
  ): import("../utils/incrediConfig").DefaultModel | null {
    const currentModel = (this.stageModelOverrides.get(stage) ??
      resolveModel(stage, this.getWorkingDirectory())
        .model) as import("../utils/incrediConfig").DefaultModel;

    const nextModel = getEscalatedModel(currentModel);

    if (!nextModel) {
      const reason = "escalation_ceiling_reached";
      this.logger.warn("Escalation blocked: already at most capable model", {
        issueNumber,
        stage,
        currentModel,
      });
      this.eventDispatcher.onEscalationBlocked(reason, signal);
      return null;
    }

    const stageCount = this.stageEscalationCounts.get(stage) ?? 0;
    const maxEscalations = getMaxEscalationsPerStage(this.getWorkingDirectory());

    if (stageCount >= maxEscalations) {
      const reason = "max_escalations_per_stage_exceeded";
      this.logger.warn("Escalation blocked: max_escalations_per_stage exceeded", {
        issueNumber,
        stage,
        stageCount,
        maxEscalations,
      });
      this.eventDispatcher.onEscalationBlocked(reason, signal);
      return null;
    }

    return nextModel;
  }

  /**
   * Execute a model escalation: update instance state, record in pipeline
   * state, log, and fire callback.
   *
   * @see Issue #1343 - Dynamic Model Escalation Engine
   */
  private async executeEscalation(
    stage: PipelineStage,
    signal: PipelineFeedbackSignal,
    nextModel: import("../utils/incrediConfig").DefaultModel,
    issueNumber: number,
    callbacks?: PipelineCallbacks
  ): Promise<void> {
    const fromModel =
      this.stageModelOverrides.get(stage) ?? resolveModel(stage, this.getWorkingDirectory()).model;

    const count = (this.stageEscalationCounts.get(stage) ?? 0) + 1;
    this.stageEscalationCounts.set(stage, count);
    this.stageModelOverrides.set(stage, nextModel);

    const record: import("../schemas/pipelineState").ModelEscalationRecord = {
      stage,
      from_model: fromModel,
      to_model: nextModel,
      rationale: signal.rationale,
      timestamp: new Date().toISOString(),
      attempt_number: count,
    };

    if (this.stateService) {
      try {
        await this.stateService.recordModelEscalation(record);
      } catch (err) {
        this.logger.warn("Failed to record model escalation in state", {
          issueNumber,
          err,
        });
      }
    }

    this.logger.info("Model escalation triggered", {
      issueNumber,
      stage,
      from: fromModel,
      to: nextModel,
      attempt: count,
    });

    this.eventDispatcher.onModelEscalated(record);
  }

  // ===================================================================
  // PRE-STAGE HEALTH CHECK — PROACTIVE MODEL ESCALATION (Issue #1394)
  //
  // Before each non-bookend stage, evaluates health trend slope and
  // per-stage failure rate. When health is declining (slope < -2%) and
  // the stage has a historically high failure rate (> 20%), proactively
  // escalates the model one tier to avoid a wasted first attempt.
  // Max 1 proactive escalation per pipeline run.
  // ===================================================================

  /**
   * Compute health trend slope from health-history.jsonl.
   *
   * Reads the last 5 snapshots, computes percent change between the
   * average of the most recent 3 and the average of the older 2.
   * Returns 0 if insufficient data (< 5 snapshots).
   *
   * Uses synchronous file reads to match existing HeadlessOrchestrator
   * patterns (readEscalationSignal, loadIssueMetadata).
   *
   * @see Issue #1394
   */
  private computeHealthTrendSlope(): number {
    try {
      const workspaceRoot = this.getWorkingDirectory();
      const filePath = path.join(workspaceRoot, ".nightgauge", "pipeline", "health-history.jsonl");

      if (!fs.existsSync(filePath)) return 0;

      const content = fs.readFileSync(filePath, "utf-8");
      const lines = content.split("\n").filter((l) => l.trim());

      // Parse snapshots only (skip recalibration markers)
      const snapshots: Array<{ score: number; timestamp: string }> = [];
      for (const line of lines) {
        try {
          const parsed = JSON.parse(line);
          if (typeof parsed.score === "number" && parsed.type !== "recalibration") {
            snapshots.push({
              score: parsed.score,
              timestamp: parsed.timestamp,
            });
          }
        } catch {
          // Skip malformed lines
        }
      }

      if (snapshots.length < 5) return 0;

      // Sort by timestamp descending, take last 5
      snapshots.sort((a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime());
      const recent5 = snapshots.slice(0, 5);

      // Recent 3 avg vs older 2 avg
      const recent3Avg = (recent5[0].score + recent5[1].score + recent5[2].score) / 3;
      const older2Avg = (recent5[3].score + recent5[4].score) / 2;

      if (older2Avg === 0) return 0;

      return ((recent3Avg - older2Avg) / older2Avg) * 100;
    } catch {
      return 0;
    }
  }

  /**
   * Compute failure rate for a specific stage from execution history JSONL.
   *
   * Reads JSONL files from the last 30 days and counts how many runs had
   * the given stage with 'failed' status vs total runs with that stage.
   * Returns 0 if insufficient data (< 5 records with the stage).
   *
   * Uses synchronous file reads to match HeadlessOrchestrator patterns.
   *
   * @see Issue #1394
   */
  private computeStageFailureRate(stage: PipelineStage): number {
    try {
      const workspaceRoot = this.getWorkingDirectory();
      const historyDir = path.join(workspaceRoot, ".nightgauge", "pipeline", "history");

      if (!fs.existsSync(historyDir)) return 0;

      // List JSONL files from last 30 days
      let files: string[];
      try {
        files = fs
          .readdirSync(historyDir)
          .filter((f) => f.endsWith(".jsonl"))
          .sort();
      } catch {
        return 0;
      }

      // Filter to last 30 days
      const cutoff = new Date();
      cutoff.setDate(cutoff.getDate() - 30);
      const cutoffStr = cutoff.toISOString().split("T")[0];
      const recentFiles = files.filter((f) => f.replace(".jsonl", "") >= cutoffStr);

      let totalWithStage = 0;
      let failedCount = 0;

      for (const file of recentFiles) {
        try {
          const content = fs.readFileSync(path.join(historyDir, file), "utf-8");
          for (const line of content.split("\n")) {
            const trimmed = line.trim();
            if (!trimmed) continue;
            try {
              const record = JSON.parse(trimmed);
              if (record.record_type !== "run") continue;
              const stageDetail = record.stages?.[stage];
              if (!stageDetail) continue;

              totalWithStage++;
              if (stageDetail.status === "failed") {
                failedCount++;
              }
            } catch {
              // Skip malformed lines
            }
          }
        } catch {
          // Skip unreadable files
        }
      }

      if (totalWithStage < 5) return 0;

      return failedCount / totalWithStage;
    } catch {
      return 0;
    }
  }

  /**
   * Evaluate whether a proactive model escalation should be applied
   * before running the given stage.
   *
   * Guards:
   * 1. Already used proactive escalation this run → null
   * 2. Bookend or issue-pickup stage → null
   * 3. Health slope >= -2% → null (health not declining)
   * 4. Stage failure rate <= 20% → null (stage historically stable)
   * 5. Already at model ceiling → null
   *
   * When all conditions are met, applies the escalation by setting
   * stageModelOverrides, records it in pipeline state, and fires
   * the onProactiveEscalation callback.
   *
   * @returns The escalated model if applied, null otherwise
   * @see Issue #1394
   */
  private evaluatePreStageHealth(
    stage: PipelineStage,
    issueNumber: number,
    callbacks?: PipelineCallbacks
  ): import("../utils/incrediConfig").DefaultModel | null {
    // Guard: already used proactive escalation this run
    if (this.proactiveEscalationApplied) return null;

    // Guard: skip bookend stages and issue-pickup (no meaningful history)
    if (isBookendStage(stage) || stage === "issue-pickup") return null;

    // 1. Compute health trend slope
    const healthSlope = this.computeHealthTrendSlope();
    // Guard: not declining (slope >= -2%)
    if (healthSlope >= -2) return null;

    // 2. Compute stage failure rate
    const stageFailureRate = this.computeStageFailureRate(stage);
    // Guard: stage is historically stable (failure rate <= 20%)
    if (stageFailureRate <= 0.2) return null;

    // 3. Compute escalated model
    const currentModel = (this.stageModelOverrides.get(stage) ??
      resolveModel(stage, this.getWorkingDirectory())
        .model) as import("../utils/incrediConfig").DefaultModel;
    const nextModel = getEscalatedModel(currentModel);
    if (!nextModel) return null; // Already at ceiling

    // 4. Apply proactive escalation
    this.proactiveEscalationApplied = true;
    this.stageModelOverrides.set(stage, nextModel);

    const record: import("../schemas/pipelineState").ProactiveEscalationRecord = {
      stage,
      from_model: currentModel,
      to_model: nextModel,
      health_trend_slope: healthSlope,
      stage_failure_rate: stageFailureRate,
      rationale: `Proactive escalation: health trend slope ${healthSlope.toFixed(1)}% (< -2%) and ${stage} failure rate ${(stageFailureRate * 100).toFixed(0)}% (> 20%)`,
      timestamp: new Date().toISOString(),
    };

    // Log with evidence
    this.logger.info("Proactive model escalation applied", {
      issueNumber,
      stage,
      fromModel: currentModel,
      toModel: nextModel,
      healthTrendSlope: healthSlope,
      stageFailureRate,
    });

    // Record in pipeline state (fire-and-forget, non-blocking)
    if (this.stateService) {
      this.stateService.recordProactiveEscalation(record).catch((err) => {
        this.logger.warn("Failed to record proactive escalation", { err });
      });
    }

    // Fire callback
    this.eventDispatcher.onProactiveEscalation(record);

    return nextModel;
  }

  // ===================================================================
  // FEEDBACK LEARNING ENGINE (Issue #1348)
  //
  // Reads COMPLEXITY_UNDERESTIMATED signals from completed stage context
  // files and immediately updates the complexity model, without waiting
  // for post-merge outcome recording via OutcomeRecorder.
  // ===================================================================

  /**
   * Apply feedback learning for COMPLEXITY_UNDERESTIMATED signals.
   *
   * Reads all feedback signals from the completed stage's context file,
   * finds any COMPLEXITY_UNDERESTIMATED signal (warning or blocking), and
   * records it in the complexity model via FeedbackLearningService.
   *
   * Non-fatal: errors are logged as warnings and do not block the pipeline.
   *
   * @see Issue #1348
   */
  private async applyFeedbackLearning(stage: PipelineStage, issueNumber: number): Promise<void> {
    const contextTypeMap: Partial<Record<PipelineStage, ContextFileType>> = {
      "feature-dev": "dev",
      "feature-validate": "validate",
    };
    const contextType = contextTypeMap[stage];
    if (!contextType) return;

    try {
      const contextPath = this.getContextPath(contextType, issueNumber);
      if (!fs.existsSync(contextPath)) return;

      const parsed = JSON.parse(fs.readFileSync(contextPath, "utf-8"));
      if (!Array.isArray(parsed.feedback) || parsed.feedback.length === 0) return;

      const underestimationSignal = (parsed.feedback as PipelineFeedbackSignal[]).find(
        (s) => s.signal_type === "COMPLEXITY_UNDERESTIMATED"
      );

      if (!underestimationSignal) return;

      // Load issue context for title, description, labels
      const issueContextPath = this.getIssueContextPath(issueNumber);
      if (!fs.existsSync(issueContextPath)) return;

      const issueRaw = JSON.parse(fs.readFileSync(issueContextPath, "utf-8"));
      const labels: string[] = Array.isArray(issueRaw.labels) ? issueRaw.labels : [];
      const issueTitle: string = issueRaw.title ?? "";
      const issueDescription: string = issueRaw.requirements?.summary ?? "";
      const predictedSize = this.extractSizeLabel(labels) ?? "S";
      const issueType = this.extractTypeLabel(labels) ?? "feature";

      const workspaceRoot = this.getWorkingDirectory();
      const modelService = new ComplexityModelService(
        path.join(workspaceRoot, ".nightgauge/complexity-model.yaml")
      );
      const learningService = new FeedbackLearningService(modelService);

      const result = await learningService.recordUnderestimation(
        issueNumber,
        predictedSize,
        issueType,
        issueTitle,
        issueDescription,
        underestimationSignal
      );

      if (result.skipped) {
        this.logger.debug("FeedbackLearning: skipped duplicate underestimation", {
          issueNumber,
        });
      } else {
        this.logger.info("FeedbackLearning: recorded underestimation", {
          issueNumber,
          patternsAdjusted: result.patternsAdjusted,
        });
      }
    } catch (error) {
      // Non-fatal: log warning but do not block the pipeline
      this.logger.warn("FeedbackLearning: failed to record underestimation", {
        issueNumber,
        error: error instanceof Error ? error.message : String(error),
      });
    }
  }

  // ===================================================================
  // REVIEWER FEEDBACK LEARNING (Issue #1409)
  //
  // Captures PR reviewer comments at pipeline-finish and feeds them
  // into the complexity model via FeedbackLearningService.
  // ===================================================================

  /**
   * Capture reviewer feedback from merged PR and adjust complexity model.
   *
   * Runs during pipeline-finish, after execution history is written but
   * before completeStage() and context cleanup. Non-fatal: all errors
   * are caught and logged as warnings.
   *
   * @see Issue #1409
   */
  private async captureReviewerFeedback(issueNumber: number): Promise<void> {
    const enabled = process.env.NIGHTGAUGE_FEEDBACK_REVIEWER_SIGNALS_ENABLED !== "false";
    if (!enabled) {
      this.logger.debug("ReviewerFeedback: disabled via env", { issueNumber });
      return;
    }

    const confidencePenalty = process.env.NIGHTGAUGE_FEEDBACK_REVIEWER_CONFIDENCE_PENALTY
      ? Number.parseFloat(process.env.NIGHTGAUGE_FEEDBACK_REVIEWER_CONFIDENCE_PENALTY)
      : 0.03;
    const minCommentLength = process.env.NIGHTGAUGE_FEEDBACK_REVIEWER_MIN_COMMENT_LENGTH
      ? Number.parseInt(process.env.NIGHTGAUGE_FEEDBACK_REVIEWER_MIN_COMMENT_LENGTH, 10)
      : 10;

    try {
      // Read pr-{N}.json to get PR number
      const prContextPath = this.getContextPath("pr", issueNumber);
      if (!fs.existsSync(prContextPath)) {
        this.logger.debug("ReviewerFeedback: no pr context file", {
          issueNumber,
        });
        return;
      }

      const prContext = JSON.parse(fs.readFileSync(prContextPath, "utf-8"));
      const prNumber = prContext.pr_number;
      if (!prNumber) {
        this.logger.debug("ReviewerFeedback: no PR number in context", {
          issueNumber,
        });
        return;
      }

      // Fetch PR reviews via gh CLI
      let reviewsJson: string;
      try {
        const { stdout: reviewsRaw } = await execFileAsync(
          "gh",
          ["pr", "view", String(prNumber), "--json", "reviews", "-q", ".reviews"],
          {
            encoding: "utf-8",
            cwd: this.getWorkingDirectory(),
            timeout: 15000,
          }
        );
        reviewsJson = reviewsRaw.trim();
      } catch {
        this.logger.debug("ReviewerFeedback: gh pr view failed", {
          issueNumber,
          prNumber,
        });
        return;
      }

      if (!reviewsJson || reviewsJson === "[]" || reviewsJson === "null") {
        this.logger.debug("ReviewerFeedback: no reviews found", {
          issueNumber,
          prNumber,
        });
        return;
      }

      const reviews: Array<{
        body: string;
        state: string;
        author: { login: string };
      }> = JSON.parse(reviewsJson);

      if (!Array.isArray(reviews) || reviews.length === 0) {
        return;
      }

      // Map reviews to comment objects for parsing
      const comments = reviews
        .filter((r) => r.body && r.body.length > 0)
        .map((r) => ({
          body: r.body,
          reviewer_login: r.author?.login ?? "unknown",
          verdict: (r.state === "APPROVED"
            ? "APPROVED"
            : r.state === "CHANGES_REQUESTED"
              ? "CHANGES_REQUESTED"
              : "COMMENTED") as "APPROVED" | "CHANGES_REQUESTED" | "COMMENTED",
        }));

      // Parse comments into reviewer signals
      const workspaceRoot = this.getWorkingDirectory();
      const modelService = new ComplexityModelService(
        path.join(workspaceRoot, ".nightgauge/complexity-model.yaml")
      );
      const learningService = new FeedbackLearningService(modelService);

      const signals = learningService.parseReviewerComments(comments, minCommentLength);

      if (signals.length === 0) {
        this.logger.debug("ReviewerFeedback: no signals detected", {
          issueNumber,
          prNumber,
          reviewCount: reviews.length,
        });
        return;
      }

      // Load issue context for title/description/labels
      const issueContextPath = this.getIssueContextPath(issueNumber);
      if (!fs.existsSync(issueContextPath)) {
        this.logger.debug("ReviewerFeedback: no issue context", {
          issueNumber,
        });
        return;
      }

      const issueRaw = JSON.parse(fs.readFileSync(issueContextPath, "utf-8"));
      const labels: string[] = Array.isArray(issueRaw.labels) ? issueRaw.labels : [];
      const issueTitle: string = issueRaw.title ?? "";
      const issueDescription: string = issueRaw.requirements?.summary ?? "";
      const predictedSize = this.extractSizeLabel(labels) ?? "S";
      const issueType = this.extractTypeLabel(labels) ?? "feature";

      // Determine overall verdict from reviews
      const hasChangesRequested = reviews.some((r) => r.state === "CHANGES_REQUESTED");
      const overallVerdict = hasChangesRequested ? "CHANGES_REQUESTED" : "APPROVED";

      const result = await learningService.processReviewerFeedback(
        issueNumber,
        predictedSize,
        issueType,
        issueTitle,
        issueDescription,
        signals,
        overallVerdict,
        confidencePenalty
      );

      if (result.skipped) {
        this.logger.debug("ReviewerFeedback: skipped (already recorded)", {
          issueNumber,
        });
      } else {
        this.logger.info("ReviewerFeedback: recorded signals", {
          issueNumber,
          signalsProcessed: result.signalsProcessed,
          patternsAdjusted: result.patternsAdjusted,
        });
      }
    } catch (error) {
      // Non-fatal: never block pipeline completion
      this.logger.warn("ReviewerFeedback: failed to capture", {
        issueNumber,
        error: error instanceof Error ? error.message : String(error),
      });
    }
  }

  // ===================================================================
  // BACKTRACK ENGINE (Issue #1342)
  //
  // Reads feedback signals from completed stage context files,
  // evaluates backtrack eligibility, and executes backward transitions.
  // ===================================================================

  /**
   * Read feedback signals from a completed stage's context file.
   *
   * Extracts the `.feedback` array from dev-{N}.json or validate-{N}.json,
   * filters for blocking signals with a backtrack_target_stage, and excludes
   * MODEL_ESCALATION_NEEDED signals (which retry same stage, not backtrack).
   */
  private readFeedbackSignals(stage: PipelineStage, issueNumber: number): PipelineFeedbackSignal[] {
    const contextTypeMap: Partial<Record<PipelineStage, ContextFileType>> = {
      "feature-dev": "dev",
      "feature-validate": "validate",
    };

    const contextType = contextTypeMap[stage];
    if (!contextType) return [];

    try {
      const contextPath = this.getContextPath(contextType, issueNumber);
      if (!fs.existsSync(contextPath)) return [];

      const content = fs.readFileSync(contextPath, "utf-8");
      const parsed = JSON.parse(content);

      if (!Array.isArray(parsed.feedback) || parsed.feedback.length === 0) {
        return [];
      }

      return parsed.feedback.filter(
        (signal: PipelineFeedbackSignal) =>
          signal.severity === "blocking" &&
          signal.backtrack_target_stage != null &&
          signal.signal_type !== "MODEL_ESCALATION_NEEDED"
      );
    } catch (err) {
      this.logger.warn("Failed to read feedback signals from context file", {
        stage,
        issueNumber,
        err,
      });
      return [];
    }
  }

  /**
   * Evaluate whether a backtrack is allowed.
   *
   * Checks:
   * 1. max_backtracks limit (from config)
   * 2. Oscillation guard (same from→to edge already traversed)
   *
   * @returns true if backtrack is allowed
   */
  private evaluateBacktrack(
    signal: PipelineFeedbackSignal,
    currentStage: PipelineStage,
    issueNumber: number,
    callbacks?: PipelineCallbacks
  ): boolean {
    const targetStage = signal.backtrack_target_stage!;
    const maxBacktracks = getMaxBacktracks(this.getWorkingDirectory());

    // Check hard limit
    if (this.backtrackCount >= maxBacktracks) {
      const reason = "max_backtracks_exceeded";
      this.logger.warn("Backtrack blocked: max_backtracks exceeded", {
        issueNumber,
        currentStage,
        targetStage,
        backtrackCount: this.backtrackCount,
        maxBacktracks,
      });
      this.eventDispatcher.onBacktrackBlocked(reason, signal);
      return false;
    }

    // Check oscillation guard
    const edgeKey = `${currentStage}->${targetStage}`;
    if (this.traversedEdges.has(edgeKey)) {
      const reason = "oscillation_detected";
      this.logger.warn("Backtrack blocked: oscillation detected", {
        issueNumber,
        currentStage,
        targetStage,
        edgeKey,
      });
      this.eventDispatcher.onBacktrackBlocked(reason, signal);
      return false;
    }

    return true;
  }

  /**
   * Execute a backtrack: write feedback file, update state, record the edge.
   *
   * @returns Index of the target stage in STAGE_ORDER for loop rewinding
   */
  private async executeBacktrack(
    signal: PipelineFeedbackSignal,
    currentStage: PipelineStage,
    issueNumber: number,
    callbacks?: PipelineCallbacks
  ): Promise<number> {
    const targetStage = signal.backtrack_target_stage as PipelineStage;
    const edgeKey = `${currentStage}->${targetStage}`;
    this.traversedEdges.add(edgeKey);
    this.backtrackCount++;

    const record: import("../schemas/pipelineState").BacktrackRecord = {
      from_stage: currentStage,
      to_stage: targetStage,
      signal_type: signal.signal_type,
      rationale: signal.rationale,
      timestamp: new Date().toISOString(),
      attempt_number: this.backtrackCount,
    };

    // Write feedback-{N}.json for the target stage to read
    try {
      const feedbackPath = this.getContextPath("feedback", issueNumber);
      const feedbackContext = {
        schema_version: "1.0",
        issue_number: issueNumber,
        signals: [signal],
        created_at: new Date().toISOString(),
      };
      fs.writeFileSync(feedbackPath, JSON.stringify(feedbackContext, null, 2));
      this.logger.info("Wrote feedback context file for backtrack", {
        issueNumber,
        path: feedbackPath,
      });
    } catch (err) {
      this.logger.warn("Failed to write feedback context file", {
        issueNumber,
        err,
      });
    }

    // Record backtrack in pipeline state
    if (this.stateService) {
      const targetIndex = STAGE_ORDER.indexOf(targetStage);
      const currentIndex = STAGE_ORDER.indexOf(currentStage);
      const intermediateStages = STAGE_ORDER.slice(targetIndex + 1, currentIndex);
      try {
        await this.stateService.recordBacktrack(record, intermediateStages);
      } catch (err) {
        this.logger.warn("Failed to record backtrack in state", {
          issueNumber,
          err,
        });
      }
    }

    this.logger.info("Backtrack triggered", {
      issueNumber,
      from: currentStage,
      to: targetStage,
      signalType: signal.signal_type,
      attemptNumber: this.backtrackCount,
    });

    this.eventDispatcher.onBacktrackTriggered(record);

    return STAGE_ORDER.indexOf(targetStage);
  }

  /**
   * Run the full pipeline from pipeline-start to pipeline-finish
   *
   * The pipeline includes bookend stages (pipeline-start, pipeline-finish) that
   * execute synchronously with zero AI token consumption. These provide reliable
   * synchronization points for initialization and cleanup.
   *
   * @param issueNumber - The GitHub issue number to work on
   * @param callbacks - Callbacks for pipeline events
   * @returns Result of the pipeline run
   */
  async runPipeline(
    issueNumber: number,
    callbacks?: PipelineCallbacks,
    modelOverride?: PipelineModelOverride
  ): Promise<PipelineRunResult> {
    // Per-issue in-flight registry, second line of defense behind the
    // dispatch-boundary guard in ConcurrentPipelineManager (#188). The
    // per-instance isRunning throw below cannot catch a duplicate dispatch
    // through a SECOND orchestrator instance — which is exactly how
    // bowlsheet#233 double-ran (two pre-flights 3s apart, racing on the
    // same context files and worktree). Static: spans all instances in
    // this extension host. Log + refuse rather than double-run.
    if (HeadlessOrchestrator.activePipelineIssues.has(issueNumber)) {
      this.logger.error(
        "Duplicate runPipeline dispatch refused — issue already has a pipeline in flight (#188)",
        { issueNumber }
      );
      return {
        success: false,
        completedStages: [],
        skippedStages: [],
        deferredStages: [],
        error: new Error(
          `Duplicate dispatch refused: issue #${issueNumber} already has a pipeline in flight (#188)`
        ),
        totalDurationMs: 0,
      };
    }
    HeadlessOrchestrator.activePipelineIssues.add(issueNumber);
    try {
      return await this.runPipelineInner(issueNumber, callbacks, modelOverride);
    } finally {
      HeadlessOrchestrator.activePipelineIssues.delete(issueNumber);
    }
  }

  /** Issues with a runPipeline currently in flight, across all orchestrator instances (#188). */
  private static activePipelineIssues = new Set<number>();

  private async runPipelineInner(
    issueNumber: number,
    callbacks?: PipelineCallbacks,
    modelOverride?: PipelineModelOverride
  ): Promise<PipelineRunResult> {
    if (this.isRunning) {
      throw new Error("Pipeline is already running");
    }

    // Consume pending model override if no explicit override was passed (#1610).
    if (!modelOverride && this.pendingUserModelOverride) {
      modelOverride = this.pendingUserModelOverride;
    }
    this.pendingUserModelOverride = null;

    // Merge defaultPipelineCallbacks so resume and auto-resume paths receive
    // OutputWindow integration without explicitly passing callbacks. (#1532)
    // Explicit callbacks take precedence — same spread order as startNextQueuedIssue().
    callbacks = { ...(this.defaultPipelineCallbacks ?? {}), ...callbacks };

    // Create event dispatcher for this run (Issue #2770 — Part 3)
    this.eventDispatcher = new OrchestratorEventDispatcher(callbacks, this.logger);

    this.isRunning = true;
    this.abortController = new AbortController();
    this.completedStageSet.clear(); // Reset duplicate-prevention tracker (#698)
    this.cachedIssueMetadata = null; // Clear stale metadata from previous run (#732)
    this.accumulatedToolCalls = []; // Reset tool call accumulator (#1004)
    this.pendingToolCalls.clear(); // Reset pending tool call tracker (#1031)
    this.stageValidationErrors.clear(); // Reset skill amendment error accumulator
    this.stageRepairAttempts.clear(); // Reset context repair tracker (#2552)
    this.stageExecutionPaths.clear(); // Reset execution-path decision tracker (#297)

    // Sync contextAssembler per-run state (Issue #2770 — Part 3)
    this.contextAssembler.clearSessionState();
    this.contextAssembler.setContextFileWaitMs(this.config.contextFileWaitMs);
    this.contextAssembler.setRepoOverride(this.repoOverride);
    this.contextAssembler.setRoutingConfig(this.config.routing ?? {});
    this.contextAssembler.setForceFullPipeline(this.config.forceFullPipeline);
    this.stageStallEvents.clear(); // Reset stall event accumulator (#2652)
    this.backtrackCount = 0; // Reset backtrack engine state (#1342)
    this.traversedEdges.clear(); // Reset oscillation guard (#1342)
    this.stageEscalationCounts.clear(); // Reset escalation engine (#1343)
    this.stageModelOverrides.clear(); // Reset escalation overrides (#1343)
    this.fableQuotaFallbackApplied.clear(); // Reset Fable→Opus usage-limit fallback guard
    this.fableFallbacks = []; // Reset surfaced Fable→Opus fallback list (#26)

    // Pre-populate model overrides for all skill stages when user selects
    // a model override via "Run Pipeline with Model" (#1610).
    this.userModelOverride = modelOverride ?? null;
    const executionAdapter = getExecutionAdapter(this.getWorkingDirectory());
    if (modelOverride && executionAdapter === "claude") {
      for (const stage of SKILL_STAGES) {
        this.stageModelOverrides.set(
          stage,
          modelOverride as import("../utils/incrediConfig").DefaultModel
        );
      }
      this.logger.info("User model override applied to all stages", {
        model: modelOverride,
        issueNumber,
        adapter: executionAdapter,
      });
    } else if (modelOverride) {
      this.logger.info("User model override captured for adapter-aware stage execution", {
        model: modelOverride,
        issueNumber,
        adapter: executionAdapter,
      });
    }

    this.proactiveEscalationApplied = false; // Reset proactive escalation guard (#1394)
    this.policyRetryBudgetIncrease = 0; // Reset health policy retry budget (#1395)
    this.pauseAutoRouting = false; // Reset health policy auto-routing pause (#1395)
    this.activePolicies = null; // Reset health policy overrides (#1395)

    // Pin workspace root for the entire pipeline run so that a repo switch
    // mid-pipeline doesn't cause subsequent stages to spawn with the wrong CWD.
    // @see Issue #1592
    this.pinnedWorkspaceRoot = this.getWorkingDirectory();

    // Authenticate every `gh`/`gh api` subprocess this extension host spawns
    // (board-status writes via projectFieldWriter, post-create/merge gates, the
    // GITHUB_TOKEN resolution paths) as the pipeline's configured identity
    // rather than the machine's ambient active gh account. On a multi-account
    // machine the active account may lack target-org access, so those bare `gh`
    // calls fail with "Could not resolve to a Repository" — false-negative
    // gates and board-status WARNs (#3892). Resolve the token once via the Go
    // binary (config → GITHUB_TOKEN → `gh auth token --user`) and export it to
    // process.env so all descendant subprocesses inherit it.
    await this.ensureGitHubTokenInProcessEnv(this.pinnedWorkspaceRoot);

    // Detect if this call is a resume of a previously-failed run (Issue #1261).
    // A resume is identified by having at least one already-complete stage in
    // state before any new stages execute. This flag is stored on the instance
    // so runBookendStage (pipeline-finish) can read it when writing the history
    // record. Recovery-run costs are excluded from the Cost Trend component.
    this.currentRunIsRecovery = false;
    if (this.stateService) {
      const existingState = await this.stateService.getState();
      if (existingState?.stages) {
        this.currentRunIsRecovery = Object.values(existingState.stages).some(
          (s) => s?.status === "complete" || s?.status === "skipped"
        );

        // Restore backtrack/escalation state from state.json on resume so
        // oscillation detection and escalation limits survive across restarts.
        // @see Issue #1342, #1343
        if (this.currentRunIsRecovery) {
          this.backtrackCount = existingState.backtrack_count ?? 0;
          // Reconstruct traversed edges from backtrack history
          for (const bt of existingState.backtracks ?? []) {
            this.traversedEdges.add(`${bt.from_stage}->${bt.to_stage}`);
          }
          // Reconstruct per-stage escalation counts from escalation history
          for (const esc of existingState.model_escalations ?? []) {
            const stage = esc.stage as PipelineStage;
            this.stageEscalationCounts.set(stage, (this.stageEscalationCounts.get(stage) ?? 0) + 1);
          }
          this.logger.info("Restored backtrack/escalation state from state.json", {
            backtrackCount: this.backtrackCount,
            traversedEdges: this.traversedEdges.size,
            escalationStages: this.stageEscalationCounts.size,
          });
        }
      }
    }

    // ===================================================================
    // AUDIT EVENT CLIENT SETUP (Issue #1582)
    // Instantiate AuditEventClient when audit.enabled is true.
    // Generate a stable pipelineRunId UUID for event correlation.
    // All audit calls are fire-and-forget: never block pipeline execution.
    // ===================================================================
    const auditConfig = getAuditConfig(this.getWorkingDirectory());
    if (auditConfig.enabled) {
      this.auditClient = new AuditEventClient(auditConfig);
    }
    this.currentPipelineRunId = crypto.randomUUID();
    this.blockedTerminalState = null;
    this.prMergedGroundTruth = false;
    this.estimatorSnapshot = null;

    // Pin the run's target repo on the state service so every stage transition
    // carries the platform's run-creation context ("owner/name"). Without this
    // the Go IPC layer emits stage events with an empty repo and the live
    // Pipelines view never materialises the run (the "No pipeline runs yet"
    // symptom for extension/HeadlessOrchestrator runs). Best-effort: resolving
    // the repo and seeding telemetry context must never block or fail the run.
    try {
      this.stateService?.setRunRepo(await this.resolveRunRepoSlug());
    } catch (err) {
      this.logger.warn("Could not set run repo for telemetry — continuing", { err });
    }

    // Emit pipeline.started now that pipelineRunId is set
    this.auditClient?.enqueue({
      action: "pipeline.started",
      resourceType: "pipeline",
      resourceId: `issue-${issueNumber}`,
      metadata: {
        pipelineRunId: this.currentPipelineRunId,
        issueNumber,
        timestamp: new Date().toISOString(),
      },
    });

    const startTime = Date.now();
    const completedStages: PipelineStage[] = [];
    const skippedStages: PipelineStage[] = [];
    const deferredStages: PipelineStage[] = [];
    let failedStage: PipelineStage | undefined;
    let error: Error | undefined;
    let budgetExceeded = false;
    // #253: true when the between-stage ceiling check performed a controlled
    // stop. Suppresses the completion reconcile's pr-merge reclassification —
    // a budget stop mid-pipeline is not a pr-merge defect.
    let budgetCeilingStopped = false;

    // Reset cost anomaly flag for this run (Issue #1335)
    this._lastCostAnomalyExceeded = false;

    // ===================================================================
    // PIPELINE BUDGET CEILING (Issue #1047)
    // Initialize pipeline-level cost ceiling enforcer for the pre-flight
    // budget estimate below (a one-time snapshot, intentionally pinned like
    // the estimator inputs). The onTokenUsage checkpoint signal is handled by
    // a separate per-stage instance inside runStage(). The between-stage
    // hard-stop check further down re-resolves config fresh at every check
    // (Issue #257) rather than reusing this snapshot for the whole run.
    // ===================================================================
    const ceilingConfig = getPipelineCeilingConfig();
    const pipelineCeiling = new PipelineBudgetCeiling(ceilingConfig);

    // ===================================================================
    // ISSUE PRE-CHECKS: Deterministic, zero AI tokens consumed.
    // Single gh call fetches both labels and state to gate the pipeline.
    // 1. Epic check: halt/auto-close epics (@see Issue #525)
    // 2. Closed-issue check: halt already-completed issues (@see Issue #696)
    // ===================================================================
    const preCheck = await this.preCheckIssue(issueNumber);

    if (preCheck.isEpic) {
      this.logger.info("Epic detected via pre-check — halting before any AI stages", {
        issueNumber,
      });

      const epicResult = await this.handleEpicIssue(issueNumber, startTime);

      // Notify pipeline complete callback so UI updates
      this.firePipelineComplete(epicResult);

      // Notify queue service for auto-start of next issue
      if (this.queueService) {
        this.handleQueueAutoStart(epicResult.success, issueNumber);
      }

      this.isRunning = false;
      this.abortController = null;
      return epicResult;
    }

    if (!this.config.forceRerun && preCheck.isClosed) {
      this.logger.info("Closed issue detected via pre-check — halting before any AI stages", {
        issueNumber,
        state: "CLOSED",
      });

      const closedResult: PipelineRunResult = {
        success: false,
        completedStages: [],
        skippedStages: [],
        deferredStages: [],
        failedStage: "pipeline-start",
        error: new Error(
          `Issue #${issueNumber} is already CLOSED. Pipeline halted — zero AI tokens consumed.\n\n` +
            `Use forceRerun: true to override this guard for legitimate re-runs.`
        ),
        totalDurationMs: Date.now() - startTime,
        outcomeType: "already-resolved",
      };

      // Classify as already-resolved for cost tracking (Issue #709)
      if (this.stateService) {
        try {
          await this.stateService.setOutcomeType("already-resolved");
        } catch {
          // Non-critical
        }
      }

      this.eventDispatcher.onStderr(
        "pipeline-start",
        `[pipeline-start-failure] issue-closed: Issue #${issueNumber} is already CLOSED. ` +
          `Pipeline halted before any AI stages. Use forceRerun: true to override.\n`
      );

      // Notify pipeline complete callback so UI updates
      this.firePipelineComplete(closedResult);

      // Notify queue service for auto-start of next issue
      if (this.queueService) {
        this.handleQueueAutoStart(closedResult.success, issueNumber);
      }

      this.isRunning = false;
      this.abortController = null;
      return closedResult;
    }

    // ===================================================================
    // AUTH PRE-CHECK: Verify GitHub CLI auth before consuming AI tokens.
    // Catches expired tokens and missing scopes early.
    // @see Issue #1141
    // ===================================================================
    const authCheck = await this.preCheckAuth();

    // Transient GitHub-quota dip (#3896): auth + scopes are fine, the bucket is
    // just momentarily below headroom and resets within the hour. Do NOT treat
    // this like a permanent auth/scope failure that burns the issue — emit a
    // `github-quota-low` marker so the Go scheduler classifies it transient
    // (issue stays Ready, global cooldown until reset, no lifetime-cap
    // increment) and retries automatically once the quota recovers.
    if (authCheck.rateLimited) {
      const retryAfterSec = authCheck.retryAfterSec ?? 60;
      const quotaError = authCheck.errorMessage || "GitHub API quota too low to start pipeline";
      this.logger.warn("GitHub quota low at pipeline-start — deferring (transient)", {
        issueNumber,
        retryAfterSec,
      });

      const quotaResult: PipelineRunResult = {
        success: false,
        completedStages: [],
        skippedStages: [],
        deferredStages: [],
        failedStage: "pipeline-start",
        // Embed the `[github-quota-low]` token in the error text itself (not
        // just the stderr marker): bootstrap/services.ts forwards error.message
        // as failureDetail, and the Go ClassifyTerminalKind fallback matches on
        // that text — so the token must be present here for the transient
        // classification to fire in IPC/autonomous mode. (#3896)
        error: new Error(
          `[github-quota-low] GitHub API quota too low — pipeline deferred before AI stages ` +
            `(transient; resetInSec=${retryAfterSec}).\n\n${quotaError}`
        ),
        totalDurationMs: Date.now() - startTime,
      };

      this.eventDispatcher.onStderr(
        "pipeline-start",
        `[pipeline-start-failure] github-quota-low: ${quotaError} ` +
          `(transient; resetInSec=${retryAfterSec})\n`
      );

      this.firePipelineComplete(quotaResult);

      if (this.queueService) {
        this.handleQueueAutoStart(quotaResult.success, issueNumber);
      }

      this.isRunning = false;
      this.abortController = null;
      return quotaResult;
    }

    // Network outage at pipeline-start (#4002): gh couldn't reach
    // api.github.com at all. NOT an auth failure — emit the
    // `github-network-outage` marker so the Go scheduler classifies it
    // environmental (short global cooldown, issue stays Ready, no
    // lifetime-cap increment, no pause) and retries once connectivity
    // returns. Pre-fix this took the github-auth-failed path below, which
    // paged the operator with "run `gh auth login`" during a DNS blip.
    if (authCheck.networkOutage) {
      const retryAfterSec = authCheck.retryAfterSec ?? 120;
      const outageError = authCheck.errorMessage || "GitHub API unreachable (network outage)";
      this.logger.warn("GitHub unreachable at pipeline-start — deferring (transient outage)", {
        issueNumber,
        retryAfterSec,
      });

      const outageResult: PipelineRunResult = {
        success: false,
        completedStages: [],
        skippedStages: [],
        deferredStages: [],
        failedStage: "pipeline-start",
        // Embed the `[github-network-outage]` token in the error text itself
        // (not just the stderr marker): bootstrap/services.ts forwards
        // error.message as failureDetail, and the Go ClassifyTerminalKind
        // fallback matches on that text — same contract as the quota path
        // above (#3896).
        error: new Error(
          `[github-network-outage] GitHub API unreachable — pipeline deferred before AI stages ` +
            `(transient; retryInSec=${retryAfterSec}).\n\n${outageError}`
        ),
        totalDurationMs: Date.now() - startTime,
      };

      this.eventDispatcher.onStderr(
        "pipeline-start",
        `[pipeline-start-failure] github-network-outage: ${outageError} ` +
          `(transient; retryInSec=${retryAfterSec})\n`
      );

      this.firePipelineComplete(outageResult);

      if (this.queueService) {
        this.handleQueueAutoStart(outageResult.success, issueNumber);
      }

      this.isRunning = false;
      this.abortController = null;
      return outageResult;
    }

    if (!authCheck.isAuthenticated || !authCheck.hasRequiredScopes) {
      const authError = authCheck.errorMessage || "GitHub auth validation failed";
      this.logger.error("GitHub auth pre-check failed", {
        issueNumber,
        isAuthenticated: authCheck.isAuthenticated,
        hasRequiredScopes: authCheck.hasRequiredScopes,
        error: authError,
      });

      const authResult: PipelineRunResult = {
        success: false,
        completedStages: [],
        skippedStages: [],
        deferredStages: [],
        failedStage: "pipeline-start",
        error: new Error(
          `GitHub auth pre-check failed — pipeline halted before AI stages.\n\n${authError}`
        ),
        totalDurationMs: Date.now() - startTime,
      };

      this.eventDispatcher.onStderr(
        "pipeline-start",
        `[pipeline-start-failure] github-auth-failed: ${authError} ` +
          `(isAuthenticated=${authCheck.isAuthenticated}, hasRequiredScopes=${authCheck.hasRequiredScopes})\n`
      );

      this.firePipelineComplete(authResult);

      if (this.queueService) {
        this.handleQueueAutoStart(authResult.success, issueNumber);
      }

      this.isRunning = false;
      this.abortController = null;
      return authResult;
    }

    this.logger.info("GitHub auth pre-check passed", { issueNumber });

    // ===================================================================
    // ADAPTER AUTH PRE-FLIGHT (Issue #3222)
    // Probe every adapter the run will use *before* any stage executes so
    // missing credentials surface as a GitHub issue comment within seconds
    // — not after the worktree is cut and the issue moved to In Progress.
    //
    // Today we probe a single global adapter resolved from
    // `getExecutionAdapter()`. When epic #3212 (B1+B2) lands the caller
    // swaps in the per-stage deduped list with no signature change.
    // ===================================================================
    // Two escape hatches: the per-project `pipeline.skip_auth_preflight` config,
    // and the NIGHTGAUGE_SKIP_AUTH_PREFLIGHT=1 env var (a process-wide
    // override for CI/debug — and what the unit-test suite sets so its
    // pipeline-path tests don't depend on real CLI auth being present, #4044).
    if (
      getSkipAuthPreflight(this.getWorkingDirectory()) ||
      process.env.NIGHTGAUGE_SKIP_AUTH_PREFLIGHT === "1"
    ) {
      this.logger.info("Adapter auth pre-flight skipped (skip_auth_preflight)", {
        issueNumber,
      });
    } else {
      const adapters = [toIncrediAdapter(executionAdapter, process.env)];
      // Inject the real preflight runner so CLI adapters (claude-headless /
      // codex / copilot) actually probe auth (`codex login status`,
      // `claude auth status`, …) instead of short-circuiting to "passed". Without
      // a runner the gate is a no-op for exactly the adapters it must guard, and
      // a logged-out user only fails later at spawn time (#4044). Same runner the
      // #4031 Adapter Doctor uses. Set `pipeline.skip_auth_preflight: true` to
      // bypass (handled above).
      const preflight = await runAdapterAuthPreflight(adapters, {
        cwd: this.getWorkingDirectory(),
        runner: createDefaultPreflightRunner(),
      });
      if (!preflight.ok) {
        const bullets = preflight.failures
          .map(
            (f: { adapter: IncrediAdapter; reason: string; suggestedFix: string }) =>
              `- **${f.adapter}**: ${f.reason}\n  Fix: ${f.suggestedFix}`
          )
          .join("\n");
        // Distinguish a transient probe TIMEOUT (adapter CLI unresponsive under
        // a concurrent dispatch burst — auth was never broken) from a definitive
        // logged-out negative. Both carry the stable `[adapter-auth-failed]`
        // marker so the terminal-kind classifier (TS classifyTerminalKind + Go
        // ClassifyTerminalKind) routes this to `adapter_auth_failed` — retryable
        // infra — never `subagent_crash`. Issue #312.
        const allTimedOut = preflight.failures.every((f: { timedOut: boolean }) => f.timedOut);
        const kindNote = allTimedOut
          ? "auth probe timed out after retry (adapter CLI unresponsive — transient, not a logged-out session)"
          : "adapter not authenticated";
        const errorMessage =
          `[adapter-auth-failed] Auth pre-flight failed — ${kindNote}. ` +
          `Pipeline halted before AI stages (zero tokens spent).\n${bullets}`;
        this.logger.error("Adapter auth pre-flight failed", {
          issueNumber,
          timedOut: allTimedOut,
          failures: preflight.failures.map(
            (f: { adapter: IncrediAdapter; reason: string; timedOut: boolean }) => ({
              adapter: f.adapter,
              reason: f.reason,
              timedOut: f.timedOut,
            })
          ),
        });

        const preflightResult: PipelineRunResult = {
          success: false,
          completedStages: [],
          skippedStages: [],
          deferredStages: [],
          failedStage: "pipeline-start",
          error: new Error(errorMessage),
          totalDurationMs: Date.now() - startTime,
        };

        const adapterList = preflight.failures
          .map((f: { adapter: IncrediAdapter; reason: string }) => `${f.adapter}=${f.reason}`)
          .join("; ");
        this.eventDispatcher.onStderr(
          "pipeline-start",
          `[pipeline-start-failure] adapter-auth-failed: ${adapterList}\n${errorMessage}\n`
        );

        this.firePipelineComplete(preflightResult);

        if (this.queueService) {
          this.handleQueueAutoStart(preflightResult.success, issueNumber);
        }

        this.isRunning = false;
        this.abortController = null;
        return preflightResult;
      }
      this.logger.info("Adapter auth pre-flight passed", {
        issueNumber,
        adapters,
      });
    }

    // ===================================================================
    // PRE-FLIGHT BUDGET GATE (Issue #1935)
    // Estimate pipeline cost BEFORE consuming any tokens. If the projected
    // cost is near or above the ceiling, warn the user and let them decide
    // whether to proceed, increase the ceiling, or split the issue.
    // Non-blocking: never halts the pipeline without user consent.
    // ===================================================================
    if (pipelineCeiling.getEffectiveCeiling() > 0 && preCheck.labels.length > 0) {
      try {
        // Pin the estimator inputs once per run (#198): calibration table,
        // labels/title, and performance mode are all externally mutable —
        // re-reading them live made two estimates for the same issue differ
        // by 83% seconds apart (bowlsheet#233). Every estimate, warning
        // threshold, and post-run comparison in this run reuses this snapshot.
        this.estimatorSnapshot = await captureEstimatorInputs(
          { labels: preCheck.labels, title: preCheck.title },
          this.getWorkingDirectory()
        );
        const preFlightResult = await runPreFlightBudgetCheck(
          { labels: preCheck.labels, title: preCheck.title },
          pipelineCeiling.getEffectiveCeiling(),
          this.getWorkingDirectory(),
          undefined,
          undefined,
          this.estimatorSnapshot
        );

        this.logger.info("Pre-flight budget estimate", {
          issueNumber,
          estimatedCost: preFlightResult.estimatedCost,
          ceilingUsd: preFlightResult.ceilingUsd,
          ceilingRatio: preFlightResult.ceilingRatio,
          complexity: preFlightResult.complexity,
          historicalAvgCost: preFlightResult.historicalAvgCost,
          shouldWarn: preFlightResult.shouldWarn,
          estimatorInputsCapturedAt: this.estimatorSnapshot.capturedAt,
          estimatorMode: this.estimatorSnapshot.mode,
        });

        this.eventDispatcher.onStderr(
          "pipeline-start",
          `[PRE-FLIGHT] ${preFlightResult.summary}\n`
        );

        // Enrich pipeline state with budget metadata for Discord/UI
        const performanceMode = this.getActivePerformanceMode();
        const superchargeActive = performanceMode === "maximum";
        let superchargeModel: string | undefined;
        if (superchargeActive) {
          try {
            const { getSuperchargeModel } =
              require("../utils/incrediConfig") as typeof import("../utils/incrediConfig");
            superchargeModel = getSuperchargeModel(this.getPersistentRoot());
          } catch {
            superchargeModel = undefined;
          }
        }
        this.stateService?.setMeta({
          complexity: preFlightResult.complexity,
          budget_estimate_usd: preFlightResult.estimatedCost,
          budget_ceiling_usd: preFlightResult.ceilingUsd,
          // Audit trail for the estimate (#198): "estimated $X under
          // calibration-as-of-T in mode M" — makes post-run cost-vs-estimate
          // deltas meaningful.
          budget_estimate_captured_at: this.estimatorSnapshot?.capturedAt,
          budget_estimate_mode: this.estimatorSnapshot?.mode,
          performance_mode: performanceMode,
          // Additively kept for one release — Discord/Dashboard read fallback (Issue #3009).
          is_supercharge: superchargeActive || undefined,
          supercharge_model: superchargeModel,
        });

        if (preFlightResult.shouldWarn) {
          const preFlightChoice = await vscode.window.showWarningMessage(
            `Issue #${issueNumber} (${preFlightResult.complexity}) is projected ` +
              `to use ${(preFlightResult.ceilingRatio * 100).toFixed(0)}% of the ` +
              `$${preFlightResult.ceilingUsd.toFixed(0)} budget ceiling.` +
              (preFlightResult.historicalAvgCost !== null
                ? ` Similar issues averaged $${preFlightResult.historicalAvgCost.toFixed(2)}.`
                : ""),
            { modal: false },
            "Proceed Anyway",
            "Cancel Pipeline"
          );

          if (preFlightChoice === "Cancel Pipeline") {
            this.logger.info("User cancelled pipeline after pre-flight budget warning", {
              issueNumber,
            });

            const cancelResult: PipelineRunResult = {
              success: false,
              completedStages: [],
              skippedStages: [],
              deferredStages: [],
              failedStage: "pipeline-start",
              error: new Error(
                `Pipeline cancelled by user after pre-flight budget warning. ` +
                  `Estimated cost: $${preFlightResult.estimatedCost.toFixed(2)}, ` +
                  `Ceiling: $${preFlightResult.ceilingUsd.toFixed(2)}. ` +
                  `Zero AI tokens consumed.`
              ),
              totalDurationMs: Date.now() - startTime,
            };

            this.eventDispatcher.onStderr(
              "pipeline-start",
              `[pipeline-start-failure] budget-cancelled-by-user: ` +
                `estimated=$${preFlightResult.estimatedCost.toFixed(2)}, ` +
                `ceiling=$${preFlightResult.ceilingUsd.toFixed(2)}, ` +
                `ratio=${(preFlightResult.ceilingRatio * 100).toFixed(0)}%\n`
            );

            this.firePipelineComplete(cancelResult);

            if (this.queueService) {
              this.handleQueueAutoStart(cancelResult.success, issueNumber);
            }

            this.isRunning = false;
            this.abortController = null;
            return cancelResult;
          }

          // User chose "Proceed Anyway" — log and continue
          this.logger.info("User acknowledged pre-flight budget warning, proceeding", {
            issueNumber,
          });
        }
      } catch (err) {
        // Pre-flight is non-critical — never blocks pipeline
        this.logger.warn("Pre-flight budget check failed (non-fatal)", {
          issueNumber,
          error: err instanceof Error ? err.message : String(err),
        });
      }
    }

    // ===================================================================
    // HEALTH-GATED POLICIES (Issue #1395)
    // Evaluate pipeline health and apply temporary per-run policy overrides.
    // Non-critical: never blocks pipeline start.
    // ===================================================================
    try {
      const { policies } = await HealthActionService.evaluateWithPolicies(
        this.getWorkingDirectory(),
        this.logger
      );
      if (policies.tier !== "none") {
        this.activePolicies = policies;
        this.policyRetryBudgetIncrease = policies.retryBudgetIncrease;

        if (policies.escalateAllStages) {
          for (const s of STAGE_ORDER) {
            if (!isBookendStage(s) && s !== "issue-pickup") {
              const current = (this.stageModelOverrides.get(s) ??
                resolveModel(s, this.getWorkingDirectory())
                  .model) as import("../utils/incrediConfig").DefaultModel;
              const escalated = getEscalatedModel(current);
              if (escalated) {
                this.stageModelOverrides.set(s, escalated);
              }
            }
          }
        }

        this.pauseAutoRouting = policies.pauseAutoRouting;

        this.eventDispatcher.onHealthPoliciesApplied(policies);

        this.logger.info("Health policies activated", {
          issueNumber,
          tier: policies.tier,
          score: policies.score,
          reasons: policies.reasons,
        });

        // Record in pipeline state for dashboard
        if (this.stateService) {
          this.stateService
            .batchUpdate((state) => ({
              ...state,
              active_health_policies: {
                tier: policies.tier,
                retry_budget_increase: policies.retryBudgetIncrease,
                escalate_all_stages: policies.escalateAllStages,
                pause_auto_routing: policies.pauseAutoRouting,
                reasons: policies.reasons,
                score: policies.score,
                applied_at: policies.timestamp,
              },
            }))
            .catch((err: unknown) => {
              this.logger.warn("Failed to record health policies in state", {
                err,
              });
            });
        }
      }
    } catch (err) {
      this.logger.warn("Health policy evaluation failed — continuing without policies", {
        err,
      });
    }

    // ===================================================================
    // PIPELINE STATE INITIALIZATION (Issue #1629)
    // Concurrent slots create per-slot PipelineStateService instances that
    // start with no state.json. Initialize pipeline state here so that
    // pipeline-start's startStage() has a valid state to modify, and
    // issue-pickup's validateStageTransition() finds an initialized pipeline.
    // The non-concurrent queue path does this in startNextQueuedIssue().
    // ===================================================================
    if (this.stateService) {
      const existingState = await this.stateService.getState();
      if (!existingState) {
        await this.stateService.initializePipeline(
          issueNumber,
          `Issue #${issueNumber}`,
          `feat/${issueNumber}` // Placeholder - issue-pickup updates with real branch
        );
        await this.stateService.setExecutionMode("automatic");
      }
    }

    // ===================================================================
    // EXISTING PR DETECTION: If a previous pipeline run created a PR for
    // this issue but failed at pr-merge, detect the open PR and generate
    // synthetic context files so resume skips directly to pr-merge.
    // This prevents expensive full re-runs (~$7+) when only the merge
    // step needs retrying.
    // @see Issue #500 - pr-merge failure causes full pipeline re-run
    // ===================================================================
    await this.detectAndRestoreExistingPr(issueNumber);

    try {
      for (let stageIndex = 0; stageIndex < STAGE_ORDER.length; stageIndex++) {
        const stage = STAGE_ORDER[stageIndex];
        // Check if aborted
        if (this.abortController?.signal.aborted) {
          this.logger.info("Pipeline aborted by user");
          break;
        }

        // ===================================================================
        // DEFERRED STAGES: When deferMerge is enabled, pr-merge and
        // pipeline-finish require human action (PR review/approval).
        // Defer them instead of attempting to run and falsely reporting failure.
        // When deferMerge is false (default), the full pipeline runs end-to-end.
        // @see Issue #628 - Queue falsely pauses when pr-merge awaits review
        // ===================================================================
        if (this.config.deferMerge && DEFERRABLE_STAGES.includes(stage)) {
          this.logger.info("Deferring stage (deferMerge enabled, requires human action)", {
            stage,
            issueNumber,
          });
          // Persist deferred status to state.json so the dashboard and
          // other consumers know this stage won't run without human action.
          if (this.stateService) {
            await this.stateService.deferStage(stage);
          }
          this.eventDispatcher.onStageSkipped(
            stage,
            `Deferred: ${stage} requires human action (PR review/merge)`
          );
          deferredStages.push(stage);
          continue;
        }

        // ===================================================================
        // RESUME SUPPORT: Skip already-completed or skipped stages.
        // When resuming from paused state, runPipeline() is called again
        // and should continue from where it left off.
        // @see Issue #535 - Fix pipeline pause/resume
        // ===================================================================
        if (this.stateService) {
          const currentState = await this.stateService.getState();
          if (currentState) {
            const stageState = currentState.stages[stage];
            if (
              stageState?.status === "complete" ||
              stageState?.status === "skipped" ||
              stageState?.status === "deferred"
            ) {
              // Verify context file exists for completed stages that produce
              // output context. If the file was deleted, re-run the stage
              // instead of skipping — otherwise the next stage will fail its
              // precondition check with a confusing error.
              let contextFileMissing = false;
              if (stageState.status === "complete") {
                const contextType = STAGE_OUTPUT_CONTEXT_TYPE[stage];
                const wsRoot = this.getWorkingDirectory();
                if (contextType && wsRoot) {
                  const contextPath = `${wsRoot}/.nightgauge/pipeline/${contextType}-${issueNumber}.json`;
                  if (!fs.existsSync(contextPath)) {
                    this.logger.warn(
                      `Resume: stage ${stage} marked complete but context file missing (${contextType}-${issueNumber}.json). Re-running stage.`
                    );
                    contextFileMissing = true;
                  }
                }
              }

              if (!contextFileMissing) {
                this.logger.debug("Skipping already-completed stage", {
                  stage,
                  status: stageState.status,
                });
                // Add to appropriate tracking array
                if (stageState.status === "complete") {
                  completedStages.push(stage);
                } else if (stageState.status === "deferred") {
                  deferredStages.push(stage);
                } else {
                  skippedStages.push(stage);
                }
                continue;
              }
              // contextFileMissing: fall through to re-run the stage
            }
          }
        }

        // Handle bookend stages synchronously (zero AI tokens)
        if (isBookendStage(stage)) {
          const bookendResult = await this.runBookendStage(stage, issueNumber, callbacks);

          if (!bookendResult.success) {
            failedStage = stage;
            error = bookendResult.error;
            break;
          }

          completedStages.push(stage);
          continue;
        }

        // Skip validation if configured (legacy config option)
        if (stage === "feature-validate" && this.config.skipValidation) {
          const legacySkipReason = "skipValidation config enabled";
          this.logger.info(`Skipping feature-validate: ${legacySkipReason}`);
          if (this.stateService) {
            try {
              await this.stateService.skipStage(stage, legacySkipReason);
            } catch (err) {
              this.logger.warn("Failed to update state for skipped stage", {
                stage,
                err,
              });
            }
          }
          skippedStages.push(stage);
          continue;
        }

        // Check routing-based stage skipping (after issue-pickup completes)
        if (this.shouldSkipStage(stage)) {
          // Issue #1593: Include complexity context in skip reason for visibility
          const route = this.currentRoutingDecision?.route;
          const isTrivial = route === "trivial";
          const skipReason = isTrivial
            ? `Skipped ${stage}: trivial complexity (${route} route)`
            : `Skipped via ${route} route: ${this.currentRoutingDecision?.explanation || "routing decision"}`;

          this.logger.info("Skipping stage based on routing decision", {
            stage,
            route,
            isTrivialComplexity: isTrivial,
            rationale: this.currentRoutingDecision?.explanation,
          });

          // Notify via callback
          this.eventDispatcher.onStageSkipped(stage, skipReason);

          // Update state to mark stage as skipped with reason (Issue #843)
          if (this.stateService) {
            try {
              await this.stateService.skipStage(stage, skipReason);
            } catch (err) {
              this.logger.warn("Failed to update state for skipped stage", {
                stage,
                err,
              });
            }
          }
          skippedStages.push(stage);
          continue;
        }

        // ===================================================================
        // Pre-condition validation: Verify that prerequisite context files
        // exist and are valid before launching the subagent. Saves AI tokens
        // by catching missing/invalid input at the orchestrator level.
        // Generalizes the previous pr-merge-only check to all stages.
        // @see Issue #1181 - Stage pre-condition validation
        // @see Issue #637 - Original pr-merge defense-in-depth
        // ===================================================================
        const preconditionError = this.validateStagePreconditions(
          stage,
          issueNumber,
          skippedStages
        );
        if (preconditionError) {
          failedStage = stage;
          error = preconditionError;
          break;
        }

        // ===================================================================
        // ARCHITECTURE-APPROVAL PRE-CHECK (Issue #4222)
        // A high-impact architectural decision stays human-owned: run the
        // deterministic approval gate BEFORE launching feature-dev so a block
        // halts cleanly with an actionable "awaiting approval" alert and ZERO
        // dev/validate spend — instead of the skill's inline block being
        // swallowed by the deterministic-context fallback and bleeding into
        // feature-validate as a confusing "missing-implementation" (#4220).
        // The alert is surfaced by the approval-aware failure comment
        // (failureComment.ts keys off the message marker). Fail-open.
        // ===================================================================
        if (stage === "feature-dev") {
          const approvalError = await this.verifyArchitectureApproval(issueNumber);
          if (approvalError) {
            this.logger.warn(
              "Architecture approval required — halting before feature-dev (no dev/validate spend)",
              { issueNumber, reason: approvalError.message }
            );
            if (this.stateService) {
              try {
                await this.stateService.failStage(stage, approvalError.message);
              } catch {
                // Non-critical — the break below still halts the pipeline.
              }
            }
            failedStage = stage;
            error = approvalError;
            break;
          }
        }

        // ===================================================================
        // PRE-STAGE HEALTH CHECK — PROACTIVE MODEL ESCALATION (Issue #1394)
        // Evaluate health trends before each stage. If health is declining
        // and this stage has a historically high failure rate, proactively
        // escalate the model one tier to avoid a wasted first attempt.
        // Max 1 proactive escalation per pipeline run.
        // ===================================================================
        this.evaluatePreStageHealth(stage, issueNumber, callbacks);

        // ===================================================================
        // DETERMINISTIC-FIRST EXECUTION FOR ISSUE-PICKUP (Issue #2614)
        //
        // Try to generate issue context deterministically BEFORE spawning
        // the LLM subagent. For most runs this saves 2+ minutes of tokens
        // and ~$0.15-0.50. Only fall through to the LLM if deterministic
        // generation fails (e.g. gh CLI unavailable, network error).
        //
        // The LLM subagent (Haiku, per LIGHTWEIGHT_STAGE_DEFAULTS) runs only
        // as a safety net for edge cases where deterministic generation is
        // insufficient (e.g. issue body has no structured sections and the
        // fallback produces minimal context).
        // @see Issue #2614 - Deterministic-first execution
        // ===================================================================
        if (stage === "issue-pickup") {
          const deterministicStartTime = Date.now();
          this.logger.info(
            "issue-pickup: attempting deterministic context generation (deterministic-first)",
            { issueNumber }
          );

          // Notify callbacks that the stage is starting
          this.eventDispatcher.onStageStart(stage);
          this.currentStage = stage;

          // Update state service to mark stage as running
          if (this.stateService) {
            try {
              await this.stateService.startStage(stage, { forceBackward: true });
            } catch (err) {
              this.logger.warn("Failed to update state for deterministic issue-pickup start", {
                stage,
                err,
              });
            }
          }

          // Emit stage.started audit event
          this.auditClient?.enqueue({
            action: "stage.started",
            resourceType: "stage",
            resourceId: `${issueNumber}:${stage}`,
            metadata: {
              pipelineRunId: this.currentPipelineRunId,
              stage,
              issueNumber,
              timestamp: new Date().toISOString(),
            },
          });

          const deterministicGenerated = await this.contextAssembler.generateDeterministicContext(
            "issue-pickup",
            issueNumber
          );

          // Fail closed on open blockedBy dependencies (#189): the primary
          // deterministic path now consults GitHub's native edges. An issue
          // whose blockers are still open must DEFER — not fall through to
          // the LLM (which would re-derive the same answer at token cost),
          // and never proceed to planning on prose alone.
          if (deterministicGenerated.blockedBy?.length) {
            const blockerList = deterministicGenerated.blockedBy
              .map(
                (d) => `${d.repo ? `${d.repo}#` : "#"}${d.number}${d.title ? ` (${d.title})` : ""}`
              )
              .join(", ");
            // A dispatched issue whose native blockedBy edges are still open
            // must DEFER — not fail (#189/#305). Nothing crashed and no tokens
            // were spent; booking this as a failure (subagent_crash) both pauses
            // autonomous and pollutes failure-rate telemetry. Terminate with a
            // distinct non-failure outcome instead: no failedStage, no error,
            // `deferred: true`, outcome_type="deferred". The issue stays
            // eligible — the Go blocker-close requeue re-dispatches it once the
            // blocker closes. The `[blocked-dependency]` marker lets the Go
            // scheduler classify the completion as a non-failure deferral.
            this.logger.info("issue-pickup deferred — open blockedBy dependencies (#189/#305)", {
              issueNumber,
              blockedBy: deterministicGenerated.blockedBy.map((d) => d.number),
            });
            if (this.stateService) {
              try {
                await this.stateService.deferStage(stage);
                await this.stateService.setOutcomeType("deferred");
              } catch {
                // Non-critical — the early return below still halts the pipeline.
              }
            }
            this.eventDispatcher.onStageSkipped(
              stage,
              `Deferred: issue #${issueNumber} is blocked by open dependencies (${blockerList})`
            );
            // Info-level marker, NOT a `[pipeline-*-failure]` line: the slot
            // manager and Go scheduler key off `[blocked-dependency]` to route
            // this to the deferral path, never the failure path.
            this.eventDispatcher.onStderr(
              stage,
              `[blocked-dependency] issue #${issueNumber} deferred — ` +
                `blocked by open dependencies: ${blockerList}\n`
            );

            const deferredResult: PipelineRunResult = {
              success: false,
              deferred: true,
              completedStages,
              skippedStages,
              deferredStages: [stage],
              // No failedStage / error — a deferral is not a failure.
              totalDurationMs: Date.now() - startTime,
              outcomeType: "deferred",
            };

            this.firePipelineComplete(deferredResult);
            if (this.queueService) {
              this.handleQueueAutoStart(deferredResult.success, issueNumber);
            }
            this.isRunning = false;
            this.abortController = null;
            return deferredResult;
          }

          if (deterministicGenerated.generated) {
            const contextError = await this.validateStageContextOutput("issue-pickup", issueNumber);
            if (!contextError) {
              this.logger.info(
                "issue-pickup: deterministic context written and validated — skipping LLM subagent",
                { issueNumber }
              );

              // #309: record the deterministic path so the authoritative history
              // stage record carries execution_path="deterministic" for pickup,
              // mirroring the pr-create/pr-merge deterministic-first hooks.
              this.stageExecutionPaths.set("issue-pickup", { path: "deterministic" });

              // Emit stage.completed audit event (cost=$0, source=deterministic)
              const deterministicDurationMs = Date.now() - deterministicStartTime;
              this.auditClient?.enqueue({
                action: "stage.completed",
                resourceType: "stage",
                resourceId: `${issueNumber}:${stage}`,
                metadata: {
                  pipelineRunId: this.currentPipelineRunId,
                  stage,
                  issueNumber,
                  model: "none",
                  outcome: "success",
                  executionSource: "deterministic",
                  durationMs: deterministicDurationMs,
                  timestamp: new Date().toISOString(),
                },
              });
              void this.auditClient?.flush();

              // Mark stage as complete in state service
              if (this.stateService) {
                try {
                  await this.stateService.completeStage(stage);
                } catch (err) {
                  this.logger.warn("Failed to update state for deterministic issue-pickup", {
                    stage,
                    err,
                  });
                }
              }

              this.eventDispatcher.onStageComplete(stage, {
                success: true,
                stage,
                durationMs: deterministicDurationMs,
              });
              completedStages.push(stage);
              await this.syncStageStatusTransition(stage, issueNumber);
              continue; // Skip runStage() — deterministic path succeeded
            }
          }

          // Deterministic generation failed — fall through to LLM subagent.
          // #309: record the punt so history shows execution_path="llm" +
          // punt_reason for pickup, not silence.
          this.stageExecutionPaths.set("issue-pickup", {
            path: "llm",
            puntReason: "deterministic-context-failed",
          });
          this.logger.warn(
            "issue-pickup: deterministic context generation failed — falling back to LLM",
            { issueNumber }
          );
        }

        // ===================================================================
        // PRE-MERGE BRANCH-BEHIND GUARD (Issue #3782)
        //
        // Before spawning the pr-merge LLM skill, check if the PR branch is
        // behind origin/main. If it is, auto-rebase (mirrors Go BranchOutOfDate
        // recovery). On rebase conflict, surface the conflicting files immediately
        // rather than wasting a full pr-merge skill invocation.
        // ===================================================================
        if (stage === "pr-merge") {
          const prContextPath = this.getContextPath("pr", issueNumber);
          let prNumber: number | undefined;
          try {
            if (fs.existsSync(prContextPath)) {
              const raw = fs.readFileSync(prContextPath, "utf-8");
              const parsed = JSON.parse(raw) as { pr_number?: unknown };
              if (typeof parsed.pr_number === "number") prNumber = parsed.pr_number;
            }
          } catch {
            // Fall through — no prNumber, guard is best-effort
          }

          if (prNumber !== undefined) {
            const cwd = this.pinnedWorkspaceRoot ?? this.getWorkingDirectory();
            const guardResult = await this.checkAndRebaseBehindBranch(prNumber, issueNumber, cwd);

            if (guardResult.status === "rebased") {
              this.eventDispatcher.onStderr(
                "pr-merge",
                `[pre-merge guard] Branch was behind origin/main — rebased and pushed. PR #${prNumber} is now up-to-date.\n`
              );
            } else if (guardResult.status === "conflict") {
              const fileList =
                guardResult.files.length > 0
                  ? guardResult.files.join(", ")
                  : "(could not determine conflicting files)";
              failedStage = stage;
              error = new Error(
                `[pre-merge-conflict] PR #${prNumber} branch has a true merge conflict against origin/main. ` +
                  `Rebase aborted. Conflicting files: ${fileList}. ` +
                  `Resolve conflicts manually, push, and resume the pipeline.`
              );
              break;
            }
            // status === "clean" or "error" → fall through to normal skill execution
          }
        }

        // Run the skill-based stage.
        // Pass model override from escalation engine if one is set (#1343, #1394),
        // or from user override (#1610). User override source is 'user-override'.
        // Pass pinned workspace root to prevent repo-switch mid-pipeline (#1592).
        const stageModelOverride =
          executionAdapter === "codex"
            ? (this.userModelOverride ?? undefined)
            : this.stageModelOverrides.get(stage);

        // Emit stage.started audit event (Issue #1582)
        const stageStartTime = Date.now();
        this.auditClient?.enqueue({
          action: "stage.started",
          resourceType: "stage",
          resourceId: `${issueNumber}:${stage}`,
          metadata: {
            pipelineRunId: this.currentPipelineRunId,
            stage,
            issueNumber,
            timestamp: new Date().toISOString(),
          },
        });

        // ===================================================================
        // DETERMINISTIC-FIRST EXECUTION FOR PR-CREATE / PR-MERGE (Issue #300)
        //
        // The VSCode dogfood path runs THIS orchestrator, not the Go scheduler,
        // so the scheduler's deterministic-first pr-stage hooks never fired here
        // and every run paid $5–8 for LLM pr-create + pr-merge sessions. Invoke
        // the SAME Go runners first (`nightgauge pr-stage`): on created/merged we
        // synthesize a success result and skip the LLM (but still run the normal
        // post-success gates below); on a punt we record execution_path="llm" +
        // punt_reason and fall through to executeSkill exactly as today; a
        // rate-limit DEFERS (no LLM fallback, #3976).
        // ===================================================================
        const detOutcome = await this.runDeterministicPrStage(stage, issueNumber, stageStartTime);
        if (detOutcome.kind === "deferred") {
          failedStage = stage;
          error = detOutcome.error;
          break;
        }

        const result =
          detOutcome.kind === "handled"
            ? detOutcome.result
            : await this.runStage(
                stage,
                issueNumber,
                callbacks,
                undefined,
                stageModelOverride,
                this.pinnedWorkspaceRoot,
                this.userModelOverride && stageModelOverride === this.userModelOverride
                  ? "user-override"
                  : undefined
              );

        if (!result.success) {
          // #3666 follow-up: shipped-partially is a recoverable budget kill,
          // NOT a hard failure. The stage's work product (e.g. a created PR)
          // shipped before the cost cap fired; the right move is to advance
          // to the next stage rather than bail out of the loop. The prior
          // #3666 work plumbed this signal through IPC and onto result, but
          // this loop's `if (!result.success)` branch never consulted it,
          // turning every shipped-partially run into a hard pipeline failure.
          // That left LifetimeIssueFailures incremented and autonomous paused
          // on runs that had actually opened working PRs (dashboard#443's
          // PR #449 is the canonical case).
          if (result.shippedPartially) {
            this.logger.warn(
              "Stage budget-killed but shipped — advancing to next stage (no pipeline failure)",
              {
                stage,
                issueNumber,
                durationMs: Date.now() - stageStartTime,
              }
            );
            // Record the recoverable exit diagnostic and the success-shaped
            // audit so the run shows up in retros + dashboards as recovered,
            // not failed.
            void this.recordStageExitDiagnostic(stage, issueNumber, result, stageStartTime);
            this.auditClient?.enqueue({
              action: "stage.completed",
              resourceType: "stage",
              resourceId: `${issueNumber}:${stage}`,
              metadata: {
                pipelineRunId: this.currentPipelineRunId,
                stage,
                issueNumber,
                executionSource: "llm",
                durationMs: Date.now() - stageStartTime,
                outcome: "shipped_partially",
                timestamp: new Date().toISOString(),
              },
            });
            void this.auditClient?.flush();
            completedStages.push(stage);
            await this.syncStageStatusTransition(stage, issueNumber);
            // Skip the rest of this iteration's post-success bookkeeping —
            // we did not really succeed, just gracefully degraded. Move on
            // to the next stage in the loop.
            continue;
          }

          // Graceful Fable → Opus fallback on a usage/quota limit. Fable has a
          // separate Max-plan usage bucket; a Fable-only exhaustion should retry
          // on Opus (still-available bucket) rather than pause the whole pipeline
          // for the global cooldown. Downgrade this stage and retry once; if Opus
          // ALSO limits (genuine account-wide exhaustion), fall through to the
          // normal failure path below, which routes to the quota cooldown.
          if (this.shouldFallbackFableToOpus(stage, result.error)) {
            this.fableQuotaFallbackApplied.add(stage);
            this.stageModelOverrides.set(stage, "opus");
            // Surface the downgrade to the notifiers (Discord/Mattermost) in
            // real time. pipeline_meta is a TS-only overlay preserved across Go
            // state syncs, so this survives to the final embed (Issue #26).
            this.fableFallbacks.push({ stage, from: "fable", to: "opus" });
            this.stateService?.setMeta({ quota_fallbacks: [...this.fableFallbacks] });
            this.logger.warn(
              "Fable usage-limit hit — downgrading stage to Opus and retrying once",
              { stage, issueNumber }
            );
            if (this.stateService) {
              try {
                await this.stateService.batchUpdate((state) => {
                  const stageState = state.stages[stage];
                  if (stageState) {
                    stageState.is_retrying = true;
                    stageState.auto_retry_count = (stageState.auto_retry_count ?? 0) + 1;
                  }
                  return state;
                });
              } catch {
                // Non-critical
              }
            }
            // Clear duplicate-prevention guard so the retry can resolve.
            this.completedStageSet.delete(stage);
            const opusRetry = await this.runStage(
              stage,
              issueNumber,
              callbacks,
              undefined,
              "opus",
              this.pinnedWorkspaceRoot
            );
            void this.recordStageExitDiagnostic(stage, issueNumber, opusRetry, stageStartTime);
            if (opusRetry.success) {
              if (this.stateService) {
                try {
                  await this.stateService.clearRetrying(stage);
                } catch {
                  // Non-critical
                }
              }
              this.eventDispatcher.onStageComplete(stage, opusRetry);
              completedStages.push(stage);
              await this.syncStageStatusTransition(stage, issueNumber);
              continue;
            }
            // Opus retry also failed — surface the Opus attempt's error and fall
            // through to the normal failure/cooldown path.
            failedStage = stage;
            error = opusRetry.error ?? result.error;
            if (opusRetry.budgetExceeded) {
              budgetExceeded = true;
            }
            break;
          }

          // Emit stage.failed audit event (Issue #1582)
          this.auditClient?.enqueue({
            action: "stage.failed",
            resourceType: "stage",
            resourceId: `${issueNumber}:${stage}`,
            metadata: {
              pipelineRunId: this.currentPipelineRunId,
              stage,
              issueNumber,
              error: result.error?.message ?? "Unknown error",
              durationMs: Date.now() - stageStartTime,
              timestamp: new Date().toISOString(),
            },
          });
          // #3619: write a stage-exit diagnostic record so failures here are
          // visible to `nightgauge exit-records tail`. Prior to this, the
          // TS-side pipeline path produced zero exit records and every retro
          // turned into log archaeology. Fire-and-forget — never blocks.
          void this.recordStageExitDiagnostic(stage, issueNumber, result, stageStartTime);
          failedStage = stage;
          error = result.error;
          if (result.budgetExceeded) {
            budgetExceeded = true;
          }
          break;
        }

        // #3619: record the healthy-exit diagnostic too. Successful runs
        // anchor what "normal" looks like for ratio-based health analysis;
        // omitting them would bias the corpus toward failures.
        void this.recordStageExitDiagnostic(stage, issueNumber, result, stageStartTime);

        // Emit stage.completed and skill.invoked audit events (Issue #1582)
        const stageDurationMs = Date.now() - stageStartTime;
        this.auditClient?.enqueue({
          action: "stage.completed",
          resourceType: "stage",
          resourceId: `${issueNumber}:${stage}`,
          metadata: {
            pipelineRunId: this.currentPipelineRunId,
            stage,
            issueNumber,
            executionSource: "llm",
            durationMs: stageDurationMs,
            timestamp: new Date().toISOString(),
          },
        });
        this.auditClient?.enqueue({
          action: "skill.invoked",
          resourceType: "skill",
          resourceId: stage,
          metadata: {
            pipelineRunId: this.currentPipelineRunId,
            stage,
            issueNumber,
            model: this.stageModelOverrides.get(stage) ?? "sonnet",
            outcome: "success",
            durationMs: stageDurationMs,
            timestamp: new Date().toISOString(),
          },
        });
        // Flush after each stage completes (no EventBus to trigger auto-flush)
        void this.auditClient?.flush();

        // Emit commit.created audit event when feature-validate writes commit_sha (Issue #1582)
        if (stage === "feature-validate" && this.auditClient) {
          try {
            const validateCtxPath = path.join(
              this.getWorkingDirectory(),
              ".nightgauge",
              "pipeline",
              `validate-${issueNumber}.json`
            );
            if (fs.existsSync(validateCtxPath)) {
              const raw = fs.readFileSync(validateCtxPath, "utf-8");
              const parsed = JSON.parse(raw) as { commit_sha?: string };
              if (parsed.commit_sha) {
                this.auditClient.enqueue({
                  action: "commit.created",
                  resourceType: "commit",
                  resourceId: parsed.commit_sha,
                  metadata: {
                    pipelineRunId: this.currentPipelineRunId,
                    issueNumber,
                    stage: "feature-validate",
                    timestamp: new Date().toISOString(),
                  },
                });
              }
            }
          } catch {
            // Non-critical — skip commit.created if context file unreadable
          }
        }

        // ===================================================================
        // POST-VALIDATE VERIFICATION: The feature-validate skill exits 0 even
        // when validation FAILS — on a hard-gate failure it writes
        // validation_status:"failed" (+ an errorCategory) and deliberately
        // leaves the code uncommitted "on disk for retry" rather than exiting
        // non-zero (build-and-tests.md: "does not exit 1 — control must reach
        // the context write"). The orchestrator, not the skill, owns the halt.
        // Without this gate the legacy TS path advanced a failed validation
        // straight into pr-create, which then found no commit to push and
        // aborted at the no-commits gate — after burning the full pr-create
        // spend and (once the worktree was pruned) destroying the retry code.
        // The Go scheduler runs FeatureValidateGate inline; this is its mirror.
        // @see Issue #4220 - failed feature-validate advanced to pr-create
        // ===================================================================
        if (stage === "feature-validate") {
          const validateVerdictError = this.verifyPostValidateState(issueNumber);
          if (validateVerdictError) {
            this.logger.error(
              "Post-validate verification failed — halting instead of advancing to pr-create",
              { issueNumber, error: validateVerdictError.message }
            );
            if (this.stateService) {
              try {
                await this.stateService.failStage(stage, validateVerdictError.message);
              } catch {
                // Non-critical — the break below still fails the pipeline.
              }
            }
            failedStage = stage;
            error = validateVerdictError;
            break;
          }

          // Verdict passed — now enforce the #1608 commit contract
          // deterministically: validated code must be committed ahead of base.
          // Remediation commits the approved tree in-place; only an
          // unrecoverable state (work gone, or backstop commit failed)
          // returns an error. See enforceValidateCommitContract docs
          // (production autonomous-run post-mortem).
          const commitContractError = await this.enforceValidateCommitContract(issueNumber);
          if (commitContractError) {
            this.logger.error(
              "Validate commit contract unmet — halting instead of advancing to pr-create",
              { issueNumber, error: commitContractError.message }
            );
            if (this.stateService) {
              try {
                await this.stateService.failStage(stage, commitContractError.message);
              } catch {
                // Non-critical — the break below still fails the pipeline.
              }
            }
            failedStage = stage;
            error = commitContractError;
            break;
          }
        }

        // ===================================================================
        // POST-PR-CREATE VERIFICATION: Deterministic safety net after the
        // pr-create skill reports success. Confirms a PR actually exists on
        // GitHub. The skill can exit 0 having written a context/assessment file
        // but never opened a PR (push blocked, prompt dismissed) — this gate
        // catches that false success and converts it into a real stage failure
        // so the retry/backtrack engine re-runs pr-create (it is idempotent).
        // The Go scheduler runs PrCreateGate inline; this is the TS-path mirror.
        // @see Issue #3869 - TS path ran no pr-create post-condition gate
        // @see Issue #3867 - AcmeApp #42 false pr-create success
        // ===================================================================
        if (stage === "pr-create") {
          const createVerifyError = await this.verifyPostCreateState(issueNumber);
          if (createVerifyError) {
            this.logger.error(
              "Post-create verification failed — failing stage instead of recording false success",
              { issueNumber, error: createVerifyError.message }
            );
            if (this.stateService) {
              try {
                await this.stateService.failStage(stage, createVerifyError.message);
              } catch {
                // Non-critical — the break below still fails the pipeline.
              }
            }
            failedStage = stage;
            error = createVerifyError;
            break;
          }
        }

        // ===================================================================
        // POST-PR-MERGE VERIFICATION: Deterministic safety net after the
        // pr-merge skill reports success. Verifies the PR was actually merged
        // and CI checks passed. The skill is AI-interpreted and can choose to
        // merge despite failing CI — this gate catches that.
        //
        // AUTO-RETRY: If verification fails because the PR is still OPEN
        // (skill exited without merging), retry the pr-merge stage once before
        // failing the pipeline. This prevents expensive full re-runs when the
        // AI skill simply failed to execute the merge command.
        // @see Issue #1819 - PRs merged with failing CI checks
        // @see Issue #500 - pr-merge verification auto-retry
        // ===================================================================
        if (stage === "pr-merge") {
          const mergeVerifyError = await this.verifyPostMergeState(issueNumber, 1, 2);
          if (!mergeVerifyError) {
            // #266: the deterministic gate confirms the PR is MERGED. Pin the
            // ground-truth flag so pipeline.notifyComplete can tell the Go
            // recording boundary the merge landed even if a later stage trips.
            this.prMergedGroundTruth = true;
          }
          if (mergeVerifyError) {
            // Check if PR is still OPEN (not CLOSED) — worth retrying once
            const isOpenPrFailure = mergeVerifyError.message.includes("state: OPEN");
            if (isOpenPrFailure) {
              // #185: never re-run pr-merge into a repo-config dead end. A
              // merge blocked by branch rules / a required-check config
              // mismatch is deterministically unwinnable — the retry would
              // rebuild the same prompt, re-run the same checks, and hit the
              // same rejection (bowlsheet#233: ~16 min and 2× tokens burned).
              const blockerClass = await this.classifyMergeBlockerRetryability(
                issueNumber,
                this.pinnedWorkspaceRoot ?? this.getWorkingDirectory()
              );
              if (!blockerClass.retryable) {
                const blockedError = new Error(
                  `pr-merge BLOCKED by repository configuration (non-retryable): ` +
                    `${blockerClass.reason}. ${mergeVerifyError.message}`
                );
                this.logger.error(
                  "Post-merge verification: repo-config blocker — skipping retry, escalating",
                  { issueNumber, reason: blockerClass.reason }
                );
                // First-class blocked terminal state (#190) — the structured
                // pr-{N}.json blocker record (when the stage wrote one)
                // carries the most precise remediation.
                const blockerRecord = this.readPrBlockerRecord(issueNumber);
                this.blockedTerminalState = {
                  blocker:
                    blockerRecord?.classification ??
                    `repo-config: ${blockerClass.reason ?? "branch protection"}`,
                  remediation: blockerRecord?.remediation ?? blockerClass.reason,
                  prNumber: blockerRecord?.prNumber,
                };
                if (this.stateService) {
                  try {
                    await this.stateService.failStage(stage, blockedError.message);
                  } catch {
                    // Non-critical — the break below still fails the pipeline.
                  }
                }
                failedStage = stage;
                error = blockedError;
                break;
              }

              this.logger.warn("Post-merge verification: PR still OPEN — retrying pr-merge once", {
                issueNumber,
              });

              // Update state to reflect the retry
              if (this.stateService) {
                try {
                  await this.stateService.batchUpdate((state) => {
                    const stageState = state.stages["pr-merge"];
                    stageState.status = "failed";
                    stageState.error = mergeVerifyError.message;
                    stageState.is_retrying = true;
                    stageState.auto_retry_count = (stageState.auto_retry_count ?? 0) + 1;
                    return state;
                  });
                } catch {
                  // Non-critical
                }
              }

              // #185: carry attempt 1's blocker context into the retry via
              // feedback-{N}.json — the skill's feedback intake reads it, so
              // attempt 2 starts from what already failed instead of a
              // byte-identical blank slate.
              this.writePrMergeRetryFeedback(
                issueNumber,
                `pr-merge attempt 1 exited without merging: ${mergeVerifyError.message}`,
                [mergeVerifyError.message]
              );

              // Clear duplicate-prevention guard so the retry can resolve (#698 vs #500)
              this.completedStageSet.delete(stage);

              // Retry the stage
              const retryResult = await this.runStage(
                stage,
                issueNumber,
                callbacks,
                undefined,
                undefined,
                this.pinnedWorkspaceRoot
              );

              if (retryResult.success) {
                // Verify again after retry
                const retryVerifyError = await this.verifyPostMergeState(issueNumber, 2, 2);
                if (retryVerifyError) {
                  this.logger.error("Post-merge verification failed after retry", { issueNumber });
                  // Retry exhausted, PR still not merged — blocked, not a
                  // generic failure (#190). Read the stage's structured
                  // blocker record for the classification/remediation.
                  if (retryVerifyError.message.includes("state: OPEN")) {
                    const record = this.readPrBlockerRecord(issueNumber);
                    this.blockedTerminalState = {
                      blocker: record?.classification ?? "pr-unmerged: retry exhausted",
                      remediation:
                        record?.remediation ??
                        "Inspect the PR's failing checks / branch rules and merge manually once clear.",
                      prNumber: record?.prNumber,
                    };
                  }
                  failedStage = stage;
                  error = retryVerifyError;
                  break;
                }
                // Retry succeeded and the gate confirms MERGED (#266).
                this.prMergedGroundTruth = true;
                // Retry succeeded — clear retry state and continue
                if (this.stateService) {
                  try {
                    await this.stateService.clearRetrying(stage);
                  } catch {
                    // Non-critical
                  }
                }
                this.eventDispatcher.onStageComplete(stage, retryResult);
                completedStages.push(stage);
                this.logger.info("Post-merge verification passed after retry", {
                  issueNumber,
                });
                // Auto-close parent epic if all sub-issues now closed (non-blocking).
                await this.invokePostMergeHook(issueNumber);
                // Skip the normal onStageComplete below since we already fired it
                continue;
              } else {
                failedStage = stage;
                error = retryResult.error ?? new Error("pr-merge retry failed");
                break;
              }
            }

            // PR is CLOSED or other non-retryable state
            failedStage = stage;
            error = mergeVerifyError;
            break;
          }
          // Initial verification passed — auto-close parent epic if all
          // sub-issues are now closed. Non-blocking; never aborts the loop.
          await this.invokePostMergeHook(issueNumber);
        }

        // ===================================================================
        // EARLY SPINNER: Signal UI to show spinner for next stage immediately
        // after stage succeeds, BEFORE any post-stage validation work.
        // This eliminates the 15-30s perceived gap between checkmark and
        // spinner caused by syncStageStatusTransition and context validation.
        // onStageStart is idempotent — runStage() fires it again harmlessly.
        // @see Issue #981 (moved from pre-delay position, Issue #634)
        // ===================================================================
        let earlySpinnerFired = false;
        if (this.config.autoContinue && stage !== "pipeline-finish") {
          const nextStageIndex = STAGE_ORDER.indexOf(stage) + 1;
          const nextStage = STAGE_ORDER[nextStageIndex];
          if (nextStage) {
            // Don't show spinner for stages that will be deferred (#981)
            const willDefer = this.config.deferMerge && DEFERRABLE_STAGES.includes(nextStage);
            if (!willDefer) {
              this.eventDispatcher.onStageStart(nextStage);
              earlySpinnerFired = true;
            }
          }
        }

        // ===================================================================
        // Generic context file validation for ALL successful skill stages.
        // Validates that the stage wrote its expected output context file.
        // Replaces the previous issue-pickup-only validation.
        // @see Issue #637 - Context file handoff validation
        // ===================================================================
        const contextError = await this.validateStageContextOutput(stage, issueNumber);
        if (contextError) {
          // Issue #3267: the per-stage if/else cascade that used to live here
          // (issue-pickup / pr-create / feature-dev / feature-validate /
          // feature-planning, ~300 lines of identical fallback boilerplate)
          // collapsed into a single dispatch through attemptContextRecovery.
          // Behaviour preserved: every stage that has a registered fallback
          // generator gets one chance to synthesize the missing context and
          // re-validate; on success the pipeline continues, on failure the
          // stage is marked failed and the loop breaks. Stages without a
          // fallback fall through to the failure path below.
          const recovery = await this.attemptContextRecovery(stage, issueNumber, contextError);
          if (recovery.recovered) {
            // Continue pipeline — context recovered deterministically.
          } else {
            failedStage = stage;
            error = recovery.error;
            // Issue #1280: Update state so the UI shows the stage as FAILED,
            // not complete. The skill process exited 0 so completeStage() was
            // already called; failStage() overwrites that status.
            if (this.stateService) {
              try {
                await this.stateService.failStage(stage, recovery.error.message);
              } catch (err) {
                this.logger.warn("Failed to update state on context validation failure", {
                  stage,
                  err,
                });
              }
            }
            // Issue #1201: Surface validation error when early spinner is showing.
            // Without this, the UI shows "Starting [Next Stage]..." indefinitely.
            if (earlySpinnerFired) {
              this.eventDispatcher.onStageError(stage, recovery.error);
            }
            break;
          }
        }

        // ===================================================================
        // DETERMINISTIC EPIC BASE_BRANCH ENFORCEMENT
        // After issue-pickup succeeds (whether by subagent or fallback),
        // verify that sub-issues of an epic target the epic branch, not main.
        // The subagent may write base_branch: "main" even when the issue is
        // a sub-issue of an epic. This deterministic override prevents PRs
        // from incorrectly targeting main and bypassing the epic flow.
        // @see Issue #1452 — #1463 merged to main instead of epic branch
        // ===================================================================
        if (stage === "issue-pickup" && !failedStage) {
          const epicBaseResult = await this.enforceEpicBaseBranch(issueNumber);
          if (!epicBaseResult.ok) {
            // Confirmed epic sub-issue with no establishable epic branch —
            // fail-closed rather than let pr-merge target main. (#1452 + the
            // acmeapp-platform#6/#7 direct-push incident.)
            failedStage = "issue-pickup";
            error = new Error(epicBaseResult.error ?? "epic base_branch enforcement failed");
            this.logger.error("Pipeline halted: epic base_branch could not be established", {
              issueNumber,
            });
            break;
          }
        }

        // ===================================================================
        // CONTEXT FILE SIZE MEASUREMENT (Issue #1009)
        // After context validation, measure the output context file size
        // and store it in PipelineStateService for telemetry/JSONL records.
        // Non-critical: failures log a warning, never break the pipeline.
        // ===================================================================
        await this.measureContextFileSize(stage, issueNumber);

        // ===================================================================
        // PR-CREATE STATE VALIDATION (Issue #1139)
        // After pr-create succeeds, verify that pipeline state correctly
        // reflects completion. If state is lost but pr-{N}.json has a valid
        // PR URL, recover state from the context file. If both are missing,
        // retry once with an upgraded model (sonnet instead of haiku).
        // ===================================================================
        if (stage === "pr-create") {
          const stateValidation = await this.validatePrCreateState(issueNumber);

          // Enrich pipeline state with PR number for Discord/UI
          try {
            const prCtxPath = this.getContextPath("pr", issueNumber);
            if (fs.existsSync(prCtxPath)) {
              const prCtx = JSON.parse(fs.readFileSync(prCtxPath, "utf-8"));
              const prUrlStr = prCtx.pr_url ?? prCtx.url ?? "";
              const prNumMatch = prUrlStr.match(/\/pull\/(\d+)/);
              if (prNumMatch) {
                this.stateService?.setMeta({
                  pr_number: parseInt(prNumMatch[1], 10),
                });
              }
            }
          } catch {
            // Non-critical
          }

          if (stateValidation === "retry-needed") {
            if (!this.prCreateRetryAttempted.has(issueNumber)) {
              this.prCreateRetryAttempted.add(issueNumber);

              this.logger.info("PR creation failed — retrying with upgraded model", {
                issueNumber,
                previousModel: "haiku",
                retryModel: "sonnet",
              });

              this.eventDispatcher.onStageError(
                stage,
                new Error("PR creation failed — retrying with upgraded model (sonnet)")
              );

              // Override model to sonnet for retry via env var
              const envKey = "NIGHTGAUGE_PIPELINE_STAGE_MODEL_PR_CREATE";
              const previousEnv = process.env[envKey];
              process.env[envKey] = "sonnet";

              try {
                const retryResult = await this.runStage(
                  stage,
                  issueNumber,
                  callbacks,
                  undefined,
                  undefined,
                  this.pinnedWorkspaceRoot
                );
                if (!retryResult.success) {
                  failedStage = stage;
                  error =
                    retryResult.error || new Error("pr-create retry with upgraded model failed");
                  if (earlySpinnerFired) {
                    this.eventDispatcher.onStageError(stage, error);
                  }
                  break;
                }
                // Re-validate after retry
                const retryValidation = await this.validatePrCreateState(issueNumber);
                if (retryValidation !== "ok" && retryValidation !== "recovered") {
                  failedStage = stage;
                  error = new Error(
                    "pr-create state validation failed after retry with upgraded model"
                  );
                  if (earlySpinnerFired) {
                    this.eventDispatcher.onStageError(stage, error);
                  }
                  break;
                }
              } finally {
                // Restore original env var (security: prevent leakage)
                if (previousEnv === undefined) {
                  delete process.env[envKey];
                } else {
                  process.env[envKey] = previousEnv;
                }
              }
            } else {
              // Already retried once — fail permanently
              failedStage = stage;
              error = new Error("PR creation failed after retry with upgraded model");
              if (earlySpinnerFired) {
                this.eventDispatcher.onStageError(stage, error);
              }
              break;
            }
          } else if (stateValidation === "recovered") {
            this.logger.info("PR state recovered from context file", {
              issueNumber,
            });
          }
          // 'ok' and 'warning' — continue normally
        }

        // =================================================================
        // PRE-MERGE MODEL RECORDING (Issue #1395)
        // After pr-create succeeds, record the execution outcome (complexity
        // model update) while still on the feature branch. The commit pushes
        // to the feature branch via `git push origin HEAD`, so the model
        // update merges with the PR — producing a single push to the base
        // branch and eliminating the redundant CI run caused by a post-merge
        // chore(model) commit.
        //
        // Safety:
        // - Idempotent: OutcomeRecorder skips if already recorded for this issue.
        // - Non-critical: failures are caught; pipeline-finish retries anyway.
        // - CI: pushing to feature branch re-triggers PR CI; pr-merge already
        //   waits for CI to pass before merging.
        // =================================================================
        if (stage === "pr-create" && this.stateService) {
          try {
            this.logger.info("Recording execution outcome pre-merge (on feature branch)", {
              issueNumber,
            });
            const outcomeResult = await this.stateService.recordExecutionOutcome("success");
            if (outcomeResult.success) {
              this.logger.info("Pre-merge model recording complete — update will merge with PR", {
                issueNumber,
              });
            } else {
              this.logger.warn(
                "Pre-merge model recording returned failure — pipeline-finish will retry",
                { issueNumber, error: outcomeResult.error }
              );
            }
          } catch (err) {
            this.logger.warn("Pre-merge model recording failed — will retry at pipeline-finish", {
              issueNumber,
              err: err instanceof Error ? err.message : String(err),
            });
          }
        }

        // After issue-pickup completes successfully, perform stage-specific
        // checks: branch validation and routing decision loading.
        // Note: Context file existence is validated by the generic
        // validateStageContextOutput() call above. @see Issue #637
        if (stage === "issue-pickup") {
          const contextPath = this.getIssueContextPath(issueNumber);

          // Defense-in-depth: verify issue-pickup actually switched to the
          // feature branch recorded in the context file. This prevents false
          // success where context is written but git stays on main/master.
          const expectedBranch = this.getExpectedBranchFromContext(contextPath);
          const currentBranch = await this.getCurrentGitBranch();
          if (expectedBranch && currentBranch && expectedBranch !== currentBranch) {
            this.logger.error("Branch mismatch after successful issue-pickup", {
              issueNumber,
              expectedBranch,
              currentBranch,
              contextPath,
            });

            // Attempt deterministic recovery before failing the stage.
            const recovered = await this.tryRecoverExpectedBranch(
              issueNumber,
              expectedBranch,
              currentBranch
            );
            const branchAfterRecovery = await this.getCurrentGitBranch();
            if (!recovered || branchAfterRecovery !== expectedBranch) {
              failedStage = stage;
              error = new Error(
                `issue-pickup reported success but git branch is "${branchAfterRecovery ?? currentBranch}" ` +
                  `instead of expected "${expectedBranch}". Automatic checkout recovery failed.`
              );
              // Issue #1201: Surface error when early spinner is showing
              if (earlySpinnerFired) {
                this.eventDispatcher.onStageError(stage, error);
              }
              break;
            }

            this.logger.info("Branch mismatch recovered after issue-pickup", {
              issueNumber,
              expectedBranch,
            });
          }

          // Note: Epic detection is now handled by the pre-check in runPipeline()
          // BEFORE any stages run. The deterministic gh CLI check there prevents
          // all AI token waste. @see isEpicIssue() and handleEpicIssue()

          this.currentRoutingDecision = await this.loadRoutingDecision(issueNumber);

          if (this.currentRoutingDecision) {
            this.logger.info("Routing decision loaded", {
              route: this.currentRoutingDecision.route,
              executeStages: this.currentRoutingDecision.executeStages,
              skipStages: this.currentRoutingDecision.skipStages,
            });

            // Enrich pipeline state with routing metadata for Discord/UI
            this.stateService?.setMeta({
              route: this.currentRoutingDecision.route,
              skip_stages: this.currentRoutingDecision.skipStages,
            });

            // Notify via callback
            this.eventDispatcher.onRoutingDecisionLoaded(this.currentRoutingDecision);
          }

          // Persist issue labels to pipeline state for sidebar display (Issue #1611)
          try {
            const ctxPathForLabels = this.getIssueContextPath(issueNumber);
            const ctxRawForLabels = fs.existsSync(ctxPathForLabels)
              ? JSON.parse(fs.readFileSync(ctxPathForLabels, "utf-8"))
              : null;
            const issueLabels: string[] = (ctxRawForLabels?.labels ?? []).map(
              (l: { name?: string } | string) => (typeof l === "string" ? l : (l.name ?? ""))
            );
            if (issueLabels.length > 0) {
              await this.stateService?.setLabels(issueLabels);
            }
          } catch {
            // Non-critical — labels are cosmetic
          }

          // Validate size estimate against calibration history (Issue #1589)
          try {
            const ctxPath = this.getIssueContextPath(issueNumber);
            const ctxRaw = fs.existsSync(ctxPath)
              ? JSON.parse(fs.readFileSync(ctxPath, "utf-8"))
              : null;
            const ctxLabels = this.normalizeLabels(ctxRaw?.labels ?? []);
            const sizeLabel = this.extractSizeLabel(ctxLabels);
            if (sizeLabel) {
              const { validateSizeEstimate } = await import("../utils/changeAnalyzer");
              const validation = await validateSizeEstimate(this.getWorkingDirectory(), sizeLabel);
              if (validation?.is_outlier) {
                this.logger.warn("Size estimate outlier detected", {
                  issueNumber,
                  sizeBucket: validation.size_bucket,
                  costRatio: validation.cost_ratio,
                  reasons: validation.outlier_reasons,
                  summary: validation.summary,
                });
              }
            }
          } catch {
            // Non-critical — calibration validation is informational only
          }
        }

        // ===================================================================
        // MODEL ESCALATION ENGINE (Issue #1343)
        // Check for MODEL_ESCALATION_NEEDED before backtrack evaluation.
        // Escalation retries the same stage with a more capable model without
        // consuming backtrack budget.
        // ===================================================================
        const escalationSignal = this.readEscalationSignal(stage, issueNumber);
        if (escalationSignal) {
          const nextModel = this.evaluateEscalation(
            stage,
            escalationSignal,
            issueNumber,
            callbacks
          );
          if (nextModel !== null) {
            await this.executeEscalation(
              stage,
              escalationSignal,
              nextModel,
              issueNumber,
              callbacks
            );
            // Re-run same stage: decrement so loop increment brings us back
            stageIndex = stageIndex - 1;
            continue;
          }
          // If escalation blocked, fall through to backtrack evaluation
        }

        // ===================================================================
        // FEEDBACK LEARNING ENGINE (Issue #1348)
        // When COMPLEXITY_UNDERESTIMATED is emitted, update the complexity
        // model immediately — do not wait for post-merge outcome recording.
        // Fires before backtrack evaluation so it records regardless of
        // whether the signal causes a backtrack.
        // ===================================================================
        await this.applyFeedbackLearning(stage, issueNumber);

        // ===================================================================
        // BACKTRACK ENGINE (Issue #1342)
        // After context validation succeeds, read feedback signals from the
        // completed stage's context file. If a blocking signal with a
        // backtrack_target_stage is found, evaluate eligibility and rewind.
        // ===================================================================
        const feedbackSignals = this.readFeedbackSignals(stage, issueNumber);
        if (feedbackSignals.length > 0) {
          const signal = feedbackSignals[0]; // Act on first blocking signal
          const canBacktrack = this.evaluateBacktrack(signal, stage, issueNumber, callbacks);
          if (canBacktrack) {
            const targetIndex = await this.executeBacktrack(signal, stage, issueNumber, callbacks);
            // Rewind: set stageIndex so the loop increments to targetIndex
            stageIndex = targetIndex - 1;
            continue;
          }
          // If blocked, continue forward (signal surfaced via callback)
        }

        completedStages.push(stage);
        await this.syncStageStatusTransition(stage, issueNumber);

        // ===================================================================
        // POST-PLANNING EARLY-EXIT: After feature-planning completes, check
        // if it signaled "verify-and-close" (issue already resolved). If so,
        // skip remaining stages and run a lightweight close path instead.
        // This saves ~60% of pipeline cost for already-resolved issues.
        // @see Issue #708
        // ===================================================================
        if (stage === "feature-planning") {
          const earlyExit = this.checkPlanningEarlyExit(issueNumber);
          if (earlyExit) {
            this.logger.info("Early exit: feature-planning signaled already-resolved issue", {
              issueNumber,
              approach: earlyExit.approach,
            });

            // Run lightweight close path (best-effort)
            await this.runAlreadyResolvedClosePath(issueNumber, callbacks);

            // Classify outcome for cost tracking
            if (this.stateService) {
              try {
                await this.stateService.setOutcomeType("already-resolved");
              } catch {
                // Non-critical
              }
            }

            // Mark remaining stages as skipped (including pipeline-finish)
            const planningIndex = STAGE_ORDER.indexOf("feature-planning");
            const remainingStages = STAGE_ORDER.slice(planningIndex + 1);
            skippedStages.push(...remainingStages);

            this.eventDispatcher.onEarlyExit(issueNumber, "already-resolved");
            break; // Exit stage loop
          }
        }

        // Check if pipeline was paused (Issue #239)
        // If paused, gracefully break after completing current stage
        if (this.stateService) {
          const isPaused = await this.stateService.isPaused();
          if (isPaused) {
            this.logger.info("Pipeline paused after stage complete", {
              stage,
              issueNumber,
            });
            break;
          }
        }

        // =================================================================
        // PIPELINE BUDGET CEILING — between-stage check (Issue #1047)
        // After each stage completes, check cumulative cost against the
        // pipeline ceiling. If exceeded, stop the pipeline gracefully.
        // =================================================================
        if (this.stateService) {
          try {
            const currentState = await this.stateService.getState();
            if (currentState) {
              // #257: re-resolve the base ceiling config from disk at every
              // between-stage check instead of reusing the instance captured
              // once at the top of runPipeline(). A live edit to
              // `pipeline.token_budget_ceiling.ceiling_usd` mid-run reached
              // the per-stage checks in runStage() (which already call
              // getPipelineCeilingConfig() fresh per stage) but never this
              // loop, which kept enforcing the stale value for the rest of
              // the run. The read is cheap (small file read), matching the
              // freshness the per-stage instances already have.
              const liveCeilingConfig = getPipelineCeilingConfig();
              const liveCeiling = new PipelineBudgetCeiling(liveCeilingConfig);
              // #253: honor a mid-run "Increase Ceiling & Continue" — layer
              // the confirmed escalation override on top of the freshly-read
              // base so a live config edit can never silently drop it.
              if (this.ceilingOverrideUsd !== null) {
                liveCeiling.setOverrideCeiling(this.ceilingOverrideUsd);
              }
              const ceilingCheck = liveCeiling.check(currentState.tokens?.estimated_cost_usd ?? 0);

              if (ceilingCheck.shouldStop) {
                this.logger.error(ceilingCheck.message, {
                  issueNumber,
                  costUsd: ceilingCheck.currentCostUsd,
                  ceilingUsd: ceilingCheck.effectiveCeilingUsd,
                  lastCompletedStage: stage,
                });
                this.eventDispatcher.onStderr(stage, `${ceilingCheck.message}\n`);

                // Classify as budget-ceiling outcome
                try {
                  await this.stateService.setOutcomeType("budget-ceiling");
                } catch {
                  // Non-critical
                }

                // Don't set failedStage — this is a controlled stop, not a
                // failure. Flag it so the completion reconcile below doesn't
                // reclassify the run as a pr-merge failure (#253): a run that
                // stopped for budget mid-pipeline naturally has an unmerged
                // PR (or none), which is not a pr-merge defect.
                budgetCeilingStopped = true;
                break;
              }
            }
          } catch (err) {
            this.logger.warn("Failed to check pipeline budget ceiling", {
              issueNumber,
              err,
            });
          }
        }

        // Check for approval gates (only for skill stages)
        if (this.config.approvalGates.includes(stage)) {
          const approved = await this.eventDispatcher.onApprovalRequired(stage);

          if (!approved) {
            this.logger.info("Pipeline stopped - approval rejected", { stage });
            break;
          }
        }

        // Delay between stages if auto-continuing (not after last stage)
        // Note: onStageStart already fired at top of post-stage block (#981)
        if (this.config.autoContinue && stage !== "pipeline-finish") {
          await this.delay(this.config.stageContinueDelay);
        }
      }
    } finally {
      this.isRunning = false;
      this.currentStage = null;
      this.abortController = null;
      this.currentRoutingDecision = null; // Clear routing decision at end of pipeline
      this.cachedRoutingTelemetry = null; // Clear routing telemetry cache (#1005)
      this.completedStageSet.clear(); // Clear duplicate-prevention tracker (#698)
      this.backtrackCount = 0; // Clear backtrack engine state (#1342)
      this.traversedEdges.clear(); // Clear oscillation guard (#1342)
      this.stageEscalationCounts.clear(); // Clear escalation engine (#1343)
      this.stageModelOverrides.clear(); // Clear escalation overrides (#1343)
      this.fableQuotaFallbackApplied.clear(); // Clear Fable→Opus usage-limit fallback guard
      this.fableFallbacks = []; // Clear surfaced Fable→Opus fallback list (#26)
      this.userModelOverride = null; // Clear user model override (#1610)
      this.proactiveEscalationApplied = false; // Clear proactive escalation guard (#1394)
      this.ceilingOverrideUsd = null; // Clear per-run ceiling override (#253)
      this.policyRetryBudgetIncrease = 0; // Clear health policy retry budget (#1395)
      this.pauseAutoRouting = false; // Clear health policy auto-routing pause (#1395)
      this.activePolicies = null; // Clear health policy overrides (#1395)
      this.pinnedWorkspaceRoot = undefined; // Clear pinned workspace root (#1592)

      // Emit pipeline.completed or pipeline.failed and dispose audit client (Issue #1582)
      if (this.auditClient && this.currentPipelineRunId) {
        const totalDurationMs = Date.now() - startTime;
        if (failedStage) {
          this.auditClient.enqueue({
            action: "pipeline.failed",
            resourceType: "pipeline",
            resourceId: `issue-${issueNumber}`,
            metadata: {
              pipelineRunId: this.currentPipelineRunId,
              issueNumber,
              totalDurationMs,
              failedStage,
              timestamp: new Date().toISOString(),
            },
          });
        } else {
          this.auditClient.enqueue({
            action: "pipeline.completed",
            resourceType: "pipeline",
            resourceId: `issue-${issueNumber}`,
            metadata: {
              pipelineRunId: this.currentPipelineRunId,
              issueNumber,
              totalDurationMs,
              stagesCompleted: completedStages,
              timestamp: new Date().toISOString(),
            },
          });
        }
        // dispose() calls flushAll() with a 5s timeout guard to avoid blocking
        void Promise.race([
          this.auditClient.dispose(),
          new Promise<void>((resolve) => setTimeout(resolve, 5_000)),
        ]).finally(() => {
          this.auditClient = null;
          this.currentPipelineRunId = null;
        });
      }

      // Clean up checkpoint signal file if it was written (Issue #1047)
      if (this.ceilingCheckpointWritten) {
        try {
          const signalPath = path.join(
            this.getWorkingDirectory(),
            ".nightgauge",
            "pipeline",
            `checkpoint-signal-${issueNumber}.json`
          );
          if (fs.existsSync(signalPath)) {
            fs.unlinkSync(signalPath);
          }
        } catch {
          // Non-critical: signal file cleanup failure is harmless
        }
        this.ceilingCheckpointWritten = false;
      }

      // Clean up wind-down signal file if it was written (Issue #2338)
      try {
        const winddownPath = path.join(
          this.getWorkingDirectory(),
          ".nightgauge",
          "pipeline",
          `winddown-signal-${issueNumber}.json`
        );
        if (fs.existsSync(winddownPath)) {
          fs.unlinkSync(winddownPath);
        }
      } catch {
        // Non-critical
      }
    }

    // ===================================================================
    // GUARD: pr-create succeeded but pr-merge never ran.
    // This catches an early-termination bug where the pipeline exits the
    // stage loop (abort, pause, error in bookend) after pr-create completes
    // but before pr-merge runs. A PR was created but never merged — that
    // is NOT a successful pipeline, it's an orphaned PR.
    // @see Issue #698
    // ===================================================================
    if (
      !failedStage &&
      deferredStages.length === 0 &&
      completedStages.includes("pr-create") &&
      !completedStages.includes("pr-merge") &&
      !skippedStages.includes("pr-merge")
    ) {
      this.logger.error("Pipeline exited after pr-create without running pr-merge — orphaned PR", {
        issueNumber,
        completedStages,
        skippedStages,
      });
      failedStage = "pr-merge";
      error = new Error(
        `pr-create succeeded but pr-merge never ran for issue #${issueNumber}. ` +
          `The created PR is orphaned. This is a pipeline orchestration error — ` +
          `pr-merge must always run after pr-create in a full pipeline.`
      );
    }

    // ===================================================================
    // SHIPPED-BUT-OVERBUDGET OVERRIDE (#3108, broadened in #3274)
    // If pr-merge failed for any reason BUT the PR actually merged AND the
    // issue is CLOSED, treat the run as a success. Originally this caught
    // budget kills (#3108); #3274 broadened it to also catch stale skill
    // exit signals (e.g. subagent emits "Failure cleanup complete" text
    // even though the merge already landed and the issue closed).
    //
    // Detection requires BOTH deterministic gates: PR=MERGED *and*
    // issue=CLOSED. The dual gate guards against the cross-slot
    // hallucination scenario from the bug report (subagent reports the
    // wrong PR number). Any failure to read either state leaves the
    // original failure intact (fail-closed).
    // ===================================================================
    let shippedButOverbudget = false;
    if (failedStage === "pr-merge") {
      const verified = await this.checkPrMergedAndIssueClosed(issueNumber);
      if (verified && verified.issueClosed) {
        const reason = budgetExceeded
          ? "pr-merge budget kill — PR is MERGED + issue CLOSED"
          : "pr-merge stale failure signal — deterministic gate confirms MERGED + issue CLOSED";
        this.logger.warn(`Reclassifying pr-merge failure as shipped-but-overbudget — ${reason}`, {
          issueNumber,
          prNumber: verified.prNumber,
          budgetExceeded,
        });
        shippedButOverbudget = true;
        this.prMergedGroundTruth = true; // #266: forge confirms MERGED
        if (!completedStages.includes("pr-merge")) {
          completedStages.push("pr-merge");
        }
        failedStage = undefined;
        error = undefined;
      }
    }

    // Only reconcile completion side-effects (issue closure, label sync)
    // when no stages were deferred and no early-exit close path already ran.
    // Deferred stages mean pr-merge hasn't run yet, so issue is expected open.
    // Early-exit (feature-dev skipped) means runAlreadyResolvedClosePath()
    // already handled close/labels/sync. @see Issue #628, #708
    const earlyExitHandled = skippedStages.includes("feature-dev");
    if (!failedStage && deferredStages.length === 0 && !earlyExitHandled && !budgetCeilingStopped) {
      // #253: time-bound the network-bound completion awaits. A wedge in the
      // `gh`-backed reconcile stranded run #236 for 8.5h with no terminal
      // record — completing the run WITHOUT closure sync beats never
      // completing it.
      const reconcile = await promiseWithTimeout(
        this.reconcileCompletionSideEffects(issueNumber),
        COMPLETION_RECONCILE_TIMEOUT_MS,
        "reconcileCompletionSideEffects"
      ).catch((err) => {
        this.logger.error(
          "Completion reconcile failed/timed out — completing run without closure sync (#253 zombie guard)",
          { issueNumber, err: err instanceof Error ? err.message : String(err) }
        );
        return null;
      });
      if (!reconcile) {
        // Degraded completion: skip closure/label sync entirely.
      } else if (reconcile.epicDeferred) {
        this.logger.info("Epic sub-issue — closure deferred to epic merge", {
          issueNumber,
        });
      } else if (reconcile.verified && !reconcile.issueClosed) {
        // #3691: classify the actual blocker so the operator gets a
        // useful pause-reason ("PR #N has failing checks: foo, bar"
        // instead of generic "issue still open"). The marker text
        // `[pr-merge-unmerged:<blocker>]` is parsed by Go's
        // ClassifyTerminalKind into TerminalKindPrMergeUnmerged so
        // autonomous routes through the recoverable branch (no
        // LifetimeIssueFailures increment) and surfaces the rich
        // summary as the pause reason.
        const diagnosis = await promiseWithTimeout(
          this.diagnosePrMergeBlocker(issueNumber),
          PR_MERGE_DIAGNOSIS_TIMEOUT_MS,
          "diagnosePrMergeBlocker"
        ).catch((err) => {
          this.logger.warn("pr-merge blocker diagnosis failed/timed out — proceeding without it", {
            issueNumber,
            err: err instanceof Error ? err.message : String(err),
          });
          return null;
        });
        failedStage = "pr-merge";
        if (diagnosis && diagnosis.blocker === "agent_gave_up") {
          // The PR is verifiably ready (MERGEABLE + CLEAN + green + no review
          // block) — the agent just stopped. Don't pause for a human; finish
          // the job deterministically, the same recovery the Go scheduler's
          // SkillExitedWithoutMerging action performs on its path. Only on
          // failure do we fall through to the diagnostic-pause path below.
          const recovered = await this.attemptDeterministicPrMerge(diagnosis.prNumber, issueNumber);
          if (recovered) {
            this.logger.info("pr-merge auto-recovered — agent left a mergeable PR; merged it", {
              issueNumber,
              prNumber: diagnosis.prNumber,
              prUrl: diagnosis.prUrl,
            });
            this.eventDispatcher?.onStderr(
              "pr-merge",
              `[pr-merge auto-recovery] PR #${diagnosis.prNumber} was mergeable but the agent ` +
                `did not merge it — merged automatically. ${diagnosis.prUrl}\n`
            );
            // Treat pr-merge as actually succeeded: clear the failure so the
            // pipeline reports completion instead of a recoverable pause.
            failedStage = undefined;
            error = undefined;
          }
        }
        // Only emit a failure if recovery above didn't clear it. (failedStage
        // is reset to undefined when attemptDeterministicPrMerge succeeded.)
        if (failedStage) {
          if (diagnosis) {
            this.logger.warn("pr-merge completed but PR not merged — diagnostic classification", {
              issueNumber,
              prNumber: diagnosis.prNumber,
              prUrl: diagnosis.prUrl,
              prState: diagnosis.prState,
              mergeable: diagnosis.mergeable,
              blocker: diagnosis.blocker,
              failingChecks: diagnosis.failingChecks,
              summary: diagnosis.summary,
            });
            error = new Error(
              `[pr-merge-unmerged:${diagnosis.blocker}] ${diagnosis.summary} ` +
                `PR: ${diagnosis.prUrl}` +
                (diagnosis.failingChecks.length > 0
                  ? ` | failing-checks: ${diagnosis.failingChecks.join(", ")}`
                  : "") +
                ` | recoverable: no LifetimeIssueFailures increment; resume after the blocker is resolved.`
            );
          } else {
            // Fail-closed: diagnostic couldn't read PR state. Keep the
            // pre-#3691 generic path so a real failure isn't silently
            // softened into a recoverable one based on incomplete data.
            error = new Error(
              reconcile.error ?? `Pipeline completed but issue #${issueNumber} is still open.`
            );
          }
        }
      } else if (reconcile.issueClosed) {
        this.logger.info("Completion reconciliation finished", {
          issueNumber,
          mergedPrNumber: reconcile.mergedPrNumber,
          epicSweepClosed: reconcile.epicSweepClosed ?? 0,
        });
      }
    }

    // ===================================================================
    // POST-PIPELINE MAIN CHECKOUT: Ensure workspace is on the base branch
    // after pipeline completion (whether stages were deferred or not).
    // This prevents the next pipeline run from starting on a stale feature
    // branch. Non-critical: failures are logged but don't break the result.
    // ===================================================================
    if (deferredStages.length > 0 && !failedStage) {
      try {
        const workspaceRoot = this.getWorkingDirectory();
        const baseBranch = (await this.stateService?.getBaseBranch()) ?? "main";
        const { stdout: currentBranchRaw } = await execFileAsync(
          "git",
          ["branch", "--show-current"],
          { encoding: "utf-8", cwd: workspaceRoot }
        );
        const currentBranch = currentBranchRaw.trim();

        if (currentBranch && currentBranch !== baseBranch) {
          this.logger.info("Checking out base branch after deferred stages", {
            currentBranch,
            baseBranch,
          });
          await execFileAsync("git", ["checkout", baseBranch], {
            encoding: "utf-8",
            cwd: workspaceRoot,
            timeout: 10000,
          });
          // Best-effort pull
          try {
            await execFileAsync("git", ["pull", "origin", baseBranch], {
              encoding: "utf-8",
              cwd: workspaceRoot,
              timeout: 15000,
            });
          } catch {
            this.logger.warn("Failed to pull latest after checkout, continuing with local state");
          }
        }
      } catch (err) {
        this.logger.warn("Failed to checkout base branch after deferral", {
          issueNumber,
          err: err instanceof Error ? err.message : String(err),
        });
      }
    }

    // Record execution outcome for FAILURE case (Issue #650, #1177, #1199).
    // The TS-side recordExecutionOutcome is a no-op stub since #1899 moved
    // pipeline state to the Go binary, which records outcomes via
    // scheduler.recordOutcome on every pipeline completion (success +
    // failure). Calling it here is harmless and we drop the prior pr-create
    // guard — the message ("no PR data to capture") was misleading because
    // (a) the call is a no-op anyway and (b) the Go-side recorder DOES
    // capture pre-PR failures with stage + cost data. Issue board recovery
    // for terminal kills happens in the Go orchestrator's onPipelineComplete
    // (revertFailedIssueStatus).
    if (this.stateService && failedStage) {
      try {
        await this.stateService.recordExecutionOutcome("failure");
      } catch (err) {
        this.logger.warn("Failed to record execution outcome", {
          issueNumber,
          err: err instanceof Error ? err.message : String(err),
        });
      }
    }

    // Classify pipeline outcome for cost tracking (Issue #709)
    // Skip classification if early-exit already set the outcome (Issue #708)
    // Non-critical: failures log warnings but never break the pipeline
    let classifiedOutcome: PipelineOutcomeType | undefined;
    if (shippedButOverbudget) {
      classifiedOutcome = "shipped-but-overbudget";
      if (this.stateService) {
        try {
          await this.stateService.setOutcomeType(classifiedOutcome);
        } catch (err) {
          this.logger.warn("Failed to record shipped-but-overbudget outcome", {
            issueNumber,
            err: err instanceof Error ? err.message : String(err),
          });
        }
      }
    } else if (earlyExitHandled) {
      classifiedOutcome = "already-resolved";
    } else if (this.blockedTerminalState) {
      // Blocked terminal state (#190): record the outcome as blocked, not
      // generic failure, so outcome telemetry and the learning loop see the
      // recurrence of this blocker class.
      classifiedOutcome = "blocked";
      if (this.stateService) {
        try {
          await this.stateService.setOutcomeType(classifiedOutcome);
        } catch (err) {
          this.logger.warn("Failed to record blocked outcome", {
            issueNumber,
            err: err instanceof Error ? err.message : String(err),
          });
        }
      }
      this.logger.error("Pipeline ended BLOCKED — PR not delivered", {
        issueNumber,
        blocker: this.blockedTerminalState.blocker,
        remediation: this.blockedTerminalState.remediation,
        prNumber: this.blockedTerminalState.prNumber,
      });
    } else if (this.stateService) {
      try {
        classifiedOutcome = this.classifyPipelineOutcome(issueNumber, completedStages);
        await this.stateService.setOutcomeType(classifiedOutcome);
        this.logger.info("Pipeline outcome classified", {
          issueNumber,
          outcome_type: classifiedOutcome,
        });
      } catch (err) {
        this.logger.warn("Failed to classify pipeline outcome", {
          issueNumber,
          err: err instanceof Error ? err.message : String(err),
        });
      }
    }

    // Post-pipeline analysis (Issue #943) + self-check (Issue #1045)
    // Run whenever at least one real stage executed (success or failure).
    // Skill amendment detection needs history from failed runs too.
    let analysisResult: PostPipelineAnalysisResult | null = null;
    if (completedStages.length > 1 || !!failedStage) {
      try {
        analysisResult = await PostPipelineAnalyzer.analyze(
          this.getPersistentRoot(),
          issueNumber,
          this.logger
        );

        if (analysisResult) {
          this.logger.info("Post-pipeline analysis complete", {
            issueNumber,
            recommendations: analysisResult.recommendationCount,
            savings: `$${analysisResult.totalPotentialSavingsUsd.toFixed(4)}`,
            calibrationUpdated: analysisResult.calibrationUpdated,
          });
        }
      } catch (err) {
        // Non-critical: never break pipeline completion
        this.logger.warn("Post-pipeline analysis failed", {
          issueNumber,
          err: err instanceof Error ? err.message : String(err),
        });
      }

      // Health evaluation and self-check summary (Issue #1045)
      try {
        const healthEval = await HealthActionService.evaluate(
          this.getWorkingDirectory(),
          this.logger
        );

        // Log health actions
        if (healthEval) {
          for (const action of healthEval.actions) {
            if (action.level === "critical") {
              this.logger.warn(action.message);
              if (action.suggestion) {
                this.logger.info(action.suggestion);
              }
            } else if (action.level === "warning") {
              this.logger.warn(action.message);
            }
          }
        }

        // Enrich pipeline state with health score for Discord/UI
        if (healthEval) {
          this.stateService?.setMeta({ health_score: healthEval.score });
        }

        // Emit self-check summary
        const selfCheck = PostPipelineAnalyzer.formatSelfCheck(
          analysisResult,
          healthEval,
          0, // Cost not available at this level; downstream consumers can provide
          0 // Historical average not available here
        );
        this.logger.info(selfCheck);
      } catch (err) {
        // Non-critical: never break pipeline completion
        this.logger.warn("Self-check summary failed", {
          issueNumber,
          err: err instanceof Error ? err.message : String(err),
        });
      }
    }

    // ===================================================================
    // DEFENSIVE BOOKKEEPING RECONCILIATION (Issue #3450)
    //
    // Trust state.json over in-memory arrays. The in-memory `completedStages`
    // is built by pushes inside the stage loop, but the loop has many early
    // exits (abort signal, isPaused trip after pr-merge, post-merge verify,
    // backtrack, approval gates, deferral). Several of those exits drop the
    // pipeline out before pipeline-finish runs without ever setting
    // `failedStage`. Without this reconciliation, the post-loop condition
    // `pipelineComplete = !failedStage && len === STAGE_ORDER.length` evaluates
    // to `false` even when state.json shows every stage terminal — producing
    // the impossible `{ success: false, failedStage: undefined }` return that
    // ConcurrentPipelineManager.processSlot then routes through
    // haltQueueOnSlotFailure as "failed at unknown", incrementing
    // lifetimeIssueFailures and pausing autonomous on an actually-successful
    // run (see #3450 evidence: PR merged, issue closed, board moved to Done).
    //
    // The reconciliation only fires when:
    //   - No stage was explicitly marked failed (`failedStage === undefined`)
    //   - In-memory bookkeeping disagrees with disk state
    //   - State.json shows every STAGE_ORDER entry as terminal (complete |
    //     skipped | deferred), which is the same condition
    //     ExecutionHistoryWriter.buildRunRecord uses to classify outcome as
    //     "complete" (defense-in-depth, see #2994)
    //
    // It NEVER overrides a real failure — a non-undefined `failedStage` skips
    // this block entirely. It also does not silently move on if state.json
    // disagrees with disk (incomplete pipeline) — that case still flows
    // through the existing failure / pause / abort handling.
    // ===================================================================
    if (!failedStage && this.stateService) {
      const accountedFor = completedStages.length + skippedStages.length + deferredStages.length;
      if (accountedFor < STAGE_ORDER.length) {
        try {
          const diskState = await this.stateService.getState();
          if (diskState) {
            const reconciled = reconcileBookkeepingFromDiskState(
              { completedStages, skippedStages, deferredStages },
              (diskState.stages ?? {}) as Record<string, { status?: string }>,
              STAGE_ORDER
            );
            if (reconciled.changed) {
              this.logger.warn(
                "Pipeline bookkeeping reconciliation: state.json shows all stages terminal " +
                  "but in-memory arrays missed some — promoting to success (#3450).",
                {
                  issueNumber,
                  recovered: reconciled.recovered,
                  completedStagesBefore: accountedFor,
                  completedStagesAfter:
                    reconciled.completedStages.length +
                    reconciled.skippedStages.length +
                    reconciled.deferredStages.length,
                }
              );
              // Reassign by mutating in place (these arrays are still `const`
              // bindings declared above; we splice the recovered entries in
              // rather than replacing the reference).
              completedStages.length = 0;
              completedStages.push(...reconciled.completedStages);
              skippedStages.length = 0;
              skippedStages.push(...reconciled.skippedStages);
              deferredStages.length = 0;
              deferredStages.push(...reconciled.deferredStages);
            }
          }
        } catch (reconcileErr) {
          // Non-critical: if state.json is unreadable, fall through to the
          // existing pipelineComplete computation. Worst case we keep the
          // pre-fix behavior for this run; we never destroy correct state.
          this.logger.warn("Pipeline bookkeeping reconciliation failed", {
            issueNumber,
            err: reconcileErr instanceof Error ? reconcileErr.message : String(reconcileErr),
          });
        }
      }
    }

    // ===================================================================
    // LABEL CLEANUP: Revert status labels on failure/abort/stop so the
    // issue returns to the ready pool. Skip for paused (will resume) and
    // deferred (waiting for human review) pipelines. @see Issue #1115
    // ===================================================================
    const pipelineComplete =
      !failedStage &&
      completedStages.length + skippedStages.length + deferredStages.length === STAGE_ORDER.length;
    if (
      !pipelineComplete &&
      deferredStages.length === 0 &&
      completedStages.includes("issue-pickup")
    ) {
      let wasPaused = false;
      if (this.stateService) {
        try {
          wasPaused = await this.stateService.isPaused();
        } catch {
          // Non-critical
        }
      }
      if (!wasPaused) {
        await this.markStatusInReviewOnFailure(issueNumber);
      }
    }

    // Auto-retro analysis on pipeline failure (Issue #1408)
    // Fire-and-forget: never awaited, never blocks failure reporting.
    // Non-critical: errors are caught inside AutoRetroService.
    if (failedStage) {
      // Thread the orchestrator's terminal failure reason into the retro so
      // the classifier sees the authoritative verdict instead of starving on
      // the subagent log alone (#3926).
      void AutoRetroService.runAfterFailure(
        this.getPersistentRoot(),
        issueNumber,
        failedStage,
        this.logger,
        error?.message
      );
    }

    const result: PipelineRunResult = {
      success: pipelineComplete,
      completedStages,
      skippedStages,
      deferredStages,
      failedStage,
      error,
      blocked: this.blockedTerminalState ?? undefined,
      totalDurationMs: Date.now() - startTime,
      outcomeType: classifiedOutcome,
      analysisResult,
      activePolicies: this.activePolicies,
      budgetExceeded,
    };

    this.firePipelineComplete(result);

    // Notify queue service of pipeline completion for auto-start
    if (this.queueService) {
      this.handleQueueAutoStart(result.success, issueNumber);
    }

    return result;
  }

  /**
   * Handle queue auto-start after pipeline completion
   *
   * If the queue has items and autoStart is enabled, dequeues the next
   * issue and starts its pipeline after a delay.
   *
   * On failure with queued items, prompts user: "[Stop Queue] [Continue to Next]"
   *
   * @param success - Whether the pipeline completed successfully
   * @param completedIssueNumber - The issue that just completed
   *
   * @see Issue #299 - Pipeline queueing with sequential processing
   */
  /**
   * Start the next queued issue's pipeline
   *
   * Initializes pipeline state and runs the full pipeline for the given
   * queue item. Used by both auto-start (after pipeline completion) and
   * manual resume (play button when queue is waiting).
   *
   * @param item - The queue item to start
   */
  async startNextQueuedIssue(item: { issueNumber: number; title?: string }): Promise<void> {
    // Initialize pipeline state for the queued issue
    if (this.stateService) {
      try {
        await this.stateService.clearPipeline();
      } catch {
        // Ignore - state may not exist yet
      }
      await this.stateService.initializePipeline(
        item.issueNumber,
        item.title || `Issue #${item.issueNumber}`,
        `feat/${item.issueNumber}` // Placeholder - issue-pickup updates with real branch
      );
      await this.stateService.setExecutionMode("automatic");
    }

    // Merge default callbacks (OutputWindow integration) with queue-specific overrides
    const pipelineCallbacks: PipelineCallbacks = {
      ...this.defaultPipelineCallbacks,
      onApprovalRequired: async () => true,
      onBackwardTransitionConfirm: async () => false,
      onBacktrackTriggered: () => {}, // No-op in queue mode (#1342)
      onBacktrackBlocked: () => {}, // No-op in queue mode (#1342)
      onModelEscalated: () => {}, // No-op in queue mode (#1343)
      onEscalationBlocked: () => {}, // No-op in queue mode (#1343)
    };

    this.logger.info("Starting pipeline for queued issue", {
      issueNumber: item.issueNumber,
    });

    await this.runPipeline(item.issueNumber, pipelineCallbacks);
  }

  private async handleQueueAutoStart(
    success: boolean,
    completedIssueNumber: number
  ): Promise<void> {
    if (!this.queueService) {
      return;
    }

    // Check stop-queue-after-current flag BEFORE touching the queue.
    // This must happen before onPipelineComplete() which may dequeue the
    // next item — if we checked after, the dequeued item would be lost.
    // @see Issue #1785
    if (this.shouldStopQueueAfterCurrent) {
      this.logger.info("Queue auto-start skipped - stop-after-current flag is set", {
        completedIssueNumber,
      });
      this.shouldStopQueueAfterCurrent = false;
      vscode.commands.executeCommand("setContext", "nightgauge.stopAfterCurrentQueue", false);
      return;
    }

    try {
      await this.queueService.onPipelineComplete(success, completedIssueNumber);

      // Dequeue next item from Go queue
      const nextItem = await this.queueService.dequeue();

      if (!nextItem) {
        // Queue is empty, paused, or autoStart disabled
        return;
      }

      this.logger.info("Auto-starting next queued issue", {
        completedIssueNumber,
        nextIssueNumber: nextItem.issueNumber,
        nextTitle: nextItem.title,
      });

      // Get config delay
      const config = this.queueService.getConfig();
      const delay = config.autoStartDelay;

      // Show notification about auto-start
      vscode.window.showInformationMessage(
        `Pipeline complete for #${completedIssueNumber}. ` +
          `Starting #${nextItem.issueNumber} - ${nextItem.title} in ${delay / 1000}s...`
      );

      // Delay then start next pipeline
      await this.delay(delay);

      await this.startNextQueuedIssue(nextItem);
    } catch (error) {
      this.logger.error("Failed to auto-start next queued issue", {
        completedIssueNumber,
        error: error instanceof Error ? error.message : "Unknown error",
      });
    }
  }

  /**
   * Run a bookend stage synchronously (no Claude CLI, zero AI tokens)
   *
   * Bookend stages provide reliable synchronization points:
   * - pipeline-start: Initialize state, reset tokens, fire stage:start event
   * - pipeline-finish: Aggregate metrics, fire pipeline:complete event
   *
   * @param stage - The bookend stage to run
   * @param issueNumber - The GitHub issue number
   * @param callbacks - Callbacks for stage events
   * @returns Result of the stage run
   */
  private async runBookendStage(
    stage: PipelineStage,
    issueNumber: number,
    callbacks?: PipelineCallbacks
  ): Promise<StageRunResult> {
    const startTime = Date.now();

    this.logger.info("Running bookend stage", { stage, issueNumber });
    this.currentStage = stage;

    // Notify stage start
    this.eventDispatcher.onStageStart(stage);

    try {
      // Update state service to mark stage as running
      if (this.stateService) {
        await this.stateService.startStage(stage, { forceBackward: true });
      }

      // Execute stage-specific logic
      if (stage === "pipeline-start") {
        // pipeline-start: Initialization is handled by startStage above
        // Additional initialization can be added here if needed
        this.logger.info("Pipeline initialized", { issueNumber });
      } else if (stage === "pipeline-finish") {
        // pipeline-finish: Final aggregation and cleanup
        // State persists until user explicitly clears (per acceptance criteria)
        this.logger.info("Pipeline finishing", { issueNumber });
        // Brief pause so the tree renders "running" before completing
        await new Promise((r) => setTimeout(r, 500));

        // The run-record history JSONL is written EXCLUSIVELY by the Go
        // binary's pipeline.notifyComplete handler (#232) — the sole
        // authoritative writer for the extension/HeadlessOrchestrator path, for
        // both success and failure. The legacy TS bookend write that lived here
        // was deleted (#313): it raced that writer, wrote to a different repo
        // root, and — because buildRunRecord derived outcome from `state.stages`
        // that had already been cleared at pipeline-finish — stamped a bogus
        // outcome="cancelled" record next to the authoritative "complete" one.
        // This block now only fans out telemetry + alerting, which are distinct
        // sinks and do not touch the run-record JSONL.
        const state = await this.stateService?.getState();
        if (state) {
          // Issue metadata still feeds telemetry + alerting below (Issue #844).
          const metadata = this.loadIssueMetadata(issueNumber);

          // Telemetry submission (Issue #1480 — fire-and-forget; opt-in gated inside TelemetryService)
          const telemetrySvc = TelemetryService.getInstance();
          if (telemetrySvc) {
            const issueLabels = metadata?.labels ?? state.labels ?? [];
            const sizeLabel =
              issueLabels.find((l: string) => l.startsWith("size:"))?.replace("size:", "") ?? null;
            const typeLabel =
              issueLabels.find((l: string) => l.startsWith("type:"))?.replace("type:", "") ?? null;
            await telemetrySvc.recordPipelineExecution({
              state,
              issueMetadata: {
                issueNumber: state.issue_number,
                sizeLabel,
                typeLabel,
              },
              startedAt: new Date(state.started_at),
              completedAt: new Date(),
            });
          }

          // Post-run alerting check (Issue #1048, Issue #1335)
          try {
            const alertingConfig = getAlertingConfig(this.getPersistentRoot());
            if (alertingConfig.enabled) {
              const costUsd = state.tokens?.estimated_cost_usd ?? 0;
              const startedAt = state.started_at ? new Date(state.started_at).getTime() : 0;
              const durationMinutes = startedAt > 0 ? (Date.now() - startedAt) / 60000 : 0;

              // Compute model/complexity-aware estimated cost (Issue #1335)
              let estimatedCostUsd = 0;
              let estimatedPerStageCosts: Record<string, number> = {};
              try {
                const { AutoModelSelector, CalibrationService } = await import("@nightgauge/sdk");
                const { getPerformanceMode } =
                  await import("../utils/resolvers/monitoringResolver");
                const selector = new AutoModelSelector();
                const issueMetadataForEstimate = this.loadIssueMetadata(issueNumber);
                const persistentRoot = this.getPersistentRoot();
                const calibrationPath = CalibrationService.getDefaultPath(persistentRoot);
                const calibration = await CalibrationService.load(calibrationPath);
                // Issue #3216: thread the active performance mode so the
                // calibration lookup hits the correct (mode, size) bucket.
                const performanceMode = getPerformanceMode(persistentRoot);
                const estimate = selector.estimatePipelineCost(
                  {
                    labels: issueMetadataForEstimate?.labels ?? [],
                    title: issueMetadataForEstimate?.title ?? `Issue #${issueNumber}`,
                  },
                  this.cachedRoutingTelemetry?.skip_stages ?? [],
                  calibration,
                  performanceMode
                );
                estimatedCostUsd = estimate.totalEstimatedCost;
                estimatedPerStageCosts = Object.fromEntries(
                  estimate.stages.map((s: { stage: string; estimatedCost: number }) => [
                    s.stage,
                    s.estimatedCost,
                  ])
                );
              } catch {
                // Non-critical: fall through with estimatedCostUsd = 0
              }

              // Build actual per-stage costs from state
              const perStageCosts: Record<string, number> = {};
              if (state.tokens?.per_stage) {
                for (const [stage, usage] of Object.entries(state.tokens.per_stage)) {
                  perStageCosts[stage] = usage.cost_usd ?? 0;
                }
              }

              const alertResult = checkPipelineAlerts({
                issueNumber,
                costUsd,
                estimatedCostUsd,
                durationMinutes,
                thresholds: alertingConfig,
                perStageCosts,
                estimatedPerStageCosts,
              });

              for (const alert of alertResult.alerts) {
                this.logger.warn(alert.message);
                if (alert.stageBreakdown) {
                  for (const s of alert.stageBreakdown) {
                    this.logger.warn(
                      `  ${s.stage}: actual=$${s.actualCost.toFixed(3)}, estimated=$${s.estimatedCost.toFixed(3)}`
                    );
                  }
                }
              }

              // Store anomaly flag for CompletedIssuesService (Issue #1335)
              this._lastCostAnomalyExceeded = alertResult.costExceeded;
            }
          } catch {
            // Alerting is non-critical — never block pipeline completion
          }
        }
      }

      // Capture reviewer feedback signals before outcome recording (Issue #1409)
      // This runs after execution history write but before completeStage,
      // so PR context files are still available for reading pr_number.
      if (stage === "pipeline-finish") {
        await this.captureReviewerFeedback(issueNumber);
      }

      // Outcome recording now fires automatically via completeStage('pipeline-finish')
      // in PipelineStateService (Issue #1200). completeStage must run BEFORE cleanup
      // so context files are still available for reading issue/PR data.

      // Complete pipeline-finish HERE (before context cleanup below) so outcome
      // recording can still read the PR/issue context files. This must NOT fire
      // for pipeline-start — that stage is completed exactly once by the
      // non-finish branch after cleanup (line ~10471). Firing it here as well
      // was the source of the duplicate pipeline-start completedStages entry
      // (two StageResults, same startedAt, different durations) (#230).
      if (stage === "pipeline-finish" && this.stateService) {
        await this.stateService.completeStage(stage);
      }

      // Clean up context files after completeStage (which records outcome internally)
      if (stage === "pipeline-finish") {
        const workspaceRoot = this.getWorkingDirectory();
        const cleanupScript = path.join(
          workspaceRoot,
          "claude-plugins/nightgauge/hooks/lib/cleanup-context-files.sh"
        );
        if (fs.existsSync(cleanupScript)) {
          try {
            await execFileAsync("bash", [cleanupScript, String(issueNumber)], {
              encoding: "utf-8",
              cwd: workspaceRoot,
              timeout: 45000,
            });
            this.logger.info("Context files cleaned up", { issueNumber });
          } catch (err) {
            this.logger.warn("Context file cleanup failed", {
              issueNumber,
              err: err instanceof Error ? err.message : String(err),
            });
          }
        }
      }

      // Mark stage as complete (pipeline-finish already completed above before cleanup)
      if (stage !== "pipeline-finish" && this.stateService) {
        await this.stateService.completeStage(stage);
      }

      const durationMs = Date.now() - startTime;
      const result: StageRunResult = {
        success: true,
        stage,
        durationMs,
      };

      this.eventDispatcher.onStageComplete(stage, result);
      return result;
    } catch (err) {
      const error = err instanceof Error ? err : new Error(String(err));
      this.logger.error("Bookend stage failed", {
        stage,
        error: error.message || String(error),
      });

      // Mark stage as failed
      if (this.stateService) {
        try {
          await this.stateService.failStage(stage, error.message);
        } catch (stateErr) {
          this.logger.warn("Failed to update state on bookend stage failure", {
            stage,
            stateErr,
          });
        }
      }

      this.eventDispatcher.onStageError(stage, error);

      return {
        success: false,
        stage,
        durationMs: Date.now() - startTime,
        error,
      };
    }
  }

  /**
   * Run a single skill-based stage
   *
   * For bookend stages (pipeline-start, pipeline-finish), use runBookendStage instead.
   *
   * @param stage - The stage to run
   * @param issueNumber - The GitHub issue number
   * @param callbacks - Callbacks for stage events
   * @param skipToPhase - Optional phase name to skip to (Issue #1187)
   * @returns Result of the stage run
   */
  async runStage(
    stage: PipelineStage,
    issueNumber: number,
    callbacks?: PipelineCallbacks,
    skipToPhase?: string,
    modelOverride?: PipelineModelOverride,
    pinnedWorkspaceRoot?: string,
    modelOverrideSource?: import("../utils/skillRunner").ModelSource
  ): Promise<StageRunResult> {
    // Create event dispatcher for this standalone stage run (Issue #2770 — Part 3)
    this.eventDispatcher = new OrchestratorEventDispatcher(callbacks, this.logger);

    // Handle bookend stages separately (synchronous, zero tokens)
    if (isBookendStage(stage)) {
      return this.runBookendStage(stage, issueNumber);
    }

    // Load issue metadata to determine base size label for budget enforcement.
    // Skip for issue-pickup: context file doesn't exist until that stage completes.
    // @see Issue #732 — AutoModelSelector metadata loading
    const issueMetadataForSize =
      stage === "issue-pickup" ? undefined : (this.loadIssueMetadata(issueNumber) ?? undefined);
    const issueSizeLabel: SizeLabel = (issueMetadataForSize?.size as SizeLabel) ?? "M";

    // For stages not yet extracted to dedicated runners (feature-validate, pr-create,
    // pr-merge), pre-compute the planning-hint budget adjustment so DefaultStageRunner
    // gets the correct effective size via the executeSkill fallback.
    // feature-dev's planning adjustment is handled by FeatureDevStageRunner instead.
    // @see Issue #1333 - Planning-aware budget enforcement
    // @see Issue #2768 — POST_PLANNING_STAGES regression: preserves zero-behavior-change
    const POST_PLANNING_NON_DEV_STAGES: ReadonlySet<string> = new Set([
      "feature-validate",
      "pr-create",
      "pr-merge",
    ]);
    let effectiveSizeLabel: SizeLabel = issueSizeLabel;
    if (POST_PLANNING_NON_DEV_STAGES.has(stage)) {
      const planningHint = this.readPlanningBudgetHint(issueNumber);
      if (planningHint) {
        effectiveSizeLabel = resolveEffectiveSize(issueSizeLabel, planningHint);
        if (effectiveSizeLabel !== issueSizeLabel) {
          this.logger.info("Budget size adjusted from planning context", {
            stage,
            issueNumber,
            issueSizeLabel,
            effectiveSizeLabel,
            assessedSize: planningHint.assessedSize,
            totalFileCount: planningHint.totalFileCount,
          });
        }
        this.stateService?.setMeta({
          complexity: planningHint.assessedSize ?? issueSizeLabel,
          file_count: planningHint.totalFileCount,
        });
      }
    }

    // Delegate to stage runner — runners handle stage-specific concerns:
    // prerequisite validation (feature-planning, feature-dev) and budget size
    // adjustment (feature-dev reads planning hints via FeatureDevStageRunner).
    // Stage-agnostic orchestration (token tracking, error recovery, model
    // escalation) runs inside executeSkill.
    // @see Issue #2768 — HeadlessOrchestrator decomposition Part 1
    const runner = StageRunnerRegistry.getRunner(stage);
    const ctx: StageRunContext = {
      stage,
      issueNumber,
      workspaceRoot: pinnedWorkspaceRoot ?? this.getWorkingDirectory(),
      issueSizeLabel,
      stateService: this.stateService ?? undefined,
      logger: this.logger,
      executeSkill: (options) =>
        this._runSkillStageCore(
          stage,
          issueNumber,
          callbacks,
          skipToPhase,
          modelOverride,
          pinnedWorkspaceRoot,
          modelOverrideSource,
          options?.sizeLabel ?? effectiveSizeLabel
        ),
      getContextPath: (type, num) => this.getContextPath(type, num),
    };
    return runner.run(ctx);
  }

  /**
   * Core skill stage execution — the machinery of a single stage run.
   *
   * Contains the full orchestration loop (state transition validation, budget
   * enforcement, token tracking, skill execution, output validation, error
   * recovery, stage completion) previously inline in runStage().
   *
   * The sizeLabel parameter is provided by the calling runner via
   * StageRunContext.executeSkill(), allowing FeatureDevStageRunner (and future
   * runners) to inject planning-context budget adjustments without coupling
   * this core loop to stage-specific logic.
   *
   * @param sizeLabel - Effective budget size. Runners may have adjusted this
   *   from the issue's default size label based on planning context.
   * @see Issue #2768 — HeadlessOrchestrator decomposition Part 1
   * @see Issue #1333 - Planning-aware budget enforcement
   */
  private async _runSkillStageCore(
    stage: PipelineStage,
    issueNumber: number,
    callbacks?: PipelineCallbacks,
    skipToPhase?: string,
    modelOverride?: PipelineModelOverride,
    pinnedWorkspaceRoot?: string,
    modelOverrideSource?: import("../utils/skillRunner").ModelSource,
    sizeLabel: SizeLabel = "M"
  ): Promise<StageRunResult> {
    this.currentStage = stage;
    const startTime = Date.now();

    // Resolve model for observability logging (Issue #2340).
    // The actual --model flag is set inside runStageSkillHeadless via the same
    // resolveModel() call, so this is read-only — no behavioral change.
    const effectiveWorkspace = pinnedWorkspaceRoot ?? this.getWorkingDirectory();
    const modelPreview = modelOverride
      ? { model: modelOverride, source: modelOverrideSource ?? "override" }
      : resolveModel(stage, effectiveWorkspace, undefined, issueNumber);
    this.logger.info("Starting stage", {
      stage,
      issueNumber,
      model: modelPreview.model,
      modelSource: modelPreview.source,
      effort: "effort" in modelPreview ? modelPreview.effort : undefined,
    });

    // VALIDATE BEFORE RUNNING
    if (this.stateService) {
      const validation = await this.stateService.validateStageTransition(stage, issueNumber);

      if (!validation.allowed) {
        if (validation.requiresConfirmation) {
          // Ask for confirmation via callback
          const confirmed = await this.eventDispatcher.onBackwardTransitionConfirm(
            stage,
            validation.confirmationMessage || "Proceed with backward transition?"
          );

          if (!confirmed) {
            this.logger.info("Backward transition cancelled by user", {
              stage,
            });
            return {
              success: false,
              stage,
              durationMs: Date.now() - startTime,
              error: new Error("Backward transition cancelled by user"),
            };
          }
          // User confirmed - proceed with forceBackward flag
          this.logger.info("User confirmed backward transition", { stage });
        } else {
          // Hard block (retry limit, wrong issue, etc.)
          this.logger.error("Stage transition blocked", {
            stage,
            reason: validation.error,
          });
          return {
            success: false,
            stage,
            durationMs: Date.now() - startTime,
            error: new Error(validation.error || "Stage transition blocked"),
          };
        }
      }
    }

    this.eventDispatcher.onStageStart(stage);

    // Update state service (with forceBackward if user confirmed)
    if (this.stateService) {
      try {
        await this.stateService.startStage(stage, { forceBackward: true });
        // Mark stage as headless execution for token tracking (Issue #498)
        // Headless mode uses stream-json output which enables token parsing
        await this.stateService.setStageExecutionMode(stage, "headless");
      } catch (err) {
        this.logger.warn("Failed to update state on stage start", {
          stage,
          err,
        });
      }
    }

    // Note: isRunning is managed by runPipeline(), not here.
    // Setting it here caused a race condition where isRunning was false
    // between stages, making the stop button report "no pipeline running."
    // @see Issue #527

    // Load issue metadata for AutoModelSelector (Issue #732)
    // Skip for issue-pickup: context file doesn't exist until that stage completes.
    const issueMetadata =
      stage === "issue-pickup" ? undefined : (this.loadIssueMetadata(issueNumber) ?? undefined);

    // Budget enforcement (Issue #835, #1333)
    // sizeLabel is passed in from the runner. FeatureDevStageRunner adjusts it
    // from planning hints; other stages use issueSizeLabel directly.
    // @see Issue #2768 — POST_PLANNING_STAGES logic moved to FeatureDevStageRunner
    const budgetConfig = getBudgetEnforcementConfig();

    const outputTokenOverrides = getOutputTokenLimitOverrides();

    // Adaptive budget overrides: compute p75 from historical exit records for
    // this (repo, stage, size_label) group and pass as stageOverrides so
    // BudgetEnforcer uses per-repo calibrated limits instead of static defaults.
    // Non-fatal: any failure returns empty overrides and logs a warning.
    // @see Issue #3667 — Adaptive per-repo stage budgets
    const adaptiveBudgetEnabled = isAdaptiveBudgetEnabled(this.getWorkingDirectory());
    const staticBudgetsForStages = Object.fromEntries(
      (Object.entries(DEFAULT_SIZE_AWARE_BUDGETS) as [string, SizeAwareBudget][]).map(([s, v]) => [
        s,
        v[sizeLabel] ?? v.M,
      ])
    );
    const adaptiveResult = await loadAdaptiveBudgetOverrides({
      workspaceRoot: this.getWorkingDirectory(),
      repo: this.repoOverride ?? "",
      sizeLabel,
      staticBudgets: staticBudgetsForStages,
      enabled: adaptiveBudgetEnabled,
    });
    for (const line of adaptiveResult.logLines) {
      this.logger.info(line);
    }
    // Merge adaptive overrides into config overrides (config overrides win when both set).
    const mergedStageOverrides: Record<
      string,
      number | Partial<import("../utils/budgetEnforcer").SizeAwareBudget>
    > = { ...adaptiveResult.stageOverrides, ...(budgetConfig.stageOverrides ?? {}) };
    const adaptiveEstimateSources: Record<string, EstimateSource> = adaptiveResult.estimateSources;

    const budgetEnforcer = new BudgetEnforcer({
      mode: budgetConfig.mode,
      gracePercent: budgetConfig.gracePercent,
      windDownPercent: budgetConfig.windDownPercent,
      stageOverrides:
        Object.keys(mergedStageOverrides).length > 0
          ? mergedStageOverrides
          : budgetConfig.stageOverrides,
      outputTokenOverrides,
    });

    // Resolve once per stage — model/effort the runner is about to pick. Used
    // on every checkBudget call so the BudgetEnforcer's hard-mode terminate
    // path scales with the same multiplier table that scales the cost-cap
    // kill. Without this, MAXIMUM (Opus high-effort) pipelines get
    // terminated by the BudgetEnforcer at Sonnet-calibrated limits while the
    // cost-cap path (which DOES scale) lets work through — a confusing
    // asymmetry the 2026-05-04 incidents made very visible (#331 pr-create
    // killed at $5.74 vs $4.50, #871 feature-dev killed at $23 vs $15).
    const budgetModelInfo = this.resolveBudgetModelInfo(stage);

    // Log the effective limit for the resolved model when it diverges from
    // the configured base — without this it's impossible to tell from the
    // logs whether a kill at $X was "configured limit" or "scaled limit".
    if (budgetModelInfo.model) {
      const effective = budgetEnforcer.getEffectiveLimit(stage, sizeLabel, budgetModelInfo);
      const baseEffective = budgetEnforcer.getEffectiveLimit(stage, sizeLabel);
      if (effective > 0 && Math.abs(effective - baseEffective) > 0.01) {
        this.logger.info("Budget enforcement scaled for model/effort", {
          stage,
          sizeLabel,
          model: budgetModelInfo.model,
          effort: budgetModelInfo.effort,
          baseEffectiveUsd: +baseEffective.toFixed(2),
          scaledEffectiveUsd: +effective.toFixed(2),
        });
      }
    }
    // Warn threshold for in-flight cost toast (Issue #3508).
    // Set inside the history block below; 0 = disabled (no history or warn off).
    let stageWarnThresholdUsd = 0;

    // Cost-cap tightness warning (Issue #3276)
    // Warn once at stage-start when effectiveCap is below historical-median × multiplier.
    // Uses execution history to compute per-stage median asynchronously; errors are non-fatal.
    try {
      const wsRoot = this.getWorkingDirectory();
      const historyRecords = await ExecutionHistoryReader.readAll(wsRoot);
      const stageCosts: number[] = [];
      for (const record of historyRecords) {
        const cost =
          "tokens" in record
            ? ((
                record as ExecutionHistoryRecord & {
                  tokens: { per_stage?: Record<string, { cost_usd?: number }> };
                }
              ).tokens?.per_stage?.[stage]?.cost_usd ?? 0)
            : 0;
        if (cost > 0) stageCosts.push(cost);
      }
      if (stageCosts.length > 0) {
        const sorted = [...stageCosts].sort((a, b) => a - b);
        const mid = Math.floor(sorted.length / 2);
        const median = sorted.length % 2 === 0 ? (sorted[mid - 1] + sorted[mid]) / 2 : sorted[mid];
        const warningMultiplier = getCostCapWarningMultiplier(wsRoot);
        const tightnessDecision = checkCostCapTightness(
          stage,
          budgetEnforcer.getEffectiveLimit(stage, sizeLabel),
          median,
          warningMultiplier,
          stageCosts.length
        );
        if (tightnessDecision.shouldWarn) {
          this.logger.warn(tightnessDecision.message, { stage });
        }
        // Issue #3508: Compute warn threshold for in-flight toast.
        // historicalMedian × warnMultiplier — passed to skillRunner so the
        // warn closure fires without needing to re-read history mid-execution.
        const warnMultiplier = getStageCostWarnMultiplier(stage, wsRoot);
        stageWarnThresholdUsd = warnMultiplier > 0 ? median * warnMultiplier : 0;
      }
    } catch {
      // Non-fatal: cost-cap tightness warning is advisory only
    }

    let budgetWarningEmitted = false;
    let budgetTerminated = false;
    let budgetTerminatedEffectiveLimit = 0;
    let budgetTerminatedCost = 0;
    let windDownSignalWritten = false;
    let outputTokenWarningEmitted = false;
    let compactionDetected = false;

    // Burn rate projector for early ceiling warnings (Issue #1935)
    const ceilingConfigForProjector = getPipelineCeilingConfig();
    const projectorCeiling =
      ceilingConfigForProjector.overrideCeilingUsd ?? ceilingConfigForProjector.ceilingUsd ?? 50;
    const burnRateProjector = new BurnRateProjector(projectorCeiling);
    let burnRateEarlyWarningEmitted = false;

    // Context budget enforcement (Issue #790 — per-stage input token budgets)
    const contextBudgetConfig = getContextBudgetConfig();
    const contextBudgetEnforcer = new ContextBudgetEnforcer({
      enabled: contextBudgetConfig.enabled,
      mode: contextBudgetConfig.mode,
      gracePercent: contextBudgetConfig.gracePercent,
      stageOverrides: contextBudgetConfig.stageOverrides,
    });
    let contextBudgetWarningEmitted = false;

    // Pipeline budget ceiling — mid-stage enforcement (Issue #1047)
    // Separate instance from the between-stages check in runPipeline() because
    // runStage() has its own scope. Warning/checkpoint state is per-stage-run.
    // #253: thread the per-run override through — a fresh per-stage instance
    // silently discarding a confirmed "Increase Ceiling & Continue" is what
    // stopped run #236 one second after the user chose to continue.
    const stageCeilingConfig = getPipelineCeilingConfig();
    const stagePipelineCeiling = new PipelineBudgetCeiling({
      ...stageCeilingConfig,
      ...(this.ceilingOverrideUsd !== null ? { overrideCeilingUsd: this.ceilingOverrideUsd } : {}),
    });
    let stageCeilingWarningEmitted = false;
    let stageCeilingCheckpointEmitted = false;
    // Issue #3542: set true once onComplete has the stage's result in hand.
    // The mid-stage ceiling hard-stop must NOT fire after the stage already
    // finished — the #3365 incident killed an already-complete feature-dev
    // run because a budget-ceiling poll tick landed 40s after completion.
    let stageResultResolved = false;

    // Budget escalation cap (#3108): after this many user "Continue" choices
    // for budget overruns in this stage, stop re-prompting and just save +
    // stop. Each escalation typically doubles the limit, so two are enough to
    // confirm intent — three or more is the unbounded burn pattern that took
    // pr-merge to $63 in #287.
    const MAX_BUDGET_ESCALATIONS_PER_STAGE = 2;
    let stageBudgetEscalationCount = 0;
    let stageCeilingEscalationCount = 0;

    // Productive-progress gate for unattended escalation (Issue #3851).
    // #3811's feature-dev burned $112 because the unattended auto-escalation
    // doubled the budget/ceiling UNCONDITIONALLY (gated only on the escalation
    // count). The proximate fix: before auto-escalating, snapshot the stage's
    // productive-progress counter (commits / new-file writes / phase markers /
    // CI progress, via `handle.getProductiveProgressDelta()`). If progress has
    // NOT advanced since the last escalation, the stage is churning — do not
    // escalate; auto-commit WIP and stop. If progress HAS advanced, the spend
    // is buying real work — escalate as before.
    //
    // -1 means "never escalated yet" (the first escalation is always allowed,
    // since there is no prior baseline to compare against — a single ceiling
    // hit on otherwise-healthy work should not be blocked).
    let productiveProgressAtLastBudgetEscalation = -1;
    let productiveProgressAtLastCeilingEscalation = -1;

    /**
     * Decide whether an unattended escalation should proceed (Issue #3851).
     * Reads the live productive-progress count off the stage handle and
     * delegates the policy to the pure `decideProgressGatedEscalation` helper
     * (unit-tested in isolation).
     */
    const shouldEscalateOnProgress = (
      lastSnapshot: number,
      handle: SkillProcessHandle | null
    ): { escalate: boolean; current: number; delta: number } =>
      decideProgressGatedEscalation(lastSnapshot, handle?.getProductiveProgressDelta?.() ?? 0);

    // Track previous cumulative totals from TokenAccumulator to compute deltas.
    // onTokenUsage receives cumulative running totals (tokenAccumulator.getTotal()),
    // but PipelineStateService.updateTokens() adds values incrementally.
    // Without delta conversion, each callback would re-add the full cumulative
    // total, inflating state by ~2× per extra callback. @see Issue #843
    let prevUsage = {
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0,
    };

    // Phase timeout event subscriptions (Issue #1187)
    // Subscribe to stale/timeout events to log warnings and optionally kill.
    // ptm may be null in test environments where vscode.EventEmitter is unavailable.
    const ptm = this.getPhaseTimeoutManager();
    const phaseTimeoutDisposables: vscode.Disposable[] = [];
    // Mutable ref so phase timeout handlers can kill the process.
    // Assigned after runStageSkillHeadless() returns the handle.
    let handleRef: { kill: () => void } | null = null;
    // Full handle ref for the shared budget/ceiling evaluator (#254). The
    // evaluator is defined before `const handle` is assigned, so it reaches the
    // handle (for kill() and productive-progress reads) through this mutable
    // binding, set the moment runStageSkillHeadless() returns — before any
    // streamed callback fires.
    let stageHandle: SkillProcessHandle | null = null;
    if (ptm) {
      phaseTimeoutDisposables.push(
        ptm.onPhaseStale((event: PhaseStaleEvent) => {
          this.logger.warn("Phase stale detected — no output activity", {
            stage: event.stage,
            phase: event.phaseName,
            phaseType: event.phaseType,
            inactivityMs: event.inactivityMs,
          });
          this.eventDispatcher.onStderr(
            stage,
            `[PhaseTimeoutManager] Phase "${event.phaseName}" stale — no output for ${Math.round(event.inactivityMs / 1000)}s\n`
          );
        })
      );
      phaseTimeoutDisposables.push(
        ptm.onPhaseTimeout((event: PhaseTimeoutEvent) => {
          this.logger.error("Phase hard timeout reached — killing process", {
            stage: event.stage,
            phase: event.phaseName,
            phaseType: event.phaseType,
            elapsedMs: event.elapsedMs,
          });
          this.eventDispatcher.onStderr(
            stage,
            `[PhaseTimeoutManager] Phase "${event.phaseName}" timed out after ${Math.round(event.elapsedMs / 1000)}s — terminating\n`
          );
          // Mark phase as failed in state
          if (this.stateService) {
            this.stateService
              .failPhase(
                stage,
                event.phaseName,
                `Hard timeout after ${Math.round(event.elapsedMs / 1000)}s`,
                0
              )
              .catch(() => {});
          }
          // Actually kill the hung process (Issue #1620)
          if (handleRef) {
            handleRef.kill();
          }
        })
      );
    }

    // Issue #3542: PRE-STAGE pipeline budget ceiling check. If the cumulative
    // pipeline cost already exceeds the ceiling BEFORE this stage starts, fail
    // fast with a clear message instead of running the stage and killing it
    // mid-stream (which risks the #3365 lost-work scenario). Only the hard
    // stop short-circuits here — warnings/checkpoints are handled mid-stage.
    if (this.stateService) {
      try {
        const preState = await this.stateService.getState();
        const preCost = preState?.tokens?.estimated_cost_usd ?? 0;
        const preCheck = stagePipelineCeiling.check(preCost);
        if (preCheck.shouldStop) {
          this.logger.error("Pipeline budget ceiling already exceeded before stage start", {
            stage,
            issueNumber,
            costUsd: preCheck.currentCostUsd,
            ceilingUsd: preCheck.effectiveCeilingUsd,
          });
          this.eventDispatcher.onStderr(stage, `${preCheck.message}\n`);
          const ceilingError = new Error(
            `${preCheck.message} — refusing to start ${stage} (raise pipeline.token_budget_ceiling.ceiling_usd to continue)`
          );
          if (this.stateService) {
            try {
              await this.stateService.failStage(stage, ceilingError.message);
            } catch (err) {
              this.logger.warn("Failed to update state on pre-stage ceiling block", { stage, err });
            }
          }
          this.eventDispatcher.onStageError(stage, ceilingError);
          const stageResult: StageRunResult = {
            success: false,
            stage,
            durationMs: 0,
            error: ceilingError,
          };
          this.eventDispatcher.onStageComplete(stage, stageResult);
          return stageResult;
        }
      } catch (err) {
        // Non-fatal: a pre-stage check failure must not block the stage.
        this.logger.warn("Pre-stage pipeline ceiling check failed — continuing", { stage, err });
      }
    }

    return new Promise((resolve) => {
      // ── First-output watchdog (#252 — zombie-run guard) ──────────────────
      // Covers the window every other detector is blind to: the skill-runner
      // preamble's unbounded awaits before spawn, and a session that spawns
      // but never streams a single event. Cost-gated detectors never activate
      // at $0, and cold-start disables the idle-kill, so without this a silent
      // stage hangs the slot forever (three 9-hour zombies on 2026-07-18).
      let stageProducedOutput = false;
      let zombieWatchdogFired = false;
      const noOutputTimer = setTimeout(() => {
        zombieWatchdogFired = true;
        const minutes = Math.round(STAGE_NO_OUTPUT_TIMEOUT_MS / 60000);
        const msg =
          `[stage-no-output-timeout] Stage ${stage} produced no output within ` +
          `${minutes} minutes of start — presumed wedged during startup ` +
          `(pre-spawn await or silent session). Failing the stage so the run ` +
          `can terminate and retry. (#252)`;
        this.logger.error(msg, { stage, issueNumber });
        this.eventDispatcher.onStderr(stage, `${msg}\n`);
        try {
          handleRef?.kill();
        } catch {
          // Process may never have spawned — nothing to kill.
        }
        // Fire-and-forget: terminal resolution must never depend on the IPC
        // round-trip that may itself be the wedged component.
        if (this.stateService) {
          void this.stateService.failStage(stage, msg).catch(() => {});
        }
        const err = new Error(msg);
        this.eventDispatcher.onStageError(stage, err);
        const stageResult: StageRunResult = {
          success: false,
          stage,
          durationMs: Date.now() - startTime,
          error: err,
        };
        this.eventDispatcher.onStageComplete(stage, stageResult);
        resolve(stageResult);
      }, STAGE_NO_OUTPUT_TIMEOUT_MS);
      const markStageOutput = () => {
        if (!stageProducedOutput) {
          stageProducedOutput = true;
          clearTimeout(noOutputTimer);
        }
      };

      // Issue #254: one evaluator, two cadences. The terminal `result`
      // envelope (opts.live === false) BOOKS authoritative cost AND enforces;
      // the live in-stage cost snapshot (opts.live === true, #233 estimator)
      // ENFORCES ONLY — it never books, so recorded totals stay authoritative
      // (no double-booking). Both feed the same BudgetEnforcer /
      // PipelineBudgetCeiling decision logic, so wind-down -> warn -> terminate
      // fire mid-stage, in order (Issue #254).
      const evaluateBudgetAndCeiling = async (
        usage: ParsedTokenUsage,
        opts: { live: boolean }
      ): Promise<void> => {
        markStageOutput();

        // Live snapshots (#254) ENFORCE ONLY — they must never book cost.
        // Only the authoritative terminal `result` envelope (opts.live ===
        // false) books deltas into PipelineStateService and emits the
        // cost.recorded audit + burn-rate samples, so recorded totals stay
        // authoritative and there is no double-booking. `prevUsage` is
        // likewise only advanced here, so the live ceiling delta below reads
        // "cost booked for this stage so far".
        if (!opts.live) {
          // Convert cumulative totals to deltas for PipelineStateService.
          // TokenAccumulator.getTotal() returns running totals, but
          // updateTokens() is additive — passing cumulative values would
          // inflate the state on every subsequent callback. @see Issue #843
          const delta = {
            inputTokens: usage.inputTokens - prevUsage.inputTokens,
            outputTokens: usage.outputTokens - prevUsage.outputTokens,
            cacheReadTokens: usage.cacheReadTokens - prevUsage.cacheReadTokens,
            cacheCreationTokens: usage.cacheCreationTokens - prevUsage.cacheCreationTokens,
            costUsd: usage.costUsd - prevUsage.costUsd,
          };
          prevUsage = {
            inputTokens: usage.inputTokens,
            outputTokens: usage.outputTokens,
            cacheReadTokens: usage.cacheReadTokens,
            cacheCreationTokens: usage.cacheCreationTokens,
            costUsd: usage.costUsd,
          };

          // Update PipelineStateService with token usage delta (Issue #404)
          // This fires the unified onTokenUsageUpdated event for UI components
          if (this.stateService) {
            try {
              await this.stateService.updateTokens({
                inputTokens: delta.inputTokens,
                outputTokens: delta.outputTokens,
                cacheReadTokens: delta.cacheReadTokens,
                cacheCreationTokens: delta.cacheCreationTokens,
                costUsd: delta.costUsd,
                // Issue #3228: forward the resolution-step label so per-stage
                // history records can attribute billed cost vs. computed cost.
                // The label rides every callback unchanged; it is not a counter.
                costSource: usage.costSource,
                stage,
              });
            } catch (err) {
              this.logger.warn("Failed to update token usage", {
                stage,
                err,
              });
            }
          }

          // Emit cost.recorded audit event for token usage (Issue #1582)
          // Uses cumulative totals (not deltas) so the platform can compute
          // per-stage cost from consecutive events if needed.
          if (this.auditClient && this.currentPipelineRunId) {
            this.auditClient.enqueue({
              action: "cost.recorded",
              resourceType: "stage",
              resourceId: stage,
              metadata: {
                pipelineRunId: this.currentPipelineRunId,
                stage,
                inputTokens: delta.inputTokens,
                outputTokens: delta.outputTokens,
                cacheReadTokens: delta.cacheReadTokens,
                cacheCreationTokens: delta.cacheCreationTokens,
                costUsd: delta.costUsd,
                timestamp: new Date().toISOString(),
              },
            });
          }

          // Burn rate sampling for early ceiling projection (Issue #1935)
          burnRateProjector.recordSample(usage.costUsd);

          // Burn rate early warning — project ceiling hit before it happens
          if (!burnRateEarlyWarningEmitted && !budgetTerminated && this.stateService) {
            try {
              const pipelineState = await this.stateService.getState();
              const totalPipelineCost = pipelineState?.tokens?.estimated_cost_usd ?? usage.costUsd;
              const projection = burnRateProjector.getProjection(totalPipelineCost);
              if (projection?.shouldWarnEarly) {
                burnRateEarlyWarningEmitted = true;
                this.logger.warn("Burn rate projection: ceiling approaching", {
                  stage,
                  issueNumber,
                  burnRatePerMinute: projection.burnRatePerMinute,
                  projectedMinutesRemaining: projection.projectedMinutesRemaining,
                  totalPipelineCost,
                });
                this.eventDispatcher.onStderr(stage, `[BURN RATE WARNING] ${projection.message}\n`);
                vscode.window.showWarningMessage(`#${issueNumber}: ${projection.message}`);
              }
            } catch {
              // Non-critical — burn rate projection failure should not break pipeline
            }
          }
        } // end `if (!opts.live)` booking block (#254)

        // Budget enforcement (Issue #835 — hard budget limits, mode-aware
        // scaling so MAXIMUM-mode runs aren't killed by Sonnet-calibrated
        // limits per the 2026-05-04 incident retro). Runs on BOTH the live
        // snapshot and the terminal envelope (#254) — `usage.costUsd` is the
        // live estimate mid-stage and the authoritative total at stage end.
        const decision = budgetEnforcer.checkBudget(
          stage,
          usage.costUsd,
          sizeLabel,
          budgetModelInfo
        );

        // Which phase side-effects are newly triggered (fire once each, in
        // wind-down → warn → terminate order). Shared latch logic (#254) so
        // the live and terminal paths can never double-fire: a threshold the
        // live estimate already crossed is a no-op when the authoritative
        // terminal cost re-evaluates it.
        const budgetActions = nextBudgetActions(decision, {
          windDownFired: windDownSignalWritten,
          warnFired: budgetWarningEmitted,
          terminated: budgetTerminated,
        });

        // Wind-down signal — tell agent to commit and exit cleanly (Issue #2338)
        if (budgetActions.fireWindDown) {
          windDownSignalWritten = true;
          this.logger.info(decision.message, {
            stage,
            costUsd: usage.costUsd,
            effectiveLimit: decision.effectiveLimit,
          });
          this.eventDispatcher.onStderr(stage, `${decision.message}\n`);
          try {
            const signalDir = path.join(this.getWorkingDirectory(), ".nightgauge", "pipeline");
            const signalPath = path.join(signalDir, `winddown-signal-${issueNumber}.json`);
            const signalContent = JSON.stringify(
              {
                reason: "stage_budget_winddown",
                stage,
                budget_usd: decision.effectiveLimit,
                current_cost_usd: decision.currentCost,
                remaining_budget_pct: Math.max(
                  0,
                  Math.round(
                    ((decision.effectiveLimit - decision.currentCost) / decision.effectiveLimit) *
                      100
                  )
                ),
                message:
                  "Approaching stage budget limit. Please commit current work, push, and exit cleanly.",
                timestamp: new Date().toISOString(),
              },
              null,
              2
            );
            fs.writeFileSync(signalPath, signalContent);
          } catch {
            // Non-critical — the agent can still be killed normally
          }
        }

        if (budgetActions.fireWarn) {
          budgetWarningEmitted = true;
          this.logger.warn(decision.message, {
            stage,
            costUsd: usage.costUsd,
            effectiveLimit: decision.effectiveLimit,
            budgetMode: decision.budgetMode,
          });
          this.eventDispatcher.onStderr(stage, `${decision.message}\n`);
        }

        if (budgetActions.fireTerminate) {
          budgetTerminated = true;
          budgetTerminatedEffectiveLimit = decision.effectiveLimit;
          budgetTerminatedCost = usage.costUsd;
          this.logger.error(decision.message, {
            stage,
            costUsd: usage.costUsd,
            effectiveLimit: decision.effectiveLimit,
          });
          this.eventDispatcher.onStderr(stage, `${decision.message}\n`);

          // Budget-pause with retro: gather diagnostics before prompting (Issue #1935)
          // The process keeps running while we wait for user input —
          // a few more seconds of cost is negligible vs losing all work.
          let retroRecommendation = "";
          try {
            const perStageCosts: Record<string, number> = {};
            if (this.stateService) {
              const state = await this.stateService.getState();
              if (state?.tokens?.per_stage) {
                for (const [s, data] of Object.entries(state.tokens.per_stage)) {
                  if (data && typeof data === "object" && "cost_usd" in data) {
                    perStageCosts[s] = (data as { cost_usd: number }).cost_usd;
                  }
                }
              }
            }
            // Stage-budget terminations report STAGE cost, not pipeline total.
            // Earlier #2777 used estimated_cost_usd as a fallback when the
            // Claude CLI hadn't streamed total_cost_usd yet, but that field
            // is pipeline-wide and corrupted the failure report,
            // budget-overrun-{N}.json, and calibration signal. @see #3120
            budgetTerminatedCost = resolveStageCostUsd(usage.costUsd, perStageCosts, stage);
            const retro = await buildBudgetRetro({
              budgetType: "stage-cost",
              currentCost: usage.costUsd,
              effectiveLimit: decision.effectiveLimit,
              stage,
              issueNumber,
              stageStartTime: startTime,
              compactionDetected,
              sizeLabel: sizeLabel ?? "M",
              workspaceRoot: this.getWorkingDirectory(),
              perStageCosts,
            });
            const retroSummary = retro.diagnosticSummary;
            retroRecommendation = retro.recommendation;
            this.eventDispatcher.onStderr(
              stage,
              `[BUDGET RETRO]\n${retroSummary}\nRecommendation: ${retroRecommendation}\n`
            );
          } catch {
            // Non-critical — show the prompt without retro if it fails
          }

          // Cap interactive re-prompts at MAX_BUDGET_ESCALATIONS_PER_STAGE
          // (#3108). After two confirmed escalations, the next overrun
          // skips the prompt and stops cleanly — silently re-burning the
          // ceiling is the failure mode that caused #287's $63 spend.
          const escalationCapHit = stageBudgetEscalationCount >= MAX_BUDGET_ESCALATIONS_PER_STAGE;

          if (escalationCapHit) {
            this.logger.warn(
              "Stage budget escalation cap reached — stopping without re-prompting",
              {
                stage,
                issueNumber,
                escalations: stageBudgetEscalationCount,
                cap: MAX_BUDGET_ESCALATIONS_PER_STAGE,
              }
            );
            this.eventDispatcher.onStderr(
              stage,
              `[BUDGET ESCALATION CAP] ${stageBudgetEscalationCount} escalations already accepted ` +
                `for ${stage} — refusing to re-prompt for a third. Saving work and stopping.\n`
            );
            vscode.window.showWarningMessage(
              `Stage ${stage} for #${issueNumber} hit the ${MAX_BUDGET_ESCALATIONS_PER_STAGE}-escalation cap. ` +
                `Saving work and stopping. Adjust pr.auto_fix_max_attempts or stage budgets, then re-run.`
            );
            await this.autoCommitWorktreeWIP(issueNumber, stage);
            stageHandle?.kill();
            return;
          }

          // Unattended (autonomous) runs have no human at the modal, so a
          // blocking showWarningMessage would stall the whole concurrent
          // slot waiting for a click that never comes — the source of the
          // "it kept prompting me to increase the budget" friction. Auto-
          // escalate (the choice the operator was clicking anyway) up to
          // the same MAX_BUDGET_ESCALATIONS_PER_STAGE cap, surfacing each
          // step in the log + a non-blocking toast. The cap + hard stop
          // still bound runaway spend; only the *prompt* is removed.
          let userChoice: string | undefined;
          if (this.unattended) {
            // Issue #3851: gate the unattended auto-escalation on PRODUCTIVE
            // progress since the last escalation. Flat progress = churn →
            // do not escalate; save work and stop. This is the proximate fix
            // for the #3811 $112 burn (escalation was unconditional).
            const progressGate = shouldEscalateOnProgress(
              productiveProgressAtLastBudgetEscalation,
              stageHandle
            );
            if (progressGate.escalate) {
              userChoice = "Increase Budget & Continue";
              productiveProgressAtLastBudgetEscalation = progressGate.current;
              this.logger.info(
                "Unattended budget escalation — auto-increasing (productive progress confirmed)",
                {
                  stage,
                  issueNumber,
                  costUsd: usage.costUsd,
                  effectiveLimit: decision.effectiveLimit,
                  escalation: stageBudgetEscalationCount + 1,
                  cap: MAX_BUDGET_ESCALATIONS_PER_STAGE,
                  productiveSignals: progressGate.current,
                  productiveDelta: progressGate.delta,
                }
              );
              vscode.window.showWarningMessage(
                `Autonomous: stage ${stage} for #${issueNumber} exceeded budget ` +
                  `($${usage.costUsd.toFixed(2)} / $${decision.effectiveLimit.toFixed(2)}) — ` +
                  `auto-escalated (${stageBudgetEscalationCount + 1}/${MAX_BUDGET_ESCALATIONS_PER_STAGE}). ` +
                  `It will stop and save work if the cap is reached.`
              );
            } else {
              // Flat productive progress since the last escalation — churn.
              // Stop instead of doubling the budget again (Issue #3851).
              userChoice = "Save Work & Stop";
              this.logger.warn(
                "Unattended budget escalation REFUSED — no productive progress since last escalation (churn)",
                {
                  stage,
                  issueNumber,
                  costUsd: usage.costUsd,
                  effectiveLimit: decision.effectiveLimit,
                  productiveSignals: progressGate.current,
                  productiveDelta: progressGate.delta,
                }
              );
              this.eventDispatcher.onStderr(
                stage,
                `[BUDGET ESCALATION REFUSED] Stage ${stage} for #${issueNumber} exceeded budget ` +
                  `($${usage.costUsd.toFixed(2)} / $${decision.effectiveLimit.toFixed(2)}) but made NO ` +
                  `productive progress (commits / new files / phases) since the last escalation. ` +
                  `Churn detected — saving work and stopping instead of escalating. (Issue #3851)\n`
              );
              vscode.window.showWarningMessage(
                `Autonomous: stage ${stage} for #${issueNumber} stopped — budget exceeded with no ` +
                  `productive progress (churn). Work saved. (Issue #3851)`
              );
            }
          } else {
            userChoice = await vscode.window.showWarningMessage(
              `Stage ${stage} for #${issueNumber} exceeded budget ` +
                `($${usage.costUsd.toFixed(2)} / $${decision.effectiveLimit.toFixed(2)}). ` +
                `Escalation ${stageBudgetEscalationCount + 1}/${MAX_BUDGET_ESCALATIONS_PER_STAGE}. ` +
                (retroRecommendation || "Work will be lost if stopped without saving."),
              { modal: false },
              "Increase Budget & Continue",
              "Save Work & Stop"
            );
          }

          if (userChoice === "Increase Budget & Continue") {
            stageBudgetEscalationCount += 1;
            // Double the effective limit and let the stage continue
            budgetTerminated = false;
            budgetTerminatedEffectiveLimit = 0;
            budgetTerminatedCost = 0;
            budgetWarningEmitted = false;
            // Apply a 2x multiplier on top of current effective limit
            const newLimit = decision.effectiveLimit * 2;
            budgetEnforcer.applyRuntimeOverride(stage, newLimit);
            this.logger.info("User chose to increase budget and continue", {
              stage,
              issueNumber,
              previousLimit: decision.effectiveLimit,
              newLimit,
              escalation: stageBudgetEscalationCount,
              cap: MAX_BUDGET_ESCALATIONS_PER_STAGE,
            });
            this.eventDispatcher.onStderr(
              stage,
              `[BUDGET INCREASED] New limit: $${newLimit.toFixed(2)}. ` +
                `Escalation ${stageBudgetEscalationCount}/${MAX_BUDGET_ESCALATIONS_PER_STAGE}. Stage continuing.\n`
            );
          } else {
            // Save work & stop (or notification was dismissed)
            this.logger.info("User chose to save work and stop (or dismissed)", {
              stage,
              issueNumber,
            });
            // Auto-commit WIP before killing (Issue #1935)
            await this.autoCommitWorktreeWIP(issueNumber, stage);
            stageHandle?.kill();
          }
        }

        // Output token enforcement (Issue #842 — cap feature-dev output tokens)
        const outputDecision = budgetEnforcer.checkOutputTokens(
          stage,
          usage.outputTokens,
          sizeLabel
        );

        if (outputDecision.shouldWarn && !outputTokenWarningEmitted) {
          outputTokenWarningEmitted = true;
          this.logger.warn(outputDecision.message, {
            stage,
            outputTokens: usage.outputTokens,
            effectiveLimit: outputDecision.effectiveLimit,
          });
          this.eventDispatcher.onStderr(stage, `${outputDecision.message}\n`);
        }

        // Output token limits are warn-only (Issue #1609).
        // The post-hoc hard kill was removed because it destroyed completed
        // work without preventing cost overruns. Cost budget enforcement
        // (`checkBudget()`) remains as the safety net.

        // Context budget enforcement (Issue #790 — per-stage input token budgets)
        const contextDecision = contextBudgetEnforcer.checkInputTokens(
          stage,
          usage.inputTokens,
          sizeLabel
        );

        if (contextDecision.shouldWarn && !contextBudgetWarningEmitted) {
          contextBudgetWarningEmitted = true;
          this.logger.warn(contextDecision.message, {
            stage,
            inputTokens: usage.inputTokens,
            effectiveLimit: contextDecision.effectiveLimit,
            budgetMode: contextDecision.budgetMode,
          });
          this.eventDispatcher.onStderr(stage, `${contextDecision.message}\n`);
        }

        if (contextDecision.shouldTerminate && !budgetTerminated) {
          budgetTerminated = true;
          this.logger.error(contextDecision.message, {
            stage,
            inputTokens: usage.inputTokens,
            effectiveLimit: contextDecision.effectiveLimit,
          });
          this.eventDispatcher.onStderr(stage, `${contextDecision.message}\n`);

          // Context budget-pause with retro (Issue #1935)
          let ctxRetroMsg = "";
          try {
            const retro = await buildBudgetRetro({
              budgetType: "context-tokens",
              currentCost: usage.costUsd,
              effectiveLimit: contextDecision.effectiveLimit,
              stage,
              issueNumber,
              stageStartTime: startTime,
              compactionDetected,
              sizeLabel: sizeLabel ?? "M",
              workspaceRoot: this.getWorkingDirectory(),
            });
            ctxRetroMsg = retro.recommendation;
            this.eventDispatcher.onStderr(
              stage,
              `[CONTEXT BUDGET RETRO]\n${retro.diagnosticSummary}\nRecommendation: ${retro.recommendation}\n`
            );
          } catch {
            // Non-critical
          }

          const ctxChoice = await vscode.window.showWarningMessage(
            `Stage ${stage} for #${issueNumber} exceeded context budget ` +
              `(${usage.inputTokens.toLocaleString()} tokens). ` +
              (ctxRetroMsg || "Work will be lost if stopped without saving."),
            { modal: false },
            "Continue Anyway",
            "Save Work & Stop"
          );

          if (ctxChoice === "Continue Anyway") {
            budgetTerminated = false;
            this.logger.info("User chose to continue past context budget", {
              stage,
              issueNumber,
              inputTokens: usage.inputTokens,
            });
            this.eventDispatcher.onStderr(
              stage,
              `[CONTEXT BUDGET OVERRIDDEN] Stage continuing per user request.\n`
            );
          } else {
            await this.autoCommitWorktreeWIP(issueNumber, stage);
            stageHandle?.kill();
          }
        }

        // Pipeline budget ceiling — mid-stage check (Issue #1047)
        // Check cumulative pipeline cost for warning/checkpoint signals.
        // Hard stop is handled between stages; mid-stage we emit warnings
        // and write a checkpoint signal file for the agent to detect.
        if (this.stateService && !budgetTerminated) {
          try {
            const pipelineState = await this.stateService.getState();
            if (pipelineState) {
              // Live pipeline-cumulative cost (#254): prior stages' booked cost
              // plus THIS stage's not-yet-booked live estimate. Mid-stage
              // `prevUsage.costUsd` is 0, so the ceiling sees the live total; at
              // stage end the terminal envelope has already booked the stage, so
              // the extra term collapses to 0 and there is no double-count.
              const ceilingCheck = stagePipelineCeiling.check(
                livePipelineCostUsd(
                  pipelineState.tokens?.estimated_cost_usd ?? 0,
                  usage.costUsd,
                  prevUsage.costUsd
                )
              );

              if (ceilingCheck.shouldWarn && !stageCeilingWarningEmitted) {
                stageCeilingWarningEmitted = true;
                this.logger.warn(ceilingCheck.message, {
                  stage,
                  costUsd: ceilingCheck.currentCostUsd,
                  ceilingUsd: ceilingCheck.effectiveCeilingUsd,
                });
                this.eventDispatcher.onStderr(stage, `${ceilingCheck.message}\n`);
              }

              if (ceilingCheck.shouldCheckpoint && !stageCeilingCheckpointEmitted) {
                stageCeilingCheckpointEmitted = true;
                this.ceilingCheckpointWritten = true;
                this.logger.warn(ceilingCheck.message, {
                  stage,
                  costUsd: ceilingCheck.currentCostUsd,
                  ceilingUsd: ceilingCheck.effectiveCeilingUsd,
                });
                this.eventDispatcher.onStderr(stage, `${ceilingCheck.message}\n`);

                // Write checkpoint signal file for the agent to detect
                try {
                  const signalPath = path.join(
                    this.getWorkingDirectory(),
                    ".nightgauge",
                    "pipeline",
                    `checkpoint-signal-${issueNumber}.json`
                  );
                  const signalContent = JSON.stringify(
                    {
                      reason: "pipeline_budget_ceiling",
                      ceiling_usd: ceilingCheck.effectiveCeilingUsd,
                      current_cost_usd: ceilingCheck.currentCostUsd,
                      message:
                        "Approaching pipeline budget ceiling. Please commit current work and exit.",
                      timestamp: new Date().toISOString(),
                    },
                    null,
                    2
                  );
                  fs.writeFileSync(signalPath, signalContent);
                  this.logger.info("Checkpoint signal file written", {
                    signalPath,
                  });
                } catch (signalErr) {
                  this.logger.warn("Failed to write checkpoint signal file", {
                    err: signalErr,
                  });
                }
              }

              // Hard stop during stage — ask user with retro (Issue #1935)
              // Issue #3542: skip the kill once the stage has already
              // returned a result. A ceiling poll tick that lands after
              // onComplete must not override a finished stage — that is
              // exactly how #3365 lost a complete feature-dev run.
              if (ceilingCheck.shouldStop && !budgetTerminated && !stageResultResolved) {
                budgetTerminated = true;
                this.logger.error(ceilingCheck.message, {
                  stage,
                  costUsd: ceilingCheck.currentCostUsd,
                  ceilingUsd: ceilingCheck.effectiveCeilingUsd,
                });
                this.eventDispatcher.onStderr(stage, `${ceilingCheck.message}\n`);

                // Gather retro diagnostics for pipeline ceiling hit
                let ceilingRetroMsg = "";
                try {
                  const ceilingPerStageCosts: Record<string, number> = {};
                  if (pipelineState?.tokens?.per_stage) {
                    for (const [s, data] of Object.entries(pipelineState.tokens.per_stage)) {
                      if (data && typeof data === "object" && "cost_usd" in data) {
                        ceilingPerStageCosts[s] = (data as { cost_usd: number }).cost_usd;
                      }
                    }
                  }
                  const ceilingRetro = await buildBudgetRetro({
                    budgetType: "pipeline-ceiling",
                    currentCost: ceilingCheck.currentCostUsd,
                    effectiveLimit: ceilingCheck.effectiveCeilingUsd,
                    stage,
                    issueNumber,
                    stageStartTime: startTime,
                    compactionDetected,
                    sizeLabel: sizeLabel ?? "M",
                    workspaceRoot: this.getWorkingDirectory(),
                    perStageCosts: ceilingPerStageCosts,
                  });
                  ceilingRetroMsg = ceilingRetro.recommendation;
                  this.eventDispatcher.onStderr(
                    stage,
                    `[CEILING RETRO]\n${ceilingRetro.diagnosticSummary}\nRecommendation: ${ceilingRetro.recommendation}\n`
                  );
                } catch {
                  // Non-critical
                }

                // Cap pipeline-ceiling escalations per stage (#3108) for
                // the same reason as the per-stage cap above.
                const ceilingEscalationCapHit =
                  stageCeilingEscalationCount >= MAX_BUDGET_ESCALATIONS_PER_STAGE;

                if (ceilingEscalationCapHit) {
                  this.logger.warn(
                    "Pipeline ceiling escalation cap reached — stopping without re-prompting",
                    {
                      stage,
                      issueNumber,
                      escalations: stageCeilingEscalationCount,
                      cap: MAX_BUDGET_ESCALATIONS_PER_STAGE,
                    }
                  );
                  this.eventDispatcher.onStderr(
                    stage,
                    `[CEILING ESCALATION CAP] ${stageCeilingEscalationCount} ceiling escalations ` +
                      `already accepted in ${stage} — refusing to re-prompt. Saving work and stopping.\n`
                  );
                  vscode.window.showWarningMessage(
                    `Pipeline ceiling for #${issueNumber} hit the ${MAX_BUDGET_ESCALATIONS_PER_STAGE}-escalation cap. ` +
                      `Saving work and stopping. Raise pipeline.budget_ceiling_usd in config and re-run if needed.`
                  );
                  await this.autoCommitWorktreeWIP(issueNumber, stage);
                  stageHandle?.kill();
                  return;
                }

                // Same unattended carve-out as the per-stage budget prompt
                // above: an autonomous slot has no human to click the modal,
                // so auto-escalate up to the cap and surface it non-blocking.
                let ceilingChoice: string | undefined;
                if (this.unattended) {
                  // Issue #3851: gate the unattended ceiling auto-escalation
                  // on PRODUCTIVE progress since the last ceiling escalation
                  // — flat = churn → stop. This is the proximate fix for the
                  // #3811 $112 burn (the ceiling doubled $75→$150→$300
                  // unconditionally).
                  const progressGate = shouldEscalateOnProgress(
                    productiveProgressAtLastCeilingEscalation,
                    stageHandle
                  );
                  if (progressGate.escalate) {
                    ceilingChoice = "Increase Ceiling & Continue";
                    productiveProgressAtLastCeilingEscalation = progressGate.current;
                    this.logger.info(
                      "Unattended pipeline-ceiling escalation — auto-increasing (productive progress confirmed)",
                      {
                        stage,
                        issueNumber,
                        currentCostUsd: ceilingCheck.currentCostUsd,
                        effectiveCeilingUsd: ceilingCheck.effectiveCeilingUsd,
                        escalation: stageCeilingEscalationCount + 1,
                        cap: MAX_BUDGET_ESCALATIONS_PER_STAGE,
                        productiveSignals: progressGate.current,
                        productiveDelta: progressGate.delta,
                      }
                    );
                    vscode.window.showWarningMessage(
                      `Autonomous: pipeline ceiling exceeded for #${issueNumber} ` +
                        `($${ceilingCheck.currentCostUsd.toFixed(2)} / $${ceilingCheck.effectiveCeilingUsd.toFixed(2)}) — ` +
                        `auto-escalated (${stageCeilingEscalationCount + 1}/${MAX_BUDGET_ESCALATIONS_PER_STAGE}). ` +
                        `It will stop and save work if the cap is reached.`
                    );
                  } else {
                    // Flat productive progress since the last ceiling
                    // escalation — churn. Stop instead of doubling the
                    // ceiling again (Issue #3851 — the $112 proximate fix).
                    ceilingChoice = "Save Work & Stop";
                    this.logger.warn(
                      "Unattended ceiling escalation REFUSED — no productive progress since last escalation (churn)",
                      {
                        stage,
                        issueNumber,
                        currentCostUsd: ceilingCheck.currentCostUsd,
                        effectiveCeilingUsd: ceilingCheck.effectiveCeilingUsd,
                        productiveSignals: progressGate.current,
                        productiveDelta: progressGate.delta,
                      }
                    );
                    this.eventDispatcher.onStderr(
                      stage,
                      `[CEILING ESCALATION REFUSED] Pipeline ceiling exceeded for #${issueNumber} ` +
                        `($${ceilingCheck.currentCostUsd.toFixed(2)} / $${ceilingCheck.effectiveCeilingUsd.toFixed(2)}) but made NO ` +
                        `productive progress (commits / new files / phases) since the last escalation. ` +
                        `Churn detected — saving work and stopping instead of escalating. (Issue #3851)\n`
                    );
                    vscode.window.showWarningMessage(
                      `Autonomous: #${issueNumber} stopped — pipeline ceiling exceeded with no ` +
                        `productive progress (churn). Work saved. (Issue #3851)`
                    );
                  }
                } else {
                  ceilingChoice = await vscode.window.showWarningMessage(
                    `Pipeline ceiling exceeded for #${issueNumber} ` +
                      `($${ceilingCheck.currentCostUsd.toFixed(2)} / $${ceilingCheck.effectiveCeilingUsd.toFixed(2)}). ` +
                      `Escalation ${stageCeilingEscalationCount + 1}/${MAX_BUDGET_ESCALATIONS_PER_STAGE}. ` +
                      (ceilingRetroMsg || "Work will be lost if stopped without saving."),
                    { modal: false },
                    "Increase Ceiling & Continue",
                    "Save Work & Stop"
                  );
                }

                if (ceilingChoice === "Increase Ceiling & Continue") {
                  stageCeilingEscalationCount += 1;
                  budgetTerminated = false;
                  this.logger.info("User chose to increase pipeline ceiling", {
                    stage,
                    issueNumber,
                    previousCeiling: ceilingCheck.effectiveCeilingUsd,
                    newCeiling: ceilingCheck.effectiveCeilingUsd * 2,
                    escalation: stageCeilingEscalationCount,
                    cap: MAX_BUDGET_ESCALATIONS_PER_STAGE,
                  });
                  // #253: actually raise the ceiling, per-run. Before this
                  // the escalation only muted warnings for the current
                  // stage; the fresh per-stage ceiling instance (and the
                  // run-level between-stage check) still stopped the
                  // pipeline at the next boundary — run #236 was stopped
                  // one second after the user confirmed continuing.
                  this.ceilingOverrideUsd = ceilingCheck.effectiveCeilingUsd * 2;
                  stagePipelineCeiling.setOverrideCeiling(this.ceilingOverrideUsd);
                  stageCeilingWarningEmitted = true;
                  stageCeilingCheckpointEmitted = true;
                  this.eventDispatcher.onStderr(
                    stage,
                    `[PIPELINE CEILING OVERRIDDEN] Escalation ` +
                      `${stageCeilingEscalationCount}/${MAX_BUDGET_ESCALATIONS_PER_STAGE}. Stage continuing per user request.\n`
                  );
                } else {
                  await this.autoCommitWorktreeWIP(issueNumber, stage);
                  stageHandle?.kill();
                }
              }
            }
          } catch (err) {
            // Non-critical: ceiling check failure should not break pipeline
            this.logger.warn("Failed to check pipeline budget ceiling in onTokenUsage", {
              stage,
              err,
            });
          }
        }
      };

      const handle = runStageSkillHeadless(
        stage,
        issueNumber,
        {
          onStdout: (data) => {
            markStageOutput();
            // Reset stale timer on any output activity (Issue #1187)
            ptm?.resetActivityTimer();
            this.eventDispatcher.onStdout(stage, data);
          },
          onPhaseStart: (_detectedStage, name, index, total) => {
            markStageOutput();
            this.eventDispatcher.onPhaseStart(stage, name, index, total);
          },
          onStderr: (data) => {
            markStageOutput();
            // Reset stale timer on any output activity (Issue #1187)
            ptm?.resetActivityTimer();
            this.eventDispatcher.onStderr(stage, data);

            // Issue #3508: Cost warn toast. skillRunner emits [cost-warn] on
            // stderr when the stage cost crosses warnThresholdUsd. Fire a
            // non-blocking VSCode warning toast so the user is aware without
            // interrupting the pipeline.
            if (data.includes("[cost-warn]")) {
              void vscode.window.showWarningMessage(
                data.replace("[cost-warn] ", "").replace(/\n$/, "")
              );
            }

            // Compaction detection (Issue #1935): Claude Code emits a message
            // when it compresses conversation history. This is a strong signal
            // the issue is too large for a single pipeline run.
            if (
              !compactionDetected &&
              /auto-compact|compressing prior|conversation.*compress/i.test(data)
            ) {
              compactionDetected = true;
              this.logger.warn(
                "Context compaction detected — issue may be too large for single pipeline run",
                { stage, issueNumber }
              );
              vscode.window.showWarningMessage(
                `#${issueNumber} triggered context compaction during ${stage}. ` +
                  `This issue may be too large — consider breaking it down into smaller sub-issues.`
              );
            }
          },
          onTokenUsage: (usage) => {
            void evaluateBudgetAndCeiling(usage, { live: false });
          },
          onCostSnapshot: (usage) => {
            void evaluateBudgetAndCeiling(usage, { live: true });
          },
          onStageProgress: (usage) => {
            // Live in-stage token/cost estimate (#233), already throttled to
            // >=5s in skillRunner. Mirror the Go-driven path (PipelineBridge):
            // fire-and-forget a stage_progress event to the platform via Go so
            // the run-detail view shows tokens/cost accruing mid-stage. The
            // authoritative per-stage totals still flow through the "complete"
            // transition; this is a live estimate only. Best-effort.
            IpcClient.getInstance()
              .call("pipeline.notifyStageProgress", {
                repo: this.repoOverride ?? "",
                issueNumber,
                stage,
                inputTokens: usage.inputTokens,
                outputTokens: usage.outputTokens,
                cacheReadTokens: usage.cacheReadTokens,
                costUsd: usage.costUsd,
              })
              .catch((err) => {
                this.logger.warn("Failed to notify stage progress", { stage, err });
              });
          },
          onToolCall: (toolName, toolInput, toolUseId) => {
            // Bridge tool call events to PipelineCallbacks (Issue #639)
            // This routes tool_use blocks from stream-json → PipelineCallbacks.onToolCall
            // → pickupIssue.ts → PipelineStateService.recordToolCall → Dashboard
            const input = toolInput as Record<string, unknown> | undefined;
            const target =
              typeof input?.file_path === "string"
                ? input.file_path
                : typeof input?.command === "string"
                  ? input.command.substring(0, 100)
                  : typeof input?.pattern === "string"
                    ? input.pattern
                    : "";

            this.eventDispatcher.onToolCall(stage, {
              tool: toolName,
              target,
              args: input as Record<string, unknown> | undefined,
            });

            // Accumulate tool call for JSONL persistence (Issue #1004)
            const callIndex = this.accumulatedToolCalls.length;
            this.accumulatedToolCalls.push({
              tool: toolName,
              target: target || undefined,
              stage,
              timestamp: new Date().toISOString(),
              args: sanitizeToolCallArgs(input),
              caller: "direct" as const,
            });

            // Track pending tool call for duration/result backfill (Issue #1031)
            if (toolUseId) {
              this.pendingToolCalls.set(toolUseId, {
                index: callIndex,
                startTime: Date.now(),
              });
            }
          },
          onToolResult: (toolUseId, result, isError) => {
            // Backfill duration_ms, result, and error on the matching tool call (Issue #1031)
            const pending = this.pendingToolCalls.get(toolUseId);
            if (pending) {
              const record = this.accumulatedToolCalls[pending.index];
              if (record) {
                record.duration_ms = Date.now() - pending.startTime;
                record.result = result.length > 200 ? result.substring(0, 200) : result;
                if (isError) {
                  record.error = record.result;
                }
              }
              this.pendingToolCalls.delete(toolUseId);
            }
          },
          onStallWarningClear: () => {
            this.eventDispatcher.onStallWarningClear(stage);
          },
          onModelResolved: (_resolvedStage, model, adapter) => {
            // Record the resolved model up-front (#367) so a stage killed
            // before completeStage/failStage still attributes its true model
            // instead of 'unknown'. Fire-and-forget: telemetry must never
            // block or throw into the run. Use the closure-captured `stage`
            // (matching every other callback here).
            const stateService = this.stateService;
            if (!stateService) return;
            void stateService.recordStageModel(stage, { model, adapter }).catch((err) => {
              this.logger.warn("Failed to record resolved stage model up-front", {
                stage,
                err,
              });
            });
          },
          onComplete: async (result: SkillRunResult) => {
            markStageOutput();
            // #252: the first-output watchdog already failed this stage and
            // resolved the promise — a late completion (e.g. from the kill we
            // issued) must not double-fire terminal events.
            if (zombieWatchdogFired) {
              return;
            }
            // Issue #3542: mark the stage resolved synchronously, before any
            // await, so a still-in-flight onTokenUsage ceiling poll cannot
            // hard-stop a stage that has already produced its result.
            stageResultResolved = true;
            const durationMs = Date.now() - startTime;
            this.currentProcess = null;

            // Collect stall events from this stage for history recording (Issue #2652).
            // Cap the per-stage array — a pathologically stuck stage can emit
            // stall events unboundedly across retries/backtracks, and we only
            // need a representative sample in the history record.
            if (result.stallEvents && result.stallEvents.length > 0) {
              const existing = this.stageStallEvents.get(stage) ?? [];
              const combined = [...existing, ...result.stallEvents];
              const capped =
                combined.length > MAX_STALL_EVENTS_PER_STAGE
                  ? combined.slice(-MAX_STALL_EVENTS_PER_STAGE)
                  : combined;
              this.stageStallEvents.set(stage, capped);
            }

            // Sweep orphaned pendingToolCalls — entries for tool_use blocks
            // that never got a matching tool_result (Claude crash mid-call,
            // process SIGKILLed, etc). Each entry holds a closure into the
            // accumulatedToolCalls array, so leaking them pins records
            // indefinitely.
            const sweepCutoff = Date.now() - PENDING_TOOL_CALL_MAX_AGE_MS;
            for (const [toolUseId, pending] of this.pendingToolCalls) {
              if (pending.startTime < sweepCutoff) {
                this.pendingToolCalls.delete(toolUseId);
              }
            }

            // Clean up phase timeout timers and subscriptions (Issue #1187)
            // Complete all active phases for this stage to cancel their timers
            if (ptm && this.stateService) {
              try {
                const pState = await this.stateService.getState();
                const phases = pState?.stages[stage]?.phases;
                if (phases) {
                  for (const p of phases) {
                    if (p.status === "running") {
                      ptm.completePhase(stage, p.name);
                    }
                  }
                }
              } catch {
                // Non-critical: timer cleanup failure shouldn't block completion
              }
            }
            for (const d of phaseTimeoutDisposables) {
              d?.dispose();
            }
            // Note: isRunning is NOT reset here. It's managed by runPipeline()'s
            // finally block. Resetting here caused a race condition where the stop
            // button saw isRunning=false between stages. @see Issue #527

            // Issue #697: Detect when a subagent exits after an interactive
            // prompt in headless mode. The exit code may be 0 but the agent
            // asked a question no one could answer — treat as a failure.
            if (result.success && result.promptDetected) {
              this.logger.error("Stage exited after interactive prompt in headless mode", {
                stage,
                durationMs,
                promptDetected: true,
              });

              const promptError = new Error(
                `${stage} exited after an interactive prompt (AskUserQuestion) in headless mode. ` +
                  `The subagent asked a question that cannot be answered without a human. ` +
                  `This is treated as a stage failure.`
              );

              // Update state service
              if (this.stateService) {
                try {
                  await this.stateService.failStage(stage, promptError.message);
                } catch (err) {
                  this.logger.warn("Failed to update state on prompt-exit failure", { stage, err });
                }
              }

              this.eventDispatcher.onStageError(stage, promptError);

              const stageResult: StageRunResult = {
                success: false,
                stage,
                durationMs,
                error: promptError,
              };

              this.eventDispatcher.onStageComplete(stage, stageResult);
              resolve(stageResult);
              return;
            }

            // ── Post-merge ground-truth override for pr-merge (#266) ─────────
            // A late per-stage kill (progress-runaway, stall, or budget) must
            // NEVER record a pr-merge whose PR already MERGED as a failure. In
            // the #266 race the budget enforcer's escalation let the stage keep
            // running, the merge landed on the forge, then a progress-runaway
            // kill fired and its failed StageRunResult won — booking a MERGED
            // run as failed/stall_kill with a misattributed $75 runaway ceiling.
            // Before any kill branch declares failure, consult the deterministic
            // per-issue PR state (pr-{N}.json → gh). If it is MERGED, the stage
            // did its job: resolve success so the terminal outcome reflects
            // ground truth, let the pipeline advance to pipeline-finish, and
            // record the merge for the Go recording boundary. The existing
            // post-merge verification gate at the call site (#1819) still runs.
            if (
              stage === "pr-merge" &&
              !result.success &&
              (result.costCapExceeded || result.stallKilled || budgetTerminated)
            ) {
              const merged = await this.checkPrMergedForIssue(issueNumber);
              if (merged) {
                this.prMergedGroundTruth = true;
                this.logger.warn(
                  "pr-merge killed after the merge landed — PR is MERGED; recording success " +
                    "(ground truth overrides late per-stage kill) (#266)",
                  {
                    stage,
                    issueNumber,
                    prNumber: merged.prNumber,
                    durationMs,
                    costCapExceeded: result.costCapExceeded ?? false,
                    stallKilled: result.stallKilled ?? false,
                    budgetTerminated,
                  }
                );
                if (this.completedStageSet.has(stage)) {
                  return;
                }
                this.completedStageSet.add(stage);
                if (this.stateService) {
                  try {
                    await this.stateService.completeStage(stage);
                  } catch (err) {
                    this.logger.warn(
                      "Failed to mark pr-merge complete on post-merge ground-truth override",
                      { stage, err }
                    );
                  }
                }
                const mergedResult: StageRunResult = {
                  success: true,
                  stage,
                  durationMs,
                };
                this.eventDispatcher.onStageComplete(stage, mergedResult);
                resolve(mergedResult);
                return;
              }
            }

            // Issue #3002 / #3508: Detect runaway-progress termination.
            // When costCapExceeded is true, the stage was killed by the
            // progress-based runaway monitor (Issue #3783). The $75 dollar
            // runaway ceiling is warn-only, so this is NOT a ceiling crossing —
            // it is a productive-progress stall. The skillRunner already emitted
            // the authoritative [runaway-progress-exceeded] marker; Go maps that
            // to TerminalKindRunawayProgress (30m backoff, no queue halt, no
            // autonomous pause). Show a warning toast — do NOT halt queue.
            if (result.costCapExceeded) {
              const costAtTermUsd = result.costAtTerminationUsd ?? 0;

              this.logger.warn("Stage terminated due to progress-based runaway kill", {
                stage,
                durationMs,
                costCapExceeded: true,
                cost_at_termination_usd: costAtTermUsd,
              });

              // Show non-blocking warning toast — Go handles retry via the
              // transient stall-kill path. #266: report the cost at termination
              // only. There is NO dollar ceiling to name (the progress monitor,
              // not a $75 ceiling, fired the kill), so the old "$X / $75" framing
              // is dropped rather than printed as a nonsensical over/under ratio.
              void vscode.window.showWarningMessage(
                `Nightgauge: Issue #${issueNumber ?? "?"} hit a progress-based runaway kill at ${stage} ` +
                  `(cost $${costAtTermUsd.toFixed(2)}) — will retry automatically in 30 min.`
              );

              // #266: PRESERVE the authoritative marker skillRunner emitted
              // ([runaway-progress-exceeded], which Go maps to
              // TerminalKindRunawayProgress). The prior fallback synthesized a
              // `[runaway-ceiling-exceeded] ... Cost $X exceeded ceiling ($75)`
              // string that (a) misattributed a progress kill as a dollar-ceiling
              // crossing and (b) printed a ceiling the cost never approached. Only
              // synthesize when skillRunner left no runaway marker at all, and do
              // so with honest progress-kill semantics (no invented ceiling).
              const emittedMarker = result.error?.message ?? "";
              const hasRunawayMarker =
                emittedMarker.includes("[runaway-progress-exceeded]") ||
                emittedMarker.includes("[runaway-ceiling-exceeded]");
              const runawayMarker = hasRunawayMarker
                ? emittedMarker
                : `[runaway-progress-exceeded] Stage ${stage} terminated: progress-based runaway kill ` +
                  `at cost $${costAtTermUsd.toFixed(4)} (no dollar ceiling crossed). ` +
                  `Treated as transient — will retry with 30-minute backoff (Issue #3783).`;
              const costCapError = new Error(runawayMarker);

              if (this.stateService) {
                try {
                  await this.stateService.failStage(stage, costCapError.message, {
                    model: result.servedModel ?? result.modelDecision?.model,
                    adapter: result.adapterDecision?.adapter,
                  });
                } catch (err) {
                  this.logger.warn("Failed to update state on runaway-progress failure", {
                    stage,
                    err,
                  });
                }
              }

              this.eventDispatcher.onStageError(stage, costCapError);

              const stageResult: StageRunResult = {
                success: false,
                stage,
                durationMs,
                error: costCapError,
              };

              this.eventDispatcher.onStageComplete(stage, stageResult);
              resolve(stageResult);
              return;
            }

            // Issue #1620: Detect stall-killed termination.
            // When stallKilled is true, the process exceeded
            // stall_kill_multiplier × stall_threshold and was forcibly killed.
            //
            // Issue #3442: PRESERVE the underlying kill marker from
            // `result.error.message` (which carries the last 3 lines of
            // stderr via inferProcessError). Pre-fix this branch synthesized
            // a generic `[stall-killed] {stage} terminated...` message and
            // discarded `result.error`, which destroyed the
            // `[rate-limit-quota-exhausted]` marker (#3386) that
            // bootstrap/services.ts depends on for terminalFailureKind
            // classification. Without that classification the autonomous
            // scheduler's global quota cooldown (#3434) is bypassed and
            // every quota-exhausted kill increments the lifetime failure
            // cap — exactly the regression #3440 was supposed to close but
            // didn't because its Go-side fallback requires failureDetail
            // (also empty in this path). Compose the original marker text
            // into the synthetic message so downstream regex still matches.
            if (result.stallKilled) {
              this.logger.error("Stage terminated due to stall detection auto-kill", {
                stage,
                durationMs,
                stallKilled: true,
                stall_indicator: true, // Issue #2871 — distinct field for dashboard filtering
              });

              const stallError = composeStallKilledError(stage, durationMs, result.error);

              if (this.stateService) {
                try {
                  await this.stateService.failStage(stage, stallError.message, {
                    model: result.servedModel ?? result.modelDecision?.model,
                    adapter: result.adapterDecision?.adapter,
                  });
                } catch (err) {
                  this.logger.warn("Failed to update state on stall-kill failure", { stage, err });
                }
              }

              this.eventDispatcher.onStageError(stage, stallError);

              const stageResult: StageRunResult = {
                success: false,
                stage,
                durationMs,
                error: stallError,
              };

              this.eventDispatcher.onStageComplete(stage, stageResult);
              resolve(stageResult);
              return;
            }

            // Issue #835: Detect budget-exceeded termination.
            // When budgetTerminated is true, the process was killed by BudgetEnforcer
            // (cost overrun or context overrun — output token limits are warn-only
            // per Issue #1609).
            // Treat as non-retryable failure with clear error message.
            if (budgetTerminated) {
              // Use the actual enforcement limit captured at termination time,
              // which accounts for efficiency adjustments (Issue #1392).
              // getEffectiveLimit() ignores efficiency adjustments and would
              // report a higher limit than what was actually enforced.
              const costLimit =
                budgetTerminatedEffectiveLimit > 0
                  ? budgetTerminatedEffectiveLimit
                  : budgetEnforcer.getEffectiveLimit(stage, sizeLabel);
              // ----------------------------------------------------------------
              // SHIPPED-PARTIALLY reclassification (#3666)
              //
              // Before declaring a hard failure, look at the work product. If
              // pr-create was killed by the budget enforcer but actually opened
              // the PR, the agent shipped what pr-create owes the pipeline —
              // pr-merge can take it home. Counting this as a hard failure
              // increments LifetimeIssueFailures and pauses autonomous on what
              // is really a recoverable cost overrun.
              //
              // Symmetric to the pr-merge `shippedButOverbudget` override at
              // line ~7286: same idea, earlier stage, different success
              // criterion (PR exists vs PR merged).
              // ----------------------------------------------------------------
              let shippedPartially = false;
              let shippedPrNumber: number | undefined;
              if (stage === "pr-create") {
                const created = await this.checkPrCreatedForIssue(issueNumber);
                if (created) {
                  shippedPartially = true;
                  shippedPrNumber = created.prNumber;
                  this.logger.warn(
                    "pr-create budget kill reclassified as shipped-partially — PR exists, will resume at pr-merge",
                    {
                      stage,
                      issueNumber,
                      prNumber: created.prNumber,
                      prState: created.state,
                      costUsd: budgetTerminatedCost,
                      effectiveLimit: costLimit,
                      overrunRatio:
                        budgetEnforcer.getBaseBudget(stage, sizeLabel) > 0
                          ? budgetTerminatedCost / budgetEnforcer.getBaseBudget(stage, sizeLabel)
                          : 0,
                    }
                  );
                }
              }

              const budgetError = new Error(
                shippedPartially
                  ? `Stage ${stage} budget kill at $${budgetTerminatedCost.toFixed(2)} (limit $${costLimit.toFixed(2)}), but PR #${shippedPrNumber} was created — autonomous will resume at pr-merge (no lifetime-fail increment).`
                  : `Stage ${stage} terminated: budget exceeded. ` +
                      `Cost $${budgetTerminatedCost.toFixed(2)} exceeded the hard limit ($${costLimit.toFixed(2)}). ` +
                      `Configure pipeline.budget_mode or pipeline.budget_grace_percent to adjust.`
              );

              if (shippedPartially) {
                this.logger.warn(
                  "Stage cost-cap hit but work product shipped — treating as recoverable",
                  {
                    stage,
                    durationMs,
                    budgetMode: budgetConfig.mode,
                    effectiveLimit: costLimit,
                    costUsd: budgetTerminatedCost,
                    prNumber: shippedPrNumber,
                  }
                );
              } else {
                this.logger.error("Stage terminated due to cost budget exceeded", {
                  stage,
                  durationMs,
                  budgetMode: budgetConfig.mode,
                  effectiveLimit: costLimit,
                  costUsd: budgetTerminatedCost,
                });
              }

              // #3666 follow-up: when shipped-partially, transition the
              // stage to COMPLETE rather than FAILED. Pre-fix, this branch
              // always called failStage(), which triggered
              // PipelineStateService.sealPhases(stage, "failed") and
              // unconditionally sealed any still-running phase (notably
              // the always-last `self-assessment` epilogue) as `failed`.
              // The outer runPipeline shipped-partially short-circuit then
              // marked the stage `complete`, but the phase state was
              // already poisoned — the UI showed "Self Assessment failed"
              // on a run that the pipeline classified as recovered. Mark
              // the stage complete here so sealPhases seals running phases
              // as `complete`, matching the actual recovered outcome.
              if (this.stateService) {
                try {
                  if (shippedPartially) {
                    await this.stateService.completeStage(stage);
                  } else {
                    await this.stateService.failStage(stage, budgetError.message, {
                      model: result.servedModel ?? result.modelDecision?.model,
                      adapter: result.adapterDecision?.adapter,
                    });
                  }
                } catch (err) {
                  this.logger.warn("Failed to update state on budget-exceeded failure", {
                    stage,
                    err,
                    shippedPartially,
                  });
                }
              }

              this.eventDispatcher.onStageError(stage, budgetError);

              // Write budget overrun context file for Go scheduler retry (Issue #2338)
              try {
                const overrunDir = path.join(this.getWorkingDirectory(), ".nightgauge", "pipeline");
                const overrunPath = path.join(overrunDir, `budget-overrun-${issueNumber}.json`);
                let wipBranch = "";
                try {
                  const { stdout: branchOut } = await execAsync("git branch --show-current", {
                    cwd: this.getWorkingDirectory(),
                    timeout: 5_000,
                  });
                  wipBranch = branchOut.trim();
                } catch {
                  // Non-critical
                }
                const baseBudget = budgetEnforcer.getBaseBudget(stage, sizeLabel);
                const overrunData = {
                  schema_version: "1.2",
                  issue_number: issueNumber,
                  stage,
                  estimated_budget_usd: baseBudget,
                  actual_cost_usd: budgetTerminatedCost,
                  effective_limit_usd: costLimit,
                  overrun_ratio: baseBudget > 0 ? budgetTerminatedCost / baseBudget : 0,
                  wip_committed: true,
                  wip_branch: wipBranch,
                  // #3666: shipped_partially is set when the budget killed the
                  // stage but its work product (e.g. a created PR) shipped. The
                  // Go scheduler treats this like budget_ceiling_hit — no
                  // lifetime-fail increment, no cascade-breaker contribution.
                  shipped_partially: shippedPartially,
                  shipped_pr_number: shippedPrNumber ?? 0,
                  // #3667: records whether this limit came from adaptive p75 or static table.
                  estimate_source: adaptiveEstimateSources[stage] ?? "static_table",
                  timestamp: new Date().toISOString(),
                };
                fs.writeFileSync(overrunPath, JSON.stringify(overrunData, null, 2));
              } catch {
                // Non-critical — retry will still work, just without overrun context
              }

              const stageResult: StageRunResult = {
                success: false,
                stage,
                durationMs,
                error: budgetError,
                budgetExceeded: true,
                shippedPartially,
              };

              this.eventDispatcher.onStageComplete(stage, stageResult);
              resolve(stageResult);
              return;
            }

            if (result.success) {
              // Issue #698: Guard against duplicate completion callbacks.
              // If the stage already completed (e.g., retry/recovery fired
              // onComplete a second time), log a warning and skip the duplicate.
              if (this.completedStageSet.has(stage)) {
                this.logger.warn("Duplicate stage completion detected — ignoring", {
                  stage,
                  durationMs,
                });
                return;
              }
              this.completedStageSet.add(stage);

              this.logger.info("Stage completed", {
                stage,
                durationMs,
                model: result.modelDecision?.model,
                modelSource: result.modelDecision?.source,
                adapter: result.adapterDecision?.adapter,
                adapterSource: result.adapterDecision?.source,
              });

              // Update state service
              if (this.stateService) {
                try {
                  // Attribute the stage to the model that actually served it
                  // (result.servedModel, #91) — falling back to the requested
                  // model — and to the executing adapter, so the Go notify
                  // handler records them for BuildV2Record (#268: by-model cost
                  // breakdown + Adapter Mix donut).
                  await this.stateService.completeStage(stage, {
                    model: result.servedModel ?? result.modelDecision?.model,
                    adapter: result.adapterDecision?.adapter,
                  });
                  // Persist model selection for post-pipeline analysis (Issue #1259)
                  if (result.modelDecision) {
                    await this.stateService.setStageModelSelection(stage, {
                      model: result.modelDecision.model,
                      source: result.modelDecision.source,
                      confidence: result.modelDecision.selectionResult?.confidence,
                      complexity: result.modelDecision.selectionResult?.complexity,
                      mode: result.modelDecision.mode,
                      effort: result.modelDecision.effort,
                    });
                  }
                  // Persist per-stage adapter + source (Issue #3223). Mirrors
                  // the model-selection persistence above; the history writer
                  // pulls these into `HistoryStageTokenUsageSchema.adapter` and
                  // `.adapter_source`.
                  if (result.adapterDecision) {
                    await this.stateService.setStageAdapter(stage, result.adapterDecision);
                  }
                  // Persist escalated_from for escalation history (Issue #1343)
                  // Note: setStageModelSelection does not accept escalated_from yet;
                  // this is captured in the execution history record via modelDecision.
                } catch (err) {
                  this.logger.warn("Failed to update state on stage complete", {
                    stage,
                    err,
                  });
                }
              }

              const stageResult: StageRunResult = {
                success: true,
                stage,
                durationMs,
              };

              this.eventDispatcher.onStageComplete(stage, stageResult);
              resolve(stageResult);
            } else {
              const error = result.error || new Error("Stage failed");
              this.logger.error("Stage failed", {
                stage,
                error: error.message || String(error),
              });

              // Check if error is retryable
              const retryConfig = getRetryConfig();
              const errorMessage = error.message || String(error);

              // Apply health policy retry budget increase (Issue #1395)
              const effectiveMaxAttempts =
                retryConfig.max_auto_attempts + this.policyRetryBudgetIncrease;

              // Get current retry count from state
              const state = this.stateService ? await this.stateService.getState() : null;
              const currentAutoRetries = state?.stages[stage].auto_retry_count ?? 0;

              const shouldRetry =
                isRetryableApiError(errorMessage, retryConfig) &&
                currentAutoRetries < effectiveMaxAttempts;

              if (shouldRetry && this.stateService) {
                // This is a retryable API error and we haven't exhausted retries
                const nextAttempt = currentAutoRetries + 1;

                // Calculate backoff delay
                const delay = isRateLimitError(errorMessage)
                  ? retryConfig.rate_limit_delay_ms
                  : calculateBackoffDelay(currentAutoRetries, retryConfig);

                this.logger.info("API error detected, scheduling retry", {
                  stage,
                  attempt: nextAttempt,
                  maxAttempts: effectiveMaxAttempts,
                  delayMs: delay,
                });

                // Update state atomically to show retrying (Issue #414)
                // Uses batchUpdate to avoid 3 sequential writes racing with other state updates
                const nextRetryAt = new Date(Date.now() + delay).toISOString();
                try {
                  await this.stateService.batchUpdate((state) => {
                    const stageState = state.stages[stage];
                    // failStage fields
                    stageState.status = "failed";
                    stageState.error = errorMessage;
                    stageState.completed_at = new Date().toISOString();
                    // markRetrying fields
                    stageState.is_retrying = true;
                    stageState.next_retry_at = nextRetryAt;
                    // recordAutoRetry field
                    stageState.auto_retry_count = (stageState.auto_retry_count ?? 0) + 1;
                    return state;
                  });
                } catch (err) {
                  this.logger.warn("Failed to update state for retry", {
                    stage,
                    err,
                  });
                }

                // Sanitize error message for user display
                const sanitizedError = sanitizeApiError(errorMessage);
                this.eventDispatcher.onStageError(
                  stage,
                  new Error(
                    `API Error - retrying in ${(delay / 1000).toFixed(1)}s (attempt ${nextAttempt}/${effectiveMaxAttempts}): ${sanitizedError}`
                  )
                );

                // Wait for backoff delay
                await new Promise((resolveWait) => setTimeout(resolveWait, delay));

                // Clear retrying flag and retry the stage
                try {
                  await this.stateService.clearRetrying(stage);
                } catch (err) {
                  this.logger.warn("Failed to clear retrying flag", {
                    stage,
                    err,
                  });
                }

                // Retry by recursively calling runStage
                this.logger.info("Retrying stage after backoff", {
                  stage,
                  attempt: nextAttempt,
                });

                const retryResult = await this.runStage(
                  stage,
                  issueNumber,
                  callbacks,
                  undefined,
                  undefined,
                  this.pinnedWorkspaceRoot
                );
                resolve(retryResult);
              } else {
                // Not retryable or retries exhausted - fail permanently
                if (currentAutoRetries >= effectiveMaxAttempts) {
                  this.logger.warn("Max auto-retry attempts exhausted", {
                    stage,
                    attempts: currentAutoRetries,
                  });
                }

                // Update state service
                if (this.stateService) {
                  try {
                    await this.stateService.failStage(stage, errorMessage, {
                      model: result.servedModel ?? result.modelDecision?.model,
                      adapter: result.adapterDecision?.adapter,
                    });
                    await this.stateService.clearRetrying(stage);
                  } catch (err) {
                    this.logger.warn("Failed to update state on stage failure", {
                      stage,
                      err,
                    });
                  }
                }

                // Sanitize error for user display
                const sanitizedError = sanitizeApiError(errorMessage);
                const displayError =
                  currentAutoRetries >= effectiveMaxAttempts
                    ? new Error(
                        `API errors persisted after ${effectiveMaxAttempts} automatic retries. Use 'Retry Stage' button to try again. Error: ${sanitizedError}`
                      )
                    : new Error(sanitizedError);

                this.eventDispatcher.onStageError(stage, displayError);

                const stageResult: StageRunResult = {
                  success: false,
                  stage,
                  durationMs,
                  error: displayError,
                };

                this.eventDispatcher.onStageComplete(stage, stageResult);
                resolve(stageResult);
              }
            }
          },
          onError: (error) => {
            this.logger.error("Stage execution error", {
              stage,
              error: error.message || String(error),
            });
            this.eventDispatcher.onStageError(stage, error);
          },
        },
        issueMetadata,
        undefined,
        skipToPhase,
        modelOverride,
        this.pauseAutoRouting,
        pinnedWorkspaceRoot ?? this.pinnedWorkspaceRoot,
        modelOverrideSource,
        undefined, // injectedSkillContent
        undefined, // autonomousMode (managed by Go scheduler for this path)
        stageWarnThresholdUsd > 0 ? stageWarnThresholdUsd : undefined,
        // Issue #3867: pass the per-issue owning repo so the repo-mismatch gate
        // compares against the issue's intended repo, NOT the workspace primary.
        this.repoOverride
      );

      this.currentProcess = handle;
      // Expose handle to phase timeout handler registered before the Promise (Issue #1620)
      handleRef = handle;
      // Expose the full handle to the shared budget/ceiling evaluator (#254).
      stageHandle = handle;

      // Record child process PID in state.json for stale-slot recovery (Issue #1643)
      if (this.stateService && handle.process?.pid) {
        this.stateService.setStageProcessPid(stage, handle.process.pid).catch(() => {
          // Non-critical — recovery will fall back to timestamp-based detection
        });
      }

      // Handle abort
      if (this.abortController) {
        const abortListener = () => {
          if (this.currentProcess) {
            this.currentProcess.kill();
          }
        };
        this.abortController.signal.addEventListener("abort", abortListener);
      }
    });
  }

  /**
   * Stop the currently running pipeline
   */
  stop(): void {
    const hadOrphanedProcesses = hasActiveProcess();

    // Prevent queue from auto-starting the next issue after this pipeline
    // completes (as a result of being stopped). Without this, the killed
    // pipeline returns with success=false, handleQueueAutoStart() fires,
    // and the queue immediately picks up the next item — defeating the stop.
    // @see Issue #1785
    this.shouldStopQueueAfterCurrent = true;
    vscode.commands.executeCommand("setContext", "nightgauge.stopAfterCurrentQueue", true);

    if (this.abortController) {
      this.abortController.abort();
    }
    if (this.currentProcess) {
      this.currentProcess.kill();
      this.currentProcess = null;
    }

    // Defense-in-depth: always clear tracked stage processes. Previously this
    // was gated on hasActiveProcess() returning true at entry, but a race
    // between child_process close events and stop() could leave the registry
    // populated after the initial check. Unconditional cleanup is safe — the
    // registry only holds handles this extension spawned.
    killAllActiveProcesses();

    // Defense-in-depth: mark current stage as failed in state.json.
    // If clearPipeline() is never called (e.g., abort command errors out),
    // this prevents the 1-hour crash recovery timeout from being the only
    // safety net against zombie state.
    // @see Issue #851
    if (this.stateService && this.currentStage) {
      this.stateService.failStage(this.currentStage, "Pipeline stopped by user").catch((err) => {
        this.logger.warn("Failed to mark stage as failed during stop", {
          error: err,
        });
      });
    }

    this.logger.info("Pipeline stopped", {
      hadOrphanedProcesses,
    });
  }

  /**
   * Gracefully stop the pipeline: SIGTERM → wait timeoutMs → SIGKILL.
   * Used by platform-initiated cancel commands (#3552). The existing stop()
   * sends SIGKILL immediately and is reserved for user-initiated stops.
   */
  async gracefulStop(timeoutMs = 10_000): Promise<void> {
    this.shouldStopQueueAfterCurrent = true;

    if (this.abortController) {
      this.abortController.abort();
    }

    const proc = this.currentProcess;
    if (proc) {
      // Send SIGTERM via the underlying ChildProcess — SkillProcessHandle.kill()
      // sends SIGKILL directly, so we bypass it to honor the graceful window.
      proc.process.kill("SIGTERM");

      await new Promise<void>((resolve) => {
        const deadline = setTimeout(() => {
          killAllActiveProcesses();
          resolve();
        }, timeoutMs);

        proc.process.once("exit", () => {
          clearTimeout(deadline);
          resolve();
        });
      });
    }

    this.currentProcess = null;
    killAllActiveProcesses();

    if (this.stateService && this.currentStage) {
      this.stateService.failStage(this.currentStage, "Pipeline cancelled by platform").catch(() => {
        // Best effort
      });
    }

    this.logger.info("Pipeline gracefully stopped", { timeoutMs });
  }

  /**
   * Check if the pipeline is currently running
   */
  getIsRunning(): boolean {
    return this.isRunning;
  }

  /**
   * Get the user-initiated model override for the current run, if any (Issue #1610).
   */
  getUserModelOverride(): PipelineModelOverride | null {
    return this.userModelOverride;
  }

  /**
   * Set a model override for the next pipeline run (Issue #1610).
   * Consumed and cleared by runPipeline(). If the next runPipeline() call
   * also passes an explicit modelOverride parameter, that takes precedence.
   */
  setNextRunModelOverride(model: PipelineModelOverride | null): void {
    this.pendingUserModelOverride = model;
  }

  /**
   * Get the currently running stage
   */
  getCurrentStage(): PipelineStage | null {
    return this.currentStage;
  }

  /**
   * Approve the current approval gate
   * Used when waiting at an approval gate
   */
  approve(): void {
    if (this.approvalResolve) {
      this.approvalResolve(true);
      this.approvalResolve = null;
    }
  }

  /**
   * Reject the current approval gate
   * Stops the pipeline at the current stage
   */
  reject(): void {
    if (this.approvalResolve) {
      this.approvalResolve(false);
      this.approvalResolve = null;
    }
  }

  /**
   * Update configuration
   */
  setConfig(config: Partial<PipelineExecutionConfig>): void {
    this.config = { ...this.config, ...config };
  }

  /**
   * Set force full pipeline flag
   *
   * When true, routing-based stage skipping is disabled and all stages run.
   *
   * @param force - True to run all stages regardless of routing
   */
  setForceFullPipeline(force: boolean): void {
    this.config.forceFullPipeline = force;
  }

  /**
   * Set routing configuration
   *
   * @param routing - Routing configuration overrides
   */
  setRoutingConfig(routing: Partial<RoutingConfig>): void {
    this.config.routing = { ...this.config.routing, ...routing };
  }

  /**
   * Delay execution
   */
  private delay(ms: number): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }

  /**
   * Set flag to stop queue processing after the current issue completes.
   *
   * Unlike stop(), this allows the current issue to finish all stages.
   * handleQueueAutoStart() checks this flag before starting the next item.
   */
  stopQueueAfterCurrent(): void {
    if (!this.isRunning) {
      this.logger.warn("stopQueueAfterCurrent() called but pipeline is not running - ignoring");
      return;
    }

    this.shouldStopQueueAfterCurrent = true;
    this.logger.info("Stop-queue-after-current flag set - queue will not auto-start next issue");
  }

  /**
   * Get the current issue number from pipeline state (if available)
   */
  async getCurrentIssueNumber(): Promise<number | null> {
    const state = await this.stateService?.getState();
    return state?.issue_number ?? null;
  }

  /**
   * Dispose of resources
   */
  dispose(): void {
    this.stop();
    for (const disposable of this.disposables) {
      disposable.dispose();
    }
  }
}

/**
 * Map an on-disk run-state.json `state` string to the discriminator the
 * Recovery Dialog understands. Unknown values fall back to "none" so the
 * dialog defaults to safe-restart-only behavior.
 */
function mapLifecycle(state: string | undefined): "running" | "paused" | "aborted" | "none" {
  switch (state) {
    case "running":
    case "paused":
    case "aborted":
      return state;
    default:
      return "none";
  }
}
