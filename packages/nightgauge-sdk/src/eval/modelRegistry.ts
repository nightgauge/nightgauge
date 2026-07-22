/**
 * Provider-aware model & pricing registry — the single source of truth for
 * model identity, token pricing, tier bands, and capability metadata across
 * ALL providers (Issues #4169, #56).
 *
 * The canonical data lives in `model-registry.json` (next to this module; copied
 * to `dist/eval/` at build, mirroring the `failure-taxonomy.yaml` precedent).
 * The Go binary reads a parity-tested mirror at `internal/models/model-registry.json`
 * — `scripts/sync-model-registry.sh` copies this file there and a Go test fails
 * if they drift. Adding a model is **one entry** in the JSON (+ a sync).
 *
 * Tier translation for non-Anthropic adapters resolves through
 * {@link resolveModelForAdapter} — this replaced the hand-synced
 * `GEMINI_TIER_MODEL_MAP` / `ADAPTER_MODEL_REMAP` / `ADAPTER_MODEL_TABLES`
 * copies and made `CODEX_MODELS`/`CODEX_TIER_MODEL_MAP` registry-derived (#56).
 *
 * Local providers (ollama / lm-studio) have NO registry entries by design: the
 * user-configured local model serves every band (mode envelopes collapse to
 * identity) and costs a truthful $0 via the unknown-model default.
 *
 * @see docs/decisions/011-model-eval-system.md
 * @see packages/nightgauge-sdk/src/eval/modelEvalSchemas.ts - ModelDescriptor
 */

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { z } from "zod";
import { ModelDescriptorSchema, type ModelDescriptor, type Provider } from "./modelEvalSchemas.js";
import type { ModelTier } from "../analysis/AutoModelSelector.js";
import type { ModelCostRate } from "../analysis/types.js";

/**
 * Rates applied when a model id is unknown to the registry: a truthful $0,
 * never a fabricated tier default. Local (ollama/lm-studio) models land here
 * by design. Callers that record costs should surface {@link isKnownModel}
 * so a $0 total is distinguishable from "not billed". Mirrors the Go
 * `tokens.CalculateCost` default.
 */
const UNKNOWN_MODEL_RATES = { input: 0, output: 0, cache_read: 0, cache_creation: 0 } as const;

/** Shape of the canonical registry file. Extra top-level keys (e.g. `$schema_note`) are ignored. */
const RegistryFileSchema = z
  .object({
    version: z.string(),
    models: z.array(ModelDescriptorSchema).min(1),
  })
  .passthrough();

function loadRegistry(): ModelDescriptor[] {
  const raw = readFileSync(resolve(__dirname, "model-registry.json"), "utf-8");
  const parsed = RegistryFileSchema.parse(JSON.parse(raw));
  const ids = new Set<string>();
  const bands = new Set<string>();
  for (const m of parsed.models) {
    if (ids.has(m.id)) throw new Error(`model-registry.json: duplicate model id "${m.id}"`);
    ids.add(m.id);
    // Tier-band resolution must be deterministic: at most one non-deprecated
    // model may serve a given (provider, band) pair.
    if (m.deprecated) continue;
    for (const tier of m.tiers ?? []) {
      const key = `${m.provider}/${tier}`;
      if (bands.has(key)) {
        throw new Error(`model-registry.json: duplicate non-deprecated band "${key}" (${m.id})`);
      }
      bands.add(key);
    }
  }
  return parsed.models;
}

/** The full registry (all models, including deprecated ones kept for cost replay). */
export const MODEL_REGISTRY: readonly ModelDescriptor[] = Object.freeze(loadRegistry());

/** Models that are not deprecated — the set the pipeline should route to today. */
export function activeModels(): ModelDescriptor[] {
  return MODEL_REGISTRY.filter((m) => !m.deprecated);
}

