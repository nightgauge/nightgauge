/**
 * Closing the Dashboard tab must not disable it for the session (#809).
 *
 * `handlePanelClosed()` was bound to `panel.onDidDispose` and called the
 * service-lifetime `dispose()`, which tore down the diagnostic logger's
 * OutputChannel, the recommendation applier and the IncrediYaml service.
 * Dashboard is a singleton captured by the `nightgauge.showDashboard` closure,
 * so the next invocation ran `show()` against a gutted object and VS Code
 * surfaced `Error running command nightgauge.showDashboard: Channel has been
 * closed.` Only an extension host restart recovered.
 *
 * The OutputChannel stub below throws that exact message once disposed, so
 * these tests reproduce the reported failure rather than asserting about
 * bookkeeping that merely correlates with it.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";

vi.mock("../../../src/views/dashboard/DashboardHtml", () => ({
  getDashboardHtml: vi.fn().mockReturnValue("<html></html>"),
  getPipelineProgressSectionHtml: vi.fn().mockReturnValue(""),
  getSummaryCardsSectionHtml: vi.fn().mockReturnValue(""),
  getAnalyticsSectionHtml: vi.fn().mockReturnValue(""),
  getPipelineSlotsSectionHtml: vi.fn().mockReturnValue(""),
}));

/** Every OutputChannel handed out, so a test can inspect what was disposed. */
const channels: {
  name: string;
  disposed: boolean;
  appendLine: (line: string) => void;
  dispose: () => void;
}[] = [];

/** Panels created by `show()`, with their captured lifecycle handlers. */
interface FakePanel {
  onDidDisposeHandler: (() => void) | null;
  messageSubscriptionDisposed: boolean;
  disposed: boolean;
}
const panels: FakePanel[] = [];

/**
 * Push a disposable onto the array VS Code was given, exactly as the real API
 * does. Which array each subscription lands in IS the fix, so a mock that
 * ignored the third argument would make these tests vacuous.
 */
function register(store: unknown, disposable: { dispose: () => void }) {
  if (Array.isArray(store)) {
    store.push(disposable);
  }
  return disposable;
}

vi.mock("vscode", () => ({
  EventEmitter: class EventEmitter {
    private listeners: ((data: unknown) => void)[] = [];
    get event() {
      return (listener: (data: unknown) => void) => {
        this.listeners.push(listener);
        return { dispose: () => {} };
      };
    }
    fire(data: unknown) {
      this.listeners.forEach((l) => l(data));
    }
    dispose = vi.fn();
  },
  Uri: {
    joinPath: vi.fn((_uri: unknown, ...segments: string[]) => ({
      fsPath: `/mock/path/${segments.join("/")}`,
    })),
    file: vi.fn((p: string) => ({ fsPath: p })),
  },
  ViewColumn: { One: 1 },
  window: {
    createWebviewPanel: vi.fn(() => {
      const panel: FakePanel = {
        onDidDisposeHandler: null,
        messageSubscriptionDisposed: false,
        disposed: false,
      };
      panels.push(panel);
      return {
        webview: {
          html: "",
          onDidReceiveMessage: vi.fn((_l: unknown, _t: unknown, store: unknown) =>
            register(store, {
              dispose: () => {
                panel.messageSubscriptionDisposed = true;
              },
            })
          ),
          postMessage: vi.fn(),
        },
        reveal: vi.fn(),
        onDidDispose: vi.fn((listener: () => void, _t: unknown, store: unknown) => {
          panel.onDidDisposeHandler = listener;
          return register(store, { dispose: vi.fn() });
        }),
        dispose: vi.fn(() => {
          panel.disposed = true;
          panel.onDidDisposeHandler?.();
        }),
        visible: true,
      };
    }),
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
    showSaveDialog: vi.fn(),
    createOutputChannel: vi.fn((name: string) => {
      const channel = {
        name,
        disposed: false,
        appendLine: (_line: string) => {
          // The reported symptom, verbatim.
          if (channel.disposed) {
            throw new Error("Channel has been closed.");
          }
        },
        append: (_s: string) => {
          if (channel.disposed) {
            throw new Error("Channel has been closed.");
          }
        },
        show: vi.fn(),
        clear: vi.fn(),
        dispose: () => {
          channel.disposed = true;
        },
      };
      channels.push(channel);
      return channel;
    }),
  },
  workspace: {
    getConfiguration: vi.fn(() => ({ get: vi.fn().mockReturnValue(undefined) })),
    fs: { writeFile: vi.fn().mockResolvedValue(undefined) },
    createFileSystemWatcher: vi.fn(() => ({
      onDidChange: vi.fn(() => ({ dispose: vi.fn() })),
      onDidCreate: vi.fn(() => ({ dispose: vi.fn() })),
      onDidDelete: vi.fn(() => ({ dispose: vi.fn() })),
      dispose: vi.fn(),
    })),
  },
  RelativePattern: vi.fn(),
}));

