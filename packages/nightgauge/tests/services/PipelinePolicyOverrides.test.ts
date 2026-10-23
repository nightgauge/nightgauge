/**
 * Unit tests for PipelinePolicyOverrides.ts
 *
 * PipelinePolicyOverrides is a type-only module. These tests validate the
 * TypeScript types at compile time via type assertions and verify expected
 * runtime values for the HealthPolicyTier union.
 *
 * @see Issue #2446 - Add tests for untested VSCode services
 * @see Issue #1395 - Health-gated pipeline policies
 */

import { describe, it, expect } from "vitest";
import type {
  PipelinePolicyOverrides,
  HealthPolicyTier,
} from "../../src/services/PipelinePolicyOverrides";

describe("PipelinePolicyOverrides", () => {
  describe("PipelinePolicyOverrides interface", () => {
    it("constructs a valid none-tier override", () => {
      const override: PipelinePolicyOverrides = {
        tier: "none",
        retryBudgetIncrease: 0,
        escalateAllStages: false,
        pauseAutoRouting: false,
        reasons: [],
        score: 0.85,
        timestamp: "2024-01-01T00:00:00Z",
      };
      expect(override.tier).toBe("none");
      expect(override.retryBudgetIncrease).toBe(0);
      expect(override.escalateAllStages).toBe(false);
      expect(override.pauseAutoRouting).toBe(false);
    });

    it("constructs a valid warning-tier override", () => {
      const override: PipelinePolicyOverrides = {
        tier: "warning",
        retryBudgetIncrease: 1,
        escalateAllStages: false,
        pauseAutoRouting: false,
        reasons: ["Token efficiency below 40%"],
        score: 0.55,
        timestamp: "2024-01-01T00:00:00Z",
      };
      expect(override.tier).toBe("warning");
      expect(override.retryBudgetIncrease).toBe(1);
      expect(override.reasons).toHaveLength(1);
    });

    it("constructs a valid critical-tier override", () => {
      const override: PipelinePolicyOverrides = {
        tier: "critical",
        retryBudgetIncrease: 2,
        escalateAllStages: true,
        pauseAutoRouting: false,
        reasons: ["Health score critical", "Failure rate elevated"],
        score: 0.35,
        timestamp: "2024-01-01T00:00:00Z",
      };
      expect(override.tier).toBe("critical");
      expect(override.escalateAllStages).toBe(true);
      expect(override.reasons).toHaveLength(2);
    });

    it("constructs a valid emergency-tier override", () => {
      const override: PipelinePolicyOverrides = {
        tier: "emergency",
        retryBudgetIncrease: 2,
        escalateAllStages: true,
        pauseAutoRouting: true,
        reasons: ["Pipeline health critical", "Auto-routing paused"],
        score: 0.15,
        timestamp: "2024-01-01T00:00:00Z",
      };
      expect(override.tier).toBe("emergency");
      expect(override.pauseAutoRouting).toBe(true);
    });

    it("supports all HealthPolicyTier values", () => {
      const tiers: HealthPolicyTier[] = ["none", "warning", "critical", "emergency"];
      expect(tiers).toHaveLength(4);
      expect(tiers).toContain("none");
      expect(tiers).toContain("warning");
      expect(tiers).toContain("critical");
      expect(tiers).toContain("emergency");
    });

    it("scores is a number", () => {
      const override: PipelinePolicyOverrides = {
        tier: "none",
        retryBudgetIncrease: 0,
        escalateAllStages: false,
        pauseAutoRouting: false,
        reasons: [],
        score: 0.72,
        timestamp: new Date().toISOString(),
      };
      expect(typeof override.score).toBe("number");
    });
  });
});
