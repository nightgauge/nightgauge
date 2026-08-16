/**
 * Band-strength classification of a recorded model reference — the ONE place
 * telemetry consumers turn "what string did the record carry" into "which
 * registry band was that, and is it a light or heavy tier" (#582).
 *
 * Replaces the pre-#582 band-substring matchers (`model.includes("haiku")`),
 * which failed open on every non-Anthropic id (`gpt-4o-mini` matched
 * nothing), skipped `fable`-band records entirely, and misfiled copilot's
 * `claude-sonnet-4.5` — an opus/fable-band model — as lightweight because its
 * NAME contains "sonnet". Spike #568 §5 dispositioned these consumers as
 * rewrite-to-envelope-fields, never aliased. Consumers: the model-routing
 * health dimension, `ModelPerformanceAnalyzer`'s under/over-routing
 * detectors, and the extension's failure-comment escalation hint.
 */

import { getModelDescriptor } from "./modelRegistry.js";
import { isTierBand, TIER_BANDS, type TierBand } from "./tierBands.js";

/**
 * Strongest registry band a recorded model reference belongs to — accepts a
 * band name or a concrete registry id (any provider; ids are globally
 * unique). `undefined` for anything the registry does not know, which keeps
 * unknown vocabulary out of both weight classes instead of misclassifying it.
 */
export function strongestBand(model: string): TierBand | undefined {
  if (isTierBand(model)) return model;
  const tiers = getModelDescriptor(model)?.tiers;
  if (!tiers?.length) return undefined;
  let best: TierBand | undefined;
  for (const tier of tiers) {
    if (!isTierBand(tier)) continue;
    if (best === undefined || TIER_BANDS.indexOf(tier) > TIER_BANDS.indexOf(best)) {
      best = tier;
    }
  }
  return best;
}

/** The two weakest bands, positionally derived from the `TIER_BANDS` order. */
const LIGHTWEIGHT_BANDS: ReadonlySet<TierBand> = new Set(TIER_BANDS.slice(0, 2));

/** True when the reference resolves to one of the two weakest bands. */
export function isLightweightModel(model: string): boolean {
  const band = strongestBand(model);
  return band !== undefined && LIGHTWEIGHT_BANDS.has(band);
}

/**
 * True when the reference resolves to one of the two strongest bands. An
 * unknown reference is NEITHER light nor heavy — both predicates return
 * false, so consumers exclude it rather than guessing a weight class.
 */
export function isHeavyweightModel(model: string): boolean {
  const band = strongestBand(model);
  return band !== undefined && !LIGHTWEIGHT_BANDS.has(band);
}
