/**
 * Registry tier bands — the ONE band-order declaration on the TypeScript side
 * (selection-query cutover, #581 / spike #568 §4).
 *
 * Deliberately a leaf module with zero imports so every consumer —
 * `AutoModelSelector`'s `MODEL_TIER_ORDER`, the skill-eval `MODEL_TIERS` Zod
 * enum, the extension's closed-set guards and the selection query — can derive
 * from it without a runtime import cycle (the registry loader sits downstream
 * of the schemas this must feed).
 *
 * The band ORDER is a declared ordering, not registry data: the registry
 * (`model-registry.json`) declares which bands each model SERVES (`tiers`),
 * and the selection query derives ladder MEMBERSHIP from that — but the
 * relative strength of the four bands has no data field in this phase (the
 * spike's registry-axis-schema deliberately added no ordering field, and
 * capability evidence does not exist yet). Until measured evidence exists,
 * this list is the single place the order is spelled; its Go pair is
 * `routing.TierBandsStrongestFirst` (internal/intelligence/routing/
 * performance_mode.go), pinned by the cross-language ladder tests.
 *
 * Band names remain the user-facing routing vocabulary until #582 retires
 * them; here they are QUERY INPUTS whose meaning (which concrete model, at
 * which effort) the registry defines.
 */

/** Tier bands in ascending capability/cost order — the single TS declaration. */
export const TIER_BANDS = ["haiku", "sonnet", "opus", "fable"] as const;

/** A registry tier band. */
export type TierBand = (typeof TIER_BANDS)[number];

/**
 * The same ladder strongest-first — the order downgrade walks traverse (#42).
 * Derived, never re-listed.
 */
export const TIER_BANDS_STRONGEST_FIRST: readonly TierBand[] = [...TIER_BANDS].reverse();

/**
 * Regex alternation over the band vocabulary, for config-file band regexes —
 * the same derivation idiom as `EFFORT_ALTERNATION` (#394): a hand-written
 * `(sonnet|opus|haiku)` silently dropped `fable` twice before it was derived.
 */
export const TIER_BAND_ALTERNATION = TIER_BANDS.join("|");

/** Membership test against the band vocabulary. */
export function isTierBand(value: unknown): value is TierBand {
  return (TIER_BANDS as readonly string[]).includes(value as string);
}
