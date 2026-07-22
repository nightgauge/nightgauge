/**
 * EpicsTabHtml - Epics & project board tab renderer
 *
 * Extracted from DashboardHtml.ts as part of the tab-based modular refactor (#1542).
 */

import { escapeHtml, formatTimestamp, formatRelativeTime } from "../DashboardComponents";
import type { DashboardAggregates } from "../DashboardState";
import type { CrossRepoEpicProgress, RepositoryProgress } from "../EpicDashboard";
import type { ProjectBoardData } from "../ProjectBoardTypes";
import type { SubIssueEstimate } from "@nightgauge/sdk";

/**
 * Generate epic estimates section HTML
 *
 * Handles three states:
 * 1. No epics found at all -> "No open epics found"
 * 2. Epics found but all failed estimation -> shows epics with warnings
 * 3. Mix of estimated and failed -> estimated epics as cards, failed in muted section
 *
 * @see Issue #987 - Epic Detection Fails Silently
 */
function getEpicEstimatesHtml(aggregates: DashboardAggregates): string {
  const entries = aggregates.epicEstimates;

  if (entries.length === 0) {
    return `
      <div class="epic-estimates-section empty-state">
        <div class="section-header">
          <h3>📊 Epic Estimates</h3>
        </div>
        <p class="empty-message">No open epics found. Create issues with the <code>type:epic</code> label to track feature progress.</p>
      </div>
    `;
  }

  const estimatedEntries = entries.filter((entry) => entry.estimate !== null);
  const failedEntries = entries.filter((entry) => entry.estimate === null);

  // Build cards for successfully estimated epics
  const epicCards = estimatedEntries
    .map((entry) => {
      const epic = entry.estimate!;
      const hours = Math.round(epic.total_remaining_minutes / 60);
      const days = (epic.total_remaining_minutes / (8 * 60)).toFixed(1);
      const progressPercent =
        epic.total_estimated_minutes > 0
          ? Math.round(
              ((epic.total_estimated_minutes - epic.total_remaining_minutes) /
                epic.total_estimated_minutes) *
                100
            )
          : 0;

      const confidenceBadge =
        epic.confidence === "high"
          ? '<span class="confidence-badge confidence-high" title="Based on strong historical data">High</span>'
          : epic.confidence === "medium"
            ? '<span class="confidence-badge confidence-medium" title="Based on moderate historical data">Medium</span>'
            : '<span class="confidence-badge confidence-low" title="Limited historical data">Low</span>';

      const subIssues = epic.sub_issues ?? [];
      const completedCount = subIssues.filter(
        (issue: SubIssueEstimate) => issue.status === "closed"
      ).length;
      const totalCount = subIssues.length;

      // Warning for low confidence (sub-issues missing size labels)
      const sizeLabelWarning =
        epic.confidence === "low" && epic.confidence_detail
          ? `<div class="epic-warning"><span class="warning-icon">&#9888;</span> ${escapeHtml(epic.confidence_detail)}</div>`
          : "";

      return `
        <div class="epic-card">
          <div class="epic-header">
            <div class="epic-title">
              <a href="https://github.com/${escapeHtml(epic.epic_number.toString())}" title="View Epic #${epic.epic_number}">
                <strong>Epic #${epic.epic_number}</strong>
              </a>
              <span class="epic-title-text">${escapeHtml(epic.epic_title)}</span>
            </div>
            <div class="epic-badges">
              ${confidenceBadge}
              <span class="epic-progress-badge">${completedCount}/${totalCount} issues</span>
            </div>
          </div>
          ${sizeLabelWarning}
          <div class="epic-metrics">
            <div class="epic-metric">
              <div class="epic-metric-value">${hours}h</div>
              <div class="epic-metric-label">Remaining (~${days} days)</div>
            </div>
            <div class="epic-metric">
              <div class="epic-metric-value">${progressPercent}%</div>
              <div class="epic-metric-label">Complete</div>
            </div>
          </div>
          <div class="progress-bar-container">
            <div class="progress-bar" style="width: ${progressPercent}%;"></div>
          </div>
          <details class="epic-details">
            <summary>View Sub-Issues (${totalCount})</summary>
            <ul class="sub-issues-list">
              ${subIssues
                .map((issue: SubIssueEstimate) => {
                  const statusIcon = issue.status === "closed" ? "✓" : " ";
                  const sizeStr = issue.size || "?";
                  const minutes = issue.estimated_minutes;
                  const issueHours = Math.round(minutes / 60);
                  return `<li class="sub-issue ${issue.status}">
                    <span class="sub-issue-status">[${statusIcon}]</span>
                    <span class="sub-issue-id">#${issue.number}</span>
                    <span class="sub-issue-size">(${sizeStr})</span>
                    <span class="sub-issue-title">${escapeHtml(issue.title)}</span>
                    <span class="sub-issue-estimate">${issueHours}h</span>
                  </li>`;
                })
                .join("")}
            </ul>
            <div class="epic-footer">
              <div class="epic-footer-item">
                <strong>Integration Buffer:</strong> ${Math.round(epic.integration_buffer_minutes / 60)}h (15%)
              </div>
              <div class="epic-footer-item">
                <strong>Confidence:</strong> ${escapeHtml(epic.confidence_detail)}
              </div>
            </div>
          </details>
        </div>
      `;
    })
    .join("");

  // Build cards for epics that failed estimation
  const failedEpicCards =
    failedEntries.length > 0
      ? `
      <div class="epic-failed-section">
        <div class="epic-failed-header">
          <span class="epic-failed-icon">&#9432;</span>
          <span>${failedEntries.length} epic${failedEntries.length !== 1 ? "s" : ""} found but cannot be estimated</span>
        </div>
        ${failedEntries
          .map(
            (entry) => `
          <div class="epic-card epic-card-failed">
            <div class="epic-header">
              <div class="epic-title">
                <strong>Epic #${entry.epic_number}</strong>
                <span class="epic-title-text">${escapeHtml(entry.epic_title)}</span>
              </div>
            </div>
            <div class="epic-warning">
              <span class="warning-icon">&#9432;</span>
              ${escapeHtml(entry.warning || "Unable to estimate this epic.")}
            </div>
          </div>
        `
          )
          .join("")}
      </div>
    `
      : "";

  const totalCount = entries.length;
  const estimatedCount = estimatedEntries.length;
  const countLabel =
    estimatedCount === totalCount
      ? `${totalCount} open epic${totalCount !== 1 ? "s" : ""}`
      : `${estimatedCount} of ${totalCount} epic${totalCount !== 1 ? "s" : ""} estimated`;

  return `
    <div class="epic-estimates-section">
      <div class="section-header">
        <h3>📊 Epic Estimates</h3>
        <span class="epic-count">${countLabel}</span>
      </div>
      <div class="epic-cards-container">
        ${epicCards}
      </div>
      ${failedEpicCards}
    </div>
  `;
}

