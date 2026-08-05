/**
 * Model `behavior` block — schema, registry data, and typed accessors (#77).
 *
 * The behavior block is the FACT layer ADR 016 overlays read instead of
 * restating. A wrong value here does not fail loudly; it propagates into every
 * rendered skill as confidently-worded prose, so these assert the shipped data
 * and not just the shape.
 */

import { describe, expect, it } from "vitest";
import {
  BehaviorSchema,
  PropensitySchema,
  THINKING_DISABLE_NEVER,
  ThinkingDisableLimitSchema,
} from "../../eval/modelEvalSchemas.js";
import {
  getModelBehavior,
  getModelDescriptor,
  getModelPropensity,
  thinkingDisableConflict,
} from "../../eval/modelRegistry.js";

describe("BehaviorSchema", () => {
  it("accepts an absent block — a model without behavior must load as before", () => {
    // The whole block is optional; this is the fail-open contract for every
    // pre-#77 entry and for providers we document nothing about.
    const descriptor = getModelDescriptor("gpt-5.5", "openai");
    expect(descriptor).toBeDefined();
    expect(descriptor?.behavior).toBeUndefined();
  });

  it("accepts an empty block and rejects unknown keys", () => {
    expect(BehaviorSchema.safeParse({}).success).toBe(true);
    // .strict() is load-bearing: a typo'd fact would otherwise parse clean and
    // read as "undeclared" forever.
    expect(BehaviorSchema.safeParse({ thinking_defualt: "on" }).success).toBe(false);
  });

  it("rejects a propensity level outside low|normal|high", () => {
    expect(PropensitySchema.safeParse({ verification: "very-high" }).success).toBe(false);
    expect(PropensitySchema.safeParse({ verification: "high" }).success).toBe(true);
  });

  it("accepts `never` as a thinking-disable ceiling but not as an effort", () => {
    // `never` deliberately lives in the limit union only. If it were added to
    // EffortLevelSchema it would become spellable in supported_efforts and in
    // stage effort config, where it is meaningless.
    expect(ThinkingDisableLimitSchema.safeParse(THINKING_DISABLE_NEVER).success).toBe(true);
    expect(ThinkingDisableLimitSchema.safeParse("high").success).toBe(true);
    expect(ThinkingDisableLimitSchema.safeParse("nope").success).toBe(false);
    expect(BehaviorSchema.safeParse({ effort_default: THINKING_DISABLE_NEVER }).success).toBe(
      false
    );
  });
});

describe("shipped behavior facts", () => {
  it.each([
    ["claude-opus-5", "on", "high", "high", 128_000],
    ["claude-sonnet-5", "on", undefined, "high", 128_000],
    ["claude-fable-5", "on", THINKING_DISABLE_NEVER, "high", 128_000],
    ["claude-haiku-4-5-20251001", "off", undefined, undefined, 64_000],
  ])("%s declares its documented runtime facts", (id, thinking, ceiling, effort, maxOut) => {
    const behavior = getModelBehavior(id as string);
    expect(behavior).toBeDefined();
    expect(behavior?.thinking_default).toBe(thinking);
    expect(behavior?.thinking_disable_max_effort).toBe(ceiling);
    expect(behavior?.effort_default).toBe(effort);
    expect(behavior?.max_output_tokens).toBe(maxOut);
  });
});

describe("thinkingDisableConflict", () => {
  it("flags fable at EVERY effort — the case an omitted ceiling got backwards", () => {
    // Pre-#77 the only way to describe fable was to omit the field, which means
    // "unconstrained" — so a thinking-disabled config sailed through the gate
    // and 400'd at the provider. Asserting every rung catches an implementation
    // that only guards the top of the ladder (the Opus 5 shape).
    for (const effort of ["low", "medium", "high", "xhigh", "max"] as const) {
      const got = thinkingDisableConflict("claude-fable-5", effort);
      expect(got.conflict, `fable at ${effort} must conflict`).toBe(true);
      expect(got.limit).toBe(THINKING_DISABLE_NEVER);
    }
  });

  it("keeps opus-5 bounded at high rather than refusing outright", () => {
    expect(thinkingDisableConflict("claude-opus-5", "high").conflict).toBe(false);
    expect(thinkingDisableConflict("claude-opus-5", "xhigh").conflict).toBe(true);
    expect(thinkingDisableConflict("claude-opus-5", "max").conflict).toBe(true);
  });

  it("never conflicts for a model with no declared constraint", () => {
    expect(thinkingDisableConflict("claude-sonnet-5", "max").conflict).toBe(false);
    // Unknown / local ids have no entry at all: local runs must not be blocked.
    expect(thinkingDisableConflict("llama-3-70b-local", "max").conflict).toBe(false);
  });
});

describe("getModelPropensity", () => {
  it("reads opus-5's three documented high propensities", () => {
    expect(getModelPropensity("claude-opus-5")).toEqual({
      verification: "high",
      delegation: "high",
      narration: "high",
    });
  });

  it("exposes the opus-4-8 -> opus-5 delegation inversion", () => {
    // This inversion is the reason the axis exists: 4.8 under-reaches for
    // subagents, 5 over-reaches. An overlay keyed the wrong way round is worse
    // than no overlay, so the data has to carry the direction.
    expect(getModelPropensity("claude-opus-4-8").delegation).toBe("low");
    expect(getModelPropensity("claude-opus-5").delegation).toBe("high");
  });

  it("fills `normal` for every undeclared axis, including unknown models", () => {
    // Total by construction — an overlay asking about any axis gets a usable
    // answer for any model, and the neutral default changes nothing.
    expect(getModelPropensity("claude-sonnet-5").delegation).toBe("normal");
    expect(getModelPropensity("claude-haiku-4-5-20251001")).toEqual({
      verification: "normal",
      delegation: "normal",
      narration: "normal",
    });
    expect(getModelPropensity("llama-3-70b-local")).toEqual({
      verification: "normal",
      delegation: "normal",
      narration: "normal",
    });
  });
});
