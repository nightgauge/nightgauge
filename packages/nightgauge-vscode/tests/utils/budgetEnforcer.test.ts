/**
 * Unit tests for BudgetEnforcer
 *
 * Tests all enforcement modes (hard/soft/threshold), size-aware budget lookup,
 * grace buffer calculation, config overrides, and message formatting.
 *
 * @see budgetEnforcer.ts
 * @see Issue #835 - Enforce hard budget limits
 */

import { describe, it, expect } from "vitest";
import {
  BudgetEnforcer,
  DEFAULT_SIZE_AWARE_BUDGETS,
  DEFAULT_OUTPUT_TOKEN_LIMITS,
  DEFAULT_BUDGET_MODE,
  DEFAULT_GRACE_PERCENT,
  BUDGET_PRESETS,
  getBudgetPreset,
  resolveEffectiveSize,
  resolveStageCostUsd,
  type BudgetMode,
  type BudgetPresetName,
  type PlanningBudgetHint,
  type SizeLabel,
} from "../../src/utils/budgetEnforcer";

describe("BudgetEnforcer", () => {
  describe("defaults", () => {
    it("should default to hard mode with 50% grace", () => {
      expect(DEFAULT_BUDGET_MODE).toBe("hard");
      expect(DEFAULT_GRACE_PERCENT).toBe(50);
    });

    it("should have size-aware budgets for all pipeline stages", () => {
      const expectedStages = [
        "issue-pickup",
        "feature-planning",
        "feature-dev",
        "feature-validate",
        "pr-create",
        "pr-merge",
      ];

      for (const stage of expectedStages) {
        expect(DEFAULT_SIZE_AWARE_BUDGETS[stage]).toBeDefined();
        const budget = DEFAULT_SIZE_AWARE_BUDGETS[stage];
        expect(budget.XS).toBeGreaterThan(0);
        expect(budget.S).toBeGreaterThan(0);
        expect(budget.M).toBeGreaterThan(0);
        expect(budget.L).toBeGreaterThan(0);
        expect(budget.XL).toBeGreaterThan(0);
      }
    });

    it("should have feature-dev M budget of $24.00 (#259 honest-accounting re-baseline)", () => {
      expect(DEFAULT_SIZE_AWARE_BUDGETS["feature-dev"].M).toBe(24.0);
    });

    // Pin the pr-merge values from #3650. Previously size:S was $0.40 which,
    // after generous preset (2.0×) + hard-mode grace (1.5×), produced an
    // effectiveLimit of $1.20 — too tight for the CI-watching path observed
    // in the #3646 retro. The bumped baseline gives size:S a $3.00 ceiling
    // post-multipliers and size:M $4.50.
    //
    // size:M re-baselined again in #265: bowlsheet #261 (M) hit $4.51 REAL
    // against the $4.50 generous+grace ceiling (1.5 × 2.0 × 1.5), tripping it
    // by one cent. New size:M = 2.0 → generous+grace = $6.00.
    it("should have pr-merge size budgets calibrated for the CI-watching path (#3650, #265)", () => {
      expect(DEFAULT_SIZE_AWARE_BUDGETS["pr-merge"]).toEqual({
        XS: 0.4,
        S: 1.0,
        M: 2.0,
        L: 3.0,
        XL: 5.0,
      });
    });

    // Pin the pr-create values from #265: the #259 ladder (M $0.30, L $1.00)
    // still undersized the LLM fallback path — observed actuals of $1.97/$2.10
    // (M) and $1.70/$2.50 (L) repeatedly tripped the standard/generous caps.
    // New values give the STANDARD (1.0×) preset ~30-50% headroom over the
    // worst observed actual per tier. A future re-tune should update this
    // assertion deliberately.
    it("should have pr-create size budgets re-baselined for the LLM fallback path (#265)", () => {
      expect(DEFAULT_SIZE_AWARE_BUDGETS["pr-create"]).toEqual({
        XS: 0.5,
        S: 1.0,
        M: 3.0,
        L: 4.0,
        XL: 5.5,
      });
    });
  });

  describe("getBaseBudget", () => {
    it("should return size-aware budget for known stage and size", () => {
      const enforcer = new BudgetEnforcer();
      expect(enforcer.getBaseBudget("feature-dev", "XS")).toBe(4.0);
      expect(enforcer.getBaseBudget("feature-dev", "M")).toBe(24.0);
      expect(enforcer.getBaseBudget("feature-dev", "XL")).toBe(80.0);
    });

    it("should fall back to M when no size label provided", () => {
      const enforcer = new BudgetEnforcer();
      expect(enforcer.getBaseBudget("feature-dev")).toBe(24.0);
    });

    it("should return 0 for unknown stage", () => {
      const enforcer = new BudgetEnforcer();
      expect(enforcer.getBaseBudget("unknown-stage")).toBe(0);
    });

    it("should use flat config override when provided", () => {
      const enforcer = new BudgetEnforcer({
        stageOverrides: { "feature-dev": 20.0 },
      });
      expect(enforcer.getBaseBudget("feature-dev", "M")).toBe(20.0);
      expect(enforcer.getBaseBudget("feature-dev", "XL")).toBe(20.0);
    });

    it("should use size-aware config override when provided", () => {
      const enforcer = new BudgetEnforcer({
        stageOverrides: {
          "feature-dev": { M: 15.0, L: 30.0 },
        },
      });
      expect(enforcer.getBaseBudget("feature-dev", "M")).toBe(15.0);
      expect(enforcer.getBaseBudget("feature-dev", "L")).toBe(30.0);
      // Size not in override falls back to M in override
      expect(enforcer.getBaseBudget("feature-dev", "XS")).toBe(15.0);
    });

    it("should fall back to defaults for non-overridden stages", () => {
      const enforcer = new BudgetEnforcer({
        stageOverrides: { "feature-dev": 20.0 },
      });
      expect(enforcer.getBaseBudget("pr-create", "M")).toBe(3.0);
    });
  });

  describe("getEffectiveLimit", () => {
    it("should apply 50% grace buffer by default", () => {
      const enforcer = new BudgetEnforcer();
      // feature-dev M = $24.00, effective = $24.00 * 1.5 = $36.00
      expect(enforcer.getEffectiveLimit("feature-dev", "M")).toBe(36.0);
    });

    it("should apply custom grace percent", () => {
      const enforcer = new BudgetEnforcer({ gracePercent: 100 });
      // feature-dev M = $24.00, effective = $24.00 * 2.0 = $48.00
      expect(enforcer.getEffectiveLimit("feature-dev", "M")).toBe(48.0);
    });

    it("should return 0 for unknown stage", () => {
      const enforcer = new BudgetEnforcer();
      expect(enforcer.getEffectiveLimit("unknown-stage")).toBe(0);
    });

    it("should apply 0% grace correctly", () => {
      const enforcer = new BudgetEnforcer({ gracePercent: 0 });
      // feature-dev M = $24.00, effective = $24.00 * 1.0 = $24.00
      expect(enforcer.getEffectiveLimit("feature-dev", "M")).toBe(24.0);
    });
  });

  describe("checkBudget - hard mode", () => {
    const enforcer = new BudgetEnforcer({ mode: "hard" });

    it("should not warn or terminate when under budget", () => {
      const decision = enforcer.checkBudget("feature-dev", 5.0, "M");
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.shouldWarn).toBe(false);
      expect(decision.message).toBe("");
    });

    it("should warn but not terminate when over budget but under grace limit", () => {
      // M budget = $24.00, effective limit = $36.00
      const decision = enforcer.checkBudget("feature-dev", 30.0, "M");
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.shouldWarn).toBe(true);
      expect(decision.message).toContain("BUDGET WARNING");
      expect(decision.message).toContain("$30.00");
    });

    it("should terminate when over effective limit", () => {
      // M budget = $24.00, effective limit = $36.00
      const decision = enforcer.checkBudget("feature-dev", 40.0, "M");
      expect(decision.shouldTerminate).toBe(true);
      expect(decision.shouldWarn).toBe(false);
      expect(decision.message).toContain("BUDGET EXCEEDED");
      expect(decision.message).toContain("$40.00");
      expect(decision.message).toContain("$36.00");
    });

    it("should terminate exactly at the effective limit boundary", () => {
      // M budget = $24.00, effective limit = $36.00
      const decision = enforcer.checkBudget("feature-dev", 36.01, "M");
      expect(decision.shouldTerminate).toBe(true);
    });

    it("should not terminate at exactly the effective limit", () => {
      const decision = enforcer.checkBudget("feature-dev", 36.0, "M");
      // cost is not > effectiveLimit, it's equal
      expect(decision.shouldTerminate).toBe(false);
    });
  });

  describe("checkBudget - soft mode", () => {
    const enforcer = new BudgetEnforcer({ mode: "soft" });

    it("should never terminate, even when far over budget", () => {
      const decision = enforcer.checkBudget("feature-dev", 100.0, "M");
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.shouldWarn).toBe(true);
      expect(decision.budgetMode).toBe("soft");
    });

    it("should warn when over base budget", () => {
      const decision = enforcer.checkBudget("feature-dev", 30.0, "M");
      expect(decision.shouldWarn).toBe(true);
      expect(decision.message).toContain("BUDGET WARNING");
    });

    it("should not warn when under budget", () => {
      const decision = enforcer.checkBudget("feature-dev", 5.0, "M");
      expect(decision.shouldWarn).toBe(false);
    });
  });

  describe("checkBudget - threshold mode", () => {
    it("should terminate at configured threshold percentage", () => {
      const enforcer = new BudgetEnforcer({
        mode: "threshold",
        gracePercent: 25,
      });
      // M budget = $24.00, effective = $24.00 * 1.25 = $30.00
      const decision = enforcer.checkBudget("feature-dev", 31.0, "M");
      expect(decision.shouldTerminate).toBe(true);
      expect(decision.budgetMode).toBe("threshold");
    });

    it("should warn but not terminate between budget and threshold", () => {
      const enforcer = new BudgetEnforcer({
        mode: "threshold",
        gracePercent: 25,
      });
      // M budget = $24.00, effective = $24.00 * 1.25 = $30.00; use cost between
      const decision = enforcer.checkBudget("feature-dev", 27.0, "M");
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.shouldWarn).toBe(true);
    });
  });

  describe("size-aware lookup", () => {
    const enforcer = new BudgetEnforcer();

    it("should return correct budget for each size", () => {
      const sizes: SizeLabel[] = ["XS", "S", "M", "L", "XL"];
      const expected = [4.0, 8.0, 24.0, 50.0, 80.0]; // feature-dev budgets

      for (let i = 0; i < sizes.length; i++) {
        expect(enforcer.getBaseBudget("feature-dev", sizes[i])).toBe(expected[i]);
      }
    });

    it("should use M fallback for unknown size", () => {
      // When no sizeLabel is provided
      const decision = enforcer.checkBudget("feature-dev", 15.0);
      // M budget = $24.00 — under budget
      expect(decision.shouldWarn).toBe(false);
      expect(decision.shouldTerminate).toBe(false);
    });

    it("should give more budget to larger issues", () => {
      expect(enforcer.getBaseBudget("feature-dev", "XS")).toBeLessThan(
        enforcer.getBaseBudget("feature-dev", "S")
      );
      expect(enforcer.getBaseBudget("feature-dev", "S")).toBeLessThan(
        enforcer.getBaseBudget("feature-dev", "M")
      );
      expect(enforcer.getBaseBudget("feature-dev", "M")).toBeLessThan(
        enforcer.getBaseBudget("feature-dev", "L")
      );
      expect(enforcer.getBaseBudget("feature-dev", "L")).toBeLessThan(
        enforcer.getBaseBudget("feature-dev", "XL")
      );
    });
  });

  describe("no budget stages", () => {
    const enforcer = new BudgetEnforcer();

    it("should not warn or terminate for unknown stages", () => {
      const decision = enforcer.checkBudget("unknown-stage", 1000.0);
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.shouldWarn).toBe(false);
      expect(decision.message).toBe("");
    });
  });

  describe("message formatting", () => {
    const enforcer = new BudgetEnforcer();

    it("should format termination message with stage name and costs", () => {
      const msg = enforcer.formatTerminationMessage("feature-dev", 19.5, 18.0);
      expect(msg).toContain("BUDGET EXCEEDED");
      expect(msg).toContain("feature-dev");
      expect(msg).toContain("$19.50");
      expect(msg).toContain("$18.00");
    });

    it("should format warning message with stage name and costs", () => {
      const msg = enforcer.formatWarningMessage("feature-dev", 22.0, 20.0);
      expect(msg).toContain("BUDGET WARNING");
      expect(msg).toContain("feature-dev");
      expect(msg).toContain("$22.00");
      expect(msg).toContain("$20.00");
    });
  });

  describe("issue-pickup stage", () => {
    const enforcer = new BudgetEnforcer();

    it("should use M budget when size is unknown (issue-pickup)", () => {
      // issue-pickup M = $1.50
      const decision = enforcer.checkBudget("issue-pickup", 1.0);
      expect(decision.shouldWarn).toBe(false);
      expect(decision.shouldTerminate).toBe(false);
    });

    it("should enforce budget for issue-pickup stage", () => {
      // issue-pickup M = $1.50, effective = $2.25
      const decision = enforcer.checkBudget("issue-pickup", 2.5);
      expect(decision.shouldTerminate).toBe(true);
    });
  });

  describe("decision metadata", () => {
    it("should include current cost and effective limit in decision", () => {
      const enforcer = new BudgetEnforcer();
      const decision = enforcer.checkBudget("feature-dev", 10.0, "M");
      expect(decision.currentCost).toBe(10.0);
      expect(decision.effectiveLimit).toBe(36.0);
      expect(decision.budgetMode).toBe("hard");
    });
  });

  // ==========================================================================
  // Output Token Enforcement (Issue #842)
  // ==========================================================================

  describe("DEFAULT_OUTPUT_TOKEN_LIMITS", () => {
    it("should have defaults only for feature-dev", () => {
      expect(DEFAULT_OUTPUT_TOKEN_LIMITS["feature-dev"]).toBeDefined();
      expect(Object.keys(DEFAULT_OUTPUT_TOKEN_LIMITS)).toEqual(["feature-dev"]);
    });

    it("should have correct per-size limits for feature-dev", () => {
      const limits = DEFAULT_OUTPUT_TOKEN_LIMITS["feature-dev"];
      expect(limits.XS).toBe(15000);
      expect(limits.S).toBe(25000);
      expect(limits.M).toBe(50000);
      expect(limits.L).toBe(100000);
      expect(limits.XL).toBe(150000);
    });
  });

  describe("getBaseOutputTokenLimit", () => {
    it("should return default limit for feature-dev", () => {
      const enforcer = new BudgetEnforcer();
      expect(enforcer.getBaseOutputTokenLimit("feature-dev", "M")).toBe(50000);
      expect(enforcer.getBaseOutputTokenLimit("feature-dev", "XS")).toBe(15000);
      expect(enforcer.getBaseOutputTokenLimit("feature-dev", "XL")).toBe(150000);
    });

    it("should fall back to M when no size label provided", () => {
      const enforcer = new BudgetEnforcer();
      expect(enforcer.getBaseOutputTokenLimit("feature-dev")).toBe(50000);
    });

    it("should return 0 for stages without output token limits", () => {
      const enforcer = new BudgetEnforcer();
      expect(enforcer.getBaseOutputTokenLimit("pr-create", "M")).toBe(0);
      expect(enforcer.getBaseOutputTokenLimit("issue-pickup", "M")).toBe(0);
      expect(enforcer.getBaseOutputTokenLimit("unknown-stage")).toBe(0);
    });

    it("should use flat config override when provided", () => {
      const enforcer = new BudgetEnforcer({
        outputTokenOverrides: { "feature-dev": 80000 },
      });
      expect(enforcer.getBaseOutputTokenLimit("feature-dev", "M")).toBe(80000);
      expect(enforcer.getBaseOutputTokenLimit("feature-dev", "XL")).toBe(80000);
    });

    it("should use size-aware config override when provided", () => {
      const enforcer = new BudgetEnforcer({
        outputTokenOverrides: {
          "feature-dev": { M: 60000, XL: 200000 },
        },
      });
      expect(enforcer.getBaseOutputTokenLimit("feature-dev", "M")).toBe(60000);
      expect(enforcer.getBaseOutputTokenLimit("feature-dev", "XL")).toBe(200000);
      // Sizes not in override fall back to M in override
      expect(enforcer.getBaseOutputTokenLimit("feature-dev", "XS")).toBe(60000);
    });

    it("should fall back to defaults for non-overridden stages", () => {
      const enforcer = new BudgetEnforcer({
        outputTokenOverrides: { "feature-validate": 30000 },
      });
      // feature-dev still uses default
      expect(enforcer.getBaseOutputTokenLimit("feature-dev", "M")).toBe(50000);
      // feature-validate uses override
      expect(enforcer.getBaseOutputTokenLimit("feature-validate", "M")).toBe(30000);
    });
  });

  describe("getEffectiveOutputTokenLimit", () => {
    it("should apply 50% grace buffer by default", () => {
      const enforcer = new BudgetEnforcer();
      // feature-dev M = 50000, effective = 50000 * 1.5 = 75000
      expect(enforcer.getEffectiveOutputTokenLimit("feature-dev", "M")).toBe(75000);
    });

    it("should apply custom grace percent", () => {
      const enforcer = new BudgetEnforcer({ gracePercent: 100 });
      // feature-dev M = 50000, effective = 50000 * 2.0 = 100000
      expect(enforcer.getEffectiveOutputTokenLimit("feature-dev", "M")).toBe(100000);
    });

    it("should return 0 for stages without output token limits", () => {
      const enforcer = new BudgetEnforcer();
      expect(enforcer.getEffectiveOutputTokenLimit("pr-create", "M")).toBe(0);
    });
  });

  describe("checkOutputTokens - hard mode (warn-only per #1609)", () => {
    const enforcer = new BudgetEnforcer({ mode: "hard" });

    it("should not warn or terminate when under limit", () => {
      const decision = enforcer.checkOutputTokens("feature-dev", 30000, "M");
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.shouldWarn).toBe(false);
      expect(decision.message).toBe("");
    });

    it("should warn but not terminate when over base limit but under grace", () => {
      // M base = 50000, effective = 75000
      const decision = enforcer.checkOutputTokens("feature-dev", 60000, "M");
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.shouldWarn).toBe(true);
      expect(decision.message).toContain("OUTPUT TOKEN WARNING");
      expect(decision.message).toContain("60,000");
    });

    it("should warn but never terminate when over effective limit (#1609)", () => {
      // M base = 50000, effective = 75000
      const decision = enforcer.checkOutputTokens("feature-dev", 80000, "M");
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.shouldWarn).toBe(true);
      expect(decision.message).toContain("OUTPUT TOKEN WARNING");
      expect(decision.message).toContain("80,000");
    });

    it("should not terminate at exactly the effective limit", () => {
      const decision = enforcer.checkOutputTokens("feature-dev", 75000, "M");
      expect(decision.shouldTerminate).toBe(false);
    });

    it("should not terminate even above the effective limit (#1609)", () => {
      const decision = enforcer.checkOutputTokens("feature-dev", 75001, "M");
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.shouldWarn).toBe(true);
    });
  });

  describe("checkOutputTokens - soft mode", () => {
    const enforcer = new BudgetEnforcer({ mode: "soft" });

    it("should never terminate, even when far over limit", () => {
      const decision = enforcer.checkOutputTokens("feature-dev", 500000, "M");
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.shouldWarn).toBe(true);
    });

    it("should not warn when under limit", () => {
      const decision = enforcer.checkOutputTokens("feature-dev", 30000, "M");
      expect(decision.shouldWarn).toBe(false);
    });
  });

  describe("checkOutputTokens - no limit configured", () => {
    const enforcer = new BudgetEnforcer();

    it("should not warn or terminate for stages without output token limits", () => {
      const decision = enforcer.checkOutputTokens("pr-create", 1000000, "M");
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.shouldWarn).toBe(false);
      expect(decision.effectiveLimit).toBe(0);
      expect(decision.message).toBe("");
    });
  });

  describe("checkOutputTokens - config override with flat number", () => {
    it("should use flat override for all sizes", () => {
      const enforcer = new BudgetEnforcer({
        outputTokenOverrides: { "feature-dev": 80000 },
      });
      // Base = 80000, effective = 80000 * 1.5 = 120000
      const decision = enforcer.checkOutputTokens("feature-dev", 90000, "XL");
      expect(decision.shouldWarn).toBe(true);
      expect(decision.shouldTerminate).toBe(false);

      const termDecision = enforcer.checkOutputTokens("feature-dev", 121000, "XL");
      // Output tokens never terminate (#1609) — warn only
      expect(termDecision.shouldTerminate).toBe(false);
      expect(termDecision.shouldWarn).toBe(true);
    });
  });

  describe("checkOutputTokens - config override with per-size object", () => {
    it("should use per-size override", () => {
      const enforcer = new BudgetEnforcer({
        outputTokenOverrides: {
          "feature-dev": { XL: 300000 },
        },
      });
      // XL base = 300000, effective = 300000 * 1.5 = 450000
      expect(enforcer.getBaseOutputTokenLimit("feature-dev", "XL")).toBe(300000);
      expect(enforcer.getEffectiveOutputTokenLimit("feature-dev", "XL")).toBe(450000);
    });
  });

  describe("output token message formatting", () => {
    const enforcer = new BudgetEnforcer();

    it("should format termination message with token counts", () => {
      const msg = enforcer.formatOutputTokenTerminationMessage("feature-dev", 80000, 75000);
      expect(msg).toContain("OUTPUT TOKEN LIMIT EXCEEDED");
      expect(msg).toContain("feature-dev");
      expect(msg).toContain("80,000");
      expect(msg).toContain("75,000");
    });

    it("should format warning message with token counts", () => {
      const msg = enforcer.formatOutputTokenWarningMessage("feature-dev", 60000, 50000);
      expect(msg).toContain("OUTPUT TOKEN WARNING");
      expect(msg).toContain("feature-dev");
      expect(msg).toContain("60,000");
      expect(msg).toContain("50,000");
    });
  });

  // ==========================================================================
  // Budget Presets (Issue #947)
  // ==========================================================================

  describe("BUDGET_PRESETS", () => {
    it("should define conservative, standard, and generous presets", () => {
      expect(BUDGET_PRESETS.conservative).toBeDefined();
      expect(BUDGET_PRESETS.standard).toBeDefined();
      expect(BUDGET_PRESETS.generous).toBeDefined();
    });

    it("should have correct multipliers", () => {
      expect(BUDGET_PRESETS.conservative.multiplier).toBe(0.5);
      expect(BUDGET_PRESETS.standard.multiplier).toBe(1.0);
      expect(BUDGET_PRESETS.generous.multiplier).toBe(2.0);
    });

    it("should have descriptions for all presets", () => {
      for (const preset of Object.values(BUDGET_PRESETS)) {
        expect(preset.description).toBeTruthy();
      }
    });
  });

  describe("getBudgetPreset", () => {
    it("should return standard preset matching defaults exactly", () => {
      const standard = getBudgetPreset("standard");
      for (const [stage, budgets] of Object.entries(DEFAULT_SIZE_AWARE_BUDGETS)) {
        expect(standard[stage]).toEqual(budgets);
      }
    });

    it("should return conservative preset at 50% of defaults", () => {
      const conservative = getBudgetPreset("conservative");
      // feature-dev M default = $24.00, conservative = $12.00
      expect(conservative["feature-dev"].M).toBe(12.0);
      expect(conservative["feature-dev"].XL).toBe(40.0);
    });

    it("should return generous preset at 200% of defaults", () => {
      const generous = getBudgetPreset("generous");
      // feature-dev M default = $24.00, generous = $48.00
      expect(generous["feature-dev"].M).toBe(48.0);
      expect(generous["feature-dev"].XL).toBe(160.0);
    });

    it("should include all stages", () => {
      const preset = getBudgetPreset("conservative");
      const stages = Object.keys(DEFAULT_SIZE_AWARE_BUDGETS);
      expect(Object.keys(preset)).toEqual(stages);
    });
  });

  // ==========================================================================
  // Planning-Aware Size Resolution (Issue #1333)
  // ==========================================================================

  describe("resolveEffectiveSize", () => {
    it("should return issue size when no planning hint", () => {
      expect(resolveEffectiveSize("S")).toBe("S");
      expect(resolveEffectiveSize("S", null)).toBe("S");
      expect(resolveEffectiveSize("S", undefined)).toBe("S");
    });

    it("should use planner size when higher than issue size", () => {
      expect(resolveEffectiveSize("S", { assessedSize: "M" })).toBe("M");
      expect(resolveEffectiveSize("XS", { assessedSize: "L" })).toBe("L");
    });

    it("should never downgrade below issue size", () => {
      expect(resolveEffectiveSize("M", { assessedSize: "S" })).toBe("M");
      expect(resolveEffectiveSize("L", { assessedSize: "XS" })).toBe("L");
    });

    it("should use same size when planner agrees with issue label", () => {
      expect(resolveEffectiveSize("M", { assessedSize: "M" })).toBe("M");
    });

    it("should bump size up by one when file count exceeds threshold", () => {
      // S threshold is 6, so >6 files bumps S -> M
      expect(resolveEffectiveSize("S", { totalFileCount: 7 })).toBe("M");
      // XS threshold is 3, so >3 files bumps XS -> S
      expect(resolveEffectiveSize("XS", { totalFileCount: 4 })).toBe("S");
      // M threshold is 12, so >12 files bumps M -> L
      expect(resolveEffectiveSize("M", { totalFileCount: 13 })).toBe("L");
    });

    it("should not bump past XL", () => {
      expect(resolveEffectiveSize("XL", { totalFileCount: 100 })).toBe("XL");
    });

    it("should not bump when file count is at or below threshold", () => {
      // S threshold is 6
      expect(resolveEffectiveSize("S", { totalFileCount: 6 })).toBe("S");
      expect(resolveEffectiveSize("S", { totalFileCount: 3 })).toBe("S");
    });

    it("should take max of planner size and file-count bump", () => {
      // Planner says M, file count of 5 doesn't exceed M threshold (12)
      expect(resolveEffectiveSize("S", { assessedSize: "M", totalFileCount: 5 })).toBe("M");
      // Planner says L, file count of 5 doesn't exceed L threshold (25)
      expect(resolveEffectiveSize("S", { assessedSize: "L", totalFileCount: 5 })).toBe("L");
    });

    it("should bump from planner-upgraded size if file count also suggests higher", () => {
      // Issue is S, planner says M, file count >12 (M threshold) -> bumps to L
      expect(resolveEffectiveSize("S", { assessedSize: "M", totalFileCount: 15 })).toBe("L");
    });

    it("should handle empty planning hint gracefully", () => {
      expect(resolveEffectiveSize("M", {})).toBe("M");
    });

    it("should handle null assessedSize in hint", () => {
      expect(resolveEffectiveSize("M", { assessedSize: null, totalFileCount: 2 })).toBe("M");
    });

    it("should reproduce the Issue #1333 scenario", () => {
      // Issue #1333: GitHub label S, planner said S, but 12 files total
      // S threshold is 6, 12 > 6 -> bumps to M
      const hint: PlanningBudgetHint = {
        assessedSize: "S",
        totalFileCount: 12,
      };
      const effective = resolveEffectiveSize("S", hint);
      expect(effective).toBe("M");

      // Verify the resulting budget would have been sufficient
      const enforcer = new BudgetEnforcer();
      const effectiveLimit = enforcer.getEffectiveLimit("feature-dev", effective);
      expect(effectiveLimit).toBe(36.0); // $24.00 * 1.5 = $36.00
      expect(7.29).toBeLessThan(effectiveLimit); // $7.29 < $36.00
    });
  });
});

