/**
 * Provider-aware model preflight — fail fast on an invalid (adapter, model)
 * pair BEFORE the model reaches a CLI/SDK, with an actionable remediation
 * message instead of an opaque error at spawn/query time.
 *
 * This is the single entry point every model-resolution path funnels through
 * (SDK adapters, the codex preflight command, and the VSCode skillRunner just
 * before spawn). It resolves any Claude-style routing tier
 * (`haiku|sonnet|opus|fable`) to a concrete per-adapter model and then, for
 * adapters with a CLOSED known set, asserts the resolved id is BOTH a known
 * id AND reachable through the adapter's transport (`checkTransportServed`,
 * #579) — throwing an {@link AdapterError} (`CONFIG_INVALID`) when it is not:
 * a transport-specific message naming provider/model/transport when the id is
 * known but `transports.cli.served: false`, or a nearest-valid suggestion
 * from the known set when the id is unrecognized entirely.
 *
 * Set membership is policy-driven via {@link ADAPTER_MODEL_POLICY}:
 *   - CLOSED (codex, gemini, gemini-sdk, grok): a finite, maintained set;
 *     unknown ids are rejected. Codex reuses the canonical
 *     {@link isValidCodexModel}/{@link resolveCodexModelAlias} registry
 *     (#4018) so there is exactly one Codex model list in the codebase.
 *     Gemini uses {@link GEMINI_MODELS}; Grok uses {@link GROK_MODELS}
 *     (absorbing #552 — a Resolve miss used to hand the raw band name
 *     straight to `grok --model`, reaching the CLI unchecked).
 *   - OPEN (claude-sdk, claude-headless, ollama, lm-studio, copilot): no closed
 *     set. claude-* accept tier keywords natively (the tier IS a valid model);
 *     ollama/lm-studio draw from a user-defined local catalog unknowable at
 *     preflight; the copilot CLI adapter does not consume a model id. These
 *     never reject — they pass the (trimmed) value through.
 *
 * A model with NO `transports.cli` fact at all (unexpressed/pending, e.g.
 * most claude/gemini entries pre-#578) is never treated as unreachable — only
 * an EXPLICIT `served: false` fails closed (#579 AC4: additive enforcement).
 *
 * @see Issue #4021 — Model↔provider validation preflight (fail fast)
 * @see Issue #4018 — Canonical Codex model registry (the dependency reused here)
 * @see Issue #579 — Transport-reachability fail-closed enforcement at selection
 */

import {
  isValidCodexModel,
  listCodexModels,
  resolveCodexModelAlias,
} from "./codexModelRegistry.js";
import {
  checkTransportServed,
  getModelDescriptor,
  mustTransportForAdapter,
  MODEL_REGISTRY,
  providerForAdapter,
  type TransportServedResult,
} from "../../eval/modelRegistry.js";
import { isTierBand, TIER_BAND_ALTERNATION, type TierBand } from "../../eval/tierBands.js";
import { AdapterError } from "./errors.js";
import type { NightgaugeAdapter } from "./ICliAdapter.js";

/** Whether an adapter has a finite, validatable model set. */
export type ModelSetKind = "closed" | "open";

/**
 * The canonical routing tiers, derived from the `TIER_BANDS` authority (#581)
 * — never re-listed (#582). For a CLOSED adapter, a bare tier must always
 * resolve to a concrete model and never survive to the CLI as `--model`.
 */
type TierKeyword = TierBand;

function isTierKeyword(value: string): value is TierKeyword {
  return isTierBand(value);
}

/**
 * True unless `m` explicitly declares `transports[transport].served: false`.
 * An absent `transports` map, or an absent key for `transport`, is the
 * unexpressed/pending state and counts as served (#579 AC4 — additive
 * enforcement). Used to keep the GEMINI_MODELS/GROK_MODELS suggestion lists
 * free of models the closed-set check would reject anyway via
 * {@link checkTransportServed}, which is the authoritative gate this
 * predicate only mirrors for listing purposes.
 */
