/**
 * IncrediYamlService Unit Tests
 *
 * Tests for YAML parsing, validation, and serialization functions.
 * Focuses on pure functions that don't require VSCode API mocking.
 */

import { describe, it, expect } from "vitest";
import {
  validateConfig,
  mergeWithDefaults,
  getConfigValue,
  setConfigValue,
} from "../../../src/views/settings/configUtils";
import type { IncrediConfig } from "../../../src/views/settings/types";
import { DEFAULT_CONFIG } from "../../../src/views/settings/types";

describe("IncrediYamlService", () => {
  describe("validateConfig", () => {
    it("should validate empty config as valid", () => {
      const result = validateConfig({});
      expect(result.valid).toBe(true);
      expect(result.errors).toHaveLength(0);
    });

    it("should validate config with all valid fields", () => {
      const config: IncrediConfig = {
        project: {
          number: 123,
          auto_dates: true,
          fields: {
            status: {
              id: "PVTSSF_status",
              options: {
                backlog: "option_backlog",
              },
            },
            priority: {
              id: "PVTSSF_priority",
              options: {
                p0: "option_p0",
              },
            },
            size: {
              id: "PVTSSF_size",
              options: {
                m: "option_m",
              },
            },
          },
        },
        pull_request: {
          merge_strategy: "squash",
          delete_branch: true,
          reviewers: ["user1", "user2"],
        },
        pipeline: {
          ci_timeout: 10,
          auto_fix: true,
        },
        validation: {
          max_files_changed: 50,
          max_lines_changed: 2000,
        },
        sanitization: {
          enabled: true,
          allowlist: ["allowed-pattern"],
          blocklist: ["blocked-pattern"],
        },
      };

      const result = validateConfig(config);
      expect(result.valid).toBe(true);
      expect(result.errors).toHaveLength(0);
    });

    it("should reject negative project number", () => {
      const config: IncrediConfig = {
        project: { number: -1 },
      };

      const result = validateConfig(config);
      expect(result.valid).toBe(false);
      expect(result.errors).toHaveLength(1);
      expect(result.errors[0].field).toBe("project.number");
    });

    it("should reject zero project number", () => {
      const config: IncrediConfig = {
        project: { number: 0 },
      };

      const result = validateConfig(config);
      expect(result.valid).toBe(false);
      expect(result.errors).toContainEqual(expect.objectContaining({ field: "project.number" }));
    });

    it("should reject invalid merge strategy", () => {
      const config = {
        pull_request: { merge_strategy: "invalid" as "squash" },
      };

      const result = validateConfig(config);
      expect(result.valid).toBe(false);
      expect(result.errors).toContainEqual(
        expect.objectContaining({ field: "pull_request.merge_strategy" })
      );
    });

    it("should accept all valid merge strategies", () => {
      for (const strategy of ["squash", "merge", "rebase"] as const) {
        const config: IncrediConfig = {
          pull_request: { merge_strategy: strategy },
        };

        const result = validateConfig(config);
        expect(result.valid).toBe(true);
      }
    });

    it("should reject negative ci_timeout", () => {
      const config: IncrediConfig = {
        pipeline: { ci_timeout: -5 },
      };

      const result = validateConfig(config);
      expect(result.valid).toBe(false);
      expect(result.errors).toContainEqual(
        expect.objectContaining({ field: "pipeline.ci_timeout" })
      );
    });

    it("should accept zero ci_timeout (0 means unlimited)", () => {
      const config: IncrediConfig = {
        pipeline: { ci_timeout: 0 },
      };

      const result = validateConfig(config);
      expect(result.valid).toBe(true);
    });

    it("should reject non-array reviewers", () => {
      const config = {
        pull_request: { reviewers: "not-an-array" as unknown as string[] },
      };

      const result = validateConfig(config);
      expect(result.valid).toBe(false);
      expect(result.errors).toContainEqual(
        expect.objectContaining({ field: "pull_request.reviewers" })
      );
    });

    it("should reject negative max_files_changed", () => {
      const config: IncrediConfig = {
        validation: { max_files_changed: -10 },
      };

      const result = validateConfig(config);
      expect(result.valid).toBe(false);
      expect(result.errors).toContainEqual(
        expect.objectContaining({ field: "validation.max_files_changed" })
      );
    });

    it("should reject negative max_lines_changed", () => {
      const config: IncrediConfig = {
        validation: { max_lines_changed: -100 },
      };

      const result = validateConfig(config);
      expect(result.valid).toBe(false);
    });

    it("should reject non-array allowlist", () => {
      const config = {
        sanitization: { allowlist: "not-an-array" as unknown as string[] },
      };

      const result = validateConfig(config);
      expect(result.valid).toBe(false);
      expect(result.errors).toContainEqual(
        expect.objectContaining({ field: "sanitization.allowlist" })
      );
    });

    it("should reject non-array blocklist", () => {
      const config = {
        sanitization: { blocklist: "not-an-array" as unknown as string[] },
      };

      const result = validateConfig(config);
      expect(result.valid).toBe(false);
    });

    it("should collect multiple validation errors", () => {
      const config = {
        project: { number: -1 },
        pull_request: { merge_strategy: "invalid" as "squash" },
        pipeline: { ci_timeout: -1 }, // Use -1 since 0 is valid
      };

      const result = validateConfig(config);
      expect(result.valid).toBe(false);
      expect(result.errors.length).toBeGreaterThanOrEqual(3);
    });
  });

  describe("mergeWithDefaults", () => {
    it("should return defaults for empty config", () => {
      const result = mergeWithDefaults({});
      expect(result).toEqual(DEFAULT_CONFIG);
    });

    it("should preserve user values over defaults", () => {
      const config: IncrediConfig = {
        project: { number: 42 },
      };

      const result = mergeWithDefaults(config);
      expect(result.project?.number).toBe(42);
      expect(result.project?.auto_dates).toBe(true); // From defaults
    });

    it("should deep merge nested objects", () => {
      const config: IncrediConfig = {
        branch: {
          base: "develop",
          // Other values should come from defaults
        },
      };

      const result = mergeWithDefaults(config);
      expect(result.branch?.base).toBe("develop");
      expect(result.branch?.protected).toEqual(["main", "master"]);
      expect(result.branch?.suggestions).toBe(true);
    });

    it("should not mutate the original config", () => {
      const config: IncrediConfig = {
        project: { number: 42 },
      };
      const original = JSON.parse(JSON.stringify(config));

      mergeWithDefaults(config);
      expect(config).toEqual(original);
    });

    it("should handle deeply nested merges", () => {
      const config: IncrediConfig = {
        branch: {
          prefixes: {
            feature: "feature/",
            // Other prefixes from defaults
          },
        },
      };

      const result = mergeWithDefaults(config);
      expect(result.branch?.prefixes?.feature).toBe("feature/");
      expect(result.branch?.prefixes?.bugfix).toBe("fix/");
    });

    it("should preserve arrays from user config", () => {
      const config: IncrediConfig = {
        branch: {
          protected: ["main", "develop", "staging"],
        },
      };

      const result = mergeWithDefaults(config);
      expect(result.branch?.protected).toEqual(["main", "develop", "staging"]);
    });
  });

  describe("getConfigValue", () => {
    it("should get top-level value", () => {
      const config: IncrediConfig = {
        project: { number: 123 },
      };

      const result = getConfigValue(config, "project");
      expect(result).toEqual({ number: 123 });
    });

    it("should get nested value", () => {
      const config: IncrediConfig = {
        project: { number: 123 },
      };

      const result = getConfigValue(config, "project.number");
      expect(result).toBe(123);
    });

    it("should get deeply nested value", () => {
      const config: IncrediConfig = {
        branch: {
          prefixes: {
            feature: "feat/",
          },
        },
      };

      const result = getConfigValue(config, "branch.prefixes.feature");
      expect(result).toBe("feat/");
    });

    it("should return undefined for missing path", () => {
      const config: IncrediConfig = {};

      const result = getConfigValue(config, "project.number");
      expect(result).toBeUndefined();
    });

    it("should return undefined for partially missing path", () => {
      const config: IncrediConfig = {
        project: {},
      };

      const result = getConfigValue(config, "project.fields.status");
      expect(result).toBeUndefined();
    });

    it("should handle null values in path", () => {
      const config = {
        project: null,
      } as unknown as IncrediConfig;

      const result = getConfigValue(config, "project.number");
      expect(result).toBeUndefined();
    });
  });

  describe("setConfigValue", () => {
    it("should set top-level value", () => {
      const config: IncrediConfig = {};

      setConfigValue(config, "project", { number: 123 });
      expect(config.project).toEqual({ number: 123 });
    });

    it("should set nested value", () => {
      const config: IncrediConfig = {
        project: {},
      };

      setConfigValue(config, "project.number", 456);
      expect(config.project?.number).toBe(456);
    });

    it("should create intermediate objects", () => {
      const config: IncrediConfig = {};

      setConfigValue(config, "branch.prefixes.feature", "feature/");
      expect(config.branch?.prefixes?.feature).toBe("feature/");
    });

    it("should preserve existing sibling values", () => {
      const config: IncrediConfig = {
        project: {
          number: 123,
          auto_dates: true,
        },
      };

      setConfigValue(config, "project.number", 456);
      expect(config.project?.number).toBe(456);
      expect(config.project?.auto_dates).toBe(true);
    });

    it("should handle setting array values", () => {
      const config: IncrediConfig = {};

      setConfigValue(config, "pull_request.reviewers", ["user1", "user2"]);
      expect(config.pull_request?.reviewers).toEqual(["user1", "user2"]);
    });

    it("should handle setting boolean values", () => {
      const config: IncrediConfig = {};

      setConfigValue(config, "sanitization.enabled", false);
      expect(config.sanitization?.enabled).toBe(false);
    });
  });

  describe("DEFAULT_CONFIG", () => {
    it("should have all required sections", () => {
      expect(DEFAULT_CONFIG.project).toBeDefined();
      expect(DEFAULT_CONFIG.pull_request).toBeDefined();
      expect(DEFAULT_CONFIG.branch).toBeDefined();
      expect(DEFAULT_CONFIG.issue).toBeDefined();
      expect(DEFAULT_CONFIG.pipeline).toBeDefined();
      expect(DEFAULT_CONFIG.validation).toBeDefined();
      expect(DEFAULT_CONFIG.sanitization).toBeDefined();
    });

    it("should have sensible default values", () => {
      expect(DEFAULT_CONFIG.pull_request?.merge_strategy).toBe("squash");
      expect(DEFAULT_CONFIG.branch?.base).toBe("main");
      expect(DEFAULT_CONFIG.pipeline?.auto_fix).toBe(true);
      expect(DEFAULT_CONFIG.sanitization?.enabled).toBe(true);
    });

    it("should be a valid configuration", () => {
      const result = validateConfig(DEFAULT_CONFIG);
      expect(result.valid).toBe(true);
    });
  });
});
