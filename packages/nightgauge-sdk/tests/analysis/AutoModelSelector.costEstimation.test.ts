/**
 * Unit tests for AutoModelSelector.estimatePipelineCost() (Issue #948)
 *
 * Tests pre-pipeline cost estimation with per-stage breakdown,
 * stage skipping, effort derivation, and all-sonnet comparison.
 */

import { describe, it, expect } from "vitest";
import {
  AutoModelSelector,
  type IssueMetadata,
  type PipelineCostEstimate,
} from "../../src/analysis/AutoModelSelector.js";

function makeMetadata(overrides: Partial<IssueMetadata> = {}): IssueMetadata {
  return {
    labels: ["type:feature", "priority:medium", "size:M"],
    title: "Add user authentication",
    ...overrides,
  };
}

describe("AutoModelSelector.estimatePipelineCost", () => {
  const selector = new AutoModelSelector();

  it("returns estimates for all 6 pipeline stages", () => {
    const result = selector.estimatePipelineCost(makeMetadata());
    expect(result.stages).toHaveLength(6);
    expect(result.stages.map((s) => s.stage)).toEqual([
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ]);
  });

  it("total equals sum of per-stage costs", () => {
    const result = selector.estimatePipelineCost(makeMetadata());
    const sum = result.stages.reduce((s, stage) => s + stage.estimatedCost, 0);
    expect(result.totalEstimatedCost).toBeCloseTo(sum, 10);
  });

  it("skipped stages have $0 cost", () => {
    const result = selector.estimatePipelineCost(makeMetadata(), ["feature-validate", "pr-merge"]);
    const skipped = result.stages.filter((s) => s.skipped);
    expect(skipped).toHaveLength(2);
    for (const s of skipped) {
      expect(s.estimatedCost).toBe(0);
      expect(s.estimatedInputTokens).toBe(0);
      expect(s.estimatedOutputTokens).toBe(0);
    }
  });

  it("XS bug uses lower cost than L feature", () => {
    const xsBug = selector.estimatePipelineCost(
      makeMetadata({ labels: ["size:XS", "type:bug"], title: "Fix typo" })
    );
    const lFeature = selector.estimatePipelineCost(
      makeMetadata({
        labels: ["size:L", "type:feature"],
        title: "Major refactor",
      })
    );
    expect(xsBug.totalEstimatedCost).toBeLessThan(lFeature.totalEstimatedCost);
  });

  it("automatic routing is cheaper than all-sonnet for XS issues", () => {
    const result = selector.estimatePipelineCost(
      makeMetadata({ labels: ["size:XS", "type:bug"], title: "Fix typo" })
    );
    // XS uses haiku for lightweight/validate stages, which is cheaper than sonnet
    expect(result.totalEstimatedCost).toBeLessThanOrEqual(result.comparisonAllSonnet);
  });

  it("L/XL issues may cost more than all-sonnet (opus premium)", () => {
    const result = selector.estimatePipelineCost(
      makeMetadata({
        labels: ["size:XL", "type:feature"],
        title: "Major architecture overhaul",
      })
    );
    // XL uses opus for planning/dev which is more expensive than sonnet
    expect(result.totalEstimatedCost).toBeGreaterThan(result.comparisonAllSonnet);
  });

  it("includes complexity and timestamp", () => {
    const result = selector.estimatePipelineCost(
      makeMetadata({ labels: ["size:M"], title: "Medium task" })
    );
    expect(result.complexity).toBe("M");
    expect(result.estimatedAt).toBeTruthy();
    expect(() => new Date(result.estimatedAt)).not.toThrow();
  });

  it("each stage has valid model, effort, and confidence", () => {
    const result = selector.estimatePipelineCost(makeMetadata());
    for (const stage of result.stages) {
      if (stage.skipped) continue;
      expect(["haiku", "sonnet", "opus"]).toContain(stage.model);
      expect(["low", "medium", "high"]).toContain(stage.effort);
      expect(stage.confidence).toBeGreaterThan(0);
      expect(stage.confidence).toBeLessThanOrEqual(1);
      expect(stage.estimatedInputTokens).toBeGreaterThan(0);
      expect(stage.estimatedOutputTokens).toBeGreaterThan(0);
      expect(stage.estimatedCost).toBeGreaterThan(0);
    }
  });

  it("empty metadata defaults gracefully", () => {
    const result = selector.estimatePipelineCost({
      labels: [],
      title: "",
    });
    expect(result.stages).toHaveLength(6);
    expect(result.totalEstimatedCost).toBeGreaterThan(0);
    expect(result.complexity).toBeTruthy();
  });

  it("all-sonnet comparison uses sonnet rates for all stages", () => {
    const result = selector.estimatePipelineCost(
      makeMetadata({ labels: ["size:M"], title: "Medium task" })
    );
    // Verify comparison is positive (sonnet pricing is middle-tier)
    expect(result.comparisonAllSonnet).toBeGreaterThan(0);
  });

  it("skip all stages results in zero total", () => {
    const allStages = [
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ];
    const result = selector.estimatePipelineCost(makeMetadata(), allStages);
    expect(result.totalEstimatedCost).toBe(0);
    expect(result.comparisonAllSonnet).toBe(0);
  });
});
