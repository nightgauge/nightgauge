/**
 * Integration tests: AutoModelSelector + StageModelCalibrationService (Issue #142)
 *
 * Regression coverage for the near-zero rank correlation bug: the estimator
 * previously rescaled every stage's cost by a single whole-run scale factor,
 * so it emitted only a handful of distinct total values regardless of what
 * each individual stage actually cost. Per-(stage, model) calibration
 * replaces that with an independent lookup per stage.
 */

import { describe, it, expect, vi } from "vitest";
import {
  AutoModelSelector,
  DEFAULT_MODEL_ENVELOPE,
  type IssueMetadata,
  type ComplexityLabel,
  type ModelEnvelope,
} from "../../src/analysis/AutoModelSelector.js";
import {
  StageModelCalibrationService,
  type StageModelCalibrationInput,
  type StageModelCalibrationTable,
} from "../../src/services/StageModelCalibrationService.js";

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

function buildTable(records: StageModelCalibrationInput[]): StageModelCalibrationTable {
  return StageModelCalibrationService.buildFromHistory(records);
}

function generateRecords(
  n: number,
  stage: string,
  model: string,
  costBase: number
): StageModelCalibrationInput[] {
  return Array.from({ length: n }, (_, i) => ({
    stage,
    model,
    cost_usd: costBase * (1 + (i / n - 0.5) * 0.2),
    input_tokens: 500_000 + i * 1000,
    output_tokens: 5_000 + i * 100,
  }));
}

