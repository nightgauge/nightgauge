/**
 * The Overview's board summary after an early open (#2287).
 *
 * A board refresh that runs before the project config resolves reads no
 * board. It used to be recorded as a loaded board of 0/0/0/0, and nothing
 * read the board again: re-showing the dashboard only reveals the panel. Now
 * the summary stays loading until the config resolves, and the
 * config-resolved event reads the real counts without a manual refresh.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createMockMemento } from "../../mocks/memento";

vi.mock("vscode", async () => (await import("../../arrival/dashboardHarness")).vscodeMockModule());
vi.mock("../../../src/services/IpcClient", async () =>
  (await import("../../arrival/dashboardHarness")).ipcClientMockModule()
);
vi.mock("../../../src/platform/TokenStorage", async () =>
  (await import("../../arrival/dashboardHarness")).tokenStorageMockModule()
);
vi.mock("../../../src/services/PipelineStateService", async () =>
  (await import("../../arrival/dashboardHarness")).pipelineStateServiceMockModule()
);
vi.mock("../../../src/services/WorkspaceManager", async () =>
  (await import("../../arrival/dashboardHarness")).workspaceManagerMockModule()
);
vi.mock(
  "../../../src/services/SanitizationLogService",
  async () =>
    await (await import("../../arrival/dashboardHarness")).sanitizationLogServiceMockModule()
);
vi.mock("../../../src/services/ProjectBoardService", async () =>
  (await import("../../arrival/dashboardHarness")).projectBoardServiceMockModule()
);
vi.mock("../../../src/services/ProjectIterationService", async () =>
  (await import("../../arrival/dashboardHarness")).projectIterationServiceMockModule()
);

import { Dashboard } from "../../../src/views/dashboard/Dashboard";
import { ProjectBoardService } from "../../../src/services/ProjectBoardService";
import type { ProjectBoardData } from "../../../src/views/dashboard/ProjectBoardTypes";
import {
  renderDashboardHtml,
  renderedText,
  resetHarness,
  tabPanelHtml,
} from "../../arrival/dashboardHarness";

/** The board the config names, once it resolves: one issue per column. */
const BOARD: Record<string, Array<{ number: number; title: string }>> = {
  Ready: [{ number: 11, title: "Ready issue" }],
  "In progress": [{ number: 12, title: "Running issue" }],
  "In review": [{ number: 13, title: "Reviewed issue" }],
  Done: [{ number: 14, title: "Finished issue" }],
  Backlog: [],
};

/**
 * A board service whose config resolves when the test says so. Each prefetch
 * records its skip reason when it starts, as the real service does after its
 * config load, and can be held open to resolve the config mid-flight.
 */
function boardService() {
  const svc = new (ProjectBoardService as unknown as new () => Record<string, unknown>)();
  const listeners: Array<() => void> = [];
  const board = {
    resolved: false,
    skip: null as string | null,
    hold: null as Promise<void> | null,
  };
  Object.assign(svc, {
    prefetchAllItems: vi.fn(async () => {
      board.skip = board.resolved ? null : "config-unresolved";
      if (board.hold) await board.hold;
    }),
    getLastPrefetchError: () => null,
    getLastPrefetchSkip: () => board.skip,
    getLastPrefetchDiagnostics: () => null,
    getItemsByStatusFromCache: async (status: string) =>
      board.resolved ? (BOARD[status] ?? []) : [],
    getOwner: () => (board.resolved ? "acme" : null),
    getProjectNumber: () => (board.resolved ? 7 : null),
    onDidResolveConfig: (listener: () => void) => {
      listeners.push(listener);
      return { dispose: vi.fn() };
    },
  });
  const resolve = () => {
    board.resolved = true;
    listeners.forEach((l) => l());
  };
  return { svc, board, resolve };
}

let dashboard: Dashboard;

function boardData(): ProjectBoardData | null {
  return (
    dashboard as unknown as { state: { getProjectBoardData(): ProjectBoardData | null } }
  ).state.getProjectBoardData();
}

/** Let the config-resolved refresh run to completion. */
async function settle(): Promise<void> {
  for (let i = 0; i < 10; i++) await new Promise((r) => setImmediate(r));
}

beforeEach(() => {
  vi.clearAllMocks();
  resetHarness();
  dashboard = new Dashboard(
    { fsPath: "/mock/extension" } as never,
    createMockMemento(),
    "/mock/workspace"
  );
  dashboard.show();
});

afterEach(async () => {
  await settle();
  dashboard.dispose();
});

describe("board summary before and after the config resolves (#2287)", () => {
  it("stays loading while the config is unresolved, then shows the real counts", async () => {
    const { svc, resolve } = boardService();
    dashboard.setProjectBoardService(svc as never);

    await dashboard.refreshProjectBoardData();
    expect(boardData()?.loadingState).toBe("loading");
    const overview = () => renderedText(tabPanelHtml(renderDashboardHtml(dashboard), "overview"));
    expect(overview()).not.toMatch(/0 Ready 0 In Progress 0 In Review 0 Done/);

    // The config resolves; no one presses refresh.
    resolve();
    await settle();

    expect(boardData()?.loadingState).toBe("loaded");
    expect(boardData()?.statusCounts).toEqual({
      ready: 1,
      inProgress: 1,
      inReview: 1,
      done: 1,
      backlog: 0,
    });
    expect(overview()).toMatch(/1 Ready 1 In Progress 1 In Review 1 Done/);
  });

  it("reads the board when the config resolves while that refresh is in flight", async () => {
    const { svc, board, resolve } = boardService();
    dashboard.setProjectBoardService(svc as never);
    let release!: () => void;
    board.hold = new Promise<void>((r) => (release = r));

    const refresh = dashboard.refreshProjectBoardData();
    await Promise.resolve();
    // The event arrives while the refresh that skipped is still running.
    resolve();
    board.hold = null;
    release();
    await refresh;
    await settle();

    expect(svc.prefetchAllItems).toHaveBeenCalledTimes(2);
    expect(boardData()?.loadingState).toBe("loaded");
    expect(boardData()?.statusCounts.ready).toBe(1);
  });

  it("does not re-read on a config event when the board was already read", async () => {
    const { svc, board, resolve } = boardService();
    board.resolved = true;
    dashboard.setProjectBoardService(svc as never);

    await dashboard.refreshProjectBoardData();
    resolve();
    await settle();

    expect(svc.prefetchAllItems).toHaveBeenCalledTimes(1);
    expect(boardData()?.loadingState).toBe("loaded");
  });

  it("shows a config with no project as not configured, not as an empty board", async () => {
    const { svc, board } = boardService();
    dashboard.setProjectBoardService(svc as never);
    (svc.prefetchAllItems as ReturnType<typeof vi.fn>).mockImplementation(async () => {
      board.skip = "not-configured";
    });

    await dashboard.refreshProjectBoardData();

    expect(boardData()?.isConfigured).toBe(false);
    expect(boardData()?.loadingState).toBe("loaded");
  });
});
