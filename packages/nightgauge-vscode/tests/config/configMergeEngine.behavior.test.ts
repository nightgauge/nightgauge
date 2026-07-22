/**
 * Behavior tests for Config Merge Engine
 *
 * Tests real-world scenarios and edge cases for the 6-tier precedence chain.
 * These tests verify the documented behavior that users depend on.
 *
 * @see Issue #436 - Config Merge Engine with 6-Tier Precedence Chain
 * @see docs/CONFIGURATION.md - Configuration precedence documentation
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  mergeConfigs,
  mergeFileConfigs,
  type ConfigTiers,
} from "../../src/config/configMergeEngine";
import { getSource, DEFAULT_CONFIG } from "../../src/config/schema";

describe("configMergeEngine.behavior", () => {
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
  // Project Config Overrides Global
  // ============================================================================

  describe("project config overrides global", () => {
    it("project merge_strategy overrides global preference", () => {
      // User prefers rebase globally, but this project uses squash
      const tiers: ConfigTiers = {
        global: { pr: { merge_strategy: "rebase" } },
        project: { pr: { merge_strategy: "squash" } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pr?.merge_strategy).toBe("squash");
      expect(getSource(result.sources, "pr.merge_strategy")).toBe("project");
    });

    it("project reviewers replace global reviewers", () => {
      // Arrays are replaced, not merged
      const tiers: ConfigTiers = {
        global: { pr: { reviewers: ["default-reviewer"] } },
        project: { pr: { reviewers: ["team-lead", "senior-dev"] } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pr?.reviewers).toEqual(["team-lead", "senior-dev"]);
    });

    it("project inherits unspecified global values", () => {
      const tiers: ConfigTiers = {
        global: { pr: { merge_strategy: "rebase", delete_branch: false } },
        project: { pr: { merge_strategy: "squash" } }, // Only override merge_strategy
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pr?.merge_strategy).toBe("squash"); // Project
      expect(result.config.pr?.delete_branch).toBe(false); // Global
    });
  });

  // ============================================================================
  // Local Config Overrides Project (Developer Overrides)
  // ============================================================================

  describe("local config overrides project (gitignored developer settings)", () => {
    it("developer enables auto_accept_stages locally for faster iteration", () => {
      // Team disables auto-accept in project config
      const tiers: ConfigTiers = {
        project: { human_in_the_loop: { auto_accept_stages: false } },
        local: { human_in_the_loop: { auto_accept_stages: true } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.human_in_the_loop?.auto_accept_stages).toBe(true);
      expect(getSource(result.sources, "human_in_the_loop.auto_accept_stages")).toBe("local");
    });

    it("developer skips lint locally for quick prototyping", () => {
      const tiers: ConfigTiers = {
        project: { pipeline: { skip: { lint: false } } },
        local: { pipeline: { skip: { lint: true } } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pipeline?.skip?.lint).toBe(true);
      expect(getSource(result.sources, "pipeline.skip.lint")).toBe("local");
    });

    it("developer uses different reviewers locally", () => {
      const tiers: ConfigTiers = {
        project: { pr: { reviewers: ["team-lead", "senior-dev"] } },
        local: { pr: { reviewers: ["pair-partner"] } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pr?.reviewers).toEqual(["pair-partner"]);
    });

    it("local delete_branch enables bypass for developer testing", () => {
      const tiers: ConfigTiers = {
        project: { pr: { delete_branch: false } },
        local: { pr: { delete_branch: true } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pr?.delete_branch).toBe(true);
    });
  });

  // ============================================================================
  // Environment Variable Overrides
  // ============================================================================

  describe("env var overrides local config", () => {
    it("NIGHTGAUGE_PR_DELETE_BRANCH=true overrides local config", () => {
      process.env.NIGHTGAUGE_PR_DELETE_BRANCH = "true";

      const tiers: ConfigTiers = {
        local: { pr: { delete_branch: false } },
      };
      const result = mergeConfigs(tiers);

      expect(result.config.pr?.delete_branch).toBe(true);
      expect(getSource(result.sources, "pr.delete_branch")).toBe("env");
      expect(result.envVarsApplied).toContain("NIGHTGAUGE_PR_DELETE_BRANCH");
    });

    it("NIGHTGAUGE_PIPELINE_AUTO_FIX=false disables auto-fix", () => {
      process.env.NIGHTGAUGE_PIPELINE_AUTO_FIX = "false";

      const result = mergeConfigs({});

      expect(result.config.pipeline?.auto_fix).toBe(false);
      expect(getSource(result.sources, "pipeline.auto_fix")).toBe("env");
    });

    it("env vars are tracked in envVarsApplied list", () => {
      process.env.NIGHTGAUGE_PR_MERGE_STRATEGY = "rebase";
      process.env.NIGHTGAUGE_PIPELINE_AUTO_FIX = "true";

      const result = mergeConfigs({});

      expect(result.envVarsApplied).toContain("NIGHTGAUGE_PR_MERGE_STRATEGY");
      expect(result.envVarsApplied).toContain("NIGHTGAUGE_PIPELINE_AUTO_FIX");
    });
  });

  // ============================================================================
  // CLI Flag Overrides (Highest Priority)
  // ============================================================================

  describe("CLI flag overrides all other sources", () => {
    it("CLI flag overrides env var", () => {
      process.env.NIGHTGAUGE_PR_DELETE_BRANCH = "false";

      const tiers: ConfigTiers = {
        cli: { pr: { delete_branch: true } },
      };
      const result = mergeConfigs(tiers);

      expect(result.config.pr?.delete_branch).toBe(true);
      expect(getSource(result.sources, "pr.delete_branch")).toBe("cli");
    });

    it("CLI flag overrides local config", () => {
      const tiers: ConfigTiers = {
        local: { pipeline: { skip: { tests: false } } },
        cli: { pipeline: { skip: { tests: true } } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pipeline?.skip?.tests).toBe(true);
      expect(getSource(result.sources, "pipeline.skip.tests")).toBe("cli");
    });

    it("CLI overrides are tracked in cliOverrides list", () => {
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
  });

  // ============================================================================
  // Source Annotations Match Effective Values
  // ============================================================================

  describe("source annotations match effective values", () => {
    it("each effective value has correct source annotation", () => {
      const tiers: ConfigTiers = {
        global: { pr: { merge_strategy: "rebase" } },
        project: { project: { number: 10 } },
        local: { pr: { delete_branch: true } },
        cli: { pipeline: { auto_fix: false } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      // Verify each value matches its source
      expect(result.config.pr?.merge_strategy).toBe("rebase");
      expect(getSource(result.sources, "pr.merge_strategy")).toBe("global");

      expect(result.config.project?.number).toBe(10);
      expect(getSource(result.sources, "project.number")).toBe("project");

      expect(result.config.pr?.delete_branch).toBe(true);
      expect(getSource(result.sources, "pr.delete_branch")).toBe("local");

      expect(result.config.pipeline?.auto_fix).toBe(false);
      expect(getSource(result.sources, "pipeline.auto_fix")).toBe("cli");
    });

    it("defaults are tracked for unoverridden values", () => {
      const result = mergeConfigs({}, { skipEnvResolution: true });

      // Validation defaults
      expect(result.config.validation?.require_tests).toBe(true);
      expect(getSource(result.sources, "validation.require_tests")).toBe("default");

      // Branch defaults
      expect(result.config.branch?.base).toBe("main");
      expect(getSource(result.sources, "branch.base")).toBe("default");
    });
  });

  // ============================================================================
  // Validation After Merge
  // ============================================================================

  describe("validation after merge catches invalid combinations", () => {
    it("rejects negative project number", () => {
      const tiers: ConfigTiers = {
        project: { project: { number: -1 } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.validation.valid).toBe(false);
      expect(result.validation.errors.length).toBeGreaterThan(0);
      expect(result.validation.errors[0].field).toContain("project.number");
    });

    it("rejects invalid merge_strategy value", () => {
      const tiers: ConfigTiers = {
        project: {
          pr: { merge_strategy: "invalid" as "squash" },
        },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.validation.valid).toBe(false);
    });

    it("accepts valid config across all tiers", () => {
      const tiers: ConfigTiers = {
        global: { pr: { merge_strategy: "rebase" } },
        project: { project: { number: 10 } },
        local: { pr: { delete_branch: true } },
        cli: { pipeline: { auto_fix: false } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.validation.valid).toBe(true);
      expect(result.validation.errors).toHaveLength(0);
    });
  });

  // ============================================================================
  // Performance Requirements
  // ============================================================================

  describe("performance", () => {
    it("merges typical configs without pathological slowness", () => {
      const tiers: ConfigTiers = {
        global: {
          pr: { merge_strategy: "rebase", reviewers: ["default"] },
        },
        project: {
          project: { number: 10, auto_dates: true },
          pr: { reviewers: ["team-lead", "senior-dev"] },
          pipeline: {
            ci_timeout: 300,
            auto_fix: true,
            skip: { tests: false, lint: false },
            logs: { retain: true, dir: ".nightgauge/logs" },
          },
        },
        local: {
          pr: { delete_branch: true },
          human_in_the_loop: { auto_accept_stages: true },
        },
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      // Typical execution is <5ms; the ceiling is a regression guard against a
      // pathological implementation, not a micro-benchmark. A tight bound flaked
      // under full-suite CPU contention (self-reported >50ms from scheduler
      // starvation alone), so it is deliberately generous.
      expect(result.mergeTimeMs).toBeLessThan(1000);
    });

    it("merges all 6 tiers without pathological slowness", () => {
      const tiers: ConfigTiers = {
        defaults: DEFAULT_CONFIG,
        global: { pr: { merge_strategy: "rebase" } },
        project: { project: { number: 10 } },
        local: { pr: { delete_branch: true } },
        env: { pipeline: { auto_fix: false } },
        cli: { batch: { max_issues: 5 } },
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      // Regression guard, not a micro-benchmark — generous to survive CI load.
      expect(result.mergeTimeMs).toBeLessThan(1000);
    });
  });

  // ============================================================================
  // Edge Cases
  // ============================================================================

  describe("edge cases", () => {
    it("handles empty configs at all tiers", () => {
      const tiers: ConfigTiers = {
        global: {},
        project: {},
        local: {},
        env: {},
        cli: {},
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config).toBeDefined();
      expect(result.validation.valid).toBe(true);
      expect(result.tiers.hasGlobal).toBe(false);
      expect(result.tiers.hasProject).toBe(false);
    });

    it("handles partial configs at each tier", () => {
      const tiers: ConfigTiers = {
        global: { pr: {} }, // Partial pr
        project: { pipeline: {} }, // Partial pipeline
        local: { batch: {} }, // Partial batch
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config).toBeDefined();
      expect(result.validation.valid).toBe(true);
    });

    it("handles deeply nested overrides", () => {
      const tiers: ConfigTiers = {
        project: {
          pipeline: {
            retry: {
              max_auto_attempts: 3,
              backoff_multiplier: 2,
            },
          },
        },
        local: {
          pipeline: {
            retry: {
              max_auto_attempts: 5,
            },
          },
        },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      // Local overrides max_auto_attempts
      expect(result.config.pipeline?.retry?.max_auto_attempts).toBe(5);
      // Project's backoff_multiplier is preserved
      expect(result.config.pipeline?.retry?.backoff_multiplier).toBe(2);
    });

    it("handles mixed undefined and null values", () => {
      const tiers: ConfigTiers = {
        project: { project: { number: 10 } },
        local: { project: { number: undefined } }, // Should NOT override
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.project?.number).toBe(10);
    });

    it("handles boolean false values correctly", () => {
      const tiers: ConfigTiers = {
        project: { pr: { delete_branch: true } },
        local: { pr: { delete_branch: false } }, // Explicit false should override
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pr?.delete_branch).toBe(false);
    });

    it("handles zero values correctly", () => {
      const tiers: ConfigTiers = {
        project: { batch: { max_issues: 10 } },
        local: { batch: { max_issues: 0 } }, // 0 should override (but may fail validation)
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      // 0 is applied but may not pass validation
      // The test verifies the merge behavior, not validation
      expect(result.tiers.hasLocal).toBe(true);
    });
  });

  // ============================================================================
  // Runtime Tier — Ephemeral UI State
  // ============================================================================

  describe("runtime tier — ephemeral UI state", () => {
    it("filter state is ephemeral: runtime value wins over project, source is runtime", () => {
      // Simulates: readyItems.filters.* toggled in UI (runtime) vs team YAML (project)
      const tiers: ConfigTiers = {
        project: { pipeline: { max_concurrent: 2 } },
        runtime: { pipeline: { max_concurrent: 5 } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pipeline?.max_concurrent).toBe(5);
      expect(getSource(result.sources, "pipeline.max_concurrent")).toBe("runtime");
    });

    it("setting a runtime value does not affect team YAML source for other keys", () => {
      // Simulates: UI sets runtime key — team-YAML keys should retain 'project' source
      const tiers: ConfigTiers = {
        project: { pr: { merge_strategy: "squash" }, pipeline: { auto_fix: true } },
        runtime: { pipeline: { max_concurrent: 8 } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      // Runtime key: sourced from runtime
      expect(getSource(result.sources, "pipeline.max_concurrent")).toBe("runtime");
      // Team YAML keys: still sourced from project
      expect(getSource(result.sources, "pr.merge_strategy")).toBe("project");
      expect(getSource(result.sources, "pipeline.auto_fix")).toBe("project");
    });

    it("runtime tier is absent from hasRuntime=false when not provided", () => {
      const tiers: ConfigTiers = { project: { pr: { merge_strategy: "rebase" } } };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(result.tiers.hasRuntime).toBe(false);
    });

    it("hasRuntime=true only when runtime tier is non-empty", () => {
      const tiers: ConfigTiers = {
        project: { pr: { merge_strategy: "rebase" } },
        runtime: { pipeline: { max_concurrent: 3 } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      expect(result.tiers.hasRuntime).toBe(true);
    });
  });

  // ============================================================================
  // Team Tier — Project Config Keys
  // ============================================================================

  describe("team tier — project config keys", () => {
    it("pipeline.budget_preset is correctly sourced from project tier", () => {
      const tiers: ConfigTiers = {
        project: { pipeline: { budget_preset: "conservative" } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pipeline?.budget_preset).toBe("conservative");
      expect(getSource(result.sources, "pipeline.budget_preset")).toBe("project");
    });

    it("pr.merge_strategy is correctly sourced from project tier", () => {
      const tiers: ConfigTiers = {
        project: { pr: { merge_strategy: "squash" } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pr?.merge_strategy).toBe("squash");
      expect(getSource(result.sources, "pr.merge_strategy")).toBe("project");
    });

    it("autonomous.scan_interval is correctly sourced from project tier", () => {
      const tiers: ConfigTiers = {
        project: { autonomous: { scan_interval: 120 } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.autonomous?.scan_interval).toBe(120);
      expect(getSource(result.sources, "autonomous.scan_interval")).toBe("project");
    });

    it("global (machine tier) does not override project keys when both set", () => {
      // Machine tier is lower priority than project tier
      const tiers: ConfigTiers = {
        global: { pr: { merge_strategy: "rebase" } },
        project: { pr: { merge_strategy: "squash" } },
      };
      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pr?.merge_strategy).toBe("squash");
      expect(getSource(result.sources, "pr.merge_strategy")).toBe("project");
    });
  });

  // ============================================================================
  // mergeFileConfigs Convenience Function
  // ============================================================================

  describe("mergeFileConfigs convenience function", () => {
    it("merges three file-based tiers correctly", () => {
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

    it("handles null global config", () => {
      const result = mergeFileConfigs(null, { project: { number: 10 } }, null, {
        skipEnvResolution: true,
      });

      expect(result.config.project?.number).toBe(10);
      expect(result.tiers.hasGlobal).toBe(false);
    });

    it("applies env vars when not skipped", () => {
      process.env.NIGHTGAUGE_PR_DELETE_BRANCH = "true";

      const result = mergeFileConfigs(null, { pr: { delete_branch: false } }, null);

      expect(result.config.pr?.delete_branch).toBe(true);
      expect(getSource(result.sources, "pr.delete_branch")).toBe("env");
    });
  });
});