describe("AutoModelSelector.estimatePipelineCost + StageModelCalibrationService", () => {
  const selector = new AutoModelSelector();

  it("each stage independently reflects its own (stage, model) cell, not a single scaled total", () => {
    // M-size feature: feature-dev routes to sonnet (STAGE_COMPLEXITY_MATRIX.dev.M).
    // Give feature-dev a wildly different calibrated cost than pr-create's cell,
    // and verify both land independently instead of being uniformly rescaled.
    const records = [
      ...generateRecords(6, "feature-dev", "sonnet", 40.0),
      ...generateRecords(6, "pr-create", "haiku", 0.15),
    ];
    const table = buildTable(records);

    const result = selector.estimatePipelineCost(makeMetadata("M"), undefined, table);

    const devStage = result.stages.find((s) => s.stage === "feature-dev")!;
    const prCreateStage = result.stages.find((s) => s.stage === "pr-create")!;

    expect(devStage.calibrated).toBe(true);
    expect(devStage.estimatedCost).toBeCloseTo(
      table.buckets["feature-dev"]!["sonnet"]!.p75_cost_usd,
      5
    );

    expect(prCreateStage.calibrated).toBe(true);
    expect(prCreateStage.estimatedCost).toBeCloseTo(
      table.buckets["pr-create"]!["haiku"]!.p75_cost_usd,
      5
    );

    // A stage with no calibration data at all keeps the static baseline
    const validateStage = result.stages.find((s) => s.stage === "feature-validate")!;
    expect(validateStage.calibrated).toBe(false);

    expect(result.calibrationUsed).toBe(true);
  });

  it("falls back to TOKEN_BASELINES for a stage without a calibrated cell", () => {
    const table = buildTable(generateRecords(10, "feature-dev", "sonnet", 40.0));
    const baseline = selector.estimatePipelineCost(makeMetadata("M"));
    const result = selector.estimatePipelineCost(makeMetadata("M"), undefined, table);

    const baselinePrCreate = baseline.stages.find((s) => s.stage === "pr-create")!;
    const resultPrCreate = result.stages.find((s) => s.stage === "pr-create")!;

    expect(resultPrCreate.calibrated).toBe(false);
    expect(resultPrCreate.estimatedCost).toBeCloseTo(baselinePrCreate.estimatedCost, 10);
  });

  it("does not use a cell below MIN_CALIBRATION_SAMPLES", () => {
    const table = buildTable(generateRecords(4, "feature-dev", "sonnet", 40.0));
    const result = selector.estimatePipelineCost(makeMetadata("M"), undefined, table);

    const devStage = result.stages.find((s) => s.stage === "feature-dev")!;
    expect(devStage.calibrated).toBe(false);
    expect(result.calibrationUsed).toBe(false);
  });

  it("uses a cell at exactly MIN_CALIBRATION_SAMPLES (boundary)", () => {
    const table = buildTable(generateRecords(5, "feature-dev", "sonnet", 40.0));
    const result = selector.estimatePipelineCost(makeMetadata("M"), undefined, table);

    const devStage = result.stages.find((s) => s.stage === "feature-dev")!;
    expect(devStage.calibrated).toBe(true);
  });

  it("does not fall back to a different model's calibrated cell for the same stage", () => {
    // Only opus has data for feature-dev; the M-size run routes feature-dev to sonnet.
    const table = buildTable(generateRecords(10, "feature-dev", "opus", 60.0));
    const result = selector.estimatePipelineCost(makeMetadata("M"), undefined, table);

    const devStage = result.stages.find((s) => s.stage === "feature-dev")!;
    expect(devStage.model).toBe("sonnet");
    expect(devStage.calibrated).toBe(false);
  });

  it("skipped stages remain $0 and uncalibrated regardless of table contents", () => {
    const table = buildTable(generateRecords(10, "pr-merge", "sonnet", 3.0));
    const result = selector.estimatePipelineCost(makeMetadata("M"), ["pr-merge"], table);

    const merge = result.stages.find((s) => s.stage === "pr-merge")!;
    expect(merge.skipped).toBe(true);
    expect(merge.estimatedCost).toBe(0);
    expect(merge.calibrated).toBe(false);
  });

  it("falls back to baselines entirely when the table is null", () => {
    const withNull = selector.estimatePipelineCost(makeMetadata("M"), undefined, null);
    const withoutArg = selector.estimatePipelineCost(makeMetadata("M"));

    expect(withNull.calibrationUsed).toBe(false);
    expect(withNull.totalEstimatedCost).toBeCloseTo(withoutArg.totalEstimatedCost, 10);
  });

  it("falls back to baselines entirely when the table is undefined", () => {
    const result = selector.estimatePipelineCost(makeMetadata("M"));
    expect(result.calibrationUsed).toBe(false);
  });

  it("always reports baselineEstimatedCost for comparison, calibrated or not", () => {
    const table = buildTable(generateRecords(10, "feature-dev", "sonnet", 40.0));
    const result = selector.estimatePipelineCost(makeMetadata("M"), undefined, table);
    const baseline = selector.estimatePipelineCost(makeMetadata("M"));

    expect(result.baselineEstimatedCost).toBeCloseTo(baseline.totalEstimatedCost, 10);
  });

  it("passes the caller's envelope into selectModel instead of always defaulting", () => {
    const spy = vi.spyOn(selector, "selectModel");
    const restrictiveEnvelope: ModelEnvelope = { floor: "haiku", ceiling: "sonnet" };

    selector.estimatePipelineCost(makeMetadata("L"), undefined, null, restrictiveEnvelope);

    // Every non-skipped call should receive the passed envelope, not the default
    const calls = spy.mock.calls;
    expect(calls.length).toBeGreaterThan(0);
    for (const call of calls) {
      const envelopeArg = call[4];
      expect(envelopeArg).toEqual(restrictiveEnvelope);
    }
    spy.mockRestore();
  });

  it("defaults to DEFAULT_MODEL_ENVELOPE when no envelope is passed", () => {
    const spy = vi.spyOn(selector, "selectModel");
    selector.estimatePipelineCost(makeMetadata("L"));

    for (const call of spy.mock.calls) {
      expect(call[4]).toEqual(DEFAULT_MODEL_ENVELOPE);
    }
    spy.mockRestore();
  });

  it("a restrictive envelope changes which model is estimated and thus which cell is looked up", () => {
    // L-size feature-dev normally routes to opus (STAGE_COMPLEXITY_MATRIX.dev.L).
    // Only sonnet has calibration data — an envelope capping the ceiling at
    // sonnet should make the estimator select sonnet and hit that cell.
    const table = buildTable(generateRecords(10, "feature-dev", "sonnet", 12.0));
    const restrictiveEnvelope: ModelEnvelope = { floor: "haiku", ceiling: "sonnet" };

    const result = selector.estimatePipelineCost(
      makeMetadata("L"),
      undefined,
      table,
      restrictiveEnvelope
    );

    const devStage = result.stages.find((s) => s.stage === "feature-dev")!;
    expect(devStage.model).toBe("sonnet");
    expect(devStage.calibrated).toBe(true);
  });
});
