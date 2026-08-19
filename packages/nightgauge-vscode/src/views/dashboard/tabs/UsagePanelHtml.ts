/**
 * UsagePanelHtml — the dashboard webview's adapter usage & quota panel
 * (Issue #661).
 *
 * Renders the `UsagePanelState` derived in `../usagePanel.ts`. Pure string
 * building: every number, date and flag it prints was decided there, so the
 * panel's behaviour is unit-testable without a webview.
 *
 * Two rendering rules are load-bearing rather than cosmetic, both from
 * docs/decisions/018-adapter-usage-quota-model.md:
 *
 * 1. **A window with no ceiling gets no bar at all** — an absolute figure and
 *    "no limit configured". A zero-width or full bar would imply a percentage
 *    against a limit nobody knows.
 * 2. **A window whose `used` is a floor (`confidence: "unknown"`) never
 *    renders a proportional bar** — it renders an explicitly indeterminate
 *    one. A proportional bar drawn from a floor reads as a measurement, and at
 *    `used: 0` it would be pixel-identical to "you have spent nothing".
 *
 * Value formatting comes from `services/usage/format` — the same function the
 * status-bar meter prints with, rather than a restatement of the per-unit
 * rules here. The two surfaces must render one figure one way.
 *
 * @see Issue #661 - Adapter usage & quota panel in the dashboard webview
 */

import { escapeHtml, formatDuration, formatTokenCount } from "../DashboardComponents";
import { formatUsageValue } from "../../../services/usage/format";
import type { UsagePlanKind } from "../../../services/usage/types";
import type {
  UsagePanelBurnRateView,
  UsagePanelFamilyGroup,
  UsagePanelRunView,
  UsagePanelState,
  UsagePanelWindowView,
} from "../usagePanel";

/** Bar colour class per severity. `unknown` is indeterminate, never proportional. */
const SEVERITY_BAR_CLASS: Record<string, string> = {
  ok: "usage-bar-ok",
  warning: "usage-bar-warning",
  critical: "usage-bar-critical",
  unknown: "usage-bar-unknown",
};

/** Confidence badge shown beside every window label. */
function confidenceBadge(view: UsagePanelWindowView): string {
  switch (view.confidence) {
    case "measured":
      return '<span class="badge badge-success" title="Every contributing record carried a reported figure">Measured</span>';
    case "estimated":
      return '<span class="badge badge-warning" title="Either a contributing record was priced from the local rate card, or the vendor figure is a cached last-seen reading rather than a live one">Estimated</span>';
    case "unknown":
      return '<span class="badge badge-danger" title="At least one contributing record could not be priced — this figure is a floor, not a total">Unknown</span>';
  }
}

/** "Aug 19, 2026, 12:00 AM (in 8h 12m)", or the honest absence of a reset. */
function resetText(view: UsagePanelWindowView, now: Date): string {
  if (view.resetsAt === null) {
    return "No scheduled reset";
  }
  const remainingMs = view.resetsAt.getTime() - now.getTime();
  const relative = remainingMs > 0 ? ` (in ${formatDuration(remainingMs)})` : "";
  return `Resets ${escapeHtml(view.resetsAt.toLocaleString())}${relative}`;
}

/**
 * "as of Aug 18, 2026, 2:05 PM" for a cached vendor reading (Issue #709), or
 * nothing.
 *
 * Only for a figure the provider is serving from cache — a `measured` one
 * describes the moment the snapshot was taken and `capturedAt` already says
 * when that was.
 */
function asOfText(view: UsagePanelWindowView): string {
  if (view.observedAt === undefined || view.confidence === "measured") {
    return "";
  }
  return `<span class="usage-as-of">Reported as of ${escapeHtml(view.observedAt.toLocaleString())}</span>`;
}

