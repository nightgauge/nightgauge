/**
 * Context Schema Exports
 *
 * Zod schemas for validating pipeline context files.
 * All schemas match the specifications in docs/CONTEXT_ARCHITECTURE.md
 */

export { IssueContextSchema, type IssueContext } from "./issue.js";
export { PlanningContextSchema, type PlanningContext } from "./planning.js";
export { ACReconcileContextSchema, type ACReconcileContext } from "./ac-reconcile.js";
export { DevContextSchema, type DevContext } from "./dev.js";
export {
  ValidateContextSchema,
  type ValidateContext,
  type DeadCodeWarning,
  type PreexistingFailure,
  type SkippedPhase,
  type MobileMcpResult,
  type MobileMcpSpecResult,
} from "./validate.js";
export { PRContextSchema, type PRContext, type RetrospectiveFeedback } from "./pr.js";
export {
  PipelineFeedbackSignalTypeSchema,
  PipelineStageSchema,
  PipelineFeedbackSignalSchema,
  PipelineFeedbackSchema,
  FeedbackContextSchema,
  ConflictFileSchema,
  ConflictContextSchema,
  ReviewerSignalTypeSchema,
  ReviewerSignalSchema,
  type PipelineFeedbackSignalType,
  type PipelineStage,
  type PipelineFeedbackSignal,
  type PipelineFeedback,
  type FeedbackContext,
  type ConflictFile,
  type ConflictContext,
  type ReviewerSignalType,
  type ReviewerSignal,
} from "./feedback.js";
export {
  ComplexityModelSchema,
  ComplexityPatternSchema,
  SizeCalibrationSchema,
  PipelineOutcomeSchema,
  ExecutionOutcomeSchema,
  PredictionAccuracySchema,
  MatchedPatternSchema,
  SizeSuggestionSchema,
  type ComplexityModel,
  type ComplexityPattern,
  type SizeCalibration,
  type PipelineOutcome,
  type ExecutionOutcome,
  type PredictionAccuracy,
  type MatchedPattern,
  type SizeSuggestion,
} from "./complexity-model.js";
export {
  SurvivalVerdictSchema,
  SurvivalRecordSchema,
  SurvivalCalibrationSchema,
  type SurvivalVerdict,
  type SurvivalRecord,
  type SurvivalCalibration,
} from "./survival.js";
export {
  KnowledgeTypeSchema,
  KnowledgeEntrySchema,
  KnowledgeIndexSchema,
  RepoTopicTypeSchema,
  type KnowledgeType,
  type KnowledgeEntry,
  type KnowledgeIndex,
  type RepoTopicType,
} from "./knowledge.js";
export {
  EpicContextSchema,
  SubIssueFindingsSchema,
  type EpicContext,
  type SubIssueFindings,
} from "./epic-context.js";
export {
  CreationManifestSchema,
  CreationManifestEntrySchema,
  ManifestBlockerRefSchema,
  ManifestSpikeArtifactSchema,
  ManifestIssueTypeSchema,
  ManifestPrioritySchema,
  ManifestSizeSchema,
  ManifestStatusSchema,
  type CreationManifest,
  type CreationManifestEntry,
  type ManifestBlockerRef,
  type ManifestSpikeArtifact,
  type ManifestIssueType,
  type ManifestPriority,
  type ManifestSize,
  type ManifestStatus,
} from "./creation-manifest.js";
export {
  RunStateSchema,
  RunStateLifecycleSchema,
  RunStageSchema,
  RunAttemptSchema,
  newRunState,
  type RunState,
  type RunStateLifecycle,
  type RunStage,
  type RunAttempt,
} from "./run-state.js";
export {
  PatternTypeSchema,
  DiscoveredPatternSchema,
  SimilarIssueSchema,
  PatternClassificationsSchema,
  PatternMiningResultSchema,
  type PatternType,
  type DiscoveredPattern,
  type SimilarIssue,
  type PatternClassifications,
  type PatternMiningResult,
  /** @deprecated Use PatternMiningResultSchema instead */
  CompassResultSchema,
  /** @deprecated Use PatternMiningResult instead */
  type CompassResult,
} from "./pattern-mining.js";

/**
 * Schema version constant for all context files
 */
export const SCHEMA_VERSION = "1.0" as const;

/**
 * Union type of all context types
 */
import type { IssueContext } from "./issue.js";
import type { PlanningContext } from "./planning.js";
import type { DevContext } from "./dev.js";
import type { ValidateContext } from "./validate.js";
import type { PRContext } from "./pr.js";
import type { FeedbackContext } from "./feedback.js";
import type { KnowledgeEntry, KnowledgeIndex } from "./knowledge.js";
import type { EpicContext } from "./epic-context.js";

export type ContextType =
  | IssueContext
  | PlanningContext
  | DevContext
  | ValidateContext
  | PRContext
  | FeedbackContext
  | KnowledgeEntry
  | KnowledgeIndex
  | EpicContext;
