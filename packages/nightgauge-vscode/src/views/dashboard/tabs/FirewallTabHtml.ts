/**
 * FirewallTabHtml - Firewall dashboard tab renderer
 *
 * Extracted from DashboardHtml.ts as part of the tab-based modular refactor (#1542).
 */

// Import shared utilities from DashboardComponents
import { escapeHtml, formatTimestamp, formatRelativeTime } from "../DashboardComponents";
// Import firewall types
import type {
  SanitizationEvent,
  FirewallFilterState,
  FirewallAggregates,
  FirewallTimeSeriesPoint,
  AllowlistSuggestion,
} from "../FirewallTypes";
import { CATEGORY_LABELS } from "../FirewallTypes";
import type { SanitizationMode } from "../../../config/schema";

/**
 * Generate firewall mode status badge HTML
 */
function getFirewallModeBadgeHtml(mode: SanitizationMode): string {
  const modeConfig: Record<SanitizationMode, { label: string; cssClass: string }> = {
    warn: { label: "Warn-Only", cssClass: "firewall-mode-warn" },
    block: { label: "Block", cssClass: "firewall-mode-block" },
    disabled: { label: "Disabled", cssClass: "firewall-mode-disabled" },
  };
  const { label, cssClass } = modeConfig[mode] ?? modeConfig.warn;
  return `<span class="firewall-mode-badge ${cssClass}">Firewall: ${label}</span>`;
}

/**
 * Generate firewall-specific JavaScript for event handling
 */
function getFirewallScript(): string {
  return `
    // Firewall filter handlers + event delegation
    (function() {
      // Firewall event row click delegation — firewall section is rendered inside the history tab panel
      const firewallTab = document.getElementById('tab-panel-history');
      if (firewallTab) {
        firewallTab.addEventListener('click', function(e) {
          const target = e.target.closest('[data-action="toggle-firewall-details"]');
          if (!target) return;
          const index = target.dataset.index;
          const details = document.getElementById('firewall-details-' + index);
          if (details) {
            details.classList.toggle('expanded');
          }
        });
      }

      // Event type filter
      document.getElementById('firewallEventTypeFilter')?.addEventListener('change', (e) => {
        const value = e.target.value;
        vscode.postMessage({
          type: 'firewallFilter',
          filter: 'eventType',
          value: value ? [value] : []
        });
      });

      // Category filter
      document.getElementById('firewallCategoryFilter')?.addEventListener('change', (e) => {
        const value = e.target.value;
        vscode.postMessage({
          type: 'firewallFilter',
          filter: 'category',
          value: value ? [value] : []
        });
      });

      // Time range filter
      document.getElementById('firewallTimeRangeFilter')?.addEventListener('change', (e) => {
        vscode.postMessage({
          type: 'firewallFilter',
          filter: 'timeRange',
          value: e.target.value
        });
      });

      // Search filter (debounced)
      let searchTimeout = null;
      document.getElementById('firewallSearchFilter')?.addEventListener('input', (e) => {
        clearTimeout(searchTimeout);
        searchTimeout = setTimeout(() => {
          vscode.postMessage({
            type: 'firewallFilter',
            filter: 'search',
            value: e.target.value
          });
        }, 300);
      });

      // Reset filters
      document.getElementById('firewallResetFilters')?.addEventListener('click', () => {
        vscode.postMessage({ type: 'firewallResetFilters' });
      });

      // Suggestion "Add" buttons (Issue #786)
      document.querySelectorAll('.suggestion-add-btn').forEach((btn) => {
        btn.addEventListener('click', (e) => {
          const target = e.currentTarget;
          const pattern = target.getAttribute('data-pattern');
          const suggestionType = target.getAttribute('data-suggestion-type');
          if (pattern && suggestionType) {
            vscode.postMessage({
              type: 'firewallAddAllowlist',
              pattern: pattern,
              suggestionType: suggestionType
            });
          }
        });
      });

      // Suggestion "Dismiss" buttons (Issue #786)
      document.querySelectorAll('.suggestion-dismiss-btn').forEach((btn) => {
        btn.addEventListener('click', (e) => {
          const target = e.currentTarget;
          const pattern = target.getAttribute('data-pattern');
          if (pattern) {
            vscode.postMessage({
              type: 'firewallDismissSuggestion',
              pattern: pattern
            });
          }
        });
      });
    })();
  `;
}

