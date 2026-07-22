/**
 * Shared types for the analysis module.
 *
 * Used by ModelPerformanceAnalyzer (#653), TokenEfficiencyAnalyzer (#651),
 * and future sibling analyzers (BaselineCalculator #652).
 */

import { deriveDefaultModelCostRates } from "../eval/modelRegistry.js";

/** Adapter-model composite identifier, e.g. "claude:sonnet", "codex:gpt-4o" */
export type ModelIdentifier = string;

/**
 * Minimal execution history record consumed by analysis modules.
 *
 * Matches the schema planned in #649 (Execution History Persistence).
 * Until #649 merges, analyzers are developed and tested with synthetic data.
 */
export interface ExecutionHistoryRecord {
  issueNumber: number;
  stage: string;
  adapter?: string;
  model?: string;
  success: boolean;
  retries: number;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens?: number;
  cacheCreationTokens?: number;
  costUsd: number;
  durationMs: number;
  timestamp: string; // ISO 8601
  complexityScore?: number;
  /**
   * Pipeline run outcome classification (Issue #709)
   *
   * - 'productive': Code was changed and/or PR was merged
   * - 'verify-and-close': Pipeline ran but no files were changed
   * - 'already-resolved': Issue was already resolved before pipeline started
   */
  outcomeType?: "productive" | "verify-and-close" | "already-resolved";
  /**
   * Model selection metadata for feedback loop analysis (Issue #734)
   *
   * Populated when auto-model-selection is active. Enables tracking
   * which model was selected, why, and whether it succeeded.
   */
  modelSelectionMode?: "manual" | "automatic" | "hybrid";
  selectedModel?: string;
  selectionSource?:
    | "env"
    | "config"
    | "stage-default"
    | "auto"
    /** AutoProviderRouter selected the (adapter, model) pair (Issue #3230). */
    | "auto-router"
    | "experiment"
    | "default"
    | "feedback-escalation"
    | "user-override";
  autoSelectorConfidence?: number;
  autoSelectorComplexity?: string;
  /** Context handoff file size in bytes (Issue #1009) */
  contextFileSizeBytes?: number;
  /** Failure category for weighted reliability scoring (Issue #1260) */
  failure_category?: "infrastructure" | "agent" | "organic";
  /**
   * True when the run used local inference (e.g., LM Studio) — cost is
   * definitively zero and should not be treated as hosted-provider billing.
   * @see Issue #2055
   */
  isLocalModel?: boolean;
}

/** Per-model, per-stage aggregated metrics */
export interface ModelStagePerformance {
  model: ModelIdentifier;
  stage: string;
  runs: number;
  successRate: number;
  avgInputTokens: number;
  avgOutputTokens: number;
  avgCostUsd: number;
  avgDurationMs: number;
  retryRate: number;
  effectiveCostPerSuccess: number;
  qualityIndicators: {
    firstAttemptSuccessRate: number;
  };
  samplePeriod: {
    earliest: string;
    latest: string;
  };
}

/** Comparison report for a single stage */
export interface StageModelComparison {
  stage: string;
  models: ModelStagePerformance[];
  recommendedModel: ModelIdentifier | null;
  recommendation: string;
  confidence: "low" | "medium" | "high";
  estimatedSavingsUsd: number;
}

export type SuggestionType = "downgrade" | "upgrade" | "complexity-based" | "ab-comparison";

/** Routing optimization suggestion */
export interface RoutingRecommendation {
  type: SuggestionType;
  stage: string;
  currentModel: ModelIdentifier;
  suggestedModel: ModelIdentifier;
  rationale: string;
  estimatedSavingsUsd: number;
  confidence: "low" | "medium" | "high";
  evidence: {
    currentSuccessRate: number;
    suggestedSuccessRate: number;
    currentEffectiveCost: number;
    suggestedEffectiveCost: number;
    sampleSizes: Record<ModelIdentifier, number>;
  };
}

/** Complete analysis result */
export interface ModelRoutingAnalysis {
  analyzedAt: string;
  recordsAnalyzed: number;
  stageComparisons: StageModelComparison[];
  recommendations: RoutingRecommendation[];
  summary: {
    totalPotentialSavingsUsd: number;
    stagesWithSufficientData: number;
    stagesNeedingMoreData: string[];
    overallRecommendation: string;
  };
  autoSelectionAnalysis?: AutoSelectionAnalysis;
}

/** Auto-selection outcome analysis (Issue #734) */
export interface AutoSelectionStageOutcome {
  stage: string;
  totalAutoSelected: number;
  successCount: number;
  failureCount: number;
  successRate: number;
  avgConfidence: number;
  modelsUsed: Record<string, number>;
}

/** Under-routing detection result */
export interface UnderRoutingPattern {
  stage: string;
  model: string;
  complexity: string;
  failureCount: number;
  suggestion: string;
}

/** Over-routing detection result */
export interface OverRoutingPattern {
  stage: string;
  model: string;
  complexity: string;
  successCount: number;
  estimatedWasteUsd: number;
  suggestion: string;
}

/** Threshold adjustment recommendation */
export interface ThresholdRecommendation {
  field: string;
  currentValue: number;
  suggestedValue: number;
  rationale: string;
  confidence: "low" | "medium" | "high";
  evidence: {
    sampleSize: number;
    affectedStages: string[];
  };
}

/** Auto-selection analysis result (part of ModelRoutingAnalysis) */
export interface AutoSelectionAnalysis {
  totalAutoSelectedRecords: number;
  overallAutoSuccessRate: number;
  perStageOutcomes: AutoSelectionStageOutcome[];
  underRoutingPatterns: UnderRoutingPattern[];
  overRoutingPatterns: OverRoutingPattern[];
  thresholdRecommendations: ThresholdRecommendation[];
  costSavingsVsStaticUsd: number;
}

