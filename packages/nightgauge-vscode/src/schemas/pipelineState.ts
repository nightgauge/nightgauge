/**
 * Zod schemas for PipelineState validation
 *
 * These schemas provide runtime validation for state.json and batch-state.json,
 * catching corrupted or malformed data before it causes downstream errors.
 *
 * @see Issue #414 - Harden Pipeline State Management
 * @see packages/nightgauge-sdk/src/context/schemas/ for SDK schema patterns
 */

import { z } from "zod";

/**
 * Local copy of the adapter enum, inlined to avoid a circular import
 * (`config/schema.ts` imports `PipelineStageSchema` from this file). MUST
 * stay in sync with `ExecutionAdapterSchema` in `../config/schema.ts` and
 * `VALID_ADAPTERS` in `../utils/resolvers/modelResolver.ts`.
 */
const StageAdapterSchema = z.enum([
  "claude",
  "codex",
  "gemini",
  "gemini-sdk",
  "lm-studio",
  "ollama",
  "copilot",
]);

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
   * Enables auditing of skip decisions in state.json and execution history.
   */
  skip_reason: z.string().optional(),
  /**
   * Model selection metadata for this stage (Issue #734)
   *
   * Records which model was selected, how, and the auto-selector's
   * confidence/complexity assessment. Flows into execution history.
   */
  model_selection: z
    .object({
      model: z.string(),
      source: z.enum([
        "env",
        "config",
        "auto",
        "default",
        "stage-default",
        "experiment",
        "feedback-escalation",
        "user-override",
      ]),
      confidence: z.number().min(0).max(1).optional(),
      complexity: z.string().optional(),
      mode: z.enum(["manual", "automatic", "hybrid"]).optional(),
      effort: z.enum(["low", "medium", "high"]).optional(),
    })
    .optional(),
  /** Context handoff file size in bytes (Issue #1009) */
  context_file_size_bytes: z.number().int().min(0).optional(),
  /**
   * Phase progress within this stage (Issue #1029)
   *
   * Persisted to state.json for recovery on extension reload.
   * Cleared when the stage restarts (supports retries).
   */
  phases: z.array(StagePhaseSchema).optional(),
  /** Name of the currently running phase (Issue #1029) */
  current_phase: z.string().optional(),
  /** Total number of phases in this stage (Issue #1206) */
  total_phases: z.number().int().min(0).optional(),
  /** PID of the child process running this stage (Issue #1643) */
  process_pid: z.number().int().positive().optional(),
  /**
   * Adapter that ran this stage (Issue #3221, formalised on schema in #3231).
   *
   * Augmented at runtime today by `PipelineStateService.setStageAdapter` and
   * persisted on `state.json`. Declared here so persisted-state reads pass
   * Zod validation without a custom schema repair pass.
   */
  adapter: StageAdapterSchema.optional(),
  /**
   * Source step that produced the resolved adapter (Issue #3223, formalised
   * on schema in #3231). Mirrors `model_selection.source` for adapter
   * routing attribution.
   */
  adapter_source: z
    .enum(["env", "stage-config", "global-config", "auto-router", "fallback", "default"])
    .optional(),
  /**
   * Adapters tried at stage start when fallback walked (Issue #3231).
   *
   * Length 1 (or absent) — no fallback was needed. Length ≥ 2 — primary
   * failed prereq, candidates were attempted in order. Last entry equals
   * `adapter` on success; on full-chain failure this lists every candidate
   * tried (the `[stage:no-adapter-available]` envelope's `adapters_tried`).
   */
  adapter_fallback_chain_used: z.array(StageAdapterSchema).optional(),
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

/**
 * Main PipelineState schema for state.json
 *
 * Schema version: 1.0
 */
export const PipelineStateSchema = z.object({
  schema_version: z.literal("1.0"),
  issue_number: z.number().int().positive(),
  title: z.string().min(1),
  branch: z.string().min(1),
  base_branch: z.string().min(1),
  started_at: z.string().datetime(),
  updated_at: z.string().datetime(),
  execution_mode: PipelineExecutionModeSchema,
  paused: z.boolean(),
  stages: StagesSchema,
  tokens: TokensSchema,
  /** Pipeline outcome classification for analytics (Issue #1005, #1047) */
  outcome_type: z
    .enum([
      "productive",
      "verify-and-close",
      "already-resolved",
      "budget-ceiling",
      "cancelled",
      "shipped-but-overbudget",
      // Run ended with the PR unmerged behind a non-retryable repo-config
      // blocker — a human must change repo config (#190).
      "blocked",
      // Pickup deferred — issue's native blockedBy dependencies still open
      // (#189/#305). Non-failure; issue stays eligible for a later tick.
      "deferred",
    ])
    .optional(),
  /** Number of backtracks executed during this pipeline run (Issue #1342) */
  backtrack_count: z.number().int().min(0).optional(),
  /** History of backtrack events during this pipeline run (Issue #1342) */
  backtracks: z.array(BacktrackRecordSchema).optional(),
  /** Model escalations executed during this pipeline run (Issue #1343) */
  model_escalations: z.array(ModelEscalationRecordSchema).optional(),
  /** Proactive model escalations applied before stages run (Issue #1394) */
  proactive_escalations: z.array(ProactiveEscalationRecordSchema).optional(),
  /** Active health-gated policies applied at pipeline start (Issue #1395) */
  active_health_policies: z
    .object({
      tier: z.string(),
      retry_budget_increase: z.number().int().min(0),
      escalate_all_stages: z.boolean(),
      pause_auto_routing: z.boolean(),
      reasons: z.array(z.string()),
      score: z.number(),
      applied_at: z.string().datetime(),
    })
    .optional(),
  /** Issue labels from GitHub (e.g., "size:M", "priority:high") (Issue #1611) */
  labels: z.array(z.string()).optional(),
});
export type PipelineState = z.infer<typeof PipelineStateSchema>;

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

/**
 * Validation result with detailed error information
 */
export interface ValidationResult {
  success: boolean;
  data?: PipelineState;
  error?: string;
  /** Specific field that failed validation */
  failedField?: string;
}

/**
 * Validate a parsed JSON object against the PipelineState schema
 *
 * Returns a ValidationResult with either the validated data or error details.
 * This should be called after JSON.parse() succeeds.
 *
 * @param data - The parsed JSON data to validate
 * @returns ValidationResult with success/data or error details
 */
export function validatePipelineState(data: unknown): ValidationResult {
  const result = PipelineStateSchema.safeParse(data);

  if (result.success) {
    return { success: true, data: result.data };
  }

  // Extract first error for debugging
  const firstError = result.error.issues[0];
  const failedField = firstError?.path.join(".");
  const errorMessage = firstError?.message || "Unknown validation error";

  return {
    success: false,
    error: `Schema validation failed: ${errorMessage}${failedField ? ` at '${failedField}'` : ""}`,
    failedField,
  };
}
