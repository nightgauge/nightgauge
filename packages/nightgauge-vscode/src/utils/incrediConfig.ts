/**
 * IncrediConfig Utilities — Facade / Re-export Module
 *
 * This file re-exports all configuration utilities from their focused domain modules.
 * All existing import paths (`from "...incrediConfig"`) continue to work unchanged.
 *
 * Domain modules:
 * - resolvers/authResolver      — GitHub auth, tokens, auth providers
 * - resolvers/stageResolver     — Stage execution mode, model, budget, effort
 * - resolvers/modelResolver     — Default model, routing, adapters, complexity
 * - resolvers/otherResolver     — Pipeline control, budget enforcement, concurrent, epic merge
 * - resolvers/monitoringResolver — Stall detection, alerting, MCP, supercharge, audit
 *
 * @see Issue #2742 - Extract incrediConfig.ts resolver classes
 * @see Issue #195 - auto_accept_stages not respected for stage transitions
 * @see Issue #433 - config.yaml (formerly nightgauge.yaml)
 */

// ============================================================================
// Auth Resolvers
// ============================================================================
export {
  type AuthProvider,
  DEFAULT_AUTH_PROVIDER,
  getAuthProvider,
  getGitHubUser,
  expandEnvVar,
  getGitHubAuthToken,
  getGitHubAuthTokens,
} from "./resolvers/authResolver";

// ============================================================================
// Stage Resolvers
// ============================================================================
export {
  type StageExecutionMode,
  DEFAULT_STAGE_EXECUTION_MODE,
  getDefaultStageExecutionMode,
  type StageBudget,
  getStageBudget,
  type ClaudeEffort,
  DEFAULT_STAGE_EFFORTS,
  EFFORT_SUPPORTING_MODELS,
  modelSupportsEffort,
  getStageModel,
  getStageOverrideModel,
  getStageModelsMatrix,
  getTypeOverrides,
  getTaskTypeStageOverrides,
  getModelDefaultEffort,
  getStageEffort,
  getExplicitStageEffort,
  conformEffortForFable,
} from "./resolvers/stageResolver";

// ============================================================================
// Model Resolvers
// ============================================================================
export {
  type DefaultModel,
  getDefaultModel,
  getFallbackModel,
  getMaxTurns,
  getCostBudget,
  type ExecutionAdapter,
  DEFAULT_EXECUTION_ADAPTER,
  getExecutionAdapter,
  type GeminiModel,
  type GeminiAuthMethod,
  getGeminiModel,
  getGeminiAuthMethod,
  type CodexModel,
  getCodexModel,
  resolveCodexPipelineModel,
  getCodexCliCommand,
  getCodexCliArgs,
  getCodexResumeEnabled,
  getLmStudioModel,
  getLmStudioBaseUrl,
  getLmStudioApiKey,
  getLmStudioTimeoutMs,
  getCopilotModel,
  type ModelRoutingMode,
  type ComplexityThresholds,
  DEFAULT_COMPLEXITY_THRESHOLDS,
  DEFAULT_CONFIDENCE_THRESHOLD,
  getModelRoutingMode,
  getComplexityThresholds,
  getMinimumModel,
  getConfidenceThreshold,
  getEscalatedModel,
} from "./resolvers/modelResolver";

/**
 * Per-run user-selected pipeline model override.
 *
 * Claude currently uses `haiku`/`sonnet`/`opus`, while Codex accepts
 * CLI-facing model identifiers such as `gpt-5.4` (see the canonical
 * CODEX_TIER_MODEL_MAP registry in the SDK). Keep this broad so adapter-aware
 * UI can pass through the correct identifier without forcing all adapters into
 * Claude's model taxonomy.
 */
export type PipelineModelOverride = string;

// ============================================================================
// Pipeline Control Resolvers
// ============================================================================
export { type BudgetMode, type SizeLabel } from "./budgetEnforcer";

