/**
 * PerformanceTabHtml - Performance metrics tab renderer
 *
 * Extracted from DashboardHtml.ts as part of the tab-based modular refactor (#1542).
 *
 * Contains:
 * - Time comparison panel (AI vs manual)
 * - Token usage table with per-stage breakdown
 * - Cost breakdown by stage
 * - Stage efficiency summary (historical averages)
 * - Cost per issue widget
 * - Cost by size bucket widget
 * - Cross-run stage comparison mini-bars
 * - Cache efficiency gauge
 * - Efficiency metrics panel
 * - PTC (Programmatic Tool Calling) metrics
 * - Outlier computation
 */

import type {
  PipelineRunSummary,
  TimeSavingsConfig,
  EfficiencyMetrics,
  StageAverageMetrics,
  StageOutlier,
  PTCMetricsDisplayData,
} from "../DashboardState";
import type { IssueCostAggregation } from "../../../utils/executionHistoryReader";
import { formatStageName, formatDuration, formatTimeSaved } from "../DashboardComponents";

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

/**
 * Stage color palette for cross-run comparison bars (Issue #1008)
 */
/**
 * Fibonacci complexity scores for t-shirt size labels.
 * Used to normalize cost-per-issue into cost-per-complexity-point.
 */
export const SIZE_FIBONACCI_SCORES: Record<string, number> = {
  XS: 1,
  S: 2,
  M: 4,
  L: 7,
  XL: 9,
};

const SIZE_DISPLAY_ORDER = ["XS", "S", "M", "L", "XL", "Unlabeled"];

/**
 * Aggregated cost statistics for a single t-shirt size bucket.
 */
export interface SizeBucketStats {
  sizeLabel: string;
  issueCount: number;
  totalCostUsd: number;
  avgCostPerIssue: number;
  totalRuns: number;
  avgRunsPerIssue: number;
  fibonacciScore: number | null;
  costPerComplexityPoint: number | null;
}

/**
 * Stage color palette for cross-run comparison bars (Issue #1008)
 */
const STAGE_COLORS: Record<string, string> = {
  "issue-pickup": "rgba(75, 192, 192, 0.8)",
  "feature-planning": "rgba(54, 162, 235, 0.8)",
  "feature-dev": "rgba(255, 159, 64, 0.8)",
  "feature-validate": "rgba(153, 102, 255, 0.8)",
  "pr-create": "rgba(255, 99, 132, 0.8)",
  "pr-merge": "rgba(255, 205, 86, 0.8)",
};

// ---------------------------------------------------------------------------
// Helper renderers (called by getPerformanceMetricsSectionHtml)
// ---------------------------------------------------------------------------

function getTimeSavedPanelHtml(
  run: PipelineRunSummary | null,
  timeSavingsConfig: TimeSavingsConfig
): string {
  if (!run) {
    return `
      <div class="time-saved-panel empty-state">
        <p>No pipeline data available. Start a pipeline to see time savings.</p>
      </div>
    `;
  }

  const actualDuration = run.usage.durationMs;
  const manualEstimate = run.manualEstimateMs ?? 0;
  const timeSaved = run.timeSavedMs ?? 0;

  // Calculate percentages for the comparison bar
  const maxTime = Math.max(actualDuration, manualEstimate);
  const aiPercent = maxTime > 0 ? (actualDuration / maxTime) * 100 : 0;
  const manualPercent = maxTime > 0 ? (manualEstimate / maxTime) * 100 : 0;

  // Calculate savings multiplier
  const multiplier = actualDuration > 0 ? manualEstimate / actualDuration : 0;

  return `
    <div class="time-saved-panel">
      <div class="section-header">
        <h3>Time Comparison</h3>
        <span class="multiplier-badge">${multiplier.toFixed(1)}x faster</span>
      </div>
      <div class="time-comparison">
        <div class="time-row">
          <span class="time-label">AI Pipeline</span>
          <div class="time-bar-wrapper">
            <div class="time-bar ai" style="width: ${aiPercent}%;"></div>
          </div>
          <span class="time-value">${formatDuration(actualDuration)}</span>
        </div>
        <div class="time-row">
          <span class="time-label">Manual Est.</span>
          <div class="time-bar-wrapper">
            <div class="time-bar manual" style="width: ${manualPercent}%;"></div>
          </div>
          <span class="time-value">${formatDuration(manualEstimate)}</span>
        </div>
      </div>
      <div class="time-saved-summary">
        <strong>Time Saved:</strong> ${formatTimeSaved(timeSaved)}
      </div>
    </div>
  `;
}

