/**
 * DataAggregator - Unified data aggregation across all telemetry sources
 *
 * Static utility class (matching ExecutionHistoryReader/PostPipelineAnalyzer pattern)
 * that reads from all 7 data sources, applies date range filtering, and returns
 * a unified AggregatedDataset with data quality metrics.
 *
 * Sources are loaded in parallel via Promise.allSettled(). Failed sources are
 * recorded as warnings in quality metrics, never thrown.
 *
 * @see Issue #1100 - Build Comprehensive Data Aggregation Layer
 * @see docs/ARCHITECTURE.md for utility patterns
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";

import { ExperimentManager, type ExperimentOutcome } from "@nightgauge/sdk";

import { ExecutionHistoryReader } from "../utils/executionHistoryReader";
import { HealthScoreHistoryReader } from "../utils/healthScoreHistory";
import type { ExecutionHistoryRecord } from "../schemas/executionHistory";
import type { HealthScoreSnapshot } from "../schemas/healthScoreHistory";
import type {
  DateRangeFilter,
  AggregatedDataset,
  AggregatedSummary,
  DataQualityMetrics,
  SourceStatus,
  StoredAnalysisReport,
  HealthReport,
} from "../types/aggregation";

const ANALYSIS_DIR = ".nightgauge/analysis";
const EXPERIMENTS_DIR = ".nightgauge/analysis/experiments";
const HEALTH_REPORT_PREFIX = "health-report-";
const PIPELINE_DIR = ".nightgauge/pipeline";

/** Source names used in quality metrics */
const SOURCE_NAMES = {
  executionHistory: "Execution History",
  healthScores: "Health Scores",
  analysisReports: "Analysis Reports",
  experimentResults: "Experiment Results",
  healthReports: "Health Reports",
} as const;

export class DataAggregator {
  /**
   * Parse period/since/until arguments into a DateRangeFilter.
   * Matches the pipeline-health skill's argument semantics.
   *
   * @param options - Date range options
   * @returns Normalized DateRangeFilter with start and end dates
   */
  static parseDateFilter(options: {
    period?: number;
    since?: string;
    until?: string;
  }): DateRangeFilter {
    const endDate = options.until ? new Date(options.until) : new Date();
    endDate.setUTCHours(23, 59, 59, 999);

    let startDate: Date;
    if (options.since) {
      startDate = new Date(options.since);
    } else {
      const periodDays = options.period ?? 30;
      startDate = new Date(endDate);
      startDate.setUTCDate(startDate.getUTCDate() - periodDays);
    }
    startDate.setUTCHours(0, 0, 0, 0);

    return { startDate, endDate };
  }

  /**
   * Aggregate data from ALL sources within a date range.
   * Returns a unified AggregatedDataset.
   * Missing sources are reported in quality metrics, never throw.
   *
   * @param workspaceRoot - Absolute path to repository root
   * @param filter - Date range filter
   * @returns Unified aggregated dataset with quality metrics
   */
  static async aggregate(
    workspaceRoot: string,
    filter: DateRangeFilter
  ): Promise<AggregatedDataset> {
    const sourceWarnings: Record<string, string[]> = {
      executionHistory: [],
      healthScores: [],
      analysisReports: [],
      experimentResults: [],
      healthReports: [],
    };

    // Load all sources in parallel
    const [
      executionHistoryResult,
      healthScoresResult,
      analysisReportsResult,
      experimentResultsResult,
      healthReportsResult,
    ] = await Promise.allSettled([
      this.readExecutionHistory(workspaceRoot, filter),
      this.readHealthScores(workspaceRoot, filter),
      this.readAnalysisReports(workspaceRoot, filter),
      this.readExperimentResults(workspaceRoot, filter),
      this.readHealthReports(workspaceRoot, filter),
    ]);

    // Extract results with graceful fallback
    const executionHistory = this.extractResult(
      executionHistoryResult,
      "executionHistory",
      sourceWarnings
    );
    const healthScores = this.extractResult(healthScoresResult, "healthScores", sourceWarnings);
    const analysisReports = this.extractResult(
      analysisReportsResult,
      "analysisReports",
      sourceWarnings
    );
    const experimentResults = this.extractResult(
      experimentResultsResult,
      "experimentResults",
      sourceWarnings
    );
    const healthReports = this.extractResult(healthReportsResult, "healthReports", sourceWarnings);

    const summary = this.computeSummary(executionHistory);
    const quality = this.computeQualityMetrics(
      filter,
      {
        executionHistory,
        healthScores,
        analysisReports,
        experimentResults,
        healthReports,
      },
      sourceWarnings
    );

    return {
      filter,
      executionHistory,
      healthScores,
      analysisReports,
      experimentResults,
      healthReports,
      summary,
      quality,
    };
  }