export {
  type PrCICheckConfig,
  DEFAULT_PR_CI_CHECK_CONFIG,
  type HumanInTheLoopConfig,
  getHumanInTheLoopConfig,
  shouldAutoAcceptStage,
  getInitialExecutionMode,
  getRetryConfig,
  getPrCICheckConfig,
  type BudgetEnforcementConfig,
  getBudgetEnforcementConfig,
  getOutputTokenLimitOverrides,
  type ContextBudgetConfig,
  getContextBudgetConfig,
  type PipelineCeilingConfig,
  getPipelineCeilingConfig,
  getMaxBacktracks,
  getMaxEscalationsPerStage,
  type EpicMergeConfig,
  DEFAULT_EPIC_MERGE_CONFIG,
  getEpicMergeConfig,
  type ConcurrentPipelineConfig,
  getConcurrentPipelineConfig,
  type EpicQueueFilterConfig,
  DEFAULT_EPIC_QUEUE_FILTER_CONFIG,
  getEpicQueueFilterConfig,
  type ContextSchemaRepairConfig,
  DEFAULT_CONTEXT_SCHEMA_REPAIR_CONFIG,
  getContextSchemaRepairConfig,
  getSkipAuthPreflight,
  isAdaptiveBudgetEnabled,
} from "./resolvers/otherResolver";

// ============================================================================
// Monitoring & Observability Resolvers
// ============================================================================
export {
  DEFAULT_STALL_THRESHOLDS,
  DEFAULT_STAGE_HARD_CAPS,
  DEFAULT_STAGE_COST_CAPS,
  COST_CAP_MODEL_SCALE,
  DEFAULT_COST_CAP_MODE_MULTIPLIER,
  getStallThresholds,
  getStallKillMultiplier,
  getStallIdleMs,
  getQuotaSignalIdleMs,
  DEFAULT_QUOTA_SIGNAL_IDLE_MS,
  shouldQuotaFastFail,
  getStageHardCapMs,
  getStageCostCapUsd,
  getCostCapModelScale,
  getCostCapModeMultiplier,
  getEffectiveStageCostCap,
  getStageCostWarnMultiplier,
  getRunwayCeilingUsd,
  getBudgetEvalCadenceMs,
  getStageTimeCapMs,
  type AutonomousStallConfig,
  getAutonomousStallConfig,
  type CalibratedStallData,
  roundUpTo30s,
  computeKillThreshold,
  getStallCalibrationMinRuns,
  precomputeCalibratedStallThresholds,
  getCalibratedStallData,
  getLargeDiffThreshold,
  type ExperimentConfigResult,
  getExperimentConfig,
  getContextFileSizeAlertThreshold,
  type AlertingConfig,
  DEFAULT_ALERTING_CONFIG,
  getAlertingConfig,
  getStageMcpTools,
  getMcpToolsConfig,
  getAuditConfig,
  writeSuperchargeStateFile,
  isSuperchargeModeActive,
  getSuperchargeModel,
  getSuperchargeCodexModel,
  getPerformanceMode,
  writePerformanceModeStateFile,
  getModeStageCodexModel,
  getLegacySuperchargeStatePath,
  getProgressRunawayConfig,
} from "./resolvers/monitoringResolver";

// Re-export performance-mode primitives so consumers can import from
// `utils/incrediConfig` consistently (Issue #3009).
export {
  type PerformanceMode,
  PERFORMANCE_MODES,
  DEFAULT_PERFORMANCE_MODE,
  MODE_PROFILES,
  type ModeProfile,
  type ModeEnvelope,
  DEFAULT_MODE_ENVELOPE,
  getModeEnvelope,
  type StageProfile as PerformanceStageProfile,
  type PipelineProfile as PerformancePipelineProfile,
  getModeStageProfile,
  getModeStageAdapterModel,
  isPerformanceMode,
} from "./modeProfiles";
