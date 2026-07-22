/**
 * ExecutionHistoryWriter - JSONL writer for pipeline execution history
 *
 * Static utility class following LogFileWriter pattern: no state, no VSCode
 * dependency, testable from SDK context.
 *
 * Writes one JSON object per line (JSONL format) to daily files:
 *   .nightgauge/pipeline/history/YYYY-MM-DD.jsonl
 *
 * Non-critical: failures log warnings, never break the pipeline.
 *
 * @see Issue #649 - Execution History Persistence
 * @see docs/ARCHITECTURE.md for utility patterns
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";

import {
  ExecutionHistoryRunRecordV2Schema,
  ExecutionOutcomeRecordV2Schema,
  type ExecutionHistoryRunRecordV2,
  type ExecutionHistoryRecord,
  type HistoryStageDetail,
  type ToolCallRecord,
} from "../schemas/executionHistory";
import type { ProactiveEscalationRecord } from "../schemas/pipelineState";
import type { StallEvent } from "../schemas/stallEvents";
import type { ExecutionAdapter } from "../config/schema";
import { classifyFailureCategory, PIPELINE_STAGE_ORDER } from "@nightgauge/sdk";

/**
 * Idempotency key for a run record (Issue #313): the stable run_id when present
 * (threaded through the runtime as a UUID v7), else issue_number + started_at.
 * Two writers reacting to the SAME completion produce the same key; distinct
 * runs never collide. Mirrors the Go writer's runRecordKey.
 */
function runRecordKey(record: ExecutionHistoryRecord): string {
  const runId = (record as { run_id?: unknown }).run_id;
  if (typeof runId === "string" && runId !== "") return `run:${runId}`;
  const startedAt = (record as { started_at?: unknown }).started_at ?? "";
  return `issue:${record.issue_number}|${String(startedAt)}`;
}

/**
 * How much stage-level data a run record carries (Issue #313). A late
 * finalizer's skeleton (empty stages) scores 0 and can never supersede a real
 * record; any run that executed scores >= 1. Mirrors the Go writer's
 * recordRichness.
 */
function stageRichness(record: ExecutionHistoryRecord): number {
  const stages = (record as { stages?: Record<string, unknown> }).stages;
  return stages ? Object.keys(stages).length : 0;
}

/**
 * Idempotency key for an already-projected index entry (Issue #313), mirroring
 * runRecordKey so the index can be de-duplicated by run identity.
 */
function indexEntryKey(entry: HistoryIndexEntry): string {
  if (entry.run_id) return `run:${entry.run_id}`;
  return `issue:${entry.issue_number}|${entry.started_at}`;
}

/**
 * Batch metrics context for attributed token tracking (Issue #805).
 * Previously in types/batch.ts — inlined here since batch mode was removed
 * and this is the only remaining consumer of the type signature.
 */
interface BatchMetricsContext {
  batchId: number;
  batchIssueNumbers: number[];
  attributionMethod: "proportional" | "equal" | "full-cost-to-each";
  batchTotalUsage: {
    inputTokens: number;
    outputTokens: number;
    cacheReadTokens: number;
    cacheCreationTokens: number;
    costUsd: number;
  };
}

/**
 * Entry in the history index — lightweight summary for list display (Issue #1007)
 */
export interface HistoryIndexEntry {
  issue_number: number;
  /**
   * Stable run identifier (Issue #313) — mirrors the run record's run_id so the
   * index can be de-duplicated by run identity (one entry per run). Additive
   * and optional: entries written before this field, and records with no
   * assigned run_id, omit it.
   */
  run_id?: string;
  title: string;
  outcome: "complete" | "failed" | "cancelled";
  outcome_type?:
    | "productive"
    | "verify-and-close"
    | "already-resolved"
    | "budget-ceiling"
    | "shipped-but-overbudget"
    | "skill-no-op"
    | "blocked"
    | "deferred";
  /** True when this run resumed a previously-failed pipeline (Issue #1261) */
  is_recovery?: boolean;
  /**
   * True when this run used the legacy supercharge envelope (Opus + max effort).
   * @deprecated Issue #3009 — prefer `performance_mode === "maximum"`. Kept
   * additively for one release so external consumers keep working.
   */
  is_supercharge?: boolean;
  /** Active performance mode for this run (Issue #3009). */
  performance_mode?: "efficiency" | "elevated" | "maximum" | "frontier";
  /** Active focus lens name when this run started, if any (Issue #2460) */
  focus_lens_active?: string;
  cost_usd: number;
  total_input_tokens: number;
  total_output_tokens: number;
  total_cache_read_tokens: number;
  total_cache_creation_tokens: number;
  duration_ms: number;
  stage_count: number;
  started_at: string;
  recorded_at: string;
  labels?: string[];
  size?: string | null;
  type?: string | null;
  branch: string;
}

