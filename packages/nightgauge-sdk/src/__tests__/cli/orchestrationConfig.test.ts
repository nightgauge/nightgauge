/**
 * Tests for the orchestration config knobs (#3901).
 *
 * Proves orchestration is off by default, that an unset knob resolves to a
 * documented default (never `undefined`), that the `CLAUDE_CODE_DISABLE_WORKFLOWS`
 * kill-switch forces `disabled`, and that the resolved value composes onto the
 * `WorkflowSpec`-facing fields. Also covers env-var loading via the SDK CLI
 * config and the non-negative cap validation.
 */

import { describe, it, expect } from "vitest";
import {
  DEFAULT_ORCHESTRATION_CONFIG,
  DISABLE_WORKFLOWS_ENV,
  resolveOrchestrationConfig,
  prefersNativeOffload,
  type OrchestrationConfig,
} from "../../cli/workflow/index.js";
import { loadConfigFromEnv, validateConfig, ConfigValidationError } from "../../cli/config.js";

const NO_ENV: NodeJS.ProcessEnv = {};

describe("resolveOrchestrationConfig", () => {
  it("is off by default: an undefined config resolves to the documented defaults", () => {
    const resolved = resolveOrchestrationConfig(undefined, NO_ENV);
    expect(resolved).toEqual(DEFAULT_ORCHESTRATION_CONFIG);
    expect(resolved.disabled).toBe(true);
    expect(resolved.max_usd).toBe(0);
    expect(resolved.max_agents).toBe(0);
    expect(resolved.max_concurrency).toBe(0);
    expect(resolved.prefer_native_offload).toEqual({});
  });

  it("never surfaces undefined for an unset knob", () => {
    const resolved = resolveOrchestrationConfig({ disabled: false }, NO_ENV);
    expect(resolved.max_usd).toBeTypeOf("number");
    expect(resolved.max_agents).toBeTypeOf("number");
    expect(resolved.max_concurrency).toBeTypeOf("number");
    expect(resolved.prefer_native_offload).toBeTypeOf("object");
  });

  it("surfaces explicitly-set knobs", () => {
    const config: OrchestrationConfig = {
      disabled: false,
      max_usd: 12.5,
      max_agents: 100,
      max_concurrency: 8,
      prefer_native_offload: { "feature-dev": true },
    };
    const resolved = resolveOrchestrationConfig(config, NO_ENV);
    expect(resolved.disabled).toBe(false);
    expect(resolved.max_usd).toBe(12.5);
    expect(resolved.max_agents).toBe(100);
    expect(resolved.max_concurrency).toBe(8);
    expect(resolved.prefer_native_offload).toEqual({ "feature-dev": true });
  });

  it("clamps negative / non-finite caps back to the uncapped default", () => {
    const resolved = resolveOrchestrationConfig(
      { max_usd: -5, max_agents: Number.NaN, max_concurrency: -1 },
      NO_ENV
    );
    expect(resolved.max_usd).toBe(0);
    expect(resolved.max_agents).toBe(0);
    expect(resolved.max_concurrency).toBe(0);
  });

  it("honors the CLAUDE_CODE_DISABLE_WORKFLOWS kill-switch even when config enables", () => {
    const resolved = resolveOrchestrationConfig(
      { disabled: false },
      { [DISABLE_WORKFLOWS_ENV]: "1" }
    );
    expect(resolved.disabled).toBe(true);
  });

  it("treats a non-truthy kill-switch value as not-disabling", () => {
    const resolved = resolveOrchestrationConfig(
      { disabled: false },
      { [DISABLE_WORKFLOWS_ENV]: "false" }
    );
    expect(resolved.disabled).toBe(false);
  });
});

describe("prefersNativeOffload", () => {
  it("returns the per-stage flag when enabled", () => {
    const resolved = resolveOrchestrationConfig(
      { disabled: false, prefer_native_offload: { "feature-dev": true } },
      NO_ENV
    );
    expect(prefersNativeOffload(resolved, "feature-dev")).toBe(true);
    expect(prefersNativeOffload(resolved, "feature-validate")).toBe(false);
  });

  it("is always false while orchestration is disabled", () => {
    const resolved = resolveOrchestrationConfig(
      { disabled: true, prefer_native_offload: { "feature-dev": true } },
      NO_ENV
    );
    expect(prefersNativeOffload(resolved, "feature-dev")).toBe(false);
  });
});

describe("loadConfigFromEnv — orchestration", () => {
  it("defaults to an empty (off) orchestration block", () => {
    const config = loadConfigFromEnv({ ANTHROPIC_API_KEY: "k" });
    expect(resolveOrchestrationConfig(config.orchestration, NO_ENV)).toEqual(
      DEFAULT_ORCHESTRATION_CONFIG
    );
  });

  it("reads orchestration knobs from environment variables", () => {
    const config = loadConfigFromEnv({
      ANTHROPIC_API_KEY: "k",
      NIGHTGAUGE_ORCHESTRATION_DISABLED: "false",
      NIGHTGAUGE_ORCHESTRATION_MAX_USD: "25",
      NIGHTGAUGE_ORCHESTRATION_MAX_AGENTS: "64",
      NIGHTGAUGE_ORCHESTRATION_MAX_CONCURRENCY: "6",
    });
    expect(config.orchestration).toMatchObject({
      disabled: false,
      max_usd: 25,
      max_agents: 64,
      max_concurrency: 6,
    });
  });

  it("force-disables via CLAUDE_CODE_DISABLE_WORKFLOWS", () => {
    const config = loadConfigFromEnv({
      ANTHROPIC_API_KEY: "k",
      [DISABLE_WORKFLOWS_ENV]: "true",
    });
    expect(config.orchestration?.disabled).toBe(true);
  });
});

describe("validateConfig — orchestration caps", () => {
  function baseConfig(orchestration: OrchestrationConfig) {
    return {
      ...loadConfigFromEnv({ ANTHROPIC_API_KEY: "k" }),
      orchestration,
    };
  }

  it("accepts zero (uncapped) and positive caps", () => {
    expect(() =>
      validateConfig(baseConfig({ max_usd: 0, max_agents: 10, max_concurrency: 4 }), {
        ANTHROPIC_API_KEY: "k",
      })
    ).not.toThrow();
  });

  it("rejects a negative budget cap", () => {
    expect(() => validateConfig(baseConfig({ max_usd: -1 }), { ANTHROPIC_API_KEY: "k" })).toThrow(
      ConfigValidationError
    );
  });

  it("rejects a negative agent cap", () => {
    expect(() =>
      validateConfig(baseConfig({ max_agents: -3 }), { ANTHROPIC_API_KEY: "k" })
    ).toThrow(ConfigValidationError);
  });
});
