/**
 * Selection query over the registry axes (#581 / spike #568 §4.1).
 *
 * The ladder tests double as the cross-language parity pin: the Go
 * derivation (internal/intelligence/routing/selection_test.go) asserts the
 * SAME rungs for the same providers, so the two implementations cannot
 * drift apart silently.
 */

import { describe, it, expect } from "vitest";
import {
  candidateLadder,
  escalationLadder,
  resolveBandEnvelope,
  ESCALATION_CEILING_BAND,
  type EnvelopeRung,
} from "../../src/eval/selectionQuery.js";
import { TIER_BANDS, TIER_BANDS_STRONGEST_FIRST, isTierBand } from "../../src/eval/tierBands.js";

describe("tier band authority", () => {
  it("declares the one ascending order and derives strongest-first from it", () => {
    expect(TIER_BANDS).toEqual(["haiku", "sonnet", "opus", "fable"]);
    expect(TIER_BANDS_STRONGEST_FIRST).toEqual([...TIER_BANDS].reverse());
  });

  it("answers band membership", () => {
    expect(isTierBand("fable")).toBe(true);
    expect(isTierBand("claude-sonnet-5")).toBe(false);
    expect(isTierBand("")).toBe(false);
  });
});

describe("candidateLadder — anthropic (rungs span models)", () => {
  const ladder = candidateLadder("anthropic");

  it("derives one rung per band, strongest first, from registry membership", () => {
    expect(ladder.map((r) => [r.band, r.modelId])).toEqual([
      ["fable", "claude-fable-5-1"],
      ["opus", "claude-opus-5"],
      ["sonnet", "claude-sonnet-5"],
      ["haiku", "claude-haiku-4-5-20251001"],
    ]);
  });

  it("carries each single-band model's DECLARED default effort and thinking — absent when undeclared", () => {
    // The FULL four-rung envelope, mirroring the Go twin
    // (TestCandidateLadderAnthropicSpansModels) rung for rung — the twin
    // pins must be symmetric, not spot-checked on one side.
    expect(ladder).toEqual<EnvelopeRung[]>([
      { band: "fable", modelId: "claude-fable-5-1", effort: "high", thinking: "on" },
      { band: "opus", modelId: "claude-opus-5", effort: "high", thinking: "on" },
      { band: "sonnet", modelId: "claude-sonnet-5", effort: "high", thinking: "on" },
      { band: "haiku", modelId: "claude-haiku-4-5-20251001", effort: undefined, thinking: "off" },
    ]);
  });
});

describe("candidateLadder — xai (rungs descend through EFFORT within one model, #532)", () => {
  it("expresses the grok-4.6 band collapse as an effort ladder the band vocabulary could not", () => {
    // All four bands map to grok-4.6 (supported_efforts low..xhigh): the
    // rungs are grok-4.6@xhigh → high → medium → low — a real cost/latency
    // ladder — instead of four identical band rungs (the #532 cost no-op).
    expect(candidateLadder("xai")).toEqual<EnvelopeRung[]>([
      { band: "fable", modelId: "grok-4.6", effort: "xhigh", thinking: "on" },
      { band: "opus", modelId: "grok-4.6", effort: "high", thinking: "on" },
      { band: "sonnet", modelId: "grok-4.6", effort: "medium", thinking: "on" },
      { band: "haiku", modelId: "grok-4.6", effort: "low", thinking: "on" },
    ]);
  });
});

describe("candidateLadder — partial multi-band providers", () => {
  it("google: two models, each descending within its own band span", () => {
    expect(candidateLadder("google")).toEqual<EnvelopeRung[]>([
      { band: "fable", modelId: "gemini-2.5-pro", effort: "high", thinking: undefined },
      { band: "opus", modelId: "gemini-2.5-pro", effort: "medium", thinking: undefined },
      { band: "sonnet", modelId: "gemini-2.5-flash", effort: "high", thinking: undefined },
      { band: "haiku", modelId: "gemini-2.5-flash", effort: "medium", thinking: undefined },
    ]);
  });

  it("openai: gpt-5.6-sol spans opus+fable; the single-band models rung at their (undeclared) default", () => {
    expect(candidateLadder("openai")).toEqual<EnvelopeRung[]>([
      { band: "fable", modelId: "gpt-5.6-sol", effort: "xhigh", thinking: undefined },
      { band: "opus", modelId: "gpt-5.6-sol", effort: "high", thinking: undefined },
      { band: "sonnet", modelId: "gpt-5.6-terra", effort: undefined, thinking: undefined },
      { band: "haiku", modelId: "gpt-5.6-luna", effort: undefined, thinking: undefined },
    ]);
  });

  it("local providers have no registry entries — empty ladder, callers keep the configured model", () => {
    expect(candidateLadder("ollama")).toEqual([]);
    expect(candidateLadder("lm-studio")).toEqual([]);
  });
});

describe("capability discipline (spike §4.3)", () => {
  it("rungs carry NO capability field — the capability axis participates as absent", () => {
    for (const provider of ["anthropic", "openai", "google", "xai", "copilot"] as const) {
      for (const rung of candidateLadder(provider)) {
        expect(Object.keys(rung).sort()).toEqual(["band", "effort", "modelId", "thinking"]);
      }
    }
  });

  it("ordering is exactly the declared band order — no capability fact reorders it", () => {
    for (const provider of ["anthropic", "openai", "google", "xai", "copilot"] as const) {
      const bands = candidateLadder(provider).map((r) => r.band);
      const declared = TIER_BANDS_STRONGEST_FIRST.filter((b) => bands.includes(b));
      expect(bands).toEqual(declared);
    }
  });
});

describe("resolveBandEnvelope — the band-input query", () => {
  it("resolves a band to its dispatch envelope for a provider", () => {
    expect(resolveBandEnvelope("anthropic", "sonnet")?.modelId).toBe("claude-sonnet-5");
    expect(resolveBandEnvelope("xai", "sonnet")).toEqual({
      band: "sonnet",
      modelId: "grok-4.6",
      effort: "medium",
      thinking: "on",
    });
  });

  it("returns undefined when the provider has no model for the band", () => {
    expect(resolveBandEnvelope("ollama", "sonnet")).toBeUndefined();
  });

  it("excludes models whose transport explicitly declares served:false, and only those (#579)", () => {
    // No current registry entry declares served:false on a banded model, so
    // the transport filter must be a no-op today — absent facts pass through.
    expect(candidateLadder("xai", "cli")).toEqual(candidateLadder("xai"));
    expect(candidateLadder("anthropic", "api")).toEqual(candidateLadder("anthropic"));
  });
});

describe("escalationLadder — membership from the registry, ceiling from policy", () => {
  it("reproduces the pre-cutover [haiku, sonnet, opus] escalation walk for anthropic", () => {
    expect(ESCALATION_CEILING_BAND).toBe("opus");
    expect(escalationLadder("anthropic")).toEqual(["haiku", "sonnet", "opus"]);
  });

  it("never includes fable — the frontier tier is explicit-opt-in only", () => {
    for (const provider of ["anthropic", "openai", "google", "xai", "copilot"] as const) {
      expect(escalationLadder(provider)).not.toContain("fable");
    }
  });

  it("drops bands the provider has no live model for", () => {
    expect(escalationLadder("ollama")).toEqual([]);
  });
});
