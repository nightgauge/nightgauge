/**
 * OverviewTabHtml - Dashboard overview tab renderer
 *
 * Extracted from DashboardHtml.ts as part of the tab-based modular refactor (#1542).
 * Contains the adapter status widget, summary stat cards, scope toggle,
 * and their associated styles.
 */

import {
  formatTimeSaved,
  formatPercent,
  formatStageName,
  formatDuration,
  formatTokenCount,
  escapeHtml,
} from "../DashboardComponents";
import type {
  DashboardAggregates,
  AdapterStatusData,
  RecentActivityDelta,
} from "../DashboardState";
import type {
  PipelineSlotsViewData,
  SlotCardData,
  QueuedCardData,
  SlotStageStatus,
} from "../SlotCardTypes";

/**
 * Generate adapter status bar widget HTML (Issue #1056)
 */
function getAdapterStatusWidgetHtml(data: AdapterStatusData | null): string {
  if (!data) {
    return "";
  }

  const authBadge = data.authConfigured
    ? '<span class="badge badge-success">Configured</span>'
    : '<span class="badge badge-warning">Not Configured</span>';

  const isGemini = data.adapter === "gemini" || data.adapter === "gemini-sdk";

  return `
    <div class="adapter-status-bar">
      <span class="adapter-label">Adapter:</span>
      <strong>${data.displayName}</strong>
      ${isGemini && data.model ? `<span class="adapter-sep">|</span> Model: <strong>${data.model}</strong>` : ""}
      ${isGemini && data.authMethod ? `<span class="adapter-sep">|</span> Auth: ${data.authMethod}` : ""}
      ${isGemini ? `<span class="adapter-sep">|</span> API Key: ${authBadge}` : ""}
    </div>`;
}

// ---------------------------------------------------------------------------
// Pipeline Slots — per-slot live cards + queued issue cards.
//
// Replaces the prior single-run "Running Now" widget and the Project Board
// "Top Ready Issues" list. One section, one source of truth: every active
// concurrent slot becomes a rich card showing live stage / phase / progress /
// tokens / cost; everything in the queue gets a compact card next to them.
// Click a card → opens the slot's per-slot output channel.
// ---------------------------------------------------------------------------

const SLOT_CARD_STAGE_ORDER = [
  "pipeline-start",
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
  "pipeline-finish",
] as const;

function priorityBadge(priority: QueuedCardData["priority"]): string {
  if (!priority) return "";
  const colors: Record<NonNullable<QueuedCardData["priority"]>, string> = {
    P0: "var(--vscode-charts-red)",
    P1: "var(--vscode-charts-orange)",
    P2: "var(--vscode-charts-blue)",
  };
  return `<span class="slot-priority-badge" style="background:${colors[priority]}">${priority}</span>`;
}

function stageDotClass(status: SlotStageStatus): string {
  switch (status) {
    case "running":
      return "slot-stage-dot running";
    case "complete":
      return "slot-stage-dot complete";
    case "failed":
      return "slot-stage-dot failed";
    case "skipped":
      return "slot-stage-dot skipped";
    case "deferred":
      return "slot-stage-dot deferred";
    default:
      return "slot-stage-dot pending";
  }
}

/**
 * Map a repo short name to one of a small palette of color classes so the
 * same repo always reads the same color in slot-card and queued-card chips
 * across runs. New / unknown repos hash into the palette deterministically
 * so the operator never sees a "no color" fallback. #3690.
 */
function slotCardRepoColorClass(repoShort: string): string {
  const palette = ["purple", "blue", "orange", "teal", "green", "pink", "yellow"];
  // Stable hash — deterministic across runs for the same name.
  let hash = 0;
  for (let i = 0; i < repoShort.length; i++) {
    hash = (hash * 31 + repoShort.charCodeAt(i)) | 0;
  }
  return palette[Math.abs(hash) % palette.length] ?? "purple";
}

/**
 * Render a single active-slot card with live pipeline telemetry.
 */
