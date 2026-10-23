/**
 * DiscoveryTabStyles - CSS styles for the Discovery Activity dashboard tab.
 *
 * @see Issue #2434 — activate autonomous self-improvement loop
 */

export function getDiscoveryTabStyles(): string {
  return `
    /* Discovery Activity Tab (#2434) */

    .discovery-tab {
      padding: 0;
    }

    /* Summary cards row */
    .discovery-summary-cards {
      display: flex;
      flex-wrap: wrap;
      gap: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }

    .discovery-summary-card {
      background: var(--vscode-editor-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
      text-align: center;
      min-width: 100px;
      flex: 1;
    }

    .discovery-summary-card.wide {
      text-align: left;
      min-width: 160px;
    }

    .discovery-summary-card .card-value {
      font-size: 2em;
      font-weight: bold;
      color: var(--vscode-foreground);
    }

    .discovery-summary-card .card-label {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
      margin-top: var(--spacing-xs);
    }

    .discovery-summary-card .card-timestamp {
      font-size: 0.9em;
      color: var(--vscode-foreground);
      margin-top: var(--spacing-xs);
    }

    /* Run card */
    .discovery-run-card {
      padding: var(--spacing-sm) 0;
    }

    .discovery-run-meta {
      display: flex;
      flex-wrap: wrap;
      gap: var(--spacing-md);
      margin-bottom: var(--spacing-md);
      font-size: 0.9em;
    }

    .discovery-meta-item {
      color: var(--vscode-descriptionForeground);
    }

    .discovery-meta-item strong {
      color: var(--vscode-foreground);
    }

    /* Count chips */
    .discovery-counts-row {
      display: flex;
      gap: var(--spacing-sm);
      margin-bottom: var(--spacing-md);
      flex-wrap: wrap;
    }

    .discovery-count-chip {
      display: inline-block;
      padding: 3px 10px;
      border-radius: 12px;
      font-size: 0.82em;
      font-weight: 600;
    }

    .discovery-count-chip.created {
      background: color-mix(in srgb, var(--vscode-terminal-ansiGreen) 20%, transparent);
      color: var(--vscode-terminal-ansiGreen);
      border: 1px solid var(--vscode-terminal-ansiGreen);
    }

    .discovery-count-chip.backlogged {
      background: color-mix(in srgb, var(--vscode-editorWarning-foreground) 15%, transparent);
      color: var(--vscode-editorWarning-foreground);
      border: 1px solid var(--vscode-editorWarning-foreground);
    }

    .discovery-count-chip.deduped {
      background: color-mix(in srgb, var(--vscode-descriptionForeground) 15%, transparent);
      color: var(--vscode-descriptionForeground);
      border: 1px solid var(--vscode-descriptionForeground);
    }

    /* Issue list */
    .discovery-issues-list {
      list-style: none;
      padding: 0;
      margin: 0 0 var(--spacing-md) 0;
    }

    .discovery-issues-list li {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      padding: var(--spacing-xs) 0;
      border-bottom: 1px solid var(--vscode-panel-border);
      font-size: 0.9em;
    }

    .discovery-issues-list li:last-child {
      border-bottom: none;
    }

    .discovery-issue-link {
      color: var(--vscode-textLink-foreground);
      text-decoration: none;
      flex: 1;
    }

    .discovery-issue-link:hover {
      text-decoration: underline;
    }

    /* Score badge */
    .discovery-score-badge {
      display: inline-block;
      padding: 2px 8px;
      border-radius: 10px;
      font-size: 0.78em;
      font-weight: bold;
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
      min-width: 32px;
      text-align: center;
    }

    /* Status badges */
    .discovery-badge {
      display: inline-block;
      padding: 2px 8px;
      border-radius: 10px;
      font-size: 0.78em;
      font-weight: 600;
      margin-left: var(--spacing-xs);
    }

    .badge-success {
      background: color-mix(in srgb, var(--vscode-terminal-ansiGreen) 20%, transparent);
      color: var(--vscode-terminal-ansiGreen);
    }

    .badge-info {
      background: color-mix(in srgb, var(--vscode-charts-blue) 20%, transparent);
      color: var(--vscode-charts-blue);
    }

    .badge-danger {
      background: color-mix(in srgb, var(--vscode-editorError-foreground) 20%, transparent);
      color: var(--vscode-editorError-foreground);
    }

    /* Error banner */
    .discovery-error-banner {
      background: color-mix(in srgb, var(--vscode-editorError-foreground) 12%, transparent);
      border: 1px solid var(--vscode-editorError-foreground);
      border-radius: var(--border-radius);
      padding: var(--spacing-sm) var(--spacing-md);
      font-size: 0.85em;
      color: var(--vscode-editorError-foreground);
      margin-top: var(--spacing-sm);
    }

    /* Backlog table */
    .discovery-backlog-table {
      width: 100%;
      border-collapse: collapse;
      font-size: 0.88em;
    }

    .discovery-backlog-table th,
    .discovery-backlog-table td {
      padding: var(--spacing-sm);
      text-align: left;
      border-bottom: 1px solid var(--vscode-panel-border);
    }

    .discovery-backlog-table th {
      color: var(--vscode-descriptionForeground);
      font-weight: 600;
    }

    .discovery-backlog-hint,
    .discovery-backlog-hint code {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
      margin-bottom: var(--spacing-md);
    }

    .discovery-truncation-note {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
      margin-top: var(--spacing-sm);
    }

    /* Configuration card */
    .discovery-config-card {
      font-size: 0.9em;
    }

    .discovery-config-example {
      background: var(--vscode-textCodeBlock-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
      font-family: var(--vscode-editor-font-family, monospace);
      font-size: 0.85em;
      white-space: pre;
      overflow-x: auto;
      margin: var(--spacing-sm) 0;
    }

    .discovery-config-hint {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }

    .discovery-config-hint code {
      background: var(--vscode-textCodeBlock-background);
      padding: 1px 4px;
      border-radius: 3px;
    }

    /* Empty states */
    .discovery-empty-state {
      text-align: center;
      padding: var(--spacing-lg) var(--spacing-md);
      color: var(--vscode-descriptionForeground);
    }

    .discovery-empty-icon {
      font-size: 2.5em;
      margin-bottom: var(--spacing-sm);
    }

    .discovery-empty-state h3 {
      color: var(--vscode-foreground);
      margin-bottom: var(--spacing-sm);
    }

    .discovery-empty-state code {
      background: var(--vscode-textCodeBlock-background);
      padding: 1px 4px;
      border-radius: 3px;
    }

    .discovery-empty-text {
      font-size: 0.88em;
      color: var(--vscode-descriptionForeground);
      font-style: italic;
    }
  `;
}
