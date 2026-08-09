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
      const computed = computeStageCost("claude", "claude-sonnet-5", sampleTokens).cost_usd;
      const native = computed * 1.5;
      const result = computeStageCost("claude", "claude-sonnet-5", sampleTokens, native);
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
      ["codex", "gpt-5.2"],
      ["codex", "gpt-5.3-codex"],
      ["codex", "gpt-5.1-codex-mini"],
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

    it("charges NO cache-write fee on OpenAI entries — the omission is the published price", () => {
      // OpenAI discounts cached input and bills no separate cache-write fee, so
      // `cache_creation_*` is deliberately absent from every openai entry.
      // Inventing a write rate there would invent a charge the vendor does not
      // make. This pins the absence as intentional rather than a pending gap.
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

    it("still prices cache reads at $0 where no rate has been transcribed yet", () => {
      // HONEST GAP, pinned so it cannot be mistaken for correctness: the
      // registry entries below carry no `cache_read` because nobody has
      // transcribed their published cached-input row. Per the $schema_note
      // that absence means UNRECORDED, not unbilled — OpenAI does bill cached
      // input on these. Their cache reads therefore contribute $0 and the
      // stage under-reports. Deriving the rate from a same-sticker sibling is
      // exactly what #392 forbids; the fix is to transcribe the vendor's row.
      const unrecorded = ["gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4"];
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
      // 200k at $1.25/Mtok = $0.25; 100k at $10/Mtok = $1.00. Total $1.25.
      const result = computeStageCost("codex", "gpt-5.5", tokens);
      expect(result).toEqual({ cost_usd: 1.25, source: "computed" });
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
      // 1 * $1.25 + 1 * $10 = $11.25 / 1_000_000 = 0.00001125 → 0.000011
      const result = computeStageCost("codex", "gpt-5.5", { input: 1, output: 1 });
      expect(result.source).toBe("computed");
      expect(result.cost_usd).toBe(0.000011);
    });
  });
});
