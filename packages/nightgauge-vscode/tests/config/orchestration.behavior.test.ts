/**
 * Behavior tests for orchestration.* configuration fields (#3901).
 *
 * Verifies the orchestration config schema accepts the documented shape, ships
 * the off-by-default baseline in DEFAULT_CONFIG, parses cleanly under the full
 * IncrediConfigSchema, and rejects invalid caps / single-agent offload stages.
 * Mirrors the SDK OrchestrationConfig contract.
 *
 * @see Issue #3901 - Orchestration config knobs
 * @see packages/nightgauge-vscode/src/config/schema.ts - OrchestrationConfigSchema
 * @see docs/WORKFLOW_ORCHESTRATION.md § Configuration knobs
 */

import { describe, it, expect } from "vitest";
import {
  OrchestrationConfigSchema,
  IncrediConfigSchema,
  DEFAULT_CONFIG,
} from "../../src/config/schema";

describe("OrchestrationConfigSchema", () => {
  it("accepts a fully-specified orchestration block", () => {
    const parsed = OrchestrationConfigSchema.parse({
      disabled: false,
      prefer_native_offload: { "feature-dev": true, "feature-validate": false },
      max_usd: 12.5,
      max_agents: 100,
      max_concurrency: 8,
    });
    expect(parsed.disabled).toBe(false);
    expect(parsed.prefer_native_offload).toEqual({
      "feature-dev": true,
      "feature-validate": false,
    });
    expect(parsed.max_usd).toBe(12.5);
    expect(parsed.max_agents).toBe(100);
    expect(parsed.max_concurrency).toBe(8);
  });

  it("accepts an empty block (all knobs optional, off-by-default at read time)", () => {
    expect(() => OrchestrationConfigSchema.parse({})).not.toThrow();
  });

  it("rejects a negative budget cap", () => {
    expect(() => OrchestrationConfigSchema.parse({ max_usd: -1 })).toThrow();
  });

  it("rejects negative agent / concurrency caps", () => {
    expect(() => OrchestrationConfigSchema.parse({ max_agents: -1 })).toThrow();
    expect(() => OrchestrationConfigSchema.parse({ max_concurrency: -1 })).toThrow();
  });

  it("rejects a non-integer agent cap", () => {
    expect(() => OrchestrationConfigSchema.parse({ max_agents: 1.5 })).toThrow();
  });

  it("strips pr-create / pr-merge offload flags (single-agent deterministic phases)", () => {
    const parsed = OrchestrationConfigSchema.parse({
      prefer_native_offload: { "pr-create": true, "feature-dev": true },
    });
    expect(parsed.prefer_native_offload).not.toHaveProperty("pr-create");
    expect(parsed.prefer_native_offload).toHaveProperty("feature-dev", true);
  });
});

describe("DEFAULT_CONFIG.orchestration", () => {
  it("is off by default with no caps", () => {
    expect(DEFAULT_CONFIG.orchestration).toEqual({
      disabled: true,
      prefer_native_offload: {},
      max_usd: 0,
      max_agents: 0,
      max_concurrency: 0,
    });
  });

  it("is a valid orchestration block under the full config schema", () => {
    expect(() =>
      IncrediConfigSchema.parse({ orchestration: DEFAULT_CONFIG.orchestration })
    ).not.toThrow();
  });
});
