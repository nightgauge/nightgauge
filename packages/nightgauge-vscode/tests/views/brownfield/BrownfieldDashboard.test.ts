/**
 * BrownfieldDashboard.test.ts
 *
 * Unit tests for BrownfieldDashboard:
 * - Creates webview panel on show()
 * - Reveals existing panel on second show()
 * - Disposes cleanly
 * - Handles refresh message
 *
 * @see Issue #1163 - Brownfield Modernization Progress Dashboard
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { BrownfieldDashboard } from "../../../src/views/brownfield/BrownfieldDashboard";

// Mock webview panel
const mockWebviewPanel = {
  webview: {
    html: "",
    onDidReceiveMessage: vi.fn(),
  },
  reveal: vi.fn(),
  onDidDispose: vi.fn(),
  dispose: vi.fn(),
  visible: true,
};

vi.mock("vscode", () => ({
  ViewColumn: { One: 1 },
  Uri: {
    joinPath: vi.fn(() => ({ fsPath: "/mock/path" })),
  },
  window: {
    createWebviewPanel: vi.fn(() => mockWebviewPanel),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  EventEmitter: class {
    private listeners: Function[] = [];
    event = (listener: Function) => {
      this.listeners.push(listener);
      return { dispose: () => {} };
    };
    fire() {
      this.listeners.forEach((l) => l());
    }
    dispose() {}
  },
  RelativePattern: class {
    constructor(
      public base: string,
      public pattern: string
    ) {}
  },
  workspace: {
    createFileSystemWatcher: vi.fn(() => ({
      onDidCreate: vi.fn(),
      onDidChange: vi.fn(),
      onDidDelete: vi.fn(),
      dispose: vi.fn(),
    })),
  },
}));

// Mock fs
vi.mock("node:fs/promises", () => ({
  default: {
    readFile: vi.fn(async () => {
      throw new Error("ENOENT");
    }),
    writeFile: vi.fn(async () => {}),
    mkdir: vi.fn(async () => {}),
  },
  readFile: vi.fn(async () => {
    throw new Error("ENOENT");
  }),
  writeFile: vi.fn(async () => {}),
  mkdir: vi.fn(async () => {}),
}));

// Create mock data service
function createMockDataService() {
  return {
    loadAll: vi.fn(async () => ({
      health: null,
      security: null,
      plan: null,
      deps: null,
      history: [],
      hasAnyData: false,
    })),
    loadHealth: vi.fn(),
    loadSecurity: vi.fn(),
    loadPlan: vi.fn(),
    loadDeps: vi.fn(),
    loadHistory: vi.fn(async () => []),
    onDataChanged: vi.fn(() => ({ dispose: () => {} })),
    dispose: vi.fn(),
  } as any;
}

describe("BrownfieldDashboard", () => {
  let dashboard: BrownfieldDashboard;
  let mockDataService: any;
  const extensionUri = { fsPath: "/ext" } as any;

  beforeEach(() => {
    vi.clearAllMocks();
    mockWebviewPanel.webview.html = "";
    mockWebviewPanel.reveal.mockClear();

    mockDataService = createMockDataService();
    dashboard = new BrownfieldDashboard(extensionUri, mockDataService);
  });

  it("creates webview panel on first show()", async () => {
    const vscode = await import("vscode");

    dashboard.show();

    expect(vscode.window.createWebviewPanel).toHaveBeenCalledWith(
      "incrediBrownfieldDashboard",
      "Brownfield Modernization Dashboard",
      1,
      expect.objectContaining({
        enableScripts: true,
        retainContextWhenHidden: true,
      })
    );
  });

  it("reveals existing panel on second show()", async () => {
    dashboard.show();

    // Second call should reveal, not create
    dashboard.show();

    expect(mockWebviewPanel.reveal).toHaveBeenCalled();
  });

  it("disposes cleanly", () => {
    dashboard.show();
    expect(() => dashboard.dispose()).not.toThrow();
  });

  it("reports isVisible correctly", () => {
    // Before show
    expect(dashboard.isVisible()).toBe(false);

    // After show
    dashboard.show();
    expect(dashboard.isVisible()).toBe(true);
  });

  it("sets HTML content after rendering", async () => {
    dashboard.show();

    // Wait for debounce + async render
    await new Promise((r) => setTimeout(r, 200));

    // Panel should have HTML set (empty state since no data)
    expect(mockWebviewPanel.webview.html).toContain("<!DOCTYPE html>");
  });
});
