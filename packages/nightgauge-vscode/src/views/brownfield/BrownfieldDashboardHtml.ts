/**
 * BrownfieldDashboardHtml - HTML template generator for brownfield dashboard
 *
 * Generates the HTML, CSS, and JavaScript for the brownfield modernization
 * dashboard webview panel. Uses var(--vscode-*) CSS custom properties for
 * full theme compatibility.
 *
 * @see Issue #1163 - Brownfield Modernization Progress Dashboard
 */

import * as vscode from "vscode";
import type {
  BrownfieldDashboardData,
  DimensionBreakdown,
  ModernizationProgress,
  SecuritySeverityCounts,
  BeforeAfterDelta,
  DependencyHealth,
  QuickWin,
  HealthStatus,
} from "./BrownfieldTypes";
import {
  getDimensionBarChartHtml,
  getProgressBarHtml,
  getSeverityBarsHtml,
  getDependencyBarHtml,
  getTrendIndicatorHtml,
} from "./BrownfieldCharts";
import { BrownfieldDashboardState } from "./BrownfieldDashboardState";

/**
 * Generate nonce for CSP
 */
function getNonce(): string {
  let text = "";
  const possible = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  for (let i = 0; i < 32; i++) {
    text += possible.charAt(Math.floor(Math.random() * possible.length));
  }
  return text;
}

/**
 * Escape HTML special characters
 */
function escapeHtml(text: string): string {
  return text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

/**
 * Format ISO date string to readable format
 */
function formatDate(iso: string): string {
  try {
    const d = new Date(iso);
    return d.toLocaleDateString(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
    });
  } catch {
    return iso;
  }
}

/**
 * Status color for inline styles
 */
function statusColor(status: HealthStatus): string {
  switch (status) {
    case "excellent":
      return "#22c55e";
    case "good":
      return "#3b82f6";
    case "fair":
      return "#eab308";
    case "poor":
      return "#f97316";
    case "critical":
      return "#ef4444";
  }
}

/**
 * Generate the full dashboard HTML
 */
export function getBrownfieldDashboardHtml(
  webview: vscode.Webview,
  data: BrownfieldDashboardData,
  state: BrownfieldDashboardState
): string {
  const nonce = getNonce();

  if (!data.hasAnyData) {
    return getEmptyStateHtml(webview, nonce);
  }

  const dimensions = state.getDimensionBreakdown();
  const progress = state.getModernizationProgress();
  const quickWins = state.getQuickWins();
  const severityCounts = state.getSecuritySeverityCounts();
  const delta = state.getBeforeAfterDelta();
  const depHealth = state.getDependencyHealth();

  const sections = [
    getHealthOverviewSection(data, dimensions),
    getDimensionBreakdownSection(dimensions),
    getModernizationProgressSection(progress),
    getPhaseTrackerSection(data),
    getQuickWinsSection(quickWins),
    getBeforeAfterSection(delta, data),
    getSecuritySection(data, severityCounts),
    getDependencySection(depHealth),
  ];

  return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'nonce-${nonce}'; script-src 'nonce-${nonce}';">
  <title>Brownfield Modernization Dashboard</title>
  <style nonce="${nonce}">
    ${getStyles()}
  </style>
</head>
<body>
  <div class="bf-dashboard">
    <div class="bf-header">
      <h1>Brownfield Modernization Dashboard</h1>
      <button class="bf-refresh-btn" onclick="refresh()">Refresh</button>
    </div>
    <div class="bf-grid">
      ${sections.join("\n")}
    </div>
  </div>
  <script nonce="${nonce}">
    const vscode = acquireVsCodeApi();
    function refresh() {
      vscode.postMessage({ type: 'refresh' });
    }
  </script>
</body>
</html>`;
}

// ---------------------------------------------------------------------------
// Section Generators
// ---------------------------------------------------------------------------

function getEmptyStateHtml(webview: vscode.Webview, nonce: string): string {
  return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'nonce-${nonce}'; script-src 'nonce-${nonce}';">
  <title>Brownfield Modernization Dashboard</title>
  <style nonce="${nonce}">
    ${getStyles()}
  </style>
</head>
<body>
  <div class="bf-dashboard">
    <div class="bf-header">
      <h1>Brownfield Modernization Dashboard</h1>
    </div>
    <div class="bf-empty-state">
      <div class="bf-empty-icon">&#128269;</div>
      <h2>No Assessment Data Found</h2>
      <p>Run brownfield assessment skills to populate this dashboard:</p>
      <ol>
        <li><code>/nightgauge-health-check</code> &mdash; Assess codebase health</li>
        <li><code>/nightgauge-security-audit</code> &mdash; Audit security posture</li>
        <li><code>/nightgauge-modernize-plan</code> &mdash; Generate modernization plan</li>
        <li><code>/nightgauge-dep-modernize</code> &mdash; Analyze dependencies</li>
      </ol>
      <p>Assessment reports are saved to <code>.nightgauge/</code> and this dashboard will auto-refresh when they appear.</p>
    </div>
  </div>
  <script nonce="${nonce}">
    const vscode = acquireVsCodeApi();
  </script>
</body>
</html>`;
}

