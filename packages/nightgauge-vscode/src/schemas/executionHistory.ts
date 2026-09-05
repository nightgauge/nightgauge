/**
 * Zod schemas for Execution History records
 *
 * Defines the JSONL record format for pipeline execution history persistence.
 * Two record types:
 * - "run": Complete pipeline run record (written at pipeline-finish)
 * - "outcome": PR merge/close outcome (appended after pr-merge)
 *
 * Three run-record schema versions:
 * - v1: Original schema (Issue #649)
 * - v2: Extended with tool_calls, outcome_type, required files/routing (Issue #1011)
 * - v3: Extended with terminal failure preservation (Issue #3001)
 *
 * Writers produce v2 or v3 run records. The reader accepts v1, v2, and v3,
 * normalizing v1 records to the v2 field set and canonicalizing historical
 * field vocabulary shared by all versions.
 *
 * @see Issue #649 - Execution History Persistence
 * @see Issue #1011 - Telemetry Schema v2
 * @see docs/ARCHITECTURE.md for utility patterns
 */

import { z } from "zod";
import { EFFORT_LEVELS, MODEL_SELECTION_SOURCES } from "@nightgauge/sdk";
import {
  PipelineExecutionModeSchema,
  PipelineStageSchema,
  StageExecutionModeSchema,
  ProactiveEscalationRecordSchema,
} from "./pipelineState";
import { StallEventSchema } from "./stallEvents";
import { ExecutionAdapterSchema, ModelRoutingModeSchema } from "../config/schema";
import { ORCHESTRATOR_CRASH_TERMINAL_KIND } from "../utils/orchestratorCrashRecord";
export {
  ORCHESTRATOR_CRASH_TERMINAL_KIND,
  isOrchestratorCrashRecord,
} from "../utils/orchestratorCrashRecord";

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
  /**
   * The model band that served this stage (#1006, given a writer in #1213).
   *
   * Stays OPTIONAL, deliberately. The Go writer now always emits it, but every
   * record already on disk predates that writer and genuinely lacks the field —
   * making it required would reject the entire existing corpus, including the
   * history the calibration loop backfills from via
   * `stages[*].model_selection.model`.
   */
  model: z.string().optional(),
  // `model_source` used to sit here, a second copy of the model_selection
  // vocabulary on the token block. It had no writer in any language — Go's
  // V2StageTokens has no such field — and zero occurrences across the real
  // corpus, so it was pure vocabulary surface area to drift against. Deleted
  // with #446; the one attribution lives at
  // `HistoryStageDetailSchema.model_selection.source` below.
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
   * Resolution step that produced `cost_usd` (Issue #3228).
   *
   * - `'native'`   — vendor-emitted cost (Claude `total_cost_usd`).
   * - `'computed'` — derived from the rate-card pricing table; the only path
   *                  that produces a non-zero cost for non-Claude adapters
   *                  prior to #3228 every non-Claude stage reported `0`).
   * - `'unknown'`  — adapter+model has no pricing entry; reported `cost_usd`
   *                  is `0` to make it impossible to silently undercount.
   * - `'deterministic'` — no model was dispatched at all (Issue #890): the
   *                  pipeline-start/pipeline-finish bookends and the
   *                  deterministic execution paths of pr-create / pr-merge
   *                  run compiled Go and spend nothing, so `cost_usd` is an
   *                  EXACT `0`, not a placeholder. `cost_unstamped` is never
   *                  set alongside it — the same carve-out that already
   *                  applies to a genuinely free local-provider run.
   *
   * Optional for backwards-compat with pre-#3228 JSONL records. Reader-side
   * normalization treats undefined as `'native'` only when `cost_usd > 0` —
   * that was the only path that ever produced a non-zero cost pre-#3228.
   *
   * Go is the sole writer (`internal/state/cost_source.go`); this enum is the
   * only validator. `TestCostSourcesPinnedToTSSchema` reads THIS literal at
   * test time and fails if the two vocabularies drift, so keep it an inline
   * `z.enum([...])` on one line rather than a reference to another module.
   */
  cost_source: z.enum(["native", "computed", "unknown", "deterministic"]).optional(),
  /**
   * Mirrors Go `state.V2StageTokens.CostUnstamped` (Issue #585, #588): true
   * when `cost_usd` is a placeholder `0` because the serving (provider,
   * model) pair could not be resolved against the pricing registry — never
   * set for a genuinely free local-provider ($0) run. OR'd across every
   * CompleteStage occurrence folded into this entry, so one unresolved
   * attempt taints the accumulated `cost_usd` even if a later retry on the
   * same stage priced cleanly. Optional/absent on pre-#585 records — readers
   * must treat absence as "not known to be unstamped", not "stamped".
   */
  cost_unstamped: z.boolean().optional(),
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
  /**
   * Structured terminal-kind constant (Issue #9), e.g. "validation_error".
   * Set only on some "fail" results whose failure shape maps cleanly onto an
   * existing TerminalKind* constant; absent otherwise and on all pre-#9
   * records. Readers fall back to prose classification via
   * classifyTerminalKind/resolveTerminalKind when absent.
   */
  terminal_kind: z.string().optional(),
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
      /**
       * How the stage ended up on the model it ran (#446).
       *
       * DERIVED, never re-listed: `MODEL_SELECTION_SOURCES` in the SDK is the
       * single authority for this vocabulary and Go — the only writer of this
       * field — mirrors it in internal/state/model_selection_source.go under a
       * cross-language pin. Six independent copies had drifted three ways and
       * listed nine values no writer could emit, which cost every real record
       * its strict parse.
       */
      source: z.enum(MODEL_SELECTION_SOURCES),
      /**
       * The RAW escalation cause behind `source: "escalation"` (#463).
       *
       * DELIBERATELY NOT AN ENUM, and deliberately not a member of
       * `MODEL_SELECTION_SOURCES`. `source` is a closed, strictly-validated
       * vocabulary shared with the SDK; every upward escalation reason
       * collapses onto its single `"escalation"` member, and nothing else on
       * the record carried the reason — so a stage that FAILED, one that
       * produced NO OUTPUT and one that STALLED over budget were the same
       * record forever. This sibling keeps the cause without widening the
       * vocabulary, which is what lets attribution stay closed.
       *
       * Written by Go (`V2ModelSelect.EscalationReason`), from the
       * `EscalationReasons` closure list in internal/state/runtime_state.go,
       * and pinned to that list by
       * `TestEscalationReasonSiblingPinnedToExecutionHistorySchema`. Absent for
       * every other `source`, the model-unavailable downgrade included — that
       * one has its own `source` member.
       */
      escalation_reason: z.string().optional(),
      confidence: z.number().min(0).max(1).optional(),
      complexity: z.string().optional(),
      /**
       * model_routing.mode (manual | automatic | hybrid) active when this
       * stage's model was resolved (Issue #580, resolves #462). Distinguishes
       * an operator-pinned model (manual) from a router-chosen one
       * (automatic/hybrid) — `source` alone cannot: under
       * `model_routing.mode: manual` every stage's source still reads
       * "scheduler", so routing analytics that filter on source counted every
       * pinned record as an automatic selection. Go is the sole writer
       * (internal/orchestrator/dispatch_envelope.go's
       * resolveDispatchSelectionMode, mirroring dispatch_routing.go's
       * modelRoutingMode exactly). Absent on records emitted before #580.
       *
       * DERIVED, never re-listed: `ModelRoutingModeSchema`
       * (../config/schema.ts) is the single cross-language authority for this
       * vocabulary — the same enum `model_routing.mode` config validation
       * already uses — pinned against Go's state.ModelRoutingModes
       * (internal/state/model_selection_envelope.go) by
       * TestModelSelectionModePinnedToModelRoutingModeSchema. A hand-relisted
       * literal here is exactly the #446 drift class the effort and thinking
       * fields above already guard against.
       */
      mode: ModelRoutingModeSchema.optional(),
      /**
       * The EFFORT_LEVELS rung actually in force for this dispatch (Issue
       * #580, absorbs #434).
       *
       * DERIVED from EFFORT_LEVELS, never re-listed — the #434 drift class
       * was a hand-copied `z.enum(["low", "medium", "high"])` that fell two
       * rungs behind the ladder and was never written by any Go site. Go
       * writes this field only where it has direct dispatch-time evidence
       * (today: the grok adapter's NIGHTGAUGE_GROK_EFFORT env var, validated
       * against the same ladder) — absent for every other adapter, which is
       * resolved entirely on this (TypeScript) side with no signal threaded
       * back to Go yet.
       */
      effort: z.enum(EFFORT_LEVELS).optional(),
      /**
       * The "on"/"off" reasoning state actually in force for this dispatch
       * (Issue #580, spike #568 §3's canonical binary thinking axis).
       * Go-written, derived from the resolved model's registry
       * `behavior.thinking_default` and the Claude Code
       * `CLAUDE_CODE_DISABLE_THINKING` interlock (#76). Absent when the model
       * declares no default. Pinned against Go's `ThinkingStates`
       * (internal/state/model_selection_envelope.go) by
       * internal/state/model_selection_envelope_test.go.
       */
      thinking: z.enum(["on", "off"]).optional(),
      /**
       * The adapter that served this stage (Issue #580) — a self-contained
       * mirror of `HistoryStageTokenUsageSchema.adapter` (#3224) so a
       * `model_selection` block does not require cross-referencing the
       * tokens block to know which provider ran it.
       */
      adapter: ExecutionAdapterSchema.optional(),
      /**
       * The concrete model id the CLI's own stream reported (Issue #580),
       * independent of `model` above — `model` may still be an unresolved
       * tier band ("sonnet") when the stream never reported anything more
       * specific. Absent means the stream reported nothing (#299/#397
       * empty-means-undetermined) — never a guess or a copy of `model`.
       */
      served_model: z.string().optional(),
      /**
       * The served-envelope analogues of `served_model` (#606): the raw
       * executor report of what the last-mile translation actually
       * dispatched, independent of `effort`/`thinking` above (which mirror
       * the request and are re-recorded onto the served value when the two
       * diverge, exactly like `model`). Absent means honestly-unreported —
       * never a guess or a copy of the requested value.
       *
       * OPEN VOCABULARY, exactly like `served_model` above (#637). All three
       * served fields are free strings because the producer treats all three
       * identically: Go types them `string` and records whatever the executor
       * reported, verbatim, with no validation against EFFORT_LEVELS or the
       * thinking axis (`ServedEffort`/`ServedThinking` in
       * internal/state/history.go, written by `RecordStageServedEffort` /
       * `RecordStageServedThinking`, which reject only ""). That is
       * deliberate on the producer side — a served value is first-hand
       * evidence of what the last mile dispatched, and normalizing an
       * unrecognized rung would turn observed evidence into a guess
       * (internal/state/history_test.go's
       * TestBuildV2Record_ServedEffortOpenVocabulary pins it).
       *
       * The consumer must therefore tolerate what the producer can emit. A
       * strict `z.enum` here fails the parse of the ENTIRE record over one
       * off-vocabulary served value, taking every unrelated field down with
       * it — the same value in `served_model` costs nothing.
       *
       * This says nothing about the REQUESTED `effort`/`thinking` fields
       * above: those are the pipeline's own resolved vocabulary, closed by
       * construction and derived from EFFORT_LEVELS, and they stay strict.
       * The canonical rungs remain EFFORT_LEVELS — the adapter boundary
       * normalizes one-way into them before reporting (#523) — this is a
       * tolerance posture, not a vocabulary change.
       */
      served_effort: z.string().optional(),
      served_thinking: z.string().optional(),
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
 *  - `github_rate_limited` — GitHub throttled a `gh` call mid-stage (secondary rate limit, emptied primary bucket, or 429); transient, short per-issue backoff, no global cooldown (#1391)
 *  - `git_transport_auth_failed` — a git or forge transport refused the credentials the machine offered (go-git's `invalid auth method` against an SSH remote, `Permission denied (publickey)`, `could not read Username` with prompts disabled, the forge API's `Bad credentials`); environmental, and matched above `premature_turn_end` because the post-condition reason retains that rule's symptom phrase (#878)
 *  - `premature_turn_end` — stage exited 0 but its post-condition gate reported no state change; agent ended its turn on a promise (#74)
 *  - `dev_produced_no_changes` — feature-dev's gate found the stage workspace empty (clean tree, branch level with base) despite the dev context reporting changed files; work was done somewhere the pipeline never reads (#202)
 *  - `dev_handoff_missing` — the inverse: the dev context is absent or empty and git finds the changed files right there; the stage did the work and ended without writing its handoff, so the work must be preserved rather than re-derived (#223)
 *  - `containment_breach` — the write-containment check found the stage wrote into a repository it does not own; it exits 0 and reports success, so nothing else in the chain marks it failed (#129, classified in #230)
 *  - `adapter_auth_failed` — pipeline-start adapter auth gate refused to launch (probe timed out after retry, or logged out); retryable infra, no cascade/lifetime-cap (#312)
 *  - `no_changes_produced` — pr-create's deterministic fallback confirmed zero commits ahead of base; genuinely nothing to open a PR for, e.g. a dispatched human-only issue (#317)
 *  - `validation_failed` — feature-validate honestly failed its quality gates (validation_status="failed"); organic implementation failure, not a subagent crash (#326)
 *  - `branch_forked` — the run's branch diverged from its remote (killed mid-push, or an operator pushed to it); every push is rejected non-fast-forward and no retry clears it (#163)
 *  - `commit_orphaned` — a killed stage's commit landed on the wrong branch (a stray `temp-pre-push-<n>` left by a SIGKILL bypassing the pre-push restore-defer) and feature-validate's branch-identity self-heal could not recover it; unrecoverable by retry, needs human action (#266)
 *  - `stage_context_unreadable` — a gate could not read a file the stage's contract says it wrote (context, plan_file, gate-metrics.jsonl) for a reason other than absence; filesystem fault (#1237)
 *  - `dev_build_verification_missing` — feature-dev's context has no build_verification object; the skill skipped its verification step (#1237)
 *  - `dev_build_verification_failed` — feature-dev ran its build and recorded status=failed; organic (#1237)
 *  - `dev_tests_failed` — feature-dev's own test run recorded failures; organic (#1237)
 *  - `pr_merge_lookup_failed` — pr-merge's gate could not establish the PR's state; infrastructure (#1237)
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
  ORCHESTRATOR_CRASH_TERMINAL_KIND,
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
  "github_rate_limited", // Issue #1391 — GitHub throttled a `gh` call mid-stage (secondary rate limit / emptied primary bucket / 429); transient, short per-issue backoff, no global cooldown
  "model_unavailable", // Issue #42 — API rejected the selected model (not on plan / unknown / model usage cap); triggers tier-downgrade fallback
  "git_transport_auth_failed", // Issue #878 — a git or forge transport refused the credentials the machine offered (go-git's `invalid auth method` against an SSH remote, `Permission denied (publickey)`, `could not read Username` with prompts disabled, the forge API's `Bad credentials`); environment class, unclearable by rerun or a stronger model
  "premature_turn_end", // Issue #74 — stage exited 0 but its gate reported no state change (agent ended its turn on a promise)
  "dev_produced_no_changes", // Issue #202 — feature-dev's gate found the stage workspace empty despite a truthful dev context; work landed where the pipeline never reads
  "dev_handoff_missing", // Issue #223 — the inverse of the above: the dev context is absent or empty and git finds the changed files in the workspace; the stage did the work and ended without writing its handoff
  "containment_breach", // Issue #230 — the write-containment check (#129) found the stage wrote into a repository it does not own; it exits 0 and reports success, so nothing else marks it failed
  "adapter_auth_failed", // Issue #312 — adapter auth pre-flight refused to launch (probe timed out after retry, or logged out); retryable infra
  "no_changes_produced", // Issue #317 — pr-create's deterministic fallback confirmed zero commits ahead of base; genuinely nothing to open a PR for
  "validation_failed", // Issue #326 — feature-validate honestly failed its quality gates (validation_status="failed"); organic implementation failure
  "branch_forked", // Issue #163 — branch diverged from its remote; pushes rejected non-fast-forward, unrecoverable by retry
  "runaway_progress", // Issue #3783 — progress-based runaway monitor fired; treated like stall_kill
  "pr_merge_unmerged", // Issue #3691 — pr-merge exited cleanly but the PR was not actually merged
  "blocked_dependency", // Issue #305 — scheduler dispatched an issue whose blockedBy dependencies are still open; non-failure deferral
  "architecture_approval_required", // Issue #4098/#4222 — architecture-approval gate halted the run before feature-dev for a human-owned decision
  "not_pipeline_actionable", // Issue #1241 — a stage DECLARED the issue's deliverable is not producible by any pipeline lap (counsel sign-off, an operator-only credential, a human decision); not a failure and not a deferral, so the issue is labelled owner-action and left parked rather than retried
  "validation_inconclusive", // Issue #221 — feature-validate's unit-test tier ran but executed zero tests
  // Declared-but-unmatched, mirroring Go: set structurally (a gate/evidence
  // override), never derived from error text.
  "abandoned_commit", // Issue #191 — a stage committed valid, unmerged work but was killed/crashed before pr-create ran
  "commit_orphaned", // Issue #266 — a killed stage's commit landed on the wrong branch and self-heal could not recover it; unrecoverable by retry
  // Gate-sourced kinds from the #1237 sweep — every KindFail site in the stage
  // gates classifies itself instead of falling through to subagent_crash.
  "stage_context_unreadable", // Issue #1237 — a gate could not read a file the stage's contract says it wrote, for a reason other than absence; filesystem fault
  "dev_build_verification_missing", // Issue #1237 — feature-dev's context has no build_verification object; the skill skipped its verification step
  "dev_build_verification_failed", // Issue #1237 — feature-dev ran its build and recorded status=failed; organic
  "dev_tests_failed", // Issue #1237 — feature-dev's own test run recorded failures; organic
  "pr_merge_lookup_failed", // Issue #1237 — pr-merge's gate could not establish the PR's state (gh failed on every attempt, local git found no merge commit); infrastructure
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
  /**
   * Mirrors Go `state.V2Tokens.CostUnstamped` (Issue #585, #588): the
   * run-level OR of every `per_stage[*].cost_unstamped` entry. True means
   * `estimated_cost_usd` folds in at least one placeholder-zero stage cost
   * and is not a fully priced total. Optional/absent on pre-#585 records.
   */
  cost_unstamped: z.boolean().optional(),
  per_stage: z.record(z.string(), HistoryStageTokenUsageSchema).optional(),
  /** PTC metrics for programmatic vs direct tool call tracking (Issue #1071) */
  ptc_metrics: PTCMetricsSchema.optional(),
});

/**
 * Canonical pipeline-run mode stored in parsed execution history.
 *
 * The retired TypeScript history writer copied the stage mode `"headless"`
 * into this pipeline-level field. At that level it meant the same thing as
 * `"automatic"`; accepting it as input and normalizing it here keeps the
 * current output vocabulary aligned with PipelineExecutionModeSchema rather
 * than exposing a third pipeline mode to every history consumer (#460).
 */
const ExecutionHistoryPipelineModeSchema = z
  .union([PipelineExecutionModeSchema, z.literal("headless")])
  .transform((mode) => (mode === "headless" ? "automatic" : mode));

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
  /**
   * The branch the run executed on, or "" when none could be determined (#397).
   *
   * `.default("")` rather than a bare `z.string()`: the output type is still
   * `string`, but a record that omits the key is normalized instead of
   * REJECTED. Rejection is not a smaller failure than a wrong branch — it drops
   * the entire record to the lenient raw-cast fallback in
   * executionHistoryReader, which skips the #3228 cost_source backfill. Our Go
   * writer always emits the key (no omitempty on V2RunRecord.Branch); this
   * covers every other producer.
   */
  branch: z.string().default(""),
  base_branch: z.string(),
  execution_mode: ExecutionHistoryPipelineModeSchema,
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
  /**
   * The branch the run executed on, or "" when none could be determined (#397).
   * See the V1 schema's note above for why this is `.default("")` and not a
   * bare `z.string()`. V3 extends this object and does not redeclare the field.
   */
  branch: z.string().default(""),
  base_branch: z.string(),
  execution_mode: ExecutionHistoryPipelineModeSchema,
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
      // One or more stages ended in `failed` status. The classifier emits this
      // instead of a success outcome whenever the run's own stage list
      // contradicts one (#1109) — the durable record, the calibration corpus
      // and the health snapshot must agree with the gates.
      "partial",
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

  /**
   * The pre-flight projection this run was dispatched against (#1213).
   *
   * Optional because it is absent on every run the gate did not estimate,
   * including the whole pre-#1213 corpus. Absent means NOT ESTIMATED — never
   * "estimated at zero", which would become a division by zero in the accuracy
   * report or a 0-cost "perfect" prediction.
   *
   * `usd` is absent when `source` is "unpriced": the provider serves a band the
   * registry cannot price, so no number was published.
   */
  budget_estimate: z
    .object({
      usd: z.number().min(0).optional(),
      source: z.string().optional(),
      provider: z.string().optional(),
      ceiling_usd: z.number().min(0).optional(),
    })
    .optional(),

  /**
   * Insertions + deletions captured against the PR base at pr-create exit
   * (#369). Optional because legacy records and runs that never reached the
   * pre-merge measurement seam have no honest value. Zero is a measured value.
   */
  actual_lines_changed: z.number().int().min(0).optional(),

  /**
   * Predicted-vs-actual routing pairs mirrored from outcomes.jsonl (#304,
   * #369). Empty/absent halves are unknown and excluded from calibration
   * denominators; actual_size is derived only from actual_lines_changed.
   */
  outcome_prediction: z
    .object({
      predicted_size: z.string(),
      actual_size: z.string().optional(),
      predicted_model: z.string(),
      actual_model: z.string().optional(),
    })
    .optional(),

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
  /**
   * The failure text behind `terminal_failure_kind` (#1329): the failed
   * stage's error, or the dispatcher's forwarded reason for a run that never
   * started a stage. Bounded (2048 runes, tail-truncated) by the Go writer.
   */
  terminal_failure_detail: z.string().optional(),
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
 * Any valid execution history record (v1, v2, or v3 run; v1 or v2 outcome)
 *
 * Uses z.union instead of z.discriminatedUnion because we have two
 * discriminator dimensions (record_type + schema_version). Run readers
 * dispatch by version and retain this ordering for general schema consumers.
 */
export const ExecutionHistoryRecordSchema = z.union([
  ExecutionHistoryRunRecordV3Schema,
  ExecutionHistoryRunRecordV2Schema,
  ExecutionHistoryRunRecordSchema,
  ExecutionOutcomeRecordV2Schema,
  ExecutionOutcomeRecordSchema,
]);
export type ExecutionHistoryRecord = z.infer<typeof ExecutionHistoryRecordSchema>;
