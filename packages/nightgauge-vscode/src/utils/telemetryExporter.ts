/**
 * TelemetryExporter - Format conversion for pipeline execution history
 *
 * Pure utility functions for converting JSONL execution history records
 * to CSV and JSON formats for external analysis. No VSCode API dependency.
 *
 * Supports three export formats:
 * - JSON: Full JSONL records as a pretty-printed JSON array
 * - CSV Runs: One row per pipeline run with aggregated metrics
 * - CSV Stages: One row per stage per run for granular analysis
 *
 * @see Issue #1010 - Telemetry Analytics Export
 * @see docs/ARCHITECTURE.md for utility patterns
 */

import type {
  ExecutionHistoryRecord,
  ExecutionHistoryRunRecordV2,
} from "../schemas/executionHistory";

export type ExportFormat = "json" | "csv-runs" | "csv-stages";

/**
 * Escape a CSV field per RFC 4180.
 * Double-quotes fields containing commas, double quotes, or newlines.
 */
function escapeCsvField(value: string): string {
  if (value.includes(",") || value.includes('"') || value.includes("\n") || value.includes("\r")) {
    return `"${value.replace(/"/g, '""')}"`;
  }
  return value;
}

/**
 * Filter records to only run records (v2 normalized).
 */
function filterRunRecords(records: ExecutionHistoryRecord[]): ExecutionHistoryRunRecordV2[] {
  return records.filter((r) => r.record_type === "run") as ExecutionHistoryRunRecordV2[];
}

/**
 * Export records as a pretty-printed JSON array.
 *
 * Returns the full JSONL records preserving all fields including
 * v2 additions (tool_calls, routing, model_selection).
 */
export function exportAsJson(records: ExecutionHistoryRecord[]): string {
  return JSON.stringify(records, null, 2);
}

/**
 * Export run records as CSV with one row per pipeline run.
 *
 * Columns cover issue metadata, timing, cost, tokens, cache,
 * model info, and routing details.
 */
export function exportAsCsvRuns(records: ExecutionHistoryRecord[]): string {
  const runs = filterRunRecords(records);

  const headers = [
    "issue_number",
    "title",
    "outcome",
    "outcome_type",
    "started_at",
    "completed_at",
    "duration_ms",
    "total_cost_usd",
    "total_input_tokens",
    "total_output_tokens",
    "cache_read_tokens",
    "cache_creation_tokens",
    "cache_hit_rate",
    "model",
    "stage_count",
    "tool_call_count",
    "files_read",
    "files_written",
    "routing_complexity",
    "routing_path",
    "size",
    "type",
    "priority",
    "execution_mode",
    "ptc_programmatic_calls",
    "ptc_direct_calls",
    "ptc_programmatic_ratio",
    "ptc_estimated_tokens_saved",
    "ptc_code_execution_count",
    "ptc_container_reuse_count",
  ];

  const rows = runs.map((run) => {
    const totalInput = run.tokens.total_input;
    const cacheRead = run.tokens.total_cache_read;
    const denominator = totalInput + cacheRead;
    const cacheHitRate = denominator > 0 ? cacheRead / denominator : 0;

    // Find primary model from heaviest stage (feature-dev or feature-planning)
    const primaryModel = getPrimaryModel(run);

    // Count non-pending stages
    const stageCount = Object.values(run.stages).filter((s) => s.status !== "pending").length;

    const values = [
      String(run.issue_number),
      escapeCsvField(run.title),
      run.outcome,
      run.outcome_type ?? "",
      run.started_at,
      run.completed_at,
      String(run.total_duration_ms),
      run.tokens.estimated_cost_usd.toFixed(6),
      String(totalInput),
      String(run.tokens.total_output),
      String(cacheRead),
      String(run.tokens.total_cache_creation),
      cacheHitRate.toFixed(4),
      primaryModel,
      String(stageCount),
      String(run.tool_calls?.length ?? 0),
      String(run.files.read_count),
      String(run.files.written_count),
      String(run.routing.complexity_score),
      escapeCsvField(run.routing.path),
      run.size ?? "",
      run.type ?? "",
      run.priority ?? "",
      run.execution_mode,
      String(run.tokens.ptc_metrics?.programmatic_calls ?? ""),
      String(run.tokens.ptc_metrics?.direct_calls ?? ""),
      run.tokens.ptc_metrics?.programmatic_ratio != null
        ? run.tokens.ptc_metrics.programmatic_ratio.toFixed(4)
        : "",
      String(run.tokens.ptc_metrics?.estimated_tokens_saved ?? ""),
      String(run.tokens.ptc_metrics?.code_execution_count ?? ""),
      String(run.tokens.ptc_metrics?.container_reuse_count ?? ""),
    ];

    return values.join(",");
  });

  return [headers.join(","), ...rows].join("\n");
}

/**
 * Export run records as CSV with one row per stage per run.
 *
 * Provides granular per-stage token breakdown for cost optimization analysis.
 */
export function exportAsCsvStages(records: ExecutionHistoryRecord[]): string {
  const runs = filterRunRecords(records);

  const headers = [
    "issue_number",
    "stage",
    "status",
    "duration_ms",
    "input_tokens",
    "output_tokens",
    "cache_read",
    "cache_creation",
    "cost_usd",
    "model",
    "model_source",
    "context_file_size_bytes",
    "error",
  ];

  const rows: string[] = [];

  for (const run of runs) {
    for (const [stageName, stageDetail] of Object.entries(run.stages)) {
      const tokenData = run.tokens.per_stage?.[stageName as keyof typeof run.tokens.per_stage];

      const values = [
        String(run.issue_number),
        stageName,
        stageDetail.status,
        String(stageDetail.duration_ms ?? ""),
        String(tokenData?.input ?? ""),
        String(tokenData?.output ?? ""),
        String(tokenData?.cache_read ?? ""),
        String(tokenData?.cache_creation ?? ""),
        tokenData?.cost_usd != null ? tokenData.cost_usd.toFixed(6) : "",
        tokenData?.model ?? stageDetail.model_selection?.model ?? "",
        tokenData?.model_source ?? stageDetail.model_selection?.source ?? "",
        String(stageDetail.context_file_size_bytes ?? ""),
        escapeCsvField(stageDetail.error ?? ""),
      ];

      rows.push(values.join(","));
    }
  }

  return [headers.join(","), ...rows].join("\n");
}

/**
 * Get the primary model from the heaviest stage (feature-dev, then feature-planning).
 */
function getPrimaryModel(run: ExecutionHistoryRunRecordV2): string {
  const preferredStages = ["feature-dev", "feature-planning"] as const;

  for (const stageName of preferredStages) {
    const tokenData = run.tokens.per_stage?.[stageName];
    if (tokenData?.model) return tokenData.model;

    const stageDetail = run.stages[stageName as keyof typeof run.stages];
    if (stageDetail?.model_selection?.model) {
      return stageDetail.model_selection.model;
    }
  }

  return "";
}
