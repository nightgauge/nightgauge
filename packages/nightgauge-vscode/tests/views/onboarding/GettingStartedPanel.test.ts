/**
 * GettingStartedPanel lifecycle tests (#4155): singleton reuse, dispatching
 * webview button clicks to the injected action callback, ignoring malformed
 * messages, and dispose resetting the singleton. Mirrors the
 * AdapterDoctorPanel test pattern.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { GettingStartedAction } from "../../../src/views/onboarding/GettingStartedPanel";

let capturedMessageHandler: ((msg: any) => void) | null;
let capturedDisposeHandler: (() => void) | null;
let createWebviewPanelMock: ReturnType<typeof vi.fn>;

function buildMockPanel() {
  capturedMessageHandler = null;
  capturedDisposeHandler = null;
  return {
    webview: {
      html: "",
      cspSource: "test-csp",
      onDidReceiveMessage: vi.fn((handler: (msg: any) => void) => {
        capturedMessageHandler = handler;
        return { dispose: vi.fn() };
      }),
      postMessage: vi.fn(),
    },
    reveal: vi.fn(),
    onDidDispose: vi.fn((handler: () => void) => {
      capturedDisposeHandler = handler;
      return { dispose: vi.fn() };
    }),
    dispose: vi.fn(),
  };
}

vi.mock("vscode", () => ({
  ViewColumn: { One: 1 },
  window: {
    createWebviewPanel: vi.fn(),
  },
}));

let GettingStartedPanel: typeof import("../../../src/views/onboarding/GettingStartedPanel").GettingStartedPanel;

beforeEach(async () => {
  const vscode = await import("vscode");
  createWebviewPanelMock = vscode.window.createWebviewPanel as any;
  createWebviewPanelMock.mockReset();
  ({ GettingStartedPanel } = await import("../../../src/views/onboarding/GettingStartedPanel"));
});

afterEach(() => {
  // Reset the singleton so each test starts clean.
  GettingStartedPanel.current?.dispose();
});

describe("GettingStartedPanel (#4155)", () => {
  it("creates a webview with the onboarding steps on first show", () => {
    const panel = buildMockPanel();
    createWebviewPanelMock.mockReturnValue(panel);

    GettingStartedPanel.show(vi.fn());

    expect(createWebviewPanelMock).toHaveBeenCalledTimes(1);
    expect(panel.webview.html).toContain("Welcome to Nightgauge");
    expect(panel.webview.html).toContain("Content-Security-Policy");
  });

  it("reuses the singleton panel and swaps the action callback on a second show", () => {
    const panel = buildMockPanel();
    createWebviewPanelMock.mockReturnValue(panel);

    const actionA = vi.fn();
    const actionB = vi.fn();
    GettingStartedPanel.show(actionA);
    GettingStartedPanel.show(actionB);

    // Only one webview ever created (singleton); the second show revealed it.
    expect(createWebviewPanelMock).toHaveBeenCalledTimes(1);
    expect(panel.reveal).toHaveBeenCalled();

    // A message now dispatches to the LATEST callback (actionB), not actionA.
    capturedMessageHandler!({ type: "action", action: "pickup" });
    expect(actionB).toHaveBeenCalledWith("pickup");
    expect(actionA).not.toHaveBeenCalled();
  });

  it.each(["init", "pickup", "docs"] satisfies GettingStartedAction[])(
    "dispatches the %s action from a button click",
    (action) => {
      const panel = buildMockPanel();
      createWebviewPanelMock.mockReturnValue(panel);
      const onAction = vi.fn();
      GettingStartedPanel.show(onAction);

      capturedMessageHandler!({ type: "action", action });

      expect(onAction).toHaveBeenCalledWith(action);
    }
  );

  it("ignores messages with an unknown action", () => {
    const panel = buildMockPanel();
    createWebviewPanelMock.mockReturnValue(panel);
    const onAction = vi.fn();
    GettingStartedPanel.show(onAction);

    capturedMessageHandler!({ type: "action", action: "not-a-real-action" });

    expect(onAction).not.toHaveBeenCalled();
  });

  it("ignores non-action messages and malformed payloads", () => {
    const panel = buildMockPanel();
    createWebviewPanelMock.mockReturnValue(panel);
    const onAction = vi.fn();
    GettingStartedPanel.show(onAction);

    capturedMessageHandler!({ type: "something-else", action: "init" });
    capturedMessageHandler!(undefined);
    capturedMessageHandler!("not an object");

    expect(onAction).not.toHaveBeenCalled();
  });

  it("resets the singleton on dispose so a later show builds a fresh panel", () => {
    const first = buildMockPanel();
    createWebviewPanelMock.mockReturnValueOnce(first);
    GettingStartedPanel.show(vi.fn());
    expect(GettingStartedPanel.current).toBeDefined();

    capturedDisposeHandler!(); // user closed the panel
    expect(GettingStartedPanel.current).toBeUndefined();

    const second = buildMockPanel();
    createWebviewPanelMock.mockReturnValueOnce(second);
    GettingStartedPanel.show(vi.fn());
    expect(createWebviewPanelMock).toHaveBeenCalledTimes(2);
  });
});
