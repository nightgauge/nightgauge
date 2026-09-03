/**
 * Execution-history feeder — the ONE mapper from JSONL run records to the
 * per-stage `ExecutionHistoryRecord` shape every analyzer consumes (#461).
 *
 * The run record written by Go (`internal/state/history.go`, mirrored by the
 * extension's `schemas/executionHistory.ts`) nests one `stages[<name>]` entry
 * per stage, and it is THAT entry — not the run — that carries
 * `model_selection.{model,source,mode,confidence,complexity}`. Routing is a
 * per-stage fact, so the record shape the analyzers receive is per-stage:
 * one flat record per executed stage, with `model` and `selectionSource`
 * copied from the stage's `model_selection` block.
 *
 * Before #461 the health path (`PipelineHealthRunner`, `buildHealthInput`)
 * built ONE record per RUN with neither field, while the post-pipeline path
 * (`PostPipelineAnalyzer.adaptRecords`) flattened per stage — two mappers,
 * one of which starved the model-routing dimension of every record forever.
 * `costAwareRouting` carried a fourth copy. All of them now call this
 * function; do not add another mapper.
 *
 * Only stages that actually executed (`status` of `complete` or `failed`)
 * produce a record. A `skipped` / `pending` / `deferred` stage dispatched no
 * model and has no outcome to attribute, so emitting it would book a phantom
 * failure against whatever model its selection block names.
 */

import type { ExecutionHistoryRecord, ModelSelectionSource } from "../types.js";
import { MODEL_SELECTION_SOURCES } from "../types.js";

/** Structural view of a wire `stages[<name>]` entry — only what the feeder reads. */
export interface HistoryRunStageInput {
  status?: string;
  started_at?: string;
  duration_ms?: number;
  auto_retry_count?: number;
  manual_retry_count?: number;
  failure_category?: "infrastructure" | "agent" | "organic";
  model_selection?: {
    model?: string;
    source?: string;
    mode?: "manual" | "automatic" | "hybrid";
    confidence?: number;
    complexity?: string;
    adapter?: string;
  };
}

/** Structural view of a wire `tokens.per_stage[<name>]` entry. */
export interface HistoryRunStageTokensInput {
  input?: number;
  output?: number;
  cache_read?: number;
  cache_creation?: number;
  cost_usd?: number;
  cost_unstamped?: boolean;
  adapter?: string;
}

/**
 * Structural view of a JSONL run record. Deliberately narrower than the
 * extension's zod schema: the feeder needs the discriminator, the issue, the
 * stage map and the per-stage token map, nothing else.
 */
export interface HistoryRunRecordInput {
  record_type?: string;
  issue_number?: number;
  started_at?: string;
  stages?: Record<string, HistoryRunStageInput | undefined>;
  tokens?: {
    per_stage?: Record<string, HistoryRunStageTokensInput | undefined>;
  };
}

const EXECUTED_STAGE_STATUSES: ReadonlySet<string> = new Set(["complete", "failed"]);

function asSelectionSource(value: string | undefined): ModelSelectionSource | undefined {
  return value !== undefined && (MODEL_SELECTION_SOURCES as readonly string[]).includes(value)
    ? (value as ModelSelectionSource)
    : undefined;
}

/**
 * Flatten JSONL history records into per-stage analyzer records.
 *
 * Non-run records (`record_type !== "run"`) and runs without a `stages` map
 * contribute nothing. Stages without a `model_selection` block still produce
 * a record — token, cost, duration and reliability dimensions need every
 * executed stage — with `model` / `selectionSource` left undefined; callers
 * that only care about routed stages filter on `model !== undefined`.
 */
export function flattenRunRecords(
  rawRecords: ReadonlyArray<HistoryRunRecordInput | Record<string, unknown>>
): ExecutionHistoryRecord[] {
  const result: ExecutionHistoryRecord[] = [];

  for (const raw of rawRecords) {
    const run = raw as HistoryRunRecordInput;
    if (run.record_type !== "run" || !run.stages) continue;

    const issueNumber = run.issue_number ?? 0;

    for (const [stageName, stage] of Object.entries(run.stages)) {
      if (!stage || !EXECUTED_STAGE_STATUSES.has(stage.status ?? "")) continue;

      const tokens = run.tokens?.per_stage?.[stageName];
      const selection = stage.model_selection;
      const inputTokens = tokens?.input ?? 0;
      const outputTokens = tokens?.output ?? 0;
      const costUsd = tokens?.cost_usd ?? 0;

      result.push({
        issueNumber,
        stage: stageName,
        adapter: selection?.adapter ?? tokens?.adapter,
        model: selection?.model,
        success: stage.status === "complete",
        retries: (stage.auto_retry_count ?? 0) + (stage.manual_retry_count ?? 0),
        inputTokens,
        outputTokens,
        cacheReadTokens: tokens?.cache_read ?? 0,
        cacheCreationTokens: tokens?.cache_creation ?? 0,
        costUsd,
        durationMs: stage.duration_ms ?? 0,
        timestamp: stage.started_at ?? run.started_at ?? "",
        modelSelectionMode: selection?.mode,
        selectedModel: selection?.model,
        selectionSource: asSelectionSource(selection?.source),
        autoSelectorConfidence: selection?.confidence,
        autoSelectorComplexity: selection?.complexity,
        failure_category: stage.failure_category,
        // A $0 stage that moved tokens ran on local inference — unless the
        // zero is a pricing-registry placeholder (`cost_unstamped`, #585).
        isLocalModel:
          costUsd === 0 && inputTokens + outputTokens > 0 && tokens?.cost_unstamped !== true,
      });
    }
  }

  return result;
}
