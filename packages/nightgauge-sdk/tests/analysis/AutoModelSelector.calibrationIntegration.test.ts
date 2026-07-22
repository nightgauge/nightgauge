/**
 * Integration tests: AutoModelSelector + CalibrationService feedback loop
 *
 * Verifies that execution history flows through CalibrationService into
 * AutoModelSelector's cost estimates, closing the estimation → execution →
 * learning → re-estimation loop.
 *
 * Tests cover:
 * - Calibration data overrides static baselines when sufficient samples exist
 * - Graceful fallback to static baselines with insufficient calibration data
 * - Per-stage cost scaling preserves relative stage proportions
 * - End-to-end simulation of multiple pipeline runs building calibration
 * - Edge cases: zero costs, skipped stages, missing buckets
 */

import { describe, it, expect } from "vitest";
import {
  AutoModelSelector,
  type IssueMetadata,
  type PipelineCostEstimate,
  type ComplexityLabel,
} from "../../src/analysis/AutoModelSelector.js";
import {
  CalibrationService,
  type CalibrationInput,
  type CalibrationTable,
  type SizeBucket,
} from "../../src/services/CalibrationService.js";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeMetadata(
  size: ComplexityLabel = "M",
  overrides: Partial<IssueMetadata> = {}
): IssueMetadata {
  return {
    labels: [`size:${size}`, "type:feature"],
    title: "Test issue",
    ...overrides,
  };
}

/** Build a CalibrationTable from a list of CalibrationInput records */
function buildCalibration(records: CalibrationInput[]): CalibrationTable {
  return CalibrationService.buildFromHistory(records);
}

