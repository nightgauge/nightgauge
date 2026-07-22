/**
 * Zod schemas for Execution History records
 *
 * Defines the JSONL record format for pipeline execution history persistence.
 * Two record types:
 * - "run": Complete pipeline run record (written at pipeline-finish)
 * - "outcome": PR merge/close outcome (appended after pr-merge)
 *
 * Two schema versions:
 * - v1: Original schema (Issue #649)
 * - v2: Extended with tool_calls, outcome_type, required files/routing (Issue #1011)
 *
 * The writer always produces v2 records. The reader accepts both v1 and v2,
 * normalizing v1 records to v2 shape with defaults.
 *
 * @see Issue #649 - Execution History Persistence
 * @see Issue #1011 - Telemetry Schema v2
 * @see docs/ARCHITECTURE.md for utility patterns
 */

import { z } from "zod";
import {
  PipelineStageSchema,
  StageExecutionModeSchema,
  ProactiveEscalationRecordSchema,
} from "./pipelineState";
import { StallEventSchema } from "./stallEvents";
import { ExecutionAdapterSchema } from "../config/schema";
import { AdapterSourceSchema } from "../utils/resolvers/adapterResolver";

// ============================================================================
// Shared Sub-Schemas
// ============================================================================

/**
 * Per-stage token usage in history records
 */
export const HistoryStageTokenUsageSchema = z.object({
  input: z.number().int().min(0),
  output: z.number().int().min(0),
  cache_read: z.number().int().min(0),
  cache_creation: z.number().int().min(0),
  cost_usd: z.number().min(0),
  /** Model used for this stage (Issue #1006) */
  model: z.string().optional(),
  /**
   * How the model was selected (Issue #1006).
   *
   * NOTE: this enum is duplicated below at
   * `HistoryStageDetailSchema.model_selection.source`. Keep both in sync — when
   * adding or removing a value, update both literals atomically. Issue #3230
   * added `"auto-router"` for AutoProviderRouter picks.
   */
  model_source: z
    .enum([
      "env",
      "config",
      "stage-default",
      "auto",
      "auto-router",
      "experiment",
      "default",
      "feedback-escalation",
      "user-override",
    ])
    .optional(),
  /** Per-stage cache hit rate: cache_read / (input + cache_read). Range [0, 1]. Absent when no tokens used. (Issue #2459) */
  cache_hit_rate: z.number().min(0).max(1).optional(),
  /**
   * Adapter that executed this stage (Issue #3224).
   *
   * Captured per-stage so analytics can attribute cost and performance to the
   * specific adapter that ran each stage (Claude / Gemini / Codex / etc.). Pre
   * #3224 records and pre-Wave-2 runs lack this field — readers MUST treat the
   * absence as adapter-unknown rather than defaulting to a value.
   */
  adapter: ExecutionAdapterSchema.optional(),
  /**
   * Source step that produced the resolved adapter (Issue #3223).
   *
   * Mirrors `model_source` so dashboards and learning consumers can distinguish
   * a per-stage env override from a stage-config value, fallback substitution,
   * or the global default. Absent on records emitted before the SkillRunner
   * dispatcher honored `resolveStageAdapter` end-to-end. Populated from
   * `AdapterDecision.source` returned by the resolver.
   */
  adapter_source: AdapterSourceSchema.optional(),
  /**
   * Adapters tried at stage start, in order, when the fallback chain walked
   * (Issue #3231). Length 1 (or absent) means no fallback was needed —
   * `adapter` already names the only candidate considered. Length ≥ 2 means
   * the primary failed prereq and one or more candidates were attempted; the
   * winner is the final adapter (== `adapter` field) when the walk
   * succeeded, or this is the exhaustive list of every candidate when the
   * full chain failed (the stage emitted `[stage:no-adapter-available]`).
   *
   * Optional — emitted only when fallback occurred to keep records terse.
   * Pre-#3231 readers ignore it; post-#3231 dashboards can compute the
   * "fallback-rate per primary adapter" metric.
   */
  adapter_fallback_chain_used: z.array(ExecutionAdapterSchema).optional(),
  /**
   * Resolution step that produced `cost_usd` (Issue #3228).
   *
   * - `'native'`   — vendor-emitted cost (Claude `total_cost_usd`).
   * - `'computed'` — derived from the rate-card pricing table; the only path
   *                  that produces a non-zero cost for non-Claude adapters
   *                  prior to #3228 every non-Claude stage reported `0`).
   * - `'unknown'`  — adapter+model has no pricing entry; reported `cost_usd`
   *                  is `0` to make it impossible to silently undercount.
   *
   * Optional for backwards-compat with pre-#3228 JSONL records. Reader-side
   * normalization treats undefined as `'native'` only when `cost_usd > 0` —
   * that was the only path that ever produced a non-zero cost pre-#3228.
   */
  cost_source: z.enum(["native", "computed", "unknown"]).optional(),
});
export type HistoryStageTokenUsage = z.infer<typeof HistoryStageTokenUsageSchema>;

