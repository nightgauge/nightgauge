/**
 * dashboardRefreshOrdering.test.ts — one refresh shows the populated health
 * widget once history exists on disk (#2274).
 *
 * arrival:overview
 *
 * The Dashboard is built the way activation builds it: a real `TelemetryStore`
 * over a workspace whose `.nightgauge/pipeline/history/` is still empty, so the
 * constructor's background load finds nothing. Then the history day-files
 * appear (the demo fixture being copied over an open workspace, or a run
 * finishing while the panel was closed) and the user presses Refresh once.
 *
 * The `refresh` handler used to start the history load and the health
 * computation in the same `Promise.all`. `HealthWidgetService.getData()` reads
 * `getHistory()` as soon as its module import settles, which is long before the
 * store has read and indexed the JSONL, so the widget was computed from the
 * old, empty history and the panel showed "Run your first pipeline" beside a
 * populated history tab. A second refresh read the history the first had
 * loaded, which is why it "fixed itself".
 *
 * The event log below records the ordering itself, so a failure names the race
 * rather than only its symptom.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { createMockMemento } from "../mocks/memento";

vi.mock("vscode", async () => (await import("./dashboardHarness")).vscodeMockModule());
vi.mock("../../src/services/IpcClient", async () =>
  (await import("./dashboardHarness")).ipcClientMockModule()
);
vi.mock("../../src/platform/TokenStorage", async () =>
  (await import("./dashboardHarness")).tokenStorageMockModule()
);
vi.mock("../../src/services/PipelineStateService", async () =>
  (await import("./dashboardHarness")).pipelineStateServiceMockModule()
);
vi.mock("../../src/services/WorkspaceManager", async () =>
  (await import("./dashboardHarness")).workspaceManagerMockModule()
);
vi.mock(
  "../../src/services/SanitizationLogService",
  async () => await (await import("./dashboardHarness")).sanitizationLogServiceMockModule()
);
vi.mock("../../src/services/ProjectBoardService", async () =>
  (await import("./dashboardHarness")).projectBoardServiceMockModule()
);
vi.mock("../../src/services/ProjectIterationService", async () =>
  (await import("./dashboardHarness")).projectIterationServiceMockModule()
);
vi.mock(
  "../../src/services/ConfigBridge",
  async () => await (await import("./dashboardHarness")).configBridgeMockModule()
);

import { Dashboard } from "../../src/views/dashboard/Dashboard";
import type { DashboardState } from "../../src/views/dashboard/DashboardState";
import type { HealthWidgetData } from "../../src/views/dashboard/HealthWidgetTypes";
import { TelemetryStore } from "../../src/services/TelemetryStore";
import {
  capturedPanels,
  renderDashboardHtml,
  renderedText,
  resetHarness,
  tabPanelHtml,
} from "./dashboardHarness";
import { RECORDED_HISTORY_JSONL } from "./fixtures";

const EMPTY_HEALTH = "Run your first pipeline to see health metrics";

interface DashboardInternals {
  state: DashboardState;
  healthWidgetData: HealthWidgetData | null;
}

let workspaceRoot: string;
let dashboard: Dashboard | undefined;

/** Lay the recorded JSONL out as one day-file per date, as the writer does. */
function writeHistory(root: string): number {
  const historyDir = path.join(root, ".nightgauge", "pipeline", "history");
  fs.mkdirSync(historyDir, { recursive: true });
  const lines = fs
    .readFileSync(RECORDED_HISTORY_JSONL, "utf-8")
    .split("\n")
    .filter((l) => l.trim().length > 0);
  const byDate = new Map<string, string[]>();
  for (const line of lines) {
    const record = JSON.parse(line) as Record<string, unknown>;
    const day = String(record["recorded_at"] ?? record["started_at"]).slice(0, 10);
    byDate.set(day, [...(byDate.get(day) ?? []), line]);
  }
  for (const [day, dayLines] of byDate) {
    fs.writeFileSync(path.join(historyDir, `${day}.jsonl`), dayLines.join("\n") + "\n", "utf-8");
  }
  return lines.filter((l) => (JSON.parse(l) as Record<string, unknown>)["record_type"] === "run")
    .length;
}

beforeEach(() => {
  vi.clearAllMocks();
  resetHarness();
  workspaceRoot = fs.mkdtempSync(path.join(os.tmpdir(), "ng-refresh-order-"));
  fs.mkdirSync(path.join(workspaceRoot, ".nightgauge"), { recursive: true });
  fs.writeFileSync(
    path.join(workspaceRoot, ".nightgauge", "config.yaml"),
    "github:\n  owner: nightgauge\n  repo: nightgauge\n",
    "utf-8"
  );
});

