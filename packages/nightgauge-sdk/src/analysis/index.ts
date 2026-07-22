/**
 * Analysis module - Post-hoc analysis of pipeline execution data
 *
 * Provides deterministic analysis modules for evaluating pipeline performance.
 * All analysis is rule-based with no AI interpretation.
 */

export { ModelPerformanceAnalyzer } from "./ModelPerformanceAnalyzer.js";
export { FailurePatternDetector } from "./FailurePatternDetector.js";

// V4 workflow outcome fold — the consumer-side ingestion of the canonical
// schemaVersion-4 WorkflowEvent node tree for the learning loop (Issue #3915)
export {
  foldWorkflowOutcome,
  foldWorkflowOutcomes,
  summarizeWorkflowOutcomes,
} from "./WorkflowOutcomeAnalyzer.js";
export type {
  WorkflowOutcome,
  WorkflowPhaseOutcome,
  WorkflowCalibrationSignal,
} from "./WorkflowOutcomeAnalyzer.js";
export { TokenEfficiencyAnalyzer } from "./TokenEfficiencyAnalyzer.js";
export {
  AutoModelSelector,
  MODEL_TIER_ORDER,
  DEFAULT_MODEL_ENVELOPE,
  clampTier,
} from "./AutoModelSelector.js";
export type { ModelEnvelope } from "./AutoModelSelector.js";
export { AutoProviderRouter } from "./AutoProviderRouter.js";
export {
  DEFAULT_AUTO_ROUTER_WEIGHTS,
  DEFAULT_AUTO_ROUTER_CONFIDENCE_THRESHOLD,
  HYBRID_DOMINANCE_THRESHOLD,
  WORKFLOW_SUBSCORE_WEIGHT,
} from "./auto-router-types.js";
export type {
  AutoRouterContext,
  AutoRouterDecision,
  AutoRouterHistoryEntry,
  AutoRouterMode,
  AutoRouterWeights,
  RouterExecutionAdapter,
  RouterStageCategory,
} from "./auto-router-types.js";
export { ExperimentManager } from "./ExperimentManager.js";
export { ExperimentEvaluator } from "./ExperimentEvaluator.js";
export { mapSourceToTestFiles, isCrossCuttingChange } from "./testFileMapper.js";

// Source-to-Test Dependency Graph (Issue #1970)
export {
  buildSourceToTestGraph,
  getAffectedTests,
  extractImports,
  serializeGraph,
  deserializeGraph,
  loadGraph,
  saveGraph,
} from "./SourceToTestGraph.js";
export type { DependencyGraph, BuildOptions, GraphQueryResult } from "./graph-types.js";
export { DependencyGraphSchema, BuildOptionsSchema } from "./graph-types.js";

// Change Impact Analyzer (Issue #1971)
export { analyzeImpact, analyzeImpactFromDiff, parseDiff } from "./ChangeImpactAnalyzer.js";
export type {
  ImpactAnalysisResult,
  AffectedTest,
  AffectedSource,
  DiffEntry,
  ImpactLevel,
  ConfidenceLevel,
  FileChangeStatus,
  ChangeImpactAnalyzerConfig,
  ParsedChangeImpactAnalyzerConfig,
  RegressionTriggerType,
  RegressionTriggerResult,
  RegressionNotTriggered,
  RegressionTriggerEvaluation,
  RegressionTriggerRuleConfig,
  RegressionTriggerConfig,
} from "./change-impact-types.js";
export {
  ChangeImpactAnalyzerConfigSchema,
  DEFAULT_INFRASTRUCTURE_PATTERNS,
  DEFAULT_DEPENDENCY_PATTERNS,
  DEFAULT_BUILD_CONFIG_PATTERNS,
  DEFAULT_SHARED_TYPES_PATTERNS,
  DEFAULT_TEST_INFRASTRUCTURE_PATTERNS,
  DEFAULT_CI_CONFIG_PATTERNS,
  RegressionTriggerConfigSchema,
} from "./change-impact-types.js";