/**
 * Stage post-condition gate outcome (Issue #3266 / #3267).
 *
 * Mirrors `state.StageGateResult` in `internal/state/history.go`. The Go
 * scheduler runs a registered gate after each successful stage and writes
 * one record per gate that ran. Pre-#3266 records omit the field on
 * HistoryStageDetailSchema; pre-#3267 records omit `kind` (readers infer
 * "ok" from `passed=true` and "fail" from `passed=false`).
 */
export const StageGateResultSchema = z.object({
  gate_name: z.string(),
  passed: z.boolean(),
  reason: z.string(),
  evidence: z.array(z.string()).optional(),
  duration_ms: z.number().int().min(0).optional(),
  timestamp: z.string(),
  /**
   * Discriminator for the outcome classifier (Issue #3267):
   *   "ok"     — gate passed; post-condition satisfied.
   *   "no_op"  — gate failed because the skill exited 0 but produced no
   *              state change (missing context, branch not created, PR
   *              still OPEN). Maps to `skill-no-op` outcome_type.
   *   "fail"   — gate failed because of a hard error.
   * Absent on pre-#3267 records.
   */
  kind: z.enum(["ok", "no_op", "fail"]).optional(),
});
export type StageGateResult = z.infer<typeof StageGateResultSchema>;

/**
 * Per-stage anomaly record (Issue #3267).
 *
 * Mirrors `state.Anomaly` in `internal/state/history.go`. Currently produced
 * by the atomic-LLM-overrun detector. The shape is intentionally future-proof
 * — `kind` is a free-string at the Zod level so adding a detector doesn't
 * require a schema change. See docs/PIPELINE_ANOMALIES.md for the catalog.
 */
export const StageAnomalySchema = z.object({
  /** Anomaly identifier (e.g. "atomic_llm_overrun"). */
  kind: z.string(),
  /** Stage name. */
  stage: z.string(),
  /** Execution path observed (`"deterministic"` | `"llm"`). */
  execution_path: z.string(),
  /** Stage cost in USD that triggered the detector. */
  stage_cost_usd: z.number().min(0),
  /** Human-readable predicate that should have matched. */
  deterministic_predicate: z.string().optional(),
  /** ISO 8601 timestamp of detection. */
  timestamp: z.string(),
});
export type StageAnomaly = z.infer<typeof StageAnomalySchema>;

/**
 * Per-stage execution details in history records
 */
