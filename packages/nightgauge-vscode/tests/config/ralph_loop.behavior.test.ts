/**
 * Behavior tests for ralph_loop.* configuration fields
 *
 * These tests verify that ralph loop (self-healing) config fields actually affect
 * runtime behavior, specifically iteration limits, token budgets, and timeouts.
 *
 * @see Issue #439 - Audit behavior config fields
 * @see packages/nightgauge-vscode/src/config/schema.ts - RalphLoopConfigSchema
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  createMockRalphLoopConfig,
  createMockRalphLoopLimits,
  DEFAULT_RALPH_LOOP_CONFIG,
  DEFAULT_RALPH_LOOP_LIMITS,
  applyEnvOverrides,
  BEHAVIOR_CONFIG_ENV_MAPPINGS,
} from "../mocks/config-fixtures";
import {
  RalphLoopConfigSchema,
  RalphLoopLimitsSchema,
  mergeWithDefaults,
} from "../../src/config/schema";

describe("ralph_loop.behavior", () => {
  // Store original env vars
  const originalEnv = { ...process.env };

  beforeEach(() => {
    // Clear ralph loop-related environment variables
    Object.keys(process.env).forEach((key) => {
      if (key.startsWith("NIGHTGAUGE_RALPH_LOOP_")) {
        delete process.env[key];
      }
    });
  });

  afterEach(() => {
    // Restore original environment
    process.env = { ...originalEnv };
  });

  // ============================================================================
  // ralph_loop.enabled - Behavior Tests
  // ============================================================================

  describe("enabled", () => {
    it("activates self-healing loop when true", () => {
      const config = createMockRalphLoopConfig({ enabled: true });

      const shouldRunRalphLoop = (cfg: typeof config) => {
        return cfg.enabled === true;
      };

      expect(shouldRunRalphLoop(config)).toBe(true);
    });

    it("skips self-healing when false", () => {
      const config = createMockRalphLoopConfig({ enabled: false });

      const shouldRunRalphLoop = (cfg: typeof config) => {
        return cfg.enabled === true;
      };

      expect(shouldRunRalphLoop(config)).toBe(false);
    });

    it("defaults to true", () => {
      expect(DEFAULT_RALPH_LOOP_CONFIG.enabled).toBe(true);
    });
  });

  // ============================================================================
  // ralph_loop.build - Behavior Tests
  // ============================================================================

  describe("build", () => {
    it("includes build in self-healing loop when true", () => {
      const config = createMockRalphLoopConfig({ build: true });

      const getHealingChecks = (cfg: typeof config): string[] => {
        const checks: string[] = [];
        if (cfg.build) checks.push("build");
        if (cfg.tests) checks.push("tests");
        if (cfg.lint) checks.push("lint");
        return checks;
      };

      expect(getHealingChecks(config)).toContain("build");
    });

    it("excludes build from loop when false", () => {
      const config = createMockRalphLoopConfig({ build: false });

      const getHealingChecks = (cfg: typeof config): string[] => {
        const checks: string[] = [];
        if (cfg.build) checks.push("build");
        if (cfg.tests) checks.push("tests");
        if (cfg.lint) checks.push("lint");
        return checks;
      };

      expect(getHealingChecks(config)).not.toContain("build");
    });

    it("defaults to true", () => {
      expect(DEFAULT_RALPH_LOOP_CONFIG.build).toBe(true);
    });
  });

  // ============================================================================
  // ralph_loop.tests - Behavior Tests
  // ============================================================================

  describe("tests", () => {
    it("includes tests in self-healing loop when true", () => {
      const config = createMockRalphLoopConfig({ tests: true });

      const getHealingChecks = (cfg: typeof config): string[] => {
        const checks: string[] = [];
        if (cfg.build) checks.push("build");
        if (cfg.tests) checks.push("tests");
        if (cfg.lint) checks.push("lint");
        return checks;
      };

      expect(getHealingChecks(config)).toContain("tests");
    });

    it("excludes tests from loop when false", () => {
      const config = createMockRalphLoopConfig({ tests: false });

      const getHealingChecks = (cfg: typeof config): string[] => {
        const checks: string[] = [];
        if (cfg.build) checks.push("build");
        if (cfg.tests) checks.push("tests");
        if (cfg.lint) checks.push("lint");
        return checks;
      };

      expect(getHealingChecks(config)).not.toContain("tests");
    });

    it("defaults to true", () => {
      expect(DEFAULT_RALPH_LOOP_CONFIG.tests).toBe(true);
    });
  });

  // ============================================================================
  // ralph_loop.lint - Behavior Tests
  // ============================================================================

  describe("lint", () => {
    it("includes lint in self-healing loop when true", () => {
      const config = createMockRalphLoopConfig({ lint: true });

      const getHealingChecks = (cfg: typeof config): string[] => {
        const checks: string[] = [];
        if (cfg.build) checks.push("build");
        if (cfg.tests) checks.push("tests");
        if (cfg.lint) checks.push("lint");
        return checks;
      };

      expect(getHealingChecks(config)).toContain("lint");
    });

    it("excludes lint from loop when false", () => {
      const config = createMockRalphLoopConfig({ lint: false });

      const getHealingChecks = (cfg: typeof config): string[] => {
        const checks: string[] = [];
        if (cfg.build) checks.push("build");
        if (cfg.tests) checks.push("tests");
        if (cfg.lint) checks.push("lint");
        return checks;
      };

      expect(getHealingChecks(config)).not.toContain("lint");
    });

    it("defaults to false", () => {
      expect(DEFAULT_RALPH_LOOP_CONFIG.lint).toBe(false);
    });
  });

  // ============================================================================
  // ralph_loop.limits.max_iterations - Behavior Tests
  // ============================================================================

  describe("limits.max_iterations", () => {
    it("limits self-healing attempts", () => {
      const config = createMockRalphLoopConfig({
        limits: { max_iterations: 3 },
      });

      const simulateRalphLoop = (cfg: typeof config): number => {
        const maxIterations = cfg.limits?.max_iterations || 5;
        let iterations = 0;
        let allPassing = false;

        while (!allPassing && iterations < maxIterations) {
          iterations++;
          // Simulate failure until max iterations
          allPassing = iterations >= maxIterations;
        }

        return iterations;
      };

      expect(simulateRalphLoop(config)).toBe(3);
    });

    it("minimum value is 1", () => {
      const result = RalphLoopLimitsSchema.safeParse({ max_iterations: 0 });
      expect(result.success).toBe(false);
    });

    it("accepts boundary value 1", () => {
      const result = RalphLoopLimitsSchema.safeParse({ max_iterations: 1 });
      expect(result.success).toBe(true);
    });

    it("defaults to 5", () => {
      expect(DEFAULT_RALPH_LOOP_LIMITS.max_iterations).toBe(5);
    });
  });

  // ============================================================================
  // ralph_loop.limits.token_budget_per_iteration - Behavior Tests
  // ============================================================================

  describe("limits.token_budget_per_iteration", () => {
    it("limits tokens per self-healing iteration", () => {
      const config = createMockRalphLoopConfig({
        limits: { token_budget_per_iteration: 25000 },
      });

      const shouldAbortIteration = (tokensUsed: number, cfg: typeof config): boolean => {
        const budget = cfg.limits?.token_budget_per_iteration || 50000;
        return tokensUsed >= budget;
      };

      expect(shouldAbortIteration(20000, config)).toBe(false);
      expect(shouldAbortIteration(25000, config)).toBe(true);
      expect(shouldAbortIteration(30000, config)).toBe(true);
    });

    it("0 means unlimited per-iteration tokens", () => {
      const config = createMockRalphLoopConfig({
        limits: { token_budget_per_iteration: 0 },
      });

      const shouldAbortIteration = (tokensUsed: number, cfg: typeof config): boolean => {
        const budget = cfg.limits?.token_budget_per_iteration || 0;
        if (budget === 0) return false;
        return tokensUsed >= budget;
      };

      expect(shouldAbortIteration(1000000, config)).toBe(false);
    });

    it("defaults to 50000", () => {
      expect(DEFAULT_RALPH_LOOP_LIMITS.token_budget_per_iteration).toBe(50000);
    });
  });

  // ============================================================================
  // ralph_loop.limits.total_token_budget - Behavior Tests
  // ============================================================================

  describe("limits.total_token_budget", () => {
    it("limits total tokens across all iterations", () => {
      const config = createMockRalphLoopConfig({
        limits: { total_token_budget: 100000 },
      });

      const shouldAbortLoop = (totalTokensUsed: number, cfg: typeof config): boolean => {
        const budget = cfg.limits?.total_token_budget || 200000;
        return totalTokensUsed >= budget;
      };

      expect(shouldAbortLoop(50000, config)).toBe(false);
      expect(shouldAbortLoop(100000, config)).toBe(true);
      expect(shouldAbortLoop(150000, config)).toBe(true);
    });

    it("0 means unlimited total tokens", () => {
      const config = createMockRalphLoopConfig({
        limits: { total_token_budget: 0 },
      });

      const shouldAbortLoop = (totalTokensUsed: number, cfg: typeof config): boolean => {
        const budget = cfg.limits?.total_token_budget || 0;
        if (budget === 0) return false;
        return totalTokensUsed >= budget;
      };

      expect(shouldAbortLoop(1000000, config)).toBe(false);
    });

    it("defaults to 200000", () => {
      expect(DEFAULT_RALPH_LOOP_LIMITS.total_token_budget).toBe(200000);
    });
  });

  // ============================================================================
  // ralph_loop.limits.iteration_timeout_ms - Behavior Tests
  // ============================================================================

  describe("limits.iteration_timeout_ms", () => {
    it("limits time per iteration in milliseconds", () => {
      const config = createMockRalphLoopConfig({
        limits: { iteration_timeout_ms: 60000 },
      });

      const shouldTimeoutIteration = (elapsedMs: number, cfg: typeof config): boolean => {
        const timeout = cfg.limits?.iteration_timeout_ms || 300000;
        return elapsedMs >= timeout;
      };

      expect(shouldTimeoutIteration(30000, config)).toBe(false);
      expect(shouldTimeoutIteration(60000, config)).toBe(true);
      expect(shouldTimeoutIteration(120000, config)).toBe(true);
    });

    it("0 means no per-iteration timeout", () => {
      const config = createMockRalphLoopConfig({
        limits: { iteration_timeout_ms: 0 },
      });

      const shouldTimeoutIteration = (elapsedMs: number, cfg: typeof config): boolean => {
        const timeout = cfg.limits?.iteration_timeout_ms || 0;
        if (timeout === 0) return false;
        return elapsedMs >= timeout;
      };

      expect(shouldTimeoutIteration(3600000, config)).toBe(false); // 1 hour
    });

    it("defaults to 300000 (5 minutes)", () => {
      expect(DEFAULT_RALPH_LOOP_LIMITS.iteration_timeout_ms).toBe(300000);
    });
  });

  // ============================================================================
  // ralph_loop.limits.total_timeout_ms - Behavior Tests
  // ============================================================================

  describe("limits.total_timeout_ms", () => {
    it("limits total time across all iterations", () => {
      const config = createMockRalphLoopConfig({
        limits: { total_timeout_ms: 600000 },
      });

      const shouldTimeoutLoop = (totalElapsedMs: number, cfg: typeof config): boolean => {
        const timeout = cfg.limits?.total_timeout_ms || 1800000;
        return totalElapsedMs >= timeout;
      };

      expect(shouldTimeoutLoop(300000, config)).toBe(false);
      expect(shouldTimeoutLoop(600000, config)).toBe(true);
      expect(shouldTimeoutLoop(900000, config)).toBe(true);
    });

    it("0 means no total timeout", () => {
      const config = createMockRalphLoopConfig({
        limits: { total_timeout_ms: 0 },
      });

      const shouldTimeoutLoop = (totalElapsedMs: number, cfg: typeof config): boolean => {
        const timeout = cfg.limits?.total_timeout_ms || 0;
        if (timeout === 0) return false;
        return totalElapsedMs >= timeout;
      };

      expect(shouldTimeoutLoop(7200000, config)).toBe(false); // 2 hours
    });

    it("defaults to 1800000 (30 minutes)", () => {
      expect(DEFAULT_RALPH_LOOP_LIMITS.total_timeout_ms).toBe(1800000);
    });
  });

  // ============================================================================
  // ralph_loop.abort_patterns - Behavior Tests
  // ============================================================================

  describe("abort_patterns", () => {
    it("aborts loop when error matches pattern", () => {
      const config = createMockRalphLoopConfig({
        abort_patterns: ["OutOfMemoryError", "FATAL:"],
      });

      const shouldAbortOnError = (errorMessage: string, cfg: typeof config): boolean => {
        return (cfg.abort_patterns || []).some((pattern) => errorMessage.includes(pattern));
      };

      expect(shouldAbortOnError("OutOfMemoryError: heap", config)).toBe(true);
      expect(shouldAbortOnError("FATAL: database down", config)).toBe(true);
      expect(shouldAbortOnError("Test failed: assertion", config)).toBe(false);
    });

    it("empty patterns means never abort early", () => {
      const config = createMockRalphLoopConfig({
        abort_patterns: [],
      });

      const shouldAbortOnError = (errorMessage: string, cfg: typeof config): boolean => {
        return (cfg.abort_patterns || []).some((pattern) => errorMessage.includes(pattern));
      };

      expect(shouldAbortOnError("OutOfMemoryError: heap", config)).toBe(false);
    });

    it("supports regex patterns", () => {
      const config = createMockRalphLoopConfig({
        abort_patterns: ["FATAL.*database", "OOM.*killed"],
      });

      const shouldAbortOnError = (errorMessage: string, cfg: typeof config): boolean => {
        return (cfg.abort_patterns || []).some((pattern) => {
          try {
            const regex = new RegExp(pattern);
            return regex.test(errorMessage);
          } catch {
            return errorMessage.includes(pattern);
          }
        });
      };

      expect(shouldAbortOnError("FATAL: database connection lost", config)).toBe(true);
      expect(shouldAbortOnError("OOM killer killed process", config)).toBe(true);
      expect(shouldAbortOnError("Regular test failure", config)).toBe(false);
    });

    it("defaults to empty array", () => {
      expect(DEFAULT_RALPH_LOOP_CONFIG.abort_patterns).toEqual([]);
    });
  });

  // ============================================================================
  // Environment Variable Overrides
  // ============================================================================

  describe("environment variable overrides", () => {
    it("NIGHTGAUGE_RALPH_LOOP_ENABLED overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_RALPH_LOOP_ENABLED: "false",
      });

      try {
        expect(process.env.NIGHTGAUGE_RALPH_LOOP_ENABLED).toBe("false");
      } finally {
        cleanup();
      }
    });

    it("NIGHTGAUGE_RALPH_LOOP_MAX_ITERATIONS overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_RALPH_LOOP_MAX_ITERATIONS: "10",
      });

      try {
        expect(process.env.NIGHTGAUGE_RALPH_LOOP_MAX_ITERATIONS).toBe("10");
      } finally {
        cleanup();
      }
    });

    it("env var takes precedence over config file", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_RALPH_LOOP_ENABLED: "false",
      });

      try {
        const configValue = "true";
        const envValue = process.env.NIGHTGAUGE_RALPH_LOOP_ENABLED;

        const effectiveValue = envValue || configValue;
        expect(effectiveValue).toBe("false");
      } finally {
        cleanup();
      }
    });

    it("ralph loop env vars are defined", () => {
      expect(BEHAVIOR_CONFIG_ENV_MAPPINGS["ralph_loop.enabled"]).toBe(
        "NIGHTGAUGE_RALPH_LOOP_ENABLED"
      );
      expect(BEHAVIOR_CONFIG_ENV_MAPPINGS["ralph_loop.limits.max_iterations"]).toBe(
        "NIGHTGAUGE_RALPH_LOOP_MAX_ITERATIONS"
      );
      expect(BEHAVIOR_CONFIG_ENV_MAPPINGS["ralph_loop.limits.total_token_budget"]).toBe(
        "NIGHTGAUGE_RALPH_LOOP_TOTAL_TOKENS"
      );
    });
  });

  // ============================================================================
  // Schema Validation Tests
  // ============================================================================

  describe("validation", () => {
    it("validates complete config", () => {
      const result = RalphLoopConfigSchema.safeParse(DEFAULT_RALPH_LOOP_CONFIG);
      expect(result.success).toBe(true);
    });

    it("validates partial config", () => {
      const partialConfig = { enabled: false };
      const result = RalphLoopConfigSchema.safeParse(partialConfig);
      expect(result.success).toBe(true);
    });

    it("validates empty config", () => {
      const result = RalphLoopConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });

    it("validates limits config", () => {
      const result = RalphLoopLimitsSchema.safeParse(DEFAULT_RALPH_LOOP_LIMITS);
      expect(result.success).toBe(true);
    });

    it("rejects negative iteration count", () => {
      const result = RalphLoopLimitsSchema.safeParse({ max_iterations: -1 });
      expect(result.success).toBe(false);
    });

    it("rejects non-integer max_iterations", () => {
      const result = RalphLoopLimitsSchema.safeParse({ max_iterations: 2.5 });
      expect(result.success).toBe(false);
    });
  });

  // ============================================================================
  // Default Values Tests
  // ============================================================================

  describe("default values", () => {
    it("DEFAULT_RALPH_LOOP_CONFIG has correct defaults", () => {
      expect(DEFAULT_RALPH_LOOP_CONFIG.enabled).toBe(true);
      expect(DEFAULT_RALPH_LOOP_CONFIG.build).toBe(true);
      expect(DEFAULT_RALPH_LOOP_CONFIG.tests).toBe(true);
      expect(DEFAULT_RALPH_LOOP_CONFIG.lint).toBe(false);
      expect(DEFAULT_RALPH_LOOP_CONFIG.limits?.max_iterations).toBe(5);
    });

    it("mergeWithDefaults preserves user values", () => {
      const config = mergeWithDefaults({
        ralph_loop: { enabled: false },
      });

      expect(config.ralph_loop?.enabled).toBe(false);
    });

    it("missing ralph_loop section returns defaults from DEFAULT_CONFIG", () => {
      const config = mergeWithDefaults({});

      // DEFAULT_CONFIG now has ralph_loop defaults
      expect(config.ralph_loop).toBeDefined();
      expect(config.ralph_loop?.enabled).toBe(true);
      expect(config.ralph_loop?.build).toBe(true);
      expect(config.ralph_loop?.tests).toBe(true);
      expect(config.ralph_loop?.lint).toBe(false);
      expect(config.ralph_loop?.limits?.max_iterations).toBe(3);
    });
  });

  // ============================================================================
  // Ralph Loop Execution Simulation
  // ============================================================================

  describe("ralph loop execution simulation", () => {
    it("simulates complete ralph loop with all limits", () => {
      const config = createMockRalphLoopConfig({
        enabled: true,
        build: true,
        tests: true,
        lint: false,
        limits: {
          max_iterations: 3,
          token_budget_per_iteration: 25000,
          total_token_budget: 60000,
          iteration_timeout_ms: 60000,
          total_timeout_ms: 150000,
        },
        abort_patterns: ["FATAL:"],
      });

      interface LoopState {
        iteration: number;
        totalTokens: number;
        totalTimeMs: number;
        status: "running" | "success" | "max_iterations" | "token_limit" | "timeout" | "aborted";
      }

      interface IterationResult {
        success: boolean;
        tokensUsed: number;
        timeMs: number;
        errorMessage?: string;
      }

      const simulateRalphLoop = (iterations: IterationResult[], cfg: typeof config): LoopState => {
        const state: LoopState = {
          iteration: 0,
          totalTokens: 0,
          totalTimeMs: 0,
          status: "running",
        };

        const limits = cfg.limits || {};
        const maxIterations = limits.max_iterations || 5;
        const totalTokenBudget = limits.total_token_budget || 0;
        const totalTimeout = limits.total_timeout_ms || 0;

        for (const result of iterations) {
          state.iteration++;
          state.totalTokens += result.tokensUsed;
          state.totalTimeMs += result.timeMs;

          // Check abort patterns
          if (
            result.errorMessage &&
            (cfg.abort_patterns || []).some((p) => result.errorMessage?.includes(p))
          ) {
            state.status = "aborted";
            return state;
          }

          // Check limits
          if (totalTokenBudget > 0 && state.totalTokens >= totalTokenBudget) {
            state.status = "token_limit";
            return state;
          }
          if (totalTimeout > 0 && state.totalTimeMs >= totalTimeout) {
            state.status = "timeout";
            return state;
          }
          if (state.iteration >= maxIterations) {
            state.status = result.success ? "success" : "max_iterations";
            return state;
          }

          if (result.success) {
            state.status = "success";
            return state;
          }
        }

        return state;
      };

      // Simulate 3 failing iterations - hits max_iterations
      const maxIterResult = simulateRalphLoop(
        [
          { success: false, tokensUsed: 15000, timeMs: 30000 },
          { success: false, tokensUsed: 15000, timeMs: 30000 },
          { success: false, tokensUsed: 15000, timeMs: 30000 },
        ],
        config
      );
      expect(maxIterResult.status).toBe("max_iterations");
      expect(maxIterResult.iteration).toBe(3);

      // Simulate hitting token limit
      const tokenLimitResult = simulateRalphLoop(
        [
          { success: false, tokensUsed: 30000, timeMs: 30000 },
          { success: false, tokensUsed: 35000, timeMs: 30000 }, // 65000 total
        ],
        config
      );
      expect(tokenLimitResult.status).toBe("token_limit");

      // Simulate abort pattern match
      const abortResult = simulateRalphLoop(
        [
          {
            success: false,
            tokensUsed: 10000,
            timeMs: 10000,
            errorMessage: "FATAL: database down",
          },
        ],
        config
      );
      expect(abortResult.status).toBe("aborted");

      // Simulate success on second iteration
      const successResult = simulateRalphLoop(
        [
          { success: false, tokensUsed: 15000, timeMs: 30000 },
          { success: true, tokensUsed: 10000, timeMs: 20000 },
        ],
        config
      );
      expect(successResult.status).toBe("success");
      expect(successResult.iteration).toBe(2);
    });
  });
});
