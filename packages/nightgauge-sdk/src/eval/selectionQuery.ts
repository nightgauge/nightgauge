/**
 * Selection query over the registry axes (selection-query cutover, #581 /
 * spike #568 §4.1).
 *
 * The unit of selection is the dispatch envelope `(model_id, effort,
 * thinking)`, chosen from a provider-scoped, envelope-valued candidate
 * ladder. This module derives that ladder from the ONE registry
 * (`model-registry.json`) instead of the five hand-inlined enums and
 * positional indices it replaces: which model serves a band, whether the
 * dispatching transport can reach it, and which effort rungs it declares are
 * all registry facts; the band ORDER is the single {@link TIER_BANDS}
 * declaration (see tierBands.ts for why order is declared, not data, in this
 * phase).
 *
 * Envelope-valued means the rungs are `(model_id, effort)` points, not model
 * names. On anthropic the rungs span models at their declared default
 * efforts; on xai — where all four bands map to grok-4.6 and a band
 * downgrade is a declared cost no-op (#532) — the rungs descend through
 * EFFORT within the one model (`grok-4.6@xhigh → high → medium → low`),
 * which is a real cost/latency ladder the band vocabulary structurally could
 * not express.
 *
 * CAPABILITY DISCIPLINE (spike §4.3): no measured capability evidence exists
 * yet, so the capability axis participates in this query as ABSENT — rungs
 * carry no capability field, the ordering is the declared one, and nothing
 * here invents a capability fact. Measured evidence (post-#528 / the honest
 * eval lane) replaces declared ordering rung-by-rung via the routing-advice
 * file (routingAdvice.ts), never by editing this derivation.
 *
 * Cost is NEVER inferred from ladder rank — it always comes from the
 * transport rate card (the #532 lesson): nothing in this module reads or
 * exposes rates.
 *
 * Go pair: internal/intelligence/routing/selection.go — the ladder tests pin
 * the two derivations to identical rungs.
 */

import type { EffortLevel, Provider, Transport } from "./modelEvalSchemas.js";
import { EFFORT_LEVELS } from "./modelEvalSchemas.js";
import { getModelDescriptor } from "./modelRegistry.js";
import { TIER_BANDS, TIER_BANDS_STRONGEST_FIRST, type TierBand } from "./tierBands.js";

/**
 * One rung of the provider-scoped candidate ladder: the dispatch envelope a
 * band-vocabulary input resolves to. `effort`/`thinking` are the registry's
 * DECLARED facts (`behavior.effort_default`, `behavior.thinking_default`) and
 * stay `undefined` when the registry declares nothing — absent, never a
 * fabricated default. There is deliberately NO capability field.
 */
export interface EnvelopeRung {
  /** The band this rung answers for — the query INPUT vocabulary (until #582). */
  band: TierBand;
  /** Concrete registry model id serving the band for this provider. */
  modelId: string;
  /** Effort rung, when the registry declares one (see ladder derivation). */
  effort?: EffortLevel;
  /** Declared default thinking state, when the registry declares one. */
  thinking?: "on" | "off";
}

function declaredThinking(modelId: string, provider: Provider): "on" | "off" | undefined {
  const td = getModelDescriptor(modelId, provider)?.behavior?.thinking_default;
  return td === "on" || td === "off" ? td : undefined;
}

/** Highest declared effort rung, or undefined for a model with no effort axis. */
function topSupportedEffort(supported: readonly string[]): EffortLevel | undefined {
  for (let i = EFFORT_LEVELS.length - 1; i >= 0; i--) {
    if (supported.includes(EFFORT_LEVELS[i])) return EFFORT_LEVELS[i];
  }
  return undefined;
}

/** The next declared effort rung strictly below `effort`, or undefined. */
function nextLowerSupportedEffort(
  supported: readonly string[],
  effort: EffortLevel | undefined
): EffortLevel | undefined {
  const start = effort === undefined ? EFFORT_LEVELS.length - 1 : EFFORT_LEVELS.indexOf(effort) - 1;
  for (let i = start; i >= 0; i--) {
    if (supported.includes(EFFORT_LEVELS[i])) return EFFORT_LEVELS[i];
  }
  return undefined;
}

