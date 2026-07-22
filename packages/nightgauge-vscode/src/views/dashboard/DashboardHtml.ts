/**
 * DashboardHtml - Thin orchestrator for the dashboard WebView
 *
 * After #1542 refactor, this file imports tab-level renderers and composes the
 * full dashboard HTML. Function implementations live in tab modules under tabs/.
 * Tab navigation added by #1539. Analytics consolidation from #1541.
 *
 * @see docs/ARCHITECTURE.md for WebView patterns
 */

import * as vscode from "vscode";
import type {
  PipelineRunSummary,
  DashboardAggregates,
  TimeSavingsConfig,
  ModelRoutingMetrics,
  AdapterStatusData,
  HistoryPaginationInfo,
  PTCMetricsDisplayData,
  FirewallDashboardData,
  UsageLimitsData,
  PlatformQuotaData,
  AuditLogData,
} from "./DashboardState";
import type { ProjectBoardData } from "./ProjectBoardTypes";
import type { HealthWidgetData } from "./HealthWidgetTypes";
import type {
  CostSummary,
  CostHistoryEntry,
  PerModeCostRollup,
  BudgetVsActualStageStat,
} from "./CostSummaryCalculator";
import type { PerformanceMode } from "../../utils/modeProfiles";
import type { StallThresholdRow } from "./tabs/PerformanceTabHtml";
import type { PipelineCostEstimate } from "@nightgauge/sdk";
import type { HealthCheckReport } from "../../types/pipelineHealth";
import type { BacktrackRecord, ModelEscalationRecord } from "../../schemas/pipelineState";
import type { IssueCostAggregation } from "../../utils/executionHistoryReader";
import type { StageAverageMetrics, StageOutlier } from "./DashboardState";
import {
  getNonce,
  formatFullDateTime,
  formatRelativeTime,
  formatStageName,
  formatTokenCount,
  escapeHtml,
  getBaseStyles,
} from "./DashboardComponents";

// Tab module imports
import {
  getAdapterStatusWidgetHtml,
  getPipelineSlotsHtml,
  getSummaryCardsHtml,
  getScopeToggleHtml,
  getAdapterStatusStyles,
  getOverviewTabStyles,
} from "./tabs/OverviewTabHtml";
import type { PipelineSlotsViewData } from "./SlotCardTypes";
import {
  getToolCallsHtml,
  getHistoryHtml,
  getPipelineProgressSectionHtml,
  getPipelineTabStyles,
} from "./tabs/PipelineTabHtml";
import {
  getEpicEstimatesHtml,
  getCrossRepoEpicProgressHtml,
  getProjectBoardWidgetHtml,
  getEpicsTabStyles,
} from "./tabs/EpicsTabHtml";
import {
  getCostEstimateWidgetHtml,
  getModelRoutingWidgetHtml,
  getUsageLimitsSectionHtml,
  getPlatformQuotaSectionHtml,
  getCostSparklineSvg,
  getCostTabStyles,
  getPerModeCostRollupHtml,
  getCostCapWarningTableHtml,
  getBudgetVsActualPanelHtml,
  getCostTabHtml,
  getCostTabScript,
  getPlatformCostTabStyles,
  type CostCapWarningRow,
} from "./tabs/CostTabHtml";
import type { CostAnalyticsResult } from "../../services/IpcClientBase";
import type { CostDateRange } from "../../services/PlatformCostService";
import {
  getPerformanceMetricsSectionHtml,
  getPTCMetricsSectionHtml,
  getCostBySizeWidgetHtml,
  getCostPerIssueWidgetHtml,
  computeOutliers,
  getTokenTableStyles,
  getPerformanceTabStyles,
  getStallThresholdTableHtml,
} from "./tabs/PerformanceTabHtml";
import {
  getFirewallSectionHtml,
  getFirewallScript,
  getFirewallTabStyles,
} from "./tabs/FirewallTabHtml";
import {
  getHealthCheckReportHtml,
  getHealthWidgetHtml,
  getHealthWidgetStyles,
  getHealthWidgetScript,
  getHealthTabStyles,
  getHealthTabHtml,
  getHealthTabScript,
} from "./tabs/HealthTabHtml";
import { getAuditTabHtml, getAuditTabScript, getAuditTabStyles } from "./tabs/AuditTabHtml";
import { getRunsTabHtml, getRunsTabScript, getRunsTabStyles } from "./tabs/RunsTabHtml";
import type {
  RunsListData,
  TrendsData,
  ComplianceData,
  RetentionIntegrityData,
  AnalyticsHealthData,
} from "./DashboardState";
import { getTrendsTabHtml, getTrendsTabScript, getTrendsTabStyles } from "./tabs/TrendsTabHtml";
import {
  getComplianceTabHtml,
  getComplianceTabScript,
  getComplianceTabStyles,
} from "./tabs/ComplianceTabHtml";
import { getDiscoveryTabHtml } from "./tabs/DiscoveryTabHtml";
import { getDiscoveryTabStyles } from "./tabs/DiscoveryTabStyles";
import type { DiscoveryActivityData } from "../../services/DiscoveryActivityService";
import {
  getDependabotTabHtml,
  getDependabotTabScript,
  getDependabotTabStyles,
} from "./tabs/DependabotTabHtml";
import type { DependabotPRData } from "../../services/DependabotPRService";

