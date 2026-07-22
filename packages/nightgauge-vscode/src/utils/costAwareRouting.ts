/**
 * Cost-aware routing (Issue #21) — auto-tune within the mode envelope.
 *
 * Feeds the adaptive router's cost-per-success logic (#2458) with real
 * execution history so it prefers a cheaper model when that model's historical
 * cost-per-success is comparable to the default pick. Because `resolveModel`
 * clamps every router pick to the active mode's `[floor, ceiling]` envelope
 * (Issue #19), this auto-tuning operates inside the band in EVERY mode — not
 * just Elevated — and can never pick below the floor or above the ceiling.
 *
 * `resolveModel` is synchronous while history I/O is async, so this module keeps
 * a per-workspace cache: the sync getter returns the warm context and triggers a
 * fire-and-forget background refresh when stale. First call returns `undefined`
 * (identical to pre-#21 routing); subsequent stages/runs use the warm context.
 * Everything fail-opens — any read/parse error yields `undefined`.
 *
 * @see docs/decisions/012-performance-mode-envelopes.md — Proposal C
 * @see packages/nightgauge-sdk/src/analysis/AutoModelSelector.ts — cost-per-success routing (#2458)
 */

import type { CostPerSuccessContext, ExecutionHistoryRecord } from "@nightgauge/sdk";
import { ExecutionHistoryReader } from "./executionHistoryReader";
import { getModelRoutingBoolean } from "./resolvers/modelResolver";

/** How long a built context is considered fresh before a background refresh. */
const CONTEXT_TTL_MS = 120_000;

interface CacheEntry {
  context?: CostPerSuccessContext;
  expiresAt: number;
  refreshing: boolean;
}

const cache = new Map<string, CacheEntry>();

/**
 * Whether cost-aware routing is enabled (Issue #21).
 * `model_routing.cost_aware` (default true) — env override
 * `NIGHTGAUGE_MODEL_ROUTING_COST_AWARE`.
 */
export function isCostAwareRoutingEnabled(workspaceRoot?: string): boolean {
  return getModelRoutingBoolean("cost_aware", "COST_AWARE", true, workspaceRoot);
}

/**
 * Build a CostPerSuccessContext from flat execution-history records. Pure and
 * synchronous so it can be unit-tested without I/O. Returns `undefined` when
 * there is no usable history (the router then behaves as if no context exists).
 */
export function buildCostPerSuccessFromRecords(
  records: ExecutionHistoryRecord[]
): CostPerSuccessContext | undefined {
  if (records.length === 0) return undefined;

  // Aggregate directly by `${tier}:${stage}` — the exact key the selector's
  // cost-per-success lookup uses (AutoModelSelector.computeCostPerSuccess). We do
  // NOT route through ModelPerformanceAnalyzer, whose keys are adapter-prefixed
  // model ids ("claude:sonnet"), not the tier aliases the router compares.
  const history: CostPerSuccessContext["history"] = {};
  for (const r of records) {
    if (!r.model || !r.stage) continue;
    const key = `${r.model}:${r.stage}`;
    const entry = history[key] ?? { totalCostUsd: 0, successCount: 0, totalCount: 0 };
    entry.totalCostUsd += r.costUsd ?? 0;
    entry.totalCount += 1;
    if (r.success) entry.successCount += 1;
    history[key] = entry;
  }

  if (Object.keys(history).length === 0) return undefined;
  return { history };
}

/**
 * Synchronously return the cached cost-per-success context, if warm. When the
 * cache is missing or stale, kicks off a background refresh (once) and returns
 * whatever is currently cached (possibly `undefined`). Never throws.
 */
export function getCostPerSuccessContext(workspaceRoot: string): CostPerSuccessContext | undefined {
  const now = Date.now();
  const entry = cache.get(workspaceRoot);
  const fresh = entry !== undefined && now < entry.expiresAt;
  if (!fresh && !entry?.refreshing) {
    const next: CacheEntry = entry ?? { expiresAt: 0, refreshing: false };
    next.refreshing = true;
    cache.set(workspaceRoot, next);
    void refreshCostPerSuccessContext(workspaceRoot)
      .catch(() => {
        /* fail-open */
      })
      .finally(() => {
        const cur = cache.get(workspaceRoot);
        if (cur) cur.refreshing = false;
      });
  }
  return entry?.context;
}

/**
 * Flatten run-level JSONL history records into the flat per-stage
 * `ExecutionHistoryRecord` shape the SDK aggregator expects. Only the fields
 * the cost-per-success aggregation needs are populated; the rest use safe
 * zeroes. Records without per-stage model-selection data are skipped.
 */
function flattenRunRecords(rawRecords: unknown[]): ExecutionHistoryRecord[] {
  const flat: ExecutionHistoryRecord[] = [];
  for (const raw of rawRecords) {
    const run = raw as {
      record_type?: string;
      issue_number?: number;
      started_at?: string;
      stages?: Record<
        string,
        {
          status?: string;
          duration_ms?: number;
          started_at?: string;
          model_selection?: { model?: string };
        }
      >;
      tokens?: {
        per_stage?: Record<string, { input?: number; output?: number; cost_usd?: number }>;
      };
    };
    if (run.record_type !== "run" || !run.stages) continue;

    for (const [stageName, stage] of Object.entries(run.stages)) {
      const model = stage.model_selection?.model;
      if (!model) continue;
      const perStage = run.tokens?.per_stage?.[stageName];
      flat.push({
        issueNumber: run.issue_number ?? 0,
        stage: stageName,
        adapter: "claude",
        model,
        success: stage.status === "complete",
        retries: 0,
        inputTokens: perStage?.input ?? 0,
        outputTokens: perStage?.output ?? 0,
        costUsd: perStage?.cost_usd ?? 0,
        durationMs: stage.duration_ms ?? 0,
        timestamp: stage.started_at ?? run.started_at ?? new Date().toISOString(),
      });
    }
  }
  return flat;
}

/**
 * Refresh the cached context for a workspace by reading and aggregating
 * execution history. Fail-open: on any error the cache is marked fresh with an
 * empty (undefined) context so we don't thrash the disk on a broken history dir.
 */
export async function refreshCostPerSuccessContext(workspaceRoot: string): Promise<void> {
  let context: CostPerSuccessContext | undefined;
  try {
    const rawRecords = await ExecutionHistoryReader.readAll(workspaceRoot);
    context = buildCostPerSuccessFromRecords(flattenRunRecords(rawRecords));
  } catch {
    context = undefined;
  }
  cache.set(workspaceRoot, {
    context,
    expiresAt: Date.now() + CONTEXT_TTL_MS,
    refreshing: false,
  });
}

/** @internal Test hook — prime the cache with a fixed context. */
export function __primeCostContextCache(
  workspaceRoot: string,
  context: CostPerSuccessContext | undefined
): void {
  cache.set(workspaceRoot, {
    context,
    expiresAt: Date.now() + CONTEXT_TTL_MS,
    refreshing: false,
  });
}

/** @internal Test hook — clear all cached contexts. */
export function __clearCostContextCache(): void {
  cache.clear();
}