export const HistoryStageDetailSchema = z.object({
  status: z.enum(["complete", "failed", "skipped", "pending", "deferred"]),
  started_at: z.string().optional(),
  completed_at: z.string().optional(),
  duration_ms: z.number().int().min(0).optional(),
  error: z.string().optional(),
  execution_mode: StageExecutionModeSchema.optional(),
  auto_retry_count: z.number().int().min(0).optional(),
  manual_retry_count: z.number().int().min(0).optional(),
  /** Reason why this stage was skipped, if applicable (Issue #843) */
  skip_reason: z.string().optional(),
  /** Model selection metadata for this stage (Issue #734) */
  model_selection: z
    .object({
      model: z.string(),
      // NOTE: duplicated from HistoryStageTokenUsageSchema.model_source above.
      // Keep both enums in sync — Issue #3230 added "auto-router".
      source: z.enum([
        "env",
        "config",
        "stage-default",
        "auto",
        "auto-router",
        "experiment",
        "default",
        "feedback-escalation",
        "user-override",
      ]),
      confidence: z.number().min(0).max(1).optional(),
      complexity: z.string().optional(),
      mode: z.enum(["manual", "automatic", "hybrid"]).optional(),
      effort: z.enum(["low", "medium", "high"]).optional(),
      /** The model that was active before escalation (Issue #1343) */
      escalated_from: z.string().optional(),
    })
    .optional(),
  /** Context handoff file size in bytes (Issue #1009) */
  context_file_size_bytes: z.number().int().min(0).optional(),
  /** Failure category for weighted reliability scoring (Issue #1260) */
  failure_category: z.enum(["infrastructure", "agent", "organic"]).optional(),
  /** Zod schema validation errors captured when the context file failed validation */
  validation_errors: z
    .array(
      z.object({
        path: z.string(),
        code: z.string(),
        message: z.string(),
        received: z.string().optional(),
        expected: z.array(z.string()).optional(),
      })
    )
    .optional(),
  /** Context schema repair metadata — tracks whether repair was attempted and its outcome (Issue #2552) */
  repair_attempted: z.boolean().optional(),
  /** Whether the repair attempt produced a schema-valid context file */
  repair_succeeded: z.boolean().optional(),
  /** Number of repair attempts made for this stage */
  repair_attempts_count: z.number().int().min(0).optional(),
  /**
   * Stall detection events recorded during this stage (Issue #2652).
   * Each entry represents a state change: warn threshold reached, user response,
   * or forcible kill. Absent when no stalls were detected during execution.
   * Backward-compatible: older history records without this field parse correctly.
   */
  stall_events: z.array(StallEventSchema).optional(),
  /**
   * Whether this stage was killed by stall detection (Issue #2871).
   * True when at least one stall_event has action "kill". Enables fast filtering
   * for stall-killed runs in the dashboard and learning system.
   */
  stall_killed: z.boolean().optional(),
  /**
   * Last lines of subagent stdout/stderr captured at terminal failure (Issue #3001).
   *
   * Bounded by the Go runtime ring buffer (≤200 lines × ≤1KB/line = ~200KB).
   * Only populated when this stage is the one that failed terminally — present
   * exclusively on V3 records. Null/absent on success or non-terminal stages.
   */
  last_output_lines: z.string().optional(),
  /**
   * Performance mode active at stage start (Issue #3215).
   *
   * Captured per-stage because the user can toggle the mode mid-run via the
   * VSCode status-bar picker. Absent on records emitted before #3215 — readers
   * MUST treat the absence as mode-unknown rather than defaulting to a value.
   */
  performance_mode: z.enum(["efficiency", "elevated", "maximum", "frontier"]).optional(),
  /**
   * Execution path for this stage (Issue #3264).
   *
   * `"deterministic"` — the stage was completed by Go-side code (e.g. the
   *   pr-merge deterministic-first runner that issues a single `gh pr merge`
   *   API call) without spawning an LLM subagent. Token / cost contribution
   *   is zero.
   * `"llm"`           — the stage ran via the existing LLM skill path.
   *
   * Absent on records emitted before PR #3264; readers MUST treat absence as
   * `unknown` rather than defaulting. Forward compatible — additional stages
   * (pr-create has been suggested in epic #3261) can adopt the same field
   * without schema growth.
   */
  execution_path: z.enum(["deterministic", "llm"]).optional(),
  /**
   * Machine-readable reason the deterministic-first hook declined and this stage
   * fell through to the LLM path (Issue #297). Only set alongside
   * `execution_path === "llm"` when a deterministic hook actually ran and punted
   * (e.g. `"missing-dev-context"`, `"dirty-merge-state: BLOCKED"`,
   * `"ci-wait-timeout"`); absent on deterministic successes, on LLM-only stages
   * with no deterministic hook, and on records emitted before #297. Mirrors the
   * Go `state.V2StageDetail.PuntReason` wire field so the two producers write an
   * identical schema. Lets pipeline-health / retro answer WHY the expensive path
   * ran without the forensic session-log archaeology #288 required.
   */
  punt_reason: z.string().optional(),
  /**
   * Per-stage post-condition gate outcomes (Issue #3266 / #3267).
   *
   * Mirrors the Go state.StageGateResult shape. Each gate result records
   * whether the stage's post-condition gate passed and, since #3267,
   * a `kind` discriminator (`"ok"` | `"no_op"` | `"fail"`) so the
   * outcome classifier can emit `skill-no-op` deterministically without
   * regex-matching reason strings. Absent on records emitted before #3266.
   */
  gate_results: z.array(StageGateResultSchema).optional(),
  /**
   * Per-stage anomaly records (Issue #3267).
   *
   * Currently used by the atomic-eligible-stage LLM-overrun detector
   * (`atomic_llm_overrun`) — fires when pr-merge or pr-create runs through
   * the LLM path while the gate still passed and the stage cost crossed the
   * configured floor. Non-blocking: anomalies are surfaced on the dashboard
   * Performance tab and in telemetry; they do not turn a passing run into
   * a failure. See docs/PIPELINE_ANOMALIES.md.
   */
  anomalies: z.array(StageAnomalySchema).optional(),
});
export type HistoryStageDetail = z.infer<typeof HistoryStageDetailSchema>;

