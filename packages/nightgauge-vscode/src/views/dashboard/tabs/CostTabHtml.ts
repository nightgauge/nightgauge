/**
 * CostTabHtml - Cost analysis tab renderer
 *
 * Extracted from DashboardHtml.ts as part of the tab-based modular refactor (#1542).
 * Platform cost tab (getCostTabHtml/Script/Styles) added by Issue #3317.
 */

import { escapeHtml, formatStageName, formatTokenCount } from "../DashboardComponents";
import type { CostAnalyticsResult } from "../../../services/IpcClientBase";
import type { CostDateRange } from "../../../services/PlatformCostService";
import type { ModelRoutingMetrics, UsageLimitsData, PlatformQuotaData } from "../DashboardState";
import type {
  CostSummary,
  CostHistoryEntry,
  PerModeCostRollup,
  ModeFilter,
  BudgetVsActualStageStat,
} from "../CostSummaryCalculator";
import type { PipelineCostEstimate } from "@nightgauge/sdk";
import type { PerformanceMode } from "../../../utils/modeProfiles";

export function getCostEstimateWidgetHtml(estimate: PipelineCostEstimate | null): string {
  if (!estimate) return "";

  const stageRows = estimate.stages
    .map(
      (s) =>
        `<tr class="${s.skipped ? "skipped-row" : ""}">
          <td>${formatStageName(s.stage)}</td>
          <td><span class="model-pill">${escapeHtml(s.model)}</span></td>
          <td>${escapeHtml(s.effort)}</td>
          <td>${s.skipped ? "—" : "$" + s.estimatedCost.toFixed(4)}</td>
          <td>${s.skipped ? "Skipped" : (s.confidence * 100).toFixed(0) + "%"}</td>
        </tr>`
    )
    .join("");

  const savingsVsSonnet = estimate.comparisonAllSonnet - estimate.totalEstimatedCost;
  const savingsPercent =
    estimate.comparisonAllSonnet > 0 ? (savingsVsSonnet / estimate.comparisonAllSonnet) * 100 : 0;
  const savingsLabel =
    savingsVsSonnet > 0
      ? `saves ${savingsPercent.toFixed(1)}%`
      : savingsVsSonnet < 0
        ? `${Math.abs(savingsPercent).toFixed(1)}% premium for complex stages`
        : "same cost";

  return `
    <details class="collapsible-section" open>
      <summary class="section-toggle">
        <span class="toggle-icon">▼</span>
        <h3>📊 Pre-Run Cost Estimate</h3>
        <span class="section-badge">~$${estimate.totalEstimatedCost.toFixed(4)}</span>
      </summary>
      <div class="section-content">
        <div class="cost-summary-widget">
          <div class="cost-total-header">
            <div class="cost-total-value">~$${estimate.totalEstimatedCost.toFixed(4)}</div>
            <div class="cost-total-label">Estimated Pipeline Cost (${escapeHtml(estimate.complexity)} complexity)</div>
          </div>

          <div class="cost-comparison-summary">
            <span>Auto routing: ~$${estimate.totalEstimatedCost.toFixed(4)}</span>
            <span> vs </span>
            <span>All-sonnet: ~$${estimate.comparisonAllSonnet.toFixed(4)}</span>
            <span class="cost-comparison-note">(${escapeHtml(savingsLabel)})</span>
          </div>

          <table class="cost-stage-table">
            <thead>
              <tr>
                <th>Stage</th>
                <th>Model</th>
                <th>Effort</th>
                <th>Est. Cost</th>
                <th>Confidence</th>
              </tr>
            </thead>
            <tbody>
              ${stageRows}
            </tbody>
            <tfoot>
              <tr class="cost-total-row">
                <td colspan="3"><strong>Total</strong></td>
                <td><strong>~$${estimate.totalEstimatedCost.toFixed(4)}</strong></td>
                <td></td>
              </tr>
            </tfoot>
          </table>
        </div>
      </div>
    </details>`;
}

/**
 * Generate cost summary widget HTML
 *
 * Shows total pipeline cost, per-stage cost table with model attribution,
 * hypothetical comparison bar, savings badge, and cost trend sparkline.
 *
 * @see Issue #945 - Per-Pipeline Cost Summary with Model-Per-Stage Breakdown
 */
export function getCostSummaryWidgetHtml(
  costSummary: CostSummary | null,
  costHistory: CostHistoryEntry[]
): string {
  if (!costSummary) return "";

  // Per-stage table rows
  const stageRows = costSummary.stages
    .map(
      (s) =>
        `<tr>
          <td>${formatStageName(s.stage)}</td>
          <td><span class="model-pill">${escapeHtml(s.model)}</span></td>
          <td>${escapeHtml(s.effortLevel)}</td>
          <td>${formatTokenCount(s.inputTokens)}</td>
          <td>${formatTokenCount(s.outputTokens)}</td>
          <td>$${s.costUsd.toFixed(4)}</td>
          <td>${s.percentOfTotal.toFixed(1)}%</td>
        </tr>`
    )
    .join("");

  // Savings bar (only shown for automatic/hybrid routing with positive savings)
  const showSavings =
    costSummary.savingsPercent > 0 &&
    (costSummary.routingMode === "automatic" || costSummary.routingMode === "hybrid");
  const actualPct =
    costSummary.hypotheticalDefaultCostUsd > 0
      ? (costSummary.totalCostUsd / costSummary.hypotheticalDefaultCostUsd) * 100
      : 100;
  const savingsBarHtml = showSavings
    ? `<div class="cost-savings-section">
        <div class="savings-badge">Saved ${costSummary.savingsPercent.toFixed(1)}% via ${costSummary.routingMode} routing</div>
        <div class="cost-comparison-bar">
          <div class="cost-bar-actual" style="width: ${Math.min(actualPct, 100)}%;">
            <span>Actual: $${costSummary.totalCostUsd.toFixed(4)}</span>
          </div>
          <div class="cost-bar-hypothetical">
            <span>Default (${costSummary.defaultModel}): $${costSummary.hypotheticalDefaultCostUsd.toFixed(4)}</span>
          </div>
        </div>
      </div>`
    : "";

  // Cost trend sparkline (inline SVG)
  const sparklineHtml = costHistory.length >= 2 ? getCostSparklineSvg(costHistory) : "";

  return `
    <details class="collapsible-section">
      <summary class="section-toggle">
        <span class="toggle-icon">▼</span>
        <h3>💰 Cost Summary</h3>
        <span class="section-badge">$${costSummary.totalCostUsd.toFixed(4)}</span>
      </summary>
      <div class="section-content">
        <div class="cost-summary-widget">
          <div class="cost-total-header">
            <div class="cost-total-value">$${costSummary.totalCostUsd.toFixed(4)}</div>
            <div class="cost-total-label">Total Pipeline Cost (${costSummary.stages.length} stages)</div>
          </div>

          ${savingsBarHtml}

          <table class="cost-stage-table">
            <thead>
              <tr>
                <th>Stage</th>
                <th>Model</th>
                <th>Effort</th>
                <th>Input</th>
                <th>Output</th>
                <th>Cost</th>
                <th>%</th>
              </tr>
            </thead>
            <tbody>
              ${stageRows}
            </tbody>
            <tfoot>
              <tr class="cost-total-row">
                <td colspan="5"><strong>Total</strong></td>
                <td><strong>$${costSummary.totalCostUsd.toFixed(4)}</strong></td>
                <td><strong>100%</strong></td>
              </tr>
            </tfoot>
          </table>

          ${sparklineHtml ? `<div class="cost-trend-section"><h4>Cost Trend (Last ${costHistory.length} Runs)</h4>${sparklineHtml}</div>` : ""}
        </div>
      </div>
    </details>`;
}

