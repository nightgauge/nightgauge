/**
 * PipelineHealthRunner - Orchestrates pipeline health analysis
 *
 * Static utility class (matching DataAggregator pattern) that aggregates data,
 * runs SDK analyzers, and produces a HealthCheckReport with findings.
 *
 * All SDK imports wrapped in try/catch for graceful degradation when SDK dist
 * is unavailable (matching HealthWidget pattern).
 *
 * @see Issue #1104 - Pipeline Health VSCode Command & Dashboard Integration
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";

import { DataAggregator } from "./DataAggregator";
import type { AggregatedDataset, AggregatedSummary } from "../types/aggregation";
import type {
  HealthCheckParams,
  HealthCheckReport,
  HealthFinding,
  HealthSeverity,
} from "../types/pipelineHealth";

const PIPELINE_DIR = ".nightgauge/pipeline";

export class PipelineHealthRunner {
  /**
   * Run a complete pipeline health analysis.
   *
   * @param workspaceRoot - Absolute path to the repository root
   * @param params - Health check parameters (period, severity filter, dryRun)
   * @returns Complete health check report
   */
  static async run(workspaceRoot: string, params: HealthCheckParams): Promise<HealthCheckReport> {
    // 1. Compute date range from params
    const filter = DataAggregator.parseDateFilter({ period: params.period });

    // 2. Aggregate data from all sources
    const dataset = await DataAggregator.aggregate(workspaceRoot, filter);

    // 3. Run SDK analyzers (graceful degradation)
    const analyzerFindings = await this.runAnalyzers(dataset);

    // 4. Filter findings by severity threshold
    const severityOrder: HealthSeverity[] = ["critical", "high", "warning", "info"];
    const thresholdIndex = severityOrder.indexOf(params.severity);
    const filteredFindings = analyzerFindings.filter((f) => {
      const findingIndex = severityOrder.indexOf(f.severity);
      return findingIndex <= thresholdIndex;
    });

    // 5. Compute summary from aggregated data
    const summary = this.computeSummary(dataset.summary, dataset);

    // 6. Count findings by severity
    const findings_by_severity: Record<HealthSeverity, number> = {
      critical: 0,
      high: 0,
      warning: 0,
      info: 0,
    };
    for (const finding of filteredFindings) {
      findings_by_severity[finding.severity]++;
    }

    // 7. Build the report
    const report: HealthCheckReport = {
      schema_version: "1.0",
      analysis_period: {
        from: filter.startDate.toISOString().split("T")[0],
        to: filter.endDate.toISOString().split("T")[0],
        period_days: params.period,
      },
      data_quality: {
        sources_found: dataset.quality.sourcesFound,
        sources_missing: dataset.quality.sourcesMissing,
        gap_days: dataset.quality.gapDays.length,
      },
      summary,
      findings: filteredFindings,
      findings_by_severity,
      created_at: new Date().toISOString(),
    };

    // 8. Write report file (unless dry run)
    if (!params.dryRun) {
      await this.writeReport(workspaceRoot, report);
    }

    return report;
  }

  /**
   * Run SDK analyzers on aggregated data.
   * Gracefully degrades if SDK dist is unavailable.
   */
  private static async runAnalyzers(dataset: AggregatedDataset): Promise<HealthFinding[]> {
    const findings: HealthFinding[] = [];

    // Convert execution history to SDK-compatible format
    const records = dataset.executionHistory
      .filter((r) => r.record_type === "run")
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
        return {
          issueNumber: run.issue_number ?? 0,
          stage: "pipeline" as string,
          success: run.outcome === "complete",
          retries: 0,
          inputTokens: run.tokens?.total_input ?? 0,
          outputTokens: run.tokens?.total_output ?? 0,
          cacheReadTokens: run.tokens?.total_cache_read,
          cacheCreationTokens: run.tokens?.total_cache_creation,
          costUsd: run.tokens?.estimated_cost_usd ?? 0,
          durationMs: run.total_duration_ms ?? 0,
          timestamp: run.recorded_at,
        };
      });

    if (records.length < 2) {
      findings.push({
        id: "insufficient-data",
        dimension: "data-quality",
        severity: "info",
        title: "Insufficient pipeline data",
        description: `Only ${records.length} pipeline run(s) found. Most analyses require at least 3 runs.`,
        evidence: [`${records.length} run records in the selected period`],
        impact: "Analysis accuracy is reduced with limited data",
        recommendation: "Run more pipelines to build up analysis data",
      });
      return findings;
    }

    // Run TokenEfficiencyAnalyzer
    try {
      const { TokenEfficiencyAnalyzer } =
        await import("@nightgauge/sdk/dist/analysis/TokenEfficiencyAnalyzer");
      const analyzer = new TokenEfficiencyAnalyzer();
      const result = analyzer.analyze(records);

      if (result.wastePatterns) {
        for (const pattern of result.wastePatterns) {
          findings.push({
            id: `token-${pattern.category ?? "unknown"}`,
            dimension: "token-economics",
            severity: this.mapSeverity(pattern.severity),
            title:
              pattern.category?.replace(/-/g, " ").replace(/^\w/, (c: string) => c.toUpperCase()) ??
              "Token waste pattern",
            description: pattern.description ?? `Token waste detected: ${pattern.category}`,
            evidence: [`Estimated savings: $${(pattern.estimatedSavingsUsd ?? 0).toFixed(4)}`],
            impact: `Potential cost reduction of $${(pattern.estimatedSavingsUsd ?? 0).toFixed(4)} per run`,
            recommendation: pattern.action?.label ?? "Review token usage patterns",
          });
        }
      }
    } catch {
      // SDK TokenEfficiencyAnalyzer not available — graceful degradation
    }

    // Run FailurePatternDetector
    try {
      const { FailurePatternDetector } =
        await import("@nightgauge/sdk/dist/analysis/FailurePatternDetector");
      const detector = new FailurePatternDetector();
      const result = detector.analyze(records);

      if (result.findings) {
        for (const finding of result.findings) {
          findings.push({
            id: `failure-${finding.category ?? "unknown"}`,
            dimension: "failure-patterns",
            severity: this.mapFailureSeverity(finding.severity),
            title: finding.category ?? "Failure pattern detected",
            description: finding.description ?? "A recurring failure pattern was detected",
            evidence: finding.affectedStages
              ? [`Affected stages: ${finding.affectedStages.join(", ")}`]
              : [],
            impact: "Pipeline reliability may be impacted",
            recommendation: "Investigate the affected stages for root cause",
          });
        }
      }
    } catch {
      // SDK FailurePatternDetector not available — graceful degradation
    }

    return findings;
  }

  /**
   * Compute report summary from aggregated data.
   */
  private static computeSummary(
    aggSummary: AggregatedSummary,
    dataset: AggregatedDataset
  ): HealthCheckReport["summary"] {
    // Compute cache hit rate from execution history
    let cacheHitRate = 0;
    const totalInput = aggSummary.totalInputTokens + aggSummary.totalCacheReadTokens;
    if (totalInput > 0) {
      cacheHitRate = aggSummary.totalCacheReadTokens / totalInput;
    }

    return {
      total_runs: aggSummary.totalRuns,
      success_rate: aggSummary.successRate,
      total_cost_usd: aggSummary.totalCostUsd,
      avg_cost_per_run: aggSummary.avgCostPerRun,
      avg_duration_ms: aggSummary.avgDurationMs,
      cache_hit_rate: cacheHitRate,
    };
  }

  /**
   * Write health report to disk.
   */
  private static async writeReport(
    workspaceRoot: string,
    report: HealthCheckReport
  ): Promise<string> {
    const dir = path.join(workspaceRoot, PIPELINE_DIR);
    await fs.mkdir(dir, { recursive: true });

    const dateStr = new Date().toISOString().split("T")[0];
    const filePath = path.join(dir, `health-report-${dateStr}.json`);
    await fs.writeFile(filePath, JSON.stringify(report, null, 2), "utf-8");
    return filePath;
  }

  /**
   * Map SDK token efficiency severity to HealthSeverity.
   */
  private static mapSeverity(sdkSeverity?: string): HealthSeverity {
    switch (sdkSeverity) {
      case "critical":
        return "critical";
      case "high":
        return "high";
      case "medium":
        return "warning";
      default:
        return "info";
    }
  }

  /**
   * Map SDK failure pattern severity to HealthSeverity.
   */
  private static mapFailureSeverity(sdkSeverity?: string): HealthSeverity {
    switch (sdkSeverity) {
      case "infrastructure":
        return "critical";
      case "manual-fix":
        return "high";
      case "transient":
        return "warning";
      default:
        return "info";
    }
  }
}