/**
 * Generate cache efficiency gauge HTML
 */
function getCacheGaugeHtml(efficiency: EfficiencyMetrics | undefined): string {
  const cacheHitRate = efficiency?.cacheHitRate ?? 0;
  const percent = Math.round(cacheHitRate * 100);

  // Determine color based on rate
  let colorClass = "low";
  if (percent >= 50) {
    colorClass = "high";
  } else if (percent >= 25) {
    colorClass = "medium";
  }

  return `
    <div class="cache-gauge">
      <div class="gauge-title">Cache Efficiency</div>
      <div class="gauge-container">
        <svg viewBox="0 0 100 50" class="gauge-svg">
          <path d="M 10 50 A 40 40 0 0 1 90 50" fill="none" stroke="var(--vscode-panel-border)" stroke-width="8" stroke-linecap="round"/>
          <path d="M 10 50 A 40 40 0 0 1 90 50" fill="none" stroke="var(--gauge-color-${colorClass})" stroke-width="8" stroke-linecap="round" stroke-dasharray="${percent * 1.26} 126" class="gauge-fill"/>
        </svg>
        <div class="gauge-value">${percent}%</div>
      </div>
      <div class="gauge-label">Cache Hit Rate</div>
    </div>
  `;
}

/**
 * Generate efficiency metrics panel HTML
 */
function getEfficiencyPanelHtml(efficiency: EfficiencyMetrics | undefined): string {
  if (!efficiency) {
    return `
      <div class="efficiency-panel empty-state">
        <p>No efficiency data available.</p>
      </div>
    `;
  }

  return `
    <div class="efficiency-panel">
      <div class="section-header">
        <h3>Efficiency Metrics</h3>
      </div>
      <div class="efficiency-grid">
        <div class="efficiency-item">
          <div class="efficiency-value">${Math.round(efficiency.tokensPerMinute).toLocaleString()}</div>
          <div class="efficiency-label">Tokens/min</div>
        </div>
        <div class="efficiency-item">
          <div class="efficiency-value">$${efficiency.costPerMinute.toFixed(4)}</div>
          <div class="efficiency-label">Cost/min</div>
        </div>
        <div class="efficiency-item">
          <div class="efficiency-value">${formatDuration(efficiency.avgStageDurationMs)}</div>
          <div class="efficiency-label">Avg Stage</div>
        </div>
      </div>
    </div>
  `;
}

/**
 * Generate cost breakdown panel HTML
 */
function getCostBreakdownHtml(run: PipelineRunSummary | null): string {
  if (!run) {
    return "";
  }

  const stagesWithCost = run.stages
    .filter((s) => s.tokenUsage?.costUsd)
    .map((s) => ({
      name: formatStageName(s.stage),
      cost: s.tokenUsage!.costUsd,
    }));

  const maxCost = Math.max(...stagesWithCost.map((s) => s.cost), 0.01);

  return `
    <div class="cost-breakdown">
      <div class="section-header">
        <h3>Cost by Stage</h3>
      </div>
      <div class="cost-bars">
        ${stagesWithCost
          .map(
            (stage) => `
          <div class="cost-row">
            <span class="cost-stage">${stage.name}</span>
            <div class="cost-bar-wrapper">
              <div class="cost-bar" style="width: ${(stage.cost / maxCost) * 100}%;"></div>
            </div>
            <span class="cost-value">$${stage.cost.toFixed(4)}</span>
          </div>
        `
          )
          .join("")}
      </div>
    </div>
  `;
}