function getHealthOverviewSection(
  data: BrownfieldDashboardData,
  dimensions: DimensionBreakdown[]
): string {
  if (!data.health) return "";

  const score = data.health.summary.overall_health_score;
  const status = data.health.summary.status;
  const color = statusColor(status);
  const trendHtml = getTrendIndicatorHtml(data.history, "health_score");
  const date = formatDate(data.health.assessment_date);

  return `
    <div class="bf-card bf-card-wide">
      <h2>Health Score Overview</h2>
      <div class="bf-score-overview">
        <div class="bf-big-score" style="color: ${color};">${score}</div>
        <div class="bf-score-details">
          <span class="bf-status-badge" style="background: ${color};">${status.toUpperCase()}</span>
          <span class="bf-score-trend">${trendHtml}</span>
          <span class="bf-date">Assessed: ${date}</span>
        </div>
      </div>
    </div>`;
}

function getDimensionBreakdownSection(dimensions: DimensionBreakdown[]): string {
  if (dimensions.length === 0) return "";

  return `
    <div class="bf-card bf-card-wide">
      <h2>Dimension Breakdown</h2>
      ${getDimensionBarChartHtml(dimensions)}
    </div>`;
}

function getModernizationProgressSection(progress: ModernizationProgress): string {
  if (progress.totalTasks === 0) return "";

  const activeLabel = progress.activePhase
    ? `Phase ${progress.activePhaseIndex}: ${escapeHtml(progress.activePhase.name)}`
    : "No active phase";

  return `
    <div class="bf-card">
      <h2>Modernization Progress</h2>
      ${getProgressBarHtml(progress.completedTasks, progress.totalTasks)}
      <div class="bf-active-phase">
        <span class="bf-phase-label">Active:</span> ${activeLabel}
      </div>
    </div>`;
}

function getPhaseTrackerSection(data: BrownfieldDashboardData): string {
  if (!data.plan || data.plan.phases.length === 0) return "";

  const phases = data.plan.phases;
  const phaseItems = phases
    .map((phase, idx) => {
      // All phases start as pending since we don't track completion yet
      const stateClass = idx === 0 ? "bf-phase-active" : "bf-phase-pending";
      const icon = idx === 0 ? "&#9654;" : "&#9675;";
      const taskCount = phase.tasks.length;

      return `
        <div class="bf-phase-item ${stateClass}">
          <span class="bf-phase-icon">${icon}</span>
          <span class="bf-phase-name">Phase ${phase.phase_number}: ${escapeHtml(phase.name)}</span>
          <span class="bf-phase-count">${taskCount} task${taskCount !== 1 ? "s" : ""}</span>
        </div>`;
    })
    .join("");

  return `
    <div class="bf-card">
      <h2>Phase Tracker</h2>
      <div class="bf-phase-list">${phaseItems}</div>
    </div>`;
}

