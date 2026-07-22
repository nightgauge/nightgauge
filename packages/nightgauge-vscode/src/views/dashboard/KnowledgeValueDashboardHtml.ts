/**
 * KnowledgeValueDashboardHtml — pure HTML renderer for the Knowledge Value
 * dashboard (#3600). Inline SVG charts + VSCode theme variables; no external
 * chart library (ADR-003).
 */

import type { KnowledgeMetricsResult } from "../../services/IpcClientBase";
import {
  computeDelta,
  formatDelta,
  hitRateBand,
  type KnowledgeValueState,
  type WindowDays,
} from "./KnowledgeValueDashboardTypes";

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function getNonce(): string {
  let text = "";
  const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  for (let i = 0; i < 32; i++) text += chars.charAt(Math.floor(Math.random() * chars.length));
  return text;
}

function bandColor(band: "green" | "yellow" | "red" | "neutral"): string {
  switch (band) {
    case "green":
      return "#22c55e";
    case "yellow":
      return "#eab308";
    case "red":
      return "#ef4444";
    default:
      return "var(--vscode-descriptionForeground)";
  }
}

export function getKnowledgeValueDashboardHtml(state: KnowledgeValueState): string {
  const nonce = getNonce();
  const csp = `default-src 'none'; style-src 'nonce-${nonce}'; script-src 'nonce-${nonce}';`;

  const body = renderBody(state);

  return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta http-equiv="Content-Security-Policy" content="${csp}">
  <title>Knowledge Value Dashboard</title>
  <style nonce="${nonce}">
    ${getStyles()}
  </style>
</head>
<body>
  <div class="kv-dashboard">
    ${renderHeader(state.windowDays, state.loading)}
    ${body}
  </div>
  <script nonce="${nonce}">
    const vscode = acquireVsCodeApi();
    function setWindow(days) {
      vscode.postMessage({ type: 'refresh', windowDays: Number(days) });
    }
    function refresh() {
      vscode.postMessage({ type: 'refresh', windowDays: ${state.windowDays} });
    }
  </script>
</body>
</html>`;
}

function renderHeader(windowDays: WindowDays, loading: boolean): string {
  const opts: WindowDays[] = [7, 30, 90];
  const buttons = opts
    .map(
      (d) =>
        `<button class="kv-window-btn${
          d === windowDays ? " kv-window-btn--active" : ""
        }" onclick="setWindow(${d})">${d}d</button>`
    )
    .join("");
  return `
  <div class="kv-header">
    <h1>Knowledge Value</h1>
    <div class="kv-controls">
      ${buttons}
      <button class="kv-refresh" onclick="refresh()" aria-label="Refresh">
        ${loading ? "Loading…" : "Refresh"}
      </button>
    </div>
  </div>`;
}

function renderBody(state: KnowledgeValueState): string {
  if (state.error) {
    return `<div class="kv-empty"><h2>Could not load metrics</h2><p>${escapeHtml(
      state.error
    )}</p></div>`;
  }
  if (!state.current) {
    return `<div class="kv-empty"><h2>Loading…</h2></div>`;
  }
  const r = state.current;
  if (r.status === "disabled") {
    return renderDisabledState();
  }
  if (r.status === "empty" || r.totals.events_in_range === 0) {
    return renderEmptyState();
  }

  const delta = computeDelta(state.current, state.prior);

  return `
  ${renderHeaderCards(r, delta)}
  ${renderHitRateGauge(r)}
  ${renderPerStageChart(r)}
  ${renderTopRecalled(r)}
  ${renderStaleEntries(r)}
  ${renderGraduationHistory(r)}`;
}

function renderDisabledState(): string {
  return `
  <div class="kv-empty">
    <h2>Knowledge telemetry is disabled</h2>
    <p>
      Enable telemetry to populate this dashboard. Add the following to your
      <code>.nightgauge/config.yaml</code>:
    </p>
    <pre>knowledge:
  telemetry:
    enabled: true
    stale_days: 30</pre>
    <p>
      Once enabled, knowledge-base operations (reads, writes, recalls,
      graduations) emit events to
      <code>.nightgauge/pipeline/history/knowledge-events.jsonl</code>.
    </p>
  </div>`;
}

function renderEmptyState(): string {
  return `
  <div class="kv-empty">
    <h2>No knowledge activity yet</h2>
    <p>
      Telemetry is enabled but no events were recorded in the selected window.
      Run a pipeline stage or a <code>knowledge</code> subcommand to populate
      this dashboard.
    </p>
  </div>`;
}

