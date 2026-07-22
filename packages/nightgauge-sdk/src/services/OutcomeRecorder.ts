/**
 * OutcomeRecorder - Orchestrates execution outcome recording with feedback loop
 *
 * Records pipeline execution outcomes to update the complexity model's
 * prediction accuracy, type/priority adjustments, and pattern confidence.
 * Supports idempotency via issue_number dedup key.
 *
 * @see Issue #650 - Feedback Loop: Record Execution Outcomes
 * @see docs/ARCHITECTURE.md - SDK utility pattern (pure TS, no VSCode deps)
 */

import { ComplexityModelService } from "./ComplexityModelService.js";
import type {
  ComplexityModel,
  ExecutionOutcome,
  PredictionAccuracy,
} from "../context/schemas/complexity-model.js";
import type { SurvivalRecord, SurvivalCalibration } from "../context/schemas/survival.js";
import type { SizeLabel } from "./SuggestionEngine.js";

/** Maximum number of recent outcomes to retain for idempotency and trend analysis */
const MAX_RECENT_OUTCOMES = 50;

/** Minimum observations before adjusting type/priority modifiers */
const MIN_OBSERVATIONS_FOR_ADJUSTMENT = 5;

/**
 * Learning rate for directional type modifier correction.
 *
 * Applied per size-level error on each wrong prediction:
 *   shift = -error × LEARNING_RATE
 *
 * Example: predicted M (idx 2), actual XS (idx 0) → error = 2 → shift = -0.10
 * After ~15-20 observations of consistent M→XS error, modifier reaches ≈ -1.5 to -2.0,
 * moving predictions from M range into S/XS range.
 */
const LEARNING_RATE = 0.05;

/** Maximum absolute value for type modifiers (prevents runaway correction) */
const MAX_MODIFIER_MAGNITUDE = 3.0;

/** Confidence boost for correct predictions */
const CONFIDENCE_BOOST = 0.02;

/** Confidence penalty for incorrect predictions */
const CONFIDENCE_PENALTY = 0.05;

/** Ordered size labels for adjacency checks */
const SIZE_ORDER: SizeLabel[] = ["XS", "S", "M", "L", "XL"];

/**
 * Neutral starting confidence for survival calibration (#4152/#4153) — not a
 * ceiling. Starting at 1.0 would permanently absorb the weak reward into the
 * clamp; starting at 0.5 leaves headroom in both directions. Mirrors
 * internal/github/outcome_survival.go's defaultSurvivalConfidence.
 */
const DEFAULT_SURVIVAL_CONFIDENCE = 0.5;

/** Bound on the survival calibration dedup ledger — mirrors Go's maxProcessedSurvivalSHAs */
const MAX_PROCESSED_SURVIVAL_SHAS = 500;

export interface OutcomeRecordResult {
  model: ComplexityModel;
  skipped: boolean;
}

/** Result of applySurvivalVerdicts — mirrors Go's SurvivalCalibrationResult. */
export interface SurvivalCalibrationApplyResult {
  model: ComplexityModel;
  processed: number;
  penaltiesApplied: number;
  rewardsApplied: number;
  confidence: number;
}

export class OutcomeRecorder {
  constructor(private modelService: ComplexityModelService) {}

