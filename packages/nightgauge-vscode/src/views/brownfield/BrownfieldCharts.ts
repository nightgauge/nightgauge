/**
 * BrownfieldCharts - Pure HTML/CSS chart generators for brownfield dashboard
 *
 * Generates HTML bar charts, progress bars, and trend indicators using
 * CSS custom properties for theme-aware rendering. No external libraries.
 *
 * Note: DashboardCharts.ts was deprecated due to Chart.js memory leaks.
 * This module uses pure HTML/CSS bars following the same approach.
 *
 * @see Issue #1163 - Brownfield Modernization Progress Dashboard
 */

import type {
  DimensionBreakdown,
  SecuritySeverityCounts,
  BrownfieldSnapshot,
  HealthStatus,
} from "./BrownfieldTypes";

/**
 * CSS color for a health status value
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
 * CSS color for a numeric score (0-100)
 */
function scoreColor(score: number): string {
  if (score >= 90) return "#22c55e";
  if (score >= 70) return "#3b82f6";
  if (score >= 50) return "#eab308";
  if (score >= 30) return "#f97316";
  return "#ef4444";
}

/**
 * Generate horizontal bar chart for dimension breakdown
 *
 * Each dimension is a labeled bar with score, color-coded by status.
 */
export function getDimensionBarChartHtml(dimensions: DimensionBreakdown[]): string {
  if (dimensions.length === 0) {
    return '<p class="brownfield-empty">No dimension data available</p>';
  }

  const bars = dimensions
    .map((dim) => {
      const color = statusColor(dim.status);
      const label = formatDimensionName(dim.name);
      return `
      <div class="bf-bar-row">
        <span class="bf-bar-label">${escapeHtml(label)}</span>
        <div class="bf-bar-track">
          <div class="bf-bar-fill" style="width: ${dim.score}%; background: ${color};"></div>
        </div>
        <span class="bf-bar-value" style="color: ${color};">${dim.score}</span>
      </div>`;
    })
    .join("");

  return `<div class="bf-bar-chart">${bars}</div>`;
}

/**
 * Generate a progress bar for modernization task completion
 */
export function getProgressBarHtml(completed: number, total: number): string {
  const percent = total > 0 ? Math.round((completed / total) * 100) : 0;
  const color = scoreColor(percent);

  return `
    <div class="bf-progress-container">
      <div class="bf-progress-track">
        <div class="bf-progress-fill" style="width: ${percent}%; background: ${color};"></div>
      </div>
      <span class="bf-progress-label">${completed} / ${total} tasks (${percent}%)</span>
    </div>`;
}

/**
 * Generate severity breakdown bars for security vulnerabilities
 */
export function getSeverityBarsHtml(counts: SecuritySeverityCounts): string {
  const total = counts.critical + counts.high + counts.medium + counts.low + counts.info;
  if (total === 0) {
    return '<p class="brownfield-empty">No vulnerabilities found</p>';
  }

  const items = [
    { label: "Critical", count: counts.critical, color: "#ef4444" },
    { label: "High", count: counts.high, color: "#f97316" },
    { label: "Medium", count: counts.medium, color: "#eab308" },
    { label: "Low", count: counts.low, color: "#3b82f6" },
    { label: "Info", count: counts.info, color: "#6b7280" },
  ];

  const bars = items
    .filter((item) => item.count > 0)
    .map((item) => {
      const pct = Math.round((item.count / total) * 100);
      return `
      <div class="bf-bar-row">
        <span class="bf-bar-label">${item.label}</span>
        <div class="bf-bar-track">
          <div class="bf-bar-fill" style="width: ${pct}%; background: ${item.color};"></div>
        </div>
        <span class="bf-bar-value">${item.count}</span>
      </div>`;
    })
    .join("");

  return `<div class="bf-bar-chart">${bars}</div>`;
}

/**
 * Generate dependency health ratio bar
 */
export function getDependencyBarHtml(total: number, outdated: number, vulnerable: number): string {
  if (total === 0) {
    return '<p class="brownfield-empty">No dependency data available</p>';
  }

  const upToDate = total - outdated;
  const upToDatePct = Math.round((upToDate / total) * 100);
  const outdatedPct = Math.round((outdated / total) * 100);

  return `
    <div class="bf-dep-bar">
      <div class="bf-dep-stacked-track">
        <div class="bf-dep-fill-good" style="width: ${upToDatePct}%;"></div>
        <div class="bf-dep-fill-warn" style="width: ${outdatedPct}%;"></div>
      </div>
      <div class="bf-dep-legend">
        <span class="bf-dep-legend-item"><span class="bf-dot bf-dot-good"></span> Up-to-date: ${upToDate}</span>
        <span class="bf-dep-legend-item"><span class="bf-dot bf-dot-warn"></span> Outdated: ${outdated}</span>
        ${vulnerable > 0 ? `<span class="bf-dep-legend-item"><span class="bf-dot bf-dot-critical"></span> Vulnerable: ${vulnerable}</span>` : ""}
      </div>
    </div>`;
}

/**
 * Generate trend sparkline as a simple inline bar-based indicator
 */
export function getTrendIndicatorHtml(
  history: BrownfieldSnapshot[],
  metric: "health_score" | "security_score"
): string {
  const values = history.map((s) => s[metric]).filter((v): v is number => v !== null);

  if (values.length < 2) {
    return '<span class="bf-trend bf-trend-stable">--</span>';
  }

  const latest = values[values.length - 1];
  const previous = values[values.length - 2];
  const delta = latest - previous;

  if (delta > 2) {
    return `<span class="bf-trend bf-trend-up" title="+${delta}">&#9650; +${delta}</span>`;
  } else if (delta < -2) {
    return `<span class="bf-trend bf-trend-down" title="${delta}">&#9660; ${delta}</span>`;
  }
  return '<span class="bf-trend bf-trend-stable" title="Stable">&#9644; 0</span>';
}

/**
 * Format dimension name from snake_case to Title Case
 */
function formatDimensionName(name: string): string {
  return name.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
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
