/**
 * PipelineSummaryHtml - HTML template generator for the pipeline summary WebView
 *
 * Generates the HTML, CSS, and JavaScript for rendering:
 * - Pipeline completion metrics (tokens, cost, duration)
 * - Stage timeline with per-stage breakdown
 * - Action buttons (Export, Reset & Start New)
 *
 * @see docs/ARCHITECTURE.md for WebView patterns
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import type { PipelineState } from "../../services/PipelineStateService";
import { getStageBudget } from "../../utils/incrediConfig";

/**
 * Generate nonce for script security
 */
function getNonce(): string {
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
function escapeHtml(text: string): string {
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
function formatStageName(stage: string): string {
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
 * Format duration in milliseconds to human-readable string
 */
function formatDuration(ms: number): string {
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
 * Format timestamp for display
 */
function formatTimestamp(isoString: string): string {
  const date = new Date(isoString);
  return date.toLocaleString("en-US", {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hour12: true,
  });
}

/**
 * Get status icon for a stage
 */
function getStatusIcon(status: string): string {
  switch (status) {
    case "complete":
      return "&#10003;"; // checkmark
    case "failed":
      return "&#10007;"; // X
    case "skipped":
      return "&#8594;"; // arrow
    case "running":
      return "&#8987;"; // hourglass
    default:
      return "&#9711;"; // circle
  }
}

/**
 * Get status color class for a stage
 */
function getStatusClass(status: string): string {
  switch (status) {
    case "complete":
      return "status-complete";
    case "failed":
      return "status-failed";
    case "skipped":
      return "status-skipped";
    case "running":
      return "status-running";
    default:
      return "status-pending";
  }
}

/**
 * Calculate total duration from pipeline state
 */
function calculateTotalDuration(state: PipelineState): number {
  const startTime = new Date(state.started_at).getTime();
  const endTime = new Date(state.updated_at ?? state.started_at).getTime();
  return endTime - startTime;
}

/**
 * Get CSS styles for the summary panel
 */
function getStyles(): string {
  return `
    :root {
      --spacing-xs: 4px;
      --spacing-sm: 8px;
      --spacing-md: 16px;
      --spacing-lg: 24px;
      --spacing-xl: 32px;
      --border-radius: 6px;
    }

    * {
      box-sizing: border-box;
      margin: 0;
      padding: 0;
    }

    body {
      font-family: var(--vscode-font-family);
      font-size: var(--vscode-font-size);
      color: var(--vscode-foreground);
      background: var(--vscode-editor-background);
      line-height: 1.5;
      padding: var(--spacing-lg);
    }

    .summary-container {
      max-width: 800px;
      margin: 0 auto;
    }

    /* Header Section */
    .summary-header {
      text-align: center;
      margin-bottom: var(--spacing-xl);
      padding-bottom: var(--spacing-lg);
      border-bottom: 1px solid var(--vscode-panel-border);
    }

    .success-icon {
      font-size: 48px;
      color: var(--vscode-charts-green);
      margin-bottom: var(--spacing-md);
    }

    .summary-header h1 {
      font-size: 1.5em;
      font-weight: 600;
      margin-bottom: var(--spacing-sm);
    }

    .summary-header .issue-info {
      color: var(--vscode-descriptionForeground);
      font-size: 1.1em;
    }

    .summary-header .timestamp {
      color: var(--vscode-descriptionForeground);
      font-size: 0.9em;
      margin-top: var(--spacing-xs);
    }

    /* Metrics Cards */
    .metrics-grid {
      display: grid;
      grid-template-columns: repeat(4, 1fr);
      gap: var(--spacing-md);
      margin-bottom: var(--spacing-xl);
    }

    @media (max-width: 700px) {
      .metrics-grid {
        grid-template-columns: repeat(2, 1fr);
      }
    }

    .metric-card {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
      text-align: center;
    }

    .metric-card.highlight {
      border-color: var(--vscode-charts-green);
      background: linear-gradient(135deg,
        var(--vscode-editorWidget-background) 0%,
        rgba(75, 192, 75, 0.1) 100%
      );
    }

    .metric-value {
      font-size: 1.6em;
      font-weight: 600;
      color: var(--vscode-foreground);
      margin-bottom: var(--spacing-xs);
    }

    .metric-label {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }

    .metric-detail {
      font-size: 0.75em;
      color: var(--vscode-descriptionForeground);
      margin-top: var(--spacing-xs);
    }

    /* Stage Timeline */
    .timeline-section {
      margin-bottom: var(--spacing-xl);
    }

    .section-title {
      font-size: 1.1em;
      font-weight: 500;
      margin-bottom: var(--spacing-md);
      padding-bottom: var(--spacing-sm);
      border-bottom: 1px solid var(--vscode-panel-border);
    }

    .stage-timeline {
      position: relative;
    }

    .stage-item {
      display: flex;
      align-items: flex-start;
      padding: var(--spacing-sm) 0;
      position: relative;
    }

    .stage-item::before {
      content: '';
      position: absolute;
      left: 15px;
      top: 32px;
      bottom: -8px;
      width: 2px;
      background: var(--vscode-panel-border);
    }

    .stage-item:last-child::before {
      display: none;
    }

    .stage-icon {
      width: 32px;
      height: 32px;
      border-radius: 50%;
      display: flex;
      align-items: center;
      justify-content: center;
      font-size: 14px;
      flex-shrink: 0;
      z-index: 1;
      background: var(--vscode-editor-background);
      border: 2px solid;
    }

    .status-complete .stage-icon {
      border-color: var(--vscode-charts-green);
      color: var(--vscode-charts-green);
    }

    .status-failed .stage-icon {
      border-color: var(--vscode-charts-red);
      color: var(--vscode-charts-red);
    }

    .status-skipped .stage-icon {
      border-color: var(--vscode-charts-yellow);
      color: var(--vscode-charts-yellow);
    }

    .status-running .stage-icon {
      border-color: var(--vscode-charts-blue);
      color: var(--vscode-charts-blue);
    }

    .status-pending .stage-icon {
      border-color: var(--vscode-descriptionForeground);
      color: var(--vscode-descriptionForeground);
    }

    .stage-content {
      flex: 1;
      margin-left: var(--spacing-md);
      min-width: 0;
    }

    .stage-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: var(--spacing-xs);
    }

    .stage-name {
      font-weight: 500;
    }

    .stage-duration {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }

    .stage-details {
      display: flex;
      gap: var(--spacing-md);
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
    }

    .stage-tokens, .stage-cost, .stage-model {
      display: flex;
      align-items: center;
      gap: var(--spacing-xs);
    }

    .stage-model {
      font-style: italic;
    }

    .stage-cost.over-budget {
      color: var(--vscode-charts-orange, #cca700);
      font-weight: 600;
    }

    /* Action Buttons */
    .actions-section {
      display: flex;
      gap: var(--spacing-md);
      justify-content: center;
      padding-top: var(--spacing-lg);
      border-top: 1px solid var(--vscode-panel-border);
    }

    .action-btn {
      padding: var(--spacing-sm) var(--spacing-lg);
      border-radius: var(--border-radius);
      font-size: 0.95em;
      cursor: pointer;
      border: none;
      display: inline-flex;
      align-items: center;
      gap: var(--spacing-sm);
      transition: background 0.2s;
    }

    .action-btn.primary {
      background: var(--vscode-button-background);
      color: var(--vscode-button-foreground);
    }

    .action-btn.primary:hover {
      background: var(--vscode-button-hoverBackground);
    }

    .action-btn.secondary {
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
    }

    .action-btn.secondary:hover {
      background: var(--vscode-button-secondaryHoverBackground);
    }

    /* Token Breakdown */
    .token-breakdown {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
      margin-bottom: var(--spacing-xl);
    }

    .token-row {
      display: flex;
      justify-content: space-between;
      padding: var(--spacing-xs) 0;
      font-size: 0.9em;
    }

    .token-label {
      color: var(--vscode-descriptionForeground);
    }

    .token-value {
      font-weight: 500;
    }

    .token-row.total {
      border-top: 1px solid var(--vscode-panel-border);
      margin-top: var(--spacing-sm);
      padding-top: var(--spacing-sm);
      font-weight: 600;
    }

    /* Confirmation Modal */
    .modal-overlay {
      display: none;
      position: fixed;
      top: 0;
      left: 0;
      right: 0;
      bottom: 0;
      background: rgba(0, 0, 0, 0.5);
      z-index: 1000;
      align-items: center;
      justify-content: center;
    }

    .modal-overlay.visible {
      display: flex;
    }

    .modal-content {
      background: var(--vscode-editor-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-lg);
      max-width: 400px;
      text-align: center;
    }

    .modal-content h3 {
      margin-bottom: var(--spacing-md);
    }

    .modal-content p {
      color: var(--vscode-descriptionForeground);
      margin-bottom: var(--spacing-lg);
    }

    .modal-actions {
      display: flex;
      gap: var(--spacing-md);
      justify-content: center;
    }
  `;
}

/**
 * Get JavaScript for the summary panel
 */
function getScript(state: PipelineState): string {
  const exportData = JSON.stringify(state, null, 2);

  return `
    (function() {
      const vscode = acquireVsCodeApi();
      const exportData = ${JSON.stringify(exportData)};

      // Export button
      document.getElementById('exportBtn')?.addEventListener('click', () => {
        vscode.postMessage({ type: 'export', data: exportData });
      });

      // Reset button - show confirmation modal
      document.getElementById('resetBtn')?.addEventListener('click', () => {
        document.getElementById('confirmModal').classList.add('visible');
      });

      // Modal cancel
      document.getElementById('modalCancel')?.addEventListener('click', () => {
        document.getElementById('confirmModal').classList.remove('visible');
      });

      // Modal confirm
      document.getElementById('modalConfirm')?.addEventListener('click', () => {
        document.getElementById('confirmModal').classList.remove('visible');
        vscode.postMessage({ type: 'reset' });
      });

      // Close modal on overlay click
      document.getElementById('confirmModal')?.addEventListener('click', (e) => {
        if (e.target.id === 'confirmModal') {
          document.getElementById('confirmModal').classList.remove('visible');
        }
      });

      // Handle panel close without reset
      window.addEventListener('beforeunload', () => {
        // Panel is being closed - handled by extension
      });
    })();
  `;
}

/**
 * Generate metrics cards HTML
 */
function getMetricsCardsHtml(state: PipelineState): string {
  const totalDuration = calculateTotalDuration(state);
  const tokens = state.tokens;
  // `total_input` is COMBINED (raw input + cache reads — Go scheduler
  // convention), so total tokens is input + output; adding cache_read again
  // double-counted it and the old "cache efficiency" pinned at ~50% (#262).
  const totalTokens = (tokens?.total_input ?? 0) + (tokens?.total_output ?? 0);
  const cost = tokens?.estimated_cost_usd ?? 0;

  // Cache hit rate: cache reads over all billed-as-input tokens.
  const totalInput = tokens?.total_input ?? 0;
  const cacheEfficiency = totalInput > 0 ? ((tokens?.total_cache_read ?? 0) / totalInput) * 100 : 0;

  // Calculate completed stages
  const stages = Object.values(state.stages);
  const completedStages = stages.filter(
    (s) => s.status === "complete" || s.status === "skipped"
  ).length;

  return `
    <div class="metrics-grid">
      <div class="metric-card highlight">
        <div class="metric-value">${formatDuration(totalDuration)}</div>
        <div class="metric-label">Total Duration</div>
      </div>
      <div class="metric-card">
        <div class="metric-value">${totalTokens.toLocaleString()}</div>
        <div class="metric-label">Total Tokens</div>
        <div class="metric-detail">${cacheEfficiency.toFixed(0)}% cache hit</div>
      </div>
      <div class="metric-card">
        <div class="metric-value">$${cost.toFixed(4)}</div>
        <div class="metric-label">Estimated Cost</div>
      </div>
      <div class="metric-card">
        <div class="metric-value">${completedStages}/${stages.length}</div>
        <div class="metric-label">Stages Complete</div>
      </div>
    </div>
  `;
}

/**
 * Generate token breakdown HTML
 */
function getTokenBreakdownHtml(state: PipelineState): string {
  const tokens = state.tokens;
  return `
    <div class="token-breakdown">
      <div class="token-row">
        <span class="token-label">Input Tokens</span>
        <span class="token-value">${(tokens?.total_input ?? 0).toLocaleString()}</span>
      </div>
      <div class="token-row">
        <span class="token-label">Output Tokens</span>
        <span class="token-value">${(tokens?.total_output ?? 0).toLocaleString()}</span>
      </div>
      <div class="token-row">
        <span class="token-label">Cache Read</span>
        <span class="token-value">${(tokens?.total_cache_read ?? 0).toLocaleString()}</span>
      </div>
      <div class="token-row">
        <span class="token-label">Cache Creation</span>
        <span class="token-value">${(tokens?.total_cache_creation ?? 0).toLocaleString()}</span>
      </div>
      <div class="token-row total">
        <span class="token-label">Estimated Cost</span>
        <span class="token-value">$${(tokens?.estimated_cost_usd ?? 0).toFixed(4)}</span>
      </div>
    </div>
  `;
}

/**
 * Generate stage timeline HTML
 */
function getStageTimelineHtml(state: PipelineState): string {
  const stageOrder = [
    "issue-pickup",
    "feature-planning",
    "feature-dev",
    "feature-validate",
    "pr-create",
    "pr-merge",
  ];

  const stageItems = stageOrder.map((stageName) => {
    const stageData = state.stages[stageName as keyof typeof state.stages];
    const statusClass = getStatusClass(stageData.status);
    const statusIcon = getStatusIcon(stageData.status);

    // Get per-stage token data if available
    const perStage = state.tokens?.per_stage?.[stageName as keyof typeof state.tokens.per_stage];
    const stageTokens = perStage
      ? perStage.input + perStage.output + (perStage.cache_read ?? 0)
      : 0;
    const stageCost = perStage ? (perStage.cost_usd ?? 0) : 0;

    // Model info from per_stage tokens (Issue #1006) or stage model_selection
    const stageModel =
      ((perStage as Record<string, unknown> | undefined)?.["model"] as string | undefined) ??
      stageData.model_selection?.model;

    // Check if stage cost exceeds budget (Issue #638)
    const budget = getStageBudget(stageName as PipelineStage);
    const overBudget = budget && stageCost > budget.maxCostUsd;

    const durationText = stageData.duration_ms
      ? formatDuration(stageData.duration_ms)
      : stageData.status === "skipped"
        ? "Skipped"
        : "-";

    return `
      <div class="stage-item ${statusClass}">
        <div class="stage-icon">${statusIcon}</div>
        <div class="stage-content">
          <div class="stage-header">
            <span class="stage-name">${formatStageName(stageName)}</span>
            <span class="stage-duration">${durationText}</span>
          </div>
          ${
            stageData.status === "complete" || stageData.status === "failed"
              ? `<div class="stage-details">
                ${stageModel ? `<span class="stage-model">${escapeHtml(stageModel)}</span>` : ""}
                <span class="stage-tokens">${stageTokens.toLocaleString()} tokens</span>
                <span class="stage-cost${overBudget ? " over-budget" : ""}">$${stageCost.toFixed(4)}${overBudget ? ` (budget: $${budget.maxCostUsd.toFixed(2)})` : ""}</span>
              </div>`
              : ""
          }
          ${
            stageData.error
              ? `<div class="stage-error" style="color: var(--vscode-charts-red); font-size: 0.85em; margin-top: 4px;">${escapeHtml(stageData.error)}</div>`
              : ""
          }
        </div>
      </div>
    `;
  });

  return `
    <div class="timeline-section">
      <h3 class="section-title">Stage Timeline</h3>
      <div class="stage-timeline">
        ${stageItems.join("")}
      </div>
    </div>
  `;
}

/**
 * Generate the full pipeline summary HTML
 */
export function getPipelineSummaryHtml(webview: vscode.Webview, state: PipelineState): string {
  const nonce = getNonce();

  return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}';">
  <title>Pipeline Complete</title>
  <style>
    ${getStyles()}
  </style>
</head>
<body>
  <div class="summary-container">
    <header class="summary-header">
      <div class="success-icon">&#10003;</div>
      <h1>Pipeline Complete!</h1>
      <div class="issue-info">
        <strong>Issue #${state.issue_number}</strong> - ${escapeHtml(state.title)}
      </div>
      <div class="timestamp">Completed ${formatTimestamp(state.updated_at ?? state.started_at)}</div>
    </header>

    ${getMetricsCardsHtml(state)}

    ${getTokenBreakdownHtml(state)}

    ${getStageTimelineHtml(state)}

    <div class="actions-section">
      <button class="action-btn secondary" id="exportBtn">
        Export Summary
      </button>
      <button class="action-btn primary" id="resetBtn">
        Reset &amp; Start New
      </button>
    </div>
  </div>

  <!-- Confirmation Modal -->
  <div class="modal-overlay" id="confirmModal">
    <div class="modal-content">
      <h3>Reset Pipeline?</h3>
      <p>This will clear all context files and pipeline state. Make sure you've exported any data you need.</p>
      <div class="modal-actions">
        <button class="action-btn secondary" id="modalCancel">Cancel</button>
        <button class="action-btn primary" id="modalConfirm">Reset</button>
      </div>
    </div>
  </div>

  <script nonce="${nonce}">
    ${getScript(state)}
  </script>
</body>
</html>`;
}
