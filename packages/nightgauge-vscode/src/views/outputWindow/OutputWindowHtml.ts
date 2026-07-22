/**
 * OutputWindowHtml - HTML template generator for the output window WebView
 *
 * Generates the HTML, CSS, and JavaScript for rendering:
 * - Real-time streaming output
 * - Stage progress indicator
 * - Token usage display
 * - Collapsible sections
 * - User input field
 *
 * @see docs/ARCHITECTURE.md for WebView patterns
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import type {
  OutputEntry,
  StageProgress,
  StageStatus,
  OutputLevel,
  SlotInfo,
} from "./OutputWindowState";
import type { ContentType } from "./contentFormatter";

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
 * Escape HTML special characters for security
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

// Markdown rendering is handled client-side using marked.js library
// See getScript() for the implementation

/**
 * Format stage name for display
 */
export function formatStageName(stage: PipelineStage): string {
  const labels: Record<PipelineStage, string> = {
    "pipeline-start": "Initialize",
    "issue-pickup": "Issue Pickup",
    "feature-planning": "Feature Planning",
    "feature-dev": "Feature Development",
    "feature-validate": "Feature Validation",
    "pr-create": "PR Creation",
    "pr-merge": "PR Merge",
    "pipeline-finish": "Completion",
  };
  return labels[stage] ?? stage;
}

/**
 * Format timestamp for display (HH:MM:SS)
 */
