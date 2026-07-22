/**
 * HealthTabHtml - Platform analytics health tab renderer (#3318)
 *
 * Exports the full 3-function tab contract: getHealthTabHtml, getHealthTabScript,
 * getHealthTabStyles. Previously this file was a stub delegating to HealthWidgetHtml.
 * The widget helpers are re-exported for backward compatibility with Dashboard.ts.
 */

import { escapeHtml } from "../DashboardComponents";
import type { HealthCheckReport } from "../../../types/pipelineHealth";
import type {
  AnalyticsHealthResult,
  AnalyticsHealthDimension,
} from "../../../services/IpcClientBase";
import type { AnalyticsHealthData } from "../DashboardState";
import {
  getHealthWidgetHtml,
  getHealthWidgetStyles,
  getHealthWidgetScript,
} from "../HealthWidgetHtml";

// ---------------------------------------------------------------------------
// Backward-compat re-exports (used by DashboardHtml.ts / Dashboard.ts)
// ---------------------------------------------------------------------------

export {
  getHealthCheckReportHtml,
  getHealthWidgetHtml,
  getHealthWidgetStyles,
  getHealthWidgetScript,
};

// ---------------------------------------------------------------------------
// Internal helpers (kept private)
// ---------------------------------------------------------------------------

const SEVERITY_BADGE_CLASS: Record<string, string> = {
  critical: "health-badge-critical",
  high: "health-badge-high",
  warning: "health-badge-warning",
  info: "health-badge-info",
};

const SEVERITY_LABEL: Record<string, string> = {
  critical: "Critical",
  high: "High",
  warning: "Warning",
  info: "Info",
};

function scoreColorClass(score: number): string {
  if (score >= 80) return "health-score-good";
  if (score >= 50) return "health-score-fair";
  return "health-score-poor";
}

function formatFreshnessLabel(fetchedAt: Date | null): string {
  if (!fetchedAt) return "";
  const diffMs = Date.now() - fetchedAt.getTime();
  const diffMin = Math.floor(diffMs / 60_000);
  if (diffMin < 1) return "Updated just now";
  if (diffMin === 1) return "Updated 1 minute ago";
  if (diffMin < 60) return `Updated ${diffMin} minutes ago`;
  const diffHr = Math.floor(diffMin / 60);
  return diffHr === 1 ? "Updated 1 hour ago" : `Updated ${diffHr} hours ago`;
}

function getDimensionCardHtml(dim: AnalyticsHealthDimension): string {
  const score = Math.round(dim.score);
  const colorClass = scoreColorClass(dim.score);
  const findingCount = dim.findings.length;
  const label = escapeHtml(dim.label || dim.name);
  return `
    <div class="health-dim-card">
      <div class="health-dim-header">
        <span class="health-dim-label">${label}</span>
        ${findingCount > 0 ? `<span class="health-dim-badge">${findingCount}</span>` : ""}
      </div>
      <div class="health-dim-score-row">
        <span class="health-dim-score ${colorClass}">${score}</span>
        <div class="health-score-track">
          <div class="health-score-bar ${colorClass}" style="width:${Math.min(score, 100)}%"></div>
        </div>
      </div>
    </div>`;
}

function getFindingHtml(finding: AnalyticsHealthDimension["findings"][number]): string {
  const severity = finding.severity in SEVERITY_BADGE_CLASS ? finding.severity : "info";
  const badgeClass = SEVERITY_BADGE_CLASS[severity];
  const badgeLabel = SEVERITY_LABEL[severity];
  const issueLink =
    finding.issue_number != null
      ? `<a class="health-finding-issue-link" href="https://github.com/nightgauge/nightgauge/issues/${finding.issue_number}" target="_blank">View #${finding.issue_number}</a>`
      : "";
  return `
    <details class="health-finding">
      <summary class="health-finding-summary">
        <span class="health-badge ${badgeClass}">${escapeHtml(badgeLabel)}</span>
        <span class="health-finding-title">${escapeHtml(finding.title)}</span>
      </summary>
      <div class="health-finding-body">
        <p class="health-finding-desc">${escapeHtml(finding.description)}</p>
        ${finding.recommendation ? `<p class="health-finding-rec"><strong>Recommendation:</strong> ${escapeHtml(finding.recommendation)}</p>` : ""}
        ${issueLink}
      </div>
    </details>`;
}