/**
 * Terminal failure kind — what aborted the pipeline run (Issue #3001).
 *
 * Independent of `failure_category` (the weighted-reliability bucket). The
 * terminal kind answers "what stopped the run", the category answers "who is
 * to blame for reliability scoring purposes".
 *
 *  - `stall_kill`         — subagent exceeded stall_kill_multiplier × threshold
 *  - `budget_exceeded`    — pipeline or stage token budget ceiling tripped
 *  - `validation_error`   — context schema validation failed terminally
 *  - `subagent_crash`     — subagent process died with non-zero exit + no recovery
 *  - `orchestrator_crash` — orchestrator process died mid-stage; record synthesized
 *                           on next startup from a stale current-run.json sidecar
 *  - `network_unavailable` — extended GitHub connectivity loss aborted the run (#3296)
 *  - `stream_idle_timeout` — Anthropic API closed a streaming response mid-flight (#3398)
 *  - `rate_limit_quota_exhausted` — idle stall while the rate-limit bucket was drained (#3386)
 *  - `worktree_uncommitted` — failure recovered: uncommitted work was auto-committed (#3542)
 *  - `budget_ceiling_hit` — the USD pipeline budget ceiling killed a running stage (#3542)
 *  - `issue_closed` — issue was already closed when pipeline started; non-failure (#3661)
 *  - `api_overloaded` — Anthropic API returned 529 "Overloaded"; transient, no pause (#3835)
 *  - `github_quota_low` — GitHub API rate-limit bucket below headroom at pipeline-start; transient, cooldown until reset (#3896)
 *  - `api_connection_lost` — Anthropic API transport drop (socket close / DNS blip mid-stage); transient, no pause (#4002)
 *  - `github_network_outage` — api.github.com unreachable at pipeline-start; transient, short global cooldown (#4002)
 *  - `premature_turn_end` — stage exited 0 but its post-condition gate reported no state change; agent ended its turn on a promise (#74)
 *  - `adapter_auth_failed` — pipeline-start adapter auth gate refused to launch (probe timed out after retry, or logged out); retryable infra, no cascade/lifetime-cap (#312)
 *  - `no_changes_produced` — pr-create's deterministic fallback confirmed zero commits ahead of base; genuinely nothing to open a PR for, e.g. a dispatched human-only issue (#317)
 *  - `validation_failed` — feature-validate honestly failed its quality gates (validation_status="failed"); organic implementation failure, not a subagent crash (#326)
 *
 * MUST stay in lockstep with the Go constants in
 * internal/orchestrator/failure_handler.go and the SDK `TerminalFailureKind`
 * union in failureClassifier.ts — a V3 record carrying a value not listed
 * here silently falls through to the V2 schema in AnyRunRecordSchema and
 * loses its terminal_failure_kind.
 */
