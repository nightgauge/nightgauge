/**
 * PipelineTabHtml - Pipeline execution tab renderer
 *
 * Extracted from DashboardHtml.ts as part of the tab-based modular refactor (#1542).
 * Contains pipeline progress, feedback events, tool calls, and history rendering.
 */

import {
  escapeHtml,
  formatTimestamp,
  formatRelativeTime,
  getProgressBarHtml,
} from "../DashboardComponents";
import type { PipelineRunSummary, ToolCallEntry, HistoryPaginationInfo } from "../DashboardState";
import type { BacktrackRecord, ModelEscalationRecord } from "../../../schemas/pipelineState";

/**
 * Render a single backtrack event as an HTML row (Issue #1349)
 */
function renderBacktrackEventHtml(record: BacktrackRecord): string {
  return `<div class="feedback-event feedback-warning">↩ Backtrack: ${escapeHtml(record.from_stage)} → ${escapeHtml(record.to_stage)} | Reason: ${escapeHtml(record.rationale)} | Attempt ${record.attempt_number}</div>`;
}

/**
 * Render a single model escalation event as an HTML row (Issue #1349)
 */
function renderEscalationEventHtml(record: ModelEscalationRecord): string {
  return `<div class="feedback-event feedback-escalation">⬆ Model Escalated: ${escapeHtml(record.from_model)} → ${escapeHtml(record.to_model)} at ${escapeHtml(record.stage)} | ${escapeHtml(record.rationale)}</div>`;
}

/**
 * Generate the Feedback Events collapsible section HTML (Issue #1349)
 *
 * Only rendered when there are backtrack or escalation events.
 */
export function getFeedbackEventsHtml(
  backtracks: BacktrackRecord[],
  modelEscalations: ModelEscalationRecord[]
): string {
  const total = backtracks.length + modelEscalations.length;
  if (total === 0) return "";

  return `
    <details class="feedback-events" open>
      <summary class="feedback-events-title">
        Feedback Events
        <span class="feedback-count">${total}</span>
      </summary>
      <div class="feedback-events-list">
        ${backtracks.map(renderBacktrackEventHtml).join("")}
        ${modelEscalations.map(renderEscalationEventHtml).join("")}
      </div>
    </details>
  `;
}

/**
 * Generate tool calls log HTML
 *
 * @param toolCalls - Tool calls to render
 * @param historicalIssueNumber - Issue number for historical runs (triggers on-demand loading)
 * @param autoLoad - When true, render a data-auto-load-issue marker instead of the manual
 *                   "Load Tool Calls" button (Issue #1842). Used for the auto-displayed most
 *                   recent run in the Pipeline tab.
 */
