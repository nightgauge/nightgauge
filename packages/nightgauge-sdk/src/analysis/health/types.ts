/**
 * Health Analysis Engine - Type Definitions
 *
 * Defines all interfaces for the multi-dimensional health analysis engine.
 * Types mirror relevant fields from AggregatedDataset (vscode package) to
 * avoid circular dependencies — callers map AggregatedDataset → HealthAnalysisInput.
 *
 * @see Issue #1101 - Multi-Dimensional Health Analysis Engine
 */

import type { ExecutionHistoryRecord } from "../types.js";
import type { WorkflowCalibrationSignal } from "../WorkflowOutcomeAnalyzer.js";

// ── Dimension Identifiers ─────────────────────────────────────────

export type HealthDimension =
  | "token-economics"
  | "cost-health"
  | "stage-effectiveness"
  | "model-routing"
  | "reliability"
  | "learning-effectiveness"
  | "pipeline-velocity"
  | "skill-drift";

export const ALL_DIMENSIONS: HealthDimension[] = [
  "token-economics",
  "cost-health",
  "stage-effectiveness",
  "model-routing",
  "reliability",
  "learning-effectiveness",
  "pipeline-velocity",
  "skill-drift",
];

// ── Severity & Status ─────────────────────────────────────────────

export type Severity = "critical" | "high" | "medium" | "low" | "info";

export type HealthStatus = "excellent" | "good" | "fair" | "poor" | "critical";

export type Confidence = "high" | "medium" | "low";

export type TrendDirection = "improving" | "stable" | "degrading";

// ── Findings ──────────────────────────────────────────────────────

export interface Finding {
  id: string;
  dimension: HealthDimension;
  severity: Severity;
  title: string;
  description: string;
  impact: string;
  recommendation: string;
  evidence: Record<string, unknown>;
  confidence: Confidence;
}

// ── Period Comparison ─────────────────────────────────────────────

export interface PeriodComparison {
  currentValue: number;
  baselineValue: number;
  changePercent: number;
  direction: TrendDirection;
  isSignificant: boolean;
}

// ── Dimension Result ──────────────────────────────────────────────

export interface DimensionResult {
  dimension: HealthDimension;
  score: number; // 0-100
  status: HealthStatus;
  findings: Finding[];
  metrics: Record<string, number>;
  hasEnoughData: boolean;
  sampleSize: number;
  periodComparison?: PeriodComparison;
}

// ── Cross-Reference ───────────────────────────────────────────────

export interface CrossReference {
  id: string;
  dimensions: HealthDimension[];
  severity: Severity;
  title: string;
  description: string;
  correlatedFindings: string[];
  confidence: Confidence;
  evidence: Record<string, unknown>;
}

// ── Configuration ─────────────────────────────────────────────────

/**
 * Per-stage cache-hit-rate alerting thresholds (Issue #3804).
 *
 * Resolves from `pipeline.cache` config: `default` mirrors
 * `pipeline.cache.alert_threshold` (as a 0–1 fraction) and `byStage` mirrors
 * `pipeline.cache.stage_alert_thresholds`. The Token Economics dimension and
 * the pipeline-audit skill read the same resolved values so the two reporting
 * surfaces agree by construction (ADR-002, ADR-003).
 *
 * Thresholds are fractions in `[0, 1]` (e.g. `0.4` for 40%), matching the
 * `cacheHitRate` metric. A stage with a `byStage` entry uses it; otherwise the
 * `default` applies.
 */
export interface CacheThresholdConfig {
  /** Global per-stage threshold fraction (0–1). */
  default: number;
  /** Optional per-stage overrides, keyed by stage name. */
  byStage?: Record<string, number>;
}

export interface HealthAnalysisConfig {
  dimensions: HealthDimension[];
  minimumSampleSizes: {
    basic: number;
    trend: number;
    significance: number;
  };
  confidenceThreshold: number;
  weights: Record<HealthDimension, number>;
  /**
   * Per-stage cache-hit-rate thresholds for low-reuse findings (Issue #3804).
   * When omitted, the Token Economics dimension falls back to
   * `DEFAULT_CACHE_THRESHOLD` (0.4 = 40%, matching `pipeline.cache.alert_threshold`).
   */
  cacheThresholds?: CacheThresholdConfig;
}