  /**
   * Record an execution outcome with conditional idempotency.
   *
   * If an existing entry has `actual_lines_changed === 0` (garbage from
   * failure-path recording) and the new outcome has real line data, the
   * old entry is removed, its calibration effects reversed, and the new
   * outcome is recorded in its place.
   *
   * Non-zero existing entries are still protected by idempotency.
   *
   * @see Issue #1198 - Allow outcome overwrite when existing entry has 0-line garbage data
   */
  async recordOutcome(outcome: ExecutionOutcome): Promise<OutcomeRecordResult> {
    let model = await this.modelService.load();

    // Non-baseline runs are excluded from calibration updates (Issues #2433, #3009).
    // They still get appended to recent_outcomes for cost tracking and
    // dashboard display, but do NOT update prediction accuracy, type
    // modifiers, or pattern confidence — those baselines should reflect
    // default-routing runs only.
    //
    //   - "elevated" and "normal" → flow into calibration (default routing)
    //   - "efficiency", "maximum", "frontier", legacy "supercharge" → skip calibration
    //   - undefined/null → flow into calibration (legacy untagged records)
    // "frontier" (Fable tier) deviates furthest from the baseline — its cost
    // profile must never pollute the elevated prediction baselines.
    const mode = outcome.pipeline_mode;
    const skipsCalibration =
      mode === "supercharge" || mode === "efficiency" || mode === "maximum" || mode === "frontier";
    if (skipsCalibration) {
      const actualBucket = this.getActualSizeBucket(outcome.actual_lines_changed, model);
      const updated = this.modelService.recordOutcome(model, {
        issue_number: outcome.issue_number,
        pr_number: outcome.pr_number,
        size_label: actualBucket,
        lines_changed: outcome.actual_lines_changed,
        model_id: outcome.model_used,
        completed_at: outcome.completed_at,
        batch_context: outcome.batch_context,
      });
      // Skip accuracy, type modifier, and pattern confidence updates
      return { model: updated, skipped: false };
    }

    // Check for existing outcome — conditional idempotency
    const existingOutcome = this.findExistingOutcome(model, outcome.issue_number);
    if (existingOutcome) {
      const isGarbage = (existingOutcome.actual_lines_changed ?? -1) === 0;
      const hasRealData = outcome.actual_lines_changed > 0;
      if (isGarbage && hasRealData) {
        // Remove garbage entry and reverse its effects before re-recording
        model = this.reverseOutcomeEffects(model, existingOutcome, outcome);
      } else {
        return { model, skipped: true };
      }
    }

    // 1. Determine actual size bucket from lines changed (before recording,
    //    so we can record under the ACTUAL bucket for accurate calibration)
    const actualBucket = this.getActualSizeBucket(outcome.actual_lines_changed, model);

    // 2. Record basic outcome via existing recordOutcome (size calibration + model tracking)
    //    Record under ACTUAL size bucket so calibration data reflects true size distribution
    let updated = this.modelService.recordOutcome(model, {
      issue_number: outcome.issue_number,
      pr_number: outcome.pr_number,
      size_label: actualBucket,
      lines_changed: outcome.actual_lines_changed,
      model_id: outcome.model_used,
      completed_at: outcome.completed_at,
      batch_context: outcome.batch_context,
    });

    // 3. Check prediction accuracy
    const wasCorrect = this.isPredictionCorrect(outcome.predicted_size, actualBucket);

    // 4. Update prediction accuracy section
    updated = this.updateAccuracy(updated, outcome, actualBucket, wasCorrect);

    // 5. Adjust type modifiers based on directional error correction
    updated = this.adjustTypeModifiers(updated, outcome, actualBucket, wasCorrect);

    // 6. Adjust pattern confidence
    updated = this.adjustPatternConfidence(updated, outcome, wasCorrect);

    return { model: updated, skipped: false };
  }

  /**
   * Determine actual size bucket from lines changed using model thresholds.
   *
   * Thresholds are upper bounds: lines <= XS threshold → XS, etc.
   */
  getActualSizeBucket(linesChanged: number, model: ComplexityModel): SizeLabel {
    const thresholds = model.lines_changed_thresholds;

    if (linesChanged <= thresholds.XS) return "XS";
    if (linesChanged <= thresholds.S) return "S";
    if (linesChanged <= thresholds.M) return "M";
    if (linesChanged <= thresholds.L) return "L";
    return "XL";
  }

  /**
   * Check if prediction was correct with adjacent-size tolerance.
   *
   * Adjacent sizes count as correct (e.g., predicted S when actual was M).
   * This prevents penalizing borderline cases.
   */
  isPredictionCorrect(predicted: SizeLabel, actual: SizeLabel): boolean {
    const predictedIdx = SIZE_ORDER.indexOf(predicted);
    const actualIdx = SIZE_ORDER.indexOf(actual);
    return Math.abs(predictedIdx - actualIdx) <= 1;
  }

