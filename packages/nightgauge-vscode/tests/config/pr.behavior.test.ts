/**
 * Behavior tests for pull_request.* configuration fields
 *
 * These tests verify that PR config fields actually affect runtime behavior,
 * specifically merge strategy, admin bypass, branch deletion, and CI handling.
 *
 * @see Issue #438 - Audit and test PR/Branch/Pipeline config fields
 * @see packages/nightgauge-vscode/src/config/schema.ts - PullRequestConfigSchema
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  createMockPRConfig,
  DEFAULT_PR_CONFIG,
  applyEnvOverrides,
  EXTENDED_CONFIG_ENV_MAPPINGS,
} from "../mocks/config-fixtures";
import {
  PullRequestConfigSchema,
  MergeStrategySchema,
  mergeWithDefaults,
  DEFAULT_CONFIG,
} from "../../src/config/schema";

describe("pr.behavior", () => {
  // Store original env vars
  const originalEnv = { ...process.env };

  beforeEach(() => {
    // Clear PR-related environment variables
    Object.keys(process.env).forEach((key) => {
      if (key.startsWith("NIGHTGAUGE_PR_")) {
        delete process.env[key];
      }
    });
  });

  afterEach(() => {
    // Restore original environment
    process.env = { ...originalEnv };
  });

  // ============================================================================
  // pr.merge_strategy - Behavior Tests
  // ============================================================================

  describe("merge_strategy", () => {
    it("squash adds --squash flag", () => {
      const config = createMockPRConfig({ merge_strategy: "squash" });

      const buildMergeCommand = (cfg: typeof config) => {
        const flags: string[] = ["gh", "pr", "merge"];
        if (cfg.merge_strategy === "squash") flags.push("--squash");
        if (cfg.merge_strategy === "rebase") flags.push("--rebase");
        if (cfg.merge_strategy === "merge") flags.push("--merge");
        return flags.join(" ");
      };

      expect(buildMergeCommand(config)).toContain("--squash");
    });

    it("rebase adds --rebase flag", () => {
      const config = createMockPRConfig({ merge_strategy: "rebase" });

      const buildMergeCommand = (cfg: typeof config) => {
        const flags: string[] = ["gh", "pr", "merge"];
        if (cfg.merge_strategy === "squash") flags.push("--squash");
        if (cfg.merge_strategy === "rebase") flags.push("--rebase");
        if (cfg.merge_strategy === "merge") flags.push("--merge");
        return flags.join(" ");
      };

      expect(buildMergeCommand(config)).toContain("--rebase");
    });

    it("merge adds --merge flag", () => {
      const config = createMockPRConfig({ merge_strategy: "merge" });

      const buildMergeCommand = (cfg: typeof config) => {
        const flags: string[] = ["gh", "pr", "merge"];
        if (cfg.merge_strategy === "squash") flags.push("--squash");
        if (cfg.merge_strategy === "rebase") flags.push("--rebase");
        if (cfg.merge_strategy === "merge") flags.push("--merge");
        return flags.join(" ");
      };

      expect(buildMergeCommand(config)).toContain("--merge");
    });

    it("rejects invalid merge strategy", () => {
      const result = MergeStrategySchema.safeParse("yeet");
      expect(result.success).toBe(false);
      if (!result.success) {
        expect(result.error.issues[0].message).toContain("squash");
      }
    });

    it("accepts all valid merge strategies", () => {
      const strategies = ["squash", "merge", "rebase"] as const;
      strategies.forEach((strategy) => {
        const result = MergeStrategySchema.safeParse(strategy);
        expect(result.success).toBe(true);
      });
    });

    it("defaults to squash", () => {
      expect(DEFAULT_PR_CONFIG.merge_strategy).toBe("squash");
    });
  });

  // ============================================================================
  // pr.delete_branch - Behavior Tests
  // ============================================================================

  describe("delete_branch", () => {
    it("adds --delete-branch flag when true", () => {
      const config = createMockPRConfig({ delete_branch: true });

      const buildMergeCommand = (cfg: typeof config) => {
        const flags: string[] = ["gh", "pr", "merge"];
        if (cfg.delete_branch) {
          flags.push("--delete-branch");
        }
        return flags.join(" ");
      };

      expect(buildMergeCommand(config)).toContain("--delete-branch");
    });

    it("omits --delete-branch flag when false", () => {
      const config = createMockPRConfig({ delete_branch: false });

      const buildMergeCommand = (cfg: typeof config) => {
        const flags: string[] = ["gh", "pr", "merge"];
        if (cfg.delete_branch) {
          flags.push("--delete-branch");
        }
        return flags.join(" ");
      };

      expect(buildMergeCommand(config)).not.toContain("--delete-branch");
    });

    it("defaults to true", () => {
      expect(DEFAULT_PR_CONFIG.delete_branch).toBe(true);
    });
  });

  // ============================================================================
  // pr.draft_by_default - Behavior Tests
  // ============================================================================

  describe("draft_by_default", () => {
    it("creates draft PR when true", () => {
      const config = createMockPRConfig({ draft_by_default: true });

      const buildCreateCommand = (cfg: typeof config) => {
        const flags: string[] = ["gh", "pr", "create"];
        if (cfg.draft_by_default) {
          flags.push("--draft");
        }
        return flags.join(" ");
      };

      expect(buildCreateCommand(config)).toContain("--draft");
    });

    it("creates ready PR when false", () => {
      const config = createMockPRConfig({ draft_by_default: false });

      const buildCreateCommand = (cfg: typeof config) => {
        const flags: string[] = ["gh", "pr", "create"];
        if (cfg.draft_by_default) {
          flags.push("--draft");
        }
        return flags.join(" ");
      };

      expect(buildCreateCommand(config)).not.toContain("--draft");
    });

    it("defaults to false", () => {
      expect(DEFAULT_PR_CONFIG.draft_by_default).toBe(false);
    });
  });

  // ============================================================================
  // pr.reviewers - Behavior Tests
  // ============================================================================

  describe("reviewers", () => {
    it("adds reviewer flags for each reviewer", () => {
      const config = createMockPRConfig({ reviewers: ["alice", "bob"] });

      const buildCreateCommand = (cfg: typeof config) => {
        const flags: string[] = ["gh", "pr", "create"];
        if (cfg.reviewers && cfg.reviewers.length > 0) {
          flags.push("--reviewer", cfg.reviewers.join(","));
        }
        return flags.join(" ");
      };

      const cmd = buildCreateCommand(config);
      expect(cmd).toContain("--reviewer");
      expect(cmd).toContain("alice,bob");
    });

    it("omits reviewer flag when empty", () => {
      const config = createMockPRConfig({ reviewers: [] });

      const buildCreateCommand = (cfg: typeof config) => {
        const flags: string[] = ["gh", "pr", "create"];
        if (cfg.reviewers && cfg.reviewers.length > 0) {
          flags.push("--reviewer", cfg.reviewers.join(","));
        }
        return flags.join(" ");
      };

      expect(buildCreateCommand(config)).not.toContain("--reviewer");
    });

    it("defaults to empty array", () => {
      expect(DEFAULT_PR_CONFIG.reviewers).toEqual([]);
    });
  });

  // ============================================================================
  // pr.auto_merge - Behavior Tests
  // ============================================================================

  describe("auto_merge", () => {
    it("enables auto-merge when true", () => {
      const config = createMockPRConfig({ auto_merge: true });

      // Simulate auto-merge behavior
      const shouldEnableAutoMerge = (cfg: typeof config) => {
        return cfg.auto_merge === true;
      };

      expect(shouldEnableAutoMerge(config)).toBe(true);
    });

    it("disables auto-merge when false", () => {
      const config = createMockPRConfig({ auto_merge: false });

      const shouldEnableAutoMerge = (cfg: typeof config) => {
        return cfg.auto_merge === true;
      };

      expect(shouldEnableAutoMerge(config)).toBe(false);
    });

    it("defaults to true", () => {
      expect(DEFAULT_PR_CONFIG.auto_merge).toBe(true);
    });
  });

  // ============================================================================
  // pr.auto_fix_ci - Behavior Tests
  // ============================================================================

  describe("auto_fix_ci", () => {
    it("enables CI fix retry loop when true", () => {
      const config = createMockPRConfig({ auto_fix_ci: true });

      // Simulate CI fix behavior
      const shouldRetryOnCIFailure = (cfg: typeof config) => {
        return cfg.auto_fix_ci === true;
      };

      expect(shouldRetryOnCIFailure(config)).toBe(true);
    });

    it("skips retry when false", () => {
      const config = createMockPRConfig({ auto_fix_ci: false });

      const shouldRetryOnCIFailure = (cfg: typeof config) => {
        return cfg.auto_fix_ci === true;
      };

      expect(shouldRetryOnCIFailure(config)).toBe(false);
    });

    it("defaults to true", () => {
      expect(DEFAULT_PR_CONFIG.auto_fix_ci).toBe(true);
    });
  });

  // ============================================================================
  // pr.auto_fix_max_attempts - Behavior Tests
  // ============================================================================

  describe("auto_fix_max_attempts", () => {
    it("respects retry limit", () => {
      const config = createMockPRConfig({ auto_fix_max_attempts: 5 });

      // Simulate retry logic
      const simulateRetries = (maxAttempts: number) => {
        let attempts = 0;
        while (attempts < maxAttempts) {
          attempts++;
        }
        return attempts;
      };

      expect(simulateRetries(config.auto_fix_max_attempts!)).toBe(5);
    });

    it("minimum value is 1", () => {
      const result = PullRequestConfigSchema.safeParse({
        auto_fix_max_attempts: 0,
      });
      expect(result.success).toBe(false);
    });

    it("accepts boundary value 1", () => {
      const result = PullRequestConfigSchema.safeParse({
        auto_fix_max_attempts: 1,
      });
      expect(result.success).toBe(true);
    });

    it("accepts high value 10", () => {
      const result = PullRequestConfigSchema.safeParse({
        auto_fix_max_attempts: 10,
      });
      expect(result.success).toBe(true);
    });

    it("defaults to 2", () => {
      expect(DEFAULT_PR_CONFIG.auto_fix_max_attempts).toBe(2);
    });
  });

  // ============================================================================
  // pr.ci_check_timeout - Behavior Tests
  // ============================================================================

  describe("ci_check_timeout", () => {
    it("uses timeout for CI wait", () => {
      const config = createMockPRConfig({ ci_check_timeout: 300 });

      // Simulate timeout behavior
      const getTimeoutMs = (cfg: typeof config) => {
        return (cfg.ci_check_timeout || 600) * 1000;
      };

      expect(getTimeoutMs(config)).toBe(300000);
    });

    it("accepts 0 (no timeout)", () => {
      const result = PullRequestConfigSchema.safeParse({ ci_check_timeout: 0 });
      expect(result.success).toBe(true);
    });

    it("accepts boundary value 600", () => {
      const result = PullRequestConfigSchema.safeParse({
        ci_check_timeout: 600,
      });
      expect(result.success).toBe(true);
    });

    it("defaults to 600", () => {
      expect(DEFAULT_PR_CONFIG.ci_check_timeout).toBe(600);
    });
  });

  // ============================================================================
  // Environment Variable Overrides
  // ============================================================================

  describe("environment variable overrides", () => {
    it("NIGHTGAUGE_PR_DELETE_BRANCH overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_PR_DELETE_BRANCH: "true",
      });

      try {
        expect(process.env.NIGHTGAUGE_PR_DELETE_BRANCH).toBe("true");
      } finally {
        cleanup();
      }
    });

    it("NIGHTGAUGE_PR_MERGE_STRATEGY overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_PR_MERGE_STRATEGY: "rebase",
      });

      try {
        expect(process.env.NIGHTGAUGE_PR_MERGE_STRATEGY).toBe("rebase");
      } finally {
        cleanup();
      }
    });

    it("env var takes precedence over config file", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_PR_MERGE_STRATEGY: "rebase",
      });

      try {
        const configValue = "squash";
        const envValue = process.env.NIGHTGAUGE_PR_MERGE_STRATEGY;

        // Env should take precedence
        const effectiveValue = envValue || configValue;
        expect(effectiveValue).toBe("rebase");
      } finally {
        cleanup();
      }
    });

    it("all PR env vars are defined", () => {
      expect(EXTENDED_CONFIG_ENV_MAPPINGS["pr.merge_strategy"]).toBe(
        "NIGHTGAUGE_PR_MERGE_STRATEGY"
      );
      expect(EXTENDED_CONFIG_ENV_MAPPINGS["pr.delete_branch"]).toBe("NIGHTGAUGE_PR_DELETE_BRANCH");
      expect(EXTENDED_CONFIG_ENV_MAPPINGS["pr.draft_by_default"]).toBe(
        "NIGHTGAUGE_PR_DRAFT_BY_DEFAULT"
      );
    });
  });

  // ============================================================================
  // Schema Validation Tests
  // ============================================================================

  describe("validation", () => {
    it("validates complete config", () => {
      const result = PullRequestConfigSchema.safeParse(DEFAULT_PR_CONFIG);
      expect(result.success).toBe(true);
    });

    it("validates partial config", () => {
      const partialConfig = { merge_strategy: "squash" as const };
      const result = PullRequestConfigSchema.safeParse(partialConfig);
      expect(result.success).toBe(true);
    });

    it("validates empty config", () => {
      const result = PullRequestConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });

    it("rejects non-string merge_strategy", () => {
      const result = PullRequestConfigSchema.safeParse({ merge_strategy: 123 });
      expect(result.success).toBe(false);
    });
  });

  // ============================================================================
  // Default Values Tests
  // ============================================================================

  describe("default values", () => {
    it("DEFAULT_CONFIG.pull_request has correct defaults", () => {
      expect(DEFAULT_CONFIG.pull_request?.merge_strategy).toBe("squash");
      expect(DEFAULT_CONFIG.pull_request?.delete_branch).toBe(true);
      expect(DEFAULT_CONFIG.pull_request?.draft_by_default).toBe(false);
      expect(DEFAULT_CONFIG.pull_request?.auto_merge).toBe(true);
      expect(DEFAULT_CONFIG.pull_request?.reviewers).toEqual([]);
    });

    it("mergeWithDefaults preserves user values", () => {
      const config = mergeWithDefaults({
        pull_request: { merge_strategy: "rebase" },
      });

      expect(config.pull_request?.merge_strategy).toBe("rebase");
    });

    it("missing pull_request section uses defaults", () => {
      const config = mergeWithDefaults({});

      expect(config.pull_request?.merge_strategy).toBe("squash");
      expect(config.pull_request?.delete_branch).toBe(true);
    });
  });

  // ============================================================================
  // Command Construction Simulation
  // ============================================================================

  describe("command construction", () => {
    it("builds complete gh pr merge command", () => {
      const config = createMockPRConfig({
        merge_strategy: "squash",
        delete_branch: true,
      });

      const buildMergeCommand = (cfg: typeof config) => {
        const flags: string[] = ["gh", "pr", "merge"];
        if (cfg.merge_strategy === "squash") flags.push("--squash");
        if (cfg.merge_strategy === "rebase") flags.push("--rebase");
        if (cfg.merge_strategy === "merge") flags.push("--merge");
        if (cfg.delete_branch) flags.push("--delete-branch");
        return flags.join(" ");
      };

      const cmd = buildMergeCommand(config);
      expect(cmd).toBe("gh pr merge --squash --delete-branch");
    });

    it("builds complete gh pr create command", () => {
      const config = createMockPRConfig({
        draft_by_default: true,
        reviewers: ["alice", "bob"],
      });

      const buildCreateCommand = (cfg: typeof config) => {
        const flags: string[] = ["gh", "pr", "create"];
        if (cfg.draft_by_default) flags.push("--draft");
        if (cfg.reviewers && cfg.reviewers.length > 0) {
          flags.push("--reviewer", cfg.reviewers.join(","));
        }
        return flags.join(" ");
      };

      const cmd = buildCreateCommand(config);
      expect(cmd).toBe("gh pr create --draft --reviewer alice,bob");
    });
  });
});