// ---------------------------------------------------------------------------
// Backward-compat: getHealthCheckReportHtml (was inline in old HealthTabHtml)
// ---------------------------------------------------------------------------

function getHealthCheckReportHtml(report: HealthCheckReport | null | undefined): string {
  if (!report) return "";

  const severityColors: Record<string, string> = {
    critical: "#e74c3c",
    high: "#e67e22",
    warning: "#f1c40f",
    info: "#3498db",
  };

  const severityIcons: Record<string, string> = {
    critical: "🔴",
    high: "🟠",
    warning: "🟡",
    info: "🔵",
  };

  const findingsHtml =
    report.findings.length === 0
      ? '<p style="color: var(--vscode-descriptionForeground); padding: 12px;">No findings for the selected criteria.</p>'
      : report.findings
          .map(
            (f) => `
      <div style="border-left: 3px solid ${severityColors[f.severity] ?? "#888"}; padding: 8px 12px; margin-bottom: 8px; background: var(--vscode-editor-background); border-radius: 0 4px 4px 0;">
        <div style="display: flex; align-items: center; gap: 8px; margin-bottom: 4px;">
          <span>${severityIcons[f.severity] ?? ""}</span>
          <strong style="flex: 1;">${escapeHtml(f.title)}</strong>
          <span style="font-size: 0.85em; color: var(--vscode-descriptionForeground);">${escapeHtml(f.dimension)}</span>
        </div>
        <p style="margin: 4px 0; color: var(--vscode-descriptionForeground); font-size: 0.9em;">${escapeHtml(f.description)}</p>
        ${f.recommendation ? `<p style="margin: 4px 0; font-size: 0.85em;"><strong>Recommendation:</strong> ${escapeHtml(f.recommendation)}</p>` : ""}
      </div>`
          )
          .join("");

  const bySev = report.findings_by_severity;
  const summaryParts = [
    bySev.critical > 0
      ? `<span style="color:${severityColors.critical}">${bySev.critical} critical</span>`
      : "",
    bySev.high > 0 ? `<span style="color:${severityColors.high}">${bySev.high} high</span>` : "",
    bySev.warning > 0
      ? `<span style="color:${severityColors.warning}">${bySev.warning} warning</span>`
      : "",
    bySev.info > 0 ? `<span style="color:${severityColors.info}">${bySev.info} info</span>` : "",
  ]
    .filter(Boolean)
    .join(" · ");

  return `
    <details class="collapsible-section" open>
      <summary class="section-toggle">
        <span class="toggle-icon">▼</span>
        <h3>Pipeline Health Report</h3>
        <span style="font-size: 0.85em; margin-left: 8px; color: var(--vscode-descriptionForeground);">
          ${report.findings.length} findings ${summaryParts ? `(${summaryParts})` : ""}
        </span>
      </summary>
      <div class="section-content">
        <div style="display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: 8px; margin-bottom: 12px;">
          <div class="metric-card" style="padding: 8px; text-align: center;">
            <div style="font-size: 1.4em; font-weight: bold;">${report.summary.total_runs}</div>
            <div style="font-size: 0.8em; color: var(--vscode-descriptionForeground);">Total Runs</div>
          </div>
          <div class="metric-card" style="padding: 8px; text-align: center;">
            <div style="font-size: 1.4em; font-weight: bold;">${(report.summary.success_rate * 100).toFixed(1)}%</div>
            <div style="font-size: 0.8em; color: var(--vscode-descriptionForeground);">Success Rate</div>
          </div>
          <div class="metric-card" style="padding: 8px; text-align: center;">
            <div style="font-size: 1.4em; font-weight: bold;">$${report.summary.total_cost_usd.toFixed(2)}</div>
            <div style="font-size: 0.8em; color: var(--vscode-descriptionForeground);">Total Cost</div>
          </div>
          <div class="metric-card" style="padding: 8px; text-align: center;">
            <div style="font-size: 1.4em; font-weight: bold;">${(report.summary.cache_hit_rate * 100).toFixed(1)}%</div>
            <div style="font-size: 0.8em; color: var(--vscode-descriptionForeground);">Cache Hit Rate</div>
          </div>
        </div>
        <div style="font-size: 0.8em; color: var(--vscode-descriptionForeground); margin-bottom: 8px;">
          Period: ${escapeHtml(report.analysis_period.from)} — ${escapeHtml(report.analysis_period.to)} (${report.analysis_period.period_days} days)
        </div>
        ${findingsHtml}
      </div>
    </details>`;
}

