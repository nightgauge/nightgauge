import { describe, it, expect } from "vitest";
import {
  validateModelForAdapter,
  resolveAndValidateModel,
  ADAPTER_MODEL_POLICY,
  GEMINI_MODELS,
} from "../../../cli/adapters/modelPreflight.js";
import { AdapterError } from "../../../cli/adapters/errors.js";
import type { IncrediAdapter } from "../../../cli/adapters/ICliAdapter.js";

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
    for (const adapter of Object.keys(ADAPTER_MODEL_POLICY) as IncrediAdapter[]) {
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
  it("covers every IncrediAdapter union member (no silent open-by-default)", () => {
    const adapters: IncrediAdapter[] = [
      "claude-sdk",
      "claude-headless",
      "codex",
      "gemini",
      "gemini-sdk",
      "lm-studio",
      "ollama",
      "copilot",
    ];
    // The Record<IncrediAdapter, …> type guards this at compile time; assert at
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
