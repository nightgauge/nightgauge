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
import {
  AdapterTransportsSchema,
  EFFORT_LEVELS,
  EffortLevelSchema,
  ModelDescriptorSchema,
  THINKING_DISABLE_NEVER,
  type AdapterTransports,
  type Behavior,
  type EffortLevel,
  type ModelDescriptor,
  type Propensity,
  type Provider,
  type ThinkingDisableLimit,
  type Transport,
} from "./modelEvalSchemas.js";
import type { ModelTier } from "../analysis/AutoModelSelector.js";
import { TIER_BANDS } from "./tierBands.js";
import type { ModelCostRate } from "../analysis/types.js";

/**
 * Rates applied when a model id is unknown to the registry: a truthful $0,
 * never a fabricated tier default. Local (ollama/lm-studio) models land here
 * by design. Callers that record costs should surface {@link isKnownModel}
 * so a $0 total is distinguishable from "not billed". Mirrors the Go
 * `tokens.CalculateCost` default.
 */
const UNKNOWN_MODEL_RATES = {
  input: 0,
  output: 0,
  cache_read: 0,
  cache_creation_5m: 0,
  cache_creation_1h: 0,
} as const;

/**
 * Shape of the canonical registry file. Extra top-level keys (e.g.
 * `$schema_note`) are ignored. Exported so the extension's packaging test can
 * parse the packaged `dist/model-registry.json` through the EXACT schema the
 * loader uses — a stale dist copy plus this strict schema is a load-time
 * crash, so skew must be a red test instead (#436).
 */
export const RegistryFileSchema = z
  .object({
    version: z.string(),
    /**
     * The effort ladder as DATA (#578) — the single data authority both
     * languages read. Must equal `EFFORT_LEVELS` exactly (order and
     * membership); {@link assertEffortLevelsMatchAuthority} throws at load
     * otherwise.
     */
    effort_levels: z.array(EffortLevelSchema),
    models: z.array(ModelDescriptorSchema).min(1),
    /**
     * The single-authority adapter→transport-axis mapping (#600) — see
     * {@link transportForAdapter}. {@link assertAdapterTransportsComplete}
     * asserts it declares exactly the closed-transport-adapter set at load.
     */
    adapter_transports: AdapterTransportsSchema,
  })
  .passthrough();

/**
 * Loader-level assert (#578): the registry's `effort_levels` declaration must
 * equal the compile-time authority {@link EFFORT_LEVELS} exactly — order as
 * much as membership, because the ladder is ascending reasoning depth and the
 * clamps index against it. Two authorities that can disagree are how the
 * pre-#394 vocabulary silently dropped `max`.
 */
export function assertEffortLevelsMatchAuthority(effortLevels: readonly string[]): void {
  const authority = EFFORT_LEVELS as readonly string[];
  const matches =
    effortLevels.length === authority.length && authority.every((l, i) => effortLevels[i] === l);
  if (!matches) {
    throw new Error(
      `model-registry.json: effort_levels ${JSON.stringify(effortLevels)} must equal ` +
        `EFFORT_LEVELS ${JSON.stringify(authority)} exactly (order and membership, #394/#578)`
    );
  }
}

/**
 * Loader-level assert (#578): a per-transport rate card without provenance is
 * an unattributable figure — `rate_provenance` is mandatory wherever
 * transport `rates` appear, the same pattern as the band-uniqueness throw.
 */
export function assertTransportRatesCarryProvenance(models: readonly ModelDescriptor[]): void {
  for (const m of models) {
    for (const [transport, facts] of Object.entries(m.transports ?? {})) {
      if (facts?.rates && !facts.rate_provenance) {
        throw new Error(
          `model-registry.json: ${m.id} transports.${transport} declares rates without ` +
            `rate_provenance — every transport rate card must state where its figures came from`
        );
      }
    }
  }
}