// Regression Trigger Evaluator (Issue #1974)
export {
  evaluateRegressionTriggers,
  evaluateLowConfidenceTrigger,
  matchesPattern,
  DEFAULT_REGRESSION_TRIGGER_CONFIG,
} from "./RegressionTriggerEvaluator.js";

// "What to Test" PR Section Generator (Issue #1972)
export { generateWhatToTestSection } from "./WhatToTestGenerator.js";
export type { WhatToTestOptions, WhatToTestSection } from "./what-to-test-types.js";

export type {
  CostHealthContext,
  CostPerSuccessContext,
  ModelStageHistory,
  ModelSelectionResult,
  EffortDerivationResult,
  IssueMetadata,
  ModelTier,
  ComplexityLabel,
  ClaudeEffort,
  StageCategory,
  AutoModelSelectorConfig,
  RoutingIssueType,
  TypeStageOverride,
  StageCostEstimate,
  PipelineCostEstimate,
} from "./AutoModelSelector.js";

export type {
  ExecutionHistoryRecord,
  ExecutionHistoryRunRecordFlat,
  ExecutionHistoryRecordExtended,
  ModelIdentifier,
  ModelStagePerformance,
  StageModelComparison,
  SuggestionType,
  RoutingRecommendation,
  ModelRoutingAnalysis,
  AutoSelectionStageOutcome,
  UnderRoutingPattern,
  OverRoutingPattern,
  ThresholdRecommendation,
  AutoSelectionAnalysis,
  ModelCostRate,
  ModelAnalyzerConfig,
  CostRates,
  TokenEfficiencyConfig,
  WasteSeverity,
  WasteCategory,
  WastePattern,
  TokenEfficiencyAnalysis,
  RecommendationAction,
} from "./types.js";

export { DEFAULT_MODEL_COST_RATES } from "./types.js";

export type {
  FailureCategory,
  FailureSeverity,
  FailurePattern,
  FailureTaxonomy,
  RootCauseCorrelation,
  TrendDirection,
  FailureFinding,
  FailureAnalysisResult,
  FailureDetectorConfig,
} from "./failureTypes.js";

export type {
  ExperimentConfig,
  ExperimentAssignment,
  ExperimentOutcome,
  ExperimentReport,
  ExperimentGroup,
  GroupMetrics,
  // Evaluation types (Issue #1396)
  ExperimentEvaluationStatus,
  ExperimentEvaluationResult,
  ExperimentConclusion,
} from "./experiment-types.js";

export {
  ExperimentConfigSchema,
  ExperimentAssignmentSchema,
  ExperimentOutcomeSchema,
  ExperimentGroupSchema,
} from "./experiment-types.js";

export { DEFAULT_RECURRING_THRESHOLD } from "./failureTypes.js";

export {
  detectFailuresByCategory,
  detectRecurringFailures,
  correlateRootCauses,
  computeFailureTrends,
  generateRecommendations,
  computeLinearTrend,
} from "./failurePatterns.js";

// Failure Category Classifier (Issue #1260) + Terminal Kind Classifier (Issue #3001)
export {
  classifyFailureCategory,
  classifyTerminalKind,
  FAILURE_CATEGORY_WEIGHTS,
} from "./health/index.js";
export type {
  FailureCategory as FailureWeightCategory,
  TerminalFailureKind,
} from "./health/index.js";

