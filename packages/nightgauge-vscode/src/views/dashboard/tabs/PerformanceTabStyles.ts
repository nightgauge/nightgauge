/**
 * PerformanceTabStyles - CSS styles for the performance metrics tab
 *
 * Extracted from PerformanceTabHtml.ts to keep each file under 1000 lines (#1542).
 */

/**
 * Performance tab CSS — time-saved panel, cache gauge, efficiency panel,
 * cost breakdown, PTC metrics, and stage comparison styles.
 */
export function getPerformanceTabStyles(): string {
  return `
    /* Time Saved Panel */
    .time-saved-panel {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }

    .multiplier-badge {
      background: var(--vscode-charts-green);
      color: white;
      padding: 2px 8px;
      border-radius: var(--border-radius);
      font-size: 0.85em;
      font-weight: 500;
    }

    .time-comparison {
      margin: var(--spacing-md) 0;
    }

    .time-row {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      margin-bottom: var(--spacing-sm);
    }

    .time-label {
      width: 80px;
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }

    .time-bar-wrapper {
      flex: 1;
      height: 24px;
      background: var(--vscode-progressBar-background);
      border-radius: var(--border-radius);
      overflow: hidden;
    }

    .time-bar {
      height: 100%;
      border-radius: var(--border-radius);
      transition: width 0.5s ease;
    }

    .time-bar.ai {
      background: var(--vscode-charts-green);
    }

    .time-bar.manual {
      background: var(--vscode-charts-orange);
    }

    .time-value {
      width: 60px;
      text-align: right;
      font-size: 0.9em;
      font-weight: 500;
    }

    .time-saved-summary {
      padding-top: var(--spacing-sm);
      border-top: 1px solid var(--vscode-panel-border);
      color: var(--vscode-foreground);
    }

    /* Cache Gauge */
    .cache-gauge {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
      text-align: center;
    }

    .gauge-title {
      font-size: 0.95em;
      font-weight: 500;
      margin-bottom: var(--spacing-sm);
    }

    .gauge-container {
      position: relative;
      width: 100px;
      height: 60px;
      margin: 0 auto;
    }

    .gauge-svg {
      width: 100%;
      height: 100%;
    }

    --gauge-color-high: var(--vscode-charts-green);
    --gauge-color-medium: var(--vscode-charts-yellow);
    --gauge-color-low: var(--vscode-charts-red);

    .gauge-fill {
      transition: stroke-dasharray 0.5s ease;
    }

    .gauge-value {
      position: absolute;
      bottom: 0;
      left: 50%;
      transform: translateX(-50%);
      font-size: 1.2em;
      font-weight: 600;
    }

    .gauge-label {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
      margin-top: var(--spacing-xs);
    }

    /* Efficiency Panel */
    .efficiency-panel {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
    }

    .efficiency-grid {
      display: grid;
      grid-template-columns: repeat(3, 1fr);
      gap: var(--spacing-md);
      text-align: center;
    }

    .efficiency-item {
      padding: var(--spacing-sm);
    }

    .efficiency-value {
      font-size: 1.1em;
      font-weight: 600;
      color: var(--vscode-foreground);
    }

    .efficiency-label {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
    }

    /* Cost Breakdown */
    .cost-breakdown {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
      margin-bottom: var(--spacing-md);
      display: flex;
      flex-direction: column;
      min-height: 100px;
    }

    .cost-bars {
      margin-top: var(--spacing-sm);
      overflow-y: auto;
      flex: 1;
      min-height: 0;
    }

    .cost-row {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      margin-bottom: var(--spacing-xs);
    }

    .cost-stage {
      width: 100px;
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }

    .cost-bar-wrapper {
      flex: 1;
      height: 16px;
      background: var(--vscode-progressBar-background);
      border-radius: var(--border-radius);
      overflow: hidden;
    }

    .cost-bar {
      height: 100%;
      background: var(--vscode-charts-purple);
      border-radius: var(--border-radius);
      transition: width 0.3s ease;
    }

    .cost-value {
      width: 60px;
      text-align: right;
      font-size: 0.85em;
      color: var(--vscode-foreground);
    }

    /* PTC Metrics Section */
    .ptc-metrics-section {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      margin-bottom: var(--spacing-md);
      overflow: hidden;
    }

    .ptc-metrics-section > summary {
      padding: var(--spacing-md);
      cursor: pointer;
      list-style: none;
    }

    .ptc-metrics-section > summary::-webkit-details-marker {
      display: none;
    }

    /* Stage efficiency summary (Issue #1008) */
    .stage-efficiency-container {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }
    .stage-efficiency-table {
      width: 100%;
      border-collapse: collapse;
      font-size: 0.85em;
    }
    .stage-efficiency-table th,
    .stage-efficiency-table td {
      padding: 5px 8px;
      text-align: left;
      border-bottom: 1px solid var(--vscode-panel-border);
    }
    .stage-efficiency-table th {
      color: var(--vscode-descriptionForeground);
      font-weight: 500;
      font-size: 0.9em;
    }
    .stage-efficiency-table td.num {
      text-align: right;
      font-variant-numeric: tabular-nums;
    }

    /* Stage comparison mini-bars (Issue #1008) */
    .stage-comparison-container {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }
    .stage-comparison-run {
      display: flex;
      align-items: center;
      margin-bottom: 4px;
      gap: 8px;
    }
    .stage-comparison-label {
      min-width: 48px;
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
      text-align: right;
    }
    .stage-comparison-bar-track {
      flex: 1;
      display: flex;
      height: 16px;
      background: var(--vscode-input-background);
      border-radius: 3px;
      overflow: hidden;
    }
    .stage-comparison-segment {
      height: 100%;
      min-width: 1px;
    }
    .stage-comparison-cost {
      min-width: 60px;
      font-size: 0.8em;
      text-align: right;
      font-variant-numeric: tabular-nums;
    }
    .stage-comparison-legend {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      margin-top: 8px;
      font-size: 0.8em;
    }
    .stage-comparison-legend-item {
      display: flex;
      align-items: center;
      gap: 4px;
    }
    .stage-comparison-legend-dot {
      display: inline-block;
      width: 10px;
      height: 10px;
      border-radius: 2px;
    }

    /* Cost by Size Widget */
    .cost-by-size-widget {
      margin-top: var(--spacing-md);
    }

    .cost-by-size-bars {
      margin-top: var(--spacing-md);
    }

    .cost-by-size-bars h4 {
      margin-bottom: var(--spacing-sm);
      color: var(--vscode-foreground);
      font-size: 0.9em;
    }

    .cost-by-size-bar-row {
      display: grid;
      grid-template-columns: 80px 1fr 90px;
      align-items: center;
      gap: var(--spacing-sm);
      margin-bottom: var(--spacing-xs);
    }

    .cost-by-size-bar-label {
      font-size: 0.85em;
      font-weight: 600;
    }

    .cost-by-size-bar-value {
      font-size: 0.85em;
      text-align: right;
      color: var(--vscode-descriptionForeground);
    }

    .cost-by-size-bar-track {
      height: 12px;
      background: rgba(128, 128, 128, 0.15);
      border-radius: 4px;
      overflow: hidden;
      position: relative;
    }

    .cost-by-size-bar-fill {
      height: 100%;
      border-radius: 4px;
      position: absolute;
      top: 0;
      left: 0;
    }

    .cost-by-size-fill-low {
      background: var(--vscode-terminal-ansiGreen);
    }

    .cost-by-size-fill-mid {
      background: var(--vscode-terminal-ansiYellow);
    }

    .cost-by-size-fill-high {
      background: var(--vscode-terminal-ansiRed);
    }

    .cost-by-size-fill-neutral {
      background: var(--vscode-descriptionForeground);
      opacity: 0.5;
    }
  `;
}