/**
 * History index structure — stored at history/index.json (Issue #1007)
 */
export interface HistoryIndex {
  schema_version: string;
  updated_at: string;
  total_runs: number;
  entries: HistoryIndexEntry[];
}

/**
 * Optional issue metadata for label recording in execution history (Issue #844).
 * Passed from HeadlessOrchestrator which caches issue context during pipeline run.
 */
export interface IssueMetadataInput {
  labels: string[];
  size?: string | null;
  type?: string | null;
  priority?: string | null;
}

/**
 * Input type for buildRunRecord — accepts the PipelineState shape from
 * PipelineStateService (which includes 'deferred' stage status).
 * Deliberately decoupled from the Zod schema to avoid circular dependency
 * and to accept the superset type that getState() returns.
 */
interface PipelineStateInput {
  issue_number: number;
  title: string;
  branch: string;
  base_branch?: string;
  started_at: string;
  execution_mode?: string;
  stages: Record<
    string,
    {
      status: string;
      started_at?: string;
      completed_at?: string;
      duration_ms?: number;
      error?: string;
      execution_mode?: "headless" | "interactive";
      auto_retry_count?: number;
      manual_retry_count?: number;
      model_selection?: {
        model: string;
        source:
          | "env"
          | "config"
          | "stage-default"
          | "auto"
          | "experiment"
          | "default"
          | "feedback-escalation"
          | "user-override";
        confidence?: number;
        complexity?: string;
        mode?: "manual" | "automatic" | "hybrid";
        effort?: "low" | "medium" | "high";
      };
      skip_reason?: string;
      /** Context handoff file size in bytes (Issue #1009) */
      context_file_size_bytes?: number;
      /** Performance mode active at stage start (Issue #3215) */
      performance_mode?: "efficiency" | "elevated" | "maximum" | "frontier";
      /** Adapter that executed this stage (Issue #3224) */
      adapter?: ExecutionAdapter;
      /** Source step that produced the resolved adapter (Issue #3223) */
      adapter_source?:
        "env" | "stage-config" | "global-config" | "auto-router" | "fallback" | "default";
      /**
       * Adapters tried at stage start when fallback walked (Issue #3231).
       *
       * Length ≥ 2 — primary failed prereq and one or more fallback
       * candidates were attempted. Length 1 (or absent) means no fallback
       * occurred. Mirrored onto the per-stage history record's
       * `adapter_fallback_chain_used` field for analytics.
       */
      adapter_fallback_chain_used?: ExecutionAdapter[];
    }
  >;
  tokens: {
    total_input: number;
    total_output: number;
    total_cache_read: number;
    total_cache_creation: number;
    estimated_cost_usd: number;
    per_stage?: Partial<
      Record<
        string,
        {
          input: number;
          output: number;
          cache_read: number;
          cache_creation: number;
          cost_usd: number;
          /**
           * Resolution step that produced `cost_usd` (Issue #3228). Mirrors
           * `PipelineStageTokens.cost_source`; passed through unchanged into
           * the emitted history record.
           */
          cost_source?: "native" | "computed" | "unknown";
        }
      >
    >;
    /** PTC metrics for programmatic vs direct tool call tracking (Issue #1071) */
    ptc_metrics?: {
      total_tool_calls: number;
      programmatic_calls: number;
      direct_calls: number;
      programmatic_ratio: number;
      estimated_tokens_saved: number;
      code_execution_count: number;
      container_reuse_count: number;
    };
  };
}

/** Default history directory relative to workspace root */
const HISTORY_DIR = ".nightgauge/pipeline/history";

/** Default retention period in days */
const DEFAULT_RETENTION_DAYS = 90;