/**
 * Default per-stage cache-hit-rate threshold (Issue #3804).
 *
 * 0.4 (40%) mirrors the existing global `pipeline.cache.alert_threshold`
 * default, so adding the per-stage finding preserves current behavior.
 */
export const DEFAULT_CACHE_THRESHOLD = 0.4;

export const DEFAULT_HEALTH_CONFIG: HealthAnalysisConfig = {
  dimensions: ALL_DIMENSIONS,
  minimumSampleSizes: {
    basic: 5,
    trend: 10,
    significance: 20,
  },
  confidenceThreshold: 0.05,
  weights: {
    "token-economics": 0.15,
    "cost-health": 0.18,
    "stage-effectiveness": 0.18,
    "model-routing": 0.12,
    reliability: 0.15,
    "learning-effectiveness": 0.1,
    "pipeline-velocity": 0.12,
    "skill-drift": 0.08,
  },
  cacheThresholds: { default: DEFAULT_CACHE_THRESHOLD },
};

// ── Analysis Result ───────────────────────────────────────────────

export interface HealthAnalysisResult {
  dimensions: Partial<Record<HealthDimension, DimensionResult>>;
  crossReferences: CrossReference[];
  overallScore: number;
  overallStatus: HealthStatus;
  summary: string;
  analyzedAt: string;
  config: HealthAnalysisConfig;
}

// ── SDK-Native Input Types ────────────────────────────────────────
// These mirror AggregatedDataset fields without importing from vscode.
// Callers map AggregatedDataset → HealthAnalysisInput.

export interface HealthAnalysisInput {
  executionHistory: ExecutionHistoryRecord[];
  healthScores: HealthScoreEntry[];
  selfTuningLog: SelfTuningEntry[];
  experimentResults: ExperimentEntry[];
  healthReports: HealthReportEntry[];
  recommendationHistory?: RecommendationHistoryEntry[];
  /** Skill self-assessment synthesis result (Issue #1986, #2320) */
  skillAssessmentSynthesis?: {
    recordsAnalyzed: number;
    totalFrictionItems: number;
    proposals: Array<{
      skillFile: string;
      findingPattern: string;
      occurrenceCount: number;
      severity: "high" | "medium" | "low";
      proposedChange: string;
    }>;
    isolatedCount: number;
  };
  /**
   * Aggregate V4 workflow-orchestration calibration signal folded from the
   * canonical schemaVersion-4 WorkflowEvent node tree (Issue #3915, epic #3899).
   * Optional — present only once a run exercises multi-agent fan-out. Feeds the
   * learning-effectiveness dimension (judge-rejection rate, fan-out efficiency,
   * native-vs-fanout cost delta).
   */
  workflowCalibration?: WorkflowCalibrationSignal;
}

export interface HealthScoreEntry {
  timestamp: string;
  score: number;
  status: string;
  components: Record<string, number>;
  costUsd: number;
  issueNumber: number;
}

export interface SelfTuningEntry {
  timestamp: string;
  field: string;
  previousValue: number | string;
  newValue: number | string;
  rationale: string;
  trigger: string;
}

export interface ExperimentEntry {
  experimentName: string;
  group: "control" | "treatment";
  issueNumber: number;
  stage: string;
  success: boolean;
  costUsd: number;
  durationMs: number;
  recordedAt: string;
}

export interface HealthReportEntry {
  createdAt: string;
  periodDays: number;
  summary: {
    totalCostUsd: number;
    avgCostPerRun: number;
    totalRuns: number;
    successRate: number;
    avgDurationMinutes: number;
    totalTokens: number;
    cacheHitRate: number;
  };
  findingCount: number;
  recommendationCount: number;
}

// ── Finding-to-Issue Engine Types ─────────────────────────────────
// @see Issue #1102 - Finding-to-Issue Generation Engine

export interface FindingToIssueConfig {
  severityThreshold: Severity;
  epicGroupingThreshold: number;
  dryRun: boolean;
  repository?: string;
  parentIssue?: number;
  hooksDir?: string;
}

