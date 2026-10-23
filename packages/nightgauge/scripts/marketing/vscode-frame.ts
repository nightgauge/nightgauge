/**
 * Marketing capture — the VS Code window around a webview.
 *
 * The dashboard content inside the editor area is the REAL webview HTML from
 * `getDashboardHtml()`; the frame (title bar, activity bar, sidebar tree,
 * editor tabs, status bar) is a faithful mock of VS Code's Dark Modern theme
 * because a webview rendered outside VS Code has no window around it.
 * The sidebar tree mirrors what `PipelineTreeProvider` shows for the same
 * run: the items, their order and their descriptions, from the same data.
 */

import { RUN_338, RUN_338_DURATION_MS, SIBLING_RUNS } from "./run-data";
import { formatDuration } from "../../src/services/notifications/transport";
import { formatCost } from "../../src/utils/formatCost";

const STAGES: Array<[string, string]> = [
  ["issue-pickup", "Issue Pickup"],
  ["feature-planning", "Feature Planning"],
  ["feature-dev", "Feature Dev"],
  ["feature-validate", "Feature Validate"],
  ["pr-create", "PR Create"],
  ["pr-merge", "PR Merge"],
];

/** The VS Code theme variables a webview inherits from the workbench. */
export const THEME_VARS = `
  --vscode-font-family: -apple-system, BlinkMacSystemFont, "Segoe WPC", "Segoe UI", system-ui, "Ubuntu", "Droid Sans", sans-serif;
  --vscode-font-size: 13px;
  --vscode-font-weight: normal;
  --vscode-editor-font-family: "SF Mono", Menlo, Monaco, "Courier New", monospace;
  --vscode-editor-font-size: 12px;
  --vscode-foreground: #cccccc;
  --vscode-editor-foreground: #cccccc;
  --vscode-editor-background: #1f1f1f;
  --vscode-editorWidget-background: #202020;
  --vscode-sideBar-background: #181818;
  --vscode-sideBar-foreground: #cccccc;
  --vscode-sideBarSectionHeader-background: #181818;
  --vscode-sideBarSectionHeader-foreground: #cccccc;
  --vscode-sideBarSectionHeader-border: #2b2b2b;
  --vscode-sideBarTitle-foreground: #cccccc;
  --vscode-activityBar-background: #181818;
  --vscode-activityBar-foreground: #d7d7d7;
  --vscode-activityBar-inactiveForeground: #868686;
  --vscode-activityBar-activeBorder: #0078d4;
  --vscode-activityBar-border: #2b2b2b;
  --vscode-titleBar-activeBackground: #181818;
  --vscode-titleBar-activeForeground: #cccccc;
  --vscode-statusBar-background: #181818;
  --vscode-statusBar-foreground: #cccccc;
  --vscode-statusBar-border: #2b2b2b;
  --vscode-statusBarItem-remoteBackground: #0078d4;
  --vscode-editorGroupHeader-tabsBackground: #181818;
  --vscode-tab-activeBackground: #1f1f1f;
  --vscode-tab-inactiveBackground: #181818;
  --vscode-tab-activeForeground: #ffffff;
  --vscode-tab-inactiveForeground: #9d9d9d;
  --vscode-tab-border: #2b2b2b;
  --vscode-tab-activeBorderTop: #0078d4;
  --vscode-panel-border: #2b2b2b;
  --vscode-widget-border: #313131;
  --vscode-input-background: #313131;
  --vscode-input-foreground: #cccccc;
  --vscode-input-border: #3c3c3c;
  --vscode-inputOption-activeBackground: #2489db;
  --vscode-dropdown-background: #313131;
  --vscode-dropdown-foreground: #cccccc;
  --vscode-dropdown-border: #3c3c3c;
  --vscode-button-background: #0078d4;
  --vscode-button-foreground: #ffffff;
  --vscode-button-hoverBackground: #026ec1;
  --vscode-button-secondaryBackground: #313131;
  --vscode-button-secondaryForeground: #cccccc;
  --vscode-badge-background: #616161;
  --vscode-badge-foreground: #f8f8f8;
  --vscode-list-hoverBackground: #2a2d2e;
  --vscode-list-activeSelectionBackground: #04395e;
  --vscode-list-activeSelectionForeground: #ffffff;
  --vscode-list-inactiveSelectionBackground: #37373d;
  --vscode-focusBorder: #0078d4;
  --vscode-descriptionForeground: #9d9d9d;
  --vscode-disabledForeground: #6f6f6f;
  --vscode-errorForeground: #f88070;
  --vscode-textLink-foreground: #4daafc;
  --vscode-textLink-activeForeground: #4daafc;
  --vscode-textPreformat-foreground: #d0d0d0;
  --vscode-textPreformat-background: #3c3c3c;
  --vscode-textBlockQuote-background: #2b2b2b;
  --vscode-textBlockQuote-border: #616161;
  --vscode-textCodeBlock-background: #2b2b2b;
  --vscode-icon-foreground: #cccccc;
  --vscode-charts-red: #f14c4c;
  --vscode-charts-blue: #3794ff;
  --vscode-charts-yellow: #cca700;
  --vscode-charts-orange: #d18616;
  --vscode-charts-green: #89d185;
  --vscode-charts-purple: #b180d7;
  --vscode-charts-lines: #cccccc80;
  --vscode-charts-foreground: #cccccc;
  --vscode-testing-iconPassed: #73c991;
  --vscode-testing-iconFailed: #f14c4c;
  --vscode-testing-iconQueued: #cca700;
  --vscode-testing-iconSkipped: #848484;
  --vscode-problemsErrorIcon-foreground: #f14c4c;
  --vscode-problemsWarningIcon-foreground: #cca700;
  --vscode-problemsInfoIcon-foreground: #3794ff;
  --vscode-progressBar-background: #0078d4;
  --vscode-notificationsInfoIcon-foreground: #3794ff;
  --vscode-notificationsWarningIcon-foreground: #cca700;
  --vscode-notificationsErrorIcon-foreground: #f14c4c;
  --vscode-gitDecoration-addedResourceForeground: #81b88b;
  --vscode-gitDecoration-modifiedResourceForeground: #e2c08d;
  --vscode-gitDecoration-deletedResourceForeground: #c74e39;
  --vscode-scrollbarSlider-background: rgba(121,121,121,.4);
  --vscode-scrollbarSlider-hoverBackground: rgba(100,100,100,.7);
  --vscode-editorGutter-addedBackground: #2ea043;
  --vscode-editorGutter-deletedBackground: #f85149;
  --vscode-editorGutter-modifiedBackground: #0078d4;
  --vscode-minimap-findMatchHighlight: #d18616;
  --vscode-symbolIcon-classForeground: #ee9d28;
  --vscode-symbolIcon-methodForeground: #b180d7;
  --vscode-terminal-ansiGreen: #23d18b;
  --vscode-terminal-ansiRed: #f14c4c;
  --vscode-terminal-ansiYellow: #f5f543;
  --vscode-terminal-ansiBlue: #3b8eea;
  --vscode-terminal-ansiBrightBlack: #666666;
`;