/**
 * Render the per-mode cost rollup card (Issue #3218).
 *
 * Shows total cost, run count, and per-stage p50/p95 for each performance
 * mode. Returns empty string when `rollup` is null. When `modeFilter` is set
 * to a concrete mode, the card renders a "Filtered to <mode>" caption — the
 * surrounding cost summary already reflects the filter, so the rollup is the
 * comparison view that shows what you're filtering away.
 *
 * Per ADR-004, stages without a recorded `performance_mode` are excluded from
 * the three concrete buckets and surfaced in `excludedUnknownStageCount`.
 */
export function getPerModeCostRollupHtml(
  rollup: PerModeCostRollup | null,
  modeFilter: ModeFilter = "all"
): string {
  if (!rollup) return "";

  const modes: PerformanceMode[] = ["efficiency", "elevated", "maximum", "frontier"];
  const cards = modes
    .map((mode) => {
      const bucket = rollup[mode];
      const isActiveFilter = modeFilter === mode;
      const excluded =
        bucket.excludedUnknownStageCount > 0
          ? `<div class="per-mode-card-excluded">${bucket.excludedUnknownStageCount} stage row${bucket.excludedUnknownStageCount === 1 ? "" : "s"} excluded — pre-#3215 history without performance_mode</div>`
          : "";
      const stageRows = bucket.perStageP50Usd
        .map((s) => {
          return `<tr>
            <td>${formatStageName(s.stage)}</td>
            <td>$${s.p50CostUsd.toFixed(4)}</td>
            <td>$${s.p95CostUsd.toFixed(4)}</td>
            <td>${s.sampleCount}</td>
          </tr>`;
        })
        .join("");
      return `<div class="per-mode-card${isActiveFilter ? " per-mode-card-active" : ""}">
        <div class="per-mode-card-header">
          <span class="mode-pill mode-${mode}">${escapeHtml(mode)}</span>
          <span class="per-mode-total">$${bucket.totalCostUsd.toFixed(4)}</span>
          <span class="per-mode-runs">${bucket.runCount} run${bucket.runCount === 1 ? "" : "s"}</span>
        </div>
        ${
          stageRows
            ? `<table class="per-mode-stage-table">
          <thead><tr><th>Stage</th><th>p50</th><th>p95</th><th>n</th></tr></thead>
          <tbody>${stageRows}</tbody>
        </table>`
            : `<div class="per-mode-card-empty">No runs recorded for this mode.</div>`
        }
        ${excluded}
      </div>`;
    })
    .join("");

  const caption =
    modeFilter !== "all"
      ? `<p class="caption">Filtered to <strong>${escapeHtml(modeFilter)}</strong> mode. Other-mode columns kept for comparison.</p>`
      : "";

  return `
    <details class="collapsible-section per-mode-cost-rollup" open>
      <summary class="section-toggle">
        <span class="toggle-icon">▼</span>
        <h4>Per-Mode Cost Rollup</h4>
      </summary>
      <div class="section-content">
        ${caption}
        <div class="per-mode-grid">${cards}</div>
      </div>
    </details>`;
}

/**
 * Generate inline SVG sparkline for cost trend
 */
export function getCostSparklineSvg(history: CostHistoryEntry[]): string {
  if (history.length < 2) return "";

  const width = 300;
  const height = 50;
  const padding = 4;

  const costs = history.map((h) => h.costUsd);
  const maxCost = Math.max(...costs);
  const minCost = Math.min(...costs);
  const range = maxCost - minCost || 1;

  const points = costs
    .map((cost, i) => {
      const x = padding + (i / (costs.length - 1)) * (width - padding * 2);
      const y = height - padding - ((cost - minCost) / range) * (height - padding * 2);
      return `${x},${y}`;
    })
    .join(" ");

  return `
    <svg class="cost-sparkline" viewBox="0 0 ${width} ${height}" width="${width}" height="${height}">
      <polyline
        points="${points}"
        fill="none"
        stroke="var(--vscode-charts-blue)"
        stroke-width="2"
        stroke-linecap="round"
        stroke-linejoin="round"
      />
    </svg>
    <div class="sparkline-labels">
      <span>#${history[0].issueNumber}</span>
      <span>#${history[history.length - 1].issueNumber}</span>
    </div>`;
}

