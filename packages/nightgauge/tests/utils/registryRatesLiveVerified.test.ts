/**
 * Literal-value guard: every non-Anthropic rate against its live-verified figure.
 *
 * ── WHAT THIS TEST CAN AND CANNOT DO ──
 *
 * It CANNOT catch a stale vendor sheet. If OpenAI or Google reprices tomorrow,
 * this test stays green while the registry under- or over-bills, because the
 * expectation below is a transcription, not a live fetch.
 *
 * What it DOES do is convert silent drift into a deliberate edit. Every figure
 * is hardcoded here beside the URL it was read from and the date it was read,
 * so changing a rate in `model-registry.json` fails this test until the same
 * change is made here — which forces whoever makes it to re-open the vendor
 * page and re-date the citation. #391 existed because a second rate table
 * drifted from the first with nothing able to detect it; the registry is now
 * the only table, and this is what stops the ONE table from rotting quietly.
 *
 * Anthropic entries are deliberately NOT listed. Their pools are derived from
 * base input by published multipliers (0.1x read, 1.25x 5m write, 2.0x 1h
 * write) and are already pinned as derivations by the Go test
 * `TestAnthropicCacheRatesFollowPublishedMultipliers`. Restating them here
 * would be a second copy of a rule that already has an authority.
 *
 * @see Issue #392 — non-Anthropic cache rates
 * @see Issue #391 — the registry is the only pricing authority
 * @see internal/models/registry_test.go — the Anthropic multiplier invariant
 */

import { describe, it, expect } from "vitest";
import { MODEL_REGISTRY, getModelDescriptor, type ModelDescriptor } from "@nightgauge/sdk";

/** The date every figure in {@link VERIFIED_RATES} was read off the vendor page. */
const VERIFIED_ON = "2026-08-09";

const OPENAI_PRICING = "https://developers.openai.com/api/docs/pricing";
const OPENAI_PROMPT_CACHING = "https://developers.openai.com/api/docs/guides/prompt-caching";
const GOOGLE_PRICING = "https://ai.google.dev/gemini-api/docs/pricing";
const XAI_MODELS = "https://docs.x.ai/developers/models";
/**
 * The #531 close-out: controlled live billing measurement against the Grok
 * Build CLI (1.0.4) — input and cache-read tokens held constant, output
 * varied, solved against held-out samples to 6e-8. The exact per-pool figures
 * are published in PR #554's body:
 * https://github.com/nightgauge/nightgauge/pull/554
 *
 * These are MEASURED CHARGED RATES for the CLI transport the pipeline
 * actually uses, not xAI's API list prices — the sheet at XAI_MODELS is not
 * this transport's bill (#570).
 */
const XAI_CLI_MEASUREMENT =
  "https://github.com/nightgauge/nightgauge/issues/531#issuecomment-5303892638";

/** The five per-1M pools the schema can express. */
const POOLS = ["input", "output", "cache_read", "cache_creation_5m", "cache_creation_1h"] as const;
type Pool = (typeof POOLS)[number];

interface VerifiedEntry {
  id: string;
  /** The vendor page the figures were read from. */
  source: string;
  /** Which published row/tier on that page — rates differ by tier. */
  row: string;
  /**
   * Expected per-1M rate for each pool. A pool ABSENT from this object must
   * also be absent from the registry: absence is a priced statement (see the
   * registry's `$schema_note`), not a gap in this table.
   */
  rates: Partial<Record<Pool, number>>;
  /** Why a pool is absent, when the absence is the interesting part. */
  absenceNote?: string;
}

/**
 * The live-verified rate card for every non-Anthropic entry this PR touched,
 * plus the two rows whose ABSENCE it pins.
 *
 * OpenAI figures are the Standard tier, short-context column. Google figures
 * are the Standard tier matching each entry's existing context row, and the
 * cached-input rates are the text/image/video modality (audio bills higher and
 * this schema has no modality axis).
 */