/**
 * Loader-level graduation assert (#600): every NON-DEPRECATED entry must
 * declare at least one transports fact. The additive fail-open behavior
 * {@link checkTransportServed} gives an absent transport key exists for
 * entries whose reachability genuinely has not been assessed yet — today
 * that is exactly the deprecated openai/google ids (unverified CLI
 * reachability) and the vendor-x-pro fixture (kept deprecated specifically so
 * it still exercises that branch). A non-deprecated entry with NO transports
 * block at all would mean a model still being routed to has never been
 * assessed for reachability on any transport — that must fail LOUD at load
 * time, naming the entry, rather than silently reading as "served" through
 * selection.
 */
export function assertNonDeprecatedModelsDeclareTransports(
  models: readonly ModelDescriptor[]
): void {
  for (const m of models) {
    if (m.deprecated) continue;
    if (!m.transports || Object.keys(m.transports).length === 0) {
      throw new Error(
        `model-registry.json: ${m.id} is a non-deprecated entry with no transports block — ` +
          `every non-deprecated entry must state at least one transport's reachability facts ` +
          `(#600); mark it deprecated if reachability is genuinely unassessed, or add transports facts`
      );
    }
  }
}

/**
 * The exact set of adapters whose model preflight consults a transport fact
 * — the `kind: "closed"` entries in `ADAPTER_MODEL_POLICY`
 * (`cli/adapters/modelPreflight.ts`) that actually call
 * {@link checkTransportServed}. Declared here (rather than imported from
 * modelPreflight.ts) to avoid a circular import — that module already
 * imports FROM this one.
 */
export const CLOSED_TRANSPORT_ADAPTERS = ["codex", "gemini", "gemini-sdk", "grok"] as const;

/**
 * Loader-level assert (#600) on the single-authority adapter→transport
 * mapping: `adapter_transports` must declare EXACTLY the
 * {@link CLOSED_TRANSPORT_ADAPTERS} set (membership, not just a superset — a
 * stale or missing entry is a silent drift risk identical to band
 * uniqueness). Value validity (closed `cli|api` set) is already structurally
 * enforced by {@link AdapterTransportsSchema}.
 */
export function assertAdapterTransportsComplete(table: AdapterTransports): void {
  const got = Object.keys(table).sort();
  const want = [...CLOSED_TRANSPORT_ADAPTERS].sort();
  const matches = got.length === want.length && want.every((a, i) => got[i] === a);
  if (!matches) {
    throw new Error(
      `model-registry.json: adapter_transports keys ${JSON.stringify(got)} must equal the ` +
        `closed-transport-adapter set ${JSON.stringify(want)} exactly (#600)`
    );
  }
}

function loadRegistry(): { models: ModelDescriptor[]; adapterTransports: AdapterTransports } {
  const raw = readFileSync(resolve(__dirname, "model-registry.json"), "utf-8");
  const parsed = RegistryFileSchema.parse(JSON.parse(raw));
  assertEffortLevelsMatchAuthority(parsed.effort_levels);
  assertTransportRatesCarryProvenance(parsed.models);
  assertNonDeprecatedModelsDeclareTransports(parsed.models);
  assertAdapterTransportsComplete(parsed.adapter_transports);
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
  return { models: parsed.models, adapterTransports: parsed.adapter_transports };
}

const loaded = loadRegistry();

/** The full registry (all models, including deprecated ones kept for cost replay). */
export const MODEL_REGISTRY: readonly ModelDescriptor[] = Object.freeze(loaded.models);

/** The single-authority adapter→transport mapping (#600), frozen at load. */
const ADAPTER_TRANSPORTS: AdapterTransports = Object.freeze(loaded.adapterTransports);

/**
 * Resolve the single-authority transport axis (#600) `adapter`'s model
 * preflight must consult. `undefined` means `adapter` is not in the
 * closed-transport-adapter set — OPEN adapters and unrecognized names never
 * had a transport check to make.
 *
 * `gemini-sdk` is pinned to `"cli"`, NOT `"api"`, despite its name and its
 * `kindSDK` doctor classification: the Go `gemini-sdk` adapter is the
 * pipeline's actual dispatch path for that adapter name and it genuinely
 * spawns the agentic `gemini` CLI, while the TypeScript `gemini-sdk` adapter
 * — though it does call the `@google/genai` API library directly — is
 * chat-completion-only and barred from pipeline stage dispatch (#57). See the
 * registry JSON's `$schema_note` for the full judgment-call rationale.
 */