// ---------------------------------------------------------------------------
// Error state helpers (#3679)
// ---------------------------------------------------------------------------

function getHealthErrorState(errorType: string | undefined): {
  icon: string;
  title: string;
  hint: string;
  cta: string;
} {
  switch (errorType) {
    case "not_signed_in":
      return {
        icon: "🔑",
        title: "Sign in to view health data",
        hint: "Connect your Nightgauge account to see 7-dimension pipeline scoring.",
        cta: `<button class="action-btn" id="healthSignInBtn">Sign in to Nightgauge</button>`,
      };
    case "token_expired":
      return {
        icon: "🔒",
        title: "Session expired",
        hint: "Your authentication token has expired. Sign in again to restore access.",
        cta: `<button class="action-btn" id="healthSignInBtn">Re-authenticate</button>`,
      };
    case "no_permission":
      return {
        icon: "🚫",
        title: "Access denied",
        hint: "Your account does not have permission to view health analytics. Check your role or license tier.",
        cta: `<button class="action-btn" id="healthRefreshBtn">Retry</button>`,
      };
    case "ipc_unavailable":
    case "ipc_timeout":
      return {
        icon: "⚡",
        title: "Go backend unavailable",
        hint:
          errorType === "ipc_timeout"
            ? "The IPC request timed out. The Go backend may be busy — retry in a moment."
            : "The Go backend is not connected. Reload the window to restart it.",
        cta: `<button class="action-btn" id="healthRefreshBtn">Retry</button>`,
      };
    case "server_error":
      return {
        icon: "🏥",
        title: "Platform server error",
        hint: "The Nightgauge platform returned an error. This is likely a temporary issue — retry shortly.",
        cta: `<button class="action-btn" id="healthRefreshBtn">Retry</button>`,
      };
    default:
      return {
        icon: "🏥",
        title: "Health data unavailable — connect to Nightgauge platform",
        hint: "Platform health analysis provides 7-dimension pipeline scoring with findings and recommendations.",
        cta: `<button class="action-btn" id="healthRefreshBtn">Refresh</button>`,
      };
  }
}

// ---------------------------------------------------------------------------
// Public tab contract
// ---------------------------------------------------------------------------

/**
 * Render the full health tab HTML.
 * When healthData is null or has no result, renders a contextual error state (#3679).
 */
