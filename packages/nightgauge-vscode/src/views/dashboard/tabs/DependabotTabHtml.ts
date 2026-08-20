import { escapeHtml } from "../DashboardComponents";
import type { DependabotPR, DependabotPRData } from "../../../services/DependabotPRService";
import type { DependabotTabState } from "../DashboardState";
import { getPlatformRetryButtonHtml, getPlatformFailureScript } from "./PlatformFailureHtml";

/**
 * Render the Dependencies tab. `state.fetchError` distinguishes a genuinely
 * empty PR list (a real success — the repo just has none open) from a fetch
 * that failed and left stale/empty data — previously both rendered the
 * identical "No open dependabot PRs found." message (#748).
 */
export function getDependabotTabHtml(state: DependabotTabState | null | undefined): string {
  if (state === undefined) {
    return getDependabotLoadingHtml();
  }
  if (state === null) {
    return getDependabotEmptyHtml();
  }
  if (state.fetchError) {
    return getDependabotFetchErrorHtml(state.fetchError);
  }
  if (state.data.prs.length === 0) {
    return getDependabotEmptyHtml();
  }
  return `
    <div class="dependabot-tab">
      ${getDependabotSummaryHtml(state.data)}
      ${getDependabotTableHtml(state.data.prs)}
    </div>
  `;
}

function getDependabotLoadingHtml(): string {
  return `<div class="dependabot-loading"><span class="loading-spinner"></span> Loading dependabot PRs…</div>`;
}

function getDependabotEmptyHtml(): string {
  return `<div class="dependabot-empty"><p>No open dependabot PRs found.</p></div>`;
}

/**
 * DependabotPRService wraps the GitHub PR list (not a platform.* IPC call),
 * so there is no typed `PlatformFailureKind` here — the raw error text is
 * the most honest thing available, shown as-is rather than reclassified.
 */
function getDependabotFetchErrorHtml(fetchError: string): string {
  return `
    <div class="dependabot-fetch-error">
      <div class="dependabot-fetch-error-icon">⚠️</div>
      <p class="dependabot-fetch-error-title">Could not load Dependabot PRs</p>
      <p class="dependabot-fetch-error-hint">${escapeHtml(fetchError)}</p>
      ${getPlatformRetryButtonHtml("dependabotRetryBtn", { type: "dependabotRefresh" })}
    </div>
  `;
}

function getDependabotSummaryHtml(data: DependabotPRData): string {
  return `
    <div class="dependabot-summary">
      <div class="summary-card">
        <span class="summary-value">${data.prs.length}</span>
        <span class="summary-label">Open PRs</span>
      </div>
      <div class="summary-card${data.securityCount > 0 ? " summary-card--danger" : ""}">
        <span class="summary-value">${data.securityCount}</span>
        <span class="summary-label">Security</span>
      </div>
      <div class="summary-card${data.staleCount > 0 ? " summary-card--warning" : ""}">
        <span class="summary-value">${data.staleCount}</span>
        <span class="summary-label">Stale (&gt;7d)</span>
      </div>
    </div>
  `;
}

function getDependabotTableHtml(prs: DependabotPR[]): string {
  const rows = prs
    .map(
      (pr) => `
    <tr>
      <td><a href="${escapeHtml(pr.url)}" target="_blank">#${pr.number}</a> ${escapeHtml(pr.title)}</td>
      <td><span class="badge badge--${pr.prType}">${escapeHtml(pr.prType)}</span></td>
      <td>${getCIBadgeHtml(pr.checkStatus)}</td>
      <td class="${pr.isStale ? "stale-age" : ""}">${pr.staleDays}d</td>
      <td>${getMergeButtonHtml(pr)}</td>
    </tr>
  `
    )
    .join("");

  return `
    <table class="dependabot-table">
      <thead>
        <tr>
          <th>PR</th>
          <th>Type</th>
          <th>CI</th>
          <th>Age</th>
          <th>Action</th>
        </tr>
      </thead>
      <tbody>${rows}</tbody>
    </table>
  `;
}