export function transportForAdapter(adapter: string): Transport | undefined {
  return ADAPTER_TRANSPORTS[adapter];
}

/**
 * Like {@link transportForAdapter} but throws instead of returning
 * `undefined` — for CLOSED-set call sites (`modelPreflight.ts`,
 * `codexModelRegistry.ts`) where `adapter` is always a member of
 * {@link CLOSED_TRANSPORT_ADAPTERS} and {@link assertAdapterTransportsComplete}
 * already guarantees the table covers it at load time. A throw here means a
 * NEW closed adapter was wired into preflight without a deliberate
 * `adapter_transports` decision — a programming error, not a runtime
 * condition, mirroring the Go `codexTransport`/`grokTransport`/
 * `geminiTransport` panic-on-missing helpers.
 */
export function mustTransportForAdapter(adapter: string): Transport {
  const t = transportForAdapter(adapter);
  if (!t) {
    throw new Error(`model-registry.json: adapter_transports has no entry for "${adapter}"`);
  }
  return t;
}

/** Models that are not deprecated — the set the pipeline should route to today. */
export function activeModels(): ModelDescriptor[] {
  return MODEL_REGISTRY.filter((m) => !m.deprecated);
}

/**
 * Map an execution adapter name (any layer's vocabulary: `claude`,
 * `claude-sdk`, `claude-headless`, `codex`, `gemini`, `gemini-sdk`,
 * `grok`, `grok-headless`, `copilot`, `ollama`, `lm-studio`) to its
 * registry provider. Unknown adapters map to `other`, which has no tier bands.
 */
export function providerForAdapter(adapter: string): Provider {
  if (adapter === "claude" || adapter.startsWith("claude-")) return "anthropic";
  if (adapter === "codex") return "openai";
  if (adapter === "gemini" || adapter === "gemini-sdk") return "google";
  if (adapter === "grok" || adapter.startsWith("grok-")) return "xai";
  if (adapter === "copilot") return "copilot";
  if (adapter === "ollama") return "ollama";
  if (adapter === "lm-studio") return "lm-studio";
  return "other";
}

/** The one multi-provider adapter: the model it runs, not its name, decides the provider (ADR-022). */
const OPENCODE_ADAPTER = "opencode";

/**
 * Map an execution adapter and the model it dispatches to the provider that
 * serves the model. For `opencode` the provider comes from the model's
 * provider key ({@link parseOpenCodeModel}). Every other adapter serves one
 * provider, so its answer is {@link providerForAdapter}'s, whatever the
 * model. Mirrors the Go `models.ProviderFor`.
 */
export function providerFor(adapter: string, model: string): Provider {
  if (adapter === OPENCODE_ADAPTER) return parseOpenCodeModel(model).provider;
  return providerForAdapter(adapter);
}

/**
 * Whether `provider` runs its models on a server the operator runs
 * (`lm-studio`, `ollama`). The single authority for that question: `other` is
 * never local, so an unrecognized provider can never be priced as a local $0.
 * Mirrors the Go `models.IsLocalProvider`.
 */
export function isLocalProvider(provider: string): boolean {
  return provider === "lm-studio" || provider === "ollama";
}

/** A declared OpenCode endpoint (#1678): its id is the provider key a stage names on `-m`. */
export interface LocalEndpoint {
  id: string;
  /** The endpoint's kind: `lm-studio`, `ollama` or `openai-compatible`. */
  provider: string;
  /** The server's OpenAI-compatible API root. */
  baseUrl: string;
}

/** An IPv4 literal on loopback or a private network (RFC 1918). */
function isLocalIPv4(host: string): boolean {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(host);
  if (!m) return false;
  const [a, b] = [Number(m[1]), Number(m[2])];
  if (m.slice(1).some((o) => Number(o) > 255)) return false;
  return a === 127 || a === 10 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168);
}