/**
 * Map an execution adapter name (any layer's vocabulary: `claude`,
 * `claude-sdk`, `claude-headless`, `codex`, `gemini`, `gemini-sdk`,
 * `copilot`, `ollama`, `lm-studio`) to its registry provider. Unknown
 * adapters map to `other`, which has no tier bands.
 */
export function providerForAdapter(adapter: string): Provider {
  if (adapter === "claude" || adapter.startsWith("claude-")) return "anthropic";
  if (adapter === "codex") return "openai";
  if (adapter === "gemini" || adapter === "gemini-sdk") return "google";
  if (adapter === "copilot") return "copilot";
  if (adapter === "ollama") return "ollama";
  if (adapter === "lm-studio") return "lm-studio";
  return "other";
}

/**
 * Resolve a model by concrete id (exact, provider-agnostic — ids are globally
 * unique) or, failing that, by tier band within `provider` → the current
 * non-deprecated model serving that band. Returns `undefined` when nothing
 * matches — notably for every lookup against a local provider, whose catalog
 * is user-defined and unknowable here.
 */
export function getModelDescriptor(
  idOrTier: string,
  provider: Provider = "anthropic"
): ModelDescriptor | undefined {
  const byId = MODEL_REGISTRY.find((m) => m.id === idOrTier);
  if (byId) return byId;
  return MODEL_REGISTRY.find(
    (m) =>
      m.provider === provider && !m.deprecated && (m.tiers ?? []).includes(idOrTier as ModelTier)
  );
}

/**
 * Registry-first tier translation: resolve a routing tier (or concrete id)
 * for the given execution adapter. `resolve(tier, adapter)` from spike #33 §3
 * — the single replacement for the per-adapter tier maps. Returns `undefined`
 * for local adapters (no tier hierarchy: callers fall back to the configured
 * local model) and for unknown values.
 */
export function resolveModelForAdapter(
  adapter: string,
  tierOrId: string
): ModelDescriptor | undefined {
  return getModelDescriptor(tierOrId, providerForAdapter(adapter));
}

/** True when the registry knows this concrete model id (any provider). */
export function isKnownModel(modelId: string): boolean {
  return MODEL_REGISTRY.some((m) => m.id === modelId);
}

/** Token counts for cost computation (cache fields optional). */
export interface TokenCounts {
  input: number;
  output: number;
  cacheRead?: number;
  cacheCreation?: number;
}

/**
 * USD cost for a model and token counts, from the registry rates. Unknown ids
 * cost a truthful $0 (matching the Go `CalculateCost` default) — check
 * {@link isKnownModel} when a caller needs to flag the estimate as unknown
 * rather than genuinely free.
 */
export function computeCostUsd(modelId: string, tokens: TokenCounts): number {
  const rates = getModelDescriptor(modelId)?.rates ?? UNKNOWN_MODEL_RATES;
  const cacheReadRate = rates.cache_read ?? 0;
  const cacheCreationRate = rates.cache_creation ?? 0;
  return (
    (tokens.input * rates.input +
      tokens.output * rates.output +
      (tokens.cacheRead ?? 0) * cacheReadRate +
      (tokens.cacheCreation ?? 0) * cacheCreationRate) /
    1_000_000
  );
}

/**
 * Derive the legacy tier-keyed `ModelCostRate` map from the registry, so the old
 * hand-maintained `DEFAULT_MODEL_COST_RATES` table has a single source. One entry
 * per Anthropic tier, taken from that tier's current (non-deprecated) model.
 */
export function deriveDefaultModelCostRates(): Record<string, ModelCostRate> {
  const out: Record<string, ModelCostRate> = {};
  for (const tier of ["haiku", "sonnet", "opus", "fable"] as const) {
    const m = getModelDescriptor(tier, "anthropic");
    if (!m) continue;
    out[tier] = {
      inputPerMillion: m.rates.input,
      outputPerMillion: m.rates.output,
      cacheReadPerMillion: m.rates.cache_read,
      cacheCreationPerMillion: m.rates.cache_creation,
    };
  }
  return out;
}