  /**
   * Update the prediction_accuracy section of the model.
   */
  updateAccuracy(
    model: ComplexityModel,
    outcome: ExecutionOutcome,
    actualBucket: SizeLabel,
    wasCorrect: boolean
  ): ComplexityModel {
    const accuracy: PredictionAccuracy = model.prediction_accuracy ?? {
      total_predictions: 0,
      correct_predictions: 0,
      by_type: {},
      by_size: {},
      recent_outcomes: [],
    };

    // Update totals
    const totalPredictions = accuracy.total_predictions + 1;
    const correctPredictions = accuracy.correct_predictions + (wasCorrect ? 1 : 0);

    // Update by_type
    const byType = { ...accuracy.by_type };
    const typeEntry = byType[outcome.issue_type] ?? { total: 0, correct: 0 };
    byType[outcome.issue_type] = {
      total: typeEntry.total + 1,
      correct: typeEntry.correct + (wasCorrect ? 1 : 0),
    };

    // Update by_size
    const bySize = { ...accuracy.by_size };
    const sizeEntry = bySize[outcome.predicted_size] ?? {
      total: 0,
      correct: 0,
    };
    bySize[outcome.predicted_size] = {
      total: sizeEntry.total + 1,
      correct: sizeEntry.correct + (wasCorrect ? 1 : 0),
    };

    // Add to recent_outcomes (keep last MAX_RECENT_OUTCOMES)
    const recentOutcomes = [
      ...accuracy.recent_outcomes,
      {
        issue_number: outcome.issue_number,
        predicted_size: outcome.predicted_size,
        actual_size_bucket: actualBucket,
        was_correct: wasCorrect,
        recorded_at: outcome.completed_at,
        actual_lines_changed: outcome.actual_lines_changed,
      },
    ].slice(-MAX_RECENT_OUTCOMES);

    return {
      ...model,
      prediction_accuracy: {
        total_predictions: totalPredictions,
        correct_predictions: correctPredictions,
        by_type: byType,
        by_size: bySize,
        recent_outcomes: recentOutcomes,
      },
    };
  }

  /**
   * Adjust type modifiers using directional error correction.
   *
   * When a prediction is wrong, shifts the type modifier in the corrective
   * direction proportional to the error magnitude:
   *
   *   shift = -(predictedIdx - actualIdx) × LEARNING_RATE
   *
   * Over-predicting (M→XS): error = +2, shift = -0.10 (modifier decreases)
   * Under-predicting (XS→M): error = -2, shift = +0.10 (modifier increases)
   *
   * This replaces the previous dampening approach (modifier × 0.95) which
   * was a no-op when the modifier was 0 and lacked directional awareness.
   *
   * Only adjusts after MIN_OBSERVATIONS_FOR_ADJUSTMENT observations
   * for that type to avoid over-fitting on small samples.
   */
  adjustTypeModifiers(
    model: ComplexityModel,
    outcome: ExecutionOutcome,
    actualBucket: SizeLabel,
    wasCorrect: boolean
  ): ComplexityModel {
    const accuracy = model.prediction_accuracy;
    if (!accuracy) return model;

    const typeData = accuracy.by_type[outcome.issue_type];
    if (!typeData || typeData.total < MIN_OBSERVATIONS_FOR_ADJUSTMENT) {
      return model;
    }

    const typeAdjustments = { ...model.type_adjustments };
    const existing = typeAdjustments[outcome.issue_type];

    if (!wasCorrect && existing) {
      // Compute directional error: positive = over-predicting, negative = under-predicting
      const predictedIdx = SIZE_ORDER.indexOf(outcome.predicted_size);
      const actualIdx = SIZE_ORDER.indexOf(actualBucket);
      const error = predictedIdx - actualIdx;

      // Shift modifier in the corrective direction
      const shift = -error * LEARNING_RATE;
      const newModifier = existing.modifier + shift;

      typeAdjustments[outcome.issue_type] = {
        ...existing,
        modifier:
          Math.round(
            Math.max(-MAX_MODIFIER_MAGNITUDE, Math.min(MAX_MODIFIER_MAGNITUDE, newModifier)) * 100
          ) / 100,
        observations: existing.observations + 1,
      };
    } else if (existing) {
      // Correct prediction — just increment observation count
      typeAdjustments[outcome.issue_type] = {
        ...existing,
        observations: existing.observations + 1,
      };
    }

    return {
      ...model,
      type_adjustments: typeAdjustments,
    };
  }

  /**
   * Adjust pattern confidence based on prediction accuracy.
   *
   * Patterns that contributed to correct predictions gain confidence (+0.02).
   * Patterns that contributed to incorrect predictions lose confidence (-0.05).
   * Confidence is clamped to [0.0, 1.0].
   */
  adjustPatternConfidence(
    model: ComplexityModel,
    outcome: ExecutionOutcome,
    wasCorrect: boolean
  ): ComplexityModel {
    const matchedPatterns = outcome.patterns_matched ?? [];
    if (matchedPatterns.length === 0) return model;

    const delta = wasCorrect ? CONFIDENCE_BOOST : -CONFIDENCE_PENALTY;

    const adjustPatterns = (patterns: ComplexityModel["patterns"]["high_complexity"]) =>
      patterns.map((p) => {
        if (matchedPatterns.includes(p.match)) {
          return {
            ...p,
            confidence: Math.max(0, Math.min(1, p.confidence + delta)),
          };
        }
        return p;
      });

    return {
      ...model,
      patterns: {
        high_complexity: adjustPatterns(model.patterns.high_complexity),
        medium_complexity: adjustPatterns(model.patterns.medium_complexity),
        low_complexity: adjustPatterns(model.patterns.low_complexity),
      },
    };
  }

