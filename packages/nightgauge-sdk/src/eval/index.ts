/**
 * Cross-Model Skill Evaluation Harness — public surface.
 *
 * @see Issue #3814 - Build a cross-model skill evaluation harness
 * @see docs/SKILL_EVALUATION.md
 */

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
  type EvalAssertion,
  type EvalAssertionType,
  type EvalScenario,
  type EvalVerdict,
  type EvalMode,
  type AssertionFailure,
  type EvalCellResult,
  type EvalRunReport,
  type EvalRecord,
} from "./schemas.js";

// Re-export ModelTier so consumers can import it from the eval surface.
export type { ModelTier } from "../analysis/AutoModelSelector.js";

export { evaluateAssertions, type ModelOutput, type AssertionEvaluation } from "./assertions.js";

export {
  MockModelRunner,
  LiveClaudeModelRunner,
  isLiveModeEnabled,
  type EvalModelRunner,
  type MockFixture,
  type MockFixtureMap,
  type SpawnFn,
  type LiveClaudeModelRunnerOptions,
} from "./modelRunner.js";

export { SkillEvalHarness, type SkillEvalHarnessRunOptions } from "./SkillEvalHarness.js";

export {
  EvalRecorder,
  DEFAULT_EVAL_RECORDS_DIR,
  reportToRecords,
  serializeReport,
  parseRecords,
  type RecordWriter,
  type EvalRecorderOptions,
  type EvalDiff,
  type EvalDiffEntry,
} from "./EvalRecorder.js";

export {
  loadScenarios,
  loadFixtures,
  parseScenario,
  defaultDirReader,
  DEFAULT_SCENARIOS_DIR,
  DEFAULT_FIXTURES_DIR,
  type DirReader,
} from "./loader.js";

export { loadEvalTasks, parseEvalTask, DEFAULT_TASKS_DIR } from "./taskLoader.js";

// ---------------------------------------------------------------------------
// Model Evaluation & Benchmarking System (model-eval lane) — see
// docs/decisions/011-model-eval-system.md and Issue #4168.
// ---------------------------------------------------------------------------
export {
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
} from "./modelEvalSchemas.js";

export {
  MODEL_REGISTRY,
  activeModels,
  getModelDescriptor,
  resolveModelForAdapter,
  providerForAdapter,
  isKnownModel,
  computeCostUsd,
  deriveDefaultModelCostRates,
  type TokenCounts,
} from "./modelRegistry.js";

export {
  ModelEvalRunner,
  mapWithConcurrency,
  type EvalWorkspace,
  type WorkspaceProvider,
  type CellExecution,
  type EvalCellExecutor,
  type ModelEvalRunOptions,
} from "./modelEvalRunner.js";

export {
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
} from "./qualityScorer.js";

export {
  WorktreeWorkspaceProvider,
  defaultExec,
  type ExecFn,
  type ExecResult,
  type WorktreeWorkspaceOptions,
} from "./worktreeWorkspace.js";

export {
  buildMatrix,
  runEvalSuite,
  evalRunToRecords,
  serializeEvalRun,
  formatComparisonMatrix,
  computeVariantDeltas,
  formatVariantDeltas,
  DEFAULT_MODEL_EVAL_RECORDS_DIR,
  type RunEvalSuiteOptions,
  type VariantDelta,
} from "./evalSuite.js";

export {
  PromptVariantSchema,
  VariantReplacementSchema,
  applyPromptVariant,
  resolveVariant,
  loadPromptVariants,
  type PromptVariant,
  type VariantReplacement,
} from "./promptVariants.js";

export {
  LiveCellExecutor,
  defaultCliSpawn,
  type LiveCellExecutorOptions,
  type CliSpawnFn,
  type CliSpawnResult,
} from "./liveCellExecutor.js";

export {
  parseClaudeResult,
  resolveEvalAdapterProfile,
  resolveEvalAdapterProfileForAdapter,
  claudeEvalProfile,
  codexEvalProfile,
  type EvalAdapterProfile,
  type SpawnTelemetry,
} from "./evalAdapters.js";

export {
  LiveClaudeJudge,
  extractJudgeVerdict,
  DEFAULT_JUDGE_MODEL,
  type LiveClaudeJudgeOptions,
  type SourceFile,
} from "./liveJudge.js";

export {
  emitEvalRun,
  resolveEvalEmitConfig,
  EvalEmitError,
  DEFAULT_PLATFORM_URL,
  EVAL_INGEST_PATH,
  type EvalEmitConfig,
  type EvalEmitConfigSources,
  type EvalIngestResult,
  type IngestRejection,
  type EmitEvalRunOptions,
  type FetchLike,
} from "./platformEmit.js";

export {
  EvalRoutingAdvisor,
  ROUTING_MODES,
  type RoutingMode,
  type Confidence,
  type ModelJobStats,
  type Recommendation,
  type AdvisorOptions,
} from "./routingAdvisor.js";