function renderSlotCard(slot: SlotCardData): string {
  const elapsedMs = slot.startedAt ? Date.now() - new Date(slot.startedAt).getTime() : 0;
  const elapsedLabel = slot.startedAt ? formatDuration(elapsedMs) : "—";
  // #3819: the TOKENS headline is fresh model I/O only — it deliberately
  // EXCLUDES cache-read tokens. Cache reads (`cache_read_input_tokens`) are a
  // distinct, ~10–20× cheaper token class the cost path already prices
  // separately; folding them in inflated a ~9-min slot to 1.1M/2.2M while COST
  // read $0.00. Cache reads are surfaced below as a dimmed "cached" annotation
  // so the cache benefit stays visible — do NOT add cacheReadTokens here.
  const tokensTotal = slot.inputTokens + slot.outputTokens;
  const cachedTokens = slot.cacheReadTokens;
  const stageProgressPct =
    slot.totalStageCount > 0
      ? Math.round((slot.completedStageCount / slot.totalStageCount) * 100)
      : 0;

  const stageDots = SLOT_CARD_STAGE_ORDER.map((stage) => {
    const entry = slot.stages.find((s) => s.stage === stage);
    const status = entry?.status ?? "pending";
    return `<span class="${stageDotClass(status)}" title="${formatStageName(stage)}: ${status}"></span>`;
  }).join("");

  const stageLabel = slot.currentStage
    ? formatStageName(slot.currentStage)
    : slot.status === "completed"
      ? "Completed"
      : slot.status === "failed"
        ? "Failed"
        : "Waiting for next stage";

  const phaseLabel = slot.currentPhase
    ? `${escapeHtml(slot.currentPhase.name)} (${slot.currentPhase.index}/${slot.currentPhase.total})`
    : "";

  const cardStateClass = `slot-card-${slot.status}`;
  const issueLine = `#${slot.issueNumber} — ${escapeHtml(slot.title)}`;
  // #3690: surface the target repo as a colored chip in the card header
  // rather than burying it in the meta subline. When the autonomous panel
  // surfaces a paused run as "issue #N failed at stage X", the operator
  // currently can't tell from the slot card alone which repo to look in —
  // dashboard, platform, flutter, etc. The chip uses a stable color derived
  // from the repo's short name so the same repo always reads the same.
  const repoShort = slot.repoName ? (slot.repoName.split("/").pop() ?? slot.repoName) : "";
  const repoChip = repoShort
    ? `<span class="slot-card-repo slot-card-repo-${escapeHtml(slotCardRepoColorClass(repoShort))}" title="${escapeHtml(slot.repoName ?? "")}">${escapeHtml(repoShort)}</span>`
    : "";
  const branchMeta = slot.branch ? escapeHtml(slot.branch) : "";

  // data-slot-index drives the click handler in the embedded JS, so the
  // payload remains a plain primitive (avoids JSON-serialised attribute escaping).
  return `
    <button type="button"
            class="slot-card ${cardStateClass}${slot.hasIssues ? " has-issues" : ""}"
            data-slot-index="${slot.slotIndex}"
            data-issue-number="${slot.issueNumber}"
            title="Open output for slot ${slot.slotIndex + 1}">
      <div class="slot-card-header">
        <span class="slot-card-badge">Slot ${slot.slotIndex + 1}</span>
        ${slot.status === "running" ? '<span class="slot-card-pulse" aria-hidden="true"></span>' : ""}
        ${repoChip}
        ${slot.epicNumber ? `<span class="slot-card-epic">Epic #${slot.epicNumber}</span>` : ""}
        <span class="slot-card-issue">${escapeHtml(issueLine)}</span>
      </div>
      ${branchMeta ? `<div class="slot-card-meta">${branchMeta}</div>` : ""}
      <div class="slot-card-stage">
        <span class="slot-card-stage-label">${escapeHtml(stageLabel)}</span>
        ${
          slot.currentStage && slot.totalStageCount > 0
            ? `<span class="slot-card-stage-index">${slot.completedStageCount}/${slot.totalStageCount}</span>`
            : ""
        }
        ${phaseLabel ? `<span class="slot-card-phase">${phaseLabel}</span>` : ""}
      </div>
      <div class="slot-card-stage-strip">${stageDots}</div>
      <div class="slot-card-progress" aria-label="Stage progress">
        <div class="slot-card-progress-fill" style="width:${stageProgressPct}%"></div>
      </div>
      <div class="slot-card-metrics">
        <div class="slot-card-metric">
          <div class="slot-card-metric-value">${elapsedLabel}</div>
          <div class="slot-card-metric-label">Elapsed</div>
        </div>
        <div class="slot-card-metric">
          <div class="slot-card-metric-value">$${slot.costUsd.toFixed(2)}</div>
          <div class="slot-card-metric-label">Cost</div>
        </div>
        <div class="slot-card-metric">
          <div class="slot-card-metric-value">${formatTokenCount(tokensTotal)}</div>
          <div class="slot-card-metric-label">Tokens</div>
          ${cachedTokens > 0 ? `<div class="slot-card-metric-sub">${formatTokenCount(cachedTokens)} cached</div>` : ""}
        </div>
        <div class="slot-card-metric">
          <div class="slot-card-metric-value">${slot.completedStageCount}/${slot.totalStageCount}</div>
          <div class="slot-card-metric-label">Stages</div>
        </div>
      </div>
    </button>
  `;
}

/**
 * Render a single queued-issue card.
 */
function renderQueuedCard(item: QueuedCardData): string {
  const blockerSummary = item.isBlocked
    ? `<span class="slot-queued-blocker">🔒 Blocked by ${item.blockerCount} issue${item.blockerCount === 1 ? "" : "s"}</span>`
    : "";
  // #3690: color-coded repo chip so queued cards visually disambiguate which
  // repo each issue belongs to (matching the running-slot card treatment).
  const queuedRepoShort = item.repoName ? (item.repoName.split("/").pop() ?? item.repoName) : "";
  const repo = queuedRepoShort
    ? `<span class="slot-queued-repo slot-card-repo-${escapeHtml(slotCardRepoColorClass(queuedRepoShort))}" title="${escapeHtml(item.repoName ?? "")}">${escapeHtml(queuedRepoShort)}</span>`
    : "";
  const epic = item.epicNumber
    ? `<span class="slot-queued-epic">Epic #${item.epicNumber}</span>`
    : "";

  return `
    <li class="slot-queued-card${item.isBlocked ? " blocked" : ""}">
      <div class="slot-queued-position">#${item.position}</div>
      <div class="slot-queued-body">
        <div class="slot-queued-title">
          ${priorityBadge(item.priority)}
          <span class="slot-queued-issue">#${item.issueNumber}</span>
          <span class="slot-queued-text">${escapeHtml(item.title)}</span>
        </div>
        <div class="slot-queued-meta">
          ${repo}
          ${epic}
          ${blockerSummary}
        </div>
      </div>
    </li>
  `;
}

