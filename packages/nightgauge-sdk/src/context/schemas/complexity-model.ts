import { z } from "zod";
import { SurvivalCalibrationSchema } from "./survival.js";

/**
 * Complexity Pattern Schema
 *
 * Patterns learned from historical data that influence complexity scoring.
 * Each pattern has a regex match, modifier, confidence, and observation count.
 */
export const ComplexityPatternSchema = z.object({
  /** Regex pattern to match against issue title/description */
  match: z.string().min(1),
  /** Score modifier: positive increases complexity, negative decreases */
  modifier: z.number(),
  /** Confidence score (0.0-1.0) based on observation consistency */
  confidence: z.number().min(0).max(1),
  /** Human-readable explanation of why this pattern affects complexity */
  rationale: z.string(),
  /** Number of observations supporting this pattern */
  observations: z.number().int().nonnegative(),
  /** Source of this pattern: "repo-specific" (default) or "cross-project" */
  source: z.enum(["repo-specific", "cross-project"]).optional(),
});
export type ComplexityPattern = z.infer<typeof ComplexityPatternSchema>;

/**
 * Patterns by Complexity Category
 */
export const PatternCategoriesSchema = z.object({
  /** Patterns indicating high complexity (L/XL) */
  high_complexity: z.array(ComplexityPatternSchema).default([]),
  /** Patterns indicating medium complexity (M) */
  medium_complexity: z.array(ComplexityPatternSchema).default([]),
  /** Patterns indicating low complexity (S/XS) */
  low_complexity: z.array(ComplexityPatternSchema).default([]),
});
export type PatternCategories = z.infer<typeof PatternCategoriesSchema>;

/**
 * Size Calibration Entry
 *
 * Tracks expected vs actual lines changed for each size label.
 */
export const SizeCalibrationEntrySchema = z.object({
  /** Expected lines changed for this size */
  expected_lines: z.number().int().nonnegative(),
  /** Actual average lines changed from observed PRs */
  actual_average_lines: z.number().nonnegative(),
  /** Expected time in minutes */
  expected_minutes: z.number().int().nonnegative().nullish(),
  /** Number of observations for this size */
  sample_count: z.number().int().nonnegative(),
  /** Human-readable note about calibration accuracy */
  accuracy_note: z.string().nullish(),
});
export type SizeCalibrationEntry = z.infer<typeof SizeCalibrationEntrySchema>;

/**
 * Size Calibration Map
 */
export const SizeCalibrationSchema = z.object({
  XS: SizeCalibrationEntrySchema,
  S: SizeCalibrationEntrySchema,
  M: SizeCalibrationEntrySchema,
  L: SizeCalibrationEntrySchema,
  XL: SizeCalibrationEntrySchema,
});
export type SizeCalibration = z.infer<typeof SizeCalibrationSchema>;

/**
 * Type Adjustment Entry
 *
 * Modifiers based on issue type (feature, bug, etc.)
 */
export const TypeAdjustmentSchema = z.object({
  /** Score modifier for this type */
  modifier: z.number(),
  /** Number of observations */
  observations: z.number().int().nonnegative(),
  /** Rationale for this adjustment */
  rationale: z.string().nullish(),
});
export type TypeAdjustment = z.infer<typeof TypeAdjustmentSchema>;

/**
 * Priority Adjustment Entry
 */
export const PriorityAdjustmentSchema = z.object({
  /** Score modifier for this priority */
  modifier: z.number(),
  /** Rationale for this adjustment */
  rationale: z.string().nullish(),
  /** Number of observations */
  observations: z.number().int().nonnegative(),
});
export type PriorityAdjustment = z.infer<typeof PriorityAdjustmentSchema>;

/**
 * Decay Configuration
 *
 * Controls how older observations lose weight over time.
 */
export const DecayConfigSchema = z.object({
  /** Whether decay is enabled */
  enabled: z.boolean().default(true),
  /** Half-life in days (observations lose 50% weight after this period) */
  half_life_days: z.number().int().positive().default(30),
});
export type DecayConfig = z.infer<typeof DecayConfigSchema>;

/**
 * Model Tracking
 *
 * Tracks which AI models have been used for pipeline executions.
 */
export const ModelTrackingSchema = z.object({
  /** Current default AI model */
  current_default: z.string(),
  /** Observation count by model ID */
  observations_by_model: z.record(z.string(), z.number().int().nonnegative()),
});
export type ModelTracking = z.infer<typeof ModelTrackingSchema>;

/**
 * Lines Changed Thresholds
 *
 * Thresholds for mapping predicted scope to size labels.
 */
export const LinesChangedThresholdsSchema = z.object({
  XS: z.number().int().positive(),
  S: z.number().int().positive(),
  M: z.number().int().positive(),
  L: z.number().int().positive(),
  XL: z.number().int().positive(),
});
export type LinesChangedThresholds = z.infer<typeof LinesChangedThresholdsSchema>;