/**
 * Generate firewall summary metrics HTML
 */
function getFirewallMetricsHtml(aggregates: FirewallAggregates, mode: SanitizationMode): string {
  const total = aggregates.totalBlocked + aggregates.totalWarned + aggregates.totalBypassed;
  const mostRecent = aggregates.mostRecentEvent
    ? formatRelativeTime(aggregates.mostRecentEvent)
    : "Never";
  const topCategory = aggregates.mostCommonCategory
    ? CATEGORY_LABELS[aggregates.mostCommonCategory]
    : "N/A";

  return `
    <div class="firewall-metrics-header">
      ${getFirewallModeBadgeHtml(mode)}
    </div>
    <div class="firewall-summary-cards">
      <div class="firewall-stat-card blocked">
        <div class="firewall-stat-value blocked">${aggregates.totalBlocked}</div>
        <div class="firewall-stat-label">Blocked</div>
      </div>
      <div class="firewall-stat-card warned">
        <div class="firewall-stat-value warned">${aggregates.totalWarned}</div>
        <div class="firewall-stat-label">Warned</div>
      </div>
      <div class="firewall-stat-card bypassed">
        <div class="firewall-stat-value bypassed">${aggregates.totalBypassed}</div>
        <div class="firewall-stat-label">Bypassed</div>
      </div>
      <div class="firewall-stat-card">
        <div class="firewall-stat-value">${total}</div>
        <div class="firewall-stat-label">Total Events</div>
      </div>
      <div class="firewall-stat-card">
        <div class="firewall-stat-value" style="font-size: 1.2em;">${mostRecent}</div>
        <div class="firewall-stat-label">Last Event</div>
      </div>
    </div>
  `;
}

/**
 * Generate firewall filter controls HTML
 */
function getFirewallFiltersHtml(filters: FirewallFilterState): string {
  const eventTypeOptions = [
    { value: "", label: "All Types" },
    { value: "blocked", label: "Blocked" },
    { value: "warned", label: "Warned" },
    { value: "bypassed", label: "Bypassed" },
  ];

  const categoryOptions = [
    { value: "", label: "All Categories" },
    { value: "destructive", label: "Destructive" },
    { value: "exfiltration", label: "Exfiltration" },
    { value: "privilege_escalation", label: "Privilege Escalation" },
    { value: "prompt_injection", label: "Prompt Injection" },
    { value: "path_traversal", label: "Path Traversal" },
    { value: "allowlist", label: "Allowlist" },
  ];

  const timeRangeOptions = [
    { value: "hour", label: "Last Hour" },
    { value: "24h", label: "Last 24 Hours" },
    { value: "7d", label: "Last 7 Days" },
    { value: "all", label: "All Time" },
  ];

  const eventTypeSelected = filters.eventTypes.length === 1 ? filters.eventTypes[0] : "";
  const categorySelected = filters.categories.length === 1 ? filters.categories[0] : "";

  return `
    <div class="firewall-filters">
      <div class="firewall-filter-group">
        <label class="firewall-filter-label">Type:</label>
        <select class="firewall-filter-select" id="firewallEventTypeFilter">
          ${eventTypeOptions
            .map(
              (opt) =>
                `<option value="${opt.value}" ${opt.value === eventTypeSelected ? "selected" : ""}>${opt.label}</option>`
            )
            .join("")}
        </select>
      </div>
      <div class="firewall-filter-group">
        <label class="firewall-filter-label">Category:</label>
        <select class="firewall-filter-select" id="firewallCategoryFilter">
          ${categoryOptions
            .map(
              (opt) =>
                `<option value="${opt.value}" ${opt.value === categorySelected ? "selected" : ""}>${opt.label}</option>`
            )
            .join("")}
        </select>
      </div>
      <div class="firewall-filter-group">
        <label class="firewall-filter-label">Time:</label>
        <select class="firewall-filter-select" id="firewallTimeRangeFilter">
          ${timeRangeOptions
            .map(
              (opt) =>
                `<option value="${opt.value}" ${opt.value === filters.timeRange ? "selected" : ""}>${opt.label}</option>`
            )
            .join("")}
        </select>
      </div>
      <div class="firewall-filter-group">
        <input
          type="text"
          class="firewall-filter-search"
          id="firewallSearchFilter"
          placeholder="Search events..."
          value="${escapeHtml(filters.searchText)}"
        />
      </div>
      <button class="action-btn" id="firewallResetFilters">Reset</button>
    </div>
  `;
}