/**
 * Render the full Pipeline Slots section.
 */
function getPipelineSlotsHtml(view: PipelineSlotsViewData | null): string {
  if (!view) return "";

  const { slots, queued, maxConcurrent, queueStatus } = view;
  const noWork = slots.length === 0 && queued.length === 0;

  if (noWork) {
    return `
      <div class="pipeline-slots empty">
        <div class="pipeline-slots-header">
          <h3>Pipeline Slots</h3>
          <span class="pipeline-slots-meta">${maxConcurrent} slot${maxConcurrent === 1 ? "" : "s"} · idle</span>
        </div>
        <div class="empty-state">
          <p>The pipeline is idle. Add an issue to the queue or start a run to see live progress here.</p>
        </div>
      </div>
    `;
  }

  const slotCards = slots.map(renderSlotCard).join("");
  const queuedItems = queued.map(renderQueuedCard).join("");

  const headerStatus = `${slots.length} running · ${queued.length} queued`;

  return `
    <div class="pipeline-slots">
      <div class="pipeline-slots-header">
        <h3>Pipeline Slots</h3>
        <span class="pipeline-slots-meta">
          ${maxConcurrent} slot${maxConcurrent === 1 ? "" : "s"} · ${headerStatus}
          ${queueStatus === "paused" ? '<span class="pipeline-slots-paused">⏸ paused</span>' : ""}
        </span>
      </div>
      ${slots.length > 0 ? `<div class="slot-card-grid">${slotCards}</div>` : ""}
      ${
        queued.length > 0
          ? `
        <div class="slot-queued-wrap">
          <h4 class="slot-queued-heading">Up next</h4>
          <ul class="slot-queued-list">${queuedItems}</ul>
        </div>
      `
          : ""
      }
    </div>
  `;
}

