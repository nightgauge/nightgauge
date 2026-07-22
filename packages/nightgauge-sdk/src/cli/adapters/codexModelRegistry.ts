/**
 * Codex model registry — the Codex-specific view over the provider-aware
 * model registry (`eval/model-registry.json`), which is the single source of
 * truth for Codex/OpenAI model identifiers, deprecation metadata, and tier
 * bands (#56). This module derives its data from the registry and layers the
 * Codex-only behaviors on top (Claude-id prefix remapping, catalog listing).
 *
 * Every Codex model id and tier mapping in the SDK and the VSCode extension MUST
 * resolve through this module or {@link resolveModelForAdapter}. Do not
 * hardcode Codex model ids anywhere else — doing so reintroduces the drift
 * this module exists to eliminate.
 *
 * @see Issue #4018 — Canonical Codex model registry (single source of truth)
 * @see Issue #56 — provider-aware registry with cross-provider tier bands
 */

import { getModelDescriptor, MODEL_REGISTRY } from "../../eval/modelRegistry.js";

/** Claude-style routing tiers used across the pipeline routing layer. */
export type CodexTier = "haiku" | "sonnet" | "opus" | "fable";

export interface CodexModelMeta {
  /** Recommended default model for most pipeline work. */
  recommended?: boolean;
  /** Research-preview model — excluded from default catalog/UI listings. */
  researchPreview?: boolean;
  /** Deprecated by OpenAI — never auto-selected; carries a replacement id. */
  deprecated?: boolean;
  /** For deprecated models, the current id callers should migrate to. */
  replacement?: string;
}

/**
 * Known Codex models keyed by the exact id passed to `codex --model`/`-m`,
 * derived from the model registry's `provider: "openai"` entries. Deprecated
 * entries are retained ONLY so they can be recognized and remapped; they are
 * never returned by {@link resolveCodexModelAlias} or surfaced by
 * {@link listCodexModels} (unless explicitly requested).
 */
export const CODEX_MODELS: Record<string, CodexModelMeta> = Object.fromEntries(
  MODEL_REGISTRY.filter((m) => m.provider === "openai").map((m) => [
    m.id,
    {
      ...(m.recommended ? { recommended: true } : {}),
      ...(m.research_preview ? { researchPreview: true } : {}),
      ...(m.deprecated ? { deprecated: true } : {}),
      ...(m.replacement ? { replacement: m.replacement } : {}),
    },
  ])
);

function mustResolveCodexTier(tier: CodexTier): string {
  const m = getModelDescriptor(tier, "openai");
  if (!m) {
    throw new Error(`model-registry.json: no non-deprecated openai model for tier band "${tier}"`);
  }
  return m.id;
}

/**
 * Claude-tier → Codex-model routing map, resolved from the registry's tier
 * bands. Shared pipeline routing speaks in tiers (haiku/sonnet/opus/fable);
 * the Codex CLI needs concrete OpenAI ids. `fable` (premium frontier tier)
 * maps to the strongest model, same as `opus`.
 */
export const CODEX_TIER_MODEL_MAP: Record<CodexTier, string> = {
  haiku: mustResolveCodexTier("haiku"),
  sonnet: mustResolveCodexTier("sonnet"),
  opus: mustResolveCodexTier("opus"),
  fable: mustResolveCodexTier("fable"),
};

/** Recommended frontier default (opus/fable tiers + the "recommended" UI tag). */
export const CODEX_RECOMMENDED_DEFAULT_MODEL = CODEX_TIER_MODEL_MAP.opus;

/** Base default model for the sonnet tier and the config `codex.model` default. */
export const CODEX_DEFAULT_BASE_MODEL = CODEX_TIER_MODEL_MAP.sonnet;

const CODEX_TIERS: readonly CodexTier[] = ["haiku", "sonnet", "opus", "fable"];

function isCodexTier(value: string): value is CodexTier {
  return (CODEX_TIERS as readonly string[]).includes(value);
}

/** True when `id` is a model the registry knows (including deprecated/preview). */
export function isValidCodexModel(id: string): boolean {
  return Object.prototype.hasOwnProperty.call(CODEX_MODELS, id);
}

/** True when `id` is a model OpenAI has deprecated. */
export function isDeprecatedCodexModel(id: string): boolean {
  return CODEX_MODELS[id]?.deprecated === true;
}

/** True when `id` is a research-preview model (excluded from default catalogs). */
export function isResearchPreviewCodexModel(id: string): boolean {
  return CODEX_MODELS[id]?.researchPreview === true;
}

export interface ListCodexModelsOptions {
  includeDeprecated?: boolean;
  includeResearchPreview?: boolean;
}

/**
 * List known Codex model ids, recommended-first. By default excludes deprecated
 * and research-preview models — the right set for catalog fallbacks and UI
 * pickers. Pass options to widen the set.
 */
export function listCodexModels(opts: ListCodexModelsOptions = {}): string[] {
  const { includeDeprecated = false, includeResearchPreview = false } = opts;
  return Object.entries(CODEX_MODELS)
    .filter(([, meta]) => {
      if (meta.deprecated && !includeDeprecated) return false;
      if (meta.researchPreview && !includeResearchPreview) return false;
      return true;
    })
    .sort(([, a], [, b]) => Number(b.recommended ?? false) - Number(a.recommended ?? false))
    .map(([id]) => id);
}

/**
 * Translate a Claude-style routing tier (`haiku`/`sonnet`/`opus`/`fable`) to a
 * concrete Codex/OpenAI model id. Exact model ids and unknown strings pass
 * through unchanged so explicit `NIGHTGAUGE_CODEX_MODEL=<id>` overrides and
 * future ids keep working. Never returns a deprecated id for a tier alias.
 *
 * @see Issue #4018 — replaces the per-file hardcoded tier maps that had drifted
 *   (opus → gpt-5.3-codex [deprecated], haiku → gpt-5.1-codex-mini [invalid]).
 */
export function resolveCodexModelAlias(model: string | undefined): string | undefined {
  if (!model) return undefined;
  const trimmed = model.trim();
  if (isCodexTier(trimmed)) {
    return CODEX_TIER_MODEL_MAP[trimmed];
  }
  // Claude escalation ids the scheduler emits ("claude-sonnet-4-6") map by
  // PREFIX to the matching tier's Codex model. This MIRRORS resolveCodexModel in
  // internal/execution/adapters/codex.go so the SDK and Go `nightgauge run
  // --adapter codex` paths resolve (and validate) the same input identically
  // (#4021). Keep the two in sync.
  if (trimmed.startsWith("claude-haiku")) return CODEX_TIER_MODEL_MAP.haiku;
  if (trimmed.startsWith("claude-sonnet")) return CODEX_TIER_MODEL_MAP.sonnet;
  if (trimmed.startsWith("claude-opus") || trimmed.startsWith("claude-fable")) {
    return CODEX_TIER_MODEL_MAP.opus;
  }
  // Remap a known-deprecated id to its replacement so a stale env/config value
  // never reaches the Codex CLI as an unusable model. Unknown ids pass through
  // unchanged so future models keep working. (User-facing validation/warnings
  // are layered on by the preflight in #4021.)
  const meta = CODEX_MODELS[trimmed];
  if (meta?.deprecated && meta.replacement) {
    return meta.replacement;
  }
  return trimmed;
}