/**
 * Generate HTML table for per-stage token usage (Issue #1008: enhanced with Duration, Model, Cache)
 * Replaces Chart.js bar/line/donut charts to eliminate memory leaks
 * from Chart.js instances that were never destroyed on webview refresh.
 */
function getTokenUsageTableHtml(
  displayRun: PipelineRunSummary | null,
  outliers?: StageOutlier[]
): string {
  if (!displayRun || displayRun.stages.length === 0) {
    return '<p class="empty-state-text">No token data available</p>';
  }

  const totalTokens = displayRun.usage.inputTokens + displayRun.usage.outputTokens;
  const totalCost = displayRun.usage.costUsd;

  // Build outlier lookup for quick access
  const outlierSet = new Set<string>();
  const outlierDetails = new Map<string, StageOutlier[]>();
  if (outliers) {
    for (const o of outliers) {
      outlierSet.add(o.stage);
      const existing = outlierDetails.get(o.stage) ?? [];
      existing.push(o);
      outlierDetails.set(o.stage, existing);
    }
  }

  // Build per-stage rows with bar visualization
  let totalDurationMs = 0;
  let totalCacheRead = 0;
  let totalCacheCreation = 0;
  const maxStageTokens = Math.max(
    ...displayRun.stages.map(
      (s) => (s.tokenUsage?.inputTokens ?? 0) + (s.tokenUsage?.outputTokens ?? 0)
    ),
    1
  );

  const rows = displayRun.stages
    .filter((s) => s.tokenUsage && s.tokenUsage.inputTokens + s.tokenUsage.outputTokens > 0)
    .map((s) => {
      const input = s.tokenUsage?.inputTokens ?? 0;
      const output = s.tokenUsage?.outputTokens ?? 0;
      const stageTotal = input + output;
      const cost = s.tokenUsage?.costUsd ?? 0;
      const cacheRead = s.tokenUsage?.cacheReadTokens ?? 0;
      const cacheCreation = s.tokenUsage?.cacheCreationTokens ?? 0;
      const duration = s.durationMs ?? 0;
      const model = s.tokenUsage?.model ?? "";
      const cacheHitRate = s.tokenUsage?.cacheHitRate;

      totalDurationMs += duration;
      totalCacheRead += cacheRead;
      totalCacheCreation += cacheCreation;

      const pct = totalTokens > 0 ? ((stageTotal / totalTokens) * 100).toFixed(1) : "0";
      const barWidth = Math.round((stageTotal / maxStageTokens) * 100);
      const inputWidth = stageTotal > 0 ? Math.round((input / stageTotal) * barWidth) : 0;
      const outputWidth = barWidth - inputWidth;

      const isOutlier = outlierSet.has(s.stage);
      const rowClass = isOutlier ? ' class="outlier"' : "";
      const stageOutliers = outlierDetails.get(s.stage);
      const outlierTitle = stageOutliers
        ? ` title="${stageOutliers.map((o) => `${o.metric}: ${o.ratio.toFixed(1)}x avg`).join(", ")}"`
        : "";

      const cacheHitCell =
        cacheHitRate !== undefined ? `${(cacheHitRate * 100).toFixed(1)}%` : "\u2014";

      return `
        <tr${rowClass}${outlierTitle}>
          <td class="stage-name-cell">${formatStageName(s.stage)}</td>
          <td class="token-bar-cell">
            <div class="token-bar-track">
              <div class="token-bar-input" style="width: ${inputWidth}%"></div>
              <div class="token-bar-output" style="width: ${outputWidth}%"></div>
            </div>
          </td>
          <td class="token-num-cell">${stageTotal.toLocaleString()}</td>
          <td class="token-pct-cell">${pct}%</td>
          <td class="token-cost-cell">$${cost.toFixed(4)}</td>
          <td class="token-duration-cell">${duration > 0 ? formatDuration(duration) : "\u2014"}</td>
          <td class="token-cache-cell">${cacheRead > 0 || cacheCreation > 0 ? `R:${cacheRead.toLocaleString()} / C:${cacheCreation.toLocaleString()}` : "\u2014"}</td>
          <td class="token-cache-hit-cell">${cacheHitCell}</td>
          <td class="token-model-cell">${model || "\u2014"}</td>
        </tr>`;
    })
    .join("");

  return `
    <div class="token-table-container">
      <table class="token-usage-table">
        <thead>
          <tr>
            <th>Stage</th>
            <th>
              <span class="legend-inline">
                <span class="legend-dot legend-input"></span> Input
                <span class="legend-dot legend-output"></span> Output
              </span>
            </th>
            <th>Tokens</th>
            <th>%</th>
            <th>Cost</th>
            <th>Duration</th>
            <th>Cache (R/C)</th>
            <th>Cache Hit %</th>
            <th>Model</th>
          </tr>
        </thead>
        <tbody>
          ${rows}
        </tbody>
        <tfoot>
          <tr class="total-row">
            <td><strong>Total</strong></td>
            <td></td>
            <td><strong>${totalTokens.toLocaleString()}</strong></td>
            <td><strong>100%</strong></td>
            <td><strong>$${totalCost.toFixed(4)}</strong></td>
            <td><strong>${totalDurationMs > 0 ? formatDuration(totalDurationMs) : "\u2014"}</strong></td>
            <td><strong>${totalCacheRead > 0 || totalCacheCreation > 0 ? `R:${totalCacheRead.toLocaleString()} / C:${totalCacheCreation.toLocaleString()}` : "\u2014"}</strong></td>
            <td></td>
            <td></td>
          </tr>
        </tfoot>
      </table>
    </div>`;
}