/**
 * Incremental-update variant used by Dashboard.postIncrementalUpdate.
 */
function getPipelineSlotsSectionHtml(view: PipelineSlotsViewData | null): string {
  return getPipelineSlotsHtml(view);
}

// ---------------------------------------------------------------------------
// Legacy single-run activity widget — replaced by Pipeline Slots above.
// Kept as a no-op stub so any external consumer that still imports it gets
// an empty string instead of an undefined export.
// ---------------------------------------------------------------------------
function getCurrentActivityWidgetHtml(): string {
  return "";
}

/**
 * Render a delta chip beneath a stat card.
 *
 * - `polarity` decides the color: "lower-is-better" (cost) flips the green
 *   / red mapping vs the default "higher-is-better".
 * - `formatter` formats the absolute delta value (e.g. `+12`, `+$5.20`).
 * - Always shows an explicit sign so the chip reads as a comparison.
 *
 * Returns an empty string when the All Time scope isn't active or when the
 * recent window has no runs — a "+0 this week" chip on day one would be
 * misleading.
 */
type DeltaPolarity = "higher-is-better" | "lower-is-better";

function renderDeltaChip(
  delta: number,
  formatter: (abs: number) => string,
  polarity: DeltaPolarity,
  windowDays: number,
  hasEnoughData: boolean,
  scope: "session" | "all"
): string {
  if (scope !== "all" || !hasEnoughData) return "";

  let directionClass: string;
  if (delta === 0) {
    directionClass = "stat-delta-stable";
  } else {
    const positive = delta > 0;
    const isImproving =
      (positive && polarity === "higher-is-better") ||
      (!positive && polarity === "lower-is-better");
    directionClass = isImproving ? "stat-delta-improving" : "stat-delta-degrading";
  }

  const sign = delta > 0 ? "+" : delta < 0 ? "−" : "±";
  const magnitude = formatter(Math.abs(delta));
  return `
    <div class="stat-delta ${directionClass}" title="Compared to the prior ${windowDays} day${windowDays === 1 ? "" : "s"}">
      ${sign}${escapeHtml(magnitude)} <span class="stat-delta-window">vs prior ${windowDays}d</span>
    </div>
  `;
}

/**
 * Render the success-rate delta in percentage points (e.g. "+5pp"), not as
 * a relative change. Around small baselines a relative percent change is
 * meaningless or explodes; percentage-point deltas always read correctly.
 */
function renderSuccessRateDeltaChip(
  pointsDelta: number,
  recentRate: number,
  windowDays: number,
  hasEnoughData: boolean,
  scope: "session" | "all"
): string {
  if (scope !== "all" || !hasEnoughData) return "";

  let directionClass: string;
  if (pointsDelta === 0) directionClass = "stat-delta-stable";
  else directionClass = pointsDelta > 0 ? "stat-delta-improving" : "stat-delta-degrading";

  const sign = pointsDelta > 0 ? "+" : pointsDelta < 0 ? "−" : "±";
  // Suppress the delta noise on a brand-new account where the recent rate is
  // a meaningful number — but still show the comparison context.
  return `
    <div class="stat-delta ${directionClass}" title="Compared to the prior ${windowDays} day${windowDays === 1 ? "" : "s"}; recent ${Math.round(recentRate * 100)}%">
      ${sign}${Math.abs(pointsDelta)}pp <span class="stat-delta-window">vs prior ${windowDays}d</span>
    </div>
  `;
}

/**
 * Generate summary stat cards HTML
 */