function renderHeaderCards(
  r: KnowledgeMetricsResult,
  delta: ReturnType<typeof computeDelta>
): string {
  const cards = [
    { label: "Writes", value: r.totals.writes, delta: delta?.writes ?? null },
    { label: "Reads", value: r.totals.reads, delta: delta?.reads ?? null },
    { label: "Recalls", value: r.totals.recalls, delta: delta?.recalls ?? null },
    { label: "Hits", value: r.totals.recall_hits, delta: delta?.recall_hits ?? null },
    { label: "Graduations", value: r.totals.graduations, delta: delta?.graduations ?? null },
  ];
  return `
  <section class="kv-cards">
    ${cards
      .map(
        (c) => `
      <div class="kv-card">
        <div class="kv-card-label">${escapeHtml(c.label)}</div>
        <div class="kv-card-value">${c.value}</div>
        <div class="kv-card-delta" data-delta="${c.delta ?? ""}">${escapeHtml(formatDelta(c.delta))}</div>
      </div>`
      )
      .join("")}
  </section>`;
}

function renderHitRateGauge(r: KnowledgeMetricsResult): string {
  const band = hitRateBand(r.hit_rate ?? null);
  const color = bandColor(band);
  const pct = r.hit_rate !== undefined && r.hit_rate !== null ? Math.round(r.hit_rate * 100) : null;
  const display = pct === null ? "—" : `${pct}%`;
  const fillWidth = pct === null ? 0 : Math.max(2, Math.min(100, pct));

  return `
  <section class="kv-section">
    <h2>Hit Rate</h2>
    <div class="kv-gauge">
      <svg width="320" height="64" viewBox="0 0 320 64" role="img" aria-label="Hit rate gauge">
        <rect x="0" y="24" width="320" height="16" fill="var(--vscode-editorWidget-background)" />
        <rect x="0" y="24" width="${(fillWidth / 100) * 320}" height="16" fill="${color}" />
        <text x="160" y="58" text-anchor="middle" font-size="12" fill="var(--vscode-descriptionForeground)">
          recall_hits / recalls = ${r.totals.recall_hits} / ${r.totals.recalls}
        </text>
      </svg>
      <span class="kv-gauge-value" style="color: ${color}">${escapeHtml(display)}</span>
    </div>
  </section>`;
}

function renderPerStageChart(r: KnowledgeMetricsResult): string {
  if (r.per_stage.length === 0) {
    return `<section class="kv-section"><h2>Per Stage</h2><p>No per-stage activity in window.</p></section>`;
  }
  const maxCount = Math.max(1, ...r.per_stage.map((p) => Math.max(p.reads, p.writes)));
  const rowHeight = 28;
  const chartHeight = r.per_stage.length * rowHeight + 20;
  const barMax = 180;
  const labelOffset = 140;

  const rows = r.per_stage
    .map((p, i) => {
      const y = 10 + i * rowHeight;
      const readW = (p.reads / maxCount) * barMax;
      const writeW = (p.writes / maxCount) * barMax;
      return `
      <text x="${labelOffset - 8}" y="${y + 14}" text-anchor="end" font-size="11"
            fill="var(--vscode-foreground)">${escapeHtml(p.stage)}</text>
      <rect x="${labelOffset}" y="${y + 2}" width="${readW}" height="10"
            fill="var(--vscode-charts-blue)" />
      <rect x="${labelOffset}" y="${y + 14}" width="${writeW}" height="10"
            fill="var(--vscode-charts-green)" />
      <text x="${labelOffset + Math.max(readW, writeW) + 8}" y="${y + 16}" font-size="10"
            fill="var(--vscode-descriptionForeground)">r=${p.reads} w=${p.writes}</text>`;
    })
    .join("\n");

  return `
  <section class="kv-section">
    <h2>Per Stage</h2>
    <svg width="${labelOffset + barMax + 80}" height="${chartHeight}" role="img" aria-label="Per-stage reads and writes">
      ${rows}
    </svg>
    <div class="kv-legend">
      <span><span class="kv-swatch" style="background: var(--vscode-charts-blue)"></span>Reads</span>
      <span><span class="kv-swatch" style="background: var(--vscode-charts-green)"></span>Writes</span>
    </div>
  </section>`;
}

function renderTopRecalled(r: KnowledgeMetricsResult): string {
  if (r.top_recalled.length === 0) {
    return `<section class="kv-section"><h2>Top Recalled</h2><p>No read or recall_hit events in window.</p></section>`;
  }
  const rows = r.top_recalled
    .map(
      (e) => `
      <tr><td class="kv-num">${e.hits}</td><td>${escapeHtml(e.path)}</td></tr>`
    )
    .join("");
  return `
  <section class="kv-section">
    <h2>Top Recalled</h2>
    <table class="kv-table">
      <thead><tr><th class="kv-num">Hits</th><th>Path</th></tr></thead>
      <tbody>${rows}</tbody>
    </table>
  </section>`;
}

