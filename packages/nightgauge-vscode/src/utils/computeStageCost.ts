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

import {
  computeCostUsd,
  getModelDescriptor,
  isKnownModel,
  isLocalProvider,
  parseOpenCodeModel,
  providerFor,
} from "@nightgauge/sdk";
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
 * #358 floor convention. Claude's stream parser supplies the real split;
 * adapters or historical records that expose only a flat total use the floor.
 */
export interface StageCostTokens {
  input: number;
  output: number;
  cache_read?: number;
  /** Cache writes bought with a 5-minute TTL. Unsplit counts go here (#358). */
  cache_creation_5m?: number;
  /** Cache writes bought with a 1-hour TTL. */
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
 * True when the stage's model runs on a server the operator runs, so no
 * provider bills it. Decided by the PROVIDER OF THE MODEL, not the adapter
 * name: `lm-studio` and `ollama` serve only local models, and an `opencode`
 * stage is local exactly when its `<provider>/<model>` names a local provider
 * key (`lmstudio/…`, `ollama/…`; ADR-022 § 1, § 3) — or, in the form a stage
 * record carries (ADR-022 § 2), the normalized provider itself
 * (`lm-studio/…`), as the SDK's `openCodeServing` reads it. Every other
 * adapter, and an `opencode` model whose provider is hosted or unrecognized
 * (`other`), is not local. The SDK's `providerFor` / `isLocalProvider` are
 * the single authority, mirroring the Go `models.ProviderFor` /
 * `models.IsLocalProvider`.
 *
 * A local model string must NEVER reach a registry lookup: a user who names
 * their local checkpoint `claude-sonnet-5` would otherwise be billed at
 * Anthropic's frontier rates for inference that costs them nothing.
 */
export function isLocalExecution(adapter: ExecutionAdapter | string, model: string): boolean {
  if (isLocalProvider(providerFor(adapter, model))) return true;
  if (adapter !== "opencode") return false;
  const slash = model.indexOf("/");
  return slash > 0 && slash < model.length - 1 && isLocalProvider(model.slice(0, slash));
}

/**
 * True when the registry can price an `opencode` model: its provider is
 * hosted and the registry lists the model's id UNDER THAT PROVIDER, the rule
 * the SDK's `openCodeStageCostUsd` and the Go `CalculateCostFor("opencode")`
 * apply. `openrouter/claude-sonnet-5` is not priceable: the registry knows
 * `claude-sonnet-5`, but as Anthropic's, and OpenRouter bills its own rates.
 */
export function isPriceableOpenCodeModel(model: string): boolean {
  if (isLocalExecution("opencode", model)) return false;
  const { provider, bareId } = parseOpenCodeModel(model);
  return isKnownModel(bareId) && getModelDescriptor(bareId)?.provider === provider;
}

/**
 * The key a stage that ran on a local model server is bucketed under for
 * stall calibration, or `undefined` for every other stage (#1657).
 *
 * A local model is a different machine from the flagship providers: a 27B
 * model on LM Studio takes 76 s to prefill and decodes at ~8 tok/s, so its
 * stage durations say nothing about a Claude stage's and vice versa. Local
 * stages are therefore bucketed by `<adapter>/<model>` — the `-m` value an
 * `opencode` stage was dispatched with — and never enter the flagship
 * `(stage, mode)` buckets.
 *
 * Locality is the provider of the model (`isLocalExecution`). A recorded
 * `model_provider` (ADR-022 § 2, the provider that actually served the stage)
 * wins over the one derived from the model string when present.
 */
export function localExecutionKey(
  adapter: string | undefined,
  model: string | undefined,
  modelProvider?: string
): string | undefined {
  if (!adapter || !model) return undefined;
  const local =
    modelProvider !== undefined ? isLocalProvider(modelProvider) : isLocalExecution(adapter, model);
  return local ? `${adapter}/${model}` : undefined;
}

/**
 * The registry id a stage's model is priced as. An `opencode` model is
 * `<provider>/<id>` and the registry keys by bare id (ADR-022 § 2), so
 * `anthropic/claude-sonnet-5` prices as `claude-sonnet-5` — the same rate card
 * the claude adapter's stage uses. Every other adapter launches a bare id.
 */
function registryPricingId(adapter: ExecutionAdapter, model: string): string {
  return adapter === "opencode" ? parseOpenCodeModel(model).bareId : model;
}

/**
 * Registry cost for a stage, or `null` when the registry does not know this
 * model id (or the model runs locally, where no id is meaningful).
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
  if (isLocalExecution(adapter, model)) return null;
  if (adapter === "opencode" && !isPriceableOpenCodeModel(model)) return null;
  const id = registryPricingId(adapter, model);
  if (!isKnownModel(id)) return null;
  return round6(
    computeCostUsd(id, {
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
 * 5m slot per the #358 floor convention) AND the model prices the two write
 * tiers differently. On such a
 * model the computed number is guaranteed to sit below native by up to 37.5%
 * of the write pool — captured Claude traffic is 1h-heavy — so the >5% warn
 * would fire on essentially every real Claude stage and point triage at
 * `model-registry.json`, which is correct. A warn that fires every time is a
 * warn nobody reads.
 *
 * The condition is deliberately written against the ABSENCE of a 1h count, so
 * parsed split traffic still gets the ordinary registry-drift check.
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
  if (adapter === "opencode") {
    return computeOpenCodeStageCost(model, tokens);
  }
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
              `(computed=$${computed.toFixed(6)}). Not a rate-card drift.`
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
    //   - local-adapter stages (lm-studio/ollama), whose catalog the
    //     registry deliberately does not carry; and
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

/**
 * An `opencode` stage's cost (ADR-022 § 3). The stream's own cost figure is
 * never read: OpenCode's `cost` comes from its catalog, not the bill, and it
 * reads 0 for a provider it cannot price, so a native value is ignored here
 * whatever it is.
 *
 *   - A local provider (`lmstudio/…`, `ollama/…`) is a stamped zero
 *     (`'computed'`, like Copilot's registry zero): no provider bills it, and
 *     its model id — even one spelled `claude-sonnet-5` — never reaches the
 *     registry.
 *   - A hosted model the registry knows is priced as its bare id, the same
 *     cost the model's own vendor adapter books.
 *   - Anything else — a hosted model the registry cannot price, an `other`
 *     provider — is unstamped (`'unknown'`, $0): an unknown cost is never
 *     booked as a stamped zero, even when the run reported 0.
 */
function computeOpenCodeStageCost(model: string, tokens: StageCostTokens): StageCostResult {
  if (isLocalExecution("opencode", model)) return { cost_usd: 0, source: "computed" };
  const computed = computeFromRegistry("opencode", model, tokens);
  if (computed === null) return { cost_usd: 0, source: "unknown" };
  return { cost_usd: computed, source: "computed" };
}