/** Inject the workbench theme into a webview HTML document. */
export function themeWebview(html: string): string {
  const cleaned = html
    .replace(/<meta http-equiv="Content-Security-Policy"[^>]*>/i, "")
    .replace(/ nonce="[^"]*"/g, "");
  const style = `<style id="ng-theme">:root{${THEME_VARS}}
    html,body{background:var(--vscode-editor-background);color:var(--vscode-foreground);font-family:var(--vscode-font-family);font-size:var(--vscode-font-size);font-weight:var(--vscode-font-weight)}
    ::-webkit-scrollbar{width:10px;height:10px}::-webkit-scrollbar-thumb{background:var(--vscode-scrollbarSlider-background)}::-webkit-scrollbar-track{background:transparent}
  </style>`;
  return cleaned.includes("<head>")
    ? cleaned.replace("<head>", `<head>${style}`)
    : `${style}${cleaned}`;
}

function icon(name: string): string {
  // Minimal 24px workbench glyphs — outline strokes like VS Code's codicons.
  const paths: Record<string, string> = {
    files: '<path d="M6 3h8l5 5v13H6z"/><path d="M14 3v5h5"/><path d="M3 7v14h11" />',
    search: '<circle cx="10.5" cy="10.5" r="6.5"/><path d="M15.5 15.5 21 21"/>',
    scm: '<circle cx="6" cy="5" r="2.5"/><circle cx="6" cy="19" r="2.5"/><circle cx="18" cy="8" r="2.5"/><path d="M6 7.5v9M18 10.5c0 4-4 4-8 5"/>',
    debug: '<path d="M7 4l12 8-12 8z"/><path d="M4 6l4 2M4 18l4-2"/>',
    extensions:
      '<rect x="3" y="11" width="7" height="7"/><rect x="10" y="4" width="7" height="7"/><rect x="10" y="11" width="7" height="7"/><rect x="3" y="4" width="7" height="7"/>',
    account: '<circle cx="12" cy="8" r="4"/><path d="M4 21c1-4 4-6 8-6s7 2 8 6"/>',
    gear: '<circle cx="12" cy="12" r="3.5"/><path d="M12 2v3M12 19v3M2 12h3M19 12h3M4.9 4.9l2.1 2.1M17 17l2.1 2.1M4.9 19.1 7 17M17 7l2.1-2.1"/>',
    gauge:
      '<path d="M4 16a8 8 0 1 1 16 0"/><path d="M12 16l4-6"/><circle cx="12" cy="16" r="1.4" fill="currentColor"/>',
  };
  return `<svg viewBox="0 0 24 24" width="24" height="24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">${paths[name] ?? ""}</svg>`;
}

