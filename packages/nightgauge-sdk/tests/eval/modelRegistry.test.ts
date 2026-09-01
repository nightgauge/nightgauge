/**
 * Tests for the provider-agnostic model & pricing registry (Issue #4169).
 *
 * Asserts the registry is the single source of truth: cost computation matches
 * the previously-hardcoded rates (regression guard), the derived
 * per-(provider, band) rates equal the old hand-maintained table, tier/id
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
  ratesForProviderTier,
  assertEffortLevelsMatchAuthority,
  assertTransportRatesCarryProvenance,
  assertNonDeprecatedModelsDeclareTransports,
  assertAdapterTransportsComplete,
  CLOSED_TRANSPORT_ADAPTERS,
  transportForAdapter,
  mustTransportForAdapter,
} from "../../src/eval/modelRegistry.js";
import { EFFORT_LEVELS, ModelDescriptorSchema } from "../../src/eval/modelEvalSchemas.js";
import { ANTHROPIC_TIER_COST_RATES } from "../../src/analysis/types.js";

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
      "claude-fable-5-1",
    ]) {
      expect(ids.has(id)).toBe(true);
    }
    expect(getModelDescriptor("claude-sonnet-5")?.deprecated).toBeUndefined();
  });

  it("claude-fable-5-1 leads the fable band; 5 is deprecated behind it (#1274)", () => {
    // The band invariant is a paired edit: the loader rejects two
    // non-deprecated models on one (provider, band), so registering 5.1
    // without deprecating 5 throws at load rather than resolving either one.
    expect(getModelDescriptor("fable", "anthropic")?.id).toBe("claude-fable-5-1");
    expect(getModelDescriptor("claude-fable-5-1")?.deprecated).toBeUndefined();
    expect(getModelDescriptor("claude-fable-5")?.deprecated).toBe(true);
    expect(getModelDescriptor("claude-fable-5")?.replacement).toBe("claude-fable-5-1");
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
    ["claude-fable-5-1", 10.0 + 50.0],
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

  it("prices Fable 5.1 cache reads at its own $0.25/MTok, not the 0.1x line (#1274)", () => {
    // Every other Anthropic entry reads at 0.1x input. 5.1 reads at 0.025x —
    // $0.25/MTok on a $10.00 input sticker, a QUARTER of Fable 5's rate. A
    // derivation-by-multiplier would price this pool 4x too high and make a
    // cache-heavy frontier stage look far more expensive than it bills.
    const only = (
      t: Partial<Record<"cacheRead" | "cacheCreation5m" | "cacheCreation1h", number>>
    ) => ({ input: 0, output: 0, ...t });
    expect(computeCostUsd("claude-fable-5-1", only({ cacheRead: M }))).toBeCloseTo(0.25, 6);
    expect(computeCostUsd("claude-fable-5", only({ cacheRead: M }))).toBeCloseTo(1.0, 6);

    // The WRITE pools are unchanged between the two, so the read rate is the
    // only axis that moved.
    expect(computeCostUsd("claude-fable-5-1", only({ cacheCreation5m: M }))).toBeCloseTo(12.5, 6);
    expect(computeCostUsd("claude-fable-5-1", only({ cacheCreation1h: M }))).toBeCloseTo(20.0, 6);
  });
});

describe("model registry — per-(provider, band) rates (regression guard)", () => {
  it("derives exactly the previously hand-maintained Anthropic tier table", () => {
    // Same values the hand-maintained table carried; only the entry point
    // changed. `deriveDefaultModelCostRates()` was replaced by
    // `ratesForProviderTier(provider, band)` in #1213 because a function whose
    // name says "default" invites exactly the silent Anthropic fallback that
    // priced grok runs at Claude rates.
    const anthropic = Object.fromEntries(
      (["haiku", "sonnet", "opus", "fable"] as const).map((b) => [
        b,
        ratesForProviderTier("anthropic", b),
      ])
    );
    expect(anthropic).toEqual({
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
        // 0.025x input, not the 0.1x every other Anthropic band takes: Fable
        // 5.1 publishes $0.25/MTok cached reads, a quarter of Fable 5's rate
        // (#1274). This row is the band LEADER's card, so it moved when the
        // band leader did.
        cacheReadPerMillion: 0.25,
        cacheCreationPerMillion: 12.5,
      },
    });
  });

  it("the ANTHROPIC_TIER_COST_RATES export is that table", () => {
    // Still exported, still Anthropic-only — but NAMED for its provider now,
    // and read only by the two hypothetical-comparison analyzers, never by the
    // estimator.
    expect(ANTHROPIC_TIER_COST_RATES.sonnet).toEqual(ratesForProviderTier("anthropic", "sonnet"));
  });

  it("returns a DIFFERENT card for another provider serving the same band", () => {
    // The defect #1213 fixed: every band was priced from the Anthropic
    // descriptor whatever adapter served the run.
    const anthropic = ratesForProviderTier("anthropic", "sonnet")!;
    const xai = ratesForProviderTier("xai", "sonnet")!;
    expect(xai.inputPerMillion).not.toBe(anthropic.inputPerMillion);
    expect(xai.outputPerMillion).not.toBe(anthropic.outputPerMillion);
  });

  it("returns undefined — never another provider's rates — when a band is unserved", () => {
    // The TypeScript analogue of Go's Stamped=false. ollama has no registry
    // entries by design; substituting Anthropic's card here is the whole bug.
    expect(ratesForProviderTier("ollama", "sonnet")).toBeUndefined();
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
      // The xai api transport is settled NOT BUILT (#553, won't do), so this
      // cell is permanently unexpressed — there is no transport to describe.
      expect(m?.transports?.api, `${id} declares an api transport fact`).toBeUndefined();
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
    // verify, and the fixture entry (also deprecated as of #600's graduation
    // gate — a non-deprecated entry may no longer omit transports entirely;
    // see the "graduation" describe block below), carry no transports at all.
    for (const id of [
      "gpt-5.2",
      "gpt-5.3-codex",
      "gpt-5.1-codex-mini",
      "gemini-2.0-flash",
      "vendor-x-pro",
    ]) {
      expect(getModelDescriptor(id)?.transports, `${id} transports`).toBeUndefined();
      expect(getModelDescriptor(id)?.deprecated, `${id} deprecated`).toBe(true);
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

// ─── Graduation: non-deprecated entries require transports facts (#600) ─────

describe("model registry — transports graduation (#600)", () => {
  it("assertNonDeprecatedModelsDeclareTransports rejects a non-deprecated entry with no transports block", () => {
    const base = MODEL_REGISTRY[0];
    const noFacts = { ...base, id: "no-facts-test", transports: undefined };
    expect(() => assertNonDeprecatedModelsDeclareTransports([noFacts])).toThrow(/no-facts-test/);
    expect(() => assertNonDeprecatedModelsDeclareTransports([noFacts])).toThrow(
      /no transports block/
    );

    const emptyFacts = { ...base, id: "empty-facts-test", transports: {} };
    expect(() => assertNonDeprecatedModelsDeclareTransports([emptyFacts])).toThrow(
      /empty-facts-test/
    );

    const withFacts = { ...base, id: "has-facts-test", transports: { cli: { served: true } } };
    expect(() => assertNonDeprecatedModelsDeclareTransports([withFacts])).not.toThrow();
  });

  it("assertNonDeprecatedModelsDeclareTransports permits a DEPRECATED entry with no transports block", () => {
    const base = MODEL_REGISTRY[0];
    const deprecatedNoFacts = {
      ...base,
      id: "deprecated-no-facts-test",
      deprecated: true,
      transports: undefined,
    };
    expect(() => assertNonDeprecatedModelsDeclareTransports([deprecatedNoFacts])).not.toThrow();
  });

  it("the shipped registry graduates: every non-deprecated entry declares at least one transport fact", () => {
    // If this ever regressed, loadRegistry() would already have thrown at
    // module import before this test ran — restated readably.
    expect(() => assertNonDeprecatedModelsDeclareTransports(MODEL_REGISTRY)).not.toThrow();
    for (const m of MODEL_REGISTRY) {
      if (m.deprecated) continue;
      expect(Object.keys(m.transports ?? {}).length, `${m.id} transports`).toBeGreaterThan(0);
    }
  });

  it("vendor-x-pro is the deliberate deprecated-carrier fixture (#600 fixture migration)", () => {
    // Graduation forbids a non-deprecated entry from omitting transports
    // entirely, so the deliberate no-transports fixture was flipped to
    // deprecated:true to keep exercising checkTransportServed's fail-open
    // branch (modelPreflight.test.ts) without violating the gate.
    const m = getModelDescriptor("vendor-x-pro", "other");
    expect(m?.deprecated).toBe(true);
    expect(m?.transports).toBeUndefined();
  });
});

// ─── Single-authority adapter→transport mapping (#600) ──────────────────────

describe("model registry — adapter→transport axis (#600)", () => {
  it("assertAdapterTransportsComplete requires EXACTLY the closed-transport-adapter set", () => {
    const complete = { codex: "cli", gemini: "cli", "gemini-sdk": "cli", grok: "cli" } as const;
    expect(() => assertAdapterTransportsComplete(complete)).not.toThrow();

    const missing = { codex: "cli", gemini: "cli", grok: "cli" } as const;
    expect(() => assertAdapterTransportsComplete(missing)).toThrow(/adapter_transports/);

    const extra = { ...complete, copilot: "cli" } as const;
    expect(() => assertAdapterTransportsComplete(extra)).toThrow(/adapter_transports/);
  });

  it("transportForAdapter/mustTransportForAdapter pin the decided mapping — parity with Go's TestTransportForAdapterPinsTheDecidedMapping", () => {
    // Hardcoded (not derived) so this suite and the Go suite each
    // independently pin the same values — the established cross-language
    // parity pattern in this codebase. An edit to adapter_transports that
    // nobody updates BOTH pins for fails loud in whichever language runs.
    const want: Record<(typeof CLOSED_TRANSPORT_ADAPTERS)[number], string> = {
      codex: "cli",
      gemini: "cli",
      "gemini-sdk": "cli",
      grok: "cli",
    };
    for (const adapter of CLOSED_TRANSPORT_ADAPTERS) {
      expect(transportForAdapter(adapter)).toBe(want[adapter]);
      expect(mustTransportForAdapter(adapter)).toBe(want[adapter]);
    }
  });

  it("gemini-sdk is deliberately pinned to cli, not api, despite its name (#600 judgment call)", () => {
    expect(transportForAdapter("gemini-sdk")).toBe("cli");
  });

  it("transportForAdapter returns undefined for OPEN adapters and unknown names", () => {
    expect(transportForAdapter("claude-headless")).toBeUndefined();
    expect(transportForAdapter("ollama")).toBeUndefined();
    expect(transportForAdapter("carrier-pigeon")).toBeUndefined();
  });

  it("mustTransportForAdapter throws for an adapter outside the closed-transport-adapter set", () => {
    expect(() => mustTransportForAdapter("claude-headless")).toThrow(/adapter_transports/);
  });
});