/** Generate N calibration records for a given size bucket and cost */
function generateRecords(
  n: number,
  size: SizeBucket,
  costBase: number,
  variance: number = 0.1
): CalibrationInput[] {
  return Array.from({ length: n }, (_, i) => ({
    outcome: "complete",
    size,
    // Spread costs around the base to get realistic p25/median/p75
    cost_usd: costBase * (1 + (i / n - 0.5) * variance * 2),
    duration_ms: 120_000 + i * 1000,
    total_tokens: 500_000 + i * 10_000,
  }));
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("AutoModelSelector + CalibrationService Integration", () => {
  const selector = new AutoModelSelector();

  describe("calibration overrides static baselines", () => {
    it("uses calibration median when ≥5 samples exist for the size bucket", () => {
      // Build calibration from 10 M-sized runs averaging ~$8.50
      const records = generateRecords(10, "M", 8.5);
      const calibration = buildCalibration(records);

      const withCalibration = selector.estimatePipelineCost(
        makeMetadata("M"),
        undefined,
        calibration
      );

      const withoutCalibration = selector.estimatePipelineCost(makeMetadata("M"));

      // Calibrated estimate should be close to $8.50 (the median of our records)
      expect(withCalibration.totalEstimatedCost).toBeCloseTo(
        calibration.buckets.elevated!["M"]!.median_cost_usd,
        2
      );

      // Should differ from the static baseline
      expect(withCalibration.totalEstimatedCost).not.toBeCloseTo(
        withoutCalibration.totalEstimatedCost,
        1
      );

      // Metadata fields
      expect(withCalibration.calibrationUsed).toBe(true);
      expect(withCalibration.calibrationSampleCount).toBe(10);
      expect(withCalibration.baselineEstimatedCost).toBe(withoutCalibration.totalEstimatedCost);
    });

    it("falls back to static baselines when < 5 samples exist", () => {
      const records = generateRecords(3, "M", 8.5);
      const calibration = buildCalibration(records);

      const result = selector.estimatePipelineCost(makeMetadata("M"), undefined, calibration);

      const baseline = selector.estimatePipelineCost(makeMetadata("M"));

      // Should match static baseline exactly
      expect(result.totalEstimatedCost).toBeCloseTo(baseline.totalEstimatedCost, 10);
      expect(result.calibrationUsed).toBe(false);
      expect(result.calibrationSampleCount).toBe(0);
      expect(result.baselineEstimatedCost).toBeUndefined();
    });

    it("falls back when calibration is null", () => {
      const result = selector.estimatePipelineCost(makeMetadata("M"), undefined, null);

      const baseline = selector.estimatePipelineCost(makeMetadata("M"));

      expect(result.totalEstimatedCost).toBeCloseTo(baseline.totalEstimatedCost, 10);
      expect(result.calibrationUsed).toBe(false);
    });

    it("falls back when calibration is undefined", () => {
      const result = selector.estimatePipelineCost(makeMetadata("M"));
      expect(result.calibrationUsed).toBe(false);
    });

    it("falls back when size bucket has no calibration data", () => {
      // Only have data for S, but issue is M
      const records = generateRecords(10, "S", 3.0);
      const calibration = buildCalibration(records);

      const result = selector.estimatePipelineCost(makeMetadata("M"), undefined, calibration);

      expect(result.calibrationUsed).toBe(false);
    });
  });

  describe("per-stage cost scaling", () => {
    it("preserves relative stage cost proportions after calibration", () => {
      const records = generateRecords(10, "M", 12.0);
      const calibration = buildCalibration(records);

      const baseline = selector.estimatePipelineCost(makeMetadata("M"));
      const calibrated = selector.estimatePipelineCost(makeMetadata("M"), undefined, calibration);

      // Get active (non-skipped) stages
      const baselineActive = baseline.stages.filter((s) => !s.skipped);
      const calibratedActive = calibrated.stages.filter((s) => !s.skipped);

      // Compute proportions for each stage
      const baselineProportions = baselineActive.map(
        (s) => s.estimatedCost / baseline.totalEstimatedCost
      );
      const calibratedProportions = calibratedActive.map(
        (s) => s.estimatedCost / calibrated.totalEstimatedCost
      );

      // Proportions should be identical (within floating point)
      for (let i = 0; i < baselineProportions.length; i++) {
        expect(calibratedProportions[i]).toBeCloseTo(baselineProportions[i], 10);
      }
    });

    it("per-stage costs sum to calibrated total", () => {
      const records = generateRecords(8, "L", 15.0);
      const calibration = buildCalibration(records);

      const result = selector.estimatePipelineCost(makeMetadata("L"), undefined, calibration);

      const stageSum = result.stages.reduce((sum, s) => sum + s.estimatedCost, 0);
      expect(stageSum).toBeCloseTo(result.totalEstimatedCost, 10);
    });

    it("skipped stages remain $0 after calibration scaling", () => {
      const records = generateRecords(10, "M", 8.0);
      const calibration = buildCalibration(records);

      const result = selector.estimatePipelineCost(
        makeMetadata("M"),
        ["feature-validate", "pr-merge"],
        calibration
      );

      const skipped = result.stages.filter((s) => s.skipped);
      expect(skipped).toHaveLength(2);
      for (const s of skipped) {
        expect(s.estimatedCost).toBe(0);
      }

      // Active stages should still sum to the total
      const activeSum = result.stages
        .filter((s) => !s.skipped)
        .reduce((sum, s) => sum + s.estimatedCost, 0);
      expect(activeSum).toBeCloseTo(result.totalEstimatedCost, 10);
    });
  });

  describe("end-to-end simulation: execution history builds calibration", () => {
    it("estimates improve as execution history accumulates", () => {
      // Phase 1: No calibration data — static baselines only
      const noCalibration = selector.estimatePipelineCost(makeMetadata("M"));
      const staticEstimate = noCalibration.totalEstimatedCost;

      // Simulate actual pipeline runs that cost significantly more than the
      // static estimate (matching the user's real scenario)
      const actualCosts = [8.65, 12.31, 7.2, 9.5, 6.8, 8.1, 10.2];
      const actualMedian = [...actualCosts].sort((a, b) => a - b)[
        Math.floor(actualCosts.length / 2)
      ]; // ~8.65

      // Phase 2: Build calibration from these "actual" runs
      const records: CalibrationInput[] = actualCosts.map((cost) => ({
        outcome: "complete",
        size: "M",
        cost_usd: cost,
        duration_ms: 180_000,
        total_tokens: 800_000,
      }));
      const calibration = buildCalibration(records);

      // Phase 3: Re-estimate with calibration
      const withCalibration = selector.estimatePipelineCost(
        makeMetadata("M"),
        undefined,
        calibration
      );

      // The calibrated estimate should be much closer to what actually happens
      expect(withCalibration.totalEstimatedCost).toBeGreaterThan(staticEstimate);
      expect(withCalibration.totalEstimatedCost).toBeCloseTo(
        calibration.buckets.elevated!["M"]!.median_cost_usd,
        2
      );
      expect(withCalibration.calibrationUsed).toBe(true);

      // Verify the estimate is now in the right ballpark of actual costs
      const estimateRatio = withCalibration.totalEstimatedCost / actualMedian;
      expect(estimateRatio).toBeGreaterThan(0.8);
      expect(estimateRatio).toBeLessThan(1.2);
    });

    it("different size buckets get independent calibration", () => {
      // S issues cost ~$3, M issues cost ~$9, L issues cost ~$18
      const sRecords = generateRecords(8, "S", 3.0);
      const mRecords = generateRecords(8, "M", 9.0);
      const lRecords = generateRecords(8, "L", 18.0);

      const calibration = buildCalibration([...sRecords, ...mRecords, ...lRecords]);

      const sEstimate = selector.estimatePipelineCost(makeMetadata("S"), undefined, calibration);
      const mEstimate = selector.estimatePipelineCost(makeMetadata("M"), undefined, calibration);
      const lEstimate = selector.estimatePipelineCost(makeMetadata("L"), undefined, calibration);

      // Each should reflect its own bucket's median
      expect(sEstimate.totalEstimatedCost).toBeCloseTo(
        calibration.buckets.elevated!["S"]!.median_cost_usd,
        1
      );
      expect(mEstimate.totalEstimatedCost).toBeCloseTo(
        calibration.buckets.elevated!["M"]!.median_cost_usd,
        1
      );
      expect(lEstimate.totalEstimatedCost).toBeCloseTo(
        calibration.buckets.elevated!["L"]!.median_cost_usd,
        1
      );

      // Ordering should be S < M < L
      expect(sEstimate.totalEstimatedCost).toBeLessThan(mEstimate.totalEstimatedCost);
      expect(mEstimate.totalEstimatedCost).toBeLessThan(lEstimate.totalEstimatedCost);
    });

    it("simulates the exact alert scenario: $4.14 estimate vs $8-45 actual", () => {
      // The user's actual scenario: static estimate is ~$4.14 for M
      const staticEstimate = selector.estimatePipelineCost(makeMetadata("M"));

      // These are the actual costs from the user's alerts
      const actualRuns: CalibrationInput[] = [
        {
          outcome: "complete",
          size: "M",
          cost_usd: 45.01,
          duration_ms: 600_000,
          total_tokens: 2_000_000,
        },
        {
          outcome: "complete",
          size: "M",
          cost_usd: 12.31,
          duration_ms: 300_000,
          total_tokens: 1_000_000,
        },
        {
          outcome: "complete",
          size: "M",
          cost_usd: 8.65,
          duration_ms: 200_000,
          total_tokens: 800_000,
        },
        // Add more runs for a realistic sample
        {
          outcome: "complete",
          size: "M",
          cost_usd: 7.5,
          duration_ms: 180_000,
          total_tokens: 750_000,
        },
        {
          outcome: "complete",
          size: "M",
          cost_usd: 9.2,
          duration_ms: 220_000,
          total_tokens: 850_000,
        },
      ];

      const calibration = buildCalibration(actualRuns);
      const calibratedEstimate = selector.estimatePipelineCost(
        makeMetadata("M"),
        undefined,
        calibration
      );

      // Before calibration: ~$4.14 (stuck on baselines)
      // After calibration: should be ~$9.20 (median of actual costs)
      expect(calibratedEstimate.totalEstimatedCost).toBeGreaterThan(
        staticEstimate.totalEstimatedCost * 1.5
      );
      expect(calibratedEstimate.calibrationUsed).toBe(true);

      // The ratio between actual and estimated should now be much closer to 1.0
      const medianActualCost = calibration.buckets.elevated!["M"]!.median_cost_usd;
      const ratio = calibratedEstimate.totalEstimatedCost / medianActualCost;
      expect(ratio).toBeCloseTo(1.0, 1);
    });
  });

  describe("edge cases", () => {
    it("handles calibration with zero median cost gracefully", () => {
      const calibration: CalibrationTable = {
        schema_version: "2",
        updated_at: new Date().toISOString(),
        total_runs_analyzed: 5,
        buckets: {
          elevated: {
            M: {
              median_cost_usd: 0,
              median_duration_ms: 120_000,
              median_total_tokens: 500_000,
              sample_count: 5,
              p25_cost_usd: 0,
              p75_cost_usd: 0,
              p25_duration_ms: 100_000,
              p75_duration_ms: 140_000,
              p25_total_tokens: 400_000,
              p75_total_tokens: 600_000,
              last_updated: new Date().toISOString(),
            },
          },
        },
      };

      const result = selector.estimatePipelineCost(makeMetadata("M"), undefined, calibration);

      // Should NOT use calibration with zero median (falls back to baseline)
      expect(result.calibrationUsed).toBe(false);
    });

    it("handles calibration with exactly 5 samples (boundary)", () => {
      const records = generateRecords(5, "M", 7.0);
      const calibration = buildCalibration(records);

      const result = selector.estimatePipelineCost(makeMetadata("M"), undefined, calibration);

      // Exactly 5 should be sufficient
      expect(result.calibrationUsed).toBe(true);
      expect(result.calibrationSampleCount).toBe(5);
    });

    it("handles calibration with 4 samples (just below boundary)", () => {
      const records = generateRecords(4, "M", 7.0);
      const calibration = buildCalibration(records);

      const result = selector.estimatePipelineCost(makeMetadata("M"), undefined, calibration);

      // 4 samples should NOT be sufficient
      expect(result.calibrationUsed).toBe(false);
    });

    it("handles empty buckets object", () => {
      const calibration: CalibrationTable = {
        schema_version: "2",
        updated_at: new Date().toISOString(),
        total_runs_analyzed: 0,
        buckets: {},
      };

      const result = selector.estimatePipelineCost(makeMetadata("M"), undefined, calibration);

      expect(result.calibrationUsed).toBe(false);
    });

    it("XL issues use calibration when available for XL bucket", () => {
      const records = generateRecords(10, "XL", 25.0);
      const calibration = buildCalibration(records);

      const result = selector.estimatePipelineCost(makeMetadata("XL"), undefined, calibration);

      expect(result.calibrationUsed).toBe(true);
      expect(result.totalEstimatedCost).toBeCloseTo(
        calibration.buckets.elevated!["XL"]!.median_cost_usd,
        2
      );
    });

    it("XS issues use calibration when available for XS bucket", () => {
      const records = generateRecords(6, "XS", 1.5);
      const calibration = buildCalibration(records);

      const result = selector.estimatePipelineCost(makeMetadata("XS"), undefined, calibration);

      expect(result.calibrationUsed).toBe(true);
      expect(result.totalEstimatedCost).toBeCloseTo(
        calibration.buckets.elevated!["XS"]!.median_cost_usd,
        2
      );
    });

    it("all stages skipped returns $0 even with calibration", () => {
      const records = generateRecords(10, "M", 8.0);
      const calibration = buildCalibration(records);

      const allStages = [
        "issue-pickup",
        "feature-planning",
        "feature-dev",
        "feature-validate",
        "pr-create",
        "pr-merge",
      ];
      const result = selector.estimatePipelineCost(makeMetadata("M"), allStages, calibration);

      // When all stages are skipped, baseline total is $0, so calibration
      // won't apply (no proportional scaling possible)
      expect(result.totalEstimatedCost).toBe(0);
    });
  });

  describe("CalibrationService.buildFromHistory round-trip", () => {
    it("builds accurate calibration from diverse execution records", () => {
      // Simulate a mix of sizes and outcomes
      const records: CalibrationInput[] = [
        // M-size completions
        ...generateRecords(7, "M", 8.0, 0.3),
        // S-size completions
        ...generateRecords(6, "S", 3.5, 0.2),
        // Failed runs (should be excluded)
        {
          outcome: "failed",
          size: "M",
          cost_usd: 2.0,
          duration_ms: 30_000,
          total_tokens: 100_000,
        },
        {
          outcome: "cancelled",
          size: "S",
          cost_usd: 0.5,
          duration_ms: 10_000,
          total_tokens: 50_000,
        },
        // Null size (should be excluded)
        {
          outcome: "complete",
          size: null,
          cost_usd: 5.0,
          duration_ms: 120_000,
          total_tokens: 500_000,
        },
      ];

      const calibration = buildCalibration(records);

      // Only completed records with valid sizes should be analyzed
      expect(calibration.total_runs_analyzed).toBe(13); // 7 + 6
      expect(calibration.buckets.elevated!["M"]!.sample_count).toBe(7);
      expect(calibration.buckets.elevated!["S"]!.sample_count).toBe(6);
      expect(calibration.buckets.elevated!["L"]).toBeUndefined();

      // Use calibration for estimates
      const mEstimate = selector.estimatePipelineCost(makeMetadata("M"), undefined, calibration);
      const sEstimate = selector.estimatePipelineCost(makeMetadata("S"), undefined, calibration);

      expect(mEstimate.calibrationUsed).toBe(true);
      expect(sEstimate.calibrationUsed).toBe(true);
      expect(mEstimate.totalEstimatedCost).toBeGreaterThan(sEstimate.totalEstimatedCost);
    });
  });

  describe("backward compatibility", () => {
    it("existing tests pass — signature is backward compatible", () => {
      // Two-arg form (no calibration)
      const result1 = selector.estimatePipelineCost(makeMetadata("M"));
      expect(result1.stages).toHaveLength(6);
      expect(result1.totalEstimatedCost).toBeGreaterThan(0);

      // Three-arg form with skipStages and no calibration
      const result2 = selector.estimatePipelineCost(makeMetadata("M"), ["pr-merge"]);
      expect(result2.stages).toHaveLength(6);
      expect(result2.totalEstimatedCost).toBeGreaterThan(0);

      // Three-arg form with undefined calibration
      const result3 = selector.estimatePipelineCost(makeMetadata("M"), undefined, undefined);
      expect(result3.stages).toHaveLength(6);
      expect(result3.totalEstimatedCost).toBeCloseTo(result1.totalEstimatedCost, 10);
    });
  });
});