/** An IPv6 literal (brackets stripped) on loopback or unique-local (fc00::/7). */
function isLocalIPv6(host: string): boolean {
  if (!host.includes(":")) return false;
  if (host === "::1") return true;
  const mapped = /^::ffff:(\d+\.\d+\.\d+\.\d+)$/.exec(host);
  if (mapped) return isLocalIPv4(mapped[1]);
  return /^f[cd][0-9a-f]{0,2}:/.test(host);
}

/**
 * Whether `baseUrl` is a server the operator runs: an http or https URL whose
 * host is `localhost`, a loopback or a private-network IP literal. A host name
 * other than `localhost` is never resolved, so it is not local; a URL carrying
 * a user name or password is not local (#2128). Mirrors the Go
 * `models.IsLocalBaseURL`.
 */
export function isLocalBaseUrl(baseUrl: string): boolean {
  let u: URL;
  try {
    u = new URL(baseUrl);
  } catch {
    return false;
  }
  if ((u.protocol !== "http:" && u.protocol !== "https:") || u.username || u.password) {
    return false;
  }
  const host = u.hostname.toLowerCase().replace(/^\[(.*)\]$/, "$1");
  if (host === "localhost" || host.endsWith(".localhost")) return true;
  return isLocalIPv4(host) || isLocalIPv6(host);
}

/**
 * Whether a declared endpoint is a model server the operator runs: its kind is
 * `lm-studio` or `ollama`, or its base URL is local. Mirrors the Go
 * `models.IsLocalEndpoint`.
 */
export function isLocalEndpoint(ep: LocalEndpoint): boolean {
  return isLocalProvider(ep.provider) || isLocalBaseUrl(ep.baseUrl);
}

/**
 * Whether `model`, dispatched on `adapter`, runs on a server the operator runs
 * (#2128). Locality follows the declared endpoint, not the provider-key brand:
 * an `opencode` model whose provider key is a declared endpoint's id is local
 * exactly when that endpoint is, so `omlx/...` on a declared loopback endpoint
 * is local. An undeclared key is local only when {@link providerFor}
 * normalizes it to a local provider; `other` stays non-local. Mirrors the Go
 * `models.IsLocalModel`.
 */
export function isLocalModel(
  adapter: string,
  model: string,
  endpoints: readonly LocalEndpoint[] = []
): boolean {
  if (adapter === OPENCODE_ADAPTER) {
    const split = splitOpenCodeModel(model);
    if (!split) return false;
    const ep = endpoints.find((e) => e.id === split.key);
    if (ep) return isLocalEndpoint(ep);
  }
  return isLocalProvider(providerFor(adapter, model));
}

/**
 * ADR-022 § 1's normalization table: the OpenCode provider keys Nightgauge
 * recognizes and the registry provider each one names. Every other key is
 * `other`. Declared endpoint ids (#1678, #1679) are resolved to their
 * endpoint's provider ahead of this table; until then a key such as
 * `lmstudio-remote` is `other`.
 */
const OPENCODE_PROVIDERS: Readonly<Record<string, Provider>> = Object.freeze({
  lmstudio: "lm-studio",
  ollama: "ollama",
  anthropic: "anthropic",
  openai: "openai",
  xai: "xai",
  google: "google",
});

/**
 * The shape of an OpenCode provider key: lowercase letters, digits and `-`,
 * never starting with `-`. It matches the Go opencode adapter's pre-spawn
 * model check, so the parser and the adapter agree on a well-formed model.
 */
const OPENCODE_PROVIDER_KEY_RE = /^[a-z0-9][a-z0-9-]*$/;

/** Go's `unicode.IsSpace || unicode.IsControl`: the White_Space property and category Cc. */
const SPACE_OR_CONTROL_RE = /[\p{White_Space}\p{Cc}]/u;

/**
 * Split a model the way OpenCode reads `-m`: on the FIRST slash, so
 * `lmstudio/qwen/qwen3.8-27b` is key `lmstudio` and model id
 * `qwen/qwen3.8-27b`. `undefined` for a malformed model: no slash, a key that
 * is not a provider-key shape (surrounding whitespace and upper case
 * included), or a model id that is empty, starts with `-`, or contains
 * whitespace or a control character. The input is never trimmed.
 */
