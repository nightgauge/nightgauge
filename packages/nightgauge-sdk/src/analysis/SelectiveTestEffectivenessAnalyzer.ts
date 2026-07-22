/**
 * SelectiveTestEffectivenessAnalyzer — rolling-window analysis of selective
 * test effectiveness.
 *
 * Reads `SelectiveTestMetricRecord` entries from a `SelectiveTestMetricsCollector`,
 * computes rolling-window statistics (weekly default), and alerts when the
 * escaped defect rate exceeds a configurable threshold.
 *
 * @see Issue #1975 - Validation & Cost Tracking
 */

import type { SelectiveTestMetricsCollector } from "./SelectiveTestMetricsCollector.js";
import type {
  SelectiveTestMetricRecord,
  SelectiveTestEffectivenessResult,
} from "./selective-test-metrics-types.js";

// ---------------------------------------------------------------------------
// Defaults
// ---------------------------------------------------------------------------

const DEFAULT_WINDOW_DAYS = 7;
const DEFAULT_ESCAPED_DEFECT_THRESHOLD = 0.02;

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

export interface SelectiveTestEffectivenessAnalyzerConfig {
  /** Rolling window in days. Default: 7 */
  windowDays?: number;
  /** Alert threshold for escaped defect rate (0.0–1.0). Default: 0.02 */
  escapedDefectThreshold?: number;
}

// ---------------------------------------------------------------------------
// SelectiveTestEffectivenessAnalyzer
// ---------------------------------------------------------------------------

export class SelectiveTestEffectivenessAnalyzer {
  private readonly windowDays: number;
  private readonly threshold: number;

  constructor(
    private readonly collector: SelectiveTestMetricsCollector,
    config?: SelectiveTestEffectivenessAnalyzerConfig
  ) {
    this.windowDays = config?.windowDays ?? DEFAULT_WINDOW_DAYS;
    this.threshold = config?.escapedDefectThreshold ?? DEFAULT_ESCAPED_DEFECT_THRESHOLD;
  }

  /**
   * Analyze the rolling window of selective test runs.
   *
   * Note: escaped defect counts are derived from records where `record_type`
   * would indicate a gap — but gap records live in a separate `graph-gaps.jsonl`
   * file (written by EscapedDefectDetector). This analyzer treats any metric
   * record with `selected_tests < total_tests` as a selective run and computes
   * savings from that data. The `escaped_defects` value must be passed in
   * externally if available (defaults to 0 when not provided).
   *
   * @param escapedDefectCount - Count of escaped defects in the window (optional)
   */
  async analyze(escapedDefectCount = 0): Promise<SelectiveTestEffectivenessResult> {
    const windowRecords = await this.collector.readWindow(this.windowDays);

    if (windowRecords.length === 0) {
      return this.zeroResult(escapedDefectCount);
    }

    const selectiveRecords = windowRecords.filter(
      (r) => r.selected_tests < (r.total_tests ?? Infinity)
    );

    const totalPrs = windowRecords.length;
    const totalSelective = selectiveRecords.length;
    const adoptionRate = totalPrs > 0 ? totalSelective / totalPrs : 0;

    const totalSkipped = selectiveRecords.reduce((sum, r) => sum + (r.skipped_tests ?? 0), 0);

    const avgSkipRate = this.computeAvgSkipRate(selectiveRecords);

    const totalCostSaved = selectiveRecords.reduce((sum, r) => sum + r.estimated_cost_saved_usd, 0);

    const totalTimeSaved = selectiveRecords.reduce((sum, r) => sum + r.estimated_time_saved_ms, 0);

    const escapedDefectRate = totalSelective > 0 ? escapedDefectCount / totalSelective : 0;

    const thresholdExceeded = escapedDefectRate > this.threshold;

    const recommendations = this.buildRecommendations(
      thresholdExceeded,
      escapedDefectRate,
      adoptionRate,
      avgSkipRate
    );

    return {
      period_days: this.windowDays,
      total_prs_analyzed: totalPrs,
      total_prs_selective: totalSelective,
      selective_adoption_rate: adoptionRate,
      total_tests_skipped: totalSkipped,
      avg_skip_rate: avgSkipRate,
      total_cost_saved_usd: totalCostSaved,
      total_time_saved_ms: totalTimeSaved,
      escaped_defects: escapedDefectCount,
      escaped_defect_rate: escapedDefectRate,
      threshold_exceeded: thresholdExceeded,
      threshold: this.threshold,
      recommendations,
    };
  }