function getQuickWinsSection(quickWins: QuickWin[]): string {
  if (quickWins.length === 0) return "";

  const rows = quickWins
    .map(
      (win) => `
      <tr>
        <td>${escapeHtml(win.title)}</td>
        <td><span class="bf-effort-badge bf-effort-${win.effort.toLowerCase()}">${win.effort}</span></td>
        <td>${escapeHtml(win.impact)}</td>
        <td>Phase ${win.phase}</td>
      </tr>`
    )
    .join("");

  return `
    <div class="bf-card bf-card-wide">
      <h2>Quick Wins</h2>
      <table class="bf-table">
        <thead>
          <tr><th>Task</th><th>Effort</th><th>Impact</th><th>Phase</th></tr>
        </thead>
        <tbody>${rows}</tbody>
      </table>
    </div>`;
}

function getBeforeAfterSection(
  delta: BeforeAfterDelta | null,
  data: BrownfieldDashboardData
): string {
  if (!delta) return "";

  const formatScore = (score: number | null): string => (score !== null ? `${score}` : "--");

  const healthDelta =
    delta.currentHealthScore !== null && delta.initialHealthScore !== null
      ? delta.currentHealthScore - delta.initialHealthScore
      : null;

  const secDelta =
    delta.currentSecurityScore !== null && delta.initialSecurityScore !== null
      ? delta.currentSecurityScore - delta.initialSecurityScore
      : null;

  const deltaHtml = (d: number | null): string => {
    if (d === null) return "";
    if (d > 0) return `<span class="bf-delta bf-delta-up">+${d}</span>`;
    if (d < 0) return `<span class="bf-delta bf-delta-down">${d}</span>`;
    return '<span class="bf-delta">0</span>';
  };

  return `
    <div class="bf-card">
      <h2>Before / After</h2>
      <table class="bf-table bf-compare-table">
        <thead>
          <tr><th></th><th>Initial (${formatDate(delta.initialDate)})</th><th>Current</th><th>Change</th></tr>
        </thead>
        <tbody>
          <tr>
            <td>Health Score</td>
            <td>${formatScore(delta.initialHealthScore)}</td>
            <td>${formatScore(delta.currentHealthScore)}</td>
            <td>${deltaHtml(healthDelta)}</td>
          </tr>
          <tr>
            <td>Security Score</td>
            <td>${formatScore(delta.initialSecurityScore)}</td>
            <td>${formatScore(delta.currentSecurityScore)}</td>
            <td>${deltaHtml(secDelta)}</td>
          </tr>
        </tbody>
      </table>
    </div>`;
}

function getSecuritySection(data: BrownfieldDashboardData, counts: SecuritySeverityCounts): string {
  if (!data.security) return "";

  const score = data.security.summary.overall_security_score;
  const status = data.security.summary.status;
  const color = statusColor(status);
  const trendHtml = getTrendIndicatorHtml(data.history, "security_score");

  return `
    <div class="bf-card">
      <h2>Security Score</h2>
      <div class="bf-score-row">
        <span class="bf-mid-score" style="color: ${color};">${score}</span>
        <span class="bf-status-badge" style="background: ${color};">${status.toUpperCase()}</span>
        ${trendHtml}
      </div>
      <h3>Findings by Severity</h3>
      ${getSeverityBarsHtml(counts)}
    </div>`;
}

function getDependencySection(depHealth: DependencyHealth): string {
  if (depHealth.total === 0) return "";

  return `
    <div class="bf-card">
      <h2>Dependency Status</h2>
      <div class="bf-dep-summary">
        <span><strong>${depHealth.total}</strong> total</span>
        <span><strong>${depHealth.upToDatePercent}%</strong> up-to-date</span>
      </div>
      ${getDependencyBarHtml(depHealth.total, depHealth.outdated, depHealth.vulnerable)}
    </div>`;
}

