/**
 * configMergeEngine.runtime.test.ts
 *
 * Tests the runtime tier inserted between local and env in the 7-tier
 * precedence chain (Issue #3335, Phase 2 of epic #3313).
 *
 * Covers:
 *   - precedence: runtime wins over default/global/project/local; env/cli still win
 *   - source map records 'runtime' for runtime-sourced keys
 *   - wasOverridden treats runtime as higher than local
 *   - getPathsFromSource('runtime') returns runtime-sourced paths
 *   - schema validation passes when runtime supplies a valid value
 *   - tier metadata: hasRuntime is set correctly
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  mergeConfigs,
  wasOverridden,
  getPathsFromSource,
  type ConfigTiers,
} from "../../src/config/configMergeEngine";
import { DEFAULT_CONFIG, type IncrediConfig } from "../../src/config/schema";

describe("configMergeEngine — runtime tier", () => {
  const originalEnv = { ...process.env };

  beforeEach(() => {
    Object.keys(process.env).forEach((key) => {
      if (key.startsWith("NIGHTGAUGE_")) {
        delete process.env[key];
      }
    });
  });

  afterEach(() => {
    process.env = { ...originalEnv };
  });

  // ── precedence ──────────────────────────────────────────────────────────────

  it("runtime overrides default", () => {
    const tiers: ConfigTiers = {
      defaults: DEFAULT_CONFIG,
      runtime: { pipeline: { max_concurrent: 7 } } as Partial<IncrediConfig>,
    };
    const result = mergeConfigs(tiers, { skipEnvResolution: true });
    expect(result.config.pipeline?.max_concurrent).toBe(7);
    expect(result.sources["pipeline.max_concurrent"]).toBe("runtime");
  });

  it("runtime overrides global", () => {
    const tiers: ConfigTiers = {
      defaults: DEFAULT_CONFIG,
      global: { pipeline: { max_concurrent: 2 } } as Partial<IncrediConfig>,
      runtime: { pipeline: { max_concurrent: 4 } } as Partial<IncrediConfig>,
    };
    const result = mergeConfigs(tiers, { skipEnvResolution: true });
    expect(result.config.pipeline?.max_concurrent).toBe(4);
    expect(result.sources["pipeline.max_concurrent"]).toBe("runtime");
  });

  it("runtime overrides project", () => {
    const tiers: ConfigTiers = {
      defaults: DEFAULT_CONFIG,
      project: { project: { number: 1 } } as Partial<IncrediConfig>,
      runtime: { project: { number: 2 } } as Partial<IncrediConfig>,
    };
    const result = mergeConfigs(tiers, { skipEnvResolution: true });
    expect(result.config.project?.number).toBe(2);
    expect(result.sources["project.number"]).toBe("runtime");
  });

  it("runtime overrides local", () => {
    const tiers: ConfigTiers = {
      defaults: DEFAULT_CONFIG,
      local: { pipeline: { auto_fix: true } } as Partial<IncrediConfig>,
      runtime: { pipeline: { auto_fix: false } } as Partial<IncrediConfig>,
    };
    const result = mergeConfigs(tiers, { skipEnvResolution: true });
    expect(result.config.pipeline?.auto_fix).toBe(false);
    expect(result.sources["pipeline.auto_fix"]).toBe("runtime");
  });

  it("env overrides runtime", () => {
    const tiers: ConfigTiers = {
      defaults: DEFAULT_CONFIG,
      runtime: { pipeline: { max_concurrent: 4 } } as Partial<IncrediConfig>,
      env: { pipeline: { max_concurrent: 8 } } as Partial<IncrediConfig>,
    };
    const result = mergeConfigs(tiers, { skipEnvResolution: true });
    expect(result.config.pipeline?.max_concurrent).toBe(8);
    expect(result.sources["pipeline.max_concurrent"]).toBe("env");
  });

  it("cli overrides runtime", () => {
    const tiers: ConfigTiers = {
      defaults: DEFAULT_CONFIG,
      runtime: { pipeline: { max_concurrent: 4 } } as Partial<IncrediConfig>,
      cli: { pipeline: { max_concurrent: 16 } } as Partial<IncrediConfig>,
    };
    const result = mergeConfigs(tiers, { skipEnvResolution: true });
    expect(result.config.pipeline?.max_concurrent).toBe(16);
    expect(result.sources["pipeline.max_concurrent"]).toBe("cli");
  });

  it("preserves the full precedence chain end-to-end", () => {
    const tiers: ConfigTiers = {
      defaults: DEFAULT_CONFIG,
      global: { pipeline: { max_concurrent: 1 } } as Partial<IncrediConfig>,
      project: { pipeline: { max_concurrent: 2 } } as Partial<IncrediConfig>,
      local: { pipeline: { max_concurrent: 3 } } as Partial<IncrediConfig>,
      runtime: { pipeline: { max_concurrent: 4 } } as Partial<IncrediConfig>,
      env: { pipeline: { max_concurrent: 5 } } as Partial<IncrediConfig>,
      cli: { pipeline: { max_concurrent: 6 } } as Partial<IncrediConfig>,
    };
    const result = mergeConfigs(tiers, { skipEnvResolution: true });
    expect(result.config.pipeline?.max_concurrent).toBe(6);
    expect(result.sources["pipeline.max_concurrent"]).toBe("cli");
  });

  // ── tier metadata ───────────────────────────────────────────────────────────

  it("sets hasRuntime=true when a non-empty runtime tier is supplied", () => {
    const tiers: ConfigTiers = {
      defaults: DEFAULT_CONFIG,
      runtime: { pipeline: { max_concurrent: 4 } } as Partial<IncrediConfig>,
    };
    const result = mergeConfigs(tiers, { skipEnvResolution: true });
    expect(result.tiers.hasRuntime).toBe(true);
  });

  it("sets hasRuntime=false when runtime tier is undefined or empty", () => {
    const noRuntime = mergeConfigs({ defaults: DEFAULT_CONFIG }, { skipEnvResolution: true });
    expect(noRuntime.tiers.hasRuntime).toBe(false);

    const emptyRuntime = mergeConfigs(
      { defaults: DEFAULT_CONFIG, runtime: {} },
      { skipEnvResolution: true }
    );
    expect(emptyRuntime.tiers.hasRuntime).toBe(false);
  });

  // ── wasOverridden ──────────────────────────────────────────────────────────

  it("wasOverridden(local) returns true when runtime supplied the value", () => {
    const tiers: ConfigTiers = {
      defaults: DEFAULT_CONFIG,
      local: { pipeline: { max_concurrent: 2 } } as Partial<IncrediConfig>,
      runtime: { pipeline: { max_concurrent: 4 } } as Partial<IncrediConfig>,
    };
    const result = mergeConfigs(tiers, { skipEnvResolution: true });
    expect(wasOverridden(result, "pipeline.max_concurrent", "local")).toBe(true);
  });

  it("wasOverridden(runtime) returns true when env or cli supplied the value", () => {
    const tiers: ConfigTiers = {
      defaults: DEFAULT_CONFIG,
      runtime: { pipeline: { max_concurrent: 4 } } as Partial<IncrediConfig>,
      env: { pipeline: { max_concurrent: 8 } } as Partial<IncrediConfig>,
    };
    const result = mergeConfigs(tiers, { skipEnvResolution: true });
    expect(wasOverridden(result, "pipeline.max_concurrent", "runtime")).toBe(true);
  });

  it("wasOverridden(runtime) returns false when only lower tiers supplied the value", () => {
    const tiers: ConfigTiers = {
      defaults: DEFAULT_CONFIG,
      local: { pipeline: { max_concurrent: 2 } } as Partial<IncrediConfig>,
    };
    const result = mergeConfigs(tiers, { skipEnvResolution: true });
    expect(wasOverridden(result, "pipeline.max_concurrent", "runtime")).toBe(false);
  });

  // ── getPathsFromSource ─────────────────────────────────────────────────────

  it("getPathsFromSource('runtime') returns paths supplied by the runtime tier", () => {
    const tiers: ConfigTiers = {
      defaults: DEFAULT_CONFIG,
      runtime: {
        pipeline: { max_concurrent: 4, auto_fix: false },
        project: { number: 99 },
      } as Partial<IncrediConfig>,
    };
    const result = mergeConfigs(tiers, { skipEnvResolution: true });
    const runtimePaths = getPathsFromSource(result, "runtime").sort();
    expect(runtimePaths).toContain("pipeline.max_concurrent");
    expect(runtimePaths).toContain("pipeline.auto_fix");
    expect(runtimePaths).toContain("project.number");
  });

  // ── schema validation ──────────────────────────────────────────────────────

  it("schema validation passes when runtime supplies a valid integer for an integer field", () => {
    const tiers: ConfigTiers = {
      defaults: DEFAULT_CONFIG,
      runtime: { pipeline: { max_concurrent: 4 } } as Partial<IncrediConfig>,
    };
    const result = mergeConfigs(tiers, { skipEnvResolution: true });
    expect(result.validation.valid).toBe(true);
  });

  // ── source-map invariants ──────────────────────────────────────────────────

  it("does not mark default-only paths as runtime", () => {
    const tiers: ConfigTiers = {
      defaults: DEFAULT_CONFIG,
      runtime: { pipeline: { max_concurrent: 4 } } as Partial<IncrediConfig>,
    };
    const result = mergeConfigs(tiers, { skipEnvResolution: true });
    // Some unrelated default path should still be 'default'
    const someDefaultPath = Object.entries(result.sources).find(([, src]) => src === "default");
    expect(someDefaultPath).toBeDefined();
  });
});