function renderStaleEntries(r: KnowledgeMetricsResult): string {
  if (r.stale_entries.length === 0) {
    return `<section class="kv-section"><h2>Stale Entries (>${r.stale_days}d)</h2><p>No stale entries.</p></section>`;
  }
  const rows = r.stale_entries
    .map(
      (e) => `
      <tr>
        <td class="kv-num">${e.days_since_touch}d</td>
        <td>${escapeHtml(e.last_touched_at ?? "never")}</td>
        <td>${escapeHtml(e.path)}</td>
      </tr>`
    )
    .join("");
  return `
  <section class="kv-section">
    <h2>Stale Entries (>${r.stale_days}d)</h2>
    <table class="kv-table">
      <thead><tr><th class="kv-num">Age</th><th>Last touched</th><th>Path</th></tr></thead>
      <tbody>${rows}</tbody>
    </table>
  </section>`;
}

function renderGraduationHistory(r: KnowledgeMetricsResult): string {
  if (r.graduation_history.length === 0) {
    return `<section class="kv-section"><h2>Graduations</h2><p>No graduations in window.</p></section>`;
  }
  const rows = r.graduation_history
    .map(
      (e) => `
      <tr>
        <td>${escapeHtml(e.timestamp)}</td>
        <td>${e.issue_number ? `#${e.issue_number}` : "—"}</td>
        <td>${escapeHtml(e.mode)}</td>
        <td>${escapeHtml(e.path ?? "")}</td>
      </tr>`
    )
    .join("");
  return `
  <section class="kv-section">
    <h2>Graduations</h2>
    <table class="kv-table">
      <thead><tr><th>When</th><th>Issue</th><th>Mode</th><th>Path</th></tr></thead>
      <tbody>${rows}</tbody>
    </table>
  </section>`;
}

function getStyles(): string {
  return `
    body {
      font-family: var(--vscode-font-family);
      background: var(--vscode-editor-background);
      color: var(--vscode-foreground);
      margin: 0;
      padding: 16px;
    }
    .kv-dashboard { max-width: 1000px; margin: 0 auto; }
    .kv-header {
      display: flex; justify-content: space-between; align-items: center;
      margin-bottom: 16px;
    }
    .kv-header h1 { margin: 0; font-size: 18px; }
    .kv-controls { display: flex; gap: 8px; align-items: center; }
    .kv-window-btn, .kv-refresh {
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
      border: 1px solid var(--vscode-button-border, transparent);
      padding: 4px 10px;
      cursor: pointer;
      border-radius: 2px;
      font-size: 12px;
    }
    .kv-window-btn--active {
      background: var(--vscode-button-background);
      color: var(--vscode-button-foreground);
    }
    .kv-cards {
      display: grid;
      grid-template-columns: repeat(5, minmax(0, 1fr));
      gap: 12px;
      margin-bottom: 20px;
    }
    .kv-card {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-widget-border, transparent);
      border-radius: 4px;
      padding: 12px;
    }
    .kv-card-label { font-size: 11px; color: var(--vscode-descriptionForeground); text-transform: uppercase; letter-spacing: 0.04em; }
    .kv-card-value { font-size: 28px; font-weight: 600; margin-top: 4px; }
    .kv-card-delta { font-size: 11px; color: var(--vscode-descriptionForeground); margin-top: 2px; }
    .kv-section {
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-widget-border, transparent);
      border-radius: 4px;
      padding: 12px 16px;
      margin-bottom: 14px;
    }
    .kv-section h2 { margin: 0 0 10px 0; font-size: 13px; text-transform: uppercase; letter-spacing: 0.04em; color: var(--vscode-descriptionForeground); }
    .kv-gauge { display: flex; align-items: center; gap: 16px; }
    .kv-gauge-value { font-size: 24px; font-weight: 600; }
    .kv-legend { font-size: 11px; color: var(--vscode-descriptionForeground); margin-top: 6px; display: flex; gap: 16px; }
    .kv-swatch { display: inline-block; width: 10px; height: 10px; margin-right: 4px; vertical-align: middle; border-radius: 2px; }
    .kv-table { width: 100%; border-collapse: collapse; font-size: 12px; }
    .kv-table th, .kv-table td { padding: 4px 8px; text-align: left; border-bottom: 1px solid var(--vscode-widget-border, transparent); }
    .kv-num { text-align: right; font-variant-numeric: tabular-nums; }
    .kv-empty { padding: 32px 16px; text-align: center; color: var(--vscode-descriptionForeground); }
    .kv-empty pre { text-align: left; background: var(--vscode-textCodeBlock-background); padding: 10px; border-radius: 4px; display: inline-block; }
    code { background: var(--vscode-textCodeBlock-background); padding: 1px 4px; border-radius: 2px; }
  `;
}