function splitOpenCodeModel(raw: string): { key: string; id: string } | undefined {
  const slash = raw.indexOf("/");
  if (slash < 0) return undefined;
  const key = raw.slice(0, slash);
  const id = raw.slice(slash + 1);
  if (!OPENCODE_PROVIDER_KEY_RE.test(key)) return undefined;
  if (id === "" || id.startsWith("-") || SPACE_OR_CONTROL_RE.test(id)) return undefined;
  return { key, id };
}

/** The result of {@link parseOpenCodeModel}. */
export interface OpenCodeModel {
  /** The normalized registry provider; `other` for an unrecognized key or a malformed model. */
  provider: Provider;
  /** The model id after the first slash; empty for a malformed model. */
  bareId: string;
  /**
   * The raw model, unchanged. A record of what was dispatched, never a
   * pricing, overlay or provider lookup key: those use `provider` and `bareId`.
   */
  upstream: string;
}

/**
 * Derive the provider of an `opencode` model id (ADR-022 § 1). Malformed
 * input (empty, no slash, an empty key or id, surrounding whitespace, a
 * mixed-case key) is provider `other` with an empty `bareId`. A bare id is
 * never qualified to a provider. Mirrors the Go `models.ParseOpenCodeModel`.
 */
export function parseOpenCodeModel(raw: string): OpenCodeModel {
  const split = splitOpenCodeModel(raw);
  if (!split) return { provider: "other", bareId: "", upstream: raw };
  const provider = Object.hasOwn(OPENCODE_PROVIDERS, split.key)
    ? OPENCODE_PROVIDERS[split.key]
    : "other";
  return { provider, bareId: split.id, upstream: raw };
}

/** The result of {@link dispatchModelFor}: the `-m` value, or why there is none. */
export type DispatchModelResult = { ok: true; model: string } | { ok: false; error: string };

/**
 * The model to pass on the adapter's `-m` flag for a stage whose model is
 * `bandOrId`. `configuredModel` is the operator's configured default for a
 * multi-provider adapter (`opencode.model`, ADR-022 § 7), a
 * `<provider>/<model>`; it may be empty.
 *
 * For `opencode`: a `<provider>/<model>` stage model is returned unchanged;
 * an empty one returns `configuredModel`; a band is resolved against
 * `configuredModel`'s provider — a local provider returns `configuredModel`,
 * since the configured local model serves every band, and a hosted provider
 * returns `<provider>/<id>` for the registry model serving that band.
 * Anything else is an error: a bare id (a registry id included) is never
 * qualified to a provider, a provider with no model in the band cannot serve
 * it, and a band with no configured provider has none to resolve against.
 *
 * The result is never a band name or a bare id. Every other adapter serves
 * one provider and resolves bands itself, so `bandOrId` is returned
 * unchanged. Mirrors the Go `models.DispatchModelFor`.
 */