/** The used/limit line: a floor is stated as one, a missing ceiling as one. */
function figureText(view: UsagePanelWindowView): string {
  const used = formatUsageValue(view.used, view.unit);
  const usedText = view.usedIsFloor ? `at least ${used}` : used;
  if (view.limit === null || view.limit <= 0) {
    return `${escapeHtml(usedText)} used &mdash; <em>no limit configured</em>`;
  }
  // A vendor-reported percentage is already against 100, so "44% of 100%
  // (44%)" would print the same number three times. What the operator on a
  // subscription plan is asking is how much is left (Issue #709).
  if (view.unit === "percent") {
    const remaining = Math.max(0, view.limit - view.used);
    return `${escapeHtml(usedText)} used &mdash; ${escapeHtml(formatUsageValue(remaining, view.unit))} remaining`;
  }
  const limit = formatUsageValue(view.limit, view.unit);
  const pct =
    view.pct === null ? "" : ` (${view.usedIsFloor ? "&ge;" : ""}${Math.round(view.pct)}%)`;
  return `${escapeHtml(usedText)} of ${escapeHtml(limit)}${pct}`;
}

/**
 * The bar for one window, or nothing.
 *
 * Emitted only when there is something honest to fill it with: a real ceiling,
 * and a `used` that is a total rather than a floor. The indeterminate variant
 * carries no inline width — its fill comes from CSS — so it cannot degenerate
 * into an empty bar at `used: 0`.
 */
function windowBarHtml(view: UsagePanelWindowView): string {
  // Rule 1: no ceiling, no bar. The figure stands on its own.
  if (view.barPct === null) {
    return "";
  }
  // Rule 2: a ceiling exists but `used` is a floor — indeterminate fill.
  if (view.severity === "unknown") {
    return `<div class="usage-progress-track"><div class="usage-progress-bar usage-bar-unknown" title="Usage is a floor, not a total"></div></div>`;
  }
  const barClass = SEVERITY_BAR_CLASS[view.severity] ?? "usage-bar-ok";
  return `<div class="usage-progress-track"><div class="usage-progress-bar ${barClass}" style="width: ${view.barPct.toFixed(1)}%"></div></div>`;
}

/** One window row: label, confidence badge, figures, bar (when honest), reset. */
function windowRowHtml(view: UsagePanelWindowView, now: Date): string {
  return `
        <div class="usage-window-row" data-window-id="${escapeHtml(view.id)}" data-severity="${view.severity}">
          <div class="usage-limits-row">
            <span class="usage-label">${escapeHtml(view.label)} ${confidenceBadge(view)}</span>
            <span class="usage-value">${figureText(view)}</span>
          </div>
          ${windowBarHtml(view)}
          <div class="usage-limits-row usage-limits-meta">
            <span class="usage-remaining">${resetText(view, now)}</span>
            ${asOfText(view)}
          </div>
        </div>`;
}

/**
 * Per-model-family breakdown, or nothing at all.
 *
 * Omitted cleanly — no heading, no empty container — when the snapshot carries
 * no `modelFamily` windows, which is every snapshot either provider produces:
 * local telemetry has no per-family limit, and the Claude `rate_limit_event`
 * channel names a window rather than a model (Issue #709). ADR 018 keeps
 * `modelFamily` reserved for a provider that really does bucket per family.
 */
function familyBreakdownHtml(groups: readonly UsagePanelFamilyGroup[], now: Date): string {
  if (groups.length === 0) {
    return "";
  }
  const sections = groups
    .map(
      (group) => `
        <div class="usage-family-group">
          <h4 class="usage-family-heading">${escapeHtml(group.modelFamily)}</h4>
          ${group.windows.map((view) => windowRowHtml(view, now)).join("")}
        </div>`
    )
    .join("");
  return `
      <div class="usage-family-breakdown">
        <h4 class="usage-subheading">Per-model breakdown</h4>
        ${sections}
      </div>`;
}

