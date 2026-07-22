/**
 * @nightgauge/sdk
 *
 * Nightgauge SDK - Programmatic pipeline orchestration
 * using the Claude Agent SDK.
 *
 * @example
 * ```typescript
 * import { query } from '@anthropic-ai/claude-agent-sdk';
 * import { PipelineOrchestrator } from '@nightgauge/sdk';
 *
 * const orchestrator = new PipelineOrchestrator(query);
 *
 * orchestrator.events.on('stage:complete', (event) => {
 *   console.log(`Completed ${event.stage}`);
 * });
 *
 * const result = await orchestrator.run(42);
 * console.log(`Total cost: $${result.usage.costUsd.toFixed(4)}`);
 * ```
 *
 * @see docs/ARCHITECTURE.md for architectural overview
 * @see docs/CONTEXT_ARCHITECTURE.md for context file specifications
 *
 * @packageDocumentation
 */

// Core Orchestrator
export {
  PipelineOrchestrator,
  DEFAULT_STAGES,
  APPROVAL_STAGES,
  type PipelineConfig,
  type PipelineResult,
  type StageResult,
  type ExecutorSelection,
} from "./orchestrator/PipelineOrchestrator.js";

export {
  StageExecutor,
  buildStagePrompt,
  loadStageSkill,
  StageTimeoutError,
  type StageExecutorOptions,
  type LoadedStageSkill,
  type SDKMessage,
  type SDKQueryFunction,
  type SDKQueryOptions,
} from "./orchestrator/StageExecutor.js";

// Context Management
export {
  ContextManager,
  ContextNotFoundError,
  ContextValidationError,
  atomicWriteJSON,
} from "./context/ContextManager.js";

// RunState — durable pipeline lifecycle (Issue #3238)
export { RunStateManager, uuidV7, type ResumeDetection } from "./context/RunStateManager.js";

// Pipeline state errors — structured contract for the recovery UX (Gap 2).
export {
  PipelineStateError,
  ContextSchemaError,
  WorktreeMissing,
  ConcurrentRunRefused,
  SchemaVersionMismatch,
  MissingInputFile,
  RunStateMissing,
  isSchemaCompatible,
  type RecoveryAction,
  type RecoveryErrorKind,
  type RecoveryRunState,
  type RecoveryRequiredPayload,
} from "./errors/PipelineStateErrors.js";

// Stage graph derived from skill manifests (Issue #3239).
export {
  StageGraph,
  loadStageGraphFromManifests,
  parseStageManifest,
  DEV_FALLBACK_PRODUCERS,
  type StageManifest,
  type StageProducer,
  type ParsedStageManifest,
} from "./orchestrator/StageGraph.js";

export {
  GeminiContextGenerator,
  type GeminiContextOptions,
  type GeminiContextConfig,
} from "./context/GeminiContextGenerator.js";

export {
  CodexContextGenerator,
  upsertManagedBlock,
  stripManagedBlock,
  CODEX_MANAGED_BEGIN,
  CODEX_MANAGED_END,
  type CodexContextOptions,
  type CodexContextConfig,
} from "./context/CodexContextGenerator.js";

export {
  CodexMcpProvisioner,
  resolveCodexHome,
  type CodexMcpOptions,
  type CodexMcpResult,
  type CodexMcpConfig,
} from "./context/CodexMcpProvisioner.js";

export {
  readPipelineMcpServers,
  toCodexMcpServer,
  computeNextCodexConfig,
  buildManagedMcpBlockInner,
  findUserDefinedServerNames,
  upsertManagedMcpBlock,
  stripManagedMcpBlock,
  hasManagedMcpBlock,
  CODEX_MCP_MANAGED_BEGIN,
  CODEX_MCP_MANAGED_END,
  type PipelineMcpServer,
  type CodexMcpServer,
} from "./context/codexMcpConfig.js";

export { systemPromptPresetForAdapter } from "./orchestrator/providerSteering.js";
export {
  BEHAVIORAL_PREAMBLE,
  isHaikuModelId,
  withBehavioralPreamble,
} from "./orchestrator/behavioralPreamble.js";

// Event System — the EventBus is the in-process WorkflowEventSink carrying the
// canonical node-tree WorkflowEvent contract (run / phase / agent / judge).
// PipelineStage is the stage-name enum, decoupled from the event shape.
export {
  EventBus,
  PipelineRunEmitter,
  PIPELINE_STAGE_ORDER,
  type PipelineStage,
  type WorkflowNodeHandler,
  type WorkflowAnyHandler,
} from "./events/EventBus.js";