/**
 * Generate stage efficiency summary card (Issue #1008)
 *
 * Shows per-stage averages across historical runs: avg cost, tokens,
 * duration, and primary model.
 */
function getStageEfficiencySummaryHtml(stageAverages: StageAverageMetrics[]): string {
  if (stageAverages.length === 0) {
    return "";
  }

  const rows = stageAverages
    .map(
      (avg) => `
      <tr>
        <td>${formatStageName(avg.stage)}</td>
        <td class="num">$${avg.avgCostUsd.toFixed(4)}</td>
        <td class="num">${Math.round(avg.avgInputTokens + avg.avgOutputTokens).toLocaleString()}</td>
        <td class="num">${avg.avgDurationMs > 0 ? formatDuration(avg.avgDurationMs) : "\u2014"}</td>
        <td class="num">${avg.primaryModel ?? "\u2014"}</td>
        <td class="num">${avg.runCount}</td>
      </tr>`
    )
    .join("");

  return `
    <details class="collapsible-section" open>
      <summary class="section-toggle">
        <span class="toggle-icon">▼</span>
        <h4 style="display:inline; margin:0;">Stage Efficiency Summary</h4>
      </summary>
      <div class="stage-efficiency-container">
        <table class="stage-efficiency-table">
          <thead>
            <tr>
              <th>Stage</th>
              <th>Avg Cost</th>
              <th>Avg Tokens</th>
              <th>Avg Duration</th>
              <th>Model</th>
              <th>Runs</th>
            </tr>
          </thead>
          <tbody>
            ${rows}
          </tbody>
        </table>
      </div>
    </details>`;
}

/**
 * Generate cross-run stage comparison mini-bars (Issue #1008)
 *
 * Horizontal stacked bar chart (pure CSS) showing cost distribution
 * by stage across the last 10 runs.
 */
