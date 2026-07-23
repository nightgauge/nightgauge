/**
 * Tests for the provider-agnostic model & pricing registry (Issue #4169).
 *
 * Asserts the registry is the single source of truth: cost computation matches
 * the previously-hardcoded rates (regression guard), the derived
 * DEFAULT_MODEL_COST_RATES equals the old hand-maintained table, tier/id
 * resolution is correct, and a non-Anthropic model resolves cleanly.
 */

import { describe, it, expect } from "vitest";
import {
  MODEL_REGISTRY,
  activeModels,
  getModelDescriptor,
  resolveModelForAdapter,
  providerForAdapter,
  isKnownModel,
  computeCostUsd,
  deriveDefaultModelCostRates,
} from "../../src/eval/modelRegistry.js";
import { ModelDescriptorSchema } from "../../src/eval/modelEvalSchemas.js";
import { DEFAULT_MODEL_COST_RATES } from "../../src/analysis/types.js";

describe("model registry — integrity", () => {
  it("every entry validates against ModelDescriptorSchema", () => {
    for (const m of MODEL_REGISTRY) {
      expect(() => ModelDescriptorSchema.parse(m)).not.toThrow();
    }
  });

  it("has no duplicate ids", () => {
    const ids = MODEL_REGISTRY.map((m) => m.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("seeds the current Claude Code models including Sonnet 5", () => {
    const ids = new Set(MODEL_REGISTRY.map((m) => m.id));
    for (const id of [
      "claude-opus-4-8",
      "claude-sonnet-5",
      "claude-haiku-4-5-20251001",
      "claude-fable-5",
    ]) {
      expect(ids.has(id)).toBe(true);
    }
    expect(getModelDescriptor("claude-sonnet-5")?.deprecated).toBeUndefined();
  });

  it("includes at least one non-Anthropic provider-neutral model", () => {
    expect(MODEL_REGISTRY.some((m) => m.provider !== "anthropic")).toBe(true);
  });

  it("keeps historical models but marks them deprecated", () => {
    for (const id of ["claude-sonnet-4-6", "claude-opus-4-7", "claude-opus-4-6"]) {
      expect(getModelDescriptor(id)?.deprecated).toBe(true);
    }
    expect(activeModels().every((m) => !m.deprecated)).toBe(true);
  });
});

describe("model registry — resolution", () => {
  it("resolves by concrete id", () => {
    expect(getModelDescriptor("claude-opus-4-8")?.display_name).toBe("Opus 4.8");
  });

  it("resolves a tier alias to the current (non-deprecated) model", () => {
    // sonnet has two entries; the tier must resolve to Sonnet 5, not the deprecated 4.6.
    expect(getModelDescriptor("sonnet")?.id).toBe("claude-sonnet-5");
    expect(getModelDescriptor("opus")?.id).toBe("claude-opus-4-8");
  });

  it("returns undefined for an unknown id/tier", () => {
    expect(getModelDescriptor("nope")).toBeUndefined();
  });

  it("tier lookups default to anthropic even though other providers share band names (#56)", () => {
    for (const tier of ["haiku", "sonnet", "opus", "fable"] as const) {
      expect(getModelDescriptor(tier)?.provider).toBe("anthropic");
    }
  });

  it("resolves provider tier bands, including multi-band models (#56)", () => {
    expect(getModelDescriptor("haiku", "openai")?.id).toBe("gpt-5.6-luna");
    expect(getModelDescriptor("sonnet", "openai")?.id).toBe("gpt-5.6-terra");
    expect(getModelDescriptor("opus", "openai")?.id).toBe("gpt-5.6-sol");
    expect(getModelDescriptor("fable", "openai")?.id).toBe("gpt-5.6-sol");
    expect(getModelDescriptor("haiku", "google")?.id).toBe("gemini-2.5-flash");
    expect(getModelDescriptor("sonnet", "google")?.id).toBe("gemini-2.5-flash");
    expect(getModelDescriptor("opus", "google")?.id).toBe("gemini-2.5-pro");
    expect(getModelDescriptor("opus", "copilot")?.id).toBe("claude-sonnet-4.5");
  });

  it("enforces at most one non-deprecated model per (provider, band)", () => {
    const seen = new Set<string>();
    for (const m of activeModels()) {
      for (const tier of m.tiers ?? []) {
        const key = `${m.provider}/${tier}`;
        expect(seen.has(key), `duplicate band ${key}`).toBe(false);
        seen.add(key);
      }
    }
  });
});

describe("model registry — adapter resolution (#56)", () => {
  it("maps adapters to providers", () => {
    expect(providerForAdapter("claude")).toBe("anthropic");
    expect(providerForAdapter("claude-sdk")).toBe("anthropic");
    expect(providerForAdapter("claude-headless")).toBe("anthropic");
    expect(providerForAdapter("codex")).toBe("openai");
    expect(providerForAdapter("gemini")).toBe("google");
    expect(providerForAdapter("gemini-sdk")).toBe("google");
    expect(providerForAdapter("copilot")).toBe("copilot");
    expect(providerForAdapter("ollama")).toBe("ollama");
    expect(providerForAdapter("lm-studio")).toBe("lm-studio");
    expect(providerForAdapter("mystery")).toBe("other");
  });

  it("resolves tiers per adapter through the registry", () => {
    expect(resolveModelForAdapter("codex", "sonnet")?.id).toBe("gpt-5.6-terra");
    expect(resolveModelForAdapter("gemini", "fable")?.id).toBe("gemini-2.5-pro");
    expect(resolveModelForAdapter("gemini-sdk", "haiku")?.id).toBe("gemini-2.5-flash");
    expect(resolveModelForAdapter("copilot", "haiku")?.id).toBe("gpt-4o-mini");
  });

  it("local adapters have no tier hierarchy — every tier misses", () => {
    for (const adapter of ["ollama", "lm-studio"]) {
      for (const tier of ["haiku", "sonnet", "opus", "fable"]) {
        expect(resolveModelForAdapter(adapter, tier)).toBeUndefined();
      }
    }
  });
});

describe("model registry — cost computation (parity with prior hardcoded rates)", () => {
  // 1M input + 1M output → input$/M + output$/M.
  const M = 1_000_000;
  const cases: Array<[string, number]> = [
    ["claude-haiku-4-5-20251001", 1.0 + 5.0],
    ["claude-sonnet-5", 3.0 + 15.0],
    ["claude-sonnet-4-6", 3.0 + 15.0],
    ["claude-opus-4-8", 5.0 + 25.0],
    ["claude-fable-5", 10.0 + 50.0],
  ];
  for (const [id, expected] of cases) {
    it(`${id} costs $${expected} for 1M in + 1M out`, () => {
      expect(computeCostUsd(id, { input: M, output: M })).toBeCloseTo(expected, 6);
    });
  }

  it("unknown model costs a truthful $0, flagged via isKnownModel (matches Go default, #56)", () => {
    expect(computeCostUsd("totally-unknown", { input: M, output: M })).toBe(0);
    expect(isKnownModel("totally-unknown")).toBe(false);
    expect(isKnownModel("claude-opus-4-8")).toBe(true);
  });

  it("non-Anthropic registry models cost at their own rates (#56)", () => {
    expect(computeCostUsd("gemini-2.5-flash", { input: M, output: M })).toBeCloseTo(0.3 + 2.5, 6);
    expect(computeCostUsd("gpt-5.5", { input: M, output: M })).toBeCloseTo(1.25 + 10.0, 6);
  });

  it("bills cache tokens at their rates", () => {
    // opus: cache_read 0.5/M, cache_creation 6.25/M
    const cost = computeCostUsd("claude-opus-4-8", {
      input: 0,
      output: 0,
      cacheRead: M,
      cacheCreation: M,
    });
    expect(cost).toBeCloseTo(0.5 + 6.25, 6);
  });
});

describe("model registry — derived DEFAULT_MODEL_COST_RATES (regression guard)", () => {
  it("derives exactly the previously hand-maintained tier table", () => {
    expect(deriveDefaultModelCostRates()).toEqual({
      haiku: {
        inputPerMillion: 1.0,
        outputPerMillion: 5.0,
        cacheReadPerMillion: 0.1,
        cacheCreationPerMillion: 1.25,
      },
      sonnet: {
        inputPerMillion: 3.0,
        outputPerMillion: 15.0,
        cacheReadPerMillion: 0.3,
        cacheCreationPerMillion: 3.75,
      },
      opus: {
        inputPerMillion: 5.0,
        outputPerMillion: 25.0,
        cacheReadPerMillion: 0.5,
        cacheCreationPerMillion: 6.25,
      },
      fable: {
        inputPerMillion: 10.0,
        outputPerMillion: 50.0,
        cacheReadPerMillion: 1.0,
        cacheCreationPerMillion: 12.5,
      },
    });
  });

  it("the analysis DEFAULT_MODEL_COST_RATES export is the derived table", () => {
    expect(DEFAULT_MODEL_COST_RATES).toEqual(deriveDefaultModelCostRates());
  });
});