// Phase Registry
export {
  PHASE_REGISTRY,
  getPhaseTotal,
  getPhaseIndex,
  formatPhaseMarker,
  parsePhaseMarker,
  parsePhaseMarkers,
  type ParsedPhaseMarker,
  type StagePhaseDefinition,
  type ExecutionStage,
} from "./events/phaseRegistry.js";

// Phase Inference (deterministic phase progress from tool activity — Issue #3760)
export { createPhaseInference, type PhaseInference } from "./events/phaseInference.js";

// Lifecycle trace recorder — the "sdk" producer for the per-run decision
// trace (ADR 013, Issue #180)
export {
  TraceRecorder,
  TRACE_SCHEMA_VERSION,
  TRACE_PRODUCER_SDK,
  type TraceEvent,
  type TraceEventKind,
  type TraceRecorderOptions,
} from "./events/traceRecorder.js";

// Workflow Orchestration contract — canonical schemaVersion-4 event tree,
// WorkflowSpec plan, and the WorkflowEventSink write boundary (epic #3899, #3904)
export {
  WORKFLOW_SCHEMA_VERSION,
  isWorkflowRun,
  isSubAgentNode,
  isJudgeVerdict,
  zeroUsage,
  CLAUDE_CEILING,
  FANOUT_CEILING,
  ABSOLUTE_CEILING,
  plannedAgentCount,
  validateWorkflowSpec,
  ArrayWorkflowEventSink,
  createSeqCounter,
  DEFAULT_ORCHESTRATION_CONFIG,
  DISABLE_WORKFLOWS_ENV,
  resolveOrchestrationConfig,
  prefersNativeOffload,
  type WorkflowSchemaVersion,
  type OrchestrationCapability,
  type WorkflowNodeKind,
  type WorkflowNodeStatus,
  type WorkflowTerminalKind,
  type WorkflowJudgeVerdict,
  type WorkflowAgentUsage,
  type WorkflowRun,
  type WorkflowPhase,
  type SubAgentNode,
  type JudgeVerdict,
  type WorkflowNode,
  type WorkflowEvent,
  type WorkflowConcurrencyCeiling,
  type WorkflowAgentSpec,
  type WorkflowJudgeSpec,
  type WorkflowPhaseSpec,
  type WorkflowSpec,
  type WorkflowEventSink,
  type OrchestrationConfig,
  type ResolvedOrchestrationConfig,
  type OrchestrationStage,
  type PreferNativeOffloadMap,
  runSdkFanout,
  AgentExecutionError,
  type AgentExecutionResult,
  type JudgeExecutionResult,
  type WorkflowExecutorBindings,
  type WorkflowPhaseSummary,
  type WorkflowRunSummary,
  type RunSdkFanoutOptions,
  makeSdkFanoutBindings,
  adapterEphemeralExec,
  parseJudgeOutcome,
  EphemeralTimeoutError,
  type EphemeralExec,
  type EphemeralExecResult,
  type SdkFanoutBindingsOptions,
  evaluateQuotaGate,
  gateWorkflowFanout,
  DEFAULT_LARGE_FANOUT_THRESHOLD,
  type WorkflowQuotaState,
  type QuotaStateProvider,
  type QuotaGateAction,
  type QuotaGateDecision,
  parseOrchestrationFrontmatter,
  type OrchestrationFrontmatterContext,
} from "./cli/workflow/index.js";

// WorkflowExecutor — backend resolution, budget enforcement, durable per-node
// journal + cross-process resume + sandboxed outputRef replay (epic #3899, #3908)
export {
  WorkflowExecutor,
  OrchestrationDisabledError,
  MAX_OUTPUT_REF_BYTES,
  DENY_NATIVE_PREFLIGHT,
  SYSTEM_CLOCK,
  resolveBackend,
  clampSpecCeiling,
  sanitizeOutputRef,
  replayJournal,
  isRunLive,
  fitsUnderAbsoluteCeiling,
  createNodeJournalFs,
  type VersionPreflight,
  type WorkflowBackend,
  type JournalFs,
  type Clock,
  type JournalRecord,
  type WorkflowExecutorDeps,
  type WorkflowExecutionResult,
} from "./orchestrator/WorkflowExecutor.js";

// Token Tracking
export {
  TokenTracker,
  type StageUsage,
  type TotalUsage,
  type SDKUsage,
  type SDKModelUsage,
  type SDKResultMessage,
  type WorkflowNodeUsage,
} from "./tracking/TokenTracker.js";

