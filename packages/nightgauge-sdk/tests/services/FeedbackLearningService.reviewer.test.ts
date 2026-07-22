import { describe, it, expect, vi, beforeEach } from "vitest";
import { FeedbackLearningService } from "../../src/services/FeedbackLearningService.js";
import { ComplexityModelService } from "../../src/services/ComplexityModelService.js";
import type { ComplexityModel } from "../../src/context/schemas/complexity-model.js";
import type { ReviewerSignal } from "../../src/context/schemas/feedback.js";

// Minimal valid model for test fixtures (matches existing test pattern)
function makeModel(overrides: Partial<ComplexityModel> = {}): ComplexityModel {
  return {
    schema_version: "1.0",
    last_updated: "2026-02-28",
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
      L: {
        expected_lines: 1200,
        actual_average_lines: 1200,
        sample_count: 0,
      },
      XL: {
        expected_lines: 2500,
        actual_average_lines: 2500,
        sample_count: 0,
      },
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

function makeSignal(overrides: Partial<ReviewerSignal> = {}): ReviewerSignal {
  return {
    signal_type: "SCOPE_UNDERESTIMATED",
    source_comment: "This PR is too large, should split it up",
    reviewer_login: "reviewer1",
    review_verdict: "CHANGES_REQUESTED",
    confidence: 0.7,
    matched_keywords: ["too large"],
    ...overrides,
  };
}

describe("FeedbackLearningService — Reviewer Feedback (Issue #1409)", () => {
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

  describe("parseReviewerComments", () => {
    it('parses SCOPE_UNDERESTIMATED from "too large" comment', () => {
      const signals = learningService.parseReviewerComments([
        {
          body: "This PR is too large, should split it up",
          reviewer_login: "alice",
          verdict: "CHANGES_REQUESTED",
        },
      ]);

      expect(signals).toHaveLength(1);
      expect(signals[0].signal_type).toBe("SCOPE_UNDERESTIMATED");
      expect(signals[0].reviewer_login).toBe("alice");
    });

    it('parses APPROACH_MISMATCH from "wrong approach" comment', () => {
      const signals = learningService.parseReviewerComments([
        {
          body: "This is the wrong approach, should have used streams instead",
          reviewer_login: "bob",
          verdict: "CHANGES_REQUESTED",
        },
      ]);

      expect(signals).toHaveLength(1);
      expect(signals[0].signal_type).toBe("APPROACH_MISMATCH");
    });

    it('parses VALIDATION_GAP from "missing tests" comment', () => {
      const signals = learningService.parseReviewerComments([
        {
          body: "There are missing tests for this edge case path",
          reviewer_login: "carol",
          verdict: "COMMENTED",
        },
      ]);

      expect(signals).toHaveLength(1);
      expect(signals[0].signal_type).toBe("VALIDATION_GAP");
    });

    it('parses COMPLEXITY_OVERESTIMATED from "over-engineered" comment', () => {
      const signals = learningService.parseReviewerComments([
        {
          body: "This solution is over-engineered, could be simpler",
          reviewer_login: "dave",
          verdict: "COMMENTED",
        },
      ]);

      // Both "over-engineered" and "could be simpler" match COMPLEXITY_OVERESTIMATED
      expect(signals.length).toBeGreaterThanOrEqual(1);
      expect(signals[0].signal_type).toBe("COMPLEXITY_OVERESTIMATED");
    });

    it('parses ARCHITECTURE_DRIFT from "doesn\'t fit" comment', () => {
      const signals = learningService.parseReviewerComments([
        {
          body: "This doesn't fit the existing pattern we use for services",
          reviewer_login: "eve",
          verdict: "CHANGES_REQUESTED",
        },
      ]);

      expect(signals).toHaveLength(1);
      expect(signals[0].signal_type).toBe("ARCHITECTURE_DRIFT");
    });

    it("emits multiple signals from a single comment", () => {
      const signals = learningService.parseReviewerComments([
        {
          body: "This PR is too large and there are missing tests for the edge cases",
          reviewer_login: "frank",
          verdict: "CHANGES_REQUESTED",
        },
      ]);

      expect(signals).toHaveLength(2);
      const types = signals.map((s) => s.signal_type).sort();
      expect(types).toContain("SCOPE_UNDERESTIMATED");
      expect(types).toContain("VALIDATION_GAP");
    });

    it("skips comments shorter than minCommentLength", () => {
      const signals = learningService.parseReviewerComments(
        [
          {
            body: "lgtm",
            reviewer_login: "grace",
            verdict: "APPROVED",
          },
        ],
        10
      );

      expect(signals).toHaveLength(0);
    });

    it("returns no signals from irrelevant comments", () => {
      const signals = learningService.parseReviewerComments([
        {
          body: "Looks good to me, nice work on the implementation!",
          reviewer_login: "heidi",
          verdict: "APPROVED",
        },
      ]);

      expect(signals).toHaveLength(0);
    });

    it("skips comments with empty body", () => {
      const signals = learningService.parseReviewerComments([
        {
          body: "",
          reviewer_login: "ivan",
          verdict: "APPROVED",
        },
      ]);

      expect(signals).toHaveLength(0);
    });
  });

  describe("processReviewerFeedback", () => {
    it("applies confidence penalty to matched patterns for SCOPE_UNDERESTIMATED", async () => {
      const model = makeModel();
      vi.mocked(modelService.load).mockResolvedValue(model);
      vi.mocked(modelService.findMatchingPatterns).mockReturnValue([
        {
          pattern: model.patterns.high_complexity[0],
          category: "high_complexity" as const,
          matched_text: "refactor",
        },
      ]);

      const result = await learningService.processReviewerFeedback(
        42,
        "S",
        "feature",
        "Refactor the auth service",
        "Needs deep refactor",
        [makeSignal({ signal_type: "SCOPE_UNDERESTIMATED" })],
        "CHANGES_REQUESTED"
      );

      expect(result.skipped).toBe(false);
      expect(result.signalsProcessed).toBe(1);
      expect(result.patternsAdjusted).toBe(1);

      const savedModel = vi.mocked(modelService.save).mock.calls[0][0];
      // Default penalty is 0.03: 0.45 - 0.03 = 0.42
      expect(savedModel.patterns.high_complexity[0].confidence).toBeCloseTo(0.42, 10);
    });

    it("boosts confidence for COMPLEXITY_OVERESTIMATED signals", async () => {
      const model = makeModel();
      vi.mocked(modelService.load).mockResolvedValue(model);
      vi.mocked(modelService.findMatchingPatterns).mockReturnValue([
        {
          pattern: model.patterns.high_complexity[0],
          category: "high_complexity" as const,
          matched_text: "refactor",
        },
      ]);

      await learningService.processReviewerFeedback(
        43,
        "L",
        "feature",
        "Refactor the API",
        "Needs refactor",
        [makeSignal({ signal_type: "COMPLEXITY_OVERESTIMATED" })],
        "APPROVED"
      );

      const savedModel = vi.mocked(modelService.save).mock.calls[0][0];
      // Boost is 0.01: 0.45 + 0.01 = 0.46
      expect(savedModel.patterns.high_complexity[0].confidence).toBeCloseTo(0.46, 10);
    });

    it("does not adjust confidence for VALIDATION_GAP signals", async () => {
      const model = makeModel();
      vi.mocked(modelService.load).mockResolvedValue(model);
      vi.mocked(modelService.findMatchingPatterns).mockReturnValue([
        {
          pattern: model.patterns.high_complexity[0],
          category: "high_complexity" as const,
          matched_text: "refactor",
        },
      ]);

      const result = await learningService.processReviewerFeedback(
        44,
        "M",
        "feature",
        "Refactor config handler",
        "",
        [makeSignal({ signal_type: "VALIDATION_GAP" })],
        "COMMENTED"
      );

      expect(result.signalsProcessed).toBe(1);
      expect(result.patternsAdjusted).toBe(0);

      const savedModel = vi.mocked(modelService.save).mock.calls[0][0];
      // Confidence unchanged
      expect(savedModel.patterns.high_complexity[0].confidence).toBe(0.45);
    });

    it("returns idempotent result when reviewer feedback already recorded", async () => {
      const model = makeModel({
        prediction_accuracy: {
          total_predictions: 3,
          correct_predictions: 2,
          by_type: {},
          by_size: {},
          recent_outcomes: [
            {
              issue_number: 42,
              predicted_size: "S",
              actual_size_bucket: "REVIEWER_FEEDBACK",
              was_correct: true,
              recorded_at: "2026-02-28T00:00:00Z",
            },
          ],
        },
      });
      vi.mocked(modelService.load).mockResolvedValue(model);

      const result = await learningService.processReviewerFeedback(
        42,
        "S",
        "feature",
        "Some title",
        "",
        [makeSignal()],
        "APPROVED"
      );

      expect(result.skipped).toBe(true);
      expect(result.signalsProcessed).toBe(0);
      expect(modelService.save).not.toHaveBeenCalled();
    });

    it("returns gracefully when no signals provided", async () => {
      const model = makeModel();
      vi.mocked(modelService.load).mockResolvedValue(model);

      const result = await learningService.processReviewerFeedback(
        45,
        "S",
        "feature",
        "Some title",
        "",
        [],
        "APPROVED"
      );

      expect(result.skipped).toBe(false);
      expect(result.signalsProcessed).toBe(0);
      expect(result.patternsAdjusted).toBe(0);
      expect(modelService.save).not.toHaveBeenCalled();
    });

    it("records REVIEWER_FEEDBACK sentinel in recent_outcomes", async () => {
      const model = makeModel();
      vi.mocked(modelService.load).mockResolvedValue(model);
      vi.mocked(modelService.findMatchingPatterns).mockReturnValue([]);

      await learningService.processReviewerFeedback(
        50,
        "M",
        "feature",
        "Add photo upload",
        "",
        [makeSignal()],
        "APPROVED"
      );

      const savedModel = vi.mocked(modelService.save).mock.calls[0][0];
      const outcome = savedModel.prediction_accuracy!.recent_outcomes.find(
        (o) => o.issue_number === 50
      );
      expect(outcome).toBeDefined();
      expect(outcome!.actual_size_bucket).toBe("REVIEWER_FEEDBACK");
      expect(outcome!.was_correct).toBe(true); // APPROVED verdict
    });

    it("marks was_correct=false for CHANGES_REQUESTED verdict", async () => {
      const model = makeModel();
      vi.mocked(modelService.load).mockResolvedValue(model);
      vi.mocked(modelService.findMatchingPatterns).mockReturnValue([]);

      await learningService.processReviewerFeedback(
        51,
        "S",
        "feature",
        "Some feature",
        "",
        [makeSignal()],
        "CHANGES_REQUESTED"
      );

      const savedModel = vi.mocked(modelService.save).mock.calls[0][0];
      const outcome = savedModel.prediction_accuracy!.recent_outcomes.find(
        (o) => o.issue_number === 51
      );
      expect(outcome!.was_correct).toBe(false);
    });

    it("applies custom confidence_penalty override", async () => {
      const model = makeModel();
      vi.mocked(modelService.load).mockResolvedValue(model);
      vi.mocked(modelService.findMatchingPatterns).mockReturnValue([
        {
          pattern: model.patterns.high_complexity[0],
          category: "high_complexity" as const,
          matched_text: "refactor",
        },
      ]);

      await learningService.processReviewerFeedback(
        52,
        "S",
        "feature",
        "Refactor the auth service",
        "Needs refactor",
        [makeSignal({ signal_type: "APPROACH_MISMATCH" })],
        "CHANGES_REQUESTED",
        0.05 // Custom penalty
      );

      const savedModel = vi.mocked(modelService.save).mock.calls[0][0];
      // Custom penalty: 0.45 - 0.05 = 0.40
      expect(savedModel.patterns.high_complexity[0].confidence).toBeCloseTo(0.4, 10);
    });

    it("decrements for ARCHITECTURE_DRIFT signals", async () => {
      const model = makeModel();
      vi.mocked(modelService.load).mockResolvedValue(model);
      vi.mocked(modelService.findMatchingPatterns).mockReturnValue([
        {
          pattern: model.patterns.medium_complexity[0],
          category: "medium_complexity" as const,
          matched_text: "config",
        },
      ]);

      await learningService.processReviewerFeedback(
        53,
        "M",
        "feature",
        "Update config handler",
        "",
        [makeSignal({ signal_type: "ARCHITECTURE_DRIFT" })],
        "CHANGES_REQUESTED"
      );

      const savedModel = vi.mocked(modelService.save).mock.calls[0][0];
      // 0.5 - 0.03 = 0.47
      expect(savedModel.patterns.medium_complexity[0].confidence).toBeCloseTo(0.47, 10);
    });

    it("calls save exactly once on successful processing", async () => {
      const model = makeModel();
      vi.mocked(modelService.load).mockResolvedValue(model);
      vi.mocked(modelService.findMatchingPatterns).mockReturnValue([]);

      await learningService.processReviewerFeedback(
        60,
        "S",
        "feature",
        "Title",
        "",
        [makeSignal()],
        "APPROVED"
      );

      expect(modelService.save).toHaveBeenCalledOnce();
    });
  });
});
