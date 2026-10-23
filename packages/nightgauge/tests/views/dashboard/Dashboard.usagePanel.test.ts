/**
 * Dashboard.usagePanel.test.ts
 *
 * Issue #661 — the end-to-end path the panel depends on:
 *
 *   AdapterUsageService.onDidChangeUsage → Dashboard → rendered webview HTML
 *
 * A panel that derives correctly but never re-renders satisfies no AC, so the
 * change-event wiring is exercised here rather than asserted structurally.
 *
 * @see docs/decisions/018-adapter-usage-quota-model.md
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";
import type { UsageSnapshot } from "../../../src/services/usage/types";

let lastCreatedPanel: any;

vi.mock("vscode", () => ({
  EventEmitter: class EventEmitter {
    private listeners: ((data: any) => void)[] = [];
    get event() {
      return (listener: (data: any) => void) => {
        this.listeners.push(listener);
        return { dispose: () => {} };
      };
    }
    fire(data: any) {
      this.listeners.forEach((l) => l(data));
    }
    dispose = vi.fn();
  },
  Uri: {
    joinPath: vi.fn((_uri: any, ...segments: string[]) => ({
      fsPath: `/mock/path/${segments.join("/")}`,
    })),
    file: vi.fn((p: string) => ({ fsPath: p })),
  },
  ViewColumn: { One: 1 },
  window: {
    createWebviewPanel: vi.fn(() => {
      lastCreatedPanel = {
        webview: {
          html: "",
          cspSource: "vscode-webview:",
          onDidReceiveMessage: vi.fn(() => ({ dispose: vi.fn() })),
          postMessage: vi.fn(),
        },
        reveal: vi.fn(),
        onDidDispose: vi.fn(() => ({ dispose: vi.fn() })),
        dispose: vi.fn(),
        visible: true,
      };
      return lastCreatedPanel;
    }),
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
    showSaveDialog: vi.fn(),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
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

// Imported after the vscode mock is registered.
import { Dashboard } from "../../../src/views/dashboard/Dashboard";

const NOW = new Date("2026-08-18T12:00:00.000Z");
const DAY_MS = 24 * 60 * 60 * 1000;

function snapshotWithMonthlyUsed(used: number): UsageSnapshot {
  return {
    adapter: "claude",
    plan: { kind: "pay-per-token" },
    capturedAt: NOW,
    windows: [
      {
        id: "local-telemetry:monthly",
        label: "This month",
        scope: "monthly",
        used,
        limit: 100,
        unit: "usd",
        resetsAt: new Date("2026-09-01T00:00:00.000Z"),
        confidence: "measured",
      },
    ],
  };
}

/** A history entry as it is persisted in workspace state. */
function serializedRun(issueNumber: number, costUsd: number, startedAt: Date) {
  return {
    issueNumber,
    title: `Run ${issueNumber}`,
    branch: "feat/x",
    startedAt: startedAt.toISOString(),
    completedAt: startedAt.toISOString(),
    status: "complete",
    stages: [],
    toolCalls: [],
    usage: {
      inputTokens: 1000,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd,
      durationMs: 1000,
      stageCount: 1,
    },
  };
}

describe("Dashboard usage panel wiring (Issue #661)", () => {
  let dashboard: Dashboard;
  let workspaceState: vscode.Memento;
  let listeners: ((snapshot: UsageSnapshot) => void)[];
  let cachedSnapshot: UsageSnapshot | null;
  let fakeUsageService: any;
  const mockExtensionUri = { fsPath: "/mock/extension" } as vscode.Uri;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    vi.setSystemTime(NOW);
    listeners = [];
    cachedSnapshot = snapshotWithMonthlyUsed(20);
    fakeUsageService = {
      getCachedSnapshot: () => cachedSnapshot,
      onDidChangeUsage: (listener: (snapshot: UsageSnapshot) => void) => {
        listeners.push(listener);
        return { dispose: vi.fn() };
      },
    };
    workspaceState = createMockMemento(
      new Map<string, unknown>([
        [
          "nightgauge.dashboard.history",
          [
            serializedRun(1, 999, new Date(NOW.getTime() - 2 * DAY_MS)),
            serializedRun(2, 999, new Date(NOW.getTime() - 1 * DAY_MS)),
          ],
        ],
      ])
    );
    dashboard = new Dashboard(mockExtensionUri, workspaceState);
  });

  afterEach(() => {
    dashboard.dispose();
    vi.useRealTimers();
  });

  function renderedHtml(): string {
    vi.advanceTimersByTime(Dashboard.getDebounceMs() + 50);
    return lastCreatedPanel.webview.html as string;
  }

  it("renders the cached snapshot as soon as the dashboard is opened", () => {
    dashboard.setAdapterUsageService(fakeUsageService);
    dashboard.show();

    expect(renderedHtml()).toContain("$20.00 of $100.00");
  });

  it("re-renders on the service's change event, with no manual refresh", () => {
    dashboard.setAdapterUsageService(fakeUsageService);
    dashboard.show();
    expect(renderedHtml()).toContain("$20.00 of $100.00");

    expect(listeners).toHaveLength(1);
    listeners[0](snapshotWithMonthlyUsed(60));

    const html = renderedHtml();
    expect(html).toContain("$60.00 of $100.00");
    expect(html).not.toContain("$20.00 of $100.00");
  });

  it("takes quota figures from the snapshot, never from run history", () => {
    dashboard.setAdapterUsageService(fakeUsageService);
    dashboard.show();

    // History carries $999 runs; the window says $20. The window wins —
    // there is no second aggregation path (epic #657 / ADR 018).
    const html = renderedHtml();
    expect(html).toContain("$20.00 of $100.00");
    expect(html).not.toContain("$999.00 of $100.00");
  });

  it("derives the burn rate and history strip from DashboardState's history", () => {
    dashboard.setAdapterUsageService(fakeUsageService);
    dashboard.show();

    const html = renderedHtml();
    expect(html).toContain("2 runs in the last 7 days");
    expect(html).toContain("#2 Run 2");
    expect(html).toContain("$1998.00");
  });

  it("omits the panel entirely when no usage service is wired", () => {
    dashboard.show();

    expect(renderedHtml()).not.toContain("Adapter Usage &amp; Quota");
  });

  it("does not subscribe twice when re-wired with the same service instance", () => {
    dashboard.setAdapterUsageService(fakeUsageService);
    dashboard.setAdapterUsageService(fakeUsageService);

    expect(listeners).toHaveLength(1);
  });
});
