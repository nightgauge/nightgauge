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
import type { ControlBandResult } from "./controlBands.js";
import type { SkillEffectivenessEntry } from "../skill-effectiveness-types.js";

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

/**
 * Status of the OVERALL score. `"no-data"` is the honest answer when no
 * dimension had enough data to score (#1197) — the overall is then `null`,
 * never a fabricated 0 (critical) or 50 (fair).
 */
export type OverallHealthStatus = HealthStatus | "no-data";

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
  /** Why hasEnoughData is false (named in the series' excluded_dimensions, #1900). */
  insufficientDataReason?: string;
  periodComparison?: PeriodComparison;
  /** Control-band outcome for the latest run (cost-health only, #1286). */
  controlBand?: ControlBandResult;
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
  /**
   * Weighted mean over the dimensions that HAVE data, re-normalised to the
   * surviving weights; `null` when none does. A dimension with
   * `hasEnoughData: false` never contributes (#1197). The rule is pinned
   * against `skills/nightgauge-pipeline-health/overall-score-corpus.json`.
   */
  overallScore: number | null;
  overallStatus: OverallHealthStatus;
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
  /**
   * Learning-effectiveness inputs (#1900). Absent → the dimension reports
   * `hasEnoughData: false` naming the missing file.
   */
  learningSignals?: LearningSignals;
  /**
   * Skill-drift input (#1900): the latest record per (skill_file, commit_hash)
   * from `<git-dir>/nightgauge-worktree/health/skill-effectiveness.jsonl`
   * (`SkillEffectivenessAnalyzer` entries, persisted by the extension),
   * restricted to changes inside the analysis period. Absent → excluded.
   */
  skillEffectiveness?: SkillEffectivenessEntry[];
  /**
   * Per-change outcome inputs (#1284): survival verdicts, backtrack trace
   * events and plan-versus-merge file lists. Absent → the per-change metrics
   * are omitted, never reported as 0. They are unscored.
   */
  changeSignals?: ChangeSignals;
}

/** One folded row of `survival-records.jsonl` (Go `survival.Record`). */
export interface SurvivalVerdictEntry {
  issueNumber: number;
  prNumber: number;
  mergeCommitSha: string;
  mergedAt: string;
  verdict: "pending" | "survived" | "reverted" | "broke" | "unobserved";
}

/** One `kind: "backtrack"` trace event (ADR 013). */
export interface BacktrackEntry {
  issueNumber: number;
  runId: string;
  ts: string;
}

/**
 * Files a change's plan named (`planning-{N}.json` `files_to_create` +
 * `files_to_modify`; null when no planning context is readable) against the
 * files its merge commit touched.
 */
export interface PlanComparisonEntry {
  issueNumber: number;
  plannedFiles: string[] | null;
  mergedFiles: string[];
}

export interface ChangeSignals {
  survival: SurvivalVerdictEntry[];
  backtracks: BacktrackEntry[];
  planComparisons: PlanComparisonEntry[];
}

/**
 * The predicted/actual pair fields of one row of
 * `<clone>/pipeline/history/outcomes.jsonl` (Go `learning.Outcome`). An empty
 * string is an unknown: it is excluded from every accuracy denominator (#340)
 * and counted as a coverage gap (#304), never as a miss.
 */
export interface LearningOutcomeEntry {
  /** `completedAt` */
  completedAt: string;
  /** `predictedModel` — registry band, or "" */
  predictedModel: string;
  /** `actualModel` — band the implementation stage served, or "" */
  actualModel: string;
  /** `predictedSize` — small|medium|large, or "" */
  predictedSize: string;
  /** `actualSize` — small|medium|large measured from lines changed, or "" */
  actualSize: string;
}

export interface LearningSignals {
  /** Outcome rows inside the analysis period. */
  outcomes: LearningOutcomeEntry[];
  /** `updated_at` of `<clone>/pipeline/calibration.json`; null when absent. */
  calibrationUpdatedAt: string | null;
}

/**
 * One scored point of the health series (#1898). Entries whose
 * `overall_score` is null are not points and never appear here.
 */
export interface HealthScoreEntry {
  timestamp: string;
  score: number;
  issueNumber: number;
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
// The ONE persisted health series (#1898): one entry per pipeline run, at the
// checkout's `health/trends.jsonl`, written by `recordHealthTrend` and read by
// the dashboard, `nightgauge health trends` and `loopverdicts`. See
// docs/HEALTH_MONITORING.md § The health series.

export interface HealthTrendEntry {
  schema_version: "2";
  timestamp: string; // ISO 8601 (analyzedAt from HealthAnalysisResult)
  run_id: string; // The pipeline run this entry scores
  issue_number: number; // 0 if unknown
  /** 0-100, or null when no dimension had data (#1197). Never coerced. */
  overall_score: number | null;
  /** `getOverallHealthStatus(overall_score)`; `"no-data"` exactly when null. */
  status: OverallHealthStatus;
  /** Scores of the dimensions that CONTRIBUTED. A starved one is absent. */
  dimensions: Partial<Record<HealthDimension, number>>;
  /** Every dimension that did not contribute, with the reason it did not. */
  excluded_dimensions: Partial<Record<HealthDimension, string>>;
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