/**
 * Generate firewall event table HTML
 */
function getFirewallEventTableHtml(events: SanitizationEvent[]): string {
  if (events.length === 0) {
    return getFirewallEmptyStateHtml();
  }

  // Sort events by timestamp descending (most recent first)
  const sortedEvents = [...events].sort((a, b) => b.timestamp.getTime() - a.timestamp.getTime());

  // Limit to most recent 100 events for performance
  const displayEvents = sortedEvents.slice(0, 100);

  const rows = displayEvents
    .map((event, index) => {
      const time = formatTimestamp(event.timestamp);
      const monthNames = [
        "Jan",
        "Feb",
        "Mar",
        "Apr",
        "May",
        "Jun",
        "Jul",
        "Aug",
        "Sep",
        "Oct",
        "Nov",
        "Dec",
      ];
      const date = `${monthNames[event.timestamp.getUTCMonth()]} ${event.timestamp.getUTCDate()}`;
      const categoryLabel = CATEGORY_LABELS[event.category] || event.category;

      return `
      <tr class="firewall-event-row" data-action="toggle-firewall-details" data-index="${index}">
        <td>
          <span class="firewall-timestamp">${date} ${time}</span>
        </td>
        <td>
          <span class="firewall-event-type ${event.event}">${event.event.toUpperCase()}</span>
        </td>
        <td>
          <span class="firewall-category-badge">${escapeHtml(categoryLabel)}</span>
        </td>
        <td>
          <span class="firewall-tool-badge">${escapeHtml(event.tool)}</span>
        </td>
        <td>
          <span class="firewall-content-preview" title="${escapeHtml(event.content)}">${escapeHtml(event.content.substring(0, 60))}${event.content.length > 60 ? "..." : ""}</span>
        </td>
      </tr>
      <tr>
        <td colspan="5">
          <div class="firewall-event-details" id="firewall-details-${index}">
            <pre>${escapeHtml(JSON.stringify({ pattern: event.pattern, content: event.content, context: event.context, branch: event.branch }, null, 2))}</pre>
          </div>
        </td>
      </tr>
    `;
    })
    .join("");

  return `
    <div class="firewall-table-container">
      <table class="firewall-event-table">
        <thead>
          <tr>
            <th>Time</th>
            <th>Type</th>
            <th>Category</th>
            <th>Tool</th>
            <th>Content</th>
          </tr>
        </thead>
        <tbody>
          ${rows}
        </tbody>
      </table>
    </div>
    ${events.length > 100 ? `<p class="firewall-pagination-note">Showing 100 of ${events.length} events</p>` : ""}
  `;
}

/**
 * Generate firewall empty state HTML
 */
function getFirewallEmptyStateHtml(): string {
  return `
    <div class="firewall-empty-state">
      <div class="empty-icon">🛡️</div>
      <h3>No Firewall Events</h3>
      <p>No sanitization events have been logged yet.</p>
      <p class="empty-hint">Events will appear here when the prompt injection firewall blocks or warns about potentially dangerous commands.</p>
    </div>
  `;
}

/**
 * Generate firewall summary section HTML (Chart.js charts removed)
 */
