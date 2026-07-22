/**
 * RunsTabHtml — HTML generator for the Runs dashboard tab.
 *
 * Follows the established tab-module contract:
 *   getRunsTabHtml()    — returns HTML string
 *   getRunsTabScript()  — returns JS string (event handlers)
 *   getRunsTabStyles()  — returns CSS string (scoped)
 *
 * Modeled after AuditTabHtml.ts (table + filters + drill-down + pagination).
 *
 * @see Issue #3319 — Add Runs Tab to Pipeline Dashboard
 */

import { escapeHtml, formatRelativeTime, formatDuration } from "../DashboardComponents";
import type { RunsListData } from "../DashboardState";
import type { RunsEntry, RunsStageEntry } from "../../../services/IpcClientBase";

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Render the full runs tab panel HTML.
 * @param data     Current runs data bundle; undefined triggers loading state.
 * @param _nonce   CSP nonce (reserved for future inline scripts; unused here).
 */
export function getRunsTabHtml(data: RunsListData | undefined, _nonce?: string): string {
  if (!data) {
    return getRunsLoadingHtml();
  }
  if (!data.hasAccess) {
    return getRunsNoAccessHtml();
  }
  if (data.isLoading) {
    return getRunsLoadingHtml();
  }

  return `
    <div class="runs-tab">
      ${data.errorMessage ? `<div class="runs-error-banner">${escapeHtml(data.errorMessage)}</div>` : ""}
      ${getRunsFiltersHtml(data)}
      ${getRunsTableHtml(data.entries)}
      ${getRunsPaginationHtml(data)}
    </div>
  `;
}

/**
 * JS event handlers for the runs tab.
 * Uses event delegation on the tab panel; vscode.postMessage() for IPC.
 */
export function getRunsTabScript(): string {
  return `
    (function() {
      var runsPanel = document.getElementById('tab-panel-runs');
      if (!runsPanel) return;

      runsPanel.addEventListener('click', function(e) {
        // Row expand/collapse
        var row = e.target.closest('[data-action="toggle-runs-detail"]');
        if (row) {
          var idx = row.getAttribute('data-index');
          var detail = document.getElementById('runs-detail-' + idx);
          if (detail) { detail.classList.toggle('expanded'); }
          return;
        }

        // Pagination
        var prevBtn = e.target.closest('#runsPrevPage');
        if (prevBtn) {
          var page = parseInt(prevBtn.getAttribute('data-page') || '0', 10);
          vscode.postMessage({ type: 'runsPageChange', page: page });
          return;
        }
        var nextBtn = e.target.closest('#runsNextPage');
        if (nextBtn) {
          var page = parseInt(nextBtn.getAttribute('data-page') || '0', 10);
          vscode.postMessage({ type: 'runsPageChange', page: page });
          return;
        }

        // Export CSV
        var exportBtn = e.target.closest('#runsExportCsv');
        if (exportBtn) {
          var filters = collectRunsFilters();
          vscode.postMessage({ type: 'runsExportCsv', filters: filters });
          return;
        }

        // Refresh
        var refreshBtn = e.target.closest('#runsRefreshBtn');
        if (refreshBtn) {
          vscode.postMessage({ type: 'runsRefresh' });
          return;
        }

        // Reset filters
        var resetBtn = e.target.closest('#runsResetFilters');
        if (resetBtn) {
          var df = document.getElementById('runsDateFrom'); if (df) df.value = '';
          var dt = document.getElementById('runsDateTo'); if (dt) dt.value = '';
          var of = document.getElementById('runsOutcomeFilter'); if (of) of.value = '';
          var bf = document.getElementById('runsBranchFilter'); if (bf) bf.value = '';
          vscode.postMessage({ type: 'runsResetFilters' });
          return;
        }
      });

      // Apply filters button
      var applyBtn = document.getElementById('runsApplyFilters');
      if (applyBtn) {
        applyBtn.addEventListener('click', function() {
          var filters = collectRunsFilters();
          vscode.postMessage({ type: 'runsFilter', filters: filters });
        });
      }

      function collectRunsFilters() {
        return {
          dateFrom: document.getElementById('runsDateFrom')?.value || '',
          dateTo: document.getElementById('runsDateTo')?.value || '',
          outcomeFilter: document.getElementById('runsOutcomeFilter')?.value || '',
          branchFilter: document.getElementById('runsBranchFilter')?.value || '',
        };
      }
    })();
  `;
}