/**
 * Regression tests for BudgetEnforcer feature-validate hard limit (Issue #1336)
 *
 * The anomalous run (feature-validate, issue #788, 2026-02-17) logged $11.88.
 * With the delta-conversion fix in place, checkBudget() now sees the TRUE per-stage
 * cost. These tests verify that the M-size $10.00 hard limit would catch future
 * runaway validate stages, and that the $11.88 anomaly would trigger a warning
 * (base > $10) but not termination (effective limit = $15.00).
 */
describe("BudgetEnforcer — feature-validate M hard limit regression (Issue #1336)", () => {
  it("feature-validate M base budget is $20.00", () => {
    expect(DEFAULT_SIZE_AWARE_BUDGETS["feature-validate"].M).toBe(20.0);
  });

  it("feature-validate M effective limit is $30.00 (50% grace)", () => {
    const enforcer = new BudgetEnforcer();
    // $20.00 × 1.5 = $30.00
    expect(enforcer.getEffectiveLimit("feature-validate", "M")).toBe(30.0);
  });

  it("warns when feature-validate cost exceeds $20.00 but stays below $30.00", () => {
    const enforcer = new BudgetEnforcer({ mode: "hard" });
    // $21.00 exceeds base budget — should warn but NOT terminate
    const decision = enforcer.checkBudget("feature-validate", 21.0, "M");
    expect(decision.shouldWarn).toBe(true);
    expect(decision.shouldTerminate).toBe(false);
  });

  it("terminates feature-validate when cost exceeds $30.00 effective limit", () => {
    const enforcer = new BudgetEnforcer({ mode: "hard" });
    const decision = enforcer.checkBudget("feature-validate", 30.01, "M");
    expect(decision.shouldTerminate).toBe(true);
    expect(decision.shouldWarn).toBe(false);
  });

  it("does not warn or terminate when feature-validate cost is under $15.00", () => {
    const enforcer = new BudgetEnforcer({ mode: "hard" });
    // Healthy runs: typical costs well under the $15.00 M budget
    for (const cost of [0.895, 5.0, 11.88]) {
      const decision = enforcer.checkBudget("feature-validate", cost, "M");
      expect(decision.shouldWarn).toBe(false);
      expect(decision.shouldTerminate).toBe(false);
    }
  });

  it("termination message includes actual cost and effective limit", () => {
    const enforcer = new BudgetEnforcer({ mode: "hard" });
    const decision = enforcer.checkBudget("feature-validate", 31.0, "M");
    expect(decision.shouldTerminate).toBe(true);
    expect(decision.message).toContain("$31.00");
    expect(decision.message).toContain("$30.00");
  });

  // Issue #2338 - Wind-down signal tests
  describe("checkBudget - wind-down signal", () => {
    it("should signal wind-down at 80% of base budget (hard mode)", () => {
      // feature-dev M = $24.00, 80% = $19.20
      const enforcer = new BudgetEnforcer({ mode: "hard" });
      const decision = enforcer.checkBudget("feature-dev", 20.0, "M");
      expect(decision.shouldWindDown).toBe(true);
      expect(decision.shouldWarn).toBe(false);
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.message).toContain("BUDGET WIND-DOWN");
    });

    it("should not signal wind-down when under threshold", () => {
      const enforcer = new BudgetEnforcer({ mode: "hard" });
      // feature-dev M = $24.00, 80% = $19.20
      const decision = enforcer.checkBudget("feature-dev", 10.0, "M");
      expect(decision.shouldWindDown).toBe(false);
      expect(decision.shouldWarn).toBe(false);
      expect(decision.shouldTerminate).toBe(false);
    });

    it("should not signal wind-down when already over base budget (warn takes over)", () => {
      const enforcer = new BudgetEnforcer({ mode: "hard" });
      // Over base ($24.00) but under effective limit ($36.00) — warn, not wind-down
      const decision = enforcer.checkBudget("feature-dev", 30.0, "M");
      expect(decision.shouldWindDown).toBe(false);
      expect(decision.shouldWarn).toBe(true);
      expect(decision.shouldTerminate).toBe(false);
    });

    it("should signal wind-down in soft mode too", () => {
      const enforcer = new BudgetEnforcer({ mode: "soft" });
      const decision = enforcer.checkBudget("feature-dev", 20.0, "M");
      expect(decision.shouldWindDown).toBe(true);
      expect(decision.shouldWarn).toBe(false);
      expect(decision.shouldTerminate).toBe(false);
    });

    it("should signal wind-down in threshold mode", () => {
      const enforcer = new BudgetEnforcer({ mode: "threshold" });
      const decision = enforcer.checkBudget("feature-dev", 20.0, "M");
      expect(decision.shouldWindDown).toBe(true);
      expect(decision.shouldWarn).toBe(false);
    });

    it("should use configurable wind-down percent", () => {
      // Set wind-down at 60% instead of default 80%
      const enforcer = new BudgetEnforcer({
        mode: "hard",
        windDownPercent: 60,
      });
      // feature-dev M = $24.00, 60% = $14.40
      const at15 = enforcer.checkBudget("feature-dev", 15.0, "M");
      expect(at15.shouldWindDown).toBe(true);

      const at8 = enforcer.checkBudget("feature-dev", 8.0, "M");
      expect(at8.shouldWindDown).toBe(false);
    });

    it("wind-down message includes threshold percentage", () => {
      const enforcer = new BudgetEnforcer({ mode: "hard" });
      const decision = enforcer.checkBudget("feature-dev", 20.0, "M");
      expect(decision.message).toContain("80%");
      expect(decision.message).toContain("wind-down");
    });

    it("should not signal wind-down for stages with no budget", () => {
      const enforcer = new BudgetEnforcer({ mode: "hard" });
      const decision = enforcer.checkBudget("unknown-stage", 100.0);
      expect(decision.shouldWindDown).toBe(false);
      expect(decision.shouldWarn).toBe(false);
      expect(decision.shouldTerminate).toBe(false);
    });
  });
});

