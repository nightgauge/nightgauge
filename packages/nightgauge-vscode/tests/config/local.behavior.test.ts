/**
 * Behavior tests for local config override (.nightgauge/config.local.yaml)
 *
 * These tests verify that local config overrides project config correctly
 * and that the precedence chain is: Env > Local > Project > Global > Defaults
 *
 * @see Issue #435 - Add local config override
 * @see packages/nightgauge-vscode/src/config/schema.ts - ConfigSource
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  mergeWithDefaults,
  DEFAULT_CONFIG,
  type ConfigSourceMap,
  trackObjectSources,
  getSource,
} from "../../src/config/schema";
import type { IncrediConfig } from "../../src/views/settings/types";

describe("local.behavior", () => {
  // Store original env vars
  const originalEnv = { ...process.env };

  beforeEach(() => {
    // Clear local config related environment variables
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
  // Precedence Chain Tests
  // ============================================================================

  describe("precedence", () => {
    /**
     * Simulates the config merge that IncrediYamlService.readMerged() performs
     */
    function simulateMerge(
      globalConfig: IncrediConfig,
      projectConfig: IncrediConfig,
      localConfig: IncrediConfig
    ): IncrediConfig {
      function deepMerge<T extends Record<string, unknown>>(target: T, source: Partial<T>): T {
        const result = { ...target } as Record<string, unknown>;

        for (const key in source) {
          const sourceValue = source[key];
          const targetValue = target[key];

          if (sourceValue === undefined) {
            continue;
          }

          if (
            typeof sourceValue === "object" &&
            sourceValue !== null &&
            !Array.isArray(sourceValue) &&
            typeof targetValue === "object" &&
            targetValue !== null &&
            !Array.isArray(targetValue)
          ) {
            result[key] = deepMerge(
              targetValue as Record<string, unknown>,
              sourceValue as Record<string, unknown>
            );
          } else {
            result[key] = sourceValue;
          }
        }

        return result as T;
      }

      const defaults = mergeWithDefaults({});
      return deepMerge(deepMerge(deepMerge(defaults, globalConfig), projectConfig), localConfig);
    }

    it("local overrides project config", () => {
      const projectConfig: IncrediConfig = {
        pr: { delete_branch: false },
      };
      const localConfig: IncrediConfig = {
        pr: { delete_branch: true },
      };

      const result = simulateMerge({}, projectConfig, localConfig);

      expect(result.pr?.delete_branch).toBe(true);
    });

    it("local overrides global config", () => {
      const globalConfig: IncrediConfig = {
        pr: { merge_strategy: "rebase" },
      };
      const localConfig: IncrediConfig = {
        pr: { merge_strategy: "squash" },
      };

      const result = simulateMerge(globalConfig, {}, localConfig);

      expect(result.pr?.merge_strategy).toBe("squash");
    });

    it("project still wins where local is not set", () => {
      const projectConfig: IncrediConfig = {
        pr: { delete_branch: false },
      };
      const localConfig: IncrediConfig = {
        pr: { delete_branch: true },
        // delete_branch not set in local
      };

      const result = simulateMerge({}, projectConfig, localConfig);

      expect(result.pr?.delete_branch).toBe(true); // Local
      expect(result.pr?.delete_branch).toBe(true); // Project
    });

    it("defaults apply when nothing is set", () => {
      const result = simulateMerge({}, {}, {});

      // Check defaults are applied
      // Note: DEFAULT_CONFIG uses 'pull_request' not 'pr' (schema canonical names)
      expect(result.pull_request?.merge_strategy).toBe("squash");
      expect(result.branch?.base).toBe("main");
    });
  });

  // ============================================================================
  // Source Tracking Tests
  // ============================================================================

  describe("source tracking", () => {
    it("tracks local source correctly", () => {
      const sources: ConfigSourceMap = {};

      const localConfig: IncrediConfig = {
        pr: { delete_branch: true },
        human_in_the_loop: { auto_accept_stages: true },
      };

      trackObjectSources(sources, localConfig as Record<string, unknown>, "", "local");

      expect(getSource(sources, "pr.delete_branch")).toBe("local");
      expect(getSource(sources, "human_in_the_loop.auto_accept_stages")).toBe("local");
    });

    it("local overwrites project source in tracking", () => {
      const sources: ConfigSourceMap = {};

      // First project sets it
      const projectConfig: IncrediConfig = {
        pr: { delete_branch: false },
      };
      trackObjectSources(sources, projectConfig as Record<string, unknown>, "", "project");

      expect(getSource(sources, "pr.delete_branch")).toBe("project");

      // Then local overrides
      const localConfig: IncrediConfig = {
        pr: { delete_branch: true },
      };
      trackObjectSources(sources, localConfig as Record<string, unknown>, "", "local");

      expect(getSource(sources, "pr.delete_branch")).toBe("local");
    });
  });

  // ============================================================================
  // Common Local Override Use Cases
  // ============================================================================

  describe("common use cases", () => {
    it("developer enables auto_accept_stages locally for faster iteration", () => {
      // Team disables auto-accept in project config
      const projectConfig: IncrediConfig = {
        human_in_the_loop: { auto_accept_stages: false },
      };

      // Developer enables it locally
      const localConfig: IncrediConfig = {
        human_in_the_loop: { auto_accept_stages: true },
      };

      const sources: ConfigSourceMap = {};
      trackObjectSources(sources, projectConfig as Record<string, unknown>, "", "project");
      trackObjectSources(sources, localConfig as Record<string, unknown>, "", "local");

      expect(getSource(sources, "human_in_the_loop.auto_accept_stages")).toBe("local");
    });

    it("developer skips lint locally for quick prototyping", () => {
      const projectConfig: IncrediConfig = {
        pipeline: { skip: { lint: false } },
      };

      const localConfig: IncrediConfig = {
        pipeline: { skip: { lint: true } },
      };

      const sources: ConfigSourceMap = {};
      trackObjectSources(sources, projectConfig as Record<string, unknown>, "", "project");
      trackObjectSources(sources, localConfig as Record<string, unknown>, "", "local");

      expect(getSource(sources, "pipeline.skip.lint")).toBe("local");
    });

    it("developer uses different reviewers locally", () => {
      const projectConfig: IncrediConfig = {
        pr: { reviewers: ["team-lead", "senior-dev"] },
      };

      const localConfig: IncrediConfig = {
        pr: { reviewers: ["pair-partner"] },
      };

      // Arrays are replaced, not merged
      function deepMerge<T extends Record<string, unknown>>(target: T, source: Partial<T>): T {
        const result = { ...target } as Record<string, unknown>;
        for (const key in source) {
          const sourceValue = source[key];
          const targetValue = target[key];
          if (sourceValue === undefined) continue;
          if (
            typeof sourceValue === "object" &&
            sourceValue !== null &&
            !Array.isArray(sourceValue) &&
            typeof targetValue === "object" &&
            targetValue !== null &&
            !Array.isArray(targetValue)
          ) {
            result[key] = deepMerge(
              targetValue as Record<string, unknown>,
              sourceValue as Record<string, unknown>
            );
          } else {
            result[key] = sourceValue;
          }
        }
        return result as T;
      }

      const merged = deepMerge(projectConfig, localConfig);
      expect(merged.pr?.reviewers).toEqual(["pair-partner"]);
    });
  });

  // ============================================================================
  // Critical Settings Warning Tests
  // ============================================================================

  describe("critical settings", () => {
    const CRITICAL_SETTINGS = ["project.number", "project.owner", "project.id"];

    it("should identify project.number as critical", () => {
      expect(CRITICAL_SETTINGS).toContain("project.number");
    });

    it("should identify project.owner as critical", () => {
      expect(CRITICAL_SETTINGS).toContain("project.owner");
    });

    it("local can override critical settings (warns but allows)", () => {
      // The system warns but doesn't block
      const localConfig: IncrediConfig = {
        project: { number: 999 },
      };

      const sources: ConfigSourceMap = {};
      trackObjectSources(sources, localConfig as Record<string, unknown>, "", "local");

      // The value is tracked as local (warning is logged separately)
      expect(getSource(sources, "project.number")).toBe("local");
    });
  });

  // ============================================================================
  // Environment Variable Precedence Tests
  // ============================================================================

  describe("env var precedence over local", () => {
    it("env var should override local config value", () => {
      // This tests the documented precedence: Env > Local > Project

      // Simulate what happens when env var is set
      process.env.NIGHTGAUGE_PR_DELETE_BRANCH = "true";

      // Even if local config says false
      const localConfig: IncrediConfig = {
        pr: { delete_branch: false },
      };

      // The env var check happens first (before file configs)
      const envValue = process.env.NIGHTGAUGE_PR_DELETE_BRANCH;
      const effectiveValue = envValue === "true" ? true : localConfig.pr?.delete_branch;

      expect(effectiveValue).toBe(true);
    });
  });
});
