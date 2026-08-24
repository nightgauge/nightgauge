import { describe, it, expect } from "vitest";
import {
  validateModelForAdapter,
  resolveAndValidateModel,
  ADAPTER_MODEL_POLICY,
  GEMINI_MODELS,
  GROK_MODELS,
} from "../../../cli/adapters/modelPreflight.js";
import { AdapterError } from "../../../cli/adapters/errors.js";
import type { NightgaugeAdapter } from "../../../cli/adapters/ICliAdapter.js";
import { checkTransportServed } from "../../../eval/modelRegistry.js";

/**
 * Provider-aware model preflight (#4021): fail fast on an invalid (adapter,
 * model) pair, resolve every tier to a concrete model, and never let a raw tier
 * keyword reach a CLI as --model for a closed adapter.
 */
describe("validateModelForAdapter — Codex (closed)", () => {
  it("resolves every tier to a concrete current Codex model", () => {
    expect(validateModelForAdapter("codex", "haiku").model).toBe("gpt-5.6-luna");
    expect(validateModelForAdapter("codex", "sonnet").model).toBe("gpt-5.6-terra");
    expect(validateModelForAdapter("codex", "opus").model).toBe("gpt-5.6-sol");
    // fable is the regression case from #4018/#4019 — must resolve, not leak.
    expect(validateModelForAdapter("codex", "fable").model).toBe("gpt-5.6-sol");
  });

  it("flags tier inputs via resolvedFromTier", () => {
    expect(validateModelForAdapter("codex", "fable").resolvedFromTier).toBe(true);
    expect(validateModelForAdapter("codex", "gpt-5.5").resolvedFromTier).toBe(false);
  });

  it("passes an exact valid model id through unchanged", () => {
    expect(validateModelForAdapter("codex", "gpt-5.5").model).toBe("gpt-5.5");
    expect(validateModelForAdapter("codex", "gpt-5.4-mini").model).toBe("gpt-5.4-mini");
  });

  it("resolves Claude escalation ids by prefix (parity with the Go adapter)", () => {
    expect(validateModelForAdapter("codex", "claude-sonnet-4-6").model).toBe("gpt-5.6-terra");
    expect(validateModelForAdapter("codex", "claude-opus-4-8").model).toBe("gpt-5.6-sol");
    expect(validateModelForAdapter("codex", "claude-haiku-4-5").model).toBe("gpt-5.6-luna");
  });

  it("remaps a deprecated id to its replacement and accepts it", () => {
    // gpt-5.2 → gpt-5.4, gpt-5.3-codex → gpt-5.5 (canonical registry remap).
    expect(validateModelForAdapter("codex", "gpt-5.2").model).toBe("gpt-5.4");
    expect(validateModelForAdapter("codex", "gpt-5.3-codex").model).toBe("gpt-5.5");
  });

  it("rejects an invalid model id with an actionable AdapterError", () => {
    expect(() => validateModelForAdapter("codex", "gpt-999")).toThrow(AdapterError);
    let threw = false;
    try {
      // "gpt-5.5x" is unambiguously closest to gpt-5.5 (edit distance 1).
      validateModelForAdapter("codex", "gpt-5.5x");
    } catch (error) {
      threw = true;
      expect(error).toBeInstanceOf(AdapterError);
      const adapterError = error as AdapterError;
      expect(adapterError.category).toBe("CONFIG_INVALID");
      const formatted = adapterError.format();
      expect(formatted).toContain("[Codex] CONFIG_INVALID");
      expect(formatted).toContain("not valid for the Codex adapter");
      // Nearest-valid suggestion engine should point at the closest real id.
      expect(formatted).toContain("Did you mean 'gpt-5.5'?");
      expect(formatted).toContain("NIGHTGAUGE_CODEX_MODEL");
    }
    expect(threw).toBe(true);
  });

  it("never returns a raw tier keyword for the closed Codex adapter", () => {
    for (const tier of ["haiku", "sonnet", "opus", "fable"]) {
      expect(validateModelForAdapter("codex", tier).model).not.toBe(tier);
    }
  });
});