// Re-exports for backward compatibility
export type {
  AdapterStatusData,
  HistoryPaginationInfo,
  PTCMetricsDisplayData,
  FirewallDashboardData,
  UsageLimitsData,
  PlatformQuotaData,
} from "./DashboardState";
export { getFeedbackEventsHtml } from "./tabs/PipelineTabHtml";
export { getToolCallsHtml } from "./tabs/PipelineTabHtml";
export { getPipelineProgressSectionHtml } from "./tabs/PipelineTabHtml";
export { getSummaryCardsSectionHtml } from "./tabs/OverviewTabHtml";
export { getPipelineSlotsSectionHtml } from "./tabs/OverviewTabHtml";
export { getCostBySizeWidgetHtml, getCostPerIssueWidgetHtml } from "./tabs/PerformanceTabHtml";
export { getEpicEstimatesHtml } from "./tabs/EpicsTabHtml";

// ---------------------------------------------------------------------------
// Mode-filter chip + mismatch advisory (Issue #3218)
// ---------------------------------------------------------------------------

/** Mode-filter chip selection — `"all"` keeps the prior unfiltered behavior. */
export type ModeFilterValue = PerformanceMode | "all";

/**
 * Data the dashboard header passes to the mode-mismatch advisory renderer
 * (Issue #3218). `null` is rendered as an empty string by `getModeMismatchAdvisoryHtml`.
 */
export interface ModeMismatchAdvisoryData {
  activeMode: PerformanceMode;
  dominantMode: PerformanceMode;
  dominantCount: number;
  windowSize: number;
}

const MODE_LABELS: Record<PerformanceMode, string> = {
  efficiency: "Efficiency",
  elevated: "Elevated",
  maximum: "Maximum",
  frontier: "Frontier",
};

/**
 * Render the mode-filter chip group. Mirrors the existing `.scope-toggle`
 * pattern — same `.toggle-btn.active` styling. Click handler is wired in
 * `getScript()` against `[data-mode]`.
 */