// PTC Metrics (Issue #1071)
export {
  aggregatePTCMetrics,
  type ProgrammaticToolMetrics,
  type PTCStageUsage,
} from "./tracking/PTCMetrics.js";

// Context Schemas
export {
  // Schema objects
  IssueContextSchema,
  PlanningContextSchema,
  ACReconcileContextSchema,
  DevContextSchema,
  ValidateContextSchema,
  PRContextSchema,
  // Complexity Model Schemas
  ComplexityModelSchema,
  ComplexityPatternSchema,
  SizeCalibrationSchema,
  PipelineOutcomeSchema,
  ExecutionOutcomeSchema,
  PredictionAccuracySchema,
  MatchedPatternSchema,
  SizeSuggestionSchema,
  // Types
  type IssueContext,
  type PlanningContext,
  type ACReconcileContext,
  type DevContext,
  type ValidateContext,
  type PRContext,
  type RetrospectiveFeedback,
  type ContextType,
  // Feedback Schemas (Issue #1341, #1342, #4072)
  PipelineFeedbackSignalTypeSchema,
  PipelineFeedbackSignalSchema,
  PipelineFeedbackSchema,
  FeedbackContextSchema,
  ConflictFileSchema,
  ConflictContextSchema,
  type PipelineFeedbackSignalType,
  type PipelineFeedbackSignal,
  type PipelineFeedback,
  type FeedbackContext,
  type ConflictFile,
  type ConflictContext,
  // Complexity Model Types
  type ComplexityModel,
  type ComplexityPattern,
  type SizeCalibration,
  type PipelineOutcome,
  type ExecutionOutcome,
  type PredictionAccuracy,
  type MatchedPattern,
  type SizeSuggestion,
  // Survival Outcome Model Schemas (#4151/#4152/#4153)
  SurvivalVerdictSchema,
  SurvivalRecordSchema,
  SurvivalCalibrationSchema,
  type SurvivalVerdict,
  type SurvivalRecord,
  type SurvivalCalibration,
  // Constants
  SCHEMA_VERSION,
  // Knowledge Schemas (Issue #1674)
  KnowledgeTypeSchema,
  KnowledgeEntrySchema,
  KnowledgeIndexSchema,
  RepoTopicTypeSchema,
  type KnowledgeType,
  type KnowledgeEntry,
  type KnowledgeIndex,
  type RepoTopicType,
  // Epic Context Schemas (Issue #2404)
  EpicContextSchema,
  SubIssueFindingsSchema,
  type EpicContext,
  type SubIssueFindings,
  // Pattern Mining Schemas (Issue #20)
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
  /** @deprecated Use PatternMiningResultSchema */
  CompassResultSchema,
  /** @deprecated Use PatternMiningResult */
  type CompassResult,
} from "./context/schemas/index.js";

// Tools (Issue #1066)
export {
  ToolRegistry,
  AllowedCallerSchema,
  ToolTypeSchema,
  CustomToolDefinitionSchema,
  ToolEntrySchema,
  type AllowedCaller,
  type ToolType,
  type CustomToolDefinition,
  type ToolEntry,
  // Pipeline Tool Definitions (Issue #1068)
  RUN_BUILD_TOOL,
  RUN_LINT_TOOL,
  RUN_TESTS_TOOL,
  RUN_TYPECHECK_TOOL,
  READ_CONTEXT_FILE_TOOL,
  WRITE_CONTEXT_FILE_TOOL,
  LIST_CONTEXT_FILES_TOOL,
  GIT_DIFF_SUMMARY_TOOL,
  GIT_LOG_STRUCTURED_TOOL,
  GIT_STATUS_STRUCTURED_TOOL,
  VALIDATION_TOOLS,
  CONTEXT_TOOLS,
  GIT_TOOLS,
  getAllPipelineToolDefinitions,
  registerPipelineTools,
  // PTC - Programmatic Tool Calling (Issue #1069)
  PTCExecutor,
  PTCValidationRunner,
  isPTCAvailable,
  createValidationHandlers,
  type PTCResult,
  type PTCExecutorOptions,
  type ValidationResult,
  type DevContextInput,
  type PTCValidationRunnerOptions,
  type ToolHandler,
  type ToolResult,
  RunBuildHandler,
  RunLintHandler,
  RunTestsHandler,
  RunTypecheckHandler,
  // Selective Test Runner (Issue #1973)
  SelectiveTestRunner,
  buildVitestArgs,
  type SelectiveTestRunnerConfig,
  type SelectiveTestResult,
  // Integration Test Gate (Issue #2909)
  classifyIntegrationOutcome,
  detectIntegrationRequirement,
  evaluateGate,
  type ClassifiedIntegrationOutcome,
  type IntegrationDetectionSignals,
  type IntegrationGateDecision,
  type IntegrationGateMode,
  type IntegrationRequirement,
  type IntegrationRunOutcome,
} from "./tools/index.js";

