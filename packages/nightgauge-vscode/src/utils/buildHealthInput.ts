/**
 * buildHealthInput - Maps AggregatedDataset to HealthAnalysisInput
 *
 * Pure function that converts the VSCode aggregated dataset to the SDK-native
 * HealthAnalysisInput type, without importing from any service class.
 *
 * The executionHistory mapping follows the PipelineHealthRunner pattern:
 * one flat SDK record per pipeline run (not per stage), so the HealthAnalysisEngine
 * receives cost/duration data at run granularity.
 *
 * @see Issue #1570 - Connect real HealthAnalysisResult to health-gated policies
 * @see packages/nightgauge-sdk/src/analysis/health/types.ts
 */

import type { AggregatedDataset } from "../types/aggregation";
import type {
  HealthAnalysisInput,
  HealthScoreEntry,
  ExperimentEntry,
  HealthReportEntry,
} from "@nightgauge/sdk";
import type { ExecutionHistoryRecord as SdkExecutionHistoryRecord } from "@nightgauge/sdk";

/**
 * Map an AggregatedDataset to a HealthAnalysisInput for use by HealthAnalysisEngine.
 *
 * Each source collection is mapped field-by-field to the SDK's types.
 * Missing or empty collections produce empty arrays — the engine handles
 * insufficient-data gracefully.
 *
 * @param dataset - Aggregated telemetry dataset from DataAggregator
 * @returns HealthAnalysisInput ready to pass to HealthAnalysisEngine.analyze()
 */
export function buildHealthInput(dataset: AggregatedDataset): HealthAnalysisInput {
  // Map raw run records to SDK ExecutionHistoryRecord (one per run, not per stage).
  // Matches PipelineHealthRunner.runAnalyzers() mapping approach to avoid circular imports.
  const executionHistory: SdkExecutionHistoryRecord[] = dataset.executionHistory
    .filter((r) => (r as { record_type?: string }).record_type === "run")
    .map((r) => {
      const run = r as unknown as {
        issue_number?: number;
        outcome?: string;
        tokens?: {
          total_input?: number;
          total_output?: number;
          total_cache_read?: number;
          total_cache_creation?: number;
          estimated_cost_usd?: number;
        };
        total_duration_ms?: number;
        recorded_at: string;
      };
      const estimatedCostUsd = run.tokens?.estimated_cost_usd ?? 0;
      const totalTokens = (run.tokens?.total_input ?? 0) + (run.tokens?.total_output ?? 0);
      return {
        issueNumber: run.issue_number ?? 0,
        stage: "pipeline",
        success: run.outcome === "complete",
        retries: 0,
        inputTokens: run.tokens?.total_input ?? 0,
        outputTokens: run.tokens?.total_output ?? 0,
        cacheReadTokens: run.tokens?.total_cache_read,
        cacheCreationTokens: run.tokens?.total_cache_creation,
        costUsd: estimatedCostUsd,
        durationMs: run.total_duration_ms ?? 0,
        timestamp: run.recorded_at,
        isLocalModel: estimatedCostUsd === 0 && totalTokens > 0,
      };
    });

  // Map HealthScoreSnapshot → HealthScoreEntry (fields align directly)
  const healthScores: HealthScoreEntry[] = dataset.healthScores.map((s) => ({
    timestamp: s.timestamp,
    score: s.score,
    status: s.status,
    components: s.components,
    costUsd: s.costUsd,
    issueNumber: s.issueNumber,
  }));

  // Map ExperimentOutcome → ExperimentEntry (snake_case → camelCase)
  const experimentResults: ExperimentEntry[] = dataset.experimentResults.map((e) => ({
    experimentName: e.experiment_name,
    group: e.group,
    issueNumber: e.issue_number,
    stage: e.stage,
    success: e.success,
    costUsd: e.cost_usd,
    durationMs: e.duration_ms,
    recordedAt: e.recorded_at,
  }));

  // Map HealthReport → HealthReportEntry (snake_case summary → camelCase)
  const healthReports: HealthReportEntry[] = dataset.healthReports.map((r) => ({
    createdAt: r.created_at,
    periodDays: r.analysis_period.period_days,
    summary: {
      totalCostUsd: r.summary.total_cost_usd,
      avgCostPerRun: r.summary.avg_cost_per_run,
      totalRuns: r.summary.total_runs,
      successRate: r.summary.success_rate,
      avgDurationMinutes: r.summary.avg_duration_minutes,
      totalTokens: r.summary.total_tokens,
      cacheHitRate: r.summary.cache_hit_rate,
    },
    findingCount: r.findings.length,
    recommendationCount: r.recommendations.length,
  }));

  return {
    executionHistory,
    healthScores,
    selfTuningLog: [],
    experimentResults,
    healthReports,
  };
}