const VERIFIED_RATES: readonly VerifiedEntry[] = [
  // ── OpenAI: GPT-5.6 family ────────────────────────────────────────────────
  // These are the only OpenAI entries carrying a cache-write rate. The vendor
  // states the rule outright: "For GPT-5.6 models and later model families,
  // cache writes cost 1.25x the uncached input token rate" (OPENAI_PROMPT_CACHING).
  // `cache_creation_1h` stays absent everywhere — OpenAI publishes no second
  // TTL tier, so there is no 1h figure to transcribe and none may be invented.
  {
    id: "gpt-5.6-sol",
    source: OPENAI_PRICING,
    row: "Standard, short context",
    rates: { input: 5.0, output: 30.0, cache_read: 0.5, cache_creation_5m: 6.25 },
    absenceNote: "no cache_creation_1h: OpenAI publishes one cache-write TTL tier, not two",
  },
  {
    id: "gpt-5.6-terra",
    source: OPENAI_PRICING,
    row: "Standard, short context",
    rates: { input: 2.0, output: 12.0, cache_read: 0.2, cache_creation_5m: 2.5 },
    absenceNote: "no cache_creation_1h: OpenAI publishes one cache-write TTL tier, not two",
  },
  {
    id: "gpt-5.6-luna",
    source: OPENAI_PRICING,
    row: "Standard, short context",
    rates: { input: 0.2, output: 1.2, cache_read: 0.02, cache_creation_5m: 0.25 },
    absenceNote: "no cache_creation_1h: OpenAI publishes one cache-write TTL tier, not two",
  },

  // ── OpenAI: pre-5.6 families ──────────────────────────────────────────────
  // The live sheet prints "-" in the cache-write column for these, i.e. no
  // write fee is charged. BOTH cache_creation_* are absent and the omission is
  // the published price.
  {
    id: "gpt-5.5",
    source: OPENAI_PRICING,
    row: "Standard, short context",
    rates: { input: 5.0, output: 30.0, cache_read: 0.5 },
    absenceNote: "no cache-write fee on pre-5.6 families — the sheet prints '-'",
  },
  {
    id: "gpt-5.4",
    source: OPENAI_PRICING,
    row: "Standard, short context",
    rates: { input: 2.5, output: 15.0, cache_read: 0.25 },
    absenceNote: "no cache-write fee on pre-5.6 families — the sheet prints '-'",
  },
  {
    id: "gpt-5.4-mini",
    source: OPENAI_PRICING,
    row: "Standard, short context",
    rates: { input: 0.75, output: 4.5, cache_read: 0.075 },
    absenceNote: "no cache-write fee on pre-5.6 families — the sheet prints '-'",
  },
  {
    id: "gpt-5.2",
    source: OPENAI_PRICING,
    row: "Standard, short context",
    rates: { input: 1.75, output: 14.0, cache_read: 0.175 },
    absenceNote: "no cache-write fee on pre-5.6 families — the sheet prints '-'",
  },
  {
    id: "gpt-5.3-codex",
    source: OPENAI_PRICING,
    row: "Standard, short context",
    rates: { input: 1.75, output: 14.0, cache_read: 0.175 },
    absenceNote: "no cache-write fee on pre-5.6 families — the sheet prints '-'",
  },

  // ── OpenAI: rows the live sheet does not publish ──────────────────────────
  // `cache_read` is absent for BOTH, and the absence is the finding, not an
  // oversight. gpt-5.1-codex-mini's row has been retired from the sheet; the
  // 0.025 it briefly carried was copied from a same-sticker sibling, which the
  // `$schema_note` forbids outright. gpt-5.3-codex-spark is a research preview
  // that has never appeared on the sheet, so its $0 input/output is a
  // placeholder rather than a price.
  {
    id: "gpt-5.1-codex-mini",
    source: OPENAI_PRICING,
    row: "not listed (row retired)",
    rates: { input: 0.25, output: 2.0 },
    absenceNote: "cache_read UNRECORDED: no published row; a sibling's rate may not be copied in",
  },
  {
    id: "gpt-5.3-codex-spark",
    source: OPENAI_PRICING,
    row: "not listed (research preview)",
    rates: { input: 0.0, output: 0.0 },
    absenceNote: "no published row at all: the $0 is a placeholder, not a verified price",
  },

  // ── Google ────────────────────────────────────────────────────────────────
  {
    id: "gemini-2.5-pro",
    source: GOOGLE_PRICING,
    row: "Standard, <=200k context; cached input = text/image/video",
    rates: { input: 1.25, output: 10.0, cache_read: 0.125 },
    absenceNote:
      "no cache_creation_*: Google bills cache STORAGE per Mtok-hour, a dimension this schema has no field for",
  },
  {
    id: "gemini-2.5-flash",
    source: GOOGLE_PRICING,
    row: "Standard; cached input = text/image/video",
    rates: { input: 0.3, output: 2.5, cache_read: 0.03 },
    absenceNote:
      "no cache_creation_*: Google bills cache STORAGE per Mtok-hour, a dimension this schema has no field for",
  },
  {
    id: "gemini-2.0-flash",
    source: GOOGLE_PRICING,
    row: "Standard; cached input = text/image/video",
    rates: { input: 0.1, output: 0.4, cache_read: 0.025 },
    absenceNote:
      "no cache_creation_*: Google bills cache STORAGE per Mtok-hour, a dimension this schema has no field for",
  },

  // ── xAI (Grok Build) ──────────────────────────────────────────────────────
  // grok-4.6 and grok-4.5 are MEASURED CLI charges (XAI_CLI_MEASUREMENT), not
  // the API sheet: the sheet declares both models at identical $2/$6 rates
  // with grok-4.5's cache-read cheaper, while the CLI bills grok-4.5 at
  // exactly 2x grok-4.6 with the cache-read relationship inverted (#570).
  // grok-build-0.1 stays on the API sheet's list price: the CLI rejects the
  // id outright, so no measured figure is obtainable, and the entry exists
  // solely so historical cost replay prices already-booked runs.
  {
    id: "grok-4.6",
    source: XAI_CLI_MEASUREMENT,
    row: "Grok Build CLI 1.0.4, measured charge (measured 2026-08-15; figures in PR #554)",
    rates: { input: 0.34, output: 1.02, cache_read: 0.085 },
    absenceNote:
      "no cache_creation_*: no cache-write fee observed on the CLI and xAI publishes none; a ≥200k-prompt multiplier is unmeasured on this transport",
  },
  {
    id: "grok-4.5",
    source: XAI_CLI_MEASUREMENT,
    row: "Grok Build CLI 1.0.4, measured charge (measured 2026-08-15; figures in PR #554)",
    rates: { input: 0.68, output: 2.04, cache_read: 0.102 },
    absenceNote:
      "no cache_creation_*: no cache-write fee observed on the CLI and xAI publishes none; a ≥200k-prompt multiplier is unmeasured on this transport",
  },
  {
    id: "grok-build-0.1",
    source: XAI_MODELS,
    row: "Text API LIST PRICE, <200k prompt tokens (read 2026-08-14) — CLI-unreachable, kept for historical cost replay",
    rates: { input: 1.0, output: 2.0, cache_read: 0.2 },
    absenceNote:
      "no cache_creation_*: xAI publishes cached-input only; ≥200k prompt tokens bill 2x and are unmodeled",
  },
] as const;