type HistoryStageStatus = "complete" | "failed" | "skipped" | "pending" | "deferred";

/** Map pipeline stage status to a valid history status */
function mapStageStatus(status: string): HistoryStageStatus {
  switch (status) {
    case "complete":
    case "failed":
    case "skipped":
    case "pending":
    case "deferred":
      return status;
    case "running":
      // At write time (pipeline-finish), running → complete
      return "complete";
    default:
      return "pending";
  }
}

export class ExecutionHistoryWriter {
  /**
   * Stores the last Zod validation error string from appendRecord, if any.
   * Callers (e.g., HeadlessOrchestrator) read this to log schema warnings
   * without requiring a thrown error or side-channel any cast.
   */
  static lastValidationError: string | null = null;

  /**
   * Append a validated record to the JSONL file for today's date.
   *
   * @param workspaceRoot - Absolute path to repository root
   * @param record - An ExecutionHistoryRunRecord or ExecutionOutcomeRecord
   */
  static async appendRecord(
    workspaceRoot: string,
    record: ExecutionHistoryRecord
  ): Promise<boolean> {
    try {
      // #307 identity-integrity gate — runs BEFORE the lenient #2249
      // "write-anyway" path below. A run record whose `repo` or `run_id` is
      // PRESENT but empty (explicit null or "") is proof of the concurrent
      // cross-contamination bug: it was assembled from shared/cleared per-run
      // state and mis-routed into a sibling repo's history JSONL (live dogfood
      // 2026-07-19: three null-identity "issue 209" rows landed in another
      // repo's history within 4s while the authoritative record — with a real
      // run_id — landed correctly). A record without identity is evidence of
      // the bug, never legitimate telemetry, so reject it loudly rather than
      // let the write-anyway policy persist it. Absent (undefined) fields are
      // the normal shape of the current builder and are deliberately NOT
      // rejected here — only a present-but-empty identity is.
      if (record.record_type === "run") {
        const repoVal = (record as { repo?: unknown }).repo;
        const runIdVal = (record as { run_id?: unknown }).run_id;
        const emptyRepo = repoVal === null || repoVal === "";
        const emptyRunId = runIdVal === null || runIdVal === "";
        if (emptyRepo || emptyRunId) {
          const msg =
            `[Nightgauge] Rejecting run record with empty identity ` +
            `(issue #${record.issue_number}, repo=${JSON.stringify(repoVal)}, ` +
            `run_id=${JSON.stringify(runIdVal)}) — cross-contamination guard (#307)`;
          console.error(msg);
          ExecutionHistoryWriter.lastValidationError = msg;
          return false;
        }
      }

      // Validate record against v2 schema before writing.
      // Warn on validation failures but STILL WRITE the record — schema
      // strictness should not prevent the dashboard from receiving data.
      // Issue #2249: records were silently dropped, leaving dashboard stale.
      if (record.record_type === "run") {
        const validation = ExecutionHistoryRunRecordV2Schema.safeParse(record);
        if (!validation.success) {
          const issues = validation.error.issues
            .map(
              (i) =>
                `${i.path.join(".")}: ${i.message} (code: ${i.code}, received: ${JSON.stringify((i as { received?: unknown }).received)})`
            )
            .join("; ");
          console.warn(
            `[Nightgauge] Run record has schema issues (issue #${record.issue_number}), writing anyway: ${issues}`
          );
          // Attach issues string so callers can log via extension output channel
          ExecutionHistoryWriter.lastValidationError = issues;
          // Continue to write — do NOT return false
        }
      } else {
        const validation = ExecutionOutcomeRecordV2Schema.safeParse(record);
        if (!validation.success) {
          const issues = validation.error.issues
            .map((i) => `${i.path.join(".")}: ${i.message} (code: ${i.code})`)
            .join("; ");
          const msg = `[Nightgauge] Invalid outcome record, skipping write: ${issues}`;
          console.warn(msg);
          console.error(msg);
          return false;
        }
      }

      const historyDir = this.getHistoryDir(workspaceRoot);
      const filename = this.getFilenameForDate(new Date(record.recorded_at));
      const filePath = path.join(historyDir, filename);

      // #313 run-record idempotency. The Go binary's pipeline.notifyComplete
      // handler is the authoritative run-record writer, but several TS paths
      // (dashboard-sync backup, the Go-scheduler pipeline.complete handler) can
      // also fire for the same completion. Before appending a run record, skip
      // it when the day's file already holds a record for this run that is at
      // least as complete: first full write wins, and a degraded skeleton can
      // never bury the authoritative record. Only a strictly richer record
      // (more stages) is allowed through, as an upgrade.
      if (record.record_type === "run") {
        const existing = await ExecutionHistoryWriter.existingRunRichness(
          filePath,
          runRecordKey(record)
        );
        if (existing !== null && stageRichness(record) <= existing) {
          return true; // already recorded by an equal-or-richer writer
        }
      }

      // Serialize as single JSON line + newline
      const line = JSON.stringify(record) + "\n";

      // Ensure directory exists
      await fs.mkdir(historyDir, { recursive: true });

      // Append (atomic per line on POSIX)
      await fs.appendFile(filePath, line, "utf-8");

      // Update history index for run records (Issue #1007)
      if (record.record_type === "run") {
        await this.updateIndex(workspaceRoot, record as ExecutionHistoryRunRecordV2);
      }
      return true;
    } catch (error) {
      // Non-critical: warn but never throw
      const msg = `[Nightgauge] Failed to write execution history: ${error}`;
      console.warn(msg);
      console.error(msg);
      return false;
    }
  }