/** Cost rates per model for token-to-USD conversion */
export interface ModelCostRate {
  inputPerMillion: number;
  outputPerMillion: number;
  cacheReadPerMillion?: number;
  cacheCreationPerMillion?: number;
}

/**
 * Default cost rates per model tier for token-to-USD conversion.
 *
 * 4-tier model strategy: Haiku (lightweight), Sonnet (default), Opus (heavy),
 * Fable (premium frontier, ~2× Opus). Rates are USD per million tokens.
 * Automatic routing caps at Opus; Fable is opt-in.
 *
 * **Derived** from the single-source model registry
 * (`packages/nightgauge-sdk/src/eval/model-registry.json`) — do NOT
 * hand-edit rates here; edit the registry. One tier entry per current
 * (non-deprecated) Anthropic model.
 *
 * @see docs/decisions/011-model-eval-system.md - Issue #4169 (single-source registry)
 * @see Issue #725 - Haiku model routing for lightweight stages
 */
export const DEFAULT_MODEL_COST_RATES: Record<string, ModelCostRate> =
  deriveDefaultModelCostRates();

/** Analyzer configuration */
export interface ModelAnalyzerConfig {
  minSamplesPerModelPerStage: number;
  costRates?: Record<string, ModelCostRate>;
  dateRange?: {
    since?: string;
    until?: string;
  };
  recencyWeight?: number;
  qualityThreshold?: number;
}

/** Token cost rates for USD estimation (shared by failure and efficiency analyzers) */
export interface CostRates {
  inputPerMillion: number;
  outputPerMillion: number;
  cacheReadPerMillion?: number;
  cacheCreationPerMillion?: number;
}

/**
 * Extended execution history record with run-level outcome and error data.
 *
 * Used by FailurePatternDetector which needs error text and run outcome
 * beyond the base per-stage record.
 */
export interface ExecutionHistoryRunRecordFlat extends ExecutionHistoryRecord {
  /** Stage error text for pattern matching */
  errorText?: string;
  /** Run-level outcome */
  outcome: "complete" | "failed" | "cancelled";
  /** Size label (XS/S/M/L/XL) for correlation analysis */
  complexityLabel?: string;
}

// --- Token Efficiency Analyzer types (#651) ---

/**
 * Extended execution history record with optional per-stage detail fields.
 *
 * Used by TokenEfficiencyAnalyzer (#651) to detect waste patterns that
 * require file-read, tool-call, and context-utilization data beyond the
 * base per-stage record.
 */
export interface ExecutionHistoryRecordExtended extends ExecutionHistoryRecord {
  /** Files read during the stage execution */
  filesRead?: string[];
  /** Files written during the stage execution */
  filesWritten?: string[];
  /** Number of tool calls made during the stage */
  toolCalls?: number;
  /** Fraction of context window used (0–1) */
  contextWindowUtilization?: number;
  /** Detailed tool call records from JSONL v2 (Issue #1004) */
  toolCallDetails?: Array<{
    tool: string;
    target?: string;
    stage?: string;
    duration_ms?: number;
    error?: string;
  }>;
}

/** Configuration for TokenEfficiencyAnalyzer */
export interface TokenEfficiencyConfig {
  costRates?: Record<string, ModelCostRate>;
  defaultCostRate?: ModelCostRate;
  thresholds?: {
    redundantReadMinOccurrences?: number;
    oversizedContextPercentile?: number;
    cacheMissRateThreshold?: number;
    toolCallsPercentile?: number;
    contextUtilizationMinimum?: number;
  };
  minSamplesForOutliers?: number;
  dateRange?: { since?: string; until?: string };
}

/** Severity levels for waste patterns */
export type WasteSeverity = "critical" | "high" | "medium" | "low" | "info";

/** Waste category identifiers for token efficiency analysis */
export type WasteCategory =
  | "redundant-file-reads"
  | "oversized-context"
  | "cache-miss-patterns"
  | "tool-call-inefficiency"
  | "context-window-utilization"
  | "zero-change-run";

/** Actionable config change for a recommendation */
export interface RecommendationAction {
  /** What type of action: config patch, or informational only */
  type: "config-patch" | "info-only";
  /** Dot-notation config path, e.g. "pipeline.skip_checks.zero_change_detection" */
  configPath: string;
  /** The suggested value to write */
  suggestedValue: unknown;
  /** Button label shown in dashboard, e.g. "Enable caching" */
  label: string;
}

/** A single detected waste pattern */
export interface WastePattern {
  category: WasteCategory;
  severity: WasteSeverity;
  title: string;
  description: string;
  affectedStages: string[];
  wastedTokens: number;
  estimatedSavingsUsd: number;
  recommendation: string;
  evidence: Record<string, unknown>;
  /** Optional actionable config change for this recommendation */
  action?: RecommendationAction;
}

/** Complete token efficiency analysis result */
export interface TokenEfficiencyAnalysis {
  analyzedAt: string;
  recordsAnalyzed: number;
  wastePatterns: WastePattern[];
  summary: {
    totalWastedTokens: number;
    totalEstimatedSavingsUsd: number;
    categorySummary: Record<
      WasteCategory,
      {
        patternCount: number;
        totalWastedTokens: number;
        totalSavingsUsd: number;
      }
    >;
    overallEfficiencyScore: number;
    topRecommendation: string;
  };
}
