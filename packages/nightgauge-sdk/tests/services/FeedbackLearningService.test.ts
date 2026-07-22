import { describe, it, expect, vi, beforeEach } from "vitest";
import { FeedbackLearningService } from "../../src/services/FeedbackLearningService.js";
import { ComplexityModelService } from "../../src/services/ComplexityModelService.js";
import type { ComplexityModel } from "../../src/context/schemas/complexity-model.js";
import type { PipelineFeedbackSignal } from "../../src/context/schemas/feedback.js";

// Minimal valid model for test fixtures
function makeModel(overrides: Partial<ComplexityModel> = {}): ComplexityModel {
  return {
    schema_version: "1.0",
    last_updated: "2026-02-26",
    total_observations: 5,
    decay: { enabled: true, half_life_days: 30 },
    model_tracking: {
      current_default: "claude-sonnet-4-6",
      observations_by_model: { "claude-sonnet-4-6": 5 },
    },
    patterns: {
      high_complexity: [
        {
          match: "refactor|redesign",
          modifier: 1.5,
          confidence: 0.45,
          rationale: "Refactors are complex",
          observations: 2,
        },
      ],
      medium_complexity: [
        {
          match: "config|setting",
          modifier: 0,
          confidence: 0.5,
          rationale: "Config changes are moderate",
          observations: 1,
        },
      ],
      low_complexity: [
        {
          match: "typo|spelling",
          modifier: -1,
          confidence: 0.7,
          rationale: "Typo fixes are trivial",
          observations: 1,
        },
      ],
    },
    size_calibration: {
      XS: { expected_lines: 50, actual_average_lines: 50, sample_count: 0 },
      S: { expected_lines: 150, actual_average_lines: 150, sample_count: 0 },
      M: { expected_lines: 500, actual_average_lines: 500, sample_count: 0 },
      L: { expected_lines: 1200, actual_average_lines: 1200, sample_count: 0 },
      XL: { expected_lines: 2500, actual_average_lines: 2500, sample_count: 0 },
    },
    type_adjustments: {
      feature: { modifier: -1.45, observations: 5 },
    },
    priority_adjustments: {
      medium: { modifier: 0, observations: 0 },
    },
    lines_changed_thresholds: {
      XS: 100,
      S: 325,
      M: 850,
      L: 1850,
      XL: 2500,
    },
    learnings: [],
    prediction_accuracy: {
      total_predictions: 3,
      correct_predictions: 2,
      by_type: { feature: { total: 3, correct: 2 } },
      by_size: { S: { total: 3, correct: 2 } },
      recent_outcomes: [],
    },
    ...overrides,
  };
}

const testSignal: PipelineFeedbackSignal = {
  signal_type: "COMPLEXITY_UNDERESTIMATED",
  emitted_by_stage: "feature-dev",
  backtrack_target_stage: "feature-planning",
  rationale: "Implementation required architectural changes beyond the plan",
  evidence: ["Had to refactor interface used by 4 callers"],
  severity: "warning",
  timestamp: "2026-02-26T10:00:00Z",
};

