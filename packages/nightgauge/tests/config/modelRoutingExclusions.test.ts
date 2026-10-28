/**
 * `model_routing.excluded_models` / `excluded_efforts` (#2063): unknown values
 * fail with the valid list, and a pin naming an excluded value is an error
 * naming the key. Go pair: internal/config/model_routing_exclusions_test.go.
 */

import { describe, it, expect } from "vitest";
import {
  ModelRoutingConfigSchema,
  exclusionPinConflicts,
  validateConfig,
} from "../../src/config/schema";

describe("model_routing exclusions schema (#2063)", () => {
  it("accepts registry ids and effort levels; empty lists pass", () => {
    expect(
      ModelRoutingConfigSchema.safeParse({
        excluded_models: ["claude-fable-5-1"],
        excluded_efforts: ["xhigh", "max"],
      }).success
    ).toBe(true);
    expect(ModelRoutingConfigSchema.safeParse({ excluded_models: [] }).success).toBe(true);
  });

  it("rejects a typo id and lists valid ids", () => {
    const r = ModelRoutingConfigSchema.safeParse({ excluded_models: ["claude-fabel-5-1"] });
    expect(r.success).toBe(false);
    const msg = JSON.stringify(r.error?.issues);
    expect(msg).toContain("Unknown model id");
    expect(msg).toContain("claude-fable-5-1");
  });

  it("rejects an unknown effort and lists the ladder", () => {
    const r = ModelRoutingConfigSchema.safeParse({ excluded_efforts: ["extreme"] });
    expect(r.success).toBe(false);
    expect(JSON.stringify(r.error?.issues)).toContain("low, medium, high, xhigh, max");
  });

  it("flags a stage_models band pin that resolves to an excluded id", () => {
    const conflicts = exclusionPinConflicts({
      pipeline: { stage_models: { "feature-dev": "fable", "pr-create": "haiku" } },
      model_routing: { excluded_models: ["claude-fable-5-1"] },
    });
    expect(conflicts).toHaveLength(1);
    expect(conflicts[0].path).toEqual(["pipeline", "stage_models", "feature-dev"]);
    expect(conflicts[0].message).toContain("claude-fable-5-1");
  });

  it("flags a stage_efforts pin naming an excluded effort", () => {
    const conflicts = exclusionPinConflicts({
      model_routing: { excluded_efforts: ["max"], stage_efforts: { "feature-dev": "max" } },
    });
    expect(conflicts.map((c) => c.path.join("."))).toEqual([
      "model_routing.stage_efforts.feature-dev",
    ]);
  });

  it("validateConfig surfaces the pin conflict as an error", () => {
    const r = validateConfig({
      pipeline: { stage_models: { "feature-dev": "fable" } },
      model_routing: { excluded_models: ["claude-fable-5-1"] },
    });
    expect(r.valid).toBe(false);
    expect(JSON.stringify(r.errors)).toContain("pipeline.stage_models.feature-dev");
  });
});
