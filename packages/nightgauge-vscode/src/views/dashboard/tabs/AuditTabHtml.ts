/**
 * AuditTabHtml — HTML generator for the Audit Trail dashboard tab.
 *
 * Follows the established tab-module contract:
 *   getAuditTabHtml()    — returns HTML string
 *   getAuditTabScript()  — returns JS string (event handlers)
 *   getAuditTabStyles()  — returns CSS string (scoped)
 *
 * Modeled after FirewallTabHtml.ts (table + filters + script pattern).
 *
 * @see Issue #1583 — Audit Log Viewer Dashboard Widget
 */

import { escapeHtml, formatRelativeTime } from "../DashboardComponents";
import { AUDIT_ACTIONS } from "@nightgauge/sdk";
import type { AuditLogData, AuditLogEntry, RetentionIntegrityData } from "../DashboardState";

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Render the full audit tab panel HTML.
 * @param data            Current audit data bundle; undefined triggers loading state.
 * @param retentionData   Retention & Integrity panel data; undefined hides the panel.
 * @param _nonce          CSP nonce (reserved for future inline scripts; unused here).
 */
export function getAuditTabHtml(
  data: AuditLogData | undefined,
  retentionData?: RetentionIntegrityData,
  _nonce?: string
): string {
  if (!data) {
    return getAuditLoadingHtml();
  }
  if (!data.hasAccess) {
    return getAuditNoAccessHtml();
  }
  if (data.isLoading) {
    return getAuditLoadingHtml();
  }

  return `
    <div class="audit-tab">
      <div class="audit-tab-header">
        <span id="stream-status-badge" class="stream-badge stream-badge--disconnected" title="Real-time SSE connection status">● offline</span>
      </div>
      ${data.isLocalFallback ? getAuditLocalBannerHtml(data) : ""}
      ${data.errorMessage && !data.isLocalFallback ? `<div class="audit-error-banner">${escapeHtml(data.errorMessage)}</div>` : ""}
      ${getAuditFiltersHtml(data)}
      <div id="audit-live-entries-list"></div>
      ${getAuditTableHtml(data.entries, data.isLocalFallback)}
      ${getAuditPaginationHtml(data)}
      ${data.isLocalFallback ? getAuditLocalOnlyNoticeHtml() : ""}
      ${getRetentionIntegrityPanelHtml(retentionData)}
    </div>
  `;
}

/**
 * JS event handlers for the audit tab (including retention & integrity panel).
 * Uses event delegation on the tab panel; vscode.postMessage() for IPC.
 */
