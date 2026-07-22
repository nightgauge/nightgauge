/**
 * HealthReportGenerator - Structured Report Output for Health Analysis
 *
 * Converts HealthAnalysisResult into JSON, Markdown, and console formats.
 * Supports period-over-period trend comparison and enforces retention.
 *
 * All output is deterministic — no AI interpretation.
 *
 * @see Issue #1105 - Structured Report Output (JSON and Markdown)
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import type {
  HealthAnalysisResult,
  DimensionResult,
  CrossReference,
  Finding,
  RecommendationReport,
  Severity,
  HealthTrendEntry,
} from "./types.js";
import { HealthTrendsWriter } from "./HealthTrendsWriter.js";
import { HealthReportSchema, type HealthReport } from "./reportSchema.js";
import { buildPeriodComparison } from "./statistics.js";
import { SEVERITY_ORDER } from "./severityMapping.js";

// ── Options ──────────────────────────────────────────────────────────

export interface HealthReportOptions {
  baselineResult?: HealthAnalysisResult;
  analysisPeriod?: { startDate: string; endDate: string };
  dataSources?: { name: string; recordCount: number }[];
  analysisDurationMs?: number;
  issueReferences?: {
    findingId: string;
    issueNumber: number;
    issueUrl: string;
  }[];
  recommendationReport?: RecommendationReport;
  /** If provided, append a HealthTrendEntry to .nightgauge/health/trends.jsonl (Issue #1411) */
  workspaceRoot?: string;
  /** Issue number for the trend entry (default: 0) */
  issueNumber?: number;
  /** Retention days for trends.jsonl pruning (default: 90) */
  trendsRetentionDays?: number;
}

export interface WriteReportsResult {
  jsonPath: string;
  markdownPath: string;
}

// ── Severity Badges ──────────────────────────────────────────────────

const SEVERITY_BADGES: Record<Severity, string> = {
  critical: "🔴",
  high: "🟠",
  medium: "🟡",
  low: "🟢",
  info: "ℹ️",
};

const STATUS_BADGES: Record<string, string> = {
  excellent: "🟢",
  good: "🟢",
  fair: "🟡",
  poor: "🟠",
  critical: "🔴",
};

// ── Sparkline Rendering ─────────────────────────────────────────────

const SPARKLINE_BLOCKS = ["▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"];

function renderSparkline(values: number[]): string {
  if (values.length === 0) return "";
  const min = Math.min(...values);
  const max = Math.max(...values);
  const range = max - min;
  if (range === 0) return SPARKLINE_BLOCKS[3].repeat(values.length);
  return values
    .map((v) => {
      const idx = Math.min(
        Math.floor(((v - min) / range) * (SPARKLINE_BLOCKS.length - 1)),
        SPARKLINE_BLOCKS.length - 1
      );
      return SPARKLINE_BLOCKS[idx];
    })
    .join("");
}

// ── Trend Indicators ─────────────────────────────────────────────────

function trendArrow(direction: string): string {
  switch (direction) {
    case "improving":
      return "↑";
    case "degrading":
      return "↓";
    default:
      return "→";
  }
}

// ── Retention ────────────────────────────────────────────────────────

const DEFAULT_MAX_FILES = 20;

// ── Generator Class ──────────────────────────────────────────────────