/**
 * Generate CSS for the token usage table (replaces chart CSS)
 */
export function getTokenTableStyles(): string {
  return `
    .token-table-container {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }
    .token-usage-table {
      width: 100%;
      border-collapse: collapse;
      font-size: 0.85em;
    }
    .token-usage-table th,
    .token-usage-table td {
      padding: 6px 8px;
      text-align: left;
      border-bottom: 1px solid var(--vscode-panel-border);
    }
    .token-usage-table th {
      color: var(--vscode-descriptionForeground);
      font-weight: 500;
      font-size: 0.9em;
    }
    .stage-name-cell {
      white-space: nowrap;
      max-width: 140px;
    }
    .token-bar-cell {
      width: 40%;
    }
    .token-bar-track {
      display: flex;
      height: 14px;
      background: var(--vscode-input-background);
      border-radius: 3px;
      overflow: hidden;
    }
    .token-bar-input {
      background: rgba(75, 192, 192, 0.8);
      height: 100%;
    }
    .token-bar-output {
      background: rgba(255, 159, 64, 0.8);
      height: 100%;
    }
    .token-num-cell, .token-pct-cell, .token-cost-cell {
      text-align: right;
      white-space: nowrap;
      font-variant-numeric: tabular-nums;
    }
    .token-duration-cell, .token-cache-cell, .token-model-cell {
      text-align: right;
      white-space: nowrap;
      font-variant-numeric: tabular-nums;
      font-size: 0.85em;
    }
    .token-model-cell {
      color: var(--vscode-descriptionForeground);
    }
    .total-row td {
      border-top: 2px solid var(--vscode-panel-border);
      border-bottom: none;
    }
    .legend-inline {
      display: inline-flex;
      align-items: center;
      gap: 4px;
      font-size: 0.85em;
    }
    .legend-dot {
      display: inline-block;
      width: 10px;
      height: 10px;
      border-radius: 2px;
    }
    .legend-dot.legend-input { background: rgba(75, 192, 192, 0.8); }
    .legend-dot.legend-output { background: rgba(255, 159, 64, 0.8); }
    .empty-state-text {
      color: var(--vscode-descriptionForeground);
      text-align: center;
      padding: var(--spacing-md);
    }
    /* Outlier highlighting (Issue #1008) */
    tr.outlier {
      border-left: 3px solid var(--vscode-charts-yellow, #e5c07b);
    }
    tr.outlier td:first-child {
      padding-left: 5px;
    }
  `;
}