export function getAuditTabScript(): string {
  return `
    (function() {
      var auditPanel = document.getElementById('tab-panel-audit');
      if (!auditPanel) return;

      // Row toggle (expand/collapse detail)
      auditPanel.addEventListener('click', function(e) {
        var row = e.target.closest('[data-action="toggle-audit-detail"]');
        if (row) {
          var idx = row.getAttribute('data-index');
          var detail = document.getElementById('audit-detail-' + idx);
          if (detail) { detail.classList.toggle('expanded'); }
          return;
        }

        // Pagination
        var prevBtn = e.target.closest('#auditPrevPage');
        if (prevBtn) {
          var page = parseInt(prevBtn.getAttribute('data-page') || '0', 10);
          vscode.postMessage({ type: 'auditPageChange', page: page });
          return;
        }
        var nextBtn = e.target.closest('#auditNextPage');
        if (nextBtn) {
          var page = parseInt(nextBtn.getAttribute('data-page') || '0', 10);
          vscode.postMessage({ type: 'auditPageChange', page: page });
          return;
        }

        // Retry platform connection (local-mode banner button)
        var retryBtn = e.target.closest('#auditRetryBtn');
        if (retryBtn) {
          retryBtn.disabled = true;
          retryBtn.textContent = 'Retrying…';
          setTimeout(function() {
            if (retryBtn) { retryBtn.disabled = false; retryBtn.textContent = 'Retry'; }
          }, 5000);
          vscode.postMessage({ type: 'auditRetry' });
          return;
        }

        // Export CSV
        var exportBtn = e.target.closest('#auditExportCsv');
        if (exportBtn) {
          var filters = collectAuditFilters();
          vscode.postMessage({ type: 'auditExportCsv', filters: filters });
          return;
        }

        // Refresh
        var refreshBtn = e.target.closest('#auditRefreshBtn');
        if (refreshBtn) {
          vscode.postMessage({ type: 'auditRefresh' });
          return;
        }

        // Reset filters
        var resetBtn = e.target.closest('#auditResetFilters');
        if (resetBtn) {
          var df = document.getElementById('auditDateFrom'); if (df) df.value = '';
          var dt = document.getElementById('auditDateTo'); if (dt) dt.value = '';
          var af = document.getElementById('auditActionFilter'); if (af) af.value = '';
          var uf = document.getElementById('auditUserFilter'); if (uf) uf.value = '';
          vscode.postMessage({ type: 'auditResetFilters' });
          return;
        }
      });

      // Apply filters button
      var applyBtn = document.getElementById('auditApplyFilters');
      if (applyBtn) {
        applyBtn.addEventListener('click', function() {
          var filters = collectAuditFilters();
          vscode.postMessage({ type: 'auditFilter', filters: filters });
        });
      }

      function collectAuditFilters() {
        return {
          dateFrom: document.getElementById('auditDateFrom')?.value || '',
          dateTo: document.getElementById('auditDateTo')?.value || '',
          actionFilter: document.getElementById('auditActionFilter')?.value || '',
          userFilter: document.getElementById('auditUserFilter')?.value || '',
        };
      }

      // Real-time SSE event handlers (Issue #3321)
      window.addEventListener('message', function(event) {
        var msg = event.data;

        if (msg.type === 'streamStatusChanged') {
          var badge = document.getElementById('stream-status-badge');
          if (badge) {
            badge.className = 'stream-badge stream-badge--' + msg.status;
            badge.textContent = msg.label || (
              msg.status === 'connected' ? '● live' :
              msg.status === 'reconnecting' ? '↻ reconnecting' : '● offline'
            );
          }
        }

        if (msg.type === 'auditLiveEvent' && msg.entry) {
          prependLiveAuditEntry(msg.entry);
        }
      });

      function prependLiveAuditEntry(entry) {
        var list = document.getElementById('audit-live-entries-list');
        if (!list) return;

        var relTime = entry.timestamp ? new Date(entry.timestamp).toLocaleTimeString() : 'just now';
        var status = entry.status || 'success';
        var action = escapeHtmlStr(entry.action || '');
        var resource = [entry.resourceType, entry.resourceId].filter(Boolean).map(escapeHtmlStr).join('/');
        var user = escapeHtmlStr(entry.userEmail || entry.userId || '');

        var row = document.createElement('div');
        row.className = 'audit-live-row';
        row.innerHTML =
          '<span class="audit-live-badge">live</span>' +
          '<span class="audit-timestamp" title="' + escapeHtmlStr(entry.timestamp || '') + '">' + escapeHtmlStr(relTime) + '</span>' +
          '<span class="audit-live-user">' + user + '</span>' +
          '<span class="audit-action-badge">' + action + '</span>' +
          (resource ? '<span class="audit-action-badge">' + resource + '</span>' : '') +
          '<span class="audit-status-badge audit-status-' + escapeHtmlStr(status) + '">' + escapeHtmlStr(status) + '</span>';

        list.insertBefore(row, list.firstChild);

        // Trim to avoid DOM bloat — keep at most 50 live rows
        var rows = list.querySelectorAll('.audit-live-row');
        if (rows.length > 50) {
          rows[rows.length - 1].remove();
        }
      }

      function escapeHtmlStr(str) {
        return String(str)
          .replace(/&/g, '&amp;')
          .replace(/</g, '&lt;')
          .replace(/>/g, '&gt;')
          .replace(/"/g, '&quot;')
          .replace(/'/g, '&#39;');
      }

      // Retention & Integrity panel handlers (#3323)
      var retentionSaveBtn = document.getElementById('retentionSaveBtn');
      if (retentionSaveBtn) {
        retentionSaveBtn.addEventListener('click', function() {
          var input = document.getElementById('retentionDaysInput');
          if (!input) return;
          var raw = parseInt(input.value, 10);
          if (isNaN(raw) || raw < 1 || raw > 3650) {
            input.setCustomValidity('Retention period must be between 1 and 3650 days.');
            input.reportValidity();
            return;
          }
          input.setCustomValidity('');
          vscode.postMessage({ type: 'retentionUpdate', retentionDays: raw });
        });
      }

      auditPanel.addEventListener('click', function(e) {
        var verifyBtn = e.target.closest('[data-action="verify-integrity"]');
        if (verifyBtn) {
          var windowDays = parseInt(verifyBtn.getAttribute('data-window') || '30', 10);
          var verifyBtns = auditPanel.querySelectorAll('[data-action="verify-integrity"]');
          verifyBtns.forEach(function(b) { b.disabled = true; });
          var spinner = document.getElementById('integrity-spinner');
          if (spinner) spinner.style.display = 'inline';
          vscode.postMessage({ type: 'retentionVerifyIntegrity', windowDays: windowDays });
          return;
        }

        var retentionRefreshBtn = e.target.closest('#retentionRefreshBtn');
        if (retentionRefreshBtn) {
          vscode.postMessage({ type: 'retentionRefresh' });
          return;
        }
      });
    })();
  `;
}