// Regression: stage-budget terminations were reporting pipeline-total cost
// instead of the stage cost, distorting the failure comment, the
// budget-overrun-{N}.json retry context, and the calibration signal.
// @see Issue #3120
describe("resolveStageCostUsd (Issue #3120)", () => {
  it("prefers the streamed stage cost when > 0", () => {
    const perStage = { "pr-create": 1.23, "feature-dev": 9.99 };
    expect(resolveStageCostUsd(4.79, perStage, "pr-create")).toBe(4.79);
  });

  it("falls back to per-stage cost from state when stream is 0", () => {
    const perStage = { "pr-create": 4.79, "feature-dev": 5.97 };
    expect(resolveStageCostUsd(0, perStage, "pr-create")).toBe(4.79);
  });

  it("never returns the pipeline-total scope (regression for #3120)", () => {
    // Reproduces the #3076 scenario: planning $2.97 + dev $5.97 + validate
    // $2.71 + pr-create $4.79 = $16.44 pipeline total. Even when the streamed
    // cost is 0, we must report the pr-create stage cost ($4.79), not the
    // pipeline aggregate ($16.44) that would be in state.tokens.estimated_cost_usd.
    const perStage = {
      "feature-planning": 2.97,
      "feature-dev": 5.97,
      "feature-validate": 2.71,
      "pr-create": 4.79,
    };
    const resolved = resolveStageCostUsd(0, perStage, "pr-create");
    expect(resolved).toBe(4.79);
    expect(resolved).not.toBe(16.44);
  });

  it("returns 0 when no per-stage data is available", () => {
    expect(resolveStageCostUsd(0, undefined, "pr-create")).toBe(0);
    expect(resolveStageCostUsd(0, {}, "pr-create")).toBe(0);
  });

  it("returns 0 for unknown stages even when other stages have data", () => {
    const perStage = { "pr-create": 4.79 };
    expect(resolveStageCostUsd(0, perStage, "feature-dev")).toBe(0);
  });

  it("ignores zero per-stage entries and reports 0", () => {
    const perStage = { "pr-create": 0 };
    expect(resolveStageCostUsd(0, perStage, "pr-create")).toBe(0);
  });
});

