/**
 * DiscoveryTabHtml - Discovery Activity tab renderer
 *
 * Shows autonomous self-improvement loop activity:
 * - Last release-watch run: version detected, issues created, backlogged
 * - Last continuous-improvement run: proposals created, mode used
 * - Pending backlog: changes scored but not yet acted upon
 *
 * Data comes from DiscoveryActivityService which reads state files
 * written by GitHub Actions workflows.
 *
 * @see Issue #2434 — activate autonomous self-improvement loop
 * @see docs/SCHEDULED_DISCOVERY.md
 */

import { escapeHtml, formatRelativeTime } from "../DashboardComponents";
import type { DiscoveryActivityData } from "../../../services/DiscoveryActivityService";

const BACKLOG_DISPLAY_LIMIT = 20;

/**
 * Generate the Discovery Activity tab HTML.
 *
 * When `data` is null (service not initialized or workspace has no data yet),
 * renders a placeholder with setup instructions.
 */
export function getDiscoveryTabHtml(data: DiscoveryActivityData | null): string {
  if (!data) {
    return getDiscoveryEmptyState(
      "Discovery service not available",
      "Open a workspace with an .nightgauge/config.yaml to enable autonomous discovery."
    );
  }

  const { releaseWatch, continuousImprovement, backlog, summary } = data;
  const hasAnyActivity = releaseWatch !== null || continuousImprovement !== null;

  return `
    <div class="discovery-tab">
      <div class="discovery-tab-header">
        <button class="action-btn" id="discoveryRefreshBtn" title="Force refresh">&#8635;</button>
        <script>
          (function() {
            var btn = document.getElementById('discoveryRefreshBtn');
            if (btn) {
              btn.addEventListener('click', function() {
                vscode.postMessage({ type: 'discoveryRefresh' });
              });
            }
          })();
        </script>
      </div>
      ${getSummaryCardsHtml(summary)}
      ${
        hasAnyActivity
          ? ""
          : getDiscoveryEmptyState(
              "No discovery activity yet",
              "Autonomous discovery runs daily (release-watch) and weekly (continuous-improvement) via GitHub Actions. Trigger a run manually via <code>workflow_dispatch</code> or wait for the next scheduled run."
            )
      }
      ${getReleaseWatchSectionHtml(releaseWatch)}
      ${getContinuousImprovementSectionHtml(continuousImprovement)}
      ${getBacklogSectionHtml(backlog)}
      ${getKillSwitchInfoHtml()}
    </div>
  `;
}

function getSummaryCardsHtml(summary: DiscoveryActivityData["summary"]): string {
  const formatTs = (ts: string | null): string => {
    if (!ts) return "Never";
    try {
      return formatRelativeTime(new Date(ts));
    } catch {
      return ts;
    }
  };

  return `
    <div class="discovery-summary-cards">
      <div class="discovery-summary-card">
        <div class="card-value">${summary.issuesCreatedThisWeek}</div>
        <div class="card-label">Issues Created (7d)</div>
      </div>
      <div class="discovery-summary-card">
        <div class="card-value">${summary.proposalsCreatedThisWeek}</div>
        <div class="card-label">Proposals (7d)</div>
      </div>
      <div class="discovery-summary-card">
        <div class="card-value">${summary.pendingBacklogCount}</div>
        <div class="card-label">Pending Backlog</div>
      </div>
      <div class="discovery-summary-card wide">
        <div class="card-label">Last Release-Watch</div>
        <div class="card-timestamp">${formatTs(summary.lastReleaseWatchAt)}</div>
      </div>
      <div class="discovery-summary-card wide">
        <div class="card-label">Last CI Review</div>
        <div class="card-timestamp">${formatTs(summary.lastContinuousImprovementAt)}</div>
      </div>
    </div>
  `;
}

function getReleaseWatchSectionHtml(run: DiscoveryActivityData["releaseWatch"]): string {
  if (!run) return "";

  const statusBadge = getStatusBadge(run.status);
  const createdCount = run.issues_created.length;
  const backloggedCount = run.issues_backlogged.length;
  const dedupedCount = run.issues_deduped.length;

  const issuesList =
    createdCount > 0
      ? `<ul class="discovery-issues-list">
          ${run.issues_created
            .map(
              (i) =>
                `<li>
                  <a href="${escapeHtml(i.url)}" class="discovery-issue-link" title="Open issue #${i.number}">
                    #${i.number} ${escapeHtml(i.title)}
                  </a>
                  ${i.score != null ? `<span class="discovery-score-badge">${i.score}</span>` : ""}
                </li>`
            )
            .join("")}
        </ul>`
      : '<p class="discovery-empty-text">No issues created in this run.</p>';

  return `
    <details class="collapsible-section" open>
      <summary class="section-toggle">
        <span class="toggle-icon">▼</span>
        <h3>🔍 Release-Watch</h3>
        ${statusBadge}
      </summary>
      <div class="section-content">
        <div class="discovery-run-card">
          <div class="discovery-run-meta">
            <span class="discovery-meta-item">
              <strong>Version:</strong> ${escapeHtml(run.new_version)}
            </span>
            <span class="discovery-meta-item">
              <strong>Since:</strong> ${escapeHtml(run.since_version)}
            </span>
            <span class="discovery-meta-item">
              <strong>Trigger:</strong> ${escapeHtml(run.triggered_by)}
            </span>
          </div>

          <div class="discovery-counts-row">
            <span class="discovery-count-chip created">${createdCount} created</span>
            <span class="discovery-count-chip backlogged">${backloggedCount} backlogged</span>
            ${dedupedCount > 0 ? `<span class="discovery-count-chip deduped">${dedupedCount} deduped</span>` : ""}
          </div>

          ${createdCount > 0 ? `<h4>Auto-Created Issues</h4>` : ""}
          ${issuesList}

          ${run.error ? `<div class="discovery-error-banner">Error: ${escapeHtml(run.error)}</div>` : ""}
        </div>
      </div>
    </details>
  `;
}

