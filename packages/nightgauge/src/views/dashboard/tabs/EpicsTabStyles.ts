/**
 * EpicsTabStyles - CSS styles for the epics & project board tab
 *
 * Extracted from EpicsTabHtml.ts to keep each file under 1000 lines (#1542).
 */

export function getEpicsTabStyles(): string {
  return `
    /* Epic Estimates Section */
    .epic-estimates-section {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }

    .epic-estimates-section .section-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: var(--spacing-md);
    }

    .epic-estimates-section h3 {
      font-size: 1.1em;
      font-weight: 600;
    }

    .epic-count {
      font-size: 0.9em;
      color: var(--vscode-descriptionForeground);
    }

    .epic-cards-container {
      display: flex;
      flex-direction: column;
      gap: var(--spacing-md);
    }

    .epic-card {
      background: var(--vscode-editor-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
    }

    .epic-header {
      display: flex;
      justify-content: space-between;
      align-items: flex-start;
      margin-bottom: var(--spacing-md);
      gap: var(--spacing-md);
    }

    .epic-title {
      display: flex;
      flex-direction: column;
      gap: var(--spacing-xs);
      flex: 1;
    }

    .epic-title a {
      color: var(--vscode-textLink-foreground);
      text-decoration: none;
      font-size: 1em;
    }

    .epic-title a:hover {
      text-decoration: underline;
    }

    .epic-title-text {
      color: var(--vscode-descriptionForeground);
      font-size: 0.9em;
    }

    .epic-badges {
      display: flex;
      gap: var(--spacing-xs);
      flex-shrink: 0;
    }

    .confidence-badge {
      padding: 2px 8px;
      border-radius: var(--border-radius);
      font-size: 0.75em;
      font-weight: 600;
    }

    .confidence-high {
      background: var(--vscode-charts-green);
      color: white;
    }

    .confidence-medium {
      background: var(--vscode-charts-yellow);
      color: black;
    }

    .confidence-low {
      background: var(--vscode-charts-orange);
      color: white;
    }

    .epic-warning {
      background: var(--vscode-inputValidation-warningBackground, rgba(255, 206, 86, 0.15));
      border: 1px solid var(--vscode-inputValidation-warningBorder, rgba(255, 206, 86, 0.5));
      color: var(--vscode-inputValidation-warningForeground, inherit);
      padding: 6px 10px;
      border-radius: var(--border-radius);
      font-size: 0.85em;
      margin-bottom: var(--spacing-sm);
    }

    .warning-icon {
      margin-right: 4px;
    }

    .epic-failed-section {
      margin-top: var(--spacing-md);
      border-top: 1px solid var(--vscode-panel-border);
      padding-top: var(--spacing-md);
    }

    .epic-failed-header {
      color: var(--vscode-descriptionForeground);
      font-size: 0.9em;
      margin-bottom: var(--spacing-sm);
      display: flex;
      align-items: center;
      gap: var(--spacing-xs);
    }

    .epic-failed-icon {
      font-size: 1.1em;
    }

    .epic-card-failed {
      opacity: 0.75;
      border-style: dashed;
    }

    .epic-progress-badge {
      padding: 2px 8px;
      border-radius: var(--border-radius);
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
      font-size: 0.75em;
    }

    .epic-metrics {
      display: grid;
      grid-template-columns: repeat(2, 1fr);
      gap: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }

    .epic-metric {
      text-align: center;
    }

    .epic-metric-value {
      font-size: 1.5em;
      font-weight: 600;
      color: var(--vscode-charts-blue);
    }

    .epic-metric-label {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
      margin-top: var(--spacing-xs);
    }

    .epic-details {
      margin-top: var(--spacing-md);
    }

    .epic-details summary {
      cursor: pointer;
      font-weight: 500;
      padding: var(--spacing-sm);
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
      border-radius: var(--border-radius);
      list-style: none;
    }

    .epic-details summary:hover {
      background: var(--vscode-button-secondaryHoverBackground);
    }

    .epic-details summary::-webkit-details-marker {
      display: none;
    }

    .epic-details[open] summary {
      margin-bottom: var(--spacing-md);
    }

    .sub-issues-list {
      list-style: none;
      padding: 0;
      margin-bottom: var(--spacing-md);
    }

    .sub-issue {
      display: grid;
      grid-template-columns: auto auto auto 1fr auto;
      gap: var(--spacing-sm);
      align-items: center;
      padding: var(--spacing-sm);
      border-bottom: 1px solid var(--vscode-panel-border);
    }

    .sub-issue:last-child {
      border-bottom: none;
    }

    .sub-issue.closed {
      opacity: 0.6;
    }

    .sub-issue.closed .sub-issue-title {
      text-decoration: line-through;
    }

    .sub-issue-status {
      font-family: monospace;
      font-weight: bold;
    }

    .sub-issue-id {
      color: var(--vscode-textLink-foreground);
      font-weight: 500;
    }

    .sub-issue-size {
      color: var(--vscode-descriptionForeground);
      font-size: 0.85em;
    }

    .sub-issue-title {
      color: var(--vscode-foreground);
    }

    .sub-issue-estimate {
      color: var(--vscode-charts-blue);
      font-weight: 500;
      font-size: 0.9em;
    }

    .epic-footer {
      display: flex;
      flex-direction: column;
      gap: var(--spacing-xs);
      padding: var(--spacing-md);
      background: var(--vscode-editorWidget-background);
      border-radius: var(--border-radius);
      font-size: 0.9em;
    }

    .epic-footer-item {
      color: var(--vscode-descriptionForeground);
    }

    .epic-estimates-section .empty-message {
      color: var(--vscode-descriptionForeground);
      font-style: italic;
      text-align: center;
      padding: var(--spacing-lg) 0;
    }

    /* Cross-Repo Epic Styles (Issue #330) */
    .cross-repo-section {
      border-color: var(--vscode-charts-blue);
    }

    .cross-repo-epic {
      border-left: 3px solid var(--vscode-charts-blue);
    }

    .cross-repo-badge {
      display: inline-block;
      padding: 2px 6px;
      background: var(--vscode-charts-blue);
      color: white;
      border-radius: var(--border-radius);
      font-size: 0.75em;
      font-weight: 500;
      margin-left: var(--spacing-xs);
    }

    .repo-progress-container {
      display: flex;
      flex-direction: column;
      gap: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }

    .repo-progress-section {
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      overflow: hidden;
    }

    .repo-progress-section > summary {
      display: flex;
      justify-content: space-between;
      align-items: center;
      padding: var(--spacing-sm) var(--spacing-md);
      background: var(--vscode-editor-background);
      cursor: pointer;
      list-style: none;
    }

    .repo-progress-section > summary::-webkit-details-marker {
      display: none;
    }

    .repo-progress-section > summary:hover {
      background: var(--vscode-list-hoverBackground);
    }

    .repo-name {
      font-weight: 600;
      color: var(--vscode-textLink-foreground);
    }

    .repo-summary-stats {
      display: flex;
      gap: var(--spacing-md);
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }

    .repo-completion {
      font-weight: 600;
      color: var(--vscode-charts-green);
    }

    .repo-issues {
      color: var(--vscode-foreground);
    }

    .repo-remaining {
      color: var(--vscode-charts-blue);
    }

    .repo-progress-bar-container {
      height: 4px;
      background: var(--vscode-progressBar-background);
    }

    .repo-progress-bar {
      height: 100%;
      background: var(--vscode-charts-green);
      transition: width 0.3s ease;
    }

    .repo-sub-issues-list {
      list-style: none;
      padding: var(--spacing-sm) var(--spacing-md);
      margin: 0;
      min-height: 60px;
      overflow-y: auto;
    }

    .repo-error {
      border-color: var(--vscode-charts-red);
    }

    .repo-error-badge {
      color: var(--vscode-charts-red);
      font-weight: 500;
    }

    .repo-error-message {
      padding: var(--spacing-md);
      color: var(--vscode-errorForeground);
      font-size: 0.9em;
    }

    /* Project Board Widget (Issue #134) */
    .project-board-widget {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }

    .project-board-widget .section-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: var(--spacing-md);
    }

    .project-board-widget .section-header h3 {
      font-size: 1.1em;
      font-weight: 600;
      margin: 0;
    }

    .project-board-widget .widget-actions {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
    }

    .project-board-widget .refresh-widget-btn {
      background: transparent;
      border: none;
      color: var(--vscode-foreground);
      cursor: pointer;
      padding: var(--spacing-xs);
      border-radius: var(--border-radius);
      font-size: 1.1em;
    }

    .project-board-widget .refresh-widget-btn:hover {
      background: var(--vscode-list-hoverBackground);
    }

    .status-counts-grid {
      display: grid;
      grid-template-columns: repeat(4, 1fr);
      gap: var(--spacing-sm);
      margin-bottom: var(--spacing-md);
    }

    @media (max-width: 600px) {
      .status-counts-grid {
        grid-template-columns: repeat(2, 1fr);
      }
    }

    .status-count-card {
      background: var(--vscode-editor-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-sm);
      text-align: center;
    }

    .status-count-value {
      font-size: 1.5em;
      font-weight: 600;
      color: var(--vscode-foreground);
    }

    .status-count-label {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
    }

    .status-ready { border-left: 3px solid var(--vscode-charts-green); }
    .status-in-progress { border-left: 3px solid var(--vscode-charts-blue); }
    .status-in-review { border-left: 3px solid var(--vscode-charts-yellow); }
    .status-done { border-left: 3px solid var(--vscode-charts-purple, #9d6aba); }

    .top-ready-issues {
      margin-bottom: var(--spacing-md);
    }

    .top-ready-issues h4 {
      font-size: 0.9em;
      font-weight: 600;
      margin: 0 0 var(--spacing-sm) 0;
      color: var(--vscode-foreground);
    }

    .ready-issues-list {
      list-style: none;
      padding: 0;
      margin: 0;
    }

    .ready-issue-item {
      display: flex;
      align-items: center;
      gap: var(--spacing-xs);
      padding: var(--spacing-xs) 0;
      border-bottom: 1px solid var(--vscode-panel-border);
      font-size: 0.9em;
    }

    .ready-issue-item:last-child {
      border-bottom: none;
    }

    .issue-priority {
      width: 18px;
      text-align: center;
    }

    .issue-link {
      color: var(--vscode-textLink-foreground);
      text-decoration: none;
      font-weight: 500;
    }

    .issue-link:hover {
      text-decoration: underline;
    }

    .issue-title {
      flex: 1;
      color: var(--vscode-descriptionForeground);
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }

    .sprint-info {
      display: flex;
      align-items: center;
      gap: var(--spacing-xs);
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
      padding: 2px 8px;
      border-radius: var(--border-radius);
      font-size: 0.85em;
    }

    .sprint-icon {
      font-size: 0.9em;
    }

    .widget-footer {
      display: flex;
      justify-content: space-between;
      align-items: center;
      padding-top: var(--spacing-sm);
      border-top: 1px solid var(--vscode-panel-border);
      font-size: 0.85em;
    }

    .open-board-link {
      color: var(--vscode-textLink-foreground);
      text-decoration: none;
    }

    .open-board-link:hover {
      text-decoration: underline;
    }

    .last-refreshed {
      color: var(--vscode-descriptionForeground);
    }

    .project-board-widget.empty-state,
    .project-board-widget.error-state,
    .project-board-widget.loading-state {
      text-align: center;
      color: var(--vscode-descriptionForeground);
    }

    .project-board-widget .error-message {
      color: var(--vscode-errorForeground);
    }

    .project-board-widget .board-loading-message {
      color: var(--vscode-descriptionForeground);
      font-style: italic;
    }

    .board-empty-banner {
      margin-top: 12px;
      padding: 8px 12px;
      border-radius: 4px;
      font-size: 12px;
      line-height: 1.5;
    }

    .board-empty-banner code {
      background: var(--vscode-textCodeBlock-background, rgba(0, 0, 0, 0.2));
      padding: 1px 4px;
      border-radius: 3px;
      font-family: var(--vscode-editor-font-family, monospace);
      font-size: 11px;
    }

    .board-empty-warning {
      background: var(--vscode-inputValidation-warningBackground, rgba(255, 196, 64, 0.12));
      border: 1px solid var(--vscode-inputValidation-warningBorder, rgba(255, 196, 64, 0.4));
      color: var(--vscode-inputValidation-warningForeground, var(--vscode-foreground));
    }

    .board-empty-info {
      background: var(--vscode-inputValidation-infoBackground, rgba(100, 149, 237, 0.08));
      border: 1px solid var(--vscode-inputValidation-infoBorder, rgba(100, 149, 237, 0.3));
      color: var(--vscode-inputValidation-infoForeground, var(--vscode-foreground));
    }
  `;
}