/**
 * Generate model routing summary widget HTML
 *
 * Shows auto-selection success rate, cost, confidence distribution,
 * and per-stage breakdown. Returns empty string when no data available.
 *
 * @see Issue #734 - Learning Feedback Loop & Model Routing Report
 */
export function getModelRoutingWidgetHtml(metrics: ModelRoutingMetrics | null): string {
  if (!metrics) return "";

  const successPct = Math.round(metrics.overallSuccessRate * 100);
  const totalConf =
    metrics.confidenceDistribution.low +
    metrics.confidenceDistribution.medium +
    metrics.confidenceDistribution.high;

  // Model usage pills
  const modelPills = Object.entries(metrics.modelUsage)
    .sort((a, b) => b[1] - a[1])
    .map(
      ([model, count]) =>
        `<span class="model-pill">${escapeHtml(model)} <strong>${count}</strong></span>`
    )
    .join(" ");

  // Per-stage rows
  const stageRows = metrics.perStage
    .sort((a, b) => b.totalRuns - a.totalRuns)
    .map(
      (s) =>
        `<tr>
          <td>${formatStageName(s.stage)}</td>
          <td>${s.totalRuns}</td>
          <td>${Math.round(s.successRate * 100)}%</td>
          <td>$${s.totalCostUsd.toFixed(4)}</td>
        </tr>`
    )
    .join("");

  return `
    <details class="collapsible-section">
      <summary class="section-toggle">
        <span class="toggle-icon">▼</span>
        <h3>🤖 Model Routing Summary</h3>
      </summary>
      <div class="section-content">
        <div class="model-routing-widget">
          <div class="summary-cards">
            <div class="summary-card">
              <div class="card-value">${metrics.totalAutoSelectedRuns}</div>
              <div class="card-label">Auto-Selected Runs</div>
            </div>
            <div class="summary-card">
              <div class="card-value ${successPct >= 80 ? "status-good" : successPct >= 60 ? "status-fair" : "status-poor"}">${successPct}%</div>
              <div class="card-label">Success Rate</div>
            </div>
            <div class="summary-card">
              <div class="card-value">$${metrics.totalCostUsd.toFixed(4)}</div>
              <div class="card-label">Total Cost</div>
            </div>
          </div>

          <div class="model-routing-details">
            <div class="model-routing-section">
              <h4>Model Usage</h4>
              <div class="model-pills">${modelPills}</div>
            </div>

            <div class="model-routing-section">
              <h4>Confidence Distribution</h4>
              <div class="confidence-bars">
                <div class="confidence-bar">
                  <span class="confidence-label">High (≥0.8)</span>
                  <div class="bar-track">
                    <div class="bar-fill bar-high" style="width: ${totalConf > 0 ? Math.round((metrics.confidenceDistribution.high / totalConf) * 100) : 0}%"></div>
                  </div>
                  <span class="confidence-count">${metrics.confidenceDistribution.high}</span>
                </div>
                <div class="confidence-bar">
                  <span class="confidence-label">Medium (0.5–0.8)</span>
                  <div class="bar-track">
                    <div class="bar-fill bar-medium" style="width: ${totalConf > 0 ? Math.round((metrics.confidenceDistribution.medium / totalConf) * 100) : 0}%"></div>
                  </div>
                  <span class="confidence-count">${metrics.confidenceDistribution.medium}</span>
                </div>
                <div class="confidence-bar">
                  <span class="confidence-label">Low (&lt;0.5)</span>
                  <div class="bar-track">
                    <div class="bar-fill bar-low" style="width: ${totalConf > 0 ? Math.round((metrics.confidenceDistribution.low / totalConf) * 100) : 0}%"></div>
                  </div>
                  <span class="confidence-count">${metrics.confidenceDistribution.low}</span>
                </div>
              </div>
            </div>
          </div>

          ${
            stageRows
              ? `<div class="model-routing-section">
              <h4>Per-Stage Breakdown</h4>
              <table class="model-routing-table">
                <thead>
                  <tr>
                    <th>Stage</th>
                    <th>Runs</th>
                    <th>Success</th>
                    <th>Cost</th>
                  </tr>
                </thead>
                <tbody>${stageRows}</tbody>
              </table>
            </div>`
              : ""
          }
        </div>
      </div>
    </details>
  `;
}

export function getUsageLimitsSectionHtml(data: UsageLimitsData | null): string {
  if (!data || data.budgetUsd <= 0) {
    return "";
  }

  const { costUsd, budgetUsd, usagePct } = data;
  const pctClamped = Math.min(100, usagePct);
  const remaining = budgetUsd - costUsd;
  const pctRounded = Math.round(usagePct);

  const barClass =
    usagePct >= 90 ? "usage-bar-critical" : usagePct >= 80 ? "usage-bar-warning" : "usage-bar-ok";

  const statusBadge =
    usagePct >= 90
      ? '<span class="badge badge-danger">Critical</span>'
      : usagePct >= 80
        ? '<span class="badge badge-warning">Warning</span>'
        : '<span class="badge badge-success">OK</span>';

  return `
  <details class="collapsible-section" open>
    <summary class="section-toggle">
      <span class="toggle-icon">▼</span>
      <h3>Usage &amp; Limits ${statusBadge}</h3>
    </summary>
    <div class="section-content">
      <div class="usage-limits-card">
        <div class="usage-limits-row">
          <span class="usage-label">Monthly Budget</span>
          <span class="usage-value">$${costUsd.toFixed(2)} used of $${budgetUsd.toFixed(2)} (${pctRounded}%)</span>
        </div>
        <div class="usage-progress-track">
          <div class="usage-progress-bar ${barClass}" style="width: ${pctClamped.toFixed(1)}%"></div>
        </div>
        <div class="usage-limits-row usage-limits-meta">
          <span class="usage-remaining">$${remaining.toFixed(2)} remaining</span>
          <button class="action-btn action-btn-sm" id="resetUsageCounterBtn" title="Reset usage counter to track from current total">Reset Counter</button>
        </div>
        <p class="usage-reset-hint">Budget tracking is cumulative. Use <strong>Reset Counter</strong> to start tracking from this point (e.g., at the start of a new billing period).</p>
      </div>
    </div>
  </details>`;
}