/** Burn rate, projected exhaustion, and the attribution caveat behind both. */
function burnRateHtml(state: UsagePanelState, now: Date): string {
  const caveat = `<p class="usage-panel-note">Rate is measured from all pipeline runs in the last ${state.lookbackDays} days. Run history carries no adapter attribution, so on a workspace that runs more than one adapter this is an upper bound and the projection is early.</p>`;

  const rate: UsagePanelBurnRateView | null = state.burnRate;
  if (rate === null) {
    // Two messages, not three: the third derivation outcome ("no-window")
    // only arises on an unknown plan, which returns the empty state above and
    // never reaches this card. A branch this function cannot execute would be
    // a message nobody can ever read.
    const message =
      state.burnRateUnavailableReason === "non-dollar-window"
        ? "Burn rate is measured in dollars from run history, which cannot describe this window's unit."
        : `Not enough recent history to state a burn rate — needs at least two runs in the last ${state.lookbackDays} days.`;
    return `
      <div class="usage-burn-card">
        <h4 class="usage-subheading">Burn rate</h4>
        <p class="usage-value">${escapeHtml(message)}</p>
      </div>`;
  }

  let projection: string;
  if (rate.alreadyExhausted) {
    projection = `<strong>${escapeHtml(rate.windowLabel)}</strong> is already at or past its ceiling.`;
  } else if (rate.projectedExhaustionAt === null) {
    projection =
      rate.limitUsd === null
        ? `No ceiling is configured for <strong>${escapeHtml(rate.windowLabel)}</strong> — there is nothing to exhaust.`
        : `No spend observed in the last ${rate.lookbackDays} days, so <strong>${escapeHtml(rate.windowLabel)}</strong> has no projected exhaustion.`;
  } else {
    const horizonMs = rate.projectedExhaustionAt.getTime() - now.getTime();
    projection =
      `At this rate <strong>${escapeHtml(rate.windowLabel)}</strong> reaches its ` +
      `${escapeHtml(formatUsageValue(rate.limitUsd ?? 0, "usd"))} ceiling on ` +
      `${escapeHtml(rate.projectedExhaustionAt.toLocaleString())} ` +
      `(in ${escapeHtml(formatDuration(Math.max(0, horizonMs)))}).`;
  }

  const floorNote = rate.usedIsFloor
    ? '<p class="usage-panel-note">This window\'s usage is a floor, not a total, so the projection is optimistic.</p>'
    : "";

  return `
      <div class="usage-burn-card">
        <h4 class="usage-subheading">Burn rate</h4>
        <div class="usage-limits-row">
          <span class="usage-label">Current rate</span>
          <span class="usage-value">$${rate.usdPerHour.toFixed(2)}/hour &middot; $${rate.usdPerDay.toFixed(2)}/day</span>
        </div>
        <div class="usage-limits-row">
          <span class="usage-label">Projection</span>
          <span class="usage-value">${projection}</span>
        </div>
        <div class="usage-limits-row usage-limits-meta">
          <span class="usage-remaining">${rate.sampleCount} run${rate.sampleCount === 1 ? "" : "s"} in the last ${rate.lookbackDays} days</span>
        </div>
        ${floorNote}
        ${caveat}
      </div>`;
}

/** The last N runs, with the spend and tokens each one cost. */
function recentRunsHtml(state: UsagePanelState): string {
  if (state.recentRuns.length === 0) {
    return `
      <div class="usage-recent-card">
        <h4 class="usage-subheading">Recent runs</h4>
        <p class="usage-value">No pipeline runs recorded yet.</p>
      </div>`;
  }

  const rows = state.recentRuns
    .map(
      (run: UsagePanelRunView) => `
            <tr>
              <td>#${run.issueNumber} ${escapeHtml(run.title)}</td>
              <td>${escapeHtml(run.startedAt.toLocaleString())}</td>
              <td class="usage-numeric">$${run.costUsd.toFixed(2)}</td>
              <td class="usage-numeric">${escapeHtml(formatTokenCount(run.tokens))}</td>
            </tr>`
    )
    .join("");

  return `
      <div class="usage-recent-card">
        <h4 class="usage-subheading">Recent runs</h4>
        <table class="usage-recent-table">
          <thead>
            <tr><th>Run</th><th>Started</th><th>Spend</th><th>Tokens</th></tr>
          </thead>
          <tbody>${rows}</tbody>
          <tfoot>
            <tr>
              <td>Last ${state.recentTotals.runCount} run${state.recentTotals.runCount === 1 ? "" : "s"}</td>
              <td></td>
              <td class="usage-numeric">$${state.recentTotals.costUsd.toFixed(2)}</td>
              <td class="usage-numeric">${escapeHtml(formatTokenCount(state.recentTotals.tokens))}</td>
            </tr>
          </tfoot>
        </table>
      </div>`;
}

