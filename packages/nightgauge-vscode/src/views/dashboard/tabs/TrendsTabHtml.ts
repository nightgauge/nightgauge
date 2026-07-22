/**
 * TrendsTabHtml — HTML generator for the Trends dashboard tab.
 *
 * Follows the established tab-module contract:
 *   getTrendsTabHtml()    — returns HTML string
 *   getTrendsTabScript()  — returns JS string (event handlers)
 *   getTrendsTabStyles()  — returns CSS string (scoped)
 *
 * Renders week-over-week success rate, cost-per-run, and total-run trends
 * as inline SVG polylines/bars. No external charting library.
 *
 * @see Issue #3320 — Add Trends Tab to Pipeline Dashboard
 */

import { escapeHtml } from "../DashboardComponents";
import type { TrendsData, TrendsDateRange } from "../DashboardState";
import type { TrendEntry } from "../../../services/IpcClientBase";

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
    return getTrendsNoAccessHtml();
  }
  if (data.result === null) {
    return getTrendsEmptyHtml();
  }

  const { result, showComparison } = data;

  if (result.current.length < SPARSE_THRESHOLD) {
    return getTrendsSparseHtml(result.current.length);
  }

  const dateRange = (result.period as TrendsDateRange) ?? "30d";

  return `
    <div class="trends-tab">
      ${data.errorMessage ? `<div class="trends-error-banner">${escapeHtml(data.errorMessage)}</div>` : ""}
      ${getTrendsDateRangeHtml(dateRange)}
      <div class="trends-comparison-row">
        <label class="trends-comparison-label">
          <input type="checkbox" id="trendsComparisonToggle"${showComparison ? " checked" : ""}>
          Show comparison (vs. previous period)
        </label>
        <button class="action-btn action-btn-sm" id="trendsRefreshBtn">Refresh</button>
      </div>
      <div class="trends-charts-grid">
        <div class="trends-chart-card">
          <h4 class="trends-chart-title">Success Rate</h4>
          ${getSuccessRateChartHtml(result.current, showComparison ? result.previous : [])}
        </div>
        <div class="trends-chart-card">
          <h4 class="trends-chart-title">Cost per Run</h4>
          ${getCostPerRunChartHtml(result.current, showComparison ? result.previous : [])}
        </div>
        <div class="trends-chart-card">
          <h4 class="trends-chart-title">Total Runs</h4>
          ${getTotalRunsChartHtml(result.current, showComparison ? result.previous : [])}
        </div>
      </div>
    </div>
  `;
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

      // Comparison toggle checkbox
      var comparisonToggle = trendsPanel.querySelector('#trendsComparisonToggle');
      if (comparisonToggle) {
        comparisonToggle.addEventListener('change', function(e) {
          vscode.postMessage({ type: 'trendsToggleComparison', show: e.target.checked });
        });
      }
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
    .trends-comparison-label {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm, 8px);
      font-size: 0.9em;
      cursor: pointer;
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
    .trends-delta-badge {
      font-size: 0.75em;
      font-weight: 600;
      padding: 1px 6px;
      border-radius: 3px;
    }
    .trends-delta-positive {
      background: color-mix(in srgb, var(--vscode-terminal-ansiGreen) 20%, transparent);
      color: var(--vscode-terminal-ansiGreen);
    }
    .trends-delta-negative {
      background: color-mix(in srgb, var(--vscode-terminal-ansiRed) 20%, transparent);
      color: var(--vscode-terminal-ansiRed);
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
    .trends-bar-previous { background: color-mix(in srgb, var(--vscode-charts-purple, #b267e6) 40%, transparent); }
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
    <div class="trends-empty">
      <div class="trends-empty-icon">⏳</div>
      <p class="trends-empty-title">Loading trends…</p>
    </div>
  `;
}

function getTrendsNoAccessHtml(): string {
  return `
    <div class="trends-no-access">
      <p>Trends data requires a connected platform account. Sign in to enable longitudinal pipeline analytics.</p>
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

function getDeltaBadge(current: number[], previous: number[]): string {
  if (previous.length === 0 || current.length === 0) return "";
  const avgCurrent = current.reduce((a, b) => a + b, 0) / current.length;
  const avgPrevious = previous.reduce((a, b) => a + b, 0) / previous.length;
  if (avgPrevious === 0) return "";
  const delta = ((avgCurrent - avgPrevious) / avgPrevious) * 100;
  const sign = delta >= 0 ? "+" : "";
  const cls = delta >= 0 ? "trends-delta-positive" : "trends-delta-negative";
  return `<div class="trends-delta-row"><span class="trends-delta-badge ${cls}">${sign}${delta.toFixed(1)}% vs prev</span></div>`;
}

function getSuccessRateChartHtml(current: TrendEntry[], previous: TrendEntry[]): string {
  const W = 300;
  const H = 80;
  const curVals = current.map((e) => e.successRate * 100);
  const prevVals = previous.map((e) => e.successRate * 100);
  const allVals = [...curVals, ...prevVals, 0];
  const minVal = 0;
  const maxVal = Math.max(...allVals, 100);

  const curPoints = escapeHtml(buildPolylinePoints(curVals, W, H, minVal, maxVal));
  const prevPoints =
    prevVals.length > 1 ? escapeHtml(buildPolylinePoints(prevVals, W, H, minVal, maxVal)) : "";

  return `
    <div class="trends-svg-wrap">
      <svg viewBox="0 0 ${W} ${H}" xmlns="http://www.w3.org/2000/svg" aria-label="Success rate trend" role="img">
        ${
          prevPoints
            ? `<polyline points="${prevPoints}" fill="none" stroke="var(--vscode-charts-green, #4ec9b0)" stroke-width="1.5" stroke-dasharray="4 2" stroke-linecap="round" stroke-linejoin="round" opacity="0.5"/>`
            : ""
        }
        <polyline points="${curPoints}" fill="none" stroke="var(--vscode-charts-green, #4ec9b0)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
      </svg>
      ${getXLabels(current)}
    </div>
    ${getDeltaBadge(curVals, prevVals)}
  `;
}

function getCostPerRunChartHtml(current: TrendEntry[], previous: TrendEntry[]): string {
  const W = 300;
  const H = 80;
  const curVals = current.map((e) => e.costPerRun);
  const prevVals = previous.map((e) => e.costPerRun);
  const allVals = [...curVals, ...prevVals, 0];
  const minVal = 0;
  const maxVal = Math.max(...allVals, 0.0001);

  const curPoints = escapeHtml(buildPolylinePoints(curVals, W, H, minVal, maxVal));
  const prevPoints =
    prevVals.length > 1 ? escapeHtml(buildPolylinePoints(prevVals, W, H, minVal, maxVal)) : "";

  return `
    <div class="trends-svg-wrap">
      <svg viewBox="0 0 ${W} ${H}" xmlns="http://www.w3.org/2000/svg" aria-label="Cost per run trend" role="img">
        ${
          prevPoints
            ? `<polyline points="${prevPoints}" fill="none" stroke="var(--vscode-charts-blue, #569cd6)" stroke-width="1.5" stroke-dasharray="4 2" stroke-linecap="round" stroke-linejoin="round" opacity="0.5"/>`
            : ""
        }
        <polyline points="${curPoints}" fill="none" stroke="var(--vscode-charts-blue, #569cd6)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
      </svg>
      ${getXLabels(current)}
    </div>
    ${getDeltaBadge(curVals, prevVals)}
  `;
}

function getTotalRunsChartHtml(current: TrendEntry[], previous: TrendEntry[]): string {
  const maxRuns = Math.max(
    ...current.map((e) => e.totalRuns),
    ...previous.map((e) => e.totalRuns),
    1
  );
  const H = 80;

  const bars = current
    .map((entry, i) => {
      const curH = Math.max(2, Math.round((entry.totalRuns / maxRuns) * H));
      const prevEntry = previous[i];
      const prevH = prevEntry ? Math.max(2, Math.round((prevEntry.totalRuns / maxRuns) * H)) : 0;
      return `
        <div class="trends-bar-wrap">
          ${prevH > 0 ? `<div class="trends-bar trends-bar-previous" style="height:${prevH}px" title="${prevEntry?.totalRuns ?? 0} (prev)"></div>` : ""}
          <div class="trends-bar trends-bar-current" style="height:${curH}px" title="${escapeHtml(String(entry.totalRuns))} runs on ${escapeHtml(entry.date)}"></div>
        </div>
      `;
    })
    .join("");

  const deltaVals = {
    cur: current.map((e) => e.totalRuns),
    prev: previous.map((e) => e.totalRuns),
  };

  return `
    <div class="trends-bar-group" aria-label="Total runs bar chart">
      ${bars}
    </div>
    ${getXLabels(current)}
    ${getDeltaBadge(deltaVals.cur, deltaVals.prev)}
  `;
}
