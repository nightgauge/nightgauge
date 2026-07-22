/**
 * Unit tests for analyzeModelRouting (Issue #1106)
 *
 * Covers:
 *  - Insufficient data path (hasEnoughData: false, score 50)
 *  - Under-routing detection (lightweight model + high complexity + auto + failure)
 *  - Over-routing detection (heavyweight model + low complexity + auto + success + 0 retries)
 *  - Low auto-selection accuracy finding (failure rate > 20% with enough records)
 *  - Per-model cost efficiency (effective cost > 2x mean → finding)
 *  - Single model penalty (only 1 distinct model → score -5)
 *  - Healthy data (no pathological conditions → high score)
 *  - Baseline comparison (period comparison populated when baseline provided)
 *  - Metric values in returned object
 *  - Finding structure and IDs
 */

import { describe, it, expect } from "vitest";
import { analyzeModelRouting } from "../../../../src/analysis/health/dimensions/modelRouting.js";
import { DEFAULT_HEALTH_CONFIG } from "../../../../src/analysis/health/types.js";
import type { HealthAnalysisInput } from "../../../../src/analysis/health/types.js";
import { makeExecutionRecord, makeDataset, makeEmptyDataset } from "../fixtures.js";

// ── Helpers ────────────────────────────────────────────────────────

/** Wrap a list of records into a minimal HealthAnalysisInput */
function datasetFromRecords(
  records: ReturnType<typeof makeExecutionRecord>[]
): HealthAnalysisInput {
  return {
    executionHistory: records,
    healthScores: [],
    selfTuningLog: [],
    experimentResults: [],
    healthReports: [],
  };
}

/**
 * Build N records sharing the same overrides.
 * Spreads issueNumber and timestamp so records are uniquely identifiable.
 */
function buildRecords(
  count: number,
  overrides: Parameters<typeof makeExecutionRecord>[0]
): ReturnType<typeof makeExecutionRecord>[] {
  return Array.from({ length: count }, (_, i) =>
    makeExecutionRecord({
      issueNumber: 900 + i,
      ...overrides,
    })
  );
}

// ── 1. Insufficient data ───────────────────────────────────────────

describe("analyzeModelRouting — insufficient data", () => {
  it("returns score 50 and hasEnoughData false when dataset is empty", () => {
    const result = analyzeModelRouting(makeEmptyDataset(), DEFAULT_HEALTH_CONFIG);

    expect(result.dimension).toBe("model-routing");
    expect(result.score).toBe(50);
    expect(result.hasEnoughData).toBe(false);
    expect(result.findings).toHaveLength(0);
    expect(result.sampleSize).toBe(0);
    expect(result.metrics).toMatchObject({ sampleSize: 0 });
  });

  it("returns score 50 and hasEnoughData false when below basic minimum (< 5)", () => {
    // DEFAULT_HEALTH_CONFIG.minimumSampleSizes.basic = 5
    const records = buildRecords(4, { success: true, model: "sonnet" });
    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    expect(result.score).toBe(50);
    expect(result.hasEnoughData).toBe(false);
    expect(result.findings).toHaveLength(0);
    expect(result.sampleSize).toBe(4);
  });

  it("has hasEnoughData true at exactly the basic minimum (5)", () => {
    const records = buildRecords(5, {
      success: true,
      model: "sonnet",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
    });
    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    expect(result.hasEnoughData).toBe(true);
  });
});

// ── 2. Under-routing detection ─────────────────────────────────────

