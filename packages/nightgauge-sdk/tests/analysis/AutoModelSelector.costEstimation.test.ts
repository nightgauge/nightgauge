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
    const result = selector.estimatePipelineCost(makeMetadata(), { provider: "anthropic" });
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
    const result = selector.estimatePipelineCost(makeMetadata(), { provider: "anthropic" });
    const sum = result.stages.reduce((s, stage) => s + stage.estimatedCost, 0);
    expect(result.totalEstimatedCost).toBeCloseTo(sum, 10);
  });

  it("skipped stages have $0 cost", () => {
    const result = selector.estimatePipelineCost(makeMetadata(), {
      provider: "anthropic",
      skipStages: ["feature-validate", "pr-merge"],
    });
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
      makeMetadata({ labels: ["size:XS", "type:bug"], title: "Fix typo" }),
      { provider: "anthropic" }
    );
    const lFeature = selector.estimatePipelineCost(
      makeMetadata({
        labels: ["size:L", "type:feature"],
        title: "Major refactor",
      }),
      { provider: "anthropic" }
    );
    expect(xsBug.totalEstimatedCost).toBeLessThan(lFeature.totalEstimatedCost);
  });

  it("automatic routing is cheaper than all-sonnet for XS issues", () => {
    const result = selector.estimatePipelineCost(
      makeMetadata({ labels: ["size:XS", "type:bug"], title: "Fix typo" }),
      { provider: "anthropic" }
    );
    // XS uses haiku for lightweight/validate stages, which is cheaper than sonnet
    expect(result.totalEstimatedCost).toBeLessThanOrEqual(result.comparisonAllSonnet);
  });

  it("L/XL issues may cost more than all-sonnet (opus premium)", () => {
    const result = selector.estimatePipelineCost(
      makeMetadata({
        labels: ["size:XL", "type:feature"],
        title: "Major architecture overhaul",
      }),
      { provider: "anthropic" }
    );
    // XL uses opus for planning/dev which is more expensive than sonnet
    expect(result.totalEstimatedCost).toBeGreaterThan(result.comparisonAllSonnet);
  });

  it("includes complexity and timestamp", () => {
    const result = selector.estimatePipelineCost(
      makeMetadata({ labels: ["size:M"], title: "Medium task" }),
      { provider: "anthropic" }
    );
    expect(result.complexity).toBe("M");
    expect(result.estimatedAt).toBeTruthy();
    expect(() => new Date(result.estimatedAt)).not.toThrow();
  });

  it("each stage has valid model, effort, and confidence", () => {
    const result = selector.estimatePipelineCost(makeMetadata(), { provider: "anthropic" });
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
    const result = selector.estimatePipelineCost(
      { labels: [], title: "" },
      { provider: "anthropic" }
    );
    expect(result.stages).toHaveLength(6);
    expect(result.totalEstimatedCost).toBeGreaterThan(0);
    expect(result.complexity).toBeTruthy();
  });

  it("all-sonnet comparison uses sonnet rates for all stages", () => {
    const result = selector.estimatePipelineCost(
      makeMetadata({ labels: ["size:M"], title: "Medium task" }),
      { provider: "anthropic" }
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
    const result = selector.estimatePipelineCost(makeMetadata(), {
      provider: "anthropic",
      skipStages: allStages,
    });
    expect(result.totalEstimatedCost).toBe(0);
    expect(result.comparisonAllSonnet).toBe(0);
  });
  // --- Provider-aware pricing (#1213; the TS half of #696) ---
  //
  // Every band used to be priced from `getModelDescriptor(tier, "anthropic")`,
  // so a run dispatched to xai or openai was ESTIMATED at Claude rates and
  // BOOKED at the real provider's. Forecast-vs-actual variance was then not
  // merely noisy but biased by the ratio between two rate cards, with both
  // numbers individually plausible. Go fixed its own half in #696; this side
  // kept the default for two more years of runs.

  it("prices the same work differently per provider, from each provider's rates", () => {
    const md = makeMetadata({ labels: ["size:M", "type:feature"], title: "Add auth" });
    const anthropic = selector.estimatePipelineCost(md, { provider: "anthropic" });
    const xai = selector.estimatePipelineCost(md, { provider: "xai" });

    expect(anthropic.provider).toBe("anthropic");
    expect(xai.provider).toBe("xai");
    // grok-4.6 is ~0.34/1.02 against anthropic sonnet's 3/15, so the same
    // token volumes cannot cost the same. Goes red the moment the estimator
    // hardcodes "anthropic" again.
    expect(xai.totalEstimatedCost).not.toBe(anthropic.totalEstimatedCost);
    expect(xai.totalEstimatedCost).toBeLessThan(anthropic.totalEstimatedCost);
    expect(xai.unpriced).toBe(false);
  });

  it("reports an unpriceable provider as unpriced rather than pricing it at Anthropic rates", () => {
    // ollama has no registry entries by design.
    const result = selector.estimatePipelineCost(makeMetadata(), { provider: "ollama" });
    expect(result.unpriced).toBe(true);
    for (const s of result.stages) {
      if (s.skipped) continue;
      expect(s.unpriced).toBe(true);
      // The ABSENCE of a price, not a price of zero.
      expect(s.estimatedCost).toBe(0);
    }
    expect(result.totalEstimatedCost).toBe(0);
    // And emphatically not the anthropic figure the old code produced.
    const anthropic = selector.estimatePipelineCost(makeMetadata(), { provider: "anthropic" });
    expect(result.totalEstimatedCost).not.toBe(anthropic.totalEstimatedCost);
  });

  it("prices copilot's zero-rate card as a real 0, distinct from unpriced", () => {
    // copilot IS in the registry, at $0. That is an answer; ollama's absence
    // is a gap. Collapsing the two would make a free provider and an unknown
    // one indistinguishable.
    const result = selector.estimatePipelineCost(makeMetadata(), { provider: "copilot" });
    expect(result.unpriced).toBe(false);
    expect(result.totalEstimatedCost).toBe(0);
  });

  it("compares against sonnet WITHIN the same provider", () => {
    // Comparing a grok run against Anthropic sonnet answers a question nobody
    // asked, and would make every non-Anthropic run look like a saving.
    const md = makeMetadata({ labels: ["size:M"], title: "Medium task" });
    const anthropic = selector.estimatePipelineCost(md, { provider: "anthropic" });
    const xai = selector.estimatePipelineCost(md, { provider: "xai" });
    expect(xai.comparisonAllSonnet).toBeGreaterThan(0);
    expect(xai.comparisonAllSonnet).toBeLessThan(anthropic.comparisonAllSonnet);
  });

  // --- Effort comes from the run, not from the size label (#1213) ---

  it("prices high and low effort differently for an IDENTICAL issue", () => {
    const md = makeMetadata({ labels: ["size:M", "type:feature"], title: "Add auth" });
    const high = selector.estimatePipelineCost(md, {
      provider: "anthropic",
      stageEfforts: { "feature-dev": "high" },
    });
    const low = selector.estimatePipelineCost(md, {
      provider: "anthropic",
      stageEfforts: { "feature-dev": "low" },
    });

    const devOf = (r: PipelineCostEstimate) => r.stages.find((s) => s.stage === "feature-dev")!;
    expect(devOf(high).effort).toBe("high");
    expect(devOf(low).effort).toBe("low");
    // Same size label, same everything else — only the dispatched effort
    // differs. Goes red if effort is re-derived from size alone.
    expect(devOf(high).estimatedCost).toBeGreaterThan(devOf(low).estimatedCost);
    expect(devOf(high).estimatedInputTokens).toBeGreaterThan(devOf(low).estimatedInputTokens);
  });

  it("falls back to deriveEffort for stages the caller did not pin", () => {
    const md = makeMetadata({ labels: ["size:M"], title: "Medium task" });
    const result = selector.estimatePipelineCost(md, {
      provider: "anthropic",
      stageEfforts: { "feature-dev": "high" },
    });
    // feature-dev pinned, feature-planning derived from size:M.
    expect(result.stages.find((s) => s.stage === "feature-dev")!.effort).toBe("high");
    expect(result.stages.find((s) => s.stage === "feature-planning")!.effort).toBe("medium");
  });

  it("stamps the provider on every stage, skipped ones included", () => {
    const result = selector.estimatePipelineCost(makeMetadata(), {
      provider: "xai",
      skipStages: ["pr-merge"],
    });
    for (const s of result.stages) {
      expect(s.provider).toBe("xai");
    }
  });
});