/**
 * Generate cross-repo epic progress section HTML
 *
 * Displays epic progress grouped by repository for multi-repo workspaces.
 * @see Issue #330 - Epic Dashboard with Cross-Repo Progress
 */
function getCrossRepoEpicProgressHtml(aggregates: DashboardAggregates): string {
  const crossRepoProgress = aggregates.crossRepoEpicProgress;

  if (!crossRepoProgress || crossRepoProgress.length === 0) {
    return ""; // Don't show section if no cross-repo data
  }

  // Filter to only show epics that span multiple repos
  const crossRepoEpics = crossRepoProgress.filter((epic) => epic.isCrossRepo);

  if (crossRepoEpics.length === 0) {
    return ""; // Don't show if no cross-repo epics
  }

  const epicCards = crossRepoEpics
    .map((epic) => {
      const hours = Math.round(epic.remainingMinutes / 60);
      const days = (epic.remainingMinutes / (8 * 60)).toFixed(1);

      const confidenceBadge =
        epic.confidence === "high"
          ? '<span class="confidence-badge confidence-high">High</span>'
          : epic.confidence === "medium"
            ? '<span class="confidence-badge confidence-medium">Medium</span>'
            : '<span class="confidence-badge confidence-low">Low</span>';

      // Count total issues across all repos
      const totalClosed = epic.repositories.reduce((sum, r) => sum + r.closedCount, 0);
      const totalOpen = epic.repositories.reduce((sum, r) => sum + r.openCount, 0);
      const totalIssues = totalClosed + totalOpen;

      // Render repository progress sections
      const repoSections = epic.repositories
        .filter((repo) => repo.subIssues.length > 0)
        .map((repo) => getRepositoryProgressHtml(repo))
        .join("");

      return `
        <div class="epic-card cross-repo-epic">
          <div class="epic-header">
            <div class="epic-title">
              <strong>Epic #${epic.epicNumber}</strong>
              <span class="cross-repo-badge" title="Spans multiple repositories">🌐 Cross-Repo</span>
              <span class="epic-title-text">${escapeHtml(epic.epicTitle)}</span>
            </div>
            <div class="epic-badges">
              ${confidenceBadge}
              <span class="epic-progress-badge">${totalClosed}/${totalIssues} issues</span>
            </div>
          </div>
          <div class="epic-metrics">
            <div class="epic-metric">
              <div class="epic-metric-value">${hours}h</div>
              <div class="epic-metric-label">Remaining (~${days} days)</div>
            </div>
            <div class="epic-metric">
              <div class="epic-metric-value">${epic.overallCompletionPercent}%</div>
              <div class="epic-metric-label">Complete</div>
            </div>
            <div class="epic-metric">
              <div class="epic-metric-value">${epic.repositories.filter((r) => r.subIssues.length > 0).length}</div>
              <div class="epic-metric-label">Repositories</div>
            </div>
          </div>
          <div class="progress-bar-container">
            <div class="progress-bar" style="width: ${epic.overallCompletionPercent}%;"></div>
          </div>
          <details class="epic-details">
            <summary>View by Repository</summary>
            <div class="repo-progress-container">
              ${repoSections}
            </div>
            <div class="epic-footer">
              <div class="epic-footer-item">
                <strong>Integration Buffer:</strong> ${Math.round(epic.integrationBufferMinutes / 60)}h (15%)
              </div>
              <div class="epic-footer-item">
                <strong>Confidence:</strong> ${escapeHtml(epic.confidenceDetail)}
              </div>
              <div class="epic-footer-item">
                <strong>Last Updated:</strong> ${formatTimestamp(epic.fetchedAt)}
              </div>
            </div>
          </details>
        </div>
      `;
    })
    .join("");

  return `
    <div class="epic-estimates-section cross-repo-section">
      <div class="section-header">
        <h3>🌐 Cross-Repo Epics</h3>
        <span class="epic-count">${crossRepoEpics.length} cross-repo epic${crossRepoEpics.length !== 1 ? "s" : ""}</span>
      </div>
      <div class="epic-cards-container">
        ${epicCards}
      </div>
    </div>
  `;
}