/**
 * Render the Platform Quota section for the dashboard (Issue #1479).
 *
 * Shows server-side quota (pipeline runs, tokens) fetched from
 * GET /v1/analytics/usage. Returns '' when data is null (no fetch yet).
 */
export function getPlatformQuotaSectionHtml(data: PlatformQuotaData | null): string {
  if (!data) return "";

  const formatDate = (iso: string): string => {
    try {
      return new Date(iso).toLocaleDateString(undefined, {
        month: "short",
        day: "numeric",
        year: "numeric",
      });
    } catch {
      return iso;
    }
  };

  const staleBanner = data.isStale
    ? `<div class="quota-stale-banner">Showing cached data from ${formatDate(data.lastFetchedAt)}</div>`
    : "";

  if (data.isCommunity) {
    return `
  <details class="collapsible-section" open>
    <summary class="section-toggle">
      <span class="toggle-icon">▼</span>
      <h3>Platform Quota</h3>
    </summary>
    <div class="section-content">
      ${staleBanner}
      <div class="quota-community-card">
        <span class="quota-community-icon">∞</span>
        <span class="quota-community-text">Unlimited community features</span>
      </div>
    </div>
  </details>`;
  }

  const renderMetricRow = (
    label: string,
    used: number,
    limit: number | null,
    pct: number | null
  ): string => {
    if (limit === null) {
      return `
        <div class="quota-metric-row">
          <span class="usage-label">${label}</span>
          <span class="usage-value">${used.toLocaleString()} used &mdash; <em>Unlimited</em></span>
        </div>`;
    }
    const pctClamped = Math.min(100, pct ?? 0);
    const pctRounded = Math.round(pct ?? 0);
    const barClass =
      (pct ?? 0) >= 90
        ? "usage-bar-critical"
        : (pct ?? 0) >= 80
          ? "usage-bar-warning"
          : "usage-bar-ok";
    const badge =
      (pct ?? 0) >= 90
        ? '<span class="badge badge-danger">Critical</span>'
        : (pct ?? 0) >= 80
          ? '<span class="badge badge-warning">Warning</span>'
          : "";
    return `
        <div class="quota-metric-row">
          <span class="usage-label">${label} ${badge}</span>
          <span class="usage-value">${used.toLocaleString()} / ${limit.toLocaleString()} (${pctRounded}%)</span>
        </div>
        <div class="usage-progress-track">
          <div class="usage-progress-bar ${barClass}" style="width: ${pctClamped.toFixed(1)}%"></div>
        </div>`;
  };

  const periodHtml =
    data.period != null
      ? `<p class="quota-period-hint">Billing period: ${formatDate(data.period.start)} – ${formatDate(data.period.end)}</p>`
      : "";

  return `
  <details class="collapsible-section" open>
    <summary class="section-toggle">
      <span class="toggle-icon">▼</span>
      <h3>Platform Quota</h3>
    </summary>
    <div class="section-content">
      ${staleBanner}
      <div class="platform-quota-card">
        ${renderMetricRow("Pipeline Runs", data.pipelineRuns.used, data.pipelineRuns.limit, data.pipelineRuns.pct)}
        ${renderMetricRow("Tokens", data.tokens.used, data.tokens.limit, data.tokens.pct)}
        ${periodHtml}
      </div>
    </div>
  </details>`;
}

// ============================================================================
// Cost Cap Tightness Warning Panel (Issue #3276)
// ============================================================================

export interface CostCapWarningRow {
  stage: string;
  effectiveCap: number;
  historicalMedian: number;
  threshold: number;
  multiplier: number;
  capEnvKey: string;
  capConfigPath: string;
  isTight: boolean;
  /** Warn threshold (historicalMedian × warnMultiplier). 0 = disabled/no history. Issue #3508 */
  warnThresholdUsd: number;
  /** Runaway ceiling (max($75, effectiveCap × ceilingMultiplier)). 0 = uncapped. Issue #3508 */
  ceilingUsd: number;
}

/**
 * Renders a per-stage cost cap tightness table for the Cost tab dashboard.
 * Rows with `isTight: true` are highlighted. Follows the StallThresholdPanel
 * pattern from PerformanceTabHtml.ts.
 *
 * Empty rows array → empty string (panel hidden).
 *
 * @see Issue #3276
 */
export function getCostCapWarningTableHtml(rows: CostCapWarningRow[]): string {
  if (rows.length === 0) return "";

  const tbody = rows
    .map((row) => {
      const medianCell = row.historicalMedian > 0 ? `$${row.historicalMedian.toFixed(2)}` : "—";
      const thresholdCell = row.historicalMedian > 0 ? `$${row.threshold.toFixed(2)}` : "—";
      const warnCell = row.warnThresholdUsd > 0 ? `$${row.warnThresholdUsd.toFixed(2)}` : "—";
      const ceilingCell = row.ceilingUsd > 0 ? `$${row.ceilingUsd.toFixed(2)}` : "—";
      const status = row.isTight
        ? `<span class="badge badge-warning">⚠ Too tight</span>`
        : `<span class="badge badge-ok">✓ OK</span>`;
      return `<tr data-stage="${row.stage}" data-is-tight="${row.isTight}" class="${row.isTight ? "cost-cap-tight" : ""}">
        <td>${formatStageName(row.stage)}</td>
        <td>$${row.effectiveCap.toFixed(2)}</td>
        <td>${medianCell}</td>
        <td>${thresholdCell}</td>
        <td>${warnCell}</td>
        <td>${ceilingCell}</td>
        <td>${status}</td>
      </tr>`;
    })
    .join("");

  return `
    <details class="collapsible-section cost-cap-warning-panel" open>
      <summary class="section-toggle">
        <span class="toggle-icon">▼</span>
        <h4>Cost Cap Tightness</h4>
      </summary>
      <div class="section-content">
        <p class="caption">Stages whose configured cap is below <strong>historical median × ${rows[0]?.multiplier ?? 1.2}</strong>. A tight cap may kill the stage mid-run. Rows with insufficient history (&lt;3 runs) show "—". <strong>Warn At</strong> fires a non-blocking toast; <strong>Ceiling</strong> kills and auto-retries in 30 min (Issue #3508).</p>
        <table class="cost-stage-table cost-cap-warning-table">
          <thead>
            <tr>
              <th>Stage</th>
              <th>Cap</th>
              <th>Median</th>
              <th>Threshold</th>
              <th>Warn At</th>
              <th>Ceiling</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>
            ${tbody}
          </tbody>
        </table>
      </div>
    </details>`;
}