export const TerminalFailureKindSchema = z.enum([
  "stall_kill",
  "budget_exceeded",
  "validation_error",
  "subagent_crash",
  "orchestrator_crash",
  "network_unavailable",
  "stream_idle_timeout",
  "rate_limit_quota_exhausted",
  "worktree_uncommitted",
  "budget_ceiling_hit",
  "issue_closed", // Issue #3661 — issue already closed when pipeline started (non-failure)
  "api_overloaded", // Issue #3835 — Anthropic 529 "Overloaded"; transient, retried without queue pause
  "github_quota_low", // Issue #3896 — GitHub API quota below headroom at pipeline-start; transient, cooldown until reset
  "api_connection_lost", // Issue #4002 — Anthropic API transport drop (socket close / DNS blip); transient, retried without queue pause
  "github_network_outage", // Issue #4002 — api.github.com unreachable at pipeline-start; transient, short global cooldown
  "model_unavailable", // Issue #42 — API rejected the selected model (not on plan / unknown / model usage cap); triggers tier-downgrade fallback
  "premature_turn_end", // Issue #74 — stage exited 0 but its gate reported no state change (agent ended its turn on a promise)
  "adapter_auth_failed", // Issue #312 — adapter auth pre-flight refused to launch (probe timed out after retry, or logged out); retryable infra
  "no_changes_produced", // Issue #317 — pr-create's deterministic fallback confirmed zero commits ahead of base; genuinely nothing to open a PR for
  "validation_failed", // Issue #326 — feature-validate honestly failed its quality gates (validation_status="failed"); organic implementation failure
]);
export type TerminalFailureKind = z.infer<typeof TerminalFailureKindSchema>;

/**
 * Tool call record for tracking individual tool invocations (Issue #1004)
 */
export const ToolCallRecordSchema = z.object({
  tool: z.string(),
  target: z.string().optional(),
  /** Pipeline stage during which this tool call occurred (Issue #1004) */
  stage: z.string().optional(),
  timestamp: z.string().optional(),
  duration_ms: z.number().int().min(0).optional(),
  args: z.record(z.string(), z.unknown()).optional(),
  result: z.string().optional(),
  error: z.string().optional(),
  /** Whether this tool call was direct or programmatic (Issue #1071) */
  caller: z.enum(["direct", "programmatic"]).optional(),
});
export type ToolCallRecord = z.infer<typeof ToolCallRecordSchema>;

// ============================================================================
// Batch Sub-Schema (shared between v1 and v2)
// ============================================================================

const BatchMetadataSchema = z.object({
  batch_id: z.number().int().positive(),
  batch_issue_numbers: z.array(z.number().int().positive()),
  attribution_method: z.enum(["proportional", "equal", "full-cost-to-each"]),
  batch_total_tokens: z.object({
    total_input: z.number().int().min(0),
    total_output: z.number().int().min(0),
    total_cache_read: z.number().int().min(0),
    total_cache_creation: z.number().int().min(0),
    estimated_cost_usd: z.number().min(0),
  }),
});

// ============================================================================
// Tokens Sub-Schema (shared between v1 and v2)
// ============================================================================

/**
 * PTC metrics sub-schema for token records (Issue #1071)
 */
const PTCMetricsSchema = z.object({
  total_tool_calls: z.number().int().min(0),
  programmatic_calls: z.number().int().min(0),
  direct_calls: z.number().int().min(0),
  programmatic_ratio: z.number().min(0).max(1),
  estimated_tokens_saved: z.number().int().min(0),
  code_execution_count: z.number().int().min(0),
  container_reuse_count: z.number().int().min(0),
});