/**
 * Render the adapter usage & quota panel.
 *
 * Returns `""` when `state` is null — there is no usage service at all (no
 * workspace root), so there is not even an adapter to name. A snapshot whose
 * plan is `"unknown"` is a different thing entirely and renders an explicit
 * empty state that names the adapter, because "nothing can describe this
 * adapter" is an answer and hiding it would let the operator read the absence
 * as zero usage.
 */
export function getUsagePanelSectionHtml(
  state: UsagePanelState | null,
  now: Date = new Date()
): string {
  if (state === null) {
    return "";
  }

  const captured = `<p class="usage-panel-note">Snapshot captured ${escapeHtml(state.capturedAt.toLocaleString())} for adapter <strong>${escapeHtml(state.adapter)}</strong>.</p>`;

  if (state.planKind === "unknown") {
    return `
  <details class="collapsible-section" id="section-adapter-usage" open>
    <summary class="section-toggle">
      <span class="toggle-icon">▼</span>
      <h3>Adapter Usage &amp; Quota <span class="badge badge-warning">Unknown</span></h3>
    </summary>
    <div class="section-content">
      <div class="usage-limits-card">
        <p class="usage-value">No usage provider can describe the <strong>${escapeHtml(state.adapter)}</strong> adapter, so its usage is <strong>unknown</strong> &mdash; not zero.</p>
        <p class="usage-panel-note">Local telemetry meters adapters billed per token. Adapters that run on your own hardware, or that bill a flat seat subscription, have no dollar meter to report, and nightgauge will not draw a bar it cannot fill honestly.</p>
        ${captured}
      </div>
    </div>
  </details>`;
  }

  // No empty-list fallback: a snapshot with no windows is exactly the unknown
  // plan (ADR 018 — "Empty exactly when plan.kind === 'unknown'"), and that
  // returned above.
  const windows = state.windows.map((view) => windowRowHtml(view, now)).join("");

  return `
  <details class="collapsible-section" id="section-adapter-usage" open>
    <summary class="section-toggle">
      <span class="toggle-icon">▼</span>
      <h3>Adapter Usage &amp; Quota &mdash; ${escapeHtml(state.adapter)} ${planBadgeHtml(state.planKind)}</h3>
    </summary>
    <div class="section-content">
      <div class="usage-limits-card">
        ${windows}
      </div>
      ${claudeFeedPromptHtml(state)}
      ${familyBreakdownHtml(state.familyGroups, now)}
      ${burnRateHtml(state, now)}
      ${recentRunsHtml(state)}
      ${captured}
    </div>
  </details>`;
}

/**
 * Badge naming the billing arrangement the windows below describe.
 *
 * Two window lists can look identical — a bar, a figure, a reset time — and
 * mean completely different things: a subscription window is an allowance that
 * refills, a pay-per-token window is spend that accumulates against a budget
 * the operator set. Naming which one is on screen is the difference between
 * "62% of my week is gone" and "$178.61 of an open-ended total".
 *
 * `unknown` never reaches here — that plan renders its own empty state above.
 */
function planBadgeHtml(planKind: UsagePlanKind): string {
  if (planKind === "subscription-window") {
    return '<span class="badge badge-info">Subscription plan</span>';
  }
  return '<span class="badge badge-muted">Pay per token</span>';
}

/**
 * Offer the Claude Max feed when the `claude` adapter is answering with
 * anything other than a subscription window (Issue #730).
 *
 * The webview counterpart of the status-bar tooltip's link, and shown under the
 * same condition — on the *observed plan kind*, never on the adapter name. An
 * API-key user on the same adapter really is pay-per-token and the dollar
 * windows above are their right answer, so this asks rather than asserts.
 */