// Health Analysis Engine (Issue #1101)
export { HealthAnalysisEngine } from "./health/index.js";
export { crossReference } from "./health/index.js";
export {
  analyzeTokenEconomics,
  analyzeCostHealth,
  analyzeStageEffectiveness,
  analyzeModelRouting,
  analyzeReliability,
  analyzeLearningEffectiveness,
  analyzePipelineVelocity,
  analyzeSkillDrift,
  ALL_DIMENSIONS,
  DEFAULT_HEALTH_CONFIG,
  DEFAULT_CACHE_THRESHOLD,
  getHealthStatus,
} from "./health/index.js";
export type {
  HealthDimension,
  Severity as HealthSeverity,
  HealthStatus,
  Confidence as HealthConfidence,
  TrendDirection as HealthTrendDirection,
  Finding as HealthFinding,
  PeriodComparison,
  DimensionResult,
  CrossReference,
  HealthAnalysisConfig,
  CacheThresholdConfig,
  HealthAnalysisResult,
  HealthAnalysisInput,
  HealthScoreEntry,
  SelfTuningEntry,
  ExperimentEntry,
  HealthReportEntry,
} from "./health/index.js";

// Finding-to-Issue Engine (Issue #1102)
export { FindingToIssueEngine } from "./health/index.js";
export {
  SEVERITY_ORDER,
  severityMeetsThreshold,
  findingToLabels,
  formatIssueTitle,
  formatIssueBody,
  formatDryRunPreview,
  DEFAULT_FINDING_TO_ISSUE_CONFIG,
} from "./health/index.js";
export type {
  FindingToIssueConfig,
  GeneratedIssue,
  EpicGroup,
  FindingToIssueResult,
} from "./health/index.js";

// Recommendation Tracking (Issue #1103)
export { RecommendationTracker } from "./health/index.js";

// Health Report Generator (Issue #1105)
export {
  HealthReportGenerator,
  type HealthReportOptions,
  type WriteReportsResult,
} from "./health/index.js";
export { HealthReportSchema } from "./health/index.js";
export type { HealthReport } from "./health/index.js";
export type {
  RecommendationHistoryEntry,
  RecurringFinding,
  RecommendationEffectivenessScore,
  RecommendationReport,
} from "./health/index.js";

// Skill Amendment Detection
export { SkillAmendmentDetector } from "./SkillAmendmentDetector.js";
export type {
  ValidationErrorRecord,
  SkillAmendmentProposal,
  SkillAmendmentAnalysisResult,
} from "./skill-amendment-types.js";

// Skill Effectiveness Tracking (Issue #1414)
export { SkillEffectivenessAnalyzer } from "./SkillEffectivenessAnalyzer.js";
export type {
  SkillChangeRecord,
  SkillEffectivenessEntry,
  SkillEffectivenessAnalysisResult,
} from "./skill-effectiveness-types.js";

// Selective Test Metrics — Validation & Cost Tracking (Issue #1975)
export { SelectiveTestMetricsCollector } from "./SelectiveTestMetricsCollector.js";
export {
  SelectiveTestEffectivenessAnalyzer,
  type SelectiveTestEffectivenessAnalyzerConfig,
} from "./SelectiveTestEffectivenessAnalyzer.js";
export { EscapedDefectDetector } from "./EscapedDefectDetector.js";
export type {
  SelectiveTestMetricRecord,
  GraphGapRecord,
  SelectiveTestEffectivenessResult,
} from "./selective-test-metrics-types.js";
export {
  SelectiveTestMetricRecordSchema,
  GraphGapRecordSchema,
} from "./selective-test-metrics-types.js";

// Skill Self-Assessment (Issue #1986)
export {
  SkillSelfAssessmentSynthesizer,
  DEFAULT_RETENTION_DAYS,
} from "./SkillSelfAssessmentSynthesizer.js";
export {
  AssessmentRecordSchema,
  FrictionRecordSchema,
  FrictionTypeSchema,
  FrictionSeveritySchema,
  SkillImprovementProposalSchema,
  SynthesisResultSchema,
} from "./self-assessment-types.js";
export type {
  AssessmentRecord,
  FrictionRecord,
  FrictionType,
  FrictionSeverity,
  SkillImprovementProposal,
  SynthesisResult,
} from "./self-assessment-types.js";
