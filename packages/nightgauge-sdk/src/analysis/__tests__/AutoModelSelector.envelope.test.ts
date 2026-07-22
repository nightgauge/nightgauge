/**
 * AutoModelSelector.envelope.test.ts (Issue #19)
 *
 * Validates the performance-mode ENVELOPE support added to the selector:
 *   - clampTier raises to floor / caps at ceiling.
 *   - the default envelope reproduces pre-envelope routing (Opus ceiling, never Fable).
 *   - an Efficiency band [haiku, sonnet] caps an otherwise-Opus pick at Sonnet.
 *   - a Frontier band [haiku, fable] escalates L/XL planning & dev to Fable,
 *     but NEVER feature-validate (data-driven exclusion) and never trivial work.
 *   - a Maximum-style band [opus, opus] raises a cheap pick to the Opus floor.
 */
import { describe, it, expect } from "vitest";
import {
  AutoModelSelector,
  clampTier,
  DEFAULT_MODEL_ENVELOPE,
  type ModelEnvelope,
} from "../AutoModelSelector.js";

const selector = new AutoModelSelector();
const meta = (size: string) => ({ labels: [`size:${size}`], title: `${size} task` });

const EFFICIENCY: ModelEnvelope = { floor: "haiku", ceiling: "sonnet" };
const FRONTIER: ModelEnvelope = { floor: "haiku", ceiling: "fable" };
const MAXIMUM: ModelEnvelope = { floor: "opus", ceiling: "opus" };

describe("clampTier (Issue #19)", () => {
  it("raises a tier up to the floor", () => {
    expect(clampTier("haiku", { floor: "sonnet", ceiling: "opus" })).toBe("sonnet");
    expect(clampTier("haiku", MAXIMUM)).toBe("opus");
  });
  it("caps a tier at the ceiling", () => {
    expect(clampTier("opus", EFFICIENCY)).toBe("sonnet");
    expect(clampTier("fable", EFFICIENCY)).toBe("sonnet");
    expect(clampTier("fable", { floor: "haiku", ceiling: "opus" })).toBe("opus");
  });
  it("is identity within the band", () => {
    expect(clampTier("sonnet", FRONTIER)).toBe("sonnet");
    expect(clampTier("fable", FRONTIER)).toBe("fable");
  });
});

describe("AutoModelSelector — default envelope preserves pre-envelope routing", () => {
  it("caps at Opus and never returns Fable for a large dev task", () => {
    const r = selector.selectModel(
      "feature-dev",
      meta("XL"),
      undefined,
      undefined,
      DEFAULT_MODEL_ENVELOPE
    );
    expect(r.model).toBe("opus");
  });
  it("matches the no-envelope call (backward compatible)", () => {
    const withDefault = selector.selectModel(
      "feature-dev",
      meta("L"),
      undefined,
      undefined,
      DEFAULT_MODEL_ENVELOPE
    );
    const without = selector.selectModel("feature-dev", meta("L"));
    expect(withDefault.model).toBe(without.model);
  });
});

describe("AutoModelSelector — Efficiency band [haiku, sonnet]", () => {
  it("caps an L dev (matrix Opus) down to Sonnet", () => {
    const r = selector.selectModel("feature-dev", meta("L"), undefined, undefined, EFFICIENCY);
    expect(r.model).toBe("sonnet");
  });
  it("caps an L validate (matrix Opus) down to Sonnet", () => {
    const r = selector.selectModel("feature-validate", meta("L"), undefined, undefined, EFFICIENCY);
    expect(r.model).toBe("sonnet");
  });
});

describe("AutoModelSelector — Frontier band [haiku, fable]", () => {
  it("escalates an L feature-dev to Fable", () => {
    const r = selector.selectModel("feature-dev", meta("L"), undefined, undefined, FRONTIER);
    expect(r.model).toBe("fable");
  });
  it("escalates an XL feature-planning to Fable", () => {
    const r = selector.selectModel("feature-planning", meta("XL"), undefined, undefined, FRONTIER);
    expect(r.model).toBe("fable");
  });
  it("does NOT escalate feature-validate to Fable, even at L (data-driven)", () => {
    const r = selector.selectModel("feature-validate", meta("L"), undefined, undefined, FRONTIER);
    expect(r.model).toBe("opus");
    expect(r.model).not.toBe("fable");
  });
  it("does NOT escalate a trivial (XS) feature-dev to Fable", () => {
    const r = selector.selectModel("feature-dev", meta("XS"), undefined, undefined, FRONTIER);
    expect(r.model).not.toBe("fable");
  });
  it("keeps lightweight pr-create on Haiku (never Fable)", () => {
    const r = selector.selectModel("pr-create", meta("XL"), undefined, undefined, FRONTIER);
    expect(r.model).toBe("haiku");
  });
});

describe("AutoModelSelector — Maximum band [opus, opus]", () => {
  it("raises an XS validate (matrix Haiku) up to the Opus floor", () => {
    const r = selector.selectModel("feature-validate", meta("XS"), undefined, undefined, MAXIMUM);
    expect(r.model).toBe("opus");
  });
});
