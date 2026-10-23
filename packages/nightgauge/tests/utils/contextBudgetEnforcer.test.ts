/**
 * Unit tests for ContextBudgetEnforcer
 *
 * Tests all enforcement modes (hard/soft/threshold), size-aware budget lookup,
 * grace buffer calculation, config overrides, and message formatting.
 *
 * @see contextBudgetEnforcer.ts
 * @see Issue #790 - Per-stage context budgets
 */

import { describe, it, expect } from "vitest";
import {
  ContextBudgetEnforcer,
  DEFAULT_INPUT_TOKEN_BUDGETS,
  DEFAULT_CONTEXT_BUDGET_MODE,
  DEFAULT_CONTEXT_GRACE_PERCENT,
  type InputTokenEnforcementDecision,
} from "../../src/utils/contextBudgetEnforcer";
import type { BudgetMode, SizeLabel } from "../../src/utils/budgetEnforcer";

describe("ContextBudgetEnforcer", () => {
  describe("defaults", () => {
    it("should default to soft mode with 50% grace", () => {
      expect(DEFAULT_CONTEXT_BUDGET_MODE).toBe("soft");
      expect(DEFAULT_CONTEXT_GRACE_PERCENT).toBe(50);
    });

    it("should have size-aware input token budgets for all pipeline stages", () => {
      const expectedStages = [
        "issue-pickup",
        "feature-planning",
        "feature-dev",
        "feature-validate",
        "pr-create",
        "pr-merge",
      ];

      for (const stage of expectedStages) {
        expect(DEFAULT_INPUT_TOKEN_BUDGETS[stage]).toBeDefined();
        const budget = DEFAULT_INPUT_TOKEN_BUDGETS[stage];
        expect(budget.XS).toBeGreaterThan(0);
        expect(budget.S).toBeGreaterThan(0);
        expect(budget.M).toBeGreaterThan(0);
        expect(budget.L).toBeGreaterThan(0);
        expect(budget.XL).toBeGreaterThan(0);
      }
    });

    it("should have feature-dev M budget of 250000 tokens", () => {
      expect(DEFAULT_INPUT_TOKEN_BUDGETS["feature-dev"].M).toBe(250000);
    });

    it("should have issue-pickup M budget of 15000 tokens", () => {
      expect(DEFAULT_INPUT_TOKEN_BUDGETS["issue-pickup"].M).toBe(15000);
    });

    it("should have feature-planning M budget of 120000 tokens", () => {
      expect(DEFAULT_INPUT_TOKEN_BUDGETS["feature-planning"].M).toBe(120000);
    });

    it("should have feature-validate M budget of 120000 tokens", () => {
      expect(DEFAULT_INPUT_TOKEN_BUDGETS["feature-validate"].M).toBe(120000);
    });

    it("should have pr-create M budget of 15000 tokens", () => {
      expect(DEFAULT_INPUT_TOKEN_BUDGETS["pr-create"].M).toBe(15000);
    });

    it("should have pr-merge M budget of 25000 tokens", () => {
      expect(DEFAULT_INPUT_TOKEN_BUDGETS["pr-merge"].M).toBe(25000);
    });
  });

  describe("getBaseContextBudget", () => {
    it("should return size-aware budget for known stage and size", () => {
      const enforcer = new ContextBudgetEnforcer();
      expect(enforcer.getBaseContextBudget("feature-dev", "XS")).toBe(100000);
      expect(enforcer.getBaseContextBudget("feature-dev", "M")).toBe(250000);
      expect(enforcer.getBaseContextBudget("feature-dev", "XL")).toBe(600000);
    });

    it("should return correct budgets for all sizes", () => {
      const enforcer = new ContextBudgetEnforcer();
      expect(enforcer.getBaseContextBudget("feature-dev", "XS")).toBe(100000);
      expect(enforcer.getBaseContextBudget("feature-dev", "S")).toBe(150000);
      expect(enforcer.getBaseContextBudget("feature-dev", "M")).toBe(250000);
      expect(enforcer.getBaseContextBudget("feature-dev", "L")).toBe(400000);
      expect(enforcer.getBaseContextBudget("feature-dev", "XL")).toBe(600000);
    });

    it("should fall back to M when no size label provided", () => {
      const enforcer = new ContextBudgetEnforcer();
      expect(enforcer.getBaseContextBudget("feature-dev")).toBe(250000);
    });

    it("should return 0 for unknown stage", () => {
      const enforcer = new ContextBudgetEnforcer();
      expect(enforcer.getBaseContextBudget("unknown-stage")).toBe(0);
    });

    it("should return 0 when enforcer is disabled", () => {
      const enforcer = new ContextBudgetEnforcer({ enabled: false });
      expect(enforcer.getBaseContextBudget("feature-dev", "M")).toBe(0);
    });

    it("should use flat config override when provided", () => {
      const enforcer = new ContextBudgetEnforcer({
        stageOverrides: { "feature-dev": 300000 },
      });
      expect(enforcer.getBaseContextBudget("feature-dev", "M")).toBe(300000);
      expect(enforcer.getBaseContextBudget("feature-dev", "XL")).toBe(300000);
    });

    it("should use size-aware config override when provided", () => {
      const enforcer = new ContextBudgetEnforcer({
        stageOverrides: {
          "feature-dev": { M: 200000, L: 500000 },
        },
      });
      expect(enforcer.getBaseContextBudget("feature-dev", "M")).toBe(200000);
      expect(enforcer.getBaseContextBudget("feature-dev", "L")).toBe(500000);
      // Size not in override falls back to M in override
      expect(enforcer.getBaseContextBudget("feature-dev", "XS")).toBe(200000);
    });

    it("should fall back to M in override when size not present", () => {
      const enforcer = new ContextBudgetEnforcer({
        stageOverrides: {
          "feature-planning": { M: 150000, XL: 400000 },
        },
      });
      // XS not in override → use M from override
      expect(enforcer.getBaseContextBudget("feature-planning", "XS")).toBe(150000);
      expect(enforcer.getBaseContextBudget("feature-planning", "XL")).toBe(400000);
    });

    it("should fall back to defaults for non-overridden stages", () => {
      const enforcer = new ContextBudgetEnforcer({
        stageOverrides: { "feature-dev": 300000 },
      });
      expect(enforcer.getBaseContextBudget("pr-create", "M")).toBe(15000);
    });
  });

  describe("getEffectiveContextLimit", () => {
    it("should apply 50% grace buffer by default", () => {
      const enforcer = new ContextBudgetEnforcer();
      // feature-dev M = 250000, effective = 250000 * 1.5 = 375000
      expect(enforcer.getEffectiveContextLimit("feature-dev", "M")).toBe(375000);
    });

    it("should apply custom grace percent", () => {
      const enforcer = new ContextBudgetEnforcer({ gracePercent: 100 });
      // feature-dev M = 250000, effective = 250000 * 2.0 = 500000
      expect(enforcer.getEffectiveContextLimit("feature-dev", "M")).toBe(500000);
    });

    it("should return 0 for unknown stage", () => {
      const enforcer = new ContextBudgetEnforcer();
      expect(enforcer.getEffectiveContextLimit("unknown-stage")).toBe(0);
    });

    it("should apply 0% grace correctly", () => {
      const enforcer = new ContextBudgetEnforcer({ gracePercent: 0 });
      // feature-dev M = 250000, effective = 250000 * 1.0 = 250000
      expect(enforcer.getEffectiveContextLimit("feature-dev", "M")).toBe(250000);
    });

    it("should round effective limit to nearest integer", () => {
      const enforcer = new ContextBudgetEnforcer({ gracePercent: 33 });
      // issue-pickup M = 15000, effective = 15000 * 1.33 = 19950
      expect(enforcer.getEffectiveContextLimit("issue-pickup", "M")).toBe(19950);
    });
  });

  describe("checkInputTokens - soft mode", () => {
    const enforcer = new ContextBudgetEnforcer({ mode: "soft" });

    it("should never terminate, even when far over budget", () => {
      const decision = enforcer.checkInputTokens("feature-dev", 1000000, "M");
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.shouldWarn).toBe(true);
      expect(decision.budgetMode).toBe("soft");
    });

    it("should warn when over base budget", () => {
      const decision = enforcer.checkInputTokens("feature-dev", 260000, "M");
      expect(decision.shouldWarn).toBe(true);
      expect(decision.message).toContain("CONTEXT BUDGET WARNING");
    });

    it("should not warn when under budget", () => {
      const decision = enforcer.checkInputTokens("feature-dev", 100000, "M");
      expect(decision.shouldWarn).toBe(false);
    });

    it("should not warn at exactly the base budget", () => {
      const decision = enforcer.checkInputTokens("feature-dev", 250000, "M");
      expect(decision.shouldWarn).toBe(false);
      expect(decision.shouldTerminate).toBe(false);
    });

    it("should warn at exactly base budget + 1", () => {
      const decision = enforcer.checkInputTokens("feature-dev", 250001, "M");
      expect(decision.shouldWarn).toBe(true);
      expect(decision.shouldTerminate).toBe(false);
    });
  });

  describe("checkInputTokens - hard mode", () => {
    const enforcer = new ContextBudgetEnforcer({ mode: "hard" });

    it("should not warn or terminate when under budget", () => {
      const decision = enforcer.checkInputTokens("feature-dev", 100000, "M");
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.shouldWarn).toBe(false);
      expect(decision.message).toBe("");
    });

    it("should warn but not terminate when over budget but under grace limit", () => {
      // M budget = 250000, effective limit = 375000
      const decision = enforcer.checkInputTokens("feature-dev", 300000, "M");
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.shouldWarn).toBe(true);
      expect(decision.message).toContain("CONTEXT BUDGET WARNING");
      expect(decision.message).toContain("300,000");
    });

    it("should terminate when over effective limit", () => {
      // M budget = 250000, effective limit = 375000
      const decision = enforcer.checkInputTokens("feature-dev", 400000, "M");
      expect(decision.shouldTerminate).toBe(true);
      expect(decision.shouldWarn).toBe(false);
      expect(decision.message).toContain("CONTEXT BUDGET EXCEEDED");
      expect(decision.message).toContain("400,000");
      expect(decision.message).toContain("375,000");
    });

    it("should terminate exactly at the effective limit boundary", () => {
      // M budget = 250000, effective limit = 375000
      const decision = enforcer.checkInputTokens("feature-dev", 375001, "M");
      expect(decision.shouldTerminate).toBe(true);
    });

    it("should not terminate at exactly the effective limit", () => {
      const decision = enforcer.checkInputTokens("feature-dev", 375000, "M");
      // tokens not > effectiveLimit, equal
      expect(decision.shouldTerminate).toBe(false);
    });

    it("should include correct metadata in decision", () => {
      const decision = enforcer.checkInputTokens("feature-dev", 300000, "M");
      expect(decision.currentInputTokens).toBe(300000);
      expect(decision.effectiveLimit).toBe(375000);
      expect(decision.budgetMode).toBe("hard");
    });
  });

  describe("checkInputTokens - threshold mode", () => {
    it("should terminate at configured threshold percentage", () => {
      const enforcer = new ContextBudgetEnforcer({
        mode: "threshold",
        gracePercent: 25,
      });
      // M budget = 250000, effective = 250000 * 1.25 = 312500
      const decision = enforcer.checkInputTokens("feature-dev", 320000, "M");
      expect(decision.shouldTerminate).toBe(true);
      expect(decision.budgetMode).toBe("threshold");
    });

    it("should warn but not terminate between budget and threshold", () => {
      const enforcer = new ContextBudgetEnforcer({
        mode: "threshold",
        gracePercent: 25,
      });
      const decision = enforcer.checkInputTokens("feature-dev", 280000, "M");
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.shouldWarn).toBe(true);
    });

    it("should not warn or terminate under base budget", () => {
      const enforcer = new ContextBudgetEnforcer({
        mode: "threshold",
        gracePercent: 25,
      });
      const decision = enforcer.checkInputTokens("feature-dev", 200000, "M");
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.shouldWarn).toBe(false);
    });
  });

  describe("disabled enforcer", () => {
    const enforcer = new ContextBudgetEnforcer({ enabled: false });

    it("should never warn when disabled", () => {
      const decision = enforcer.checkInputTokens("feature-dev", 1000000, "M");
      expect(decision.shouldWarn).toBe(false);
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.message).toBe("");
      expect(decision.effectiveLimit).toBe(0);
    });

    it("should never terminate when disabled", () => {
      const decision = enforcer.checkInputTokens("feature-dev", 10000000, "M");
      expect(decision.shouldTerminate).toBe(false);
    });

    it("should return 0 effective limit when disabled", () => {
      expect(enforcer.getEffectiveContextLimit("feature-dev", "M")).toBe(0);
    });

    it("should return 0 base budget when disabled", () => {
      expect(enforcer.getBaseContextBudget("feature-dev", "M")).toBe(0);
    });
  });

  describe("size-aware lookup", () => {
    const enforcer = new ContextBudgetEnforcer();

    it("should return correct budget for each size", () => {
      const sizes: SizeLabel[] = ["XS", "S", "M", "L", "XL"];
      const expected = [100000, 150000, 250000, 400000, 600000]; // feature-dev budgets

      for (let i = 0; i < sizes.length; i++) {
        expect(enforcer.getBaseContextBudget("feature-dev", sizes[i])).toBe(expected[i]);
      }
    });

    it("should use M fallback for missing size", () => {
      // When no sizeLabel is provided
      const decision = enforcer.checkInputTokens("feature-dev", 200000);
      // M budget = 250000 — under budget
      expect(decision.shouldWarn).toBe(false);
      expect(decision.shouldTerminate).toBe(false);
    });

    it("should give more budget to larger issues", () => {
      expect(enforcer.getBaseContextBudget("feature-dev", "XS")).toBeLessThan(
        enforcer.getBaseContextBudget("feature-dev", "S")
      );
      expect(enforcer.getBaseContextBudget("feature-dev", "S")).toBeLessThan(
        enforcer.getBaseContextBudget("feature-dev", "M")
      );
      expect(enforcer.getBaseContextBudget("feature-dev", "M")).toBeLessThan(
        enforcer.getBaseContextBudget("feature-dev", "L")
      );
      expect(enforcer.getBaseContextBudget("feature-dev", "L")).toBeLessThan(
        enforcer.getBaseContextBudget("feature-dev", "XL")
      );
    });

    it("should have increasing budgets across all stages", () => {
      const stages = [
        "issue-pickup",
        "feature-planning",
        "feature-dev",
        "feature-validate",
        "pr-create",
        "pr-merge",
      ];

      for (const stage of stages) {
        const budgets = DEFAULT_INPUT_TOKEN_BUDGETS[stage];
        expect(budgets.XS).toBeLessThan(budgets.S);
        expect(budgets.S).toBeLessThan(budgets.M);
        expect(budgets.M).toBeLessThan(budgets.L);
        expect(budgets.L).toBeLessThan(budgets.XL);
      }
    });
  });

  describe("unknown stages", () => {
    const enforcer = new ContextBudgetEnforcer();

    it("should not warn or terminate for unknown stages", () => {
      const decision = enforcer.checkInputTokens("unknown-stage", 1000000);
      expect(decision.shouldTerminate).toBe(false);
      expect(decision.shouldWarn).toBe(false);
      expect(decision.message).toBe("");
    });

    it("should return 0 base budget for unknown stage", () => {
      expect(enforcer.getBaseContextBudget("unknown-stage", "M")).toBe(0);
    });

    it("should return 0 effective limit for unknown stage", () => {
      expect(enforcer.getEffectiveContextLimit("unknown-stage", "M")).toBe(0);
    });

    it("should have 0 effective limit in decision for unknown stage", () => {
      const decision = enforcer.checkInputTokens("unknown-stage", 100000);
      expect(decision.effectiveLimit).toBe(0);
    });
  });

  describe("message formatting", () => {
    const enforcer = new ContextBudgetEnforcer();

    it("should format termination message with stage name and token counts", () => {
      const msg = enforcer.formatTerminationMessage("feature-dev", 400000, 375000);
      expect(msg).toContain("CONTEXT BUDGET EXCEEDED");
      expect(msg).toContain("feature-dev");
      expect(msg).toContain("400,000");
      expect(msg).toContain("375,000");
    });

    it("should format warning message with stage name and token counts", () => {
      const msg = enforcer.formatWarningMessage("feature-dev", 260000, 250000);
      expect(msg).toContain("CONTEXT BUDGET WARNING");
      expect(msg).toContain("feature-dev");
      expect(msg).toContain("260,000");
      expect(msg).toContain("250,000");
    });

    it("should format large token counts with commas", () => {
      const msg = enforcer.formatWarningMessage("feature-planning", 150000, 120000);
      expect(msg).toContain("150,000");
      expect(msg).toContain("120,000");
    });

    it('should include "terminated" in termination message', () => {
      const msg = enforcer.formatTerminationMessage("feature-dev", 400000, 375000);
      expect(msg).toContain("terminated");
    });

    it('should include "exceeding budget" in warning message', () => {
      const msg = enforcer.formatWarningMessage("feature-dev", 260000, 250000);
      expect(msg).toContain("exceeding budget");
    });
  });

  describe("decision metadata", () => {
    it("should include current input tokens and effective limit in decision", () => {
      const enforcer = new ContextBudgetEnforcer();
      const decision = enforcer.checkInputTokens("feature-dev", 200000, "M");
      expect(decision.currentInputTokens).toBe(200000);
      expect(decision.effectiveLimit).toBe(375000);
      expect(decision.budgetMode).toBe("soft"); // default mode
    });

    it("should include correct budget mode in decision", () => {
      const hardEnforcer = new ContextBudgetEnforcer({ mode: "hard" });
      const decision = hardEnforcer.checkInputTokens("feature-dev", 200000, "M");
      expect(decision.budgetMode).toBe("hard");
    });

    it("should include message in decision when warning", () => {
      const enforcer = new ContextBudgetEnforcer({ mode: "soft" });
      const decision = enforcer.checkInputTokens("feature-dev", 300000, "M");
      expect(decision.shouldWarn).toBe(true);
      expect(decision.message).not.toBe("");
      expect(decision.message).toContain("CONTEXT BUDGET WARNING");
    });

    it("should include message in decision when terminating", () => {
      const enforcer = new ContextBudgetEnforcer({ mode: "hard" });
      const decision = enforcer.checkInputTokens("feature-dev", 400000, "M");
      expect(decision.shouldTerminate).toBe(true);
      expect(decision.message).not.toBe("");
      expect(decision.message).toContain("CONTEXT BUDGET EXCEEDED");
    });

    it("should have empty message when under budget", () => {
      const enforcer = new ContextBudgetEnforcer();
      const decision = enforcer.checkInputTokens("feature-dev", 100000, "M");
      expect(decision.message).toBe("");
    });
  });

  describe("issue-pickup stage", () => {
    const enforcer = new ContextBudgetEnforcer();

    it("should use M budget when size is unknown (issue-pickup)", () => {
      // issue-pickup M = 15000
      const decision = enforcer.checkInputTokens("issue-pickup", 10000);
      expect(decision.shouldWarn).toBe(false);
      expect(decision.shouldTerminate).toBe(false);
    });

    it("should enforce budget for issue-pickup stage", () => {
      // issue-pickup M = 15000, effective = 22500 (soft mode)
      const decision = enforcer.checkInputTokens("issue-pickup", 20000);
      expect(decision.shouldWarn).toBe(true);
    });

    it("should have correct XS budget for issue-pickup", () => {
      expect(enforcer.getBaseContextBudget("issue-pickup", "XS")).toBe(5000);
    });

    it("should have correct XL budget for issue-pickup", () => {
      expect(enforcer.getBaseContextBudget("issue-pickup", "XL")).toBe(40000);
    });
  });

  describe("pr-create stage", () => {
    const enforcer = new ContextBudgetEnforcer();

    it("should have correct budget values for all sizes", () => {
      expect(enforcer.getBaseContextBudget("pr-create", "XS")).toBe(5000);
      expect(enforcer.getBaseContextBudget("pr-create", "S")).toBe(8000);
      expect(enforcer.getBaseContextBudget("pr-create", "M")).toBe(15000);
      expect(enforcer.getBaseContextBudget("pr-create", "L")).toBe(25000);
      expect(enforcer.getBaseContextBudget("pr-create", "XL")).toBe(40000);
    });
  });

  describe("pr-merge stage", () => {
    const enforcer = new ContextBudgetEnforcer();

    it("should have correct budget values for all sizes", () => {
      expect(enforcer.getBaseContextBudget("pr-merge", "XS")).toBe(10000);
      expect(enforcer.getBaseContextBudget("pr-merge", "S")).toBe(15000);
      expect(enforcer.getBaseContextBudget("pr-merge", "M")).toBe(25000);
      expect(enforcer.getBaseContextBudget("pr-merge", "L")).toBe(40000);
      expect(enforcer.getBaseContextBudget("pr-merge", "XL")).toBe(60000);
    });
  });

  describe("feature-planning stage", () => {
    const enforcer = new ContextBudgetEnforcer();

    it("should have correct budget values for all sizes", () => {
      expect(enforcer.getBaseContextBudget("feature-planning", "XS")).toBe(50000);
      expect(enforcer.getBaseContextBudget("feature-planning", "S")).toBe(80000);
      expect(enforcer.getBaseContextBudget("feature-planning", "M")).toBe(120000);
      expect(enforcer.getBaseContextBudget("feature-planning", "L")).toBe(200000);
      expect(enforcer.getBaseContextBudget("feature-planning", "XL")).toBe(300000);
    });
  });

  describe("feature-validate stage", () => {
    const enforcer = new ContextBudgetEnforcer();

    it("should have correct budget values for all sizes", () => {
      expect(enforcer.getBaseContextBudget("feature-validate", "XS")).toBe(50000);
      expect(enforcer.getBaseContextBudget("feature-validate", "S")).toBe(80000);
      expect(enforcer.getBaseContextBudget("feature-validate", "M")).toBe(120000);
      expect(enforcer.getBaseContextBudget("feature-validate", "L")).toBe(200000);
      expect(enforcer.getBaseContextBudget("feature-validate", "XL")).toBe(300000);
    });
  });

  describe("config override edge cases", () => {
    it("should handle partial size-aware override with missing M", () => {
      const enforcer = new ContextBudgetEnforcer({
        stageOverrides: {
          "feature-dev": { XL: 800000 },
        },
      });
      // XL present in override
      expect(enforcer.getBaseContextBudget("feature-dev", "XL")).toBe(800000);
      // M not in override → undefined → fall back to default
      expect(enforcer.getBaseContextBudget("feature-dev", "M")).toBe(250000);
    });

    it("should handle flat override for all stages", () => {
      const enforcer = new ContextBudgetEnforcer({
        stageOverrides: {
          "issue-pickup": 20000,
          "feature-planning": 150000,
          "feature-dev": 300000,
          "feature-validate": 150000,
          "pr-create": 20000,
          "pr-merge": 30000,
        },
      });
      expect(enforcer.getBaseContextBudget("issue-pickup", "M")).toBe(20000);
      expect(enforcer.getBaseContextBudget("feature-planning", "XL")).toBe(150000);
      expect(enforcer.getBaseContextBudget("feature-dev", "XS")).toBe(300000);
    });

    it("should handle mixed flat and size-aware overrides", () => {
      const enforcer = new ContextBudgetEnforcer({
        stageOverrides: {
          "feature-dev": 350000, // flat
          "feature-planning": { M: 150000, L: 250000 }, // size-aware
        },
      });
      expect(enforcer.getBaseContextBudget("feature-dev", "XL")).toBe(350000);
      expect(enforcer.getBaseContextBudget("feature-planning", "M")).toBe(150000);
      expect(enforcer.getBaseContextBudget("feature-planning", "L")).toBe(250000);
    });
  });

  describe("boundary conditions", () => {
    const enforcer = new ContextBudgetEnforcer({ mode: "hard" });

    it("should handle 0 input tokens", () => {
      const decision = enforcer.checkInputTokens("feature-dev", 0, "M");
      expect(decision.shouldWarn).toBe(false);
      expect(decision.shouldTerminate).toBe(false);
    });

    it("should handle exactly base budget", () => {
      const decision = enforcer.checkInputTokens("feature-dev", 250000, "M");
      expect(decision.shouldWarn).toBe(false);
      expect(decision.shouldTerminate).toBe(false);
    });

    it("should handle exactly effective limit", () => {
      const decision = enforcer.checkInputTokens("feature-dev", 375000, "M");
      expect(decision.shouldWarn).toBe(true);
      expect(decision.shouldTerminate).toBe(false);
    });

    it("should handle base + 1", () => {
      const decision = enforcer.checkInputTokens("feature-dev", 250001, "M");
      expect(decision.shouldWarn).toBe(true);
      expect(decision.shouldTerminate).toBe(false);
    });

    it("should handle effective + 1", () => {
      const decision = enforcer.checkInputTokens("feature-dev", 375001, "M");
      expect(decision.shouldWarn).toBe(false);
      expect(decision.shouldTerminate).toBe(true);
    });
  });
});