/**
 * Generate project board widget HTML
 *
 * Renders the Project Board Summary widget with status counts, top ready issues,
 * sprint information, and a link to open the project board in browser.
 *
 * @see Issue #134 - Project Board Dashboard Widget
 */
function getProjectBoardWidgetHtml(projectBoardData: ProjectBoardData | null): string {
  // No data (panel just opened, pre-fetch hasn't fired yet) — render nothing so
  // the widget doesn't flash an empty board.
  if (!projectBoardData) {
    return "";
  }

  if (!projectBoardData.isConfigured) {
    return `
      <div class="project-board-widget empty-state">
        <div class="section-header">
          <h3>📋 Project Board Summary</h3>
        </div>
        <p>Project board not configured. Add project.number to .nightgauge/config.yaml</p>
      </div>
    `;
  }

  // Initial fetch in flight — distinct from the steady-state "empty board"
  // case so users don't see a frozen 0/0/0/0 while data is still loading.
  if (projectBoardData.loadingState === "loading") {
    return `
      <div class="project-board-widget loading-state">
        <div class="section-header">
          <h3>📋 Project Board Summary</h3>
        </div>
        <p class="board-loading-message">Loading project board…</p>
      </div>
    `;
  }

  if (projectBoardData.error || projectBoardData.loadingState === "error") {
    const message = projectBoardData.error ?? "Project board fetch failed";
    return `
      <div class="project-board-widget error-state">
        <div class="section-header">
          <h3>📋 Project Board Summary</h3>
          <button class="refresh-widget-btn" id="refreshProjectBoard" title="Retry fetch">
            &#8635;
          </button>
        </div>
        <p class="error-message">${escapeHtml(message)}</p>
      </div>
    `;
  }

  const { statusCounts, topReadyIssues, currentSprint, projectUrl, lastRefreshed, diagnostics } =
    projectBoardData;

  // Decode the "all zeros" outcome. The board can have items that all get
  // filtered out by the workspace's repo match — the user sees 0/0/0/0 and
  // assumes the board is empty when really their `project.repo` config is
  // wrong. Surface it explicitly.
  const totalCount =
    statusCounts.ready +
    statusCounts.inProgress +
    statusCounts.inReview +
    statusCounts.done +
    statusCounts.backlog;
  const repoFilteredOutEverything =
    totalCount === 0 &&
    diagnostics !== undefined &&
    diagnostics.rawItemCount > 0 &&
    diagnostics.filteredItemCount === 0;
  const trulyEmptyBoard =
    totalCount === 0 && diagnostics !== undefined && diagnostics.rawItemCount === 0;

  // Generate status count cards
  const statusCards = `
    <div class="status-counts-grid">
      <div class="status-count-card status-ready">
        <div class="status-count-value">${statusCounts.ready}</div>
        <div class="status-count-label">Ready</div>
      </div>
      <div class="status-count-card status-in-progress">
        <div class="status-count-value">${statusCounts.inProgress}</div>
        <div class="status-count-label">In Progress</div>
      </div>
      <div class="status-count-card status-in-review">
        <div class="status-count-value">${statusCounts.inReview}</div>
        <div class="status-count-label">In Review</div>
      </div>
      <div class="status-count-card status-done">
        <div class="status-count-value">${statusCounts.done}</div>
        <div class="status-count-label">Done</div>
      </div>
    </div>
  `;

  // Honest empty-state diagnostics. We only render these when statusCounts is
  // all zeros, so a healthy board with real items shows no extra chrome.
  let emptyStateBanner = "";
  if (repoFilteredOutEverything) {
    const repoLabel = diagnostics?.expectedRepo
      ? `<code>${escapeHtml(diagnostics.expectedRepo)}</code>`
      : "this workspace's repo";
    emptyStateBanner = `
      <div class="board-empty-banner board-empty-warning" role="status">
        Board has ${diagnostics!.rawItemCount} item${diagnostics!.rawItemCount === 1 ? "" : "s"},
        but none belong to ${repoLabel}.
        Check <code>project.repo</code> in <code>.nightgauge/nightgauge.yaml</code>.
      </div>
    `;
  } else if (trulyEmptyBoard) {
    emptyStateBanner = `
      <div class="board-empty-banner board-empty-info" role="status">
        No issues on this project board yet.
      </div>
    `;
  }

  // The "Top Ready Issues" list that used to render here was replaced by the
  // Pipeline Slots cards on the Overview tab — those show every running slot
  // and the queue with live telemetry, so an additional ready-only list here
  // would just duplicate information.
  void topReadyIssues;

  // Sprint section
  const sprintSection = currentSprint
    ? `
    <div class="sprint-info">
      <span class="sprint-icon">🏃</span>
      <span class="sprint-name">${escapeHtml(currentSprint.title)}</span>
    </div>
  `
    : "";

  // Open board link
  const openBoardLink = projectUrl
    ? `
    <a href="${escapeHtml(projectUrl)}" class="open-board-link" title="Open project board in browser">
      Open Board ↗
    </a>
  `
    : "";

  // Last refreshed timestamp
  const lastRefreshedText = lastRefreshed ? formatRelativeTime(lastRefreshed) : "Never";

  return `
    <div class="project-board-widget">
      <div class="section-header">
        <h3>📋 Project Board Summary</h3>
        <div class="widget-actions">
          ${sprintSection}
          <button class="refresh-widget-btn" id="refreshProjectBoard" title="Refresh project board data">
            &#8635;
          </button>
        </div>
      </div>

      ${statusCards}
      ${emptyStateBanner}

      <div class="widget-footer">
        ${openBoardLink}
        <span class="last-refreshed">Updated ${lastRefreshedText}</span>
      </div>
    </div>
  `;
}