export function getToolCallsHtml(
  toolCalls: ToolCallEntry[],
  historicalIssueNumber?: number,
  autoLoad?: boolean
): string {
  if (toolCalls.length === 0) {
    // For historical runs, load tool calls on-demand (Issue #1032)
    if (historicalIssueNumber !== undefined) {
      if (autoLoad) {
        // Auto-load: render a marker div; the webview JS fires loadRunDetails on page load (Issue #1842)
        return `
          <div class="tool-calls-section" data-auto-load-issue="${historicalIssueNumber}">
            <div class="section-header">
              <h3>Tool Calls</h3>
            </div>
            <div class="empty-state" id="tool-calls-load-container">
              <p class="tool-calls-hint">Loading tool calls for most recent run...</p>
            </div>
          </div>
        `;
      }
      // Manual load button for history-tab-selected runs
      return `
        <div class="tool-calls-section">
          <div class="section-header">
            <h3>Tool Calls</h3>
          </div>
          <div class="empty-state" id="tool-calls-load-container">
            <button class="load-tool-calls-btn" data-action="load-tool-calls" data-issue="${historicalIssueNumber}">
              Load Tool Calls
            </button>
            <p class="tool-calls-hint">Tool calls are loaded on-demand from execution history.</p>
          </div>
        </div>
      `;
    }
    return `
      <div class="tool-calls-section">
        <div class="section-header">
          <h3>Tool Calls</h3>
          <select class="tool-filter" id="toolFilter">
            <option value="all">All</option>
          </select>
        </div>
        <div class="empty-state">
          <p>No tool calls recorded yet. Tool calls are tracked during pipeline execution.</p>
        </div>
      </div>
    `;
  }

  // Get unique tool types for filter
  const toolTypes = [...new Set(toolCalls.map((tc) => tc.tool))];

  return `
    <div class="tool-calls-section">
      <div class="section-header">
        <h3>Tool Calls</h3>
        <select class="tool-filter" id="toolFilter">
          <option value="all">All</option>
          ${toolTypes.map((t) => `<option value="${escapeHtml(t)}">${escapeHtml(t)}</option>`).join("")}
        </select>
      </div>
      <div class="tool-calls-list" id="toolCallsList">
        ${toolCalls
          .slice(-50) // Show last 50
          .reverse() // Most recent first
          .map(
            (tc, index) => `
          <div class="tool-call-item" data-tool="${escapeHtml(tc.tool)}"${tc.args ? ` data-args="${escapeHtml(JSON.stringify(tc.args))}"` : ""}${tc.result ? ` data-result="${escapeHtml(tc.result.substring(0, 500))}${tc.result.length > 500 ? "..." : ""}"` : ""}${tc.error ? ` data-error="${escapeHtml(tc.error)}"` : ""}${tc.durationMs ? ` data-duration="${tc.durationMs}"` : ""}>
            <div class="tool-call-header" data-action="toggle-tool-call" data-index="${index}">
              <span class="tool-call-chevron" id="chevron-${index}">&#9654;</span>
              <span class="tool-call-name">${escapeHtml(tc.tool)}</span>
              <span class="tool-call-target">${escapeHtml(tc.target)}</span>
              <span class="tool-call-time">${formatTimestamp(tc.timestamp)}</span>
            </div>
            <div class="tool-call-details" id="details-${index}" style="display: none;"></div>
          </div>
        `
          )
          .join("")}
      </div>
    </div>
  `;
}

/**
 * Generate pipeline history HTML
 *
 * @param history - The paginated history items to display
 * @param pagination - Optional pagination metadata (total count, hasMore flag)
 */
export function getHistoryHtml(
  history: PipelineRunSummary[],
  pagination?: HistoryPaginationInfo
): string {
  if (history.length === 0) {
    return `
      <div class="history-section">
        <div class="section-header">
          <h3>Pipeline History</h3>
        </div>
        <div class="empty-state">
          <p>No pipeline runs recorded. Run a pipeline to see history and metrics.</p>
        </div>
      </div>
    `;
  }

  const totalCount = pagination?.totalCount ?? history.length;
  const hasMore = pagination?.hasMore ?? false;

  return `
    <div class="history-section">
      <div class="section-header">
        <h3>Pipeline History</h3>
        <span class="history-count">Showing ${history.length} of ${totalCount}</span>
      </div>
      <div class="history-list">
        ${history
          .map(
            (run) => `
          <div class="history-item" data-issue="${run.issueNumber}" data-action="select-history-run">
            <div class="history-item-main">
              <span class="history-issue">#${run.issueNumber}</span>
              <span class="history-title">${escapeHtml(run.title.substring(0, 30))}${run.title.length > 30 ? "..." : ""}</span>
            </div>
            <div class="history-item-stats">
              <div class="history-progress">
                ${run.stages.map((s) => `<span class="history-dot ${s.status === "complete" ? "complete" : s.status === "skipped" ? "skipped" : "pending"}"></span>`).join("")}
              </div>
              <span class="history-cost">$${run.usage.costUsd.toFixed(2)}</span>
              <span class="history-time">${formatRelativeTime(run.startedAt)}</span>
            </div>
          </div>
        `
          )
          .join("")}
      </div>
      ${
        hasMore
          ? `<div class="history-load-more">
          <button class="load-more-btn" data-action="load-more-history">Load More</button>
        </div>`
          : ""
      }
    </div>
  `;
}

/**
 * Generate the inner HTML for the pipeline progress section (Issue #923).
 * Used by Dashboard.ts for incremental updates via postMessage.
 */