/**
 * The provider-scoped candidate ladder, strongest rung first.
 *
 * Derivation, all from registry facts:
 *
 * - Walk the bands strongest-first; each band resolves to the provider's
 *   current non-deprecated model serving it (registry `tiers` — membership).
 * - When `transport` is given, a model whose `transports[transport].served`
 *   is explicitly `false` is excluded (fail-closed, #579). An ABSENT
 *   transport fact passes through — the unexpressed/pending state must not
 *   be read as unserved.
 * - A model serving ONE band rungs at its declared `behavior.effort_default`
 *   (absent when undeclared).
 * - A model serving SEVERAL bands descends through its declared
 *   `supported_efforts`: its strongest band rungs at the TOP declared effort,
 *   each weaker band one declared rung lower. A band that cannot descend
 *   further (ladder bottom reached) yields no rung — the band collapse would
 *   otherwise re-create the #532 "downgrade is a no-op" lie.
 *
 * PROVENANCE of the multi-band descent rule: spike #568 §4.1 derives it for
 * the FULLY-collapsed provider (xai, all four bands on grok-4.6), where the
 * effort rungs are the only ladder the provider has. Applying it to
 * PARTIALLY-collapsed providers (google's pro/flash pairs, openai's
 * gpt-5.6-sol) is this module's generalization, not a spike mandate: it
 * synthesizes effort points (gemini-2.5-pro@medium, gpt-5.6-sol@high) no
 * registry field declares as a band's serving envelope. The decision the
 * earlier version of this note deferred is now MADE and PINNED (#606): the
 * Go RetryEngine's downgrade walk executes the descent ONLY on
 * fully-collapsed providers and keeps the same-model skip on
 * partially-collapsed ones — their synthesized rungs stay declared-ladder
 * shape that nothing dispatches, because those providers have a real weaker
 * MODEL to fall to. Pinned by
 * TestEvaluateDowngrade_PartiallyCollapsedProviderKeepsSameModelSkip
 * (retry_engine_test.go) and the twin ladder tests, so widening the descent
 * cannot happen by accident. (Go pair: the identical note on
 * CandidateLadder, selection.go.)
 *
 * Local providers (ollama / lm-studio) have no registry entries by design, so
 * their ladder is empty and callers keep the configured local model.
 */
export function candidateLadder(
  provider: Provider,
  transport?: Transport,
  exclusions: Exclusions = NO_EXCLUSIONS
): EnvelopeRung[] {
  const rungs: EnvelopeRung[] = [];
  const lastEffort = new Map<string, EffortLevel | undefined>();

  for (const band of TIER_BANDS_STRONGEST_FIRST) {
    const model = getModelDescriptor(band, provider);
    if (!model || model.provider !== provider) continue;
    if (exclusions.models.includes(model.id)) continue;
    if (transport) {
      const facts = model.transports?.[transport];
      if (facts && facts.served === false) continue;
    }
    const allowed = allowedEfforts(model.supported_efforts, exclusions);
    if (model.supported_efforts.length > 0 && allowed.length === 0) continue; // every effort excluded

    let effort: EffortLevel | undefined;
    if (lastEffort.has(model.id)) {
      const lower = nextLowerSupportedEffort(allowed, lastEffort.get(model.id));
      if (lower === undefined) continue; // cannot descend further — no rung
      effort = lower;
    } else if ((model.tiers ?? []).length > 1) {
      effort = topSupportedEffort(allowed);
    } else {
      effort = model.behavior?.effort_default as EffortLevel | undefined;
      if (effort !== undefined && exclusions.efforts.includes(effort)) {
        effort = nextLowerSupportedEffort(allowed, effort);
        if (effort === undefined) continue; // nothing allowed at or below the default
      }
    }
    lastEffort.set(model.id, effort);
    rungs.push({
      band,
      modelId: model.id,
      effort,
      thinking: declaredThinking(model.id, provider),
    });
  }

  return rungs;
}

