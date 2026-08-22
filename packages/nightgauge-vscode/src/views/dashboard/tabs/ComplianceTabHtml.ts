/**
 * ComplianceTabHtml — HTML generator for the Compliance dashboard tab.
 *
 * Follows the established tab-module contract:
 *   getComplianceTabHtml()    — returns HTML string
 *   getComplianceTabScript()  — returns JS string (event handlers)
 *   getComplianceTabStyles()  — returns CSS string (scoped)
 *
 * Role-gated: shows locked state for non-owner/admin users.
 * Polling is managed in Dashboard.ts (2s for first 30s, then 5s).
 *
 * @see Issue #3322 — Add Compliance Report Generation UI in Extension
 */

import { escapeHtml, formatRelativeTime } from "../DashboardComponents";
import type { ComplianceData, ComplianceReportEntry } from "../DashboardState";
import {
  renderPlatformFailure,
  getPlatformRetryButtonHtml,
  getPlatformSignInButtonHtml,
  getPlatformFailureScript,
} from "./PlatformFailureHtml";

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Render the full compliance tab panel HTML.
 * @param data  Current compliance data; undefined triggers loading state.
 */
export function getComplianceTabHtml(data: ComplianceData | undefined): string {
  if (!data) {
    return getComplianceLoadingHtml();
  }
  if (!data.hasAccess) {
    return getComplianceNoAccessHtml(data.failure);
  }
  if (data.isLoading) {
    return getComplianceLoadingHtml();
  }

  // hasAccess is true here, so a `failure` means a mutation (e.g. generate
  // report) failed while the list itself is still showing — render it as a
  // banner rather than replacing the whole tab (#748).
  const banner = data.failure
    ? `<div class="compliance-error-banner">${renderPlatformFailure(data.failure).hintHtml}</div>`
    : "";

  return `
    <div class="compliance-tab">
      ${banner}
      ${getComplianceGenerateFormHtml(data)}
      ${getComplianceReportsTableHtml(data)}
    </div>
  `;
}

/**
 * JS event handlers for the compliance tab.
 * Uses event delegation on the tab panel; vscode.postMessage() for IPC.
 */
export function getComplianceTabScript(): string {
  return `
    (function() {
      var compPanel = document.getElementById('tab-panel-compliance');
      if (!compPanel) return;

      compPanel.addEventListener('click', function(e) {
        ${getPlatformFailureScript()}
        // Generate report
        var genBtn = e.target.closest('#complianceGenerateBtn');
        if (genBtn) {
          var reportType = document.getElementById('complianceReportType')?.value || 'soc2';
          var startDate = document.getElementById('complianceStartDate')?.value || '';
          var endDate = document.getElementById('complianceEndDate')?.value || '';
          var format = document.getElementById('complianceFormat')?.value || 'pdf';
          vscode.postMessage({ type: 'complianceGenerateReport', reportType: reportType, startDate: startDate, endDate: endDate, format: format });
          return;
        }

        // Download report
        var dlBtn = e.target.closest('[data-action="compliance-download"]');
        if (dlBtn) {
          var reportId = dlBtn.getAttribute('data-report-id');
          if (reportId) { vscode.postMessage({ type: 'complianceDownloadReport', reportId: reportId }); }
          return;
        }

        // Refresh
        var refreshBtn = e.target.closest('#complianceRefreshBtn');
        if (refreshBtn) {
          vscode.postMessage({ type: 'complianceRefresh' });
          return;
        }

      });
    })();
  `;
}

/**
 * Scoped CSS for the compliance tab panel.
 */