// ---------------------------------------------------------------------------
// CSS Styles
// ---------------------------------------------------------------------------

function getStyles(): string {
  return `
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: var(--vscode-font-family);
      font-size: var(--vscode-font-size);
      color: var(--vscode-foreground);
      background: var(--vscode-editor-background);
      padding: 16px;
    }
    .bf-dashboard { max-width: 1200px; margin: 0 auto; }
    .bf-header {
      display: flex; justify-content: space-between; align-items: center;
      margin-bottom: 20px; padding-bottom: 12px;
      border-bottom: 1px solid var(--vscode-panel-border);
    }
    .bf-header h1 { font-size: 1.4em; font-weight: 600; }
    .bf-refresh-btn {
      background: var(--vscode-button-background);
      color: var(--vscode-button-foreground);
      border: none; padding: 6px 14px; border-radius: 4px;
      cursor: pointer; font-size: 0.85em;
    }
    .bf-refresh-btn:hover { background: var(--vscode-button-hoverBackground); }

    /* Grid layout */
    .bf-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(420px, 1fr));
      gap: 16px;
    }

    /* Cards */
    .bf-card {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: 8px; padding: 16px;
    }
    .bf-card-wide { grid-column: 1 / -1; }
    .bf-card h2 {
      font-size: 1em; font-weight: 600; margin-bottom: 12px;
      color: var(--vscode-foreground);
    }
    .bf-card h3 {
      font-size: 0.9em; font-weight: 600; margin: 12px 0 8px;
      color: var(--vscode-descriptionForeground);
    }

    /* Score overview */
    .bf-score-overview { display: flex; align-items: center; gap: 20px; }
    .bf-big-score { font-size: 3em; font-weight: 700; line-height: 1; }
    .bf-mid-score { font-size: 2em; font-weight: 700; line-height: 1; }
    .bf-score-details { display: flex; flex-direction: column; gap: 6px; }
    .bf-score-row { display: flex; align-items: center; gap: 12px; margin-bottom: 12px; }
    .bf-status-badge {
      display: inline-block; padding: 2px 10px; border-radius: 12px;
      color: #fff; font-size: 0.75em; font-weight: 600; text-transform: uppercase;
    }
    .bf-date { color: var(--vscode-descriptionForeground); font-size: 0.85em; }

    /* Bar charts */
    .bf-bar-chart { display: flex; flex-direction: column; gap: 8px; }
    .bf-bar-row { display: flex; align-items: center; gap: 8px; }
    .bf-bar-label {
      width: 140px; font-size: 0.85em; text-align: right;
      color: var(--vscode-descriptionForeground); flex-shrink: 0;
    }
    .bf-bar-track {
      flex: 1; height: 16px; border-radius: 4px;
      background: var(--vscode-editorWidget-border, rgba(128,128,128,0.2));
      overflow: hidden;
    }
    .bf-bar-fill { height: 100%; border-radius: 4px; transition: width 0.3s ease; }
    .bf-bar-value { width: 30px; font-size: 0.85em; font-weight: 600; text-align: right; }

    /* Progress bar */
    .bf-progress-container { margin: 8px 0; }
    .bf-progress-track {
      height: 20px; border-radius: 6px;
      background: var(--vscode-editorWidget-border, rgba(128,128,128,0.2));
      overflow: hidden; margin-bottom: 6px;
    }
    .bf-progress-fill { height: 100%; border-radius: 6px; transition: width 0.3s ease; }
    .bf-progress-label {
      font-size: 0.85em; color: var(--vscode-descriptionForeground);
    }

    /* Active phase */
    .bf-active-phase {
      margin-top: 8px; font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }
    .bf-phase-label { font-weight: 600; }

    /* Phase tracker */
    .bf-phase-list { display: flex; flex-direction: column; gap: 6px; }
    .bf-phase-item {
      display: flex; align-items: center; gap: 8px;
      padding: 6px 10px; border-radius: 4px; font-size: 0.85em;
    }
    .bf-phase-active {
      background: var(--vscode-list-activeSelectionBackground);
      color: var(--vscode-list-activeSelectionForeground);
    }
    .bf-phase-pending { color: var(--vscode-descriptionForeground); }
    .bf-phase-icon { font-size: 0.8em; }
    .bf-phase-name { flex: 1; }
    .bf-phase-count {
      font-size: 0.8em; color: var(--vscode-descriptionForeground);
    }

    /* Quick wins table */
    .bf-table {
      width: 100%; border-collapse: collapse; font-size: 0.85em;
    }
    .bf-table th {
      text-align: left; padding: 6px 8px;
      border-bottom: 1px solid var(--vscode-panel-border);
      color: var(--vscode-descriptionForeground); font-weight: 600;
    }
    .bf-table td {
      padding: 6px 8px;
      border-bottom: 1px solid var(--vscode-panel-border);
    }
    .bf-effort-badge {
      display: inline-block; padding: 1px 6px; border-radius: 4px;
      font-size: 0.8em; font-weight: 600;
    }
    .bf-effort-xs, .bf-effort-s { background: #22c55e20; color: #22c55e; }
    .bf-effort-m { background: #eab30820; color: #eab308; }
    .bf-effort-l, .bf-effort-xl { background: #f9731620; color: #f97316; }

    /* Before/After comparison */
    .bf-compare-table td:first-child { font-weight: 600; }
    .bf-delta { font-weight: 600; }
    .bf-delta-up { color: #22c55e; }
    .bf-delta-down { color: #ef4444; }

    /* Dependency bar */
    .bf-dep-bar { margin: 8px 0; }
    .bf-dep-stacked-track {
      display: flex; height: 20px; border-radius: 6px;
      overflow: hidden;
      background: var(--vscode-editorWidget-border, rgba(128,128,128,0.2));
    }
    .bf-dep-fill-good { background: #22c55e; }
    .bf-dep-fill-warn { background: #eab308; }
    .bf-dep-legend { display: flex; gap: 16px; margin-top: 8px; font-size: 0.8em; }
    .bf-dep-legend-item { display: flex; align-items: center; gap: 4px; }
    .bf-dot { width: 8px; height: 8px; border-radius: 50%; display: inline-block; }
    .bf-dot-good { background: #22c55e; }
    .bf-dot-warn { background: #eab308; }
    .bf-dot-critical { background: #ef4444; }
    .bf-dep-summary {
      display: flex; gap: 20px; margin-bottom: 8px; font-size: 0.9em;
    }

    /* Trend indicators */
    .bf-trend { font-size: 0.85em; font-weight: 600; }
    .bf-trend-up { color: #22c55e; }
    .bf-trend-down { color: #ef4444; }
    .bf-trend-stable { color: var(--vscode-descriptionForeground); }

    /* Empty state */
    .bf-empty-state {
      text-align: center; padding: 60px 20px;
      color: var(--vscode-descriptionForeground);
    }
    .bf-empty-icon { font-size: 3em; margin-bottom: 16px; }
    .bf-empty-state h2 { margin-bottom: 12px; color: var(--vscode-foreground); }
    .bf-empty-state ol {
      text-align: left; display: inline-block; margin: 12px 0;
    }
    .bf-empty-state li { margin: 6px 0; }
    .bf-empty-state code {
      background: var(--vscode-textCodeBlock-background);
      padding: 2px 6px; border-radius: 3px; font-size: 0.9em;
    }
    .brownfield-empty {
      color: var(--vscode-descriptionForeground);
      font-style: italic; font-size: 0.85em;
    }
  `;
}