export function getModeFilterToggleHtml(currentMode: ModeFilterValue): string {
  const buttons: { value: ModeFilterValue; label: string }[] = [
    { value: "all", label: "All Modes" },
    { value: "efficiency", label: "Efficiency" },
    { value: "elevated", label: "Elevated" },
    { value: "maximum", label: "Maximum" },
  ];
  return `
    <div class="mode-toggle scope-toggle" role="group" aria-label="Performance mode filter">
      ${buttons
        .map(
          (b) =>
            `<button class="toggle-btn ${
              currentMode === b.value ? "active" : ""
            }" data-mode="${b.value}">${b.label}</button>`
        )
        .join("")}
    </div>
  `;
}

/**
 * Render the mode-mismatch advisory banner (Issue #3218). Hidden when
 * `data` is null. Copy is locked to AC4: `<N> of your last <window> runs
 * used <X> mode; current mode is <Y> — comparing trends?`.
 */
export function getModeMismatchAdvisoryHtml(data: ModeMismatchAdvisoryData | null): string {
  if (!data) return "";
  const dominant = MODE_LABELS[data.dominantMode];
  const active = MODE_LABELS[data.activeMode];
  return `
    <div class="mode-mismatch-advisory" role="status">
      <span class="mode-mismatch-icon" aria-hidden="true">⚠</span>
      <span class="mode-mismatch-text">${data.dominantCount} of your last ${data.windowSize} runs used ${escapeHtml(
        dominant
      )} mode; current mode is ${escapeHtml(active)} — comparing trends?</span>
    </div>
  `;
}

// ---------------------------------------------------------------------------
// Style composition
// ---------------------------------------------------------------------------

/**
 * Compose all CSS styles for the dashboard from base + tab modules.
 *
 * Replaces the former 2,400-line monolithic getStyles() with a thin composer
 * that concatenates getBaseStyles() + each tab's getTabStyles() + auxiliary
 * widget styles (health widget, token table, adapter status).
 */
function getStyles(): string {
  return [
    getBaseStyles(),
    getOverviewTabStyles(),
    getAdapterStatusStyles(),
    getPipelineTabStyles(),
    getPerformanceTabStyles(),
    getTokenTableStyles(),
    getCostTabStyles(),
    getEpicsTabStyles(),
    getFirewallTabStyles(),
    getHealthTabStyles(),
    getHealthWidgetStyles(),
    getAuditTabStyles(),
    getRunsTabStyles(),
    getTrendsTabStyles(),
    getDiscoveryTabStyles(),
    getPlatformCostTabStyles(),
    getComplianceTabStyles(),
    getDependabotTabStyles(),
    getTabCss(),
  ]
    .map((css) => css.trim())
    .join("\n");
}

// ---------------------------------------------------------------------------
// Tab navigation (Issue #1539)
// ---------------------------------------------------------------------------

const VALID_TABS = [
  "overview",
  "pipeline",
  "analytics",
  "history",
  "epics",
  "audit",
  "discovery",
  "cost",
  "health",
  "runs",
  "trends",
  "compliance",
  "dependencies",
] as const;
type TabId = (typeof VALID_TABS)[number];

function getTabBarHtml(activeTab: string): string {
  const tabs: { id: TabId; label: string }[] = [
    { id: "overview", label: "Overview" },
    { id: "pipeline", label: "Pipeline" },
    { id: "analytics", label: "Analytics" },
    { id: "history", label: "History" },
    { id: "epics", label: "Epics" },
    { id: "audit", label: "Audit Trail" },
    { id: "discovery", label: "Discovery" },
    { id: "cost", label: "Cost" },
    { id: "health", label: "Health" },
    { id: "runs", label: "Runs" },
    { id: "trends", label: "Trends" },
    { id: "compliance", label: "Compliance" },
    { id: "dependencies", label: "Dependencies" },
  ];
  return `<div class="tab-bar" role="tablist" aria-label="Dashboard sections">${tabs
    .map((t) => {
      const isActive = t.id === activeTab;
      return `<button class="tab-btn${isActive ? " active" : ""}" role="tab" aria-selected="${isActive}" aria-controls="tab-panel-${t.id}" id="tab-${t.id}" data-tab="${t.id}" tabindex="${isActive ? 0 : -1}">${t.label}</button>`;
    })
    .join("")}</div>`;
}

function getTabCss(): string {
  return `
    .tab-bar {
      display: flex;
      flex-wrap: wrap;
      border-bottom: 1px solid var(--vscode-panel-border);
      margin-bottom: 12px;
      gap: 0;
      position: sticky;
      top: 0;
      background: var(--vscode-editor-background);
      z-index: 10;
      padding-top: 4px;
    }
    .tab-btn {
      background: none;
      border: none;
      border-bottom: 2px solid transparent;
      color: var(--vscode-foreground);
      cursor: pointer;
      font-family: var(--vscode-font-family);
      font-size: var(--vscode-font-size);
      opacity: 0.7;
      padding: 6px 16px;
      transition: opacity 0.1s;
    }
    .tab-btn:hover { opacity: 1; }
    .tab-btn.active {
      border-bottom-color: var(--vscode-progressBar-background);
      color: var(--vscode-foreground);
      opacity: 1;
    }
    .tab-btn:focus-visible {
      outline: 1px solid var(--vscode-focusBorder);
      outline-offset: -1px;
    }
    .tab-panel { display: none; }
    .tab-panel.active {
      display: block;
    }
    .mode-mismatch-advisory {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm, 8px);
      padding: 6px 12px;
      margin: 4px 0 8px 0;
      background: var(--vscode-editorWarning-background, var(--vscode-inputValidation-warningBackground));
      border: 1px solid var(--vscode-editorWarning-foreground);
      border-radius: var(--border-radius, 3px);
      color: var(--vscode-editorWarning-foreground);
      font-size: 0.85em;
    }
    .mode-mismatch-icon {
      font-size: 1em;
    }
    .mode-toggle {
      margin-left: 4px;
    }
  `;
}

function getTabScript(): string {
  return `
    function activateTab(tabId) {
      document.querySelectorAll('.tab-btn').forEach(function(btn) {
        var isActive = btn.dataset.tab === tabId;
        btn.classList.toggle('active', isActive);
        btn.setAttribute('aria-selected', String(isActive));
        btn.tabIndex = isActive ? 0 : -1;
      });
      document.querySelectorAll('.tab-panel').forEach(function(panel) {
        panel.classList.toggle('active', panel.id === 'tab-panel-' + tabId);
      });
    }

    (function() {
      var savedTab = (vscode.getState() && vscode.getState().activeTab) || 'overview';
      activateTab(savedTab);
      // Notify backend so lazy-loaded tabs (audit, discovery) fetch their data
      // on restore. Without this, the tab panel is visible but data never loads
      // (Issue #2582).
      vscode.postMessage({ type: 'selectTab', tab: savedTab });

      document.querySelectorAll('.tab-btn').forEach(function(btn, _i, buttons) {
        btn.addEventListener('click', function() {
          var tabId = btn.dataset.tab;
          activateTab(tabId);
          var prevState = vscode.getState() || {};
          vscode.setState(Object.assign({}, prevState, { activeTab: tabId }));
          vscode.postMessage({ type: 'selectTab', tab: tabId });
        });
        btn.addEventListener('contextmenu', function(e) {
          e.preventDefault();
          vscode.postMessage({ type: 'openInBrowser', tab: btn.dataset.tab });
        });
        btn.addEventListener('keydown', function(e) {
          var btns = Array.from(buttons);
          var idx = btns.indexOf(btn);
          if (e.key === 'ArrowRight') {
            btns[(idx + 1) % btns.length].focus();
            e.preventDefault();
          } else if (e.key === 'ArrowLeft') {
            btns[(idx - 1 + btns.length) % btns.length].focus();
            e.preventDefault();
          } else if (e.key === 'Home') {
            btns[0].focus();
            e.preventDefault();
          } else if (e.key === 'End') {
            btns[btns.length - 1].focus();
            e.preventDefault();
          } else if (e.key === 'Enter' || e.key === ' ') {
            btn.click();
            e.preventDefault();
          }
        });
      });
    })();
  `;
}

// ---------------------------------------------------------------------------
// Analytics section (Issue #1541) — consolidates Cost + Performance + PTC
// ---------------------------------------------------------------------------

/**
 * Generate unified Analytics section HTML (Issue #1541).
 *
 * Consolidates Cost Summary, Performance Metrics, and PTC Metrics into a
 * single collapsible section with sub-sections. Cost Estimate moves
 * to the Pipeline Run section separately.
 */
export function getAnalyticsSectionHtml(
  costSummary: CostSummary | null,
  costHistory: CostHistoryEntry[],
  displayRun: PipelineRunSummary | null,
  timeSavingsConfig: TimeSavingsConfig,
  stageAverages: StageAverageMetrics[],
  history: PipelineRunSummary[],
  costPerIssue: IssueCostAggregation[],
  ptcMetrics: PTCMetricsDisplayData | null,
  // Issue #3218 — per-mode rollup, mode filter, and stall threshold rows
  perModeRollup: PerModeCostRollup | null = null,
  modeFilter: ModeFilterValue = "all",
  stallThresholdRows: StallThresholdRow[] = [],
  // Issue #3276 — cost cap tightness warning rows
  costCapWarningRows: CostCapWarningRow[] = [],
  // Issue #3269 — budget vs actual stats
  budgetVsActualStats: BudgetVsActualStageStat[] = []
): string {
  const outliers = displayRun ? computeOutliers(displayRun, stageAverages) : [];

  return `
    <details class="collapsible-section" open>
      <summary class="section-toggle">
        <span class="toggle-icon">▼</span>
        <h3>Analytics</h3>
        ${costSummary ? `<span class="section-badge">$${costSummary.totalCostUsd.toFixed(4)}</span>` : ""}
      </summary>
      <div class="section-content">
        <div id="section-analytics">
          ${getAnalyticsCostSubsectionHtml(costSummary, costHistory, costPerIssue, perModeRollup, modeFilter, costCapWarningRows, budgetVsActualStats)}
          ${getAnalyticsPerformanceSubsectionHtml(displayRun, timeSavingsConfig, stageAverages, outliers, history, stallThresholdRows)}
          ${ptcMetrics && ptcMetrics.programmaticCalls > 0 ? getPTCMetricsSectionHtml(ptcMetrics) : ""}
        </div>
      </div>
    </details>`;
}

function getAnalyticsCostSubsectionHtml(
  costSummary: CostSummary | null,
  costHistory: CostHistoryEntry[],
  costPerIssue: IssueCostAggregation[],
  perModeRollup: PerModeCostRollup | null = null,
  modeFilter: ModeFilterValue = "all",
  costCapWarningRows: CostCapWarningRow[] = [],
  budgetVsActualStats: BudgetVsActualStageStat[] = []
): string {
  if (!costSummary) return "";

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

  const sparklineHtml = costHistory.length >= 2 ? getCostSparklineSvg(costHistory) : "";

  return `
    <details class="collapsible-section" open>
      <summary class="section-toggle">
        <span class="toggle-icon">▼</span>
        <h4>Cost Analysis</h4>
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
        ${getPerModeCostRollupHtml(perModeRollup, modeFilter)}
        ${getCostCapWarningTableHtml(costCapWarningRows)}
        ${getBudgetVsActualPanelHtml(budgetVsActualStats)}
        ${getCostBySizeWidgetHtml(costPerIssue)}
        ${getCostPerIssueWidgetHtml(costPerIssue)}
      </div>
    </details>`;
}

function getAnalyticsPerformanceSubsectionHtml(
  displayRun: PipelineRunSummary | null,
  timeSavingsConfig: TimeSavingsConfig,
  stageAverages: StageAverageMetrics[],
  outliers: StageOutlier[],
  history: PipelineRunSummary[],
  stallThresholdRows: StallThresholdRow[] = []
): string {
  return `
    <details class="collapsible-section" open>
      <summary class="section-toggle">
        <span class="toggle-icon">▼</span>
        <h4>Performance</h4>
      </summary>
      <div class="section-content" id="section-performance-metrics">
        ${getPerformanceMetricsSectionHtml(displayRun, timeSavingsConfig, stageAverages, outliers, history)}
        ${getStallThresholdTableHtml(stallThresholdRows)}
      </div>
    </details>`;
}

// ---------------------------------------------------------------------------
// Dashboard script (event handlers, incremental updates)
// ---------------------------------------------------------------------------

function getScript(): string {
  return `
    (function() {
      // Reset Usage Counter button (Issue #1333)
      document.getElementById('resetUsageCounterBtn')?.addEventListener('click', function() {
        vscode.postMessage({ type: 'resetUsageCounter' });
      });

      // Refresh button with animation
      document.getElementById('refreshBtn')?.addEventListener('click', function() {
        const btn = this;
        const icon = document.getElementById('refreshIcon');
        const lastUpdated = document.getElementById('lastUpdated');

        // Start animation
        icon.classList.add('spinning');
        btn.classList.add('refreshing');
        btn.disabled = true;
        lastUpdated.classList.add('refreshing');
        lastUpdated.textContent = 'Refreshing...';

        // Send refresh message
        vscode.postMessage({ type: 'refresh' });

        // Stop animation after 2 seconds (data should be updated by then)
        setTimeout(() => {
          icon.classList.remove('spinning');
          btn.classList.remove('refreshing');
          btn.disabled = false;
          lastUpdated.classList.remove('refreshing');
          const d = new Date();
          const yr = d.getUTCFullYear();
          const mo = String(d.getUTCMonth() + 1).padStart(2, '0');
          const dy = String(d.getUTCDate()).padStart(2, '0');
          const hh = String(d.getUTCHours()).padStart(2, '0');
          const mm = String(d.getUTCMinutes()).padStart(2, '0');
          const ss = String(d.getUTCSeconds()).padStart(2, '0');
          const now = yr + '-' + mo + '-' + dy + ' ' + hh + ':' + mm + ':' + ss + ' UTC';
          lastUpdated.textContent = \`Last updated: \${now}\`;
        }, 2000);
      });

      // Export buttons
      document.getElementById('exportJson')?.addEventListener('click', () => {
        vscode.postMessage({ type: 'export', format: 'json', target: 'current' });
      });

      document.getElementById('exportCsv')?.addEventListener('click', () => {
        vscode.postMessage({ type: 'export', format: 'csv', target: 'current' });
      });

      // Export Analytics buttons (Issue #1010)
      document.getElementById('exportAnalyticsJsonAll')?.addEventListener('click', () => {
        vscode.postMessage({ type: 'exportAnalytics', format: 'json', dateRange: 'all' });
      });
      document.getElementById('exportAnalyticsCsvRuns30')?.addEventListener('click', () => {
        vscode.postMessage({ type: 'exportAnalytics', format: 'csv-runs', dateRange: 'last30' });
      });
      document.getElementById('exportAnalyticsCsvStages30')?.addEventListener('click', () => {
        vscode.postMessage({ type: 'exportAnalytics', format: 'csv-stages', dateRange: 'last30' });
      });
      document.getElementById('exportAnalyticsCustom')?.addEventListener('click', () => {
        // Trigger the full command palette command for custom date range + format options
        vscode.postMessage({ type: 'executeCommand', command: 'nightgauge.exportTelemetry' });
      });

      // Scope + mode toggle buttons (Issue #3218 added mode-toggle)
      document.querySelectorAll('.toggle-btn').forEach(btn => {
        btn.addEventListener('click', (e) => {
          const target = e.target;
          if (target.dataset.scope) {
            vscode.postMessage({ type: 'setScope', scope: target.dataset.scope });
          } else if (target.dataset.mode) {
            vscode.postMessage({ type: 'setModeFilter', mode: target.dataset.mode });
          }
        });
      });

      // Tool filter
      document.getElementById('toolFilter')?.addEventListener('change', (e) => {
        const filter = e.target.value;
        document.querySelectorAll('.tool-call-item').forEach(item => {
          if (filter === 'all' || item.dataset.tool === filter) {
            item.style.display = '';
          } else {
            item.style.display = 'none';
          }
        });
      });

      // Project board refresh button (Issue #134)
      document.getElementById('refreshProjectBoard')?.addEventListener('click', function() {
        const btn = this;
        btn.classList.add('spinning');
        btn.disabled = true;
        vscode.postMessage({ type: 'refreshProjectBoard' });
        // Animation stops when page re-renders with new data
        setTimeout(() => {
          btn.classList.remove('spinning');
          btn.disabled = false;
        }, 2000);
      });

      // Allowlist of valid section IDs for incremental updates (Issue #923)
      const VALID_SECTIONS = new Set(['pipeline-progress', 'summary-cards', 'analytics', 'tool-calls', 'pipeline-slots']);

      // Slot card click → open per-slot output channel. Delegated so it
      // survives section re-renders without re-binding.
      document.addEventListener('click', function(event) {
        const card = event.target.closest && event.target.closest('.slot-card');
        if (!card) return;
        const slotIndex = Number(card.getAttribute('data-slot-index'));
        if (Number.isFinite(slotIndex)) {
          vscode.postMessage({ type: 'openSlotOutput', slotIndex: slotIndex });
        }
      });

      // Debounce accumulator: coalesces rapid incremental updates per section (Issue #1244)
      var _pendingIncrementalUpdates = {};
      var _incrementalUpdateTimer = null;
      function _flushIncrementalUpdates() {
        Object.keys(_pendingIncrementalUpdates).forEach(function(section) {
          var el = document.getElementById('section-' + section);
          if (el) {
            el.innerHTML = _pendingIncrementalUpdates[section];
          }
        });
        _pendingIncrementalUpdates = {};
      }

      // Handle messages from extension (incremental updates + full render fallback)
      window.addEventListener('message', event => {
        const message = event.data;
        if (message.type === 'incrementalUpdate' && VALID_SECTIONS.has(message.section) && typeof message.html === 'string') {
          // Debounce: accumulate rapid updates, flush after 50 ms of silence
          _pendingIncrementalUpdates[message.section] = message.html;
          clearTimeout(_incrementalUpdateTimer);
          _incrementalUpdateTimer = setTimeout(_flushIncrementalUpdates, 50);
        } else if (message.type === 'metricsRefreshing') {
          // Toggle a subtle refreshing indicator on widget containers (Issue #998)
          const widgets = document.querySelectorAll('.chart-container, .health-widget, .metric-card');
          widgets.forEach(el => {
            if (message.active) {
              el.classList.add('refreshing');
            } else {
              el.classList.remove('refreshing');
            }
          });
        } else if (message.type === 'restoreScrollPosition' && typeof message.scrollY === 'number') {
          // Restore scroll position after a full re-render
          window.scrollTo(0, message.scrollY);
        } else if (message.type === 'requestScrollPosition') {
          // Extension is about to do a full re-render — report current scroll position
          vscode.postMessage({ type: 'scrollPosition', scrollY: window.scrollY });
        } else if (message.type === 'update') {
          location.reload();
        } else if (message.type === 'runDetailLiveUpdate') {
          // Live stage-status update from pipeline SSE events (#3714)
          var issueNum = message.issueNumber;
          var update = message.update || {};
          var detailPanel = document.querySelector('[data-run-detail-issue="' + issueNum + '"]');
          if (!detailPanel) return;
          if (update.allComplete) {
            detailPanel.querySelectorAll('[data-stage-status="running"]').forEach(function(row) {
              row.setAttribute('data-stage-status', 'completed');
            });
            return;
          }
          if (!update.stage) return;
          var stageRow = detailPanel.querySelector('[data-stage-name="' + update.stage + '"]');
          if (!stageRow) return;
          if (update.status) {
            stageRow.setAttribute('data-stage-status', update.status);
          }
          if (typeof update.durationMs === 'number') {
            var durationCell = stageRow.querySelector('[data-stage-duration]');
            if (durationCell) {
              var ms = update.durationMs;
              durationCell.textContent = ms < 1000 ? ms + 'ms' : (ms / 1000).toFixed(1) + 's';
            }
          }
        }
      });
    })();

    // Escape HTML in JS context (for lazy-rendered content)
    function escapeHtmlJs(text) {
      const div = document.createElement('div');
      div.appendChild(document.createTextNode(text));
      return div.innerHTML;
    }

    // Toggle tool call details (lazy rendering)
    function handleToggleToolCall(index) {
      const details = document.getElementById('details-' + index);
      const chevron = document.getElementById('chevron-' + index);
      if (!details || !chevron) return;
      if (details.style.display === 'none') {
        // Lazily populate content on first expansion
        if (!details.dataset.populated) {
          const item = details.closest('.tool-call-item');
          let html = '';
          if (item.dataset.args) {
            try {
              const formatted = JSON.stringify(JSON.parse(item.dataset.args), null, 2);
              html += '<div class="tool-call-args"><strong>Args:</strong> <code>' + escapeHtmlJs(formatted) + '</code></div>';
            } catch (_e) {
              html += '<div class="tool-call-args"><strong>Args:</strong> <code>' + escapeHtmlJs(item.dataset.args) + '</code></div>';
            }
          }
          if (item.dataset.result) {
            html += '<div class="tool-call-result"><strong>Result:</strong> <pre>' + escapeHtmlJs(item.dataset.result) + '</pre></div>';
          }
          if (item.dataset.error) {
            html += '<div class="tool-call-error"><strong>Error:</strong> ' + escapeHtmlJs(item.dataset.error) + '</div>';
          }
          if (item.dataset.duration) {
            html += '<div class="tool-call-duration">' + item.dataset.duration + 'ms</div>';
          }
          details.innerHTML = html;
          details.dataset.populated = 'true';
        }
        details.style.display = 'block';
        chevron.classList.add('expanded');
      } else {
        details.style.display = 'none';
        chevron.classList.remove('expanded');
      }
    }

    // Pipeline tab event delegation — handles toggle-tool-call clicks in the pipeline panel
    (function() {
      const pipelinePanel = document.getElementById('tab-panel-pipeline');
      if (pipelinePanel) {
        pipelinePanel.addEventListener('click', function(e) {
          const target = e.target.closest('[data-action]');
          if (!target) return;

          const action = target.dataset.action;
          if (action === 'toggle-tool-call') {
            handleToggleToolCall(parseInt(target.dataset.index, 10));
          }
        });

        // Auto-load tool calls for the most recent historical run (Issue #1842)
        const autoLoadEl = pipelinePanel.querySelector('[data-auto-load-issue]');
        if (autoLoadEl) {
          const issueNumber = parseInt(autoLoadEl.dataset.autoLoadIssue, 10);
          if (!isNaN(issueNumber)) {
            const container = document.getElementById('tool-calls-load-container');
            if (container) {
              container.innerHTML = '<p class="tool-calls-loading">Loading tool calls...</p>';
            }
            vscode.postMessage({ type: 'loadRunDetails', issueNumber: issueNumber });
          }
        }
      }
    })();

    // History tab event delegation — handles history/tool-call actions in the history panel
    (function() {
      const historyPanel = document.getElementById('tab-panel-history');
      if (historyPanel) {
        historyPanel.addEventListener('click', function(e) {
          const target = e.target.closest('[data-action]');
          if (!target) return;

          const action = target.dataset.action;
          if (action === 'select-history-run') {
            const row = target.closest('[data-issue]');
            if (row) {
              vscode.postMessage({ type: 'selectRun', issueNumber: parseInt(row.dataset.issue, 10) });
            }
          } else if (action === 'load-tool-calls') {
            const container = document.getElementById('tool-calls-load-container');
            if (container) {
              container.innerHTML = '<p class="tool-calls-loading">Loading tool calls...</p>';
            }
            vscode.postMessage({ type: 'loadRunDetails', issueNumber: parseInt(target.dataset.issue, 10) });
          } else if (action === 'load-more-history') {
            vscode.postMessage({ type: 'loadMoreHistory' });
          }
        });
      }
    })();
  `;
}

// ---------------------------------------------------------------------------
// Main HTML composer
// ---------------------------------------------------------------------------

export function getDashboardHtml(
  webview: vscode.Webview,
  currentRun: PipelineRunSummary | null,
  history: PipelineRunSummary[],
  aggregates: DashboardAggregates,
  timeSavingsConfig: TimeSavingsConfig,
  scope: "session" | "all" = "all",
  firewallData?: FirewallDashboardData,
  projectBoardData?: ProjectBoardData | null,
  healthWidgetData?: HealthWidgetData | null,
  modelRoutingMetrics?: ModelRoutingMetrics | null,
  appliedCategories?: string[],
  costSummary?: CostSummary | null,
  costHistory?: CostHistoryEntry[],
  costEstimate?: PipelineCostEstimate | null,
  historyPagination?: HistoryPaginationInfo,
  ptcMetrics?: PTCMetricsDisplayData | null,
  adapterStatusData?: AdapterStatusData | null,
  healthCheckReport?: HealthCheckReport | null,
  backtracks?: BacktrackRecord[],
  modelEscalations?: ModelEscalationRecord[],
  usageLimitsData?: UsageLimitsData | null,
  activeTab: string = "overview",
  platformQuotaData?: PlatformQuotaData | null,
  auditLogData?: AuditLogData | null,
  discoveryActivityData?: DiscoveryActivityData | null,
  pipelineSlotsView?: PipelineSlotsViewData | null,
  // Issue #3218 — mode-aware dashboard surfacing.
  modeFilter: ModeFilterValue = "all",
  perModeRollup?: PerModeCostRollup | null,
  stallThresholdRows?: StallThresholdRow[],
  modeMismatchAdvisory?: ModeMismatchAdvisoryData | null,
  // Issue #3276 — cost cap tightness warning rows
  costCapWarningRows?: CostCapWarningRow[],
  // Issue #3269 — budget vs actual stats
  budgetVsActualStats?: BudgetVsActualStageStat[],
  // Issue #3317 — platform cost analytics tab
  platformCostData?: CostAnalyticsResult | null,
  costDateRange: CostDateRange = "7d",
  // Issue #3318 — platform analytics health tab
  healthAnalyticsData?: AnalyticsHealthData | null,
  healthFetchedAt?: Date | null,
  // Issue #3319 — pipeline runs history tab
  runsData?: RunsListData | null,
  // Issue #3320 — longitudinal trends tab
  trendsData?: TrendsData | null,
  // Issue #3322 — compliance report generation tab
  complianceData?: ComplianceData | null,
  // Issue #3323 — audit retention & integrity panel
  retentionIntegrityData?: RetentionIntegrityData | null,
  // Issue #3116 — dependabot PR dependencies tab
  dependabotData?: DependabotPRData | null
): string {
  const nonce = getNonce();
  const renderTs = Date.now();

  // Get run to display (current or most recent from history)
  const displayRun = currentRun || (history.length > 0 ? history[0] : null);

  // Generate health widget HTML (sparkline Chart.js charts removed)
  let healthWidgetHtml = "";
  if (healthWidgetData) {
    healthWidgetHtml = getHealthWidgetHtml(healthWidgetData, false, appliedCategories ?? []);
  }

  const lastUpdated = formatFullDateTime(new Date());

  return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}';">
  <title>Nightgauge Dashboard</title>
  <style>
    ${getStyles()}
  </style>
</head>
<body>
  <div class="dashboard">
    <header class="dashboard-header">
      <div class="header-title">
        <h1>Nightgauge Dashboard</h1>
        <span class="last-updated" id="lastUpdated">Last updated: ${lastUpdated}</span>
      </div>
      <div class="header-actions">
        ${getScopeToggleHtml(scope)}
        ${getModeFilterToggleHtml(modeFilter)}
        <button class="action-btn primary" id="refreshBtn" title="Refresh Dashboard">
          <span id="refreshIcon">&#8635;</span> Refresh
        </button>
        <div class="dropdown">
          <button class="action-btn">Export &#9662;</button>
          <div class="dropdown-content">
            <button class="dropdown-item" id="exportJson">Dashboard JSON</button>
            <button class="dropdown-item" id="exportCsv">Dashboard CSV</button>
            <div class="dropdown-divider"></div>
            <button class="dropdown-item" id="exportAnalyticsJsonAll">Analytics JSON (All Time)</button>
            <button class="dropdown-item" id="exportAnalyticsCsvRuns30">Analytics CSV — Runs (30 Days)</button>
            <button class="dropdown-item" id="exportAnalyticsCsvStages30">Analytics CSV — Stages (30 Days)</button>
            <button class="dropdown-item" id="exportAnalyticsCustom">Custom Export...</button>
          </div>
        </div>
      </div>
    </header>

    ${getModeMismatchAdvisoryHtml(modeMismatchAdvisory ?? null)}

    ${getTabBarHtml(activeTab)}

    <div class="tab-panel${activeTab === "overview" ? " active" : ""}" id="tab-panel-overview" role="tabpanel" aria-labelledby="tab-overview">
      <!-- Adapter Status Bar (Issue #1056) -->
      ${getAdapterStatusWidgetHtml(adapterStatusData ?? null)}

      <!-- Pipeline Slots — live cards for each running slot + queued issues -->
      <div id="section-pipeline-slots">
      ${getPipelineSlotsHtml(pipelineSlotsView ?? null)}
      </div>

      <!-- PRIORITY 0: Pipeline Health Widget (Issue #655) -->
      ${healthWidgetHtml}

      <!-- PRIORITY 0.5: Pipeline Health Check Report (Issue #1104) -->
      ${getHealthCheckReportHtml(healthCheckReport)}

      <!-- PRIORITY 1: Overall Statistics -->
      <div id="section-summary-cards">
      ${getSummaryCardsHtml(aggregates, scope)}
      </div>

      <!-- PRIORITY 2.6: Project Board Summary (Issue #134) -->
      ${getProjectBoardWidgetHtml(projectBoardData ?? null)}

      <!-- PRIORITY 2.7: Model Routing Summary (Issue #734) -->
      ${getModelRoutingWidgetHtml(modelRoutingMetrics ?? null)}

      <!-- PRIORITY 2.8: Platform Quota (Issue #1479) -->
      ${getPlatformQuotaSectionHtml(platformQuotaData ?? null)}

      <!-- PRIORITY 2.9: Usage & Limits (Issue #1333) -->
      ${getUsageLimitsSectionHtml(usageLimitsData ?? null)}
    </div>

    <div class="tab-panel${activeTab === "pipeline" ? " active" : ""}" id="tab-panel-pipeline" role="tabpanel" aria-labelledby="tab-pipeline">
      <!-- PRIORITY 3: Current or most recent run -->
      ${
        displayRun
          ? `<details class="collapsible-section" open>
        <summary class="section-toggle">
          <span class="toggle-icon">▼</span>
          <h3>${
            currentRun
              ? "Current Pipeline Run"
              : `Most Recent Pipeline Run${displayRun.completedAt ? ` — Completed ${formatRelativeTime(displayRun.completedAt)}` : ""}`
          }</h3>
        </summary>
        <div class="section-content">
          <div id="section-pipeline-progress">
          ${getPipelineProgressSectionHtml(
            displayRun,
            currentRun ? (backtracks ?? []) : [],
            currentRun ? (modelEscalations ?? []) : []
          )}
          </div>
          ${currentRun ? getCostEstimateWidgetHtml(costEstimate ?? null) : ""}
        </div>
      </details>`
          : '<div class="empty-state"><p>No pipeline runs yet. Run a pipeline to see progress here.</p></div>'
      }

      <!-- Tool Calls (Collapsible) -->
      <details class="collapsible-section">
        <summary class="section-toggle">
          <span class="toggle-icon">▼</span>
          <h3>Tool Call Log</h3>
        </summary>
        <div class="section-content" id="section-tool-calls">
          ${getToolCallsHtml(
            displayRun?.toolCalls || [],
            displayRun && !currentRun ? displayRun.issueNumber : undefined,
            displayRun && !currentRun ? true : false
          )}
        </div>
      </details>
    </div>

    <div class="tab-panel${activeTab === "analytics" ? " active" : ""}" id="tab-panel-analytics" role="tabpanel" aria-labelledby="tab-analytics">
      <!-- PRIORITY 4: Analytics (Issue #1541) — consolidates Cost Summary, Performance, PTC -->
      ${getAnalyticsSectionHtml(costSummary ?? null, costHistory ?? [], displayRun, timeSavingsConfig, aggregates.stageAverages, history, aggregates.costPerIssue ?? [], ptcMetrics ?? null, perModeRollup ?? null, modeFilter, stallThresholdRows ?? [], costCapWarningRows ?? [], budgetVsActualStats ?? [])}
    </div>

    <div class="tab-panel${activeTab === "history" ? " active" : ""}" id="tab-panel-history" role="tabpanel" aria-labelledby="tab-history">
      <!-- History (Collapsible) -->
      <details class="collapsible-section">
        <summary class="section-toggle">
          <span class="toggle-icon">▼</span>
          <h3>Pipeline History</h3>
        </summary>
        <div class="section-content">
          ${getHistoryHtml(history, historyPagination)}
        </div>
      </details>

      <!-- Firewall Dashboard (Issue #387) — charts removed, table remains -->
      ${firewallData ? getFirewallSectionHtml(firewallData.events, firewallData.filters, firewallData.aggregates, firewallData.timeSeriesData, nonce, firewallData.suggestions) : ""}
    </div>

    <div class="tab-panel${activeTab === "epics" ? " active" : ""}" id="tab-panel-epics" role="tabpanel" aria-labelledby="tab-epics">
      ${getEpicEstimatesHtml(aggregates)}
      ${getCrossRepoEpicProgressHtml(aggregates)}
    </div>

    <div class="tab-panel${activeTab === "audit" ? " active" : ""}" id="tab-panel-audit" role="tabpanel" aria-labelledby="tab-audit">
      ${getAuditTabHtml(auditLogData ?? undefined, retentionIntegrityData ?? undefined, nonce)}
    </div>

    <div class="tab-panel${activeTab === "discovery" ? " active" : ""}" id="tab-panel-discovery" role="tabpanel" aria-labelledby="tab-discovery">
      <!-- Discovery Activity (Issue #2434) — autonomous self-improvement loop -->
      ${getDiscoveryTabHtml(discoveryActivityData ?? null)}
    </div>

    <div class="tab-panel${activeTab === "cost" ? " active" : ""}" id="tab-panel-cost" role="tabpanel" aria-labelledby="tab-cost">
      <!-- Platform Cost Analytics (Issue #3317) — server-aggregated cost data -->
      ${getCostTabHtml(platformCostData ?? null, costDateRange)}
    </div>

    <div class="tab-panel${activeTab === "health" ? " active" : ""}" id="tab-panel-health" role="tabpanel" aria-labelledby="tab-health">
      <!-- Platform Analytics Health (Issue #3318) — 7-dimension health score -->
      ${getHealthTabHtml(healthAnalyticsData ?? null, healthFetchedAt ?? null)}
    </div>

    <div class="tab-panel${activeTab === "runs" ? " active" : ""}" id="tab-panel-runs" role="tabpanel" aria-labelledby="tab-runs">
      <!-- Pipeline Runs History (Issue #3319) — paginated run list with drill-down -->
      ${getRunsTabHtml(runsData ?? undefined)}
    </div>

    <div class="tab-panel${activeTab === "trends" ? " active" : ""}" id="tab-panel-trends" role="tabpanel" aria-labelledby="tab-trends">
      <!-- Longitudinal Trends (Issue #3320) — success rate, cost, runs over time -->
      ${getTrendsTabHtml(trendsData ?? undefined)}
    </div>

    <div class="tab-panel${activeTab === "compliance" ? " active" : ""}" id="tab-panel-compliance" role="tabpanel" aria-labelledby="tab-compliance">
      <!-- Compliance Report Generation (Issue #3322) — SOC2/ISO27001 report generation and download -->
      ${getComplianceTabHtml(complianceData ?? undefined)}
    </div>

    <div class="tab-panel${activeTab === "dependencies" ? " active" : ""}" id="tab-panel-dependencies" role="tabpanel" aria-labelledby="tab-dependencies">
      <!-- Dependencies Tab (Issue #3116) — Dependabot PRs with CI status and one-click merge -->
      ${getDependabotTabHtml(dependabotData)}
    </div>
  </div>

  <script nonce="${nonce}">
    const vscode = acquireVsCodeApi();
    console.log('[Nightgauge] Dashboard rendered (no Chart.js), renderTs=' + ${renderTs});
    ${getScript()}

    // Tab navigation (Issue #1539)
    ${getTabScript()}

    // Health widget toggle handler (Issue #655)
    ${getHealthWidgetScript()}

    // Firewall event handlers
    ${getFirewallScript()}

    // Audit Trail event handlers (Issue #1583)
    ${getAuditTabScript()}

    // Platform Cost Tab event handlers (Issue #3317)
    ${getCostTabScript()}

    // Platform Health Tab event handlers (Issue #3318)
    ${getHealthTabScript()}

    // Pipeline Runs Tab event handlers (Issue #3319)
    ${getRunsTabScript()}

    // Trends Tab event handlers (Issue #3320)
    ${getTrendsTabScript()}

    // Compliance Tab event handlers (Issue #3322)
    ${getComplianceTabScript()}

    // Dependencies Tab event handlers (Issue #3116)
    ${getDependabotTabScript()}
  </script>
</body>
</html>`;
}
