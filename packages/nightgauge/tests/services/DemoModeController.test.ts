/**
 * Demo-mode awareness (#2108, ADR-026 decisions 4-6): the badge follows the
 * `ipc.ready` demo flag, `demo.uiStep` runs only allowlisted actions and only
 * in demo mode, and platform SSE and the heartbeat never start in demo mode.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as vscode from "vscode";
import {
  DEMO_CONTEXT_KEY,
  DEMO_UI_ACTIONS,
  DEMO_VIEW_TARGETS,
  DemoModeController,
  isDemoMode,
} from "../../src/services/DemoModeController";
import { AgentHeartbeatService } from "../../src/services/AgentHeartbeatService";
import { EventStreamService } from "../../src/services/EventStreamService";
import { VALID_TABS } from "../../src/views/dashboard/DashboardHtml";
import { makeMockTokenStorage } from "../mocks/token-storage";
import { makeMockLogger } from "../mocks/logger";
import { createRequire } from "node:module";

type Handler = (data: unknown) => void;

function fakeIpc(readyPayload: unknown = null) {
  const handlers = new Map<string, Handler[]>();
  const statusListeners: Array<(connected: boolean) => void> = [];
  return {
    readyPayload,
    on(event: string, handler: Handler) {
      handlers.set(event, [...(handlers.get(event) ?? []), handler]);
      return { dispose: () => {} };
    },
    onDidChangeStatus: ((listener: (connected: boolean) => void) => {
      statusListeners.push(listener);
      return { dispose: () => {} };
    }) as vscode.Event<boolean>,
    emit(event: string, data: unknown) {
      for (const h of handlers.get(event) ?? []) h(data);
    },
    disconnect() {
      for (const l of statusListeners) l(false);
    },
  };
}

function setup(readyPayload: unknown = null) {
  const ipc = fakeIpc(readyPayload);
  const badge = {
    show: vi.fn(),
    dispose: vi.fn(),
  } as unknown as vscode.StatusBarItem & { show: ReturnType<typeof vi.fn> };
  const ui = {
    showDashboard: vi.fn(),
    selectDashboardTab: vi.fn(() => true),
    revealActiveIssue: vi.fn(async () => true),
  };
  const host = {
    createStatusBarItem: vi.fn(() => badge),
    executeCommand: vi.fn(async () => undefined),
    log: vi.fn(),
    stopPlatformServices: vi.fn(),
  };
  const controller = new DemoModeController(ipc, ui, host);
  return { ipc, badge, ui, host, controller };
}

let active: DemoModeController | null = null;
afterEach(() => {
  active?.dispose();
  active = null;
});

describe("demo badge and context key", () => {
  it("shows an accessible badge and sets the context key on demo: true", () => {
    const t = setup();
    active = t.controller;
    t.ipc.emit("ipc.ready", { protocolVersion: 2, demo: true });
    expect(isDemoMode()).toBe(true);
    expect(t.badge.show).toHaveBeenCalledOnce();
    expect(t.badge.text).toContain("Demo");
    expect(t.badge.accessibilityInformation?.label).toMatch(/demo mode/i);
    expect(t.host.executeCommand).toHaveBeenCalledWith("setContext", DEMO_CONTEXT_KEY, true);
    expect(t.host.stopPlatformServices).toHaveBeenCalledOnce();
  });

  it.each([[{ protocolVersion: 2 }], [{ protocolVersion: 2, demo: "true" }], [null]])(
    "shows nothing for ipc.ready %j",
    (payload) => {
      const t = setup();
      active = t.controller;
      t.ipc.emit("ipc.ready", payload);
      expect(isDemoMode()).toBe(false);
      expect(t.host.createStatusBarItem).not.toHaveBeenCalled();
    }
  );

  it("clears the badge and context key on disconnect", () => {
    const t = setup();
    active = t.controller;
    t.ipc.emit("ipc.ready", { demo: true });
    t.ipc.disconnect();
    expect(isDemoMode()).toBe(false);
    expect(t.badge.dispose).toHaveBeenCalledOnce();
    expect(t.host.executeCommand).toHaveBeenLastCalledWith("setContext", DEMO_CONTEXT_KEY, false);
  });

  it("clears when a non-demo daemon reconnects", () => {
    const t = setup();
    active = t.controller;
    t.ipc.emit("ipc.ready", { demo: true });
    t.ipc.emit("ipc.ready", { protocolVersion: 2 });
    expect(isDemoMode()).toBe(false);
    expect(t.badge.dispose).toHaveBeenCalledOnce();
  });

  it("picks up a handshake that landed before it was constructed", () => {
    const t = setup({ protocolVersion: 2, demo: true });
    active = t.controller;
    expect(isDemoMode()).toBe(true);
    expect(t.badge.show).toHaveBeenCalledOnce();
  });
});

describe("demo.uiStep", () => {
  let t: ReturnType<typeof setup>;
  beforeEach(() => {
    t = setup();
    active = t.controller;
  });

  it("does nothing unless the daemon announced demo mode", async () => {
    expect(await t.controller.handleUiStep({ command: "dashboard.open" })).toBe(false);
    t.ipc.emit("demo.uiStep", { command: "view.focus", args: { target: "pipeline-tree" } });
    await Promise.resolve();
    expect(t.ui.showDashboard).not.toHaveBeenCalled();
    expect(t.host.executeCommand).not.toHaveBeenCalled();
    expect(t.host.log).toHaveBeenCalledWith(expect.stringMatching(/not in demo mode/));
  });

  it("runs each allowlisted action", async () => {
    t.ipc.emit("ipc.ready", { demo: true });
    expect(await t.controller.handleUiStep({ command: "dashboard.open" })).toBe(true);
    expect(t.ui.showDashboard).toHaveBeenCalled();
    expect(
      await t.controller.handleUiStep({ command: "dashboard.tab", args: { target: "history" } })
    ).toBe(true);
    expect(t.ui.selectDashboardTab).toHaveBeenCalledWith("history");
    expect(
      await t.controller.handleUiStep({ command: "view.focus", args: { target: "attention" } })
    ).toBe(true);
    expect(t.host.executeCommand).toHaveBeenCalledWith("nightgauge.attentionView.focus");
    expect(await t.controller.handleUiStep({ command: "pipeline.expandActiveIssue" })).toBe(true);
    expect(t.ui.revealActiveIssue).toHaveBeenCalled();
  });

  it.each([
    [{ command: "workbench.action.terminal.new" }],
    [{ command: "nightgauge.showDashboard" }],
    [{ command: "dashboard.tab", args: { target: "settings" } }],
    [{ command: "view.focus", args: { target: "workbench.view.scm" } }],
    [{}],
  ])("rejects and logs %j", async (step) => {
    t.ipc.emit("ipc.ready", { demo: true });
    t.host.executeCommand.mockClear();
    expect(await t.controller.handleUiStep(step)).toBe(false);
    expect(t.host.executeCommand).not.toHaveBeenCalled();
    expect(t.ui.showDashboard).not.toHaveBeenCalled();
    expect(t.host.log).toHaveBeenCalledWith(expect.stringMatching(/^demo\.uiStep dropped/));
  });
});

describe("platform services in demo mode", () => {
  it("does not start the agent heartbeat", () => {
    vi.useFakeTimers();
    const fetchSpy = vi.fn();
    vi.stubGlobal("fetch", fetchSpy);
    try {
      const t = setup();
      active = t.controller;
      t.ipc.emit("ipc.ready", { demo: true });
      const heartbeat = new AgentHeartbeatService(
        () => "https://platform.invalid",
        makeMockTokenStorage(),
        makeMockLogger()
      );
      heartbeat.start("agent-1");
      vi.advanceTimersByTime(120_000);
      expect(fetchSpy).not.toHaveBeenCalled();
      heartbeat.dispose();
    } finally {
      vi.unstubAllGlobals();
      vi.useRealTimers();
    }
  });

  it("does not open the platform SSE stream", () => {
    const t = setup();
    active = t.controller;
    t.ipc.emit("ipc.ready", { demo: true });
    const service = EventStreamService.getInstance({
      context: { globalState: { get: vi.fn(), update: vi.fn() } } as never,
      logger: makeMockLogger() as never,
      tokenRefreshManager: { forceRefresh: vi.fn() } as never,
    });
    const sse = (service as unknown as { _sseClient: { connect: unknown; reconnect: unknown } })
      ._sseClient;
    const connect = vi.spyOn(sse as never, "connect" as never);
    const reconnect = vi.spyOn(sse as never, "reconnect" as never);
    service.connect("https://platform.invalid", "t");
    service.reconnect("https://platform.invalid", "t");
    expect(connect).not.toHaveBeenCalled();
    expect(reconnect).not.toHaveBeenCalled();
    EventStreamService.resetInstance();
  });
});

describe("allowlist agrees with the demo scenario validator", () => {
  const { UI_ACTIONS } = createRequire(__filename)("../../demo/daemon/scenario.cjs") as {
    UI_ACTIONS: Record<string, string[] | null>;
  };

  it("has the same actions and targets", () => {
    expect(Object.keys(UI_ACTIONS).sort()).toEqual([...DEMO_UI_ACTIONS].sort());
    expect(UI_ACTIONS["dashboard.tab"]).toEqual([...VALID_TABS]);
    expect([...(UI_ACTIONS["view.focus"] ?? [])].sort()).toEqual(
      Object.keys(DEMO_VIEW_TARGETS).sort()
    );
  });
});
