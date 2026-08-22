/**
 * TrendsTabHtml — HTML generator for the Trends dashboard tab.
 *
 * Follows the established tab-module contract:
 *   getTrendsTabHtml()    — returns HTML string
 *   getTrendsTabScript()  — returns JS string (event handlers)
 *   getTrendsTabStyles()  — returns CSS string (scoped)
 *
 * Renders success-rate, total-run, and token trends as inline SVG
 * polylines/bars. No external charting library.
 *
 * The three series are exactly what GET /v1/analytics/trends returns. It used
 * to render a cost-per-run chart and a "vs. previous period" comparison; the
 * endpoint supplies neither (it has no cost metric, and documents its
 * comparison window as an unimplemented follow-up), so both plotted a series
 * that could only ever be empty — see #801.
 *
 * @see Issue #3320 — Add Trends Tab to Pipeline Dashboard
 * @see Issue 801 — render what the endpoint actually returns
 */

import { escapeHtml } from "../DashboardComponents";
import type { TrendsData, TrendsDateRange } from "../DashboardState";
import type { AnalyticsTrendsResult, TrendEntry } from "../../../services/IpcClientBase";
import {
  renderPlatformFailure,
  getPlatformRetryButtonHtml,
  getPlatformSignInButtonHtml,
  getPlatformFailureScript,
} from "./PlatformFailureHtml";

const SPARSE_THRESHOLD = 7;

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Render the full trends tab panel HTML.
 * @param data  Current trends data bundle; undefined triggers loading state.
 */
export function getTrendsTabHtml(data: TrendsData | null | undefined): string {
  if (!data || data.isLoading) {
    return getTrendsLoadingHtml();
  }
  if (!data.hasAccess) {
    return getTrendsNoAccessHtml(data.failure);
  }
  if (data.result === null) {
    return getTrendsEmptyHtml();
  }

  const { result } = data;

  if (result.entries.length < SPARSE_THRESHOLD) {
    return getTrendsSparseHtml(result.entries.length);
  }

  return `
    <div class="trends-tab">
      ${getTrendsDateRangeHtml(data.dateRange)}
      <div class="trends-comparison-row">
        <span class="trends-window-label">${getTrendsWindowLabel(result)}</span>
        <button class="action-btn action-btn-sm" id="trendsRefreshBtn">Refresh</button>
      </div>
      <div class="trends-charts-grid">
        <div class="trends-chart-card">
          <h4 class="trends-chart-title">Success Rate</h4>
          ${getSuccessRateChartHtml(result.entries, result.targetSuccessRate)}
        </div>
        <div class="trends-chart-card">
          <h4 class="trends-chart-title">Total Runs</h4>
          ${getTotalRunsChartHtml(result.entries)}
        </div>
        <div class="trends-chart-card">
          <h4 class="trends-chart-title">Tokens</h4>
          ${getTokensChartHtml(result.entries)}
        </div>
      </div>
    </div>
  `;
}

/**
 * The window the SERVER resolved, not the one the client asked for.
 *
 * These differed silently until #801: the client sent a `period` parameter the
 * endpoint does not declare, so every request got the default 30-day window no
 * matter which range was selected. Showing the resolved bounds makes a
 * recurrence visible instead of invisible.
 */
function getTrendsWindowLabel(result: AnalyticsTrendsResult): string {
  const from = result.dateFrom.substring(0, 10);
  const to = result.dateTo.substring(0, 10);
  if (!from || !to) return "";
  const repos = result.repos.length === 1 ? result.repos[0] : `${result.repos.length} repos`;
  return escapeHtml(`${from} → ${to} · ${result.granularity} · ${repos}`);
}

/**
 * JS event handlers for the trends tab.
 * Uses event delegation on the tab panel; vscode.postMessage() for IPC.
 */
export function getTrendsTabScript(): string {
  return `
    (function() {
      var trendsPanel = document.getElementById('tab-panel-trends');
      if (!trendsPanel) return;

      trendsPanel.addEventListener('click', function(e) {
        ${getPlatformFailureScript()}
        // Date range toggle
        var rangeBtn = e.target.closest('[data-trends-range]');
        if (rangeBtn) {
          var range = rangeBtn.getAttribute('data-trends-range');
          if (range !== '30d' && range !== '90d' && range !== '180d') return;
          trendsPanel.querySelectorAll('[data-trends-range]').forEach(function(b) {
            b.classList.toggle('active', b === rangeBtn);
          });
          vscode.postMessage({ type: 'trendsDateRangeChange', range: range });
          return;
        }

        // Refresh button
        var refreshBtn = e.target.closest('#trendsRefreshBtn');
        if (refreshBtn) {
          vscode.postMessage({ type: 'trendsRefresh' });
          return;
        }
      });

    })();
  `;
}