/**
 * Renders the budget vs actual panel for the Cost tab dashboard (Issue #3269).
 *
 * Shows per-stage, per-execution-path p50/p90/ratio-to-cap stats.
 * Hidden when rows array is empty or all samples were below the minimum threshold.
 */
export function getBudgetVsActualPanelHtml(stats: BudgetVsActualStageStat[]): string {
  if (stats.length === 0) return "";

  const tbody = stats
    .map((row) => {
      const pathLabel =
        row.executionPath === "deterministic" ? "det." : row.executionPath === "llm" ? "llm" : "?";
      const capCell = row.capUsd > 0 ? `$${row.capUsd.toFixed(2)}` : "—";
      const ratioCell = !isNaN(row.ratioToCap) ? `${(row.ratioToCap * 100).toFixed(0)}%` : "—";
      const status = row.isOverProvisioned
        ? `<span class="badge badge-info">⬇ Over-provisioned</span>`
        : !isNaN(row.ratioToCap) && row.ratioToCap > 0.8
          ? `<span class="badge badge-warning">⚠ Tight</span>`
          : `<span class="badge badge-ok">✓ OK</span>`;
      return `<tr>
        <td>${formatStageName(row.stage)}</td>
        <td><span class="path-pill path-${row.executionPath}">${pathLabel}</span></td>
        <td>${row.sampleCount}</td>
        <td>$${row.p50CostUsd.toFixed(4)}</td>
        <td>$${row.p90CostUsd.toFixed(4)}</td>
        <td>${capCell}</td>
        <td>${ratioCell}</td>
        <td>${status}</td>
      </tr>`;
    })
    .join("");

  return `
    <details class="collapsible-section budget-vs-actual-panel" open>
      <summary class="section-toggle">
        <span class="toggle-icon">▼</span>
        <h4>Budget vs Actual</h4>
      </summary>
      <div class="section-content">
        <p class="caption">Per-stage p50/p90 vs configured M-size cap, segmented by execution path. <strong>det.</strong> = deterministic (≈$0); <strong>llm</strong> = LLM skill path. ⬇ Over-provisioned = cap &gt; 2× p90. Minimum 3 samples required.</p>
        <table class="cost-stage-table budget-vs-actual-table">
          <thead>
            <tr>
              <th>Stage</th>
              <th>Path</th>
              <th>n</th>
              <th>p50</th>
              <th>p90</th>
              <th>Cap (M)</th>
              <th>Ratio</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>${tbody}</tbody>
        </table>
      </div>
    </details>`;
}