function formatTimestamp(date: Date): string {
  return date.toLocaleTimeString("en-US", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

/**
 * Get level badge class
 */
function getLevelClass(level: OutputLevel): string {
  const levelClasses: Record<OutputLevel, string> = {
    info: "level-info",
    debug: "level-debug",
    warning: "level-warning",
    error: "level-error",
    tool: "level-tool",
    user: "level-user",
  };
  return levelClasses[level] || "level-info";
}

/**
 * Get level badge text
 */
function getLevelBadge(level: OutputLevel): string {
  const levelLabels: Record<OutputLevel, string> = {
    info: "INFO",
    debug: "DEBUG",
    warning: "WARN",
    error: "ERROR",
    tool: "TOOL",
    user: "USER",
  };
  return levelLabels[level] || "INFO";
}

/**
 * Generate single output entry HTML
 * Note: Markdown rendering happens client-side via marked.js
 * Content type formatting also happens client-side (Issue #428)
 */
function getEntryHtml(entry: OutputEntry): string {
  const levelClass = getLevelClass(entry.level);
  const levelBadge = getLevelBadge(entry.level);
  const timestamp = formatTimestamp(entry.timestamp);
  const stageName = entry.stage ? formatStageName(entry.stage) : "";

  // Store raw text in data attribute for client-side markdown rendering
  // Escape for HTML attribute safety
  const escapedText = escapeHtml(entry.text).replace(/"/g, "&quot;").replace(/\n/g, "&#10;");

  // Content type and language for formatted rendering (Issue #428)
  const contentTypeAttr = entry.contentType ? ` data-content-type="${entry.contentType}"` : "";
  const languageAttr = entry.language ? ` data-language="${entry.language}"` : "";
  const formattedClass = entry.contentType ? ` formatted-content content-${entry.contentType}` : "";

  const mainContent = `
    <div class="entry-header">
      <span class="entry-time">${timestamp}</span>
      <span class="entry-badge ${levelClass}">${levelBadge}</span>
      ${stageName ? `<span class="entry-stage">[${stageName}]</span>` : ""}
    </div>
    <div class="entry-content markdown-body${formattedClass}" data-raw-text="${escapedText}"${contentTypeAttr}${languageAttr}></div>
  `;

  if (entry.collapsible && entry.details) {
    return `
      <div class="output-entry ${levelClass} collapsible ${entry.collapsed ? "collapsed" : ""}" data-entry-id="${entry.id}">
        <div class="entry-toggle">
          <span class="toggle-icon">${entry.collapsed ? "▶" : "▼"}</span>
          ${mainContent}
        </div>
        <div class="entry-details" ${entry.collapsed ? 'style="display: none;"' : ""}>
          <pre>${escapeHtml(entry.details)}</pre>
        </div>
      </div>
    `;
  }

  return `
    <div class="output-entry ${levelClass}" data-entry-id="${entry.id}">
      ${mainContent}
    </div>
  `;
}

/**
 * Generate output entries HTML
 *
 * All entries with a stage are wrapped in collapsed <details> groups.
 * Completed/errored/skipped stages show their final status icon.
 * Running or unknown stages show a running indicator.
 */
function getEntriesHtml(entries: OutputEntry[], stages?: StageProgress[]): string {
  if (entries.length === 0) {
    return `
      <div class="empty-state">
        <p>No output yet. Start a pipeline to see activity here.</p>
      </div>
    `;
  }

  // Build map of stage statuses
  const stageStatuses = new Map<string, StageStatus>();
  if (stages) {
    for (const s of stages) {
      stageStatuses.set(s.stage, s.status);
    }
  }

  // Group entries by stage, preserving order
  const result: string[] = [];
  const stageBuffers = new Map<string, string[]>();
  const stageOrder: string[] = [];

  for (const entry of entries) {
    if (entry.stage) {
      if (!stageBuffers.has(entry.stage)) {
        stageBuffers.set(entry.stage, []);
        stageOrder.push(entry.stage);
        // Mark position in result for later insertion
        result.push(`__STAGE_GROUP_${entry.stage}__`);
      }
      stageBuffers.get(entry.stage)!.push(getEntryHtml(entry));
    } else {
      result.push(getEntryHtml(entry));
    }
  }

  if (stageOrder.length === 0) {
    return entries.map((entry) => getEntryHtml(entry)).join("");
  }

  // Replace placeholders with <details> groups
  const statusIcons: Record<string, string> = {
    complete: "\u2713",
    error: "\u2717",
    skipped: "\u21B7",
  };
  const statusClasses: Record<string, string> = {
    complete: "stage-complete",
    error: "stage-error",
    skipped: "stage-skipped",
  };

  return result
    .map((html) => {
      for (const stage of stageOrder) {
        if (html === `__STAGE_GROUP_${stage}__`) {
          const status = stageStatuses.get(stage);
          const isTerminal = status === "complete" || status === "error" || status === "skipped";
          const icon = isTerminal ? statusIcons[status!] || "" : "\u25B6";
          const cls = isTerminal ? statusClasses[status!] || "" : "stage-running";
          const spinnerHtml = isTerminal ? "" : '<span class="stage-group-spinner"></span>';
          const stageName = formatStageName(stage as PipelineStage);
          const entryHtmls = stageBuffers.get(stage)!;
          return `<details class="stage-group ${cls}" id="stage-group-${stage}">
            <summary class="stage-group-summary">
              ${spinnerHtml}<span class="stage-group-icon">${icon}</span>
              <span class="stage-group-name">${stageName}</span>
              <span class="stage-group-count">${entryHtmls.length} entr${entryHtmls.length === 1 ? "y" : "ies"}</span>
            </summary>
            ${entryHtmls.join("")}
          </details>`;
        }
      }
      return html;
    })
    .join("");
}

/**
 * Get CSS styles
 */
function getStyles(): string {
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

    body {
      font-family: var(--vscode-font-family);
      font-size: var(--vscode-font-size);
      color: var(--vscode-foreground);
      background: var(--vscode-editor-background);
      line-height: 1.5;
      height: 100vh;
      display: flex;
      flex-direction: column;
      overflow: hidden;
    }

    .output-window {
      display: flex;
      flex-direction: column;
      height: 100%;
    }

    /* Header */
    .output-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      padding: var(--spacing-sm) var(--spacing-md);
      border-bottom: 1px solid var(--vscode-panel-border);
      background: var(--vscode-editorWidget-background);
    }

    .output-title {
      font-weight: 600;
      font-size: 1.1em;
      flex: 0 1 auto;
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      padding-right: var(--spacing-sm);
    }

    .header-actions {
      display: flex;
      flex-wrap: wrap;
      gap: var(--spacing-sm);
      row-gap: var(--spacing-xs);
      align-items: center;
      justify-content: flex-end;
      flex: 1 0 auto;
      min-width: 0;
    }

    .action-btn {
      display: inline-flex;
      align-items: center;
      gap: var(--spacing-xs);
      padding: var(--spacing-xs) var(--spacing-sm);
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
      border: none;
      border-radius: var(--border-radius);
      cursor: pointer;
      font-size: 0.85em;
    }

    .action-btn:hover {
      background: var(--vscode-button-secondaryHoverBackground);
    }

    .action-btn.danger {
      background: var(--vscode-inputValidation-errorBackground);
      color: var(--vscode-inputValidation-errorForeground);
      border: 1px solid var(--vscode-inputValidation-errorBorder);
    }

    .action-btn.danger:hover:not(:disabled) {
      opacity: 0.9;
    }

    /* Disabled state for pipeline-dependent buttons (Issue #431) */
    .action-btn:disabled,
    .action-btn.disabled {
      opacity: 0.5;
      cursor: not-allowed;
      pointer-events: none;
    }

    .action-btn.danger:disabled,
    .action-btn.danger.disabled {
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-disabledForeground);
      border-color: var(--vscode-panel-border);
    }

    /* Tool Call Indicators */
    .tool-indicator {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      padding: var(--spacing-xs) var(--spacing-sm);
      margin: var(--spacing-xs) 0;
      background: var(--vscode-editorWidget-background);
      border-radius: var(--border-radius);
      border-left: 3px solid var(--vscode-charts-blue);
      font-size: 0.9em;
      transition: opacity 0.3s ease;
    }

    .tool-indicator.complete {
      opacity: 0.7;
      border-left-color: var(--vscode-charts-green);
    }

    .tool-indicator-spinner {
      width: 14px;
      height: 14px;
      border: 2px solid var(--vscode-charts-blue);
      border-top-color: transparent;
      border-radius: 50%;
      animation: spin 1s linear infinite;
      flex-shrink: 0;
    }

    .tool-indicator.complete .tool-indicator-spinner {
      display: none;
    }

    .tool-indicator-icon {
      font-size: 1em;
      flex-shrink: 0;
    }

    .tool-indicator.complete .tool-indicator-icon::before {
      content: '\\2713';
      color: var(--vscode-charts-green);
    }

    .tool-indicator-label {
      font-weight: 500;
      color: var(--vscode-foreground);
    }

    .tool-indicator-target {
      color: var(--vscode-descriptionForeground);
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    /* Tool type colors */
    .tool-indicator.tool-edit {
      border-left-color: var(--vscode-charts-yellow);
    }
    .tool-indicator.tool-edit .tool-indicator-spinner {
      border-color: var(--vscode-charts-yellow);
      border-top-color: transparent;
    }

    .tool-indicator.tool-read {
      border-left-color: var(--vscode-charts-blue);
    }

    .tool-indicator.tool-write {
      border-left-color: var(--vscode-charts-green);
    }
    .tool-indicator.tool-write .tool-indicator-spinner {
      border-color: var(--vscode-charts-green);
      border-top-color: transparent;
    }

    .tool-indicator.tool-bash {
      border-left-color: var(--vscode-charts-purple);
    }
    .tool-indicator.tool-bash .tool-indicator-spinner {
      border-color: var(--vscode-charts-purple);
      border-top-color: transparent;
    }

    .tool-indicator.tool-search {
      border-left-color: var(--vscode-charts-orange);
    }
    .tool-indicator.tool-search .tool-indicator-spinner {
      border-color: var(--vscode-charts-orange);
      border-top-color: transparent;
    }

    .tool-indicator.tool-task {
      border-left-color: var(--vscode-charts-red);
    }
    .tool-indicator.tool-task .tool-indicator-spinner {
      border-color: var(--vscode-charts-red);
      border-top-color: transparent;
    }

    .tool-indicator.tool-web {
      border-left-color: var(--vscode-charts-blue);
    }

    .tool-indicator.tool-question {
      border-left-color: var(--vscode-charts-yellow);
    }

    .tool-indicator.tool-todo {
      border-left-color: var(--vscode-charts-green);
    }

    /* Question Prompt Styles (Issue #118) */
    .question-prompt {
      display: flex;
      flex-direction: column;
      gap: var(--spacing-md);
      padding: var(--spacing-md);
      margin: var(--spacing-sm) 0;
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-charts-yellow);
      border-radius: var(--border-radius);
      animation: slideIn 0.3s ease-out;
    }

    @keyframes slideIn {
      from {
        opacity: 0;
        transform: translateY(-10px);
      }
      to {
        opacity: 1;
        transform: translateY(0);
      }
    }

    .question-prompt.waiting {
      border-color: var(--vscode-charts-yellow);
    }

    .question-prompt.answered {
      opacity: 0.7;
      border-color: var(--vscode-charts-green);
    }

    .question-header {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
    }

    .question-icon {
      font-size: 1.2em;
      color: var(--vscode-charts-yellow);
    }

    .question-prompt.answered .question-icon {
      color: var(--vscode-charts-green);
    }

    .question-badge {
      padding: 2px 8px;
      background: var(--vscode-charts-yellow);
      color: var(--vscode-editor-background);
      border-radius: 3px;
      font-size: 0.75em;
      font-weight: 600;
      text-transform: uppercase;
    }

    .question-prompt.answered .question-badge {
      background: var(--vscode-charts-green);
    }

    .question-text {
      font-weight: 500;
      color: var(--vscode-foreground);
      margin-bottom: var(--spacing-xs);
    }

    .question-options {
      display: flex;
      flex-wrap: wrap;
      gap: var(--spacing-sm);
    }

    .question-option-btn {
      display: flex;
      flex-direction: column;
      align-items: flex-start;
      gap: 2px;
      padding: var(--spacing-sm) var(--spacing-md);
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      cursor: pointer;
      font-size: 0.9em;
      text-align: left;
      min-width: 120px;
      max-width: 280px;
      transition: all 0.15s ease;
    }

    .question-option-btn:hover {
      background: var(--vscode-button-secondaryHoverBackground);
      border-color: var(--vscode-focusBorder);
    }

    .question-option-btn:focus {
      outline: none;
      border-color: var(--vscode-focusBorder);
      box-shadow: 0 0 0 1px var(--vscode-focusBorder);
    }

    .question-option-btn.selected {
      background: var(--vscode-button-background);
      color: var(--vscode-button-foreground);
      border-color: var(--vscode-button-background);
    }

    .question-option-btn:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }

    .option-label {
      font-weight: 500;
    }

    .option-description {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
      line-height: 1.3;
    }

    .question-option-btn.selected .option-description {
      color: var(--vscode-button-foreground);
      opacity: 0.9;
    }

    /* Multi-select checkbox style */
    .question-options.multi-select .question-option-btn {
      position: relative;
      padding-left: calc(var(--spacing-md) + 20px);
    }

    .question-options.multi-select .question-option-btn::before {
      content: '';
      position: absolute;
      left: var(--spacing-sm);
      top: 50%;
      transform: translateY(-50%);
      width: 14px;
      height: 14px;
      border: 1px solid var(--vscode-checkbox-border);
      background: var(--vscode-checkbox-background);
      border-radius: 2px;
    }

    .question-options.multi-select .question-option-btn.selected::before {
      background: var(--vscode-checkbox-selectBackground);
      border-color: var(--vscode-checkbox-selectBorder);
    }

    .question-options.multi-select .question-option-btn.selected::after {
      content: '\\2713';
      position: absolute;
      left: calc(var(--spacing-sm) + 2px);
      top: 50%;
      transform: translateY(-50%);
      color: var(--vscode-checkbox-foreground);
      font-size: 10px;
      font-weight: bold;
    }

    /* Custom text input option */
    .question-custom-input {
      display: flex;
      gap: var(--spacing-sm);
      margin-top: var(--spacing-sm);
      width: 100%;
    }

    .question-custom-input input {
      flex: 1;
      padding: var(--spacing-sm);
      background: var(--vscode-input-background);
      color: var(--vscode-input-foreground);
      border: 1px solid var(--vscode-input-border);
      border-radius: var(--border-radius);
      font-size: 0.9em;
    }

    .question-custom-input input:focus {
      outline: none;
      border-color: var(--vscode-focusBorder);
    }

    .question-actions {
      display: flex;
      gap: var(--spacing-sm);
      margin-top: var(--spacing-sm);
      padding-top: var(--spacing-sm);
      border-top: 1px solid var(--vscode-panel-border);
    }

    .question-submit-btn {
      padding: var(--spacing-sm) var(--spacing-md);
      background: var(--vscode-button-background);
      color: var(--vscode-button-foreground);
      border: none;
      border-radius: var(--border-radius);
      cursor: pointer;
      font-weight: 500;
    }

    .question-submit-btn:hover {
      background: var(--vscode-button-hoverBackground);
    }

    .question-submit-btn:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }

    .question-cancel-btn {
      padding: var(--spacing-sm) var(--spacing-md);
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
      border: none;
      border-radius: var(--border-radius);
      cursor: pointer;
    }

    .question-cancel-btn:hover {
      background: var(--vscode-button-secondaryHoverBackground);
    }

    /* Question answered state */
    .question-answered-text {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      padding: var(--spacing-xs) var(--spacing-sm);
      background: var(--vscode-textCodeBlock-background);
      border-radius: var(--border-radius);
      font-size: 0.9em;
    }

    .question-answered-text .answer-label {
      color: var(--vscode-descriptionForeground);
    }

    .question-answered-text .answer-value {
      color: var(--vscode-foreground);
      font-weight: 500;
    }

    /* Pulse animation for active indicators */
    @keyframes pulse {
      0%, 100% { opacity: 1; }
      50% { opacity: 0.7; }
    }

    .tool-indicator.active {
      animation: pulse 2s ease-in-out infinite;
    }

    /* Bounce animation for certain tools */
    @keyframes bounce {
      0%, 100% { transform: translateY(0); }
      50% { transform: translateY(-2px); }
    }

    .tool-indicator.bounce .tool-indicator-icon {
      animation: bounce 0.6s ease infinite;
    }

    /* Tool Summary */
    .tool-summary {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      padding: var(--spacing-sm);
      margin: var(--spacing-sm) 0;
      background: var(--vscode-editorWidget-background);
      border-radius: var(--border-radius);
      border: 1px solid var(--vscode-panel-border);
      font-size: 0.9em;
    }

    .tool-summary-icon {
      color: var(--vscode-charts-green);
    }

    .tool-summary-text {
      color: var(--vscode-descriptionForeground);
    }

    .tool-summary-count {
      font-weight: 500;
      color: var(--vscode-foreground);
    }

    /* Execution Mode Indicator (Issue #496) */
    .mode-indicator {
      display: flex;
      align-items: center;
      gap: var(--spacing-xs);
      padding: var(--spacing-xs) var(--spacing-sm);
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      font-size: 0.85em;
    }

    .mode-indicator-icon {
      font-size: 1em;
    }

    .mode-indicator-label {
      font-weight: 500;
      text-transform: uppercase;
      letter-spacing: 0.5px;
      font-size: 0.8em;
    }

    .mode-indicator.mode-headless {
      border-color: var(--vscode-charts-blue);
    }

    .mode-indicator.mode-headless .mode-indicator-icon {
      color: var(--vscode-charts-blue);
    }

    .mode-indicator.mode-headless .mode-indicator-label {
      color: var(--vscode-charts-blue);
    }

    .mode-indicator.mode-interactive {
      border-color: var(--vscode-charts-green);
    }

    .mode-indicator.mode-interactive .mode-indicator-icon {
      color: var(--vscode-charts-green);
    }

    .mode-indicator.mode-interactive .mode-indicator-label {
      color: var(--vscode-charts-green);
    }

    /* Message Input Container (Issue #497) */
    .message-input-container {
      display: none;
      padding: var(--spacing-sm) var(--spacing-md);
      border-top: 1px solid var(--vscode-panel-border);
      background: var(--vscode-editorWidget-background);
    }

    /* Show input only in interactive mode */
    .output-window.mode-interactive .message-input-container {
      display: flex;
      gap: var(--spacing-sm);
      align-items: flex-end;
    }

    .message-input-wrapper {
      flex: 1;
      display: flex;
      flex-direction: column;
      gap: var(--spacing-xs);
    }

    .message-input {
      width: 100%;
      min-height: 36px;
      max-height: 120px;
      padding: var(--spacing-sm);
      background: var(--vscode-input-background);
      color: var(--vscode-input-foreground);
      border: 1px solid var(--vscode-input-border);
      border-radius: var(--border-radius);
      font-family: var(--vscode-font-family);
      font-size: var(--vscode-font-size);
      resize: none;
      line-height: 1.4;
    }

    .message-input:focus {
      outline: none;
      border-color: var(--vscode-focusBorder);
    }

    .message-input::placeholder {
      color: var(--vscode-input-placeholderForeground);
    }

    .message-input:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }

    .message-send-btn {
      display: flex;
      align-items: center;
      justify-content: center;
      padding: var(--spacing-sm) var(--spacing-md);
      min-width: 60px;
      height: 36px;
      background: var(--vscode-button-background);
      color: var(--vscode-button-foreground);
      border: none;
      border-radius: var(--border-radius);
      cursor: pointer;
      font-weight: 500;
      font-size: 0.9em;
      transition: background 0.15s ease;
    }

    .message-send-btn:hover:not(:disabled) {
      background: var(--vscode-button-hoverBackground);
    }

    .message-send-btn:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }

    /* Send feedback animation */
    .message-send-btn.sending {
      animation: sendPulse 0.3s ease;
    }

    @keyframes sendPulse {
      0% { transform: scale(1); }
      50% { transform: scale(0.95); }
      100% { transform: scale(1); }
    }

    .message-send-btn.success {
      background: var(--vscode-charts-green);
    }

    .message-send-btn.error {
      background: var(--vscode-charts-red);
    }

    /* Input hint */
    .message-input-hint {
      font-size: 0.75em;
      color: var(--vscode-descriptionForeground);
      padding: 0 var(--spacing-xs);
    }

    /* Output Content */
    .output-content {
      flex: 1;
      overflow-y: auto;
      padding: var(--spacing-sm) var(--spacing-md);
      font-family: var(--vscode-editor-font-family);
      font-size: 12px;
    }

    .output-entry {
      padding: var(--spacing-xs) 0;
      border-bottom: 1px solid var(--vscode-panel-border);
    }

    .output-entry:last-child {
      border-bottom: none;
    }

    .entry-header {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      flex-wrap: wrap;
    }

    .entry-time {
      color: var(--vscode-descriptionForeground);
      font-size: 0.9em;
      min-width: 70px;
    }

    .entry-badge {
      padding: 1px 6px;
      border-radius: 3px;
      font-size: 0.75em;
      font-weight: 500;
      min-width: 45px;
      text-align: center;
    }

    .level-info .entry-badge,
    .entry-badge.level-info {
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
    }

    .level-debug .entry-badge,
    .entry-badge.level-debug {
      background: var(--vscode-descriptionForeground);
      color: var(--vscode-editor-background);
      opacity: 0.7;
    }

    .level-warning .entry-badge,
    .entry-badge.level-warning {
      background: var(--vscode-charts-yellow);
      color: var(--vscode-editor-background);
    }

    .level-error .entry-badge,
    .entry-badge.level-error {
      background: var(--vscode-charts-red);
      color: white;
    }

    .level-tool .entry-badge,
    .entry-badge.level-tool {
      background: var(--vscode-charts-blue);
      color: white;
    }

    .level-user .entry-badge,
    .entry-badge.level-user {
      background: var(--vscode-charts-green);
      color: white;
    }

    .entry-stage {
      color: var(--vscode-descriptionForeground);
      font-size: 0.85em;
    }

    .entry-text {
      flex: 1;
      word-break: break-word;
    }

    /* Stage groups — collapsed <details> for all stages */
    .stage-group {
      margin: var(--spacing-sm) 0;
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      overflow: hidden;
    }

    .stage-group-summary {
      cursor: pointer;
      padding: var(--spacing-sm) var(--spacing-md);
      background: var(--vscode-sideBar-background);
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      font-weight: 500;
      font-size: 0.9em;
      user-select: none;
      list-style: none;
    }

    .stage-group-summary::-webkit-details-marker {
      display: none;
    }

    .stage-group-summary::before {
      content: '▶';
      font-size: 0.75em;
      color: var(--vscode-descriptionForeground);
      transition: transform 0.15s ease;
    }

    .stage-group[open] > .stage-group-summary::before {
      transform: rotate(90deg);
    }

    .stage-group-summary:hover {
      background: var(--vscode-list-hoverBackground);
    }

    .stage-group-icon {
      font-size: 1em;
    }

    .stage-complete .stage-group-icon {
      color: var(--vscode-charts-green);
    }

    .stage-error .stage-group-icon {
      color: var(--vscode-errorForeground);
    }

    .stage-skipped .stage-group-icon {
      color: var(--vscode-descriptionForeground);
    }

    .stage-group-name {
      flex: 1;
    }

    .stage-group-count {
      color: var(--vscode-descriptionForeground);
      font-weight: normal;
      font-size: 0.85em;
    }

    .stage-running .stage-group-icon {
      color: var(--vscode-progressBar-background, #0078d4);
    }

    @keyframes spin {
      to { transform: rotate(360deg); }
    }

    .stage-running .stage-group-spinner {
      display: inline-block;
      width: 12px;
      height: 12px;
      border: 2px solid var(--vscode-descriptionForeground);
      border-top-color: var(--vscode-progressBar-background, #0078d4);
      border-radius: 50%;
      animation: spin 0.8s linear infinite;
      margin-right: 4px;
    }

    /* Collapsible entries */
    .output-entry.collapsible .entry-toggle {
      cursor: pointer;
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
    }

    .output-entry.collapsible .entry-toggle:hover {
      background: var(--vscode-list-hoverBackground);
    }

    .toggle-icon {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
      min-width: 12px;
    }

    .entry-details {
      margin-top: var(--spacing-sm);
      margin-left: var(--spacing-lg);
      padding: var(--spacing-sm);
      background: var(--vscode-textCodeBlock-background);
      border-radius: var(--border-radius);
      overflow-x: auto;
    }

    .entry-details pre {
      margin: 0;
      font-family: var(--vscode-editor-font-family);
      font-size: 0.9em;
      white-space: pre-wrap;
      word-break: break-all;
    }

    /* Empty state */
    .empty-state {
      display: flex;
      align-items: center;
      justify-content: center;
      height: 100%;
      color: var(--vscode-descriptionForeground);
      text-align: center;
      padding: var(--spacing-lg);
    }

    /* Auto-scroll indicator */
    .auto-scroll-indicator {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }

    .auto-scroll-indicator.enabled {
      color: var(--vscode-charts-green);
    }

    /* Word wrap toggle (Issue #161) */
    .word-wrap-indicator {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }

    .word-wrap-indicator.enabled {
      color: var(--vscode-charts-green);
    }

    /* Timestamp toggle (Issue #160) */
    .timestamp-indicator {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }

    .timestamp-indicator.enabled {
      color: var(--vscode-charts-green);
    }

    /* Timestamp visibility control (Issue #160) */
    .output-content.timestamps-hidden .entry-time {
      display: none;
    }

    /* Word wrap state for output content (Issue #161) */
    .output-content.word-wrap-enabled .entry-content,
    .output-content.word-wrap-enabled .entry-details pre,
    .output-content.word-wrap-enabled .diff-line,
    .output-content.word-wrap-enabled .formatted-json code {
      white-space: pre-wrap;
      word-break: break-word;
    }

    .output-content.word-wrap-disabled .entry-content,
    .output-content.word-wrap-disabled .entry-details pre,
    .output-content.word-wrap-disabled .diff-line,
    .output-content.word-wrap-disabled .formatted-json code {
      white-space: pre;
      word-break: normal;
    }

    /* Entry content for markdown */
    .entry-content {
      margin-top: var(--spacing-xs);
      padding-left: var(--spacing-sm);
      line-height: 1.6;
    }

    /* Markdown Body Styles (GitHub-like) */
    .markdown-body {
      font-size: 13px;
    }

    .markdown-body p {
      margin: 0.3em 0;
    }

    .markdown-body p:first-child {
      margin-top: 0;
    }

    .markdown-body p:last-child {
      margin-bottom: 0;
    }

    .markdown-body h1, .markdown-body h2 {
      font-size: 1.3em;
      font-weight: 600;
      color: var(--vscode-foreground);
      margin: var(--spacing-md) 0 var(--spacing-sm) 0;
      padding-bottom: var(--spacing-xs);
      border-bottom: 1px solid var(--vscode-panel-border);
    }

    .markdown-body h3 {
      font-size: 1.15em;
      font-weight: 600;
      color: var(--vscode-foreground);
      margin: var(--spacing-sm) 0 var(--spacing-xs) 0;
    }

    .markdown-body h4, .markdown-body h5, .markdown-body h6 {
      font-size: 1em;
      font-weight: 600;
      color: var(--vscode-foreground);
      margin: var(--spacing-xs) 0;
    }

    .markdown-body pre {
      background: var(--vscode-textCodeBlock-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-sm);
      margin: var(--spacing-sm) 0;
      overflow-x: auto;
      font-family: var(--vscode-editor-font-family);
      font-size: 0.9em;
    }

    .markdown-body pre code {
      background: none;
      padding: 0;
      white-space: pre;
      font-size: inherit;
    }

    .markdown-body code {
      background: var(--vscode-textCodeBlock-background);
      padding: 2px 6px;
      border-radius: 3px;
      font-family: var(--vscode-editor-font-family);
      font-size: 0.9em;
    }

    .markdown-body ul, .markdown-body ol {
      margin: var(--spacing-xs) 0;
      padding-left: var(--spacing-lg);
    }

    .markdown-body li {
      margin: var(--spacing-xs) 0;
    }

    .markdown-body a {
      color: var(--vscode-textLink-foreground);
      text-decoration: none;
    }

    .markdown-body a:hover {
      text-decoration: underline;
    }

    .markdown-body hr {
      border: none;
      border-top: 1px solid var(--vscode-panel-border);
      margin: var(--spacing-md) 0;
    }

    .markdown-body strong {
      font-weight: 600;
      color: var(--vscode-foreground);
    }

    .markdown-body em {
      font-style: italic;
    }

    .markdown-body blockquote {
      border-left: 3px solid var(--vscode-textBlockQuote-border);
      margin: var(--spacing-sm) 0;
      padding: var(--spacing-xs) var(--spacing-md);
      color: var(--vscode-textBlockQuote-foreground);
      background: var(--vscode-textBlockQuote-background);
    }

    .markdown-body table {
      border-collapse: collapse;
      margin: var(--spacing-sm) 0;
      width: 100%;
    }

    .markdown-body th, .markdown-body td {
      border: 1px solid var(--vscode-panel-border);
      padding: var(--spacing-xs) var(--spacing-sm);
      text-align: left;
    }

    .markdown-body th {
      background: var(--vscode-editorWidget-background);
      font-weight: 600;
    }

    /* Diff formatting (Issue #428) */
    .diff-container {
      font-family: var(--vscode-editor-font-family);
      font-size: 12px;
      line-height: 1.4;
      background: var(--vscode-textCodeBlock-background);
      border-radius: var(--border-radius);
      padding: var(--spacing-xs);
      margin: var(--spacing-xs) 0;
      overflow-x: auto;
    }

    .diff-line {
      white-space: pre;
      padding: 0 var(--spacing-sm);
      min-height: 1.4em;
    }

    .diff-add {
      background: var(--vscode-diffEditor-insertedLineBackground);
      color: var(--vscode-gitDecoration-addedResourceForeground, var(--vscode-charts-green));
    }

    .diff-del {
      background: var(--vscode-diffEditor-removedLineBackground);
      color: var(--vscode-gitDecoration-deletedResourceForeground, var(--vscode-charts-red));
    }

    .diff-hunk {
      color: var(--vscode-charts-blue);
      font-weight: 500;
      background: var(--vscode-diffEditor-diagonalFill, rgba(128, 128, 128, 0.1));
    }

    .diff-header {
      color: var(--vscode-descriptionForeground);
      font-weight: 500;
    }

    .diff-context {
      color: var(--vscode-descriptionForeground);
    }

    /* JSON formatting (Issue #428) */
    .formatted-json {
      background: var(--vscode-textCodeBlock-background);
      border-radius: var(--border-radius);
      padding: var(--spacing-sm);
      margin: var(--spacing-xs) 0;
      overflow-x: auto;
      font-family: var(--vscode-editor-font-family);
      font-size: 12px;
    }

    .formatted-json code {
      background: none;
      padding: 0;
    }

    /* Code block with language header (Issue #428) */
    .code-block {
      background: var(--vscode-textCodeBlock-background);
      border-radius: var(--border-radius);
      margin: var(--spacing-xs) 0;
      overflow-x: auto;
    }

    .code-block-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      padding: var(--spacing-xs) var(--spacing-sm);
      background: var(--vscode-editorWidget-background);
      border-bottom: 1px solid var(--vscode-panel-border);
      font-size: 0.85em;
    }

    .code-block-filename {
      color: var(--vscode-foreground);
      font-weight: 500;
    }

    .code-block-language {
      color: var(--vscode-descriptionForeground);
      text-transform: uppercase;
      font-size: 0.8em;
    }

    .code-block code {
      display: block;
      padding: var(--spacing-sm);
      white-space: pre;
      font-family: var(--vscode-editor-font-family);
      font-size: 12px;
    }

    /* Formatted content indicator (Issue #428) */
    .formatted-content {
      border-left: 3px solid var(--vscode-charts-blue);
      padding-left: var(--spacing-sm);
    }

    .formatted-content.content-diff {
      border-left-color: var(--vscode-charts-yellow);
    }

    .formatted-content.content-json {
      border-left-color: var(--vscode-charts-green);
    }

    .formatted-content.content-code {
      border-left-color: var(--vscode-charts-purple, #a855f7);
    }

    /* Collapsible fenced code blocks (Issue #846) */
    .collapsible-code {
      margin: var(--spacing-xs) 0;
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      overflow: hidden;
    }

    .code-toggle {
      display: flex;
      align-items: center;
      gap: var(--spacing-xs);
      padding: var(--spacing-xs) var(--spacing-sm);
      cursor: pointer;
      background: var(--vscode-editorWidget-background);
      user-select: none;
    }

    .code-toggle:hover {
      background: var(--vscode-list-hoverBackground);
    }

    .code-summary {
      font-size: 0.9em;
      color: var(--vscode-descriptionForeground);
    }

    .code-details pre {
      margin: 0;
      padding: var(--spacing-sm);
      background: var(--vscode-textCodeBlock-background);
      overflow-x: auto;
    }

    /* Search UI (Issue #158) */
    .search-container {
      display: flex;
      align-items: center;
      gap: var(--spacing-xs);
      padding: 0 var(--spacing-sm);
      background: var(--vscode-input-background);
      border: 1px solid var(--vscode-input-border);
      border-radius: var(--border-radius);
      margin-right: var(--spacing-sm);
    }

    .search-container:focus-within {
      border-color: var(--vscode-focusBorder);
    }

    .search-input {
      background: transparent;
      border: none;
      color: var(--vscode-input-foreground);
      font-size: 0.85em;
      padding: var(--spacing-xs) 0;
      min-width: 120px;
      width: 150px;
      outline: none;
    }

    .search-input::placeholder {
      color: var(--vscode-input-placeholderForeground);
    }

    .search-nav-btn {
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 2px 4px;
      background: transparent;
      color: var(--vscode-foreground);
      border: none;
      border-radius: 2px;
      cursor: pointer;
      font-size: 0.85em;
      opacity: 0.7;
    }

    .search-nav-btn:hover:not(:disabled) {
      background: var(--vscode-button-secondaryHoverBackground);
      opacity: 1;
    }

    .search-nav-btn:disabled {
      opacity: 0.3;
      cursor: not-allowed;
    }

    .search-toggle-btn {
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 2px 6px;
      background: transparent;
      color: var(--vscode-descriptionForeground);
      border: 1px solid transparent;
      border-radius: 2px;
      cursor: pointer;
      font-size: 0.75em;
      font-weight: 500;
    }

    .search-toggle-btn:hover {
      background: var(--vscode-button-secondaryHoverBackground);
      color: var(--vscode-foreground);
    }

    .search-toggle-btn.enabled {
      background: var(--vscode-button-background);
      color: var(--vscode-button-foreground);
      border-color: var(--vscode-button-background);
    }

    .search-match-count {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
      white-space: nowrap;
      padding: 0 var(--spacing-xs);
      min-width: 40px;
      text-align: center;
    }

    .search-match-count.no-matches {
      color: var(--vscode-errorForeground);
    }

    .search-clear-btn {
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 2px;
      background: transparent;
      color: var(--vscode-descriptionForeground);
      border: none;
      border-radius: 2px;
      cursor: pointer;
      font-size: 0.9em;
    }

    .search-clear-btn:hover {
      color: var(--vscode-foreground);
      background: var(--vscode-button-secondaryHoverBackground);
    }

    /* Search match highlighting */
    .search-match {
      background: var(--vscode-editor-findMatchHighlightBackground, rgba(234, 92, 0, 0.33));
      border-radius: 2px;
    }

    .search-match.current {
      background: var(--vscode-editor-findMatchBackground, rgba(255, 230, 0, 0.6));
      outline: 1px solid var(--vscode-editor-findMatchBorder, var(--vscode-charts-yellow));
    }

    /* Hide entries that don't match filter */
    .output-content.search-active .output-entry.search-hidden {
      display: none;
    }

    /* Search results info */
    .search-results-info {
      display: flex;
      align-items: center;
      gap: var(--spacing-xs);
    }

    /* Collapsible search bar (Issue #850) */
    .search-toggle-icon-btn {
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 2px 4px;
      background: transparent;
      color: var(--vscode-foreground);
      border: none;
      border-radius: 2px;
      cursor: pointer;
      font-size: 1em;
      opacity: 0.8;
    }

    .search-toggle-icon-btn:hover {
      opacity: 1;
      background: var(--vscode-button-secondaryHoverBackground);
    }

    .search-container.collapsed .search-input,
    .search-container.collapsed .search-nav-btn,
    .search-container.collapsed .search-toggle-btn,
    .search-container.collapsed .search-match-count,
    .search-container.collapsed .search-clear-btn {
      display: none;
    }

    .search-container.collapsed {
      border: none;
      background: transparent;
      padding: 0;
      margin-right: 0;
    }

    .search-container:not(.collapsed) .search-toggle-icon-btn {
      display: none;
    }

    .search-container:not(.collapsed) {
      transition: width 0.15s ease;
    }

    /* Responsive breakpoints (Issue #850) */
    @media (max-width: 500px) {
      .output-title {
        max-width: 140px;
        font-size: 1em;
      }

      .action-btn {
        padding: 2px 6px;
        font-size: 0.8em;
      }

      .mode-indicator-label {
        display: none;
      }
    }

    @media (max-width: 400px) {
      .output-header {
        padding: var(--spacing-xs) var(--spacing-sm);
      }

      .header-actions {
        gap: var(--spacing-xs);
      }

      .output-title {
        max-width: 100px;
        font-size: 0.95em;
      }

      .action-btn {
        padding: 2px 4px;
        font-size: 0.75em;
      }
    }

    /* =========================================================
     * Slot tab bar — Issue #2705
     * Follows Dashboard.ts tab pattern for visual consistency.
     * ========================================================= */

    .slot-tab-bar {
      display: flex;
      align-items: center;
      gap: 2px;
      padding: 0 var(--spacing-sm);
      border-bottom: 1px solid var(--vscode-panel-border);
      background: var(--vscode-editorWidget-background);
      overflow-x: auto;
      flex-shrink: 0;
    }

    .slot-tab-btn {
      padding: 5px 12px;
      border: none;
      background: transparent;
      color: var(--vscode-foreground);
      cursor: pointer;
      border-bottom: 2px solid transparent;
      margin-bottom: -1px;
      white-space: nowrap;
      font-size: var(--vscode-font-size);
      opacity: 0.7;
      transition: opacity 0.1s;
    }

    .slot-tab-btn:hover {
      opacity: 1;
      background: var(--vscode-list-hoverBackground);
    }

    .slot-tab-btn.active {
      opacity: 1;
      border-bottom-color: var(--vscode-focusBorder);
      color: var(--vscode-focusBorder);
    }

    /* Tab badges — status dot, stage chip, elapsed time, cost (Issue #2815) */
    .tab-badge {
      display: inline-flex;
      align-items: center;
      margin-left: 4px;
      padding: 1px 4px;
      border-radius: 3px;
      font-size: 0.78em;
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
      white-space: nowrap;
    }
    .tab-badge-status { background: transparent; padding: 0 2px; }
    .tab-badge-running { color: var(--vscode-charts-blue); }
    .tab-badge-complete { color: var(--vscode-charts-green); }
    .tab-badge-error { color: var(--vscode-charts-red); }
    .tab-badge-stage { opacity: 0.85; }
    .tab-badge-elapsed { font-variant-numeric: tabular-nums; }
    .tab-badge-cost { opacity: 0.85; }
    @keyframes spin { to { transform: rotate(360deg); } }
    .tab-badge-spinner {
      display: inline-block;
      width: 8px;
      height: 8px;
      border: 1.5px solid var(--vscode-charts-blue);
      border-top-color: transparent;
      border-radius: 50%;
      animation: spin 0.8s linear infinite;
    }

    /* Archived slot styles (Issue #2818) */
    .slot-tab-btn.archived {
      font-style: italic;
    }

    .slot-tab-chip {
      display: inline-block;
      margin-left: 6px;
      padding: 1px 6px;
      border-radius: 8px;
      font-size: 0.75em;
      font-style: normal;
      line-height: 1.4;
      vertical-align: baseline;
    }

    .slot-tab-chip-archived {
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
      opacity: 0.85;
    }

    /* Each slot panel fills the remaining output area */
    .slot-panel {
      display: none;
      flex-direction: column;
      flex: 1;
      overflow-y: auto;
      overflow-x: hidden;
      height: 100%;
    }

    .slot-panel.active {
      display: flex;
    }

    /* When tab bar is present, outputContent uses flex layout to stack panels */
    .output-content.has-slot-tabs {
      overflow: hidden;
      display: flex;
      flex-direction: column;
    }

    /* ==========================================================
     * Overview dashboard — Issue #2817
     * Card grid rendered inside the "Overview" slot panel (null).
     * ========================================================= */
    .slot-panel[data-slot="null"] .overview-panel {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
      gap: var(--spacing-md);
      padding: var(--spacing-md);
      overflow-y: auto;
      width: 100%;
      box-sizing: border-box;
    }
    .overview-panel-empty {
      padding: var(--spacing-md);
      opacity: 0.75;
    }
    .overview-card {
      display: flex;
      flex-direction: column;
      gap: var(--spacing-xs);
      padding: var(--spacing-sm) var(--spacing-md);
      border: 1px solid var(--vscode-panel-border);
      border-radius: 4px;
      background: var(--vscode-editorWidget-background);
      cursor: pointer;
      transition: border-color 0.1s, background 0.1s;
    }
    .overview-card:hover {
      border-color: var(--vscode-focusBorder);
      background: var(--vscode-list-hoverBackground);
    }
    .overview-card:focus {
      outline: 1px solid var(--vscode-focusBorder);
      outline-offset: 1px;
    }
    .overview-card-header {
      display: flex;
      flex-direction: column;
      gap: 2px;
    }
    .overview-card-title {
      font-size: 1em;
      line-height: 1.3;
    }
    .overview-card-issue-title {
      opacity: 0.9;
    }
    .overview-card-repo {
      font-size: 0.8em;
      opacity: 0.7;
    }
    .overview-card-status {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      flex-wrap: wrap;
    }
    .overview-status-badge {
      display: inline-block;
      padding: 2px 8px;
      border-radius: 10px;
      font-size: 0.78em;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.02em;
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
    }
    .overview-status-running {
      background: var(--vscode-charts-blue);
      color: var(--vscode-editor-background);
    }
    .overview-status-complete {
      background: var(--vscode-charts-green);
      color: var(--vscode-editor-background);
    }
    .overview-status-error {
      background: var(--vscode-inputValidation-errorBackground, var(--vscode-charts-red));
      color: var(--vscode-editor-foreground);
    }
    .overview-status-skipped {
      background: var(--vscode-descriptionForeground);
      color: var(--vscode-editor-background);
    }
    .overview-card-stage {
      opacity: 0.85;
      font-size: 0.9em;
    }
    .overview-card-phase {
      opacity: 0.75;
      font-size: 0.85em;
      font-style: italic;
      margin-left: var(--spacing-xs);
    }
    .overview-card-phase:empty {
      display: none;
    }
    .overview-card-metrics {
      display: flex;
      justify-content: space-between;
      gap: var(--spacing-sm);
      font-variant-numeric: tabular-nums;
      font-size: 0.9em;
    }
    .overview-card-elapsed { opacity: 0.85; }
    .overview-card-cost { opacity: 0.95; font-weight: 600; }
    .overview-card-tokens {
      font-size: 0.78em;
      opacity: 0.75;
      font-variant-numeric: tabular-nums;
    }
    .overview-card-actions {
      display: flex;
      gap: var(--spacing-xs);
      margin-top: var(--spacing-xs);
      flex-wrap: wrap;
    }
    .overview-card-btn {
      padding: 3px 10px;
      border: 1px solid var(--vscode-button-border, transparent);
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
      border-radius: 3px;
      cursor: pointer;
      font-size: 0.85em;
    }
    .overview-card-btn:hover {
      background: var(--vscode-button-secondaryHoverBackground);
    }
  `;
}

/**
 * Get JavaScript for the WebView
 */
function getScript(): string {
  return `
    (function() {
      const vscode = acquireVsCodeApi();
      let autoScroll = true;
      let wordWrap = true;
      let showTimestamps = true;
      let currentActiveStage = null; // Track the active pipeline stage for tool indicators

      // DOM elements
      const outputContent = document.getElementById('outputContent');
      const stopBtn = document.getElementById('stopBtn');
      const copyBtn = document.getElementById('copyBtn');
      const clearBtn = document.getElementById('clearBtn');
      const exportBtn = document.getElementById('exportBtn');
      const autoScrollBtn = document.getElementById('autoScrollBtn');
      const wordWrapBtn = document.getElementById('wordWrapBtn');
      const timestampBtn = document.getElementById('timestampBtn');

      // =====================================================================
      // Slot tab switching — Issue #2705
      // Tab panels are CSS-toggled; active slot is synced to the extension.
      // =====================================================================

      /** Return the currently active slot panel element */
      function getActivePanel() {
        return outputContent ? outputContent.querySelector('.slot-panel.active') : null;
      }

      /** Switch to the given slot panel, save/restore per-panel scroll state */
      function switchSlotTab(slotKey) {
        const tabBar = document.getElementById('slotTabBar');
        if (!tabBar) return; // single-slot mode — no tab bar

        // Save current panel scroll position
        const currentPanel = getActivePanel();
        if (currentPanel) {
          const panelId = currentPanel.dataset.slot;
          const state = vscode.getState() || {};
          state['scroll_' + panelId] = currentPanel.scrollTop;
          vscode.setState(state);
        }

        // Deactivate all tabs and panels
        tabBar.querySelectorAll('.slot-tab-btn').forEach(function(b) {
          b.classList.remove('active');
        });
        if (outputContent) {
          outputContent.querySelectorAll('.slot-panel').forEach(function(p) {
            p.classList.remove('active');
          });
        }

        // Activate the clicked tab and its panel
        const clickedBtn = tabBar.querySelector('.slot-tab-btn[data-slot="' + slotKey + '"]');
        if (clickedBtn) clickedBtn.classList.add('active');

        const targetPanel = outputContent
          ? outputContent.querySelector('.slot-panel[data-slot="' + slotKey + '"]')
          : null;
        if (targetPanel) {
          targetPanel.classList.add('active');

          // Restore scroll position for this panel
          const state = vscode.getState() || {};
          const saved = state['scroll_' + slotKey];
          if (saved !== undefined) {
            targetPanel.scrollTop = saved;
          } else if (autoScroll) {
            targetPanel.scrollTop = targetPanel.scrollHeight;
          }
        }

        // Notify extension so it can sync activeSlotIndex state
        const slotIndex = slotKey === 'null' ? null : parseInt(slotKey, 10);
        vscode.postMessage({ type: 'tab:switch', slotIndex: slotIndex });
      }

      // Wire up tab bar click delegation (bar may be injected dynamically)
      document.addEventListener('click', function(e) {
        const btn = e.target.closest && e.target.closest('.slot-tab-btn');
        if (btn) {
          switchSlotTab(btn.dataset.slot);
          return;
        }

        // Overview card action buttons — Issue #2817
        const actionBtn = e.target.closest && e.target.closest('.overview-card-btn[data-overview-action]');
        if (actionBtn) {
          const action = actionBtn.dataset.overviewAction;
          const slotAttr = actionBtn.dataset.slot;
          const slotIdx = slotAttr != null ? parseInt(slotAttr, 10) : NaN;
          if (Number.isNaN(slotIdx)) return;
          e.stopPropagation();
          if (action === 'open-tab') {
            switchSlotTab(String(slotIdx));
          } else if (action === 'reveal-github' || action === 'open-log') {
            vscode.postMessage({ type: 'slot:action', slotIndex: slotIdx, action: action });
          }
          return;
        }

        // Overview card body click → open that slot's tab (Issue #2817)
        const card = e.target.closest && e.target.closest('.overview-card');
        if (card) {
          const slotAttr = card.dataset.slot;
          if (slotAttr != null) switchSlotTab(slotAttr);
          return;
        }
      });

      // Keyboard activation for overview cards (Issue #2817)
      document.addEventListener('keydown', function(e) {
        if (e.key !== 'Enter' && e.key !== ' ') return;
        const card = e.target.closest && e.target.closest('.overview-card');
        if (!card) return;
        e.preventDefault();
        const slotAttr = card.dataset.slot;
        if (slotAttr != null) switchSlotTab(slotAttr);
      });

      // Keep overview card elapsed timers ticking for running slots (Issue #2817)
      function updateOverviewCardElapsed() {
        const cards = document.querySelectorAll('.overview-card-elapsed[data-started-at]');
        cards.forEach(function(el) {
          if (el.dataset.completedAt) return;
          const started = parseInt(el.dataset.startedAt || '0', 10);
          if (!started) return;
          const total = Math.max(0, Math.floor((Date.now() - started) / 1000));
          const s = total % 60;
          const m = Math.floor(total / 60) % 60;
          const h = Math.floor(total / 3600);
          el.textContent = h > 0
            ? h + 'h ' + m + 'm ' + s + 's'
            : m > 0
              ? m + 'm ' + s + 's'
              : s + 's';
        });
      }
      const overviewElapsedTimer = setInterval(updateOverviewCardElapsed, 1000);
      window.addEventListener('unload', function() {
        clearInterval(overviewElapsedTimer);
      });

      // Save scroll position per panel on scroll (Issue #2705)
      if (outputContent) {
        outputContent.addEventListener('scroll', function(e) {
          const panel = e.target.closest && e.target.closest('.slot-panel');
          if (!panel) return;
          const panelId = panel.dataset.slot;
          const state = vscode.getState() || {};
          state['scroll_' + panelId] = panel.scrollTop;
          vscode.setState(state);
        }, true); // capture phase so panel scroll events are caught
      }

      // =====================================================================
      // Slot tab badge timers — Issue #2815
      // Elapsed time is computed in the WebView to avoid 1/s postMessage
      // overhead. TypeScript provides startedAt epoch ms once; JS ticks.
      // =====================================================================

      /** Map<slotIndex, intervalId> for running-slot elapsed timers */
      const slotTimers = {};

      function formatElapsed(startedAt, completedAt) {
        const ms = (completedAt != null ? completedAt : Date.now()) - startedAt;
        const s = Math.floor(ms / 1000);
        const m = Math.floor(s / 60);
        const h = Math.floor(m / 60);
        return h > 0
          ? h + ':' + String(m % 60).padStart(2, '0') + ':' + String(s % 60).padStart(2, '0')
          : m + ':' + String(s % 60).padStart(2, '0');
      }

      function updateElapsedBadge(slotIndex, startedAt, completedAt) {
        const btn = document.querySelector('.slot-tab-btn[data-slot="' + slotIndex + '"]');
        if (!btn) return;
        const badge = btn.querySelector('.tab-badge-elapsed');
        if (!badge) return;
        badge.textContent = formatElapsed(startedAt, completedAt != null ? completedAt : null);
      }

      // Seed timers for any running slots rendered in the initial HTML
      document.querySelectorAll('.slot-tab-btn[data-slot]').forEach(function(btn) {
        const slotKey = btn.dataset.slot;
        if (slotKey === 'null') return;
        const badge = btn.querySelector('.tab-badge-elapsed');
        if (!badge) return;
        const startedAt = parseInt(badge.dataset.startedAt || '0', 10);
        const completedAtRaw = badge.dataset.completedAt;
        const completedAt = completedAtRaw ? parseInt(completedAtRaw, 10) : null;
        if (!startedAt) return;
        if (completedAt != null) {
          badge.textContent = formatElapsed(startedAt, completedAt);
        } else {
          badge.textContent = formatElapsed(startedAt, null);
          const idx = parseInt(slotKey, 10);
          slotTimers[idx] = setInterval(function() {
            updateElapsedBadge(idx, startedAt, null);
          }, 1000);
        }
      });

      // Clean up timers when the WebView unloads to avoid battery drain
      window.addEventListener('unload', function() {
        Object.keys(slotTimers).forEach(function(k) {
          clearInterval(slotTimers[k]);
        });
      });

      // Event delegation for all interactive elements in the output area.
      // Uses delegation instead of inline onclick/onkeydown to comply with
      // CSP nonce policy — inline event handlers are blocked when script-src
      // requires a nonce.
      outputContent?.addEventListener('click', (e) => {
        // Collapsible entry toggles
        const toggle = e.target.closest('.entry-toggle');
        if (toggle) {
          const entryEl = toggle.closest('.output-entry.collapsible');
          if (entryEl) {
            console.log('[OutputWindow] Toggle clicked for entry:', entryEl.dataset.entryId);
            toggleEntry(entryEl.dataset.entryId);
          }
          return;
        }

        // Code block toggles (fenced code collapse)
        const codeToggle = e.target.closest('.code-toggle');
        if (codeToggle) {
          const container = codeToggle.closest('.collapsible-code');
          if (container) {
            console.log('[OutputWindow] Code toggle clicked:', container.dataset.codeId);
            toggleCodeBlock(container.dataset.codeId);
          }
          return;
        }

        // Question option buttons
        const optionBtn = e.target.closest('.question-option-btn');
        if (optionBtn) {
          handleOptionClick(optionBtn);
          return;
        }

        // Question submit button
        const submitBtn = e.target.closest('.question-submit-btn');
        if (submitBtn) {
          const questionId = submitBtn.dataset.questionId;
          if (questionId) submitQuestionResponse(questionId);
          return;
        }

        // Question cancel/skip button
        const cancelBtn = e.target.closest('.question-cancel-btn');
        if (cancelBtn) {
          const questionId = cancelBtn.dataset.questionId;
          if (questionId) cancelQuestion(questionId);
          return;
        }
      });

      // Delegated keydown/input for question custom input fields
      outputContent?.addEventListener('keydown', (e) => {
        const input = e.target.closest('.question-custom-input input');
        if (input) handleCustomInputKeydown(e, input);
      });
      outputContent?.addEventListener('input', (e) => {
        const input = e.target.closest('.question-custom-input input');
        if (input) handleCustomInput(input);
      });

      // Handle copy button (Issue #156)
      copyBtn?.addEventListener('click', () => {
        vscode.postMessage({ type: 'copy-to-clipboard' });
      });

      // Handle clear button
      clearBtn?.addEventListener('click', () => {
        vscode.postMessage({ type: 'clear-logs' });
      });

      // Handle export button
      exportBtn?.addEventListener('click', () => {
        vscode.postMessage({ type: 'export', format: 'json' });
      });

      // Handle auto-scroll toggle
      autoScrollBtn?.addEventListener('click', () => {
        autoScroll = !autoScroll;
        userScrolledUp = false; // Reset scroll tracking on manual toggle
        autoScrollBtn.classList.toggle('enabled', autoScroll);
        autoScrollBtn.textContent = autoScroll ? 'Auto-scroll: ON' : 'Auto-scroll: OFF';
        vscode.postMessage({ type: 'toggle-auto-scroll', enabled: autoScroll });
      });

      // Auto-scroll detection: disable when user scrolls up, re-enable when user scrolls to bottom (Issue #159)
      const scrollThreshold = 50; // pixels from bottom to consider "at bottom"
      let userScrolledUp = false;

      outputContent?.addEventListener('scroll', () => {
        const isAtBottom =
          outputContent.scrollHeight - outputContent.scrollTop - outputContent.clientHeight
          < scrollThreshold;

        if (!isAtBottom && autoScroll) {
          // User scrolled up while auto-scroll was enabled
          autoScroll = false;
          userScrolledUp = true;
          if (autoScrollBtn) {
            autoScrollBtn.classList.remove('enabled');
            autoScrollBtn.textContent = 'Auto-scroll: OFF';
          }
          vscode.postMessage({ type: 'toggle-auto-scroll', enabled: false });
        } else if (isAtBottom && !autoScroll && userScrolledUp) {
          // User scrolled back to bottom - re-enable auto-scroll
          autoScroll = true;
          userScrolledUp = false;
          if (autoScrollBtn) {
            autoScrollBtn.classList.add('enabled');
            autoScrollBtn.textContent = 'Auto-scroll: ON';
          }
          vscode.postMessage({ type: 'toggle-auto-scroll', enabled: true });
        }
      });

      // Handle word wrap toggle (Issue #161)
      wordWrapBtn?.addEventListener('click', () => {
        wordWrap = !wordWrap;
        wordWrapBtn.classList.toggle('enabled', wordWrap);
        wordWrapBtn.textContent = wordWrap ? 'Word wrap: ON' : 'Word wrap: OFF';
        updateWordWrapClass();
        vscode.postMessage({ type: 'toggle-word-wrap', enabled: wordWrap });
      });

      // Update word wrap CSS class on output content
      function updateWordWrapClass() {
        if (outputContent) {
          outputContent.classList.toggle('word-wrap-enabled', wordWrap);
          outputContent.classList.toggle('word-wrap-disabled', !wordWrap);
        }
      }

      // Initialize word wrap class
      updateWordWrapClass();

      // Handle timestamp toggle (Issue #160)
      timestampBtn?.addEventListener('click', () => {
        showTimestamps = !showTimestamps;
        timestampBtn.classList.toggle('enabled', showTimestamps);
        timestampBtn.textContent = showTimestamps ? 'Timestamps: ON' : 'Timestamps: OFF';
        updateTimestampClass();
        vscode.postMessage({ type: 'toggle-timestamps', enabled: showTimestamps });
      });

      // Update timestamp CSS class on output content
      function updateTimestampClass() {
        if (outputContent) {
          outputContent.classList.toggle('timestamps-hidden', !showTimestamps);
        }
      }

      // Initialize timestamp class
      updateTimestampClass();

      // Handle stop button
      stopBtn?.addEventListener('click', () => {
        vscode.postMessage({ type: 'interrupt' });
      });

      // ===== Message Input Functionality (Issue #497) =====
      const messageInput = document.getElementById('messageInput');
      const messageSendBtn = document.getElementById('messageSendBtn');
      const messageInputContainer = document.getElementById('messageInputContainer');

      // Message history for up/down arrow navigation
      const messageHistory = [];
      const MAX_HISTORY_SIZE = 50;
      let historyIndex = -1;
      let currentDraft = '';

      // Per-entry render caps — prevent a single oversize log line from
      // ballooning into hundreds of KB of DOM inside the webview. Entries
      // above these thresholds render a lightweight placeholder pointing at
      // the on-disk log file instead of being passed through renderContent().
      // See PR "fix: prune tool indicator/summary DOM nodes and cap oversize
      // entry HTML" and earlier caps in #2850.
      // Raised from 16 KB / 64 KB to 256 KB / 1 MB (#2863 was an empty
      // merge — this commit is the actual fix). Routine pipeline output
      // (streamed diffs, Read tool results, Edit tool args, reasoning
      // traces) easily exceeds the prior 16 KB cap, which turned the
      // panel into a wall of "Content too large" placeholders. The new
      // thresholds still catch genuinely pathological multi-MB single
      // entries without suppressing merely-large ones.
      const MAX_WEBVIEW_ENTRY_BYTES = 256 * 1024;
      const MAX_WEBVIEW_DETAILS_BYTES = 1024 * 1024;

      // Enable/disable input based on process state
      let inputEnabled = false;

      function setInputEnabled(enabled) {
        inputEnabled = enabled;
        if (messageInput) {
          messageInput.disabled = !enabled;
          if (enabled) {
            messageInput.placeholder = 'Send a message to the agent...';
          } else {
            messageInput.placeholder = 'No interactive process running';
          }
        }
        if (messageSendBtn) {
          messageSendBtn.disabled = !enabled || !messageInput?.value.trim();
        }
      }

      // Auto-resize textarea
      function autoResizeInput() {
        if (!messageInput) return;
        messageInput.style.height = 'auto';
        const newHeight = Math.min(messageInput.scrollHeight, 120);
        messageInput.style.height = newHeight + 'px';
      }

      // Send message
      function sendMessage() {
        if (!messageInput || !inputEnabled) return;
        const text = messageInput.value.trim();
        if (!text) return;

        // Add to history
        if (messageHistory.length === 0 || messageHistory[messageHistory.length - 1] !== text) {
          messageHistory.push(text);
          if (messageHistory.length > MAX_HISTORY_SIZE) {
            messageHistory.shift();
          }
        }
        historyIndex = -1;
        currentDraft = '';

        // Visual feedback
        if (messageSendBtn) {
          messageSendBtn.classList.add('sending');
          setTimeout(() => messageSendBtn.classList.remove('sending'), 300);
        }

        // Send to extension
        vscode.postMessage({ type: 'send-message', text: text });

        // Clear input
        messageInput.value = '';
        autoResizeInput();
        updateSendButtonState();
      }

      // Update send button enabled state
      function updateSendButtonState() {
        if (messageSendBtn && messageInput) {
          messageSendBtn.disabled = !inputEnabled || !messageInput.value.trim();
        }
      }

      // Handle keyboard events in message input
      messageInput?.addEventListener('keydown', (e) => {
        if (e.key === 'Enter' && !e.shiftKey) {
          e.preventDefault();
          sendMessage();
        } else if (e.key === 'ArrowUp' && messageInput.selectionStart === 0) {
          e.preventDefault();
          navigateHistory(-1);
        } else if (e.key === 'ArrowDown' && messageInput.selectionStart === messageInput.value.length) {
          e.preventDefault();
          navigateHistory(1);
        }
      });

      // Handle input changes
      messageInput?.addEventListener('input', () => {
        autoResizeInput();
        updateSendButtonState();
        // Reset history navigation when user types
        if (historyIndex !== -1) {
          currentDraft = messageInput.value;
        }
      });

      // Handle send button click
      messageSendBtn?.addEventListener('click', sendMessage);

      // Navigate through message history
      function navigateHistory(direction) {
        if (!messageInput || messageHistory.length === 0) return;

        // Save current draft before navigating
        if (historyIndex === -1) {
          currentDraft = messageInput.value;
        }

        const newIndex = historyIndex + direction;

        if (newIndex < -1) {
          // Already at most recent, do nothing
          return;
        } else if (newIndex === -1) {
          // Return to draft
          historyIndex = -1;
          messageInput.value = currentDraft;
        } else if (newIndex >= messageHistory.length) {
          // Past oldest, do nothing
          return;
        } else {
          // Navigate in history (reverse order - up goes to older)
          historyIndex = newIndex;
          const historyIdx = messageHistory.length - 1 - historyIndex;
          messageInput.value = messageHistory[historyIdx];
        }

        // Move cursor to end
        messageInput.selectionStart = messageInput.value.length;
        messageInput.selectionEnd = messageInput.value.length;
        autoResizeInput();
        updateSendButtonState();
      }

      // Handle message sent feedback from extension
      function handleMessageSentFeedback(success, error) {
        if (messageSendBtn) {
          if (success) {
            messageSendBtn.classList.add('success');
            setTimeout(() => messageSendBtn.classList.remove('success'), 500);
          } else {
            messageSendBtn.classList.add('error');
            setTimeout(() => messageSendBtn.classList.remove('error'), 500);
            if (error) {
              console.warn('Message send failed:', error);
            }
          }
        }
      }

      // Enable input when in interactive mode with a running process
      function updateInputForMode(mode) {
        if (mode === 'interactive') {
          setInputEnabled(true);
        } else {
          setInputEnabled(false);
        }
      }

      // ===== Search Functionality (Issue #158) =====
      const searchInput = document.getElementById('searchInput');
      const searchPrevBtn = document.getElementById('searchPrevBtn');
      const searchNextBtn = document.getElementById('searchNextBtn');
      const searchCaseSensitiveBtn = document.getElementById('searchCaseSensitiveBtn');
      const searchRegexBtn = document.getElementById('searchRegexBtn');
      const searchClearBtn = document.getElementById('searchClearBtn');
      const searchMatchCount = document.getElementById('searchMatchCount');
      const searchContainer = document.getElementById('searchContainer');
      const searchToggleBtn = document.getElementById('searchToggleBtn');

      // Search expand/collapse (Issue #850)
      let searchCollapseTimer = null;

      function expandSearch() {
        if (searchContainer) {
          searchContainer.classList.remove('collapsed');
          searchInput?.focus();
          searchInput?.select();
        }
      }

      function collapseSearch() {
        if (searchContainer && (!searchInput || !searchInput.value)) {
          searchContainer.classList.add('collapsed');
        }
      }

      searchToggleBtn?.addEventListener('click', expandSearch);

      // Auto-collapse when search input loses focus and is empty
      searchInput?.addEventListener('blur', () => {
        clearTimeout(searchCollapseTimer);
        searchCollapseTimer = setTimeout(() => {
          if (!searchInput.value) {
            collapseSearch();
          }
        }, 200);
      });

      // Cancel collapse if search regains focus quickly
      searchInput?.addEventListener('focus', () => {
        clearTimeout(searchCollapseTimer);
      });

      let searchText = searchInput?.value || '';
      let searchCaseSensitive = searchCaseSensitiveBtn?.classList.contains('enabled') || false;
      let searchUseRegex = searchRegexBtn?.classList.contains('enabled') || false;
      let currentMatchIndex = -1;
      let searchMatches = [];

      // Debounce search input
      let searchDebounceTimer = null;
      const SEARCH_DEBOUNCE_MS = 150;

      searchInput?.addEventListener('input', (e) => {
        clearTimeout(searchDebounceTimer);
        searchDebounceTimer = setTimeout(() => {
          searchText = e.target.value;
          currentMatchIndex = -1;
          performSearch();
          vscode.postMessage({ type: 'search-text-change', text: searchText });
        }, SEARCH_DEBOUNCE_MS);
      });

      // Handle Enter and Shift+Enter for navigation
      searchInput?.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') {
          e.preventDefault();
          if (e.shiftKey) {
            navigateToPrevMatch();
          } else {
            navigateToNextMatch();
          }
        } else if (e.key === 'Escape') {
          clearSearch();
          collapseSearch();
          searchInput?.blur();
        }
      });

      // Keyboard shortcut: Cmd/Ctrl+F to focus search (expand if collapsed)
      document.addEventListener('keydown', (e) => {
        if ((e.metaKey || e.ctrlKey) && e.key === 'f') {
          e.preventDefault();
          expandSearch();
        }
      });

      searchPrevBtn?.addEventListener('click', navigateToPrevMatch);
      searchNextBtn?.addEventListener('click', navigateToNextMatch);

      searchCaseSensitiveBtn?.addEventListener('click', () => {
        searchCaseSensitive = !searchCaseSensitive;
        searchCaseSensitiveBtn.classList.toggle('enabled', searchCaseSensitive);
        currentMatchIndex = -1;
        performSearch();
        vscode.postMessage({ type: 'toggle-search-case-sensitive', enabled: searchCaseSensitive });
      });

      searchRegexBtn?.addEventListener('click', () => {
        searchUseRegex = !searchUseRegex;
        searchRegexBtn.classList.toggle('enabled', searchUseRegex);
        currentMatchIndex = -1;
        performSearch();
        vscode.postMessage({ type: 'toggle-search-use-regex', enabled: searchUseRegex });
      });

      searchClearBtn?.addEventListener('click', clearSearch);

      function clearSearch() {
        if (searchInput) searchInput.value = '';
        searchText = '';
        currentMatchIndex = -1;
        searchMatches = [];
        clearHighlights();
        updateSearchUI();
        vscode.postMessage({ type: 'search-text-change', text: '' });
        if (outputContent) {
          outputContent.classList.remove('search-active');
        }
      }

      function performSearch() {
        clearHighlights();
        searchMatches = [];

        if (!searchText || !outputContent) {
          updateSearchUI();
          outputContent?.classList.remove('search-active');
          return;
        }

        outputContent.classList.add('search-active');

        // Build regex for search
        let regex;
        try {
          if (searchUseRegex) {
            regex = new RegExp(searchText, searchCaseSensitive ? 'g' : 'gi');
          } else {
            const escaped = searchText.replace(/[.*+?^$\x7b\x7d()|\\[\\]\\\\]/g, '\\\\$&');
            regex = new RegExp(escaped, searchCaseSensitive ? 'g' : 'gi');
          }
        } catch (e) {
          // Invalid regex - show error in match count
          if (searchMatchCount) {
            searchMatchCount.textContent = 'Invalid regex';
            searchMatchCount.classList.add('no-matches');
          }
          return;
        }

        // Search all output entries
        const entries = outputContent.querySelectorAll('.output-entry');
        entries.forEach((entry) => {
          const contentEl = entry.querySelector('.entry-content');
          if (!contentEl) return;

          // Get raw text content for searching
          const textContent = contentEl.textContent || '';

          // Check if entry matches
          const matches = [];
          let match;
          while ((match = regex.exec(textContent)) !== null) {
            matches.push({
              entry: entry,
              contentEl: contentEl,
              index: match.index,
              length: match[0].length,
              text: match[0]
            });
          }

          if (matches.length > 0) {
            entry.classList.remove('search-hidden');
            searchMatches.push(...matches);
            // Highlight matches in this entry
            highlightMatches(contentEl, regex);
          } else {
            // Optionally hide non-matching entries (commented out for now - just highlight mode)
            // entry.classList.add('search-hidden');
            entry.classList.remove('search-hidden');
          }
        });

        updateSearchUI();

        // Auto-navigate to first match
        if (searchMatches.length > 0 && currentMatchIndex === -1) {
          currentMatchIndex = 0;
          scrollToCurrentMatch();
        }
      }

      function highlightMatches(contentEl, regex) {
        // Use TreeWalker to find text nodes
        const walker = document.createTreeWalker(contentEl, NodeFilter.SHOW_TEXT, null, false);
        const textNodes = [];
        let node;
        while ((node = walker.nextNode())) {
          textNodes.push(node);
        }

        // Process text nodes in reverse order to not mess up indices
        for (let i = textNodes.length - 1; i >= 0; i--) {
          const textNode = textNodes[i];
          const text = textNode.nodeValue;
          if (!text) continue;

          // Reset regex lastIndex
          regex.lastIndex = 0;

          const fragments = [];
          let lastIndex = 0;
          let match;

          while ((match = regex.exec(text)) !== null) {
            // Add text before match
            if (match.index > lastIndex) {
              fragments.push(document.createTextNode(text.substring(lastIndex, match.index)));
            }
            // Add highlighted match
            const mark = document.createElement('mark');
            mark.className = 'search-match';
            mark.textContent = match[0];
            fragments.push(mark);
            lastIndex = match.index + match[0].length;
          }

          // Add remaining text
          if (lastIndex < text.length) {
            fragments.push(document.createTextNode(text.substring(lastIndex)));
          }

          // Replace text node with fragments
          if (fragments.length > 0) {
            const parent = textNode.parentNode;
            fragments.forEach(frag => parent.insertBefore(frag, textNode));
            parent.removeChild(textNode);
          }
        }
      }

      function clearHighlights() {
        if (!outputContent) return;

        // Remove all highlight marks
        outputContent.querySelectorAll('.search-match').forEach(mark => {
          const text = mark.textContent;
          const textNode = document.createTextNode(text);
          mark.parentNode.replaceChild(textNode, mark);
        });

        // Normalize text nodes (merge adjacent text nodes)
        outputContent.querySelectorAll('.entry-content').forEach(el => {
          el.normalize();
        });

        // Remove hidden class from all entries
        outputContent.querySelectorAll('.output-entry.search-hidden').forEach(el => {
          el.classList.remove('search-hidden');
        });
      }

      function updateSearchUI() {
        const hasMatches = searchMatches.length > 0;
        const hasSearch = searchText.length > 0;

        if (searchMatchCount) {
          if (!hasSearch) {
            searchMatchCount.textContent = '';
            searchMatchCount.classList.remove('no-matches');
          } else if (hasMatches) {
            searchMatchCount.textContent = (currentMatchIndex + 1) + '/' + searchMatches.length;
            searchMatchCount.classList.remove('no-matches');
          } else {
            searchMatchCount.textContent = '0 results';
            searchMatchCount.classList.add('no-matches');
          }
        }

        if (searchPrevBtn) searchPrevBtn.disabled = !hasMatches;
        if (searchNextBtn) searchNextBtn.disabled = !hasMatches;

        // Update current match highlight
        updateCurrentMatchHighlight();
      }

      function updateCurrentMatchHighlight() {
        // Remove current class from all marks
        outputContent?.querySelectorAll('.search-match.current').forEach(mark => {
          mark.classList.remove('current');
        });

        // Add current class to current match
        if (currentMatchIndex >= 0 && currentMatchIndex < searchMatches.length) {
          const allMarks = outputContent?.querySelectorAll('.search-match');
          if (allMarks && allMarks[currentMatchIndex]) {
            allMarks[currentMatchIndex].classList.add('current');
          }
        }
      }

      function navigateToNextMatch() {
        if (searchMatches.length === 0) return;
        currentMatchIndex = (currentMatchIndex + 1) % searchMatches.length;
        scrollToCurrentMatch();
        updateSearchUI();
      }

      function navigateToPrevMatch() {
        if (searchMatches.length === 0) return;
        currentMatchIndex = (currentMatchIndex - 1 + searchMatches.length) % searchMatches.length;
        scrollToCurrentMatch();
        updateSearchUI();
      }

      function scrollToCurrentMatch() {
        const allMarks = outputContent?.querySelectorAll('.search-match');
        if (allMarks && allMarks[currentMatchIndex]) {
          allMarks[currentMatchIndex].scrollIntoView({ behavior: 'smooth', block: 'center' });
          // Temporarily disable auto-scroll when navigating matches
          if (autoScroll) {
            autoScroll = false;
            if (autoScrollBtn) {
              autoScrollBtn.classList.remove('enabled');
              autoScrollBtn.textContent = 'Auto-scroll: OFF';
            }
            vscode.postMessage({ type: 'toggle-auto-scroll', enabled: false });
          }
        }
      }

      // Re-apply search when new content is appended
      function reapplySearchOnNewContent() {
        if (searchText && searchText.length > 0) {
          // Small delay to let DOM update
          setTimeout(performSearch, 50);
        }
      }

      // Initial search if search text was persisted
      if (searchText && searchText.length > 0) {
        performSearch();
      }

      // Handle messages from extension
      window.addEventListener('message', event => {
        const message = event.data;

        switch (message.type) {
          case 'append':
            appendEntry(message.entry);
            break;
          case 'update-tokens':
            // Token footer removed — token/cost data shown in Pipeline view
            break;
          case 'clear':
            if (outputContent) {
              // Clear all slot panels (Issue #2705)
              const allPanels = outputContent.querySelectorAll('.slot-panel');
              if (allPanels.length > 0) {
                allPanels.forEach(function(p) {
                  p.innerHTML = '<div class="empty-state"><p>Output cleared.</p></div>';
                });
              } else {
                outputContent.innerHTML = '<div class="empty-state"><p>Output cleared.</p></div>';
              }
            }
            break;
          case 'clear-stage':
            if (outputContent && message.stage) {
              clearStageEntries(message.stage);
            }
            break;
          case 'remove-oldest': {
            if (!outputContent) break;
            const count = message.count;
            if (!count || count <= 0) break;
            const slotIndex = message.slotIndex;
            const panelSelector = (slotIndex === undefined || slotIndex === null)
              ? '.slot-panel[data-slot="null"]'
              : '.slot-panel[data-slot="' + slotIndex + '"]';
            const panel = outputContent.querySelector(panelSelector);
            if (!panel) break;
            // Collect the N oldest .output-entry nodes. DOM order matches append
            // order (we always appendChild), so first N in document order are
            // the oldest. Track their containing stage groups so we can refresh
            // the entry-count chip after removal.
            const entries = panel.querySelectorAll('.output-entry');
            const toRemove = Math.min(count, entries.length);
            const groupsToRefresh = new Set();
            for (let i = 0; i < toRemove; i++) {
              const el = entries[i];
              const group = el.closest && el.closest('.stage-group');
              if (group) groupsToRefresh.add(group);
              el.remove();
            }
            groupsToRefresh.forEach(function(g) { updateStageGroupCount(g); });
            break;
          }
          case 'remove-stall-warnings':
            if (outputContent && message.stage) {
              removeStallWarnings(message.stage);
            }
            break;
          case 'collapse-stage':
            if (outputContent && message.stage) {
              collapseStageEntries(message.stage, message.status);
            }
            break;
          case 'set-auto-scroll':
            autoScroll = message.enabled;
            if (autoScrollBtn) {
              autoScrollBtn.classList.toggle('enabled', autoScroll);
              autoScrollBtn.textContent = autoScroll ? 'Auto-scroll: ON' : 'Auto-scroll: OFF';
            }
            break;
          case 'set-word-wrap':
            wordWrap = message.enabled;
            if (wordWrapBtn) {
              wordWrapBtn.classList.toggle('enabled', wordWrap);
              wordWrapBtn.textContent = wordWrap ? 'Word wrap: ON' : 'Word wrap: OFF';
            }
            updateWordWrapClass();
            break;
          case 'set-timestamps':
            showTimestamps = message.enabled;
            if (timestampBtn) {
              timestampBtn.classList.toggle('enabled', showTimestamps);
              timestampBtn.textContent = showTimestamps ? 'Timestamps: ON' : 'Timestamps: OFF';
            }
            updateTimestampClass();
            break;
          case 'tool-indicator':
            appendToolIndicator(message.indicator);
            break;
          case 'tool-indicator-complete':
            markToolIndicatorComplete(message.id);
            break;
          case 'tool-summary':
            appendToolSummary(message.summary);
            break;
          case 'question-prompt':
            appendQuestionPrompt(message.question);
            break;
          case 'question-answered':
            markQuestionAnswered(message.questionId, message.answer);
            break;
          case 'pipeline-state':
            updatePipelineState(message.state);
            break;
          case 'set-search-state':
            updateSearchState(message.state);
            break;
          case 'set-mode':
            updateExecutionMode(message.mode);
            break;
          case 'message-sent-feedback':
            handleMessageSentFeedback(message.success, message.error);
            break;
          case 'overview-card-update': {
            // Live patch the per-issue Overview card in place so cost / tokens /
            // stage / status / elapsed-anchor stay current mid-pipeline. Companion
            // to slot-badge-update — fires on every event that drives the badge.
            const u = message;
            const card = document.querySelector('.overview-card[data-slot="' + u.slotIndex + '"]');
            if (!card) break;
            // Status badge
            const statusBadge = card.querySelector('.overview-status-badge');
            if (statusBadge) {
              statusBadge.className = 'overview-status-badge overview-status-' + u.status;
              statusBadge.textContent = u.statusLabel;
            }
            // Stage label
            const stageEl = card.querySelector('.overview-card-stage');
            if (stageEl) stageEl.textContent = u.stageLabel || '—';
            // Cost
            const costEl = card.querySelector('.overview-card-cost');
            if (costEl) costEl.textContent = '$' + (u.costUsd || 0).toFixed(4);
            // Tokens (input · output · cache)
            const tokensEl = card.querySelector('.overview-card-tokens');
            if (tokensEl) {
              tokensEl.textContent =
                (u.inputTokens || 0).toLocaleString() + ' in · ' +
                (u.outputTokens || 0).toLocaleString() + ' out · ' +
                (u.cacheTokens || 0).toLocaleString() + ' cache';
            }
            // Phase label patch (Issue #3010). The span is always present in
            // initial HTML; CSS hides it when empty, so we only set/clear text.
            const phaseEl = card.querySelector('.overview-card-phase');
            if (phaseEl) {
              if (u.currentPhase) {
                phaseEl.textContent =
                  u.currentPhase.name + ' · ' + u.currentPhase.index + '/' + u.currentPhase.total;
              } else {
                phaseEl.textContent = '';
              }
            }
            // Elapsed timer anchors — the existing 1Hz overviewElapsedTimer
            // keeps ticking from these data attrs. Establish-once semantics
            // (Issue #3010): set on first arrival, never overwrite or delete
            // on subsequent patches. The slot's startedAt is immutable once
            // registered, so re-stamping it on every token-delta patch was
            // resetting the elapsed math to ~0 and freezing the visible tick.
            const elapsedEl = card.querySelector('.overview-card-elapsed');
            if (elapsedEl) {
              if (u.startedAt != null && !elapsedEl.dataset.startedAt) {
                elapsedEl.dataset.startedAt = String(u.startedAt);
              }
              if (u.completedAt != null) {
                elapsedEl.dataset.completedAt = String(u.completedAt);
                // Stamp the final value so the dash doesn't flash before the
                // next tick (the 1Hz updater skips completed cards).
                const startMs = parseInt(elapsedEl.dataset.startedAt || String(u.completedAt), 10);
                const total = Math.max(
                  0,
                  Math.floor((u.completedAt - startMs) / 1000)
                );
                const s = total % 60, m = Math.floor(total / 60) % 60, h = Math.floor(total / 3600);
                elapsedEl.textContent = h > 0
                  ? h + 'h ' + m + 'm ' + s + 's'
                  : m > 0
                    ? m + 'm ' + s + 's'
                    : s + 's';
              }
              // Note: never delete data-completed-at on a patch lacking it —
              // a running slot's update simply doesn't advance the completion
              // stamp.
            }
            break;
          }
          case 'slot-badge-update': {
            // Update cost badge and status icon without a full panel reload (Issue #2815)
            const { slotIndex, status, startedAt, completedAt, costUsd } = message;
            const btn = document.querySelector('.slot-tab-btn[data-slot="' + slotIndex + '"]');
            if (btn) {
              const costBadge = btn.querySelector('.tab-badge-cost');
              if (costBadge && costUsd > 0) {
                costBadge.textContent = '$' + costUsd.toFixed(4);
              }
              const statusBadge = btn.querySelector('.tab-badge-status');
              if (statusBadge) {
                statusBadge.className =
                  'tab-badge tab-badge-status tab-badge-' + status;
                const spinner = statusBadge.querySelector('.tab-badge-spinner');
                if (status === 'running') {
                  if (!spinner) {
                    statusBadge.innerHTML = '<span class="tab-badge-spinner"></span>';
                  }
                } else if (status === 'complete') {
                  statusBadge.textContent = '✓';
                } else if (status === 'error') {
                  statusBadge.textContent = '✗';
                } else {
                  statusBadge.textContent = '';
                }
              }
            }
            // Manage elapsed timer for this slot
            if (status === 'running' && startedAt != null) {
              if (slotTimers[slotIndex]) clearInterval(slotTimers[slotIndex]);
              slotTimers[slotIndex] = setInterval(function() {
                updateElapsedBadge(slotIndex, startedAt, null);
              }, 1000);
            } else if ((status === 'complete' || status === 'error') && slotTimers[slotIndex]) {
              clearInterval(slotTimers[slotIndex]);
              delete slotTimers[slotIndex];
              if (startedAt != null) {
                updateElapsedBadge(slotIndex, startedAt, completedAt != null ? completedAt : null);
              }
            }
            break;
          }
        }
      });

      // Update search state from extension
      function updateSearchState(state) {
        if (!state) return;

        searchText = state.searchText || '';
        searchCaseSensitive = state.caseSensitive || false;
        searchUseRegex = state.useRegex || false;

        if (searchInput) searchInput.value = searchText;
        if (searchCaseSensitiveBtn) {
          searchCaseSensitiveBtn.classList.toggle('enabled', searchCaseSensitive);
        }
        if (searchRegexBtn) {
          searchRegexBtn.classList.toggle('enabled', searchUseRegex);
        }

        currentMatchIndex = -1;
        performSearch();
      }

      // ===== Execution Mode Functions (Issue #496) =====
      let currentMode = 'headless';

      /**
       * Update execution mode and UI rendering
       * @param modeData - The execution mode data from extension
       */
      function updateExecutionMode(modeData) {
        if (!modeData || !modeData.mode) return;

        const mode = modeData.mode;
        currentMode = mode;

        // Update output-window class for CSS-based mode handling
        const outputWindow = document.querySelector('.output-window');
        if (outputWindow) {
          outputWindow.classList.remove('mode-headless', 'mode-interactive');
          outputWindow.classList.add('mode-' + mode);
        }

        // Update mode indicator
        const modeIndicator = document.getElementById('modeIndicator');
        if (modeIndicator) {
          modeIndicator.classList.remove('mode-headless', 'mode-interactive');
          modeIndicator.classList.add('mode-' + mode);

          const iconEl = modeIndicator.querySelector('.mode-indicator-icon');
          const labelEl = modeIndicator.querySelector('.mode-indicator-label');

          if (mode === 'interactive') {
            if (iconEl) iconEl.textContent = '\\uD83D\\uDCAC'; // 💬 speech bubble
            if (labelEl) labelEl.textContent = 'Interactive';
            modeIndicator.title = 'Interactive mode: Raw text output, user input enabled';
          } else {
            if (iconEl) iconEl.textContent = '\\u2699'; // ⚙️ gear
            if (labelEl) labelEl.textContent = 'Headless';
            modeIndicator.title = 'Headless mode: Stream-json output, token tracking enabled';
          }
        }

        // Update message input state based on mode (Issue #497)
        updateInputForMode(mode);
      }

      function clearStageEntries(stage) {
        if (!outputContent) return;
        // Remove output entries tagged with this stage
        const stageName = formatStageName(stage);
        outputContent.querySelectorAll('.output-entry').forEach(el => {
          const stageEl = el.querySelector('.entry-stage');
          if (stageEl && stageEl.textContent === '[' + stageName + ']') {
            el.remove();
          }
        });
        // Also remove tool indicators (they don't have stage tags but are
        // interleaved with stage entries - they'll be regenerated)
        // Leave them for now as they auto-complete and new ones will append
      }

      // Remove stall warning entries for a stage (Issue #797)
      function removeStallWarnings(stage) {
        if (!outputContent) return;
        const stageName = formatStageName(stage);
        outputContent.querySelectorAll('.output-entry').forEach(el => {
          const stageEl = el.querySelector('.entry-stage');
          const matchesStage = stageEl && stageEl.textContent === '[' + stageName + ']';
          if (matchesStage) {
            const contentEl = el.querySelector('.entry-content');
            if (contentEl && contentEl.textContent && contentEl.textContent.includes('Stage still running after')) {
              el.remove();
            }
          }
        });
      }

      // Get or create a collapsed <details> group for a stage.
      // During live execution, entries are placed inside the group immediately
      // so the output stays collapsed by default.
      function getOrCreateStageGroup(stage) {
        if (!outputContent) return null;
        const stageName = formatStageName(stage);
        const groupId = 'stage-group-' + stage;

        // Return existing group if present
        let details = document.getElementById(groupId);
        if (details) return details;

        // Groups always live inside the "All" panel (aggregated view)
        const allPanel = outputContent.querySelector('.slot-panel[data-slot="null"]') || outputContent;

        // Remove empty state if present
        const emptyState = allPanel.querySelector('.empty-state');
        if (emptyState) emptyState.remove();

        // Create new collapsed <details> group with running state
        details = document.createElement('details');
        details.className = 'stage-group stage-running';
        details.id = groupId;

        const summary = document.createElement('summary');
        summary.className = 'stage-group-summary';
        summary.innerHTML = '<span class="stage-group-spinner"></span>' +
          '<span class="stage-group-icon">\u25B6</span> ' +
          '<span class="stage-group-name">' + stageName + '</span>' +
          '<span class="stage-group-count">0 entries</span>';
        details.appendChild(summary);

        allPanel.appendChild(details);
        return details;
      }

      // Update entry count in a stage group's summary
      function updateStageGroupCount(details) {
        if (!details) return;
        const countEl = details.querySelector('.stage-group-count');
        if (countEl) {
          const count = details.querySelectorAll('.output-entry, .tool-indicator, .tool-summary').length;
          countEl.textContent = count + ' entr' + (count === 1 ? 'y' : 'ies');
        }
      }

      // Update an existing stage group to its final status (complete/error/skipped)
      function collapseStageEntries(stage, status) {
        if (!outputContent) return;
        const groupId = 'stage-group-' + stage;
        const details = document.getElementById(groupId);

        if (!details) {
          // No group exists (shouldn't happen, but handle gracefully)
          return;
        }

        // Build status icon
        const icons = { complete: '\u2713', error: '\u2717', skipped: '\u21B7' };
        const icon = icons[status] || '';
        const statusClass = status === 'error' ? 'stage-error' : status === 'skipped' ? 'stage-skipped' : 'stage-complete';

        // Update group styling from running to final state
        details.className = 'stage-group ' + statusClass;

        // Close the group if it wasn't manually opened by the user
        // (if user opened it, respect that — but running groups start closed)
        if (!details.hasAttribute('open')) {
          details.removeAttribute('open');
        }

        // Drop transient progress nodes now that the stage is done — tool
        // indicators and tool summaries are not load-bearing once a stage
        // completes, and they accumulate unboundedly during a long pipeline
        // run (one .tool-indicator per tool call). The .output-entry log
        // lines are preserved so scrollback still reflects what happened.
        const transientNodes = details.querySelectorAll('.tool-indicator, .tool-summary');
        for (let i = 0; i < transientNodes.length; i++) {
          transientNodes[i].remove();
        }
        updateStageGroupCount(details);

        // Update summary with final icon (remove spinner)
        const stageName = formatStageName(stage);
        const summary = details.querySelector('.stage-group-summary');
        if (summary) {
          const count = details.querySelectorAll('.output-entry, .tool-indicator, .tool-summary').length;
          summary.innerHTML = '<span class="stage-group-icon">' + icon + '</span> ' +
            '<span class="stage-group-name">' + stageName + '</span>' +
            '<span class="stage-group-count">' + count + ' entr' + (count === 1 ? 'y' : 'ies') + '</span>';
        }
      }

      function appendEntry(entry) {
        if (!outputContent) return;

        const timestamp = new Date(entry.timestamp).toLocaleTimeString('en-US', {
          hour: '2-digit',
          minute: '2-digit',
          second: '2-digit',
          hour12: false
        });

        // Oversize guard — renderContent() on a large text or details payload
        // can easily 10x the byte size once markdown/ANSI/code-formatting
        // runs expand it into nested spans. At 500 entries this used to
        // reach GBs in the renderer, so above these caps we substitute a
        // placeholder and point the user at the on-disk log file.
        const entryTextLen = (entry.text && entry.text.length) || 0;
        const entryDetailsLen = (entry.details && entry.details.length) || 0;
        const isOversize =
          entryTextLen > MAX_WEBVIEW_ENTRY_BYTES ||
          entryDetailsLen > MAX_WEBVIEW_DETAILS_BYTES;

        // Build inner HTML for the entry
        const contentType = entry.contentType;
        const formattedClass = contentType ? ' formatted-content content-' + contentType : '';
        let html = '<div class="entry-header">' +
          '<span class="entry-time">' + timestamp + '</span>' +
          '<span class="entry-badge ' + getLevelClass(entry.level) + '">' + getLevelBadge(entry.level) + '</span>';
        if (entry.stage) {
          html += '<span class="entry-stage">[' + formatStageName(entry.stage) + ']</span>';
        }
        html += '</div>';

        if (isOversize) {
          const totalBytes = entryTextLen + entryDetailsLen;
          const approxKb = Math.max(1, Math.round(totalBytes / 1024));
          const combined = (entry.text || '') + (entry.details ? '\\n' + entry.details : '');
          let lineCount = 0;
          for (let i = 0; i < combined.length; i++) {
            if (combined.charCodeAt(i) === 10) lineCount++;
          }
          if (combined.length > 0) lineCount++;
          html += '<div class="entry-content entry-content-oversize">' +
            '<em>Content too large to render inline (' + approxKb + ' KB, ' +
            lineCount + ' line' + (lineCount === 1 ? '' : 's') +
            ') — see the on-disk log file.</em>' +
            '</div>';
        } else {
          html += '<div class="entry-content markdown-body' + formattedClass + '">' + renderContent(entry.text, contentType) + '</div>';
        }

        /** Build a fresh DOM element for one panel placement */
        function buildEntryEl() {
          const el = document.createElement('div');
          el.className = 'output-entry ' + getLevelClass(entry.level);
          el.dataset.entryId = entry.id;
          if (entry.collapsible && entry.details && !isOversize) {
            el.classList.add('collapsible', 'collapsed');
            const detailsContent = contentType
              ? renderContent(entry.details, contentType)
              : '<pre>' + escapeHtml(entry.details) + '</pre>';
            el.innerHTML =
              '<div class="entry-toggle">' +
              '<span class="toggle-icon">▶</span>' + html + '</div>' +
              '<div class="entry-details" style="display: none;">' + detailsContent + '</div>';
          } else {
            el.innerHTML = html;
          }
          return el;
        }

        /** Append an entry element to a given panel container */
        function appendToPanel(panel) {
          if (!panel) return;
          // Remove empty state
          const emptyState = panel.querySelector('.empty-state');
          if (emptyState) emptyState.remove();

          const el = buildEntryEl();
          // Place inside stage group only for the "All" panel (stage groups are global)
          if (entry.stage && panel === allPanel) {
            const group = getOrCreateStageGroup(entry.stage);
            if (group) {
              group.appendChild(el);
              updateStageGroupCount(group);
              return;
            }
          }
          panel.appendChild(el);
        }

        // Always append to the "All" panel
        const allPanel = outputContent.querySelector('.slot-panel[data-slot="null"]');
        appendToPanel(allPanel);

        // Also append to slot-specific panel if slotIndex is provided (Issue #2705)
        if (entry.slotIndex !== undefined && entry.slotIndex !== null) {
          const slotPanel = outputContent.querySelector('.slot-panel[data-slot="' + entry.slotIndex + '"]');
          appendToPanel(slotPanel);
        }

        // Auto-scroll the active panel
        if (autoScroll) {
          const active = getActivePanel() || allPanel;
          if (active) active.scrollTop = active.scrollHeight;
        }

        // Re-apply search to include new content (Issue #158)
        reapplySearchOnNewContent();
      }

      function getLevelClass(level) {
        const classes = { info: 'level-info', debug: 'level-debug', warning: 'level-warning',
                         error: 'level-error', tool: 'level-tool', user: 'level-user' };
        return classes[level] || 'level-info';
      }

      function getLevelBadge(level) {
        const labels = { info: 'INFO', debug: 'DEBUG', warning: 'WARN',
                        error: 'ERROR', tool: 'TOOL', user: 'USER' };
        return labels[level] || 'INFO';
      }

      function formatStageName(stage) {
        const labels = {
          'pipeline-start': 'Initialize',
          'issue-pickup': 'Issue Pickup',
          'feature-planning': 'Feature Planning',
          'feature-dev': 'Feature Development',
          'feature-validate': 'Feature Validation',
          'pr-create': 'PR Creation',
          'pr-merge': 'PR Merge',
          'pipeline-finish': 'Completion',
        };
        return labels[stage] || stage;
      }

      function escapeHtml(text) {
        const div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML;
      }

      // Tool indicator functions
      function appendToolIndicator(indicator) {
        if (!outputContent) return;

        const allPanel = outputContent.querySelector('.slot-panel[data-slot="null"]') || outputContent;

        // Remove empty state if present
        const emptyState = allPanel.querySelector('.empty-state');
        if (emptyState) {
          emptyState.remove();
        }

        const indicatorEl = document.createElement('div');
        indicatorEl.className = 'tool-indicator ' + getToolColorClass(indicator.tool) + (indicator.isActive ? ' active' : '');
        indicatorEl.dataset.toolId = indicator.id;

        const spinnerHtml = indicator.isActive ? '<div class="tool-indicator-spinner"></div>' : '';
        const iconHtml = '<span class="tool-indicator-icon">' + getToolIcon(indicator.tool) + '</span>';
        const labelHtml = '<span class="tool-indicator-label">' + escapeHtml(indicator.tool) + '</span>';
        const targetHtml = indicator.target ? '<span class="tool-indicator-target">' + escapeHtml(indicator.target) + '</span>' : '';

        indicatorEl.innerHTML = spinnerHtml + iconHtml + labelHtml + targetHtml;

        // Place inside the current stage group if one is active
        if (currentActiveStage) {
          const group = getOrCreateStageGroup(currentActiveStage);
          if (group) {
            group.appendChild(indicatorEl);
            updateStageGroupCount(group);
          } else {
            allPanel.appendChild(indicatorEl);
          }
        } else {
          allPanel.appendChild(indicatorEl);
        }

        if (autoScroll) {
          const active = getActivePanel() || allPanel;
          if (active) active.scrollTop = active.scrollHeight;
        }
      }

      function markToolIndicatorComplete(id) {
        const indicator = document.querySelector('[data-tool-id="' + id + '"]');
        if (indicator) {
          indicator.classList.remove('active');
          indicator.classList.add('complete');
          // Remove spinner
          const spinner = indicator.querySelector('.tool-indicator-spinner');
          if (spinner) spinner.remove();
        }
      }

      function appendToolSummary(summary) {
        if (!outputContent) return;

        const allPanel = outputContent.querySelector('.slot-panel[data-slot="null"]') || outputContent;

        const summaryEl = document.createElement('div');
        summaryEl.className = 'tool-summary';

        summaryEl.innerHTML =
          '<span class="tool-summary-icon">\\u2713</span>' +
          '<span class="tool-summary-text">' + escapeHtml(summary.formatted) + '</span>';

        // Place inside the current stage group if one is active
        if (currentActiveStage) {
          const group = getOrCreateStageGroup(currentActiveStage);
          if (group) {
            group.appendChild(summaryEl);
            updateStageGroupCount(group);
          } else {
            allPanel.appendChild(summaryEl);
          }
        } else {
          allPanel.appendChild(summaryEl);
        }

        if (autoScroll) {
          const active = getActivePanel() || allPanel;
          if (active) active.scrollTop = active.scrollHeight;
        }
      }

      function getToolColorClass(tool) {
        const colorMap = {
          'Edit': 'tool-edit',
          'Read': 'tool-read',
          'Write': 'tool-write',
          'Bash': 'tool-bash',
          'Glob': 'tool-search',
          'Grep': 'tool-search',
          'Task': 'tool-task',
          'WebFetch': 'tool-web',
          'WebSearch': 'tool-web',
          'AskUserQuestion': 'tool-question',
          'TodoWrite': 'tool-todo'
        };
        return colorMap[tool] || 'tool-unknown';
      }

      function getToolIcon(tool) {
        const iconMap = {
          'Edit': '\\u270E',
          'Read': '\\uD83D\\uDCC4',
          'Write': '\\uD83D\\uDCDD',
          'Bash': '\\u2318',
          'Glob': '\\uD83D\\uDD0D',
          'Grep': '\\uD83D\\uDD0E',
          'Task': '\\u2699',
          'WebFetch': '\\u2601',
          'WebSearch': '\\uD83C\\uDF10',
          'AskUserQuestion': '\\u2753',
          'TodoWrite': '\\u2611'
        };
        return iconMap[tool] || '\\uD83D\\uDEE0';
      }

      // Configure marked.js for safe rendering
      if (typeof marked !== 'undefined') {
        marked.setOptions({
          breaks: false, // Was: true — caused double-spacing (Issue #846)
          gfm: true,     // GitHub Flavored Markdown
          sanitize: false // We trust our own content
        });

        // Custom renderer for fenced code blocks (Issue #846)
        // Auto-collapse code blocks longer than CODE_COLLAPSE_THRESHOLD (8 lines)
        var renderer = new marked.Renderer();
        renderer.code = function(code, language) {
          // Handle marked v5+ object argument format
          var text = typeof code === 'object' ? code.text : code;
          var lang = typeof code === 'object' ? code.lang : language;
          var lines = text.split('\\n');
          var lineCount = lines.length;
          var langLabel = lang ? ', ' + lang : '';

          if (lineCount > 8) {
            // Collapse long fenced code blocks
            var id = 'code-' + Math.random().toString(36).slice(2, 11);
            return '<div class="collapsible-code collapsed" data-code-id="' + id + '">' +
              '<div class="code-toggle">' +
              '<span class="toggle-icon">\\u25B6</span> ' +
              '<span class="code-summary">Code block (' + lineCount + ' lines' + langLabel + ')</span>' +
              '</div>' +
              '<div class="code-details" style="display: none;">' +
              '<pre><code class="language-' + (lang || 'text') + '">' +
              escapeHtml(text) + '</code></pre>' +
              '</div></div>';
          }

          // Short code blocks render normally
          return '<pre><code class="language-' + (lang || 'text') + '">' +
            escapeHtml(text) + '</code></pre>';
        };

        marked.use({ renderer: renderer });
      }

      // Toggle collapsible fenced code blocks (Issue #846)
      function toggleCodeBlock(codeId) {
        var container = document.querySelector('[data-code-id="' + codeId + '"]');
        if (!container) return;
        var details = container.querySelector('.code-details');
        var icon = container.querySelector('.toggle-icon');
        if (details.style.display === 'none') {
          details.style.display = 'block';
          icon.textContent = '\\u25BC';
          container.classList.remove('collapsed');
        } else {
          details.style.display = 'none';
          icon.textContent = '\\u25B6';
          container.classList.add('collapsed');
        }
      }

      // ===== Content Formatting Functions (Issue #428) =====

      /**
       * Detect content type from text
       */
      function detectContentType(text) {
        if (!text || text.trim().length === 0) return 'text';

        const trimmed = text.trim();

        // Check for structured patch (JSON with patch-specific fields)
        if (isStructuredPatch(trimmed)) return 'structured-patch';

        // Check for unified diff
        if (isDiff(trimmed)) return 'diff';

        // Check for JSON
        if (isJson(trimmed)) return 'json';

        // Check for code blocks (Issue #639)
        if (isCode(trimmed)) return 'code';

        return 'text';
      }

      function isDiff(text) {
        const lines = text.split('\\n');
        if (lines.length < 2) return false;

        let hunkHeaders = 0, additions = 0, deletions = 0, fileHeaders = 0;

        for (const line of lines) {
          if (line.startsWith('@@') && line.includes('@@')) hunkHeaders++;
          else if (line.startsWith('--- ') || line.startsWith('+++ ')) fileHeaders++;
          else if (line.startsWith('+') && !line.startsWith('+++')) additions++;
          else if (line.startsWith('-') && !line.startsWith('---')) deletions++;
        }

        return hunkHeaders > 0 || (fileHeaders >= 2 && (additions > 0 || deletions > 0)) || (additions >= 2 && deletions >= 2);
      }

      function isJson(text) {
        const trimmed = text.trim();
        if (!trimmed.startsWith('{') && !trimmed.startsWith('[')) return false;
        try { JSON.parse(trimmed); return true; } catch { return false; }
      }

      function isStructuredPatch(text) {
        if (!text.trim().startsWith('{')) return false;
        try {
          const parsed = JSON.parse(text);
          return typeof parsed === 'object' && parsed !== null &&
            (('oldStart' in parsed && 'newStart' in parsed) ||
             ('hunks' in parsed && Array.isArray(parsed.hunks)) ||
             'structuredPatch' in parsed);
        } catch { return false; }
      }

      /**
       * Check if text looks like a code block (Issue #639)
       */
      function isCode(text) {
        const lines = text.split('\\n');
        const nonEmptyLines = lines.filter(function(l) { return l.trim().length > 0; });
        if (nonEmptyLines.length < 3) return false;

        var indicators = {};
        var codeLineCount = 0;

        var structuralPatterns = [
          { pattern: /[{]\\s*$/, name: 'open-brace' },
          { pattern: /^\\s*[}]/, name: 'close-brace' },
          { pattern: /;\\s*$/, name: 'semicolon' },
          { pattern: /=>\\s*[{(]?/, name: 'arrow' },
          { pattern: /\\)\\s*[:{]/, name: 'paren-block' },
          { pattern: /^\\s{2,}\\S/, name: 'indented' }
        ];

        var keywordPattern = /\\b(function|class|interface|type|const|let|var|import|export|return|if|else|for|while|switch|case|throw|async|await|new|this|extends|implements)\\b/;
        var typePattern = /:\\s*(string|number|boolean|void|any|null|undefined|Promise|Array|Record|Map|Set)\\b/;

        for (var i = 0; i < nonEmptyLines.length; i++) {
          var line = nonEmptyLines[i];
          var isCodeLine = false;

          for (var j = 0; j < structuralPatterns.length; j++) {
            if (structuralPatterns[j].pattern.test(line)) {
              indicators[structuralPatterns[j].name] = true;
              isCodeLine = true;
            }
          }

          if (keywordPattern.test(line)) {
            indicators['keyword'] = true;
            isCodeLine = true;
          }

          if (typePattern.test(line)) {
            indicators['type-annotation'] = true;
            isCodeLine = true;
          }

          if (isCodeLine) codeLineCount++;
        }

        var indicatorCount = Object.keys(indicators).length;
        var codeRatio = codeLineCount / nonEmptyLines.length;
        return indicatorCount >= 3 && codeRatio > 0.4;
      }

      /**
       * Format diff content with CSS classes
       */
      function formatDiff(text) {
        const lines = text.split('\\n');
        const formattedLines = lines.map(line => {
          const escapedLine = escapeHtml(line);
          if (line.startsWith('@@') && line.includes('@@')) {
            return '<div class="diff-line diff-hunk">' + escapedLine + '</div>';
          } else if (line.startsWith('+++') || line.startsWith('---')) {
            return '<div class="diff-line diff-header">' + escapedLine + '</div>';
          } else if (line.startsWith('+')) {
            return '<div class="diff-line diff-add">' + escapedLine + '</div>';
          } else if (line.startsWith('-')) {
            return '<div class="diff-line diff-del">' + escapedLine + '</div>';
          } else {
            return '<div class="diff-line diff-context">' + escapedLine + '</div>';
          }
        });
        return '<div class="diff-container">' + formattedLines.join('') + '</div>';
      }

      /**
       * Format JSON with pretty-printing
       */
      function formatJson(text) {
        try {
          const parsed = JSON.parse(text.trim());
          const formatted = JSON.stringify(parsed, null, 2);
          return '<pre class="formatted-json"><code class="language-json">' + escapeHtml(formatted) + '</code></pre>';
        } catch {
          return '<pre class="formatted-json"><code>' + escapeHtml(text) + '</code></pre>';
        }
      }

      /**
       * Format structured patch as readable diff
       */
      function formatStructuredPatch(text) {
        try {
          const parsed = JSON.parse(text.trim());
          const diffLines = [];

          if (parsed.oldFileName || parsed.newFileName) {
            diffLines.push('--- ' + (parsed.oldFileName || 'a/file'));
            diffLines.push('+++ ' + (parsed.newFileName || 'b/file'));
          }

          if (parsed.hunks && Array.isArray(parsed.hunks)) {
            for (const hunk of parsed.hunks) {
              diffLines.push('@@ -' + hunk.oldStart + ',' + hunk.oldLines + ' +' + hunk.newStart + ',' + hunk.newLines + ' @@');
              if (hunk.lines && Array.isArray(hunk.lines)) {
                diffLines.push(...hunk.lines);
              }
            }
          }

          if (diffLines.length > 0) {
            return formatDiff(diffLines.join('\\n'));
          }
          return formatJson(text);
        } catch {
          return formatJson(text);
        }
      }

      /**
       * Format code content with code-block styling (Issue #639)
       */
      function formatCode(text, language) {
        var escaped = escapeHtml(text);
        var lang = language || detectCodeLanguage(text);
        var langLabel = lang && lang !== 'text' ? lang.toUpperCase() : '';
        var header = langLabel
          ? '<div class="code-block-header"><span class="code-block-language">' + langLabel + '</span></div>'
          : '';
        return '<div class="code-block">' + header + '<code>' + escaped + '</code></div>';
      }

      /**
       * Detect programming language from code content (Issue #639)
       */
      function detectCodeLanguage(text) {
        var trimmed = text.trim();
        if (/\\b(interface|type\\s+\\w+\\s*=|:\\s*(string|number|boolean|void|Promise))/.test(trimmed)) return 'typescript';
        if (/\\bdef\\s+\\w+\\s*\\(|\\bimport\\s+\\w+|\\bclass\\s+\\w+.*:$/.test(trimmed)) return 'python';
        if (/\\bfunc\\s+\\w+|\\bpackage\\s+\\w+|\\bgo\\s+/.test(trimmed)) return 'go';
        if (/\\bpub\\s+fn\\s+|\\blet\\s+mut\\s+|\\bimpl\\s+/.test(trimmed)) return 'rust';
        if (/\\b(const|let|var|function|class|import|export|async|await)\\b/.test(trimmed)) return 'javascript';
        return 'text';
      }

      /**
       * Render content based on type (markdown, diff, json, etc.)
       */
      function renderContent(text, contentType) {
        // Auto-detect if no content type provided
        const type = contentType || detectContentType(text);

        switch (type) {
          case 'diff':
            return formatDiff(text);
          case 'json':
            return formatJson(text);
          case 'structured-patch':
            return formatStructuredPatch(text);
          case 'code':
            return formatCode(text);
          default:
            return renderMarkdown(text);
        }
      }

      function renderMarkdown(text) {
        // Convert literal \\n strings to actual newlines
        let processed = text.replace(/\\\\n/g, '\\n');

        // Close unclosed fenced code blocks before parsing.
        // Streaming content_block_delta messages may split a fenced code block
        // across multiple appendLine() calls, leaving an opening fence without
        // a matching close.  marked.js treats everything after the unclosed
        // fence as code, breaking the output.
        processed = balanceFencedCodeBlocks(processed);

        // Use marked.js if available, otherwise escape HTML
        if (typeof marked !== 'undefined' && marked.parse) {
          try {
            return marked.parse(processed);
          } catch (e) {
            console.error('Markdown parsing error:', e);
            return escapeHtml(processed).replace(/\\n/g, '<br>');
          }
        }

        // Fallback: just escape HTML and convert newlines
        return escapeHtml(processed).replace(/\\n/g, '<br>');
      }

      /**
       * Ensure fenced code blocks are balanced.
       *
       * Scans lines for GFM fence markers (backticks or tildes) and appends a
       * matching closing fence if the text ends with an unclosed block.  This
       * prevents marked.js from swallowing all subsequent content as code.
       */
      function balanceFencedCodeBlocks(text) {
        var BACKTICK = String.fromCharCode(96);
        var lines = text.split('\\n');
        var inFence = false;
        var fenceChar = '';
        var fenceLen = 0;

        for (var i = 0; i < lines.length; i++) {
          var trimmed = lines[i].trimStart();

          if (!inFence) {
            // Opening fence: line starts with 3+ backticks or tildes
            var ch = trimmed.charAt(0);
            if (ch === BACKTICK || ch === '~') {
              var count = 0;
              while (count < trimmed.length && trimmed.charAt(count) === ch) count++;
              if (count >= 3) {
                inFence = true;
                fenceChar = ch;
                fenceLen = count;
              }
            }
          } else {
            // Closing fence: same char, at least as many repeats, only whitespace after
            var ch2 = trimmed.charAt(0);
            if (ch2 === fenceChar) {
              var cnt = 0;
              while (cnt < trimmed.length && trimmed.charAt(cnt) === fenceChar) cnt++;
              if (cnt >= fenceLen && trimmed.substring(cnt).trim() === '') {
                inFence = false;
              }
            }
          }
        }

        if (inFence) {
          text = text + '\\n' + fenceChar.repeat(fenceLen);
        }
        return text;
      }

      // Render content for all existing entries on page load
      function renderAllMarkdown() {
        document.querySelectorAll('.entry-content[data-raw-text]').forEach(el => {
          const rawText = el.getAttribute('data-raw-text')
            .replace(/&quot;/g, '"')
            .replace(/&#10;/g, '\\n')
            .replace(/&lt;/g, '<')
            .replace(/&gt;/g, '>')
            .replace(/&amp;/g, '&');
          const contentType = el.getAttribute('data-content-type');
          el.innerHTML = renderContent(rawText, contentType);
        });
      }

      // Render on DOM ready
      if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', renderAllMarkdown);
      } else {
        renderAllMarkdown();
      }

      // ===== Pipeline State Functions (Issue #431) =====

      /**
       * Update pipeline-dependent UI elements based on running state
       * @param state - Pipeline state with isRunning, isBatchRunning, currentStage
       */
      function updatePipelineState(state) {
        // Track current active stage for tool indicator placement
        currentActiveStage = state.currentStage || null;

        const stopBtn = document.getElementById('stopBtn');
        if (stopBtn) {
          const isActive = state.isRunning;
          if (isActive) {
            stopBtn.disabled = false;
            stopBtn.classList.remove('disabled');
            stopBtn.title = state.currentStage
              ? 'Stop pipeline (' + state.currentStage + ')'
              : 'Stop pipeline';
          } else {
            stopBtn.disabled = true;
            stopBtn.classList.add('disabled');
            stopBtn.title = 'No pipeline running';
          }
        }
      }

      // ===== Question Prompt Functions (Issue #118) =====

      // Track active question state
      let activeQuestionId = null;
      const questionSelections = new Map(); // questionIndex → Map<questionIndex, selection>

      function appendQuestionPrompt(question) {
        if (!outputContent) return;

        // Remove empty state if present
        const emptyState = outputContent.querySelector('.empty-state');
        if (emptyState) {
          emptyState.remove();
        }

        activeQuestionId = question.id;
        questionSelections.set(question.id, new Map());

        const promptEl = document.createElement('div');
        promptEl.className = 'question-prompt waiting';
        promptEl.dataset.questionId = question.id;

        let html = '';

        // Render each question in the payload
        question.questions.forEach((q, qIndex) => {
          html += '<div class="question-item" data-question-index="' + qIndex + '">';
          html += '<div class="question-header">';
          html += '<span class="question-icon">\\u2753</span>';
          html += '<span class="question-badge">' + escapeHtml(q.header) + '</span>';
          html += '</div>';
          html += '<div class="question-text">' + escapeHtml(q.question) + '</div>';
          html += '<div class="question-options' + (q.multiSelect ? ' multi-select' : '') + '" data-multi-select="' + q.multiSelect + '">';

          // Render options as buttons
          q.options.forEach((opt, optIndex) => {
            html += '<button class="question-option-btn" ';
            html += 'data-question-id="' + question.id + '" ';
            html += 'data-question-index="' + qIndex + '" ';
            html += 'data-option-index="' + optIndex + '" ';
            html += 'data-option-label="' + escapeHtml(opt.label) + '" ';
            html += '>';
            html += '<span class="option-label">' + escapeHtml(opt.label) + '</span>';
            if (opt.description) {
              html += '<span class="option-description">' + escapeHtml(opt.description) + '</span>';
            }
            html += '</button>';
          });

          html += '</div>'; // question-options

          // Add "Other" custom input option
          html += '<div class="question-custom-input">';
          html += '<input type="text" placeholder="Or type your own answer..." ';
          html += 'data-question-id="' + question.id + '" ';
          html += 'data-question-index="' + qIndex + '" ';
          html += '/>';
          html += '</div>';

          html += '</div>'; // question-item
        });

        // Add action buttons
        html += '<div class="question-actions">';
        html += '<button class="question-submit-btn" data-question-id="' + question.id + '" disabled>Submit</button>';
        html += '<button class="question-cancel-btn" data-question-id="' + question.id + '">Skip</button>';
        html += '</div>';

        promptEl.innerHTML = html;
        const questionAllPanel = outputContent
          ? (outputContent.querySelector('.slot-panel[data-slot="null"]') || outputContent)
          : null;
        if (questionAllPanel) questionAllPanel.appendChild(promptEl);

        if (autoScroll) {
          const active = getActivePanel() || questionAllPanel;
          if (active) active.scrollTop = active.scrollHeight;
        }

        // Focus the first option
        const firstOption = promptEl.querySelector('.question-option-btn');
        if (firstOption) {
          firstOption.focus();
        }
      }

      function handleOptionClick(btn) {
        const questionId = btn.dataset.questionId;
        const questionIndex = parseInt(btn.dataset.questionIndex, 10);
        const optionLabel = btn.dataset.optionLabel;
        const optionsContainer = btn.closest('.question-options');
        const isMultiSelect = optionsContainer.dataset.multiSelect === 'true';

        // Clear custom input for this question
        const customInput = btn.closest('.question-item').querySelector('input');
        if (customInput) {
          customInput.value = '';
        }

        // Get or create selections map for this question
        if (!questionSelections.has(questionId)) {
          questionSelections.set(questionId, new Map());
        }
        const selections = questionSelections.get(questionId);

        if (isMultiSelect) {
          // Toggle selection
          if (btn.classList.contains('selected')) {
            btn.classList.remove('selected');
            const current = selections.get(questionIndex) || [];
            selections.set(questionIndex, current.filter(v => v !== optionLabel));
          } else {
            btn.classList.add('selected');
            const current = selections.get(questionIndex) || [];
            current.push(optionLabel);
            selections.set(questionIndex, current);
          }
        } else {
          // Single select - deselect others
          optionsContainer.querySelectorAll('.question-option-btn').forEach(b => {
            b.classList.remove('selected');
          });
          btn.classList.add('selected');
          selections.set(questionIndex, optionLabel);
        }

        updateSubmitButton(questionId);
      }

      function handleCustomInput(input) {
        const questionId = input.dataset.questionId;
        const questionIndex = parseInt(input.dataset.questionIndex, 10);
        const value = input.value.trim();

        // Deselect all options when typing custom input
        const questionItem = input.closest('.question-item');
        questionItem.querySelectorAll('.question-option-btn.selected').forEach(btn => {
          btn.classList.remove('selected');
        });

        // Update selection
        if (!questionSelections.has(questionId)) {
          questionSelections.set(questionId, new Map());
        }
        const selections = questionSelections.get(questionId);

        if (value) {
          selections.set(questionIndex, value);
        } else {
          selections.delete(questionIndex);
        }

        updateSubmitButton(questionId);
      }

      function handleCustomInputKeydown(event, input) {
        if (event.key === 'Enter') {
          event.preventDefault();
          const questionId = input.dataset.questionId;
          submitQuestionResponse(questionId);
        }
      }

      function updateSubmitButton(questionId) {
        const promptEl = document.querySelector('[data-question-id="' + questionId + '"]');
        if (!promptEl) return;

        const submitBtn = promptEl.querySelector('.question-submit-btn');
        const selections = questionSelections.get(questionId);

        // Check if all questions have answers
        const questionItems = promptEl.querySelectorAll('.question-item');
        let allAnswered = true;

        questionItems.forEach((item, index) => {
          const selection = selections?.get(index);
          if (!selection || (Array.isArray(selection) && selection.length === 0)) {
            allAnswered = false;
          }
        });

        submitBtn.disabled = !allAnswered;
      }

      function submitQuestionResponse(questionId) {
        const promptEl = document.querySelector('[data-question-id="' + questionId + '"]');
        if (!promptEl) return;

        const selections = questionSelections.get(questionId);
        if (!selections) return;

        // Build answers object
        const answers = {};
        selections.forEach((value, index) => {
          answers['q' + index] = value;
        });

        // Disable the prompt UI
        promptEl.querySelectorAll('button, input').forEach(el => {
          el.disabled = true;
        });

        // Send response to extension
        vscode.postMessage({
          type: 'question-response',
          questionId: questionId,
          response: { answers: answers }
        });

        activeQuestionId = null;
      }

      function cancelQuestion(questionId) {
        const promptEl = document.querySelector('[data-question-id="' + questionId + '"]');
        if (!promptEl) return;

        // Disable the prompt UI
        promptEl.querySelectorAll('button, input').forEach(el => {
          el.disabled = true;
        });

        promptEl.classList.add('cancelled');

        // Send cancel to extension
        vscode.postMessage({
          type: 'question-response',
          questionId: questionId,
          response: null // null indicates cancelled/skipped
        });

        activeQuestionId = null;
      }

      function markQuestionAnswered(questionId, answer) {
        const promptEl = document.querySelector('[data-question-id="' + questionId + '"]');
        if (!promptEl) return;

        promptEl.classList.remove('waiting');
        promptEl.classList.add('answered');

        // Replace options with answered text
        const actionsEl = promptEl.querySelector('.question-actions');
        if (actionsEl) {
          let answerText = '';
          if (typeof answer === 'string') {
            answerText = answer;
          } else if (typeof answer === 'object' && answer.answers) {
            answerText = Object.values(answer.answers).flat().join(', ');
          }

          actionsEl.innerHTML =
            '<div class="question-answered-text">' +
            '<span class="answer-label">Answered:</span>' +
            '<span class="answer-value">' + escapeHtml(answerText) + '</span>' +
            '</div>';
        }

        // Disable all interactive elements
        promptEl.querySelectorAll('button, input').forEach(el => {
          el.disabled = true;
        });
      }



      // Toggle function for collapsible entries (inside IIFE for CSP compliance)
      function toggleEntry(entryId) {
        console.log('[OutputWindow] toggleEntry called with entryId:', entryId);
        const entry = document.querySelector('[data-entry-id="' + entryId + '"]');
        if (!entry) {
          console.warn('[OutputWindow] No element found for entry ID:', entryId);
          return;
        }
        if (!entry.classList.contains('collapsible')) {
          console.warn('[OutputWindow] Entry is not collapsible:', entryId);
          return;
        }
        const details = entry.querySelector('.entry-details');
        const icon = entry.querySelector('.toggle-icon');

        if (details && details.style.display === 'none') {
          details.style.display = 'block';
          if (icon) icon.textContent = '▼';
          entry.classList.remove('collapsed');
          console.log('[OutputWindow] Entry EXPANDED:', entryId);
        } else if (details) {
          details.style.display = 'none';
          if (icon) icon.textContent = '▶';
          entry.classList.add('collapsed');
          console.log('[OutputWindow] Entry COLLAPSED:', entryId);
        }

        vscode.postMessage({ type: 'toggle-entry', entryId: entryId });
      }

      // Initialize auto-scroll — scroll the active panel to bottom
      if (autoScroll && outputContent) {
        const initPanel = getActivePanel() || outputContent;
        initPanel.scrollTop = initPanel.scrollHeight;
      }
    })();
  `;
}

/**
 * Search state for initial HTML rendering (Issue #158)
 */
export interface SearchState {
  searchText: string;
  caseSensitive: boolean;
  useRegex: boolean;
}

/**
 * Render inline badge HTML for a slot tab button (Issue #2815).
 *
 * Shows status icon (spinner/✓/✗), stage chip, live elapsed timer, and cost.
 * Returns empty string when the slot has no displayable badge data.
 */
function getTabBadgesHtml(slot: SlotInfo): string {
  const status = slot.status ?? "pending";
  const cost = slot.tokenUsage?.costUsd ?? 0;
  const costStr = cost > 0 ? `$${cost.toFixed(4)}` : "";

  const statusIcon =
    status === "running"
      ? '<span class="tab-badge-spinner"></span>'
      : status === "complete"
        ? "✓"
        : status === "error"
          ? "✗"
          : "";

  const elapsedHtml =
    slot.startedAt != null
      ? `<span class="tab-badge tab-badge-elapsed"${slot.completedAt != null ? ` data-completed-at="${slot.completedAt}"` : ""} data-started-at="${slot.startedAt}">0:00</span>`
      : "";

  const parts: string[] = [];
  if (statusIcon) {
    parts.push(`<span class="tab-badge tab-badge-status tab-badge-${status}">${statusIcon}</span>`);
  }
  if (slot.stage) {
    parts.push(
      `<span class="tab-badge tab-badge-stage">${escapeHtml(formatStageName(slot.stage))}</span>`
    );
  }
  if (elapsedHtml) parts.push(elapsedHtml);
  if (costStr) parts.push(`<span class="tab-badge tab-badge-cost">${escapeHtml(costStr)}</span>`);

  return parts.join("");
}

/**
 * Render the per-slot tab bar HTML.
 *
 * Hidden (returns empty string) only when no slots are registered.
 * Single-slot pipelines render a tab bar so users can track individual runs.
 *
 * @param activeSlots  - Sorted list of active slot infos
 * @param activeSlotIndex - Currently selected slot index, or null for "All"
 * @see Issue #2705, #2812, #2815
 */
function getTabBarHtml(activeSlots: SlotInfo[], activeSlotIndex: number | null): string {
  if (activeSlots.length === 0) return "";

  const allActive =
    activeSlotIndex === null ? ' class="slot-tab-btn active"' : ' class="slot-tab-btn"';
  let html = `<div class="slot-tab-bar" id="slotTabBar">`;
  html += `<button${allActive} data-slot="null" title="Overview of all active slots">Overview</button>`;

  for (const slot of activeSlots) {
    const isActive = activeSlotIndex === slot.slotIndex;
    const archivedCls = slot.archived ? " archived" : "";
    const cls = isActive
      ? ` class="slot-tab-btn active${archivedCls}"`
      : ` class="slot-tab-btn${archivedCls}"`;
    const stageLabel = slot.stage ? ` · ${formatStageName(slot.stage)}` : "";
    const archivedChip = slot.archived
      ? ` <span class="slot-tab-chip slot-tab-chip-archived">Archived</span>`
      : "";
    const label = `Slot ${slot.slotIndex + 1} · #${slot.issueNumber}${stageLabel}`;
    const titleHint = slot.archived ? " (archived — rebuilt from on-disk log)" : "";
    const fullTitle = `Slot ${slot.slotIndex + 1}: #${slot.issueNumber} — ${escapeHtml(slot.title)}${stageLabel}${titleHint}`;
    html += `<button${cls} data-slot="${slot.slotIndex}" title="${fullTitle}">${escapeHtml(label)}${archivedChip}${getTabBadgesHtml(slot)}</button>`;
  }

  html += `</div>`;
  return html;
}