describe("BudgetEnforcer — mode-aware model scaling (2026-05-04 incidents)", () => {
  // Anchors:
  //   - Issue #871 feature-dev: terminated at $23.03 (cost-at-kill) /
  //     $25.31 final, against a $5 cost-cap × 3.0 = $15 effective. The fix
  //     bumps opus:high to 5.0× so feature-dev gets $25 effective.
  //   - Issue #331 pr-create: terminated at $5.74 against $4.50 effective.
  //     pr-create base = $1.50 (M), generous preset (×2) = $3, hard mode +
  //     50% grace = $4.50. Without scale, MAXIMUM mode kills every pr-create.
  // Both come from the same root cause: BudgetEnforcer didn't apply the
  // mode-aware scale that the cost-cap path used.

  describe("opus:high scaling", () => {
    it("widens pr-create M generous-preset effective limit so $5.74 (issue #331) does NOT terminate", () => {
      // generous preset on pr-create M: $1.50 × 2 = $3 base, ×1.5 grace = $4.50.
      // With opus:high 5.0× on top, base becomes $15, effective $22.50 — well
      // above the $5.74 incident spend.
      const enforcer = new BudgetEnforcer({
        mode: "hard",
        gracePercent: 50,
        stageOverrides: {
          "pr-create": { XS: 0.5 * 2, S: 0.75 * 2, M: 1.5 * 2, L: 2.0 * 2, XL: 3.0 * 2 },
        },
      });
      const decision = enforcer.checkBudget("pr-create", 5.74, "M", {
        model: "claude-opus-4-7",
        effort: "high",
      });
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.effectiveLimit).toBeGreaterThan(5.74);
    });

    it("widens feature-dev M generous-preset effective limit so $23.03 (issue #871) does NOT terminate", () => {
      // generous preset on feature-dev M: $20 × 2 = $40 base, ×1.5 grace = $60.
      // With opus:high 5.0× this becomes huge — the BudgetEnforcer was never
      // the limit that fired on #871 (the cost-cap kill at $15 fired first),
      // but verify the BudgetEnforcer alone would also pass.
      const enforcer = new BudgetEnforcer({
        mode: "hard",
        gracePercent: 50,
        stageOverrides: {
          "feature-dev": { XS: 5 * 2, S: 10 * 2, M: 20 * 2, L: 50 * 2, XL: 80 * 2 },
        },
      });
      const decision = enforcer.checkBudget("feature-dev", 23.03, "M", {
        model: "claude-opus-4-7",
        effort: "high",
      });
      expect(decision.shouldTerminate).toBe(false);
    });

    it("still terminates extreme runaways even with opus:high scaling", () => {
      // Ceiling regression: an unscaled generous pr-create has base $3 /
      // effective $4.50. With 5.0× scale, base $15 / effective $22.50.
      // A $100 spend must still trip terminate.
      const enforcer = new BudgetEnforcer({
        mode: "hard",
        gracePercent: 50,
        stageOverrides: {
          "pr-create": { XS: 0.5 * 2, S: 0.75 * 2, M: 1.5 * 2, L: 2.0 * 2, XL: 3.0 * 2 },
        },
      });
      const decision = enforcer.checkBudget("pr-create", 100.0, "M", {
        model: "claude-opus-4-7",
        effort: "high",
      });
      expect(decision.shouldTerminate).toBe(true);
    });
  });

  describe("Sonnet medium (no scale) preserves prior behavior", () => {
    it("keeps the unscaled $4.50 pr-create effective limit when model is Sonnet medium", () => {
      const enforcer = new BudgetEnforcer({
        mode: "hard",
        gracePercent: 50,
        stageOverrides: {
          "pr-create": { XS: 0.5 * 2, S: 0.75 * 2, M: 1.5 * 2, L: 2.0 * 2, XL: 3.0 * 2 },
        },
      });
      const decision = enforcer.checkBudget("pr-create", 5.74, "M", {
        model: "claude-sonnet-4-6",
        effort: "medium",
      });
      // Without scaling, $5.74 trips effective $4.50. This matches pre-fix
      // behavior for Sonnet so calibration baselines aren't disturbed.
      expect(decision.shouldTerminate).toBe(true);
      expect(decision.effectiveLimit).toBeCloseTo(4.5, 2);
    });
  });

  describe("undefined modelInfo preserves pre-fix behavior", () => {
    it("equals scale = 1.0 when modelInfo is omitted (back-compat)", () => {
      const enforcer = new BudgetEnforcer({ mode: "hard", gracePercent: 50 });
      const withInfo = enforcer.checkBudget("pr-create", 1.0, "M", undefined);
      const withoutInfo = enforcer.checkBudget("pr-create", 1.0, "M");
      expect(withInfo.effectiveLimit).toBe(withoutInfo.effectiveLimit);
    });
  });

  describe("getEffectiveLimit reflects scaling", () => {
    it("returns base × scale × grace when modelInfo is supplied", () => {
      const enforcer = new BudgetEnforcer({ mode: "hard", gracePercent: 50 });
      // pr-create M default = $3.00 (#265; no preset multiplier in this enforcer).
      // grace = 1.5. Sonnet medium → 1.0× → $3.00 × 1.5 = $4.50.
      // Opus high → 5.0× → $3.00 × 5.0 × 1.5 = $22.50.
      const baseEff = enforcer.getEffectiveLimit("pr-create", "M");
      const sonnetEff = enforcer.getEffectiveLimit("pr-create", "M", {
        model: "sonnet",
        effort: "medium",
      });
      const opusHighEff = enforcer.getEffectiveLimit("pr-create", "M", {
        model: "opus",
        effort: "high",
      });
      expect(baseEff).toBeCloseTo(4.5, 2);
      expect(sonnetEff).toBeCloseTo(4.5, 2);
      expect(opusHighEff).toBeCloseTo(22.5, 2);
    });
  });
});
