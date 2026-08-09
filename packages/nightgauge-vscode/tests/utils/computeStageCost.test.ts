/**
 * Unit tests for computeStageCost.
 *
 * Covers the three-step resolver chain (native -> registry-computed ->
 * unknown), the >5% drift warn, and the specific defects #391/#392 closed:
 * the routed models that priced at $0 against the deleted extension table, and
 * the non-Anthropic cache pools that priced at $0 against the registry.
 *
 * @see Issue #391 — the registry is the only pricing authority
 * @see Issue #392 — non-Anthropic cache rates
 * @see Issue #3228 — Unified `computeStageCost` across all adapters
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { getModelDescriptor } from "@nightgauge/sdk";
import { computeStageCost, type StageCostTokens } from "../../src/utils/computeStageCost";

const sampleTokens: StageCostTokens = {
  input: 100_000,
  output: 50_000,
  cache_read: 20_000,
  cache_creation_5m: 10_000,
};

/**
 * Read a rate straight out of the registry rather than restating it. Restating
 * it would make the test a second copy of the pricing table — the exact defect
 * #391 removed — and it would agree with a typo in the registry instead of
 * catching one.
 */
function rate(modelId: string, pool: "input" | "output" | "cache_read"): number {
  const d = getModelDescriptor(modelId);
  expect(d, `registry has no entry for ${modelId}`).toBeDefined();
  const v = d!.rates[pool];
  expect(v, `${modelId} has no ${pool} rate recorded`).toBeDefined();
  return v!;
}