  /**
   * Apply the bias-safe survival calibration rule (#4152 penalize-reverts,
   * #4153 weak-reward-survived; spike #4134 §1.2) to a batch of finalized
   * survival records. Mirrors internal/github/outcome_survival.go's
   * ApplySurvivalVerdicts field-for-field so both languages converge on the
   * same calibration state persisted to the shared complexity-model.yaml —
   * see docs/OUTCOME_RECORDING.md#survival-calibration.
   *
   * Bias-safe rule:
   *   - reverted / broke (proven negative) → confidence PENALTY, gated behind
   *     MIN_OBSERVATIONS_FOR_ADJUSTMENT cumulative negative observations.
   *   - survived (weak positive, terminal only) → confidence BOOST, gated
   *     behind MIN_OBSERVATIONS_FOR_ADJUSTMENT cumulative *finalized* survived
   *     observations — deliberately separate and weaker than the penalty, and
   *     never applied to pending/unproven survival.
   *   - pending → skipped entirely (not terminal, carries no signal).
   *   - unobserved → terminal but no signal; ledgered so it isn't rescanned.
   *
   * Pure, like the rest of this class's helpers — it does not load/save the
   * model. Records are deduplicated by merge_commit_sha against a persisted
   * ledger (`survival_calibration.processed_shas`), so it is safe to call
   * with any subset of records — including a full re-read of the survival
   * journal — without double-counting an already-processed verdict.
   */
  applySurvivalVerdicts(
    model: ComplexityModel,
    records: SurvivalRecord[]
  ): SurvivalCalibrationApplyResult {
    const existing = model.prediction_accuracy?.survival_calibration;
    const sc: SurvivalCalibration = existing
      ? { ...existing, processed_shas: [...existing.processed_shas] }
      : {
          confidence: DEFAULT_SURVIVAL_CONFIDENCE,
          negative_observations: 0,
          positive_observations: 0,
          penalties_applied: 0,
          rewards_applied: 0,
          processed_shas: [],
        };

    const alreadyProcessed = new Set(sc.processed_shas);
    let processed = 0;
    let penaltiesApplied = 0;
    let rewardsApplied = 0;

    for (const rec of records) {
      const isTerminal = rec.verdict !== "pending";
      if (!rec.merge_commit_sha || !isTerminal || alreadyProcessed.has(rec.merge_commit_sha)) {
        continue;
      }

      if (rec.verdict === "reverted" || rec.verdict === "broke") {
        sc.negative_observations += 1;
        if (sc.negative_observations >= MIN_OBSERVATIONS_FOR_ADJUSTMENT) {
          sc.confidence = clampConfidence(sc.confidence - CONFIDENCE_PENALTY);
          sc.penalties_applied += 1;
          penaltiesApplied += 1;
        }
      } else if (rec.verdict === "survived") {
        sc.positive_observations += 1;
        if (sc.positive_observations >= MIN_OBSERVATIONS_FOR_ADJUSTMENT) {
          sc.confidence = clampConfidence(sc.confidence + CONFIDENCE_BOOST);
          sc.rewards_applied += 1;
          rewardsApplied += 1;
        }
      }
      // "unobserved" — terminal, no signal (censored data); falls through to
      // the ledger bookkeeping below without touching confidence/counters.

      alreadyProcessed.add(rec.merge_commit_sha);
      sc.processed_shas.push(rec.merge_commit_sha);
      if (sc.processed_shas.length > MAX_PROCESSED_SURVIVAL_SHAS) {
        sc.processed_shas = sc.processed_shas.slice(-MAX_PROCESSED_SURVIVAL_SHAS);
      }
      processed += 1;
    }

    if (processed === 0) {
      return {
        model,
        processed: 0,
        penaltiesApplied: 0,
        rewardsApplied: 0,
        confidence: sc.confidence,
      };
    }

    const accuracy: PredictionAccuracy = model.prediction_accuracy ?? {
      total_predictions: 0,
      correct_predictions: 0,
      by_type: {},
      by_size: {},
      recent_outcomes: [],
    };

    return {
      model: {
        ...model,
        prediction_accuracy: {
          ...accuracy,
          survival_calibration: sc,
        },
      },
      processed,
      penaltiesApplied,
      rewardsApplied,
      confidence: sc.confidence,
    };
  }