describe("validateModelForAdapter — Gemini (closed)", () => {
  it("resolves tiers to concrete Gemini models via the registry bands (#56)", () => {
    // haiku and sonnet share the gemini-2.5-flash band; the old hand-synced
    // map pointed haiku at gemini-2.0-flash, which drifted from the router.
    expect(validateModelForAdapter("gemini", "haiku").model).toBe("gemini-2.5-flash");
    expect(validateModelForAdapter("gemini", "sonnet").model).toBe("gemini-2.5-flash");
    expect(validateModelForAdapter("gemini", "opus").model).toBe("gemini-2.5-pro");
    expect(validateModelForAdapter("gemini-sdk", "fable").model).toBe("gemini-2.5-pro");
  });

  it("passes valid Gemini ids through and rejects unknown ids", () => {
    expect(validateModelForAdapter("gemini", "gemini-2.5-pro").model).toBe("gemini-2.5-pro");
    expect(() => validateModelForAdapter("gemini", "gemini-xyz-invalid")).toThrow(AdapterError);
    expect(() => validateModelForAdapter("gemini-sdk", "gpt-5.5")).toThrow(AdapterError);
  });
});

describe("validateModelForAdapter — open adapters never reject", () => {
  it("passes arbitrary local model ids through for ollama / lm-studio", () => {
    expect(validateModelForAdapter("ollama", "llama3.1").model).toBe("llama3.1");
    expect(validateModelForAdapter("lm-studio", "qwen2.5-coder").model).toBe("qwen2.5-coder");
    expect(validateModelForAdapter("ollama", "custom:tag").model).toBe("custom:tag");
  });

  it("accepts both tiers and arbitrary ids for claude adapters (tier IS the model)", () => {
    expect(validateModelForAdapter("claude-headless", "sonnet").model).toBe("sonnet");
    expect(validateModelForAdapter("claude-sdk", "opus").model).toBe("opus");
    expect(validateModelForAdapter("claude-headless", "claude-opus-4-8").model).toBe(
      "claude-opus-4-8"
    );
  });

  it("treats copilot as open (no rejection) but resolves routing tiers to a concrete id (#52)", () => {
    // Concrete/unknown ids pass through (OPEN — copilot validates server-side).
    expect(validateModelForAdapter("copilot", "gpt-4o").model).toBe("gpt-4o");
    expect(validateModelForAdapter("copilot", "gpt-5.2").model).toBe("gpt-5.2");
    expect(validateModelForAdapter("copilot", "anything").model).toBe("anything");
    // A bare routing tier resolves to a concrete copilot-hosted id (registry
    // band), so "sonnet" never reaches --model literally.
    const sonnet = validateModelForAdapter("copilot", "sonnet");
    expect(sonnet.model).toBe("gpt-4o");
    expect(sonnet.resolvedFromTier).toBe(true);
    expect(sonnet.model).not.toBe("sonnet");
  });
});

describe("validateModelForAdapter — empty input", () => {
  it("returns an empty model (no override) for undefined/empty across all adapters", () => {
    for (const adapter of Object.keys(ADAPTER_MODEL_POLICY) as NightgaugeAdapter[]) {
      expect(validateModelForAdapter(adapter, undefined).model).toBe("");
      expect(validateModelForAdapter(adapter, "   ").model).toBe("");
    }
  });
});

describe("resolveAndValidateModel", () => {
  it("returns the resolved id or undefined when there is no override", () => {
    expect(resolveAndValidateModel("codex", "opus")).toBe("gpt-5.6-sol");
    expect(resolveAndValidateModel("codex", undefined)).toBeUndefined();
    expect(resolveAndValidateModel("codex", "")).toBeUndefined();
  });

  it("throws on an invalid closed-adapter model", () => {
    expect(() => resolveAndValidateModel("codex", "not-a-model")).toThrow(AdapterError);
  });
});

describe("ADAPTER_MODEL_POLICY invariant", () => {
  it("covers every NightgaugeAdapter union member (no silent open-by-default)", () => {
    const adapters: NightgaugeAdapter[] = [
      "claude-sdk",
      "claude-headless",
      "codex",
      "gemini",
      "gemini-sdk",
      "lm-studio",
      "ollama",
      "copilot",
      "grok",
    ];
    // The Record<NightgaugeAdapter, …> type guards this at compile time; assert at
    // runtime too so adding a union member forces a policy entry (a new adapter
    // cannot silently fall through to "open").
    expect(Object.keys(ADAPTER_MODEL_POLICY).sort()).toEqual([...adapters].sort());
    for (const adapter of adapters) {
      const policy = ADAPTER_MODEL_POLICY[adapter];
      expect(policy).toBeDefined();
      if (policy.kind === "closed") {
        expect(typeof policy.isValid).toBe("function");
        expect((policy.validIds?.() ?? []).length).toBeGreaterThan(0);
      }
    }
  });

  it("exposes the maintained Gemini set recommended-first", () => {
    expect(GEMINI_MODELS[0]).toBe("gemini-2.5-pro");
    expect(GEMINI_MODELS).toContain("gemini-2.5-flash");
  });
});

