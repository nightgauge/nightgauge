/**
 * modeProfiles.maxModel.test.ts
 *
 * `model_routing.max_model` envelope arithmetic (#1201).
 *
 * This is the layer the knob-agreement suite cannot reach: `AutoModelSelector`
 * is mocked out there, so the frontier-reasoning escalation that dispatches
 * Fable never runs and a "cap turns fable into opus" assertion would pass
 * without the cap existing. Here the envelope is asserted directly.
 *
 * Go mirror: `TestApplyMaxModel` in
 * `internal/intelligence/routing/performance_mode_test.go`. Both sides must
 * agree, or one config file dispatches two tiers depending on who dispatched.
 */

import { describe, it, expect } from "vitest";
import { applyMaxModel, getRoutedTierEnvelope } from "../../src/utils/modeProfiles";
import type { PipelineStage } from "@nightgauge/sdk";

const DEV = "feature-dev" as PipelineStage;
const VALIDATE = "feature-validate" as PipelineStage;

describe("applyMaxModel (#1201)", () => {
  it("lowers a ceiling above the cap", () => {
    expect(applyMaxModel({ floor: "haiku", ceiling: "fable" }, "opus")).toEqual({
      floor: "haiku",
      ceiling: "opus",
    });
  });

  it("is a no-op when the cap equals the ceiling", () => {
    const env = { floor: "haiku", ceiling: "opus" } as const;
    expect(applyMaxModel(env, "opus")).toEqual(env);
  });

  it("never raises a ceiling below the cap", () => {
    const env = { floor: "haiku", ceiling: "sonnet" } as const;
    expect(
      applyMaxModel(env, "fable"),
      "max_model means 'no higher than'; raising would be an escape hatch out of the chosen mode"
    ).toEqual(env);
  });

  it("is a no-op when unset", () => {
    const env = { floor: "haiku", ceiling: "fable" } as const;
    expect(applyMaxModel(env, undefined)).toEqual(env);
  });

  it("leaves the floor alone even when the cap lands below it", () => {
    // A cap that dragged the floor down would WIDEN the range of tiers a stage
    // can route to — the opposite of what a cap is for.
    const capped = applyMaxModel({ floor: "opus", ceiling: "fable" }, "haiku");
    expect(capped.floor).toBe("opus");
    expect(capped.ceiling).toBe("haiku");
  });
});

describe("getRoutedTierEnvelope with a cap (#1201)", () => {
  it("premise: frontier keeps a fable ceiling on feature-dev", () => {
    expect(
      getRoutedTierEnvelope("frontier", DEV).ceiling,
      "if this is not fable, every capped assertion below is vacuous"
    ).toBe("fable");
  });

  it("caps frontier's fable ceiling on feature-dev", () => {
    expect(getRoutedTierEnvelope("frontier", DEV, "opus").ceiling).toBe("opus");
  });

  it("still narrows non-reasoning stages to opus before the cap applies", () => {
    // feature-validate never gets the fable ceiling regardless — #19's rule.
    expect(getRoutedTierEnvelope("frontier", VALIDATE).ceiling).toBe("opus");
    expect(getRoutedTierEnvelope("frontier", VALIDATE, "sonnet").ceiling).toBe("sonnet");
  });

  it("cannot widen efficiency's ceiling", () => {
    expect(getRoutedTierEnvelope("efficiency", DEV, "fable").ceiling).toBe("sonnet");
  });
});
