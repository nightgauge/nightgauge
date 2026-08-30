/**
 * Zod schemas for pipeline stage/state sub-objects.
 *
 * These schemas provide runtime validation for batch-state.json and for the
 * stage-shaped payloads that cross the IPC boundary, catching corrupted or
 * malformed data before it causes downstream errors.
 *
 * The top-level run-state object schema is NOT here — see the #471 tombstone
 * below for why it was retired.
 *
 * @see Issue #414 - Harden Pipeline State Management
 * @see packages/nightgauge-sdk/src/context/schemas/ for SDK schema patterns
 */

import { z } from "zod";

/**
 * Pipeline stage names
 */
export const PipelineStageSchema = z.enum([
  "pipeline-start",
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
  "pipeline-finish",
]);
export type PipelineStage = z.infer<typeof PipelineStageSchema>;

/**
 * Stage status values
 */
export const PipelineStageStatusSchema = z.enum([
  "pending",
  "running",
  "complete",
  "failed",
  "skipped",
]);
export type PipelineStageStatus = z.infer<typeof PipelineStageStatusSchema>;

/**
 * Execution mode for pipeline runs
 */
export const PipelineExecutionModeSchema = z.enum(["automatic", "manual"]);
export type PipelineExecutionMode = z.infer<typeof PipelineExecutionModeSchema>;

/**
 * Stage execution mode for token tracking availability
 *
 * - 'headless': Automated execution with stream-json output - tokens are tracked
 * - 'interactive': Conversational execution with raw text output - tokens are N/A
 *
 * @see Issue #498 - Token tracking for interactive execution mode
 * @see docs/INTERACTIVE_MODE.md
 */
export const StageExecutionModeSchema = z.enum(["headless", "interactive"]);
export type StageExecutionMode = z.infer<typeof StageExecutionModeSchema>;

/**
 * Per-stage token usage breakdown
 */
export const StageTokenUsageSchema = z.object({
  input: z.number().int().min(0),
  output: z.number().int().min(0),
  cache_read: z.number().int().min(0),
  cache_creation: z.number().int().min(0),
  cost_usd: z.number().min(0),
});
export type StageTokenUsage = z.infer<typeof StageTokenUsageSchema>;

/**
 * Phase state within a pipeline stage
 *
 * Phases are sub-steps within a stage (e.g., "Load Context", "Write Plan").
 * Phase definitions are owned by the emitter — canonical names are not enforced.
 *
 * Mirrors the SDK's StagePhase interface shape but uses Zod for runtime validation.
 *
 * @see Issue #1029 - Persist and recover phase state
 * @see packages/nightgauge-sdk/src/events/EventBus.ts - StagePhase interface
 */
/**
 * Record of a single backtrack event during pipeline execution
 *
 * Tracks when the orchestrator rewinds to an earlier stage
 * in response to a blocking feedback signal.
 *
 * @see Issue #1342 - Orchestrator Backtrack Engine
 */
export const BacktrackRecordSchema = z.object({
  from_stage: PipelineStageSchema,
  to_stage: PipelineStageSchema,
  signal_type: z.string(),
  rationale: z.string(),
  timestamp: z.string().datetime(),
  attempt_number: z.number().int().min(1),
});
export type BacktrackRecord = z.infer<typeof BacktrackRecordSchema>;

/**
 * Record of a single model escalation event during pipeline execution
 *
 * Tracks when the orchestrator retries the same stage with a more capable
 * model in response to a MODEL_ESCALATION_NEEDED feedback signal.
 *
 * @see Issue #1343 - Dynamic Model Escalation Engine
 */
export const ModelEscalationRecordSchema = z.object({
  stage: PipelineStageSchema,
  from_model: z.string(),
  to_model: z.string(),
  rationale: z.string(),
  timestamp: z.string().datetime(),
  attempt_number: z.number().int().min(1),
});
export type ModelEscalationRecord = z.infer<typeof ModelEscalationRecordSchema>;

/**
 * Record of a proactive model escalation applied before a stage runs
 *
 * Unlike reactive escalation (which retries after failure), proactive
 * escalation preemptively upgrades the model based on health trend
 * and per-stage failure rate, avoiding a wasted first attempt.
 *
 * @see Issue #1394 - Pre-stage health check — proactive model escalation
 */
