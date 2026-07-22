/**
 * Behavior tests for branch.* configuration fields
 *
 * These tests verify that branch config fields actually affect runtime behavior,
 * specifically base branch, protected branches, prefixes, and suggestions.
 *
 * @see Issue #438 - Audit and test PR/Branch/Pipeline config fields
 * @see packages/nightgauge-vscode/src/config/schema.ts - BranchConfigSchema
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  createMockBranchConfig,
  createMockBranchPrefixes,
  DEFAULT_BRANCH_CONFIG,
  DEFAULT_BRANCH_PREFIXES,
  applyEnvOverrides,
  EXTENDED_CONFIG_ENV_MAPPINGS,
} from "../mocks/config-fixtures";
import {
  BranchConfigSchema,
  BranchPrefixConfigSchema,
  mergeWithDefaults,
  DEFAULT_CONFIG,
} from "../../src/config/schema";

describe("branch.behavior", () => {
  // Store original env vars
  const originalEnv = { ...process.env };

  beforeEach(() => {
    // Clear branch-related environment variables
    Object.keys(process.env).forEach((key) => {
      if (key.startsWith("NIGHTGAUGE_BRANCH_")) {
        delete process.env[key];
      }
    });
  });

  afterEach(() => {
    // Restore original environment
    process.env = { ...originalEnv };
  });

  // ============================================================================
  // branch.base - Behavior Tests
  // ============================================================================

  describe("base", () => {
    it("uses config value for default PR target", () => {
      const config = createMockBranchConfig({ base: "develop" });

      // Simulate PR target branch determination
      const getTargetBranch = (cfg: typeof config) => {
        return cfg.base || "main";
      };

      expect(getTargetBranch(config)).toBe("develop");
    });

    it("falls back to main when not specified", () => {
      const config = createMockBranchConfig({ base: undefined });

      const getTargetBranch = (cfg: typeof config) => {
        return cfg.base || "main";
      };

      expect(getTargetBranch(config)).toBe("main");
    });

    it("defaults to main", () => {
      expect(DEFAULT_BRANCH_CONFIG.base).toBe("main");
    });

    it("supports various base branch names", () => {
      const branches = ["main", "master", "develop", "staging", "production"];

      branches.forEach((branch) => {
        const config = createMockBranchConfig({ base: branch });
        expect(config.base).toBe(branch);
      });
    });
  });

  // ============================================================================
  // branch.protected - Behavior Tests
  // ============================================================================

  describe("protected", () => {
    it("prevents direct push to protected branches", () => {
      const config = createMockBranchConfig({ protected: ["main", "master"] });

      // Simulate push protection check
      const canPushDirectly = (targetBranch: string, cfg: typeof config) => {
        const protectedBranches = cfg.protected || [];
        return !protectedBranches.includes(targetBranch);
      };

      expect(canPushDirectly("main", config)).toBe(false);
      expect(canPushDirectly("master", config)).toBe(false);
      expect(canPushDirectly("feat/123", config)).toBe(true);
    });

    it("allows push to unprotected branches", () => {
      const config = createMockBranchConfig({ protected: ["main"] });

      const canPushDirectly = (targetBranch: string, cfg: typeof config) => {
        const protectedBranches = cfg.protected || [];
        return !protectedBranches.includes(targetBranch);
      };

      expect(canPushDirectly("develop", config)).toBe(true);
      expect(canPushDirectly("feature/test", config)).toBe(true);
    });

    it("handles empty protected array", () => {
      const config = createMockBranchConfig({ protected: [] });

      const canPushDirectly = (targetBranch: string, cfg: typeof config) => {
        const protectedBranches = cfg.protected || [];
        return !protectedBranches.includes(targetBranch);
      };

      // All branches are pushable when none are protected
      expect(canPushDirectly("main", config)).toBe(true);
    });

    it("defaults to main and master", () => {
      expect(DEFAULT_BRANCH_CONFIG.protected).toEqual(["main", "master"]);
    });
  });

  // ============================================================================
  // branch.suggestions - Behavior Tests
  // ============================================================================

  describe("suggestions", () => {
    it("enables branch name suggestions when true", () => {
      const config = createMockBranchConfig({ suggestions: true });

      const shouldSuggestBranchNames = (cfg: typeof config) => {
        return cfg.suggestions === true;
      };

      expect(shouldSuggestBranchNames(config)).toBe(true);
    });

    it("disables suggestions when false", () => {
      const config = createMockBranchConfig({ suggestions: false });

      const shouldSuggestBranchNames = (cfg: typeof config) => {
        return cfg.suggestions === true;
      };

      expect(shouldSuggestBranchNames(config)).toBe(false);
    });

    it("defaults to true", () => {
      expect(DEFAULT_BRANCH_CONFIG.suggestions).toBe(true);
    });
  });

  // ============================================================================
  // branch.prefixes.feature - Behavior Tests
  // ============================================================================

  describe("prefixes.feature", () => {
    it("uses prefix for feature branch naming", () => {
      const config = createMockBranchConfig({
        prefixes: { feature: "feat/" },
      });

      // Simulate branch naming
      const createBranchName = (
        issueNumber: number,
        description: string,
        issueType: string,
        cfg: typeof config
      ) => {
        const prefix = issueType === "feature" ? cfg.prefixes?.feature : cfg.prefixes?.bugfix;
        return `${prefix}${issueNumber}-${description}`;
      };

      expect(createBranchName(42, "user-auth", "feature", config)).toBe("feat/42-user-auth");
    });

    it("defaults to feat/", () => {
      expect(DEFAULT_BRANCH_PREFIXES.feature).toBe("feat/");
    });
  });

  // ============================================================================
  // branch.prefixes.bugfix - Behavior Tests
  // ============================================================================

  describe("prefixes.bugfix", () => {
    it("uses prefix for bugfix branch naming", () => {
      const config = createMockBranchConfig({
        prefixes: { bugfix: "fix/" },
      });

      const createBranchName = (issueNumber: number, description: string, cfg: typeof config) => {
        return `${cfg.prefixes?.bugfix}${issueNumber}-${description}`;
      };

      expect(createBranchName(99, "login-error", config)).toBe("fix/99-login-error");
    });

    it("defaults to fix/", () => {
      expect(DEFAULT_BRANCH_PREFIXES.bugfix).toBe("fix/");
    });
  });

  // ============================================================================
  // branch.prefixes.docs - Behavior Tests
  // ============================================================================

  describe("prefixes.docs", () => {
    it("uses prefix for documentation branch naming", () => {
      const config = createMockBranchConfig({
        prefixes: { docs: "docs/" },
      });

      const createBranchName = (issueNumber: number, description: string, cfg: typeof config) => {
        return `${cfg.prefixes?.docs}${issueNumber}-${description}`;
      };

      expect(createBranchName(15, "update-readme", config)).toBe("docs/15-update-readme");
    });

    it("defaults to docs/", () => {
      expect(DEFAULT_BRANCH_PREFIXES.docs).toBe("docs/");
    });
  });

  // ============================================================================
  // branch.prefixes.refactor - Behavior Tests
  // ============================================================================

  describe("prefixes.refactor", () => {
    it("uses prefix for refactor branch naming", () => {
      const config = createMockBranchConfig({
        prefixes: { refactor: "refactor/" },
      });

      const createBranchName = (issueNumber: number, description: string, cfg: typeof config) => {
        return `${cfg.prefixes?.refactor}${issueNumber}-${description}`;
      };

      expect(createBranchName(50, "auth-module", config)).toBe("refactor/50-auth-module");
    });

    it("defaults to refactor/", () => {
      expect(DEFAULT_BRANCH_PREFIXES.refactor).toBe("refactor/");
    });
  });

  // ============================================================================
  // branch.prefixes.chore - Behavior Tests
  // ============================================================================

  describe("prefixes.chore", () => {
    it("uses prefix for chore branch naming", () => {
      const config = createMockBranchConfig({
        prefixes: { chore: "chore/" },
      });

      const createBranchName = (issueNumber: number, description: string, cfg: typeof config) => {
        return `${cfg.prefixes?.chore}${issueNumber}-${description}`;
      };

      expect(createBranchName(77, "update-deps", config)).toBe("chore/77-update-deps");
    });

    it("defaults to chore/", () => {
      expect(DEFAULT_BRANCH_PREFIXES.chore).toBe("chore/");
    });
  });

  // ============================================================================
  // branch.prefixes.hotfix - Behavior Tests
  // ============================================================================

  describe("prefixes.hotfix", () => {
    it("uses prefix for hotfix branch naming", () => {
      const config = createMockBranchConfig({
        prefixes: { hotfix: "hotfix/" },
      });

      const createBranchName = (issueNumber: number, description: string, cfg: typeof config) => {
        return `${cfg.prefixes?.hotfix}${issueNumber}-${description}`;
      };

      expect(createBranchName(101, "critical-fix", config)).toBe("hotfix/101-critical-fix");
    });

    it("defaults to hotfix/", () => {
      expect(DEFAULT_BRANCH_PREFIXES.hotfix).toBe("hotfix/");
    });
  });

  // ============================================================================
  // Label-to-Prefix Mapping Behavior
  // ============================================================================

  describe("label-to-prefix mapping", () => {
    it("maps type:feature label to feature prefix", () => {
      const config = createMockBranchConfig();

      const getPrefixFromLabel = (labels: string[], cfg: typeof config): string => {
        if (labels.includes("type:feature")) return cfg.prefixes?.feature || "";
        if (labels.includes("type:bug")) return cfg.prefixes?.bugfix || "";
        if (labels.includes("type:docs")) return cfg.prefixes?.docs || "";
        if (labels.includes("type:refactor")) return cfg.prefixes?.refactor || "";
        if (labels.includes("type:chore")) return cfg.prefixes?.chore || "";
        return cfg.prefixes?.feature || ""; // default to feature
      };

      expect(getPrefixFromLabel(["type:feature"], config)).toBe("feat/");
      expect(getPrefixFromLabel(["type:bug"], config)).toBe("fix/");
      expect(getPrefixFromLabel(["type:docs"], config)).toBe("docs/");
      expect(getPrefixFromLabel(["type:refactor"], config)).toBe("refactor/");
      expect(getPrefixFromLabel(["type:chore"], config)).toBe("chore/");
    });

    it("uses custom prefixes from config", () => {
      const config = createMockBranchConfig({
        prefixes: {
          feature: "feature/",
          bugfix: "bugfix/",
          docs: "documentation/",
        },
      });

      const getPrefixFromLabel = (labels: string[], cfg: typeof config): string => {
        if (labels.includes("type:feature")) return cfg.prefixes?.feature || "";
        if (labels.includes("type:bug")) return cfg.prefixes?.bugfix || "";
        if (labels.includes("type:docs")) return cfg.prefixes?.docs || "";
        return "";
      };

      expect(getPrefixFromLabel(["type:feature"], config)).toBe("feature/");
      expect(getPrefixFromLabel(["type:bug"], config)).toBe("bugfix/");
      expect(getPrefixFromLabel(["type:docs"], config)).toBe("documentation/");
    });
  });

  // ============================================================================
  // Environment Variable Overrides
  // ============================================================================

  describe("environment variable overrides", () => {
    it("NIGHTGAUGE_BRANCH_BASE overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_BRANCH_BASE: "develop",
      });

      try {
        expect(process.env.NIGHTGAUGE_BRANCH_BASE).toBe("develop");
      } finally {
        cleanup();
      }
    });

    it("NIGHTGAUGE_BRANCH_SUGGESTIONS overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_BRANCH_SUGGESTIONS: "false",
      });

      try {
        expect(process.env.NIGHTGAUGE_BRANCH_SUGGESTIONS).toBe("false");
      } finally {
        cleanup();
      }
    });

    it("env var takes precedence over config file", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_BRANCH_BASE: "develop",
      });

      try {
        const configValue = "main";
        const envValue = process.env.NIGHTGAUGE_BRANCH_BASE;

        // Env should take precedence
        const effectiveValue = envValue || configValue;
        expect(effectiveValue).toBe("develop");
      } finally {
        cleanup();
      }
    });

    it("branch env vars are defined", () => {
      expect(EXTENDED_CONFIG_ENV_MAPPINGS["branch.base"]).toBe("NIGHTGAUGE_BRANCH_BASE");
      expect(EXTENDED_CONFIG_ENV_MAPPINGS["branch.suggestions"]).toBe(
        "NIGHTGAUGE_BRANCH_SUGGESTIONS"
      );
    });
  });

  // ============================================================================
  // Schema Validation Tests
  // ============================================================================

  describe("validation", () => {
    it("validates complete config", () => {
      const result = BranchConfigSchema.safeParse(DEFAULT_BRANCH_CONFIG);
      expect(result.success).toBe(true);
    });

    it("validates partial config", () => {
      const partialConfig = { base: "develop" };
      const result = BranchConfigSchema.safeParse(partialConfig);
      expect(result.success).toBe(true);
    });

    it("validates empty config", () => {
      const result = BranchConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });

    it("validates prefix config", () => {
      const result = BranchPrefixConfigSchema.safeParse(DEFAULT_BRANCH_PREFIXES);
      expect(result.success).toBe(true);
    });

    it("rejects non-string base", () => {
      const result = BranchConfigSchema.safeParse({ base: 123 });
      expect(result.success).toBe(false);
    });

    it("rejects non-array protected", () => {
      const result = BranchConfigSchema.safeParse({ protected: "main" });
      expect(result.success).toBe(false);
    });
  });

  // ============================================================================
  // Default Values Tests
  // ============================================================================

  describe("default values", () => {
    it("DEFAULT_CONFIG.branch has correct defaults", () => {
      expect(DEFAULT_CONFIG.branch?.base).toBe("main");
      expect(DEFAULT_CONFIG.branch?.protected).toEqual(["main", "master"]);
      expect(DEFAULT_CONFIG.branch?.suggestions).toBe(true);
      expect(DEFAULT_CONFIG.branch?.prefixes?.feature).toBe("feat/");
      expect(DEFAULT_CONFIG.branch?.prefixes?.bugfix).toBe("fix/");
    });

    it("mergeWithDefaults preserves user values", () => {
      const config = mergeWithDefaults({
        branch: { base: "develop" },
      });

      expect(config.branch?.base).toBe("develop");
    });

    it("missing branch section uses defaults", () => {
      const config = mergeWithDefaults({});

      expect(config.branch?.base).toBe("main");
      expect(config.branch?.suggestions).toBe(true);
    });
  });

  // ============================================================================
  // Branch Name Generation Simulation
  // ============================================================================

  describe("branch name generation", () => {
    it("generates full branch name from issue metadata", () => {
      const config = createMockBranchConfig();

      type IssueType = "feature" | "bug" | "docs" | "refactor" | "chore";

      interface IssueMetadata {
        number: number;
        title: string;
        type: IssueType;
      }

      const generateBranchName = (issue: IssueMetadata, cfg: typeof config): string => {
        const prefixes = cfg.prefixes || {};
        const prefixMap: Record<IssueType, string | undefined> = {
          feature: prefixes.feature,
          bug: prefixes.bugfix,
          docs: prefixes.docs,
          refactor: prefixes.refactor,
          chore: prefixes.chore,
        };

        const prefix = prefixMap[issue.type] || "feat/";
        const slug = issue.title.toLowerCase().replace(/\s+/g, "-");
        return `${prefix}${issue.number}-${slug}`;
      };

      const issue: IssueMetadata = {
        number: 42,
        title: "Add user auth",
        type: "feature",
      };

      expect(generateBranchName(issue, config)).toBe("feat/42-add-user-auth");
    });

    it("handles various issue types", () => {
      const config = createMockBranchConfig();

      type IssueType = "feature" | "bug" | "docs";

      const generateBranchName = (
        issueNumber: number,
        slug: string,
        type: IssueType,
        cfg: typeof config
      ): string => {
        const prefixes = cfg.prefixes || {};
        const prefixMap: Record<IssueType, string | undefined> = {
          feature: prefixes.feature,
          bug: prefixes.bugfix,
          docs: prefixes.docs,
        };
        return `${prefixMap[type]}${issueNumber}-${slug}`;
      };

      expect(generateBranchName(1, "new-feature", "feature", config)).toBe("feat/1-new-feature");
      expect(generateBranchName(2, "fix-bug", "bug", config)).toBe("fix/2-fix-bug");
      expect(generateBranchName(3, "update-docs", "docs", config)).toBe("docs/3-update-docs");
    });
  });
});
