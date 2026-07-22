/**
 * Services Exports
 *
 * Services provide core business logic for the Nightgauge pipeline.
 */

export { ComplexityModelService, ModelValidationError } from "./ComplexityModelService.js";

export {
  SuggestionEngine,
  type IssueType,
  type Priority,
  type SizeLabel,
  type ScoringSignals,
} from "./SuggestionEngine.js";

export { EpicEstimator, type EpicEstimate, type SubIssueEstimate } from "./EpicEstimator.js";

export {
  OutcomeRecorder,
  type OutcomeRecordResult,
  type SurvivalCalibrationApplyResult,
} from "./OutcomeRecorder.js";

export {
  FeedbackLearningService,
  type RecordUnderestimationResult,
  type ProcessReviewerFeedbackResult,
} from "./FeedbackLearningService.js";

export {
  CalibrationService,
  type CalibrationTable,
  type BucketCalibration,
  type EstimateValidation,
  type CalibrationInput,
  type SizeBucket,
  type CalibrationMode,
} from "./CalibrationService.js";

// Public API: scaffoldForIssue, create, read, update, list, search, generateIndex, pruneEmpty, generateSlug, isSubstantive
// @internal (not public API, but exported for testability): generatePRD, generateDecisionsTemplate, contentIsSubstantive
export {
  KnowledgeService,
  type ScaffoldResult,
  type KnowledgeConfig,
  type KnowledgeReadResult,
  type KnowledgeListEntry,
  type KnowledgeSearchResult,
  type KnowledgeListFilter,
  type KnowledgeRegenResult,
  type RepoTopicResult,
} from "./KnowledgeService.js";
