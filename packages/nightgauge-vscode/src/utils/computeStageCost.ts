/**
 * Unified per-stage cost resolver.
 *
 * Resolves the USD cost for a single pipeline stage via a three-step chain:
 *
 *   1. **native**   — vendor-emitted `total_cost_usd` from the stream (Claude
 *                     today; future SDK adapters may also emit this).
 *   2. **computed** — registry rate-card cost from the SDK's
 *                     {@link computeCostUsd}, evaluated against the
 *                     accumulated token totals.
 *   3. **unknown**  — the registry does not know this model id; cost is
 *                     reported as `0` with an `'unknown'` label so analytics
 *                     can distinguish "we don't know" from "we know it was
 *                     zero".
 *
 * ── THE REGISTRY IS THE ONLY PRICING AUTHORITY (#391) ──
 *
 * This module used to price from `providerPricing.ts`, a second hand-curated
 * `(adapter, model)` rate table living inside the extension. That table has
 * been DELETED. It is not refactored, wrapped, or kept as a fallback: a second
 * table is the defect, because it drifts silently and nothing can detect the
 * drift. Three failures it had shipped by the time it was removed:
 *
 *   - Its Claude keys had rotted to the `claude-opus-4-8` era, so the three
 *     models the pipeline actually routes today (`claude-opus-5`,
 *     `claude-sonnet-5`, `claude-haiku-4-5-20251001`) had NO entry at all and
 *     every stage they ran booked $0 with source `'unknown'`.
 *   - It carried ONE `cache_write_per_mtok` (the 5-minute column), so it could
 *     not express #358's two-tier split and under-priced every 1-hour cache
 *     write by 37.5%.
 *   - Its `gpt-5.4` row said $1.00/$8.00 where the registry said $1.25/$10.00.
 *
 * None of this was theoretical: the number this module returns ships over IPC
 * as `stageResult.costUsd` and WINS over the Go scheduler's own calculation
 * whenever it is non-zero (`scheduler.go` calls `tokens.CalculateCost` only as
 * the `== 0` fallback). For codex and gemini there is no native vendor cost at
 * all, so whatever this file computes IS the booked number.
 *
 * The derivation guard against future drift is structural rather than a test:
 * there is exactly one table (`model-registry.json`), the Go mirror is
 * byte-compared by a parity test, and both language layers read the same file.
 *
 * @see Issue #391 — providerPricing.ts duplicates the registry
 * @see Issue #392 — non-Anthropic cache rates
 * @see Issue #358 — split 5m/1h cache-write rates
 * @see Issue #3228 — Unified `computeStageCost` across all adapters
 */

import { computeCostUsd, getModelDescriptor, isKnownModel } from "@nightgauge/sdk";
import type { ExecutionAdapter } from "../config/schema";

/**
 * Per-stage token totals consumed by the cost resolver. Mirrors the
 * `ParsedTokenUsage` field set in `tokenParser.ts` but in `snake_case` so the
 * helper can be reused from history-side code without converting field names
 * twice.
 *
 * Cache creation is split by TTL tier because Anthropic prices the two pools
 * differently — a 5-minute write is 1.25x base input, a 1-hour write is 2.0x
 * (#358). A caller that knows only a single combined cache-creation count MUST
 * book it into {@link cache_creation_5m}: that is the cheaper tier, so the
 * estimate is a floor rather than an overstatement. This is the documented
 * #358 floor convention, and it is what every caller in the extension does
 * today because the CLI's per-tier split is not yet plumbed end to end (#390).
 * Splitting the field here — rather than waiting for #390 — is what makes the
 * 1h rate reachable at all: the moment #390 populates
 * {@link cache_creation_1h}, this path prices it correctly with no further
 * change.
 */