export class HealthReportGenerator {
  /**
   * Generate a validated JSON report object from analysis results.
   */
  generateJsonReport(
    result: HealthAnalysisResult,
    options: HealthReportOptions = {}
  ): HealthReport {
    const now = new Date().toISOString();
    const period = options.analysisPeriod ?? {
      startDate: now.split("T")[0],
      endDate: now.split("T")[0],
    };

    const periodDays = this.computePeriodDays(period.startDate, period.endDate);

    const dataSources = options.dataSources ?? [];
    const totalRecords = dataSources.reduce((sum, ds) => sum + ds.recordCount, 0);

    // Build dimensions map
    const dimensions: Record<string, HealthReport["dimensions"][string]> = {};
    const allFindings: Finding[] = [];

    for (const [dimKey, dimResult] of Object.entries(result.dimensions)) {
      if (!dimResult) continue;
      allFindings.push(...dimResult.findings);
      dimensions[dimKey] = this.mapDimensionResult(dimResult);
    }

    // Build trend comparison
    const trendComparison = this.buildTrendComparison(result, options);

    // Data quality
    const dimResults = Object.values(result.dimensions).filter(
      (d): d is DimensionResult => d !== undefined
    );
    const sampleSizes = dimResults.map((d) => d.sampleSize);

    const criticalFindings = allFindings.filter((f) => f.severity === "critical").length;

    const report: HealthReport = {
      schema_version: "1.0",
      generated_at: now,
      analysis_period: {
        start_date: period.startDate,
        end_date: period.endDate,
        period_days: periodDays,
      },
      metadata: {
        data_sources: dataSources.map((ds) => ({
          name: ds.name,
          record_count: ds.recordCount,
        })),
        total_records: totalRecords,
        analysis_duration_ms: options.analysisDurationMs ?? 0,
      },
      summary: {
        overall_score: result.overallScore,
        overall_status: result.overallStatus,
        total_findings: allFindings.length,
        critical_findings: criticalFindings,
        cross_references: result.crossReferences.length,
        text: result.summary,
      },
      dimensions,
      cross_references: result.crossReferences.map((cr) => this.mapCrossReference(cr)),
      trend_comparison: trendComparison,
      data_quality: {
        dimensions_with_data: dimResults.filter((d) => d.hasEnoughData).length,
        dimensions_without_data: dimResults.filter((d) => !d.hasEnoughData).length,
        avg_sample_size:
          sampleSizes.length > 0
            ? Math.round(sampleSizes.reduce((a, b) => a + b, 0) / sampleSizes.length)
            : 0,
        lowest_sample_size: sampleSizes.length > 0 ? Math.min(...sampleSizes) : 0,
      },
    };

    // Optional fields
    if (options.issueReferences && options.issueReferences.length > 0) {
      report.issue_references = options.issueReferences.map((ref) => ({
        finding_id: ref.findingId,
        issue_number: ref.issueNumber,
        issue_url: ref.issueUrl,
      }));
    }

    if (options.recommendationReport) {
      const eff = options.recommendationReport.effectiveness;
      report.recommendation_effectiveness = {
        total_recommendations: eff.total_recommendations,
        implemented_count: eff.implemented_count,
        pending_count: eff.pending_count,
        not_created_count: eff.not_created_count,
        improved_count: eff.improved_count,
        no_effect_count: eff.no_effect_count,
        effectiveness_percent: eff.effectiveness_percent,
      };
    }

    // Validate against schema
    HealthReportSchema.parse(report);

    return report;
  }

