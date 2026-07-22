import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as os from "node:os";
import * as yaml from "js-yaml";
import { ComplexityModelService } from "../../src/services/ComplexityModelService.js";
import { OutcomeRecorder } from "../../src/services/OutcomeRecorder.js";
import type {
  ComplexityModel,
  ExecutionOutcome,
} from "../../src/context/schemas/complexity-model.js";

/**
 * Integration tests for the outcome feedback loop.
 *
 * These tests exercise the full end-to-end cycle of recording execution
 * outcomes against a complexity model persisted on disk, verifying that
 * prediction accuracy, size calibration, and pattern confidence evolve
 * correctly across multiple calibration cycles.
 *
 * @see Issue #650 - Feedback Loop: Record Execution Outcomes
 */

/** Starting model fixture with no existing prediction_accuracy */
const baseModel: ComplexityModel = {
  schema_version: "1.0",
  last_updated: "2026-02-05",
  total_observations: 0,
  decay: { enabled: true, half_life_days: 30 },
  model_tracking: {
    current_default: "claude-opus-4-6",
    observations_by_model: {},
  },
  patterns: {
    high_complexity: [
      {
        match: "batch|multiple",
        modifier: 1.5,
        confidence: 0.8,
        rationale: "Batch ops",
        observations: 3,
      },
    ],
    medium_complexity: [],
    low_complexity: [],
  },
  size_calibration: {
    XS: { expected_lines: 50, actual_average_lines: 50, sample_count: 0 },
    S: { expected_lines: 150, actual_average_lines: 150, sample_count: 0 },
    M: { expected_lines: 500, actual_average_lines: 500, sample_count: 0 },
    L: { expected_lines: 1200, actual_average_lines: 1200, sample_count: 0 },
    XL: { expected_lines: 2500, actual_average_lines: 2500, sample_count: 0 },
  },
  type_adjustments: {
    feature: { modifier: 0.0, observations: 0 },
  },
  priority_adjustments: {},
  lines_changed_thresholds: { XS: 50, S: 150, M: 500, L: 1200, XL: 2500 },
  learnings: [],
};

/** Helper to create an ExecutionOutcome with sensible defaults */
function createOutcome(
  issueNumber: number,
  overrides: Partial<ExecutionOutcome> = {}
): ExecutionOutcome {
  return {
    issue_number: issueNumber,
    issue_type: "feature",
    pr_number: issueNumber + 1000,
    predicted_size: "M",
    actual_lines_changed: 450,
    stages_run: ["issue-pickup", "feature-planning", "feature-dev", "pr-create", "pr-merge"],
    stages_failed: [],
    model_used: "claude-opus-4-6",
    completed_at: new Date().toISOString(),
    outcome: "success",
    patterns_matched: ["batch|multiple"],
    ...overrides,
  };
}