  /**
   * Format the analysis result as a Markdown summary.
   */
  async formatReport(escapedDefectCount = 0): Promise<string> {
    const result = await this.analyze(escapedDefectCount);

    const adoptionPct = (result.selective_adoption_rate * 100).toFixed(1);
    const skipPct = (result.avg_skip_rate * 100).toFixed(1);
    const defectRatePct = (result.escaped_defect_rate * 100).toFixed(2);
    const costSaved = result.total_cost_saved_usd.toFixed(4);
    const timeSavedMin = (result.total_time_saved_ms / 60_000).toFixed(1);

    let md = "## Selective Test Effectiveness Report\n\n";
    md += `**Period**: last ${result.period_days} days\n\n`;
    md += "| Metric | Value |\n|--------|-------|\n";
    md += `| PRs analyzed | ${result.total_prs_analyzed} |\n`;
    md += `| PRs using selective testing | ${result.total_prs_selective} (${adoptionPct}%) |\n`;
    md += `| Average skip rate | ${skipPct}% |\n`;
    md += `| Total tests skipped | ${result.total_tests_skipped} |\n`;
    md += `| Estimated cost saved | $${costSaved} |\n`;
    md += `| Estimated time saved | ${timeSavedMin} min |\n`;
    md += `| Escaped defects | ${result.escaped_defects} |\n`;
    md += `| Escaped defect rate | ${defectRatePct}% (threshold: ${(result.threshold * 100).toFixed(1)}%) |\n\n`;

    if (result.threshold_exceeded) {
      md +=
        "> **Alert**: Escaped defect rate exceeds threshold. Review dependency graph coverage.\n\n";
    }

    if (result.recommendations.length > 0) {
      md += "### Recommendations\n\n";
      for (const rec of result.recommendations) {
        md += `- ${rec}\n`;
      }
    }

    return md;
  }

  // ---------------------------------------------------------------------------
  // Private helpers
  // ---------------------------------------------------------------------------

  private zeroResult(escapedDefectCount: number): SelectiveTestEffectivenessResult {
    const escapedDefectRate = 0;
    const thresholdExceeded = escapedDefectRate > this.threshold;
    return {
      period_days: this.windowDays,
      total_prs_analyzed: 0,
      total_prs_selective: 0,
      selective_adoption_rate: 0,
      total_tests_skipped: 0,
      avg_skip_rate: 0,
      total_cost_saved_usd: 0,
      total_time_saved_ms: 0,
      escaped_defects: escapedDefectCount,
      escaped_defect_rate: escapedDefectRate,
      threshold_exceeded: thresholdExceeded,
      threshold: this.threshold,
      recommendations: [],
    };
  }

  private computeAvgSkipRate(records: SelectiveTestMetricRecord[]): number {
    if (records.length === 0) return 0;

    const skipRates = records
      .map((r) => {
        if (r.total_tests == null || r.total_tests === 0) return null;
        return (r.skipped_tests ?? 0) / r.total_tests;
      })
      .filter((v): v is number => v !== null);

    if (skipRates.length === 0) return 0;
    return skipRates.reduce((sum, r) => sum + r, 0) / skipRates.length;
  }

  private buildRecommendations(
    thresholdExceeded: boolean,
    escapedDefectRate: number,
    adoptionRate: number,
    avgSkipRate: number
  ): string[] {
    const recs: string[] = [];

    if (thresholdExceeded) {
      recs.push(
        `Escaped defect rate (${(escapedDefectRate * 100).toFixed(2)}%) exceeds threshold — review dependency graph coverage and consider rebuilding the graph.`
      );
    }

    if (adoptionRate < 0.5) {
      recs.push(
        "Selective testing adoption is below 50% — consider ensuring the graph cache is up to date and available during validation."
      );
    }

    if (avgSkipRate < 0.1 && adoptionRate > 0) {
      recs.push(
        "Average skip rate is low (<10%) — the dependency graph may be overly conservative. Review cross-cutting change detection thresholds."
      );
    }

    return recs;
  }
}