function getSummaryCardsHtml(aggregates: DashboardAggregates, scope: "session" | "all"): string {
  const runs = scope === "session" ? aggregates.sessionRuns : aggregates.totalRuns;
  const timeSaved =
    scope === "session" ? aggregates.sessionTimeSavedMs : aggregates.totalTimeSavedMs;
  const cost = scope === "session" ? aggregates.sessionCostUsd : aggregates.totalCostUsd;
  const tokens = scope === "session" ? aggregates.sessionTokens : aggregates.totalTokens;

  // Defensive default — if aggregation hasn't populated the delta yet (or a
  // caller hands us a hand-built DashboardAggregates from a test fixture), we
  // treat it as "no signal" rather than throwing. The chip helpers already
  // suppress output when hasEnoughData is false.
  const d: RecentActivityDelta = aggregates.recentDelta ?? {
    runsDelta: 0,
    runsPrior: 0,
    timeSavedDeltaMs: 0,
    timeSavedPriorMs: 0,
    costDeltaUsd: 0,
    costPriorUsd: 0,
    successRatePointsDelta: 0,
    successRateRecent: 0,
    successRatePrior: 0,
    hasEnoughData: false,
    windowDays: 7,
  };
  const runsChip = renderDeltaChip(
    d.runsDelta,
    (n) => `${n} run${n === 1 ? "" : "s"}`,
    "higher-is-better",
    d.windowDays,
    d.hasEnoughData,
    scope
  );
  const timeSavedChip = renderDeltaChip(
    d.timeSavedDeltaMs,
    (n) => formatTimeSaved(n),
    "higher-is-better",
    d.windowDays,
    d.hasEnoughData,
    scope
  );
  const costChip = renderDeltaChip(
    d.costDeltaUsd,
    (n) => `$${n.toFixed(2)}`,
    "lower-is-better",
    d.windowDays,
    d.hasEnoughData,
    scope
  );
  const successChip = renderSuccessRateDeltaChip(
    d.successRatePointsDelta,
    d.successRateRecent,
    d.windowDays,
    d.hasEnoughData,
    scope
  );

  return `
    <div class="summary-cards">
      <div class="stat-card">
        <div class="stat-icon">&#9650;</div>
        <div class="stat-content">
          <div class="stat-value">${runs}</div>
          <div class="stat-label">Pipeline Runs</div>
          ${runsChip}
        </div>
      </div>
      <div class="stat-card highlight">
        <div class="stat-icon">&#9200;</div>
        <div class="stat-content">
          <div class="stat-value">${formatTimeSaved(timeSaved)}</div>
          <div class="stat-label">Time Saved</div>
          ${timeSavedChip}
        </div>
      </div>
      <div class="stat-card">
        <div class="stat-icon">&#36;</div>
        <div class="stat-content">
          <div class="stat-value">$${cost.toFixed(2)}</div>
          <div class="stat-label">Total Cost</div>
          ${costChip}
        </div>
      </div>
      <div class="stat-card">
        <div class="stat-icon">&#10003;</div>
        <div class="stat-content">
          <div class="stat-value">${formatPercent(aggregates.successRate)}</div>
          <div class="stat-label">Success Rate</div>
          ${successChip}
        </div>
      </div>
    </div>
  `;
}

/**
 * Generate scope toggle HTML
 */
function getScopeToggleHtml(currentScope: "session" | "all"): string {
  return `
    <div class="scope-toggle">
      <button class="toggle-btn ${currentScope === "session" ? "active" : ""}" data-scope="session">Session</button>
      <button class="toggle-btn ${currentScope === "all" ? "active" : ""}" data-scope="all">All Time</button>
    </div>
  `;
}

/**
 * Generate the inner HTML for the summary cards section (Issue #923).
 * Used by Dashboard.ts for incremental updates via postMessage.
 */
function getSummaryCardsSectionHtml(
  aggregates: DashboardAggregates,
  scope: "session" | "all"
): string {
  return getSummaryCardsHtml(aggregates, scope);
}

/**
 * Generate CSS for adapter status bar (Issue #1056)
 */
function getAdapterStatusStyles(): string {
  return `
    .adapter-status-bar {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      padding: var(--spacing-xs) var(--spacing-md);
      margin-bottom: var(--spacing-md);
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }
    .adapter-label {
      color: var(--vscode-descriptionForeground);
    }
    .adapter-sep {
      color: var(--vscode-panel-border);
    }
    .badge {
      padding: 1px 6px;
      border-radius: 3px;
      font-size: 0.8em;
      font-weight: 500;
    }
    .badge-success {
      background: var(--vscode-testing-iconPassed);
      color: var(--vscode-editor-background);
    }
    .badge-warning {
      background: var(--vscode-editorWarning-foreground);
      color: var(--vscode-editor-background);
    }
  `;
}

/**
 * Generate CSS for overview tab components
 *
 * Includes styles for summary cards, scope toggle, empty state,
 * and adapter status bar.
 */
