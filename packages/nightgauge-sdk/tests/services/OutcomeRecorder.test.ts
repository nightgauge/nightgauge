import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as os from "node:os";
import * as yaml from "js-yaml";
import { OutcomeRecorder } from "../../src/services/OutcomeRecorder.js";
import { ComplexityModelService } from "../../src/services/ComplexityModelService.js";
import type {
  ComplexityModel,
  ExecutionOutcome,
} from "../../src/context/schemas/complexity-model.js";
import type { SurvivalRecord, SurvivalVerdict } from "../../src/context/schemas/survival.js";
import type { SizeLabel } from "../../src/services/SuggestionEngine.js";

describe("OutcomeRecorder", () => {
  let tempDir: string;
  let modelPath: string;
  let modelService: ComplexityModelService;
  let recorder: OutcomeRecorder;

  const validModel: ComplexityModel = {
    schema_version: "1.0",
    last_updated: "2026-02-05",
    total_observations: 45,
    decay: { enabled: true, half_life_days: 30 },
    model_tracking: {
      current_default: "claude-opus-4-6",
      observations_by_model: { "claude-opus-4-6": 45 },
    },
    patterns: {
      high_complexity: [
        {
          match: "batch|multiple",
          modifier: 1.5,
          confidence: 0.85,
          rationale: "Batch operations require state management",
          observations: 3,
        },
      ],
      medium_complexity: [
        {
          match: "pipeline",
          modifier: 0.2,
          confidence: 0.82,
          rationale: "Pipeline changes",
          observations: 24,
        },
      ],
      low_complexity: [
        {
          match: "typo|spelling",
          modifier: -2.0,
          confidence: 0.95,
          rationale: "Text fixes are trivial",
          observations: 2,
        },
      ],
    },
    size_calibration: {
      XS: { expected_lines: 50, actual_average_lines: 30, sample_count: 0 },
      S: { expected_lines: 150, actual_average_lines: 120, sample_count: 10 },
      M: { expected_lines: 500, actual_average_lines: 580, sample_count: 22 },
      L: { expected_lines: 1200, actual_average_lines: 1400, sample_count: 11 },
      XL: { expected_lines: 2500, actual_average_lines: 2800, sample_count: 1 },
    },
    type_adjustments: {
      feature: { modifier: 0.0, observations: 25 },
      bug: {
        modifier: -0.2,
        observations: 18,
        rationale: "Bug fixes tend to be smaller",
      },
    },
    priority_adjustments: {
      high: {
        modifier: 0.1,
        observations: 28,
        rationale: "High priority issues are often more complex",
      },
      medium: { modifier: 0.0, observations: 15 },
    },
    lines_changed_thresholds: { XS: 50, S: 200, M: 800, L: 2000, XL: 999999 },
    learnings: ["M is the most common size"],
  };

  function createOutcome(overrides: Partial<ExecutionOutcome> = {}): ExecutionOutcome {
    return {
      issue_number: 42,
      issue_type: "feature",
      pr_number: 100,
      predicted_size: "M",
      actual_lines_changed: 450,
      stages_run: ["issue-pickup", "feature-planning", "feature-dev", "pr-create", "pr-merge"],
      stages_failed: [],
      model_used: "claude-opus-4-6",
      completed_at: new Date().toISOString(),
      outcome: "success",
      ...overrides,
    };
  }

  /** Build a terminal survival record with a unique merge_commit_sha (#4152/#4153) */
  function makeSurvivalRecord(n: number, verdict: SurvivalVerdict): SurvivalRecord {
    return {
      kind: "survival",
      merge_commit_sha: `sha-${n}`,
      issue_number: 4150 + n,
      pr_number: 4200 + n,
      repo: "nightgauge/nightgauge",
      base_ref: "main",
      merged_at: "2026-06-01T12:00:00Z",
      verdict,
    };
  }

  /** Write the model to a temp YAML file so ComplexityModelService can load it */
  async function writeModel(model: ComplexityModel): Promise<void> {
    await fs.writeFile(modelPath, yaml.dump(model), "utf-8");
  }

  beforeEach(async () => {
    tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "outcome-recorder-test-"));
    modelPath = path.join(tempDir, "complexity-model.yaml");
    modelService = new ComplexityModelService(modelPath);
    recorder = new OutcomeRecorder(modelService);
  });

  afterEach(async () => {
    await fs.rm(tempDir, { recursive: true, force: true });
  });

  // ── 1. Records successful outcome ────────────────────────────────────────
  describe("recordOutcome — full flow", () => {
    it("should record a successful outcome and return updated model with skipped=false", async () => {
      // Arrange
      await writeModel(validModel);
      const outcome = createOutcome();

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert
      expect(result.skipped).toBe(false);
      expect(result.model.total_observations).toBe(validModel.total_observations + 1);
      expect(result.model.prediction_accuracy).toBeDefined();
      expect(result.model.prediction_accuracy!.total_predictions).toBe(1);
      expect(result.model.prediction_accuracy!.recent_outcomes).toHaveLength(1);
      expect(result.model.prediction_accuracy!.recent_outcomes[0].issue_number).toBe(42);
    });
  });

  // ── 2. Idempotency check ─────────────────────────────────────────────────
  describe("idempotency", () => {
    it("should skip recording when the same issue_number is already in recent_outcomes", async () => {
      // Arrange — model already has issue 42 recorded
      const modelWithExisting: ComplexityModel = {
        ...validModel,
        prediction_accuracy: {
          total_predictions: 1,
          correct_predictions: 1,
          by_type: { feature: { total: 1, correct: 1 } },
          by_size: { M: { total: 1, correct: 1 } },
          recent_outcomes: [
            {
              issue_number: 42,
              predicted_size: "M",
              actual_size_bucket: "M",
              was_correct: true,
              recorded_at: "2026-02-05T12:00:00.000Z",
            },
          ],
        },
      };
      await writeModel(modelWithExisting);
      const outcome = createOutcome({ issue_number: 42 });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert
      expect(result.skipped).toBe(true);
      expect(result.model.prediction_accuracy!.total_predictions).toBe(1); // unchanged
    });

    it("should allow recording a different issue_number", async () => {
      // Arrange — model has issue 42 but we record issue 99
      const modelWithExisting: ComplexityModel = {
        ...validModel,
        prediction_accuracy: {
          total_predictions: 1,
          correct_predictions: 1,
          by_type: {},
          by_size: {},
          recent_outcomes: [
            {
              issue_number: 42,
              predicted_size: "M",
              actual_size_bucket: "M",
              was_correct: true,
              recorded_at: "2026-02-05T12:00:00.000Z",
            },
          ],
        },
      };
      await writeModel(modelWithExisting);
      const outcome = createOutcome({ issue_number: 99 });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert
      expect(result.skipped).toBe(false);
      expect(result.model.prediction_accuracy!.total_predictions).toBe(2);
    });
  });

  // ── 3. Prediction accuracy — correct ──────────────────────────────────────
  describe("isPredictionCorrect", () => {
    it("should return true when predicted and actual sizes are identical", () => {
      // Arrange / Act / Assert
      expect(recorder.isPredictionCorrect("M", "M")).toBe(true);
      expect(recorder.isPredictionCorrect("XS", "XS")).toBe(true);
      expect(recorder.isPredictionCorrect("XL", "XL")).toBe(true);
    });

    // ── 4. Prediction accuracy — adjacent correct ──────────────────────────
    it("should return true for adjacent sizes (within 1 step)", () => {
      expect(recorder.isPredictionCorrect("S", "M")).toBe(true);
      expect(recorder.isPredictionCorrect("M", "S")).toBe(true);
      expect(recorder.isPredictionCorrect("XS", "S")).toBe(true);
      expect(recorder.isPredictionCorrect("L", "XL")).toBe(true);
      expect(recorder.isPredictionCorrect("XL", "L")).toBe(true);
    });

    // ── 5. Prediction accuracy — incorrect ─────────────────────────────────
    it("should return false when sizes are more than 1 step apart", () => {
      expect(recorder.isPredictionCorrect("XS", "M")).toBe(false);
      expect(recorder.isPredictionCorrect("XS", "L")).toBe(false);
      expect(recorder.isPredictionCorrect("XS", "XL")).toBe(false);
      expect(recorder.isPredictionCorrect("S", "L")).toBe(false);
      expect(recorder.isPredictionCorrect("S", "XL")).toBe(false);
      expect(recorder.isPredictionCorrect("M", "XL")).toBe(false);
      // Also test reverse direction
      expect(recorder.isPredictionCorrect("XL", "XS")).toBe(false);
      expect(recorder.isPredictionCorrect("L", "XS")).toBe(false);
    });
  });

  // ── 3 continued: correct prediction increments correct_predictions ────────
  describe("prediction accuracy tracking", () => {
    it("should increment correct_predictions when prediction is correct", async () => {
      // Arrange — predicted M, actual lines 450 => bucket M (<=800), so correct
      await writeModel(validModel);
      const outcome = createOutcome({
        predicted_size: "M",
        actual_lines_changed: 450,
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert
      expect(result.model.prediction_accuracy!.correct_predictions).toBe(1);
      expect(result.model.prediction_accuracy!.total_predictions).toBe(1);
    });

    it("should not increment correct_predictions when prediction is incorrect", async () => {
      // Arrange — predicted XS, actual lines 1500 => bucket L (>800 <=2000)
      // Distance: XS(0) to L(3) = 3, so incorrect
      await writeModel(validModel);
      const outcome = createOutcome({
        predicted_size: "XS",
        actual_lines_changed: 1500,
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert
      expect(result.model.prediction_accuracy!.correct_predictions).toBe(0);
      expect(result.model.prediction_accuracy!.total_predictions).toBe(1);
    });
  });

  // ── 6. Accuracy by type tracking ─────────────────────────────────────────
  describe("accuracy by_type tracking", () => {
    it("should track feature type predictions separately", async () => {
      // Arrange
      await writeModel(validModel);
      const outcome = createOutcome({
        issue_type: "feature",
        predicted_size: "M",
        actual_lines_changed: 450,
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert
      const featureEntry = result.model.prediction_accuracy!.by_type["feature"];
      expect(featureEntry).toBeDefined();
      expect(featureEntry.total).toBe(1);
      expect(featureEntry.correct).toBe(1); // M predicted, M actual => correct
    });

    it("should track bug type predictions separately", async () => {
      // Arrange
      await writeModel(validModel);
      const outcome = createOutcome({
        issue_type: "bug",
        predicted_size: "S",
        actual_lines_changed: 100,
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert
      const bugEntry = result.model.prediction_accuracy!.by_type["bug"];
      expect(bugEntry).toBeDefined();
      expect(bugEntry.total).toBe(1);
      expect(bugEntry.correct).toBe(1); // S predicted, 100 lines => bucket S (<=200), correct
    });

    it("should accumulate separate type counters across multiple recordings", async () => {
      // Arrange — pre-seed with a feature prediction
      const modelWithAccuracy: ComplexityModel = {
        ...validModel,
        prediction_accuracy: {
          total_predictions: 1,
          correct_predictions: 1,
          by_type: { feature: { total: 1, correct: 1 } },
          by_size: { M: { total: 1, correct: 1 } },
          recent_outcomes: [
            {
              issue_number: 10,
              predicted_size: "M",
              actual_size_bucket: "M",
              was_correct: true,
              recorded_at: "2026-02-05T12:00:00.000Z",
            },
          ],
        },
      };
      await writeModel(modelWithAccuracy);
      const outcome = createOutcome({
        issue_number: 20,
        issue_type: "bug",
        predicted_size: "L",
        actual_lines_changed: 1500,
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert
      expect(result.model.prediction_accuracy!.by_type["feature"]).toEqual({
        total: 1,
        correct: 1,
      });
      expect(result.model.prediction_accuracy!.by_type["bug"]).toEqual({
        total: 1,
        correct: 1,
      }); // L predicted, 1500 => L bucket, correct
    });
  });

  // ── 7. Accuracy by size tracking ─────────────────────────────────────────
  describe("accuracy by_size tracking", () => {
    it("should track each predicted size bucket separately", async () => {
      // Arrange
      await writeModel(validModel);
      const outcome = createOutcome({
        predicted_size: "S",
        actual_lines_changed: 100,
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert
      const sEntry = result.model.prediction_accuracy!.by_size["S"];
      expect(sEntry).toBeDefined();
      expect(sEntry.total).toBe(1);
      expect(sEntry.correct).toBe(1); // S predicted, 100 lines => S bucket (<=200), correct
    });

    it("should increment only the predicted size bucket, not the actual bucket", async () => {
      // Arrange — predicted S, actual lines 500 => bucket M
      // S(1) to M(2) distance = 1 => adjacent correct
      await writeModel(validModel);
      const outcome = createOutcome({
        predicted_size: "S",
        actual_lines_changed: 500,
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert
      expect(result.model.prediction_accuracy!.by_size["S"]).toEqual({
        total: 1,
        correct: 1,
      });
      expect(result.model.prediction_accuracy!.by_size["M"]).toBeUndefined(); // not tracked under M
    });
  });

  // ── 8. Type adjustment — below threshold ──────────────────────────────────
  describe("adjustTypeModifiers — below threshold", () => {
    it("should not adjust type modifier when observations < 5", async () => {
      // Arrange — seed 3 observations for 'docs' type (below MIN_OBSERVATIONS_FOR_ADJUSTMENT=5)
      const modelWithLowObs: ComplexityModel = {
        ...validModel,
        type_adjustments: {
          ...validModel.type_adjustments,
          docs: { modifier: 0.5, observations: 10 },
        },
        prediction_accuracy: {
          total_predictions: 3,
          correct_predictions: 0,
          by_type: { docs: { total: 3, correct: 0 } }, // 0% accuracy but only 3 obs
          by_size: {},
          recent_outcomes: [
            {
              issue_number: 1,
              predicted_size: "M",
              actual_size_bucket: "XL",
              was_correct: false,
              recorded_at: "2026-02-05T12:00:00.000Z",
            },
            {
              issue_number: 2,
              predicted_size: "M",
              actual_size_bucket: "XL",
              was_correct: false,
              recorded_at: "2026-02-05T12:01:00.000Z",
            },
            {
              issue_number: 3,
              predicted_size: "M",
              actual_size_bucket: "XL",
              was_correct: false,
              recorded_at: "2026-02-05T12:02:00.000Z",
            },
          ],
        },
      };
      await writeModel(modelWithLowObs);
      const outcome = createOutcome({
        issue_number: 50,
        issue_type: "docs",
        predicted_size: "M",
        actual_lines_changed: 3000,
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert — 4 total obs for docs type, still below 5, modifier unchanged
      // (the by_type total will now be 4 after this outcome is recorded,
      // but adjustTypeModifiers is called after updateAccuracy, so it sees 4 < 5)
      expect(result.model.type_adjustments["docs"].modifier).toBe(0.5); // unchanged
    });
  });

  // ── 9. Type adjustment — above threshold with directional correction ──────
  describe("adjustTypeModifiers — directional error correction", () => {
    it("should shift modifier negative when over-predicting (M→XS)", async () => {
      // Arrange — 5+ observations for 'feature' with over-prediction pattern
      const modelWithOverPrediction: ComplexityModel = {
        ...validModel,
        type_adjustments: {
          ...validModel.type_adjustments,
          feature: { modifier: 0.0, observations: 25 },
        },
        prediction_accuracy: {
          total_predictions: 5,
          correct_predictions: 0,
          by_type: { feature: { total: 5, correct: 0 } },
          by_size: {},
          recent_outcomes: [
            {
              issue_number: 1,
              predicted_size: "M",
              actual_size_bucket: "XS",
              was_correct: false,
              recorded_at: "2026-02-01T00:00:00.000Z",
            },
            {
              issue_number: 2,
              predicted_size: "M",
              actual_size_bucket: "XS",
              was_correct: false,
              recorded_at: "2026-02-02T00:00:00.000Z",
            },
            {
              issue_number: 3,
              predicted_size: "M",
              actual_size_bucket: "XS",
              was_correct: false,
              recorded_at: "2026-02-03T00:00:00.000Z",
            },
            {
              issue_number: 4,
              predicted_size: "M",
              actual_size_bucket: "XS",
              was_correct: false,
              recorded_at: "2026-02-04T00:00:00.000Z",
            },
            {
              issue_number: 5,
              predicted_size: "M",
              actual_size_bucket: "XS",
              was_correct: false,
              recorded_at: "2026-02-04T12:00:00.000Z",
            },
          ],
        },
      };
      await writeModel(modelWithOverPrediction);
      // predicted M, actual 30 lines => XS bucket. M(2)->XS(0) = error 2
      // shift = -2 * 0.05 = -0.10
      const outcome = createOutcome({
        issue_number: 60,
        issue_type: "feature",
        predicted_size: "M",
        actual_lines_changed: 30, // bucket XS, M->XS = 2 steps, incorrect
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert — modifier shifted: 0.0 + (-2 * 0.05) = -0.10
      expect(result.model.type_adjustments["feature"].modifier).toBeCloseTo(-0.1, 2);
      expect(result.model.type_adjustments["feature"].observations).toBe(26);
    });

    it("should shift modifier positive when under-predicting (XS→L)", async () => {
      // Arrange — consistent under-prediction
      const modelWithUnderPrediction: ComplexityModel = {
        ...validModel,
        type_adjustments: {
          ...validModel.type_adjustments,
          feature: { modifier: -1.0, observations: 25 },
        },
        prediction_accuracy: {
          total_predictions: 5,
          correct_predictions: 0,
          by_type: { feature: { total: 5, correct: 0 } },
          by_size: {},
          recent_outcomes: Array.from({ length: 5 }, (_, i) => ({
            issue_number: i + 1,
            predicted_size: "XS",
            actual_size_bucket: "L",
            was_correct: false,
            recorded_at: `2026-02-0${i + 1}T00:00:00.000Z`,
          })),
        },
      };
      await writeModel(modelWithUnderPrediction);
      // predicted XS, actual 1500 lines => L bucket. XS(0)->L(3) = error -3
      // shift = -(-3) * 0.05 = +0.15
      const outcome = createOutcome({
        issue_number: 60,
        issue_type: "feature",
        predicted_size: "XS",
        actual_lines_changed: 1500, // bucket L, XS->L = 3 steps, incorrect
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert — modifier shifted: -1.0 + (3 * 0.05) = -0.85
      expect(result.model.type_adjustments["feature"].modifier).toBeCloseTo(-0.85, 2);
      expect(result.model.type_adjustments["feature"].observations).toBe(26);
    });

    it("should only increment observation count on correct prediction", async () => {
      // Arrange — 5 observations for 'feature', 4 correct => 80% accuracy
      const modelWithHighAccuracy: ComplexityModel = {
        ...validModel,
        type_adjustments: {
          ...validModel.type_adjustments,
          feature: { modifier: 0.5, observations: 25 },
        },
        prediction_accuracy: {
          total_predictions: 5,
          correct_predictions: 4,
          by_type: { feature: { total: 5, correct: 4 } }, // 80%
          by_size: {},
          recent_outcomes: [
            {
              issue_number: 1,
              predicted_size: "M",
              actual_size_bucket: "M",
              was_correct: true,
              recorded_at: "2026-02-01T00:00:00.000Z",
            },
            {
              issue_number: 2,
              predicted_size: "M",
              actual_size_bucket: "M",
              was_correct: true,
              recorded_at: "2026-02-02T00:00:00.000Z",
            },
            {
              issue_number: 3,
              predicted_size: "M",
              actual_size_bucket: "M",
              was_correct: true,
              recorded_at: "2026-02-03T00:00:00.000Z",
            },
            {
              issue_number: 4,
              predicted_size: "M",
              actual_size_bucket: "M",
              was_correct: true,
              recorded_at: "2026-02-04T00:00:00.000Z",
            },
            {
              issue_number: 5,
              predicted_size: "XS",
              actual_size_bucket: "L",
              was_correct: false,
              recorded_at: "2026-02-04T12:00:00.000Z",
            },
          ],
        },
      };
      await writeModel(modelWithHighAccuracy);
      const outcome = createOutcome({
        issue_number: 60,
        issue_type: "feature",
        predicted_size: "M",
        actual_lines_changed: 450, // bucket M, correct
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert — modifier unchanged, only observation count incremented
      expect(result.model.type_adjustments["feature"].modifier).toBe(0.5);
      expect(result.model.type_adjustments["feature"].observations).toBe(26);
    });

    it("should clamp modifier to [-3, 3] range", async () => {
      // Arrange — modifier already at -2.9, another over-prediction pushes it further
      const modelNearLimit: ComplexityModel = {
        ...validModel,
        type_adjustments: {
          ...validModel.type_adjustments,
          feature: { modifier: -2.9, observations: 50 },
        },
        prediction_accuracy: {
          total_predictions: 10,
          correct_predictions: 0,
          by_type: { feature: { total: 10, correct: 0 } },
          by_size: {},
          recent_outcomes: Array.from({ length: 10 }, (_, i) => ({
            issue_number: i + 1,
            predicted_size: "M",
            actual_size_bucket: "XS",
            was_correct: false,
            recorded_at: `2026-02-0${(i % 9) + 1}T00:00:00.000Z`,
          })),
        },
      };
      await writeModel(modelNearLimit);
      // M→XS: error 2, shift = -0.1, new = -2.9 + -0.1 = -3.0 (at limit)
      const outcome = createOutcome({
        issue_number: 60,
        issue_type: "feature",
        predicted_size: "M",
        actual_lines_changed: 30,
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert — clamped to -3.0
      expect(result.model.type_adjustments["feature"].modifier).toBe(-3.0);
    });
  });

  // ── 10. Pattern confidence — correct boost ────────────────────────────────
  describe("adjustPatternConfidence", () => {
    it("should boost matching pattern confidence by +0.02 on correct prediction", async () => {
      // Arrange — outcome references 'pipeline' pattern, prediction correct
      await writeModel(validModel);
      const outcome = createOutcome({
        predicted_size: "M",
        actual_lines_changed: 450, // bucket M, correct
        patterns_matched: ["pipeline"],
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert
      const pipelinePattern = result.model.patterns.medium_complexity[0];
      expect(pipelinePattern.confidence).toBeCloseTo(0.82 + 0.02, 10);
    });

    // ── 11. Pattern confidence — incorrect penalty ──────────────────────────
    it("should penalize matching pattern confidence by -0.05 on incorrect prediction", async () => {
      // Arrange — outcome references 'batch|multiple' pattern, prediction incorrect
      await writeModel(validModel);
      const outcome = createOutcome({
        predicted_size: "XS",
        actual_lines_changed: 1500, // bucket L, XS->L = 3 steps, incorrect
        patterns_matched: ["batch|multiple"],
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert
      const batchPattern = result.model.patterns.high_complexity[0];
      expect(batchPattern.confidence).toBeCloseTo(0.85 - 0.05, 10);
    });

    it("should not modify patterns that were not matched", async () => {
      // Arrange — only 'pipeline' matched, others should stay the same
      await writeModel(validModel);
      const outcome = createOutcome({
        predicted_size: "M",
        actual_lines_changed: 450,
        patterns_matched: ["pipeline"],
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert — high_complexity and low_complexity patterns unchanged
      expect(result.model.patterns.high_complexity[0].confidence).toBe(0.85);
      expect(result.model.patterns.low_complexity[0].confidence).toBe(0.95);
    });

    it("should not adjust any pattern confidence when patterns_matched is empty", async () => {
      // Arrange
      await writeModel(validModel);
      const outcome = createOutcome({
        predicted_size: "M",
        actual_lines_changed: 450,
        patterns_matched: [],
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert — all patterns unchanged
      expect(result.model.patterns.high_complexity[0].confidence).toBe(0.85);
      expect(result.model.patterns.medium_complexity[0].confidence).toBe(0.82);
      expect(result.model.patterns.low_complexity[0].confidence).toBe(0.95);
    });

    it("should not adjust any pattern confidence when patterns_matched is undefined", async () => {
      // Arrange
      await writeModel(validModel);
      const outcome = createOutcome({
        predicted_size: "M",
        actual_lines_changed: 450,
        // patterns_matched intentionally omitted
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert — all patterns unchanged
      expect(result.model.patterns.high_complexity[0].confidence).toBe(0.85);
      expect(result.model.patterns.medium_complexity[0].confidence).toBe(0.82);
      expect(result.model.patterns.low_complexity[0].confidence).toBe(0.95);
    });

    // ── 12. Pattern confidence — clamped ────────────────────────────────────
    it("should clamp confidence to a maximum of 1.0", async () => {
      // Arrange — pattern already at 0.99 confidence
      const modelNearMax: ComplexityModel = {
        ...validModel,
        patterns: {
          ...validModel.patterns,
          medium_complexity: [
            {
              match: "pipeline",
              modifier: 0.2,
              confidence: 0.99,
              rationale: "Pipeline changes",
              observations: 24,
            },
          ],
        },
      };
      await writeModel(modelNearMax);
      const outcome = createOutcome({
        predicted_size: "M",
        actual_lines_changed: 450, // correct
        patterns_matched: ["pipeline"],
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert — 0.99 + 0.02 = 1.01, but clamped to 1.0
      expect(result.model.patterns.medium_complexity[0].confidence).toBe(1.0);
    });

    it("should clamp confidence to a minimum of 0.0", async () => {
      // Arrange — pattern already at 0.03 confidence
      const modelNearMin: ComplexityModel = {
        ...validModel,
        patterns: {
          ...validModel.patterns,
          high_complexity: [
            {
              match: "batch|multiple",
              modifier: 1.5,
              confidence: 0.03,
              rationale: "Batch ops",
              observations: 3,
            },
          ],
        },
      };
      await writeModel(modelNearMin);
      const outcome = createOutcome({
        predicted_size: "XS",
        actual_lines_changed: 1500, // bucket L, incorrect
        patterns_matched: ["batch|multiple"],
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert — 0.03 - 0.05 = -0.02, but clamped to 0.0
      expect(result.model.patterns.high_complexity[0].confidence).toBe(0.0);
    });
  });

  // ── 13. Size bucket calculation ───────────────────────────────────────────
  describe("getActualSizeBucket", () => {
    it("should map lines to XS bucket (lines <= 50)", () => {
      expect(recorder.getActualSizeBucket(0, validModel)).toBe("XS");
      expect(recorder.getActualSizeBucket(1, validModel)).toBe("XS");
      expect(recorder.getActualSizeBucket(50, validModel)).toBe("XS");
    });

    it("should map lines to S bucket (50 < lines <= 200)", () => {
      expect(recorder.getActualSizeBucket(51, validModel)).toBe("S");
      expect(recorder.getActualSizeBucket(100, validModel)).toBe("S");
      expect(recorder.getActualSizeBucket(200, validModel)).toBe("S");
    });

    it("should map lines to M bucket (200 < lines <= 800)", () => {
      expect(recorder.getActualSizeBucket(201, validModel)).toBe("M");
      expect(recorder.getActualSizeBucket(450, validModel)).toBe("M");
      expect(recorder.getActualSizeBucket(800, validModel)).toBe("M");
    });

    it("should map lines to L bucket (800 < lines <= 2000)", () => {
      expect(recorder.getActualSizeBucket(801, validModel)).toBe("L");
      expect(recorder.getActualSizeBucket(1500, validModel)).toBe("L");
      expect(recorder.getActualSizeBucket(2000, validModel)).toBe("L");
    });

    it("should map lines to XL bucket (lines > 2000)", () => {
      expect(recorder.getActualSizeBucket(2001, validModel)).toBe("XL");
      expect(recorder.getActualSizeBucket(5000, validModel)).toBe("XL");
      expect(recorder.getActualSizeBucket(999999, validModel)).toBe("XL");
    });

    it("should use threshold boundaries exactly (boundary values)", () => {
      // Verify that threshold values themselves fall into their bucket (upper-bound inclusive)
      const thresholds = validModel.lines_changed_thresholds;
      expect(recorder.getActualSizeBucket(thresholds.XS, validModel)).toBe("XS");
      expect(recorder.getActualSizeBucket(thresholds.S, validModel)).toBe("S");
      expect(recorder.getActualSizeBucket(thresholds.M, validModel)).toBe("M");
      expect(recorder.getActualSizeBucket(thresholds.L, validModel)).toBe("L");
      // XL threshold is 999999 — anything at or above it is still XL
      expect(recorder.getActualSizeBucket(thresholds.XL, validModel)).toBe("XL");
    });
  });

  // ── 14. Recent outcomes pruned ────────────────────────────────────────────
  describe("recent outcomes pruning", () => {
    it("should keep only the last 50 recent outcomes", async () => {
      // Arrange — model already has 50 recent outcomes
      const existingOutcomes = Array.from({ length: 50 }, (_, i) => ({
        issue_number: i + 1,
        predicted_size: "M",
        actual_size_bucket: "M",
        was_correct: true,
        recorded_at: `2026-02-0${(i % 9) + 1}T00:00:00.000Z`,
      }));
      const modelWith50: ComplexityModel = {
        ...validModel,
        prediction_accuracy: {
          total_predictions: 50,
          correct_predictions: 50,
          by_type: {},
          by_size: {},
          recent_outcomes: existingOutcomes,
        },
      };
      await writeModel(modelWith50);

      // Act — record one more (issue_number 51)
      const outcome = createOutcome({
        issue_number: 51,
        predicted_size: "M",
        actual_lines_changed: 450,
      });
      const result = await recorder.recordOutcome(outcome);

      // Assert — still 50 outcomes, oldest (issue_number 1) dropped
      expect(result.model.prediction_accuracy!.recent_outcomes).toHaveLength(50);
      const issueNumbers = result.model.prediction_accuracy!.recent_outcomes.map(
        (o) => o.issue_number
      );
      expect(issueNumbers).not.toContain(1); // oldest dropped
      expect(issueNumbers).toContain(51); // newest added
      expect(issueNumbers).toContain(2); // second-oldest still present
    });

    it("should not prune when under 50 outcomes", async () => {
      // Arrange
      await writeModel(validModel);
      const outcome = createOutcome();

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert
      expect(result.model.prediction_accuracy!.recent_outcomes).toHaveLength(1);
    });
  });

  // ── 15. Failure outcome recorded ──────────────────────────────────────────
  describe("failure outcome recording", () => {
    it("should record a failure outcome with stages_failed populated", async () => {
      // Arrange
      await writeModel(validModel);
      const outcome = createOutcome({
        issue_number: 77,
        outcome: "failure",
        stages_run: ["issue-pickup", "feature-planning", "feature-dev"],
        stages_failed: ["feature-dev"],
        actual_lines_changed: 0,
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert
      expect(result.skipped).toBe(false);
      expect(result.model.prediction_accuracy).toBeDefined();
      expect(result.model.prediction_accuracy!.total_predictions).toBe(1);
      // With 0 lines changed and predicted M: bucket XS, M->XS = 2 steps, incorrect
      expect(result.model.prediction_accuracy!.correct_predictions).toBe(0);
      const recent = result.model.prediction_accuracy!.recent_outcomes[0];
      expect(recent.issue_number).toBe(77);
      expect(recent.actual_size_bucket).toBe("XS");
      expect(recent.was_correct).toBe(false);
    });

    it("should record partial failure outcome", async () => {
      // Arrange
      await writeModel(validModel);
      const outcome = createOutcome({
        issue_number: 88,
        outcome: "partial",
        stages_run: ["issue-pickup", "feature-planning", "feature-dev", "pr-create"],
        stages_failed: ["pr-create"],
        actual_lines_changed: 300, // bucket M, predicted M => correct
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert
      expect(result.skipped).toBe(false);
      expect(result.model.prediction_accuracy!.correct_predictions).toBe(1);
    });
  });

  // ── Additional edge cases ────────────────────────────────────────────────
  describe("edge cases", () => {
    it("should initialize prediction_accuracy when model has none", async () => {
      // Arrange — the validModel has no prediction_accuracy field
      await writeModel(validModel);
      expect(validModel.prediction_accuracy).toBeUndefined();

      // Act
      const outcome = createOutcome();
      const result = await recorder.recordOutcome(outcome);

      // Assert — prediction_accuracy should be created from scratch
      expect(result.model.prediction_accuracy).toBeDefined();
      expect(result.model.prediction_accuracy!.total_predictions).toBe(1);
      expect(result.model.prediction_accuracy!.by_type).toHaveProperty("feature");
      expect(result.model.prediction_accuracy!.by_size).toHaveProperty("M");
    });

    it("should handle unknown issue_type in type_adjustments gracefully", async () => {
      // Arrange — type 'docs' has no entry in type_adjustments
      await writeModel(validModel);
      const outcome = createOutcome({ issue_type: "docs" });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert — should not throw; type_adjustments may not have 'docs' but by_type should
      expect(result.model.prediction_accuracy!.by_type["docs"]).toBeDefined();
      expect(result.model.prediction_accuracy!.by_type["docs"].total).toBe(1);
    });

    it("should update model_tracking with the model_used from outcome", async () => {
      // Arrange
      await writeModel(validModel);
      const outcome = createOutcome({ model_used: "claude-opus-4-6" });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert — model_tracking should reflect the new observation
      expect(result.model.model_tracking.observations_by_model["claude-opus-4-6"]).toBe(46);
    });

    it("should update size_calibration under ACTUAL bucket (not predicted)", async () => {
      // Arrange
      await writeModel(validModel);
      // predicted M, but 450 lines => actual bucket M (<=800), so same bucket here
      const outcome = createOutcome({
        predicted_size: "M",
        actual_lines_changed: 450,
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert — recorded under actual bucket M: (580 * 22 + 450) / 23 = 574
      expect(result.model.size_calibration.M.sample_count).toBe(23);
      expect(result.model.size_calibration.M.actual_average_lines).toBeCloseTo(574, 0);
    });

    it("should record under actual XS bucket when predicted M but actual lines are small", async () => {
      // Arrange — this is the key bug scenario: predicted M, actual XS
      await writeModel(validModel);
      // XS has sample_count 0 and actual_average_lines 30
      const outcome = createOutcome({
        predicted_size: "M",
        actual_lines_changed: 40, // bucket XS (<=50)
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert — recorded under actual bucket XS, NOT predicted bucket M
      expect(result.model.size_calibration.XS.sample_count).toBe(1);
      expect(result.model.size_calibration.XS.actual_average_lines).toBe(40);
      // M bucket should be unchanged
      expect(result.model.size_calibration.M.sample_count).toBe(22);
      expect(result.model.size_calibration.M.actual_average_lines).toBe(580);
    });

    it("should handle multiple patterns matched across categories", async () => {
      // Arrange — both 'batch|multiple' and 'pipeline' matched, prediction correct
      await writeModel(validModel);
      const outcome = createOutcome({
        predicted_size: "M",
        actual_lines_changed: 450,
        patterns_matched: ["batch|multiple", "pipeline"],
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert — both patterns boosted
      expect(result.model.patterns.high_complexity[0].confidence).toBeCloseTo(0.85 + 0.02, 10);
      expect(result.model.patterns.medium_complexity[0].confidence).toBeCloseTo(0.82 + 0.02, 10);
    });

    it("should handle multiple patterns matched with incorrect prediction", async () => {
      // Arrange — both 'batch|multiple' and 'pipeline' matched, prediction incorrect
      await writeModel(validModel);
      const outcome = createOutcome({
        predicted_size: "XS",
        actual_lines_changed: 1500, // bucket L, XS->L = 3 steps
        patterns_matched: ["batch|multiple", "pipeline"],
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert — both patterns penalized
      expect(result.model.patterns.high_complexity[0].confidence).toBeCloseTo(0.85 - 0.05, 10);
      expect(result.model.patterns.medium_complexity[0].confidence).toBeCloseTo(0.82 - 0.05, 10);
    });
  });

  // ── Garbage overwrite (Issue #1198) ─────────────────────────────────────
  describe("garbage overwrite (Issue #1198)", () => {
    it("should overwrite a 0-line entry with real line data", async () => {
      // Arrange — model has a garbage entry for issue 77 (0 lines → XS)
      const modelWithGarbage: ComplexityModel = {
        ...validModel,
        total_observations: 46,
        prediction_accuracy: {
          total_predictions: 1,
          correct_predictions: 0,
          by_type: { feature: { total: 1, correct: 0 } },
          by_size: { M: { total: 1, correct: 0 } },
          recent_outcomes: [
            {
              issue_number: 77,
              predicted_size: "M",
              actual_size_bucket: "XS",
              was_correct: false,
              recorded_at: "2026-02-20T00:00:00.000Z",
              actual_lines_changed: 0,
            },
          ],
        },
      };
      await writeModel(modelWithGarbage);

      // Act — record real data for same issue
      const outcome = createOutcome({
        issue_number: 77,
        predicted_size: "M",
        actual_lines_changed: 493,
      });
      const result = await recorder.recordOutcome(outcome);

      // Assert — not skipped, new data recorded
      expect(result.skipped).toBe(false);
      expect(result.model.prediction_accuracy!.recent_outcomes).toHaveLength(1);
      const entry = result.model.prediction_accuracy!.recent_outcomes[0];
      expect(entry.issue_number).toBe(77);
      expect(entry.actual_lines_changed).toBe(493);
      expect(entry.actual_size_bucket).toBe("M");
    });

    it("should still protect non-zero entries with idempotency", async () => {
      // Arrange — model has a real entry for issue 77 (493 lines → M)
      const modelWithReal: ComplexityModel = {
        ...validModel,
        prediction_accuracy: {
          total_predictions: 1,
          correct_predictions: 1,
          by_type: { feature: { total: 1, correct: 1 } },
          by_size: { M: { total: 1, correct: 1 } },
          recent_outcomes: [
            {
              issue_number: 77,
              predicted_size: "M",
              actual_size_bucket: "M",
              was_correct: true,
              recorded_at: "2026-02-20T00:00:00.000Z",
              actual_lines_changed: 493,
            },
          ],
        },
      };
      await writeModel(modelWithReal);

      // Act — try to re-record
      const outcome = createOutcome({
        issue_number: 77,
        predicted_size: "M",
        actual_lines_changed: 500,
      });
      const result = await recorder.recordOutcome(outcome);

      // Assert — skipped
      expect(result.skipped).toBe(true);
      expect(result.model.prediction_accuracy!.total_predictions).toBe(1);
    });

    it("should correct calibration data when overwriting garbage entry", async () => {
      // Arrange — garbage entry recorded under XS bucket with 0 lines
      // XS bucket had sample_count 0 before garbage → now has 1
      const modelWithGarbage: ComplexityModel = {
        ...validModel,
        total_observations: 46,
        size_calibration: {
          ...validModel.size_calibration,
          XS: { expected_lines: 50, actual_average_lines: 0, sample_count: 1 },
        },
        prediction_accuracy: {
          total_predictions: 1,
          correct_predictions: 0,
          by_type: { feature: { total: 1, correct: 0 } },
          by_size: { M: { total: 1, correct: 0 } },
          recent_outcomes: [
            {
              issue_number: 77,
              predicted_size: "M",
              actual_size_bucket: "XS",
              was_correct: false,
              recorded_at: "2026-02-20T00:00:00.000Z",
              actual_lines_changed: 0,
            },
          ],
        },
      };
      await writeModel(modelWithGarbage);

      // Act — overwrite with real data (493 lines → M bucket)
      const outcome = createOutcome({
        issue_number: 77,
        predicted_size: "M",
        actual_lines_changed: 493,
      });
      const result = await recorder.recordOutcome(outcome);

      // Assert — XS calibration reversed (sample_count back to 0)
      expect(result.model.size_calibration.XS.sample_count).toBe(0);
      // M calibration updated with real data
      expect(result.model.size_calibration.M.sample_count).toBe(
        validModel.size_calibration.M.sample_count + 1
      );
    });

    it("should store actual_lines_changed in recent_outcomes", async () => {
      // Arrange
      await writeModel(validModel);
      const outcome = createOutcome({
        issue_number: 99,
        actual_lines_changed: 250,
      });

      // Act
      const result = await recorder.recordOutcome(outcome);

      // Assert
      const entry = result.model.prediction_accuracy!.recent_outcomes[0];
      expect(entry.actual_lines_changed).toBe(250);
    });

    it("should treat entries without actual_lines_changed as non-garbage (backward compat)", async () => {
      // Arrange — old entry without actual_lines_changed field
      const modelWithOldEntry: ComplexityModel = {
        ...validModel,
        prediction_accuracy: {
          total_predictions: 1,
          correct_predictions: 1,
          by_type: { feature: { total: 1, correct: 1 } },
          by_size: { M: { total: 1, correct: 1 } },
          recent_outcomes: [
            {
              issue_number: 77,
              predicted_size: "M",
              actual_size_bucket: "M",
              was_correct: true,
              recorded_at: "2026-02-20T00:00:00.000Z",
              // no actual_lines_changed — old format
            },
          ],
        },
      };
      await writeModel(modelWithOldEntry);

      // Act
      const outcome = createOutcome({
        issue_number: 77,
        actual_lines_changed: 500,
      });
      const result = await recorder.recordOutcome(outcome);

      // Assert — skipped (undefined defaults to -1, not treated as garbage)
      expect(result.skipped).toBe(true);
    });
  });

  // ── End-to-end persistence round-trip ────────────────────────────────────
  describe("persistence round-trip", () => {
    it("should produce a model that can be saved and reloaded", async () => {
      // Arrange
      await writeModel(validModel);
      const outcome = createOutcome({
        predicted_size: "M",
        actual_lines_changed: 450,
        patterns_matched: ["pipeline"],
      });

      // Act — record, save, reload
      const { model: updated } = await recorder.recordOutcome(outcome);
      await modelService.save(updated);
      const reloaded = await modelService.load();

      // Assert — reloaded model preserves prediction accuracy
      expect(reloaded.prediction_accuracy).toBeDefined();
      expect(reloaded.prediction_accuracy!.total_predictions).toBe(1);
      expect(reloaded.prediction_accuracy!.correct_predictions).toBe(1);
      expect(reloaded.prediction_accuracy!.recent_outcomes).toHaveLength(1);
      expect(reloaded.prediction_accuracy!.recent_outcomes[0].issue_number).toBe(42);
    });
  });

  // ── applySurvivalVerdicts — bias-safe calibration (#4152/#4153) ───────────
  describe("applySurvivalVerdicts", () => {
    it("should not apply a penalty for a single reverted record below the observation floor", () => {
      const result = recorder.applySurvivalVerdicts(validModel, [
        makeSurvivalRecord(1, "reverted"),
      ]);

      expect(result.penaltiesApplied).toBe(0);
      expect(result.confidence).toBe(0.5); // unchanged default
      expect(result.model.prediction_accuracy?.survival_calibration?.negative_observations).toBe(1);
    });

    it("should apply a penalty once 5 reverted/broke observations have accrued", () => {
      let model = validModel;
      let result;
      for (let i = 1; i <= 5; i++) {
        result = recorder.applySurvivalVerdicts(model, [makeSurvivalRecord(i, "reverted")]);
        model = result.model;
      }

      expect(result!.penaltiesApplied).toBe(1);
      expect(result!.confidence).toBeCloseTo(0.45, 5); // 0.5 - CONFIDENCE_PENALTY (0.05)
      expect(model.prediction_accuracy?.survival_calibration?.negative_observations).toBe(5);
      expect(model.prediction_accuracy?.survival_calibration?.penalties_applied).toBe(1);
    });

    it("should NOT reward a single survived record below the finalized-survival floor", () => {
      const result = recorder.applySurvivalVerdicts(validModel, [
        makeSurvivalRecord(1, "survived"),
      ]);

      expect(result.rewardsApplied).toBe(0);
      expect(result.confidence).toBe(0.5); // unchanged — never reward unproven/thin survival
    });

    it("should apply the weak reward once 5 finalized survived records have accrued", () => {
      let model = validModel;
      let result;
      for (let i = 1; i <= 5; i++) {
        result = recorder.applySurvivalVerdicts(model, [makeSurvivalRecord(i, "survived")]);
        model = result.model;
      }

      expect(result!.rewardsApplied).toBe(1);
      expect(result!.confidence).toBeCloseTo(0.52, 5); // 0.5 + CONFIDENCE_BOOST (0.02)
      expect(model.prediction_accuracy?.survival_calibration?.positive_observations).toBe(5);
      // Bias-safety: reward magnitude must stay strictly below the penalty magnitude.
      const boost = result!.confidence - 0.5;
      expect(boost).toBeLessThan(0.05);
    });

    it("should never move calibration for pending or unobserved records", () => {
      const pending = makeSurvivalRecord(1, "pending");
      const unobserved = makeSurvivalRecord(2, "unobserved");

      const result = recorder.applySurvivalVerdicts(validModel, [pending, unobserved]);

      expect(result.penaltiesApplied).toBe(0);
      expect(result.rewardsApplied).toBe(0);
      expect(result.confidence).toBe(0.5);
      const sc = result.model.prediction_accuracy?.survival_calibration;
      expect(sc?.negative_observations ?? 0).toBe(0);
      expect(sc?.positive_observations ?? 0).toBe(0);
      // The still-pending record must not be ledgered (it isn't terminal yet).
      expect(sc?.processed_shas).not.toContain(pending.merge_commit_sha);
    });

    it("should be idempotent when the same merge_commit_sha is re-processed", () => {
      const rec = makeSurvivalRecord(1, "reverted");

      const first = recorder.applySurvivalVerdicts(validModel, [rec]);
      expect(first.processed).toBe(1);

      const second = recorder.applySurvivalVerdicts(first.model, [rec]);
      expect(second.processed).toBe(0);
      expect(second.model.prediction_accuracy?.survival_calibration?.negative_observations).toBe(1); // not double-counted
    });
  });
});