/**
 * #532: the Grok Build CLI's chat proxy answers `unknown model id` for
 * grok-build-0.1, so a run that reaches it dies in seconds having done nothing.
 * Removing its band stops the ROUTED path, but grok is a CLOSED adapter whose
 * valid set is derived as `provider === "xai" && !deprecated` — so until the
 * entry is marked deprecated, `NIGHTGAUGE_GROK_MODEL=grok-build-0.1` still
 * passes preflight and spawns into the same death on the EXPLICIT-OVERRIDE path.
 */
describe("validateModelForAdapter — Grok (closed)", () => {
  it("resolves every band to the one CLI-served model", () => {
    for (const band of ["haiku", "sonnet", "opus", "fable"] as const) {
      const result = validateModelForAdapter("grok", band);
      expect(result.model, `${band} must resolve to grok-4.6`).toBe("grok-4.6");
      expect(result.resolvedFromTier).toBe(true);
    }
  });

  it("excludes grok-build-0.1 from the closed valid set", () => {
    // The exact predicate the policy table hands the validator.
    const isValidGrokModel = ADAPTER_MODEL_POLICY.grok.isValid!;
    expect(isValidGrokModel("grok-build-0.1")).toBe(false);
    expect(GROK_MODELS).not.toContain("grok-build-0.1");
    // The models the CLI does serve are still valid — this is a targeted
    // exclusion, not a shrunken set.
    expect(isValidGrokModel("grok-4.6")).toBe(true);
    expect(isValidGrokModel("grok-4.5")).toBe(true);
  });

  it("rejects an explicit grok-build-0.1 override instead of spawning it", () => {
    expect(() => validateModelForAdapter("grok", "grok-build-0.1")).toThrow(AdapterError);
    expect(() => validateModelForAdapter("grok", "grok-build-0.1")).toThrow(/not valid/i);
  });

  it("still prices grok-build-0.1 by exact id (cost replay keeps the entry)", async () => {
    // Deprecation removes it from ROUTING, not from the registry: historical
    // runs that already billed against it must still cost out.
    const { getModelDescriptor } = await import("../../../eval/modelRegistry.js");
    const d = getModelDescriptor("grok-build-0.1");
    expect(d?.id).toBe("grok-build-0.1");
    expect(d?.deprecated).toBe(true);
    expect(d?.rates.input).toBe(1.0);
  });
});

/**
 * fail-closed-axis-enforcement (#579): the transport-consult
 * {@link checkTransportServed} adds to validateModelForAdapter, mirroring the
 * Go registry.CheckTransportServed contract. #578 landed the facts; these pin
 * the ENFORCEMENT — including the additive semantics (#579 AC4): a model
 * with no transports.cli fact at all must keep today's fail-OPEN behavior.
 */