describe("FeedbackLearningService", () => {
  let modelService: ComplexityModelService;
  let learningService: FeedbackLearningService;

  beforeEach(() => {
    modelService = {
      load: vi.fn(),
      save: vi.fn().mockResolvedValue(undefined),
      isOutcomeRecorded: vi.fn().mockReturnValue(false),
      findMatchingPatterns: vi.fn().mockReturnValue([]),
    } as unknown as ComplexityModelService;

    learningService = new FeedbackLearningService(modelService);
  });

  describe("normal update", () => {
    it("decrements matched pattern confidence, records recent_outcome, increments total_predictions, and saves", async () => {
      const model = makeModel();
      vi.mocked(modelService.load).mockResolvedValue(model);
      vi.mocked(modelService.findMatchingPatterns).mockReturnValue([
        {
          pattern: model.patterns.high_complexity[0],
          category: "high_complexity",
          matched_text: "refactor",
        },
      ]);

      const result = await learningService.recordUnderestimation(
        42,
        "S",
        "feature",
        "Refactor the auth service",
        "Needs deep refactor",
        testSignal
      );

      expect(result.skipped).toBe(false);
      expect(result.patternsAdjusted).toBe(1);
      expect(modelService.save).toHaveBeenCalledOnce();

      const savedModel = vi.mocked(modelService.save).mock.calls[0][0];

      // Confidence decremented: 0.45 - 0.05 = 0.40
      expect(savedModel.patterns.high_complexity[0].confidence).toBe(0.4);

      // recent_outcomes entry added
      expect(savedModel.prediction_accuracy?.recent_outcomes).toHaveLength(1);
      const entry = savedModel.prediction_accuracy!.recent_outcomes[0];
      expect(entry.issue_number).toBe(42);
      expect(entry.predicted_size).toBe("S");
      expect(entry.actual_size_bucket).toBe("UNDERESTIMATED");
      expect(entry.was_correct).toBe(false);

      // total_predictions incremented, correct_predictions unchanged
      expect(savedModel.prediction_accuracy?.total_predictions).toBe(4);
      expect(savedModel.prediction_accuracy?.correct_predictions).toBe(2);
    });
  });

  describe("idempotency", () => {
    it("returns {skipped: true} and does NOT call save on duplicate issue", async () => {
      const model = makeModel();
      vi.mocked(modelService.load).mockResolvedValue(model);
      vi.mocked(modelService.isOutcomeRecorded).mockReturnValue(true);

      const result = await learningService.recordUnderestimation(
        42,
        "S",
        "feature",
        "Refactor the auth service",
        "Needs deep refactor",
        testSignal
      );

      expect(result.skipped).toBe(true);
      expect(result.patternsAdjusted).toBe(0);
      expect(modelService.save).not.toHaveBeenCalled();
    });
  });

  describe("pattern confidence decrement", () => {
    it("decrements confidence by exactly 0.05 on a matched pattern", async () => {
      const model = makeModel();
      vi.mocked(modelService.load).mockResolvedValue(model);
      // Pattern with confidence 0.45 that matches
      vi.mocked(modelService.findMatchingPatterns).mockReturnValue([
        {
          pattern: model.patterns.high_complexity[0], // confidence: 0.45
          category: "high_complexity",
          matched_text: "refactor",
        },
      ]);

      await learningService.recordUnderestimation(
        99,
        "M",
        "refactor",
        "Refactor the pipeline",
        "",
        testSignal
      );

      const savedModel = vi.mocked(modelService.save).mock.calls[0][0];
      // 0.45 - 0.05 = 0.40
      expect(savedModel.patterns.high_complexity[0].confidence).toBeCloseTo(0.4, 10);
    });

    it("clamps confidence to 0.0 when it would go negative", async () => {
      const modelWithLowConfidence = makeModel();
      modelWithLowConfidence.patterns.high_complexity[0].confidence = 0.02;

      vi.mocked(modelService.load).mockResolvedValue(modelWithLowConfidence);
      vi.mocked(modelService.findMatchingPatterns).mockReturnValue([
        {
          pattern: modelWithLowConfidence.patterns.high_complexity[0],
          category: "high_complexity",
          matched_text: "refactor",
        },
      ]);

      await learningService.recordUnderestimation(
        100,
        "S",
        "feature",
        "Refactor something",
        "",
        testSignal
      );

      const savedModel = vi.mocked(modelService.save).mock.calls[0][0];
      expect(savedModel.patterns.high_complexity[0].confidence).toBe(0);
    });
  });

  describe("atomic write", () => {
    it("calls modelService.save exactly once on a successful (non-skipped) call", async () => {
      const model = makeModel();
      vi.mocked(modelService.load).mockResolvedValue(model);

      await learningService.recordUnderestimation(
        55,
        "S",
        "feature",
        "Add feature",
        "",
        testSignal
      );

      expect(modelService.save).toHaveBeenCalledOnce();
      expect(modelService.save).toHaveBeenCalledWith(
        expect.objectContaining({ schema_version: "1.0" })
      );
    });
  });

  describe("pre-computed matchedPatterns", () => {
    it("uses provided matchedPatterns without calling findMatchingPatterns", async () => {
      const model = makeModel();
      vi.mocked(modelService.load).mockResolvedValue(model);

      await learningService.recordUnderestimation(
        77,
        "S",
        "feature",
        "Some title",
        "Some description",
        testSignal,
        ["refactor|redesign"] // pre-computed
      );

      expect(modelService.findMatchingPatterns).not.toHaveBeenCalled();

      const savedModel = vi.mocked(modelService.save).mock.calls[0][0];
      // The high_complexity pattern has match 'refactor|redesign' — confidence should be decremented
      expect(savedModel.patterns.high_complexity[0].confidence).toBeCloseTo(0.4, 10);
    });
  });
});
