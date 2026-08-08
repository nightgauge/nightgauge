/**
 * The model registry is the single authority on the effort axis (#336).
 *
 * "Haiku has no effort axis" used to be encoded twice: correctly in a
 * hardcoded `EFFORT_SUPPORTING_MODELS = {sonnet, opus, fable}` set that
 * actually gated the `--effort` flag, and wrongly in the registry, which
 * declared `supported_efforts: ["low","medium","high"]` for Haiku. The
 * registry copy was dead data, so nothing failed when it rotted.
 *
 * These tests run against the REAL registry — no hand-authored parallel
 * fixture, because a fake registry would reintroduce exactly the second copy
 * this issue deletes. `modelSupportsEffort("haiku")` is therefore a direct
 * assertion about `model-registry.json`: it fails while Haiku declares levels
 * it does not have.
 *
 * @see Issue #336 - registry becomes the authority for the effort axis
 * @see Issue #1235 - per-model effort level configuration (the deleted set)
 */

import { describe, it, expect } from "vitest";
import { getModelDescriptor } from "@nightgauge/sdk";
import {
  assertEffortSupported,
  modelSupportsEffort,
  supportedEffortsFor,
} from "../../../src/utils/resolvers/stageResolver";

/** A user-configured local model — the registry has no entry by design (#56). */
const LOCAL_MODEL = "qwen3-coder:32b";

describe("effort axis — the registry declares it (#336)", () => {
  it("haiku declares an EMPTY effort axis, not a set of levels", () => {
    // The load-bearing data assertion. `[]` is a positive declaration ("this
    // model has no effort axis"), distinct from the field being absent.
    const haiku = getModelDescriptor("haiku");
    expect(haiku?.id).toBe("claude-haiku-4-5-20251001");
    expect(haiku?.supported_efforts).toEqual([]);
  });

  it("the effort-bearing bands declare non-empty ladders", () => {
    for (const band of ["sonnet", "opus", "fable"] as const) {
      expect(supportedEffortsFor(band)?.length).toBeGreaterThan(0);
    }
  });

  it("resolves through a concrete id as readily as through a band", () => {
    expect(supportedEffortsFor("claude-haiku-4-5-20251001")).toEqual([]);
    expect(supportedEffortsFor("claude-sonnet-5")).toEqual(supportedEffortsFor("sonnet"));
  });

  it("returns undefined — not [] — for a model the registry does not know", () => {
    // The two states must stay distinguishable: `[]` is "declared, no axis",
    // `undefined` is "nothing is known". The consumers below fail in OPPOSITE
    // directions on them, so collapsing them would silently break one.
    expect(supportedEffortsFor(LOCAL_MODEL)).toBeUndefined();
  });
});

describe("modelSupportsEffort — emission gate, fails CLOSED (#336)", () => {
  // Behavior preservation: every member of the deleted EFFORT_SUPPORTING_MODELS
  // set, plus the band it deliberately excluded, must decide exactly as before.
  it.each([
    ["sonnet", true],
    ["opus", true],
    ["fable", true],
    ["haiku", false],
  ] as const)("%s -> %s (same decision the band set made)", (band, expected) => {
    expect(modelSupportsEffort(band)).toBe(expected);
  });

  it("suppresses --effort for haiku because the REGISTRY says so", () => {
    // This is the test that fails on a tree where the gate is registry-routed
    // but haiku still declares ["low","medium","high"].
    expect(modelSupportsEffort("haiku")).toBe(false);
    expect(modelSupportsEffort("claude-haiku-4-5-20251001")).toBe(false);
  });

  it("emits no effort for a model with no registry entry", () => {
    // Fail closed. Handing `--effort` to an unknown provider risks an outright
    // rejection, and the deleted band set never contained local models either.
    expect(modelSupportsEffort(LOCAL_MODEL)).toBe(false);
    expect(modelSupportsEffort("")).toBe(false);
  });
});

describe("assertEffortSupported — level validation, fails OPEN (#336)", () => {
  it("skips validation when the registry declares no effort axis", () => {
    // Opposite fail direction from the emission gate, on the same input: a
    // stage must never be blocked because the metadata is missing or empty.
    expect(() =>
      assertEffortSupported("max", "claude-haiku-4-5-20251001", supportedEffortsFor("haiku"))
    ).not.toThrow();
  });

  it("skips validation for a model with no registry entry", () => {
    expect(() =>
      assertEffortSupported("max", LOCAL_MODEL, supportedEffortsFor(LOCAL_MODEL))
    ).not.toThrow();
  });

  it("still rejects a level a registered model does not declare", () => {
    expect(() => assertEffortSupported("max", "claude-opus-4-8", ["low", "medium"])).toThrow(
      /not supported/
    );
  });

  it("accepts every level the current opus band actually declares", () => {
    for (const level of supportedEffortsFor("opus") ?? []) {
      expect(() => assertEffortSupported(level, "opus", supportedEffortsFor("opus"))).not.toThrow();
    }
  });
});