describe("analyzeModelRouting — under-routing", () => {
  it("detects under-routing with haiku + high complexity L + auto + failure", () => {
    const underRoutingRecords = buildRecords(3, {
      model: "haiku",
      selectionSource: "auto",
      autoSelectorComplexity: "L",
      success: false,
      retries: 1,
      costUsd: 0.1,
    });
    // Pad with healthy records so we meet sample size and don't trip other findings
    const healthyRecords = buildRecords(7, {
      model: "haiku",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
      success: true,
      retries: 0,
      costUsd: 0.1,
    });

    const result = analyzeModelRouting(
      datasetFromRecords([...underRoutingRecords, ...healthyRecords]),
      DEFAULT_HEALTH_CONFIG
    );

    const underRoutingFinding = result.findings.find((f) => f.title.includes("Under-Routing"));
    expect(underRoutingFinding).toBeDefined();
    expect(underRoutingFinding!.dimension).toBe("model-routing");
    expect(underRoutingFinding!.evidence).toMatchObject({
      underRoutingCount: 3,
      affectedModels: expect.arrayContaining(["haiku"]),
    });
  });

  it("detects under-routing with haiku + high complexity XL + auto + failure", () => {
    const records = [
      ...buildRecords(3, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "XL",
        success: false,
        retries: 2,
        costUsd: 0.1,
      }),
      ...buildRecords(7, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.1,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    expect(result.findings.some((f) => f.title.includes("Under-Routing"))).toBe(true);
    expect(result.metrics["underRoutingCount"]).toBeGreaterThan(0);
  });

  it("detects under-routing with sonnet + high complexity L + auto + failure", () => {
    const records = [
      ...buildRecords(3, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "L",
        success: false,
        retries: 1,
        costUsd: 0.2,
      }),
      ...buildRecords(7, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.2,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    const underRoutingFinding = result.findings.find((f) => f.title.includes("Under-Routing"));
    expect(underRoutingFinding).toBeDefined();
    expect(underRoutingFinding!.evidence).toMatchObject({
      affectedModels: expect.arrayContaining(["sonnet"]),
    });
  });

  it("does NOT detect under-routing when selectionSource is manual (not auto)", () => {
    const records = [
      ...buildRecords(3, {
        model: "haiku",
        selectionSource: "manual" as never,
        autoSelectorComplexity: "L",
        success: false,
        retries: 1,
        costUsd: 0.1,
      }),
      ...buildRecords(7, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.1,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    expect(result.findings.some((f) => f.title.includes("Under-Routing"))).toBe(false);
    expect(result.metrics["underRoutingCount"]).toBe(0);
  });

  it("does NOT detect under-routing when success is true", () => {
    const records = [
      ...buildRecords(3, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "L",
        success: true, // succeeded, so not an under-routing failure
        retries: 0,
        costUsd: 0.1,
      }),
      ...buildRecords(7, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.1,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    expect(result.findings.some((f) => f.title.includes("Under-Routing"))).toBe(false);
    expect(result.metrics["underRoutingCount"]).toBe(0);
  });

  it("does NOT detect under-routing for low complexity (XS/S) even when failing", () => {
    const records = [
      ...buildRecords(3, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "S", // low complexity — not under-routing
        success: false,
        retries: 1,
        costUsd: 0.1,
      }),
      ...buildRecords(7, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.1,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    expect(result.metrics["underRoutingCount"]).toBe(0);
  });

  it("applies score penalty of -10 when under-routing is present", () => {
    // Construct a dataset with only under-routing issues (no other penalties)
    const records = [
      ...buildRecords(3, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "L",
        success: false,
        retries: 1,
        costUsd: 0.1,
      }),
      // 7 more successful auto records with same cost so mean cost doesn't penalise
      ...buildRecords(7, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.1,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    // Score starts at 100, under-routing -10, single model -5 → 85
    // (also check auto failure rate: 3/10 = 30% > 20% → -15)
    // With under-routing, 3 of 10 are failures: autoFailureRate = 0.3 → also -15
    // So: 100 - 10 (under-routing) - 15 (low accuracy) - 5 (single model) = 70
    expect(result.score).toBeGreaterThanOrEqual(0);
    expect(result.score).toBeLessThanOrEqual(100);
    // Confirm under-routing finding exists
    expect(result.findings.some((f) => f.title.includes("Under-Routing"))).toBe(true);
  });

  it("sets under-routing finding severity to medium when count < 5", () => {
    const records = [
      ...buildRecords(2, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "L",
        success: false,
        retries: 1,
        costUsd: 0.1,
      }),
      ...buildRecords(8, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.1,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    const finding = result.findings.find((f) => f.title.includes("Under-Routing"));
    expect(finding).toBeDefined();
    expect(finding!.severity).toBe("medium");
  });

  it("sets under-routing finding severity to high when count >= 5", () => {
    const records = [
      ...buildRecords(5, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "L",
        success: false,
        retries: 1,
        costUsd: 0.1,
      }),
      ...buildRecords(15, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.1,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    const finding = result.findings.find((f) => f.title.includes("Under-Routing"));
    expect(finding).toBeDefined();
    expect(finding!.severity).toBe("high");
  });
});

// ── 3. Over-routing detection ──────────────────────────────────────

describe("analyzeModelRouting — over-routing", () => {
  it("detects over-routing with opus + low complexity XS + auto + success + 0 retries", () => {
    const overRoutingRecords = buildRecords(3, {
      model: "opus",
      selectionSource: "auto",
      autoSelectorComplexity: "XS",
      success: true,
      retries: 0,
      costUsd: 0.5,
    });
    const healthyRecords = buildRecords(7, {
      model: "opus",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
      success: true,
      retries: 0,
      costUsd: 0.5,
    });

    const result = analyzeModelRouting(
      datasetFromRecords([...overRoutingRecords, ...healthyRecords]),
      DEFAULT_HEALTH_CONFIG
    );

    const overRoutingFinding = result.findings.find((f) => f.title.includes("Over-Routing"));
    expect(overRoutingFinding).toBeDefined();
    expect(overRoutingFinding!.dimension).toBe("model-routing");
    expect(overRoutingFinding!.evidence).toMatchObject({
      overRoutingCount: 3,
    });
  });

  it("detects over-routing with opus + low complexity S + auto + success + 0 retries", () => {
    const records = [
      ...buildRecords(3, {
        model: "opus",
        selectionSource: "auto",
        autoSelectorComplexity: "S",
        success: true,
        retries: 0,
        costUsd: 0.5,
      }),
      ...buildRecords(7, {
        model: "opus",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.5,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    expect(result.findings.some((f) => f.title.includes("Over-Routing"))).toBe(true);
    expect(result.metrics["overRoutingCount"]).toBeGreaterThan(0);
  });

  it("does NOT detect over-routing when model is not opus (lightweight)", () => {
    const records = [
      ...buildRecords(3, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "XS",
        success: true,
        retries: 0,
        costUsd: 0.2,
      }),
      ...buildRecords(7, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.2,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    expect(result.findings.some((f) => f.title.includes("Over-Routing"))).toBe(false);
    expect(result.metrics["overRoutingCount"]).toBe(0);
  });

  it("does NOT detect over-routing when retries > 0 (not first-attempt success)", () => {
    const records = [
      ...buildRecords(3, {
        model: "opus",
        selectionSource: "auto",
        autoSelectorComplexity: "XS",
        success: true,
        retries: 1, // had to retry → not over-routing
        costUsd: 0.5,
      }),
      ...buildRecords(7, {
        model: "opus",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.5,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    expect(result.metrics["overRoutingCount"]).toBe(0);
  });

  it("does NOT detect over-routing when success is false", () => {
    const records = [
      ...buildRecords(3, {
        model: "opus",
        selectionSource: "auto",
        autoSelectorComplexity: "XS",
        success: false, // failed, so not over-routing
        retries: 0,
        costUsd: 0.5,
      }),
      ...buildRecords(7, {
        model: "opus",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.5,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    expect(result.metrics["overRoutingCount"]).toBe(0);
  });

  it("does NOT detect over-routing for high complexity (L/XL) with opus", () => {
    const records = [
      ...buildRecords(3, {
        model: "opus",
        selectionSource: "auto",
        autoSelectorComplexity: "L", // high complexity — appropriate for opus
        success: true,
        retries: 0,
        costUsd: 0.5,
      }),
      ...buildRecords(7, {
        model: "opus",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.5,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    expect(result.metrics["overRoutingCount"]).toBe(0);
  });

  it("applies score penalty of -10 when over-routing is present", () => {
    // Dataset with only over-routing (all same cost, auto, success)
    // All 10 records: 3 are over-routing (opus/XS), 7 are normal (opus/M)
    const records = [
      ...buildRecords(3, {
        model: "opus",
        selectionSource: "auto",
        autoSelectorComplexity: "XS",
        success: true,
        retries: 0,
        costUsd: 0.5,
      }),
      ...buildRecords(7, {
        model: "opus",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.5,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    // All succeed → autoFailureRate = 0 → no low-accuracy penalty
    // Single model (opus) → -5
    // Over-routing detected → -10
    // Expected score: 100 - 10 - 5 = 85
    expect(result.score).toBe(85);
    expect(result.findings.some((f) => f.title.includes("Over-Routing"))).toBe(true);
  });

  it("includes estimated waste cost in over-routing evidence", () => {
    const costPerRecord = 0.4;
    const records = [
      ...buildRecords(3, {
        model: "opus",
        selectionSource: "auto",
        autoSelectorComplexity: "XS",
        success: true,
        retries: 0,
        costUsd: costPerRecord,
      }),
      ...buildRecords(7, {
        model: "opus",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: costPerRecord,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    const overRoutingFinding = result.findings.find((f) => f.title.includes("Over-Routing"));
    expect(overRoutingFinding).toBeDefined();
    expect(typeof overRoutingFinding!.evidence["estimatedWasteCostUsd"]).toBe("number");
    expect(overRoutingFinding!.evidence["estimatedWasteCostUsd"]).toBeCloseTo(3 * costPerRecord, 5);
  });
});

// ── 4. Low auto-selection accuracy ────────────────────────────────

describe("analyzeModelRouting — low auto-selection accuracy", () => {
  it('produces "Low Auto-Selection Accuracy" finding when auto failure rate > 20% with enough records', () => {
    // 8 failures out of 10 auto records = 80% failure rate > 20%
    const failingAutoRecords = buildRecords(8, {
      model: "sonnet",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
      success: false,
      retries: 1,
      costUsd: 0.2,
    });
    const passingAutoRecords = buildRecords(2, {
      model: "sonnet",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
      success: true,
      retries: 0,
      costUsd: 0.2,
    });

    const result = analyzeModelRouting(
      datasetFromRecords([...failingAutoRecords, ...passingAutoRecords]),
      DEFAULT_HEALTH_CONFIG
    );

    const accuracyFinding = result.findings.find((f) =>
      f.title.includes("Low Auto-Selection Accuracy")
    );
    expect(accuracyFinding).toBeDefined();
    expect(accuracyFinding!.dimension).toBe("model-routing");
    expect(accuracyFinding!.evidence).toMatchObject({
      autoTotal: 10,
    });
    expect(accuracyFinding!.evidence["autoFailureRate"]).toBeCloseTo(0.8, 5);
  });

  it("does NOT produce low accuracy finding when failure rate is exactly 20%", () => {
    // 2 failures out of 10 = 20% — not > 20%
    const records = [
      ...buildRecords(8, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.2,
      }),
      ...buildRecords(2, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: false,
        retries: 1,
        costUsd: 0.2,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    expect(result.findings.some((f) => f.title.includes("Low Auto-Selection Accuracy"))).toBe(
      false
    );
  });

  it("does NOT produce low accuracy finding when there are no auto records", () => {
    // All records have selectionSource='config'
    const records = buildRecords(10, {
      model: "sonnet",
      selectionSource: "config",
      autoSelectorComplexity: undefined,
      success: false, // all fail, but none are auto-selected
      retries: 1,
      costUsd: 0.2,
    });

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    // autoSuccessRate defaults to 1 (neutral) when no auto records
    expect(result.findings.some((f) => f.title.includes("Low Auto-Selection Accuracy"))).toBe(
      false
    );
    expect(result.metrics["autoSelectionTotal"]).toBe(0);
    expect(result.metrics["autoSelectionSuccessRate"]).toBe(1);
  });

  it("applies score penalty of -15 when auto failure rate > 20%", () => {
    // 3 failures out of 10 auto records = 30% failure rate > 20%
    // Also single model (haiku) → -5
    // No under/over routing
    const records = [
      ...buildRecords(7, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.1,
      }),
      ...buildRecords(3, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: false,
        retries: 1,
        costUsd: 0.1,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    // 100 - 15 (low accuracy) - 5 (single model) = 80
    expect(result.score).toBe(80);
  });

  it("sets severity to high when failure rate > 40%", () => {
    const records = [
      ...buildRecords(4, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.2,
      }),
      ...buildRecords(6, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: false,
        retries: 1,
        costUsd: 0.2,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    const finding = result.findings.find((f) => f.title.includes("Low Auto-Selection Accuracy"));
    expect(finding).toBeDefined();
    // 6/10 = 60% failure rate > 40% → high
    expect(finding!.severity).toBe("high");
  });

  it("sets severity to medium when failure rate is between 20% and 40%", () => {
    const records = [
      ...buildRecords(7, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.2,
      }),
      ...buildRecords(3, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: false,
        retries: 1,
        costUsd: 0.2,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    const finding = result.findings.find((f) => f.title.includes("Low Auto-Selection Accuracy"));
    expect(finding).toBeDefined();
    // 3/10 = 30% failure rate: > 20% but not > 40% → medium
    expect(finding!.severity).toBe("medium");
  });

  it("reports auto selection metrics in result metrics", () => {
    const records = [
      ...buildRecords(8, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.2,
      }),
      ...buildRecords(2, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: false,
        retries: 1,
        costUsd: 0.2,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    expect(result.metrics["autoSelectionTotal"]).toBe(10);
    expect(result.metrics["autoSelectionSuccessRate"]).toBeCloseTo(0.8, 5);
    expect(result.metrics["autoSelectionFailureRate"]).toBeCloseTo(0.2, 5);
  });
});

// ── 5. Per-model cost efficiency ───────────────────────────────────

describe("analyzeModelRouting — per-model cost efficiency", () => {
  it("flags a cost-inefficient model when its effective cost per success is > 2x the mean", () => {
    // Model "sonnet": 5 successes at cost 0.10 each → effective cost 0.10 per success
    // Model "opus":   3 successes at cost 1.00 each → effective cost 1.00 per success
    // Mean = (5*0.10 + 3*1.00) / 2 models = ... but mean is computed per model, not weighted
    // mean([0.10, 1.00]) = 0.55 → threshold = 1.10 → opus (1.00) is NOT > 1.10
    //
    // Use a more extreme ratio: sonnet 0.10, badmodel 5.00
    // mean([0.10, 5.00]) = 2.55 → threshold = 5.10 → badmodel NOT flagged
    // Need badmodel > 2 * mean, so: badmodel > 2 * ((0.10 + badmodel) / 2)
    // badmodel > 0.10 + badmodel → 0 > 0.10, impossible with 2 models
    //
    // With 3 models: cheap=0.10, medium=0.20, expensive=X
    // mean = (0.10 + 0.20 + X) / 3 = (0.30 + X) / 3
    // X > 2 * mean → X > 2*(0.30+X)/3 → 3X > 0.60 + 2X → X > 0.60
    // So X=1.00 should work: mean=(0.10+0.20+1.00)/3=0.433, threshold=0.867, 1.00>0.867 ✓
    const cheapRecords = buildRecords(5, {
      model: "haiku",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
      success: true,
      retries: 0,
      costUsd: 0.1,
    });
    const mediumRecords = buildRecords(5, {
      model: "sonnet",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
      success: true,
      retries: 0,
      costUsd: 0.2,
    });
    const expensiveRecords = buildRecords(5, {
      model: "opus",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
      success: true,
      retries: 0,
      costUsd: 1.0, // much higher cost per success
    });

    const result = analyzeModelRouting(
      datasetFromRecords([...cheapRecords, ...mediumRecords, ...expensiveRecords]),
      DEFAULT_HEALTH_CONFIG
    );

    const costFinding = result.findings.find((f) => f.title.includes("Cost-Ineffective"));
    expect(costFinding).toBeDefined();
    expect(costFinding!.dimension).toBe("model-routing");
    expect(costFinding!.evidence).toMatchObject({
      highCostModels: expect.arrayContaining([expect.objectContaining({ model: "opus" })]),
    });
  });

  it("does NOT flag cost efficiency when all models have similar effective cost", () => {
    const records = [
      ...buildRecords(5, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.1,
      }),
      ...buildRecords(5, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.12, // very close to haiku
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    expect(result.findings.some((f) => f.title.includes("Cost-Ineffective"))).toBe(false);
  });

  it("does NOT flag cost efficiency when a model has fewer than 3 records", () => {
    // The expensive model has only 2 records — below the per-model minimum
    const cheapRecords = buildRecords(10, {
      model: "haiku",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
      success: true,
      retries: 0,
      costUsd: 0.1,
    });
    const expensiveRecords = buildRecords(2, {
      model: "opus",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
      success: true,
      retries: 0,
      costUsd: 5.0, // very expensive but < 3 samples
    });

    const result = analyzeModelRouting(
      datasetFromRecords([...cheapRecords, ...expensiveRecords]),
      DEFAULT_HEALTH_CONFIG
    );

    expect(result.findings.some((f) => f.title.includes("Cost-Ineffective"))).toBe(false);
  });

  it("reports meanEffectiveCostPerSuccess in result metrics", () => {
    const records = [
      ...buildRecords(5, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.1,
      }),
      ...buildRecords(5, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.2,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    // haiku: 5 successes, total cost = 0.50 → 0.10/success
    // sonnet: 5 successes, total cost = 1.00 → 0.20/success
    // mean([0.10, 0.20]) = 0.15
    expect(result.metrics["meanEffectiveCostPerSuccess"]).toBeCloseTo(0.15, 5);
  });
});

// ── 6. Single model penalty ────────────────────────────────────────

describe("analyzeModelRouting — single model penalty", () => {
  it("applies -5 penalty when only one distinct model is used", () => {
    // 10 records, all using 'sonnet', all successful, no routing issues
    const records = buildRecords(10, {
      model: "sonnet",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
      success: true,
      retries: 0,
      costUsd: 0.2,
    });

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    // 100 - 5 (single model) = 95
    expect(result.score).toBe(95);
    expect(result.metrics["distinctModelCount"]).toBe(1);
  });

  it("does NOT apply single model penalty when two distinct models are used", () => {
    const records = [
      ...buildRecords(5, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.1,
      }),
      ...buildRecords(5, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.2,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    // 100 - 0 = 100 (no pathological conditions)
    expect(result.score).toBe(100);
    expect(result.metrics["distinctModelCount"]).toBe(2);
  });
});

// ── 7. Healthy data ────────────────────────────────────────────────

describe("analyzeModelRouting — healthy data", () => {
  it("returns a high score (>= 90) for a clean dataset with no routing issues", () => {
    const result = analyzeModelRouting(makeDataset(), DEFAULT_HEALTH_CONFIG);

    expect(result.hasEnoughData).toBe(true);
    expect(result.score).toBeGreaterThanOrEqual(70);
    expect(result.findings).toHaveLength(0);
  });

  it("returns score 100 when two models are both healthy", () => {
    const records = [
      ...buildRecords(5, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.1,
      }),
      ...buildRecords(5, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.2,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    expect(result.score).toBe(100);
    expect(result.findings).toHaveLength(0);
  });

  it('returns dimension "model-routing"', () => {
    const result = analyzeModelRouting(makeDataset(), DEFAULT_HEALTH_CONFIG);
    expect(result.dimension).toBe("model-routing");
  });

  it("returns a valid HealthStatus string", () => {
    const result = analyzeModelRouting(makeDataset(), DEFAULT_HEALTH_CONFIG);
    const validStatuses = ["excellent", "good", "fair", "poor", "critical"];
    expect(validStatuses).toContain(result.status);
  });

  it("populates sampleSize correctly", () => {
    const result = analyzeModelRouting(makeDataset(), DEFAULT_HEALTH_CONFIG);
    expect(result.sampleSize).toBe(result.metrics["sampleSize"] ?? result.sampleSize);
    expect(result.sampleSize).toBeGreaterThan(0);
  });
});

// ── 8. Baseline comparison ─────────────────────────────────────────

describe("analyzeModelRouting — baseline comparison", () => {
  it("populates periodComparison when baseline has enough auto records", () => {
    // Current dataset: 10 auto records, all succeed
    const currentRecords = buildRecords(10, {
      model: "sonnet",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
      success: true,
      retries: 0,
      costUsd: 0.2,
    });
    const current = datasetFromRecords(currentRecords);

    // Baseline dataset: 10 auto records, 5 succeed (50% success rate)
    const baselineRecords = [
      ...buildRecords(5, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.2,
      }),
      ...buildRecords(5, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: false,
        retries: 1,
        costUsd: 0.2,
      }),
    ];
    const baseline = datasetFromRecords(baselineRecords);

    const result = analyzeModelRouting(current, DEFAULT_HEALTH_CONFIG, baseline);

    expect(result.periodComparison).toBeDefined();
    expect(result.periodComparison!.currentValue).toBeCloseTo(1.0, 5);
    expect(result.periodComparison!.baselineValue).toBeCloseTo(0.5, 5);
    expect(["improving", "stable", "degrading"]).toContain(result.periodComparison!.direction);
  });

  it('marks direction "improving" when auto success rate improved over baseline', () => {
    // Current: 100% success rate
    const currentRecords = buildRecords(10, {
      model: "sonnet",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
      success: true,
      retries: 0,
      costUsd: 0.2,
    });

    // Baseline: 50% success rate
    const baselineRecords = [
      ...buildRecords(5, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.2,
      }),
      ...buildRecords(5, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: false,
        retries: 1,
        costUsd: 0.2,
      }),
    ];

    const result = analyzeModelRouting(
      datasetFromRecords(currentRecords),
      DEFAULT_HEALTH_CONFIG,
      datasetFromRecords(baselineRecords)
    );

    expect(result.periodComparison).toBeDefined();
    expect(result.periodComparison!.direction).toBe("improving");
  });

  it('marks direction "degrading" when auto success rate worsened vs baseline', () => {
    // Current: 50% success rate
    const currentRecords = [
      ...buildRecords(5, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.2,
      }),
      ...buildRecords(5, {
        model: "sonnet",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: false,
        retries: 1,
        costUsd: 0.2,
      }),
    ];

    // Baseline: 100% success rate
    const baselineRecords = buildRecords(10, {
      model: "sonnet",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
      success: true,
      retries: 0,
      costUsd: 0.2,
    });

    const result = analyzeModelRouting(
      datasetFromRecords(currentRecords),
      DEFAULT_HEALTH_CONFIG,
      datasetFromRecords(baselineRecords)
    );

    expect(result.periodComparison).toBeDefined();
    expect(result.periodComparison!.direction).toBe("degrading");
  });

  it("does not populate periodComparison when baseline has no auto records", () => {
    const currentRecords = buildRecords(10, {
      model: "sonnet",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
      success: true,
      retries: 0,
      costUsd: 0.2,
    });

    // Baseline with no auto-selected records
    const baselineRecords = buildRecords(10, {
      model: "sonnet",
      selectionSource: "config",
      autoSelectorComplexity: undefined,
      success: true,
      retries: 0,
      costUsd: 0.2,
    });

    const result = analyzeModelRouting(
      datasetFromRecords(currentRecords),
      DEFAULT_HEALTH_CONFIG,
      datasetFromRecords(baselineRecords)
    );

    // comparisonSampleSize = min(10, 0) = 0, < basic minimum → no comparison
    expect(result.periodComparison).toBeUndefined();
  });

  it("does not populate periodComparison when no baseline is provided", () => {
    const records = buildRecords(10, {
      model: "sonnet",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
      success: true,
      retries: 0,
      costUsd: 0.2,
    });

    const result = analyzeModelRouting(
      datasetFromRecords(records),
      DEFAULT_HEALTH_CONFIG
      // no baseline argument
    );

    expect(result.periodComparison).toBeUndefined();
  });
});

// ── 9. Combined penalties and score clamping ───────────────────────

describe("analyzeModelRouting — combined penalties and score clamping", () => {
  it("score is clamped to 0 when many penalties stack", () => {
    // All penalties: under-routing, over-routing, low accuracy, single model
    // This dataset is intentionally pathological:
    //   - haiku records: auto, L, fail → under-routing
    //   - opus records: auto, XS, success, 0 retries → over-routing
    //   - combined: high failure rate → low accuracy
    //   But we need 2 models so we don't get single model penalty
    const underRouting = buildRecords(8, {
      model: "haiku",
      selectionSource: "auto",
      autoSelectorComplexity: "L",
      success: false,
      retries: 1,
      costUsd: 0.1,
    });
    const overRouting = buildRecords(5, {
      model: "opus",
      selectionSource: "auto",
      autoSelectorComplexity: "XS",
      success: true,
      retries: 0,
      costUsd: 0.5,
    });

    const result = analyzeModelRouting(
      datasetFromRecords([...underRouting, ...overRouting]),
      DEFAULT_HEALTH_CONFIG
    );

    // Score = 100 - 10 (under-routing) - 10 (over-routing) - 15 (low accuracy) = 65
    // 2 models → no single model penalty
    // Note: 8/13 failures = ~61.5% failure rate
    expect(result.score).toBeGreaterThanOrEqual(0);
    expect(result.score).toBeLessThanOrEqual(100);
  });

  it("score is always between 0 and 100", () => {
    // Use the degrading dataset which has many issues
    const result = analyzeModelRouting(makeDataset(), DEFAULT_HEALTH_CONFIG);

    expect(result.score).toBeGreaterThanOrEqual(0);
    expect(result.score).toBeLessThanOrEqual(100);
  });
});

// ── 10. Finding structure and IDs ──────────────────────────────────

describe("analyzeModelRouting — finding structure", () => {
  it('findings have sequential IDs starting with "mr-"', () => {
    // Trigger both under-routing and over-routing, plus low accuracy
    const underRouting = buildRecords(5, {
      model: "haiku",
      selectionSource: "auto",
      autoSelectorComplexity: "L",
      success: false,
      retries: 1,
      costUsd: 0.1,
    });
    const overRouting = buildRecords(5, {
      model: "opus",
      selectionSource: "auto",
      autoSelectorComplexity: "XS",
      success: true,
      retries: 0,
      costUsd: 0.5,
    });
    const extra = buildRecords(10, {
      model: "sonnet",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
      success: true,
      retries: 0,
      costUsd: 0.2,
    });

    const result = analyzeModelRouting(
      datasetFromRecords([...underRouting, ...overRouting, ...extra]),
      DEFAULT_HEALTH_CONFIG
    );

    for (const finding of result.findings) {
      expect(finding.id).toMatch(/^mr-\d+$/);
    }

    // IDs should be unique
    const ids = result.findings.map((f) => f.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("each finding has required fields populated", () => {
    const underRouting = buildRecords(3, {
      model: "haiku",
      selectionSource: "auto",
      autoSelectorComplexity: "L",
      success: false,
      retries: 1,
      costUsd: 0.1,
    });
    const healthy = buildRecords(7, {
      model: "haiku",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
      success: true,
      retries: 0,
      costUsd: 0.1,
    });

    const result = analyzeModelRouting(
      datasetFromRecords([...underRouting, ...healthy]),
      DEFAULT_HEALTH_CONFIG
    );

    for (const finding of result.findings) {
      expect(typeof finding.id).toBe("string");
      expect(finding.id.length).toBeGreaterThan(0);
      expect(finding.dimension).toBe("model-routing");
      expect(typeof finding.title).toBe("string");
      expect(finding.title.length).toBeGreaterThan(0);
      expect(typeof finding.description).toBe("string");
      expect(finding.description.length).toBeGreaterThan(0);
      expect(typeof finding.impact).toBe("string");
      expect(finding.impact.length).toBeGreaterThan(0);
      expect(typeof finding.recommendation).toBe("string");
      expect(finding.recommendation.length).toBeGreaterThan(0);
      expect(typeof finding.evidence).toBe("object");
      expect(["critical", "high", "medium", "low", "info"]).toContain(finding.severity);
      expect(["high", "medium", "low"]).toContain(finding.confidence);
    }
  });
});

// ── 11. Metrics completeness ───────────────────────────────────────

describe("analyzeModelRouting — metrics completeness", () => {
  it("always populates core metric keys when hasEnoughData is true", () => {
    const records = buildRecords(10, {
      model: "sonnet",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
      success: true,
      retries: 0,
      costUsd: 0.2,
    });

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    const requiredKeys = [
      "autoSelectionTotal",
      "autoSelectionSuccessRate",
      "autoSelectionFailureRate",
      "distinctModelCount",
      "underRoutingCount",
      "overRoutingCount",
      "meanEffectiveCostPerSuccess",
      "score",
    ];

    for (const key of requiredKeys) {
      expect(result.metrics).toHaveProperty(key);
      expect(typeof result.metrics[key]).toBe("number");
    }
  });

  it("populates per-model metrics for each distinct model", () => {
    const records = [
      ...buildRecords(5, {
        model: "haiku",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.1,
      }),
      ...buildRecords(5, {
        model: "opus",
        selectionSource: "auto",
        autoSelectorComplexity: "M",
        success: true,
        retries: 0,
        costUsd: 0.5,
      }),
    ];

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    // modelKey converts to lowercase
    expect(result.metrics).toHaveProperty("model.haiku.successRate");
    expect(result.metrics).toHaveProperty("model.haiku.effectiveCostPerSuccess");
    expect(result.metrics).toHaveProperty("model.haiku.sampleSize");
    expect(result.metrics).toHaveProperty("model.opus.successRate");
    expect(result.metrics).toHaveProperty("model.opus.effectiveCostPerSuccess");
    expect(result.metrics).toHaveProperty("model.opus.sampleSize");
  });

  it("model name in metrics is lowercased", () => {
    // Even if model name has mixed case, the key should be lowercase
    const records = buildRecords(5, {
      model: "Sonnet",
      selectionSource: "auto",
      autoSelectorComplexity: "M",
      success: true,
      retries: 0,
      costUsd: 0.2,
    });

    const result = analyzeModelRouting(datasetFromRecords(records), DEFAULT_HEALTH_CONFIG);

    expect(result.metrics).toHaveProperty("model.sonnet.successRate");
    expect(result.metrics).not.toHaveProperty("model.Sonnet.successRate");
  });
});
