/**
 * FeedbackLearningService - Mid-pipeline complexity model updates
 *
 * Records COMPLEXITY_UNDERESTIMATED feedback signals to the complexity model
 * immediately when detected, without waiting for PR merge.
 *
 * @see Issue #1348
 */
import { ComplexityModelService } from "./ComplexityModelService.js";
import type { ComplexityModel } from "../context/schemas/complexity-model.js";
import type { PipelineFeedbackSignal } from "../context/schemas/feedback.js";
import type { ReviewerSignal, ReviewerSignalType } from "../context/schemas/feedback.js";

/** Confidence decrement applied to matched patterns on underestimation */
const UNDERESTIMATION_CONFIDENCE_PENALTY = 0.05;

/** Default confidence penalty for reviewer signals (smaller than automated) */
const REVIEWER_CONFIDENCE_PENALTY = 0.03;

/** Confidence boost for COMPLEXITY_OVERESTIMATED reviewer signals */
const REVIEWER_CONFIDENCE_BOOST = 0.01;

/** Sentinel bucket value written to recent_outcomes for mid-pipeline records */
const UNDERESTIMATED_BUCKET = "UNDERESTIMATED";

/** Sentinel bucket for reviewer feedback entries (distinct from UNDERESTIMATED) */
const REVIEWER_FEEDBACK_BUCKET = "REVIEWER_FEEDBACK";

/** Maximum number of recent outcomes to retain */
const MAX_RECENT_OUTCOMES = 50;

export interface RecordUnderestimationResult {
  /** true when the outcome was already recorded for this issue (idempotent duplicate) */
  skipped: boolean;
  /** Number of matched patterns whose confidence was decremented */
  patternsAdjusted: number;
}

export interface ProcessReviewerFeedbackResult {
  /** true when reviewer feedback was already recorded for this issue (idempotent) */
  skipped: boolean;
  /** Number of reviewer signals processed */
  signalsProcessed: number;
  /** Number of complexity model patterns adjusted */
  patternsAdjusted: number;
}

/** Signal types that decrement pattern confidence (negative reviewer feedback) */
const DECREMENT_SIGNALS: ReadonlySet<ReviewerSignalType> = new Set([
  "SCOPE_UNDERESTIMATED",
  "APPROACH_MISMATCH",
  "ARCHITECTURE_DRIFT",
]);

/** Signal types that increment pattern confidence (positive reviewer feedback) */
const INCREMENT_SIGNALS: ReadonlySet<ReviewerSignalType> = new Set(["COMPLEXITY_OVERESTIMATED"]);