/** CSS for the trends tab. */
export function getTrendsTabStyles(): string {
  return `
    .trends-tab {
      padding: var(--spacing-md, 12px) 0;
    }
    .trends-error-banner {
      background: var(--vscode-inputValidation-errorBackground);
      border: 1px solid var(--vscode-inputValidation-errorBorder);
      border-radius: var(--border-radius, 3px);
      padding: var(--spacing-sm, 8px) var(--spacing-md, 12px);
      margin-bottom: var(--spacing-md, 12px);
      font-size: 0.85em;
    }
    .trends-date-range {
      display: flex;
      gap: 4px;
      margin-bottom: var(--spacing-md, 12px);
    }
    .trends-comparison-row {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin-bottom: var(--spacing-md, 12px);
    }
    .trends-window-label {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }
    .trends-charts-grid {
      display: grid;
      grid-template-columns: 1fr 1fr 1fr;
      gap: var(--spacing-md, 12px);
    }
    @media (max-width: 480px) {
      .trends-charts-grid {
        grid-template-columns: 1fr;
      }
    }
    .trends-chart-card {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius, 3px);
      padding: var(--spacing-md, 12px);
    }
    .trends-chart-title {
      font-size: 0.85em;
      font-weight: 600;
      color: var(--vscode-descriptionForeground);
      text-transform: uppercase;
      letter-spacing: 0.04em;
      margin: 0 0 var(--spacing-sm, 8px) 0;
    }
    .trends-svg-wrap {
      display: flex;
      flex-direction: column;
      gap: 4px;
    }
    .trends-svg-wrap svg {
      width: 100%;
      height: 80px;
      overflow: visible;
    }
    .trends-x-labels {
      display: flex;
      justify-content: space-between;
      font-size: 0.7em;
      color: var(--vscode-descriptionForeground);
    }
    .trends-delta-row {
      display: flex;
      justify-content: flex-end;
      margin-top: 4px;
    }
    .trends-target-note {
      font-size: 0.75em;
      color: var(--vscode-descriptionForeground);
    }
    .trends-bar-group {
      display: flex;
      align-items: flex-end;
      justify-content: space-between;
      gap: 2px;
      height: 80px;
    }
    .trends-bar-wrap {
      flex: 1;
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: flex-end;
      height: 100%;
      gap: 2px;
    }
    .trends-bar {
      width: 100%;
      border-radius: 2px 2px 0 0;
      min-height: 2px;
    }
    .trends-bar-current { background: var(--vscode-charts-purple, #b267e6); }
    .trends-empty {
      display: flex;
      flex-direction: column;
      align-items: center;
      padding: var(--spacing-xl, 32px) var(--spacing-md, 12px);
      gap: var(--spacing-sm, 8px);
      text-align: center;
    }
    .trends-empty-icon { font-size: 2em; }
    .trends-empty-title {
      color: var(--vscode-foreground);
      font-weight: 600;
      margin: 0;
    }
    .trends-empty-hint {
      color: var(--vscode-descriptionForeground);
      font-size: 0.85em;
      margin: 0;
    }
    .trends-no-access {
      color: var(--vscode-descriptionForeground);
      font-size: 0.9em;
      padding: var(--spacing-lg, 16px) 0;
    }
  `;
}

// ---------------------------------------------------------------------------
// Private helpers
// ---------------------------------------------------------------------------

function getTrendsLoadingHtml(): string {
  return `
    <div class="trends-empty" role="status" aria-live="polite" aria-atomic="true">
      <div class="trends-empty-icon">⏳</div>
      <p class="trends-empty-title">Loading trends…</p>
    </div>
  `;
}

/**
 * Render the trends no-access state from the classified `PlatformFailure`
 * the service actually reported — never a fixed "Sign in" message for a
 * user who is already signed in but hit a different failure (#748).
 */
function getTrendsNoAccessHtml(failure: TrendsData["failure"]): string {
  if (!failure) {
    return `
      <div class="trends-no-access">
        <p>Trends data requires a connected platform account. Sign in to enable longitudinal pipeline analytics.</p>
      </div>
    `;
  }
  const rendered = renderPlatformFailure(failure);
  const cta = rendered.showSignIn
    ? getPlatformSignInButtonHtml("trendsSignInBtn")
    : rendered.showRetry
      ? getPlatformRetryButtonHtml("trendsRetryBtn", { type: "trendsRefresh" })
      : "";
  return `
    <div class="trends-no-access">
      <div class="trends-empty-icon">${rendered.icon}</div>
      <p class="trends-empty-title">${escapeHtml(rendered.title)}</p>
      <p>${rendered.hintHtml}</p>
      ${cta}
    </div>
  `;
}

function getTrendsEmptyHtml(): string {
  return `
    <div class="trends-empty">
      <div class="trends-empty-icon">📈</div>
      <p class="trends-empty-title">No trends data yet</p>
      <p class="trends-empty-hint">Run the pipeline a few times and check back soon.</p>
    </div>
  `;
}

function getTrendsSparseHtml(count: number): string {
  return `
    <div class="trends-empty">
      <div class="trends-empty-icon">📊</div>
      <p class="trends-empty-title">Not enough data</p>
      <p class="trends-empty-hint">Trends require at least ${SPARSE_THRESHOLD} pipeline runs. You have ${count} so far — keep going!</p>
    </div>
  `;
}