export function dispatchModelFor(
  adapter: string,
  bandOrId: string,
  configuredModel: string
): DispatchModelResult {
  if (adapter !== OPENCODE_ADAPTER) return { ok: true, model: bandOrId };
  if (bandOrId.includes("/")) {
    if (!splitOpenCodeModel(bandOrId)) {
      return {
        ok: false,
        error: `model "${bandOrId}" is not a valid <provider>/<model> for the ${adapter} adapter`,
      };
    }
    return { ok: true, model: bandOrId };
  }
  if (bandOrId !== "" && !(TIER_BANDS as readonly string[]).includes(bandOrId)) {
    return {
      ok: false,
      error:
        `model "${bandOrId}" names no provider, and the ${adapter} adapter never infers one: ` +
        `name it as <provider>/<model>`,
    };
  }
  const configured = splitOpenCodeModel(configuredModel);
  if (!configured) {
    return {
      ok: false,
      error:
        `the ${adapter} adapter has no provider to resolve stage model "${bandOrId}" against: ` +
        `name the stage model as <provider>/<model>, or configure a <provider>/<model> default (opencode.model)`,
    };
  }
  if (bandOrId === "") return { ok: true, model: configuredModel };
  const { provider } = parseOpenCodeModel(configuredModel);
  if (isLocalProvider(provider)) return { ok: true, model: configuredModel };
  const m = getModelDescriptor(bandOrId, provider);
  if (m && m.provider === provider) return { ok: true, model: `${configured.key}/${m.id}` };
  return {
    ok: false,
    error:
      `provider "${provider}" of the configured model "${configuredModel}" has no registry model ` +
      `in band "${bandOrId}": name the stage model as <provider>/<model>`,
  };
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

// ---------------------------------------------------------------------------
// Transport-reachability enforcement (fail-closed-axis-enforcement, #579)
// ---------------------------------------------------------------------------

/**
 * The result of consulting a model's transport reachability facts (#579).
 * Mirrors the Go `models.CheckTransportServed` contract exactly, so both
 * languages answer the same reachability question the same way:
 *
 * - `found: false` — `idOrTier` does not resolve to any registry entry for
 *   `provider` — the ordinary {@link getModelDescriptor} miss, unchanged.
 * - `found: true, model` — resolved and selectable through `transport`: the
 *   fact is explicit `served: true`, OR the transport key (or the whole
 *   `transports` map) is absent — the unexpressed/pending state that MUST
 *   fail OPEN with today's behavior (#579 AC4).
 * - `found: true, unreachable` — resolved, but `transports[transport]`
 *   explicitly declares `served: false`. `model` is absent; `unreachable`
 *   carries provider, model id, and transport for a classified error.
 */
export interface TransportServedResult {
  found: boolean;
  model?: ModelDescriptor;
  unreachable?: { provider: Provider; model: string; transport: Transport };
}

/**
 * The transport-aware selection entry point (#579): resolves `idOrTier` for
 * `provider` exactly like {@link getModelDescriptor} (concrete id — provider
 * agnostic, ids are globally unique — then tier band within `provider`), then
 * classifies the result against `transport`. Selection paths (adapter model
 * preflight — `validateModelForAdapter` in `cli/adapters/modelPreflight.ts`)
 * call this INSTEAD OF bare `getModelDescriptor` so a model the dispatching
 * transport cannot reach fails closed BEFORE spawn with a classified error
 * naming provider, model, and transport — rather than resolving silently and
 * reaching the CLI unchecked (#552's exact gap, absorbed by #579).
 *
 * `getModelDescriptor`'s exact-id lookup is deliberately provider-agnostic
 * (ids are globally unique across providers) — callers that need a
 * provider-scoped closed set (every current closed-adapter policy does) must
 * additionally check `model.provider === provider` on a `found` result,
 * exactly as {@link resolveModelForAdapter}'s callers already do.
 */
export function checkTransportServed(
  provider: Provider,
  transport: Transport,
  idOrTier: string
): TransportServedResult {
  const model = getModelDescriptor(idOrTier, provider);
  if (!model) return { found: false };
  const facts = model.transports?.[transport];
  if (facts && facts.served === false) {
    return { found: true, unreachable: { provider: model.provider, model: model.id, transport } };
  }
  return { found: true, model };
}

// ---------------------------------------------------------------------------
// Typed behavior accessors (#77)
// ---------------------------------------------------------------------------

/**
 * The `behavior` block for a model, or `undefined` when the model is unknown
 * or declares none. Consumers read facts through this and
 * {@link getModelPropensity} rather than reaching into the registry JSON.
 */
export function getModelBehavior(
  idOrTier: string,
  provider: Provider = "anthropic"
): Behavior | undefined {
  return getModelDescriptor(idOrTier, provider)?.behavior;
}

/**
 * Every propensity axis resolved to a concrete level, filling `normal` for
 * anything undeclared — including for a model with no `behavior` block and for
 * unknown/local ids, which have no registry entry at all. Total by design: an
 * overlay asking "is verification high here?" gets a usable answer for every
 * model, and the neutral default changes nothing.
 */
export function getModelPropensity(
  idOrTier: string,
  provider: Provider = "anthropic"
): Required<Propensity> {
  const p = getModelBehavior(idOrTier, provider)?.propensity;
  return {
    verification: p?.verification ?? "normal",
    delegation: p?.delegation ?? "normal",
    narration: p?.narration ?? "normal",
  };
}

/**
 * Whether disabling thinking is invalid for this model at `effort`, and the
 * declared ceiling. `limit` is `never` when the model refuses at every effort
 * (Fable 5) — callers must not render that as "lower the effort to …".
 * Mirrors the Go `ModelDescriptor.ThinkingDisableConflict`.
 */
export function thinkingDisableConflict(
  idOrTier: string,
  effort: EffortLevel,
  provider: Provider = "anthropic"
): { conflict: boolean; limit?: ThinkingDisableLimit } {
  const limit = getModelBehavior(idOrTier, provider)?.thinking_disable_max_effort;
  if (!limit) return { conflict: false };
  if (limit === THINKING_DISABLE_NEVER) return { conflict: true, limit };
  const ladder = EFFORT_LEVELS as readonly string[];
  const limitIdx = ladder.indexOf(limit);
  const requestedIdx = ladder.indexOf(effort);
  if (limitIdx < 0 || requestedIdx < 0) return { conflict: false, limit };
  return { conflict: requestedIdx > limitIdx, limit };
}

/**
 * Token counts for cost computation (cache fields optional).
 *
 * Cache creation is split by TTL tier because Anthropic prices the two pools
 * differently (#358). A caller that knows only a single combined
 * cache-creation count must put it in `cacheCreation5m` — that is the cheaper
 * tier, so the estimate is a floor rather than an overstatement. On captured
 * Claude CLI traffic the writes are 1h-heavy, so that floor under-prices the
 * cache-creation pool by ~1.6x on 1h-heavy stages. Plumbing the real split end
 * to end is #390.
 */
export interface TokenCounts {
  input: number;
  output: number;
  cacheRead?: number;
  /** Cache writes bought with a 5-minute TTL. */
  cacheCreation5m?: number;
  /** Cache writes bought with a 1-hour TTL. */
  cacheCreation1h?: number;
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
  const cacheCreation5mRate = rates.cache_creation_5m ?? 0;
  const cacheCreation1hRate = rates.cache_creation_1h ?? 0;
  return (
    (tokens.input * rates.input +
      tokens.output * rates.output +
      (tokens.cacheRead ?? 0) * cacheReadRate +
      (tokens.cacheCreation5m ?? 0) * cacheCreation5mRate +
      (tokens.cacheCreation1h ?? 0) * cacheCreation1hRate) /
    1_000_000
  );
}

/** Per-million rates for one (provider, band) pair. */
export interface ProviderTierRates {
  inputPerMillion: number;
  outputPerMillion: number;
  /** Absent when the provider publishes no cache tier; callers fall back to the input rate. */
  cacheReadPerMillion?: number;
  /**
   * ONE cache-creation rate, because every consumer holds an UNSPLIT
   * cache-creation count. Per the #358 convention that is the 5m tier — the
   * cheaper one, so a derived estimate is a floor.
   */
  cacheCreationPerMillion?: number;
}

/**
 * Rates for a `(provider, band)` pair, or `undefined` when that provider
 * serves no model in that band.
 *
 * `undefined` is the TypeScript analogue of Go's `Stamped=false`, and it is the
 * whole point of this function: it NEVER falls back to another provider.
 * `deriveDefaultModelCostRates()`, which this replaces, called
 * `getModelDescriptor(tier, "anthropic")` for every band, so a run dispatched
 * to xai or openai was estimated at Claude prices while the actual was booked
 * at the real provider rate — the exact asymmetry #696 fixed on the Go side and
 * left standing on this one (#1213).
 *
 * A local provider (ollama, lm-studio) has no registry entries by design; it
 * reports unpriced rather than a confident $0, because "free" and "unknown" are
 * different answers and only one of them is safe to add into a total.
 */
export function ratesForProviderTier(
  provider: Provider,
  tier: string
): ProviderTierRates | undefined {
  const m = getModelDescriptor(tier, provider);
  if (!m || m.provider !== provider) return undefined;
  return {
    inputPerMillion: m.rates.input,
    outputPerMillion: m.rates.output,
    cacheReadPerMillion: m.rates.cache_read,
    cacheCreationPerMillion: m.rates.cache_creation_5m,
  };
}
