/**
 * grokEffort.enforcement.test.ts (#569)
 *
 * The registry's `supported_efforts` is the semantic authority on the Grok
 * `--effort` value; the static GROK_CLI_EFFORTS vocabulary is syntax only.
 * These tests run against the REAL registry — a fake one would reintroduce
 * the second authority this issue deletes (same discipline as the extension's
 * effortAuthority tests for #336).
 *
 * The load-bearing case is the #532-signature repro the AC requires: a
 * provider-global effort env of `xhigh` against a model whose ladder tops out
 * at `high` must throw a classified AdapterError BEFORE spawn — never reach
 * `grok --effort` and die as `unknown effort level 'xhigh'`, exit 1, no work.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { grokCliEffortFlag } from "../grokEffort.js";
import { AdapterError } from "../errors.js";

describe("grokCliEffortFlag — registry enforcement (#569)", () => {
  beforeEach(() => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    delete process.env.NIGHTGAUGE_GROK_MODEL;
    delete process.env.NIGHTGAUGE_MODEL;
  });

  afterEach(() => {
    vi.restoreAllMocks();
    delete process.env.NIGHTGAUGE_GROK_MODEL;
    delete process.env.NIGHTGAUGE_MODEL;
  });

  it("rejects xhigh against grok-4.5 (tops out at high) — the #532 repro", () => {
    // grok-4.5 declares supported_efforts [low, medium, high].
    let thrown: unknown;
    try {
      grokCliEffortFlag("xhigh", "grok-4.5");
    } catch (error) {
      thrown = error;
    }
    expect(thrown).toBeInstanceOf(AdapterError);
    const err = thrown as AdapterError;
    // Classified, and naming model, requested effort, and the declared ladder.
    expect(err.category).toBe("CONFIG_INVALID");
    expect(err.message).toContain("xhigh");
    expect(err.message).toContain("grok-4.5");
    expect(err.message).toContain("low, medium, high");
  });

  it("reads the provider-global env model the adapter dispatches with", () => {
    // The GrokAdapter call site passes only the effort; the model defaults to
    // the same env resolution the adapter uses (NIGHTGAUGE_GROK_MODEL).
    process.env.NIGHTGAUGE_GROK_MODEL = "grok-4.5";
    expect(() => grokCliEffortFlag("xhigh")).toThrow(AdapterError);
    expect(() => grokCliEffortFlag("high")).not.toThrow();
  });

  it("rejects max above every current xai ladder (grok-4.6 tops out at xhigh)", () => {
    expect(() => grokCliEffortFlag("max", "grok-4.6")).toThrow(
      /supports: low, medium, high, xhigh/
    );
  });

  it("accepts every declared rung, with grok-native rungs normalized first (#523)", () => {
    // grok-4.6 declares [low, medium, high, xhigh]; none/minimal → low.
    for (const effort of ["none", "minimal", "low", "medium", "high", "xhigh"] as const) {
      expect(grokCliEffortFlag(effort, "grok-4.6")).toBe(effort);
    }
  });

  it("resolves band names to the served model before enforcing", () => {
    // sonnet resolves to grok-4.6 for the xai provider, which declares xhigh.
    expect(grokCliEffortFlag("xhigh", "sonnet")).toBe("xhigh");
    expect(() => grokCliEffortFlag("max", "sonnet")).toThrow(AdapterError);
  });

  it("rejects ANY explicit effort when the model declares no effort axis (#336)", () => {
    // grok-build-0.1 declares supported_efforts: [] — a positive declaration.
    for (const effort of ["low", "none", "xhigh"] as const) {
      expect(() => grokCliEffortFlag(effort, "grok-build-0.1")).toThrow(/no effort axis/);
    }
  });

  it("passes through with a warning for a model with no registry descriptor (#336)", () => {
    expect(grokCliEffortFlag("xhigh", "some-unregistered-model")).toBe("xhigh");
    expect(console.warn).toHaveBeenCalledWith(expect.stringContaining("no"));
    // No model configured at all → same unknown-model pass-through.
    expect(grokCliEffortFlag("xhigh")).toBe("xhigh");
  });

  it("still drops non-vocabulary values (syntax filter), now with a warning", () => {
    expect(grokCliEffortFlag("banana", "grok-4.5")).toBeUndefined();
    expect(console.warn).toHaveBeenCalledWith(expect.stringContaining("banana"));
  });

  it("no explicit effort → nothing to enforce", () => {
    expect(grokCliEffortFlag(undefined, "grok-4.5")).toBeUndefined();
    expect(grokCliEffortFlag("", "grok-4.5")).toBeUndefined();
  });
});