function servedOverTransport(
  m: (typeof MODEL_REGISTRY)[number],
  transport: ReturnType<typeof mustTransportForAdapter>
): boolean {
  return m.transports?.[transport]?.served !== false;
}

// ---------------------------------------------------------------------------
// Gemini closed set
// ---------------------------------------------------------------------------

/**
 * The transport axis (#600) that gates the Gemini closed set. `gemini` and
 * `gemini-sdk` are DELIBERATELY pinned to the SAME transport (`"cli"` today —
 * see {@link mustTransportForAdapter}) precisely because {@link GEMINI_MODELS}
 * is a single shared list feeding both policy entries; if the single
 * authority ever assigned them different transports this list would be wrong
 * for whichever adapter it was NOT computed from — `modelRegistry.test.ts`
 * pins both to the identical value so that divergence fails loud instead of
 * silently.
 */
const GEMINI_TRANSPORT = mustTransportForAdapter("gemini");

/**
 * Known Gemini models (recommended-first), derived from the model registry's
 * `provider: "google"` entries that are also transport-reachable (#579,
 * #600) — the same single source the Codex {@link listCodexModels} set
 * resolves through (#56). Add new GA ids to `eval/model-registry.json`;
 * remove retired ids there.
 */
export const GEMINI_MODELS: readonly string[] = MODEL_REGISTRY.filter(
  (m) => m.provider === "google" && !m.deprecated && servedOverTransport(m, GEMINI_TRANSPORT)
)
  .sort((a, b) => Number(b.recommended ?? false) - Number(a.recommended ?? false))
  .map((m) => m.id);

/**
 * Claude-tier → Gemini-model routing, resolved from the registry's tier bands
 * (haiku+sonnet → gemini-2.5-flash, opus+fable → gemini-2.5-pro). This is the
 * same lookup AutoProviderRouter uses — one source, no mirrored tables.
 */
function resolveGeminiModel(model: string | undefined): string | undefined {
  if (!model) return undefined;
  const trimmed = model.trim();
  if (!trimmed) return undefined;
  if (isTierKeyword(trimmed)) return getModelDescriptor(trimmed, "google")?.id ?? trimmed;
  return trimmed;
}

function isValidGeminiModel(id: string): boolean {
  return GEMINI_MODELS.includes(id);
}

/** The transport axis (#600) that gates the Grok closed set. */
const GROK_TRANSPORT = mustTransportForAdapter("grok");

// Known xai models that are transport-reachable (#579, #600): grok-build-0.1
// is excluded by BOTH `!m.deprecated` and `servedOverTransport`
// independently — it keeps `deprecated: true` for historical cost replay,
// but its unselectability no longer rests on that flag alone (#578 landed
// `transports.cli.served: false` as an INDEPENDENT reason).
export const GROK_MODELS: readonly string[] = MODEL_REGISTRY.filter(
  (m) => m.provider === "xai" && !m.deprecated && servedOverTransport(m, GROK_TRANSPORT)
)
  .sort((a, b) => Number(b.recommended ?? false) - Number(a.recommended ?? false))
  .map((m) => m.id);

function resolveGrokModel(model: string | undefined): string | undefined {
  if (!model) return undefined;
  const trimmed = model.trim();
  if (!trimmed) return undefined;
  if (isTierKeyword(trimmed)) return getModelDescriptor(trimmed, "xai")?.id ?? trimmed;
  return trimmed;
}

function isValidGrokModel(id: string): boolean {
  return GROK_MODELS.includes(id);
}

// ---------------------------------------------------------------------------
// Copilot open-set tier resolution
// ---------------------------------------------------------------------------

/**
 * Claude-tier → Copilot-model routing, resolved from the registry's
 * `provider: "copilot"` band assignments (the same single source the Go
 * resolveCopilotModel reads, giving Go↔SDK parity like codex #56). Copilot is
 * an OPEN set — its live catalog is larger than the registry bands and the CLI
 * validates server-side — so a concrete id (or any unknown value) passes
 * through unchanged; only a bare routing tier is translated to a concrete id so
 * "sonnet" never reaches `--model` literally (#52).
 */