  /**
   * Build a v2 run record from the current PipelineState snapshot.
   *
   * Always produces schema_version '2' records with required files/routing
   * fields and optional outcome_type/tool_calls fields.
   *
   * When batchContext is provided (Issue #805), the record includes batch metadata
   * with attributed token values and canonical batch totals.
   *
   * When issueMetadata is provided (Issue #844), the record includes issue labels
   * and extracted size/type/priority fields for pipeline cost analysis.
   *
   * @param state - PipelineState snapshot
   * @param batchContext - Optional batch metrics context for attribution
   * @param issueMetadata - Optional issue metadata with labels (Issue #844)
   * @param options - Optional v2 fields (outcome_type, tool_calls)
   */
  static buildRunRecord(
    state: PipelineStateInput,
    batchContext?: BatchMetricsContext,
    issueMetadata?: IssueMetadataInput,
    options?: {
      outcome_type?:
        | "productive"
        | "verify-and-close"
        | "already-resolved"
        | "budget-ceiling"
        | "shipped-but-overbudget"
        | "skill-no-op"
        | "blocked"
        | "deferred";
      tool_calls?: ToolCallRecord[];
      files?: { read_count: number; written_count: number };
      routing?: {
        complexity_score: number;
        path: string;
        skip_stages: string[];
      };
      /** True when this run resumed a previously-failed pipeline (Issue #1261) */
      is_recovery?: boolean;
      /**
       * @deprecated Issue #3009 — prefer `performance_mode === "maximum"`.
       * Retained additively for one release.
       */
      is_supercharge?: boolean;
      /** Active performance mode for this run (Issue #3009). */
      performance_mode?: "efficiency" | "elevated" | "maximum" | "frontier";
      /** Proactive model escalations applied during this pipeline run (Issue #1394) */
      proactive_escalations?: ProactiveEscalationRecord[];
      /** Zod schema validation errors per stage, captured for skill amendment detection */
      stageValidationErrors?: Map<string, NonNullable<HistoryStageDetail["validation_errors"]>>;
      /** Context schema repair attempts per stage (Issue #2552) */
      stageRepairAttempts?: Map<
        string,
        { attempted: boolean; succeeded: boolean; attempts_count: number }
      >;
      /** Active focus lens state at pipeline start (Issue #2460) */
      focus_lens_active?: { lens: string; set_at?: string; set_by?: string };
      /** Stall events accumulated per stage during execution (Issue #2652) */
      stageStallEvents?: Map<string, StallEvent[]>;
      /**
       * Execution-path decision per stage (Issue #297). The orchestrator records
       * whether a deterministic-first hook completed the stage (`"deterministic"`)
       * or punted to the LLM skill (`"llm"`, with the machine-readable
       * `puntReason`). Populates `execution_path` + `punt_reason` on the history
       * stage record so the decision is observable without session-log
       * archaeology — the TS-path counterpart of Go's `RecordExecutionPath` /
       * `RecordStagePuntReason` → `BuildV2Record`.
       */
      stageExecutionPaths?: Map<string, { path: "deterministic" | "llm"; puntReason?: string }>;
      /**
       * Default adapter to record on per-stage tokens when the stage state did
       * not supply one (Issue #3224). The orchestrator passes the active global
       * adapter here; once the per-stage adapter resolver (#3221) lands, every
       * stage will populate `state.stages[stage].adapter` and this fallback
       * stops being read.
       */
      defaultAdapter?: ExecutionAdapter;
      /** Pipeline run UUID for platform deduplication vs. real-time events (#3558) */
      run_id?: string;
    }
  ): ExecutionHistoryRunRecordV2 {
    const now = new Date().toISOString();
    const startedAt = state.started_at;
    const completedAt = now;
    const totalDurationMs = new Date(completedAt).getTime() - new Date(startedAt).getTime();

    // Determine overall outcome from stage statuses.
    // Defense-in-depth (Issue #2994): outcome is "complete" only when every
    // canonical stage is accounted for — present in state.stages with terminal
    // status (complete/skipped/deferred), or listed in routing.skip_stages.
    // Otherwise the run was cut short and is "cancelled" — never "complete".
    const stageValues = Object.values(state.stages);
    const hasFailed = stageValues.some((s) => s.status === "failed");
    const skipStages = new Set(options?.routing?.skip_stages ?? []);
    const isStageTerminal = (s: { status: string } | undefined) =>
      s != null && (s.status === "complete" || s.status === "skipped" || s.status === "deferred");
    const allStagesAccountedFor = PIPELINE_STAGE_ORDER.every(
      (stage) => skipStages.has(stage) || isStageTerminal(state.stages[stage])
    );
    let outcome: "complete" | "failed" | "cancelled";
    if (hasFailed) {
      outcome = "failed";
    } else if (allStagesAccountedFor) {
      outcome = "complete";
    } else {
      outcome = "cancelled";
    }

    // Map PipelineState stages to history stage details
    const stages = {} as ExecutionHistoryRunRecordV2["stages"];
    for (const [stageName, stageState] of Object.entries(state.stages)) {
      // Map pipeline status to history status (running → complete at write time)
      const historyStatus = mapStageStatus(stageState.status);
      const failure_category =
        historyStatus === "failed" && stageState.error
          ? classifyFailureCategory(stageState.error, stageName)
          : undefined;

      // Merge repair metadata if present (Issue #2552)
      const repairData = options?.stageRepairAttempts?.get(stageName);

      const stageStallEvents = options?.stageStallEvents?.get(stageName);
      // Derive stall_killed: true when any stall event has action "kill" (Issue #2871)
      const stallKilledFlag =
        stageStallEvents && stageStallEvents.some((e) => e.action === "kill") ? true : undefined;
      // Execution-path decision (Issue #297): recorded per-stage by the
      // orchestrator's deterministic-first hooks so the history stage record
      // carries WHICH path ran and, on a punt, WHY.
      const execPathRecord = options?.stageExecutionPaths?.get(stageName);
      const executionPath = execPathRecord?.path;
      const puntReason = execPathRecord?.puntReason;
      stages[stageName as keyof ExecutionHistoryRunRecordV2["stages"]] = {
        status: historyStatus,
        started_at: stageState.started_at,
        completed_at: stageState.completed_at,
        duration_ms: stageState.duration_ms,
        error: stageState.error,
        execution_mode: stageState.execution_mode,
        auto_retry_count: stageState.auto_retry_count,
        manual_retry_count: stageState.manual_retry_count,
        skip_reason: stageState.skip_reason,
        model_selection: stageState.model_selection,
        context_file_size_bytes: stageState.context_file_size_bytes,
        failure_category,
        validation_errors: options?.stageValidationErrors?.get(stageName),
        repair_attempted: repairData?.attempted,
        repair_succeeded: repairData?.succeeded,
        repair_attempts_count: repairData?.attempts_count,
        stall_events:
          stageStallEvents && stageStallEvents.length > 0 ? stageStallEvents : undefined,
        stall_killed: stallKilledFlag,
        performance_mode: stageState.performance_mode,
        execution_path: executionPath,
        punt_reason: puntReason,
      };
    }

    // Build batch metadata if batch context is present (Issue #805)
    const batch = batchContext
      ? {
          batch_id: batchContext.batchId,
          batch_issue_numbers: batchContext.batchIssueNumbers,
          attribution_method: batchContext.attributionMethod,
          batch_total_tokens: {
            total_input: batchContext.batchTotalUsage.inputTokens,
            total_output: batchContext.batchTotalUsage.outputTokens,
            total_cache_read: batchContext.batchTotalUsage.cacheReadTokens,
            total_cache_creation: batchContext.batchTotalUsage.cacheCreationTokens,
            estimated_cost_usd: batchContext.batchTotalUsage.costUsd,
          },
        }
      : undefined;

    // Compute token totals from per-stage breakdown as a fallback for when
    // the top-level accumulators are not yet populated (e.g., interim writes
    // where state.tokens.total_input is still 0 but per_stage has data).
    const perStageValues = state.tokens.per_stage ? Object.values(state.tokens.per_stage) : [];
    const computedInput = perStageValues.reduce((sum, s) => sum + (s?.input ?? 0), 0);
    const computedOutput = perStageValues.reduce((sum, s) => sum + (s?.output ?? 0), 0);
    const computedCacheRead = perStageValues.reduce((sum, s) => sum + (s?.cache_read ?? 0), 0);
    const computedCacheCreation = perStageValues.reduce(
      (sum, s) => sum + (s?.cache_creation ?? 0),
      0
    );
    const computedCost = perStageValues.reduce((sum, s) => sum + (s?.cost_usd ?? 0), 0);

    return {
      schema_version: "2",
      record_type: "run",
      issue_number: state.issue_number,
      title: state.title,
      branch: state.branch,
      base_branch: state.base_branch ?? "main",
      execution_mode: (state.execution_mode as "automatic" | "manual") ?? "automatic",
      started_at: startedAt,
      completed_at: completedAt,
      total_duration_ms: Math.max(0, totalDurationMs),
      outcome,
      labels: issueMetadata?.labels ?? [],
      size: issueMetadata?.size ?? null,
      type: issueMetadata?.type ?? null,
      priority: issueMetadata?.priority ?? null,
      stages,
      tokens: {
        total_input: state.tokens.total_input || computedInput,
        total_output: state.tokens.total_output || computedOutput,
        total_cache_read: state.tokens.total_cache_read || computedCacheRead,
        total_cache_creation: state.tokens.total_cache_creation || computedCacheCreation,
        estimated_cost_usd: state.tokens.estimated_cost_usd || computedCost,
        per_stage: state.tokens.per_stage
          ? (Object.fromEntries(
              Object.entries(state.tokens.per_stage).map(([stage, usage]) => {
                const modelSel = state.stages[stage]?.model_selection;
                // Issue #3224: prefer the per-stage adapter recorded by the
                // resolver (#3221). Fall back to the run-level default the
                // orchestrator passes in. Omit entirely when neither is set so
                // existing dashboards keep treating the field as "unknown".
                const stageAdapter = state.stages[stage]?.adapter ?? options?.defaultAdapter;
                // Issue #3223: per-stage adapter source mirrors `model_source`
                // so dashboards can attribute the routing step (env /
                // stage-config / fallback / global / default). Only set when
                // the resolver actually provided a value.
                const stageAdapterSource = state.stages[stage]?.adapter_source;
                // Issue #3231: persist the fallback audit trail when the
                // walker attempted at least one fallback candidate. Absent
                // for the common primary-success path so existing dashboards
                // keep treating "no field" as "no fallback occurred".
                const stageFallbackChainUsedRaw = state.stages[stage]?.adapter_fallback_chain_used;
                const stageFallbackChainUsed =
                  stageFallbackChainUsedRaw && stageFallbackChainUsedRaw.length >= 2
                    ? stageFallbackChainUsedRaw
                    : undefined;
                // Issue #3228: spread cost_source from the state's per_stage
                // entry. The `...usage` spread above already carries it when
                // present; this explicit reference documents the intent and
                // keeps the conditional shape consistent with the other
                // *_source fields above.
                const costSource = (usage as { cost_source?: "native" | "computed" | "unknown" })
                  .cost_source;
                return [
                  stage,
                  {
                    ...usage,
                    ...(modelSel ? { model: modelSel.model, model_source: modelSel.source } : {}),
                    ...(stageAdapter ? { adapter: stageAdapter } : {}),
                    ...(stageAdapterSource ? { adapter_source: stageAdapterSource } : {}),
                    ...(stageFallbackChainUsed
                      ? { adapter_fallback_chain_used: stageFallbackChainUsed }
                      : {}),
                    ...(costSource ? { cost_source: costSource } : {}),
                  },
                ];
              })
            ) as ExecutionHistoryRunRecordV2["tokens"]["per_stage"])
          : undefined,
        ptc_metrics: state.tokens.ptc_metrics,
      },
      outcome_type: options?.outcome_type,
      tool_calls: options?.tool_calls,
      files: options?.files ?? { read_count: 0, written_count: 0 },
      routing: options?.routing ?? {
        complexity_score: 0,
        path: "unknown",
        skip_stages: [],
      },
      batch,
      is_recovery: options?.is_recovery,
      is_supercharge: options?.is_supercharge,
      performance_mode: options?.performance_mode,
      proactive_escalations: options?.proactive_escalations,
      focus_lens_active: options?.focus_lens_active,
      run_id: options?.run_id,
      recorded_at: now,
    };
  }

