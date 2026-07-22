/**
 * Unit tests for computeStageCost.
 *
 * Covers the three-step resolver chain (native -> table-computed -> unknown)
 * and the >5% drift warn signal for native vs. computed disagreement.
 *
 * @see Issue #3228 — Unified `computeStageCost` across all adapters
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { computeStageCost, type StageCostTokens } from "../../src/utils/computeStageCost";

const sampleTokens: StageCostTokens = {
  input: 100_000,
  output: 50_000,
  cache_read: 20_000,
  cache_creation: 10_000,
};

describe("computeStageCost", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("native cost path", () => {
    it("returns native when present even if no table entry exists", () => {
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
      const result = computeStageCost("gemini", "made-up-model-no-table-entry", sampleTokens, 0.5);
      expect(result).toEqual({ cost_usd: 0.5, source: "native" });
      // No drift comparison is possible without a table entry; no warn.
      expect(warn).not.toHaveBeenCalled();
    });

    it("returns native and does NOT warn when computed agrees within 5%", () => {
      // Manually compute Sonnet cost for sampleTokens:
      //   100k * $3 + 50k * $15 + 20k * $0.3 + 10k * $3.75 = $300 + $750 + $6 + $37.5
      //   = $1093.5 / 1_000_000 = 0.001094 (rounded to 6 dp)
      const computedExpected =
        (100_000 * 3 + 50_000 * 15 + 20_000 * 0.3 + 10_000 * 3.75) / 1_000_000;
      // Native within 1% of computed → no warn
      const native = computedExpected * 1.005;
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

      const result = computeStageCost("claude", "claude-sonnet-4-6", sampleTokens, native);
      expect(result.source).toBe("native");
      expect(result.cost_usd).toBe(native);
      expect(warn).not.toHaveBeenCalled();
    });

    it("returns native and warns once when computed disagrees by more than 5%", () => {
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
      // Native is 50% higher than what the table predicts → drift warn fires.
      const native = 0.005;
      const result = computeStageCost("claude", "claude-sonnet-4-6", sampleTokens, native);
      expect(result).toEqual({ cost_usd: native, source: "native" });
      expect(warn).toHaveBeenCalledTimes(1);
      // The warn message must include enough info to diagnose pricing drift.
      const msg = String(warn.mock.calls[0][0]);
      expect(msg).toContain("Pricing drift");
      expect(msg).toContain("claude/claude-sonnet-4-6");
      expect(msg).toContain("native=$");
      expect(msg).toContain("computed=$");
      expect(msg).toContain("delta=");
    });
  });

  describe("computed cost path", () => {
    it("returns computed for Claude Opus when no native cost is supplied", () => {
      // 100k * $5 + 50k * $25 + 20k * $0.5 + 10k * $6.25
      //   = $500 + $1250 + $10 + $62.5 = $1822.5 / 1_000_000 = 0.0018225
      const expected = (100_000 * 5 + 50_000 * 25 + 20_000 * 0.5 + 10_000 * 6.25) / 1_000_000;
      const result = computeStageCost("claude", "claude-opus-4-7", sampleTokens);
      expect(result.source).toBe("computed");
      expect(result.cost_usd).toBeCloseTo(expected, 6);
    });

    it("computes Codex (gpt-5.5) input/output without cache fields", () => {
      const tokens: StageCostTokens = { input: 200_000, output: 100_000 };
      // 200k tokens at $1.25/Mtok = $0.25. 100k at $10/Mtok = $1.00.
      // Total = $1.25.
      const result = computeStageCost("codex", "gpt-5.5", tokens);
      expect(result).toEqual({ cost_usd: 1.25, source: "computed" });
    });

    it("treats native=0 as 'no native cost' and falls through to computed", () => {
      // The Claude tokenParser path emits costUsd=0 when no native cost is
      // present; computeStageCost MUST treat that as "no native" and compute
      // from the table rather than returning $0 with source 'native'.
      const result = computeStageCost("claude", "claude-haiku-4-5", sampleTokens, 0);
      expect(result.source).toBe("computed");
      expect(result.cost_usd).toBeGreaterThan(0);
    });

    it("respects cache discounts in the computed cost", () => {
      const noCacheTokens: StageCostTokens = { input: 100_000, output: 50_000 };
      const withCacheTokens: StageCostTokens = {
        input: 100_000,
        output: 50_000,
        cache_read: 50_000,
        cache_creation: 10_000,
      };
      const noCache = computeStageCost("claude", "claude-sonnet-4-6", noCacheTokens);
      const withCache = computeStageCost("claude", "claude-sonnet-4-6", withCacheTokens);
      // 50k cache_read at $0.3/Mtok = $0.015. 10k cache_creation at
      // $3.75/Mtok = $0.0375. Cache contribution to delta = $0.0525.
      expect(withCache.cost_usd - noCache.cost_usd).toBeCloseTo(0.0525, 6);
    });
  });

  describe("local adapters", () => {
    it("lm-studio returns { 0, 'computed' } regardless of model string", () => {
      const result = computeStageCost("lm-studio", "any-local-model", sampleTokens);
      expect(result).toEqual({ cost_usd: 0, source: "computed" });
    });

    it("ollama returns { 0, 'computed' } regardless of model string", () => {
      const result = computeStageCost("ollama", "llama3.2", sampleTokens);
      expect(result).toEqual({ cost_usd: 0, source: "computed" });
    });
  });

  describe("zero-rate flat-billed adapters", () => {
    it("Copilot gpt-4o returns { 0, 'computed' } (subscription billing)", () => {
      const result = computeStageCost("copilot", "gpt-4o", sampleTokens);
      // The table records 0 input/output rates because Copilot bills flat
      // per-request; treating it as "computed zero" is more accurate than
      // labeling it "unknown".
      expect(result).toEqual({ cost_usd: 0, source: "computed" });
    });
  });

  describe("unknown adapter+model", () => {
    it("returns { 0, 'unknown' } when no table entry exists", () => {
      const result = computeStageCost("gemini", "made-up-model", sampleTokens);
      expect(result).toEqual({ cost_usd: 0, source: "unknown" });
    });

    it("returns { 0, 'unknown' } when native is undefined and adapter has no table", () => {
      // Codex with unknown model — no table entry exists.
      const result = computeStageCost("codex", "future-unreleased-model", {
        input: 1000,
        output: 500,
      });
      expect(result).toEqual({ cost_usd: 0, source: "unknown" });
    });
  });

  describe("rounding precision", () => {
    it("rounds computed cost to 6 decimals (matches Claude precision)", () => {
      // Pick tokens that produce a long-tail value:
      //   1 * $1.25 + 1 * $10 = $11.25 / 1_000_000 = 0.00001125 (8 dp)
      //   should round to 0.000011 (6 dp)
      const result = computeStageCost("codex", "gpt-5.5", { input: 1, output: 1 });
      expect(result.source).toBe("computed");
      // 6-decimal rounding: 0.00001125 → 0.000011
      expect(result.cost_usd).toBe(0.000011);
    });
  });
});