  /**
   * Find an existing outcome entry for the given issue number.
   *
   * @see Issue #1198 - Conditional idempotency
   */
  private findExistingOutcome(
    model: ComplexityModel,
    issueNumber: number
  ): PredictionAccuracy["recent_outcomes"][number] | undefined {
    return (model.prediction_accuracy?.recent_outcomes ?? []).find(
      (o) => o.issue_number === issueNumber
    );
  }

  /**
   * Reverse the effects of a garbage outcome entry and remove it from recent_outcomes.
   *
   * Reverses:
   * 1. recent_outcomes — removes the old entry
   * 2. total_predictions / correct_predictions counters
   * 3. by_type and by_size counters
   * 4. size_calibration running average for the old bucket
   * 5. total_observations and model_tracking counts
   *
   * @see Issue #1198 - Calibration correction on overwrite
   */
  private reverseOutcomeEffects(
    model: ComplexityModel,
    existing: PredictionAccuracy["recent_outcomes"][number],
    newOutcome: ExecutionOutcome
  ): ComplexityModel {
    const accuracy = model.prediction_accuracy!;

    // 1. Remove old entry from recent_outcomes
    const filteredOutcomes = accuracy.recent_outcomes.filter(
      (o) => o.issue_number !== existing.issue_number
    );

    // 2. Reverse prediction counters
    const totalPredictions = Math.max(0, accuracy.total_predictions - 1);
    const correctPredictions = Math.max(
      0,
      accuracy.correct_predictions - (existing.was_correct ? 1 : 0)
    );

    // 3. Reverse by_type
    const byType = { ...accuracy.by_type };
    const typeEntry = byType[newOutcome.issue_type];
    if (typeEntry && typeEntry.total > 0) {
      byType[newOutcome.issue_type] = {
        total: typeEntry.total - 1,
        correct: Math.max(0, typeEntry.correct - (existing.was_correct ? 1 : 0)),
      };
    }

    // 4. Reverse by_size (keyed by predicted_size)
    const bySize = { ...accuracy.by_size };
    const sizeEntry = bySize[existing.predicted_size];
    if (sizeEntry && sizeEntry.total > 0) {
      bySize[existing.predicted_size] = {
        total: sizeEntry.total - 1,
        correct: Math.max(0, sizeEntry.correct - (existing.was_correct ? 1 : 0)),
      };
    }

    // 5. Reverse size_calibration for old actual bucket
    const oldBucket = existing.actual_size_bucket as keyof typeof model.size_calibration;
    const cal = model.size_calibration[oldBucket];
    let updatedCalibration = { ...model.size_calibration };
    if (cal && cal.sample_count > 0) {
      const newSampleCount = cal.sample_count - 1;
      // Reverse running average: old_avg = (total - removed_value) / (n - 1)
      // removed_value ≈ 0 for garbage entries (actual_lines_changed was 0)
      const removedLines = existing.actual_lines_changed ?? 0;
      const newAverage =
        newSampleCount > 0
          ? (cal.actual_average_lines * cal.sample_count - removedLines) / newSampleCount
          : cal.expected_lines;
      updatedCalibration = {
        ...updatedCalibration,
        [oldBucket]: {
          ...cal,
          actual_average_lines: Math.round(newAverage),
          sample_count: newSampleCount,
        },
      };
    }

    // 6. Reverse total_observations and model_tracking
    const totalObservations = Math.max(0, model.total_observations - 1);
    const modelId = newOutcome.model_used;
    const currentModelCount = model.model_tracking.observations_by_model[modelId] ?? 0;

    return {
      ...model,
      total_observations: totalObservations,
      size_calibration: updatedCalibration,
      model_tracking: {
        ...model.model_tracking,
        observations_by_model: {
          ...model.model_tracking.observations_by_model,
          [modelId]: Math.max(0, currentModelCount - 1),
        },
      },
      prediction_accuracy: {
        total_predictions: totalPredictions,
        correct_predictions: correctPredictions,
        by_type: byType,
        by_size: bySize,
        recent_outcomes: filteredOutcomes,
      },
    };
  }
}

/**
 * Clamp a confidence value to [0, 1]. Mirrors outcome_survival.go's
 * clampConfidence exactly.
 */
function clampConfidence(v: number): number {
  return Math.max(0, Math.min(1, v));
}