function check(kind: "done" | "running" | "pending"): string {
  if (kind === "done") return '<span class="ti pass">✓</span>';
  if (kind === "running") return '<span class="ti run">↻</span>';
  return '<span class="ti pend">○</span>';
}

function sidebarTree(): string {
  const running = SIBLING_RUNS.find((r) => r.status === "running");
  const stageRows = STAGES.map(([id, label]) => {
    const s = RUN_338.stages[id as keyof typeof RUN_338.stages];
    const cost = s.cost_usd > 0 ? ` · ${formatCost(s.cost_usd)}` : "";
    return `<div class="row l3">${check("done")}<span class="lbl">${label}</span><span class="desc">${formatDuration(s.duration_ms)}${cost}</span></div>`;
  }).join("");
  const runningRows = running
    ? STAGES.map(([id, label]) => {
        const order = STAGES.findIndex(([x]) => x === id);
        const cur = STAGES.findIndex(([x]) => x === running.currentStage);
        const kind = order < cur ? "done" : order === cur ? "running" : "pending";
        const desc = order === cur ? `Implementation · ${formatDuration(running.durationMs)}` : "";
        return `<div class="row l3">${check(kind)}<span class="lbl">${label}</span><span class="desc">${desc}</span></div>`;
      }).join("")
    : "";
  const completed = SIBLING_RUNS.filter((r) => r.status === "complete").length + 1;
  return `
  <div class="pane">
    <div class="pane-hdr"><span class="chev">▾</span>PIPELINE<span class="desc">2 slots active</span></div>
    ${
      running
        ? `<div class="row l1"><span class="chev">▾</span><span class="ri run">▶</span><span class="lbl">#${running.issueNumber} — ${running.title}</span></div>${runningRows}`
        : ""
    }
    <div class="row l1"><span class="chev">▾</span><span class="ri pass">✓</span><span class="lbl">#${RUN_338.issue_number} — ${RUN_338.title}</span><span class="desc">${formatDuration(RUN_338_DURATION_MS)}</span></div>
    ${stageRows}
    <div class="row l1"><span class="chev">▸</span><span class="ri">≡</span><span class="lbl">Queued</span><span class="desc">3</span></div>
    <div class="row l1"><span class="chev">▸</span><span class="ri pass">✓</span><span class="lbl">Completed</span><span class="desc">${completed} today</span></div>
  </div>
  <div class="pane">
    <div class="pane-hdr"><span class="chev">▸</span>REPOSITORIES<span class="desc">4</span></div>
  </div>
  <div class="pane">
    <div class="pane-hdr"><span class="chev">▸</span>ATTENTION<span class="desc">0 open</span></div>
  </div>
  <div class="pane">
    <div class="pane-hdr"><span class="chev">▸</span>KNOWLEDGE<span class="desc">${RUN_338.issue_number} · PRD, ADR, retro</span></div>
  </div>`;
}

export interface FrameOptions {
  title: string;
  tabLabel: string;
  workspaceName: string;
  /** Data URI of the Nightgauge activity-bar icon. */
  activityIcon: string;
  /** Themed webview document to place in the editor area (srcdoc). */
  webviewHtml: string;
}

export function renderFrame(o: FrameOptions): string {
  const totalToday =
    RUN_338.tokens.estimated_cost_usd + SIBLING_RUNS.reduce((a, r) => a + r.costUsd, 0);
  const srcdoc = o.webviewHtml.replace(/&/g, "&amp;").replace(/"/g, "&quot;");
  return `<!doctype html><html><head><meta charset="utf-8"><style>
  :root{${THEME_VARS}}
  html,body{margin:0;width:1440px;height:900px;overflow:hidden;background:#1f1f1f;color:var(--vscode-foreground);font-family:var(--vscode-font-family);font-size:13px;-webkit-font-smoothing:antialiased}
  .win{display:grid;grid-template-rows:36px 1fr 22px;height:900px;width:1440px}
  .titlebar{background:var(--vscode-titleBar-activeBackground);border-bottom:1px solid var(--vscode-panel-border);display:flex;align-items:center;position:relative;color:var(--vscode-titleBar-activeForeground)}
  .lights{position:absolute;left:12px;top:11px;display:flex;gap:8px}.lights i{width:12px;height:12px;border-radius:50%;display:block}
  .lights .r{background:#ff5f57}.lights .y{background:#febc2e}.lights .g{background:#28c840}
  .titlebar .t{margin:0 auto;font-size:13px;color:#cccccc}
  .main{display:grid;grid-template-columns:48px 300px 1fr;min-height:0}
  .activity{background:var(--vscode-activityBar-background);border-right:1px solid var(--vscode-activityBar-border);display:flex;flex-direction:column;align-items:center;padding-top:2px}
  .act{width:48px;height:48px;display:flex;align-items:center;justify-content:center;color:var(--vscode-activityBar-inactiveForeground);position:relative}
  .act.on{color:var(--vscode-activityBar-foreground)}.act.on::before{content:"";position:absolute;left:0;top:0;bottom:0;width:2px;background:var(--vscode-activityBar-activeBorder)}
  .act img{width:24px;height:24px;filter:grayscale(1) brightness(1.6)}.act.on img{filter:none}
  .act.bottom{margin-top:auto}
  .sidebar{background:var(--vscode-sideBar-background);border-right:1px solid var(--vscode-panel-border);display:flex;flex-direction:column;min-height:0;overflow:hidden}
  .sb-title{height:35px;display:flex;align-items:center;padding:0 20px;font-size:11px;letter-spacing:.04em;color:var(--vscode-sideBarTitle-foreground);text-transform:uppercase}
  .pane-hdr{height:22px;display:flex;align-items:center;gap:4px;padding:0 8px 0 4px;font-size:11px;font-weight:700;letter-spacing:.02em;border-top:1px solid var(--vscode-sideBarSectionHeader-border);color:var(--vscode-sideBarSectionHeader-foreground)}
  .pane-hdr .desc{margin-left:auto;font-weight:400;color:var(--vscode-descriptionForeground);text-transform:none;letter-spacing:0}
  .row{height:22px;display:flex;align-items:center;gap:6px;padding-right:8px;white-space:nowrap;overflow:hidden}
  .row .lbl{overflow:hidden;text-overflow:ellipsis}
  .row .desc{color:var(--vscode-descriptionForeground);font-size:12px;margin-left:auto;padding-left:8px;flex:none}
  .row.l1{padding-left:8px}.row.l3{padding-left:38px}
  .chev{width:16px;text-align:center;color:var(--vscode-icon-foreground);font-size:11px;flex:none}
  .ri,.ti{width:16px;text-align:center;flex:none;font-size:13px}
  .pass{color:var(--vscode-testing-iconPassed)}.run{color:var(--vscode-charts-blue)}.pend{color:var(--vscode-disabledForeground)}
  .editor{display:grid;grid-template-rows:35px 1fr;min-height:0;background:var(--vscode-editor-background)}
  .tabs{background:var(--vscode-editorGroupHeader-tabsBackground);border-bottom:1px solid var(--vscode-tab-border);display:flex}
  .tab{display:flex;align-items:center;gap:8px;padding:0 12px;font-size:13px;color:var(--vscode-tab-inactiveForeground);background:var(--vscode-tab-inactiveBackground);border-right:1px solid var(--vscode-tab-border);position:relative}
  .tab.on{color:var(--vscode-tab-activeForeground);background:var(--vscode-tab-activeBackground)}.tab.on::before{content:"";position:absolute;left:0;right:0;top:0;height:1px;background:var(--vscode-tab-activeBorderTop)}
  .tab img{width:16px;height:16px}.tab .x{color:var(--vscode-descriptionForeground);margin-left:6px}
  iframe{border:0;width:100%;height:100%;display:block;background:var(--vscode-editor-background)}
  .status{background:var(--vscode-statusBar-background);border-top:1px solid var(--vscode-statusBar-border);display:flex;align-items:center;font-size:12px;color:var(--vscode-statusBar-foreground)}
  .status .it{padding:0 8px;display:flex;align-items:center;gap:5px;height:22px}
  .status .remote{background:var(--vscode-statusBarItem-remoteBackground);color:#fff;padding:0 10px}
  .status .right{margin-left:auto;display:flex}
</style></head><body><div class="win">
  <div class="titlebar"><div class="lights"><i class="r"></i><i class="y"></i><i class="g"></i></div><div class="t">${o.title}</div></div>
  <div class="main">
    <div class="activity">
      <div class="act">${icon("files")}</div><div class="act">${icon("search")}</div><div class="act">${icon("scm")}</div><div class="act">${icon("debug")}</div><div class="act">${icon("extensions")}</div>
      <div class="act on"><img src="${o.activityIcon}" alt=""></div>
      <div class="act bottom">${icon("account")}</div><div class="act">${icon("gear")}</div>
    </div>
    <div class="sidebar"><div class="sb-title">Nightgauge Pipeline</div>${sidebarTree()}</div>
    <div class="editor">
      <div class="tabs"><div class="tab"><span style="color:#40c4ff">◆</span> app_router.dart <span class="x">×</span></div><div class="tab on"><img src="${o.activityIcon}" alt=""> ${o.tabLabel} <span class="x">×</span></div></div>
      <iframe srcdoc="${srcdoc}"></iframe>
    </div>
  </div>
  <div class="status">
    <div class="it remote">${icon("gauge").replace('width="24" height="24"', 'width="14" height="14"')} Nightgauge · 2 slots</div>
    <div class="it"><span class="pass">✓</span> circuit breaker: armed</div>
    <div class="it">⑂ main</div>
    <div class="it">☁ ${(21.2).toFixed(1)}M cached tokens · ${formatCost(totalToday)} today</div>
    <div class="right"><div class="it">⚡ Performance: Frontier</div><div class="it">${o.workspaceName}</div></div>
  </div>
</div></body></html>`;
}