function getFirewallChartsHtml(
  _timeSeriesData: FirewallTimeSeriesPoint[],
  aggregates: FirewallAggregates,
  _nonce: string
): string {
  const categories = (
    [
      "destructive",
      "exfiltration",
      "privilege_escalation",
      "prompt_injection",
      "path_traversal",
      "allowlist",
    ] as const
  )
    .filter((cat) => (aggregates.categoryBreakdown[cat] ?? 0) > 0)
    .map((cat) => {
      const count = aggregates.categoryBreakdown[cat] ?? 0;
      const label = cat
        .split("_")
        .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
        .join(" ");
      return `<li><strong>${count}</strong> ${label}</li>`;
    })
    .join("");

  return categories
    ? `<div class="firewall-summary-section"><h4>Category Breakdown</h4><ul class="firewall-category-list">${categories}</ul></div>`
    : "";
}

/**
 * Generate suggested allowlist entries HTML (Issue #786)
 */
function getFirewallSuggestionsHtml(suggestions: AllowlistSuggestion[] | undefined): string {
  if (!suggestions || suggestions.length === 0) {
    return `
      <div class="firewall-suggestions-section">
        <h4>Suggested Allowlist Entries</h4>
        <p class="empty-state-text">No suggestions — all blocked events are either already allowlisted or system-level.</p>
      </div>`;
  }

  const cards = suggestions
    .map(
      (s, i) => `
      <div class="suggestion-card" data-index="${i}">
        <div class="suggestion-header">
          <span class="suggestion-type-badge suggestion-type-${s.type}">${s.type === "safe_directory" ? "Safe Dir" : "Allowlist"}</span>
          <span class="suggestion-freq-badge">${s.frequency} event${s.frequency !== 1 ? "s" : ""}</span>
          <span class="suggestion-last-seen">Last: ${formatRelativeTime(s.lastOccurrence)}</span>
        </div>
        <code class="suggestion-pattern">${escapeHtml(s.pattern)}</code>
        <p class="suggestion-description">${escapeHtml(s.description)}</p>
        <p class="suggestion-example"><em>Example:</em> ${escapeHtml(s.exampleContent.substring(0, 120))}${s.exampleContent.length > 120 ? "..." : ""}</p>
        <div class="suggestion-actions">
          <button class="btn btn-primary btn-sm suggestion-add-btn"
            data-pattern="${escapeHtml(s.pattern)}"
            data-suggestion-type="${s.type}">Add to ${s.type === "safe_directory" ? "Safe Dirs" : "Allowlist"}</button>
          <button class="btn btn-ghost btn-sm suggestion-dismiss-btn"
            data-pattern="${escapeHtml(s.pattern)}">Dismiss</button>
        </div>
      </div>`
    )
    .join("");

  return `
    <div class="firewall-suggestions-section">
      <h4>Suggested Allowlist Entries <span class="badge">${suggestions.length}</span></h4>
      <div class="suggestion-cards">${cards}</div>
    </div>`;
}

/**
 * Generate complete firewall section HTML
 */
function getFirewallSectionHtml(
  events: SanitizationEvent[],
  filters: FirewallFilterState,
  aggregates: FirewallAggregates,
  timeSeriesData: FirewallTimeSeriesPoint[],
  nonce: string,
  suggestions?: AllowlistSuggestion[],
  mode: SanitizationMode = "warn"
): string {
  return `
    <details class="collapsible-section">
      <summary class="section-toggle">
        <span class="toggle-icon">▼</span>
        <h3>🛡️ Prompt Injection Firewall</h3>
        ${aggregates.totalBlocked > 0 ? `<span class="badge badge-warning">${aggregates.totalBlocked} blocked</span>` : ""}
      </summary>
      <div class="section-content">
        <div class="firewall-section">
          ${getFirewallMetricsHtml(aggregates, mode)}
          ${getFirewallFiltersHtml(filters)}
          ${getFirewallChartsHtml(timeSeriesData, aggregates, nonce)}
          ${getFirewallEventTableHtml(events)}
          ${getFirewallSuggestionsHtml(suggestions)}
        </div>
      </div>
    </details>
  `;
}

/**
 * Firewall tab CSS styles extracted from getStyles()
 */