  /**
   * Generate a GFM Markdown report string.
   */
  generateMarkdownReport(result: HealthAnalysisResult, options: HealthReportOptions = {}): string {
    const lines: string[] = [];
    const statusBadge = STATUS_BADGES[result.overallStatus] ?? "⚪";

    // Executive summary
    lines.push("# Pipeline Health Report");
    lines.push("");
    lines.push(
      `**Overall Score**: ${statusBadge} **${result.overallScore}/100** (${result.overallStatus})`
    );
    lines.push("");
    lines.push(`> ${result.summary}`);
    lines.push("");

    if (options.analysisPeriod) {
      lines.push(
        `**Period**: ${options.analysisPeriod.startDate} to ${options.analysisPeriod.endDate}`
      );
      lines.push("");
    }

    // Dimension sections
    lines.push("## Dimensions");
    lines.push("");

    const dimEntries = Object.entries(result.dimensions)
      .filter(([, d]) => d !== undefined)
      .sort(([, a], [, b]) => (a!.score ?? 0) - (b!.score ?? 0));

    for (const [dimKey, dimResult] of dimEntries) {
      if (!dimResult) continue;
      lines.push(...this.renderDimensionMarkdown(dimKey, dimResult));
      lines.push("");
    }

    // Cross-references
    if (result.crossReferences.length > 0) {
      lines.push("## Cross-Dimension Insights");
      lines.push("");
      for (const cr of result.crossReferences) {
        lines.push(`- ${SEVERITY_BADGES[cr.severity]} **${cr.title}** — ${cr.description}`);
        lines.push(`  - Dimensions: ${cr.dimensions.join(", ")} | Confidence: ${cr.confidence}`);
      }
      lines.push("");
    }

    // Recommendations ranked by severity
    const allFindings = Object.values(result.dimensions)
      .filter((d): d is DimensionResult => d !== undefined)
      .flatMap((d) => d.findings)
      .sort((a, b) => (SEVERITY_ORDER[b.severity] ?? 0) - (SEVERITY_ORDER[a.severity] ?? 0));

    if (allFindings.length > 0) {
      lines.push("## Recommended Actions");
      lines.push("");
      for (const finding of allFindings) {
        lines.push(
          `1. ${SEVERITY_BADGES[finding.severity]} **[${finding.severity.toUpperCase()}]** ${finding.title}`
        );
        lines.push(`   - ${finding.recommendation}`);
      }
      lines.push("");
    }

    // Trend comparison table
    if (options.baselineResult) {
      lines.push("## Trend Comparison");
      lines.push("");
      lines.push("| Dimension | Current | Baseline | Change | Trend |");
      lines.push("|-----------|---------|----------|--------|-------|");

      for (const [dimKey, dimResult] of Object.entries(result.dimensions)) {
        if (!dimResult?.periodComparison) continue;
        const pc = dimResult.periodComparison;
        const arrow = trendArrow(pc.direction);
        const changeStr =
          pc.changePercent >= 0
            ? `+${pc.changePercent.toFixed(1)}%`
            : `${pc.changePercent.toFixed(1)}%`;
        lines.push(
          `| ${dimKey} | ${pc.currentValue.toFixed(1)} | ${pc.baselineValue.toFixed(1)} | ${changeStr} | ${arrow} ${pc.direction} |`
        );
      }
      lines.push("");
    }

    // Recommendation effectiveness
    if (options.recommendationReport) {
      const eff = options.recommendationReport.effectiveness;
      lines.push("## Recommendation Effectiveness");
      lines.push("");
      lines.push(`- Total recommendations: ${eff.total_recommendations}`);
      lines.push(`- Implemented: ${eff.implemented_count}`);
      lines.push(`- Improved: ${eff.improved_count}`);
      lines.push(`- Effectiveness: ${eff.effectiveness_percent.toFixed(1)}%`);
      lines.push("");
    }

    lines.push(`---\n*Generated at ${new Date().toISOString()} by HealthReportGenerator*`);

    return lines.join("\n");
  }

  /**
   * Generate a compact console summary (under 20 lines).
   */
  generateConsoleSummary(result: HealthAnalysisResult, options: HealthReportOptions = {}): string {
    const lines: string[] = [];

    lines.push(
      `Pipeline Health: ${result.overallStatus.toUpperCase()} (${result.overallScore}/100)`
    );
    lines.push("");

    // Dimension scores table
    const dimEntries = Object.entries(result.dimensions).filter(([, d]) => d !== undefined);
    for (const [dimKey, dimResult] of dimEntries) {
      if (!dimResult) continue;
      const bar = renderSparkline([dimResult.score]);
      const trendStr = dimResult.periodComparison
        ? ` ${trendArrow(dimResult.periodComparison.direction)}`
        : "";
      lines.push(
        `  ${dimKey.padEnd(22)} ${String(dimResult.score).padStart(3)}/100 ${bar}${trendStr}`
      );
    }

    lines.push("");

    // Top 3 findings
    const topFindings = Object.values(result.dimensions)
      .filter((d): d is DimensionResult => d !== undefined)
      .flatMap((d) => d.findings)
      .sort((a, b) => (SEVERITY_ORDER[b.severity] ?? 0) - (SEVERITY_ORDER[a.severity] ?? 0))
      .slice(0, 3);

    if (topFindings.length > 0) {
      lines.push("Top findings:");
      for (const f of topFindings) {
        lines.push(`  ${SEVERITY_BADGES[f.severity]} [${f.severity}] ${f.title}`);
      }
    }

    // Baseline trend
    if (options.baselineResult) {
      const scoreChange = result.overallScore - options.baselineResult.overallScore;
      const dir = scoreChange > 0 ? "improving" : scoreChange < 0 ? "degrading" : "stable";
      lines.push("");
      lines.push(
        `Trend: ${trendArrow(dir)} ${dir} (${scoreChange >= 0 ? "+" : ""}${scoreChange} pts)`
      );
    }

    return lines.join("\n");
  }