function getTrendsDateRangeHtml(activeRange: TrendsDateRange): string {
  const ranges: TrendsDateRange[] = ["30d", "90d", "180d"];
  const labels: Record<TrendsDateRange, string> = {
    "30d": "30 Days",
    "90d": "90 Days",
    "180d": "180 Days",
  };
  return `
    <div class="trends-date-range" role="group" aria-label="Trends date range">
      ${ranges
        .map(
          (r) =>
            `<button class="toggle-btn${r === activeRange ? " active" : ""}" data-trends-range="${r}">${labels[r]}</button>`
        )
        .join("")}
    </div>
  `;
}

function buildPolylinePoints(
  values: number[],
  W: number,
  H: number,
  minVal: number,
  maxVal: number
): string {
  const range = maxVal - minVal || 1;
  const step = W / Math.max(values.length - 1, 1);
  return values
    .map((v, i) => {
      const x = (i * step).toFixed(1);
      const y = (H - 4 - ((v - minVal) / range) * (H - 8)).toFixed(1);
      return `${x},${y}`;
    })
    .join(" ");
}

function getXLabels(entries: TrendEntry[]): string {
  if (entries.length === 0) return "";
  const first = escapeHtml(entries[0].date);
  const last = escapeHtml(entries[entries.length - 1].date);
  return `<div class="trends-x-labels"><span>${first}</span><span>${last}</span></div>`;
}

function getSuccessRateChartHtml(entries: TrendEntry[], target: number): string {
  const W = 300;
  const H = 80;
  // successRate is already a percentage (0-100) — the endpoint's own
  // success_rate metric and its targetSuccessRate companion are both in those
  // units. This used to multiply by 100 on the assumption it was a fraction,
  // which pinned every point to the top of the chart (#801).
  const vals = entries.map((e) => e.successRate);
  const maxVal = Math.max(...vals, target, 100);
  const points = escapeHtml(buildPolylinePoints(vals, W, H, 0, maxVal));
  const targetY = target > 0 ? H - (target / maxVal) * H : -1;

  return `
    <div class="trends-svg-wrap">
      <svg viewBox="0 0 ${W} ${H}" xmlns="http://www.w3.org/2000/svg" aria-label="Success rate trend" role="img">
        ${
          targetY >= 0
            ? `<line x1="0" y1="${targetY.toFixed(1)}" x2="${W}" y2="${targetY.toFixed(1)}" stroke="var(--vscode-charts-green, #4ec9b0)" stroke-width="1" stroke-dasharray="4 2" opacity="0.5"><title>Target ${escapeHtml(String(target))}%</title></line>`
            : ""
        }
        <polyline points="${points}" fill="none" stroke="var(--vscode-charts-green, #4ec9b0)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
      </svg>
      ${getXLabels(entries)}
    </div>
    ${target > 0 ? `<div class="trends-delta-row"><span class="trends-target-note">Target ${escapeHtml(String(target))}%</span></div>` : ""}
  `;
}

/**
 * Token volume per bucket.
 *
 * Ingested runs carry tokens as a single per-stage total with no
 * input/output/cacheRead split, so this is the whole amount rather than a
 * stacked breakdown.
 */
function getTokensChartHtml(entries: TrendEntry[]): string {
  const W = 300;
  const H = 80;
  const vals = entries.map((e) => e.totalTokens);
  const maxVal = Math.max(...vals, 1);
  const points = escapeHtml(buildPolylinePoints(vals, W, H, 0, maxVal));

  return `
    <div class="trends-svg-wrap">
      <svg viewBox="0 0 ${W} ${H}" xmlns="http://www.w3.org/2000/svg" aria-label="Token usage trend" role="img">
        <polyline points="${points}" fill="none" stroke="var(--vscode-charts-blue, #569cd6)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
      </svg>
      ${getXLabels(entries)}
    </div>
    <div class="trends-delta-row"><span class="trends-target-note">Peak ${escapeHtml(formatTokenCount(maxVal))}</span></div>
  `;
}

/** Compact token count for a chart caption (1_234_000 -> "1.2M"). */
function formatTokenCount(tokens: number): string {
  if (tokens >= 1_000_000) return `${(tokens / 1_000_000).toFixed(1)}M`;
  if (tokens >= 1_000) return `${(tokens / 1_000).toFixed(1)}K`;
  return String(tokens);
}

function getTotalRunsChartHtml(entries: TrendEntry[]): string {
  const maxRuns = Math.max(...entries.map((e) => e.totalRuns), 1);
  const H = 80;

  const bars = entries
    .map((entry) => {
      const h = Math.max(2, Math.round((entry.totalRuns / maxRuns) * H));
      return `
        <div class="trends-bar-wrap">
          <div class="trends-bar trends-bar-current" style="height:${h}px" title="${escapeHtml(String(entry.totalRuns))} runs on ${escapeHtml(entry.date)}"></div>
        </div>
      `;
    })
    .join("");

  return `
    <div class="trends-bar-group" aria-label="Total runs bar chart">
      ${bars}
    </div>
    ${getXLabels(entries)}
  `;
}