function getStageComparisonHtml(history: PipelineRunSummary[]): string {
  // Need at least 2 runs to compare
  const runs = history.slice(0, 10);
  if (runs.length < 2) {
    return "";
  }

  // Find max total cost for scaling
  const maxCost = Math.max(...runs.map((r) => r.usage.costUsd), 0.01);

  // Collect all stage names that appear in any run
  const allStages = new Set<string>();
  for (const run of runs) {
    for (const s of run.stages) {
      if (s.tokenUsage?.costUsd) {
        allStages.add(s.stage);
      }
    }
  }

  const runBars = runs
    .map((run) => {
      const runCost = run.usage.costUsd;
      const segments = run.stages
        .filter((s) => s.tokenUsage?.costUsd && s.tokenUsage.costUsd > 0)
        .map((s) => {
          const pct = runCost > 0 ? ((s.tokenUsage!.costUsd / maxCost) * 100).toFixed(1) : "0";
          const color = STAGE_COLORS[s.stage] ?? "rgba(128, 128, 128, 0.6)";
          return `<div class="stage-comparison-segment" style="width: ${pct}%; background: ${color};" title="${formatStageName(s.stage)}: $${s.tokenUsage!.costUsd.toFixed(4)}"></div>`;
        })
        .join("");

      return `
        <div class="stage-comparison-run">
          <span class="stage-comparison-label">#${run.issueNumber}</span>
          <div class="stage-comparison-bar-track">
            ${segments}
          </div>
          <span class="stage-comparison-cost">$${runCost.toFixed(4)}</span>
        </div>`;
    })
    .join("");

  const legend = [...allStages]
    .map(
      (stage) => `
      <span class="stage-comparison-legend-item">
        <span class="stage-comparison-legend-dot" style="background: ${STAGE_COLORS[stage] ?? "rgba(128, 128, 128, 0.6)"}"></span>
        ${formatStageName(stage)}
      </span>`
    )
    .join("");

  return `
    <details class="collapsible-section" open>
      <summary class="section-toggle">
        <span class="toggle-icon">▼</span>
        <h4 style="display:inline; margin:0;">Cross-Run Stage Comparison (Last ${runs.length} Runs)</h4>
      </summary>
      <div class="stage-comparison-container">
        ${runBars}
        <div class="stage-comparison-legend">
          ${legend}
        </div>
      </div>
    </details>`;
}

// ---------------------------------------------------------------------------
// Cost by Size — aggregation + widget
// ---------------------------------------------------------------------------

/**
 * Group cost-per-issue aggregations into t-shirt size buckets.
 *
 * Normalizes size labels to uppercase, maps unrecognized labels to
 * "Unlabeled", and returns buckets sorted by SIZE_DISPLAY_ORDER.
 */
export function aggregateCostBySize(aggregations: IssueCostAggregation[]): SizeBucketStats[] {
  if (aggregations.length === 0) return [];

  const buckets = new Map<string, IssueCostAggregation[]>();
  for (const agg of aggregations) {
    const raw = agg.sizeLabel?.toUpperCase() ?? "Unlabeled";
    const key = raw in SIZE_FIBONACCI_SCORES ? raw : "Unlabeled";
    const arr = buckets.get(key) ?? [];
    arr.push(agg);
    buckets.set(key, arr);
  }

  const result: SizeBucketStats[] = [];
  for (const [label, items] of buckets) {
    const issueCount = items.length;
    const totalCostUsd = items.reduce((sum, i) => sum + i.totalCostUsd, 0);
    const totalRuns = items.reduce((sum, i) => sum + i.runCount, 0);
    const avgCostPerIssue = totalCostUsd / issueCount;
    const fibScore = SIZE_FIBONACCI_SCORES[label] ?? null;
    result.push({
      sizeLabel: label,
      issueCount,
      totalCostUsd,
      avgCostPerIssue,
      totalRuns,
      avgRunsPerIssue: totalRuns / issueCount,
      fibonacciScore: fibScore,
      costPerComplexityPoint: fibScore !== null ? avgCostPerIssue / fibScore : null,
    });
  }

  result.sort(
    (a, b) => SIZE_DISPLAY_ORDER.indexOf(a.sizeLabel) - SIZE_DISPLAY_ORDER.indexOf(b.sizeLabel)
  );

  return result;
}

/**
 * Render the Cost by Size widget — a table + horizontal bar chart.
 *
 * Groups cost-per-issue data by t-shirt size, shows averages per bucket,
 * and visualizes relative avg cost with color-coded bars based on
 * cost-per-complexity-point efficiency.
 */