/**
 * Scoped CSS for the audit tab panel.
 */
export function getAuditTabStyles(): string {
  return `
    /* Audit Trail Tab Styles (Issue #1583) */
    .audit-tab {
      padding: var(--spacing-sm) 0;
    }

    .audit-error-banner {
      background: rgba(255, 99, 132, 0.15);
      border: 1px solid rgba(255, 99, 132, 0.4);
      border-radius: var(--border-radius);
      color: rgba(255, 99, 132, 1);
      font-size: 0.85em;
      margin-bottom: var(--spacing-md);
      padding: var(--spacing-sm) var(--spacing-md);
    }

    .audit-local-banner {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      background: rgba(255, 200, 0, 0.1);
      border: 1px solid #c8a000;
      border-radius: var(--border-radius);
      color: #c8a000;
      font-size: 0.85em;
      margin-bottom: var(--spacing-md);
      padding: var(--spacing-sm) var(--spacing-md);
    }

    .audit-local-banner .audit-local-icon {
      flex-shrink: 0;
    }

    .audit-local-banner .audit-local-label {
      flex: 1;
    }

    .audit-local-notice {
      background: rgba(255, 200, 0, 0.07);
      border: 1px solid rgba(200, 160, 0, 0.3);
      border-radius: var(--border-radius);
      color: var(--vscode-descriptionForeground);
      font-size: 0.82em;
      margin-top: var(--spacing-md);
      padding: var(--spacing-sm) var(--spacing-md);
    }

    .audit-empty-state {
      text-align: center;
      padding: 48px var(--spacing-md);
      color: var(--vscode-descriptionForeground);
    }

    .audit-empty-state .empty-icon {
      font-size: 3em;
      margin-bottom: var(--spacing-md);
    }

    .audit-loading {
      text-align: center;
      padding: 48px var(--spacing-md);
      color: var(--vscode-descriptionForeground);
    }

    .audit-no-access {
      text-align: center;
      padding: 48px var(--spacing-md);
      color: var(--vscode-descriptionForeground);
    }

    .audit-no-access .empty-icon {
      font-size: 3em;
      margin-bottom: var(--spacing-md);
    }

    /* Filters */
    .audit-filters {
      display: flex;
      flex-wrap: wrap;
      gap: var(--spacing-sm);
      align-items: center;
      margin-bottom: var(--spacing-md);
      padding: var(--spacing-sm) var(--spacing-md);
      background: var(--vscode-editor-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
    }

    .audit-filter-group {
      display: flex;
      align-items: center;
      gap: var(--spacing-xs);
    }

    .audit-filter-label {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
      white-space: nowrap;
    }

    .audit-filter-input,
    .audit-filter-select {
      padding: 3px 6px;
      background: var(--vscode-input-background);
      color: var(--vscode-input-foreground);
      border: 1px solid var(--vscode-input-border);
      border-radius: var(--border-radius);
      font-size: 0.82em;
    }

    .audit-filter-select {
      background: var(--vscode-dropdown-background);
      color: var(--vscode-dropdown-foreground);
      border-color: var(--vscode-dropdown-border);
    }

    .audit-filters-actions {
      display: flex;
      gap: var(--spacing-sm);
      margin-left: auto;
    }

    /* Table */
    .audit-table-container {
      overflow-x: auto;
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      margin-bottom: var(--spacing-md);
    }

    .audit-table {
      width: 100%;
      border-collapse: collapse;
      font-size: 0.88em;
    }

    .audit-table th {
      text-align: left;
      padding: var(--spacing-sm);
      border-bottom: 2px solid var(--vscode-panel-border);
      font-weight: 600;
      white-space: nowrap;
    }

    .audit-table td {
      padding: var(--spacing-sm);
      border-bottom: 1px solid var(--vscode-panel-border);
      vertical-align: top;
    }

    .audit-table tr:hover {
      background: var(--vscode-list-hoverBackground);
    }

    .audit-event-row {
      cursor: pointer;
    }

    .audit-timestamp {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
      white-space: nowrap;
    }

    .audit-action-badge {
      display: inline-block;
      padding: 1px 6px;
      border-radius: var(--border-radius);
      font-size: 0.78em;
      font-family: var(--vscode-editor-font-family);
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
    }

    .audit-status-badge {
      display: inline-block;
      padding: 1px 6px;
      border-radius: var(--border-radius);
      font-size: 0.78em;
      font-weight: 600;
    }

    .audit-status-success {
      background: rgba(75, 192, 75, 0.2);
      color: rgba(75, 192, 75, 1);
    }

    .audit-status-failure {
      background: rgba(255, 99, 132, 0.2);
      color: rgba(255, 99, 132, 1);
    }

    .audit-status-pending {
      background: rgba(255, 206, 86, 0.2);
      color: rgba(255, 206, 86, 1);
    }

    .audit-detail-row td {
      padding: 0;
    }

    .audit-detail-panel {
      display: none;
      padding: var(--spacing-md);
      background: var(--vscode-textCodeBlock-background);
      border-top: 1px solid var(--vscode-panel-border);
    }

    .audit-detail-panel.expanded {
      display: block;
    }

    .audit-detail-panel pre {
      font-family: var(--vscode-editor-font-family);
      font-size: 0.82em;
      white-space: pre-wrap;
      word-break: break-all;
      margin: 0;
    }

    .audit-cost-note {
      margin-top: var(--spacing-xs);
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
    }

    /* Pagination */
    .audit-pagination {
      display: flex;
      align-items: center;
      justify-content: center;
      gap: var(--spacing-md);
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }

    .audit-pagination button:disabled {
      opacity: 0.4;
      cursor: default;
    }

    /* Stream status badge (Issue #3321) */
    .audit-tab-header {
      display: flex;
      align-items: center;
      justify-content: flex-end;
      margin-bottom: var(--spacing-sm);
    }

    .stream-badge {
      display: inline-block;
      padding: 2px 8px;
      border-radius: 12px;
      font-size: 0.78em;
      font-weight: 600;
      letter-spacing: 0.02em;
      border: 1px solid transparent;
    }

    .stream-badge--connected {
      background: rgba(75, 192, 75, 0.15);
      border-color: rgba(75, 192, 75, 0.4);
      color: var(--vscode-testing-iconPassed, rgba(75, 192, 75, 1));
    }

    .stream-badge--reconnecting {
      background: rgba(255, 206, 86, 0.15);
      border-color: rgba(255, 206, 86, 0.4);
      color: var(--vscode-charts-yellow, rgba(200, 160, 0, 1));
    }

    .stream-badge--disconnected {
      background: var(--vscode-editor-background);
      border-color: var(--vscode-panel-border);
      color: var(--vscode-descriptionForeground);
    }

    /* Live audit rows prepended above the static table (Issue #3321) */
    #audit-live-entries-list {
      margin-bottom: var(--spacing-sm);
    }

    .audit-live-row {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      padding: var(--spacing-xs) var(--spacing-sm);
      border-left: 2px solid var(--vscode-charts-green, rgba(75, 192, 75, 1));
      background: rgba(75, 192, 75, 0.05);
      border-radius: 0 var(--border-radius) var(--border-radius) 0;
      margin-bottom: 2px;
      font-size: 0.85em;
      animation: auditLiveFadeIn 0.3s ease-in;
    }

    @keyframes auditLiveFadeIn {
      from { opacity: 0; transform: translateY(-4px); }
      to   { opacity: 1; transform: translateY(0); }
    }

    .audit-live-badge {
      display: inline-block;
      padding: 1px 5px;
      border-radius: var(--border-radius);
      font-size: 0.75em;
      font-weight: 700;
      text-transform: uppercase;
      letter-spacing: 0.04em;
      background: rgba(75, 192, 75, 0.25);
      color: var(--vscode-charts-green, rgba(75, 192, 75, 1));
      flex-shrink: 0;
    }

    .audit-live-user {
      color: var(--vscode-descriptionForeground);
      max-width: 120px;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    /* Retention & Integrity panel (#3323) */
    .retention-integrity-panel {
      margin-top: var(--spacing-lg, 24px);
      padding: var(--spacing-md);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      background: var(--vscode-editor-background);
    }

    .retention-integrity-panel h3 {
      margin: 0 0 var(--spacing-md) 0;
      font-size: 1em;
      font-weight: 600;
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
    }

    .retention-integrity-panel h3 .panel-refresh-btn {
      margin-left: auto;
      background: none;
      border: none;
      cursor: pointer;
      color: var(--vscode-descriptionForeground);
      font-size: 1em;
      padding: 2px 4px;
    }

    .retention-card,
    .integrity-card {
      padding: var(--spacing-sm) var(--spacing-md);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      background: var(--vscode-sideBar-background, var(--vscode-editor-background));
      margin-bottom: var(--spacing-sm);
    }

    .retention-card label,
    .integrity-card label {
      display: block;
      font-size: 0.85em;
      font-weight: 600;
      color: var(--vscode-descriptionForeground);
      margin-bottom: var(--spacing-xs, 4px);
    }

    .retention-edit-row {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
    }

    .retention-edit-row input[type="number"] {
      width: 80px;
      padding: 3px 6px;
      background: var(--vscode-input-background);
      color: var(--vscode-input-foreground);
      border: 1px solid var(--vscode-input-border);
      border-radius: var(--border-radius);
      font-size: 0.9em;
    }

    .retention-last-updated {
      display: block;
      font-size: 0.78em;
      color: var(--vscode-descriptionForeground);
      margin-top: 4px;
    }

    .integrity-window-row {
      display: flex;
      gap: var(--spacing-sm);
      flex-wrap: wrap;
      margin-top: var(--spacing-xs, 4px);
    }

    .integrity-spinner {
      display: none;
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
      margin-top: var(--spacing-xs, 4px);
    }

    .integrity-result {
      margin-top: var(--spacing-sm);
      font-size: 0.88em;
    }

    .integrity-result-valid {
      display: inline-flex;
      align-items: center;
      gap: 4px;
      padding: 2px 8px;
      border-radius: var(--border-radius);
      background: rgba(75, 192, 75, 0.15);
      border: 1px solid rgba(75, 192, 75, 0.4);
      color: var(--vscode-testing-iconPassed, rgba(75, 192, 75, 1));
      font-weight: 600;
      font-size: 0.85em;
    }

    .integrity-result-invalid {
      display: inline-flex;
      align-items: center;
      gap: 4px;
      padding: 2px 8px;
      border-radius: var(--border-radius);
      background: rgba(255, 99, 132, 0.15);
      border: 1px solid rgba(255, 99, 132, 0.4);
      color: rgba(255, 99, 132, 1);
      font-weight: 600;
      font-size: 0.85em;
    }

    .retention-no-access {
      margin-top: var(--spacing-lg, 24px);
      padding: var(--spacing-md);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      background: var(--vscode-editor-background);
      color: var(--vscode-descriptionForeground);
      font-size: 0.88em;
      text-align: center;
    }
  `;
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

function getRetentionIntegrityPanelHtml(data: RetentionIntegrityData | undefined): string {
  if (!data) {
    return "";
  }

  if (!data.hasAccess) {
    return `
      <div class="retention-no-access">
        <span>🔒 Retention &amp; Integrity controls require an Enterprise plan.</span>
      </div>
    `;
  }

  if (data.isLoading) {
    return `
      <div class="retention-integrity-panel">
        <h3>Retention &amp; Integrity</h3>
        <p style="color:var(--vscode-descriptionForeground);font-size:0.88em;">Loading…</p>
      </div>
    `;
  }

  const retentionDays = data.retentionConfig?.retentionDays ?? 730;
  const updatedAt = data.retentionConfig?.updatedAt
    ? `Last updated: ${escapeHtml(data.retentionConfig.updatedAt)}`
    : "";

  let integrityHtml = "";
  if (data.integrityResult) {
    const { valid, checkedCount, windowDays, message, checkedAt } = data.integrityResult;
    const badgeClass = valid ? "integrity-result-valid" : "integrity-result-invalid";
    const badgeLabel = valid ? "✓ Valid" : "✗ Invalid";
    integrityHtml = `
      <div class="integrity-result">
        <span class="${badgeClass}">${badgeLabel}</span>
        &nbsp;${escapeHtml(checkedCount.toString())} entries checked over last ${escapeHtml(windowDays.toString())} days
        — ${escapeHtml(message)}
        <span class="retention-last-updated">Checked at: ${escapeHtml(checkedAt)}</span>
      </div>
    `;
  }

  const verifyDisabled = data.isVerifying ? "disabled" : "";
  const spinnerStyle = data.isVerifying ? "display:inline" : "display:none";
  const errorHtml = data.errorMessage
    ? `<div class="audit-error-banner" style="margin-top:var(--spacing-sm)">${escapeHtml(data.errorMessage)}</div>`
    : "";

  return `
    <section class="retention-integrity-panel">
      <h3>
        Retention &amp; Integrity
        <button class="action-btn panel-refresh-btn" id="retentionRefreshBtn" title="Refresh">&#8635;</button>
      </h3>
      ${errorHtml}

      <div class="retention-card">
        <label for="retentionDaysInput">Audit retention period</label>
        <div class="retention-edit-row">
          <input type="number" id="retentionDaysInput" value="${retentionDays}" min="1" max="3650" />
          <span>days</span>
          <button class="action-btn" id="retentionSaveBtn">Save</button>
        </div>
        ${updatedAt ? `<span class="retention-last-updated">${escapeHtml(updatedAt)}</span>` : ""}
      </div>

      <div class="integrity-card">
        <label>Verify audit log integrity</label>
        <div class="integrity-window-row">
          <button class="action-btn" data-action="verify-integrity" data-window="30" ${verifyDisabled}>Last 30 days</button>
          <button class="action-btn" data-action="verify-integrity" data-window="90" ${verifyDisabled}>Last 90 days</button>
          <button class="action-btn" data-action="verify-integrity" data-window="365" ${verifyDisabled}>Last 365 days</button>
        </div>
        <span id="integrity-spinner" class="integrity-spinner" style="${spinnerStyle}">⏳ Verifying…</span>
        ${integrityHtml}
      </div>
    </section>
  `;
}

function getAuditLoadingHtml(): string {
  return `
    <div class="audit-loading">
      <div class="empty-icon">⏳</div>
      <p>Loading audit events…</p>
    </div>
  `;
}

function getAuditNoAccessHtml(): string {
  return `
    <div class="audit-no-access">
      <div class="empty-icon">🔒</div>
      <h3>No Access</h3>
      <p>Connect to the platform and ensure you have audit read permissions to view events.</p>
    </div>
  `;
}

function getAuditLocalBannerHtml(data: AuditLogData): string {
  const label = escapeHtml(data.localDataLabel ?? "Showing local telemetry — platform unreachable");
  return `
    <div class="audit-local-banner">
      <span class="audit-local-icon">⚠️</span>
      <span class="audit-local-label">${label}</span>
      <button class="action-btn" id="auditRetryBtn">Retry</button>
    </div>
  `;
}

function getAuditLocalOnlyNoticeHtml(): string {
  return `
    <div class="audit-local-notice">
      Export CSV, compliance reports, and advanced search require platform access.
    </div>
  `;
}

function getAuditFiltersHtml(data: AuditLogData): string {
  const { filters } = data;

  const actionOptions = [
    `<option value="">All Actions</option>`,
    ...AUDIT_ACTIONS.map(
      (a) =>
        `<option value="${escapeHtml(a)}" ${filters.actionFilter === a ? "selected" : ""}>${escapeHtml(a)}</option>`
    ),
  ].join("");

  return `
    <div class="audit-filters">
      <div class="audit-filter-group">
        <label class="audit-filter-label" for="auditDateFrom">From:</label>
        <input type="date" id="auditDateFrom" class="audit-filter-input"
          value="${escapeHtml(filters.dateFrom.substring(0, 10))}" />
      </div>
      <div class="audit-filter-group">
        <label class="audit-filter-label" for="auditDateTo">To:</label>
        <input type="date" id="auditDateTo" class="audit-filter-input"
          value="${escapeHtml(filters.dateTo.substring(0, 10))}" />
      </div>
      <div class="audit-filter-group">
        <label class="audit-filter-label" for="auditActionFilter">Action:</label>
        <select id="auditActionFilter" class="audit-filter-select">
          ${actionOptions}
        </select>
      </div>
      <div class="audit-filter-group">
        <label class="audit-filter-label" for="auditUserFilter">User:</label>
        <input type="text" id="auditUserFilter" class="audit-filter-input"
          placeholder="User ID or email"
          value="${escapeHtml(filters.userFilter)}" />
      </div>
      <div class="audit-filters-actions">
        <button class="action-btn" id="auditApplyFilters">Apply</button>
        <button class="action-btn" id="auditResetFilters">Reset</button>
        ${!data.isLocalFallback ? `<button class="action-btn" id="auditExportCsv" title="Export filtered results as CSV">Export CSV</button>` : ""}
        <button class="action-btn" id="auditRefreshBtn" title="Force refresh">&#8635;</button>
      </div>
    </div>
  `;
}

function getAuditTableHtml(entries: AuditLogEntry[], isLocalFallback = false): string {
  if (entries.length === 0) {
    const emptyMsg = isLocalFallback
      ? "No local pipeline runs found for the selected date range."
      : "Connect to the platform or adjust your filters to see events.";
    return `
      <div class="audit-empty-state">
        <div class="empty-icon">📋</div>
        <h3>No ${isLocalFallback ? "Local" : "Audit"} Events</h3>
        <p>No events found for the selected filters and date range.</p>
        <p style="font-size:0.85em; color: var(--vscode-descriptionForeground);">
          ${escapeHtml(emptyMsg)}
        </p>
      </div>
    `;
  }

  const rows = entries
    .map((entry, index) => {
      const relTime = formatRelativeTime(new Date(entry.timestamp));
      const statusClass = `audit-status-${entry.status}`;
      const resource = [entry.resourceType, entry.resourceId]
        .filter((v): v is string => Boolean(v))
        .map(escapeHtml)
        .join("/");

      const detailData: Record<string, unknown> = {};
      if (entry.metadata) {
        detailData["metadata"] = entry.metadata;
      }
      if (entry.costUsd !== undefined) {
        detailData["costUsd"] = entry.costUsd;
      }
      detailData["userId"] = entry.userId;
      if (entry.userEmail) {
        detailData["userEmail"] = entry.userEmail;
      }

      return `
        <tr class="audit-event-row" data-action="toggle-audit-detail" data-index="${index}">
          <td><span class="audit-timestamp" title="${escapeHtml(entry.timestamp)}">${escapeHtml(relTime)}</span></td>
          <td>${escapeHtml(entry.userEmail ?? entry.userId)}</td>
          <td><span class="audit-action-badge">${escapeHtml(entry.action)}</span></td>
          <td>${resource ? `<span class="audit-action-badge">${resource}</span>` : ""}</td>
          <td><span class="audit-status-badge ${statusClass}">${escapeHtml(entry.status)}</span></td>
        </tr>
        <tr class="audit-detail-row">
          <td colspan="5">
            <div class="audit-detail-panel" id="audit-detail-${index}">
              <pre>${escapeHtml(JSON.stringify(detailData, null, 2))}</pre>
              ${entry.costUsd !== undefined ? `<p class="audit-cost-note">Cost: $${entry.costUsd.toFixed(4)} USD</p>` : ""}
            </div>
          </td>
        </tr>
      `;
    })
    .join("");

  return `
    <div class="audit-table-container">
      <table class="audit-table">
        <thead>
          <tr>
            <th>Timestamp</th>
            <th>User</th>
            <th>Action</th>
            <th>Resource</th>
            <th>Status</th>
          </tr>
        </thead>
        <tbody>
          ${rows}
        </tbody>
      </table>
    </div>
  `;
}

function getAuditPaginationHtml(data: AuditLogData): string {
  const { pagination } = data;
  if (pagination.totalCount <= pagination.pageSize) {
    return "";
  }

  const totalPages = Math.ceil(pagination.totalCount / pagination.pageSize);
  const currentPage = pagination.page + 1; // 1-based for display

  return `
    <div class="audit-pagination">
      <button class="action-btn" id="auditPrevPage"
        data-page="${pagination.page - 1}"
        ${!pagination.hasPrevPage ? "disabled" : ""}>
        &lsaquo; Prev
      </button>
      <span>Page ${currentPage} of ${totalPages} &nbsp;&middot;&nbsp; ${pagination.totalCount} events</span>
      <button class="action-btn" id="auditNextPage"
        data-page="${pagination.page + 1}"
        ${!pagination.hasNextPage ? "disabled" : ""}>
        Next &rsaquo;
      </button>
    </div>
  `;
}