// Services
export {
  ComplexityModelService,
  ModelValidationError,
  SuggestionEngine,
  EpicEstimator,
  OutcomeRecorder,
  FeedbackLearningService,
  type IssueType,
  type Priority,
  type SizeLabel,
  type ScoringSignals,
  type EpicEstimate,
  type SubIssueEstimate,
  type OutcomeRecordResult,
  type SurvivalCalibrationApplyResult,
  type RecordUnderestimationResult,
  CalibrationService,
  type CalibrationTable,
  type BucketCalibration,
  type EstimateValidation,
  type CalibrationInput,
  type SizeBucket,
  type CalibrationMode,
  KnowledgeService,
  type ScaffoldResult,
  type KnowledgeConfig,
  type KnowledgeReadResult,
  type KnowledgeListEntry,
  type KnowledgeSearchResult,
  type KnowledgeListFilter,
  type KnowledgeRegenResult,
  type RepoTopicResult,
} from "./services/index.js";

// Pipeline Stages
export {
  // Base stage
  BaseStage,
  type StageConfig,
  type StageExecuteOptions as StageExecOptions,
  type StageExecuteResult,
  // Concrete stages
  IssuePickupStage,
  FeaturePlanningStage,
  FeatureDevStage,
  PRCreateStage,
} from "./stages/index.js";

// CLI Utilities (for programmatic CLI usage)
export {
  loadConfigFromEnv,
  mergeConfig,
  validateConfig,
  ConfigValidationError,
  DEFAULT_CONFIG,
  type CLIConfig,
} from "./cli/config.js";

export {
  OutputFormatter,
  createFormatter,
  type OutputFormat,
  type LogLevel,
  type PipelineJSONOutput,
  type StageJSONOutput,
  type StatusJSONOutput,
} from "./cli/output.js";

export { EXIT_CODES } from "./cli/commands/run.js";