export function getCostBySizeWidgetHtml(aggregations: IssueCostAggregation[]): string {
  const buckets = aggregateCostBySize(aggregations);
  if (buckets.length === 0) return "";

  const maxAvgCost = Math.max(...buckets.map((b) => b.avgCostPerIssue));

  // Compute median cost/point for color thresholds (excluding Unlabeled)
  const cppValues = buckets
    .filter((b) => b.costPerComplexityPoint !== null)
    .map((b) => b.costPerComplexityPoint as number)
    .sort((a, b) => a - b);
  const medianCpp = cppValues.length > 0 ? cppValues[Math.floor(cppValues.length / 2)] : 0;

  const rows = buckets
    .map((b) => {
      const cppCell =
        b.costPerComplexityPoint !== null ? `$${b.costPerComplexityPoint.toFixed(4)}` : "\u2014";
      return `<tr>
        <td><strong>${b.sizeLabel}</strong></td>
        <td>${b.issueCount}</td>
        <td>$${b.totalCostUsd.toFixed(4)}</td>
        <td>$${b.avgCostPerIssue.toFixed(4)}</td>
        <td>${b.avgRunsPerIssue.toFixed(1)}</td>
        <td>${cppCell}</td>
      </tr>`;
    })
    .join("\n");

  const bars = buckets
    .map((b) => {
      const widthPct = maxAvgCost > 0 ? (b.avgCostPerIssue / maxAvgCost) * 100 : 0;
      let colorClass = "cost-by-size-fill-neutral";
      if (b.costPerComplexityPoint !== null && medianCpp > 0) {
        const ratio = b.costPerComplexityPoint / medianCpp;
        if (ratio < 0.85) colorClass = "cost-by-size-fill-low";
        else if (ratio <= 1.15) colorClass = "cost-by-size-fill-mid";
        else colorClass = "cost-by-size-fill-high";
      }
      return `<div class="cost-by-size-bar-row">
        <span class="cost-by-size-bar-label">${b.sizeLabel}</span>
        <div class="cost-by-size-bar-track">
          <div class="cost-by-size-bar-fill ${colorClass}" style="width: ${widthPct.toFixed(1)}%;"></div>
        </div>
        <span class="cost-by-size-bar-value">$${b.avgCostPerIssue.toFixed(4)}</span>
      </div>`;
    })
    .join("\n");

  return `
    <div class="cost-by-size-widget">
      <details class="collapsible-section" open>
        <summary class="section-toggle">
          <span class="toggle-icon">\u25BC</span>
          <h4>Cost by Size</h4>
          <span class="section-badge">${buckets.length} sizes</span>
        </summary>
        <div class="section-content">
          <table class="cost-stage-table">
            <thead>
              <tr>
                <th>Size</th>
                <th>Issues</th>
                <th>Total Cost</th>
                <th>Avg Cost</th>
                <th>Avg Runs</th>
                <th>Cost/Point</th>
              </tr>
            </thead>
            <tbody>
              ${rows}
            </tbody>
          </table>
          <div class="cost-by-size-bars">
            <h4>Average Cost Comparison</h4>
            ${bars}
          </div>
        </div>
      </details>
    </div>`;
}

// ---------------------------------------------------------------------------
// Cost per Issue widget
// ---------------------------------------------------------------------------

/**
 * Generate cost-per-issue table widget HTML (Issue #1410).
 *
 * Shows aggregated cost data per issue with run counts, backtracks,
 * issue type, and size labels.
 */
export function getCostPerIssueWidgetHtml(aggregations: IssueCostAggregation[]): string {
  if (aggregations.length === 0) {
    return `
      <div class="cost-per-issue-widget">
        <h4>Cost per Issue</h4>
        <p class="empty-state">No cost data yet. Run a pipeline to see per-issue costs.</p>
      </div>`;
  }

  const rows = aggregations
    .map((a) => {
      const cost = `$${a.totalCostUsd.toFixed(4)}`;
      const hasBacktracks = a.backtrackCount > 0;
      const rowClass = hasBacktracks ? ' class="row-has-backtracks"' : "";
      const typeCell = a.issueType ?? "\u2014";
      const sizeCell = a.sizeLabel ?? "\u2014";
      return `<tr${rowClass}>
        <td>#${a.issueNumber}</td>
        <td>${cost}</td>
        <td>${a.runCount}</td>
        <td>${a.backtrackCount}</td>
        <td>${typeCell}</td>
        <td>${sizeCell}</td>
      </tr>`;
    })
    .join("\n");

  return `
    <div class="cost-per-issue-widget">
      <h4>Cost per Issue (Last ${aggregations.length})</h4>
      <table class="cost-per-issue-table">
        <thead>
          <tr>
            <th>Issue</th>
            <th>Total Cost</th>
            <th>Runs</th>
            <th>Backtracks</th>
            <th>Type</th>
            <th>Size</th>
          </tr>
        </thead>
        <tbody>
          ${rows}
        </tbody>
      </table>
    </div>`;
}

