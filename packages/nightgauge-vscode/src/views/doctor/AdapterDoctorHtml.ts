/**
 * AdapterDoctorHtml — pure renderer for the Adapter Doctor webview (Issue #4031).
 *
 * Renders a per-adapter health table (binary/version/auth/MCP + remediation) and
 * a per-stage resolution table (which adapter + model each pipeline stage resolves
 * to, and whether that adapter is ready). Kept framework-free and side-effect-free
 * so it can be unit-tested without a VSCode host.
 *
 * Data sources, merged by the command layer:
 *   - Go `nightgauge doctor --adapters … --json` → binary/version/MCP facts.
 *   - SDK `runAdapterAuthPreflight` → auth verdict + actionable `suggestedFix`.
 *   - TS resolvers (`resolveStageAdapter`, `getStageModel`) → per-stage routing.
 */

/** Codex MCP managed-block state (codex only). */
export interface AdapterMcpReport {
  configPath: string;
  configPresent: boolean;
  managedBlock: boolean;
}

/** One adapter's merged health row. Keyed by the resolved SDK adapter name. */
export interface AdapterReportRow {
  sdkAdapter: string;
  displayName: string;
  kind: string; // "cli" | "sdk" | "http" | "unknown"
  binary?: string;
  installed: boolean;
  path?: string;
  version?: string;
  versionOk: boolean;
  minVersion?: string;
  mcp?: AdapterMcpReport;
  authOk: boolean;
  authReason?: string;
  remediations: string[];
  ok: boolean;
}

/** One pipeline stage's resolved adapter + model. */
export interface StageResolutionRow {
  stage: string;
  adapter: string; // extension ExecutionAdapter id
  sdkAdapter: string;
  source: string;
  model: string; // tier, or "(auto / router)" when deferred
  codexModel?: string; // concrete Codex model id when the adapter is codex
  status: "ok" | "error" | "warn" | "unknown";
}

/** The full report handed to the panel. */
export interface AdapterDoctorReport {
  rows: AdapterReportRow[];
  stages: StageResolutionRow[];
  generatedAt: string;
  /** False when the Go binary could not be resolved — binary/version/MCP are then unknown. */
  binaryResolved: boolean;
  notes: string[];
}