// Analysis
export {
  ModelPerformanceAnalyzer,
  FailurePatternDetector,
  foldWorkflowOutcome,
  foldWorkflowOutcomes,
  summarizeWorkflowOutcomes,
  TokenEfficiencyAnalyzer,
  AutoModelSelector,
  AutoProviderRouter,
  DEFAULT_AUTO_ROUTER_WEIGHTS,
  DEFAULT_AUTO_ROUTER_CONFIDENCE_THRESHOLD,
  HYBRID_DOMINANCE_THRESHOLD,
  WORKFLOW_SUBSCORE_WEIGHT,
  type AutoRouterContext,
  type AutoRouterDecision,
  type AutoRouterHistoryEntry,
  type AutoRouterMode,
  type AutoRouterWeights,
  type RouterExecutionAdapter,
  type RouterStageCategory,
  ExperimentManager,
  ExperimentEvaluator,
  DEFAULT_RECURRING_THRESHOLD,
  detectFailuresByCategory,
  detectRecurringFailures,
  correlateRootCauses,
  computeFailureTrends,
  generateRecommendations,
  computeLinearTrend,
  type ExecutionHistoryRecord,
  type ExecutionHistoryRunRecordFlat,
  type ExecutionHistoryRecordExtended,
  type ModelIdentifier,
  type ModelStagePerformance,
  type StageModelComparison,
  type SuggestionType,
  type RoutingRecommendation,
  type ModelRoutingAnalysis,
  type WorkflowOutcome,
  type WorkflowPhaseOutcome,
  type WorkflowCalibrationSignal,
  type ThresholdRecommendation,
  type AutoSelectionAnalysis,
  type ModelCostRate,
  type ModelAnalyzerConfig,
  type CostRates,
  type TokenEfficiencyConfig,
  type WasteSeverity,
  type WasteCategory,
  type WastePattern,
  type TokenEfficiencyAnalysis,
  type FailureCategory,
  type FailureSeverity,
  type FailurePattern,
  type FailureTaxonomy,
  type RootCauseCorrelation,
  type TrendDirection,
  type FailureFinding,
  type FailureAnalysisResult,
  type FailureDetectorConfig,
  type CostHealthContext,
  type CostPerSuccessContext,
  type ModelStageHistory,
  type ModelSelectionResult,
  type EffortDerivationResult,
  type IssueMetadata,
  type ModelTier,
  type ModelEnvelope,
  MODEL_TIER_ORDER,
  DEFAULT_MODEL_ENVELOPE,
  clampTier,
  type ComplexityLabel,
  type ClaudeEffort,
  type StageCategory,
  type AutoModelSelectorConfig,
  type RoutingIssueType,
  type TypeStageOverride,
  type StageCostEstimate,
  type PipelineCostEstimate,
  type ExperimentConfig,
  type ExperimentAssignment,
  type ExperimentOutcome,
  type ExperimentReport,
  type ExperimentGroup,
  type GroupMetrics,
  // Evaluation types (Issue #1396)
  type ExperimentEvaluationStatus,
  type ExperimentEvaluationResult,
  type ExperimentConclusion,
  ExperimentConfigSchema,
  ExperimentAssignmentSchema,
  ExperimentOutcomeSchema,
  ExperimentGroupSchema,
  // Failure Category Classifier (Issue #1260) + Terminal Kind Classifier (Issue #3001)
  classifyFailureCategory,
  classifyTerminalKind,
  FAILURE_CATEGORY_WEIGHTS,
  type FailureWeightCategory,
  type TerminalFailureKind,
  // Health Analysis Engine (Issue #1101)
  HealthAnalysisEngine,
  crossReference,
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
  type HealthDimension,
  type HealthSeverity,
  type HealthStatus,
  type HealthConfidence,
  type HealthTrendDirection,
  type HealthFinding,
  type PeriodComparison,
  type DimensionResult,
  type CrossReference,
  type HealthAnalysisConfig,
  type CacheThresholdConfig,
  type HealthAnalysisResult,
  type HealthAnalysisInput,
  type HealthScoreEntry,
  type SelfTuningEntry,
  type ExperimentEntry,
  type HealthReportEntry,
  // Recommendation Tracking (Issue #1103)
  RecommendationTracker,
  type RecommendationHistoryEntry,
  type RecurringFinding,
  type RecommendationEffectivenessScore,
  type RecommendationReport,
  // Health Report Generator (Issue #1105)
  HealthReportGenerator,
  HealthReportSchema,
  type HealthReport,
  type HealthReportOptions,
  type WriteReportsResult,
  // Skill Amendment Detection
  SkillAmendmentDetector,
  type ValidationErrorRecord,
  type SkillAmendmentProposal,
  type SkillAmendmentAnalysisResult,
  // Skill Effectiveness Tracking (Issue #1414)
  SkillEffectivenessAnalyzer,
  type SkillChangeRecord,
  type SkillEffectivenessEntry,
  type SkillEffectivenessAnalysisResult,
  // Selective Test Metrics — Validation & Cost Tracking (Issue #1975)
  SelectiveTestMetricsCollector,
  SelectiveTestEffectivenessAnalyzer,
  type SelectiveTestEffectivenessAnalyzerConfig,
  EscapedDefectDetector,
  type SelectiveTestMetricRecord,
  type GraphGapRecord,
  type SelectiveTestEffectivenessResult,
  SelectiveTestMetricRecordSchema,
  GraphGapRecordSchema,
  // Skill Self-Assessment (Issue #1986)
  SkillSelfAssessmentSynthesizer,
  DEFAULT_RETENTION_DAYS,
  AssessmentRecordSchema,
  FrictionRecordSchema,
  FrictionTypeSchema,
  FrictionSeveritySchema,
  SkillImprovementProposalSchema,
  SynthesisResultSchema,
  type AssessmentRecord,
  type FrictionRecord,
  type FrictionType,
  type FrictionSeverity,
  type SkillImprovementProposal,
  type SynthesisResult,
} from "./analysis/index.js";

