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

import {
  getModelDescriptor,
  MODEL_REGISTRY,
  mustTransportForAdapter,
} from "../../eval/modelRegistry.js";
import { isTierBand, TIER_BANDS, type TierBand } from "../../eval/tierBands.js";

/**
 * Claude-style routing tiers used across the pipeline routing layer —
 * derived from the `TIER_BANDS` authority (#581), never re-listed (#582).
 */
export type CodexTier = TierBand;

export interface CodexModelMeta {
  /** Recommended default model for most pipeline work. */
  recommended?: boolean;
  /** Research-preview model — excluded from default catalog/UI listings. */
  researchPreview?: boolean;
  /** Deprecated by OpenAI — never auto-selected; carries a replacement id. */
  deprecated?: boolean;
  /** For deprecated models, the current id callers should migrate to. */
  replacement?: string;
  /**
   * False ONLY when the model's `transports[<codex transport>].served` fact
   * is explicitly `false` (#600). An absent transports map, or an absent key
   * for the codex transport, is the unexpressed/pending state and reads as
   * `true` — the same additive semantics {@link checkTransportServed}
   * enforces (#579 AC4). Always set (not sparse like the flags above) so
   * every consumer can read it without an `?? true` fallback.
   */
  servedOverTransport: boolean;
}

/** The transport axis (#600) that gates codex model reachability. */
const CODEX_TRANSPORT = mustTransportForAdapter("codex");

function servedOverCodexTransport(m: (typeof MODEL_REGISTRY)[number]): boolean {
  return m.transports?.[CODEX_TRANSPORT]?.served !== false;
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
      servedOverTransport: servedOverCodexTransport(m),
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
export const CODEX_TIER_MODEL_MAP: Record<CodexTier, string> = Object.fromEntries(
  TIER_BANDS.map((tier) => [tier, mustResolveCodexTier(tier)])
) as Record<CodexTier, string>;

/** Recommended frontier default (opus/fable tiers + the "recommended" UI tag). */
export const CODEX_RECOMMENDED_DEFAULT_MODEL = CODEX_TIER_MODEL_MAP.opus;

/** Base default model for the sonnet tier and the config `codex.model` default. */
export const CODEX_DEFAULT_BASE_MODEL = CODEX_TIER_MODEL_MAP.sonnet;

function isCodexTier(value: string): value is CodexTier {
  return isTierBand(value);
}

/**
 * Pure predicate core of {@link isValidCodexModel} (#600), exported
 * separately so the served-filtering behavior is directly unit-testable
 * against synthetic {@link CodexModelMeta} data: no current `openai` registry
 * entry has `transports.<codex transport>.served: false`, so a test against
 * the LIVE registry cannot exercise that branch — mirrors the same
 * hand-constructed-fixture approach `internal/models/registry_axes_test.go`'s
 * `TestValidateTransportsGraduated` uses on the Go side.
 */
export function isServedCodexModelMeta(meta: CodexModelMeta | undefined): boolean {
  return meta !== undefined && meta.servedOverTransport;
}

/**
 * True when `id` is a model the registry knows (including deprecated/preview)
 * AND the codex adapter's transport does not explicitly declare it unserved
 * (#600). This is defense-in-depth, not the primary enforcement gate —
 * `validateModelForAdapter` (`modelPreflight.ts`) already consults
 * `checkTransportServed` FIRST and throws before this predicate is ever
 * reached for a known-but-unserved id; it exists so this predicate can never
 * disagree with that gate if a caller ever consults it directly.
 */
export function isValidCodexModel(id: string): boolean {
  if (!Object.prototype.hasOwnProperty.call(CODEX_MODELS, id)) return false;
  return isServedCodexModelMeta(CODEX_MODELS[id]);
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
  /**
   * Include models the codex adapter's transport explicitly declares
   * unserved (#600). Default false: a suggestion/remediation list should
   * never recommend a model `checkTransportServed` would reject.
   */
  includeUnserved?: boolean;
}

/**
 * Pure filter/sort core of {@link listCodexModels} (#600), exported
 * separately so the served-filtering behavior is directly unit-testable
 * against synthetic `Record<string, CodexModelMeta>` data — see
 * {@link isServedCodexModelMeta}'s doc for why a live-registry test cannot
 * exercise the served:false branch today.
 */
export function filterCodexModelIds(
  models: Record<string, CodexModelMeta>,
  opts: ListCodexModelsOptions = {}
): string[] {
  const {
    includeDeprecated = false,
    includeResearchPreview = false,
    includeUnserved = false,
  } = opts;
  return Object.entries(models)
    .filter(([, meta]) => {
      if (meta.deprecated && !includeDeprecated) return false;
      if (meta.researchPreview && !includeResearchPreview) return false;
      if (!meta.servedOverTransport && !includeUnserved) return false;
      return true;
    })
    .sort(([, a], [, b]) => Number(b.recommended ?? false) - Number(a.recommended ?? false))
    .map(([id]) => id);
}

/**
 * List known Codex model ids, recommended-first. By default excludes
 * deprecated, research-preview, and transport-unserved models (#600) — the
 * right set for catalog fallbacks, UI pickers, and preflight remediation
 * suggestions. Pass options to widen the set.
 */
export function listCodexModels(opts: ListCodexModelsOptions = {}): string[] {
  return filterCodexModelIds(CODEX_MODELS, opts);
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