function resolveCopilotModel(model: string | undefined): string | undefined {
  const trimmed = model?.trim();
  if (!trimmed) return undefined;
  if (isTierKeyword(trimmed)) return getModelDescriptor(trimmed, "copilot")?.id ?? trimmed;
  return trimmed;
}

// ---------------------------------------------------------------------------
// Policy table
// ---------------------------------------------------------------------------

export interface AdapterModelPolicy {
  /** Closed sets are validated; open sets pass through. */
  kind: ModelSetKind;
  /** Human-readable adapter name for AdapterError formatting. */
  displayName: string;
  /** The `NIGHTGAUGE_*_MODEL` env var that configures this adapter (for remediation text). */
  envVar?: string;
  /** Docs URL surfaced in the remediation message. */
  docsUrl?: string;
  /**
   * Resolve a tier keyword (or pass an exact id) to a concrete model id. Returns
   * `undefined` for empty input (meaning "no override — use the adapter default").
   */
  resolve: (model: string | undefined) => string | undefined;
  /** CLOSED only — enumerate valid concrete ids (recommended-first) for the suggestion engine. */
  validIds?: () => string[];
  /** CLOSED only — predicate the resolved id must satisfy. */
  isValid?: (id: string) => boolean;
}

/** Identity resolver for OPEN adapters: pass the trimmed value through. */
function identityResolve(model: string | undefined): string | undefined {
  const trimmed = model?.trim();
  return trimmed ? trimmed : undefined;
}

const CODEX_DOCS_URL = "https://developers.openai.com/codex";
const GEMINI_DOCS_URL = "https://ai.google.dev/gemini-api/docs";

/**
 * Per-adapter model policy. Every {@link NightgaugeAdapter} union member MUST have
 * an entry — the `Record<NightgaugeAdapter, …>` type makes a missing adapter a
 * compile error, and `modelPreflight.test.ts` asserts it at runtime so adding a
 * new adapter forces a policy decision (no silent open-by-default fallthrough).
 */
export const ADAPTER_MODEL_POLICY: Record<NightgaugeAdapter, AdapterModelPolicy> = {
  "claude-sdk": {
    kind: "open",
    displayName: "Claude SDK",
    resolve: identityResolve,
  },
  "claude-headless": {
    kind: "open",
    displayName: "Claude",
    resolve: identityResolve,
  },
  codex: {
    kind: "closed",
    displayName: "Codex",
    envVar: "NIGHTGAUGE_CODEX_MODEL",
    docsUrl: CODEX_DOCS_URL,
    resolve: resolveCodexModelAlias,
    validIds: () => listCodexModels(),
    isValid: isValidCodexModel,
  },
  gemini: {
    kind: "closed",
    displayName: "Gemini",
    envVar: "NIGHTGAUGE_GEMINI_MODEL",
    docsUrl: GEMINI_DOCS_URL,
    resolve: resolveGeminiModel,
    validIds: () => [...GEMINI_MODELS],
    isValid: isValidGeminiModel,
  },
  "gemini-sdk": {
    kind: "closed",
    displayName: "Gemini SDK",
    envVar: "NIGHTGAUGE_GEMINI_MODEL",
    docsUrl: GEMINI_DOCS_URL,
    resolve: resolveGeminiModel,
    validIds: () => [...GEMINI_MODELS],
    isValid: isValidGeminiModel,
  },
  // OPEN — user-defined local catalog, unknowable at preflight. Presence (empty
  // vs set) is enforced by the adapters themselves; validity is not our call.
  "lm-studio": {
    kind: "open",
    displayName: "LM Studio",
    envVar: "NIGHTGAUGE_LM_STUDIO_MODEL",
    resolve: identityResolve,
  },
  ollama: {
    kind: "open",
    displayName: "Ollama",
    envVar: "NIGHTGAUGE_OLLAMA_MODEL",
    resolve: identityResolve,
  },
  // OPEN — the Copilot CLI `--model` flag now actually forces the model (#52),
  // but copilot's live catalog is larger than the registry bands and the CLI
  // validates server-side, so a strict set would reject valid ids like
  // "gpt-5.2". Tiers resolve to a concrete copilot-hosted id; concrete/unknown
  // ids pass through.
  copilot: {
    kind: "open",
    displayName: "Copilot",
    envVar: "NIGHTGAUGE_COPILOT_MODEL",
    resolve: resolveCopilotModel,
  },
  grok: {
    kind: "closed",
    displayName: "Grok",
    envVar: "NIGHTGAUGE_GROK_MODEL",
    docsUrl: "https://docs.x.ai/build/overview",
    resolve: resolveGrokModel,
    validIds: () => [...GROK_MODELS],
    isValid: isValidGrokModel,
  },
};

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