// Cross-Model Skill Evaluation Harness (Issue #3814)
export {
  EVAL_SCHEMA_VERSION,
  MODEL_TIERS,
  MODEL_TIER_VERSION_LABELS,
  PIPELINE_SKILLS,
  ModelTierSchema,
  EvalAssertionSchema,
  EvalScenarioSchema,
  EvalVerdictSchema,
  EvalModeSchema,
  AssertionFailureSchema,
  EvalCellResultSchema,
  EvalRunReportSchema,
  EvalRecordSchema,
  evaluateAssertions,
  MockModelRunner,
  LiveClaudeModelRunner,
  isLiveModeEnabled,
  SkillEvalHarness,
  EvalRecorder,
  DEFAULT_EVAL_RECORDS_DIR,
  reportToRecords,
  serializeReport,
  parseRecords,
  loadScenarios,
  loadFixtures,
  parseScenario,
  defaultDirReader,
  DEFAULT_SCENARIOS_DIR,
  DEFAULT_FIXTURES_DIR,
  loadEvalTasks,
  parseEvalTask,
  DEFAULT_TASKS_DIR,
  type DirReader,
  type EvalAssertion,
  type EvalAssertionType,
  type EvalScenario,
  type EvalVerdict,
  type EvalMode,
  type AssertionFailure,
  type EvalCellResult,
  type EvalRunReport,
  type EvalRecord,
  type ModelOutput,
  type AssertionEvaluation,
  type EvalModelRunner,
  type MockFixture,
  type MockFixtureMap,
  type SpawnFn,
  type LiveClaudeModelRunnerOptions,
  type SkillEvalHarnessRunOptions,
  type RecordWriter,
  type EvalRecorderOptions,
  type EvalDiff,
  type EvalDiffEntry,
  // Model-eval lane (docs/decisions/011-model-eval-system.md, #4168)
  MODEL_EVAL_SCHEMA_VERSION,
  BASELINE_PROMPT_VARIANT,
  PROVIDERS,
  ProviderSchema,
  EFFORT_LEVELS,
  EffortLevelSchema,
  REASONING_LEVELS,
  ReasoningLevelSchema,
  TokenRatesSchema,
  ModelDescriptorSchema,
  QUALITY_DIMENSIONS,
  QualityDimensionNameSchema,
  QualityDimensionScoreSchema,
  RubricCriterionSchema,
  EvalRubricSchema,
  EvalScoreSchema,
  JOB_CLASSES,
  JobClassSchema,
  DifficultySchema,
  EvalFixtureRefSchema,
  CheckCommandSchema,
  EvalTaskSchema,
  EvalMatrixCellSchema,
  TokenUsageSchema,
  GateResultSchema,
  ModelEvalCellResultSchema,
  EvalRunSummarySchema,
  EvalRunSchema,
  ModelEvalRecordSchema,
  type Provider,
  type EffortLevel,
  type ReasoningLevel,
  type TokenRates,
  type ModelDescriptor,
  type QualityDimensionName,
  type QualityDimensionScore,
  type RubricCriterion,
  type EvalRubric,
  type EvalScore,
  type JobClass,
  type Difficulty,
  type EvalFixtureRef,
  type CheckCommand,
  type EvalTask,
  type EvalMatrixCell,
  type TokenUsage,
  type GateResult,
  type ModelEvalCellResult,
  type EvalRunSummary,
  type EvalRun,
  type ModelEvalRecord,
  // Model & pricing registry (single source of truth, #4169; provider-aware #56)
  MODEL_REGISTRY,
  activeModels,
  getModelDescriptor,
  resolveModelForAdapter,
  providerForAdapter,
  isKnownModel,
  computeCostUsd,
  deriveDefaultModelCostRates,
  type TokenCounts,
  // Model-eval matrix runner (#4171)
  ModelEvalRunner,
  mapWithConcurrency,
  type EvalWorkspace,
  type WorkspaceProvider,
  type CellExecution,
  type EvalCellExecutor,
  type ModelEvalRunOptions,
  // Grading & scoring engine (#4173)
  scoreCell,
  computeCorrectness,
  computeAutomatedScore,
  aggregateJudge,
  runJudgeWithReliabilityGuard,
  JOB_CLASS_WEIGHTS,
  type ScoreWeights,
  type AutomatedBaseline,
  type AutomatedMetrics,
  type JudgeDimensionScore,
  type EvalJudgeVerdict,
  type EvalJudge,
  type ReliabilityGuardOptions,
  type ScoreCellInput,
  // Eval suite + real workspace provider (#4174)
  WorktreeWorkspaceProvider,
  defaultExec,
  buildMatrix,
  runEvalSuite,
  evalRunToRecords,
  serializeEvalRun,
  formatComparisonMatrix,
  computeVariantDeltas,
  formatVariantDeltas,
  DEFAULT_MODEL_EVAL_RECORDS_DIR,
  type ExecFn,
  type ExecResult,
  type WorktreeWorkspaceOptions,
  type RunEvalSuiteOptions,
  type VariantDelta,
  // Prompt-variant axis (#72)
  PromptVariantSchema,
  VariantReplacementSchema,
  applyPromptVariant,
  resolveVariant,
  loadPromptVariants,
  type PromptVariant,
  type VariantReplacement,
  // Eval → routing feedback (#4175)
  EvalRoutingAdvisor,
  ROUTING_MODES,
  type RoutingMode,
  type Confidence,
  type ModelJobStats,
  type Recommendation,
  type AdvisorOptions,
} from "./eval/index.js";

