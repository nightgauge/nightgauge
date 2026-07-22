/**
 * Unit tests for Config Merge Engine
 *
 * Tests the core merge functionality including:
 * - Deep merge behavior (objects, arrays, scalars)
 * - 6-tier precedence chain
 * - Source tracking
 * - Validation after merge
 *
 * @see Issue #436 - Config Merge Engine with 6-Tier Precedence Chain
 * @see packages/nightgauge-vscode/src/config/configMergeEngine.ts
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  mergeConfigs,
  deepMerge,
  getLeafPaths,
  mergeFileConfigs,
  wasOverridden,
  getPathsFromSource,
  getFormattedEntries,
  formatConfigDisplay,
  getValueAtPath,
  type ConfigTiers,
} from "../../src/config/configMergeEngine";
import { DEFAULT_CONFIG, type IncrediConfig, getSource } from "../../src/config/schema";

describe("configMergeEngine", () => {
  // Store original env vars
  const originalEnv = { ...process.env };

  beforeEach(() => {
    // Clear all NIGHTGAUGE_ env vars
    Object.keys(process.env).forEach((key) => {
      if (key.startsWith("NIGHTGAUGE_")) {
        delete process.env[key];
      }
    });
  });

  afterEach(() => {
    // Restore original environment
    process.env = { ...originalEnv };
  });

  // ============================================================================
  // deepMerge Tests
  // ============================================================================

  describe("deepMerge", () => {
    it("merges flat objects", () => {
      const target = { a: 1, b: 2 };
      const source = { b: 3, c: 4 };
      const result = deepMerge(target, source);

      expect(result).toEqual({ a: 1, b: 3, c: 4 });
    });

    it("merges nested objects recursively", () => {
      const target = { nested: { a: 1, b: 2 } };
      const source = { nested: { b: 3, c: 4 } };
      const result = deepMerge(target, source);

      expect(result).toEqual({ nested: { a: 1, b: 3, c: 4 } });
    });

    it("merges deeply nested objects (3+ levels)", () => {
      const target = {
        level1: {
          level2: {
            level3: { a: 1, b: 2 },
          },
        },
      };
      const source = {
        level1: {
          level2: {
            level3: { b: 3, c: 4 },
          },
        },
      };
      const result = deepMerge(target, source);

      expect(result).toEqual({
        level1: {
          level2: {
            level3: { a: 1, b: 3, c: 4 },
          },
        },
      });
    });

    it("replaces arrays (not concatenate)", () => {
      const target = { arr: [1, 2, 3] };
      const source = { arr: [4, 5] };
      const result = deepMerge(target, source);

      expect(result).toEqual({ arr: [4, 5] });
    });

    it("scalars use last-writer-wins", () => {
      const target = { value: "original" };
      const source = { value: "override" };
      const result = deepMerge(target, source);

      expect(result).toEqual({ value: "override" });
    });

    it("undefined values do NOT override defined values", () => {
      const target = { a: 1, b: 2 };
      const source = { a: undefined, b: 3 };
      const result = deepMerge(target, source);

      expect(result).toEqual({ a: 1, b: 3 });
    });

    it("null values DO override (explicit null)", () => {
      const target = { a: 1, b: 2 };
      const source = { a: null };
      const result = deepMerge(target, source as Partial<typeof target>);

      expect(result).toEqual({ a: null, b: 2 });
    });

    it("handles null source gracefully", () => {
      const target = { a: 1 };
      const result = deepMerge(target, null);

      expect(result).toEqual({ a: 1 });
    });

    it("handles undefined source gracefully", () => {
      const target = { a: 1 };
      const result = deepMerge(target, undefined);

      expect(result).toEqual({ a: 1 });
    });

    it("creates new object (does not mutate target)", () => {
      const target = { a: 1 };
      const source = { b: 2 };
      const result = deepMerge(target, source);

      expect(result).not.toBe(target);
      expect(target).toEqual({ a: 1 });
    });

    it("creates new nested objects (does not mutate)", () => {
      const target = { nested: { a: 1 } };
      const source = { nested: { b: 2 } };
      const result = deepMerge(target, source);

      expect(result.nested).not.toBe(target.nested);
    });
  });

  // ============================================================================
  // getLeafPaths Tests
  // ============================================================================

  describe("getLeafPaths", () => {
    it("extracts flat paths", () => {
      const obj = { a: 1, b: "hello" };
      const paths = getLeafPaths(obj);

      expect(paths).toEqual(["a", "b"]);
    });

    it("extracts nested paths", () => {
      const obj = { pr: { merge_strategy: "squash" } };
      const paths = getLeafPaths(obj);

      expect(paths).toEqual(["pr.merge_strategy"]);
    });

    it("handles deeply nested paths", () => {
      const obj = {
        pipeline: {
          retry: {
            max_auto_attempts: 3,
          },
        },
      };
      const paths = getLeafPaths(obj);

      expect(paths).toEqual(["pipeline.retry.max_auto_attempts"]);
    });

    it("includes array paths as leaves", () => {
      const obj = { reviewers: ["alice", "bob"] };
      const paths = getLeafPaths(obj);

      expect(paths).toEqual(["reviewers"]);
    });

    it("skips undefined values", () => {
      const obj = { a: 1, b: undefined };
      const paths = getLeafPaths(obj);

      expect(paths).toEqual(["a"]);
    });
  });

  // ============================================================================
  // mergeConfigs Tests - Precedence
  // ============================================================================

  describe("mergeConfigs - precedence", () => {
    it("returns defaults when no tiers provided", () => {
      const result = mergeConfigs({}, { skipEnvResolution: true });

      expect(result.config).toBeDefined();
      expect(result.tiers.hasDefaults).toBe(true);
      expect(result.validation.valid).toBe(true);
    });

    it("global overrides defaults", () => {
      const tiers: ConfigTiers = {
        global: { pr: { merge_strategy: "rebase" } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pr?.merge_strategy).toBe("rebase");
      expect(getSource(result.sources, "pr.merge_strategy")).toBe("global");
    });

    it("project overrides global", () => {
      const tiers: ConfigTiers = {
        global: { pr: { merge_strategy: "rebase" } },
        project: { pr: { merge_strategy: "squash" } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pr?.merge_strategy).toBe("squash");
      expect(getSource(result.sources, "pr.merge_strategy")).toBe("project");
    });

    it("local overrides project", () => {
      const tiers: ConfigTiers = {
        project: { pr: { delete_branch: false } },
        local: { pr: { delete_branch: true } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pr?.delete_branch).toBe(true);
      expect(getSource(result.sources, "pr.delete_branch")).toBe("local");
    });

    it("env overrides local", () => {
      const tiers: ConfigTiers = {
        local: { pr: { delete_branch: false } },
        env: { pr: { delete_branch: true } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pr?.delete_branch).toBe(true);
      expect(getSource(result.sources, "pr.delete_branch")).toBe("env");
    });

    it("cli overrides env (highest priority)", () => {
      const tiers: ConfigTiers = {
        env: { pr: { delete_branch: false } },
        cli: { pr: { delete_branch: true } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pr?.delete_branch).toBe(true);
      expect(getSource(result.sources, "pr.delete_branch")).toBe("cli");
    });

    it("all 6 tiers in order produces correct precedence", () => {
      const tiers: ConfigTiers = {
        defaults: { pr: { merge_strategy: "merge" } } as IncrediConfig,
        global: { pr: { merge_strategy: "rebase" } },
        project: { pr: { merge_strategy: "squash" } },
        local: { pr: { delete_branch: false } },
        env: { pr: { draft_by_default: true } },
        cli: { pr: { auto_merge: true } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      // Highest tier with each value wins
      expect(result.config.pr?.merge_strategy).toBe("squash"); // project
      expect(result.config.pr?.delete_branch).toBe(false); // local
      expect(result.config.pr?.draft_by_default).toBe(true); // env
      expect(result.config.pr?.auto_merge).toBe(true); // cli
    });
  });

  // ============================================================================
  // mergeConfigs Tests - Source Tracking
  // ============================================================================

  describe("mergeConfigs - source tracking", () => {
    it("tracks default source for unoverridden values", () => {
      const result = mergeConfigs({}, { skipEnvResolution: true });

      // pull_request is the canonical key in DEFAULT_CONFIG
      expect(getSource(result.sources, "pull_request.merge_strategy")).toBe("default");
    });

    it("tracks sources for all tiers", () => {
      const tiers: ConfigTiers = {
        global: { issue: { auto_assign: false } },
        project: { project: { number: 10 } },
        local: { pr: { delete_branch: true } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(getSource(result.sources, "issue.auto_assign")).toBe("global");
      expect(getSource(result.sources, "project.number")).toBe("project");
      expect(getSource(result.sources, "pr.delete_branch")).toBe("local");
    });

    it("tracks nested paths correctly", () => {
      const tiers: ConfigTiers = {
        project: {
          pipeline: {
            retry: {
              max_auto_attempts: 5,
            },
          },
        },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(getSource(result.sources, "pipeline.retry.max_auto_attempts")).toBe("project");
    });

    it("updates source when overridden", () => {
      const tiers: ConfigTiers = {
        global: { pr: { merge_strategy: "rebase" } },
        project: { pr: { merge_strategy: "squash" } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      // Final source should be project (which overrode global)
      expect(getSource(result.sources, "pr.merge_strategy")).toBe("project");
    });
  });

  // ============================================================================
  // mergeConfigs Tests - Tier Metadata
  // ============================================================================

  describe("mergeConfigs - tier metadata", () => {
    it("correctly identifies present tiers", () => {
      const tiers: ConfigTiers = {
        global: { pr: { merge_strategy: "rebase" } },
        project: { project: { number: 10 } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.tiers.hasDefaults).toBe(true);
      expect(result.tiers.hasGlobal).toBe(true);
      expect(result.tiers.hasProject).toBe(true);
      expect(result.tiers.hasLocal).toBe(false);
      expect(result.tiers.hasEnv).toBe(false);
      expect(result.tiers.hasCli).toBe(false);
    });

    it("ignores empty objects as not present", () => {
      const tiers: ConfigTiers = {
        global: {},
        project: { project: { number: 10 } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.tiers.hasGlobal).toBe(false);
      expect(result.tiers.hasProject).toBe(true);
    });
  });

  // ============================================================================
  // mergeConfigs Tests - Validation
  // ============================================================================

  describe("mergeConfigs - validation", () => {
    it("validates final config by default", () => {
      const result = mergeConfigs({}, { skipEnvResolution: true });

      expect(result.validation.valid).toBe(true);
      expect(result.validation.errors).toHaveLength(0);
    });

    it("reports validation errors for invalid config", () => {
      const tiers: ConfigTiers = {
        project: {
          project: { number: -1 }, // Invalid: must be positive
        },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.validation.valid).toBe(false);
      expect(result.validation.errors.length).toBeGreaterThan(0);
    });

    it("can skip validation when requested", () => {
      const tiers: ConfigTiers = {
        project: {
          project: { number: -1 }, // Invalid but validation skipped
        },
      };
      const result = mergeConfigs(tiers, {
        skipEnvResolution: true,
        skipValidation: true,
      });

      expect(result.validation.valid).toBe(true);
    });
  });

  // ============================================================================
  // mergeConfigs Tests - CLI Overrides
  // ============================================================================

  describe("mergeConfigs - CLI overrides", () => {
    it("tracks CLI overrides", () => {
      const tiers: ConfigTiers = {
        cli: {
          pr: { merge_strategy: "rebase" },
          pipeline: { auto_fix: false },
        },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.cliOverrides).toContain("pr.merge_strategy");
      expect(result.cliOverrides).toContain("pipeline.auto_fix");
    });

    it("CLI overrides list is empty when no CLI tier", () => {
      const result = mergeConfigs({}, { skipEnvResolution: true });

      expect(result.cliOverrides).toHaveLength(0);
    });
  });

  // ============================================================================
  // mergeConfigs Tests - Performance
  // ============================================================================

  describe("mergeConfigs - performance", () => {
    it("merges typical configs without pathological slowness", () => {
      const tiers: ConfigTiers = {
        global: { pr: { merge_strategy: "rebase" } },
        project: {
          project: { number: 10 },
          pr: { reviewers: ["alice", "bob"] },
          pipeline: { auto_fix: true },
        },
        local: { pr: { delete_branch: true } },
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      // Merging a handful of tiny objects is sub-millisecond; the ceiling is a
      // regression guard against a genuinely pathological implementation (an
      // accidental O(n²) blow-up or sync I/O), not a micro-benchmark. A tight
      // wall-clock bound flaked under full-suite CPU contention (self-reported
      // >50ms purely from scheduler starvation), so it is generous by design.
      expect(result.mergeTimeMs).toBeLessThan(1000);
    });
  });

  // ============================================================================
  // mergeFileConfigs Tests
  // ============================================================================

  describe("mergeFileConfigs", () => {
    it("merges global, project, and local configs", () => {
      const result = mergeFileConfigs(
        { pr: { merge_strategy: "rebase" } },
        { project: { number: 10 } },
        { pr: { delete_branch: true } },
        { skipEnvResolution: true }
      );

      expect(result.config.pr?.merge_strategy).toBe("rebase");
      expect(result.config.project?.number).toBe(10);
      expect(result.config.pr?.delete_branch).toBe(true);
    });

    it("handles null/undefined configs gracefully", () => {
      const result = mergeFileConfigs(null, undefined, null, {
        skipEnvResolution: true,
      });

      expect(result.config).toBeDefined();
      expect(result.validation.valid).toBe(true);
    });
  });

  // ============================================================================
  // Utility Function Tests
  // ============================================================================

  describe("wasOverridden", () => {
    it("returns true when path was overridden by higher tier", () => {
      const tiers: ConfigTiers = {
        global: { pr: { merge_strategy: "rebase" } },
        project: { pr: { merge_strategy: "squash" } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(wasOverridden(result, "pr.merge_strategy", "global")).toBe(true);
    });

    it("returns false when path was not overridden", () => {
      const tiers: ConfigTiers = {
        global: { pr: { merge_strategy: "rebase" } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(wasOverridden(result, "pr.merge_strategy", "project")).toBe(false);
    });
  });

  describe("getPathsFromSource", () => {
    it("returns all paths from a specific source", () => {
      const tiers: ConfigTiers = {
        global: { pr: { merge_strategy: "rebase" } },
        project: {
          project: { number: 10 },
          pr: { delete_branch: true },
        },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      const projectPaths = getPathsFromSource(result, "project");

      expect(projectPaths).toContain("project.number");
      expect(projectPaths).toContain("pr.delete_branch");
    });
  });

  describe("getValueAtPath", () => {
    it("gets value at simple path", () => {
      const obj = { a: 1, b: 2 };
      expect(getValueAtPath(obj, "a")).toBe(1);
    });

    it("gets value at nested path", () => {
      const obj = { pr: { merge_strategy: "squash" } };
      expect(getValueAtPath(obj, "pr.merge_strategy")).toBe("squash");
    });

    it("returns undefined for missing path", () => {
      const obj = { a: 1 };
      expect(getValueAtPath(obj, "b.c")).toBeUndefined();
    });
  });

  describe("getFormattedEntries", () => {
    it("returns sorted entries with source labels", () => {
      const tiers: ConfigTiers = {
        global: { pr: { merge_strategy: "rebase" } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      const entries = getFormattedEntries(result);

      expect(entries.length).toBeGreaterThan(0);
      const prEntry = entries.find((e) => e.path === "pr.merge_strategy");
      expect(prEntry?.source).toBe("global");
      expect(prEntry?.sourceLabel).toContain("Global");
    });
  });

  describe("formatConfigDisplay", () => {
    it("formats as JSON when json option is true", () => {
      const result = mergeConfigs({}, { skipEnvResolution: true });
      const output = formatConfigDisplay(result, { json: true });

      const parsed = JSON.parse(output);
      expect(parsed.config).toBeDefined();
      expect(parsed.sources).toBeDefined();
    });

    it("formats as readable text by default", () => {
      const result = mergeConfigs({}, { skipEnvResolution: true });
      const output = formatConfigDisplay(result);

      expect(output).toContain("Effective Configuration");
      expect(output).toContain("Merge time:");
    });
  });

  // ============================================================================
  // 7-Tier mergeConfigs — Full Chain
  // ============================================================================

  describe("7-tier mergeConfigs — full chain", () => {
    const TIER_KEY = "pipeline.max_concurrent";

    const TABLE: Array<{
      label: string;
      tiers: ConfigTiers;
      expectedValue: number;
      expectedSource: string;
    }> = [
      {
        label: "defaults wins when no other tier set",
        tiers: { defaults: DEFAULT_CONFIG },
        // DEFAULT_CONFIG value for pipeline.max_concurrent
        expectedValue: DEFAULT_CONFIG.pipeline?.max_concurrent ?? 3,
        expectedSource: "default",
      },
      {
        label: "global wins over defaults",
        tiers: {
          defaults: DEFAULT_CONFIG,
          global: { pipeline: { max_concurrent: 10 } } as Partial<IncrediConfig>,
        },
        expectedValue: 10,
        expectedSource: "global",
      },
      {
        label: "project wins over global",
        tiers: {
          defaults: DEFAULT_CONFIG,
          global: { pipeline: { max_concurrent: 10 } } as Partial<IncrediConfig>,
          project: { pipeline: { max_concurrent: 20 } } as Partial<IncrediConfig>,
        },
        expectedValue: 20,
        expectedSource: "project",
      },
      {
        label: "local wins over project",
        tiers: {
          defaults: DEFAULT_CONFIG,
          project: { pipeline: { max_concurrent: 20 } } as Partial<IncrediConfig>,
          local: { pipeline: { max_concurrent: 30 } } as Partial<IncrediConfig>,
        },
        expectedValue: 30,
        expectedSource: "local",
      },
      {
        label: "runtime wins over local",
        tiers: {
          defaults: DEFAULT_CONFIG,
          local: { pipeline: { max_concurrent: 30 } } as Partial<IncrediConfig>,
          runtime: { pipeline: { max_concurrent: 40 } } as Partial<IncrediConfig>,
        },
        expectedValue: 40,
        expectedSource: "runtime",
      },
      {
        label: "env wins over runtime",
        tiers: {
          defaults: DEFAULT_CONFIG,
          runtime: { pipeline: { max_concurrent: 40 } } as Partial<IncrediConfig>,
          env: { pipeline: { max_concurrent: 50 } } as Partial<IncrediConfig>,
        },
        expectedValue: 50,
        expectedSource: "env",
      },
      {
        label: "cli wins over env (all 7 tiers set, cli wins)",
        tiers: {
          defaults: DEFAULT_CONFIG,
          global: { pipeline: { max_concurrent: 10 } } as Partial<IncrediConfig>,
          project: { pipeline: { max_concurrent: 20 } } as Partial<IncrediConfig>,
          local: { pipeline: { max_concurrent: 30 } } as Partial<IncrediConfig>,
          runtime: { pipeline: { max_concurrent: 40 } } as Partial<IncrediConfig>,
          env: { pipeline: { max_concurrent: 50 } } as Partial<IncrediConfig>,
          cli: { pipeline: { max_concurrent: 99 } } as Partial<IncrediConfig>,
        },
        expectedValue: 99,
        expectedSource: "cli",
      },
    ];

    TABLE.forEach(({ label, tiers, expectedValue, expectedSource }) => {
      it(label, () => {
        const result = mergeConfigs(tiers, { skipEnvResolution: true });
        expect(result.config.pipeline?.max_concurrent).toBe(expectedValue);
        expect(result.sources[TIER_KEY]).toBe(expectedSource);
      });
    });

    it("each tier contributes a distinct key with correct source in full-chain merge", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        global: { pr: { merge_strategy: "rebase" } } as Partial<IncrediConfig>,
        project: { project: { number: 42 } } as Partial<IncrediConfig>,
        local: { pr: { delete_branch: true } } as Partial<IncrediConfig>,
        runtime: { pipeline: { max_concurrent: 7 } } as Partial<IncrediConfig>,
        env: { pipeline: { auto_fix: false } } as Partial<IncrediConfig>,
        cli: { batch: { max_issues: 5 } } as Partial<IncrediConfig>,
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.sources["pr.merge_strategy"]).toBe("global");
      expect(result.sources["project.number"]).toBe("project");
      expect(result.sources["pr.delete_branch"]).toBe("local");
      expect(result.sources["pipeline.max_concurrent"]).toBe("runtime");
      expect(result.sources["pipeline.auto_fix"]).toBe("env");
      expect(result.sources["batch.max_issues"]).toBe("cli");
    });
  });
});
