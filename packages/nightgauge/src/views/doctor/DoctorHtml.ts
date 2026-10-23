/**
 * DoctorHtml — pure renderer for the Doctor panel (ADR-025).
 *
 * One card per finding, grouped by severity, plus the `adapters` group for
 * adapter health. Side-effect free, so tests assert structure without a
 * VS Code host.
 *
 * Security (ADR-025 section 8):
 *   - CSP `default-src 'none'`, nonce-only script and style, no `unsafe-eval`,
 *     no remote resource.
 *   - Every finding string goes through `esc`. The inline script updates the
 *     page with `textContent` only; it never parses finding text as HTML.
 *   - The script posts only `{ type, fingerprint, remedyId }`; the panel
 *     validates each message and opens links itself, allowlisted hosts only.
 *
 * Accessibility: theme CSS variables only (light, dark, high contrast),
 * native buttons and `<details>` in reading order, severity as text, and
 * progress and outcomes in `aria-live="polite"` regions.
 */

import type {
  DoctorAdapterHealth,
  DoctorFinding,
  DoctorProgressEvent,
  DoctorRemedy,
  DoctorRunResult,
  DoctorSeverity,
} from "../../services/IpcClientBase";

/** Hosts a manual-remedy or docs link may open on (ADR-025 section 8). */
export const LINK_HOSTS: readonly string[] = ["github.com", "docs.github.com", "nightgauge.dev"];

/** Where a finding's `docs` path ("docs/DOCTOR.md#ngd017") is published. */
export const DOCS_BASE = "https://github.com/nightgauge/nightgauge/blob/main/";

export const FINGERPRINT_RE = /^[0-9a-f]{16}$/;
export const REMEDY_ID_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

/** Severity groups in display order. Adapter findings render in their own group. */
export const SEVERITY_ORDER: readonly DoctorSeverity[] = [
  "blocker",
  "warning",
  "housekeeping",
  "info",
];

const SEVERITY_LABEL: Record<DoctorSeverity, string> = {
  blocker: "Blocker",
  warning: "Warning",
  housekeeping: "Housekeeping",
  info: "Info",
};

const GROUP_HEADING: Record<DoctorSeverity, string> = {
  blocker: "Blockers",
  warning: "Warnings",
  housekeeping: "Housekeeping",
  info: "Info",
};

export interface DoctorViewState {
  phase: "running" | "ready" | "error";
  result?: DoctorRunResult;
  error?: string;
  /** `doctor.progress` events seen so far in this scan. */
  progress: DoctorProgressEvent[];
  /** When the shown result was produced, for display. */
  generatedAt?: string;
}

export interface DoctorHtmlSecurity {
  cspSource: string;
  nonce: string;
}