  /**
   * Delete history files older than the retention period.
   *
   * @param workspaceRoot - Absolute path to repository root
   * @param retentionDays - Number of days to retain (default: 90)
   */
  static async cleanupOldFiles(
    workspaceRoot: string,
    retentionDays: number = DEFAULT_RETENTION_DAYS
  ): Promise<{ deleted: string[] }> {
    const deleted: string[] = [];
    try {
      const historyDir = this.getHistoryDir(workspaceRoot);
      const cutoff = new Date();
      cutoff.setDate(cutoff.getDate() - retentionDays);
      cutoff.setHours(0, 0, 0, 0);

      let entries: string[];
      try {
        entries = await fs.readdir(historyDir);
      } catch {
        // Directory doesn't exist — nothing to clean
        return { deleted };
      }

      for (const entry of entries) {
        if (!entry.endsWith(".jsonl")) continue;

        // Parse date from YYYY-MM-DD.jsonl filename
        const dateStr = entry.replace(".jsonl", "");
        const fileDate = new Date(dateStr + "T00:00:00Z");
        if (isNaN(fileDate.getTime())) continue;

        if (fileDate < cutoff) {
          await fs.unlink(path.join(historyDir, entry));
          deleted.push(entry);
        }
      }
    } catch (error) {
      console.warn(`[Nightgauge] History cleanup failed: ${error}`);
    }
    return { deleted };
  }