export function getCostTabStyles(): string {
  return `
    /* Budget vs Actual Panel (Issue #3269) */
    .path-pill {
      display: inline-block;
      padding: 1px 5px;
      border-radius: 3px;
      font-size: 0.8em;
      font-family: monospace;
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
    }
    .path-pill.path-deterministic { background: var(--vscode-charts-green, #4ec9b0); color: var(--vscode-editor-background); }
    .path-pill.path-llm { background: var(--vscode-charts-blue, #569cd6); color: var(--vscode-editor-background); }
    .badge-info {
      background: var(--vscode-charts-blue, #569cd6);
      color: var(--vscode-editor-background);
      border-radius: 3px;
      padding: 1px 6px;
      font-size: 0.8em;
    }
    /* Per-Mode Cost Rollup (Issue #3218) */
    .per-mode-cost-rollup .per-mode-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
      gap: var(--spacing-md, 12px);
    }
    .per-mode-card {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius, 4px);
      padding: var(--spacing-sm, 8px);
    }
    .per-mode-card-active {
      border-color: var(--vscode-progressBar-background);
    }
    .per-mode-card-header {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm, 8px);
      margin-bottom: 6px;
    }
    .per-mode-total {
      font-weight: 600;
    }
    .per-mode-runs {
      color: var(--vscode-descriptionForeground);
      font-size: 0.85em;
    }
    .per-mode-card-excluded {
      margin-top: 4px;
      color: var(--vscode-descriptionForeground);
      font-size: 0.8em;
      font-style: italic;
    }
    .per-mode-card-empty {
      color: var(--vscode-descriptionForeground);
      font-size: 0.85em;
      padding: 4px 0;
    }
    .per-mode-stage-table {
      width: 100%;
      font-size: 0.85em;
      border-collapse: collapse;
    }
    .per-mode-stage-table th, .per-mode-stage-table td {
      padding: 2px 4px;
      text-align: left;
    }
    /* Mode pill (shared with Stall Threshold table — Issue #3218) */
    .mode-pill {
      padding: 1px 6px;
      border-radius: 3px;
      font-size: 0.8em;
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
    }
    .mode-pill.mode-efficiency { background: var(--vscode-charts-green, #4ec9b0); color: var(--vscode-editor-background); }
    .mode-pill.mode-elevated { background: var(--vscode-charts-blue, #569cd6); color: var(--vscode-editor-background); }
    .mode-pill.mode-maximum { background: var(--vscode-charts-purple, #b267e6); color: var(--vscode-editor-background); }
    /* Stall threshold table (Issue #3218) */
    .stall-threshold-panel .caption {
      color: var(--vscode-descriptionForeground);
      font-size: 0.85em;
      margin: 0 0 6px 0;
    }
    .stall-threshold-table em {
      color: var(--vscode-descriptionForeground);
      font-style: italic;
    }
    /* Model Routing Widget (Issue #734) */
    .model-routing-widget .summary-cards {
      display: grid;
      grid-template-columns: repeat(3, 1fr);
      gap: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }

    .model-routing-widget .summary-card {
      background: var(--vscode-editor-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
      text-align: center;
    }

    .model-routing-widget .card-value {
      font-size: 1.8em;
      font-weight: bold;
    }

    .model-routing-widget .card-label {
      color: var(--vscode-descriptionForeground);
      font-size: 0.85em;
      margin-top: var(--spacing-xs);
    }

    .model-routing-widget .status-good { color: var(--vscode-terminal-ansiGreen); }
    .model-routing-widget .status-fair { color: var(--vscode-terminal-ansiYellow); }
    .model-routing-widget .status-poor { color: var(--vscode-terminal-ansiRed); }

    .model-routing-details {
      display: grid;
      grid-template-columns: 1fr 1fr;
      gap: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }

    .model-routing-section {
      margin-bottom: var(--spacing-md);
    }

    .model-routing-section h4 {
      margin-bottom: var(--spacing-sm);
      color: var(--vscode-foreground);
    }

    .model-pills {
      display: flex;
      flex-wrap: wrap;
      gap: var(--spacing-sm);
    }

    .model-pill {
      display: inline-block;
      padding: 2px 10px;
      border-radius: 12px;
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
      font-size: 0.85em;
    }

    .confidence-bars {
      display: flex;
      flex-direction: column;
      gap: var(--spacing-sm);
    }

    .confidence-bar {
      display: grid;
      grid-template-columns: 120px 1fr 40px;
      align-items: center;
      gap: var(--spacing-sm);
    }

    .confidence-label {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }

    .confidence-count {
      font-size: 0.85em;
      text-align: right;
    }

    .bar-track {
      height: 8px;
      background: color-mix(in srgb, var(--vscode-progressBar-background) 20%, transparent);
      border-radius: 4px;
      overflow: hidden;
    }

    .bar-fill {
      height: 100%;
      border-radius: 4px;
    }

    .bar-track { position: relative; }
    .bar-fill { position: absolute; top: 0; left: 0; }

    .bar-high { background: var(--vscode-terminal-ansiGreen); }
    .bar-medium { background: var(--vscode-terminal-ansiYellow); }
    .bar-low { background: var(--vscode-terminal-ansiRed); }

    .model-routing-table {
      width: 100%;
      border-collapse: collapse;
    }

    .model-routing-table th,
    .model-routing-table td {
      padding: var(--spacing-sm);
      text-align: left;
      border-bottom: 1px solid var(--vscode-panel-border);
    }

    .model-routing-table th {
      color: var(--vscode-descriptionForeground);
      font-weight: 600;
      font-size: 0.85em;
    }

    /* Cost Summary Widget (Issue #945) */
    .cost-summary-widget {
      padding: var(--spacing-sm) 0;
    }

    .cost-total-header {
      text-align: center;
      margin-bottom: var(--spacing-md);
    }

    .cost-total-value {
      font-size: 2.2em;
      font-weight: bold;
      color: var(--vscode-foreground);
    }

    .cost-total-label {
      color: var(--vscode-descriptionForeground);
      font-size: 0.9em;
      margin-top: var(--spacing-xs);
    }

    .cost-savings-section {
      margin-bottom: var(--spacing-md);
    }

    .savings-badge {
      display: inline-block;
      padding: 4px 12px;
      border-radius: 12px;
      background: var(--vscode-terminal-ansiGreen);
      color: var(--vscode-editor-background);
      font-size: 0.85em;
      font-weight: 600;
      margin-bottom: var(--spacing-sm);
    }

    .cost-comparison-bar {
      position: relative;
      height: 28px;
      background: var(--vscode-editor-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      overflow: hidden;
    }

    .cost-bar-actual {
      position: absolute;
      top: 0;
      left: 0;
      height: 100%;
      background: var(--vscode-terminal-ansiGreen);
      opacity: 0.3;
      display: flex;
      align-items: center;
      padding-left: 8px;
    }

    .cost-bar-actual span,
    .cost-bar-hypothetical span {
      font-size: 0.8em;
      white-space: nowrap;
      position: relative;
      z-index: 1;
    }

    .cost-bar-hypothetical {
      position: absolute;
      top: 0;
      right: 0;
      height: 100%;
      display: flex;
      align-items: center;
      padding-right: 8px;
    }

    .cost-bar-hypothetical span {
      color: var(--vscode-descriptionForeground);
    }

    .cost-stage-table {
      width: 100%;
      border-collapse: collapse;
      margin-bottom: var(--spacing-md);
    }

    .cost-stage-table th,
    .cost-stage-table td {
      padding: var(--spacing-sm);
      text-align: left;
      border-bottom: 1px solid var(--vscode-panel-border);
    }

    .cost-stage-table th {
      color: var(--vscode-descriptionForeground);
      font-weight: 600;
      font-size: 0.85em;
    }

    .cost-total-row td {
      border-top: 2px solid var(--vscode-panel-border);
      border-bottom: none;
    }

    .cost-trend-section {
      margin-top: var(--spacing-md);
    }

    .cost-trend-section h4 {
      margin-bottom: var(--spacing-sm);
      color: var(--vscode-foreground);
    }

    .cost-sparkline {
      width: 100%;
      max-width: 300px;
    }

    .sparkline-labels {
      display: flex;
      justify-content: space-between;
      max-width: 300px;
      font-size: 0.75em;
      color: var(--vscode-descriptionForeground);
    }

    /* Usage & Limits styles (Issue #1333) */
    .usage-limits-card {
      padding: var(--spacing-md);
      background: var(--vscode-editor-background);
      border: 1px solid var(--vscode-panel-border, rgba(128,128,128,0.2));
      border-radius: var(--border-radius);
    }

    .usage-limits-row {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: var(--spacing-sm);
    }

    .usage-limits-meta {
      margin-top: var(--spacing-xs);
    }

    .usage-label {
      font-weight: 500;
      color: var(--vscode-foreground);
    }

    .usage-value {
      font-size: 0.9em;
      color: var(--vscode-descriptionForeground);
    }

    .usage-remaining {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }

    .usage-progress-track {
      height: 8px;
      background: var(--vscode-progressBar-background, rgba(128,128,128,0.2));
      border-radius: 4px;
      overflow: hidden;
      margin-bottom: var(--spacing-xs);
    }

    .usage-progress-bar {
      height: 100%;
      border-radius: 4px;
      transition: width 0.3s ease;
    }

    .usage-bar-ok {
      background: var(--vscode-terminal-ansiGreen, #4ec9b0);
    }

    .usage-bar-warning {
      background: var(--vscode-editorWarning-foreground, #cca700);
    }

    .usage-bar-critical {
      background: var(--vscode-editorError-foreground, #f14c4c);
    }

    .usage-reset-hint {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
      margin-top: var(--spacing-sm);
    }

    .action-btn-sm {
      padding: 2px 8px;
      font-size: 0.8em;
    }

    /* Platform Quota Section (Issue #1479) */
    .platform-quota-card {
      background: var(--vscode-editor-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
    }

    .quota-metric-row {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: var(--spacing-xs);
    }

    .quota-stale-banner {
      background: var(--vscode-inputValidation-warningBackground, #6c4f1e);
      border: 1px solid var(--vscode-inputValidation-warningBorder, #b89500);
      border-radius: var(--border-radius);
      padding: var(--spacing-xs) var(--spacing-sm);
      font-size: 0.85em;
      margin-bottom: var(--spacing-sm);
    }

    .quota-community-card {
      display: flex;
      align-items: center;
      gap: var(--spacing-md);
      background: var(--vscode-editor-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
    }

    .quota-community-icon {
      font-size: 2em;
      color: var(--vscode-terminal-ansiGreen);
    }

    .quota-community-text {
      font-size: 1em;
      color: var(--vscode-foreground);
    }

    .quota-period-hint {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
      margin-top: var(--spacing-sm);
      margin-bottom: 0;
    }
  `;
}