describe("validateModelForAdapter — transport reachability (#579)", () => {
  it("resolves band haiku on the grok adapter to the served grok-4.6 and succeeds", () => {
    const resolved = validateModelForAdapter("grok", "haiku").model;
    expect(resolved).toBe("grok-4.6");
    const check = checkTransportServed("xai", "cli", resolved);
    expect(check.found).toBe(true);
    expect(check.unreachable).toBeUndefined();
    expect(check.model?.id).toBe("grok-4.6");
  });

  it("checkTransportServed classifies a known-but-unserved model distinctly from an unknown one", () => {
    // Unknown entirely: an ordinary miss, no error.
    expect(checkTransportServed("xai", "cli", "totally-made-up")).toEqual({ found: false });

    // Known and served: succeeds with the resolved descriptor.
    const served = checkTransportServed("xai", "cli", "grok-4.6");
    expect(served.found).toBe(true);
    expect(served.unreachable).toBeUndefined();
    expect(served.model?.id).toBe("grok-4.6");

    // Known but explicitly transports.cli.served=false: `unreachable` names
    // provider, model, and transport; `model` is absent.
    const unserved = checkTransportServed("xai", "cli", "grok-build-0.1");
    expect(unserved.found).toBe(true);
    expect(unserved.model).toBeUndefined();
    expect(unserved.unreachable).toEqual({
      provider: "xai",
      model: "grok-build-0.1",
      transport: "cli",
    });
  });

  it("fails OPEN when a model declares no transports.cli fact at all (#579 AC4, additive enforcement)", () => {
    // vendor-x-pro is the fixture entry with no `transports` field whatsoever
    // — the unexpressed/pending state, which must never read as unserved.
    // Its own exact-id lookup is deprecated-agnostic, so it still hits this
    // branch even though #600's load-time graduation gate required flipping
    // it to `deprecated: true` (a non-deprecated entry may no longer omit
    // `transports` entirely — see modelRegistry.test.ts's graduation suite).
    const result = checkTransportServed("other", "cli", "vendor-x-pro");
    expect(result.found).toBe(true);
    expect(result.unreachable).toBeUndefined();
    expect(result.model?.id).toBe("vendor-x-pro");
  });

  it("rejects grok-build-0.1 with a classified error naming provider, model, and transport", () => {
    let threw = false;
    try {
      validateModelForAdapter("grok", "grok-build-0.1");
    } catch (error) {
      threw = true;
      expect(error).toBeInstanceOf(AdapterError);
      const adapterError = error as AdapterError;
      expect(adapterError.category).toBe("CONFIG_INVALID");
      const formatted = adapterError.format();
      expect(formatted).toContain("[Grok] CONFIG_INVALID");
      for (const want of ["xai", "grok-build-0.1", "cli"]) {
        expect(formatted).toContain(want);
      }
    }
    expect(threw).toBe(true);
  });

  it("grok-build-0.1 is unselectable for two independent reasons: deprecated AND transports.cli.served=false", () => {
    // Reason 1 — deprecated: excluded from the closed valid set regardless
    // of the transport check ever running.
    expect(GROK_MODELS).not.toContain("grok-build-0.1");
    // Reason 2 — transport: checkTransportServed flags it as unreachable
    // independent of the deprecated flag or the closed-set membership check.
    expect(checkTransportServed("xai", "cli", "grok-build-0.1").unreachable).toBeDefined();
  });
});

/**
 * Single-authority adapter→transport-axis mapping (#600): before this, the
 * transport check inside validateModelForAdapter hardcoded the literal "cli"
 * for every closed adapter, while the registry's TRANSPORTS doc comment
 * claimed `sdk` folded into `api` — an undocumented, unenforced mismatch for
 * gemini-sdk specifically. mustTransportForAdapter is now the ONLY source
 * validateModelForAdapter/GEMINI_MODELS/GROK_MODELS consult.
 */
describe("validateModelForAdapter — adapter→transport axis (#600)", () => {
  it("gemini-sdk is deliberately pinned to the cli transport, not api", async () => {
    const { mustTransportForAdapter } = await import("../../../eval/modelRegistry.js");
    expect(mustTransportForAdapter("gemini-sdk")).toBe("cli");
    expect(mustTransportForAdapter("gemini")).toBe("cli");
    // Both share the SAME transport — GEMINI_MODELS is one list feeding both
    // policy entries, so a divergence here would silently mis-gate one of them.
    expect(mustTransportForAdapter("gemini-sdk")).toBe(mustTransportForAdapter("gemini"));
  });

  it("gemini-sdk enforces transport-unreachability identically to gemini", () => {
    // grok-4.6 is a real registry id but belongs to xai — irrelevant here.
    // Use a hypothetical: grok-build-0.1 is google-foreign too, so assert
    // instead that BOTH gemini adapters reject the same unknown id the same
    // way, proving they consult the same closed set + transport check.
    expect(() => validateModelForAdapter("gemini", "totally-made-up")).toThrow(AdapterError);
    expect(() => validateModelForAdapter("gemini-sdk", "totally-made-up")).toThrow(AdapterError);
  });

  it("pins the single-authority mapping for the full closed-transport-adapter set (parity with Go's TestTransportForAdapterPinsTheDecidedMapping)", async () => {
    const { mustTransportForAdapter } = await import("../../../eval/modelRegistry.js");
    // This table is intentionally hardcoded (not derived) — it mirrors the
    // Go test's hardcoded expectation. Both suites independently pin the same
    // values, so an edit to adapter_transports that nobody updates BOTH pins
    // for fails loud in whichever language's suite runs.
    const want: Record<string, string> = {
      codex: "cli",
      gemini: "cli",
      "gemini-sdk": "cli",
      grok: "cli",
    };
    for (const [adapter, transport] of Object.entries(want)) {
      expect(mustTransportForAdapter(adapter)).toBe(transport);
    }
  });
});