afterEach(async () => {
  await new Promise((resolve) => setImmediate(resolve));
  dashboard?.dispose();
  dashboard = undefined;
  fs.rmSync(workspaceRoot, { recursive: true, force: true });
});

describe("arrival: Overview health widget after one refresh (#2274)", () => {
  it("computes health from the history the same refresh loaded", async () => {
    dashboard = new Dashboard(
      { fsPath: "/mock/extension" } as never,
      createMockMemento(),
      workspaceRoot,
      new TelemetryStore(workspaceRoot)
    );
    const internals = dashboard as unknown as DashboardInternals;

    // Activation: the constructor's background load and the panel's own
    // health computation both run against a workspace with no history yet.
    dashboard.show();
    await internals.state.loadFromTelemetryStore();
    await dashboard.refreshHealthWidgetData();
    expect(internals.state.getHistory()).toHaveLength(0);
    expect(internals.healthWidgetData?.isEmpty).toBe(true);

    // History arrives on disk while the panel is open.
    const runs = writeHistory(workspaceRoot);
    expect(runs).toBeGreaterThan(0);

    // Instrument the ordering: when does the health computation read
    // history, and when does the history load finish?
    const events: string[] = [];
    const state = internals.state;
    const getHistory = state.getHistory.bind(state);
    vi.spyOn(state, "getHistory").mockImplementation(() => {
      const history = getHistory();
      events.push(`getHistory:${history.length}`);
      return history;
    });
    const load = state.loadFromTelemetryStore.bind(state);
    vi.spyOn(state, "loadFromTelemetryStore").mockImplementation(async () => {
      events.push("historyLoad:start");
      const result = await load();
      events.push(`historyLoad:end:${state.getHistory().length}`);
      return result;
    });
    const getHealthData = state.getHealthData.bind(state);
    vi.spyOn(state, "getHealthData").mockImplementation(async (...args) => {
      events.push("health:start");
      const data = await getHealthData(...args);
      events.push(`health:end:isEmpty=${String(data?.isEmpty)}`);
      return data;
    });

    // One refresh, exactly as the webview sends it.
    const panel = capturedPanels.at(-1);
    expect(panel).toBeDefined();
    await panel!.dispatchMessage({ type: "refresh" });

    // The health computation must start only after the history load ended.
    const loadEnd = events.findIndex((e) => e.startsWith("historyLoad:end"));
    const healthStart = events.indexOf("health:start");
    expect(loadEnd, events.join(" -> ")).toBeGreaterThanOrEqual(0);
    expect(healthStart, events.join(" -> ")).toBeGreaterThan(loadEnd);

    expect(internals.healthWidgetData?.isEmpty, events.join(" -> ")).toBe(false);
    const overview = renderedText(tabPanelHtml(renderDashboardHtml(dashboard), "overview"));
    expect(overview).not.toContain(EMPTY_HEALTH);
  });

  it("re-computes health when the background history load finds runs", async () => {
    // The history is already on disk when the Dashboard is built, but the
    // panel's own health computation on show() can still beat the
    // constructor's background load. When that load lands, the widget must be
    // recomputed rather than left on the empty placeholder until a refresh.
    writeHistory(workspaceRoot);
    const store = new TelemetryStore(workspaceRoot);
    // Hold the constructor's load until show() has computed health.
    let release!: () => void;
    const gate = new Promise<void>((resolve) => (release = resolve));
    const getAll = store.getAllRunSummaries.bind(store);
    vi.spyOn(store, "getAllRunSummaries").mockImplementation(async () => {
      await gate;
      return getAll();
    });

    dashboard = new Dashboard(
      { fsPath: "/mock/extension" } as never,
      createMockMemento(),
      workspaceRoot,
      store
    );
    const internals = dashboard as unknown as DashboardInternals;
    dashboard.show();
    await dashboard.refreshHealthWidgetData();
    expect(internals.healthWidgetData?.isEmpty).toBe(true);

    release();
    // No refresh is sent: the background load landing must be enough.
    await vi.waitFor(() => expect(internals.healthWidgetData?.isEmpty).toBe(false), {
      timeout: 3_000,
    });

    expect(internals.state.getHistory().length).toBeGreaterThan(0);
  });
});