function describeRate(v: number | undefined): string {
  return v === undefined ? "(absent)" : String(v);
}

/** Render mismatches as one aligned table so a failure reads as a diff, not a stack. */
function renderMismatchTable(
  rows: { id: string; pool: string; expected: string; actual: string; source: string }[]
): string {
  const header = {
    id: "MODEL",
    pool: "POOL",
    expected: "EXPECTED",
    actual: "REGISTRY",
    source: "SOURCE",
  };
  const all = [header, ...rows];
  const w = (k: keyof typeof header) => Math.max(...all.map((r) => r[k].length));
  const line = (r: typeof header) =>
    `${r.id.padEnd(w("id"))}  ${r.pool.padEnd(w("pool"))}  ${r.expected.padEnd(
      w("expected")
    )}  ${r.actual.padEnd(w("actual"))}  ${r.source}`;
  return [line(header), ...rows.map(line)].join("\n");
}

describe("registry rates match their live-verified vendor figures", () => {
  it(`every non-Anthropic rate equals its transcribed figure (verified ${VERIFIED_ON})`, () => {
    const mismatches: {
      id: string;
      pool: string;
      expected: string;
      actual: string;
      source: string;
    }[] = [];

    for (const entry of VERIFIED_RATES) {
      const d: ModelDescriptor | undefined = getModelDescriptor(entry.id);
      if (!d) {
        mismatches.push({
          id: entry.id,
          pool: "(entry)",
          expected: "present in registry",
          actual: "MISSING",
          source: entry.source,
        });
        continue;
      }
      for (const pool of POOLS) {
        const expected = entry.rates[pool];
        const actual = d.rates[pool];
        if (expected !== actual) {
          mismatches.push({
            id: entry.id,
            pool,
            expected: describeRate(expected),
            actual: describeRate(actual),
            source: entry.source,
          });
        }
      }
    }

    expect(
      mismatches,
      mismatches.length === 0
        ? ""
        : `Registry rates disagree with the live-verified table in this file ` +
            `(read ${VERIFIED_ON}). Re-open the cited source (vendor page, or the ` +
            `measurement for CLI-measured rows), correct BOTH the registry ` +
            `and this table, and re-date the citation:\n\n${renderMismatchTable(mismatches)}\n`
    ).toEqual([]);
  });

  it("the GPT-5.6 cache-write rates are exactly 1.25x their input rate", () => {
    // Not a derivation the registry may perform — a cross-check that the
    // transcribed figure agrees with the vendor's own stated rule:
    // "For GPT-5.6 models and later model families, cache writes cost 1.25x
    // the uncached input token rate" (OPENAI_PROMPT_CACHING). A transcription
    // typo in either the input rate or the write rate breaks the identity.
    const family = VERIFIED_RATES.filter((e) => e.id.startsWith("gpt-5.6-"));
    expect(
      family.length,
      "no gpt-5.6 entries in the table — this asserted nothing"
    ).toBeGreaterThan(0);
    for (const entry of family) {
      const d = getModelDescriptor(entry.id)!;
      expect(
        d.rates.cache_creation_5m,
        `${entry.id} must carry a 5m cache-write rate`
      ).toBeDefined();
      expect(d.rates.cache_creation_5m!, `${entry.id}: see ${OPENAI_PROMPT_CACHING}`).toBeCloseTo(
        d.rates.input * 1.25,
        12
      );
    }
  });

  it("the xAI CLI-measured rates preserve the measured relationships (#570)", () => {
    // Cross-checks against the #531 measurement's two findings, so a future
    // "correction" that quietly reverts to the API sheet fails loudly:
    // 1. grok-4.5 bills exactly 2x grok-4.6 on input and output.
    // 2. grok-4.5's cache-read is the MORE EXPENSIVE of the two — the API
    //    sheet declares the inverse, which is exactly what made the 2x
    //    regression invisible to telemetry (see XAI_CLI_MEASUREMENT).
    const g46 = getModelDescriptor("grok-4.6")!;
    const g45 = getModelDescriptor("grok-4.5")!;
    expect(g45.rates.input, `see ${XAI_CLI_MEASUREMENT}`).toBeCloseTo(g46.rates.input * 2, 12);
    expect(g45.rates.output, `see ${XAI_CLI_MEASUREMENT}`).toBeCloseTo(g46.rates.output * 2, 12);
    expect(g45.rates.cache_read, "grok-4.5 must carry a cache-read rate").toBeDefined();
    expect(g46.rates.cache_read, "grok-4.6 must carry a cache-read rate").toBeDefined();
    expect(
      g45.rates.cache_read!,
      `grok-4.5's measured cache-read is HIGHER than grok-4.6's; see ${XAI_CLI_MEASUREMENT}`
    ).toBeGreaterThan(g46.rates.cache_read!);
  });

  it("no OpenAI entry carries a 1-hour cache-write rate", () => {
    // OpenAI has ONE cache-write tier. A 1h rate appearing on an openai entry
    // would be an Anthropic-shaped assumption leaking across providers, and it
    // would bill a fee the vendor does not charge.
    for (const entry of VERIFIED_RATES) {
      const d = getModelDescriptor(entry.id)!;
      if (d.provider !== "openai") continue;
      expect(
        d.rates.cache_creation_1h,
        `${entry.id} must not carry a 1h cache-write rate`
      ).toBeUndefined();
    }
  });

  it("every openai/google/xai registry entry is cited in this table", () => {
    // Adding a priced OpenAI, Google, or xAI model without citing where its
    // numbers came from is the drift this guard exists to prevent.
    const cited = new Set(VERIFIED_RATES.map((e) => e.id));
    const uncited = MODEL_REGISTRY.filter(
      (m) =>
        (m.provider === "openai" || m.provider === "google" || m.provider === "xai") &&
        !cited.has(m.id)
    ).map((m) => m.id);
    expect(
      uncited,
      `these openai/google/xai entries carry rates with no citation: ${uncited.join(", ")}`
    ).toEqual([]);
  });

  it("every citation carries a source URL, a named row, and a reason for each absence", () => {
    expect(VERIFIED_ON).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    for (const entry of VERIFIED_RATES) {
      // An absent pool is a priced statement, so it must say WHY it is absent.
      // Without this the table could silently degrade into "we didn't look".
      if (POOLS.some((pool) => entry.rates[pool] === undefined)) {
        expect(
          entry.absenceNote,
          `${entry.id} omits a pool but records no reason for the omission`
        ).toBeTruthy();
      }
      expect(entry.source, `${entry.id} has no source URL`).toMatch(/^https:\/\//);
      expect(
        entry.row.length,
        `${entry.id} does not name the published row it read`
      ).toBeGreaterThan(0);
    }
  });
});