// ---------------------------------------------------------------------------
// Platform Cost Tab — Issue #3317
// ---------------------------------------------------------------------------

const CHART_COLORS = [
  "var(--vscode-charts-blue)",
  "var(--vscode-charts-green)",
  "var(--vscode-charts-yellow)",
  "var(--vscode-charts-orange)",
  "var(--vscode-charts-red)",
  "var(--vscode-charts-purple)",
];

function formatCostUsd(costUsd: string): string {
  const n = parseFloat(costUsd);
  if (isNaN(n)) return "$0.0000";
  return "$" + n.toFixed(4);
}

function getPlatformCostEmptyStateHtml(): string {
  return `
    <div class="platform-cost-empty">
      <div class="platform-cost-empty-icon">📊</div>
      <p class="platform-cost-empty-title">Telemetry pending — opt in under settings</p>
      <p class="platform-cost-empty-hint">Enable telemetry to see server-aggregated cost data for your workspace.</p>
    </div>
  `;
}

function getPlatformCostDateRangeHtml(dateRange: CostDateRange): string {
  const ranges: CostDateRange[] = ["7d", "30d", "90d"];
  const labels: Record<CostDateRange, string> = {
    "7d": "7 Days",
    "30d": "30 Days",
    "90d": "90 Days",
  };
  return `
    <div class="platform-cost-date-range" role="group" aria-label="Cost date range">
      ${ranges
        .map(
          (r) =>
            `<button class="toggle-btn${r === dateRange ? " active" : ""}" data-cost-range="${r}">${labels[r]}</button>`
        )
        .join("")}
    </div>
  `;
}

function getPlatformCostTotalCardHtml(data: CostAnalyticsResult): string {
  return `
    <div class="platform-cost-total-card">
      <span class="platform-cost-total-label">Total Spend</span>
      <span class="platform-cost-total-value">${escapeHtml(formatCostUsd(data.totalCostUsd))}</span>
      <span class="platform-cost-total-tokens">${data.totalTokens.toLocaleString()} tokens</span>
    </div>
  `;
}

function getPlatformCostModelBreakdownHtml(data: CostAnalyticsResult): string {
  const byModel = data.breakdown.byModel;
  if (byModel.length === 0) {
    return `<p class="platform-cost-no-data">No model data available.</p>`;
  }

  const totalCost = byModel.reduce((sum, m) => sum + parseFloat(m.costUsd || "0"), 0);

  const rows = byModel
    .slice()
    .sort((a, b) => parseFloat(b.costUsd || "0") - parseFloat(a.costUsd || "0"))
    .map((m, i) => {
      const cost = parseFloat(m.costUsd || "0");
      const pct = totalCost > 0 ? (cost / totalCost) * 100 : 0;
      const color = CHART_COLORS[i % CHART_COLORS.length];
      return `
        <div class="platform-cost-model-row">
          <div class="platform-cost-model-bar-wrap">
            <div class="platform-cost-model-bar" style="width:${pct.toFixed(1)}%;background:${color}"></div>
          </div>
          <span class="platform-cost-model-name">${escapeHtml(m.modelId)}</span>
          <span class="platform-cost-model-cost">${escapeHtml(formatCostUsd(m.costUsd))}</span>
          <span class="platform-cost-model-tokens">${m.tokens.toLocaleString()} tok</span>
        </div>
      `;
    })
    .join("");

  return `<div class="platform-cost-model-list">${rows}</div>`;
}