/** Reviewer comment pattern matchers for natural-language signal detection */
const REVIEWER_PATTERNS: ReadonlyArray<{
  type: ReviewerSignalType;
  keywords: RegExp;
}> = [
  {
    type: "SCOPE_UNDERESTIMATED",
    keywords:
      /\b(too large|too big|split this|break (this |it )?up|should be separate|multiple PRs?|scope too|oversized)\b/i,
  },
  {
    type: "APPROACH_MISMATCH",
    keywords:
      /\b(wrong approach|should have used|better (approach|pattern|way)|instead of|shouldn't use|anti.?pattern|not the right)\b/i,
  },
  {
    type: "VALIDATION_GAP",
    keywords:
      /\b(missing tests?|untested|no (test|coverage)|needs? tests?|test coverage|add tests?)\b/i,
  },
  {
    type: "COMPLEXITY_OVERESTIMATED",
    keywords:
      /\b(over.?engineer|too complex|simpler|unnecessarily complex|YAGNI|could be simpler|overkill)\b/i,
  },
  {
    type: "ARCHITECTURE_DRIFT",
    keywords:
      /\b(architectural concern|doesn't fit|pattern violation|inconsistent with|breaks? convention|style mismatch|doesn't follow)\b/i,
  },
];

export class FeedbackLearningService {
  constructor(private modelService: ComplexityModelService) {}

  /**
   * Record a COMPLEXITY_UNDERESTIMATED feedback signal.
   *
   * Steps:
   * 1. Load the model.
   * 2. Idempotency check — if issue_number already in recent_outcomes, return {skipped: true}.
   * 3. Find matched patterns from issue title+description (or use provided list).
   * 4. Decrement confidence by UNDERESTIMATION_CONFIDENCE_PENALTY on each matched pattern.
   * 5. Add a recent_outcomes entry (was_correct: false, actual_size_bucket: 'UNDERESTIMATED').
   * 6. Increment prediction_accuracy.total_predictions; leave correct_predictions unchanged.
   * 7. Save atomically.
   *
   * @param issueNumber GitHub issue number (dedup key)
   * @param predictedSizeLabel Size label predicted by the model (e.g. 'S')
   * @param issueType Issue type label (e.g. 'feature')
   * @param issueTitle Issue title (for pattern matching)
   * @param issueDescription Issue description/requirements (for pattern matching)
   * @param signal The originating feedback signal
   * @param matchedPatterns Optional pre-computed matched pattern strings (avoids re-matching)
   */
  async recordUnderestimation(
    issueNumber: number,
    predictedSizeLabel: string,
    issueType: string,
    issueTitle: string,
    issueDescription: string,
    signal: PipelineFeedbackSignal,
    matchedPatterns?: string[]
  ): Promise<RecordUnderestimationResult> {
    const model = await this.modelService.load();

    // Idempotency check
    if (this.modelService.isOutcomeRecorded(model, issueNumber)) {
      return { skipped: true, patternsAdjusted: 0 };
    }

    // Find matching patterns if not provided
    const combinedText = `${issueTitle} ${issueDescription}`;
    const patternMatches = matchedPatterns
      ? matchedPatterns
      : this.modelService.findMatchingPatterns(combinedText, model).map((m) => m.pattern.match);

    // Apply confidence decrement to matched patterns
    let updated = this.decrementMatchedPatternConfidence(model, patternMatches);

    // Record in prediction_accuracy
    updated = this.recordInAccuracy(updated, issueNumber, predictedSizeLabel, issueType);

    // Atomic save
    await this.modelService.save(updated);

    return { skipped: false, patternsAdjusted: patternMatches.length };
  }

  /**
   * Decrement confidence by UNDERESTIMATION_CONFIDENCE_PENALTY for all matched patterns.
   * Confidence is clamped to [0.0, 1.0].
   */
  private decrementMatchedPatternConfidence(
    model: ComplexityModel,
    matchedPatterns: string[]
  ): ComplexityModel {
    if (matchedPatterns.length === 0) return model;

    const adjust = (patterns: ComplexityModel["patterns"]["high_complexity"]) =>
      patterns.map((p) =>
        matchedPatterns.includes(p.match)
          ? {
              ...p,
              confidence: Math.max(0, p.confidence - UNDERESTIMATION_CONFIDENCE_PENALTY),
            }
          : p
      );

    return {
      ...model,
      patterns: {
        high_complexity: adjust(model.patterns.high_complexity),
        medium_complexity: adjust(model.patterns.medium_complexity),
        low_complexity: adjust(model.patterns.low_complexity),
      },
    };
  }

  /**
   * Process reviewer feedback signals and adjust complexity model patterns.
   *
   * Steps:
   * 1. Load the model.
   * 2. Idempotency check — if reviewer feedback already recorded for this issue
   *    (sentinel bucket REVIEWER_FEEDBACK), return {skipped: true}.
   * 3. For each reviewer signal, find matching complexity patterns and apply
   *    confidence adjustments based on signal type.
   * 4. Record a recent_outcomes entry with actual_size_bucket = 'REVIEWER_FEEDBACK'.
   * 5. Atomic save.
   *
   * @param issueNumber GitHub issue number (dedup key)
   * @param predictedSizeLabel Size label predicted by the model
   * @param issueType Issue type label
   * @param issueTitle Issue title (for pattern matching)
   * @param issueDescription Issue description (for pattern matching)
   * @param reviewerSignals Parsed reviewer signals
   * @param overallVerdict Overall review verdict (APPROVED, CHANGES_REQUESTED, COMMENTED)
   * @param confidencePenalty Override for default confidence penalty (default: 0.03)
   *
   * @see Issue #1409
   */
  async processReviewerFeedback(
    issueNumber: number,
    predictedSizeLabel: string,
    issueType: string,
    issueTitle: string,
    issueDescription: string,
    reviewerSignals: ReviewerSignal[],
    overallVerdict: string,
    confidencePenalty: number = REVIEWER_CONFIDENCE_PENALTY
  ): Promise<ProcessReviewerFeedbackResult> {
    const model = await this.modelService.load();

    // Idempotency: check for REVIEWER_FEEDBACK sentinel in recent_outcomes
    const existingReviewerOutcome = (model.prediction_accuracy?.recent_outcomes ?? []).find(
      (o) => o.issue_number === issueNumber && o.actual_size_bucket === REVIEWER_FEEDBACK_BUCKET
    );
    if (existingReviewerOutcome) {
      return { skipped: true, signalsProcessed: 0, patternsAdjusted: 0 };
    }

    if (reviewerSignals.length === 0) {
      return { skipped: false, signalsProcessed: 0, patternsAdjusted: 0 };
    }

    // Find matching complexity patterns from issue text
    const combinedText = `${issueTitle} ${issueDescription}`;
    const patternMatches = this.modelService
      .findMatchingPatterns(combinedText, model)
      .map((m) => m.pattern.match);

    // Apply confidence adjustments based on each reviewer signal type
    let updated = model;
    let totalPatternsAdjusted = 0;

    for (const signal of reviewerSignals) {
      if (DECREMENT_SIGNALS.has(signal.signal_type)) {
        updated = this.adjustPatternConfidence(updated, patternMatches, -confidencePenalty);
        totalPatternsAdjusted += patternMatches.length;
      } else if (INCREMENT_SIGNALS.has(signal.signal_type)) {
        updated = this.adjustPatternConfidence(updated, patternMatches, REVIEWER_CONFIDENCE_BOOST);
        totalPatternsAdjusted += patternMatches.length;
      }
      // VALIDATION_GAP: no confidence change (testing gap, not complexity error)
    }

    // Record reviewer feedback outcome with sentinel bucket
    const wasCorrect = overallVerdict === "APPROVED";
    updated = this.recordReviewerOutcome(updated, issueNumber, predictedSizeLabel, wasCorrect);

    // Atomic save
    await this.modelService.save(updated);

    return {
      skipped: false,
      signalsProcessed: reviewerSignals.length,
      patternsAdjusted: totalPatternsAdjusted,
    };
  }

  /**
   * Parse review comments into structured reviewer signals.
   *
   * Each comment is tested against all REVIEWER_PATTERNS. Multiple signals
   * can be emitted from a single comment. Comments shorter than
   * minCommentLength are skipped.
   *
   * @param comments Array of {body, reviewer_login, verdict} objects
   * @param minCommentLength Minimum comment length to consider (default: 10)
   * @returns Array of parsed ReviewerSignal objects
   *
   * @see Issue #1409
   */
  parseReviewerComments(
    comments: Array<{
      body: string;
      reviewer_login: string;
      verdict: "APPROVED" | "CHANGES_REQUESTED" | "COMMENTED";
    }>,
    minCommentLength: number = 10
  ): ReviewerSignal[] {
    const signals: ReviewerSignal[] = [];

    for (const comment of comments) {
      if (!comment.body || comment.body.length < minCommentLength) {
        continue;
      }

      for (const pattern of REVIEWER_PATTERNS) {
        const match = comment.body.match(pattern.keywords);
        if (match) {
          signals.push({
            signal_type: pattern.type,
            source_comment: comment.body,
            reviewer_login: comment.reviewer_login,
            review_verdict: comment.verdict,
            confidence: 0.7,
            matched_keywords: [match[0]],
          });
        }
      }
    }

    return signals;
  }

  /**
   * Adjust confidence on matched patterns by a given delta.
   * Confidence is clamped to [0.0, 1.0].
   */
  private adjustPatternConfidence(
    model: ComplexityModel,
    matchedPatterns: string[],
    delta: number
  ): ComplexityModel {
    if (matchedPatterns.length === 0) return model;

    const adjust = (patterns: ComplexityModel["patterns"]["high_complexity"]) =>
      patterns.map((p) =>
        matchedPatterns.includes(p.match)
          ? {
              ...p,
              confidence: Math.max(0, Math.min(1, p.confidence + delta)),
            }
          : p
      );

    return {
      ...model,
      patterns: {
        high_complexity: adjust(model.patterns.high_complexity),
        medium_complexity: adjust(model.patterns.medium_complexity),
        low_complexity: adjust(model.patterns.low_complexity),
      },
    };
  }

  /**
   * Record a reviewer feedback entry in recent_outcomes.
   * Uses REVIEWER_FEEDBACK sentinel bucket for idempotency separation.
   */
  private recordReviewerOutcome(
    model: ComplexityModel,
    issueNumber: number,
    predictedSizeLabel: string,
    wasCorrect: boolean
  ): ComplexityModel {
    const accuracy = model.prediction_accuracy ?? {
      total_predictions: 0,
      correct_predictions: 0,
      by_type: {},
      by_size: {},
      recent_outcomes: [],
    };

    const recentOutcomes = [
      ...accuracy.recent_outcomes,
      {
        issue_number: issueNumber,
        predicted_size: predictedSizeLabel,
        actual_size_bucket: REVIEWER_FEEDBACK_BUCKET,
        was_correct: wasCorrect,
        recorded_at: new Date().toISOString(),
      },
    ].slice(-MAX_RECENT_OUTCOMES);

    return {
      ...model,
      prediction_accuracy: {
        ...accuracy,
        recent_outcomes: recentOutcomes,
      },
    };
  }

  /**
   * Add a recent_outcomes entry and update prediction_accuracy counters.
   *
   * - total_predictions incremented by 1
   * - correct_predictions unchanged (underestimation is always wrong)
   * - by_type and by_size counters updated
   * - recent_outcomes capped at MAX_RECENT_OUTCOMES
   */
  private recordInAccuracy(
    model: ComplexityModel,
    issueNumber: number,
    predictedSizeLabel: string,
    issueType: string
  ): ComplexityModel {
    const accuracy = model.prediction_accuracy ?? {
      total_predictions: 0,
      correct_predictions: 0,
      by_type: {},
      by_size: {},
      recent_outcomes: [],
    };

    const byType = { ...accuracy.by_type };
    const typeEntry = byType[issueType] ?? { total: 0, correct: 0 };
    byType[issueType] = {
      total: typeEntry.total + 1,
      correct: typeEntry.correct, // unchanged — underestimation is incorrect
    };

    const bySize = { ...accuracy.by_size };
    const sizeEntry = bySize[predictedSizeLabel] ?? { total: 0, correct: 0 };
    bySize[predictedSizeLabel] = {
      total: sizeEntry.total + 1,
      correct: sizeEntry.correct, // unchanged
    };

    const recentOutcomes = [
      ...accuracy.recent_outcomes,
      {
        issue_number: issueNumber,
        predicted_size: predictedSizeLabel,
        actual_size_bucket: UNDERESTIMATED_BUCKET,
        was_correct: false,
        recorded_at: new Date().toISOString(),
      },
    ].slice(-MAX_RECENT_OUTCOMES);

    return {
      ...model,
      prediction_accuracy: {
        total_predictions: accuracy.total_predictions + 1,
        correct_predictions: accuracy.correct_predictions,
        by_type: byType,
        by_size: bySize,
        recent_outcomes: recentOutcomes,
      },
    };
  }
}
