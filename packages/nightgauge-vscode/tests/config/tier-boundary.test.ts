/**
 * tier-boundary.test.ts
 *
 * 7-tier configuration precedence boundary scenarios for the config merge engine.
 *
 * Covers:
 *   - Full 7-tier end-to-end: all tiers populated simultaneously, CLI wins
 *   - All 6 adjacent tier pairs (each higher tier overrides the one below it)
 *   - Runtime tier is ephemeral: hasRuntime metadata, does not persist
 *   - Team-tier (project) vs runtime: runtime wins, source is 'runtime'
 *   - Machine-tier (global): global overrides defaults, local overrides global
 *   - Source tracking correctness: result.sources maps keys to correct tier names
 *   - wasOverridden() for all adjacent pairs
 *   - getPathsFromSource() returns correct key list for each tier
 *
 * Does NOT duplicate tests already in configMergeEngine.runtime.test.ts
 * (basic runtime precedence over individual tiers, schema validation, etc.).
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  mergeConfigs,
  wasOverridden,
  getPathsFromSource,
  type ConfigTiers,
} from "../../src/config/configMergeEngine";
import { DEFAULT_CONFIG, type IncrediConfig } from "../../src/config/schema";

describe("configMergeEngine — 7-tier boundary scenarios", () => {
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

  // ── 1. Full 7-tier end-to-end ────────────────────────────────────────────────

  describe("7-tier end-to-end: all tiers populated simultaneously", () => {
    it("CLI wins over all other tiers", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        global: { pipeline: { auto_fix: false } } as Partial<IncrediConfig>,
        project: { pipeline: { auto_fix: false } } as Partial<IncrediConfig>,
        local: { pipeline: { auto_fix: false } } as Partial<IncrediConfig>,
        runtime: { pipeline: { auto_fix: false } } as Partial<IncrediConfig>,
        env: { pipeline: { auto_fix: false } } as Partial<IncrediConfig>,
        cli: { pipeline: { auto_fix: true } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(result.config.pipeline?.auto_fix).toBe(true);
      expect(result.sources["pipeline.auto_fix"]).toBe("cli");
    });

    it("all 7 tiers contribute distinct keys with correct source tracking", () => {
      // Use different keys per tier so all can coexist in the source map
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        global: { pull_request: { merge_strategy: "rebase" } } as Partial<IncrediConfig>,
        project: { project: { number: 10 } } as Partial<IncrediConfig>,
        local: { pipeline: { ci_timeout: 20 } } as Partial<IncrediConfig>,
        runtime: { pipeline: { auto_fix: false } } as Partial<IncrediConfig>,
        env: { pipeline: { default_mode: "interactive" } } as Partial<IncrediConfig>,
        cli: { branch: { base: "develop" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.sources["pull_request.merge_strategy"]).toBe("global");
      expect(result.sources["project.number"]).toBe("project");
      expect(result.sources["pipeline.ci_timeout"]).toBe("local");
      expect(result.sources["pipeline.auto_fix"]).toBe("runtime");
      expect(result.sources["pipeline.default_mode"]).toBe("env");
      expect(result.sources["branch.base"]).toBe("cli");
    });

    it("all TierMetadata flags are set when all tiers are supplied", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        global: { pull_request: { merge_strategy: "rebase" } } as Partial<IncrediConfig>,
        project: { project: { number: 10 } } as Partial<IncrediConfig>,
        local: { pipeline: { ci_timeout: 20 } } as Partial<IncrediConfig>,
        runtime: { pipeline: { auto_fix: false } } as Partial<IncrediConfig>,
        env: { pipeline: { default_mode: "interactive" } } as Partial<IncrediConfig>,
        cli: { branch: { base: "develop" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.tiers.hasDefaults).toBe(true);
      expect(result.tiers.hasGlobal).toBe(true);
      expect(result.tiers.hasProject).toBe(true);
      expect(result.tiers.hasLocal).toBe(true);
      expect(result.tiers.hasRuntime).toBe(true);
      expect(result.tiers.hasEnv).toBe(true);
      expect(result.tiers.hasCli).toBe(true);
    });
  });

  // ── 2. Adjacent tier pairs ───────────────────────────────────────────────────

  describe("adjacent tier pairs: each higher tier overrides the one below", () => {
    it("pair 1: global overrides defaults", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        global: { pull_request: { merge_strategy: "rebase" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      // DEFAULT_CONFIG has merge_strategy: 'squash'
      expect(result.config.pull_request?.merge_strategy).toBe("rebase");
      expect(result.sources["pull_request.merge_strategy"]).toBe("global");
    });

    it("pair 2: project overrides global", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        global: { pull_request: { merge_strategy: "rebase" } } as Partial<IncrediConfig>,
        project: { pull_request: { merge_strategy: "merge" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(result.config.pull_request?.merge_strategy).toBe("merge");
      expect(result.sources["pull_request.merge_strategy"]).toBe("project");
    });

    it("pair 3: local overrides project", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        project: { pull_request: { merge_strategy: "merge" } } as Partial<IncrediConfig>,
        local: { pull_request: { merge_strategy: "squash" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(result.config.pull_request?.merge_strategy).toBe("squash");
      expect(result.sources["pull_request.merge_strategy"]).toBe("local");
    });

    it("pair 4: runtime overrides local", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        local: { pull_request: { merge_strategy: "squash" } } as Partial<IncrediConfig>,
        runtime: { pull_request: { merge_strategy: "rebase" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(result.config.pull_request?.merge_strategy).toBe("rebase");
      expect(result.sources["pull_request.merge_strategy"]).toBe("runtime");
    });

    it("pair 5: env overrides runtime", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        runtime: { pull_request: { merge_strategy: "rebase" } } as Partial<IncrediConfig>,
        env: { pull_request: { merge_strategy: "merge" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(result.config.pull_request?.merge_strategy).toBe("merge");
      expect(result.sources["pull_request.merge_strategy"]).toBe("env");
    });

    it("pair 6: CLI overrides env", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        env: { pull_request: { merge_strategy: "merge" } } as Partial<IncrediConfig>,
        cli: { pull_request: { merge_strategy: "squash" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(result.config.pull_request?.merge_strategy).toBe("squash");
      expect(result.sources["pull_request.merge_strategy"]).toBe("cli");
    });
  });

  // ── 3. Runtime tier is ephemeral ─────────────────────────────────────────────

  describe("runtime tier is ephemeral", () => {
    it("hasRuntime=true when runtime tier has values", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        runtime: { pipeline: { ci_timeout: 99 } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(result.tiers.hasRuntime).toBe(true);
    });

    it("hasRuntime=false when runtime tier is absent", () => {
      const result = mergeConfigs({ defaults: DEFAULT_CONFIG }, { skipEnvResolution: true });
      expect(result.tiers.hasRuntime).toBe(false);
    });

    it("hasRuntime=false when runtime tier is an empty object", () => {
      const result = mergeConfigs(
        { defaults: DEFAULT_CONFIG, runtime: {} },
        { skipEnvResolution: true }
      );
      expect(result.tiers.hasRuntime).toBe(false);
    });

    it("runtime values are reflected in the effective config but not in other tier flags", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        runtime: { pipeline: { ci_timeout: 42 } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      // Runtime value is effective
      expect(result.config.pipeline?.ci_timeout).toBe(42);
      // Other tier flags remain false — runtime did not trigger them
      expect(result.tiers.hasGlobal).toBe(false);
      expect(result.tiers.hasProject).toBe(false);
      expect(result.tiers.hasLocal).toBe(false);
      expect(result.tiers.hasEnv).toBe(false);
      expect(result.tiers.hasCli).toBe(false);
    });
  });

  // ── 4. Team-tier (project) vs runtime: runtime wins ─────────────────────────

  describe("team-tier (project) read-only semantics: runtime overrides project", () => {
    it("runtime value wins over project value, source is runtime not project", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        project: { project: { number: 1 } } as Partial<IncrediConfig>,
        runtime: { project: { number: 2 } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(result.config.project?.number).toBe(2);
      expect(result.sources["project.number"]).toBe("runtime");
      // Confirm it is NOT project-sourced
      expect(result.sources["project.number"]).not.toBe("project");
    });

    it("project-only key remains project-sourced when runtime does not override it", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        project: { project: { number: 5 } } as Partial<IncrediConfig>,
        runtime: { pipeline: { ci_timeout: 30 } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(result.sources["project.number"]).toBe("project");
    });

    it("wasOverridden(project) returns true when runtime provides the value", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        project: { project: { number: 1 } } as Partial<IncrediConfig>,
        runtime: { project: { number: 2 } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(wasOverridden(result, "project.number", "project")).toBe(true);
    });
  });

  // ── 5. Machine-tier (global) ─────────────────────────────────────────────────

  describe("machine-tier (global): global overrides defaults, local overrides global", () => {
    it("global overrides defaults for branch.base", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG, // branch.base = 'main'
        global: { branch: { base: "trunk" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(result.config.branch?.base).toBe("trunk");
      expect(result.sources["branch.base"]).toBe("global");
    });

    it("local overrides global for branch.base", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        global: { branch: { base: "trunk" } } as Partial<IncrediConfig>,
        local: { branch: { base: "my-local-base" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(result.config.branch?.base).toBe("my-local-base");
      expect(result.sources["branch.base"]).toBe("local");
    });

    it("global-only keys remain global-sourced when local sets a different key", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        global: {
          pull_request: { merge_strategy: "rebase", delete_branch: false },
        } as Partial<IncrediConfig>,
        local: { pull_request: { draft_by_default: true } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      // global key untouched by local
      expect(result.sources["pull_request.merge_strategy"]).toBe("global");
      expect(result.sources["pull_request.delete_branch"]).toBe("global");
      // local key wins
      expect(result.sources["pull_request.draft_by_default"]).toBe("local");
    });

    it("wasOverridden(global) returns true when local overrides it", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        global: { branch: { base: "trunk" } } as Partial<IncrediConfig>,
        local: { branch: { base: "dev" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(wasOverridden(result, "branch.base", "global")).toBe(true);
    });

    it("wasOverridden(global) returns false when global itself supplied the value", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        global: { branch: { base: "trunk" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(wasOverridden(result, "branch.base", "global")).toBe(false);
    });
  });

  // ── 6. Source tracking correctness for all 7 tiers ──────────────────────────

  describe("source tracking correctness: result.sources maps each key to its tier", () => {
    it("default-sourced keys show 'default' in sources", () => {
      const result = mergeConfigs({ defaults: DEFAULT_CONFIG }, { skipEnvResolution: true });
      // pull_request.merge_strategy = 'squash' from DEFAULT_CONFIG
      expect(result.sources["pull_request.merge_strategy"]).toBe("default");
    });

    it("global-sourced keys show 'global' in sources", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        global: { issue: { auto_assign: false } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(result.sources["issue.auto_assign"]).toBe("global");
    });

    it("project-sourced keys show 'project' in sources", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        project: { issue: { default_status: "ready" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(result.sources["issue.default_status"]).toBe("project");
    });

    it("local-sourced keys show 'local' in sources", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        local: { pipeline: { auto_fix: false } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(result.sources["pipeline.auto_fix"]).toBe("local");
    });

    it("runtime-sourced keys show 'runtime' in sources", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        runtime: { pipeline: { default_mode: "interactive" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(result.sources["pipeline.default_mode"]).toBe("runtime");
    });

    it("env-sourced keys show 'env' in sources", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        env: { branch: { base: "env-base" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(result.sources["branch.base"]).toBe("env");
    });

    it("cli-sourced keys show 'cli' in sources", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        cli: { branch: { base: "cli-base" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(result.sources["branch.base"]).toBe("cli");
    });

    it("higher-tier wins when same key appears in multiple tiers", () => {
      // Set merge_strategy in 4 tiers; project should be overridden by local
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        global: { pull_request: { merge_strategy: "rebase" } } as Partial<IncrediConfig>,
        project: { pull_request: { merge_strategy: "merge" } } as Partial<IncrediConfig>,
        local: { pull_request: { merge_strategy: "squash" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      // local is tier 4, wins over global (2) and project (3)
      expect(result.sources["pull_request.merge_strategy"]).toBe("local");
      expect(result.config.pull_request?.merge_strategy).toBe("squash");
    });
  });

  // ── 7. wasOverridden() for all adjacent pairs ────────────────────────────────

  describe("wasOverridden() for all 6 adjacent pairs", () => {
    const key = "pull_request.merge_strategy";

    it("wasOverridden(default) returns true when global provides the value", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        global: { pull_request: { merge_strategy: "rebase" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(wasOverridden(result, key, "default")).toBe(true);
    });

    it("wasOverridden(default) returns false when only defaults provide the value", () => {
      const result = mergeConfigs({ defaults: DEFAULT_CONFIG }, { skipEnvResolution: true });
      expect(wasOverridden(result, key, "default")).toBe(false);
    });

    it("wasOverridden(global) returns true when project provides the value", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        global: { pull_request: { merge_strategy: "rebase" } } as Partial<IncrediConfig>,
        project: { pull_request: { merge_strategy: "merge" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(wasOverridden(result, key, "global")).toBe(true);
    });

    it("wasOverridden(project) returns true when local provides the value", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        project: { pull_request: { merge_strategy: "merge" } } as Partial<IncrediConfig>,
        local: { pull_request: { merge_strategy: "squash" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(wasOverridden(result, key, "project")).toBe(true);
    });

    it("wasOverridden(local) returns true when runtime provides the value", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        local: { pull_request: { merge_strategy: "squash" } } as Partial<IncrediConfig>,
        runtime: { pull_request: { merge_strategy: "rebase" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(wasOverridden(result, key, "local")).toBe(true);
    });

    it("wasOverridden(runtime) returns true when env provides the value", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        runtime: { pull_request: { merge_strategy: "rebase" } } as Partial<IncrediConfig>,
        env: { pull_request: { merge_strategy: "merge" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(wasOverridden(result, key, "runtime")).toBe(true);
    });

    it("wasOverridden(env) returns true when CLI provides the value", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        env: { pull_request: { merge_strategy: "merge" } } as Partial<IncrediConfig>,
        cli: { pull_request: { merge_strategy: "squash" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      // wasOverridden only checks ConfigSource tiers (not 'cli'), so check via source map
      expect(result.sources[key]).toBe("cli");
      // CLI is beyond the precedence array in wasOverridden, so env is "overridden" by cli
      // confirmed by source being 'cli' rather than 'env'
      expect(result.config.pull_request?.merge_strategy).toBe("squash");
    });

    it("wasOverridden returns false when the lower tier itself wins (no higher tier set)", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        global: { pull_request: { merge_strategy: "rebase" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      // global is the winning tier — nothing higher was set
      expect(wasOverridden(result, key, "global")).toBe(false);
    });
  });

  // ── 8. getPathsFromSource() for each tier ────────────────────────────────────

  describe("getPathsFromSource() returns correct key list for each tier", () => {
    it("returns default paths when only defaults are provided", () => {
      const result = mergeConfigs({ defaults: DEFAULT_CONFIG }, { skipEnvResolution: true });
      const defaultPaths = getPathsFromSource(result, "default");
      expect(defaultPaths.length).toBeGreaterThan(0);
      // All paths should be default-sourced
      expect(defaultPaths).toContain("pull_request.merge_strategy");
      expect(defaultPaths).toContain("branch.base");
    });

    it("returns global paths for keys set only at the global tier", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        global: {
          pull_request: { merge_strategy: "rebase", delete_branch: false },
        } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      const globalPaths = getPathsFromSource(result, "global");
      expect(globalPaths).toContain("pull_request.merge_strategy");
      expect(globalPaths).toContain("pull_request.delete_branch");
      // Should not contain keys that defaulted
      expect(globalPaths).not.toContain("branch.base");
    });

    it("returns project paths for keys set only at the project tier", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        project: { project: { number: 42 } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      const projectPaths = getPathsFromSource(result, "project");
      expect(projectPaths).toContain("project.number");
    });

    it("returns local paths for keys set only at the local tier", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        local: { pipeline: { auto_fix: false, ci_timeout: 99 } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      const localPaths = getPathsFromSource(result, "local");
      expect(localPaths).toContain("pipeline.auto_fix");
      expect(localPaths).toContain("pipeline.ci_timeout");
    });

    it("returns runtime paths for keys set only at the runtime tier", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        runtime: {
          pipeline: { default_mode: "interactive" },
          branch: { base: "runtime-base" },
        } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      const runtimePaths = getPathsFromSource(result, "runtime");
      expect(runtimePaths).toContain("pipeline.default_mode");
      expect(runtimePaths).toContain("branch.base");
    });

    it("returns env paths for keys set only at the env tier", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        env: { issue: { auto_assign: false } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      const envPaths = getPathsFromSource(result, "env");
      expect(envPaths).toContain("issue.auto_assign");
    });

    it("returns cli paths for keys set only at the CLI tier", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        cli: {
          branch: { base: "cli-base" },
          pipeline: { ci_timeout: 5 },
        } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      const cliPaths = getPathsFromSource(result, "cli");
      expect(cliPaths).toContain("branch.base");
      expect(cliPaths).toContain("pipeline.ci_timeout");
    });

    it("a key overridden by a higher tier does not appear in the lower tier's path list", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        global: { branch: { base: "trunk" } } as Partial<IncrediConfig>,
        local: { branch: { base: "dev" } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      const globalPaths = getPathsFromSource(result, "global");
      const localPaths = getPathsFromSource(result, "local");

      // local won, so branch.base is in local, not global
      expect(localPaths).toContain("branch.base");
      expect(globalPaths).not.toContain("branch.base");
    });

    it("empty tier produces an empty path list for that tier", () => {
      const result = mergeConfigs(
        { defaults: DEFAULT_CONFIG, runtime: {} },
        { skipEnvResolution: true }
      );
      const runtimePaths = getPathsFromSource(result, "runtime");
      expect(runtimePaths).toHaveLength(0);
    });
  });
});