/**
 * Prediction Accuracy Schema
 *
 * Tracks how well the complexity model predicts size labels.
 * Updated after each pipeline outcome is recorded.
 *
 * @see Issue #650 - Feedback Loop: Record Execution Outcomes
 */
export const PredictionAccuracySchema = z.object({
  /** Total number of predictions evaluated */
  total_predictions: z.number().int().nonnegative().default(0),
  /** Number of correct predictions (including adjacent-size tolerance) */
  correct_predictions: z.number().int().nonnegative().default(0),
  /** Accuracy breakdown by issue type */
  by_type: z
    .record(
      z.string(),
      z.object({
        total: z.number().int().nonnegative(),
        correct: z.number().int().nonnegative(),
      })
    )
    .default({}),
  /** Accuracy breakdown by predicted size */
  by_size: z
    .record(
      z.string(),
      z.object({
        total: z.number().int().nonnegative(),
        correct: z.number().int().nonnegative(),
      })
    )
    .default({}),
  /** Recent outcomes for idempotency checks and trend analysis (max 50) */
  recent_outcomes: z
    .array(
      z.object({
        issue_number: z.number().int().positive(),
        predicted_size: z.string(),
        actual_size_bucket: z.string(),
        was_correct: z.boolean(),
        recorded_at: z.string(),
        /** Actual lines changed — used to detect 0-line garbage entries (Issue #1198) */
        actual_lines_changed: z.number().nonnegative().optional(),
      })
    )
    .default([]),
  /**
   * Bias-safe calibration state derived from finalized post-merge survival
   * verdicts (#4152 penalize-reverts, #4153 weak-reward-survived; spike
   * #4134 §1.2). Absent until the first finalized survival record is applied.
   */
  survival_calibration: SurvivalCalibrationSchema.optional(),
});
export type PredictionAccuracy = z.infer<typeof PredictionAccuracySchema>;

/**
 * Critical Files Registry Schema
 *
 * Files whose modification significantly increases issue complexity.
 * When referenced in technical notes, each critical file bumps the
 * complexity score via the criticalFilesReferenced signal.
 *
 * @see Issue #1309 - File-aware complexity signals
 */
const CriticalFilesSchema = z.object({
  description: z.string().optional(),
  registry: z.array(z.string()),
  per_file_modifier: z.number().min(0).max(5),
  max_modifier: z.number().min(0).max(5),
});
type _CriticalFiles = z.infer<typeof CriticalFilesSchema>;

/**
 * Complete Complexity Model Schema
 *
 * The full schema for .nightgauge/complexity-model.yaml
 *
 * @see docs/ARCHITECTURE.md for design rationale
 */
export const ComplexityModelSchema = z.object({
  /** Schema version for forward compatibility */
  schema_version: z.string().default("1.0"),
  /** Last update timestamp (YYYY-MM-DD) */
  last_updated: z.string(),
  /** Bootstrap date when model was first created */
  bootstrap_date: z.string().nullish(),
  /** Source repo path when model was seeded from another repo (Issue #1323) */
  seeded_from: z.string().nullish(),
  /** Total number of observations in the model */
  total_observations: z.number().int().nonnegative(),

  /** Decay configuration */
  decay: DecayConfigSchema,

  /** AI model tracking */
  model_tracking: ModelTrackingSchema,

  /** Keyword patterns by complexity category */
  patterns: PatternCategoriesSchema,

  /** Size calibration data */
  size_calibration: SizeCalibrationSchema,

  /** Type-specific adjustments */
  type_adjustments: z.record(z.string(), TypeAdjustmentSchema),

  /** Priority-specific adjustments */
  priority_adjustments: z.record(z.string(), PriorityAdjustmentSchema),

  /** Lines changed thresholds for size mapping */
  lines_changed_thresholds: LinesChangedThresholdsSchema,

  /** Human-readable learnings and notes */
  learnings: z.array(z.string()).default([]),

  /** Prediction accuracy tracking (Issue #650) */
  prediction_accuracy: PredictionAccuracySchema.nullish(),

  /** Critical files registry for file-aware scoring (Issue #1309) */
  critical_files: CriticalFilesSchema.optional(),
});
export type ComplexityModel = z.infer<typeof ComplexityModelSchema>;

/**
 * Pipeline Outcome Schema
 *
 * Data recorded when a pipeline run completes (after PR merge).
 * Used by pr-merge skill to update the complexity model.
 */