/**
 * Scoped CSS for the runs tab panel.
 */
export function getRunsTabStyles(): string {
  return `
    /* Runs Tab Styles (Issue #3319) */
    .runs-tab {
      padding: var(--spacing-sm) 0;
    }

    .runs-error-banner {
      background: rgba(255, 99, 132, 0.15);
      border: 1px solid rgba(255, 99, 132, 0.4);
      border-radius: var(--border-radius);
      color: rgba(255, 99, 132, 1);
      font-size: 0.85em;
      margin-bottom: var(--spacing-md);
      padding: var(--spacing-sm) var(--spacing-md);
    }

    .runs-empty-state {
      text-align: center;
      padding: 48px var(--spacing-md);
      color: var(--vscode-descriptionForeground);
    }

    .runs-empty-state .empty-icon {
      font-size: 3em;
      margin-bottom: var(--spacing-md);
    }

    .runs-loading {
      text-align: center;
      padding: 48px var(--spacing-md);
      color: var(--vscode-descriptionForeground);
    }

    .runs-no-access {
      text-align: center;
      padding: 48px var(--spacing-md);
      color: var(--vscode-descriptionForeground);
    }

    .runs-no-access .empty-icon {
      font-size: 3em;
      margin-bottom: var(--spacing-md);
    }

    /* Filters */
    .runs-filters {
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

    .runs-filter-group {
      display: flex;
      align-items: center;
      gap: var(--spacing-xs);
    }

    .runs-filter-label {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
      white-space: nowrap;
    }

    .runs-filter-input,
    .runs-filter-select {
      padding: 3px 6px;
      background: var(--vscode-input-background);
      color: var(--vscode-input-foreground);
      border: 1px solid var(--vscode-input-border);
      border-radius: var(--border-radius);
      font-size: 0.82em;
    }

    .runs-filter-select {
      background: var(--vscode-dropdown-background);
      color: var(--vscode-dropdown-foreground);
      border-color: var(--vscode-dropdown-border);
    }

    .runs-filters-actions {
      display: flex;
      gap: var(--spacing-sm);
      margin-left: auto;
    }

    /* Table */
    .runs-table-container {
      overflow-x: auto;
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      margin-bottom: var(--spacing-md);
    }

    .runs-table {
      width: 100%;
      border-collapse: collapse;
      font-size: 0.88em;
    }

    .runs-table th {
      text-align: left;
      padding: var(--spacing-sm);
      border-bottom: 2px solid var(--vscode-panel-border);
      font-weight: 600;
      white-space: nowrap;
    }

    .runs-table td {
      padding: var(--spacing-sm);
      border-bottom: 1px solid var(--vscode-panel-border);
      vertical-align: top;
    }

    .runs-table tr:hover {
      background: var(--vscode-list-hoverBackground);
    }

    .runs-row {
      cursor: pointer;
    }

    .runs-timestamp {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
      white-space: nowrap;
    }

    .runs-branch {
      font-family: var(--vscode-editor-font-family);
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
      max-width: 180px;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    /* Outcome badge colors */
    .runs-outcome-badge {
      display: inline-block;
      padding: 1px 6px;
      border-radius: var(--border-radius);
      font-size: 0.78em;
      font-weight: 600;
    }

    .runs-outcome-productive,
    .runs-outcome-success {
      background: rgba(75, 192, 75, 0.2);
      color: rgba(75, 192, 75, 1);
    }

    .runs-outcome-failed {
      background: rgba(255, 99, 132, 0.2);
      color: rgba(255, 99, 132, 1);
    }

    .runs-outcome-cancelled {
      background: rgba(255, 206, 86, 0.2);
      color: rgba(255, 206, 86, 1);
    }

    .runs-outcome-verify-and-close {
      background: rgba(54, 162, 235, 0.2);
      color: rgba(54, 162, 235, 1);
    }

    .runs-outcome-default {
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
    }

    /* Detail panel */
    .runs-detail-row td {
      padding: 0;
    }

    .runs-detail-panel {
      display: none;
      padding: var(--spacing-md);
      background: var(--vscode-textCodeBlock-background);
      border-top: 1px solid var(--vscode-panel-border);
    }

    .runs-detail-panel.expanded {
      display: block;
    }

    .runs-stages-table {
      width: 100%;
      border-collapse: collapse;
      font-size: 0.82em;
      margin-top: var(--spacing-sm);
    }

    .runs-stages-table th,
    .runs-stages-table td {
      padding: 4px 8px;
      text-align: left;
      border-bottom: 1px solid var(--vscode-panel-border);
    }

    .runs-stages-table th {
      font-weight: 600;
      border-bottom: 2px solid var(--vscode-panel-border);
    }

    /* Pagination */
    .runs-pagination {
      display: flex;
      align-items: center;
      justify-content: center;
      gap: var(--spacing-md);
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }

    .runs-pagination button:disabled {
      opacity: 0.4;
      cursor: default;
    }
  `;
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

function getRunsLoadingHtml(): string {
  return `
    <div class="runs-loading">
      <div class="empty-icon">⏳</div>
      <p>Loading pipeline runs…</p>
    </div>
  `;
}

function getRunsNoAccessHtml(): string {
  return `
    <div class="runs-no-access">
      <div class="empty-icon">🔒</div>
      <h3>No Access</h3>
      <p>Connect to the platform to view pipeline run history.</p>
    </div>
  `;
}

function getRunsFiltersHtml(data: RunsListData): string {
  const { filters } = data;
  const outcomes = ["productive", "verify-and-close", "failed", "cancelled"];
  const outcomeOptions = [
    `<option value="">All Outcomes</option>`,
    ...outcomes.map(
      (o) =>
        `<option value="${escapeHtml(o)}" ${filters.outcomeFilter === o ? "selected" : ""}>${escapeHtml(o)}</option>`
    ),
  ].join("");

  return `
    <div class="runs-filters">
      <div class="runs-filter-group">
        <label class="runs-filter-label" for="runsDateFrom">From:</label>
        <input type="date" id="runsDateFrom" class="runs-filter-input"
          value="${escapeHtml(filters.dateFrom.substring(0, 10))}" />
      </div>
      <div class="runs-filter-group">
        <label class="runs-filter-label" for="runsDateTo">To:</label>
        <input type="date" id="runsDateTo" class="runs-filter-input"
          value="${escapeHtml(filters.dateTo.substring(0, 10))}" />
      </div>
      <div class="runs-filter-group">
        <label class="runs-filter-label" for="runsOutcomeFilter">Outcome:</label>
        <select id="runsOutcomeFilter" class="runs-filter-select">
          ${outcomeOptions}
        </select>
      </div>
      <div class="runs-filter-group">
        <label class="runs-filter-label" for="runsBranchFilter">Branch:</label>
        <input type="text" id="runsBranchFilter" class="runs-filter-input"
          placeholder="Branch name"
          value="${escapeHtml(filters.branchFilter)}" />
      </div>
      <div class="runs-filters-actions">
        <button class="action-btn" id="runsApplyFilters">Apply</button>
        <button class="action-btn" id="runsResetFilters">Reset</button>
        <button class="action-btn" id="runsExportCsv" title="Export filtered results as CSV">Export CSV</button>
        <button class="action-btn" id="runsRefreshBtn" title="Force refresh">&#8635;</button>
      </div>
    </div>
  `;
}

function getOutcomeBadgeClass(outcome: string): string {
  switch (outcome) {
    case "productive":
    case "success":
      return "runs-outcome-productive";
    case "failed":
      return "runs-outcome-failed";
    case "cancelled":
      return "runs-outcome-cancelled";
    case "verify-and-close":
      return "runs-outcome-verify-and-close";
    default:
      return "runs-outcome-default";
  }
}

function getRunsStagesHtml(stages: RunsStageEntry[]): string {
  if (!stages || stages.length === 0) {
    return `<p style="margin:0; font-size:0.85em; color: var(--vscode-descriptionForeground);">No stage detail available.</p>`;
  }

  const rows = stages
    .map(
      (s) => `
        <tr data-stage-name="${escapeHtml(s.name)}" data-stage-status="${s.failure_category ? "failed" : "completed"}">
          <td>${escapeHtml(s.name)}</td>
          <td>${escapeHtml(s.model)}</td>
          <td data-stage-duration>${formatDuration(s.duration_ms)}</td>
          <td>${s.input_tokens.toLocaleString()}</td>
          <td>${s.output_tokens.toLocaleString()}</td>
          <td>$${escapeHtml(s.cost_usd)}</td>
          <td>${s.retry_count}</td>
          <td>${s.failure_category ? escapeHtml(s.failure_category) : "—"}</td>
        </tr>
      `
    )
    .join("");

  return `
    <table class="runs-stages-table">
      <thead>
        <tr>
          <th>Stage</th>
          <th>Model</th>
          <th>Duration</th>
          <th>Input Tokens</th>
          <th>Output Tokens</th>
          <th>Cost</th>
          <th>Retries</th>
          <th>Failure</th>
        </tr>
      </thead>
      <tbody>${rows}</tbody>
    </table>
  `;
}

function getRunsTableHtml(entries: RunsEntry[]): string {
  if (entries.length === 0) {
    return `
      <div class="runs-empty-state">
        <div class="empty-icon">🏃</div>
        <h3>No Runs Found</h3>
        <p>No pipeline runs match the selected filters.</p>
        <p style="font-size:0.85em; color: var(--vscode-descriptionForeground);">
          Adjust your date range or outcome filter, or connect to the platform.
        </p>
      </div>
    `;
  }

  const rows = entries
    .map((entry, index) => {
      const relTime = formatRelativeTime(new Date(entry.started_at));
      const badgeClass = getOutcomeBadgeClass(entry.outcome);
      const durationStr = formatDuration(entry.duration_ms);
      const costStr = entry.total_cost_usd ? `$${escapeHtml(entry.total_cost_usd)}` : "—";

      return `
        <tr class="runs-row" data-action="toggle-runs-detail" data-index="${index}">
          <td><span class="runs-timestamp" title="${escapeHtml(entry.started_at)}">${escapeHtml(relTime)}</span></td>
          <td>#${entry.issue_number}</td>
          <td title="${escapeHtml(entry.title)}">${escapeHtml(entry.title.length > 60 ? entry.title.substring(0, 60) + "…" : entry.title)}</td>
          <td><span class="runs-branch" title="${escapeHtml(entry.branch)}">${escapeHtml(entry.branch)}</span></td>
          <td><span class="runs-outcome-badge ${badgeClass}">${escapeHtml(entry.outcome)}</span></td>
          <td>${escapeHtml(durationStr)}</td>
          <td>${costStr}</td>
        </tr>
        <tr class="runs-detail-row">
          <td colspan="7">
            <div class="runs-detail-panel" id="runs-detail-${index}" data-run-detail-issue="${entry.issue_number}">
              ${getRunsStagesHtml(entry.stages ?? [])}
            </div>
          </td>
        </tr>
      `;
    })
    .join("");

  return `
    <div class="runs-table-container">
      <table class="runs-table">
        <thead>
          <tr>
            <th>Started</th>
            <th>Issue</th>
            <th>Title</th>
            <th>Branch</th>
            <th>Outcome</th>
            <th>Duration</th>
            <th>Cost</th>
          </tr>
        </thead>
        <tbody>
          ${rows}
        </tbody>
      </table>
    </div>
  `;
}

function getRunsPaginationHtml(data: RunsListData): string {
  const { pagination } = data;
  if (!pagination.hasMore && pagination.page === 0) {
    return "";
  }

  const hasPrev = pagination.page > 0;
  const hasNext = pagination.hasMore;
  const displayPage = pagination.page + 1;

  return `
    <div class="runs-pagination">
      <button class="action-btn" id="runsPrevPage"
        data-page="${pagination.page - 1}"
        ${!hasPrev ? "disabled" : ""}>
        &lsaquo; Prev
      </button>
      <span>Page ${displayPage}${pagination.totalCount > 0 ? ` &nbsp;&middot;&nbsp; ${pagination.totalCount} runs` : ""}</span>
      <button class="action-btn" id="runsNextPage"
        data-page="${pagination.page + 1}"
        ${!hasNext ? "disabled" : ""}>
        Next &rsaquo;
      </button>
    </div>
  `;
}