export const DEFAULT_FINDING_TO_ISSUE_CONFIG: FindingToIssueConfig = {
  severityThreshold: "high",
  epicGroupingThreshold: 3,
  dryRun: false,
};

export interface GeneratedIssue {
  findingId: string;
  title: string;
  body: string;
  labels: string[];
  severity: Severity;
  dimension: HealthDimension;
  issueNumber?: number;
  issueUrl?: string;
  epicNumber?: number;
  skipped?: boolean;
  skipReason?: string;
}

export interface EpicGroup {
  dimension: HealthDimension;
  findings: Finding[];
  epicTitle: string;
  epicBody: string;
  epicLabels: string[];
  epicNumber?: number;
  subIssues: GeneratedIssue[];
}

export interface FindingToIssueResult {
  totalFindings: number;
  filteredFindings: number;
  duplicatesSkipped: number;
  issuesCreated: number;
  epicsCreated: number;
  generatedIssues: GeneratedIssue[];
  epicGroups: EpicGroup[];
  dryRun: boolean;
  healthReportRef?: string;
}

// ── Recommendation Tracking Types ────────────────────────────────
// @see Issue #1103 - Recommendation Tracking

export interface RecommendationHistoryEntry {
  schema_version: "1";
  finding_id: string;
  created_at: string; // ISO 8601
  severity: Severity;
  dimension: HealthDimension;
  title: string;
  recommendation: string;
  issue_number?: number;
  issue_url?: string;
  issue_state?: "open" | "closed" | "not_created";
  metric_before?: number; // Dimension score at recommendation time
  metric_after?: number; // Dimension score at assessment time
  improvement_percent?: number; // ((after - before) / before) * 100
  assessed_at?: string; // ISO 8601
  health_report_ref?: string;
}

export interface RecurringFinding {
  finding_title: string;
  dimension: HealthDimension;
  occurrence_count: number;
  first_seen: string; // ISO 8601
  last_seen: string; // ISO 8601
  issue_numbers: number[];
  all_closed: boolean;
}

export interface RecommendationEffectivenessScore {
  total_recommendations: number;
  implemented_count: number; // Issues closed
  pending_count: number; // Issues still open
  not_created_count: number; // No issue was created
  improved_count: number; // metric_after > metric_before
  no_effect_count: number; // metric_after <= metric_before
  effectiveness_percent: number; // (improved / implemented) * 100
}

export interface RecommendationReport {
  assessed_at: string; // ISO 8601
  effectiveness: RecommendationEffectivenessScore;
  recurring_findings: RecurringFinding[];
  self_assessment: {
    total_health_checks: number;
    avg_finding_count: number;
    finding_count_trend: TrendDirection;
    recommendation_follow_through_rate: number;
    overall_effectiveness: "effective" | "mixed" | "ineffective";
  };
  entries: RecommendationHistoryEntry[];
}

// ── Health Trend Time-Series ──────────────────────────────────────
// @see Issue #1411 - Health trend persistence and dashboard sparklines

export interface HealthTrendEntry {
  schema_version: "1";
  timestamp: string; // ISO 8601 (analyzedAt from HealthAnalysisResult)
  run_id: string; // Derived: analyzedAt timestamp string (unique per analysis)
  issue_number: number; // 0 if unknown
  overall_score: number; // 0-100
  dimensions: Partial<Record<HealthDimension, number>>; // 7 dimension scores
  significant_findings: string[]; // Top 3 finding titles (for hover tooltip)
}

export interface HealthTrendsReadOptions {
  /** Maximum number of most-recent entries to return */
  limit?: number;
  /** Start date for date-range filtering */
  startDate?: Date;
  /** End date for date-range filtering */
  endDate?: Date;
}

// ── Utility ───────────────────────────────────────────────────────

export function getHealthStatus(score: number): HealthStatus {
  if (score >= 90) return "excellent";
  if (score >= 70) return "good";
  if (score >= 50) return "fair";
  if (score >= 30) return "poor";
  return "critical";
}