export interface StageCostTokens {
  input: number;
  output: number;
  cache_read?: number;
  /** Cache writes bought with a 5-minute TTL. Unsplit counts go here (#358). */
  cache_creation_5m?: number;
  /** Cache writes bought with a 1-hour TTL. Populated once #390 lands. */
  cache_creation_1h?: number;
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
 * Adapters whose model catalog is user-defined and therefore unknowable to the
 * registry. Their model string must NEVER reach a registry lookup: a user who
 * names their local checkpoint `claude-sonnet-5` would otherwise be billed at
 * Anthropic's frontier rates for inference that costs them nothing.
 */
function isLocalAdapter(adapter: ExecutionAdapter): boolean {
  return adapter === "lm-studio" || adapter === "ollama";
}

/**
 * Registry cost for a stage, or `null` when the registry does not know this
 * model id (or the adapter is local, where no id is meaningful).
 *
 * The `isKnownModel` gate is load-bearing, not defensive: `getModelDescriptor`
 * — which `computeCostUsd` calls — falls back to a TIER-band lookup when the
 * string is not an exact id, defaulting to the `anthropic` provider. Without
 * this gate a codex stage running a model literally named `opus` would be
 * priced at Claude Opus rates. `isKnownModel` is exact-id-only, so gating on it
 * makes that fallback unreachable from the billing path.
 */
function computeFromRegistry(
  adapter: ExecutionAdapter,
  model: string,
  tokens: StageCostTokens
): number | null {
  if (isLocalAdapter(adapter)) return null;
  if (!isKnownModel(model)) return null;
  return round6(
    computeCostUsd(model, {
      input: tokens.input,
      output: tokens.output,
      cacheRead: tokens.cache_read,
      cacheCreation5m: tokens.cache_creation_5m,
      cacheCreation1h: tokens.cache_creation_1h,
    })
  );
}

/**
 * True when the computed number is a KNOWN FLOOR rather than an independent
 * estimate, which makes a native-vs-computed drift comparison meaningless.
 *
 * The stage supplied cache-write tokens with no TTL split (everything in the
 * 5m slot per the #358 floor convention, because #390 has not plumbed the real
 * split yet) AND the model prices the two write tiers differently. On such a
 * model the computed number is guaranteed to sit below native by up to 37.5%
 * of the write pool — captured Claude traffic is 1h-heavy — so the >5% warn
 * would fire on essentially every real Claude stage and point triage at
 * `model-registry.json`, which is correct. A warn that fires every time is a
 * warn nobody reads.
 *
 * The condition is deliberately written against the ABSENCE of a 1h count, so
 * the warn re-arms itself the moment #390 supplies the split — there is no
 * flag to remember to remove.
 *
 * Only called from the branch where `computeFromRegistry` already returned a
 * value, so `model` is an exact registry id (`isKnownModel` gated) and this
 * lookup cannot land on `getModelDescriptor`'s tier-band fallback.
 */
function isUnsplitCacheWriteFloor(model: string, tokens: StageCostTokens): boolean {
  const writes5m = tokens.cache_creation_5m ?? 0;
  const writes1h = tokens.cache_creation_1h ?? 0;
  // No writes to mis-tier, or the caller already knows the split.
  if (writes5m <= 0 || writes1h > 0) return false;
  const rates = getModelDescriptor(model)?.rates;
  const rate5m = rates?.cache_creation_5m;
  const rate1h = rates?.cache_creation_1h;
  // A provider with one write tier (OpenAI) has no floor to fall to: an
  // unsplit count is the whole truth there, so drift on it is real drift.
  if (rate5m === undefined || rate1h === undefined) return false;
  return rate1h !== rate5m;
}

/**
 * Resolve the USD cost for a single stage.
 *
 * @param adapter Execution adapter that ran the stage.
 * @param model   Model identifier for the stage.
 * @param tokens  Per-stage token totals.
 * @param native  Optional vendor-emitted cost (Claude today). When `> 0`,
 *                always wins — vendor billing is the source of truth. When
 *                both are present and differ by more than 5%, a single
 *                `console.warn` is emitted as a non-gating drift signal —
 *                unless the computed side is the #358 unsplit cache-write
 *                floor, where the gap is expected and the line is emitted at
 *                `console.debug` instead (see {@link isUnsplitCacheWriteFloor}).
 */
export function computeStageCost(
  adapter: ExecutionAdapter,
  model: string,
  tokens: StageCostTokens,
  native?: number
): StageCostResult {
  if (native !== undefined && native > 0) {
    const computed = computeFromRegistry(adapter, model, tokens);
    if (computed !== null && computed > 0) {
      const deltaPct = Math.abs(native - computed) / native;
      if (deltaPct > DRIFT_WARN_THRESHOLD) {
        if (isUnsplitCacheWriteFloor(model, tokens)) {
          // Expected under-read, not drift: downgraded so the number is still
          // observable but never presents as a registry defect.
          console.debug(
            `[computeStageCost] Computed cost for ${adapter}/${model} is the #358 unsplit ` +
              `cache-write floor (all writes booked at the 5m rate), so it reads ` +
              `${(deltaPct * 100).toFixed(1)}% below native=$${native.toFixed(6)} ` +
              `(computed=$${computed.toFixed(6)}). Not a rate-card drift — the drift warn ` +
              `re-arms once #390 plumbs the per-TTL split.`
          );
        } else {
          console.warn(
            `[computeStageCost] Pricing drift for ${adapter}/${model}: ` +
              `native=$${native.toFixed(6)}, computed=$${computed.toFixed(6)}, ` +
              `delta=${(deltaPct * 100).toFixed(1)}%. ` +
              `Native wins; review model-registry.json.`
          );
        }
      }
    }
    return { cost_usd: native, source: "native" };
  }

  const computed = computeFromRegistry(adapter, model, tokens);
  if (computed === null) {
    // Two distinct populations land here, and `'unknown'` is right for both:
    //   - local adapters (lm-studio/ollama), whose catalog the registry
    //     deliberately does not carry; and
    //   - any model id the registry has never heard of.
    // Both are "$0 because we cannot price it", NOT "$0 because it is free".
    // Copilot is the opposite case and does NOT land here: its registry
    // entries record an explicit 0 input/output because Copilot bills flat
    // per-request in the user's subscription tier, so it resolves to
    // `'computed'` zero — a knowingly-free stage, which is strictly more
    // information than `'unknown'`.
    return { cost_usd: 0, source: "unknown" };
  }
  return { cost_usd: computed, source: "computed" };
}