const TokensSchema = z.object({
  total_input: z.number().int().min(0),
  total_output: z.number().int().min(0),
  total_cache_read: z.number().int().min(0),
  total_cache_creation: z.number().int().min(0),
  estimated_cost_usd: z.number().min(0),
  per_stage: z.record(z.string(), HistoryStageTokenUsageSchema).optional(),
  /** PTC metrics for programmatic vs direct tool call tracking (Issue #1071) */
  ptc_metrics: PTCMetricsSchema.optional(),
});

// ============================================================================
// V1 Run Record Schema
// ============================================================================

/**
 * V1 pipeline run record — original schema (Issue #649)
 */
export const ExecutionHistoryRunRecordSchema = z.object({
  schema_version: z.literal("1"),
  record_type: z.literal("run"),
  issue_number: z.number().int().positive(),
  title: z.string(),
  branch: z.string(),
  base_branch: z.string(),
  execution_mode: z.enum(["automatic", "manual"]),
  started_at: z.string(),
  completed_at: z.string(),
  total_duration_ms: z.number().int().min(0),
  outcome: z.enum(["complete", "failed", "cancelled"]),

  /** Issue labels from GitHub (Issue #844) */
  labels: z.array(z.string()).optional(),
  /** Extracted size label (e.g., 'S', 'M', 'L') (Issue #844) */
  size: z.string().nullable().optional(),
  /** Extracted type label (e.g., 'feature', 'bug') (Issue #844) */
  type: z.string().nullable().optional(),
  /** Extracted priority label (e.g., 'high', 'low') (Issue #844) */
  priority: z.string().nullable().optional(),

  stages: z.record(z.string(), HistoryStageDetailSchema),
  tokens: TokensSchema,

  files: z
    .object({
      read_count: z.number().int().min(0).optional(),
      written_count: z.number().int().min(0).optional(),
    })
    .optional(),

  routing: z
    .object({
      complexity_score: z.number().int().min(0).optional(),
      path: z.string().optional(),
      skip_stages: z.array(z.string()).optional(),
    })
    .optional(),

  /** Batch metadata for batched pipeline runs (Issue #805) */
  batch: BatchMetadataSchema.optional(),

  recorded_at: z.string(),
});
export type ExecutionHistoryRunRecord = z.infer<typeof ExecutionHistoryRunRecordSchema>;

// ============================================================================
// V2 Run Record Schema (Issue #1011)
// ============================================================================

/**
 * V2 pipeline run record — extended schema with new telemetry fields
 *
 * Changes from v1:
 * - `files` is required (was optional) with required sub-fields
 * - `routing` is required (was optional) with required sub-fields
 * - `outcome_type` added (optional, populated by Issue #1005)
 * - `tool_calls` added (optional, populated by Issue #1004)
 *
 * @see Issue #1011 - Telemetry Schema v2
 */