/**
 * `model_routing.excluded_models` / `excluded_efforts` (#2063). The empty
 * value excludes nothing and is the no-op path. Go pair: routing.Exclusions.
 */
export interface Exclusions {
  models: readonly string[];
  efforts: readonly string[];
}

export const NO_EXCLUSIONS: Exclusions = { models: [], efforts: [] };

function allowedEfforts(supported: readonly string[], ex: Exclusions): string[] {
  return supported.filter((e) => !ex.efforts.includes(e));
}

function isNoExclusions(ex: Exclusions): boolean {
  return ex.models.length === 0 && ex.efforts.length === 0;
}

/**
 * `band` when the exclusion-filtered ladder has a rung for it, else the
 * strongest eligible band BELOW it — never above. `undefined` when nothing is
 * eligible at or below `band`. A band the provider does not serve at all is
 * returned unchanged. Go pair: routing.EligibleBand.
 */
export function eligibleBand(
  provider: Provider,
  band: TierBand,
  exclusions: Exclusions = NO_EXCLUSIONS
): TierBand | undefined {
  if (isNoExclusions(exclusions) || !candidateLadder(provider).some((r) => r.band === band)) {
    return band;
  }
  const eligible = candidateLadder(provider, undefined, exclusions);
  const rank = TIER_BANDS.indexOf(band);
  return TIER_BANDS_STRONGEST_FIRST.find(
    (b) => TIER_BANDS.indexOf(b) <= rank && eligible.some((r) => r.band === b)
  );
}

/**
 * Lowers an envelope's floor and ceiling to the strongest eligible band at or
 * below each; a bound with nothing eligible is left alone (dispatch refuses
 * it). Go pair: routing.ApplyExclusions.
 */
export function applyExclusions<E extends { floor: TierBand; ceiling: TierBand }>(
  envelope: E,
  provider: Provider,
  exclusions: Exclusions = NO_EXCLUSIONS
): E {
  return {
    ...envelope,
    floor: eligibleBand(provider, envelope.floor, exclusions) ?? envelope.floor,
    ceiling: eligibleBand(provider, envelope.ceiling, exclusions) ?? envelope.ceiling,
  };
}

/**
 * Resolve one band input to its dispatch envelope for a provider — the query
 * that replaces "band → hardcoded ladder position". Returns `undefined` when
 * the provider has no (reachable) model for the band.
 */
export function resolveBandEnvelope(
  provider: Provider,
  band: TierBand,
  transport?: Transport,
  exclusions: Exclusions = NO_EXCLUSIONS
): EnvelopeRung | undefined {
  return candidateLadder(provider, transport, exclusions).find((r) => r.band === band);
}

/**
 * The band an automatic escalation may not exceed. A POLICY constant, not a
 * registry fact: Fable (the frontier tier at ~2× Opus) is reachable only by
 * explicit opt-in — the `frontier` mode envelope, a per-run override, or an
 * explicit per-stage model — never by the post-failure escalation walk. This
 * pins the pre-cutover `["haiku", "sonnet", "opus"]` escalation ceiling.
 */
export const ESCALATION_CEILING_BAND: TierBand = "opus";

/**
 * The bands the post-failure escalation walk may traverse, weakest first —
 * membership derived from the registry (a band with no live model for the
 * provider is no escalation target), order from {@link TIER_BANDS}, ceiling
 * from {@link ESCALATION_CEILING_BAND}. Replaces the hand-inlined
 * `["haiku", "sonnet", "opus"]` escalation ladders.
 */
export function escalationLadder(
  provider: Provider = "anthropic",
  exclusions: Exclusions = NO_EXCLUSIONS
): TierBand[] {
  const ceiling = TIER_BANDS.indexOf(ESCALATION_CEILING_BAND);
  const eligible = candidateLadder(provider, undefined, exclusions);
  return TIER_BANDS.filter((band, i) => {
    if (i > ceiling) return false;
    const m = getModelDescriptor(band, provider);
    if (m === undefined || m.provider !== provider) return false;
    return isNoExclusions(exclusions) || eligible.some((r) => r.band === band);
  });
}
