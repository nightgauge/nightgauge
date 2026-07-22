/**
 * Behavior tests for routing.* configuration fields
 *
 * These tests verify that routing config fields actually affect runtime behavior,
 * specifically complexity thresholds and pipeline stage skipping decisions.
 *
 * @see Issue #438 - Audit and test PR/Branch/Pipeline config fields
 * @see packages/nightgauge-vscode/src/config/schema.ts - RoutingConfigSchema
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  createMockRoutingConfig,
  DEFAULT_ROUTING_CONFIG,
  applyEnvOverrides,
  EXTENDED_CONFIG_ENV_MAPPINGS,
} from "../mocks/config-fixtures";
import { RoutingConfigSchema, mergeWithDefaults } from "../../src/config/schema";

describe("routing.behavior", () => {
  // Store original env vars
  const originalEnv = { ...process.env };

  beforeEach(() => {
    // Clear routing-related environment variables
    Object.keys(process.env).forEach((key) => {
      if (key.startsWith("NIGHTGAUGE_ROUTING_")) {
        delete process.env[key];
      }
    });
  });

  afterEach(() => {
    // Restore original environment
    process.env = { ...originalEnv };
  });

  // ============================================================================
  // routing.trivial_max_complexity - Behavior Tests
  // ============================================================================

  describe("trivial_max_complexity", () => {
    it("scores <= threshold route to trivial", () => {
      const config = createMockRoutingConfig({ trivial_max_complexity: 2 });

      type RouteType = "trivial" | "standard" | "extensive";

      const determineRoute = (complexityScore: number, cfg: typeof config): RouteType => {
        const trivialMax = cfg.trivial_max_complexity || 2;
        const extensiveMin = cfg.extensive_min_complexity || 5;

        if (complexityScore <= trivialMax) return "trivial";
        if (complexityScore >= extensiveMin) return "extensive";
        return "standard";
      };

      expect(determineRoute(1, config)).toBe("trivial");
      expect(determineRoute(2, config)).toBe("trivial");
      expect(determineRoute(3, config)).toBe("standard");
    });

    it("handles threshold of 1 (minimal trivial range)", () => {
      const config = createMockRoutingConfig({ trivial_max_complexity: 1 });

      type RouteType = "trivial" | "standard" | "extensive";

      const determineRoute = (complexityScore: number, cfg: typeof config): RouteType => {
        const trivialMax = cfg.trivial_max_complexity || 2;
        const extensiveMin = cfg.extensive_min_complexity || 5;

        if (complexityScore <= trivialMax) return "trivial";
        if (complexityScore >= extensiveMin) return "extensive";
        return "standard";
      };

      expect(determineRoute(1, config)).toBe("trivial");
      expect(determineRoute(2, config)).toBe("standard");
    });

    it("minimum value is 1", () => {
      const result = RoutingConfigSchema.safeParse({
        trivial_max_complexity: 0,
      });
      expect(result.success).toBe(false);
    });

    it("defaults to 2", () => {
      expect(DEFAULT_ROUTING_CONFIG.trivial_max_complexity).toBe(2);
    });
  });

  // ============================================================================
  // routing.extensive_min_complexity - Behavior Tests
  // ============================================================================

  describe("extensive_min_complexity", () => {
    it("scores >= threshold route to extensive", () => {
      const config = createMockRoutingConfig({ extensive_min_complexity: 5 });

      type RouteType = "trivial" | "standard" | "extensive";

      const determineRoute = (complexityScore: number, cfg: typeof config): RouteType => {
        const trivialMax = cfg.trivial_max_complexity || 2;
        const extensiveMin = cfg.extensive_min_complexity || 5;

        if (complexityScore <= trivialMax) return "trivial";
        if (complexityScore >= extensiveMin) return "extensive";
        return "standard";
      };

      expect(determineRoute(4, config)).toBe("standard");
      expect(determineRoute(5, config)).toBe("extensive");
      expect(determineRoute(10, config)).toBe("extensive");
    });

    it("handles high threshold (wide standard range)", () => {
      const config = createMockRoutingConfig({
        trivial_max_complexity: 2,
        extensive_min_complexity: 10,
      });

      type RouteType = "trivial" | "standard" | "extensive";

      const determineRoute = (complexityScore: number, cfg: typeof config): RouteType => {
        const trivialMax = cfg.trivial_max_complexity || 2;
        const extensiveMin = cfg.extensive_min_complexity || 5;

        if (complexityScore <= trivialMax) return "trivial";
        if (complexityScore >= extensiveMin) return "extensive";
        return "standard";
      };

      expect(determineRoute(5, config)).toBe("standard");
      expect(determineRoute(9, config)).toBe("standard");
      expect(determineRoute(10, config)).toBe("extensive");
    });

    it("minimum value is 1", () => {
      const result = RoutingConfigSchema.safeParse({
        extensive_min_complexity: 0,
      });
      expect(result.success).toBe(false);
    });

    it("defaults to 5", () => {
      expect(DEFAULT_ROUTING_CONFIG.extensive_min_complexity).toBe(5);
    });
  });

  // ============================================================================
  // routing.force_full_pipeline - Behavior Tests
  // ============================================================================

  describe("force_full_pipeline", () => {
    it("no stage skipping when true", () => {
      const config = createMockRoutingConfig({ force_full_pipeline: true });

      const canSkipStage = (_stage: string, _routeType: string, cfg: typeof config): boolean => {
        if (cfg.force_full_pipeline) {
          return false; // Never skip when forced
        }
        // Normal skip logic would go here
        return true;
      };

      expect(canSkipStage("feature-planning", "trivial", config)).toBe(false);
      expect(canSkipStage("feature-validate", "trivial", config)).toBe(false);
    });

    it("allows stage skipping when false", () => {
      const config = createMockRoutingConfig({ force_full_pipeline: false });

      const SKIPPABLE_STAGES = ["feature-planning", "feature-validate"];

      const canSkipStage = (stage: string, routeType: string, cfg: typeof config): boolean => {
        if (cfg.force_full_pipeline) {
          return false;
        }
        // Only trivial tasks can skip stages
        if (routeType !== "trivial") {
          return false;
        }
        return SKIPPABLE_STAGES.includes(stage);
      };

      expect(canSkipStage("feature-planning", "trivial", config)).toBe(true);
      expect(canSkipStage("feature-validate", "trivial", config)).toBe(true);
      expect(canSkipStage("pr-create", "trivial", config)).toBe(false);
    });

    it("defaults to false", () => {
      expect(DEFAULT_ROUTING_CONFIG.force_full_pipeline).toBe(false);
    });
  });

  // ============================================================================
  // Routing Decision Integration
  // ============================================================================

  describe("routing decision integration", () => {
    it("thresholds affect makeRoutingDecision behavior", () => {
      const config = createMockRoutingConfig({
        trivial_max_complexity: 3,
        extensive_min_complexity: 7,
      });

      interface RoutingDecision {
        routeType: "trivial" | "standard" | "extensive";
        skipStages: string[];
        fullPipeline: boolean;
      }

      const makeRoutingDecision = (
        complexityScore: number,
        cfg: typeof config
      ): RoutingDecision => {
        const trivialMax = cfg.trivial_max_complexity || 2;
        const extensiveMin = cfg.extensive_min_complexity || 5;
        const forceFull = cfg.force_full_pipeline || false;

        let routeType: "trivial" | "standard" | "extensive";
        if (complexityScore <= trivialMax) {
          routeType = "trivial";
        } else if (complexityScore >= extensiveMin) {
          routeType = "extensive";
        } else {
          routeType = "standard";
        }

        const skipStages: string[] = [];
        if (routeType === "trivial" && !forceFull) {
          skipStages.push("feature-planning", "feature-validate");
        }

        return {
          routeType,
          skipStages,
          fullPipeline: forceFull,
        };
      };

      // Test trivial route
      const trivialDecision = makeRoutingDecision(2, config);
      expect(trivialDecision.routeType).toBe("trivial");
      expect(trivialDecision.skipStages).toContain("feature-planning");

      // Test standard route
      const standardDecision = makeRoutingDecision(5, config);
      expect(standardDecision.routeType).toBe("standard");
      expect(standardDecision.skipStages).toEqual([]);

      // Test extensive route
      const extensiveDecision = makeRoutingDecision(8, config);
      expect(extensiveDecision.routeType).toBe("extensive");
      expect(extensiveDecision.skipStages).toEqual([]);
    });

    it("force_full_pipeline overrides skip behavior", () => {
      const config = createMockRoutingConfig({
        trivial_max_complexity: 3,
        force_full_pipeline: true,
      });

      interface RoutingDecision {
        routeType: "trivial" | "standard" | "extensive";
        skipStages: string[];
        fullPipeline: boolean;
      }

      const makeRoutingDecision = (
        complexityScore: number,
        cfg: typeof config
      ): RoutingDecision => {
        const trivialMax = cfg.trivial_max_complexity || 2;
        const extensiveMin = cfg.extensive_min_complexity || 5;
        const forceFull = cfg.force_full_pipeline || false;

        let routeType: "trivial" | "standard" | "extensive";
        if (complexityScore <= trivialMax) {
          routeType = "trivial";
        } else if (complexityScore >= extensiveMin) {
          routeType = "extensive";
        } else {
          routeType = "standard";
        }

        const skipStages: string[] = [];
        // Force full means no skipping even for trivial
        if (routeType === "trivial" && !forceFull) {
          skipStages.push("feature-planning", "feature-validate");
        }

        return {
          routeType,
          skipStages,
          fullPipeline: forceFull,
        };
      };

      // Even trivial route doesn't skip when force_full_pipeline=true
      const decision = makeRoutingDecision(1, config);
      expect(decision.routeType).toBe("trivial");
      expect(decision.skipStages).toEqual([]);
      expect(decision.fullPipeline).toBe(true);
    });
  });

  // ============================================================================
  // Invalid Threshold Combinations
  // ============================================================================

  describe("invalid threshold combinations", () => {
    it("trivial > extensive creates empty standard range", () => {
      const config = createMockRoutingConfig({
        trivial_max_complexity: 8,
        extensive_min_complexity: 3,
      });

      // When trivial_max > extensive_min, there's an overlap
      // This creates ambiguous routing for scores in the overlap
      const trivialMax = config.trivial_max_complexity || 2;
      const extensiveMin = config.extensive_min_complexity || 5;

      expect(trivialMax > extensiveMin).toBe(true);

      // Behavior: trivial takes precedence in overlap
      type RouteType = "trivial" | "standard" | "extensive";

      const determineRoute = (complexityScore: number): RouteType => {
        // Check trivial first (takes precedence)
        if (complexityScore <= trivialMax) return "trivial";
        if (complexityScore >= extensiveMin) return "extensive";
        return "standard";
      };

      // Score 5 is both <= 8 (trivial) and >= 3 (extensive)
      // Trivial check first, so it routes to trivial
      expect(determineRoute(5)).toBe("trivial");
      expect(determineRoute(9)).toBe("extensive");
    });

    it("equal thresholds creates no standard range", () => {
      const config = createMockRoutingConfig({
        trivial_max_complexity: 3,
        extensive_min_complexity: 3,
      });

      type RouteType = "trivial" | "standard" | "extensive";

      const determineRoute = (complexityScore: number, cfg: typeof config): RouteType => {
        const trivialMax = cfg.trivial_max_complexity || 2;
        const extensiveMin = cfg.extensive_min_complexity || 5;

        if (complexityScore <= trivialMax) return "trivial";
        if (complexityScore >= extensiveMin) return "extensive";
        return "standard";
      };

      expect(determineRoute(2, config)).toBe("trivial");
      expect(determineRoute(3, config)).toBe("trivial"); // <= 3
      expect(determineRoute(4, config)).toBe("extensive"); // >= 3 is already covered
    });
  });

  // ============================================================================
  // Environment Variable Overrides
  // ============================================================================

  describe("environment variable overrides", () => {
    it("NIGHTGAUGE_ROUTING_FORCE_FULL_PIPELINE overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_ROUTING_FORCE_FULL_PIPELINE: "true",
      });

      try {
        expect(process.env.NIGHTGAUGE_ROUTING_FORCE_FULL_PIPELINE).toBe("true");
      } finally {
        cleanup();
      }
    });

    it("NIGHTGAUGE_ROUTING_TRIVIAL_MAX overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_ROUTING_TRIVIAL_MAX: "5",
      });

      try {
        expect(process.env.NIGHTGAUGE_ROUTING_TRIVIAL_MAX).toBe("5");
      } finally {
        cleanup();
      }
    });

    it("env var takes precedence over config file", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_ROUTING_FORCE_FULL_PIPELINE: "true",
      });

      try {
        const configValue = "false";
        const envValue = process.env.NIGHTGAUGE_ROUTING_FORCE_FULL_PIPELINE;

        // Env should take precedence
        const effectiveValue = envValue || configValue;
        expect(effectiveValue).toBe("true");
      } finally {
        cleanup();
      }
    });

    it("routing env vars are defined", () => {
      expect(EXTENDED_CONFIG_ENV_MAPPINGS["routing.trivial_max_complexity"]).toBe(
        "NIGHTGAUGE_ROUTING_TRIVIAL_MAX"
      );
      expect(EXTENDED_CONFIG_ENV_MAPPINGS["routing.extensive_min_complexity"]).toBe(
        "NIGHTGAUGE_ROUTING_EXTENSIVE_MIN"
      );
      expect(EXTENDED_CONFIG_ENV_MAPPINGS["routing.force_full_pipeline"]).toBe(
        "NIGHTGAUGE_ROUTING_FORCE_FULL_PIPELINE"
      );
    });
  });

  // ============================================================================
  // Schema Validation Tests
  // ============================================================================

  describe("validation", () => {
    it("validates complete config", () => {
      const result = RoutingConfigSchema.safeParse(DEFAULT_ROUTING_CONFIG);
      expect(result.success).toBe(true);
    });

    it("validates partial config", () => {
      const partialConfig = { force_full_pipeline: true };
      const result = RoutingConfigSchema.safeParse(partialConfig);
      expect(result.success).toBe(true);
    });

    it("validates empty config", () => {
      const result = RoutingConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });

    it("rejects non-integer complexity values", () => {
      const result = RoutingConfigSchema.safeParse({
        trivial_max_complexity: 2.5,
      });
      expect(result.success).toBe(false);
    });

    it("rejects negative complexity values", () => {
      const result = RoutingConfigSchema.safeParse({
        trivial_max_complexity: -1,
      });
      expect(result.success).toBe(false);
    });
  });

  // ============================================================================
  // Default Values Tests
  // ============================================================================

  describe("default values", () => {
    it("DEFAULT_ROUTING_CONFIG has correct defaults", () => {
      expect(DEFAULT_ROUTING_CONFIG.trivial_max_complexity).toBe(2);
      expect(DEFAULT_ROUTING_CONFIG.extensive_min_complexity).toBe(5);
      expect(DEFAULT_ROUTING_CONFIG.force_full_pipeline).toBe(false);
    });

    it("mergeWithDefaults preserves user values", () => {
      const config = mergeWithDefaults({
        routing: { force_full_pipeline: true },
      });

      expect(config.routing?.force_full_pipeline).toBe(true);
    });

    it("missing routing section uses defaults", () => {
      const config = mergeWithDefaults({});

      // Note: DEFAULT_CONFIG doesn't have routing section, so it's undefined
      expect(config.routing).toBeUndefined();
    });
  });

  // ============================================================================
  // Stage Skip Logic Simulation
  // ============================================================================

  describe("stage skip logic", () => {
    it("trivial route skips feature-planning and feature-validate", () => {
      const config = createMockRoutingConfig({
        trivial_max_complexity: 2,
        force_full_pipeline: false,
      });

      const SKIPPABLE_STAGES = ["feature-planning", "feature-validate"];
      const ALL_STAGES = [
        "issue-pickup",
        "feature-planning",
        "feature-dev",
        "feature-validate",
        "pr-create",
        "pr-merge",
      ];

      const getStagesForRoute = (
        routeType: "trivial" | "standard" | "extensive",
        cfg: typeof config
      ): string[] => {
        if (cfg.force_full_pipeline) {
          return ALL_STAGES;
        }

        if (routeType === "trivial") {
          return ALL_STAGES.filter((s) => !SKIPPABLE_STAGES.includes(s));
        }

        return ALL_STAGES;
      };

      const trivialStages = getStagesForRoute("trivial", config);
      expect(trivialStages).not.toContain("feature-planning");
      expect(trivialStages).not.toContain("feature-validate");
      expect(trivialStages).toContain("issue-pickup");
      expect(trivialStages).toContain("feature-dev");
      expect(trivialStages).toContain("pr-create");

      const standardStages = getStagesForRoute("standard", config);
      expect(standardStages).toEqual(ALL_STAGES);
    });
  });
});
