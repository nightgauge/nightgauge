/**
 * Unit tests for AutoModelSelector cost-per-success routing (Issue #2458)
 *
 * Tests the cost-aware model selection logic that prefers cheaper models
 * when their cost-per-success is comparable to the default selection.
 */

import { describe, it, expect } from "vitest";
import {
  AutoModelSelector,
  type CostPerSuccessContext,
  type IssueMetadata,
  type ModelStageHistory,
} from "../AutoModelSelector.js";

// --- Test helpers ---

function makeMetadata(overrides: Partial<IssueMetadata> = {}): IssueMetadata {
  return {
    labels: ["type:feature", "size:M"],
    title: "Add user authentication",
    ...overrides,
  };
}

function makeHistory(entries: Record<string, Partial<ModelStageHistory>>): CostPerSuccessContext {
  const history: Record<string, ModelStageHistory> = {};
  for (const [key, partial] of Object.entries(entries)) {
    history[key] = {
      totalCostUsd: partial.totalCostUsd ?? 10,
      successCount: partial.successCount ?? 5,
      totalCount: partial.totalCount ?? 5,
    };
  }
  return { history };
}

describe("AutoModelSelector — cost-per-success routing (Issue #2458)", () => {
  const selector = new AutoModelSelector();

  describe("computeCostPerSuccess — minimum requirements", () => {
    it("returns no-op when history is empty", () => {
      const ctx: CostPerSuccessContext = { history: {} };
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["size:L"] }),
        undefined,
        ctx
      );
      // L/XL dev uses opus; no history → no CPS routing
      expect(result.model).toBe("opus");
      expect(result.costPerSuccessRouting).toBeUndefined();
    });

    it("does not apply CPS routing when current model has no history", () => {
      const ctx = makeHistory({
        "sonnet:feature-dev": {
          totalCostUsd: 5,
          successCount: 10,
          totalCount: 10,
        },
      });
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["size:L"] }),
        undefined,
        ctx
      );
      // opus has no history → computeCostPerSuccess returns null → no routing
      expect(result.model).toBe("opus");
      expect(result.costPerSuccessRouting).toBeUndefined();
    });

    it("does not apply CPS routing when sample size is below minimum (n<5)", () => {
      const ctx = makeHistory({
        "opus:feature-dev": { totalCostUsd: 8, successCount: 2, totalCount: 2 },
        "sonnet:feature-dev": {
          totalCostUsd: 3,
          successCount: 2,
          totalCount: 2,
        },
      });
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["size:L"] }),
        undefined,
        ctx
      );
      expect(result.model).toBe("opus");
      expect(result.costPerSuccessRouting).toBeUndefined();
    });

    it("does not apply CPS routing when candidate success rate is below floor (70%)", () => {
      const ctx = makeHistory({
        "opus:feature-dev": {
          totalCostUsd: 20,
          successCount: 10,
          totalCount: 10,
        },
        "sonnet:feature-dev": {
          totalCostUsd: 5,
          successCount: 3, // 3/10 = 30% success rate
          totalCount: 10,
        },
      });
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["size:L"] }),
        undefined,
        ctx
      );
      expect(result.model).toBe("opus");
      expect(result.costPerSuccessRouting).toBeUndefined();
    });

    it("does not apply CPS routing when successCount is zero", () => {
      const ctx = makeHistory({
        "opus:feature-dev": {
          totalCostUsd: 10,
          successCount: 5,
          totalCount: 5,
        },
        "sonnet:feature-dev": {
          totalCostUsd: 5,
          successCount: 0,
          totalCount: 5,
        },
      });
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["size:L"] }),
        undefined,
        ctx
      );
      expect(result.model).toBe("opus");
      expect(result.costPerSuccessRouting).toBeUndefined();
    });
  });

  describe("cost-per-success calculation and preference", () => {
    it("prefers sonnet over opus when sonnet CPS is cheaper (within 20%)", () => {
      // opus CPS = 20/10 = $2.00/success
      // sonnet CPS = 5/10 = $0.50/success → ratio 0.25 ≤ 1.2 → prefer sonnet
      const ctx = makeHistory({
        "opus:feature-dev": {
          totalCostUsd: 20,
          successCount: 10,
          totalCount: 10,
        },
        "sonnet:feature-dev": {
          totalCostUsd: 5,
          successCount: 10,
          totalCount: 10,
        },
      });
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["size:L"] }),
        undefined,
        ctx
      );
      expect(result.model).toBe("sonnet");
      expect(result.costPerSuccessRouting?.applied).toBe(true);
      expect(result.costPerSuccessRouting?.fromModel).toBe("opus");
      expect(result.costPerSuccessRouting?.toModel).toBe("sonnet");
      expect(result.costPerSuccessRouting?.fromCostPerSuccess).toBeCloseTo(2.0, 4);
      expect(result.costPerSuccessRouting?.toCostPerSuccess).toBeCloseTo(0.5, 4);
      expect(result.reasoning).toContain("Cost-per-success");
    });

    it("prefers sonnet when CPS ratio equals threshold exactly (1.2)", () => {
      // opus CPS = 10/5 = $2.00/success
      // sonnet CPS = 12/5 = $2.40/success → ratio 2.40/2.00 = 1.2 exactly → prefer sonnet
      const ctx = makeHistory({
        "opus:feature-dev": {
          totalCostUsd: 10,
          successCount: 5,
          totalCount: 5,
        },
        "sonnet:feature-dev": {
          totalCostUsd: 12,
          successCount: 5,
          totalCount: 5,
        },
      });
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["size:L"] }),
        undefined,
        ctx
      );
      expect(result.model).toBe("sonnet");
      expect(result.costPerSuccessRouting?.applied).toBe(true);
    });

    it("keeps opus when sonnet CPS ratio exceeds threshold (>1.2)", () => {
      // opus CPS = 10/5 = $2.00/success
      // sonnet CPS = 13/5 = $2.60/success → ratio 1.3 > 1.2 → keep opus
      const ctx = makeHistory({
        "opus:feature-dev": {
          totalCostUsd: 10,
          successCount: 5,
          totalCount: 5,
        },
        "sonnet:feature-dev": {
          totalCostUsd: 13,
          successCount: 5,
          totalCount: 5,
        },
      });
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["size:L"] }),
        undefined,
        ctx
      );
      expect(result.model).toBe("opus");
      expect(result.costPerSuccessRouting).toBeUndefined();
    });

    it("uses custom maxCostRatioThreshold when provided", () => {
      // opus CPS = 10/5 = $2.00/success
      // sonnet CPS = 13/5 = $2.60/success → ratio 1.3
      // With threshold 1.5 → 1.3 ≤ 1.5 → prefer sonnet
      const ctx: CostPerSuccessContext = {
        ...makeHistory({
          "opus:feature-dev": {
            totalCostUsd: 10,
            successCount: 5,
            totalCount: 5,
          },
          "sonnet:feature-dev": {
            totalCostUsd: 13,
            successCount: 5,
            totalCount: 5,
          },
        }),
        maxCostRatioThreshold: 1.5,
      };
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["size:L"] }),
        undefined,
        ctx
      );
      expect(result.model).toBe("sonnet");
      expect(result.costPerSuccessRouting?.applied).toBe(true);
    });

    it("uses custom minSampleSize when provided", () => {
      // With minSampleSize=3, samples of 3 should be accepted
      const ctx: CostPerSuccessContext = {
        ...makeHistory({
          "opus:feature-dev": {
            totalCostUsd: 6,
            successCount: 3,
            totalCount: 3,
          },
          "sonnet:feature-dev": {
            totalCostUsd: 2,
            successCount: 3,
            totalCount: 3,
          },
        }),
        minSampleSize: 3,
      };
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["size:L"] }),
        undefined,
        ctx
      );
      expect(result.model).toBe("sonnet");
      expect(result.costPerSuccessRouting?.applied).toBe(true);
    });

    it("uses custom minSuccessRate when provided", () => {
      // With minSuccessRate=0.5, a 60% success rate should be accepted
      const ctx: CostPerSuccessContext = {
        ...makeHistory({
          "opus:feature-dev": {
            totalCostUsd: 10,
            successCount: 5,
            totalCount: 5,
          },
          "sonnet:feature-dev": {
            totalCostUsd: 3,
            successCount: 6,
            totalCount: 10, // 60% success rate
          },
        }),
        minSuccessRate: 0.5,
      };
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["size:L"] }),
        undefined,
        ctx
      );
      expect(result.model).toBe("sonnet");
      expect(result.costPerSuccessRouting?.applied).toBe(true);
    });
  });

  describe("model tier selection order", () => {
    it("prefers haiku over sonnet when opus selected and haiku has better CPS", () => {
      // opus → check sonnet (no history) → check haiku (good CPS)
      const ctx = makeHistory({
        "opus:feature-dev": {
          totalCostUsd: 20,
          successCount: 5,
          totalCount: 5,
        },
        "haiku:feature-dev": {
          totalCostUsd: 2,
          successCount: 5,
          totalCount: 5,
        },
      });
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["size:L"] }),
        undefined,
        ctx
      );
      expect(result.model).toBe("haiku");
      expect(result.costPerSuccessRouting?.toModel).toBe("haiku");
    });

    it("prefers sonnet over haiku when both qualify (most capable cheaper model)", () => {
      // Both sonnet and haiku pass threshold; sonnet is tried first (more capable)
      const ctx = makeHistory({
        "opus:feature-dev": {
          totalCostUsd: 20,
          successCount: 5,
          totalCount: 5,
        },
        "sonnet:feature-dev": {
          totalCostUsd: 5,
          successCount: 5,
          totalCount: 5,
        },
        "haiku:feature-dev": {
          totalCostUsd: 1,
          successCount: 5,
          totalCount: 5,
        },
      });
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["size:L"] }),
        undefined,
        ctx
      );
      // sonnet is tried first and passes → selected
      expect(result.model).toBe("sonnet");
      expect(result.costPerSuccessRouting?.toModel).toBe("sonnet");
    });

    it("tries haiku when sonnet fails threshold from sonnet baseline", () => {
      // Starting from sonnet (M complexity dev) → can only downgrade to haiku
      const ctx = makeHistory({
        "sonnet:feature-planning": {
          totalCostUsd: 10,
          successCount: 5,
          totalCount: 5,
        },
        "haiku:feature-planning": {
          totalCostUsd: 2,
          successCount: 5,
          totalCount: 5,
        },
      });
      const result = selector.selectModel(
        "feature-planning",
        makeMetadata({ labels: ["size:M"] }),
        undefined,
        ctx
      );
      // M planning → sonnet by matrix; haiku has better CPS
      expect(result.model).toBe("haiku");
      expect(result.costPerSuccessRouting?.fromModel).toBe("sonnet");
      expect(result.costPerSuccessRouting?.toModel).toBe("haiku");
    });

    it("does not attempt downgrade for haiku (floor)", () => {
      // haiku is already the cheapest; CPS context should be a no-op
      const ctx = makeHistory({
        "haiku:pr-create": { totalCostUsd: 1, successCount: 5, totalCount: 5 },
      });
      // pr-create is a lightweight stage and always returns haiku
      const result = selector.selectModel(
        "pr-create",
        makeMetadata({ labels: ["size:XL"] }),
        undefined,
        ctx
      );
      expect(result.model).toBe("haiku");
      // Lightweight stage → haiku path, no CPS routing attempted
      expect(result.costPerSuccessRouting).toBeUndefined();
    });
  });

  describe("reasoning string", () => {
    it("includes cost-per-success rationale in reasoning when applied", () => {
      const ctx = makeHistory({
        "opus:feature-dev": {
          totalCostUsd: 10,
          successCount: 5,
          totalCount: 5,
        },
        "sonnet:feature-dev": {
          totalCostUsd: 3,
          successCount: 5,
          totalCount: 5,
        },
      });
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["size:L"] }),
        undefined,
        ctx
      );
      expect(result.reasoning).toContain("Cost-per-success");
      expect(result.reasoning).toContain("sonnet");
      expect(result.reasoning).toContain("opus");
      expect(result.reasoning).toContain("/success");
    });

    it("does not mention cost-per-success in reasoning when not applied", () => {
      const result = selector.selectModel("feature-dev", makeMetadata({ labels: ["size:M"] }));
      expect(result.reasoning).not.toContain("Cost-per-success");
    });
  });

  describe("interaction with other routing mechanisms", () => {
    it("applies CPS routing after type overrides take effect", () => {
      // type:docs dev → opus by DEFAULT_TYPE_OVERRIDES
      // With CPS data showing sonnet is comparable, CPS routing should still apply
      const ctx = makeHistory({
        "opus:feature-dev": {
          totalCostUsd: 10,
          successCount: 5,
          totalCount: 5,
        },
        "sonnet:feature-dev": {
          totalCostUsd: 3,
          successCount: 5,
          totalCount: 5,
        },
      });
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["type:docs", "size:M"] }),
        undefined,
        ctx
      );
      // Type override sets opus for docs/dev, then CPS routing evaluates
      expect(result.costPerSuccessRouting?.applied).toBe(true);
      expect(result.model).toBe("sonnet");
    });

    it("CPS routing does not interfere with lightweight stages", () => {
      const ctx = makeHistory({
        "haiku:pr-create": { totalCostUsd: 1, successCount: 5, totalCount: 5 },
        "sonnet:pr-create": { totalCostUsd: 5, successCount: 5, totalCount: 5 },
      });
      const result = selector.selectModel(
        "pr-create",
        makeMetadata({ labels: ["size:XL"] }),
        undefined,
        ctx
      );
      // Lightweight → haiku returned immediately, CPS not evaluated
      expect(result.model).toBe("haiku");
      expect(result.costPerSuccessRouting).toBeUndefined();
    });

    it("CPS routing absent when context not provided", () => {
      const result = selector.selectModel("feature-dev", makeMetadata({ labels: ["size:L"] }));
      expect(result.costPerSuccessRouting).toBeUndefined();
    });
  });

  describe("integration: mixed-complexity scenarios", () => {
    const mixedHistory = makeHistory({
      // XS/S dev normally → sonnet; with CPS data haiku might be preferred
      "sonnet:feature-dev": { totalCostUsd: 8, successCount: 8, totalCount: 8 },
      "haiku:feature-dev": { totalCostUsd: 1, successCount: 8, totalCount: 8 },
      // L/XL dev normally → opus
      "opus:feature-dev": { totalCostUsd: 30, successCount: 6, totalCount: 6 },
      // Planning normally → sonnet; haiku candidate
      "sonnet:feature-planning": {
        totalCostUsd: 5,
        successCount: 10,
        totalCount: 10,
      },
      "haiku:feature-planning": {
        totalCostUsd: 1,
        successCount: 10,
        totalCount: 10,
      },
    });

    it("XS dev: routes sonnet→haiku when haiku CPS is within threshold", () => {
      // sonnet CPS = 8/8 = $1.00; haiku CPS = 1/8 = $0.125; ratio 0.125 ≤ 1.2
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["size:XS"] }),
        undefined,
        mixedHistory
      );
      expect(result.model).toBe("haiku");
      expect(result.costPerSuccessRouting?.applied).toBe(true);
    });

    it("L dev: routes opus→sonnet when sonnet CPS within threshold", () => {
      // opus CPS = 30/6 = $5.00; sonnet CPS = 8/8 = $1.00; ratio 0.2 ≤ 1.2
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["size:L"] }),
        undefined,
        mixedHistory
      );
      expect(result.model).toBe("sonnet");
      expect(result.costPerSuccessRouting?.fromModel).toBe("opus");
      expect(result.costPerSuccessRouting?.toModel).toBe("sonnet");
    });

    it("feature-planning: routes sonnet→haiku when haiku CPS is favorable", () => {
      // sonnet CPS = 5/10 = $0.50; haiku CPS = 1/10 = $0.10; ratio 0.2 ≤ 1.2
      const result = selector.selectModel(
        "feature-planning",
        makeMetadata({ labels: ["size:M"] }),
        undefined,
        mixedHistory
      );
      expect(result.model).toBe("haiku");
      expect(result.costPerSuccessRouting?.applied).toBe(true);
    });

    it("validates cost delta captured in routing result", () => {
      const result = selector.selectModel(
        "feature-dev",
        makeMetadata({ labels: ["size:L"] }),
        undefined,
        mixedHistory
      );
      const cps = result.costPerSuccessRouting;
      expect(cps?.applied).toBe(true);
      // from = opus CPS ≈ 5.0, to = sonnet CPS ≈ 1.0
      expect(cps?.fromCostPerSuccess).toBeCloseTo(5.0, 2);
      expect(cps?.toCostPerSuccess).toBeCloseTo(1.0, 2);
    });
  });
});