export function getComplianceTabStyles(): string {
  return `
    /* Compliance Tab Styles (Issue #3322) */
    .compliance-tab {
      padding: var(--spacing-sm) 0;
    }

    .compliance-error-banner {
      background: rgba(255, 99, 132, 0.15);
      border: 1px solid rgba(255, 99, 132, 0.4);
      border-radius: var(--border-radius);
      color: rgba(255, 99, 132, 1);
      font-size: 0.85em;
      margin-bottom: var(--spacing-md);
      padding: var(--spacing-sm) var(--spacing-md);
    }

    .compliance-loading,
    .compliance-no-access {
      text-align: center;
      padding: 48px var(--spacing-md);
      color: var(--vscode-descriptionForeground);
    }

    .compliance-no-access .empty-icon,
    .compliance-loading .empty-icon {
      font-size: 3em;
      margin-bottom: var(--spacing-md);
    }

    /* Generate Form */
    .compliance-generate-form {
      background: var(--vscode-editor-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      margin-bottom: var(--spacing-md);
      padding: var(--spacing-md);
    }

    .compliance-form-title {
      font-size: 0.9em;
      font-weight: 600;
      margin-bottom: var(--spacing-sm);
    }

    .compliance-form-row {
      display: flex;
      flex-wrap: wrap;
      gap: var(--spacing-sm);
      align-items: flex-end;
    }

    .compliance-form-group {
      display: flex;
      flex-direction: column;
      gap: 4px;
    }

    .compliance-form-label {
      font-size: 0.78em;
      color: var(--vscode-descriptionForeground);
    }

    .compliance-form-select,
    .compliance-form-input {
      padding: 3px 6px;
      background: var(--vscode-input-background);
      color: var(--vscode-input-foreground);
      border: 1px solid var(--vscode-input-border);
      border-radius: var(--border-radius);
      font-size: 0.82em;
    }

    .compliance-form-select {
      background: var(--vscode-dropdown-background);
      color: var(--vscode-dropdown-foreground);
      border-color: var(--vscode-dropdown-border);
    }

    .compliance-generating-indicator {
      font-size: 0.82em;
      color: var(--vscode-descriptionForeground);
      margin-left: var(--spacing-sm);
    }

    /* Reports Table */
    .compliance-table-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin-bottom: var(--spacing-sm);
    }

    .compliance-table-title {
      font-size: 0.9em;
      font-weight: 600;
    }

    /* The endpoint returns the newest 50 rows and no cursor, so the cap is
       stated rather than paged around (#803). */
    .compliance-table-note {
      font-weight: 400;
      color: var(--vscode-descriptionForeground);
    }

    .compliance-table-container {
      overflow-x: auto;
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      margin-bottom: var(--spacing-md);
    }

    .compliance-table {
      width: 100%;
      border-collapse: collapse;
      font-size: 0.88em;
    }

    .compliance-table th {
      text-align: left;
      padding: var(--spacing-sm);
      border-bottom: 2px solid var(--vscode-panel-border);
      font-weight: 600;
      white-space: nowrap;
    }

    .compliance-table td {
      padding: var(--spacing-sm);
      border-bottom: 1px solid var(--vscode-panel-border);
      vertical-align: middle;
    }

    .compliance-table tr:hover {
      background: var(--vscode-list-hoverBackground);
    }

    .compliance-empty-state {
      text-align: center;
      padding: 32px var(--spacing-md);
      color: var(--vscode-descriptionForeground);
    }

    /* Status badges */
    .compliance-status-badge {
      display: inline-flex;
      align-items: center;
      gap: 4px;
      padding: 2px 7px;
      border-radius: var(--border-radius);
      font-size: 0.78em;
      font-weight: 600;
    }

    .status-pending {
      background: rgba(255, 206, 86, 0.2);
      color: rgba(255, 206, 86, 1);
    }

    .status-complete {
      background: rgba(75, 192, 75, 0.2);
      color: rgba(75, 192, 75, 1);
    }

    .status-failed {
      background: rgba(255, 99, 132, 0.2);
      color: rgba(255, 99, 132, 1);
    }

    .compliance-spinner {
      display: inline-block;
      width: 10px;
      height: 10px;
      border: 2px solid currentColor;
      border-top-color: transparent;
      border-radius: 50%;
      animation: compliance-spin 0.8s linear infinite;
    }

    @keyframes compliance-spin {
      to { transform: rotate(360deg); }
    }

  `;
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

function getComplianceLoadingHtml(): string {
  return `
    <div class="compliance-loading" role="status" aria-live="polite" aria-atomic="true">
      <div class="empty-icon">⏳</div>
      <p>Loading compliance reports…</p>
    </div>
  `;
}

/**
 * Render the compliance no-access state from the classified `PlatformFailure`
 * the service actually reported (#748). This replaces the fixed "Access
 * Required — available to owner and admin roles on eligible plans" copy that
 * used to render for *every* failure kind, including a rejected credential —
 * the bug reported in #748, verified live against an account owner on a pro
 * plan whose 401 was told to "upgrade your plan."
 */
function getComplianceNoAccessHtml(failure: ComplianceData["failure"]): string {
  if (!failure) {
    return `
      <div class="compliance-no-access">
        <div class="empty-icon">🔒</div>
        <h3>Access Required</h3>
        <p>Connect to the platform to generate compliance reports.</p>
      </div>
    `;
  }
  const rendered = renderPlatformFailure(failure);
  const cta = rendered.showSignIn
    ? getPlatformSignInButtonHtml("complianceSignInBtn")
    : rendered.showRetry
      ? getPlatformRetryButtonHtml("complianceRetryBtn", { type: "complianceRefresh" })
      : "";
  return `
    <div class="compliance-no-access">
      <div class="empty-icon">${rendered.icon}</div>
      <h3>${escapeHtml(rendered.title)}</h3>
      <p>${rendered.hintHtml}</p>
      ${cta}
    </div>
  `;
}

function getComplianceGenerateFormHtml(data: ComplianceData): string {
  const isGenerating = data.isGenerating;
  return `
    <div class="compliance-generate-form">
      <div class="compliance-form-title">Generate Compliance Report</div>
      <div class="compliance-form-row">
        <div class="compliance-form-group">
          <label class="compliance-form-label" for="complianceReportType">Report Type</label>
          <select id="complianceReportType" class="compliance-form-select">
            <option value="soc2">SOC 2</option>
            <option value="iso27001">ISO 27001</option>
          </select>
        </div>
        <div class="compliance-form-group">
          <label class="compliance-form-label" for="complianceStartDate">Start Date</label>
          <input type="date" id="complianceStartDate" class="compliance-form-input"
            value="${escapeHtml(data.filters.startDate ?? "")}" />
        </div>
        <div class="compliance-form-group">
          <label class="compliance-form-label" for="complianceEndDate">End Date</label>
          <input type="date" id="complianceEndDate" class="compliance-form-input"
            value="${escapeHtml(data.filters.endDate ?? "")}" />
        </div>
        <div class="compliance-form-group">
          <label class="compliance-form-label" for="complianceFormat">Format</label>
          <select id="complianceFormat" class="compliance-form-select">
            <option value="pdf">PDF</option>
          </select>
        </div>
        <div class="compliance-form-group" style="justify-content: flex-end;">
          <button class="action-btn" id="complianceGenerateBtn" ${isGenerating ? "disabled" : ""}>
            ${isGenerating ? "Generating…" : "Generate"}
          </button>
          ${isGenerating ? `<span class="compliance-generating-indicator"><span class="compliance-spinner"></span> Report in progress…</span>` : ""}
        </div>
      </div>
    </div>
  `;
}

/**
 * Status badge over the platform's own vocabulary — pending | complete |
 * failed. A failed row carries the reason on the badge's tooltip: the list
 * endpoint returns `errorMessage` precisely so the grid can show it without a
 * per-row detail fetch, and a bare "failed" tells the operator nothing (#803).
 */
function getStatusBadgeHtml(entry: ComplianceReportEntry): string {
  const spinnerHtml = entry.status === "pending" ? `<span class="compliance-spinner"></span>` : "";
  const title = entry.errorMessage ? ` title="${escapeHtml(entry.errorMessage)}"` : "";
  return `<span class="compliance-status-badge status-${escapeHtml(entry.status)}"${title}>${spinnerHtml}${escapeHtml(entry.status)}</span>`;
}

function getComplianceReportsTableHtml(data: ComplianceData): string {
  const { reports } = data;

  const tableRows =
    reports.length === 0
      ? `<tr><td colspan="6"><div class="compliance-empty-state">No compliance reports yet. Generate one above.</div></td></tr>`
      : reports
          .map((r: ComplianceReportEntry) => {
            const relTime = r.createdAt ? formatRelativeTime(new Date(r.createdAt)) : "—";
            // A finished report is downloadable, full stop: the artifact is
            // resolved on demand by platform.auditDownloadReport, which serves
            // a signed URL or the JSON payload. Gating on a `downloadUrl` the
            // list never carried hid the button from every report (#803).
            const downloadCell =
              r.status === "complete"
                ? `<button class="action-btn" data-action="compliance-download" data-report-id="${escapeHtml(r.id)}">Download</button>`
                : "—";
            return `
              <tr>
                <td>${escapeHtml(r.reportType.toUpperCase())}</td>
                <td>${escapeHtml(r.startDate)} — ${escapeHtml(r.endDate)}</td>
                <td>${getStatusBadgeHtml(r)}</td>
                <td><span title="${escapeHtml(r.createdAt)}">${escapeHtml(relTime)}</span></td>
                <td>${escapeHtml(r.format.toUpperCase())}</td>
                <td>${downloadCell}</td>
              </tr>
            `;
          })
          .join("");

  return `
    <div class="compliance-table-header">
      <div class="compliance-table-title">Past Reports<span class="compliance-table-note"> (newest 50)</span></div>
      <button class="action-btn" id="complianceRefreshBtn" title="Refresh">&#8635;</button>
    </div>
    <div class="compliance-table-container">
      <table class="compliance-table">
        <thead>
          <tr>
            <th>Type</th>
            <th>Date Range</th>
            <th>Status</th>
            <th>Created</th>
            <th>Format</th>
            <th>Download</th>
          </tr>
        </thead>
        <tbody>
          ${tableRows}
        </tbody>
      </table>
    </div>
  `;
}
