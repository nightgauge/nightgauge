/**
 * Demo mode against the real extension (#2105, #2106, #2108, ADR-026).
 *
 * Runs alone in the launcher's demo window (launch.ts): a fresh VSCode whose
 * first activation is on the demo workspace, as in a demo session (#2110).
 * The demo daemon arrives through `nightgauge.backend.binaryPath`, and the
 * reference scenario through the environment (`NIGHTGAUGE_DEMO_SCENARIO`),
 * since a setting cannot carry arguments. The scenario's initial state is
 * served from the first request; its steps wait for
 * `NIGHTGAUGE_DEMO_START_FILE`, which only this suite creates. So the first
 * cases see the static seed and the playback cases see the scenario run.
 *
 * Playback is observed by recording, not by sampling: every Pipeline tree
 * change is read as it fires and every dashboard message is recorded by the
 * observer, so a busy machine can slow the run down without making a
 * checkpoint unobservable.
 */

import * as assert from "node:assert/strict";
import * as fs from "node:fs";
import http from "node:http";
import https from "node:https";
import * as vscode from "vscode";
import { suite, test } from "../harness.js";
import { capturedPanels, capturedStatusBarItems, capturedTreeProviders } from "../observe.js";
import { delay, materializeDemoWorkspace, waitFor, workspaceRoot } from "../fixture.js";
import { extension } from "./activation.suite.js";

type Provider = vscode.TreeDataProvider<unknown>;

/** Stages the reference scenario runs issue #133 through, in order. */
const STAGES = [
  "pipeline-start",
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
  "pipeline-finish",
];
/** Dashboard tabs the reference scenario's `ui` steps select, in order. */
const UI_TABS = ["pipeline", "analytics", "history", "overview"];

function provider(viewId: string): Provider {
  const captured = capturedTreeProviders().find((entry) => entry.viewId === viewId);
  assert.ok(captured, `No provider captured for ${viewId}`);
  return captured.provider;
}

async function treeItem(p: Provider, element: unknown): Promise<vscode.TreeItem> {
  return await p.getTreeItem(element);
}

function labelOf(item: vscode.TreeItem): string {
  const label = item.label;
  return typeof label === "string" ? label : (label?.label ?? "");
}

async function children(p: Provider, element?: unknown): Promise<unknown[]> {
  return ((await p.getChildren(element)) ?? []) as unknown[];
}

/** Labels of a tree to `depth` levels, indented, for assertion messages. */
async function dumpTree(p: Provider, depth = 2): Promise<string> {
  const lines: string[] = [];
  const walk = async (element: unknown, level: number): Promise<void> => {
    for (const child of await children(p, element)) {
      const item = await treeItem(p, child);
      lines.push(`${"  ".repeat(level)}${labelOf(item)} | ${String(item.description ?? "")}`);
      if (level + 1 < depth) await walk(child, level + 1);
    }
  };
  await walk(undefined, 0);
  return lines.join("\n");
}

function dashboard() {
  return capturedPanels().find((p) => p.viewType === "nightgaugeDashboard" && !p.disposed);
}

function tabPanel(html: string, tab: string): string {
  const start = html.indexOf(`id="tab-panel-${tab}"`);
  if (start < 0) return "";
  const next = html.indexOf('class="tab-panel', start + 1);
  return html.slice(start, next < 0 ? undefined : next);
}

