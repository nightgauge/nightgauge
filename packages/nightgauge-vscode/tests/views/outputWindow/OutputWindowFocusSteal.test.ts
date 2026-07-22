/**
 * OutputWindowFocusSteal.test.ts — Issue #1403 + no-reveal-on-show fix
 *
 * Verifies that OutputWindow.show() does not steal keyboard focus or the
 * ViewColumn's active-tab slot:
 *
 *  - createWebviewPanel receives { viewColumn, preserveFocus: true }.
 *  - Calling show() a second time on an existing panel is a no-op —
 *    it does NOT call panel.reveal(), which would otherwise switch the
 *    active tab in ViewColumn.Two to the Output Window on every
 *    pipeline stage transition and interrupt the user (e.g., pulling
 *    them out of a Claude Code chat tab).
 *  - reveal() is the explicit "force to foreground" method and DOES
 *    call panel.reveal(), keeping preserveFocus=true.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { OutputWindow } from "../../../src/views/outputWindow/OutputWindow";

const mockReveal = vi.fn();
const mockPanel = {
  webview: {
    html: "",
    onDidReceiveMessage: vi.fn(() => ({ dispose: vi.fn() })),
    postMessage: vi.fn(),
    asWebviewUri: vi.fn((uri: any) => uri),
  },
  reveal: mockReveal,
  onDidDispose: vi.fn(() => ({ dispose: vi.fn() })),
  onDidChangeViewState: vi.fn(() => ({ dispose: vi.fn() })),
  dispose: vi.fn(),
  visible: true,
};

vi.mock("vscode", () => ({
  ViewColumn: { One: 1, Two: 2 },
  Uri: {
    joinPath: vi.fn(() => ({ fsPath: "/mock/path" })),
    file: vi.fn((p: string) => ({ fsPath: p })),
  },
  window: {
    createWebviewPanel: vi.fn(() => mockPanel),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  EventEmitter: class {
    event = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
  },
}));

function createMockWorkspaceState(): any {
  const storage = new Map<string, any>();
  return {
    get: vi.fn((key: string) => storage.get(key)),
    update: vi.fn((key: string, value: any) => {
      storage.set(key, value);
      return Promise.resolve();
    }),
  };
}

describe("OutputWindow focus-steal fix (Issue #1403)", () => {
  let outputWindow: OutputWindow;

  beforeEach(async () => {
    vi.clearAllMocks();
    // Reset onDidDispose to a no-op so show() doesn't crash
    mockPanel.onDidDispose.mockReturnValue({ dispose: vi.fn() });
    mockPanel.onDidChangeViewState.mockReturnValue({ dispose: vi.fn() });
    mockPanel.webview.onDidReceiveMessage.mockReturnValue({ dispose: vi.fn() });

    const extensionUri = { fsPath: "/mock/ext" } as any;
    const workspaceState = createMockWorkspaceState();
    outputWindow = new OutputWindow(extensionUri, workspaceState);
  });

  it("createWebviewPanel receives preserveFocus: true on first show()", async () => {
    const vscode = await import("vscode");

    outputWindow.show();

    expect(vscode.window.createWebviewPanel).toHaveBeenCalledWith(
      "incrediOutputWindow",
      "Nightgauge Output",
      expect.objectContaining({ viewColumn: 2, preserveFocus: true }),
      expect.objectContaining({ enableScripts: true })
    );
  });

  it("show() on an existing panel is a no-op — does NOT call panel.reveal()", () => {
    outputWindow.show(); // creates panel

    mockReveal.mockClear();
    outputWindow.show(); // should be a no-op now — no reveal

    expect(mockReveal).not.toHaveBeenCalled();
  });

  it("show() called many times in a row never reveals (automated stage transitions)", async () => {
    const vscode = await import("vscode");

    outputWindow.show(); // creates panel
    mockReveal.mockClear();
    (vscode.window.createWebviewPanel as any).mockClear();

    // Simulate many stage transitions in rapid succession — this is
    // the exact scenario that previously stole the user's active tab
    // over and over.
    for (let i = 0; i < 10; i++) {
      outputWindow.show();
    }

    expect(mockReveal).not.toHaveBeenCalled();
    expect(vscode.window.createWebviewPanel).not.toHaveBeenCalled();
  });

  it("reveal() on an existing panel DOES call panel.reveal() with preserveFocus=true", () => {
    outputWindow.show(); // creates panel

    mockReveal.mockClear();
    outputWindow.reveal(); // explicit user-intent reveal

    expect(mockReveal).toHaveBeenCalledTimes(1);
    expect(mockReveal).toHaveBeenCalledWith(2, true);
  });

  it("reveal() on a fresh instance creates the panel and then reveals it", async () => {
    const vscode = await import("vscode");

    outputWindow.reveal(); // no panel yet — should create, then reveal

    expect(vscode.window.createWebviewPanel).toHaveBeenCalledTimes(1);
    expect(mockReveal).toHaveBeenCalledWith(2, true);
  });
});