export const ProactiveEscalationRecordSchema = z.object({
  stage: PipelineStageSchema,
  from_model: z.string(),
  to_model: z.string(),
  health_trend_slope: z.number(),
  stage_failure_rate: z.number(),
  rationale: z.string(),
  timestamp: z.string().datetime(),
});
export type ProactiveEscalationRecord = z.infer<typeof ProactiveEscalationRecordSchema>;

export const StagePhaseSchema = z.object({
  name: z.string().min(1),
  status: z.enum(["pending", "running", "complete", "skipped", "failed"]),
  started_at: z.string().datetime().optional(),
  completed_at: z.string().datetime().optional(),
});
export type StagePhase = z.infer<typeof StagePhaseSchema>;

/**
 * Individual stage state
 */
export const StageStateSchema = z.object({
  status: PipelineStageStatusSchema,
  started_at: z.string().datetime().optional(),
  completed_at: z.string().datetime().optional(),
  duration_ms: z.number().int().min(0).optional(),
  error: z.string().optional(),
  is_retrying: z.boolean().optional(),
  next_retry_at: z.string().datetime().optional(),
  auto_retry_count: z.number().int().min(0).optional(),
  manual_retry_count: z.number().int().min(0).optional(),
  retry_count: z.number().int().min(0).optional(), // deprecated
  /**
   * Execution mode for this stage run
   *
   * When 'interactive', token usage is unavailable (displayed as N/A).
   * When 'headless' or undefined, token usage is tracked normally.
   *
   * @see Issue #498 - Token tracking for interactive execution mode
   */
  execution_mode: StageExecutionModeSchema.optional(),
  /**
   * Reason why this stage was skipped (Issue #843)
   *
   * Persisted when a stage is skipped via routing decisions or legacy config.
   * Enables auditing of skip decisions in the execution history record.
   */
  skip_reason: z.string().optional(),
  // `model_selection` used to sit here, carrying a THIRD copy of the selection
  // vocabulary (already drifted: it never gained "auto-router"). Nothing wrote
  // a model_selection into state.json — its writer,
  // PipelineStateService.setStageModelSelection, was an empty stub, since
  // deleted with #465 — and this schema's only validator, validatePipelineState,
  // had no callers, so the enum guarded nothing; that validator is gone too
  // (#467). Deleted with #446; history attribution lives on the history record
  // (`HistoryStageDetailSchema.model_selection`).
  /** Context handoff file size in bytes (Issue #1009) */
  context_file_size_bytes: z.number().int().min(0).optional(),
  /**
   * Phase progress within this stage (Issue #1029)
   *
   * Carried on the runtime snapshot for recovery on extension reload.
   * Cleared when the stage restarts (supports retries).
   */
  phases: z.array(StagePhaseSchema).optional(),
  /** Name of the currently running phase (Issue #1029) */
  current_phase: z.string().optional(),
  /** Total number of phases in this stage (Issue #1206) */
  total_phases: z.number().int().min(0).optional(),
  // `process_pid` used to sit here (Issue #1643), the stage child's pid for the
  // TypeScript stale-slot scanner. Its writer (`setStageProcessPid`) was an
  // empty stub and its only reader was that scanner, which read a pipeline
  // state file nothing writes; both were deleted with #427. (The scanner's class name is
  // deliberately not repeated here — `tests/bootstrap/staleSlotScannerRemoved.test.ts`
  // is the one place that names it, as the guard asserting it stays gone.) The stage-child
  // pid that actually decides liveness travels the IPC wire as `stagePid` on
  // `pipeline.notifyStageTransition` and lands on the Go runtime snapshot
  // (`RuntimeState.SetStageChild`), where the orphan ladder probes it —
  // internal/ipc/pipeline_orphan_reconcile.go, ADR-017 §7.2 arm 3.
});
export type StageState = z.infer<typeof StageStateSchema>;

/**
 * PTC metrics for programmatic vs direct tool call tracking (Issue #1071)
 */
export const PTCMetricsSchema = z.object({
  total_tool_calls: z.number().int().min(0),
  programmatic_calls: z.number().int().min(0),
  direct_calls: z.number().int().min(0),
  programmatic_ratio: z.number().min(0).max(1),
  estimated_tokens_saved: z.number().int().min(0),
  code_execution_count: z.number().int().min(0),
  container_reuse_count: z.number().int().min(0),
});
export type PTCMetrics = z.infer<typeof PTCMetricsSchema>;