/**
 * Generate the inner HTML for the performance metrics section (Issue #923).
 * Kept as internal helper — used by getAnalyticsPerformanceSubsectionHtml
 * in DashboardHtml.ts. Cost Per Issue moved to cost sub-section (Issue #1541).
 */
export function getPerformanceMetricsSectionHtml(
  displayRun: PipelineRunSummary | null,
  timeSavingsConfig: TimeSavingsConfig,
  stageAverages?: StageAverageMetrics[],
  outliers?: StageOutlier[],
  history?: PipelineRunSummary[]
): string {
  return `
    ${getTimeSavedPanelHtml(displayRun, timeSavingsConfig)}

    <h3>Token Usage by Stage</h3>
    ${getTokenUsageTableHtml(displayRun, outliers)}

    ${getCostBreakdownHtml(displayRun)}

    ${getStageEfficiencySummaryHtml(stageAverages ?? [])}

    ${getStageComparisonHtml(history ?? [])}

    ${getCacheGaugeHtml(displayRun?.efficiency)}

    ${getEfficiencyPanelHtml(displayRun?.efficiency)}
  `;
}

/**
 * Generate the PTC metrics section HTML (Issue #1071)
 *
 * Only rendered when PTC data exists (programmaticCalls > 0).
 * Displayed as a collapsible section in the dashboard.
 */
export function getPTCMetricsSectionHtml(ptcMetrics: PTCMetricsDisplayData | null): string {
  if (!ptcMetrics || ptcMetrics.programmaticCalls === 0) {
    return "";
  }

  const ratioPercent = Math.round(ptcMetrics.programmaticRatio * 100);
  const savedFormatted = ptcMetrics.estimatedTokensSaved.toLocaleString();

  return `
    <details class="ptc-metrics-section" open>
      <summary><h3 style="display:inline">Programmatic Tool Calling (PTC)</h3></summary>
      <div class="ptc-metrics-grid" style="display:grid;grid-template-columns:repeat(3,1fr);gap:8px;margin-top:8px;">
        <div class="metric-card">
          <div class="metric-label">Programmatic Ratio</div>
          <div class="metric-value">${ratioPercent}%</div>
          <div class="metric-detail">${ptcMetrics.programmaticCalls} PTC / ${ptcMetrics.directCalls} direct</div>
        </div>
        <div class="metric-card">
          <div class="metric-label">Est. Tokens Saved</div>
          <div class="metric-value">${savedFormatted}</div>
          <div class="metric-detail">vs direct tool calls</div>
        </div>
        <div class="metric-card">
          <div class="metric-label">Code Executions</div>
          <div class="metric-value">${ptcMetrics.codeExecutionCount}</div>
          <div class="metric-detail">${ptcMetrics.containerReuseCount} container reuses</div>
        </div>
      </div>
    </details>
  `;
}

/**
 * Compute stage outliers for a run against historical averages (Issue #1008)
 *
 * Pure function version for use in rendering — mirrors DashboardState.getStageOutliers().
 */
