/**
 * Health Analysis Module - Multi-Dimensional Pipeline Health Assessment
 *
 * @see Issue #1101 - Multi-Dimensional Health Analysis Engine
 */

export { HealthAnalysisEngine } from "./HealthAnalysisEngine.js";
export { HealthTrendsWriter } from "./HealthTrendsWriter.js";
export { FindingToIssueEngine } from "./FindingToIssueEngine.js";
export { RecommendationTracker } from "./RecommendationTracker.js";
export {
  HealthReportGenerator,
  type HealthReportOptions,
  type WriteReportsResult,
} from "./HealthReportGenerator.js";
export { HealthReportSchema, type HealthReport } from "./reportSchema.js";
export { crossReference } from "./crossReferencer.js";

export {
  SEVERITY_ORDER,
  severityMeetsThreshold,
  findingToLabels,
  severityToPriorityLabel,
  severityToSizeLabel,
  severityToTypeLabel,
  dimensionToComponentLabel,
} from "./severityMapping.js";

export {
  formatIssueTitle,
  formatIssueBody,
  formatEpicTitle,
  formatEpicBody,
  formatDryRunPreview,
} from "./issueTemplates.js";

export {
  computePercentile,
  computeTrend,
  isStatisticallySignificant,
  computeChangePercent,
  hasEnoughData,
  buildPeriodComparison,
  mean,
  standardDeviation,
  clamp,
} from "./statistics.js";

export {
  classifyFailureCategory,
  classifyTerminalKind,
  FAILURE_CATEGORY_WEIGHTS,
  type FailureCategory,
  type TerminalFailureKind,
} from "./failureClassifier.js";

export { analyzeTokenEconomics } from "./dimensions/tokenEconomics.js";
export { analyzeCostHealth } from "./dimensions/costHealth.js";
export { analyzeStageEffectiveness } from "./dimensions/stageEffectiveness.js";
export { analyzeModelRouting } from "./dimensions/modelRouting.js";
export { analyzeReliability } from "./dimensions/reliability.js";
export { analyzeLearningEffectiveness } from "./dimensions/learningEffectiveness.js";
export { analyzePipelineVelocity } from "./dimensions/pipelineVelocity.js";
export { analyzeSkillDrift } from "./dimensions/skillDrift.js";

export type {
  HealthDimension,
  HealthTrendEntry,
  HealthTrendsReadOptions,
  Severity,
  HealthStatus,
  Confidence,
  TrendDirection,
  Finding,
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
  FindingToIssueConfig,
  GeneratedIssue,
  EpicGroup,
  FindingToIssueResult,
  RecommendationHistoryEntry,
  RecurringFinding,
  RecommendationEffectivenessScore,
  RecommendationReport,
} from "./types.js";

export {
  ALL_DIMENSIONS,
  DEFAULT_HEALTH_CONFIG,
  DEFAULT_CACHE_THRESHOLD,
  DEFAULT_FINDING_TO_ISSUE_CONFIG,
  getHealthStatus,
} from "./types.js";