export interface ModelValidationResult {
  /** The concrete model id that should be passed to the CLI/SDK ("" when no override). */
  model: string;
  /** True when the input was a tier keyword that was resolved to a concrete id. */
  resolvedFromTier: boolean;
}

/** Case-insensitive Levenshtein edit distance (small inputs — straightforward DP). */
function levenshtein(a: string, b: string): number {
  const s = a.toLowerCase();
  const t = b.toLowerCase();
  const rows = s.length + 1;
  const cols = t.length + 1;
  const dist: number[][] = Array.from({ length: rows }, () => new Array<number>(cols).fill(0));
  for (let i = 0; i < rows; i++) dist[i][0] = i;
  for (let j = 0; j < cols; j++) dist[0][j] = j;
  for (let i = 1; i < rows; i++) {
    for (let j = 1; j < cols; j++) {
      const cost = s[i - 1] === t[j - 1] ? 0 : 1;
      dist[i][j] = Math.min(dist[i - 1][j] + 1, dist[i][j - 1] + 1, dist[i - 1][j - 1] + cost);
    }
  }
  return dist[s.length][t.length];
}

/**
 * Nearest valid id by edit distance, within a forgiving threshold. Returns
 * `undefined` when nothing is close enough (so we don't suggest gibberish).
 * Ties break lexicographically for determinism.
 */
function nearestValid(input: string, candidates: string[]): string | undefined {
  let best: string | undefined;
  let bestDist = Number.POSITIVE_INFINITY;
  // Forgiving enough to catch a fat-fingered suffix ("gpt-5.5x" → "gpt-5.5") or
  // a wrong dotted version, but tight enough that unrelated junk
  // ("totally-made-up") yields no suggestion.
  const threshold = Math.max(3, Math.ceil(input.length / 2));
  for (const candidate of candidates) {
    const dist = levenshtein(input, candidate);
    if (dist < bestDist || (dist === bestDist && best !== undefined && candidate < best)) {
      bestDist = dist;
      best = candidate;
    }
  }
  return best !== undefined && bestDist <= threshold ? best : undefined;
}

function buildInvalidModelError(
  policy: AdapterModelPolicy,
  input: string,
  resolved: string
): AdapterError {
  const validIds = policy.validIds?.() ?? [];
  const suggestion = nearestValid(resolved, validIds) ?? nearestValid(input, validIds);
  const resolvedNote = resolved !== input ? ` (resolved to '${resolved}')` : "";
  const lines = [
    `Model '${input}' is not valid for the ${policy.displayName} adapter${resolvedNote}.`,
    `Valid models: ${validIds.join(", ")}.`,
  ];
  if (suggestion) {
    lines.push(`Did you mean '${suggestion}'?`);
  }
  if (policy.envVar) {
    lines.push(
      `Fix: set ${policy.envVar} to one of the valid models, or a tier (${TIER_BAND_ALTERNATION}).`
    );
  }
  return new AdapterError(lines.join("\n"), "CONFIG_INVALID", policy.displayName, policy.docsUrl);
}