export function esc(value: unknown): string {
  return String(value ?? "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

/** True for an `https` URL on an allowlisted host, with no credentials in it. */
export function isAllowedLink(url: string): boolean {
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return false;
  }
  if (parsed.protocol !== "https:" || parsed.username || parsed.password || parsed.port) {
    return false;
  }
  return LINK_HOSTS.includes(parsed.hostname.toLowerCase());
}

/** The published URL of a finding's reference anchor, or undefined. */
export function docsUrl(finding: Pick<DoctorFinding, "docs" | "code">): string | undefined {
  if (/^docs\/DOCTOR\.md#[a-z0-9_-]+$/i.test(finding.docs ?? "")) {
    return DOCS_BASE + finding.docs;
  }
  if (/^NGD\d{3}$/.test(finding.code ?? "")) {
    return `${DOCS_BASE}docs/DOCTOR.md#${finding.code.toLowerCase()}`;
  }
  return undefined;
}

export function remediesOf(finding: DoctorFinding): DoctorRemedy[] {
  return (finding.remedies ?? []).filter(
    (r) => r && typeof r.id === "string" && REMEDY_ID_RE.test(r.id)
  );
}

/** The first allowlisted link of a manual remedy. */
export function firstAllowedLink(remedy: DoctorRemedy): string | undefined {
  return (remedy.links ?? []).find(isAllowedLink);
}

/** Findings in display groups: severities (without adapters) and adapters. */
export function groupFindings(findings: readonly DoctorFinding[]): {
  bySeverity: Record<DoctorSeverity, DoctorFinding[]>;
  adapters: DoctorFinding[];
} {
  const bySeverity: Record<DoctorSeverity, DoctorFinding[]> = {
    blocker: [],
    warning: [],
    housekeeping: [],
    info: [],
  };
  const adapters: DoctorFinding[] = [];
  for (const f of findings) {
    if (f.check === "adapters") adapters.push(f);
    else if (bySeverity[f.severity]) bySeverity[f.severity].push(f);
  }
  return { bySeverity, adapters };
}

/** Housekeeping findings "Fix all safe" applies: those with an auto remedy. */
export function safeFixes(findings: readonly DoctorFinding[]): Array<{
  finding: DoctorFinding;
  remedy: DoctorRemedy;
}> {
  const out: Array<{ finding: DoctorFinding; remedy: DoctorRemedy }> = [];
  for (const f of findings) {
    if (f.severity !== "housekeeping" || f.check === "adapters") continue;
    const remedy = remediesOf(f).find((r) => r.kind === "auto");
    if (remedy) out.push({ finding: f, remedy });
  }
  return out;
}

function plural(n: number, one: string, many = `${one}s`): string {
  return `${n} ${n === 1 ? one : many}`;
}

function button(
  label: string,
  action: string,
  accessibleName: string,
  fingerprint?: string,
  remedyId?: string,
  extraClass = ""
): string {
  const fp = fingerprint ? ` data-fingerprint="${esc(fingerprint)}"` : "";
  const rid = remedyId ? ` data-remedy="${esc(remedyId)}"` : "";
  const cls = extraClass ? ` class="${extraClass}"` : "";
  return `<button type="button"${cls} data-action="${esc(action)}"${fp}${rid} aria-label="${esc(accessibleName)}">${esc(label)}</button>`;
}

function renderEvidence(evidence: Record<string, string> | null | undefined): string {
  const entries = Object.entries(evidence ?? {}).sort(([a], [b]) => a.localeCompare(b));
  if (entries.length === 0) return `<dl class="evidence-list"></dl>`;
  return `<dl class="evidence-list">${entries
    .map(([k, v]) => `<dt>${esc(k)}</dt><dd>${esc(v)}</dd>`)
    .join("")}</dl>`;
}

function renderManual(remedy: DoctorRemedy): string {
  const steps = (remedy.steps ?? []).length
    ? `<ol class="steps">${(remedy.steps ?? []).map((s) => `<li>${esc(s)}</li>`).join("")}</ol>`
    : "";
  const links = (remedy.links ?? []).length
    ? `<ul class="links">${(remedy.links ?? [])
        .map((l) =>
          isAllowedLink(l)
            ? `<li>${esc(l)}</li>`
            : `<li>${esc(l)} <span class="muted">(not opened: host not allowlisted)</span></li>`
        )
        .join("")}</ul>`
    : "";
  return `<div class="manual"><p class="remedy-summary">${esc(remedy.summary)}</p>${steps}${links}</div>`;
}

function renderActions(finding: DoctorFinding): string {
  const fp = finding.fingerprint;
  const buttons: string[] = [];
  for (const r of remediesOf(finding)) {
    if (r.kind === "auto") {
      buttons.push(button("Fix", "fix", `Fix ${finding.code}: ${r.summary}`, fp, r.id, "primary"));
    } else if (r.kind === "confirm") {
      buttons.push(
        button("Review & fix", "review", `Review and fix ${finding.code}: ${r.summary}`, fp, r.id)
      );
    } else if (r.kind === "manual" && firstAllowedLink(r)) {
      buttons.push(
        button("Open", "open", `Open the link for ${finding.code}: ${r.summary}`, fp, r.id)
      );
    }
  }
  buttons.push(
    button("Check again", "recheck", `Check ${finding.code} again`, fp, undefined, "secondary")
  );
  return `<div class="actions">${buttons.join("")}</div>`;
}

/** One finding's card. Exported for tests. */
export function renderCard(finding: DoctorFinding): string {
  const fp = FINGERPRINT_RE.test(finding.fingerprint) ? finding.fingerprint : "";
  const sev = SEVERITY_LABEL[finding.severity] ? finding.severity : "info";
  const titleId = `title-${fp}`;
  const url = docsUrl(finding);
  const code = url
    ? `<a class="code" href="${esc(url)}" data-action="openDocs" data-fingerprint="${esc(fp)}" aria-label="${esc(`${finding.code} reference`)}">${esc(finding.code)}</a>`
    : `<span class="code">${esc(finding.code)}</span>`;
  const manual = remediesOf(finding)
    .filter((r) => r.kind === "manual")
    .map(renderManual)
    .join("");
  const actions = fp ? renderActions(finding) : "";
  return `
  <article class="card sev-${sev}" id="card-${esc(fp)}" data-fingerprint="${esc(fp)}" aria-labelledby="${esc(titleId)}">
    <header class="card-head">
      <span class="badge sev-${sev}"><span class="sr-only">Severity: </span>${esc(SEVERITY_LABEL[sev])}</span>
      ${code}
      <h3 id="${esc(titleId)}">${esc(finding.title)}</h3>
    </header>
    <p class="cause">${esc(finding.cause)}</p>
    ${manual}
    <details class="evidence">
      <summary>Details</summary>
      <p class="muted small">Check <code>${esc(finding.check)}</code> · fingerprint <code>${esc(fp)}</code></p>
      ${renderEvidence(finding.evidence)}
    </details>
    <pre class="preview" hidden></pre>
    ${actions}
    <p class="verify" role="status" aria-live="polite" tabindex="-1"></p>
  </article>`;
}

function renderGroup(sev: DoctorSeverity, findings: DoctorFinding[]): string {
  if (findings.length === 0) return "";
  const cards = findings.map(renderCard).join("\n");
  const headingId = `group-${sev}`;
  if (sev === "housekeeping") {
    const safe = safeFixes(findings).length;
    const fixAll = safe
      ? button(
          "Fix all safe",
          "fixAllSafe",
          `Fix all safe housekeeping items (${plural(safe, "item")})`,
          undefined,
          undefined,
          "primary"
        )
      : "";
    return `
  <section class="group" aria-labelledby="${headingId}">
    <div class="group-head">
      <h2 id="${headingId}">${GROUP_HEADING[sev]} (${findings.length})</h2>
      ${fixAll}
    </div>
    <details class="group-body" id="housekeeping-body">
      <summary>Show ${plural(findings.length, "housekeeping item")}</summary>
      ${cards}
    </details>
  </section>`;
  }
  return `
  <section class="group" aria-labelledby="${headingId}">
    <div class="group-head"><h2 id="${headingId}">${GROUP_HEADING[sev]} (${findings.length})</h2></div>
    ${cards}
  </section>`;
}

function renderAdapterRows(rows: readonly DoctorAdapterHealth[]): string {
  return rows
    .map((a) => {
      const version = a.version
        ? a.min_version && !a.version_ok
          ? `${esc(a.version)} (minimum ${esc(a.min_version)})`
          : esc(a.version)
        : a.installed
          ? "installed"
          : "not installed";
      const status = a.ok
        ? (a.warnings ?? []).length
          ? "Usable, with warnings"
          : "Usable"
        : `Not usable${a.code ? ` (${esc(a.code)})` : ""}`;
      return `<tr><th scope="row">${esc(a.adapter)}</th><td>${esc(a.kind)}</td><td>${version}</td><td>${status}</td></tr>`;
    })
    .join("");
}

function renderAdapters(result: DoctorRunResult, findings: DoctorFinding[]): string {
  const rows = result.adapters ?? [];
  const table = rows.length
    ? `<table class="adapters">
      <caption class="sr-only">Adapter health</caption>
      <thead><tr><th scope="col">Adapter</th><th scope="col">Kind</th><th scope="col">Version</th><th scope="col">Status</th></tr></thead>
      <tbody>${renderAdapterRows(rows)}</tbody>
    </table>`
    : `<p class="muted">No adapters were probed in this scan.</p>`;
  return `
  <section class="group" aria-labelledby="group-adapters">
    <div class="group-head"><h2 id="group-adapters">Adapters (${plural(findings.length, "finding")})</h2></div>
    ${table}
    ${findings.map(renderCard).join("\n")}
  </section>`;
}

/** The one-line verdict: "Healthy", or the blocker and warning counts. */
export function verdictText(result: DoctorRunResult): string {
  const s = result.summary;
  if (s.blocker > 0) return `${plural(s.blocker, "blocker")}, ${plural(s.warning, "warning")}`;
  if (s.warning > 0) return plural(s.warning, "warning");
  return "Healthy";
}

function renderProgressItems(progress: readonly DoctorProgressEvent[]): string {
  return progress
    .filter((p) => p.phase !== "started")
    .map((p) => `<li>${esc(progressLine(p))}</li>`)
    .join("");
}

/** The text of one progress event. The webview script mirrors this wording. */
export function progressLine(p: DoctorProgressEvent): string {
  switch (p.phase) {
    case "started":
      return `Checking ${p.title}…`;
    case "skipped":
      return `${p.title}: skipped (a dependency did not pass)`;
    case "timeout":
      return `${p.title}: timed out`;
    default:
      return p.findings > 0 ? `${p.title}: ${plural(p.findings, "finding")}` : `${p.title}: passed`;
  }
}

function renderBody(state: DoctorViewState): string {
  const runButton = button(
    state.phase === "running" ? "Running…" : "Run again",
    "run",
    "Run the doctor again"
  );
  if (state.phase === "running") {
    const done = state.progress.filter((p) => p.phase !== "started").length;
    const total = state.progress.length ? state.progress[0].total : 0;
    return `
  <div class="toolbar">${runButton.replace("<button ", "<button disabled ")}</div>
  <section aria-labelledby="progress-heading">
    <h2 id="progress-heading">Running checks</h2>
    <p id="progress-status" role="status" aria-live="polite">${esc(
      total ? `Checked ${done} of ${total}` : "Starting the doctor…"
    )}</p>
    <ol id="progress" class="progress">${renderProgressItems(state.progress)}</ol>
  </section>`;
  }
  if (state.phase === "error" || !state.result) {
    return `
  <div class="toolbar">${runButton}</div>
  <p class="error-text" role="alert">The doctor could not run: ${esc(state.error ?? "no result")}</p>`;
  }
  const result = state.result;
  const findings = result.findings ?? [];
  const { bySeverity, adapters } = groupFindings(findings);
  const s = result.summary;
  const groups = SEVERITY_ORDER.map((sev) => renderGroup(sev, bySeverity[sev])).join("\n");
  const empty =
    findings.length === 0 ? `<p class="healthy">No findings: every check passed.</p>` : "";
  return `
  <div class="toolbar">
    ${runButton}
    <span class="meta">${state.generatedAt ? `Last run: ${esc(state.generatedAt)}` : ""}</span>
  </div>
  <p class="verdict" role="status" aria-live="polite"><strong>${esc(verdictText(result))}</strong>
    <span class="meta">${esc(
      `${plural(s.blocker, "blocker")} · ${plural(s.warning, "warning")} · ${s.housekeeping} housekeeping · ${s.info} info`
    )}</span></p>
  ${empty}
  ${groups}
  ${renderAdapters(result, adapters)}`;
}

/** The inline script: posts `{type, fingerprint, remedyId}`, writes text only. */
function script(): string {
  return `
(function () {
  const vscode = acquireVsCodeApi();
  const FP = /^[0-9a-f]{16}$/;
  function card(fp) {
    return typeof fp === "string" && FP.test(fp) ? document.getElementById("card-" + fp) : null;
  }
  document.addEventListener("click", function (ev) {
    const el = ev.target && ev.target.closest ? ev.target.closest("[data-action]") : null;
    if (!el) return;
    ev.preventDefault();
    if (el.disabled) return;
    const fp = el.getAttribute("data-fingerprint") || undefined;
    const rid = el.getAttribute("data-remedy") || undefined;
    const action = el.getAttribute("data-action");
    if (Object.prototype.hasOwnProperty.call(POST, action)) POST[action](fp, rid);
  });
  // The closed set of messages this page posts: {type, fingerprint, remedyId}.
  const POST = {
    run: function () { vscode.postMessage({ type: "run" }); },
    fixAllSafe: function () { vscode.postMessage({ type: "fixAllSafe" }); },
    fix: function (fp, rid) { vscode.postMessage({ type: "fix", fingerprint: fp, remedyId: rid }); },
    review: function (fp, rid) { vscode.postMessage({ type: "review", fingerprint: fp, remedyId: rid }); },
    open: function (fp, rid) { vscode.postMessage({ type: "open", fingerprint: fp, remedyId: rid }); },
    openDocs: function (fp) { vscode.postMessage({ type: "openDocs", fingerprint: fp }); },
    recheck: function (fp) { vscode.postMessage({ type: "recheck", fingerprint: fp }); },
  };
  function setBusy(c, busy) {
    c.querySelectorAll(".actions button").forEach(function (b) { b.disabled = busy; });
    c.setAttribute("aria-busy", busy ? "true" : "false");
  }
  function setEvidence(c, pairs) {
    const dl = c.querySelector(".evidence-list");
    if (!dl || !Array.isArray(pairs)) return;
    while (dl.firstChild) dl.removeChild(dl.firstChild);
    pairs.forEach(function (p) {
      if (!Array.isArray(p) || p.length !== 2) return;
      const dt = document.createElement("dt");
      dt.textContent = String(p[0]);
      const dd = document.createElement("dd");
      dd.textContent = String(p[1]);
      dl.appendChild(dt);
      dl.appendChild(dd);
    });
    const details = c.querySelector("details.evidence");
    if (details) details.open = true;
  }
  const PHASE = { skipped: "skipped (a dependency did not pass)", timeout: "timed out" };
  window.addEventListener("message", function (ev) {
    const m = ev.data;
    if (!m || typeof m !== "object") return;
    if (m.type === "progress" && m.event && typeof m.event === "object") {
      const e = m.event;
      const status = document.getElementById("progress-status");
      const list = document.getElementById("progress");
      if (!status || !list) return;
      if (e.phase === "started") {
        status.textContent = "Checking " + String(e.title) + "…";
        return;
      }
      const li = document.createElement("li");
      const n = Number(e.findings) || 0;
      li.textContent = String(e.title) + ": " + (PHASE[e.phase] ||
        (n > 0 ? n + (n === 1 ? " finding" : " findings") : "passed"));
      list.appendChild(li);
      status.textContent = "Checked " + list.children.length + " of " + (Number(e.total) || 0);
      return;
    }
    const c = card(m.fingerprint);
    if (!c) return;
    const verify = c.querySelector(".verify");
    if (m.type === "busy") {
      setBusy(c, !!m.busy);
      if (verify && typeof m.text === "string") verify.textContent = m.text;
    } else if (m.type === "preview") {
      const pre = c.querySelector(".preview");
      if (pre) {
        pre.textContent = String(m.preview || "");
        pre.hidden = false;
      }
    } else if (m.type === "outcome") {
      if (verify) verify.textContent = String(m.text || "");
      c.classList.remove("resolved", "present", "failed");
      if (m.state === "resolved" || m.state === "present" || m.state === "failed") {
        c.classList.add(m.state);
      }
      if (m.evidence) setEvidence(c, m.evidence);
      if (m.state === "resolved") {
        setBusy(c, true);
        c.querySelectorAll(".actions button").forEach(function (b) {
          b.setAttribute("aria-disabled", "true");
        });
        const active = document.activeElement;
        if (verify && (!active || active === document.body || c.contains(active))) verify.focus();
      } else {
        setBusy(c, false);
      }
    }
  });
})();`;
}

/** Render the whole Doctor panel. Pure: identical input, identical output. */
export function renderDoctorHtml(state: DoctorViewState, security: DoctorHtmlSecurity): string {
  const nonce = esc(security.nonce);
  const csp = [
    "default-src 'none'",
    `style-src ${security.cspSource} 'nonce-${nonce}'`,
    `script-src 'nonce-${nonce}'`,
  ].join("; ");
  return /* html */ `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta http-equiv="Content-Security-Policy" content="${csp}" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Nightgauge Doctor</title>
  <style nonce="${nonce}">
    body {
      font-family: var(--vscode-font-family);
      font-size: var(--vscode-font-size);
      color: var(--vscode-foreground);
      background: var(--vscode-editor-background);
      padding: 1.25rem;
      max-width: 60rem;
    }
    h1 { font-size: 1.4em; margin: 0 0 0.25rem; }
    h2 { font-size: 1.1em; margin: 0; }
    h3 { font-size: 1em; margin: 0; display: inline; }
    .meta, .muted { color: var(--vscode-descriptionForeground); }
    .small { font-size: 0.9em; }
    .sr-only {
      position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px;
      overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0;
    }
    .toolbar { display: flex; align-items: center; gap: 1rem; margin: 0.75rem 0; }
    .verdict { margin: 0.5rem 0 1rem; }
    .verdict .meta { margin-left: 0.5rem; }
    .group { margin: 1.25rem 0; }
    .group-head { display: flex; align-items: center; gap: 1rem; margin-bottom: 0.5rem; }
    .group-body > summary { cursor: pointer; margin-bottom: 0.5rem; }
    .card {
      border: 1px solid var(--vscode-widget-border, var(--vscode-contrastBorder));
      border-left-width: 4px;
      border-radius: 3px;
      padding: 0.6rem 0.8rem;
      margin: 0.5rem 0;
      background: var(--vscode-editorWidget-background);
    }
    .card.sev-blocker { border-left-color: var(--vscode-editorError-foreground); }
    .card.sev-warning { border-left-color: var(--vscode-editorWarning-foreground); }
    .card.sev-housekeeping { border-left-color: var(--vscode-editorInfo-foreground); }
    .card.sev-info { border-left-color: var(--vscode-descriptionForeground); }
    .card.resolved { opacity: 0.8; }
    .card-head { display: flex; align-items: baseline; gap: 0.6rem; flex-wrap: wrap; }
    .badge {
      font-size: 0.8em; font-weight: 600; text-transform: uppercase; letter-spacing: 0.04em;
      padding: 0.05rem 0.4rem; border-radius: 2px;
      border: 1px solid currentColor;
    }
    .badge.sev-blocker { color: var(--vscode-editorError-foreground); }
    .badge.sev-warning { color: var(--vscode-editorWarning-foreground); }
    .badge.sev-housekeeping { color: var(--vscode-editorInfo-foreground); }
    .badge.sev-info { color: var(--vscode-descriptionForeground); }
    .code { font-family: var(--vscode-editor-font-family); color: var(--vscode-textLink-foreground); }
    .cause { margin: 0.4rem 0; }
    .evidence summary { cursor: pointer; }
    .evidence-list { display: grid; grid-template-columns: max-content 1fr; gap: 0.15rem 0.8rem; margin: 0.4rem 0; }
    .evidence-list dt { color: var(--vscode-descriptionForeground); }
    .evidence-list dd { margin: 0; font-family: var(--vscode-editor-font-family); word-break: break-all; }
    .preview {
      font-family: var(--vscode-editor-font-family);
      background: var(--vscode-textCodeBlock-background);
      padding: 0.4rem 0.6rem; white-space: pre-wrap; margin: 0.4rem 0;
    }
    .actions { display: flex; gap: 0.5rem; flex-wrap: wrap; margin-top: 0.5rem; }
    .verify { margin: 0.4rem 0 0; min-height: 1em; }
    .card.resolved .verify { color: var(--vscode-testing-iconPassed); }
    .card.present .verify, .card.failed .verify, .error-text { color: var(--vscode-errorForeground); }
    .healthy { color: var(--vscode-testing-iconPassed); }
    code { font-family: var(--vscode-editor-font-family); }
    button {
      font: inherit;
      color: var(--vscode-button-secondaryForeground);
      background: var(--vscode-button-secondaryBackground);
      border: 1px solid var(--vscode-button-border, transparent);
      padding: 0.3rem 0.8rem; border-radius: 2px; cursor: pointer;
    }
    button.primary { color: var(--vscode-button-foreground); background: var(--vscode-button-background); }
    button:hover:not(:disabled) { background: var(--vscode-button-secondaryHoverBackground); }
    button.primary:hover:not(:disabled) { background: var(--vscode-button-hoverBackground); }
    button:disabled { opacity: 0.6; cursor: default; }
    button:focus-visible, a:focus-visible, summary:focus-visible, .verify:focus-visible {
      outline: 1px solid var(--vscode-focusBorder); outline-offset: 2px;
    }
    table.adapters { border-collapse: collapse; width: 100%; margin: 0.5rem 0; }
    table.adapters th, table.adapters td {
      text-align: left; padding: 0.3rem 0.5rem;
      border-bottom: 1px solid var(--vscode-widget-border, var(--vscode-contrastBorder));
    }
    ol.progress { color: var(--vscode-descriptionForeground); }
  </style>
</head>
<body>
  <h1>Nightgauge Doctor</h1>
  <p class="meta">Every check the doctor runs, one card per finding. Fixes are verified by re-running the check.</p>
  ${renderBody(state)}
  <script nonce="${nonce}">${script()}</script>
</body>
</html>`;
}