  // ---------------------------------------------------------------------------
  // Private: Per-source readers
  // ---------------------------------------------------------------------------

  private static async readExecutionHistory(
    workspaceRoot: string,
    filter: DateRangeFilter
  ): Promise<ExecutionHistoryRecord[]> {
    return ExecutionHistoryReader.readDateRange(workspaceRoot, filter.startDate, filter.endDate);
  }

  private static async readHealthScores(
    workspaceRoot: string,
    filter: DateRangeFilter
  ): Promise<HealthScoreSnapshot[]> {
    return HealthScoreHistoryReader.readDateRange(workspaceRoot, filter.startDate, filter.endDate);
  }

  private static async readAnalysisReports(
    workspaceRoot: string,
    filter: DateRangeFilter
  ): Promise<StoredAnalysisReport[]> {
    const analysisDir = path.join(workspaceRoot, ANALYSIS_DIR);
    const reports: StoredAnalysisReport[] = [];

    let entries: string[];
    try {
      entries = await fs.readdir(analysisDir);
    } catch {
      return reports;
    }

    const analysisFiles = entries.filter((e) => e.startsWith("analysis-") && e.endsWith(".json"));

    for (const file of analysisFiles) {
      try {
        const content = await fs.readFile(path.join(analysisDir, file), "utf-8");
        const parsed = JSON.parse(content) as StoredAnalysisReport;
        if (parsed.created_at) {
          const createdAt = new Date(parsed.created_at);
          if (createdAt >= filter.startDate && createdAt <= filter.endDate) {
            reports.push(parsed);
          }
        }
      } catch {
        // Skip malformed analysis files
        console.warn(`[Nightgauge] Skipping malformed analysis file: ${file}`);
      }
    }

    return reports;
  }

  private static async readExperimentResults(
    workspaceRoot: string,
    filter: DateRangeFilter
  ): Promise<ExperimentOutcome[]> {
    const experimentsDir = path.join(workspaceRoot, EXPERIMENTS_DIR);
    const outcomes: ExperimentOutcome[] = [];

    let entries: string[];
    try {
      entries = await fs.readdir(experimentsDir);
    } catch {
      return outcomes;
    }

    const jsonlFiles = entries.filter((e) => e.endsWith(".jsonl"));

    for (const file of jsonlFiles) {
      const experimentName = file.replace(".jsonl", "");
      try {
        const fileOutcomes = ExperimentManager.readOutcomes(workspaceRoot, experimentName);
        for (const outcome of fileOutcomes) {
          const recordedAt = new Date(outcome.recorded_at);
          if (recordedAt >= filter.startDate && recordedAt <= filter.endDate) {
            outcomes.push(outcome);
          }
        }
      } catch {
        console.warn(`[Nightgauge] Skipping malformed experiment file: ${file}`);
      }
    }

    return outcomes;
  }

