/**
 * DashboardComponents - Shared utilities and UI primitives for dashboard tab modules
 *
 * Extracted from DashboardHtml.ts as part of the tab-based modular refactor (#1542).
 * All tab modules import shared formatters and UI helpers from here to avoid duplication.
 *
 * @see docs/ARCHITECTURE.md for WebView patterns
 */

import type { PipelineRunSummary } from "./DashboardState";
import type { BacktrackRecord, ModelEscalationRecord } from "../../schemas/pipelineState";

/**
 * Generate nonce for script security
 */
export function getNonce(): string {
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
export function escapeHtml(text: string): string {
  const htmlEntities: Record<string, string> = {
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  };
  return text.replace(/[&<>"']/g, (char) => htmlEntities[char] || char);
}

/**
 * Format stage name for display
 */
export function formatStageName(stage: string): string {
  const labels: Record<string, string> = {
    "pipeline-start": "Initialize",
    "issue-pickup": "Issue Pickup",
    "feature-planning": "Feature Planning",
    "feature-dev": "Feature Development",
    "feature-validate": "Feature Validation",
    "pr-create": "PR Creation",
    "pr-merge": "PR Merge",
    "pipeline-finish": "Completion",
  };
  return (
    labels[stage] ??
    stage
      .split("-")
      .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
      .join(" ")
  );
}

/**
 * Format timestamp for display (UTC, time-only)
 *
 * Uses deterministic UTC-based formatting for timezone consistency
 * with formatFullDateTime().
 *
 * @see Issue #715 - Standardize on UTC across all dashboard timestamps
 * @see Issue #614 - formatFullDateTime was intentionally made UTC-based
 */
export function formatTimestamp(date: Date): string {
  const hours = String(date.getUTCHours()).padStart(2, "0");
  const minutes = String(date.getUTCMinutes()).padStart(2, "0");
  const seconds = String(date.getUTCSeconds()).padStart(2, "0");
  return `${hours}:${minutes}:${seconds} UTC`;
}

/**
 * Format full date/time for the "Last updated" indicator
 *
 * Uses deterministic UTC-based formatting to be timezone-safe:
 * "2026-02-12 14:32:45 UTC"
 *
 * @see Issue #614 - Last-updated should show full date/time, not time-only
 */
export function formatFullDateTime(date: Date): string {
  const year = date.getUTCFullYear();
  const month = String(date.getUTCMonth() + 1).padStart(2, "0");
  const day = String(date.getUTCDate()).padStart(2, "0");
  const hours = String(date.getUTCHours()).padStart(2, "0");
  const minutes = String(date.getUTCMinutes()).padStart(2, "0");
  const seconds = String(date.getUTCSeconds()).padStart(2, "0");
  return `${year}-${month}-${day} ${hours}:${minutes}:${seconds} UTC`;
}

/**
 * Format relative time (e.g., "2 minutes ago")
 */
export function formatRelativeTime(date: Date): string {
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffMins = Math.floor(diffMs / 60000);
  const diffHours = Math.floor(diffMs / 3600000);
  const diffDays = Math.floor(diffMs / 86400000);

  if (diffDays > 0) {
    return diffDays === 1 ? "Yesterday" : `${diffDays} days ago`;
  }
  if (diffHours > 0) {
    return `${diffHours}h ago`;
  }
  if (diffMins > 0) {
    return `${diffMins}m ago`;
  }
  return "Just now";
}

/**
 * Format duration in milliseconds to human-readable string
 */
export function formatDuration(ms: number): string {
  if (ms < 1000) {
    return `${ms}ms`;
  }
  const seconds = Math.floor(ms / 1000);
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);

  if (hours > 0) {
    const remainingMins = minutes % 60;
    return `${hours}h ${remainingMins}m`;
  }
  if (minutes > 0) {
    const remainingSecs = seconds % 60;
    return `${minutes}m ${remainingSecs}s`;
  }
  return `${seconds}s`;
}

/**
 * Format time saved for display
 */
export function formatTimeSaved(ms: number): string {
  const minutes = Math.floor(ms / 60000);
  const hours = Math.floor(minutes / 60);

  if (hours > 0) {
    const remainingMins = minutes % 60;
    return `${hours}h ${remainingMins}m`;
  }
  return `${minutes}m`;
}

/**
 * Format percentage for display
 */
export function formatPercent(value: number): string {
  return `${Math.round(value * 100)}%`;
}

/**
 * Format token count for display (e.g., 1500 → "1.5K", 1500000 → "1.5M")
 */
export function formatTokenCount(tokens: number): string {
  if (tokens >= 1_000_000) return `${(tokens / 1_000_000).toFixed(1)}M`;
  if (tokens >= 1_000) return `${(tokens / 1_000).toFixed(1)}K`;
  return String(tokens);
}

/**
 * Truncate text to a maximum length with ellipsis
 */
export function truncateText(text: string, maxLength: number): string {
  if (text.length <= maxLength) return text;
  return text.substring(0, maxLength - 3) + "...";
}

/**
 * Get status indicator HTML
 */
export function getStatusBadge(status: string): string {
  const statusColors: Record<string, string> = {
    running: "var(--vscode-charts-blue)",
    complete: "var(--vscode-charts-green)",
    failed: "var(--vscode-charts-red)",
    cancelled: "var(--vscode-charts-yellow)",
    pending: "var(--vscode-descriptionForeground)",
    skipped: "var(--vscode-descriptionForeground)",
  };
  const color = statusColors[status] || "var(--vscode-descriptionForeground)";
  const label = status.charAt(0).toUpperCase() + status.slice(1);
  return `<span class="status-badge" style="background: ${color};">${label}</span>`;
}

/**
 * Generate progress bar HTML
 */
export function getProgressBarHtml(
  run: PipelineRunSummary | null,
  backtrackCount: number = 0
): string {
  if (!run) {
    return `
      <div class="progress-section empty-state">
        <p>No pipeline running. Start a pipeline to see progress here.</p>
      </div>
    `;
  }

  const completedStages = run.stages.filter(
    (s) => s.status === "complete" || s.status === "skipped"
  ).length;
  const progressPercent = Math.round((completedStages / run.stages.length) * 100);
  const currentStageIndex = run.stages.findIndex((s) => s.status === "running");

  // Generate routing badge HTML
  const routingBadge = run.routing
    ? `<span class="route-badge route-${run.routing.route}" title="${run.routing.wasOverridden ? `Overridden from ${run.routing.originalRoute}` : `~${run.routing.estimatedTimeMinutes}min`}">${run.routing.route.toUpperCase()}</span>`
    : "";

  const retryDepthHtml =
    backtrackCount > 0
      ? `<span class="retry-depth" title="Backtrack attempts">↩ Retry ${backtrackCount}</span>`
      : "";

  return `
    <div class="progress-section">
      <div class="progress-header">
        <div class="progress-title">
          <strong>Issue #${run.issueNumber}</strong> - ${escapeHtml(run.title)}
        </div>
        <div class="progress-badges">
          ${routingBadge}
          ${getStatusBadge(run.status)}
        </div>
      </div>
      <div class="progress-bar-container">
        <div class="progress-bar" style="width: ${progressPercent}%;"></div>
      </div>
      <div class="progress-info">
        <span class="progress-percent">${progressPercent}%</span>
        <span class="progress-stage">
          ${run.currentStage ? `Stage: ${formatStageName(run.currentStage)} (${currentStageIndex + 1}/${run.stages.length})` : "Completed"}
        </span>
        ${run.routing?.skippedStages.length ? `<span class="skipped-count">${run.routing.skippedStages.length} stage(s) skipped</span>` : ""}
        ${retryDepthHtml}
      </div>
      <div class="stage-indicators">
        ${run.stages
          .map((stage) => {
            const statusClass =
              stage.status === "complete"
                ? "complete"
                : stage.status === "running"
                  ? "running"
                  : stage.status === "failed"
                    ? "failed"
                    : stage.status === "skipped"
                      ? "skipped"
                      : stage.status === "deferred"
                        ? "deferred"
                        : "pending";
            return `<div class="stage-dot ${statusClass}" title="${formatStageName(stage.stage)}: ${stage.status}"></div>`;
          })
          .join("")}
      </div>
    </div>
  `;
}

/**
 * Base/shared CSS styles used across all dashboard tabs.
 *
 * Covers :root variables, resets, body/layout, header, dropdowns, collapsible
 * sections, charts grid, sparklines, refresh animation, and common utilities.
 */
export function getBaseStyles(): string {
  return `
    :root {
      --spacing-xs: 4px;
      --spacing-sm: 8px;
      --spacing-md: 16px;
      --spacing-lg: 24px;
      --border-radius: 4px;
    }

    * {
      box-sizing: border-box;
      margin: 0;
      padding: 0;
    }

    html {
      height: 100%;
    }

    body {
      font-family: var(--vscode-font-family);
      font-size: var(--vscode-font-size);
      color: var(--vscode-foreground);
      background: var(--vscode-editor-background);
      line-height: 1.5;
      min-height: 100%;
      overflow-y: auto;
      box-sizing: border-box;
    }

    .dashboard {
      max-width: 1200px;
      margin: 0 auto;
      width: 100%;
      padding: var(--spacing-md);
      box-sizing: border-box;
    }

    /* Header */
    .dashboard-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      padding-bottom: var(--spacing-md);
      border-bottom: 1px solid var(--vscode-panel-border);
      margin-bottom: var(--spacing-md);
    }

    .header-title {
      display: flex;
      flex-direction: column;
      gap: 2px;
    }

    .dashboard-header h1 {
      font-size: 1.3em;
      font-weight: 600;
      margin: 0;
      letter-spacing: -0.01em;
    }

    .last-updated {
      font-size: 0.75em;
      color: var(--vscode-descriptionForeground);
      opacity: 0.8;
    }

    .last-updated.refreshing {
      color: var(--vscode-charts-blue);
      opacity: 1;
      font-weight: 500;
    }

    .header-actions {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
    }

    .action-btn {
      display: inline-flex;
      align-items: center;
      gap: var(--spacing-xs);
      padding: 4px 10px;
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
      border: 1px solid transparent;
      border-radius: var(--border-radius);
      cursor: pointer;
      font-size: 0.8em;
      white-space: nowrap;
      transition: background 0.15s;
    }

    .action-btn:hover {
      background: var(--vscode-button-secondaryHoverBackground);
    }

    .action-btn.primary {
      background: var(--vscode-button-background);
      color: var(--vscode-button-foreground);
    }

    .action-btn.primary:hover {
      background: var(--vscode-button-hoverBackground);
    }

    .dropdown {
      position: relative;
      display: inline-block;
    }

    .dropdown-content {
      display: none;
      position: absolute;
      right: 0;
      background: var(--vscode-dropdown-background);
      border: 1px solid var(--vscode-dropdown-border);
      border-radius: var(--border-radius);
      z-index: 100;
      min-width: 120px;
    }

    .dropdown:hover .dropdown-content {
      display: block;
    }

    .dropdown-item {
      display: block;
      width: 100%;
      padding: var(--spacing-sm) var(--spacing-md);
      text-align: left;
      background: none;
      border: none;
      color: var(--vscode-dropdown-foreground);
      cursor: pointer;
    }

    .dropdown-item:hover {
      background: var(--vscode-list-hoverBackground);
    }

    .dropdown-divider {
      height: 1px;
      background: var(--vscode-panel-border);
      margin: 4px 0;
    }

    /* Charts Section */
    .charts-section {
      display: grid;
      grid-template-columns: 1fr 1fr;
      gap: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }

    @media (max-width: 800px) {
      .charts-section {
        grid-template-columns: 1fr;
      }
    }

    .chart-container {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
    }

    .chart-container h3 {
      font-size: 0.95em;
      font-weight: 500;
      margin-bottom: var(--spacing-sm);
      color: var(--vscode-foreground);
    }

    .chart-wrapper {
      height: 200px;
      position: relative;
    }

    .chart-summary {
      margin-top: var(--spacing-sm);
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }

    /* Enhanced Charts Grid */
    .charts-grid {
      display: grid;
      grid-template-columns: 2fr 1fr;
      gap: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }

    @media (max-width: 800px) {
      .charts-grid {
        grid-template-columns: 1fr;
      }
    }

    .charts-main {
      display: flex;
      flex-direction: column;
      gap: var(--spacing-md);
      min-height: 0;
      overflow-y: auto;
    }

    .charts-sidebar {
      display: flex;
      flex-direction: column;
      gap: var(--spacing-md);
    }

    /* Sparklines */
    .sparkline-section {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
    }

    .sparkline-grid {
      display: grid;
      grid-template-columns: repeat(2, 1fr);
      gap: var(--spacing-md);
    }

    .sparkline-item {
      text-align: center;
    }

    .sparkline-canvas {
      height: 40px;
      width: 100%;
    }

    .sparkline-label {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
      margin-top: var(--spacing-xs);
    }

    /* Collapsible Sections */
    .collapsible-section {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      margin-bottom: var(--spacing-md);
      overflow: hidden;
    }

    .section-toggle {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      padding: var(--spacing-md);
      cursor: pointer;
      background: var(--vscode-editor-background);
      list-style: none;
      user-select: none;
      transition: background 0.2s ease;
    }

    .section-toggle::-webkit-details-marker {
      display: none;
    }

    .section-toggle:hover {
      background: var(--vscode-list-hoverBackground);
    }

    .section-toggle h3 {
      font-size: 1.1em;
      font-weight: 600;
      margin: 0;
    }

    .toggle-icon {
      font-size: 0.8em;
      transition: transform 0.2s ease;
      color: var(--vscode-descriptionForeground);
    }

    details[open] .toggle-icon {
      transform: rotate(0deg);
    }

    details:not([open]) .toggle-icon {
      transform: rotate(-90deg);
    }

    .section-content {
      padding: var(--spacing-md);
      border-top: 1px solid var(--vscode-panel-border);
      overflow-y: auto;
    }

    /* Refresh Animation */
    @keyframes spin {
      from { transform: rotate(0deg); }
      to { transform: rotate(360deg); }
    }

    #refreshIcon.spinning {
      display: inline-block;
      animation: spin 1s linear infinite;
    }

    .action-btn.refreshing {
      opacity: 0.6;
      cursor: not-allowed;
    }

    /* Metrics refreshing visual feedback (Issue #998) */
    .refreshing {
      opacity: 0.7;
      transition: opacity 0.2s ease;
    }

    @keyframes pulse {
      0%, 100% { opacity: 1; }
      50% { opacity: 0.5; }
    }

    /* Empty State */
    .empty-state {
      padding: var(--spacing-lg);
      text-align: center;
      color: var(--vscode-descriptionForeground);
    }

    .section-badge {
      margin-left: auto;
      padding: 2px 8px;
      border-radius: 10px;
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
      font-size: 0.8em;
      font-weight: 600;
    }

    .action-btn-sm {
      padding: 2px 8px;
      font-size: 0.8em;
    }

    .btn-sm {
      padding: 3px 10px;
      font-size: 0.8em;
    }

    .btn-ghost {
      background: transparent;
      color: var(--vscode-descriptionForeground);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      cursor: pointer;
    }

    .btn-ghost:hover {
      background: var(--vscode-list-hoverBackground);
    }
  `;
}
