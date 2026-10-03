/**
 * `pipeline.performance_mode.default` was documented as "the mode applied when
 * no state file is present", but neither resolver read it (#2343): the mode is
 * NIGHTGAUGE_PERFORMANCE_MODE, else the checkout's performance-mode.yaml, else
 * `elevated`. The key is removed, and the schema rejects it rather than
 * silently dropping a setting that never took effect.
 *
 * @see docs/PERFORMANCE_MODES.md
 */

import { describe, it, expect } from "vitest";
import { validateConfig } from "../../src/config/schema";

describe("pipeline.performance_mode schema (#2343)", () => {
  it("rejects the removed `default` key", () => {
    const result = validateConfig({ pipeline: { performance_mode: { default: "efficiency" } } });

    expect(result.valid).toBe(false);
    const error = result.errors.find((e) => e.field === "pipeline.performance_mode");
    expect(error?.message).toContain("default");
  });

  it("still accepts the maximum-profile overrides", () => {
    const result = validateConfig({
      pipeline: { performance_mode: { overrides: { maximum: { model: "opus" } } } },
    });

    expect(result.errors).toEqual([]);
    expect(result.valid).toBe(true);
  });
});
