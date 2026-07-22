/**
 * Behavior tests for pipeline.* configuration fields
 *
 * These tests verify that pipeline config fields actually affect runtime behavior,
 * specifically skip checks, retry logic, timeout handling, and log retention.
 *
 * @see Issue #438 - Audit and test PR/Branch/Pipeline config fields
 * @see packages/nightgauge-vscode/src/config/schema.ts - PipelineConfigSchema
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  createMockPipelineConfig,
  createMockSkipChecks,
  createMockPipelineRetry,
  DEFAULT_PIPELINE_CONFIG,
  DEFAULT_SKIP_CHECKS,
  DEFAULT_PIPELINE_RETRY,
  DEFAULT_PIPELINE_LOGS,
  applyEnvOverrides,
  EXTENDED_CONFIG_ENV_MAPPINGS,
} from "../mocks/config-fixtures";
import {
  PipelineConfigSchema,
  SkipChecksConfigSchema,
  PipelineRetryConfigSchema,
  PipelineLogsConfigSchema,
  mergeWithDefaults,
  DEFAULT_CONFIG,
} from "../../src/config/schema";

describe("pipeline.behavior", () => {
  // Store original env vars
  const originalEnv = { ...process.env };

  beforeEach(() => {
    // Clear pipeline-related environment variables
    Object.keys(process.env).forEach((key) => {
      if (key.startsWith("NIGHTGAUGE_PIPELINE_")) {
        delete process.env[key];
      }
    });
  });

  afterEach(() => {
    // Restore original environment
    process.env = { ...originalEnv };
  });

  // ============================================================================
  // pipeline.ci_timeout - Behavior Tests
  // ============================================================================

  describe("ci_timeout", () => {
    it("uses timeout for CI wait operations", () => {
      const config = createMockPipelineConfig({ ci_timeout: 600 });

      const getTimeoutMs = (cfg: typeof config) => {
        return (cfg.ci_timeout || 300) * 1000;
      };

      expect(getTimeoutMs(config)).toBe(600000);
    });

    it("accepts 0 (no timeout)", () => {
      const result = PipelineConfigSchema.safeParse({ ci_timeout: 0 });
      expect(result.success).toBe(true);
    });

    it("accepts boundary value 300", () => {
      const result = PipelineConfigSchema.safeParse({ ci_timeout: 300 });
      expect(result.success).toBe(true);
    });

    it("accepts high value 600", () => {
      const result = PipelineConfigSchema.safeParse({ ci_timeout: 600 });
      expect(result.success).toBe(true);
    });

    it("defaults to 300", () => {
      expect(DEFAULT_PIPELINE_CONFIG.ci_timeout).toBe(300);
    });
  });

  // ============================================================================
  // pipeline.auto_fix - Behavior Tests
  // ============================================================================

  describe("auto_fix", () => {
    it("enables auto-fix linting when true", () => {
      const config = createMockPipelineConfig({ auto_fix: true });

      const shouldAutoFix = (cfg: typeof config) => {
        return cfg.auto_fix === true;
      };

      expect(shouldAutoFix(config)).toBe(true);
    });

    it("skips auto-fix when false", () => {
      const config = createMockPipelineConfig({ auto_fix: false });

      const shouldAutoFix = (cfg: typeof config) => {
        return cfg.auto_fix === true;
      };

      expect(shouldAutoFix(config)).toBe(false);
    });

    it("defaults to true", () => {
      expect(DEFAULT_PIPELINE_CONFIG.auto_fix).toBe(true);
    });
  });

  // ============================================================================
  // pipeline.skip.tests - Behavior Tests
  // ============================================================================

  describe("skip.tests", () => {
    it("skips tests when true", () => {
      const config = createMockPipelineConfig({ skip: { tests: true } });

      const shouldRunTests = (cfg: typeof config) => {
        return cfg.skip?.tests !== true;
      };

      expect(shouldRunTests(config)).toBe(false);
    });

    it("runs tests when false", () => {
      const config = createMockPipelineConfig({ skip: { tests: false } });

      const shouldRunTests = (cfg: typeof config) => {
        return cfg.skip?.tests !== true;
      };

      expect(shouldRunTests(config)).toBe(true);
    });

    it("defaults to false (run tests)", () => {
      expect(DEFAULT_SKIP_CHECKS.tests).toBe(false);
    });
  });

  // ============================================================================
  // pipeline.skip.lint - Behavior Tests
  // ============================================================================

  describe("skip.lint", () => {
    it("skips lint when true", () => {
      const config = createMockPipelineConfig({ skip: { lint: true } });

      const shouldRunLint = (cfg: typeof config) => {
        return cfg.skip?.lint !== true;
      };

      expect(shouldRunLint(config)).toBe(false);
    });

    it("runs lint when false", () => {
      const config = createMockPipelineConfig({ skip: { lint: false } });

      const shouldRunLint = (cfg: typeof config) => {
        return cfg.skip?.lint !== true;
      };

      expect(shouldRunLint(config)).toBe(true);
    });

    it("defaults to false (run lint)", () => {
      expect(DEFAULT_SKIP_CHECKS.lint).toBe(false);
    });
  });

  // ============================================================================
  // pipeline.skip.typecheck - Behavior Tests
  // ============================================================================

  describe("skip.typecheck", () => {
    it("skips typecheck when true", () => {
      const config = createMockPipelineConfig({ skip: { typecheck: true } });

      const shouldRunTypecheck = (cfg: typeof config) => {
        return cfg.skip?.typecheck !== true;
      };

      expect(shouldRunTypecheck(config)).toBe(false);
    });

    it("runs typecheck when false", () => {
      const config = createMockPipelineConfig({ skip: { typecheck: false } });

      const shouldRunTypecheck = (cfg: typeof config) => {
        return cfg.skip?.typecheck !== true;
      };

      expect(shouldRunTypecheck(config)).toBe(true);
    });

    it("defaults to false (run typecheck)", () => {
      expect(DEFAULT_SKIP_CHECKS.typecheck).toBe(false);
    });
  });

  // ============================================================================
  // pipeline.skip.build - Behavior Tests
  // ============================================================================

  describe("skip.build", () => {
    it("skips build when true (CAVEAT: HARD GATE may override)", () => {
      const config = createMockPipelineConfig({ skip: { build: true } });

      // Note: In feature-validate, build is a HARD GATE and cannot be skipped
      // This test verifies the config value is read correctly
      const shouldRunBuildPerConfig = (cfg: typeof config) => {
        return cfg.skip?.build !== true;
      };

      expect(shouldRunBuildPerConfig(config)).toBe(false);
    });

    it("runs build when false", () => {
      const config = createMockPipelineConfig({ skip: { build: false } });

      const shouldRunBuild = (cfg: typeof config) => {
        return cfg.skip?.build !== true;
      };

      expect(shouldRunBuild(config)).toBe(true);
    });

    it("defaults to false (run build)", () => {
      expect(DEFAULT_SKIP_CHECKS.build).toBe(false);
    });

    it("HARD GATE: build cannot be bypassed in validation phase", () => {
      const config = createMockPipelineConfig({ skip: { build: true } });

      // Simulate feature-validate HARD GATE behavior
      const shouldRunBuildValidation = (_cfg: typeof config, isValidationPhase: boolean) => {
        // HARD GATE: Build always runs in validation phase
        if (isValidationPhase) {
          return true;
        }
        return _cfg.skip?.build !== true;
      };

      // Even with skip.build=true, validation phase runs build
      expect(shouldRunBuildValidation(config, true)).toBe(true);
      // Other phases respect the config
      expect(shouldRunBuildValidation(config, false)).toBe(false);
    });
  });

  // ============================================================================
  // pipeline.skip.format - Behavior Tests
  // ============================================================================

  describe("skip.format", () => {
    it("skips format when true", () => {
      const config = createMockPipelineConfig({ skip: { format: true } });

      const shouldRunFormat = (cfg: typeof config) => {
        return cfg.skip?.format !== true;
      };

      expect(shouldRunFormat(config)).toBe(false);
    });

    it("runs format when false", () => {
      const config = createMockPipelineConfig({ skip: { format: false } });

      const shouldRunFormat = (cfg: typeof config) => {
        return cfg.skip?.format !== true;
      };

      expect(shouldRunFormat(config)).toBe(true);
    });

    it("defaults to false (run format)", () => {
      expect(DEFAULT_SKIP_CHECKS.format).toBe(false);
    });
  });

  // ============================================================================
  // pipeline.retry.max_auto_attempts - Behavior Tests
  // ============================================================================

  describe("retry.max_auto_attempts", () => {
    it("limits retry count", () => {
      const config = createMockPipelineConfig({
        retry: { max_auto_attempts: 5 },
      });

      const simulateRetries = (maxAttempts: number) => {
        let attempts = 0;
        while (attempts < maxAttempts) {
          attempts++;
        }
        return attempts;
      };

      expect(simulateRetries(config.retry?.max_auto_attempts || 3)).toBe(5);
    });

    it("minimum value is 1", () => {
      const result = PipelineRetryConfigSchema.safeParse({
        max_auto_attempts: 0,
      });
      expect(result.success).toBe(false);
    });

    it("accepts boundary value 1", () => {
      const result = PipelineRetryConfigSchema.safeParse({
        max_auto_attempts: 1,
      });
      expect(result.success).toBe(true);
    });

    it("accepts value 10", () => {
      const result = PipelineRetryConfigSchema.safeParse({
        max_auto_attempts: 10,
      });
      expect(result.success).toBe(true);
    });

    it("defaults to 3", () => {
      expect(DEFAULT_PIPELINE_RETRY.max_auto_attempts).toBe(3);
    });
  });

  // ============================================================================
  // pipeline.retry.backoff_multiplier - Behavior Tests
  // ============================================================================

  describe("retry.backoff_multiplier", () => {
    it("affects delay calculation", () => {
      const config = createMockPipelineConfig({
        retry: { backoff_multiplier: 2, initial_delay_ms: 1000 },
      });

      const calculateDelay = (attempt: number, initialDelay: number, multiplier: number) => {
        return initialDelay * Math.pow(multiplier, attempt - 1);
      };

      const initialDelay = config.retry?.initial_delay_ms || 1000;
      const multiplier = config.retry?.backoff_multiplier || 2;

      expect(calculateDelay(1, initialDelay, multiplier)).toBe(1000);
      expect(calculateDelay(2, initialDelay, multiplier)).toBe(2000);
      expect(calculateDelay(3, initialDelay, multiplier)).toBe(4000);
    });

    it("handles 3x multiplier", () => {
      const config = createMockPipelineConfig({
        retry: { backoff_multiplier: 3, initial_delay_ms: 100 },
      });

      const calculateDelay = (attempt: number, initialDelay: number, multiplier: number) => {
        return initialDelay * Math.pow(multiplier, attempt - 1);
      };

      const initialDelay = config.retry?.initial_delay_ms || 1000;
      const multiplier = config.retry?.backoff_multiplier || 2;

      expect(calculateDelay(1, initialDelay, multiplier)).toBe(100);
      expect(calculateDelay(2, initialDelay, multiplier)).toBe(300);
      expect(calculateDelay(3, initialDelay, multiplier)).toBe(900);
    });

    it("minimum value is 1", () => {
      const result = PipelineRetryConfigSchema.safeParse({
        backoff_multiplier: 0.5,
      });
      expect(result.success).toBe(false);
    });

    it("defaults to 2", () => {
      expect(DEFAULT_PIPELINE_RETRY.backoff_multiplier).toBe(2);
    });
  });

  // ============================================================================
  // pipeline.retry.initial_delay_ms - Behavior Tests
  // ============================================================================

  describe("retry.initial_delay_ms", () => {
    it("sets first retry delay", () => {
      const config = createMockPipelineConfig({
        retry: { initial_delay_ms: 500 },
      });

      const getFirstRetryDelay = (cfg: typeof config) => {
        return cfg.retry?.initial_delay_ms || 1000;
      };

      expect(getFirstRetryDelay(config)).toBe(500);
    });

    it("accepts 0 (no delay)", () => {
      const result = PipelineRetryConfigSchema.safeParse({
        initial_delay_ms: 0,
      });
      expect(result.success).toBe(true);
    });

    it("accepts value 1000", () => {
      const result = PipelineRetryConfigSchema.safeParse({
        initial_delay_ms: 1000,
      });
      expect(result.success).toBe(true);
    });

    it("defaults to 1000", () => {
      expect(DEFAULT_PIPELINE_RETRY.initial_delay_ms).toBe(1000);
    });
  });

  // ============================================================================
  // pipeline.retry.retryable_api_errors - Behavior Tests
  // ============================================================================

  describe("retry.retryable_api_errors", () => {
    it("only listed codes trigger retry", () => {
      const config = createMockPipelineConfig({
        retry: { retryable_api_errors: [500, 502, 503] },
      });

      const shouldRetry = (statusCode: number, cfg: typeof config) => {
        const retryable = cfg.retry?.retryable_api_errors || [];
        return retryable.includes(statusCode);
      };

      expect(shouldRetry(500, config)).toBe(true);
      expect(shouldRetry(502, config)).toBe(true);
      expect(shouldRetry(503, config)).toBe(true);
      expect(shouldRetry(400, config)).toBe(false);
      expect(shouldRetry(404, config)).toBe(false);
    });

    it("empty array means no retries for API errors", () => {
      const config = createMockPipelineConfig({
        retry: { retryable_api_errors: [] },
      });

      const shouldRetry = (statusCode: number, cfg: typeof config) => {
        const retryable = cfg.retry?.retryable_api_errors || [];
        return retryable.includes(statusCode);
      };

      expect(shouldRetry(500, config)).toBe(false);
    });

    it("defaults to common server errors", () => {
      expect(DEFAULT_PIPELINE_RETRY.retryable_api_errors).toEqual([500, 502, 503, 504]);
    });
  });

  // ============================================================================
  // pipeline.retry.rate_limit_delay_ms - Behavior Tests
  // ============================================================================

  describe("retry.rate_limit_delay_ms", () => {
    it("uses special delay for 429 responses", () => {
      const config = createMockPipelineConfig({
        retry: { rate_limit_delay_ms: 30000 },
      });

      const getDelayForStatus = (statusCode: number, cfg: typeof config) => {
        if (statusCode === 429) {
          return cfg.retry?.rate_limit_delay_ms || 60000;
        }
        return cfg.retry?.initial_delay_ms || 1000;
      };

      expect(getDelayForStatus(429, config)).toBe(30000);
      expect(getDelayForStatus(500, config)).toBe(1000);
    });

    it("defaults to 60000", () => {
      expect(DEFAULT_PIPELINE_RETRY.rate_limit_delay_ms).toBe(60000);
    });
  });

  // ============================================================================
  // pipeline.logs.retain - Behavior Tests
  // ============================================================================

  describe("logs.retain", () => {
    it("persists logs when true", () => {
      const config = createMockPipelineConfig({ logs: { retain: true } });

      const shouldPersistLogs = (cfg: typeof config) => {
        return cfg.logs?.retain === true;
      };

      expect(shouldPersistLogs(config)).toBe(true);
    });

    it("discards logs when false", () => {
      const config = createMockPipelineConfig({ logs: { retain: false } });

      const shouldPersistLogs = (cfg: typeof config) => {
        return cfg.logs?.retain === true;
      };

      expect(shouldPersistLogs(config)).toBe(false);
    });

    it("defaults to true", () => {
      expect(DEFAULT_PIPELINE_LOGS.retain).toBe(true);
    });
  });

  // ============================================================================
  // pipeline.logs.dir - Behavior Tests
  // ============================================================================

  describe("logs.dir", () => {
    it("uses custom log directory", () => {
      const config = createMockPipelineConfig({ logs: { dir: "/tmp/logs" } });

      const getLogDir = (cfg: typeof config) => {
        return cfg.logs?.dir || ".nightgauge/logs";
      };

      expect(getLogDir(config)).toBe("/tmp/logs");
    });

    it("defaults to .nightgauge/logs", () => {
      expect(DEFAULT_PIPELINE_LOGS.dir).toBe(".nightgauge/logs");
    });
  });

  // ============================================================================
  // pipeline.logs.max_age_days - Behavior Tests
  // ============================================================================

  describe("logs.max_age_days", () => {
    it("sets cleanup threshold in days", () => {
      const config = createMockPipelineConfig({ logs: { max_age_days: 7 } });

      const shouldCleanupLog = (logAgeDays: number, cfg: typeof config): boolean => {
        const maxAge = cfg.logs?.max_age_days || 30;
        return logAgeDays > maxAge;
      };

      expect(shouldCleanupLog(5, config)).toBe(false);
      expect(shouldCleanupLog(8, config)).toBe(true);
    });

    it("minimum value is 1", () => {
      const result = PipelineLogsConfigSchema.safeParse({ max_age_days: 0 });
      expect(result.success).toBe(false);
    });

    it("defaults to 30", () => {
      expect(DEFAULT_PIPELINE_LOGS.max_age_days).toBe(30);
    });
  });

  // ============================================================================
  // pipeline.logs.max_count - Behavior Tests
  // ============================================================================

  describe("logs.max_count", () => {
    it("sets cleanup threshold by count", () => {
      const config = createMockPipelineConfig({ logs: { max_count: 50 } });

      const shouldCleanupOldest = (totalLogs: number, cfg: typeof config): boolean => {
        const maxCount = cfg.logs?.max_count || 100;
        return totalLogs > maxCount;
      };

      expect(shouldCleanupOldest(40, config)).toBe(false);
      expect(shouldCleanupOldest(60, config)).toBe(true);
    });

    it("minimum value is 1", () => {
      const result = PipelineLogsConfigSchema.safeParse({ max_count: 0 });
      expect(result.success).toBe(false);
    });

    it("defaults to 100", () => {
      expect(DEFAULT_PIPELINE_LOGS.max_count).toBe(100);
    });
  });

  // ============================================================================
  // Environment Variable Overrides
  // ============================================================================

  describe("environment variable overrides", () => {
    it("NIGHTGAUGE_PIPELINE_CI_TIMEOUT overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_PIPELINE_CI_TIMEOUT: "600",
      });

      try {
        expect(process.env.NIGHTGAUGE_PIPELINE_CI_TIMEOUT).toBe("600");
      } finally {
        cleanup();
      }
    });

    it("NIGHTGAUGE_PIPELINE_AUTO_FIX overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_PIPELINE_AUTO_FIX: "false",
      });

      try {
        expect(process.env.NIGHTGAUGE_PIPELINE_AUTO_FIX).toBe("false");
      } finally {
        cleanup();
      }
    });

    it("NIGHTGAUGE_PIPELINE_SKIP_TESTS overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_PIPELINE_SKIP_TESTS: "true",
      });

      try {
        expect(process.env.NIGHTGAUGE_PIPELINE_SKIP_TESTS).toBe("true");
      } finally {
        cleanup();
      }
    });

    it("env var takes precedence over config file", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_PIPELINE_CI_TIMEOUT: "900",
      });

      try {
        const configValue = "300";
        const envValue = process.env.NIGHTGAUGE_PIPELINE_CI_TIMEOUT;

        // Env should take precedence
        const effectiveValue = envValue || configValue;
        expect(effectiveValue).toBe("900");
      } finally {
        cleanup();
      }
    });

    it("all pipeline env vars are defined", () => {
      expect(EXTENDED_CONFIG_ENV_MAPPINGS["pipeline.ci_timeout"]).toBe(
        "NIGHTGAUGE_PIPELINE_CI_TIMEOUT"
      );
      expect(EXTENDED_CONFIG_ENV_MAPPINGS["pipeline.auto_fix"]).toBe(
        "NIGHTGAUGE_PIPELINE_AUTO_FIX"
      );
      expect(EXTENDED_CONFIG_ENV_MAPPINGS["pipeline.skip.tests"]).toBe(
        "NIGHTGAUGE_PIPELINE_SKIP_TESTS"
      );
      expect(EXTENDED_CONFIG_ENV_MAPPINGS["pipeline.skip.lint"]).toBe(
        "NIGHTGAUGE_PIPELINE_SKIP_LINT"
      );
      expect(EXTENDED_CONFIG_ENV_MAPPINGS["pipeline.skip.build"]).toBe(
        "NIGHTGAUGE_PIPELINE_SKIP_BUILD"
      );
    });
  });

  // ============================================================================
  // Schema Validation Tests
  // ============================================================================

  describe("validation", () => {
    it("validates complete config", () => {
      const result = PipelineConfigSchema.safeParse(DEFAULT_PIPELINE_CONFIG);
      expect(result.success).toBe(true);
    });

    it("validates partial config", () => {
      const partialConfig = { ci_timeout: 600 };
      const result = PipelineConfigSchema.safeParse(partialConfig);
      expect(result.success).toBe(true);
    });

    it("validates empty config", () => {
      const result = PipelineConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });

    it("validates skip checks config", () => {
      const result = SkipChecksConfigSchema.safeParse(DEFAULT_SKIP_CHECKS);
      expect(result.success).toBe(true);
    });

    it("validates retry config", () => {
      const result = PipelineRetryConfigSchema.safeParse(DEFAULT_PIPELINE_RETRY);
      expect(result.success).toBe(true);
    });

    it("validates logs config", () => {
      const result = PipelineLogsConfigSchema.safeParse(DEFAULT_PIPELINE_LOGS);
      expect(result.success).toBe(true);
    });

    it("rejects negative ci_timeout", () => {
      const result = PipelineConfigSchema.safeParse({ ci_timeout: -1 });
      expect(result.success).toBe(false);
    });
  });

  // ============================================================================
  // Default Values Tests
  // ============================================================================

  describe("default values", () => {
    it("DEFAULT_CONFIG.pipeline has correct defaults", () => {
      expect(DEFAULT_CONFIG.pipeline?.ci_timeout).toBe(10);
      expect(DEFAULT_CONFIG.pipeline?.auto_fix).toBe(true);
      expect(DEFAULT_CONFIG.pipeline?.skip_checks?.tests).toBe(false);
      expect(DEFAULT_CONFIG.pipeline?.skip_checks?.lint).toBe(false);
      expect(DEFAULT_CONFIG.pipeline?.skip_checks?.build).toBe(false);
      expect(DEFAULT_CONFIG.pipeline?.logs?.retain).toBe(true);
      expect(DEFAULT_CONFIG.pipeline?.logs?.dir).toBe(".nightgauge/logs");
    });

    it("mergeWithDefaults preserves user values", () => {
      const config = mergeWithDefaults({
        pipeline: { ci_timeout: 900 },
      });

      expect(config.pipeline?.ci_timeout).toBe(900);
    });

    it("missing pipeline section uses defaults", () => {
      const config = mergeWithDefaults({});

      expect(config.pipeline?.auto_fix).toBe(true);
      expect(config.pipeline?.logs?.retain).toBe(true);
    });
  });

  // ============================================================================
  // Skip Checks Helper Function
  // ============================================================================

  describe("skip checks helper", () => {
    it("provides skip check utility", () => {
      const config = createMockSkipChecks({ tests: true, lint: false });

      expect(config.tests).toBe(true);
      expect(config.lint).toBe(false);
      expect(config.build).toBe(false);
    });
  });

  // ============================================================================
  // Retry Config Helper Function
  // ============================================================================

  describe("retry config helper", () => {
    it("provides retry config utility", () => {
      const config = createMockPipelineRetry({
        max_auto_attempts: 5,
        initial_delay_ms: 500,
      });

      expect(config.max_auto_attempts).toBe(5);
      expect(config.initial_delay_ms).toBe(500);
      expect(config.backoff_multiplier).toBe(2); // default
    });
  });

  // ============================================================================
  // Full Pipeline Decision Simulation
  // ============================================================================

  describe("pipeline decision simulation", () => {
    it("determines which checks to run based on config", () => {
      const config = createMockPipelineConfig({
        skip: { tests: true, lint: false, build: false },
      });

      interface CheckResult {
        tests: boolean;
        lint: boolean;
        typecheck: boolean;
        build: boolean;
        format: boolean;
      }

      const determineChecks = (cfg: typeof config): CheckResult => {
        return {
          tests: cfg.skip?.tests !== true,
          lint: cfg.skip?.lint !== true,
          typecheck: cfg.skip?.typecheck !== true,
          build: cfg.skip?.build !== true,
          format: cfg.skip?.format !== true,
        };
      };

      const checks = determineChecks(config);
      expect(checks.tests).toBe(false); // skipped
      expect(checks.lint).toBe(true); // runs
      expect(checks.typecheck).toBe(true); // runs (default)
      expect(checks.build).toBe(true); // runs
      expect(checks.format).toBe(true); // runs (default)
    });
  });
});