// Query Language (GQL)
export {
  // Types
  type TokenType,
  type Token,
  type ASTNode,
  type ASTNodeType,
  type ComparisonNode,
  type BinaryNode,
  type UnaryNode,
  type ComparisonOperator,
  type BooleanOperator,
  type FieldName,
  type FieldType,
  type FieldDefinition,
  type ParseResult,
  type QueryError,
  type QueryableIssue,
  type QueryResult,
  type SavedQuery,
  type SavedQueriesFile,
  // Errors
  QueryParseError,
  LexerError,
  ParserError,
  UnknownFieldError,
  InvalidOperatorError,
  InvalidValueError,
  QueryTooLongError,
  InvalidCharacterError,
  EvaluationError,
  // Constants
  MAX_QUERY_LENGTH,
  ALLOWED_FIELDS,
  FIELD_DEFINITIONS,
  SIZE_ORDER,
  // Schemas
  FieldNameSchema,
  SavedQuerySchema,
  SavedQueriesFileSchema,
  // Validation
  isValidField,
  getFieldDefinition,
  isValidOperatorForField,
  getAllowedOperators,
  getAllowedValues,
  isValidValueForField,
  parseRelativeDate,
  parseDateValue,
  // Lexer
  Lexer,
  tokenize,
  // Parser
  Parser,
  parse,
  validate,
  validate as validateQuery,
  isValid,
  isValid as isValidQuery,
  // Evaluator
  evaluateNode,
  evaluate,
  executeQuery,
} from "./query/index.js";

export {
  extractWikiLinks,
  resolveWikiLink,
  type WikiLink,
  type ResolvedWikiLink,
  type CrossRepoConfig,
} from "./utils/wikiLinkResolver.js";

// AC Reconciliation Pre-Flight (Issue #3003)
export {
  parseAcceptanceCriteria,
  selectRule,
  reconcileAcceptanceCriteria,
  MOSTLY_SATISFIED_THRESHOLD,
  AC_RULES,
  type ReconcileOptions,
  type RuleSelection,
  type AcceptanceCriterion,
  type AggregateStatus as ACAggregateStatus,
  type Classification as ACClassification,
  type ReconciledCriterion,
  type RuleContext,
  type RuleEvaluator,
  type RuleResult,
  type SuggestedApproach as ACSuggestedApproach,
  type SuggestedRoute as ACSuggestedRoute,
  type ACReconcileReport,
} from "./preflight/index.js";

// Audit Trail (Issue #1581)
export {
  AuditEventClient,
  AuditActionSchema,
  AuditEventSchema,
  AuditConfigSchema,
  AUDIT_ACTIONS,
  type AuditAction,
  type AuditEvent,
  type AuditConfig,
} from "./audit/index.js";

// Prompt Template System (Issue #9)
export {
  parseTemplateFile,
  type PromptTemplate,
  type TemplateLayer,
  type TemplateParam,
  type TemplateMetadata,
} from "./templates/PromptTemplate.js";

export {
  PromptRenderer,
  defaultRenderer,
  type TemplateContext,
} from "./templates/PromptRenderer.js";

export { TemplateRegistry, defaultRegistry } from "./templates/TemplateRegistry.js";

// Adapter Error Infrastructure (Issue #2596)
export {
  AdapterError,
  type AdapterErrorCategory,
  throwAuthError,
  throwBinaryNotFound,
  throwModelNotFound,
  throwServerUnreachable,
  throwVersionMismatch,
  throwConfigInvalid,
  throwTimeoutError,
} from "./cli/adapters/errors.js";

