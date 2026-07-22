/**
 * Tests for Ralph Loop Controller
 *
 * Tests the deterministic loop control functions that enforce
 * iteration limits, token budgets, and timeouts.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  isLoopEnabledForPhase,
  matchesAbortPattern,
  shouldAttemptFix,
  canContinueLoop,
  hasIterationTimedOut,
  createLoopState,
  recordIteration,
  createLoopResult,
  getRemainingTokenBudget,
  getRemainingTimeBudget,
  formatLoopResult,
  mergeConfig,
  loadConfigFromEnv,
  DEFAULT_RALPH_LOOP_CONFIG,
  type RalphLoopConfig,
  type RalphLoopState,
  type RalphLoopIteration,
} from "../../src/utils/ralphLoopController";
import type { ClassifiedError } from "../../src/utils/errorClassifier";

describe("ralphLoopController", () => {
  describe("DEFAULT_RALPH_LOOP_CONFIG", () => {
    it("should have safe default values", () => {
      expect(DEFAULT_RALPH_LOOP_CONFIG.enabled).toBe(true);
      expect(DEFAULT_RALPH_LOOP_CONFIG.build_enabled).toBe(true);
      expect(DEFAULT_RALPH_LOOP_CONFIG.tests_enabled).toBe(true);
      expect(DEFAULT_RALPH_LOOP_CONFIG.lint_enabled).toBe(false);
      expect(DEFAULT_RALPH_LOOP_CONFIG.max_iterations).toBe(3);
      expect(DEFAULT_RALPH_LOOP_CONFIG.token_budget_per_iteration).toBe(2000);
      expect(DEFAULT_RALPH_LOOP_CONFIG.total_token_budget).toBe(10000);
      expect(DEFAULT_RALPH_LOOP_CONFIG.iteration_timeout_ms).toBe(60000);
      expect(DEFAULT_RALPH_LOOP_CONFIG.total_timeout_ms).toBe(300000);
    });

    it("should have abort patterns for configuration errors", () => {
      expect(DEFAULT_RALPH_LOOP_CONFIG.abort_patterns).toContain("Module not found");
      expect(DEFAULT_RALPH_LOOP_CONFIG.abort_patterns).toContain("Cannot find module");
      expect(DEFAULT_RALPH_LOOP_CONFIG.abort_patterns).toContain("ENOENT");
      expect(DEFAULT_RALPH_LOOP_CONFIG.abort_patterns).toContain("Permission denied");
      expect(DEFAULT_RALPH_LOOP_CONFIG.abort_patterns).toContain("Out of memory");
    });
  });

  describe("isLoopEnabledForPhase", () => {
    it("should return false when loop is globally disabled", () => {
      const config: RalphLoopConfig = {
        ...DEFAULT_RALPH_LOOP_CONFIG,
        enabled: false,
      };
      expect(isLoopEnabledForPhase(config, "build")).toBe(false);
      expect(isLoopEnabledForPhase(config, "tests")).toBe(false);
      expect(isLoopEnabledForPhase(config, "lint")).toBe(false);
    });

    it("should respect phase-specific settings", () => {
      const config: RalphLoopConfig = {
        ...DEFAULT_RALPH_LOOP_CONFIG,
        enabled: true,
        build_enabled: true,
        tests_enabled: false,
        lint_enabled: true,
      };
      expect(isLoopEnabledForPhase(config, "build")).toBe(true);
      expect(isLoopEnabledForPhase(config, "tests")).toBe(false);
      expect(isLoopEnabledForPhase(config, "lint")).toBe(true);
    });

    it("should return true for default config and build phase", () => {
      expect(isLoopEnabledForPhase(DEFAULT_RALPH_LOOP_CONFIG, "build")).toBe(true);
    });

    it("should return true for default config and tests phase", () => {
      expect(isLoopEnabledForPhase(DEFAULT_RALPH_LOOP_CONFIG, "tests")).toBe(true);
    });

    it("should return false for default config and lint phase", () => {
      expect(isLoopEnabledForPhase(DEFAULT_RALPH_LOOP_CONFIG, "lint")).toBe(false);
    });
  });

  describe("matchesAbortPattern", () => {
    const config = DEFAULT_RALPH_LOOP_CONFIG;

    it('should match "Module not found" errors', () => {
      const error: ClassifiedError = {
        type: "build",
        severity: "configuration",
        message: "Cannot find module 'react'",
        rawOutput: "Module not found: react",
      };
      expect(matchesAbortPattern(error, config)).toBe("Module not found");
    });

    it("should match ENOENT errors", () => {
      const error: ClassifiedError = {
        type: "build",
        severity: "configuration",
        message: "File not found",
        rawOutput: "ENOENT: no such file or directory",
      };
      expect(matchesAbortPattern(error, config)).toBe("ENOENT");
    });

    it("should match permission denied errors", () => {
      const error: ClassifiedError = {
        type: "build",
        severity: "configuration",
        message: "Access denied",
        rawOutput: "Permission denied: /etc/passwd",
      };
      expect(matchesAbortPattern(error, config)).toBe("Permission denied");
    });

    it("should return null for fixable errors", () => {
      const error: ClassifiedError = {
        type: "build",
        severity: "fixable",
        message: "Property 'foo' does not exist",
        rawOutput: "TS2339: Property foo does not exist",
      };
      expect(matchesAbortPattern(error, config)).toBeNull();
    });

    it("should be case-insensitive", () => {
      const error: ClassifiedError = {
        type: "build",
        severity: "configuration",
        message: "out of MEMORY error",
        rawOutput: "OUT OF MEMORY",
      };
      expect(matchesAbortPattern(error, config)).toBe("Out of memory");
    });
  });

  describe("shouldAttemptFix", () => {
    it("should return true for fixable errors", () => {
      const error: ClassifiedError = {
        type: "build",
        severity: "fixable",
        message: "Type error",
        rawOutput: "TS2345: Type error",
      };
      expect(shouldAttemptFix(error)).toBe(true);
    });

    it("should return false for configuration errors", () => {
      const error: ClassifiedError = {
        type: "build",
        severity: "configuration",
        message: "Module not found",
        rawOutput: "Module not found",
      };
      expect(shouldAttemptFix(error)).toBe(false);
    });

    it("should return false for architectural errors", () => {
      const error: ClassifiedError = {
        type: "build",
        severity: "architectural",
        message: "Circular dependency",
        rawOutput: "Circular dependency detected",
      };
      expect(shouldAttemptFix(error)).toBe(false);
    });

    it("should return false for unknown errors", () => {
      const error: ClassifiedError = {
        type: "unknown",
        severity: "unknown",
        message: "Something went wrong",
        rawOutput: "Error",
      };
      expect(shouldAttemptFix(error)).toBe(false);
    });
  });

  describe("canContinueLoop", () => {
    let state: RalphLoopState;
    const config = DEFAULT_RALPH_LOOP_CONFIG;

    beforeEach(() => {
      state = createLoopState("build");
    });

    it("should allow continuation at start", () => {
      const result = canContinueLoop(state, config);
      expect(result.canContinue).toBe(true);
      expect(result.reason).toBeUndefined();
    });

    it("should block when max iterations reached", () => {
      state.current_iteration = 3;
      const result = canContinueLoop(state, config);
      expect(result.canContinue).toBe(false);
      expect(result.reason).toContain("Maximum iterations");
      expect(result.reason).toContain("3");
    });

    it("should block when token budget exhausted", () => {
      state.tokens_consumed = 10000;
      const result = canContinueLoop(state, config);
      expect(result.canContinue).toBe(false);
      expect(result.reason).toContain("token budget");
    });

    it("should block when total timeout exceeded", () => {
      state.start_time = Date.now() - 400000; // 400 seconds ago
      const result = canContinueLoop(state, config);
      expect(result.canContinue).toBe(false);
      expect(result.reason).toContain("timeout");
    });

    it("should allow continuation when under all limits", () => {
      state.current_iteration = 2;
      state.tokens_consumed = 5000;
      const result = canContinueLoop(state, config);
      expect(result.canContinue).toBe(true);
    });
  });

  describe("hasIterationTimedOut", () => {
    it("should return false when under timeout", () => {
      const startTime = Date.now() - 30000; // 30 seconds ago
      expect(hasIterationTimedOut(startTime, DEFAULT_RALPH_LOOP_CONFIG)).toBe(false);
    });

    it("should return true when over timeout", () => {
      const startTime = Date.now() - 70000; // 70 seconds ago
      expect(hasIterationTimedOut(startTime, DEFAULT_RALPH_LOOP_CONFIG)).toBe(true);
    });

    it("should return true when exactly at timeout", () => {
      const startTime = Date.now() - 60000; // exactly 60 seconds
      expect(hasIterationTimedOut(startTime, DEFAULT_RALPH_LOOP_CONFIG)).toBe(true);
    });
  });

  describe("createLoopState", () => {
    it("should create state for build phase", () => {
      const state = createLoopState("build");
      expect(state.phase).toBe("build");
      expect(state.current_iteration).toBe(0);
      expect(state.tokens_consumed).toBe(0);
      expect(state.iterations).toEqual([]);
      expect(state.is_running).toBe(true);
      expect(state.was_aborted).toBe(false);
      expect(state.loop_id).toContain("ralph-build-");
    });

    it("should create state for tests phase", () => {
      const state = createLoopState("tests");
      expect(state.phase).toBe("tests");
      expect(state.loop_id).toContain("ralph-tests-");
    });

    it("should create state for lint phase", () => {
      const state = createLoopState("lint");
      expect(state.phase).toBe("lint");
      expect(state.loop_id).toContain("ralph-lint-");
    });
  });

  describe("recordIteration", () => {
    it("should record iteration and update state", () => {
      const state = createLoopState("build");
      const iteration: RalphLoopIteration = {
        iteration: 1,
        error: {
          type: "build",
          severity: "fixable",
          message: "Type error",
          rawOutput: "TS2345",
        },
        tokens_consumed: 1500,
        duration_ms: 5000,
        success: false,
      };

      recordIteration(state, iteration);

      expect(state.iterations).toHaveLength(1);
      expect(state.iterations[0]).toBe(iteration);
      expect(state.tokens_consumed).toBe(1500);
      expect(state.current_iteration).toBe(1);
    });

    it("should accumulate across multiple iterations", () => {
      const state = createLoopState("build");
      const error: ClassifiedError = {
        type: "build",
        severity: "fixable",
        message: "Error",
        rawOutput: "Error",
      };

      recordIteration(state, {
        iteration: 1,
        error,
        tokens_consumed: 1000,
        duration_ms: 3000,
        success: false,
      });

      recordIteration(state, {
        iteration: 2,
        error,
        tokens_consumed: 1500,
        duration_ms: 4000,
        success: true,
      });

      expect(state.iterations).toHaveLength(2);
      expect(state.tokens_consumed).toBe(2500);
      expect(state.current_iteration).toBe(2);
    });
  });

  describe("createLoopResult", () => {
    it("should create success result", () => {
      const state = createLoopState("build");
      state.iterations.push({
        iteration: 1,
        error: {
          type: "build",
          severity: "fixable",
          message: "Fixed",
          rawOutput: "OK",
        },
        tokens_consumed: 1000,
        duration_ms: 5000,
        success: true,
      });
      state.tokens_consumed = 1000;

      const result = createLoopResult(state, true);

      expect(result.success).toBe(true);
      expect(result.iterations_count).toBe(1);
      expect(result.total_tokens).toBe(1000);
      expect(result.message).toContain("Fixed after 1 iteration");
      expect(result.abort_reason).toBeUndefined();
    });

    it("should create max iterations failure result", () => {
      const state = createLoopState("build");
      state.current_iteration = 3;

      const result = createLoopResult(state, false, "max_iterations");

      expect(result.success).toBe(false);
      expect(result.abort_reason).toBe("max_iterations");
      expect(result.message).toContain("maximum");
    });

    it("should create token budget failure result", () => {
      const state = createLoopState("build");
      state.tokens_consumed = 10000;

      const result = createLoopResult(state, false, "token_budget");

      expect(result.success).toBe(false);
      expect(result.abort_reason).toBe("token_budget");
      expect(result.message).toContain("token budget");
    });

    it("should create timeout failure result", () => {
      const state = createLoopState("build");

      const result = createLoopResult(state, false, "timeout");

      expect(result.success).toBe(false);
      expect(result.abort_reason).toBe("timeout");
      expect(result.message).toContain("timeout");
    });

    it("should create abort pattern result", () => {
      const state = createLoopState("build");

      const result = createLoopResult(state, false, "abort_pattern", "Module not found");

      expect(result.success).toBe(false);
      expect(result.abort_reason).toBe("abort_pattern");
      expect(result.abort_pattern).toBe("Module not found");
      expect(result.message).toContain("Module not found");
    });
  });

  describe("getRemainingTokenBudget", () => {
    it("should return per-iteration budget when plenty left", () => {
      const state = createLoopState("build");
      state.tokens_consumed = 0;

      const remaining = getRemainingTokenBudget(state, DEFAULT_RALPH_LOOP_CONFIG);

      expect(remaining).toBe(2000); // per-iteration budget
    });

    it("should return remaining budget when less than per-iteration", () => {
      const state = createLoopState("build");
      state.tokens_consumed = 9000;

      const remaining = getRemainingTokenBudget(state, DEFAULT_RALPH_LOOP_CONFIG);

      expect(remaining).toBe(1000); // only 1000 left of total
    });

    it("should return 0 when budget exhausted", () => {
      const state = createLoopState("build");
      state.tokens_consumed = 10000;

      const remaining = getRemainingTokenBudget(state, DEFAULT_RALPH_LOOP_CONFIG);

      expect(remaining).toBe(0);
    });
  });

  describe("getRemainingTimeBudget", () => {
    it("should return per-iteration timeout when plenty left", () => {
      const state = createLoopState("build");
      state.start_time = Date.now();

      const remaining = getRemainingTimeBudget(state, DEFAULT_RALPH_LOOP_CONFIG);

      // Should be close to iteration timeout (60000ms)
      expect(remaining).toBeLessThanOrEqual(60000);
      expect(remaining).toBeGreaterThan(59000);
    });

    it("should return remaining time when less than per-iteration", () => {
      const state = createLoopState("build");
      state.start_time = Date.now() - 250000; // 250 seconds ago

      const remaining = getRemainingTimeBudget(state, DEFAULT_RALPH_LOOP_CONFIG);

      // Should be close to 50000ms remaining of total
      expect(remaining).toBeLessThanOrEqual(50000);
    });
  });

  describe("formatLoopResult", () => {
    it("should format success result", () => {
      const result = createLoopResult(createLoopState("build"), true);
      const formatted = formatLoopResult(result);

      expect(formatted).toContain("✓");
      expect(formatted).toContain("Iterations: 0");
      expect(formatted).toContain("Tokens: 0");
    });

    it("should format failure result with abort reason", () => {
      const result = createLoopResult(createLoopState("build"), false, "max_iterations");
      const formatted = formatLoopResult(result);

      expect(formatted).toContain("✗");
      expect(formatted).toContain("Abort reason: max_iterations");
    });
  });

  describe("mergeConfig", () => {
    it("should merge user config with defaults", () => {
      const userConfig = {
        max_iterations: 5,
        enabled: false,
      };

      const merged = mergeConfig(userConfig);

      expect(merged.max_iterations).toBe(5);
      expect(merged.enabled).toBe(false);
      expect(merged.token_budget_per_iteration).toBe(2000); // default
    });

    it("should merge abort patterns (not replace)", () => {
      const userConfig = {
        abort_patterns: ["Custom error"],
      };

      const merged = mergeConfig(userConfig);

      expect(merged.abort_patterns).toContain("Module not found"); // default
      expect(merged.abort_patterns).toContain("Custom error"); // user added
    });

    it("should return defaults when empty config provided", () => {
      const merged = mergeConfig({});

      expect(merged).toEqual({
        ...DEFAULT_RALPH_LOOP_CONFIG,
        abort_patterns: DEFAULT_RALPH_LOOP_CONFIG.abort_patterns,
      });
    });
  });

  describe("loadConfigFromEnv", () => {
    const originalEnv = process.env;

    beforeEach(() => {
      vi.resetModules();
      process.env = { ...originalEnv };
    });

    afterEach(() => {
      process.env = originalEnv;
    });

    it("should load enabled from env", () => {
      process.env.NIGHTGAUGE_RALPH_LOOP_ENABLED = "false";
      const config = loadConfigFromEnv();
      expect(config.enabled).toBe(false);
    });

    it("should load max_iterations from env", () => {
      process.env.NIGHTGAUGE_RALPH_LOOP_MAX_ITERATIONS = "5";
      const config = loadConfigFromEnv();
      expect(config.max_iterations).toBe(5);
    });

    it("should load token_budget_per_iteration from env", () => {
      process.env.NIGHTGAUGE_RALPH_LOOP_TOKEN_BUDGET = "3000";
      const config = loadConfigFromEnv();
      expect(config.token_budget_per_iteration).toBe(3000);
    });

    it("should load total_token_budget from env", () => {
      process.env.NIGHTGAUGE_RALPH_LOOP_TOTAL_TOKEN_BUDGET = "20000";
      const config = loadConfigFromEnv();
      expect(config.total_token_budget).toBe(20000);
    });

    it("should load iteration_timeout_ms from env", () => {
      process.env.NIGHTGAUGE_RALPH_LOOP_ITERATION_TIMEOUT = "120000";
      const config = loadConfigFromEnv();
      expect(config.iteration_timeout_ms).toBe(120000);
    });

    it("should load total_timeout_ms from env", () => {
      process.env.NIGHTGAUGE_RALPH_LOOP_TOTAL_TIMEOUT = "600000";
      const config = loadConfigFromEnv();
      expect(config.total_timeout_ms).toBe(600000);
    });

    it("should ignore invalid numeric values", () => {
      process.env.NIGHTGAUGE_RALPH_LOOP_MAX_ITERATIONS = "invalid";
      const config = loadConfigFromEnv();
      expect(config.max_iterations).toBeUndefined();
    });

    it("should ignore negative values", () => {
      process.env.NIGHTGAUGE_RALPH_LOOP_MAX_ITERATIONS = "-5";
      const config = loadConfigFromEnv();
      expect(config.max_iterations).toBeUndefined();
    });

    it("should ignore zero values", () => {
      process.env.NIGHTGAUGE_RALPH_LOOP_MAX_ITERATIONS = "0";
      const config = loadConfigFromEnv();
      expect(config.max_iterations).toBeUndefined();
    });
  });
});