function getRepositoryProgressHtml(repo: RepositoryProgress): string {
  if (repo.status === "error") {
    return `
      <details class="repo-progress-section repo-error">
        <summary>
          <span class="repo-name">${escapeHtml(repo.name)}</span>
          <span class="repo-error-badge">⚠️ Error</span>
        </summary>
        <div class="repo-error-message">${escapeHtml(repo.errorMessage || "Unknown error")}</div>
      </details>
    `;
  }

  if (repo.subIssues.length === 0) {
    return ""; // Skip repos with no issues
  }

  const hours = Math.round(repo.remainingMinutes / 60);

  return `
    <details class="repo-progress-section">
      <summary>
        <span class="repo-name">${escapeHtml(repo.name)}</span>
        <div class="repo-summary-stats">
          <span class="repo-completion">${repo.completionPercent}%</span>
          <span class="repo-issues">${repo.closedCount}/${repo.closedCount + repo.openCount}</span>
          <span class="repo-remaining">${hours}h left</span>
        </div>
      </summary>
      <div class="repo-progress-bar-container">
        <div class="repo-progress-bar" style="width: ${repo.completionPercent}%;"></div>
      </div>
      <ul class="repo-sub-issues-list">
        ${repo.subIssues
          .map((issue) => {
            const statusIcon = issue.status === "closed" ? "✓" : " ";
            const sizeStr = issue.size || "?";
            const issueHours = Math.round(issue.estimated_minutes / 60);
            return `<li class="sub-issue ${issue.status}">
              <span class="sub-issue-status">[${statusIcon}]</span>
              <span class="sub-issue-id">#${issue.number}</span>
              <span class="sub-issue-size">(${sizeStr})</span>
              <span class="sub-issue-title">${escapeHtml(issue.title)}</span>
              <span class="sub-issue-estimate">${issueHours}h</span>
            </li>`;
          })
          .join("")}
      </ul>
    </details>
  `;
}

export {
  getEpicEstimatesHtml,
  getCrossRepoEpicProgressHtml,
  getProjectBoardWidgetHtml,
  getRepositoryProgressHtml,
};

// CSS styles extracted to EpicsTabStyles.ts (#1542)
export { getEpicsTabStyles } from "./EpicsTabStyles";