  /**
   * Returns the absolute path to the history directory.
   */
  static getHistoryDir(workspaceRoot: string): string {
    return path.join(workspaceRoot, HISTORY_DIR);
  }

  /**
   * Returns the JSONL filename for the given date.
   *
   * @param date - Date to generate filename for (default: now)
   * @returns Filename like "2026-02-13.jsonl"
   */
  static getFilenameForDate(date?: Date): string {
    const d = date ?? new Date();
    return d.toISOString().split("T")[0] + ".jsonl";
  }

  /**
   * Scan a day's JSONL file for the richest run record already recorded for a
   * given run key (Issue #313). Returns null when the file is absent or holds
   * no record for the key. Backs appendRecord's idempotency so the several TS
   * "backup" writers defer to whatever record — including the Go authoritative
   * one — already landed for this run.
   */
  private static async existingRunRichness(filePath: string, key: string): Promise<number | null> {
    let content: string;
    try {
      content = await fs.readFile(filePath, "utf-8");
    } catch {
      return null; // no file yet → nothing recorded for this run
    }
    if (typeof content !== "string" || content === "") {
      return null; // empty/absent → nothing recorded for this run
    }
    let best: number | null = null;
    for (const line of content.split("\n")) {
      if (!line.trim()) continue;
      let rec: ExecutionHistoryRecord;
      try {
        rec = JSON.parse(line) as ExecutionHistoryRecord;
      } catch {
        continue; // skip malformed lines
      }
      if (rec.record_type !== "run") continue;
      if (runRecordKey(rec) !== key) continue;
      const richness = stageRichness(rec);
      if (best === null || richness > best) best = richness;
    }
    return best;
  }

