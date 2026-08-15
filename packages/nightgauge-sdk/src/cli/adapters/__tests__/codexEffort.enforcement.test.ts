/**
 * codexEffort.enforcement.test.ts (#569)
 *
 * The registry's `supported_efforts` is the semantic authority on the Codex
 * `model_reasoning_effort` value; the static CODEX_REASONING_EFFORTS
 * vocabulary is syntax only. These tests run against the REAL registry — a
 * fake one would reintroduce the second authority this issue deletes (same
 * discipline as grokEffort.enforcement.test.ts).
 *
 * The load-bearing case is the SDK-dispatch repro from the #576 review: a
 * NIGHTGAUGE_CODEX_REASONING_EFFORT of `xhigh` against a model whose ladder
 * tops out at `high` (gpt-5.4) used to pass the static vocabulary filter and
 * reach the codex CLI with the registry never asked — the same silent
 * pass-through class as #532. It must throw a classified AdapterError BEFORE
 * spawn.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { codexReasoningEffortFlag } from "../codexEffort.js";
import { AdapterError } from "../errors.js";

describe("codexReasoningEffortFlag — registry enforcement (#569)", () => {
  beforeEach(() => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    delete process.env.NIGHTGAUGE_CODEX_MODEL;
  });

  afterEach(() => {
    vi.restoreAllMocks();
    delete process.env.NIGHTGAUGE_CODEX_MODEL;
  });

  it("rejects xhigh against gpt-5.4 (tops out at high) — the SDK-dispatch repro", () => {
    // gpt-5.4 declares supported_efforts [low, medium, high].
    let thrown: unknown;
    try {
      codexReasoningEffortFlag("xhigh", "gpt-5.4");
    } catch (error) {
      thrown = error;
    }
    expect(thrown).toBeInstanceOf(AdapterError);
    const err = thrown as AdapterError;
    // Classified, and naming model, requested effort, and the declared ladder.
    expect(err.category).toBe("CONFIG_INVALID");
    expect(err.message).toContain("xhigh");
    expect(err.message).toContain("gpt-5.4");
    expect(err.message).toContain("low, medium, high");
  });

  it("defaults the model to the env resolution the adapter dispatches with", () => {
    process.env.NIGHTGAUGE_CODEX_MODEL = "gpt-5.4";
    expect(() => codexReasoningEffortFlag("xhigh")).toThrow(AdapterError);
    expect(() => codexReasoningEffortFlag("high")).not.toThrow();
  });

  it("rejects max above every current openai ladder (gpt-5.6-sol tops out at xhigh)", () => {
    expect(() => codexReasoningEffortFlag("max", "gpt-5.6-sol")).toThrow(
      /supports: low, medium, high, xhigh/
    );
  });

  it("accepts every declared rung, with the codex-native `none` rung normalized to low", () => {
    // gpt-5.6-sol declares [low, medium, high, xhigh]; none → low.
    for (const effort of ["none", "low", "medium", "high", "xhigh"] as const) {
      expect(codexReasoningEffortFlag(effort, "gpt-5.6-sol")).toBe(effort);
    }
  });

  it("resolves band names to the served model before enforcing", () => {
    // sonnet resolves to gpt-5.6-terra for the openai provider, which declares xhigh.
    expect(codexReasoningEffortFlag("xhigh", "sonnet")).toBe("xhigh");
    expect(() => codexReasoningEffortFlag("max", "sonnet")).toThrow(AdapterError);
  });

  it("rejects ANY explicit effort when the model declares no effort axis (#336)", () => {
    // Registry id lookup is provider-agnostic (ids are globally unique), so the
    // one []-ladder id in the registry exercises the no-axis branch here too.
    for (const effort of ["low", "none", "xhigh"] as const) {
      expect(() => codexReasoningEffortFlag(effort, "claude-haiku-4-5-20251001")).toThrow(
        /no effort axis/
      );
    }
  });

  it("passes through with a warning for a model with no registry descriptor (#336)", () => {
    expect(codexReasoningEffortFlag("xhigh", "some-unregistered-model")).toBe("xhigh");
    expect(console.warn).toHaveBeenCalledWith(expect.stringContaining("no"));
    // No model configured at all → same unknown-model pass-through.
    expect(codexReasoningEffortFlag("xhigh")).toBe("xhigh");
  });

  it("still fails fast on non-vocabulary values, now classified", () => {
    let thrown: unknown;
    try {
      codexReasoningEffortFlag("ultra", "gpt-5.4");
    } catch (error) {
      thrown = error;
    }
    expect(thrown).toBeInstanceOf(AdapterError);
    expect((thrown as AdapterError).category).toBe("CONFIG_INVALID");
    expect((thrown as AdapterError).message).toMatch(/Invalid NIGHTGAUGE_CODEX_REASONING_EFFORT/);
  });

  it("no explicit effort → nothing to enforce", () => {
    expect(codexReasoningEffortFlag(undefined, "gpt-5.4")).toBeUndefined();
    expect(codexReasoningEffortFlag("", "gpt-5.4")).toBeUndefined();
    expect(codexReasoningEffortFlag("  ", "gpt-5.4")).toBeUndefined();
  });
});