import { Dashboard } from "../../../src/views/dashboard/Dashboard";

describe("Dashboard — close and reopen (#809)", () => {
  let dashboard: Dashboard;
  const workspaceRoot = "/test/workspace";
  const extensionUri = { fsPath: "/mock/extension" } as vscode.Uri;

  const anyChannelDisposed = () => channels.some((c) => c.disposed);
  /** Exercise the logger the way a render does — throws if the channel died. */
  const logSomething = () => channels.forEach((c) => c.appendLine("probe"));

  beforeEach(() => {
    vi.clearAllMocks();
    channels.length = 0;
    panels.length = 0;
    dashboard = new Dashboard(extensionUri, createMockMemento(), workspaceRoot);
  });

  afterEach(() => {
    dashboard.dispose();
  });

  it("reopens after the tab is closed, three times over", () => {
    for (let cycle = 1; cycle <= 3; cycle += 1) {
      expect(() => dashboard.show()).not.toThrow();
      expect(panels).toHaveLength(cycle);

      // The user closes the tab: VS Code fires onDidDispose.
      panels[cycle - 1].onDidDisposeHandler?.();

      // Probe the logger BEFORE the bookkeeping assertion, so the pre-fix
      // code fails with the reported error itself rather than with a flag
      // that merely correlates with it.
      expect(() => logSomething()).not.toThrow();
      expect(anyChannelDisposed()).toBe(false);
    }
  });

  it("does not dispose service-scoped resources when the panel closes", () => {
    dashboard.show();
    const openedChannels = channels.length;
    expect(openedChannels).toBeGreaterThan(0);

    panels[0].onDidDisposeHandler?.();

    expect(anyChannelDisposed()).toBe(false);
    expect(channels).toHaveLength(openedChannels);
  });

  it("does release the panel's own webview message subscription", () => {
    dashboard.show();
    expect(panels[0].messageSubscriptionDisposed).toBe(false);

    panels[0].onDidDisposeHandler?.();

    expect(panels[0].messageSubscriptionDisposed).toBe(true);
  });

  it("leaks no panel subscription across repeated cycles", () => {
    for (let cycle = 0; cycle < 3; cycle += 1) {
      dashboard.show();
      panels[cycle].onDidDisposeHandler?.();
    }

    // Every panel's subscription was released with its own panel — none
    // carried over into the next cycle's collection.
    expect(panels.map((p) => p.messageSubscriptionDisposed)).toEqual([true, true, true]);
  });

  it("leaks no timers across repeated cycles", () => {
    vi.useFakeTimers();
    try {
      const baseline = vi.getTimerCount();
      for (let cycle = 0; cycle < 3; cycle += 1) {
        dashboard.show();
        panels[cycle].onDidDisposeHandler?.();
      }
      expect(vi.getTimerCount()).toBe(baseline);
    } finally {
      vi.useRealTimers();
    }
  });

  it("still tears everything down on the extension's own dispose", () => {
    dashboard.show();
    expect(anyChannelDisposed()).toBe(false);

    dashboard.dispose();

    expect(channels.every((c) => c.disposed)).toBe(true);
    // The panel itself is closed by the service teardown, and the resulting
    // onDidDispose -> handlePanelClosed -> disposePanelScoped is idempotent:
    // a second dispose() must not throw.
    expect(panels[0].disposed).toBe(true);
    expect(() => dashboard.dispose()).not.toThrow();
  });
});