  /**
   * Collapse duplicate run records for a run to the single richest one (Issue
   * #313): most stages wins, ties keep the later record, and a skeleton (empty
   * stages) never wins over a real record. Used by the index rebuild so
   * reconstructing from the append-only JSONL source of truth yields exactly
   * one entry per run. Mirrors the Go reader's dedupeRichestByKey.
   */
  static dedupeRichestRunRecords<T extends ExecutionHistoryRecord>(records: T[]): T[] {
    const pos = new Map<string, number>();
    const out: T[] = [];
    for (const rec of records) {
      const key = runRecordKey(rec);
      const at = pos.get(key);
      if (at !== undefined) {
        if (stageRichness(rec) >= stageRichness(out[at])) out[at] = rec;
        continue;
      }
      pos.set(key, out.length);
      out.push(rec);
    }
    return out;
  }

  /**
   * Build a lightweight index entry from a full v2 run record (Issue #1007).
   *
   * Used by both inline index updates (after appendRecord) and
   * TelemetryStore.rebuildIndex() for full index reconstruction.
   */
  static buildIndexEntry(record: ExecutionHistoryRunRecordV2): HistoryIndexEntry {
    const stageCount = Object.values(record.stages).filter(
      (s) => s.status === "complete" || s.status === "skipped"
    ).length;

    return {
      issue_number: record.issue_number,
      run_id: (record as { run_id?: string }).run_id,
      title: record.title,
      outcome: record.outcome,
      outcome_type: record.outcome_type,
      is_recovery: record.is_recovery,
      is_supercharge: record.is_supercharge,
      performance_mode: record.performance_mode,
      focus_lens_active: record.focus_lens_active?.lens,
      cost_usd: record.tokens.estimated_cost_usd,
      total_input_tokens: record.tokens.total_input,
      total_output_tokens: record.tokens.total_output,
      total_cache_read_tokens: record.tokens.total_cache_read,
      total_cache_creation_tokens: record.tokens.total_cache_creation,
      duration_ms: record.total_duration_ms,
      stage_count: stageCount,
      started_at: record.started_at,
      recorded_at: record.recorded_at,
      labels: record.labels,
      size: record.size,
      type: record.type,
      branch: record.branch,
    };
  }