  private static async readHealthReports(
    workspaceRoot: string,
    filter: DateRangeFilter
  ): Promise<HealthReport[]> {
    const pipelineDir = path.join(workspaceRoot, PIPELINE_DIR);
    const reports: HealthReport[] = [];

    let entries: string[];
    try {
      entries = await fs.readdir(pipelineDir);
    } catch {
      return reports;
    }

    const healthFiles = entries.filter(
      (e) => e.startsWith(HEALTH_REPORT_PREFIX) && e.endsWith(".json")
    );

    for (const file of healthFiles) {
      try {
        const content = await fs.readFile(path.join(pipelineDir, file), "utf-8");
        const parsed = JSON.parse(content) as HealthReport;
        if (parsed.analysis_period) {
          const reportFrom = new Date(parsed.analysis_period.from);
          const reportTo = new Date(parsed.analysis_period.to);
          // Include if the report period overlaps with the filter range
          if (reportTo >= filter.startDate && reportFrom <= filter.endDate) {
            reports.push(parsed);
          }
        }
      } catch {
        console.warn(`[Nightgauge] Skipping malformed health report: ${file}`);
      }
    }

    return reports;
  }

  // ---------------------------------------------------------------------------
  // Private: Result extraction and summary computation
  // ---------------------------------------------------------------------------

  private static extractResult<T>(
    result: PromiseSettledResult<T[]>,
    sourceName: string,
    warnings: Record<string, string[]>
  ): T[] {
    if (result.status === "fulfilled") {
      return result.value;
    }
    warnings[sourceName].push(
      `Failed to load: ${result.reason instanceof Error ? result.reason.message : String(result.reason)}`
    );
    return [];
  }

  /**
   * Compute summary metrics from execution history run records.
   * Only counts records with record_type === 'run'.
   */
  private static computeSummary(records: ExecutionHistoryRecord[]): AggregatedSummary {
    const runRecords = records.filter((r) => r.record_type === "run");

    const totalRuns = runRecords.length;
    let successfulRuns = 0;
    let failedRuns = 0;
    let totalCost = 0;
    let totalInput = 0;
    let totalOutput = 0;
    let totalCacheRead = 0;
    let totalCacheCreation = 0;
    let totalDuration = 0;

    for (const r of runRecords) {
      // Run records have outcome, tokens, total_duration_ms
      const run = r as {
        outcome: string;
        tokens: {
          estimated_cost_usd: number;
          total_input: number;
          total_output: number;
          total_cache_read: number;
          total_cache_creation: number;
        };
        total_duration_ms: number;
      };

      if (run.outcome === "complete") successfulRuns++;
      if (run.outcome === "failed") failedRuns++;

      totalCost += run.tokens.estimated_cost_usd;
      totalInput += run.tokens.total_input;
      totalOutput += run.tokens.total_output;
      totalCacheRead += run.tokens.total_cache_read;
      totalCacheCreation += run.tokens.total_cache_creation;
      totalDuration += run.total_duration_ms;
    }

    return {
      totalRuns,
      successfulRuns,
      failedRuns,
      successRate: totalRuns > 0 ? successfulRuns / totalRuns : 0,
      totalCostUsd: totalCost,
      avgCostPerRun: totalRuns > 0 ? totalCost / totalRuns : 0,
      totalInputTokens: totalInput,
      totalOutputTokens: totalOutput,
      totalCacheReadTokens: totalCacheRead,
      totalCacheCreationTokens: totalCacheCreation,
      avgDurationMs: totalRuns > 0 ? totalDuration / totalRuns : 0,
    };
  }

