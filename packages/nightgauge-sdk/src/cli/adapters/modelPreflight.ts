/**
 * Provider-aware model preflight — fail fast on an invalid (adapter, model)
 * pair BEFORE the model reaches a CLI/SDK, with an actionable remediation
 * message instead of an opaque error at spawn/query time.
 *
 * This is the single entry point every model-resolution path funnels through
 * (SDK adapters, the codex preflight command, and the VSCode skillRunner just
 * before spawn). It resolves any Claude-style routing tier
 * (`haiku|sonnet|opus|fable`) to a concrete per-adapter model and then, for
 * adapters with a CLOSED known set, asserts the resolved id is valid — throwing
 * an {@link AdapterError} (`CONFIG_INVALID`) with a nearest-valid suggestion
 * when it is not.
 *
 * Set membership is policy-driven via {@link ADAPTER_MODEL_POLICY}:
 *   - CLOSED (codex, gemini, gemini-sdk): a finite, maintained set; unknown ids
 *     are rejected. Codex reuses the canonical {@link isValidCodexModel}/
 *     {@link resolveCodexModelAlias} registry (#4018) so there is exactly one
 *     Codex model list in the codebase. Gemini uses {@link GEMINI_MODELS}.
 *   - OPEN (claude-sdk, claude-headless, ollama, lm-studio, copilot): no closed
 *     set. claude-* accept tier keywords natively (the tier IS a valid model);
 *     ollama/lm-studio draw from a user-defined local catalog unknowable at
 *     preflight; the copilot CLI adapter does not consume a model id. These
 *     never reject — they pass the (trimmed) value through.
 *
 * @see Issue #4021 — Model↔provider validation preflight (fail fast)
 * @see Issue #4018 — Canonical Codex model registry (the dependency reused here)
 */

import {
  isValidCodexModel,
  listCodexModels,
  resolveCodexModelAlias,
} from "./codexModelRegistry.js";
import { getModelDescriptor, MODEL_REGISTRY } from "../../eval/modelRegistry.js";
import { AdapterError } from "./errors.js";
import type { IncrediAdapter } from "./ICliAdapter.js";

/** Whether an adapter has a finite, validatable model set. */
export type ModelSetKind = "closed" | "open";

/**
 * The four canonical routing tiers. For a CLOSED adapter, a bare tier must
 * always resolve to a concrete model and never survive to the CLI as `--model`.
 */
const TIER_KEYWORDS = ["haiku", "sonnet", "opus", "fable"] as const;
type TierKeyword = (typeof TIER_KEYWORDS)[number];

function isTierKeyword(value: string): value is TierKeyword {
  return (TIER_KEYWORDS as readonly string[]).includes(value);
}

// ---------------------------------------------------------------------------
// Gemini closed set
// ---------------------------------------------------------------------------

/**
 * Known Gemini models (recommended-first), derived from the model registry's
 * `provider: "google"` entries — the same single source the Codex
 * {@link listCodexModels} set resolves through (#56). Add new GA ids to
 * `eval/model-registry.json`; remove retired ids there.
 */
export const GEMINI_MODELS: readonly string[] = MODEL_REGISTRY.filter(
  (m) => m.provider === "google" && !m.deprecated
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
 * Per-adapter model policy. Every {@link IncrediAdapter} union member MUST have
 * an entry — the `Record<IncrediAdapter, …>` type makes a missing adapter a
 * compile error, and `modelPreflight.test.ts` asserts it at runtime so adding a
 * new adapter forces a policy decision (no silent open-by-default fallthrough).
 */
export const ADAPTER_MODEL_POLICY: Record<IncrediAdapter, AdapterModelPolicy> = {
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
      `Fix: set ${policy.envVar} to one of the valid models, or a tier (haiku|sonnet|opus|fable).`
    );
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
  adapter: IncrediAdapter,
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
  adapter: IncrediAdapter,
  model: string | undefined
): string | undefined {
  const result = validateModelForAdapter(adapter, model);
  return result.model || undefined;
}