describe("Outcome Feedback Loop Integration", () => {
  let tempDir: string;
  let modelPath: string;

  beforeEach(async () => {
    tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "nightgauge-outcome-feedback-"));
    modelPath = path.join(tempDir, "complexity-model.yaml");
    await fs.writeFile(modelPath, yaml.dump(baseModel), "utf-8");
  });

  afterEach(async () => {
    await fs.rm(tempDir, { recursive: true, force: true });
  });

  it("should calibrate over 3+ sequential cycles", async () => {
    const modelService = new ComplexityModelService(modelPath);
    const recorder = new OutcomeRecorder(modelService);

    // --- Cycle 1: M prediction, 450 actual lines (within M threshold of 500) ---
    const outcome1 = createOutcome(101, {
      predicted_size: "M",
      actual_lines_changed: 450,
    });
    const result1 = await recorder.recordOutcome(outcome1);
    expect(result1.skipped).toBe(false);
    await modelService.save(result1.model);

    expect(result1.model.total_observations).toBe(1);
    expect(result1.model.prediction_accuracy?.total_predictions).toBe(1);
    expect(result1.model.size_calibration.M.sample_count).toBe(1);

    // --- Cycle 2: S prediction, 120 actual lines (within S threshold of 150) ---
    const outcome2 = createOutcome(102, {
      predicted_size: "S",
      actual_lines_changed: 120,
    });
    const result2 = await recorder.recordOutcome(outcome2);
    expect(result2.skipped).toBe(false);
    await modelService.save(result2.model);

    expect(result2.model.total_observations).toBe(2);
    expect(result2.model.prediction_accuracy?.total_predictions).toBe(2);
    expect(result2.model.size_calibration.S.sample_count).toBe(1);
    // M sample_count should still be 1 from cycle 1
    expect(result2.model.size_calibration.M.sample_count).toBe(1);

    // --- Cycle 3: L prediction, 1100 actual lines (within L threshold of 1200) ---
    const outcome3 = createOutcome(103, {
      predicted_size: "L",
      actual_lines_changed: 1100,
    });
    const result3 = await recorder.recordOutcome(outcome3);
    expect(result3.skipped).toBe(false);
    await modelService.save(result3.model);

    expect(result3.model.total_observations).toBe(3);
    expect(result3.model.prediction_accuracy?.total_predictions).toBe(3);
    expect(result3.model.size_calibration.L.sample_count).toBe(1);

    // All three predictions should be correct (actual falls within predicted bucket or adjacent)
    expect(result3.model.prediction_accuracy?.correct_predictions).toBe(3);

    // Pattern confidence should have increased 3 times (+0.02 each)
    // Original confidence: 0.80 → expected: 0.86
    const highPattern = result3.model.patterns.high_complexity.find(
      (p) => p.match === "batch|multiple"
    );
    expect(highPattern).toBeDefined();
    expect(highPattern!.confidence).toBeCloseTo(0.86, 2);

    // Verify recent_outcomes log has all 3 entries
    expect(result3.model.prediction_accuracy?.recent_outcomes).toHaveLength(3);
  });

  it("should persist data through save/load cycles", async () => {
    const modelService = new ComplexityModelService(modelPath);
    const recorder = new OutcomeRecorder(modelService);

    // --- Record first outcome and save ---
    const outcome1 = createOutcome(201, {
      predicted_size: "M",
      actual_lines_changed: 480,
    });
    const result1 = await recorder.recordOutcome(outcome1);
    expect(result1.skipped).toBe(false);
    await modelService.save(result1.model);

    // --- Reload from disk and verify persistence ---
    const loadedModel = await modelService.load();
    expect(loadedModel.total_observations).toBe(1);
    expect(loadedModel.prediction_accuracy).toBeDefined();
    expect(loadedModel.prediction_accuracy?.total_predictions).toBe(1);
    expect(loadedModel.prediction_accuracy?.correct_predictions).toBe(1);
    expect(loadedModel.prediction_accuracy?.recent_outcomes).toHaveLength(1);
    expect(loadedModel.prediction_accuracy?.recent_outcomes[0].issue_number).toBe(201);

    // Size calibration persisted
    expect(loadedModel.size_calibration.M.sample_count).toBe(1);
    expect(loadedModel.size_calibration.M.actual_average_lines).toBe(480);

    // Model tracking persisted
    expect(loadedModel.model_tracking.observations_by_model["claude-opus-4-6"]).toBe(1);

    // --- Record second outcome (loads from disk internally) ---
    const outcome2 = createOutcome(202, {
      predicted_size: "S",
      actual_lines_changed: 100,
    });
    const result2 = await recorder.recordOutcome(outcome2);
    expect(result2.skipped).toBe(false);
    await modelService.save(result2.model);

    // --- Final reload to verify both outcomes persisted ---
    const finalModel = await modelService.load();
    expect(finalModel.total_observations).toBe(2);
    expect(finalModel.prediction_accuracy?.total_predictions).toBe(2);
    expect(finalModel.prediction_accuracy?.correct_predictions).toBe(2);
    expect(finalModel.prediction_accuracy?.recent_outcomes).toHaveLength(2);

    // Verify the on-disk YAML file is valid and readable
    const rawContent = await fs.readFile(modelPath, "utf-8");
    const parsedYaml = yaml.load(rawContent) as Record<string, unknown>;
    expect(parsedYaml).toHaveProperty("prediction_accuracy");
    expect(parsedYaml).toHaveProperty("total_observations", 2);
  });

  it("should track mixed correct and incorrect predictions", async () => {
    const modelService = new ComplexityModelService(modelPath);
    const recorder = new OutcomeRecorder(modelService);

    // --- Outcome 1: Correct prediction (M predicted, 400 actual = M bucket) ---
    const correctOutcome = createOutcome(301, {
      predicted_size: "M",
      actual_lines_changed: 400,
      issue_type: "feature",
    });
    const r1 = await recorder.recordOutcome(correctOutcome);
    await modelService.save(r1.model);

    // --- Outcome 2: Incorrect prediction (XS predicted, 1500 actual = XL bucket) ---
    // XS vs XL differ by more than 1 step → incorrect
    const incorrectOutcome = createOutcome(302, {
      predicted_size: "XS",
      actual_lines_changed: 1500,
      issue_type: "bug",
      patterns_matched: [],
    });
    const r2 = await recorder.recordOutcome(incorrectOutcome);
    await modelService.save(r2.model);

    // --- Outcome 3: Correct prediction (S predicted, 60 actual = S bucket, adjacent to XS) ---
    const adjacentCorrect = createOutcome(303, {
      predicted_size: "S",
      actual_lines_changed: 60,
      issue_type: "feature",
    });
    const r3 = await recorder.recordOutcome(adjacentCorrect);
    await modelService.save(r3.model);

    // --- Outcome 4: Incorrect prediction (XS predicted, 800 actual = L bucket) ---
    const incorrectOutcome2 = createOutcome(304, {
      predicted_size: "XS",
      actual_lines_changed: 800,
      issue_type: "bug",
      patterns_matched: [],
    });
    const r4 = await recorder.recordOutcome(incorrectOutcome2);
    await modelService.save(r4.model);

    const finalModel = r4.model;

    // Total predictions: 4, correct: 2 (outcomes 1 and 3)
    expect(finalModel.prediction_accuracy?.total_predictions).toBe(4);
    expect(finalModel.prediction_accuracy?.correct_predictions).toBe(2);

    // by_type accuracy: feature = 2/2 correct, bug = 0/2 correct
    const byType = finalModel.prediction_accuracy?.by_type;
    expect(byType?.feature).toEqual({ total: 2, correct: 2 });
    expect(byType?.bug).toEqual({ total: 2, correct: 0 });

    // by_size accuracy
    const bySize = finalModel.prediction_accuracy?.by_size;
    // M predicted once, was correct
    expect(bySize?.M).toEqual({ total: 1, correct: 1 });
    // XS predicted twice, both incorrect
    expect(bySize?.XS).toEqual({ total: 2, correct: 0 });
    // S predicted once, was correct (adjacent tolerance)
    expect(bySize?.S).toEqual({ total: 1, correct: 1 });

    // Verify recent_outcomes has was_correct flags set properly
    const recentOutcomes = finalModel.prediction_accuracy?.recent_outcomes ?? [];
    expect(recentOutcomes).toHaveLength(4);
    expect(recentOutcomes[0].was_correct).toBe(true); // issue 301
    expect(recentOutcomes[1].was_correct).toBe(false); // issue 302
    expect(recentOutcomes[2].was_correct).toBe(true); // issue 303
    expect(recentOutcomes[3].was_correct).toBe(false); // issue 304
  });

  it("should preserve model data that is not outcome-related", async () => {
    const modelService = new ComplexityModelService(modelPath);
    const recorder = new OutcomeRecorder(modelService);

    // Record a few outcomes to exercise multiple update paths
    const outcome1 = createOutcome(401, {
      predicted_size: "M",
      actual_lines_changed: 450,
    });
    const r1 = await recorder.recordOutcome(outcome1);
    await modelService.save(r1.model);

    const outcome2 = createOutcome(402, {
      predicted_size: "L",
      actual_lines_changed: 1000,
    });
    const r2 = await recorder.recordOutcome(outcome2);
    await modelService.save(r2.model);

    // Reload and verify non-outcome fields are untouched
    const finalModel = await modelService.load();

    // Schema version preserved
    expect(finalModel.schema_version).toBe(baseModel.schema_version);

    // Decay settings preserved exactly
    expect(finalModel.decay).toEqual(baseModel.decay);

    // Learnings array preserved
    expect(finalModel.learnings).toEqual(baseModel.learnings);

    // Lines changed thresholds preserved
    expect(finalModel.lines_changed_thresholds).toEqual(baseModel.lines_changed_thresholds);

    // Priority adjustments preserved (empty object)
    expect(finalModel.priority_adjustments).toEqual(baseModel.priority_adjustments);

    // Pattern structure preserved (match, modifier, rationale, observations remain)
    // Note: confidence may have been adjusted by the feedback loop, so we
    // check structural fields only.
    const highPattern = finalModel.patterns.high_complexity[0];
    expect(highPattern.match).toBe("batch|multiple");
    expect(highPattern.modifier).toBe(1.5);
    expect(highPattern.rationale).toBe("Batch ops");
    expect(highPattern.observations).toBe(3);

    // Empty pattern categories preserved
    expect(finalModel.patterns.medium_complexity).toEqual([]);
    expect(finalModel.patterns.low_complexity).toEqual([]);

    // Sizes that were NOT recorded should be unchanged
    expect(finalModel.size_calibration.XS).toEqual(baseModel.size_calibration.XS);
    expect(finalModel.size_calibration.S).toEqual(baseModel.size_calibration.S);
    expect(finalModel.size_calibration.XL).toEqual(baseModel.size_calibration.XL);
  });

  it("should increase correct_predictions and pattern confidence after correct prediction", async () => {
    const modelService = new ComplexityModelService(modelPath);
    const recorder = new OutcomeRecorder(modelService);

    // Record the initial model's pattern confidence
    const initialConfidence = baseModel.patterns.high_complexity[0].confidence; // 0.80

    // --- Correct prediction: predicted M, actual 450 lines = M bucket ---
    const outcome = createOutcome(501, {
      predicted_size: "M",
      actual_lines_changed: 450,
      patterns_matched: ["batch|multiple"],
    });
    const result = await recorder.recordOutcome(outcome);
    await modelService.save(result.model);

    // correct_predictions should have increased
    expect(result.model.prediction_accuracy?.correct_predictions).toBe(1);
    expect(result.model.prediction_accuracy?.total_predictions).toBe(1);

    // Pattern confidence should have increased by CONFIDENCE_BOOST (0.02)
    const updatedPattern = result.model.patterns.high_complexity.find(
      (p) => p.match === "batch|multiple"
    );
    expect(updatedPattern).toBeDefined();
    expect(updatedPattern!.confidence).toBe(initialConfidence + 0.02);
    expect(updatedPattern!.confidence).toBeGreaterThan(initialConfidence);

    // --- Another correct prediction to verify continued improvement ---
    const outcome2 = createOutcome(502, {
      predicted_size: "S",
      actual_lines_changed: 100,
      patterns_matched: ["batch|multiple"],
    });
    const result2 = await recorder.recordOutcome(outcome2);
    await modelService.save(result2.model);

    expect(result2.model.prediction_accuracy?.correct_predictions).toBe(2);

    // Pattern confidence increased again (+0.02 more)
    const updatedPattern2 = result2.model.patterns.high_complexity.find(
      (p) => p.match === "batch|multiple"
    );
    expect(updatedPattern2!.confidence).toBe(initialConfidence + 0.04);
    expect(updatedPattern2!.confidence).toBeGreaterThan(updatedPattern!.confidence);
  });

  it("should handle idempotency — skip duplicate issue numbers", async () => {
    const modelService = new ComplexityModelService(modelPath);
    const recorder = new OutcomeRecorder(modelService);

    // Record first outcome
    const outcome = createOutcome(601);
    const result1 = await recorder.recordOutcome(outcome);
    expect(result1.skipped).toBe(false);
    await modelService.save(result1.model);

    // Try to record the same issue number again
    const duplicateOutcome = createOutcome(601, {
      actual_lines_changed: 9999, // Different data, same issue number
    });
    const result2 = await recorder.recordOutcome(duplicateOutcome);
    expect(result2.skipped).toBe(true);

    // Model should be unchanged — total_observations still 1
    expect(result2.model.total_observations).toBe(1);
    expect(result2.model.prediction_accuracy?.total_predictions).toBe(1);
  });
});