function getContinuousImprovementSectionHtml(
  run: DiscoveryActivityData["continuousImprovement"]
): string {
  if (!run) return "";

  const statusBadge = getStatusBadge(run.status);
  const createdCount = run.proposals_created.length;
  const backloggedCount = run.proposals_backlogged.length;

  const proposalsList =
    createdCount > 0
      ? `<ul class="discovery-issues-list">
          ${run.proposals_created
            .map(
              (p) =>
                `<li>
                  <a href="${escapeHtml(p.url)}" class="discovery-issue-link" title="Open issue #${p.number}">
                    #${p.number} ${escapeHtml(p.title)}
                  </a>
                </li>`
            )
            .join("")}
        </ul>`
      : '<p class="discovery-empty-text">No proposals created in this run.</p>';

  const dryRunBadge = run.dry_run ? '<span class="discovery-badge badge-info">Dry Run</span>' : "";

  return `
    <details class="collapsible-section" open>
      <summary class="section-toggle">
        <span class="toggle-icon">▼</span>
        <h3>🔄 Continuous Improvement</h3>
        ${statusBadge}
        ${dryRunBadge}
      </summary>
      <div class="section-content">
        <div class="discovery-run-card">
          <div class="discovery-run-meta">
            <span class="discovery-meta-item">
              <strong>Mode:</strong> ${escapeHtml(run.mode)}
            </span>
            <span class="discovery-meta-item">
              <strong>Trigger:</strong> ${escapeHtml(run.triggered_by)}
            </span>
            <span class="discovery-meta-item">
              <strong>Issue creation:</strong> ${run.create_issues ? "enabled" : "disabled"}
            </span>
          </div>

          <div class="discovery-counts-row">
            <span class="discovery-count-chip created">${createdCount} proposals</span>
            <span class="discovery-count-chip backlogged">${backloggedCount} backlogged</span>
          </div>

          ${createdCount > 0 ? `<h4>Proposal Issues</h4>` : ""}
          ${proposalsList}

          ${run.error ? `<div class="discovery-error-banner">Error: ${escapeHtml(run.error)}</div>` : ""}
        </div>
      </div>
    </details>
  `;
}

function getBacklogSectionHtml(backlog: DiscoveryActivityData["backlog"]): string {
  if (backlog.length === 0) return "";

  const rows = backlog
    .sort((a, b) => b.score - a.score)
    .slice(0, BACKLOG_DISPLAY_LIMIT)
    .map(
      (entry) =>
        `<tr>
          <td>${escapeHtml(entry.title)}</td>
          <td><span class="discovery-score-badge">${entry.score}</span></td>
          <td>${escapeHtml(entry.reason)}</td>
        </tr>`
    )
    .join("");

  return `
    <details class="collapsible-section">
      <summary class="section-toggle">
        <span class="toggle-icon">▼</span>
        <h3>📋 Pending Backlog</h3>
        <span class="section-badge">${backlog.length}</span>
      </summary>
      <div class="section-content">
        <p class="discovery-backlog-hint">
          These changes scored below the auto-creation threshold. Raise the score threshold
          or run <code>/nightgauge:release-watch --create-issues</code> to process them.
        </p>
        <table class="discovery-backlog-table">
          <thead>
            <tr>
              <th>Change</th>
              <th>Score</th>
              <th>Reason</th>
            </tr>
          </thead>
          <tbody>${rows}</tbody>
        </table>
        ${backlog.length > BACKLOG_DISPLAY_LIMIT ? `<p class="discovery-truncation-note">Showing top ${BACKLOG_DISPLAY_LIMIT} of ${backlog.length} entries.</p>` : ""}
      </div>
    </details>
  `;
}

function getKillSwitchInfoHtml(): string {
  return `
    <details class="collapsible-section">
      <summary class="section-toggle">
        <span class="toggle-icon">▼</span>
        <h3>⚙️ Configuration</h3>
      </summary>
      <div class="section-content">
        <div class="discovery-config-card">
          <p>Autonomous discovery is configured in <code>.nightgauge/config.yaml</code>:</p>
          <pre class="discovery-config-example">autonomous_discovery:
  enabled: true        # Master switch
  kill_switch: false   # Pause issue creation (detection continues)
  score_threshold: 70  # Min score to auto-create an issue

scheduled_tasks:
  release_watch:
    enabled: true      # Daily at 9 AM UTC
  continuous_improvement:
    enabled: true      # Weekly on Monday 8 AM UTC</pre>
          <p class="discovery-config-hint">
            Set <code>kill_switch: true</code> to pause issue creation without
            disabling detection infrastructure. Set <code>enabled: false</code>
            to disable all scheduled runs.
          </p>
        </div>
      </div>
    </details>
  `;
}

function getStatusBadge(status: "running" | "completed" | "failed"): string {
  switch (status) {
    case "completed":
      return '<span class="discovery-badge badge-success">Completed</span>';
    case "running":
      return '<span class="discovery-badge badge-info">Running</span>';
    case "failed":
      return '<span class="discovery-badge badge-danger">Failed</span>';
  }
}

/**
 * @param title - Plain text title (will be escaped)
 * @param bodyHtml - Trusted HTML body — MUST NOT contain user-supplied data.
 *   All call sites pass static string literals only.
 */
function getDiscoveryEmptyState(title: string, bodyHtml: string): string {
  return `
    <div class="discovery-empty-state">
      <div class="discovery-empty-icon">🔍</div>
      <h3>${escapeHtml(title)}</h3>
      <p>${bodyHtml}</p>
    </div>
  `;
}