export const ExecutionHistoryRunRecordV2Schema = z.object({
  schema_version: z.literal("2"),
  record_type: z.literal("run"),
  issue_number: z.number().int().positive(),
  /**
   * "owner/name" this run belongs to. Written by the Go history producer from
   * RuntimeState.Repo and consumed by TelemetryUploaderService to populate the
   * platform's strict V4 `repo` field (and thus the dashboard's pipeline_runs
   * run list). Optional because records written before this field shipped omit
   * it — the uploader skips telemetry mapping for those rather than guessing.
   */
  repo: z.string().optional(),
  title: z.string(),
  branch: z.string(),
  base_branch: z.string(),
  execution_mode: z.enum(["automatic", "manual"]),
  started_at: z.string(),
  completed_at: z.string(),
  total_duration_ms: z.number().int().min(0),
  outcome: z.enum(["complete", "failed", "cancelled"]),

  /** Issue labels from GitHub (Issue #844) */
  labels: z.array(z.string()).optional(),
  /** Extracted size label (Issue #844) */
  size: z.string().nullable().optional(),
  /** Extracted type label (Issue #844) */
  type: z.string().nullable().optional(),
  /** Extracted priority label (Issue #844) */
  priority: z.string().nullable().optional(),

  stages: z.record(z.string(), HistoryStageDetailSchema),
  tokens: TokensSchema,

  /** Outcome classification for analytics (Issue #1005, #1047, #3267) */
  outcome_type: z
    .enum([
      "productive",
      "verify-and-close",
      "already-resolved",
      "budget-ceiling",
      "shipped-but-overbudget",
      // Skill exited 0 but the post-condition gate detected no state change
      // (Issue #3267). Distinct from "failed" — the skill didn't error, it
      // just didn't do the work.
      "skill-no-op",
      // Run ended with the PR unmerged behind a non-retryable repo-config
      // blocker — a human must change repo config (#190).
      "blocked",
      // Pickup deferred because the issue's native blockedBy dependencies are
      // still open (#189/#305). A non-failure: no tokens spent, the issue stays
      // eligible. Paired with outcome="cancelled" and an empty
      // terminal_failure_kind on the run record.
      "deferred",
    ])
    .optional(),

  /** Tool call records for the pipeline run (Issue #1004) */
  tool_calls: z.array(ToolCallRecordSchema).optional(),

  /** File operation counts — required in v2 (Issue #1005) */
  files: z.object({
    read_count: z.number().int().min(0),
    written_count: z.number().int().min(0),
  }),

  /** Routing metadata — required in v2 (Issue #1005) */
  routing: z.object({
    complexity_score: z.number().int().min(0),
    path: z.string(),
    skip_stages: z.array(z.string()),
  }),

  /** Batch metadata for batched pipeline runs (Issue #805) */
  batch: BatchMetadataSchema.optional(),

  /**
   * True when this run resumed a previously-failed pipeline (Issue #1261).
   *
   * Recovery-run costs are excluded from the Cost Trend health component to
   * prevent a successful resume from inflating the cost baseline.
   */
  is_recovery: z.boolean().optional(),

  /**
   * True when this run used the legacy supercharge envelope (Opus + max effort).
   *
   * @deprecated Issue #3009 — prefer `performance_mode === "maximum"`. Kept
   * additively for one release so dashboards and external consumers (Discord
   * embed, cost-trend filter) keep working until they migrate.
   */
  is_supercharge: z.boolean().optional(),

  /**
   * Active performance mode for this run (Issue #3009).
   *
   * Calibration and cost-trend health components segment on this field;
   * `efficiency` and `maximum` runs are excluded from prediction-accuracy
   * baselines while `elevated` flows through normally.
   */
  performance_mode: z.enum(["efficiency", "elevated", "maximum", "frontier"]).optional(),

  /** Proactive model escalations applied before stages run (Issue #1394) */
  proactive_escalations: z.array(ProactiveEscalationRecordSchema).optional(),

  /**
   * Active focus lens state at pipeline start (Issue #2460).
   *
   * Records which focus lens (if any) was active when this pipeline run began.
   * Used for A/B comparison of focus vs non-focus run outcomes and costs.
   * Absent when no focus lens was active (equivalent to "general" lens).
   */
  focus_lens_active: z
    .object({
      /** The active lens name (e.g., "quality", "security", "features") */
      lens: z.string(),
      /** When the focus was set (ISO 8601) */
      set_at: z.string().optional(),
      /** Who set the focus ("cli", "vscode", "ipc") */
      set_by: z.string().optional(),
    })
    .optional(),

  /**
   * Pipeline run UUID for platform deduplication vs. real-time events (#3558).
   *
   * Written by the Go scheduler into run-state.json (RunState.RunID). Carried
   * into JSONL batch records so the platform can deduplicate against records
   * received via the real-time event path (#3556) for the same run. Optional
   * for backward compatibility — pre-#3558 records parse without it.
   */
  run_id: z.string().optional(),

  recorded_at: z.string(),
});
export type ExecutionHistoryRunRecordV2 = z.infer<typeof ExecutionHistoryRunRecordV2Schema>;

// ============================================================================
// V3 Run Record Schema (Issue #3001)
// ============================================================================

