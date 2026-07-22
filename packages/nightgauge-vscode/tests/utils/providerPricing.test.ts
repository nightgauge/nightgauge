/**
 * Unit tests for providerPricing.
 *
 * Covers required (adapter, model) coverage from the AC, the null-fallback
 * contract, the local-tier synthetic entry, and a stale-pricing guard
 * (warn at 90 days, fail at 180 days) per the issue spec.
 *
 * @see providerPricing.ts
 * @see Issue #3227 — Provider pricing tables: (adapter, model) cost map
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { listCodexModels } from "@nightgauge/sdk";
import {
  getProviderPricing,
  PRICING_TABLE_FOR_TESTS,
  type PricingEntry,
} from "../../src/utils/providerPricing";
import type { ExecutionAdapter } from "../../src/config/schema";

const REQUIRED_PAIRS: Array<{ adapter: ExecutionAdapter; model: string }> = [
  { adapter: "claude", model: "claude-opus-4-7" },
  { adapter: "claude", model: "claude-sonnet-4-6" },
  { adapter: "claude", model: "claude-haiku-4-5" },
  { adapter: "codex", model: "gpt-5.5" },
  { adapter: "codex", model: "gpt-5.4" },
  { adapter: "codex", model: "gpt-5.4-mini" },
  { adapter: "gemini", model: "gemini-2.5-pro" },
  { adapter: "gemini", model: "gemini-2.5-flash" },
  { adapter: "gemini-sdk", model: "gemini-2.5-pro" },
  { adapter: "gemini-sdk", model: "gemini-2.5-flash" },
  { adapter: "copilot", model: "gpt-4o" },
  { adapter: "copilot", model: "gpt-4o-mini" },
  { adapter: "copilot", model: "claude-sonnet-4.5" },
];

describe("getProviderPricing", () => {
  describe("required (adapter, model) coverage", () => {
    it.each(REQUIRED_PAIRS)(
      "returns a non-null entry for ($adapter, $model)",
      ({ adapter, model }) => {
        const entry = getProviderPricing(adapter, model);
        expect(entry).not.toBeNull();
        expect(entry?.last_verified).toMatch(/^\d{4}-\d{2}-\d{2}$/);
        expect(entry?.source_url.length).toBeGreaterThan(0);
      }
    );
  });

  describe("Claude entries", () => {
    it.each(["claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5"] as const)(
      "includes both cache fields for %s",
      (model) => {
        const entry = getProviderPricing("claude", model);
        expect(entry).not.toBeNull();
        expect(typeof entry?.cache_read_per_mtok).toBe("number");
        expect(typeof entry?.cache_write_per_mtok).toBe("number");
        expect(entry?.cache_read_per_mtok).toBeGreaterThan(0);
        expect(entry?.cache_write_per_mtok).toBeGreaterThan(0);
        expect(entry?.input_per_mtok).toBeGreaterThan(0);
        expect(entry?.output_per_mtok).toBeGreaterThan(0);
        expect(entry?.tier).toBe("paid");
      }
    );
  });

  describe("Codex entries", () => {
    it("returns gpt-5.5 with positive input/output rates", () => {
      const entry = getProviderPricing("codex", "gpt-5.5");
      expect(entry).not.toBeNull();
      expect(entry?.input_per_mtok).toBeGreaterThan(0);
      expect(entry?.output_per_mtok).toBeGreaterThan(0);
      expect(entry?.tier).toBe("paid");
    });

    it("returns gpt-5.4-mini (the lightweight tier) with positive rates", () => {
      const entry = getProviderPricing("codex", "gpt-5.4-mini");
      expect(entry).not.toBeNull();
      expect(entry?.input_per_mtok).toBeGreaterThan(0);
      expect(entry?.output_per_mtok).toBeGreaterThan(0);
      expect(entry?.tier).toBe("paid");
    });

    it("does not carry deprecated/invalid Codex model rows", () => {
      // Issue #4022 — drift cleanup: these ids were removed from the registry.
      expect(getProviderPricing("codex", "gpt-5.1-codex-mini")).toBeNull();
      expect(getProviderPricing("codex", "gpt-5-mini")).toBeNull();
      expect(getProviderPricing("codex", "gpt-5.3-codex")).toBeNull();
      expect(getProviderPricing("codex", "gpt-5.2")).toBeNull();
    });

    it("has a pricing row for every non-deprecated registry Codex model", () => {
      for (const model of listCodexModels()) {
        const entry = getProviderPricing("codex", model);
        expect(entry, `missing codex pricing row for ${model}`).not.toBeNull();
        expect(entry?.tier).toBe("paid");
      }
    });
  });

  describe("Gemini entries", () => {
    it("captures the long-context premium tier in notes for gemini-2.5-pro", () => {
      for (const adapter of ["gemini", "gemini-sdk"] as const) {
        const entry = getProviderPricing(adapter, "gemini-2.5-pro");
        expect(entry).not.toBeNull();
        expect(entry?.notes).toBeDefined();
        expect(entry?.notes?.length ?? 0).toBeGreaterThan(0);
      }
    });

    it("returns the same shape for gemini and gemini-sdk", () => {
      const a = getProviderPricing("gemini", "gemini-2.5-flash");
      const b = getProviderPricing("gemini-sdk", "gemini-2.5-flash");
      expect(a).not.toBeNull();
      expect(b).not.toBeNull();
      expect(a?.input_per_mtok).toBe(b?.input_per_mtok);
      expect(a?.output_per_mtok).toBe(b?.output_per_mtok);
    });
  });

  describe("Copilot entries", () => {
    it.each(["gpt-4o", "gpt-4o-mini", "claude-sonnet-4.5"] as const)(
      "%s has tier=paid and zero per-token rates",
      (model) => {
        const entry = getProviderPricing("copilot", model);
        expect(entry).not.toBeNull();
        expect(entry?.tier).toBe("paid");
        expect(entry?.input_per_mtok).toBe(0);
        expect(entry?.output_per_mtok).toBe(0);
        expect(entry?.notes).toBeDefined();
      }
    );
  });

  describe("local adapters (lm-studio / ollama)", () => {
    it.each(["lm-studio", "ollama"] as const)(
      "%s returns a tier=local zero entry for any model string",
      (adapter) => {
        for (const model of ["any-string", "llama3-70b", "qwen2.5-coder:32b", ""]) {
          const entry = getProviderPricing(adapter, model);
          expect(entry).not.toBeNull();
          expect(entry?.tier).toBe("local");
          expect(entry?.input_per_mtok).toBe(0);
          expect(entry?.output_per_mtok).toBe(0);
        }
      }
    );
  });

  describe("null fallback", () => {
    it("returns null for an unknown claude model", () => {
      expect(getProviderPricing("claude", "made-up-model")).toBeNull();
    });

    it("returns null for unknown codex / gemini / copilot models", () => {
      expect(getProviderPricing("codex", "gpt-9000")).toBeNull();
      expect(getProviderPricing("gemini", "gemini-99")).toBeNull();
      expect(getProviderPricing("gemini-sdk", "gemini-99")).toBeNull();
      expect(getProviderPricing("copilot", "unknown-model")).toBeNull();
    });

    it("does not throw for unknown combos — callers should be safe", () => {
      expect(() => getProviderPricing("claude", "")).not.toThrow();
    });
  });
});

describe("stale-pricing guard", () => {
  const WARN_DAYS = 90;
  const FAIL_DAYS = 180;
  const MS_PER_DAY = 1000 * 60 * 60 * 24;

  function daysSince(verifiedDate: string, now: Date): number {
    // Compute on UTC midnight to avoid timezone / clock-skew flakiness.
    const verified = new Date(`${verifiedDate}T00:00:00Z`);
    const today = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate()));
    return Math.floor((today.getTime() - verified.getTime()) / MS_PER_DAY);
  }

  function* iterEntries(): Iterable<{
    adapter: string;
    model: string;
    entry: PricingEntry;
  }> {
    for (const [adapter, models] of Object.entries(PRICING_TABLE_FOR_TESTS)) {
      if (!models) continue;
      for (const [model, entry] of Object.entries(models)) {
        yield { adapter, model, entry };
      }
    }
  }

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("warns (does not fail) when an entry is older than 90 days", () => {
    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const now = new Date();
    const stale: string[] = [];
    for (const { adapter, model, entry } of iterEntries()) {
      if (entry.tier === "local") continue;
      const days = daysSince(entry.last_verified, now);
      if (days >= WARN_DAYS) {
        stale.push(`${adapter}/${model}: ${days} days since verification`);
      }
    }
    if (stale.length > 0) {
      console.warn(
        `[providerPricing] ${stale.length} entr${
          stale.length === 1 ? "y" : "ies"
        } past 90-day verification:\n  ${stale.join("\n  ")}`
      );
      expect(warnSpy).toHaveBeenCalled();
    } else {
      // No stale entries — warn was not expected.
      expect(warnSpy).not.toHaveBeenCalled();
    }
  });

  it("fails when any non-local entry is older than 180 days", () => {
    const now = new Date();
    for (const { adapter, model, entry } of iterEntries()) {
      if (entry.tier === "local") continue;
      const days = daysSince(entry.last_verified, now);
      expect(
        days,
        `${adapter}/${model} last verified ${entry.last_verified} ` +
          `(${days} days ago) — refresh required (>=180 days)`
      ).toBeLessThan(FAIL_DAYS);
    }
  });

  it("every non-local entry has a parseable YYYY-MM-DD last_verified", () => {
    for (const { adapter, model, entry } of iterEntries()) {
      expect(entry.last_verified, `${adapter}/${model}`).toMatch(/^\d{4}-\d{2}-\d{2}$/);
      expect(
        Number.isNaN(new Date(`${entry.last_verified}T00:00:00Z`).getTime()),
        `${adapter}/${model} has unparseable last_verified=${entry.last_verified}`
      ).toBe(false);
    }
  });
});