export function getPipelineProgressSectionHtml(
  currentRun: PipelineRunSummary | null,
  backtracks: BacktrackRecord[] = [],
  modelEscalations: ModelEscalationRecord[] = []
): string {
  return (
    getProgressBarHtml(currentRun, backtracks.length) +
    getFeedbackEventsHtml(backtracks, modelEscalations)
  );
}

export { renderBacktrackEventHtml, renderEscalationEventHtml };

/**
 * Get CSS styles for the pipeline tab
 */
export function getPipelineTabStyles(): string {
  return `
    /* Progress Section */
    .progress-section {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }

    .progress-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: var(--spacing-sm);
    }

    .progress-title {
      font-size: 1em;
    }

    .status-badge {
      display: inline-block;
      padding: 2px 8px;
      border-radius: var(--border-radius);
      color: white;
      font-size: 0.8em;
      font-weight: 500;
    }

    .progress-badges {
      display: flex;
      gap: var(--spacing-xs);
      align-items: center;
    }

    .route-badge {
      display: inline-block;
      padding: 2px 8px;
      border-radius: var(--border-radius);
      color: white;
      font-size: 0.75em;
      font-weight: 600;
      letter-spacing: 0.5px;
    }

    .route-trivial {
      background: var(--vscode-charts-green);
    }

    .route-standard {
      background: var(--vscode-charts-blue);
    }

    .route-extensive {
      background: var(--vscode-charts-purple);
    }

    .skipped-count {
      font-style: italic;
      color: var(--vscode-charts-yellow);
    }

    .progress-bar-container {
      height: 8px;
      background: var(--vscode-progressBar-background);
      border-radius: 4px;
      overflow: hidden;
      margin-bottom: var(--spacing-sm);
    }

    .progress-bar {
      height: 100%;
      background: var(--vscode-progressBar-background);
      background: linear-gradient(90deg,
        var(--vscode-charts-blue) 0%,
        var(--vscode-charts-green) 100%
      );
      transition: width 0.3s ease;
    }

    .progress-info {
      display: flex;
      justify-content: space-between;
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
      margin-bottom: var(--spacing-sm);
    }

    .stage-indicators {
      display: flex;
      gap: var(--spacing-xs);
    }

    .stage-dot {
      width: 12px;
      height: 12px;
      border-radius: 50%;
      background: var(--vscode-descriptionForeground);
    }

    .stage-dot.complete {
      background: var(--vscode-charts-green);
    }

    .stage-dot.running {
      background: var(--vscode-charts-blue);
      animation: pulse 1s infinite;
    }

    .stage-dot.failed {
      background: var(--vscode-charts-red);
    }

    .stage-dot.skipped {
      background: var(--vscode-charts-yellow);
    }

    .stage-dot.deferred {
      background: var(--vscode-charts-orange, #ff9f40);
      border: 1px dashed var(--vscode-descriptionForeground, #888);
    }

    @keyframes pulse {
      0%, 100% { opacity: 1; }
      50% { opacity: 0.5; }
    }

    /* Tool Calls Section */
    .tool-calls-section {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }

    .section-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: var(--spacing-sm);
    }

    .section-header h3 {
      font-size: 0.95em;
      font-weight: 500;
    }

    .tool-filter {
      padding: var(--spacing-xs) var(--spacing-sm);
      background: var(--vscode-dropdown-background);
      color: var(--vscode-dropdown-foreground);
      border: 1px solid var(--vscode-dropdown-border);
      border-radius: var(--border-radius);
      font-size: 0.85em;
    }

    .tool-calls-list {
      min-height: 150px;
      overflow-y: auto;
    }

    .tool-call-item {
      border-bottom: 1px solid var(--vscode-panel-border);
    }

    .tool-call-item:last-child {
      border-bottom: none;
    }

    .tool-call-header {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      padding: var(--spacing-sm) 0;
      cursor: pointer;
    }

    .tool-call-header:hover {
      background: var(--vscode-list-hoverBackground);
    }

    .tool-call-chevron {
      font-size: 0.7em;
      color: var(--vscode-descriptionForeground);
      transition: transform 0.2s;
    }

    .tool-call-chevron.expanded {
      transform: rotate(90deg);
    }

    .tool-call-name {
      font-weight: 500;
      color: var(--vscode-symbolIcon-methodForeground);
    }

    .tool-call-target {
      flex: 1;
      color: var(--vscode-descriptionForeground);
      font-size: 0.9em;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }

    .tool-call-time {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
    }

    .tool-call-details {
      padding: var(--spacing-sm) var(--spacing-md);
      background: var(--vscode-textCodeBlock-background);
      border-radius: var(--border-radius);
      margin-bottom: var(--spacing-sm);
      font-size: 0.85em;
    }

    .tool-call-details code,
    .tool-call-details pre {
      font-family: var(--vscode-editor-font-family);
      white-space: pre-wrap;
      word-break: break-all;
    }

    .tool-call-error {
      color: var(--vscode-errorForeground);
    }

    .load-tool-calls-btn {
      padding: var(--spacing-xs) var(--spacing-md);
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
      border: none;
      border-radius: var(--border-radius);
      cursor: pointer;
      font-size: 0.85em;
    }

    .load-tool-calls-btn:hover {
      background: var(--vscode-button-secondaryHoverBackground);
    }

    .tool-calls-hint {
      font-size: 0.8em;
      margin-top: var(--spacing-xs);
    }

    .tool-calls-loading {
      color: var(--vscode-descriptionForeground);
      font-style: italic;
    }

    /* History Section */
    .history-section {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
    }

    .history-list {
      min-height: 150px;
      overflow-y: auto;
    }

    .history-item {
      display: flex;
      justify-content: space-between;
      align-items: center;
      padding: var(--spacing-sm);
      border-bottom: 1px solid var(--vscode-panel-border);
      cursor: pointer;
    }

    .history-item:last-child {
      border-bottom: none;
    }

    .history-item:hover {
      background: var(--vscode-list-hoverBackground);
    }

    .history-item-main {
      display: flex;
      gap: var(--spacing-sm);
      align-items: center;
    }

    .history-issue {
      font-weight: 500;
      color: var(--vscode-textLink-foreground);
    }

    .history-title {
      color: var(--vscode-foreground);
    }

    .history-item-stats {
      display: flex;
      gap: var(--spacing-md);
      align-items: center;
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }

    .history-progress {
      display: flex;
      gap: 2px;
    }

    .history-dot {
      width: 8px;
      height: 8px;
      border-radius: 50%;
      background: var(--vscode-descriptionForeground);
    }

    .history-dot.complete {
      background: var(--vscode-charts-green);
    }

    .history-dot.skipped {
      background: var(--vscode-charts-yellow);
    }

    .history-count {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }

    .history-load-more {
      text-align: center;
      padding: var(--spacing-sm) 0 0;
    }

    .load-more-btn {
      padding: var(--spacing-xs) var(--spacing-md);
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
      border: none;
      border-radius: var(--border-radius);
      cursor: pointer;
      font-size: 0.85em;
    }

    .load-more-btn:hover {
      background: var(--vscode-button-secondaryHoverBackground);
    }

    /* Feedback Events (Issue #1349) */
    .retry-depth {
      color: var(--vscode-editorWarning-foreground, #f5a623);
      font-size: 0.85em;
      font-weight: 600;
    }

    .feedback-events {
      margin-top: var(--spacing-sm);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-xs) var(--spacing-sm);
    }

    .feedback-events-title {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      cursor: pointer;
      font-weight: 600;
      font-size: 0.9em;
      list-style: none;
    }

    .feedback-events-title::-webkit-details-marker {
      display: none;
    }

    .feedback-count {
      padding: 1px 6px;
      border-radius: 10px;
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
      font-size: 0.8em;
    }

    .feedback-events-list {
      margin-top: var(--spacing-xs);
      display: flex;
      flex-direction: column;
      gap: 2px;
    }

    .feedback-event {
      font-size: 0.85em;
      padding: 2px 0;
    }

    .feedback-warning {
      color: var(--vscode-editorWarning-foreground, #f5a623);
    }

    .feedback-blocked {
      color: var(--vscode-editorError-foreground, #f14c4c);
    }

    .feedback-escalation {
      color: var(--vscode-descriptionForeground, #888);
    }
  `;
}