  /**
   * Write both JSON and Markdown reports to disk and enforce retention.
   */
  async writeReports(
    result: HealthAnalysisResult,
    outputDir: string,
    options: HealthReportOptions = {}
  ): Promise<WriteReportsResult> {
    await fs.mkdir(outputDir, { recursive: true });

    const dateStr = new Date().toISOString().split("T")[0];
    const jsonPath = path.join(outputDir, `health-report-${dateStr}.json`);
    const mdPath = path.join(outputDir, `health-report-${dateStr}.md`);

    const jsonReport = this.generateJsonReport(result, options);
    const mdReport = this.generateMarkdownReport(result, options);

    await fs.writeFile(jsonPath, JSON.stringify(jsonReport, null, 2), "utf-8");
    await fs.writeFile(mdPath, mdReport, "utf-8");

    await this.enforceRetention(outputDir);

    // Append dimension trend entry (Issue #1411) — non-critical, never blocks
    if (options.workspaceRoot) {
      try {
        const entry = buildTrendEntry(result, options.issueNumber ?? 0);
        await HealthTrendsWriter.append(options.workspaceRoot, entry);
        // Lazy prune (non-blocking, ignore errors)
        HealthTrendsWriter.pruneOldEntries(
          options.workspaceRoot,
          options.trendsRetentionDays ?? 90
        ).catch(() => {});
      } catch (err) {
        console.warn("[HealthReportGenerator] Failed to write trend entry:", err);
      }
    }

    return { jsonPath, markdownPath: mdPath };
  }

  /**
   * Enforce retention: keep last `maxFiles` reports per type (JSON and MD).
   * Matches PostPipelineAnalyzer retention pattern.
   */
  async enforceRetention(outputDir: string, maxFiles: number = DEFAULT_MAX_FILES): Promise<void> {
    let entries: string[];
    try {
      entries = await fs.readdir(outputDir);
    } catch {
      return;
    }

    // Enforce per-type: JSON and MD independently
    for (const ext of [".json", ".md"]) {
      const files = entries.filter((e) => e.startsWith("health-report-") && e.endsWith(ext)).sort();

      if (files.length <= maxFiles) continue;

      const toDelete = files.slice(0, files.length - maxFiles);
      for (const file of toDelete) {
        try {
          await fs.unlink(path.join(outputDir, file));
        } catch {
          // Non-critical: skip deletion failures
        }
      }
    }
  }

  // ── Private Helpers ────────────────────────────────────────────────

  private mapDimensionResult(dim: DimensionResult): HealthReport["dimensions"][string] {
    const mapped: HealthReport["dimensions"][string] = {
      dimension: dim.dimension,
      score: dim.score,
      status: dim.status,
      has_enough_data: dim.hasEnoughData,
      sample_size: dim.sampleSize,
      findings: dim.findings.map((f) => ({
        id: f.id,
        severity: f.severity,
        title: f.title,
        description: f.description,
        impact: f.impact,
        recommendation: f.recommendation,
        confidence: f.confidence,
      })),
      metrics: dim.metrics,
    };

    if (dim.periodComparison) {
      mapped.period_comparison = {
        current_value: dim.periodComparison.currentValue,
        baseline_value: dim.periodComparison.baselineValue,
        change_percent: dim.periodComparison.changePercent,
        direction: dim.periodComparison.direction,
        is_significant: dim.periodComparison.isSignificant,
      };
    }

    return mapped;
  }

  private mapCrossReference(cr: CrossReference): HealthReport["cross_references"][number] {
    return {
      id: cr.id,
      dimensions: cr.dimensions,
      severity: cr.severity,
      title: cr.title,
      description: cr.description,
      correlated_findings: cr.correlatedFindings,
      confidence: cr.confidence,
    };
  }