/**
 * Token usage totals
 */
export const TokensSchema = z.object({
  total_input: z.number().int().min(0),
  total_output: z.number().int().min(0),
  total_cache_read: z.number().int().min(0),
  total_cache_creation: z.number().int().min(0),
  estimated_cost_usd: z.number().min(0),
  per_stage: z.record(z.string(), StageTokenUsageSchema).optional(),
  /** PTC metrics for programmatic vs direct tool call tracking (Issue #1071) */
  ptc_metrics: PTCMetricsSchema.optional(),
});
export type Tokens = z.infer<typeof TokensSchema>;

/**
 * Stages record - all pipeline stages mapped to their state
 */
export const StagesSchema = z.record(z.string(), StageStateSchema);
export type Stages = z.infer<typeof StagesSchema>;

/*
 * The top-level pipeline-state object schema and its inferred type used to sit
 * here: the wire format of <worktree>/.nightgauge/pipeline/<the writer-less
 * state file>. Retired by #471.
 *
 * It had ZERO consumers in src. Nothing in Go, TypeScript or any skill has ever
 * written that file, so the format it described never existed on disk, and the
 * readers that reached for it all took their not-found fallback on every real
 * run. The type it exported was not the one the extension uses either — the
 * live PipelineState is a hand-written interface in PipelineStateService, whose
 * _lastState is populated from Go over IPC (pipeline.stateChanged /
 * applyRuntimeSnapshot) and never from a file read. The two had already drifted
 * into independent shapes.
 *
 * The per-stage and sub-object schemas in this file (StageStateSchema,
 * StagesSchema, PipelineStageSchema, StagePhase, BacktrackRecord,
 * ModelEscalationRecord, StallEscalationLevel, PauseForStallPayload, ...) are
 * live and stay — they are consumed by config/schema.ts, the tree providers and
 * the orchestrator event dispatcher.
 */
// ============================================================================
// Stall Escalation Types (Issue #2656)
// ============================================================================

/**
 * Escalation levels for autonomous mode stall handling.
 * Each level represents a progressively more aggressive notification.
 */
export const StallEscalationLevelSchema = z.enum([
  "status_bar",
  "output_panel",
  "notification",
  "discord",
  "pause",
]);
export type StallEscalationLevel = z.infer<typeof StallEscalationLevelSchema>;

/**
 * Metadata tracking the current stall escalation state.
 */
export const StallEscalationMetadataSchema = z.object({
  level: StallEscalationLevelSchema,
  elapsed_ms: z.number().int().min(0),
  stall_threshold_ms: z.number().int().min(0),
  extreme_threshold_ms: z.number().int().min(0),
  last_escalation_at: z.string().datetime(),
  escalation_count: z.number().int().min(0),
});
export type StallEscalationMetadata = z.infer<typeof StallEscalationMetadataSchema>;

/**
 * Payload for the pause-for-stall dialog in autonomous mode.
 */
export const PauseForStallPayloadSchema = z.object({
  reason: z.literal("stall_extreme"),
  issue_number: z.number().int().positive(),
  stage: PipelineStageSchema,
  elapsed_ms: z.number().int().min(0),
  threshold_ms: z.number().int().min(0),
  timeout_ms: z.number().int().min(0),
});
export type PauseForStallPayload = z.infer<typeof PauseForStallPayloadSchema>;

/**
 * User's resolution of a stall pause dialog.
 */
export const PauseResolutionSchema = z.object({
  action: z.enum(["resume", "abort"]),
  issue_number: z.number().int().positive(),
  stage: PipelineStageSchema,
  resolved_at: z.string().datetime(),
});
export type PauseResolution = z.infer<typeof PauseResolutionSchema>;

// `validatePipelineState` and its `ValidationResult` used to sit here: a
// `safeParse` wrapper that flattened Zod's first issue into a string. It had
// ZERO callers in src — the schema it wrapped was consumed for types only — so
// it guarded nothing while looking like a guard, which is precisely how the
// drifted `model_selection` field survived here unnoticed until #446. Deleted
// with #467. The schema it validated is itself gone as of #471 (see the
// tombstone above). If a read site ever genuinely needs validation, call
// `safeParse` at that site, where its result can actually change what the code
// does.