export function getFirewallTabStyles(): string {
  return `
    /* Firewall Dashboard Styles (Issue #387) */
    .firewall-metrics-header {
      margin-bottom: var(--spacing-md);
    }

    .firewall-mode-badge {
      display: inline-block;
      padding: 4px 12px;
      border-radius: var(--border-radius);
      font-size: 0.85em;
      font-weight: 600;
    }

    .firewall-mode-warn {
      background: rgba(255, 206, 86, 0.2);
      color: rgba(255, 206, 86, 1);
    }

    .firewall-mode-block {
      background: rgba(255, 99, 132, 0.2);
      color: rgba(255, 99, 132, 1);
    }

    .firewall-mode-disabled {
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
    }

    .firewall-section {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }

    .firewall-summary-cards {
      display: grid;
      grid-template-columns: repeat(5, 1fr);
      gap: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }

    @media (max-width: 900px) {
      .firewall-summary-cards {
        grid-template-columns: repeat(3, 1fr);
      }
    }

    @media (max-width: 600px) {
      .firewall-summary-cards {
        grid-template-columns: repeat(2, 1fr);
      }
    }

    .firewall-stat-card {
      background: var(--vscode-editor-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
      text-align: center;
    }

    .firewall-stat-card.blocked {
      border-color: rgba(255, 99, 132, 0.5);
    }

    .firewall-stat-card.warned {
      border-color: rgba(255, 206, 86, 0.5);
    }

    .firewall-stat-card.bypassed {
      border-color: rgba(75, 192, 75, 0.5);
    }

    .firewall-stat-value {
      font-size: 1.8em;
      font-weight: 600;
    }

    .firewall-stat-value.blocked {
      color: rgba(255, 99, 132, 1);
    }

    .firewall-stat-value.warned {
      color: rgba(255, 206, 86, 1);
    }

    .firewall-stat-value.bypassed {
      color: rgba(75, 192, 75, 1);
    }

    .firewall-stat-label {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
      margin-top: var(--spacing-xs);
    }

    .firewall-filters {
      display: flex;
      gap: var(--spacing-md);
      flex-wrap: wrap;
      align-items: center;
      margin-bottom: var(--spacing-md);
      padding: var(--spacing-md);
      background: var(--vscode-editor-background);
      border-radius: var(--border-radius);
    }

    .firewall-filter-group {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
    }

    .firewall-filter-label {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }

    .firewall-filter-select {
      padding: var(--spacing-xs) var(--spacing-sm);
      background: var(--vscode-dropdown-background);
      color: var(--vscode-dropdown-foreground);
      border: 1px solid var(--vscode-dropdown-border);
      border-radius: var(--border-radius);
      font-size: 0.85em;
    }

    .firewall-filter-search {
      padding: var(--spacing-xs) var(--spacing-sm);
      background: var(--vscode-input-background);
      color: var(--vscode-input-foreground);
      border: 1px solid var(--vscode-input-border);
      border-radius: var(--border-radius);
      font-size: 0.85em;
      min-width: 150px;
    }

    .firewall-charts {
      display: grid;
      grid-template-columns: 2fr 1fr;
      gap: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }

    @media (max-width: 800px) {
      .firewall-charts {
        grid-template-columns: 1fr;
      }
    }

    .firewall-event-table {
      width: 100%;
      border-collapse: collapse;
      font-size: 0.9em;
    }

    .firewall-event-table th {
      text-align: left;
      padding: var(--spacing-sm);
      border-bottom: 2px solid var(--vscode-panel-border);
      color: var(--vscode-foreground);
      font-weight: 600;
    }

    .firewall-event-table td {
      padding: var(--spacing-sm);
      border-bottom: 1px solid var(--vscode-panel-border);
      vertical-align: top;
    }

    .firewall-event-table tr:hover {
      background: var(--vscode-list-hoverBackground);
    }

    .firewall-event-type {
      display: inline-block;
      padding: 2px 8px;
      border-radius: var(--border-radius);
      font-size: 0.8em;
      font-weight: 600;
    }

    .firewall-event-type.blocked {
      background: rgba(255, 99, 132, 0.2);
      color: rgba(255, 99, 132, 1);
    }

    .firewall-event-type.warned {
      background: rgba(255, 206, 86, 0.2);
      color: rgba(255, 206, 86, 1);
    }

    .firewall-event-type.bypassed {
      background: rgba(75, 192, 75, 0.2);
      color: rgba(75, 192, 75, 1);
    }

    .firewall-category-badge {
      display: inline-block;
      padding: 2px 8px;
      border-radius: var(--border-radius);
      font-size: 0.75em;
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
    }

    .firewall-content-preview {
      max-width: 300px;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font-family: var(--vscode-editor-font-family);
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }

    .firewall-event-row {
      cursor: pointer;
    }

    .firewall-event-details {
      display: none;
      padding: var(--spacing-md);
      background: var(--vscode-textCodeBlock-background);
      border-radius: var(--border-radius);
    }

    .firewall-event-details.expanded {
      display: block;
    }

    .firewall-event-details pre {
      font-family: var(--vscode-editor-font-family);
      font-size: 0.85em;
      white-space: pre-wrap;
      word-break: break-all;
      margin: 0;
    }

    .firewall-empty-state {
      text-align: center;
      padding: var(--spacing-lg) * 2;
      color: var(--vscode-descriptionForeground);
    }

    .firewall-empty-state .empty-icon {
      font-size: 3em;
      margin-bottom: var(--spacing-md);
    }

    .firewall-timestamp {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
      white-space: nowrap;
    }

    .firewall-tool-badge {
      display: inline-block;
      padding: 1px 6px;
      border-radius: var(--border-radius);
      font-size: 0.75em;
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
    }

    .firewall-table-container {
      min-height: 200px;
      overflow-y: auto;
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
    }

    /* Allowlist Suggestions (Issue #786) */
    .firewall-suggestions-section {
      margin-top: var(--spacing-md);
      padding-top: var(--spacing-md);
      border-top: 1px solid var(--vscode-panel-border);
    }
    .firewall-suggestions-section h4 {
      font-size: 1em;
      font-weight: 600;
      margin: 0 0 var(--spacing-md) 0;
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
    }
    .suggestion-cards {
      display: flex;
      flex-direction: column;
      gap: var(--spacing-sm);
    }
    .suggestion-card {
      background: var(--vscode-editor-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
    }
    .suggestion-header {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      margin-bottom: var(--spacing-xs);
    }
    .suggestion-type-badge {
      display: inline-block;
      padding: 2px 8px;
      border-radius: var(--border-radius);
      font-size: 0.75em;
      font-weight: 600;
    }
    .suggestion-type-allowlist {
      background: rgba(54, 162, 235, 0.2);
      color: rgba(54, 162, 235, 1);
    }
    .suggestion-type-safe_directory {
      background: rgba(75, 192, 75, 0.2);
      color: rgba(75, 192, 75, 1);
    }
    .suggestion-freq-badge {
      display: inline-block;
      padding: 2px 8px;
      border-radius: var(--border-radius);
      font-size: 0.75em;
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
    }
    .suggestion-last-seen {
      font-size: 0.75em;
      color: var(--vscode-descriptionForeground);
      margin-left: auto;
    }
    .suggestion-pattern {
      display: block;
      font-family: var(--vscode-editor-font-family);
      font-size: 0.9em;
      padding: var(--spacing-xs) var(--spacing-sm);
      background: var(--vscode-textCodeBlock-background);
      border-radius: var(--border-radius);
      margin: var(--spacing-xs) 0;
      word-break: break-all;
    }
    .suggestion-description {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
      margin: var(--spacing-xs) 0;
    }
    .suggestion-example {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
      margin: var(--spacing-xs) 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .suggestion-actions {
      display: flex;
      gap: var(--spacing-sm);
      margin-top: var(--spacing-sm);
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

// Export getFirewallScript for use in DashboardHtml.ts main script block
export { getFirewallScript };

// Export getFirewallSectionHtml for use in DashboardHtml.ts
export { getFirewallSectionHtml };