export function getHealthTabHtml(
  healthData: AnalyticsHealthData | null,
  fetchedAt: Date | null
): string {
  const data = healthData?.result ?? null;
  if (data === null) {
    const errorType = healthData?.errorType;
    const { icon, title, hint, cta } = getHealthErrorState(errorType);
    return `
      <div class="health-tab">
        <div class="health-empty-state">
          <div class="health-empty-icon">${icon}</div>
          <p class="health-empty-title">${escapeHtml(title)}</p>
          <p class="health-empty-hint">${escapeHtml(hint)}</p>
          ${cta}
        </div>
      </div>`;
  }

  const overallScore = Math.round(data.overall_score);
  const overallColorClass = scoreColorClass(data.overall_score);
  const freshnessLabel = formatFreshnessLabel(fetchedAt);

  const dimCardsHtml = data.dimensions.map(getDimensionCardHtml).join("");

  // Collect all findings across all dimensions, sorted by severity
  const severityOrder = ["critical", "high", "warning", "info"];
  const allFindings = data.dimensions.flatMap((dim) =>
    dim.findings.map((f) => ({ ...f, dimName: dim.name }))
  );
  allFindings.sort((a, b) => severityOrder.indexOf(a.severity) - severityOrder.indexOf(b.severity));

  const findingsHtml =
    allFindings.length === 0
      ? `<p class="health-no-findings">No findings — pipeline health looks good.</p>`
      : allFindings.map(getFindingHtml).join("");

  return `
    <div class="health-tab">
      <div class="health-tab-header">
        <div class="health-overall-card">
          <div class="health-overall-label">Overall Score</div>
          <div class="health-overall-score ${overallColorClass}">${overallScore}</div>
          <div class="health-score-track health-overall-track">
            <div class="health-score-bar ${overallColorClass}" style="width:${Math.min(overallScore, 100)}%"></div>
          </div>
          <div class="health-meta">
            ${data.total_runs > 0 ? `<span>${data.total_runs} run${data.total_runs === 1 ? "" : "s"} · ${data.period_days}d period</span>` : ""}
          </div>
        </div>
        <div class="health-tab-actions">
          ${freshnessLabel ? `<span class="health-freshness">${escapeHtml(freshnessLabel)}</span>` : ""}
          <button class="action-btn" id="healthRefreshBtn">Refresh</button>
        </div>
      </div>

      <div class="health-section">
        <h3 class="health-section-title">Dimensions</h3>
        <div class="health-dim-grid">
          ${dimCardsHtml}
        </div>
      </div>

      <div class="health-section">
        <h3 class="health-section-title">Findings</h3>
        <div class="health-findings-list">
          ${findingsHtml}
        </div>
      </div>
    </div>`;
}

/**
 * JS event handlers for the health tab.
 * Refresh button posts healthRefresh to the extension host.
 */
export function getHealthTabScript(): string {
  return `
    (function() {
      var healthPanel = document.getElementById('tab-panel-health');
      if (!healthPanel) return;
      healthPanel.addEventListener('click', function(e) {
        if (e.target.closest('#healthRefreshBtn')) {
          vscode.postMessage({ type: 'healthRefresh' });
        } else if (e.target.closest('#healthSignInBtn')) {
          vscode.postMessage({ type: 'signInWithPlatform' });
        }
      });
    })();
  `;
}