export function computeOutliers(
  run: PipelineRunSummary,
  averages: StageAverageMetrics[]
): StageOutlier[] {
  const avgMap = new Map<string, StageAverageMetrics>();
  for (const avg of averages) {
    avgMap.set(avg.stage, avg);
  }

  const outliers: StageOutlier[] = [];
  const OUTLIER_THRESHOLD = 2.0;

  for (const stage of run.stages) {
    const avg = avgMap.get(stage.stage);
    if (!avg || avg.runCount < 2) continue;

    if (
      stage.tokenUsage &&
      avg.avgCostUsd > 0 &&
      stage.tokenUsage.costUsd > avg.avgCostUsd * OUTLIER_THRESHOLD
    ) {
      outliers.push({
        stage: stage.stage,
        metric: "cost",
        value: stage.tokenUsage.costUsd,
        avg: avg.avgCostUsd,
        ratio: stage.tokenUsage.costUsd / avg.avgCostUsd,
      });
    }

    if (
      stage.durationMs !== undefined &&
      avg.avgDurationMs > 0 &&
      stage.durationMs > avg.avgDurationMs * OUTLIER_THRESHOLD
    ) {
      outliers.push({
        stage: stage.stage,
        metric: "duration",
        value: stage.durationMs,
        avg: avg.avgDurationMs,
        ratio: stage.durationMs / avg.avgDurationMs,
      });
    }
  }

  return outliers;
}

// CSS styles extracted to PerformanceTabStyles.ts (#1542)
export { getPerformanceTabStyles, getTokenTableStyles } from "./PerformanceTabStyles";

// ---------------------------------------------------------------------------
// Stall threshold table (Issue #3218)
// ---------------------------------------------------------------------------

/**
 * Per-cell row in the stall threshold table.
 *
 * One row per `(stage, mode)` triple. `size` is currently always `"all"`
 * (ADR-002 — size keying is reserved). `warnSec`/`killSec` are `null` when
 * `getCalibratedStallData()` returned `undefined` for the cell — the renderer
 * surfaces "—" so users can see the gap rather than a fabricated default.
 */
export interface StallThresholdRow {
  stage: string;
  mode: "efficiency" | "elevated" | "maximum" | "frontier";
  size: "all" | string;
  warnSec: number | null;
  killSec: number | null;
  source: "env" | "config" | "calibrated" | "static";
  isColdStart: boolean;
}

/**
 * Render the stall threshold table panel (Issue #3218).
 *
 * Each row shows a `(stage, mode)` cell with its current `warnSec`,
 * `killSec`, source, and cold-start indicator. Cold-start rows render
 * `killSec` as "disabled" because auto-kill is suppressed below `min_runs`.
 *
 * Empty rows array → empty string (panel hidden).
 */
export function getStallThresholdTableHtml(rows: StallThresholdRow[]): string {
  if (rows.length === 0) return "";

  const tbody = rows
    .map((row) => {
      const warn = row.warnSec === null ? "—" : `${Math.round(row.warnSec)}s`;
      const kill =
        row.killSec === null
          ? "—"
          : row.isColdStart || row.killSec === 0
            ? "<em>disabled</em>"
            : `${Math.round(row.killSec)}s`;
      const cold = row.isColdStart ? '<span class="badge badge-warning">cold</span>' : "";
      return `<tr data-stage="${row.stage}" data-mode="${row.mode}">
        <td>${formatStageName(row.stage)}</td>
        <td><span class="mode-pill mode-${row.mode}">${row.mode}</span></td>
        <td>${row.size}</td>
        <td>${warn}</td>
        <td>${kill}</td>
        <td>${row.source}</td>
        <td>${cold}</td>
      </tr>`;
    })
    .join("");

  return `
    <details class="collapsible-section stall-threshold-panel" open>
      <summary class="section-toggle">
        <span class="toggle-icon">▼</span>
        <h4>Stall Thresholds (calibrated)</h4>
      </summary>
      <div class="section-content">
        <p class="caption">Active stall watchdog thresholds per <code>(stage, mode)</code>. The <strong>Size</strong> column is reserved (size dimension currently bucketed internally only — see #3216).</p>
        <table class="cost-stage-table stall-threshold-table">
          <thead>
            <tr>
              <th>Stage</th>
              <th>Mode</th>
              <th>Size</th>
              <th>Warn</th>
              <th>Kill</th>
              <th>Source</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            ${tbody}
          </tbody>
        </table>
      </div>
    </details>`;
}