  /**
   * Compute data quality metrics across all sources.
   * Reports per-source availability, record counts, date ranges, and gap days.
   */
  private static computeQualityMetrics(
    filter: DateRangeFilter,
    data: {
      executionHistory: ExecutionHistoryRecord[];
      healthScores: HealthScoreSnapshot[];
      analysisReports: StoredAnalysisReport[];
      experimentResults: ExperimentOutcome[];
      healthReports: HealthReport[];
    },
    warnings: Record<string, string[]>
  ): DataQualityMetrics {
    const sources: SourceStatus[] = [];

    // Build source status for each source
    sources.push(
      this.buildSourceStatus(
        SOURCE_NAMES.executionHistory,
        data.executionHistory,
        (r) => r.recorded_at,
        warnings.executionHistory
      )
    );
    sources.push(
      this.buildSourceStatus(
        SOURCE_NAMES.healthScores,
        data.healthScores,
        (r) => r.timestamp,
        warnings.healthScores
      )
    );
    sources.push(
      this.buildSourceStatus(
        SOURCE_NAMES.analysisReports,
        data.analysisReports,
        (r) => r.created_at,
        warnings.analysisReports
      )
    );
    sources.push(
      this.buildSourceStatus(
        SOURCE_NAMES.experimentResults,
        data.experimentResults,
        (r) => r.recorded_at,
        warnings.experimentResults
      )
    );
    sources.push(
      this.buildSourceStatus(
        SOURCE_NAMES.healthReports,
        data.healthReports,
        (r) => r.created_at,
        warnings.healthReports
      )
    );

    const totalRecords = sources.reduce((sum, s) => sum + s.recordCount, 0);
    const sourcesFound = sources.filter((s) => s.available).length;
    const sourcesMissing = sources.filter((s) => !s.available).length;

    // Compute overall date coverage
    const allDates = sources
      .filter((s) => s.dateRange !== null)
      .flatMap((s) => [s.dateRange!.earliest, s.dateRange!.latest]);
    const dateRangeCovered =
      allDates.length > 0
        ? {
            start: allDates.sort()[0],
            end: allDates.sort()[allDates.length - 1],
          }
        : null;

    // Compute gap days — dates in the filter range with no data from any source
    const gapDays = this.computeGapDays(filter, data);

    return {
      totalRecords,
      sourcesFound,
      sourcesMissing,
      sources,
      dateRangeRequested: {
        start: filter.startDate.toISOString().split("T")[0],
        end: filter.endDate.toISOString().split("T")[0],
      },
      dateRangeCovered,
      gapDays,
    };
  }

  /**
   * Build a SourceStatus for a single data source.
   */
  private static buildSourceStatus<T>(
    name: string,
    records: T[],
    getTimestamp: (record: T) => string,
    warnings: string[]
  ): SourceStatus {
    if (records.length === 0) {
      return {
        name,
        available: warnings.length === 0, // Available but empty vs failed
        recordCount: 0,
        dateRange: null,
        warnings,
      };
    }

    const timestamps = records.map(getTimestamp).sort();
    return {
      name,
      available: true,
      recordCount: records.length,
      dateRange: {
        earliest: timestamps[0],
        latest: timestamps[timestamps.length - 1],
      },
      warnings,
    };
  }

  /**
   * Find gap days — dates in the filter range where no source has data.
   * Collects all unique dates from all sources and identifies missing days.
   */
  private static computeGapDays(
    filter: DateRangeFilter,
    data: {
      executionHistory: ExecutionHistoryRecord[];
      healthScores: HealthScoreSnapshot[];
      analysisReports: StoredAnalysisReport[];
      experimentResults: ExperimentOutcome[];
      healthReports: HealthReport[];
    }
  ): string[] {
    // Collect all dates (YYYY-MM-DD) where data exists
    const datesWithData = new Set<string>();

    for (const r of data.executionHistory) {
      datesWithData.add(r.recorded_at.split("T")[0]);
    }
    for (const r of data.healthScores) {
      datesWithData.add(r.timestamp.split("T")[0]);
    }
    for (const r of data.analysisReports) {
      datesWithData.add(r.created_at.split("T")[0]);
    }
    for (const r of data.experimentResults) {
      datesWithData.add(r.recorded_at.split("T")[0]);
    }
    for (const r of data.healthReports) {
      datesWithData.add(r.created_at.split("T")[0]);
    }

    // If no data at all, don't report every day as a gap
    if (datesWithData.size === 0) {
      return [];
    }

    // Walk through each day in the filter range
    const gaps: string[] = [];
    const current = new Date(filter.startDate);
    current.setUTCHours(0, 0, 0, 0);
    const end = new Date(filter.endDate);
    end.setUTCHours(0, 0, 0, 0);

    while (current <= end) {
      const dateStr = current.toISOString().split("T")[0];
      if (!datesWithData.has(dateStr)) {
        gaps.push(dateStr);
      }
      current.setUTCDate(current.getUTCDate() + 1);
    }

    return gaps;
  }
}