/** CSS for the platform health tab (#3318). */
export function getHealthTabStyles(): string {
  return `
    .health-tab {
      padding: var(--spacing-md, 12px) 0;
    }
    .health-tab-header {
      display: flex;
      align-items: flex-start;
      justify-content: space-between;
      gap: var(--spacing-md, 12px);
      margin-bottom: var(--spacing-lg, 16px);
      flex-wrap: wrap;
    }
    .health-tab-actions {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm, 8px);
      flex-shrink: 0;
    }
    .health-overall-card {
      flex: 1;
      min-width: 180px;
    }
    .health-overall-label {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
      text-transform: uppercase;
      letter-spacing: 0.05em;
      margin-bottom: 4px;
    }
    .health-overall-score {
      font-size: 2.8em;
      font-weight: 700;
      line-height: 1;
      margin-bottom: 6px;
    }
    .health-overall-track {
      max-width: 240px;
      margin-bottom: 6px;
    }
    .health-meta {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
    }
    .health-score-good { color: var(--vscode-terminal-ansiGreen, #4ec9b0); }
    .health-score-fair { color: var(--vscode-editorWarning-foreground, #cca700); }
    .health-score-poor { color: var(--vscode-editorError-foreground, #f14c4c); }
    .health-score-track {
      height: 6px;
      background: var(--vscode-editor-inactiveSelectionBackground);
      border-radius: 3px;
      overflow: hidden;
    }
    .health-score-bar {
      height: 100%;
      border-radius: 3px;
      transition: width 0.3s ease;
    }
    .health-score-bar.health-score-good { background: var(--vscode-terminal-ansiGreen, #4ec9b0); }
    .health-score-bar.health-score-fair { background: var(--vscode-editorWarning-foreground, #cca700); }
    .health-score-bar.health-score-poor { background: var(--vscode-editorError-foreground, #f14c4c); }
    .health-freshness {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
    }
    .health-section {
      margin-bottom: var(--spacing-lg, 16px);
    }
    .health-section-title {
      font-size: 0.85em;
      font-weight: 600;
      color: var(--vscode-descriptionForeground);
      text-transform: uppercase;
      letter-spacing: 0.04em;
      margin: 0 0 var(--spacing-sm, 8px) 0;
    }
    .health-dim-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
      gap: var(--spacing-sm, 8px);
    }
    .health-dim-card {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius, 3px);
      padding: var(--spacing-sm, 8px);
    }
    .health-dim-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin-bottom: 6px;
    }
    .health-dim-label {
      font-size: 0.85em;
      color: var(--vscode-foreground);
      font-weight: 500;
    }
    .health-dim-badge {
      font-size: 0.75em;
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
      border-radius: 10px;
      padding: 1px 6px;
    }
    .health-dim-score-row {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm, 8px);
    }
    .health-dim-score {
      font-size: 1.1em;
      font-weight: 700;
      min-width: 28px;
    }
    .health-dim-score-row .health-score-track {
      flex: 1;
    }
    .health-findings-list {
      display: flex;
      flex-direction: column;
      gap: 4px;
    }
    .health-finding {
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius, 3px);
      overflow: hidden;
    }
    .health-finding-summary {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm, 8px);
      padding: 6px 10px;
      cursor: pointer;
      background: var(--vscode-editorWidget-background);
      list-style: none;
    }
    .health-finding-summary::-webkit-details-marker { display: none; }
    .health-finding-title {
      font-size: 0.9em;
      color: var(--vscode-foreground);
      flex: 1;
    }
    .health-finding-body {
      padding: 8px 10px;
      background: var(--vscode-editor-background);
      border-top: 1px solid var(--vscode-panel-border);
    }
    .health-finding-desc {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
      margin: 0 0 6px 0;
    }
    .health-finding-rec {
      font-size: 0.85em;
      margin: 0 0 6px 0;
    }
    .health-finding-issue-link {
      font-size: 0.8em;
      color: var(--vscode-textLink-foreground);
    }
    .health-badge {
      display: inline-block;
      padding: 1px 6px;
      border-radius: 3px;
      font-size: 0.75em;
      font-weight: 600;
      flex-shrink: 0;
    }
    .health-badge-critical {
      background: var(--vscode-editorError-foreground, #f14c4c);
      color: var(--vscode-editor-background);
    }
    .health-badge-high {
      background: var(--vscode-editorWarning-foreground, #cca700);
      color: var(--vscode-editor-background);
    }
    .health-badge-warning {
      background: color-mix(in srgb, var(--vscode-editorWarning-foreground, #cca700) 60%, transparent);
      color: var(--vscode-foreground);
    }
    .health-badge-info {
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
    }
    .health-no-findings {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
      padding: var(--spacing-sm, 8px) 0;
    }
    .health-empty-state {
      display: flex;
      flex-direction: column;
      align-items: center;
      padding: var(--spacing-xl, 32px) var(--spacing-md, 12px);
      gap: var(--spacing-sm, 8px);
      text-align: center;
    }
    .health-empty-icon { font-size: 2em; }
    .health-empty-title {
      color: var(--vscode-foreground);
      font-weight: 600;
      margin: 0;
    }
    .health-empty-hint {
      color: var(--vscode-descriptionForeground);
      font-size: 0.85em;
      margin: 0;
    }
  `;
}
