/**
 * Type definitions for the Data Aggregation Layer
 *
 * Defines interfaces for the unified aggregated dataset,
 * date range filtering, data quality metrics, and source status reporting.
 *
 * @see Issue #1100 - Build Comprehensive Data Aggregation Layer
 */

import type { ExecutionHistoryRecord } from "../schemas/executionHistory";
import type { HealthScoreSnapshot } from "../schemas/healthScoreHistory";
import type { ExperimentOutcome } from "@nightgauge/sdk";

/** Date range filter for aggregation queries */
export interface DateRangeFilter {
  startDate: Date;
  endDate: Date;
}

/** Status of a single data source after aggregation */
export interface SourceStatus {
  name: string;
  available: boolean;
  recordCount: number;
  dateRange: { earliest: string; latest: string } | null;
  warnings: string[];
}

/** Data quality metrics across all sources */
export interface DataQualityMetrics {
  totalRecords: number;
  sourcesFound: number;
  sourcesMissing: number;
  sources: SourceStatus[];
  dateRangeRequested: { start: string; end: string };
  dateRangeCovered: { start: string; end: string } | null;
  gapDays: string[];
}

/** Minimal health report structure from health-report-*.json files */
export interface HealthReport {
  schema_version: string;
  analysis_period: {
    from: string;
    to: string;
    period_days: number;
    data_sources_found: number;
    data_sources_missing: number;
  };
  summary: {
    total_cost_usd: number;
    avg_cost_per_run: number;
    total_runs: number;
    success_rate: number;
    avg_duration_minutes: number;
    total_tokens: number;
    cache_hit_rate: number;
  };
  findings: Array<{
    id: string;
    dimension: string;
    severity: string;
    title: string;
    description: string;
  }>;
  recommendations: Array<{
    id: string;
    priority: string;
    title: string;
    description: string;
  }>;
  created_at: string;
}

/** Stored post-pipeline analysis file structure */
export interface StoredAnalysisReport {
  issue_number: number;
  pipeline_completion_time: string;
  analysis: unknown;
  failure_analysis?: unknown;
  auto_tune_applied: Array<{
    field: string;
    previousValue: number;
    newValue: number;
    rationale: string;
  }>;
  created_at: string;
}

/** The unified aggregated dataset returned by DataAggregator */
export interface AggregatedDataset {
  filter: DateRangeFilter;
  executionHistory: ExecutionHistoryRecord[];
  healthScores: HealthScoreSnapshot[];
  analysisReports: StoredAnalysisReport[];
  experimentResults: ExperimentOutcome[];
  healthReports: HealthReport[];
  summary: AggregatedSummary;
  quality: DataQualityMetrics;
}

/** Pre-computed summary from the aggregated data */
export interface AggregatedSummary {
  totalRuns: number;
  successfulRuns: number;
  failedRuns: number;
  successRate: number;
  totalCostUsd: number;
  avgCostPerRun: number;
  totalInputTokens: number;
  totalOutputTokens: number;
  totalCacheReadTokens: number;
  totalCacheCreationTokens: number;
  avgDurationMs: number;
}