function text(html: string): string {
  return html
    .replace(/<script\b[^>]*>[\s\S]*?<\/script[^>]*>/gi, " ")
    .replace(/<style\b[^>]*>[\s\S]*?<\/style[^>]*>/gi, " ")
    .replace(/<[^>]+>/g, " ")
    .replace(/&#8635;/g, "↻")
    .replace(/\s+/g, " ");
}

/** The running stage of the Pipeline tree's active issue, or null. */
async function runningStage(p: Provider): Promise<string | null> {
  for (const root of await children(p)) {
    const item = await treeItem(p, root);
    if (item.contextValue !== "issue") continue;
    for (const child of await children(p, root)) {
      const stageItem = await treeItem(p, child);
      if (/^stage-(bookend-)?running$/.test(stageItem.contextValue ?? "")) {
        return String((child as { stage?: unknown }).stage ?? labelOf(stageItem));
      }
    }
  }
  return null;
}

/** Outbound HTTP attempts recorded while the scenario plays (ADR-026 § 6). */
const outbound: string[] = [];
const restoreHttp: Array<() => void> = [];

function describeTarget(target: unknown): string {
  if (typeof target === "string" || target instanceof URL) return String(target);
  return JSON.stringify((target as { host?: unknown } | null)?.host ?? target);
}

/**
 * Record every outbound HTTP attempt: the default imports are Node's own
 * module objects, which the extension's bundle shares, and fetch is global.
 */
function recordOutboundHttp(): void {
  for (const [name, mod] of [
    ["http", http],
    ["https", https],
  ] as const) {
    for (const fn of ["request", "get"] as const) {
      const original = mod[fn];
      (mod as Record<string, unknown>)[fn] = (...args: unknown[]) => {
        outbound.push(`${name}.${fn} ${describeTarget(args[0])}`);
        return (original as (...a: unknown[]) => unknown)(...args);
      };
      restoreHttp.push(() => ((mod as Record<string, unknown>)[fn] = original));
    }
  }
  const originalFetch = globalThis.fetch;
  globalThis.fetch = ((input: unknown, init?: unknown) => {
    outbound.push(`fetch ${String(input instanceof Request ? input.url : input)}`);
    return originalFetch(...([input, init] as Parameters<typeof fetch>));
  }) as typeof fetch;
  restoreHttp.push(() => (globalThis.fetch = originalFetch));
}

/** Pipeline tree running-stage transitions, recorded as the tree fires changes. */
const stageTransitions: string[] = [];

function startRecordingStages(): void {
  const p = provider("nightgauge.pipelineView");
  const subscribe = p.onDidChangeTreeData;
  assert.ok(subscribe, "The Pipeline tree provider exposes no onDidChangeTreeData");
  let reading = Promise.resolve();
  // Read in order, one change at a time, so no transition is reordered.
  subscribe(() => {
    reading = reading.then(async () => {
      const stage = await runningStage(p);
      if (stage && stageTransitions.at(-1) !== stage) stageTransitions.push(stage);
    });
  });
}

interface SeededItem {
  number: number;
  title: string;
  repo: string;
}
interface SeededQueueItem {
  issueNumber: number;
  title: string;
  repo: string;
}

/** The scenario the launcher handed the daemon. */
function scenario(): {
  state: { board: Record<string, SeededItem[]>; queue: SeededQueueItem[] };
} {
  const file = process.env.NIGHTGAUGE_DEMO_SCENARIO;
  assert.ok(file, "NIGHTGAUGE_DEMO_SCENARIO is unset — the launcher did not wire it");
  return JSON.parse(fs.readFileSync(file, "utf8"));
}

function scenarioBoard(): Record<string, SeededItem[]> {
  return scenario().state.board;
}

/** The demo workspace's repository, as its config names it. */
function demoRepo(): string {
  const config = fs.readFileSync(`${workspaceRoot()}/.nightgauge/config.yaml`, "utf8");
  const match = /^\s+repo:\s*(\S+)/m.exec(config);
  assert.ok(match, "The demo workspace config names no github.repo");
  return match[1];
}

function eventLog(): Array<{ at: number; event: string; data: Record<string, unknown> }> {
  const file = process.env.NIGHTGAUGE_DEMO_EVENT_LOG;
  if (!file || !fs.existsSync(file)) return [];
  return fs
    .readFileSync(file, "utf8")
    .split("\n")
    .filter((line) => line.trim())
    .map((line) => JSON.parse(line));
}

suite("demo mode", () => {
  test("the extension activates on the demo workspace", async () => {
    materializeDemoWorkspace();
    await extension().activate();
    assert.equal(extension().isActive, true);
  });

  test("the demo daemon's handshake shows the Demo badge", async () => {
    // Any IPC call connects the backend; the handshake follows.
    await vscode.commands.executeCommand("nightgauge.showDashboard");
    const badge = await waitFor(
      () =>
        capturedStatusBarItems().find(
          (item) => item.id === "nightgauge.demoMode" && item.text.includes("Demo")
        ),
      10_000,
      "the Demo status bar badge"
    );
    assert.match(badge.accessibilityInformation?.label ?? "", /demo mode/i);
  });

  test("the scenario waits for its start signal", () => {
    const events = eventLog().map((e) => e.event);
    assert.ok(events.includes("ipc.ready"), "The event log recorded no ipc.ready");
    assert.deepEqual(
      events.filter((e) => e !== "ipc.ready"),
      [],
      "The daemon played scenario steps before the start file existed"
    );
  });

  test("the Repositories tree shows the seeded board counts", async () => {
    const p = provider("nightgauge.repositoriesView");
    const seeded = scenarioBoard();
    const shown: Record<string, Record<string, number>> = {};
    const expected: Record<string, Record<string, number>> = {};
    for (const repo of await children(p)) {
      const name = labelOf(await treeItem(p, repo));
      shown[name] = {};
      expected[name] = {};
      for (const status of ["Ready", "In Progress", "Backlog"]) {
        expected[name][status] = (seeded[status] ?? []).filter((i) => i.repo === name).length;
      }
      for (const summary of await children(p, repo)) {
        // A status group's label carries its count once its issues load.
        await children(p, summary);
        const match = /^(Ready|In Progress|Backlog): (\d+) issues?$/.exec(
          labelOf(await treeItem(p, summary))
        );
        if (match) shown[name][match[1]] = Number(match[2]);
      }
    }
    assert.ok(Object.keys(shown).length > 0, "The Repositories tree has no repository");
    assert.ok(
      Object.values(expected).some((counts) => Object.values(counts).some((n) => n > 0)),
      `No seeded issue belongs to a repository the tree shows: ${Object.keys(shown).join(", ")}`
    );
    assert.deepEqual(shown, expected, `Repositories tree:\n${await dumpTree(p)}`);
  });

  // The seeded active run (#112) cannot be asserted here: the extension
  // learns run state only from pipeline.stateChanged events, and nothing asks
  // the daemon for runs already in flight, so a static seed can show the
  // queue but not an active issue. Playback below asserts the active run.
  test("the Pipeline tree shows the seeded queue", async () => {
    const p = provider("nightgauge.pipelineView");
    const expected = scenario().state.queue.map((q) => `#${q.issueNumber} - ${q.title}`);
    let shown: string[] = [];
    for (let attempt = 0; attempt < 50 && shown.length < expected.length; attempt++) {
      shown = [];
      for (const root of await children(p)) {
        if (!/^Queued Issues/.test(labelOf(await treeItem(p, root)))) continue;
        for (const child of await children(p, root)) shown.push(labelOf(await treeItem(p, child)));
      }
      if (shown.length < expected.length) await delay(100);
    }
    assert.deepEqual(shown, expected, `Pipeline tree:\n${await dumpTree(p)}`);
  });

  test("the dashboard Overview shows the seeded totals", async () => {
    const repo = demoRepo();
    const seeded = scenarioBoard();
    const count = (status: string) =>
      (seeded[status] ?? []).filter((item) => item.repo === repo).length;
    const expected = `${count("Ready")} Ready ${count("In Progress")} In Progress ${count("In Review")} In Review ${count("Done")} Done`;
    const upNext = scenario()
      .state.queue.map((q, i) => `#${i + 1} #${q.issueNumber} ${q.title}`)
      .join(" .*");
    await vscode.commands.executeCommand("nightgauge.showDashboard");
    const panel = await waitFor(dashboard, 5_000, "the Dashboard panel");
    for (const listener of panel.messageListeners) {
      await listener({ type: "selectTab", tab: "overview" });
    }
    const overview = () => text(tabPanel(panel.panel.webview.html, "overview"));
    await waitFor(
      () => (overview().includes(`Project Board Summary ↻ ${expected}`) ? true : undefined),
      15_000,
      "the Overview's board summary"
    ).catch(() => undefined);
    const body = overview();
    assert.ok(
      body.includes(`Project Board Summary ↻ ${expected}`),
      `Overview board summary is not "${expected}":\n${body.slice(0, 3000)}`
    );
    assert.match(body, new RegExp(`Up next ${upNext}`), `Overview queue:\n${body.slice(0, 3000)}`);
  });

  test("the start signal plays the scenario", async () => {
    const startFile = process.env.NIGHTGAUGE_DEMO_START_FILE;
    assert.ok(startFile, "NIGHTGAUGE_DEMO_START_FILE is unset — the launcher did not wire it");
    recordOutboundHttp();
    startRecordingStages();
    fs.writeFileSync(startFile, "");
    await waitFor(
      () => eventLog().find((e) => e.event === "stage.start"),
      15_000,
      "the scenario's first stage.start"
    );
  });

  test("the Pipeline tree's active stage walks every stage in order", async () => {
    await waitFor(
      () => (stageTransitions.includes("pipeline-finish") ? true : undefined),
      25_000,
      "the Pipeline tree to reach pipeline-finish"
    ).catch(() => undefined);
    // Up to the first run's last stage; the scenario then starts a second run.
    const firstRun = stageTransitions.slice(0, stageTransitions.indexOf("pipeline-finish") + 1);
    assert.deepEqual(
      firstRun,
      STAGES,
      `Running-stage transitions: ${stageTransitions.join(" -> ") || "(none)"}`
    );
  });

  test("the active dashboard tab follows each UI step", async () => {
    const panel = await waitFor(dashboard, 5_000, "the Dashboard panel");
    const activated = () =>
      panel.posted
        .filter(
          (m): m is { type: string; tab: string } =>
            (m as { type?: unknown }).type === "activateTab"
        )
        .map((m) => m.tab);
    await waitFor(
      () => (activated().length >= UI_TABS.length ? true : undefined),
      20_000,
      "four activateTab messages"
    ).catch(() => undefined);
    assert.deepEqual(activated(), UI_TABS);
    // The webview's own answer: it activated each tab the way a click does.
    // Its reply to the last activateTab arrives after the post, so wait for it.
    const selected = () =>
      panel.received
        .filter(
          (m): m is { type: string; tab: string } => (m as { type?: unknown }).type === "selectTab"
        )
        .map((m) => m.tab)
        .filter((tab, i, all) => all[i - 1] !== tab);
    await waitFor(
      () => (selected().slice(-UI_TABS.length).join() === UI_TABS.join() ? true : undefined),
      10_000,
      "the webview's selectTab reply to each UI step"
    ).catch(() => undefined);
    assert.deepEqual(
      selected().slice(-UI_TABS.length),
      UI_TABS,
      `webview selectTab: ${selected().join(", ")}`
    );
  });

  test("the scenario played to its end without outbound HTTP", async () => {
    await waitFor(
      () => eventLog().find((e) => e.event === "pipeline.complete"),
      20_000,
      "pipeline.complete in the event log"
    );
    for (const restore of restoreHttp.splice(0)) restore();
    assert.deepEqual(outbound, [], "The extension made outbound HTTP calls in demo mode");
  });
});