function esc(value: unknown): string {
  return String(value ?? "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

function statusIcon(ok: boolean): string {
  return ok ? "✓" : "✗";
}

function statusClass(ok: boolean): string {
  return ok ? "ok" : "error";
}

function renderVersionCell(row: AdapterReportRow, binaryResolved: boolean): string {
  if (!binaryResolved) return `<span class="muted">unknown</span>`;
  if (row.kind === "sdk")
    return row.installed ? "API key set" : `<span class="muted">no API key</span>`;
  if (row.kind === "http")
    return row.installed ? "model configured" : `<span class="muted">model env unset</span>`;
  if (!row.installed) return `<span class="error">not on PATH</span>`;
  const v = row.version ? esc(row.version) : "unknown version";
  if (row.minVersion && !row.versionOk) {
    return `<span class="warn">${v} (min ${esc(row.minVersion)})</span>`;
  }
  return esc(v);
}

function renderMcpCell(row: AdapterReportRow): string {
  if (!row.mcp) return `<span class="muted">—</span>`;
  if (!row.mcp.configPresent) {
    return `<span class="muted">no config.toml</span>`;
  }
  return row.mcp.managedBlock
    ? `<span class="ok">MCP block ✓</span>`
    : `<span class="muted">no MCP block</span>`;
}

function renderAuthCell(row: AdapterReportRow): string {
  if (row.authOk) return `<span class="ok">authenticated</span>`;
  const reason = row.authReason ? `<div class="reason">${esc(row.authReason)}</div>` : "";
  return `<span class="error">not authenticated</span>${reason}`;
}

function renderAdapterRows(report: AdapterDoctorReport): string {
  if (report.rows.length === 0) {
    return `<tr><td colspan="5" class="muted">No adapters configured across pipeline stages.</td></tr>`;
  }
  return report.rows
    .map((row) => {
      const remediation = row.remediations.length
        ? `<ul class="remediation">${row.remediations.map((r) => `<li>${esc(r)}</li>`).join("")}</ul>`
        : "";
      return `
      <tr data-adapter="${esc(row.sdkAdapter)}" class="adapter-row ${statusClass(row.ok)}">
        <td>
          <span class="status ${statusClass(row.ok)}">${statusIcon(row.ok)}</span>
          <strong>${esc(row.displayName)}</strong>
          <div class="muted small">${esc(row.sdkAdapter)} · ${esc(row.kind)}</div>
        </td>
        <td>${renderVersionCell(row, report.binaryResolved)}</td>
        <td>${renderAuthCell(row)}</td>
        <td>${renderMcpCell(row)}</td>
        <td>${remediation || `<span class="muted">—</span>`}</td>
      </tr>`;
    })
    .join("\n");
}

function renderStageRows(report: AdapterDoctorReport): string {
  if (report.stages.length === 0) {
    return `<tr><td colspan="4" class="muted">No pipeline stages resolved.</td></tr>`;
  }
  return report.stages
    .map((s) => {
      const model = s.codexModel
        ? `${esc(s.model)} <span class="muted small">→ ${esc(s.codexModel)}</span>`
        : esc(s.model);
      return `
      <tr data-stage="${esc(s.stage)}">
        <td><code>${esc(s.stage)}</code></td>
        <td>${esc(s.adapter)} <span class="muted small">(${esc(s.source)})</span></td>
        <td>${model}</td>
        <td><span class="status ${esc(s.status)}">${esc(s.status)}</span></td>
      </tr>`;
    })
    .join("\n");
}

/**
 * Webview hardening inputs (Issue #4031 review): the webview's CSP source and a
 * per-render script nonce, matching the strict `default-src 'none'` + nonced
 * inline-script pattern used by the other extension webviews (SettingsHtml,
 * RecoveryDialogHtml, …). Required so the panel — which renders strings sourced
 * from the filesystem and CLI/Go subprocess output — has defense-in-depth even
 * if a future edit forgets to escape a field.
 */
export interface AdapterDoctorHtmlSecurity {
  cspSource: string;
  nonce: string;
}

/**
 * Render the full Adapter Doctor webview HTML. Pure: identical input → identical
 * output, so the panel can re-render on refresh and tests can assert structure.
 */
export function renderAdapterDoctorHtml(
  report: AdapterDoctorReport,
  security: AdapterDoctorHtmlSecurity
): string {
  const csp = `default-src 'none'; style-src ${security.cspSource} 'unsafe-inline'; script-src 'nonce-${security.nonce}';`;
  const notes = report.notes.length
    ? `<div class="notes">${report.notes.map((n) => `<div class="note">ℹ ${esc(n)}</div>`).join("")}</div>`
    : "";

  const binaryWarning = report.binaryResolved
    ? ""
    : `<div class="note warn">⚠ The nightgauge Go binary could not be resolved — binary/version/MCP facts are unavailable; only auth status is shown.</div>`;

  return /* html */ `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta http-equiv="Content-Security-Policy" content="${csp}" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Adapter Doctor</title>
  <style>
    body {
      font-family: var(--vscode-font-family);
      color: var(--vscode-foreground);
      background: var(--vscode-editor-background);
      padding: 1.5rem;
      max-width: 1024px;
    }
    h1 { font-size: 1.4rem; margin-top: 0; }
    h2 { font-size: 1.1rem; margin-top: 2rem; }
    .meta { color: var(--vscode-descriptionForeground); font-size: 0.9rem; }
    .small { font-size: 0.85rem; }
    .muted { color: var(--vscode-descriptionForeground); }
    table { border-collapse: collapse; width: 100%; margin-top: 0.75rem; }
    th, td {
      text-align: left; padding: 0.5rem 0.6rem; vertical-align: top;
      border-bottom: 1px solid var(--vscode-widget-border);
    }
    th { font-weight: 600; color: var(--vscode-descriptionForeground); }
    .status { font-weight: 700; margin-right: 0.35rem; }
    .ok { color: var(--vscode-testing-iconPassed, #3fb950); }
    .error { color: var(--vscode-testing-iconFailed, #f85149); }
    .warn { color: var(--vscode-testing-iconQueued, #d29922); }
    .unknown { color: var(--vscode-descriptionForeground); }
    .reason { color: var(--vscode-descriptionForeground); font-size: 0.82rem; margin-top: 0.2rem; }
    ul.remediation { margin: 0; padding-left: 1.1rem; }
    ul.remediation li { font-size: 0.85rem; margin: 0.15rem 0; }
    .notes { margin-top: 1.25rem; }
    .note { color: var(--vscode-descriptionForeground); font-size: 0.85rem; margin: 0.2rem 0; }
    .note.warn { color: var(--vscode-testing-iconQueued, #d29922); }
    button {
      background: var(--vscode-button-background); color: var(--vscode-button-foreground);
      border: none; padding: 0.4rem 0.9rem; cursor: pointer; border-radius: 2px; font: inherit;
    }
    button:hover { background: var(--vscode-button-hoverBackground); }
    code { font-family: var(--vscode-editor-font-family); }
    .toolbar { display: flex; align-items: center; gap: 1rem; margin-top: 0.5rem; }
  </style>
</head>
<body>
  <h1>Adapter Doctor</h1>
  <p class="meta">Per-adapter readiness for pipeline execution — CLI install + version, authentication, Codex MCP, and per-stage routing.</p>
  <div class="toolbar">
    <button id="refresh" type="button">Re-run checks</button>
    <span class="meta">Last run: ${esc(report.generatedAt)}</span>
  </div>

  ${binaryWarning}

  <h2>Adapters</h2>
  <table id="adapter-table">
    <thead>
      <tr><th>Adapter</th><th>Install / Version</th><th>Auth</th><th>Codex MCP</th><th>How to fix</th></tr>
    </thead>
    <tbody>${renderAdapterRows(report)}</tbody>
  </table>

  <h2>Per-stage resolution</h2>
  <p class="meta">Adapter + model each stage resolves to today (model tiers resolve to a concrete provider model at runtime).</p>
  <table id="stage-table">
    <thead>
      <tr><th>Stage</th><th>Adapter (source)</th><th>Model</th><th>Status</th></tr>
    </thead>
    <tbody>${renderStageRows(report)}</tbody>
  </table>

  ${notes}

  <script nonce="${security.nonce}">
    const vscode = acquireVsCodeApi();
    document.getElementById('refresh').addEventListener('click', () => {
      vscode.postMessage({ type: 'refresh' });
    });
  </script>
</body>
</html>`;
}