function claudeFeedPromptHtml(state: UsagePanelState): string {
  if (state.adapter !== "claude" || state.planKind === "subscription-window") {
    return "";
  }
  return `
      <p class="usage-panel-note usage-feed-prompt">
        On a Claude Max or Pro plan? These are locally-derived costs, not your plan's allowance.
        <button type="button" id="enableClaudeUsageFeed" class="link-button">Show my 5-hour and weekly limits</button>
      </p>`;
}

/**
 * Panel-specific styles. The bar track/fill classes are shared with the
 * pre-existing usage section (`getCostTabStyles`); only what this panel adds
 * is defined here.
 */
export function getUsagePanelStyles(): string {
  return `
    /* Adapter Usage & Quota panel (Issue #661) */

    /*
     * Plan badge (Issue #730). badge-info is already defined by the cost tab,
     * whose styles ship on the same page; only the pay-per-token variant is new,
     * and it is deliberately quieter — a subscription window is the state worth
     * drawing the eye to, since it is the one an operator can run out of.
     */
    .badge-muted {
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
      border-radius: 3px;
      padding: 1px 6px;
      font-size: 0.8em;
    }

    /*
     * The "wire up my plan's limits" call to action (Issue #730). Styled as a
     * link rather than a button: it sits inside a sentence, and it opens a
     * confirmation dialog rather than doing anything on its own.
     */
    .usage-feed-prompt .link-button {
      background: none;
      border: none;
      padding: 0;
      font: inherit;
      color: var(--vscode-textLink-foreground);
      cursor: pointer;
      text-decoration: underline;
    }

    .usage-feed-prompt .link-button:hover {
      color: var(--vscode-textLink-activeForeground);
    }

    .usage-window-row {
      padding: var(--spacing-sm) 0;
      border-bottom: 1px solid var(--vscode-panel-border, rgba(128,128,128,0.15));
    }

    .usage-window-row:last-child {
      border-bottom: none;
    }

    .usage-subheading {
      margin: 0 0 var(--spacing-sm) 0;
      font-size: 0.95em;
      color: var(--vscode-foreground);
    }

    /*
     * As-of stamp for a cached vendor reading (Issue #709). Same muted weight
     * as the reset text beside it: it qualifies the figure rather than
     * competing with it.
     */
    .usage-as-of {
      color: var(--vscode-descriptionForeground);
      font-size: 0.85em;
    }

    .usage-family-heading {
      margin: var(--spacing-sm) 0 0 0;
      font-size: 0.85em;
      color: var(--vscode-descriptionForeground);
      text-transform: uppercase;
      letter-spacing: 0.04em;
    }

    .usage-family-breakdown,
    .usage-burn-card,
    .usage-recent-card {
      margin-top: var(--spacing-md);
      padding: var(--spacing-md);
      background: var(--vscode-editor-background);
      border: 1px solid var(--vscode-panel-border, rgba(128,128,128,0.2));
      border-radius: var(--border-radius);
    }

    /*
     * Indeterminate fill for a window whose usage is a floor. Full width and
     * striped on purpose: a proportional fill drawn from a floor would read as
     * a measurement, and at zero it would be indistinguishable from an empty
     * bar (docs/decisions/018-adapter-usage-quota-model.md).
     */
    .usage-bar-unknown {
      width: 100%;
      background: repeating-linear-gradient(
        45deg,
        var(--vscode-descriptionForeground, #888),
        var(--vscode-descriptionForeground, #888) 6px,
        transparent 6px,
        transparent 12px
      );
      opacity: 0.6;
    }

    .usage-panel-note {
      margin-top: var(--spacing-sm);
      font-size: 0.8em;
      color: var(--vscode-descriptionForeground);
    }

    .usage-recent-table {
      width: 100%;
      border-collapse: collapse;
      font-size: 0.85em;
    }

    .usage-recent-table th,
    .usage-recent-table td {
      padding: 4px 8px;
      text-align: left;
      border-bottom: 1px solid var(--vscode-panel-border, rgba(128,128,128,0.15));
    }

    .usage-recent-table tfoot td {
      font-weight: 600;
      border-bottom: none;
    }

    .usage-numeric {
      text-align: right;
      font-variant-numeric: tabular-nums;
    }
  `;
}