  private buildTrendComparison(
    result: HealthAnalysisResult,
    options: HealthReportOptions
  ): HealthReport["trend_comparison"] {
    if (!options.baselineResult) {
      return { has_baseline: false };
    }

    const baseline = options.baselineResult;
    const overallChange = result.overallScore - baseline.overallScore;
    const overallDirection: "improving" | "stable" | "degrading" =
      overallChange > 0 ? "improving" : overallChange < 0 ? "degrading" : "stable";

    const perDimension: Record<
      string,
      {
        current_value: number;
        baseline_value: number;
        change_percent: number;
        direction: "improving" | "stable" | "degrading";
        is_significant: boolean;
      }
    > = {};

    for (const [dimKey, dimResult] of Object.entries(result.dimensions)) {
      if (!dimResult) continue;
      const baselineDim = baseline.dimensions[dimKey as keyof typeof baseline.dimensions];
      if (!baselineDim) continue;

      const pc = buildPeriodComparison(dimResult.score, baselineDim.score, dimResult.sampleSize);
      perDimension[dimKey] = {
        current_value: pc.currentValue,
        baseline_value: pc.baselineValue,
        change_percent: pc.changePercent,
        direction: pc.direction,
        is_significant: pc.isSignificant,
      };
    }

    return {
      has_baseline: true,
      overall_score_change: overallChange,
      overall_direction: overallDirection,
      per_dimension: Object.keys(perDimension).length > 0 ? perDimension : undefined,
    };
  }

  private computePeriodDays(startDate: string, endDate: string): number {
    const start = new Date(startDate);
    const end = new Date(endDate);
    const diffMs = end.getTime() - start.getTime();
    return Math.max(0, Math.ceil(diffMs / (1000 * 60 * 60 * 24)));
  }

  private renderDimensionMarkdown(dimKey: string, dim: DimensionResult): string[] {
    const lines: string[] = [];
    const badge = STATUS_BADGES[dim.status] ?? "⚪";

    lines.push(`### ${badge} ${dimKey} — ${dim.score}/100 (${dim.status})`);
    lines.push("");

    if (!dim.hasEnoughData) {
      lines.push("_Insufficient data for reliable analysis._");
      lines.push("");
      return lines;
    }

    // Sparkline for score
    if (dim.periodComparison) {
      const sparkValues = [dim.periodComparison.baselineValue, dim.score];
      lines.push(
        `Trend: ${renderSparkline(sparkValues)} ${trendArrow(dim.periodComparison.direction)} (${dim.periodComparison.changePercent >= 0 ? "+" : ""}${dim.periodComparison.changePercent.toFixed(1)}%)`
      );
      lines.push("");
    }

    // Findings
    if (dim.findings.length > 0) {
      for (const finding of dim.findings) {
        lines.push(
          `- ${SEVERITY_BADGES[finding.severity]} **${finding.title}**: ${finding.description}`
        );
      }
      lines.push("");
    }

    return lines;
  }
}

// ── Module-level helpers ──────────────────────────────────────────

/**
 * Build a HealthTrendEntry from a HealthAnalysisResult.
 * @internal used by writeReports() — Issue #1411
 */
function buildTrendEntry(result: HealthAnalysisResult, issueNumber: number): HealthTrendEntry {
  // Extract per-dimension scores
  const dimensions: Partial<Record<string, number>> = {};
  for (const [key, dim] of Object.entries(result.dimensions)) {
    if (dim) {
      dimensions[key] = dim.score;
    }
  }

  // Top 3 finding titles by severity
  const allFindings = Object.values(result.dimensions)
    .filter((d): d is DimensionResult => d !== undefined)
    .flatMap((d) => d.findings)
    .sort((a, b) => (SEVERITY_ORDER[b.severity] ?? 0) - (SEVERITY_ORDER[a.severity] ?? 0))
    .slice(0, 3)
    .map((f) => f.title);

  return {
    schema_version: "1",
    timestamp: result.analyzedAt,
    run_id: result.analyzedAt,
    issue_number: issueNumber,
    overall_score: result.overallScore,
    dimensions,
    significant_findings: allFindings,
  };
}