// Adapter Auth Pre-Flight (Issue #3222)
export {
  validateAdapterAuth,
  DEFAULT_AUTH_TIMEOUT_MS,
  type AdapterAuthResult,
  type ValidateAdapterAuthOptions,
} from "./cli/adapters/validateAdapterAuth.js";
export {
  runAdapterAuthPreflight,
  type AdapterAuthFailure,
  type AdapterPreflightAggregateResult,
  type RunAdapterAuthPreflightOptions,
} from "./cli/adapters/runAdapterAuthPreflight.js";
// Process-wide dedup / single-flight / timeout-retry for auth probes (#312).
export {
  probeAdapterAuthCached,
  resetAuthPreflightCache,
  DEFAULT_PREFLIGHT_CACHE_TTL_MS,
  DEFAULT_PREFLIGHT_RETRY_DELAY_MS,
  DEFAULT_PREFLIGHT_INITIAL_TIMEOUT_MS,
  DEFAULT_PREFLIGHT_RETRY_TIMEOUT_MS,
  type AuthPreflightCacheOptions,
} from "./cli/adapters/authPreflightCache.js";
// Default subprocess runner so auth callers actually probe CLI adapters (#4031).
export { createDefaultPreflightRunner, type PreflightCommandRunner } from "./cli/codexPreflight.js";

// Adapter type re-export for consumers wiring per-stage adapter lists.
export type { IncrediAdapter } from "./cli/adapters/ICliAdapter.js";
// Agentic truth-gate for pipeline dispatch (#57)
export { isAgenticAdapter } from "./cli/adapters/AdapterRegistry.js";

// Canonical Codex model registry (#4018) — single source of truth for Codex
// model ids, deprecation metadata, and the tier→model map. The VSCode extension
// imports these from the SDK barrel (no duplicate model lists across packages).
export {
  CODEX_MODELS,
  CODEX_TIER_MODEL_MAP,
  CODEX_RECOMMENDED_DEFAULT_MODEL,
  CODEX_DEFAULT_BASE_MODEL,
  isValidCodexModel,
  isDeprecatedCodexModel,
  isResearchPreviewCodexModel,
  listCodexModels,
  resolveCodexModelAlias,
} from "./cli/adapters/codexModelRegistry.js";
export type {
  CodexTier,
  CodexModelMeta,
  ListCodexModelsOptions,
} from "./cli/adapters/codexModelRegistry.js";

// Provider-aware model preflight (#4021) — single fail-fast validator for an
// (adapter, model) pair. The VSCode extension imports these from the SDK barrel
// so the model-validity policy lives in exactly one place.
export {
  validateModelForAdapter,
  resolveAndValidateModel,
  ADAPTER_MODEL_POLICY,
  GEMINI_MODELS,
} from "./cli/adapters/modelPreflight.js";
export type {
  ModelValidationResult,
  AdapterModelPolicy,
  ModelSetKind,
} from "./cli/adapters/modelPreflight.js";

// Provider-aware Codex sandbox mapping (#4026) — derive `--sandbox` mode +
// approval policy from a stage's allowed-tools (Codex has no per-tool allowlist).
export {
  resolveCodexSandboxMode,
  codexSandboxFlags,
  applyCodexSandboxProfile,
  CODEX_BYPASS_FLAG,
} from "./cli/adapters/codexSandbox.js";
export type { CodexSandboxMode } from "./cli/adapters/codexSandbox.js";

// Claude native Dynamic Workflows ("ultracode") offload — version gate (>=
// v2.1.154), typed downgrade signal, and the sink-emitting driver (#3910). The
// `supportsNativeWorkflow` predicate is reused by the WorkflowExecutor (#3908).
export {
  MIN_NATIVE_WORKFLOW_VERSION,
  ULTRACODE_KEYWORD_RENAME_VERSION,
  supportsNativeWorkflow,
  ultracodeKeyword,
  parseVersion,
  preflightNativeWorkflow,
  isNativeWorkflowDisabledByEnv,
  detectClaudeCliVersion,
  detectClaudeSdkVersion,
  mapNativeUsage,
  emitNativeWorkflowTree,
  runClaudeNativeWorkflow,
  NativeWorkflowUnavailableError,
  type NativeWorkflowUnavailableReason,
  type NativeWorkflowReadiness,
  type NativeWorkflowSurface,
  type ClaudeNativeWorkflowOptions,
  type NativeAgentUsageReport,
  type NativeProgressEvent,
} from "./cli/adapters/ClaudeNativeWorkflow.js";

// Work-item contract — stable interface for issue discovery across backends (Issue #2565)
export type {
  WorkItem,
  WorkItemSource,
  IWorkItemProvider,
  WorkItemEvent,
  BlockingIssue as WorkItemBlockingIssue,
  Priority as WorkItemPriority,
  Size as WorkItemSize,
  SortBy as WorkItemSortBy,
  SortDirection as WorkItemSortDirection,
} from "./types/WorkItem.js";
export { isBlocked as isWorkItemBlocked, isEpicItem } from "./types/WorkItem.js";