/**
 * The classified fail-closed error for #579: `unreachable.model` IS a known
 * registry entry, but `transports[unreachable.transport]` explicitly declares
 * `served: false` for the dispatching adapter's provider — it is not the
 * generic "unknown model" case {@link buildInvalidModelError} handles, so the
 * remediation text names the transport fact directly rather than only
 * appearing buried in a valid-models list.
 */
function buildTransportUnreachableError(
  policy: AdapterModelPolicy,
  unreachable: NonNullable<TransportServedResult["unreachable"]>
): AdapterError {
  const lines = [
    `Model '${unreachable.model}' is not valid for the ${policy.displayName} adapter: ` +
      `not served over the '${unreachable.transport}' transport ` +
      `(transports.${unreachable.transport}.served=false, provider '${unreachable.provider}').`,
    `Choose a served model, or dispatch through a transport that serves it.`,
  ];
  if (policy.envVar) {
    lines.push(`Fix: set ${policy.envVar} to a served model.`);
  }
  return new AdapterError(lines.join("\n"), "CONFIG_INVALID", policy.displayName, policy.docsUrl);
}

/**
 * Validate (and resolve) a model for an adapter. Throws an
 * {@link AdapterError} (`CONFIG_INVALID`) when the resolved model is invalid for
 * a CLOSED adapter; otherwise returns the concrete model that should run.
 *
 * Empty/undefined input is not an error — it means "no override; use the
 * adapter default" and returns `{ model: "" }`. This is the single function all
 * preflight call sites use.
 */
export function validateModelForAdapter(
  adapter: NightgaugeAdapter,
  model: string | undefined
): ModelValidationResult {
  const policy = ADAPTER_MODEL_POLICY[adapter];
  if (!policy) {
    // Defensive — the Record type prevents this at compile time.
    throw new AdapterError(
      `No model policy is defined for adapter "${String(adapter)}".`,
      "CONFIG_INVALID",
      String(adapter)
    );
  }

  const trimmed = model?.trim();
  if (!trimmed) {
    return { model: "", resolvedFromTier: false };
  }

  const resolvedFromTier = isTierKeyword(trimmed);
  const resolved = policy.resolve(trimmed) ?? trimmed;

  if (policy.kind === "closed") {
    // #579/#600: consult the transport fact FIRST, so a model that IS a known
    // registry entry but explicitly unreachable through THIS adapter's
    // transport (resolved per-adapter from the single authority, NOT a
    // hardcoded "cli" literal — see mustTransportForAdapter) fails closed
    // with an error naming provider, model, and transport — distinct from
    // the generic "unknown model" case below, which the closed set
    // (policy.isValid) still handles for ids the registry has never heard
    // of. A model with no transports fact for that transport
    // (unexpressed/pending) is NOT unreachable here — it falls through to the
    // ordinary isValid check, which is the additive-enforcement semantics
    // (#579 AC4).
    const transportCheck = checkTransportServed(
      providerForAdapter(adapter),
      mustTransportForAdapter(adapter),
      resolved
    );
    if (transportCheck.unreachable) {
      throw buildTransportUnreachableError(policy, transportCheck.unreachable);
    }

    const valid = policy.isValid ? policy.isValid(resolved) : false;
    if (!valid) {
      throw buildInvalidModelError(policy, trimmed, resolved);
    }
  }

  return { model: resolved, resolvedFromTier };
}

/**
 * Convenience wrapper: returns the resolved concrete model id, or `undefined`
 * when there is no override (adapters then fall back to their own default).
 * Throws on an invalid (adapter, model) pair — identical semantics to
 * {@link validateModelForAdapter}.
 */
export function resolveAndValidateModel(
  adapter: NightgaugeAdapter,
  model: string | undefined
): string | undefined {
  const result = validateModelForAdapter(adapter, model);
  return result.model || undefined;
}