function getCIBadgeHtml(checkStatus?: string): string {
  if (!checkStatus) return '<span class="badge badge--unknown">—</span>';
  const status = checkStatus.toUpperCase();
  if (status === "SUCCESS") return '<span class="badge badge--success">✓ passing</span>';
  if (status === "FAILURE" || status === "ERROR")
    return '<span class="badge badge--danger">✗ failing</span>';
  if (status === "PENDING") return '<span class="badge badge--pending">● pending</span>';
  return `<span class="badge badge--unknown">${escapeHtml(checkStatus)}</span>`;
}

function getMergeButtonHtml(pr: DependabotPR): string {
  return `<button
    class="merge-btn"
    data-action="mergeDependabotPR"
    data-pr-node-id="${escapeHtml(pr.nodeId)}"
    data-owner="${escapeHtml(pr.repo.split("/")[0] ?? "")}"
    data-repo="${escapeHtml(pr.repo.split("/")[1] ?? "")}"
    title="Squash merge this dependabot PR"
  >Merge</button>`;
}

export function getDependabotTabScript(): string {
  return `
    (function() {
      var depPanel = document.getElementById('tab-panel-dependencies');
      if (!depPanel) return;

      depPanel.addEventListener('click', function(e) {
        ${getPlatformFailureScript()}
        var mergeBtn = e.target.closest('[data-action="mergeDependabotPR"]');
        if (mergeBtn) {
          mergeBtn.disabled = true;
          mergeBtn.textContent = 'Merging…';
          vscode.postMessage({
            type: 'mergeDependabotPR',
            prNodeId: mergeBtn.getAttribute('data-pr-node-id'),
            owner: mergeBtn.getAttribute('data-owner'),
            repo: mergeBtn.getAttribute('data-repo')
          });
          return;
        }

        var refreshBtn = e.target.closest('#dependabotRefreshBtn');
        if (refreshBtn) {
          vscode.postMessage({ type: 'dependabotRefresh' });
          return;
        }
      });
    })();
  `;
}

export function getDependabotTabStyles(): string {
  return `
    .dependabot-tab { padding: 8px 0; }
    .dependabot-loading, .dependabot-empty { padding: 24px; text-align: center; opacity: 0.7; }
    .dependabot-summary {
      display: flex;
      gap: 12px;
      margin-bottom: 16px;
    }
    .summary-card {
      background: var(--vscode-editor-inactiveSelectionBackground);
      border-radius: 4px;
      padding: 10px 16px;
      text-align: center;
      min-width: 80px;
    }
    .summary-card--danger { border: 1px solid var(--vscode-inputValidation-errorBorder); }
    .summary-card--warning { border: 1px solid var(--vscode-inputValidation-warningBorder); }
    .summary-value { display: block; font-size: 1.5em; font-weight: bold; }
    .summary-label { font-size: 0.8em; opacity: 0.7; }
    .dependabot-table { width: 100%; border-collapse: collapse; font-size: 0.9em; }
    .dependabot-table th, .dependabot-table td {
      padding: 6px 10px;
      border-bottom: 1px solid var(--vscode-panel-border);
      text-align: left;
    }
    .dependabot-table th { opacity: 0.7; font-weight: 600; }
    .stale-age { color: var(--vscode-inputValidation-warningForeground); font-weight: bold; }
    .badge { border-radius: 3px; padding: 2px 6px; font-size: 0.8em; }
    .badge--security { background: var(--vscode-inputValidation-errorBackground); }
    .badge--dependency { background: var(--vscode-editor-inactiveSelectionBackground); }
    .badge--success { color: var(--vscode-testing-iconPassed); }
    .badge--danger { color: var(--vscode-testing-iconFailed); }
    .badge--pending { opacity: 0.6; }
    .badge--unknown { opacity: 0.5; }
    .merge-btn {
      background: var(--vscode-button-background);
      color: var(--vscode-button-foreground);
      border: none;
      border-radius: 3px;
      padding: 3px 10px;
      cursor: pointer;
      font-size: 0.85em;
    }
    .merge-btn:hover { background: var(--vscode-button-hoverBackground); }
    .merge-btn:disabled { opacity: 0.5; cursor: not-allowed; }
  `;
}
