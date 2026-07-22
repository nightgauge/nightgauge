/**
 * Pipeline Health Check types
 *
 * TypeScript interfaces for health check results, findings, and report structure.
 * Used by PipelineHealthRunner service and the Dashboard health report section.
 *
 * @see Issue #1104 - Pipeline Health VSCode Command & Dashboard Integration
 */

/** Health check run parameters (from quick pick) */
export interface HealthCheckParams {
  period: number; // days (7, 30, etc.)
  severity: HealthSeverity;
  dryRun: boolean;
}

/** Severity levels for health findings */
export type HealthSeverity = "info" | "warning" | "high" | "critical";

/** A single health finding */
export interface HealthFinding {
  id: string;
  dimension: string; // token-economics, cost-health, stage-effectiveness, etc.
  severity: HealthSeverity;
  title: string;
  description: string;
  evidence: string[]; // specific data points
  impact: string;
  recommendation: string;
}

/** Complete health check report */
export interface HealthCheckReport {
  schema_version: "1.0";
  analysis_period: {
    from: string;
    to: string;
    period_days: number;
  };
  data_quality: {
    sources_found: number;
    sources_missing: number;
    gap_days: number;
  };
  summary: {
    total_runs: number;
    success_rate: number;
    total_cost_usd: number;
    avg_cost_per_run: number;
    avg_duration_ms: number;
    cache_hit_rate: number;
  };
  findings: HealthFinding[];
  findings_by_severity: Record<HealthSeverity, number>;
  created_at: string;
}