  /**
   * Update the history index after a JSONL append (Issue #1007).
   *
   * Reads the existing index, prepends the new entry, and writes back atomically.
   * If the index is missing or corrupt, rebuilds from scratch is deferred to
   * TelemetryStore — this method only creates a fresh index with the single entry.
   *
   * Non-critical: index update failures are logged but don't fail the append.
   */
  private static async updateIndex(
    workspaceRoot: string,
    record: ExecutionHistoryRunRecordV2
  ): Promise<void> {
    const indexPath = path.join(this.getHistoryDir(workspaceRoot), "index.json");

    try {
      // Read existing index (or start fresh)
      let index: HistoryIndex = {
        schema_version: "1",
        updated_at: "",
        total_runs: 0,
        entries: [],
      };

      try {
        const content = await fs.readFile(indexPath, "utf-8");
        const parsed = JSON.parse(content);
        if (parsed.schema_version && Array.isArray(parsed.entries)) {
          index = parsed;
        }
      } catch {
        // Index missing or corrupt — start fresh
      }

      // Build the new entry, drop any prior entry for the same run (an upgrade
      // replacing a leaner record, or a re-append that slipped past the
      // idempotency skip), and prepend it so the index holds exactly one entry
      // per run, most-recent-first (Issue #313).
      const entry = this.buildIndexEntry(record);
      const entryKey = runRecordKey(record);
      index.entries = index.entries.filter((e) => indexEntryKey(e) !== entryKey);
      index.entries.unshift(entry);
      index.total_runs = index.entries.length;
      index.updated_at = new Date().toISOString();

      // Write atomically (temp file + rename)
      const tempPath = indexPath + ".tmp";
      await fs.writeFile(tempPath, JSON.stringify(index, null, 2), "utf-8");
      await fs.rename(tempPath, indexPath);
    } catch (error) {
      // Non-critical: index update failures don't break the append
      console.warn(`[Nightgauge] Failed to update history index: ${error}`);
    }
  }
}