describe("computeStageCost", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("native cost path", () => {
    it("returns native when present even if the registry has no entry", () => {
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
      const result = computeStageCost(
        "gemini",
        "made-up-model-no-registry-entry",
        sampleTokens,
        0.5
      );
      expect(result).toEqual({ cost_usd: 0.5, source: "native" });
      // No drift comparison is possible without a registry entry; no warn.
      expect(warn).not.toHaveBeenCalled();
    });

    it("returns native and does NOT warn when computed agrees within 5%", () => {
      const computedExpected = computeStageCost("claude", "claude-sonnet-5", sampleTokens).cost_usd;
      const native = computedExpected * 1.005;
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

      const result = computeStageCost("claude", "claude-sonnet-5", sampleTokens, native);
      expect(result.source).toBe("native");
      expect(result.cost_usd).toBe(native);
      expect(warn).not.toHaveBeenCalled();
    });

    it("returns native and warns once when computed disagrees by more than 5%", () => {
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
      // No cache-write tokens at all, so the comparison is apples-to-apples and
      // the floor-convention suppression below cannot swallow this warn.
      const tokens: StageCostTokens = { input: 100_000, output: 50_000, cache_read: 20_000 };
      const computed = computeStageCost("claude", "claude-sonnet-5", tokens).cost_usd;
      const native = computed * 1.5;
      const result = computeStageCost("claude", "claude-sonnet-5", tokens, native);
      expect(result).toEqual({ cost_usd: native, source: "native" });
      expect(warn).toHaveBeenCalledTimes(1);
      const msg = String(warn.mock.calls[0][0]);
      expect(msg).toContain("Pricing drift");
      expect(msg).toContain("claude/claude-sonnet-5");
      expect(msg).toContain("native=$");
      expect(msg).toContain("computed=$");
      expect(msg).toContain("delta=");
      // The remediation must point at the surviving authority, not the deleted table.
      expect(msg).toContain("model-registry.json");
    });
  });

  describe("the drift warn must not cry wolf on the #358 floor convention", () => {
    // Every real Claude stage today books its UNSPLIT cache-write count into
    // the 5m slot (the documented #358 floor). On 1h-heavy traffic — which is
    // what the captured transcripts show — that floor under-reads the write
    // pool by up to 37.5%, so a naive native-vs-computed comparison fires the
    // >5% warn on essentially every stage. The registry is not wrong there;
    // the INPUT is a known-low floor, and a warn that fires every time trains
    // the operator to ignore the one that matters. So the warn only fires when
    // the comparison is apples-to-apples.
    //
    // This suppression is self-revoking: it is conditioned on the 1h count
    // being absent, so the moment #390 plumbs the real split the warn re-arms
    // with no code change here.
    const heavyWrites = 400_000;

    it("does NOT warn when cache writes are unsplit and the model has a distinct 1h rate", () => {
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
      const debug = vi.spyOn(console, "debug").mockImplementation(() => {});
      const unsplit: StageCostTokens = {
        input: 100_000,
        output: 50_000,
        cache_read: 20_000,
        cache_creation_5m: heavyWrites,
      };
      // Premise: the model prices the two write tiers differently, so booking
      // 1h writes as 5m genuinely produces a low computed number.
      const d = getModelDescriptor("claude-sonnet-5")!;
      expect(d.rates.cache_creation_1h).toBeDefined();
      expect(d.rates.cache_creation_1h).not.toBe(d.rates.cache_creation_5m);

      const computed = computeStageCost("claude", "claude-sonnet-5", unsplit).cost_usd;
      // The vendor billed the writes at the 1h rate; the floor booked them at 5m.
      const native = computeStageCost("claude", "claude-sonnet-5", {
        input: unsplit.input,
        output: unsplit.output,
        cache_read: unsplit.cache_read,
        cache_creation_1h: heavyWrites,
      }).cost_usd;
      expect(Math.abs(native - computed) / native).toBeGreaterThan(0.05);

      const result = computeStageCost("claude", "claude-sonnet-5", unsplit, native);
      expect(result).toEqual({ cost_usd: native, source: "native" });
      expect(warn).not.toHaveBeenCalled();
      // Downgraded, not discarded: the signal survives at debug level and names
      // the convention responsible so triage is not sent to the registry.
      expect(debug).toHaveBeenCalledTimes(1);
      const msg = String(debug.mock.calls[0][0]);
      expect(msg).toContain("#358");
      expect(msg).toContain("floor");
      expect(msg).toContain("#390");
    });

    it("DOES warn on the same drift once the split is known", () => {
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
      const split: StageCostTokens = {
        input: 100_000,
        output: 50_000,
        cache_read: 20_000,
        cache_creation_5m: heavyWrites / 2,
        cache_creation_1h: heavyWrites / 2,
      };
      const computed = computeStageCost("claude", "claude-sonnet-5", split).cost_usd;
      const native = computed * 1.5;
      const result = computeStageCost("claude", "claude-sonnet-5", split, native);
      expect(result).toEqual({ cost_usd: native, source: "native" });
      expect(warn).toHaveBeenCalledTimes(1);
      expect(String(warn.mock.calls[0][0])).toContain("Pricing drift");
    });

    it("DOES warn on unsplit writes when the model has no distinct 1h rate", () => {
      // OpenAI publishes one cache-write tier, so an unsplit count is not a
      // floor there — it is the whole truth, and a >5% gap is real drift.
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
      const d = getModelDescriptor("gpt-5.6-sol")!;
      expect(d.rates.cache_creation_1h).toBeUndefined();
      const unsplit: StageCostTokens = {
        input: 100_000,
        output: 50_000,
        cache_read: 20_000,
        cache_creation_5m: heavyWrites,
      };
      const computed = computeStageCost("codex", "gpt-5.6-sol", unsplit).cost_usd;
      computeStageCost("codex", "gpt-5.6-sol", unsplit, computed * 1.5);
      expect(warn).toHaveBeenCalledTimes(1);
      expect(String(warn.mock.calls[0][0])).toContain("Pricing drift");
    });
  });

  describe("the models the pipeline routes TODAY (#391 regression)", () => {
    // These three are what `resolveDispatchModel` actually hands the runner.
    // Against the deleted `providerPricing.ts` every one of them returned
    // { cost_usd: 0, source: 'unknown' }: that table's Claude keys had rotted
    // to the `claude-opus-4-8` era and carried no entry for any of them. Since
    // the extension's number wins over Go's whenever it is non-zero, a $0 here
    // is not a cosmetic label — it is the booked cost of the run.
    it.each(["claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5-20251001"])(
      "%s prices non-zero with source 'computed'",
      (model) => {
        const result = computeStageCost("claude", model, sampleTokens);
        expect(result.source).toBe("computed");
        expect(result.cost_usd).toBeGreaterThan(0);
      }
    );

    it("prices claude-opus-5 from the registry rates, pool by pool", () => {
      const expected =
        (100_000 * rate("claude-opus-5", "input") +
          50_000 * rate("claude-opus-5", "output") +
          20_000 * rate("claude-opus-5", "cache_read") +
          10_000 * getModelDescriptor("claude-opus-5")!.rates.cache_creation_5m!) /
        1_000_000;
      const result = computeStageCost("claude", "claude-opus-5", sampleTokens);
      expect(result.source).toBe("computed");
      expect(result.cost_usd).toBeCloseTo(expected, 6);
    });
  });

  describe("split cache-creation tiers (#358 / #390)", () => {
    it("prices a 1h write above the same-size 5m write", () => {
      const base: StageCostTokens = { input: 0, output: 0 };
      const as5m = computeStageCost("claude", "claude-opus-5", {
        ...base,
        cache_creation_5m: 100_000,
      });
      const as1h = computeStageCost("claude", "claude-opus-5", {
        ...base,
        cache_creation_1h: 100_000,
      });
      expect(as5m.cost_usd).toBeGreaterThan(0);
      // 1.25x vs 2.0x base input: the 5m floor is 62.5% of the 1h cost.
      expect(as5m.cost_usd / as1h.cost_usd).toBeCloseTo(0.625, 9);
    });

    it("sums both tiers when a caller supplies both", () => {
      const both = computeStageCost("claude", "claude-opus-5", {
        input: 0,
        output: 0,
        cache_creation_5m: 40_000,
        cache_creation_1h: 60_000,
      });
      const only5m = computeStageCost("claude", "claude-opus-5", {
        input: 0,
        output: 0,
        cache_creation_5m: 40_000,
      });
      const only1h = computeStageCost("claude", "claude-opus-5", {
        input: 0,
        output: 0,
        cache_creation_1h: 60_000,
      });
      expect(both.cost_usd).toBeCloseTo(only5m.cost_usd + only1h.cost_usd, 9);
    });
  });

  describe("non-Anthropic cache traffic (#392)", () => {
    // Before #392 every non-Anthropic registry entry carried input/output only,
    // so a codex or gemini stage's cache reads were priced at $0 via the
    // nil-rate-contributes-$0 path in `computeCostUsd` / `CalculateCost`. Both
    // providers bill cached input.
    it.each([
      ["gemini", "gemini-2.5-pro"],
      ["gemini", "gemini-2.5-flash"],
      ["gemini", "gemini-2.0-flash"],
      ["gemini-sdk", "gemini-2.5-pro"],
      ["codex", "gpt-5.6-sol"],
      ["codex", "gpt-5.6-terra"],
      ["codex", "gpt-5.6-luna"],
      ["codex", "gpt-5.5"],
      ["codex", "gpt-5.4"],
      ["codex", "gpt-5.4-mini"],
      ["codex", "gpt-5.2"],
      ["codex", "gpt-5.3-codex"],
    ] as const)("%s/%s charges for cache reads", (adapter, model) => {
      const noCache = computeStageCost(adapter, model, { input: 100_000, output: 50_000 });
      const withCache = computeStageCost(adapter, model, {
        input: 100_000,
        output: 50_000,
        cache_read: 500_000,
      });
      expect(withCache.source).toBe("computed");
      expect(withCache.cost_usd).toBeGreaterThan(noCache.cost_usd);
      expect(withCache.cost_usd - noCache.cost_usd).toBeCloseTo(
        (500_000 * rate(model, "cache_read")) / 1_000_000,
        6
      );
    });

    it("charges NO cache-write fee on pre-5.6 OpenAI entries — the omission is the published price", () => {
      // Before the GPT-5.6 family OpenAI discounts cached input and bills no
      // separate cache-write fee (the sheet prints '-'), so `cache_creation_*`
      // is deliberately absent on those entries. Inventing a write rate there
      // would invent a charge the vendor does not make. This pins the absence
      // as intentional rather than a pending gap.
      const d = getModelDescriptor("gpt-5.2")!;
      expect(d.rates.cache_creation_5m).toBeUndefined();
      expect(d.rates.cache_creation_1h).toBeUndefined();

      const noWrite = computeStageCost("codex", "gpt-5.2", { input: 1000, output: 500 });
      const withWrite = computeStageCost("codex", "gpt-5.2", {
        input: 1000,
        output: 500,
        cache_creation_5m: 1_000_000,
        cache_creation_1h: 1_000_000,
      });
      expect(withWrite.cost_usd).toBe(noWrite.cost_usd);
    });

    it("DOES charge a 5m cache-write fee on the GPT-5.6 family, and never a 1h one", () => {
      // "For GPT-5.6 models and later model families, cache writes cost 1.25x
      // the uncached input token rate" — the vendor's own sentence. The 1h slot
      // stays unpriced because OpenAI publishes a single write tier: a 1h count
      // reaching this path must contribute nothing rather than be charged at an
      // Anthropic-shaped guess.
      for (const model of ["gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"]) {
        const d = getModelDescriptor(model)!;
        expect(d.rates.cache_creation_5m).toBeCloseTo(d.rates.input * 1.25, 12);
        expect(d.rates.cache_creation_1h).toBeUndefined();

        const base = { input: 100_000, output: 50_000 };
        const noWrite = computeStageCost("codex", model, base);
        const with5m = computeStageCost("codex", model, { ...base, cache_creation_5m: 1_000_000 });
        expect(with5m.cost_usd - noWrite.cost_usd).toBeCloseTo(d.rates.cache_creation_5m!, 6);

        const with1h = computeStageCost("codex", model, { ...base, cache_creation_1h: 1_000_000 });
        expect(with1h.cost_usd).toBe(noWrite.cost_usd);
      }
    });

    it("still prices cache reads at $0 where the vendor publishes no row", () => {
      // HONEST GAP, pinned so it cannot be mistaken for correctness. Both
      // entries below carry no `cache_read` because the live sheet has no row
      // for them: gpt-5.1-codex-mini's row has been retired, and
      // gpt-5.3-codex-spark is a research preview that has never been listed
      // (its $0 input/output is a placeholder, not a price — pinned here so the
      // deleted extension table's old proxy rate cannot silently read as a
      // regression). Per the $schema_note that absence means UNRECORDED, not
      // unbilled. Copying a same-sticker sibling's rate is exactly what #392
      // forbids; the fix is a published row to transcribe.
      const unrecorded = ["gpt-5.1-codex-mini", "gpt-5.3-codex-spark"];
      for (const model of unrecorded) {
        expect(getModelDescriptor(model)!.rates.cache_read).toBeUndefined();
        const noCache = computeStageCost("codex", model, { input: 100_000, output: 50_000 });
        const withCache = computeStageCost("codex", model, {
          input: 100_000,
          output: 50_000,
          cache_read: 500_000,
        });
        expect(withCache.cost_usd).toBe(noCache.cost_usd);
        // Known but unpriced-for-that-pool is still 'computed', not 'unknown':
        // the model IS in the registry, only one of its pools is unrecorded.
        expect(withCache.source).toBe("computed");
      }
    });
  });

  describe("computed cost path", () => {
    it("computes Codex (gpt-5.5) input/output without cache fields", () => {
      const tokens: StageCostTokens = { input: 200_000, output: 100_000 };
      // 200k at $5/Mtok = $1.00; 100k at $30/Mtok = $3.00. Total $4.00.
      const result = computeStageCost("codex", "gpt-5.5", tokens);
      expect(result).toEqual({ cost_usd: 4.0, source: "computed" });
    });

    it("treats native=0 as 'no native cost' and falls through to computed", () => {
      // The Claude tokenParser path emits costUsd=0 when no native cost is
      // present; computeStageCost MUST treat that as "no native" and compute
      // from the registry rather than returning $0 with source 'native'.
      const result = computeStageCost("claude", "claude-haiku-4-5-20251001", sampleTokens, 0);
      expect(result.source).toBe("computed");
      expect(result.cost_usd).toBeGreaterThan(0);
    });
  });

  describe("local adapters", () => {
    // The registry deliberately carries no ollama/lm-studio entries: the
    // user-configured local model serves every band. `'unknown'` (not
    // `'computed'`) is the truthful label — the cost is $0 because it cannot
    // be priced, not because the vendor charges nothing per token.
    it("lm-studio returns { 0, 'unknown' } regardless of model string", () => {
      expect(computeStageCost("lm-studio", "any-local-model", sampleTokens)).toEqual({
        cost_usd: 0,
        source: "unknown",
      });
    });

    it("ollama returns { 0, 'unknown' } regardless of model string", () => {
      expect(computeStageCost("ollama", "llama3.2", sampleTokens)).toEqual({
        cost_usd: 0,
        source: "unknown",
      });
    });

    it("never prices a local model against a colliding registry id", () => {
      // A user is free to name their local checkpoint after a frontier model.
      // Routing that string into the registry would bill Anthropic's rates for
      // inference that costs the user nothing — so the local short-circuit runs
      // BEFORE any id lookup.
      expect(computeStageCost("ollama", "claude-opus-5", sampleTokens)).toEqual({
        cost_usd: 0,
        source: "unknown",
      });
    });
  });

  describe("zero-rate flat-billed adapters", () => {
    it("Copilot gpt-4o returns { 0, 'computed' } (subscription billing)", () => {
      // Copilot's registry entries record an explicit 0 input/output because it
      // bills flat per-request in the user's tier. That is knowingly-free, which
      // is strictly more information than 'unknown' — so it must NOT collapse
      // into the local/unknown bucket.
      expect(computeStageCost("copilot", "gpt-4o", sampleTokens)).toEqual({
        cost_usd: 0,
        source: "computed",
      });
    });
  });

  describe("unknown adapter+model", () => {
    it("returns { 0, 'unknown' } when the registry has no entry", () => {
      expect(computeStageCost("gemini", "made-up-model", sampleTokens)).toEqual({
        cost_usd: 0,
        source: "unknown",
      });
    });

    it("returns { 0, 'unknown' } for an unreleased Codex model", () => {
      expect(
        computeStageCost("codex", "future-unreleased-model", { input: 1000, output: 500 })
      ).toEqual({ cost_usd: 0, source: "unknown" });
    });

    it("does NOT fall back to an Anthropic tier band for a bare tier name", () => {
      // `getModelDescriptor` falls back to a tier-band lookup against the
      // `anthropic` provider when the string is not an exact id. On the billing
      // path that would price a codex stage running a model called `opus` at
      // Claude Opus rates. The exact-id `isKnownModel` gate makes that
      // unreachable.
      for (const tier of ["opus", "sonnet", "haiku", "fable"]) {
        expect(computeStageCost("codex", tier, sampleTokens)).toEqual({
          cost_usd: 0,
          source: "unknown",
        });
      }
    });

    it("does not throw on an empty model string", () => {
      expect(() => computeStageCost("claude", "", sampleTokens)).not.toThrow();
      expect(computeStageCost("claude", "", sampleTokens).source).toBe("unknown");
    });
  });

  describe("rounding precision", () => {
    it("rounds computed cost to 6 decimals (matches Claude precision)", () => {
      // 1 * $5 + 1 * $30 = $35 / 1_000_000 = 0.000035 — exact at 6 decimals, so
      // pair it with a value that genuinely needs rounding.
      expect(computeStageCost("codex", "gpt-5.5", { input: 1, output: 1 })).toEqual({
        cost_usd: 0.000035,
        source: "computed",
      });
      // 1 * $0.2 + 1 * $1.2 = $1.40 / 1_000_000 = 0.0000014 → 0.000001
      const rounded = computeStageCost("codex", "gpt-5.6-luna", { input: 1, output: 1 });
      expect(rounded.source).toBe("computed");
      expect(rounded.cost_usd).toBe(0.000001);
    });
  });
});