export const PipelineOutcomeSchema = z.object({
  /** GitHub issue number */
  issue_number: z.number().int().positive(),
  /** GitHub PR number */
  pr_number: z.number().int().positive(),
  /** Size label assigned at issue creation */
  size_label: z.enum(["XS", "S", "M", "L", "XL"]),
  /** Actual lines changed in the PR */
  lines_changed: z.number().int().nonnegative(),
  /** Duration in minutes from issue pickup to PR merge */
  duration_minutes: z.number().int().nonnegative().nullish(),
  /** AI model ID used for the pipeline run */
  model_id: z.string(),
  /** Completion timestamp (ISO 8601) */
  completed_at: z.string().datetime(),
  /** Batch context for attributed lines_changed (Issue #805) */
  batch_context: z
    .object({
      epic_number: z.number().int().positive(),
      attributed_lines_changed: z.number().int().min(0),
    })
    .nullish(),
});
export type PipelineOutcome = z.infer<typeof PipelineOutcomeSchema>;

/**
 * Execution Outcome Schema
 *
 * Extended outcome with full execution metrics recorded after PR merge
 * or stage failure. Used by OutcomeRecorder for the feedback loop.
 *
 * @see Issue #650 - Feedback Loop: Record Execution Outcomes
 */
export const ExecutionOutcomeSchema = z.object({
  /** GitHub issue number (dedup key) */
  issue_number: z.number().int().positive(),
  /** Issue type label (feature, bug, docs, etc.) */
  issue_type: z.string(),
  /** GitHub PR number */
  pr_number: z.number().int().positive(),
  /** Predicted size from complexity model */
  predicted_size: z.enum(["XS", "S", "M", "L", "XL"]),
  /** Actual lines changed (additions + deletions) */
  actual_lines_changed: z.number().int().nonnegative(),
  /** Total tokens consumed across all stages */
  actual_tokens_total: z.number().int().nonnegative().nullish(),
  /** Total cost in USD */
  actual_cost_usd: z.number().nonnegative().nullish(),
  /** Total duration in milliseconds */
  actual_duration_ms: z.number().int().nonnegative().nullish(),
  /** Stages that were run */
  stages_run: z.array(z.string()),
  /** Stages that failed */
  stages_failed: z.array(z.string()),
  /** AI model ID used */
  model_used: z.string(),
  /** Whether build passed on first attempt */
  build_passed_first: z.boolean().nullish(),
  /** Whether tests passed on first attempt */
  tests_passed_first: z.boolean().nullish(),
  /** Number of PR review iterations */
  pr_review_iterations: z.number().int().nonnegative().nullish(),
  /** Patterns that matched during estimation */
  patterns_matched: z.array(z.string()).nullish(),
  /** Completion timestamp (ISO 8601) */
  completed_at: z.string(),
  /** Outcome classification */
  outcome: z.enum(["success", "failure", "partial"]),
  /** Batch context for attributed metrics (Issue #805) */
  batch_context: z
    .object({
      epic_number: z.number().int().positive(),
      attributed_lines_changed: z.number().int().min(0),
    })
    .nullish(),
  /**
   * Pipeline mode at time of execution.
   *
   * Calibration baselines should reflect the default routing behavior.
   * `efficiency`, `maximum`, and `frontier` (and the legacy `supercharge`)
   * intentionally deviate from the baseline and are excluded from
   * prediction-accuracy / type-modifier / pattern-confidence updates.
   *
   * `normal` and `supercharge` are retained for backward-compat reads of
   * historical outcome records written before #3009. New writes always emit
   * one of `efficiency` / `elevated` / `maximum` / `frontier`.
   *
   * @see Issue #2433 - Supercharge pipeline mode analytics segmentation
   * @see Issue #3009 - Replace Supercharge with explicit performance modes
   */
  pipeline_mode: z
    .enum(["normal", "supercharge", "efficiency", "elevated", "maximum", "frontier"])
    .nullish(),
});
export type ExecutionOutcome = z.infer<typeof ExecutionOutcomeSchema>;

/**
 * Matched Pattern Result
 *
 * Returned by pattern matching operations.
 */
export const MatchedPatternSchema = z.object({
  /** The pattern that matched */
  pattern: ComplexityPatternSchema,
  /** The category this pattern belongs to */
  category: z.enum(["high_complexity", "medium_complexity", "low_complexity"]),
  /** The text that matched the pattern */
  matched_text: z.string(),
});
export type MatchedPattern = z.infer<typeof MatchedPatternSchema>;

/**
 * Size Suggestion Result
 *
 * Output from the SuggestionEngine.
 */
export const SizeSuggestionSchema = z.object({
  /** Suggested size label */
  size: z.enum(["XS", "S", "M", "L", "XL"]),
  /** Confidence score (0.0-1.0) */
  confidence: z.number().min(0).max(1),
  /** Human-readable rationale for the suggestion */
  rationale: z.string(),
  /** Patterns that matched and influenced the suggestion */
  matched_patterns: z.array(z.string()),
});
export type SizeSuggestion = z.infer<typeof SizeSuggestionSchema>;
