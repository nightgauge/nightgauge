/**
 * Unified per-stage cost resolver.
 *
 * Resolves the USD cost for a single pipeline stage via a three-step chain:
 *
 *   1. **native**   — vendor-emitted `total_cost_usd` from the stream (Claude
 *                     today; future SDK adapters may also emit this).
 *   2. **computed** — published rate-card cost from `getProviderPricing`,
 *                     evaluated against the accumulated token totals.
 *   3. **unknown**  — adapter+model has no pricing entry; cost is reported as
 *                     `0` with a `'unknown'` label so analytics can distinguish
 *                     "we don't know" from "we know it was zero".
 *
 * Native takes precedence even when computed disagrees — vendor billing is
 * the source of truth. When both are present and differ by more than 5%, a
 * single `console.warn` is emitted as a non-gating drift signal so the
 * pricing table stays honest as vendors revise rates between table refreshes.
 *
 * Local adapters (`lm-studio`, `ollama`) and Copilot zero-rate flat-billed
 * entries return `{ cost_usd: 0, source: 'computed' }` — the synthetic table
 * entry encodes "intentionally zero", which is more informative than
 * `'unknown'`.
 *
 * @see Issue #3228 — Unified `computeStageCost` across all adapters
 * @see Issue #3227 — Provider pricing tables: (adapter, model) cost map
 * @see Epic #3213 — Per-stage cost accuracy across adapters
 */

import type { ExecutionAdapter } from "../config/schema";
import { getProviderPricing } from "./providerPricing";

/**
 * Per-stage token totals consumed by the cost resolver. Mirrors the
 * `ParsedTokenUsage` field set in `tokenParser.ts` but in `snake_case` so the
 * helper can be reused from history-side code without converting field names
 * twice.
 */
export interface StageCostTokens {
  input: number;
  output: number;
  cache_read?: number;
  cache_creation?: number;
}

/**
 * Resolved cost plus the resolution step that produced it. Callers use the
 * `source` label to attribute downstream analytics (e.g., distinguish billed
 * cost from rate-card-computed cost).
 */
export interface StageCostResult {
  cost_usd: number;
  source: "native" | "computed" | "unknown";
}

/**
 * Drift threshold above which native vs. computed cost emits a warn. 5% is
 * loose enough to absorb routine table refresh latency yet tight enough to
 * surface a stale rate card before it skews calibration baselines.
 */
const DRIFT_WARN_THRESHOLD = 0.05;

/**
 * Round to 6 decimals — matches Claude's `total_cost_usd` precision so
 * rate-card-computed values are visually indistinguishable in JSONL records.
 */
function round6(n: number): number {
  return Math.round(n * 1_000_000) / 1_000_000;
}

/**
 * Compute the rate-card cost for a stage given a pricing entry. Returns
 * `null` when the entry is absent (caller decides whether that means
 * `'unknown'` or `'computed'` zero-rate).
 */
function computeFromTable(
  adapter: ExecutionAdapter,
  model: string,
  tokens: StageCostTokens
): number | null {
  const entry = getProviderPricing(adapter, model);
  if (!entry) return null;
  const cost =
    (tokens.input * entry.input_per_mtok +
      tokens.output * entry.output_per_mtok +
      (tokens.cache_read ?? 0) * (entry.cache_read_per_mtok ?? 0) +
      (tokens.cache_creation ?? 0) * (entry.cache_write_per_mtok ?? 0)) /
    1_000_000;
  return round6(cost);
}

/**
 * Resolve the USD cost for a single stage.
 *
 * @param adapter Execution adapter that ran the stage.
 * @param model   Model identifier for the stage.
 * @param tokens  Per-stage token totals.
 * @param native  Optional vendor-emitted cost (Claude today). When `> 0`,
 *                always wins.
 */
export function computeStageCost(
  adapter: ExecutionAdapter,
  model: string,
  tokens: StageCostTokens,
  native?: number
): StageCostResult {
  if (native !== undefined && native > 0) {
    const computed = computeFromTable(adapter, model, tokens);
    if (computed !== null && computed > 0) {
      const deltaPct = Math.abs(native - computed) / native;
      if (deltaPct > DRIFT_WARN_THRESHOLD) {
        console.warn(
          `[computeStageCost] Pricing drift for ${adapter}/${model}: ` +
            `native=$${native.toFixed(6)}, computed=$${computed.toFixed(6)}, ` +
            `delta=${(deltaPct * 100).toFixed(1)}%. ` +
            `Native wins; review pricing table.`
        );
      }
    }
    return { cost_usd: native, source: "native" };
  }

  const computed = computeFromTable(adapter, model, tokens);
  if (computed === null) {
    return { cost_usd: 0, source: "unknown" };
  }
  return { cost_usd: computed, source: "computed" };
}