export function getOverviewTabStyles(): string {
  return `
    /* Empty State */
    .empty-state {
      padding: var(--spacing-lg);
      text-align: center;
      color: var(--vscode-descriptionForeground);
    }

    /* Summary Cards */
    .summary-cards {
      display: grid;
      grid-template-columns: repeat(4, 1fr);
      gap: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }

    @media (max-width: 900px) {
      .summary-cards {
        grid-template-columns: repeat(2, 1fr);
      }
    }

    @media (max-width: 500px) {
      .summary-cards {
        grid-template-columns: 1fr;
      }
    }

    .stat-card {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
      display: flex;
      align-items: center;
      gap: var(--spacing-md);
    }

    .stat-card.highlight {
      border-color: var(--vscode-charts-green);
      background: linear-gradient(135deg,
        var(--vscode-editorWidget-background) 0%,
        rgba(75, 192, 75, 0.1) 100%
      );
    }

    .stat-icon {
      font-size: 1.5em;
      color: var(--vscode-charts-blue);
      width: 40px;
      height: 40px;
      display: flex;
      align-items: center;
      justify-content: center;
      background: var(--vscode-badge-background);
      border-radius: 50%;
    }

    .stat-card.highlight .stat-icon {
      color: var(--vscode-charts-green);
    }

    .stat-content {
      flex: 1;
    }

    .stat-value {
      font-size: 1.4em;
      font-weight: 600;
      color: var(--vscode-foreground);
    }

    .stat-label {
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
    }

    /* Recent-vs-prior delta chip beneath each stat value. The arrow color
       semantics match the sparklines: green for the "good" direction of
       this metric, red for the "bad" direction, neutral on zero. */
    .stat-delta {
      margin-top: 4px;
      font-size: 0.75em;
      font-weight: 500;
      letter-spacing: 0.1px;
      display: inline-flex;
      align-items: baseline;
      gap: 4px;
    }
    .stat-delta-window {
      color: var(--vscode-descriptionForeground);
      font-weight: 400;
      opacity: 0.75;
    }
    .stat-delta-improving { color: #4bc04b; }
    .stat-delta-degrading { color: #ff6384; }
    .stat-delta-stable { color: var(--vscode-descriptionForeground); }

    /* Scope Toggle */
    .scope-toggle {
      display: flex;
      gap: 1px;
      background: var(--vscode-input-background);
      border-radius: var(--border-radius);
      padding: 2px;
    }

    .toggle-btn {
      padding: 3px 10px;
      background: transparent;
      border: none;
      border-radius: 3px;
      color: var(--vscode-descriptionForeground);
      cursor: pointer;
      font-size: 0.8em;
      transition: background 0.15s, color 0.15s;
      white-space: nowrap;
    }

    .toggle-btn:hover {
      background: var(--vscode-list-hoverBackground);
    }

    .toggle-btn.active {
      background: var(--vscode-button-background);
      color: var(--vscode-button-foreground);
    }

    /* Pipeline Slots — live cards for active runs + queued items */
    .pipeline-slots {
      margin-bottom: var(--spacing-md);
    }

    .pipeline-slots-header {
      display: flex;
      align-items: baseline;
      justify-content: space-between;
      gap: var(--spacing-md);
      margin-bottom: var(--spacing-sm);
    }

    .pipeline-slots-header h3 {
      margin: 0;
      font-size: 0.95em;
    }

    .pipeline-slots-meta {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
    }

    .pipeline-slots-paused {
      color: var(--vscode-editorWarning-foreground);
      margin-left: var(--spacing-xs);
    }

    .slot-card-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
      gap: var(--spacing-md);
      margin-bottom: var(--spacing-md);
    }

    .slot-card {
      text-align: left;
      cursor: pointer;
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-left: 3px solid var(--vscode-charts-blue);
      border-radius: var(--border-radius);
      padding: var(--spacing-md);
      color: var(--vscode-foreground);
      font: inherit;
      display: flex;
      flex-direction: column;
      gap: var(--spacing-sm);
      transition: border-color 0.15s, background 0.15s;
    }

    .slot-card:hover {
      background: var(--vscode-list-hoverBackground);
    }

    .slot-card:focus-visible {
      outline: 2px solid var(--vscode-focusBorder);
      outline-offset: -2px;
    }

    .slot-card-running { border-left-color: var(--vscode-charts-blue); }
    .slot-card-completed { border-left-color: var(--vscode-charts-green); }
    .slot-card-failed { border-left-color: var(--vscode-charts-red); }
    .slot-card-paused { border-left-color: var(--vscode-charts-yellow); }
    .slot-card-preparing { border-left-color: var(--vscode-charts-purple); }
    .slot-card.has-issues { border-left-color: var(--vscode-charts-orange); }

    .slot-card-header {
      display: flex;
      align-items: center;
      gap: var(--spacing-xs);
      flex-wrap: wrap;
    }

    .slot-card-badge {
      font-size: 0.7em;
      font-weight: 700;
      letter-spacing: 0.06em;
      text-transform: uppercase;
      padding: 2px 6px;
      border-radius: 3px;
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
    }

    .slot-card-pulse {
      width: 8px;
      height: 8px;
      border-radius: 50%;
      background: var(--vscode-charts-green);
      box-shadow: 0 0 0 0 rgba(75, 192, 75, 0.55);
      animation: slot-card-pulse 1.6s ease-in-out infinite;
      flex-shrink: 0;
    }

    @keyframes slot-card-pulse {
      0% { box-shadow: 0 0 0 0 rgba(75, 192, 75, 0.55); }
      70% { box-shadow: 0 0 0 10px rgba(75, 192, 75, 0); }
      100% { box-shadow: 0 0 0 0 rgba(75, 192, 75, 0); }
    }

    .slot-card-epic {
      font-size: 0.7em;
      padding: 1px 5px;
      border-radius: 3px;
      background: var(--vscode-input-background);
      color: var(--vscode-descriptionForeground);
    }

    /* #3690: per-repo colored chip so multi-repo runs are visually
       disambiguated at a glance in the slot card header. */
    .slot-card-repo {
      font-size: 0.7em;
      font-weight: 600;
      padding: 1px 6px;
      border-radius: 8px;
      letter-spacing: 0.02em;
      white-space: nowrap;
      border: 1px solid transparent;
    }
    .slot-card-repo-purple { background: rgba(155, 89, 182, 0.15); color: #b07acc; border-color: rgba(155, 89, 182, 0.4); }
    .slot-card-repo-blue   { background: rgba( 52,152,219, 0.15); color: #6cb6e6; border-color: rgba( 52,152,219, 0.4); }
    .slot-card-repo-orange { background: rgba(230,126, 34, 0.15); color: #f0a061; border-color: rgba(230,126, 34, 0.4); }
    .slot-card-repo-teal   { background: rgba( 26,188,156, 0.15); color: #5bd1b5; border-color: rgba( 26,188,156, 0.4); }
    .slot-card-repo-green  { background: rgba( 46,204,113, 0.15); color: #6cd693; border-color: rgba( 46,204,113, 0.4); }
    .slot-card-repo-pink   { background: rgba(231, 76,180, 0.15); color: #f08fcb; border-color: rgba(231, 76,180, 0.4); }
    .slot-card-repo-yellow { background: rgba(241,196, 15, 0.15); color: #e8c970; border-color: rgba(241,196, 15, 0.4); }

    .slot-card-issue {
      flex: 1;
      min-width: 0;
      font-size: 0.95em;
      font-weight: 500;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .slot-card-meta {
      font-size: 0.75em;
      color: var(--vscode-descriptionForeground);
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .slot-card-stage {
      display: flex;
      align-items: baseline;
      gap: var(--spacing-xs);
      flex-wrap: wrap;
    }

    .slot-card-stage-label {
      font-weight: 600;
      font-size: 0.95em;
    }

    .slot-card-stage-index {
      font-size: 0.75em;
      color: var(--vscode-descriptionForeground);
    }

    .slot-card-phase {
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
    }

    .slot-card-phase::before {
      content: "· ";
    }

    .slot-card-stage-strip {
      display: flex;
      gap: 4px;
    }

    .slot-stage-dot {
      flex: 1;
      height: 6px;
      border-radius: 3px;
      background: var(--vscode-input-background);
    }

    .slot-stage-dot.running {
      background: var(--vscode-charts-blue);
      animation: slot-card-stage-pulse 1.4s ease-in-out infinite;
    }
    .slot-stage-dot.complete { background: var(--vscode-charts-green); }
    .slot-stage-dot.failed { background: var(--vscode-charts-red); }
    .slot-stage-dot.skipped { background: var(--vscode-descriptionForeground); opacity: 0.4; }
    .slot-stage-dot.deferred { background: var(--vscode-charts-yellow); opacity: 0.6; }

    @keyframes slot-card-stage-pulse {
      0%, 100% { opacity: 1; }
      50% { opacity: 0.55; }
    }

    .slot-card-progress {
      height: 4px;
      border-radius: 2px;
      background: var(--vscode-input-background);
      overflow: hidden;
    }

    .slot-card-progress-fill {
      height: 100%;
      background: linear-gradient(90deg, var(--vscode-charts-blue), var(--vscode-charts-green));
      transition: width 0.4s ease;
    }

    .slot-card-metrics {
      display: grid;
      grid-template-columns: repeat(4, 1fr);
      gap: var(--spacing-xs);
    }

    .slot-card-metric {
      text-align: center;
    }

    .slot-card-metric-value {
      font-size: 0.95em;
      font-weight: 600;
      font-variant-numeric: tabular-nums;
    }

    .slot-card-metric-label {
      font-size: 0.7em;
      text-transform: uppercase;
      letter-spacing: 0.04em;
      color: var(--vscode-descriptionForeground);
    }

    /* #3819: dimmed secondary annotation surfacing cache-read tokens that the
       TOKENS headline deliberately excludes (fresh model I/O only). */
    .slot-card-metric-sub {
      font-size: 0.65em;
      font-variant-numeric: tabular-nums;
      color: var(--vscode-descriptionForeground);
      opacity: 0.8;
      margin-top: 1px;
    }

    /* Queued cards */
    .slot-queued-wrap {
      border-top: 1px dashed var(--vscode-panel-border);
      padding-top: var(--spacing-sm);
    }

    .slot-queued-heading {
      margin: 0 0 var(--spacing-xs) 0;
      font-size: 0.8em;
      text-transform: uppercase;
      letter-spacing: 0.06em;
      color: var(--vscode-descriptionForeground);
    }

    .slot-queued-list {
      list-style: none;
      margin: 0;
      padding: 0;
      display: flex;
      flex-direction: column;
      gap: var(--spacing-xs);
    }

    .slot-queued-card {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      padding: var(--spacing-xs) var(--spacing-sm);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      background: var(--vscode-editorWidget-background);
    }

    .slot-queued-card.blocked {
      border-color: var(--vscode-editorWarning-foreground);
      opacity: 0.85;
    }

    .slot-queued-position {
      flex: 0 0 32px;
      text-align: center;
      font-size: 0.85em;
      font-weight: 600;
      color: var(--vscode-descriptionForeground);
    }

    .slot-queued-body {
      flex: 1;
      min-width: 0;
    }

    .slot-queued-title {
      display: flex;
      align-items: center;
      gap: var(--spacing-xs);
      overflow: hidden;
    }

    .slot-priority-badge {
      font-size: 0.65em;
      font-weight: 700;
      color: var(--vscode-editor-background);
      padding: 1px 5px;
      border-radius: 3px;
      letter-spacing: 0.04em;
    }

    .slot-queued-issue {
      font-weight: 600;
      font-size: 0.85em;
      color: var(--vscode-foreground);
    }

    .slot-queued-text {
      flex: 1;
      min-width: 0;
      font-size: 0.85em;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      color: var(--vscode-foreground);
    }

    .slot-queued-meta {
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
      font-size: 0.75em;
      color: var(--vscode-descriptionForeground);
      margin-top: 2px;
    }

    .slot-queued-blocker {
      color: var(--vscode-editorWarning-foreground);
    }
  `;
}

export {
  getAdapterStatusWidgetHtml,
  getCurrentActivityWidgetHtml,
  getPipelineSlotsHtml,
  getPipelineSlotsSectionHtml,
  getSummaryCardsHtml,
  getSummaryCardsSectionHtml,
  getScopeToggleHtml,
  getAdapterStatusStyles,
};
