/**
 * HealthWidgetHtml - HTML/CSS rendering for the pipeline health widget
 *
 * Renders the health widget as a collapsible section in the dashboard WebView.
 * Follows existing Dashboard styling patterns.
 *
 * @see Issue #655 - Pipeline Health Dashboard Widget
 */

import type {
  HealthWidgetData,
  HealthComponent,
  TrendSparkline,
  ActiveAlert,
  Recommendation,
  PredictionAccuracy,
  HealthStatus,
  TrendChartDay,
  TrendAnalysis,
  TrendRange,
  DimensionSparkline,
} from "./HealthWidgetTypes";
import { getHealthStatusColor, TREND_RANGE_LABELS, DEFAULT_TREND_RANGE } from "./HealthWidgetTypes";

/**
 * Escape HTML special characters to prevent XSS
 */
function escapeHtml(text: string): string {
  const htmlEntities: Record<string, string> = {
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  };
  return text.replace(/[&<>"']/g, (char) => htmlEntities[char] || char);
}

function escapeAttr(text: string): string {
  return escapeHtml(text);
}

/**
 * Format savings amount for display.
 * Values >= $0.01 show 2 decimal places; sub-cent values show "< $0.01".
 */
function formatSavings(usd: number): string {
  if (usd >= 0.01) return `$${usd.toFixed(2)}`;
  if (usd > 0) return "< $0.01";
  return "$0.00";
}

/**
 * Status badge emoji/icon for health status
 */
function getStatusIcon(status: HealthStatus): string {
  switch (status) {
    case "excellent":
      return "●";
    case "good":
      return "●";
    case "fair":
      return "●";
    case "poor":
      return "●";
    case "critical":
      return "●";
  }
}

/**
 * Alert level icon
 */
function getAlertIcon(level: "info" | "warning" | "critical"): string {
  switch (level) {
    case "info":
      return "ℹ";
    case "warning":
      return "⚠";
    case "critical":
      return "✖";
  }
}

/**
 * Trend direction arrow
 */
function getTrendArrow(trend: "up" | "down" | "stable" | "improving" | "degrading"): string {
  switch (trend) {
    case "up":
    case "improving":
      return "↑";
    case "down":
    case "degrading":
      return "↓";
    case "stable":
      return "→";
  }
}

/**
 * Render the complete health widget HTML section
 */
export function getHealthWidgetHtml(
  data: HealthWidgetData,
  collapsed: boolean = false,
  appliedCategories: string[] = []
): string {
  if (data.isEmpty) {
    return getEmptyStateHtml();
  }

  const collapseClass = collapsed ? "collapsed" : "";
  const toggleIcon = collapsed ? "▶" : "▼";

  return `
    <div class="health-widget ${collapseClass}" id="healthWidget">
      <div class="health-widget-header" id="healthWidgetHeader">
        <span class="toggle-icon">${toggleIcon}</span>
        <h2>Pipeline Health</h2>
        <span class="health-badge ${getHealthStatusColor(data.summary.status)}">
          ${getStatusIcon(data.summary.status)} ${data.summary.status.toUpperCase()}
        </span>
        <span class="health-score-inline">${Math.round(data.summary.score)}</span>
      </div>
      <div class="health-widget-body">
        ${getHealthSummaryHtml(data.summary)}
        ${getSparklinesSectionHtml(data.sparklines)}
        ${getTrendChartSectionHtml(data.trendChart, data.trendAnalysis, data.trendRange)}
        ${getDimensionSparklinesSectionHtml(data.dimensionSparklines)}
        ${getAlertsPanelHtml(data.alerts)}
        ${getRecommendationsHtml(data.recommendations, appliedCategories)}
        ${getPredictionAccuracyHtml(data.predictionAccuracy)}
        <div class="health-updated">Last updated: ${escapeHtml(data.lastUpdated)}</div>
      </div>
    </div>
  `;
}

/**
 * Empty state when no pipeline runs exist yet
 */
function getEmptyStateHtml(): string {
  return `
    <div class="health-widget" id="healthWidget">
      <div class="health-widget-header">
        <h2>Pipeline Health</h2>
      </div>
      <div class="health-widget-body health-empty-state">
        <div class="empty-icon">📊</div>
        <p>Run your first pipeline to see health metrics</p>
        <p class="empty-hint">Health scores, trends, and recommendations will appear here after pipeline runs complete.</p>
      </div>
    </div>
  `;
}

/**
 * Health summary card with score and component breakdown
 */
function getHealthSummaryHtml(summary: {
  score: number;
  status: HealthStatus;
  components: HealthComponent[];
}): string {
  const componentBars = summary.components
    .map((c) => {
      const insufficientClass = c.insufficientData ? " health-component-insufficient" : "";
      const titleAttr = c.insufficientDataMessage
        ? ` title="${escapeAttr(c.insufficientDataMessage)}"`
        : "";
      const trendContent = c.insufficientData ? "—" : getTrendArrow(c.trend);
      return `
      <div class="health-component-row${insufficientClass}"${titleAttr}>
        <span class="component-label">${escapeHtml(c.label)}</span>
        <div class="component-bar-container">
          <div class="component-bar ${getHealthStatusColor(c.score >= 70 ? "good" : c.score >= 40 ? "fair" : "poor")}"
               style="width: ${Math.round(c.score)}%">
          </div>
        </div>
        <span class="component-score">${Math.round(c.score)}</span>
        <span class="component-trend">${trendContent}</span>
      </div>
    `;
    })
    .join("");

  return `
    <div class="health-summary-card">
      <div class="health-score-large ${getHealthStatusColor(summary.status)}">
        ${Math.round(summary.score)}
      </div>
      <div class="health-component-breakdown">
        ${componentBars}
      </div>
    </div>
  `;
}

/**
 * Format a sparkline metric value for display.
 * Cost uses dollar format; tokens use locale grouping; percentages show %.
 */
function formatSparklineValue(value: number, metric: string): string {
  if (metric === "cost") return `$${value.toFixed(4)}`;
  return value.toLocaleString();
}

/**
 * Map (direction, polarity) to a CSS color class.
 *
 * The arrow direction is the literal direction of the data; the color tells
 * the user whether that direction is good or bad for this metric. A falling
 * cost is good (green), but a falling success rate is bad (red).
 */
function getSparklineColorClass(
  trend: "up" | "down" | "stable",
  polarity: "higher-is-better" | "lower-is-better"
): string {
  if (trend === "stable") return "trend-stable";
  const isImproving =
    (trend === "up" && polarity === "higher-is-better") ||
    (trend === "down" && polarity === "lower-is-better");
  return isImproving ? "trend-improving" : "trend-degrading";
}

/**
 * "Recent Activity" section — raw per-run metrics over the last N runs.
 *
 * The section title is intentionally explicit about the timeframe so users
 * don't conflate these with the all-time / composite numbers shown above in
 * the Pipeline Health card and headline stats. The window size is read from
 * the first sparkline's data length (capped by the producer at `limit`) so
 * the header stays in sync if the limit ever changes.
 */
function getSparklinesSectionHtml(sparklines: TrendSparkline[]): string {
  if (sparklines.length === 0) return "";

  // All sparklines share the same window — read it once for the header.
  const windowSize = Math.max(...sparklines.map((s) => s.data.length), 0);
  const windowLabel = windowSize > 0 ? `Last ${windowSize} Runs` : "Recent Runs";

  const sparklineCards = sparklines
    .map((s) => {
      // Show average of the last 5 data points. For most metrics 0 means
      // "missing"; some metrics (e.g. success rate) opt out via
      // `treatZeroAsMissing: false` because 0 is a valid observation.
      const recent = s.data.slice(-5);
      const treatZeroAsMissing = s.treatZeroAsMissing !== false;
      const withData = treatZeroAsMissing ? recent.filter((v) => v > 0) : recent;
      const avg = withData.length > 0 ? withData.reduce((a, b) => a + b, 0) / withData.length : 0;
      const displayVal = formatSparklineValue(
        s.metric === "cost" ? avg : Math.round(avg),
        s.metric
      );
      const unit = s.metric === "cost" ? "" : (s.unit ?? "");
      const runsLabel =
        withData.length > 0
          ? `avg of ${withData.length} run${withData.length !== 1 ? "s" : ""}`
          : "no data";
      const colorClass = getSparklineColorClass(s.trend, s.polarity);
      return `
      <div class="sparkline-card">
        <div class="sparkline-header">
          <span class="sparkline-label">${escapeHtml(s.label)}</span>
          <span class="sparkline-trend ${colorClass}">
            ${getTrendArrow(s.trend)}
          </span>
        </div>
        <div class="sparkline-value">${displayVal}${unit ? " " + escapeHtml(unit) : ""}</div>
        <div class="sparkline-subtitle">${escapeHtml(runsLabel)}</div>
      </div>
    `;
    })
    .join("");

  return `
    <div class="health-sparklines-section">
      <h3>Recent Activity <span class="health-sparklines-window">(${escapeHtml(windowLabel)})</span></h3>
      <div class="sparklines-grid">
        ${sparklineCards}
      </div>
    </div>
  `;
}

/**
 * Active alerts panel
 */
function getAlertsPanelHtml(alerts: ActiveAlert[]): string {
  if (alerts.length === 0) return "";

  const alertItems = alerts
    .slice(0, 5)
    .map(
      (a) => `
      <div class="alert-item alert-${a.level}">
        <span class="alert-icon">${getAlertIcon(a.level)}</span>
        <div class="alert-content">
          <span class="alert-stage">${escapeHtml(a.stage)}</span>
          <span class="alert-message">${escapeHtml(a.message)}</span>
        </div>
      </div>
    `
    )
    .join("");

  const overflowNote =
    alerts.length > 5
      ? `<div class="alerts-overflow">+ ${alerts.length - 5} more alerts</div>`
      : "";

  return `
    <div class="health-alerts-panel">
      <h3>Active Alerts (${alerts.length})</h3>
      ${alertItems}
      ${overflowNote}
    </div>
  `;
}

/**
 * Top recommendations section
 */
function getRecommendationsHtml(
  recommendations: Recommendation[],
  appliedCategories: string[] = []
): string {
  if (recommendations.length === 0) return "";

  const appliedSet = new Set(appliedCategories);

  const cards = recommendations
    .slice(0, 5)
    .map((r) => {
      const isApplied = appliedSet.has(r.category);
      const isActionable = r.action?.type === "config-patch";

      let actionHtml = "";
      if (isApplied) {
        actionHtml = `
          <div class="recommendation-actions">
            <span class="recommendation-applied-badge">Applied</span>
            <button class="recommendation-revert-btn"
              data-action="revert-recommendation" data-category="${escapeAttr(r.category)}">Revert</button>
          </div>`;
      } else if (isActionable && r.action) {
        actionHtml = `
          <div class="recommendation-actions">
            <button class="recommendation-apply-btn"
              data-action="apply-recommendation" data-category="${escapeAttr(r.category)}" data-config-path="${escapeAttr(r.action.configPath)}" data-value="${escapeAttr(JSON.stringify(r.action.suggestedValue))}">${escapeHtml(r.action.label)}</button>
          </div>`;
      }

      return `
      <div class="recommendation-card${isApplied ? " recommendation-applied" : ""}">
        <div class="recommendation-header">
          <span class="recommendation-title">${escapeHtml(r.title)}</span>
          <span class="recommendation-savings">~${formatSavings(r.estimatedSavingsUsd)}</span>
        </div>
        <p class="recommendation-desc">${escapeHtml(r.description)}</p>
        ${actionHtml}
      </div>
    `;
    })
    .join("");

  return `
    <div class="health-recommendations">
      <h3>Recommendations</h3>
      ${cards}
    </div>
  `;
}

/**
 * Prediction accuracy summary card
 */
function getPredictionAccuracyHtml(accuracy: PredictionAccuracy | null): string {
  if (!accuracy) return "";

  return `
    <div class="health-prediction-card">
      <h3>Prediction Accuracy</h3>
      <div class="prediction-stats">
        <div class="prediction-stat">
          <span class="stat-value">${accuracy.accuracyPercent}%</span>
          <span class="stat-label">Accuracy</span>
        </div>
        <div class="prediction-stat">
          <span class="stat-value">${accuracy.totalObservations}</span>
          <span class="stat-label">Observations</span>
        </div>
        <div class="prediction-stat">
          <span class="stat-value">${getTrendArrow(accuracy.trend)}</span>
          <span class="stat-label">Trend</span>
        </div>
      </div>
    </div>
  `;
}

/**
 * Trend chart section with CSS-only bars and a range selector.
 * Adapts bar count and labels based on the selected range:
 * - 24h: 24 bars (one per hour), labels show hour (0-23)
 * - 7d: 7 bars (one per day), labels show day-of-month
 * - 30d: 30 bars (one per day), labels show day-of-month
 * - 90d: 90 bars (one per day), labels show day-of-month
 *
 * @see Issue #789 - Persist Health Scores to Disk with 30-Day Trend
 */
export function getTrendChartSectionHtml(
  trendChart?: TrendChartDay[],
  trendAnalysis?: TrendAnalysis | null,
  trendRange: TrendRange = DEFAULT_TREND_RANGE
): string {
  if (!trendChart || trendChart.length === 0) {
    return "";
  }

  const isHourly = trendRange === "24h";
  const bucketCount = isHourly ? 24 : trendRange === "7d" ? 7 : trendRange === "30d" ? 30 : 90;

  const dataMap = new Map<string, TrendChartDay>();
  for (const day of trendChart) {
    dataMap.set(day.date, day);
  }

  const now = new Date();
  const bars: string[] = [];

  for (let i = bucketCount - 1; i >= 0; i--) {
    let bucketKey: string;
    let label: string;

    if (isHourly) {
      const d = new Date(now);
      d.setHours(d.getHours() - i, 0, 0, 0);
      // Match aggregateByHour key format: "YYYY-MM-DDTHH"
      bucketKey = d.toISOString().slice(0, 13);
      label = String(d.getHours());
    } else {
      const d = new Date(now);
      d.setUTCDate(d.getUTCDate() - i);
      // Use UTC date format to match test data and persistence format
      const year = d.getUTCFullYear();
      const month = String(d.getUTCMonth() + 1).padStart(2, "0");
      const date = String(d.getUTCDate()).padStart(2, "0");
      bucketKey = `${year}-${month}-${date}`;
      label = String(d.getUTCDate());
    }

    const entry = dataMap.get(bucketKey);

    if (entry) {
      const height = Math.max(2, entry.avgScore);
      const colorClass =
        entry.avgScore > 70
          ? "trend-bar-green"
          : entry.avgScore >= 50
            ? "trend-bar-yellow"
            : "trend-bar-red";
      const tooltip = isHourly
        ? `${escapeAttr(bucketKey)}:00: ${entry.avgScore} (${entry.count} run${entry.count !== 1 ? "s" : ""})`
        : `${escapeAttr(bucketKey)}: ${entry.avgScore} (${entry.count} run${entry.count !== 1 ? "s" : ""})`;
      bars.push(`
        <div class="trend-bar ${colorClass}" style="height: ${height}%" title="${tooltip}">
          <span class="trend-bar-label">${label}</span>
        </div>
      `);
    } else {
      const tooltip = isHourly
        ? `${escapeAttr(bucketKey)}:00: no data`
        : `${escapeAttr(bucketKey)}: no data`;
      bars.push(`
        <div class="trend-bar trend-bar-empty" title="${tooltip}">
          <span class="trend-bar-label">${label}</span>
        </div>
      `);
    }
  }

  const trendArrowHtml = trendAnalysis ? getTrendAnalysisHtml(trendAnalysis) : "";

  // Build range selector options
  const rangeOptions = (["24h", "7d", "30d", "90d"] as TrendRange[])
    .map(
      (r) =>
        `<option value="${r}"${r === trendRange ? " selected" : ""}>${escapeHtml(TREND_RANGE_LABELS[r])}</option>`
    )
    .join("");

  return `
    <div class="health-trend-section">
      <div class="trend-header">
        <h3>Health Trend</h3>
        <select class="trend-range-select" id="trendRangeSelect">
          ${rangeOptions}
        </select>
      </div>
      <div class="trend-chart">
        ${bars.join("")}
      </div>
      ${trendArrowHtml}
    </div>
  `;
}

/**
 * Trend analysis summary line
 */
function getTrendAnalysisHtml(analysis: TrendAnalysis): string {
  const arrowClass =
    analysis.direction === "improving"
      ? "trend-improving"
      : analysis.direction === "declining"
        ? "trend-declining"
        : "trend-stable-dir";
  const arrow =
    analysis.direction === "improving" ? "↑" : analysis.direction === "declining" ? "↓" : "→";

  return `
    <div class="trend-analysis">
      <span class="trend-arrow ${arrowClass}">${arrow}</span>
      <span>${escapeHtml(analysis.message)}</span>
    </div>
  `;
}

/**
 * Render per-dimension sparkline mini-charts from time-series trends.jsonl.
 * Only renders when dimensionSparklines is present and non-empty.
 *
 * @see Issue #1411 - Health trend persistence and dashboard sparklines
 */
export function getDimensionSparklinesSectionHtml(sparklines?: DimensionSparkline[]): string {
  if (!sparklines || sparklines.length === 0) return "";

  const cards = sparklines
    .map((s) => {
      const trendArrow = s.trend === "improving" ? "↑" : s.trend === "declining" ? "↓" : "→";
      const trendClass =
        s.trend === "improving"
          ? "dim-trend-improving"
          : s.trend === "declining"
            ? "dim-trend-declining"
            : "dim-trend-stable";

      // Render bars: each score as a CSS variable-height bar (0-100%)
      const bars = s.data
        .map((score) => {
          const height = Math.max(2, score); // min 2% for visibility
          const colorClass =
            score > 70 ? "dim-bar-green" : score >= 50 ? "dim-bar-yellow" : "dim-bar-red";
          return `<div class="dim-bar ${colorClass}" style="height: ${height}%" title="${escapeAttr(String(Math.round(score)))}"></div>`;
        })
        .join("");

      const lastScore = s.data.length > 0 ? Math.round(s.data[s.data.length - 1]) : "—";

      return `
        <div class="dim-sparkline-card">
          <div class="dim-sparkline-header">
            <span class="dim-sparkline-label">${escapeHtml(s.label)}</span>
            <span class="dim-sparkline-trend ${trendClass}">${trendArrow}</span>
          </div>
          <div class="dim-sparkline-bars">${bars}</div>
          <div class="dim-sparkline-score">${lastScore}</div>
        </div>
      `;
    })
    .join("");

  return `
    <div class="dimension-sparklines">
      <h3>Dimension Trends</h3>
      <div class="sparklines-grid">
        ${cards}
      </div>
    </div>
  `;
}

/**
 * Get CSS styles for the health widget
 */
export function getHealthWidgetStyles(): string {
  return `
    .health-widget {
      margin-bottom: 16px;
      border: 1px solid var(--vscode-panel-border, #333);
      border-radius: 6px;
      overflow: hidden;
    }
    .health-widget.collapsed .health-widget-body {
      display: none;
    }
    .health-widget-header {
      display: flex;
      align-items: center;
      gap: 8px;
      padding: 10px 14px;
      cursor: pointer;
      background: var(--vscode-sideBar-background, #1e1e1e);
      user-select: none;
    }
    .health-widget-header h2 {
      margin: 0;
      font-size: 14px;
      flex: 1;
    }
    .toggle-icon {
      font-size: 10px;
      color: var(--vscode-descriptionForeground);
    }
    .health-badge {
      font-size: 11px;
      font-weight: 600;
      padding: 2px 8px;
      border-radius: 10px;
      text-transform: uppercase;
    }
    .health-excellent { background: rgba(75, 192, 75, 0.2); color: #4bc04b; }
    .health-good { background: rgba(54, 162, 235, 0.2); color: #36a2eb; }
    .health-fair { background: rgba(255, 206, 86, 0.2); color: #ffce56; }
    .health-poor { background: rgba(255, 159, 64, 0.2); color: #ff9f40; }
    .health-critical { background: rgba(255, 99, 132, 0.2); color: #ff6384; }
    .health-score-inline {
      font-size: 16px;
      font-weight: 700;
      min-width: 30px;
      text-align: right;
    }
    .health-widget-body {
      padding: 14px;
    }
    .health-empty-state {
      text-align: center;
      padding: 24px 14px;
      color: var(--vscode-descriptionForeground);
    }
    .empty-icon {
      font-size: 32px;
      margin-bottom: 8px;
    }
    .empty-hint {
      font-size: 12px;
      opacity: 0.7;
    }
    .health-summary-card {
      display: flex;
      gap: 16px;
      margin-bottom: 14px;
    }
    .health-score-large {
      font-size: 36px;
      font-weight: 700;
      min-width: 60px;
      text-align: center;
      line-height: 1;
      padding: 8px;
    }
    .health-component-breakdown {
      flex: 1;
    }
    .health-component-row {
      display: flex;
      align-items: center;
      gap: 6px;
      margin-bottom: 4px;
      font-size: 12px;
    }
    .component-label {
      min-width: 100px;
      color: var(--vscode-descriptionForeground);
    }
    .component-bar-container {
      flex: 1;
      height: 6px;
      background: var(--vscode-progressBar-background, #333);
      border-radius: 3px;
      overflow: hidden;
    }
    .component-bar {
      height: 100%;
      border-radius: 3px;
      transition: width 0.3s ease;
    }
    .component-score {
      min-width: 24px;
      text-align: right;
      font-weight: 600;
    }
    .component-trend {
      min-width: 14px;
    }
    .health-sparklines-section h3,
    .health-alerts-panel h3,
    .health-recommendations h3,
    .health-prediction-card h3 {
      font-size: 13px;
      margin: 12px 0 8px;
      color: var(--vscode-foreground);
    }
    .health-sparklines-window {
      font-size: 11px;
      font-weight: 400;
      color: var(--vscode-descriptionForeground);
      margin-left: 4px;
    }
    .sparklines-grid {
      display: grid;
      grid-template-columns: 1fr 1fr;
      gap: 8px;
    }
    .sparkline-card {
      padding: 8px;
      border: 1px solid var(--vscode-panel-border, #333);
      border-radius: 4px;
    }
    .sparkline-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: 4px;
      font-size: 11px;
    }
    .sparkline-label {
      color: var(--vscode-descriptionForeground);
    }
    .sparkline-value {
      font-size: 16px;
      font-weight: 600;
      padding: 4px 0 0;
      color: var(--vscode-foreground);
    }
    .sparkline-subtitle {
      font-size: 10px;
      color: var(--vscode-descriptionForeground);
      opacity: 0.7;
    }
    /* Sparkline arrow colors are driven by metric polarity, not direction.
       trend-improving is green regardless of arrow direction (rising success
       rate vs falling cost both improve); trend-degrading is red. */
    .trend-improving { color: #4bc04b; }
    .trend-degrading { color: #ff6384; }
    .trend-stable { color: var(--vscode-descriptionForeground); }
    .alert-item {
      display: flex;
      align-items: flex-start;
      gap: 8px;
      padding: 6px 8px;
      margin-bottom: 4px;
      border-radius: 4px;
      font-size: 12px;
    }
    .alert-info { background: rgba(54, 162, 235, 0.1); }
    .alert-warning { background: rgba(255, 206, 86, 0.1); }
    .alert-critical { background: rgba(255, 99, 132, 0.1); }
    .alert-icon { min-width: 14px; }
    .alert-content { flex: 1; }
    .alert-stage {
      font-weight: 600;
      margin-right: 6px;
    }
    .alerts-overflow {
      font-size: 11px;
      color: var(--vscode-descriptionForeground);
      padding-top: 4px;
    }
    .recommendation-card {
      padding: 8px;
      margin-bottom: 6px;
      border: 1px solid var(--vscode-panel-border, #333);
      border-radius: 4px;
    }
    .recommendation-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
    }
    .recommendation-title {
      font-weight: 600;
      font-size: 12px;
    }
    .recommendation-savings {
      color: #4bc04b;
      font-size: 12px;
      font-weight: 600;
    }
    .recommendation-desc {
      font-size: 11px;
      color: var(--vscode-descriptionForeground);
      margin: 4px 0 0;
    }
    .recommendation-actions {
      margin-top: 6px;
      display: flex;
      align-items: center;
      gap: 8px;
    }
    .recommendation-apply-btn {
      background: var(--vscode-button-background);
      color: var(--vscode-button-foreground);
      border: none;
      border-radius: 3px;
      padding: 3px 10px;
      font-size: 11px;
      cursor: pointer;
    }
    .recommendation-apply-btn:hover {
      background: var(--vscode-button-hoverBackground);
    }
    .recommendation-revert-btn {
      background: transparent;
      color: var(--vscode-textLink-foreground);
      border: 1px solid var(--vscode-textLink-foreground);
      border-radius: 3px;
      padding: 2px 8px;
      font-size: 11px;
      cursor: pointer;
    }
    .recommendation-revert-btn:hover {
      background: var(--vscode-textLink-foreground);
      color: var(--vscode-editor-background);
    }
    .recommendation-applied-badge {
      font-size: 11px;
      font-weight: 600;
      color: #4bc04b;
    }
    .recommendation-applied {
      border-color: #4bc04b44;
    }
    .health-prediction-card {
      margin-top: 8px;
    }
    .prediction-stats {
      display: flex;
      gap: 16px;
    }
    .prediction-stat {
      text-align: center;
    }
    .stat-value {
      display: block;
      font-size: 18px;
      font-weight: 700;
    }
    .stat-label {
      display: block;
      font-size: 11px;
      color: var(--vscode-descriptionForeground);
    }
    .health-updated {
      font-size: 11px;
      color: var(--vscode-descriptionForeground);
      margin-top: 10px;
      text-align: right;
    }
    /* Health Trend Chart (Issue #789) */
    .health-trend-section {
      margin-top: 12px;
    }
    .trend-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin: 12px 0 8px;
    }
    .trend-header h3 {
      font-size: 13px;
      margin: 0;
      color: var(--vscode-foreground);
    }
    .trend-range-select {
      font-size: 11px;
      padding: 2px 6px;
      background: var(--vscode-dropdown-background);
      color: var(--vscode-dropdown-foreground);
      border: 1px solid var(--vscode-dropdown-border);
      border-radius: var(--border-radius, 3px);
      cursor: pointer;
    }
    .health-trend-section h3 {
      font-size: 13px;
      margin: 12px 0 8px;
      color: var(--vscode-foreground);
    }
    .trend-chart {
      display: flex;
      align-items: flex-end;
      gap: 2px;
      height: 80px;
      padding: 4px 0;
      border-bottom: 1px solid var(--vscode-panel-border, #333);
    }
    .trend-bar {
      flex: 1;
      min-width: 0;
      border-radius: 2px 2px 0 0;
      position: relative;
      transition: height 0.3s ease;
    }
    .trend-bar-green { background: rgba(75, 192, 75, 0.7); }
    .trend-bar-yellow { background: rgba(255, 206, 86, 0.7); }
    .trend-bar-red { background: rgba(255, 99, 132, 0.7); }
    .trend-bar-empty {
      background: var(--vscode-panel-border, #333);
      height: 2px !important;
    }
    .trend-bar-label {
      position: absolute;
      bottom: -16px;
      left: 50%;
      transform: translateX(-50%);
      font-size: 8px;
      color: var(--vscode-descriptionForeground);
      opacity: 0.6;
      white-space: nowrap;
    }
    .trend-analysis {
      display: flex;
      align-items: center;
      gap: 6px;
      margin-top: 20px;
      font-size: 12px;
    }
    .trend-arrow {
      font-size: 14px;
      font-weight: 700;
    }
    /* Trend-analysis arrow colors (re-uses trend-improving from above). */
    .trend-declining { color: #ff6384; }
    .trend-stable-dir { color: var(--vscode-descriptionForeground); }
    /* Dimension Sparklines (Issue #1411) */
    .dimension-sparklines {
      margin: 12px 0;
    }
    .dimension-sparklines h3 {
      font-size: 13px;
      margin: 0 0 8px;
      color: var(--vscode-foreground);
    }
    .sparklines-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
      gap: 8px;
    }
    .dim-sparkline-card {
      background: var(--vscode-editor-background, #1e1e1e);
      border: 1px solid var(--vscode-panel-border, #333);
      border-radius: 4px;
      padding: 6px 8px;
    }
    .dim-sparkline-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: 4px;
    }
    .dim-sparkline-label {
      font-size: 10px;
      color: var(--vscode-descriptionForeground);
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .dim-sparkline-trend {
      font-size: 11px;
      font-weight: 700;
      flex-shrink: 0;
    }
    .dim-trend-improving { color: #4bc04b; }
    .dim-trend-declining { color: #ff6384; }
    .dim-trend-stable { color: var(--vscode-descriptionForeground); }
    .dim-sparkline-bars {
      display: flex;
      align-items: flex-end;
      height: 28px;
      gap: 1px;
      margin-bottom: 2px;
    }
    .dim-bar {
      flex: 1;
      min-width: 2px;
      border-radius: 1px 1px 0 0;
    }
    .dim-bar-green { background: rgba(75, 192, 75, 0.7); }
    .dim-bar-yellow { background: rgba(255, 206, 86, 0.7); }
    .dim-bar-red { background: rgba(255, 99, 132, 0.7); }
    .dim-sparkline-score {
      font-size: 10px;
      font-weight: 600;
      color: var(--vscode-foreground);
      text-align: right;
    }
    /* Insufficient data visual treatment (Issue #991) */
    .health-component-insufficient {
      opacity: 0.6;
    }
    .health-component-insufficient .component-bar-container {
      border-style: dashed;
      border-width: 1px;
      border-color: var(--vscode-panel-border, #333);
    }
    .health-component-insufficient .component-label::after {
      content: ' (insufficient data)';
      font-style: italic;
      font-size: 0.85em;
      opacity: 0.7;
    }
  `;
}

/**
 * Get JavaScript code for health widget interactivity
 */
export function getHealthWidgetScript(): string {
  // All event handlers use addEventListener instead of inline on* attributes
  // because the webview CSP uses script nonces which block inline handlers.
  return `
    // Health widget collapse/expand
    const healthWidgetHeader = document.getElementById('healthWidgetHeader');
    if (healthWidgetHeader) {
      healthWidgetHeader.addEventListener('click', function() {
        const widget = document.getElementById('healthWidget');
        if (widget) {
          widget.classList.toggle('collapsed');
          const icon = widget.querySelector('.toggle-icon');
          if (icon) {
            icon.textContent = widget.classList.contains('collapsed') ? '▶' : '▼';
          }
          vscode.postMessage({ type: 'healthToggle', collapsed: widget.classList.contains('collapsed') });
        }
      });
    }

    // Health trend range selector
    const trendRangeSelect = document.getElementById('trendRangeSelect');
    if (trendRangeSelect) {
      trendRangeSelect.addEventListener('change', function() {
        vscode.postMessage({
          type: 'healthTrendRange',
          range: this.value,
        });
      });
    }

    // Recommendation apply/revert — event delegation on the widget container
    const healthWidget = document.getElementById('healthWidget');
    if (healthWidget) {
      healthWidget.addEventListener('click', function(e) {
        const target = e.target.closest('[data-action]');
        if (!target) return;

        const action = target.dataset.action;
        if (action === 'apply-recommendation') {
          let value;
          try { value = JSON.parse(target.dataset.value); } catch { value = target.dataset.value; }
          vscode.postMessage({
            type: 'applyRecommendation',
            category: target.dataset.category,
            configPath: target.dataset.configPath,
            value: value,
          });
        } else if (action === 'revert-recommendation') {
          vscode.postMessage({
            type: 'revertRecommendation',
            category: target.dataset.category,
          });
        }
      });
    }
  `;
}
