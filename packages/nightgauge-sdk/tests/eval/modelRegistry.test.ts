/**
 * Tests for the provider-agnostic model & pricing registry (Issue #4169).
 *
 * Asserts the registry is the single source of truth: cost computation matches
 * the previously-hardcoded rates (regression guard), the derived
 * DEFAULT_MODEL_COST_RATES equals the old hand-maintained table, tier/id
 * resolution is correct, and a non-Anthropic model resolves cleanly.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import {
  MODEL_REGISTRY,
  activeModels,
  getModelDescriptor,
  resolveModelForAdapter,
  providerForAdapter,
  isKnownModel,
  computeCostUsd,
  deriveDefaultModelCostRates,
  assertEffortLevelsMatchAuthority,
  assertTransportRatesCarryProvenance,
} from "../../src/eval/modelRegistry.js";
import { EFFORT_LEVELS, ModelDescriptorSchema } from "../../src/eval/modelEvalSchemas.js";
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

  it("declares Haiku's empty effort axis rather than inventing levels (#336)", () => {
    // Haiku has no extended thinking, so it has no effort axis at all. `[]`
    // says that; the previous ["low","medium","high"] was dead data that the
    // dispatch path had to contradict with a hardcoded band set.
    expect(getModelDescriptor("haiku")?.supported_efforts).toEqual([]);
    // `[]` is the declaration; "unknown" is having no descriptor at all.
    expect(getModelDescriptor("qwen3-coder:32b")).toBeUndefined();
  });

  it("every other non-deprecated Anthropic model declares a usable ladder", () => {
    for (const m of activeModels()) {
      if (m.provider !== "anthropic" || m.id === "claude-haiku-4-5-20251001") continue;
      expect(m.supported_efforts.length, `${m.id} declares no effort levels`).toBeGreaterThan(0);
    }
  });

  it("every non-deprecated Anthropic model declares a non-empty tier band (#336)", () => {
    // Keeps the tier-LESS class non-Anthropic by construction. `modelTierBand`
    // returns undefined for an entry with no `tiers`, so the extension's
    // `--effort` emission gate sees that model's raw id instead of a band —
    // safe only because the gate is scoped to `adapter === "claude"`. An
    // Anthropic entry without tiers would put an id on the Claude path that
    // the band-keyed lookups cannot answer for.
    for (const m of activeModels()) {
      if (m.provider !== "anthropic") continue;
      expect(m.tiers?.length ?? 0, `${m.id} declares no tier band`).toBeGreaterThan(0);
    }
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
    // Asserts the PROPERTY, not the id of the day. Pinning ids here means the
    // test has to be chased forward on every model release, and a test nobody
    // updates is a test that documents a superseded model as correct (#74).
    for (const tier of ["haiku", "sonnet", "opus", "fable"] as const) {
      const resolved = getModelDescriptor(tier);
      expect(resolved, `no model serves the anthropic/${tier} band`).toBeDefined();
      expect(resolved?.deprecated ?? false, `${tier} resolves to deprecated ${resolved?.id}`).toBe(
        false
      );
      expect(resolved?.tiers).toContain(tier);
      expect(resolved?.provider).toBe("anthropic");
    }
  });

  it("prefers the live model over a deprecated one sharing the same band", () => {
    // Both sonnet 4.6/5 and opus 4.8/5 coexist; resolution must skip the
    // deprecated entry rather than returning whichever is listed first.
    const deprecatedIds = MODEL_REGISTRY.filter((m) => m.deprecated).map((m) => m.id);
    expect(deprecatedIds.length, "fixture assumes deprecated entries exist").toBeGreaterThan(0);
    for (const tier of ["sonnet", "opus"] as const) {
      expect(deprecatedIds).not.toContain(getModelDescriptor(tier)?.id);
    }
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
    expect(providerForAdapter("grok")).toBe("xai");
    expect(providerForAdapter("grok-headless")).toBe("xai");
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
    expect(resolveModelForAdapter("grok", "haiku")?.id).toBe("grok-4.6");
    expect(resolveModelForAdapter("grok", "sonnet")?.id).toBe("grok-4.6");
    expect(resolveModelForAdapter("grok", "opus")?.id).toBe("grok-4.6");
    expect(resolveModelForAdapter("grok", "fable")?.id).toBe("grok-4.6");
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
    // Rates live-verified 2026-08-09 and pinned to their vendor citations by
    // packages/nightgauge-vscode/tests/utils/registryRatesLiveVerified.test.ts.
    expect(computeCostUsd("gemini-2.5-flash", { input: M, output: M })).toBeCloseTo(0.3 + 2.5, 6);
    expect(computeCostUsd("gpt-5.5", { input: M, output: M })).toBeCloseTo(5.0 + 30.0, 6);
  });

  it("bills cache tokens at their rates, with the two cache-write tiers priced apart (#358)", () => {
    // opus: cache_read 0.5/M, cache_creation_5m 6.25/M, cache_creation_1h 10/M
    const cost = computeCostUsd("claude-opus-4-8", {
      input: 0,
      output: 0,
      cacheRead: M,
      cacheCreation5m: M,
      cacheCreation1h: M,
    });
    expect(cost).toBeCloseTo(0.5 + 6.25 + 10.0, 6);

    // The tiers must not collapse into each other: an hour-long write costs
    // 1.6x a five-minute one, which is exactly what a single blended rate hid.
    const fiveMin = computeCostUsd("claude-opus-4-8", { input: 0, output: 0, cacheCreation5m: M });
    const oneHour = computeCostUsd("claude-opus-4-8", { input: 0, output: 0, cacheCreation1h: M });
    expect(fiveMin).toBeCloseTo(6.25, 6);
    expect(oneHour).toBeCloseTo(10.0, 6);
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

describe("model registry — orthogonal axis fields (#578, spike #568 §1/§2)", () => {
  it("declares the effort ladder as data, equal to EFFORT_LEVELS exactly", () => {
    const file = JSON.parse(
      readFileSync(resolve(__dirname, "../../src/eval/model-registry.json"), "utf-8")
    ) as { effort_levels: string[] };
    expect(file.effort_levels).toEqual([...EFFORT_LEVELS]);
  });

  it("pins the measured grok CLI catalog facts (M-cat, 2026-08-15)", () => {
    for (const [id, served] of [
      ["grok-4.6", true],
      ["grok-4.5", true],
      ["grok-build-0.1", false],
    ] as const) {
      const m = getModelDescriptor(id);
      expect(m, `${id} missing from registry`).toBeDefined();
      const cli = m?.transports?.cli;
      expect(cli, `${id} declares no cli transport facts`).toBeDefined();
      expect(cli?.served, `${id} cli served`).toBe(served);
      expect(cli?.verified).toBe("2026-08-15");
      expect(cli?.evidence, `${id} carries a verified date but no evidence`).toBeTruthy();
      // The xai api transport is pending #553; pending stays unexpressed.
      expect(m?.transports?.api, `${id} guessed an api transport fact`).toBeUndefined();
    }
  });

  it("states grok-build-0.1's #532 unreachability as data, not via deprecated", () => {
    // served:false means "exists at the provider, unreachable through this
    // transport" — the fact previously smuggled through `deprecated: true`.
    expect(getModelDescriptor("grok-build-0.1")?.transports?.cli?.served).toBe(false);
  });

  it("records each rate card's provenance per the spike inventory", () => {
    const expected: Record<string, string> = {
      "grok-4.6": "measured",
      "grok-4.5": "measured",
      "grok-build-0.1": "list",
      "gpt-4o-mini": "subscription",
      "gpt-4o": "subscription",
      "claude-sonnet-4.5": "subscription",
      "gpt-5.3-codex-spark": "placeholder",
      "vendor-x-pro": "placeholder",
    };
    for (const m of MODEL_REGISTRY) {
      // Everything not called out above transcribes a vendor sheet: "list".
      expect(m.rate_provenance, `${m.id} rate_provenance`).toBe(expected[m.id] ?? "list");
    }
  });

  it("leaves unverified/pending transport cells unexpressed rather than guessed", () => {
    // Deprecated openai/google entries whose reachability the spike could not
    // verify, and the fixture entry, carry no transports at all.
    for (const id of [
      "gpt-5.2",
      "gpt-5.3-codex",
      "gpt-5.1-codex-mini",
      "gemini-2.0-flash",
      "vendor-x-pro",
    ]) {
      expect(getModelDescriptor(id)?.transports, `${id} transports`).toBeUndefined();
    }
  });

  it("assertEffortLevelsMatchAuthority requires exact order and membership", () => {
    expect(() => assertEffortLevelsMatchAuthority([...EFFORT_LEVELS])).not.toThrow();
    expect(() => assertEffortLevelsMatchAuthority([])).toThrow(/effort_levels/);
    expect(() => assertEffortLevelsMatchAuthority(["low", "medium", "high", "xhigh"])).toThrow();
    expect(() =>
      assertEffortLevelsMatchAuthority(["low", "medium", "xhigh", "high", "max"])
    ).toThrow();
    expect(() => assertEffortLevelsMatchAuthority([...EFFORT_LEVELS, "ultra"])).toThrow();
  });

  it("assertTransportRatesCarryProvenance rejects an unattributed transport rate card", () => {
    const base = MODEL_REGISTRY[0];
    const withProvenance = {
      ...base,
      transports: {
        cli: { served: true, rates: { input: 1, output: 2 }, rate_provenance: "measured" as const },
      },
    };
    const withoutProvenance = {
      ...base,
      transports: { cli: { served: true, rates: { input: 1, output: 2 } } },
    };
    expect(() => assertTransportRatesCarryProvenance([withProvenance])).not.toThrow();
    expect(() => assertTransportRatesCarryProvenance([withoutProvenance])).toThrow(
      /rates without.*rate_provenance/s
    );
    // Rate-less transport facts need no provenance — they inherit the top-level card.
    expect(() =>
      assertTransportRatesCarryProvenance([{ ...base, transports: { cli: { served: true } } }])
    ).not.toThrow();
  });

  it("the shipped data passes both loader asserts (the module loaded proves it, restated readably)", () => {
    expect(() => assertTransportRatesCarryProvenance(MODEL_REGISTRY)).not.toThrow();
    const file = JSON.parse(
      readFileSync(resolve(__dirname, "../../src/eval/model-registry.json"), "utf-8")
    ) as { effort_levels: string[] };
    expect(() => assertEffortLevelsMatchAuthority(file.effort_levels)).not.toThrow();
  });
});