function getPlatformCostSparklineHtml(data: CostAnalyticsResult, dateRange: CostDateRange): string {
  const byDay = data.breakdown.byDay;
  if (byDay.length < 2) {
    return `<p class="platform-cost-no-data">Not enough daily data for trend.</p>`;
  }

  const maxDays = dateRange === "7d" ? 7 : dateRange === "30d" ? 30 : 90;
  const entries = byDay.slice(-maxDays);
  const values = entries.map((d) => parseFloat(d.costUsd || "0"));
  const maxVal = Math.max(...values, 0.000001);

  const W = 400;
  const H = 60;
  const step = W / Math.max(values.length - 1, 1);

  const points = values
    .map((v, i) => {
      const x = (i * step).toFixed(1);
      const y = (H - (v / maxVal) * (H - 4)).toFixed(1);
      return `${x},${y}`;
    })
    .join(" ");

  return `
    <div class="platform-cost-sparkline-wrap">
      <svg viewBox="0 0 ${W} ${H}" xmlns="http://www.w3.org/2000/svg" aria-label="Daily cost trend" role="img">
        <polyline
          points="${escapeHtml(points)}"
          fill="none"
          stroke="var(--vscode-charts-blue)"
          stroke-width="2"
          stroke-linejoin="round"
          stroke-linecap="round"
        />
      </svg>
      <div class="platform-cost-sparkline-labels">
        <span>${entries.length > 0 ? escapeHtml(entries[0].date) : ""}</span>
        <span>${entries.length > 0 ? escapeHtml(entries[entries.length - 1].date) : ""}</span>
      </div>
    </div>
  `;
}

/**
 * Render the full platform cost tab panel HTML.
 * When data is null, renders an empty state with telemetry opt-in prompt.
 */
export function getCostTabHtml(
  data: CostAnalyticsResult | null,
  dateRange: CostDateRange = "7d"
): string {
  if (data === null) {
    return `
      <div class="platform-cost-tab">
        ${getPlatformCostDateRangeHtml(dateRange)}
        ${getPlatformCostEmptyStateHtml()}
      </div>
    `;
  }

  return `
    <div class="platform-cost-tab">
      ${getPlatformCostDateRangeHtml(dateRange)}
      ${getPlatformCostTotalCardHtml(data)}

      <div class="platform-cost-section">
        <h3 class="platform-cost-section-title">Cost by Model</h3>
        ${getPlatformCostModelBreakdownHtml(data)}
      </div>

      <div class="platform-cost-section">
        <h3 class="platform-cost-section-title">Daily Trend</h3>
        ${getPlatformCostSparklineHtml(data, dateRange)}
      </div>
    </div>
  `;
}

/**
 * JS event handlers for the platform cost tab.
 * Date range buttons post costDateRangeChange to the extension host.
 */
export function getCostTabScript(): string {
  return `
    (function() {
      var costPanel = document.getElementById('tab-panel-cost');
      if (!costPanel) return;
      costPanel.addEventListener('click', function(e) {
        var btn = e.target.closest('[data-cost-range]');
        if (!btn) return;
        var range = btn.getAttribute('data-cost-range');
        if (range !== '7d' && range !== '30d' && range !== '90d') return;
        costPanel.querySelectorAll('[data-cost-range]').forEach(function(b) {
          b.classList.toggle('active', b === btn);
        });
        vscode.postMessage({ type: 'costDateRangeChange', range: range });
      });
    })();
  `;
}

/** CSS for the platform cost tab. */
export function getPlatformCostTabStyles(): string {
  return `
    .platform-cost-tab {
      padding: var(--spacing-md, 12px) 0;
    }
    .platform-cost-date-range {
      display: flex;
      gap: 4px;
      margin-bottom: var(--spacing-md, 12px);
    }
    .platform-cost-total-card {
      display: flex;
      align-items: baseline;
      gap: var(--spacing-md, 12px);
      padding: var(--spacing-md, 12px) var(--spacing-lg, 16px);
      background: var(--vscode-editor-inactiveSelectionBackground);
      border-radius: var(--border-radius, 3px);
      margin-bottom: var(--spacing-md, 12px);
    }
    .platform-cost-total-label {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }
    .platform-cost-total-value {
      font-size: 1.5em;
      font-weight: 600;
      color: var(--vscode-foreground);
    }
    .platform-cost-total-tokens {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
    }
    .platform-cost-section {
      margin-bottom: var(--spacing-lg, 16px);
    }
    .platform-cost-section-title {
      font-size: 0.85em;
      font-weight: 600;
      color: var(--vscode-descriptionForeground);
      text-transform: uppercase;
      letter-spacing: 0.04em;
      margin: 0 0 var(--spacing-sm, 8px) 0;
    }
    .platform-cost-model-list {
      display: flex;
      flex-direction: column;
      gap: 6px;
    }
    .platform-cost-model-row {
      display: grid;
      grid-template-columns: 140px 1fr auto auto;
      align-items: center;
      gap: var(--spacing-sm, 8px);
      font-size: 0.85em;
    }
    .platform-cost-model-bar-wrap {
      height: 8px;
      background: var(--vscode-editor-inactiveSelectionBackground);
      border-radius: 4px;
      overflow: hidden;
    }
    .platform-cost-model-bar {
      height: 100%;
      border-radius: 4px;
      transition: width 0.3s ease;
    }
    .platform-cost-model-name {
      color: var(--vscode-foreground);
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    .platform-cost-model-cost {
      color: var(--vscode-foreground);
      font-variant-numeric: tabular-nums;
    }
    .platform-cost-model-tokens {
      color: var(--vscode-descriptionForeground);
      font-variant-numeric: tabular-nums;
    }
    .platform-cost-sparkline-wrap {
      display: flex;
      flex-direction: column;
      gap: 2px;
    }
    .platform-cost-sparkline-wrap svg {
      width: 100%;
      height: 60px;
    }
    .platform-cost-sparkline-labels {
      display: flex;
      justify-content: space-between;
      font-size: 0.75em;
      color: var(--vscode-descriptionForeground);
    }
    .platform-cost-empty {
      display: flex;
      flex-direction: column;
      align-items: center;
      padding: var(--spacing-xl, 32px) var(--spacing-md, 12px);
      gap: var(--spacing-sm, 8px);
      text-align: center;
    }
    .platform-cost-empty-icon { font-size: 2em; }
    .platform-cost-empty-title {
      color: var(--vscode-foreground);
      font-weight: 600;
      margin: 0;
    }
    .platform-cost-empty-hint {
      color: var(--vscode-descriptionForeground);
      font-size: 0.85em;
      margin: 0;
    }
    .platform-cost-no-data {
      color: var(--vscode-descriptionForeground);
      font-size: 0.85em;
    }
  `;
}