/**
 * Format an elapsed duration in ms as "Hh Mm Ss", "Mm Ss", or "Ss".
 */
function formatElapsedDuration(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const s = total % 60;
  const m = Math.floor(total / 60) % 60;
  const h = Math.floor(total / 3600);
  if (h > 0) return `${h}h ${m}m ${s}s`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

/**
 * Render HTML for a single overview card (Issue #2817).
 */
function getOverviewCardHtml(slot: SlotInfo, nowMs: number): string {
  const status = slot.status ?? "pending";
  const statusLabel =
    status === "running"
      ? "Running"
      : status === "complete"
        ? "Complete"
        : status === "error"
          ? "Error"
          : status === "skipped"
            ? "Skipped"
            : "Pending";

  const stageLabel = slot.stage ? formatStageName(slot.stage) : "—";

  const hasStart = typeof slot.startedAt === "number" && slot.startedAt > 0;
  const completedAt = slot.completedAt ?? null;
  const endMs = completedAt ?? nowMs;
  const elapsedMs = hasStart ? endMs - (slot.startedAt as number) : 0;
  const elapsedText = hasStart ? formatElapsedDuration(elapsedMs) : "—";

  const usage = slot.tokenUsage ?? {
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheCreationTokens: 0,
    costUsd: 0,
  };
  const cacheTokens = (usage.cacheReadTokens ?? 0) + (usage.cacheCreationTokens ?? 0);
  const cost = usage.costUsd ?? 0;
  const costText = `$${cost.toFixed(4)}`;
  const tokensText = `${usage.inputTokens.toLocaleString()} in · ${usage.outputTokens.toLocaleString()} out · ${cacheTokens.toLocaleString()} cache`;

  const repoLine = slot.repoSlug
    ? `<div class="overview-card-repo">${escapeHtml(slot.repoSlug)}</div>`
    : "";

  const elapsedAttrs = hasStart
    ? ` data-started-at="${slot.startedAt as number}"${completedAt != null ? ` data-completed-at="${completedAt}"` : ""}`
    : "";

  const slotIndex = slot.slotIndex;
  const issueTitle = slot.title ?? "";

  // Phase span is always rendered (possibly empty) so the patch handler has
  // a consistent target node; CSS hides empty spans (Issue #3010).
  const phase = slot.currentPhase;
  const phaseText = phase ? `${escapeHtml(phase.name)} · ${phase.index}/${phase.total}` : "";

  return `<div class="overview-card" data-slot="${slotIndex}" role="button" tabindex="0" aria-label="Open tab for issue #${slot.issueNumber}">
      <div class="overview-card-header">
        <div class="overview-card-title"><strong>#${slot.issueNumber}</strong>${issueTitle ? ` <span class="overview-card-issue-title">${escapeHtml(issueTitle)}</span>` : ""}</div>
        ${repoLine}
      </div>
      <div class="overview-card-status">
        <span class="overview-status-badge overview-status-${status}">${statusLabel}</span>
        <span class="overview-card-stage">${escapeHtml(stageLabel)}</span>
        <span class="overview-card-phase">${phaseText}</span>
      </div>
      <div class="overview-card-metrics">
        <div class="overview-card-elapsed"${elapsedAttrs}>${escapeHtml(elapsedText)}</div>
        <div class="overview-card-cost">${escapeHtml(costText)}</div>
      </div>
      <div class="overview-card-tokens">${escapeHtml(tokensText)}</div>
      <div class="overview-card-actions">
        <button type="button" class="overview-card-btn" data-overview-action="open-tab" data-slot="${slotIndex}">Open tab</button>
        <button type="button" class="overview-card-btn" data-overview-action="reveal-github" data-slot="${slotIndex}">Open GitHub</button>
        <button type="button" class="overview-card-btn" data-overview-action="open-log" data-slot="${slotIndex}">Open log</button>
      </div>
    </div>`;
}

/**
 * Render the Overview dashboard panel HTML (Issue #2817).
 *
 * One card per registered slot. Each card surfaces issue header, status badge,
 * current stage, elapsed time, cost, token totals, and per-slot action buttons.
 * Clicking the card (or any action button) routes through the existing
 * `data-slot` tab-switch handler for consistency.
 *
 * @param activeSlots - Sorted list of active slot infos
 * @param nowMs       - Injectable "now" timestamp (defaults to Date.now()) to
 *                      keep rendering deterministic under test.
 */
export function getOverviewPanelHtml(activeSlots: SlotInfo[], nowMs: number = Date.now()): string {
  // Exclude archived slots — the Overview is for live pipeline work, not
  // historical runs rehydrated from on-disk logs. Archived runs remain
  // browsable via their per-slot tabs.
  const liveSlots = activeSlots.filter((s) => !s.archived);
  if (liveSlots.length === 0) {
    return `<div class="overview-panel overview-panel-empty">No active pipeline slots.</div>`;
  }
  const cards = liveSlots.map((slot) => getOverviewCardHtml(slot, nowMs)).join("");
  return `<div class="overview-panel">${cards}</div>`;
}

/**
 * Render per-slot tab panel HTML for the initial page load.
 *
 * Generates one panel per active slot plus the "Overview" aggregated panel.
 * CSS controls visibility — only the active panel is shown.
 *
 * @param entries     - Aggregated entries (unused when overview is active; kept
 *                      for backwards compatibility with zero-slot rendering)
 * @param stages      - Global stage progress list (used only in zero-slot mode)
 * @param activeSlots - Active slot infos
 * @param slotEntries - Map<slotIndex, entries> for per-slot panels
 * @param activeSlotIndex - Active slot, or null for "Overview"
 * @param slotStages  - Map<slotIndex, StageProgress[]> for per-slot stage groups (Issue #2814)
 * @see Issue #2705, #2814, #2817
 */
function getSlotPanelsHtml(
  entries: OutputEntry[],
  stages: StageProgress[],
  activeSlots: SlotInfo[],
  slotEntries: Map<number, OutputEntry[]>,
  activeSlotIndex: number | null,
  slotStages?: Map<number, StageProgress[]>
): string {
  if (activeSlots.length === 0) {
    return `<div class="slot-panel active" id="slot-panel-all" data-slot="null">${getEntriesHtml(entries, stages)}</div>`;
  }

  const allActive = activeSlotIndex === null ? " active" : "";
  // When slots are registered, the "all" panel now renders the Overview
  // dashboard instead of the aggregated entries (Issue #2817).
  const overviewHtml = getOverviewPanelHtml(activeSlots);
  let html = `<div class="slot-panel${allActive}" id="slot-panel-all" data-slot="null">${overviewHtml}</div>`;

  for (const slot of activeSlots) {
    const isActive = activeSlotIndex === slot.slotIndex;
    const cls = isActive ? " active" : "";
    const panelEntries = slotEntries.get(slot.slotIndex) ?? [];
    const panelStages = slotStages?.get(slot.slotIndex) ?? stages;
    html += `<div class="slot-panel${cls}" id="slot-panel-${slot.slotIndex}" data-slot="${slot.slotIndex}">${getEntriesHtml(panelEntries, panelStages)}</div>`;
  }

  return html;
}

/**
 * Generate the full output window HTML
 */
export function getOutputWindowHtml(
  webview: vscode.Webview,
  entries: OutputEntry[],
  _stages: StageProgress[],
  autoScroll: boolean,
  wordWrap: boolean,
  showTimestamps: boolean,
  issueNumber?: number,
  searchState?: SearchState,
  activeSlots?: SlotInfo[],
  activeSlotIndex?: number | null,
  slotEntries?: Map<number, OutputEntry[]>,
  slotStages?: Map<number, StageProgress[]>
): string {
  const nonce = getNonce();

  const title = (() => {
    const slots = activeSlots ?? [];
    const activeIdx = activeSlotIndex ?? null;
    const slot =
      activeIdx !== null && activeIdx !== undefined
        ? slots.find((s) => s.slotIndex === activeIdx)
        : slots[0];
    if (slot) {
      const repoSuffix = slot.repoSlug ? ` · ${slot.repoSlug}` : "";
      const titleSuffix = slot.title ? ` — ${slot.title}` : "";
      return `Nightgauge Output — #${slot.issueNumber}${titleSuffix}${repoSuffix}`;
    }
    if (issueNumber) {
      return `Nightgauge Output - Issue #${issueNumber}`;
    }
    return "Nightgauge Output";
  })();

  // Search state defaults (Issue #158)
  const searchText = searchState?.searchText ?? "";
  const searchCaseSensitive = searchState?.caseSensitive ?? false;
  const searchUseRegex = searchState?.useRegex ?? false;

  // Slot tab bar and panels (Issue #2705, #2812)
  const slots = activeSlots ?? [];
  const activeSlot = activeSlotIndex ?? null;
  const tabBarHtml = getTabBarHtml(slots, activeSlot);
  const hasMultipleSlots = slots.length > 0;
  const slotPanelsHtml = getSlotPanelsHtml(
    entries,
    _stages,
    slots,
    slotEntries ?? new Map(),
    activeSlot,
    slotStages
  );

  return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}' https://cdn.jsdelivr.net; connect-src https://cdn.jsdelivr.net;">
  <title>${title}</title>
  <script src="https://cdn.jsdelivr.net/npm/marked/marked.min.js" nonce="${nonce}"></script>
  <style>
    ${getStyles()}
  </style>
</head>
<body>
  <div class="output-window mode-headless">
    <header class="output-header">
      <span class="output-title">${title}</span>
      <div class="header-actions">
        <div class="mode-indicator mode-headless" id="modeIndicator" title="Headless mode: Stream-json output, token tracking enabled">
          <span class="mode-indicator-icon">⚙</span>
          <span class="mode-indicator-label">Headless</span>
        </div>
        <div class="search-container${searchText ? "" : " collapsed"}" id="searchContainer">
          <button class="search-toggle-icon-btn" id="searchToggleBtn" title="Search (Ctrl+F)">&#x1F50D;</button>
          <input type="text" class="search-input" id="searchInput" placeholder="Search..." value="${escapeHtml(searchText)}" />
          <span class="search-match-count" id="searchMatchCount"></span>
          <button class="search-nav-btn" id="searchPrevBtn" title="Previous match (Shift+Enter)" disabled>&lt;</button>
          <button class="search-nav-btn" id="searchNextBtn" title="Next match (Enter)" disabled>&gt;</button>
          <button class="search-toggle-btn ${searchCaseSensitive ? "enabled" : ""}" id="searchCaseSensitiveBtn" title="Case sensitive">Aa</button>
          <button class="search-toggle-btn ${searchUseRegex ? "enabled" : ""}" id="searchRegexBtn" title="Use regular expression">.*</button>
          <button class="search-clear-btn" id="searchClearBtn" title="Clear search">×</button>
        </div>
        <button class="action-btn auto-scroll-indicator ${autoScroll ? "enabled" : ""}" id="autoScrollBtn">
          ${autoScroll ? "Auto-scroll: ON" : "Auto-scroll: OFF"}
        </button>
        <button class="action-btn word-wrap-indicator ${wordWrap ? "enabled" : ""}" id="wordWrapBtn" title="Toggle word wrap for long lines">
          ${wordWrap ? "Word wrap: ON" : "Word wrap: OFF"}
        </button>
        <button class="action-btn timestamp-indicator ${showTimestamps ? "enabled" : ""}" id="timestampBtn" title="Toggle timestamp display for each line">
          ${showTimestamps ? "Timestamps: ON" : "Timestamps: OFF"}
        </button>
        <button class="action-btn" id="copyBtn" title="Copy output to clipboard">Copy</button>
        <button class="action-btn" id="clearBtn" title="Clear output">Clear</button>
        <button class="action-btn" id="exportBtn" title="Export as JSON">Export</button>
        <button class="action-btn danger" id="stopBtn" title="No pipeline running" disabled>Stop</button>
      </div>
    </header>

    ${hasMultipleSlots ? tabBarHtml : ""}
    <main class="output-content ${wordWrap ? "word-wrap-enabled" : "word-wrap-disabled"} ${showTimestamps ? "" : "timestamps-hidden"} ${hasMultipleSlots ? "has-slot-tabs" : ""}" id="outputContent">
      ${slotPanelsHtml}
    </main>

    <div class="message-input-container" id="messageInputContainer">
      <div class="message-input-wrapper">
        <textarea class="message-input" id="messageInput" placeholder="Send a message to the agent..." rows="1" disabled></textarea>
        <span class="message-input-hint">Enter to send, Shift+Enter for newline, ↑/↓ for history</span>
      </div>
      <button class="message-send-btn" id="messageSendBtn" disabled>Send</button>
    </div>

  </div>

  <script nonce="${nonce}">
    ${getScript()}
  </script>
</body>
</html>`;
}
