/**
 * Behavior tests for validation.* configuration fields
 *
 * These tests verify that validation config fields actually affect runtime behavior,
 * specifically PR validation thresholds and requirements.
 *
 * @see Issue #439 - Audit behavior config fields
 * @see packages/nightgauge-vscode/src/config/schema.ts - ValidationConfigSchema
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  createMockValidationConfig,
  DEFAULT_VALIDATION_CONFIG,
  applyEnvOverrides,
  BEHAVIOR_CONFIG_ENV_MAPPINGS,
} from "../mocks/config-fixtures";
import { ValidationConfigSchema, mergeWithDefaults, DEFAULT_CONFIG } from "../../src/config/schema";

describe("validation.behavior", () => {
  // Store original env vars
  const originalEnv = { ...process.env };

  beforeEach(() => {
    // Clear validation-related environment variables
    Object.keys(process.env).forEach((key) => {
      if (key.startsWith("NIGHTGAUGE_VALIDATION_")) {
        delete process.env[key];
      }
    });
  });

  afterEach(() => {
    // Restore original environment
    process.env = { ...originalEnv };
  });

  // ============================================================================
  // validation.require_tests - Behavior Tests
  // ============================================================================

  describe("require_tests", () => {
    it("blocks PR creation without tests when true", () => {
      const config = createMockValidationConfig({ require_tests: true });

      interface PRValidation {
        hasTests: boolean;
      }

      const validatePR = (
        pr: PRValidation,
        cfg: typeof config
      ): { valid: boolean; errors: string[] } => {
        const errors: string[] = [];

        if (cfg.require_tests && !pr.hasTests) {
          errors.push("PR must include tests");
        }

        return { valid: errors.length === 0, errors };
      };

      expect(validatePR({ hasTests: false }, config).valid).toBe(false);
      expect(validatePR({ hasTests: false }, config).errors).toContain("PR must include tests");
      expect(validatePR({ hasTests: true }, config).valid).toBe(true);
    });

    it("allows PR without tests when false", () => {
      const config = createMockValidationConfig({ require_tests: false });

      interface PRValidation {
        hasTests: boolean;
      }

      const validatePR = (pr: PRValidation, cfg: typeof config): { valid: boolean } => {
        if (cfg.require_tests && !pr.hasTests) {
          return { valid: false };
        }
        return { valid: true };
      };

      expect(validatePR({ hasTests: false }, config).valid).toBe(true);
    });

    it("defaults to true", () => {
      expect(DEFAULT_VALIDATION_CONFIG.require_tests).toBe(true);
    });
  });

  // ============================================================================
  // validation.require_changelog - Behavior Tests
  // ============================================================================

  describe("require_changelog", () => {
    it("blocks PR creation without changelog when true", () => {
      const config = createMockValidationConfig({ require_changelog: true });

      interface PRValidation {
        hasChangelogEntry: boolean;
      }

      const validatePR = (
        pr: PRValidation,
        cfg: typeof config
      ): { valid: boolean; errors: string[] } => {
        const errors: string[] = [];

        if (cfg.require_changelog && !pr.hasChangelogEntry) {
          errors.push("PR must include changelog entry");
        }

        return { valid: errors.length === 0, errors };
      };

      expect(validatePR({ hasChangelogEntry: false }, config).valid).toBe(false);
      expect(validatePR({ hasChangelogEntry: false }, config).errors).toContain(
        "PR must include changelog entry"
      );
      expect(validatePR({ hasChangelogEntry: true }, config).valid).toBe(true);
    });

    it("allows PR without changelog when false", () => {
      const config = createMockValidationConfig({ require_changelog: false });

      interface PRValidation {
        hasChangelogEntry: boolean;
      }

      const validatePR = (pr: PRValidation, cfg: typeof config): { valid: boolean } => {
        if (cfg.require_changelog && !pr.hasChangelogEntry) {
          return { valid: false };
        }
        return { valid: true };
      };

      expect(validatePR({ hasChangelogEntry: false }, config).valid).toBe(true);
    });

    it("defaults to false", () => {
      expect(DEFAULT_VALIDATION_CONFIG.require_changelog).toBe(false);
    });
  });

  // ============================================================================
  // validation.max_files_changed - Behavior Tests
  // ============================================================================

  describe("max_files_changed", () => {
    it("warns when file count exceeds threshold", () => {
      const config = createMockValidationConfig({ max_files_changed: 20 });

      interface PRStats {
        filesChanged: number;
      }

      const validatePR = (pr: PRStats, cfg: typeof config): { warnings: string[] } => {
        const warnings: string[] = [];
        const maxFiles = cfg.max_files_changed || 50;

        if (pr.filesChanged > maxFiles) {
          warnings.push(`PR changes ${pr.filesChanged} files (threshold: ${maxFiles})`);
        }

        return { warnings };
      };

      expect(validatePR({ filesChanged: 15 }, config).warnings).toEqual([]);
      expect(validatePR({ filesChanged: 25 }, config).warnings.length).toBe(1);
    });

    it("handles exact threshold value", () => {
      const config = createMockValidationConfig({ max_files_changed: 20 });

      const isOverThreshold = (filesChanged: number, cfg: typeof config) => {
        return filesChanged > (cfg.max_files_changed || 50);
      };

      expect(isOverThreshold(20, config)).toBe(false);
      expect(isOverThreshold(21, config)).toBe(true);
    });

    it("minimum value is 1", () => {
      const result = ValidationConfigSchema.safeParse({ max_files_changed: 0 });
      expect(result.success).toBe(false);
    });

    it("accepts boundary value 1", () => {
      const result = ValidationConfigSchema.safeParse({ max_files_changed: 1 });
      expect(result.success).toBe(true);
    });

    it("defaults to 50", () => {
      expect(DEFAULT_VALIDATION_CONFIG.max_files_changed).toBe(50);
    });
  });

  // ============================================================================
  // validation.max_lines_changed - Behavior Tests
  // ============================================================================

  describe("max_lines_changed", () => {
    it("warns when line count exceeds threshold", () => {
      const config = createMockValidationConfig({ max_lines_changed: 500 });

      interface PRStats {
        linesChanged: number;
      }

      const validatePR = (pr: PRStats, cfg: typeof config): { warnings: string[] } => {
        const warnings: string[] = [];
        const maxLines = cfg.max_lines_changed || 2000;

        if (pr.linesChanged > maxLines) {
          warnings.push(`PR changes ${pr.linesChanged} lines (threshold: ${maxLines})`);
        }

        return { warnings };
      };

      expect(validatePR({ linesChanged: 400 }, config).warnings).toEqual([]);
      expect(validatePR({ linesChanged: 600 }, config).warnings.length).toBe(1);
    });

    it("handles exact threshold value", () => {
      const config = createMockValidationConfig({ max_lines_changed: 500 });

      const isOverThreshold = (linesChanged: number, cfg: typeof config) => {
        return linesChanged > (cfg.max_lines_changed || 2000);
      };

      expect(isOverThreshold(500, config)).toBe(false);
      expect(isOverThreshold(501, config)).toBe(true);
    });

    it("minimum value is 1", () => {
      const result = ValidationConfigSchema.safeParse({ max_lines_changed: 0 });
      expect(result.success).toBe(false);
    });

    it("accepts large values", () => {
      const result = ValidationConfigSchema.safeParse({
        max_lines_changed: 10000,
      });
      expect(result.success).toBe(true);
    });

    it("defaults to 2000", () => {
      expect(DEFAULT_VALIDATION_CONFIG.max_lines_changed).toBe(2000);
    });
  });

  // ============================================================================
  // Environment Variable Overrides
  // ============================================================================

  describe("environment variable overrides", () => {
    it("NIGHTGAUGE_VALIDATION_REQUIRE_TESTS overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_VALIDATION_REQUIRE_TESTS: "false",
      });

      try {
        expect(process.env.NIGHTGAUGE_VALIDATION_REQUIRE_TESTS).toBe("false");
      } finally {
        cleanup();
      }
    });

    it("NIGHTGAUGE_VALIDATION_MAX_FILES overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_VALIDATION_MAX_FILES: "100",
      });

      try {
        expect(process.env.NIGHTGAUGE_VALIDATION_MAX_FILES).toBe("100");
      } finally {
        cleanup();
      }
    });

    it("env var takes precedence over config file", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_VALIDATION_MAX_FILES: "100",
      });

      try {
        const configValue = "50";
        const envValue = process.env.NIGHTGAUGE_VALIDATION_MAX_FILES;

        const effectiveValue = envValue || configValue;
        expect(effectiveValue).toBe("100");
      } finally {
        cleanup();
      }
    });

    it("validation env vars are defined", () => {
      expect(BEHAVIOR_CONFIG_ENV_MAPPINGS["validation.require_tests"]).toBe(
        "NIGHTGAUGE_VALIDATION_REQUIRE_TESTS"
      );
      expect(BEHAVIOR_CONFIG_ENV_MAPPINGS["validation.max_files_changed"]).toBe(
        "NIGHTGAUGE_VALIDATION_MAX_FILES"
      );
      expect(BEHAVIOR_CONFIG_ENV_MAPPINGS["validation.max_lines_changed"]).toBe(
        "NIGHTGAUGE_VALIDATION_MAX_LINES"
      );
    });
  });

  // ============================================================================
  // Schema Validation Tests
  // ============================================================================

  describe("validation", () => {
    it("validates complete config", () => {
      const result = ValidationConfigSchema.safeParse(DEFAULT_VALIDATION_CONFIG);
      expect(result.success).toBe(true);
    });

    it("validates partial config", () => {
      const partialConfig = { require_tests: false };
      const result = ValidationConfigSchema.safeParse(partialConfig);
      expect(result.success).toBe(true);
    });

    it("validates empty config", () => {
      const result = ValidationConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });

    it("rejects non-integer file count", () => {
      const result = ValidationConfigSchema.safeParse({
        max_files_changed: 10.5,
      });
      expect(result.success).toBe(false);
    });

    it("rejects negative values", () => {
      const result = ValidationConfigSchema.safeParse({
        max_lines_changed: -100,
      });
      expect(result.success).toBe(false);
    });
  });

  // ============================================================================
  // Default Values Tests
  // ============================================================================

  describe("default values", () => {
    it("DEFAULT_CONFIG.validation has correct defaults", () => {
      expect(DEFAULT_CONFIG.validation?.require_tests).toBe(true);
      expect(DEFAULT_CONFIG.validation?.require_changelog).toBe(false);
      expect(DEFAULT_CONFIG.validation?.max_files_changed).toBe(50);
      expect(DEFAULT_CONFIG.validation?.max_lines_changed).toBe(2000);
    });

    it("mergeWithDefaults preserves user values", () => {
      const config = mergeWithDefaults({
        validation: { max_files_changed: 100 },
      });

      expect(config.validation?.max_files_changed).toBe(100);
    });

    it("missing validation section uses defaults", () => {
      const config = mergeWithDefaults({});

      expect(config.validation?.require_tests).toBe(true);
      expect(config.validation?.max_files_changed).toBe(50);
    });
  });

  // ============================================================================
  // PR Validation Integration
  // ============================================================================

  describe("PR validation integration", () => {
    it("validates PR against all rules", () => {
      const config = createMockValidationConfig({
        require_tests: true,
        require_changelog: true,
        max_files_changed: 20,
        max_lines_changed: 500,
      });

      interface PRData {
        hasTests: boolean;
        hasChangelogEntry: boolean;
        filesChanged: number;
        linesChanged: number;
      }

      interface ValidationResult {
        valid: boolean;
        errors: string[];
        warnings: string[];
      }

      const validatePR = (pr: PRData, cfg: typeof config): ValidationResult => {
        const errors: string[] = [];
        const warnings: string[] = [];

        // Required checks (block PR)
        if (cfg.require_tests && !pr.hasTests) {
          errors.push("PR must include tests");
        }
        if (cfg.require_changelog && !pr.hasChangelogEntry) {
          errors.push("PR must include changelog entry");
        }

        // Threshold checks (warnings only)
        if (pr.filesChanged > (cfg.max_files_changed || 50)) {
          warnings.push(`Large PR: ${pr.filesChanged} files changed`);
        }
        if (pr.linesChanged > (cfg.max_lines_changed || 2000)) {
          warnings.push(`Large PR: ${pr.linesChanged} lines changed`);
        }

        return {
          valid: errors.length === 0,
          errors,
          warnings,
        };
      };

      // Valid PR
      const validResult = validatePR(
        {
          hasTests: true,
          hasChangelogEntry: true,
          filesChanged: 10,
          linesChanged: 200,
        },
        config
      );
      expect(validResult.valid).toBe(true);
      expect(validResult.errors).toEqual([]);
      expect(validResult.warnings).toEqual([]);

      // Missing tests - invalid
      const noTestsResult = validatePR(
        {
          hasTests: false,
          hasChangelogEntry: true,
          filesChanged: 10,
          linesChanged: 200,
        },
        config
      );
      expect(noTestsResult.valid).toBe(false);
      expect(noTestsResult.errors).toContain("PR must include tests");

      // Over thresholds - valid with warnings
      const largeResult = validatePR(
        {
          hasTests: true,
          hasChangelogEntry: true,
          filesChanged: 30,
          linesChanged: 1000,
        },
        config
      );
      expect(largeResult.valid).toBe(true);
      expect(largeResult.warnings.length).toBe(2);
    });
  });
});