/**
 * V3 pipeline run record — adds terminal failure preservation fields.
 *
 * Changes from v2 (additive only):
 * - `terminal_failure_kind` (optional) — what aborted the run; absent on success
 *
 * Per-stage `last_output_lines` is added on `HistoryStageDetailSchema` and is
 * therefore valid in V3 records (and also tolerated as an unknown field on V1/V2
 * — Zod ignores unknown keys by default with `.object()`, but our schema is
 * non-strict so this is safe).
 *
 * @see Issue #3001 — Preserve pipeline + queue state on terminal failure
 */
export const ExecutionHistoryRunRecordV3Schema = ExecutionHistoryRunRecordV2Schema.extend({
  schema_version: z.literal("3"),
  /**
   * What aborted the pipeline run, if it failed (Issue #3001).
   * Absent on `outcome === "complete"`.
   */
  terminal_failure_kind: TerminalFailureKindSchema.optional(),
});
export type ExecutionHistoryRunRecordV3 = z.infer<typeof ExecutionHistoryRunRecordV3Schema>;

// ============================================================================
// V1 Outcome Record Schema
// ============================================================================

/**
 * V1 PR merge/close outcome — appended after pr-merge stage completes
 */
export const ExecutionOutcomeRecordSchema = z.object({
  schema_version: z.literal("1"),
  record_type: z.literal("outcome"),
  issue_number: z.number().int().positive(),
  pr_number: z.number().int().positive(),
  outcome: z.enum(["merged", "closed"]),
  merged_at: z.string().optional(),
  closed_at: z.string().optional(),
  recorded_at: z.string(),
});
export type ExecutionOutcomeRecord = z.infer<typeof ExecutionOutcomeRecordSchema>;

// ============================================================================
// V2 Outcome Record Schema (Issue #1011)
// ============================================================================

/**
 * V2 outcome record — same shape as v1, just schema_version bumped
 */
export const ExecutionOutcomeRecordV2Schema = z.object({
  schema_version: z.literal("2"),
  record_type: z.literal("outcome"),
  issue_number: z.number().int().positive(),
  pr_number: z.number().int().positive(),
  outcome: z.enum(["merged", "closed"]),
  merged_at: z.string().optional(),
  closed_at: z.string().optional(),
  recorded_at: z.string(),
});
export type ExecutionOutcomeRecordV2 = z.infer<typeof ExecutionOutcomeRecordV2Schema>;

// ============================================================================
// Version-Aware Union Schemas
// ============================================================================

/**
 * Any valid run record (v1, v2, or v3)
 *
 * Tries V3 first (most recent), then V2, then V1. Issue #3001: V3 added
 * `terminal_failure_kind`; readers accept all three so older daily JSONLs
 * remain valid without migration.
 */
export const AnyRunRecordSchema = z.union([
  ExecutionHistoryRunRecordV3Schema,
  ExecutionHistoryRunRecordV2Schema,
  ExecutionHistoryRunRecordSchema,
]);
export type AnyRunRecord = z.infer<typeof AnyRunRecordSchema>;

/**
 * Any valid outcome record (v1 or v2)
 */
export const AnyOutcomeRecordSchema = z.union([
  ExecutionOutcomeRecordV2Schema,
  ExecutionOutcomeRecordSchema,
]);
export type AnyOutcomeRecord = z.infer<typeof AnyOutcomeRecordSchema>;

/**
 * Any valid execution history record (run or outcome, v1 or v2)
 *
 * Uses z.union instead of z.discriminatedUnion because we have two
 * discriminator dimensions (record_type + schema_version). The reader
 * tries v2 first, falls back to v1.
 */
export const ExecutionHistoryRecordSchema = z.union([
  ExecutionHistoryRunRecordV3Schema,
  ExecutionHistoryRunRecordV2Schema,
  ExecutionHistoryRunRecordSchema,
  ExecutionOutcomeRecordV2Schema,
  ExecutionOutcomeRecordSchema,
]);
export type ExecutionHistoryRecord = z.infer<typeof ExecutionHistoryRecordSchema>;
